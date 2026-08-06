# Landing Page: Add "How it works" + Comparison + FAQ Blocks

> **Goal:** Bổ sung 3 block chuẩn dev-tool landing (theo research Evil Martians 100+ trang + Beszel) để landing không còn "thiếu gì đó": How it works (kiến trúc), Comparison table (data có sẵn từ docs/comparison.md), FAQ accordion (details/summary thuần).

**Context:** Landing hiện có: Hero → KPI → Demo → Screenshots(4) → Features(bento) → Security → Quickstart → CTA → Footer. Cần chèn: **How it works** (sau KPI, trước Demo), **Comparison** (sau Features, trước Security), **FAQ** (sau Quickstart, trước CTA). Nav cập nhật thêm link.

**Constraints (bắt buộc):**
- HTML + CSS thuần + vanilla JS, zero framework, no CDN (fonts tự host sẵn)
- Tokens đồng bộ 100% dashboard: `--bg-deep #090909, --bg-base #101010, --text-primary #e5e1d9, --accent #d99a3d, --green #55bf78, radius 4px, --line-soft, --bg-card, --bg-raise, --font-mono, --ease, --maxw 1160px`
- Anti-slop rules (hallmark + taste-skill): KHÔNG eyebrow mới, KHÔNG scroll-reveal, KHÔNG fake chrome, KHÔNG icon-grid đều 3 cột, mỗi block layout riêng biệt
- `prefers-reduced-motion` tôn trọng (không thêm motion mới — FAQ dùng details/summary native, 0 JS)
- Identity ẩn: không xuất hiện `100.120.110.25`, `debian-server`, "Binh Minh"; demo data giữ nguyên
- Không xóa screenshots khỏi repo — chỉ bỏ khỏi HTML (đã làm)

**Kiến trúc thật (nguồn: `site/index.html` security section + `docs/comparison.md`):**
- Dashboard = 1 static Go binary, đọc/ghi 1 file SQLite
- Host agent = cùng binary, mode agent, giao tiếp qua Unix socket local — không có TCP listener
- Remote nodes = 1 binary/node, rootless, dial-out qua Tailscale WSS — không bao giờ listen
- Auth = Tailscale identity (tailnet login), zero password DB
- 3 binaries, 3 privilege boundaries, rootless Podman

**Files:**
- Modify: `site/index.html` (chèn 3 section + 1 nav link)
- Modify: `site/css/style.css` (thêm ~120 dòng CSS)
- Modify: `site/js/reveal.js` (KHÔNG cần — FAQ dùng native details/summary; verify lại, nếu OK thì không đụng)
- Test: copy 3 file sang `/tmp/landing-test/`, dùng playwright (`/tmp/responsive-audit.py` + audit2) + browser console

**Verify criteria (mỗi task):**
1. Render đúng: section xuất hiện đúng vị trí, không lỗi console
2. Anti-slop: 0 eyebrow mới, 0 reveal mới, 0 fake chrome
3. Responsive 6 viewport (320/375/414/768/1024/1440): hScroll=0, không wrap CTA, comparison table scroll ngang trên mobile (không vỡ)
4. HTML valid (details/summary đóng mở đúng)

---

## Task 1: Thêm "How it works" section vào index.html

**Objective:** Chèn section giải thích kiến trúc 3 tầng sau KPI strip, trước Demo.

**File:** Modify `site/index.html` — chèn giữa `</section>` (kpi, dòng 62) và `<!-- ══════════════ LIVE DEMO` (dòng 64):

```html
<!-- ══════════════ HOW IT WORKS ══════════════ -->
<section class="how" id="how">
  <div class="section-head">
    <h2>Three binaries. One SQLite file. Zero ports to open.</h2>
    <p class="section-sub">No Node.js, no reverse proxy, no metrics exporters — the whole console is two processes talking over a Unix socket, plus one outbound-only agent per remote node.</p>
  </div>
  <div class="how-grid">
    <article class="how-card">
      <span class="how-num">01</span>
      <h3>Dashboard</h3>
      <p>A single static Go binary serves the UI and reads and writes one SQLite file. Configurable history quota, no TSDB.</p>
    </article>
    <article class="how-card">
      <span class="how-num">02</span>
      <h3>Host agent</h3>
      <p>The same binary, in agent mode, talks to the dashboard over a local Unix socket. No TCP listener — nothing to port-forward.</p>
    </article>
    <article class="how-card">
      <span class="how-num">03</span>
      <h3>Remote nodes</h3>
      <p>One binary per node, installed unprivileged, dials out to your tailnet over WSS. It never listens, so there is no inbound surface.</p>
    </article>
  </div>
  <p class="how-note">Everything runs rootless. Your Tailscale login is the only password.</p>
</section>
```

**Step 1:** Patch vào index.html.
**Step 2:** Verify: `grep -c 'how-card' site/index.html` = 3; section nằm giữa kpi và demo (đọc lại 10 dòng quanh vùng chèn).
**Step 3:** Commit (cùng Task 3 — gộp HTML 3 block 1 commit).

## Task 2: Thêm Comparison table section

**Objective:** Bảng 5 cột rút gọn từ docs/comparison.md — chèn sau Features section (sau `</section>` dòng ~133), trước SECURITY.

**Rows chọn (7 hàng khác biệt nhất, lấy data thật từ comparison.md):**
| Feature | HomeLab Dashboard | Start pages | Uptime Kuma | Beszel/Netdata | Grafana stack |
|---|---|---|---|---|---|
| Zero inbound ports | ✅ native | — | — | ⚠️ outbound WS possible | — |
| SLO error budgets 7/30/90d | ✅ first-party | — | ⚠️ uptime % only | — | ⚠️ needs plugin |
| Backup freshness monitoring | ✅ first-party | — | ⚠️ diff mechanism | — | ⚠️ hand-rolled |
| 90-day SQLite history, no Prometheus | ✅ native | — | ⚠️ status pages | ⚠️ basic ~30d | ✅ but heavy |
| Interactive container shell | ✅ native | — | — | — | — |
| Confirmed host Bash | ✅ native | — | — | — | ⚠️ Cockpit-like |
| Tailscale identity = sole auth | ✅ native | — | — | — | — |

```html
<!-- ══════════════ COMPARISON ══════════════ -->
<section class="compare" id="compare">
  <div class="section-head">
    <h2>How it stacks against the usual homelab stack.</h2>
    <p class="section-sub">The full feature matrix with honest "we don't do this" notes lives in <a href="https://github.com/bnhminh1010/homelab-dashboard/blob/main/docs/comparison.md" target="_blank" rel="noopener">docs/comparison.md</a>.</p>
  </div>
  <div class="cmp-wrap">
    <table class="cmp-table">
      <thead>
        <tr><th>Feature</th><th class="cmp-own">HomeLab Dashboard</th><th>Start pages</th><th>Uptime Kuma</th><th>Beszel / Netdata</th><th>Grafana stack</th></tr>
      </thead>
      <tbody>
        <tr><td>Zero inbound ports</td><td class="cmp-own">✅ native</td><td>—</td><td>—</td><td>⚠️ outbound WS possible</td><td>—</td></tr>
        <tr><td>SLO error budgets 7/30/90d</td><td class="cmp-own">✅ first-party</td><td>—</td><td>⚠️ uptime % only</td><td>—</td><td>⚠️ needs plugin</td></tr>
        <tr><td>Backup freshness monitoring</td><td class="cmp-own">✅ first-party</td><td>—</td><td>⚠️ diff mechanism</td><td>—</td><td>⚠️ hand-rolled</td></tr>
        <tr><td>90-day SQLite history, no Prometheus</td><td class="cmp-own">✅ native</td><td>—</td><td>⚠️ status pages</td><td>⚠️ basic ~30d</td><td>✅ but heavy</td></tr>
        <tr><td>Interactive container shell</td><td class="cmp-own">✅ native</td><td>—</td><td>—</td><td>—</td><td>—</td></tr>
        <tr><td>Confirmed host Bash</td><td class="cmp-own">✅ native</td><td>—</td><td>—</td><td>—</td><td>⚠️ Cockpit-like</td></tr>
        <tr><td>Tailscale identity = sole auth</td><td class="cmp-own">✅ native</td><td>—</td><td>—</td><td>—</td><td>—</td></tr>
      </tbody>
    </table>
  </div>
</section>
```

**Step 1:** Patch HTML (thay cả block cũ nếu có compare placeholder — kiểm tra hiện không có).
**Step 2:** Verify: `grep -c '<tr>' site/index.html` tăng đúng 8 (1 header + 7 rows).

## Task 3: Thêm FAQ accordion section

**Objective:** 5 câu hỏi details/summary thuần HTML — chèn sau Quickstart (`</section>` dòng ~172), trước COMPARE/CTA (dòng 174).

```html
<!-- ══════════════ FAQ ══════════════ -->
<section class="faq" id="faq">
  <div class="section-head">
    <h2>Fair questions, straight answers.</h2>
  </div>
  <div class="faq-list">
    <details class="faq-item"><summary>Do I need Tailscale?</summary><p>Yes — your tailnet identity is the auth plane, so there is no password database to secure. Tailscale's free tier covers a personal tailnet with 100 devices.</p></details>
    <details class="faq-item"><summary>How much does it cost?</summary><p>Nothing. Apache-2.0, one static Go binary, one SQLite file. No SaaS, no licence keys, no telemetry.</p></details>
    <details class="faq-item"><summary>Does it work with Docker, or only Podman?</summary><p>Container health, stats, logs and shells are built against the rootless Podman socket. If you run Docker, the container workspaces won't have a socket to talk to.</p></details>
    <details class="faq-item"><summary>How is this different from Grafana, Beszel or Uptime Kuma?</summary><p>Those are point tools — metrics, monitoring or availability. This console closes the loop: see the alert, open the right log, drop into the right shell, all in one place, with zero inbound ports. See the <a href="https://github.com/bnhminh1010/homelab-dashboard/blob/main/docs/comparison.md" target="_blank" rel="noopener">full comparison</a>.</p></details>
    <details class="faq-item"><summary>Where does my data live?</summary><p>One SQLite file on the dashboard host. History is rolled up in three tiers — 10s samples for 48h, 1-minute for 30 days, 15-minute for 90 days — with a configurable quota.</p></details>
  </div>
</section>
```

**Step 1:** Patch HTML.
**Step 2:** Verify: `<details>` count = 5, mỗi `<summary>` không rỗng; đọc lại vùng chèn.

## Task 4: Nav link "How it works"

**Objective:** Thêm 1 link vào nav-links (sau Features, trước Security — thứ tự đúng flow trang).

**File:** `site/index.html` dòng 25-26:
```html
    <a href="#features">Features</a>
    <a href="#how">How it works</a>
    <a href="#security">Security</a>
```
(Không thêm FAQ vào nav — 6 link là đủ, mobile đã ẩn hết trừ GitHub.)

## Task 5: CSS cho 3 block + responsive

**File:** Modify `site/css/style.css` — append trước `/* ─── RESPONSIVE ─── */`.

**Step 1 — How it works (khác quickstart — dùng số + border-top, không dùng card grid đều):**
```css
/* ─── HOW IT WORKS ─── */
.how { padding: 88px 24px; }
.how-grid { max-width: var(--maxw); margin: 0 auto; display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px 32px; }
.how-card { border-top: 1px solid var(--line); padding-top: 18px; }
.how-num { font-family: var(--font-mono); font-size: 12px; color: var(--accent); letter-spacing: .1em; }
.how-card h3 { font-size: 1.05rem; font-weight: 600; margin: 6px 0 8px; }
.how-card p { color: var(--text-secondary); font-size: 14.5px; }
.how-note { max-width: var(--maxw); margin: 28px auto 0; font-family: var(--font-mono); font-size: 12.5px; color: var(--text-dim); }
```
Lưu ý: 3 cột đều bị hallmark cấm? → KHÔNG: hallmark cấm *icon-grid 3 cột feature cards*; đây là **numbered steps** (bun-style, Evil Martians "step-by-step layout") — chấp nhận được, và border-top thay cho card-box tránh template.

**Step 2 — Comparison (table scroll ngang mobile):**
```css
/* ─── COMPARISON ─── */
.compare { padding: 104px 24px; }
.cmp-wrap { max-width: var(--maxw); margin: 0 auto; overflow-x: auto; border: 1px solid var(--line-soft); border-radius: var(--radius); }
.cmp-table { width: 100%; min-width: 720px; border-collapse: collapse; font-size: 13.5px; background: var(--bg-card); }
.cmp-table th, .cmp-table td { text-align: left; padding: 11px 14px; border-bottom: 1px solid var(--line-soft); white-space: nowrap; }
.cmp-table th { font-family: var(--font-mono); font-size: 11px; letter-spacing: .06em; color: var(--text-dim); font-weight: 500; }
.cmp-table tbody tr:last-child th, .cmp-table tbody tr:last-child td { border-bottom: 0; }
.cmp-own { color: var(--text-primary); background: rgba(217, 154, 61, 0.05); font-weight: 600; }
```
(mobile: `min-width: 720px` + wrapper scroll — bảng không vỡ layout, đúng gate 34.)

**Step 3 — FAQ (native details, 0 JS):**
```css
/* ─── FAQ ─── */
.faq { padding: 96px 24px; }
.faq-list { max-width: 760px; margin: 0 auto; }
.faq-item { border-bottom: 1px solid var(--line-soft); }
.faq-item summary { list-style: none; cursor: pointer; padding: 18px 4px; font-weight: 600; font-size: 15.5px; display: flex; justify-content: space-between; align-items: center; gap: 16px; }
.faq-item summary::-webkit-details-marker { display: none; }
.faq-item summary::after { content: "+"; font-family: var(--font-mono); color: var(--accent); font-size: 18px; transition: transform var(--dur) var(--ease); }
.faq-item[open] summary::after { transform: rotate(45deg); }
.faq-item p { padding: 0 4px 18px; color: var(--text-secondary); font-size: 14.5px; line-height: 1.65; }
.faq-item p a { color: var(--accent); }
```

**Step 4 — Responsive (media query ≤900px hiện có):**
```css
  .how-grid { grid-template-columns: 1fr; gap: 26px; }
```

**Verify:** `python3 -c` parse CSS braces cân bằng; copy sang /tmp test.

## Task 6: Verify toàn diện

**Step 1:** Copy 3 file (index.html, style.css, reveal.js) sang `/tmp/landing-test/`.
**Step 2:** Restart server nếu cần; chạy playwright script đã có:
```bash
cd /tmp && python3 responsive-audit.py   # 6 viewport: hScroll=0, wrap=0
python3 responsive-audit2.py             # iframeH, title lines, img overflow
```
**Step 3:** Browser console (Chrome devtools qua CDP hoặc `browser_navigate` + `browser_console`):
- `document.querySelectorAll('.section-eyebrow').length` = 0 (không thêm eyebrow)
- `.reveal` count không tăng (0 — đã bỏ universal)
- 3 section mới render: `#how, #compare, #faq` tồn tại, `details` mở/đóng được
- 0 JS errors
**Step 4:** Chụp screenshot 1440 + 375 cho user duyệt (`/tmp/shot-1440-how.png` etc).

## Task 7: Commit

**Step 1:** `git status` — chỉ 3 file: `site/index.html`, `site/css/style.css` (reveal.js chỉ nếu thay đổi).
**Step 2:**
```bash
git add site/index.html site/css/style.css
git commit -m "feat(landing): add how-it-works, comparison table and FAQ blocks
- how: 3-tier architecture (dashboard/unix-socket agent/outbound WSS nodes)
- compare: 7-row matrix distilled from docs/comparison.md, scrolls on mobile
- faq: native details/summary, zero JS, 5 questions
- nav: +How it works link
- anti-slop: no new eyebrows/reveal/fake chrome; numbered steps + table + accordion layouts"
git push
```
**Constraint:** commit message có context + constraint + confidence.

## Out of scope (đã chốt với user)
- ❌ Testimonials (chưa có user thật — giữ integrity)
- ❌ Pricing, Blog/Changelog (early stage)
- ❌ Pages deploy (đang kẹt queue — để sau)
