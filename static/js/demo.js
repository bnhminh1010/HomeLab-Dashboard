const nowIso = () => new Date().toISOString();
const edgeMode = new URLSearchParams(window.location.search).get("edge") === "1";
const edgeContainerID = "04fe5d1ce9fc995c2f071051aaff95fb94a0fcdc39cfc54cc5cea05d5678edfc";
const edgeUnknownContainerID = "8d439abf2f349e6c2d35f0df8c139738571c3ba6d7eefcb732c8ee86f67b0a91";

const demoServices = [
  { id: "svc_immich", name: "Immich", displayUrl: "https://immich.homelab.ts.net", probeUrl: "http://100.64.0.10:2283/api/server/ping", status: "up", latencyMs: 12, lastCheckedAt: nowIso() },
  { id: "svc_crw", name: "fastCRW", displayUrl: "https://crw.homelab.ts.net", probeUrl: "http://100.64.0.11:3000/health", status: "up", latencyMs: 7, lastCheckedAt: nowIso() },
  { id: "svc_hermes", name: "Hermes", displayUrl: "https://bot.homelab.ts.net", probeUrl: "", status: "unknown", latencyMs: null, lastCheckedAt: null },
  { id: "svc_glance", name: "Glance", displayUrl: "https://glance.homelab.ts.net", probeUrl: "http://100.64.0.12:8082/health", status: "up", latencyMs: 18, lastCheckedAt: nowIso() },
];

const demoContainers = [
  { id: edgeMode ? edgeContainerID : "demo-immich-server", name: "immich_server", image: "ghcr.io/immich-app/immich-server:release", state: "running", health: "healthy", uptimeSeconds: 184320, cpuUsagePercent: 2.1, cpuNormalizedPercent: 2.1, memoryUsageBytes: 471859200, memoryLimitBytes: 8589934592, ports: ["2283→0.0.0.0"], restartCount: 0 },
  { id: "demo-immich-redis", name: "immich_redis", image: "docker.io/valkey/valkey:8", state: "running", health: "healthy", uptimeSeconds: 184300, cpuUsagePercent: 0.1, cpuNormalizedPercent: 0.1, memoryUsageBytes: 33554432, memoryLimitBytes: 1073741824, ports: ["6379/tcp"], restartCount: 4 },
  { id: "demo-fastcrw", name: "crw", image: "localhost/fastcrw:latest", state: "running", health: "healthy", uptimeSeconds: 14800, cpuUsagePercent: 0.8, cpuNormalizedPercent: 0.8, memoryUsageBytes: 125829120, memoryLimitBytes: 536870912, ports: ["3000→host"], restartCount: 1 },
  { id: "demo-worker", name: "ml_worker", image: "localhost/ml-worker:latest", state: "stopped", uptimeSeconds: 0, cpuUsagePercent: 0, memoryUsageBytes: 0, memoryLimitBytes: 2147483648, ports: [], restartCount: 0 },
  ...(edgeMode ? [{ id: "demo-history-long-resource", name: "transcoding_machine_learning_worker_for_archived_photos_and_videos", image: "localhost/history-fixture:latest", state: "running", health: "healthy", uptimeSeconds: 3200, cpuUsagePercent: 1.2, memoryUsageBytes: 104857600, memoryLimitBytes: 1073741824, ports: [], restartCount: 0 }] : []),
];

let sequence = 0;

function demoAlerts() {
  if (!edgeMode) return [{ id: "demo-ok", level: "info", source: "dashboard", message: "All systems operational", occurredAt: nowIso() }];
  const occurredAt = nowIso();
  const alerts = [
    { id: "edge-matched-container", level: "critical", source: `local/container/${edgeContainerID}`, message: "Container restart loop detected is firing (value 292.00)", occurredAt },
    { id: "edge-unknown-container", level: "warning", source: `local/container/${edgeUnknownContainerID}`, message: "Container is unhealthy is firing (value 0.00)", occurredAt },
  ];
  for (let index = 0; index < 48; index += 1) {
    alerts.push({
      id: `edge-alert-${index}`,
      level: index % 3 === 0 ? "critical" : "warning",
      source: `local/container/${index % 2 ? edgeContainerID : edgeUnknownContainerID}`,
      message: `Synthetic alert ${index + 1}: sustained metric threshold exceeded during regression coverage.`,
      occurredAt,
    });
  }
  return alerts;
}

function snapshot() {
  const wave = sequence / 5;
  const cpu = edgeMode ? 160 : 25 + Math.sin(wave) * 13 + Math.random() * 3;
  const ramUsed = edgeMode ? 18_253_611_008 : 6_657_867_776 + Math.sin(wave / 2) * 120_000_000;
  sequence += 1;
  return {
    version: 1,
    type: "metrics.snapshot",
    seq: sequence,
    collectedAt: nowIso(),
    truncated: edgeMode,
    truncatedSources: edgeMode ? ["containers", "alerts"] : undefined,
    data: {
      system: {
        hostname: "debian-server",
        os: "Debian GNU/Linux 13",
        kernel: "Linux 6.12.95",
        uptimeSeconds: 1_230_840 + sequence * 2,
        processCount: 243,
        loadAverages: [0.82, 0.56, 0.31],
        cpu: { usagePercent: cpu, cores: 8, frequencyMHz: 2200, temperatureCelsius: edgeMode ? 86 : 46 },
        memory: { totalBytes: 17_179_869_184, usedBytes: ramUsed, availableBytes: 10_522_001_408, swapTotalBytes: 2_147_483_648, swapUsedBytes: 134_217_728, zramUsedBytes: 67_108_864 },
      },
      disks: [{ mountPoint: "/", device: "/dev/nvme0n1p2", totalBytes: 493_921_239_040, usedBytes: edgeMode ? 459_346_752_307 : 404_800_143_360, usagePercent: edgeMode ? 93 : 82, readBytesPerSecond: 13_002_342 + Math.random() * 500_000, writeBytesPerSecond: 3_302_342 + Math.random() * 300_000 }],
      network: { interface: "tailscale0", rxBytesPerSecond: 12_288 + Math.random() * 6000, txBytesPerSecond: 3420 + Math.random() * 1500 },
      services: demoServices.map((service) => ({ ...service, lastCheckedAt: service.probeUrl ? nowIso() : null })),
      containers: demoContainers.map((container, index) => ({ ...container, cpuUsagePercent: Math.max(0, container.cpuUsagePercent + Math.sin(wave + index) * 0.2) })),
      alerts: demoAlerts(),
    },
  };
}

export class DemoApi {
  constructor() { this.demo = true; }

  async session() {
    return {
      identity: { login: "binhminh@tailnet", name: "Binh Minh" },
      role: "admin",
      csrfToken: "demo",
      capabilities: { manageServices: true, containerExec: true, hostShell: true },
    };
  }

  async snapshot() { return snapshot(); }

  async logsStatus() {
    return { enabled: true, backend: "loki", nodeId: "local", retentionHours: 168 };
  }

  async queryLogs({ service = "", container = "", level = "", q = "", regex = false } = {}) {
    const entries = [
      { timestamp: new Date(Date.now() - 18_000).toISOString(), labels: { job: "podman", node: "local", service_name: "Immich", container_name: "immich_server" }, line: '{"level":"info","msg":"health check completed","request_id":"demo-18"}' },
      { timestamp: new Date(Date.now() - 47_000).toISOString(), labels: { job: "podman", node: "local", service_name: "fastCRW", container_name: "crw" }, line: '{"level":"warn","msg":"upstream response exceeded target","latency_ms":842}' },
      { timestamp: new Date(Date.now() - 92_000).toISOString(), labels: { job: "podman", node: "local", service_name: "Immich", container_name: "immich_redis" }, line: 'Ready to accept connections' },
    ];
    const needle = q.trim().toLowerCase();
    let matcher = null;
    if (regex && q.trim()) {
      try {
        matcher = new RegExp(q.trim(), "i");
      } catch (error) {
        error.code = "invalid_logs_query";
        throw error;
      }
    }
    return { entries: entries.filter((entry) => {
      if (service && entry.labels.service_name !== service) return false;
      if (container && entry.labels.container_name !== container) return false;
      if (level && !entry.line.toLowerCase().includes(`\"level\":\"${level.toLowerCase()}\"`)) return false;
      return !needle || (matcher ? matcher.test(entry.line) : entry.line.toLowerCase().includes(needle));
    }) };
  }

  async listOperationalEvents({ node = "local", limit = 100 } = {}) {
    const events = [
      { id: 3, type: "container.restarted", source: "automatic", visibility: "normal", title: "Immich Redis restarted", summary: "Podman reported a successful restart", nodeId: node, containerId: "demo-immich-redis", actor: "binhminh@tailnet", occurredAt: new Date(Date.now() - 18 * 60_000).toISOString() },
      { id: 2, type: "deploy", source: "manual", visibility: "normal", title: "fastCRW image updated", summary: "Rolled forward to the latest local image", nodeId: node, serviceId: "svc_crw", actor: "binhminh@tailnet", occurredAt: new Date(Date.now() - 3 * 60 * 60_000).toISOString() },
      { id: 1, type: "backup.reported", source: "automatic", visibility: "normal", title: "Nightly backup completed", summary: "Snapshot retention check passed", nodeId: node, occurredAt: new Date(Date.now() - 9 * 60 * 60_000).toISOString() },
    ];
    return { items: events.slice(0, Math.max(0, Math.min(Number(limit) || 100, 100))) };
  }

  async systemHistory(node, range) {
    const count = 60;
    const span = 24 * 60 * 60 * 1000;
    const points = Array.from({ length: count }, (_, index) => {
      const angle = index / 6;
      const memoryPercent = 38 + Math.sin(index / 11) * 5;
      const diskPercent = edgeMode ? 93 : 82;
      return {
        at: new Date(Date.now() - span + index * span / (count - 1)).toISOString(),
        cpuPercent: edgeMode ? 74 + Math.sin(angle) * 18 : 24 + Math.sin(angle) * 12,
        memoryUsedBytes: memoryPercent / 100 * 17_179_869_184,
        memoryTotalBytes: 17_179_869_184,
        diskUsedBytes: diskPercent / 100 * 493_921_239_040,
        diskTotalBytes: 493_921_239_040,
      };
    });
    return { resolution: "raw", sourcePointCount: points.length, points, node: node || "local", range: range || "24h" };
  }

  async createService(service) {
    const created = { ...service, id: `demo-${crypto.randomUUID()}`, status: service.probeUrl ? "up" : "unknown", lastCheckedAt: service.probeUrl ? nowIso() : null, latencyMs: service.probeUrl ? 9 : null };
    demoServices.push(created);
    return { data: { service: created } };
  }

  async updateService(id, service) {
    const index = demoServices.findIndex((item) => item.id === id);
    const updated = { ...(demoServices[index] || {}), ...service, id, status: service.probeUrl ? "up" : "unknown", lastCheckedAt: service.probeUrl ? nowIso() : null };
    if (index >= 0) demoServices[index] = updated;
    return { data: { service: updated } };
  }

  async deleteService(id) {
    const index = demoServices.findIndex((item) => item.id === id);
    if (index >= 0) demoServices.splice(index, 1);
    return null;
  }

  async cancelTerminalSession() { return null; }
}

export function startDemoFeed(onSnapshot) {
  onSnapshot(snapshot());
  const timer = window.setInterval(() => onSnapshot(snapshot()), 2000);
  return () => window.clearInterval(timer);
}
