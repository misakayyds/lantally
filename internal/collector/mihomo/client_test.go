package mihomo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/misakayyds/lantally/internal/protocol"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "synthetic", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseConnectionsSplitsDirectAndProxy(t *testing.T) {
	first, _, next, gaps, err := ParseConnections(nil, loadFixture(t, "mihomo-connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ByOutbound) != 0 || len(gaps) != 0 {
		t.Fatalf("first sample should establish baseline with zero deltas/gaps, got outbounds=%+v gaps=%+v", first.ByOutbound, gaps)
	}
	if len(next) != 3 {
		t.Fatalf("expected 3 baseline connections, got %d", len(next))
	}

	secondSnapshot := []byte(`{
		"connections": [
			{"id":"conn-direct-1","upload":400,"download":900,"chains":["DIRECT"]},
			{"id":"conn-proxy-1","upload":260,"download":980,"chains":["ss-test","PROXY"]},
			{"id":"conn-proxy-2","upload":130,"download":450,"chains":["ss-test","PROXY"]}
		]
	}`)

	got, devices, _, gaps, err := ParseConnections(next, secondSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", gaps)
	}
	if len(devices) != 0 {
		t.Fatalf("fixture without LAN sourceIP must stay node-level, got %+v", devices)
	}

	direct := findOutbound(t, got.ByOutbound, "DIRECT")
	proxy := findOutbound(t, got.ByOutbound, "ss-test")
	if direct.DirectRx != 200 || direct.DirectTx != 100 || direct.Group != "" {
		t.Fatalf("direct delta = %+v, want rx=200 tx=100 group=\"\"", direct)
	}
	if proxy.ProxyRx != 230 || proxy.ProxyTx != 90 || proxy.Group != "PROXY" {
		t.Fatalf("proxy delta = %+v, want rx=230 tx=90 group=PROXY", proxy)
	}
}

func TestParseConnectionsDoesNotEmitGapForNormalConnectionClose(t *testing.T) {
	prev := map[string]ConnCounters{
		"conn-direct-1": {Upload: 300, Download: 700},
	}
	raw := []byte(`{"connections":[{"id":"conn-direct-1","upload":350,"download":750,"chains":["DIRECT"]}]}`)
	first, _, next, gaps, err := ParseConnections(prev, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps on active conn: %+v", gaps)
	}
	if len(first.ByOutbound) != 1 {
		t.Fatalf("expected one outbound delta, got %+v", first.ByOutbound)
	}

	closed := []byte(`{"connections":[]}`)
	_, _, afterClose, gaps, err := ParseConnections(next, closed)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("normal connection close must not emit GapCollectorReset, got %+v", gaps)
	}
	if len(afterClose) != 0 {
		t.Fatalf("closed connection should be evicted from next state, got %+v", afterClose)
	}
}

func TestParseConnectionsCountsNewShortLivedConnectionsFromZero(t *testing.T) {
	// Baseline has zero connections, yet prev is non-nil.
	_, _, prev, _, err := ParseConnections(nil, []byte(`{"connections":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil {
		t.Fatal("baseline state map must be non-nil even when zero connections exist")
	}

	raw := []byte(`{
		"connections": [
			{"id":"short-1","upload":120,"download":480,"chains":["ss-test","Auto","PROXY"]}
		]
	}`)
	got, _, next, gaps, err := ParseConnections(prev, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", gaps)
	}
	if len(next) != 1 {
		t.Fatalf("expected 1 tracked connection, got %d", len(next))
	}
	proxy := findOutbound(t, got.ByOutbound, "ss-test")
	if proxy.ProxyRx != 480 || proxy.ProxyTx != 120 || proxy.Group != "PROXY" {
		t.Fatalf("short-lived connection delta = %+v, want rx=480 tx=120 group=PROXY", proxy)
	}
}

func TestParseConnectionsEmitsGapOnCounterDecrease(t *testing.T) {
	prev := map[string]ConnCounters{
		"conn-1": {Upload: 500, Download: 1000},
	}
	raw := []byte(`{"connections":[{"id":"conn-1","upload":100,"download":200,"chains":["ss-test","PROXY"]}]}`)
	got, _, next, gaps, err := ParseConnections(prev, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ByOutbound) != 0 {
		t.Fatalf("counter decrease must skip negative/corrupted delta, got %+v", got.ByOutbound)
	}
	if len(gaps) != 1 || gaps[0].Reason != protocol.GapCollectorReset {
		t.Fatalf("expected 1 collector_reset gap on counter decrease, got %+v", gaps)
	}
	if next["conn-1"].Upload != 100 || next["conn-1"].Download != 200 {
		t.Fatalf("next counters not updated after reset: %+v", next["conn-1"])
	}
}

func TestParseConnectionsEmitsLanSourceIPDevices(t *testing.T) {
	prev := map[string]ConnCounters{
		"conn-direct-1": {Upload: 300, Download: 700},
		"conn-proxy-1":  {Upload: 200, Download: 800},
		"conn-proxy-2":  {Upload: 100, Download: 400},
	}
	raw := []byte(`{
		"connections": [
			{"id":"conn-direct-1","upload":400,"download":900,"chains":["DIRECT"],"metadata":{"sourceIP":"192.168.0.10"}},
			{"id":"conn-proxy-1","upload":260,"download":980,"chains":["ss-test","PROXY"],"metadata":{"sourceIP":"192.168.0.10"}},
			{"id":"conn-proxy-2","upload":130,"download":450,"chains":["ss-test","PROXY"],"metadata":{"sourceIP":"10.0.0.20"}}
		]
	}`)
	got, devices, _, gaps, err := ParseConnections(prev, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", gaps)
	}
	if findOutbound(t, got.ByOutbound, "ss-test").ProxyRx != 230 {
		t.Fatalf("node-level proxy must still include all connections: %+v", got.ByOutbound)
	}

	want := map[string]protocol.DeviceDelta{
		"192.168.0.10|DIRECT": {
			ObsIP: "192.168.0.10", Source: protocol.SourceMihomo, Outbound: "DIRECT",
			RxDelta: 200, TxDelta: 100,
		},
		"192.168.0.10|ss-test": {
			ObsIP: "192.168.0.10", Source: protocol.SourceMihomo, Outbound: "ss-test",
			RxDelta: 180, TxDelta: 60,
		},
		"10.0.0.20|ss-test": {
			ObsIP: "10.0.0.20", Source: protocol.SourceMihomo, Outbound: "ss-test",
			RxDelta: 50, TxDelta: 30,
		},
	}
	if len(devices) != len(want) {
		t.Fatalf("devices = %+v, want %d", devices, len(want))
	}
	for _, delta := range devices {
		key := delta.ObsIP + "|" + delta.Outbound
		expected, ok := want[key]
		if !ok || delta != expected {
			t.Fatalf("device %q = %+v, want %+v", key, delta, expected)
		}
	}
}

func TestParseConnectionsIgnoresNonLanSourceIP(t *testing.T) {
	prev := map[string]ConnCounters{
		"conn-proxy-1": {Upload: 200, Download: 800},
		"conn-proxy-2": {Upload: 100, Download: 400},
		"conn-proxy-3": {Upload: 50, Download: 50},
	}
	raw := []byte(`{
		"connections": [
			{"id":"conn-proxy-1","upload":260,"download":980,"chains":["ss-test","PROXY"],"metadata":{"sourceIP":"203.0.113.10"}},
			{"id":"conn-proxy-2","upload":130,"download":450,"chains":["ss-test","PROXY"]},
			{"id":"conn-proxy-3","upload":80,"download":90,"chains":["ss-test","PROXY"],"metadata":{"sourceIP":"8.8.8.8","destinationIP":"1.1.1.1"}}
		]
	}`)
	got, devices, _, _, err := ParseConnections(prev, raw)
	if err != nil {
		t.Fatal(err)
	}
	proxy := findOutbound(t, got.ByOutbound, "ss-test")
	if proxy.ProxyRx != 270 || proxy.ProxyTx != 120 {
		t.Fatalf("missing sourceIP must still count at node level: %+v", proxy)
	}
	if len(devices) != 0 {
		t.Fatalf("public or missing sourceIP must not create devices: %+v", devices)
	}
}

func TestCollectorUsesLocalHTTPClient(t *testing.T) {
	raw := loadFixture(t, "mihomo-connections.json")
	server := httptestServer(t, raw)
	defer server.Close()

	collector := NewCollector(NewClient(server.URL, "test-secret", server.Client()))
	at := time.Unix(1_700_000_000, 0).UTC()
	if proxy, _, gaps, err := collector.Collect(context.Background(), at); err != nil {
		t.Fatal(err)
	} else if len(gaps) != 0 || proxy != nil {
		t.Fatalf("baseline must emit nil proxy and zero gaps, got proxy=%+v gaps=%+v", proxy, gaps)
	}

	updated := []byte(`{"connections":[{"id":"conn-direct-1","upload":400,"download":900,"chains":["DIRECT"]}]}`)
	server.SetResponse(updated)

	proxy, _, gaps, err := collector.Collect(context.Background(), at.Add(15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("normal closure of proxy conns must not emit gaps, got %+v", gaps)
	}
	if proxy == nil || len(proxy.ByOutbound) != 1 || proxy.ByOutbound[0].Name != "DIRECT" {
		t.Fatalf("proxy delta = %+v", proxy)
	}
}

func TestParseConnectionsWithOptionsExtractsGroupProcessAndOptInDomains(t *testing.T) {
	prev := map[string]ConnCounters{}
	raw := []byte(`{
		"connections": [
			{
				"id":"c1","upload":10,"download":100,"chains":["ss-test","AI"],
				"metadata":{"process":"","processPath":"C:\\Program Files\\Synthetic\\app.exe","host":"a.b.example.co.uk:443"}
			},
			{
				"id":"c2","upload":20,"download":200,"chains":["ss-test","PROXY"],
				"metadata":{"process":"/usr/local/bin/curl","host":"203.0.113.5","sniffHost":"v2.api.watched.example.org"}
			},
			{
				"id":"c3","upload":5,"download":30,"chains":["DIRECT"],
				"metadata":{"process":"python3","host":"sub.tail1.example.com"}
			},
			{
				"id":"c4","upload":5,"download":10,"chains":["DIRECT"],
				"metadata":{"process":"python3","host":"sub.tail2.example.net"}
			},
			{
				"id":"c5","upload":10,"download":50,"chains":["DIRECT"],
				"metadata":{"process":"mdns","host":"[fe80::1%eth0]:443"}
			}
		]
	}`)

	// 1. Default options: domains disabled, group and process extracted.
	snapOff, _, err := ParseConnectionsWithOptions(prev, raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if dim := findDimension(snapOff.Dimensions, "domain", "example.co.uk"); dim != nil {
		t.Fatalf("domain collection must be disabled by default, got %+v", snapOff.Dimensions)
	}
	if dim := findDimension(snapOff.Dimensions, "group", "AI"); dim == nil || dim.RxDelta != 100 || dim.TxDelta != 10 {
		t.Fatalf("missing group AI dimension: %+v", snapOff.Dimensions)
	}
	if dim := findDimension(snapOff.Dimensions, "group", "PROXY"); dim == nil || dim.RxDelta != 200 || dim.TxDelta != 20 {
		t.Fatalf("missing group PROXY dimension: %+v", snapOff.Dimensions)
	}
	if dim := findDimension(snapOff.Dimensions, "process", "app.exe"); dim == nil || dim.RxDelta != 100 {
		t.Fatalf("missing Windows basename process dimension: %+v", snapOff.Dimensions)
	}
	if dim := findDimension(snapOff.Dimensions, "process", "curl"); dim == nil || dim.RxDelta != 200 {
		t.Fatalf("missing POSIX basename process dimension: %+v", snapOff.Dimensions)
	}

	// 2. Opt-in domain collection with TopN=1 and watch_domains preservation.
	snapOn, _, err := ParseConnectionsWithOptions(prev, raw, Options{
		CollectDomains: true,
		WatchDomains:   []string{"api.watched.example.org"},
		DomainTopN:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dim := findDimension(snapOn.Dimensions, "domain", "api.watched.example.org"); dim == nil || dim.RxDelta != 200 {
		t.Fatalf("watched domain must be preserved outside other: %+v", snapOn.Dimensions)
	}
	if dim := findDimension(snapOn.Dimensions, "domain", "example.co.uk"); dim == nil || dim.RxDelta != 100 {
		t.Fatalf("top domain eTLD+1 mismatch: %+v", snapOn.Dimensions)
	}
	if dim := findDimension(snapOn.Dimensions, "domain", "other"); dim == nil || dim.RxDelta != 40 || dim.TxDelta != 10 {
		t.Fatalf("tail domains should fold into other (rx=40, tx=10): %+v", snapOn.Dimensions)
	}
	if dim := findDimension(snapOn.Dimensions, "domain", "fe80::1%eth0"); dim != nil {
		t.Fatalf("zoned IPv6 literal must not be emitted as domain: %+v", snapOn.Dimensions)
	}

	// 3. WatchDomains-only (CollectDomains == false) collects ONLY watched domains.
	snapWatchOnly, _, err := ParseConnectionsWithOptions(prev, raw, Options{
		CollectDomains: false,
		WatchDomains:   []string{"api.watched.example.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dim := findDimension(snapWatchOnly.Dimensions, "domain", "api.watched.example.org"); dim == nil || dim.RxDelta != 200 {
		t.Fatalf("watched domain missing in watch-only mode: %+v", snapWatchOnly.Dimensions)
	}
	if dim := findDimension(snapWatchOnly.Dimensions, "domain", "example.co.uk"); dim != nil {
		t.Fatalf("non-watched domain leaked when collect_domains=false: %+v", snapWatchOnly.Dimensions)
	}
}

func TestCollectorWebSocketStreamingAndReconnectGap(t *testing.T) {
	var connCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("websocket accept: %v", err)
			return
		}
		defer ws.CloseNow()

		attempt := connCount.Add(1)
		ctx := r.Context()
		if attempt == 1 {
			// Baseline frame.
			_ = ws.Write(ctx, websocket.MessageText, []byte(`{"connections":[{"id":"c1","upload":10,"download":20,"chains":["ss-test","PROXY"]}]}`))
			// Update frame + short-lived connection.
			_ = ws.Write(ctx, websocket.MessageText, []byte(`{"connections":[{"id":"c1","upload":30,"download":70,"chains":["ss-test","PROXY"]},{"id":"c2","upload":5,"download":15,"chains":["DIRECT"]}]}`))
			time.Sleep(30 * time.Millisecond)
			// Force drop to trigger reconnect + single GapCollectorReset on recovery.
			_ = ws.Close(websocket.StatusGoingAway, "restart")
			return
		}
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"connections":[{"id":"c1","upload":40,"download":90,"chains":["ss-test","PROXY"]}]}`))
		<-ctx.Done()
	}))
	defer server.Close()

	col := NewCollectorWithOptions(
		NewClient(server.URL, "test-secret", server.Client()),
		Options{ReconnectBackoff: 15 * time.Millisecond, WSIdleTimeout: time.Second},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	col.Start(ctx)
	defer col.Close()

	// Wait for attempt 2 to establish and emit recovery frame.
	deadline := time.Now().Add(3 * time.Second)
	var combined ProxyStats
	for time.Now().Before(deadline) {
		snap, err := col.CollectSnapshot(ctx, time.Now().UTC())
		if err == nil {
			if snap.Proxy != nil {
				for _, ob := range snap.Proxy.ByOutbound {
					combined.add(ob)
				}
			}
			combined.gaps += len(snap.Gaps)
			if connCount.Load() >= 2 && combined.proxyRx >= 70 && combined.gaps >= 1 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if combined.proxyRx != 70 || combined.proxyTx != 30 {
		t.Fatalf("ws accumulated proxy rx/tx = (%d,%d), want (70,30)", combined.proxyRx, combined.proxyTx)
	}
	if combined.directRx != 15 || combined.directTx != 5 {
		t.Fatalf("ws accumulated direct rx/tx = (%d,%d), want (15,5)", combined.directRx, combined.directTx)
	}
	if combined.gaps != 1 {
		t.Fatalf("expected exactly 1 GapCollectorReset for WS reconnect, got %d", combined.gaps)
	}
}

func TestCollectorHTTPFallbackWhenWSUnsupported(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusOK} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			var pollCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Upgrade") == "websocket" {
					w.WriteHeader(status)
					return
				}
				n := pollCount.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if n == 1 {
					_, _ = w.Write([]byte(`{"connections":[{"id":"c1","upload":10,"download":20,"chains":["ss-test","PROXY"]}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"connections":[{"id":"c1","upload":50,"download":120,"chains":["ss-test","PROXY"]}]}`))
			}))
			defer server.Close()

			col := NewCollectorWithOptions(
				NewClient(server.URL, "test-secret", server.Client()),
				Options{FallbackPollInterval: 15 * time.Millisecond},
			)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			col.Start(ctx)
			defer col.Close()

			deadline := time.Now().Add(2 * time.Second)
			var proxyRx uint64
			for time.Now().Before(deadline) {
				snap, err := col.CollectSnapshot(ctx, time.Now().UTC())
				if err == nil && snap.Proxy != nil {
					for _, ob := range snap.Proxy.ByOutbound {
						proxyRx += ob.ProxyRx
					}
					if proxyRx >= 100 {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
			if proxyRx != 100 {
				t.Fatalf("HTTP fallback proxyRx = %d, want 100 (polls=%d)", proxyRx, pollCount.Load())
			}
		})
	}
}

func TestFetchProxiesAndProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/proxies":
			_, _ = w.Write([]byte(`{
				"proxies": {
					"PROXY": {"name":"PROXY","type":"Selector","now":"ss-test","all":["ss-test","DIRECT"]},
					"ss-test": {"name":"ss-test","type":"Shadowsocks"}
				}
			}`))
		case "/providers/proxies":
			_, _ = w.Write([]byte(`{
				"providers": {
					"default": {
						"name": "default",
						"vehicleType": "Compatible",
						"proxies": [{"name":"DIRECT"},{"name":"REJECT"}]
					},
					"sub-alpha": {
						"name": "sub-alpha",
						"vehicleType": "HTTP",
						"subscriptionInfo": {"Upload": 100, "Download": 400, "Total": 10000, "Expire": 1800000000},
						"proxies": [{"name":"ss-test"},{"name":"vmess-test"}]
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-secret", server.Client())
	proxies, err := client.FetchProxies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 2 {
		t.Fatalf("got %d proxies, want 2: %+v", len(proxies), proxies)
	}
	if proxies[0].Name != "PROXY" || !proxies[0].IsGroup || proxies[0].Now != "ss-test" || len(proxies[0].All) != 2 {
		t.Fatalf("unexpected group proxy: %+v", proxies[0])
	}
	if proxies[1].Name != "ss-test" || proxies[1].IsGroup {
		t.Fatalf("unexpected leaf proxy: %+v", proxies[1])
	}

	providers, err := client.FetchProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("got %d providers, want 2: %+v", len(providers), providers)
	}
	if providers[0].Name != "default" || providers[0].VehicleType != "Compatible" || providers[0].SubscriptionInfo != nil {
		t.Fatalf("unexpected Compatible provider: %+v", providers[0])
	}
	sub := providers[1]
	if sub.Name != "sub-alpha" || sub.VehicleType != "HTTP" || sub.SubscriptionInfo == nil {
		t.Fatalf("unexpected HTTP provider: %+v", sub)
	}
	if sub.SubscriptionInfo.Upload != 100 || sub.SubscriptionInfo.Download != 400 || sub.SubscriptionInfo.Total != 10000 || sub.SubscriptionInfo.Expire != 1800000000 {
		t.Fatalf("subscriptionInfo mismatch: %+v", sub.SubscriptionInfo)
	}
	if len(sub.Proxies) != 2 || sub.Proxies[0] != "ss-test" {
		t.Fatalf("provider proxies mismatch: %+v", sub.Proxies)
	}
}

type ProxyStats struct {
	proxyRx  uint64
	proxyTx  uint64
	directRx uint64
	directTx uint64
	gaps     int
}

func (p *ProxyStats) add(ob protocol.OutboundDelta) {
	p.proxyRx += ob.ProxyRx
	p.proxyTx += ob.ProxyTx
	p.directRx += ob.DirectRx
	p.directTx += ob.DirectTx
}

func findOutbound(t *testing.T, outbounds []protocol.OutboundDelta, name string) protocol.OutboundDelta {
	t.Helper()
	for _, outbound := range outbounds {
		if outbound.Name == name {
			return outbound
		}
	}
	t.Fatalf("outbound %q not found in %+v", name, outbounds)
	return protocol.OutboundDelta{}
}

func findDimension(dims []DimensionDelta, dimType, key string) *DimensionDelta {
	for i := range dims {
		if dims[i].Type == dimType && dims[i].Key == key {
			return &dims[i]
		}
	}
	return nil
}

type testMihomoServer struct {
	*httptest.Server
	mu       sync.Mutex
	response []byte
}

func (s *testMihomoServer) SetResponse(raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.response = append([]byte(nil), raw...)
}

func httptestServer(t *testing.T, initial []byte) *testMihomoServer {
	t.Helper()
	server := &testMihomoServer{response: append([]byte(nil), initial...)}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		server.mu.Lock()
		defer server.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(server.response)
	}))
	return server
}

func TestCleanHostFiltersZonedIPv6AndAllIPLiterals(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"[fe80::1%eth0]:443", ""},
		{"[fe80::1%eth0]", ""},
		{"[fe80::1%eth0].", ""},
		{"[fe80::1%]:443", ""},
		{"fe80::1%eth0", ""},
		{"[2001:db8::1]:443", ""},
		{"[2001:db8::1].", ""},
		{"2001:db8::1", ""},
		{"203.0.113.5:8080", ""},
		{"203.0.113.5", ""},
		{"a.b.example.co.uk:443", "a.b.example.co.uk"},
		{"Sub.Example.COM.", "sub.example.com"},
		{"example.com:443.", "example.com"},
	}
	for _, tc := range cases {
		if got := cleanHost(tc.raw); got != tc.want {
			t.Errorf("cleanHost(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSetDomainOptionsPreservesTopNAndTimeouts(t *testing.T) {
	col := NewCollectorWithOptions(NewClient("http://127.0.0.1:9090", "", nil), Options{
		DomainTopN:           12,
		DialTimeout:          3 * time.Second,
		WSIdleTimeout:        4 * time.Second,
		FallbackPollInterval: 50 * time.Millisecond,
		ReconnectBackoff:     25 * time.Millisecond,
	})
	col.SetDomainOptions(true, []string{"api.example.test"})
	col.mu.Lock()
	if !col.opts.CollectDomains || len(col.opts.WatchDomains) != 1 || col.opts.WatchDomains[0] != "api.example.test" {
		col.mu.Unlock()
		t.Fatalf("domain options not applied: %+v", col.opts)
	}
	if col.opts.DomainTopN != 12 || col.opts.DialTimeout != 3*time.Second || col.opts.WSIdleTimeout != 4*time.Second {
		col.mu.Unlock()
		t.Fatalf("SetDomainOptions clobbered non-domain options: %+v", col.opts)
	}
	col.mu.Unlock()

	col.SetDomainOptions(false, nil)
	col.mu.Lock()
	defer col.mu.Unlock()
	if col.opts.CollectDomains || len(col.opts.WatchDomains) != 0 || col.opts.DomainTopN != 12 {
		t.Fatalf("SetDomainOptions revert mismatch: %+v", col.opts)
	}
}

func TestWatchDomainsExactKeywordRegexWithoutCollectDomains(t *testing.T) {
	opts := Options{
		CollectDomains: false,
		WatchDomains: []string{
			"api.watched.test",
			"kw:openai",
			`re:^chat\.[a-z]+\.example$`,
		},
	}
	base := map[string]ConnCounters{"b": {Upload: 1, Download: 1}}
	frame := []byte(`{"connections":[
		{"id":"c1","upload":100,"download":200,"chains":["hk-01","Proxy"],"metadata":{"host":"sub.api.watched.test"}},
		{"id":"c2","upload":300,"download":400,"chains":["hk-01","Proxy"],"metadata":{"host":"api.openai.com"}},
		{"id":"c3","upload":500,"download":600,"chains":["hk-01","Proxy"],"metadata":{"host":"chat.ai.example"}},
		{"id":"c4","upload":700,"download":800,"chains":["hk-01","Proxy"],"metadata":{"host":"private.unwatched.example"}}
	]}`)

	snap, _, err := ParseConnectionsWithOptions(base, frame, opts)
	if err != nil {
		t.Fatalf("ParseConnectionsWithOptions: %v", err)
	}
	if got := findDimension(snap.Dimensions, "domain", "api.watched.test"); got == nil || got.RxDelta != 200 || got.TxDelta != 100 {
		t.Fatalf("exact watched domain mismatch: %+v", snap.Dimensions)
	}
	if got := findDimension(snap.Dimensions, "domain", "api.openai.com"); got == nil || got.RxDelta != 400 || got.TxDelta != 300 {
		t.Fatalf("keyword watched domain mismatch: %+v", snap.Dimensions)
	}
	if got := findDimension(snap.Dimensions, "domain", "chat.ai.example"); got == nil || got.RxDelta != 600 || got.TxDelta != 500 {
		t.Fatalf("regex watched domain mismatch: %+v", snap.Dimensions)
	}
	if leaked := findDimension(snap.Dimensions, "domain", "unwatched.example"); leaked != nil {
		t.Fatalf("unwatched domain leaked: %+v", leaked)
	}
	if other := findDimension(snap.Dimensions, "domain", "other"); other != nil {
		t.Fatalf("unexpected other domain bucket while CollectDomains=false: %+v", other)
	}
}

func TestWatchDomainsExactParentDoesNotMaskKeywordOrRegex(t *testing.T) {
	opts := Options{
		CollectDomains: false,
		WatchDomains: []string{
			"cdn.example.com",
			"kw:video",
			`re:^audio\.cdn\.example\.com$`,
		},
	}
	base := map[string]ConnCounters{"b": {Upload: 1, Download: 1}}
	frame := []byte(`{"connections":[
		{"id":"c1","upload":200,"download":800,"chains":["hk-01","Proxy"],"metadata":{"host":"video.cdn.example.com"}},
		{"id":"c2","upload":300,"download":700,"chains":["hk-01","Proxy"],"metadata":{"host":"audio.cdn.example.com"}},
		{"id":"c3","upload":500,"download":500,"chains":["hk-01","Proxy"],"metadata":{"host":"unmatched.example.net"}}
	]}`)

	snap, _, err := ParseConnectionsWithOptions(base, frame, opts)
	if err != nil {
		t.Fatalf("ParseConnectionsWithOptions: %v", err)
	}
	if got := findDimension(snap.Dimensions, "domain", "video.cdn.example.com"); got == nil || got.RxDelta != 800 || got.TxDelta != 200 {
		t.Fatalf("keyword subdomain masked or missing: %+v", snap.Dimensions)
	}
	if got := findDimension(snap.Dimensions, "domain", "audio.cdn.example.com"); got == nil || got.RxDelta != 700 || got.TxDelta != 300 {
		t.Fatalf("regex subdomain masked or missing: %+v", snap.Dimensions)
	}
	if collapsed := findDimension(snap.Dimensions, "domain", "cdn.example.com"); collapsed != nil {
		t.Fatalf("expected subdomain keys preserved instead of collapsed to parent: %+v", snap.Dimensions)
	}
	if leaked := findDimension(snap.Dimensions, "domain", "example.net"); leaked != nil {
		t.Fatalf("unmatched domain leaked: %+v", leaked)
	}
	if leakedRaw := findDimension(snap.Dimensions, "domain", "unmatched.example.net"); leakedRaw != nil {
		t.Fatalf("unmatched domain leaked: %+v", leakedRaw)
	}
	if other := findDimension(snap.Dimensions, "domain", "other"); other != nil {
		t.Fatalf("unexpected other bucket while CollectDomains=false: %+v", other)
	}
}

func TestClientQueryTokenAndWSSchemeNormalization(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Query().Get("token") != "syn-tok" && r.Header.Get("Authorization") != "Bearer syn-hdr" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/version":
			_, _ = w.Write([]byte(`{"meta":true,"version":"v1.18.0-syn"}`))
		case "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{"HK-01":{"name":"HK-01","type":"Shadowsocks"},"Proxy":{"name":"Proxy","type":"Selector","all":["HK-01"]}}}`))
		case "/providers/proxies":
			_, _ = w.Write([]byte(`{"providers":{"SubA":{"name":"SubA","vehicleType":"HTTP","proxies":[{"name":"HK-01"}]}}}`))
		case "/connections":
			http.Error(w, "unexpected /connections call during HTTP probe", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// HTTP base URL with ?token=... query parameter.
	c1 := NewClient(srv.URL+"?token=syn-tok", "", nil)
	res := c1.ProbeHTTP(context.Background())
	if !res.OK || res.Version != "v1.18.0-syn" || !res.Meta || res.ProxyCount != 2 || res.GroupCount != 1 {
		t.Fatalf("ProbeHTTP with query token failed: %+v", res)
	}
	if _, err := c1.FetchProviders(context.Background()); err != nil {
		t.Fatalf("FetchProviders with query token: %v", err)
	}

	// ws:// base URL normalizes to http:// for REST calls.
	wsBase := "ws" + srv.URL[len("http"):]
	c2 := NewClient(wsBase, "syn-hdr", nil)
	res2 := c2.ProbeHTTP(context.Background())
	if !res2.OK || res2.ProxyCount != 2 {
		t.Fatalf("ProbeHTTP with ws:// base failed: %+v", res2)
	}

	// Verify /connections was NEVER touched during ProbeHTTP.
	mu.Lock()
	defer mu.Unlock()
	for _, req := range seen {
		if req == "GET /connections?" {
			t.Fatalf("ProbeHTTP must never hit /connections, got %v", seen)
		}
	}
}

func TestProbeHTTPAuthFailureAndLegacy404Version(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-sec" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/version" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/proxies" {
			_, _ = w.Write([]byte(`{"proxies":{"HK-01":{"name":"HK-01","type":"Shadowsocks"}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	bad := NewClient(srv.URL, "wrong-sec", nil).ProbeHTTP(context.Background())
	if bad.OK || bad.Error == "" {
		t.Fatalf("expected unauthorized failure, got %+v", bad)
	}

	legacy := NewClient(srv.URL, "good-sec", nil).ProbeHTTP(context.Background())
	if !legacy.OK || legacy.ProxyCount != 1 {
		t.Fatalf("expected legacy 404 /version fallback to succeed via /proxies, got %+v", legacy)
	}
}

func TestCollectorInitialCountersAndSnapshotCounters(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/connections" {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write([]byte(`{"connections":[
				{"id":"b","upload":10,"download":10,"chains":["DIRECT"]},
				{"id":"c2","upload":250,"download":250,"chains":["HK-01","Proxy"]}
			]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	seed := map[string]ConnCounters{"b": {Upload: 10, Download: 10}}
	col := NewCollectorWithOptions(NewClient(srv.URL, "", nil), Options{
		InitialCounters: seed,
	})
	snap, err := col.CollectSnapshot(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Proxy == nil || len(snap.Proxy.ByOutbound) != 1 || snap.Proxy.ByOutbound[0].ProxyRx != 250 || snap.Proxy.ByOutbound[0].ProxyTx != 250 {
		t.Fatalf("expected c2 delta 250/250 with seeded baseline, got %+v", snap.Proxy)
	}
	if connected, lastErr := col.Status(); !connected || lastErr != nil {
		t.Fatalf("Status = (%v, %v), want (true, nil)", connected, lastErr)
	}
	if col.LastFrameAt().IsZero() {
		t.Fatal("expected LastFrameAt to be set")
	}
	copied := col.SnapshotCounters()
	if copied["c2"].Download != 250 || copied["b"].Upload != 10 {
		t.Fatalf("unexpected SnapshotCounters: %+v", copied)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	if err := col.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("hits after PollOnce = %d, want 2", hits)
	}
}

func TestCollectorMalformedWSFrameDoesNotEmitFalseGap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()

		frames := []string{
			`{"connections":[{"id":"b","upload":1,"download":1,"chains":["DIRECT"]}]}`,
			`{not-valid-json`,
			`{"connections":[{"id":"b","upload":1,"download":1,"chains":["DIRECT"]},{"id":"c1","upload":10,"download":50,"chains":["HK-01","Proxy"]}]}`,
		}
		for _, f := range frames {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(f)); err != nil {
				return
			}
		}
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	col := NewCollectorWithOptions(NewClient(srv.URL, "", nil), Options{
		WSIdleTimeout: 5 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	col.Start(ctx)
	defer col.Close()

	deadline := time.Now().Add(4 * time.Second)
	var totalProxyRx uint64
	var totalGaps int
	for time.Now().Before(deadline) {
		snap, err := col.CollectSnapshot(ctx, time.Now().UTC())
		if err == nil {
			totalGaps += len(snap.Gaps)
			if snap.Proxy != nil {
				for _, ob := range snap.Proxy.ByOutbound {
					totalProxyRx += ob.ProxyRx
				}
			}
			if totalProxyRx >= 50 {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if totalProxyRx != 50 || totalGaps != 0 {
		t.Fatalf("malformed WS frame triggered proxyRx=%d gaps=%d, want proxyRx=50 gaps=0", totalProxyRx, totalGaps)
	}
}

func TestParseConnectionsSimultaneousResetDeduplicatesFrameGap(t *testing.T) {
	prev := map[string]ConnCounters{
		"c1": {Upload: 100, Download: 100},
		"c2": {Upload: 200, Download: 200},
		"c3": {Upload: 300, Download: 300},
	}
	frame := []byte(`{"connections":[
		{"id":"c1","upload":1,"download":1,"chains":["HK-01","Proxy"]},
		{"id":"c2","upload":2,"download":2,"chains":["HK-01","Proxy"]},
		{"id":"c3","upload":3,"download":3,"chains":["HK-01","Proxy"]}
	]}`)
	_, _, _, gaps, err := ParseConnections(prev, frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Reason != protocol.GapCollectorReset {
		t.Fatalf("simultaneous resets emitted %d gaps (%+v), want exactly 1", len(gaps), gaps)
	}
}

func TestMergedFrameAndHTTPFallbackErrorsAndProbeHTTP5xx(t *testing.T) {
	// 1. parsedFrame.mergeFrom accumulates overlapping outbounds, devices, groups, processes, domains, gaps.
	p1 := newParsedFrame()
	p1.byOutbound["HK-01"] = &protocol.OutboundDelta{Name: "HK-01", ProxyRx: 10, ProxyTx: 5}
	p1.byDevice[deviceKey{ip: "203.0.113.10", outbound: "HK-01"}] = &protocol.DeviceDelta{ObsIP: "203.0.113.10", Source: protocol.SourceMihomo, Outbound: "HK-01", RxDelta: 10, TxDelta: 5}
	addDimension(p1.byGroup, "group", "Proxy", 10, 5)
	addDimension(p1.byProcess, "process", "curl", 10, 5)
	p1.byDomain["a.test"] = &domainAgg{delta: DimensionDelta{Type: "domain", Key: "a.test", RxDelta: 10, TxDelta: 5}}
	p1.gaps = append(p1.gaps, protocol.Gap{Reason: protocol.GapCollectorReset})

	p2 := newParsedFrame()
	p2.byOutbound["HK-01"] = &protocol.OutboundDelta{Name: "HK-01", Group: "Proxy", ProxyRx: 20, ProxyTx: 10}
	p2.byDevice[deviceKey{ip: "203.0.113.10", outbound: "HK-01"}] = &protocol.DeviceDelta{ObsIP: "203.0.113.10", Source: protocol.SourceMihomo, Outbound: "HK-01", RxDelta: 20, TxDelta: 10}
	addDimension(p2.byGroup, "group", "Proxy", 20, 10)
	addDimension(p2.byProcess, "process", "curl", 20, 10)
	p2.byDomain["a.test"] = &domainAgg{delta: DimensionDelta{Type: "domain", Key: "a.test", RxDelta: 20, TxDelta: 10}, watched: true}
	p2.gaps = append(p2.gaps, protocol.Gap{Reason: protocol.GapCollectorReset})

	p1.mergeFrom(p2)
	snap := p1.toSnapshot(10)
	if snap.Proxy == nil || snap.Proxy.ByOutbound[0].ProxyRx != 30 || snap.Proxy.ByOutbound[0].Group != "Proxy" {
		t.Fatalf("merged outbound mismatch: %+v", snap.Proxy)
	}
	if len(snap.Devices) != 1 || snap.Devices[0].RxDelta != 30 {
		t.Fatalf("merged device mismatch: %+v", snap.Devices)
	}
	if len(snap.Gaps) != 2 {
		t.Fatalf("merged gaps = %d, want 2", len(snap.Gaps))
	}

	// 2. HTTP fallback recovers from transient 500.
	var pollStep atomic.Int32
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/connections" {
			step := pollStep.Add(1)
			switch step {
			case 1:
				_, _ = w.Write([]byte(`{"connections":[{"id":"b","upload":1,"download":1,"chains":["DIRECT"]}]}`))
			case 2:
				http.Error(w, "transient error", http.StatusInternalServerError)
			default:
				_, _ = w.Write([]byte(`{"connections":[{"id":"b","upload":1,"download":1,"chains":["DIRECT"]},{"id":"c1","upload":15,"download":35,"chains":["HK-01","Proxy"]}]}`))
			}
			return
		}
		http.Error(w, "upstream failure", http.StatusInternalServerError)
	}))
	defer fallbackSrv.Close()

	fbCol := NewCollectorWithOptions(NewClient(fallbackSrv.URL, "", nil), Options{
		FallbackPollInterval: 20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fbCol.Start(ctx)
	defer fbCol.Close()

	var gotRx uint64
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		s, err := fbCol.CollectSnapshot(ctx, time.Now().UTC())
		if err == nil && s.Proxy != nil {
			for _, ob := range s.Proxy.ByOutbound {
				gotRx += ob.ProxyRx
			}
			if gotRx >= 35 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if gotRx != 35 {
		t.Fatalf("HTTP fallback recovery proxyRx = %d, want 35", gotRx)
	}

	// 3. ProbeHTTP on 500 and closed connection.
	probe500 := NewClient(fallbackSrv.URL, "", nil).ProbeHTTP(context.Background())
	if probe500.OK || !strings.Contains(probe500.Error, "500") {
		t.Fatalf("ProbeHTTP 500 = %+v", probe500)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()
	probeClosed := NewClient("http://"+closedAddr, "", nil).ProbeHTTP(context.Background())
	if probeClosed.OK || probeClosed.Error == "" {
		t.Fatalf("ProbeHTTP closed = %+v", probeClosed)
	}

	// 4. etld1OrCandidate edge case on single-label host.
	if got := etld1OrCandidate("localhost"); got != "localhost" {
		t.Fatalf("etld1OrCandidate(localhost) = %q, want localhost", got)
	}
}
