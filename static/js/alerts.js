const demoRules = [
  { id: "rule_cpu", name: "Sustained host CPU", resourceType: "host", nodeSelector: "*", resourceSelector: "*", metric: "system.cpu.percent", operator: "gt", threshold: 90, forSeconds: 300, severity: "warning", cooldownSeconds: 1800, enabled: true },
  { id: "rule_disk", name: "Critical root disk", resourceType: "disk", nodeSelector: "*", resourceSelector: "*", metric: "disk.used.percent", operator: "gt", threshold: 95, forSeconds: 600, severity: "critical", cooldownSeconds: 1800, enabled: true },
];

const METRICS_BY_RESOURCE = {
	node: [["node.online", "Node online (1 or 0)"]],
	host: [
    ["system.cpu.percent", "CPU usage (%)"],
    ["system.memory.percent", "Memory usage (%)"],
    ["system.temperature.celsius", "Temperature (°C)"],
  ],
  disk: [["disk.used.percent", "Disk usage (%)"]],
  service: [["service.consecutive_failures", "Consecutive probe failures"]],
  container: [
    ["container.healthy", "Healthy (1 or 0)"],
    ["container.restarts", "Restart count"],
  ],
};

function alertKey(state) {
  return { ruleId: state.ruleId, nodeId: state.nodeId, resourceType: state.resourceType, resourceId: state.resourceId };
}

function friendlyTime(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString();
}

function createEmpty(message) {
  const element = document.createElement("div");
  element.className = "management-empty";
  element.textContent = message;
  return element;
}

function actionButton(label, action, className = "") {
  const button = document.createElement("button");
  button.type = "button";
  button.textContent = label;
  button.className = className;
  button.addEventListener("click", action);
  return button;
}

export function createAlertsController({ api, demo = false, toast }) {
  const openButton = document.getElementById("alerts-jump");
  const dialog = document.getElementById("alert-center-dialog");
  const dialogStatus = document.getElementById("alert-center-status");
  const statesList = document.getElementById("active-alerts-list");
  const stateCount = document.getElementById("active-alerts-count");
  const rulesList = document.getElementById("alert-rules-list");
  const eventsList = document.getElementById("alert-events-list");
  const newRuleButton = document.getElementById("alert-rule-add");
  const ntfyStatus = document.getElementById("ntfy-status");
  const ntfyTest = document.getElementById("ntfy-test");
  const ruleDialog = document.getElementById("alert-rule-dialog");
  const ruleForm = document.getElementById("alert-rule-form");
  const ruleTitle = document.getElementById("alert-rule-dialog-title");
  const ruleError = document.getElementById("alert-rule-error");
  const ruleSubmit = document.getElementById("alert-rule-submit");
  let admin = false;
  let node = "";
  let opener = null;
  let ruleOpener = null;
  let rules = [];
  let states = [];
  let events = [];

  function setDialogStatus(message, level = "info") {
    dialogStatus.textContent = message || "";
    dialogStatus.dataset.level = level;
  }

  function open() {
    opener = document.activeElement;
    if (!dialog.open) dialog.showModal();
    dialog.querySelector("[data-dialog-close]")?.focus();
    refresh();
  }

  function close() {
    if (dialog.open) dialog.close();
  }

  function renderStates() {
    statesList.replaceChildren();
    stateCount.textContent = String(states.length);
    if (!states.length) {
      statesList.append(createEmpty("All clear — no active alert states."));
      return;
    }
    const ruleMap = new Map(rules.map((rule) => [rule.id, rule]));
    for (const state of states) {
      const rule = ruleMap.get(state.ruleId) || {};
      const item = document.createElement("article");
      item.className = "management-item";
      item.dataset.status = state.status || "pending";
      item.dataset.severity = rule.severity || "warning";
      const head = document.createElement("div");
      head.className = "management-item-head";
      const title = document.createElement("strong");
      title.className = "management-item-title";
      title.textContent = rule.name || state.ruleId || "Alert";
      const badge = document.createElement("span");
      badge.className = `badge badge-${state.status === "firing" ? "down" : "degraded"}`;
      badge.textContent = String(state.status || "pending").toUpperCase();
      head.append(title, badge);
      const meta = document.createElement("div");
      meta.className = "management-item-meta";
      const flags = [];
      if (state.acknowledgedAt) flags.push(`ACK ${state.acknowledgedBy || ""}`.trim());
      if (state.silencedUntil) flags.push(`SILENCED UNTIL ${friendlyTime(state.silencedUntil)}`);
      meta.textContent = `${state.nodeId || "local"} · ${state.resourceType || "resource"}/${state.resourceId || "*"} · VALUE ${Number(state.lastValue).toFixed(2)}${flags.length ? ` · ${flags.join(" · ")}` : ""}`;
      item.append(head, meta);
      if (admin && state.status !== "resolved") {
        const actions = document.createElement("div");
        actions.className = "management-actions admin-only";
        if (!state.acknowledgedAt) actions.append(actionButton("ACKNOWLEDGE", () => runStateAction(state, "ack")));
        for (const duration of ["1h", "6h", "24h"]) actions.append(actionButton(`SILENCE ${duration.toUpperCase()}`, () => runStateAction(state, "silence", duration)));
        item.append(actions);
      }
      statesList.append(item);
    }
  }

  function renderRules() {
    rulesList.replaceChildren();
    if (!rules.length) {
      rulesList.append(createEmpty("No alert rules configured."));
      return;
    }
    for (const rule of rules) {
      const item = document.createElement("article");
      item.className = "management-item";
      item.dataset.severity = rule.severity || "warning";
      const head = document.createElement("div");
      head.className = "management-item-head";
      const title = document.createElement("strong");
      title.className = "management-item-title";
      title.textContent = rule.name;
      const badge = document.createElement("span");
      badge.className = `badge badge-${rule.enabled ? "up" : "muted"}`;
      badge.textContent = rule.enabled ? String(rule.severity || "warning").toUpperCase() : "DISABLED";
      head.append(title, badge);
      const meta = document.createElement("div");
      meta.className = "management-item-meta";
      meta.textContent = `${rule.nodeSelector || "*"} · ${rule.resourceType}/${rule.resourceSelector || "*"} · ${rule.metric} ${rule.operator} ${rule.threshold} · FOR ${rule.forSeconds || 0}s · COOLDOWN ${rule.cooldownSeconds || 0}s`;
      item.append(head, meta);
      if (admin) {
        const actions = document.createElement("div");
        actions.className = "management-actions admin-only";
        actions.append(actionButton("EDIT", () => openRule(rule)));
        actions.append(actionButton("DELETE", () => deleteRule(rule), "danger"));
        item.append(actions);
      }
      rulesList.append(item);
    }
  }

  function renderEvents() {
    eventsList.replaceChildren();
    if (!events.length) {
      eventsList.append(createEmpty("No alert events recorded yet."));
      return;
    }
    for (const event of events.slice(0, 50)) {
      const item = document.createElement("article");
      item.className = "management-item";
      item.dataset.severity = event.severity || "info";
      const head = document.createElement("div");
      head.className = "management-item-head";
      const title = document.createElement("strong");
      title.className = "management-item-title";
      title.textContent = event.message || `${event.ruleId || "Rule"} ${event.type || "event"}`;
      const badge = document.createElement("span");
      badge.className = `badge badge-${event.type === "resolved" ? "up" : event.severity === "critical" ? "down" : "degraded"}`;
      badge.textContent = String(event.type || "event").toUpperCase();
      head.append(title, badge);
      const meta = document.createElement("div");
      meta.className = "management-item-meta";
      meta.textContent = `${event.nodeId || "local"} · ${friendlyTime(event.occurredAt)}${event.actor ? ` · ${event.actor}` : ""}`;
      item.append(head, meta);
      eventsList.append(item);
    }
  }

  async function runStateAction(state, action, duration = "") {
    try {
      if (!demo) {
        if (action === "ack") await api.acknowledgeAlert(alertKey(state));
        else await api.silenceAlert(alertKey(state), duration);
      }
      if (action === "ack") {
        state.acknowledgedAt = new Date().toISOString();
        state.acknowledgedBy = "current user";
      } else {
        const hours = Number.parseInt(duration, 10);
        state.silencedUntil = new Date(Date.now() + hours * 3600_000).toISOString();
      }
      renderStates();
      toast(action === "ack" ? "Alert acknowledged." : `Alert silenced for ${duration}.`);
    } catch (error) {
      toast(error?.message || "Unable to update the alert.", "error");
    }
  }

  function openRule(rule = null) {
    if (!admin) return;
    ruleOpener = document.activeElement;
    ruleForm.reset();
    ruleForm.elements.id.value = rule?.id || "";
    ruleForm.elements.name.value = rule?.name || "";
    ruleForm.elements.resourceType.value = rule?.resourceType || "host";
    syncMetricOptions(rule?.metric || "system.cpu.percent");
    ruleForm.elements.nodeSelector.value = rule?.nodeSelector || "*";
    ruleForm.elements.resourceSelector.value = rule?.resourceSelector || "*";
    ruleForm.elements.operator.value = rule?.operator || "gt";
    ruleForm.elements.threshold.value = rule?.threshold ?? 90;
    ruleForm.elements.forSeconds.value = rule?.forSeconds ?? 300;
    ruleForm.elements.cooldownSeconds.value = rule?.cooldownSeconds ?? 1800;
    ruleForm.elements.severity.value = rule?.severity || "warning";
    ruleForm.elements.enabled.checked = rule?.enabled !== false;
    ruleTitle.textContent = rule ? "Edit alert rule" : "New alert rule";
    ruleSubmit.textContent = rule ? "UPDATE RULE" : "CREATE RULE";
    ruleError.hidden = true;
    if (!ruleDialog.open) ruleDialog.showModal();
    ruleForm.elements.name.focus();
  }

  function syncMetricOptions(preferred = "") {
    const resource = ruleForm.elements.resourceType.value || "host";
    const choices = METRICS_BY_RESOURCE[resource] || [];
    const options = choices.map(([value, label]) => {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = label;
      return option;
    });
    if (preferred && !choices.some(([value]) => value === preferred)) {
      const unsupported = document.createElement("option");
      unsupported.value = preferred;
      unsupported.textContent = `${preferred} (unsupported by current collectors)`;
      options.push(unsupported);
    }
    ruleForm.elements.metric.replaceChildren(...options);
    ruleForm.elements.metric.value = preferred || choices[0]?.[0] || "";
  }

  async function deleteRule(rule) {
    if (!admin || !window.confirm(`Delete alert rule “${rule.name}”?`)) return;
    try {
      if (!demo) await api.deleteAlertRule(rule.id);
      rules = rules.filter((item) => item.id !== rule.id);
      renderRules();
      toast("Alert rule deleted.");
    } catch (error) {
      toast(error?.message || "Unable to delete the alert rule.", "error");
    }
  }

  async function refresh() {
    setDialogStatus("Loading alert states, rules, and delivery status…");
    const demoStates = [];
    const demoEvents = [{ id: 1, ruleId: "rule_disk", nodeId: "local", resourceType: "system", resourceId: "root", type: "resolved", severity: "critical", message: "Root disk recovered below threshold", occurredAt: new Date(Date.now() - 3600_000).toISOString() }];
    const requests = demo || typeof api.listAlertRules !== "function"
      ? [Promise.resolve(demoRules), Promise.resolve(demoStates), Promise.resolve(demoEvents), Promise.resolve({ configured: true, url: "https://ntfy.sh", topic: "homelab-demo", tokenConfigured: true })]
      : [api.listAlertRules(), api.listAlerts({ node, active: true }), api.listAlertEvents({ node }), api.ntfyStatus()];
    const [ruleResult, stateResult, eventResult, ntfyResult] = await Promise.allSettled(requests);
    if (ruleResult.status === "fulfilled") rules = Array.isArray(ruleResult.value) ? ruleResult.value : [];
    if (stateResult.status === "fulfilled") states = Array.isArray(stateResult.value) ? stateResult.value : [];
    if (eventResult.status === "fulfilled") events = Array.isArray(eventResult.value) ? eventResult.value : [];
    renderRules();
    renderStates();
    renderEvents();
    if (ntfyResult.status === "fulfilled") {
      const value = ntfyResult.value || {};
      ntfyStatus.dataset.configured = String(Boolean(value.configured));
      ntfyStatus.textContent = value.configured ? `CONFIGURED · ${value.url || "server"}/${value.topic || "topic"} · TOKEN ${value.tokenConfigured ? "LOADED" : "NOT SET"}` : "NOT CONFIGURED · SET NTFY_URL, NTFY_TOPIC, AND NTFY_TOKEN_FILE";
      ntfyTest.disabled = !admin || !value.configured;
    } else {
      ntfyStatus.dataset.configured = "false";
      ntfyStatus.textContent = "NOTIFICATION STATUS UNAVAILABLE";
      ntfyTest.disabled = true;
    }
    const failures = [ruleResult, stateResult, eventResult].filter((result) => result.status === "rejected");
    setDialogStatus(failures.length ? `${failures.length} monitoring source${failures.length === 1 ? " is" : "s are"} unavailable; available data is shown.` : `Monitoring state refreshed for ${node || "all nodes"}.`, failures.length ? "error" : "info");
  }

  openButton.addEventListener("click", open);
  dialog.querySelectorAll("[data-dialog-close]").forEach((button) => button.addEventListener("click", close));
  dialog.addEventListener("close", () => { if (opener?.isConnected) opener.focus(); opener = null; });
  newRuleButton.addEventListener("click", () => openRule());
  ruleForm.elements.resourceType.addEventListener("change", () => syncMetricOptions());
  ruleDialog.querySelectorAll("[data-dialog-close]").forEach((button) => button.addEventListener("click", () => ruleDialog.close()));
  ruleDialog.addEventListener("close", () => { if (ruleOpener?.isConnected) ruleOpener.focus(); ruleOpener = null; });
  ruleForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!admin || !ruleForm.reportValidity()) return;
    const form = new FormData(ruleForm);
    const id = String(form.get("id") || "");
    const payload = {
      name: String(form.get("name") || "").trim(),
      resourceType: String(form.get("resourceType") || "system"),
      metric: String(form.get("metric") || "").trim(),
      nodeSelector: String(form.get("nodeSelector") || "*").trim(),
      resourceSelector: String(form.get("resourceSelector") || "*").trim(),
      operator: String(form.get("operator") || "gt"),
      threshold: Number(form.get("threshold")),
      forSeconds: Number(form.get("forSeconds")),
      cooldownSeconds: Number(form.get("cooldownSeconds")),
      severity: String(form.get("severity") || "warning"),
      enabled: form.get("enabled") === "on",
    };
    ruleSubmit.disabled = true;
    ruleError.hidden = true;
    try {
      let saved;
      if (demo) saved = { ...payload, id: id || `demo_${Date.now()}` };
      else saved = id ? await api.updateAlertRule(id, payload) : await api.createAlertRule(payload);
      const index = rules.findIndex((rule) => rule.id === saved.id);
      if (index >= 0) rules[index] = saved; else rules.push(saved);
      renderRules();
      ruleDialog.close();
      toast(id ? "Alert rule updated." : "Alert rule created.");
    } catch (error) {
      ruleError.textContent = error?.message || "Unable to save the alert rule.";
      ruleError.hidden = false;
    } finally {
      ruleSubmit.disabled = false;
    }
  });
  ntfyTest.addEventListener("click", async () => {
    ntfyTest.disabled = true;
    try {
      if (!demo) await api.testNtfy();
      toast("ntfy test push delivered.");
    } catch (error) {
      toast(error?.message || "ntfy test push failed.", "error");
    } finally {
      ntfyTest.disabled = !admin || ntfyStatus.dataset.configured !== "true";
    }
  });

  return {
    open,
    refresh,
    setAdmin(value) { admin = Boolean(value); ntfyTest.disabled = !admin || ntfyStatus.dataset.configured !== "true"; if (dialog.open) { renderRules(); renderStates(); } },
    setNode(value) { node = value === "local" ? "local" : value || ""; if (dialog.open) refresh(); },
  };
}
