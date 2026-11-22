import type {
  AlertItem,
  ClaimResponse,
  DeviceDetailResponse,
  DevicesResponse,
  DimensionsResponse,
  DirectCollector,
  DirectCollectorInput,
  MultiplierRule,
  NodeDetailResponse,
  NodesResponse,
  OverviewResponse,
  PreviewMatch,
  ProbeResult,
  ProviderInput,
  ProvidersResponse,
  ProviderStatus,
  ProxyResponse,
  RuleInput,
  SettingsResponse,
  TimeRangeState,
  TrafficGroup,
  TrafficSeries,
  WatchInput,
  WatchItem,
  WatchItemStatus,
} from "./types";

let unauthorizedHandler: (() => void) | null = null;

export function setUnauthorizedHandler(fn: (() => void) | null): void {
  unauthorizedHandler = fn;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export function buildTimeParams(
  range: TimeRangeState,
  extra?: Record<string, string | number | undefined | null>,
): URLSearchParams {
  const params = new URLSearchParams();
  if (range.preset === "custom") {
    params.set("range", "custom");
    if (range.from) params.set("from", range.from);
    if (range.to) params.set("to", range.to);
  } else {
    params.set("range", range.preset);
  }
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (tz) {
      params.set("tz", tz);
    } else {
      params.set("tz_offset", String(-new Date().getTimezoneOffset()));
    }
  } catch {
    params.set("tz_offset", String(-new Date().getTimezoneOffset()));
  }
  if (extra) {
    for (const [k, v] of Object.entries(extra)) {
      if (v !== undefined && v !== null && String(v) !== "") {
        params.set(k, String(v));
      }
    }
  }
  return params;
}

interface FetchOpts extends RequestInit {
  skipAuthRedirect?: boolean;
}

async function request<T>(path: string, opts: FetchOpts = {}): Promise<T> {
  const { skipAuthRedirect, headers, ...rest } = opts;
  const res = await fetch(path, {
    credentials: "same-origin",
    ...rest,
    headers: {
      ...(rest.body ? { "Content-Type": "application/json" } : {}),
      ...(headers || {}),
    },
  });
  if (res.status === 401 && !skipAuthRedirect && unauthorizedHandler) {
    unauthorizedHandler();
  }
  if (!res.ok) {
    const text = (await res.text().catch(() => "")).trim();
    throw new ApiError(res.status, text || res.statusText || `HTTP ${res.status}`);
  }
  const ct = res.headers.get("Content-Type") || "";
  if (ct.includes("application/octet-stream")) {
    return (await res.blob()) as unknown as T;
  }
  const raw = await res.text();
  if (!raw) return {} as T;
  return JSON.parse(raw) as T;
}

export const api = {
  setupStatus(): Promise<{ needed: boolean }> {
    return request("/v1/setup", { skipAuthRedirect: true });
  },
  setup(password: string): Promise<{ status: string }> {
    return request("/v1/setup", {
      method: "POST",
      body: JSON.stringify({ password }),
      skipAuthRedirect: true,
    });
  },
  login(password: string): Promise<{ status: string }> {
    return request("/v1/login", {
      method: "POST",
      body: JSON.stringify({ password }),
      skipAuthRedirect: true,
    });
  },
  logout(): Promise<{ status: string }> {
    return request("/v1/logout", { method: "POST", skipAuthRedirect: true });
  },
  settings(skipAuthRedirect = false): Promise<SettingsResponse> {
    return request("/v1/settings", { skipAuthRedirect });
  },
  changePassword(current: string, password: string): Promise<{ status: string }> {
    return request("/v1/settings/password", {
      method: "PUT",
      body: JSON.stringify({ current, password }),
      skipAuthRedirect: true,
    });
  },
  async downloadBackup(): Promise<Blob> {
    const res = await fetch("/v1/backup", { credentials: "same-origin" });
    if (res.status === 401 && unauthorizedHandler) {
      unauthorizedHandler();
    }
    if (!res.ok) {
      const text = (await res.text().catch(() => "")).trim();
      throw new ApiError(res.status, text || `HTTP ${res.status}`);
    }
    return res.blob();
  },

  overview(range: TimeRangeState, group: TrafficGroup): Promise<OverviewResponse> {
    const q = buildTimeParams(range, { group });
    return request(`/v1/overview?${q.toString()}`);
  },
  traffic(
    range: TimeRangeState,
    opts: { group: TrafficGroup; class?: string; node?: string; device?: string },
  ): Promise<TrafficSeries> {
    const q = buildTimeParams(range, {
      group: opts.group,
      class: opts.class,
      node: opts.node,
      device: opts.device,
    });
    return request(`/v1/traffic?${q.toString()}`);
  },
  dimensions(
    range: TimeRangeState,
    opts: { type: "group" | "process" | "domain" | "iface" | ""; limit?: number; sort?: "total" | "rx" | "tx"; node_id?: string },
  ): Promise<DimensionsResponse> {
    const q = buildTimeParams(range, {
      type: opts.type,
      limit: opts.limit,
      sort: opts.sort,
      node_id: opts.node_id,
    });
    return request(`/v1/dimensions?${q.toString()}`);
  },

  proxy(range: TimeRangeState): Promise<ProxyResponse> {
    const q = buildTimeParams(range, { group: "outbound", class: "proxy_raw" });
    return request(`/v1/proxy?${q.toString()}`);
  },
  setQuickMultiplier(name: string, factor: number): Promise<{ status: string }> {
    return request("/v1/proxy/multipliers", {
      method: "PUT",
      body: JSON.stringify({ name, factor }),
    });
  },
  setBilling(reset_day: number, provider_bytes: number): Promise<unknown> {
    return request("/v1/proxy/billing", {
      method: "PUT",
      body: JSON.stringify({ reset_day, provider_bytes }),
    });
  },
  listRules(): Promise<{ rules: MultiplierRule[] }> {
    return request("/v1/proxy/rules");
  },
  createRule(input: RuleInput): Promise<{ rule: MultiplierRule }> {
    return request("/v1/proxy/rules", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  updateRule(id: number, input: RuleInput): Promise<{ rule: MultiplierRule }> {
    return request(`/v1/proxy/rules/${id}`, {
      method: "PUT",
      body: JSON.stringify(input),
    });
  },
  deleteRule(id: number): Promise<{ status: string }> {
    return request(`/v1/proxy/rules/${id}`, { method: "DELETE" });
  },
  async exportRules(): Promise<Blob> {
    const res = await fetch("/v1/proxy/rules/export", { credentials: "same-origin" });
    if (res.status === 401 && unauthorizedHandler) unauthorizedHandler();
    if (!res.ok) {
      const text = (await res.text().catch(() => "")).trim();
      throw new ApiError(res.status, text || `HTTP ${res.status}`);
    }
    return res.blob();
  },
  importRules(
    mode: "replace" | "merge",
    rules: RuleInput[],
  ): Promise<{ status: string; imported: number; rules: MultiplierRule[] }> {
    return request("/v1/proxy/rules/import", {
      method: "POST",
      body: JSON.stringify({ mode, rules }),
    });
  },
  previewRules(
    sample_names: string[],
    rules?: RuleInput[],
  ): Promise<{ matches: PreviewMatch[] }> {
    return request("/v1/proxy/rules/preview", {
      method: "POST",
      body: JSON.stringify(rules ? { sample_names, rules } : { sample_names }),
    });
  },

  listProviders(): Promise<ProvidersResponse> {
    const q = new URLSearchParams();
    try {
      const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
      if (tz) q.set("tz", tz);
    } catch {
      q.set("tz_offset", String(-new Date().getTimezoneOffset()));
    }
    return request(`/v1/proxy/providers?${q.toString()}`);
  },
  createProvider(input: ProviderInput): Promise<ProviderStatus> {
    return request("/v1/proxy/providers", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  updateProvider(id: number, input: ProviderInput): Promise<ProviderStatus> {
    return request(`/v1/proxy/providers/${id}`, {
      method: "PUT",
      body: JSON.stringify(input),
    });
  },
  deleteProvider(id: number): Promise<{ status: string }> {
    return request(`/v1/proxy/providers/${id}`, { method: "DELETE" });
  },

  listWatch(range: TimeRangeState): Promise<{ items: WatchItemStatus[] }> {
    const q = buildTimeParams(range);
    return request(`/v1/watch?${q.toString()}`);
  },
  getWatch(id: number, range: TimeRangeState): Promise<WatchItemStatus> {
    const q = buildTimeParams(range);
    return request(`/v1/watch/${id}?${q.toString()}`);
  },
  createWatch(input: WatchInput): Promise<WatchItem> {
    return request("/v1/watch", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  updateWatch(id: number, input: WatchInput): Promise<WatchItem> {
    return request(`/v1/watch/${id}`, {
      method: "PUT",
      body: JSON.stringify(input),
    });
  },
  deleteWatch(id: number): Promise<{ status: string }> {
    return request(`/v1/watch/${id}`, { method: "DELETE" });
  },

  listDevices(range: TimeRangeState): Promise<DevicesResponse> {
    const q = buildTimeParams(range);
    return request(`/v1/devices?${q.toString()}`);
  },
  getDevice(id: string, range: TimeRangeState): Promise<DeviceDetailResponse> {
    const q = buildTimeParams(range);
    return request(`/v1/devices/${encodeURIComponent(id)}?${q.toString()}`);
  },
  renameDevice(id: string, name: string): Promise<DeviceDetailResponse> {
    return request(`/v1/devices/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: JSON.stringify({ name }),
    });
  },
  mergeDevice(winnerId: string, other_id: string, reason = "manual"): Promise<{ status: string }> {
    return request(`/v1/devices/${encodeURIComponent(winnerId)}/merge`, {
      method: "POST",
      body: JSON.stringify({ other_id, reason }),
    });
  },
  unmergeDevice(id: string): Promise<{ status: string }> {
    return request(`/v1/devices/${encodeURIComponent(id)}/unmerge`, {
      method: "POST",
    });
  },

  listNodes(range: TimeRangeState): Promise<NodesResponse> {
    const q = buildTimeParams(range, { group: "node" });
    return request(`/v1/nodes?${q.toString()}`);
  },
  getNode(id: string, range: TimeRangeState): Promise<NodeDetailResponse> {
    const q = buildTimeParams(range);
    return request(`/v1/nodes/${encodeURIComponent(id)}?${q.toString()}`);
  },
  setNodeConfig(id: string, collect_domains: boolean): Promise<{ status: string; id: string; collect_domains: boolean }> {
    return request(`/v1/nodes/${encodeURIComponent(id)}/config`, {
      method: "PUT",
      body: JSON.stringify({ collect_domains }),
    });
  },
  revokeNode(id: string): Promise<{ status: string }> {
    return request(`/v1/nodes/${encodeURIComponent(id)}/revoke`, {
      method: "POST",
    });
  },
  createClaim(site_id: string, node_id: string, local: boolean): Promise<ClaimResponse> {
    return request("/v1/claims", {
      method: "POST",
      body: JSON.stringify({ site_id, node_id, local }),
    });
  },

  listDirectCollectors(): Promise<{ collectors: DirectCollector[] }> {
    return request("/v1/direct-collectors");
  },
  createDirectCollector(input: DirectCollectorInput): Promise<DirectCollector> {
    return request("/v1/direct-collectors", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  updateDirectCollector(id: number, input: DirectCollectorInput): Promise<DirectCollector> {
    return request(`/v1/direct-collectors/${id}`, {
      method: "PUT",
      body: JSON.stringify(input),
    });
  },
  deleteDirectCollector(id: number): Promise<{ status: string }> {
    return request(`/v1/direct-collectors/${id}`, { method: "DELETE" });
  },
  testDirectCollectorConfig(url: string, secret?: string): Promise<ProbeResult> {
    return request("/v1/direct-collectors/test", {
      method: "POST",
      body: JSON.stringify({ url, ...(secret ? { secret } : {}) }),
    });
  },
  testDirectCollectorByID(
    id: number,
    url?: string,
    secret?: string,
    clearSecret?: boolean,
  ): Promise<ProbeResult> {
    return request(`/v1/direct-collectors/${id}/test`, {
      method: "POST",
      body: JSON.stringify({
        ...(url ? { url } : {}),
        ...(clearSecret ? { clear_secret: true } : secret ? { secret } : {}),
      }),
    });
  },
  toggleDirectCollector(id: number, enabled?: boolean): Promise<DirectCollector> {
    return request(`/v1/direct-collectors/${id}/toggle`, {
      method: "POST",
      body: JSON.stringify(enabled === undefined ? {} : { enabled }),
    });
  },

  listAlerts(): Promise<{ alerts: AlertItem[] }> {
    return request("/v1/alerts");
  },
  ackAlert(id: number): Promise<{ status: string }> {
    return request(`/v1/alerts/${id}/ack`, { method: "POST" });
  },
};
