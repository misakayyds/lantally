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
	if totals[ClassProxyAdjusted] != 40 {
		t.Fatalf("proxy_adjusted = %d, want 40", totals[ClassProxyAdjusted])
	}
}

func TestNodeIncrementsFallsBackToIfaceThenMihomo(t *testing.T) {
	ifaceOnly := protocol.Batch{
		Interfaces: []protocol.IfaceDelta{{Name: "br-lan", RxDelta: 1024, TxDelta: 512}},
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
	if totals[ClassProxyUnadjusted] != 6 {
		t.Fatalf("unknown multiplier should stay unadjusted, got %d", totals[ClassProxyUnadjusted])
	}
	if totals[ClassProxyAdjusted] != 0 {
		t.Fatalf("unknown multiplier must not silently use 1.0, adjusted=%d", totals[ClassProxyAdjusted])
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
	if outboundTotals(got, ClassProxyAdjusted)["proxy-a"] != 40 {
		t.Fatalf("proxy-a adjusted = %+v", outboundTotals(got, ClassProxyAdjusted))
	}
	if outboundTotals(got, ClassProxyUnadjusted)["proxy-b"] != 40 {
		t.Fatalf("proxy-b unadjusted = %+v", outboundTotals(got, ClassProxyUnadjusted))
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
	if len(got) != 1 || got[0].DeviceID != "dev-a" || got[0].Class != ClassProxyRaw || got[0].Outbound != "proxy-a" || got[0].Rx != 9 || got[0].Tx != 1 {
		t.Fatalf("mihomo proxy increment = %+v", got)
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
