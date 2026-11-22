//go:build windows

package iface

import (
	"context"
	"testing"
	"time"
)

func TestWindowsCollectorCollectSucceeds(t *testing.T) {
	col := NewCollector()
	t0 := time.Now().UTC()
	deltas, gaps, err := col.Collect(context.Background(), t0)
	if err != nil {
		t.Fatalf("windows Collect baseline failed: %v", err)
	}
	if len(deltas) != 0 || len(gaps) != 0 {
		t.Fatalf("first Collect should only establish baseline, got deltas=%+v gaps=%+v", deltas, gaps)
	}
	_, _, err = col.Collect(context.Background(), t0.Add(time.Second))
	if err != nil {
		t.Fatalf("windows Collect second sample failed: %v", err)
	}
}
