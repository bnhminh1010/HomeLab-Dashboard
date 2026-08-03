ALTER TABLE alert_rules ADD COLUMN runbook_url TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS alert_maintenance_windows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    node_selector TEXT NOT NULL DEFAULT '*',
    resource_type TEXT NOT NULL,
    resource_selector TEXT NOT NULL DEFAULT '*',
    weekdays_json TEXT NOT NULL,
    start_minute INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    timezone TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_alert_maintenance_windows_enabled
    ON alert_maintenance_windows(enabled, resource_type);
