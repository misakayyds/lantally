package protocol

import (
	"testing"
	"time"
)

func TestValidateRejectsWrongVersion(t *testing.T) {
	err := Validate(Batch{
		ProtocolVersion: 0,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
	})
	if err == nil {
		t.Fatal("expected version error")
	}
}

func TestValidateAcceptsMinimalV1(t *testing.T) {
	err := Validate(Batch{
		ProtocolVersion: 1,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
		Capabilities:    []Capability{CapIface},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsEmptyIDs(t *testing.T) {
	err := Validate(Batch{
		ProtocolVersion: 1,
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
	})
	if err == nil {
		t.Fatal("expected missing id error")
	}
}
