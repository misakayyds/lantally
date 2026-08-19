const ROUTES = {
  overview: { title: "概览", eyebrow: "Dashboard" },
  nodes: { title: "节点", eyebrow: "Agents" },
  devices: { title: "设备", eyebrow: "Traffic" },
  proxy: { title: "代理", eyebrow: "Proxy" },
  alerts: { title: "告警", eyebrow: "Alerts" },
  settings: { title: "设置", eyebrow: "Settings" },
};

const EMPTY_ICON = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M8 12h8"/></svg>`;

const loginScreen = document.getElementById("login-screen");
const appShell = document.getElementById("app-shell");
const loginForm = document.getElementById("login-form");
const loginError = document.getElementById("login-error");
const logoutBtn = document.getElementById("logout-btn");
const refreshBtn = document.getElementById("refresh-btn");
const pageTitle = document.getElementById("page-title");
const pageEyebrow = document.getElementById("page-eyebrow");
const toast = document.getElementById("toast");

const enrollDialog = document.getElementById("enroll-dialog");
const enrollForm = document.getElementById("enroll-form");
const enrollError = document.getElementById("enroll-error");
const enrollResult = document.getElementById("enroll-result");
const enrollToken = document.getElementById("enroll-token");
const enrollSubmitBtn = document.getElementById("enroll-submit-btn");
const enrollCancelBtn = document.getElementById("enroll-cancel-btn");
const enrollOpenBtn = document.getElementById("enroll-open-btn");
const copyTokenBtn = document.getElementById("copy-token-btn");
const enrollLocal = document.getElementById("enroll-local");
const enrollCommand = document.getElementById("enroll-command");
const enrollWait = document.getElementById("enroll-wait");
const enrollHint = document.getElementById("enroll-hint");
const loginModeLabel = document.getElementById("login-mode-label");
const setupConfirm = document.getElementById("setup-confirm");
const loginSubmitBtn = document.getElementById("login-submit-btn");
const loginPassword2 = document.getElementById("login-password2");

const RANGE_HOURS = { "24h": 24, "72h": 72, "7d": 168, "30d": 720 };
const RANGE_TITLES = {
  "24h": "最近24小时流量使用情况",
  "72h": "最近72小时流量使用情况",
  "7d": "最近7天流量使用情况",
  "30d": "最近30天流量使用情况",
};

let currentRoute = "overview";
let currentDeviceId = "";
let uiState = { range: "72h", group: "node" };
let refreshTimer = null;
let writingHash = false;
let setupNeeded = false;
let claimWaitTimer = null;
let claimCommands = { linux: "", macos: "", windows: "" };
let claimPlatform = "linux";
let claimNodeId = "";

function showToast(message) {
  toast.textContent = message;
  toast.classList.remove("hidden");
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => toast.classList.add("hidden"), 2400);
}

async function api(path, options = {}) {
  return fetch(path, {
    credentials: "same-origin",
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...(options.headers || {}),
    },
  });
}

function setScreen(loggedIn) {
  loginScreen.classList.toggle("hidden", loggedIn);
  loginScreen.setAttribute("aria-hidden", loggedIn ? "true" : "false");
  appShell.classList.toggle("hidden", !loggedIn);
  appShell.setAttribute("aria-hidden", loggedIn ? "false" : "true");
}

function parseHash() {
  const raw = (location.hash || "#overview").replace(/^#/, "") || "overview";
  const [pathPart, queryPart] = raw.split("?");
  const parts = (pathPart || "overview").split("/").filter(Boolean);
  const params = new URLSearchParams(queryPart || "");
  const range = params.get("range");
  const group = params.get("group");
  if (RANGE_HOURS[range]) uiState.range = range;
  if (["node", "device", "class", "outbound"].includes(group)) uiState.group = group;
  return { route: parts[0] || "overview", id: parts[1] || "" };
}

function writeHash(route, id) {
  const params = new URLSearchParams();
  params.set("range", uiState.range);
  params.set("group", uiState.group);
  const path = id ? `${route}/${encodeURIComponent(id)}` : route;
  const next = `#${path}?${params.toString()}`;
  if (location.hash === next) return;
  writingHash = true;
  location.hash = next;
  writingHash = false;
}

function rangeWindow() {
  const hours = RANGE_HOURS[uiState.range] || 72;
  const to = new Date();
  const from = new Date(to.getTime() - hours * 3600 * 1000);
  return { from: from.toISOString(), to: to.toISOString() };
}

function trafficParams(extra = {}) {
  const { from, to } = rangeWindow();
  const params = new URLSearchParams({
    from,
    to,
    group: extra.group || uiState.group,
  });
  if (extra.class) params.set("class", extra.class);
  if (extra.node) params.set("node", extra.node);
  if (extra.device) params.set("device", extra.device);
  return params;
}

function syncToolbar() {
  const toolbar = document.getElementById("chart-toolbar");
  const groupChips = document.getElementById("group-chips");
  const showToolbar =
    ["overview", "nodes", "devices", "proxy"].includes(currentRoute) &&
    !((currentRoute === "devices" || currentRoute === "nodes") && currentDeviceId);
  toolbar.hidden = !showToolbar;
  document.querySelectorAll("[data-range]").forEach((btn) => {
    btn.classList.toggle("active", btn.dataset.range === uiState.range);
  });
  document.querySelectorAll("[data-group]").forEach((btn) => {
    btn.classList.toggle("active", btn.dataset.group === uiState.group);
  });
  groupChips.hidden = currentRoute === "devices" && Boolean(currentDeviceId);
}

function setRoute(route, id = "") {
  if (!ROUTES[route]) route = "overview";
  currentRoute = route;
  currentDeviceId = id;

  document.querySelectorAll(".nav-item").forEach((link) => {
    link.classList.toggle("active", link.dataset.route === route);
  });
  document.querySelectorAll(".view").forEach((view) => {
    view.classList.toggle("active", view.dataset.view === route);
  });

  pageTitle.textContent =
    route === "devices" && id ? "设备详情" : route === "nodes" && id ? "节点详情" : ROUTES[route].title;
  pageEyebrow.textContent = ROUTES[route].eyebrow;
  syncToolbar();
}

function formatBytes(value) {
  const n = Number(value || 0);
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let i = -1;
  let v = n;
  do {
    v /= 1024;
    i += 1;
  } while (v >= 1024 && i < units.length - 1);
  return `${v.toFixed(v >= 10 ? 0 : 1)} ${units[i]}`;
}

function formatLastSeen(iso) {
  if (!iso) return "等待上报";
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "等待上报";
  const sec = (Date.now() - t.getTime()) / 1000;
  if (sec < 45) return "刚刚";
  if (sec < 3600) return `${Math.max(1, Math.floor(sec / 60))} 分钟前`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`;
  return formatStamp(t);
}

const CHART_PALETTE = [
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

const CLASS_LABELS = {
  direct: "直连",
  proxy_raw: "代理原始",
  proxy_adjusted: "代理倍率后",
  proxy_unadjusted: "未调倍率",
  total: "总量",
};

function colorFor(key) {
  let hash = 0;
  for (let i = 0; i < key.length; i += 1) {
    hash = (hash * 31 + key.charCodeAt(i)) >>> 0;
  }
  return CHART_PALETTE[hash % CHART_PALETTE.length];
}

function seriesLabel(key) {
  return CLASS_LABELS[key] || key;
}

function formatChartBytes(value) {
  const n = Number(value || 0);
  if (n < 1024) return `${n} B`;
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

function niceMax(value) {
  if (value <= 0) return 1;
  const exp = 10 ** Math.floor(Math.log10(value));
  const n = value / exp;
  const nice = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return nice * exp;
}

function pad2(n) {
  return String(n).padStart(2, "0");
}

function formatStamp(date) {
  return `${date.getFullYear()}/${date.getMonth() + 1}/${pad2(date.getDate())} ${pad2(date.getHours())}:${pad2(date.getMinutes())}:${pad2(date.getSeconds())}`;
}

function stackedMax(points, keys) {
  let max = 0;
  for (const point of points) {
    let sum = 0;
    for (const key of keys) sum += Number((point.values || {})[key] || 0);
    if (sum > max) max = sum;
  }
  return max;
}

function axisUnit(maxBytes) {
  if (maxBytes >= 1024 * 1024 * 1024) return { div: 1024 * 1024 * 1024, label: "流量/GB" };
  if (maxBytes >= 1024 * 1024) return { div: 1024 * 1024, label: "流量/MB" };
  if (maxBytes >= 1024) return { div: 1024, label: "流量/kB" };
  return { div: 1, label: "流量/B" };
}

function renderTrafficChart(el, series, emptyHint) {
  if (!el) return;
  const title = RANGE_TITLES[uiState.range] || RANGE_TITLES["72h"];
  const points = series?.points || [];
  const keys = [...(series?.keys || [])].sort((a, b) => {
    return Number((series.totals || {})[b] || 0) - Number((series.totals || {})[a] || 0);
  });
  const totals = series?.totals || {};
  const bucketSec = Number(series?.bucket_seconds || 1800);
  const hasData = keys.some((key) => Number(totals[key] || 0) > 0);
  const pageSize = 4;
  let legendPage = 0;

  const width = 1080;
  const height = 320;
  const pad = { top: 18, right: 10, bottom: 36, left: 58 };
  const plotW = width - pad.left - pad.right;
  const plotH = height - pad.top - pad.bottom;
  const maxBytes = stackedMax(points, keys);
  const unit = axisUnit(maxBytes);
  const yMax = niceMax(maxBytes / unit.div);
  const ticks = 5;
  const n = Math.max(points.length, 1);
  const slot = plotW / n;
  const barW = Math.max(1.2, slot * 0.72);

  function draw() {
    const pageCount = Math.max(1, Math.ceil(keys.length / pageSize));
    legendPage = Math.max(0, Math.min(legendPage, pageCount - 1));
    const visibleKeys = keys.slice(legendPage * pageSize, legendPage * pageSize + pageSize);

    const grid = [];
    for (let i = 0; i <= ticks; i += 1) {
      const value = (yMax / ticks) * (ticks - i);
      const y = pad.top + (plotH / ticks) * i;
      grid.push(`
        <line x1="${pad.left}" x2="${width - pad.right}" y1="${y}" y2="${y}" stroke="#eef1f4" />
        <text x="${pad.left - 8}" y="${y + 4}" text-anchor="end" fill="#9aa3af" font-size="11">${
          value >= 100 ? value.toFixed(0) : value.toFixed(2)
        }</text>`);
    }

    const xLabels = [];
    let lastDay = "";
    points.forEach((point, i) => {
      const t = new Date(point.t);
      const day = `${t.getFullYear()}-${t.getMonth()}-${t.getDate()}`;
      const hour = t.getHours();
      const minute = t.getMinutes();
      const x = pad.left + slot * i + slot / 2;
      if (day !== lastDay) {
        lastDay = day;
        xLabels.push(
          `<text x="${x}" y="${height - 8}" text-anchor="middle" fill="#6b7280" font-size="11">${t.getDate()}</text>`
        );
      } else if (hour === 12 && minute === 0) {
        xLabels.push(
          `<text x="${x}" y="${height - 8}" text-anchor="middle" fill="#9aa3af" font-size="11">12:00</text>`
        );
      }
    });

    const bars = points
      .map((point, i) => {
        let yBase = pad.top + plotH;
        const x = pad.left + slot * i + (slot - barW) / 2;
        const segs = keys
          .map((key) => {
            const bytes = Number((point.values || {})[key] || 0);
            if (bytes <= 0) return "";
            const h = (bytes / unit.div / yMax) * plotH;
            const y = yBase - h;
            const rect = `<rect x="${x}" y="${y}" width="${barW}" height="${Math.max(h, 0)}" fill="${colorFor(key)}" />`;
            yBase = y;
            return rect;
          })
          .join("");
        return `${segs}<rect class="chart-hit" data-bucket="${i}" x="${pad.left + slot * i}" y="${pad.top}" width="${slot}" height="${plotH}" fill="transparent" />`;
      })
      .join("");

    const legendItems = visibleKeys
      .map((key) => {
        return `<div class="chart-legend-item">
          <span class="chart-swatch" style="background:${colorFor(key)}"></span>
          <span>${escapeHtml(seriesLabel(key))} (${formatChartBytes(totals[key] || 0)})</span>
        </div>`;
      })
      .join("");

    const nav =
      keys.length > pageSize
        ? `<div class="chart-legend-nav">
            <button type="button" data-legend="-1" ${legendPage === 0 ? "disabled" : ""} aria-label="上一页">‹</button>
            <span>${legendPage + 1}/${pageCount}</span>
            <button type="button" data-legend="1" ${legendPage >= pageCount - 1 ? "disabled" : ""} aria-label="下一页">›</button>
          </div>`
        : "";

    el.innerHTML = `
      <div class="chart-panel">
        <div class="chart-head">
          <span class="chart-mark" aria-hidden="true"><span></span><span></span><span></span></span>
          <h3 class="chart-title">${escapeHtml(title)}</h3>
        </div>
        <div class="chart-plot">
          <svg viewBox="0 0 ${width} ${height}" role="img" aria-label="${escapeHtml(title)}">
            <text x="14" y="${pad.top + plotH / 2}" fill="#9aa3af" font-size="11" text-anchor="middle" transform="rotate(-90 14 ${pad.top + plotH / 2})">${unit.label}</text>
            ${grid.join("")}
            ${bars}
            ${xLabels.join("")}
          </svg>
          ${hasData ? "" : `<div class="chart-empty">${escapeHtml(emptyHint)}</div>`}
          <div class="chart-tooltip hidden"></div>
        </div>
        <div class="chart-legend">
          <div class="chart-legend-items">${legendItems || `<span class="chart-legend-item">暂无分段</span>`}</div>
          ${nav}
        </div>
      </div>`;

    const plot = el.querySelector(".chart-plot");
    const tooltip = el.querySelector(".chart-tooltip");

    el.querySelectorAll("[data-legend]").forEach((btn) => {
      btn.addEventListener("click", () => {
        legendPage += Number(btn.dataset.legend);
        draw();
      });
    });

    plot.querySelectorAll(".chart-hit").forEach((hit) => {
      hit.addEventListener("pointerenter", (event) => showTip(event, Number(hit.dataset.bucket)));
      hit.addEventListener("pointermove", (event) => showTip(event, Number(hit.dataset.bucket)));
      hit.addEventListener("pointerleave", () => tooltip.classList.add("hidden"));
    });

    function showTip(event, index) {
      const point = points[index];
      if (!point) return;
      const from = new Date(point.t);
      const to = new Date(from.getTime() + bucketSec * 1000);
      const rows = keys
        .map((key) => {
          const bytes = Number((point.values || {})[key] || 0);
          if (bytes <= 0) return "";
          return `<div class="chart-tip-row">
            <span class="chart-tip-dot" style="background:${colorFor(key)}"></span>
            <span class="chart-tip-name">${escapeHtml(seriesLabel(key))}</span>
            <span class="chart-tip-val">${formatChartBytes(bytes)}</span>
          </div>`;
        })
        .join("");
      let sum = 0;
      for (const key of keys) sum += Number((point.values || {})[key] || 0);
      tooltip.innerHTML = `
        ${rows || `<div class="chart-tip-row"><span class="chart-tip-name">无流量</span></div>`}
        <div class="chart-tip-meta">
          <span>From</span><strong>${formatStamp(from)}</strong>
          <span>To</span><strong>${formatStamp(to)}</strong>
          <span>Sum</span><strong>${formatChartBytes(sum)}</strong>
        </div>`;
      tooltip.classList.remove("hidden");
      const bounds = plot.getBoundingClientRect();
      const left = Math.min(event.clientX - bounds.left + 12, bounds.width - 236);
      const top = Math.max(8, event.clientY - bounds.top - 12);
      tooltip.style.left = `${Math.max(8, left)}px`;
      tooltip.style.top = `${top}px`;
    }
  }

  draw();
}

function renderOverview(data) {
  const stats = document.getElementById("overview-stats");
  const count = data.ingest_batches ?? 0;
  const bytes = data.bytes ?? {};

  stats.innerHTML = `
    <article class="metric">
      <div class="metric-label">已接收批次</div>
      <div class="metric-value brand">${count.toLocaleString("zh-CN")}</div>
    </article>
    <article class="metric">
      <div class="metric-label">总量</div>
      <div class="metric-value">${formatBytes(bytes.total)}</div>
    </article>
    <article class="metric">
      <div class="metric-label">直连</div>
      <div class="metric-value">${formatBytes(bytes.direct)}</div>
    </article>
    <article class="metric">
      <div class="metric-label">代理（原始）</div>
      <div class="metric-value">${formatBytes(bytes.proxy_raw)}</div>
    </article>
  `;

  renderTrafficChart(
    document.getElementById("overview-chart"),
    data.traffic,
    "新上报后才会出现柱状图。累计总量仍显示在上方。"
  );
}

function renderNodes(data) {
  renderTrafficChart(
    document.getElementById("nodes-chart"),
    data.traffic,
    "节点上报后，按节点堆叠显示所选时间范围。"
  );

  const wrap = document.getElementById("nodes-table-wrap");
  const nodes = data.nodes ?? [];

  if (nodes.length === 0) {
    wrap.innerHTML = `
      <div class="empty">
        ${EMPTY_ICON}
        <h3>还没有节点</h3>
        <p>添加节点并在目标机器上执行安装命令后，上报会出现在这里。</p>
      </div>`;
    return;
  }

  wrap.innerHTML = `
    <table>
      <thead><tr><th>节点</th><th>站点</th><th>最后上报</th><th>总量</th><th>直连</th><th>代理</th></tr></thead>
      <tbody>
        ${nodes
          .map((node) => {
            const bytes = node.bytes || {};
            const id = node.id || node.ID || "";
            const seen = formatLastSeen(node.last_seen_at);
            const waiting = !node.last_seen_at;
            return `
          <tr class="row-link" data-node="${escapeHtml(id)}">
            <td>${escapeHtml(id)}</td>
            <td>${escapeHtml(node.site_id || node.SiteID || "")}</td>
            <td class="${waiting ? "status-muted" : ""}">${escapeHtml(seen)}</td>
            <td>${formatBytes(bytes.total)}</td>
            <td>${formatBytes(bytes.direct)}</td>
            <td>${formatBytes(bytes.proxy_raw)}</td>
          </tr>`;
          })
          .join("")}
      </tbody>
    </table>`;
  wrap.querySelectorAll("[data-node]").forEach((row) => {
    row.addEventListener("click", () => writeHash("nodes", row.dataset.node));
  });
}

function renderNodeDetail(node) {
  const bytes = node.bytes || {};
  const id = node.id || "";
  const seen = formatLastSeen(node.last_seen_at);
  document.getElementById("nodes-chart").innerHTML = "";
  document.getElementById("nodes-table-wrap").innerHTML = `
    <div class="card">
      <div class="card-header">
        <div>
          <h3>${escapeHtml(id)}</h3>
          <p class="card-sub">站点 ${escapeHtml(node.site_id || "")} · ${escapeHtml(seen)}</p>
        </div>
        <button type="button" class="btn btn-text" id="node-back-btn">返回列表</button>
      </div>
      <div class="metrics">
        <article class="metric">
          <div class="metric-label">总量</div>
          <div class="metric-value">${formatBytes(bytes.total)}</div>
        </article>
        <article class="metric">
          <div class="metric-label">直连</div>
          <div class="metric-value">${formatBytes(bytes.direct)}</div>
        </article>
        <article class="metric">
          <div class="metric-label">代理</div>
          <div class="metric-value">${formatBytes(bytes.proxy_raw)}</div>
        </article>
      </div>
      <div class="device-actions">
        <button type="button" id="node-reissue-btn" class="btn btn-secondary btn-sm">重新发放领取码</button>
        <button type="button" id="node-revoke-btn" class="btn btn-secondary btn-sm">撤销节点</button>
      </div>
      <p class="note">${node.last_seen_at ? "已收到上报。" : "还没有第一包。把安装命令在目标机器上跑一次即可。"}</p>
    </div>`;
  document.getElementById("node-back-btn").addEventListener("click", () => writeHash("nodes"));
  document.getElementById("node-reissue-btn").addEventListener("click", () => {
    document.getElementById("enroll-site").value = node.site_id || "home";
    document.getElementById("enroll-node").value = id;
    resetEnrollDialog();
    enrollDialog.showModal();
  });
  document.getElementById("node-revoke-btn").addEventListener("click", async () => {
    if (!window.confirm(`撤销节点 ${id}？该节点的 token 立刻失效。`)) return;
    const response = await api(`/v1/nodes/${encodeURIComponent(id)}/revoke`, {
      method: "POST",
      body: "{}",
    });
    if (!response.ok) {
      showToast("撤销失败");
      return;
    }
    showToast("节点已撤销");
    writeHash("nodes");
  });
}

function renderEmptyCard(containerId, title, description) {
  document.getElementById(containerId).innerHTML = `
    <div class="card">
      <div class="empty">
        ${EMPTY_ICON}
        <h3>${escapeHtml(title)}</h3>
        <p>${escapeHtml(description)}</p>
      </div>
    </div>`;
}

function renderDevices(data) {
  const devices = data.devices ?? [];
  if (devices.length === 0) {
    renderEmptyCard("devices-content", "暂无设备", "agent 上报带 IP/MAC 的观察后会出现在这里。");
    return;
  }
  document.getElementById("devices-content").innerHTML = `
    <div id="devices-chart"></div>
    <div class="card">
      <div class="card-header">
        <div>
          <h3>设备</h3>
          <p class="card-sub">点进设备可改名、合并，并查看直连/代理曲线</p>
        </div>
      </div>
      <table>
        <thead><tr><th>设备</th><th>地址</th><th>总量</th><th>直连</th><th>代理</th></tr></thead>
        <tbody>
          ${devices
            .map((device) => {
              const bytes = device.bytes || {};
              const label = device.name || device.id || "";
              const addr = [device.ip, device.mac].filter(Boolean).join(" / ") || "—";
              return `<tr class="row-link" data-device="${escapeHtml(device.id || "")}">
                <td>${escapeHtml(label)}</td>
                <td>${escapeHtml(addr)}</td>
                <td>${formatBytes(bytes.total)}</td>
                <td>${formatBytes(bytes.direct)}</td>
                <td>${formatBytes(bytes.proxy_raw)}</td>
              </tr>`;
            })
            .join("")}
        </tbody>
      </table>
    </div>`;
  renderTrafficChart(
    document.getElementById("devices-chart"),
    data.traffic,
    "有设备流量后，这里按设备堆叠显示所选时间范围。"
  );
  document.querySelectorAll("#devices-content [data-device]").forEach((row) => {
    row.addEventListener("click", () => {
      writeHash("devices", row.dataset.device);
    });
  });
}

function renderDeviceDetail(detail, traffic) {
  const device = detail.device || {};
  const merged = detail.merged_from || [];
  const bytes = device.bytes || {};
  const label = device.name || device.id || "";
  document.getElementById("devices-content").innerHTML = `
    <p><a class="back-link" href="#devices">← 返回设备列表</a></p>
    <div class="metrics">
      <article class="metric">
        <div class="metric-label">总量</div>
        <div class="metric-value">${formatBytes(bytes.total)}</div>
      </article>
      <article class="metric">
        <div class="metric-label">直连</div>
        <div class="metric-value">${formatBytes(bytes.direct)}</div>
      </article>
      <article class="metric">
        <div class="metric-label">代理</div>
        <div class="metric-value">${formatBytes(bytes.proxy_raw)}</div>
      </article>
    </div>
    <div id="device-chart"></div>
    <div class="card">
      <div class="card-header">
        <div>
          <h3>${escapeHtml(label)}</h3>
          <p class="card-sub">${escapeHtml([device.ip, device.mac, device.id].filter(Boolean).join(" · "))}</p>
        </div>
      </div>
      <div class="device-actions">
        <div class="field">
          <label for="device-name">备注名</label>
          <input id="device-name" value="${escapeHtml(device.name || "")}" placeholder="例如 书房电脑" />
        </div>
        <button type="button" id="device-rename-btn" class="btn btn-primary btn-sm">保存名称</button>
      </div>
      <div class="device-actions">
        <div class="field">
          <label for="device-merge-id">合并到当前设备</label>
          <input id="device-merge-id" placeholder="另一个设备 ID" />
        </div>
        <button type="button" id="device-merge-btn" class="btn btn-secondary btn-sm">合并</button>
        <button type="button" id="device-unmerge-btn" class="btn btn-text btn-sm">拆分最近一次合并</button>
      </div>
      ${merged.length ? `<p class="note">已合并：${escapeHtml(merged.join("、"))}</p>` : `<p class="note">合并只改身份归属，不会重算历史字节。</p>`}
    </div>`;
  renderTrafficChart(
    document.getElementById("device-chart"),
    traffic,
    "该设备还没有时间曲线。新上报后会出现直连/代理柱。"
  );
  document.getElementById("device-rename-btn").addEventListener("click", async () => {
    const name = document.getElementById("device-name").value.trim();
    const response = await api(`/v1/devices/${encodeURIComponent(device.id)}`, {
      method: "PUT",
      body: JSON.stringify({ name }),
    });
    if (!response.ok) {
      showToast("改名失败");
      return;
    }
    showToast("已保存名称");
    await loadView("devices", device.id);
  });
  document.getElementById("device-merge-btn").addEventListener("click", async () => {
    const other_id = document.getElementById("device-merge-id").value.trim();
    if (!other_id) return;
    const response = await api(`/v1/devices/${encodeURIComponent(device.id)}/merge`, {
      method: "POST",
      body: JSON.stringify({ other_id, reason: "manual" }),
    });
    if (!response.ok) {
      showToast("合并失败");
      return;
    }
    showToast("已合并");
    await loadView("devices", device.id);
  });
  document.getElementById("device-unmerge-btn").addEventListener("click", async () => {
    const response = await api(`/v1/devices/${encodeURIComponent(device.id)}/unmerge`, {
      method: "POST",
      body: "{}",
    });
    if (!response.ok) {
      showToast("拆分失败");
      return;
    }
    showToast("已拆分");
    await loadView("devices", device.id);
  });
}

function renderProxy(data) {
  const proxy = data.proxy || {};
  const outbounds = data.outbounds || [];
  const rows = outbounds
    .map((row) => {
      const share = Number(row.share || 0) * 100;
      const badge = row.configured ? "" : `<span class="badge">未折算</span>`;
      const factor = row.configured ? String(row.factor) : "";
      return `<tr>
        <td>${escapeHtml(row.name || "")} ${badge}</td>
        <td>${formatBytes(row.raw)}</td>
        <td>
          <input class="factor-input" data-outbound="${escapeHtml(row.name || "")}" inputmode="decimal" placeholder="x1.5" value="${escapeHtml(factor)}" />
        </td>
        <td>${formatBytes(row.adjusted)}</td>
        <td>${share.toFixed(1)}%</td>
      </tr>`;
    })
    .join("");
  document.getElementById("proxy-content").innerHTML = `
    <div id="proxy-chart"></div>
    <div class="metrics">
      <article class="metric">
        <div class="metric-label">直连</div>
        <div class="metric-value">${formatBytes(proxy.direct)}</div>
      </article>
      <article class="metric">
        <div class="metric-label">代理（原始）</div>
        <div class="metric-value">${formatBytes(proxy.proxy_raw)}</div>
      </article>
      <article class="metric">
        <div class="metric-label">代理（倍率后）</div>
        <div class="metric-value">${formatBytes(proxy.proxy_adjusted)}</div>
      </article>
      <article class="metric">
        <div class="metric-label">未调倍率</div>
        <div class="metric-value">${formatBytes(proxy.proxy_unadjusted)}</div>
      </article>
    </div>
    <div class="card">
      <div class="card-header">
        <div>
          <h3>出口节点</h3>
          <p class="card-sub">行内填写倍率后立即保存；只影响之后入账</p>
        </div>
      </div>
      ${
        outbounds.length
          ? `<table>
        <thead><tr><th>出口</th><th>原始</th><th>倍率</th><th>折算</th><th>占比</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>
      <p class="note">倍率不会回溯重算已经入账的历史。未配置的出口保持「未折算」。</p>`
          : `<div class="empty"><h3>还没有出口流量</h3><p>接入 Mihomo 后，这里按 outbound 列出用量和倍率。</p></div>`
      }
    </div>`;
  renderTrafficChart(
    document.getElementById("proxy-chart"),
    data.traffic,
    "接入 Mihomo 后，这里按出口节点堆叠显示。"
  );
  document.querySelectorAll(".factor-input").forEach((input) => {
    input.addEventListener("change", async () => {
      const factor = Number(input.value);
      if (!(factor > 0)) {
        showToast("倍率必须大于 0");
        return;
      }
      const response = await api("/v1/proxy/multipliers", {
        method: "PUT",
        body: JSON.stringify({ name: input.dataset.outbound, factor }),
      });
      if (!response.ok) {
        showToast("保存倍率失败");
        return;
      }
      showToast("已保存倍率，后续入账生效");
    });
  });

  const billing = data.billing || {};
  const ratioPct = Number(billing.ratio || 0) * 100;
  document.getElementById("proxy-content").insertAdjacentHTML(
    "beforeend",
    `<div class="card">
      <div class="card-header">
        <div>
          <h3>本月账单对照</h3>
          <p class="card-sub">手填服务商已用量和重置日，对照本地折算用量。偏差超过 10% 会告警。</p>
        </div>
      </div>
      <div class="metrics">
        <article class="metric">
          <div class="metric-label">本地折算</div>
          <div class="metric-value">${formatBytes(billing.local_bytes)}</div>
        </article>
        <article class="metric">
          <div class="metric-label">服务商计数</div>
          <div class="metric-value">${formatBytes(billing.provider_bytes)}</div>
        </article>
        <article class="metric">
          <div class="metric-label">差值</div>
          <div class="metric-value">${formatBytes(Math.abs(Number(billing.delta || 0)))}</div>
        </article>
        <article class="metric">
          <div class="metric-label">差率</div>
          <div class="metric-value">${ratioPct.toFixed(1)}%</div>
        </article>
      </div>
      <div class="device-actions">
        <div class="field">
          <label for="billing-reset-day">重置日（每月 1–28）</label>
          <input id="billing-reset-day" type="number" min="1" max="28" value="${escapeHtml(String(billing.reset_day || 1))}" />
        </div>
        <div class="field">
          <label for="billing-provider">服务商已用量（字节）</label>
          <input id="billing-provider" inputmode="numeric" value="${escapeHtml(String(billing.provider_bytes || 0))}" />
        </div>
        <button type="button" id="billing-save-btn" class="btn btn-primary btn-sm">保存对照</button>
      </div>
      <p class="note">对照窗口从最近一次重置日到现在。保存后只影响告警，不会改账本历史。</p>
    </div>`
  );
  document.getElementById("billing-save-btn").addEventListener("click", async () => {
    const reset_day = Number(document.getElementById("billing-reset-day").value);
    const provider_bytes = Number(document.getElementById("billing-provider").value);
    const response = await api("/v1/proxy/billing", {
      method: "PUT",
      body: JSON.stringify({ reset_day, provider_bytes }),
    });
    if (!response.ok) {
      showToast("保存对账失败");
      return;
    }
    showToast("已更新账单对照");
    await loadView("proxy");
  });
}

const ALERT_LABELS = {
  silence: "节点静默",
  growth: "用量突增",
  reset: "计数器重置",
  drift: "对账偏差",
};

function renderAlerts(data) {
  const alerts = data.alerts ?? [];
  if (alerts.length === 0) {
    renderEmptyCard("alerts-content", "暂无告警", "节点停报、重置频繁或对账偏差过大时会在这里提示。");
    return;
  }
  document.getElementById("alerts-content").innerHTML = `
    <div class="card">
      <div class="card-header">
        <div>
          <h3>待处理告警</h3>
          <p class="card-sub">确认后同一事件不再重复提示，恢复后如再次发生会重新告警</p>
        </div>
      </div>
      <table>
        <thead><tr><th>类型</th><th>对象</th><th>说明</th><th>时间</th><th></th></tr></thead>
        <tbody>
          ${alerts
            .map((item) => {
              const when = item.observed_at ? formatStamp(new Date(item.observed_at)) : "";
              return `<tr>
                <td>${escapeHtml(ALERT_LABELS[item.kind] || item.kind || "")}</td>
                <td>${escapeHtml(item.node_id || item.device_id || "全局")}</td>
                <td>${escapeHtml(item.message || "")}</td>
                <td>${escapeHtml(when)}</td>
                <td><button type="button" class="btn btn-secondary btn-sm" data-ack="${item.id}">确认</button></td>
              </tr>`;
            })
            .join("")}
        </tbody>
      </table>
    </div>`;
  document.querySelectorAll("[data-ack]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const response = await api(`/v1/alerts/${btn.dataset.ack}/ack`, {
        method: "POST",
        body: "{}",
      });
      if (!response.ok) {
        showToast("确认失败");
        return;
      }
      showToast("已确认");
      await loadView("alerts");
    });
  });
}

function renderSettings(data) {
  const retention = data.retention || {};
  const samplesDays = retention.samples_days || 14;
  const daily = retention.daily || "permanent";
  document.getElementById("settings-content").innerHTML = `
    <div class="card">
      <div class="card-header">
        <div>
          <h3>管理员密码</h3>
          <p class="card-sub">修改后当前会话仍然有效</p>
        </div>
      </div>
      <div class="device-actions">
        <div class="field">
          <label for="settings-current">当前密码</label>
          <input id="settings-current" type="password" autocomplete="current-password" />
        </div>
        <div class="field">
          <label for="settings-new">新密码（至少 8 位）</label>
          <input id="settings-new" type="password" autocomplete="new-password" minlength="8" />
        </div>
        <button type="button" id="settings-password-btn" class="btn btn-primary btn-sm">保存密码</button>
      </div>
    </div>
    <div class="card">
      <div class="card-header">
        <div>
          <h3>备份</h3>
          <p class="card-sub">下载当前 SQLite。升级前 server 也会在迁移时自动复制一份 bak 文件。</p>
        </div>
        <button type="button" id="settings-backup-btn" class="btn btn-secondary btn-sm">下载 lantally.db</button>
      </div>
    </div>
    <div class="card">
      <div class="card-header">
        <div>
          <h3>数据保留与协议</h3>
          <p class="card-sub">公开部署必须放在 HTTPS 反代后面。</p>
        </div>
      </div>
      <div class="metrics">
        <article class="metric">
          <div class="metric-label">明细采样</div>
          <div class="metric-value">${escapeHtml(String(samplesDays))} 天</div>
        </article>
        <article class="metric">
          <div class="metric-label">日聚合</div>
          <div class="metric-value">${escapeHtml(daily === "permanent" ? "永久" : String(daily))}</div>
        </article>
        <article class="metric">
          <div class="metric-label">协议版本</div>
          <div class="metric-value">${escapeHtml(String(data.protocol_version || 1))}</div>
        </article>
        <article class="metric">
          <div class="metric-label">软件版本</div>
          <div class="metric-value">${escapeHtml(String(data.app_version || ""))}</div>
        </article>
      </div>
      <p class="note">v0.1 冻结 protocol_version=1。旧 agent 可继续上报；未知版本会被拒绝并返回明确错误。升级失败时用数据目录里的 lantally.db.bak-&lt;unix&gt; 覆盖后重启。</p>
    </div>`;
  document.getElementById("settings-password-btn").addEventListener("click", async () => {
    const current = document.getElementById("settings-current").value;
    const password = document.getElementById("settings-new").value;
    if (password.length < 8) {
      showToast("新密码至少 8 位");
      return;
    }
    const response = await api("/v1/settings/password", {
      method: "PUT",
      body: JSON.stringify({ current, password }),
    });
    if (!response.ok) {
      showToast(response.status === 401 ? "当前密码不正确" : "修改失败");
      return;
    }
    document.getElementById("settings-current").value = "";
    document.getElementById("settings-new").value = "";
    showToast("密码已更新");
  });
  document.getElementById("settings-backup-btn").addEventListener("click", async () => {
    const response = await fetch("/v1/backup", { credentials: "same-origin" });
    if (!response.ok) {
      showToast("备份失败");
      return;
    }
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = "lantally.db";
    link.click();
    URL.revokeObjectURL(url);
    showToast("已开始下载备份");
  });
}

async function loadView(route, id = "") {
  if (route === "nodes" && id) {
    const detailRes = await api(`/v1/nodes/${encodeURIComponent(id)}`);
    if (detailRes.status === 401) {
      setScreen(false);
      stopAutoRefresh();
      return false;
    }
    if (!detailRes.ok) {
      showToast("加载节点失败");
      return true;
    }
    renderNodeDetail(await detailRes.json());
    return true;
  }
  if (route === "devices" && id) {
    const detailRes = await api(`/v1/devices/${encodeURIComponent(id)}`);
    if (detailRes.status === 401) {
      setScreen(false);
      stopAutoRefresh();
      return false;
    }
    if (!detailRes.ok) {
      showToast("加载设备失败");
      return true;
    }
    const trafficRes = await api(`/v1/traffic?${trafficParams({ group: "class", device: id }).toString()}`);
    const detail = await detailRes.json();
    const traffic = trafficRes.ok ? await trafficRes.json() : { keys: [], points: [], totals: {} };
    renderDeviceDetail(detail, traffic);
    return true;
  }

  const query = trafficParams({
    group: route === "proxy" ? "outbound" : uiState.group,
    class: route === "proxy" ? "proxy_raw" : "",
  });
  const response = await api(`/v1/${route}?${query.toString()}`);
  if (response.status === 401) {
    setScreen(false);
    stopAutoRefresh();
    return false;
  }
  if (!response.ok) {
    showToast("加载失败");
    return true;
  }
  const data = await response.json();
  switch (route) {
    case "overview":
      renderOverview(data);
      break;
    case "nodes":
      renderNodes(data);
      break;
    case "devices":
      {
        const trafficRes = await api(`/v1/traffic?${trafficParams({ group: "device", class: "total" }).toString()}`);
        if (trafficRes.ok) data.traffic = await trafficRes.json();
      }
      renderDevices(data);
      break;
    case "proxy":
      renderProxy(data);
      break;
    case "alerts":
      renderAlerts(data);
      break;
    case "settings":
      renderSettings(data);
      break;
  }
  return true;
}

async function refreshCurrent() {
  await loadView(currentRoute, currentDeviceId);
  if (currentRoute !== "overview") await loadView("overview");
}

function startAutoRefresh() {
  stopAutoRefresh();
  refreshTimer = setInterval(() => {
    if (currentRoute === "overview") loadView("overview");
  }, 30000);
}

function stopAutoRefresh() {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = null;
}

function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function applyAuthMode() {
  loginModeLabel.textContent = setupNeeded ? "首次设置" : "登录控制台";
  setupConfirm.classList.toggle("hidden", !setupNeeded);
  loginPassword2.required = setupNeeded;
  loginSubmitBtn.textContent = setupNeeded ? "完成设置" : "进入";
  document.getElementById("login-password").autocomplete = setupNeeded
    ? "new-password"
    : "current-password";
}

function stopClaimWait() {
  if (claimWaitTimer) {
    clearInterval(claimWaitTimer);
    claimWaitTimer = null;
  }
}

function showClaimCommand() {
  enrollCommand.textContent = claimCommands[claimPlatform] || "";
  document.querySelectorAll("[data-platform]").forEach((btn) => {
    btn.classList.toggle("active", btn.dataset.platform === claimPlatform);
  });
}

async function pollClaimNode() {
  if (!claimNodeId) return;
  const response = await api(`/v1/nodes/${encodeURIComponent(claimNodeId)}`);
  if (response.status === 404) {
    enrollWait.textContent = "等待领取码被兑换…";
    enrollWait.classList.remove("ok");
    return;
  }
  if (!response.ok) return;
  const node = await response.json();
  if (node.last_seen_at) {
    enrollWait.textContent = "已收到第一包 ✓";
    enrollWait.classList.add("ok");
    stopClaimWait();
    await loadView("nodes");
    return;
  }
  enrollWait.textContent = "领取码已兑换，等待第一包上报…";
  enrollWait.classList.remove("ok");
}

function startClaimWait(nodeId) {
  claimNodeId = nodeId;
  stopClaimWait();
  pollClaimNode();
  claimWaitTimer = setInterval(pollClaimNode, 2000);
}

function resetEnrollDialog() {
  enrollError.classList.add("hidden");
  enrollResult.classList.add("hidden");
  enrollToken.textContent = "";
  enrollCommand.textContent = "";
  enrollHint.textContent = "";
  enrollWait.textContent = "等待第一包上报…";
  enrollWait.classList.remove("ok");
  enrollSubmitBtn.textContent = "生成安装命令";
  enrollSubmitBtn.disabled = false;
  claimCommands = { linux: "", macos: "", windows: "" };
  claimPlatform = "linux";
  stopClaimWait();
}

async function bootstrap() {
  try {
    const setupRes = await fetch("/v1/setup", { credentials: "same-origin" });
    if (setupRes.ok) {
      setupNeeded = Boolean((await setupRes.json()).needed);
    }
  } catch {
    setupNeeded = false;
  }
  applyAuthMode();
  if (setupNeeded) {
    setScreen(false);
    return;
  }
  const parsed = parseHash();
  const ok = await loadView("overview");
  if (!ok) {
    setScreen(false);
    return;
  }
  setScreen(true);
  setRoute(parsed.route, parsed.id);
  await loadView(currentRoute, currentDeviceId);
  startAutoRefresh();
}

loginForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  loginError.classList.add("hidden");
  const password = String(new FormData(loginForm).get("password") || "");
  if (setupNeeded) {
    const again = String(loginPassword2.value || "");
    if (password.length < 8) {
      loginError.textContent = "密码至少 8 位";
      loginError.classList.remove("hidden");
      return;
    }
    if (password !== again) {
      loginError.textContent = "两次输入的密码不一致";
      loginError.classList.remove("hidden");
      return;
    }
    const response = await api("/v1/setup", {
      method: "POST",
      body: JSON.stringify({ password }),
    });
    if (!response.ok) {
      loginError.textContent = response.status === 409 ? "已经设置过管理员" : "设置失败";
      loginError.classList.remove("hidden");
      return;
    }
    setupNeeded = false;
    applyAuthMode();
    loginForm.reset();
    setScreen(true);
    setRoute("overview");
    writeHash("overview");
    await refreshCurrent();
    startAutoRefresh();
    return;
  }
  const response = await api("/v1/login", {
    method: "POST",
    body: JSON.stringify({ password }),
  });
  if (!response.ok) {
    loginError.textContent = "密码不正确";
    loginError.classList.remove("hidden");
    return;
  }
  loginForm.reset();
  setScreen(true);
  setRoute("overview");
  writeHash("overview");
  await refreshCurrent();
  startAutoRefresh();
});

logoutBtn.addEventListener("click", async () => {
  await api("/v1/logout", { method: "POST", body: "{}" });
  stopAutoRefresh();
  stopClaimWait();
  setScreen(false);
  location.hash = "";
});

refreshBtn.addEventListener("click", () => refreshCurrent());

window.addEventListener("hashchange", async () => {
  if (writingHash) return;
  const parsed = parseHash();
  setRoute(parsed.route, parsed.id);
  await loadView(currentRoute, currentDeviceId);
});

document.getElementById("chart-toolbar").addEventListener("click", async (event) => {
  const btn = event.target.closest("[data-range],[data-group]");
  if (!btn) return;
  if (btn.dataset.range) uiState.range = btn.dataset.range;
  if (btn.dataset.group) uiState.group = btn.dataset.group;
  writeHash(currentRoute, currentDeviceId);
  syncToolbar();
  await loadView(currentRoute, currentDeviceId);
});

document.querySelectorAll(".nav-item").forEach((link) => {
  link.addEventListener("click", (event) => {
    event.preventDefault();
    writeHash(link.dataset.route);
  });
});

enrollOpenBtn.addEventListener("click", () => {
  resetEnrollDialog();
  enrollDialog.showModal();
});

enrollCancelBtn.addEventListener("click", () => enrollDialog.close());
enrollDialog.addEventListener("close", resetEnrollDialog);

enrollForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  enrollError.classList.add("hidden");

  const formData = new FormData(enrollForm);
  const site_id = String(formData.get("site_id") || "").trim();
  const node_id = String(formData.get("node_id") || "").trim();
  const local = Boolean(enrollLocal.checked);
  if (!site_id || !node_id) return;

  const response = await api("/v1/claims", {
    method: "POST",
    body: JSON.stringify({ site_id, node_id, local }),
  });

  if (!response.ok) {
    enrollError.textContent = "生成领取码失败";
    enrollError.classList.remove("hidden");
    return;
  }

  const data = await response.json();
  enrollToken.textContent = data.code || "";
  claimCommands = {
    linux: data.linux || "",
    macos: data.macos || "",
    windows: data.windows || "",
  };
  enrollHint.textContent = data.mihomo_hint || (local ? "" : "安装脚本会只读探测本机 Mihomo，密钥不会上传。");
  enrollResult.classList.remove("hidden");
  enrollSubmitBtn.textContent = "已生成";
  enrollSubmitBtn.disabled = true;
  showClaimCommand();
  startClaimWait(node_id);
  await loadView("nodes");
  showToast("领取码已生成");
});

copyTokenBtn.addEventListener("click", async () => {
  const text = enrollCommand.textContent || enrollToken.textContent;
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    showToast("已复制");
  } catch {
    showToast("请手动复制");
  }
});

document.querySelector(".install-platforms").addEventListener("click", (event) => {
  const btn = event.target.closest("[data-platform]");
  if (!btn) return;
  event.preventDefault();
  claimPlatform = btn.dataset.platform;
  showClaimCommand();
});

bootstrap();
