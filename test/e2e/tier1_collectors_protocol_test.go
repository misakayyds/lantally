//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTier1_IngestProtocol(t *testing.T) {
	t.Run("F1_ValidGzipV1BatchAccepted", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f1-1")

		batch := syntheticBatch("site-e2e", "node-f1-1", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1024, "tx_delta": 512}),
		)
		status, body := postIngestBatch(t, srv.BaseURL, tok, batch)
		if status != http.StatusOK {
			t.Fatalf("ingest status = %d, want 200; body=%s", status, string(body))
		}
		resp := decodeMap(t, body)
		if asString(resp["status"]) != "ok" || asBool(resp["duplicate"]) {
			t.Fatalf("unexpected ingest response: %s", string(body))
		}

		ovStatus, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovStatus != http.StatusOK {
			t.Fatalf("overview status = %d; body=%s", ovStatus, string(ovBody))
		}
		ov := decodeMap(t, ovBody)
		bytesMap := asMap(ov["bytes"])
		total, _ := asUint64(bytesMap["total"])
		if total != 1536 {
			t.Fatalf("overview bytes.total = %d, want 1536; body=%s", total, string(ovBody))
		}
	})

	t.Run("F1_DuplicateRetryIdempotent", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f1-dup")

		batch := syntheticBatch("site-e2e", "node-f1-dup", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 2000, "tx_delta": 500}),
		)
		s1, b1 := postIngestBatch(t, srv.BaseURL, tok, batch)
		if s1 != http.StatusOK || asBool(decodeMap(t, b1)["duplicate"]) {
			t.Fatalf("first ingest failed: %d %s", s1, string(b1))
		}
		s2, b2 := postIngestBatch(t, srv.BaseURL, tok, batch)
		if s2 != http.StatusOK || !asBool(decodeMap(t, b2)["duplicate"]) {
			t.Fatalf("duplicate ingest = %d %s, want 200 duplicate=true", s2, string(b2))
		}

		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		ov := decodeMap(t, ovBody)
		total, _ := asUint64(asMap(ov["bytes"])["total"])
		if total != 2500 {
			t.Fatalf("bytes.total = %d, want 2500 (no double count)", total)
		}
	})

	t.Run("F1_NewBootIDResetsSequenceSpace", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f1-boot")

		now := time.Now().UTC()
		for seq := uint64(1); seq <= 2; seq++ {
			b := syntheticBatch("site-e2e", "node-f1-boot", "boot-a", seq, now.Add(time.Duration(seq)*time.Second),
				withCapabilities("iface"),
				withInterfaces(map[string]any{"name": "eth0", "rx_delta": 100, "tx_delta": 50}),
			)
			st, body := postIngestBatch(t, srv.BaseURL, tok, b)
			if st != http.StatusOK {
				t.Fatalf("boot-a seq %d failed: %d %s", seq, st, string(body))
			}
		}

		b2 := syntheticBatch("site-e2e", "node-f1-boot", "boot-b", 1, now.Add(5*time.Second),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 200, "tx_delta": 100}),
		)
		st, body := postIngestBatch(t, srv.BaseURL, tok, b2)
		if st != http.StatusOK || asBool(decodeMap(t, body)["duplicate"]) {
			t.Fatalf("boot-b seq 1 = %d %s, want 200 duplicate=false", st, string(body))
		}

		aSt, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		if aSt != http.StatusOK {
			t.Fatalf("GET /v1/alerts status = %d; body=%s", aSt, string(aBody))
		}
		alerts := asSlice(decodeMap(t, aBody)["alerts"])
		foundReset := false
		for _, item := range alerts {
			if asString(asMap(item)["kind"]) == "reset" {
				foundReset = true
			}
		}
		if !foundReset {
			t.Fatalf("expected reset alert after boot_id change; alerts=%s", string(aBody))
		}
	})

	t.Run("F1_EmptyArraysAndOmittedOptionals", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f1-empty")

		batch := syntheticBatch("site-e2e", "node-f1-empty", "boot-1", 1, time.Now().UTC())
		st, body := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("empty arrays batch status = %d, want 200; body=%s", st, string(body))
		}
	})

	t.Run("F1_V02OmitemptyExtensionsAccepted", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f1-v2")

		batch := syntheticBatch("site-e2e", "node-f1-v2", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{
				"name": "HK-IEPL-01", "group": "AI",
				"direct_rx": 0, "direct_tx": 0, "proxy_rx": 800, "proxy_tx": 200, "unadjusted": false,
			}),
			withDimensions(
				map[string]any{"type": "group", "key": "AI", "rx_delta": 800, "tx_delta": 200},
				map[string]any{"type": "process", "key": "curl", "rx_delta": 800, "tx_delta": 200},
			),
			withProxyNodes("HK-IEPL-01", "US-Plain"),
			withProviders(map[string]any{
				"name": "AirportA", "vehicle_type": "HTTP",
				"upload": 1024, "download": 4096, "total": 1073741824, "expire": 1893456000,
				"proxies": []string{"HK-IEPL-01"},
			}),
		)
		st, body := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("v0.2 omitempty batch status = %d, want 200; body=%s", st, string(body))
		}

		trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		if trSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=process status = %d, want 200; body=%s", trSt, string(trBody))
		}
		totals := asMap(decodeMap(t, trBody)["totals"])
		curlTotal, _ := asUint64(totals["curl"])
		if curlTotal != 1000 {
			t.Fatalf("process curl total = %d, want 1000; body=%s", curlTotal, string(trBody))
		}
	})
}

func TestTier1_HeartbeatSync(t *testing.T) {
	t.Run("F2_DefaultPrefsPreserveExactV01Response", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f2-def")

		batch := syntheticBatch("site-e2e", "node-f2-def", "boot-1", 1, time.Now().UTC())
		st, body := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest status = %d; body=%s", st, string(body))
		}
		if string(body) != "{\"status\":\"ok\",\"duplicate\":false}\n" {
			t.Fatalf("ingest body = %q, want exact v0.1 JSON", string(body))
		}
	})

	t.Run("F2_NodeCollectDomainsEnabledInResponse", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f2-cfg")

		cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-f2-cfg/config", map[string]any{
			"collect_domains": true,
		})
		if cSt != http.StatusOK {
			t.Fatalf("PUT /v1/nodes/node-f2-cfg/config status = %d; body=%s", cSt, string(cBody))
		}

		batch := syntheticBatch("site-e2e", "node-f2-cfg", "boot-1", 1, time.Now().UTC())
		st, body := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest status = %d; body=%s", st, string(body))
		}
		m := decodeMap(t, body)
		cd, ok := m["collect_domains"].(bool)
		if !ok || !cd {
			t.Fatalf("expected collect_domains=true in ingest response; body=%s", string(body))
		}
	})

	t.Run("F2_WatchDomainsDeliveredInResponse", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f2-watch")

		wSt, wBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name": "API Watch",
			"matchers": map[string]any{
				"domain": map[string]any{"type": "exact", "values": []string{"api.watched.test"}},
			},
		})
		if wSt != http.StatusOK && wSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch status = %d; body=%s", wSt, string(wBody))
		}

		batch := syntheticBatch("site-e2e", "node-f2-watch", "boot-1", 1, time.Now().UTC())
		st, body := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest status = %d; body=%s", st, string(body))
		}
		m := decodeMap(t, body)
		var found bool
		for _, v := range asSlice(m["watch_domains"]) {
			if asString(v) == "api.watched.test" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected api.watched.test in watch_domains; body=%s", string(body))
		}
	})

	t.Run("F2_AgentHotReloadsDomainsOnWithoutRestart", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f2-hot-on")

		mihomo := newMockMihomo(t, "e2e-secret")
		mihomo.SetConnections([]MockMihomoConn{{
			ID: "c1", Upload: 100, Download: 200, Chains: []string{"HK-01", "Proxy"},
			Metadata: MockMihomoMetadata{SourceIP: "10.203.113.10", Host: "api.first.example"},
		}})

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f2-hot-on", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "e2e-secret")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			return mihomo.ConnRequestCount() >= 1
		})
		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "c1", Upload: 300, Download: 600, Chains: []string{"HK-01", "Proxy"},
			Metadata: MockMihomoMetadata{SourceIP: "10.203.113.10", Host: "api.first.example"},
		}})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			tot, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["total"])
			return tot >= 600
		})

		// Domains must be absent before opt-in.
		dSt, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if dSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=domain status = %d; body=%s", dSt, string(dBody))
		}
		if len(asMap(decodeMap(t, dBody)["totals"])) != 0 {
			t.Fatalf("expected 0 domains before opt-in; body=%s", string(dBody))
		}

		// Enable collect_domains on server without restarting agent.
		cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-f2-hot-on/config", map[string]any{
			"collect_domains": true,
		})
		if cSt != http.StatusOK {
			t.Fatalf("PUT config status = %d; body=%s", cSt, string(cBody))
		}

		// Wait one heartbeat cycle for agent to pick up collect_domains=true, then push new traffic.
		time.Sleep(350 * time.Millisecond)
		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "c2", Upload: 400, Download: 800, Chains: []string{"HK-01", "Proxy"},
			Metadata: MockMihomoMetadata{SourceIP: "10.203.113.10", Host: "cdn.second.example"},
		}})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if st != http.StatusOK {
				return false
			}
			tot, _ := asUint64(asMap(decodeMap(t, b)["totals"])["second.example"])
			return tot >= 1200
		})
	})

	t.Run("F2_AgentHotDisablesDomainsOnRevert", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f2-hot-off")

		cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-f2-hot-off/config", map[string]any{
			"collect_domains": true,
		})
		if cSt != http.StatusOK {
			t.Fatalf("enable collect_domains failed: %d %s", cSt, string(cBody))
		}

		mihomo := newMockMihomo(t, "e2e-secret")
		mihomo.SetConnections([]MockMihomoConn{{
			ID: "c1", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"},
			Metadata: MockMihomoMetadata{Host: "a.before.example"},
		}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f2-hot-off", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "e2e-secret")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			return mihomo.ConnRequestCount() >= 1
		})
		time.Sleep(250 * time.Millisecond)
		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "c1", Upload: 300, Download: 500, Chains: []string{"HK-01", "Proxy"},
			Metadata: MockMihomoMetadata{Host: "a.before.example"},
		}})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if st != http.StatusOK {
				return false
			}
			tot, _ := asUint64(asMap(decodeMap(t, b)["totals"])["before.example"])
			return tot >= 600
		})

		// Revert collect_domains=false without restarting agent.
		cSt2, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-f2-hot-off/config", map[string]any{
			"collect_domains": false,
		})
		if cSt2 != http.StatusOK {
			t.Fatalf("disable collect_domains failed: %d", cSt2)
		}
		time.Sleep(400 * time.Millisecond)

		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "c2", Upload: 500, Download: 500, Chains: []string{"HK-01", "Proxy"},
			Metadata: MockMihomoMetadata{Host: "b.after.example"},
		}})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			tot, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["total"])
			return tot >= 1600
		})
		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if _, exists := asMap(decodeMap(t, dBody)["totals"])["after.example"]; exists {
			t.Fatalf("after.example should not be collected after collect_domains=false; body=%s", string(dBody))
		}
	})
}

func TestTier1_MihomoChains(t *testing.T) {
	t.Run("F3_TwoHopExtractsExitAndGroup", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f3-2hop")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{
			ID: "c1", Upload: 100, Download: 200, Chains: []string{"HK-IEPL-01", "AI"},
		}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f3-2hop", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		time.Sleep(150 * time.Millisecond)
		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "c1", Upload: 600, Download: 1200, Chains: []string{"HK-IEPL-01", "AI"},
		}})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			tot, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return tot == 1500
		})

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		foundExit := false
		for _, item := range asSlice(decodeMap(t, pBody)["outbounds"]) {
			ob := asMap(item)
			if asString(ob["name"]) == "HK-IEPL-01" {
				raw, _ := asUint64(ob["raw"])
				if raw != 1500 {
					t.Fatalf("HK-IEPL-01 raw = %d, want 1500", raw)
				}
				foundExit = true
			}
			if asString(ob["name"]) == "AI" {
				t.Fatalf("strategy group AI must not be recorded as outbound; body=%s", string(pBody))
			}
		}
		if !foundExit {
			t.Fatalf("missing HK-IEPL-01 outbound; body=%s", string(pBody))
		}

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		if gSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=group status = %d; body=%s", gSt, string(gBody))
		}
		aiTot, _ := asUint64(asMap(decodeMap(t, gBody)["totals"])["AI"])
		if aiTot != 1500 {
			t.Fatalf("group AI total = %d, want 1500; body=%s", aiTot, string(gBody))
		}
	})

	t.Run("F3_ThreeHopExtractsLeafAndRootGroup", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f3-3hop")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{
			ID: "c1", Upload: 50, Download: 50, Chains: []string{"JP-02", "Auto-Best", "Streaming"},
		}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f3-3hop", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		time.Sleep(150 * time.Millisecond)
		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "c1", Upload: 350, Download: 650, Chains: []string{"JP-02", "Auto-Best", "Streaming"},
		}})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			tot, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return tot == 900
		})

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		foundJP := false
		for _, item := range asSlice(decodeMap(t, pBody)["outbounds"]) {
			if asString(asMap(item)["name"]) == "JP-02" {
				foundJP = true
			}
		}
		if !foundJP {
			t.Fatalf("expected outbound JP-02; body=%s", string(pBody))
		}

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		if gSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=group status = %d; body=%s", gSt, string(gBody))
		}
		stTot, _ := asUint64(asMap(decodeMap(t, gBody)["totals"])["Streaming"])
		if stTot != 900 {
			t.Fatalf("group Streaming total = %d, want 900; body=%s", stTot, string(gBody))
		}
	})

	t.Run("F3_DirectChainMapsToDirectLedger", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f3-direct")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{
			ID: "c1", Upload: 100, Download: 200, Chains: []string{"DIRECT"},
		}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f3-direct", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		time.Sleep(150 * time.Millisecond)
		mihomo.SetConnections([]MockMihomoConn{{
			ID: "c1", Upload: 600, Download: 1200, Chains: []string{"DIRECT"},
		}})
		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "c1", Upload: 600, Download: 1200, Chains: []string{"DIRECT"},
		}})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			dir, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["direct"])
			return dir == 1500
		})

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		pm := decodeMap(t, pBody)
		pr, _ := asUint64(asMap(pm["proxy"])["proxy_raw"])
		if pr != 0 {
			t.Fatalf("proxy_raw = %d, want 0 for DIRECT", pr)
		}
		for _, item := range asSlice(pm["outbounds"]) {
			if asString(asMap(item)["name"]) == "DIRECT" {
				t.Fatalf("DIRECT must not appear in proxy outbounds; body=%s", string(pBody))
			}
		}
	})

	t.Run("F3_MixedConnectionsAggregateByExitAndGroup", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f3-mix")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "base", Upload: 10, Download: 10, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f3-mix", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		time.Sleep(150 * time.Millisecond)
		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "base", Upload: 10, Download: 10, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 100, Download: 200, Chains: []string{"HK-01", "AI"}},
			{ID: "c2", Upload: 150, Download: 250, Chains: []string{"HK-01", "Coding"}},
			{ID: "c3", Upload: 50, Download: 150, Chains: []string{"US-01", "AI"}},
		})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			tot, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return tot == 900
		})

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		gotOut := map[string]uint64{}
		for _, item := range asSlice(decodeMap(t, pBody)["outbounds"]) {
			ob := asMap(item)
			r, _ := asUint64(ob["raw"])
			gotOut[asString(ob["name"])] = r
		}
		if gotOut["HK-01"] != 700 || gotOut["US-01"] != 200 {
			t.Fatalf("unexpected outbounds: %v; body=%s", gotOut, string(pBody))
		}

		_, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		gTotals := asMap(decodeMap(t, gBody)["totals"])
		ai, _ := asUint64(gTotals["AI"])
		coding, _ := asUint64(gTotals["Coding"])
		if ai != 500 || coding != 400 {
			t.Fatalf("unexpected group totals AI=%d Coding=%d; body=%s", ai, coding, string(gBody))
		}
	})

	t.Run("F3_LanSourceIPDeviceUsesExitNode", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f3-lan")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "base", Upload: 10, Download: 10, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f3-lan", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		time.Sleep(150 * time.Millisecond)
		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "base", Upload: 10, Download: 10, Chains: []string{"DIRECT"}},
			{
				ID: "c-lan", Upload: 300, Download: 700, Chains: []string{"SG-01", "Proxy"},
				Metadata: MockMihomoMetadata{SourceIP: "10.203.113.10"},
			},
		})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
			return len(asSlice(decodeMap(t, b)["devices"])) == 1
		})

		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		dev := asMap(asSlice(decodeMap(t, dBody)["devices"])[0])
		if asString(dev["ip"]) != "10.203.113.10" {
			t.Fatalf("device ip = %q, want 10.203.113.10", asString(dev["ip"]))
		}
		pr, _ := asUint64(asMap(dev["bytes"])["proxy_raw"])
		if pr != 1000 {
			t.Fatalf("device proxy_raw = %d, want 1000", pr)
		}

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		foundSG := false
		for _, item := range asSlice(decodeMap(t, pBody)["outbounds"]) {
			if asString(asMap(item)["name"]) == "SG-01" {
				foundSG = true
			}
		}
		if !foundSG {
			t.Fatalf("expected SG-01 outbound; body=%s", string(pBody))
		}
	})
}

func TestTier1_MihomoStream(t *testing.T) {
	t.Run("F4_WSPreferredWithBearerAuth", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f4-ws")

		mihomo := newMockMihomo(t, "ws-bearer-secret")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "c0", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"}}},
			[]MockMihomoConn{
				{ID: "c0", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"}},
				{ID: "c1", Upload: 200, Download: 400, Chains: []string{"HK-01", "Proxy"}},
			},
		)

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f4-ws", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "ws-bearer-secret")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			return mihomo.WSConnectCount() >= 1
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			tot, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return tot == 600
		})

		foundWSAuth := false
		for _, rec := range mihomo.Requests() {
			if rec.IsWS && rec.Path == "/connections" && rec.Auth == "Bearer ws-bearer-secret" {
				foundWSAuth = true
			}
		}
		if !foundWSAuth {
			t.Fatalf("expected WS /connections request with Bearer auth; requests=%+v", mihomo.Requests())
		}
	})

	t.Run("F4_StartupBaselineNotCounted", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f4-base")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{
			ID: "conn-A", Upload: 1000, Download: 4000, Chains: []string{"HK-IEPL-01"},
		}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f4-base", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		time.Sleep(250 * time.Millisecond)

		_, ov0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		pr0, _ := asUint64(asMap(decodeMap(t, ov0)["bytes"])["proxy_raw"])
		if pr0 != 0 {
			t.Fatalf("startup baseline must yield 0 bytes, got %d", pr0)
		}

		mihomo.SetConnections([]MockMihomoConn{{
			ID: "conn-A", Upload: 1300, Download: 4600, Chains: []string{"HK-IEPL-01"},
		}})
		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "conn-A", Upload: 1300, Download: 4600, Chains: []string{"HK-IEPL-01"},
		}})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 900
		})
	})

	t.Run("F4_NewConnectionPostStartupCountedFromZero", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f4-newconn")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "conn-A", Upload: 1000, Download: 4000, Chains: []string{"HK-01", "Proxy"}}},
			[]MockMihomoConn{
				{ID: "conn-A", Upload: 1000, Download: 4000, Chains: []string{"HK-01", "Proxy"}},
				{ID: "conn-B", Upload: 500, Download: 2000, Chains: []string{"HK-01", "Proxy"}},
			},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f4-newconn", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 2500
		})
	})

	t.Run("F4_HTTPFallbackWhenWSRejected", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f4-http")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetRejectWS(http.StatusNotFound)
		mihomo.SetConnections([]MockMihomoConn{{ID: "c1", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"}}})

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f4-http", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			for _, rec := range mihomo.Requests() {
				if !rec.IsWS && rec.Path == "/connections" {
					return true
				}
			}
			return false
		})
		mihomo.SetConnections([]MockMihomoConn{
			{ID: "c1", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"}},
			{ID: "c2", Upload: 400, Download: 600, Chains: []string{"HK-01", "Proxy"}},
		})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 1000
		})
	})

	t.Run("F4_NormalCloseProducesNoResetAlert", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f4-close")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "conn-A", Upload: 100, Download: 200, Chains: []string{"HK-01", "Proxy"}}},
			[]MockMihomoConn{{ID: "conn-A", Upload: 400, Download: 700, Chains: []string{"HK-01", "Proxy"}}},
			[]MockMihomoConn{}, // conn-A closed normally
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f4-close", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			if st != http.StatusOK {
				return false
			}
			m := decodeMap(t, b)
			pr, _ := asUint64(asMap(m["bytes"])["proxy_raw"])
			batches, _ := asUint64(m["ingest_batches"])
			return pr == 800 && batches >= 3
		})
		time.Sleep(250 * time.Millisecond)

		aSt, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		if aSt != http.StatusOK {
			t.Fatalf("GET /v1/alerts status = %d; body=%s", aSt, string(aBody))
		}
		for _, item := range asSlice(decodeMap(t, aBody)["alerts"]) {
			al := asMap(item)
			if asString(al["kind"]) == "reset" && asString(al["message"]) != "collector gap detected: reboot" {
				t.Fatalf("normal connection close must not raise non-reboot reset alert; alerts=%s", string(aBody))
			}
		}
	})
}

func TestTier1_ProcessDomain(t *testing.T) {
	t.Run("F5_MetadataProcessRecorded", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f5-proc")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{
				{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
				{ID: "c1", Upload: 150, Download: 450, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Process: "curl"}},
			},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f5-proc", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
			if st != http.StatusOK {
				return false
			}
			tot, _ := asUint64(asMap(decodeMap(t, b)["totals"])["curl"])
			return tot == 600
		})
		trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		if trSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=process status = %d; body=%s", trSt, string(trBody))
		}
		trMap := decodeMap(t, trBody)
		rx, _ := asUint64(asMap(trMap["rx_totals"])["curl"])
		tx, _ := asUint64(asMap(trMap["tx_totals"])["curl"])
		bd := asMap(asMap(trMap["breakdown"])["curl"])
		bdRx, _ := asUint64(bd["rx"])
		bdTx, _ := asUint64(bd["tx"])
		bdTot, _ := asUint64(bd["total"])
		if rx != 450 || tx != 150 || bdRx != 450 || bdTx != 150 || bdTot != 600 {
			t.Fatalf("curl rx=%d tx=%d breakdown=%v, want 450/150/600; body=%s", rx, tx, bd, string(trBody))
		}

		dimSt, dimBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=process&sort=rx", nil)
		if dimSt != http.StatusOK {
			t.Fatalf("GET /v1/dimensions?type=process&sort=rx status = %d; body=%s", dimSt, string(dimBody))
		}
		items := asSlice(decodeMap(t, dimBody)["items"])
		if len(items) == 0 {
			t.Fatalf("expected process dimension items; body=%s", string(dimBody))
		}
		it := asMap(items[0])
		dRx, _ := asUint64(it["rx"])
		dTx, _ := asUint64(it["tx"])
		dTot, _ := asUint64(it["total"])
		if asString(it["key"]) != "curl" || dRx != 450 || dTx != 150 || dTot != 600 {
			t.Fatalf("dimension item = %v, want curl 450/150/600; body=%s", it, string(dimBody))
		}
	})

	t.Run("F5_ProcessPathStrippedToBasename", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f5-base")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{
				{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
				{ID: "c1", Upload: 100, Download: 200, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{ProcessPath: "/usr/bin/git"}},
				{ID: "c2", Upload: 200, Download: 300, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{ProcessPath: `C:\Apps\Tool\tool.exe`}},
			},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f5-base", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
			if st != http.StatusOK {
				return false
			}
			m := asMap(decodeMap(t, b)["totals"])
			g, _ := asUint64(m["git"])
			tl, _ := asUint64(m["tool.exe"])
			return g == 300 && tl == 500
		})
	})

	t.Run("F5_DomainsOffByDefault", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f5-domoff")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{
				{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
				{ID: "c1", Upload: 300, Download: 700, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "api.example.com", SniffHost: "api.example.com"}},
			},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f5-domoff", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			tot, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return tot == 1000
		})

		dSt, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if dSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=domain status = %d; body=%s", dSt, string(dBody))
		}
		if len(asMap(decodeMap(t, dBody)["totals"])) != 0 {
			t.Fatalf("domain dimension must stay empty by default; body=%s", string(dBody))
		}
	})

	t.Run("F5_DomainsOptInNormalizesETLDPlusOne", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f5-etld")

		cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-f5-etld/config", map[string]any{"collect_domains": true})
		if cSt != http.StatusOK {
			t.Fatalf("PUT node config status = %d; body=%s", cSt, string(cBody))
		}

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f5-etld", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			if mihomo.ConnRequestCount() < 1 {
				return false
			}
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-f5-etld", nil)
			return st == http.StatusOK && asBool(decodeMap(t, b)["online"])
		})
		time.Sleep(100 * time.Millisecond)

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 100, Download: 300, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "cdn.a.example.com"}},
			{ID: "c2", Upload: 200, Download: 400, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{SniffHost: "api.example.com"}},
		})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if st != http.StatusOK {
				return false
			}
			tot, _ := asUint64(asMap(decodeMap(t, b)["totals"])["example.com"])
			return tot == 1000
		})
	})

	t.Run("F5_DomainQueryExposesRxTxTotal", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f5-rxtx")

		cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-f5-rxtx/config", map[string]any{"collect_domains": true})
		if cSt != http.StatusOK {
			t.Fatalf("PUT node config status = %d; body=%s", cSt, string(cBody))
		}

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f5-rxtx", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			if mihomo.ConnRequestCount() < 1 {
				return false
			}
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-f5-rxtx", nil)
			return st == http.StatusOK && asBool(decodeMap(t, b)["online"])
		})
		time.Sleep(100 * time.Millisecond)

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 250, Download: 750, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "docs.example.org"}},
		})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if st != http.StatusOK {
				return false
			}
			tot, _ := asUint64(asMap(decodeMap(t, b)["totals"])["example.org"])
			return tot == 1000
		})

		trSt, trB := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if trSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=domain status = %d; body=%s", trSt, string(trB))
		}
		trMap := decodeMap(t, trB)
		rx, _ := asUint64(asMap(trMap["rx_totals"])["example.org"])
		tx, _ := asUint64(asMap(trMap["tx_totals"])["example.org"])
		tot, _ := asUint64(asMap(trMap["totals"])["example.org"])
		bd := asMap(asMap(trMap["breakdown"])["example.org"])
		bdRx, _ := asUint64(bd["rx"])
		bdTx, _ := asUint64(bd["tx"])
		if rx != 750 || tx != 250 || tot != 1000 || bdRx != 750 || bdTx != 250 {
			t.Fatalf("domain rx=%d tx=%d tot=%d breakdown=%v, want 750/250/1000; body=%s", rx, tx, tot, bd, string(trB))
		}

		dimSt, dimBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=domain", nil)
		if dimSt != http.StatusOK {
			t.Fatalf("GET /v1/dimensions?type=domain status = %d; body=%s", dimSt, string(dimBody))
		}
		var foundDom bool
		for _, raw := range asSlice(decodeMap(t, dimBody)["items"]) {
			it := asMap(raw)
			if asString(it["key"]) == "example.org" {
				dRx, _ := asUint64(it["rx"])
				dTx, _ := asUint64(it["tx"])
				dTot, _ := asUint64(it["total"])
				if dRx != 750 || dTx != 250 || dTot != 1000 {
					t.Fatalf("domain dimension rx=%d tx=%d total=%d, want 750/250/1000", dRx, dTx, dTot)
				}
				foundDom = true
			}
		}
		if !foundDom {
			t.Fatalf("example.org missing from /v1/dimensions?type=domain; body=%s", string(dimBody))
		}
	})
}

func TestTier1_MihomoSync(t *testing.T) {
	t.Run("F6_ProxiesSyncedForRulePreview", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f6-proxies")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetProxies(map[string]MockMihomoProxy{
			"DIRECT":     {Name: "DIRECT", Type: "Direct", Alive: true},
			"HK-IEPL-x2": {Name: "HK-IEPL-x2", Type: "Shadowsocks", Alive: true},
			"US-Plain":   {Name: "US-Plain", Type: "VMess", Alive: true},
		})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f6-proxies", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/preview", map[string]any{})
			if st != http.StatusOK {
				return false
			}
			s := string(b)
			return strings.Contains(s, "HK-IEPL-x2") && strings.Contains(s, "US-Plain")
		})
	})

	t.Run("F6_HTTPProviderSubscriptionSynced", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f6-prov")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetProviders(map[string]MockMihomoProvider{
			"AirportA": {
				Name: "AirportA", Type: "Proxy", VehicleType: "HTTP",
				Proxies:          []MockMihomoProxy{{Name: "HK-IEPL-01", Type: "Shadowsocks", Alive: true}},
				SubscriptionInfo: &MockSubscriptionInfo{Upload: 1024, Download: 4096, Total: 107374182400, Expire: 1893456000},
			},
		})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f6-prov", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
			if st != http.StatusOK {
				return false
			}
			for _, item := range asSlice(decodeMap(t, b)["providers"]) {
				p := asMap(item)
				if asString(p["name"]) == "AirportA" {
					tot, _ := asUint64(p["total_bytes"])
					used, _ := asUint64(p["used_bytes"])
					return tot == 107374182400 && used == 5120
				}
			}
			return false
		})
	})

	t.Run("F6_CompatibleProviderHandledCleanly", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f6-compat")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetProviders(map[string]MockMihomoProvider{
			"default": {Name: "default", Type: "Proxy", VehicleType: "Compatible", Proxies: []MockMihomoProxy{}},
		})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f6-compat", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			for _, r := range mihomo.Requests() {
				if r.Path == "/providers/proxies" {
					return true
				}
			}
			return false
		})
		time.Sleep(300 * time.Millisecond)

		pSt, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if pSt != http.StatusOK {
			t.Fatalf("GET /v1/proxy/providers status = %d; body=%s", pSt, string(pBody))
		}
		_, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		for _, item := range asSlice(decodeMap(t, aBody)["alerts"]) {
			if asString(asMap(item)["kind"]) == "drift" {
				t.Fatalf("Compatible provider without subscriptionInfo must not create drift alert; alerts=%s", string(aBody))
			}
		}
	})

	t.Run("F6_BearerSecretSentOnAllSyncEndpoints", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f6-auth")

		const sec = "e2e-sync-secret-val"
		mihomo := newMockMihomo(t, sec)
		mihomo.SetProxies(map[string]MockMihomoProxy{"HK-01": {Name: "HK-01", Type: "Shadowsocks", Alive: true}})
		mihomo.SetProviders(map[string]MockMihomoProvider{"A": {Name: "A", Type: "Proxy", VehicleType: "HTTP"}})

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f6-auth", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, sec)

		seen := map[string]bool{}
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			for _, rec := range mihomo.Requests() {
				if rec.Auth == "Bearer "+sec {
					seen[rec.Path] = true
				}
			}
			return seen["/connections"] && seen["/proxies"] && seen["/providers/proxies"]
		})
	})

	t.Run("F6_StrictlyReadOnlyHTTPMethods", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f6-ro")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetProxies(map[string]MockMihomoProxy{"HK-01": {Name: "HK-01", Type: "Shadowsocks", Alive: true}})
		mihomo.SetProviders(map[string]MockMihomoProvider{"A": {Name: "A", Type: "Proxy", VehicleType: "HTTP"}})

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f6-ro", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			return len(mihomo.Requests()) >= 3
		})
		if nonGet := mihomo.NonGetRequests(); len(nonGet) > 0 {
			t.Fatalf("agent issued non-GET requests to Mihomo: %+v", nonGet)
		}
	})
}

func TestTier1_IfaceExclusion(t *testing.T) {
	t.Run("F7_VirtualNICsExcludedFromNodeTotal", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f7-virt")

		batch := syntheticBatch("site-e2e", "node-f7-virt", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(
				map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 500},
				map[string]any{"name": "lo", "rx_delta": 9000, "tx_delta": 9000},
				map[string]any{"name": "tun0", "rx_delta": 1000, "tx_delta": 500},
				map[string]any{"name": "docker0", "rx_delta": 2000, "tx_delta": 1000},
				map[string]any{"name": "wg0", "rx_delta": 3000, "tx_delta": 1500},
			),
		)
		st, body := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}
		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		tot, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"])
		if tot != 1500 {
			t.Fatalf("node total = %d, want 1500 (virtual NICs excluded); body=%s", tot, string(ovBody))
		}
	})

	t.Run("F7_PerInterfaceDimensionRetainsAllNICs", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f7-all")

		batch := syntheticBatch("site-e2e", "node-f7-all", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(
				map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 500},
				map[string]any{"name": "lo", "rx_delta": 200, "tx_delta": 100},
				map[string]any{"name": "tun0", "rx_delta": 300, "tx_delta": 100},
				map[string]any{"name": "docker0", "rx_delta": 400, "tx_delta": 100},
				map[string]any{"name": "wg0", "rx_delta": 500, "tx_delta": 200},
			),
		)
		st, _ := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest status = %d", st)
		}

		trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface", nil)
		if trSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=iface status = %d; body=%s", trSt, string(trBody))
		}
		totals := asMap(decodeMap(t, trBody)["totals"])
		want := map[string]uint64{"eth0": 1500, "lo": 300, "tun0": 400, "docker0": 500, "wg0": 700}
		for k, w := range want {
			got, _ := asUint64(totals[k])
			if got != w {
				t.Fatalf("iface %s total = %d, want %d; body=%s", k, got, w, string(trBody))
			}
		}
	})

	t.Run("F7_NlbwmonPrecedenceOverIface", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f7-nlbw")

		batch := syntheticBatch("site-e2e", "node-f7-nlbw", "boot-1", 1, time.Now().UTC(),
			withCapabilities("nlbwmon", "iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 500}),
			withDevices(map[string]any{"obs_ip": "203.0.113.10", "obs_mac": "02:00:00:00:00:10", "rx_delta": 700, "tx_delta": 200, "source": "nlbwmon"}),
		)
		st, body := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}
		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		tot, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"])
		if tot != 900 {
			t.Fatalf("node total = %d, want nlbwmon 900 (not iface 1500)", tot)
		}
	})

	t.Run("F7_CompiledAgentIfaceRunsWithoutUnsupportedErr", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f7-agent")

		agent := startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-f7-agent", Interval: "200ms",
			Collectors: AgentCollectorsConfig{Iface: true},
		}, tok, "")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-f7-agent", nil)
			if st != http.StatusOK || !asBool(decodeMap(t, b)["online"]) {
				return false
			}
			ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			if ovSt != http.StatusOK {
				return false
			}
			batches, _ := asUint64(decodeMap(t, ovBody)["ingest_batches"])
			return batches >= 2
		})
		if strings.Contains(strings.ToLower(agent.Logs()), "unsupported") {
			t.Fatalf("agent logs contain unsupported error: %s", agent.Logs())
		}
		aSt, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		if aSt != http.StatusOK {
			t.Fatalf("GET /v1/alerts status = %d; body=%s", aSt, string(aBody))
		}
		for _, item := range asSlice(decodeMap(t, aBody)["alerts"]) {
			al := asMap(item)
			if asString(al["kind"]) == "reset" && asString(al["message"]) != "collector gap detected: reboot" {
				t.Fatalf("iface collector emitted non-reboot reset alert on host OS; alerts=%s", string(aBody))
			}
		}
	})

	t.Run("F7_ConfiguredBrLanIncludedWhenOptedIn", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f7-brlan")

		// PUT /v1/nodes/{id}/config accepts only collect_domains; missing collect_domains must return 400.
		cSt, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-f7-brlan/config", map[string]any{
			"include_ifaces": []string{"br-lan"},
		})
		if cSt != http.StatusBadRequest {
			t.Fatalf("PUT /v1/nodes/node-f7-brlan/config without collect_domains status = %d, want 400", cSt)
		}

		// Opt-in virtual bridge inclusion is passed via batch.metadata.iface_include (from agent.json iface.include).
		batch := syntheticBatch("site-e2e", "node-f7-brlan", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withMetadata(map[string]any{"iface_include": []string{"br-lan"}}),
			withInterfaces(
				map[string]any{"name": "br-lan", "rx_delta": 1024, "tx_delta": 512},
				map[string]any{"name": "br-01", "rx_delta": 999, "tx_delta": 1},
			),
		)
		st, body := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}
		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		tot, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"])
		if tot != 1536 {
			t.Fatalf("node total = %d, want 1536 (br-lan opted in, br-01 excluded); body=%s", tot, string(ovBody))
		}
		trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface", nil)
		if trSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=iface status = %d; body=%s", trSt, string(trBody))
		}
		totals := asMap(decodeMap(t, trBody)["totals"])
		brLan, _ := asUint64(totals["br-lan"])
		br01, _ := asUint64(totals["br-01"])
		if brLan != 1536 || br01 != 1000 {
			t.Fatalf("iface totals br-lan=%d br-01=%d, want 1536/1000; body=%s", brLan, br01, string(trBody))
		}
	})
}

func TestTier1_DirectCollectors(t *testing.T) {
	t.Run("F8_CRUDLifecycle", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		mihomo := newMockMihomo(t, "s1")
		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "Router-A", "site_id": "site-e2e", "node_id": "dc-1",
			"url": mihomo.URL(), "secret": "s1", "enabled": false, "collect_domains": false,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("POST /v1/direct-collectors status = %d; body=%s", cSt, string(cBody))
		}
		created := decodeMap(t, cBody)
		id := fmt.Sprintf("%v", created["id"])

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/direct-collectors", nil)
		if gSt != http.StatusOK || !strings.Contains(string(gBody), "Router-A") {
			t.Fatalf("GET /v1/direct-collectors = %d %s", gSt, string(gBody))
		}

		uSt, uBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/direct-collectors/"+id, map[string]any{
			"name": "Router-A-Updated", "site_id": "site-e2e", "node_id": "dc-1",
			"url": mihomo.URL(), "enabled": false,
		})
		if uSt != http.StatusOK {
			t.Fatalf("PUT /v1/direct-collectors/%s = %d %s", id, uSt, string(uBody))
		}

		dSt, _ := doJSONRequest(t, client, http.MethodDelete, srv.BaseURL+"/v1/direct-collectors/"+id, nil)
		if dSt != http.StatusOK {
			t.Fatalf("DELETE /v1/direct-collectors/%s = %d", id, dSt)
		}
	})

	t.Run("F8_TestConnectionSuccessAndFailure", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		mihomo := newMockMihomo(t, "good-sec")
		okSt, okBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "Good", "site_id": "site-e2e", "node_id": "dc-good",
			"url": mihomo.URL(), "secret": "good-sec", "enabled": false,
		})
		if okSt != http.StatusOK && okSt != http.StatusCreated {
			t.Fatalf("create Good direct collector = %d %s", okSt, string(okBody))
		}
		goodID := fmt.Sprintf("%v", decodeMap(t, okBody)["id"])

		tSt, tBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+goodID+"/test", map[string]any{})
		if tSt != http.StatusOK {
			t.Fatalf("healthy test status = %d; body=%s", tSt, string(tBody))
		}

		badSt, badBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "BadAuth", "site_id": "site-e2e", "node_id": "dc-bad",
			"url": mihomo.URL(), "secret": "wrong-sec", "enabled": false,
		})
		if badSt != http.StatusOK && badSt != http.StatusCreated {
			t.Fatalf("create BadAuth direct collector = %d %s", badSt, string(badBody))
		}
		badID := fmt.Sprintf("%v", decodeMap(t, badBody)["id"])
		bSt, bBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+badID+"/test", map[string]any{})
		if bSt == http.StatusOK && asBool(decodeMap(t, bBody)["ok"]) {
			t.Fatalf("wrong secret test should report failure; body=%s", string(bBody))
		}
	})

	t.Run("F8_IngestsTrafficAndProvidersAgentlessly", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		mihomo := newMockMihomo(t, "dc-sec")
		mihomo.SetProviders(map[string]MockMihomoProvider{
			"DirectAirport": {
				Name: "DirectAirport", Type: "Proxy", VehicleType: "HTTP",
				Proxies:          []MockMihomoProxy{{Name: "HK-DC-01", Type: "Shadowsocks", Alive: true}},
				SubscriptionInfo: &MockSubscriptionInfo{Upload: 500, Download: 1500, Total: 100000, Expire: 1893456000},
			},
		})
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 10, Download: 10, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{
				{ID: "b", Upload: 10, Download: 10, Chains: []string{"DIRECT"}},
				{ID: "c1", Upload: 400, Download: 600, Chains: []string{"HK-DC-01", "AI"}, Metadata: MockMihomoMetadata{Process: "node"}},
			},
		)

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "Agentless-1", "site_id": "site-e2e", "node_id": "dc-agentless",
			"url": mihomo.URL(), "secret": "dc-sec", "enabled": true,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("create direct collector failed: %d %s", cSt, string(cBody))
		}

		waitUntil(t, 6*time.Second, 100*time.Millisecond, func() bool {
			_, ov := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, ov)["bytes"])["proxy_raw"])
			return pr == 1000
		})

		_, trB := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		nodeTot, _ := asUint64(asMap(decodeMap(t, trB)["totals"])["node"])
		if nodeTot != 1000 {
			t.Fatalf("direct collector process node total = %d, want 1000", nodeTot)
		}

		_, pvB := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if !strings.Contains(string(pvB), "DirectAirport") {
			t.Fatalf("expected DirectAirport in /v1/proxy/providers; body=%s", string(pvB))
		}
	})

	t.Run("F8_ToggleDisableAndEnable", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		mihomo := newMockMihomo(t, "dc-sec")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 10, Download: 10, Chains: []string{"DIRECT"}}})

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "ToggleDC", "site_id": "site-e2e", "node_id": "dc-toggle",
			"url": mihomo.URL(), "secret": "dc-sec", "enabled": true,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("create ToggleDC = %d %s", cSt, string(cBody))
		}
		id := fmt.Sprintf("%v", decodeMap(t, cBody)["id"])

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })

		// Disable
		tSt, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+id+"/toggle", map[string]any{})
		if tSt != http.StatusOK {
			t.Fatalf("toggle disable status = %d", tSt)
		}
		time.Sleep(250 * time.Millisecond)
		before := mihomo.ConnRequestCount()
		time.Sleep(500 * time.Millisecond)
		if mihomo.ConnRequestCount() > before {
			t.Fatalf("disabled direct collector still polling (%d -> %d)", before, mihomo.ConnRequestCount())
		}

		// Re-enable
		tSt2, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+id+"/toggle", map[string]any{})
		if tSt2 != http.StatusOK {
			t.Fatalf("toggle enable status = %d", tSt2)
		}
		mihomo.SetConnections([]MockMihomoConn{
			{ID: "b", Upload: 10, Download: 10, Chains: []string{"DIRECT"}},
			{ID: "c2", Upload: 250, Download: 250, Chains: []string{"HK-01", "Proxy"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, ov := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, ov)["bytes"])["proxy_raw"])
			return pr >= 500
		})
	})

	t.Run("F8_CoexistsWithRemoteAgent", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "remote-agent-1")

		mihomo := newMockMihomo(t, "dc-sec")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 200, Download: 300, Chains: []string{"HK-01", "Proxy"}}},
		)
		dcSt, dcBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "LocalDC", "site_id": "site-e2e", "node_id": "direct-node-1",
			"url": mihomo.URL(), "secret": "dc-sec", "enabled": true,
		})
		if dcSt != http.StatusOK && dcSt != http.StatusCreated {
			t.Fatalf("create LocalDC = %d %s", dcSt, string(dcBody))
		}

		batch := syntheticBatch("site-e2e", "remote-agent-1", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 500}),
		)
		st, _ := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("remote ingest failed: %d", st)
		}

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes", nil)
			nodes := asSlice(decodeMap(t, b)["nodes"])
			seen := map[string]bool{}
			for _, n := range nodes {
				seen[asString(asMap(n)["id"])] = true
			}
			return seen["remote-agent-1"] && seen["direct-node-1"]
		})
	})
}
