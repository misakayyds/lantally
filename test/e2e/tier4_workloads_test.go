//go:build e2e

package e2e

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTier4_Workloads(t *testing.T) {
	t.Run("S1_HouseholdMultiNodeGatewayAndWorkstation", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokGW := enrollNode(t, client, srv.BaseURL, "site-home", "gw-openwrt")
		tokWS := enrollNode(t, client, srv.BaseURL, "site-home", "ws-desktop")

		mihomo := newMockMihomo(t, "s1-secret")
		mihomo.SetProxies(map[string]MockMihomoProxy{
			"DIRECT":     {Name: "DIRECT", Type: "Direct", Alive: true},
			"HK-IEPL-01": {Name: "HK-IEPL-01", Type: "Shadowsocks", Alive: true},
			"US-Plain":   {Name: "US-Plain", Type: "VMess", Alive: true},
		})
		mihomo.SetConnections([]MockMihomoConn{{ID: "base", Upload: 10, Download: 10, Chains: []string{"DIRECT"}}})

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-home", NodeID: "gw-openwrt", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tokGW, "s1-secret")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			batches, _ := asUint64(decodeMap(t, b)["ingest_batches"])
			return mihomo.WSConnectCount() >= 1 && batches >= 1
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "base", Upload: 10, Download: 10, Chains: []string{"DIRECT"}},
			{
				ID: "c1", Upload: 300, Download: 700, Chains: []string{"HK-IEPL-01", "Auto-Best", "AI"},
				Metadata: MockMihomoMetadata{SourceIP: "10.203.113.10", Process: "Cursor"},
			},
			{
				ID: "c2", Upload: 100, Download: 400, Chains: []string{"US-Plain", "Streaming"},
				Metadata: MockMihomoMetadata{SourceIP: "10.203.113.10", Process: "chrome"},
			},
			{
				ID: "c3", Upload: 200, Download: 300, Chains: []string{"DIRECT"},
			},
		})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			bm := asMap(decodeMap(t, b)["bytes"])
			pr, _ := asUint64(bm["proxy_raw"])
			dir, _ := asUint64(bm["direct"])
			return pr == 1500 && dir == 500
		})

		// Workstation node reports physical eth0 + virtual NICs (lo, tun0, wg0, docker0).
		wsBatch := syntheticBatch("site-home", "ws-desktop", "boot-ws-1", 1, time.Now().UTC(),
			withCapabilities("iface", "mihomo"),
			withInterfaces(
				map[string]any{"name": "eth0", "rx_delta": 2000, "tx_delta": 1000},
				map[string]any{"name": "lo", "rx_delta": 9000, "tx_delta": 9000},
				map[string]any{"name": "tun0", "rx_delta": 1500, "tx_delta": 500},
				map[string]any{"name": "wg0", "rx_delta": 800, "tx_delta": 200},
				map[string]any{"name": "docker0", "rx_delta": 400, "tx_delta": 100},
			),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tokWS, wsBatch); st != http.StatusOK {
			t.Fatalf("ws-desktop ingest = %d %s", st, string(body))
		}

		// Add IEPL 2.0x multiplier rule and check retroactive calculation.
		rSt, rBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "IEPL2x", "match_type": "keyword", "pattern": "IEPL", "factor": 2.0, "priority": 100, "enabled": true,
		})
		if rSt != http.StatusOK && rSt != http.StatusCreated {
			t.Fatalf("POST /v1/proxy/rules = %d %s", rSt, string(rBody))
		}
		_ = extractRuleID(t, rBody)

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		obs := map[string]map[string]any{}
		for _, item := range asSlice(decodeMap(t, pBody)["outbounds"]) {
			m := asMap(item)
			name := asString(m["name"])
			if name == "AI" || name == "Streaming" || name == "Auto-Best" || name == "DIRECT" {
				t.Fatalf("non-exit name %q leaked into outbounds: %s", name, string(pBody))
			}
			obs[name] = m
		}
		if raw, _ := asUint64(obs["HK-IEPL-01"]["raw"]); raw != 1000 {
			t.Fatalf("HK-IEPL-01 raw = %d, want 1000", raw)
		}
		if adj, _ := asUint64(obs["HK-IEPL-01"]["adjusted"]); adj != 2000 || asString(obs["HK-IEPL-01"]["match_source"]) != "rule" {
			t.Fatalf("HK-IEPL-01 adjusted/source unexpected: %v", obs["HK-IEPL-01"])
		}
		if adj, _ := asUint64(obs["US-Plain"]["adjusted"]); adj != 500 || asString(obs["US-Plain"]["match_source"]) != "default" {
			t.Fatalf("US-Plain adjusted/source unexpected: %v", obs["US-Plain"])
		}

		_, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		gT := asMap(decodeMap(t, gBody)["totals"])
		if ai, _ := asUint64(gT["AI"]); ai != 1000 {
			t.Fatalf("group AI = %d, want 1000", ai)
		}
		if st, _ := asUint64(gT["Streaming"]); st != 500 {
			t.Fatalf("group Streaming = %d, want 500", st)
		}

		_, prBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		prMap := decodeMap(t, prBody)
		if rx, _ := asUint64(asMap(prMap["rx_totals"])["Cursor"]); rx != 700 {
			t.Fatalf("process Cursor rx = %d, want 700", rx)
		}
		if tx, _ := asUint64(asMap(prMap["tx_totals"])["Cursor"]); tx != 300 {
			t.Fatalf("process Cursor tx = %d, want 300", tx)
		}
		if rx, _ := asUint64(asMap(prMap["rx_totals"])["chrome"]); rx != 400 {
			t.Fatalf("process chrome rx = %d, want 400", rx)
		}

		_, nBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=node", nil)
		nT := asMap(decodeMap(t, nBody)["totals"])
		if wsTot, _ := asUint64(nT["ws-desktop"]); wsTot != 3000 {
			t.Fatalf("ws-desktop total = %d, want 3000 (virtual NICs excluded); body=%s", wsTot, string(nBody))
		}
		if gwTot, _ := asUint64(nT["gw-openwrt"]); gwTot != 2000 {
			t.Fatalf("gw-openwrt total = %d, want 2000; body=%s", gwTot, string(nBody))
		}

		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		ovBytes := asMap(decodeMap(t, ovBody)["bytes"])
		if tot, _ := asUint64(ovBytes["total"]); tot != 5000 {
			t.Fatalf("overview total = %d, want 5000", tot)
		}
		if pa, _ := asUint64(ovBytes["proxy_adjusted"]); pa != 2500 {
			t.Fatalf("overview proxy_adjusted = %d, want 2500", pa)
		}

		_, ifDimBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=iface", nil)
		ifGot := map[string]uint64{}
		for _, raw := range asSlice(decodeMap(t, ifDimBody)["items"]) {
			it := asMap(raw)
			v, _ := asUint64(it["total"])
			ifGot[asString(it["key"])] = v
		}
		for k, want := range map[string]uint64{"eth0": 3000, "lo": 18000, "tun0": 2000, "wg0": 1000, "docker0": 500} {
			if ifGot[k] != want {
				t.Fatalf("iface %s = %d, want %d; all=%v", k, ifGot[k], want, ifGot)
			}
		}

		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs := asSlice(decodeMap(t, dBody)["devices"])
		if len(devs) != 1 {
			t.Fatalf("expected 1 device for 10.203.113.10; body=%s", string(dBody))
		}
		dev := asMap(devs[0])
		devID := asString(dev["id"])
		prBd := asMap(asMap(dev["breakdown"])["proxy_raw"])
		if rx, _ := asUint64(prBd["rx"]); rx != 1100 {
			t.Fatalf("device proxy_raw.rx = %d, want 1100; dev=%v", rx, dev)
		}
		if tx, _ := asUint64(prBd["tx"]); tx != 400 {
			t.Fatalf("device proxy_raw.tx = %d, want 400; dev=%v", tx, dev)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/devices/"+devID, map[string]string{"name": "LivingRoom-PC"}); st != http.StatusOK {
			t.Fatalf("rename device = %d", st)
		}
		_, dBody2 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		if !strings.Contains(string(dBody2), "LivingRoom-PC") {
			t.Fatalf("renamed device missing: %s", string(dBody2))
		}
	})

	t.Run("S2_AgentlessDirectCollectorAndProviderReconciliation", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		const dcSecret = "dc-secret-e2e"
		mihomo := newMockMihomo(t, dcSecret)
		mihomo.SetProxies(map[string]MockMihomoProxy{
			"HK-IEPL-01": {Name: "HK-IEPL-01", Type: "Shadowsocks", Alive: true},
			"JP-x2":      {Name: "JP-x2", Type: "VMess", Alive: true},
			"DIRECT":     {Name: "DIRECT", Type: "Direct", Alive: true},
		})
		mihomo.SetProviders(map[string]MockMihomoProvider{
			"AirportMain": {
				Name: "AirportMain", Type: "Proxy", VehicleType: "HTTP",
				Proxies: []MockMihomoProxy{
					{Name: "HK-IEPL-01", Type: "Shadowsocks", Alive: true},
					{Name: "JP-x2", Type: "VMess", Alive: true},
				},
				SubscriptionInfo: &MockSubscriptionInfo{Upload: 400, Download: 1600, Total: 100000, Expire: 1893456000},
			},
		})
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{
				{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
				{ID: "c1", Upload: 300, Download: 700, Chains: []string{"HK-IEPL-01", "AI"}, Metadata: MockMihomoMetadata{Process: "codex"}},
			},
		)

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "RouterDC", "site_id": "site-e2e", "node_id": "dc-s2",
			"url": mihomo.URL(), "secret": dcSecret, "enabled": false,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("POST /v1/direct-collectors = %d %s", cSt, string(cBody))
		}
		dcID := extractEntityID(t, cBody, "collector")

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/direct-collectors", nil)
		if gSt != http.StatusOK || strings.Contains(string(gBody), dcSecret) {
			t.Fatalf("GET /v1/direct-collectors status=%d leaked=%v", gSt, strings.Contains(string(gBody), dcSecret))
		}
		for _, it := range asSlice(decodeMap(t, gBody)["collectors"]) {
			if !asBool(asMap(it)["has_secret"]) {
				t.Fatalf("expected has_secret=true: %s", string(gBody))
			}
		}

		tSt, tBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+dcID+"/test", map[string]any{})
		if tSt != http.StatusOK || !asBool(decodeMap(t, tBody)["ok"]) {
			t.Fatalf("direct collector test = %d %s", tSt, string(tBody))
		}

		if tgSt, tgBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+dcID+"/toggle", map[string]any{}); tgSt != http.StatusOK {
			t.Fatalf("toggle enable = %d %s", tgSt, string(tgBody))
		}

		waitUntil(t, 6*time.Second, 100*time.Millisecond, func() bool {
			_, ov := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, ov)["bytes"])["proxy_raw"])
			return pr == 1000
		})
		if nonGet := mihomo.NonGetRequests(); len(nonGet) > 0 {
			t.Fatalf("direct collector must be read-only; non-GET: %+v", nonGet)
		}

		_, grpBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		if ai, _ := asUint64(asMap(decodeMap(t, grpBody)["totals"])["AI"]); ai != 1000 {
			t.Fatalf("group AI = %d, want 1000", ai)
		}
		_, procBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		pm := decodeMap(t, procBody)
		if rx, _ := asUint64(asMap(pm["rx_totals"])["codex"]); rx != 700 {
			t.Fatalf("process codex rx = %d, want 700", rx)
		}
		if tx, _ := asUint64(asMap(pm["tx_totals"])["codex"]); tx != 300 {
			t.Fatalf("process codex tx = %d, want 300", tx)
		}

		prevSt, prevBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/preview", map[string]any{})
		if prevSt != http.StatusOK {
			t.Fatalf("preview = %d %s", prevSt, string(prevBody))
		}
		matches := extractPreviewMatches(t, prevBody)
		if len(matches) < 2 || !strings.Contains(string(prevBody), "HK-IEPL-01") || !strings.Contains(string(prevBody), "JP-x2") {
			t.Fatalf("preview missing synced proxies: %s", string(prevBody))
		}

		pvSt, pvBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if pvSt != http.StatusOK {
			t.Fatalf("GET /v1/proxy/providers = %d %s", pvSt, string(pvBody))
		}
		provs := asSlice(decodeMap(t, pvBody)["providers"])
		if len(provs) != 1 {
			t.Fatalf("expected 1 provider; body=%s", string(pvBody))
		}
		p := asMap(provs[0])
		used, _ := asUint64(p["used_bytes"])
		tot, _ := asUint64(p["total_bytes"])
		loc, _ := asUint64(p["local_adjusted"])
		if asString(p["name"]) != "AirportMain" || used != 2000 || tot != 100000 || loc != 1000 {
			t.Fatalf("unexpected AirportMain reconciliation: %s", string(pvBody))
		}

		// Disable and confirm polling stops.
		if tg2St, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+dcID+"/toggle", map[string]any{}); tg2St != http.StatusOK {
			t.Fatalf("toggle disable = %d", tg2St)
		}
		time.Sleep(250 * time.Millisecond)
		before := mihomo.ConnRequestCount()
		time.Sleep(450 * time.Millisecond)
		if mihomo.ConnRequestCount() > before {
			t.Fatalf("disabled direct collector continued polling (%d -> %d)", before, mihomo.ConnRequestCount())
		}
	})

	t.Run("S3_RetroactiveMultiplierOverhaulImportExport", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-s3")

		now := time.Now().UTC()
		b1 := syntheticBatch("site-e2e", "node-s3", "boot-1", 1, now.Add(-48*time.Hour),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "HK-IEPL-01", "proxy_rx": 700, "proxy_tx": 300}),
		)
		b2 := syntheticBatch("site-e2e", "node-s3", "boot-1", 2, now.Add(-30*time.Minute),
			withCapabilities("mihomo"),
			withProxyOutbounds(
				map[string]any{"name": "JP-x2.5", "proxy_rx": 800, "proxy_tx": 200},
				map[string]any{"name": "US-Plain", "proxy_rx": 400, "proxy_tx": 100},
			),
			withProviders(map[string]any{
				"name": "AirportS3", "vehicle_type": "HTTP",
				"upload": 500, "download": 2000, "total": 50000, "expire": 1893456000,
				"proxies": []string{"HK-IEPL-01", "JP-x2.5", "US-Plain"},
			}),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b1); st != http.StatusOK {
			t.Fatalf("ingest b1 = %d %s", st, string(body))
		}
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b2); st != http.StatusOK {
			t.Fatalf("ingest b2 = %d %s", st, string(body))
		}

		// Initial 1.0x default check across 24h vs 7d.
		_, ov0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		bm0 := asMap(decodeMap(t, ov0)["bytes"])
		if pr, _ := asUint64(bm0["proxy_raw"]); pr != 2500 {
			t.Fatalf("initial proxy_raw = %d, want 2500", pr)
		}
		if pa, _ := asUint64(bm0["proxy_adjusted"]); pa != 2500 {
			t.Fatalf("initial proxy_adjusted = %d, want 2500", pa)
		}
		_, tr24Before := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=outbound&class=proxy_raw&range=24h", nil)
		t24 := asMap(decodeMap(t, tr24Before)["totals"])
		if jp, _ := asUint64(t24["JP-x2.5"]); jp != 1000 {
			t.Fatalf("24h JP-x2.5 = %d, want 1000", jp)
		}
		if _, hasHK := t24["HK-IEPL-01"]; hasHK {
			t.Fatalf("HK-IEPL-01 (-48h) should not appear in 24h range: %s", string(tr24Before))
		}

		// Import IEPL->3.0 + auto_extract.
		imSt, imBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/import", map[string]any{
			"mode": "replace",
			"rules": []map[string]any{
				{"name": "IEPL3x", "match_type": "keyword", "pattern": "IEPL", "factor": 3.0, "priority": 100, "enabled": true},
				{"name": "Auto", "match_type": "auto_extract", "pattern": "", "factor": 1.0, "priority": 50, "enabled": true},
			},
		})
		if imSt != http.StatusOK {
			t.Fatalf("import rules = %d %s", imSt, string(imBody))
		}

		// Expected: HK-IEPL-01 (1000*3.0=3000) + JP-x2.5 (1000*2.5=2500) + US-Plain (500*1.0=500) = 6000.
		_, ov1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		bm1 := asMap(decodeMap(t, ov1)["bytes"])
		if pr, _ := asUint64(bm1["proxy_raw"]); pr != 2500 {
			t.Fatalf("post-import proxy_raw = %d, want 2500 unchanged", pr)
		}
		if pa, _ := asUint64(bm1["proxy_adjusted"]); pa != 6000 {
			t.Fatalf("post-import proxy_adjusted = %d, want 6000", pa)
		}

		_, tr7 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=outbound&class=proxy_adjusted&range=7d", nil)
		m7 := decodeMap(t, tr7)
		if hkRx, _ := asUint64(asMap(m7["rx_totals"])["HK-IEPL-01"]); hkRx != 2100 {
			t.Fatalf("7d HK-IEPL-01 adjusted rx = %d, want 2100; body=%s", hkRx, string(tr7))
		}
		if jpTx, _ := asUint64(asMap(m7["tx_totals"])["JP-x2.5"]); jpTx != 500 {
			t.Fatalf("7d JP-x2.5 adjusted tx = %d, want 500; body=%s", jpTx, string(tr7))
		}
		if usTot, _ := asUint64(asMap(m7["totals"])["US-Plain"]); usTot != 500 {
			t.Fatalf("7d US-Plain adjusted = %d, want 500; body=%s", usTot, string(tr7))
		}

		// Provider reconciliation local_adjusted must also reflect 6000 (M3).
		pvSt, pvBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if pvSt != http.StatusOK {
			t.Fatalf("GET /v1/proxy/providers = %d %s", pvSt, string(pvBody))
		}
		provs := asSlice(decodeMap(t, pvBody)["providers"])
		if len(provs) != 1 {
			t.Fatalf("expected 1 provider; body=%s", string(pvBody))
		}
		if loc, _ := asUint64(asMap(provs[0])["local_adjusted"]); loc != 6000 {
			t.Fatalf("provider local_adjusted = %d, want 6000; body=%s", loc, string(pvBody))
		}

		// Export -> delete all -> revert to 2500 -> re-import -> 6000.
		exSt, exBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/rules/export", nil)
		if exSt != http.StatusOK {
			t.Fatalf("export = %d %s", exSt, string(exBody))
		}
		_, lsBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/rules", nil)
		for _, r := range asSlice(decodeMap(t, lsBody)["rules"]) {
			rid := fmt.Sprintf("%v", asMap(r)["id"])
			if dSt, _ := doJSONRequest(t, client, http.MethodDelete, srv.BaseURL+"/v1/proxy/rules/"+rid, nil); dSt != http.StatusOK {
				t.Fatalf("delete rule %s = %d", rid, dSt)
			}
		}
		_, ovRev := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if pa, _ := asUint64(asMap(decodeMap(t, ovRev)["bytes"])["proxy_adjusted"]); pa != 2500 {
			t.Fatalf("reverted proxy_adjusted = %d, want 2500", pa)
		}
		if reSt, _, reBody := doRawRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/import", exBody, map[string]string{"Content-Type": "application/json"}); reSt != http.StatusOK {
			t.Fatalf("re-import = %d %s", reSt, string(reBody))
		}
		_, ovRest := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if pa, _ := asUint64(asMap(decodeMap(t, ovRest)["bytes"])["proxy_adjusted"]); pa != 6000 {
			t.Fatalf("restored proxy_adjusted = %d, want 6000", pa)
		}
	})

	t.Run("S4_OptInDomainAuditAndAIWatchlistQuota", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-s4")

		mihomo := newMockMihomo(t, "s4")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		agent := startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-s4", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true}, Mihomo: &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s4")
		cfgHash := agent.ConfigSHA256(t)

		getOverview := func() (uint64, uint64) {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			if st != http.StatusOK {
				return 0, 0
			}
			m := decodeMap(t, b)
			batches, _ := asUint64(m["ingest_batches"])
			pr, _ := asUint64(asMap(m["bytes"])["proxy_raw"])
			return batches, pr
		}
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			batches, _ := getOverview()
			return mihomo.WSConnectCount() >= 1 && batches >= 1
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 100, Download: 400, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "secret.private.example"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, pr := getOverview()
			return pr == 500
		})
		_, d0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if len(asMap(decodeMap(t, d0)["totals"])) != 0 {
			t.Fatalf("domains leaked with collect_domains=false: %s", string(d0))
		}

		wSt, wBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name": "AI Coding",
			"matchers": map[string]any{
				"domain":  map[string]any{"type": "exact", "values": []string{"openai.com"}},
				"process": map[string]any{"type": "exact", "values": []string{"Cursor"}},
			},
			"override_factor": 1.5,
			"quota_value":     1, "quota_unit": "MB", "reset_type": "monthly", "reset_day": 1, "pinned": true,
		})
		if wSt != http.StatusOK && wSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch = %d %s", wSt, string(wBody))
		}
		b0, _ := getOverview()
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			batches, _ := getOverview()
			return batches >= b0+2
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 100, Download: 400, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "secret.private.example"}},
			{ID: "c2", Upload: 200, Download: 800, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "api.openai.com", Process: "Cursor"}},
			{ID: "c3", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "other.unwatched.example"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			tot, _ := asUint64(asMap(decodeMap(t, b)["totals"])["openai.com"])
			return tot == 1000
		})
		_, d1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if strings.Contains(string(d1), "unwatched.example") {
			t.Fatalf("unwatched.example leaked via watch_domains: %s", string(d1))
		}

		// Enable collect_domains=true -> eTLD+1 merges subdomains to example.com.
		b1, _ := getOverview()
		if cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-s4/config", map[string]any{
			"collect_domains": true,
		}); cSt != http.StatusOK {
			t.Fatalf("PUT collect_domains=true = %d %s", cSt, string(cBody))
		}
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			batches, _ := getOverview()
			return batches >= b1+2
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 100, Download: 400, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "secret.private.example"}},
			{ID: "c2", Upload: 200, Download: 800, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "api.openai.com", Process: "Cursor"}},
			{ID: "c3", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "other.unwatched.example"}},
			{ID: "c4", Upload: 300, Download: 700, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "cdn.docs.example.com"}},
			{ID: "c5", Upload: 200, Download: 800, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{SniffHost: "api.example.com"}},
			{ID: "c6", Upload: 200000, Download: 900000, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "api.openai.com", Process: "Cursor"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			tot, _ := asUint64(asMap(decodeMap(t, b)["totals"])["example.com"])
			return tot == 2000
		})
		_, d2 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		dm2 := decodeMap(t, d2)
		if rx, _ := asUint64(asMap(dm2["rx_totals"])["example.com"]); rx != 1500 {
			t.Fatalf("example.com rx = %d, want 1500; body=%s", rx, string(d2))
		}
		if tx, _ := asUint64(asMap(dm2["tx_totals"])["example.com"]); tx != 500 {
			t.Fatalf("example.com tx = %d, want 500; body=%s", tx, string(d2))
		}

		_, wGet := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/watch", nil)
		items := asSlice(decodeMap(t, wGet)["items"])
		if len(items) != 1 {
			t.Fatalf("expected 1 watch item; body=%s", string(wGet))
		}
		it := asMap(items[0])
		ratio, _ := asFloat64(it["used_ratio"])
		if !asBool(it["over_quota"]) || ratio < 1.0 || len(asSlice(it["series"])) == 0 {
			t.Fatalf("expected over_quota=true ratio>=1.0 and non-empty series; body=%s", string(wGet))
		}
		_, ovFinal := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if !strings.Contains(fmt.Sprintf("%v", decodeMap(t, ovFinal)["pinned_watch"]), "AI Coding") {
			t.Fatalf("AI Coding missing from overview.pinned_watch; body=%s", string(ovFinal))
		}
		if agent.ConfigSHA256(t) != cfgHash {
			t.Fatalf("agent config mutated on disk during hot reload")
		}
	})

	t.Run("S5_AirGappedFreshInstallSetupClaimAndSPA", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		sSt, sBody := doJSONRequest(t, nil, http.MethodGet, srv.BaseURL+"/v1/setup", nil)
		if sSt != http.StatusOK || !asBool(decodeMap(t, sBody)["needed"]) {
			t.Fatalf("GET /v1/setup = %d %s, want needed=true", sSt, string(sBody))
		}
		for _, p := range []string{"/v1/overview", "/v1/nodes", "/v1/backup"} {
			if st, _ := doJSONRequest(t, nil, http.MethodGet, srv.BaseURL+p, nil); st != http.StatusUnauthorized {
				t.Fatalf("unauthenticated GET %s = %d, want 401", p, st)
			}
		}

		hSt, hHdr, htmlBytes := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/", nil, nil)
		if hSt != http.StatusOK || !strings.Contains(hHdr.Get("Content-Type"), "text/html") {
			t.Fatalf("GET / = %d ct=%q", hSt, hHdr.Get("Content-Type"))
		}
		html := string(htmlBytes)
		forbidden := []string{"fonts.googleapis.com", "fonts.gstatic.com", "unpkg.com", "cdn.jsdelivr.net"}
		for _, pat := range forbidden {
			if strings.Contains(html, pat) {
				t.Fatalf("index.html contains external CDN %q", pat)
			}
		}
		if regexp.MustCompile(`(?is)<(?:script|link)\b[^>]*\b(?:src|href)\s*=\s*["']https?://`).MatchString(html) {
			t.Fatalf("index.html contains external http(s) script/link tag")
		}

		assetRe := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
		matches := assetRe.FindAllStringSubmatch(html, -1)
		if len(matches) == 0 {
			t.Fatalf("index.html missing Vite /assets/* references:\n%s", html)
		}
		var jsBundle strings.Builder
		for _, m := range matches {
			st, hdr, body := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+m[1], nil, nil)
			if st != http.StatusOK || len(body) == 0 {
				t.Fatalf("asset %s failed: %d len=%d", m[1], st, len(body))
			}
			for _, pat := range forbidden {
				if strings.Contains(string(body), pat) {
					t.Fatalf("asset %s contains external CDN %q", m[1], pat)
				}
			}
			if strings.HasSuffix(m[1], ".js") {
				if !strings.Contains(hdr.Get("Content-Type"), "javascript") {
					t.Fatalf("asset %s MIME = %q", m[1], hdr.Get("Content-Type"))
				}
				jsBundle.Write(body)
			} else if strings.HasSuffix(m[1], ".css") && !strings.Contains(hdr.Get("Content-Type"), "text/css") {
				t.Fatalf("asset %s MIME = %q", m[1], hdr.Get("Content-Type"))
			}
		}
		jsText := jsBundle.String()
		for _, pair := range [][]string{{"Overview", "概览"}, {"Proxy", "代理"}, {"Watchlist", "关注"}, {"Apps", "应用"}, {"Devices", "设备"}, {"Nodes", "节点"}, {"Alerts", "告警"}} {
			if !strings.Contains(jsText, pair[0]) && !strings.Contains(jsText, pair[1]) {
				t.Fatalf("SPA JS bundle missing view %v", pair)
			}
		}

		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "airgap-admin-1")
		if st2, _ := doJSONRequest(t, nil, http.MethodPost, srv.BaseURL+"/v1/setup", map[string]string{"password": "airgap-admin-2"}); st2 != http.StatusConflict {
			t.Fatalf("second setup = %d, want 409", st2)
		}

		claim := createClaim(t, client, srv.BaseURL, "site-lan", "lan-gw", true)
		code := asString(claim["code"])
		if len(code) != 8 {
			t.Fatalf("claim code = %q, want 8 chars", code)
		}
		if st, hdr, body := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/install.sh", nil, nil); st != http.StatusOK || !strings.Contains(hdr.Get("Content-Type"), "text/x-shellscript") || !strings.Contains(string(body), srv.BaseURL) {
			t.Fatalf("GET /install.sh failed: %d ct=%q", st, hdr.Get("Content-Type"))
		}
		if st, hdr, body := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/install.ps1", nil, nil); st != http.StatusOK || !strings.Contains(hdr.Get("Content-Type"), "text/plain") || !strings.Contains(string(body), srv.BaseURL) {
			t.Fatalf("GET /install.ps1 failed: %d ct=%q", st, hdr.Get("Content-Type"))
		}

		rSt, rMap := redeemClaim(t, srv.BaseURL, code)
		tok := asString(rMap["token"])
		if rSt != http.StatusOK || !strings.HasPrefix(tok, "lt_") {
			t.Fatalf("redeem claim = %d %v", rSt, rMap)
		}
		if rSt2, _ := redeemClaim(t, srv.BaseURL, code); rSt2 != http.StatusConflict {
			t.Fatalf("second redeem = %d, want 409", rSt2)
		}

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-lan", NodeID: "lan-gw", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Iface: true},
		}, tok, "")
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/lan-gw", nil)
			return st == http.StatusOK && asBool(decodeMap(t, b)["online"])
		})
		_, nodesBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes", nil)
		if strings.Contains(string(nodesBody), "lt_") || strings.Contains(string(nodesBody), tok) {
			t.Fatalf("GET /v1/nodes leaked agent token: %s", string(nodesBody))
		}
	})

	t.Run("S6_ShortLivedBurstsWSFallbackAndIdempotentReplay", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokWS := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-s6-ws")
		tokRep := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-s6-rep")

		proxy := newFaultIngestProxy(t, srv.BaseURL)
		mihomo := newMockMihomo(t, "s6")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "base", Upload: 500, Download: 500, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{
				{ID: "base", Upload: 500, Download: 500, Chains: []string{"DIRECT"}},
				{ID: "burst-1", Upload: 200, Download: 800, Chains: []string{"HK-01", "Proxy"}},
				{ID: "burst-2", Upload: 100, Download: 400, Chains: []string{"JP-01", "Proxy"}},
			},
			[]MockMihomoConn{{ID: "base", Upload: 500, Download: 500, Chains: []string{"DIRECT"}}},
		)

		startAgent(t, AgentConfig{
			ServerURL: proxy.URL(), SiteID: "site-e2e", NodeID: "node-s6-ws", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true}, Mihomo: &AgentMihomoConfig{URL: mihomo.URL()},
		}, tokWS, "s6")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			m := decodeMap(t, b)
			pr, _ := asUint64(asMap(m["bytes"])["proxy_raw"])
			batches, _ := asUint64(m["ingest_batches"])
			return pr == 1500 && batches >= 3
		})
		// Normal burst connection close must NOT emit collector_reset alert.
		_, aPre := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		for _, it := range asSlice(decodeMap(t, aPre)["alerts"]) {
			al := asMap(it)
			if asString(al["kind"]) == "reset" && asString(al["message"]) != "collector gap detected: reboot" {
				t.Fatalf("burst close produced unexpected reset alert: %s", string(aPre))
			}
		}

		// Drop one upstream 200 response in proxy so live agent retries and receives duplicate=true.
		proxy.SetDropAfterForward(1)
		waitUntil(t, 6*time.Second, 50*time.Millisecond, func() bool {
			var sawDropped, sawDup bool
			for _, rec := range proxy.Records() {
				if rec.Dropped {
					sawDropped = true
				}
				if !rec.Dropped && strings.Contains(rec.ResponseBody, `"duplicate":true`) {
					sawDup = true
				}
			}
			return sawDropped && sawDup
		})
		_, ovMid := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if pr, _ := asUint64(asMap(decodeMap(t, ovMid)["bytes"])["proxy_raw"]); pr != 1500 {
			t.Fatalf("proxy_raw after dropped-response retry = %d, want 1500", pr)
		}

		// Force WS -> HTTP fallback.
		mihomo.SetConnections([]MockMihomoConn{{ID: "base", Upload: 500, Download: 500, Chains: []string{"DIRECT"}}})
		mihomo.SetRejectWS(http.StatusNotFound)
		mihomo.DisconnectWS()
		waitUntil(t, 6*time.Second, 50*time.Millisecond, func() bool {
			for _, rec := range mihomo.Requests() {
				if !rec.IsWS && rec.Path == "/connections" {
					return true
				}
			}
			return false
		})
		mihomo.SetConnections([]MockMihomoConn{
			{ID: "base", Upload: 500, Download: 500, Chains: []string{"DIRECT"}},
			{ID: "c-http", Upload: 300, Download: 700, Chains: []string{"HK-01", "Proxy"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 2500
		})

		// Sequence replay, regression (409), and reboot reset verification on node-s6-rep.
		now := time.Now().UTC()
		b1 := syntheticBatch("site-e2e", "node-s6-rep", "boot-1", 1, now,
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 300, "tx_delta": 100}),
		)
		if st, r1 := postIngestBatch(t, srv.BaseURL, tokRep, b1); st != http.StatusOK || asBool(decodeMap(t, r1)["duplicate"]) {
			t.Fatalf("seq=1 = %d %s", st, string(r1))
		}
		if st, rDup := postIngestBatch(t, srv.BaseURL, tokRep, b1); st != http.StatusOK || !asBool(decodeMap(t, rDup)["duplicate"]) {
			t.Fatalf("replay seq=1 = %d %s", st, string(rDup))
		}
		b3 := syntheticBatch("site-e2e", "node-s6-rep", "boot-1", 3, now.Add(time.Second),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 200, "tx_delta": 100}),
		)
		if st, _ := postIngestBatch(t, srv.BaseURL, tokRep, b3); st != http.StatusOK {
			t.Fatalf("seq=3 = %d", st)
		}
		bRegress := syntheticBatch("site-e2e", "node-s6-rep", "boot-1", 2, now.Add(2*time.Second),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 50, "tx_delta": 50}),
		)
		if st, _ := postIngestBatch(t, srv.BaseURL, tokRep, bRegress); st != http.StatusConflict {
			t.Fatalf("regressed seq=2 = %d, want 409", st)
		}
		bBoot2 := syntheticBatch("site-e2e", "node-s6-rep", "boot-2", 1, now.Add(3*time.Second),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 50, "tx_delta": 50}),
		)
		if st, rBoot := postIngestBatch(t, srv.BaseURL, tokRep, bBoot2); st != http.StatusOK || asBool(decodeMap(t, rBoot)["duplicate"]) {
			t.Fatalf("boot-2 seq=1 = %d %s", st, string(rBoot))
		}
		_, aPost := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		foundReboot := false
		for _, it := range asSlice(decodeMap(t, aPost)["alerts"]) {
			if asString(asMap(it)["kind"]) == "reset" {
				foundReboot = true
			}
		}
		if !foundReboot {
			t.Fatalf("expected reset alert after boot-2; alerts=%s", string(aPost))
		}
	})

	t.Run("S7_MultiProviderHybridSyncAndDriftAlert", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokAgent := enrollNode(t, client, srv.BaseURL, "site-e2e", "agent-gw2")

		provs := map[string]MockMihomoProvider{
			"SharedSub": {
				Name: "SharedSub", Type: "Proxy", VehicleType: "HTTP",
				Proxies:          []MockMihomoProxy{{Name: "HK-01", Type: "Shadowsocks", Alive: true}},
				SubscriptionInfo: &MockSubscriptionInfo{Upload: 1000, Download: 4000, Total: 107374182400, Expire: 1893456000},
			},
			"default": {Name: "default", Type: "Proxy", VehicleType: "Compatible"},
		}
		mDirect := newMockMihomo(t, "dc-s7")
		mDirect.SetProviders(provs)
		mDirect.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})

		mAgent := newMockMihomo(t, "ag-s7")
		mAgent.SetProviders(provs)
		mAgent.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 1500, Download: 3500, Chains: []string{"HK-01", "Proxy"}}},
		)

		if rSt, rBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "HK2x", "match_type": "exact", "pattern": "HK-01", "factor": 2.0, "priority": 100, "enabled": true,
		}); rSt != http.StatusOK && rSt != http.StatusCreated {
			t.Fatalf("POST rule = %d %s", rSt, string(rBody))
		}
		if dcSt, dcBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "DC-GW1", "site_id": "site-e2e", "node_id": "dc-gw1",
			"url": mDirect.URL(), "secret": "dc-s7", "enabled": true,
		}); dcSt != http.StatusOK && dcSt != http.StatusCreated {
			t.Fatalf("POST direct collector = %d %s", dcSt, string(dcBody))
		}
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "agent-gw2", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true}, Mihomo: &AgentMihomoConfig{URL: mAgent.URL()},
		}, tokAgent, "ag-s7")

		if mSt, mBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/providers", map[string]any{
			"name": "BackupTB", "unit": "TB", "total_value": 2, "used_value": 0, "reset_day": 15,
		}); mSt != http.StatusOK && mSt != http.StatusCreated {
			t.Fatalf("POST manual provider = %d %s", mSt, string(mBody))
		}

		waitUntil(t, 6*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
			if st != http.StatusOK {
				return false
			}
			var hasShared, hasBackup bool
			for _, it := range asSlice(decodeMap(t, b)["providers"]) {
				p := asMap(it)
				if asString(p["name"]) == "SharedSub" {
					loc, _ := asUint64(p["local_adjusted"])
					if loc == 10000 {
						hasShared = true
					}
				}
				if asString(p["name"]) == "BackupTB" {
					hasBackup = true
				}
			}
			return hasShared && hasBackup
		})

		_, pvBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		sharedCount := 0
		for _, it := range asSlice(decodeMap(t, pvBody)["providers"]) {
			p := asMap(it)
			if asString(p["vehicle_type"]) == "Compatible" {
				t.Fatalf("Compatible provider leaked: %s", string(pvBody))
			}
			if asString(p["name"]) == "SharedSub" {
				sharedCount++
				dr, _ := asFloat64(p["drift_ratio"])
				if !asBool(p["drift_alert"]) || dr < 0.99 {
					t.Fatalf("expected drift_alert=true drift_ratio>=0.99; got %v", p)
				}
			}
			if asString(p["name"]) == "BackupTB" {
				tot, _ := asUint64(p["total_bytes"])
				if tot != 2*(1<<40) && tot != 2*1000*1000*1000*1000 {
					t.Fatalf("BackupTB total_bytes = %d, want 2 TiB or 2 TB", tot)
				}
			}
		}
		if sharedCount != 1 {
			t.Fatalf("SharedSub count = %d, want 1", sharedCount)
		}

		_, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		var driftID string
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			al := asMap(it)
			if asString(al["kind"]) == "drift" {
				driftID = fmt.Sprintf("%v", al["id"])
			}
		}
		if driftID == "" {
			t.Fatalf("expected drift alert in /v1/alerts; body=%s", string(aBody))
		}
		if ackSt, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/alerts/"+driftID+"/ack", map[string]any{}); ackSt != http.StatusOK {
			t.Fatalf("ack drift alert = %d", ackSt)
		}
	})

	t.Run("S8_DeviceMergeUnmergeAndVirtualNICIsolation", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokGW := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-s8-gw")
		tokHost := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-s8-host")

		now := time.Now().UTC()
		bGW1 := syntheticBatch("site-e2e", "node-s8-gw", "boot-1", 1, now.Add(-48*time.Hour),
			withCapabilities("nlbwmon", "iface"),
			withDevices(
				map[string]any{"obs_ip": "203.0.113.21", "obs_mac": "02:00:00:00:00:21", "rx_delta": 1000, "tx_delta": 500, "source": "nlbwmon"},
				map[string]any{"obs_ip": "203.0.113.22", "obs_mac": "02:00:00:00:00:22", "rx_delta": 600, "tx_delta": 400, "source": "nlbwmon"},
			),
			withInterfaces(
				map[string]any{"name": "eth0", "rx_delta": 1600, "tx_delta": 900},
				map[string]any{"name": "lo", "rx_delta": 5000, "tx_delta": 5000},
				map[string]any{"name": "wg0", "rx_delta": 700, "tx_delta": 300},
				map[string]any{"name": "tun0", "rx_delta": 400, "tx_delta": 100},
				map[string]any{"name": "veth0", "rx_delta": 200, "tx_delta": 100},
				map[string]any{"name": "br-0", "rx_delta": 300, "tx_delta": 200},
			),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tokGW, bGW1); st != http.StatusOK {
			t.Fatalf("ingest bGW1 = %d %s", st, string(body))
		}

		bHost1 := syntheticBatch("site-e2e", "node-s8-host", "boot-1", 1, now.Add(-2*time.Hour),
			withCapabilities("iface"),
			withInterfaces(
				map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 200},
				map[string]any{"name": "wg0", "rx_delta": 800, "tx_delta": 200},
				map[string]any{"name": "lo", "rx_delta": 3000, "tx_delta": 3000},
			),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tokHost, bHost1); st != http.StatusOK {
			t.Fatalf("ingest bHost1 = %d %s", st, string(body))
		}

		_, nTr := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=node&node=node-s8-host", nil)
		if hostTot, _ := asUint64(asMap(decodeMap(t, nTr)["totals"])["node-s8-host"]); hostTot != 1200 {
			t.Fatalf("node-s8-host total = %d, want 1200 (eth0 only); body=%s", hostTot, string(nTr))
		}
		_, if7 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface&range=7d", nil)
		if7T := asMap(decodeMap(t, if7)["totals"])
		for k, want := range map[string]uint64{"wg0": 2000, "tun0": 500, "lo": 16000, "veth0": 300, "br-0": 500} {
			if got, _ := asUint64(if7T[k]); got != want {
				t.Fatalf("7d iface %s = %d, want %d; body=%s", k, got, want, string(if7))
			}
		}
		_, if24 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface&range=24h", nil)
		if24T := asMap(decodeMap(t, if24)["totals"])
		if wg24, _ := asUint64(if24T["wg0"]); wg24 != 1000 {
			t.Fatalf("24h iface wg0 = %d, want 1000; body=%s", wg24, string(if24))
		}

		_, dBody0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		var idA, idB string
		for _, raw := range asSlice(decodeMap(t, dBody0)["devices"]) {
			d := asMap(raw)
			switch asString(d["mac"]) {
			case "02:00:00:00:00:21":
				idA = asString(d["id"])
			case "02:00:00:00:00:22":
				idB = asString(d["id"])
			}
		}
		if idA == "" || idB == "" {
			t.Fatalf("failed to find devA/devB: %s", string(dBody0))
		}
		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/devices/"+idA, map[string]string{"name": "Studio-Mac"}); st != http.StatusOK {
			t.Fatalf("rename devA = %d", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+idA+"/merge", map[string]string{"other_id": idB, "reason": "dual nic"}); st != http.StatusOK {
			t.Fatalf("merge = %d", st)
		}

		bGW2 := syntheticBatch("site-e2e", "node-s8-gw", "boot-1", 2, now.Add(-1*time.Hour),
			withCapabilities("nlbwmon"),
			withDevices(map[string]any{"obs_ip": "203.0.113.22", "obs_mac": "02:00:00:00:00:22", "rx_delta": 300, "tx_delta": 200, "source": "nlbwmon"}),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tokGW, bGW2); st != http.StatusOK {
			t.Fatalf("ingest bGW2 = %d %s", st, string(body))
		}

		_, dBody1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs1 := asSlice(decodeMap(t, dBody1)["devices"])
		if len(devs1) != 1 || asString(asMap(devs1[0])["name"]) != "Studio-Mac" {
			t.Fatalf("expected 1 canonical Studio-Mac; body=%s", string(dBody1))
		}
		if tot, _ := asUint64(asMap(devs1[0])["total"]); tot != 2000 {
			t.Fatalf("canonical device total = %d, want 2000 (1500 pre-merge + 500 post-merge); body=%s", tot, string(dBody1))
		}
		_, detBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices/"+idA, nil)
		if len(asSlice(decodeMap(t, detBody)["merged_from"])) != 1 {
			t.Fatalf("expected 1 merged_from entry; body=%s", string(detBody))
		}

		_, dev24 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=device&range=24h", nil)
		if a24, _ := asUint64(asMap(decodeMap(t, dev24)["totals"])[idA]); a24 != 500 {
			t.Fatalf("24h traffic for canonical idA = %d, want 500; body=%s", a24, string(dev24))
		}

		// Unmerge B -> post-unmerge batch for B routes back to B.
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+idB+"/unmerge", map[string]any{}); st != http.StatusOK {
			t.Fatalf("unmerge = %d", st)
		}
		bGW3 := syntheticBatch("site-e2e", "node-s8-gw", "boot-1", 3, now.Add(-10*time.Minute),
			withCapabilities("nlbwmon"),
			withDevices(map[string]any{"obs_ip": "203.0.113.22", "obs_mac": "02:00:00:00:00:22", "rx_delta": 100, "tx_delta": 50, "source": "nlbwmon"}),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tokGW, bGW3); st != http.StatusOK {
			t.Fatalf("ingest bGW3 = %d %s", st, string(body))
		}
		_, dBody2 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs2 := asSlice(decodeMap(t, dBody2)["devices"])
		if len(devs2) != 2 {
			t.Fatalf("expected 2 devices after unmerge; body=%s", string(dBody2))
		}
		gotByID := map[string]uint64{}
		for _, raw := range devs2 {
			d := asMap(raw)
			v, _ := asUint64(d["total"])
			gotByID[asString(d["id"])] = v
		}
		if gotByID[idA] != 2000 || gotByID[idB] != 1150 {
			t.Fatalf("post-unmerge totals idA=%d idB=%d, want 2000/1150; body=%s", gotByID[idA], gotByID[idB], string(dBody2))
		}
	})

	t.Run("S9_RestartMigrationBackupAndVMPrivacyPush", func(t *testing.T) {
		vm := newMockVM(t)
		dbPath := filepath.Join(t.TempDir(), "s9-mig.db")
		srv1 := startServer(t, ServerOpts{DBPath: dbPath, MetricsURL: vm.URL()})
		c1 := newCookieClient(t)
		setupAdmin(t, c1, srv1.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, c1, srv1.BaseURL, "site-e2e", "node-s9")

		if rSt, rBody := doJSONRequest(t, c1, http.MethodPost, srv1.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "IEPL2x", "match_type": "keyword", "pattern": "IEPL", "factor": 2.0, "priority": 100, "enabled": true,
		}); rSt != http.StatusOK && rSt != http.StatusCreated {
			t.Fatalf("POST rule = %d %s", rSt, string(rBody))
		}

		b1 := syntheticBatch("site-e2e", "node-s9", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface", "mihomo"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1200, "tx_delta": 300}),
			withDevices(map[string]any{
				"obs_ip": "203.0.113.99", "obs_mac": "02:00:00:00:00:99",
				"rx_delta": 800, "tx_delta": 200, "source": "mihomo", "outbound": "HK-IEPL-01",
			}),
			withProxyOutbounds(map[string]any{"name": "HK-IEPL-01", "proxy_rx": 800, "proxy_tx": 200}),
			withDimensions(
				map[string]any{"type": "domain", "key": "example.com", "rx_delta": 800, "tx_delta": 200},
				map[string]any{"type": "process", "key": "curl", "rx_delta": 800, "tx_delta": 200},
			),
		)
		if st, body := postIngestBatch(t, srv1.BaseURL, tok, b1); st != http.StatusOK {
			t.Fatalf("ingest b1 = %d %s", st, string(body))
		}

		reqs := vm.WaitForSamples(t, 5*time.Second, 1)
		allowed := map[string]bool{"site_id": true, "node_id": true, "device_id": true, "class": true, "direction": true}
		labelRe := regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="[^"]*"`)
		forbidden := []string{"203.0.113.", "02:00:00:", "example.com", "curl", "HK-IEPL-01", "domain", "destination", "source_ip", "conn_id", "url"}
		for _, r := range reqs {
			for _, bad := range forbidden {
				if strings.Contains(r.RawBody, bad) {
					t.Fatalf("forbidden substring %q leaked in VM payload: %s", bad, r.RawBody)
				}
			}
			for _, line := range r.Lines {
				for _, m := range labelRe.FindAllStringSubmatch(line, -1) {
					if !allowed[m[1]] {
						t.Fatalf("non-whitelisted VM label %q in %q", m[1], line)
					}
				}
			}
		}

		bkSt, bkHdr, bkBytes := doRawRequest(t, c1, http.MethodGet, srv1.BaseURL+"/v1/backup", nil, nil)
		if bkSt != http.StatusOK || !strings.Contains(bkHdr.Get("Content-Disposition"), "attachment") || !strings.HasPrefix(string(bkBytes), "SQLite format 3\x00") {
			t.Fatalf("GET /v1/backup failed: status=%d disp=%q len=%d", bkSt, bkHdr.Get("Content-Disposition"), len(bkBytes))
		}
		srv1.Stop()

		rawDB, err := os.ReadFile(dbPath)
		if err != nil {
			t.Fatalf("read db: %v", err)
		}
		binary.BigEndian.PutUint32(rawDB[60:64], 1)
		if err := os.WriteFile(dbPath, rawDB, 0o600); err != nil {
			t.Fatalf("write patched db: %v", err)
		}

		srv2 := startServer(t, ServerOpts{DBPath: dbPath, MetricsURL: vm.URL()})
		baks, err := filepath.Glob(filepath.Join(filepath.Dir(dbPath), "lantally.db.bak-*"))
		if err != nil || len(baks) == 0 {
			t.Fatalf("expected pre-migration backup lantally.db.bak-*, got %v (err=%v)", baks, err)
		}

		c2 := newCookieClient(t)
		if lSt, _ := loginAdmin(t, c2, srv2.BaseURL, "e2e-admin-pass-1"); lSt != http.StatusOK {
			t.Fatalf("re-login after restart = %d", lSt)
		}
		_, ov2 := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/overview", nil)
		bm2 := asMap(decodeMap(t, ov2)["bytes"])
		if tot, _ := asUint64(bm2["total"]); tot != 1500 {
			t.Fatalf("post-migration total = %d, want 1500", tot)
		}
		if pr, _ := asUint64(bm2["proxy_raw"]); pr != 1000 {
			t.Fatalf("post-migration proxy_raw = %d, want 1000", pr)
		}
		if pa, _ := asUint64(bm2["proxy_adjusted"]); pa != 2000 {
			t.Fatalf("post-migration proxy_adjusted = %d, want 2000", pa)
		}
		_, rulesBody := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/proxy/rules", nil)
		if !strings.Contains(string(rulesBody), "IEPL2x") {
			t.Fatalf("rule lost across migration/restart: %s", string(rulesBody))
		}
		_, devBody := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/devices", nil)
		if len(asSlice(decodeMap(t, devBody)["devices"])) != 1 {
			t.Fatalf("device lost across migration/restart: %s", string(devBody))
		}

		// Replay seq=1 after restart -> must deduplicate cleanly.
		if dupSt, dupBody := postIngestBatch(t, srv2.BaseURL, tok, b1); dupSt != http.StatusOK || !asBool(decodeMap(t, dupBody)["duplicate"]) {
			t.Fatalf("replay seq=1 post-restart = %d %s", dupSt, string(dupBody))
		}
		_, ov3 := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/overview", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, ov3)["bytes"])["total"]); tot != 1500 {
			t.Fatalf("post-replay total = %d, want 1500 (no double-count)", tot)
		}
	})
}
