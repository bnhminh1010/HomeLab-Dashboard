export const OVERVIEW_WIDGET_CATALOG = Object.freeze([
  { id: "overview-attention", label: "Needs Attention", defaultSize: "full", required: true },
  { id: "overview-trend", label: "Resource Trend", defaultSize: "medium" },
  { id: "overview-recent-changes", label: "Recent Changes", defaultSize: "small" },
  { id: "system-card", label: "System Snapshot", defaultSize: "medium" },
  { id: "overview-service-pulse", label: "Probe Coverage", defaultSize: "small" },
  { id: "overview-quick-launchpad", label: "Quick Launchpad", defaultSize: "full", defaultHidden: true },
  { id: "overview-service-groups", label: "Service Groups", defaultSize: "medium", defaultHidden: true },
  { id: "overview-top-containers", label: "Top Containers", defaultSize: "medium", defaultHidden: true },
  { id: "overview-storage-pools", label: "Storage Pools", defaultSize: "small", defaultHidden: true },
  { id: "overview-operator-notes", label: "Operator Notes", defaultSize: "small", defaultHidden: true },
]);

export const OVERVIEW_WIDGET_ORDER = OVERVIEW_WIDGET_CATALOG.map((widget) => widget.id);
export const OVERVIEW_WIDGET_LABELS = Object.freeze(Object.fromEntries(OVERVIEW_WIDGET_CATALOG.map((widget) => [widget.id, widget.label])));
export const OVERVIEW_WIDGET_DEFAULT_SIZES = Object.freeze(Object.fromEntries(OVERVIEW_WIDGET_CATALOG.map((widget) => [widget.id, widget.defaultSize])));
export const OVERVIEW_WIDGET_DEFAULT_HIDDEN = Object.freeze(OVERVIEW_WIDGET_CATALOG.filter((widget) => widget.defaultHidden).map((widget) => widget.id));
export const OVERVIEW_WIDGET_SIZES = Object.freeze(["small", "medium", "full"]);

export function normalizeOverviewPreferences(preferences = {}) {
  const hidden = new Set(Array.isArray(preferences.hiddenOverviewWidgets) ? preferences.hiddenOverviewWidgets : []);
  for (const widget of OVERVIEW_WIDGET_DEFAULT_HIDDEN) {
    // New widgets are opt-in for existing and fresh users until explicitly enabled.
    if (!Array.isArray(preferences.hiddenOverviewWidgets)) hidden.add(widget);
  }
  const hiddenOverviewWidgets = [...hidden].filter((id) => id !== "overview-attention" && OVERVIEW_WIDGET_ORDER.includes(id));
  const overviewWidgetSizes = {};
  for (const widget of OVERVIEW_WIDGET_CATALOG) {
    overviewWidgetSizes[widget.id] = OVERVIEW_WIDGET_SIZES.includes(preferences.overviewWidgetSizes?.[widget.id])
      ? preferences.overviewWidgetSizes[widget.id]
      : widget.defaultSize;
  }
  return { hiddenOverviewWidgets, overviewWidgetSizes };
}
