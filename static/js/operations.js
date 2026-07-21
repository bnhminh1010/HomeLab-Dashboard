import { bytes, percent, timeAgo } from "./format.js";

const SLO_WINDOWS = new Set([7, 30, 90]);
const TIMELINE_RANGES = new Map([
  ["1h", 60 * 60_000], ["6h", 6 * 60 * 60_000], ["24h", 24 * 60 * 60_000],
  ["7d", 7 * 24 * 60 * 60_000], ["30d", 30 * 24 * 60 * 60_000], ["90d", 90 * 24 * 60 * 60_000],
]);

function field(value, camel, exported) {
  return value?.[camel] ?? value?.[exported];
}

function array(payload) {
  return Array.isArray(payload) ? payload : (Array.isArray(payload?.items) ? payload.items : []);
}

function statusOf(service = {}) {
  return String(service.status || service.health?.status || "unknown").toLowerCase();
}

function healthLevel(status) {
  if (["up", "running", "healthy"].includes(status)) return "up";
  if (["down", "error", "unhealthy", "crashed"].includes(status)) return "critical";
  if (["degraded", "warning"].includes(status)) return "warning";
  return "unknown";
}

function durationLabel(seconds) {
  const value = Math.max(0, Number(seconds) || 0);
  if (value < 60) return `${Math.round(value)}s`;
  if (value < 3600) return `${Math.round(value / 60)}m`;
  if (value < 86400) return `${(value / 3600).toFixed(1)}h`;
  return `${(value / 86400).toFixed(1)}d`;
}

function sloTargetLabel(value) {
  const target = Number(value);
  return (Number.isFinite(target) ? target : 99.5).toFixed(3).replace(/0+$/, "").replace(/\.$/, "");
}

function dateLabel(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "unknown time" : date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function sameNodeSnapshot(snapshot, fallbackID = "local") {
  const data = snapshot?.data || snapshot || {};
  return {
    id: fallbackID,
    name: data?.system?.hostname || fallbackID,
    online: true,
    stale: false,
    snapshot: { data },
    lastSeenAt: snapshot?.collectedAt || new Date().toISOString(),
  };
}

function demoSLOs(services, window) {
  return services.map((service, index) => {
    const availability = 99.9 - (index % 5) * 0.12;
    const target = 99.5;
    const remaining = Math.max(-20, 100 - (target - availability) * 200);
    return {
      serviceId: service.id || service.ID, name: service.name, nodeId: "local",
      policy: { targetPercent: target, windowDays: window }, known: true,
      availabilityPercent: availability, errorBudgetRemainingPercent: remaining,
      errorBudgetRemainingSeconds: remaining * 45, atRisk: remaining <= 20,
      errorBudgetExhausted: remaining <= 0, observedSeconds: window * 86400,
    };
  });
}

export function createOperationsController({ api, demo = false, toast, onSelectNode, onOpenServices }) {
  const sloList = document.getElementById("slo-list");
  const sloStatus = document.getElementById("slo-status");
  const sloButtons = [...document.querySelectorAll("[data-slo-window]")];
  const timeline = document.getElementById("history-events-list");
  const markers = document.getElementById("history-event-markers");
  const timelineContext = document.getElementById("history-timeline-context");
  const eventForm = document.getElementById("operations-event-form");
  const nodesGrid = document.getElementById("nodes-workspace-grid");
  const nodesStatus = document.getElementById("nodes-workspace-status");
  const nodesRefresh = document.getElementById("nodes-workspace-refresh");
  const checksList = document.getElementById("checks-list");
  const checksStatus = document.getElementById("checks-status");
  const canvas = document.getElementById("topology-canvas");
  const topologyCount = document.getElementById("topology-count");
  const topologyStatus = document.getElementById("topology-status");
  const topologyEdgeList = document.getElementById("topology-edge-list");
  const topologyForm = document.getElementById("topology-form");
  const topologyDependent = document.getElementById("topology-dependent");
  const topologyDependency = document.getElementById("topology-dependency");
  const topologyLabel = document.getElementById("topology-label");
  let services = [];
  let localSnapshot = null;
  let selectedNode = "local";
  let admin = false;
  let sloWindow = 30;
  let topology = [];
  let nodes = [];
  let serviceFingerprint = "";
  let topologyHealthFingerprint = "";
  let timelineRange = "24h";
  let displayedTimelineRange = "";
  let hasTimelineResult = false;
  let hasSLOResult = false;
  let hasChecksResult = false;
  let hasTopologyResult = false;
  let controllers = { slo: null, events: null, nodes: null, checks: null, topology: null };

  function abort(name) {
    controllers[name]?.abort();
    controllers[name] = null;
  }

  function setStatus(element, message, level = "") {
    if (!element) return;
    element.textContent = message;
    element.dataset.level = level;
  }

  function setStale(content, status, stale) {
    content?.toggleAttribute("data-stale", stale);
    status?.toggleAttribute("data-stale", stale);
  }

  function setTimelineStale(stale) {
    timeline?.toggleAttribute("data-stale", stale);
    markers?.toggleAttribute("data-stale", stale);
    if (!timelineContext) return;
    const rangeChanged = stale && displayedTimelineRange && displayedTimelineRange !== timelineRange;
    timelineContext.textContent = rangeChanged
      ? `${timelineRange.toUpperCase()} · showing ${displayedTimelineRange.toUpperCase()}`
      : timelineRange.toUpperCase();
    timelineContext.toggleAttribute("data-stale", stale);
  }

  function setSLOWindow(next) {
    sloWindow = SLO_WINDOWS.has(Number(next)) ? Number(next) : 30;
    for (const button of sloButtons) {
      const active = Number(button.dataset.sloWindow) === sloWindow;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    }
    refreshSLO();
  }

  function serviceName(id) {
    return services.find((service) => String(service.id || service.ID) === String(id))?.name || id || "Unknown service";
  }

  function renderSLO(items) {
    sloList.replaceChildren();
    hasSLOResult = true;
    setStale(sloList, sloStatus, false);
    if (!items.length) {
      setStatus(sloStatus, services.length ? "No SLO history is available yet; probes will populate it over time." : "Add a service to establish a service objective.");
      return;
    }
    for (const report of items) {
      const article = document.createElement("article");
      article.className = "slo-item";
      const head = document.createElement("div");
      head.className = "slo-item-head";
      const name = document.createElement("strong");
      name.textContent = report.name || serviceName(report.serviceId);
      const state = document.createElement("span");
      state.className = "badge";
      const remaining = Number(report.errorBudgetRemainingPercent);
      const known = Boolean(report.known);
      state.dataset.level = !known ? "unknown" : report.errorBudgetExhausted ? "critical" : report.atRisk ? "warning" : "up";
      state.textContent = !known ? "NO DATA" : report.errorBudgetExhausted ? "BUDGET EXHAUSTED" : report.atRisk ? "AT RISK" : "ON TARGET";
      head.append(name, state);

      const metric = document.createElement("div");
      metric.className = "slo-metric mono";
      const actual = document.createElement("strong");
      actual.textContent = known ? `${Number(report.availabilityPercent).toFixed(3)}%` : "—";
      const target = document.createElement("span");
      target.textContent = `TARGET ${sloTargetLabel(report.policy?.targetPercent)}% · ${sloWindow}D`;
      metric.append(actual, target);
      const progress = document.createElement("div");
      progress.className = "progress slo-budget";
      progress.dataset.level = state.dataset.level === "critical" ? "critical" : state.dataset.level === "warning" ? "warning" : "";
      const fill = document.createElement("span");
      fill.className = "progress-fill";
      progress.append(fill);
      const budget = Number.isFinite(remaining) ? Math.max(0, Math.min(100, remaining)) : 0;
      progress.style.setProperty("--progress", String(budget));
      progress.setAttribute("aria-label", known ? `${budget.toFixed(1)} percent error budget remaining` : "No observed SLO data");
      const detail = document.createElement("p");
      detail.className = "slo-detail mono";
      detail.textContent = known
        ? `${budget.toFixed(1)}% budget remaining · ${durationLabel(report.errorBudgetRemainingSeconds)} left`
        : "Unknown observations are excluded until a probe produces a known state.";
      article.append(head, metric, progress, detail);
      if (admin) {
        const controls = document.createElement("form");
        controls.className = "slo-controls";
        const targetInput = document.createElement("input");
        targetInput.type = "number"; targetInput.min = "90"; targetInput.max = "99.999"; targetInput.step = "0.001";
        targetInput.value = String(report.policy?.targetPercent ?? 99.5); targetInput.setAttribute("aria-label", `SLO target for ${name.textContent}`);
        const windowSelect = document.createElement("select");
        for (const optionValue of [7, 30, 90]) {
          const option = document.createElement("option"); option.value = String(optionValue); option.textContent = `${optionValue}D`;
          option.selected = Number(report.policy?.windowDays) === optionValue; windowSelect.append(option);
        }
        const save = document.createElement("button"); save.type = "submit"; save.className = "text-button"; save.textContent = "SAVE POLICY";
        controls.append(targetInput, windowSelect, save);
        controls.addEventListener("submit", async (event) => {
          event.preventDefault();
          save.disabled = true;
          try {
            if (!demo) await api.updateServiceSLO(report.serviceId, { targetPercent: Number(targetInput.value), windowDays: Number(windowSelect.value) });
            toast("Service objective saved.");
            refreshSLO();
          } catch (error) {
            toast(error?.message || "Unable to save service objective.", "error");
          } finally { save.disabled = false; }
        });
        article.append(controls);
      }
      sloList.append(article);
    }
    setStatus(sloStatus, `Dashboard probe availability over ${sloWindow} days. Degraded and down time consume the error budget.`, "");
  }

  async function refreshSLO() {
    abort("slo");
    if (!sloList) return;
    const pending = new AbortController(); controllers.slo = pending;
    sloList.setAttribute("aria-busy", "true");
    setStatus(sloStatus, "Loading dashboard-local service objectives…");
    try {
      // Service health probes run from the dashboard's local collector, so
      // their history is intentionally not reinterpreted as remote-node data.
      const payload = demo ? { items: demoSLOs(services, sloWindow) } : await api.listSLOs({ node: "local", window: sloWindow, signal: pending.signal });
      if (!pending.signal.aborted) renderSLO(array(payload));
    } catch (error) {
      if (!pending.signal.aborted) {
        const message = error?.message || "Unable to load service objectives.";
        setStale(sloList, sloStatus, hasSLOResult);
        setStatus(sloStatus, hasSLOResult ? `${message} Showing last successful result.` : message, "error");
      }
    } finally {
      if (controllers.slo === pending) controllers.slo = null;
      sloList.removeAttribute("aria-busy");
    }
  }

  function renderEvents(items, from, to) {
    timeline.replaceChildren(); markers.replaceChildren();
    timeline.removeAttribute("aria-label");
    hasTimelineResult = true;
    displayedTimelineRange = timelineRange;
    setTimelineStale(false);
    if (!items.length) {
      const empty = document.createElement("p"); empty.className = "timeline-empty"; empty.textContent = "No recorded changes for this node in the selected operational window."; timeline.append(empty);
      return;
    }
    const span = Math.max(1, to - from);
    for (const event of items.slice(0, 12)) {
      const item = document.createElement("article"); item.className = "timeline-item"; item.dataset.source = event.source || "automatic";
      const dot = document.createElement("span"); dot.className = "status-dot"; dot.setAttribute("aria-hidden", "true");
      const copy = document.createElement("div");
      const title = document.createElement("strong"); title.textContent = event.title || event.type;
      const detail = document.createElement("span"); detail.className = "mono"; detail.textContent = `${event.summary ? `${event.summary} · ` : ""}${timeAgo(event.occurredAt)}`;
      copy.append(title, detail); item.append(dot, copy); timeline.append(item);
      const marker = document.createElement("button"); marker.type = "button"; marker.className = "history-event-marker"; marker.title = `${event.title} · ${dateLabel(event.occurredAt)}`; marker.setAttribute("aria-label", `Show event: ${event.title || event.type} at ${dateLabel(event.occurredAt)}`);
      const position = ((new Date(event.occurredAt).getTime() - from) / span) * 100;
      marker.style.left = `${Math.max(1, Math.min(99, position))}%`;
      marker.addEventListener("click", () => item.scrollIntoView({ block: "nearest", behavior: "smooth" }));
      markers.append(marker);
    }
  }

  async function refreshEvents() {
    abort("events");
    if (!timeline) return;
    const pending = new AbortController(); controllers.events = pending;
    try {
      const to = new Date(); const from = new Date(to.getTime() - (TIMELINE_RANGES.get(timelineRange) || TIMELINE_RANGES.get("24h")));
      const payload = demo ? { items: [] } : await api.listOperationalEvents({ node: selectedNode, from: from.toISOString(), to: to.toISOString(), limit: 100, signal: pending.signal });
      if (!pending.signal.aborted) renderEvents(array(payload), from.getTime(), to.getTime());
    } catch (error) {
      if (!pending.signal.aborted) {
        const message = error?.message || "Operational timeline is unavailable.";
        if (hasTimelineResult) {
          setTimelineStale(true);
          timeline.setAttribute("aria-label", `${message} Showing the last successful result.`);
        } else {
          markers.replaceChildren();
          timeline.replaceChildren();
          const empty = document.createElement("p"); empty.className = "timeline-empty"; empty.textContent = message; timeline.append(empty);
        }
      }
    } finally { if (controllers.events === pending) controllers.events = null; }
  }

  function setTimelineRange(next, shouldRefresh = true) {
    timelineRange = TIMELINE_RANGES.has(next) ? next : "24h";
    setTimelineStale(hasTimelineResult && displayedTimelineRange !== timelineRange);
    if (shouldRefresh) refreshEvents();
  }

  function nodeResources(snapshot) {
    const data = snapshot?.data || snapshot || {};
    const memory = data.system?.memory || {};
    const disks = Array.isArray(data.disks) ? data.disks : [];
    const root = disks.find((disk) => disk.mountPoint === "/") || disks[0] || {};
    const percentFor = (used, total, fallback) => total > 0 ? Number(used) / Number(total) * 100 : Number(fallback) || 0;
    return {
      cpu: Number(data.system?.cpu?.usagePercent ?? data.system?.cpu?.percent) || 0,
      memory: percentFor(memory.usedBytes ?? memory.used, memory.totalBytes ?? memory.total, 0),
      disk: percentFor(root.usedBytes ?? root.used, root.totalBytes ?? root.total, root.usagePercent ?? root.percent),
    };
  }

  function nodeCard(state) {
    const node = state.node || {}; const snapshot = state.snapshot || null;
    const id = node.id || state.id || "local";
    const article = document.createElement("button"); article.type = "button"; article.className = "node-workspace-card";
    article.dataset.state = state.online ? "online" : "offline";
    const head = document.createElement("div"); head.className = "node-workspace-head";
    const name = document.createElement("strong"); name.textContent = node.displayName || node.hostname || state.name || id;
    const badge = document.createElement("span"); badge.className = `badge badge-${state.online ? "up" : "down"}`; badge.textContent = state.online ? "ONLINE" : "OFFLINE";
    head.append(name, badge); article.append(head);
    const meta = document.createElement("span"); meta.className = "node-workspace-meta mono"; meta.textContent = snapshot ? `SEEN ${timeAgo(state.lastSeenAt || snapshot.collectedAt)}` : "NO SNAPSHOT"; article.append(meta);
    const resources = nodeResources(snapshot);
    for (const [label, value] of Object.entries(resources)) {
      const row = document.createElement("div"); row.className = "node-resource";
      const line = document.createElement("div");
      const metricLabel = document.createElement("span"); metricLabel.textContent = label.toUpperCase();
      const metricValue = document.createElement("strong"); metricValue.textContent = percent(value, 1);
      line.append(metricLabel, metricValue);
      const bar = document.createElement("div"); bar.className = "progress progress-thin"; bar.style.setProperty("--progress", String(Math.max(0, Math.min(100, value))));
      const fill = document.createElement("span"); fill.className = "progress-fill"; bar.append(fill);
      row.append(line, bar); article.append(row);
    }
    article.addEventListener("click", () => onSelectNode?.(id));
    return article;
  }

  function renderNodes() {
    nodesGrid.replaceChildren();
    const local = sameNodeSnapshot(localSnapshot, "local");
    const all = [local, ...nodes];
    all.sort((left, right) => Number(Boolean(right.online)) - Number(Boolean(left.online)) || String(left.node?.displayName || left.name).localeCompare(String(right.node?.displayName || right.name)));
    for (const state of all) nodesGrid.append(nodeCard(state));
    nodesGrid.removeAttribute("aria-busy");
    setStatus(nodesStatus, `${all.length} node${all.length === 1 ? "" : "s"} available. Click a node to focus its metrics.`);
  }

  async function refreshNodes() {
    abort("nodes");
    if (!nodesGrid) return;
    const pending = new AbortController(); controllers.nodes = pending; nodesGrid.setAttribute("aria-busy", "true");
    try {
      const payload = demo ? [] : await api.listNodes(pending.signal);
      if (!pending.signal.aborted) { nodes = array(payload); renderNodes(); }
    } catch (error) {
      if (!pending.signal.aborted) { renderNodes(); setStatus(nodesStatus, error?.message || "Remote node inventory is unavailable.", "error"); }
    } finally { if (controllers.nodes === pending) controllers.nodes = null; }
  }

  function checkRow(level, title, detail) {
    const article = document.createElement("article"); article.className = "check-item"; article.dataset.level = level;
    const dot = document.createElement("span"); dot.className = "status-dot"; dot.setAttribute("aria-hidden", "true");
    const copy = document.createElement("div"); const heading = document.createElement("strong"); heading.textContent = title;
    const sub = document.createElement("span"); sub.className = "mono"; sub.textContent = detail; copy.append(heading, sub); article.append(dot, copy); return article;
  }

  function renderChecks(payload) {
    checksList.replaceChildren();
    hasChecksResult = true;
    setStale(checksList, checksStatus, false);
    const certificates = Array.isArray(payload?.certificates) ? payload.certificates : [];
    const backups = Array.isArray(payload?.backups) ? payload.backups : [];
    for (const certificate of certificates) {
      const level = certificate.level || "ok";
      const title = `TLS · ${certificate.serviceName || certificate.serviceId}`;
      const detail = certificate.error || (Number.isFinite(Number(certificate.daysLeft)) ? `${certificate.daysLeft}d until expiry` : "certificate observed");
      checksList.append(checkRow(level, title, detail));
    }
    for (const backup of backups) {
      const status = backup.status || {}; const healthy = Boolean(backup.healthy);
      const detail = healthy ? `SUCCESS · ${durationLabel(backup.ageSeconds)} ago · ${bytes(status.bytes || 0)}` : `${String(status.status || "unknown").toUpperCase()} · ${backup.reason || "needs attention"}`;
      checksList.append(checkRow(healthy ? "ok" : "warning", `BACKUP · ${status.job || "unnamed"}`, detail));
    }
    if (!checksList.childElementCount) checksList.append(checkRow("unknown", "No data-health checks configured", "Add an HTTPS display URL or BACKUP_STATUS_FILE report."));
    checksList.removeAttribute("aria-busy");
    setStatus(checksStatus, `${certificates.length} certificate and ${backups.length} backup check${certificates.length + backups.length === 1 ? "" : "s"}.`);
  }

  async function refreshChecks() {
    abort("checks");
    if (!checksList) return;
    const pending = new AbortController(); controllers.checks = pending; checksList.setAttribute("aria-busy", "true"); setStatus(checksStatus, "Loading certificate and backup checks…");
    try { if (!demo) renderChecks(await api.operationalChecks(selectedNode, pending.signal)); else renderChecks({}); }
    catch (error) {
      if (!pending.signal.aborted) {
        const message = error?.message || "Health checks are unavailable.";
        setStale(checksList, checksStatus, hasChecksResult);
        setStatus(checksStatus, hasChecksResult ? `${message} Showing last successful result.` : message, "error");
      }
    }
    finally { if (controllers.checks === pending) controllers.checks = null; checksList.removeAttribute("aria-busy"); }
  }

  function syncTopologySelects() {
    const valueA = topologyDependent.value; const valueB = topologyDependency.value;
    for (const element of [topologyDependent, topologyDependency]) {
      element.replaceChildren();
      for (const service of services) {
        const option = document.createElement("option"); option.value = String(service.id || service.ID); option.textContent = service.name || option.value; element.append(option);
      }
    }
    topologyDependent.value = services.some((service) => String(service.id || service.ID) === valueA) ? valueA : (topologyDependent.options[0]?.value || "");
    topologyDependency.value = services.some((service) => String(service.id || service.ID) === valueB) ? valueB : (topologyDependency.options[1]?.value || topologyDependency.options[0]?.value || "");
  }

  function renderTopologyEdgeList() {
    if (!topologyEdgeList) return;
    topologyEdgeList.replaceChildren();
    topologyEdgeList.hidden = topology.length === 0;
    for (const edge of topology) {
      const item = document.createElement("article");
      item.className = "topology-edge-item";
      const copy = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = `${serviceName(edge.dependentServiceId)} → ${serviceName(edge.dependencyServiceId)}`;
      copy.append(title);
      if (edge.label) {
        const label = document.createElement("span");
        label.className = "mono";
        label.textContent = edge.label;
        copy.append(label);
      }
      item.append(copy);
      if (admin) {
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "text-button";
        remove.textContent = "REMOVE";
        remove.setAttribute("aria-label", `Remove dependency: ${title.textContent}`);
        remove.addEventListener("click", () => removeTopologyEdge(edge));
        item.append(remove);
      }
      topologyEdgeList.append(item);
    }
  }

  function renderTopology() {
    canvas.replaceChildren();
    topologyCount.textContent = String(topology.length);
    renderTopologyEdgeList();
    const visible = services.slice(0, 100);
    if (!visible.length) { canvas.textContent = "Add services before drawing their dependencies."; return; }
    const width = Math.max(360, canvas.clientWidth || 640); const height = Math.max(260, Math.ceil(visible.length / 5) * 116);
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg"); svg.setAttribute("viewBox", `0 0 ${width} ${height}`); svg.setAttribute("role", "group"); svg.setAttribute("aria-label", "Manual service dependencies");
    const defs = document.createElementNS(svg.namespaceURI, "defs"); const marker = document.createElementNS(svg.namespaceURI, "marker"); marker.setAttribute("id", "topology-arrow"); marker.setAttribute("viewBox", "0 0 10 10"); marker.setAttribute("refX", "8"); marker.setAttribute("refY", "5"); marker.setAttribute("markerWidth", "6"); marker.setAttribute("markerHeight", "6"); marker.setAttribute("orient", "auto-start-reverse"); const path = document.createElementNS(svg.namespaceURI, "path"); path.setAttribute("d", "M 0 0 L 10 5 L 0 10 z"); marker.append(path); defs.append(marker); svg.append(defs);
    const positions = new Map(); const columns = Math.min(5, Math.max(1, visible.length));
    visible.forEach((service, index) => positions.set(String(service.id || service.ID), { x: 80 + (index % columns) * ((width - 160) / Math.max(1, columns - 1)), y: 64 + Math.floor(index / columns) * 110, service }));
    for (const edge of topology) {
      const from = positions.get(String(edge.dependentServiceId)); const to = positions.get(String(edge.dependencyServiceId)); if (!from || !to) continue;
      const line = document.createElementNS(svg.namespaceURI, "line"); line.setAttribute("class", "topology-edge"); line.setAttribute("x1", from.x); line.setAttribute("y1", from.y); line.setAttribute("x2", to.x); line.setAttribute("y2", to.y); line.setAttribute("marker-end", "url(#topology-arrow)"); line.setAttribute("tabindex", admin ? "0" : "-1"); line.setAttribute("aria-label", `${serviceName(edge.dependentServiceId)} depends on ${serviceName(edge.dependencyServiceId)}${admin ? "; activate to remove" : ""}`);
      if (admin) {
        line.setAttribute("role", "button");
        line.setAttribute("aria-keyshortcuts", "Enter Space Delete");
        line.addEventListener("click", () => removeTopologyEdge(edge));
        line.addEventListener("keydown", (event) => {
          if (event.key === "Enter" || event.key === " " || event.key === "Delete") {
            event.preventDefault();
            removeTopologyEdge(edge);
          }
        });
      }
      svg.append(line);
    }
    for (const position of positions.values()) {
      const group = document.createElementNS(svg.namespaceURI, "g"); group.setAttribute("class", "topology-node"); group.dataset.level = healthLevel(statusOf(position.service)); group.setAttribute("tabindex", "0"); group.setAttribute("role", "button"); group.setAttribute("aria-label", `Open service ${position.service.name}`);
      const rect = document.createElementNS(svg.namespaceURI, "rect"); rect.setAttribute("x", position.x - 62); rect.setAttribute("y", position.y - 24); rect.setAttribute("width", "124"); rect.setAttribute("height", "48"); rect.setAttribute("rx", "5");
      const text = document.createElementNS(svg.namespaceURI, "text"); text.setAttribute("x", position.x); text.setAttribute("y", position.y + 4); text.setAttribute("text-anchor", "middle"); text.textContent = String(position.service.name || "service").slice(0, 20);
      group.append(rect, text); group.addEventListener("click", () => onOpenServices?.(position.service)); group.addEventListener("keydown", (event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onOpenServices?.(position.service); } }); svg.append(group);
    }
    canvas.append(svg);
  }

  async function refreshTopology() {
    abort("topology"); if (!canvas) return;
    const pending = new AbortController(); controllers.topology = pending; setStatus(topologyStatus, "Loading manual dependencies…");
    try {
      topology = demo ? [] : array(await api.listTopology(selectedNode, pending.signal));
      if (!pending.signal.aborted) {
        renderTopology();
        hasTopologyResult = true;
        setStale(canvas, topologyStatus, false);
        setStatus(topologyStatus, topology.length ? (admin ? "Select an edge or use the relationship list to remove it." : "Manual dependencies are read-only for viewers.") : "No dependencies have been curated for this node.");
      }
    } catch (error) {
      if (!pending.signal.aborted) {
        const message = error?.message || "Topology is unavailable.";
        setStale(canvas, topologyStatus, hasTopologyResult);
        setStatus(topologyStatus, hasTopologyResult ? `${message} Showing last successful result.` : message, "error");
      }
    }
    finally { if (controllers.topology === pending) controllers.topology = null; }
  }

  async function removeTopologyEdge(edge) {
    if (!admin || !window.confirm(`Remove ${serviceName(edge.dependentServiceId)} → ${serviceName(edge.dependencyServiceId)}?`)) return;
    try { if (!demo) await api.deleteTopologyDependency(edge.id, selectedNode); await refreshTopology(); toast("Topology edge removed."); }
    catch (error) { toast(error?.message || "Unable to remove topology edge.", "error"); }
  }

  topologyForm?.addEventListener("submit", async (event) => {
    event.preventDefault(); if (!admin) return;
    const dependentServiceId = topologyDependent.value; const dependencyServiceId = topologyDependency.value;
    if (!dependentServiceId || !dependencyServiceId || dependentServiceId === dependencyServiceId) { setStatus(topologyStatus, "Choose two different services.", "error"); return; }
    const submit = topologyForm.querySelector("button[type='submit']"); submit.disabled = true;
    try { if (!demo) await api.createTopologyDependency({ nodeId: selectedNode, dependentServiceId, dependencyServiceId, label: topologyLabel.value.trim() }); topologyLabel.value = ""; await refreshTopology(); toast("Topology edge added."); }
    catch (error) { setStatus(topologyStatus, error?.message || "Unable to create topology edge.", "error"); }
    finally { submit.disabled = false; }
  });
  eventForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!admin) return;
    const data = new FormData(eventForm);
    const submit = eventForm.querySelector("button[type='submit']");
    submit.disabled = true;
    try {
      if (!demo) await api.createOperationalEvent({
        type: String(data.get("type") || "note"), title: String(data.get("title") || "").trim(),
        summary: String(data.get("summary") || "").trim(), nodeId: selectedNode,
      });
      eventForm.reset();
      await refreshEvents();
      toast("Operational change recorded.");
    } catch (error) {
      toast(error?.message || "Unable to record operational change.", "error");
    } finally { submit.disabled = false; }
  });
  for (const button of sloButtons) button.addEventListener("click", () => setSLOWindow(button.dataset.sloWindow));
  nodesRefresh?.addEventListener("click", () => { refreshNodes(); refreshChecks(); });

  return {
    setAdmin(value) { admin = Boolean(value); if (services.length) refreshSLO(); renderTopology(); },
    setServices(next) {
      const normalized = Array.isArray(next) ? next : [];
      const fingerprint = normalized.map((service) => `${service.id || service.ID}:${service.name || ""}`).join("\u0000");
      const healthFingerprint = normalized.map((service) => `${service.id || service.ID}:${statusOf(service)}`).join("\u0000");
      const changed = fingerprint !== serviceFingerprint;
      const healthChanged = healthFingerprint !== topologyHealthFingerprint;
      services = normalized;
      serviceFingerprint = fingerprint;
      topologyHealthFingerprint = healthFingerprint;
      if (changed) syncTopologySelects();
      if (changed) refreshSLO();
      if (changed || healthChanged) renderTopology();
    },
    setSnapshot(snapshot) { localSnapshot = snapshot; renderNodes(); },
    setNode(node) {
      const next = node || "local";
      const changed = next !== selectedNode;
      selectedNode = next;
      if (changed) {
        hasTimelineResult = false;
        displayedTimelineRange = "";
        markers?.replaceChildren();
        timeline?.replaceChildren();
        setTimelineStale(false);
        hasChecksResult = false;
        hasTopologyResult = false;
        checksList?.replaceChildren();
        topology = [];
        renderTopology();
      }
      refreshSLO(); refreshEvents(); refreshChecks(); refreshTopology();
    },
    refreshNodes,
    refreshEvents,
    refreshChecks,
    refreshTopology,
    setTimelineRange,
    activate(workspace) { if (workspace === "nodes") { refreshNodes(); refreshChecks(); } if (workspace === "topology") refreshTopology(); },
    destroy() { Object.keys(controllers).forEach(abort); },
  };
}
