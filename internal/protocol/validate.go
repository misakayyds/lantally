package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnsupportedVersion = errors.New("unsupported protocol_version")
	errSiteID             = errors.New("site_id is required")
	errNodeID             = errors.New("node_id is required")
	errBootID             = errors.New("boot_id is required")
	errSequence           = errors.New("sequence must be greater than 0")
	errSampledAt          = errors.New("sampled_at is required")
	errIntervalMS         = errors.New("interval_ms must be greater than 0")
)

const SupportedVersion = 1

type VersionError struct {
	Got  int
	Want int
}

func (e VersionError) Error() string {
	return fmt.Sprintf("unsupported protocol_version %d; server accepts %d", e.Got, e.Want)
}

func (e VersionError) Unwrap() error {
	return ErrUnsupportedVersion
}

func knownCapability(c Capability) bool {
	switch c {
	case CapIface, CapConntrack, CapNlbwmon, CapMihomo:
		return true
	default:
		return false
	}
}

func knownDimensionType(t DimensionType) bool {
	switch t {
	case DimGroup, DimProcess, DimDomain, DimIface:
		return true
	default:
		return false
	}
}

func knownDeviceSource(s DeviceSource) bool {
	switch s {
	case SourceNlbwmon, SourceMihomo, SourceNeigh:
		return true
	default:
		return false
	}
}

func knownGapReason(r GapReason) bool {
	switch r {
	case GapReboot, GapBufferDrop, GapCollectorReset:
		return true
	default:
		return false
	}
}

// Validate checks semantic constraints on an ingest batch.
func Validate(b Batch) error {
	if b.ProtocolVersion != SupportedVersion {
		return VersionError{Got: b.ProtocolVersion, Want: SupportedVersion}
	}
	if b.SiteID == "" {
		return errSiteID
	}
	if b.NodeID == "" {
		return errNodeID
	}
	if b.BootID == "" {
		return errBootID
	}
	if b.Sequence == 0 {
		return errSequence
	}
	if b.SampledAt.IsZero() {
		return errSampledAt
	}
	if b.IntervalMS <= 0 {
		return errIntervalMS
	}
	for _, cap := range b.Capabilities {
		if !knownCapability(cap) {
			return fmt.Errorf("unknown capability: %q", cap)
		}
	}
	for _, iface := range b.Interfaces {
		if strings.TrimSpace(iface.Name) == "" {
			return errors.New("interface name is required")
		}
	}
	for _, dev := range b.Devices {
		if strings.TrimSpace(dev.ObsIP) == "" {
			return errors.New("device obs_ip is required")
		}
		if !knownDeviceSource(dev.Source) {
			return fmt.Errorf("unknown device source: %q", dev.Source)
		}
	}
	if b.Proxy != nil {
		for _, ob := range b.Proxy.ByOutbound {
			if strings.TrimSpace(ob.Name) == "" {
				return errors.New("outbound name is required")
			}
		}
	}
	for _, gap := range b.Gaps {
		if !knownGapReason(gap.Reason) {
			return fmt.Errorf("unknown gap reason: %q", gap.Reason)
		}
		if gap.From.IsZero() || gap.To.IsZero() {
			return errors.New("gap from and to are required")
		}
	}
	if trimmedMeta := bytes.TrimSpace(b.Metadata); len(trimmedMeta) > 0 {
		if trimmedMeta[0] != '{' {
			return errors.New("metadata must be a JSON object")
		}
		var obj map[string]any
		if err := json.Unmarshal(trimmedMeta, &obj); err != nil || obj == nil {
			return errors.New("metadata must be a JSON object")
		}
	}
	for _, d := range b.Dimensions {
		if !knownDimensionType(d.Type) {
			return fmt.Errorf("unknown dimension type: %q", d.Type)
		}
		if strings.TrimSpace(d.Key) == "" {
			return errors.New("dimension key is required")
		}
	}
	for _, node := range b.ProxyNodes {
		if strings.TrimSpace(node) == "" {
			return errors.New("proxy_node name must not be empty")
		}
	}
	for _, p := range b.Providers {
		if strings.TrimSpace(p.Name) == "" {
			return errors.New("provider name is required")
		}
		for _, member := range p.Proxies {
			if strings.TrimSpace(member) == "" {
				return errors.New("provider proxy name must not be empty")
			}
		}
	}
	return nil
}
