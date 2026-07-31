# Dashboard Production Research — So sánh & Gap Analysis

> Graphite Workbench (BinhMinh Homelab) vs. Grafana / Datadog / Honeycomb / Netflix Vizceral / Google SRE
> Ngày: 2026-07-21

> [!NOTE]
> Research cũ, viết trước khi một số gap được triển khai. Hiện tại code đã có
> SLO/error budget (`internal/slo`), change timeline (`internal/monitoring`),
> TLS expiry (`internal/services/tls.go`) và backup freshness
> (`internal/healthchecks`). Mục 2.3, 2.4, 2.6, 2.7 và các hàng tương ứng trong
> bảng gap analysis không còn phản ánh trạng thái hiện tại.

---

## 1. Dashboard hiện tại — Graphite Workbench

### Workspaces

| Workspace | Tính năng |
|---|---|
| **Overview** | Health strip (Overall Health, Metrics Stream, Services, Containers), Needs Attention queue (incident triage), Service Pulse (probed endpoints), System card (CPU/RAM/DISK/NETWORK/DISK I/O/UPTIME/PROCS/LOAD), Resource Trend chart (24h, Chart.js) |
| **Services** | Service cards với health probe (up/down/degraded/unknown), ADD/EDIT/DELETE, display URL open |
| **Containers** | Podman container inventory (running/stopped/crashed/unhealthy), exec/logs actions |
| **History** | Multi-range charts (1h/6h/24h/7d/30d/90d), System/Container/Service views, 90-day retention |
| **Alerts** | Alert rules CRUD (metric threshold, resource selector, severity, for/cooldown), active alerts, recent events, ntfy push delivery |
| **Terminal** | xterm.js Host Shell, container exec (admin), container logs (search/download/follow) |
| **Multi-node** | Outbound WSS agents (4 remote + 1 local), node selector, enrollment tokens |
| **Auth** | Tailscale identity (admin/viewer), session renewal |
| **Settings** | Export/import JSON config (merge/replace), preferences persistence |

### Stack

- **Backend:** Go + Gin HTTP, Gorilla WebSocket, SQLite (local DB), JSON snapshot protocol
- **Frontend:** Vanilla JS ES modules, Chart.js, xterm.js, CSS custom properties
- **Deploy:** Podman container, Tailscale serve, host-agent + node-agent systemd units
- **Design:** Graphite layers, amber accent, dark theme, monospace metrics, workspace rail nav

---

## 2. Research — Production Dashboards Có Gì Đặc Biệt

### Source tham khảo

- **Grafana** — https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/best-practices/
- **Datadog** — https://docs.datadoghq.com/dashboards/ (Topology Map, Service Map, Host Map)
- **Netflix Vizceral** — WebGL traffic flow visualization (https://netflixtechblog.com/vizceral-open-source-acc0c32113fe)
- **Honeycomb** — BubbleUp anomaly detection, heatmaps, triggers
- **Google SRE** — Four Golden Signals, SLO/Error Budget, USE/RED methods
- **Lightstep/Chronosphere** — Service health timelines, change-related incident correlation

### 2.1. Topology / Service Dependency Map

**Nguồn:** Datadog Topology Map widget, Netflix Vizceral (WebGL), Lightstep Service Map

Datadog vẽ directed graph: services là nodes, connections là edges. Màu thể hiện health, độ dày edge thể hiện traffic volume. Netflix Vizceral dùng Three.js/WebGL cho animated traffic flow.

**Dashboard hiện tại:** Services list phẳng, không có visual dependency graph.

**Impact:** ⭐⭐⭐⭐⭐ (wow factor cao nhất)

### 2.2. Anomaly Detection / Baseline Deviation

**Nguồn:** Honeycomb BubbleUp, Datadog Watchdog, Amazon DevOps Guru

Thay vì threshold tĩnh (`cpu > 80%`), các dashboard học baseline (ví dụ: CPU luôn 20-30% vào tối thứ 7) và báo động khi metric lệch khỏi pattern mùa. Không cần người dùng cấu hình threshold thủ công.

**Dashboard hiện tại:** Chỉ threshold tĩnh trong alert rules.

**Impact:** ⭐⭐⭐⭐

### 2.3. SLO / Error Budget Tracking

**Nguồn:** Google SRE handbook, Grafana SLO plugin, Datadog SLO widget

Widget nhỏ: "Service X UP 99.5% over 30d" với progress bar, error budget remaining, burn rate alerts. Cần rolling window (7d, 30d, 90d) và target %.

**Dashboard hiện tại:** "X/Y UP" static, không rolling window SLO.

**Code complexity:** Thấp. Logic đơn giản: count successful probes / total probes per window.

**Impact:** ⭐⭐⭐⭐

### 2.4. Change Timeline / Deployment Annotations

**Nguồn:** Grafana Annotations, Datadog Event Timeline, Honeycomb Triggers

Overlay events lên time-series chart: "Deployed v2.3", "Config changed", "Container restarted". Giúp giải thích ngay metric spikes/dips.

**Dashboard hiện tại:** History chart không có event markers.

**Backend cần:** Collection mới (events/changes) + API endpoint.

**Impact:** ⭐⭐⭐⭐

### 2.5. Multi-node Comparison / Heatmap

**Nguồn:** Grafana, Datadog Host Map

Grid các node, mỗi cell là CPU%/RAM%/DISK% color-coded (green→yellow→red). So sánh tất cả nodes trên một màn hình.

**Dashboard hiện tại:** Chọn 1 node tại mỗi thời điểm, không tổng quan tất cả nodes.

**Impact:** ⭐⭐⭐

### 2.6. Certificate / TLS Expiry Tracking

Common trong homelab dashboards. Service SSL cert → show ngày hết hạn, alert N ngày trước expiry.

**Dashboard hiện tại:** Không có.

**Impact:** ⭐⭐⭐

### 2.7. Backup Freshness / Data Health

Backup cuối cùng chạy lúc nào? Thành công/fail? Dung lượng? Dashboard hiện tại không có.

**Impact:** ⭐⭐⭐

### 2.8. GPU Monitoring (Radeon 780M)

Dashboard monitor CPU/RAM/DISK nhưng không có GPU metrics: utilization, VRAM, temperature, encoder load.

**Dashboard hiện tại:** Không có.

**Impact:** ⭐⭐

### 2.9. URL Change Detection / Config Drift

Phát hiện thay đổi open ports, DNS records, TLS certs giữa các lần scan.

**Impact:** ⭐⭐

### 2.10. Micro-interactions / UI Polish

- Number transition animation (Grafana: giá trị animate từ cũ → mới)
- Toast slide-in/fade-out (hiện tại pop-in cứng)
- State transition hint (WAITING → HEALTHY → ACTION NEEDED)

**Dashboard hiện tại:** Thiếu animation polish, toast chưa mượt.

**Impact:** ⭐⭐

---

## 3. Gap Analysis — Ma trận so sánh

| Tính năng | Graphite Workbench | Grafana | Datadog | Honeycomb |
|---|---|---|---|---|
| Host metrics (CPU/RAM/DISK/NET) | ✅ Full | ✅ | ✅ | ✅ |
| Service health probes | ✅ | ✅ | ✅ | ✅ |
| Container inventory | ✅ | ✅ | ✅ | — |
| Real-time terminal exec | ✅ | ❌ | ❌ | ❌ |
| Alert rules (threshold) | ✅ | ✅ | ✅ | ✅ |
| Historical charts (90d) | ✅ | ✅ | ✅ | ✅ |
| Multi-node agents | ✅ | ✅ | ✅ | ✅ |
| **Service topology map** | **❌** | ✅ (plugin) | ✅ | ✅ |
| **Anomaly detection** | **❌** | ✅ (ML) | ✅ Watchdog | ✅ |
| **SLO / Error Budget** | **❌** | ✅ | ✅ | ✅ |
| **Change timeline overlay** | **❌** | ✅ Annotations | ✅ Events | ✅ Triggers |
| **Multi-node heatmap** | **❌** | ✅ | ✅ Host Map | ❌ |
| **Certificate expiry** | **❌** | ❌ | ❌ | ❌ |
| **GPU monitoring** | **❌** | ✅ | ✅ | ❌ |
| **Backup freshness** | **❌** | ❌ | ❌ | ❌ |
| **Number animation** | **❌** | ✅ (minimal) | ✅ | ✅ |
| **Config drift detection** | **❌** | ❌ | ❌ | ❌ |

---

## 4. Maturity Model (Grafana classification)

| Level | Đặc điểm | Graphite Workbench |
|---|---|---|
| **Low** — default state | Dashboard sprawl, no version control, no strategy | ❌ |
| **Medium** — methodical | Template variables, observability strategy, drill-downs, hierarchical dashboards | ✅ **Hiện tại ở level này** |
| **High** — optimized | Active sprawl reduction, consistency by design, scripting libs, no browser editing, directed browsing | ⬆️ **Mục tiêu** |

---

## 5. Đề xuất — 5 thứ nên làm nhất (theo impact/công sức)

### #1: Topology Service Map 🥇

**Impact:** ⭐⭐⭐⭐⭐ | **Công sức:** Cao (2-3 ngày)

Vẽ Canvas/WebGL directed graph: mỗi service/container là node, connections là edges. Health color-coding. Tương tác: hover → detail popup, click → navigate.

**Backend cần:** Graph model + API trả về nodes/edges.
**Frontend:** Canvas rendering (Three.js hoặc vanilla Canvas).
**Data:** Từ service probe relationships + container network links.

### #2: SLO Tracking Panel 🥈

**Impact:** ⭐⭐⭐⭐ | **Công sức:** Thấp (0.5 ngày)

Widget: mỗi service có SLO target %, rolling window actual %, error budget bar.

**Backend:** API tính SLO từ probe history.
**Frontend:** Chart.js bar/horizontal gauge.
**Data:** Probe history đã có sẵn trong 90-day retention.

### #3: Change Timeline Annotations 🥉

**Impact:** ⭐⭐⭐⭐ | **Công sức:** Trung bình (1 ngày)

Overlay event markers lên History chart. Events: deployment, container restart, service update, config change.

**Backend cần:** `internal/events/` — model, store, API endpoint.
**Frontend:** Chart.js plugin annotation overlay.
**Auto-track:** Podman container restart, service config update, node agent connect/disconnect.

### #4: Multi-node Comparison Dashboard

**Impact:** ⭐⭐⭐ | **Công sức:** Trung bình (1 ngày)

Workspace mới "Nodes": grid cards, mỗi card là 1 node với CPU/RAM/DISK progress bars, health dot, last-seen timestamp.

**Backend:** Snapshot data pool đã có.
**Frontend:** Grid layout, color-coded health, top-N sắp xếp theo mức độ critical.

### #5: Certificate Expiry + Backup Freshness

**Impact:** ⭐⭐⭐ | **Công sức:** Thấp (0.5 ngày mỗi cái)

**Certificate:** Probe HTTPS endpoints, parse cert → NotAfter date, alert < 30 days.
**Backup:** RESTIC/Borg/Wal-G check script → report last success, age, size.

Cả hai đều có thể implement như service probes mới với custom checker.

---

## 6. Tham khảo thêm

- **Grafana dashboard best practices:** https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/best-practices/
- **The USE Method (Brendan Gregg):** http://www.brendangregg.com/usemethod.html
- **The RED Method (Tom Wilkie):** https://grafana.com/blog/2018/08/02/the-red-method-how-to-instrument-your-services
- **Google SRE — Four Golden Signals:** https://sre.google/sre-book/monitoring-distributed-systems/
- **Netflix Vizceral:** https://netflixtechblog.com/vizceral-open-source-acc0c32113fe
- **Datadog Topology Map:** https://docs.datadoghq.com/dashboards/widgets/topology_map/
- **Wikimedia Grafana runbook:** https://wikitech.wikimedia.org/wiki/Performance/Runbook/Grafana_best_practices

---

*Generated by Hermes Agent research · 2026-07-21*
