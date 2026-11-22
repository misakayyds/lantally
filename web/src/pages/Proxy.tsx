import React, { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type {
  DimensionStat,
  MetricDirection,
  MultiplierRule,
  PreviewMatch,
  ProviderInput,
  ProvidersResponse,
  ProviderStatus,
  ProxyResponse,
  RuleInput,
  RuleMatchType,
  TrafficSeries,
} from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { Modal } from "../components/Modal";
import { StackedTrafficChart } from "../components/StackedTrafficChart";
import { useApp } from "../state/AppContext";
import { downloadBlob, formatBytes, formatDateISO, formatPercent, formatSignedBytes } from "../utils/format";

export default function Proxy() {
  const { lang, t, timeRange, refreshTick, showToast } = useApp();
  const [proxyData, setProxyData] = useState<ProxyResponse | null>(null);
  const [groupsData, setGroupsData] = useState<DimensionStat[]>([]);
  const [groupSeries, setGroupSeries] = useState<TrafficSeries | null>(null);
  const [showGroupChart, setShowGroupChart] = useState(false);
  const [rules, setRules] = useState<MultiplierRule[]>([]);
  const [providersRes, setProvidersRes] = useState<ProvidersResponse | null>(null);
  const [direction, setDirection] = useState<MetricDirection>("total");
  const [inlineFactors, setInlineFactors] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);

  // Rule modal
  const [ruleModalOpen, setRuleModalOpen] = useState(false);
  const [editingRule, setEditingRule] = useState<MultiplierRule | null>(null);
  const [ruleSubmitting, setRuleSubmitting] = useState(false);
  const [ruleFactorInput, setRuleFactorInput] = useState("1");
  const [ruleForm, setRuleForm] = useState<RuleInput>({
    name: "",
    match_type: "keyword",
    pattern: "",
    factor: 1,
    priority: 100,
    enabled: true,
  });

  // Import JSON modal
  const [importOpen, setImportOpen] = useState(false);
  const [importMode, setImportMode] = useState<"replace" | "merge">("merge");
  const [importText, setImportText] = useState("");
  const [importSubmitting, setImportSubmitting] = useState(false);

  // Live Preview
  const [previewInput, setPreviewInput] = useState("");
  const [previewMatches, setPreviewMatches] = useState<PreviewMatch[] | null>(null);

  // Provider modal
  const [providerModalOpen, setProviderModalOpen] = useState(false);
  const [editingProvider, setEditingProvider] = useState<ProviderStatus | null>(null);
  const [providerSubmitting, setProviderSubmitting] = useState(false);
  const [providerOrig, setProviderOrig] = useState<{
    unit: "MB" | "GB" | "TB";
    total_value: string;
    used_value: string;
    linked_nodes: string;
  } | null>(null);
  const [providerForm, setProviderForm] = useState<{
    name: string;
    unit: "MB" | "GB" | "TB";
    total_value: string;
    used_value: string;
    reset_day: string;
    outbound_pattern: string;
    linked_nodes: string;
    expire_date: string;
    enabled: boolean;
  }>({
    name: "",
    unit: "GB",
    total_value: "500",
    used_value: "0",
    reset_day: "1",
    outbound_pattern: "",
    linked_nodes: "",
    expire_date: "",
    enabled: true,
  });

  // Legacy billing
  const [resetDay, setResetDay] = useState("1");
  const [providerGB, setProviderGB] = useState("0");

  const load = useCallback(async () => {
    try {
      const [pRes, gRes, rRes, provRes] = await Promise.all([
        api.proxy(timeRange),
        api.dimensions(timeRange, { type: "group", limit: 100, sort: "total" }),
        api.listRules(),
        api.listProviders(),
      ]);
      setProxyData(pRes);
      setGroupsData(gRes.items || []);
      setRules(rRes.rules || []);
      setProvidersRes(provRes);
      setResetDay(String(pRes.billing?.reset_day || 1));
      setProviderGB(((pRes.billing?.provider_bytes || 0) / 1024 ** 3).toFixed(2));
      const facMap: Record<string, string> = {};
      for (const ob of pRes.outbounds || []) {
        facMap[ob.name] = String(ob.factor);
      }
      setInlineFactors(facMap);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [timeRange]);

  useEffect(() => {
    void load();
  }, [load, refreshTick]);

  useEffect(() => {
    if (!showGroupChart) return;
    api
      .traffic(timeRange, { group: "group" })
      .then(setGroupSeries)
      .catch(() => {});
  }, [showGroupChart, timeRange, refreshTick]);

  const handleQuickFactor = async (name: string) => {
    const raw = (inlineFactors[name] ?? "").trim();
    if (raw === "") {
      showToast(lang === "zh-CN" ? "请输入有效倍率" : "Enter a valid factor");
      return;
    }
    const val = Number(raw);
    if (!Number.isFinite(val) || val < 0) {
      showToast(lang === "zh-CN" ? "倍率不能小于 0" : "Factor must be >= 0");
      return;
    }
    try {
      if (val === 0) {
        const existing = rules.find(
          (r) => r.match_type === "exact" && r.pattern === name,
        );
        const payload: RuleInput = {
          name: existing ? existing.name : name,
          match_type: "exact",
          pattern: name,
          factor: 0,
          priority: existing ? existing.priority : 100,
          enabled: true,
        };
        if (existing) {
          await api.updateRule(existing.id, payload);
        } else {
          await api.createRule(payload);
        }
      } else {
        await api.setQuickMultiplier(name, val);
      }
      showToast(t.saved);
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const openNewRule = () => {
    setEditingRule(null);
    setRuleFactorInput("1");
    setRuleForm({
      name: "",
      match_type: "keyword",
      pattern: "",
      factor: 1,
      priority: 100,
      enabled: true,
    });
    setRuleModalOpen(true);
  };

  const openEditRule = (rule: MultiplierRule) => {
    setEditingRule(rule);
    setRuleFactorInput(String(rule.factor));
    setRuleForm({
      name: rule.name,
      match_type: rule.match_type,
      pattern: rule.pattern,
      factor: rule.factor,
      priority: rule.priority,
      enabled: rule.enabled,
    });
    setRuleModalOpen(true);
  };

  const saveRule = async (e: React.FormEvent) => {
    e.preventDefault();
    if (ruleSubmitting) return;
    let parsedFactor = ruleForm.factor;
    if (ruleForm.match_type !== "auto_extract") {
      const rawFactor = ruleFactorInput.trim();
      if (rawFactor === "") {
        showToast(lang === "zh-CN" ? "请输入有效倍率" : "Enter a valid factor");
        return;
      }
      parsedFactor = Number(rawFactor);
      if (!Number.isFinite(parsedFactor) || parsedFactor < 0) {
        showToast(lang === "zh-CN" ? "倍率不能小于 0" : "Factor must be >= 0");
        return;
      }
    }
    const payload: RuleInput = { ...ruleForm, factor: parsedFactor };
    setRuleSubmitting(true);
    try {
      if (editingRule) {
        await api.updateRule(editingRule.id, payload);
      } else {
        await api.createRule(payload);
      }
      setRuleModalOpen(false);
      showToast(t.saved);
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    } finally {
      setRuleSubmitting(false);
    }
  };

  const toggleRuleEnabled = async (rule: MultiplierRule) => {
    try {
      await api.updateRule(rule.id, {
        name: rule.name,
        match_type: rule.match_type,
        pattern: rule.pattern,
        factor: rule.factor,
        priority: rule.priority,
        enabled: !rule.enabled,
      });
      showToast(t.saved);
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const removeRule = async (id: number) => {
    try {
      await api.deleteRule(id);
      showToast(t.deleted);
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const handleExportRules = async () => {
    try {
      const blob = await api.exportRules();
      downloadBlob(blob, "lantally-multiplier-rules.json");
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const handleImportRules = async (e: React.FormEvent) => {
    e.preventDefault();
    if (importSubmitting) return;
    setImportSubmitting(true);
    try {
      const parsed: unknown = JSON.parse(importText);
      const list = Array.isArray(parsed)
        ? (parsed as RuleInput[])
        : parsed && typeof parsed === "object" && Array.isArray((parsed as { rules?: unknown }).rules)
          ? ((parsed as { rules: RuleInput[] }).rules)
          : null;
      if (!list) {
        showToast(lang === "zh-CN" ? "JSON 必须包含 rules 数组" : "JSON must contain a rules array");
        return;
      }
      const res = await api.importRules(importMode, list);
      setImportOpen(false);
      setImportText("");
      showToast(
        lang === "zh-CN" ? `已导入 ${res.imported} 条规则` : `Imported ${res.imported} rules`,
      );
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    } finally {
      setImportSubmitting(false);
    }
  };

  const runPreview = async () => {
    const customNames = previewInput
      .split(/\r?\n/)
      .map((s) => s.trim())
      .filter(Boolean);
    const names =
      customNames.length > 0 ? customNames : (proxyData?.outbounds || []).map((o) => o.name);
    try {
      const res = await api.previewRules(names);
      setPreviewMatches(res.matches || []);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const openNewProvider = () => {
    setEditingProvider(null);
    setProviderOrig(null);
    setProviderForm({
      name: "",
      unit: "GB",
      total_value: "500",
      used_value: "0",
      reset_day: "1",
      outbound_pattern: "",
      linked_nodes: "",
      expire_date: "",
      enabled: true,
    });
    setProviderModalOpen(true);
  };

  const openEditProvider = (p: ProviderStatus) => {
    setEditingProvider(p);
    const unitRaw = (p.unit || p.quota_unit || "GB").toUpperCase();
    const unit: "MB" | "GB" | "TB" = unitRaw === "MB" ? "MB" : unitRaw === "TB" ? "TB" : "GB";
    const div = unit === "MB" ? 1024 ** 2 : unit === "TB" ? 1024 ** 4 : 1024 ** 3;
    const exp = p.expire_at || p.expire || 0;
    let expStr = "";
    if (exp > 0) {
      const d = new Date(exp * 1000);
      if (!Number.isNaN(d.getTime())) {
        expStr = `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}-${String(d.getUTCDate()).padStart(2, "0")}`;
      }
    }
    const totalStr = p.total_bytes > 0 ? (p.total_bytes / div).toFixed(2) : "0";
    const usedStr = p.used_bytes > 0 ? (p.used_bytes / div).toFixed(2) : "0";
    const linkedStr = (p.linked_nodes || []).join(", ");
    setProviderOrig({
      unit,
      total_value: totalStr,
      used_value: usedStr,
      linked_nodes: linkedStr,
    });
    setProviderForm({
      name: p.name,
      unit,
      total_value: totalStr,
      used_value: usedStr,
      reset_day: String(p.reset_day || 1),
      outbound_pattern: p.outbound_pattern || "",
      linked_nodes: linkedStr,
      expire_date: expStr,
      enabled: p.enabled,
    });
    setProviderModalOpen(true);
  };

  const saveProvider = async (e: React.FormEvent) => {
    e.preventDefault();
    if (providerSubmitting) return;
    const totalVal = Number(providerForm.total_value);
    const usedVal = Number(providerForm.used_value);
    const rDay = Number(providerForm.reset_day);
    const resetDayClamped = Number.isFinite(rDay) && rDay >= 1 && rDay <= 28 ? rDay : 1;

    let payload: ProviderInput;
    if (editingProvider) {
      payload = {
        name: providerForm.name.trim(),
        reset_day: resetDayClamped,
        outbound_pattern: providerForm.outbound_pattern.trim(),
        enabled: providerForm.enabled,
      };
      const unitChanged = !providerOrig || providerForm.unit !== providerOrig.unit;
      const totalChanged = !providerOrig || providerForm.total_value !== providerOrig.total_value;
      const usedChanged = !providerOrig || providerForm.used_value !== providerOrig.used_value;
      const linkedChanged =
        !providerOrig || providerForm.linked_nodes.trim() !== providerOrig.linked_nodes.trim();
      if (unitChanged) {
        payload.unit = providerForm.unit;
      }
      if (totalChanged || unitChanged) {
        payload.total_value = Number.isFinite(totalVal) ? totalVal : 0;
      }
      if (usedChanged) {
        payload.used_value = Number.isFinite(usedVal) ? usedVal : 0;
      }
      if (linkedChanged) {
        payload.linked_nodes = providerForm.linked_nodes
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean);
      }
      if (providerForm.expire_date) {
        const ts = Math.floor(new Date(`${providerForm.expire_date}T23:59:59Z`).getTime() / 1000);
        if (Number.isFinite(ts) && ts > 0) payload.expire_at = ts;
      } else {
        payload.expire_at = 0;
      }
    } else {
      payload = {
        name: providerForm.name.trim(),
        unit: providerForm.unit,
        total_value: Number.isFinite(totalVal) ? totalVal : 0,
        used_value: Number.isFinite(usedVal) ? usedVal : 0,
        reset_day: resetDayClamped,
        outbound_pattern: providerForm.outbound_pattern.trim(),
        linked_nodes: providerForm.linked_nodes
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        enabled: providerForm.enabled,
      };
      if (providerForm.expire_date) {
        const ts = Math.floor(new Date(`${providerForm.expire_date}T23:59:59Z`).getTime() / 1000);
        if (Number.isFinite(ts) && ts > 0) payload.expire_at = ts;
      }
    }

    setProviderSubmitting(true);
    try {
      if (editingProvider) {
        await api.updateProvider(editingProvider.id, payload);
      } else {
        await api.createProvider(payload);
      }
      setProviderModalOpen(false);
      showToast(t.saved);
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    } finally {
      setProviderSubmitting(false);
    }
  };

  const removeProvider = async (id: number) => {
    try {
      await api.deleteProvider(id);
      showToast(t.deleted);
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const saveBilling = async (e: React.FormEvent) => {
    e.preventDefault();
    const rDay = Number(resetDay);
    const gb = Number(providerGB);
    if (!Number.isFinite(rDay) || rDay < 1 || rDay > 28) {
      showToast(lang === "zh-CN" ? "结算日需在 1..28" : "Reset day must be 1..28");
      return;
    }
    const bytes = Math.max(0, Math.round((Number.isFinite(gb) ? gb : 0) * 1024 ** 3));
    try {
      await api.setBilling(rDay, bytes);
      showToast(t.saved);
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const pbd = proxyData?.proxy_breakdown || {};
  const pSummary = proxyData?.proxy || {};
  const outbounds = proxyData?.outbounds || [];
  const providers = providersRes?.providers || [];
  const provSum = providersRes?.summary;

  return (
    <section id="proxy" className="page-stack">
      {error && <div className="banner banner-error">{error}</div>}

      <div className="metrics">
        <article className="metric">
          <div className="metric-label">{t.direct}</div>
          <div className="metric-value">{formatBytes(pSummary.direct)}</div>
          {pbd.direct && (
            <div className="metric-sub">
              RX {formatBytes(pbd.direct.rx)} · TX {formatBytes(pbd.direct.tx)}
            </div>
          )}
        </article>
        <article className="metric">
          <div className="metric-label">{t.proxyRaw}</div>
          <div className="metric-value">{formatBytes(pSummary.proxy_raw)}</div>
          <div className="metric-sub">
            RX {formatBytes(proxyData?.rx)} · TX {formatBytes(proxyData?.tx)}
          </div>
        </article>
        <article className="metric">
          <div className="metric-label">{t.proxyAdjusted}</div>
          <div className="metric-value brand">{formatBytes(pSummary.proxy_adjusted)}</div>
          {pbd.proxy_adjusted && (
            <div className="metric-sub">
              RX {formatBytes(pbd.proxy_adjusted.rx)} · TX {formatBytes(pbd.proxy_adjusted.tx)}
            </div>
          )}
        </article>
        <article className="metric">
          <div className="metric-label">{t.proxyUnadjusted}</div>
          <div className="metric-value">{formatBytes(pSummary.proxy_unadjusted)}</div>
          <div className="metric-sub">
            {lang === "zh-CN" ? "未命中任何倍率规则" : "Matched default 1x"}
          </div>
        </article>
      </div>

      <StackedTrafficChart
        title={lang === "zh-CN" ? `代理出口趋势（${timeRange.preset}）` : `Outbound Traffic (${timeRange.preset})`}
        series={proxyData?.traffic}
        direction={direction}
        onDirectionChange={setDirection}
      />

      {/* Exit Nodes */}
      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.exitNodesTitle}</h3>
            <p className="card-sub">{t.exitNodesSub}</p>
          </div>
        </div>
        {outbounds.length === 0 ? (
          <EmptyState
            title={t.noData}
            description={lang === "zh-CN" ? "暂无出口节点代理记录" : "No outbound traffic recorded yet"}
          />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === "zh-CN" ? "出口节点" : "Exit Node"}</th>
                  <th>RX</th>
                  <th>TX</th>
                  <th>{lang === "zh-CN" ? "原始" : "Raw"}</th>
                  <th>{lang === "zh-CN" ? "倍率" : "Factor"}</th>
                  <th>{lang === "zh-CN" ? "倍率后" : "Adjusted"}</th>
                  <th>{lang === "zh-CN" ? "占比" : "Share"}</th>
                  <th>{lang === "zh-CN" ? "快捷改倍率" : "Quick Set"}</th>
                </tr>
              </thead>
              <tbody>
                {outbounds.map((ob) => (
                  <tr key={ob.name}>
                    <td className="sans">{ob.name}</td>
                    <td>{formatBytes(ob.raw_rx ?? ob.rx)}</td>
                    <td>{formatBytes(ob.raw_tx ?? ob.tx)}</td>
                    <td>{formatBytes(ob.raw)}</td>
                    <td>
                      <span style={{ marginRight: "0.4rem" }}>{ob.factor}x</span>
                      {ob.match_source === "default" ? (
                        <span className="badge">{t.default1x}</span>
                      ) : (
                        <span className="badge badge-info">
                          {ob.matched_rule_name || ob.matched_rule || t.ruleMatched}
                        </span>
                      )}
                    </td>
                    <td>{formatBytes(ob.adjusted)}</td>
                    <td>{formatPercent(ob.share)}</td>
                    <td>
                      <div className="inline-edit">
                        <input
                          type="number"
                          step="any"
                          min="0"
                          className="factor-input"
                          value={inlineFactors[ob.name] ?? String(ob.factor)}
                          onChange={(e) =>
                            setInlineFactors((prev) => ({ ...prev, [ob.name]: e.target.value }))
                          }
                        />
                        <button
                          type="button"
                          className="btn btn-secondary btn-sm"
                          onClick={() => handleQuickFactor(ob.name)}
                        >
                          {t.apply}
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

      {/* Strategy Groups */}
      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.strategyGroupsTitle}</h3>
            <p className="card-sub">{t.strategyGroupsSub}</p>
          </div>
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => setShowGroupChart((v) => !v)}
          >
            {showGroupChart
              ? lang === "zh-CN"
                ? "隐藏趋势图"
                : "Hide Chart"
              : lang === "zh-CN"
                ? "显示趋势图"
                : "Show Chart"}
          </button>
        </div>
        {showGroupChart && groupSeries && (
          <div style={{ padding: "1rem 1.15rem 0" }}>
            <StackedTrafficChart
              title={lang === "zh-CN" ? "策略组趋势" : "Strategy Group Trend"}
              series={groupSeries}
              direction={direction}
              onDirectionChange={setDirection}
            />
          </div>
        )}
        {groupsData.length === 0 ? (
          <EmptyState
            title={t.noData}
            description={lang === "zh-CN" ? "暂无策略组记录" : "No strategy group traffic yet"}
          />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === "zh-CN" ? "策略组" : "Group"}</th>
                  <th>RX</th>
                  <th>TX</th>
                  <th>{t.total}</th>
                  <th>{lang === "zh-CN" ? "占比" : "Share"}</th>
                </tr>
              </thead>
              <tbody>
                {groupsData.map((g) => (
                  <tr key={g.key}>
                    <td className="sans">{g.key}</td>
                    <td>{formatBytes(g.rx)}</td>
                    <td>{formatBytes(g.tx)}</td>
                    <td>{formatBytes(g.total)}</td>
                    <td>
                      <div className="share-cell">
                        <div className="share-bar">
                          <div style={{ width: `${Math.min(100, g.share * 100)}%` }} />
                        </div>
                        <span>{formatPercent(g.share)}</span>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Multiplier Rule Editor + Preview */}
      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.rulesTitle}</h3>
            <p className="card-sub">{t.rulesSub}</p>
          </div>
          <div className="btn-row">
            <button type="button" className="btn btn-secondary btn-sm" onClick={handleExportRules}>
              {t.exportJson}
            </button>
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              onClick={() => setImportOpen(true)}
            >
              {t.importJson}
            </button>
            <button type="button" className="btn btn-primary btn-sm" onClick={openNewRule}>
              {t.addRule}
            </button>
          </div>
        </div>
        {rules.length === 0 ? (
          <EmptyState
            title={lang === "zh-CN" ? "暂无倍率规则" : "No multiplier rules"}
            description={
              lang === "zh-CN"
                ? "未配置规则时所有出口节点均按默认 1x 计入倍率后流量"
                : "All exit nodes default to 1x when no rules are configured"
            }
          />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === "zh-CN" ? "名称" : "Name"}</th>
                  <th>{lang === "zh-CN" ? "类型" : "Type"}</th>
                  <th>{lang === "zh-CN" ? "模式" : "Pattern"}</th>
                  <th>{lang === "zh-CN" ? "倍率" : "Factor"}</th>
                  <th>{lang === "zh-CN" ? "优先级" : "Priority"}</th>
                  <th>{lang === "zh-CN" ? "状态" : "Enabled"}</th>
                  <th>{lang === "zh-CN" ? "操作" : "Actions"}</th>
                </tr>
              </thead>
              <tbody>
                {rules.map((r) => (
                  <tr key={r.id}>
                    <td className="sans">{r.name}</td>
                    <td>
                      <span className="badge badge-info">{r.match_type}</span>
                    </td>
                    <td>{r.pattern || <span className="status-muted">(auto)</span>}</td>
                    <td>{r.match_type === "auto_extract" ? "auto" : `${r.factor}x`}</td>
                    <td>{r.priority}</td>
                    <td>
                      <button
                        type="button"
                        className={`chip-btn${r.enabled ? " active" : ""}`}
                        onClick={() => toggleRuleEnabled(r)}
                      >
                        {r.enabled
                          ? lang === "zh-CN"
                            ? "启用"
                            : "Enabled"
                          : lang === "zh-CN"
                            ? "停用"
                            : "Disabled"}
                      </button>
                    </td>
                    <td>
                      <div className="btn-row">
                        <button
                          type="button"
                          className="btn btn-secondary btn-sm"
                          onClick={() => openEditRule(r)}
                        >
                          {lang === "zh-CN" ? "编辑" : "Edit"}
                        </button>
                        <button
                          type="button"
                          className="btn btn-danger btn-sm"
                          onClick={() => removeRule(r.id)}
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

        <div className="preview-box">
          <h4>{t.livePreview}</h4>
          <div className="preview-controls">
            <textarea
              rows={2}
              placeholder={t.previewPlaceholder}
              value={previewInput}
              onChange={(e) => setPreviewInput(e.target.value)}
            />
            <button type="button" className="btn btn-secondary btn-sm" onClick={runPreview}>
              {t.runPreview}
            </button>
          </div>
          {previewMatches && (
            <div className="preview-results">
              {previewMatches.map((m) => (
                <div key={m.name} className="preview-chip">
                  <span>{m.name}</span>
                  <strong>{m.factor}x</strong>
                  <span className={m.match_source === "default" ? "badge" : "badge badge-info"}>
                    {m.match_source === "default"
                      ? t.default1x
                      : m.matched_rule_name || m.matched_rule || m.matched_type}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Multi-Provider Quota Reconciliation */}
      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.providersTitle}</h3>
            <p className="card-sub">{t.providersSub}</p>
          </div>
          <button type="button" className="btn btn-primary btn-sm" onClick={openNewProvider}>
            {t.addProvider}
          </button>
        </div>
        {provSum && (
          <div className="provider-summary-bar">
            <span>
              {lang === "zh-CN" ? "活跃订阅" : "Active"}: <strong>{provSum.active_count}</strong>
            </span>
            <span>
              {lang === "zh-CN" ? "机场已用 / 总额" : "Provider Used / Total"}:{" "}
              <strong className="mono">
                {formatBytes(provSum.used_bytes)} / {formatBytes(provSum.total_bytes)}
              </strong>
            </span>
            <span>
              {lang === "zh-CN" ? "本地倍率后" : "Local Adjusted"}:{" "}
              <strong className="mono">{formatBytes(provSum.local_adjusted)}</strong>
            </span>
            <span>
              {lang === "zh-CN" ? "偏差 Drift" : "Drift"}:{" "}
              <strong className={`mono${provSum.drift_alert ? " text-danger" : ""}`}>
                {formatSignedBytes(provSum.drift_bytes)} ({formatPercent(provSum.drift_ratio)})
              </strong>
            </span>
          </div>
        )}
        {providers.length === 0 ? (
          <EmptyState
            title={t.noData}
            description={
              lang === "zh-CN"
                ? "配置 Mihomo 订阅自动同步或手动添加 MB/GB/TB 套餐进行对账"
                : "Auto-syncs Mihomo HTTP providers or add manual MB/GB/TB subscriptions"
            }
          />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{lang === "zh-CN" ? "订阅名称" : "Provider"}</th>
                  <th>{lang === "zh-CN" ? "来源" : "Source"}</th>
                  <th>{lang === "zh-CN" ? "机场已用 / 配额" : "Used / Total"}</th>
                  <th>{lang === "zh-CN" ? "本地估算" : "Local Adjusted"}</th>
                  <th>{lang === "zh-CN" ? "漂移" : "Drift"}</th>
                  <th>{lang === "zh-CN" ? "到期 / 结算日" : "Expiry / Reset"}</th>
                  <th>{lang === "zh-CN" ? "操作" : "Actions"}</th>
                </tr>
              </thead>
              <tbody>
                {providers.map((p) => {
                  const pct = p.total_bytes > 0 ? Math.min(100, (p.used_bytes / p.total_bytes) * 100) : 0;
                  return (
                    <tr key={p.id}>
                      <td className="sans">
                        <div>{p.name}</div>
                        {p.shared_group && (
                          <span className="badge badge-info">
                            {lang === "zh-CN" ? `共享组 ${p.shared_group}` : `Shared ${p.shared_group}`}
                          </span>
                        )}
                      </td>
                      <td>
                        <span className={`badge${p.source === "mihomo" ? " badge-ok" : ""}`}>
                          {p.source === "mihomo" ? "Mihomo" : "Manual"}
                        </span>
                      </td>
                      <td>
                        <div>
                          {formatBytes(p.used_bytes)} / {formatBytes(p.total_bytes)}
                        </div>
                        {p.total_bytes > 0 && (
                          <div className="progress-track compact">
                            <div className="progress-fill" style={{ width: `${pct}%` }} />
                          </div>
                        )}
                      </td>
                      <td>{formatBytes(p.local_adjusted)}</td>
                      <td>
                        <span className={p.drift_alert || p.over_drift ? "text-danger" : ""}>
                          {formatSignedBytes(p.drift_bytes)} ({formatPercent(p.drift_ratio)})
                        </span>
                      </td>
                      <td>
                        <div>{formatDateISO(p.expire_at || p.expire)}</div>
                        <div className="status-muted">
                          {lang === "zh-CN" ? `每月 ${p.reset_day || 1} 日` : `Day ${p.reset_day || 1}`}
                        </div>
                      </td>
                      <td>
                        <div className="btn-row">
                          <button
                            type="button"
                            className="btn btn-secondary btn-sm"
                            onClick={() => openEditProvider(p)}
                          >
                            {lang === "zh-CN" ? "编辑" : "Edit"}
                          </button>
                          <button
                            type="button"
                            className="btn btn-danger btn-sm"
                            onClick={() => removeProvider(p.id)}
                          >
                            {lang === "zh-CN" ? "删除" : "Delete"}
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Legacy Billing */}
      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.legacyBillingTitle}</h3>
            <p className="card-sub">{t.legacyBillingSub}</p>
          </div>
        </div>
        <form className="device-actions" onSubmit={saveBilling}>
          <div className="field">
            <label htmlFor="billing-reset-day">{lang === "zh-CN" ? "每月结算日 (1-28)" : "Reset Day (1-28)"}</label>
            <input
              id="billing-reset-day"
              type="number"
              min={1}
              max={28}
              value={resetDay}
              onChange={(e) => setResetDay(e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="billing-provider-gb">{lang === "zh-CN" ? "全局对账基准 (GB)" : "Provider Baseline (GB)"}</label>
            <input
              id="billing-provider-gb"
              type="number"
              step="any"
              min={0}
              value={providerGB}
              onChange={(e) => setProviderGB(e.target.value)}
            />
          </div>
          <button type="submit" className="btn btn-secondary btn-sm">
            {t.apply}
          </button>
        </form>
        {proxyData?.billing && (
          <p className="note">
            {lang === "zh-CN" ? "本地估算" : "Local"}: {formatBytes(proxyData.billing.local_bytes)} ·{" "}
            {lang === "zh-CN" ? "偏差" : "Delta"}: {formatSignedBytes(proxyData.billing.delta)}
          </p>
        )}
      </div>

      {/* Rule Modal */}
      <Modal
        open={ruleModalOpen}
        title={editingRule ? t.editRule : t.addRule}
        onClose={() => setRuleModalOpen(false)}
      >
        <form onSubmit={saveRule} className="modal-form-stack">
          <div className="field">
            <label>{lang === "zh-CN" ? "规则名称" : "Rule Name"}</label>
            <input
              required
              value={ruleForm.name}
              onChange={(e) => setRuleForm((f) => ({ ...f, name: e.target.value }))}
              placeholder="HK 2x"
            />
          </div>
          <div className="field-row">
            <div className="field">
              <label>{lang === "zh-CN" ? "匹配类型" : "Match Type"}</label>
              <select
                value={ruleForm.match_type}
                onChange={(e) =>
                  setRuleForm((f) => ({ ...f, match_type: e.target.value as RuleMatchType }))
                }
              >
                <option value="keyword">keyword</option>
                <option value="regex">regex</option>
                <option value="exact">exact</option>
                <option value="auto_extract">auto_extract</option>
              </select>
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "优先级" : "Priority"}</label>
              <input
                type="number"
                required
                value={ruleForm.priority}
                onChange={(e) => setRuleForm((f) => ({ ...f, priority: Number(e.target.value) }))}
              />
            </div>
          </div>
          <div className="field-row">
            <div className="field">
              <label>
                {lang === "zh-CN" ? "匹配模式" : "Pattern"}
                {ruleForm.match_type === "auto_extract" && (lang === "zh-CN" ? "（可留空使用内置提取）" : " (optional)")}
              </label>
              <input
                required={ruleForm.match_type !== "auto_extract"}
                value={ruleForm.pattern}
                onChange={(e) => setRuleForm((f) => ({ ...f, pattern: e.target.value }))}
                placeholder={ruleForm.match_type === "auto_extract" ? "" : "HK|香港"}
              />
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "倍率" : "Factor"}</label>
              <input
                type="number"
                step="any"
                min="0"
                required={ruleForm.match_type !== "auto_extract"}
                disabled={ruleForm.match_type === "auto_extract"}
                value={ruleFactorInput}
                onChange={(e) => setRuleFactorInput(e.target.value)}
              />
            </div>
          </div>
          <label className="check-field">
            <input
              type="checkbox"
              checked={ruleForm.enabled ?? true}
              onChange={(e) => setRuleForm((f) => ({ ...f, enabled: e.target.checked }))}
            />
            {lang === "zh-CN" ? "启用此规则" : "Enable rule"}
          </label>
          <div className="modal-footer">
            <button type="submit" className="btn btn-primary btn-sm" disabled={ruleSubmitting}>
              {lang === "zh-CN" ? "保存" : "Save"}
            </button>
          </div>
        </form>
      </Modal>

      {/* Import JSON Modal */}
      <Modal
        open={importOpen}
        title={t.importJson}
        onClose={() => setImportOpen(false)}
      >
        <form onSubmit={handleImportRules} className="modal-form-stack">
          <div className="field">
            <label>{lang === "zh-CN" ? "导入模式" : "Import Mode"}</label>
            <select value={importMode} onChange={(e) => setImportMode(e.target.value as "replace" | "merge")}>
              <option value="merge">{lang === "zh-CN" ? "合并 (merge)" : "Merge"}</option>
              <option value="replace">{lang === "zh-CN" ? "替换全部 (replace)" : "Replace All"}</option>
            </select>
          </div>
          <div className="field">
            <label>{lang === "zh-CN" ? "规则 JSON" : "Rules JSON"}</label>
            <textarea
              rows={7}
              required
              value={importText}
              onChange={(e) => setImportText(e.target.value)}
              placeholder='{"rules":[{"name":"HK 2x","match_type":"keyword","pattern":"HK","factor":2,"priority":100,"enabled":true}]}'
            />
          </div>
          <div className="modal-footer">
            <button type="submit" className="btn btn-primary btn-sm" disabled={importSubmitting}>
              {t.importJson}
            </button>
          </div>
        </form>
      </Modal>

      {/* Provider Modal */}
      <Modal
        open={providerModalOpen}
        title={editingProvider ? t.editProvider : t.addProvider}
        onClose={() => setProviderModalOpen(false)}
      >
        <form onSubmit={saveProvider} className="modal-form-stack">
          <div className="field">
            <label>{lang === "zh-CN" ? "订阅名称" : "Provider Name"}</label>
            <input
              required
              value={providerForm.name}
              onChange={(e) => setProviderForm((f) => ({ ...f, name: e.target.value }))}
            />
          </div>
          <div className="field-row">
            <div className="field">
              <label>{lang === "zh-CN" ? "配额单位" : "Unit"}</label>
              <select
                value={providerForm.unit}
                onChange={(e) =>
                  setProviderForm((f) => ({ ...f, unit: e.target.value as "MB" | "GB" | "TB" }))
                }
              >
                <option value="MB">MB</option>
                <option value="GB">GB</option>
                <option value="TB">TB</option>
              </select>
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "总配额" : "Total Quota"}</label>
              <input
                type="number"
                step="any"
                min="0"
                required
                value={providerForm.total_value}
                onChange={(e) => setProviderForm((f) => ({ ...f, total_value: e.target.value }))}
              />
            </div>
          </div>
          <div className="field-row">
            <div className="field">
              <label>{lang === "zh-CN" ? "已用流量" : "Used"}</label>
              <input
                type="number"
                step="any"
                min="0"
                value={providerForm.used_value}
                onChange={(e) => setProviderForm((f) => ({ ...f, used_value: e.target.value }))}
              />
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "每月重置日 (1-28)" : "Reset Day (1-28)"}</label>
              <input
                type="number"
                min={1}
                max={28}
                value={providerForm.reset_day}
                onChange={(e) => setProviderForm((f) => ({ ...f, reset_day: e.target.value }))}
              />
            </div>
          </div>
          <div className="field-row">
            <div className="field">
              <label>{lang === "zh-CN" ? "出口正则 (可选)" : "Outbound Regex (opt)"}</label>
              <input
                value={providerForm.outbound_pattern}
                onChange={(e) =>
                  setProviderForm((f) => ({ ...f, outbound_pattern: e.target.value }))
                }
              />
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "到期日期 (可选)" : "Expire Date (opt)"}</label>
              <input
                type="date"
                value={providerForm.expire_date}
                onChange={(e) => setProviderForm((f) => ({ ...f, expire_date: e.target.value }))}
              />
            </div>
          </div>
          <div className="field">
            <label>{lang === "zh-CN" ? "关联节点 (逗号分隔)" : "Linked Nodes (comma-separated)"}</label>
            <input
              value={providerForm.linked_nodes}
              onChange={(e) => setProviderForm((f) => ({ ...f, linked_nodes: e.target.value }))}
              placeholder="gateway-1, proxy-2"
            />
          </div>
          <label className="check-field">
            <input
              type="checkbox"
              checked={providerForm.enabled}
              onChange={(e) => setProviderForm((f) => ({ ...f, enabled: e.target.checked }))}
            />
            {lang === "zh-CN" ? "启用订阅对账" : "Enable provider"}
          </label>
          <div className="modal-footer">
            <button type="submit" className="btn btn-primary btn-sm" disabled={providerSubmitting}>
              {lang === "zh-CN" ? "保存" : "Save"}
            </button>
          </div>
        </form>
      </Modal>
    </section>
  );
}
