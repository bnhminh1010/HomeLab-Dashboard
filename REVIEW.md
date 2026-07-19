# Dashboard Review

> Cập nhật ngày 2026-07-19 trên `main` tại commit `5606ea5`.
> Phạm vi: source Go, frontend vanilla JS, WebSocket, terminal, Podman Compose và artifact mới được push.

## Kết luận

- Giao diện dashboard đã đầy đủ theo plan: bố cục 3/2/1 cột, system metrics, services, containers, alerts, terminal xterm.js, trạng thái loading/offline/empty và responsive.
- Dự án vẫn là **Go + HTML/CSS/vanilla JS**, không có Node.js, npm, bundler hoặc CDN runtime.
- Bộ acceptance browser pass trên desktop, tablet và mobile; không phát hiện external request hay lỗi console trong kịch bản demo.
- Chưa nên kết luận runtime hoàn toàn ổn định. Hai vấn đề ưu tiên cao nhất là contract WebSocket làm mất dữ liệu service và cấu hình trust header có thể bị dùng sai khi chạy trực tiếp.
- Commit `5606ea5` không thay đổi source terminal. Commit chỉ thêm file review này và binary `dashboard`; tính năng Exec đã tồn tại từ commit cha `a335a03`.

## Findings cần xử lý

### P1 — Live WebSocket làm mất dữ liệu chi tiết và có thể xóa Probe URL

Frontend lấy snapshot đầu tiên từ `/api/v1/snapshot`, nhưng luồng realtime lại kết nối `/ws/metrics` tại `static/js/socket.js:40`. Đây là endpoint legacy; serializer tại `internal/httpapi/legacy.go:49-58` không gửi `probeUrl`, `lastCheckedAt` hoặc `latencyMs` của service. Nó cũng thiếu network interface và `cpuNormalizedPercent` của container.

Mỗi frame mới lại render đè state tại `static/js/app.js:126-134`. Hệ quả:

- Card đang có probe chuyển thành `Probe not configured` và mất latency sau tick đầu tiên.
- Form Edit nhận `probeUrl` rỗng; nếu admin bấm Save, probe URL hiện có sẽ bị xóa khỏi database.
- Network interface đổi thành `default`; CPU bar container có thể dùng giá trị chưa normalize.

Hướng xử lý: dùng `/ws/v1/metrics` để REST và WebSocket dùng chung `SnapshotEnvelope`, hoặc bổ sung đủ field vào legacy serializer. Nếu chuyển sang endpoint v1, cần reset `lastSequence` khi mở một WebSocket connection mới.

### P1 — `TRUST_TAILSCALE_HEADERS=false` không tắt việc tin identity header

`internal/auth/auth.go:55-79` luôn đọc `Tailscale-User-Login`. Tham số boolean hiện chỉ điều khiển Secure cookie/origin, dù được truyền từ biến `TRUST_TAILSCALE_HEADERS` tại `cmd/dashboard/main.go:106-109`.

Nếu bind binary trực tiếp ra `0.0.0.0`, client có thể tự gửi `Tailscale-User-Login` trùng một giá trị trong `ADMIN_USERS`, tạo session rồi gọi API admin. Vì vậy:

- Tiếp tục dùng topology Compose hiện tại: dashboard bind `127.0.0.1` trong network namespace của Tailscale và không publish host port.
- Không dùng lại mẫu systemd cũ bind `0.0.0.0`.
- Nếu cần chạy standalone, phải implement trust boundary thực sự dựa trên proxy/remote address thay vì chỉ đổi tên hoặc đổi giá trị env.

### P2 — Tab đang mở không tự phục hồi sau khi backend restart

Browser session chỉ nằm trong memory tại `internal/auth/auth.go:47-67`, nên backend restart làm cookie cũ mất hiệu lực. Frontend chỉ gọi `api.session()` một lần khi start tại `static/js/app.js:256-276`; reconnect tại `static/js/socket.js:36-64` chỉ mở lại WebSocket và không xin session/CSRF mới.

Kết quả: tab cũ có thể retry `401` vô hạn cho tới khi người dùng reload trang. Cần có flow renew session trước hoặc sau reconnect thất bại, sau đó mở lại metrics WebSocket.

### P2 — Realtime render làm mất keyboard focus và spam screen reader

`static/js/services.js:81-89` và `static/js/containers.js:58-65` dùng `replaceChildren()` cho toàn bộ danh sách mỗi metrics tick. Hai vùng này lại có `aria-live="polite"` tại `static/index.html:125,165`.

Focus trên service, Logs hoặc Exec bị loại khỏi DOM khoảng mỗi 2 giây; screen reader cũng có thể đọc lại cả danh sách liên tục. Nên update keyed/in-place theo ID và chỉ dùng live region cho thay đổi trạng thái quan trọng.

### P2 — Readiness có thể xanh khi collector đang hỏng

`internal/metrics/hub.go:171-186` vẫn publish snapshot kể cả khi collector trả lỗi. `/health/ready` tại `internal/httpapi/server.go:65-78` chỉ cần database ping thành công và tồn tại một snapshot.

Nếu mount `/host/proc` sai hoặc Podman socket hỏng ngay từ startup, readiness vẫn có thể trả `200`. Nên lưu trạng thái thành công/thất bại của lần collect gần nhất và đưa dependency bắt buộc vào readiness.

### P2 — Terminal timeout không chặn được stream write bị treo

Idle/hard timer chạy trong vòng `select` tại `internal/terminal/manager.go:294-306`, nhưng input được ghi đồng bộ bằng `writeAll()` tại `internal/terminal/manager.go:322-345`. Nếu Podman stream ngừng đọc và `Write` bị block, goroutine không quay lại `select`; timeout không thể đóng session.

Nên thực hiện write có khả năng cancel/deadline hoặc tách write worker có giới hạn queue và đóng connection khi context hết hạn.

### P2 — Binary được commit không phải artifact production

File `dashboard` có kích thước `28,937,516` byte, là ELF x86-64 dynamically linked, `CGO_ENABLED=1`, có debug info và ghi `vcs.revision=a335a03`. Trong khi `Containerfile:11-12` tự build binary static, stripped với `CGO_ENABLED=0`.

Binary root không được Podman Compose sử dụng, nhưng làm repository và build context tăng gần 29 MB. Nên bỏ file khỏi Git và thêm `/dashboard` vào `.gitignore`, `.dockerignore` cùng `.containerignore`. Khi cần phát hành binary, dùng release artifact có version/checksum thay vì commit trực tiếp.

### P3 — Các vấn đề nhỏ còn lại

- Root filesystem qua LVM/dm-crypt có thể luôn hiện Disk I/O `Idle`, vì `internal/metrics/collector.go:316-340` so tên `/dev/mapper/...` trực tiếp với tên `dm-*` trong `/proc/diskstats`.
- Ở mobile, CSS ẩn text `COLLAPSE/EXPAND` nhưng nút terminal không có `aria-label`, nên toggle có thể mất accessible name.
- Frontend chưa hiển thị cờ `truncated`; count có thể trông như danh sách đầy đủ dù backend đã cắt frame xuống dưới 50 KB.
- `static/lib/**` là vendor bundle nhưng chưa có `linguist-vendored`, nên GitHub đang tính tỷ lệ JavaScript cao hơn code thực viết.

## Các mục review cũ đã xác minh lại

| Mục cũ | Kết luận hiện tại |
|---|---|
| Terminal chỉ xem log | Sai/stale. `logs` và admin-only `exec` đã có typed input, PTY stream và resize. |
| CSRF chỉ dùng một lần | Không đúng theo code/test. Token chỉ được compare, không rotate hoặc delete; cùng token đã được test qua nhiều mutation. Nếu homelab còn lỗi thì cần capture HTTP status/body để tìm nguyên nhân khác. |
| Cần systemd để chạy persistent | Không bắt buộc. Compose đã có named volumes, healthcheck và `restart: unless-stopped`. |
| Service icon map sai | Không đúng. `model.Service.Icon` có JSON tag, store/manager giữ icon và frontend đọc `service.icon`. Dữ liệu cũ không có icon vẫn có thể hiển thị fallback. |
| `TestSvc` còn sót | Đây là dữ liệu runtime trong SQLite, không phải lỗi source. Cần kiểm tra API/database trên homelab trước khi xóa. |
| 9 containers, 8 services, RAM ~30 MB | Chỉ là snapshot quan sát trước đây, không phải bảo đảm từ repository hoặc test hiện tại. |

## Verification đã chạy

- `go test ./... -count=1` — pass.
- `go test -race ./... -count=1` — pass.
- `go vet ./...` — pass.
- `go test -tags=integration ./internal/httpapi -count=1` — pass.
- `go test -tags=browser ./tests/browser -count=1` — pass desktop/tablet/mobile, xterm, edge states và zero external request.
- `go mod verify` — all modules verified.
- `podman compose config` với env mẫu — pass; cấu hình không dùng Node/npm.

## Chưa xác minh trong lượt review này

- Exec shell end-to-end với Podman socket và container thật trên homelab.
- Tailscale HTTPS/identity thực tế sau recreate.
- Số container/service, RAM runtime và record `TestSvc` trong database hiện tại.

## Thứ tự xử lý đề xuất

1. Đồng nhất contract `/api/v1/snapshot` và WebSocket để tránh mất `probeUrl`.
2. Làm rõ và enforce trust boundary của Tailscale headers.
3. Renew browser session khi backend restart.
4. Update service/container DOM in-place.
5. Sửa readiness và cancelable terminal write.
6. Bỏ binary khỏi Git, ignore artifact và đánh dấu `static/lib/**` là vendored cho GitHub Linguist.
