CREATE TABLE IF NOT EXISTS node_enrollments (
    id TEXT PRIMARY KEY,
    token_hash BLOB NOT NULL UNIQUE,
    created_by TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_node_enrollments_expires
    ON node_enrollments(expires_at);

CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    hostname TEXT NOT NULL,
    credential_hash BLOB NOT NULL,
    last_seen_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    revoked_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_nodes_active
    ON nodes(revoked_at, lower(display_name));
