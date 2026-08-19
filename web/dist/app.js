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

function renderOverview(data) {
  const stats = document.getElementById("overview-stats");
  const tags = document.getElementById("ledger-tags");
  const count = data.ingest_batches ?? 0;
  const ledgers = data.ledgers ?? [];
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

  tags.innerHTML = ledgers.length
    ? ledgers.map((name) => `<span class="chip">${escapeHtml(name)}</span>`).join("")
    : `<span class="chip">暂无</span>`;
}

function renderNodes(data) {
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
      <thead><tr><th>节点</th><th>站点</th></tr></thead>
      <tbody>
        ${nodes
          .map(
            (node) => `
          <tr>
            <td>${escapeHtml(node.ID || node.id || "")}</td>
            <td>${escapeHtml(node.SiteID || node.site_id || "")}</td>
          </tr>`
          )
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
  const hasData = Object.values(proxy).some((value) => Number(value) > 0);
  if (!hasData) {
    renderEmptyCard("proxy-content", "暂无代理统计", "接入 Mihomo 后按 outbound 展示直连与代理。");
    return;
  }
  document.getElementById("proxy-content").innerHTML = `
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
