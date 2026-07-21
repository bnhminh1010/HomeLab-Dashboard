CREATE TABLE IF NOT EXISTS topology_dependencies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT NOT NULL,
    dependent_service_id TEXT NOT NULL,
    dependency_service_id TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CONSTRAINT topology_dependencies_unique_edge
        UNIQUE (node_id, dependent_service_id, dependency_service_id),
    CONSTRAINT topology_dependencies_not_self
        CHECK (dependent_service_id <> dependency_service_id),
    FOREIGN KEY (dependent_service_id) REFERENCES services(id) ON DELETE CASCADE,
    FOREIGN KEY (dependency_service_id) REFERENCES services(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_topology_dependencies_node
    ON topology_dependencies(node_id, id);

CREATE INDEX IF NOT EXISTS idx_topology_dependencies_dependency
    ON topology_dependencies(node_id, dependency_service_id);
