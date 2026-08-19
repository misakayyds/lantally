package nlbwmon

import (
	"context"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/protocol"
)

func TestCollectorConvertsSuccessiveDumps(t *testing.T) {
	dumps := [][]byte{
		[]byte(`{"columns":["mac","ip","rx_bytes","tx_bytes"],"data":[["02:00:00:00:00:0a","192.168.0.10",1200,600]]}`),
		[]byte(`{"columns":["mac","ip","rx_bytes","tx_bytes"],"data":[["02:00:00:00:00:0a","192.168.0.10",1500,800]]}`),
	}
	index := 0
	collector := NewCollector(func(context.Context) ([]byte, error) {
		raw := dumps[index]
		index++
		return raw, nil
	})
	if collector.Capability() != protocol.CapNlbwmon {
		t.Fatalf("capability = %q", collector.Capability())
	}

	at := time.Unix(1_700_000_000, 0).UTC()
	first, gaps, err := collector.Collect(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 || len(gaps) != 0 {
		t.Fatalf("baseline must be empty, got devices=%+v gaps=%+v", first, gaps)
	}

	second, gaps, err := collector.Collect(context.Background(), at.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", gaps)
	}
	want := protocol.DeviceDelta{
		ObsIP: "192.168.0.10", ObsMAC: "02:00:00:00:00:0a",
		RxDelta: 300, TxDelta: 200, Source: protocol.SourceNlbwmon,
	}
	if len(second) != 1 || second[0] != want {
		t.Fatalf("delta = %+v, want %+v", second, want)
	}
}
