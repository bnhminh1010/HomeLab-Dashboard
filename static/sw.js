const CACHE_NAME = "homelab-dashboard-shell-v3";
const SHELL = [
  "./",
  "./index.html",
  "./manifest.webmanifest",
  "./css/tokens.css",
  "./css/style.css",
  "./lib/xterm.css",
  "./lib/chart.umd.min.js",
  "./lib/xterm.mjs",
  "./lib/addon-fit.mjs",
  "./js/alerts.js",
  "./js/api.js",
  "./js/app.js",
  "./js/containers.js",
  "./js/demo.js",
  "./js/format.js",
  "./js/history.js",
  "./js/logs.js",
  "./js/metrics.js",
  "./js/nodes.js",
  "./js/operations.js",
  "./js/overview.js",
  "./js/services.js",
  "./js/settings.js",
  "./js/socket.js",
  "./js/terminal.js",
  "./icons/icon-192.svg",
  "./icons/icon-512.svg",
];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE_NAME).then((cache) => cache.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(caches.keys().then((keys) => Promise.all(keys.filter((key) => key !== CACHE_NAME).map((key) => caches.delete(key)))).then(() => self.clients.claim()));
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);
  if (request.method !== "GET" || url.origin !== self.location.origin || url.pathname.includes("/api/") || url.pathname.includes("/ws/")) return;
  if (request.mode === "navigate") {
    event.respondWith(fetch(request).catch(() => caches.match("./index.html")));
    return;
  }
  event.respondWith(caches.match(request).then((cached) => cached || fetch(request)));
});
