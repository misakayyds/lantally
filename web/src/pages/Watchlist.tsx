import React, { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type {
  WatchDimension,
  WatchInput,
  WatchItemStatus,
  WatchMatcherMap,
  WatchMatchType,
} from "../api/types";
import { EmptyState } from "../components/EmptyState";
import { Modal } from "../components/Modal";
import { WatchTrendChart } from "../components/WatchTrendChart";
import { useApp } from "../state/AppContext";
import { formatBytes, formatPercent } from "../utils/format";

const ALL_DIMS: WatchDimension[] = [
  "node",
  "iface",
  "outbound",
  "group",
  "process",
  "domain",
  "device",
];

interface MatcherRow {
  dim: WatchDimension;
  type: WatchMatchType;
  valuesText: string;
}

export default function Watchlist() {
  const { lang, t, timeRange, refreshTick, showToast } = useApp();
  const [items, setItems] = useState<WatchItemStatus[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [selectedDetail, setSelectedDetail] = useState<WatchItemStatus | null>(null);
  const [detailTick, setDetailTick] = useState(0);
  const [error, setError] = useState<string | null>(null);

  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<WatchItemStatus | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [name, setName] = useState("");
  const [matcherRows, setMatcherRows] = useState<MatcherRow[]>([
    { dim: "process", type: "keyword", valuesText: "" },
  ]);
  const [overrideFactor, setOverrideFactor] = useState("");
  const [quotaUnit, setQuotaUnit] = useState<"MB" | "GB" | "TB">("GB");
  const [quotaValue, setQuotaValue] = useState("0");
  const [resetType, setResetType] = useState<"none" | "monthly" | "fixed">("none");
  const [resetDay, setResetDay] = useState("1");
  const [expiresAt, setExpiresAt] = useState("");
  const [pinned, setPinned] = useState(false);
  const [enabled, setEnabled] = useState(true);

  const load = useCallback(async () => {
    try {
      const res = await api.listWatch(timeRange);
      const list = res.items || [];
      setItems(list);
      if (list.length === 0) {
        setSelectedId(null);
        setSelectedDetail(null);
      } else {
        setSelectedId((cur) => (cur !== null && list.some((i) => i.id === cur) ? cur : list[0].id));
      }
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [timeRange]);

  useEffect(() => {
    void load();
  }, [load, refreshTick]);

  useEffect(() => {
    if (selectedId === null) {
      setSelectedDetail(null);
      return;
    }
    let cancelled = false;
    api
      .getWatch(selectedId, timeRange)
      .then((res) => {
        if (!cancelled) setSelectedDetail(res);
      })
      .catch(() => {
        if (!cancelled) setSelectedDetail(null);
      });
    return () => {
      cancelled = true;
    };
  }, [selectedId, timeRange, refreshTick, detailTick]);

  const openCreate = () => {
    setEditing(null);
    setName("");
    setMatcherRows([{ dim: "process", type: "keyword", valuesText: "" }]);
    setOverrideFactor("");
    setQuotaUnit("GB");
    setQuotaValue("0");
    setResetType("none");
    setResetDay("1");
    setExpiresAt("");
    setPinned(false);
    setEnabled(true);
    setModalOpen(true);
  };

  const openEdit = (item: WatchItemStatus) => {
    setEditing(item);
    setName(item.name);
    const rows: MatcherRow[] = [];
    for (const [k, v] of Object.entries(item.matchers || {})) {
      if (v) {
        rows.push({
          dim: k as WatchDimension,
          type: v.type,
          valuesText: (v.values || []).join(", "),
        });
      }
    }
    setMatcherRows(rows.length > 0 ? rows : [{ dim: "process", type: "keyword", valuesText: "" }]);
    setOverrideFactor(item.override_factor ? String(item.override_factor) : "");
    setQuotaUnit(item.quota_unit || "GB");
    setQuotaValue(String(item.quota_value || 0));
    setResetType(item.reset_type === "fixed_date" ? "fixed" : item.reset_type || "none");
    setResetDay(String(item.reset_day || 1));
    setExpiresAt(item.expires_at ? item.expires_at.slice(0, 10) : "");
    setPinned(Boolean(item.pinned));
    setEnabled(Boolean(item.enabled));
    setModalOpen(true);
  };

  const buildInputFromItem = (item: WatchItemStatus, overrides: Partial<WatchInput> = {}): WatchInput => ({
    name: item.name,
    matchers: item.matchers,
    override_factor: item.override_factor ?? null,
    quota_unit: item.quota_unit || "GB",
    quota_value: item.quota_value || 0,
    reset_type: item.reset_type === "fixed_date" ? "fixed" : item.reset_type || "none",
    reset_day: item.reset_day || 1,
    expires_at: item.expires_at || undefined,
    pinned: item.pinned,
    enabled: item.enabled,
    ...overrides,
  });

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (submitting) return;
    const matchers: WatchMatcherMap = {};
    for (const row of matcherRows) {
      const vals = row.valuesText
        .split(/[,\n]/)
        .map((s) => s.trim())
        .filter(Boolean);
      if (vals.length > 0) {
        const existing = matchers[row.dim];
        if (existing) {
          const merged = Array.from(new Set([...existing.values, ...vals]));
          matchers[row.dim] = { type: existing.type, values: merged };
        } else {
          matchers[row.dim] = { type: row.type, values: vals };
        }
      }
    }
    if (Object.keys(matchers).length === 0) {
      showToast(lang === "zh-CN" ? "请至少填写一个匹配维度与值" : "Add at least one dimension matcher");
      return;
    }
    const factorNum = overrideFactor.trim() ? Number(overrideFactor) : null;
    if (factorNum !== null && (!Number.isFinite(factorNum) || factorNum <= 0)) {
      showToast(lang === "zh-CN" ? "覆盖倍率需大于 0" : "Override factor must be > 0");
      return;
    }
    const payload: WatchInput = {
      name: name.trim(),
      matchers,
      override_factor: factorNum,
      quota_unit: quotaUnit,
      quota_value: Number(quotaValue) || 0,
      reset_type: resetType,
      reset_day: resetType === "monthly" ? Math.min(28, Math.max(1, Number(resetDay) || 1)) : 1,
      expires_at: expiresAt || undefined,
      pinned,
      enabled,
    };
    setSubmitting(true);
    try {
      if (editing) {
        await api.updateWatch(editing.id, payload);
      } else {
        await api.createWatch(payload);
      }
      setModalOpen(false);
      showToast(t.saved);
      await load();
      setDetailTick((n) => n + 1);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    } finally {
      setSubmitting(false);
    }
  };

  const togglePin = async (item: WatchItemStatus) => {
    try {
      await api.updateWatch(item.id, buildInputFromItem(item, { pinned: !item.pinned }));
      showToast(t.saved);
      await load();
      setDetailTick((n) => n + 1);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  const removeItem = async (id: number) => {
    try {
      await api.deleteWatch(id);
      showToast(t.deleted);
      await load();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <section id="watchlist" className="page-stack">
      {error && <div className="banner banner-error">{error}</div>}

      <div className="card">
        <div className="card-header">
          <div>
            <h3>{t.watchTitle}</h3>
            <p className="card-sub">{t.watchSub}</p>
          </div>
          <button type="button" className="btn btn-primary btn-sm" onClick={openCreate}>
            {t.addWatch}
          </button>
        </div>

        {items.length === 0 ? (
          <EmptyState
            title={lang === "zh-CN" ? "暂无关注项" : "No watch items"}
            description={
              lang === "zh-CN"
                ? "创建一个自定义关注项以跟踪特定应用、域名、网卡或出口节点的流量配额与趋势"
                : "Create a custom watch item to track traffic quotas and trends across dimensions"
            }
          />
        ) : (
          <div className="watch-list">
            {items.map((item) => {
              const pct = Math.min(100, Math.max(0, (item.used_ratio || 0) * 100));
              const isSelected = selectedId === item.id;
              return (
                <div
                  key={item.id}
                  className={`watch-card${isSelected ? " selected" : ""}`}
                  onClick={() => setSelectedId(item.id)}
                >
                  <div className="watch-card-top">
                    <div>
                      <div className="watch-title-row">
                        <strong>{item.name}</strong>
                        {item.pinned && (
                          <span className="badge badge-info">
                            {lang === "zh-CN" ? "已置顶" : "Pinned"}
                          </span>
                        )}
                        {item.override_factor ? (
                          <span className="badge">{item.override_factor}x</span>
                        ) : null}
                        {item.over_quota && (
                          <span className="badge badge-danger">{lang === "zh-CN" ? "超限" : "Over"}</span>
                        )}
                        {item.expired && (
                          <span className="badge badge-warn">{lang === "zh-CN" ? "过期" : "Expired"}</span>
                        )}
                        {item.overlap_possible && (
                          <span className="badge badge-warn">
                            {lang === "zh-CN" ? "维度可能重叠" : "Overlap possible"}
                          </span>
                        )}
                      </div>
                      <div className="matcher-tags">
                        {Object.entries(item.matchers || {}).map(([dim, m]) =>
                          m ? (
                            <span key={dim} className="chip">
                              {dim} ({m.type}): {m.values.join(", ")}
                            </span>
                          ) : null,
                        )}
                      </div>
                    </div>
                    <div className="btn-row" onClick={(e) => e.stopPropagation()}>
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        onClick={() => togglePin(item)}
                      >
                        {item.pinned
                          ? lang === "zh-CN"
                            ? "取消置顶"
                            : "Unpin"
                          : lang === "zh-CN"
                            ? "置顶概览"
                            : "Pin"}
                      </button>
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        onClick={() => openEdit(item)}
                      >
                        {lang === "zh-CN" ? "编辑" : "Edit"}
                      </button>
                      <button
                        type="button"
                        className="btn btn-danger btn-sm"
                        onClick={() => removeItem(item.id)}
                      >
                        {lang === "zh-CN" ? "删除" : "Delete"}
                      </button>
                    </div>
                  </div>

                  <div className="watch-stats-row">
                    <span>
                      {t.proxyAdjusted}: <strong className="mono">{formatBytes(item.adjusted)}</strong>
                    </span>
                    <span>
                      {lang === "zh-CN" ? "原始" : "Raw"}: <strong className="mono">{formatBytes(item.total)}</strong>
                    </span>
                    <span>
                      RX: <strong className="mono">{formatBytes(item.rx)}</strong>
                    </span>
                    <span>
                      TX: <strong className="mono">{formatBytes(item.tx)}</strong>
                    </span>
                    {item.days_remaining !== null && item.days_remaining !== undefined && (
                      <span>
                        {lang === "zh-CN" ? `剩余 ${item.days_remaining} 天` : `${item.days_remaining}d left`}
                      </span>
                    )}
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
        )}
      </div>

      {selectedDetail && (
        <div className="card" style={{ padding: "1rem 1.15rem" }}>
          <WatchTrendChart
            title={
              lang === "zh-CN"
                ? `${selectedDetail.name} · 回溯趋势`
                : `${selectedDetail.name} · Trend`
            }
            points={selectedDetail.series || selectedDetail.buckets || []}
          />
        </div>
      )}

      <Modal
        open={modalOpen}
        wide
        title={editing ? t.editWatch : t.addWatch}
        onClose={() => setModalOpen(false)}
      >
        <form onSubmit={handleSave} className="modal-form-stack">
          <div className="field-row">
            <div className="field">
              <label>{lang === "zh-CN" ? "关注项名称" : "Name"}</label>
              <input
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={lang === "zh-CN" ? "例如：YouTube / 机场流量" : "e.g. Streaming"}
              />
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "覆盖倍率 (留空沿用全局)" : "Override Factor (opt)"}</label>
              <input
                type="number"
                step="any"
                min="0.01"
                value={overrideFactor}
                onChange={(e) => setOverrideFactor(e.target.value)}
                placeholder="1.0"
              />
            </div>
          </div>

          <div className="field">
            <label>{lang === "zh-CN" ? "匹配维度 (多维度取交集 AND)" : "Matchers (AND across dimensions)"}</label>
            <div className="matcher-builder">
              {matcherRows.map((row, idx) => (
                <div key={idx} className="matcher-row">
                  <select
                    value={row.dim}
                    onChange={(e) =>
                      setMatcherRows((prev) =>
                        prev.map((r, i) =>
                          i === idx ? { ...r, dim: e.target.value as WatchDimension } : r,
                        ),
                      )
                    }
                  >
                    {ALL_DIMS.map((d) => (
                      <option
                        key={d}
                        value={d}
                        disabled={
                          d !== row.dim && matcherRows.some((other, oIdx) => oIdx !== idx && other.dim === d)
                        }
                      >
                        {d}
                      </option>
                    ))}
                  </select>
                  <select
                    value={row.type}
                    onChange={(e) =>
                      setMatcherRows((prev) =>
                        prev.map((r, i) =>
                          i === idx ? { ...r, type: e.target.value as WatchMatchType } : r,
                        ),
                      )
                    }
                  >
                    <option value="exact">exact</option>
                    <option value="keyword">keyword</option>
                    <option value="regex">regex</option>
                  </select>
                  <input
                    value={row.valuesText}
                    onChange={(e) =>
                      setMatcherRows((prev) =>
                        prev.map((r, i) => (i === idx ? { ...r, valuesText: e.target.value } : r)),
                      )
                    }
                    placeholder={lang === "zh-CN" ? "值（逗号分隔，OR）" : "Values (comma-separated)"}
                  />
                  {matcherRows.length > 1 && (
                    <button
                      type="button"
                      className="btn btn-secondary btn-sm"
                      onClick={() => setMatcherRows((prev) => prev.filter((_, i) => i !== idx))}
                    >
                      ×
                    </button>
                  )}
                </div>
              ))}
              <button
                type="button"
                className="btn btn-secondary btn-sm"
                disabled={matcherRows.length >= ALL_DIMS.length}
                onClick={() =>
                  setMatcherRows((prev) => {
                    const used = new Set(prev.map((r) => r.dim));
                    const nextDim = (["domain", ...ALL_DIMS] as WatchDimension[]).find(
                      (d) => !used.has(d),
                    );
                    if (!nextDim) return prev;
                    return [...prev, { dim: nextDim, type: "keyword", valuesText: "" }];
                  })
                }
              >
                + {lang === "zh-CN" ? "添加维度" : "Add Dimension"}
              </button>
            </div>
          </div>

          <div className="field-row">
            <div className="field">
              <label>{lang === "zh-CN" ? "配额数值 (0 表示不限)" : "Quota Value (0 = unlimited)"}</label>
              <input
                type="number"
                step="any"
                min="0"
                value={quotaValue}
                onChange={(e) => setQuotaValue(e.target.value)}
              />
            </div>
            <div className="field">
              <label>{lang === "zh-CN" ? "配额单位" : "Quota Unit"}</label>
              <select
                value={quotaUnit}
                onChange={(e) => setQuotaUnit(e.target.value as "MB" | "GB" | "TB")}
              >
                <option value="MB">MB</option>
                <option value="GB">GB</option>
                <option value="TB">TB</option>
              </select>
            </div>
          </div>

          <div className="field-row">
            <div className="field">
              <label>{lang === "zh-CN" ? "周期类型" : "Reset Type"}</label>
              <select
                value={resetType}
                onChange={(e) => setResetType(e.target.value as "none" | "monthly" | "fixed")}
              >
                <option value="none">{lang === "zh-CN" ? "无周期 (none)" : "None"}</option>
                <option value="monthly">{lang === "zh-CN" ? "按月重置 (monthly)" : "Monthly"}</option>
                <option value="fixed">{lang === "zh-CN" ? "固定到期 (fixed)" : "Fixed Date"}</option>
              </select>
            </div>
            {resetType === "monthly" ? (
              <div className="field">
                <label>{lang === "zh-CN" ? "重置日 (1-28)" : "Reset Day (1-28)"}</label>
                <input
                  type="number"
                  min={1}
                  max={28}
                  value={resetDay}
                  onChange={(e) => setResetDay(e.target.value)}
                />
              </div>
            ) : (
              <div className="field">
                <label>{lang === "zh-CN" ? "到期日 (可选)" : "Expires At (opt)"}</label>
                <input
                  type="date"
                  value={expiresAt}
                  onChange={(e) => setExpiresAt(e.target.value)}
                />
              </div>
            )}
          </div>

          <div className="check-inline-row">
            <label className="check-field">
              <input
                type="checkbox"
                checked={pinned}
                onChange={(e) => setPinned(e.target.checked)}
              />
              {lang === "zh-CN" ? "置顶到概览页" : "Pin to Overview"}
            </label>
            <label className="check-field">
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
              />
              {lang === "zh-CN" ? "启用" : "Enabled"}
            </label>
          </div>

          <div className="modal-footer">
            <button type="submit" className="btn btn-primary btn-sm" disabled={submitting}>
              {lang === "zh-CN" ? "保存" : "Save"}
            </button>
          </div>
        </form>
      </Modal>
    </section>
  );
}
