import { copyText, displayEndpoint, safeHttpUrl, showCopied, timeAgo } from "./format.js";

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

function safeProbeUrl(value) {
  const httpUrl = safeHttpUrl(value);
  if (httpUrl) return httpUrl;
  try {
    const url = new URL(String(value));
    const port = Number(url.port);
    if (url.protocol !== "tcp:" || !url.hostname || !url.port || !Number.isInteger(port) || port < 1 || port > 65535) return null;
    if (url.username || url.password || url.pathname !== "" || url.search || url.hash) return null;
    return url;
  } catch {
    return null;
  }
}

export function createServicesController({ api, toast, onChanged }) {
  const grid = document.getElementById("services-grid");
  const empty = document.getElementById("services-empty");
  const count = document.getElementById("services-count");
  const filterCount = document.getElementById("services-filter-count");
  const filterInput = document.getElementById("services-filter-input");
  const filterButtons = [...document.querySelectorAll("[data-service-filter]")];
  const filterBar = document.getElementById("services-filter-bar");
  const filterToggle = document.getElementById("services-filter-toggle");
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
  let filter = "all";
  let filterText = "";

  function validate(payload) {
    let displayValue = payload.displayUrl.trim();
    if (/^\d{1,5}$/.test(displayValue)) {
      const port = Number(displayValue);
      if (port < 1 || port > 65535) throw new Error("Port must be between 1 and 65535.");
      displayValue = `http://${window.location.hostname}:${port}`;
    }
    const displayUrl = safeHttpUrl(displayValue);
    const probeType = ["none", "http", "tcp"].includes(payload.probeType) ? payload.probeType : "none";
    const rawProbeUrl = probeType === "none" ? "" : payload.probeUrl.trim();
    const probeUrl = rawProbeUrl ? safeProbeUrl(rawProbeUrl) : null;
    if (!payload.name.trim()) throw new Error("Service name is required.");
    if (!displayUrl) throw new Error("Display URL must be an absolute HTTP or HTTPS URL without credentials.");
    if (rawProbeUrl && !probeUrl) throw new Error("Probe endpoint must be HTTP/HTTPS or tcp://host:port without credentials or paths.");
    if (probeType === "http" && probeUrl && !["http:", "https:"].includes(probeUrl.protocol)) throw new Error("HTTP probe type requires an HTTP or HTTPS URL.");
    if (probeType === "tcp" && probeUrl?.protocol !== "tcp:") throw new Error("TCP probe type requires tcp://host:port.");
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
      probeType: String(data.get("probeType") || "none"),
      probeUrl: String(data.get("probeUrl") || ""),
    });
  }

  function createCard() {
    const article = document.createElement("article");
    article.className = "service-card";
    article.setAttribute("role", "listitem");

    const heading = document.createElement("div");
    heading.className = "service-main";
    const link = document.createElement("a");
    link.className = "service-link";
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    const name = document.createElement("span");
    name.className = "service-name";
    link.append(name);
    heading.append(link);

    const endpoint = document.createElement("span");
    endpoint.className = "service-endpoint mono";
    endpoint.dataset.label = "Endpoint";

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

    const status = document.createElement("span");
    status.className = "service-status mono";
    status.dataset.label = "Status";
    const dot = document.createElement("span");
    dot.className = "status-dot";
    dot.setAttribute("aria-hidden", "true");
    const statusText = document.createElement("span");
    status.append(dot, statusText);
    const latency = document.createElement("span");
    latency.className = "service-latency mono";
    latency.dataset.label = "Latency";

    const checked = document.createElement("div");
    checked.className = "service-meta mono";
    checked.dataset.label = "Checked";

    article.append(heading, endpoint, status, latency, checked, trigger);
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
    endpoint.textContent = displayEndpoint(service.displayUrl);
    endpoint.title = service.displayUrl;
    endpoint.setAttribute("aria-label", `Endpoint ${displayEndpoint(service.displayUrl)}`);
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
    latency.setAttribute("aria-label", `Latency ${latency.textContent}`);
    checked.hidden = !service.lastCheckedAt;
    checked.textContent = service.lastCheckedAt ? timeAgo(service.lastCheckedAt) : "";
    if (service.lastCheckedAt) checked.setAttribute("aria-label", `Last checked ${timeAgo(service.lastCheckedAt)}`);
    else checked.removeAttribute("aria-label");
  }

  function summary() {
    const up = services.filter((service) => UP_STATES.has(service.status)).length;
    const down = services.filter((service) => DOWN_STATES.has(service.status) || ["degraded", "warning"].includes(service.status)).length;
    return { total: services.length, up, down, unknown: Math.max(0, services.length - up - down) };
  }

  function matchesFilter(service) {
    const haystack = [service.name, service.displayUrl, service.probeUrl, service.status].join(" ").toLowerCase();
    if (filterText && !haystack.includes(filterText)) return false;
    if (filter === "up") return UP_STATES.has(service.status);
    if (filter === "attention") return DOWN_STATES.has(service.status) || ["degraded", "warning"].includes(service.status);
    if (filter === "unknown") return !UP_STATES.has(service.status) && !DOWN_STATES.has(service.status) && !["degraded", "warning"].includes(service.status);
    return true;
  }

  function applyFilter() {
    let visible = 0;
    for (const service of services) {
      const card = cards.get(service.key);
      if (!card) continue;
      const matches = matchesFilter(service);
      card.hidden = !matches;
      if (matches) visible += 1;
    }
    filterCount.textContent = `${visible} / ${services.length} SHOWN`;
    const activeFilters = Number(filter !== "all") + Number(Boolean(filterText));
    if (filterToggle) filterToggle.textContent = activeFilters ? `FILTERS · ${activeFilters}` : "FILTERS";
    grid.hidden = services.length === 0;
    empty.hidden = services.length !== 0 && visible !== 0;
    if (services.length > 0 && visible === 0) {
      empty.querySelector("strong").textContent = "No services match this filter";
      empty.querySelector("span").textContent = "Choose All to restore the complete endpoint inventory.";
    } else {
      empty.querySelector("strong").textContent = "No services configured";
      empty.querySelector("span").textContent = "Use Add Service to register your first endpoint.";
    }
  }

  function setFilter(next) {
    filter = ["all", "attention", "up", "unknown"].includes(next) ? next : "all";
    for (const button of filterButtons) {
      const active = button.dataset.serviceFilter === filter;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    }
    applyFilter();
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
    applyFilter();
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
      try {
        await copyText(service.displayUrl);
        showCopied(copy, "COPY URL");
        copy.focus();
      } catch {
        closeMenu(false);
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
    serviceForm.elements.probeType.value = "none";
    serviceForm.elements.probeUrl.placeholder = "https://service.tailnet.ts.net/health";
    serviceTitle.textContent = "Add service";
    serviceSubmit.textContent = "ADD SERVICE";
    showServiceDialog(invoker);
  }

  function openEdit(service, invoker) {
    serviceForm.elements.id.value = service.id;
    serviceForm.elements.name.value = service.name;
    serviceForm.elements.displayUrl.value = service.displayUrl;
    const probeType = service.probeUrl.toLowerCase().startsWith("tcp:") ? "tcp" : service.probeUrl ? "http" : "none";
    serviceForm.elements.probeType.value = probeType;
    serviceForm.elements.probeUrl.value = service.probeUrl;
    serviceForm.elements.probeUrl.placeholder = probeType === "tcp" ? "tcp://redis:6379" : "https://service.tailnet.ts.net/health";
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
  serviceForm.elements.probeType.addEventListener("change", () => {
    serviceForm.elements.probeUrl.placeholder = serviceForm.elements.probeType.value === "tcp"
      ? "tcp://redis:6379"
      : "https://service.tailnet.ts.net/health";
    if (serviceForm.elements.probeType.value === "none") serviceForm.elements.probeUrl.value = "";
  });
  filterToggle?.addEventListener("click", () => {
    const expanded = filterToggle.getAttribute("aria-expanded") !== "true";
    filterToggle.setAttribute("aria-expanded", String(expanded));
    if (filterBar) filterBar.dataset.collapsed = String(!expanded);
    if (expanded) window.requestAnimationFrame(() => filterInput?.focus({ preventScroll: true }));
  });
  for (const button of filterButtons) button.addEventListener("click", () => setFilter(button.dataset.serviceFilter));
  filterInput?.addEventListener("input", () => {
    filterText = filterInput.value.trim().toLowerCase();
    applyFilter();
  });
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
    focusFilter() {
      if (filterToggle && filterBar) {
        filterToggle.setAttribute("aria-expanded", "true");
        filterBar.dataset.collapsed = "false";
      }
      filterInput?.focus({ preventScroll: true });
    },
    applyRoute({ state = "all", query = "" } = {}) {
      const nextQuery = String(query || "").slice(0, 160);
      if (filterInput) filterInput.value = nextQuery;
      filterText = nextQuery.trim().toLowerCase();
      setFilter(state);
      if (filterToggle && filterBar && (filter !== "all" || filterText)) {
        filterToggle.setAttribute("aria-expanded", "true");
        filterBar.dataset.collapsed = "false";
      }
    },
  };
}
