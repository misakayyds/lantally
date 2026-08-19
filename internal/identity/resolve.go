// Package identity resolves device observations conservatively within a site.
package identity

import (
	"crypto/rand"
	"encoding/hex"
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
	ErrPinnedConflict   = errors.New("conflicting user pins")
	ErrIdentityNotFound = errors.New("identity not found")
)

type Evidence struct {
	Rank  EvidenceRank
	Value string
}

type Store interface {
	CreateIdentity(siteID, deviceID string, now time.Time) error
	FindIdentities(siteID string, rank int, value string) ([]string, error)
	AddIdentityEvidence(
		siteID, deviceID string,
		rank int,
		value, nodeID string,
		observedAt time.Time,
	) error
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

func (r *Resolver) Resolve(
	siteID string,
	obs protocol.DeviceDelta,
	now time.Time,
) (deviceID string, limitation string, err error) {
	if strings.TrimSpace(siteID) == "" {
		return "", "", errors.New("site ID is required")
	}
	ip := net.ParseIP(strings.TrimSpace(obs.ObsIP))
	if ip == nil {
		return "", "", fmt.Errorf("invalid observed IP %q", obs.ObsIP)
	}
	if strings.TrimSpace(r.nodeID) == "" {
		return "", "", errors.New("node ID is required")
	}
	now = now.UTC()

	var evidence []Evidence
	if strings.TrimSpace(obs.ObsMAC) != "" {
		hardwareAddr, parseErr := net.ParseMAC(obs.ObsMAC)
		if parseErr != nil {
			return "", "", fmt.Errorf("invalid observed MAC %q: %w", obs.ObsMAC, parseErr)
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
			Value: scopedIPValue(r.nodeID, ip.String(), now),
		})
		limitation = "source IP scoped to site, node, and one-hour window"
	}

	for _, candidateEvidence := range evidence {
		candidates, findErr := r.store.FindIdentities(
			siteID,
			int(candidateEvidence.Rank),
			candidateEvidence.Value,
		)
		if findErr != nil {
			return "", "", findErr
		}
		if len(candidates) == 1 {
			deviceID, err = r.store.CanonicalIdentity(candidates[0])
			if err != nil {
				return "", "", err
			}
			break
		}
		if len(candidates) > 1 {
			limitation = "conflicting identity evidence; identities were not auto-merged"
		}
	}

	if deviceID == "" {
		deviceID, err = newDeviceID()
		if err != nil {
			return "", "", err
		}
		if err = r.store.CreateIdentity(siteID, deviceID, now); err != nil {
			return "", "", err
		}
	}
	for _, item := range evidence {
		if err = r.store.AddIdentityEvidence(
			siteID,
			deviceID,
			int(item.Rank),
			item.Value,
			r.nodeID,
			now,
		); err != nil {
			return "", "", err
		}
	}
	return deviceID, limitation, nil
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

func newDeviceID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate device ID: %w", err)
	}
	return "dev_" + hex.EncodeToString(raw[:]), nil
}
