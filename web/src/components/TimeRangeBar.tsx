import { useEffect, useState } from "react";
import type { TimeRangePreset } from "../api/types";
import { useApp } from "../state/AppContext";

export function TimeRangeBar() {
  const { lang, t, timeRange, setTimeRange, showToast } = useApp();
  const [draftFrom, setDraftFrom] = useState(timeRange.from);
  const [draftTo, setDraftTo] = useState(timeRange.to);

  useEffect(() => {
    setDraftFrom(timeRange.from);
    setDraftTo(timeRange.to);
  }, [timeRange.preset, timeRange.from, timeRange.to]);

  const presets: { key: TimeRangePreset; label: string }[] = [
    { key: "today", label: t.rangeToday },
    { key: "24h", label: t.range24h },
    { key: "72h", label: t.range72h },
    { key: "7d", label: t.range7d },
    { key: "30d", label: t.range30d },
    { key: "billing", label: t.rangeBilling },
    { key: "custom", label: t.rangeCustom },
  ];

  const applyCustomRange = () => {
    if (!draftFrom || !draftTo || draftFrom > draftTo) {
      showToast(
        lang === "zh-CN"
          ? "请选择有效的起止日期（开始日期不能晚于结束日期）"
          : "Select valid dates (From must not be after To)",
      );
      return;
    }
    setTimeRange({ preset: "custom", from: draftFrom, to: draftTo });
  };

  const selectPreset = (preset: TimeRangePreset) => {
    if (preset === "custom") {
      applyCustomRange();
    } else {
      setTimeRange({ ...timeRange, preset });
    }
  };

  return (
    <div className="time-bar">
      <div className="chip-group" role="group" aria-label="Time range">
        {presets.map((p) => (
          <button
            key={p.key}
            type="button"
            className={`chip-btn${timeRange.preset === p.key ? " active" : ""}`}
            onClick={() => selectPreset(p.key)}
          >
            {p.label}
          </button>
        ))}
      </div>
      {timeRange.preset === "custom" && (
        <div className="custom-range-inputs">
          <input
            type="date"
            aria-label="From date"
            value={draftFrom}
            onChange={(e) => setDraftFrom(e.target.value)}
          />
          <span className="status-muted">~</span>
          <input
            type="date"
            aria-label="To date"
            value={draftTo}
            onChange={(e) => setDraftTo(e.target.value)}
          />
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={applyCustomRange}
          >
            {t.apply}
          </button>
        </div>
      )}
    </div>
  );
}
