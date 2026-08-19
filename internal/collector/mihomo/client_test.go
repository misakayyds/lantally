package mihomo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	first, _, _, _, err := ParseConnections(nil, loadFixture(t, "mihomo-connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ByOutbound) != 0 {
		t.Fatalf("first sample should establish baseline, got %+v", first.ByOutbound)
	}

	secondSnapshot := []byte(`{
		"connections": [
			{"id":"conn-direct-1","upload":400,"download":900,"chains":["DIRECT"]},
			{"id":"conn-proxy-1","upload":260,"download":980,"chains":["PROXY","ss-test"]},
			{"id":"conn-proxy-2","upload":130,"download":450,"chains":["PROXY","ss-test"]}
		]
	}`)
	prev := map[string]ConnCounters{
		"conn-direct-1": {Upload: 300, Download: 700},
		"conn-proxy-1":  {Upload: 200, Download: 800},
		"conn-proxy-2":  {Upload: 100, Download: 400},
	}

	got, devices, _, gaps, err := ParseConnections(prev, secondSnapshot)
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
	if direct.DirectRx != 200 || direct.DirectTx != 100 {
		t.Fatalf("direct delta = %+v, want rx=200 tx=100", direct)
	}
	if proxy.ProxyRx != 230 || proxy.ProxyTx != 90 {
		t.Fatalf("proxy delta = %+v, want rx=230 tx=90", proxy)
	}
}

func TestParseConnectionsEmitsGapForDisappearingConnection(t *testing.T) {
	prev := map[string]ConnCounters{
		"conn-direct-1": {Upload: 300, Download: 700},
	}
	raw := []byte(`{"connections":[{"id":"conn-direct-1","upload":350,"download":750,"chains":["DIRECT"]}]}`)
	first, _, next, _, err := ParseConnections(prev, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ByOutbound) != 1 {
		t.Fatalf("expected one outbound delta, got %+v", first.ByOutbound)
	}

	closed := []byte(`{"connections":[]}`)
	_, _, _, gaps, err := ParseConnections(next, closed)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Reason != protocol.GapCollectorReset {
		t.Fatalf("expected disappearance gap, got %+v", gaps)
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
			{"id":"conn-proxy-1","upload":260,"download":980,"chains":["PROXY","ss-test"],"metadata":{"sourceIP":"192.168.0.10"}},
			{"id":"conn-proxy-2","upload":130,"download":450,"chains":["PROXY","ss-test"],"metadata":{"sourceIP":"10.0.0.20"}}
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
			{"id":"conn-proxy-1","upload":260,"download":980,"chains":["PROXY","ss-test"],"metadata":{"sourceIP":"203.0.113.10"}},
			{"id":"conn-proxy-2","upload":130,"download":450,"chains":["PROXY","ss-test"]},
			{"id":"conn-proxy-3","upload":80,"download":90,"chains":["PROXY","ss-test"],"metadata":{"sourceIP":"8.8.8.8","destinationIP":"1.1.1.1"}}
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
	if _, _, gaps, err := collector.Collect(context.Background(), at); err != nil {
		t.Fatal(err)
	} else if len(gaps) != 0 {
		t.Fatalf("baseline gaps = %+v", gaps)
	}

	updated := []byte(`{"connections":[{"id":"conn-direct-1","upload":400,"download":900,"chains":["DIRECT"]}]}`)
	server.SetResponse(updated)

	proxy, _, gaps, err := collector.Collect(context.Background(), at.Add(15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 2 {
		t.Fatalf("expected disappearance gaps for vanished proxy conns, got %+v", gaps)
	}
	if proxy == nil || len(proxy.ByOutbound) != 1 {
		t.Fatalf("proxy delta = %+v", proxy)
	}
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
