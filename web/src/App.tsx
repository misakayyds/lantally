import { useCallback, useEffect, useState } from "react";
import { api, ApiError, setUnauthorizedHandler } from "./api/client";
import { LoginScreen } from "./components/LoginScreen";
import { TimeRangeBar } from "./components/TimeRangeBar";
import { type RouteKey, VIEW_META } from "./i18n/translations";
import Alerts from "./pages/Alerts";
import AppsDomains from "./pages/AppsDomains";
import Devices from "./pages/Devices";
import Nodes from "./pages/Nodes";
import Overview from "./pages/Overview";
import Proxy from "./pages/Proxy";
import Settings from "./pages/Settings";
import Watchlist from "./pages/Watchlist";
import { AppProvider, useApp } from "./state/AppContext";

const NAV_ROUTES: RouteKey[] = [
  "overview",
  "proxy",
  "watchlist",
  "apps",
  "devices",
  "nodes",
  "alerts",
];

function Shell() {
  const { lang, setLang, t, routeState, navigate, toastMessage, showToast, triggerRefresh } =
    useApp();

  const [authState, setAuthState] = useState<"checking" | "setup" | "login" | "ready">("checking");

  const bootstrapAuth = useCallback(async () => {
    try {
      const setup = await api.setupStatus();
      if (setup.needed) {
        setAuthState("setup");
        return;
      }
      await api.settings(true);
      setAuthState("ready");
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setAuthState("login");
      } else {
        setAuthState("login");
      }
    }
  }, []);

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setAuthState("login");
    });
    void bootstrapAuth();
    return () => setUnauthorizedHandler(null);
  }, [bootstrapAuth]);

  const handleLogout = async () => {
    try {
      await api.logout();
    } catch {
      // ignore
    }
    setAuthState("login");
  };

  if (authState === "checking") {
    return (
      <div className="login-main" style={{ minHeight: "100vh" }}>
        <span className="status-muted">{t.loading}</span>
      </div>
    );
  }

  if (authState === "setup" || authState === "login") {
    return (
      <LoginScreen
        setupNeeded={authState === "setup"}
        onAuthenticated={() => setAuthState("ready")}
      />
    );
  }

  const currentMeta = VIEW_META[routeState.route];
  const pageTitle =
    routeState.route === "devices" && routeState.id
      ? t.deviceDetail
      : routeState.route === "nodes" && routeState.id
        ? t.nodeDetail
        : currentMeta[lang === "zh-CN" ? "zh" : "en"];

  const showTimeBar =
    ["overview", "proxy", "watchlist", "apps", "devices", "nodes"].includes(routeState.route) &&
    !routeState.id;

  return (
    <div className="app">
      <header className="app-header">
        <div className="header-left">
          <span className="logo-mark sm">LT</span>
          <span className="logo-text">LanTally</span>
        </div>

        <nav className="header-nav" aria-label="Main Navigation">
          {NAV_ROUTES.map((key) => {
            const label = VIEW_META[key][lang === "zh-CN" ? "zh" : "en"];
            const active = routeState.route === key;
            return (
              <a
                key={key}
                href={`#${key}`}
                className={`nav-item${active ? " active" : ""}`}
                onClick={(e) => {
                  e.preventDefault();
                  navigate(key);
                }}
              >
                {label}
              </a>
            );
          })}
        </nav>

        <div className="header-right">
          <button
            type="button"
            className={`btn btn-text btn-sm${routeState.route === "settings" ? " active" : ""}`}
            onClick={() => navigate("settings")}
          >
            {VIEW_META.settings[lang === "zh-CN" ? "zh" : "en"]}
          </button>
          <button
            type="button"
            className="btn btn-text btn-sm"
            onClick={() => setLang(lang === "zh-CN" ? "en" : "zh-CN")}
          >
            {lang === "zh-CN" ? "EN" : "中文"}
          </button>
          <button
            type="button"
            className="btn btn-text btn-sm"
            onClick={() => {
              triggerRefresh();
              showToast(t.refreshed);
            }}
          >
            {t.refresh}
          </button>
          <button type="button" className="btn btn-text btn-sm muted" onClick={handleLogout}>
            {t.logout}
          </button>
        </div>
      </header>

      <main className="app-main">
        <header className="page-header">
          <div>
            <p className="page-kicker">{currentMeta.eyebrow}</p>
            <h2>{pageTitle}</h2>
          </div>
          {showTimeBar && <TimeRangeBar />}
        </header>

        {routeState.route === "overview" && <Overview />}
        {routeState.route === "proxy" && <Proxy />}
        {routeState.route === "watchlist" && <Watchlist />}
        {routeState.route === "apps" && <AppsDomains />}
        {routeState.route === "devices" && <Devices />}
        {routeState.route === "nodes" && <Nodes />}
        {routeState.route === "alerts" && <Alerts />}
        {routeState.route === "settings" && <Settings />}
      </main>

      {toastMessage && (
        <div className="toast" role="status" aria-live="polite">
          {toastMessage}
        </div>
      )}
    </div>
  );
}

export default function App() {
  return (
    <AppProvider>
      <Shell />
    </AppProvider>
  );
}
