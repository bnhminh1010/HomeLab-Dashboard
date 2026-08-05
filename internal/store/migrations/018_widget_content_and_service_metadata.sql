ALTER TABLE services ADD COLUMN category TEXT NOT NULL DEFAULT 'Uncategorized';
ALTER TABLE services ADD COLUMN tags_json TEXT NOT NULL DEFAULT '[]';

CREATE TABLE IF NOT EXISTS launchpad_bookmarks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    url TEXT NOT NULL,
    icon TEXT NOT NULL DEFAULT '',
    tag TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS operator_notes (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    text TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS widget_content_meta (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    launchpad_revision INTEGER NOT NULL DEFAULT 0
);

INSERT OR IGNORE INTO operator_notes(singleton_id, text, revision, updated_at, updated_by)
VALUES (1, '', 0, '1970-01-01T00:00:00Z', '');
INSERT OR IGNORE INTO widget_content_meta(singleton_id, launchpad_revision) VALUES (1, 0);

UPDATE dashboard_ui_preferences
SET overview_widget_sizes_json = json_set(
    overview_widget_sizes_json,
    '$."overview-quick-launchpad"', 'full',
    '$."overview-service-groups"', 'medium',
    '$."overview-top-containers"', 'medium',
    '$."overview-storage-pools"', 'small',
    '$."overview-operator-notes"', 'small'
),
hidden_overview_widgets_json = json_insert(
    hidden_overview_widgets_json,
    '$[#]', 'overview-quick-launchpad',
    '$[#]', 'overview-service-groups',
    '$[#]', 'overview-top-containers',
    '$[#]', 'overview-storage-pools',
    '$[#]', 'overview-operator-notes'
);
