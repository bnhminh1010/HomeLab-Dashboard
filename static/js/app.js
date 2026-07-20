import { DashboardApi, unwrapSnapshot } from "./api.js";
import { createAlertsController } from "./alerts.js";
import { createContainersController } from "./containers.js";
import { DemoApi, startDemoFeed } from "./demo.js";
import { bytes, clamp, number, percent, rate, setProgress, setText, timeAgo, uptime } from "./format.js";
import { createHistoryController } from "./history.js";
import { createMetricCharts } from "./metrics.js";
import { createNodesController } from "./nodes.js";
import { createServicesController } from "./services.js";
import { createSettingsController } from "./settings.js";
import { MetricsStream } from "./socket.js";
import { createTerminalController } from "./terminal.js";

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
let sessionHostShellCapability = false;
let snapshotPartial = false;
const alertNodes = new Map();
const overviewSummary = {
  services: { total: 0, up: 0, down: 0, unknown: 0 },
  containers: { total: 0, running: 0, issue: 0, stopped: 0 },
  alerts: { total: 0, critical: 0, warning: 0 },
};

const elements = Object.fromEntries([
  "system-card", "system-title", "system-status", "header-hostname", "freshness", "freshness-text",
  "offline-banner", "offline-message", "session-user", "session-role", "demo-badge", "cpu-percent",
  "cpu-progress", "cpu-detail", "ram-percent", "ram-progress", "ram-detail", "disk-percent",
  "disk-progress", "disk-detail", "disk-device", "disk-warning", "network-interface", "network-down",
  "network-up", "uptime", "processes", "load-average", "io-read", "io-write", "io-read-progress",
  "io-write-progress", "alerts-list", "alerts-count",
  "services-stale", "alerts-card", "overview-health", "overview-health-detail",
  "overview-connection", "overview-updated", "overview-services", "overview-services-detail",
  "overview-containers", "overview-containers-detail", "dashboard-status",
  "snapshot-partial",
].map((id) => [id, document.getElementById(id)]));

function toast(message, level = "info") {
  const region = document.getElementById("toast-region");
  const item = document.createElement("div");
  item.className = "toast";
  item.dataset.level = level;
  item.textContent = String(message);
  region.append(item);
  window.setTimeout(() => item.remove(), 4500);
}

const terminal = createTerminalController({ api, demo, toast });
const containersController = createContainersController({ terminal, toast });
const servicesController = createServicesController({
  api,
  toast,
  onChanged: (services, summary) => {
    latestServices = services;
    overviewSummary.services = summary;
    if (selectedNodeId === "local") historyController.setResources(latestSelectedContainers, latestServices);
    updateOverview();
  },
});
const historyController = createHistoryController({ api, demo, toast });
const alertsController = createAlertsController({ api, demo, toast });
const nodesController = createNodesController({
  api,
  demo,
  toast,
  onSelect: selectNode,
});
const settingsController = createSettingsController({
  api,
  demo,
  toast,
  onApplied: async () => {
    await hydratePreferences();
    await Promise.allSettled([refreshSnapshot(), alertsController.refresh()]);
  },
});

function setSystemBadge(label, state) {
  const dot = document.createElement("span");
  dot.className = "status-dot";
  dot.setAttribute("aria-hidden", "true");
  elements["system-status"].className = `badge badge-${state}`;
  elements["system-status"].replaceChildren(dot, document.createTextNode(label));
}

function announce(message) {
  setText(elements["dashboard-status"], message);
}

function setOverviewHealth(label, state, detail) {
  const dot = document.createElement("span");
  dot.className = "status-dot";
  dot.setAttribute("aria-hidden", "true");
  elements["overview-health"].dataset.state = state;
  elements["overview-health"].replaceChildren(dot, document.createTextNode(label));
  setText(elements["overview-health-detail"], detail);
}

function updateOverview() {
  const services = overviewSummary.services;
  const containers = overviewSummary.containers;
  const alerts = overviewSummary.alerts;
  setText(elements["overview-services"], `${services.up} / ${services.total} UP`);
  setText(
    elements["overview-services-detail"],
    services.down ? `${services.down} need attention` : services.unknown ? `${services.unknown} without a health probe` : services.total ? "All probes responding" : "No services configured",
  );
  setText(elements["overview-containers"], `${containers.running} / ${containers.total} RUNNING`);
  setText(
    elements["overview-containers-detail"],
    containers.issue ? `${containers.issue} with runtime issues` : containers.stopped ? `${containers.stopped} stopped` : containers.total ? "Podman inventory healthy" : "No containers reported",
  );

  if (["stale", "offline"].includes(connectionState) && latestCollectedAt) {
    setOverviewHealth("STALE", "degraded", "Last known data is preserved while metrics reconnect");
  } else if (connectionState !== "online") {
    setOverviewHealth("WAITING", "waiting", "Connecting to the dashboard");
  } else if (snapshotPartial) {
    setOverviewHealth("PARTIAL", "degraded", "The latest frame was size-limited; omitted inventory is not treated as healthy");
  } else if (alerts.critical || services.down || containers.issue) {
    const issueCount = alerts.critical + services.down + containers.issue;
    setOverviewHealth("ACTION NEEDED", "down", `${issueCount} monitored ${issueCount === 1 ? "issue needs" : "issues need"} attention`);
  } else if (alerts.warning) {
    setOverviewHealth("DEGRADED", "degraded", `${alerts.warning} warning${alerts.warning === 1 ? "" : "s"} active`);
  } else {
    setOverviewHealth("HEALTHY", "up", services.unknown ? `${services.unknown} service${services.unknown === 1 ? " has" : "s have"} no health probe` : "All monitored systems operational");
  }
}

function setSnapshotCompleteness(envelope) {
  snapshotPartial = envelope?.truncated === true;
  const sources = Array.isArray(envelope?.truncatedSources) ? envelope.truncatedSources.map(String) : [];
  elements["snapshot-partial"].hidden = !snapshotPartial;
  elements["snapshot-partial"].title = snapshotPartial
    ? `Snapshot was truncated${sources.length ? `: ${sources.join(", ")}` : " to fit the frame limit"}`
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
      setText(elements["system-title"], "Unable to reach server");
      setText(elements["cpu-detail"], "Waiting for the dashboard backend");
    }
  }

  const connectionLabel = state === "online" ? (demo ? "DEMO LIVE" : "LIVE") : state === "connected" ? "SYNCING" : state.toUpperCase();
  chip.setAttribute("aria-label", `Metrics stream: ${connectionLabel.toLowerCase()}`);
  elements["overview-connection"].dataset.state = state;
  setText(elements["overview-connection"], connectionLabel);
  setText(elements["overview-updated"], latestCollectedAt ? `Updated ${timeAgo(latestCollectedAt)}` : "No snapshot yet");
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
  overviewSummary.services = servicesController.render(selectedNodeId === "local" ? data.services : latestServices);
  overviewSummary.containers = containersController.render(data.containers);
  historyController.setResources(data.containers, data.services);
  overviewSummary.alerts = renderAlerts(data.alerts);
  setConnectionState(state, { collectedAt: latestCollectedAt });
}

function renderLocalSnapshot(payload) {
  latestLocalEnvelope = payload;
  latestServices = normalize(unwrapSnapshot(payload)).services;
  if (demo) localConnectionState = "online";
  if (selectedNodeId === "local") renderSelectedSnapshot(payload, "online");
  else {
    overviewSummary.services = servicesController.render(latestServices);
    updateOverview();
  }
}

function renderUnavailableNode(state) {
  const name = state?.node?.displayName || state?.node?.hostname || selectedNodeId;
  latestCollectedAt = state?.lastSeenAt || state?.node?.lastSeenAt || null;
  setSnapshotCompleteness(null);
  setConnectionState("offline");
  setText(elements["system-title"], name || "Remote node");
  setText(elements["header-hostname"], state?.node?.hostname || name || "remote");
  setText(elements["cpu-percent"], "—");
  setText(elements["cpu-detail"], "No remote metrics snapshot received");
  setText(elements["ram-percent"], "—");
  setText(elements["ram-detail"], "Waiting for the node agent");
  elements["ram-percent"].classList.remove("is-over-limit");
  setText(elements["disk-percent"], "—");
  setText(elements["disk-detail"], "—");
  setText(elements["disk-device"], "—");
  elements["disk-warning"].hidden = true;
  elements["disk-progress"].dataset.level = "";
  setText(elements["network-interface"], "—");
  setText(elements["network-down"], "—");
  setText(elements["network-up"], "—");
  setText(elements.uptime, "—");
  setText(elements.processes, "—");
  setText(elements["load-average"], "—");
  setText(elements["io-read"], "Idle");
  setText(elements["io-write"], "Idle");
  setProgress(elements["cpu-progress"], 0);
  setProgress(elements["ram-progress"], 0);
  setProgress(elements["disk-progress"], 0);
  setProgress(elements["io-read-progress"], 0);
  setProgress(elements["io-write-progress"], 0);
  overviewSummary.services = servicesController.render(latestServices);
  overviewSummary.containers = containersController.render([]);
  latestSelectedContainers = [];
  historyController.setResources([], []);
  overviewSummary.alerts = renderAlerts([]);
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
  terminal.setNode?.(selectedNodeId, nodeLabel);
  if (nodeChanged) {
    charts.reset();
    historyController.setNode(selectedNodeId);
    alertsController.setNode(selectedNodeId);
  }
  terminal.setHostShellCapability?.(sessionHostShellCapability);
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
  setText(elements["system-title"], hostname);
  setText(elements["header-hostname"], hostname);
  setText(elements["cpu-percent"], percent(cpuUsage, 1));
  const cpuMaximum = cpuUsage > 100 ? Math.max(100, cpuCores * 100, cpuUsage) : 100;
  setProgress(elements["cpu-progress"], cpuUsage, cpuMaximum);
  const temperatureLabel = Number.isFinite(Number(temperature)) ? `${Number(temperature).toFixed(0)}°C${Number(temperature) > 80 ? " 🔥" : ""}` : "temp n/a";
  setText(elements["cpu-detail"], `${frequency ? `${frequency.toFixed(0)} MHz` : "freq n/a"} · ${cpuCores || "—"} cores · ${temperatureLabel}`);

  const memoryOverLimit = totalMemory > 0 && usedMemory > totalMemory;
  setText(elements["ram-percent"], `${percent(ramUsage, 1)}${memoryOverLimit ? " ⚠" : ""}`);
  elements["ram-percent"].classList.toggle("is-over-limit", memoryOverLimit);
  setProgress(elements["ram-progress"], ramUsage);
  const swapPercent = swapTotal > 0 ? (swapUsed / swapTotal) * 100 : 0;
  setText(elements["ram-detail"], `${bytes(usedMemory)} / ${bytes(totalMemory)} · swap ${percent(swapPercent, 0)}`);

  setText(elements["disk-percent"], percent(diskUsage, 1));
  setProgress(elements["disk-progress"], diskUsage);
  if (diskUsage > 90) elements["disk-progress"].dataset.level = "hot";
  setText(elements["disk-detail"], `${bytes(diskUsed)} / ${bytes(diskTotal)}`);
  setText(elements["disk-device"], `${disk.device || "unknown device"} · ${disk.mountPoint || "/"}`);
  elements["disk-warning"].hidden = diskUsage <= 90;

  setText(elements["network-interface"], network.interface || network.name || "default");
  setText(elements["network-down"], rate(network.rxBytesPerSecond ?? network.bytesRecv));
  setText(elements["network-up"], rate(network.txBytesPerSecond ?? network.bytesSent));
  setText(elements.uptime, uptime(system.uptimeSeconds ?? system.uptime));
  setText(elements.processes, number(system.processCount ?? system.processes));
  const load = system.loadAverages || system.load || [];
  setText(elements["load-average"], Array.isArray(load) && load.length ? load.slice(0, 3).map((item) => number(item).toFixed(2)).join(" ") : "—");

  setText(elements["io-read"], rate(readRate));
  setText(elements["io-write"], rate(writeRate));
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
  const message = document.createElement("div");
  message.className = "alert-message";
  const meta = document.createElement("div");
  meta.className = "alert-meta";
  content.append(message, meta);
  item.append(dot, content);
  item.refs = { message, meta };
  return item;
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
  elements["alerts-card"].hidden = items.length === 0;
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
    node.refs.meta.textContent = [alert.source, alert.occurredAt || alert.timestamp ? timeAgo(alert.occurredAt || alert.timestamp) : ""].filter(Boolean).join(" · ");
    elements["alerts-list"].append(node);
  }
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
  sessionAdmin = admin;
  sessionHostShellCapability = session.capabilities?.hostShell === true;
  document.body.classList.toggle("viewer", !admin);
  document.body.classList.toggle("admin", admin);
  setText(elements["session-user"], login);
  setText(elements["session-role"], admin ? "ADMIN" : "VIEWER");
  const identityGroup = document.getElementById("session-identity");
  identityGroup.setAttribute("aria-label", `Signed in as ${login}, role ${admin ? "administrator" : "viewer"}`);
  identityGroup.title = `${login} · ${admin ? "ADMIN" : "VIEWER"}`;
  servicesController.setAdmin(admin);
  containersController.setAdmin(admin);
  alertsController.setAdmin(admin);
  nodesController.setAdmin(admin);
  settingsController.setAdmin(admin);
  terminal.setHostShellCapability(sessionHostShellCapability);
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
  if (demo || typeof api.preferences !== "function") return;
  try {
    const preferences = await api.preferences();
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
  try {
	applySession(await api.session());
	if (demo) await nodesController.refresh();
	await hydratePreferences();
  } catch (error) {
    applySession({ role: "viewer", identity: { login: "unauthenticated" } });
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
window.addEventListener("beforeunload", () => {
  stream?.stop();
  stopDemoFeed?.();
  terminal.disconnect(false);
  charts.destroy();
  historyController.destroy();
  nodesController.destroy();
  window.clearInterval(sessionKeepalive);
  window.clearTimeout(sessionRenewalTimer);
  window.clearTimeout(preferenceSaveTimer);
});

start();
