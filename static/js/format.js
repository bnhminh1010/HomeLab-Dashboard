export const clamp = (value, min = 0, max = 100) => Math.min(max, Math.max(min, Number(value) || 0));

export function number(value, fallback = 0) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

export function percent(value, digits = 0) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? `${parsed.toFixed(digits)}%` : "—";
}

export function bytes(value, digits = 1) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) return "—";
  if (parsed === 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  const power = Math.min(Math.floor(Math.log(parsed) / Math.log(1024)), units.length - 1);
  const scaled = parsed / (1024 ** power);
  const precision = scaled >= 100 || power === 0 ? 0 : digits;
  return `${scaled.toFixed(precision)} ${units[power]}`;
}

export function rate(value) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return "Idle";
  return `${bytes(parsed)}/s`;
}

export function uptime(totalSeconds) {
  let remaining = Math.max(0, Math.floor(number(totalSeconds)));
  const days = Math.floor(remaining / 86400);
  remaining %= 86400;
  const hours = Math.floor(remaining / 3600);
  remaining %= 3600;
  const minutes = Math.floor(remaining / 60);
  const parts = [];
  if (days) parts.push(`${days}d`);
  if (hours || days) parts.push(`${hours}h`);
  parts.push(`${minutes}m`);
  return parts.join(" ");
}

export function timeAgo(value, now = Date.now()) {
  if (!value) return "just now";
  let timestamp = typeof value === "number" ? value : Date.parse(value);
  if (Number.isFinite(timestamp) && timestamp < 10_000_000_000) timestamp *= 1000;
  if (!Number.isFinite(timestamp)) return "just now";
  const seconds = Math.max(0, Math.round((now - timestamp) / 1000));
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

export function safeHttpUrl(value) {
  try {
    const url = new URL(String(value));
    return ["http:", "https:"].includes(url.protocol) && !url.username && !url.password ? url : null;
  } catch {
    return null;
  }
}

export function websocketUrl(path) {
  const url = new URL(path, window.location.href);
  url.protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}

export function displayEndpoint(value) {
  const url = safeHttpUrl(value);
  if (!url) return String(value || "—");
  return `${url.host}${url.pathname === "/" ? "" : url.pathname}`;
}

export function setText(element, value, animate = false) {
  if (!element) return;
  const next = String(value ?? "—");
  if (element.textContent === next) return;
  element.textContent = next;
  if (animate && element.classList.contains("mono") && !window.matchMedia?.("(prefers-reduced-motion: reduce)")?.matches) {
    element.animate?.(
      [{ opacity: 0.55, transform: "translateY(1px)" }, { opacity: 1, transform: "translateY(0)" }],
      { duration: 180, easing: "ease-out" },
    );
  }
}

export function setProgress(element, value, maximum = 100) {
  if (!element) return;
  const safeMaximum = Math.max(1, number(maximum, 100));
  const actual = clamp(value, 0, safeMaximum);
  const normalized = clamp(actual / safeMaximum * 100);
  element.style.setProperty("--progress", normalized.toFixed(2));
  element.setAttribute("aria-valuemax", safeMaximum.toFixed(1));
  element.setAttribute("aria-valuenow", actual.toFixed(1));
  element.dataset.level = normalized > 90 ? "critical" : normalized >= 80 ? "hot" : normalized >= 50 ? "warning" : "normal";
}
