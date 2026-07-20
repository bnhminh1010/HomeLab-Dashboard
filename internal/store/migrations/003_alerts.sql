CREATE TABLE IF NOT EXISTS alert_rules (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    node_selector TEXT NOT NULL DEFAULT '*',
    resource_selector TEXT NOT NULL DEFAULT '*',
    metric TEXT NOT NULL,
    operator TEXT NOT NULL,
    threshold REAL NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    severity TEXT NOT NULL,
    cooldown_ms INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_alert_rules_enabled
    ON alert_rules(enabled, resource_type, metric);

CREATE TABLE IF NOT EXISTS alert_states (
    rule_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    status TEXT NOT NULL,
    pending_since TEXT,
    firing_since TEXT,
    resolved_at TEXT,
    last_evaluated_at TEXT NOT NULL,
    last_notified_at TEXT,
    last_value REAL NOT NULL,
    clean_evaluations INTEGER NOT NULL DEFAULT 0,
    acknowledged_at TEXT,
    acknowledged_by TEXT NOT NULL DEFAULT '',
    silenced_until TEXT,
    silenced_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (rule_id, node_id, resource_type, resource_id)
);

CREATE INDEX IF NOT EXISTS idx_alert_states_status
    ON alert_states(status, node_id, last_evaluated_at DESC);

CREATE TABLE IF NOT EXISTS alert_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    status TEXT NOT NULL,
    severity TEXT NOT NULL,
    value REAL NOT NULL,
    message TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_alert_events_occurred_at
    ON alert_events(occurred_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_alert_events_resource
    ON alert_events(rule_id, node_id, resource_id, occurred_at DESC);

CREATE TABLE IF NOT EXISTS alert_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL,
    title TEXT NOT NULL,
    message TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    delivered_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_alert_deliveries_due
    ON alert_deliveries(status, next_attempt_at, id);
