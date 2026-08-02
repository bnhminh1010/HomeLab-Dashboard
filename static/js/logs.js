const ranges = {
  "15m": 15 * 60 * 1000,
  "1h": 60 * 60 * 1000,
  "6h": 6 * 60 * 60 * 1000,
  "24h": 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000,
};

function field(value, camel, snake = camel) {
  return value?.[camel] ?? value?.[snake] ?? "";
}

function option(select, value, label) {
  const item = document.createElement("option");
  item.value = value;
  item.textContent = label;
  select.append(item);
}

function formatTimestamp(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString([], { hour12: false });
}

// Historical logs deliberately refresh only on explicit operator action. Live
// per-container output remains available through the terminal workbench.
export function createLogsController({ api, demo, toast }) {
  const panel = document.getElementById("logs-panel");
  const status = document.getElementById("logs-status");
  const context = document.getElementById("logs-context");
  const list = document.getElementById("logs-list");
  const empty = document.getElementById("logs-empty");
  const serviceSelect = document.getElementById("logs-service");
  const containerSelect = document.getElementById("logs-container");
  const levelSelect = document.getElementById("logs-level");
  const search = document.getElementById("logs-search");
  const refreshButton = document.getElementById("logs-refresh");
  const rangeButtons = [...document.querySelectorAll("[data-logs-range]")];
  let range = "1h";
  let node = "local";
  let enabled = false;
  let pending = null;
  let lastRefreshAt = 0;

  function setStatus(message, state = "muted") {
    status.textContent = message;
    status.dataset.state = state;
  }

  function setRange(next, refresh = true) {
    range = ranges[next] ? next : "1h";
    for (const button of rangeButtons) {
      const active = button.dataset.logsRange === range;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    }
    context.textContent = `${range.toUpperCase()} · LOCAL`;
    if (refresh) void load();
  }

  function render(entries) {
    list.replaceChildren();
    empty.hidden = entries.length > 0;
    for (const entry of entries) {
      const labels = entry?.labels || {};
      const row = document.createElement("article");
      row.className = "historical-log-entry";
      const metadata = document.createElement("div");
      metadata.className = "historical-log-meta mono";
      const timestamp = document.createElement("time");
      timestamp.dateTime = entry.timestamp || "";
      timestamp.textContent = formatTimestamp(entry.timestamp);
      const resource = document.createElement("span");
      resource.textContent = labels.service_name || labels.container_name || "podman";
      const container = document.createElement("span");
      container.textContent = labels.container_name || "local";
      metadata.append(timestamp, resource, container);
      const line = document.createElement("pre");
      line.className = "historical-log-line";
      line.textContent = String(entry.line || "");
      row.append(metadata, line);
      list.append(row);
    }
  }

  async function load() {
    if (!enabled || node !== "local") return;
    pending?.abort();
    const request = new AbortController();
    pending = request;
    refreshButton.disabled = true;
    panel?.setAttribute("aria-busy", "true");
    setStatus("QUERYING", "pending");
    const to = new Date();
    const from = new Date(to.getTime() - ranges[range]);
    try {
      const payload = await api.queryLogs({
        node,
        from: from.toISOString(),
        to: to.toISOString(),
        service: serviceSelect.value,
        container: containerSelect.value,
        level: levelSelect.value,
        q: search.value.trim(),
        limit: 200,
        signal: request.signal,
      });
      const entries = Array.isArray(payload?.entries) ? payload.entries : [];
      render(entries);
      setStatus(entries.length ? `${entries.length} ENTRIES` : "NO MATCHES", entries.length ? "ready" : "muted");
      lastRefreshAt = Date.now();
    } catch (error) {
      if (error?.name === "AbortError") return;
      render([]);
      setStatus(error?.code === "logs_disabled" ? "NOT CONFIGURED" : "UNAVAILABLE", "error");
      toast?.(error?.message || "Unable to query historical logs.", "error");
    } finally {
      if (pending === request) pending = null;
      refreshButton.disabled = false;
      panel?.setAttribute("aria-busy", "false");
    }
  }

  async function checkStatus() {
    if (node !== "local") {
      enabled = false;
      render([]);
      empty.textContent = "Historical logs are available for the local node only in this release.";
      empty.hidden = false;
      setStatus("LOCAL NODE ONLY", "muted");
      return;
    }
    try {
      const payload = await api.logsStatus();
      enabled = Boolean(payload?.enabled);
      if (!enabled) {
        render([]);
        empty.textContent = "Central logs are optional. Enable the Loki + Vector overlay to search retained local logs.";
        empty.hidden = false;
        setStatus("NOT CONFIGURED", "muted");
        return;
      }
      const retention = Number(payload?.retentionHours || 0);
      setStatus(`READY · ${retention || 168}H`, "ready");
    } catch (error) {
      enabled = false;
      setStatus("UNAVAILABLE", "error");
      if (!demo) toast?.(error?.message || "Unable to read logging status.", "error");
    }
  }

  function setResources(containers = [], services = []) {
    const service = serviceSelect.value;
    const container = containerSelect.value;
    serviceSelect.replaceChildren(); option(serviceSelect, "", "All services");
    for (const item of services) {
      const name = field(item, "name");
      if (name) option(serviceSelect, name, name);
    }
    containerSelect.replaceChildren(); option(containerSelect, "", "All containers");
    for (const item of containers) {
      const name = field(item, "name");
      if (name) option(containerSelect, name, name);
    }
    serviceSelect.value = [...serviceSelect.options].some((item) => item.value === service) ? service : "";
    containerSelect.value = [...containerSelect.options].some((item) => item.value === container) ? container : "";
  }

  for (const button of rangeButtons) button.addEventListener("click", () => setRange(button.dataset.logsRange));
  refreshButton.addEventListener("click", () => void load());
  for (const control of [serviceSelect, containerSelect, levelSelect]) control.addEventListener("change", () => void load());
  search.addEventListener("keydown", (event) => { if (event.key === "Enter") void load(); });

  return {
    setResources,
    setNode(next) { node = next || "local"; },
    async activate() {
      await checkStatus();
      if (enabled && Date.now() - lastRefreshAt > 30_000) await load();
    },
    destroy() { pending?.abort(); },
  };
}
