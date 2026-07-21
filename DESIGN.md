# Homelab Dashboard UI Contract

## Product posture

Homelab Dashboard is an operator workbench, not a marketing site.  It helps a
technical user identify an unhealthy service, understand why, and take a safe
next action.  The interface is dense where data is useful and quiet elsewhere.

## Visual system

- Use the graphite surface palette and amber interaction accent defined in
  `static/css/tokens.css`. Green, yellow, and red are reserved for operational
  status only.
- Use the sans face for names and explanatory copy. Use the monospace face for
  telemetry, timestamps, identifiers, command output, and compact controls.
- Keep the workspace rail, plain panels, divider-led lists, and real tables.
  On narrow screens, tables may become labelled rows.
- Preserve the single inline-SVG stroke family used by the app. Icons must have
  an accessible label or adjacent text when they trigger an action.

## Copy and hierarchy

- State facts directly: "3 services down", "Last check 42s ago", and
  "No probe configured" are preferable to thematic labels or slogans.
- Panel titles use Title Case. Uppercase monospace is only for short states,
  units, table headers, and compact metadata.
- A label above every heading is forbidden. Add a label only when it conveys a
  real scope, time range, or source that cannot be expressed in the title.
- Do not invent uptime, performance, adoption, reliability, or security claims.
  Demo data must remain visibly marked as sample data.

## Anti-patterns

- No emoji as product icons, no service-icon picker, and no decorative empty
  state glyphs.
- No gradients, neon/glow, glass blur, floating decoration, fake browser or
  terminal chrome, decorative shadows, or hover lift.
- Do not nest decorative cards. Prefer one panel with rows and dividers; reserve
  a separate container for a genuinely independent task.
- Do not use a badge when inline status text, a dot, or a count communicates the
  same fact. Colour must never be the only status signal.
- Motion must communicate a state change, use transform/opacity only, respect
  reduced motion, and never animate keyboard focus.

## Acceptance for UI changes

- Verify 320, 375, 414, 768, and desktop widths with no horizontal overflow,
  truncated actions, or inaccessible controls.
- Include before/after screenshots in the PR when layout, typography, colour,
  copy hierarchy, or interaction changes.
- Resolve every critical or major finding from the UI anti-slop review before
  release.
