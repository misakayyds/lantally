import React, { useMemo, useRef, useState } from "react";
import type { MetricDirection, TrafficPoint, TrafficSeries } from "../api/types";
import { useApp } from "../state/AppContext";
import { axisUnit, colorFor, formatChartBytes, formatStamp, niceMax } from "../utils/format";

interface StackedTrafficChartProps {
  title: string;
  series: TrafficSeries | null | undefined;
  emptyHint?: string;
  direction?: MetricDirection;
  onDirectionChange?: (dir: MetricDirection) => void;
  rightControls?: React.ReactNode;
}

function pointMapForDir(pt: TrafficPoint, dir: MetricDirection): Record<string, number> {
  if (dir === "rx" && pt.rx_values) return pt.rx_values;
  if (dir === "tx" && pt.tx_values) return pt.tx_values;
  return pt.values || {};
}

function totalsForDir(series: TrafficSeries, dir: MetricDirection): Record<string, number> {
  if (dir === "rx" && series.rx_totals) return series.rx_totals;
  if (dir === "tx" && series.tx_totals) return series.tx_totals;
  return series.totals || {};
}

export function StackedTrafficChart({
  title,
  series,
  emptyHint,
  direction = "total",
  onDirectionChange,
  rightControls,
}: StackedTrafficChartProps) {
  const { lang, t } = useApp();
  const [legendPage, setLegendPage] = useState(0);
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);
  const [tipPos, setTipPos] = useState<{ left: number; top: number }>({ left: 12, top: 12 });
  const plotRef = useRef<HTMLDivElement | null>(null);

  const classLabels: Record<string, string> = useMemo(
    () => ({
      direct: t.direct,
      proxy_raw: t.proxyRaw,
      proxy_adjusted: t.proxyAdjusted,
      proxy_unadjusted: t.proxyUnadjusted,
      total: t.total,
    }),
    [t],
  );

  const labelFor = (key: string) => classLabels[key] || key;

  const points = series?.points || [];
  const totals = series ? totalsForDir(series, direction) : {};
  const keys = useMemo(() => {
    const rawKeys = [...(series?.keys || [])];
    return rawKeys.sort((a, b) => Number(totals[b] || 0) - Number(totals[a] || 0));
  }, [series?.keys, totals]);

  const bucketSec = Number(series?.bucket_seconds || 1800);
  const hasData = keys.some((k) => Number(totals[k] || 0) > 0);

  const width = 1080;
  const height = 310;
  const pad = { top: 18, right: 10, bottom: 36, left: 60 };
  const plotW = width - pad.left - pad.right;
  const plotH = height - pad.top - pad.bottom;

  let maxBytes = 0;
  for (const pt of points) {
    const m = pointMapForDir(pt, direction);
    let sum = 0;
    for (const k of keys) sum += Number(m[k] || 0);
    if (sum > maxBytes) maxBytes = sum;
  }

  const unit = axisUnit(maxBytes, lang);
  const yMax = niceMax(maxBytes / unit.div);
  const ticks = 5;
  const n = Math.max(points.length, 1);
  const slot = plotW / n;
  const barW = Math.max(1.4, slot * 0.72);

  const pageSize = 4;
  const pageCount = Math.max(1, Math.ceil(keys.length / pageSize));
  const safePage = Math.max(0, Math.min(legendPage, pageCount - 1));
  const visibleKeys = keys.slice(safePage * pageSize, safePage * pageSize + pageSize);

  const handlePointer = (e: React.PointerEvent<SVGRectElement>, index: number) => {
    setHoverIdx(index);
    if (!plotRef.current) return;
    const bounds = plotRef.current.getBoundingClientRect();
    const left = Math.max(8, Math.min(e.clientX - bounds.left + 12, bounds.width - 245));
    const top = Math.max(8, e.clientY - bounds.top - 14);
    setTipPos({ left, top });
  };

  let lastDay = "";
  const hoveredPoint = hoverIdx !== null ? points[hoverIdx] : undefined;

  return (
    <div className="chart-panel">
      <div className="chart-head-row">
        <div className="chart-head">
          <span className="chart-mark" aria-hidden="true">
            <span />
            <span />
            <span />
          </span>
          <h3 className="chart-title">{title}</h3>
        </div>
        <div className="chart-head-actions">
          {onDirectionChange && (
            <div className="chip-group compact" role="group" aria-label="Direction">
              {(["total", "rx", "tx"] as MetricDirection[]).map((dir) => (
                <button
                  key={dir}
                  type="button"
                  className={`chip-btn${direction === dir ? " active" : ""}`}
                  onClick={() => onDirectionChange(dir)}
                >
                  {dir === "total" ? t.metricTotal : dir === "rx" ? t.metricRx : t.metricTx}
                </button>
              ))}
            </div>
          )}
          {rightControls}
        </div>
      </div>

      <div className="chart-plot" ref={plotRef}>
        <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label={title}>
          <text
            x={15}
            y={pad.top + plotH / 2}
            fill="#9aa3af"
            fontSize="11"
            textAnchor="middle"
            transform={`rotate(-90 15 ${pad.top + plotH / 2})`}
          >
            {unit.label}
          </text>

          {Array.from({ length: ticks + 1 }).map((_, i) => {
            const value = (yMax / ticks) * (ticks - i);
            const y = pad.top + (plotH / ticks) * i;
            return (
              <g key={i}>
                <line x1={pad.left} x2={width - pad.right} y1={y} y2={y} stroke="#eef1f4" />
                <text x={pad.left - 8} y={y + 4} textAnchor="end" fill="#9aa3af" fontSize="11">
                  {value >= 100 ? value.toFixed(0) : value.toFixed(2)}
                </text>
              </g>
            );
          })}

          {points.map((pt, i) => {
            const m = pointMapForDir(pt, direction);
            let yBase = pad.top + plotH;
            const x = pad.left + slot * i + (slot - barW) / 2;
            const tDate = new Date(pt.t);
            const day = `${tDate.getFullYear()}-${tDate.getMonth()}-${tDate.getDate()}`;
            let xLabel: React.ReactNode = null;
            if (!Number.isNaN(tDate.getTime())) {
              const cx = pad.left + slot * i + slot / 2;
              if (day !== lastDay) {
                lastDay = day;
                xLabel = (
                  <text x={cx} y={height - 8} textAnchor="middle" fill="#6b7280" fontSize="11">
                    {`${tDate.getMonth() + 1}/${tDate.getDate()}`}
                  </text>
                );
              } else if (tDate.getHours() === 12 && tDate.getMinutes() === 0) {
                xLabel = (
                  <text x={cx} y={height - 8} textAnchor="middle" fill="#9aa3af" fontSize="11">
                    12:00
                  </text>
                );
              }
            }

            return (
              <g key={pt.t + i}>
                {keys.map((key) => {
                  const bytes = Number(m[key] || 0);
                  if (bytes <= 0) return null;
                  const h = (bytes / unit.div / yMax) * plotH;
                  const y = yBase - h;
                  yBase = y;
                  return (
                    <rect
                      key={key}
                      x={x}
                      y={y}
                      width={barW}
                      height={Math.max(h, 0)}
                      fill={colorFor(key)}
                    />
                  );
                })}
                {xLabel}
                <rect
                  x={pad.left + slot * i}
                  y={pad.top}
                  width={slot}
                  height={plotH}
                  fill="transparent"
                  onPointerEnter={(e) => handlePointer(e, i)}
                  onPointerMove={(e) => handlePointer(e, i)}
                  onPointerLeave={() => setHoverIdx(null)}
                />
              </g>
            );
          })}
        </svg>

        {!hasData && <div className="chart-empty">{emptyHint || t.emptyTraffic}</div>}

        {hoveredPoint && (
          <div className="chart-tooltip" style={{ left: `${tipPos.left}px`, top: `${tipPos.top}px` }}>
            {(() => {
              const m = pointMapForDir(hoveredPoint, direction);
              const from = new Date(hoveredPoint.t);
              const to = new Date(from.getTime() + bucketSec * 1000);
              const nonZero = keys.filter((k) => Number(m[k] || 0) > 0);
              let sum = 0;
              for (const k of keys) sum += Number(m[k] || 0);
              return (
                <>
                  {nonZero.length === 0 ? (
                    <div className="chart-tip-row">
                      <span className="chart-tip-name">{t.noData}</span>
                    </div>
                  ) : (
                    nonZero.slice(0, 8).map((k) => (
                      <div className="chart-tip-row" key={k}>
                        <span className="chart-tip-dot" style={{ background: colorFor(k) }} />
                        <span className="chart-tip-name">{labelFor(k)}</span>
                        <span className="chart-tip-val">{formatChartBytes(m[k])}</span>
                      </div>
                    ))
                  )}
                  <div className="chart-tip-meta">
                    <span>From</span>
                    <strong>{formatStamp(from)}</strong>
                    <span>To</span>
                    <strong>{formatStamp(to)}</strong>
                    <span>Sum</span>
                    <strong>{formatChartBytes(sum)}</strong>
                  </div>
                </>
              );
            })()}
          </div>
        )}
      </div>

      <div className="chart-legend">
        <div className="chart-legend-items">
          {visibleKeys.length === 0 ? (
            <span className="chart-legend-item">{t.noSegments}</span>
          ) : (
            visibleKeys.map((key) => (
              <div className="chart-legend-item" key={key}>
                <span className="chart-swatch" style={{ background: colorFor(key) }} />
                <span>
                  {labelFor(key)} ({formatChartBytes(totals[key] || 0)})
                </span>
              </div>
            ))
          )}
        </div>
        {keys.length > pageSize && (
          <div className="chart-legend-nav">
            <button
              type="button"
              disabled={safePage === 0}
              onClick={() => setLegendPage((p) => Math.max(0, p - 1))}
              aria-label="Previous"
            >
              ‹
            </button>
            <span>
              {safePage + 1}/{pageCount}
            </span>
            <button
              type="button"
              disabled={safePage >= pageCount - 1}
              onClick={() => setLegendPage((p) => Math.min(pageCount - 1, p + 1))}
              aria-label="Next"
            >
              ›
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
