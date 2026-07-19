import { Terminal } from "../lib/xterm.mjs";
import { FitAddon } from "../lib/addon-fit.mjs";
import { clamp, websocketUrl } from "./format.js";

const MAX_OUTPUT_FRAME = 1024 * 1024;
const MAX_INPUT_CHUNK = 8 * 1024;

function cleanControlText(value) {
  return String(value || "").replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, "").slice(0, 300);
}

function sessionFromResponse(payload) {
  return payload?.data?.session || payload?.session || payload?.data || payload || {};
}

export function createTerminalController({ api, demo = false, toast }) {
  const panel = document.getElementById("terminal-panel");
  const body = document.getElementById("terminal-body");
  const host = document.getElementById("terminal");
  const resizeHandle = document.getElementById("terminal-resize");
  const toggle = document.getElementById("terminal-toggle");
  const toggleLabel = toggle.querySelector("span");
  const clear = document.getElementById("terminal-clear");
  const bell = document.getElementById("terminal-bell");
  const disconnectButton = document.getElementById("terminal-disconnect");
  const sessionLabel = document.getElementById("terminal-session-label");

  const terminal = new Terminal({
    allowProposedApi: false,
    allowTransparency: true,
    cursorBlink: true,
    cursorStyle: "block",
    disableStdin: true,
    drawBoldTextInBrightColors: true,
    fontFamily: '"JetBrains Mono", "Fira Code", "Cascadia Code", Consolas, monospace',
    fontSize: 13,
    lineHeight: 1.15,
    scrollback: 5000,
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
  let reconnectTimer = 0;
  let connectionVersion = 0;

  function fit() {
    if (panel.classList.contains("is-collapsed")) return;
    try { fitAddon.fit(); } catch { /* xterm may be between layout frames */ }
  }

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
    resizeTimer = window.setTimeout(sendResize, 100);
  });

  terminal.onData((data) => {
    if (demoExec) {
      if (data === "\r") {
        terminal.write("\r\n");
        if (demoLine.trim() === "exit") {
          writeNotice("Demo shell exited.", "33");
          demoExec = false;
          active = null;
          updateActiveState();
          return;
        }
        if (demoLine.trim()) terminal.writeln(`demo: command execution is simulated (${cleanControlText(demoLine.trim())})`);
        demoLine = "";
        terminal.write("\x1b[32mdemo@homelab\x1b[0m:\x1b[34m/app\x1b[0m$ ");
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
    terminal.options.disableStdin = !active || active.readOnly || active.connecting;
    sessionLabel.textContent = active
      ? `${active.connecting ? (active.connectedOnce ? "RECONNECTING" : "CONNECTING") : active.mode.toUpperCase()} · ${active.containerName}`
      : "IDLE";
  }

  function expand() {
    if (!panel.classList.contains("is-collapsed")) return;
    panel.classList.remove("is-collapsed");
    toggle.setAttribute("aria-expanded", "true");
    toggleLabel.textContent = "COLLAPSE";
    requestAnimationFrame(() => { fit(); terminal.focus(); });
  }

  function togglePanel() {
    const collapse = !panel.classList.contains("is-collapsed");
    panel.classList.toggle("is-collapsed", collapse);
    toggle.setAttribute("aria-expanded", String(!collapse));
    toggleLabel.textContent = collapse ? "EXPAND" : "COLLAPSE";
    if (!collapse) requestAnimationFrame(fit);
  }

  async function open({ mode, containerId, containerName }) {
    if (!['logs', 'exec'].includes(mode)) throw new Error("Unsupported terminal mode.");
    if (active?.mode === "exec" && !window.confirm(`Disconnect the current Exec session for ${active.containerName}?`)) return;
    disconnect(false);
    expand();
    terminal.clear();
    terminal.reset();
    terminal.options.disableStdin = true;
    writeNotice(`Opening ${mode} for ${containerName}…`);

    if (demo) {
      active = { mode, containerId, containerName, readOnly: mode === "logs" };
      updateActiveState();
      if (mode === "logs") {
        terminal.writeln("\x1b[90m2026-07-17T09:41:02Z\x1b[0m service ready on :8080");
        terminal.writeln("\x1b[90m2026-07-17T09:41:04Z\x1b[0m health probe \x1b[32mok\x1b[0m");
        terminal.writeln("\x1b[90m2026-07-17T09:41:06Z\x1b[0m GET /api/ping 200 4ms");
      } else {
        demoExec = true;
        demoLine = "";
        terminal.write("\x1b[32mdemo@homelab\x1b[0m:\x1b[34m/app\x1b[0m$ ");
        terminal.focus();
      }
      return;
    }

    const version = connectionVersion;
    active = { mode, containerId, containerName, readOnly: mode === "logs", connecting: true, connectedOnce: false };
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
      active = null;
      updateActiveState();
      throw error;
    }
  }

  async function startRemoteSession(version) {
    if (!active || version !== connectionVersion) return;
    active.connecting = true;
    updateActiveState();
    const request = { mode: active.mode, containerId: active.containerId };
    const payload = await api.createTerminalSession({
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
        active.connecting = false;
        active.connectedOnce = true;
        updateActiveState();
        writeNotice("Session connected.", "32");
        sendResize();
        if (!active?.readOnly) terminal.focus();
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
        active.id = "";
        active.connecting = true;
        updateActiveState();
        writeNotice(event.reason || "Connection lost. Retrying in 3 seconds…", "33");
        scheduleReconnect(version);
      });
    });
  }

  function scheduleReconnect(version) {
    if (demo || reconnectTimer || !active || version !== connectionVersion) return;
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
        active = null;
        updateActiveState();
      }
    }, 3000);
  }

  async function handleMessage(event, version, connection) {
    if (version !== connectionVersion || socket !== connection) return;
    if (typeof event.data === "string") {
      try {
        const control = JSON.parse(event.data);
        if (control.type === "ready") return;
        if (control.type === "exit") {
          writeNotice(`Process exited with code ${Number(control.exitCode) || 0}.`, "33");
          active = null;
          connectionVersion += 1;
          if (socket === connection) socket = null;
          connection.close(1000, "process exited");
          updateActiveState();
        }
        if (control.type === "error") {
          writeNotice(control.message || control.code || "Terminal error.", "31");
          active = null;
          connectionVersion += 1;
          if (socket === connection) socket = null;
          connection.close(1000, "terminal error");
          updateActiveState();
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

  function disconnect(showNotice = true) {
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
    updateActiveState();
    if (!demo && pendingSessionID) void cancelPendingSession(pendingSessionID);
  }

  toggle.addEventListener("click", togglePanel);
  clear.addEventListener("click", () => terminal.clear());
  bell.addEventListener("click", () => terminal.write("\x07"));
  disconnectButton.addEventListener("click", () => disconnect());

  resizeHandle.addEventListener("pointerdown", (event) => {
    const startY = event.clientY;
    const startHeight = body.getBoundingClientRect().height;
    resizeHandle.setPointerCapture(event.pointerId);
    const move = (moveEvent) => {
      const height = clamp(startHeight + startY - moveEvent.clientY, 80, window.innerHeight * 0.6);
      document.documentElement.style.setProperty("--terminal-height", `${height}px`);
      resizeHandle.setAttribute("aria-valuenow", String(Math.round(height)));
      fit();
    };
    const end = () => {
      resizeHandle.removeEventListener("pointermove", move);
      resizeHandle.removeEventListener("pointerup", end);
      resizeHandle.removeEventListener("pointercancel", end);
      fit();
    };
    resizeHandle.addEventListener("pointermove", move);
    resizeHandle.addEventListener("pointerup", end);
    resizeHandle.addEventListener("pointercancel", end);
  });

  resizeHandle.addEventListener("keydown", (event) => {
    if (!['ArrowUp', 'ArrowDown'].includes(event.key)) return;
    event.preventDefault();
    const current = body.getBoundingClientRect().height;
    const height = clamp(current + (event.key === "ArrowUp" ? 16 : -16), 80, window.innerHeight * 0.6);
    document.documentElement.style.setProperty("--terminal-height", `${height}px`);
    resizeHandle.setAttribute("aria-valuenow", String(Math.round(height)));
    fit();
  });

  new ResizeObserver(() => fit()).observe(body);
  requestAnimationFrame(() => {
    fit();
    terminal.writeln("\x1b[36mHomelab container terminal\x1b[0m");
    terminal.writeln("Select \x1b[1mLogs\x1b[0m or \x1b[1mExec\x1b[0m on a container to start a typed session.");
  });

  return { open, disconnect, fit };
}
