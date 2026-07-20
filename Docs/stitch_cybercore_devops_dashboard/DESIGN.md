---
name: Graphite Workbench
colors:
  base: '#101010'
  deep: '#090909'
  surface: '#151515'
  card: '#191919'
  border: '#303030'
  text: '#e5e1d9'
  text-muted: '#7e7a73'
  accent: '#d99a3d'
  accent-hover: '#efb35a'
  healthy: '#55bf78'
  warning: '#d7ad45'
  critical: '#e15f59'
typography:
  ui: system sans-serif stack
  mono: JetBrains Mono / Fira Code / Cascadia Code fallback stack
spacing:
  unit: 4px
  page-gutter: 16px
  module-gap: 12px
shape:
  control-radius: 4px
  panel-radius: 6px
---

## Brand and style

Graphite Workbench is an austere operational interface for a private homelab.
It is designed for scanning live status, narrowing the current task through a
workspace rail, and acting without ornamental visual noise. The design rejects
cyberpunk neon, glassmorphism, ambient gradients, decorative glow, and card
lift effects.

The visual reference is a dark operations workbench: graphite layers, tight
rules, restrained amber navigation signals, and semantic health colours. The
reference informs hierarchy and density only; no source branding, copy, or
pixel layout is reproduced.

## Colour system

- **Graphite surfaces:** Base, recess, card, and raised layers use neutral
  graphite steps. A panel is separated by a visible low-contrast rule, not
  blur or shadow.
- **Amber:** Used only for active workspace navigation, keyboard focus,
  primary actions, and intentionally selected controls. It must not colour
  every icon, border, or metric.
- **Semantic status:** Green means healthy, yellow/orange warns, red requires
  action, and neutral grey means unknown or stopped. Semantic colours never
  replace the amber interaction hierarchy.
- **Tokens:** Every application colour is declared in `static/css/tokens.css`.
  Component CSS and JavaScript theme configuration reference those named roles;
  no cyan token or hard-coded neon accent is allowed.

## Typography

- UI headings use a compact system sans-serif role with modest weight and
  uppercase labels only where they improve scanability.
- Metrics, timestamps, resource identifiers, terminal output, and dense
  controls use the monospace role with tabular numerals.
- The application loads no web font. The two roles are defined as resilient
  local/system stacks so the dashboard remains self-hosted and offline-safe.

## Navigation and layout

- **Desktop:** A 216px workspace rail can collapse to a 60px icon rail.
- **Desktop and tablet from 900px:** The 216px rail can collapse to a 60px
  icon rail. Each icon retains an accessible name and a native tooltip.
- **Mobile below 900px:** The rail becomes a keyboard-accessible drawer with a
  backdrop and Escape handling.
- **Workspaces:** Overview, Services, Containers, History, and Alerts are
  distinct views. Terminal remains a global bottom workbench and the rail
  action expands/focuses it rather than creating a duplicate terminal page.
- **Overview triage:** Overview starts with the existing health strip, then
  places a bounded **Needs attention** queue and **Service pulse** ahead of the
  host snapshot. The queue orders alert severity, failed service probes, then
  container runtime issues, so operators see the next useful action before
  dense telemetry. It shows at most five rows while retaining the full Alerts
  workspace as the source of record.
- **History preview:** The Overview resource trend requests the existing
  24-hour system history only when that workspace becomes active. Results are
  cached for five minutes per node, refreshed explicitly when needed, and show
  CPU, RAM, and disk utilisation without creating a second storage/API path.
- **Remote context:** A remote host changes the host snapshot and resource
  trend only. Service probes remain dashboard-local and are labelled as such;
  the UI must not imply remote service inventory where none exists.
- **Information density:** Use 4px-based spacing, 12px module gaps, and a
  single internal dashboard scroller. Dense data may truncate safely, but raw
  identifiers remain available through accessible labels and tooltips.

## Components and interactions

- Panels have matte graphite backgrounds, 1px rules, and 4–6px radii.
- Hover uses only a subtle surface/border change. Never add glow, blur, or
  vertical card movement.
- Every keyboard focusable control has a clear amber `:focus-visible` ring;
  touch layouts provide 44px targets for reachable controls.
- Triage actions reuse the established service URL validation and the existing
  container Logs/Shell terminal path. Shell remains unavailable to viewers,
  protected containers, and non-running containers.
- Progress bars, charts, xterm.js, dialogs, menus, and logs use the same
  graphite/amber token family. Health colours remain semantic inside charts
  and progress states.
- Motion is functional only: short opacity or colour feedback for a state
  change, disabled under `prefers-reduced-motion`.

## Responsive and overflow rules

- `html` and `body` clip horizontal overflow while the dashboard owns vertical
  scrolling. No layout uses `100vw` for content width.
- Long resource IDs are contextualised (node, type, resolved name or shortened
  ID), wrapped safely, and expose their complete raw source on focus/hover.
- History metadata uses a responsive grid so node, resource, resolution,
  storage, and refresh controls cannot overlap or escape the panel.
- Validate the interface at 320, 375, 414, 768, 900, 1280, and wide desktop
  widths before release.
