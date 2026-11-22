export function formatBytes(value: number | undefined | null): string {
  const n = Number(value || 0);
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  if (n < 1024) return `${Math.round(n)} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let i = -1;
  let v = n;
  do {
    v /= 1024;
    i += 1;
  } while (v >= 1024 && i < units.length - 1);
  return `${v.toFixed(v >= 10 ? 0 : 1)} ${units[i]}`;
}

export function formatSignedBytes(value: number | undefined | null): string {
  const n = Number(value || 0);
  if (!Number.isFinite(n) || n === 0) return "0 B";
  const prefix = n > 0 ? "+" : "-";
  return `${prefix}${formatBytes(Math.abs(n))}`;
}

export function formatChartBytes(value: number | undefined | null): string {
  const n = Number(value || 0);
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  if (n < 1024) return `${Math.round(n)} B`;
  const units = ["kB", "MB", "GB", "TB"];
  let i = -1;
  let v = n;
  do {
    v /= 1024;
    i += 1;
  } while (v >= 1024 && i < units.length - 1);
  const digits = v >= 100 ? 0 : v >= 10 ? 1 : 2;
  return `${v.toFixed(digits)} ${units[i]}`;
}

export function formatPercent(ratio: number | undefined | null): string {
  const n = Number(ratio || 0) * 100;
  if (!Number.isFinite(n)) return "0.0%";
  return `${n.toFixed(1)}%`;
}

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

export function formatStamp(date: Date): string {
  if (Number.isNaN(date.getTime())) return "-";
  return `${date.getFullYear()}/${date.getMonth() + 1}/${pad2(date.getDate())} ${pad2(date.getHours())}:${pad2(date.getMinutes())}:${pad2(date.getSeconds())}`;
}

export function formatDateISO(unixSeconds: number | undefined | null): string {
  if (!unixSeconds || unixSeconds <= 0) return "-";
  const d = new Date(unixSeconds * 1000);
  if (Number.isNaN(d.getTime())) return "-";
  return `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())}`;
}

export function formatLastSeen(iso: string | undefined | null, lang: "zh-CN" | "en"): string {
  if (!iso) return lang === "zh-CN" ? "等待上报" : "Pending";
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return lang === "zh-CN" ? "等待上报" : "Pending";
  const sec = (Date.now() - t.getTime()) / 1000;
  if (sec < 45) return lang === "zh-CN" ? "刚刚" : "Just now";
  if (sec < 3600) {
    const m = Math.max(1, Math.floor(sec / 60));
    return lang === "zh-CN" ? `${m} 分钟前` : `${m}m ago`;
  }
  if (sec < 86400) {
    const h = Math.floor(sec / 3600);
    return lang === "zh-CN" ? `${h} 小时前` : `${h}h ago`;
  }
  return formatStamp(t);
}

export const CHART_PALETTE = [
  "#6db56d",
  "#e2c14c",
  "#5b9bd5",
  "#9b7eb8",
  "#e07a5f",
  "#4db6ac",
  "#f0a05a",
  "#7a9e7e",
  "#6c7ae0",
  "#c97b84",
  "#88b04b",
  "#4a90c8",
];

export function colorFor(key: string): string {
  let hash = 0;
  for (let i = 0; i < key.length; i += 1) {
    hash = (hash * 31 + key.charCodeAt(i)) >>> 0;
  }
  return CHART_PALETTE[hash % CHART_PALETTE.length];
}

export function niceMax(value: number): number {
  if (value <= 0) return 1;
  const exp = 10 ** Math.floor(Math.log10(value));
  const n = value / exp;
  const nice = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return nice * exp;
}

export function axisUnit(maxBytes: number, lang: "zh-CN" | "en"): { div: number; label: string } {
  const prefix = lang === "zh-CN" ? "流量/" : "Traffic/";
  if (maxBytes >= 1024 * 1024 * 1024) return { div: 1024 * 1024 * 1024, label: `${prefix}GB` };
  if (maxBytes >= 1024 * 1024) return { div: 1024 * 1024, label: `${prefix}MB` };
  if (maxBytes >= 1024) return { div: 1024, label: `${prefix}kB` };
  return { div: 1, label: `${prefix}B` };
}

export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
