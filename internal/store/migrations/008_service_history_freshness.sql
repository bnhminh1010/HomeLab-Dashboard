-- Earlier rollups carried the last service state through monitoring gaps.
-- Rebuild them once using observation-expiry semantics. The CREATE statements
-- keep this migration compatible with databases that recorded migration 002
-- under an older component without actually owning the history tables.
CREATE TABLE IF NOT EXISTS history_service_transitions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('up', 'down', 'degraded', 'unknown')),
    observed_at INTEGER NOT NULL,
    UNIQUE (node_id, service_id, observed_at, state)
);

CREATE TABLE IF NOT EXISTS history_service_uptime_1h (
    node_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    bucket_at INTEGER NOT NULL,
    up_seconds INTEGER NOT NULL,
    down_seconds INTEGER NOT NULL,
    degraded_seconds INTEGER NOT NULL,
    unknown_seconds INTEGER NOT NULL,
    transition_count INTEGER NOT NULL,
    PRIMARY KEY (node_id, service_id, bucket_at)
);

CREATE TABLE IF NOT EXISTS history_maintenance (
    job TEXT PRIMARY KEY,
    cursor_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_history_service_transitions_time
    ON history_service_transitions(observed_at, node_id, service_id);

DELETE FROM history_service_uptime_1h;
DELETE FROM history_maintenance WHERE job = 'service_1h';
