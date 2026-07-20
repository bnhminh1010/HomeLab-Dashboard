CREATE TABLE IF NOT EXISTS history_nodes (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

INSERT OR IGNORE INTO history_nodes(id, display_name, created_at)
VALUES ('local', 'Local host', unixepoch());

CREATE TABLE IF NOT EXISTS history_host_raw (
    node_id TEXT NOT NULL,
    collected_at INTEGER NOT NULL,
    cpu_percent REAL NOT NULL,
    memory_used_bytes INTEGER NOT NULL,
    memory_total_bytes INTEGER NOT NULL,
    disk_used_bytes INTEGER NOT NULL,
    disk_total_bytes INTEGER NOT NULL,
    network_rx_bytes_per_second REAL NOT NULL,
    network_tx_bytes_per_second REAL NOT NULL,
    load_one REAL NOT NULL,
    temperature_celsius REAL,
    PRIMARY KEY (node_id, collected_at),
    FOREIGN KEY (node_id) REFERENCES history_nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_host_raw_time
    ON history_host_raw(collected_at);

CREATE TABLE IF NOT EXISTS history_host_rollup_1m (
    node_id TEXT NOT NULL,
    bucket_at INTEGER NOT NULL,
    sample_count INTEGER NOT NULL,
    cpu_percent REAL NOT NULL,
    memory_used_bytes REAL NOT NULL,
    memory_total_bytes REAL NOT NULL,
    disk_used_bytes REAL NOT NULL,
    disk_total_bytes REAL NOT NULL,
    network_rx_bytes_per_second REAL NOT NULL,
    network_tx_bytes_per_second REAL NOT NULL,
    load_one REAL NOT NULL,
    temperature_celsius REAL,
    temperature_sample_count INTEGER NOT NULL,
    PRIMARY KEY (node_id, bucket_at),
    FOREIGN KEY (node_id) REFERENCES history_nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_host_rollup_1m_time
    ON history_host_rollup_1m(bucket_at);

CREATE TABLE IF NOT EXISTS history_host_rollup_15m (
    node_id TEXT NOT NULL,
    bucket_at INTEGER NOT NULL,
    sample_count INTEGER NOT NULL,
    cpu_percent REAL NOT NULL,
    memory_used_bytes REAL NOT NULL,
    memory_total_bytes REAL NOT NULL,
    disk_used_bytes REAL NOT NULL,
    disk_total_bytes REAL NOT NULL,
    network_rx_bytes_per_second REAL NOT NULL,
    network_tx_bytes_per_second REAL NOT NULL,
    load_one REAL NOT NULL,
    temperature_celsius REAL,
    temperature_sample_count INTEGER NOT NULL,
    PRIMARY KEY (node_id, bucket_at),
    FOREIGN KEY (node_id) REFERENCES history_nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_host_rollup_15m_time
    ON history_host_rollup_15m(bucket_at);

CREATE TABLE IF NOT EXISTS history_container_instances (
    node_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    name TEXT NOT NULL,
    image TEXT NOT NULL,
    first_seen_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    PRIMARY KEY (node_id, instance_id),
    FOREIGN KEY (node_id) REFERENCES history_nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_container_instances_name
    ON history_container_instances(node_id, name, last_seen_at DESC);

CREATE TABLE IF NOT EXISTS history_container_raw (
    node_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    collected_at INTEGER NOT NULL,
    cpu_percent REAL NOT NULL,
    memory_usage_bytes INTEGER NOT NULL,
    memory_limit_bytes INTEGER NOT NULL,
    restart_count INTEGER NOT NULL,
    PRIMARY KEY (node_id, instance_id, collected_at),
    FOREIGN KEY (node_id, instance_id)
        REFERENCES history_container_instances(node_id, instance_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_container_raw_time
    ON history_container_raw(collected_at);

CREATE TABLE IF NOT EXISTS history_container_rollup_5m (
    node_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    bucket_at INTEGER NOT NULL,
    sample_count INTEGER NOT NULL,
    cpu_percent REAL NOT NULL,
    memory_usage_bytes REAL NOT NULL,
    memory_limit_bytes REAL NOT NULL,
    restart_count INTEGER NOT NULL,
    PRIMARY KEY (node_id, instance_id, bucket_at),
    FOREIGN KEY (node_id, instance_id)
        REFERENCES history_container_instances(node_id, instance_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_container_rollup_5m_time
    ON history_container_rollup_5m(bucket_at);

CREATE TABLE IF NOT EXISTS history_container_rollup_1h (
    node_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    bucket_at INTEGER NOT NULL,
    sample_count INTEGER NOT NULL,
    cpu_percent REAL NOT NULL,
    memory_usage_bytes REAL NOT NULL,
    memory_limit_bytes REAL NOT NULL,
    restart_count INTEGER NOT NULL,
    PRIMARY KEY (node_id, instance_id, bucket_at),
    FOREIGN KEY (node_id, instance_id)
        REFERENCES history_container_instances(node_id, instance_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_container_rollup_1h_time
    ON history_container_rollup_1h(bucket_at);

CREATE TABLE IF NOT EXISTS history_service_transitions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('up', 'down', 'degraded', 'unknown')),
    observed_at INTEGER NOT NULL,
    UNIQUE (node_id, service_id, observed_at, state),
    FOREIGN KEY (node_id) REFERENCES history_nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_service_transitions_lookup
    ON history_service_transitions(node_id, service_id, observed_at);

CREATE TABLE IF NOT EXISTS history_service_uptime_1h (
    node_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    bucket_at INTEGER NOT NULL,
    up_seconds INTEGER NOT NULL,
    down_seconds INTEGER NOT NULL,
    degraded_seconds INTEGER NOT NULL,
    unknown_seconds INTEGER NOT NULL,
    transition_count INTEGER NOT NULL,
    PRIMARY KEY (node_id, service_id, bucket_at),
    FOREIGN KEY (node_id) REFERENCES history_nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_history_service_uptime_1h_time
    ON history_service_uptime_1h(bucket_at);

CREATE TABLE IF NOT EXISTS history_maintenance (
    job TEXT PRIMARY KEY,
    cursor_at INTEGER NOT NULL
);
