package nlbwmon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/protocol"
)

func TestParseDumpSyntheticFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "fixtures", "synthetic", "nlbwmon.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := ParseDump(f)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]protocol.DeviceDelta{
		"203.0.113.10|02:00:00:00:00:0a": {
			ObsIP:   "203.0.113.10",
			ObsMAC:  "02:00:00:00:00:0a",
			RxDelta: 1200,
			TxDelta: 600,
			Source:  protocol.SourceNlbwmon,
		},
		"203.0.113.11|02:00:00:00:00:0b": {
			ObsIP:   "203.0.113.11",
			ObsMAC:  "02:00:00:00:00:0b",
			RxDelta: 400,
			TxDelta: 300,
			Source:  protocol.SourceNlbwmon,
		},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d devices, want %d: %+v", len(got), len(want), got)
	}
	for _, delta := range got {
		key := delta.ObsIP + "|" + delta.ObsMAC
		expected, ok := want[key]
		if !ok {
			t.Fatalf("unexpected device %q: %+v", key, delta)
		}
		if delta != expected {
			t.Fatalf("device %q: got %+v, want %+v", key, delta, expected)
		}
	}
}

func TestTrackerConvertsTwoDumpsToIntervalDeltas(t *testing.T) {
	firstDump := `{
		"columns": ["mac","ip","rx_bytes","tx_bytes"],
		"data": [
			["02:00:00:00:00:0a","203.0.113.10",1200,600]
		]
	}`
	secondDump := `{
		"columns": ["mac","ip","rx_bytes","tx_bytes"],
		"data": [
			["02:00:00:00:00:0a","203.0.113.10",1500,800]
		]
	}`

	first, err := ParseDump(strings.NewReader(firstDump))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseDump(strings.NewReader(secondDump))
	if err != nil {
		t.Fatal(err)
	}

	tracker := NewTracker()
	t0 := time.Unix(1_700_000_000, 0).UTC()
	if deltas, gaps := tracker.Sample(first, t0); len(deltas) != 0 || len(gaps) != 0 {
		t.Fatalf("first sample must establish baseline, got deltas=%+v gaps=%+v", deltas, gaps)
	}

	deltas, gaps := tracker.Sample(second, t0.Add(30*time.Second))
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", gaps)
	}
	want := []protocol.DeviceDelta{{
		ObsIP:   "203.0.113.10",
		ObsMAC:  "02:00:00:00:00:0a",
		RxDelta: 300,
		TxDelta: 200,
		Source:  protocol.SourceNlbwmon,
	}}
	if len(deltas) != len(want) {
		t.Fatalf("got %d deltas, want %d: %+v", len(deltas), len(want), deltas)
	}
	if deltas[0] != want[0] {
		t.Fatalf("delta = %+v, want %+v", deltas[0], want[0])
	}
}

func TestParseDumpSkipsMalformedRows(t *testing.T) {
	dump := `{
		"columns": ["mac","ip","rx_bytes","tx_bytes"],
		"data": [
			["02:00:00:00:00:0a","203.0.113.10",1200,600],
			["bad-mac","not-an-ip","nope","also-bad"],
			["02:00:00:00:00:0b","203.0.113.11",400,300]
		]
	}`

	got, err := ParseDump(strings.NewReader(dump))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d devices, want 2 malformed row skipped: %+v", len(got), got)
	}
}

func TestParseDumpRejectsEmptyDocument(t *testing.T) {
	_, err := ParseDump(strings.NewReader(`{"columns":[],"data":[]}`))
	if err == nil {
		t.Fatal("expected error for dump missing columns")
	}
}

func TestTrackerRecordsResetForCounterDecrease(t *testing.T) {
	tracker := NewTracker()
	t0 := time.Unix(1_700_000_000, 0).UTC()
	first := []protocol.DeviceDelta{{
		ObsIP: "203.0.113.10", ObsMAC: "02:00:00:00:00:0a",
		RxDelta: 1200, TxDelta: 600, Source: protocol.SourceNlbwmon,
	}}
	tracker.Sample(first, t0)

	deltas, gaps := tracker.Sample([]protocol.DeviceDelta{{
		ObsIP: "203.0.113.10", ObsMAC: "02:00:00:00:00:0a",
		RxDelta: 100, TxDelta: 700, Source: protocol.SourceNlbwmon,
	}}, t0.Add(30*time.Second))
	if len(deltas) != 0 {
		t.Fatalf("reset interval must not emit deltas: %+v", deltas)
	}
	if len(gaps) != 1 || gaps[0].Reason != protocol.GapCollectorReset {
		t.Fatalf("expected collector reset gap, got %+v", gaps)
	}
}
