import { bytes, clamp, number, percent, setProgress, uptime } from "./format.js";

function normalizedContainer(container = {}) {
  const state = String(container.state || container.status || "unknown").toLowerCase();
  const health = String(container.health || container.healthStatus || "").toLowerCase();
  const restartCount = number(container.restartCount ?? container.restarts);
  const effectiveState = restartCount > 3
    ? "crashed"
    : ["unhealthy", "crashed", "dead"].includes(health) ? health : state;
  return {
    id: String(container.id || container.ID || ""),
    name: String(container.name || container.names?.[0] || "unnamed-container"),
    image: String(container.image || ""),
    state: effectiveState,
    uptimeSeconds: number(container.uptimeSeconds ?? container.uptime),
    cpuUsagePercent: number(container.cpuUsagePercent ?? container.cpuPercent),
    cpuNormalizedPercent: number(container.cpuNormalizedPercent ?? container.cpuUsagePercent ?? container.cpuPercent),
    memoryUsageBytes: number(container.memoryUsageBytes ?? container.memoryUsage),
    memoryLimitBytes: number(container.memoryLimitBytes ?? container.memoryLimit),
    ports: Array.isArray(container.ports) ? container.ports.map(String) : [],
    restartCount,
    protected: Boolean(container.protected),
    actions: container.actions || {},
  };
}

function metric(label, displayValue, progressValue, className = "") {
  const wrapper = document.createElement("div");
  wrapper.className = "mini-metric";
  const heading = document.createElement("div");
  const key = document.createElement("span");
  key.textContent = label;
  const value = document.createElement("span");
  value.textContent = displayValue;
  heading.append(key, value);
  const bar = document.createElement("div");
  bar.className = `progress ${className}`.trim();
  bar.setAttribute("role", "progressbar");
  bar.setAttribute("aria-label", `${label} ${displayValue}`);
  bar.setAttribute("aria-valuemin", "0");
  bar.setAttribute("aria-valuemax", "100");
  const fill = document.createElement("span");
  fill.className = "progress-fill";
  bar.append(fill);
  setProgress(bar, progressValue);
  wrapper.append(heading, bar);
  return wrapper;
}

export function createContainersController({ terminal, toast }) {
  const list = document.getElementById("containers-list");
  const empty = document.getElementById("containers-empty");
  const count = document.getElementById("containers-count");
  let admin = false;
  let containers = [];
  let initialized = false;

  function render(nextContainers) {
    initialized = true;
    containers = Array.isArray(nextContainers) ? nextContainers.map(normalizedContainer) : [];
    count.textContent = String(containers.length);
    list.setAttribute("aria-busy", "false");
    list.replaceChildren(...containers.map(containerItem));
    list.hidden = containers.length === 0;
    empty.hidden = containers.length !== 0;
  }

  function containerItem(container) {
    const article = document.createElement("article");
    article.className = "container-item";

    const heading = document.createElement("div");
    heading.className = "container-heading";
    const name = document.createElement("span");
    name.className = "container-name";
    name.textContent = container.name;
    name.title = container.name;
    const state = document.createElement("span");
    state.className = "container-state";
    state.dataset.state = container.state;
    const dot = document.createElement("span");
    dot.className = "status-dot";
    dot.setAttribute("aria-hidden", "true");
    const stateLabel = document.createElement("span");
    stateLabel.textContent = container.state;
    state.append(dot, stateLabel);
    heading.append(name, state);
    article.append(heading);

    const subtitle = document.createElement("div");
    subtitle.className = "container-subtitle";
    const runtime = container.state === "running" ? `Up ${uptime(container.uptimeSeconds)}` : container.state;
    subtitle.textContent = [runtime, container.image, container.restartCount ? `${container.restartCount} restarts` : ""].filter(Boolean).join(" · ");
    subtitle.title = subtitle.textContent;
    article.append(subtitle);

    if (container.state === "running" || container.state === "healthy") {
      const metrics = document.createElement("div");
      metrics.className = "container-metrics";
      const memoryPercent = container.memoryLimitBytes > 0 ? (container.memoryUsageBytes / container.memoryLimitBytes) * 100 : 0;
      const memoryText = container.memoryLimitBytes > 0
        ? `${bytes(container.memoryUsageBytes, 0)}/${bytes(container.memoryLimitBytes, 0)}${memoryPercent > 100 ? " ⚠" : ""}`
        : bytes(container.memoryUsageBytes, 0);
      metrics.append(
        metric("CPU", percent(container.cpuUsagePercent, 1), clamp(container.cpuNormalizedPercent)),
        metric("MEM", memoryText, clamp(memoryPercent), "progress-yellow"),
      );
      article.append(metrics);
    }

    if (container.ports.length) {
      const ports = document.createElement("div");
      ports.className = "container-ports";
      ports.textContent = `PORTS ${container.ports.join(", ")}`;
      ports.title = container.ports.join(", ");
      article.append(ports);
    }

    const actions = document.createElement("div");
    actions.className = "container-actions";
    const running = ["running", "healthy"].includes(container.state);
    const logs = document.createElement("button");
    logs.type = "button";
    logs.className = "container-action";
    logs.textContent = "▶ LOGS";
    logs.disabled = container.actions.logs === false || !container.id;
    logs.addEventListener("click", () => openTerminal(logs, container, "logs"));
    const exec = document.createElement("button");
    exec.type = "button";
    exec.className = "container-action";
    exec.textContent = "⌁ EXEC";
    exec.disabled = !admin || !running || container.protected || container.actions.exec === false || !container.id;
    exec.title = !admin ? "Admin role required" : container.protected ? "Protected container" : !running ? "Container is not running" : "Open container shell";
    exec.addEventListener("click", () => openTerminal(exec, container, "exec"));
    actions.append(logs, exec);
    article.append(actions);
    return article;
  }

  async function openTerminal(control, container, mode) {
    control.disabled = true;
    try {
      await terminal.open({ mode, containerId: container.id, containerName: container.name });
    } catch (error) {
      toast(error?.message || `Unable to open ${mode}.`, "error");
    } finally {
      control.disabled = mode === "exec" && (!admin || container.protected || !["running", "healthy"].includes(container.state));
    }
  }

  return {
    render,
    setAdmin(value) {
      admin = Boolean(value);
      if (initialized) render(containers);
    },
  };
}
