# Performance benchmarks

> Measured on a real deployment, not synthetic. Conditions and timestamps are
> included so the numbers are reproducible — take them as "typical for a
> single-node private console", not worst case.

## Measurement conditions

- **Host:** Debian 13 (kernel `6.12.95+deb13-amd64`), 7.5 GiB RAM, x86-64
- **Deployment:** single node (local), dashboard running as a native binary
  (not containerized in this sample), host agent as a systemd user unit
- **Workload at sample time:** 9 containers (Immich server+postgres+redis,
  Jellyfin, AdGuard Home, Glance, Excalidraw, Vaultwarden, CRW),
  6 monitored services, 10 alert rules, 0 remote nodes
- **Uptime at sample time:** 13 h 11 m since dashboard start
- **Sample time:** 2026-08-06 ~19:23 Asia/Saigon
- All values from `/proc` and `podman stats`; nothing was installed or
  modified to produce this document.

## Memory (RSS, `ps`/`/proc`)

| Component | RSS | Swap | Notes |
|---|---:|---:|---|
| `dashboard` (binary) | **40.9 MB** | 0 | `VmRSS` 40,868 kB, `VmSwap` 0; cgroup `memory.current` 66 MB including page cache |
| `homelab-host-agent` | **8.6 MB** | 0 | systemd user unit, idle at sample time |
| `node-agent` / `smart-agent` | — | — | not running: single-node deployment uses local collection only |

Total steady-state: **≈ 50 MB RSS** for the whole console on this node.
`%MEM` of the dashboard on this host: 0.5% of 7.5 GiB.

## CPU

| Component | CPU |
|---|---|
| `dashboard` | **2.4 %** average since start (13 h window, includes collection + SQLite rollup + WebSocket fan-out) |
| `homelab-host-agent` | 0.0 % idle |

Collection cadence: host samples every 10 s, containers every 30 s, services
every 60 s (see [architecture](../README.md#architecture-and-data-lifecycle)).
The dashboard is event-driven; idle CPU approaches zero between polls.

## Storage

| Item | Size at sample |
|---|---:|
| `data/dashboard.db` (SQLite, 3-tier history) | **29.7 MB** after 13 h |
| History quota (`HISTORY_QUOTA_BYTES`) | 2 GiB default (64 MiB–16 GiB configurable) |

At this growth rate a single node stays well under the 2 GiB quota for the
full 90-day retention window; the quota exists to bound worst-case fleets,
and the UI warns at 80% and pauses raw history at 100% while live metrics,
alert evaluation and service transitions continue.

## Scaling notes (1–5 nodes)

- Each **remote node** adds one `node-agent` process (dial-out WSS, snapshots
  every 10 s). Expect the same order of magnitude as the host agent
  (≈ 5–15 MB RSS) per remote node.
- SQLite growth scales with series count: host + container rows dominate;
  5 nodes × 10 containers each is still a small fraction of the 2 GiB quota.
- The dashboard fan-out is per connected browser session; a single operator
  (the intended use) sees negligible CPU impact from the WebSocket layer.

*Method note: `%CPU` in `ps` is lifetime-average CPU time ÷ wall time. The
2.4 % figure therefore includes startup and any burst activity across the
13-hour window; instantaneous idle is lower.*
