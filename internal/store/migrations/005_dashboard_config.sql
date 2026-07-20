CREATE TABLE IF NOT EXISTS dashboard_ui_preferences (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    terminal_height INTEGER NOT NULL DEFAULT 200,
    terminal_collapsed INTEGER NOT NULL DEFAULT 0,
    history_range TEXT NOT NULL DEFAULT '24h',
    default_node_id TEXT NOT NULL DEFAULT 'local'
);

INSERT INTO dashboard_ui_preferences (
    singleton_id, terminal_height, terminal_collapsed, history_range, default_node_id
) VALUES (1, 200, 0, '24h', 'local')
ON CONFLICT(singleton_id) DO NOTHING;
