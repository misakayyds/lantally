package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misakayyds/lantally/internal/accounting"
	"github.com/misakayyds/lantally/internal/alert"
	"github.com/misakayyds/lantally/internal/collector/mihomo"
	"github.com/misakayyds/lantally/internal/collector/sim"
	"github.com/misakayyds/lantally/internal/enroll"
	"github.com/misakayyds/lantally/internal/identity"
	"github.com/misakayyds/lantally/internal/protocol"
	"github.com/misakayyds/lantally/internal/store/metrics"
	sqlitestore "github.com/misakayyds/lantally/internal/store/sqlite"
)

func syntheticToken(credentialID string, fill byte) string {
	return "lt_" + credentialID + "_" + strings.Repeat(string(fill), 32)
}

func testServer(t *testing.T) (*sqlitestore.Store, http.Handler) {
	t.Helper()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	token := syntheticToken("sim-cred", 'a')
	if err := store.CreateNode(
		context.Background(),
		"sim-node",
		"sim-site",
		"sim-cred",
		enroll.HashToken(token),
	); err != nil {
		t.Fatal(err)
	}
	return store, Routes(store)
}

func postBatch(t *testing.T, handler http.Handler, token string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

type failOnRead struct {
	reads int
}

func (r *failOnRead) Read([]byte) (int, error) {
	r.reads++
	return 0, io.ErrUnexpectedEOF
}

func TestIngestAuthenticatesBeforeReadingBody(t *testing.T) {
	_, handler := testServer(t)
	tests := []struct {
		name          string
		authorization string
	}{
		{name: "missing authorization"},
		{
			name:          "bad credential",
			authorization: "Bearer " + syntheticToken("missing", 'z'),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := &failOnRead{}
			req := httptest.NewRequest(http.MethodPost, "/v1/ingest", body)
			req.Header.Set("Content-Encoding", "gzip")
			req.Header.Set("Content-Type", "application/json")
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			if body.reads != 0 {
				t.Fatalf("unauthorized request body was read %d times", body.reads)
			}
		})
	}
}

func TestIngestRetryStoresOneBatchAndStablePayloadHash(t *testing.T) {
	store, handler := testServer(t)
	raw, err := protocol.Encode(sim.Snapshot(1, "boot-a"))
	if err != nil {
		t.Fatal(err)
	}
	testToken := syntheticToken("sim-cred", 'a')

	first := postBatch(t, handler, testToken, raw)
	if first.Code != http.StatusOK || first.Body.String() != "{\"status\":\"ok\",\"duplicate\":false}\n" {
		t.Fatalf("first response: status=%d body=%q", first.Code, first.Body.String())
	}
	second := postBatch(t, handler, testToken, raw)
	if second.Code != http.StatusOK || second.Body.String() != "{\"status\":\"ok\",\"duplicate\":true}\n" {
		t.Fatalf("retry response: status=%d body=%q", second.Code, second.Body.String())
	}

	count, err := store.BatchCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one stored batch, got %d", count)
	}
	sum := sha256.Sum256(raw)
	hash, err := store.BatchPayloadHash(context.Background(), "sim-node", "boot-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("payload hash = %q, want %q", hash, hex.EncodeToString(sum[:]))
	}

	totals, err := store.LedgerTotals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if totals["total"] != 1536 {
		t.Fatalf("node total after first ingest = %d, want iface 1024+512=1536", totals["total"])
	}
	secondTotals, err := store.LedgerTotals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if secondTotals["total"] != totals["total"] {
		t.Fatalf("duplicate ingest changed totals from %d to %d", totals["total"], secondTotals["total"])
	}

	devices, err := store.ListDeviceLedgers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected one resolved device, got %d", len(devices))
	}
	if devices[0].Bytes["total"] != 0 {
		t.Fatalf("neigh observation should not count as total, got %d", devices[0].Bytes["total"])
	}
}

func TestIngestRequiresGzipJSONContract(t *testing.T) {
	_, handler := testServer(t)
	gzipped, err := protocol.Encode(sim.Snapshot(1, "boot-contract"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sim.Snapshot(1, "boot-contract"))
	if err != nil {
		t.Fatal(err)
	}
	token := syntheticToken("sim-cred", 'a')

	tests := []struct {
		name            string
		body            []byte
		contentEncoding string
		contentType     string
	}{
		{
			name:        "missing content encoding",
			body:        gzipped,
			contentType: "application/json",
		},
		{
			name:            "wrong content type",
			body:            gzipped,
			contentEncoding: "gzip",
			contentType:     "text/plain",
		},
		{
			name:            "raw JSON labeled gzip",
			body:            raw,
			contentEncoding: "gzip",
			contentType:     "application/json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+token)
			if tt.contentEncoding != "" {
				req.Header.Set("Content-Encoding", tt.contentEncoding)
			}
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIngestRejectsBadAndRevokedTokens(t *testing.T) {
	store, handler := testServer(t)
	testToken := syntheticToken("sim-cred", 'a')
	raw, err := protocol.Encode(sim.Snapshot(1, "boot-a"))
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, syntheticToken("missing", 'z'), raw); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status=%d body=%q", rec.Code, rec.Body.String())
	}
	if err := store.RevokeNode(context.Background(), "sim-node"); err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, testToken, raw); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestIngestRejectsProtocolV2(t *testing.T) {
	_, handler := testServer(t)
	testToken := syntheticToken("sim-cred", 'a')
	batch := sim.Snapshot(1, "boot-a")
	batch.ProtocolVersion = 2
	raw, err := protocol.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	rec := postBatch(t, handler, testToken, raw)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "protocol_version 2") {
		t.Fatalf("body should name unsupported version, got %q", rec.Body.String())
	}
}

func TestIngestRejectsSequenceRegression(t *testing.T) {
	_, handler := testServer(t)
	testToken := syntheticToken("sim-cred", 'a')
	for _, seq := range []uint64{2, 1} {
		raw, err := protocol.Encode(sim.Snapshot(seq, "boot-a"))
		if err != nil {
			t.Fatal(err)
		}
		rec := postBatch(t, handler, testToken, raw)
		want := http.StatusOK
		if seq == 1 {
			want = http.StatusConflict
		}
		if rec.Code != want {
			t.Fatalf("sequence %d: status=%d body=%q", seq, rec.Code, rec.Body.String())
		}
	}
}

func TestIngestReturnsEmpty503WhenSQLiteUnavailable(t *testing.T) {
	store, handler := testServer(t)
	testToken := syntheticToken("sim-cred", 'a')
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(sim.Snapshot(1, "boot-a"))
	if err != nil {
		t.Fatal(err)
	}
	rec := postBatch(t, handler, testToken, raw)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected no ACK body, got %q", rec.Body.String())
	}
}

func TestIngestRejectsCredentialIdentityMismatch(t *testing.T) {
	_, handler := testServer(t)
	testToken := syntheticToken("sim-cred", 'a')
	batch := sim.Snapshot(1, "boot-a")
	batch.NodeID = "other-node"
	raw, err := protocol.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	rec := postBatch(t, handler, testToken, raw)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHealthzChecksSQLite(t *testing.T) {
	store, handler := testServer(t)
	request := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return rec
	}
	if rec := request(); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthy response: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if rec := request(); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy response: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestIngestAppliesVirtualIfaceExclusionsAndMetadataOverrides(t *testing.T) {
	store, handler := testServer(t)
	token := syntheticToken("sim-cred", 'a')

	// Sequence 1: virtual NICs excluded by default, eth0 counted, ListOutboundLedgers stays clean.
	batch1 := sim.Snapshot(1, "boot-iface")
	batch1.Devices = nil
	batch1.Interfaces = []protocol.IfaceDelta{
		{Name: "eth0", RxDelta: 1000, TxDelta: 500},
		{Name: "lo", RxDelta: 200, TxDelta: 200},
		{Name: "br-lan", RxDelta: 300, TxDelta: 100},
	}
	raw1, err := protocol.Encode(batch1)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, token, raw1); rec.Code != http.StatusOK {
		t.Fatalf("seq 1 status=%d body=%q", rec.Code, rec.Body.String())
	}
	totals, err := store.LedgerTotals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if totals["total"] != 1500 {
		t.Fatalf("total after seq 1 = %d, want 1500 (eth0 only)", totals["total"])
	}
	outbounds, err := store.ListOutboundLedgers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ob := range outbounds {
		if ob.Name == "eth0" || ob.Name == "lo" || ob.Name == "br-lan" {
			t.Fatalf("interface name leaked into ListOutboundLedgers: %+v", outbounds)
		}
	}

	// Sequence 2: explicit metadata iface_include opts in br-lan.
	batch2 := sim.Snapshot(2, "boot-iface")
	batch2.Devices = nil
	batch2.Proxy = nil
	batch2.Interfaces = []protocol.IfaceDelta{
		{Name: "br-lan", RxDelta: 400, TxDelta: 100},
		{Name: "lo", RxDelta: 99, TxDelta: 1},
	}
	batch2.Metadata = json.RawMessage(`{"iface_include":["br-lan"]}`)
	raw2, err := protocol.Encode(batch2)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, token, raw2); rec.Code != http.StatusOK {
		t.Fatalf("seq 2 status=%d body=%q", rec.Code, rec.Body.String())
	}
	totals, err = store.LedgerTotals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if totals["total"] != 2000 {
		t.Fatalf("total after seq 2 = %d, want 2000", totals["total"])
	}
}

func TestIngestSyncsCollectDomainsPreference(t *testing.T) {
	store, handler := testServer(t)
	token := syntheticToken("sim-cred", 'a')

	// Default false -> exact v0.1 JSON without collect_domains.
	raw1, err := protocol.Encode(sim.Snapshot(1, "boot-sync"))
	if err != nil {
		t.Fatal(err)
	}
	rec1 := postBatch(t, handler, token, raw1)
	if rec1.Code != http.StatusOK || rec1.Body.String() != "{\"status\":\"ok\",\"duplicate\":false}\n" {
		t.Fatalf("default prefs response = %d %q", rec1.Code, rec1.Body.String())
	}

	// Enable collect_domains -> fresh and duplicate retries return collect_domains: true.
	if err := store.SetNodeCollectDomains(context.Background(), "sim-node", true); err != nil {
		t.Fatal(err)
	}
	recDup := postBatch(t, handler, token, raw1)
	if recDup.Code != http.StatusOK {
		t.Fatalf("duplicate retry status = %d %q", recDup.Code, recDup.Body.String())
	}
	var respDup protocol.IngestResponse
	if err := json.Unmarshal(recDup.Body.Bytes(), &respDup); err != nil {
		t.Fatal(err)
	}
	if !respDup.Duplicate || respDup.CollectDomains == nil || !*respDup.CollectDomains {
		t.Fatalf("duplicate retry prefs mismatch: %+v", respDup)
	}

	// Disable collect_domains -> omitted again.
	if err := store.SetNodeCollectDomains(context.Background(), "sim-node", false); err != nil {
		t.Fatal(err)
	}
	raw2, err := protocol.Encode(sim.Snapshot(2, "boot-sync"))
	if err != nil {
		t.Fatal(err)
	}
	rec2 := postBatch(t, handler, token, raw2)
	if rec2.Code != http.StatusOK || rec2.Body.String() != "{\"status\":\"ok\",\"duplicate\":false}\n" {
		t.Fatalf("reverted prefs response = %d %q", rec2.Code, rec2.Body.String())
	}
}

func TestIngestPersistsDimensionsAndProxyNodesWithoutDoubleCountingIface(t *testing.T) {
	store, handler := testServer(t)
	token := syntheticToken("sim-cred", 'a')

	batch := sim.Snapshot(1, "boot-dim")
	batch.Interfaces = []protocol.IfaceDelta{
		{Name: "eth0", RxDelta: 500, TxDelta: 200},
		{Name: "wg0", RxDelta: 100, TxDelta: 50},
	}
	batch.Dimensions = []protocol.DimensionDelta{
		{Type: protocol.DimIface, Key: "eth0", RxDelta: 500, TxDelta: 200},
		{Type: protocol.DimIface, Key: "wg0", RxDelta: 100, TxDelta: 50},
		{Type: protocol.DimGroup, Key: "PROXY", RxDelta: 300, TxDelta: 100},
		{Type: protocol.DimProcess, Key: "curl", RxDelta: 200, TxDelta: 50},
	}
	batch.ProxyNodes = []string{"hk-01", "DIRECT", "GLOBAL"}
	batch.Providers = []protocol.ProviderSnapshot{{
		Name:        "sub-alpha",
		VehicleType: "HTTP",
		Proxies:     []string{"hk-01", "jp-02", "REJECT"},
	}}
	raw, err := protocol.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, token, raw); rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d %s", rec.Code, rec.Body.String())
	}

	ifaceStats, err := store.ListDimensionStats(context.Background(), sqlitestore.DimensionQuery{
		Type: "iface",
		From: batch.SampledAt.Add(-time.Hour),
		To:   batch.SampledAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ifaceStats.TotalRx != 600 || ifaceStats.TotalTx != 250 {
		t.Fatalf("iface dimensions double-counted or missing: %+v", ifaceStats)
	}

	catalog, err := store.ListProxyNodeNames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 || catalog[0] != "hk-01" || catalog[1] != "jp-02" {
		t.Fatalf("proxy_node_catalog = %+v, want [hk-01 jp-02]", catalog)
	}
}

func TestIngestDeliversWatchDomainsAndSyncsProviders(t *testing.T) {
	store, handler := testServer(t)
	token := syntheticToken("sim-cred", 'a')

	if _, err := store.CreateWatchItem(context.Background(), accounting.WatchItem{
		Name: "WatchDomain",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDomain: {Type: accounting.MatchExact, Values: []string{"api.watched.test"}},
		},
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWatchItem(context.Background(), accounting.WatchItem{
		Name: "WatchKW",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDomain: {Type: accounting.MatchKeyword, Values: []string{"openai"}},
		},
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	batch := sim.Snapshot(1, "boot-m3")
	batch.Providers = []protocol.ProviderSnapshot{
		{Name: "default", VehicleType: "Compatible"},
		{Name: "AirportA", VehicleType: "HTTP", Upload: 100, Download: 400, Total: 10000, Expire: 1893456000, Proxies: []string{"hk-01", "DIRECT"}},
	}
	raw, err := protocol.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	rec := postBatch(t, handler, token, raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d %s", rec.Code, rec.Body.String())
	}

	var resp protocol.IngestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.CollectDomains != nil {
		t.Fatalf("collect_domains must remain omitted when false, got %v", *resp.CollectDomains)
	}
	if len(resp.WatchDomains) != 2 || resp.WatchDomains[0] != "api.watched.test" || resp.WatchDomains[1] != "kw:openai" {
		t.Fatalf("watch_domains = %v, want [api.watched.test kw:openai]", resp.WatchDomains)
	}

	subs, err := store.ListProviderSubscriptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Name != "AirportA" || subs[0].UsedBytes != 500 || subs[0].TotalBytes != 10000 {
		t.Fatalf("synced providers = %+v", subs)
	}
}

func TestEndToEndSubdomainKeywordRegexWatchDomains(t *testing.T) {
	store, handler := testServer(t)
	token := syntheticToken("sim-cred", 'a')
	ctx := context.Background()

	wKW, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "SubdomainKW",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDomain: {Type: accounting.MatchKeyword, Values: []string{"video"}},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wRE, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "SubdomainRE",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDomain: {Type: accounting.MatchRegex, Values: []string{`^api\.service\.org$`}},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	collect, watchDomains, err := store.NodeDomainPreferences(ctx, "sim-node")
	if err != nil {
		t.Fatal(err)
	}
	if collect {
		t.Fatalf("expected collect_domains=false by default")
	}

	frame := []byte(`{"connections":[
		{"id":"c1","upload":100,"download":900,"chains":["hk-01","Proxy"],"metadata":{"host":"video.cdn.example.com"}},
		{"id":"c2","upload":200,"download":1800,"chains":["hk-01","Proxy"],"metadata":{"host":"api.service.org"}},
		{"id":"c3","upload":5000,"download":5000,"chains":["hk-01","Proxy"],"metadata":{"host":"secret.unmatched.example.net"}}
	]}`)
	snap, _, err := mihomo.ParseConnectionsWithOptions(
		map[string]mihomo.ConnCounters{"baseline": {Upload: 1, Download: 1}},
		frame,
		mihomo.Options{
			CollectDomains: collect,
			WatchDomains:   watchDomains,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	batch := sim.Snapshot(1, "boot-watch-e2e")
	batch.SampledAt = time.Now().UTC()
	batch.Dimensions = make([]protocol.DimensionDelta, 0, len(snap.Dimensions))
	for _, d := range snap.Dimensions {
		batch.Dimensions = append(batch.Dimensions, protocol.DimensionDelta{
			Type:    protocol.DimensionType(d.Type),
			Key:     d.Key,
			RxDelta: d.RxDelta,
			TxDelta: d.TxDelta,
		})
	}
	raw, err := protocol.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, token, raw); rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d %s", rec.Code, rec.Body.String())
	}

	stKW, err := store.EvaluateWatchItem(ctx, wKW, time.Time{}, time.Time{}, 0, time.UTC, batch.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	if stKW.Rx != 900 || stKW.Tx != 100 || stKW.Total != 1000 {
		t.Fatalf("kw:video watch status = rx:%d tx:%d total:%d, want 900/100/1000", stKW.Rx, stKW.Tx, stKW.Total)
	}

	stRE, err := store.EvaluateWatchItem(ctx, wRE, time.Time{}, time.Time{}, 0, time.UTC, batch.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	if stRE.Rx != 1800 || stRE.Tx != 200 || stRE.Total != 2000 {
		t.Fatalf("re:^api.service.org$ watch status = rx:%d tx:%d total:%d, want 1800/200/2000", stRE.Rx, stRE.Tx, stRE.Total)
	}

	domStats, err := store.ListDimensionStats(ctx, sqlitestore.DimensionQuery{
		Type: "domain",
		From: batch.SampledAt.Add(-time.Hour),
		To:   batch.SampledAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(domStats.Items) != 2 {
		t.Fatalf("expected exactly 2 watched domains with zero leakage, got %+v", domStats.Items)
	}
	for _, item := range domStats.Items {
		if item.Key != "video.cdn.example.com" && item.Key != "api.service.org" {
			t.Fatalf("unexpected leaked domain %q in %+v", item.Key, domStats.Items)
		}
	}
}

func TestExactParentWatchDomainsDoesNotMaskKeywordRegexSubdomains(t *testing.T) {
	store, handler := testServer(t)
	token := syntheticToken("sim-cred", 'a')
	ctx := context.Background()

	parentCDN, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "ParentCDN",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDomain: {Type: accounting.MatchExact, Values: []string{"cdn.example.com"}},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	videoSubKW, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "VideoSubKW",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDomain: {Type: accounting.MatchKeyword, Values: []string{"video"}},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	audioSubRE, err := store.CreateWatchItem(ctx, accounting.WatchItem{
		Name: "AudioSubRE",
		Matchers: accounting.WatchMatcherMap{
			accounting.WatchDimDomain: {Type: accounting.MatchRegex, Values: []string{`^audio\.cdn\.example\.com$`}},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	collect, watchDomains, err := store.NodeDomainPreferences(ctx, "sim-node")
	if err != nil {
		t.Fatal(err)
	}
	if collect {
		t.Fatalf("expected collect_domains=false by default")
	}

	frame := []byte(`{"connections":[
		{"id":"c1","upload":200,"download":800,"chains":["hk-01","Proxy"],"metadata":{"host":"video.cdn.example.com"}},
		{"id":"c2","upload":300,"download":700,"chains":["hk-01","Proxy"],"metadata":{"host":"audio.cdn.example.com"}},
		{"id":"c3","upload":4000,"download":6000,"chains":["hk-01","Proxy"],"metadata":{"host":"secret.unmatched.example.net"}}
	]}`)
	snap, _, err := mihomo.ParseConnectionsWithOptions(
		map[string]mihomo.ConnCounters{"baseline": {Upload: 1, Download: 1}},
		frame,
		mihomo.Options{
			CollectDomains: collect,
			WatchDomains:   watchDomains,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	batch := sim.Snapshot(1, "boot-watch-overlap")
	batch.SampledAt = time.Now().UTC()
	batch.Dimensions = make([]protocol.DimensionDelta, 0, len(snap.Dimensions))
	for _, d := range snap.Dimensions {
		batch.Dimensions = append(batch.Dimensions, protocol.DimensionDelta{
			Type:    protocol.DimensionType(d.Type),
			Key:     d.Key,
			RxDelta: d.RxDelta,
			TxDelta: d.TxDelta,
		})
	}
	raw, err := protocol.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, token, raw); rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d %s", rec.Code, rec.Body.String())
	}

	stParent, err := store.EvaluateWatchItem(ctx, parentCDN, time.Time{}, time.Time{}, 0, time.UTC, batch.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	if stParent.Total != 2000 {
		t.Fatalf("ParentCDN total = %d, want 2000", stParent.Total)
	}

	stKW, err := store.EvaluateWatchItem(ctx, videoSubKW, time.Time{}, time.Time{}, 0, time.UTC, batch.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	if stKW.Total != 1000 || stKW.Rx != 800 || stKW.Tx != 200 {
		t.Fatalf("VideoSubKW status = rx:%d tx:%d total:%d, want 800/200/1000", stKW.Rx, stKW.Tx, stKW.Total)
	}

	stRE, err := store.EvaluateWatchItem(ctx, audioSubRE, time.Time{}, time.Time{}, 0, time.UTC, batch.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	if stRE.Total != 1000 || stRE.Rx != 700 || stRE.Tx != 300 {
		t.Fatalf("AudioSubRE status = rx:%d tx:%d total:%d, want 700/300/1000", stRE.Rx, stRE.Tx, stRE.Total)
	}

	domStats, err := store.ListDimensionStats(ctx, sqlitestore.DimensionQuery{
		Type: "domain",
		From: batch.SampledAt.Add(-time.Hour),
		To:   batch.SampledAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(domStats.Items) != 2 {
		t.Fatalf("expected exactly 2 watched domains with zero leakage, got %+v", domStats.Items)
	}
	for _, item := range domStats.Items {
		if item.Key != "video.cdn.example.com" && item.Key != "audio.cdn.example.com" {
			t.Fatalf("unexpected domain %q in %+v", item.Key, domStats.Items)
		}
	}
}

func TestIngestRebootBatchResolvesSilenceAlertAndRaisesGapAlerts(t *testing.T) {
	store, handler := testServer(t)
	token := syntheticToken("sim-cred", 'a')
	ctx := context.Background()

	// Seed boot-1 and raise an open silence alert.
	raw1, err := protocol.Encode(sim.Snapshot(1, "boot-1"))
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, token, raw1); rec.Code != http.StatusOK {
		t.Fatalf("boot-1 ingest = %d %s", rec.Code, rec.Body.String())
	}
	if err := store.RaiseAlert(ctx, alert.Record{
		Kind:        alert.KindSilence,
		SiteID:      "sim-site",
		NodeID:      "sim-node",
		Message:     "node stopped reporting within expected interval window",
		ObservedAt:  time.Now().UTC(),
		Fingerprint: "silence|sim-node",
	}); err != nil {
		t.Fatal(err)
	}

	// Send boot-2 reboot batch also carrying a buffer_drop gap.
	b2 := sim.Snapshot(1, "boot-2")
	now := time.Now().UTC()
	b2.Gaps = []protocol.Gap{
		{Reason: protocol.GapBufferDrop, From: now.Add(-time.Minute), To: now},
	}
	raw2, err := protocol.Encode(b2)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, handler, token, raw2); rec.Code != http.StatusOK {
		t.Fatalf("boot-2 ingest = %d %s", rec.Code, rec.Body.String())
	}

	alerts, err := store.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hasRebootReset, hasBufferDropReset bool
	for _, al := range alerts {
		if al.Kind == string(alert.KindSilence) && al.NodeID == "sim-node" {
			t.Fatalf("silence alert was not resolved on reboot batch: %+v", al)
		}
		if al.Kind == string(alert.KindReset) && al.NodeID == "sim-node" {
			if strings.Contains(al.Message, "buffer_drop") {
				hasBufferDropReset = true
			} else if strings.Contains(al.Message, "reset") {
				hasRebootReset = true
			}
		}
	}
	if !hasRebootReset || !hasBufferDropReset {
		t.Fatalf("expected both reboot and buffer_drop reset alerts, got %+v", alerts)
	}
}

func TestIngestRejectsOversizedGzipAndSiteMismatchAndConflictingDeviceAndMetrics(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "lantally.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	token := syntheticToken("sim-cred", 'a')
	if err := store.CreateNode(context.Background(), "sim-node", "sim-site", "sim-cred", enroll.HashToken(token)); err != nil {
		t.Fatal(err)
	}

	var metricWrites int
	metricSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metricWrites++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer metricSrv.Close()

	h := NewHandler(store)
	h.SetMetrics(metrics.NewWriter(metricSrv.URL, metricSrv.Client()))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", h.Ingest)

	// 1. Oversized gzip payload (>MaxDecodeSize) -> 400.
	oversized := append([]byte{0x1f, 0x8b}, bytes.Repeat([]byte("a"), protocol.MaxDecodeSize)...)
	if rec := postBatch(t, mux, token, oversized); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body = %d, want 400", rec.Code)
	}

	// 2. SiteID mismatch -> 401.
	siteMismatch := sim.Snapshot(1, "boot-m")
	siteMismatch.SiteID = "other-site"
	rawMismatch, _ := protocol.Encode(siteMismatch)
	if rec := postBatch(t, mux, token, rawMismatch); rec.Code != http.StatusUnauthorized {
		t.Fatalf("site mismatch = %d, want 401", rec.Code)
	}

	// 3. Conflicting device identity evidence is skipped without failing batch; SetMetrics writes once.
	dupMAC := "02:00:00:00:00:77"
	resv := identity.NewResolver(store, "sim-node")
	if _, _, err := resv.Resolve("sim-site", protocol.DeviceDelta{ObsIP: "203.0.113.70", ObsMAC: dupMAC, Source: protocol.SourceNeigh}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	devB, _, err := resv.Resolve("sim-site", protocol.DeviceDelta{ObsIP: "203.0.113.71", ObsMAC: "02:00:00:00:00:78", Source: protocol.SourceNeigh}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AttachIdentityEvidence("sim-site", devB, 1, dupMAC, "sim-node", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	b := sim.Snapshot(1, "boot-m")
	b.Devices = []protocol.DeviceDelta{{
		ObsIP: "203.0.113.77", ObsMAC: dupMAC, RxDelta: 500, TxDelta: 100, Source: protocol.SourceNlbwmon,
	}}
	rawOK, err := protocol.Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postBatch(t, mux, token, rawOK); rec.Code != http.StatusOK {
		t.Fatalf("conflicting device batch = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if metricWrites != 1 {
		t.Fatalf("metricWrites after new batch = %d, want 1", metricWrites)
	}
	// Duplicate retry must not emit duplicate metric writes.
	if rec := postBatch(t, mux, token, rawOK); rec.Code != http.StatusOK {
		t.Fatalf("duplicate batch = %d %s", rec.Code, rec.Body.String())
	}
	if metricWrites != 1 {
		t.Fatalf("metricWrites after duplicate batch = %d, want still 1", metricWrites)
	}
}
