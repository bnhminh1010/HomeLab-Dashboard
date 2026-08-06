# Architecture Decision Records

> Each record answers **why** a foundational choice was made: the context we
> were in, the decision we took, and the consequences — including the things
> we deliberately gave up. Every claim here maps to code in `internal/` or
> `compose.yml`; nothing is aspirational.

| # | Decision | Status |
|---|---|---|
| [ADR-001](#adr-001-tailscale-identity-is-the-sole-auth-plane) | Tailscale identity is the sole auth plane | Accepted |
| [ADR-002](#adr-002-sqlite-3-tier-rollup-instead-of-prometheus) | SQLite 3-tier rollup instead of Prometheus | Accepted |
| [ADR-003](#adr-003-no-npm-go-embed-static-assets) | No npm — `go:embed` static assets | Accepted |
| [ADR-004](#adr-004-outbound-only-agents-zero-inbound-ports) | Outbound-only agents, zero inbound ports | Accepted |
| [ADR-005](#adr-005-no-public-rest-api-by-design) | No public REST API by design | Accepted |
| [ADR-006](#adr-006-three-static-go-binaries-not-one-monolith) | Three static Go binaries, not one monolith | Accepted |

---

## ADR-001: Tailscale identity is the sole auth plane

**Status:** Accepted

### Context

Homelab dashboards tend to bolt on a second identity system: local users,
passwords, SSO. That doubles the attack surface (credential storage, password
reset flows) and the maintenance burden for a 1–5 node private console. The
existing Tailscale tailnet already authenticates every device and user, and
the dashboard is only reachable through the tailnet anyway.

### Decision

Tailscale identity is the **only** authentication source. There is no
password, no local user database, no fallback. The Tailscale Serve
proxy injects `Tailscale-User-Login` and `Tailscale-User-Name` headers;
the dashboard maps an exact login name to the `viewer` or `admin` role
via `ADMIN_USERS`.

Production trusts those identity headers **only when the request's
immediate peer is loopback** — see `internal/auth/auth.go` (`remoteIsLoopback`).
The Compose topology keeps the dashboard in the Tailscale container's network
namespace and never publishes it, so a direct `0.0.0.0` exposure cannot
fake identity.

### Consequences

- **Positive:** no credential store to leak or rotate; roles follow the
  tailnet; revoking a user from the tailnet removes access immediately.
- **Positive:** the security story is one sentence: "your tailnet identity
  is your login, and the headers are only trusted from loopback."
- **Negative:** a non-Tailscale deployment (plain LAN) has no auth. The
  binary refuses to trust identity headers unless the peer is loopback.
- **Negative:** `ADMIN_USERS` is a static list — there is no per-user
  UI administration.

---

## ADR-002: SQLite 3-tier rollup instead of Prometheus

**Status:** Accepted

### Context

A 1–5 node homelab generates a few hundred time series at most. Standing up
Prometheus + node_exporter + Grafana + Alertmanager just to answer "was my
host up last night?" is exactly the stack sprawl this project set out to
avoid. Prometheus also adds its own retention/disk story, while the operator
wants one predictable file they can back up.

### Decision

History lives in a single SQLite database with a **3-tier rollup**:

| Tier | Resolution | Retention |
|---|---|---|
| Raw | 10 s (host) / 30 s (container) | 48 h |
| Intermediate | 1 min / 5 min | 30 d |
| Long-term | 15 min / 1 h | 90 d |

Rollup is implemented in `internal/history/`. A configurable
`HISTORY_QUOTA_BYTES` (default 2 GiB, range 64 MiB–16 GiB) bounds the
database; at 80% the UI warns, at 100% raw history pauses while live
metrics, alert evaluation and service transitions continue. The history
picker combines live inventory with retained instances, so a resource
remains selectable after it leaves the current snapshot.

### Consequences

- **Positive:** one file, one volume, backup = copy the file. No separate
  metrics store to operate.
- **Positive:** bounded disk by design — the quota is visible in the UI,
  not a silent tail of growth.
- **Negative:** not Prometheus-compatible — cannot be scraped, no
  PromQL. Deliberate; see ADR-005.
- **Negative:** retention is fixed at 90 days per tier; extending to
  years would need a config change, not a feature flag.

---

## ADR-003: No npm — `go:embed` static assets

**Status:** Accepted

### Context

A web UI is unavoidable, but a Node.js toolchain — package manifest, node_modules
tree, bundler, transitive build — is a supply-chain and maintenance liability
that dwarfs the value of a build step for a handful of static files. The
project's identity is "three static Go binaries, no Node.js, no npm, no CDN
runtime dependencies."

### Decision

The UI is hand-written HTML/CSS/vanilla JS under `static/`, embedded into
the binary with `go:embed` (`assets.go`). The three external
browser libraries that genuinely earn their keep — xterm.js, FitAddon,
Chart.js — are versioned, vendored bundles under `static/lib/`, committed
to the repo and shipped from the same origin (no CDN).

`.gitattributes` marks `static/lib/` as `linguist-vendored` so GitHub
language statistics do not count vendored JS as application code.

### Consequences

- **Positive:** `go build ./cmd/dashboard` produces a single self-contained
  binary — no asset pipeline, no cache-busting tooling beyond Go's own
  ETag handling.
- **Positive:** zero npm audit surface; the only third-party code is three
  vendored browser bundles pinned by commit.
- **Negative:** no JS minification/bundling step — files are hand-kept
  readable. Fine at this size, a re-review point if the UI grows much
  larger.

---

## ADR-004: Outbound-only agents, zero inbound ports

**Status:** Accepted

### Context

Remote homelab nodes often sit behind CGNAT, in a VLAN, or on a laptop that
roams. Exposing an inbound agent port means securing a listener on every
node, opening firewall holes, and managing certificates — the opposite of
the "no inbound port" promise. SSH is not a substitute: it widens the
perimeter to a full login surface per node.

### Decision

Every remote node runs a `node-agent` that **dials out** to the dashboard
over Tailscale HTTPS/WSS and keeps the connection alive. The dashboard
never listens for agent connections; it accepts only the tailnet HTTPS
terminated by Tailscale Serve (local host access goes through a
Unix-socket host agent, also dial-out from the dashboard's perspective:
the dashboard connects to the socket, never the reverse).

Remote agents send snapshots and heartbeats every 10 seconds. Enrollment
uses a one-time token with a 10-minute TTL.

### Consequences

- **Positive:** no firewall rules, no reverse proxy, no inbound listener
  on any node — works from CGNAT/VLAN/roaming hosts.
- **Positive:** the blast radius of a compromised node is a dial-out
  client, not an open server.
- **Negative:** requires the tailnet — a node that cannot reach the
  dashboard cannot be monitored (by design; there is no fallback channel).
- **Negative:** first connection latency is bounded by the heartbeat
  interval (10 s), not by an inbound push.

---

## ADR-005: No public REST API by design

**Status:** Accepted

### Context

Observability and ops tools get asked for "an API" early — automation,
integration, exports. Opening one on this console would mean either
exposing the Tailscale identity model outside the tailnet or building a
second auth scheme (API keys), which ADR-001 explicitly rejects. It would
also convert a private console into a service with a public interface to
maintain and secure.

### Decision

There is **no public REST API** and no API-key scheme. All HTTP routes are
same-origin, Tailscale-authenticated, and CSRF/`SameSite`-guarded. The
intended integration paths are **outbound** from the dashboard:

- **ntfy** push notifications for alerts (`NTFY_TOPIC`).
- **Generic webhooks** signed with HMAC-SHA256 (per-webhook secret) for
  alert delivery.

Configuration export/import uses a versioned JSON document with an
`If-Match` revision guard — the sanctioned way to move configuration in
and out, without a query API.

### Consequences

- **Positive:** no public surface to rat-hole, no API-key rotation, no
  auth duplication; the trust model stays one-dimensional.
- **Positive:** automation is still possible — via webhooks for outbound
  events and the export/import document for configuration.
- **Negative:** third-party tools that expect a REST API cannot integrate
  directly. Accepted for a 1–5 node private console; revisit if a real
  use case demands it (an API key scheme would be a new ADR).

---

## ADR-006: Three static Go binaries, not one monolith

**Status:** Accepted

### Context

A single binary is tempting (one artifact, one update), but the runtime
privilege boundaries differ: the dashboard itself is a web UI with a
Podman socket connection; the local host agent provides a Bash shell
on the host; a remote node agent collects host/container metrics on
another machine. Merging them forces the least-privileged component to
carry the most-privileged one's capabilities, or requires an in-binary
privilege split that is harder to reason about than a process boundary.

### Decision

Three binaries, one module:

| Binary | Runs as | Job |
|---|---|---|
| `cmd/dashboard` | rootless Podman container (read-only FS, `cap_drop ALL`, `no-new-privileges`) | API + UI + SQLite + alerts + shell multiplexing |
| `cmd/host-agent` | unprivileged host account | local Bash via Unix socket |
| `cmd/node-agent` | unprivileged account on each remote node | dial-out metrics/container collection |

Each is built from the same Go module, shares `internal/`, and is
released as part of the same versioned artifact. The host agent and node
agent are installed by the same one-command installer.

### Consequences

- **Positive:** the dashboard container carries no host shell capability
  of its own; the host agent is a separate unit the user can disable
  (`systemctl --user disable --now homelab-host-agent.service`) without
  touching the dashboard.
- **Positive:** each agent is compiled to exactly what it needs — the
  node agent does not embed the web UI or the SQLite schema.
- **Negative:** three artifacts to build and version — handled by the
  release workflow, but it is more moving parts than one binary.

---

*Last updated: 2026-08-06. If a decision changes, update this file, do not
delete the record — ADRs are append-only history.*
