const ROUTES = {
  overview: { title: "概览", eyebrow: "Dashboard" },
  nodes: { title: "节点", eyebrow: "Agents" },
  devices: { title: "设备", eyebrow: "Traffic" },
  proxy: { title: "代理", eyebrow: "Proxy" },
  alerts: { title: "告警", eyebrow: "Alerts" },
};

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
  showToast.timer = setTimeout(() => toast.classList.add("hidden"), 2600);
}

async function api(path, options = {}) {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...(options.headers || {}),
    },
  });
  return response;
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

  document.querySelectorAll(".nav-link").forEach((link) => {
    link.classList.toggle("active", link.dataset.route === route);
  });
  document.querySelectorAll(".view").forEach((view) => {
    view.classList.toggle("active", view.dataset.view === route);
  });

  pageTitle.textContent = ROUTES[route].title;
  pageEyebrow.textContent = ROUTES[route].eyebrow;
}

function renderOverview(data) {
  const stats = document.getElementById("overview-stats");
  const tags = document.getElementById("ledger-tags");
  const count = data.ingest_batches ?? 0;
  const ledgers = data.ledgers ?? [];

  stats.innerHTML = `
    <article class="stat-card accent">
      <div class="label">已接收批次</div>
      <div class="value">${count.toLocaleString("zh-CN")}</div>
    </article>
    <article class="stat-card">
      <div class="label">账本维度</div>
      <div class="value">${ledgers.length}</div>
    </article>
    <article class="stat-card">
      <div class="label">服务状态</div>
      <div class="value" style="font-size:1.1rem;color:var(--ok)">在线</div>
    </article>
  `;

  tags.innerHTML = ledgers
    .map((name) => `<span class="tag">${escapeHtml(name)}</span>`)
    .join("");
}

function renderNodes(data) {
  const wrap = document.getElementById("nodes-table-wrap");
  const nodes = data.nodes ?? [];

  if (nodes.length === 0) {
    wrap.innerHTML = `
      <div class="empty-state">
        <div class="icon" aria-hidden="true">◎</div>
        <h3>还没有上报节点</h3>
        <p>点击「注册节点」生成 agent token，然后在网关或旁路由上部署 agent。</p>
      </div>
    `;
    return;
  }

  wrap.innerHTML = `
    <table>
      <thead>
        <tr><th>节点 ID</th><th>站点 ID</th></tr>
      </thead>
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
    </table>
  `;
}

function renderEmptyPanel(containerId, title, description) {
  document.getElementById(containerId).innerHTML = `
    <div class="panel">
      <div class="empty-state">
        <div class="icon" aria-hidden="true">…</div>
        <h3>${escapeHtml(title)}</h3>
        <p>${escapeHtml(description)}</p>
      </div>
    </div>
  `;
}

async function loadView(route) {
  const response = await api(`/v1/${route}`);
  if (response.status === 401) {
    setScreen(false);
    stopAutoRefresh();
    return false;
  }
  if (!response.ok) {
    showToast(`加载 ${route} 失败`);
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
      renderEmptyPanel(
        "devices-content",
        "暂无设备数据",
        "agent 开始上报后，这里会显示各设备的流量增量。"
      );
      break;
    case "proxy":
      renderEmptyPanel(
        "proxy-content",
        "暂无代理统计",
        "接入 Mihomo 采集器后，会按 outbound 汇总直连与代理流量。"
      );
      break;
    case "alerts":
      renderEmptyPanel(
        "alerts-content",
        "暂无告警",
        "节点离线或用量偏差过大时，告警会出现在这里。"
      );
      break;
  }
  return true;
}

async function refreshCurrent() {
  await loadView(currentRoute);
  if (currentRoute !== "overview") {
    await loadView("overview");
  }
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
  const hash = location.hash.replace("#", "");
  setRoute(hash || "overview");
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
    loginError.textContent = "密码不正确，请检查容器日志或 bootstrap 环境变量。";
    loginError.classList.remove("hidden");
    return;
  }
  loginForm.reset();
  setScreen(true);
  setRoute("overview");
  await refreshCurrent();
  startAutoRefresh();
  showToast("登录成功");
});

logoutBtn.addEventListener("click", async () => {
  await api("/v1/logout", { method: "POST", body: "{}" });
  stopAutoRefresh();
  setScreen(false);
  location.hash = "";
  showToast("已退出登录");
});

refreshBtn.addEventListener("click", async () => {
  await refreshCurrent();
  showToast("已刷新");
});

window.addEventListener("hashchange", async () => {
  const route = location.hash.replace("#", "") || "overview";
  setRoute(route);
  await loadView(route);
});

enrollOpenBtn.addEventListener("click", () => {
  resetEnrollDialog();
  enrollDialog.showModal();
});

enrollCancelBtn.addEventListener("click", () => {
  enrollDialog.close();
});

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
    const text = await response.text();
    enrollError.textContent =
      response.status === 409
        ? "节点 ID 已存在，请换一个名称。"
        : text || "注册失败，请稍后重试。";
    enrollError.classList.remove("hidden");
    return;
  }

  const data = await response.json();
  enrollToken.textContent = data.token || "";
  enrollResult.classList.remove("hidden");
  enrollSubmitBtn.textContent = "已生成";
  enrollSubmitBtn.disabled = true;
  await loadView("nodes");
  showToast(`节点 ${data.node_id} 已注册`);
});

copyTokenBtn.addEventListener("click", async () => {
  const token = enrollToken.textContent;
  if (!token) return;
  try {
    await navigator.clipboard.writeText(token);
    showToast("Token 已复制");
  } catch {
    showToast("复制失败，请手动选择复制");
  }
});

bootstrap();
