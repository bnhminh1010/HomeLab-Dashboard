import { bytes, clamp, number, percent, setProgress, uptime } from "./format.js";

const RUNNING_STATES = new Set(["running", "healthy"]);
const ISSUE_STATES = new Set(["crashed", "unhealthy", "dead", "restarting"]);

function normalizedContainer(container = {}, index = 0) {
  const state = String(container.state || container.status || "unknown").toLowerCase();
  const health = String(container.health || container.healthStatus || "").toLowerCase();
  const restartCount = number(container.restartCount ?? container.restarts);
  const effectiveState = restartCount > 3
    ? "crashed"
    : ["unhealthy", "crashed", "dead"].includes(health) ? health : state;
  const id = String(container.id || container.ID || "");
  const name = String(container.name || container.names?.[0] || "unnamed-container");
  return {
    id,
    key: id || `${name}-${index}`,
    originalIndex: index,
    name,
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

function statePriority(state) {
  if (ISSUE_STATES.has(state)) return 0;
  if (!RUNNING_STATES.has(state)) return 1;
  return 2;
}

function createMetric(label, className = "") {
  const wrapper = document.createElement("div");
  wrapper.className = "mini-metric";
  const heading = document.createElement("div");
  const key = document.createElement("span");
  key.textContent = label;
  const value = document.createElement("span");
  heading.append(key, value);
  const bar = document.createElement("div");
  bar.className = `progress ${className}`.trim();
  bar.setAttribute("role", "progressbar");
  bar.setAttribute("aria-valuemin", "0");
  bar.setAttribute("aria-valuemax", "100");
  const fill = document.createElement("span");
  fill.className = "progress-fill";
  bar.append(fill);
  wrapper.append(heading, bar);
  wrapper.refs = { value, bar, label };
  return wrapper;
}

function updateMetric(metric, displayValue, progressValue) {
  metric.refs.value.textContent = displayValue;
  metric.refs.bar.setAttribute("aria-label", `${metric.refs.label} ${displayValue}`);
  setProgress(metric.refs.bar, progressValue);
}

export function createContainersController({ terminal, toast }) {
  const list = document.getElementById("containers-list");
  const empty = document.getElementById("containers-empty");
  const count = document.getElementById("containers-count");
  const items = new Map();
  let admin = false;
  let nodeId = "local";
  let containers = [];
  let initialized = false;

  function createItem() {
    const article = document.createElement("article");
    article.className = "container-item";
    const heading = document.createElement("div");
    heading.className = "container-heading";
    const name = document.createElement("span");
    name.className = "container-name";
    const state = document.createElement("span");
    state.className = "container-state";
    const dot = document.createElement("span");
    dot.className = "status-dot";
    dot.setAttribute("aria-hidden", "true");
    const stateLabel = document.createElement("span");
    state.append(dot, stateLabel);
    heading.append(name, state);

    const subtitle = document.createElement("div");
    subtitle.className = "container-subtitle";

    const telemetry = document.createElement("div");
    telemetry.className = "container-telemetry";
    const metrics = document.createElement("div");
    metrics.className = "container-metrics";
    const cpu = createMetric("CPU");
    const memory = createMetric("MEM", "progress-yellow");
    metrics.append(cpu, memory);
    const ports = document.createElement("div");
    ports.className = "container-ports";
    telemetry.append(metrics, ports);

    const actions = document.createElement("div");
    actions.className = "container-actions";
    const logs = document.createElement("button");
    logs.type = "button";
    logs.className = "container-action";
    logs.textContent = "▶ LOGS";
    logs.addEventListener("click", () => openTerminal(logs, article.container, "logs"));
    const exec = document.createElement("button");
    exec.type = "button";
    exec.className = "container-action";
    exec.textContent = "⌁ CONTAINER SHELL";
    exec.addEventListener("click", () => openTerminal(exec, article.container, "exec"));
    actions.append(logs, exec);

    article.append(heading, subtitle, telemetry, actions);
    article.refs = { name, state, stateLabel, subtitle, telemetry, metrics, cpu, memory, ports, logs, exec };
    return article;
  }

  function updateItem(article, container) {
    article.container = container;
    article.dataset.containerId = container.id;
    article.dataset.state = container.state;
    const { name, state, stateLabel, subtitle, telemetry, metrics, cpu, memory, ports, logs, exec } = article.refs;
    name.textContent = container.name;
    name.title = container.name;
    state.dataset.state = container.state;
    stateLabel.textContent = container.state;
    state.setAttribute("aria-label", `Status ${container.state}`);

    const runtime = RUNNING_STATES.has(container.state) ? `Up ${uptime(container.uptimeSeconds)}` : container.state;
    subtitle.textContent = [runtime, container.image, container.restartCount ? `${container.restartCount} restarts` : ""].filter(Boolean).join(" · ");
    subtitle.title = subtitle.textContent;

    const running = RUNNING_STATES.has(container.state);
    metrics.hidden = !running;
    telemetry.hidden = !running && container.ports.length === 0;
    const memoryPercent = container.memoryLimitBytes > 0 ? (container.memoryUsageBytes / container.memoryLimitBytes) * 100 : 0;
    const memoryText = container.memoryLimitBytes > 0
      ? `${bytes(container.memoryUsageBytes, 0)}/${bytes(container.memoryLimitBytes, 0)}${memoryPercent > 100 ? " ⚠" : ""}`
      : bytes(container.memoryUsageBytes, 0);
    updateMetric(cpu, percent(container.cpuUsagePercent, 1), clamp(container.cpuNormalizedPercent));
    updateMetric(memory, memoryText, clamp(memoryPercent));

    ports.hidden = container.ports.length === 0;
    ports.textContent = container.ports.length ? `PORTS ${container.ports.join(", ")}` : "";
    ports.title = container.ports.join(", ");
    logs.disabled = container.actions.logs === false || !container.id;
    logs.setAttribute("aria-label", `Open logs for ${container.name}`);
    exec.disabled = !admin || !running || container.protected || container.actions.exec === false || !container.id;
    exec.title = !admin ? "Admin role required" : container.protected ? "Protected container" : !running ? "Container is not running" : "Open container shell";
    exec.setAttribute("aria-label", `Open shell in ${container.name}`);
  }

  function summary() {
    const running = containers.filter((container) => RUNNING_STATES.has(container.state)).length;
    const issue = containers.filter((container) => ISSUE_STATES.has(container.state)).length;
    return { total: containers.length, running, issue, stopped: Math.max(0, containers.length - running - issue) };
  }

  function render(nextContainers) {
    initialized = true;
    list.querySelectorAll(".skeleton-container").forEach((node) => node.remove());
    containers = Array.isArray(nextContainers) ? nextContainers.map(normalizedContainer) : [];
    containers.sort((a, b) => statePriority(a.state) - statePriority(b.state) || a.originalIndex - b.originalIndex);
    count.textContent = String(containers.length);
    list.setAttribute("aria-busy", "false");

    const nextKeys = new Set(containers.map((container) => container.key));
    for (const [key, item] of items) {
      if (nextKeys.has(key)) continue;
      item.remove();
      items.delete(key);
    }
    for (const container of containers) {
      let item = items.get(container.key);
      if (!item) {
        item = createItem();
        items.set(container.key, item);
      }
      updateItem(item, container);
      list.append(item);
    }
    list.hidden = containers.length === 0;
    empty.hidden = containers.length !== 0;
    return summary();
  }

  async function openTerminal(control, container, mode) {
    if (!container) return;
    control.disabled = true;
    try {
      await terminal.open({ mode, containerId: container.id, containerName: container.name, nodeId, invoker: control });
    } catch (error) {
      toast(error?.message || `Unable to open ${mode}.`, "error");
    } finally {
      updateItem(control.closest(".container-item"), control.closest(".container-item").container);
    }
  }

  return {
    render,
    setAdmin(value) {
      admin = Boolean(value);
      if (initialized) return render(containers);
      return summary();
    },
    setNode(value) {
      nodeId = value || "local";
    },
  };
}
