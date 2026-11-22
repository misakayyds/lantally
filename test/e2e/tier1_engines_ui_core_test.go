//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTier1_EnginesUICore(t *testing.T) {
	// --- F9: Multi-Dimensional rx/tx Ledger (6 subtests) ---
	t.Run("F9_OverviewRxTxBreakdown", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f9-ov")

		batch := syntheticBatch("site-e2e", "node-f9-ov", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface", "mihomo"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1024, "tx_delta": 512}),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 400, "proxy_tx": 200}),
		)
		st, _ := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d", st)
		}

		ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovSt != http.StatusOK {
			t.Fatalf("GET /v1/overview status = %d; body=%s", ovSt, string(ovBody))
		}
		ov := decodeMap(t, ovBody)
		tot, _ := asUint64(asMap(ov["bytes"])["total"])
		if tot != 1536 {
			t.Fatalf("bytes.total = %d, want 1536", tot)
		}
		topRx, _ := asUint64(ov["rx"])
		topTx, _ := asUint64(ov["tx"])
		topTot, _ := asUint64(ov["total"])
		if topRx != 1024 || topTx != 512 || topTot != 1536 {
			t.Fatalf("overview top rx=%d tx=%d total=%d, want 1024/512/1536; body=%s", topRx, topTx, topTot, string(ovBody))
		}

		detail := asMap(ov["breakdown"])
		if detail == nil {
			t.Fatalf("expected breakdown in /v1/overview; body=%s", string(ovBody))
		}
		totDetail := asMap(detail["total"])
		rx, _ := asUint64(totDetail["rx"])
		tx, _ := asUint64(totDetail["tx"])
		dtot, _ := asUint64(totDetail["total"])
		if rx != 1024 || tx != 512 || dtot != 1536 {
			t.Fatalf("overview total rx=%d tx=%d total=%d, want 1024/512/1536; body=%s", rx, tx, dtot, string(ovBody))
		}
		prDetail := asMap(detail["proxy_raw"])
		prx, _ := asUint64(prDetail["rx"])
		ptx, _ := asUint64(prDetail["tx"])
		ptot, _ := asUint64(prDetail["total"])
		if prx != 400 || ptx != 200 || ptot != 600 {
			t.Fatalf("overview proxy_raw rx=%d tx=%d total=%d, want 400/200/600; body=%s", prx, ptx, ptot, string(ovBody))
		}
	})

	t.Run("F9_TrafficGroupByGroupAndProcess", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f9-gp")

		batch := syntheticBatch("site-e2e", "node-f9-gp", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withDimensions(
				map[string]any{"type": "group", "key": "AI", "rx_delta": 300, "tx_delta": 100},
				map[string]any{"type": "process", "key": "curl", "rx_delta": 200, "tx_delta": 50},
			),
		)
		st, _ := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d", st)
		}

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		if gSt != http.StatusOK {
			t.Fatalf("group=group status = %d; body=%s", gSt, string(gBody))
		}
		aiTot, _ := asUint64(asMap(decodeMap(t, gBody)["totals"])["AI"])
		if aiTot != 400 {
			t.Fatalf("group AI total = %d, want 400; body=%s", aiTot, string(gBody))
		}

		pSt, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		if pSt != http.StatusOK {
			t.Fatalf("group=process status = %d; body=%s", pSt, string(pBody))
		}
		curlTot, _ := asUint64(asMap(decodeMap(t, pBody)["totals"])["curl"])
		if curlTot != 250 {
			t.Fatalf("process curl total = %d, want 250; body=%s", curlTot, string(pBody))
		}
	})

	t.Run("F9_TrafficGroupByDomainAndIface", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f9-di")

		batch := syntheticBatch("site-e2e", "node-f9-di", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface", "mihomo"),
			withDimensions(
				map[string]any{"type": "domain", "key": "example.com", "rx_delta": 500, "tx_delta": 100},
				map[string]any{"type": "iface", "key": "eth0", "rx_delta": 1000, "tx_delta": 200},
			),
		)
		st, _ := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d", st)
		}

		dSt, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if dSt != http.StatusOK {
			t.Fatalf("group=domain status = %d; body=%s", dSt, string(dBody))
		}
		domTot, _ := asUint64(asMap(decodeMap(t, dBody)["totals"])["example.com"])
		if domTot != 600 {
			t.Fatalf("domain example.com = %d, want 600; body=%s", domTot, string(dBody))
		}

		iSt, iBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface", nil)
		if iSt != http.StatusOK {
			t.Fatalf("group=iface status = %d; body=%s", iSt, string(iBody))
		}
		ifTot, _ := asUint64(asMap(decodeMap(t, iBody)["totals"])["eth0"])
		if ifTot != 1200 {
			t.Fatalf("iface eth0 = %d, want 1200; body=%s", ifTot, string(iBody))
		}
	})

	t.Run("F9_TrafficMetricRxTxTotalSelector", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-a")

		batch := syntheticBatch("site-e2e", "node-a", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 800, "tx_delta": 200}),
		)
		st, _ := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d", st)
		}

		qSt, qBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=node", nil)
		if qSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=node status = %d; body=%s", qSt, string(qBody))
		}
		qm := decodeMap(t, qBody)
		rxGot, _ := asUint64(asMap(qm["rx_totals"])["node-a"])
		txGot, _ := asUint64(asMap(qm["tx_totals"])["node-a"])
		totGot, _ := asUint64(asMap(qm["totals"])["node-a"])
		bd := asMap(asMap(qm["breakdown"])["node-a"])
		bdRx, _ := asUint64(bd["rx"])
		bdTx, _ := asUint64(bd["tx"])
		bdTot, _ := asUint64(bd["total"])
		if rxGot != 800 || txGot != 200 || totGot != 1000 || bdRx != 800 || bdTx != 200 || bdTot != 1000 {
			t.Fatalf("node-a rx/tx/total = %d/%d/%d bd=%v, want 800/200/1000; body=%s", rxGot, txGot, totGot, bd, string(qBody))
		}
		var matchedPoint bool
		for _, ptRaw := range asSlice(qm["points"]) {
			pt := asMap(ptRaw)
			pv, _ := asUint64(asMap(pt["values"])["node-a"])
			prx, _ := asUint64(asMap(pt["rx_values"])["node-a"])
			ptx, _ := asUint64(asMap(pt["tx_values"])["node-a"])
			if pv == 1000 && prx == 800 && ptx == 200 {
				matchedPoint = true
			}
		}
		if !matchedPoint {
			t.Fatalf("expected traffic point with rx_values=800 tx_values=200 values=1000; body=%s", string(qBody))
		}

		for _, sortBy := range []string{"rx", "tx"} {
			dSt, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/dimensions?type=iface&sort="+sortBy, nil)
			if dSt != http.StatusOK {
				t.Fatalf("GET /v1/dimensions?type=iface&sort=%s status = %d; body=%s", sortBy, dSt, string(dBody))
			}
			items := asSlice(decodeMap(t, dBody)["items"])
			if len(items) != 1 || asString(asMap(items[0])["key"]) != "eth0" {
				t.Fatalf("unexpected iface dimensions sort=%s: %s", sortBy, string(dBody))
			}
		}
	})

	t.Run("F9_ProxyAndDevicesExposeRxTx", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f9-pd")

		batch := syntheticBatch("site-e2e", "node-f9-pd", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withDevices(map[string]any{
				"obs_ip": "203.0.113.1", "obs_mac": "02:00:00:00:00:01",
				"rx_delta": 600, "tx_delta": 300, "source": "mihomo", "outbound": "hk-01",
			}),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 600, "proxy_tx": 300}),
		)
		st, _ := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d", st)
		}

		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		outbounds := asSlice(decodeMap(t, pBody)["outbounds"])
		if len(outbounds) != 1 {
			t.Fatalf("expected 1 outbound; body=%s", string(pBody))
		}
		ob := asMap(outbounds[0])
		rawRx, _ := asUint64(ob["raw_rx"])
		rawTx, _ := asUint64(ob["raw_tx"])
		raw, _ := asUint64(ob["raw"])
		if rawRx != 600 || rawTx != 300 || raw != 900 {
			t.Fatalf("outbound rx/tx/raw = %d/%d/%d, want 600/300/900; body=%s", rawRx, rawTx, raw, string(pBody))
		}

		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs := asSlice(decodeMap(t, dBody)["devices"])
		if len(devs) != 1 {
			t.Fatalf("expected 1 device; body=%s", string(dBody))
		}
		dev := asMap(devs[0])
		devRx, _ := asUint64(dev["rx"])
		devTx, _ := asUint64(dev["tx"])
		devTot, _ := asUint64(dev["total"])
		if devRx != 600 || devTx != 300 || devTot != 900 {
			t.Fatalf("device top-level rx/tx/total = %d/%d/%d, want 600/300/900; body=%s", devRx, devTx, devTot, string(dBody))
		}
		rxTx := asMap(dev["breakdown"])
		if rxTx == nil {
			t.Fatalf("expected device breakdown; body=%s", string(dBody))
		}
		prDetail := asMap(rxTx["proxy_raw"])
		drx, _ := asUint64(prDetail["rx"])
		dtx, _ := asUint64(prDetail["tx"])
		dtot, _ := asUint64(prDetail["total"])
		if drx != 600 || dtx != 300 || dtot != 900 {
			t.Fatalf("device proxy_raw rx=%d tx=%d total=%d, want 600/300/900; body=%s", drx, dtx, dtot, string(dBody))
		}
	})

	t.Run("F9_TrafficLegacyGroupsNodeDeviceClassOutbound", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f9-leg")

		batch := syntheticBatch("site-e2e", "node-f9-leg", "boot-1", 1, time.Now().UTC(),
			withCapabilities("nlbwmon", "mihomo"),
			withDevices(map[string]any{
				"obs_ip": "203.0.113.5", "obs_mac": "02:00:00:00:00:05",
				"rx_delta": 500, "tx_delta": 100, "source": "nlbwmon",
			}),
			withProxyOutbounds(map[string]any{"name": "ss-test", "proxy_rx": 300, "proxy_tx": 100}),
		)
		st, _ := postIngestBatch(t, srv.BaseURL, tok, batch)
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d", st)
		}

		for _, grp := range []string{"node", "device", "class", "outbound"} {
			qSt, qBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group="+grp, nil)
			if qSt != http.StatusOK {
				t.Fatalf("group=%s status = %d; body=%s", grp, qSt, string(qBody))
			}
			totals := asMap(decodeMap(t, qBody)["totals"])
			if len(totals) == 0 {
				t.Fatalf("group=%s returned empty totals; body=%s", grp, string(qBody))
			}
		}
	})

	// --- F10: Time Range Filters (5 subtests) ---
	t.Run("F10_Range24h72hBuckets30m", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f10-1")

		now := time.Now().UTC()
		batch := syntheticBatch("site-e2e", "node-f10-1", "boot-1", 1, now.Add(-1*time.Hour),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 500, "tx_delta": 250}),
		)
		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, batch); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		for _, dur := range []time.Duration{24 * time.Hour, 72 * time.Hour} {
			url := fmt.Sprintf("%s/v1/traffic?from=%s&to=%s",
				srv.BaseURL,
				now.Add(-dur).Format(time.RFC3339),
				now.Format(time.RFC3339),
			)
			st, body := doJSONRequest(t, client, http.MethodGet, url, nil)
			if st != http.StatusOK {
				t.Fatalf("query %s status = %d; body=%s", dur, st, string(body))
			}
			m := decodeMap(t, body)
			bs, _ := asUint64(m["bucket_seconds"])
			tot, _ := asUint64(asMap(m["totals"])["node-f10-1"])
			if bs != 1800 || tot != 750 {
				t.Fatalf("dur=%s bucket=%d total=%d, want 1800/750", dur, bs, tot)
			}
		}
	})

	t.Run("F10_Range7dAnd30dBuckets", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "f10-rollup.db")
		srv := startServer(t, ServerOpts{DBPath: dbPath})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f10-30d")

		now := time.Now().UTC()
		bOld := syntheticBatch("site-e2e", "node-f10-30d", "boot-1", 1, now.Add(-10*24*time.Hour),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 0}),
		)
		bRec := syntheticBatch("site-e2e", "node-f10-30d", "boot-1", 2, now.Add(-48*time.Hour),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 500, "tx_delta": 0}),
		)
		if s1, b1 := postIngestBatch(t, srv.BaseURL, tok, bOld); s1 != http.StatusOK {
			t.Fatalf("ingest bOld failed: %d %s", s1, string(b1))
		}
		if s2, b2 := postIngestBatch(t, srv.BaseURL, tok, bRec); s2 != http.StatusOK {
			t.Fatalf("ingest bRec failed: %d %s", s2, string(b2))
		}
		srv.Stop()

		// Restarting on same DB triggers MaintainLedgers (RollupDaily + PurgeSamples).
		srv2 := startServer(t, ServerOpts{DBPath: dbPath})
		client2 := newCookieClient(t)
		st, _ := loginAdmin(t, client2, srv2.BaseURL, "e2e-admin-pass-1")
		if st != http.StatusOK {
			t.Fatalf("login after restart failed: %d", st)
		}

		q7 := fmt.Sprintf("%s/v1/traffic?from=%s&to=%s", srv2.BaseURL, now.Add(-7*24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
		s7, b7 := doJSONRequest(t, client2, http.MethodGet, q7, nil)
		if s7 != http.StatusOK {
			t.Fatalf("7d status = %d; body=%s", s7, string(b7))
		}
		m7 := decodeMap(t, b7)
		if bs7, _ := asUint64(m7["bucket_seconds"]); bs7 != 7200 {
			t.Fatalf("7d bucket_seconds = %d, want 7200", bs7)
		}
		if tot7, _ := asUint64(asMap(m7["totals"])["node-f10-30d"]); tot7 != 500 {
			t.Fatalf("7d total = %d, want 500", tot7)
		}

		q30 := fmt.Sprintf("%s/v1/traffic?from=%s&to=%s", srv2.BaseURL, now.Add(-30*24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
		s30, b30 := doJSONRequest(t, client2, http.MethodGet, q30, nil)
		if s30 != http.StatusOK {
			t.Fatalf("30d status = %d; body=%s", s30, string(b30))
		}
		m30 := decodeMap(t, b30)
		if bs30, _ := asUint64(m30["bucket_seconds"]); bs30 != 86400 {
			t.Fatalf("30d bucket_seconds = %d, want 86400", bs30)
		}
		if tot30, _ := asUint64(asMap(m30["totals"])["node-f10-30d"]); tot30 != 1500 {
			t.Fatalf("30d total = %d, want 1500", tot30)
		}
	})

	t.Run("F10_RangeTodayPreset", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f10-today")

		now := time.Now().UTC()
		midnightUTC := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		tzOffsetMin := 0
		if now.Sub(midnightUTC) < 2*time.Minute || midnightUTC.Add(24*time.Hour).Sub(now) < 2*time.Minute {
			// Shift away from UTC midnight so both ingest and query stay deterministically in the same local day.
			tzOffsetMin = 360
		}
		loc := time.FixedZone("today-test", tzOffsetMin*60)
		nowLocal := now.In(loc)
		midnightLocal := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)

		bYest := syntheticBatch("site-e2e", "node-f10-today", "boot-1", 1, midnightLocal.Add(-2*time.Hour).UTC(),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 9000, "tx_delta": 0}),
		)
		bToday := syntheticBatch("site-e2e", "node-f10-today", "boot-1", 2, now.Add(-10*time.Second),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 400, "tx_delta": 100}),
		)
		if s1, b1 := postIngestBatch(t, srv.BaseURL, tok, bYest); s1 != http.StatusOK {
			t.Fatalf("ingest bYest failed: %d %s", s1, string(b1))
		}
		if s2, b2 := postIngestBatch(t, srv.BaseURL, tok, bToday); s2 != http.StatusOK {
			t.Fatalf("ingest bToday failed: %d %s", s2, string(b2))
		}

		st, body := doJSONRequest(t, client, http.MethodGet, fmt.Sprintf("%s/v1/traffic?range=today&tz_offset=%d", srv.BaseURL, tzOffsetMin), nil)
		if st != http.StatusOK {
			t.Fatalf("range=today status = %d; body=%s", st, string(body))
		}
		tot, _ := asUint64(asMap(decodeMap(t, body)["totals"])["node-f10-today"])
		if tot != 500 {
			t.Fatalf("today total = %d, want 500 (excluding yesterday 9000); body=%s", tot, string(body))
		}
	})

	t.Run("F10_RangeBillingPreset", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f10-bill")

		now := time.Now().UTC()
		resetDay := 1
		var cycleStart time.Time
		if now.Day() == 1 && now.Hour() == 0 && now.Minute() < 2 {
			resetDay = 15
			prevMonth := now.AddDate(0, -1, 0)
			cycleStart = time.Date(prevMonth.Year(), prevMonth.Month(), 15, 0, 0, 0, 0, time.UTC)
		} else {
			cycleStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		}

		bSt, bBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/billing", map[string]any{
			"reset_day": resetDay, "provider_bytes": 100000,
		})
		if bSt != http.StatusOK {
			t.Fatalf("PUT billing status = %d; body=%s", bSt, string(bBody))
		}

		bPrev := syntheticBatch("site-e2e", "node-f10-bill", "boot-1", 1, cycleStart.Add(-24*time.Hour),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 5000, "tx_delta": 0}),
		)
		bCurr := syntheticBatch("site-e2e", "node-f10-bill", "boot-1", 2, now.Add(-1*time.Minute),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 700, "tx_delta": 300}),
		)
		if s1, b1 := postIngestBatch(t, srv.BaseURL, tok, bPrev); s1 != http.StatusOK {
			t.Fatalf("ingest bPrev failed: %d %s", s1, string(b1))
		}
		if s2, b2 := postIngestBatch(t, srv.BaseURL, tok, bCurr); s2 != http.StatusOK {
			t.Fatalf("ingest bCurr failed: %d %s", s2, string(b2))
		}

		st, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?range=billing", nil)
		if st != http.StatusOK {
			t.Fatalf("range=billing status = %d; body=%s", st, string(body))
		}
		tot, _ := asUint64(asMap(decodeMap(t, body)["totals"])["node-f10-bill"])
		if tot != 1000 {
			t.Fatalf("billing total = %d, want 1000; body=%s", tot, string(body))
		}
	})

	t.Run("F10_CustomFromToAndNodeFilter", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokA := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-a")
		tokB := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-b")

		now := time.Now().UTC()
		if sA, bA := postIngestBatch(t, srv.BaseURL, tokA, syntheticBatch("site-e2e", "node-a", "boot-1", 1, now.Add(-30*time.Minute),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 300, "tx_delta": 100}))); sA != http.StatusOK {
			t.Fatalf("ingest node-a failed: %d %s", sA, string(bA))
		}
		if sB, bB := postIngestBatch(t, srv.BaseURL, tokB, syntheticBatch("site-e2e", "node-b", "boot-1", 1, now.Add(-30*time.Minute),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 900, "tx_delta": 100}))); sB != http.StatusOK {
			t.Fatalf("ingest node-b failed: %d %s", sB, string(bB))
		}

		url := fmt.Sprintf("%s/v1/traffic?from=%s&to=%s&node=node-a",
			srv.BaseURL, now.Add(-2*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
		st, body := doJSONRequest(t, client, http.MethodGet, url, nil)
		if st != http.StatusOK {
			t.Fatalf("custom query status = %d; body=%s", st, string(body))
		}
		totals := asMap(decodeMap(t, body)["totals"])
		if gotA, _ := asUint64(totals["node-a"]); gotA != 400 {
			t.Fatalf("node-a = %d, want 400", gotA)
		}
		if _, hasB := totals["node-b"]; hasB {
			t.Fatalf("node-b should be excluded by &node=node-a filter; body=%s", string(body))
		}
	})

	// --- F11: Keyword / Regex / Auto-Extract Multiplier Engine (5 subtests) ---
	t.Run("F11_RulesCRUDLifecycle", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "IEPL 2x", "match_type": "keyword", "pattern": "IEPL", "factor": 2.0, "priority": 100, "enabled": true,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("POST /v1/proxy/rules status = %d; body=%s", cSt, string(cBody))
		}
		id := extractRuleID(t, cBody)

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/rules", nil)
		if gSt != http.StatusOK || !strings.Contains(string(gBody), "IEPL 2x") {
			t.Fatalf("GET /v1/proxy/rules = %d %s", gSt, string(gBody))
		}

		uSt, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/rules/"+id, map[string]any{
			"name": "IEPL 2.5x", "match_type": "keyword", "pattern": "IEPL", "factor": 2.5, "priority": 100, "enabled": true,
		})
		if uSt != http.StatusOK {
			t.Fatalf("PUT /v1/proxy/rules/%s status = %d", id, uSt)
		}

		dSt, _ := doJSONRequest(t, client, http.MethodDelete, srv.BaseURL+"/v1/proxy/rules/"+id, nil)
		if dSt != http.StatusOK {
			t.Fatalf("DELETE /v1/proxy/rules/%s status = %d", id, dSt)
		}
		_, gBody2 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/rules", nil)
		if len(asSlice(decodeMap(t, gBody2)["rules"])) != 0 {
			t.Fatalf("expected 0 rules after delete; body=%s", string(gBody2))
		}
	})

	t.Run("F11_UnmatchedDefault1xBadge", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f11-def")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f11-def", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "US-Plain", "proxy_rx": 700, "proxy_tx": 300}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		pSt, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		if pSt != http.StatusOK {
			t.Fatalf("GET /v1/proxy status = %d; body=%s", pSt, string(pBody))
		}
		obs := asSlice(decodeMap(t, pBody)["outbounds"])
		if len(obs) != 1 {
			t.Fatalf("expected 1 outbound; body=%s", string(pBody))
		}
		ob := asMap(obs[0])
		factor, _ := asFloat64(ob["factor"])
		adj, _ := asUint64(ob["adjusted"])
		if factor != 1.0 || adj != 1000 || asString(ob["match_source"]) != "default" {
			t.Fatalf("expected default 1.0x badge (factor=1, adjusted=1000, match_source=default); got=%s", string(pBody))
		}
	})

	t.Run("F11_RegexAndAutoExtractLivePreview", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f11-prev")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f11-prev", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyNodes("HK-IEPL-01", "SG-x2.5", "JP-Plain"),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}
		if r1St, r1Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "Regex IEPL", "match_type": "regex", "pattern": `IEPL`, "factor": 3.0, "priority": 100, "enabled": true,
		}); r1St != http.StatusOK && r1St != http.StatusCreated {
			t.Fatalf("POST regex rule status = %d; body=%s", r1St, string(r1Body))
		}
		if r2St, r2Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "Auto", "match_type": "auto_extract", "pattern": "", "factor": 1.0, "priority": 10, "enabled": true,
		}); r2St != http.StatusOK && r2St != http.StatusCreated {
			t.Fatalf("POST auto_extract rule status = %d; body=%s", r2St, string(r2Body))
		}

		st, body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/preview", map[string]any{})
		if st != http.StatusOK {
			t.Fatalf("POST /v1/proxy/rules/preview status = %d; body=%s", st, string(body))
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
		if got["HK-IEPL-01"] != 3.0 || got["SG-x2.5"] != 2.5 || got["JP-Plain"] != 1.0 || sources["JP-Plain"] != "default" {
			t.Fatalf("unexpected preview factors=%v sources=%v; body=%s", got, sources, string(body))
		}
	})

	t.Run("F11_RetroactiveRecalculationAfterRuleEdit", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f11-retro")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f11-retro", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "HK-IEPL-01", "proxy_rx": 800, "proxy_tx": 200}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		// Initially 1.0x default -> 1000
		ov0St, ov0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ov0St != http.StatusOK {
			t.Fatalf("overview status = %d", ov0St)
		}
		if adj0, _ := asUint64(asMap(decodeMap(t, ov0)["bytes"])["proxy_adjusted"]); adj0 != 1000 {
			t.Fatalf("initial proxy_adjusted = %d, want 1000", adj0)
		}

		// Add rule IEPL -> 2.5x; historical queries update immediately.
		rSt, rBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "IEPL", "match_type": "keyword", "pattern": "IEPL", "factor": 2.5, "priority": 100, "enabled": true,
		})
		if rSt != http.StatusOK && rSt != http.StatusCreated {
			t.Fatalf("POST rule status = %d; body=%s", rSt, string(rBody))
		}
		ruleID := extractRuleID(t, rBody)

		assertAdjusted := func(want uint64) {
			t.Helper()
			ovSt, ov := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			if ovSt != http.StatusOK {
				t.Fatalf("overview status = %d", ovSt)
			}
			if got, _ := asUint64(asMap(decodeMap(t, ov)["bytes"])["proxy_adjusted"]); got != want {
				t.Fatalf("overview proxy_adjusted = %d, want %d", got, want)
			}
			prSt, pr := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
			if prSt != http.StatusOK {
				t.Fatalf("proxy status = %d", prSt)
			}
			if got, _ := asUint64(asMap(decodeMap(t, pr)["proxy"])["proxy_adjusted"]); got != want {
				t.Fatalf("proxy proxy_adjusted = %d, want %d", got, want)
			}
			trSt, tr := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=outbound&class=proxy_adjusted", nil)
			if trSt != http.StatusOK {
				t.Fatalf("traffic status = %d", trSt)
			}
			if got, _ := asUint64(asMap(decodeMap(t, tr)["totals"])["HK-IEPL-01"]); got != want {
				t.Fatalf("traffic HK-IEPL-01 proxy_adjusted = %d, want %d", got, want)
			}
		}
		assertAdjusted(2500)

		// Update rule to 3.0x -> immediately 3000 without new ingest.
		uSt, uBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/rules/"+ruleID, map[string]any{
			"name": "IEPL", "match_type": "keyword", "pattern": "IEPL", "factor": 3.0, "priority": 100, "enabled": true,
		})
		if uSt != http.StatusOK {
			t.Fatalf("PUT rule status = %d; body=%s", uSt, string(uBody))
		}
		assertAdjusted(3000)
	})

	t.Run("F11_RulesJSONExportImportRoundTrip", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f11-io")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f11-io", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "HK-IEPL-01", "proxy_rx": 1000, "proxy_tx": 0}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		s1, r1 := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "IEPL", "match_type": "keyword", "pattern": "IEPL", "factor": 2.0, "priority": 100, "enabled": true,
		})
		if s1 != http.StatusOK && s1 != http.StatusCreated {
			t.Fatalf("POST rule 1 status = %d; body=%s", s1, string(r1))
		}
		s2, r2 := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "US", "match_type": "keyword", "pattern": "US", "factor": 0.5, "priority": 50, "enabled": true,
		})
		if s2 != http.StatusOK && s2 != http.StatusCreated {
			t.Fatalf("POST rule 2 status = %d; body=%s", s2, string(r2))
		}
		id1 := extractRuleID(t, r1)
		id2 := extractRuleID(t, r2)

		exSt, exBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/rules/export", nil)
		if exSt != http.StatusOK {
			t.Fatalf("export status = %d; body=%s", exSt, string(exBody))
		}

		if d1St, d1Body := doJSONRequest(t, client, http.MethodDelete, srv.BaseURL+"/v1/proxy/rules/"+id1, nil); d1St != http.StatusOK {
			t.Fatalf("DELETE rule 1 status = %d; body=%s", d1St, string(d1Body))
		}
		if d2St, d2Body := doJSONRequest(t, client, http.MethodDelete, srv.BaseURL+"/v1/proxy/rules/"+id2, nil); d2St != http.StatusOK {
			t.Fatalf("DELETE rule 2 status = %d; body=%s", d2St, string(d2Body))
		}

		lsSt, lsBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/rules", nil)
		if lsSt != http.StatusOK || len(asSlice(decodeMap(t, lsBody)["rules"])) != 0 {
			t.Fatalf("expected 0 rules after delete before import: %d %s", lsSt, string(lsBody))
		}
		ovPreSt, ovPre := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovPreSt != http.StatusOK {
			t.Fatalf("overview pre-import status = %d", ovPreSt)
		}
		if adjPre, _ := asUint64(asMap(decodeMap(t, ovPre)["bytes"])["proxy_adjusted"]); adjPre != 1000 {
			t.Fatalf("proxy_adjusted after delete = %d, want 1000 before import", adjPre)
		}

		imSt, _, imBody := doRawRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/import", exBody, map[string]string{
			"Content-Type": "application/json",
		})
		if imSt != http.StatusOK {
			t.Fatalf("import status = %d; body=%s", imSt, string(imBody))
		}

		ovSt, ov := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovSt != http.StatusOK {
			t.Fatalf("overview post-import status = %d", ovSt)
		}
		if adj, _ := asUint64(asMap(decodeMap(t, ov)["bytes"])["proxy_adjusted"]); adj != 2000 {
			t.Fatalf("restored proxy_adjusted = %d, want 2000", adj)
		}
	})

	// --- F12: Custom Watchlist (5 subtests) ---
	t.Run("F12_WatchCRUDAndMatchers", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name": "AI Coding",
			"matchers": map[string]any{
				"group":   map[string]any{"type": "exact", "values": []string{"AI"}},
				"process": map[string]any{"type": "exact", "values": []string{"Cursor"}},
			},
			"quota_value": 10, "quota_unit": "GB", "reset_type": "monthly", "reset_day": 1, "pinned": true,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch status = %d; body=%s", cSt, string(cBody))
		}
		id := fmt.Sprintf("%v", decodeMap(t, cBody)["id"])

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/watch", nil)
		if gSt != http.StatusOK || !strings.Contains(string(gBody), "AI Coding") {
			t.Fatalf("GET /v1/watch = %d %s", gSt, string(gBody))
		}

		uSt, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/watch/"+id, map[string]any{
			"name": "AI Coding Renamed",
			"matchers": map[string]any{
				"group": map[string]any{"type": "exact", "values": []string{"AI"}},
			},
			"quota_value": 20, "quota_unit": "GB", "reset_type": "monthly", "reset_day": 1, "pinned": true,
		})
		if uSt != http.StatusOK {
			t.Fatalf("PUT /v1/watch/%s status = %d", id, uSt)
		}

		dSt, _ := doJSONRequest(t, client, http.MethodDelete, srv.BaseURL+"/v1/watch/"+id, nil)
		if dSt != http.StatusOK {
			t.Fatalf("DELETE /v1/watch/%s status = %d", id, dSt)
		}
	})

	t.Run("F12_WatchRetroactiveTrendAndRxTx", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f12-retro")

		now := time.Now().UTC()
		if s1, b1 := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f12-retro", "boot-1", 1, now.Add(-2*time.Hour),
			withCapabilities("mihomo"),
			withDimensions(map[string]any{"type": "process", "key": "Cursor", "rx_delta": 600, "tx_delta": 200}),
		)); s1 != http.StatusOK {
			t.Fatalf("ingest 1 failed: %d %s", s1, string(b1))
		}
		if s2, b2 := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f12-retro", "boot-1", 2, now.Add(-30*time.Minute),
			withCapabilities("mihomo"),
			withDimensions(map[string]any{"type": "process", "key": "Cursor", "rx_delta": 400, "tx_delta": 100}),
		)); s2 != http.StatusOK {
			t.Fatalf("ingest 2 failed: %d %s", s2, string(b2))
		}

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name": "Cursor Retro",
			"matchers": map[string]any{
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
		item := asMap(items[0])
		rx, _ := asUint64(item["rx"])
		tx, _ := asUint64(item["tx"])
		if rx != 1000 || tx != 300 || len(asSlice(item["series"])) == 0 {
			t.Fatalf("retroactive watch rx=%d tx=%d seriesLen=%d; body=%s", rx, tx, len(asSlice(item["series"])), string(gBody))
		}
	})

	t.Run("F12_WatchOverrideMultiplier", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f12-ov")

		if rSt, rBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules", map[string]any{
			"name": "HK2x", "match_type": "exact", "pattern": "hk-01", "factor": 2.0, "priority": 100, "enabled": true,
		}); rSt != http.StatusOK && rSt != http.StatusCreated {
			t.Fatalf("POST rule status = %d; body=%s", rSt, string(rBody))
		}
		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f12-ov", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 700, "proxy_tx": 300}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		if w1St, w1Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":     "Inherit",
			"matchers": map[string]any{"outbound": map[string]any{"type": "exact", "values": []string{"hk-01"}}},
		}); w1St != http.StatusOK && w1St != http.StatusCreated {
			t.Fatalf("POST watch Inherit status = %d; body=%s", w1St, string(w1Body))
		}
		if w2St, w2Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":            "Override1x",
			"matchers":        map[string]any{"outbound": map[string]any{"type": "exact", "values": []string{"hk-01"}}},
			"override_factor": 1.0,
		}); w2St != http.StatusOK && w2St != http.StatusCreated {
			t.Fatalf("POST watch Override1x status = %d; body=%s", w2St, string(w2Body))
		}

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/watch", nil)
		if gSt != http.StatusOK {
			t.Fatalf("GET /v1/watch status = %d; body=%s", gSt, string(gBody))
		}
		got := map[string]uint64{}
		for _, it := range asSlice(decodeMap(t, gBody)["items"]) {
			m := asMap(it)
			adj, _ := asUint64(m["adjusted"])
			got[asString(m["name"])] = adj
		}
		if got["Inherit"] != 2000 || got["Override1x"] != 1000 {
			t.Fatalf("watch override mismatch: %v; body=%s", got, string(gBody))
		}
	})

	t.Run("F12_WatchQuotaProgressAndOverQuota", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f12-quota")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f12-quota", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withDimensions(map[string]any{"type": "process", "key": "codex", "rx_delta": 2097152, "tx_delta": 0}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}
		if wSt, wBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":        "Codex Quota",
			"matchers":    map[string]any{"process": map[string]any{"type": "exact", "values": []string{"codex"}}},
			"quota_value": 1, "quota_unit": "MB", "reset_type": "none",
		}); wSt != http.StatusOK && wSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch status = %d; body=%s", wSt, string(wBody))
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
		qb, _ := asUint64(it["quota_bytes"])
		ratio, _ := asFloat64(it["used_ratio"])
		if (qb != 1048576 && qb != 1000000) || ratio < 2.0 || !asBool(it["over_quota"]) {
			t.Fatalf("unexpected quota status qb=%d ratio=%v over=%v; body=%s", qb, ratio, it["over_quota"], string(gBody))
		}
	})

	t.Run("F12_WatchPinnedAppearsOnOverview", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f12-pin")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f12-pin", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withDimensions(map[string]any{"type": "iface", "key": "wg0", "rx_delta": 500, "tx_delta": 500}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		if w1St, w1Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name": "PinnedWG", "matchers": map[string]any{"iface": map[string]any{"type": "exact", "values": []string{"wg0"}}}, "pinned": true,
		}); w1St != http.StatusOK && w1St != http.StatusCreated {
			t.Fatalf("POST watch PinnedWG status = %d; body=%s", w1St, string(w1Body))
		}
		if w2St, w2Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name": "UnpinnedWG", "matchers": map[string]any{"iface": map[string]any{"type": "exact", "values": []string{"wg0"}}}, "pinned": false,
		}); w2St != http.StatusOK && w2St != http.StatusCreated {
			t.Fatalf("POST watch UnpinnedWG status = %d; body=%s", w2St, string(w2Body))
		}

		ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovSt != http.StatusOK {
			t.Fatalf("GET /v1/overview status = %d; body=%s", ovSt, string(ovBody))
		}
		pinned := asSlice(decodeMap(t, ovBody)["pinned_watch"])
		if len(pinned) != 1 || asString(asMap(pinned[0])["name"]) != "PinnedWG" {
			t.Fatalf("expected only PinnedWG in overview.pinned_watch; body=%s", string(ovBody))
		}
	})

	// --- F13: Multi-Provider Quota Reconciliation (5 subtests) ---
	t.Run("F13_AutoSyncMihomoProviders", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f13-auto")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f13-auto", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 700, "proxy_tx": 300}),
			withProviders(map[string]any{
				"name": "AirportA", "vehicle_type": "HTTP",
				"upload": 100, "download": 900, "total": 10000, "expire": 1893456000,
				"proxies": []string{"hk-01"},
			}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		st, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if st != http.StatusOK {
			t.Fatalf("GET /v1/proxy/providers status = %d; body=%s", st, string(body))
		}
		provs := asSlice(decodeMap(t, body)["providers"])
		if len(provs) != 1 {
			t.Fatalf("expected 1 provider; body=%s", string(body))
		}
		p := asMap(provs[0])
		used, _ := asUint64(p["used_bytes"])
		tot, _ := asUint64(p["total_bytes"])
		loc, _ := asUint64(p["local_adjusted"])
		if asString(p["name"]) != "AirportA" || used != 1000 || tot != 10000 || loc != 1000 {
			t.Fatalf("unexpected provider sync: %s", string(body))
		}
	})

	t.Run("F13_ManualProviderMBGBTBCRUD", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/providers", map[string]any{
			"name": "ManualSub", "unit": "GB", "total_value": 50, "used_value": 5, "reset_day": 15,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("POST /v1/proxy/providers status = %d; body=%s", cSt, string(cBody))
		}
		id := fmt.Sprintf("%v", decodeMap(t, cBody)["id"])

		_, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		provs := asSlice(decodeMap(t, gBody)["providers"])
		if len(provs) != 1 {
			t.Fatalf("expected 1 provider; body=%s", string(gBody))
		}
		tot, _ := asUint64(asMap(provs[0])["total_bytes"])
		if tot != 50*(1<<30) && tot != 50*1000*1000*1000 {
			t.Fatalf("total_bytes = %d, want 50 GiB or 50 GB", tot)
		}

		uSt, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/providers/"+id, map[string]any{
			"name": "ManualSub", "unit": "TB", "total_value": 1, "used_value": 0, "reset_day": 15,
		})
		if uSt != http.StatusOK {
			t.Fatalf("PUT /v1/proxy/providers/%s status = %d", id, uSt)
		}
		dSt, _ := doJSONRequest(t, client, http.MethodDelete, srv.BaseURL+"/v1/proxy/providers/"+id, nil)
		if dSt != http.StatusOK {
			t.Fatalf("DELETE /v1/proxy/providers/%s status = %d", id, dSt)
		}
	})

	t.Run("F13_ProviderDriftCalculationAndAlert", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f13-drift")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f13-drift", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 1500, "proxy_tx": 0}),
			withProviders(map[string]any{
				"name": "DriftAirport", "vehicle_type": "HTTP",
				"upload": 200, "download": 800, "total": 10000, "expire": 1893456000,
				"proxies": []string{"hk-01"},
			}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		pSt, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if pSt != http.StatusOK {
			t.Fatalf("GET /v1/proxy/providers status = %d; body=%s", pSt, string(pBody))
		}
		p := asMap(asSlice(decodeMap(t, pBody)["providers"])[0])
		ratio, _ := asFloat64(p["drift_ratio"])
		if ratio < 0.45 || !asBool(p["drift_alert"]) {
			t.Fatalf("expected drift_ratio ~ 0.5 and drift_alert=true; body=%s", string(pBody))
		}

		_, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		foundDrift := false
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			if asString(asMap(it)["kind"]) == "drift" {
				foundDrift = true
			}
		}
		if !foundDrift {
			t.Fatalf("expected drift alert in /v1/alerts; body=%s", string(aBody))
		}
	})

	t.Run("F13_LegacyBillingEndpointPreserved", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "f13-billing.db")
		srv := startServer(t, ServerOpts{DBPath: dbPath})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f13-bill")

		// Explicitly configure hk-01=1.0 so both v0.1 and v0.2 compute proxy_adjusted=120.
		mSt, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/proxy/multipliers", map[string]any{
			"name": "hk-01", "factor": 1.0,
		})
		if mSt != http.StatusOK {
			t.Fatalf("PUT /v1/proxy/multipliers status = %d", mSt)
		}

		iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f13-bill", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 100, "proxy_tx": 20}),
		))
		if iSt != http.StatusOK {
			t.Fatalf("ingest status = %d; body=%s", iSt, string(iBody))
		}
		srv.Stop()

		// Restart triggers MaintainLedgers so ledger_daily reflects today's ingested sample for BillingStatus.
		srv2 := startServer(t, ServerOpts{DBPath: dbPath})
		client2 := newCookieClient(t)
		if lSt, _ := loginAdmin(t, client2, srv2.BaseURL, "e2e-admin-pass-1"); lSt != http.StatusOK {
			t.Fatalf("re-login status = %d", lSt)
		}

		bSt, _ := doJSONRequest(t, client2, http.MethodPut, srv2.BaseURL+"/v1/proxy/billing", map[string]any{
			"reset_day": 1, "provider_bytes": 100,
		})
		if bSt != http.StatusOK {
			t.Fatalf("PUT /v1/proxy/billing status = %d", bSt)
		}

		_, pBody := doJSONRequest(t, client2, http.MethodGet, srv2.BaseURL+"/v1/proxy", nil)
		bill := asMap(decodeMap(t, pBody)["billing"])
		loc, _ := asUint64(bill["local_bytes"])
		prov, _ := asUint64(bill["provider_bytes"])
		ratio, _ := asFloat64(bill["ratio"])
		if loc != 120 || prov != 100 || ratio < 0.19 {
			t.Fatalf("unexpected billing: %s", string(pBody))
		}

		_, aBody := doJSONRequest(t, client2, http.MethodGet, srv2.BaseURL+"/v1/alerts", nil)
		foundDrift := false
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			if asString(asMap(it)["kind"]) == "drift" {
				foundDrift = true
			}
		}
		if !foundDrift {
			t.Fatalf("expected drift alert from legacy billing; alerts=%s", string(aBody))
		}
	})

	t.Run("F13_CompatibleAndSharedProviderHandling", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok1 := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f13-s1")
		tok2 := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f13-s2")

		shared := map[string]any{
			"name": "SharedSub", "vehicle_type": "HTTP",
			"upload": 1000, "download": 4000, "total": 100000, "expire": 1893456000,
			"proxies": []string{"hk-01"},
		}
		compat := map[string]any{
			"name": "default", "vehicle_type": "Compatible",
			"upload": 0, "download": 0, "total": 0, "expire": 0,
			"proxies": []string{},
		}
		if s1, b1 := postIngestBatch(t, srv.BaseURL, tok1, syntheticBatch("site-e2e", "node-f13-s1", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"), withProviders(shared, compat),
		)); s1 != http.StatusOK {
			t.Fatalf("ingest 1 failed: %d %s", s1, string(b1))
		}
		if s2, b2 := postIngestBatch(t, srv.BaseURL, tok2, syntheticBatch("site-e2e", "node-f13-s2", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"), withProviders(shared, compat),
		)); s2 != http.StatusOK {
			t.Fatalf("ingest 2 failed: %d %s", s2, string(b2))
		}

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if gSt != http.StatusOK {
			t.Fatalf("GET /v1/proxy/providers status = %d; body=%s", gSt, string(gBody))
		}
		var sharedCount int
		var totalQuotaSum uint64
		for _, it := range asSlice(decodeMap(t, gBody)["providers"]) {
			p := asMap(it)
			if asString(p["name"]) == "SharedSub" {
				sharedCount++
			}
			if asString(p["vehicle_type"]) == "Compatible" {
				t.Fatalf("Compatible non-quota provider should be filtered; body=%s", string(gBody))
			}
			tb, _ := asUint64(p["total_bytes"])
			totalQuotaSum += tb
		}
		if sharedCount != 1 || totalQuotaSum != 100000 {
			t.Fatalf("shared provider duplicated (count=%d sum=%d); body=%s", sharedCount, totalQuotaSum, string(gBody))
		}
	})

	// --- F14: Embedded Vite + React + TS Web UI (5 subtests) ---
	t.Run("F14_EmbeddedIndexServedAtRoot", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		st, hdr, body := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/", nil, nil)
		if st != http.StatusOK {
			t.Fatalf("GET / status = %d", st)
		}
		if !strings.Contains(hdr.Get("Content-Type"), "text/html") {
			t.Fatalf("Content-Type = %q, want text/html", hdr.Get("Content-Type"))
		}
		if !strings.Contains(string(body), "LanTally") {
			t.Fatalf("index.html missing LanTally title/brand")
		}
	})

	t.Run("F14_StaticAssetsServedWithMimeTypes", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		_, _, htmlBytes := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/", nil, nil)
		html := string(htmlBytes)

		re := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
		matches := re.FindAllStringSubmatch(html, -1)
		if len(matches) == 0 {
			t.Fatalf("expected Vite /assets/* references in index.html; got:\n%s", html)
		}
		for _, m := range matches {
			assetPath := m[1]
			st, hdr, body := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+assetPath, nil, nil)
			if st != http.StatusOK || len(body) == 0 {
				t.Fatalf("asset %s failed: status=%d len=%d", assetPath, st, len(body))
			}
			ct := hdr.Get("Content-Type")
			if strings.HasSuffix(assetPath, ".js") && !strings.Contains(ct, "javascript") {
				t.Fatalf("asset %s Content-Type = %q, want javascript", assetPath, ct)
			}
			if strings.HasSuffix(assetPath, ".css") && !strings.Contains(ct, "text/css") {
				t.Fatalf("asset %s Content-Type = %q, want text/css", assetPath, ct)
			}
		}
	})

	t.Run("F14_SPAPagesPresentInBundle", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		_, _, htmlBytes := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/", nil, nil)
		re := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`)
		matches := re.FindAllStringSubmatch(string(htmlBytes), -1)
		if len(matches) == 0 {
			t.Fatalf("no /assets/*.js bundle found in index.html:\n%s", string(htmlBytes))
		}

		var bundle strings.Builder
		for _, m := range matches {
			st, _, js := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+m[1], nil, nil)
			if st == http.StatusOK {
				bundle.Write(js)
			}
		}
		jsText := bundle.String()
		requiredViews := [][]string{
			{"Overview", "概览"},
			{"Proxy", "代理"},
			{"Watchlist", "关注"},
			{"Apps", "应用"},
			{"Devices", "设备"},
			{"Nodes", "节点"},
			{"Alerts", "告警"},
		}
		for _, pair := range requiredViews {
			if !strings.Contains(jsText, pair[0]) && !strings.Contains(jsText, pair[1]) {
				t.Fatalf("SPA bundle missing view %v", pair)
			}
		}
	})

	t.Run("F14_ProtectedAPIsRequireAuthNotBypassed", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		setupAdmin(t, newCookieClient(t), srv.BaseURL, "e2e-admin-pass-1")

		paths := []string{"/v1/overview", "/v1/traffic", "/v1/proxy", "/v1/devices", "/v1/nodes", "/v1/alerts", "/v1/settings", "/v1/backup"}
		for _, p := range paths {
			st, _ := doJSONRequest(t, nil, http.MethodGet, srv.BaseURL+p, nil)
			if st != http.StatusUnauthorized {
				t.Fatalf("unauthenticated GET %s = %d, want 401", p, st)
			}
		}
	})

	t.Run("F14_SetupStatusDrivesInitialGate", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		st1, b1 := doJSONRequest(t, nil, http.MethodGet, srv.BaseURL+"/v1/setup", nil)
		if st1 != http.StatusOK || !asBool(decodeMap(t, b1)["needed"]) {
			t.Fatalf("initial GET /v1/setup = %d %s, want needed=true", st1, string(b1))
		}
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		st2, b2 := doJSONRequest(t, nil, http.MethodGet, srv.BaseURL+"/v1/setup", nil)
		if st2 != http.StatusOK || asBool(decodeMap(t, b2)["needed"]) {
			t.Fatalf("post-setup GET /v1/setup = %d %s, want needed=false", st2, string(b2))
		}
	})

	// --- F15: Container Entrypoint & Multi-Arch Build (5 subtests) ---
	t.Run("F15_ServerVersionAndListenFlags", func(t *testing.T) {
		out, err := exec.Command(serverBinPath, "-version").CombinedOutput()
		if err != nil {
			t.Fatalf("lantally-server -version failed: %v (%s)", err, string(out))
		}
		if strings.TrimSpace(string(out)) == "" {
			t.Fatalf("lantally-server -version printed empty output")
		}

		srv := startServer(t, ServerOpts{})
		st, _, body := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/healthz", nil, nil)
		if st != http.StatusOK || string(body) != "ok\n" {
			t.Fatalf("GET /healthz = %d %q", st, string(body))
		}
	})

	t.Run("F15_ServerDemoModeSeedsSyntheticData", func(t *testing.T) {
		srv := startServer(t, ServerOpts{Demo: true, BootstrapPassword: "e2e-demo-pass-1"})
		client := newCookieClient(t)
		st, _ := loginAdmin(t, client, srv.BaseURL, "e2e-demo-pass-1")
		if st != http.StatusOK {
			t.Fatalf("login with bootstrap password failed: %d", st)
		}

		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		ov := decodeMap(t, ovBody)
		batches, _ := asUint64(ov["ingest_batches"])
		tot, _ := asUint64(asMap(ov["bytes"])["total"])
		if batches != 24 || tot == 0 {
			t.Fatalf("demo overview batches=%d total=%d; body=%s", batches, tot, string(ovBody))
		}

		_, nBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes", nil)
		if !strings.Contains(string(nBody), "demo-gateway") {
			t.Fatalf("demo nodes missing demo-gateway; body=%s", string(nBody))
		}
	})

	t.Run("F15_DockerfileEntrypointIsServerAndHasWinArm64", func(t *testing.T) {
		root := repoRoot(t)
		dfBytes, err := os.ReadFile(filepath.Join(root, "deploy", "docker", "Dockerfile"))
		if err != nil {
			t.Fatalf("read Dockerfile: %v", err)
		}
		df := string(dfBytes)
		if !strings.Contains(df, `ENTRYPOINT ["/usr/local/bin/lantally-server"]`) {
			t.Fatalf("Dockerfile missing ENTRYPOINT [\"/usr/local/bin/lantally-server\"]")
		}
		if !strings.Contains(df, "npm run build") || !strings.Contains(df, "web/dist") {
			t.Fatalf("Dockerfile missing Node/Vite frontend build stage (npm run build + web/dist)")
		}
		if !strings.Contains(df, "windows/arm64") || !strings.Contains(df, "GOOS=windows GOARCH=arm64") {
			t.Fatalf("Dockerfile missing windows/arm64 agent build")
		}

		shBytes, err := os.ReadFile(filepath.Join(root, "scripts", "build-release.sh"))
		if err != nil {
			t.Fatalf("read build-release.sh: %v", err)
		}
		sh := string(shBytes)
		if !strings.Contains(sh, "windows_arm64") || !regexp.MustCompile(`windows\s+arm64`).MatchString(sh) {
			t.Fatalf("scripts/build-release.sh missing windows arm64 build target")
		}
	})

	t.Run("F15_OptionalExternalMetricsPush", func(t *testing.T) {
		vm := newMockVM(t)
		srv := startServer(t, ServerOpts{MetricsURL: vm.URL()})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f15-vm")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f15-vm", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 500, "tx_delta": 250}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		reqs := vm.WaitForSamples(t, 5*time.Second, 1)
		if !strings.Contains(reqs[0].RawBody, "lantally_bytes_total") {
			t.Fatalf("VM payload missing lantally_bytes_total: %s", reqs[0].RawBody)
		}
	})

	t.Run("F15_ServerRunsWithoutExternalVM", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f15-novm")

		st, _ := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f15-novm", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 400, "tx_delta": 100}),
		))
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d", st)
		}
		ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovSt != http.StatusOK {
			t.Fatalf("overview failed: %d", ovSt)
		}
		if tot, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"]); tot != 500 {
			t.Fatalf("total = %d, want 500", tot)
		}
	})

	// --- F16: Preserve v0.1 Core & Regressions (6 subtests) ---
	t.Run("F16_SetupLoginLogoutFlow", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		if st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil); st != http.StatusOK {
			t.Fatalf("authenticated overview = %d, want 200", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/logout", nil); st != http.StatusOK {
			t.Fatalf("logout = %d, want 200", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil); st != http.StatusUnauthorized {
			t.Fatalf("overview after logout = %d, want 401", st)
		}
		if st, _ := loginAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1"); st != http.StatusOK {
			t.Fatalf("re-login = %d, want 200", st)
		}
	})

	t.Run("F16_ClaimCodeAndInstallScripts", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		claim := createClaim(t, client, srv.BaseURL, "site-e2e", "node-f16-claim", true)
		code := asString(claim["code"])
		if len(code) != 8 {
			t.Fatalf("claim code = %q, want 8 chars", code)
		}

		shSt, shHdr, shBody := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/install.sh", nil, nil)
		if shSt != http.StatusOK || !strings.Contains(shHdr.Get("Content-Type"), "text/x-shellscript") || !strings.Contains(string(shBody), srv.BaseURL) {
			t.Fatalf("GET /install.sh failed: %d ct=%q", shSt, shHdr.Get("Content-Type"))
		}
		psSt, psHdr, psBody := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/install.ps1", nil, nil)
		if psSt != http.StatusOK || !strings.Contains(psHdr.Get("Content-Type"), "text/plain") || !strings.Contains(string(psBody), srv.BaseURL) {
			t.Fatalf("GET /install.ps1 failed: %d ct=%q", psSt, psHdr.Get("Content-Type"))
		}

		rSt, rMap := redeemClaim(t, srv.BaseURL, code)
		if rSt != http.StatusOK || !strings.HasPrefix(asString(rMap["token"]), "lt_") || asString(rMap["node_id"]) != "node-f16-claim" {
			t.Fatalf("redeem claim failed: %d %v", rSt, rMap)
		}
	})

	t.Run("F16_AgentBinaryDownloadHandler", func(t *testing.T) {
		agentDir := t.TempDir()
		linPath := filepath.Join(agentDir, "linux", "amd64", "lantally-agent")
		winPath := filepath.Join(agentDir, "windows", "arm64", "lantally-agent.exe")
		if err := os.MkdirAll(filepath.Dir(linPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(winPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(linPath, []byte("synthetic-linux-amd64-elf"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(winPath, []byte("synthetic-windows-arm64-pe"), 0o644); err != nil {
			t.Fatal(err)
		}

		srv := startServer(t, ServerOpts{AgentDir: agentDir})
		st1, _, b1 := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/agents/linux/amd64", nil, nil)
		st2, _, b2 := doRawRequest(t, nil, http.MethodGet, srv.BaseURL+"/agents/windows/arm64", nil, nil)
		if st1 != http.StatusOK || string(b1) != "synthetic-linux-amd64-elf" {
			t.Fatalf("linux/amd64 = %d %q", st1, string(b1))
		}
		if st2 != http.StatusOK || string(b2) != "synthetic-windows-arm64-pe" {
			t.Fatalf("windows/arm64 = %d %q", st2, string(b2))
		}
	})

	t.Run("F16_DeviceIdentityRenameMergeUnmerge", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f16-dev")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f16-dev", "boot-1", 1, time.Now().UTC(),
			withCapabilities("nlbwmon"),
			withDevices(
				map[string]any{"obs_ip": "203.0.113.11", "obs_mac": "02:00:00:00:00:11", "rx_delta": 100, "tx_delta": 50, "source": "nlbwmon"},
				map[string]any{"obs_ip": "203.0.113.12", "obs_mac": "02:00:00:00:00:12", "rx_delta": 200, "tx_delta": 50, "source": "nlbwmon"},
			),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs := asSlice(decodeMap(t, dBody)["devices"])
		if len(devs) != 2 {
			t.Fatalf("expected 2 devices, got %d", len(devs))
		}
		id1 := asString(asMap(devs[0])["id"])
		id2 := asString(asMap(devs[1])["id"])

		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/devices/"+id1, map[string]string{"name": "Workstation-Test"}); st != http.StatusOK {
			t.Fatalf("rename failed: %d", st)
		}
		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+id1+"/merge", map[string]string{"other_id": id2, "reason": "same host"}); st != http.StatusOK {
			t.Fatalf("merge failed: %d", st)
		}

		_, detBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices/"+id1, nil)
		det := decodeMap(t, detBody)
		if asString(asMap(det["device"])["name"]) != "Workstation-Test" || len(asSlice(det["merged_from"])) != 1 {
			t.Fatalf("unexpected merged device detail: %s", string(detBody))
		}
		_, dBody2 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		if len(asSlice(decodeMap(t, dBody2)["devices"])) != 1 {
			t.Fatalf("expected 1 canonical device after merge")
		}

		if st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/devices/"+id2+"/unmerge", map[string]any{}); st != http.StatusOK {
			t.Fatalf("unmerge failed: %d", st)
		}
		_, dBody3 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		if len(asSlice(decodeMap(t, dBody3)["devices"])) != 2 {
			t.Fatalf("expected 2 devices after unmerge")
		}
	})

	t.Run("F16_NodeEnrollDetailAndRevoke", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f16-rev")

		if iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f16-rev", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"), withInterfaces(map[string]any{"name": "eth0", "rx_delta": 100, "tx_delta": 50}),
		)); iSt != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", iSt, string(iBody))
		}

		st, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-f16-rev", nil)
		if st != http.StatusOK || !asBool(decodeMap(t, body)["online"]) {
			t.Fatalf("node detail = %d %s", st, string(body))
		}

		rSt, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/nodes/node-f16-rev/revoke", nil)
		if rSt != http.StatusOK {
			t.Fatalf("revoke = %d", rSt)
		}
		if gSt, _ := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-f16-rev", nil); gSt != http.StatusNotFound {
			t.Fatalf("revoked node detail = %d, want 404", gSt)
		}
		if iSt, _ := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f16-rev", "boot-1", 2, time.Now().UTC())); iSt != http.StatusUnauthorized {
			t.Fatalf("revoked node ingest = %d, want 401", iSt)
		}
	})

	t.Run("F16_SettingsPasswordChangeAndBackup", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		sSt, sBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/settings", nil)
		if sSt != http.StatusOK {
			t.Fatalf("GET /v1/settings = %d", sSt)
		}
		sm := decodeMap(t, sBody)
		if pv, _ := asUint64(sm["protocol_version"]); pv != 1 {
			t.Fatalf("settings protocol_version = %d, want 1", pv)
		}
		if sd, _ := asUint64(asMap(sm["retention"])["samples_days"]); sd != 14 {
			t.Fatalf("settings retention.samples_days = %d, want 14", sd)
		}

		pSt, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/settings/password", map[string]string{
			"current": "e2e-admin-pass-1", "password": "e2e-admin-pass-2",
		})
		if pSt != http.StatusOK {
			t.Fatalf("PUT /v1/settings/password = %d", pSt)
		}
		c2 := newCookieClient(t)
		if lSt, _ := loginAdmin(t, c2, srv.BaseURL, "e2e-admin-pass-2"); lSt != http.StatusOK {
			t.Fatalf("login with new password = %d, want 200", lSt)
		}

		bSt, bHdr, bBytes := doRawRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/backup", nil, nil)
		if bSt != http.StatusOK || !strings.HasPrefix(string(bBytes), "SQLite format 3\x00") {
			t.Fatalf("GET /v1/backup failed: status=%d ct=%q len=%d", bSt, bHdr.Get("Content-Type"), len(bBytes))
		}
	})

	// --- F17: Docs, ADRs & Privacy/Safety Audit (6 subtests) ---
	t.Run("F17_VMMetricsLabelWhitelist", func(t *testing.T) {
		vm := newMockVM(t)
		srv := startServer(t, ServerOpts{MetricsURL: vm.URL()})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f17-vm")

		st, body := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f17-vm", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface", "mihomo"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1024, "tx_delta": 512}),
			withDevices(map[string]any{"obs_ip": "203.0.113.77", "obs_mac": "02:00:00:00:00:77", "rx_delta": 100, "tx_delta": 50, "source": "mihomo", "outbound": "hk-01"}),
			withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 100, "proxy_tx": 50}),
		))
		if st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		reqs := vm.WaitForSamples(t, 5*time.Second, 1)
		allowed := map[string]bool{"site_id": true, "node_id": true, "device_id": true, "class": true, "direction": true}
		labelRe := regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="[^"]*"`)
		totalLines := 0
		for _, r := range reqs {
			totalLines += len(r.Lines)
			for _, line := range r.Lines {
				for _, m := range labelRe.FindAllStringSubmatch(line, -1) {
					if !allowed[m[1]] {
						t.Fatalf("forbidden/non-whitelisted VM label %q in line %q", m[1], line)
					}
				}
			}
		}
		if totalLines == 0 {
			t.Fatalf("expected non-empty VM metric lines, got 0")
		}
	})

	t.Run("F17_DomainCollectionDisabledByDefault", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-f17-domdef")

		st, body := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-f17-domdef", "boot-1", 1, time.Now().UTC()))
		if st != http.StatusOK {
			t.Fatalf("ingest = %d", st)
		}
		if _, exists := decodeMap(t, body)["collect_domains"]; exists {
			t.Fatalf("default node ingest response should omit collect_domains; body=%s", string(body))
		}
		_, nBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-f17-domdef", nil)
		nm := decodeMap(t, nBody)
		cd, hasField := nm["collect_domains"]
		if !hasField || asBool(cd) {
			t.Fatalf("expected collect_domains=false on node detail; body=%s", string(nBody))
		}
	})

	t.Run("F17_ReadOnlySurfaceNoExecOrPolicyWriteRoutes", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		probes := []struct{ method, path string }{
			{http.MethodPost, "/v1/exec"},
			{http.MethodPost, "/v1/firewall"},
			{http.MethodPut, "/v1/proxies/switch"},
			{http.MethodPost, "/v1/nodes/n1/restart"},
		}
		for _, p := range probes {
			st, _ := doJSONRequest(t, client, p.method, srv.BaseURL+p.path, map[string]any{})
			if st != http.StatusNotFound && st != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want 404/405", p.method, p.path, st)
			}
		}
	})

	t.Run("F17_DirectCollectorSecretsRedacted", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		const sec = "super-secret-bearer-e2e"
		mihomo := newMockMihomo(t, sec)
		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "RedactedDC", "site_id": "site-e2e", "node_id": "dc-red",
			"url": mihomo.URL(), "secret": sec, "enabled": false,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("POST /v1/direct-collectors = %d %s", cSt, string(cBody))
		}
		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/direct-collectors", nil)
		if gSt != http.StatusOK {
			t.Fatalf("GET /v1/direct-collectors = %d %s", gSt, string(gBody))
		}
		if strings.Contains(string(gBody), sec) {
			t.Fatalf("plaintext secret leaked in GET /v1/direct-collectors: %s", string(gBody))
		}
		var foundRedacted bool
		for _, it := range asSlice(decodeMap(t, gBody)["collectors"]) {
			c := asMap(it)
			if asString(c["name"]) == "RedactedDC" {
				foundRedacted = true
				if !asBool(c["has_secret"]) {
					t.Fatalf("expected RedactedDC has_secret=true; body=%s", string(gBody))
				}
				if rawSec := asString(c["secret"]); rawSec != "" && rawSec != "********" && !strings.Contains(rawSec, "*") {
					t.Fatalf("unexpected non-masked secret field on RedactedDC: %s", string(gBody))
				}
			}
		}
		if !foundRedacted {
			t.Fatalf("RedactedDC missing from GET /v1/direct-collectors: %s", string(gBody))
		}
	})

	t.Run("F17_NoRealPrivateIPsOrSecretsInRepoFixtures", func(t *testing.T) {
		root := repoRoot(t)
		var files []string
		for _, sub := range []string{"testdata/fixtures/synthetic", "docs/adr"} {
			entries, err := os.ReadDir(filepath.Join(root, sub))
			if err != nil {
				t.Fatalf("readdir %s: %v", sub, err)
			}
			for _, e := range entries {
				if !e.IsDir() {
					files = append(files, filepath.Join(root, sub, e.Name()))
				}
			}
		}
		for _, doc := range []string{"README.md", "README.zh-CN.md"} {
			files = append(files, filepath.Join(root, doc))
		}

		badPatterns := []*regexp.Regexp{
			regexp.MustCompile(`192\.168\.8\.`),
			regexp.MustCompile(`lt_[A-Za-z0-9]{8,}`),
			regexp.MustCompile(`subscribe\?token=`),
		}
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			text := string(raw)
			for _, pat := range badPatterns {
				if pat.MatchString(text) {
					t.Fatalf("sensitive/real pattern %s matched in %s", pat, f)
				}
			}
		}
	})

	t.Run("F17_ADRsDocumentV02Decisions", func(t *testing.T) {
		root := repoRoot(t)
		idxBytes, err := os.ReadFile(filepath.Join(root, "docs", "adr", "README.md"))
		if err != nil {
			t.Fatalf("read docs/adr/README.md: %v", err)
		}
		idx := strings.ToLower(string(idxBytes))
		for _, topic := range []string{"multiplier", "direct", "domain"} {
			if !strings.Contains(idx, topic) {
				t.Fatalf("docs/adr/README.md missing v0.2 ADR topic %q", topic)
			}
		}
	})
}
