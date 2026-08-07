# Changelog

All notable changes to this project are documented in this file, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) conventions. This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- *(nothing yet)*

## [0.1.0] - 2026-08-07

First public cut: Tailscale-only operations console in Go, with no inbound ports.

### Added

- **Core console**: Go-only homelab operations dashboard — no Node.js runtime, no npm, no CDN dependencies
- **Monitoring workbench**: historical multi-node monitoring with 3-tier SQLite rollups (10s × 48h, 1m × 30d, 15m × 90d) and configurable quota
- **Actionable overview**: production operations brief with health overview and widget customization
- **Interactive shells**: container exec shell and confirmed host Bash, both gated by Tailscale identity
- **Logs**: optional Loki + Vector search, regex search with result navigation
- **Alerts**: configurable alerts with HMAC webhook delivery provider
- **Containers**: multi-mount collector and SMART disk polling host-helper (Phase B)
- **Workspaces**: multi-workspace customization and light theme
- **Platform**: multi-arch container images published to GHCR and Docker Hub
- **Adoption**: public demo mode (`?demo=1`) with neutralized identity, demo installer, TCP probes
- **Docs**: operations guide, architecture decision records, comparison matrix, migration guide, measured benchmarks
- **Storefront**: HostDeck landing page with live demo, comparison matrix, security, quickstart with copyable installer command, docs index; custom domain `hostdeck.thinkai.id.vn`

### Changed

- Repositioned as a Tailscale-only operations console; corrected claims in docs
- Redesigned UI as a graphite workspace; replaced SYSTEM STATS eyebrow with a semantic accessibility title
- Neutralized demo identity before public screenshots (`admin@tailnet`, `home-n-01`)
- Stopped tracking local design documentation in the repository

### Fixed

- UI: backdrop blur reduced 12px → 4px, cyan-glow spread 20px → 8px, double-glow on brand mark removed, layout root max-width `100vw` → `100%`, gap-based spacing in container lists
- Browser: recent-change click race eliminated; viewer widget controls and settings cancel restored
- CI: Go toolchain and `x/` deps patched to clear govulncheck; Loki query bounds refreshed during retries; Vector fixture discovery made deterministic; Pages artifacts isolated across reruns; checks gated after browser regressions

[Unreleased]: https://github.com/bnhminh1010/homelab-dashboard/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/bnhminh1010/homelab-dashboard/releases/tag/v0.1.0
