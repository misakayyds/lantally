//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTier2_IngestEdges(t *testing.T) {
	t.Run("F1_RejectUnsupportedProtocolVersions", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-ver")

		for _, ver := range []int{2, 0, -1} {
			b := syntheticBatch("site-e2e", "node-t2-ver", "boot-1", 1, time.Now().UTC())
			b["protocol_version"] = ver
			st, body := postIngestBatch(t, srv.BaseURL, tok, b)
			if st != http.StatusBadRequest || !strings.Contains(string(body), "protocol_version") {
				t.Fatalf("protocol_version=%d -> status=%d body=%q, want 400 mentioning protocol_version", ver, st, string(body))
			}
		}
	})

	t.Run("F1_RejectUnknownFieldsAllLevels", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-unk")

		cases := []struct {
			name  string
			batch map[string]any
		}{
			{
				name: "root",
				batch: func() map[string]any {
					b := syntheticBatch("site-e2e", "node-t2-unk", "boot-1", 1, time.Now().UTC())
					b["extra_root"] = 1
					return b
				}(),
			},
			{
				name: "interfaces",
				batch: syntheticBatch("site-e2e", "node-t2-unk", "boot-1", 1, time.Now().UTC(),
					withInterfaces(map[string]any{"name": "eth0", "rx_delta": 1, "tx_delta": 1, "extra_iface": true}),
				),
			},
			{
				name: "devices",
				batch: syntheticBatch("site-e2e", "node-t2-unk", "boot-1", 1, time.Now().UTC(),
					withDevices(map[string]any{"obs_ip": "203.0.113.10", "rx_delta": 1, "tx_delta": 1, "source": "nlbwmon", "extra_dev": "x"}),
				),
			},
			{
				name: "outbound",
				batch: syntheticBatch("site-e2e", "node-t2-unk", "boot-1", 1, time.Now().UTC(),
					withProxyOutbounds(map[string]any{"name": "hk-01", "proxy_rx": 1, "proxy_tx": 1, "extra_ob": 1}),
				),
			},
		}
		for _, tc := range cases {
			st, body := postIngestBatch(t, srv.BaseURL, tok, tc.batch)
			if st != http.StatusBadRequest {
				t.Fatalf("unknown field (%s) status = %d, want 400; body=%s", tc.name, st, string(body))
			}
		}
	})

	t.Run("F1_RejectSequenceZeroAndRegression", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-seq")

		b0 := syntheticBatch("site-e2e", "node-t2-seq", "boot-1", 0, time.Now().UTC())
		if st, _ := postIngestBatch(t, srv.BaseURL, tok, b0); st != http.StatusBadRequest {
			t.Fatalf("sequence=0 status = %d, want 400", st)
		}

		b2 := syntheticBatch("site-e2e", "node-t2-seq", "boot-1", 2, time.Now().UTC())
		if st, _ := postIngestBatch(t, srv.BaseURL, tok, b2); st != http.StatusOK {
			t.Fatalf("sequence=2 status = %d, want 200", st)
		}
		b1 := syntheticBatch("site-e2e", "node-t2-seq", "boot-1", 1, time.Now().UTC())
		if st, _ := postIngestBatch(t, srv.BaseURL, tok, b1); st != http.StatusConflict {
			t.Fatalf("sequence regression status = %d, want 409", st)
		}
	})

	t.Run("F1_RejectEncodingContentTypeAndOversize", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-enc")

		b := syntheticBatch("site-e2e", "node-t2-enc", "boot-1", 1, time.Now().UTC())
		rawJSON, _ := json.Marshal(b)
		gz := gzipJSON(t, b)

		if st, _ := postIngestRaw(t, srv.BaseURL, tok, gz, map[string]string{"Content-Type": "application/json"}); st != http.StatusBadRequest {
			t.Fatalf("missing Content-Encoding status = %d, want 400", st)
		}
		if st, _ := postIngestRaw(t, srv.BaseURL, tok, gz, map[string]string{"Content-Encoding": "gzip", "Content-Type": "text/plain"}); st != http.StatusBadRequest {
			t.Fatalf("bad Content-Type status = %d, want 400", st)
		}
		if st, _ := postIngestRaw(t, srv.BaseURL, tok, rawJSON, map[string]string{"Content-Encoding": "gzip", "Content-Type": "application/json"}); st != http.StatusBadRequest {
			t.Fatalf("non-gzip body status = %d, want 400", st)
		}

		oversize := syntheticBatch("site-e2e", "node-t2-enc", "boot-1", 1, time.Now().UTC(),
			withMetadata(map[string]any{"pad": strings.Repeat("a", (4<<20)+1024)}),
		)
		if st, _ := postIngestBatch(t, srv.BaseURL, tok, oversize); st != http.StatusBadRequest {
			t.Fatalf(">4MiB decompressed batch status = %d, want 400", st)
		}
	})

	t.Run("F1_RejectBadRevokedOrMismatchedToken", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok1 := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-tok1")
		tok2 := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-tok2")

		b1 := syntheticBatch("site-e2e", "node-t2-tok1", "boot-1", 1, time.Now().UTC())
		if st, _ := postIngestBatch(t, srv.BaseURL, "", b1); st != http.StatusUnauthorized {
			t.Fatalf("missing bearer status = %d, want 401", st)
		}
		if st, _ := postIngestBatch(t, srv.BaseURL, "lt_bad_token", b1); st != http.StatusUnauthorized {
			t.Fatalf("bad bearer status = %d, want 401", st)
		}
		if st, _ := postIngestBatch(t, srv.BaseURL, tok2, b1); st != http.StatusUnauthorized {
			t.Fatalf("mismatched node_id status = %d, want 401", st)
		}
		bWrongSite := syntheticBatch("other-site", "node-t2-tok1", "boot-1", 1, time.Now().UTC())
		if st, _ := postIngestBatch(t, srv.BaseURL, tok1, bWrongSite); st != http.StatusUnauthorized {
			t.Fatalf("mismatched site_id status = %d, want 401", st)
		}

		rvSt, rvBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/nodes/node-t2-tok1/revoke", nil)
		if rvSt != http.StatusOK {
			t.Fatalf("revoke status = %d; body=%s", rvSt, string(rvBody))
		}
		if st, _ := postIngestBatch(t, srv.BaseURL, tok1, b1); st != http.StatusUnauthorized {
			t.Fatalf("revoked token status = %d, want 401", st)
		}
	})

	t.Run("F1_RejectTrailingJSONAndMissingRequired", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-req")

		b := syntheticBatch("site-e2e", "node-t2-req", "boot-1", 1, time.Now().UTC())
		raw, _ := json.Marshal(b)
		trailing := append(append([]byte{}, raw...), []byte(`{"extra":true}`)...)
		if st, _ := postIngestRaw(t, srv.BaseURL, tok, gzipBytes(t, trailing), map[string]string{
			"Content-Encoding": "gzip", "Content-Type": "application/json",
		}); st != http.StatusBadRequest {
			t.Fatalf("trailing JSON status = %d, want 400", st)
		}

		for _, mut := range []func(map[string]any){
			func(m map[string]any) { m["site_id"] = "" },
			func(m map[string]any) { m["node_id"] = "" },
			func(m map[string]any) { m["boot_id"] = "" },
			func(m map[string]any) { m["sampled_at"] = "0001-01-01T00:00:00Z" },
			func(m map[string]any) { m["interval_ms"] = 0 },
		} {
			bad := syntheticBatch("site-e2e", "node-t2-req", "boot-1", 1, time.Now().UTC())
			mut(bad)
			st, _ := postIngestBatch(t, srv.BaseURL, tok, bad)
			if st != http.StatusBadRequest && st != http.StatusUnauthorized {
				t.Fatalf("missing required field status = %d, want 400/401", st)
			}
		}
	})
}

func TestTier2_HeartbeatEdges(t *testing.T) {
	t.Run("F2_DuplicateRetryKeepsLatestPrefs", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f2-dup")

		b := syntheticBatch("site-e2e", "node-t2-f2-dup", "boot-1", 1, time.Now().UTC())
		if s0, b0 := postIngestBatch(t, srv.BaseURL, tok, b); s0 != http.StatusOK {
			t.Fatalf("initial ingest status = %d; body=%s", s0, string(b0))
		}
		if st, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-t2-f2-dup/config", map[string]any{"collect_domains": true}); st != http.StatusOK {
			t.Fatalf("PUT config status = %d", st)
		}
		st, body := postIngestBatch(t, srv.BaseURL, tok, b)
		if st != http.StatusOK {
			t.Fatalf("duplicate retry status = %d", st)
		}
		m := decodeMap(t, body)
		if !asBool(m["duplicate"]) || !asBool(m["collect_domains"]) {
			t.Fatalf("expected duplicate=true and collect_domains=true; body=%s", string(body))
		}
	})

	t.Run("F2_WatchDomainsActiveWhileCollectDomainsOff", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f2-wonly")

		wSt, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":     "OnlyWatched",
			"matchers": map[string]any{"domain": map[string]any{"type": "exact", "values": []string{"target.watched.test"}}},
		})
		if wSt != http.StatusOK && wSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch status = %d", wSt)
		}

		iSt, body := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f2-wonly", "boot-1", 1, time.Now().UTC()))
		if iSt != http.StatusOK {
			t.Fatalf("ingest status = %d; body=%s", iSt, string(body))
		}
		m := decodeMap(t, body)
		if asBool(m["collect_domains"]) {
			t.Fatalf("collect_domains should remain false; body=%s", string(body))
		}
		var hasTarget bool
		for _, v := range asSlice(m["watch_domains"]) {
			if asString(v) == "target.watched.test" {
				hasTarget = true
			}
		}
		if !hasTarget {
			t.Fatalf("expected target.watched.test in watch_domains; body=%s", string(body))
		}
	})

	t.Run("F2_EmptyOrDuplicateWatchDomainsHandled", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f2-dedup")

		w1St, w1Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":     "W1",
			"matchers": map[string]any{"domain": map[string]any{"type": "exact", "values": []string{"dup.example.test", "  "}}},
		})
		if w1St != http.StatusOK && w1St != http.StatusCreated {
			t.Fatalf("POST /v1/watch W1 status = %d; body=%s", w1St, string(w1Body))
		}
		w2St, w2Body := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":     "W2",
			"matchers": map[string]any{"domain": map[string]any{"type": "exact", "values": []string{"dup.example.test"}}},
		})
		if w2St != http.StatusOK && w2St != http.StatusCreated {
			t.Fatalf("POST /v1/watch W2 status = %d; body=%s", w2St, string(w2Body))
		}

		st, body := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f2-dedup", "boot-1", 1, time.Now().UTC()))
		if st != http.StatusOK {
			t.Fatalf("ingest status = %d; body=%s", st, string(body))
		}
		m := decodeMap(t, body)
		dupCount := 0
		for _, v := range asSlice(m["watch_domains"]) {
			s := asString(v)
			if strings.TrimSpace(s) == "" {
				t.Fatalf("empty/whitespace entry leaked into watch_domains: %s", string(body))
			}
			if s == "dup.example.test" {
				dupCount++
			}
		}
		if dupCount != 1 {
			t.Fatalf("expected dup.example.test once in watch_domains, got %d; body=%s", dupCount, string(body))
		}
	})

	t.Run("F2_AgentJSONNeverMutatedOnDisk", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f2-sha")

		mihomo := newMockMihomo(t, "s")
		agent := startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f2-sha", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")
		beforeHash := agent.ConfigSHA256(t)

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-t2-f2-sha", nil)
			return st == http.StatusOK && asBool(decodeMap(t, b)["online"])
		})

		c1St, c1Body := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-t2-f2-sha/config", map[string]any{"collect_domains": true})
		if c1St != http.StatusOK {
			t.Fatalf("PUT collect_domains=true status = %d; body=%s", c1St, string(c1Body))
		}
		time.Sleep(300 * time.Millisecond)
		c2St, c2Body := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-t2-f2-sha/config", map[string]any{"collect_domains": false})
		if c2St != http.StatusOK {
			t.Fatalf("PUT collect_domains=false status = %d; body=%s", c2St, string(c2Body))
		}
		time.Sleep(300 * time.Millisecond)

		afterHash := agent.ConfigSHA256(t)
		if beforeHash != afterHash {
			t.Fatalf("agent.json modified on disk: %s != %s", beforeHash, afterHash)
		}
	})

	t.Run("F2_MultiNodeIsolatedDomainPrefs", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tokA := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-a")
		tokB := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-b")

		cSt, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-t2-a/config", map[string]any{"collect_domains": true})
		if cSt != http.StatusOK {
			t.Fatalf("PUT node-t2-a config status = %d", cSt)
		}

		_, bA := postIngestBatch(t, srv.BaseURL, tokA, syntheticBatch("site-e2e", "node-t2-a", "boot-1", 1, time.Now().UTC()))
		_, bB := postIngestBatch(t, srv.BaseURL, tokB, syntheticBatch("site-e2e", "node-t2-b", "boot-1", 1, time.Now().UTC()))
		if !asBool(decodeMap(t, bA)["collect_domains"]) || asBool(decodeMap(t, bB)["collect_domains"]) {
			t.Fatalf("domain prefs leaked across nodes: A=%s B=%s", string(bA), string(bB))
		}
	})
}

func TestTier2_ChainsEdges(t *testing.T) {
	t.Run("F3_EmptyChainsMapsToDirect", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f3-empty")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "c1", Upload: 50, Download: 100, Chains: []string{}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f3-empty", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		time.Sleep(150 * time.Millisecond)
		mihomo.SetConnections([]MockMihomoConn{{ID: "c1", Upload: 250, Download: 400, Chains: []string{}}})
		_ = mihomo.PushWSFrame([]MockMihomoConn{{ID: "c1", Upload: 250, Download: 400, Chains: []string{}}})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			dir, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["direct"])
			return dir == 500
		})
		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		if pr, _ := asUint64(asMap(decodeMap(t, pBody)["proxy"])["proxy_raw"]); pr != 0 {
			t.Fatalf("empty chains produced proxy_raw=%d, want 0", pr)
		}
	})

	t.Run("F3_WhitespaceOrLowercaseDirectNormalized", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f3-ws")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 100, Download: 200, Chains: []string{" direct "}}},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f3-ws", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			dir, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["direct"])
			return dir == 300
		})
		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		if len(asSlice(decodeMap(t, pBody)["outbounds"])) != 0 {
			t.Fatalf("whitespace direct created proxy outbound: %s", string(pBody))
		}
	})

	t.Run("F3_SingleElementProxyChainEmptyGroup", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f3-single")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 200, Download: 300, Chains: []string{"HK-01"}}},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f3-single", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 500
		})
		_, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		if _, exists := asMap(decodeMap(t, gBody)["totals"])["HK-01"]; exists {
			t.Fatalf("single-element chain must not record HK-01 as strategy group; body=%s", string(gBody))
		}
	})

	t.Run("F3_EmptyTrailingChainElementHandled", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f3-trail")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 100, Download: 100, Chains: []string{"Exit-01", "Relay", ""}}},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f3-trail", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 200
		})
		_, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=group", nil)
		if _, hasEmpty := asMap(decodeMap(t, gBody)["totals"])[""]; hasEmpty {
			t.Fatalf("empty group key recorded: %s", string(gBody))
		}
	})

	t.Run("F3_RegressionGuardAgainstReversedGroup", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f3-rev")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 250, Download: 250, Chains: []string{"Exit-Real-Node", "PROXY"}}},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f3-rev", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 500
		})
		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		for _, it := range asSlice(decodeMap(t, pBody)["outbounds"]) {
			if asString(asMap(it)["name"]) == "PROXY" {
				t.Fatalf("reversed chains regression: PROXY attributed as outbound in %s", string(pBody))
			}
		}
	})
}

func TestTier2_StreamEdges(t *testing.T) {
	t.Run("F4_ShortLivedBurstBetweenTicksCaptured", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f4-burst")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{
				{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
				{ID: "short-1", Upload: 500, Download: 2000, Chains: []string{"HK-Burst", "Proxy"}},
			},
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f4-burst", Interval: "800ms",
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
			return pr == 2500 && batches >= 2
		})
		aSt, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		if aSt != http.StatusOK {
			t.Fatalf("GET /v1/alerts status = %d; body=%s", aSt, string(aBody))
		}
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			al := asMap(it)
			if asString(al["kind"]) == "reset" && asString(al["message"]) != "collector gap detected: reboot" {
				t.Fatalf("short-lived connection close emitted non-reboot reset alert: %s", string(aBody))
			}
		}
	})

	t.Run("F4_CounterRegressionEmitsResetNoUnderflow", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f4-reg")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "c1", Upload: 5000, Download: 5000, Chains: []string{"HK-01", "Proxy"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 5500, Download: 5500, Chains: []string{"HK-01", "Proxy"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 200, Download: 200, Chains: []string{"HK-01", "Proxy"}}},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f4-reg", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

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
		ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovSt != http.StatusOK {
			t.Fatalf("GET /v1/overview status = %d; body=%s", ovSt, string(ovBody))
		}
		pr, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["proxy_raw"])
		if pr < 1000 || pr > 1000000 {
			t.Fatalf("unexpected proxy_raw=%d after counter regression (want in [1000, 1000000])", pr)
		}
	})

	t.Run("F4_WSAbruptDropReconnectsAndRebaselines", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f4-reconn")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "c1", Upload: 1000, Download: 1000, Chains: []string{"HK-01", "Proxy"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f4-reconn", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.WSConnectCount() >= 1 })
		_ = mihomo.PushWSFrame([]MockMihomoConn{{ID: "c1", Upload: 1200, Download: 1300, Chains: []string{"HK-01", "Proxy"}}})
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			if st != http.StatusOK {
				return false
			}
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 500
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
		time.Sleep(250 * time.Millisecond)

		// Existing connection must not be double-counted on reconnect re-baseline.
		ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovSt != http.StatusOK {
			t.Fatalf("GET /v1/overview status = %d; body=%s", ovSt, string(ovBody))
		}
		pr, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["proxy_raw"])
		if pr != 500 {
			t.Fatalf("proxy_raw after WS reconnect = %d, want 500 (no double-count)", pr)
		}
	})

	t.Run("F4_MihomoFailureIsolatedFromIface", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f4-iso")

		mihomo := newMockMihomo(t, "correct-secret")
		agent := startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f4-iso", Interval: "200ms",
			Collectors: AgentCollectorsConfig{Iface: true, Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "wrong-secret")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-t2-f4-iso", nil)
			if st != http.StatusOK || !asBool(decodeMap(t, b)["online"]) {
				return false
			}
			ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			if ovSt != http.StatusOK {
				return false
			}
			batches, _ := asUint64(decodeMap(t, ovBody)["ingest_batches"])
			if batches < 2 {
				return false
			}
			trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface", nil)
			if trSt != http.StatusOK {
				return false
			}
			return len(asMap(decodeMap(t, trBody)["totals"])) > 0
		})
		if strings.Contains(strings.ToLower(agent.Logs()), "unsupported") {
			t.Fatalf("agent logs contain unsupported error: %s", agent.Logs())
		}
		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if pr, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["proxy_raw"]); pr != 0 {
			t.Fatalf("failing mihomo produced proxy_raw=%d, want 0", pr)
		}
		aSt, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		if aSt != http.StatusOK {
			t.Fatalf("GET /v1/alerts status = %d; body=%s", aSt, string(aBody))
		}
		hasMihomoReset := false
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			al := asMap(it)
			if asString(al["kind"]) == "reset" && strings.Contains(asString(al["message"]), "collector_reset") {
				hasMihomoReset = true
			}
		}
		if !hasMihomoReset {
			t.Fatalf("expected collector_reset alert from failing mihomo; alerts=%s", string(aBody))
		}
	})

	t.Run("F4_IdleIdenticalFramesNoZeroInflation", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f4-idle")

		mihomo := newMockMihomo(t, "s")
		idle := []MockMihomoConn{{ID: "c1", Upload: 500, Download: 1000, Chains: []string{"HK-01"}}}
		mihomo.SetConnections(idle)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f4-idle", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		_ = mihomo.PushWSFrame(idle)
		time.Sleep(450 * time.Millisecond)

		_, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		pr, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["proxy_raw"])
		if pr != 0 {
			t.Fatalf("idle identical frames inflated proxy_raw to %d, want 0", pr)
		}
		_, pBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy", nil)
		if len(asSlice(decodeMap(t, pBody)["outbounds"])) != 0 {
			t.Fatalf("idle identical frames created zero-delta outbound rows: %s", string(pBody))
		}
	})
}

func TestTier2_ProcessDomainEdges(t *testing.T) {
	t.Run("F5_FullDirectoryPathNeverExposed", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f5-path")

		mihomo := newMockMihomo(t, "s")
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{
				ID: "c1", Upload: 100, Download: 200, Chains: []string{"HK-01", "Proxy"},
				Metadata: MockMihomoMetadata{ProcessPath: `C:\Users\test\bin\app.exe`},
			}},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f5-path", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
			if st != http.StatusOK {
				return false
			}
			tot, _ := asUint64(asMap(decodeMap(t, b)["totals"])["app.exe"])
			return tot == 300
		})
		_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=process", nil)
		if strings.Contains(string(b), "Users") || strings.Contains(string(b), `test\bin`) {
			t.Fatalf("directory path leaked in process traffic: %s", string(b))
		}
	})

	t.Run("F5_TopNTruncationRollsIntoOther", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f5-topn")

		if st, body := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-t2-f5-topn/config", map[string]any{"collect_domains": true}); st != http.StatusOK {
			t.Fatalf("PUT config status = %d; body=%s", st, string(body))
		}

		conns := []MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}}
		for i := 1; i <= 60; i++ {
			conns = append(conns, MockMihomoConn{
				ID:       fmt.Sprintf("c-%d", i),
				Upload:   uint64(200 - i),
				Download: uint64(2000 - i*10),
				Chains:   []string{"HK-01", "Proxy"},
				Metadata: MockMihomoMetadata{Host: fmt.Sprintf("api.dom%02d.test", i)},
			})
		}

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f5-topn", Interval: "200ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			if mihomo.ConnRequestCount() < 1 {
				return false
			}
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-t2-f5-topn", nil)
			return st == http.StatusOK && asBool(decodeMap(t, b)["online"])
		})
		time.Sleep(100 * time.Millisecond)
		_ = mihomo.PushWSFrame(conns)

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if st != http.StatusOK {
				return false
			}
			totals := asMap(decodeMap(t, b)["totals"])
			oth, _ := asUint64(totals["other"])
			return len(totals) > 0 && len(totals) < 60 && oth > 0
		})
	})

	t.Run("F5_WatchedDomainExemptFromTopNAndOptOut", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f5-wex")

		// Phase 1: collect_domains stays FALSE, only watch_domains has rare.watched.test.
		wSt, wBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/watch", map[string]any{
			"name":     "RareWatch",
			"matchers": map[string]any{"domain": map[string]any{"type": "exact", "values": []string{"rare.watched.test"}}},
		})
		if wSt != http.StatusOK && wSt != http.StatusCreated {
			t.Fatalf("POST /v1/watch status = %d; body=%s", wSt, string(wBody))
		}

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f5-wex", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			if mihomo.ConnRequestCount() < 1 {
				return false
			}
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-t2-f5-wex", nil)
			return st == http.StatusOK && asBool(decodeMap(t, b)["online"])
		})
		time.Sleep(100 * time.Millisecond)

		phase1 := []MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "w1", Upload: 120, Download: 380, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "rare.watched.test"}},
			{ID: "u1", Upload: 900, Download: 900, Chains: []string{"HK-01", "Proxy"}, Metadata: MockMihomoMetadata{Host: "unwatched.example"}},
		}
		_ = mihomo.PushWSFrame(phase1)

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if st != http.StatusOK {
				return false
			}
			totals := asMap(decodeMap(t, b)["totals"])
			w, _ := asUint64(totals["rare.watched.test"])
			_, hasUnwatched := totals["unwatched.example"]
			return w == 500 && !hasUnwatched
		})

		// Phase 2: collect_domains=true with >50 larger competing domains; rare.watched.test remains exempt from Top-N truncation.
		cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-t2-f5-wex/config", map[string]any{"collect_domains": true})
		if cSt != http.StatusOK {
			t.Fatalf("PUT collect_domains=true status = %d; body=%s", cSt, string(cBody))
		}
		time.Sleep(300 * time.Millisecond)

		phase2 := append([]MockMihomoConn{}, phase1...)
		phase2 = append(phase2, MockMihomoConn{
			ID: "w2", Upload: 10, Download: 20, Chains: []string{"HK-01", "Proxy"},
			Metadata: MockMihomoMetadata{Host: "rare.watched.test"},
		})
		for i := 1; i <= 60; i++ {
			phase2 = append(phase2, MockMihomoConn{
				ID:       fmt.Sprintf("top-%d", i),
				Upload:   uint64(200 + i),
				Download: uint64(1000 + i*10),
				Chains:   []string{"HK-01", "Proxy"},
				Metadata: MockMihomoMetadata{Host: fmt.Sprintf("sub.comp%02d.test", i)},
			})
		}
		_ = mihomo.PushWSFrame(phase2)

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if st != http.StatusOK {
				return false
			}
			totals := asMap(decodeMap(t, b)["totals"])
			w, _ := asUint64(totals["rare.watched.test"])
			oth, _ := asUint64(totals["other"])
			return w == 530 && oth > 0
		})
	})

	t.Run("F5_IPLiteralHostIgnoredForDomains", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f5-ipl")

		cSt, cBody := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/nodes/node-t2-f5-ipl/config", map[string]any{"collect_domains": true})
		if cSt != http.StatusOK {
			t.Fatalf("PUT node config status = %d; body=%s", cSt, string(cBody))
		}

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f5-ipl", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			if mihomo.ConnRequestCount() < 1 {
				return false
			}
			st, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/nodes/node-t2-f5-ipl", nil)
			return st == http.StatusOK && asBool(decodeMap(t, b)["online"])
		})
		time.Sleep(100 * time.Millisecond)

		_ = mihomo.PushWSFrame([]MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{
				ID: "c1", Upload: 200, Download: 300, Chains: []string{"HK-01", "Proxy"},
				Metadata: MockMihomoMetadata{Host: "203.0.113.50", SniffHost: ""},
			},
			{
				ID: "c2", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"},
				Metadata: MockMihomoMetadata{Host: "ok.example.test"},
			},
		})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			ovSt, ovB := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			if ovSt != http.StatusOK {
				return false
			}
			pr, _ := asUint64(asMap(decodeMap(t, ovB)["bytes"])["proxy_raw"])
			if pr != 700 {
				return false
			}
			dSt, dB := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			if dSt != http.StatusOK {
				return false
			}
			okDom, _ := asUint64(asMap(decodeMap(t, dB)["totals"])["example.test"])
			return okDom == 200
		})
		dSt, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if dSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=domain status = %d; body=%s", dSt, string(dBody))
		}
		if _, hasIP := asMap(decodeMap(t, dBody)["totals"])["203.0.113.50"]; hasIP {
			t.Fatalf("IP literal leaked into domain dimension: %s", string(dBody))
		}
	})

	t.Run("F5_NonLanSourceIPStaysNodeLevel", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f5-nonlan")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f5-nonlan", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		time.Sleep(150 * time.Millisecond)

		next := []MockMihomoConn{
			{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}},
			{ID: "c1", Upload: 100, Download: 200, Chains: []string{"HK-01"}, Metadata: MockMihomoMetadata{SourceIP: "203.0.113.10"}},
			{ID: "c2", Upload: 300, Download: 400, Chains: []string{"HK-01"}, Metadata: MockMihomoMetadata{SourceIP: "10.203.113.10"}},
		}
		mihomo.SetConnections(next)
		_ = mihomo.PushWSFrame(next)

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 1000
		})
		_, dBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/devices", nil)
		devs := asSlice(decodeMap(t, dBody)["devices"])
		if len(devs) != 1 || asString(asMap(devs[0])["ip"]) != "10.203.113.10" {
			t.Fatalf("expected only 10.203.113.10 device; got %s", string(dBody))
		}
	})
}

func TestTier2_SyncEdges(t *testing.T) {
	t.Run("F6_Providers404DoesNotBreakTraffic", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f6-404")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetProviderStatus(http.StatusNotFound)
		mihomo.QueueWSFrames(
			[]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}},
			[]MockMihomoConn{{ID: "c1", Upload: 200, Download: 300, Chains: []string{"HK-01", "Proxy"}}},
		)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f6-404", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, b := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
			pr, _ := asUint64(asMap(decodeMap(t, b)["bytes"])["proxy_raw"])
			return pr == 500
		})
	})

	t.Run("F6_ProviderZeroTotalAndZeroExpire", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f6-zero")

		iSt, iBody := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f6-zero", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProviders(map[string]any{
				"name": "ZeroProv", "vehicle_type": "HTTP",
				"upload": 50, "download": 50, "total": 0, "expire": 0,
				"proxies": []string{"hk-01"},
			}),
		))
		if iSt != http.StatusOK {
			t.Fatalf("ingest status = %d; body=%s", iSt, string(iBody))
		}

		st, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if st != http.StatusOK || strings.Contains(string(body), "NaN") || strings.Contains(string(body), "Inf") {
			t.Fatalf("GET /v1/proxy/providers = %d %s", st, string(body))
		}
		var foundZero bool
		for _, it := range asSlice(decodeMap(t, body)["providers"]) {
			p := asMap(it)
			if asString(p["name"]) == "ZeroProv" {
				foundZero = true
				used, okUsed := asUint64(p["used_bytes"])
				tot, okTot := asUint64(p["total_bytes"])
				_, okDrift := asFloat64(p["drift_ratio"])
				if !okUsed || used != 100 || !okTot || tot != 0 || !okDrift {
					t.Fatalf("ZeroProv unexpected fields: %s", string(body))
				}
			}
		}
		if !foundZero {
			t.Fatalf("ZeroProv missing from GET /v1/proxy/providers: %s", string(body))
		}
	})

	t.Run("F6_SharedSubscriptionNotDoubleCounted", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok1 := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f6-sh1")
		tok2 := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f6-sh2")

		prov := map[string]any{
			"name": "SharedPlan", "vehicle_type": "HTTP",
			"upload": 1000, "download": 2000, "total": 50000, "expire": 1893456000,
			"proxies": []string{"hk-01", "us-01"},
		}
		if s1, b1 := postIngestBatch(t, srv.BaseURL, tok1, syntheticBatch("site-e2e", "node-t2-f6-sh1", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"), withProviders(prov),
		)); s1 != http.StatusOK {
			t.Fatalf("ingest node 1 failed: %d %s", s1, string(b1))
		}
		if s2, b2 := postIngestBatch(t, srv.BaseURL, tok2, syntheticBatch("site-e2e", "node-t2-f6-sh2", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"), withProviders(prov),
		)); s2 != http.StatusOK {
			t.Fatalf("ingest node 2 failed: %d %s", s2, string(b2))
		}

		pSt, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if pSt != http.StatusOK {
			t.Fatalf("GET /v1/proxy/providers status = %d; body=%s", pSt, string(body))
		}
		var count int
		for _, it := range asSlice(decodeMap(t, body)["providers"]) {
			if asString(asMap(it)["name"]) == "SharedPlan" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("expected 1 deduplicated SharedPlan row, got %d; body=%s", count, string(body))
		}
	})

	t.Run("F6_ProviderMemberListUpdateSynced", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f6-upd")

		if s1, b1 := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f6-upd", "boot-1", 1, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProviders(map[string]any{
				"name": "UpdProv", "vehicle_type": "HTTP",
				"upload": 10, "download": 20, "total": 10000, "expire": 1893456000,
				"proxies": []string{"hk-01"},
			}),
		)); s1 != http.StatusOK {
			t.Fatalf("ingest 1 failed: %d %s", s1, string(b1))
		}
		if s2, b2 := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f6-upd", "boot-1", 2, time.Now().UTC(),
			withCapabilities("mihomo"),
			withProviders(map[string]any{
				"name": "UpdProv", "vehicle_type": "HTTP",
				"upload": 10, "download": 20, "total": 10000, "expire": 1893456000,
				"proxies": []string{"hk-01", "jp-02"},
			}),
		)); s2 != http.StatusOK {
			t.Fatalf("ingest 2 failed: %d %s", s2, string(b2))
		}

		pSt, body := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/proxy/providers", nil)
		if pSt != http.StatusOK {
			t.Fatalf("GET /v1/proxy/providers status = %d; body=%s", pSt, string(body))
		}
		provs := asSlice(decodeMap(t, body)["providers"])
		if len(provs) != 1 || !strings.Contains(string(body), "jp-02") {
			t.Fatalf("expected 1 UpdProv with jp-02; body=%s", string(body))
		}
	})

	t.Run("F6_Large200NodeProxiesListHandled", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f6-200")

		proxies := make(map[string]MockMihomoProxy, 200)
		for i := 0; i < 200; i++ {
			name := fmt.Sprintf("Proxy-Node-%03d-x1.5", i)
			proxies[name] = MockMihomoProxy{Name: name, Type: "Shadowsocks", Alive: true}
		}
		mihomo := newMockMihomo(t, "s")
		mihomo.SetProxies(proxies)
		startAgent(t, AgentConfig{
			ServerURL: srv.BaseURL, SiteID: "site-e2e", NodeID: "node-t2-f6-200", Interval: "150ms",
			Collectors: AgentCollectorsConfig{Mihomo: true},
			Mihomo:     &AgentMihomoConfig{URL: mihomo.URL()},
		}, tok, "s")

		waitUntil(t, 6*time.Second, 100*time.Millisecond, func() bool {
			st, b := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/proxy/rules/preview", map[string]any{})
			if st != http.StatusOK {
				return false
			}
			items := asSlice(decodeMap(t, b)["matches"])
			return len(items) >= 200
		})
	})
}

func TestTier2_IfaceEdges(t *testing.T) {
	t.Run("F7_AllDefaultVirtualPrefixesExcluded", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f7-vpref")

		virts := []string{"lo", "lo0", "tun0", "utun4", "wg0", "tailscale0", "docker0", "veth9a", "br-01"}
		var ifaces []map[string]any
		ifaces = append(ifaces, map[string]any{"name": "eth0", "rx_delta": 1000, "tx_delta": 500})
		for _, v := range virts {
			ifaces = append(ifaces, map[string]any{"name": v, "rx_delta": 500, "tx_delta": 500})
		}

		if st, body := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f7-vpref", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"), withInterfaces(ifaces...),
		)); st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovSt != http.StatusOK {
			t.Fatalf("overview status = %d; body=%s", ovSt, string(ovBody))
		}
		if tot, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"]); tot != 1500 {
			t.Fatalf("node total = %d, want 1500 (all virtual prefixes excluded)", tot)
		}

		trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface", nil)
		if trSt != http.StatusOK {
			t.Fatalf("group=iface status = %d; body=%s", trSt, string(trBody))
		}
		totals := asMap(decodeMap(t, trBody)["totals"])
		for _, v := range virts {
			if got, _ := asUint64(totals[v]); got != 1000 {
				t.Fatalf("iface %s = %d, want 1000 in group=iface", v, got)
			}
		}
	})

	t.Run("F7_OnlyVirtualIfacesYieldsZeroTotal", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f7-onlyv")

		if st, body := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f7-onlyv", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"),
			withInterfaces(
				map[string]any{"name": "lo", "rx_delta": 1000, "tx_delta": 1000},
				map[string]any{"name": "tun0", "rx_delta": 2000, "tx_delta": 2000},
			),
		)); st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		trSt, trBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=iface", nil)
		if trSt != http.StatusOK {
			t.Fatalf("GET /v1/traffic?group=iface status = %d; body=%s", trSt, string(trBody))
		}
		totals := asMap(decodeMap(t, trBody)["totals"])
		loTot, _ := asUint64(totals["lo"])
		tunTot, _ := asUint64(totals["tun0"])
		if loTot != 2000 || tunTot != 4000 {
			t.Fatalf("expected lo=2000 tun0=4000 in group=iface; body=%s", string(trBody))
		}

		ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovSt != http.StatusOK {
			t.Fatalf("overview status = %d; body=%s", ovSt, string(ovBody))
		}
		if tot, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"]); tot != 0 {
			t.Fatalf("virtual-only node total = %d, want 0", tot)
		}
	})

	t.Run("F7_CounterDropAndDisappearanceGap", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f7-gap")

		now := time.Now().UTC()
		if st, body := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f7-gap", "boot-1", 1, now,
			withCapabilities("iface"),
			withInterfaces(map[string]any{"name": "eth0", "rx_delta": 500, "tx_delta": 500}),
			withGaps(map[string]any{
				"reason": "collector_reset",
				"from":   now.Add(-15 * time.Second).Format(time.RFC3339),
				"to":     now.Format(time.RFC3339),
			}),
		)); st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		aSt, aBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/alerts", nil)
		if aSt != http.StatusOK {
			t.Fatalf("GET /v1/alerts status = %d; body=%s", aSt, string(aBody))
		}
		foundReset := false
		for _, it := range asSlice(decodeMap(t, aBody)["alerts"]) {
			if asString(asMap(it)["kind"]) == "reset" {
				foundReset = true
			}
		}
		if !foundReset {
			t.Fatalf("expected reset alert on collector_reset gap; body=%s", string(aBody))
		}
	})

	t.Run("F7_CrossCompileCGO0AllPlatforms", func(t *testing.T) {
		root := repoRoot(t)
		outDir := t.TempDir()
		targets := []struct{ goos, goarch, ext string }{
			{"windows", "amd64", ".exe"},
			{"windows", "arm64", ".exe"},
			{"darwin", "amd64", ""},
			{"darwin", "arm64", ""},
			{"linux", "amd64", ""},
			{"linux", "arm64", ""},
		}
		for _, tgt := range targets {
			outBin := filepath.Join(outDir, fmt.Sprintf("agent-%s-%s%s", tgt.goos, tgt.goarch, tgt.ext))
			cmd := exec.Command("go", "build", "-o", outBin, "./cmd/lantally-agent")
			cmd.Dir = root
			cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0", "GOOS="+tgt.goos, "GOARCH="+tgt.goarch)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				t.Fatalf("cross-compile %s/%s failed: %v\n%s", tgt.goos, tgt.goarch, err, out.String())
			}
		}
	})

	t.Run("F7_PhysicalNamesNeverFalselyExcluded", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")
		tok := enrollNode(t, client, srv.BaseURL, "site-e2e", "node-t2-f7-phys")

		phys := []string{"eth0", "en0", "wlan0", "ens3", "enp3s0", "bridge0"}
		var ifaces []map[string]any
		for _, n := range phys {
			ifaces = append(ifaces, map[string]any{"name": n, "rx_delta": 100, "tx_delta": 0})
		}
		ifaces = append(ifaces, map[string]any{"name": "br-01", "rx_delta": 9000, "tx_delta": 0})

		if st, body := postIngestBatch(t, srv.BaseURL, tok, syntheticBatch("site-e2e", "node-t2-f7-phys", "boot-1", 1, time.Now().UTC(),
			withCapabilities("iface"), withInterfaces(ifaces...),
		)); st != http.StatusOK {
			t.Fatalf("ingest failed: %d %s", st, string(body))
		}

		ovSt, ovBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/overview", nil)
		if ovSt != http.StatusOK {
			t.Fatalf("overview status = %d; body=%s", ovSt, string(ovBody))
		}
		if tot, _ := asUint64(asMap(decodeMap(t, ovBody)["bytes"])["total"]); tot != 600 {
			t.Fatalf("physical interfaces total = %d, want 600 (bridge0 included, br-01 excluded)", tot)
		}
	})
}

func TestTier2_DirectCollectorEdges(t *testing.T) {
	t.Run("F8_SecretMaskedInAPIAndAbsentInLogs", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		const sec = "e2e-synthetic-secret-xyz"
		mihomo := newMockMihomo(t, sec)
		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "MaskDC", "site_id": "site-e2e", "node_id": "dc-mask",
			"url": mihomo.URL(), "secret": sec, "enabled": true,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("create direct collector = %d %s", cSt, string(cBody))
		}
		id := fmt.Sprintf("%v", decodeMap(t, cBody)["id"])
		tSt, tBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors/"+id+"/test", map[string]any{})
		if tSt != http.StatusOK {
			t.Fatalf("test direct collector = %d %s", tSt, string(tBody))
		}

		gSt, gBody := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/direct-collectors", nil)
		if gSt != http.StatusOK {
			t.Fatalf("GET /v1/direct-collectors = %d %s", gSt, string(gBody))
		}
		if !strings.Contains(string(gBody), "MaskDC") {
			t.Fatalf("MaskDC missing from GET /v1/direct-collectors: %s", string(gBody))
		}
		if strings.Contains(string(gBody), sec) || strings.Contains(string(tBody), sec) {
			t.Fatalf("secret exposed in direct collector API response: %s / %s", string(gBody), string(tBody))
		}
		if strings.Contains(srv.Logs(), sec) {
			t.Fatalf("secret exposed in server logs")
		}
	})

	t.Run("F8_UnauthenticatedRequestsReturn401", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		setupAdmin(t, newCookieClient(t), srv.BaseURL, "e2e-admin-pass-1")

		reqs := []struct{ m, p string }{
			{http.MethodGet, "/v1/direct-collectors"},
			{http.MethodPost, "/v1/direct-collectors"},
			{http.MethodPut, "/v1/direct-collectors/1"},
			{http.MethodDelete, "/v1/direct-collectors/1"},
			{http.MethodPost, "/v1/direct-collectors/1/test"},
			{http.MethodPost, "/v1/direct-collectors/1/toggle"},
		}
		for _, r := range reqs {
			st, _ := doJSONRequest(t, nil, r.m, srv.BaseURL+r.p, map[string]any{})
			if st != http.StatusUnauthorized {
				t.Fatalf("unauth %s %s = %d, want 401", r.m, r.p, st)
			}
		}
	})

	t.Run("F8_DomainToggleAppliedLive", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})

		cSt, cBody := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "LiveDomDC", "site_id": "site-e2e", "node_id": "dc-livedom",
			"url": mihomo.URL(), "secret": "s", "enabled": true, "collect_domains": false,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("create LiveDomDC = %d %s", cSt, string(cBody))
		}
		id := fmt.Sprintf("%v", decodeMap(t, cBody)["id"])

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "c1", Upload: 100, Download: 100, Chains: []string{"HK-01", "Proxy"},
			Metadata: MockMihomoMetadata{Host: "off.example.com"},
		}})
		time.Sleep(250 * time.Millisecond)

		_, d0 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
		if len(asMap(decodeMap(t, d0)["totals"])) != 0 {
			t.Fatalf("expected 0 domains when collect_domains=false; body=%s", string(d0))
		}

		uSt, _ := doJSONRequest(t, client, http.MethodPut, srv.BaseURL+"/v1/direct-collectors/"+id, map[string]any{
			"name": "LiveDomDC", "site_id": "site-e2e", "node_id": "dc-livedom",
			"url": mihomo.URL(), "enabled": true, "collect_domains": true,
		})
		if uSt != http.StatusOK {
			t.Fatalf("PUT direct collector status = %d", uSt)
		}
		time.Sleep(200 * time.Millisecond)
		_ = mihomo.PushWSFrame([]MockMihomoConn{{
			ID: "c2", Upload: 200, Download: 300, Chains: []string{"HK-01", "Proxy"},
			Metadata: MockMihomoMetadata{Host: "api.on.example.org"},
		}})

		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			_, d1 := doJSONRequest(t, client, http.MethodGet, srv.BaseURL+"/v1/traffic?group=domain", nil)
			tot, _ := asUint64(asMap(decodeMap(t, d1)["totals"])["example.org"])
			return tot == 500
		})
	})

	t.Run("F8_PersistsAndResumesAcrossServerRestart", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "dc-restart.db")
		mihomo := newMockMihomo(t, "s")
		mihomo.SetConnections([]MockMihomoConn{{ID: "b", Upload: 1, Download: 1, Chains: []string{"DIRECT"}}})

		srv1 := startServer(t, ServerOpts{DBPath: dbPath})
		c1 := newCookieClient(t)
		setupAdmin(t, c1, srv1.BaseURL, "e2e-admin-pass-1")
		cSt, _ := doJSONRequest(t, c1, http.MethodPost, srv1.BaseURL+"/v1/direct-collectors", map[string]any{
			"name": "PersistDC", "site_id": "site-e2e", "node_id": "dc-persist",
			"url": mihomo.URL(), "secret": "s", "enabled": true,
		})
		if cSt != http.StatusOK && cSt != http.StatusCreated {
			t.Fatalf("create direct collector = %d", cSt)
		}
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool { return mihomo.ConnRequestCount() >= 1 })
		srv1.Stop()

		before := mihomo.ConnRequestCount()
		srv2 := startServer(t, ServerOpts{DBPath: dbPath})
		_ = srv2
		waitUntil(t, 5*time.Second, 50*time.Millisecond, func() bool {
			return mihomo.ConnRequestCount() > before
		})
	})

	t.Run("F8_InvalidURLOrEmptyFieldsRejected400", func(t *testing.T) {
		srv := startServer(t, ServerOpts{})
		client := newCookieClient(t)
		setupAdmin(t, client, srv.BaseURL, "e2e-admin-pass-1")

		badPayloads := []map[string]any{
			{"name": "", "site_id": "site-e2e", "node_id": "dc-1", "url": "http://127.0.0.1:9090"},
			{"name": "DC", "site_id": "", "node_id": "dc-1", "url": "http://127.0.0.1:9090"},
			{"name": "DC", "site_id": "site-e2e", "node_id": "", "url": "http://127.0.0.1:9090"},
			{"name": "DC", "site_id": "site-e2e", "node_id": "dc-1", "url": "not-a-url"},
			{"name": "DC", "site_id": "site-e2e", "node_id": "dc-1", "url": "file:///etc/passwd"},
		}
		for i, p := range badPayloads {
			st, _ := doJSONRequest(t, client, http.MethodPost, srv.BaseURL+"/v1/direct-collectors", p)
			if st != http.StatusBadRequest {
				t.Fatalf("bad payload #%d status = %d, want 400", i, st)
			}
		}
	})
}
