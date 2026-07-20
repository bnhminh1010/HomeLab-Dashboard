import { displayEndpoint, safeHttpUrl, timeAgo } from "./format.js";

const UP_STATES = new Set(["up", "running", "healthy"]);
const DOWN_STATES = new Set(["down", "error", "unhealthy", "crashed"]);

function normalizedService(service = {}, index = 0) {
  const id = String(service.id || service.ID || "");
  const displayUrl = String(service.displayUrl || service.displayURL || service.url || "");
  const name = String(service.name || "Unnamed service");
  return {
    id,
    key: id || displayUrl || `${name}-${index}`,
    originalIndex: index,
    name,
    icon: String(service.icon || "◇"),
    displayUrl,
    probeUrl: String(service.probeUrl || service.probeURL || ""),
    status: String(service.status || service.health?.status || "unknown").toLowerCase(),
    lastCheckedAt: service.lastCheckedAt || service.lastCheck || null,
    latencyMs: Number(service.latencyMs ?? service.latency ?? NaN),
  };
}

function serviceFromResponse(payload, fallback) {
  const candidate = payload?.data?.service || payload?.service || payload?.data || payload;
  return candidate && typeof candidate === "object" && !Array.isArray(candidate)
    ? normalizedService({ ...fallback, ...candidate })
    : null;
}

function menuButton(label, className = "") {
  const element = document.createElement("button");
  element.type = "button";
  element.textContent = label;
  element.className = className;
  element.setAttribute("role", "menuitem");
  element.tabIndex = -1;
  return element;
}

function stateLabel(status) {
  if (UP_STATES.has(status)) return "UP";
  if (DOWN_STATES.has(status)) return "DOWN";
  if (status === "degraded" || status === "warning") return "DEGRADED";
  return "NO PROBE";
}

function statePriority(status) {
  if (DOWN_STATES.has(status)) return 0;
  if (status === "degraded" || status === "warning") return 1;
  if (!UP_STATES.has(status)) return 2;
  return 3;
}

export function createServicesController({ api, toast, onChanged }) {
  const grid = document.getElementById("services-grid");
  const empty = document.getElementById("services-empty");
  const count = document.getElementById("services-count");
  const focusAdd = document.getElementById("focus-add-service");
  const menu = document.getElementById("context-menu");
  const serviceDialog = document.getElementById("service-dialog");
  const serviceForm = document.getElementById("service-form");
  const serviceTitle = document.getElementById("service-dialog-title");
  const serviceSubmit = document.getElementById("service-form-submit");
  const serviceError = document.getElementById("service-form-error");
  const deleteDialog = document.getElementById("delete-dialog");
  const deleteForm = document.getElementById("delete-form");
  const deleteError = document.getElementById("delete-form-error");
  const deleteName = document.getElementById("delete-service-name");
  const cards = new Map();
  let services = [];
  let admin = false;
  let initialized = false;
  let menuTrigger = null;
  let dialogInvoker = null;

  function validate(payload) {
    let displayValue = payload.displayUrl.trim();
    if (/^\d{1,5}$/.test(displayValue)) {
      const port = Number(displayValue);
      if (port < 1 || port > 65535) throw new Error("Port must be between 1 and 65535.");
      displayValue = `http://${window.location.hostname}:${port}`;
    }
    const displayUrl = safeHttpUrl(displayValue);
    const probeUrl = payload.probeUrl ? safeHttpUrl(payload.probeUrl) : null;
    if (!payload.name.trim()) throw new Error("Service name is required.");
    if (!displayUrl) throw new Error("Display URL must be an absolute HTTP or HTTPS URL without credentials.");
    if (payload.probeUrl && !probeUrl) throw new Error("Probe URL must be an absolute HTTP or HTTPS URL without credentials.");
    return {
      name: payload.name.trim(),
      displayUrl: displayUrl.toString(),
      probeUrl: probeUrl?.toString() || "",
    };
  }

  function payloadFrom(form) {
    const data = new FormData(form);
    return validate({
      name: String(data.get("name") || ""),
      displayUrl: String(data.get("displayUrl") || ""),
      probeUrl: String(data.get("probeUrl") || ""),
    });
  }

  function createCard() {
    const article = document.createElement("article");
    article.className = "service-card";

    const heading = document.createElement("div");
    heading.className = "service-main";
    const link = document.createElement("a");
    link.className = "service-link";
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    const name = document.createElement("span");
    name.className = "service-name";
    const endpoint = document.createElement("span");
    endpoint.className = "service-endpoint mono";
    link.append(name, endpoint);
    heading.append(link);

    const trigger = document.createElement("button");
    trigger.type = "button";
    trigger.className = "service-menu-button admin-only";
    trigger.textContent = "⋮";
    trigger.setAttribute("aria-haspopup", "menu");
    trigger.setAttribute("aria-expanded", "false");
    trigger.addEventListener("click", (event) => {
      event.stopPropagation();
      const rect = trigger.getBoundingClientRect();
      openMenu(article.service, rect.right, rect.bottom, trigger);
    });

    const statusRow = document.createElement("div");
    statusRow.className = "service-status-row mono";
    const status = document.createElement("span");
    status.className = "service-status";
    const dot = document.createElement("span");
    dot.className = "status-dot";
    dot.setAttribute("aria-hidden", "true");
    const statusText = document.createElement("span");
    status.append(dot, statusText);
    const latency = document.createElement("span");
    latency.className = "service-latency";
    statusRow.append(status, latency);

    const checked = document.createElement("div");
    checked.className = "service-meta mono";

    article.append(heading, trigger, statusRow, checked);
    article.refs = { link, name, endpoint, trigger, status, statusText, latency, checked };
    article.addEventListener("contextmenu", (event) => {
      if (!admin) return;
      event.preventDefault();
      openMenu(article.service, event.clientX, event.clientY, trigger);
    });
    return article;
  }

  function updateCard(article, service) {
    article.service = service;
    article.dataset.serviceId = service.id;
    const { link, name, endpoint, trigger, status, statusText, latency, checked } = article.refs;
    name.textContent = service.name;
    name.title = service.name;
    endpoint.textContent = `↗ ${displayEndpoint(service.displayUrl)}`;
    endpoint.title = service.displayUrl;
    const url = safeHttpUrl(service.displayUrl);
    if (url) {
      link.href = url.toString();
      link.removeAttribute("aria-disabled");
    } else {
      link.removeAttribute("href");
      link.setAttribute("aria-disabled", "true");
    }
    link.setAttribute("aria-label", `Open ${service.name} in a new tab`);
    trigger.hidden = !admin;
    trigger.setAttribute("aria-label", `Actions for ${service.name}`);
    status.dataset.status = service.status;
    statusText.textContent = stateLabel(service.status);
    status.setAttribute("aria-label", `Status ${stateLabel(service.status)}`);
    latency.textContent = Number.isFinite(service.latencyMs) ? `${Math.round(service.latencyMs)} ms` : "—";
    checked.hidden = !service.lastCheckedAt;
    checked.textContent = service.lastCheckedAt ? `Checked ${timeAgo(service.lastCheckedAt)}` : "";
  }

  function summary() {
    const up = services.filter((service) => UP_STATES.has(service.status)).length;
    const down = services.filter((service) => DOWN_STATES.has(service.status) || ["degraded", "warning"].includes(service.status)).length;
    return { total: services.length, up, down, unknown: Math.max(0, services.length - up - down) };
  }

  function render(nextServices) {
    initialized = true;
    const focused = grid.contains(document.activeElement) ? document.activeElement : null;
    grid.querySelectorAll(".skeleton-card").forEach((node) => node.remove());
    services = Array.isArray(nextServices) ? nextServices.map(normalizedService) : [];
    services.sort((a, b) => statePriority(a.status) - statePriority(b.status) || a.originalIndex - b.originalIndex);
    count.textContent = String(services.length);
    grid.setAttribute("aria-busy", "false");

    const nextKeys = new Set(services.map((service) => service.key));
    for (const [key, card] of cards) {
      if (nextKeys.has(key)) continue;
      if (card.contains(menuTrigger)) closeMenu(false);
      card.remove();
      cards.delete(key);
    }
    for (const service of services) {
      let card = cards.get(service.key);
      if (!card) {
        card = createCard();
        cards.set(service.key, card);
      }
      updateCard(card, service);
      grid.append(card);
    }
    grid.hidden = services.length === 0;
    empty.hidden = services.length !== 0;
    if (focused?.isConnected && document.activeElement !== focused) focused.focus({ preventScroll: true });
    return summary();
  }

  function openMenu(service, x, y, trigger) {
    if (!admin || !service) return;
    closeMenu(false);
    menuTrigger = trigger;
    trigger.setAttribute("aria-expanded", "true");
    const edit = menuButton("Edit service");
    edit.addEventListener("click", () => {
      const invoker = menuTrigger;
      closeMenu(false);
      openEdit(service, invoker);
    });
    const copy = menuButton("Copy URL");
    copy.addEventListener("click", async () => {
      const invoker = menuTrigger;
      closeMenu(false);
      try {
        await navigator.clipboard.writeText(service.displayUrl);
        toast("Service URL copied.");
        invoker?.focus();
      } catch {
        toast("Clipboard access is unavailable in this browser.", "error");
        invoker?.focus();
      }
    });
    const remove = menuButton("Delete service", "danger");
    remove.addEventListener("click", () => {
      const invoker = menuTrigger;
      closeMenu(false);
      openDelete(service, invoker);
    });
    menu.replaceChildren(edit, copy, remove);
    menu.hidden = false;
    menu.style.left = `${Math.max(8, Math.min(x, window.innerWidth - 174))}px`;
    menu.style.top = `${Math.max(8, Math.min(y, window.innerHeight - 132))}px`;
    requestAnimationFrame(() => edit.focus());
  }

  function closeMenu(restoreFocus = true) {
    if (menu.hidden) return;
    const trigger = menuTrigger;
    trigger?.setAttribute("aria-expanded", "false");
    menu.hidden = true;
    menu.replaceChildren();
    menuTrigger = null;
    if (restoreFocus) trigger?.focus?.();
  }

  function showServiceDialog(invoker) {
    dialogInvoker = invoker || document.activeElement;
    serviceError.hidden = true;
    serviceDialog.showModal();
    serviceForm.elements.name.focus();
  }

  function openCreate(invoker) {
    serviceForm.reset();
    serviceForm.elements.id.value = "";
    serviceTitle.textContent = "Add service";
    serviceSubmit.textContent = "ADD SERVICE";
    showServiceDialog(invoker);
  }

  function openEdit(service, invoker) {
    serviceForm.elements.id.value = service.id;
    serviceForm.elements.name.value = service.name;
    serviceForm.elements.probeUrl.value = service.probeUrl;
    serviceTitle.textContent = "Edit service";
    serviceSubmit.textContent = "SAVE CHANGES";
    showServiceDialog(invoker);
  }

  function openDelete(service, invoker) {
    dialogInvoker = invoker || document.activeElement;
    deleteForm.elements.id.value = service.id;
    deleteName.textContent = service.name;
    deleteError.hidden = true;
    deleteDialog.showModal();
    requestAnimationFrame(() => deleteDialog.querySelector("[data-dialog-close]")?.focus());
  }

  async function mutate(buttonElement, action, onSuccess, errorElement = null) {
    buttonElement.disabled = true;
    if (errorElement) errorElement.hidden = true;
    try {
      const result = await action();
      await onSuccess(result);
    } catch (error) {
      const message = error?.message || "The service request failed.";
      if (errorElement) {
        errorElement.textContent = message;
        errorElement.hidden = false;
      } else toast(message, "error");
    } finally {
      buttonElement.disabled = false;
    }
  }

  serviceForm.addEventListener("submit", (event) => {
    event.preventDefault();
    if (!admin) return;
    let payload;
    try {
      payload = payloadFrom(serviceForm);
    } catch (error) {
      serviceError.textContent = error.message;
      serviceError.hidden = false;
      return;
    }
    const id = serviceForm.elements.id.value;
    if (!id) {
      mutate(serviceSubmit, () => api.createService(payload), async (result) => {
        const created = serviceFromResponse(result, payload);
        if (created?.id) {
          const nextSummary = render([...services, created]);
          onChanged?.([...services], nextSummary);
        }
        serviceDialog.close();
        toast(`${payload.name} added.`);
      }, serviceError);
      return;
    }
    mutate(serviceSubmit, () => api.updateService(id, payload), async (result) => {
      const updated = serviceFromResponse(result, { id, ...payload }) || normalizedService({ id, ...payload });
      const nextSummary = render(services.map((service) => service.id === id ? { ...service, ...updated } : service));
      onChanged?.([...services], nextSummary);
      serviceDialog.close();
      toast(`${payload.name} updated.`);
    }, serviceError);
  });

  deleteForm.addEventListener("submit", (event) => {
    event.preventDefault();
    const submit = deleteForm.querySelector("button[type=submit]");
    const id = deleteForm.elements.id.value;
    const name = deleteName.textContent;
    mutate(submit, () => api.deleteService(id), async () => {
      const nextSummary = render(services.filter((service) => service.id !== id));
      onChanged?.([...services], nextSummary);
      deleteDialog.close();
      toast(`${name} deleted.`);
    }, deleteError);
  });

  focusAdd.addEventListener("click", () => openCreate(focusAdd));
  document.addEventListener("pointerdown", (event) => {
    if (!menu.hidden && !menu.contains(event.target) && event.target !== menuTrigger) closeMenu(false);
  });
  document.addEventListener("keydown", (event) => {
    if (menu.hidden) return;
    if (event.key === "Escape") {
      event.preventDefault();
      closeMenu();
      return;
    }
    const items = [...menu.querySelectorAll("button")];
    const current = items.indexOf(document.activeElement);
    if (event.key === "ArrowDown") {
      event.preventDefault();
      items[(current + 1) % items.length]?.focus();
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      items[(current - 1 + items.length) % items.length]?.focus();
    } else if (event.key === "Home") {
      event.preventDefault();
      items[0]?.focus();
    } else if (event.key === "End") {
      event.preventDefault();
      items.at(-1)?.focus();
    }
  });

  for (const closeButton of document.querySelectorAll("[data-dialog-close]")) {
    closeButton.addEventListener("click", () => closeButton.closest("dialog")?.close());
  }
  for (const dialog of [serviceDialog, deleteDialog]) {
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) dialog.close();
    });
    dialog.addEventListener("close", () => {
      const invoker = dialogInvoker;
      dialogInvoker = null;
      // Browsers often restore focus synchronously when a modal closes. The
      // fallback runs one frame later only when there is no longer a useful
      // active element; otherwise it could steal focus from a user who has
      // already moved on to another control.
      requestAnimationFrame(() => {
        if (!invoker?.isConnected || document.activeElement === invoker) return;
        const active = document.activeElement;
        if (active === document.body || active === document.documentElement || !active?.isConnected) {
          invoker.focus({ preventScroll: true });
        }
      });
    });
  }

  return {
    render,
    setAdmin(value) {
      admin = Boolean(value);
      if (initialized) return render(services);
      return summary();
    },
  };
}
