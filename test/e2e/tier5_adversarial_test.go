//go:build e2e

package e2e

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestTier5_Adversarial exercises adversarial edge cases, regression boundaries,
// and state transitions uncovered during Tier 5 white-box coverage hardening.
func TestTier5_Adversarial(t *testing.T) {
	t.Run("DirectCollectorNoSecretEditAndRedactedURLPreservation", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		var probeTokenHits atomic.Int32
		mockMihomo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("token") != "syn-url-secret-77" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			probeTokenHits.Add(1)
			switch r.URL.Path {
			case "/version":
				_, _ = w.Write([]byte(`{"version":"v1.18.1-syn","meta":true}`))
			case "/proxies":
				_, _ = w.Write([]byte(`{"proxies":{"HK-01":{"name":"HK-01","type":"Shadowsocks"}}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer mockMihomo.Close()

		// 1. Secret-less direct collector with URL-embedded token.
		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name":    "NoSecretDC",
			"site_id": "site-t5",
			"node_id": "dc-t5-nosec",
			"url":     mockMihomo.URL + "?token=syn-url-secret-77",
			"enabled": false,
		})
		if cSt != http.StatusCreated && cSt != http.StatusOK {
			t.Fatalf("POST secret-less direct collector = %d %s", cSt, string(cBody))
		}
		dcID := extractEntityID(t, cBody, "collector")
		redactedURL := asString(decodeMap(t, cBody)["url"])
		if !strings.Contains(redactedURL, "[redacted]") || strings.Contains(string(cBody), "syn-url-secret-77") {
			t.Fatalf("expected redacted URL without raw token; body=%s", string(cBody))
		}

		// 2. PUT omitting secret and url on secret-less collector must return 200 (not 503).
		u1St, u1Body := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/direct-collectors/"+dcID, map[string]any{
			"name": "NoSecretRenamed",
		})
		if u1St != http.StatusOK {
			t.Fatalf("PUT rename on secret-less collector = %d %s, want 200", u1St, string(u1Body))
		}

		// 3. PUT sending unchanged redacted URL preserves underlying URL token.
		u2St, u2Body := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/direct-collectors/"+dcID, map[string]any{
			"name": "NoSecretWithRedactedURL",
			"url":  redactedURL,
		})
		if u2St != http.StatusOK {
			t.Fatalf("PUT with unchanged redacted URL = %d %s, want 200", u2St, string(u2Body))
		}

		// Verify saved test probe still authenticates with syn-url-secret-77.
		pSt, pBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+dcID+"/test", map[string]any{})
		if pSt != http.StatusOK || !asBool(decodeMap(t, pBody)["ok"]) || probeTokenHits.Load() == 0 {
			t.Fatalf("POST /{id}/test failed after redacted URL edit: status=%d hits=%d body=%s", pSt, probeTokenHits.Load(), string(pBody))
		}

		// 4. PUT with a different URL containing literal [redacted] must return 400.
		badSt, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/direct-collectors/"+dcID, map[string]any{
			"url": "http://127.0.0.1:9999?token=[redacted]",
		})
		if badSt != http.StatusBadRequest {
			t.Fatalf("PUT with tampered [redacted] URL = %d, want 400", badSt)
		}

		// 5. Collector with bearer secret -> clear_secret: true -> subsequent edit must succeed (200).
		c2St, c2Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name":    "ClearSecDC",
			"site_id": "site-t5",
			"node_id": "dc-t5-clear",
			"url":     mockMihomo.URL,
			"secret":  "bearer-secret-77",
			"enabled": false,
		})
		if c2St != http.StatusCreated && c2St != http.StatusOK {
			t.Fatalf("POST with-secret collector = %d %s", c2St, string(c2Body))
		}
		dc2ID := extractEntityID(t, c2Body, "collector")
		if clrSt, clrBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/direct-collectors/"+dc2ID, map[string]any{
			"clear_secret": true,
		}); clrSt != http.StatusOK || asBool(decodeMap(t, clrBody)["has_secret"]) {
			t.Fatalf("clear_secret failed: %d %s", clrSt, string(clrBody))
		}
		if edSt, edBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/direct-collectors/"+dc2ID, map[string]any{
			"name": "AfterClearEdit",
		}); edSt != http.StatusOK {
			t.Fatalf("edit after clear_secret = %d %s, want 200", edSt, string(edBody))
		}
	})

	t.Run("DirectCollectorTestByIDClearSecretWithRedactedURL", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		var tokenHits atomic.Int32
		var authHeaderHits atomic.Int32
		mockMihomo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("token") != "syn-url-tok-88" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			tokenHits.Add(1)
			if r.Header.Get("Authorization") != "" {
				authHeaderHits.Add(1)
			}
			switch r.URL.Path {
			case "/version":
				_, _ = w.Write([]byte(`{"version":"v1.18.1-syn","meta":true}`))
			case "/proxies":
				_, _ = w.Write([]byte(`{"proxies":{"HK-01":{"name":"HK-01","type":"Shadowsocks"}}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer mockMihomo.Close()

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name":    "URLTokenAndBearerDC",
			"site_id": "site-t5",
			"node_id": "dc-t5-clearprobe",
			"url":     mockMihomo.URL + "?token=syn-url-tok-88",
			"secret":  "syn-bearer-88",
			"enabled": false,
		})
		if cSt != http.StatusCreated && cSt != http.StatusOK {
			t.Fatalf("POST direct collector = %d %s", cSt, string(cBody))
		}
		dcID := extractEntityID(t, cBody, "collector")
		redactedURL := asString(decodeMap(t, cBody)["url"])
		if !strings.Contains(redactedURL, "[redacted]") {
			t.Fatalf("expected redacted URL in create response, got %q", redactedURL)
		}

		// POST /{id}/test with {"clear_secret":true} must succeed (200), send stored URL token, and omit Authorization header.
		pSt, pBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+dcID+"/test", map[string]any{
			"clear_secret": true,
		})
		if pSt != http.StatusOK || !asBool(decodeMap(t, pBody)["ok"]) {
			t.Fatalf("POST /{id}/test with clear_secret=true = %d %s", pSt, string(pBody))
		}
		if tokenHits.Load() == 0 {
			t.Fatalf("expected stored URL token to be sent")
		}
		if authHeaderHits.Load() != 0 {
			t.Fatalf("expected Authorization header to be omitted when clear_secret=true, got %d hits", authHeaderHits.Load())
		}

		// Passing unchanged redacted URL + clear_secret=true must also succeed without 400 or Authorization header.
		tokenHits.Store(0)
		authHeaderHits.Store(0)
		p2St, p2Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+dcID+"/test", map[string]any{
			"url":          redactedURL,
			"clear_secret": true,
		})
		if p2St != http.StatusOK || !asBool(decodeMap(t, p2Body)["ok"]) {
			t.Fatalf("POST /{id}/test with redacted URL + clear_secret=true = %d %s", p2St, string(p2Body))
		}
		if tokenHits.Load() == 0 || authHeaderHits.Load() != 0 {
			t.Fatalf("expected stored URL token and omitted Authorization header, tokenHits=%d authHits=%d", tokenHits.Load(), authHeaderHits.Load())
		}
	})

	t.Run("DeviceSelfMergeCyclePreventionAndUnmerge404", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-t5", "node-t5-merge")

		b := syntheticBatch("site-t5", "node-t5-merge", "boot-1", 1, time.Now().UTC(),
			withCapabilities("nlbwmon"),
			withDevices(
				map[string]any{"obs_ip": "203.0.113.41", "obs_mac": "02:00:00:00:00:41", "rx_delta": 400, "tx_delta": 100, "source": "nlbwmon"},
			),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b); st != http.StatusOK {
			t.Fatalf("initial ingest = %d %s", st, string(body))
		}

		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs := asSlice(decodeMap(t, dBody)["devices"])
		if len(devs) == 0 {
			t.Fatalf("expected 1 device; body=%s", string(dBody))
		}
		devID := asString(asMap(devs[0])["id"])

		// Self-merge must be rejected with 400 Bad Request (no cycle created).
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+devID+"/merge", map[string]string{"other_id": devID}); st != http.StatusBadRequest {
			t.Fatalf("self-merge status = %d, want 400", st)
		}

		// Subsequent ingest for the same device must continue succeeding (200, not 503).
		b["sequence"] = 2
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b); st != http.StatusOK {
			t.Fatalf("subsequent ingest after self-merge attempt = %d %s, want 200", st, string(body))
		}

		// Unmerge on unmerged device returns 404; merge with missing device returns 404.
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+devID+"/unmerge", map[string]any{}); st != http.StatusNotFound {
			t.Fatalf("unmerge unmerged device = %d, want 404", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/dev_nonexistent/merge", map[string]string{"other_id": devID}); st != http.StatusNotFound {
			t.Fatalf("merge missing device = %d, want 404", st)
		}
	})

	t.Run("RevokeNodeResolvesSilenceAlert", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-t5", "node-t5-sil")

		b := syntheticBatch("site-t5", "node-t5-sil", "boot-1", 1, time.Now().UTC(), withIntervalMS(200))
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b); st != http.StatusOK {
			t.Fatalf("ingest = %d %s", st, string(body))
		}

		// Wait until silence alert appears (> 3 * 200ms = 600ms).
		waitUntil(t, 5*time.Second, 100*time.Millisecond, func() bool {
			st, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
			if st != http.StatusOK {
				return false
			}
			for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
				al := asMap(it)
				if asString(al["kind"]) == "silence" && asString(al["node_id"]) == "node-t5-sil" {
					return true
				}
			}
			return false
		})

		if rSt, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/nodes/node-t5-sil/revoke", nil); rSt != http.StatusOK {
			t.Fatalf("revoke = %d, want 200", rSt)
		}

		_, afterAlerts := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		for _, it := range asSlice(decodeMap(t, afterAlerts)["alerts"]) {
			al := asMap(it)
			if asString(al["kind"]) == "silence" && asString(al["node_id"]) == "node-t5-sil" {
				t.Fatalf("silence alert remained open after RevokeNode: %s", string(afterAlerts))
			}
		}
	})

	t.Run("LoginRateLimitEmptyXFFAndLogout", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		// Verify logout invalidates cookie.
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/logout", nil); st != http.StatusOK {
			t.Fatalf("POST /v1/logout = %d, want 200", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil); st != http.StatusUnauthorized {
			t.Fatalf("overview after logout = %d, want 401", st)
		}

		// Empty/whitespace X-Forwarded-For falls back to direct peer IP (127.0.0.1) for rate limiting.
		blankXFF := map[string]string{"X-Forwarded-For": "   , 198.51.100.99"}
		for i := 0; i < 5; i++ {
			st, _ := doJSONRequestWithHeaders(t, nil, http.MethodPost, srv.BaseURL+"/v1/login", map[string]string{"password": "wrong"}, blankXFF)
			if st != http.StatusUnauthorized {
				t.Fatalf("attempt %d = %d, want 401", i+1, st)
			}
		}
		if st6, _ := doJSONRequestWithHeaders(t, nil, http.MethodPost, srv.BaseURL+"/v1/login", map[string]string{"password": "e2e-admin-pass-1"}, blankXFF); st6 != http.StatusTooManyRequests {
			t.Fatalf("6th attempt with blank first XFF = %d, want 429", st6)
		}
		// Distinct trusted-proxy client IP still succeeds.
		if stOK, _ := doJSONRequestWithHeaders(t, nil, http.MethodPost, srv.BaseURL+"/v1/login", map[string]string{"password": "e2e-admin-pass-1"}, map[string]string{"X-Forwarded-For": "198.51.100.77"}); stOK != http.StatusOK {
			t.Fatalf("distinct XFF client = %d, want 200", stOK)
		}
	})

	t.Run("ZeroMultiplierRuleAndLegacyMultiplierContract", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-t5", "node-t5-0x")

		b := syntheticBatch("site-t5", "node-t5-0x", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(
				map[string]any{"name": "Free-Relay", "proxy_rx": 1200, "proxy_tx": 800},
				map[string]any{"name": "Paid-Relay", "proxy_rx": 500, "proxy_tx": 500},
			),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b); st != http.StatusOK {
			t.Fatalf("ingest = %d %s", st, string(body))
		}

		// 0.0x free-node rule via /v1/proxy/rules succeeds and scales Free-Relay adjusted=0.
		if st, body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "FreeRule", "match_type": "exact", "pattern": "Free-Relay", "factor": 0, "priority": 100, "enabled": true,
		}); st != http.StatusOK && st != http.StatusCreated {
			t.Fatalf("POST 0x rule = %d %s", st, string(body))
		}

		// Legacy PUT /v1/proxy/multipliers still rejects <=0 while accepting >0.
		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/multipliers", map[string]any{
			"name": "Paid-Relay", "factor": 0,
		}); st != http.StatusBadRequest {
			t.Fatalf("legacy PUT multiplier 0 = %d, want 400", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/multipliers", map[string]any{
			"name": "Paid-Relay", "factor": 2,
		}); st != http.StatusOK {
			t.Fatalf("legacy PUT multiplier 2 = %d, want 200", st)
		}

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		proxyMap := asMap(decodeMap(t, pBody)["proxy"])
		if adj, _ := asUint64(proxyMap["proxy_adjusted"]); adj != 2000 {
			t.Fatalf("proxy_adjusted = %d, want 2000 (0*2000 + 2*1000); body=%s", adj, string(pBody))
		}
	})

	t.Run("IngestRebootBatchResolvesSilenceAndRaisesGaps", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-t5", "node-t5-reboot")

		b1 := syntheticBatch("site-t5", "node-t5-reboot", "boot-1", 1, time.Now().UTC(), withIntervalMS(200))
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b1); st != http.StatusOK {
			t.Fatalf("boot-1 ingest = %d %s", st, string(body))
		}

		// Wait until silence alert fires.
		waitUntil(t, 5*time.Second, 100*time.Millisecond, func() bool {
			st, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
			if st != http.StatusOK {
				return false
			}
			for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
				al := asMap(it)
				if asString(al["kind"]) == "silence" && asString(al["node_id"]) == "node-t5-reboot" {
					return true
				}
			}
			return false
		})

		// Send boot-2 reboot batch carrying a buffer_drop gap.
		now := time.Now().UTC()
		b2 := syntheticBatch("site-t5", "node-t5-reboot", "boot-2", 1, now,
			withIntervalMS(15000),
			withGaps(map[string]any{
				"reason": "buffer_drop",
				"from":   now.Add(-time.Minute).Format(time.RFC3339),
				"to":     now.Format(time.RFC3339),
			}),
		)
		if st, body := postIngestBatch(t, srv.BaseURL, tok, b2); st != http.StatusOK {
			t.Fatalf("boot-2 ingest = %d %s", st, string(body))
		}

		_, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		var hasReboot, hasBufferDrop bool
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			al := asMap(it)
			if asString(al["node_id"]) != "node-t5-reboot" {
				continue
			}
			if asString(al["kind"]) == "silence" {
				t.Fatalf("silence alert stayed open after reboot batch: %s", string(aBody))
			}
			if asString(al["kind"]) == "reset" {
				msg := asString(al["message"])
				if strings.Contains(msg, "buffer_drop") {
					hasBufferDrop = true
				} else if strings.Contains(msg, "reset") {
					hasReboot = true
				}
			}
		}
		if !hasReboot || !hasBufferDrop {
			t.Fatalf("expected both reboot and buffer_drop reset alerts, got %s", string(aBody))
		}
	})

	t.Run("IngestSchemaRejectionOnMalformedBatches", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-t5", "node-t5-schema")

		now := time.Now().UTC()
		badBatches := []struct {
			name  string
			batch map[string]any
		}{
			{
				name: "empty interface name",
				batch: syntheticBatch("site-t5", "node-t5-schema", "boot-1", 1, now,
					withInterfaces(map[string]any{"name": "", "rx_delta": 1, "tx_delta": 1})),
			},
			{
				name: "empty device obs_ip",
				batch: syntheticBatch("site-t5", "node-t5-schema", "boot-1", 1, now,
					withDevices(map[string]any{"obs_ip": "", "rx_delta": 1, "tx_delta": 1, "source": "mihomo"})),
			},
			{
				name: "invalid device source",
				batch: syntheticBatch("site-t5", "node-t5-schema", "boot-1", 1, now,
					withDevices(map[string]any{"obs_ip": "203.0.113.10", "rx_delta": 1, "tx_delta": 1, "source": "bogus"})),
			},
			{
				name: "empty outbound name",
				batch: syntheticBatch("site-t5", "node-t5-schema", "boot-1", 1, now,
					withProxyOutbounds(map[string]any{"name": "", "proxy_rx": 1, "proxy_tx": 1})),
			},
			{
				name: "invalid gap reason",
				batch: syntheticBatch("site-t5", "node-t5-schema", "boot-1", 1, now,
					withGaps(map[string]any{"reason": "bogus", "from": now.Format(time.RFC3339), "to": now.Format(time.RFC3339)})),
			},
			{
				name: "zero gap from",
				batch: syntheticBatch("site-t5", "node-t5-schema", "boot-1", 1, now,
					withGaps(map[string]any{"reason": "reboot", "from": "0001-01-01T00:00:00Z", "to": now.Format(time.RFC3339)})),
			},
			{
				name: "non-object metadata array",
				batch: func() map[string]any {
					b := syntheticBatch("site-t5", "node-t5-schema", "boot-1", 1, now)
					b["metadata"] = []int{1, 2, 3}
					return b
				}(),
			},
		}

		for _, tc := range badBatches {
			st, body := postIngestBatch(t, srv.BaseURL, tok, tc.batch)
			if st != http.StatusBadRequest {
				t.Fatalf("%s: POST /v1/ingest status = %d %s, want 400", tc.name, st, string(body))
			}
		}

		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if batches, _ := asUint64(decodeMap(t, ovBody)["ingest_batches"]); batches != 0 {
			t.Fatalf("expected 0 stored ingest_batches after schema rejections, got %d", batches)
		}
	})

	t.Run("MihomoAdapterMetaSyncRecoveryAfterTransient503", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-t5", "node-t5-meta503")

		var proxyHits, providerHits atomic.Int32
		mh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				_, _ = w.Write([]byte(`{"proxies":{"HK-Recovered":{"name":"HK-Recovered","type":"Shadowsocks"}}}`))
			case "/providers/proxies":
				if providerHits.Add(1) == 1 {
					http.Error(w, "starting up", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"providers":{"SubRecovery":{"name":"SubRecovery","vehicleType":"HTTP","subscriptionInfo":{"Upload":10,"Download":20,"Total":1000,"Expire":1893456000},"proxies":[{"name":"HK-Recovered"}]}}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer mh.Close()

		startAgent(t, AgentConfig{
			ServerURL:  srv.BaseURL,
			SiteID:     "site-t5",
			NodeID:     "node-t5-meta503",
			Interval:   "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mh.URL},
		}, tok, "")

		// Must recover within a few ticks (well under the 5m meta sync interval).
		waitUntil(t, 5*time.Second, 100*time.Millisecond, func() bool {
			st, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
			if st != http.StatusOK {
				return false
			}
			for _, p := range asSlice(decodeMap(t, pBody)["providers"]) {
				if asString(asMap(p)["name"]) == "SubRecovery" {
					return true
				}
			}
			return false
		})
	})
}
