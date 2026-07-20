---
name: Neon Terminal
colors:
  surface: '#0f1416'
  surface-dim: '#0f1416'
  surface-bright: '#353a3c'
  surface-container-lowest: '#0a0f11'
  surface-container-low: '#171c1e'
  surface-container: '#1b2022'
  surface-container-high: '#252b2d'
  surface-container-highest: '#303637'
  on-surface: '#dee3e5'
  on-surface-variant: '#bcc9cd'
  inverse-surface: '#dee3e5'
  inverse-on-surface: '#2c3133'
  outline: '#879397'
  outline-variant: '#3d494c'
  surface-tint: '#60d5f2'
  primary: '#d4f5ff'
  on-primary: '#003640'
  primary-container: '#6ee2ff'
  on-primary-container: '#006475'
  inverse-primary: '#00687a'
  secondary: '#c3c6d3'
  on-secondary: '#2c303a'
  secondary-container: '#454954'
  on-secondary-container: '#b5b8c5'
  tertiary: '#ffedce'
  on-tertiary: '#3f2e00'
  tertiary-container: '#fecc59'
  on-tertiary-container: '#745600'
  error: '#ffb4ab'
  on-error: '#690005'
  error-container: '#93000a'
  on-error-container: '#ffdad6'
  primary-fixed: '#acecff'
  primary-fixed-dim: '#60d5f2'
  on-primary-fixed: '#001f26'
  on-primary-fixed-variant: '#004e5c'
  secondary-fixed: '#dfe2ef'
  secondary-fixed-dim: '#c3c6d3'
  on-secondary-fixed: '#181b25'
  on-secondary-fixed-variant: '#434751'
  tertiary-fixed: '#ffdf9d'
  tertiary-fixed-dim: '#f0c04e'
  on-tertiary-fixed: '#251a00'
  on-tertiary-fixed-variant: '#5b4300'
  background: '#0f1416'
  on-background: '#dee3e5'
  surface-variant: '#303637'
typography:
  headline-lg:
    fontFamily: Inter
    fontSize: 24px
    fontWeight: '700'
    lineHeight: 32px
    letterSpacing: -0.02em
  headline-md:
    fontFamily: Inter
    fontSize: 18px
    fontWeight: '600'
    lineHeight: 24px
    letterSpacing: -0.01em
  body-md:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: '400'
    lineHeight: 20px
  code-lg:
    fontFamily: jetbrainsMono
    fontSize: 16px
    fontWeight: '500'
    lineHeight: 24px
  code-md:
    fontFamily: jetbrainsMono
    fontSize: 13px
    fontWeight: '400'
    lineHeight: 18px
  label-caps:
    fontFamily: jetbrainsMono
    fontSize: 11px
    fontWeight: '700'
    lineHeight: 16px
    letterSpacing: 0.05em
rounded:
  sm: 0.125rem
  DEFAULT: 0.25rem
  md: 0.375rem
  lg: 0.5rem
  xl: 0.75rem
  full: 9999px
spacing:
  unit: 4px
  container-padding: 16px
  gutter: 12px
  card-gap: 8px
---

## Brand & Style

This design system targets high-performance DevOps environments where information density and rapid error detection are critical. The visual narrative combines a "Cyberpunk Terminal" aesthetic with modern Glassmorphism.

The personality is technical, high-fidelity, and authoritative. It utilizes deep layering to separate system background from interactive modules. The style is defined by:
- **Glassmorphism:** Modules use semi-transparent backgrounds with backdrop-blur(4px) to maintain legibility over ambient glow effects.
- **Cybernetic Accents:** Thin, high-contrast borders and neon "glow" states indicate health and activity.
- **High-Density:** Minimal whitespace between functional elements to maximize data visualization real-estate.

## Colors

The palette is rooted in a deep navy/cyan base to reduce eye strain during long-duration monitoring.

- **Primary Accent:** Cyan (#6EE2FF) is used for active states, primary actions, and "Healthy" system pulses.
- **Status Semantic Palette:** Standardized green, yellow, orange, and red are used for log severity and pipeline health.
- **Glass Surfaces:** Containers use a translucent version of the secondary color with an additive 1px border using the primary color at low opacity.

## Typography

This design system uses a dual-font strategy to balance readability with technical flavor.

- **Inter (Sans-Serif):** Used for all UI labels, navigation, and structural headers. It provides a clean, neutral balance to the complex data visualization.
- **JetBrains Mono (Monospace):** Reserved for metrics, timestamps, logs, and terminal outputs. The monospaced nature ensures that columns of numbers remain perfectly aligned for quick scanning.
- **Scaling:** On mobile devices, `headline-lg` should scale down to 20px. Data density is prioritized over large display type.

## Layout & Spacing

The layout utilizes a **fluid grid** model optimized for wide-screen monitoring setups.

- **Grid:** A 12-column grid system is used for the desktop dashboard.
- **Density:** Elements use a strict 4px baseline grid. Padding within cards is kept at a tight 12px or 16px to ensure the maximum number of data points are visible above the fold.
- **Breakpoints:**
  - **Desktop (1280px+):** 12 columns, 16px margins.
  - **Tablet (768px - 1279px):** 6 columns, 12px margins.
  - **Mobile (<767px):** 2 columns or single column stack, 8px margins.

## Elevation & Depth

Hierarchy is established through **Glassmorphism and Tonal Layers** rather than traditional shadows.

1.  **Background Layer:** Solid `#0a0e17`.
2.  **Card Layer:** Semi-transparent base with `backdrop-filter: blur(12px)`. This creates a sense of "suspended" modules.
3.  **Active/Hover State:** Elements do not lift; instead, they increase the intensity of their cyan border and apply a subtle `box-shadow: 0 0 10px rgba(110, 226, 255, 0.3)` to simulate a neon glow.
4.  **Overlays/Modals:** Higher transparency with a darker backdrop dimming (scrim) to focus the user on the task.

## Shapes

The shape language is "Soft-Industrial."

- **Radius:** A consistent 0.25rem (4px) radius is applied to cards, buttons, and input fields. This prevents the UI from feeling too sharp/aggressive while maintaining a precise, engineered appearance.
- **Interactive Elements:** Buttons and tags may use a slightly more aggressive rounding for distinction, but the primary structural units (containers) must remain at the `Soft` level.

## Components

### Buttons
- **Primary:** Solid Cyan (#6EE2FF) with dark text. No border. On hover, apply an external glow.
- **Ghost:** Transparent background, 1px cyan border. Used for secondary dashboard actions.

### Cards & Modules
- **Base:** Glassmorphic background with a 1px border (`rgba(110, 226, 255, 0.1)`).
- **Header:** Integrated into the card with a bottom 1px divider.

### Input Fields
- **Style:** Dark backgrounds with a bottom-only 1px cyan border that expands to a full border on focus.
- **Font:** Use Monospace for values, Sans-Serif for placeholders.

### Chips / Status Badges
- **Indicator:** Small circular "pulse" dot next to the label.
- **Background:** Low-opacity version of the status color (e.g., Green at 10% alpha).

### Data Visualizations
- **Charts:** Use thin 1.5px lines for line charts. Use a vertical gradient fill (status color to transparent) for area charts.
- **Grids:** Monospaced numbers, tight row heights (32px), alternate row shading for readability.