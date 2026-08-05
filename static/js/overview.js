import { bytes, clamp, rate, safeHttpUrl, timeAgo } from "./format.js";
import { OVERVIEW_WIDGET_ORDER } from "./widget-catalog.js";

const HISTORY_TTL_MS = 5 * 60 * 1000;
const HISTORY_RETRY_DELAY_MS = 30 * 1000;
const EVENTS_TTL_MS = 60 * 1000;
const MAX_ATTENTION = 5;
const MAX_RECENT_CHANGES = 5;
const PROBLEM_CONTAINERS = new Set(["crashed", "unhealthy", "dead", "restarting"]);
const PROBLEM_SERVICES = new Set(["down", "error", "unhealthy", "crashed", "degraded", "warning"]);
const ACTIONABLE_ALERTS = new Set(["critical", "error", "warning", "warn"]);
const OVERVIEW_WIDGET_IDS = OVERVIEW_WIDGET_ORDER;
const ICONS = Object.freeze({
  link: '<path d="M10 13a5 5 0 0 0 7.1.1l2-2a5 5 0 0 0-7.1-7.1l-1.1 1.1"/><path d="M14 11a5 5 0 0 0-7.1-.1l-2 2A5 5 0 0 0 12 20l1.1-1.1"/>',
  server: '<rect x="3" y="4" width="18" height="6" rx="1"/><rect x="3" y="14" width="18" height="6" rx="1"/><path d="M7 7h.01M7 17h.01M11 7h7M11 17h7"/>',
  storage: '<path d="M4 5h16v5H4zM4 14h16v5H4z"/><path d="M8 7h.01M8 16h.01"/>',
  network: '<circle cx="5" cy="6" r="2"/><circle cx="19" cy="6" r="2"/><circle cx="12" cy="18" r="2"/><path d="m6.7 7.1 4.1 8.1M17.3 7.1l-4.1 8.1M7 6h10"/>',
});

function token(name, fallback = "transparent") {
  return globalThis.getComputedStyle?.(document.documentElement).getPropertyValue(name).trim() || fallback;
}

function numeric(value) {
  const next = Number(value);
  return Number.isFinite(next) ? next : 0;
}

function field(value, camel, exported) {
  return value?.[camel] ?? value?.[exported];
}

function containerState(container = {}) {
  const restarts = numeric(container.restartCount ?? container.restarts);
  const health = String(container.health || container.healthStatus || "").toLowerCase();
  const state = String(container.state || container.status || "unknown").toLowerCase();
  if (restarts > 3) return "crashed";
  return ["unhealthy", "crashed", "dead"].includes(health) ? health : state;
}

function serviceState(service = {}) {
  return String(service?.status || service?.health?.status || "unknown").toLowerCase();
}

function incidentLevel(value) {
  const level = String(value || "").toLowerCase();
  if (["critical", "error", "crashed", "down"].includes(level)) return "critical";
  return "warning";
}

function incidentPriority(item) {
  if (item.level === "critical") return 0;
  if (item.kind === "alert") return 1;
  if (item.kind === "service") return 2;
  return 3;
}

function historyPercent(point, usedField, totalField) {
  const used = numeric(field(point, usedField, usedField[0].toUpperCase() + usedField.slice(1)));
  const total = numeric(field(point, totalField, totalField[0].toUpperCase() + totalField.slice(1)));
  return total > 0 ? clamp(used / total * 100) : 0;
}

function createTrendChart(canvas) {
  if (!canvas || typeof globalThis.Chart !== "function") return null;
  const colors = {
    accent: token("--accent"),
    green: token("--green"),
    yellow: token("--yellow"),
    dim: token("--text-dim"),
    secondary: token("--text-secondary"),
    primary: token("--text-primary"),
    overlay: token("--bg-overlay"),
    border: token("--border-subtle"),
    accentMuted: token("--accent-muted"),
  };
  return new globalThis.Chart(canvas.getContext("2d"), {
    type: "line",
    data: {
      labels: [],
      datasets: [
        { label: "CPU", data: [], borderColor: colors.accent, fill: false },
        { label: "RAM", data: [], borderColor: colors.green, fill: false },
        { label: "DISK", data: [], borderColor: colors.yellow, fill: false },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: false,
      normalized: true,
      interaction: { intersect: false, mode: "index" },
      scales: {
        x: { grid: { display: false }, ticks: { color: colors.dim, maxTicksLimit: 6, maxRotation: 0, font: { family: "monospace", size: 9 } } },
        y: { min: 0, max: 100, grid: { color: colors.border }, ticks: { color: colors.dim, callback: (value) => `${value}%`, font: { family: "monospace", size: 9 } } },
      },
      plugins: {
        legend: { display: true, align: "end", labels: { color: colors.secondary, usePointStyle: true, boxWidth: 7, font: { family: "monospace", size: 9 } } },
        tooltip: { backgroundColor: colors.overlay, borderColor: colors.accentMuted, borderWidth: 1, titleColor: colors.primary, bodyColor: colors.secondary, callbacks: { label: (context) => `${context.dataset.label}: ${Number(context.raw).toFixed(1)}%` } },
      },
      elements: { point: { radius: 0, hoverRadius: 3 }, line: { borderWidth: 1.5, tension: 0.18 } },
    },
  });
}

function historyLabel(at) {
  const date = new Date(at);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function compactSource(source) {
  const value = String(source || "").trim();
  if (!value) return "system";
  const parts = value.split("/").filter(Boolean);
  if (parts.length >= 3 && parts[1]?.toLowerCase() === "container") {
    const id = parts.slice(2).join("/");
    return `${parts[0].toUpperCase()} · CONTAINER · ${id.length > 22 ? `${id.slice(0, 12)}…${id.slice(-6)}` : id}`;
  }
  return value.length > 42 ? `${value.slice(0, 32)}…${value.slice(-7)}` : value;
}

function containerForSource(source, containers) {
  const parts = String(source || "").split("/").filter(Boolean);
  if (parts.length < 3 || parts[1]?.toLowerCase() !== "container") return null;
  const target = parts.slice(2).join("/");
  return containers.find((container) => {
    const id = String(container?.id || container?.ID || "");
    return id === target || (id.length >= 12 && (id.startsWith(target) || target.startsWith(id)));
  }) || null;
}

export function collectOverviewIncidents({ alerts = [], services = [], containers = [], partial = false } = {}) {
  const incidents = [];
  if (partial) incidents.push({ kind: "frame", level: "warning", title: "Latest snapshot is partial", detail: "Some alert or inventory data was omitted; inspect the full alert workspace." });

  for (const alert of Array.isArray(alerts) ? alerts : []) {
    const alertLevel = String(alert?.level ?? alert?.severity ?? alert?.status ?? "").toLowerCase();
    if (!ACTIONABLE_ALERTS.has(alertLevel)) continue;
    const occurred = alert.occurredAt || alert.timestamp;
    const container = containerForSource(alert.source, containers);
    incidents.push({
      kind: "alert",
      level: incidentLevel(alertLevel),
      title: String(alert.message || "System alert"),
      detail: [container?.name || compactSource(alert.source), occurred ? timeAgo(occurred) : ""].filter(Boolean).join(" · "),
      detailTitle: String(alert.source || "").trim(),
      container,
    });
  }

  for (const service of Array.isArray(services) ? services : []) {
    const status = serviceState(service);
    if (!PROBLEM_SERVICES.has(status)) continue;
    incidents.push({ kind: "service", level: incidentLevel(status), title: `${service.name || "Service"} is ${status}`, detail: Number.isFinite(Number(service.latencyMs)) ? `${Math.round(Number(service.latencyMs))} ms · endpoint probe` : "Endpoint probe needs attention", service });
  }

  for (const container of Array.isArray(containers) ? containers : []) {
    const state = containerState(container);
    if (!PROBLEM_CONTAINERS.has(state)) continue;
    incidents.push({ kind: "container", level: incidentLevel(state), title: `${container.name || "Container"} is ${state}`, detail: numeric(container.restartCount ?? container.restarts) ? `${numeric(container.restartCount ?? container.restarts)} restarts` : String(container.image || "Container runtime issue"), container });
  }

  return incidents.sort((left, right) => incidentPriority(left) - incidentPriority(right));
}

export function createOverviewController({ api, demo = false, toast, onNavigate, onOpenContainerTerminal, onWidgetPreferences }) {
  const attentionPanel = document.getElementById("overview-attention");
  const attentionTitle = document.getElementById("overview-attention-title");
  const attentionList = document.getElementById("overview-attention-list");
  const attentionEmpty = document.getElementById("overview-attention-empty");
  const attentionCount = document.getElementById("overview-attention-count");
  const servicePanel = document.getElementById("overview-service-pulse");
  const serviceList = document.getElementById("overview-service-pulse-list");
  const serviceEmpty = document.getElementById("overview-service-pulse-empty");
  const serviceCount = document.getElementById("overview-service-pulse-count");
  const launchpadList = document.getElementById("overview-launchpad-list");
  const launchpadEmpty = document.getElementById("overview-launchpad-empty");
  const launchpadCount = document.getElementById("overview-launchpad-count");
  const serviceGroupsList = document.getElementById("overview-service-groups-list");
  const serviceGroupsEmpty = document.getElementById("overview-service-groups-empty");
  const serviceGroupsCount = document.getElementById("overview-service-groups-count");
  const topContainersList = document.getElementById("overview-top-containers-list");
  const topContainersEmpty = document.getElementById("overview-top-containers-empty");
  const storagePoolsList = document.getElementById("overview-storage-pools-list");
  const storagePoolsEmpty = document.getElementById("overview-storage-pools-empty");
  const noteInput = document.getElementById("overview-operator-note-input");
  const noteStatus = document.getElementById("overview-note-status");
  const noteUpdated = document.getElementById("overview-note-updated");
  const chartWrap = document.getElementById("overview-trend-chart-wrap");
  const chartStatus = document.getElementById("overview-trend-status");
  const nodeLabel = document.getElementById("overview-trend-node");
  const resolutionLabel = document.getElementById("overview-trend-resolution");
  const refreshButton = document.getElementById("overview-trend-refresh");
  const openHistoryButton = document.getElementById("overview-trend-open");
  const changesList = document.getElementById("overview-recent-changes-list");
  const changesEmpty = document.getElementById("overview-recent-changes-empty");
  const changesNodeLabel = document.getElementById("overview-recent-changes-node");
  const changesStatus = document.getElementById("overview-recent-changes-status");
  const changesRefreshButton = document.getElementById("overview-recent-changes-refresh");
  const chart = createTrendChart(document.getElementById("overview-trend-chart"));
  const cache = new Map();
  const eventCache = new Map();
  const retryAfter = new Map();
  let request = null;
  let requestNode = "";
  let eventRequest = null;
  let eventRequestNode = "";
  let renderedEventsKey = "";
  let active = false;
  let node = "local";
  let nodeName = "local";
  let hiddenWidgets = new Set();
  let latest = { services: [], containers: [], alerts: [], admin: false, remote: false, partial: false, connection: "connecting" };
  let launchpad = { items: [], revision: 0 };
  let operatorNote = { text: "", revision: 0, updatedAt: null, updatedBy: "" };
  let widgetContentLoaded = false;
  let noteSaveTimer = 0;
  let containerRank = "cpu";

  function setTrendStatus(message, level = "info") {
    chartStatus.textContent = message;
    chartStatus.dataset.level = level === "error" ? "error" : "";
    chartStatus.hidden = !message;
  }

  function cancelTrendRequest() {
    if (!request) return;
    request.abort();
    request = null;
    requestNode = "";
    chartWrap?.setAttribute("aria-busy", "false");
    refreshButton.disabled = false;
  }

  function cancelEventRequest() {
    if (!eventRequest) return;
    eventRequest.abort();
    eventRequest = null;
    eventRequestNode = "";
    changesRefreshButton.disabled = false;
  }

  function clearTrend() {
    if (chart) {
      chart.data.labels = [];
      for (const dataset of chart.data.datasets) dataset.data = [];
      chart.update("none");
    }
    resolutionLabel.textContent = "24H · WAITING";
  }

  function widgetVisible(id) {
    return !hiddenWidgets.has(id);
  }

  const widgetPopover = document.getElementById("overview-widget-popover");
  const widgetMenuTitle = document.getElementById("overview-widget-menu-title");
  const widgetMenuSize = document.getElementById("overview-widget-menu-size");
  const widgetMenuHide = document.getElementById("overview-widget-menu-hide");
  const widgetMenuReset = document.getElementById("overview-widget-menu-reset");
  const widgetMenuTriggers = new Map();
  let widgetMenuWidget = "";
  let widgetMenuTrigger = null;
  let widgetMenuAuthenticated = false;
  let widgetMenuBusy = false;

  function setWidgetMenuBusy(value) {
    widgetMenuBusy = Boolean(value);
    for (const control of [widgetMenuHide, widgetMenuReset, ...widgetMenuSize.querySelectorAll("button")]) {
      if (control) control.disabled = widgetMenuBusy;
    }
  }

  function restoreWidgetFocus(trigger) {
    if (trigger?.isConnected && !trigger.closest("[hidden]")) {
      trigger.focus({ preventScroll: true });
      return;
    }
    document.getElementById("overview-title")?.focus({ preventScroll: true });
  }

  function positionWidgetPopover() {
    if (!widgetPopover?.matches(":popover-open") || !widgetMenuTrigger) return;
    const triggerRect = widgetMenuTrigger.getBoundingClientRect();
    const menuRect = widgetPopover.getBoundingClientRect();
    const left = Math.max(8, Math.min(window.innerWidth - menuRect.width - 8, triggerRect.right - menuRect.width));
    const top = triggerRect.bottom + menuRect.height + 8 <= window.innerHeight
      ? triggerRect.bottom + 6
      : Math.max(8, triggerRect.top - menuRect.height - 6);
    widgetPopover.style.left = `${left}px`;
    widgetPopover.style.top = `${top}px`;
  }

  function closeWidgetPopover({ restoreFocus = true } = {}) {
    if (!widgetPopover?.matches(":popover-open")) return;
    widgetPopover.hidePopover();
    if (restoreFocus && widgetMenuTrigger?.isConnected) widgetMenuTrigger.focus({ preventScroll: true });
    widgetMenuTrigger = null;
    widgetMenuWidget = "";
  }

  function requestWidgetPreferences(update) {
    if (widgetMenuBusy) return;
    const trigger = widgetMenuTrigger;
    setWidgetMenuBusy(true);
    closeWidgetPopover({ restoreFocus: false });
    Promise.resolve(onWidgetPreferences?.(update))
      .catch((error) => toast?.(error?.message || "Unable to save overview widget preferences.", "error"))
      .finally(() => {
        setWidgetMenuBusy(false);
        restoreWidgetFocus(trigger);
      });
  }

  function openWidgetPopover(widgetID, trigger) {
    if (!widgetPopover || !widgetMenuAuthenticated || widgetMenuBusy) return;
    widgetMenuWidget = widgetID;
    widgetMenuTrigger = trigger;
    widgetMenuTitle.textContent = document.getElementById(widgetID)?.querySelector("h2")?.textContent || "Overview widget";
    widgetMenuSize.replaceChildren();
    const currentSize = document.getElementById(widgetID)?.dataset.widgetSize || "medium";
    for (const size of ["small", "medium", "full"]) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "text-button widget-size-option";
      button.textContent = size.toUpperCase();
      button.dataset.size = size;
      button.setAttribute("role", "menuitemradio");
      button.setAttribute("aria-checked", String(size === currentSize));
      button.addEventListener("keydown", (event) => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        const buttons = [...widgetMenuSize.querySelectorAll("button")];
        const current = buttons.indexOf(button);
        const next = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : current + (event.key === "ArrowRight" ? 1 : -1);
        if (!buttons[next]) return;
        event.preventDefault();
        buttons[next].focus();
      });
      button.addEventListener("click", () => requestWidgetPreferences({ widgetID, size }));
      widgetMenuSize.append(button);
    }
    widgetMenuHide.hidden = widgetID === "overview-attention";
    widgetMenuReset.hidden = false;
    widgetPopover.showPopover();
    positionWidgetPopover();
  }

  function setupWidgetMenus() {
    for (const widgetID of OVERVIEW_WIDGET_IDS) {
      const panel = document.getElementById(widgetID);
      const header = panel?.querySelector(".panel-header");
      let actions = header?.querySelector(".panel-actions");
      if (header && !actions) {
        actions = document.createElement("div");
        actions.className = "panel-actions";
        header.append(actions);
      }
      if (!panel || !actions || widgetMenuTriggers.has(widgetID)) continue;
      const trigger = document.createElement("button");
      trigger.type = "button";
      trigger.className = "icon-button widget-menu-trigger";
      trigger.hidden = !widgetMenuAuthenticated;
      trigger.setAttribute("aria-label", `Configure ${panel.querySelector("h2")?.textContent || "overview widget"}`);
      trigger.setAttribute("aria-haspopup", "menu");
      trigger.title = "Configure widget";
      trigger.innerHTML = '<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>';
      trigger.addEventListener("click", () => openWidgetPopover(widgetID, trigger));
      actions.append(trigger);
      widgetMenuTriggers.set(widgetID, trigger);
    }
    window.addEventListener("resize", positionWidgetPopover);
    widgetPopover?.addEventListener("toggle", (event) => {
      if (event.newState !== "closed") return;
      const trigger = widgetMenuTrigger;
      widgetMenuTrigger = null;
      widgetMenuWidget = "";
      if (!widgetMenuBusy) restoreWidgetFocus(trigger);
    });
    widgetMenuHide?.addEventListener("click", () => requestWidgetPreferences({ widgetID: widgetMenuWidget, hidden: true }));
    widgetMenuReset?.addEventListener("click", () => requestWidgetPreferences({ reset: true }));
  }

  setupWidgetMenus();

  function resizeChart() {
    window.requestAnimationFrame(() => chart?.resize());
  }

  function actionButton(label, handler, disabled = false) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "text-button";
    button.textContent = label;
    button.disabled = disabled;
    button.addEventListener("click", handler);
    return button;
  }

  function appendContainerActions(actions, container, admin) {
    const state = containerState(container);
    const canLogs = container?.actions?.logs !== false && Boolean(container?.id || container?.ID);
    const canShell = admin && state === "running" && !container?.protected && container?.actions?.exec !== false && Boolean(container?.id || container?.ID);
    if (canLogs) actions.append(actionButton("LOGS", (event) => onOpenContainerTerminal?.(container, "logs", event.currentTarget)));
    if (canShell) actions.append(actionButton("SHELL", (event) => onOpenContainerTerminal?.(container, "exec", event.currentTarget)));
  }

  function containerQuery(container) {
    return String(container?.name || container?.id || container?.ID || "").slice(0, 160);
  }

  function serviceQuery(service) {
    return String(service?.name || service?.id || service?.ID || "").slice(0, 160);
  }

  function openService(service) {
    const url = safeHttpUrl(String(service?.displayUrl || service?.displayURL || service?.url || ""));
    if (!url) {
      toast?.("This service has no valid display URL.", "error");
      return;
    }
    window.open(url.toString(), "_blank", "noopener,noreferrer");
  }

  function createIncident(item) {
    const article = document.createElement("article");
    article.className = "overview-action-item";
    article.dataset.level = item.level;
    article.dataset.kind = item.kind;
    const dot = document.createElement("span");
    dot.className = "status-dot";
    dot.setAttribute("aria-hidden", "true");
    const copy = document.createElement("div");
    copy.className = "overview-action-copy";
    const title = document.createElement("strong");
    title.className = "overview-action-title";
    title.textContent = item.title;
    title.title = item.title;
    const detail = document.createElement("span");
    detail.className = "overview-action-detail";
    detail.textContent = item.detail;
    if (item.detailTitle) {
      detail.title = item.detailTitle;
      detail.setAttribute("aria-label", item.detailTitle);
    }
    copy.append(title, detail);
    const actions = document.createElement("div");
    actions.className = "overview-action-actions";
    if (item.container) {
      actions.append(actionButton("VIEW", () => onNavigate?.({ workspace: "containers", node, state: "attention", query: containerQuery(item.container) })));
      appendContainerActions(actions, item.container, latest.admin);
    }
    if (item.service) {
      actions.append(actionButton("VIEW", () => onNavigate?.({ workspace: "services", state: "attention", query: serviceQuery(item.service) })));
      actions.append(actionButton("OPEN", () => openService(item.service)));
    }
    if (item.kind === "alert" || item.kind === "frame" || !actions.childElementCount) {
      actions.append(actionButton("ALERTS", () => onNavigate?.({ workspace: "alerts", node, state: "firing", source: item.detailTitle || "" })));
    }
    article.append(dot, copy, actions);
    return article;
  }

  function iconSvg(name) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("fill", "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "1.7");
    svg.setAttribute("aria-hidden", "true");
    svg.innerHTML = ICONS[name] || ICONS.link;
    return svg;
  }

  function renderLaunchpad() {
    if (!launchpadList) return;
    const items = Array.isArray(launchpad.items) ? launchpad.items.slice(0, 24) : [];
    launchpadList.replaceChildren(...items.map((item) => {
      const article = document.createElement("article");
      article.className = "launchpad-item";
      const link = document.createElement("a");
      link.className = "launchpad-link";
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      const url = safeHttpUrl(item.url);
      if (url) link.href = url.toString();
      link.append(iconSvg(item.icon), Object.assign(document.createElement("span"), { textContent: String(item.title || "Untitled") }));
      link.title = item.url || "";
      article.append(link);
      if (item.tag) {
        const tag = document.createElement("span");
        tag.className = "launchpad-tag mono";
        tag.textContent = String(item.tag).toUpperCase();
        article.append(tag);
      }
      return article;
    }));
    launchpadCount.textContent = String(items.length);
    launchpadEmpty.hidden = items.length > 0;
  }

  function renderServiceGroups() {
    if (!serviceGroupsList) return;
    const groups = new Map();
    for (const service of Array.isArray(latest.services) ? latest.services : []) {
      const category = String(service.category || "Uncategorized").trim() || "Uncategorized";
      if (!groups.has(category)) groups.set(category, []);
      groups.get(category).push(service);
    }
    const entries = [...groups.entries()].sort((a, b) => a[0].localeCompare(b[0]));
    serviceGroupsList.replaceChildren(...entries.map(([category, services]) => {
      const row = document.createElement("button");
      row.type = "button";
      row.className = "service-group-row";
      const up = services.filter((item) => ["up", "running", "healthy"].includes(serviceState(item))).length;
      const issue = services.filter((item) => PROBLEM_SERVICES.has(serviceState(item))).length;
      const tags = [...new Set(services.flatMap((item) => Array.isArray(item.tags) ? item.tags : []))].slice(0, 3);
      const copy = document.createElement("span");
      copy.className = "service-group-copy";
      copy.innerHTML = `<strong></strong><span></span>`;
      copy.firstElementChild.textContent = category;
      copy.lastElementChild.textContent = tags.length ? tags.map((tag) => String(tag).toUpperCase()).join(" · ") : "No tags";
      const count = document.createElement("span");
      count.className = "service-group-count mono";
      count.textContent = `${up}/${services.length} UP${issue ? ` · ${issue} ISSUE${issue === 1 ? "" : "S"}` : ""}`;
      row.append(copy, count);
      row.addEventListener("click", () => onNavigate?.({ workspace: "services", state: "all", query: category }));
      return row;
    }));
    serviceGroupsCount.textContent = String(entries.length);
    serviceGroupsEmpty.hidden = entries.length > 0;
  }

  function renderTopContainers() {
    if (!topContainersList) return;
    const containers = (Array.isArray(latest.containers) ? latest.containers : []).map((item) => {
      const memory = numeric(item.memoryUsageBytes ?? item.memoryUsage);
      const limit = numeric(item.memoryLimitBytes ?? item.memoryLimit);
      return { item, state: containerState(item), cpu: numeric(item.cpuNormalizedPercent ?? item.cpuUsagePercent ?? item.cpuPercent), ram: limit > 0 ? memory / limit * 100 : 0, restarts: numeric(item.restartCount ?? item.restarts) };
    });
    containers.sort((a, b) => containerRank === "ram" ? b.ram - a.ram : containerRank === "issues" ? (PROBLEM_CONTAINERS.has(b.state) ? 1 : 0) - (PROBLEM_CONTAINERS.has(a.state) ? 1 : 0) || b.restarts - a.restarts : b.cpu - a.cpu);
    const rows = containers.slice(0, 5);
    topContainersList.replaceChildren(...rows.map(({ item, cpu, ram, state, restarts }) => {
      const row = document.createElement("div");
      row.className = "top-container-row";
      const name = document.createElement("strong");
      name.textContent = String(item.name || item.id || "Unnamed container");
      name.title = name.textContent;
      const detail = document.createElement("span");
      detail.className = "mono";
      detail.textContent = containerRank === "ram" ? `${ram.toFixed(1)}% RAM` : containerRank === "issues" ? `${state.toUpperCase()} · ${restarts} RESTARTS` : `${cpu.toFixed(1)}% CPU`;
      const dot = document.createElement("span");
      dot.className = "status-dot";
      dot.dataset.state = PROBLEM_CONTAINERS.has(state) ? "down" : state === "running" ? "up" : "muted";
      row.append(dot, name, detail);
      return row;
    }));
    topContainersEmpty.hidden = rows.length > 0;
  }

  function renderStoragePools() {
    if (!storagePoolsList) return;
    const disks = Array.isArray(latest.disks) ? latest.disks : [];
    storagePoolsList.replaceChildren(...disks.map((disk) => {
      const row = document.createElement("div");
      row.className = "storage-pool-row";
      const percentValue = numeric(disk.usagePercent ?? disk.percent);
      row.innerHTML = '<div class="storage-pool-heading"><strong></strong><span class="mono"></span></div><div class="progress progress-thin"><span class="progress-fill"></span></div><div class="storage-pool-meta mono"><span></span><span></span></div>';
      row.querySelector("strong").textContent = String(disk.mountPoint || "/");
      row.querySelector(".storage-pool-heading span").textContent = `${percentValue.toFixed(1)}% USED`;
      row.querySelector(".progress").style.setProperty("--progress", clamp(percentValue));
      row.querySelector(".progress").dataset.level = percentValue > 90 ? "critical" : percentValue >= 80 ? "hot" : percentValue >= 50 ? "warning" : "normal";
      row.querySelector(".storage-pool-meta span").textContent = `${bytes(disk.usedBytes ?? disk.used)} / ${bytes(disk.totalBytes ?? disk.total)} · ${disk.device || "device unavailable"}`;
      row.querySelector(".storage-pool-meta span:last-child").textContent = `R ${rate(disk.readBytesPerSecond ?? disk.readRate)} · W ${rate(disk.writeBytesPerSecond ?? disk.writeRate)}`;
      return row;
    }));
    storagePoolsEmpty.hidden = disks.length > 0;
  }

  function renderOperatorNote() {
    if (!noteInput) return;
    noteInput.value = String(operatorNote.text || "");
    noteInput.disabled = !latest.admin;
    noteStatus.textContent = latest.admin ? "EDITABLE" : "READ ONLY";
    noteUpdated.textContent = operatorNote.updatedAt ? `UPDATED ${timeAgo(operatorNote.updatedAt)}` : "SHARED NOTE";
  }

  async function loadWidgetContent(force = false) {
    if (!active || (!widgetVisible("overview-quick-launchpad") && !widgetVisible("overview-operator-notes")) || (widgetContentLoaded && !force)) return;
    widgetContentLoaded = true;
    try {
      if (widgetVisible("overview-quick-launchpad") && typeof api?.getLaunchpad === "function") launchpad = normalizeLaunchpad(await api.getLaunchpad());
      if (widgetVisible("overview-operator-notes") && typeof api?.getOperatorNote === "function") operatorNote = normalizeNote(await api.getOperatorNote());
      renderLaunchpad();
      renderOperatorNote();
    } catch (error) {
      widgetContentLoaded = false;
      noteStatus.textContent = error?.message || "CONTENT UNAVAILABLE";
    }
  }

  function normalizeLaunchpad(payload) { const data = payload?.data || payload || {}; const revision = Number(data.revision); return { items: Array.isArray(data.items) ? data.items : [], revision: Number.isFinite(revision) ? revision : 0 }; }
  function normalizeNote(payload) { const data = payload?.data || payload || {}; const revision = Number(data.revision); return { text: String(data.text || "").slice(0, 4096), revision: Number.isFinite(revision) ? revision : 0, updatedAt: data.updatedAt || data.updated_at || null, updatedBy: String(data.updatedBy || data.updated_by || "") }; }

  function renderAttention() {
    const incidents = collectOverviewIncidents(latest);
    const visible = incidents.slice(0, MAX_ATTENTION);
    attentionList.replaceChildren(...visible.map(createIncident));
    attentionCount.textContent = String(incidents.length);
    attentionEmpty.hidden = visible.length > 0;
    attentionPanel?.classList.toggle("is-empty", visible.length === 0);
    if (attentionTitle) attentionTitle.textContent = visible.length ? "Needs Attention" : "All Clear";
    return {
      total: incidents.length,
      critical: incidents.filter((incident) => incident.level === "critical").length,
      warning: incidents.filter((incident) => incident.level === "warning").length,
    };
  }

  function renderServicePulse() {
    const services = Array.isArray(latest.services) ? latest.services : [];
    const configured = services
      .filter((service) => String(service?.probeUrl || service?.probeURL || "").trim())
      .sort((left, right) => {
        const leftProblem = PROBLEM_SERVICES.has(serviceState(left)) ? 0 : 1;
        const rightProblem = PROBLEM_SERVICES.has(serviceState(right)) ? 0 : 1;
        return leftProblem - rightProblem || numeric(right?.latencyMs) - numeric(left?.latencyMs);
      });
    const probed = configured.slice(0, MAX_ATTENTION);
    const rows = probed.map((service) => {
      const status = serviceState(service);
      return {
        kind: "service",
        level: PROBLEM_SERVICES.has(status) ? incidentLevel(status) : "healthy",
        title: String(service?.name || "Service"),
        detail: `${status.toUpperCase()} · ${Number.isFinite(Number(service?.latencyMs)) ? `${Math.round(Number(service.latencyMs))} ms` : "latency unavailable"}`,
        service,
      };
    });
    serviceList.replaceChildren(...rows.map(createIncident));
    serviceCount.textContent = `${configured.length} / ${services.length}`;
    serviceEmpty.hidden = rows.length > 0;
    if (latest.remote && !rows.length) serviceEmpty.textContent = "Service probes are managed from the Local node.";
    else if (!services.length) serviceEmpty.textContent = "No services are configured.";
    else serviceEmpty.textContent = `0 of ${services.length} services have a configured health probe.`;
  }

  function eventRoute(event) {
    const container = String(event?.containerId || event?.containerID || "");
    const service = String(event?.serviceId || event?.serviceID || "");
    if (container) {
      const match = latest.containers.find((item) => String(item?.id || item?.ID || item?.instanceId || item?.InstanceID || "") === container);
      return { workspace: "containers", node, state: "all", query: match ? containerQuery(match) : container };
    }
    if (service) {
      const match = latest.services.find((item) => String(item?.id || item?.ID || "") === service);
      return { workspace: "services", state: "all", query: match ? serviceQuery(match) : service };
    }
    return { workspace: "history", node, range: "24h", kind: "system" };
  }

  function renderEvents(items, { stale = false } = {}) {
    const events = [...(Array.isArray(items) ? items : [])]
      .sort((left, right) => new Date(right?.occurredAt || right?.timestamp || 0) - new Date(left?.occurredAt || left?.timestamp || 0))
      .slice(0, MAX_RECENT_CHANGES);
    changesEmpty.textContent = "No recorded operational changes in the last 24 hours.";
    const eventKey = events.map((event) => [
      event?.id,
      event?.type,
      event?.title,
      event?.summary,
      event?.occurredAt || event?.timestamp,
      event?.containerId || event?.containerID,
      event?.serviceId || event?.serviceID,
    ].map((value) => String(value ?? "")).join("\u001f")).join("\u001e");
    if (eventKey !== renderedEventsKey) {
      const rows = events.map((event) => {
        const article = document.createElement("article");
        article.className = "overview-action-item";
        article.dataset.kind = "change";
        const dot = document.createElement("span");
        dot.className = "status-dot";
        dot.setAttribute("aria-hidden", "true");
        const copy = document.createElement("div");
        copy.className = "overview-action-copy";
        const title = document.createElement("strong");
        title.className = "overview-action-title";
        title.textContent = String(event?.title || event?.type || "Operational change");
        const detail = document.createElement("span");
        detail.className = "overview-action-detail";
        const occurredAt = event?.occurredAt || event?.timestamp;
        detail.textContent = [event?.summary, occurredAt ? timeAgo(occurredAt) : "time unavailable"].filter(Boolean).join(" · ");
        copy.append(title, detail);
        const actions = document.createElement("div");
        actions.className = "overview-action-actions";
        actions.append(actionButton("VIEW", () => onNavigate?.(eventRoute(event))));
        article.append(dot, copy, actions);
        return article;
      });
      changesList.replaceChildren(...rows);
      renderedEventsKey = eventKey;
    }
    changesEmpty.hidden = events.length > 0;
    changesStatus.textContent = stale ? "24H · STALE" : `24H · ${events.length} RECORDED`;
  }

  async function loadEvents(force = false) {
    if (!active || !changesList || !widgetVisible("overview-recent-changes")) return;
    const requestedNode = node;
    const cached = eventCache.get(requestedNode);
    if (!force && cached && Date.now() - cached.loadedAt < EVENTS_TTL_MS) {
      renderEvents(cached.items);
      return;
    }
    if (!force && eventRequest && eventRequestNode === requestedNode) return;
    cancelEventRequest();
    const pending = new AbortController();
    eventRequest = pending;
    eventRequestNode = requestedNode;
    changesRefreshButton.disabled = true;
    changesStatus.textContent = "24H · LOADING";
    try {
      const to = new Date();
      const from = new Date(to.getTime() - 24 * 60 * 60 * 1000);
      const payload = typeof api?.listOperationalEvents !== "function"
        ? { items: [] }
        : await api.listOperationalEvents({ node: requestedNode, from: from.toISOString(), to: to.toISOString(), limit: MAX_RECENT_CHANGES, signal: pending.signal });
      if (pending.signal.aborted || eventRequest !== pending || node !== requestedNode) return;
      const items = Array.isArray(payload) ? payload : Array.isArray(payload?.items) ? payload.items : [];
      eventCache.set(requestedNode, { loadedAt: Date.now(), items });
      renderEvents(items);
    } catch (error) {
      if (error?.name === "AbortError" || pending.signal.aborted || eventRequest !== pending) return;
      if (cached) {
        renderEvents(cached.items, { stale: true });
        changesEmpty.textContent = "The latest refresh failed; showing the last successful operational history.";
      } else {
        changesList.replaceChildren();
        renderedEventsKey = "";
        changesEmpty.hidden = false;
        changesEmpty.textContent = error?.message || "Operational change history is unavailable.";
        changesStatus.textContent = "24H · UNAVAILABLE";
      }
    } finally {
      if (eventRequest === pending) {
        eventRequest = null;
        eventRequestNode = "";
        changesRefreshButton.disabled = false;
      }
    }
  }

  function renderTrend(payload) {
    const points = Array.isArray(payload?.points) ? payload.points : [];
    if (chart) {
      chart.data.labels = points.map((point) => historyLabel(field(point, "at", "At")));
      chart.data.datasets[0].data = points.map((point) => clamp(numeric(field(point, "cpuPercent", "CPUPercent"))));
      chart.data.datasets[1].data = points.map((point) => historyPercent(point, "memoryUsedBytes", "memoryTotalBytes"));
      chart.data.datasets[2].data = points.map((point) => historyPercent(point, "diskUsedBytes", "diskTotalBytes"));
      chart.update("none");
    }
    const sourceCount = numeric(payload?.sourcePointCount) || points.length;
    resolutionLabel.textContent = `24H · ${String(payload?.resolution || "auto").toUpperCase()}${sourceCount > points.length ? ` · ${points.length}/${sourceCount}` : ""}`;
    setTrendStatus(points.length ? "" : "No historical samples are available for this node.");
  }

  async function loadTrend(force = false) {
    if (!active || !widgetVisible("overview-trend") || typeof api?.systemHistory !== "function") return;
    const requestedNode = node;
    if (request && requestNode !== requestedNode) {
      cancelTrendRequest();
    }
    const cached = cache.get(requestedNode);
    if (!force && cached && Date.now() - cached.loadedAt < HISTORY_TTL_MS) {
      renderTrend(cached.payload);
      return;
    }
    if (!force && retryAfter.get(requestedNode) > Date.now()) {
      setTrendStatus("History retry pending; retrying shortly.", "error");
      return;
    }
    if (force) retryAfter.delete(requestedNode);
    if (!force && request?.signal && requestNode === requestedNode) return;
    request?.abort();
    const pending = new AbortController();
    request = pending;
    requestNode = requestedNode;
    chartWrap?.setAttribute("aria-busy", "true");
    refreshButton.disabled = true;
    setTrendStatus("Loading 24-hour history…");
    try {
      const payload = await api.systemHistory(requestedNode, "24h", pending.signal);
      if (pending.signal.aborted || request !== pending || node !== requestedNode) return;
      cache.set(requestedNode, { loadedAt: Date.now(), payload });
      retryAfter.delete(requestedNode);
      renderTrend(payload);
    } catch (error) {
      if (error?.name === "AbortError") return;
      if (request !== pending || node !== requestedNode) return;
      retryAfter.set(requestedNode, Date.now() + HISTORY_RETRY_DELAY_MS);
      resolutionLabel.textContent = "24H · UNAVAILABLE";
      setTrendStatus(error?.message || "Unable to load 24-hour history.", "error");
    } finally {
      if (request === pending) {
        request = null;
        requestNode = "";
        chartWrap?.setAttribute("aria-busy", "false");
        refreshButton.disabled = false;
      }
    }
  }

  document.getElementById("overview-alerts-open")?.addEventListener("click", () => onNavigate?.({ workspace: "alerts", node, state: "firing" }));
  openHistoryButton?.addEventListener("click", () => onNavigate?.({ workspace: "history", node, range: "24h", kind: "system" }));
  refreshButton?.addEventListener("click", () => loadTrend(true));
  changesRefreshButton?.addEventListener("click", () => loadEvents(true));
  for (const button of document.querySelectorAll("[data-container-rank]")) {
    button.addEventListener("click", () => {
      containerRank = button.dataset.containerRank || "cpu";
      for (const peer of document.querySelectorAll("[data-container-rank]")) peer.setAttribute("aria-pressed", String(peer === button));
      renderTopContainers();
    });
  }
  noteInput?.addEventListener("input", () => {
    if (!latest.admin || typeof api?.updateOperatorNote !== "function") return;
    noteStatus.textContent = "SAVING…";
    window.clearTimeout(noteSaveTimer);
    noteSaveTimer = window.setTimeout(async () => {
      try {
        operatorNote = normalizeNote(await api.updateOperatorNote(noteInput.value.slice(0, 4096), operatorNote.revision));
        noteStatus.textContent = "SAVED";
        renderOperatorNote();
      } catch (error) {
        noteStatus.textContent = error?.status === 409 ? "CONFLICT — RELOAD" : "SAVE FAILED";
      }
    }, 500);
  });

  return {
    update(next) {
      latest = { ...latest, ...next };
      const attention = renderAttention();
      renderServicePulse();
      renderServiceGroups();
      renderTopContainers();
      renderStoragePools();
      renderLaunchpad();
      renderOperatorNote();
      if (active) {
        loadTrend();
        loadEvents();
        loadWidgetContent();
      }
      return attention;
    },
    applyPreferences(preferences = {}) {
      hiddenWidgets = new Set((Array.isArray(preferences.hiddenOverviewWidgets) ? preferences.hiddenOverviewWidgets : [])
        .filter((id) => id !== "overview-attention" && OVERVIEW_WIDGET_IDS.includes(id)));
      for (const id of OVERVIEW_WIDGET_IDS) {
        const widget = document.getElementById(id);
        if (!widget) continue;
        widget.hidden = hiddenWidgets.has(id);
        widget.setAttribute("data-widget-visible", String(!widget.hidden));
        const requestedSize = preferences.overviewWidgetSizes?.[id];
        if (["small", "medium", "full"].includes(requestedSize)) widget.dataset.widgetSize = requestedSize;
      }
      if (!widgetVisible("overview-trend")) {
        cancelTrendRequest();
        clearTrend();
      }
      if (!widgetVisible("overview-recent-changes")) cancelEventRequest();
      if (!widgetVisible("overview-quick-launchpad") && !widgetVisible("overview-operator-notes")) widgetContentLoaded = false;
      resizeChart();
      if (active) {
        if (widgetVisible("overview-trend")) loadTrend();
        if (widgetVisible("overview-recent-changes")) loadEvents();
        loadWidgetContent(true);
      }
    },
    setAdmin(value) {
      widgetMenuAuthenticated = Boolean(value);
      latest.admin = widgetMenuAuthenticated;
      renderOperatorNote();
      for (const trigger of widgetMenuTriggers.values()) trigger.hidden = !widgetMenuAuthenticated;
      if (!widgetMenuAuthenticated) closeWidgetPopover({ restoreFocus: false });
    },
    setAuthenticated(value) {
      this.setAdmin(value);
    },
    setNode(nextNode, label = "") {
      const next = nextNode || "local";
      if (next !== node) {
        cancelTrendRequest();
        cancelEventRequest();
        renderedEventsKey = "";
        clearTrend();
        setTrendStatus("Loading 24-hour history…");
        const cachedEvents = eventCache.get(next);
        if (cachedEvents) renderEvents(cachedEvents.items, { stale: Date.now() - cachedEvents.loadedAt >= EVENTS_TTL_MS });
        else {
          changesList?.replaceChildren();
          if (changesEmpty) {
            changesEmpty.hidden = false;
            changesEmpty.textContent = "Loading operational changes…";
          }
          if (changesStatus) changesStatus.textContent = "24H · WAITING";
        }
      }
      node = next;
      nodeName = label || node;
      nodeLabel.textContent = `NODE · ${nodeName.toUpperCase()}`;
      changesNodeLabel.textContent = `NODE · ${nodeName.toUpperCase()}`;
      if (active) {
        loadTrend();
        loadEvents();
      }
    },
    activate() {
      active = true;
      resizeChart();
      if (widgetVisible("overview-trend")) loadTrend();
      if (widgetVisible("overview-recent-changes")) loadEvents();
      loadWidgetContent();
    },
    deactivate() {
      active = false;
      cancelTrendRequest();
      cancelEventRequest();
    },
    destroy() {
      request?.abort();
      eventRequest?.abort();
      chart?.destroy();
      window.removeEventListener("resize", positionWidgetPopover);
    },
  };
}
