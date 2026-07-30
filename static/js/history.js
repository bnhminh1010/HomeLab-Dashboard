const RANGE_LABELS = new Set(["1h", "6h", "24h", "7d", "30d", "90d"]);
const HISTORY_KINDS = new Set(["system", "container", "service"]);
const MAX_CHART_POINTS = 60;

function field(value, camel, exported) {
  return value?.[camel] ?? value?.[exported];
}

function pointTime(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString([], {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
  });
}

function demoHistory(range, kind) {
  const counts = { "1h": 60, "6h": 72, "24h": 96, "7d": 84, "30d": 90, "90d": 90 };
  const count = counts[range] || 60;
  const spanMs = { "1h": 3.6e6, "6h": 21.6e6, "24h": 86.4e6, "7d": 604.8e6, "30d": 2.592e9, "90d": 7.776e9 }[range];
  const points = Array.from({ length: count }, (_, index) => {
    const at = new Date(Date.now() - spanMs + index * spanMs / Math.max(1, count - 1)).toISOString();
    if (kind === "container") {
      return {
        at,
        cpuPercent: 8 + Math.sin(index / 4) * 5,
        memoryUsageBytes: (420 + Math.sin(index / 9) * 40) * 1024 ** 2,
        memoryLimitBytes: 1024 ** 3,
        restartCount: index > count * 0.8 ? 1 : 0,
      };
    }
    if (kind === "service") {
      const degraded = index % 17 === 0 ? 300 : 0;
      return { at, upSeconds: 3600 - degraded, downSeconds: 0, degradedSeconds: degraded, unknownSeconds: 0, transitionCount: degraded ? 2 : 0 };
    }
    return {
      at,
      cpuPercent: 27 + Math.sin(index / 5) * 12 + Math.sin(index / 2) * 3,
      memoryUsedBytes: (6.1 + Math.sin(index / 13) * 0.45) * 1024 ** 3,
      memoryTotalBytes: 16 * 1024 ** 3,
    };
  });
  return {
    resolution: kind === "service" ? "1h" : range === "90d" ? (kind === "container" ? "1h" : "15m") : range === "30d" || range === "7d" ? (kind === "container" ? "5m" : "1m") : "raw",
    sourcePointCount: points.length,
    quota: { usedBytes: 38 * 1024 * 1024, limitBytes: 2 * 1024 * 1024 * 1024, ratio: 0.018 },
    points,
  };
}

function formatBytes(value) {
  const bytes = Number(value);
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  const units = ["B", "KiB", "MiB", "GiB"];
  let amount = bytes;
  let index = 0;
  while (amount >= 1024 && index < units.length - 1) { amount /= 1024; index += 1; }
  return `${amount.toFixed(index > 1 ? 1 : 0)} ${units[index]}`;
}

function colorToken(name, fallback = "transparent") {
  return globalThis.getComputedStyle?.(document.documentElement).getPropertyValue(name).trim() || fallback;
}

function createChart(canvas) {
  if (!canvas || typeof globalThis.Chart !== "function") return null;
  const colors = {
    accent: colorToken("--accent"),
    accentSoft: colorToken("--accent-soft"),
    green: colorToken("--green"),
    greenSoft: colorToken("--green-soft"),
    dim: colorToken("--text-dim"),
    secondary: colorToken("--text-secondary"),
    primary: colorToken("--text-primary"),
    overlay: colorToken("--bg-overlay"),
    grid: colorToken("--border-subtle"),
    tooltipBorder: colorToken("--accent-muted"),
  };
  return new globalThis.Chart(canvas.getContext("2d"), {
    type: "line",
    data: { labels: [], datasets: [
      { label: "CPU", data: [], borderColor: colors.accent, backgroundColor: colors.accentSoft, fill: true },
      { label: "RAM", data: [], borderColor: colors.green, backgroundColor: colors.greenSoft, fill: false },
    ] },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: false,
      normalized: true,
      interaction: { intersect: false, mode: "index" },
      scales: {
        x: { grid: { display: false }, ticks: { color: colors.dim, maxTicksLimit: 8, maxRotation: 0, font: { family: "monospace", size: 9 } } },
        y: { min: 0, max: 100, grid: { color: colors.grid }, ticks: { color: colors.dim, callback: (value) => `${value}%`, font: { family: "monospace", size: 9 } } },
      },
      plugins: {
        legend: { display: true, align: "end", labels: { color: colors.secondary, usePointStyle: true, boxWidth: 7, font: { family: "monospace", size: 9 } } },
        tooltip: { backgroundColor: colors.overlay, borderColor: colors.tooltipBorder, borderWidth: 1, titleColor: colors.primary, bodyColor: colors.secondary, callbacks: { label: (context) => `${context.dataset.label}: ${Number(context.raw).toFixed(1)}%` } },
      },
      elements: { point: { radius: 0, hoverRadius: 3 }, line: { borderWidth: 1.5, tension: 0.18 } },
    },
  });
}

function projectedPoint(point, kind) {
  const at = field(point, "at", "At");
  if (kind === "container") {
    const used = Number(field(point, "memoryUsageBytes", "MemoryUsageBytes")) || 0;
    const limit = Number(field(point, "memoryLimitBytes", "MemoryLimitBytes")) || 0;
    return { at, first: Number(field(point, "cpuPercent", "CPUPercent")) || 0, second: limit > 0 ? Math.min(100, used / limit * 100) : 0 };
  }
  if (kind === "service") {
    const up = Number(field(point, "upSeconds", "UpSeconds")) || 0;
    const down = Number(field(point, "downSeconds", "DownSeconds")) || 0;
    const degraded = Number(field(point, "degradedSeconds", "DegradedSeconds")) || 0;
    const unknown = Number(field(point, "unknownSeconds", "UnknownSeconds")) || 0;
    const total = up + down + degraded + unknown;
    return { at, first: total > 0 ? up / total * 100 : 0, second: total > 0 ? (down + degraded) / total * 100 : 0 };
  }
  const total = Number(field(point, "memoryTotalBytes", "MemoryTotalBytes")) || 0;
  const used = Number(field(point, "memoryUsedBytes", "MemoryUsedBytes")) || 0;
  return { at, first: Number(field(point, "cpuPercent", "CPUPercent")) || 0, second: total > 0 ? Math.min(100, used / total * 100) : 0 };
}

function chartPoints(points, kind) {
  const projected = points.map((point) => projectedPoint(point, kind));
  if (projected.length <= MAX_CHART_POINTS) return projected;
  const reduced = [];
  for (let bucket = 0; bucket < MAX_CHART_POINTS; bucket += 1) {
    const start = Math.floor(bucket * projected.length / MAX_CHART_POINTS);
    const end = Math.max(start + 1, Math.floor((bucket + 1) * projected.length / MAX_CHART_POINTS));
    const slice = projected.slice(start, end);
    reduced.push({
      at: slice[Math.floor(slice.length / 2)].at,
      first: slice.reduce((sum, point) => sum + point.first, 0) / slice.length,
      second: slice.reduce((sum, point) => sum + point.second, 0) / slice.length,
    });
  }
  return reduced;
}

function normalizeResources(items, kind) {
  return (Array.isArray(items) ? items : []).map((item) => {
    const catalogID = kind === "container"
      ? field(item, "instanceId", "InstanceID")
      : field(item, "serviceId", "ServiceID");
    const id = String(item?.id || item?.ID || catalogID || "");
    return {
      id,
      label: String(item?.name || item?.displayName || id || `Unnamed ${kind}`),
    };
  }).filter((item) => item.id).sort((a, b) => a.label.localeCompare(b.label));
}

function mergeResources(catalog, live) {
  const merged = new Map(catalog.map((item) => [item.id, item]));
  for (const item of live) merged.set(item.id, item);
  return [...merged.values()].sort((a, b) => a.label.localeCompare(b.label) || a.id.localeCompare(b.id));
}

export function createHistoryController({ api, demo = false, toast, onRangeChange }) {
  const panel = document.getElementById("history-panel");
  const wrap = document.getElementById("history-chart-wrap");
  const empty = document.getElementById("history-empty");
  const title = document.getElementById("history-title");
  const nodeLabel = document.getElementById("history-node");
  const resourceSummary = document.getElementById("history-resource-summary");
  const resourcePicker = document.getElementById("history-resource-picker");
  const resourceSelect = document.getElementById("history-resource");
  const resolutionLabel = document.getElementById("history-resolution");
  const quotaLabel = document.getElementById("history-quota");
  const refreshButton = document.getElementById("history-refresh");
  const rangeButtons = [...document.querySelectorAll("[data-history-range]")];
  const kindButtons = [...document.querySelectorAll("[data-history-kind]")];
  const chart = createChart(document.getElementById("history-chart"));
  let node = "local";
  let storedRange = "";
  let storedKind = "";
  try {
    storedRange = localStorage.getItem("homelab.historyRange") || "";
    storedKind = localStorage.getItem("homelab.historyKind") || "";
  } catch { /* Storage is optional. */ }
  let range = RANGE_LABELS.has(storedRange) ? storedRange : "24h";
  let kind = HISTORY_KINDS.has(storedKind) ? storedKind : "system";
  let request = null;
  let catalogRequest = null;
  let liveResources = { container: [], service: [] };
  let catalogResources = { container: [], service: [] };
  let resources = { container: [], service: [] };
  let resourceSignatures = { container: "", service: "" };
  const selectedResources = { container: "", service: "" };

  function selectRange(next) {
    range = RANGE_LABELS.has(next) ? next : "24h";
    try { localStorage.setItem("homelab.historyRange", range); } catch { /* Storage is optional. */ }
    for (const button of rangeButtons) {
      const active = button.dataset.historyRange === range;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    }
  }

  function setRange(nextRange, shouldRefresh = true) {
    selectRange(nextRange);
    onRangeChange?.(range, shouldRefresh);
    if (shouldRefresh) refresh();
  }

  function syncResourcePicker() {
    const system = kind === "system";
    resourcePicker.hidden = system;
    resourceSelect.disabled = system;
    if (system) {
      resourceSummary.textContent = "RESOURCE · SYSTEM";
      return;
    }
    const available = resources[kind];
    const prior = selectedResources[kind];
    resourceSelect.replaceChildren();
    for (const resource of available) {
      const option = document.createElement("option");
      option.value = resource.id;
      option.textContent = resource.label;
      resourceSelect.append(option);
    }
    selectedResources[kind] = available.some((resource) => resource.id === prior) ? prior : (available[0]?.id || "");
    resourceSelect.value = selectedResources[kind];
    resourceSelect.disabled = available.length === 0;
    const selected = available.find((resource) => resource.id === selectedResources[kind]);
    resourceSummary.textContent = `RESOURCE · ${selected?.label || `NO ${kind.toUpperCase()}S`}`;
  }

  function applyResources(refreshOnChange = true) {
    const next = {
      container: mergeResources(catalogResources.container, liveResources.container),
      service: mergeResources(catalogResources.service, liveResources.service),
    };
    const nextSignatures = {
      container: next.container.map((item) => `${item.id}:${item.label}`).join("\u0000"),
      service: next.service.map((item) => `${item.id}:${item.label}`).join("\u0000"),
    };
    const changed = nextSignatures[kind] !== resourceSignatures[kind];
    resources = next;
    resourceSignatures = nextSignatures;
    syncResourcePicker();
    if (refreshOnChange && kind !== "system" && changed) refresh();
  }

  async function loadCatalog() {
    catalogRequest?.abort();
    if (demo || typeof api.historyResources !== "function") return;
    const pending = new AbortController();
    catalogRequest = pending;
    try {
      const payload = await api.historyResources(node, pending.signal);
      if (pending.signal.aborted || catalogRequest !== pending) return;
      catalogResources = {
        container: mergeResources(normalizeResources(payload?.containers, "container"), catalogResources.container),
        service: mergeResources(normalizeResources(payload?.services, "service"), catalogResources.service),
      };
      applyResources();
    } catch (error) {
      if (error?.name !== "AbortError") toast?.(error?.message || "Unable to load archived history resources.", "error");
    } finally {
      if (catalogRequest === pending) catalogRequest = null;
    }
  }

  function selectKind(next) {
    kind = HISTORY_KINDS.has(next) ? next : "system";
    try { localStorage.setItem("homelab.historyKind", kind); } catch { /* Storage is optional. */ }
    for (const button of kindButtons) {
      const active = button.dataset.historyKind === kind;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    }
    title.textContent = `${kind.toUpperCase()} HISTORY`;
    if (chart) {
      const labels = kind === "service" ? ["UPTIME", "UNAVAILABLE"] : kind === "container" ? ["CPU", "MEM"] : ["CPU", "RAM"];
      chart.data.datasets[0].label = labels[0];
      chart.data.datasets[1].label = labels[1];
    }
    syncResourcePicker();
  }

  function setOverlay(message, level = "info") {
    empty.textContent = message;
    empty.dataset.level = level;
    empty.hidden = !message;
  }

  function render(payload) {
    const points = Array.isArray(payload?.points) ? payload.points : [];
    const visiblePoints = chartPoints(points, kind);
    if (chart) {
      chart.data.labels = visiblePoints.map((point) => pointTime(point.at));
      chart.data.datasets[0].data = visiblePoints.map((point) => point.first);
      chart.data.datasets[1].data = visiblePoints.map((point) => point.second);
      chart.update("none");
    }
    setOverlay(!chart && points.length ? "History data loaded, but the local chart renderer is unavailable." : points.length ? "" : "No historical samples in this range.", !chart && points.length ? "error" : "info");
    const sourceCount = Number(payload?.sourcePointCount) || points.length;
    const sampling = sourceCount > visiblePoints.length ? ` · ${visiblePoints.length}/${sourceCount} SHOWN` : "";
    resolutionLabel.textContent = `RESOLUTION · ${String(payload?.resolution || "auto").toUpperCase()}${sampling}`;
    const quota = payload?.quota;
    quotaLabel.dataset.level = "";
    if (!quota) {
      quotaLabel.textContent = "STORAGE · UNAVAILABLE";
    } else {
      const used = Number(quota.usedBytes ?? quota.UsedBytes) || 0;
      const limit = Number(quota.limitBytes ?? quota.LimitBytes) || 0;
      const ratio = Number(quota.ratio ?? quota.Ratio) || (limit ? used / limit : 0);
      const full = Boolean(quota.full ?? quota.Full) || ratio >= 1;
      const warning = Boolean(quota.warning ?? quota.Warning) || ratio >= 0.8;
      quotaLabel.textContent = `STORAGE · ${formatBytes(used)} / ${formatBytes(limit)}${full ? " · RAW PAUSED" : ""}`;
      quotaLabel.dataset.level = full ? "full" : warning ? "warning" : "";
    }
  }

  async function refresh() {
    request?.abort();
    request = new AbortController();
    const resourceID = kind === "system" ? "" : selectedResources[kind];
    if (kind !== "system" && !resourceID) {
      render({ points: [], resolution: "auto" });
      setOverlay(`No ${kind}s are available for this node.`);
      return;
    }
    wrap.setAttribute("aria-busy", "true");
    setOverlay("Loading metric history…");
    refreshButton.disabled = true;
    try {
      let payload;
      if (demo) payload = demoHistory(range, kind);
      else if (kind === "container") payload = await api.containerHistory(node, resourceID, range, request.signal);
      else if (kind === "service") payload = await api.serviceHistory(node, resourceID, range, request.signal);
      else payload = await api.systemHistory(node, range, request.signal);
      render(payload);
    } catch (error) {
      if (error?.name === "AbortError") return;
      setOverlay(error?.status === 404 ? "No historical samples exist for this resource yet." : (error?.message || "Unable to load metric history."), "error");
    } finally {
      wrap.setAttribute("aria-busy", "false");
      refreshButton.disabled = false;
    }
  }

  for (const button of rangeButtons) button.addEventListener("click", () => setRange(button.dataset.historyRange));
  for (const button of kindButtons) button.addEventListener("click", () => { selectKind(button.dataset.historyKind); refresh(); });
  resourceSelect.addEventListener("change", () => {
    selectedResources[kind] = resourceSelect.value;
    syncResourcePicker();
    refresh();
  });
  refreshButton.addEventListener("click", () => refresh().catch((error) => toast(error?.message || "History refresh failed.", "error")));
  selectRange(range);
  selectKind(kind);

  return {
    setNode(nextNode) {
      request?.abort();
      catalogRequest?.abort();
      node = nextNode || "local";
      liveResources = { container: [], service: [] };
      catalogResources = { container: [], service: [] };
      selectedResources.container = "";
      selectedResources.service = "";
      applyResources(false);
      nodeLabel.textContent = `NODE · ${node.toUpperCase()}`;
      loadCatalog();
      refresh();
    },
    setResources(containers, services) {
      const nextLive = {
        container: normalizeResources(containers, "container"),
        service: normalizeResources(services, "service"),
      };
      catalogResources = {
        container: mergeResources(catalogResources.container, nextLive.container),
        service: mergeResources(catalogResources.service, nextLive.service),
      };
      liveResources = nextLive;
      applyResources();
    },
    activate() {
      window.requestAnimationFrame(() => chart?.resize());
    },
    refresh,
    setRange,
    range: () => range,
    destroy() { request?.abort(); catalogRequest?.abort(); chart?.destroy(); panel?.removeAttribute("aria-busy"); },
  };
}
