import { useState } from "react";
import type { MetricDirection, WatchPoint } from "../api/types";
import { useApp } from "../state/AppContext";
import { axisUnit, formatChartBytes, formatStamp, niceMax } from "../utils/format";

interface WatchTrendChartProps {
  title: string;
  points: WatchPoint[];
}

export function WatchTrendChart({ title, points }: WatchTrendChartProps) {
  const { lang, t } = useApp();
  const [dir, setDir] = useState<MetricDirection>("total");
  const [useAdjusted, setUseAdjusted] = useState(true);
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);

  const valFor = (pt: WatchPoint): number => {
    if (useAdjusted) {
      return dir === "rx" ? pt.adjusted_rx : dir === "tx" ? pt.adjusted_tx : pt.adjusted;
    }
    return dir === "rx" ? pt.rx : dir === "tx" ? pt.tx : pt.total;
  };

  const width = 960;
  const height = 220;
  const pad = { top: 14, right: 10, bottom: 30, left: 56 };
  const plotW = width - pad.left - pad.right;
  const plotH = height - pad.top - pad.bottom;

  let maxBytes = 0;
  for (const pt of points) {
    const v = valFor(pt);
    if (v > maxBytes) maxBytes = v;
  }
  const unit = axisUnit(maxBytes, lang);
  const yMax = niceMax(maxBytes / unit.div);
  const n = Math.max(points.length, 1);
  const slot = plotW / n;
  const barW = Math.max(2, slot * 0.68);
  const ticks = 4;

  const hovered = hoverIdx !== null ? points[hoverIdx] : undefined;

  return (
    <div className="watch-trend-box">
      <div className="chart-head-row" style={{ marginBottom: "0.45rem" }}>
        <strong style={{ fontSize: "0.88rem" }}>{title}</strong>
        <div className="chart-head-actions">
          <div className="chip-group compact">
            <button
              type="button"
              className={`chip-btn${useAdjusted ? " active" : ""}`}
              onClick={() => setUseAdjusted(true)}
            >
              {lang === "zh-CN" ? "倍率后" : "Adjusted"}
            </button>
            <button
              type="button"
              className={`chip-btn${!useAdjusted ? " active" : ""}`}
              onClick={() => setUseAdjusted(false)}
            >
              {lang === "zh-CN" ? "原始" : "Raw"}
            </button>
          </div>
          <div className="chip-group compact">
            {(["total", "rx", "tx"] as MetricDirection[]).map((d) => (
              <button
                key={d}
                type="button"
                className={`chip-btn${dir === d ? " active" : ""}`}
                onClick={() => setDir(d)}
              >
                {d === "total" ? t.metricTotal : d === "rx" ? t.metricRx : t.metricTx}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="chart-plot">
        <svg viewBox={`0 0 ${width} ${height}`}>
          {Array.from({ length: ticks + 1 }).map((_, i) => {
            const val = (yMax / ticks) * (ticks - i);
            const y = pad.top + (plotH / ticks) * i;
            return (
              <g key={i}>
                <line x1={pad.left} x2={width - pad.right} y1={y} y2={y} stroke="#eef1f4" />
                <text x={pad.left - 8} y={y + 4} textAnchor="end" fill="#9aa3af" fontSize="10">
                  {val >= 100 ? val.toFixed(0) : val.toFixed(1)}
                </text>
              </g>
            );
          })}
          {points.map((pt, i) => {
            const v = valFor(pt);
            const h = v > 0 ? (v / unit.div / yMax) * plotH : 0;
            const x = pad.left + slot * i + (slot - barW) / 2;
            const y = pad.top + plotH - h;
            return (
              <g key={pt.t + i}>
                <rect x={x} y={y} width={barW} height={Math.max(h, 0)} fill="#1e40af" opacity={0.85} rx={1.5} />
                <rect
                  x={pad.left + slot * i}
                  y={pad.top}
                  width={slot}
                  height={plotH}
                  fill="transparent"
                  onPointerEnter={() => setHoverIdx(i)}
                  onPointerLeave={() => setHoverIdx(null)}
                />
              </g>
            );
          })}
        </svg>
        {maxBytes === 0 && <div className="chart-empty">{t.emptyTraffic}</div>}
      </div>
      {hovered && (
        <div className="watch-hover-line">
          <span>{formatStamp(new Date(hovered.t))}</span>
          <span>
            {t.metricTotal}: {formatChartBytes(valFor(hovered))} (RX {formatChartBytes(useAdjusted ? hovered.adjusted_rx : hovered.rx)} / TX{" "}
            {formatChartBytes(useAdjusted ? hovered.adjusted_tx : hovered.tx)})
          </span>
        </div>
      )}
    </div>
  );
}
