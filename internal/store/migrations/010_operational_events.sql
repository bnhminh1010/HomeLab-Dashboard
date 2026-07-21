-- Operational timeline data is intentionally separate from audit_events. It
-- stores concise, user-facing changes without request metadata or credentials.
CREATE TABLE IF NOT EXISTS operational_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('automatic', 'manual')),
    visibility TEXT NOT NULL CHECK (visibility IN ('normal', 'sensitive')),
    title TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    node_id TEXT NOT NULL DEFAULT '',
    service_id TEXT NOT NULL DEFAULT '',
    container_id TEXT NOT NULL DEFAULT '',
    actor TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_operational_events_occurred_at
    ON operational_events(occurred_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_operational_events_type_occurred_at
    ON operational_events(event_type, occurred_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_operational_events_node_occurred_at
    ON operational_events(node_id, occurred_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_operational_events_service_occurred_at
    ON operational_events(service_id, occurred_at DESC, id DESC);
