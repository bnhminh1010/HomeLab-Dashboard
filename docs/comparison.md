# HomeLab Dashboard vs. the field

> Honest, feature-by-feature comparison against the tools people actually run
> in a homelab. Every checkmark is backed by code in `internal/` — see
> [ADR-005](adr.md#adr-005-no-public-rest-api-by-design) and
> [ADR-002](adr.md#adr-002-sqlite-3-tier-rollup-instead-of-prometheus) for the
> two deliberate "we don't do this" choices.
>
> Last updated: 2026-08-06.

## What this project is

A **Tailscale-only operations console for 1–5 homelab nodes**: observe host,
container, service and backup state; then open the correct container log,
container shell, or an explicitly confirmed host Bash — without inbound
ports, without a metrics stack, without a Node.js toolchain.

It is **not** a replacement for everything below. The last section says
plainly when you should pick something else.

## Positioning

| | **HomeLab Dashboard** | Homepage / Homarr (start pages) | Uptime Kuma / Gatus (availability) | Beszel / Netdata (monitoring) | Portainer / Cockpit (admin) | Grafana + Prometheus (stack) |
|---|---|---|---|---|---|---|
| Core loop | see → understand → **act** | bookmarks | is it up? | is it healthy? | manage it | visualize everything |
| Scale target | 1–5 nodes | 1 box | many endpoints | many hosts | many hosts | fleets |
| Auth model | Tailscale identity only | local per-app | per-app | per-app | per-app / SSO | per-app / SSO |
| Inbound ports needed | **none** | 1+ | 1+ | 1+ (Beszel outbound WS possible) | 1+ | many |
| Runtime footprint | 1 static Go binary | Node.js app | Node.js app | Go (Beszel) / C (Netdata) | Go / C + agents | ≥4 services + exporters |

## Feature matrix

Rows are features implemented in this codebase; cells describe the
closest competitor capability, not marketing claims.

| Feature | HomeLab Dashboard | Start pages | Uptime Kuma | Beszel/Netdata | Portainer/Cockpit | Grafana stack |
|---|---|---|---|---|---|---|
| Live host metrics (CPU/mem/disk/net/load/temp) | ✅ native | — | — | ✅ | ⚠️ (Cockpit) | ✅ via exporters |
| Container health + resource usage | ✅ native (Podman) | — | — | ⚠️ container-level weak | ✅ (Docker-first) | ✅ via cAdvisor |
| Per-service HTTP/HTTPS/TCP probes | ✅ native | ⚠️ link-only | ✅ native | — | — | ✅ via Blackbox |
| **SLO error budgets** (7/30/90-day) | ✅ **first-party** | — | ⚠️ uptime % only | — | — | ⚠️ needs Sloth/Prometheus plugin |
| **Backup freshness monitoring** | ✅ **first-party** | — | ⚠️ different mechanism | — | — | ⚠️ hand-rolled |
| 3-tier SQLite history 90d (no Prometheus) | ✅ **native** | — | ⚠️ status pages | ⚠️ basic agg ~30d | — | ✅ but heavy |
| Read-only container logs + follow/pause/regex | ✅ native | — | — | ⚠️ limited | ✅ | ✅ via Loki |
| **Interactive container shell** | ✅ native (admin) | — | — | — | ✅ | — |
| **Confirmed host Bash** (explicit allowlist + confirm) | ✅ **native** | — | — | — | ⚠️ Cockpit full sysadmin | — |
| **Outbound-only agent, zero inbound port** | ✅ **native** | — | — | ⚠️ Beszel outbound WS | — | — |
| **Tailscale identity as sole auth** | ✅ **native** | — | — | — | — | — |
| Alerts + ack/silence + ntfy + HMAC webhooks | ✅ native | — | ✅ | ⚠️ | ✅ | ✅ Alertmanager (heavier) |
| Config export/import + `If-Match` revision guard | ✅ native | — | — | — | ⚠️ | ⚠️ dashboards as JSON |
| **Demo mode simulating edge cases** (restart loop, stale, partial) | ✅ **native** | ⚠️ static happy-path | — | — | — | — |
| **PARTIAL snapshot degradation** (explicit chip) | ✅ **native** | — | — | — | — | — |
| PWA installable | ✅ | ✅ | ✅ | ✅ | — | — |

## When to use what

Pick **HomeLab Dashboard** when: your lab is 1–5 nodes, you're already on
Tailscale, you want observe→act in one place, and you'd rather not run a
metrics stack for what SQLite can hold.

Pick something else when:

- **Homepage/Homarr** — you want a pretty bookmark launcher first, ops second.
- **Uptime Kuma/Gatus** — you monitor public/external endpoints and need
  many locations and status pages.
- **Beszel/Netdata** — you need deep per-host metric drill-down and
  long-term graphing of many hosts.
- **Portainer** — you manage many hosts' containers with team RBAC and
  stacks as a primary workflow.
- **Cockpit** — you want full host administration (users, services, updates)
  on a single server, not a fleet console.
- **Grafana stack** — you need PromQL, federation, complex dashboards, or
  you're already running it.

## Claims we deliberately do not make

From our own competitive analysis (`Docs/opensource-positioning.md`), these
are weak or non-defensible and we never use them:

- ❌ "First web terminal for homelabs" — Cockpit, Portainer and ttyd did it first.
- ❌ "Works with Tailscale" as a differentiator — anything can sit behind
  Tailscale Serve.
- ❌ "Has a dashboard / has metrics / has uptime monitoring" — everyone has those.

What **is** defensible: the combination — tailnet-only identity, zero inbound
ports, first-party SLO + backup freshness, confirmed host Bash, edge-case
demo mode, PARTIAL degradation — **in one static binary**.
