package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ifacecollector "github.com/misakayyds/lantally/internal/collector/iface"
	mihomocollector "github.com/misakayyds/lantally/internal/collector/mihomo"
	nlbwmoncollector "github.com/misakayyds/lantally/internal/collector/nlbwmon"
	"github.com/misakayyds/lantally/internal/protocol"

	"github.com/coder/websocket"
)

type fakeCollector struct {
	capability protocol.Capability
	deltas     []protocol.IfaceDelta
	devices    []protocol.DeviceDelta
	proxy      *protocol.ProxyDelta
	err        error
}

func (f fakeCollector) Capability() protocol.Capability {
	return f.capability
}

func (f fakeCollector) Collect(context.Context, time.Time) (snapshot, error) {
	if f.err != nil {
		return snapshot{}, f.err
	}
	return snapshot{interfaces: f.deltas, devices: f.devices, proxy: f.proxy}, nil
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
	if !first.Gaps[0].From.Equal(at) || !first.Gaps[0].To.Equal(at) {
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

func TestBatchQueueOverflowCarriesRebootGapToSurvivingHead(t *testing.T) {
	q := newBatchQueue(2)
	t0 := time.Unix(1_700_000_000, 0).UTC()
	q.Push(protocol.Batch{
		Sequence:  1,
		SampledAt: t0,
		Gaps: []protocol.Gap{{
			Reason: protocol.GapReboot,
			From:   t0,
			To:     t0,
		}},
	})
	for sequence := uint64(2); sequence <= 4; sequence++ {
		q.Push(protocol.Batch{
			Sequence:  sequence,
			SampledAt: t0.Add(time.Duration(sequence-1) * time.Second),
		})
	}

	nextSent := q.Peek()
	if nextSent.Sequence != 3 {
		t.Fatalf("oldest surviving sequence = %d, want 3", nextSent.Sequence)
	}
	reboot, ok := gapByReason(nextSent.Gaps, protocol.GapReboot)
	if !ok {
		t.Fatalf("reboot gap was lost after repeated overflow: %+v", nextSent.Gaps)
	}
	if !reboot.From.Equal(t0) || !reboot.To.Equal(t0) {
		t.Fatalf("reboot marker changed during carry: %+v", reboot)
	}
	if _, ok := gapByReason(nextSent.Gaps, protocol.GapBufferDrop); !ok {
		t.Fatalf("buffer-drop gap missing beside reboot gap: %+v", nextSent.Gaps)
	}
}

func gapByReason(gaps []protocol.Gap, reason protocol.GapReason) (protocol.Gap, bool) {
	for _, gap := range gaps {
		if gap.Reason == reason {
			return gap, true
		}
	}
	return protocol.Gap{}, false
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

func TestLoadConfigEnablesMihomoFromSecretFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	secretPath := filepath.Join(dir, "mihomo.secret")
	if err := os.WriteFile(tokenPath, []byte("lt_credential_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, []byte("local-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "agent.json")
	raw := `{
		"server_url":"https://collector.example.test",
		"site_id":"site-test",
		"node_id":"node-test",
		"token_file":` + mustJSON(t, tokenPath) + `,
		"collectors":{"iface":true,"mihomo":true},
		"mihomo":{"url":"http://127.0.0.1:9090","secret_file":` + mustJSON(t, secretPath) + `}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Collectors.Mihomo || cfg.Mihomo.URL != "http://127.0.0.1:9090" || cfg.Mihomo.SecretFile != secretPath {
		t.Fatalf("unexpected mihomo config: %+v", cfg)
	}
	secret, err := loadSecret(cfg.Mihomo.SecretFile)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "local-secret" {
		t.Fatalf("secret = %q", secret)
	}

	missingURL := `{
		"server_url":"https://collector.example.test",
		"site_id":"site-test",
		"node_id":"node-test",
		"token_file":` + mustJSON(t, tokenPath) + `,
		"collectors":{"mihomo":true}
	}`
	if err := os.WriteFile(configPath, []byte(missingURL), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("expected error when mihomo is enabled without url")
	}
}

func TestLoadConfigLeavesNlbwmonOffByDefault(t *testing.T) {
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
		"collectors":{"iface":true}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collectors.Nlbwmon {
		t.Fatal("nlbwmon must stay off unless collectors.nlbwmon is true")
	}

	enabled := `{
		"server_url":"https://collector.example.test",
		"site_id":"site-test",
		"node_id":"node-test",
		"token_file":` + mustJSON(t, tokenPath) + `,
		"collectors":{"nlbwmon":true},
		"nlbwmon":{"command":"/usr/sbin/nlbw"}
	}`
	if err := os.WriteFile(configPath, []byte(enabled), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Collectors.Nlbwmon || cfg.Nlbwmon.Command != "/usr/sbin/nlbw" {
		t.Fatalf("nlbwmon config = %+v", cfg)
	}
}

func TestCollectMergesMihomoProxyWithoutDroppingIface(t *testing.T) {
	var cfg Config
	cfg.SiteID = "site-test"
	cfg.NodeID = "node-test"
	cfg.Interval = 15 * time.Second
	cfg.Collectors.Iface = true
	cfg.Collectors.Mihomo = true

	a := newAgent(cfg, "boot-test", map[string]collector{
		"iface": fakeCollector{
			capability: protocol.CapIface,
			deltas:     []protocol.IfaceDelta{{Name: "eth0", RxDelta: 10, TxDelta: 4}},
		},
		"mihomo": fakeCollector{
			capability: protocol.CapMihomo,
			proxy: &protocol.ProxyDelta{
				ByOutbound: []protocol.OutboundDelta{
					{Name: "DIRECT", DirectRx: 3, DirectTx: 1},
					{Name: "ss-test", ProxyRx: 7, ProxyTx: 2},
				},
			},
		},
	})
	batch := a.collect(context.Background(), time.Unix(1_700_000_000, 0).UTC())
	if !hasCapability(batch.Capabilities, protocol.CapIface) || !hasCapability(batch.Capabilities, protocol.CapMihomo) {
		t.Fatalf("capabilities = %+v", batch.Capabilities)
	}
	if len(batch.Interfaces) != 1 || batch.Interfaces[0].Name != "eth0" {
		t.Fatalf("iface missing: %+v", batch.Interfaces)
	}
	if batch.Proxy == nil || len(batch.Proxy.ByOutbound) != 2 {
		t.Fatalf("proxy missing: %+v", batch.Proxy)
	}
}

func TestMihomoCollectErrorKeepsIfaceAndRecordsGap(t *testing.T) {
	var cfg Config
	cfg.SiteID = "site-test"
	cfg.NodeID = "node-test"
	cfg.Interval = 15 * time.Second
	cfg.Collectors.Iface = true
	cfg.Collectors.Mihomo = true

	a := newAgent(cfg, "boot-test", map[string]collector{
		"iface": fakeCollector{
			capability: protocol.CapIface,
			deltas:     []protocol.IfaceDelta{{Name: "br-lan", RxDelta: 1, TxDelta: 1}},
		},
		"mihomo": fakeCollector{
			capability: protocol.CapMihomo,
			err:        errors.New("mihomo unavailable"),
		},
	})
	batch := a.collect(context.Background(), time.Unix(1_700_000_000, 0).UTC())
	if len(batch.Interfaces) != 1 {
		t.Fatalf("iface should survive mihomo error: %+v", batch.Interfaces)
	}
	if batch.Proxy != nil {
		t.Fatalf("proxy should be absent on collector error: %+v", batch.Proxy)
	}
	foundReset := false
	for _, gap := range batch.Gaps {
		if gap.Reason == protocol.GapCollectorReset {
			foundReset = true
		}
	}
	if !foundReset {
		t.Fatalf("expected collector_reset gap, got %+v", batch.Gaps)
	}
}

func TestCollectMergesNlbwmonDevicesWhenEnabled(t *testing.T) {
	var cfg Config
	cfg.SiteID = "site-test"
	cfg.NodeID = "node-test"
	cfg.Interval = 15 * time.Second
	cfg.Collectors.Iface = true
	cfg.Collectors.Nlbwmon = true

	a := newAgent(cfg, "boot-test", map[string]collector{
		"iface": fakeCollector{
			capability: protocol.CapIface,
			deltas:     []protocol.IfaceDelta{{Name: "br-lan", RxDelta: 10, TxDelta: 4}},
		},
		"nlbwmon": fakeCollector{
			capability: protocol.CapNlbwmon,
			devices: []protocol.DeviceDelta{{
				ObsIP: "192.168.0.10", ObsMAC: "02:00:00:00:00:0a",
				RxDelta: 7, TxDelta: 3, Source: protocol.SourceNlbwmon,
			}},
		},
	})
	batch := a.collect(context.Background(), time.Unix(1_700_000_000, 0).UTC())
	if !hasCapability(batch.Capabilities, protocol.CapNlbwmon) {
		t.Fatalf("capabilities = %+v", batch.Capabilities)
	}
	if len(batch.Devices) != 1 || batch.Devices[0].Source != protocol.SourceNlbwmon {
		t.Fatalf("nlbwmon devices missing: %+v", batch.Devices)
	}
}

func TestDisabledNlbwmonDoesNotReportDevices(t *testing.T) {
	var cfg Config
	cfg.SiteID = "site-test"
	cfg.NodeID = "node-test"
	cfg.Interval = 15 * time.Second
	cfg.Collectors.Nlbwmon = false

	a := newAgent(cfg, "boot-test", map[string]collector{
		"nlbwmon": fakeCollector{
			capability: protocol.CapNlbwmon,
			devices: []protocol.DeviceDelta{{
				ObsIP: "192.168.0.10", RxDelta: 7, TxDelta: 3, Source: protocol.SourceNlbwmon,
			}},
		},
	})
	batch := a.collect(context.Background(), time.Unix(1_700_000_000, 0).UTC())
	if hasCapability(batch.Capabilities, protocol.CapNlbwmon) {
		t.Fatalf("disabled nlbwmon leaked capability: %+v", batch.Capabilities)
	}
	if len(batch.Devices) != 0 {
		t.Fatalf("disabled nlbwmon leaked devices: %+v", batch.Devices)
	}
}

func hasCapability(caps []protocol.Capability, want protocol.Capability) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
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

func TestLoadConfigIfaceOptionsAndMetadataOmission(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("lt_credential_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Unconfigured iface options must keep batch.Metadata == nil.
	cfgDefaultPath := filepath.Join(dir, "default.json")
	rawDefault := `{
		"server_url":"https://collector.example.test",
		"site_id":"site-test",
		"node_id":"node-test",
		"token_file":` + mustJSON(t, tokenPath) + `,
		"collectors":{"iface":true}
	}`
	if err := os.WriteFile(cfgDefaultPath, []byte(rawDefault), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgDefault, err := LoadConfig(cfgDefaultPath)
	if err != nil {
		t.Fatal(err)
	}
	aDefault := newAgent(cfgDefault, "boot-default", nil)
	batchDefault := aDefault.collect(context.Background(), time.Unix(1_700_000_000, 0).UTC())
	if batchDefault.Metadata != nil {
		t.Fatalf("expected nil metadata when iface options unconfigured, got %s", batchDefault.Metadata)
	}

	// Configured iface options serialize into batch.Metadata.
	cfgCustomPath := filepath.Join(dir, "custom.json")
	rawCustom := `{
		"server_url":"https://collector.example.test",
		"site_id":"site-test",
		"node_id":"node-test",
		"token_file":` + mustJSON(t, tokenPath) + `,
		"collectors":{"iface":true},
		"iface":{"exclude_prefixes":["lo","wg"],"include":["br-lan"]}
	}`
	if err := os.WriteFile(cfgCustomPath, []byte(rawCustom), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgCustom, err := LoadConfig(cfgCustomPath)
	if err != nil {
		t.Fatal(err)
	}
	aCustom := newAgent(cfgCustom, "boot-custom", nil)
	batchCustom := aCustom.collect(context.Background(), time.Unix(1_700_000_000, 0).UTC())
	if string(batchCustom.Metadata) != `{"iface_exclude_prefixes":["lo","wg"],"iface_include":["br-lan"]}` {
		t.Fatalf("unexpected iface metadata: %s", batchCustom.Metadata)
	}
}

func TestMihomoAdapterThrottlesMetaSyncAndSurvivesProviders404(t *testing.T) {
	var connCalls, proxyCalls, providerCalls atomic.Int32
	mihomoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connections":
			n := connCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				_, _ = w.Write([]byte(`{"connections":[{"id":"c1","upload":10,"download":20,"chains":["hk-01","PROXY"],"metadata":{"process":"curl","host":"a.example.com"}}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"connections":[{"id":"c1","upload":50,"download":120,"chains":["hk-01","PROXY"],"metadata":{"process":"curl","host":"a.example.com"}}]}`))
		case "/proxies":
			proxyCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"proxies":{"PROXY":{"name":"PROXY","type":"Selector","all":["hk-01","DIRECT"]},"hk-01":{"name":"hk-01","type":"Shadowsocks"},"GLOBAL":{"name":"GLOBAL","type":"Selector","all":["PROXY"]}}}`))
		case "/providers/proxies":
			providerCalls.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mihomoSrv.Close()

	client := mihomocollector.NewClient(mihomoSrv.URL, "", mihomoSrv.Client())
	col := mihomocollector.NewCollectorWithOptions(client, mihomocollector.Options{CollectDomains: true})
	adapter := &mihomoAdapter{
		inner:        col,
		client:       client,
		metaInterval: 5 * time.Minute,
	}

	var cfg Config
	cfg.SiteID = "site-test"
	cfg.NodeID = "node-test"
	cfg.Interval = 15 * time.Second
	cfg.Collectors.Iface = true
	cfg.Collectors.Mihomo = true
	a := newAgent(cfg, "boot-meta", map[string]collector{
		"iface": fakeCollector{
			capability: protocol.CapIface,
			deltas:     []protocol.IfaceDelta{{Name: "eth0", RxDelta: 300, TxDelta: 100}},
		},
		"mihomo": adapter,
	})

	t0 := time.Unix(1_700_000_000, 0).UTC()
	// First tick establishes connection baseline and syncs /proxies once (404 on providers is non-fatal).
	b1 := a.collect(context.Background(), t0)
	if len(b1.ProxyNodes) != 1 || b1.ProxyNodes[0] != "hk-01" {
		t.Fatalf("b1 proxy_nodes = %+v, want [hk-01] (excluding PROXY/GLOBAL/DIRECT)", b1.ProxyNodes)
	}
	// Second tick 15s later -> delta collected, /proxies throttled.
	b2 := a.collect(context.Background(), t0.Add(15*time.Second))
	if len(b2.ProxyNodes) != 0 {
		t.Fatalf("expected /proxies sync to be throttled at +15s, got %+v", b2.ProxyNodes)
	}
	if proxyCalls.Load() != 1 || providerCalls.Load() != 1 {
		t.Fatalf("expected 1 proxy/provider call during throttle window, got proxy=%d provider=%d", proxyCalls.Load(), providerCalls.Load())
	}

	var hasGroup, hasProc, hasDomain, hasIface bool
	for _, d := range b2.Dimensions {
		switch {
		case d.Type == protocol.DimGroup && d.Key == "PROXY" && d.RxDelta == 100 && d.TxDelta == 40:
			hasGroup = true
		case d.Type == protocol.DimProcess && d.Key == "curl" && d.RxDelta == 100 && d.TxDelta == 40:
			hasProc = true
		case d.Type == protocol.DimDomain && d.Key == "example.com" && d.RxDelta == 100 && d.TxDelta == 40:
			hasDomain = true
		case d.Type == protocol.DimIface && d.Key == "eth0" && d.RxDelta == 300 && d.TxDelta == 100:
			hasIface = true
		}
	}
	if !hasGroup || !hasProc || !hasDomain || !hasIface {
		t.Fatalf("missing expected dimensions in b2: %+v", b2.Dimensions)
	}
}

func TestRunHotReloadsDomainOptionsWithoutMutatingConfigFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("lt_cred_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mihomoTick atomic.Int32
	mihomoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connections":
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				for {
					n := mihomoTick.Add(1)
					dl := n * 100
					frame := `{"connections":[{"id":"c1","upload":` + mustJSONInt(int64(n*10)) + `,"download":` + mustJSONInt(int64(dl)) + `,"chains":["hk-01","PROXY"],"metadata":{"host":"sub.example.com"}}]}`
					if err := ws.Write(r.Context(), websocket.MessageText, []byte(frame)); err != nil {
						return
					}
					select {
					case <-r.Context().Done():
						return
					case <-time.After(15 * time.Millisecond):
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			n := mihomoTick.Add(1)
			dl := n * 100
			_, _ = w.Write([]byte(`{"connections":[{"id":"c1","upload":` + mustJSONInt(int64(n*10)) + `,"download":` + mustJSONInt(int64(dl)) + `,"chains":["hk-01","PROXY"],"metadata":{"host":"sub.example.com"}}]}`))
		case "/proxies":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"proxies":{"hk-01":{"name":"hk-01","type":"Shadowsocks"}}}`))
		case "/providers/proxies":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"providers":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mihomoSrv.Close()

	var batchCount atomic.Int32
	var sawDomainOn, sawDomainOff atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ingestSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		batch, err := protocol.Decode(raw)
		if err != nil {
			t.Error(err)
			return
		}
		hasDomain := false
		for _, d := range batch.Dimensions {
			if d.Type == protocol.DimDomain && d.Key == "example.com" {
				hasDomain = true
			}
		}
		n := batchCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case n == 1:
			// Enable collect_domains on first response.
			_, _ = w.Write([]byte(`{"status":"ok","duplicate":false,"collect_domains":true}`))
		case hasDomain && !sawDomainOn.Load():
			sawDomainOn.Store(true)
			// Revert collect_domains back to false (omitted in response).
			_, _ = w.Write([]byte(`{"status":"ok","duplicate":false}`))
		case !hasDomain && sawDomainOn.Load():
			sawDomainOff.Store(true)
			_, _ = w.Write([]byte(`{"status":"ok","duplicate":false}`))
			cancel()
		default:
			if sawDomainOn.Load() {
				_, _ = w.Write([]byte(`{"status":"ok","duplicate":false}`))
			} else {
				_, _ = w.Write([]byte(`{"status":"ok","duplicate":false,"collect_domains":true}`))
			}
		}
	}))
	defer ingestSrv.Close()

	cfgPath := filepath.Join(dir, "agent.json")
	origCfg := `{"server_url":` + mustJSON(t, ingestSrv.URL) + `,"site_id":"site-test","node_id":"node-test","token_file":` + mustJSON(t, tokenPath) + `,"interval":"35ms","collectors":{"iface":false,"mihomo":true},"mihomo":{"url":` + mustJSON(t, mihomoSrv.URL) + `}}`
	if err := os.WriteFile(cfgPath, []byte(origCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	runCtx, runCancel := context.WithTimeout(ctx, 3*time.Second)
	defer runCancel()
	_ = run(runCtx, cfg, "lt_cred_secret")

	if !sawDomainOn.Load() || !sawDomainOff.Load() {
		t.Fatalf("expected collect_domains toggle on then off, sawOn=%v sawOff=%v batches=%d", sawDomainOn.Load(), sawDomainOff.Load(), batchCount.Load())
	}
	afterCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCfg) != origCfg {
		t.Fatalf("agent.json was mutated on disk: got %s, want %s", afterCfg, origCfg)
	}
}

func TestMihomoAdapterRetriesMetaSyncAfterTransient503(t *testing.T) {
	var proxyHits, providerHits atomic.Int32
	mihomoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/connections":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"connections":[{"id":"b","upload":1,"download":1,"chains":["DIRECT"]}]}`))
		case "/proxies":
			if proxyHits.Add(1) == 1 {
				http.Error(w, "starting up", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"proxies":{"HK-01":{"name":"HK-01","type":"Shadowsocks"}}}`))
		case "/providers/proxies":
			if providerHits.Add(1) == 1 {
				http.Error(w, "starting up", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"providers":{"SubA":{"name":"SubA","vehicleType":"HTTP","proxies":[{"name":"HK-01"}]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mihomoSrv.Close()

	client := mihomocollector.NewClient(mihomoSrv.URL, "", mihomoSrv.Client())
	col := mihomocollector.NewCollectorWithOptions(client, mihomocollector.Options{})
	adapter := &mihomoAdapter{
		inner:        col,
		client:       client,
		metaInterval: 5 * time.Minute,
	}

	t0 := time.Unix(1_700_000_000, 0).UTC()
	snap1, err := adapter.Collect(context.Background(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap1.proxyNodes) != 0 {
		t.Fatalf("expected empty proxyNodes on 503, got %+v", snap1.proxyNodes)
	}

	// Next tick 15s later MUST retry /proxies & /providers/proxies because t0 failed with 503.
	snap2, err := adapter.Collect(context.Background(), t0.Add(15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap2.proxyNodes) != 1 || snap2.proxyNodes[0] != "HK-01" {
		t.Fatalf("expected HK-01 after retry at +15s, got %+v (proxyHits=%d providerHits=%d)", snap2.proxyNodes, proxyHits.Load(), providerHits.Load())
	}
	if len(snap2.providers) != 1 || snap2.providers[0].Name != "SubA" {
		t.Fatalf("expected SubA provider after retry, got %+v", snap2.providers)
	}
}

func TestRunRetriesOnIngest503AndDropsPermanent4xx(t *testing.T) {
	// 1. 503 twice then 200 succeeds on retry without duplicating.
	var seqs503 []uint64
	var attempts atomic.Int32
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	srv503 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b, err := protocol.Decode(raw)
		if err != nil {
			t.Error(err)
			return
		}
		if attempts.Add(1) <= 2 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		seqs503 = append(seqs503, b.Sequence)
		_, _ = w.Write([]byte(`{"status":"ok","duplicate":false}`))
		cancel1()
	}))
	defer srv503.Close()

	var cfg Config
	cfg.ServerURL = srv503.URL
	cfg.SiteID = "site-test"
	cfg.NodeID = "node-test"
	cfg.Interval = 20 * time.Millisecond
	runCtx1, runCancel1 := context.WithTimeout(ctx1, 8*time.Second)
	defer runCancel1()
	_ = run(runCtx1, cfg, "lt_cred_secret")
	if len(seqs503) == 0 || attempts.Load() < 3 {
		t.Fatalf("expected retries on 503 then delivery, attempts=%d seqs=%v", attempts.Load(), seqs503)
	}

	// 2. Permanent 400 rejection on seq 1 drops head batch so seq 2 delivers immediately.
	var deliveredSeqs []uint64
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	srv400 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b, err := protocol.Decode(raw)
		if err != nil {
			t.Error(err)
			return
		}
		if b.Sequence == 1 {
			http.Error(w, "invalid batch", http.StatusBadRequest)
			return
		}
		deliveredSeqs = append(deliveredSeqs, b.Sequence)
		_, _ = w.Write([]byte(`{"status":"ok","duplicate":false}`))
		cancel2()
	}))
	defer srv400.Close()

	cfg.ServerURL = srv400.URL
	runCtx2, runCancel2 := context.WithTimeout(ctx2, 4*time.Second)
	defer runCancel2()
	_ = run(runCtx2, cfg, "lt_cred_secret")
	if len(deliveredSeqs) == 0 || deliveredSeqs[0] != 2 {
		t.Fatalf("expected seq 2 delivered after seq 1 permanent 400 drop, got %v", deliveredSeqs)
	}
}

func TestLoadConfigErrorsAndQueueDropBounds(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("lt_cred_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Trailing JSON rejected by ensureJSONEOF.
	trailingPath := filepath.Join(dir, "trailing.json")
	_ = os.WriteFile(trailingPath, []byte(`{"server_url":"https://a.test","site_id":"s","node_id":"n","token_file":`+mustJSON(t, tokenPath)+`}{}`), 0o600)
	if _, err := LoadConfig(trailingPath); err == nil {
		t.Fatal("expected trailing JSON error")
	}

	// Invalid interval duration rejected.
	badDurPath := filepath.Join(dir, "baddur.json")
	_ = os.WriteFile(badDurPath, []byte(`{"server_url":"https://a.test","site_id":"s","node_id":"n","token_file":`+mustJSON(t, tokenPath)+`,"interval":"not-a-dur"}`), 0o600)
	if _, err := LoadConfig(badDurPath); err == nil {
		t.Fatal("expected invalid interval error")
	}

	// Whitespace inside token and multi-line secret rejected.
	wsPath := filepath.Join(dir, "ws.tok")
	_ = os.WriteFile(wsPath, []byte("lt_bad token"), 0o600)
	if _, err := loadToken(wsPath); err == nil {
		t.Fatal("expected whitespace token error")
	}
	nlSecPath := filepath.Join(dir, "nl.sec")
	_ = os.WriteFile(nlSecPath, []byte("line1\nline2\n"), 0o600)
	if _, err := loadSecret(nlSecPath); err == nil {
		t.Fatal("expected multi-line secret error")
	}
	if sec, err := loadSecret(""); err != nil || sec != "" {
		t.Fatalf("empty secret file = (%q, %v), want empty nil", sec, err)
	}

	// newBatchQueue(0) panics.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic on newBatchQueue(0)")
			}
		}()
		_ = newBatchQueue(0)
	}()

	// carryBufferDrop merges an earlier From bound.
	t0 := time.Unix(1_700_000_000, 0).UTC()
	b := protocol.Batch{
		Gaps: []protocol.Gap{{Reason: protocol.GapBufferDrop, From: t0.Add(10 * time.Second), To: t0.Add(20 * time.Second)}},
	}
	carryBufferDrop(&b, t0, t0.Add(30*time.Second))
	if !b.Gaps[0].From.Equal(t0) || !b.Gaps[0].To.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("carryBufferDrop bounds = %+v", b.Gaps[0])
	}

	// ifaceAdapter & nlbwmonAdapter Capability() and Collect() error paths.
	ctxCancel, cancel := context.WithCancel(context.Background())
	cancel()
	ia := ifaceAdapter{inner: ifacecollector.NewCollector()}
	if ia.Capability() != protocol.CapIface {
		t.Fatalf("ifaceAdapter capability = %q", ia.Capability())
	}
	if _, err := ia.Collect(ctxCancel, t0); err == nil {
		t.Fatal("expected ifaceAdapter Collect error on cancelled ctx")
	}
	na := nlbwmonAdapter{inner: nlbwmoncollector.NewCollector(func(context.Context) ([]byte, error) {
		return []byte(`{"columns":["mac","ip","rx_bytes","tx_bytes"],"data":[["02:00:00:00:00:0a","203.0.113.10",10,20]]}`), nil
	})}
	if na.Capability() != protocol.CapNlbwmon {
		t.Fatalf("nlbwmonAdapter capability = %q", na.Capability())
	}
	if _, err := na.Collect(context.Background(), t0); err != nil {
		t.Fatalf("nlbwmonAdapter Collect = %v", err)
	}
}

func mustJSONInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
