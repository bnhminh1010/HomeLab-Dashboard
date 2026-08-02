const JSON_HEADERS = { Accept: "application/json" };

export class ApiError extends Error {
  constructor(message, status = 0, code = "request_failed", fields = null) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export class DashboardApi {
  constructor() {
    this.csrfToken = "";
    this.demo = false;
  }

  async request(path, options = {}) {
    const headers = new Headers(JSON_HEADERS);
    for (const [name, value] of Object.entries(options.headers || {})) headers.set(name, value);
    if (options.body !== undefined) headers.set("Content-Type", "application/json");
    if (options.mutation && this.csrfToken) headers.set("X-CSRF-Token", this.csrfToken);

    const response = await fetch(path, {
      method: options.method || "GET",
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: "same-origin",
      cache: options.cache || "no-store",
      signal: options.signal,
    });

    const contentType = response.headers.get("content-type") || "";
    let payload = null;
    if (response.status !== 204 && contentType.includes("application/json")) {
      try { payload = await response.json(); } catch { throw new ApiError("The server returned invalid JSON.", response.status, "invalid_json"); }
    }

    if (!response.ok) {
      const detail = payload?.error || payload || {};
      throw new ApiError(detail.message || `Request failed (${response.status}).`, response.status, detail.code, detail.fields);
    }
    return payload;
  }

  async session(signal) {
    const payload = await this.request("/api/v1/session", { method: "POST", signal });
    this.csrfToken = payload?.csrfToken || payload?.csrf_token || "";
    return payload;
  }

  snapshot(signal) {
    return this.request("/api/v1/snapshot", { signal });
  }

  preferences(signal) {
    return this.request("/api/v1/preferences", { signal });
  }

  updatePreferences(preferences) {
    return this.request("/api/v1/preferences", { method: "PATCH", mutation: true, body: preferences });
  }

  createService(service) {
    return this.request("/api/services", { method: "POST", mutation: true, body: service });
  }

  updateService(id, service) {
    return this.request(`/api/services/${encodeURIComponent(id)}`, { method: "PATCH", mutation: true, body: service });
  }

  deleteService(id) {
    return this.request(`/api/services/${encodeURIComponent(id)}`, { method: "DELETE", mutation: true });
  }

  createTerminalSession(options) {
    return this.request("/api/v1/terminal/sessions", { method: "POST", mutation: true, body: options });
  }

  createHostTerminalSession(options) {
    return this.request("/api/v1/terminal/host-sessions", { method: "POST", mutation: true, body: options });
  }

  cancelTerminalSession(id) {
    return this.request(`/api/v1/terminal/sessions/${encodeURIComponent(id)}`, { method: "DELETE", mutation: true });
  }

  listNodes(signal) {
    return this.request("/api/v1/nodes", { signal });
  }

  createNodeEnrollment() {
    return this.request("/api/v1/nodes/enrollment-tokens", { method: "POST", mutation: true, body: {} });
  }

  revokeNode(id) {
    return this.request(`/api/v1/nodes/${encodeURIComponent(id)}`, { method: "DELETE", mutation: true });
  }

  systemHistory(node, range, signal) {
    const query = new URLSearchParams({ node: node || "local", range: range || "24h", resolution: "auto", maxPoints: "60" });
    return this.request(`/api/v1/history/system?${query}`, { signal });
  }

  containerHistory(node, instanceId, range, signal) {
    const query = new URLSearchParams({ node: node || "local", range: range || "24h", resolution: "auto", maxPoints: "60" });
    return this.request(`/api/v1/history/containers/${encodeURIComponent(instanceId)}?${query}`, { signal });
  }

  serviceHistory(node, serviceId, range, signal) {
    const query = new URLSearchParams({ node: node || "local", range: range || "24h", maxPoints: "60" });
    return this.request(`/api/v1/history/services/${encodeURIComponent(serviceId)}?${query}`, { signal });
  }

  historyResources(node, signal) {
    const query = new URLSearchParams({ node: node || "local" });
    return this.request(`/api/v1/history/resources?${query}`, { signal });
  }

  logsStatus(signal) {
    return this.request("/api/v1/logs/status", { signal });
  }

  queryLogs({ node = "local", from = "", to = "", service = "", container = "", level = "", q = "", limit = 200, signal } = {}) {
    const query = new URLSearchParams({ node, limit: String(limit) });
    if (from) query.set("from", from);
    if (to) query.set("to", to);
    if (service) query.set("service", service);
    if (container) query.set("container", container);
    if (level) query.set("level", level);
    if (q) query.set("q", q);
    return this.request(`/api/v1/logs/query?${query}`, { signal });
  }

  listSLOs({ node = "local", window = 30, signal } = {}) {
    const query = new URLSearchParams({ node, window: String(window) });
    return this.request(`/api/v1/slos?${query}`, { signal });
  }

  updateServiceSLO(id, policy) {
    return this.request(`/api/v1/services/${encodeURIComponent(id)}/slo`, { method: "PATCH", mutation: true, body: policy });
  }

  listOperationalEvents({ node = "", service = "", from = "", to = "", limit = 100, signal } = {}) {
    const query = new URLSearchParams({ limit: String(limit) });
    if (node) query.set("node", node);
    if (service) query.set("service", service);
    if (from) query.set("from", from);
    if (to) query.set("to", to);
    return this.request(`/api/v1/events?${query}`, { signal });
  }

  createOperationalEvent(event) {
    return this.request("/api/v1/events", { method: "POST", mutation: true, body: event });
  }

  listTopology(node = "local", signal) {
    const query = new URLSearchParams({ node });
    return this.request(`/api/v1/topology/dependencies?${query}`, { signal });
  }

  createTopologyDependency(input) {
    return this.request("/api/v1/topology/dependencies", { method: "POST", mutation: true, body: input });
  }

  deleteTopologyDependency(id, node = "local") {
    const query = new URLSearchParams({ node });
    return this.request(`/api/v1/topology/dependencies/${encodeURIComponent(id)}?${query}`, { method: "DELETE", mutation: true });
  }

  operationalChecks(node = "", signal) {
    const query = new URLSearchParams();
    if (node) query.set("node", node);
    const suffix = query.size ? `?${query}` : "";
    return this.request(`/api/v1/operations/checks${suffix}`, { signal });
  }

  listAlertRules(signal) {
    return this.request("/api/v1/alert-rules", { signal });
  }

  createAlertRule(rule) {
    return this.request("/api/v1/alert-rules", { method: "POST", mutation: true, body: rule });
  }

  updateAlertRule(id, rule) {
    return this.request(`/api/v1/alert-rules/${encodeURIComponent(id)}`, { method: "PATCH", mutation: true, body: rule });
  }

  deleteAlertRule(id) {
    return this.request(`/api/v1/alert-rules/${encodeURIComponent(id)}`, { method: "DELETE", mutation: true });
  }

  listAlerts({ node = "", active = true, limit = 100, signal } = {}) {
    const query = new URLSearchParams({ active: String(active), limit: String(limit) });
    if (node) query.set("node", node);
    return this.request(`/api/v1/alerts?${query}`, { signal });
  }

  listAlertEvents({ node = "", limit = 100, signal } = {}) {
    const query = new URLSearchParams({ limit: String(limit) });
    if (node) query.set("node", node);
    return this.request(`/api/v1/alerts/events?${query}`, { signal });
  }

  acknowledgeAlert(alert) {
    return this.request("/api/v1/alerts/acknowledge", { method: "POST", mutation: true, body: alert });
  }

  silenceAlert(alert, duration) {
    return this.request("/api/v1/alerts/silence", { method: "POST", mutation: true, body: { ...alert, duration } });
  }

  ntfyStatus(signal) {
    return this.request("/api/v1/notifications/ntfy", { signal });
  }

  testNtfy() {
    return this.request("/api/v1/notifications/ntfy/test", { method: "POST", mutation: true, body: {} });
  }

  exportDashboardConfig(signal) {
    return this.request("/api/v1/config/export", { signal });
  }

  previewDashboardImport(document, mode) {
    const query = new URLSearchParams({ mode: mode || "merge" });
    return this.request(`/api/v1/config/import/preview?${query}`, { method: "POST", mutation: true, body: document });
  }

  applyDashboardImport(document, mode, revision) {
    const query = new URLSearchParams({ mode: mode || "merge" });
    return this.request(`/api/v1/config/import/apply?${query}`, {
      method: "POST", mutation: true, body: document,
      headers: { "If-Match": `"${revision || ""}"` },
    });
  }
}

export function unwrapSnapshot(payload) {
  if (payload?.data && (payload.type === "metrics.snapshot" || payload.version)) return payload.data;
  return payload?.data || payload || {};
}
