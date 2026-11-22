import React, { createContext, useCallback, useContext, useEffect, useState } from "react";
import { DICT, type Lang, type RouteKey, VIEW_META } from "../i18n/translations";
import type { TimeRangePreset, TimeRangeState, TrafficGroup } from "../api/types";

const VALID_PRESETS = new Set<TimeRangePreset>([
  "today",
  "24h",
  "72h",
  "7d",
  "30d",
  "billing",
  "custom",
]);

const VALID_GROUPS = new Set<TrafficGroup>([
  "node",
  "outbound",
  "group",
  "process",
  "domain",
  "iface",
  "device",
  "class",
]);

function defaultCustomDates(): { from: string; to: string } {
  const now = new Date();
  const weekAgo = new Date(now.getTime() - 7 * 86400 * 1000);
  const fmt = (d: Date) =>
    `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  return { from: fmt(weekAgo), to: fmt(now) };
}

export interface RouteState {
  route: RouteKey;
  id: string;
}

function parseLocationHash(): { route: RouteState; preset?: TimeRangePreset; from?: string; to?: string; group?: TrafficGroup } {
  const raw = (window.location.hash || "#overview").replace(/^#/, "") || "overview";
  const [pathPart, queryPart] = raw.split("?");
  const parts = (pathPart || "overview").split("/").filter(Boolean);
  const rawRoute = (parts[0] || "overview") as RouteKey;
  const route: RouteKey = rawRoute in VIEW_META ? rawRoute : "overview";
  const id = parts[1] ? decodeURIComponent(parts[1]) : "";
  const params = new URLSearchParams(queryPart || "");
  const r = params.get("range") as TimeRangePreset | null;
  const g = params.get("group") as TrafficGroup | null;
  const f = params.get("from") || undefined;
  const t = params.get("to") || undefined;
  return {
    route: { route, id },
    preset: r && VALID_PRESETS.has(r) ? r : undefined,
    from: f,
    to: t,
    group: g && VALID_GROUPS.has(g) ? g : undefined,
  };
}

interface AppContextValue {
  lang: Lang;
  setLang: (l: Lang) => void;
  t: (typeof DICT)[Lang];
  routeState: RouteState;
  navigate: (route: RouteKey, id?: string) => void;
  timeRange: TimeRangeState;
  setTimeRange: (next: TimeRangeState) => void;
  overviewGroup: TrafficGroup;
  setOverviewGroup: (g: TrafficGroup) => void;
  toastMessage: string | null;
  showToast: (msg: string) => void;
  refreshTick: number;
  triggerRefresh: () => void;
}

const AppContext = createContext<AppContextValue | null>(null);

export function AppProvider({ children }: { children: React.ReactNode }) {
  const initial = parseLocationHash();
  const defDates = defaultCustomDates();

  const [lang, setLangState] = useState<Lang>(() => {
    try {
      const saved = localStorage.getItem("lantally_lang");
      if (saved === "en" || saved === "zh-CN") return saved;
    } catch {
      // ignore
    }
    return "zh-CN";
  });

  const [routeState, setRouteState] = useState<RouteState>(initial.route);
  const [timeRange, setTimeRangeState] = useState<TimeRangeState>({
    preset: initial.preset || "72h",
    from: initial.from || defDates.from,
    to: initial.to || defDates.to,
  });
  const [overviewGroup, setOverviewGroupState] = useState<TrafficGroup>(initial.group || "node");
  const [toastMessage, setToastMessage] = useState<string | null>(null);
  const [refreshTick, setRefreshTick] = useState(0);

  const setLang = useCallback((next: Lang) => {
    setLangState(next);
    document.documentElement.lang = next;
    try {
      localStorage.setItem("lantally_lang", next);
    } catch {
      // ignore
    }
  }, []);

  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);

  const syncHash = useCallback(
    (r: RouteState, tr: TimeRangeState, grp: TrafficGroup) => {
      const params = new URLSearchParams();
      params.set("range", tr.preset);
      if (tr.preset === "custom") {
        if (tr.from) params.set("from", tr.from);
        if (tr.to) params.set("to", tr.to);
      }
      params.set("group", grp);
      const path = r.id ? `${r.route}/${encodeURIComponent(r.id)}` : r.route;
      const target = `#${path}?${params.toString()}`;
      if (window.location.hash !== target) {
        window.location.hash = target;
      }
    },
    [],
  );

  const navigate = useCallback(
    (route: RouteKey, id = "") => {
      const next = { route, id };
      setRouteState(next);
      syncHash(next, timeRange, overviewGroup);
    },
    [overviewGroup, syncHash, timeRange],
  );

  const setTimeRange = useCallback(
    (next: TimeRangeState) => {
      setTimeRangeState(next);
      syncHash(routeState, next, overviewGroup);
    },
    [overviewGroup, routeState, syncHash],
  );

  const setOverviewGroup = useCallback(
    (g: TrafficGroup) => {
      setOverviewGroupState(g);
      syncHash(routeState, timeRange, g);
    },
    [routeState, syncHash, timeRange],
  );

  useEffect(() => {
    const onHashChange = () => {
      const parsed = parseLocationHash();
      setRouteState((prev) =>
        prev.route === parsed.route.route && prev.id === parsed.route.id ? prev : parsed.route,
      );
      if (parsed.preset) {
        setTimeRangeState((prev) => {
          const nextFrom = parsed.from || prev.from;
          const nextTo = parsed.to || prev.to;
          if (prev.preset === parsed.preset && prev.from === nextFrom && prev.to === nextTo) {
            return prev;
          }
          return {
            preset: parsed.preset!,
            from: nextFrom,
            to: nextTo,
          };
        });
      }
      if (parsed.group) {
        setOverviewGroupState((prev) => (prev === parsed.group ? prev : parsed.group!));
      }
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  const showToast = useCallback((msg: string) => {
    setToastMessage(msg);
  }, []);

  useEffect(() => {
    if (!toastMessage) return;
    const timer = window.setTimeout(() => setToastMessage(null), 2600);
    return () => window.clearTimeout(timer);
  }, [toastMessage]);

  const triggerRefresh = useCallback(() => {
    setRefreshTick((n) => n + 1);
  }, []);

  return (
    <AppContext.Provider
      value={{
        lang,
        setLang,
        t: DICT[lang],
        routeState,
        navigate,
        timeRange,
        setTimeRange,
        overviewGroup,
        setOverviewGroup,
        toastMessage,
        showToast,
        refreshTick,
        triggerRefresh,
      }}
    >
      {children}
    </AppContext.Provider>
  );
}

export function useApp(): AppContextValue {
  const ctx = useContext(AppContext);
  if (!ctx) throw new Error("useApp must be used inside AppProvider");
  return ctx;
}
