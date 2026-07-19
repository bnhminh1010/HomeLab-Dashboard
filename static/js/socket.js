import { websocketUrl } from "./format.js";

const MAX_METRICS_FRAME_BYTES = 50 * 1024;

export class MetricsStream {
  constructor({ onSnapshot, onState, onError }) {
    this.onSnapshot = onSnapshot;
    this.onState = onState;
    this.onError = onError;
    this.socket = null;
    this.stopped = true;
    this.retryTimer = 0;
    this.staleTimer = 0;
    this.lastMessageAt = 0;
    this.lastSequence = -1;
  }

  start() {
    this.stop();
    this.stopped = false;
    this.staleTimer = window.setInterval(() => this.checkFreshness(), 1000);
    this.connect();
  }

  stop() {
    this.stopped = true;
    window.clearTimeout(this.retryTimer);
    window.clearInterval(this.staleTimer);
    if (this.socket) {
      this.socket.onclose = null;
      this.socket.close(1000, "dashboard closed");
      this.socket = null;
    }
  }

  connect() {
    if (this.stopped) return;
    this.onState?.("connecting");
    try {
      this.socket = new WebSocket(websocketUrl("/ws/metrics"));
    } catch (error) {
      this.onError?.(error);
      this.scheduleRetry();
      return;
    }
    this.socket.addEventListener("open", () => this.onState?.("connected"));
    this.socket.addEventListener("message", (event) => this.handleMessage(event.data));
    this.socket.addEventListener("error", () => this.onState?.("offline"));
    this.socket.addEventListener("close", () => {
      this.socket = null;
      if (!this.stopped) {
        this.onState?.("offline");
        this.scheduleRetry();
      }
    });
  }

  scheduleRetry() {
    if (this.stopped || this.retryTimer) return;
    const delay = 3000 + Math.round((Math.random() - 0.5) * 500);
    this.retryTimer = window.setTimeout(() => {
      this.retryTimer = 0;
      this.connect();
    }, delay);
  }

  async handleMessage(raw) {
    try {
      let text;
      if (typeof raw === "string") {
        if (new TextEncoder().encode(raw).byteLength > MAX_METRICS_FRAME_BYTES) throw new Error("Oversized metrics frame discarded.");
        text = raw;
      } else if (raw instanceof Blob) {
        if (raw.size > MAX_METRICS_FRAME_BYTES) throw new Error("Oversized metrics frame discarded.");
        text = await raw.text();
      } else {
        if (raw.byteLength > MAX_METRICS_FRAME_BYTES) throw new Error("Oversized metrics frame discarded.");
        text = new TextDecoder().decode(raw);
      }

      const payload = JSON.parse(text);
      if (payload?.type && payload.type !== "metrics.snapshot") return;
      const sequence = Number(payload?.seq);
      if (Number.isFinite(sequence) && sequence <= this.lastSequence) return;
      if (Number.isFinite(sequence)) this.lastSequence = sequence;
      this.lastMessageAt = Date.now();
      this.onState?.("online", { collectedAt: payload?.collectedAt });
      this.onSnapshot?.(payload);
    } catch (error) {
      this.onError?.(error);
    }
  }

  checkFreshness() {
    if (this.lastMessageAt && Date.now() - this.lastMessageAt > 10_000) this.onState?.("stale");
  }
}
