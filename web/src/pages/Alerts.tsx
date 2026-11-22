import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { AlertItem } from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { useApp } from "../state/AppContext";
import { formatStamp } from "../utils/format";

export default function Alerts() {
  const { lang, t, refreshTick, showToast } = useApp();
  const [alerts, setAlerts] = useState<AlertItem[]>([]);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await api.listAlerts();
      setAlerts(res.alerts || []);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load, refreshTick]);

  const kindLabel = (kind: string) => {
    switch (kind) {
      case "growth":
        return t.kindGrowth;
      case "silence":
        return t.kindSilence;
      case "reset":
        return t.kindReset;
      case "drift":
        return t.kindDrift;
      case "quota":
        return t.kindQuota;
      default:
        return kind;
    }
  };

  const handleAck = async (id: number) => {
    try {
      await api.ackAlert(id);
      showToast(lang === "zh-CN" ? "已确认告警" : "Alert acknowledged");
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <section id="alerts" className="page-stack">
      {error && <div className="banner banner-error">{error}</div>}

      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.alertsTitle}</h3>
            <p className="card-sub">{t.alertsSub}</p>
          </div>
        </div>
        {alerts.length === 0 ? (
          <EmptyState
            title={t.noAlerts}
            description={
              lang === "zh-CN"
                ? "所有节点上报、对账与配额均处于正常范围内"
                : "All nodes, reconciliations, and quotas are healthy"
            }
          />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === "zh-CN" ? "类型" : "Kind"}</th>
                  <th>{lang === "zh-CN" ? "目标" : "Target"}</th>
                  <th>{lang === "zh-CN" ? "详情" : "Message"}</th>
                  <th>{lang === "zh-CN" ? "时间" : "Observed"}</th>
                  <th>{lang === "zh-CN" ? "操作" : "Action"}</th>
                </tr>
              </thead>
              <tbody>
                {alerts.map((a) => (
                  <tr key={a.id}>
                    <td>
                      <span className="badge badge-warn">{kindLabel(a.kind)}</span>
                    </td>
                    <td>
                      {a.site_id} / {a.node_id}
                      {a.device_id ? ` (${a.device_id})` : ""}
                    </td>
                    <td className="sans">{a.message}</td>
                    <td>{formatStamp(new Date(a.observed_at))}</td>
                    <td>
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        onClick={() => handleAck(a.id)}
                      >
                        {t.ack}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </section>
  );
}
