import { DashboardApi, unwrapSnapshot } from "./api.js";
import { createAlertsController } from "./alerts.js";
import { createContainersController } from "./containers.js";
import { DemoApi, startDemoFeed } from "./demo.js";
import { bytes, clamp, number, percent, rate, setProgress, setText, timeAgo, uptime } from "./format.js";
import { createHistoryController } from "./history.js";
import { createLogsController } from "./logs.js";
import { createMetricCharts } from "./metrics.js";
import { createNodesController } from "./nodes.js";
import { createOperationsController } from "./operations.js";
import { createOverviewController } from "./overview.js";
import { createServicesController } from "./services.js";
import { createSettingsController } from "./settings.js";
import { MetricsStream } from "./socket.js";
import { createTerminalController } from "./terminal.js";
import { OVERVIEW_WIDGET_DEFAULT_HIDDEN, OVERVIEW_WIDGET_DEFAULT_SIZES, OVERVIEW_WIDGET_ORDER, normalizeOverviewPreferences } from "./widget-catalog.js";

const demo = new URLSearchParams(window.location.search).get("demo") === "1";
const api = demo ? new DemoApi() : new DashboardApi();
const charts = createMetricCharts();
let stream = null;
let stopDemoFeed = null;
let latestCollectedAt = null;
let refreshing = null;
let connectionState = "connecting";
let localConnectionState = "connecting";
let lastAnnouncedConnection = null;
let latestLocalEnvelope = null;
let latestServices = [];
let latestSelectedContainers = [];
let selectedNodeId = "local";
let nodeSelectionInitialized = false;
let sessionRenewalTimer = null;
let lastSessionRenewalAt = 0;
let preferenceSaveTimer = null;
let preferenceSavePending = {};
let sessionAdmin = false;
let sessionAuthenticated = false;
let sessionHostShellCapability = false;
let snapshotPartial = false;
let snapshotPartialSources = [];
let latestOverviewData = { system: {}, disks: [], services: [], containers: [], alerts: [] };
const alertNodes = new Map();
const WORKSPACE_STORAGE_KEY = "homelab.workspace.active";
const SIDEBAR_COLLAPSED_STORAGE_KEY = "homelab.sidebar.collapsed";
const THEME_STORAGE_KEY = "homelab.theme";
const WORKSPACE_ORDER = ["overview", "services", "containers", "nodes", "history", "logs", "alerts", "topology"];
const WORKSPACES = new Set(WORKSPACE_ORDER);
const ROUTE_RANGES = new Set(["1h", "6h", "24h", "7d", "30d", "90d"]);
const ROUTE_KINDS = new Set(["system", "container", "service"]);
const ROUTE_STATES = {
  services: new Set(["all", "attention", "up", "unknown"]),
  containers: new Set(["all", "attention", "running", "stopped"]),
  alerts: new Set(["firing"]),
};
let routeNodeSelectionReady = false;
let activeAlertSource = "";
let workspacePreferences = { workspaceOrder: [...WORKSPACE_ORDER], hiddenWorkspaces: [] };
const overviewSummary = {
  services: { total: 0, up: 0, down: 0, unknown: 0 },
  containers: { total: 0, running: 0, issue: 0, stopped: 0 },
};

const elements = Object.fromEntries([
  "system-card", "system-title", "system-hostname", "system-status", "header-hostname", "freshness", "freshness-text",
  "offline-banner", "offline-message", "session-user", "session-role", "demo-badge", "cpu-percent",
  "cpu-progress", "cpu-detail", "ram-percent", "ram-progress", "ram-detail", "disk-percent",
  "disk-progress", "disk-detail", "disk-device", "disk-warning", "network-interface", "network-down",
  "network-up", "uptime", "processes", "load-average", "io-read", "io-write", "io-read-progress",
  "io-write-progress", "alerts-list", "alerts-count", "alerts-empty", "alerts-partial",
  "services-stale", "alerts-card", "overview-health", "overview-health-detail",
  "overview-connection", "overview-updated", "overview-services", "overview-services-detail",
  "overview-containers", "overview-containers-detail", "overview-attention-total", "overview-attention-detail", "dashboard-status",
  "snapshot-partial",
].map((id) => [id, document.getElementById(id)]));

function toast(message, level = "info") {
  const region = document.getElementById("toast-region");
  const item = document.createElement("div");
  item.className = "toast";
  item.dataset.level = level;
  const content = document.createElement("span");
  content.className = "toast-message";
  content.textContent = String(message);
  const progress = document.createElement("span");
  progress.className = "toast-progress";
  progress.setAttribute("aria-hidden", "true");
  item.append(content, progress);
  region.append(item);
  window.setTimeout(() => item.remove(), 3000);
}

function setStateText(element, value) { setText(element, value, true); }
function setMetricText(element, value) { setText(element, value, false); }

const terminal = createTerminalController({ api, demo, toast });
const containersController = createContainersController({ terminal, api, toast, onLifecycle: () => refreshSnapshot() });
const servicesController = createServicesController({
  api,
  toast,
  onChanged: (services, summary) => {
    latestServices = services;
    latestOverviewData = { ...latestOverviewData, services };
    overviewSummary.services = summary;
    if (selectedNodeId === "local") historyController.setResources(latestSelectedContainers, latestServices);
    logsController.setResources(latestSelectedContainers, latestServices);
    updateOverview();
  },
});
let operationsController = null;
const historyController = createHistoryController({
  api,
  demo,
  toast,
  onRangeChange: (range, shouldRefresh) => operationsController?.setTimelineRange(range, shouldRefresh),
});
const logsController = createLogsController({ api, demo, toast });
const overviewController = createOverviewController({
  api,
  demo,
  toast,
  onNavigate: (route) => navigateTo(route),
  onOpenContainerTerminal: (container, mode, invoker) => containersController.open(container, mode, invoker),
  onWidgetPreferences: applyOverviewWidgetIntent,
});
const alertsController = createAlertsController({ api, demo, toast });
const nodesController = createNodesController({
  api,
  demo,
  toast,
  onSelect: selectNode,
});
operationsController = createOperationsController({
  api,
  demo,
  toast,
  onSelectNode: (nodeID) => nodesController.setSelected(nodeID),
  onOpenServices: () => document.querySelector("[data-workspace='services']")?.click(),
});
operationsController.setTimelineRange(historyController.range(), false);
const settingsController = createSettingsController({
  api,
  demo,
  toast,
  onApplied: async () => {
    await hydratePreferences();
    await Promise.allSettled([refreshSnapshot(), alertsController.refresh()]);
  },
  onWorkspacePreferencesApplied: async (next) => {
    if (demo) {
      storeValue("homelab.demo.workspace-preferences", JSON.stringify(next));
      applyWorkspacePreferences(next);
      return workspacePreferences;
    }
    const updated = await api.updatePreferences(next);
    applyWorkspacePreferences(updated);
    return workspacePreferences;
  },
  onOverviewPreferencesApplied: async (next) => {
    if (!sessionAuthenticated) throw new Error("Sign in to save overview widget preferences.");
    const update = {
      hiddenOverviewWidgets: next.hiddenOverviewWidgets,
      overviewWidgetSizes: next.overviewWidgetSizes,
    };
    if (demo) {
      const merged = { ...workspacePreferences, ...update };
      storeValue("homelab.demo.workspace-preferences", JSON.stringify(merged));
      applyWorkspacePreferences(merged);
      return workspacePreferences;
    }
    const updated = await api.updatePreferences(update);
    applyWorkspacePreferences(updated);
    return workspacePreferences;
  },
});

function storageValue(key) {
  try { return localStorage.getItem(key) || ""; } catch { return ""; }
}

function storeValue(key, value) {
  try { localStorage.setItem(key, value); } catch { /* Storage is optional. */ }
}

function normalizeWorkspacePreferences(preferences = {}) {
  const seen = new Set();
  const order = [];
  for (const workspace of Array.isArray(preferences.workspaceOrder) ? preferences.workspaceOrder : []) {
    if (!WORKSPACES.has(workspace) || seen.has(workspace)) continue;
    seen.add(workspace);
    order.push(workspace);
  }
  for (const workspace of WORKSPACE_ORDER) if (!seen.has(workspace)) order.push(workspace);
  const hidden = [...new Set(Array.isArray(preferences.hiddenWorkspaces) ? preferences.hiddenWorkspaces : [])]
    .filter((workspace) => workspace !== "overview" && WORKSPACES.has(workspace));
  const overview = normalizeOverviewPreferences(preferences);
  return { workspaceOrder: order, hiddenWorkspaces: hidden, ...overview };
}

function themeName() {
  return document.documentElement.classList.contains("theme-light") ? "light" : "dark";
}

function preferredTheme() {
  const saved = storageValue(THEME_STORAGE_KEY);
  return saved === "light" || saved === "dark" ? saved : themeName();
}

function applyTheme(next, { persist = false } = {}) {
  const theme = next === "light" ? "light" : "dark";
  document.documentElement.classList.toggle("theme-light", theme === "light");
  document.documentElement.classList.toggle("theme-dark", theme === "dark");
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", theme === "light" ? "#f6f5f3" : "#101010");
  const toggle = document.getElementById("theme-toggle");
  if (toggle) {
    const toLight = theme === "dark";
    toggle.setAttribute("aria-pressed", String(theme === "light"));
    toggle.setAttribute("aria-label", toLight ? "Switch to light theme" : "Switch to dark theme");
    toggle.title = toLight ? "Switch to light theme" : "Switch to dark theme";
  }
  if (persist) storeValue(THEME_STORAGE_KEY, theme);
  charts.updateTheme?.();
  historyController.updateTheme?.();
  terminal.updateTheme?.();
}

function applyWorkspacePreferences(preferences) {
  workspacePreferences = normalizeWorkspacePreferences(preferences);
  overviewController.applyPreferences(workspacePreferences);
  workspaceNavigation?.applyPreferences(workspacePreferences);
  settingsController.setWorkspacePreferences(workspacePreferences);
  const visibleRoute = normalizeRoute(currentRoute);
  if (visibleRoute.workspace !== currentRoute.workspace) workspaceNavigation?.navigate(visibleRoute, { replace: true, focus: false });
  return workspacePreferences;
}

async function applyOverviewWidgetIntent(intent = {}) {
  const previous = workspacePreferences;
  const next = {
    ...previous,
    hiddenOverviewWidgets: [...previous.hiddenOverviewWidgets],
    overviewWidgetSizes: { ...previous.overviewWidgetSizes },
  };
  if (intent.reset) {
    next.hiddenOverviewWidgets = [...OVERVIEW_WIDGET_DEFAULT_HIDDEN];
    next.overviewWidgetSizes = { ...OVERVIEW_WIDGET_DEFAULT_SIZES };
  } else if (OVERVIEW_WIDGET_ORDER.includes(intent.widgetID)) {
    if (intent.hidden && intent.widgetID !== "overview-attention") next.hiddenOverviewWidgets.push(intent.widgetID);
    if (intent.hidden === false) next.hiddenOverviewWidgets = next.hiddenOverviewWidgets.filter((id) => id !== intent.widgetID);
    if (["small", "medium", "full"].includes(intent.size)) next.overviewWidgetSizes[intent.widgetID] = intent.size;
  }
  applyWorkspacePreferences(next);
  try {
    if (!sessionAuthenticated) throw new Error("Sign in to save overview widget preferences.");
    const update = { hiddenOverviewWidgets: next.hiddenOverviewWidgets, overviewWidgetSizes: next.overviewWidgetSizes };
    if (demo) {
      const merged = { ...workspacePreferences, ...update };
      storeValue("homelab.demo.workspace-preferences", JSON.stringify(merged));
      applyWorkspacePreferences(merged);
      return workspacePreferences;
    }
    const updated = await api.updatePreferences(update);
    applyWorkspacePreferences(updated);
    return workspacePreferences;
  } catch (error) {
    applyWorkspacePreferences(previous);
    throw error;
  }
}

function safeRouteValue(value, maxLength = 160) {
  return String(value || "").replace(/[\u0000-\u001f\u007f]/g, "").trim().slice(0, maxLength);
}

function normalizeRoute(route = {}) {
  const requestedWorkspace = WORKSPACES.has(route.workspace) ? route.workspace : "overview";
  const workspace = workspacePreferences.hiddenWorkspaces.includes(requestedWorkspace) ? "overview" : requestedWorkspace;
  const normalized = { workspace };
  const state = safeRouteValue(route.state, 24).toLowerCase();
  if (ROUTE_STATES[workspace]?.has(state)) normalized.state = state;
  const query = safeRouteValue(route.query ?? route.q);
  if (["services", "containers"].includes(workspace) && query) normalized.query = query;
  const node = safeRouteValue(route.node, 128);
  if (node && /^[A-Za-z0-9_.:-]+$/.test(node) && ["overview", "containers", "nodes", "history", "logs", "alerts", "topology"].includes(workspace)) normalized.node = node;
  const range = safeRouteValue(route.range, 8).toLowerCase();
  if (workspace === "history" && ROUTE_RANGES.has(range)) normalized.range = range;
  const kind = safeRouteValue(route.kind, 16).toLowerCase();
  if (workspace === "history" && ROUTE_KINDS.has(kind)) normalized.kind = kind;
  const resource = safeRouteValue(route.resource, 200);
  if (workspace === "history" && resource && ["container", "service"].includes(normalized.kind)) normalized.resource = resource;
  const source = safeRouteValue(route.source, 200);
  if (workspace === "alerts" && source) normalized.source = source;
  return normalized;
}

function routeFromLocation() {
  const raw = window.location.hash.replace(/^#/, "");
  if (!raw) return normalizeRoute({ workspace: storageValue(WORKSPACE_STORAGE_KEY) || "overview" });
  const separator = raw.indexOf("?");
  let workspace = "overview";
  try { workspace = decodeURIComponent(separator >= 0 ? raw.slice(0, separator) : raw); } catch { /* Invalid hashes fall back to Overview. */ }
  const query = new URLSearchParams(separator >= 0 ? raw.slice(separator + 1) : "");
  return normalizeRoute({
    workspace,
    state: query.get("state"),
    query: query.get("q"),
    node: query.get("node"),
    range: query.get("range"),
    kind: query.get("kind"),
    resource: query.get("resource"),
    source: query.get("source"),
  });
}

function routeHash(route) {
  const normalized = normalizeRoute(route);
  const query = new URLSearchParams();
  if (normalized.state) query.set("state", normalized.state);
  if (normalized.query) query.set("q", normalized.query);
  if (normalized.node) query.set("node", normalized.node);
  if (normalized.range) query.set("range", normalized.range);
  if (normalized.kind) query.set("kind", normalized.kind);
  if (normalized.resource) query.set("resource", normalized.resource);
  if (normalized.source) query.set("source", normalized.source);
  return `#${encodeURIComponent(normalized.workspace)}${query.size ? `?${query}` : ""}`;
}

let currentRoute = routeFromLocation();

function navigateTo(route, options) {
  workspaceNavigation?.navigate(route, options);
}

function createWorkspaceNavigation() {
  const dashboard = document.getElementById("dashboard");
  const sidebar = document.getElementById("workspace-sidebar");
  const openButton = document.getElementById("sidebar-open");
  const collapseButton = document.getElementById("sidebar-collapse");
  const backdrop = document.getElementById("sidebar-backdrop");
  const drawerBackground = [
    document.querySelector(".app-header"),
    dashboard,
    document.getElementById("terminal-panel"),
  ].filter(Boolean);
  const workspaceButtons = [...document.querySelectorAll("[data-workspace]")];
  const workspacePanels = [...document.querySelectorAll("[data-workspace-panel]")];
  const workbenchLabel = sidebar.querySelector(".sidebar-group-label");
  const drawerQuery = window.matchMedia("(max-width: 899px)");
  let activeWorkspace = currentRoute.workspace;
  let drawerOpener = null;

  function isDrawer() {
    return drawerQuery.matches;
  }

  function syncDrawerControls() {
    const open = isDrawer() && document.body.classList.contains("sidebar-drawer-open");
    openButton.setAttribute("aria-expanded", String(isDrawer() && open));
    backdrop.hidden = !open;
    sidebar.toggleAttribute("inert", isDrawer() && !open);
    sidebar.setAttribute("aria-hidden", String(isDrawer() && !open));
    if (open) {
      sidebar.setAttribute("role", "dialog");
      sidebar.setAttribute("aria-modal", "true");
    } else {
      sidebar.removeAttribute("role");
      sidebar.removeAttribute("aria-modal");
    }
    for (const element of drawerBackground) element.toggleAttribute("inert", open);
  }

  function setCollapsed(next) {
    const collapsed = Boolean(next);
    document.body.classList.toggle("sidebar-collapsed", collapsed);
    collapseButton.setAttribute("aria-pressed", String(collapsed));
    collapseButton.setAttribute("aria-label", collapsed ? "Expand sidebar" : "Collapse sidebar");
    collapseButton.title = collapsed ? "Expand sidebar" : "Collapse sidebar";
    storeValue(SIDEBAR_COLLAPSED_STORAGE_KEY, collapsed ? "true" : "false");
  }

  function closeDrawer({ restoreFocus = false } = {}) {
    if (!document.body.classList.contains("sidebar-drawer-open")) return;
    document.body.classList.remove("sidebar-drawer-open");
    syncDrawerControls();
    if (restoreFocus && drawerOpener?.isConnected) drawerOpener.focus({ preventScroll: true });
    drawerOpener = null;
  }

  function openDrawer() {
    if (!isDrawer()) return;
    drawerOpener = document.activeElement;
    document.body.classList.add("sidebar-drawer-open");
    syncDrawerControls();
    sidebar.querySelector("[data-workspace]")?.focus({ preventScroll: true });
  }

  function focusWorkspace(workspace) {
    const panel = workspacePanels.find((item) => item.dataset.workspacePanel === workspace);
    const target = panel?.querySelector("h2[tabindex='-1']") || panel?.querySelector("[data-workspace-focus]");
    dashboard.scrollTop = 0;
    window.requestAnimationFrame(() => target?.focus({ preventScroll: true }));
  }

  function selectWorkspace(next, { focus = false } = {}) {
    const workspace = WORKSPACES.has(next) ? next : "overview";
    activeWorkspace = workspace;
    for (const panel of workspacePanels) {
      const active = panel.dataset.workspacePanel === workspace;
      panel.hidden = !active;
      panel.dataset.active = String(active);
    }
    for (const button of workspaceButtons) {
      const active = button.dataset.workspace === workspace;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-current", active ? "page" : "false");
    }
    storeValue(WORKSPACE_STORAGE_KEY, workspace);
    if (workspace === "history") window.requestAnimationFrame(() => historyController.activate());
    if (workspace === "logs") window.requestAnimationFrame(() => logsController.activate());
    if (workspace === "nodes" || workspace === "topology") window.requestAnimationFrame(() => operationsController.activate(workspace));
    if (workspace === "overview") window.requestAnimationFrame(() => overviewController.activate());
    else overviewController.deactivate();
    if (focus) focusWorkspace(workspace);
  }

  function applyPreferences(preferences) {
    const normalized = normalizeWorkspacePreferences(preferences);
    const hidden = new Set(normalized.hiddenWorkspaces);
    for (const workspace of normalized.workspaceOrder) {
      const button = workspaceButtons.find((item) => item.dataset.workspace === workspace);
      if (!button) continue;
      sidebar.querySelector(".workspace-nav")?.insertBefore(button, workbenchLabel || null);
      button.hidden = hidden.has(workspace);
    }
    if (hidden.has(activeWorkspace)) {
      const route = normalizeRoute({ ...currentRoute, workspace: activeWorkspace });
      navigate(route, { replace: true, focus: false });
    }
  }

  function focusTerminal() {
    closeDrawer();
    const panel = document.getElementById("terminal-panel");
    if (panel.classList.contains("is-collapsed")) document.getElementById("terminal-toggle").click();
    window.requestAnimationFrame(() => {
      terminal.fit();
      document.querySelector("#terminal .xterm-helper-textarea")?.focus({ preventScroll: true });
    });
  }

  function focusableDrawerElements() {
    return [...sidebar.querySelectorAll("button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])")]
      .filter((element) => !element.hidden && element.offsetParent !== null);
  }

  function applyRoute(route, { focus = false, applyNode = routeNodeSelectionReady } = {}) {
    const normalized = normalizeRoute(route);
    currentRoute = normalized;
    if (applyNode && normalized.node && nodesController.selectedNode() !== normalized.node) nodesController.setSelected(normalized.node);
    selectWorkspace(normalized.workspace, { focus });
    if (normalized.workspace === "services") servicesController.applyRoute({ state: normalized.state, query: normalized.query });
    if (normalized.workspace === "containers") containersController.applyRoute({ state: normalized.state, query: normalized.query });
    if (normalized.workspace === "history") historyController.applyRoute({ ...normalized, refresh: routeNodeSelectionReady });
    if (normalized.workspace === "alerts") applyAlertRoute(normalized);
  }

  function navigate(route, { replace = false, focus = true } = {}) {
    const normalized = normalizeRoute(route);
    const hash = routeHash(normalized);
    const method = replace ? "replaceState" : "pushState";
    window.history[method](null, "", `${window.location.pathname}${window.location.search}${hash}`);
    lastBrowserRoute = window.location.href;
    applyRoute(normalized, { focus });
  }

  workspaceButtons.forEach((button) => button.addEventListener("click", () => {
    navigate({ workspace: button.dataset.workspace }, { focus: true });
    closeDrawer();
  }));
  sidebar.querySelector("[data-sidebar-action='terminal']")?.addEventListener("click", focusTerminal);
  collapseButton.addEventListener("click", () => setCollapsed(!document.body.classList.contains("sidebar-collapsed")));
  openButton.addEventListener("click", () => {
    if (isDrawer()) {
      if (document.body.classList.contains("sidebar-drawer-open")) closeDrawer({ restoreFocus: true });
      else openDrawer();
      return;
    }
    setCollapsed(!document.body.classList.contains("sidebar-collapsed"));
  });
  backdrop.addEventListener("click", () => closeDrawer({ restoreFocus: true }));
  document.addEventListener("keydown", (event) => {
    if (!isDrawer() || !document.body.classList.contains("sidebar-drawer-open")) return;
    if (event.key === "Escape") {
      event.preventDefault();
      closeDrawer({ restoreFocus: true });
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = focusableDrawerElements();
    if (!focusable.length) return;
    const first = focusable[0];
    const last = focusable.at(-1);
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  });
  const onDrawerChange = () => {
    if (!isDrawer()) closeDrawer();
    syncDrawerControls();
  };
  if (typeof drawerQuery.addEventListener === "function") drawerQuery.addEventListener("change", onDrawerChange);
  else drawerQuery.addListener(onDrawerChange);

  setCollapsed(storageValue(SIDEBAR_COLLAPSED_STORAGE_KEY) === "true");
  applyPreferences(workspacePreferences);
  selectWorkspace(activeWorkspace);
  syncDrawerControls();

  return { selectWorkspace, focusTerminal, navigate, applyRoute, applyPreferences };
}

const workspaceNavigation = createWorkspaceNavigation();
if (window.location.hash !== routeHash(currentRoute)) {
  window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}${routeHash(currentRoute)}`);
}
workspaceNavigation.applyRoute(currentRoute, { applyNode: false });
let lastBrowserRoute = window.location.href;
function restoreBrowserRoute() {
  if (lastBrowserRoute === window.location.href) return;
  lastBrowserRoute = window.location.href;
  const route = routeFromLocation();
  const normalizedHash = routeHash(route);
  if (window.location.hash !== normalizedHash) window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}${normalizedHash}`);
  lastBrowserRoute = window.location.href;
  workspaceNavigation.applyRoute(route, { focus: true });
}
window.addEventListener("popstate", restoreBrowserRoute);
window.addEventListener("hashchange", restoreBrowserRoute);

document.getElementById("overview-attention-kpi")?.addEventListener("click", () => navigateTo({ workspace: "alerts", node: selectedNodeId, state: "firing" }));
document.getElementById("overview-services-kpi")?.addEventListener("click", () => navigateTo({ workspace: "services", state: "all" }));
document.getElementById("overview-containers-kpi")?.addEventListener("click", () => navigateTo({ workspace: "containers", node: selectedNodeId, state: "all" }));

function createKeyboardShortcuts() {
  const dialog = document.getElementById("keyboard-shortcuts-dialog");
  const closeButtons = [document.getElementById("keyboard-shortcuts-close"), document.getElementById("keyboard-shortcuts-dismiss")];
  const workspaceByKey = { o: "overview", s: "services", c: "containers", n: "nodes", h: "history", l: "logs", a: "alerts", t: "topology" };
  let navigationLeadUntil = 0;

  function isTyping(target) {
    if (!(target instanceof Element)) return false;
    return Boolean(target.closest("input, textarea, select, [contenteditable='true'], .xterm"));
  }

  function hasOtherDialogOpen() {
    return [...document.querySelectorAll("dialog[open]")].some((item) => item !== dialog);
  }

  function open() {
    if (!dialog.open) dialog.showModal();
    window.requestAnimationFrame(() => document.getElementById("keyboard-shortcuts-close")?.focus());
  }

  for (const button of closeButtons) button?.addEventListener("click", () => dialog.close());
  dialog.addEventListener("click", (event) => { if (event.target === dialog) dialog.close(); });
  document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented || event.metaKey || event.altKey || isTyping(event.target)) return;
    if (event.ctrlKey && event.key === "`") {
      event.preventDefault();
      workspaceNavigation.focusTerminal();
      return;
    }
    if (event.ctrlKey) return;
    if (hasOtherDialogOpen()) return;
    if (event.key === "?") {
      event.preventDefault();
      open();
      return;
    }
    if (dialog.open) return;
    if (event.key === "/") {
      let handled = false;
      if (currentRoute.workspace === "services") {
        servicesController.focusFilter();
        handled = true;
      } else if (currentRoute.workspace === "containers") {
        containersController.focusFilter();
        handled = true;
      } else if (currentRoute.workspace === "logs") {
        const search = document.getElementById("logs-search");
        if (search) {
          logsController.focusSearch?.();
          handled = true;
        }
      } else {
        const target = document.querySelector("[data-workspace-panel]:not([hidden]) [data-workspace-filter]");
        if (target) {
          target.focus({ preventScroll: true });
          handled = true;
        }
      }
      if (handled) event.preventDefault();
      return;
    }
    const key = event.key.toLowerCase();
    if (key === "g") {
      event.preventDefault();
      navigationLeadUntil = Date.now() + 900;
      return;
    }
    if (Date.now() <= navigationLeadUntil && workspaceByKey[key]) {
      event.preventDefault();
      navigationLeadUntil = 0;
      workspaceNavigation.navigate({ workspace: workspaceByKey[key] }, { focus: true });
      return;
    }
    navigationLeadUntil = 0;
  });
}

createKeyboardShortcuts();

function setSystemBadge(label, state) {
  const dot = document.createElement("span");
  dot.className = "status-dot";
  dot.setAttribute("aria-hidden", "true");
  elements["system-status"].className = `badge badge-${state}`;
  elements["system-status"].replaceChildren(dot, document.createTextNode(label));
}

function announce(message) {
  setMetricText(elements["dashboard-status"], message);
}

function setOverviewHealth(label, state, detail) {
  const dot = document.createElement("span");
  dot.className = "status-dot";
  dot.setAttribute("aria-hidden", "true");
  elements["overview-health"].dataset.state = state;
  elements["overview-health"].replaceChildren(dot, document.createTextNode(label));
  setMetricText(elements["overview-health-detail"], detail);
}

function updateOverview() {
  const services = overviewSummary.services;
  const containers = overviewSummary.containers;
  const attention = overviewController.update({
    ...latestOverviewData,
    services: latestOverviewData.services,
    containers: latestOverviewData.containers,
    alerts: latestOverviewData.alerts,
    remote: selectedNodeId !== "local",
    admin: sessionAdmin,
    partial: snapshotPartial,
    connection: connectionState,
  });
  const monitoredServices = Math.max(0, services.total - services.unknown);
  setMetricText(
    elements["overview-services"],
    services.total && monitoredServices === 0 ? `${services.total} UNMONITORED` : `${services.up} / ${monitoredServices} UP`,
  );
  setMetricText(
    elements["overview-services-detail"],
    services.down ? `${services.down} need attention` : services.unknown ? `${services.unknown} without a health probe` : services.total ? "All probes responding" : "No services configured",
  );
  setMetricText(elements["overview-containers"], `${containers.running} / ${containers.total} RUNNING`);
  setMetricText(
    elements["overview-containers-detail"],
    containers.issue ? `${containers.issue} with runtime issues` : containers.stopped ? `${containers.stopped} stopped` : containers.total ? "Podman inventory healthy" : "No containers reported",
  );
  setMetricText(elements["overview-attention-total"], `${attention.total} ACTIVE`);
  setMetricText(
    elements["overview-attention-detail"],
    attention.critical ? `${attention.critical} critical` : attention.warning ? `${attention.warning} warning${attention.warning === 1 ? "" : "s"}` : "No monitored incidents",
  );
  document.getElementById("overview-attention-kpi")?.setAttribute("aria-label", attention.total ? `Open ${attention.total} active monitored incidents` : "Open active alerts");

  if (["stale", "offline"].includes(connectionState) && latestCollectedAt) {
    setOverviewHealth("STALE", "degraded", "Last known data is preserved while metrics reconnect");
  } else if (connectionState !== "online") {
    setOverviewHealth("WAITING", "waiting", "Connecting to the dashboard");
  } else if (snapshotPartial) {
    setOverviewHealth("PARTIAL", "degraded", "The latest frame was size-limited; omitted inventory is not treated as healthy");
  } else if (attention.critical) {
    setOverviewHealth("ACTION NEEDED", "down", `${attention.total} monitored ${attention.total === 1 ? "issue needs" : "issues need"} attention`);
  } else if (attention.warning) {
    setOverviewHealth("DEGRADED", "degraded", `${attention.warning} warning${attention.warning === 1 ? "" : "s"} active`);
  } else {
    setOverviewHealth("HEALTHY", "up", services.unknown ? `${services.unknown} service${services.unknown === 1 ? " has" : "s have"} no health probe` : "All monitored systems operational");
  }
}

function setSnapshotCompleteness(envelope) {
  snapshotPartial = envelope?.truncated === true;
  snapshotPartialSources = Array.isArray(envelope?.truncatedSources) ? envelope.truncatedSources.map(String) : [];
  elements["snapshot-partial"].hidden = !snapshotPartial;
  elements["snapshot-partial"].title = snapshotPartial
    ? `Snapshot was truncated${snapshotPartialSources.length ? `: ${snapshotPartialSources.join(", ")}` : " to fit the frame limit"}`
    : "";
}

function setConnectionState(state, detail = {}) {
  const chip = elements.freshness;
  const text = elements["freshness-text"];
  const banner = elements["offline-banner"];
  const message = elements["offline-message"];
  const hasData = Boolean(latestCollectedAt);
  const stale = ["stale", "offline"].includes(state) && hasData;
  connectionState = state;
  document.body.classList.toggle("is-stale", ["stale", "offline"].includes(state) && hasData);
  document.body.classList.toggle("is-offline", state === "offline");
  const servicesStale = ["stale", "offline"].includes(localConnectionState) && Boolean(latestLocalEnvelope);
  elements["services-stale"].hidden = !servicesStale;

  if (state === "online") {
    latestCollectedAt = detail.collectedAt || latestCollectedAt || new Date().toISOString();
    chip.dataset.state = "online";
    text.textContent = "LIVE";
    banner.hidden = true;
    setSystemBadge("ONLINE", "up");
  } else if (state === "connected") {
    chip.dataset.state = "connecting";
    text.textContent = "SYNCING";
    banner.hidden = hasData;
    message.textContent = "Connected. Waiting for a valid metrics snapshot…";
  } else if (state === "connecting") {
    chip.dataset.state = "connecting";
    text.textContent = "CONNECTING";
    banner.hidden = hasData;
    message.textContent = "Connecting to the metrics stream…";
    if (!hasData) setSystemBadge("WAITING", "muted");
  } else if (state === "stale") {
    chip.dataset.state = "offline";
    text.textContent = "STALE";
    banner.hidden = false;
    message.textContent = "Metrics are stale. Showing the last valid snapshot.";
    setSystemBadge("STALE", "degraded");
  } else {
    chip.dataset.state = "offline";
    text.textContent = "OFFLINE";
    banner.hidden = false;
    message.textContent = hasData ? "Connection lost. Retrying in 3 seconds; last snapshot preserved." : "Unable to reach the dashboard server. Retrying…";
    setSystemBadge("OFFLINE", "down");
    if (!hasData) {
      setMetricText(elements["system-hostname"], "Unable to reach server");
      setMetricText(elements["cpu-detail"], "Waiting for the dashboard backend");
    }
  }

  const connectionLabel = state === "online" ? (demo ? "DEMO LIVE" : "LIVE") : state === "connected" ? "SYNCING" : state.toUpperCase();
  chip.setAttribute("aria-label", `Metrics stream: ${connectionLabel.toLowerCase()}`);
  elements["overview-connection"].dataset.state = state;
  setStateText(elements["overview-connection"], connectionLabel);
  setStateText(elements["overview-updated"], latestCollectedAt ? `Updated ${timeAgo(latestCollectedAt)}` : "No snapshot yet");
  if (state !== lastAnnouncedConnection) {
    const messages = {
      online: "Metrics stream is live.",
      connected: "Metrics stream connected. Waiting for a snapshot.",
      connecting: "Connecting to the metrics stream.",
      stale: "Metrics are stale. Last known data is displayed.",
      offline: "Metrics stream is offline. Reconnection is in progress.",
    };
    announce(messages[state] || "Metrics stream state changed.");
    lastAnnouncedConnection = state;
  }
  updateOverview();
}

function memoryValue(memory, explicit, legacy) {
  if (memory?.[explicit] != null) return number(memory[explicit]);
  return number(memory?.[legacy]) * 1024;
}

function diskValue(disk, explicit, legacy) {
  if (disk?.[explicit] != null) return number(disk[explicit]);
  return number(disk?.[legacy]) * 1024 * 1024;
}

function normalize(data) {
  const system = data?.system || {};
  let disks = data?.disks || system.disks || [];
  if (!Array.isArray(disks)) {
    disks = Object.entries(disks).map(([mountPoint, disk]) => ({ mountPoint, ...disk }));
  }
  if (!disks.length && system.disk && typeof system.disk === "object") {
    disks = Object.entries(system.disk).map(([mountPoint, disk]) => ({ mountPoint, ...disk }));
  }
  return {
    system,
    disks,
    network: data?.network || system.network || {},
    services: data?.services || [],
    containers: data?.containers || [],
    alerts: data?.alerts || [],
  };
}

function renderSelectedSnapshot(payload, state = "online") {
  const envelope = payload || {};
  setSnapshotCompleteness(envelope);
  const data = normalize(unwrapSnapshot(envelope));
  latestCollectedAt = envelope.collectedAt || new Date().toISOString();
  renderSystem(data.system, data.disks, data.network);
  if (selectedNodeId === "local") latestServices = data.services;
  latestSelectedContainers = data.containers;
  const selectedServices = selectedNodeId === "local" ? data.services : latestServices;
  latestOverviewData = { system: data.system, disks: data.disks, services: selectedServices, containers: data.containers, alerts: data.alerts };
  overviewSummary.services = servicesController.render(selectedServices);
  overviewSummary.containers = containersController.render(data.containers);
  operationsController.setServices(selectedServices);
  historyController.setResources(data.containers, data.services);
  logsController.setResources(data.containers, selectedServices);
  renderAlerts(data.alerts);
  setConnectionState(state, { collectedAt: latestCollectedAt });
}

function renderLocalSnapshot(payload) {
  latestLocalEnvelope = payload;
  latestServices = normalize(unwrapSnapshot(payload)).services;
  operationsController.setSnapshot(payload);
  if (demo) localConnectionState = "online";
  if (selectedNodeId === "local") renderSelectedSnapshot(payload, "online");
  else {
    overviewSummary.services = servicesController.render(latestServices);
    latestOverviewData = { ...latestOverviewData, services: latestServices };
    updateOverview();
  }
}

function renderUnavailableNode(state) {
  const name = state?.node?.displayName || state?.node?.hostname || selectedNodeId;
  latestCollectedAt = state?.lastSeenAt || state?.node?.lastSeenAt || null;
  setSnapshotCompleteness(null);
  setConnectionState("offline");
  setMetricText(elements["system-hostname"], name || "Remote node");
  setMetricText(elements["header-hostname"], state?.node?.hostname || name || "remote");
  setMetricText(elements["cpu-percent"], "—");
  setMetricText(elements["cpu-detail"], "No remote metrics snapshot received");
  setMetricText(elements["ram-percent"], "—");
  setMetricText(elements["ram-detail"], "Waiting for the node agent");
  elements["ram-percent"].classList.remove("is-over-limit");
  setMetricText(elements["disk-percent"], "—");
  setMetricText(elements["disk-detail"], "—");
  setMetricText(elements["disk-device"], "—");
  elements["disk-warning"].hidden = true;
  elements["disk-progress"].dataset.level = "";
  setMetricText(elements["network-interface"], "—");
  setMetricText(elements["network-down"], "—");
  setMetricText(elements["network-up"], "—");
  setMetricText(elements.uptime, "—");
  setMetricText(elements.processes, "—");
  setMetricText(elements["load-average"], "—");
  setMetricText(elements["io-read"], "Idle");
  setMetricText(elements["io-write"], "Idle");
  setProgress(elements["cpu-progress"], 0);
  setProgress(elements["ram-progress"], 0);
  setProgress(elements["disk-progress"], 0);
  setProgress(elements["io-read-progress"], 0);
  setProgress(elements["io-write-progress"], 0);
  overviewSummary.services = servicesController.render(latestServices);
  overviewSummary.containers = containersController.render([]);
  latestSelectedContainers = [];
  latestOverviewData = { system: {}, disks: [], services: latestServices, containers: [], alerts: [] };
  historyController.setResources([], []);
  logsController.setResources([], latestServices);
  renderAlerts([]);
  updateOverview();
}

function selectNode({ id, state }) {
  const nextNodeId = id || "local";
  const nodeChanged = !nodeSelectionInitialized || selectedNodeId !== nextNodeId;
  nodeSelectionInitialized = true;
  selectedNodeId = nextNodeId;
  const remote = selectedNodeId !== "local";
  document.body.classList.toggle("remote-node", remote);
  const nodeLabel = state?.node?.displayName || state?.node?.hostname || selectedNodeId;
  containersController.setNode(selectedNodeId);
  overviewController.setNode(selectedNodeId, nodeLabel);
  terminal.setNode?.(selectedNodeId, nodeLabel);
  logsController.setNode(selectedNodeId);
  if (nodeChanged) {
    charts.reset();
    historyController.setNode(selectedNodeId);
    alertsController.setNode(selectedNodeId);
  }
  terminal.setHostShellCapability?.(sessionHostShellCapability);
  operationsController.setNode(selectedNodeId);
  if (!remote) {
    if (latestLocalEnvelope) renderSelectedSnapshot(latestLocalEnvelope, localConnectionState === "online" ? "online" : localConnectionState);
    else refreshSnapshot().catch(() => setConnectionState("offline"));
    return;
  }
  if (state?.snapshot) {
    renderSelectedSnapshot(state.snapshot, state.online ? "online" : "stale");
  } else {
    renderUnavailableNode(state);
  }
}

function renderSystem(system, disks, network) {
  const cpu = system.cpu || {};
  const memory = system.memory || {};
  const hostname = system.hostname || "unknown-host";
  const cpuUsage = number(cpu.usagePercent ?? cpu.percent);
  const cpuCores = number(cpu.cores);
  const frequency = number(cpu.frequencyMHz ?? cpu.freq);
  const temperature = cpu.temperatureCelsius ?? cpu.temp;
  const totalMemory = memoryValue(memory, "totalBytes", "total");
  const usedMemory = memoryValue(memory, "usedBytes", "used");
  const swapTotal = memoryValue(memory, "swapTotalBytes", "swapTotal");
  const swapUsed = memoryValue(memory, "swapUsedBytes", "swapUsed");
  const ramUsage = totalMemory > 0 ? (usedMemory / totalMemory) * 100 : 0;
  const disk = disks.find((item) => item.mountPoint === "/") || disks[0] || {};
  const diskTotal = diskValue(disk, "totalBytes", "total");
  const diskUsed = diskValue(disk, "usedBytes", "used");
  const diskUsage = number(disk.usagePercent ?? disk.percent ?? (diskTotal ? diskUsed / diskTotal * 100 : 0));
  const readRate = number(disk.readBytesPerSecond ?? disk.readBps);
  const writeRate = number(disk.writeBytesPerSecond ?? disk.writeBps);
  const maxIo = Math.max(readRate, writeRate, 1);

  elements["system-card"].setAttribute("aria-busy", "false");
  setMetricText(elements["system-hostname"], hostname);
  setMetricText(elements["header-hostname"], hostname);
  setMetricText(elements["cpu-percent"], percent(cpuUsage, 1));
  const cpuMaximum = cpuUsage > 100 ? Math.max(100, cpuCores * 100, cpuUsage) : 100;
  setProgress(elements["cpu-progress"], cpuUsage, cpuMaximum);
  const temperatureLabel = Number.isFinite(Number(temperature)) ? `${Number(temperature).toFixed(0)}°C${Number(temperature) > 80 ? " · HOT" : ""}` : "temp n/a";
  setMetricText(elements["cpu-detail"], `${frequency ? `${frequency.toFixed(0)} MHz` : "freq n/a"} · ${cpuCores || "—"} cores · ${temperatureLabel}`);

  const memoryOverLimit = totalMemory > 0 && usedMemory > totalMemory;
  setMetricText(elements["ram-percent"], `${percent(ramUsage, 1)}${memoryOverLimit ? " ⚠" : ""}`);
  elements["ram-percent"].classList.toggle("is-over-limit", memoryOverLimit);
  setProgress(elements["ram-progress"], ramUsage);
  const swapPercent = swapTotal > 0 ? (swapUsed / swapTotal) * 100 : 0;
  setMetricText(elements["ram-detail"], `${bytes(usedMemory)} / ${bytes(totalMemory)} · swap ${percent(swapPercent, 0)}`);

  setMetricText(elements["disk-percent"], percent(diskUsage, 1));
  setProgress(elements["disk-progress"], diskUsage);
  if (diskUsage > 90) elements["disk-progress"].dataset.level = "hot";
  setMetricText(elements["disk-detail"], `${bytes(diskUsed)} / ${bytes(diskTotal)}`);
  setMetricText(elements["disk-device"], `${disk.device || "unknown device"} · ${disk.mountPoint || "/"}`);
  elements["disk-warning"].hidden = diskUsage <= 90;

  setMetricText(elements["network-interface"], network.interface || network.name || "default");
  setMetricText(elements["network-down"], rate(network.rxBytesPerSecond ?? network.bytesRecv));
  setMetricText(elements["network-up"], rate(network.txBytesPerSecond ?? network.bytesSent));
  setMetricText(elements.uptime, uptime(system.uptimeSeconds ?? system.uptime));
  setMetricText(elements.processes, number(system.processCount ?? system.processes));
  const load = system.loadAverages || system.load || [];
  setMetricText(elements["load-average"], Array.isArray(load) && load.length ? load.slice(0, 3).map((item) => number(item).toFixed(2)).join(" ") : "—");

  setMetricText(elements["io-read"], rate(readRate));
  setMetricText(elements["io-write"], rate(writeRate));
  setProgress(elements["io-read-progress"], clamp(readRate / maxIo * 100));
  setProgress(elements["io-write-progress"], clamp(writeRate / maxIo * 100));
  charts.update(cpuUsage, ramUsage);
}

function alertSeverity(level) {
  if (["critical", "error"].includes(level)) return 0;
  if (["warning", "warn", "degraded"].includes(level)) return 1;
  return 2;
}

function createAlertNode() {
  const item = document.createElement("article");
  item.className = "alert-item";
  const dot = document.createElement("span");
  dot.className = "status-dot";
  dot.setAttribute("aria-hidden", "true");
  const content = document.createElement("div");
  content.className = "alert-content";
  const message = document.createElement("div");
  message.className = "alert-message";
  const meta = document.createElement("div");
  meta.className = "alert-meta";
  meta.setAttribute("role", "note");
  content.append(message, meta);
  item.append(dot, content);
  item.refs = { message, meta };
  return item;
}

function abbreviatedIdentifier(value) {
  const identifier = String(value || "").trim();
  if (identifier.length <= 20) return identifier;
  return `${identifier.slice(0, 12)}…${identifier.slice(-6)}`;
}

function containerForAlertIdentifier(identifier) {
  const value = String(identifier || "").trim();
  if (!value) return null;
  return latestSelectedContainers.find((container) => {
    const id = String(container?.id || container?.ID || container?.instanceId || container?.InstanceID || "");
    return id === value || (id.length >= 12 && (id.startsWith(value) || value.startsWith(id)));
  }) || null;
}

function compactAlertSource(value) {
  const source = String(value || "").trim();
  if (!source) return "";
  const parts = source.split("/").filter(Boolean);
  if (parts.length < 3) return `SOURCE · ${abbreviatedIdentifier(source).toUpperCase()}`;
  const [node, resourceType, ...resourceParts] = parts;
  const resourceID = resourceParts.join("/");
  let resource = abbreviatedIdentifier(resourceID);
  if (resourceType.toLowerCase() === "container") {
    const container = containerForAlertIdentifier(resourceID);
    resource = String(container?.name || container?.Name || resource);
  }
  return [node.toUpperCase(), resourceType.toUpperCase(), resource].filter(Boolean).join(" · ");
}

function applyAlertRoute({ source = "" } = {}) {
  activeAlertSource = String(source || "").trim().toLowerCase();
  let visible = 0;
  for (const node of alertNodes.values()) {
    const matches = !activeAlertSource || String(node.dataset.source || "").includes(activeAlertSource);
    node.hidden = !matches;
    if (matches) visible += 1;
  }
  elements["alerts-count"].textContent = String(visible);
  elements["alerts-empty"].hidden = visible > 0;
  elements["alerts-card"].dataset.empty = String(visible === 0);
  const strong = elements["alerts-empty"].querySelector("strong");
  const detail = elements["alerts-empty"].querySelector("span");
  if (activeAlertSource && visible === 0) {
    strong.textContent = "No matching active alerts";
    detail.textContent = "The selected incident source is not present in the latest snapshot.";
  } else {
    strong.textContent = "All clear";
    detail.textContent = "No active warning or critical alerts.";
  }
}

function renderAlerts(alerts) {
  const items = (Array.isArray(alerts) ? alerts : [])
    .map((alert, index) => {
      const level = String(alert.level || "info").toLowerCase();
      const key = String(alert.id || `${alert.source || "system"}:${alert.message || "alert"}:${alert.occurredAt || alert.timestamp || index}`);
      return { ...alert, key, level, index };
    })
    .filter((alert) => alertSeverity(alert.level) < 2)
    .sort((a, b) => alertSeverity(a.level) - alertSeverity(b.level) || a.index - b.index);

  elements["alerts-count"].textContent = String(items.length);
  elements["alerts-empty"].hidden = items.length > 0;
  const alertsPartial = snapshotPartialSources.includes("alerts");
  elements["alerts-partial"].hidden = !alertsPartial;
  elements["alerts-partial"].title = alertsPartial ? "The latest alert snapshot was truncated." : "";
  elements["alerts-card"].dataset.empty = String(items.length === 0);
  const nextKeys = new Set(items.map((alert) => alert.key));
  for (const [key, node] of alertNodes) {
    if (nextKeys.has(key)) continue;
    node.remove();
    alertNodes.delete(key);
  }
  for (const alert of items) {
    let node = alertNodes.get(alert.key);
    if (!node) {
      node = createAlertNode();
      alertNodes.set(alert.key, node);
    }
    node.dataset.level = alert.level;
    node.refs.message.textContent = String(alert.message || "System alert");
    const rawSource = String(alert.source || "").trim();
    node.dataset.source = rawSource.toLowerCase();
    const source = compactAlertSource(rawSource);
    node.refs.meta.textContent = [source, alert.occurredAt || alert.timestamp ? timeAgo(alert.occurredAt || alert.timestamp) : ""].filter(Boolean).join(" · ");
    node.refs.meta.title = rawSource;
    node.refs.meta.setAttribute("aria-label", rawSource ? `Alert source: ${rawSource}` : "Alert source unavailable");
    elements["alerts-list"].append(node);
  }
  applyAlertRoute({ source: activeAlertSource });
  return {
    total: items.length,
    critical: items.filter((alert) => alertSeverity(alert.level) === 0).length,
    warning: items.filter((alert) => alertSeverity(alert.level) === 1).length,
  };
}

async function refreshSnapshot() {
  if (refreshing) return refreshing;
  refreshing = api.snapshot()
    .then(renderLocalSnapshot)
    .catch((error) => { if (!latestCollectedAt) setConnectionState("offline"); throw error; })
    .finally(() => { refreshing = null; });
  return refreshing;
}

function applySession(session = {}) {
  const identity = session.identity || session.user || {};
  const login = typeof identity === "string" ? identity : identity.login || identity.email || identity.name || session.login || "tailnet user";
  const role = String(session.role || "viewer").toLowerCase();
  const admin = role === "admin";
  const authenticated = session.authenticated === true || (session.authenticated !== false && Boolean(session.csrfToken) && login !== "unauthenticated");
  sessionAuthenticated = authenticated;
  sessionAdmin = admin;
  sessionHostShellCapability = session.capabilities?.hostShell === true;
  document.body.classList.toggle("viewer", !admin);
  document.body.classList.toggle("admin", admin);
  setMetricText(elements["session-user"], authenticated ? login : "unauthenticated");
  setMetricText(elements["session-role"], authenticated ? (admin ? "ADMIN" : "VIEWER") : "SIGN IN");
  const identityGroup = document.getElementById("session-identity");
  identityGroup.setAttribute("aria-label", authenticated ? `Signed in as ${login}, role ${admin ? "administrator" : "viewer"}` : "Not signed in; sign in to save dashboard preferences");
  identityGroup.title = authenticated ? `${login} · ${admin ? "ADMIN" : "VIEWER"}` : "Not signed in";
  servicesController.setAdmin(admin);
  containersController.setAdmin(admin);
  alertsController.setAdmin(admin);
  nodesController.setAdmin(admin);
  settingsController.setAdmin(admin);
  settingsController.setSession({ authenticated });
  overviewController.setAuthenticated(authenticated);
  operationsController.setAdmin(admin);
  terminal.setHostShellCapability(sessionHostShellCapability);
  updateOverview();
}

function handleLocalConnectionState(state, detail = {}) {
  localConnectionState = state;
  if (selectedNodeId === "local") setConnectionState(state, detail);
  else elements["services-stale"].hidden = !(["stale", "offline"].includes(state) && Boolean(latestLocalEnvelope));
}

function scheduleSessionRenewal(delay = 1200) {
  if (demo || sessionRenewalTimer) return;
  sessionRenewalTimer = window.setTimeout(async () => {
    sessionRenewalTimer = null;
    if (Date.now() - lastSessionRenewalAt < 12_000) return;
    lastSessionRenewalAt = Date.now();
    try {
      applySession(await api.session());
    } catch {
      if (["offline", "stale"].includes(localConnectionState)) scheduleSessionRenewal(3000);
    }
  }, delay);
}

async function hydratePreferences() {
  if (demo) {
    try { applyWorkspacePreferences(JSON.parse(storageValue("homelab.demo.workspace-preferences") || "{}")); }
    catch { applyWorkspacePreferences({}); }
    return;
  }
  if (typeof api.preferences !== "function") return;
  try {
    const preferences = await api.preferences();
    applyWorkspacePreferences(preferences);
    if (preferences?.historyRange) historyController.setRange(preferences.historyRange, false);
    await nodesController.refresh();
    if (preferences?.defaultNodeId) nodesController.setSelected(preferences.defaultNodeId);
    const height = Number(preferences?.terminalHeight);
    if (Number.isFinite(height) && height >= 120) {
      const bounded = Math.min(height, Math.max(120, window.innerHeight * 0.6));
      document.documentElement.style.setProperty("--terminal-height", `${Math.round(bounded)}px`);
      try { localStorage.setItem("homelab.terminal.height", String(Math.round(bounded))); } catch { /* Storage is optional. */ }
      terminal.fit();
    }
    const panel = document.getElementById("terminal-panel");
    if (typeof preferences?.terminalCollapsed === "boolean" &&
        preferences.terminalCollapsed !== panel.classList.contains("is-collapsed")) {
      document.getElementById("terminal-toggle").click();
    }
    historyController.refresh();
  } catch (error) {
    console.warn("Dashboard preferences unavailable; using local UI preferences.", error);
  }
}

function savePreferences(update) {
  if (!sessionAdmin || demo || typeof api.updatePreferences !== "function") return;
  preferenceSavePending = { ...preferenceSavePending, ...update };
  window.clearTimeout(preferenceSaveTimer);
  preferenceSaveTimer = window.setTimeout(async () => {
    const pending = preferenceSavePending;
    preferenceSavePending = {};
    try { await api.updatePreferences(pending); }
    catch (error) { console.warn("Unable to persist dashboard preferences.", error); }
  }, 450);
}

async function start() {
  elements["demo-badge"].hidden = !demo;
  applyTheme(preferredTheme());
  try {
	applySession(await api.session());
	if (demo) await nodesController.refresh();
	await hydratePreferences();
    routeNodeSelectionReady = true;
    workspaceNavigation.applyRoute(routeFromLocation(), { focus: false, applyNode: true });
  } catch (error) {
    applySession({ role: "viewer", identity: { login: "unauthenticated" } });
    routeNodeSelectionReady = true;
    workspaceNavigation.applyRoute(routeFromLocation(), { focus: false, applyNode: true });
    toast(error?.message || "Unable to load the Tailscale session.", "error");
  }

  if (demo) {
    stopDemoFeed = startDemoFeed(renderLocalSnapshot);
    return;
  }

  try { await refreshSnapshot(); } catch { /* WebSocket retry owns the live recovery path. */ }
  stream = new MetricsStream({
    onSnapshot: renderLocalSnapshot,
    onState: handleLocalConnectionState,
    onError: (error) => console.warn("Metrics stream frame rejected:", error),
    refreshSession: async () => {
      const session = await api.session();
      applySession(session);
      return session;
    },
  });
  stream.start();
}

document.getElementById("theme-toggle")?.addEventListener("click", () => applyTheme(themeName() === "dark" ? "light" : "dark", { persist: true }));
document.getElementById("node-selector").addEventListener("change", () => savePreferences({ defaultNodeId: nodesController.selectedNode() }));
document.querySelectorAll("[data-history-range]").forEach((button) => button.addEventListener("click", () => savePreferences({ historyRange: historyController.range() })));
document.getElementById("terminal-toggle").addEventListener("click", () => window.setTimeout(() => savePreferences({ terminalCollapsed: document.getElementById("terminal-panel").classList.contains("is-collapsed") }), 0));
document.getElementById("terminal-resize").addEventListener("pointerup", () => savePreferences({ terminalHeight: Math.round(document.getElementById("terminal-body").getBoundingClientRect().height) }));
document.getElementById("terminal-resize").addEventListener("keydown", (event) => {
  if (!["ArrowUp", "ArrowDown"].includes(event.key)) return;
  window.setTimeout(() => savePreferences({ terminalHeight: Math.round(document.getElementById("terminal-body").getBoundingClientRect().height) }), 0);
});
for (const id of ["terminal-size-compact", "terminal-size-default"]) {
  document.getElementById(id)?.addEventListener("click", () => window.setTimeout(() => savePreferences({ terminalHeight: Math.round(document.getElementById("terminal-body").getBoundingClientRect().height), terminalCollapsed: false }), 0));
}
const sessionKeepalive = window.setInterval(() => scheduleSessionRenewal(0), 5 * 60_000);
if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("./sw.js", { scope: "./" }).catch((error) => {
      console.warn("PWA shell registration unavailable:", error);
    });
  }, { once: true });
}
window.addEventListener("beforeunload", () => {
  stream?.stop();
  stopDemoFeed?.();
  terminal.disconnect(false);
  charts.destroy();
  overviewController.destroy();
  historyController.destroy();
  logsController.destroy();
  operationsController.destroy();
  nodesController.destroy();
  window.clearInterval(sessionKeepalive);
  window.clearTimeout(sessionRenewalTimer);
  window.clearTimeout(preferenceSaveTimer);
});

start();
