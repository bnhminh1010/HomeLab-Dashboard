# Migration guide

> Coming from another homelab tool? Here is the honest version: this is not a
> data migration, it is a **re-onboarding**. HostDeck does not import
> Homepage bookmarks, Uptime Kuma status pages or Portainer stacks — those
> store different kinds of state. What you re-create is small, and what you
> import is your existing configuration document (see step 5).
>
> Time to onboard a fresh lab: ~15 minutes.

## 1. From a start page (Homepage / Homarr / Dashy)

Those tools store **bookmarks** (link tiles). This dashboard stores **live
services** (probes + state), so there is nothing to import — re-enter what
you actually watch:

1. Install (see [README quick start](../README.md#quick-start-with-podman-compose)).
2. In **Services**, add each service you care about with an HTTP/HTTPS or
   TCP probe. This is where your bookmarks become *health checks with SLOs*.
3. Optional: use service dependencies in **Topology** to rebuild your mental
   map as a graph instead of a grid.

## 2. From an availability monitor (Uptime Kuma / Gatus)

Kuma monitors endpoints with heartbeat checks; this dashboard probes
**services** and tracks **backup freshness**:

1. Recreate your critical HTTP checks as services (probe type HTTP/HTTPS).
   TCP-only checks (SSH, DB ports) map to the TCP probe type.
2. Alert rules replace Kuma's notification-per-check: create stateful rules
   on node/host/service/container/backup freshness, route to **ntfy** or an
   **HMAC-signed webhook** (see [operations.md](operations.md)).
3. Uptime history: this dashboard keeps 90 days of service uptime
   (transitions + hourly buckets). Your old Kuma uptime chart does not
   transfer.

## 3. From a monitoring stack (Prometheus + Grafana + exporters)

This is the biggest mindset shift, and it is deliberate (see
[ADR-002](adr.md#adr-002-sqlite-3-tier-rollup-instead-of-prometheus)):

| You had | Here |
|---|---|
| Prometheus retention | 3-tier SQLite rollup, 90 days, `HISTORY_QUOTA_BYTES` |
| PromQL ad-hoc queries | Fixed history charts (host/container/service) |
| Grafana dashboards | 8 workspaces: Overview, Services, Containers, Logs, Topology, Terminal, Alerts, Nodes |
| node_exporter per host | built-in host collection (10 s samples) |
| Alertmanager | built-in alert rules + ack/silence + ntfy/webhook |

**What you lose:** arbitrary metric queries, multi-source joins, custom
Grafana panels. **What you gain:** one binary, one SQLite file, no
exporter fleet. If you need PromQL or fleet-scale dashboards, keep the
stack — this tool is not its replacement (see [comparison.md](comparison.md#when-to-use-what)).

## 4. From an admin console (Portainer / Cockpit)

| You had | Here |
|---|---|
| Docker Compose stacks (Portainer) | rootless Podman + `compose.yml` (one command install) |
| Container start/stop/restart (Portainer) | ✅ available on containers (admin) |
| Container logs + console (Portainer) | ✅ logs with regex search + interactive shell (admin) |
| Full host administration (Cockpit) | ⚠️ **No** — no user/service/update management. Only the confirmed host Bash shell for host access |
| Team RBAC | Tailscale identity: viewer/admin only |

Caveat: Portainer manages **stacks as a unit**; this dashboard treats
containers as inventory. Your Docker volumes/named containers can move to
rootless Podman with `podman generate systemd` or a fresh Compose file —
there is no automatic stack importer.

## 5. Import / export configuration

Your runtime configuration (services, alert rules, node list, preferences)
lives in a versioned JSON document behind an `If-Match` revision guard:

1. **Export:** Settings → Export. You get a JSON file with a revision number.
2. **Import (same or new install):** Settings → Import → choose the file.
   Preview shows the diff; the server accepts the write only if the
   `If-Match` revision still matches (concurrent edits are rejected, not
   silently overwritten).
3. Secrets are **not** in the export — `.env` values (`TS_AUTHKEY`,
   `ADMIN_USERS`, webhook secrets, `NTFY_TOPIC`) are configured per install,
   never part of the JSON document.

## 6. Rollback

Upgrades are versioned image tags (see
[README Packages](../README.md#packages)):

```bash
DASHBOARD_IMAGE=ghcr.io/bnhminh1010/homelab-dashboard:vX.Y.Z docker compose up -d
```

SQLite lives in a named volume, so downgrading to a previous tag keeps your
history. If a new schema version is incompatible with an older binary,
restore the pre-upgrade database copy from your own volume backup — this
project documents monitoring *backup freshness*, not the backup of its own
volume, so back up the volume before upgrading.

---

*Not sure it fits your setup? Read [comparison.md](comparison.md) first —
it includes the cases where you should keep your current tool.*
