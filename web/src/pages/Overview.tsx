import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { MetricDirection, OverviewResponse, TrafficGroup } from "../api/types";
import { StackedTrafficChart } from "../components/StackedTrafficChart";
import { useApp } from "../state/AppContext";
import { formatBytes, formatPercent } from "../utils/format";

export default function Overview() {
  const { lang, t, timeRange, overviewGroup, setOverviewGroup, navigate, refreshTick } = useApp();
  const [data, setData] = useState<OverviewResponse | null>(null);
  const [direction, setDirection] = useState<MetricDirection>("total");
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await api.overview(timeRange, overviewGroup);
      setData(res);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [overviewGroup, timeRange]);

  useEffect(() => {
    void load();
  }, [load, refreshTick]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      void load();
    }, 30000);
    return () => window.clearInterval(timer);
  }, [load]);

  const bd = data?.breakdown || {};
  const totalBd = bd.total || { rx: data?.rx || 0, tx: data?.tx || 0, total: data?.total || 0 };
  const directBd = bd.direct || { rx: 0, tx: 0, total: data?.bytes?.direct || 0 };
  const rawBd = bd.proxy_raw || { rx: 0, tx: 0, total: data?.bytes?.proxy_raw || 0 };
  const adjBd = bd.proxy_adjusted || { rx: 0, tx: 0, total: data?.bytes?.proxy_adjusted || 0 };

  const groupOptions: { key: TrafficGroup; label: string }[] = [
    { key: "node", label: t.groupNode },
    { key: "outbound", label: t.groupOutbound },
    { key: "group", label: t.groupGroup },
    { key: "process", label: t.groupProcess },
    { key: "domain", label: t.groupDomain },
    { key: "iface", label: t.groupIface },
    { key: "device", label: t.groupDevice },
    { key: "class", label: t.groupClass },
  ];

  const chartTitle =
    lang === "zh-CN"
      ? `流量趋势（${timeRange.preset}）`
      : `Traffic Trend (${timeRange.preset})`;

  return (
    <section id="overview" className="page-stack">
      {error && <div className="banner banner-error">{error}</div>}

      <div className="metrics metrics-5">
        <article className="metric">
          <div className="metric-label">{t.total}</div>
          <div className="metric-value">{formatBytes(totalBd.total)}</div>
          <div className="metric-sub">
            RX {formatBytes(totalBd.rx)} · TX {formatBytes(totalBd.tx)}
          </div>
        </article>
        <article className="metric">
          <div className="metric-label">{t.direct}</div>
          <div className="metric-value">{formatBytes(directBd.total)}</div>
          <div className="metric-sub">
            RX {formatBytes(directBd.rx)} · TX {formatBytes(directBd.tx)}
          </div>
        </article>
        <article className="metric">
          <div className="metric-label">{t.proxyRaw}</div>
          <div className="metric-value">{formatBytes(rawBd.total)}</div>
          <div className="metric-sub">
            RX {formatBytes(rawBd.rx)} · TX {formatBytes(rawBd.tx)}
          </div>
        </article>
        <article className="metric">
          <div className="metric-label">{t.proxyAdjusted}</div>
          <div className="metric-value brand">{formatBytes(adjBd.total)}</div>
          <div className="metric-sub">
            RX {formatBytes(adjBd.rx)} · TX {formatBytes(adjBd.tx)}
          </div>
        </article>
        <article className="metric">
          <div className="metric-label">{t.ingestBatches}</div>
          <div className="metric-value brand">
            {(data?.ingest_batches ?? 0).toLocaleString(lang === "zh-CN" ? "zh-CN" : "en-US")}
          </div>
          <div className="metric-sub">
            {lang === "zh-CN" ? "按 30 秒自动刷新" : "Auto-refresh every 30s"}
          </div>
        </article>
      </div>

      {data && data.pinned_watch && data.pinned_watch.length > 0 && (
        <div className="card">
          <div className="card-header">
            <div>
              <h3>{t.pinnedWatchTitle}</h3>
              <p className="card-sub">
                {lang === "zh-CN" ? "来自自定义关注项的实时配额与用量摘要" : "Pinned custom watchlist quotas and usage"}
              </p>
            </div>
            <button type="button" className="btn btn-secondary btn-sm" onClick={() => navigate("watchlist")}>
              {t.manageWatch}
            </button>
          </div>
          <div className="pinned-grid">
            {data.pinned_watch.map((item) => {
              const pct = Math.min(100, Math.max(0, (item.used_ratio || 0) * 100));
              return (
                <div
                  key={item.id}
                  className="pinned-card"
                  role="button"
                  tabIndex={0}
                  onClick={() => navigate("watchlist")}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") navigate("watchlist");
                  }}
                >
                  <div className="pinned-head">
                    <strong>{item.name}</strong>
                    <div className="badge-row">
                      {item.override_factor ? (
                        <span className="badge badge-info">{item.override_factor}x</span>
                      ) : null}
                      {item.over_quota && (
                        <span className="badge badge-danger">{lang === "zh-CN" ? "超限" : "Over"}</span>
                      )}
                      {item.expired && (
                        <span className="badge badge-warn">{lang === "zh-CN" ? "已过期" : "Expired"}</span>
                      )}
                    </div>
                  </div>
                  <div className="pinned-nums">
                    <span className="mono">{formatBytes(item.adjusted)}</span>
                    <span className="status-muted mono">
                      RX {formatBytes(item.adjusted_rx)} · TX {formatBytes(item.adjusted_tx)}
                    </span>
                  </div>
                  {item.quota_bytes > 0 && (
                    <div className="quota-wrap">
                      <div className="progress-track">
                        <div
                          className={`progress-fill${item.over_quota ? " danger" : pct > 80 ? " warn" : ""}`}
                          style={{ width: `${pct}%` }}
                        />
                      </div>
                      <div className="quota-meta">
                        <span>
                          {formatBytes(item.used_bytes)} / {item.quota_value} {item.quota_unit}
                        </span>
                        <span>{formatPercent(item.used_ratio)}</span>
                      </div>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}

      <StackedTrafficChart
        title={chartTitle}
        series={data?.traffic}
        direction={direction}
        onDirectionChange={setDirection}
        rightControls={
          <div className="chip-group compact" role="group" aria-label="Group by">
            {groupOptions.map((g) => (
              <button
                key={g.key}
                type="button"
                className={`chip-btn${overviewGroup === g.key ? " active" : ""}`}
                onClick={() => setOverviewGroup(g.key)}
              >
                {g.label}
              </button>
            ))}
          </div>
        }
      />
    </section>
  );
}
