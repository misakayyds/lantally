// Package nlbwmon parses read-only nlbwmon JSON dumps into device traffic deltas.
//
// On OpenWrt the agent reads cumulative counters with:
//
//	/usr/sbin/nlbw -c json
//
// The utility queries the local nlbwmon daemon over its Unix socket only; it
// does not modify firewall rules, conntrack, or the nlbwmon database.
package nlbwmon

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/protocol"
)

type dumpDocument struct {
	Columns []string          `json:"columns"`
	Data    [][]json.RawMessage `json:"data"`
}

type hostKey struct {
	ip  string
	mac string
}

// ParseDump parses one nlbwmon JSON dump and returns cumulative byte counters
// grouped by observed IP and MAC. RxDelta and TxDelta hold absolute totals until
// Tracker.Sample converts them into interval deltas.
func ParseDump(r io.Reader) ([]protocol.DeviceDelta, error) {
	var doc dumpDocument
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode nlbwmon dump: %w", err)
	}
	if len(doc.Columns) == 0 {
		return nil, fmt.Errorf("nlbwmon dump missing columns")
	}

	indexes := buildColumnIndexes(doc.Columns)
	aggregates := make(map[hostKey]protocol.DeviceDelta)

	for _, row := range doc.Data {
		parsed, ok := parseRow(indexes, row)
		if !ok {
			continue
		}
		key := hostKey{ip: parsed.ObsIP, mac: parsed.ObsMAC}
		current := aggregates[key]
		current.ObsIP = parsed.ObsIP
		current.ObsMAC = parsed.ObsMAC
		current.Source = protocol.SourceNlbwmon
		current.RxDelta += parsed.RxDelta
		current.TxDelta += parsed.TxDelta
		aggregates[key] = current
	}

	if len(aggregates) == 0 && len(doc.Data) > 0 {
		return nil, fmt.Errorf("nlbwmon dump contained no usable rows")
	}

	result := make([]protocol.DeviceDelta, 0, len(aggregates))
	for _, delta := range aggregates {
		result = append(result, delta)
	}
	return result, nil
}

// Tracker converts cumulative nlbwmon snapshots into interval deltas.
type Tracker struct {
	previous   map[hostKey]protocol.DeviceDelta
	previousAt time.Time
}

func NewTracker() *Tracker {
	return &Tracker{}
}

func (t *Tracker) Sample(current []protocol.DeviceDelta, at time.Time) ([]protocol.DeviceDelta, []protocol.Gap) {
	next := make(map[hostKey]protocol.DeviceDelta, len(current))
	for _, sample := range current {
		next[hostKey{ip: sample.ObsIP, mac: sample.ObsMAC}] = sample
	}
	if t.previous == nil {
		t.previous = next
		t.previousAt = at
		return nil, nil
	}

	var deltas []protocol.DeviceDelta
	var gaps []protocol.Gap
	for _, sample := range current {
		previous, ok := t.previous[hostKey{ip: sample.ObsIP, mac: sample.ObsMAC}]
		if !ok {
			continue
		}
		rx, rxReset := accounting.DeltaUint64(previous.RxDelta, sample.RxDelta)
		tx, txReset := accounting.DeltaUint64(previous.TxDelta, sample.TxDelta)
		if rxReset || txReset {
			gaps = append(gaps, collectorResetGap(t.previousAt, at))
			continue
		}
		deltas = append(deltas, protocol.DeviceDelta{
			ObsIP:   sample.ObsIP,
			ObsMAC:  sample.ObsMAC,
			RxDelta: rx,
			TxDelta: tx,
			Source:  protocol.SourceNlbwmon,
		})
	}
	for key := range t.previous {
		if _, ok := next[key]; !ok {
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

type columnMap struct {
	mac      int
	ip       int
	rxBytes  int
	txBytes  int
	hasMAC   bool
	hasIP    bool
	hasRX    bool
	hasTX    bool
}

func buildColumnIndexes(columns []string) columnMap {
	var idx columnMap
	idx.mac = -1
	idx.ip = -1
	idx.rxBytes = -1
	idx.txBytes = -1
	for i, name := range columns {
		switch strings.ToLower(name) {
		case "mac":
			idx.mac = i
			idx.hasMAC = true
		case "ip":
			idx.ip = i
			idx.hasIP = true
		case "rx_bytes":
			idx.rxBytes = i
			idx.hasRX = true
		case "tx_bytes":
			idx.txBytes = i
			idx.hasTX = true
		}
	}
	return idx
}

func parseRow(indexes columnMap, row []json.RawMessage) (protocol.DeviceDelta, bool) {
	if !indexes.hasIP || !indexes.hasRX || !indexes.hasTX {
		return protocol.DeviceDelta{}, false
	}
	if indexes.ip >= len(row) || indexes.rxBytes >= len(row) || indexes.txBytes >= len(row) {
		return protocol.DeviceDelta{}, false
	}

	ip, ok := stringCell(row[indexes.ip])
	if !ok || net.ParseIP(strings.TrimSpace(ip)) == nil {
		return protocol.DeviceDelta{}, false
	}
	rx, ok := uint64Cell(row[indexes.rxBytes])
	if !ok {
		return protocol.DeviceDelta{}, false
	}
	tx, ok := uint64Cell(row[indexes.txBytes])
	if !ok {
		return protocol.DeviceDelta{}, false
	}

	var mac string
	if indexes.hasMAC && indexes.mac >= 0 && indexes.mac < len(row) {
		rawMAC, macOK := stringCell(row[indexes.mac])
		if macOK {
			if hardwareAddr, err := net.ParseMAC(strings.TrimSpace(rawMAC)); err == nil {
				mac = strings.ToLower(hardwareAddr.String())
			}
		}
	}

	return protocol.DeviceDelta{
		ObsIP:   ip,
		ObsMAC:  mac,
		RxDelta: rx,
		TxDelta: tx,
		Source:  protocol.SourceNlbwmon,
	}, true
}

func stringCell(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

func uint64Cell(raw json.RawMessage) (uint64, bool) {
	var number uint64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, true
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return 0, false
	}
	number, err := strconv.ParseUint(strings.TrimSpace(asString), 10, 64)
	if err != nil {
		return 0, false
	}
	return number, true
}
