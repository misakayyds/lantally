package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/protocol"
)

type fakeCollector struct {
	capability protocol.Capability
	deltas     []protocol.IfaceDelta
}

func (f fakeCollector) Capability() protocol.Capability {
	return f.capability
}

func (f fakeCollector) Collect(context.Context, time.Time) ([]protocol.IfaceDelta, []protocol.Gap, error) {
	return f.deltas, nil, nil
}

func TestDisabledIfaceDoesNotPreventSimReporting(t *testing.T) {
	var cfg Config
	cfg.SiteID = "site-test"
	cfg.NodeID = "node-test"
	cfg.Interval = 15 * time.Second
	cfg.Collectors.Iface = false

	a := newAgent(cfg, "boot-test", map[string]collector{
		"iface": fakeCollector{
			capability: protocol.CapIface,
			deltas:     []protocol.IfaceDelta{{Name: "eth0", RxDelta: 1, TxDelta: 2}},
		},
		"sim": fakeCollector{
			capability: protocol.CapIface,
			deltas:     []protocol.IfaceDelta{{Name: "sim0", RxDelta: 3, TxDelta: 4}},
		},
	})

	batch := a.collect(context.Background(), time.Unix(1_700_000_000, 0).UTC())
	if len(batch.Capabilities) != 1 || batch.Capabilities[0] != protocol.CapIface {
		t.Fatalf("sim capability missing with iface disabled: %+v", batch.Capabilities)
	}
	if len(batch.Interfaces) != 1 || batch.Interfaces[0].Name != "sim0" {
		t.Fatalf("sim report missing with iface disabled: %+v", batch.Interfaces)
	}
}

func TestSequentialAgentSessionsUseDistinctBootIDs(t *testing.T) {
	var cfg Config
	cfg.SiteID = "site-test"
	cfg.NodeID = "node-test"
	cfg.Interval = 15 * time.Second

	first, err := newAgentSession(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newAgentSession(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstBatch := first.collect(context.Background(), time.Unix(1_700_000_000, 0).UTC())
	secondBatch := second.collect(context.Background(), time.Unix(1_700_000_015, 0).UTC())
	if firstBatch.Sequence != 1 || secondBatch.Sequence != 1 {
		t.Fatalf("session sequences = (%d, %d), want (1, 1)", firstBatch.Sequence, secondBatch.Sequence)
	}
	if firstBatch.BootID == secondBatch.BootID {
		t.Fatalf("sequential sessions reused boot_id %q", firstBatch.BootID)
	}
	for _, id := range []string{firstBatch.BootID, secondBatch.BootID} {
		if len(id) != 36 || strings.Count(id, "-") != 4 {
			t.Fatalf("boot_id %q is not UUID-shaped", id)
		}
	}
}

func TestFirstBatchRecordsRebootGap(t *testing.T) {
	var cfg Config
	cfg.SiteID = "site-test"
	cfg.NodeID = "node-test"
	cfg.Interval = 15 * time.Second
	at := time.Unix(1_700_000_000, 0).UTC()
	a := newAgent(cfg, "session-test", nil)

	first := a.collect(context.Background(), at)
	if len(first.Gaps) != 1 || first.Gaps[0].Reason != protocol.GapReboot {
		t.Fatalf("first batch reboot gap missing: %+v", first.Gaps)
	}
	if !first.Gaps[0].From.Equal(at.Add(-cfg.Interval)) || !first.Gaps[0].To.Equal(at) {
		t.Fatalf("first batch reboot gap bounds = %+v", first.Gaps[0])
	}
	second := a.collect(context.Background(), at.Add(cfg.Interval))
	for _, gap := range second.Gaps {
		if gap.Reason == protocol.GapReboot {
			t.Fatalf("reboot gap repeated on second batch: %+v", second.Gaps)
		}
	}
}

func TestBatchQueueOverflowMarksNextBatchSent(t *testing.T) {
	q := newBatchQueue(2)
	t0 := time.Unix(1_700_000_000, 0).UTC()
	q.Push(protocol.Batch{Sequence: 1, SampledAt: t0})
	q.Push(protocol.Batch{Sequence: 2, SampledAt: t0.Add(time.Second)})
	q.Push(protocol.Batch{Sequence: 3, SampledAt: t0.Add(2 * time.Second)})

	if q.Len() != 2 {
		t.Fatalf("queue length = %d, want 2", q.Len())
	}
	if got := q.Peek().Sequence; got != 2 {
		t.Fatalf("oldest sequence = %d, want 2", got)
	}
	nextSent := q.Peek()
	if len(nextSent.Gaps) != 1 || nextSent.Gaps[0].Reason != protocol.GapBufferDrop {
		t.Fatalf("overflow gap missing from next batch sent: %+v", nextSent.Gaps)
	}
	if !nextSent.Gaps[0].From.Equal(t0) || !nextSent.Gaps[0].To.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("overflow gap has wrong bounds: %+v", nextSent.Gaps[0])
	}
}

func TestBatchQueueRepeatedOverflowCarriesEarliestBoundary(t *testing.T) {
	q := newBatchQueue(2)
	t0 := time.Unix(1_700_000_000, 0).UTC()
	for sequence := uint64(1); sequence <= 5; sequence++ {
		q.Push(protocol.Batch{
			Sequence:  sequence,
			SampledAt: t0.Add(time.Duration(sequence-1) * time.Second),
		})
	}

	nextSent := q.Peek()
	if nextSent.Sequence != 4 {
		t.Fatalf("oldest surviving sequence = %d, want 4", nextSent.Sequence)
	}
	if len(nextSent.Gaps) != 1 || nextSent.Gaps[0].Reason != protocol.GapBufferDrop {
		t.Fatalf("carried overflow gap missing: %+v", nextSent.Gaps)
	}
	gap := nextSent.Gaps[0]
	if !gap.From.Equal(t0) || !gap.To.Equal(t0.Add(4*time.Second)) {
		t.Fatalf("carried gap = %+v, want earliest from %s through %s", gap, t0, t0.Add(4*time.Second))
	}
}

func TestBackoffIsBoundedAndIncreases(t *testing.T) {
	b := newBackoff(time.Second, time.Minute, func() float64 { return 0.5 })
	first := b.Next()
	second := b.Next()
	if first != time.Second || second != 2*time.Second {
		t.Fatalf("backoff = (%s, %s), want (1s, 2s)", first, second)
	}
	for range 10 {
		if got := b.Next(); got > time.Minute {
			t.Fatalf("backoff exceeded maximum: %s", got)
		}
	}
	b.Reset()
	if got := b.Next(); got != time.Second {
		t.Fatalf("reset backoff = %s, want 1s", got)
	}
}

func TestLoadConfigAndFullBearerToken(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("lt_credential_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "agent.json")
	raw := `{
		"server_url":"https://collector.example.test",
		"site_id":"site-test",
		"node_id":"node-test",
		"token_file":` + mustJSON(t, tokenPath) + `,
		"interval":"15s",
		"collectors":{"iface":true}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != 15*time.Second || !cfg.Collectors.Iface {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	token, err := loadToken(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if token != "lt_credential_secret" {
		t.Fatalf("token = %q, want full bearer token", token)
	}
}

func TestPostBatchUsesGzipJSONAndBearerToken(t *testing.T) {
	var received protocol.Batch
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ingest" {
			t.Errorf("path = %q, want /v1/ingest", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer lt_credential_secret" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("content encoding = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received, err = protocol.Decode(body)
		if err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	batch := protocol.Batch{
		ProtocolVersion: 1,
		SiteID:          "site-test",
		NodeID:          "node-test",
		BootID:          "boot-test",
		Sequence:        1,
		SampledAt:       time.Unix(1_700_000_000, 0).UTC(),
		IntervalMS:      15000,
	}
	if err := postBatch(context.Background(), server.Client(), server.URL, "lt_credential_secret", batch); err != nil {
		t.Fatal(err)
	}
	if received.Sequence != batch.Sequence {
		t.Fatalf("received sequence = %d, want %d", received.Sequence, batch.Sequence)
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
