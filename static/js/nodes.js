const MAX_TOTAL_NODES = 5;
const MAX_REMOTE_NODES = MAX_TOTAL_NODES - 1;

function demoNodes() {
  return [
    { node: { id: "node_demo", displayName: "compute-01", hostname: "compute-01" }, online: false, lastSeenAt: new Date(Date.now() - 90_000).toISOString(), lastSequence: 42, agentVersion: "demo" },
  ];
}

function displayName(state) {
  return state?.node?.displayName || state?.node?.hostname || state?.node?.id || "unnamed-node";
}

function timeAgo(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "never seen";
  const seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

function empty(message) {
  const item = document.createElement("div");
  item.className = "management-empty";
  item.textContent = message;
  return item;
}

export function createNodesController({ api, demo = false, toast, onSelect }) {
  const selector = document.getElementById("node-selector");
  const switcher = selector.closest(".node-switcher");
  const openButton = document.getElementById("nodes-open");
  const dialog = document.getElementById("nodes-dialog");
  const list = document.getElementById("nodes-list");
  const status = document.getElementById("nodes-status");
  const enrollButton = document.getElementById("node-enroll-create");
  const result = document.getElementById("enrollment-result");
  const token = document.getElementById("enrollment-token");
  const expiry = document.getElementById("enrollment-expiry");
  const copyButton = document.getElementById("enrollment-copy");
  let states = [];
  let admin = false;
  let selected = "local";
  try { selected = localStorage.getItem("homelab.defaultNode") || "local"; } catch { /* Storage is optional. */ }
  let opener = null;
  let refreshTimer = null;
  let refreshing = null;
  let lastSelectionFingerprint = "";

  function setStatus(message, level = "info") {
    status.textContent = message || "";
    status.dataset.level = level;
  }

  function clearSecret() {
    token.textContent = "";
    expiry.textContent = "";
    result.hidden = true;
  }

  function open() {
    opener = document.activeElement;
    clearSecret();
    if (!dialog.open) dialog.showModal();
    dialog.querySelector("[data-dialog-close]")?.focus();
    refresh(true);
  }

  function close() {
    if (dialog.open) dialog.close();
  }

  function renderSelector() {
    const available = new Set(["local"]);
    const fragment = document.createDocumentFragment();
    const local = document.createElement("option");
    local.value = "local";
    local.textContent = "LOCAL · THIS HOST";
    fragment.append(local);
    for (const state of states.slice(0, MAX_REMOTE_NODES)) {
      const id = state?.node?.id;
      if (!id) continue;
      available.add(id);
      const option = document.createElement("option");
      option.value = id;
      option.textContent = `${state.online ? "●" : "○"} ${displayName(state)}`;
      fragment.append(option);
    }
    if (!available.has(selected)) selected = "local";
    selector.replaceChildren(fragment);
    selector.value = selected;
    const selectedState = states.find((state) => state?.node?.id === selected);
    switcher.dataset.online = selected === "local" ? "true" : selectedState?.online ? "true" : "false";
    selector.title = selected === "local" ? "Local dashboard host" : `${displayName(selectedState)} · ${selectedState?.online ? "online" : "offline"}`;
  }

  function renderList() {
    list.replaceChildren();
    if (!states.length) {
      list.append(empty("No remote nodes enrolled. The local node remains available."));
    }
    for (const state of states) {
      const item = document.createElement("article");
      item.className = "management-item";
      item.dataset.status = state.online ? "online" : "offline";
      const head = document.createElement("div");
      head.className = "management-item-head";
      const title = document.createElement("strong");
      title.className = "management-item-title";
      title.textContent = displayName(state);
      title.title = state?.node?.id || "";
      const badge = document.createElement("span");
      badge.className = `badge badge-${state.online ? "up" : "down"}`;
      badge.textContent = state.online ? "ONLINE" : "OFFLINE";
      head.append(title, badge);
      const meta = document.createElement("div");
      meta.className = "management-item-meta";
      meta.textContent = `${state?.node?.hostname || "hostname unknown"} · ${state?.agentVersion || "agent version unknown"} · seen ${timeAgo(state?.lastSeenAt || state?.node?.lastSeenAt)}`;
      item.append(head, meta);
      if (admin) {
        const actions = document.createElement("div");
        actions.className = "management-actions";
        const revoke = document.createElement("button");
        revoke.type = "button";
        revoke.className = "danger";
        revoke.textContent = "REVOKE NODE";
        revoke.addEventListener("click", async () => {
          if (!window.confirm(`Revoke ${displayName(state)}? Its agent will be disconnected immediately.`)) return;
          revoke.disabled = true;
          try {
            if (!demo) await api.revokeNode(state.node.id);
            states = states.filter((entry) => entry?.node?.id !== state.node.id);
            if (selected === state.node.id) {
              selected = "local";
              notifySelection();
            }
            renderSelector();
            renderList();
            setStatus("Node revoked.");
          } catch (error) {
            setStatus(error?.message || "Unable to revoke the node.", "error");
            revoke.disabled = false;
          }
        });
        actions.append(revoke);
        item.append(actions);
      }
      list.append(item);
    }
    enrollButton.disabled = !admin || states.length >= MAX_REMOTE_NODES;
    enrollButton.title = states.length >= MAX_REMOTE_NODES ? "The five-node total limit has been reached" : "Create a one-time enrollment token";
  }

  function notifySelection() {
    try { localStorage.setItem("homelab.defaultNode", selected); } catch { /* Storage is optional. */ }
    const state = states.find((entry) => entry?.node?.id === selected) || null;
    switcher.dataset.online = selected === "local" ? "true" : state?.online ? "true" : "false";
    const fingerprint = selected === "local"
      ? "local"
      : [selected, state?.online, state?.lastSequence, state?.snapshot?.seq, state?.snapshot?.collectedAt].join(":");
    if (fingerprint === lastSelectionFingerprint) return;
    lastSelectionFingerprint = fingerprint;
    onSelect?.({ id: selected, state });
  }

  async function refresh(updateDialog = false) {
    if (refreshing) return refreshing;
    refreshing = (async () => {
      try {
        states = demo || typeof api.listNodes !== "function" ? demoNodes() : (await api.listNodes());
        if (!Array.isArray(states)) states = states?.nodes || states?.data || [];
        states = states.slice(0, MAX_REMOTE_NODES);
        renderSelector();
        if (updateDialog || dialog.open) renderList();
        notifySelection();
        const total = states.length + 1;
        setStatus(total >= MAX_TOTAL_NODES ? "Node limit reached (5 / 5 total)." : `${total} / ${MAX_TOTAL_NODES} total nodes (${states.length} remote).`);
      } catch (error) {
        switcher.dataset.online = selected === "local" ? "true" : "unknown";
        if (updateDialog || dialog.open) {
          list.replaceChildren(empty("Node inventory is unavailable."));
          setStatus(error?.message || "Unable to load monitoring nodes.", "error");
        }
      } finally {
        refreshing = null;
      }
    })();
    return refreshing;
  }

  selector.addEventListener("change", () => {
    selected = selector.value || "local";
    notifySelection();
  });
  openButton.addEventListener("click", open);
  dialog.querySelectorAll("[data-dialog-close]").forEach((button) => button.addEventListener("click", close));
  dialog.addEventListener("close", () => {
    clearSecret();
    if (opener?.isConnected) opener.focus();
    opener = null;
  });
  enrollButton.addEventListener("click", async () => {
    if (!admin || states.length >= MAX_REMOTE_NODES) return;
    enrollButton.disabled = true;
    clearSecret();
    setStatus("Creating a one-time enrollment token…");
    try {
      const enrollment = demo
        ? { token: "enroll_demo_token_shown_once", expiresAt: new Date(Date.now() + 600_000).toISOString() }
        : await api.createNodeEnrollment();
      token.textContent = enrollment?.token || "";
      expiry.textContent = `EXPIRES · ${new Date(enrollment?.expiresAt).toLocaleString()}`;
      result.hidden = false;
      result.scrollIntoView({ block: "nearest" });
      setStatus("Token created. It will not be shown again after this dialog closes.");
    } catch (error) {
      setStatus(error?.message || "Unable to create an enrollment token.", "error");
    } finally {
      enrollButton.disabled = states.length >= MAX_REMOTE_NODES || !result.hidden;
    }
  });
  copyButton.addEventListener("click", async () => {
    if (!token.textContent) return;
    try {
      await navigator.clipboard.writeText(token.textContent);
      toast("Enrollment token copied.");
    } catch {
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(token);
      selection.removeAllRanges();
      selection.addRange(range);
      toast("Clipboard unavailable; token selected for manual copy.");
    }
  });

  refreshTimer = window.setInterval(() => refresh(false), 10_000);

  return {
    setAdmin(value) { admin = Boolean(value); if (dialog.open) renderList(); },
    selectedNode: () => selected,
    setSelected(nextNode) {
      selected = nextNode || "local";
      renderSelector();
      notifySelection();
    },
    refresh,
    destroy() { window.clearInterval(refreshTimer); clearSecret(); },
  };
}
