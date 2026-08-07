# Open-Source Launch: Storefront Finalization + GTM Execution

> **Goal:** Đưa `homelab-dashboard` (HostDeck) từ "repo cá nhân 3★" đến "repo sẵn sàng đón contributor" — theo `Docs/opensource-growth-strategy.md` + `Docs/marketing-gtm-strategy.md`, quét lại product 2026-08-07, fix nốt các lỗ hổng storefront rồi mới launch.

## Audit 2026-08-07 — Kết quả quét toàn diện

### ✅ Đã chuẩn (không đụng)
- Landing: HostDeck, 3 trang, hamburger popup, copy button, quickstart lệnh thật (đối chiếu `docs/operations.md`)
- Custom domain `hostdeck.thinkai.id.vn` + CNAME trong `site/` (bền qua deploy)
- `?demo=1` **PASS checklist skill**: `demo.js` pure mock (0 fetch/WebSocket), `app.js` dòng 19 `demo ? new DemoApi() : new DashboardApi()`, không UI module nào gọi `fetch()` trực tiếp
- Identity: **sạch** — 0 match `binhminh|debian-server|100.120` trong README + site/ + static/
- Repo storefront files: LICENSE (Apache-2.0) ✅, CONTRIBUTING.md ✅, SECURITY.md ✅, CHANGELOG.md ✅
- Screenshots: 10 PNG + demo.gif (411KB) ✅
- Topics: 9 tags (`container, dashboard, devops, go, homelab, podman, self-hosted, slo, tailscale`) ✅

### ❌ Lỗ hổng phát hiện (phải fix trước launch)

| # | Lỗ hổng | Bằng chứng | Mức độ |
|---|---|---|---|
| **G1** | **README + docs vẫn "HomeLab Dashboard"** — rebrand HostDeck chưa phủ hết | README dòng 1 title, 22 alt, 35 alt; `docs/comparison.md` 4 chỗ, `migration.md` 1, `operations.md` 1 | **P0** — visitor GitHub thấy tên cũ lệch với landing |
| **G2** | **GitHub description chưa rebrand + homepage trống** | API: description `"HomeLab DevOps Dashboard — Real-time monitoring..."`, homepage `""` | **P0** — hiện trong search + SEO |
| **G3** | **README demo link trỏ Pages cũ** | dòng 20: `bnhminh1010.github.io/HomeLab-Dashboard/` — giờ có custom domain | P1 |
| **G4** | **Số liệu Wedge 2 sai benchmark** | GTM strategy nói "30MB RAM" + "6 containers 2GB" — benchmark thật: dashboard 40.9MB + agent 8.6MB ≈ 50MB RSS; "6 containers 2GB" không nguồn | P1 — anti-slop, dùng số đo được |
| **G5** | **GitHub Discussions chưa bật** | API: `has_discussions: False` — strategy yêu cầu community hub | P1 |
| **G6** | **Demo GIF chưa verify IP leak** | `site/screenshots/demo.gif` (411KB) — chưa extract frame kiểm tra theo 4f skill (đã verify Task 2.1: 3 frame sạch IP) | ✅ xong |
| **G7** | Landing chưa link thẳng install (chỉ copy button) | Quickstart có nút Copy nhưng hero/CTA không có `curl | bash` inline | P2 |

## Plan

### Phase 1 — Storefront finalization (P0, làm ngay)

**Task 1.1 — Rebrand README → HostDeck (Level 1.5)**
- Đổi title `HomeLab Dashboard` → `HostDeck` (giữ nguyên description "Tailscale-only operations console…")
- Đổi 2 alt text demo/screenshot → HostDeck
- **KHÔNG đổi**: repo slug, service names, config keys, 30 link repo (quy tắc rename cũ)
- Verify: `grep -c "HomeLab Dashboard" README.md` = 0; rendered-browser check title

**Task 1.2 — Rebrand docs/ → HostDeck**
- `docs/comparison.md` (title + 3 chỗ cột comparison), `migration.md` (1), `operations.md` (1)
- Cột comparison giữ nguyên cấu trúc — chỉ đổi display name
- Verify: `grep -rn "HomeLab Dashboard" docs/` = 0 (trừ URL/repo slug hợp lệ)

**Task 1.3 — GitHub repo settings (qua API, cần token)**
- Description → `HostDeck — Tailscale-only operations console for 1–5 rootless Podman nodes: observe, act, retain. Go, zero inbound ports.`
- Homepage → `https://hostdeck.thinkai.id.vn`
- Bật Discussions (PATCH `has_discussions: true`)
- Verify: GET repo → description/homepage/has_discussions mới

**Task 1.4 — README demo link + quickstart**
- Dòng 20 demo link → `https://hostdeck.thinkai.id.vn/app/` (hoặc giữ Pages URL nếu muốn — quyết định: custom domain là chuẩn)
- Kiểm tra quickstart section README đã có `curl -fsSL … install.sh | bash` (dòng 169 có) ✅ — không đụng

### Phase 2 — Launch assets (P1)

**Task 2.1 — Verify + fix demo.gif**
- Extract frame 30 bằng ffmpeg → so brightness với `services.png` masked (4f skill)
- Nếu lộ IP: re-record theo `scripts/record-demo-gif.js` (mask mỗi workspace)
- Verify: frame extract không có `100.x.x.x`

**Task 2.2 — GTM copy đúng số liệu**
- Cập nhật `Docs/marketing-gtm-strategy.md` Wedge 2 → "≈50MB RSS" (hoặc "40.9MB dashboard" theo benchmark)
- Bỏ/gắn nguồn "6 containers 2GB" (không có trong docs thật — đánh dấu cần kiểm chứng hoặc bỏ)
- Verify: không còn số liệu vô nguồn

**Task 2.3 — good first issues ×3-5**
- Dựa `Docs/market-gaps-research.md` (P1 items: restart/stop buttons — đã có?, client-side filter, webhook + HMAC — đã có?)
- Tạo issues với label `good first issue` + mô tả file cần đụng
- Verify: `GET /issues?labels=good first issue` ≥3 open

### Phase 3 — Launch (sau Phase 1+2, ưu tiên theo skill reality check)

**Task 3.1 — Đăng ký selfh.st** (ưu tiên #1 theo skill — 30k readers, judge trên polish)
- Điều kiện: README+screenshots+site live (đã đủ sau Phase 1)
- Submit qua `selfh.st/submit/` với description HostDeck + 3 wedge

**Task 3.2 — r/selfhosted + r/homelab**
- "I built HostDeck — Tailscale-only ops console, zero inbound ports" + screenshot + setup story
- KHÔNG bash competitor (skill pitfall #1), nhấn 3 wedge

**Task 3.3 — Show HN** (bonus channel, KHÔNG phải engine — Beszel 24k★ từ blog/YouTube, HN launch 3pt)
- "Show HN: HostDeck — zero-dependency Tailscale-only homelab console in Go"
- Wed morning PT nếu có thể

**Task 3.4 — awesome-selfhosted PR** (sau khi có stable release)
- Điều kiện tiên quyết: LICENSE ✅ (có), steady commits ✅, **cần release tag stable + không dead link**
- PR `software/hostdeck.yml` (SPDX: Apache-2.0)

## Constraints

- Tiếng Việt khi báo cáo; commit chuẩn (context + constraint + confidence)
- **Identity ẩn tuyệt đối**: 0 match `binhminh|Binh Minh|debian-server|100.120` trong public (README, docs, site, static)
- Anti-slop: số liệu phải từ `docs/benchmarks.md` thật, không phóng đại
- Rename Level 1.5: không đổi repo slug/service/config keys/URL repo
- KHÔNG đụng batch DNS chưa commit của user (static/js/services.js, cmd/, internal/)
- GitHub API qua token từ git remote (gh CLI không cài)

## Verify criteria (mỗi task)

1. Task 1.x: grep sạch tên cũ + rendered browser check + API GET xác nhận
2. Task 2.x: GIF frame clean, số liệu khớp benchmark, issues count ≥3
3. Task 3.x: submissions xong, PR/links còn sống sau 24h
