import React, { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type {
  ClaimResponse,
  DirectCollector,
  DirectCollectorInput,
  MetricDirection,
  NodeDetailResponse,
  NodeSummary,
  ProbeResult,
  TrafficSeries,
} from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { Modal } from "../components/Modal";
import { StackedTrafficChart } from "../components/StackedTrafficChart";
import { useApp } from "../state/AppContext";
import { formatBytes, formatLastSeen } from "../utils/format";

export default function Nodes() {
  const { lang, t, routeState, navigate, timeRange, refreshTick, showToast } = useApp();
  const nodeId = routeState.route === "nodes" ? routeState.id : "";

  const [nodes, setNodes] = useState<NodeSummary[]>([]);
  const [nodeSeries, setNodeSeries] = useState<TrafficSeries | null>(null);
  const [collectors, setCollectors] = useState<DirectCollector[]>([]);
  const [direction, setDirection] = useState<MetricDirection>("total");
  const [error, setError] = useState<string | null>(null);

  // Node detail
  const [nodeDetail, setNodeDetail] = useState<NodeDetailResponse | null>(null);

  // Claim modal
  const [claimOpen, setClaimOpen] = useState(false);
  const [claimSite, setClaimSite] = useState("home");
  const [claimNodeId, setClaimNodeId] = useState("");
  const [claimLocal, setClaimLocal] = useState(false);
  const [claimRes, setClaimRes] = useState<ClaimResponse | null>(null);
  const [claimPlatform, setClaimPlatform] = useState<"linux" | "macos" | "windows">("linux");
  const [claimArrived, setClaimArrived] = useState(false);
  const [activeClaimNodeId, setActiveClaimNodeId] = useState("");
  const [initialLastSeen, setInitialLastSeen] = useState("");
  const [claimSubmitting, setClaimSubmitting] = useState(false);

  // Direct collector modal
  const [dcOpen, setDcOpen] = useState(false);
  const [editingDc, setEditingDc] = useState<DirectCollector | null>(null);
  const [dcName, setDcName] = useState("");
  const [dcSite, setDcSite] = useState("home");
  const [dcNodeId, setDcNodeId] = useState("");
  const [dcUrl, setDcUrl] = useState("");
  const [dcOrigUrl, setDcOrigUrl] = useState("");
  const [dcSecret, setDcSecret] = useState("");
  const [dcClearSecret, setDcClearSecret] = useState(false);
  const [dcPollMs, setDcPollMs] = useState("15000");
  const [dcCollectDomains, setDcCollectDomains] = useState(false);
  const [dcEnabled, setDcEnabled] = useState(true);
  const [probeResult, setProbeResult] = useState<ProbeResult | null>(null);
  const [probing, setProbing] = useState(false);
  const [dcSubmitting, setDcSubmitting] = useState(false);

  const loadList = useCallback(async () => {
    try {
      const [nRes, dcRes] = await Promise.all([
        api.listNodes(timeRange),
        api.listDirectCollectors(),
      ]);
      setNodes(nRes.nodes || []);
      setNodeSeries(nRes.traffic);
      setCollectors(dcRes.collectors || []);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [timeRange]);

  const loadDetail = useCallback(async () => {
    if (!nodeId) return;
    try {
      const res = await api.getNode(nodeId, timeRange);
      setNodeDetail(res);
      setError(null);
    } catch (err) {
      setNodeDetail(null);
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [nodeId, timeRange]);

  useEffect(() => {
    setNodeDetail(null);
    setError(null);
  }, [nodeId]);

  useEffect(() => {
    if (nodeId) {
      void loadDetail();
    } else {
      void loadList();
    }
  }, [loadDetail, loadList, nodeId, refreshTick]);

  // 2s polling while claim modal is open and waiting
  useEffect(() => {
    if (!claimOpen || !claimRes || !activeClaimNodeId || claimArrived) return;
    const timer = window.setInterval(async () => {
      try {
        const res = await api.getNode(activeClaimNodeId, timeRange);
        if (
          res &&
          res.last_seen_at &&
          (!initialLastSeen || res.last_seen_at !== initialLastSeen)
        ) {
          setClaimArrived(true);
          if (nodeId) void loadDetail();
          else void loadList();
        }
      } catch {
        // node not reported yet
      }
    }, 2000);
    return () => window.clearInterval(timer);
  }, [
    activeClaimNodeId,
    claimArrived,
    claimOpen,
    claimRes,
    initialLastSeen,
    loadDetail,
    loadList,
    nodeId,
    timeRange,
  ]);

  const openClaimModal = (presetSite = "home", presetNode = "") => {
    setClaimSite(presetSite);
    setClaimNodeId(presetNode);
    setClaimLocal(false);
    setClaimRes(null);
    setClaimArrived(false);
    setActiveClaimNodeId("");
    setInitialLastSeen("");
    setClaimOpen(true);
  };

  const handleCreateClaim = async (e: React.FormEvent) => {
    e.preventDefault();
    if (claimSubmitting) return;
    const targetNodeId = claimNodeId.trim();
    const existingNode =
      nodeDetail && nodeDetail.id === targetNodeId
        ? nodeDetail
        : nodes.find((n) => n.id === targetNodeId);
    const baselineLastSeen = existingNode?.last_seen_at || "";
    setClaimSubmitting(true);
    try {
      const res = await api.createClaim(claimSite.trim(), targetNodeId, claimLocal);
      setClaimRes(res);
      setActiveClaimNodeId(targetNodeId);
      setInitialLastSeen(baselineLastSeen);
      setClaimArrived(false);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    } finally {
      setClaimSubmitting(false);
    }
  };

  const copyCommand = async () => {
    if (!claimRes) return;
    const cmd = claimRes[claimPlatform];
    try {
      await navigator.clipboard.writeText(cmd);
      showToast(t.copied);
    } catch {
      showToast(t.copyFailed);
    }
  };

  const toggleNodeDomains = async (id: string, current: boolean) => {
    try {
      await api.setNodeConfig(id, !current);
      showToast(t.saved);
      if (nodeId) await loadDetail();
      else await loadList();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const handleRevoke = async (id: string) => {
    const ok = window.confirm(
      lang === "zh-CN"
        ? `确定要吊销节点 ${id} 吗？如果是直连采集器节点，将同步停用对应采集器。`
        : `Revoke node ${id}?`,
    );
    if (!ok) return;
    try {
      await api.revokeNode(id);
      showToast(lang === "zh-CN" ? "节点已吊销" : "Node revoked");
      if (nodeId) navigate("nodes");
      else await loadList();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const openNewDirectCollector = () => {
    setEditingDc(null);
    setDcName("");
    setDcSite("home");
    setDcNodeId("");
    setDcUrl("");
    setDcOrigUrl("");
    setDcSecret("");
    setDcClearSecret(false);
    setDcPollMs("15000");
    setDcCollectDomains(false);
    setDcEnabled(true);
    setProbeResult(null);
    setDcOpen(true);
  };

  const openEditDirectCollector = (dc: DirectCollector) => {
    setEditingDc(dc);
    setDcName(dc.name);
    setDcSite(dc.site_id);
    setDcNodeId(dc.node_id);
    setDcUrl(dc.url);
    setDcOrigUrl(dc.url);
    setDcSecret(dc.has_secret ? "********" : "");
    setDcClearSecret(false);
    setDcPollMs(String(dc.poll_interval_ms || 15000));
    setDcCollectDomains(dc.collect_domains);
    setDcEnabled(dc.enabled);
    setProbeResult(null);
    setDcOpen(true);
  };

  const handleProbeInModal = async () => {
    setProbing(true);
    setProbeResult(null);
    try {
      const secretArg =
        !dcClearSecret && dcSecret && dcSecret !== "********" ? dcSecret : undefined;
      const trimmedUrl = dcUrl.trim();
      const urlArg =
        editingDc && trimmedUrl === dcOrigUrl ? undefined : trimmedUrl || undefined;
      const res = editingDc
        ? await api.testDirectCollectorByID(editingDc.id, urlArg, secretArg, dcClearSecret)
        : await api.testDirectCollectorConfig(trimmedUrl, secretArg);
      setProbeResult(res);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    } finally {
      setProbing(false);
    }
  };

  const handleProbeRow = async (id: number) => {
    try {
      const res = await api.testDirectCollectorByID(id);
      if (res.ok) {
        showToast(
          lang === "zh-CN"
            ? `连接成功 (${res.latency_ms}ms · ${res.proxy_count} 代理)`
            : `Connected (${res.latency_ms}ms · ${res.proxy_count} proxies)`,
        );
      } else {
        showToast(res.error || "Probe failed");
      }
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const handleSaveDirectCollector = async (e: React.FormEvent) => {
    e.preventDefault();
    if (dcSubmitting) return;
    const trimmedUrl = dcUrl.trim();
    const payload: DirectCollectorInput = {
      name: dcName.trim(),
      site_id: dcSite.trim() || "default",
      ...(dcNodeId.trim() ? { node_id: dcNodeId.trim() } : {}),
      ...(!editingDc || trimmedUrl !== dcOrigUrl ? { url: trimmedUrl } : {}),
      poll_interval_ms: Number(dcPollMs) || 15000,
      collect_domains: dcCollectDomains,
      enabled: dcEnabled,
    };
    if (dcClearSecret) {
      payload.clear_secret = true;
    } else if (dcSecret && dcSecret !== "********") {
      payload.secret = dcSecret;
    }
    setDcSubmitting(true);
    try {
      if (editingDc) {
        await api.updateDirectCollector(editingDc.id, payload);
      } else {
        await api.createDirectCollector(payload);
      }
      setDcOpen(false);
      showToast(t.saved);
      await loadList();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    } finally {
      setDcSubmitting(false);
    }
  };

  const toggleDc = async (dc: DirectCollector) => {
    try {
      await api.toggleDirectCollector(dc.id, !dc.enabled);
      showToast(t.saved);
      await loadList();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const deleteDc = async (id: number) => {
    try {
      await api.deleteDirectCollector(id);
      showToast(t.deleted);
      await loadList();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <section id="nodes" className="page-stack">
      {nodeId ? (
        <>
          <div>
            <a
              href="#nodes"
              className="back-link"
              onClick={(e) => {
                e.preventDefault();
                navigate("nodes");
              }}
            >
              ← {t.backToNodes}
            </a>
          </div>
          {error && <div className="banner banner-error">{error}</div>}
          {nodeDetail && (
            <>
              <div className="metrics">
                <article className="metric">
                  <div className="metric-label">{lang === "zh-CN" ? "节点状态" : "Status"}</div>
                  <div className={`metric-value ${nodeDetail.online ? "ok" : ""}`}>
                    {nodeDetail.online
                      ? lang === "zh-CN"
                        ? "在线"
                        : "Online"
                      : lang === "zh-CN"
                        ? "离线 / 静默"
                        : "Offline"}
                  </div>
                  <div className="metric-sub">
                    {nodeDetail.mode} · {formatLastSeen(nodeDetail.last_seen_at, lang)}
                  </div>
                </article>
                <article className="metric">
                  <div className="metric-label">{t.total}</div>
                  <div className="metric-value">{formatBytes(nodeDetail.total)}</div>
                  <div className="metric-sub">
                    RX {formatBytes(nodeDetail.rx)} · TX {formatBytes(nodeDetail.tx)}
                  </div>
                </article>
                <article className="metric">
                  <div className="metric-label">{t.direct}</div>
                  <div className="metric-value">{formatBytes(nodeDetail.bytes?.direct)}</div>
                </article>
                <article className="metric">
                  <div className="metric-label">{t.proxyAdjusted}</div>
                  <div className="metric-value brand">
                    {formatBytes(nodeDetail.bytes?.proxy_adjusted)}
                  </div>
                </article>
              </div>

              <div className="card">
                <div className="card-header">
                  <div>
                    <h3>{nodeDetail.id}</h3>
                    <p className="card-sub">
                      site: {nodeDetail.site_id} · boot: {nodeDetail.last_boot_id || "-"}
                    </p>
                  </div>
                  <div className="btn-row">
                    <button
                      type="button"
                      className={`chip-btn${nodeDetail.collect_domains ? " active" : ""}`}
                      onClick={() => toggleNodeDomains(nodeDetail.id, nodeDetail.collect_domains)}
                    >
                      {lang === "zh-CN" ? "域名采集: " : "Domains: "}
                      {nodeDetail.collect_domains ? "ON" : "OFF"}
                    </button>
                    {nodeDetail.mode !== "direct" && (
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        onClick={() => openClaimModal(nodeDetail.site_id, nodeDetail.id)}
                      >
                        {lang === "zh-CN" ? "重发领取码" : "Reissue Claim"}
                      </button>
                    )}
                    <button
                      type="button"
                      className="btn btn-danger btn-sm"
                      onClick={() => handleRevoke(nodeDetail.id)}
                    >
                      {lang === "zh-CN" ? "吊销节点" : "Revoke"}
                    </button>
                  </div>
                </div>
              </div>
            </>
          )}
        </>
      ) : (
        <>
          {error && <div className="banner banner-error">{error}</div>}

          {/* Direct Collectors Panel */}
          <div className="card">
            <div className="card-header">
              <div>
                <h3>{t.directCollectorsTitle}</h3>
                <p className="card-sub">{t.directCollectorsSub}</p>
              </div>
              <button type="button" className="btn btn-primary btn-sm" onClick={openNewDirectCollector}>
                {t.addDirectCollector}
              </button>
            </div>
            {collectors.length === 0 ? (
              <EmptyState
                title={lang === "zh-CN" ? "暂无直连采集器" : "No direct collectors"}
                description={
                  lang === "zh-CN"
                    ? "可直接填写局域网内 Mihomo / Clash External Controller 地址，由服务端直接采集"
                    : "Add a LAN Mihomo/Clash controller URL to poll directly from LanTally Server"
                }
              />
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>{lang === "zh-CN" ? "名称" : "Name"}</th>
                      <th>{lang === "zh-CN" ? "节点 ID" : "Node ID"}</th>
                      <th>URL</th>
                      <th>{lang === "zh-CN" ? "状态" : "Status"}</th>
                      <th>{lang === "zh-CN" ? "域名采集" : "Domains"}</th>
                      <th>{lang === "zh-CN" ? "启用" : "Enabled"}</th>
                      <th>{lang === "zh-CN" ? "操作" : "Actions"}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {collectors.map((dc) => (
                      <tr key={dc.id}>
                        <td className="sans">
                          <strong>{dc.name}</strong>
                          <div className="status-muted">{dc.site_id}</div>
                        </td>
                        <td>{dc.node_id}</td>
                        <td>
                          <div>{dc.url}</div>
                          {dc.has_secret && (
                            <span className="status-muted">
                              {lang === "zh-CN" ? "已配置 Secret" : "Secret set"}
                            </span>
                          )}
                        </td>
                        <td>
                          {dc.last_status === "ok" ? (
                            <span className="badge badge-ok">OK</span>
                          ) : dc.last_status === "error" ? (
                            <span className="badge badge-danger" title={dc.last_error}>
                              Error
                            </span>
                          ) : (
                            <span className="status-muted">-</span>
                          )}
                          {dc.last_error && (
                            <div className="text-danger" style={{ fontSize: "0.75rem", maxWidth: "16rem" }}>
                              {dc.last_error}
                            </div>
                          )}
                        </td>
                        <td>{dc.collect_domains ? "ON" : "OFF"}</td>
                        <td>
                          <button
                            type="button"
                            className={`chip-btn${dc.enabled ? " active" : ""}`}
                            onClick={() => toggleDc(dc)}
                          >
                            {dc.enabled ? "ON" : "OFF"}
                          </button>
                        </td>
                        <td>
                          <div className="btn-row">
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              onClick={() => handleProbeRow(dc.id)}
                            >
                              {lang === "zh-CN" ? "测试" : "Test"}
                            </button>
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              onClick={() => openEditDirectCollector(dc)}
                            >
                              {lang === "zh-CN" ? "编辑" : "Edit"}
                            </button>
                            <button
                              type="button"
                              className="btn btn-danger btn-sm"
                              onClick={() => deleteDc(dc.id)}
                            >
                              {lang === "zh-CN" ? "删除" : "Delete"}
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <StackedTrafficChart
            title={lang === "zh-CN" ? `节点流量趋势（${timeRange.preset}）` : `Node Traffic (${timeRange.preset})`}
            series={nodeSeries}
            direction={direction}
            onDirectionChange={setDirection}
          />

          {/* Registered Nodes */}
          <div className="card">
            <div className="card-header">
              <div>
                <h3>{t.nodesTitle}</h3>
                <p className="card-sub">{t.nodesSub}</p>
              </div>
              <button
                type="button"
                className="btn btn-primary btn-sm"
                onClick={() => openClaimModal()}
              >
                {t.addAgentNode}
              </button>
            </div>
            {nodes.length === 0 ? (
              <EmptyState
                title={t.noData}
                description={
                  lang === "zh-CN"
                    ? "添加直连采集器或生成 Agent 安装命令即可开始采集流量"
                    : "Add a direct collector or deploy an agent node to start collecting"
                }
              />
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>{lang === "zh-CN" ? "节点" : "Node"}</th>
                      <th>{lang === "zh-CN" ? "站点" : "Site"}</th>
                      <th>{lang === "zh-CN" ? "模式" : "Mode"}</th>
                      <th>{lang === "zh-CN" ? "最近上报" : "Last Seen"}</th>
                      <th>RX</th>
                      <th>TX</th>
                      <th>{t.total}</th>
                      <th>{lang === "zh-CN" ? "域名采集 (opt-in)" : "Domains"}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {nodes.map((n) => (
                      <tr
                        key={n.id}
                        className="row-link"
                        onClick={() => navigate("nodes", n.id)}
                      >
                        <td className="sans">
                          <strong>{n.id}</strong>
                        </td>
                        <td>{n.site_id}</td>
                        <td>
                          <span className={`badge${n.mode === "direct" ? " badge-info" : ""}`}>
                            {n.mode || "agent"}
                          </span>
                        </td>
                        <td>{formatLastSeen(n.last_seen_at, lang)}</td>
                        <td>{formatBytes(n.rx)}</td>
                        <td>{formatBytes(n.tx)}</td>
                        <td>{formatBytes(n.total)}</td>
                        <td onClick={(e) => e.stopPropagation()}>
                          <button
                            type="button"
                            className={`chip-btn${n.collect_domains ? " active" : ""}`}
                            onClick={() => toggleNodeDomains(n.id, n.collect_domains)}
                          >
                            {n.collect_domains ? "ON" : "OFF"}
                          </button>
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

      {/* Agent Claim Modal */}
      <Modal
        open={claimOpen}
        title={t.addAgentNode}
        subtitle={
          lang === "zh-CN"
            ? "生成一次性领取码，在目标机器执行一条命令即可完成部署"
            : "Generate a one-time claim code and run the installer on the target host"
        }
        onClose={() => setClaimOpen(false)}
      >
        <form onSubmit={handleCreateClaim} className="modal-form-stack">
          <div className="field-row">
            <div className="field">
              <label>{lang === "zh-CN" ? "站点 ID" : "Site ID"}</label>
              <input
                required
                value={claimSite}
                onChange={(e) => setClaimSite(e.target.value)}
                placeholder="home"
              />
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "节点 ID" : "Node ID"}</label>
              <input
                required
                value={claimNodeId}
                onChange={(e) => setClaimNodeId(e.target.value)}
                placeholder="proxy-node-1"
              />
            </div>
          </div>
          <label className="check-field">
            <input
              type="checkbox"
              checked={claimLocal}
              onChange={(e) => setClaimLocal(e.target.checked)}
            />
            {lang === "zh-CN"
              ? "本机即节点（server 和 agent 同机，Mihomo 默认 127.0.0.1:9090）"
              : "Local host mode (agent co-located with server)"}
          </label>

          {claimRes && (
            <div className="token-panel">
              <label>
                {lang === "zh-CN" ? "领取码" : "Claim Code"} <code>{claimRes.code}</code>
              </label>
              <div className="chip-group install-platforms">
                <button
                  type="button"
                  className={`chip-btn${claimPlatform === "linux" ? " active" : ""}`}
                  onClick={() => setClaimPlatform("linux")}
                >
                  OpenWrt / Linux
                </button>
                <button
                  type="button"
                  className={`chip-btn${claimPlatform === "macos" ? " active" : ""}`}
                  onClick={() => setClaimPlatform("macos")}
                >
                  macOS
                </button>
                <button
                  type="button"
                  className={`chip-btn${claimPlatform === "windows" ? " active" : ""}`}
                  onClick={() => setClaimPlatform("windows")}
                >
                  Windows
                </button>
              </div>
              <div className="token-row">
                <code>{claimRes[claimPlatform]}</code>
                <button type="button" className="btn btn-secondary btn-sm" onClick={copyCommand}>
                  {lang === "zh-CN" ? "复制命令" : "Copy"}
                </button>
              </div>
              <p className={`wait-status${claimArrived ? " ok" : ""}`}>
                {claimArrived
                  ? lang === "zh-CN"
                    ? "已收到第一包 ✓"
                    : "First batch received ✓"
                  : lang === "zh-CN"
                    ? "等待第一包上报…"
                    : "Waiting for first batch..."}
              </p>
              {claimRes.mihomo_hint && (
                <p className="note" style={{ padding: "0.4rem 0 0" }}>
                  {claimRes.mihomo_hint}
                </p>
              )}
            </div>
          )}

          <div className="modal-footer">
            <button type="submit" className="btn btn-primary btn-sm" disabled={claimSubmitting}>
              {lang === "zh-CN" ? "生成安装命令" : "Generate Command"}
            </button>
          </div>
        </form>
      </Modal>

      {/* Direct Collector Modal */}
      <Modal
        open={dcOpen}
        wide
        title={editingDc ? t.editDirectCollector : t.addDirectCollector}
        onClose={() => setDcOpen(false)}
      >
        <form onSubmit={handleSaveDirectCollector} className="modal-form-stack">
          <div className="field-row">
            <div className="field">
              <label>{lang === "zh-CN" ? "名称" : "Name"}</label>
              <input
                required
                value={dcName}
                onChange={(e) => setDcName(e.target.value)}
                placeholder="OpenWrt Mihomo"
              />
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "站点 ID" : "Site ID"}</label>
              <input
                value={dcSite}
                onChange={(e) => setDcSite(e.target.value)}
                placeholder="home"
              />
            </div>
          </div>

          <div className="field-row">
            <div className="field">
              <label>
                Mihomo API URL{" "}
                {editingDc && dcOrigUrl.includes("[redacted]") && (
                  <span className="status-muted">
                    ({lang === "zh-CN" ? "保持掩码 URL 不变即不覆盖原密钥" : "Leave masked URL unchanged to keep"})
                  </span>
                )}
              </label>
              <input
                required
                value={dcUrl}
                onChange={(e) => setDcUrl(e.target.value)}
                placeholder="http://127.0.0.1:9090"
              />
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "节点 ID (可选，留空自动生成)" : "Node ID (opt)"}</label>
              <input
                value={dcNodeId}
                disabled={Boolean(editingDc)}
                onChange={(e) => setDcNodeId(e.target.value)}
                placeholder="dc-router"
              />
            </div>
          </div>

          <div className="field-row">
            <div className="field">
              <label>
                Secret{" "}
                {editingDc?.has_secret && !dcClearSecret && (
                  <span className="status-muted">
                    ({lang === "zh-CN" ? "保留 ******** 即不修改" : "Leave ******** to keep"})
                  </span>
                )}
              </label>
              <input
                type="password"
                value={dcSecret}
                disabled={dcClearSecret}
                onChange={(e) => setDcSecret(e.target.value)}
                placeholder={lang === "zh-CN" ? "留空表示无密钥" : "Optional secret"}
              />
              {editingDc?.has_secret && (
                <label className="check-field" style={{ marginTop: "0.35rem" }}>
                  <input
                    type="checkbox"
                    checked={dcClearSecret}
                    onChange={(e) => setDcClearSecret(e.target.checked)}
                  />
                  {lang === "zh-CN" ? "清除已保存的 Secret" : "Clear stored secret"}
                </label>
              )}
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "落盘间隔 (ms)" : "Flush Interval (ms)"}</label>
              <input
                type="number"
                min={200}
                max={300000}
                value={dcPollMs}
                onChange={(e) => setDcPollMs(e.target.value)}
              />
            </div>
          </div>

          <div className="check-inline-row">
            <label className="check-field">
              <input
                type="checkbox"
                checked={dcEnabled}
                onChange={(e) => setDcEnabled(e.target.checked)}
              />
              {lang === "zh-CN" ? "启用采集" : "Enabled"}
            </label>
            <label className="check-field">
              <input
                type="checkbox"
                checked={dcCollectDomains}
                onChange={(e) => setDcCollectDomains(e.target.checked)}
              />
              {lang === "zh-CN" ? "开启域名采集 (opt-in)" : "Collect Domains (opt-in)"}
            </label>
          </div>

          {probeResult && (
            <div className={`banner ${probeResult.ok ? "banner-ok" : "banner-error"}`}>
              {probeResult.ok ? (
                <span>
                  ✓ {probeResult.version || "Mihomo"} · {probeResult.proxy_count} proxies ·{" "}
                  {probeResult.group_count} groups · WS: {probeResult.ws_supported ? "Yes" : "No"} ·{" "}
                  {probeResult.latency_ms}ms
                </span>
              ) : (
                <span>✗ {probeResult.error}</span>
              )}
            </div>
          )}

          <div className="modal-footer" style={{ justifyContent: "space-between" }}>
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              disabled={probing || !dcUrl.trim()}
              onClick={handleProbeInModal}
            >
              {probing
                ? lang === "zh-CN"
                  ? "探测中…"
                  : "Testing..."
                : lang === "zh-CN"
                  ? "测试连接"
                  : "Test Connection"}
            </button>
            <button type="submit" className="btn btn-primary btn-sm" disabled={dcSubmitting}>
              {lang === "zh-CN" ? "保存" : "Save"}
            </button>
          </div>
        </form>
      </Modal>
    </section>
  );
}
