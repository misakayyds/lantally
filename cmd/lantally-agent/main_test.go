package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestBatchQueueOverflowDropsOldestAndMarksGap(t *testing.T) {
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
	q.Pop()
	newest := q.Peek()
	if newest.Sequence != 3 {
		t.Fatalf("newest sequence = %d, want 3", newest.Sequence)
	}
	if len(newest.Gaps) != 1 || newest.Gaps[0].Reason != protocol.GapBufferDrop {
		t.Fatalf("overflow gap missing: %+v", newest.Gaps)
	}
	if !newest.Gaps[0].From.Equal(t0) || !newest.Gaps[0].To.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("overflow gap has wrong bounds: %+v", newest.Gaps[0])
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
