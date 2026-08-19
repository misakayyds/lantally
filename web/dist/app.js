const ROUTES = {
  overview: { title: "概览", eyebrow: "Dashboard" },
  nodes: { title: "节点", eyebrow: "Agents" },
  devices: { title: "设备", eyebrow: "Traffic" },
  proxy: { title: "代理", eyebrow: "Proxy" },
  alerts: { title: "告警", eyebrow: "Alerts" },
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

let currentRoute = "overview";
let refreshTimer = null;

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

function setRoute(route) {
  if (!ROUTES[route]) route = "overview";
  currentRoute = route;

  document.querySelectorAll(".nav-item").forEach((link) => {
    link.classList.toggle("active", link.dataset.route === route);
  });
  document.querySelectorAll(".view").forEach((view) => {
    view.classList.toggle("active", view.dataset.view === route);
  });

  pageTitle.textContent = ROUTES[route].title;
  pageEyebrow.textContent = ROUTES[route].eyebrow;
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
  const digits = v >= 10 ? 1 : 2;
  return `${v.toFixed(digits)} ${units[i]}`;
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
          <h3 class="chart-title">最近72小时流量使用情况</h3>
        </div>
        <div class="chart-plot">
          <svg viewBox="0 0 ${width} ${height}" role="img" aria-label="最近72小时流量使用情况">
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
    "节点上报后，按节点堆叠显示最近 72 小时流量。"
  );

  const wrap = document.getElementById("nodes-table-wrap");
  const nodes = data.nodes ?? [];

  if (nodes.length === 0) {
    wrap.innerHTML = `
      <div class="empty">
        ${EMPTY_ICON}
        <h3>还没有节点</h3>
        <p>注册节点并部署 agent 后，上报会出现在这里。</p>
      </div>`;
    return;
  }

  wrap.innerHTML = `
    <table>
      <thead><tr><th>节点</th><th>站点</th><th>总量</th><th>直连</th><th>代理</th></tr></thead>
      <tbody>
        ${nodes
          .map((node) => {
            const bytes = node.bytes || {};
            return `
          <tr>
            <td>${escapeHtml(node.id || node.ID || "")}</td>
            <td>${escapeHtml(node.site_id || node.SiteID || "")}</td>
            <td>${formatBytes(bytes.total)}</td>
            <td>${formatBytes(bytes.direct)}</td>
            <td>${formatBytes(bytes.proxy_raw)}</td>
          </tr>`;
          })
          .join("")}
      </tbody>
    </table>`;
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
    <div class="card">
      <div class="card-header">
        <div>
          <h3>设备</h3>
          <p class="card-sub">neigh 观察只用于识别，不计入总量</p>
        </div>
      </div>
      <table>
        <thead><tr><th>设备</th><th>站点</th><th>总量</th><th>直连</th><th>代理</th></tr></thead>
        <tbody>
          ${devices
            .map((device) => {
              const bytes = device.bytes || {};
              return `<tr>
                <td>${escapeHtml(device.id || "")}</td>
                <td>${escapeHtml(device.site_id || "")}</td>
                <td>${formatBytes(bytes.total)}</td>
                <td>${formatBytes(bytes.direct)}</td>
                <td>${formatBytes(bytes.proxy_raw)}</td>
              </tr>`;
            })
            .join("")}
        </tbody>
      </table>
    </div>`;
}

function renderProxy(data) {
  const proxy = data.proxy || {};
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
    </div>`;
  renderTrafficChart(
    document.getElementById("proxy-chart"),
    data.traffic,
    "接入 Mihomo 后，这里按直连和代理堆叠显示。"
  );
}

async function loadView(route) {
  const response = await api(`/v1/${route}`);
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
      renderDevices(data);
      break;
    case "proxy":
      renderProxy(data);
      break;
    case "alerts":
      renderEmptyCard("alerts-content", "暂无告警", "节点离线或偏差过大时会在这里提示。");
      break;
  }
  return true;
}

async function refreshCurrent() {
  await loadView(currentRoute);
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

function resetEnrollDialog() {
  enrollError.classList.add("hidden");
  enrollResult.classList.add("hidden");
  enrollToken.textContent = "";
  enrollSubmitBtn.textContent = "生成 Token";
  enrollSubmitBtn.disabled = false;
}

async function bootstrap() {
  const ok = await loadView("overview");
  if (!ok) {
    setScreen(false);
    return;
  }
  setScreen(true);
  setRoute(location.hash.replace("#", "") || "overview");
  await loadView(currentRoute);
  startAutoRefresh();
}

loginForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  loginError.classList.add("hidden");
  const password = new FormData(loginForm).get("password");
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
  await refreshCurrent();
  startAutoRefresh();
});

logoutBtn.addEventListener("click", async () => {
  await api("/v1/logout", { method: "POST", body: "{}" });
  stopAutoRefresh();
  setScreen(false);
  location.hash = "";
});

refreshBtn.addEventListener("click", () => refreshCurrent());

window.addEventListener("hashchange", async () => {
  setRoute(location.hash.replace("#", "") || "overview");
  await loadView(currentRoute);
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
  if (!site_id || !node_id) return;

  const response = await api("/v1/enroll", {
    method: "POST",
    body: JSON.stringify({ site_id, node_id }),
  });

  if (!response.ok) {
    enrollError.textContent =
      response.status === 409 ? "节点 ID 已存在" : "注册失败";
    enrollError.classList.remove("hidden");
    return;
  }

  const data = await response.json();
  enrollToken.textContent = data.token || "";
  enrollResult.classList.remove("hidden");
  enrollSubmitBtn.textContent = "已生成";
  enrollSubmitBtn.disabled = true;
  await loadView("nodes");
  showToast("节点已注册");
});

copyTokenBtn.addEventListener("click", async () => {
  const token = enrollToken.textContent;
  if (!token) return;
  try {
    await navigator.clipboard.writeText(token);
    showToast("已复制");
  } catch {
    showToast("请手动复制");
  }
});

bootstrap();
