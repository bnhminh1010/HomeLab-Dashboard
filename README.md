# HomeLab Dashboard

A compact, self-hosted monitoring and operations dashboard for a rootless
Podman homelab. The dashboard backend, API, WebSocket server and frontend
assets are delivered as one Go binary. The UI is plain HTML, CSS and native
JavaScript.

There is **no Node.js toolchain**: no npm, package manifest, bundler, CDN or
runtime download. xterm.js, FitAddon and Chart.js are versioned browser bundles
under `static/lib/` and embedded with `go:embed`. The repository `.gitattributes`
marks that directory as `linguist-vendored`, so GitHub language statistics do
not mistake the local browser dependencies for application JavaScript.

## Capabilities

- Live host CPU, memory, disk, network, load, uptime, temperature and process
  metrics, plus rootless Podman container health and resource usage.
- Tiered SQLite history for host, container and service-uptime data, with UI
  ranges from 1 hour to 90 days, an archived-resource picker and an explicit
  storage quota indicator.
- Stateful alert rules for node availability, CPU, memory, disk, temperature,
  service failures and container health/restarts. Alerts can be acknowledged or
  silenced for 1, 6 or 24 hours.
- Optional ntfy delivery for firing and resolved alerts, including cooldown,
  bounded retries, superseded-delivery handling and an admin test-push action.
- A dedicated, read-only container log viewer with follow, pause, search and
  download. Its browser buffer is bounded to 10,000 lines or 5 MiB, and log
  lookback is limited to 24 hours.
- Admin-only interactive container shells in xterm.js, with PTY resize and
  session limits.
- An explicitly confirmed Bash login shell on the selected host. The local
  host uses a Unix-socket host agent; remote hosts use the outbound node agent.
- Up to five monitoring nodes total: the built-in local node and four remote
  Linux/Podman nodes connected outbound over Tailscale HTTPS/WSS.
- Persistent service shortcuts with SSRF-safe health probes and service uptime
  history.
- Versioned, secret-free dashboard configuration export/import with merge and
  replace previews. Apply is guarded by a revision/`If-Match` precondition so
  a stale preview cannot overwrite newer configuration.
- Viewer/admin authorization from Tailscale identity, same-origin and CSRF
  checks for mutations, bounded audit history, responsive 3/2/1-column UI and
  an explicit `?demo=1` fixture mode.

## Architecture and data lifecycle

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

## Run with Podman Compose

Prerequisites are Linux with systemd and `/bin/bash`, rootless Podman 5.8+, a
Compose provider (`podman-compose` or the Docker Compose plugin), and a tailnet
with MagicDNS and HTTPS certificates enabled. Enable the user socket and make
sure the rootless runtime directory matches the account that owns Podman:

```bash
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
