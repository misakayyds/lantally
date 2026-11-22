package accounting

import (
	"testing"

	"github.com/misakayyds/lantally/internal/protocol"
)

func TestNodeIncrementsPrefersNlbwmonOverIfaceAndMihomo(t *testing.T) {
	batch := protocol.Batch{
		Capabilities: []protocol.Capability{protocol.CapNlbwmon, protocol.CapIface, protocol.CapMihomo},
		Interfaces: []protocol.IfaceDelta{
			{Name: "eth0", RxDelta: 100, TxDelta: 40},
		},
		Devices: []protocol.DeviceDelta{
			{ObsIP: "203.0.113.1", ObsMAC: "02:00:00:00:00:01", RxDelta: 50, TxDelta: 10, Source: protocol.SourceNlbwmon},
			{ObsIP: "203.0.113.2", ObsMAC: "02:00:00:00:00:02", RxDelta: 25, TxDelta: 5, Source: protocol.SourceNlbwmon},
			{ObsIP: "192.168.0.10", RxDelta: 999, TxDelta: 1, Source: protocol.SourceMihomo, Outbound: "proxy-a"},
		},
		Proxy: &protocol.ProxyDelta{
			ByOutbound: []protocol.OutboundDelta{
				{Name: "DIRECT", DirectRx: 8, DirectTx: 2},
				{Name: "proxy-a", ProxyRx: 16, ProxyTx: 4},
			},
		},
	}

	got := NodeIncrements(batch, map[string]float64{"proxy-a": 2})
	totals := classTotals(got)

	if totals[ClassTotal] != 90 {
		t.Fatalf("total = %d, want nlbwmon 50+10+25+5=90, not iface or mihomo", totals[ClassTotal])
	}
	if totals[ClassDirect] != 10 {
		t.Fatalf("direct = %d, want 10", totals[ClassDirect])
	}
	if totals[ClassProxyRaw] != 20 {
		t.Fatalf("proxy_raw = %d, want 20", totals[ClassProxyRaw])
	}
	if totals[ClassProxyAdjusted] != 0 || totals[ClassProxyUnadjusted] != 0 {
		t.Fatalf("ingest increments must not emit stale proxy_adjusted/proxy_unadjusted: %+v", totals)
	}
}

func TestNodeIncrementsFallsBackToIfaceThenMihomo(t *testing.T) {
	ifaceOnly := protocol.Batch{
		Interfaces: []protocol.IfaceDelta{{Name: "eth0", RxDelta: 1024, TxDelta: 512}},
	}
	if totals := classTotals(NodeIncrements(ifaceOnly, nil)); totals[ClassTotal] != 1536 {
		t.Fatalf("iface total = %d, want 1536", totals[ClassTotal])
	}

	mihomoOnly := protocol.Batch{
		Proxy: &protocol.ProxyDelta{
			ByOutbound: []protocol.OutboundDelta{
				{Name: "DIRECT", DirectRx: 3, DirectTx: 1},
				{Name: "unknown", ProxyRx: 5, ProxyTx: 1},
			},
		},
	}
	totals := classTotals(NodeIncrements(mihomoOnly, nil))
	if totals[ClassTotal] != 10 {
		t.Fatalf("mihomo fallback total = %d, want 10", totals[ClassTotal])
	}
	if totals[ClassProxyRaw] != 6 {
		t.Fatalf("mihomo fallback proxy_raw = %d, want 6", totals[ClassProxyRaw])
	}
	if totals[ClassProxyAdjusted] != 0 || totals[ClassProxyUnadjusted] != 0 {
		t.Fatalf("ingest increments must emit raw proxy only, got %+v", totals)
	}
}

func TestNodeIncrementsKeepsPerOutboundProxyBreakdown(t *testing.T) {
	batch := protocol.Batch{
		Devices: []protocol.DeviceDelta{
			{ObsIP: "192.168.0.10", ObsMAC: "02:00:00:00:00:0a", RxDelta: 100, TxDelta: 20, Source: protocol.SourceNlbwmon},
		},
		Proxy: &protocol.ProxyDelta{
			ByOutbound: []protocol.OutboundDelta{
				{Name: "DIRECT", DirectRx: 8, DirectTx: 2},
				{Name: "proxy-a", ProxyRx: 16, ProxyTx: 4},
				{Name: "proxy-b", ProxyRx: 30, ProxyTx: 10},
			},
		},
	}

	got := NodeIncrements(batch, map[string]float64{"proxy-a": 2})
	totals := classTotals(got)
	if totals[ClassTotal] != 120 {
		t.Fatalf("total = %d, want nlbwmon 120, not mihomo 70", totals[ClassTotal])
	}
	if totals[ClassDirect] != 10 {
		t.Fatalf("direct = %d, want 10", totals[ClassDirect])
	}
	if totals[ClassProxyRaw] != 60 {
		t.Fatalf("proxy_raw = %d, want 60", totals[ClassProxyRaw])
	}

	rawByOutbound := outboundTotals(got, ClassProxyRaw)
	if rawByOutbound["proxy-a"] != 20 || rawByOutbound["proxy-b"] != 40 {
		t.Fatalf("proxy_raw by outbound = %+v, want proxy-a=20 proxy-b=40", rawByOutbound)
	}
	if _, ok := rawByOutbound["DIRECT"]; ok {
		t.Fatalf("DIRECT must not appear on proxy_raw: %+v", rawByOutbound)
	}
	if rawByOutbound[""] != 0 {
		t.Fatalf("empty outbound must not duplicate proxy_raw: %+v", rawByOutbound)
	}
	if sum := sumMap(rawByOutbound); sum != totals[ClassProxyRaw] {
		t.Fatalf("outbound sum %d != proxy_raw %d", sum, totals[ClassProxyRaw])
	}
	if len(outboundTotals(got, ClassProxyAdjusted)) != 0 || len(outboundTotals(got, ClassProxyUnadjusted)) != 0 {
		t.Fatalf("expected no ingest-time adjusted/unadjusted increments, got %+v", got)
	}
}

func TestDeviceIncrementsKeepMihomoOutbound(t *testing.T) {
	proxy := protocol.DeviceDelta{
		ObsIP:    "192.168.0.10",
		RxDelta:  9,
		TxDelta:  1,
		Source:   protocol.SourceMihomo,
		Outbound: "proxy-a",
	}
	got := DeviceIncrements(proxy, "dev-a", nil)
	if len(got) != 1 {
		t.Fatalf("mihomo proxy increment = %+v", got)
	}
	if got[0].DeviceID != "dev-a" || got[0].Class != ClassProxyRaw || got[0].Outbound != "proxy-a" || got[0].Rx != 9 || got[0].Tx != 1 {
		t.Fatalf("mihomo proxy raw = %+v", got[0])
	}

	direct := protocol.DeviceDelta{
		ObsIP:    "192.168.0.11",
		RxDelta:  4,
		TxDelta:  2,
		Source:   protocol.SourceMihomo,
		Outbound: "DIRECT",
	}
	got = DeviceIncrements(direct, "dev-b", nil)
	if len(got) != 1 || got[0].Class != ClassDirect || got[0].Outbound != "" || got[0].Rx != 4 || got[0].Tx != 2 {
		t.Fatalf("mihomo direct increment = %+v", got)
	}
}

func TestDeviceIncrementsEmitRawOnlyRegardlessOfMultiplierMap(t *testing.T) {
	proxy := protocol.DeviceDelta{
		ObsIP:    "192.168.0.10",
		RxDelta:  100,
		TxDelta:  0,
		Source:   protocol.SourceMihomo,
		Outbound: "ss-test",
	}
	got := DeviceIncrements(proxy, "dev-a", map[string]float64{"ss-test": 1.5})
	if len(got) != 1 || got[0].Class != ClassProxyRaw || got[0].Rx != 100 || got[0].Tx != 0 {
		t.Fatalf("device increments = %+v, want single ClassProxyRaw", got)
	}
}

func TestDeviceIncrementsSkipNeighBytes(t *testing.T) {
	neigh := protocol.DeviceDelta{
		ObsIP: "203.0.113.1", RxDelta: 2048, TxDelta: 1024, Source: protocol.SourceNeigh,
	}
	if got := DeviceIncrements(neigh, "dev-a", nil); len(got) != 0 {
		t.Fatalf("neigh observations are identity only, got %+v", got)
	}

	nlbw := protocol.DeviceDelta{
		ObsIP: "203.0.113.1", RxDelta: 7, TxDelta: 3, Source: protocol.SourceNlbwmon,
	}
	got := DeviceIncrements(nlbw, "dev-a", nil)
	if len(got) != 1 || got[0].DeviceID != "dev-a" || got[0].Class != ClassTotal || got[0].Rx != 7 || got[0].Tx != 3 {
		t.Fatalf("nlbwmon device increment = %+v", got)
	}
}

func classTotals(increments []Increment) map[string]uint64 {
	out := map[string]uint64{}
	for _, item := range increments {
		if item.DeviceID != "" {
			continue
		}
		out[item.Class] += item.Rx + item.Tx
	}
	return out
}

func outboundTotals(increments []Increment, class string) map[string]uint64 {
	out := map[string]uint64{}
	for _, item := range increments {
		if item.DeviceID != "" || item.Class != class {
			continue
		}
		out[item.Outbound] += item.Rx + item.Tx
	}
	return out
}

func sumMap(values map[string]uint64) uint64 {
	var sum uint64
	for _, value := range values {
		sum += value
	}
	return sum
}

func TestNodeIncrementsExcludesVirtualIfacesFromTotalButPreservesClassIface(t *testing.T) {
	batch := protocol.Batch{
		Interfaces: []protocol.IfaceDelta{
			{Name: "eth0", RxDelta: 1000, TxDelta: 500},
			{Name: "lo", RxDelta: 200, TxDelta: 200},
			{Name: "lo0", RxDelta: 50, TxDelta: 50},
			{Name: "Loopback Pseudo-Interface 1", RxDelta: 300, TxDelta: 300},
			{Name: "tun0", RxDelta: 900, TxDelta: 100},
			{Name: "utun4", RxDelta: 400, TxDelta: 100},
			{Name: "wg0", RxDelta: 250, TxDelta: 50},
			{Name: "tailscale0", RxDelta: 150, TxDelta: 50},
			{Name: "Tailscale", RxDelta: 120, TxDelta: 30},
			{Name: "docker0", RxDelta: 80, TxDelta: 20},
			{Name: "veth1a2b", RxDelta: 60, TxDelta: 40},
			{Name: "br-lan", RxDelta: 700, TxDelta: 300},
		},
	}

	got := NodeIncrements(batch, nil)
	totals := classTotals(got)
	if totals[ClassTotal] != 1500 {
		t.Fatalf("ClassTotal = %d, want 1500 (eth0 only, virtual/loopback excluded)", totals[ClassTotal])
	}

	perIface := ifaceTotalsMap(got)
	for _, iface := range batch.Interfaces {
		if perIface[iface.Name] != iface.RxDelta+iface.TxDelta {
			t.Fatalf("ClassIface %q = %d, want %d", iface.Name, perIface[iface.Name], iface.RxDelta+iface.TxDelta)
		}
	}
}

func TestNodeIncrementsFallsBackToProxyWhenAllIfacesExcluded(t *testing.T) {
	batch := protocol.Batch{
		Interfaces: []protocol.IfaceDelta{
			{Name: "lo", RxDelta: 500, TxDelta: 500},
			{Name: "utun4", RxDelta: 200, TxDelta: 100},
		},
		Proxy: &protocol.ProxyDelta{
			ByOutbound: []protocol.OutboundDelta{
				{Name: "DIRECT", DirectRx: 30, DirectTx: 10},
				{Name: "ss-test", ProxyRx: 60, ProxyTx: 20},
			},
		},
	}
	got := NodeIncrements(batch, nil)
	totals := classTotals(got)
	if totals[ClassTotal] != 120 {
		t.Fatalf("ClassTotal = %d, want 120 (proxy fallback when all ifaces excluded)", totals[ClassTotal])
	}
	perIface := ifaceTotalsMap(got)
	if perIface["lo"] != 1000 || perIface["utun4"] != 300 {
		t.Fatalf("ClassIface records missing: %+v", perIface)
	}
}

func TestNodeIncrementsWithOptionsAllowsBridgeOptInAndCustomPrefixes(t *testing.T) {
	batch := protocol.Batch{
		Interfaces: []protocol.IfaceDelta{
			{Name: "br-lan", RxDelta: 1000, TxDelta: 500},
			{Name: "lo", RxDelta: 100, TxDelta: 100},
		},
	}

	if totals := classTotals(NodeIncrements(batch, nil)); totals[ClassTotal] != 0 {
		t.Fatalf("expected br-lan and lo excluded by default, got %d", totals[ClassTotal])
	}

	got := NodeIncrementsWithOptions(batch, nil, NodeOptions{IncludeIfaces: []string{"br-lan"}})
	if totals := classTotals(got); totals[ClassTotal] != 1500 {
		t.Fatalf("IncludeIfaces br-lan total = %d, want 1500", totals[ClassTotal])
	}

	gotAll := NodeIncrementsWithOptions(batch, nil, NodeOptions{ExcludeIfacePrefixes: []string{}})
	if totals := classTotals(gotAll); totals[ClassTotal] != 1700 {
		t.Fatalf("empty ExcludeIfacePrefixes total = %d, want 1700", totals[ClassTotal])
	}

	gotCustom := NodeIncrementsWithOptions(batch, nil, NodeOptions{ExcludeIfacePrefixes: []string{"lo"}})
	if totals := classTotals(gotCustom); totals[ClassTotal] != 1500 {
		t.Fatalf("custom ExcludeIfacePrefixes total = %d, want 1500", totals[ClassTotal])
	}
}

func ifaceTotalsMap(increments []Increment) map[string]uint64 {
	out := map[string]uint64{}
	for _, item := range increments {
		if item.Class == ClassIface {
			out[item.Iface] += item.Rx + item.Tx
		}
	}
	return out
}

func TestNodeIncrementsZeroDeltaIfaceAndNlbwmonWithProxyTraffic(t *testing.T) {
	// Zero-delta non-excluded iface must fall back to proxy totals so ClassTotal >= ClassProxyRaw.
	zeroIface := protocol.Batch{
		Interfaces: []protocol.IfaceDelta{
			{Name: "eth0", RxDelta: 0, TxDelta: 0},
		},
		Proxy: &protocol.ProxyDelta{
			ByOutbound: []protocol.OutboundDelta{
				{Name: "DIRECT", DirectRx: 100, DirectTx: 50},
				{Name: "hk-01", ProxyRx: 1000, ProxyTx: 500},
			},
		},
	}
	totals := classTotals(NodeIncrements(zeroIface, nil))
	if totals[ClassTotal] != 1650 || totals[ClassProxyRaw] != 1500 {
		t.Fatalf("zero-delta iface fallback mismatch: %+v, want total=1650 proxy_raw=1500", totals)
	}

	// Zero-delta nlbwmon device falls back to non-zero eth0 iface.
	zeroNlbw := protocol.Batch{
		Interfaces: []protocol.IfaceDelta{
			{Name: "eth0", RxDelta: 400, TxDelta: 100},
		},
		Devices: []protocol.DeviceDelta{
			{ObsIP: "203.0.113.10", ObsMAC: "02:00:00:00:00:0a", RxDelta: 0, TxDelta: 0, Source: protocol.SourceNlbwmon},
		},
		Proxy: &protocol.ProxyDelta{
			ByOutbound: []protocol.OutboundDelta{
				{Name: "hk-01", ProxyRx: 200, ProxyTx: 100},
			},
		},
	}
	totalsNlbw := classTotals(NodeIncrements(zeroNlbw, nil))
	if totalsNlbw[ClassTotal] != 500 {
		t.Fatalf("zero-delta nlbwmon fallback mismatch: %+v, want total=500", totalsNlbw)
	}
}
