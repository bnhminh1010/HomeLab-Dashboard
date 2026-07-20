# Đánh giá HomeLab Dashboard

> Cập nhật ngày 2026-07-20 theo source trong worktree hiện tại. File này đánh
> giá implementation; bằng chứng release phải gắn với revision cụ thể ở phần
> cuối.

## Kết luận

Roadmap dành cho homelab cá nhân đã được hiện thực đầy đủ ở mức source và UI:

- lịch sử metric theo tầng đến 90 ngày;
- alert rule có state, cooldown, acknowledge/silence và ntfy;
- viewer log container riêng, có streaming và công cụ tìm kiếm;
- dashboard tối đa 5 node (`local` + 4 remote) qua outbound node agent;
- terminal container và Bash thật trên host đang chọn;
- export/import cấu hình có preview, revision và kiểm tra xung đột;
- retention cho cả metric lẫn dữ liệu vận hành;
- UI monitoring workbench responsive, giữ last-known state khi nguồn dữ liệu
  stale/offline.

Dự án vẫn giữ đúng ràng buộc **Go + HTML/CSS/vanilla JS**, không có Node.js,
npm, bundler hay CDN runtime. Đây là một dashboard vận hành homelab gọn nhẹ,
không phải bản thay thế Grafana/Prometheus/Loki hoặc một control plane doanh
nghiệp có HA.

## Đối chiếu roadmap với implementation

| Hạng mục | Trạng thái | Bằng chứng source chính |
|---|---|---|
| History đến 90 ngày | Đã triển khai | `internal/history/types.go`, `internal/store/history.go`, `internal/httpapi/history.go`, `static/js/history.js` |
| Catalog resource history đã lưu trữ | Đã triển khai, có giới hạn | `GET /api/v1/history/resources`, `internal/store/history.go`, `static/js/history.js` |
| Quota và retention | Đã triển khai | `internal/history/writer.go`, `internal/store/retention.go`, `cmd/dashboard/main.go` |
| Alerting | Đã triển khai | `internal/alerts/`, `internal/store/alerts.go`, `internal/httpapi/alerts.go`, `static/js/alerts.js` |
| ntfy | Đã triển khai, tùy chọn | `internal/alerts/ntfy.go`, `.env.example`, `compose.yml` |
| Container logs viewer | Đã triển khai | `internal/terminal/manager.go`, `static/js/terminal.js`, `static/index.html` |
| Multi-node | Đã triển khai, giới hạn 5 node | `internal/nodes/`, `internal/nodeagent/`, `cmd/node-agent/`, `static/js/nodes.js` |
| Remote install/enroll | Đã triển khai | `deploy/node-agent/install.sh`, `deploy/node-agent/homelab-node-agent.service` |
| Config export/import | Đã triển khai | `internal/dashboardconfig/`, `internal/httpapi/config.go`, `static/js/settings.js` |
| Host Bash | Đã triển khai cho local và remote | `internal/hostagent/`, `internal/nodeagent/sessions.go`, `internal/terminal/manager.go` |
| UI/UX workbench | Đã triển khai | `static/index.html`, `static/css/style.css`, các module `static/js/` |

## Các điểm đã làm tốt

### 1. Lịch sử có chính sách dữ liệu rõ ràng

- Host: raw 10 giây trong 48 giờ, rollup 1 phút trong 30 ngày và 15 phút trong
  90 ngày.
- Rollup host 1 phút làm lại 5 phút dữ liệu gần nhất. Cửa sổ này bao phủ clock
  skew tối đa 2 phút của remote node cộng phần đệm scheduler, nên snapshot đến
  trễ vẫn được gộp trước khi raw hết hạn.
- Container: raw 30 giây trong 48 giờ, rollup 5 phút trong 30 ngày và 1 giờ
  trong 90 ngày.
- Service: observation/transition được tổng hợp thành uptime theo giờ và giữ
  90 ngày.
- Query tự chọn resolution theo khoảng thời gian và API chặn range lớn hơn 90
  ngày.
- UI trộn inventory live với catalog history đã lưu, nên vẫn chọn được container
  hoặc service đã biến mất khỏi snapshot hiện tại. Mỗi danh sách container và
  service được giới hạn ở 500 resource mới thấy gần nhất trên node đang chọn;
  đây là giới hạn có chủ ý để endpoint và picker luôn hữu hạn.
- Quota mặc định 2 GiB, cảnh báo từ 80%; khi đầy chỉ dừng raw host/container,
  không làm dừng live metric, alert hoặc service transition.
- Pipeline không ghi history/alert từ source đã bị đánh dấu stale. Khi resource
  biến mất, nó phát mẫu clean có giới hạn để resolve incident thay vì giữ alert
  ma mãi.
- Snapshot vượt budget truyền tải mang cả cờ `truncated` và danh sách
  `truncatedSources`. UI hiện chip **PARTIAL**, đưa overview về degraded và
  pipeline coi từng source bị cắt là stale; inventory bị lược bỏ vì giới hạn
  frame không bị diễn giải thành healthy hoặc resolved.

### 2. Alert lifecycle không chỉ là danh sách cảnh báo giả lập

- Có 9 default rule cho node offline, CPU, memory, disk warning/critical,
  temperature, service failure, container health và restart loop.
- Rule hỗ trợ selector theo node/resource, operator, threshold, thời gian duy
  trì, severity, cooldown và enabled state.
- Có state pending/firing/resolved, event history, acknowledge và silence 1/6/24
  giờ.
- Delivery ntfy có firing/resolved message, retry tăng dần, tối đa 5 lần,
  dead-letter và trạng thái `superseded` để lifecycle mới không gửi notification
  cũ. Khi ntfy tắt, alert engine vẫn giữ đúng cooldown mà không tạo hàng đợi
  delivery vô nghĩa.
- URL ntfy chỉ nhận HTTP(S), không nhận credential trong URL; topic và title
  được validate. Token chỉ đọc từ file secret, không xuất qua API config.

### 3. Logs và terminal đã tách đúng use case

- Nút **Logs** mở viewer read-only, không giả làm terminal nhập lệnh.
- Viewer hỗ trợ follow, pause, search, download và thống kê số dòng/dung lượng.
  Browser chỉ giữ tối đa 10.000 dòng hoặc 5 MiB; `since` tối đa 24 giờ và tail
  tối đa 500 dòng mỗi lần mở.
- Container Shell và Host Shell dùng xterm.js, có PTY resize, heartbeat, timeout,
  session quota và cleanup khi disconnect.
- Protocol chỉ nhận operation typed. Remote container exec luôn mở `/bin/sh`;
  remote host shell luôn mở `/bin/bash` và không có field command/argv tùy ý.

### 4. Multi-node có trust boundary và lifecycle cụ thể

- Giới hạn được enforce phía backend là 5 node tổng cộng, gồm `local` và tối đa
  4 remote.
- Enrollment token random, chỉ dùng một lần và hết hạn sau 10 phút; credential
  lưu dạng hash ở dashboard và file credential phía agent có mode `0600`.
- Agent chạy rootless, kết nối outbound WSS, gửi heartbeat/snapshot mỗi 10 giây,
  reconnect bằng exponential backoff và không mở inbound management port.
- Registry giữ last-known snapshot khi disconnect nhưng đánh dấu stale; alert
  `node.online` xử lý offline/recovery riêng, không đánh giá metric cache như dữ
  liệu mới.
- Node vừa được enroll nhưng chưa từng kết nối có grace period 2 phút trước khi
  rule offline bắt đầu đánh giá, tránh báo động giả trong lúc cài agent.
- Revoke node đóng connection, vô hiệu credential, dọn alert runtime/delivery
  liên quan và đưa default node về `local` nếu cần.

### 5. Import cấu hình tránh leak secret và tránh lost update

- Schema version `homelab-dashboard.config/v1`, tối đa 1 MiB, reject unknown
  field, duplicate key và trailing JSON.
- Export chỉ gồm services, alert rules, UI preferences và display metadata của
  node; không có credential, enrollment token, ntfy token, session, history,
  audit hoặc alert runtime.
- Preview cho biết thay đổi của `merge`/`replace`. Revision SHA-256 được trả qua
  ETag; apply bắt buộc `If-Match` và kiểm tra lại trong transaction. Thiếu
  revision trả `428`, revision stale trả `412`.
- Import node metadata không thể enroll node mới. Default node không còn tồn tại
  được normalize về `local` kèm warning.

### 6. Những finding quan trọng của review cũ đã được xử lý

- Frontend live dùng `/ws/v1/metrics`, giữ cùng `SnapshotEnvelope` với REST và
  reset sequence khi reconnect; không còn mất `probeUrl`, latency hoặc normalized
  container CPU qua serializer legacy.
- Identity header chỉ được tin khi bật trust và peer trực tiếp là loopback;
  config cũng từ chối `TRUST_TAILSCALE_HEADERS=true` với listen address không
  phải loopback.
- Metrics reconnect renew browser session/CSRF, nên tab đang mở có flow tự phục
  hồi sau backend restart.
- Service/container DOM update theo key thay vì `replaceChildren()` toàn bộ mỗi
  tick, giảm mất focus và nhiễu accessibility.
- Readiness yêu cầu SQLite hoạt động và lần collect thành công còn đủ mới;
  snapshot cache stale không làm readiness xanh giả.
- Binary `/dashboard` đã được bỏ khỏi Git, ignore trong worktree và loại khỏi
  build context. Image luôn build binary mới từ source thay vì mang theo ELF
  cũ trong repository.
- `static/lib/**` đã được đánh dấu `linguist-vendored` và `Docs/**` là
  `linguist-documentation` trong `.gitattributes`. Các rule này chỉ sửa cách
  GitHub thống kê ngôn ngữ, không thêm Node.js hay thay đổi runtime.
- Static handler dùng ETag và bắt revalidate: `/lib/` có `max-age=86400`, asset
  ứng dụng có `max-age=3600`, đều kèm `must-revalidate`. Vì tên file vendor ổn
  định qua các release, chính sách này tránh cache cũ giữ bundle đã nâng cấp.

## Giới hạn còn lại và rủi ro vận hành

### Giới hạn theo thiết kế

- Một dashboard instance và một SQLite database: không có HA, leader election
  hoặc multi-writer cluster.
- History tối đa 90 ngày và quota là soft stop cho raw metric, không có remote
  object storage, Prometheus remote-write hoặc Grafana-compatible API.
- Notification chỉ hỗ trợ ntfy; chưa có email, Slack, Telegram hoặc PagerDuty.
- Node cap cố định 5 tổng cộng. Không có fleet management, rolling upgrade hay
  policy distribution cho agent.
- Service shortcuts/probes là cấu hình toàn dashboard; remote node agent chỉ
  thu host/container metric.
- Log viewer chỉ giữ buffer phía browser trong phiên hiện tại. Download lấy
  buffer này, không phải kho log lâu dài như Loki/Elasticsearch.
- RBAC chỉ có hai role viewer/admin từ Tailscale login, chưa có OIDC/SSO mapping
  hoặc permission chi tiết theo node/action.

### Rủi ro cần kiểm chứng trên homelab thật

- Host Shell có toàn bộ quyền của Unix account chạy agent, gồm filesystem,
  network, rootless Podman và `sudo` nếu account được cấp. Đây là tradeoff chủ
  ý, không phải sandbox. Chỉ admin tin cậy mới được nằm trong allowlist.
- Remote node unit cố ý không bật `ProtectSystem`, `PrivateTmp` hoặc
  `NoNewPrivileges` để Bash giống login shell thật. Revoke node/stop service là
  kill switch của đường shell này.
- Podman socket, cgroup layout, sensor path, LVM/dm-crypt disk I/O, Tailscale
  Serve identity và systemd user/linger phụ thuộc máy thật; fake socket/unit test
  không thể chứng minh toàn bộ runtime này.
- Import JSON đã giới hạn 1 MiB nhưng chưa có giới hạn độ sâu lồng nhau. Chỉ
  admin được import; vẫn nên thêm depth budget trước khi mở dashboard cho tập
  người dùng rộng hơn.
- Rule node offline mặc định firing ngay khi connection mất sau khi node đã
  online. Điều này ưu tiên phát hiện nhanh nhưng có thể gây alert flapping với
  mạng remote chập chờn.
- Khi SQLite bị kẹt lúc shutdown, flush history cuối dùng context không bị hủy
  để tránh mất record. Hết deadline tổng, process không đóng database cưỡng bức
  và để systemd/OS thu hồi; cần quan sát shutdown chậm trên máy thật.

## Bằng chứng test có trong repository

Các nhóm regression test đã được thêm cùng implementation:

- history writer, rollup, retention, quota và service freshness:
  `internal/history/*_test.go`, `internal/store/history_test.go`;
- alert lifecycle, delivery race/supersede, ntfy và persistence:
  `internal/alerts/*_test.go`, `internal/store/alerts_test.go`;
- enrollment, registry reconnect/race, agent protocol và remote session:
  `internal/nodes/*_test.go`, `internal/nodeagent/*_test.go`;
- config validation, secret-free export, revision conflict và atomic apply:
  `internal/dashboardconfig/*_test.go`, `internal/store/config_test.go`,
  `internal/httpapi/extensions_test.go`;
- keyed UI, responsive layout, log viewer, history, settings và edge states:
  `tests/browser/dashboard_test.go`.

## Bằng chứng release — worktree candidate

Không suy diễn trạng thái release chỉ từ việc regression test tồn tại. Các
result dưới đây áp dụng cho worktree candidate dựa trên base commit `06d7be6`
(implementation trong worktree chưa commit), chạy ngày **2026-07-20 14:31 +07**.

- [x] Go module, vet, unit và race: `go mod verify`, `go vet ./...`,
  `go test ./... -count=1 -timeout=180s` và
  `go test -race ./... -count=1 -timeout=240s` đều pass.
- [x] Integration/browser: `go test -tags=integration ./internal/httpapi
  -count=1 -timeout=120s -v` pass; full browser suite pass trong 16.592s. Test
  edge-state (bao gồm menu dịch vụ/focus restoration) lặp 10 lần không lỗi.
- [x] Compose/image/container smoke: `podman compose config` pass với config
  ntfy và Host Shell; `podman build -t homelab-dashboard:verify .` pass; image
  khởi động được và `/health/live` trả `200`. `/health/ready` cố ý trả `503`
  trong smoke cô lập vì container không được mount Podman socket/host metrics.
- [x] Asset: test no-Node/no-CDN, manifest checksum và cache header pass;
  frontend nén là **225,600 bytes**, dưới budget 500 KiB.
- [x] Hygiene: `gofmt -l .` và `git diff --check` không có output. Unit systemd
  và script agent qua syntax check; `systemd-analyze --user verify` chỉ cảnh báo
  expected rằng binary ở `~/.local/libexec/` chưa tồn tại trước khi installer
  được chạy.

Các lệnh release gate chuẩn:

```bash
go mod verify
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
go test -tags=integration ./internal/httpapi -count=1
go test -tags=browser ./tests/browser -count=1
podman compose config
podman build -t homelab-dashboard:verify .
```

Sau đó cần smoke test trên homelab thật: Tailscale login viewer/admin, local và
remote Host Shell, container logs/exec, alert firing/resolved gửi ntfy, node
disconnect/reconnect/revoke, history sau restart và import conflict `412`.
Đây là gate riêng, không được coi là đã pass chỉ vì fake socket, browser fixture
hoặc container smoke trong môi trường phát triển đã pass.

## Đánh giá thực tế

- **Homelab cá nhân:** khoảng **9/10 ở mức tính năng**. Các thiếu hụt lớn trong
  roadmap cũ (history, alert, logs, multi-node, export/import) đều đã có
  implementation cụ thể.
- **Production doanh nghiệp:** khoảng **5/10 về kiến trúc**. Security boundary
  đã rõ hơn và dữ liệu có lifecycle, nhưng vẫn thiếu HA, external metrics/log
  backend, notification integrations, SSO/RBAC chi tiết và fleet operations.
- **Trạng thái release hiện tại:** source gần đạt candidate; chỉ nên gọi là
  production-ready sau khi release gates và smoke test host-specific ở trên
  pass trên revision sẽ deploy.
