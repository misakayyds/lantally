package protocol

import (
	"errors"
	"fmt"
)

var (
	errProtocolVersion = errors.New("protocol_version must be 1")
	errSiteID          = errors.New("site_id is required")
	errNodeID          = errors.New("node_id is required")
	errBootID          = errors.New("boot_id is required")
	errSequence        = errors.New("sequence must be greater than 0")
	errSampledAt       = errors.New("sampled_at is required")
	errIntervalMS      = errors.New("interval_ms must be greater than 0")
)

func knownCapability(c Capability) bool {
	switch c {
	case CapIface, CapConntrack, CapNlbwmon, CapMihomo:
		return true
	default:
		return false
	}
}

// Validate checks semantic constraints on an ingest batch.
func Validate(b Batch) error {
	if b.ProtocolVersion != 1 {
		return errProtocolVersion
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
	return nil
}
