## 🔍 Dashboard Review & Future Improvements

### Đã chạy tốt
- [x] System metrics realtime (CPU, RAM, Disk, Temp)
- [x] Container stats (9 containers detected)
- [x] Services list (8 services, CRUD API)
- [x] Tailscale auth + HTTPS
- [x] Frontend hiển thị ổn định
- [x] RAM chỉ ~30MB

### Cần cải thiện

#### 1. Web Terminal — chỉ đọc log, chưa có bash
Hiện tại terminal chỉ chạy `podman logs -f <container>` → xem log realtime.

**Chưa có:** `podman exec -it <container> sh` → shell tương tác.

Lý do: code trong `internal/terminal/manager.go` có vẻ chỉ implement log streaming, chưa có exec shell.

Cần thêm:
- Mode "exec" bên cạnh "logs" (hiện tại chỉ có `CreateFromContainer`)
- PTY allocation qua `podman exec -it`
- Resize event từ xterm.js gửi xuống

#### 2. CSRF session chỉ dùng 1 lần
Mỗi session chỉ POST được 1 service rồi CSRF token hết hiệu lực.
→ Cần debug lại logic CSRF validation.

#### 3. Chạy persistent
Hiện tại chạy bằng tay `./dashboard`. Cần systemd service:

```bash
# ~/.config/systemd/user/dashboard.service
[Unit]
Description=HomeLab Dashboard
After=network.target podman.socket

[Service]
Type=simple
ExecStart=/home/binhminh/HomeLab-Minh/dashboard
Environment=DASHBOARD_ADDR=0.0.0.0:8081
Environment=DATA_PATH=/home/binhminh/HomeLab-Minh/data/dashboard.db
Environment=ADMIN_USERS=admin@local
Environment=HOST_PROC_PATH=/proc
Environment=HOST_SYS_PATH=/sys
Environment=HOST_ROOT_PATH=/
Environment=PODMAN_SOCKET=/run/user/1000/podman/podman.sock
Environment=TRUST_TAILSCALE_HEADERS=false
Restart=always

[Install]
WantedBy=default.target
```

#### 4. Icon không hiển thị trên services
Field `icon` trong API response có vẻ ko được map đúng JSON tag.
→ Kiểm tra `model.Service` struct vs API response.

#### 5. TestSvc còn sót
Xoá: `curl -X DELETE ... /api/v1/services/svc_VhajTksTKFB-WbGK`
