// Package iface collects cumulative Linux interface counters and converts them
// into interval deltas.
package iface

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/protocol"
)

// ParseProcNetDev parses cumulative receive and transmit byte counters.
// RxDelta and TxDelta carry absolute values until Tracker.Sample converts them.
func ParseProcNetDev(r io.Reader) ([]protocol.IfaceDelta, error) {
	scanner := bufio.NewScanner(r)
	var result []protocol.IfaceDelta
	seen := make(map[string]struct{})
	for scanner.Scan() {
		line := scanner.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		fields := strings.Fields(line[colon+1:])
		if name == "" || len(fields) < 16 {
			return nil, fmt.Errorf("malformed /proc/net/dev row %q", line)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate interface %q", name)
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s receive bytes: %w", name, err)
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s transmit bytes: %w", name, err)
		}
		seen[name] = struct{}{}
		result = append(result, protocol.IfaceDelta{Name: name, RxDelta: rx, TxDelta: tx})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Tracker converts cumulative interface snapshots into interval deltas.
type Tracker struct {
	previous   map[string]protocol.IfaceDelta
	previousAt time.Time
}

func NewTracker() *Tracker {
	return &Tracker{}
}

func (t *Tracker) Sample(current []protocol.IfaceDelta, at time.Time) ([]protocol.IfaceDelta, []protocol.Gap) {
	next := make(map[string]protocol.IfaceDelta, len(current))
	for _, sample := range current {
		next[sample.Name] = sample
	}
	if t.previous == nil {
		t.previous = next
		t.previousAt = at
		return nil, nil
	}

	var deltas []protocol.IfaceDelta
	var gaps []protocol.Gap
	for _, sample := range current {
		previous, ok := t.previous[sample.Name]
		if !ok {
			continue
		}
		rx, rxReset := accounting.DeltaUint64(previous.RxDelta, sample.RxDelta)
		tx, txReset := accounting.DeltaUint64(previous.TxDelta, sample.TxDelta)
		if rxReset || txReset {
			gaps = append(gaps, collectorResetGap(t.previousAt, at))
			continue
		}
		deltas = append(deltas, protocol.IfaceDelta{Name: sample.Name, RxDelta: rx, TxDelta: tx})
	}
	for name := range t.previous {
		if _, ok := next[name]; !ok {
			gaps = append(gaps, collectorResetGap(t.previousAt, at))
		}
	}
	t.previous = next
	t.previousAt = at
	return deltas, gaps
}

func collectorResetGap(from, to time.Time) protocol.Gap {
	return protocol.Gap{Reason: protocol.GapCollectorReset, From: from, To: to}
}

// Collector reads the platform interface snapshot and tracks interval deltas.
type Collector struct {
	tracker *Tracker
}

func NewCollector() *Collector {
	return &Collector{tracker: NewTracker()}
}

func (c *Collector) Capability() protocol.Capability {
	return protocol.CapIface
}

func (c *Collector) Collect(ctx context.Context, at time.Time) ([]protocol.IfaceDelta, []protocol.Gap, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	snapshot, err := readProcNetDev()
	if err != nil {
		return nil, nil, err
	}
	deltas, gaps := c.tracker.Sample(snapshot, at)
	return deltas, gaps, nil
}
