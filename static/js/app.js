import { DashboardApi, unwrapSnapshot } from "./api.js";
import { createContainersController } from "./containers.js";
import { DemoApi, startDemoFeed } from "./demo.js";
import { bytes, clamp, number, percent, rate, setProgress, setText, timeAgo, uptime } from "./format.js";
import { createMetricCharts } from "./metrics.js";
import { createServicesController } from "./services.js";
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
let lastAnnouncedConnection = null;
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
const servicesController = createServicesController({ api, toast });

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
  } else if (alerts.critical || services.down || containers.issue) {
    const issueCount = alerts.critical + services.down + containers.issue;
    setOverviewHealth("ACTION NEEDED", "down", `${issueCount} monitored ${issueCount === 1 ? "issue needs" : "issues need"} attention`);
  } else if (alerts.warning) {
    setOverviewHealth("DEGRADED", "degraded", `${alerts.warning} warning${alerts.warning === 1 ? "" : "s"} active`);
  } else {
    setOverviewHealth("HEALTHY", "up", services.unknown ? `${services.unknown} service${services.unknown === 1 ? " has" : "s have"} no health probe` : "All monitored systems operational");
  }
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
  elements["services-stale"].hidden = !stale;

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

function renderSnapshot(payload) {
  const envelope = payload || {};
  const data = normalize(unwrapSnapshot(envelope));
  latestCollectedAt = envelope.collectedAt || new Date().toISOString();
  renderSystem(data.system, data.disks, data.network);
  overviewSummary.services = servicesController.render(data.services);
  overviewSummary.containers = containersController.render(data.containers);
  overviewSummary.alerts = renderAlerts(data.alerts);
  setConnectionState("online", { collectedAt: latestCollectedAt });
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
    .then(renderSnapshot)
    .catch((error) => { if (!latestCollectedAt) setConnectionState("offline"); throw error; })
    .finally(() => { refreshing = null; });
  return refreshing;
}

function applySession(session = {}) {
  const identity = session.identity || session.user || {};
  const login = typeof identity === "string" ? identity : identity.login || identity.email || identity.name || session.login || "tailnet user";
  const role = String(session.role || "viewer").toLowerCase();
  const admin = role === "admin";
  document.body.classList.toggle("viewer", !admin);
  setText(elements["session-user"], login);
  setText(elements["session-role"], admin ? "ADMIN" : "VIEWER");
  const identityGroup = document.getElementById("session-identity");
  identityGroup.setAttribute("aria-label", `Signed in as ${login}, role ${admin ? "administrator" : "viewer"}`);
  identityGroup.title = `${login} · ${admin ? "ADMIN" : "VIEWER"}`;
  servicesController.setAdmin(admin);
  containersController.setAdmin(admin);
  terminal.setHostShellCapability(session.capabilities?.hostShell === true);
}

async function start() {
  elements["demo-badge"].hidden = !demo;
  try {
    applySession(await api.session());
  } catch (error) {
    applySession({ role: "viewer", identity: { login: "unauthenticated" } });
    toast(error?.message || "Unable to load the Tailscale session.", "error");
  }

  if (demo) {
    stopDemoFeed = startDemoFeed(renderSnapshot);
    return;
  }

  try { await refreshSnapshot(); } catch { /* WebSocket retry owns the live recovery path. */ }
  stream = new MetricsStream({
    onSnapshot: renderSnapshot,
    onState: setConnectionState,
    onError: (error) => console.warn("Metrics stream frame rejected:", error),
  });
  stream.start();
}

document.getElementById("alerts-jump").addEventListener("click", () => {
  const target = elements["alerts-card"].hidden ? document.getElementById("health-overview") : elements["alerts-card"];
  target.scrollIntoView({ behavior: "smooth", block: "start" });
});
window.addEventListener("beforeunload", () => {
  stream?.stop();
  stopDemoFeed?.();
  terminal.disconnect(false);
  charts.destroy();
});

start();
