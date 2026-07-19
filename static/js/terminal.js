import { Terminal } from "../lib/xterm.mjs";
import { FitAddon } from "../lib/addon-fit.mjs";
import { clamp, websocketUrl } from "./format.js";

const MAX_OUTPUT_FRAME = 1024 * 1024;
const MAX_INPUT_CHUNK = 8 * 1024;
const HEIGHT_STORAGE_KEY = "homelab.terminal.height";
const MOBILE_WORKBENCH_QUERY = "(max-width: 899px)";
const REDUCED_MOTION_QUERY = "(prefers-reduced-motion: reduce)";

function cleanControlText(value) {
  return String(value || "").replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, "").slice(0, 300);
}

function sessionFromResponse(payload) {
  return payload?.data?.session || payload?.session || payload?.data || payload || {};
}

export function createTerminalController({ api, demo = false, toast }) {
  const demoHostAgentState = demo ? new URLSearchParams(window.location.search).get("hostAgent") : "";
  const panel = document.getElementById("terminal-panel");
  const body = document.getElementById("terminal-body");
  const host = document.getElementById("terminal");
  const resizeHandle = document.getElementById("terminal-resize");
  const toggle = document.getElementById("terminal-toggle");
  const toggleLabel = toggle.querySelector("span");
  const clear = document.getElementById("terminal-clear");
  const bell = document.getElementById("terminal-bell");
  const hostShellButton = document.getElementById("terminal-host-shell");
  const disconnectButton = document.getElementById("terminal-disconnect");
  const sessionLabel = document.getElementById("terminal-session-label");
  const hostShellDialog = document.getElementById("host-shell-dialog");
  const hostShellConfirmForm = document.getElementById("host-shell-confirm-form");
  const hostShellTarget = document.getElementById("host-shell-target");
  const hostShellReplaceWarning = document.getElementById("host-shell-replace-warning");
  const toolbar = panel.querySelector(".terminal-toolbar");
  const tools = panel.querySelector(".terminal-tools");
  const reducedMotion = window.matchMedia(REDUCED_MOTION_QUERY);
  const mobileWorkbench = window.matchMedia(MOBILE_WORKBENCH_QUERY);

  // Cold loads always leave the dashboard unobscured. Only explicit terminal
  // actions expand this workbench; collapse state is intentionally not stored.
  panel.classList.add("is-collapsed");
  panel.classList.add("terminal-state-idle");
  toggle.setAttribute("aria-expanded", "false");
  toggle.setAttribute("aria-label", "Expand terminal workbench");
  toggle.title = "Expand terminal workbench";
  toggleLabel.textContent = "EXPAND";
  toolbar.tabIndex = -1;

  function terminalButton(id, label, title) {
    const button = document.createElement("button");
    button.id = id;
    button.className = "terminal-button terminal-size-button";
    button.type = "button";
    button.textContent = label;
    button.title = title;
    return button;
  }

  const compactButton = terminalButton("terminal-size-compact", "COMPACT", "Use compact terminal height");
  const defaultButton = terminalButton("terminal-size-default", "DEFAULT", "Use default terminal height");
  const maximizeButton = terminalButton("terminal-maximize", "MAXIMIZE", "Maximize terminal workbench");
  maximizeButton.classList.add("terminal-maximize");
  maximizeButton.setAttribute("aria-pressed", "false");
  tools.insertBefore(compactButton, disconnectButton);
  tools.insertBefore(defaultButton, disconnectButton);
  tools.insertBefore(maximizeButton, toggle);

  const terminal = new Terminal({
    allowProposedApi: false,
    allowTransparency: true,
    cursorBlink: !reducedMotion.matches,
    cursorStyle: "block",
    disableStdin: true,
    drawBoldTextInBrightColors: true,
    fontFamily: '"JetBrains Mono", "Fira Code", "Cascadia Code", Consolas, monospace',
    fontSize: 13,
    lineHeight: 1.15,
    minimumContrastRatio: 4.5,
    screenReaderMode: true,
    scrollback: 5000,
    smoothScrollDuration: reducedMotion.matches ? 0 : 100,
    bellStyle: "visual",
    theme: {
      background: "#0a0e17",
      foreground: "#e2e8f0",
      cursor: "#6ee2ff",
      cursorAccent: "#0a0e17",
      selectionBackground: "rgba(110,226,255,.30)",
      black: "#0a0e17",
      red: "#ff6b64",
      green: "#35dd79",
      yellow: "#f0c04e",
      blue: "#6ee2ff",
      magenta: "#c792ea",
      cyan: "#6ee2ff",
      white: "#e2e8f0",
      brightBlack: "#64748b",
      brightRed: "#ff9b95",
      brightGreen: "#68ee9c",
      brightYellow: "#ffe29a",
      brightBlue: "#a5efff",
      brightMagenta: "#deb7f4",
      brightCyan: "#bff5ff",
      brightWhite: "#ffffff",
    },
  });
  const fitAddon = new FitAddon();
  terminal.loadAddon(fitAddon);
  terminal.open(host);

  let socket = null;
  let active = null;
  let demoExec = false;
  let demoLine = "";
  let resizeTimer = 0;
  let fitFrame = 0;
  let reconnectTimer = 0;
  let connectionVersion = 0;
  let inactiveLabel = "IDLE";
  let inactiveState = "idle";
  let inactiveMode = "";
  let sessionInvoker = null;

  function defaultHeight() {
    return clamp(window.innerHeight * 0.36, 240, 420);
  }

  function heightLimit() {
    return Math.max(120, window.innerHeight * 0.6);
  }

  function setHeight(value, persist = false) {
    const height = clamp(value, 120, heightLimit());
    document.documentElement.style.setProperty("--terminal-height", `${Math.round(height)}px`);
    resizeHandle.setAttribute("aria-valuemin", "120");
    resizeHandle.setAttribute("aria-valuemax", String(Math.round(heightLimit())));
    resizeHandle.setAttribute("aria-valuenow", String(Math.round(height)));
    if (persist) {
      try { window.localStorage.setItem(HEIGHT_STORAGE_KEY, String(Math.round(height))); } catch { /* storage may be disabled */ }
    }
    return height;
  }

  function storedHeight() {
    try {
      const value = Number(window.localStorage.getItem(HEIGHT_STORAGE_KEY));
      return Number.isFinite(value) && value > 0 ? value : defaultHeight();
    } catch {
      return defaultHeight();
    }
  }

  function fitNow() {
    if (panel.classList.contains("is-collapsed")) return;
    try { fitAddon.fit(); } catch { /* xterm may be between layout frames */ }
  }

  function fit() {
    if (fitFrame) return;
    fitFrame = window.requestAnimationFrame(() => {
      fitFrame = 0;
      fitNow();
    });
  }

  function syncWorkbenchClass() {
    panel.classList.toggle("is-mobile-workbench", mobileWorkbench.matches && !panel.classList.contains("is-collapsed"));
  }

  function setPanelState(mode, state) {
    for (const value of ["host", "logs", "exec"]) panel.classList.toggle(`terminal-mode-${value}`, mode === value);
    for (const value of ["idle", "connecting", "connected", "disconnected", "exited", "unavailable"]) {
      panel.classList.toggle(`terminal-state-${value}`, state === value);
    }
  }

  function captureInvoker(explicitInvoker = null) {
    const candidate = explicitInvoker || document.activeElement;
    sessionInvoker = candidate instanceof HTMLElement ? candidate : null;
  }

  function restoreInvoker(force = false) {
    if (!sessionInvoker?.isConnected) {
      sessionInvoker = null;
      return;
    }
    const focused = document.activeElement;
    if (force || focused === document.body || panel.contains(focused)) sessionInvoker.focus({ preventScroll: true });
    sessionInvoker = null;
  }

  function confirmHostShell() {
    if (hostShellDialog.open) return Promise.resolve(false);
    const hostname = cleanControlText(document.getElementById("header-hostname")?.textContent || "host") || "host";
    hostShellTarget.textContent = hostname === "—" ? "the homelab host" : hostname;
    hostShellReplaceWarning.hidden = !active;
    hostShellDialog.returnValue = "";
    return new Promise((resolve) => {
      let settled = false;
      const finish = (confirmed) => {
        if (settled) return;
        settled = true;
        hostShellConfirmForm.removeEventListener("click", onClick);
        hostShellDialog.removeEventListener("cancel", onCancel);
        if (hostShellDialog.open) hostShellDialog.close(confirmed ? "confirm" : "cancel");
        resolve(confirmed);
      };
      const onClick = (event) => {
        const submitter = event.target.closest('button[type="submit"]');
        if (!submitter) return;
        event.preventDefault();
        finish(submitter.value === "confirm");
      };
      const onCancel = (event) => {
        event.preventDefault();
        finish(false);
      };
      hostShellConfirmForm.addEventListener("click", onClick);
      hostShellDialog.addEventListener("cancel", onCancel);
      hostShellDialog.showModal();
    });
  }

  setHeight(storedHeight());

  function writeNotice(message, color = "36") {
    terminal.writeln(`\x1b[${color}m[homelab]\x1b[0m ${cleanControlText(message)}`);
  }

  function sendControl(message) {
    if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message));
  }

  function terminalSize() {
    return {
      cols: Math.round(clamp(terminal.cols, 20, 300)),
      rows: Math.round(clamp(terminal.rows, 5, 100)),
    };
  }

  function retryable(error) {
    const status = Number(error?.status) || 0;
    return status === 0 || status === 408 || status === 429 || status >= 500;
  }

  async function cancelPendingSession(id) {
    if (!id) return;
    try { await api.cancelTerminalSession(id); } catch { /* already claimed, expired, or unavailable */ }
  }

  function sendResize() {
    if (!active || demo) return;
    sendControl({ type: "resize", ...terminalSize() });
  }

  terminal.onResize(() => {
    window.clearTimeout(resizeTimer);
    resizeTimer = window.setTimeout(sendResize, 120);
  });

  terminal.attachCustomKeyEventHandler((event) => {
    if (event.type !== "keydown" || event.key !== "F6" || !event.ctrlKey || !event.shiftKey) return true;
    event.preventDefault();
    window.requestAnimationFrame(() => toolbar.focus({ preventScroll: true }));
    return false;
  });

  reducedMotion.addEventListener("change", ({ matches }) => {
    terminal.options.cursorBlink = !matches;
    terminal.options.smoothScrollDuration = matches ? 0 : 100;
  });
  mobileWorkbench.addEventListener("change", syncWorkbenchClass);

  terminal.onData((data) => {
    if (demoExec) {
      if (data === "\r") {
        terminal.write("\r\n");
        if (demoLine.trim() === "exit") {
          const endedMode = active?.mode || "exec";
          const endedContainer = active?.containerName || "container";
          writeNotice("Demo shell exited.", "33");
          inactiveLabel = endedMode === "host" ? "HOST · EXITED" : `EXEC · ${endedContainer} · EXITED`;
          inactiveState = "exited";
          inactiveMode = endedMode;
          demoExec = false;
          active = null;
          updateActiveState();
          restoreInvoker();
          return;
        }
        if (demoLine.trim()) terminal.writeln(`demo: command execution is simulated (${cleanControlText(demoLine.trim())})`);
        demoLine = "";
        const prompt = active?.mode === "host"
          ? "\x1b[32mbinhminh@debian-server\x1b[0m:\x1b[34m~\x1b[0m$ "
          : "\x1b[32mdemo@homelab\x1b[0m:\x1b[34m/app\x1b[0m$ ";
        terminal.write(prompt);
      } else if (data === "\x7f") {
        if (demoLine) { demoLine = demoLine.slice(0, -1); terminal.write("\b \b"); }
      } else if (!/[\x00-\x1f]/.test(data)) {
        demoLine += data;
        terminal.write(data);
      }
      return;
    }
    if (!active || active.readOnly || socket?.readyState !== WebSocket.OPEN) return;
    const encoded = new TextEncoder().encode(data);
    for (let offset = 0; offset < encoded.byteLength && socket?.readyState === WebSocket.OPEN; offset += MAX_INPUT_CHUNK) {
      socket.send(encoded.slice(offset, offset + MAX_INPUT_CHUNK));
    }
  });

  function updateActiveState() {
    disconnectButton.hidden = !active;
    terminal.options.disableStdin = !active || active.readOnly || active.connecting || !active.ready;
    if (!active) {
      sessionLabel.textContent = inactiveLabel;
      setPanelState(inactiveMode, inactiveState);
    } else if (active.connecting) {
      const scope = active.mode === "host" ? "HOST" : `${active.mode.toUpperCase()} · ${active.containerName}`;
      sessionLabel.textContent = `${scope} · ${active.connectedOnce ? "RECONNECTING" : "CONNECTING"}`;
      setPanelState(active.mode, "connecting");
    } else if (active.mode === "host") {
      const identity = active.hostUser ? `${active.hostUser}@${active.hostname || "host"}` : (active.hostname || "host");
      sessionLabel.textContent = `HOST · ${identity} · CONNECTED`;
      setPanelState(active.mode, "connected");
    } else if (active.mode === "logs") {
      sessionLabel.textContent = `LOGS · ${active.containerName} · STREAMING`;
      setPanelState(active.mode, "connected");
    } else {
      sessionLabel.textContent = `EXEC · ${active.containerName} · CONNECTED`;
      setPanelState(active.mode, "connected");
    }
  }

  function expand() {
    const wasCollapsed = panel.classList.contains("is-collapsed");
    panel.classList.remove("is-collapsed");
    toggle.setAttribute("aria-expanded", "true");
    toggle.setAttribute("aria-label", "Collapse terminal workbench");
    toggle.title = "Collapse terminal workbench";
    toggleLabel.textContent = "COLLAPSE";
    syncWorkbenchClass();
    if (wasCollapsed) fitNow();
    fit();
  }

  function setMaximized(maximized) {
    panel.classList.toggle("is-maximized", maximized);
    maximizeButton.setAttribute("aria-pressed", String(maximized));
    maximizeButton.textContent = maximized ? "RESTORE" : "MAXIMIZE";
    maximizeButton.title = maximized ? "Restore terminal height" : "Maximize terminal workbench";
    fit();
  }

  function collapsePanel(restoreFocus = true) {
    if (panel.classList.contains("is-collapsed")) return;
    setMaximized(false);
    panel.classList.add("is-collapsed");
    panel.classList.remove("is-mobile-workbench");
    toggle.setAttribute("aria-expanded", "false");
    toggle.setAttribute("aria-label", "Expand terminal workbench");
    toggle.title = "Expand terminal workbench";
    toggleLabel.textContent = "EXPAND";
    if (restoreFocus) restoreInvoker(true);
  }

  function togglePanel() {
    if (panel.classList.contains("is-collapsed")) expand();
    else collapsePanel();
  }

  async function open({ mode, containerId, containerName, invoker = null }) {
    if (!['logs', 'exec'].includes(mode)) throw new Error("Unsupported terminal mode.");
    if ((active?.mode === "exec" || active?.mode === "host") && !window.confirm(`Disconnect the current ${active.mode === "host" ? "Host Shell" : "Exec"} session?`)) return;
    disconnect(false);
    captureInvoker(invoker);
    inactiveLabel = "IDLE";
    inactiveState = "idle";
    inactiveMode = "";
    expand();
    terminal.clear();
    terminal.reset();
    terminal.options.disableStdin = true;
    writeNotice(`Opening ${mode} for ${containerName}…`);

    if (demo) {
      active = { mode, containerId, containerName, readOnly: mode === "logs", ready: true, connectedOnce: true };
      updateActiveState();
      if (mode === "logs") {
        terminal.writeln("\x1b[90m2026-07-17T09:41:02Z\x1b[0m service ready on :8080");
        terminal.writeln("\x1b[90m2026-07-17T09:41:04Z\x1b[0m health probe \x1b[32mok\x1b[0m");
        terminal.writeln("\x1b[90m2026-07-17T09:41:06Z\x1b[0m GET /api/ping 200 4ms");
      } else {
        demoExec = true;
        demoLine = "";
        terminal.write("\x1b[32mdemo@homelab\x1b[0m:\x1b[34m/app\x1b[0m$ ");
        window.requestAnimationFrame(() => terminal.focus());
      }
      return;
    }

    const version = connectionVersion;
    active = { mode, containerId, containerName, readOnly: mode === "logs", connecting: true, connectedOnce: false, ready: false };
    updateActiveState();
    try {
      await startRemoteSession(version);
    } catch (error) {
      if (version !== connectionVersion || !active) return;
      if (retryable(error)) {
        active.connecting = true;
        updateActiveState();
        writeNotice(`${error?.message || "Terminal connection failed."} Retrying in 3 seconds…`, "33");
        scheduleReconnect(version);
        return;
      }
      inactiveLabel = `${mode.toUpperCase()} · UNAVAILABLE`;
      inactiveState = "unavailable";
      inactiveMode = mode;
      active = null;
      updateActiveState();
      restoreInvoker();
      throw error;
    }
  }

  async function openHostShell() {
    if (!await confirmHostShell()) return;
    disconnect(false);
    captureInvoker();
    inactiveLabel = "IDLE";
    inactiveState = "idle";
    inactiveMode = "";
    expand();
    terminal.clear();
    terminal.reset();
    terminal.options.disableStdin = true;
    writeNotice("Opening Bash on the homelab host with its configured host-agent account…");

    if (demo) {
      if (demoHostAgentState === "offline") {
        inactiveLabel = "HOST · UNAVAILABLE";
        inactiveState = "unavailable";
        inactiveMode = "host";
        updateActiveState();
        writeNotice("Host agent unavailable. Start homelab-host-agent and try again.", "31");
        restoreInvoker();
        throw new Error("Host agent unavailable.");
      }
      active = {
        mode: "host",
        readOnly: false,
        connecting: false,
        connectedOnce: true,
        ready: true,
        hostname: "debian-server",
        hostUser: "binhminh",
      };
      demoExec = true;
      demoLine = "";
      updateActiveState();
      terminal.writeln("\x1b[36mDemo host Bash session\x1b[0m");
      terminal.write("\x1b[32mbinhminh@debian-server\x1b[0m:\x1b[34m~\x1b[0m$ ");
      window.requestAnimationFrame(() => terminal.focus());
      if (demoHostAgentState === "disconnect") {
        window.setTimeout(() => {
          if (active?.mode !== "host") return;
          demoExec = false;
          demoLine = "";
          active = null;
          inactiveLabel = "HOST · DISCONNECTED";
          inactiveState = "disconnected";
          inactiveMode = "host";
          updateActiveState();
          writeNotice("Host shell disconnected. Click HOST SHELL to start a new session.", "33");
          restoreInvoker();
        }, 150);
      }
      return;
    }

    const version = connectionVersion;
    active = { mode: "host", readOnly: false, connecting: true, connectedOnce: false, ready: false };
    updateActiveState();
    try {
      await startRemoteSession(version);
    } catch (error) {
      if (version !== connectionVersion || !active) return;
      active = null;
      inactiveLabel = "HOST · UNAVAILABLE";
      inactiveState = "unavailable";
      inactiveMode = "host";
      updateActiveState();
      writeNotice(error?.message || "Unable to open the host shell.", "31");
      restoreInvoker();
      throw error;
    }
  }

  async function startRemoteSession(version) {
    if (!active || version !== connectionVersion) return;
    active.connecting = true;
    active.ready = false;
    updateActiveState();
    const request = { mode: active.mode, containerId: active.containerId };
    const payload = request.mode === "host"
      ? await api.createHostTerminalSession(terminalSize())
      : await api.createTerminalSession({
        ...request,
        ...(request.mode === "logs" ? { tail: 200, follow: true } : {}),
        ...terminalSize(),
      });
    const session = sessionFromResponse(payload);
    if (!session.id || !session.websocketUrl) throw new Error("The server returned an invalid terminal session.");
    const sessionID = String(session.id);
    if (!active || version !== connectionVersion || active.mode !== request.mode || active.containerId !== request.containerId) {
      await cancelPendingSession(sessionID);
      return;
    }
    active.id = sessionID;
    active.readOnly = session.readOnly ?? active.mode === "logs";
    updateActiveState();
    try {
      await connectTerminal(session.websocketUrl, version);
    } catch (error) {
      if (active?.id === sessionID) active.id = "";
      await cancelPendingSession(sessionID);
      throw error;
    }
  }

  function connectTerminal(url, version) {
    return new Promise((resolve, reject) => {
      const resolvedUrl = /^wss?:\/\//i.test(url) ? url : websocketUrl(url);
      const connection = new WebSocket(resolvedUrl);
      socket = connection;
      connection.binaryType = "arraybuffer";
      let opened = false;
      let settled = false;
      const fail = (error) => {
        if (settled) return;
        settled = true;
        reject(error);
      };
      const timeout = window.setTimeout(() => {
        connection.close();
        fail(new Error("Terminal connection timed out."));
      }, 10_000);
      connection.addEventListener("open", () => {
        window.clearTimeout(timeout);
        if (!active || version !== connectionVersion) {
          fail(new Error("Terminal connection was superseded."));
          connection.close(1000, "superseded");
          return;
        }
        opened = true;
        settled = true;
        resolve();
      }, { once: true });
      connection.addEventListener("message", (event) => handleMessage(event, version, connection));
      connection.addEventListener("error", () => {
        window.clearTimeout(timeout);
        if (!opened) fail(new Error("Terminal WebSocket failed."));
      }, { once: true });
      connection.addEventListener("close", (event) => {
        window.clearTimeout(timeout);
        if (socket === connection) socket = null;
        if (!opened) {
          fail(new Error(event.reason || "Terminal WebSocket closed before connecting."));
          return;
        }
        if (version !== connectionVersion || !active) return;
        if (active.mode === "host") {
          active = null;
          inactiveLabel = "HOST · DISCONNECTED";
          inactiveState = "disconnected";
          inactiveMode = "host";
          connectionVersion += 1;
          updateActiveState();
          writeNotice(event.reason || "Host shell disconnected. Click HOST SHELL to start a new session.", "33");
          restoreInvoker();
          return;
        }
        active.id = "";
        active.connecting = true;
        active.ready = false;
        updateActiveState();
        writeNotice(event.reason || "Connection lost. Retrying in 3 seconds…", "33");
        scheduleReconnect(version);
      });
    });
  }

  function scheduleReconnect(version) {
    if (demo || reconnectTimer || !active || active.mode === "host" || version !== connectionVersion) return;
    reconnectTimer = window.setTimeout(async () => {
      reconnectTimer = 0;
      if (!active || version !== connectionVersion) return;
      writeNotice("Reconnecting terminal session…", "36");
      try {
        await startRemoteSession(version);
      } catch (error) {
        if (!active || version !== connectionVersion) return;
        if (retryable(error)) {
          writeNotice("Reconnect failed. Retrying in 3 seconds…", "33");
          scheduleReconnect(version);
          return;
        }
        writeNotice(error?.message || "Terminal reconnect was rejected.", "31");
        const endedMode = active.mode;
        const endedContainer = active.containerName || "container";
        active = null;
        inactiveLabel = `${endedMode.toUpperCase()} · ${endedContainer} · DISCONNECTED`;
        inactiveState = "disconnected";
        inactiveMode = endedMode;
        updateActiveState();
        restoreInvoker();
      }
    }, 3000);
  }

  async function handleMessage(event, version, connection) {
    if (version !== connectionVersion || socket !== connection) return;
    if (typeof event.data === "string") {
      try {
        const control = JSON.parse(event.data);
        if (control.type === "ready") {
          if (!active) return;
          active.readOnly = control.readOnly ?? active.readOnly;
          if (active.mode === "host") {
            active.hostname = cleanControlText(control.hostname || control.host || "host");
            active.hostUser = cleanControlText(control.user || control.unixUser || control.username || "");
          }
          active.connecting = false;
          active.connectedOnce = true;
          active.ready = true;
          updateActiveState();
          writeNotice("Session ready.", "32");
          sendResize();
          if (!active.readOnly) window.requestAnimationFrame(() => terminal.focus());
          return;
        }
        if (control.type === "exit") {
          const endedMode = active?.mode || "exec";
          const endedContainer = active?.containerName || "container";
          writeNotice(`Process exited with code ${Number(control.exitCode) || 0}.`, "33");
          active = null;
          inactiveLabel = endedMode === "host" ? "HOST · EXITED" : `${endedMode.toUpperCase()} · ${endedContainer} · EXITED`;
          inactiveState = "exited";
          inactiveMode = endedMode;
          connectionVersion += 1;
          if (socket === connection) socket = null;
          connection.close(1000, "process exited");
          updateActiveState();
          restoreInvoker();
        }
        if (control.type === "error") {
          const endedMode = active?.mode || "exec";
          const endedContainer = active?.containerName || "container";
          writeNotice(control.message || control.code || "Terminal error.", "31");
          active = null;
          inactiveLabel = endedMode === "host" ? "HOST · DISCONNECTED" : `${endedMode.toUpperCase()} · ${endedContainer} · DISCONNECTED`;
          inactiveState = "disconnected";
          inactiveMode = endedMode;
          connectionVersion += 1;
          if (socket === connection) socket = null;
          connection.close(1000, "terminal error");
          updateActiveState();
          restoreInvoker();
        }
      } catch {
        writeNotice("Invalid terminal control frame discarded.", "31");
      }
      return;
    }
    const data = event.data instanceof Blob ? new Uint8Array(await event.data.arrayBuffer()) : new Uint8Array(event.data);
    if (version !== connectionVersion || socket !== connection) return;
    if (data.byteLength > MAX_OUTPUT_FRAME) {
      writeNotice("Oversized terminal output frame discarded.", "31");
      return;
    }
    terminal.write(data);
  }

  function disconnect(showNotice = true, restoreFocus = showNotice) {
    const pendingSessionID = active?.connecting ? active.id : "";
    connectionVersion += 1;
    window.clearTimeout(reconnectTimer);
    reconnectTimer = 0;
    demoExec = false;
    demoLine = "";
    const connection = socket;
    socket = null;
    if (connection) {
      if (connection.readyState === WebSocket.OPEN) connection.send(JSON.stringify({ type: "close" }));
      connection.close(1000, "user disconnected");
    }
    if (active && showNotice) writeNotice("Session closed.", "33");
    active = null;
    inactiveLabel = "IDLE";
    inactiveState = "idle";
    inactiveMode = "";
    updateActiveState();
    if (restoreFocus) restoreInvoker();
    else sessionInvoker = null;
    if (!demo && pendingSessionID) void cancelPendingSession(pendingSessionID);
  }

  toggle.addEventListener("click", togglePanel);
  clear.addEventListener("click", () => terminal.clear());
  bell.addEventListener("click", () => terminal.write("\x07"));
  compactButton.addEventListener("click", () => {
    setMaximized(false);
    setHeight(180, true);
    expand();
  });
  defaultButton.addEventListener("click", () => {
    setMaximized(false);
    setHeight(defaultHeight(), true);
    expand();
  });
  maximizeButton.addEventListener("click", () => {
    expand();
    setMaximized(!panel.classList.contains("is-maximized"));
  });
  hostShellButton.addEventListener("click", () => {
    void openHostShell().catch((error) => toast(error?.message || "Unable to open the host shell.", "error"));
  });
  disconnectButton.addEventListener("click", () => disconnect());

  resizeHandle.addEventListener("pointerdown", (event) => {
    const startY = event.clientY;
    const startHeight = body.getBoundingClientRect().height;
    setMaximized(false);
    resizeHandle.setPointerCapture(event.pointerId);
    const move = (moveEvent) => {
      setHeight(startHeight + startY - moveEvent.clientY);
      fit();
    };
    const end = () => {
      resizeHandle.removeEventListener("pointermove", move);
      resizeHandle.removeEventListener("pointerup", end);
      resizeHandle.removeEventListener("pointercancel", end);
      setHeight(body.getBoundingClientRect().height, true);
      fit();
    };
    resizeHandle.addEventListener("pointermove", move);
    resizeHandle.addEventListener("pointerup", end);
    resizeHandle.addEventListener("pointercancel", end);
  });

  resizeHandle.addEventListener("keydown", (event) => {
    if (!['ArrowUp', 'ArrowDown'].includes(event.key)) return;
    event.preventDefault();
    setMaximized(false);
    const current = body.getBoundingClientRect().height;
    setHeight(current + (event.key === "ArrowUp" ? 16 : -16), true);
    fit();
  });

  new ResizeObserver(() => fit()).observe(body);
  requestAnimationFrame(() => {
    syncWorkbenchClass();
    fit();
    terminal.writeln("\x1b[36mHomelab terminal\x1b[0m");
    terminal.writeln("Select container \x1b[1mLogs\x1b[0m / \x1b[1mExec\x1b[0m, or open an authorized \x1b[1mHost Shell\x1b[0m.");
  });

  function setHostShellCapability(enabled) {
    hostShellButton.hidden = !enabled;
    hostShellButton.disabled = !enabled;
    if (!enabled && active?.mode === "host") disconnect(false);
  }

  return { open, disconnect, fit, setHostShellCapability };
}
