import { clamp, safeHttpUrl, timeAgo } from "./format.js";

const HISTORY_TTL_MS = 5 * 60 * 1000;
const HISTORY_RETRY_DELAY_MS = 30 * 1000;
const MAX_ATTENTION = 5;
const PROBLEM_CONTAINERS = new Set(["crashed", "unhealthy", "dead", "restarting"]);
const PROBLEM_SERVICES = new Set(["down", "error", "unhealthy", "crashed", "degraded", "warning"]);
const ACTIONABLE_ALERTS = new Set(["critical", "error", "warning", "warn"]);

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

export function createOverviewController({ api, toast, onOpenAlerts, onOpenContainerTerminal }) {
  const attentionPanel = document.getElementById("overview-attention");
  const attentionTitle = document.getElementById("overview-attention-title");
  const attentionList = document.getElementById("overview-attention-list");
  const attentionEmpty = document.getElementById("overview-attention-empty");
  const attentionCount = document.getElementById("overview-attention-count");
  const servicePanel = document.getElementById("overview-service-pulse");
  const serviceList = document.getElementById("overview-service-pulse-list");
  const serviceEmpty = document.getElementById("overview-service-pulse-empty");
  const serviceCount = document.getElementById("overview-service-pulse-count");
  const chartWrap = document.getElementById("overview-trend-chart-wrap");
  const chartStatus = document.getElementById("overview-trend-status");
  const nodeLabel = document.getElementById("overview-trend-node");
  const resolutionLabel = document.getElementById("overview-trend-resolution");
  const refreshButton = document.getElementById("overview-trend-refresh");
  const chart = createTrendChart(document.getElementById("overview-trend-chart"));
  const cache = new Map();
  const retryAfter = new Map();
  let request = null;
  let requestNode = "";
  let active = false;
  let node = "local";
  let nodeName = "local";
  let latest = { services: [], containers: [], alerts: [], admin: false, remote: false, partial: false, connection: "connecting" };

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

  function clearTrend() {
    if (chart) {
      chart.data.labels = [];
      for (const dataset of chart.data.datasets) dataset.data = [];
      chart.update("none");
    }
    resolutionLabel.textContent = "24H · WAITING";
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
    if (item.container) appendContainerActions(actions, item.container, latest.admin);
    if (item.service) actions.append(actionButton("OPEN", () => openService(item.service)));
    if (!actions.childElementCount) actions.append(actionButton("ALERTS", () => onOpenAlerts?.()));
    article.append(dot, copy, actions);
    return article;
  }

  function renderAttention() {
    const incidents = collectOverviewIncidents(latest);
    const visible = incidents.slice(0, MAX_ATTENTION);
    attentionList.replaceChildren(...visible.map(createIncident));
    attentionCount.textContent = String(incidents.length);
    attentionEmpty.hidden = visible.length > 0;
    attentionPanel?.classList.toggle("is-empty", visible.length === 0);
    if (attentionTitle) attentionTitle.textContent = visible.length ? "NEEDS ATTENTION" : "ALL CLEAR";
    return {
      total: incidents.length,
      critical: incidents.filter((incident) => incident.level === "critical").length,
      warning: incidents.filter((incident) => incident.level === "warning").length,
    };
  }

  function renderServicePulse() {
    const services = Array.isArray(latest.services) ? latest.services : [];
    const probed = services
      .filter((service) => String(service?.probeUrl || service?.probeURL || "").trim())
      .sort((left, right) => {
        const leftProblem = PROBLEM_SERVICES.has(serviceState(left)) ? 0 : 1;
        const rightProblem = PROBLEM_SERVICES.has(serviceState(right)) ? 0 : 1;
        return leftProblem - rightProblem || numeric(right?.latencyMs) - numeric(left?.latencyMs);
      })
      .slice(0, MAX_ATTENTION);
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
    serviceCount.textContent = String(probed.length);
    serviceEmpty.hidden = rows.length > 0;
    if (servicePanel) servicePanel.hidden = rows.length === 0;
    if (latest.remote && !rows.length) serviceEmpty.textContent = "Service probes are managed from the Local node.";
    else serviceEmpty.textContent = "No services with a configured probe.";
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
    if (!active || typeof api?.systemHistory !== "function") return;
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

  document.getElementById("overview-alerts-open")?.addEventListener("click", () => onOpenAlerts?.());
  refreshButton?.addEventListener("click", () => loadTrend(true));

  return {
    update(next) {
      latest = { ...latest, ...next };
      const attention = renderAttention();
      renderServicePulse();
      if (active) loadTrend();
      return attention;
    },
    setNode(nextNode, label = "") {
      const next = nextNode || "local";
      if (next !== node) {
        cancelTrendRequest();
        clearTrend();
        setTrendStatus("Loading 24-hour history…");
      }
      node = next;
      nodeName = label || node;
      nodeLabel.textContent = `NODE · ${nodeName.toUpperCase()}`;
      if (active) loadTrend();
    },
    activate() {
      active = true;
      window.requestAnimationFrame(() => chart?.resize());
      loadTrend();
    },
    deactivate() {
      active = false;
      cancelTrendRequest();
    },
    destroy() {
      request?.abort();
      chart?.destroy();
    },
  };
}
