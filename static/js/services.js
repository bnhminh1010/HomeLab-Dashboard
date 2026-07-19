import { displayEndpoint, safeHttpUrl, timeAgo } from "./format.js";

function normalizedService(service = {}) {
  return {
    id: String(service.id || service.ID || ""),
    name: String(service.name || "Unnamed service"),
    icon: String(service.icon || "◇"),
    displayUrl: String(service.displayUrl || service.displayURL || service.url || ""),
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

function button(label, className = "") {
  const element = document.createElement("button");
  element.type = "button";
  element.textContent = label;
  element.className = className;
  element.setAttribute("role", "menuitem");
  return element;
}

export function createServicesController({ api, toast }) {
  const grid = document.getElementById("services-grid");
  const empty = document.getElementById("services-empty");
  const count = document.getElementById("services-count");
  const quickForm = document.getElementById("quick-add-form");
  const focusAdd = document.getElementById("focus-add-service");
  const menu = document.getElementById("context-menu");
  const serviceDialog = document.getElementById("service-dialog");
  const serviceForm = document.getElementById("service-form");
  const serviceError = document.getElementById("service-form-error");
  const deleteDialog = document.getElementById("delete-dialog");
  const deleteForm = document.getElementById("delete-form");
  const deleteError = document.getElementById("delete-form-error");
  const deleteName = document.getElementById("delete-service-name");
  let services = [];
  let admin = false;
  let initialized = false;
  let menuTrigger = null;

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
      icon: payload.icon.trim() || "◇",
      displayUrl: displayUrl.toString(),
      probeUrl: probeUrl?.toString() || "",
    };
  }

  function payloadFrom(form) {
    const data = new FormData(form);
    return validate({
      name: String(data.get("name") || ""),
      icon: String(data.get("icon") || ""),
      displayUrl: String(data.get("displayUrl") || ""),
      probeUrl: String(data.get("probeUrl") || ""),
    });
  }

  function render(nextServices) {
    initialized = true;
    services = Array.isArray(nextServices) ? nextServices.map(normalizedService) : [];
    count.textContent = String(services.length);
    grid.setAttribute("aria-busy", "false");
    grid.replaceChildren(...services.map(serviceCard));
    grid.hidden = services.length === 0;
    empty.hidden = services.length !== 0;
  }

  function serviceCard(service) {
    const article = document.createElement("article");
    article.className = "service-card";
    article.dataset.serviceId = service.id;
    article.tabIndex = 0;
    article.setAttribute("aria-label", `Open ${service.name} in a new tab`);

    const main = document.createElement("div");
    main.className = "service-main";
    const status = document.createElement("span");
    status.className = "service-status status-dot";
    status.dataset.status = service.status;
    status.title = `Status: ${service.status}`;
    status.setAttribute("aria-label", `Status ${service.status}`);
    const icon = document.createElement("span");
    icon.className = "service-icon";
    icon.textContent = service.icon;
    icon.setAttribute("aria-hidden", "true");
    const name = document.createElement("span");
    name.className = "service-name";
    name.textContent = service.name;
    name.title = service.name;
    main.append(status, icon, name);
    article.append(main);

    if (admin) {
      const trigger = document.createElement("button");
      trigger.type = "button";
      trigger.className = "service-menu-button";
      trigger.textContent = "⋮";
      trigger.setAttribute("aria-label", `Actions for ${service.name}`);
      trigger.setAttribute("aria-haspopup", "menu");
      trigger.addEventListener("click", (event) => {
        event.stopPropagation();
        const rect = trigger.getBoundingClientRect();
        openMenu(service, rect.right, rect.bottom, trigger);
      });
      article.append(trigger);
    }

    const url = safeHttpUrl(service.displayUrl);
    const link = document.createElement("a");
    link.className = "service-link";
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    if (url) link.href = url.toString();
    else link.removeAttribute("href");
    link.setAttribute("aria-label", `Open ${service.name}`);
    const linkMark = document.createElement("span");
    linkMark.textContent = "↗";
    linkMark.setAttribute("aria-hidden", "true");
    const endpoint = document.createElement("span");
    endpoint.textContent = displayEndpoint(service.displayUrl);
    endpoint.title = service.displayUrl;
    link.append(linkMark, endpoint);
    article.append(link);

    const meta = document.createElement("div");
    meta.className = "service-meta";
    const check = document.createElement("span");
    check.textContent = service.lastCheckedAt ? `Checked ${timeAgo(service.lastCheckedAt)}` : "Probe not configured";
    const latency = document.createElement("span");
    latency.textContent = Number.isFinite(service.latencyMs) ? `${Math.round(service.latencyMs)} ms` : service.status.toUpperCase();
    meta.append(check, latency);
    article.append(meta);

    if (admin) {
      article.addEventListener("contextmenu", (event) => {
        event.preventDefault();
        openMenu(service, event.clientX, event.clientY, article);
      });
    }

    const openService = (event) => {
      if (event.target.closest("a, button") || !url) return;
      window.open(url.toString(), "_blank", "noopener,noreferrer");
    };
    article.addEventListener("click", openService);
    article.addEventListener("keydown", (event) => {
      if ((event.key === "Enter" || event.key === " ") && !event.target.closest("a, button")) {
        event.preventDefault();
        openService(event);
      }
    });
    return article;
  }

  function openMenu(service, x, y, trigger) {
    closeMenu(false);
    menuTrigger = trigger;
    const edit = button("Edit service");
    edit.addEventListener("click", () => { closeMenu(false); openEdit(service); });
    const copy = button("Copy URL");
    copy.addEventListener("click", async () => {
      closeMenu(false);
      try {
        await navigator.clipboard.writeText(service.displayUrl);
        toast("Service URL copied.");
      } catch {
        toast("Clipboard access is unavailable in this browser.", "error");
      }
    });
    const remove = button("Delete service", "danger");
    remove.addEventListener("click", () => { closeMenu(false); openDelete(service); });
    menu.replaceChildren(edit, copy, remove);
    menu.hidden = false;
    menu.style.left = `${Math.min(x, window.innerWidth - 174)}px`;
    menu.style.top = `${Math.min(y, window.innerHeight - 125)}px`;
    requestAnimationFrame(() => edit.focus());
  }

  function closeMenu(restoreFocus = true) {
    if (menu.hidden) return;
    menu.hidden = true;
    menu.replaceChildren();
    if (restoreFocus) menuTrigger?.focus?.();
    menuTrigger = null;
  }

  function openEdit(service) {
    serviceForm.elements.id.value = service.id;
    serviceForm.elements.name.value = service.name;
    serviceForm.elements.icon.value = service.icon;
    serviceForm.elements.displayUrl.value = service.displayUrl;
    serviceForm.elements.probeUrl.value = service.probeUrl;
    serviceError.hidden = true;
    serviceDialog.showModal();
    serviceForm.elements.name.focus();
  }

  function openDelete(service) {
    deleteForm.elements.id.value = service.id;
    deleteName.textContent = service.name;
    deleteError.hidden = true;
    deleteDialog.showModal();
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

  quickForm.addEventListener("submit", (event) => {
    event.preventDefault();
    if (!admin) return;
    const submit = quickForm.querySelector("button[type=submit]");
    let payload;
    try { payload = payloadFrom(quickForm); } catch (error) { toast(error.message, "error"); return; }
    mutate(submit, () => api.createService(payload), async (result) => {
      const created = serviceFromResponse(result, payload);
      if (created?.id) render([...services, created]);
      quickForm.reset();
      toast(`${payload.name} added.`);
    });
  });

  serviceForm.addEventListener("submit", (event) => {
    event.preventDefault();
    const submit = serviceForm.querySelector("button[type=submit]");
    let payload;
    try { payload = payloadFrom(serviceForm); } catch (error) {
      serviceError.textContent = error.message;
      serviceError.hidden = false;
      return;
    }
    const id = serviceForm.elements.id.value;
    mutate(submit, () => api.updateService(id, payload), async (result) => {
      const updated = serviceFromResponse(result, { id, ...payload }) || normalizedService({ id, ...payload });
      render(services.map((service) => service.id === id ? { ...service, ...updated } : service));
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
      render(services.filter((service) => service.id !== id));
      deleteDialog.close();
      toast(`${name} deleted.`);
    }, deleteError);
  });

  focusAdd.addEventListener("click", () => {
    document.getElementById("quick-add-card").scrollIntoView({ behavior: "smooth", block: "center" });
    document.getElementById("quick-name").focus({ preventScroll: true });
  });

  document.addEventListener("pointerdown", (event) => {
    if (!menu.hidden && !menu.contains(event.target) && event.target !== menuTrigger) closeMenu(false);
  });
  document.addEventListener("keydown", (event) => {
    if (menu.hidden) return;
    if (event.key === "Escape") { event.preventDefault(); closeMenu(); return; }
    const items = [...menu.querySelectorAll("button")];
    const current = items.indexOf(document.activeElement);
    if (event.key === "ArrowDown") { event.preventDefault(); items[(current + 1) % items.length]?.focus(); }
    if (event.key === "ArrowUp") { event.preventDefault(); items[(current - 1 + items.length) % items.length]?.focus(); }
  });
  for (const closeButton of document.querySelectorAll("[data-dialog-close]")) {
    closeButton.addEventListener("click", () => closeButton.closest("dialog")?.close());
  }
  for (const dialog of [serviceDialog, deleteDialog]) {
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) dialog.close();
    });
  }

  return {
    render,
    setAdmin(value) {
      admin = Boolean(value);
      if (initialized) render(services);
    },
  };
}
