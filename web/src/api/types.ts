export type TimeRangePreset =
  | "today"
  | "24h"
  | "72h"
  | "7d"
  | "30d"
  | "billing"
  | "custom";

export interface TimeRangeState {
  preset: TimeRangePreset;
  from: string; // YYYY-MM-DD or RFC3339
  to: string;   // YYYY-MM-DD or RFC3339
}

export type MetricDirection = "total" | "rx" | "tx";

export type TrafficGroup =
  | "node"
  | "outbound"
  | "group"
  | "process"
  | "domain"
  | "iface"
  | "device"
  | "class";

export interface ResolvedTimeRange {
  preset: string;
  from: string;
  to: string;
}

export interface RxTxTotal {
  rx: number;
  tx: number;
  total: number;
}

export interface TrafficPoint {
  t: string;
  values: Record<string, number>;
  rx_values?: Record<string, number>;
  tx_values?: Record<string, number>;
}

export interface TrafficSeries {
  from?: string;
  to?: string;
  bucket_seconds: number;
  keys: string[];
  totals: Record<string, number>;
  rx_totals?: Record<string, number>;
  tx_totals?: Record<string, number>;
  breakdown?: Record<string, RxTxTotal>;
  points: TrafficPoint[];
}

export type WatchDimension =
  | "node"
  | "iface"
  | "outbound"
  | "group"
  | "process"
  | "domain"
  | "device";

export type WatchMatchType = "exact" | "keyword" | "regex";

export interface WatchMatcher {
  type: WatchMatchType;
  values: string[];
}

export type WatchMatcherMap = Partial<Record<WatchDimension, WatchMatcher>>;

export interface WatchPoint {
  t: string;
  rx: number;
  tx: number;
  total: number;
  adjusted: number;
  adjusted_rx: number;
  adjusted_tx: number;
}

export interface WatchItem {
  id: number;
  name: string;
  matchers: WatchMatcherMap;
  override_factor?: number | null;
  quota_bytes: number;
  quota_unit: "MB" | "GB" | "TB";
  quota_value: number;
  reset_type: "none" | "monthly" | "fixed" | "fixed_date";
  reset_day: number;
  expires_at?: string;
  pinned: boolean;
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface WatchItemStatus extends WatchItem {
  rx: number;
  tx: number;
  total: number;
  adjusted: number;
  adjusted_rx: number;
  adjusted_tx: number;
  used_bytes: number;
  used_ratio: number;
  days_remaining: number | null;
  over_quota: boolean;
  expired: boolean;
  overlap_possible?: boolean;
  cycle_from?: string;
  cycle_to?: string;
  from?: string;
  to?: string;
  bucket_seconds?: number;
  series: WatchPoint[];
  buckets?: WatchPoint[];
}

export interface WatchInput {
  name: string;
  matchers: WatchMatcherMap;
  override_factor?: number | null;
  quota_unit: "MB" | "GB" | "TB";
  quota_value: number;
  reset_type: "none" | "monthly" | "fixed";
  reset_day?: number;
  expires_at?: string;
  pinned: boolean;
  enabled: boolean;
}

export interface OverviewResponse {
  ledgers: string[];
  ingest_batches: number;
  bytes: Record<string, number>;
  breakdown: Record<string, RxTxTotal>;
  rx: number;
  tx: number;
  total: number;
  range: ResolvedTimeRange;
  traffic: TrafficSeries;
  pinned_watch: WatchItemStatus[];
}

export interface DimensionStat {
  type: string;
  key: string;
  rx: number;
  tx: number;
  total: number;
  share: number;
}

export interface DimensionsResponse {
  type: string;
  from: string;
  to: string;
  range: ResolvedTimeRange;
  total_rx: number;
  total_tx: number;
  total: number;
  items: DimensionStat[];
}

export interface OutboundLedger {
  name: string;
  rx: number;
  tx: number;
  total: number;
  raw: number;
  raw_rx: number;
  raw_tx: number;
  factor: number;
  adjusted: number;
  adjusted_rx: number;
  adjusted_tx: number;
  unadjusted: number;
  configured: boolean;
  match_source: "rule" | "default";
  matched_rule?: string;
  matched_rule_id?: number;
  matched_rule_name?: string;
  matched_type?: string;
  share: number;
}

export interface BillingStatus {
  reset_day: number;
  provider_bytes: number;
  local_bytes: number;
  delta: number;
  ratio: number;
  from: string;
  to: string;
}

export interface ProxyResponse {
  proxy: Record<string, number>;
  proxy_breakdown?: Record<string, RxTxTotal>;
  rx: number;
  tx: number;
  total: number;
  range: ResolvedTimeRange;
  outbounds: OutboundLedger[];
  traffic: TrafficSeries;
  billing: BillingStatus;
}

export type RuleMatchType = "keyword" | "regex" | "exact" | "auto_extract";

export interface MultiplierRule {
  id: number;
  name: string;
  match_type: RuleMatchType;
  pattern: string;
  factor: number;
  priority: number;
  enabled: boolean;
  updated_at?: string;
}

export interface RuleInput {
  name: string;
  match_type: RuleMatchType;
  pattern: string;
  factor: number;
  priority: number;
  enabled?: boolean;
}

export interface PreviewMatch {
  name: string;
  factor: number;
  match_source: "rule" | "default";
  matched_rule?: string;
  matched_rule_id?: number;
  matched_rule_name?: string;
  matched_type?: string;
}

export interface ProviderStatus {
  id: number;
  name: string;
  source: string;
  source_type?: string;
  node_id?: string;
  provider_name?: string;
  vehicle_type?: string;
  upload_bytes: number;
  download_bytes: number;
  used_bytes: number;
  total_bytes: number;
  unit?: string;
  quota_unit?: string;
  expire?: number;
  expire_at?: number;
  reset_day: number;
  outbound_pattern?: string;
  linked_nodes?: string[];
  proxies?: string[];
  nodes_locked?: boolean;
  enabled: boolean;
  updated_at?: string;
  local_adjusted: number;
  proxy_adjusted: number;
  drift_bytes: number;
  drift_ratio: number;
  drift_alert: boolean;
  over_drift: boolean;
  cycle_from?: string;
  cycle_to?: string;
  shared_group?: string;
}

export interface ProviderSummary {
  total_bytes: number;
  used_bytes: number;
  local_adjusted: number;
  proxy_adjusted: number;
  drift_bytes: number;
  drift_ratio: number;
  drift_alert: boolean;
  active_count: number;
}

export interface ProvidersResponse {
  providers: ProviderStatus[];
  summary: ProviderSummary;
}

export interface ProviderInput {
  name: string;
  unit?: "MB" | "GB" | "TB";
  total_value?: number;
  used_value?: number;
  expire_at?: number;
  reset_day?: number;
  outbound_pattern?: string;
  linked_nodes?: string[];
  enabled?: boolean;
}

export interface DeviceLedger {
  id: string;
  site_id: string;
  name: string;
  ip?: string;
  mac?: string;
  rx: number;
  tx: number;
  total: number;
  bytes: Record<string, number>;
  breakdown?: Record<string, RxTxTotal>;
}

export interface DevicesResponse {
  devices: DeviceLedger[];
  range: ResolvedTimeRange;
}

export interface DeviceDetailResponse {
  device: DeviceLedger;
  merged_from: string[];
  range: ResolvedTimeRange;
}

export interface NodeSummary {
  id: string;
  site_id: string;
  mode: "agent" | "direct" | string;
  last_seen_at: string;
  last_boot_id: string;
  collect_domains: boolean;
  bytes: Record<string, number>;
  breakdown?: Record<string, RxTxTotal>;
  rx: number;
  tx: number;
  total: number;
}

export interface NodesResponse {
  nodes: NodeSummary[];
  range: ResolvedTimeRange;
  traffic: TrafficSeries;
}

export interface NodeDetailResponse extends NodeSummary {
  range: ResolvedTimeRange;
  online: boolean;
}

export interface ClaimResponse {
  code: string;
  expires_at: string;
  local: boolean;
  linux: string;
  macos: string;
  windows: string;
  mihomo_hint: string;
}

export interface DirectCollector {
  id: number;
  name: string;
  site_id: string;
  node_id: string;
  url: string;
  has_secret: boolean;
  secret?: string;
  enabled: boolean;
  collect_domains: boolean;
  poll_interval_ms: number;
  last_seen_at: string;
  last_status?: "ok" | "error" | string;
  last_error: string;
  created_at: string;
  updated_at: string;
}

export interface DirectCollectorInput {
  name?: string;
  site_id?: string;
  node_id?: string;
  url?: string;
  secret?: string;
  clear_secret?: boolean;
  enabled?: boolean;
  collect_domains?: boolean;
  poll_interval_ms?: number;
}

export interface ProbeResult {
  ok: boolean;
  version?: string;
  meta?: boolean;
  proxy_count: number;
  group_count: number;
  ws_supported: boolean;
  latency_ms: number;
  error?: string;
}

export interface AlertItem {
  id: number;
  kind: "growth" | "silence" | "reset" | "drift" | "quota" | string;
  site_id: string;
  node_id: string;
  device_id?: string;
  message: string;
  observed_at: string;
  acknowledged_at?: string;
}

export interface SettingsResponse {
  app_version: string;
  protocol_version: number;
  retention: {
    samples_days: number;
    daily: string;
  };
  public_https: string;
}
