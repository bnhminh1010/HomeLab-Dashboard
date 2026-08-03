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

export function createContainersController({ terminal, api, toast, onLifecycle }) {
  const list = document.getElementById("containers-list");
  const empty = document.getElementById("containers-empty");
  const count = document.getElementById("containers-count");
  const filterCount = document.getElementById("containers-filter-count");
  const filterInput = document.getElementById("containers-filter-input");
  const filterButtons = [...document.querySelectorAll("[data-container-filter]")];
  const filterBar = document.getElementById("containers-filter-bar");
  const filterToggle = document.getElementById("containers-filter-toggle");
  const items = new Map();
  let admin = false;
  let nodeId = "local";
  let containers = [];
  let initialized = false;
  let filter = "all";
  let filterText = "";

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
    logs.textContent = "LIVE LOGS";
    logs.addEventListener("click", () => openTerminal(logs, article.container, "logs"));
    const exec = document.createElement("button");
    exec.type = "button";
    exec.className = "container-action";
    exec.textContent = "SHELL";
    exec.addEventListener("click", () => openTerminal(exec, article.container, "exec"));
    const restart = document.createElement("button");
    restart.type = "button";
    restart.className = "container-action";
    restart.textContent = "RESTART";
    restart.addEventListener("click", () => runLifecycle(restart, article.container, "restart"));
    const stop = document.createElement("button");
    stop.type = "button";
    stop.className = "container-action container-action-danger";
    stop.textContent = "STOP";
    stop.addEventListener("click", () => runLifecycle(stop, article.container, "stop"));
    actions.append(logs, exec, restart, stop);

    article.append(heading, subtitle, telemetry, actions);
    article.refs = { name, state, stateLabel, subtitle, telemetry, metrics, cpu, memory, ports, logs, exec, restart, stop };
    return article;
  }

  function updateItem(article, container) {
    article.container = container;
    article.dataset.containerId = container.id;
    article.dataset.state = container.state;
    const { name, state, stateLabel, subtitle, telemetry, metrics, cpu, memory, ports, logs, exec, restart, stop } = article.refs;
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
    const lifecycleAvailable = typeof api?.restartContainer === "function" && typeof api?.stopContainer === "function";
    const lifecycleReason = !lifecycleAvailable ? "Dashboard version does not support lifecycle actions" : !admin ? "Admin role required" : container.protected ? "Protected container" : !running ? "Container is not running" : "";
    restart.disabled = Boolean(lifecycleReason) || container.actions.restart !== true || !container.id;
    stop.disabled = Boolean(lifecycleReason) || container.actions.stop !== true || !container.id;
    restart.title = lifecycleReason || "Restart this container";
    stop.title = lifecycleReason || "Stop this container";
    restart.setAttribute("aria-label", `Restart ${container.name}`);
    stop.setAttribute("aria-label", `Stop ${container.name}`);
  }

  function summary() {
    const running = containers.filter((container) => RUNNING_STATES.has(container.state)).length;
    const issue = containers.filter((container) => ISSUE_STATES.has(container.state)).length;
    return { total: containers.length, running, issue, stopped: Math.max(0, containers.length - running - issue) };
  }

  function matchesFilter(container) {
    const haystack = [container.name, container.image, container.state].join(" ").toLowerCase();
    if (filterText && !haystack.includes(filterText)) return false;
    if (filter === "running") return RUNNING_STATES.has(container.state);
    if (filter === "attention") return ISSUE_STATES.has(container.state);
    if (filter === "stopped") return !RUNNING_STATES.has(container.state) && !ISSUE_STATES.has(container.state);
    return true;
  }

  function applyFilter() {
    let visible = 0;
    for (const container of containers) {
      const item = items.get(container.key);
      if (!item) continue;
      const matches = matchesFilter(container);
      item.hidden = !matches;
      if (matches) visible += 1;
    }
    filterCount.textContent = `${visible} / ${containers.length} SHOWN`;
    const activeFilters = Number(filter !== "all") + Number(Boolean(filterText));
    if (filterToggle) filterToggle.textContent = activeFilters ? `FILTERS · ${activeFilters}` : "FILTERS";
    list.hidden = containers.length === 0;
    empty.hidden = containers.length !== 0 && visible !== 0;
    if (containers.length > 0 && visible === 0) {
      empty.querySelector("strong").textContent = "No containers match this filter";
      empty.querySelector("span").textContent = "Choose All to restore the complete Podman inventory.";
    } else {
      empty.querySelector("strong").textContent = "No containers running";
      empty.querySelector("span").textContent = "The Podman host returned an empty inventory.";
    }
  }

  function setFilter(next) {
    filter = ["all", "attention", "running", "stopped"].includes(next) ? next : "all";
    for (const button of filterButtons) {
      const active = button.dataset.containerFilter === filter;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    }
    applyFilter();
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
    applyFilter();
    return summary();
  }

  async function openTerminal(control, container, mode) {
    if (!control || !container) return;
    control.disabled = true;
    try {
      await terminal.open({ mode, containerId: container.id, containerName: container.name, nodeId, invoker: control });
    } catch (error) {
      toast(error?.message || `Unable to open ${mode}.`, "error");
    } finally {
      const item = control.closest(".container-item");
      if (item?.container) updateItem(item, item.container);
    }
  }

  async function runLifecycle(control, container, action) {
    if (!admin || !control || !container || control.disabled) return;
    const verb = action === "stop" ? "Stop" : "Restart";
    if (!window.confirm(`${verb} ${container.name} on ${nodeId}? This changes the running workload.`)) return;
    control.disabled = true;
    try {
      if (action === "restart") await api.restartContainer(container.id, nodeId);
      else await api.stopContainer(container.id, nodeId);
      toast(`${container.name}: ${verb.toLowerCase()} requested.`, "success");
      await onLifecycle?.();
    } catch (error) {
      toast(error?.message || `Unable to ${action} ${container.name}.`, "error");
    } finally {
      const item = control.closest(".container-item");
      if (item?.container) updateItem(item, item.container);
    }
  }

  for (const button of filterButtons) button.addEventListener("click", () => setFilter(button.dataset.containerFilter));
  filterToggle?.addEventListener("click", () => {
    const expanded = filterToggle.getAttribute("aria-expanded") !== "true";
    filterToggle.setAttribute("aria-expanded", String(expanded));
    if (filterBar) filterBar.dataset.collapsed = String(!expanded);
    if (expanded) window.requestAnimationFrame(() => filterInput?.focus({ preventScroll: true }));
  });
  filterInput?.addEventListener("input", () => {
    filterText = filterInput.value.trim().toLowerCase();
    applyFilter();
  });

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
    open(container, mode, invoker) {
      return openTerminal(invoker, container, mode);
    },
    focusFilter() {
      if (filterToggle && filterBar) {
        filterToggle.setAttribute("aria-expanded", "true");
        filterBar.dataset.collapsed = "false";
      }
      filterInput?.focus({ preventScroll: true });
    },
    applyRoute({ state = "all", query = "" } = {}) {
      const nextQuery = String(query || "").slice(0, 160);
      if (filterInput) filterInput.value = nextQuery;
      filterText = nextQuery.trim().toLowerCase();
      setFilter(state);
      if (filterToggle && filterBar && (filter !== "all" || filterText)) {
        filterToggle.setAttribute("aria-expanded", "true");
        filterBar.dataset.collapsed = "false";
      }
    },
  };
}
