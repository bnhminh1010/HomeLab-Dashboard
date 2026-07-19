# HomeLab Dashboard

A compact, self-hosted DevOps dashboard for a rootless Podman homelab. The
backend and asset server are one Go binary; the browser UI is plain HTML, CSS
and native JavaScript.

There is **no Node.js toolchain**: no npm, package manifest, bundler, CDN or
runtime download. xterm.js, FitAddon and Chart.js are versioned browser bundles
committed under `static/lib/` and embedded with `go:embed`.

## What it shows

- Host CPU, memory, disk, network, uptime, temperature and process metrics.
- Rootless Podman container status, resource usage and logs.
- Admin-only interactive shell sessions inside running containers.
- An explicitly opened Bash PTY on the host through a least-privilege Go agent;
  the browser cannot choose the shell, command, working directory or environment.
- Persistent service shortcuts with SSRF-safe health probes.
- A responsive dark dashboard and explicit `?demo=1` fixture mode.

## Run with Podman Compose

Prerequisites are Linux with systemd and `/bin/bash`, rootless Podman 5.8+, a Compose provider
(`podman-compose` or the Docker Compose plugin), and a tailnet with MagicDNS
and HTTPS certificates enabled. Enable the user socket and make sure the
rootless runtime directory matches your user ID:

```bash
systemctl --user enable --now podman.socket
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
test -S "$XDG_RUNTIME_DIR/podman/podman.sock"
cp .env.example .env
chmod 600 .env
```

Edit `.env` once, set `XDG_RUNTIME_DIR` to the value printed by
`echo "$XDG_RUNTIME_DIR"`, and set `TS_AUTHKEY` plus the comma-separated
Tailscale login names in `ADMIN_USERS`.

### One-time Host Shell installation

Host Bash cannot safely run from inside the rootless dashboard container. A
small Go agent therefore runs directly as the current Unix user and exposes
only a protected PTY protocol over `$XDG_RUNTIME_DIR/homelab-dashboard/agent.sock`.
Install or update it with Podman; Go, Node.js and npm are not required on the
homelab host:

```bash
./deploy/host-agent/install.sh
systemctl --user status homelab-host-agent.service
```

The installer builds the static agent from the `host-agent-export` Containerfile
stage, copies it to `~/.local/libexec`, and enables its systemd user service.
Run the installer again after pulling a revision that changes the agent. If the
service must survive logouts and the account is not already linger-enabled, an
administrator can run `loginctl enable-linger "$USER"` once.

To enable the toolbar button, set `HOST_SHELL_ENABLED=true` and list the allowed
Tailscale logins in `HOST_SHELL_USERS`. **Every login in `HOST_SHELL_USERS` must
also appear exactly in `ADMIN_USERS`** or the dashboard refuses to start. Then
validate and start everything:

```bash
podman compose config
podman compose up -d --build
```

The Compose project publishes no host port. Open
`https://homelab-dashboard.<your-tailnet>.ts.net` (or the hostname selected in
`TS_HOSTNAME`) from a device in the same tailnet. Tailscale Serve terminates
HTTPS/WSS and supplies the login identity used by the dashboard.

Service probe URLs may use RFC1918 or Tailnet IPs from the configured allowlist.
With userspace Tailscale, validated Tailnet IPs are routed through the local
SOCKS5 listener; the proxy never receives an unvalidated hostname. A MagicDNS
probe hostname must also be resolvable inside the dashboard container.

Useful operations:

```bash
podman compose ps
podman compose logs -f tailscale dashboard
podman compose down
systemctl --user restart homelab-host-agent.service
journalctl --user -u homelab-host-agent.service -f
```

If HTTPS consent, certificate issuance or authentication fails, the Tailscale
container logs contain the actionable Serve error. Never commit the auth key;
keep `.env` mode `0600` and revoke the key if it is exposed.

SQLite data and Tailscale node state live in named volumes and survive a
recreate. The `.env` file is ignored by Git.

## Security boundary

Every Tailnet user can view metrics and logs. Only exact login names listed in
`ADMIN_USERS` can mutate services or open Container Exec. Host Bash additionally
requires `HOST_SHELL_ENABLED=true` and an exact login match in
`HOST_SHELL_USERS`. Requests also require a strict same-origin browser session
and CSRF token.

The mounted rootless Podman socket is as powerful as the Unix account that owns
it. The dashboard therefore never exposes a generic Podman proxy, filters its
own protected containers, accepts only typed log/exec requests, and runs with a
read-only filesystem, dropped capabilities and `no-new-privileges`.

The host agent runs as the Unix account that installs the user service. In this
deployment that is `binhminh`, so Host Shell has the same filesystem, Podman and
`sudo` access as an ordinary local Bash session for `binhminh`; it never starts
as root. Its runtime directory is mode `0700`, its Unix socket is mode `0600`,
and it accepts only the expected peer UID. Opening Host Shell always requires a
visible confirmation. A disconnect terminates the PTY and the UI never
automatically starts a replacement host shell.

To disable this boundary immediately, set `HOST_SHELL_ENABLED=false`, recreate
the dashboard, and optionally run
`systemctl --user disable --now homelab-host-agent.service`.

## Development and verification

The project uses Go tooling only:

```bash
go vet ./...
go test -race ./...
go test -tags=integration ./internal/httpapi
go test -tags=browser ./tests/browser
```

Browser tests use chromedp and a locally installed Chromium/Chrome. Tests use a
fake Podman Unix socket and do not touch the real host socket.
