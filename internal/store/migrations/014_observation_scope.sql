-- Certificate results are tied to the exact configured HTTPS endpoint. This
-- prevents a certificate from an old URL remaining visible after a service is
-- moved to HTTP or pointed at a different host.
ALTER TABLE certificate_observations
    ADD COLUMN endpoint_url TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_certificate_observations_checked_at
    ON certificate_observations(checked_at DESC);
