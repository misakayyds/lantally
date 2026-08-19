package protocol

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validBatch() Batch {
	return Batch{
		ProtocolVersion: 1,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
	}
}

func TestValidateRejectsWrongVersion(t *testing.T) {
	b := validBatch()
	b.ProtocolVersion = 2
	err := Validate(b)
	var ver VersionError
	if !errors.As(err, &ver) || ver.Got != 2 || ver.Want != 1 {
		t.Fatalf("expected VersionError got=2 want=1, got %v", err)
	}
	if !strings.Contains(err.Error(), "protocol_version 2") {
		t.Fatalf("error should name the version: %v", err)
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
	if !errors.Is(err, errSiteID) {
		t.Fatalf("expected missing site_id error, got %v", err)
	}
}

func TestValidateRejectsEmptyNodeID(t *testing.T) {
	b := validBatch()
	b.NodeID = ""
	err := Validate(b)
	if !errors.Is(err, errNodeID) {
		t.Fatalf("expected node_id error, got %v", err)
	}
}

func TestValidateRejectsEmptyBootID(t *testing.T) {
	b := validBatch()
	b.BootID = ""
	err := Validate(b)
	if !errors.Is(err, errBootID) {
		t.Fatalf("expected boot_id error, got %v", err)
	}
}

func TestValidateRejectsZeroSequence(t *testing.T) {
	b := validBatch()
	b.Sequence = 0
	err := Validate(b)
	if !errors.Is(err, errSequence) {
		t.Fatalf("expected sequence error, got %v", err)
	}
}

func TestValidateRejectsZeroSampledAt(t *testing.T) {
	b := validBatch()
	b.SampledAt = time.Time{}
	err := Validate(b)
	if !errors.Is(err, errSampledAt) {
		t.Fatalf("expected sampled_at error, got %v", err)
	}
}

func TestValidateRejectsNonPositiveIntervalMS(t *testing.T) {
	for _, interval := range []int{0, -1} {
		b := validBatch()
		b.IntervalMS = interval
		err := Validate(b)
		if !errors.Is(err, errIntervalMS) {
			t.Fatalf("interval_ms=%d: expected interval error, got %v", interval, err)
		}
	}
}

func TestValidateRejectsUnknownCapability(t *testing.T) {
	b := validBatch()
	b.Capabilities = []Capability{"not-a-real-cap"}
	err := Validate(b)
	if err == nil {
		t.Fatal("expected unknown capability error")
	}
	if !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("expected unknown capability error, got %v", err)
	}
}
