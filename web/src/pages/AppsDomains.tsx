import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { DimensionStat, MetricDirection } from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { useApp } from "../state/AppContext";
import { formatBytes, formatPercent } from "../utils/format";

export default function AppsDomains() {
  const { lang, t, timeRange, refreshTick, navigate } = useApp();
  const [sort, setSort] = useState<MetricDirection>("total");
  const [limit, setLimit] = useState(50);
  const [processes, setProcesses] = useState<DimensionStat[]>([]);
  const [domains, setDomains] = useState<DimensionStat[]>([]);
  const [domainEnabledCount, setDomainEnabledCount] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [procRes, domRes, nodesRes] = await Promise.all([
        api.dimensions(timeRange, { type: "process", sort, limit }),
        api.dimensions(timeRange, { type: "domain", sort, limit }),
        api.listNodes(timeRange),
      ]);
      setProcesses(procRes.items || []);
      setDomains(domRes.items || []);
      const enabled = (nodesRes.nodes || []).filter((n) => n.collect_domains).length;
      setDomainEnabledCount(enabled);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [limit, sort, timeRange]);

  useEffect(() => {
    void load();
  }, [load, refreshTick]);

  const renderTable = (items: DimensionStat[], emptyDesc: string) => {
    if (items.length === 0) {
      return <EmptyState title={t.noData} description={emptyDesc} />;
    }
    return (
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>{lang === "zh-CN" ? "名称" : "Name"}</th>
              <th>RX</th>
              <th>TX</th>
              <th>{t.total}</th>
              <th>{lang === "zh-CN" ? "占比" : "Share"}</th>
            </tr>
          </thead>
          <tbody>
            {items.map((item, idx) => (
              <tr key={item.key}>
                <td>{idx + 1}</td>
                <td className="sans">{item.key}</td>
                <td>{formatBytes(item.rx)}</td>
                <td>{formatBytes(item.tx)}</td>
                <td>{formatBytes(item.total)}</td>
                <td>
                  <div className="share-cell">
                    <div className="share-bar">
                      <div style={{ width: `${Math.min(100, item.share * 100)}%` }} />
                    </div>
                    <span>{formatPercent(item.share)}</span>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  };

  return (
    <section id="apps" className="page-stack">
      {error && <div className="banner banner-error">{error}</div>}

      {domainEnabledCount === 0 && (
        <div className="banner banner-info">
          <span>{t.domainsDisabledBanner}</span>
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => navigate("nodes")}
          >
            {t.goToNodes}
          </button>
        </div>
      )}

      <div className="toolbar-inline">
        <div className="chip-group" role="group" aria-label="Sort by">
          {(["total", "rx", "tx"] as MetricDirection[]).map((s) => (
            <button
              key={s}
              type="button"
              className={`chip-btn${sort === s ? " active" : ""}`}
              onClick={() => setSort(s)}
            >
              {lang === "zh-CN" ? "排序: " : "Sort: "}
              {s === "total" ? t.metricTotal : s === "rx" ? t.metricRx : t.metricTx}
            </button>
          ))}
        </div>
        <div className="chip-group" role="group" aria-label="Limit">
          {[20, 50, 100].map((n) => (
            <button
              key={n}
              type="button"
              className={`chip-btn${limit === n ? " active" : ""}`}
              onClick={() => setLimit(n)}
            >
              Top {n}
            </button>
          ))}
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.appsTitle}</h3>
            <p className="card-sub">
              {lang === "zh-CN"
                ? "来自 Mihomo metadata.process 的应用进程流量排行"
                : "Process traffic ranking extracted from Mihomo metadata.process"}
            </p>
          </div>
        </div>
        {renderTable(
          processes,
          lang === "zh-CN" ? "暂无应用进程流量记录" : "No process traffic recorded yet",
        )}
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.domainsTitle}</h3>
            <p className="card-sub">
              {lang === "zh-CN"
                ? "仅在节点开启域名采集或命中关注规则时记录 eTLD+1 域名"
                : "Collects eTLD+1 domains only when enabled per node or matched by watchlist"}
            </p>
          </div>
          {domainEnabledCount !== null && domainEnabledCount > 0 && (
            <span className="badge badge-ok">
              {lang === "zh-CN"
                ? `${domainEnabledCount} 个节点已开启`
                : `${domainEnabledCount} node(s) enabled`}
            </span>
          )}
        </div>
        {renderTable(
          domains,
          lang === "zh-CN"
            ? "暂无域名流量记录（请确认已在节点开启 collect_domains）"
            : "No domain traffic recorded yet",
        )}
      </div>
    </section>
  );
}
