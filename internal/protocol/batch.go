package protocol

import (
	"encoding/json"
	"time"
)

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
	ProtocolVersion int                `json:"protocol_version"`
	SiteID          string             `json:"site_id"`
	NodeID          string             `json:"node_id"`
	BootID          string             `json:"boot_id"`
	Sequence        uint64             `json:"sequence"`
	SampledAt       time.Time          `json:"sampled_at"`
	IntervalMS      int                `json:"interval_ms"`
	Capabilities    []Capability       `json:"capabilities"`
	Interfaces      []IfaceDelta       `json:"interfaces"`
	Devices         []DeviceDelta      `json:"devices"`
	Proxy           *ProxyDelta        `json:"proxy,omitempty"`
	Dimensions      []DimensionDelta   `json:"dimensions,omitempty"`
	ProxyNodes      []string           `json:"proxy_nodes,omitempty"`
	Providers       []ProviderSnapshot `json:"providers,omitempty"`
	Gaps            []Gap              `json:"gaps,omitempty"`
	Metadata        json.RawMessage    `json:"metadata,omitempty"`
}

type DimensionType string

const (
	DimGroup   DimensionType = "group"
	DimProcess DimensionType = "process"
	DimDomain  DimensionType = "domain"
	DimIface   DimensionType = "iface"
)

type DimensionDelta struct {
	Type    DimensionType `json:"type"`
	Key     string        `json:"key"`
	RxDelta uint64        `json:"rx_delta"`
	TxDelta uint64        `json:"tx_delta"`
}

type ProviderSnapshot struct {
	Name        string   `json:"name"`
	VehicleType string   `json:"vehicle_type,omitempty"`
	Upload      uint64   `json:"upload"`
	Download    uint64   `json:"download"`
	Total       uint64   `json:"total"`
	Expire      int64    `json:"expire"`
	Proxies     []string `json:"proxies,omitempty"`
}

// IngestResponse is returned by POST /v1/ingest.
// CollectDomains and WatchDomains are omitted when default (false / empty)
// to preserve exact v0.1 JSON responses.
type IngestResponse struct {
	Status         string   `json:"status"`
	Duplicate      bool     `json:"duplicate"`
	CollectDomains *bool    `json:"collect_domains,omitempty"`
	WatchDomains   []string `json:"watch_domains,omitempty"`
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
	ObsIP    string       `json:"obs_ip"`
	ObsMAC   string       `json:"obs_mac,omitempty"`
	RxDelta  uint64       `json:"rx_delta"`
	TxDelta  uint64       `json:"tx_delta"`
	Source   DeviceSource `json:"source"`
	Outbound string       `json:"outbound,omitempty"`
}

type ProxyDelta struct {
	ByOutbound []OutboundDelta `json:"by_outbound"`
}

type OutboundDelta struct {
	Name       string `json:"name"`
	Group      string `json:"group,omitempty"`
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
