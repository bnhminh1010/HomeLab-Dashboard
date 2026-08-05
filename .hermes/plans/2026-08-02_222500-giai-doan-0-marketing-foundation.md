# Giai đoạn 0 — Marketing Foundation Plan

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.
>
> **TRẠNG THÁI:** Task 1 ✅ · Task 2 ✅ (đang implement) · Task 3 ⬜ · Task 4 ⬜ · Task 5 ⬜ · Task 6 ⬜ · Task 7 ⬜

**Goal:** Biến repo HomeLab-Minh từ "code 9/10, visibility 0/10" thành repo sẵn sàng launch: README có screenshot/GIF, GitHub metadata đầy đủ, website 1 trang với demo không-cần-auth, installer 1 lệnh.

**Kiến trúc:** Không đụng vào core dashboard (Go backend). Chỉ thêm: ảnh demo, GitHub metadata, static site (GitHub Pages), install script. Mọi thứ deploy miễn phí, không thêm dependency.

**Tech Stack:** Markdown, GitHub Pages (static HTML/CSS/JS — tái sử dụng `static/` hiện có), bash installer, gh CLI / GitHub API.

---

## Task 1: Chụp screenshot demo chất lượng cao

**Objective:** Có 4-6 screenshot đẹp từ demo mode để đưa vào README + website.

**Files:**
- Tạo: `assets/screenshots/overview.png`, `services.png`, `containers.png`, `history.png`, `terminal.png`, `topology.png`

**Step 1: Khởi động dashboard ở demo mode**

```bash
cd /home/binhminh/Developer/HomeLab-Minh
go build -o /tmp/hlab-demo ./cmd/dashboard
DATA_PATH=/tmp/hlab-data/dashboard.db TRUST_TAILSCALE_HEADERS=true \
  ADMIN_LOGINS=test@example.com /tmp/hlab-demo &   # listen 127.0.0.1:8082
```

**Step 2: Chạy proxy inject header (đã có sẵn pattern ở /tmp/hlab-proxy)**

Mở từng workspace qua browser (viewport 1440×900, dark mode):
1. Overview (health + trend) → `overview.png`
2. Services + SLO → `services.png`
3. Containers → `containers.png`
4. History (chart + timeline) → `history.png`
5. Terminal workbench (host shell) → `terminal.png`
6. Topology → `topology.png`

**Step 3: Chụp**

- Dùng browser tools navigate từng workspace, chụp full-page (headless Chrome `--screenshot` hoặc browser tool).
- Yêu cầu: không có thanh scroll, header đầy đủ (node switcher, DEMO badge), màu sắc đồng nhất.

**Step 4: Tối ưu ảnh**

```bash
# pngquant hoặc cwebp — mục tiêu < 500KB/ảnh
```

**Step 5: Commit**

```bash
git add assets/screenshots/
git commit -m "docs: add demo screenshots for README and site"
```

**Verify:** Mỗi ảnh mở được, hiển thị đúng workspace, không lỗi render (chart hiển thị, không JS error — check browser console trước khi chụp).

---

## Task 2: Chèn screenshot + GIF demo vào README

**Objective:** README có hero section với screenshot đầu tiên trong 10 dòng đầu, kèm liên kết ảnh còn lại.

**Files:**
- Modify: `README.md` (hero section hiện tại ~dòng 1-50)

**Step 1: Xem hero hiện tại**

```bash
head -50 README.md
```

**Step 2: Thêm screenshot block ngay sau phần giới thiệu**

```markdown
## Overview

![Dashboard overview](assets/screenshots/overview.png)

<details>
<summary>More screenshots</summary>

![Services and SLO](assets/screenshots/services.png)
![Containers](assets/screenshots/containers.png)
![History](assets/screenshots/history.png)
![Terminal](assets/screenshots/terminal.png)

</details>
```

**Step 3: Tạo GIF demo ngắn (10-15s)**

- Ghi màn hình: overview → click services → containers → terminal (loop)
- Nén GIF: mục tiêu < 2MB (gifsicle hoặc ffmpeg palette)

**Step 4: Thêm badge row (shields.io, verified)**

License Apache-2.0, Go version, CI status, Docker pulls, Release.

**Step 5: Commit**

```bash
git add README.md assets/
git commit -m "docs: add screenshots and demo GIF to README hero"
```

**Verify:** Mở README trên GitHub preview (hoặc render local) — ảnh hiển thị, không broken link, GIF chạy.

---

## Task 3: GitHub metadata (description + topics + homepage)

**Objective:** Repo xuất hiện trong GitHub search + Google với từ khóa đúng.

**Files:**
- Chỉnh qua GitHub API/gh CLI (không phải file trong repo)

**Step 1: Xác định từ khóa**

Topics (10-15):
```
monitoring, homelab, self-hosted, selfhosted, podman, tailscale, dashboard,
slo, observability, devops, containers, ops-console
```

Description (~1 câu, có keyword):
```
Tailscale-only operations console for 1-5 homelab nodes: monitor services,
SLOs, container health, logs — then open the right shell without exposing
an admin port. Go, zero-dependency.
```

**Step 2: Set qua gh CLI**

```bash
gh repo edit bnhminh1010/homelab-dashboard \
  --description "..." \
  --add-topic monitoring --add-topic homelab ... \
  --homepage "https://bnhminh1010.github.io/homelab-dashboard/"
```

**Step 3: Verify**

```bash
gh repo view bnhminh1010/homelab-dashboard --json description,topics,homepageUrl
```

**Verify:** Description + topics hiển thị trên GitHub, homepage trỏ đúng (sau Task 4).

---

## Task 4: Website 1 trang (GitHub Pages) với demo không-cần-auth

**Objective:** Landing page tĩnh: hero + demo nhúng (chạy ngay không cần đăng nhập) + quickstart + link GitHub.

**Files:**
- Tạo: `site/index.html`, `site/css/`, `site/js/` (hoặc tái sử dụng static/ qua GitHub Actions)
- Tạo: `.github/workflows/pages.yml`

**Step 1: Kiểm tra khả năng demo standalone**

Demo UI hiện dùng `DemoApi` (client-side mock) — xác nhận `static/index.html?demo=1` render đầy đủ KHÔNG cần API thật:
- Nếu có: copy `static/` → `site/` là xong (zero backend)
- Nếu thiếu: thêm `?demo=1` bypass auth path hoặc tạo `site/demo.html` dùng `demo.js` thuần

> ⚠️ Đây là rủi ro kỹ thuật chính của Task 4 — verify trước khi build site.

**Step 2: Landing page**

```
Hero:    "Your homelab, one Tailscale-only console." + demo screenshot
Demo:    Iframe nhúng site/demo.html (demo mode, không auth)
How:     3 bước quickstart (compose up)
Compare: Bảng so sánh vs Beszel / Uptime Kuma / Grafana (✅/❌, fact-only)
Footer:  GitHub · Docs · Apache-2.0
```

**Step 3: GitHub Actions deploy**

`.github/workflows/pages.yml`: build site/ → artifact → deploy-pages (permissions: contents read, pages write, id-token write).

**Step 4: Bật Pages**

Repo Settings → Pages → Source: GitHub Actions. Homepage URL = kết quả.

**Step 5: Commit**

```bash
git add site/ .github/workflows/pages.yml
git commit -m "feat(site): add landing page with embedded demo"
```

**Verify:** `https://bnhminh1010.github.io/homelab-dashboard/` load, demo iframe hoạt động không cần login, mobile OK (responsive), không JS error.

---

## Task 5: Installer 1 lệnh (curl | bash)

**Objective:** Giảm friction cài đặt — pattern của Beszel (`get.beszel.dev`), dùng subdomain sẵn có `getdashboard.thinkai.id.vn` (0 đồng).

**Files:**
- Tạo: `install.sh` (root repo)
- Tạo: `.github/workflows/release.yml` bổ sung hoặc script đứng riêng

**Step 1: Viết install.sh**

- Detect OS/arch (linux amd64/arm64)
- Download binary từ GitHub Release (latest tag) hoặc pull compose file
- Mặc định: `podman compose up -d` (path: repo root `compose.yml`)
- Không sudo, in rõ từng bước, idempotent (chạy lại an toàn)
- `set -euo pipefail`

**Step 2: Test script**

```bash
bash -n install.sh          # syntax
shellcheck install.sh        # nếu có
# chạy thử trên máy local (không thực sự cài — dùng --dry-run flag hoặc test riêng)
```

**Step 3: Gắn subdomain (anh tự làm trên Cloudflare, 5 phút)**

Sau khi Task 4 deploy GitHub Pages site (có chứa install.sh), thêm DNS record:

```
Type:   CNAME
Name:   getdashboard
Target: bnhminh1010.github.io          (hoặc target redirect, tùy setup)
Proxy:  DNS only (grey cloud)          — tránh CF chặn curl/redirect
```

→ Installer cuối: `curl -sL https://getdashboard.thinkai.id.vn | bash`

Lưu ý: nếu CNAME tới GitHub Pages site thì URL đầy đủ là `https://getdashboard.thinkai.id.vn/install.sh` — kiểm tra đường dẫn sau khi setup.

**Step 4: Commit**

```bash
git add install.sh
git commit -m "feat: add one-line installer for release images"
```

**Verify:** `bash -n` + dry-run exit 0; sau khi anh thêm DNS: `curl -sL https://getdashboard.thinkai.id.vn/install.sh | head -5` trả về nội dung script.

---

## Task 6: Chuẩn bị awesome-selfhosted PR

**Objective:** Sẵn sàng submit lên awesome-selfhosted — điều kiện cần: license ✓ (Apache-2.0 có sẵn), activity đều, 1 release ổn định.

**Files:**
- Tạo (ngoài repo, chuẩn bị nội dung): `homelab-dashboard.yml` theo template awesome-selfhosted-data

**Step 1: Verify điều kiện**

- [ ] LICENSE file ở root ✓ (đã có Apache-2.0)
- [ ] Hoạt động 6+ tháng liên tục (git log)
- [ ] ≥ 1 release stable (git tag — hiện có v0.0.1-beta.1..4, cân nhắc v1.0.0-alpha)

**Step 2: Viết yml theo template**

```yaml
name: Homelab Dashboard
description: >-
  Tailscale-only operations console for homelab nodes. Monitor services,
  SLOs, containers and logs; open shells without exposing admin ports.
  ([Demo](https://bnhminh1010.github.io/homelab-dashboard/))
  ([Source Code](https://github.com/bnhminh1010/homelab-dashboard))
license: Apache-2.0
platform: Go/Docker
tags:
  - monitoring
  - self-hosting
```

**Step 3: PR sau khi repo public + release ổn**

- Tạo PR vào `awesome-selfhosted/awesome-selfhosted-data` (sau Task 3-4 hoàn tất)
- Chú ý: bot tự check link chết + unmaintained

**Verify:** Yml hợp lệ (tham khảo template `.github/ISSUE_TEMPLATES/addition.md` của awesome-selfhosted-data), PR không bị bot reject vì link chết.

---

## Task 7: Selfh.st submit + tài khoản cộng đồng

**Objective:** Đưa project lên radar của kênh homelab lớn nhất (30k+ readers).

**Files:**
- Không code — chỉ thao tác web

**Step 1: Tạo tài khoản cộng đồng**

- GitHub: tạo org account hoặc dùng account cá nhân — cần ảnh đại diện + bio rõ ràng
- Mastodon/Fediverse (homelab community active): tạo account, theo dõi @selfhst

**Step 2: Submit selfh.st**

- Vào `selfh.st/submit/` sau khi README có screenshot + website live
- Kèm: mô tả ngắn, link repo, link demo, tag `monitoring`/`dashboard`

**Step 3: Theo dõi**

- Newsletter thứ 6 hàng tuần — nếu chưa được feature, submit lại sau 2-3 tuần với update mới (release mới, tính năng mới)

**Verify:** Submit form thành công (xác nhận email nếu có), ghi lại ngày submit để follow-up.

---

## Thứ tự thực hiện & phụ thuộc

```
Task 1 (screenshots) ──► Task 2 (README) ──► Task 3 (GH metadata)
        │                                        │
        └──────────────► Task 4 (website) ───────┘
                              │
Task 5 (installer) ◄──────────┘ (độc lập, chạy song song được)
Task 6 (awesome PR) ── sau Task 3+4 + release ổn
Task 7 (selfh.st) ──── sau Task 2+4
```

## Tests / Validation

| Task | Kiểm tra |
|---|---|
| 1 | Browser console 0 error trước khi chụp; ảnh < 500KB |
| 2 | README render trên GitHub, ảnh hiển thị, GIF chạy |
| 3 | `gh repo view` hiển thị description/topics/homepage |
| 4 | Pages URL load, demo iframe hoạt động không login, mobile OK |
| 5 | `bash -n` + dry-run exit 0 |
| 6 | Yml theo template, link không chết |
| 7 | Submit thành công, có follow-up date |

## Risks & Tradeoffs

1. **Demo standalone (Task 4)**: `static/index.html?demo=1` hiện yêu cầu auth (401 nếu thiếu Tailscale headers — đã verify). Nếu `DemoApi` không chạy standalone, cần thêm 1 route server `GET /demo` bỏ qua auth (chỉ serve static + mock data) — chạm vào backend nhưng nhỏ (1 handler). **Verify trước, rẻ hơn đoán.**
2. **Release ổn định (Task 6)**: tags hiện là beta.1-4 — awesome-selfhosted không đòi hỏi stable nhưng bot check hoạt động. Cân nhắc tag `v1.0.0-alpha.1` trước khi PR.
3. **Homepage URL**: Task 3 cần biết URL Pages trước — làm Task 4 trước Task 3 phần homepage (hoặc set sau).
4. **Token trong git remote**: `origin` đang chứa access token (x-access-token). KHÔNG commit/print. Không liên quan task nhưng cần nhớ.

## Open Questions — ĐÃ CHỐT

1. ✅ **Repo public?** — Đã public (`bnhminh1010/homelab-dashboard`). Task 3/6 không bị chặn.
2. ✅ **Tên repo?** — Giữ nguyên `homelab-dashboard`. Không đổi tên.
3. ✅ **Domain ngắn cho installer?** — **CÓ, dùng subdomain của `thinkai.id.vn` (đã có sẵn, 0 đồng)**. DNS đang chạy Cloudflare (NS: arushi/cesar.ns.cloudflare.com), có website + email MX đang dùng → chỉ thêm subdomain mới, không đụng record hiện tại:
   - **`getdashboard.thinkai.id.vn`** → trỏ về installer (CNAME → GitHub Pages site hoặc redirect, tùy setup Task 4)
   - Lưu ý: `.id.vn` là TLD Việt Nam — hơi lạ với audience quốc tế (selfhosted community) nhưng chấp nhận được; đây là điểm cộng nhỏ chứ không phải deal-breaker
   - Cách thêm: Cloudflare dashboard → DNS → Add record (5 phút). **Cần anh tự làm** vì tôi không có quyền Cloudflare — plan sẽ ghi rõ record cần thêm
   - Nếu CF proxy (orange cloud) gây vấn đề với curl installer → dùng DNS only (grey cloud) cho record này
