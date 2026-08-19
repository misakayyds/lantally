package protocol

import "time"

type Capability string

const (
	CapIface     Capability = "iface"
	CapConntrack Capability = "conntrack"
	CapNlbwmon   Capability = "nlbwmon"
	CapMihomo    Capability = "mihomo"
)

type GapReason string

const (
	GapReboot         GapReason = "reboot"
	GapBufferDrop     GapReason = "buffer_drop"
	GapCollectorReset GapReason = "collector_reset"
)

type Batch struct {
	ProtocolVersion int           `json:"protocol_version"`
	SiteID          string        `json:"site_id"`
	NodeID          string        `json:"node_id"`
	BootID          string        `json:"boot_id"`
	Sequence        uint64        `json:"sequence"`
	SampledAt       time.Time     `json:"sampled_at"`
	IntervalMS      int           `json:"interval_ms"`
	Capabilities    []Capability  `json:"capabilities"`
	Interfaces      []IfaceDelta  `json:"interfaces"`
	Devices         []DeviceDelta `json:"devices"`
	Proxy           *ProxyDelta   `json:"proxy,omitempty"`
	Gaps            []Gap         `json:"gaps,omitempty"`
}

type IfaceDelta struct {
	Name    string `json:"name"`
	RxDelta uint64 `json:"rx_delta"`
	TxDelta uint64 `json:"tx_delta"`
}

type DeviceSource string

const (
	SourceNlbwmon DeviceSource = "nlbwmon"
	SourceMihomo  DeviceSource = "mihomo"
	SourceNeigh   DeviceSource = "neigh"
)

type DeviceDelta struct {
	ObsIP   string       `json:"obs_ip"`
	ObsMAC  string       `json:"obs_mac,omitempty"`
	RxDelta uint64       `json:"rx_delta"`
	TxDelta uint64       `json:"tx_delta"`
	Source  DeviceSource `json:"source"`
}

type ProxyDelta struct {
	ByOutbound []OutboundDelta `json:"by_outbound"`
}

type OutboundDelta struct {
	Name       string `json:"name"`
	DirectRx   uint64 `json:"direct_rx"`
	DirectTx   uint64 `json:"direct_tx"`
	ProxyRx    uint64 `json:"proxy_rx"`
	ProxyTx    uint64 `json:"proxy_tx"`
	Unadjusted bool   `json:"unadjusted"`
}

type Gap struct {
	Reason GapReason `json:"reason"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
}
