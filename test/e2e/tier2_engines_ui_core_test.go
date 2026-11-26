//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTier2_EnginesUICore(t *testing.T) {
	// --- F9 Edges (5 subtests) ---
	t.Run("F9_EmptyDimensionSliceAndZeroBytes", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f9-empty")

		st, _ := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f9-empty", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withDimensions(map[string]any{"type": "process", "key": "idleproc", "rx_delta": 0, "tx_delta": 0}),
		))
		if st != http.StatusOK {
			t.Fatalf("ingest status = %d", st)
		}

		trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		if trSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=process status = %d; body=%s", trSt, string(trBody))
		}
		m := decodeMap(t, trBody)
		if len(asSlice(m["keys"])) != 0 || len(asMap(m["totals"])) != 0 {
			t.Fatalf("zero-byte dimension created phantom key: %s", string(trBody))
		}
	})

	t.Run("F9_InvalidTrafficGroupAndMetricParam", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		if st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=invalid_dim", nil); st != http.StatusBadRequest {
			t.Fatalf("invalid group status = %d, want 400", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=invalid_dim", nil); st != http.StatusBadRequest {
			t.Fatalf("invalid dimension type status = %d, want 400", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=process&sort=packets", nil); st != http.StatusBadRequest {
			t.Fatalf("invalid dimension sort status = %d, want 400", st)
		}
	})

	t.Run("F9_DailyRollupSurvivesSamplePurge", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "f9-purge.db")
		srv1 := startServer(t, ServerOpts{DBPath: dbPath})
		c1 := newCookieClient(t)
		setupAdmin(t, c1, srv1.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, c1, srv1.BaseURL, "site-e2e", "node-t2-f9-purge")

		now := time.Now().UTC()
		if s1, b1 := postIngestBatch(t, srv1.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f9-purge", "boot-1", 1, now.Add(-20*24*time.Hour),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 0}),
		)); s1 != http.StatusOK {
			t.Fatalf("ingest old failed: %d %s", s1, string(b1))
		}
		if s2, b2 := postIngestBatch(t, srv1.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f9-purge", "boot-1", 2, now.Add(-1*time.Hour),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 500, "tx_delta": 0}),
		)); s2 != http.StatusOK {
			t.Fatalf("ingest recent failed: %d %s", s2, string(b2))
		}
		srv1.Stop()

		// Restart triggers MaintainLedgers: rolls >14d sample into ledger_daily and purges it from ledger_samples.
		srv2 := startServer(t, ServerOpts{DBPath: dbPath})
		c2 := newCookieClient(t)
		if lSt, lBody := loginAdmin(t, c2, srv2.BaseURL, "e2e-admin-pass-1"); lSt != http.StatusOK {
			t.Fatalf("re-login status = %d; body=%s", lSt, string(lBody))
		}

		q30 := fmt.Sprintf("%s/v1/traffic?from=%s&to=%s", srv2.BaseURL, now.Add(-30*24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
		_, b30 := doJSONRequest(t, c2, http.MethodGet, q30, nil)
		if tot30, _ := asUint64(asMap(decodeMap(t, b30)["totals"])["node-t2-f9-purge"]); tot30 != 1500 {
			t.Fatalf("30d daily rollup total = %d, want 1500; body=%s", tot30, string(b30))
		}

		q72 := fmt.Sprintf("%s/v1/traffic?from=%s&to=%s", srv2.BaseURL, now.Add(-72*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
		_, b72 := doJSONRequest(t, c2, http.MethodGet, q72, nil)
		if tot72, _ := asUint64(asMap(decodeMap(t, b72)["totals"])["node-t2-f9-purge"]); tot72 != 500 {
			t.Fatalf("72h sample total = %d, want 500; body=%s", tot72, string(b72))
		}
	})

	t.Run("F9_DuplicateBatchDoesNotDoubleDimensionRxTx", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f9-dup")

		b := syntheticBatch("site-e2e", "node-t2-f9-dup", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withDimensions(map[string]any{"type": "process", "key": "curl", "rx_delta": 600, "tx_delta": 200}),
		)
		s1, _ := postIngestBatch(t, srv.BaseURL, tok, b)
		s2, r2 := postIngestBatch(t, srv.BaseURL, tok, b)
		if s1 != http.StatusOK || s2 != http.StatusOK || !asBool(decodeMap(t, r2)["duplicate"]) {
			t.Fatalf("expected duplicate=true on second ingest: s1=%d s2=%d body=%s", s1, s2, string(r2))
		}

		trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		if trSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=process status = %d", trSt)
		}
		if tot, _ := asUint64(asMap(decodeMap(t, trBody)["totals"])["curl"]); tot != 800 {
			t.Fatalf("duplicate batch double-counted dimension curl=%d, want 800", tot)
		}
	})

	t.Run("F9_LargeUint64DeltaNoSignedOverflow", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f9-big")

		const rxBig uint64 = 10995116277760 // 10 TiB
		const txBig uint64 = 5497558138880  // 5 TiB
		st, _ := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f9-big", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": rxBig, "tx_delta": txBig}),
		))
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d", st)
		}

		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		tot, ok := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"])
		if !ok || tot != rxBig+txBig {
			t.Fatalf("overview total = %d (ok=%v), want %d", tot, ok, rxBig+txBig)
		}
		trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=node", nil)
		if trSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=node status = %d; body=%s", trSt, string(trBody))
		}
		trMap := decodeMap(t, trBody)
		if rxGot, ok := asUint64(asMap(trMap["rx_totals"])["node-t2-f9-big"]); !ok || rxGot != rxBig {
			t.Fatalf("traffic rx_totals = %d (ok=%v), want %d; body=%s", rxGot, ok, rxBig, string(trBody))
		}
		if txGot, ok := asUint64(asMap(trMap["tx_totals"])["node-t2-f9-big"]); !ok || txGot != txBig {
			t.Fatalf("traffic tx_totals = %d (ok=%v), want %d; body=%s", txGot, ok, txBig, string(trBody))
		}
		if totGot, ok := asUint64(asMap(trMap["totals"])["node-t2-f9-big"]); !ok || totGot != rxBig+txBig {
			t.Fatalf("traffic totals = %d (ok=%v), want %d; body=%s", totGot, ok, rxBig+txBig, string(trBody))
		}
	})

	// --- F10 Edges (5 subtests) ---
	t.Run("F10_MalformedFromToTimestamps", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		badQueries := []string{
			"/v1/traffic?from=not-a-date",
			"/v1/traffic?to=2026-99-99",
			"/v1/traffic?bucket=0",
			"/v1/traffic?bucket=-10",
		}
		for _, q := range badQueries {
			if st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+q, nil); st != http.StatusBadRequest {
				t.Fatalf("GET %s = %d, want 400", q, st)
			}
		}
	})

	t.Run("F10_ReversedFromAfterToBoundary", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?from=2026-08-20T00:00:00Z&to=2026-08-19T00:00:00Z", nil)
		if st != http.StatusBadRequest {
			t.Fatalf("reversed from>to status = %d, want 400", st)
		}
	})

	t.Run("F10_InvalidRangeAndTzOffsetBounds", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		if st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?range=100y", nil); st != http.StatusBadRequest {
			t.Fatalf("invalid range=100y status = %d, want 400", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?range=today&tz_offset=9999", nil); st != http.StatusBadRequest {
			t.Fatalf("out-of-range tz_offset=9999 status = %d, want 400", st)
		}
	})

	t.Run("F10_MidnightBoundaryTodayWithTzOffset", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f10-tz")

		loc := time.FixedZone("UTC+8", 8*3600)
		nowLocal := time.Now().In(loc)
		midnightLocal := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
		beforeMidnight := midnightLocal.Add(-2 * time.Minute).UTC()
		afterMidnight := midnightLocal.Add(1 * time.Second).UTC()
		if nowLocal.Sub(midnightLocal) < 2*time.Second {
			afterMidnight = nowLocal.UTC()
		}

		if s1, b1 := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f10-tz", "boot-1", 1, beforeMidnight,
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 9000, "tx_delta": 0}),
		)); s1 != http.StatusOK {
			t.Fatalf("ingest beforeMidnight failed: %d %s", s1, string(b1))
		}
		if s2, b2 := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f10-tz", "boot-1", 2, afterMidnight,
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 350, "tx_delta": 50}),
		)); s2 != http.StatusOK {
			t.Fatalf("ingest afterMidnight failed: %d %s", s2, string(b2))
		}

		st, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?range=today&tz_offset=480", nil)
		if st != http.StatusOK {
			t.Fatalf("range=today&tz_offset=480 status = %d; body=%s", st, string(body))
		}
		if tot, _ := asUint64(asMap(decodeMap(t, body)["totals"])["node-t2-f10-tz"]); tot != 400 {
			t.Fatalf("UTC+8 today total = %d, want 400; body=%s", tot, string(body))
		}
	})

	t.Run("F10_Boundary72hVs72hPlus1sBucketSwitch", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		now := time.Now().UTC()
		checks := []struct {
			span time.Duration
			want uint64
		}{
			{72 * time.Hour, 1800},
			{72*time.Hour + time.Second, 7200},
			{7 * 24 * time.Hour, 7200},
			{7*24*time.Hour + time.Second, 86400},
		}
		for _, c := range checks {
			u := fmt.Sprintf("%s/v1/traffic?from=%s&to=%s", srv.BaseURL, now.Add(-c.span).Format(time.RFC3339), now.Format(time.RFC3339))
			st, body := doJSONRequest(t, client, http.MethodGet, u, nil)
			if st != http.StatusOK {
				t.Fatalf("span=%s status=%d", c.span, st)
			}
			bs, _ := asUint64(decodeMap(t, body)["bucket_seconds"])
			if bs != c.want {
				t.Fatalf("span=%s bucket_seconds=%d, want %d", c.span, bs, c.want)
			}
		}
	})

	// --- F11 Edges (6 subtests, covering E9) ---
	t.Run("F11_E9_PriorityConflictAndDefault1x", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f11-e9")

		r1St, r1 := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "RegexIEPL", "match_type": "regex", "pattern": `IEPL`, "factor": 2.0, "priority": 100, "enabled": true,
		})
		if r1St != http.StatusOK && r1St != http.StatusCreated {
			t.Fatalf("POST RegexIEPL status = %d; body=%s", r1St, string(r1))
		}
		r1ID := extractRuleID(t, r1)
		if r2St, r2 := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "Auto", "match_type": "auto_extract", "pattern": "", "factor": 1.0, "priority": 50, "enabled": true,
		}); r2St != http.StatusOK && r2St != http.StatusCreated {
			t.Fatalf("POST Auto rule status = %d; body=%s", r2St, string(r2))
		}

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f11-e9", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(
				map[string]any{"name": "SG-IEPL-x3", "proxy_rx": 1000, "proxy_tx": 0},
				map[string]any{"name": "US-Plain", "proxy_rx": 500, "proxy_tx": 0},
			),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		checkProxy := func(wantSG uint64) {
			t.Helper()
			pSt, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
			if pSt != http.StatusOK {
				t.Fatalf("GET /v1/proxy status = %d; body=%s", pSt, string(pBody))
			}
			obs := map[string]map[string]any{}
			for _, it := range asSlice(decodeMap(t, pBody)["outbounds"]) {
				m := asMap(it)
				obs[asString(m["name"])] = m
			}
			sgAdj, _ := asUint64(obs["SG-IEPL-x3"]["adjusted"])
			usAdj, _ := asUint64(obs["US-Plain"]["adjusted"])
			usFac, _ := asFloat64(obs["US-Plain"]["factor"])
			if sgAdj != wantSG || asString(obs["SG-IEPL-x3"]["match_source"]) != "rule" {
				t.Fatalf("SG-IEPL-x3 adjusted=%d source=%q, want %d/rule", sgAdj, asString(obs["SG-IEPL-x3"]["match_source"]), wantSG)
			}
			if usAdj != 500 || usFac != 1.0 || asString(obs["US-Plain"]["match_source"]) != "default" {
				t.Fatalf("US-Plain adjusted=%d factor=%v source=%q, want 500/1.0/default", usAdj, usFac, asString(obs["US-Plain"]["match_source"]))
			}
		}
		checkProxy(2000)

		if uSt, uBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/rules/"+r1ID, map[string]any{
			"name": "RegexIEPL", "match_type": "regex", "pattern": `IEPL`, "factor": 3.0, "priority": 100, "enabled": true,
		}); uSt != http.StatusOK {
			t.Fatalf("PUT rule status = %d; body=%s", uSt, string(uBody))
		}
		checkProxy(3000)
	})

	t.Run("F11_InvalidRegexAndNonPositiveFactorRejected", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f11-zero")

		bad := []map[string]any{
			{"name": "BadRegex", "match_type": "regex", "pattern": "[unclosed", "factor": 2.0, "priority": 10, "enabled": true},
			{"name": "NegFac", "match_type": "keyword", "pattern": "HK", "factor": -1.5, "priority": 10, "enabled": true},
			{"name": "EmptyPat", "match_type": "keyword", "pattern": "", "factor": 1.5, "priority": 10, "enabled": true},
			{"name": "BadType", "match_type": "glob", "pattern": "HK*", "factor": 1.5, "priority": 10, "enabled": true},
		}
		for i, payload := range bad {
			if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", payload); st != http.StatusBadRequest {
				t.Fatalf("bad rule #%d status = %d, want 400", i, st)
			}
		}

		// factor == 0.0 is valid for multiplier_rules (0x free nodes).
		zSt, zBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "FreeNode0x", "match_type": "keyword", "pattern": "Free", "factor": 0.0, "priority": 100, "enabled": true,
		})
		if zSt != http.StatusOK && zSt != http.StatusCreated {
			t.Fatalf("zero factor rule status = %d, want 200; body=%s", zSt, string(zBody))
		}
		_ = extractRuleID(t, zBody)

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f11-zero", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "HK-Free", "proxy_rx": 500, "proxy_tx": 100}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}
		pSt, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		if pSt != http.StatusOK {
			t.Fatalf("GET /v1/proxy status = %d; body=%s", pSt, string(pBody))
		}
		ob := asMap(asSlice(decodeMap(t, pBody)["outbounds"])[0])
		adj, _ := asUint64(ob["adjusted"])
		fac, okFac := asFloat64(ob["factor"])
		if !okFac || fac != 0.0 || adj != 0 || asString(ob["match_source"]) != "rule" {
			t.Fatalf("expected free node 0x adjusted=0 match_source=rule; got=%s", string(pBody))
		}
	})

	t.Run("F11_DisabledRuleSkippedInRecalculation", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f11-dis")

		rSt, rBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "HK5x", "match_type": "keyword", "pattern": "HK", "factor": 5.0, "priority": 100, "enabled": true,
		})
		if rSt != http.StatusOK && rSt != http.StatusCreated {
			t.Fatalf("POST rule status = %d; body=%s", rSt, string(rBody))
		}
		id := extractRuleID(t, rBody)

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f11-dis", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"), withProxyOutbounds(map[string]any{"name": "HK-01", "proxy_rx": 100, "proxy_tx": 0}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}
		_, ov1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if adj, _ := asUint64(asMap(decodeMap(t, ov1)["bytes"])["proxy_adjusted"]); adj != 500 {
			t.Fatalf("enabled rule proxy_adjusted = %d, want 500", adj)
		}

		if uSt, uBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/rules/"+id, map[string]any{
			"name": "HK5x", "match_type": "keyword", "pattern": "HK", "factor": 5.0, "priority": 100, "enabled": false,
		}); uSt != http.StatusOK {
			t.Fatalf("PUT rule disable status = %d; body=%s", uSt, string(uBody))
		}
		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		ob := asMap(asSlice(decodeMap(t, pBody)["outbounds"])[0])
		if adj, _ := asUint64(ob["adjusted"]); adj != 100 || asString(ob["match_source"]) != "default" {
			t.Fatalf("disabled rule still applied: %s", string(pBody))
		}
	})

	t.Run("F11_AutoExtractPatternVariationsAndZero", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f11-auto")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f11-auto", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyNodes("HK-x2", "SG-2.5x", "JP-×0.5", "TW-倍率3", "US-x0", "US-Plain", "Node-x2026"),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}
		if rSt, rBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "AutoExtract", "match_type": "auto_extract", "pattern": "", "factor": 1.0, "priority": 100, "enabled": true,
		}); rSt != http.StatusOK && rSt != http.StatusCreated {
			t.Fatalf("POST AutoExtract rule status = %d; body=%s", rSt, string(rBody))
		}

		st, body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/preview", map[string]any{})
		if st != http.StatusOK {
			t.Fatalf("preview status = %d; body=%s", st, string(body))
		}
		items := extractPreviewMatches(t, body)
		got := map[string]float64{}
		sources := map[string]string{}
		for _, it := range items {
			row := asMap(it)
			f, _ := asFloat64(row["factor"])
			got[asString(row["name"])] = f
			sources[asString(row["name"])] = asString(row["match_source"])
		}
		want := map[string]struct {
			factor float64
			source string
		}{
			"HK-x2":      {2.0, "rule"},
			"SG-2.5x":    {2.5, "rule"},
			"JP-×0.5":    {0.5, "rule"},
			"TW-倍率3":     {3.0, "rule"},
			"US-x0":      {0.0, "rule"},
			"US-Plain":   {1.0, "default"},
			"Node-x2026": {1.0, "default"},
		}
		for k, w := range want {
			if got[k] != w.factor || sources[k] != w.source {
				t.Fatalf("auto_extract %s = (%v, %q), want (%v, %q); all=%v sources=%v", k, got[k], sources[k], w.factor, w.source, got, sources)
			}
		}
	})

	t.Run("F11_FailedImportIsAtomic", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		if rSt, rBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "ExistingA", "match_type": "exact", "pattern": "A", "factor": 2.0, "priority": 100, "enabled": true,
		}); rSt != http.StatusOK && rSt != http.StatusCreated {
			t.Fatalf("POST ExistingA rule status = %d; body=%s", rSt, string(rBody))
		}

		st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/import", map[string]any{
			"version": 1,
			"rules": []map[string]any{
				{"name": "ValidNew", "match_type": "keyword", "pattern": "HK", "factor": 1.5, "priority": 50, "enabled": true},
				{"name": "Broken", "match_type": "regex", "pattern": "[bad", "factor": 2.0, "priority": 40, "enabled": true},
			},
		})
		if st != http.StatusBadRequest {
			t.Fatalf("failed import status = %d, want 400", st)
		}

		_, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/rules", nil)
		rules := asSlice(decodeMap(t, gBody)["rules"])
		if len(rules) != 1 || asString(asMap(rules[0])["name"]) != "ExistingA" {
			t.Fatalf("failed import mutated existing rules: %s", string(gBody))
		}
	})

	t.Run("F11_LegacyMultiplierRejectsEmptyOrZero", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/multipliers", map[string]any{"name": "", "factor": 2}); st != http.StatusBadRequest {
			t.Fatalf("empty name status = %d, want 400", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/multipliers", map[string]any{"name": "a", "factor": 0}); st != http.StatusBadRequest {
			t.Fatalf("zero factor status = %d, want 400", st)
		}
	})

	// --- F12 Edges (5 subtests) ---
	t.Run("F12_RejectEmptyMatchersAndInvalidRegex", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name": "Empty", "matchers": map[string]any{},
		}); st != http.StatusBadRequest {
			t.Fatalf("empty matchers status = %d, want 400", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":     "BadRegex",
			"matchers": map[string]any{"domain": map[string]any{"type": "regex", "values": []string{"("}}},
		}); st != http.StatusBadRequest {
			t.Fatalf("invalid regex matcher status = %d, want 400", st)
		}
	})

	t.Run("F12_RejectInvalidResetDayAndNegativeQuota", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		baseMatcher := map[string]any{"process": map[string]any{"type": "exact", "values": []string{"curl"}}}
		bad := []map[string]any{
			{"name": "D29", "matchers": baseMatcher, "reset_type": "monthly", "reset_day": 29},
			{"name": "D0", "matchers": baseMatcher, "reset_type": "monthly", "reset_day": 0},
			{"name": "NegQ", "matchers": baseMatcher, "quota_value": -5, "quota_unit": "GB"},
		}
		for i, p := range bad {
			if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", p); st != http.StatusBadRequest {
				t.Fatalf("bad watch #%d status = %d, want 400", i, st)
			}
		}
	})

	t.Run("F12_MultiDimIntersectionOrUnionSemantics", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f12-md")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f12-md", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withDimensions(
				map[string]any{"type": "group", "key": "AI", "rx_delta": 500, "tx_delta": 100},
				map[string]any{"type": "process", "key": "Cursor", "rx_delta": 500, "tx_delta": 100},
			),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}
		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name": "MultiDim",
			"matchers": map[string]any{
				"group":   map[string]any{"type": "exact", "values": []string{"AI"}},
				"process": map[string]any{"type": "exact", "values": []string{"Cursor"}},
			},
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch status = %d; body=%s", cSt, string(cBody))
		}

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/watch", nil)
		if gSt != http.StatusOK {
			t.Fatalf("GET /v1/watch status = %d; body=%s", gSt, string(gBody))
		}
		items := asSlice(decodeMap(t, gBody)["items"])
		if len(items) != 1 {
			t.Fatalf("expected 1 watch item; body=%s", string(gBody))
		}
		it := asMap(items[0])
		rx, _ := asUint64(it["rx"])
		tx, _ := asUint64(it["tx"])
		// Group and process are orthogonal summaries of the same 500/100 traffic; watch must not double-count into 1000/200.
		if rx != 500 || tx != 100 {
			t.Fatalf("multi-dim watch rx=%d tx=%d, want 500/100 (no double-count); body=%s", rx, tx, string(gBody))
		}
	})

	t.Run("F12_WatchDeletedMissingReturns404", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/watch/99999", map[string]any{
			"name":     "Missing",
			"matchers": map[string]any{"process": map[string]any{"type": "exact", "values": []string{"curl"}}},
		}); st != http.StatusNotFound {
			t.Fatalf("PUT missing watch status = %d, want 404", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodDelete, srv.BaseURL+"/v1/watch/99999", nil); st != http.StatusNotFound {
			t.Fatalf("DELETE missing watch status = %d, want 404", st)
		}
	})

	t.Run("F12_WatchOverrideFactorZeroRejected", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		for _, fac := range []float64{0, -2.0} {
			st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
				"name":            "BadFac",
				"matchers":        map[string]any{"process": map[string]any{"type": "exact", "values": []string{"curl"}}},
				"override_factor": fac,
			})
			if st != http.StatusBadRequest {
				t.Fatalf("override_factor=%v status = %d, want 400", fac, st)
			}
		}
	})

	// --- F13 Edges (5 subtests) ---
	t.Run("F13_TotalZeroProviderNoDivideByZero", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f13-z")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f13-z", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 500, "proxy_tx": 0}),
			withProviders(map[string]any{
				"name": "ZeroTotal", "vehicle_type": "HTTP",
				"upload": 0, "download": 0, "total": 0, "expire": 0,
				"proxies": []string{"hk-01"},
			}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		st, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if st != http.StatusOK || strings.Contains(string(body), "NaN") || strings.Contains(string(body), "Inf") {
			t.Fatalf("providers zero-total failed: %d %s", st, string(body))
		}
		provs := asSlice(decodeMap(t, body)["providers"])
		if len(provs) == 0 {
			t.Fatalf("expected ZeroTotal provider in /v1/proxy/providers: %s", string(body))
		}
		p := asMap(provs[0])
		if dr, ok := asFloat64(p["drift_ratio"]); !ok || dr != 0 {
			t.Fatalf("drift_ratio = %v (ok=%v), want 0", dr, ok)
		}
	})

	t.Run("F13_MissingExpireAndDriftThresholdBoundary", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f13-bnd")

		// Exact 0.10 ratio (1100 vs 1000): must NOT trigger drift alert.
		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f13-bnd", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 1100, "proxy_tx": 0}),
			withProviders(map[string]any{
				"name": "BoundarySub", "vehicle_type": "HTTP",
				"upload": 0, "download": 1000, "total": 10000, "expire": 0,
				"proxies": []string{"hk-01"},
			}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest 1 failed: %d %s", iSt, string(iBody))
		}

		pSt, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if pSt != http.StatusOK || strings.Contains(string(pBody), "NaN") || strings.Contains(string(pBody), "Inf") {
			t.Fatalf("GET /v1/proxy/providers = %d %s", pSt, string(pBody))
		}
		var foundBoundary bool
		for _, it := range asSlice(decodeMap(t, pBody)["providers"]) {
			p := asMap(it)
			if asString(p["name"]) == "BoundarySub" {
				foundBoundary = true
				exp, _ := asUint64(p["expire"])
				if exp != 0 {
					t.Fatalf("BoundarySub expire = %d, want 0", exp)
				}
			}
		}
		if !foundBoundary {
			t.Fatalf("BoundarySub missing from GET /v1/proxy/providers: %s", string(pBody))
		}

		_, a1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		for _, it := range asSlice(decodeMap(t, a1)["alerts"]) {
			if asString(asMap(it)["kind"]) == "drift" {
				t.Fatalf("exact 0.10 drift ratio must not alert; alerts=%s", string(a1))
			}
		}

		// Bump local to 1101 (ratio 0.101 > 0.10): must trigger drift alert.
		if iSt2, iBody2 := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f13-bnd", "boot-1", 2, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 1, "proxy_tx": 0}),
		)); iSt2 != http.StatusOK {
			t.Fatalf("ingest 2 failed: %d %s", iSt2, string(iBody2))
		}
		_, a2 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		found := false
		for _, it := range asSlice(decodeMap(t, a2)["alerts"]) {
			if asString(asMap(it)["kind"]) == "drift" {
				found = true
			}
		}
		if !found {
			t.Fatalf("ratio 0.101 > 0.10 must trigger drift alert; alerts=%s", string(a2))
		}
	})

	t.Run("F13_ManualProviderInvalidUnitOrResetDay", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		bad := []map[string]any{
			{"name": "BadUnit", "unit": "PB", "total_value": 1, "reset_day": 1},
			{"name": "BadDay", "unit": "GB", "total_value": 10, "reset_day": 31},
			{"name": "", "unit": "GB", "total_value": 10, "reset_day": 1},
		}
		for i, p := range bad {
			if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/providers", p); st != http.StatusBadRequest {
				t.Fatalf("bad manual provider #%d status = %d, want 400", i, st)
			}
		}
	})

	t.Run("F13_LegacyBillingInvalidResetDay", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		for _, day := range []int{0, 29} {
			if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/billing", map[string]any{
				"reset_day": day, "provider_bytes": 100,
			}); st != http.StatusBadRequest {
				t.Fatalf("reset_day=%d status = %d, want 400", day, st)
			}
		}
	})

	t.Run("F13_ProviderUpdateResolvingDriftAlert", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f13-res")

		if s1, b1 := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f13-res", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 2000, "proxy_tx": 0}),
			withProviders(map[string]any{
				"name": "ResolveSub", "vehicle_type": "HTTP",
				"upload": 0, "download": 1000, "total": 10000, "expire": 1893456000,
				"proxies": []string{"hk-01"},
			}),
		)); s1 != http.StatusOK {
			t.Fatalf("ingest 1 failed: %d %s", s1, string(b1))
		}

		_, a1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		hasDrift := false
		for _, it := range asSlice(decodeMap(t, a1)["alerts"]) {
			if asString(asMap(it)["kind"]) == "drift" {
				hasDrift = true
			}
		}
		if !hasDrift {
			t.Fatalf("expected drift alert initially; body=%s", string(a1))
		}

		// Sync provider usage up to 2000 -> drift resolves automatically.
		if s2, b2 := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f13-res", "boot-1", 2, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProviders(map[string]any{
				"name": "ResolveSub", "vehicle_type": "HTTP",
				"upload": 0, "download": 2000, "total": 10000, "expire": 1893456000,
				"proxies": []string{"hk-01"},
			}),
		)); s2 != http.StatusOK {
			t.Fatalf("ingest 2 failed: %d %s", s2, string(b2))
		}

		_, a2 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		for _, it := range asSlice(decodeMap(t, a2)["alerts"]) {
			if asString(asMap(it)["kind"]) == "drift" {
				t.Fatalf("drift alert should auto-resolve after provider update; alerts=%s", string(a2))
			}
		}
	})

	// --- F14 Edges (5 subtests, covering E12) ---
	t.Run("F14_E12_ZeroGoogleFontsOrExternalCDN", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		st, _, htmlBytes := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/", nil, nil)
		if st != http.StatusOK || len(htmlBytes) == 0 {
			t.Fatalf("GET / failed: status=%d len=%d", st, len(htmlBytes))
		}
		html := string(htmlBytes)

		forbidden := []string{"fonts.googleapis.com", "fonts.gstatic.com", "unpkg.com", "cdn.jsdelivr.net"}
		for _, pat := range forbidden {
			if strings.Contains(html, pat) {
				t.Fatalf("index.html contains external CDN reference %q", pat)
			}
		}
		extTagRe := regexp.MustCompile(`(?is)<(?:script|link)\b[^>]*\b(?:src|href)\s*=\s*["']https?://`)
		if extTagRe.MatchString(html) {
			t.Fatalf("index.html contains external http(s) script/stylesheet tag:\n%s", html)
		}

		assetRe := regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["'](/assets/[^"']+)["']`)
		matches := assetRe.FindAllStringSubmatch(html, -1)
		if len(matches) == 0 {
			t.Fatalf("index.html missing /assets/* bundle references:\n%s", html)
		}
		cssExtRe := regexp.MustCompile(`(?i)(?:@import\b[^;]*https?://|url\(\s*["']?https?://)`)
		for _, m := range matches {
			aSt, _, asset := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+m[1], nil, nil)
			if aSt != http.StatusOK || len(asset) == 0 {
				t.Fatalf("asset %s failed: status=%d len=%d", m[1], aSt, len(asset))
			}
			for _, pat := range forbidden {
				if strings.Contains(string(asset), pat) {
					t.Fatalf("asset %s contains external CDN %q", m[1], pat)
				}
			}
			if strings.HasSuffix(strings.ToLower(m[1]), ".css") && cssExtRe.Match(asset) {
				t.Fatalf("CSS asset %s contains external @import or url(https://...)", m[1])
			}
		}
	})

	t.Run("F14_StaticAssetsMethodAndPathTraversalSafe", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		for _, p := range []string{"/../etc/passwd", "/assets/../../etc/passwd", "/nonexistent.js"} {
			st, _, body := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+p, nil, nil)
			if strings.Contains(string(body), "root:x:") {
				t.Fatalf("path traversal leaked host file on %s", p)
			}
			if st == http.StatusInternalServerError {
				t.Fatalf("unexpected 500 on %s", p)
			}
		}
		for _, tc := range []struct{ method, path string }{
			{http.MethodPost, "/"},
			{http.MethodPost, "/styles.css"},
			{http.MethodPut, "/assets/index.js"},
		} {
			st, _, _ := doRawRequest(t, nil, tc.method, srv.BaseURL+tc.path, []byte("mut"), nil)
			if st == http.StatusOK || st == http.StatusCreated || st == http.StatusInternalServerError {
				t.Fatalf("non-GET static request %s %s returned unexpected status %d", tc.method, tc.path, st)
			}
		}
	})

	t.Run("F14_APIRoutesNotShadowedBySPAFallback", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		setupAdmin(t, newCookieClient(t), srv.BaseURL, "e2e-admin-pass-1")

		stOv, hdrOv, _ := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/v1/overview", nil, nil)
		if stOv != http.StatusUnauthorized || strings.Contains(hdrOv.Get("Content-Type"), "text/html") {
			t.Fatalf("GET /v1/overview shadowed: status=%d ct=%q", stOv, hdrOv.Get("Content-Type"))
		}
		stUnk, hdrUnk, _ := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/v1/nonexistent", nil, nil)
		if stUnk == http.StatusOK && strings.Contains(hdrUnk.Get("Content-Type"), "text/html") {
			t.Fatalf("unknown /v1/* route returned 200 text/html SPA fallback")
		}
	})

	t.Run("F14_EmptyStateEndpointsReturnEmptyArraysNotNull", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		checks := []struct{ path, key string }{
			{"/v1/devices", "devices"},
			{"/v1/nodes", "nodes"},
			{"/v1/alerts", "alerts"},
			{"/v1/proxy", "outbounds"},
		}
		for _, c := range checks {
			st, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+c.path, nil)
			if st != http.StatusOK {
				t.Fatalf("GET %s = %d", c.path, st)
			}
			raw := string(body)
			if strings.Contains(raw, fmt.Sprintf(`"%s":null`, c.key)) {
				t.Fatalf("GET %s serialized %s as null instead of []: %s", c.path, c.key, raw)
			}
		}
	})

	t.Run("F14_SetupConflictAfterInitialized", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		setupAdmin(t, newCookieClient(t), srv.BaseURL, "e2e-admin-pass-1")
		st, _ := doJSONRequest(t, nil, http.MethodPost, srv.BaseURL+"/v1/setup", map[string]string{"password": "another-password-1"})
		if st != http.StatusConflict {
			t.Fatalf("second setup status = %d, want 409", st)
		}
	})

	// --- F15 Edges (5 subtests) ---
	t.Run("F15_SQLitePreMigrationBackupOnBehindSchema", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "upgrade.db")
		srv1 := startServer(t, ServerOpts{DBPath: dbPath})
		c1 := newCookieClient(t)
		setupAdmin(t, c1, srv1.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, c1, srv1.BaseURL, "site-e2e", "node-t2-f15-mig")
		if iSt, iBody := postIngestBatch(t, srv1.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f15-mig", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 700, "tx_delta": 300}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}
		srv1.Stop()

		// Lower PRAGMA user_version to 1 to simulate behind-schema DB (works under WAL and DELETE).
		if prev := setSQLiteUserVersion(t, dbPath, 1); prev < 11 {
			t.Fatalf("unexpected user_version %d", prev)
		}

		srv2 := startServer(t, ServerOpts{DBPath: dbPath})
		baks, err := filepath.Glob(filepath.Join(filepath.Dir(dbPath), "lantally.db.bak-*"))
		if err != nil || len(baks) == 0 {
			t.Fatalf("expected pre-migration backup lantally.db.bak-* in %s, got %v (err=%v)", filepath.Dir(dbPath), baks, err)
		}

		c2 := newCookieClient(t)
		if st, _ := loginAdmin(t, c2, srv2.BaseURL, "e2e-admin-pass-1"); st != http.StatusOK {
			t.Fatalf("login after migration = %d", st)
		}
		_, ovBody := doJSONRequest(t, c2, http.MethodGet, srv2.BaseURL+"/v1/overview", nil)
		if tot, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"]); tot != 1000 {
			t.Fatalf("migrated DB total = %d, want 1000", tot)
		}
	})

	t.Run("F15_UnreachableExternalVMDoesNotCrashIngest", func(t *testing.T) {
		closedAddr := freeAddr(t)
		srv := startServer(t, ServerOpts{MetricsURL: "http://" + closedAddr})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f15-vm")

		st, _ := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f15-vm", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 200, "tx_delta": 100}),
		))
		if st != http.StatusOK {
			t.Fatalf("ingest with unreachable VM = %d, want 200", st)
		}
	})

	t.Run("F15_InvalidDBPathExitsNonZero", func(t *testing.T) {
		dir := t.TempDir()
		blocker := filepath.Join(dir, "file-not-dir")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		badDB := filepath.Join(blocker, "lantally.db")
		cmd := newServerCmd(freeAddr(t), badDB)
		if err := cmd.Run(); err == nil {
			t.Fatalf("expected non-zero exit for invalid -db path %s", badDB)
		}
	})

	t.Run("F15_CrossCompileAllTargetsZeroCGO", func(t *testing.T) {
		root := repoRoot(t)
		outDir := t.TempDir()
		for _, tgt := range []struct{ goos, goarch, ext string }{
			{"windows", "arm64", ".exe"},
			{"darwin", "arm64", ""},
		} {
			cmd := newCrossBuildCmd(root, filepath.Join(outDir, "agent-"+tgt.goos+"-"+tgt.goarch+tgt.ext), tgt.goos, tgt.goarch, "./cmd/lantally-agent")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("cross-compile %s/%s failed: %v\n%s", tgt.goos, tgt.goarch, err, string(out))
			}
		}
	})

	t.Run("F15_DockerfileDoesNotDependOnVictoriaMetricsBinary", func(t *testing.T) {
		root := repoRoot(t)
		df, err := os.ReadFile(filepath.Join(root, "deploy", "docker", "Dockerfile"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(df), `ENTRYPOINT ["/usr/local/bin/lantally-allinone"]`) {
			t.Fatalf("Dockerfile still uses lantally-allinone entrypoint which requires missing victoria-metrics binary")
		}
	})

	// --- F16 Edges (6 subtests) ---
	t.Run("F16_LoginRateLimitLockoutAndXFF", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		setupAdmin(t, newCookieClient(t), srv.BaseURL, "e2e-admin-pass-1")

		xff1 := map[string]string{"X-Forwarded-For": "198.51.100.5"}
		for i := 0; i < 5; i++ {
			st, _ := doJSONRequestWithHeaders(t, nil, http.MethodPost, srv.BaseURL+"/v1/login", map[string]string{"password": "wrong-password"}, xff1)
			if st != http.StatusUnauthorized {
				t.Fatalf("attempt %d status = %d, want 401", i+1, st)
			}
		}
		st6, _ := doJSONRequestWithHeaders(t, nil, http.MethodPost, srv.BaseURL+"/v1/login", map[string]string{"password": "e2e-admin-pass-1"}, xff1)
		if st6 != http.StatusTooManyRequests {
			t.Fatalf("6th attempt status = %d, want 429", st6)
		}

		xff2 := map[string]string{"X-Forwarded-For": "198.51.100.6"}
		stOther, _ := doJSONRequestWithHeaders(t, nil, http.MethodPost, srv.BaseURL+"/v1/login", map[string]string{"password": "e2e-admin-pass-1"}, xff2)
		if stOther != http.StatusOK {
			t.Fatalf("different client IP status = %d, want 200", stOther)
		}
	})

	t.Run("F16_CookieFlagsHTTPSVsHTTP", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		setupAdmin(t, newCookieClient(t), srv.BaseURL, "e2e-admin-pass-1")

		loginRaw := func(headers map[string]string) string {
			st, hdr, _ := doRawRequest(t, nil, http.MethodPost, srv.BaseURL+"/v1/login", []byte(`{"password":"e2e-admin-pass-1"}`), headers)
			if st != http.StatusOK {
				t.Fatalf("login status = %d", st)
			}
			return hdr.Get("Set-Cookie")
		}

		httpCookie := loginRaw(map[string]string{"Content-Type": "application/json", "X-Forwarded-For": "198.51.100.20"})
		if !strings.Contains(httpCookie, "HttpOnly") || !strings.Contains(httpCookie, "SameSite=Strict") || strings.Contains(httpCookie, "Secure") {
			t.Fatalf("HTTP cookie flags unexpected: %q", httpCookie)
		}

		httpsCookie := loginRaw(map[string]string{"Content-Type": "application/json", "X-Forwarded-For": "198.51.100.21", "X-Forwarded-Proto": "https"})
		if !strings.Contains(httpsCookie, "HttpOnly") || !strings.Contains(httpsCookie, "SameSite=Strict") || !strings.Contains(httpsCookie, "Secure") {
			t.Fatalf("HTTPS cookie flags unexpected: %q", httpsCookie)
		}
	})

	t.Run("F16_ClaimCodeSingleUseAndNotFound", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		if st, _ := redeemClaim(t, srv.BaseURL, "ZZZZZZZZ"); st != http.StatusNotFound {
			t.Fatalf("unknown claim status = %d, want 404", st)
		}
		c := createClaim(t, client, srv.BaseURL, "site-e2e", "node-t2-claim", true)
		code := asString(c["code"])
		if st, _ := redeemClaim(t, srv.BaseURL, code); st != http.StatusOK {
			t.Fatalf("first redeem = %d, want 200", st)
		}
		if st, _ := redeemClaim(t, srv.BaseURL, code); st != http.StatusConflict {
			t.Fatalf("second redeem = %d, want 409", st)
		}
	})

	t.Run("F16_AgentBinaryPathTraversalAndMissingDir", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		for _, p := range []string{"/agents/../../etc/passwd", "/agents/linux/mips"} {
			st, _, _ := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+p, nil, nil)
			if st != http.StatusNotFound {
				t.Fatalf("GET %s = %d, want 404", p, st)
			}
		}
	})

	t.Run("F16_DeviceCrossSiteMergeAndBadAlertAck", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokA := enrollNode(t, client, srv.BaseURL, "site-a", "node-sa")
		tokB := enrollNode(t, client, srv.BaseURL, "site-b", "node-sb")

		if sA, bA := postIngestBatch(t, srv.BaseURL, tokA, syntheticBatch("site-a", "node-sa", "boot-1", 1, time.Now().UTC(),
			withCapabilities("nlbwmon"),
			withDevices(map[string]any{"obs_ip": "203.0.113.10", "obs_mac": "02:00:00:00:00:10", "rx_delta": 100, "tx_delta": 0, "source": "nlbwmon"}),
		)); sA != http.StatusOK {
			t.Fatalf("ingest site-a failed: %d %s", sA, string(bA))
		}
		if sB, bB := postIngestBatch(t, srv.BaseURL, tokB, syntheticBatch("site-b", "node-sb", "boot-1", 1, time.Now().UTC(),
			withCapabilities("nlbwmon"),
			withDevices(map[string]any{"obs_ip": "203.0.113.11", "obs_mac": "02:00:00:00:00:11", "rx_delta": 100, "tx_delta": 0, "source": "nlbwmon"}),
		)); sB != http.StatusOK {
			t.Fatalf("ingest site-b failed: %d %s", sB, string(bB))
		}

		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs := asSlice(decodeMap(t, dBody)["devices"])
		id1 := asString(asMap(devs[0])["id"])
		id2 := asString(asMap(devs[1])["id"])
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+id1+"/merge", map[string]string{"other_id": id2}); st != http.StatusBadRequest {
			t.Fatalf("cross-site merge status = %d, want 400", st)
		}

		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/alerts/abc/ack", map[string]any{}); st != http.StatusBadRequest {
			t.Fatalf("non-integer alert ack = %d, want 400", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/alerts/99999/ack", map[string]any{}); st != http.StatusNotFound {
			t.Fatalf("missing alert ack = %d, want 404", st)
		}

		// Trigger a real reset alert and verify ack + double-ack.
		if sR, bR := postIngestBatch(t, srv.BaseURL, tokA, syntheticBatch("site-a", "node-sa", "boot-2", 1, time.Now().UTC())); sR != http.StatusOK {
			t.Fatalf("ingest boot-2 failed: %d %s", sR, string(bR))
		}
		_, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		alerts := asSlice(decodeMap(t, aBody)["alerts"])
		if len(alerts) == 0 {
			t.Fatalf("expected reset alert")
		}
		aid := fmt.Sprintf("%v", asMap(alerts[0])["id"])
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/alerts/"+aid+"/ack", map[string]any{}); st != http.StatusOK {
			t.Fatalf("first ack = %d, want 200", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/alerts/"+aid+"/ack", map[string]any{}); st != http.StatusNotFound {
			t.Fatalf("double ack = %d, want 404", st)
		}
	})

	t.Run("F16_ChangePasswordValidationAndInvalidAuth", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/settings/password", map[string]string{
			"current": "wrong-current", "password": "e2e-admin-pass-2",
		}); st != http.StatusUnauthorized {
			t.Fatalf("wrong current password status = %d, want 401", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/settings/password", map[string]string{
			"current": "e2e-admin-pass-1", "password": "short",
		}); st != http.StatusBadRequest {
			t.Fatalf("short new password status = %d, want 400", st)
		}
		if st, _ := loginAdmin(t, newCookieClient(t), srv.BaseURL, "e2e-admin-pass-1"); st != http.StatusOK {
			t.Fatalf("original password should still work, got %d", st)
		}
	})

	// --- F17 Edges (5 subtests) ---
	t.Run("F17_VMRejectsForbiddenLabelsInCodeContract", func(t *testing.T) {
		vm := newMockVM(t)
		srv := startServer(t, ServerOpts{MetricsURL: vm.URL()})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f17-vm")

		st, body := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f17-vm", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface", "mihomo"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 512, "tx_delta": 256}),
			withDevices(map[string]any{"obs_ip": "203.0.113.88", "obs_mac": "02:00:00:00:00:88", "rx_delta": 100, "tx_delta": 50, "source": "mihomo", "outbound": "hk-01"}),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 100, "proxy_tx": 50}),
		))
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		reqs := vm.WaitForSamples(t, 5*time.Second, 1)
		for _, r := range reqs {
			for _, bad := range []string{"destination", "domain", "url", "conn_id", "source_ip", "203.0.113.", "02:00:00:", "hk-01"} {
				if strings.Contains(r.RawBody, bad) {
					t.Fatalf("forbidden substring %q leaked in VM payload: %s", bad, r.RawBody)
				}
			}
		}
	})

	t.Run("F17_SettingsAndErrorsNeverLeakTokensOrHashes", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f17-leak")
		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f17-leak", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 100, "tx_delta": 50}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		for _, ep := range []string{"/v1/settings", "/v1/nodes", "/v1/devices", "/v1/alerts", "/v1/overview"} {
			gSt, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+ep, nil)
			if gSt != http.StatusOK {
				t.Fatalf("GET %s status = %d; body=%s", ep, gSt, string(body))
			}
			s := string(body)
			if strings.Contains(s, tok) || strings.Contains(s, "argon2") || strings.Contains(s, "lt_") {
				t.Fatalf("token or hash leaked on %s: %s", ep, s)
			}
		}
	})

	t.Run("F17_DirectCollectorURLAndBearerSecretMasked", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		closed := freeAddr(t)
		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "ClosedDC", "site_id": "site-e2e", "node_id": "dc-closed",
			"url": "http://" + closed + "?token=secret123", "secret": "tok-xyz-e2e", "enabled": false,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("POST /v1/direct-collectors = %d %s", cSt, string(cBody))
		}
		id := fmt.Sprintf("%v", decodeMap(t, cBody)["id"])

		tSt, testBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+id+"/test", map[string]any{})
		if tSt == http.StatusNotFound || tSt == http.StatusInternalServerError {
			t.Fatalf("POST /v1/direct-collectors/%s/test status = %d; body=%s", id, tSt, string(testBody))
		}
		if tSt == http.StatusOK && asBool(decodeMap(t, testBody)["ok"]) {
			t.Fatalf("closed-port direct collector test should not report ok=true: %s", string(testBody))
		}
		lSt, listBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/direct-collectors", nil)
		if lSt != http.StatusOK {
			t.Fatalf("GET /v1/direct-collectors status = %d; body=%s", lSt, string(listBody))
		}
		var foundClosed bool
		for _, it := range asSlice(decodeMap(t, listBody)["collectors"]) {
			c := asMap(it)
			if asString(c["name"]) == "ClosedDC" {
				foundClosed = true
				if !asBool(c["has_secret"]) {
					t.Fatalf("expected ClosedDC has_secret=true; body=%s", string(listBody))
				}
			}
		}
		if !foundClosed {
			t.Fatalf("ClosedDC missing from GET /v1/direct-collectors: %s", string(listBody))
		}
		combined := string(testBody) + "\n" + string(listBody)
		if strings.Contains(combined, "tok-xyz-e2e") || strings.Contains(combined, "secret123") {
			t.Fatalf("secret or query token leaked in API response: %s", combined)
		}
	})

	t.Run("F17_OptInDomainDisabledPreventsDomainLeak", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f17-nodom")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{
				ID: "c1", Upload: 100, Download: 200, Chains: []string{"HK-01", "Proxy"},
				Metadata: MockMihomoMetadata{Host: "sensitive.example.org", SniffHost: "sensitive.example.org"},
			}},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f17-nodom", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 300
		})
		dSt, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if dSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=domain = %d; body=%s", dSt, string(dBody))
		}
		if len(asMap(decodeMap(t, dBody)["totals"])) != 0 {
			t.Fatalf("domains collected while opt-in disabled: %s", string(dBody))
		}
	})

	t.Run("F17_InstallScriptHandlesMalformedOrMissingClaim", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		st, _, body := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/install.sh", nil, map[string]string{
			"X-Forwarded-Proto": "https",
			"Host":              "demo.example.test:8443",
		})
		if st != http.StatusOK {
			t.Fatalf("GET /install.sh = %d", st)
		}
		s := string(body)
		if !strings.Contains(s, "https://demo.example.test:8443") || !strings.Contains(s, "umask 077") || !strings.Contains(s, "chmod 600") {
			t.Fatalf("install.sh missing https base URL or 0600/umask hardening:\n%s", s)
		}
	})
}
