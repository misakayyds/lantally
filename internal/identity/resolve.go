// Package identity resolves device observations conservatively within a site.
package identity

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/protocol"
)

type EvidenceRank int

const (
	RankPinned EvidenceRank = iota
	RankStableMAC
	RankDHCPNeigh
	RankScopedIP
)

var (
	ErrPinnedConflict      = errors.New("conflicting user pins")
	ErrIdentityNotFound    = errors.New("identity not found")
	ErrConflictingEvidence = errors.New("conflicting identity evidence")
	ErrResolverNotConfigured = errors.New("identity resolver not configured")
)

// DeviceDelta is the observation payload consumed by Resolve.
type DeviceDelta = protocol.DeviceDelta

var defaultResolver *Resolver

// SetResolver configures the package-level Resolve, Merge, and Unmerge helpers.
func SetResolver(r *Resolver) {
	defaultResolver = r
}

type Evidence struct {
	Rank  EvidenceRank
	Value string
}

type Store interface {
	ResolveIdentityAtomic(
		siteID, nodeID string,
		evidence []Evidence,
		limitation string,
		now time.Time,
	) (deviceID string, resolvedLimitation string, err error)
	CanonicalIdentity(deviceID string) (string, error)
	IdentityPins(deviceID string) ([]string, error)
	PinIdentity(siteID, deviceID, pin string, now time.Time) error
	MergeIdentities(a, b string, rank int, value string, now time.Time) error
	UnmergeIdentity(deviceID string) error
}

type Resolver struct {
	store  Store
	nodeID string
}

const scopedIPWindow = time.Hour

func NewResolver(store Store, nodeID string) *Resolver {
	return &Resolver{store: store, nodeID: nodeID}
}

func Resolve(
	siteID string,
	obs DeviceDelta,
	now time.Time,
) (deviceID string, limitation string, err error) {
	if defaultResolver == nil {
		return "", "", ErrResolverNotConfigured
	}
	return defaultResolver.Resolve(siteID, obs, now)
}

func Merge(a, b string, evidence Evidence) error {
	if defaultResolver == nil {
		return ErrResolverNotConfigured
	}
	return defaultResolver.Merge(a, b, evidence)
}

func Unmerge(deviceID string) error {
	if defaultResolver == nil {
		return ErrResolverNotConfigured
	}
	return defaultResolver.Unmerge(deviceID)
}

func (r *Resolver) Resolve(
	siteID string,
	obs DeviceDelta,
	now time.Time,
) (deviceID string, limitation string, err error) {
	evidence, limitation, err := buildEvidence(r.nodeID, obs, now)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(siteID) == "" {
		return "", "", errors.New("site ID is required")
	}
	if strings.TrimSpace(r.nodeID) == "" {
		return "", "", errors.New("node ID is required")
	}
	return r.store.ResolveIdentityAtomic(siteID, r.nodeID, evidence, limitation, now.UTC())
}

func buildEvidence(
	nodeID string,
	obs DeviceDelta,
	now time.Time,
) (evidence []Evidence, limitation string, err error) {
	ip := net.ParseIP(strings.TrimSpace(obs.ObsIP))
	if ip == nil {
		return nil, "", fmt.Errorf("invalid observed IP %q", obs.ObsIP)
	}
	now = now.UTC()

	if strings.TrimSpace(obs.ObsMAC) != "" {
		hardwareAddr, parseErr := net.ParseMAC(obs.ObsMAC)
		if parseErr != nil {
			return nil, "", fmt.Errorf("invalid observed MAC %q: %w", obs.ObsMAC, parseErr)
		}
		mac := strings.ToLower(hardwareAddr.String())
		evidence = append(evidence, Evidence{Rank: RankStableMAC, Value: mac})
		if obs.Source == protocol.SourceNeigh {
			evidence = append(evidence, Evidence{
				Rank:  RankDHCPNeigh,
				Value: ip.String() + "|" + mac,
			})
		}
		if hardwareAddr[0]&0x02 != 0 {
			limitation = "locally administered MAC; matched by exact value only"
		}
	} else {
		evidence = append(evidence, Evidence{
			Rank:  RankScopedIP,
			Value: scopedIPValue(nodeID, ip.String(), now),
		})
		limitation = "source IP scoped to site, node, and one-hour window"
	}
	return evidence, limitation, nil
}

func (r *Resolver) Pin(siteID, deviceID, pin string) error {
	if strings.TrimSpace(pin) == "" {
		return errors.New("pin is required")
	}
	canonical, err := r.store.CanonicalIdentity(deviceID)
	if err != nil {
		return err
	}
	pins, err := r.store.IdentityPins(canonical)
	if err != nil {
		return err
	}
	if len(pins) > 0 && pins[0] != pin {
		return ErrPinnedConflict
	}
	return r.store.PinIdentity(siteID, canonical, pin, time.Now().UTC())
}

func (r *Resolver) Merge(a, b string, evidence Evidence) error {
	if strings.TrimSpace(evidence.Value) == "" {
		return errors.New("merge evidence is required")
	}
	first, err := r.store.CanonicalIdentity(a)
	if err != nil {
		return err
	}
	second, err := r.store.CanonicalIdentity(b)
	if err != nil {
		return err
	}
	if first == second {
		return nil
	}
	firstPins, err := r.store.IdentityPins(first)
	if err != nil {
		return err
	}
	secondPins, err := r.store.IdentityPins(second)
	if err != nil {
		return err
	}
	if pinsConflict(firstPins, secondPins) {
		return ErrPinnedConflict
	}
	return r.store.MergeIdentities(
		first,
		second,
		int(evidence.Rank),
		evidence.Value,
		time.Now().UTC(),
	)
}

func (r *Resolver) Unmerge(deviceID string) error {
	return r.store.UnmergeIdentity(deviceID)
}

func pinsConflict(a, b []string) bool {
	for _, left := range a {
		for _, right := range b {
			if left != right {
				return true
			}
		}
	}
	return false
}

func scopedIPValue(nodeID, ip string, now time.Time) string {
	window := now.UTC().Unix() / int64(scopedIPWindow/time.Second)
	return fmt.Sprintf("%s|%d|%s", nodeID, window, ip)
}
