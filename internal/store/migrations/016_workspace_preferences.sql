ALTER TABLE dashboard_ui_preferences
    ADD COLUMN hidden_workspaces_json TEXT NOT NULL DEFAULT '[]';

ALTER TABLE dashboard_ui_preferences
    ADD COLUMN workspace_order_json TEXT NOT NULL DEFAULT '["overview","services","containers","nodes","history","logs","alerts","topology"]';
