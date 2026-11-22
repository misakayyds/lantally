//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTier3_Pairwise(t *testing.T) {
	t.Run("F1_F2_BatchV1HeartbeatHotReload", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokProbe := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p1-2-probe")
		tokAgent := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p1-2")

		// F1+F2: default preferences must emit exact v0.1 JSON and DisallowUnknownFields must reject extra fields.
		probeBatch := syntheticBatch("site-e2e", "node-p1-2-probe", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 100, "tx_delta": 50}),
		)
		pSt, pBody := postIngestBatch(t, srv.BaseURL, tokProbe, probeBatch)
		if pSt != http.StatusOK || string(pBody) != "{\"status\":\"ok\",\"duplicate\":false}\n" {
			t.Fatalf("probe ingest = %d %q, want exact v0.1 JSON", pSt, string(pBody))
		}
		badBatch := syntheticBatch("site-e2e", "node-p1-2-probe", "boot-1", 2, time.Now().UTC())
		badBatch["extra_v2"] = 1
		if bSt, _ := postIngestBatch(t, srv.BaseURL, tokProbe, badBatch); bSt != http.StatusBadRequest {
			t.Fatalf("unknown field batch status = %d, want 400", bSt)
		}

		mihomo := newMockMihomo(t, "s1")
		mihomo.SetConnections([]MockMihomoConn{{ID: "c0", Upload: 10, Download: 10, Chains: []string{"DIRECT"}}})
		agent := startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-p1-2", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tokAgent, "s1")
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
			return mihomo.WSConnectCount() >= 1 && batches >= 2
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "c0", Upload: 10, Download: 10, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 200, Download: 400, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "sub.alpha.example"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, pr := getOverview()
			return pr == 600
		})
		_, dom0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if len(asMap(decodeMap(t, dom0)["totals"])) != 0 {
			t.Fatalf("expected 0 domains before opt-in; body=%s", string(dom0))
		}

		// Enable collect_domains on server and wait for at least one agent ingest heartbeat to deliver it.
		b0, _ := getOverview()
		if cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-p1-2/config", map[string]any{
			"collect_domains": true,
		}); cSt != http.StatusOK {
			t.Fatalf("PUT collect_domains=true failed: %d %s", cSt, string(cBody))
		}
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			batches, _ := getOverview()
			return batches >= b0+2
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "c0", Upload: 10, Download: 10, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 200, Download: 400, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "sub.alpha.example"}},
			{ID: "c2", Upload: 300, Download: 700, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "cdn.beta.example"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if st != http.StatusOK {
				return false
			}
			totals := asMap(decodeMap(t, b)["totals"])
			beta, _ := asUint64(totals["beta.example"])
			return beta == 1000
		})
		_, dom1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if _, hasAlpha := asMap(decodeMap(t, dom1)["totals"])["alpha.example"]; hasAlpha {
			t.Fatalf("alpha.example from pre-opt-in window should not appear; body=%s", string(dom1))
		}
		if agent.ConfigSHA256(t) != cfgHash {
			t.Fatalf("agent config mutated on disk during hot reload")
		}

		// Toggle collect_domains=false and verify new domains stop recording while proxy_raw advances.
		b1, _ := getOverview()
		if cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-p1-2/config", map[string]any{
			"collect_domains": false,
		}); cSt != http.StatusOK {
			t.Fatalf("PUT collect_domains=false failed: %d %s", cSt, string(cBody))
		}
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			batches, _ := getOverview()
			return batches >= b1+2
		})
		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "c0", Upload: 10, Download: 10, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 200, Download: 400, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "sub.alpha.example"}},
			{ID: "c2", Upload: 300, Download: 700, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "cdn.beta.example"}},
			{ID: "c3", Upload: 100, Download: 200, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "api.gamma.example"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, pr := getOverview()
			return pr == 1900
		})
		_, dom2 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if _, hasGamma := asMap(decodeMap(t, dom2)["totals"])["gamma.example"]; hasGamma {
			t.Fatalf("gamma.example recorded after collect_domains=false; body=%s", string(dom2))
		}
	})

	t.Run("F3_F11_ChainsExitNodeRetroactiveMultiplier", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p3-11")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-p3-11", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			if st != http.StatusOK {
				return false
			}
			batches, _ := asUint64(decodeMap(t, b)["ingest_batches"])
			return mihomo.WSConnectCount() >= 1 && batches >= 1
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 200, Download: 800, Chains: []string{"HK-IEPL-01", "Auto", "AI"}},
			{ID: "c2", Upload: 100, Download: 400, Chains: []string{"US-Plain-02", "Streaming"}},
			{ID: "c3", Upload: 50, Download: 150, Chains: []string{"DIRECT"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			bm := asMap(decodeMap(t, b)["bytes"])
			pr, _ := asUint64(bm["proxy_raw"])
			dir, _ := asUint64(bm["direct"])
			return pr == 1500 && dir == 200
		})

		_, p0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		obs0 := map[string]map[string]any{}
		for _, item := range asSlice(decodeMap(t, p0)["outbounds"]) {
			m := asMap(item)
			name := asString(m["name"])
			if name == "AI" || name == "Auto" || name == "Streaming" || name == "DIRECT" {
				t.Fatalf("unexpected non-exit outbound %q; body=%s", name, string(p0))
			}
			obs0[name] = m
		}
		if f, _ := asFloat64(obs0["HK-IEPL-01"]["factor"]); f != 1.0 || asString(obs0["HK-IEPL-01"]["match_source"]) != "default" {
			t.Fatalf("expected HK-IEPL-01 default 1.0x before rules; got=%v", obs0["HK-IEPL-01"])
		}

		// Add IEPL -> 2.5x AND a decoy rule matching strategy group "AI" -> 9.0x (must NOT match HK-IEPL-01).
		if st, body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "IEPL25", "match_type": "keyword", "pattern": "IEPL", "factor": 2.5, "priority": 100, "enabled": true,
		}); st != http.StatusOK && st != http.StatusCreated {
			t.Fatalf("POST rule IEPL25 = %d %s", st, string(body))
		}
		if st, body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "GroupDecoy", "match_type": "exact", "pattern": "AI", "factor": 9.0, "priority": 200, "enabled": true,
		}); st != http.StatusOK && st != http.StatusCreated {
			t.Fatalf("POST rule GroupDecoy = %d %s", st, string(body))
		}

		_, ov1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		bm1 := asMap(decodeMap(t, ov1)["bytes"])
		pr1, _ := asUint64(bm1["proxy_raw"])
		pa1, _ := asUint64(bm1["proxy_adjusted"])
		pu1, _ := asUint64(bm1["proxy_unadjusted"])
		if pr1 != 1500 || pa1 != 3000 || pu1 != 500 {
			t.Fatalf("overview bytes raw=%d adj=%d unadj=%d, want 1500/3000/500; body=%s", pr1, pa1, pu1, string(ov1))
		}

		_, obBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=outbound&class=proxy_adjusted", nil)
		obTot := asMap(decodeMap(t, obBody)["totals"])
		hkAdj, _ := asUint64(obTot["HK-IEPL-01"])
		usAdj, _ := asUint64(obTot["US-Plain-02"])
		if hkAdj != 2500 || usAdj != 500 {
			t.Fatalf("outbound proxy_adjusted HK=%d US=%d, want 2500/500; body=%s", hkAdj, usAdj, string(obBody))
		}

		_, grpBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		grpTot := asMap(decodeMap(t, grpBody)["totals"])
		aiTot, _ := asUint64(grpTot["AI"])
		stTot, _ := asUint64(grpTot["Streaming"])
		if aiTot != 1000 || stTot != 500 {
			t.Fatalf("group totals AI=%d Streaming=%d, want raw 1000/500; body=%s", aiTot, stTot, string(grpBody))
		}
	})

	t.Run("F4_F7_WSBurstReconnectAndVirtualNICExclusion", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokWS := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p4-7-ws")
		tokNIC := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p4-7-nic")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "c1", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"}}})
		agent := startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-p4-7-ws", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true, Iface: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tokWS, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.WSConnectCount() >= 1 })
		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "c1", Upload: 200, Download: 300, Chains: []string{"HK-01", "Proxy"}},
			{ID: "short-1", Upload: 300, Download: 700, Chains: []string{"HK-01", "Proxy"}},
		})
		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "c1", Upload: 200, Download: 300, Chains: []string{"HK-01", "Proxy"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 1300
		})

		mihomo.DisconnectWS()
		waitUntil(t, 6*time.Second, 50*time.Millisecond, func() bool { return mihomo.WSConnectCount() >= 2 })
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
			if st != http.StatusOK {
				return false
			}
			for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
				al := asMap(it)
				if asString(al["kind"]) == "reset" && strings.Contains(asString(al["message"]), "collector_reset") {
					return true
				}
			}
			return false
		})
		if strings.Contains(strings.ToLower(agent.Logs()), "unsupported") {
			t.Fatalf("agent logs contain unsupported error: %s", agent.Logs())
		}
		_, ovAfter := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if pr, _ := asUint64(asMap(decodeMap(t, ovAfter)["bytes"])["proxy_raw"]); pr != 1300 {
			t.Fatalf("proxy_raw after WS reconnect = %d, want 1300", pr)
		}

		// Deterministic multi-NIC + proxy batch verifying virtual NIC exclusion while iface dimension keeps all NICs.
		b := syntheticBatch("site-e2e", "node-p4-7-nic", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface", "mihomo"),
			withInterfaces(
				map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 500},
				map[string]any{"name": "lo", "rx_delta": 9000, "tx_delta": 9000},
				map[string]any{"name": "tun0", "rx_delta": 1300, "tx_delta": 0},
				map[string]any{"name": "wg0", "rx_delta": 500, "tx_delta": 500},
			),
			withProxyOutbounds(map[string]any{"name": "HK-01", "proxy_rx": 1000, "proxy_tx": 300}),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tokNIC, b); st != http.StatusOK {
			t.Fatalf("ingest node-p4-7-nic failed: %d %s", st, string(body))
		}
		_, nodeTr := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=node&node=node-p4-7-nic", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, nodeTr)["totals"])["node-p4-7-nic"]); tot != 1500 {
			t.Fatalf("node-p4-7-nic total = %d, want 1500 (virtual NICs excluded); body=%s", tot, string(nodeTr))
		}
		_, ifaceTr := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface&node=node-p4-7-nic", nil)
		ifTot := asMap(decodeMap(t, ifaceTr)["totals"])
		for k, want := range map[string]uint64{"eth0": 1500, "lo": 18000, "tun0": 1300, "wg0": 1000} {
			if got, _ := asUint64(ifTot[k]); got != want {
				t.Fatalf("iface %s = %d, want %d; body=%s", k, got, want, string(ifaceTr))
			}
		}

		// Same-node combined F4+F7: proxy outbound + collector_reset gap + mixed physical/virtual NICs on one node.
		tokCombo := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p4-7-combo")
		nowCombo := time.Now().UTC()
		bCombo := syntheticBatch("site-e2e", "node-p4-7-combo", "boot-c", 1, nowCombo,
			withCapabilities("iface", "mihomo"),
			withInterfaces(
				map[string]any{"name": "eth0", "rx_delta": 1200, "tx_delta": 800},
				map[string]any{"name": "lo", "rx_delta": 5000, "tx_delta": 5000},
				map[string]any{"name": "tun0", "rx_delta": 900, "tx_delta": 100},
				map[string]any{"name": "wg0", "rx_delta": 400, "tx_delta": 200},
			),
			withProxyOutbounds(map[string]any{"name": "HK-01", "proxy_rx": 700, "proxy_tx": 300}),
			withGaps(map[string]any{
				"reason": "collector_reset",
				"from":   nowCombo.Add(-2 * time.Second).Format(time.RFC3339),
				"to":     nowCombo.Format(time.RFC3339),
			}),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tokCombo, bCombo); st != http.StatusOK {
			t.Fatalf("ingest node-p4-7-combo failed: %d %s", st, string(body))
		}
		_, comboNodeTr := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=node&node=node-p4-7-combo", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, comboNodeTr)["totals"])["node-p4-7-combo"]); tot != 2000 {
			t.Fatalf("node-p4-7-combo total = %d, want 2000 (eth0 only); body=%s", tot, string(comboNodeTr))
		}
		_, comboIfaceTr := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface&node=node-p4-7-combo", nil)
		comboIfTot := asMap(decodeMap(t, comboIfaceTr)["totals"])
		for k, want := range map[string]uint64{"eth0": 2000, "lo": 10000, "tun0": 1000, "wg0": 600} {
			if got, _ := asUint64(comboIfTot[k]); got != want {
				t.Fatalf("combo iface %s = %d, want %d; body=%s", k, got, want, string(comboIfaceTr))
			}
		}

		// All-virtual-NIC node falls back to Mihomo proxy+direct totals while preserving iface records.
		tokVirt := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p4-7-virt")
		bVirt := syntheticBatch("site-e2e", "node-p4-7-virt", "boot-v", 1, time.Now().UTC(),
			withCapabilities("iface", "mihomo"),
			withInterfaces(
				map[string]any{"name": "lo", "rx_delta": 3000, "tx_delta": 3000},
				map[string]any{"name": "utun4", "rx_delta": 800, "tx_delta": 200},
			),
			withProxyOutbounds(
				map[string]any{"name": "DIRECT", "direct_rx": 150, "direct_tx": 50},
				map[string]any{"name": "HK-01", "proxy_rx": 600, "proxy_tx": 400},
			),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tokVirt, bVirt); st != http.StatusOK {
			t.Fatalf("ingest node-p4-7-virt failed: %d %s", st, string(body))
		}
		_, virtNodeTr := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=node&node=node-p4-7-virt", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, virtNodeTr)["totals"])["node-p4-7-virt"]); tot != 1200 {
			t.Fatalf("all-virtual-NIC node fallback total = %d, want 1200; body=%s", tot, string(virtNodeTr))
		}
	})

	t.Run("F5_F12_OptInDomainWatchExceptionAndQuota", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p5-12")

		wSt, wBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name": "AI API",
			"matchers": map[string]any{
				"domain": map[string]any{"type": "exact", "values": []string{"api.watched.test"}},
			},
			"quota_value": 1, "quota_unit": "MB", "reset_type": "monthly", "reset_day": 1, "pinned": true,
		})
		if wSt != http.StatusOK && wSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch status = %d; body=%s", wSt, string(wBody))
		}

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-p5-12", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			batches, _ := asUint64(decodeMap(t, b)["ingest_batches"])
			return mihomo.WSConnectCount() >= 1 && batches >= 1
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 524288, Download: 1048576, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "sub.api.watched.test"}},
			{ID: "c2", Upload: 500000, Download: 500000, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "private.unwatched.example"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if st != http.StatusOK {
				return false
			}
			tot, _ := asUint64(asMap(decodeMap(t, b)["totals"])["api.watched.test"])
			return tot == 1572864
		})

		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=domain", nil)
		if strings.Contains(string(dBody), "unwatched.example") {
			t.Fatalf("unwatched domain leaked while collect_domains=false: %s", string(dBody))
		}

		_, wListBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/watch", nil)
		items := asSlice(decodeMap(t, wListBody)["items"])
		if len(items) != 1 {
			t.Fatalf("expected 1 watch item; body=%s", string(wListBody))
		}
		it := asMap(items[0])
		rx, _ := asUint64(it["rx"])
		tx, _ := asUint64(it["tx"])
		adj, _ := asUint64(it["adjusted"])
		ratio, _ := asFloat64(it["used_ratio"])
		if rx != 1048576 || tx != 524288 || adj != 1572864 || !asBool(it["over_quota"]) || ratio < 1.5 {
			t.Fatalf("unexpected watch evaluation: %s", string(wListBody))
		}
		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		pinned := asSlice(decodeMap(t, ovBody)["pinned_watch"])
		if len(pinned) == 0 || !strings.Contains(asString(asMap(pinned[0])["name"]), "AI API") {
			t.Fatalf("AI API missing from overview.pinned_watch: %s", string(ovBody))
		}
	})

	t.Run("F6_F13_ProvidersSyncCompatibleSharedAndDriftAlert", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokA := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p6-13-a")
		tokB := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p6-13-b")

		shared := map[string]MockMihomoProvider{
			"SharedAirport": {
				Name: "SharedAirport", Type: "Proxy", VehicleType: "HTTP",
				Proxies:          []MockMihomoProxy{{Name: "HK-Shared-01", Type: "Shadowsocks", Alive: true}},
				SubscriptionInfo: &MockSubscriptionInfo{Upload: 200, Download: 800, Total: 100000, Expire: 1893456000},
			},
			"default": {Name: "default", Type: "Proxy", VehicleType: "Compatible"},
		}
		mockA := newMockMihomo(t, "s")
		mockA.SetProviders(shared)
		mockA.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 500, Download: 1500, Chains: []string{"HK-Shared-01", "Proxy"}}},
		)
		mockB := newMockMihomo(t, "s")
		mockB.SetProviders(shared)
		mockB.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-p6-13-a", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true}, Mihomo: &AgentMihomoConfig{URL: mockA.URL()},
		}, tokA, "s")
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-p6-13-b", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true}, Mihomo: &AgentMihomoConfig{URL: mockB.URL()},
		}, tokB, "s")

		if mSt, mBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/providers", map[string]any{
			"name": "BackupPool", "unit": "TB", "total_value": 2, "used_value": 0, "reset_day": 15,
		}); mSt != http.StatusOK && mSt != http.StatusCreated {
			t.Fatalf("POST manual provider status = %d; body=%s", mSt, string(mBody))
		}

		waitUntil(t, 6*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
			if st != http.StatusOK {
				return false
			}
			var foundShared, foundBackup bool
			for _, it := range asSlice(decodeMap(t, b)["providers"]) {
				p := asMap(it)
				if asString(p["name"]) == "SharedAirport" {
					loc, _ := asUint64(p["local_adjusted"])
					if loc == 2000 {
						foundShared = true
					}
				}
				if asString(p["name"]) == "BackupPool" {
					foundBackup = true
				}
			}
			return foundShared && foundBackup
		})

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		sharedCount := 0
		for _, it := range asSlice(decodeMap(t, pBody)["providers"]) {
			p := asMap(it)
			if asString(p["vehicle_type"]) == "Compatible" {
				t.Fatalf("Compatible provider must be filtered out: %s", string(pBody))
			}
			if asString(p["name"]) == "SharedAirport" {
				sharedCount++
				tot, _ := asUint64(p["total_bytes"])
				dr, _ := asFloat64(p["drift_ratio"])
				if tot != 100000 || !asBool(p["drift_alert"]) || dr < 0.9 {
					t.Fatalf("SharedAirport unexpected fields: %v", p)
				}
			}
			if asString(p["name"]) == "BackupPool" {
				tot, _ := asUint64(p["total_bytes"])
				if tot != 2*(1<<40) && tot != 2*1000*1000*1000*1000 {
					t.Fatalf("BackupPool total_bytes = %d, want 2 TiB or 2 TB", tot)
				}
			}
		}
		if sharedCount != 1 {
			t.Fatalf("SharedAirport count = %d, want 1 (shared sub dedup); body=%s", sharedCount, string(pBody))
		}
		_, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		hasDrift := false
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			if asString(asMap(it)["kind"]) == "drift" {
				hasDrift = true
			}
		}
		if !hasDrift {
			t.Fatalf("expected drift alert in /v1/alerts; body=%s", string(aBody))
		}
	})

	t.Run("F8_F11_DirectCollectorPollingAndMultiplierPreview", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		mihomo := newMockMihomo(t, "dc-sec")
		mihomo.SetProxies(map[string]MockMihomoProxy{
			"HK-IEPL-01": {Name: "HK-IEPL-01", Type: "Shadowsocks", Alive: true},
			"SG-x2.5":    {Name: "SG-x2.5", Type: "VMess", Alive: true},
			"JP-Plain":   {Name: "JP-Plain", Type: "Trojan", Alive: true},
		})
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{
				{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
				{ID: "c1", Upload: 400, Download: 600, Chains: []string{"HK-IEPL-01", "Proxy"}},
				{ID: "c2", Upload: 200, Download: 800, Chains: []string{"SG-x2.5", "Proxy"}},
			},
		)

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "LocalMihomo", "site_id": "site-e2e", "node_id": "dc-p8-11",
			"url": mihomo.URL(), "secret": "dc-sec", "enabled": true,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("POST /v1/direct-collectors status = %d; body=%s", cSt, string(cBody))
		}

		waitUntil(t, 6*time.Second, 100*time.Millisecond, func() bool {
			_, ov := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, ov)["bytes"])["proxy_raw"])
			if pr != 2000 {
				return false
			}
			pSt, pBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/preview", map[string]any{})
			if pSt != http.StatusOK {
				return false
			}
			s := string(pBody)
			return strings.Contains(s, "HK-IEPL-01") && strings.Contains(s, "SG-x2.5") && strings.Contains(s, "JP-Plain")
		})

		if st, body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "RegexIEPL", "match_type": "regex", "pattern": `^HK-IEPL-`, "factor": 3.0, "priority": 100, "enabled": true,
		}); st != http.StatusOK && st != http.StatusCreated {
			t.Fatalf("POST regex rule = %d %s", st, string(body))
		}
		if st, body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "Auto", "match_type": "auto_extract", "pattern": "", "factor": 1.0, "priority": 50, "enabled": true,
		}); st != http.StatusOK && st != http.StatusCreated {
			t.Fatalf("POST auto_extract rule = %d %s", st, string(body))
		}

		pSt, pBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/preview", map[string]any{})
		if pSt != http.StatusOK {
			t.Fatalf("preview status = %d; body=%s", pSt, string(pBody))
		}
		factors := map[string]float64{}
		sources := map[string]string{}
		for _, it := range extractPreviewMatches(t, pBody) {
			row := asMap(it)
			f, _ := asFloat64(row["factor"])
			factors[asString(row["name"])] = f
			sources[asString(row["name"])] = asString(row["match_source"])
		}
		if factors["HK-IEPL-01"] != 3.0 || sources["HK-IEPL-01"] != "rule" ||
			factors["SG-x2.5"] != 2.5 || sources["SG-x2.5"] != "rule" ||
			factors["JP-Plain"] != 1.0 || sources["JP-Plain"] != "default" {
			t.Fatalf("unexpected preview factors=%v sources=%v", factors, sources)
		}

		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		bm := asMap(decodeMap(t, ovBody)["bytes"])
		pr, _ := asUint64(bm["proxy_raw"])
		pa, _ := asUint64(bm["proxy_adjusted"])
		if pr != 2000 || pa != 5500 {
			t.Fatalf("overview raw=%d adjusted=%d, want 2000/5500; body=%s", pr, pa, string(ovBody))
		}
	})

	t.Run("F9_F10_MultiDimRxTxLedgerAndTimeRangeFilters", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "p9-10.db")
		srv := startServer(t, ServerOpts{DBPath: dbPath})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p9-10")

		now := time.Now().UTC()
		midnightUTC := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		tzOffsetMin := 0
		if now.Sub(midnightUTC) < 2*time.Minute || midnightUTC.Add(24*time.Hour).Sub(now) < 2*time.Minute {
			tzOffsetMin = 360
		}

		batches := []struct {
			seq uint64
			at  time.Time
			dim []map[string]any
		}{
			{1, now.Add(-20 * 24 * time.Hour), []map[string]any{
				{"type": "process", "key": "curl", "rx_delta": 1000, "tx_delta": 500},
				{"type": "group", "key": "AI", "rx_delta": 1000, "tx_delta": 500},
				{"type": "domain", "key": "old.test", "rx_delta": 1000, "tx_delta": 500},
				{"type": "iface", "key": "eth0", "rx_delta": 1000, "tx_delta": 500},
			}},
			{2, now.Add(-48 * time.Hour), []map[string]any{
				{"type": "process", "key": "curl", "rx_delta": 600, "tx_delta": 200},
				{"type": "group", "key": "AI", "rx_delta": 600, "tx_delta": 200},
				{"type": "iface", "key": "eth0", "rx_delta": 600, "tx_delta": 200},
			}},
			{3, now.Add(-10 * time.Second), []map[string]any{
				{"type": "process", "key": "git", "rx_delta": 300, "tx_delta": 100},
				{"type": "group", "key": "Dev", "rx_delta": 300, "tx_delta": 100},
				{"type": "iface", "key": "eth0", "rx_delta": 300, "tx_delta": 100},
			}},
		}
		for _, tc := range batches {
			b := syntheticBatch("site-e2e", "node-p9-10", "boot-1", tc.seq, tc.at,
				withCapabilities("mihomo", "iface"),
				withDimensions(tc.dim...),
			)
			if st, body := postIngestBatch(t, srv.BaseURL, tok, b); st != http.StatusOK {
				t.Fatalf("ingest seq=%d failed: %d %s", tc.seq, st, string(body))
			}
		}
		srv.Stop()

		// Restart triggers MaintainLedgers rollup (>14d rolled into dimension_daily and purged from dimension_samples).
		srv2 := startServer(t, ServerOpts{DBPath: dbPath})
		c2 := newCookieClient(t)
		if lSt, _ := loginAdmin(t, c2, srv2.BaseURL, "e2e-admin-pass-1"); lSt != http.StatusOK {
			t.Fatalf("re-login failed: %d", lSt)
		}

		s30, b30 := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/traffic?group=process&range=30d", nil)
		if s30 != http.StatusOK {
			t.Fatalf("30d traffic status = %d; body=%s", s30, string(b30))
		}
		m30 := decodeMap(t, b30)
		if bs, _ := asUint64(m30["bucket_seconds"]); bs != 86400 {
			t.Fatalf("30d bucket_seconds = %d, want 86400", bs)
		}
		if cRx, _ := asUint64(asMap(m30["rx_totals"])["curl"]); cRx != 1600 {
			t.Fatalf("30d curl rx = %d, want 1600; body=%s", cRx, string(b30))
		}
		if cTx, _ := asUint64(asMap(m30["tx_totals"])["curl"]); cTx != 700 {
			t.Fatalf("30d curl tx = %d, want 700; body=%s", cTx, string(b30))
		}
		if gTot, _ := asUint64(asMap(m30["totals"])["git"]); gTot != 400 {
			t.Fatalf("30d git total = %d, want 400; body=%s", gTot, string(b30))
		}

		_, b7 := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/traffic?group=process&range=7d", nil)
		m7 := decodeMap(t, b7)
		if cTot7, _ := asUint64(asMap(m7["totals"])["curl"]); cTot7 != 800 {
			t.Fatalf("7d curl total = %d, want 800; body=%s", cTot7, string(b7))
		}
		if gTot7, _ := asUint64(asMap(m7["totals"])["git"]); gTot7 != 400 {
			t.Fatalf("7d git total = %d, want 400; body=%s", gTot7, string(b7))
		}

		_, bToday := doJSONRequest(t, c2, http.MethodGet, fmt.Sprintf("%s/v1/traffic?group=process&range=today&tz_offset=%d", srv2.BaseURL, tzOffsetMin), nil)
		tToday := asMap(decodeMap(t, bToday)["totals"])
		if gToday, _ := asUint64(tToday["git"]); gToday != 400 {
			t.Fatalf("today git total = %d, want 400; body=%s", gToday, string(bToday))
		}
		if _, hasCurl := tToday["curl"]; hasCurl {
			t.Fatalf("yesterday/old curl should be excluded from today; body=%s", string(bToday))
		}

		dSt, dBody := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/dimensions?type=group&range=30d&sort=rx", nil)
		if dSt != http.StatusOK {
			t.Fatalf("dimensions 30d status = %d; body=%s", dSt, string(dBody))
		}
		items := asSlice(decodeMap(t, dBody)["items"])
		if len(items) != 2 || asString(asMap(items[0])["key"]) != "AI" || asString(asMap(items[1])["key"]) != "Dev" {
			t.Fatalf("unexpected 30d group dimensions sort=rx: %s", string(dBody))
		}
	})

	t.Run("F11_F13_MultiplierWeightsVsProviderReconciliation", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p11-13")

		b := syntheticBatch("site-e2e", "node-p11-13", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(
				map[string]any{"name": "HK-IEPL", "proxy_rx": 1000, "proxy_tx": 0},
				map[string]any{"name": "US-Lite", "proxy_rx": 1000, "proxy_tx": 0},
			),
			withProviders(map[string]any{
				"name": "AirportX", "vehicle_type": "HTTP",
				"upload": 0, "download": 3000, "total": 100000, "expire": 1893456000,
				"proxies": []string{"HK-IEPL", "US-Lite"},
			}),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b); st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		p0St, p0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if p0St != http.StatusOK {
			t.Fatalf("GET /v1/proxy/providers = %d; body=%s", p0St, string(p0))
		}
		provs0 := asSlice(decodeMap(t, p0)["providers"])
		if len(provs0) == 0 {
			t.Fatalf("expected at least 1 provider before rules; body=%s", string(p0))
		}
		pr0 := asMap(provs0[0])
		if !asBool(pr0["drift_alert"]) {
			t.Fatalf("expected drift_alert=true before multiplier rules (2000 vs 3000); body=%s", string(p0))
		}

		// Apply 2.5x and 0.5x -> 1000*2.5 + 1000*0.5 = 3000, matching provider used_bytes=3000.
		if st, body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "HK25", "match_type": "exact", "pattern": "HK-IEPL", "factor": 2.5, "priority": 100, "enabled": true,
		}); st != http.StatusOK && st != http.StatusCreated {
			t.Fatalf("POST HK25 failed: %d %s", st, string(body))
		}
		if st, body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "US05", "match_type": "exact", "pattern": "US-Lite", "factor": 0.5, "priority": 100, "enabled": true,
		}); st != http.StatusOK && st != http.StatusCreated {
			t.Fatalf("POST US05 failed: %d %s", st, string(body))
		}

		_, p1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		provs1 := asSlice(decodeMap(t, p1)["providers"])
		if len(provs1) == 0 {
			t.Fatalf("expected at least 1 provider after rules; body=%s", string(p1))
		}
		pr1 := asMap(provs1[0])
		loc1, _ := asUint64(pr1["local_adjusted"])
		used1, _ := asUint64(pr1["used_bytes"])
		dr1, _ := asFloat64(pr1["drift_ratio"])
		if loc1 != 3000 || used1 != 3000 || dr1 >= 0.01 || asBool(pr1["drift_alert"]) {
			t.Fatalf("expected resolved drift after rules: %s", string(p1))
		}
		_, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			if asString(asMap(it)["kind"]) == "drift" {
				t.Fatalf("drift alert should auto-resolve after rules aligned local_adjusted; alerts=%s", string(aBody))
			}
		}
	})

	t.Run("F14_F16_EmbeddedSPASetupAuthGateAndRateLimit", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)

		sSt, sBody := doJSONRequest(t, nil, http.MethodGet, srv.BaseURL+"/v1/setup", nil)
		if sSt != http.StatusOK || !asBool(decodeMap(t, sBody)["needed"]) {
			t.Fatalf("initial /v1/setup = %d %s", sSt, string(sBody))
		}
		hSt, hHdr, htmlBytes := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/", nil, nil)
		if hSt != http.StatusOK || !strings.Contains(hHdr.Get("Content-Type"), "text/html") {
			t.Fatalf("GET / = %d ct=%q", hSt, hHdr.Get("Content-Type"))
		}
		assetRe := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`)
		matches := assetRe.FindAllStringSubmatch(string(htmlBytes), -1)
		if len(matches) == 0 {
			t.Fatalf("expected Vite /assets/*.js reference in index.html:\n%s", string(htmlBytes))
		}
		jsPath := matches[0][1]

		if uSt, _ := doJSONRequest(t, nil, http.MethodGet, srv.BaseURL+"/v1/overview", nil); uSt != http.StatusUnauthorized {
			t.Fatalf("unauthenticated /v1/overview = %d, want 401", uSt)
		}

		// Setup admin -> verify cookie flags + setup conflict.
		setupRawStatus, setupHdr, _ := doRawRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/setup", []byte(`{"password":"e2e-admin-pass-1"}`), map[string]string{"Content-Type": "application/json"})
		if setupRawStatus != http.StatusOK {
			t.Fatalf("POST /v1/setup = %d", setupRawStatus)
		}
		sc := setupHdr.Get("Set-Cookie")
		if !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Strict") {
			t.Fatalf("unexpected setup Set-Cookie: %q", sc)
		}
		if cSt, _ := doJSONRequest(t, nil, http.MethodPost, srv.BaseURL+"/v1/setup", map[string]string{"password": "another-password"}); cSt != http.StatusConflict {
			t.Fatalf("second setup = %d, want 409", cSt)
		}

		// Lock out 198.51.100.40 via 5 failed logins -> 6th returns 429, while SPA assets remain 200 and 198.51.100.41 succeeds.
		xffLocked := map[string]string{"X-Forwarded-For": "198.51.100.40"}
		for i := 0; i < 5; i++ {
			if st, _ := doJSONRequestWithHeaders(t, nil, http.MethodPost, srv.BaseURL+"/v1/login", map[string]string{"password": "wrong"}, xffLocked); st != http.StatusUnauthorized {
				t.Fatalf("failed login #%d = %d, want 401", i+1, st)
			}
		}
		if st6, _ := doJSONRequestWithHeaders(t, nil, http.MethodPost, srv.BaseURL+"/v1/login", map[string]string{"password": "e2e-admin-pass-1"}, xffLocked); st6 != http.StatusTooManyRequests {
			t.Fatalf("6th login = %d, want 429", st6)
		}
		aSt, _, jsBytes := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+jsPath, nil, xffLocked)
		if aSt != http.StatusOK || len(jsBytes) == 0 || strings.Contains(string(jsBytes), "fonts.googleapis.com") {
			t.Fatalf("SPA JS asset failed or leaked CDN under locked XFF: status=%d len=%d", aSt, len(jsBytes))
		}

		cOther := newCookieClient(t)
		if okSt, _ := doJSONRequestWithHeaders(t, cOther, http.MethodPost, srv.BaseURL+"/v1/login", map[string]string{"password": "e2e-admin-pass-1"}, map[string]string{"X-Forwarded-For": "198.51.100.41"}); okSt != http.StatusOK {
			t.Fatalf("other IP login = %d, want 200", okSt)
		}
		for _, ep := range []string{"/v1/overview", "/v1/proxy", "/v1/watch", "/v1/direct-collectors"} {
			if st, _ := doJSONRequest(t, cOther, http.MethodGet, srv.BaseURL+ep, nil); st != http.StatusOK {
				t.Fatalf("authenticated GET %s = %d, want 200", ep, st)
			}
		}
	})

	t.Run("F1_F16_IdempotentReplayAndDeviceIdentityMerge", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p1-16")

		now := time.Now().UTC()
		b1 := syntheticBatch("site-e2e", "node-p1-16", "boot-1", 1, now.Add(-10*time.Minute),
			withCapabilities("nlbwmon"),
			withDevices(
				map[string]any{"obs_ip": "203.0.113.21", "obs_mac": "02:00:00:00:00:21", "rx_delta": 1000, "tx_delta": 500, "source": "nlbwmon"},
				map[string]any{"obs_ip": "203.0.113.22", "obs_mac": "02:00:00:00:00:22", "rx_delta": 400, "tx_delta": 100, "source": "nlbwmon"},
			),
		)
		if st, r1 := postIngestBatch(t, srv.BaseURL, tok, b1); st != http.StatusOK || asBool(decodeMap(t, r1)["duplicate"]) {
			t.Fatalf("seq=1 ingest = %d %s", st, string(r1))
		}

		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs := asSlice(decodeMap(t, dBody)["devices"])
		if len(devs) != 2 {
			t.Fatalf("expected 2 devices; body=%s", string(dBody))
		}
		var idA, idB string
		for _, raw := range devs {
			d := asMap(raw)
			if asString(d["mac"]) == "02:00:00:00:00:21" {
				idA = asString(d["id"])
			} else if asString(d["mac"]) == "02:00:00:00:00:22" {
				idB = asString(d["id"])
			}
		}
		if idA == "" || idB == "" {
			t.Fatalf("failed to locate devA/devB IDs: %s", string(dBody))
		}

		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/devices/"+idA, map[string]string{"name": "Desktop-Main"}); st != http.StatusOK {
			t.Fatalf("rename devA failed: %d", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+idA+"/merge", map[string]string{"other_id": idB, "reason": "same host"}); st != http.StatusOK {
			t.Fatalf("merge failed: %d", st)
		}

		// Replay exact seq=1 batch -> duplicate=true, no double count (idA keeps its pre-merge 1500).
		if st, rDup := postIngestBatch(t, srv.BaseURL, tok, b1); st != http.StatusOK || !asBool(decodeMap(t, rDup)["duplicate"]) {
			t.Fatalf("replay seq=1 = %d %s, want 200 duplicate=true", st, string(rDup))
		}
		_, dBody1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs1 := asSlice(decodeMap(t, dBody1)["devices"])
		if len(devs1) != 1 {
			t.Fatalf("expected 1 canonical device; body=%s", string(dBody1))
		}
		if tot, _ := asUint64(asMap(devs1[0])["total"]); tot != 1500 {
			t.Fatalf("canonical total after replay = %d, want 1500; body=%s", tot, string(dBody1))
		}

		// Send seq=2 for devB MAC -> ResolveDevice attributes new observations to canonical devA (1500 + 300 = 1800).
		b2 := syntheticBatch("site-e2e", "node-p1-16", "boot-1", 2, now.Add(-5*time.Minute),
			withCapabilities("nlbwmon"),
			withDevices(map[string]any{"obs_ip": "203.0.113.22", "obs_mac": "02:00:00:00:00:22", "rx_delta": 200, "tx_delta": 100, "source": "nlbwmon"}),
		)
		if st, _ := postIngestBatch(t, srv.BaseURL, tok, b2); st != http.StatusOK {
			t.Fatalf("seq=2 ingest = %d", st)
		}
		_, dBody2 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs2 := asSlice(decodeMap(t, dBody2)["devices"])
		if len(devs2) != 1 {
			t.Fatalf("expected 1 canonical device after seq=2; body=%s", string(dBody2))
		}
		if tot, _ := asUint64(asMap(devs2[0])["total"]); tot != 1800 {
			t.Fatalf("canonical total after seq=2 = %d, want 1800; body=%s", tot, string(dBody2))
		}

		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+idB+"/unmerge", map[string]any{}); st != http.StatusOK {
			t.Fatalf("unmerge failed: %d", st)
		}
		_, dBody3 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		if len(asSlice(decodeMap(t, dBody3)["devices"])) != 2 {
			t.Fatalf("expected 2 devices after unmerge; body=%s", string(dBody3))
		}
		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"]); tot != 2300 {
			t.Fatalf("overview total = %d, want 2300; body=%s", tot, string(ovBody))
		}
	})

	t.Run("F8_F17_DirectCollectorSecretMaskingReadOnlyAudit", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		const secGood = "dc-super-secret-99"
		const secBad = "bad-secret-88"
		const urlToken = "url-leak-guard"
		mihomo := newMockMihomo(t, secGood)
		mihomo.SetProxies(map[string]MockMihomoProxy{"HK-01": {Name: "HK-01", Type: "Shadowsocks", Alive: true}})
		mihomo.SetProviders(map[string]MockMihomoProvider{"SubA": {Name: "SubA", Type: "Proxy", VehicleType: "HTTP"}})

		c1St, c1Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "MaskedGood", "site_id": "site-e2e", "node_id": "dc-p8-17",
			"url": mihomo.URL() + "?token=" + urlToken, "secret": secGood, "enabled": true,
		})
		if c1St != http.StatusOK && c1St != http.StatusCreated {
			t.Fatalf("POST MaskedGood = %d %s", c1St, string(c1Body))
		}
		id1 := extractEntityID(t, c1Body, "collector")

		closed := freeAddr(t)
		c2St, c2Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "MaskedClosed", "site_id": "site-e2e", "node_id": "dc-p8-17-bad",
			"url": "http://" + closed + "?token=" + urlToken, "secret": secBad, "enabled": false,
		})
		if c2St != http.StatusOK && c2St != http.StatusCreated {
			t.Fatalf("POST MaskedClosed = %d %s", c2St, string(c2Body))
		}
		id2 := extractEntityID(t, c2Body, "collector")

		t1St, t1Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+id1+"/test", map[string]any{})
		if t1St != http.StatusOK {
			t.Fatalf("test MaskedGood = %d %s", t1St, string(t1Body))
		}
		_, t2Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+id2+"/test", map[string]any{})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			seen := map[string]bool{}
			for _, rec := range mihomo.Requests() {
				seen[rec.Path] = true
			}
			return mihomo.ConnRequestCount() >= 1 && seen["/proxies"] && seen["/providers/proxies"]
		})

		lSt, listBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/direct-collectors", nil)
		if lSt != http.StatusOK {
			t.Fatalf("GET /v1/direct-collectors = %d %s", lSt, string(listBody))
		}
		for _, it := range asSlice(decodeMap(t, listBody)["collectors"]) {
			c := asMap(it)
			if !asBool(c["has_secret"]) {
				t.Fatalf("expected has_secret=true on %v", c)
			}
		}
		combined := string(listBody) + "\n" + string(t1Body) + "\n" + string(t2Body) + "\n" + srv.Logs()
		for _, secret := range []string{secGood, secBad, urlToken} {
			if strings.Contains(combined, secret) {
				t.Fatalf("secret %q leaked in API responses or server logs", secret)
			}
		}
		if nonGet := mihomo.NonGetRequests(); len(nonGet) > 0 {
			t.Fatalf("direct collector issued non-GET requests: %+v", nonGet)
		}
	})

	t.Run("F15_F17_ServerCLIFlagsAndVMPrivacyLabelWhitelist", func(t *testing.T) {
		vm := newMockVM(t)
		dbPath := filepath.Join(t.TempDir(), "cli-vm.db")
		srv := startServer(t, ServerOpts{
			DBPath:            dbPath,
			Demo:              true,
			BootstrapPassword: "e2e-cli-vm-pass",
			MetricsURL:        vm.URL(),
		})
		client := newCookieClient(t)
		if lSt, _ := loginAdmin(t, client, srv.BaseURL, "e2e-cli-vm-pass"); lSt != http.StatusOK {
			t.Fatalf("bootstrap login = %d, want 200", lSt)
		}
		_, ov0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if batches, _ := asUint64(decodeMap(t, ov0)["ingest_batches"]); batches != 24 {
			t.Fatalf("demo ingest_batches = %d, want 24", batches)
		}

		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p15-17")
		b := syntheticBatch("site-e2e", "node-p15-17", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface", "mihomo"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1024, "tx_delta": 512}),
			withDevices(map[string]any{
				"obs_ip": "203.0.113.99", "obs_mac": "02:00:00:00:00:99",
				"rx_delta": 300, "tx_delta": 100, "source": "mihomo", "outbound": "SecretNodeName",
			}),
			withProxyOutbounds(map[string]any{"name": "SecretNodeName", "proxy_rx": 300, "proxy_tx": 100}),
			withDimensions(map[string]any{"type": "domain", "key": "private.domain.test", "rx_delta": 300, "tx_delta": 100}),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b); st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		reqs := vm.WaitForSamples(t, 5*time.Second, 1)
		allowed := map[string]bool{"site_id": true, "node_id": true, "device_id": true, "class": true, "direction": true}
		labelRe := regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="[^"]*"`)
		forbidden := []string{"private.domain.test", "203.0.113.99", "02:00:00:00:00:99", "SecretNodeName", "domain", "destination", "url", "conn_id", "source_ip", tok}
		for _, r := range reqs {
			for _, bad := range forbidden {
				if strings.Contains(r.RawBody, bad) {
					t.Fatalf("forbidden substring %q leaked in VM payload: %s", bad, r.RawBody)
				}
			}
			for _, line := range r.Lines {
				for _, m := range labelRe.FindAllStringSubmatch(line, -1) {
					if !allowed[m[1]] {
						t.Fatalf("non-whitelisted VM label %q in line %q", m[1], line)
					}
				}
			}
		}
	})

	t.Run("F2_F8_DirectCollectorDomainToggleAndWatchDomainsIsolation", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokRemote := enrollNode(t, client, srv.BaseURL, "site-e2e", "remote-p2-8")

		if wSt, wBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":     "SharedWatch",
			"matchers": map[string]any{"domain": map[string]any{"type": "exact", "values": []string{"watched.shared.test"}}},
		}); wSt != http.StatusOK && wSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch status = %d; body=%s", wSt, string(wBody))
		}

		mRemote := newMockMihomo(t, "s")
		mRemote.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		mDirect := newMockMihomo(t, "s")
		mDirect.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "remote-p2-8", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true}, Mihomo: &AgentMihomoConfig{URL: mRemote.URL()},
		}, tokRemote, "s")

		dcSt, dcBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "DirectP28", "site_id": "site-e2e", "node_id": "dc-p2-8",
			"url": mDirect.URL(), "secret": "s", "enabled": true, "collect_domains": false,
		})
		if dcSt != http.StatusOK && dcSt != http.StatusCreated {
			t.Fatalf("POST direct collector = %d %s", dcSt, string(dcBody))
		}
		dcID := extractEntityID(t, dcBody, "collector")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			return mRemote.WSConnectCount() >= 1 && mDirect.WSConnectCount() >= 1
		})

		// Enable collect_domains ONLY on direct collector dc-p2-8.
		if uSt, uBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/direct-collectors/"+dcID, map[string]any{
			"name": "DirectP28", "site_id": "site-e2e", "node_id": "dc-p2-8",
			"url": mDirect.URL(), "enabled": true, "collect_domains": true,
		}); uSt != http.StatusOK {
			t.Fatalf("PUT direct collector = %d %s", uSt, string(uBody))
		}
		// Wait deterministically until direct collector runner applies collect_domains=true.
		warmupBytes := uint64(0)
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			warmupBytes++
			_ = mDirect.PushWSFrame([]MockMihomoConn{
				{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
				{ID: "warmup", Upload: warmupBytes, Download: 0, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "warmup.probe.test"}},
			})
			_, bw := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain&node=dc-p2-8", nil)
			wTot, _ := asUint64(asMap(decodeMap(t, bw)["totals"])["probe.test"])
			return wTot > 0
		})

		_ = mDirect.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "warmup", Upload: warmupBytes, Download: 0, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "warmup.probe.test"}},
			{ID: "d1", Upload: 100, Download: 200, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "dc.only.example"}},
		})
		_ = mRemote.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "r1", Upload: 100, Download: 200, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "remote.unwatched.example"}},
			{ID: "r2", Upload: 150, Download: 250, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "api.watched.shared.test"}},
		})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, bd := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain&node=dc-p2-8", nil)
			_, br := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain&node=remote-p2-8", nil)
			dTot, _ := asUint64(asMap(decodeMap(t, bd)["totals"])["only.example"])
			rTot, _ := asUint64(asMap(decodeMap(t, br)["totals"])["watched.shared.test"])
			return dTot == 300 && rTot == 400
		})
		_, brFinal := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain&node=remote-p2-8", nil)
		if strings.Contains(string(brFinal), "unwatched.example") {
			t.Fatalf("remote node inherited direct collector collect_domains=true: %s", string(brFinal))
		}
	})

	t.Run("F3_F5_ProcessBasenameAndDomainsOrthogonalToChains", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p3-5")

		if cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-p3-5/config", map[string]any{
			"collect_domains": true,
		}); cSt != http.StatusOK {
			t.Fatalf("PUT collect_domains=true failed: %d %s", cSt, string(cBody))
		}

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-p3-5", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true}, Mihomo: &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			batches, _ := asUint64(decodeMap(t, b)["ingest_batches"])
			return mihomo.WSConnectCount() >= 1 && batches >= 1
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{
				ID: "c1", Upload: 100, Download: 300, Chains: []string{"HK-01", "AI"},
				Metadata: MockMihomoMetadata{ProcessPath: "/usr/local/bin/cursor-agent", Host: "api.openai.example"},
			},
			{
				ID: "c2", Upload: 200, Download: 400, Chains: []string{"HK-01", "Streaming"},
				Metadata: MockMihomoMetadata{ProcessPath: `C:\Apps\Media\player.exe`, SniffHost: "cdn.video.example"},
			},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 1000
		})

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		obs := asSlice(decodeMap(t, pBody)["outbounds"])
		if len(obs) != 1 || asString(asMap(obs[0])["name"]) != "HK-01" {
			t.Fatalf("expected single outbound HK-01; body=%s", string(pBody))
		}
		if hkRaw, _ := asUint64(asMap(obs[0])["raw"]); hkRaw != 1000 {
			t.Fatalf("HK-01 raw = %d, want 1000", hkRaw)
		}

		_, gB := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		gT := asMap(decodeMap(t, gB)["totals"])
		if ai, _ := asUint64(gT["AI"]); ai != 400 {
			t.Fatalf("group AI = %d, want 400", ai)
		}
		if st, _ := asUint64(gT["Streaming"]); st != 600 {
			t.Fatalf("group Streaming = %d, want 600", st)
		}

		_, prB := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		if strings.Contains(string(prB), "/usr/local/bin") || strings.Contains(string(prB), `C:\Apps`) {
			t.Fatalf("process directory path leaked: %s", string(prB))
		}
		prT := asMap(decodeMap(t, prB)["totals"])
		if ca, _ := asUint64(prT["cursor-agent"]); ca != 400 {
			t.Fatalf("process cursor-agent = %d, want 400", ca)
		}
		if pl, _ := asUint64(prT["player.exe"]); pl != 600 {
			t.Fatalf("process player.exe = %d, want 600", pl)
		}

		_, dmB := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		dmT := asMap(decodeMap(t, dmB)["totals"])
		if oa, _ := asUint64(dmT["openai.example"]); oa != 400 {
			t.Fatalf("domain openai.example = %d, want 400", oa)
		}
		if vd, _ := asUint64(dmT["video.example"]); vd != 600 {
			t.Fatalf("domain video.example = %d, want 600", vd)
		}
	})

	t.Run("F4_F6_WSStreamingWithProxiesProvidersSyncAnd404Resilience", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p4-6")

		const sec = "ws-sync-sec"
		mihomo := newMockMihomo(t, sec)
		mihomo.SetProviderStatus(http.StatusNotFound)
		mihomo.SetProxies(map[string]MockMihomoProxy{
			"HK-01": {Name: "HK-01", Type: "Shadowsocks", Alive: true},
			"JP-x2": {Name: "JP-x2", Type: "VMess", Alive: true},
		})
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})

		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-p4-6", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true}, Mihomo: &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, sec)

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			seen := map[string]bool{}
			for _, r := range mihomo.Requests() {
				if r.Auth == "Bearer "+sec {
					seen[r.Path] = true
				}
			}
			return mihomo.WSConnectCount() >= 1 && seen["/proxies"] && seen["/providers/proxies"]
		})

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 300, Download: 700, Chains: []string{"HK-01", "Proxy"}},
		})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, ov := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, ov)["bytes"])["proxy_raw"])
			return pr == 1000
		})

		pSt, pBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/preview", map[string]any{})
		if pSt != http.StatusOK {
			t.Fatalf("preview status = %d; body=%s", pSt, string(pBody))
		}
		if !strings.Contains(string(pBody), "HK-01") || !strings.Contains(string(pBody), "JP-x2") {
			t.Fatalf("preview missing synced proxies: %s", string(pBody))
		}

		_, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			al := asMap(it)
			kind := asString(al["kind"])
			if (kind == "reset" && asString(al["message"]) != "collector gap detected: reboot") || kind == "drift" {
				t.Fatalf("unexpected alert on /providers/proxies 404: %s", string(aBody))
			}
		}
	})

	t.Run("F5_F9_ProcessDomainTopNWithRxTxAndSortLimit", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p5-9")

		if cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-p5-9/config", map[string]any{
			"collect_domains": true,
		}); cSt != http.StatusOK {
			t.Fatalf("PUT collect_domains=true = %d %s", cSt, string(cBody))
		}

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-p5-9", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true}, Mihomo: &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			batches, _ := asUint64(decodeMap(t, b)["ingest_batches"])
			return mihomo.WSConnectCount() >= 1 && batches >= 1
		})

		conns := []MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "pa", Upload: 100, Download: 9000, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Process: "procA"}},
			{ID: "pb", Upload: 8000, Download: 200, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Process: "procB"}},
		}
		for i := 1; i <= 55; i++ {
			conns = append(conns, MockMihomoConn{
				ID:       fmt.Sprintf("d-%d", i),
				Upload:   uint64(100 - i),
				Download: uint64(2000 - i*10),
				Chains:   []string{"HK-01", "Proxy"},
				Metadata: MockMihomoMetadata{Host: fmt.Sprintf("sub.dom%02d.test", i)},
			})
		}
		_ = mihomo.PushWSFrame(conns)

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=domain&limit=100", nil)
			if st != http.StatusOK {
				return false
			}
			items := asSlice(decodeMap(t, b)["items"])
			if len(items) == 0 || len(items) > 51 {
				return false
			}
			for _, raw := range items {
				if asString(asMap(raw)["key"]) == "other" {
					return true
				}
			}
			return false
		})

		// Verify limit=10 & sort=total descending + share sum ~ 1.0 on full list.
		lSt, lBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=domain&limit=10&sort=total", nil)
		if lSt != http.StatusOK {
			t.Fatalf("dimensions limit=10 = %d %s", lSt, string(lBody))
		}
		lItems := asSlice(decodeMap(t, lBody)["items"])
		if len(lItems) != 10 {
			t.Fatalf("expected 10 items for limit=10, got %d", len(lItems))
		}
		var prevTot uint64 = ^uint64(0)
		for _, raw := range lItems {
			tot, _ := asUint64(asMap(raw)["total"])
			if tot > prevTot {
				t.Fatalf("limit=10 sort=total not descending: %s", string(lBody))
			}
			prevTot = tot
		}
		_, fullDom := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=domain&limit=100", nil)
		var shareSum float64
		for _, raw := range asSlice(decodeMap(t, fullDom)["items"]) {
			s, _ := asFloat64(asMap(raw)["share"])
			shareSum += s
		}
		if shareSum < 0.99 || shareSum > 1.01 {
			t.Fatalf("domain share sum = %v, want ~1.0", shareSum)
		}

		// Verify process sort=rx vs sort=tx.
		_, rxBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=process&sort=rx", nil)
		rxItems := asSlice(decodeMap(t, rxBody)["items"])
		if len(rxItems) != 2 || asString(asMap(rxItems[0])["key"]) != "procA" {
			t.Fatalf("sort=rx expected procA first: %s", string(rxBody))
		}
		aRx, _ := asUint64(asMap(rxItems[0])["rx"])
		aTx, _ := asUint64(asMap(rxItems[0])["tx"])
		if aRx != 9000 || aTx != 100 {
			t.Fatalf("procA rx/tx = %d/%d, want 9000/100", aRx, aTx)
		}

		_, txBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=process&sort=tx", nil)
		txItems := asSlice(decodeMap(t, txBody)["items"])
		if len(txItems) != 2 || asString(asMap(txItems[0])["key"]) != "procB" {
			t.Fatalf("sort=tx expected procB first: %s", string(txBody))
		}
		bRx, _ := asUint64(asMap(txItems[0])["rx"])
		bTx, _ := asUint64(asMap(txItems[0])["tx"])
		if bRx != 200 || bTx != 8000 {
			t.Fatalf("procB rx/tx = %d/%d, want 200/8000", bRx, bTx)
		}
	})

	t.Run("F7_F12_VirtualNICWatchItemWhileNodeTotalExcluded", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p7-12")

		if wSt, wBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":        "Mesh VPN",
			"matchers":    map[string]any{"iface": map[string]any{"type": "regex", "values": []string{"^(wg|tailscale)"}}},
			"quota_value": 2, "quota_unit": "MB", "pinned": true,
		}); wSt != http.StatusOK && wSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch = %d %s", wSt, string(wBody))
		}

		b := syntheticBatch("site-e2e", "node-p7-12", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(
				map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 500},
				map[string]any{"name": "wg0", "rx_delta": 1048576, "tx_delta": 524288},
				map[string]any{"name": "tailscale0", "rx_delta": 524288, "tx_delta": 0},
				map[string]any{"name": "docker0", "rx_delta": 99999, "tx_delta": 99999},
			),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b); st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		ov := decodeMap(t, ovBody)
		if tot, _ := asUint64(asMap(ov["bytes"])["total"]); tot != 1500 {
			t.Fatalf("overview bytes.total = %d, want 1500 (eth0 only); body=%s", tot, string(ovBody))
		}
		if !strings.Contains(fmt.Sprintf("%v", ov["pinned_watch"]), "Mesh VPN") {
			t.Fatalf("Mesh VPN missing from overview.pinned_watch; body=%s", string(ovBody))
		}

		_, wBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/watch", nil)
		items := asSlice(decodeMap(t, wBody)["items"])
		if len(items) != 1 {
			t.Fatalf("expected 1 watch item; body=%s", string(wBody))
		}
		it := asMap(items[0])
		rx, _ := asUint64(it["rx"])
		tx, _ := asUint64(it["tx"])
		adj, _ := asUint64(it["adjusted"])
		ratio, _ := asFloat64(it["used_ratio"])
		if rx != 1572864 || tx != 524288 || adj != 2097152 || ratio < 1.0 {
			t.Fatalf("Mesh VPN unexpected stats: %s", string(wBody))
		}
	})

	t.Run("F10_F11_HistoricalDailyRollupWithRetroactiveMultiplierRules", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "p10-11.db")
		srv := startServer(t, ServerOpts{DBPath: dbPath})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p10-11")

		now := time.Now().UTC()
		bOld := syntheticBatch("site-e2e", "node-p10-11", "boot-1", 1, now.Add(-20*24*time.Hour),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "HK-IEPL", "proxy_rx": 1000, "proxy_tx": 0}),
		)
		bRec := syntheticBatch("site-e2e", "node-p10-11", "boot-1", 2, now.Add(-2*time.Hour),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "HK-IEPL", "proxy_rx": 500, "proxy_tx": 0}),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tok, bOld); st != http.StatusOK {
			t.Fatalf("ingest bOld = %d %s", st, string(body))
		}
		if st, body := postIngestBatch(t, srv.BaseURL, tok, bRec); st != http.StatusOK {
			t.Fatalf("ingest bRec = %d %s", st, string(body))
		}
		srv.Stop()

		srv2 := startServer(t, ServerOpts{DBPath: dbPath})
		c2 := newCookieClient(t)
		if lSt, _ := loginAdmin(t, c2, srv2.BaseURL, "e2e-admin-pass-1"); lSt != http.StatusOK {
			t.Fatalf("re-login failed: %d", lSt)
		}

		ex0St, ex0Body := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/proxy/rules/export", nil)
		if ex0St != http.StatusOK || len(asSlice(decodeMap(t, ex0Body)["rules"])) != 0 {
			t.Fatalf("initial export = %d %s", ex0St, string(ex0Body))
		}

		imSt, imBody := doJSONRequest(t, c2, http.MethodPost, srv2.BaseURL+"/v1/proxy/rules/import", map[string]any{
			"mode": "replace",
			"rules": []map[string]any{
				{"name": "IEPL2x", "match_type": "keyword", "pattern": "IEPL", "factor": 2.0, "priority": 100, "enabled": true},
			},
		})
		if imSt != http.StatusOK {
			t.Fatalf("import failed: %d %s", imSt, string(imBody))
		}

		_, tr30 := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/traffic?group=outbound&class=proxy_adjusted&range=30d", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, tr30)["totals"])["HK-IEPL"]); tot != 3000 {
			t.Fatalf("30d daily rollup proxy_adjusted = %d, want 3000; body=%s", tot, string(tr30))
		}
		_, tr24 := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/traffic?group=outbound&class=proxy_adjusted&range=24h", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, tr24)["totals"])["HK-IEPL"]); tot != 1000 {
			t.Fatalf("24h sample proxy_adjusted = %d, want 1000; body=%s", tot, string(tr24))
		}

		ex1St, ex1Body := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/proxy/rules/export", nil)
		if ex1St != http.StatusOK {
			t.Fatalf("export rules failed: %d", ex1St)
		}
		_, lsBody := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/proxy/rules", nil)
		for _, r := range asSlice(decodeMap(t, lsBody)["rules"]) {
			rid := fmt.Sprintf("%v", asMap(r)["id"])
			if dSt, _ := doJSONRequest(t, c2, http.MethodDelete, srv2.BaseURL+"/v1/proxy/rules/"+rid, nil); dSt != http.StatusOK {
				t.Fatalf("delete rule %s failed: %d", rid, dSt)
			}
		}
		_, trRev := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/traffic?group=outbound&class=proxy_adjusted&range=30d", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, trRev)["totals"])["HK-IEPL"]); tot != 1500 {
			t.Fatalf("30d after delete = %d, want 1500", tot)
		}
		if reSt, _, reBody := doRawRequest(t, c2, http.MethodPost, srv2.BaseURL+"/v1/proxy/rules/import", ex1Body, map[string]string{"Content-Type": "application/json"}); reSt != http.StatusOK {
			t.Fatalf("re-import exported rules failed: %d %s", reSt, string(reBody))
		}
		_, trRest := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/traffic?group=outbound&class=proxy_adjusted&range=30d", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, trRest)["totals"])["HK-IEPL"]); tot != 3000 {
			t.Fatalf("30d after re-import = %d, want 3000", tot)
		}
	})

	t.Run("F12_F16_WatchlistOnMergedDeviceAndNodeRevokePersistence", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-p12-16")

		b := syntheticBatch("site-e2e", "node-p12-16", "boot-1", 1, time.Now().UTC(),
			withCapabilities("nlbwmon"),
			withDevices(
				map[string]any{"obs_ip": "203.0.113.31", "obs_mac": "02:00:00:00:00:31", "rx_delta": 500, "tx_delta": 100, "source": "nlbwmon"},
				map[string]any{"obs_ip": "203.0.113.32", "obs_mac": "02:00:00:00:00:32", "rx_delta": 300, "tx_delta": 100, "source": "nlbwmon"},
			),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b); st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs := asSlice(decodeMap(t, dBody)["devices"])
		if len(devs) < 2 {
			t.Fatalf("expected at least 2 devices before merge; body=%s", string(dBody))
		}
		id1 := asString(asMap(devs[0])["id"])
		id2 := asString(asMap(devs[1])["id"])
		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/devices/"+id1, map[string]string{"name": "NAS"}); st != http.StatusOK {
			t.Fatalf("rename failed: %d", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+id1+"/merge", map[string]string{"other_id": id2}); st != http.StatusOK {
			t.Fatalf("merge failed: %d", st)
		}

		if wSt, wBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":     "NodeWatch",
			"matchers": map[string]any{"node": map[string]any{"type": "exact", "values": []string{"node-p12-16"}}},
		}); wSt != http.StatusOK && wSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch = %d %s", wSt, string(wBody))
		}

		if rSt, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/nodes/node-p12-16/revoke", nil); rSt != http.StatusOK {
			t.Fatalf("revoke failed: %d", rSt)
		}
		if gSt, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-p12-16", nil); gSt != http.StatusNotFound {
			t.Fatalf("revoked node GET = %d, want 404", gSt)
		}
		if iSt, _ := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-p12-16", "boot-1", 2, time.Now().UTC())); iSt != http.StatusUnauthorized {
			t.Fatalf("revoked node ingest = %d, want 401", iSt)
		}

		_, wBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/watch", nil)
		items := asSlice(decodeMap(t, wBody)["items"])
		if len(items) != 1 {
			t.Fatalf("expected 1 watch item after revoke; body=%s", string(wBody))
		}
		rx, _ := asUint64(asMap(items[0])["rx"])
		tx, _ := asUint64(asMap(items[0])["tx"])
		if rx+tx != 1000 {
			t.Fatalf("watch total after revoke = %d, want 1000; body=%s", rx+tx, string(wBody))
		}

		bSt, _, bBytes := doRawRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/backup", nil, nil)
		if bSt != http.StatusOK || !strings.HasPrefix(string(bBytes), "SQLite format 3\x00") {
			t.Fatalf("backup failed: %d len=%d", bSt, len(bBytes))
		}
	})

	t.Run("F14_F15_DemoModeCLIAndEmbeddedSPAAssets", func(t *testing.T) {
		srv := startServer(t, ServerOpts{Demo: true, BootstrapPassword: "e2e-demo-spa-pass"})
		hSt, _, htmlBytes := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/", nil, nil)
		if hSt != http.StatusOK {
			t.Fatalf("GET / = %d", hSt)
		}
		html := string(htmlBytes)
		if strings.Contains(html, "fonts.googleapis.com") || strings.Contains(html, "fonts.gstatic.com") {
			t.Fatalf("index.html contains Google Fonts reference")
		}

		assetRe := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
		matches := assetRe.FindAllStringSubmatch(html, -1)
		if len(matches) == 0 {
			t.Fatalf("index.html missing Vite /assets/* bundle:\n%s", html)
		}
		var jsBundle strings.Builder
		for _, m := range matches {
			st, hdr, body := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+m[1], nil, nil)
			if st != http.StatusOK || len(body) == 0 {
				t.Fatalf("asset %s failed: %d len=%d", m[1], st, len(body))
			}
			if strings.HasSuffix(m[1], ".js") {
				if !strings.Contains(hdr.Get("Content-Type"), "javascript") {
					t.Fatalf("js MIME = %q", hdr.Get("Content-Type"))
				}
				jsBundle.Write(body)
			} else if strings.HasSuffix(m[1], ".css") && !strings.Contains(hdr.Get("Content-Type"), "text/css") {
				t.Fatalf("css MIME = %q", hdr.Get("Content-Type"))
			}
		}
		jsText := jsBundle.String()
		for _, pair := range [][]string{{"Overview", "概览"}, {"Proxy", "代理"}, {"Watchlist", "关注"}, {"Apps", "应用"}, {"Devices", "设备"}, {"Nodes", "节点"}, {"Alerts", "告警"}} {
			if !strings.Contains(jsText, pair[0]) && !strings.Contains(jsText, pair[1]) {
				t.Fatalf("SPA JS bundle missing view %v", pair)
			}
		}

		client := newCookieClient(t)
		if lSt, _ := loginAdmin(t, client, srv.BaseURL, "e2e-demo-spa-pass"); lSt != http.StatusOK {
			t.Fatalf("demo login = %d, want 200", lSt)
		}
		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		ov := decodeMap(t, ovBody)
		if batches, _ := asUint64(ov["ingest_batches"]); batches != 24 {
			t.Fatalf("demo ingest_batches = %d, want 24", batches)
		}
		for _, ep := range []struct{ path, key string }{
			{"/v1/devices", "devices"},
			{"/v1/nodes", "nodes"},
			{"/v1/proxy", "outbounds"},
			{"/v1/alerts", "alerts"},
		} {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+ep.path, nil)
			if st != http.StatusOK || strings.Contains(string(b), fmt.Sprintf(`"%s":null`, ep.key)) {
				t.Fatalf("GET %s = %d (null check) body=%s", ep.path, st, string(b))
			}
		}
	})
}
