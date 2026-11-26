import React, { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type {
  DeviceDetailResponse,
  DeviceLedger,
  DimensionStat,
  MetricDirection,
  TrafficSeries,
} from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { StackedTrafficChart } from "../components/StackedTrafficChart";
import { useApp } from "../state/AppContext";
import { formatBytes, formatPercent } from "../utils/format";

export default function Devices() {
  const { lang, t, routeState, navigate, timeRange, refreshTick, showToast } = useApp();
  const deviceId = routeState.route === "devices" ? routeState.id : "";

  const [devices, setDevices] = useState<DeviceLedger[]>([]);
  const [deviceSeries, setDeviceSeries] = useState<TrafficSeries | null>(null);
  const [ifaces, setIfaces] = useState<DimensionStat[]>([]);
  const [ifaceSeries, setIfaceSeries] = useState<TrafficSeries | null>(null);
  const [direction, setDirection] = useState<MetricDirection>("total");
  const [tab, setTab] = useState<"devices" | "ifaces">("devices");
  const [error, setError] = useState<string | null>(null);

  // Detail state
  const [detail, setDetail] = useState<DeviceDetailResponse | null>(null);
  const [detailSeries, setDetailSeries] = useState<TrafficSeries | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [mergeTarget, setMergeTarget] = useState("");

  const loadList = useCallback(async () => {
    try {
      const [devRes, devTr, ifDim, ifTr] = await Promise.all([
        api.listDevices(timeRange),
        api.traffic(timeRange, { group: "device" }),
        api.dimensions(timeRange, { type: "iface", limit: 100, sort: "total" }),
        api.traffic(timeRange, { group: "iface" }),
      ]);
      setDevices(devRes.devices || []);
      setDeviceSeries(devTr);
      setIfaces(ifDim.items || []);
      setIfaceSeries(ifTr);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [timeRange]);

  const loadDetail = useCallback(async () => {
    if (!deviceId) return;
    try {
      const [dRes, dSeries, listRes] = await Promise.all([
        api.getDevice(deviceId, timeRange),
        api.traffic(timeRange, { group: "class", device: deviceId }),
        api.listDevices(timeRange),
      ]);
      setDetail(dRes);
      setDetailSeries(dSeries);
      setRenameValue(dRes.device.name || "");
      setDevices(listRes.devices || []);
      setError(null);
    } catch (err) {
      setDetail(null);
      setDetailSeries(null);
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [deviceId, timeRange]);

  useEffect(() => {
    setDetail(null);
    setDetailSeries(null);
    setError(null);
  }, [deviceId]);

  useEffect(() => {
    if (deviceId) {
      void loadDetail();
    } else {
      void loadList();
    }
  }, [deviceId, loadDetail, loadList, refreshTick]);

  const handleRename = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!deviceId || !renameValue.trim()) return;
    try {
      await api.renameDevice(deviceId, renameValue.trim());
      showToast(t.saved);
      await loadDetail();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const handleMerge = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!deviceId || !mergeTarget) return;
    try {
      await api.mergeDevice(deviceId, mergeTarget);
      setMergeTarget("");
      showToast(lang === "zh-CN" ? "已合并设备" : "Device merged");
      await loadDetail();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const handleUnmerge = async () => {
    if (!deviceId) return;
    try {
      await api.unmergeDevice(deviceId);
      showToast(lang === "zh-CN" ? "已撤销合并" : "Unmerged");
      await loadDetail();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const deviceLabel = (d: DeviceLedger) => d.name || d.ip || d.mac || d.id;
  const deviceLabels: Record<string, string> = {};
  for (const d of devices) {
    deviceLabels[d.id] = deviceLabel(d);
  }

  if (deviceId) {
    const dev = detail?.device;
    const candidates = devices.filter(
      (d) => d.id !== deviceId && (!dev || d.site_id === dev.site_id),
    );
    return (
      <section id="devices" className="page-stack">
        <div>
          <a
            href="#devices"
            className="back-link"
            onClick={(e) => {
              e.preventDefault();
              navigate("devices");
            }}
          >
            ← {t.backToDevices}
          </a>
        </div>
        {error && <div className="banner banner-error">{error}</div>}
        {dev && (
          <>
            <div className="metrics">
              <article className="metric">
                <div className="metric-label">{lang === "zh-CN" ? "设备名称" : "Device"}</div>
                <div className="metric-value" style={{ fontSize: "1.25rem", fontFamily: "var(--font)" }}>
                  {deviceLabel(dev)}
                </div>
                <div className="metric-sub mono">
                  {dev.ip || "-"} {dev.mac ? `· ${dev.mac}` : ""}
                </div>
              </article>
              <article className="metric">
                <div className="metric-label">{t.total}</div>
                <div className="metric-value">{formatBytes(dev.total)}</div>
                <div className="metric-sub">
                  RX {formatBytes(dev.rx)} · TX {formatBytes(dev.tx)}
                </div>
              </article>
              <article className="metric">
                <div className="metric-label">{t.direct}</div>
                <div className="metric-value">{formatBytes(dev.bytes?.direct)}</div>
              </article>
              <article className="metric">
                <div className="metric-label">{t.proxyAdjusted}</div>
                <div className="metric-value brand">{formatBytes(dev.bytes?.proxy_adjusted)}</div>
              </article>
            </div>

            <StackedTrafficChart
              title={lang === "zh-CN" ? `${deviceLabel(dev)} 流量分类趋势` : `${deviceLabel(dev)} Traffic by Class`}
              series={detailSeries}
              direction={direction}
              onDirectionChange={setDirection}
            />

            <div className="card">
              <div className="card-header">
                <div>
                  <h3>{lang === "zh-CN" ? "重命名与身份合并" : "Rename & Merge"}</h3>
                  <p className="card-sub">
                    {lang === "zh-CN"
                      ? "将同一台主机在不同网卡或 DHCP 租约下产生的设备记录合并到当前设备"
                      : "Rename this device or merge duplicate records under the same site"}
                  </p>
                </div>
              </div>
              <form className="device-actions" onSubmit={handleRename}>
                <div className="field">
                  <label>{lang === "zh-CN" ? "设备名称" : "Device Name"}</label>
                  <input value={renameValue} onChange={(e) => setRenameValue(e.target.value)} required />
                </div>
                <button type="submit" className="btn btn-primary btn-sm">
                  {lang === "zh-CN" ? "保存名称" : "Save Name"}
                </button>
              </form>
              <form className="device-actions" onSubmit={handleMerge}>
                <div className="field">
                  <label>{lang === "zh-CN" ? "合并其他设备到当前设备" : "Merge another device into this one"}</label>
                  <select value={mergeTarget} onChange={(e) => setMergeTarget(e.target.value)}>
                    <option value="">
                      {lang === "zh-CN" ? "选择要被合并的设备..." : "Select device to merge..."}
                    </option>
                    {candidates.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name} ({c.ip || c.mac || c.id})
                      </option>
                    ))}
                  </select>
                </div>
                <button type="submit" className="btn btn-secondary btn-sm" disabled={!mergeTarget}>
                  {lang === "zh-CN" ? "执行合并" : "Merge"}
                </button>
                <button type="button" className="btn btn-secondary btn-sm" onClick={handleUnmerge}>
                  {lang === "zh-CN" ? "撤销最近合并" : "Unmerge"}
                </button>
              </form>
              {detail.merged_from && detail.merged_from.length > 0 && (
                <div className="chip-list">
                  <span className="status-muted" style={{ fontSize: "0.82rem" }}>
                    {lang === "zh-CN" ? "已合并来源:" : "Merged from:"}
                  </span>
                  {detail.merged_from.map((m) => (
                    <span key={m} className="chip">
                      {m}
                    </span>
                  ))}
                </div>
              )}
            </div>
          </>
        )}
      </section>
    );
  }

  return (
    <section id="devices" className="page-stack">
      {error && <div className="banner banner-error">{error}</div>}

      <div className="chip-group" style={{ alignSelf: "flex-start" }}>
        <button
          type="button"
          className={`chip-btn${tab === "devices" ? " active" : ""}`}
          onClick={() => setTab("devices")}
        >
          {t.devicesTitle}
        </button>
        <button
          type="button"
          className={`chip-btn${tab === "ifaces" ? " active" : ""}`}
          onClick={() => setTab("ifaces")}
        >
          {t.ifacesTitle}
        </button>
      </div>

      {tab === "devices" ? (
        <>
          <StackedTrafficChart
            title={lang === "zh-CN" ? `设备流量趋势（${timeRange.preset}）` : `Device Traffic (${timeRange.preset})`}
            series={deviceSeries}
            direction={direction}
            onDirectionChange={setDirection}
            labelMap={deviceLabels}
          />
          <div className="card">
            <div className="card-header">
              <div>
                <h3>{t.devicesTitle}</h3>
                <p className="card-sub">{t.devicesSub}</p>
              </div>
            </div>
            {devices.length === 0 ? (
              <EmptyState
                title={t.noData}
                description={lang === "zh-CN" ? "暂无局域网设备流量" : "No LAN devices recorded yet"}
              />
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>{lang === "zh-CN" ? "名称" : "Name"}</th>
                      <th>{lang === "zh-CN" ? "站点" : "Site"}</th>
                      <th>IP / MAC</th>
                      <th>RX</th>
                      <th>TX</th>
                      <th>{t.total}</th>
                      <th>{t.direct}</th>
                      <th>{t.proxyAdjusted}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {devices.map((d) => (
                      <tr
                        key={d.id}
                        className="row-link"
                        onClick={() => navigate("devices", d.id)}
                      >
                        <td className="sans">
                          <strong>{deviceLabel(d)}</strong>
                        </td>
                        <td>{d.site_id}</td>
                        <td>
                          {d.ip || "-"}
                          {d.mac ? ` · ${d.mac}` : ""}
                        </td>
                        <td>{formatBytes(d.rx)}</td>
                        <td>{formatBytes(d.tx)}</td>
                        <td>{formatBytes(d.total)}</td>
                        <td>{formatBytes(d.bytes?.direct)}</td>
                        <td>{formatBytes(d.bytes?.proxy_adjusted)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      ) : (
        <>
          <StackedTrafficChart
            title={lang === "zh-CN" ? `网卡接口趋势（${timeRange.preset}）` : `Interface Traffic (${timeRange.preset})`}
            series={ifaceSeries}
            direction={direction}
            onDirectionChange={setDirection}
          />
          <div className="card">
            <div className="card-header">
              <div>
                <h3>{t.ifacesTitle}</h3>
                <p className="card-sub">{t.ifacesSub}</p>
              </div>
            </div>
            {ifaces.length === 0 ? (
              <EmptyState
                title={t.noData}
                description={lang === "zh-CN" ? "暂无网卡维度数据" : "No interface stats recorded yet"}
              />
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>{lang === "zh-CN" ? "接口名" : "Interface"}</th>
                      <th>RX</th>
                      <th>TX</th>
                      <th>{t.total}</th>
                      <th>{lang === "zh-CN" ? "占比" : "Share"}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {ifaces.map((i) => (
                      <tr key={i.key}>
                        <td className="sans">{i.key}</td>
                        <td>{formatBytes(i.rx)}</td>
                        <td>{formatBytes(i.tx)}</td>
                        <td>{formatBytes(i.total)}</td>
                        <td>
                          <div className="share-cell">
                            <div className="share-bar">
                              <div style={{ width: `${Math.min(100, i.share * 100)}%` }} />
                            </div>
                            <span>{formatPercent(i.share)}</span>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      )}
    </section>
  );
}
