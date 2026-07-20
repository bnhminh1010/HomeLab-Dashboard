const MAX_CONFIG_BYTES = 1024 * 1024;
const CONFIG_VERSION = "homelab-dashboard.config/v1";

function demoDocument() {
  return { version: CONFIG_VERSION, services: [], alertRules: [], uiPreferences: { terminalHeight: 200, terminalCollapsed: true, historyRange: "24h", defaultNodeId: "local" }, nodes: [] };
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

export function createSettingsController({ api, demo = false, toast, onApplied }) {
  const openButton = document.getElementById("settings-open");
  const dialog = document.getElementById("settings-dialog");
  const exportButton = document.getElementById("config-export");
  const fileInput = document.getElementById("config-file");
  const modePicker = document.getElementById("config-mode");
  const previewButton = document.getElementById("config-preview");
  const applyButton = document.getElementById("config-apply");
  const result = document.getElementById("config-preview-result");
  const status = document.getElementById("settings-status");
  let admin = false;
  let opener = null;
  let parsedDocument = null;
  let validPreview = null;
  let previewMode = "";

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
    if (!dialog.open) dialog.showModal();
    dialog.querySelector("[data-dialog-close]")?.focus();
  }

  function close() {
    if (dialog.open) dialog.close();
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
      if (candidate.version !== CONFIG_VERSION) throw new Error(`Unsupported config version: ${candidate.version || "missing"}.`);
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
      setStatus("Configuration exported. Runtime data and secrets were excluded.");
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

  return {
    setAdmin(value) {
      admin = Boolean(value);
      exportButton.disabled = !admin;
      previewButton.disabled = !admin || !parsedDocument;
      applyButton.disabled = !admin || !validPreview || previewMode !== mode();
    },
    open,
  };
}
