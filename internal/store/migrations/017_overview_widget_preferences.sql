ALTER TABLE dashboard_ui_preferences
    ADD COLUMN hidden_overview_widgets_json TEXT NOT NULL DEFAULT '[]';

ALTER TABLE dashboard_ui_preferences
    ADD COLUMN overview_widget_sizes_json TEXT NOT NULL DEFAULT '{"overview-attention":"full","overview-trend":"medium","overview-recent-changes":"small","system-card":"medium","overview-service-pulse":"small"}';
