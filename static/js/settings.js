const MAX_CONFIG_BYTES = 1024 * 1024;
const CONFIG_VERSION = "homelab-dashboard.config/v2";
const LEGACY_CONFIG_VERSION = "homelab-dashboard.config/v1";
const WORKSPACE_ORDER = ["overview", "services", "containers", "nodes", "history", "logs", "alerts", "topology"];
const WORKSPACE_LABELS = {
  overview: "OVERVIEW", services: "SERVICES", containers: "CONTAINERS", nodes: "NODES",
  history: "HISTORY", logs: "LOGS", alerts: "ALERTS", topology: "TOPOLOGY",
};
const OVERVIEW_WIDGET_ORDER = ["overview-attention", "overview-trend", "overview-recent-changes", "system-card", "overview-service-pulse"];
const OVERVIEW_WIDGET_LABELS = {
  "overview-attention": "Needs Attention",
  "overview-trend": "Resource Trend",
  "overview-recent-changes": "Recent Changes",
  "system-card": "System Snapshot",
  "overview-service-pulse": "Probe Coverage",
};
const OVERVIEW_WIDGET_DEFAULT_SIZES = {
  "overview-attention": "full",
  "overview-trend": "medium",
  "overview-recent-changes": "small",
  "system-card": "medium",
  "overview-service-pulse": "small",
};
const OVERVIEW_WIDGET_SIZES = new Set(["small", "medium", "full"]);

function normalizeWorkspacePreferences(preferences = {}) {
  const seen = new Set();
  const order = [];
  for (const workspace of Array.isArray(preferences.workspaceOrder) ? preferences.workspaceOrder : []) {
    if (!WORKSPACE_ORDER.includes(workspace) || seen.has(workspace)) continue;
    seen.add(workspace);
    order.push(workspace);
  }
  for (const workspace of WORKSPACE_ORDER) if (!seen.has(workspace)) order.push(workspace);
  const hidden = [...new Set(Array.isArray(preferences.hiddenWorkspaces) ? preferences.hiddenWorkspaces : [])]
    .filter((workspace) => workspace !== "overview" && WORKSPACE_ORDER.includes(workspace));
  const hiddenOverviewWidgets = [...new Set(Array.isArray(preferences.hiddenOverviewWidgets) ? preferences.hiddenOverviewWidgets : [])]
    .filter((widget) => widget !== "overview-attention" && OVERVIEW_WIDGET_ORDER.includes(widget));
  const overviewWidgetSizes = {};
  for (const widget of OVERVIEW_WIDGET_ORDER) {
    const requested = preferences.overviewWidgetSizes?.[widget];
    overviewWidgetSizes[widget] = OVERVIEW_WIDGET_SIZES.has(requested) ? requested : OVERVIEW_WIDGET_DEFAULT_SIZES[widget];
  }
  return { workspaceOrder: order, hiddenWorkspaces: hidden, hiddenOverviewWidgets, overviewWidgetSizes };
}

function demoDocument() {
  return { version: CONFIG_VERSION, services: [], alertRules: [], uiPreferences: { terminalHeight: 200, terminalCollapsed: true, historyRange: "24h", defaultNodeId: "local", ...normalizeWorkspacePreferences() }, nodes: [] };
}

function downloadJSON(document) {
  const blob = new Blob([`${JSON.stringify(document, null, 2)}\n`], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const anchor = documentElement("a");
  anchor.href = url;
  anchor.download = `homelab-dashboard-config-${new Date().toISOString().slice(0, 10)}.json`;
  anchor.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function documentElement(tag) {
  return globalThis.document.createElement(tag);
}

function previewSummary(preview) {
  const wrapper = documentElement("div");
  const list = documentElement("dl");
  const summary = preview?.summary || {};
  for (const [section, counts] of Object.entries(summary)) {
    const term = documentElement("dt");
    term.textContent = section.toUpperCase();
    const detail = documentElement("dd");
    detail.textContent = `+${Number(counts.added) || 0}  ~${Number(counts.updated) || 0}  −${Number(counts.deleted) || 0}  =${Number(counts.unchanged) || 0}${counts.skipped ? `  skipped ${counts.skipped}` : ""}`;
    list.append(term, detail);
  }
  if (!list.children.length) {
    const message = documentElement("span");
    message.textContent = "Preview completed with no configuration changes.";
    wrapper.append(message);
  } else {
    wrapper.append(list);
  }
  for (const warning of preview?.warnings || []) {
    const item = documentElement("p");
    item.className = "warning-text";
    item.textContent = String(warning);
    wrapper.append(item);
  }
  return wrapper;
}

export function createSettingsController({ api, demo = false, toast, onApplied, onWorkspacePreferencesApplied, onOverviewPreferencesApplied }) {
  const openButton = document.getElementById("settings-open");
  const dialog = document.getElementById("settings-dialog");
  const exportButton = document.getElementById("config-export");
  const fileInput = document.getElementById("config-file");
  const modePicker = document.getElementById("config-mode");
  const previewButton = document.getElementById("config-preview");
  const applyButton = document.getElementById("config-apply");
  const result = document.getElementById("config-preview-result");
  const status = document.getElementById("settings-status");
  const workspaceList = document.getElementById("sidebar-workspaces-list");
  const workspaceApplyButton = document.getElementById("workspace-config-apply");
  const workspaceCancelButton = document.getElementById("workspace-config-cancel");
  const workspaceReadonly = document.getElementById("workspace-config-readonly");
  const overviewWidgetsList = document.getElementById("overview-widgets-list");
  const overviewApplyButton = document.getElementById("overview-config-apply");
  const overviewCancelButton = document.getElementById("overview-config-cancel");
  const overviewReadonly = document.getElementById("overview-config-readonly");
  let admin = false;
  let authenticated = Boolean(demo);
  let opener = null;
  let parsedDocument = null;
  let validPreview = null;
  let previewMode = "";
  let workspacePreferences = normalizeWorkspacePreferences();
  let workspaceDraft = normalizeWorkspacePreferences();
  let overviewDraft = normalizeWorkspacePreferences();

  const mode = () => modePicker.querySelector('input[name="config-import-mode"]:checked')?.value || "merge";

  function setStatus(message, level = "info") {
    status.textContent = message || "";
    status.dataset.level = level;
  }

  function resetPreview(message = "Select a JSON file to preview an import.") {
    validPreview = null;
    previewMode = "";
    applyButton.disabled = true;
    result.replaceChildren();
    const text = documentElement("span");
    text.textContent = message;
    result.append(text);
  }

  function open() {
    opener = document.activeElement;
    workspaceDraft = normalizeWorkspacePreferences(workspacePreferences);
    overviewDraft = normalizeWorkspacePreferences(workspacePreferences);
    renderWorkspaceList();
    renderOverviewWidgets();
    if (!dialog.open) dialog.showModal();
    dialog.querySelector("[data-dialog-close]")?.focus();
  }

  function close() {
    if (dialog.open) dialog.close();
  }

  function renderWorkspaceList() {
    if (!workspaceList) return;
    workspaceList.replaceChildren();
    const hidden = new Set(workspaceDraft.hiddenWorkspaces);
    workspaceDraft.workspaceOrder.forEach((workspace, index) => {
      const item = documentElement("div");
      item.className = "workspace-config-item";
      item.dataset.workspaceConfig = workspace;
      const label = documentElement("label");
      label.className = "workspace-config-label";
      const visible = documentElement("input");
      visible.type = "checkbox";
      visible.checked = !hidden.has(workspace);
      visible.disabled = !admin || workspace === "overview";
      visible.setAttribute("aria-label", `${WORKSPACE_LABELS[workspace]} visibility`);
      visible.addEventListener("change", () => {
        const nextHidden = new Set(workspaceDraft.hiddenWorkspaces);
        if (visible.checked) nextHidden.delete(workspace);
        else nextHidden.add(workspace);
        workspaceDraft.hiddenWorkspaces = [...nextHidden];
      });
      const text = documentElement("span");
      text.textContent = workspace === "overview" ? `${WORKSPACE_LABELS[workspace]} · REQUIRED` : WORKSPACE_LABELS[workspace];
      label.append(visible, text);
      const actions = documentElement("div");
      actions.className = "workspace-config-actions";
      for (const [direction, symbol, labelText] of [[-1, "↑", "Move up"], [1, "↓", "Move down"]]) {
        const button = documentElement("button");
        button.type = "button";
        button.textContent = symbol;
        button.title = labelText;
        button.dataset.workspaceMove = String(direction);
        button.setAttribute("aria-label", `${labelText} ${WORKSPACE_LABELS[workspace]}`);
        button.disabled = !admin || (direction < 0 ? index === 0 : index === workspaceDraft.workspaceOrder.length - 1);
        button.addEventListener("click", () => {
          const nextIndex = index + direction;
          const next = [...workspaceDraft.workspaceOrder];
          [next[index], next[nextIndex]] = [next[nextIndex], next[index]];
          workspaceDraft.workspaceOrder = next;
          renderWorkspaceList();
        });
        actions.append(button);
      }
      item.append(label, actions);
      workspaceList.append(item);
    });
    workspaceReadonly.hidden = admin;
    workspaceApplyButton.disabled = !admin;
    // Cancel is a local UI action and must remain available in read-only mode.
    workspaceCancelButton.disabled = false;
  }

  function renderOverviewWidgets() {
    if (!overviewWidgetsList) return;
    overviewWidgetsList.replaceChildren();
    const hidden = new Set(overviewDraft.hiddenOverviewWidgets);
    for (const widget of OVERVIEW_WIDGET_ORDER) {
      const item = documentElement("div");
      item.className = "overview-widget-config-item";
      const label = documentElement("label");
      label.className = "workspace-config-label";
      const visible = documentElement("input");
      visible.type = "checkbox";
      visible.checked = !hidden.has(widget);
      visible.disabled = widget === "overview-attention" || !authenticated;
      visible.setAttribute("aria-label", `${OVERVIEW_WIDGET_LABELS[widget]} visibility`);
      visible.addEventListener("change", () => {
        const nextHidden = new Set(overviewDraft.hiddenOverviewWidgets);
        if (visible.checked) nextHidden.delete(widget);
        else nextHidden.add(widget);
        overviewDraft.hiddenOverviewWidgets = [...nextHidden];
      });
      const text = documentElement("span");
      text.textContent = widget === "overview-attention" ? `${OVERVIEW_WIDGET_LABELS[widget]} · REQUIRED` : OVERVIEW_WIDGET_LABELS[widget];
      label.append(visible, text);
      const size = documentElement("select");
      size.className = "overview-widget-size-select mono";
      size.disabled = !authenticated;
      size.setAttribute("aria-label", `${OVERVIEW_WIDGET_LABELS[widget]} size`);
      for (const optionValue of ["small", "medium", "full"]) {
        const option = documentElement("option");
        option.value = optionValue;
        option.textContent = optionValue.toUpperCase();
        option.selected = overviewDraft.overviewWidgetSizes[widget] === optionValue;
        size.append(option);
      }
      size.addEventListener("change", () => { overviewDraft.overviewWidgetSizes[widget] = size.value; });
      item.append(label, size);
      overviewWidgetsList.append(item);
    }
    overviewReadonly.hidden = authenticated;
    overviewReadonly.textContent = authenticated ? "" : "Sign in to save overview widget preferences.";
    overviewApplyButton.disabled = !authenticated;
    overviewCancelButton.disabled = false;
  }

  async function readFile(file) {
    parsedDocument = null;
    resetPreview("Reading configuration file…");
    previewButton.disabled = true;
    if (!file) {
      resetPreview();
      return;
    }
    if (file.size > MAX_CONFIG_BYTES) {
      resetPreview("The selected file exceeds the 1 MiB import limit.");
      setStatus("Configuration file rejected: too large.", "error");
      return;
    }
    try {
      const text = await file.text();
      if (new TextEncoder().encode(text).byteLength > MAX_CONFIG_BYTES) throw new Error("The decoded file exceeds 1 MiB.");
      const candidate = JSON.parse(text);
      if (!candidate || typeof candidate !== "object" || Array.isArray(candidate)) throw new Error("The top-level JSON value must be an object.");
      if (![CONFIG_VERSION, LEGACY_CONFIG_VERSION].includes(candidate.version)) throw new Error(`Unsupported config version: ${candidate.version || "missing"}.`);
      parsedDocument = candidate;
      previewButton.disabled = !admin;
      resetPreview(`Ready to preview ${file.name} in ${mode()} mode.`);
      setStatus("File validated locally. Server preview is required before apply.");
    } catch (error) {
      resetPreview(error?.message || "The selected file is not valid JSON.");
      setStatus("Configuration file rejected.", "error");
    }
  }

  openButton.addEventListener("click", open);
  dialog.querySelectorAll("[data-dialog-close]").forEach((button) => button.addEventListener("click", close));
  dialog.addEventListener("close", () => {
    if (opener?.isConnected) opener.focus();
    opener = null;
  });
  exportButton.addEventListener("click", async () => {
    if (!admin) return;
    exportButton.disabled = true;
    setStatus("Preparing sanitized configuration export…");
    try {
      const payload = demo || typeof api.exportDashboardConfig !== "function" ? demoDocument() : await api.exportDashboardConfig();
      downloadJSON(payload);
      setStatus("Configuration exported. Runtime data, secrets, and local maintenance schedules were excluded.");
    } catch (error) {
      setStatus(error?.message || "Unable to export dashboard configuration.", "error");
    } finally {
      exportButton.disabled = false;
    }
  });
  fileInput.addEventListener("change", () => readFile(fileInput.files?.[0]));
  modePicker.addEventListener("change", () => {
    if (!parsedDocument) return;
    resetPreview(`Mode changed to ${mode()}. Run preview again before apply.`);
    previewButton.disabled = !admin;
  });
  previewButton.addEventListener("click", async () => {
    if (!admin || !parsedDocument) return;
    previewButton.disabled = true;
    applyButton.disabled = true;
    setStatus("Computing an atomic import preview…");
    try {
      const payload = demo
		? { version: CONFIG_VERSION, mode: mode(), revision: "demo", summary: { services: { added: 0, updated: 0, deleted: 0, unchanged: 0, skipped: 0 }, alertRules: { added: 0, updated: 0, deleted: 0, unchanged: 0, skipped: 0 } }, changes: [], warnings: [] }
        : await api.previewDashboardImport(parsedDocument, mode());
      validPreview = payload;
      previewMode = mode();
      result.replaceChildren(previewSummary(payload));
      applyButton.disabled = false;
      setStatus(`Preview ready in ${previewMode} mode. Review it before applying.`);
    } catch (error) {
      validPreview = null;
      result.replaceChildren();
      const message = documentElement("span");
      message.textContent = error?.message || "Unable to preview the import.";
      result.append(message);
      setStatus("Server rejected the import preview.", "error");
    } finally {
      previewButton.disabled = !parsedDocument || !admin;
    }
  });
  applyButton.addEventListener("click", async () => {
    if (!admin || !parsedDocument || !validPreview || previewMode !== mode()) return;
    if (mode() === "replace" && !window.confirm("Replace dashboard configuration using this reviewed preview? Items listed as deleted will be removed.")) return;
    applyButton.disabled = true;
    previewButton.disabled = true;
    setStatus("Applying configuration transaction…");
    try {
	  const payload = demo ? { preview: validPreview } : await api.applyDashboardImport(parsedDocument, mode(), validPreview.revision);
      validPreview = null;
      result.replaceChildren(previewSummary(payload?.preview || {}));
      setStatus("Import applied successfully. Dashboard data is refreshing.");
      toast("Dashboard configuration imported.");
      await onApplied?.();
	} catch (error) {
	  if (error?.status === 412 || error?.code === "config_revision_conflict") {
		validPreview = null;
		applyButton.disabled = true;
		result.replaceChildren();
		const message = documentElement("span");
		message.textContent = "Dashboard configuration changed after this preview. Run preview again before applying.";
		result.append(message);
		setStatus("Preview expired because the dashboard changed.", "error");
	  } else {
		setStatus(error?.message || "Import failed; no partial changes were applied.", "error");
		applyButton.disabled = false;
	  }
    } finally {
      previewButton.disabled = !parsedDocument || !admin;
    }
  });
  workspaceCancelButton?.addEventListener("click", () => {
    workspaceDraft = normalizeWorkspacePreferences(workspacePreferences);
    renderWorkspaceList();
    setStatus("Workspace layout changes discarded.");
    close();
  });
  overviewCancelButton?.addEventListener("click", () => {
    overviewDraft = normalizeWorkspacePreferences(workspacePreferences);
    renderOverviewWidgets();
    setStatus("Overview widget changes discarded.");
    close();
  });
  workspaceApplyButton?.addEventListener("click", async () => {
    if (!admin || typeof onWorkspacePreferencesApplied !== "function") return;
    workspaceApplyButton.disabled = true;
    workspaceCancelButton.disabled = true;
    setStatus("Saving workspace layout…");
    try {
      const saved = await onWorkspacePreferencesApplied(normalizeWorkspacePreferences(workspaceDraft));
      workspacePreferences = normalizeWorkspacePreferences(saved || workspaceDraft);
      workspaceDraft = normalizeWorkspacePreferences(workspacePreferences);
      renderWorkspaceList();
      setStatus("Workspace layout saved.");
      toast("Workspace layout updated.");
    } catch (error) {
      setStatus(error?.message || "Unable to save workspace layout.", "error");
      renderWorkspaceList();
    }
  });
  overviewApplyButton?.addEventListener("click", async () => {
    if (!authenticated || typeof onOverviewPreferencesApplied !== "function") return;
    overviewApplyButton.disabled = true;
    overviewCancelButton.disabled = true;
    setStatus("Saving overview widget layout…");
    try {
      const saved = await onOverviewPreferencesApplied(normalizeWorkspacePreferences(overviewDraft));
      workspacePreferences = normalizeWorkspacePreferences(saved || overviewDraft);
      overviewDraft = normalizeWorkspacePreferences(workspacePreferences);
      renderOverviewWidgets();
      setStatus("Overview widget layout saved.");
      toast("Overview widgets updated.");
    } catch (error) {
      setStatus(error?.message || "Unable to save overview widget layout.", "error");
      renderOverviewWidgets();
    }
  });

  return {
    setAdmin(value) {
      admin = Boolean(value);
      exportButton.disabled = !admin;
      previewButton.disabled = !admin || !parsedDocument;
      applyButton.disabled = !admin || !validPreview || previewMode !== mode();
      renderWorkspaceList();
      renderOverviewWidgets();
    },
    setSession(value = {}) {
      authenticated = value.authenticated === true;
      renderWorkspaceList();
      renderOverviewWidgets();
    },
    setWorkspacePreferences(preferences) {
      workspacePreferences = normalizeWorkspacePreferences(preferences);
      workspaceDraft = normalizeWorkspacePreferences(workspacePreferences);
      overviewDraft = normalizeWorkspacePreferences(workspacePreferences);
      renderWorkspaceList();
      renderOverviewWidgets();
    },
    open,
  };
}
