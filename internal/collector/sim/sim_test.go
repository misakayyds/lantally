package sim

import (
	"strings"
	"testing"

	"github.com/lantally/lantally/internal/protocol"
)

func TestSnapshotUsesSyntheticAddresses(t *testing.T) {
	b := Snapshot(1, "boot-1")
	if err := protocol.Validate(b); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.Devices[0].ObsIP, "203.0.113.") {
		t.Fatalf("expected TEST-NET-3, got %s", b.Devices[0].ObsIP)
	}
	if !strings.HasPrefix(b.Devices[0].ObsMAC, "02:00:00:00:00:") {
		t.Fatalf("expected locally administered MAC, got %s", b.Devices[0].ObsMAC)
	}
}

func TestSnapshotRoundTripGzipJSON(t *testing.T) {
	in := Snapshot(42, "boot-roundtrip")
	raw, err := protocol.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := protocol.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Sequence != 42 {
		t.Fatalf("sequence: got %d want 42", out.Sequence)
	}
	if out.BootID != "boot-roundtrip" {
		t.Fatalf("boot_id: got %q want boot-roundtrip", out.BootID)
	}
	if len(out.Devices) == 0 || out.Devices[0].ObsIP != "203.0.113.1" {
		t.Fatalf("device IP lost: %+v", out.Devices)
	}
}

func TestSnapshotSequenceMonotonic(t *testing.T) {
	var prev uint64
	for seq := uint64(1); seq <= 50; seq++ {
		b := Snapshot(seq, "boot-1")
		if err := protocol.Validate(b); err != nil {
			t.Fatal(err)
		}
		if b.Sequence != seq {
			t.Fatalf("got sequence %d want %d", b.Sequence, seq)
		}
		if seq > 1 && b.Sequence <= prev {
			t.Fatal("sequence must increase")
		}
		prev = b.Sequence
	}
}
