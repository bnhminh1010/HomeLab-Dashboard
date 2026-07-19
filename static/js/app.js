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

const elements = Object.fromEntries([
  "system-card", "system-title", "system-status", "header-hostname", "freshness", "freshness-text",
  "offline-banner", "offline-message", "session-user", "session-role", "demo-badge", "cpu-percent",
  "cpu-progress", "cpu-detail", "ram-percent", "ram-progress", "ram-detail", "disk-percent",
  "disk-progress", "disk-detail", "disk-device", "disk-warning", "network-interface", "network-down",
  "network-up", "uptime", "processes", "load-average", "io-read", "io-write", "io-read-progress",
  "io-write-progress", "alerts-list", "alerts-count",
  "services-stale",
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

function setConnectionState(state, detail = {}) {
  const chip = elements.freshness;
  const text = elements["freshness-text"];
  const banner = elements["offline-banner"];
  const message = elements["offline-message"];
  const hasData = Boolean(latestCollectedAt);
  const stale = ["stale", "offline"].includes(state) && hasData;
  document.body.classList.toggle("is-stale", ["stale", "offline"].includes(state) && hasData);
  document.body.classList.toggle("is-offline", state === "offline");
  elements["services-stale"].hidden = !stale;

  if (state === "online") {
    latestCollectedAt = detail.collectedAt || latestCollectedAt || new Date().toISOString();
    chip.dataset.state = "online";
    text.textContent = demo ? "DEMO LIVE" : "LIVE";
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
  servicesController.render(data.services);
  containersController.render(data.containers);
  renderAlerts(data.alerts);
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

function renderAlerts(alerts) {
  const items = Array.isArray(alerts) ? alerts : [];
  elements["alerts-count"].textContent = String(items.length);
  if (!items.length) {
    const empty = document.createElement("div");
    empty.className = "alert-item";
    empty.dataset.level = "info";
    const dot = document.createElement("span");
    dot.className = "status-dot";
    const content = document.createElement("div");
    const message = document.createElement("div");
    message.className = "alert-message";
    message.textContent = "All clear — no system alerts.";
    content.append(message);
    empty.append(dot, content);
    elements["alerts-list"].replaceChildren(empty);
    return;
  }
  const nodes = items.map((alert) => {
    const item = document.createElement("article");
    item.className = "alert-item";
    item.dataset.level = String(alert.level || "info").toLowerCase();
    const dot = document.createElement("span");
    dot.className = "status-dot";
    dot.setAttribute("aria-hidden", "true");
    const content = document.createElement("div");
    const message = document.createElement("div");
    message.className = "alert-message";
    message.textContent = String(alert.message || "System alert");
    const meta = document.createElement("div");
    meta.className = "alert-meta";
    meta.textContent = [alert.source, alert.occurredAt || alert.timestamp ? timeAgo(alert.occurredAt || alert.timestamp) : ""].filter(Boolean).join(" · ");
    content.append(message, meta);
    item.append(dot, content);
    return item;
  });
  elements["alerts-list"].replaceChildren(...nodes);
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
  servicesController.setAdmin(admin);
  containersController.setAdmin(admin);
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
  document.getElementById("alerts-card").scrollIntoView({ behavior: "smooth", block: "center" });
});
window.addEventListener("beforeunload", () => {
  stream?.stop();
  stopDemoFeed?.();
  terminal.disconnect(false);
  charts.destroy();
});

start();
