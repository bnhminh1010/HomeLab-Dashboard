const nowIso = () => new Date().toISOString();
const edgeMode = new URLSearchParams(window.location.search).get("edge") === "1";

const demoServices = [
  { id: "svc_immich", name: "Immich", icon: "📸", displayUrl: "https://immich.homelab.ts.net", probeUrl: "http://100.64.0.10:2283/api/server/ping", status: "up", latencyMs: 12, lastCheckedAt: nowIso() },
  { id: "svc_crw", name: "fastCRW", icon: "⚙", displayUrl: "https://crw.homelab.ts.net", probeUrl: "http://100.64.0.11:3000/health", status: "up", latencyMs: 7, lastCheckedAt: nowIso() },
  { id: "svc_hermes", name: "Hermes", icon: "🤖", displayUrl: "https://bot.homelab.ts.net", probeUrl: "", status: "unknown", latencyMs: null, lastCheckedAt: null },
  { id: "svc_glance", name: "Glance", icon: "◇", displayUrl: "https://glance.homelab.ts.net", probeUrl: "http://100.64.0.12:8082/health", status: "up", latencyMs: 18, lastCheckedAt: nowIso() },
];

const demoContainers = [
  { id: "demo-immich-server", name: "immich_server", image: "ghcr.io/immich-app/immich-server:release", state: "running", health: "healthy", uptimeSeconds: 184320, cpuUsagePercent: 2.1, cpuNormalizedPercent: 2.1, memoryUsageBytes: 471859200, memoryLimitBytes: 8589934592, ports: ["2283→0.0.0.0"], restartCount: 0 },
  { id: "demo-immich-redis", name: "immich_redis", image: "docker.io/valkey/valkey:8", state: "running", health: "healthy", uptimeSeconds: 184300, cpuUsagePercent: 0.1, cpuNormalizedPercent: 0.1, memoryUsageBytes: 33554432, memoryLimitBytes: 1073741824, ports: ["6379/tcp"], restartCount: 4 },
  { id: "demo-fastcrw", name: "crw", image: "localhost/fastcrw:latest", state: "running", health: "healthy", uptimeSeconds: 14800, cpuUsagePercent: 0.8, cpuNormalizedPercent: 0.8, memoryUsageBytes: 125829120, memoryLimitBytes: 536870912, ports: ["3000→host"], restartCount: 1 },
  { id: "demo-worker", name: "ml_worker", image: "localhost/ml-worker:latest", state: "stopped", uptimeSeconds: 0, cpuUsagePercent: 0, memoryUsageBytes: 0, memoryLimitBytes: 2147483648, ports: [], restartCount: 0 },
];

let sequence = 0;

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
      alerts: [{ id: "demo-ok", level: "info", source: "dashboard", message: "All systems operational", occurredAt: nowIso() }],
    },
  };
}

export class DemoApi {
  constructor() { this.demo = true; }

  async session() {
    return { identity: { login: "binhminh@tailnet", name: "Binh Minh" }, role: "admin", csrfToken: "demo" };
  }

  async snapshot() { return snapshot(); }

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
