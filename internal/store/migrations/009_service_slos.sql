-- Per-service availability objectives. Policies are optional: a missing row
-- means the dashboard applies the product default (99.5% over 30 days).
CREATE TABLE IF NOT EXISTS service_slo_policies (
    service_id TEXT PRIMARY KEY,
    target_percent REAL NOT NULL CHECK (target_percent >= 90 AND target_percent <= 99.999),
    window_days INTEGER NOT NULL CHECK (window_days IN (7, 30, 90)),
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (service_id) REFERENCES services(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_service_slo_policies_updated_at
    ON service_slo_policies(updated_at DESC);
