package protocol

import (
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := Batch{
		ProtocolVersion: 1,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        7,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
		Capabilities:    []Capability{CapIface},
		Interfaces: []IfaceDelta{{
			Name:    "eth0",
			RxDelta: 100,
			TxDelta: 20,
		}},
	}
	raw, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Sequence != 7 || out.Interfaces[0].RxDelta != 100 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestDecodeRejectsWrongVersion(t *testing.T) {
	raw, err := Encode(Batch{
		ProtocolVersion: 2,
		SiteID:          "site-a",
		NodeID:          "node-a",
		BootID:          "boot-1",
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(raw); err == nil {
		t.Fatal("expected version rejection")
	}
}
