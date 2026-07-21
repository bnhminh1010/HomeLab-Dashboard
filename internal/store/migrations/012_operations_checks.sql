CREATE TABLE IF NOT EXISTS certificate_observations (
    service_id TEXT PRIMARY KEY NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    checked_at TEXT NOT NULL,
    not_after TEXT,
    issuer TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS backup_observations (
    node_id TEXT NOT NULL,
    job TEXT NOT NULL,
    status TEXT NOT NULL,
    completed_at TEXT,
    expected_within_seconds INTEGER NOT NULL DEFAULT 0,
    bytes INTEGER NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT '',
    observed_at TEXT NOT NULL,
    PRIMARY KEY (node_id, job)
);

CREATE INDEX IF NOT EXISTS idx_backup_observations_observed_at
    ON backup_observations(observed_at DESC);
