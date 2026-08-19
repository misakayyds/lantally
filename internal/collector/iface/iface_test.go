package iface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/protocol"
)

func TestParseProcNetDevSyntheticFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "fixtures", "synthetic", "proc_net_dev.txt")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := ParseProcNetDev(f)
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.IfaceDelta{
		{Name: "eth0", RxDelta: 18446744070000000000, TxDelta: 18446744060000000000},
		{Name: "lo", RxDelta: 9223372036854775808, TxDelta: 9223372036854775908},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d interfaces, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("interface %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseProcNetDevRejectsMalformedCounter(t *testing.T) {
	_, err := ParseProcNetDev(strings.NewReader("eth0: nope 1 0 0 0 0 0 0 2 1 0 0 0 0 0 0\n"))
	if err == nil {
		t.Fatal("expected malformed counter error")
	}
}

func TestTrackerEmitsDeltasAfterTwoSamples(t *testing.T) {
	tracker := NewTracker()
	t0 := time.Unix(1_700_000_000, 0).UTC()
	first := []protocol.IfaceDelta{
		{Name: "eth0", RxDelta: 100, TxDelta: 200},
		{Name: "lo", RxDelta: 50, TxDelta: 50},
	}
	if deltas, gaps := tracker.Sample(first, t0); len(deltas) != 0 || len(gaps) != 0 {
		t.Fatalf("first sample must establish baseline, got deltas=%+v gaps=%+v", deltas, gaps)
	}

	deltas, gaps := tracker.Sample([]protocol.IfaceDelta{
		{Name: "eth0", RxDelta: 140, TxDelta: 260},
		{Name: "lo", RxDelta: 55, TxDelta: 57},
	}, t0.Add(15*time.Second))
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", gaps)
	}
	want := []protocol.IfaceDelta{
		{Name: "eth0", RxDelta: 40, TxDelta: 60},
		{Name: "lo", RxDelta: 5, TxDelta: 7},
	}
	if len(deltas) != len(want) {
		t.Fatalf("got %d deltas, want %d: %+v", len(deltas), len(want), deltas)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Errorf("delta %d: got %+v, want %+v", i, deltas[i], want[i])
		}
	}
}

func TestTrackerRecordsResetForCounterDecreaseAndDisappearance(t *testing.T) {
	tracker := NewTracker()
	t0 := time.Unix(1_700_000_000, 0).UTC()
	tracker.Sample([]protocol.IfaceDelta{
		{Name: "eth0", RxDelta: 100, TxDelta: 200},
		{Name: "lo", RxDelta: 50, TxDelta: 50},
	}, t0)

	deltas, gaps := tracker.Sample([]protocol.IfaceDelta{
		{Name: "eth0", RxDelta: 10, TxDelta: 220},
	}, t0.Add(15*time.Second))
	if len(deltas) != 0 {
		t.Fatalf("reset interval must not emit partial or negative deltas: %+v", deltas)
	}
	if len(gaps) != 2 {
		t.Fatalf("got %d gaps, want reset and disappearance gaps: %+v", len(gaps), gaps)
	}
	for _, gap := range gaps {
		if gap.Reason != protocol.GapCollectorReset || !gap.From.Equal(t0) ||
			!gap.To.Equal(t0.Add(15*time.Second)) {
			t.Errorf("unexpected gap: %+v", gap)
		}
	}
}
