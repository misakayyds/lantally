package accounting

import (
	"strings"

	"github.com/misakayyds/lantally/internal/protocol"
)

const (
	ClassTotal           = "total"
	ClassDirect          = "direct"
	ClassProxyRaw        = "proxy_raw"
	ClassProxyAdjusted   = "proxy_adjusted"
	ClassProxyUnadjusted = "proxy_unadjusted"
	ClassIface           = "iface"
)

var defaultExcludedIfacePrefixes = []string{
	"lo", "tun", "utun", "wg", "tailscale", "docker", "veth", "br-",
}

// NodeOptions configures interface exclusion/inclusion when deriving node totals.
type NodeOptions struct {
	// ExcludeIfacePrefixes overrides the default virtual/loopback exclusion prefixes.
	// A nil slice uses DefaultExcludedIfacePrefixes(); a non-nil empty slice excludes none.
	ExcludeIfacePrefixes []string
	// IncludeIfaces explicitly opts specific interface names (e.g. "br-lan") into ClassTotal.
	IncludeIfaces []string
}

// DefaultExcludedIfacePrefixes returns a copy of the default virtual/loopback
// interface name prefixes excluded from node-level ClassTotal.
func DefaultExcludedIfacePrefixes() []string {
	out := make([]string, len(defaultExcludedIfacePrefixes))
	copy(out, defaultExcludedIfacePrefixes)
	return out
}

// IsExcludedIface reports whether an interface name is excluded from node-level ClassTotal.
func IsExcludedIface(name string, opts NodeOptions) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return true
	}
	for _, inc := range opts.IncludeIfaces {
		if strings.EqualFold(strings.TrimSpace(inc), trimmed) {
			return false
		}
	}
	prefixes := opts.ExcludeIfacePrefixes
	if prefixes == nil {
		prefixes = defaultExcludedIfacePrefixes
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range prefixes {
		p := strings.ToLower(strings.TrimSpace(prefix))
		if p != "" && strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// Increment is one ledger delta. Empty DeviceID means node-level totals.
type Increment struct {
	DeviceID string
	Class    string
	Outbound string
	Iface    string
	Rx       uint64
	Tx       uint64
}

// NodeIncrements derives node-level ledger deltas using default virtual-NIC exclusions.
// total prefers nlbwmon, else iface (excluding virtual/loopback), else Mihomo.
func NodeIncrements(batch protocol.Batch, _ map[string]float64) []Increment {
	return NodeIncrementsWithOptions(batch, nil, NodeOptions{})
}

// NodeIncrementsWithOptions derives node-level ledger deltas with custom interface options.
func NodeIncrementsWithOptions(
	batch protocol.Batch,
	_ map[string]float64,
	opts NodeOptions,
) []Increment {
	var out []Increment

	if rx, tx, ok := nlbwmonTotals(batch.Devices); ok {
		out = append(out, Increment{Class: ClassTotal, Rx: rx, Tx: tx})
	} else if rx, tx, ok := ifaceTotalsWithOptions(batch.Interfaces, opts); ok {
		out = append(out, Increment{Class: ClassTotal, Rx: rx, Tx: tx})
	} else if batch.Proxy != nil {
		directRx, directTx, proxyRx, proxyTx := proxyTotals(batch.Proxy)
		out = append(out, Increment{Class: ClassTotal, Rx: directRx + proxyRx, Tx: directTx + proxyTx})
	}

	if batch.Proxy != nil {
		directRx, directTx, _, _ := proxyTotals(batch.Proxy)
		if directRx+directTx > 0 {
			out = append(out, Increment{Class: ClassDirect, Rx: directRx, Tx: directTx})
		}
		out = append(out, proxyIncrements(batch.Proxy)...)
	}
	out = append(out, IfaceIncrements(batch.Interfaces)...)
	return compactIncrements(out)
}

// IfaceIncrements emits one ClassIface increment per observed interface (including virtual NICs).
func IfaceIncrements(ifaces []protocol.IfaceDelta) []Increment {
	if len(ifaces) == 0 {
		return nil
	}
	out := make([]Increment, 0, len(ifaces))
	for _, iface := range ifaces {
		name := strings.TrimSpace(iface.Name)
		if name == "" {
			continue
		}
		out = append(out, Increment{
			Class: ClassIface,
			Iface: name,
			Rx:    iface.RxDelta,
			Tx:    iface.TxDelta,
		})
	}
	return out
}

// DeviceIncrements attributes per-device bytes. Neigh observations are identity-only.
func DeviceIncrements(obs protocol.DeviceDelta, deviceID string, _ map[string]float64) []Increment {
	if deviceID == "" || obs.RxDelta+obs.TxDelta == 0 {
		return nil
	}
	switch obs.Source {
	case protocol.SourceNlbwmon:
		return []Increment{{DeviceID: deviceID, Class: ClassTotal, Rx: obs.RxDelta, Tx: obs.TxDelta}}
	case protocol.SourceMihomo:
		if obs.Outbound == "DIRECT" {
			return []Increment{{DeviceID: deviceID, Class: ClassDirect, Rx: obs.RxDelta, Tx: obs.TxDelta}}
		}
		return []Increment{{
			DeviceID: deviceID,
			Class:    ClassProxyRaw,
			Outbound: obs.Outbound,
			Rx:       obs.RxDelta,
			Tx:       obs.TxDelta,
		}}
	default:
		return nil
	}
}

func proxyIncrements(proxy *protocol.ProxyDelta) []Increment {
	if proxy == nil {
		return nil
	}
	var out []Increment
	for _, outbound := range proxy.ByOutbound {
		if outbound.Name == "DIRECT" {
			continue
		}
		if outbound.ProxyRx+outbound.ProxyTx > 0 {
			out = append(out, Increment{
				Class:    ClassProxyRaw,
				Outbound: outbound.Name,
				Rx:       outbound.ProxyRx,
				Tx:       outbound.ProxyTx,
			})
		}
	}
	return out
}

func nlbwmonTotals(devices []protocol.DeviceDelta) (rx, tx uint64, ok bool) {
	for _, obs := range devices {
		if obs.Source != protocol.SourceNlbwmon {
			continue
		}
		rx += obs.RxDelta
		tx += obs.TxDelta
	}
	return rx, tx, rx+tx > 0
}

func ifaceTotalsWithOptions(ifaces []protocol.IfaceDelta, opts NodeOptions) (rx, tx uint64, ok bool) {
	for _, iface := range ifaces {
		if IsExcludedIface(iface.Name, opts) {
			continue
		}
		rx += iface.RxDelta
		tx += iface.TxDelta
	}
	return rx, tx, rx+tx > 0
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
