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
}

export function unwrapSnapshot(payload) {
  if (payload?.data && (payload.type === "metrics.snapshot" || payload.version)) return payload.data;
  return payload?.data || payload || {};
}
