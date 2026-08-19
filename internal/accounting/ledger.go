package accounting

import "github.com/misakayyds/lantally/internal/protocol"

const (
	ClassTotal           = "total"
	ClassDirect          = "direct"
	ClassProxyRaw        = "proxy_raw"
	ClassProxyAdjusted   = "proxy_adjusted"
	ClassProxyUnadjusted = "proxy_unadjusted"
)

// Increment is one ledger delta. Empty DeviceID means node-level totals.
type Increment struct {
	DeviceID string
	Class    string
	Rx       uint64
	Tx       uint64
}

// NodeIncrements derives node-level ledger deltas.
// total prefers nlbwmon, else iface, else Mihomo. nlbwmon is never added to Mihomo.
func NodeIncrements(batch protocol.Batch, multipliers map[string]float64) []Increment {
	var out []Increment

	if rx, tx, ok := nlbwmonTotals(batch.Devices); ok {
		out = append(out, Increment{Class: ClassTotal, Rx: rx, Tx: tx})
	} else if rx, tx, ok := ifaceTotals(batch.Interfaces); ok {
		out = append(out, Increment{Class: ClassTotal, Rx: rx, Tx: tx})
	} else if batch.Proxy != nil {
		directRx, directTx, proxyRx, proxyTx := proxyTotals(batch.Proxy)
		out = append(out, Increment{Class: ClassTotal, Rx: directRx + proxyRx, Tx: directTx + proxyTx})
	}

	if batch.Proxy != nil {
		directRx, directTx, proxyRx, proxyTx := proxyTotals(batch.Proxy)
		if directRx+directTx > 0 {
			out = append(out, Increment{Class: ClassDirect, Rx: directRx, Tx: directTx})
		}
		if proxyRx+proxyTx > 0 {
			out = append(out, Increment{Class: ClassProxyRaw, Rx: proxyRx, Tx: proxyTx})
		}
		adjustedRx, adjustedTx, unadjRx, unadjTx := proxyAdjusted(batch.Proxy, multipliers)
		if adjustedRx+adjustedTx > 0 {
			out = append(out, Increment{Class: ClassProxyAdjusted, Rx: adjustedRx, Tx: adjustedTx})
		}
		if unadjRx+unadjTx > 0 {
			out = append(out, Increment{Class: ClassProxyUnadjusted, Rx: unadjRx, Tx: unadjTx})
		}
	}
	return compactIncrements(out)
}

// DeviceIncrements attributes per-device bytes. Neigh observations are identity-only.
func DeviceIncrements(obs protocol.DeviceDelta, deviceID string, multipliers map[string]float64) []Increment {
	_ = multipliers
	if deviceID == "" || obs.RxDelta+obs.TxDelta == 0 {
		return nil
	}
	switch obs.Source {
	case protocol.SourceNlbwmon:
		return []Increment{{DeviceID: deviceID, Class: ClassTotal, Rx: obs.RxDelta, Tx: obs.TxDelta}}
	case protocol.SourceMihomo:
		return []Increment{{DeviceID: deviceID, Class: ClassProxyRaw, Rx: obs.RxDelta, Tx: obs.TxDelta}}
	default:
		return nil
	}
}

func nlbwmonTotals(devices []protocol.DeviceDelta) (rx, tx uint64, ok bool) {
	for _, obs := range devices {
		if obs.Source != protocol.SourceNlbwmon {
			continue
		}
		rx += obs.RxDelta
		tx += obs.TxDelta
		ok = true
	}
	return rx, tx, ok
}

func ifaceTotals(ifaces []protocol.IfaceDelta) (rx, tx uint64, ok bool) {
	if len(ifaces) == 0 {
		return 0, 0, false
	}
	for _, iface := range ifaces {
		rx += iface.RxDelta
		tx += iface.TxDelta
	}
	return rx, tx, true
}

func proxyTotals(proxy *protocol.ProxyDelta) (directRx, directTx, proxyRx, proxyTx uint64) {
	if proxy == nil {
		return 0, 0, 0, 0
	}
	for _, outbound := range proxy.ByOutbound {
		directRx += outbound.DirectRx
		directTx += outbound.DirectTx
		proxyRx += outbound.ProxyRx
		proxyTx += outbound.ProxyTx
	}
	return directRx, directTx, proxyRx, proxyTx
}

func proxyAdjusted(proxy *protocol.ProxyDelta, multipliers map[string]float64) (adjRx, adjTx, unadjRx, unadjTx uint64) {
	if proxy == nil {
		return 0, 0, 0, 0
	}
	for _, outbound := range proxy.ByOutbound {
		rx, unadj := ApplyMultiplier(outbound.ProxyRx, outbound.Name, multipliers)
		tx, unadjTxFlag := ApplyMultiplier(outbound.ProxyTx, outbound.Name, multipliers)
		if unadj || unadjTxFlag || outbound.Unadjusted {
			unadjRx += outbound.ProxyRx
			unadjTx += outbound.ProxyTx
			continue
		}
		adjRx += rx
		adjTx += tx
	}
	return adjRx, adjTx, unadjRx, unadjTx
}

func compactIncrements(items []Increment) []Increment {
	out := items[:0]
	for _, item := range items {
		if item.Rx == 0 && item.Tx == 0 {
			continue
		}
		out = append(out, item)
	}
	return out
}
