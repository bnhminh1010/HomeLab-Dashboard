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

function severity(entry) {
  const labeled = String(entry?.labels?.level || "").toLowerCase();
  if (["debug", "info", "warn", "warning", "error", "fatal", "critical"].includes(labeled)) return labeled;
  const line = String(entry?.line || "");
  try {
    const parsed = JSON.parse(line);
    const value = String(parsed?.level || parsed?.severity || "").toLowerCase();
    if (["debug", "info", "warn", "warning", "error", "fatal", "critical"].includes(value)) return value;
  } catch { /* Plain-text logs are common and remain unclassified. */ }
  if (/\b(fatal|critical|error)\b/i.test(line)) return "error";
  if (/\bwarn(?:ing)?\b/i.test(line)) return "warn";
  return "";
}

function createMatcher(query, regex) {
  if (!query) return null;
  try {
    return new RegExp(regex ? query : query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "gi");
  } catch {
    return null;
  }
}

function appendHighlighted(target, value, matcher) {
  if (!matcher) {
    target.textContent = value;
    return;
  }
  let cursor = 0;
  let match;
  matcher.lastIndex = 0;
  while ((match = matcher.exec(value)) !== null) {
    const start = match.index;
    const end = start + match[0].length;
    if (start > cursor) target.append(document.createTextNode(value.slice(cursor, start)));
    if (end === start) {
      target.append(document.createTextNode(match[0]));
      if (matcher.lastIndex === start) matcher.lastIndex += 1;
    } else {
      const mark = document.createElement("mark");
      mark.textContent = match[0];
      target.append(mark);
    }
    cursor = end;
  }
  if (cursor < value.length) target.append(document.createTextNode(value.slice(cursor)));
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
  const regexToggle = document.getElementById("logs-regex-toggle");
  const matchStatus = document.getElementById("logs-match-status");
  const refreshButton = document.getElementById("logs-refresh");
  const rangeButtons = [...document.querySelectorAll("[data-logs-range]")];
  let range = "1h";
  let node = "local";
  let enabled = false;
  let pending = null;
  let lastRefreshAt = 0;
  let isRegex = false;
  let activeQuery = "";
  let activeQueryIsRegex = false;
  let matchIndex = -1;
  let matchRows = [];

  function setStatus(message, state = "muted") {
    status.textContent = message;
    status.dataset.state = state;
  }

  function updateMatchStatus() {
    if (!activeQuery || matchRows.length === 0) {
      matchStatus.textContent = activeQuery ? "0 MATCHES" : "NO SEARCH";
      return;
    }
    matchStatus.textContent = `${matchIndex + 1} OF ${matchRows.length}`;
  }

  function resetMatches() {
    for (const row of matchRows) row.removeAttribute("data-match-active");
    matchRows = [];
    matchIndex = -1;
    updateMatchStatus();
  }

  function selectMatch(nextIndex) {
    if (matchRows.length === 0) return;
    matchRows[matchIndex]?.removeAttribute("data-match-active");
    matchIndex = (nextIndex + matchRows.length) % matchRows.length;
    const row = matchRows[matchIndex];
    row.dataset.matchActive = "true";
    row.scrollIntoView({ block: "nearest" });
    updateMatchStatus();
  }

  function navigateMatches(previous) {
    if (matchRows.length === 0) return;
    selectMatch(matchIndex < 0 ? (previous ? matchRows.length - 1 : 0) : matchIndex + (previous ? -1 : 1));
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
    resetMatches();
    const matcher = createMatcher(activeQuery, activeQueryIsRegex);
    empty.hidden = entries.length > 0;
    for (const entry of entries) {
      const labels = entry?.labels || {};
      const row = document.createElement("article");
      row.className = "historical-log-entry";
      const level = severity(entry);
      if (level) row.dataset.severity = level;
      const metadata = document.createElement("div");
      metadata.className = "historical-log-meta mono";
      const timestamp = document.createElement("time");
      timestamp.dateTime = entry.timestamp || "";
      timestamp.textContent = formatTimestamp(entry.timestamp);
      const resource = document.createElement("span");
      resource.textContent = labels.service_name || labels.container_name || "podman";
      const container = document.createElement("span");
      container.textContent = labels.container_name || "local";
      if (level) {
        const levelLabel = document.createElement("span");
        levelLabel.className = "historical-log-level";
        levelLabel.textContent = level.toUpperCase();
        metadata.append(timestamp, resource, container, levelLabel);
      } else {
        metadata.append(timestamp, resource, container);
      }
      const line = document.createElement("pre");
      line.className = "historical-log-line";
      appendHighlighted(line, String(entry.line || ""), matcher);
      row.append(metadata, line);
      list.append(row);
      if (activeQuery) matchRows.push(row);
    }
    if (matchRows.length > 0) {
      matchIndex = 0;
      matchRows[0].dataset.matchActive = "true";
    }
    updateMatchStatus();
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
        regex: isRegex,
        limit: 200,
        signal: request.signal,
      });
      const entries = Array.isArray(payload?.entries) ? payload.entries : [];
      activeQuery = search.value.trim();
      activeQueryIsRegex = isRegex;
      render(entries);
      setStatus(entries.length ? `${entries.length} ENTRIES` : "NO MATCHES", entries.length ? "ready" : "muted");
      lastRefreshAt = Date.now();
    } catch (error) {
      if (error?.name === "AbortError") return;
      if (error?.code === "invalid_logs_query") {
        setStatus("INVALID REGEX", "error");
        toast?.("Invalid regular expression. Check the pattern and try again.", "error");
      } else {
        render([]);
        setStatus(error?.code === "logs_disabled" ? "NOT CONFIGURED" : "UNAVAILABLE", "error");
        toast?.(error?.message || "Unable to query historical logs.", "error");
      }
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
  regexToggle?.addEventListener("click", () => {
    isRegex = !isRegex;
    regexToggle.setAttribute("aria-pressed", String(isRegex));
    regexToggle.title = isRegex ? "Use plain text search" : "Use Regular Expression (.*)";
    void load();
  });
  for (const control of [serviceSelect, containerSelect, levelSelect]) control.addEventListener("change", () => void load());
  search.addEventListener("keydown", (event) => {
    if (event.key !== "Enter") return;
    event.preventDefault();
    const query = search.value.trim();
    if (query !== activeQuery || isRegex !== activeQueryIsRegex) {
      void load();
      return;
    }
    navigateMatches(event.shiftKey);
  });

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
