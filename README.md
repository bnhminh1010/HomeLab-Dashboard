<h1 align="center">HomeLab Dashboard</h1>

<p align="center"><strong>A quiet operations workbench for a Tailscale-connected, rootless Podman homelab.</strong></p>

<p align="center"><code>Go</code> · <code>SQLite</code> · <code>Podman</code> · <code>Tailscale</code> · <code>WebSocket</code> · <code>Vanilla JS</code></p>

<p align="center"><sub>One binary. No Node.js. No npm. No CDN runtime dependencies.</sub></p>

<p align="center"><a href="#quick-start-with-podman-compose">Quick start</a> · <a href="#architecture-and-data-lifecycle">Architecture</a> · <a href="#security-boundary">Security</a> · <a href="#operations-and-troubleshooting">Operations</a> · <a href="#development-and-verification">Development</a></p>

> [!NOTE]
> This is an intentionally small, single-instance operations console—not a
> replacement for Grafana, Prometheus, Loki, or an enterprise control plane.
> It favors direct visibility and safe everyday action on a personal homelab.

| Observe | Act | Retain | Trust |
|---|---|---|---|
| Host, services, containers, TLS, backups and up to five nodes | Logs, container shells and explicitly confirmed host Bash | Tiered metrics, SLOs, alerts and operational events for up to 90 days | Tailscale identity, viewer/admin roles, CSRF and same-origin mutation guards |

## Capabilities

### See the state of the lab

- Live CPU, memory, disk, network, load, uptime, temperature and process
  metrics; rootless Podman health and resource usage; and a graphite workbench
  that adapts from desktop to mobile.
- Tiered SQLite history for host, container and service uptime across
  **1 hour–90 days**, including an archived-resource picker and a visible
  storage-quota state.
- Local and remote fleet capacity, HTTPS certificate observations, backup
  freshness, manually curated service dependencies and a 90-day change
  timeline with markers aligned to the selected history window.
- Per-service availability objectives with 7/30/90-day windows and an explicit
  error budget once probe observations exist.

### Respond without leaving the dashboard

- Persistent service shortcuts with SSRF-safe probes, read-only container logs
  with follow/pause/search/download, and admin-only interactive container
  shells in xterm.js.
- An explicitly confirmed Bash login shell on the selected host. It requires an
  admin identity, the dedicated host-shell allowlist and a visible confirmation;
  local sessions use a Unix-socket host agent while remote sessions use an
  outbound node agent.
- Stateful alert rules for node availability, host resources, services,
  containers and backup freshness. Incidents can be acknowledged or silenced
  for 1, 6 or 24 hours.
- Optional ntfy delivery for firing and resolved alerts, with cooldowns,
  retries, superseded-delivery handling and an admin test push.

### Keep configuration and access deliberate

- One local node plus up to four remote Linux/Podman nodes, connected outbound
  over Tailscale HTTPS/WSS—no inbound agent port.
- Versioned, secret-free export/import with merge/replace previews and a
  revision/`If-Match` guard against stale writes.
- Viewer/admin authorization from Tailscale identity, same-origin and CSRF
  checks for mutations, plus bounded audit history.

There is **no Node.js toolchain**: no npm, package manifest, bundler, CDN or
runtime download. xterm.js, FitAddon and Chart.js are versioned browser bundles
under `static/lib/` and embedded with `go:embed`. The repository `.gitattributes`
marks that directory as `linguist-vendored`, so GitHub language statistics do
not mistake the local browser dependencies for application JavaScript.

## Architecture and data lifecycle

```text
Tailnet browser
      │ HTTPS / WSS (Tailscale Serve)
      ▼
┌───────────────────────────────────────────────────────┐
│ Dashboard container                                   │
│ Go API + WebSocket + embedded static UI + SQLite       │
└───────┬─────────────────────┬─────────────────────┬───┘
        │                     │                     │
        ▼                     ▼                     ▼
  Podman socket         Host-agent PTY       Tailscale WSS
  containers/logs       local Bash           remote node agents
                                                    │
                                                    ▼
                                            Podman + host metrics
```

The dashboard has no published host port. Tailscale Serve terminates the
tailnet HTTPS/WSS connection, while the Go process listens only on loopback in
the shared network namespace. Remote agents always dial out to the dashboard.

The dashboard collects the local node every 2 seconds. A monitoring pipeline
writes host samples every 10 seconds, container samples every 30 seconds and
service observations every minute. Remote node agents send host/container
snapshots and heartbeats every 10 seconds.

History is rolled up and retained in tiers:

| Data | Raw | Intermediate | Long-term |
|---|---:|---:|---:|
| Host | 10 s for 48 h | 1 min for 30 d | 15 min for 90 d |
| Container | 30 s for 48 h | 5 min for 30 d | 1 h for 90 d |
| Service uptime | transitions | hourly uptime buckets | 90 d |

The history resource picker combines the live inventory with retained
container instances and service series, so a resource can still be selected
after it disappears from the current snapshot. Catalog queries are bounded to
the 500 most recently seen entries per resource type for the selected node.

`HISTORY_QUOTA_BYTES` defaults to 2 GiB and accepts values from 64 MiB through
16 GiB. At 80% the UI reports a warning. At 100%, new raw host/container
history is paused while live metrics, alert evaluation and service transition
recording continue. Retention and rollups run at startup and every minute;
operational cleanup runs at startup and daily. Alert events, resolved alert
states and audit records are retained for 90 days, completed notification
deliveries for 30 days, and active incidents/queued delivery work are preserved.

The latest TLS certificate observation is refreshed at startup and every 12
hours for each configured HTTPS service. It shares the service-probe CIDR
allowlist and optional Tailscale SOCKS route, so the certificate feature cannot
be used as a separate network scanner. A backup job may optionally write a
small status JSON file; the dashboard keeps only its newest status per job and
evaluates its configured freshness interval.

The SQLite database and Tailscale node state live in named volumes and survive
a Compose recreate. History is local to this dashboard instance; it is not a
Prometheus-compatible long-term archive.

Metrics frames that exceed their transport budget identify the omitted source
lists explicitly. The UI displays a visible **PARTIAL** chip and degrades the
overview instead of treating an incomplete inventory as healthy. The monitoring
pipeline treats each truncated source as stale, so omitted resources do not
produce false history or false alert recovery.

Embedded assets use content ETags. Stable local vendor URLs under `/lib/` have
a one-day cache lifetime with `must-revalidate`; application assets use a
one-hour revalidation window. This keeps repeat loads small while ensuring a
browser revalidates the committed vendor bundles after an upgrade.

## Quick start with Podman Compose

Prerequisites: Linux with systemd and `/bin/bash`, rootless Podman, a Compose
provider (`podman-compose` or the Docker Compose plugin), and a tailnet with
MagicDNS and HTTPS certificates enabled. Clone the repository, enable the user
socket and make sure the runtime directory belongs to the account that owns
Podman:

```bash
git clone https://github.com/bnhminh1010/HomeLab-Minh.git
cd HomeLab-Minh
systemctl --user enable --now podman.socket
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
test -S "$XDG_RUNTIME_DIR/podman/podman.sock"
cp .env.example .env
chmod 600 .env
```

Edit `.env`, set `XDG_RUNTIME_DIR` to the value printed by
`echo "$XDG_RUNTIME_DIR"`, set `TS_AUTHKEY`, and list the exact comma-separated
Tailscale login names allowed to administer the dashboard in `ADMIN_USERS`.
Then validate and start the project:

```bash
podman compose config
podman compose up -d --build
podman compose ps
```

> [!IMPORTANT]
> Do not publish the dashboard directly on `0.0.0.0` while
> `TRUST_TAILSCALE_HEADERS=true`. Compose deliberately exposes no host port;
> access it through Tailscale Serve instead.

The Compose project publishes no host port. Open
`https://homelab-dashboard.<your-tailnet>.ts.net` (or the hostname selected in
`TS_HOSTNAME`) from a device in the same tailnet. Tailscale Serve terminates
HTTPS/WSS and supplies the identity headers used by the dashboard.

Service probe URLs may use RFC1918, CGNAT/Tailnet or ULA addresses from the
configured allowlist. With userspace Tailscale, validated Tailnet IPs are
routed through the local SOCKS5 listener. A MagicDNS probe hostname must also
resolve inside the dashboard container.

### Local host Bash

Host Bash cannot provide normal host behavior from inside the read-only
dashboard container. A small Go agent therefore runs as the current Unix user
and exposes a protected PTY protocol over
`$XDG_RUNTIME_DIR/homelab-dashboard/agent.sock`.

Install or update it with Podman; the host does not need Go, Node.js or npm:

```bash
./deploy/host-agent/install.sh
systemctl --user status homelab-host-agent.service
```

Set `HOST_SHELL_ENABLED=true` and list allowed Tailscale logins in
`HOST_SHELL_USERS`. Every host-shell login must also appear in `ADMIN_USERS`,
otherwise startup fails. Recreate the dashboard after changing these values:

```bash
podman compose up -d --build --force-recreate dashboard
```

Run the installer again after pulling a revision that changes the host agent.
If it must survive logouts, an administrator may run
`loginctl enable-linger "$USER"` once.

### ntfy notifications

Alert evaluation and the in-dashboard incident list work even when ntfy is
disabled. To enable delivery, set the URL and topic together:

```dotenv
NTFY_URL=https://ntfy.sh
NTFY_TOPIC=my-private-homelab-topic
```

For a protected topic, create a host file readable only by the Podman account
and point `NTFY_TOKEN_FILE` at its absolute path:

```bash
install -d -m 0700 "$HOME/.config/homelab-dashboard"
install -m 0600 /dev/null "$HOME/.config/homelab-dashboard/ntfy-token"
# Write the token into the file without committing it.
```

```dotenv
NTFY_TOKEN_FILE=/home/you/.config/homelab-dashboard/ntfy-token
```

Leave `NTFY_TOKEN_FILE` empty for anonymous delivery. The Compose file mounts
the configured token at a fixed read-only path; the token is never exported in
dashboard configuration. After recreating the dashboard, use **Settings → ntfy
delivery → Test push** as an administrator.

### Backup status reports

The dashboard does not run or credential backup software. Any local backup job
can publish its most recent result by atomically replacing a small JSON file on
the host. Set `BACKUP_STATUS_FILE` in `.env` to that host path, then recreate
the dashboard. The Compose file exposes it read-only inside the container.

For a remote node, set `BACKUP_STATUS_FILE=/absolute/path/report.json` in
`~/.config/systemd/user/homelab-node-agent.service.d/backup.conf`, then run
`systemctl --user daemon-reload && systemctl --user restart homelab-node-agent`.
The complete JSON contract and an atomic writer example are in
[`deploy/node-agent/README.md`](deploy/node-agent/README.md#backup-status-reports).

### Add a remote node

The dashboard supports four remote nodes in addition to `local`. A remote node
needs Linux, systemd, `/bin/bash`, rootless Podman and connectivity to the
dashboard's Tailscale HTTPS URL. It opens an outbound connection; no inbound
agent port is exposed.

1. Open **Settings → Enrolled nodes → Create token**. The one-time token expires
   after 10 minutes and is consumed on first successful enrollment.
2. On the remote host, check out the same repository and run the installer as
   the unprivileged account that owns the rootless Podman socket:

   ```bash
   ./deploy/node-agent/install.sh \
     https://homelab-dashboard.<your-tailnet>.ts.net \
     "rack-node-1"
   ```

3. Paste the token when prompted. It is not echoed. The installer builds and
   extracts the static `node-agent-export` binary, writes credentials to
   `~/.config/homelab-node-agent/credentials.json` with mode `0600`, and enables
   `homelab-node-agent.service`.
4. Confirm the node becomes online in the selector and test its metrics, logs
   and shell actions.

The agent reconnects with exponential backoff capped at 30 seconds. Revoking a
node disconnects it, invalidates its credential, removes its active alert
runtime and resets a matching default-node preference to `local`. Full install,
prebuilt-binary and uninstall instructions are in
[`deploy/node-agent/README.md`](deploy/node-agent/README.md).

### Export and import dashboard configuration

Administrators can export one deterministic JSON document from Settings. The
document is limited to 1 MiB and contains only:

- service definitions;
- alert rules;
- service SLO target/window policies and manual service-dependency edges;
- UI preferences; and
- display metadata for nodes that are already enrolled.

It never contains node credentials, enrollment tokens, ntfy tokens, sessions,
history, audit records or alert runtime. Import cannot enroll a node; node
metadata is applied only to an existing node ID. `merge` preserves entries not
present in the file, while `replace` may delete portable services/rules after
the preview shows the exact change set.

Every apply must use the revision returned by preview in `If-Match`. If another
admin or process changes portable configuration between preview and apply, the
API returns `412 Precondition Failed`; preview again before retrying. A missing
revision returns `428 Precondition Required`.

## Security boundary

### Access at a glance

| Actor | Allowed actions |
|---|---|
| **Viewer** | Read metrics, history, alerts and container logs. |
| **Admin** | Mutate services/rules/configuration, acknowledge or silence incidents, manage nodes, open container shells and test ntfy. |
| **Host shell user** | An admin explicitly listed in `HOST_SHELL_USERS`, after an in-UI confirmation. The login shell has the Unix account's normal power. |

Every authenticated Tailnet user can view metrics and container logs. Only
exact login names in `ADMIN_USERS` can mutate configuration, acknowledge or
silence alerts, create/revoke nodes, open Container Shell, test ntfy or import
configuration. Host Bash also requires the explicit host-shell allowlist and a
visible confirmation.

Production trusts Tailscale identity headers only when the dashboard listens
on loopback and the request's immediate peer is loopback. The Compose topology
keeps the dashboard in the Tailscale container's network namespace and does not
publish it directly. Do not expose the binary on `0.0.0.0` while trusting
identity headers.

The mounted rootless Podman socket is as powerful as its Unix owner. The
dashboard therefore hides its protected containers, accepts typed log/exec
operations rather than arbitrary commands, and runs with a read-only
filesystem, dropped capabilities and `no-new-privileges`.

The local host agent and every remote node agent run as the account that
installed their systemd user service, never as root. **Host Shell intentionally
has that account's normal filesystem, network, rootless Podman and optional
`sudo` access.** The remote unit does not use `ProtectSystem`, `PrivateTmp` or
`NoNewPrivileges`, because those controls would make the advertised host login
shell restricted or read-only. A disconnect terminates active streams; host
shells are never resumed automatically.

To immediately disable local host access, set `HOST_SHELL_ENABLED=false`,
recreate the dashboard and optionally stop the local agent:

```bash
systemctl --user disable --now homelab-host-agent.service
```

To remove a remote shell path, revoke that node in Settings and stop or
uninstall its node agent.

## Operations and troubleshooting

Useful commands on the dashboard host:

```bash
podman compose ps
podman compose logs -f tailscale dashboard
systemctl --user status homelab-host-agent.service
journalctl --user -u homelab-host-agent.service -f
test -S "$XDG_RUNTIME_DIR/homelab-dashboard/agent.sock"
```

Useful commands on a remote node:

```bash
systemctl --user status homelab-node-agent.service
journalctl --user -u homelab-node-agent.service -f
systemctl --user restart homelab-node-agent.service
test -S "$XDG_RUNTIME_DIR/podman/podman.sock"
```

Common failure modes:

- **`podman: not found` with a container path prompt** — that is Container
  Shell, so commands run inside that container. Use **Host Shell** when you need
  the host's `podman`, filesystem or user environment.
- **`Unable to reserve a host shell session` on `local`** — verify
  `HOST_SHELL_ENABLED`, that the login is in both allowlists, the host-agent
  service is active, and its Unix socket exists under the same
  `XDG_RUNTIME_DIR` mounted by Compose. Re-run `deploy/host-agent/install.sh`
  after an agent update and inspect its journal.
- **The same error on a remote node** — verify the selected node is online and
  inspect `homelab-node-agent.service`. A revoked or deleted credential must be
  enrolled again; an expired enrollment token cannot be reused.
- **Remote node is offline** — confirm Tailscale DNS/HTTPS from that host,
  system clock accuracy, and the agent journal. The dashboard keeps the last
  known snapshot marked stale while the agent reconnects.
- **History says `RAW PAUSED`** — increase `HISTORY_QUOTA_BYTES` within the
  supported range or allow retention to age out old tiers. Live monitoring and
  alerts continue while raw history is paused.
- **ntfy test fails** — configure URL and topic together, use an absolute token
  file path, confirm file permissions/readability and inspect dashboard logs.
- **Old tab shows offline after a dashboard restart** — the UI normally renews
  its in-memory browser session on reconnect; if the browser blocks cookies or
  storage, reload the page through the Tailscale HTTPS URL.
- **HTTPS, certificate or identity failure** — inspect the Tailscale container
  logs. Never commit the auth key; keep `.env` mode `0600` and revoke an exposed
  key.

## Development and verification

The project uses Go tooling only:

```bash
go mod verify
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
go test -tags=integration ./internal/httpapi -count=1
go test -tags=browser ./tests/browser -count=1
```

Browser tests use chromedp and a locally installed Chromium/Chrome. Unit tests
use fake Podman/agent Unix sockets and do not touch the real host socket. A
release should also validate `podman compose config`, build the image, and run
an end-to-end smoke test on the target homelab because Tailscale identity,
systemd user services and rootless Podman are host-specific.
