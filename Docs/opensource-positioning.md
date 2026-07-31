# Open-Source Positioning & Community Notes

> Tổng hợp từ feature survey + thị trường homelab dashboard 2025–2026.
> Mục đích: dẫn dắt cách giới thiệu project khi open-source, tránh claim sai.

---

## 1. Positioning statement

> **HomeLab Dashboard is a private, Tailscale-only operations console for 1–5
> homelab nodes. Monitor service health, backup freshness, logs and error
> budgets; then open the correct container or Bash shell without exposing an
> admin port.**

Không phải "another dashboard", không phải launcher, không phải Grafana clone.
Trọng tâm: một vòng lặp người vận hành homelab thực sự chạy — *thấy vấn đề,
hiểu vì sao, xử lý an toàn.*

## 2. Bản đồ thị trường (2025–2026)

| Nhóm | Đại diện | Họ làm gì |
|---|---|---|
| Start pages | Homepage, Homarr, Dashy, Heimdall, Glance | Aggregation link/tile + widgets, ít trạng thái vận hành thật |
| Monitoring | Beszel, Glances, Netdata | Metrics node-level sâu, không phải sản phẩm operations |
| Availability | Uptime Kuma, Gatus | Probe/alert uptime đơn thuần, không host/container/shell |
| Admin/control | Cockpit, Portainer, Komodo | Quản trị server/container truyền thống, model agent nặng |
| Web terminals | ttyd, WeTTY, GoTTY, Shell In A Box | Terminal server đứng riêng, không identity/role/context |

HomeLab Dashboard lấp khe: **console vận hành 1–5 node, tailnet-only,
low-footprint** — kết hợp observe + diagnose + controlled shell + lịch sử trong
một binary, không cần stack Prometheus/Grafana hay reverse proxy.

## 3. Điểm riêng mạnh (defendable)

1. **No inbound agent port.** Host access qua Unix-socket host agent local;
   node từ xa dial out qua Tailscale HTTPS/WSS. Không SSH, không agent listener,
   không reverse proxy phải bảo vệ. Mạnh cho CGNAT, VLAN, node xa, máy di động.
2. **Tailscale identity là auth plane.** Role viewer/admin từ Tailnet identity,
   session + CSRF + SameSite=Strict, identity headers chỉ tin khi loopback.
3. **Operate, không chỉ observe.** Container shell + host Bash login có confirm
   tường minh, gắn context với alert/log/metric đưa bạn tới đó.
4. **Bounded operational memory.** History SQLite tầng 1h–90d, quota thấy rõ,
   archived-resource picker — không cần Prometheus.
5. **Backup freshness + SLO/error budget** từ probe history, không sở hữu backup
   credentials, không cần metrics stack.
6. **Rootless Podman-first**, container read-only, cap_drop ALL,
   no-new-privileges, một container một volume.

## 4. Claim không nên dùng (weak / non-defensible)

- "Có dashboard", "có metrics", "có uptime monitoring", "có web terminal" —
  ai cũng có.
- "Works với Tailscale" — gì cũng đặt sau Tailscale Serve được.
- **"First web terminal for homelabs"** — sai; Cockpit, Portainer, ttyd đã làm.

Terminal từ dashboard không unique; cái unique là **identity-aware, tailnet-only,
context-linked terminal + container ops bên trong console observability nhẹ**.

## 5. Open-source notes

### Security là credibility test chính

Cộng đồng homelab rất nhạy với terminal trong browser (high-trust feature).
Khi giới thiệu, nêu rõ đã có:
- Target node + user identity + role hiển thị rõ trong session.
- Confirm modal trước khi mở host shell.
- Session audit record (reserve/close), idle timeout 15 phút, hard cap 1 giờ.
- Host agent chạy non-root (account sở hữu systemd user unit).
- Compose: read-only FS, tmpfs noexec, drop all caps, PID cap.
- Enroll token 1 lần, TTL 10 phút; credentials mode `0600`.

### Claims phải verify được

Từng claim trong README "Why this is different" đều có code tương ứng trong
`internal/` (SLO, monitoring timeline, tls.go, healthchecks). Không thêm claim
marketing không có implementation.

### Sẵn sàng repo

- License Apache-2.0 — có (`LICENSE`).
- `SECURITY.md`, `CONTRIBUTING.md`, `.github/` (issues, PR template, rulesets) — có.
- Image GHCR + Docker Hub public, multi-arch (amd64/arm64), attestation + SBOM — có.
- `compose.yml` production image-only, exact-tag policy — có.

### Góc cộng đồng

Cộng đồng selfhosted (r/selfhosted, r/homelab, Tailscale community) 2025–2026
chuộng:
- Truy cập private (Tailscale/WireGuard) hơn expose dashboard công khai.
- Tránh stack sprawl — không muốn chạy Prometheus + exporters + Grafana + Loki
  chỉ để xem vài server nhà.
- Low idle resource — Go/static binary, SQLite, RAM/CPU nhỏ (Pi, mini-PC, NAS).
- Backup/restore đơn giản — một volume, data path rõ, upgrade rollback được.
- Local control, không tài khoản SaaS.
- Terminal an toàn: role limit, audit, session short-lived, origin/CSRF,
  target identity hiển thị.

## 6. Khe thị trường chưa ai lấp (roadmap gợi ý)

1. Small-fleet private operations console (1–5 node) — vị trí hiện tại.
2. Đường dẫn chẩn đoán an toàn sau alert: probe fail → logs → container exec →
   host shell → change trail, tất cả trong một auth UI.
3. Backup operations thay vì backup dashboard: freshness, lịch trình, stale
   threshold, alert.
4. Remote homelab node không cần SSH exposure — outbound-only agent qua tailnet.
5. Rootless Podman-first — hầu hết tool khác ưu tiên Docker/K8s.

## 7. Tham chiếu

- README: `## Why this is different` (đã reposition).
- `Docs/dashboard-production-research.md` — bản so sánh cũ với Grafana/Datadog
  (note đã đánh dấu phần lỗi thời).
- `DESIGN.md` — contract UI: "operator workbench, not marketing site".
