package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/alerts"
	"github.com/binhminh/HomeLab-Minh/internal/dashboardconfig"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/nodes"
)

func (s *Store) LoadDashboardConfig(ctx context.Context) (dashboardconfig.Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("begin dashboard config snapshot: %w", err)
	}
	defer tx.Rollback()
	snapshot, err := loadDashboardConfigTx(ctx, tx)
	if err != nil {
		return dashboardconfig.Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("commit dashboard config snapshot: %w", err)
	}
	return snapshot, nil
}

func (s *Store) GetDashboardUIPreferences(ctx context.Context) (dashboardconfig.UIPreferences, error) {
	var preferences dashboardconfig.UIPreferences
	if err := s.db.QueryRowContext(ctx, `
		SELECT terminal_height, terminal_collapsed, history_range, default_node_id
		FROM dashboard_ui_preferences WHERE singleton_id = 1`).Scan(
		&preferences.TerminalHeight, &preferences.TerminalCollapsed,
		&preferences.HistoryRange, &preferences.DefaultNodeID,
	); err != nil {
		return dashboardconfig.UIPreferences{}, fmt.Errorf("get dashboard UI preferences: %w", err)
	}
	return preferences, nil
}

func (s *Store) UpdateDashboardUIPreferences(ctx context.Context, preferences dashboardconfig.UIPreferences, actor string) (dashboardconfig.UIPreferences, error) {
	if err := dashboardconfig.ValidateUIPreferences(preferences); err != nil {
		return dashboardconfig.UIPreferences{}, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return dashboardconfig.UIPreferences{}, errors.New("dashboard preferences actor is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dashboardconfig.UIPreferences{}, fmt.Errorf("begin dashboard UI preferences update: %w", err)
	}
	defer tx.Rollback()
	if err := validateDashboardDefaultNodeTx(ctx, tx, preferences.DefaultNodeID); err != nil {
		return dashboardconfig.UIPreferences{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE dashboard_ui_preferences
		SET terminal_height = ?, terminal_collapsed = ?, history_range = ?, default_node_id = ?
		WHERE singleton_id = 1`, preferences.TerminalHeight, preferences.TerminalCollapsed,
		preferences.HistoryRange, preferences.DefaultNodeID); err != nil {
		return dashboardconfig.UIPreferences{}, fmt.Errorf("update dashboard UI preferences: %w", err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"terminalHeight": preferences.TerminalHeight, "terminalCollapsed": preferences.TerminalCollapsed,
		"historyRange": preferences.HistoryRange, "defaultNodeId": preferences.DefaultNodeID,
	})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events(actor, action, target_type, target_id, outcome, metadata_json, created_at)
		VALUES (?, 'preferences.update', 'ui_preferences', 'dashboard', 'success', ?, ?)`,
		actor, string(metadata), s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return dashboardconfig.UIPreferences{}, fmt.Errorf("audit dashboard UI preferences: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return dashboardconfig.UIPreferences{}, fmt.Errorf("commit dashboard UI preferences: %w", err)
	}
	return preferences, nil
}

// ApplyDashboardConfig applies all portable sections and its audit record in
// one SQLite transaction. Node credentials are neither read nor written; only
// display fields of already enrolled, active nodes can change.
func (s *Store) ApplyDashboardConfig(
	ctx context.Context,
	incoming dashboardconfig.Snapshot,
	mode dashboardconfig.ImportMode,
	actor string,
	expectedRevision string,
) error {
	if mode == "" {
		mode = dashboardconfig.ImportMerge
	}
	if !mode.Valid() {
		return dashboardconfig.ErrInvalidImportMode
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return errors.New("dashboard config import actor is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin dashboard config import: %w", err)
	}
	defer tx.Rollback()
	current, err := loadDashboardConfigTx(ctx, tx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(expectedRevision) == "" {
		return dashboardconfig.ErrRevisionRequired
	}
	currentRevision, err := dashboardconfig.Revision(current)
	if err != nil {
		return err
	}
	if currentRevision != expectedRevision {
		return dashboardconfig.ErrRevisionConflict
	}
	if err := validateDashboardDefaultNodeTx(ctx, tx, incoming.UIPreferences.DefaultNodeID); err != nil {
		return err
	}
	now := s.now().UTC()
	counts := map[string]int{
		"services": len(incoming.Services), "alertRules": len(incoming.AlertRules),
		"nodes": len(incoming.Nodes),
	}
	if err := applyConfigServices(ctx, tx, current.Services, incoming.Services, mode, now); err != nil {
		return err
	}
	if err := applyConfigAlertRules(ctx, tx, current.AlertRules, incoming.AlertRules, mode, now); err != nil {
		return err
	}
	if current.UIPreferences != incoming.UIPreferences {
		if _, err := tx.ExecContext(ctx, `
			UPDATE dashboard_ui_preferences
			SET terminal_height = ?, terminal_collapsed = ?, history_range = ?, default_node_id = ?
			WHERE singleton_id = 1`, incoming.UIPreferences.TerminalHeight,
			incoming.UIPreferences.TerminalCollapsed, incoming.UIPreferences.HistoryRange,
			incoming.UIPreferences.DefaultNodeID); err != nil {
			return fmt.Errorf("update dashboard UI preferences: %w", err)
		}
	}
	if err := applyConfigNodeMetadata(ctx, tx, current.Nodes, incoming.Nodes, now); err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]any{
		"version": dashboardconfig.DocumentVersion,
		"mode":    mode,
		"counts":  counts,
	})
	if err != nil {
		return fmt.Errorf("encode dashboard config audit metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events(actor, action, target_type, target_id, outcome, metadata_json, created_at)
		VALUES (?, 'config.import', 'dashboard_config', ?, 'success', ?, ?)`,
		actor, dashboardconfig.DocumentVersion, string(metadata), now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("append dashboard config audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit dashboard config import: %w", err)
	}
	return nil
}

func loadDashboardConfigTx(ctx context.Context, tx *sql.Tx) (dashboardconfig.Snapshot, error) {
	snapshot := dashboardconfig.Snapshot{
		Services: make([]model.Service, 0), AlertRules: make([]alerts.AlertRule, 0),
		Nodes: make([]nodes.Node, 0),
	}
	serviceRows, err := tx.QueryContext(ctx, `
		SELECT id, name, icon, display_url, probe_url, created_at, updated_at
		FROM services ORDER BY id`)
	if err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("load dashboard config services: %w", err)
	}
	for serviceRows.Next() {
		service, scanErr := scanService(serviceRows)
		if scanErr != nil {
			serviceRows.Close()
			return dashboardconfig.Snapshot{}, fmt.Errorf("scan dashboard config service: %w", scanErr)
		}
		snapshot.Services = append(snapshot.Services, service)
	}
	if err := serviceRows.Close(); err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("close dashboard config services: %w", err)
	}
	if err := serviceRows.Err(); err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("load dashboard config services: %w", err)
	}

	ruleRows, err := tx.QueryContext(ctx, `
		SELECT id, name, resource_type, node_selector, resource_selector, metric, operator,
			threshold, duration_ms, severity, cooldown_ms, enabled, created_at, updated_at
		FROM alert_rules ORDER BY id`)
	if err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("load dashboard config alert rules: %w", err)
	}
	for ruleRows.Next() {
		rule, scanErr := scanAlertRule(ruleRows)
		if scanErr != nil {
			ruleRows.Close()
			return dashboardconfig.Snapshot{}, fmt.Errorf("scan dashboard config alert rule: %w", scanErr)
		}
		snapshot.AlertRules = append(snapshot.AlertRules, rule)
	}
	if err := ruleRows.Close(); err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("close dashboard config alert rules: %w", err)
	}
	if err := ruleRows.Err(); err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("load dashboard config alert rules: %w", err)
	}

	if err := tx.QueryRowContext(ctx, `
		SELECT terminal_height, terminal_collapsed, history_range, default_node_id
		FROM dashboard_ui_preferences WHERE singleton_id = 1`).Scan(
		&snapshot.UIPreferences.TerminalHeight, &snapshot.UIPreferences.TerminalCollapsed,
		&snapshot.UIPreferences.HistoryRange, &snapshot.UIPreferences.DefaultNodeID,
	); err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("load dashboard UI preferences: %w", err)
	}

	nodeRows, err := tx.QueryContext(ctx, `
		SELECT id, display_name, hostname, last_seen_at, created_at, updated_at
		FROM nodes WHERE revoked_at IS NULL ORDER BY id`)
	if err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("load dashboard config nodes: %w", err)
	}
	for nodeRows.Next() {
		var node nodes.Node
		var lastSeen sql.NullInt64
		var created, updated int64
		if err := nodeRows.Scan(&node.ID, &node.DisplayName, &node.Hostname, &lastSeen, &created, &updated); err != nil {
			nodeRows.Close()
			return dashboardconfig.Snapshot{}, fmt.Errorf("scan dashboard config node: %w", err)
		}
		node.CreatedAt = time.Unix(created, 0).UTC()
		node.UpdatedAt = time.Unix(updated, 0).UTC()
		if lastSeen.Valid {
			seen := time.Unix(lastSeen.Int64, 0).UTC()
			node.LastSeenAt = &seen
		}
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	if err := nodeRows.Close(); err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("close dashboard config nodes: %w", err)
	}
	if err := nodeRows.Err(); err != nil {
		return dashboardconfig.Snapshot{}, fmt.Errorf("load dashboard config nodes: %w", err)
	}
	return snapshot, nil
}

func applyConfigServices(
	ctx context.Context,
	tx *sql.Tx,
	current []model.Service,
	incoming []model.Service,
	mode dashboardconfig.ImportMode,
	now time.Time,
) error {
	currentByID := make(map[string]model.Service, len(current))
	incomingByID := make(map[string]model.Service, len(incoming))
	for _, service := range current {
		currentByID[service.ID] = service
	}
	for _, service := range incoming {
		incomingByID[service.ID] = service
		existing, exists := currentByID[service.ID]
		if exists && sameServiceDefinition(existing, service) {
			continue
		}
		if exists {
			if _, err := tx.ExecContext(ctx, `
				UPDATE services SET name = ?, icon = ?, display_url = ?, probe_url = ?, updated_at = ?
				WHERE id = ?`, service.Name, service.Icon, service.DisplayURL, service.ProbeURL,
				now.Format(time.RFC3339Nano), service.ID); err != nil {
				return fmt.Errorf("update imported service %s: %w", service.ID, err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO services(id, name, icon, display_url, probe_url, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, service.ID, service.Name, service.Icon,
			service.DisplayURL, service.ProbeURL, now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert imported service %s: %w", service.ID, err)
		}
	}
	if mode == dashboardconfig.ImportReplace {
		for _, service := range current {
			if _, retained := incomingByID[service.ID]; retained {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM services WHERE id = ?", service.ID); err != nil {
				return fmt.Errorf("delete replaced service %s: %w", service.ID, err)
			}
		}
	}
	return nil
}

func applyConfigAlertRules(
	ctx context.Context,
	tx *sql.Tx,
	current []alerts.AlertRule,
	incoming []alerts.AlertRule,
	mode dashboardconfig.ImportMode,
	now time.Time,
) error {
	currentByID := make(map[string]alerts.AlertRule, len(current))
	incomingByID := make(map[string]alerts.AlertRule, len(incoming))
	for _, rule := range current {
		currentByID[rule.ID] = rule
	}
	for _, rule := range incoming {
		incomingByID[rule.ID] = rule
		existing, exists := currentByID[rule.ID]
		if exists && sameAlertRuleDefinition(existing, rule) {
			continue
		}
		if exists {
			if _, err := tx.ExecContext(ctx, `
				UPDATE alert_rules SET name = ?, resource_type = ?, node_selector = ?,
					resource_selector = ?, metric = ?, operator = ?, threshold = ?, duration_ms = ?,
					severity = ?, cooldown_ms = ?, enabled = ?, updated_at = ? WHERE id = ?`,
				rule.Name, rule.ResourceType, rule.NodeSelector, rule.ResourceSelector,
				rule.Metric, rule.Operator, rule.Threshold, rule.For.Milliseconds(),
				rule.Severity, rule.Cooldown.Milliseconds(), rule.Enabled,
				formatAlertTime(now), rule.ID); err != nil {
				return fmt.Errorf("update imported alert rule %s: %w", rule.ID, err)
			}
			if err := clearImportedAlertRuntime(ctx, tx, rule.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO alert_rules (
				id, name, resource_type, node_selector, resource_selector, metric, operator,
				threshold, duration_ms, severity, cooldown_ms, enabled, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rule.ID, rule.Name, rule.ResourceType, rule.NodeSelector, rule.ResourceSelector,
			rule.Metric, rule.Operator, rule.Threshold, rule.For.Milliseconds(), rule.Severity,
			rule.Cooldown.Milliseconds(), rule.Enabled, formatAlertTime(now), formatAlertTime(now)); err != nil {
			return fmt.Errorf("insert imported alert rule %s: %w", rule.ID, err)
		}
	}
	if mode == dashboardconfig.ImportReplace {
		for _, rule := range current {
			if _, retained := incomingByID[rule.ID]; retained {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM alert_rules WHERE id = ?", rule.ID); err != nil {
				return fmt.Errorf("delete replaced alert rule %s: %w", rule.ID, err)
			}
			if err := clearImportedAlertRuntime(ctx, tx, rule.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func clearImportedAlertRuntime(ctx context.Context, tx *sql.Tx, ruleID string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM alert_states WHERE rule_id = ?", ruleID); err != nil {
		return fmt.Errorf("reset imported alert state %s: %w", ruleID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE alert_deliveries
		SET status = 'superseded', last_error = 'alert rule imported'
		WHERE rule_id = ? AND status IN ('pending', 'processing')`, ruleID); err != nil {
		return fmt.Errorf("reset imported alert deliveries %s: %w", ruleID, err)
	}
	return nil
}

func validateDashboardDefaultNodeTx(ctx context.Context, tx *sql.Tx, nodeID string) error {
	if nodeID == "local" {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM nodes WHERE id = ? AND revoked_at IS NULL)`, nodeID).Scan(&exists); err != nil {
		return fmt.Errorf("validate dashboard default node: %w", err)
	}
	if exists == 0 {
		return &dashboardconfig.ValidationError{
			Path:    "uiPreferences.defaultNodeId",
			Message: "must reference local or an actively enrolled node",
		}
	}
	return nil
}

func applyConfigNodeMetadata(
	ctx context.Context,
	tx *sql.Tx,
	current []nodes.Node,
	incoming []nodes.Node,
	now time.Time,
) error {
	currentByID := make(map[string]nodes.Node, len(current))
	for _, node := range current {
		currentByID[node.ID] = node
	}
	for _, node := range incoming {
		existing, enrolled := currentByID[node.ID]
		if !enrolled || existing.DisplayName == node.DisplayName && existing.Hostname == node.Hostname {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE nodes SET display_name = ?, hostname = ?, updated_at = ?
			WHERE id = ? AND revoked_at IS NULL`, node.DisplayName, node.Hostname,
			now.Unix(), node.ID); err != nil {
			return fmt.Errorf("update imported node metadata %s: %w", node.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE history_nodes SET display_name = ? WHERE id = ?`, node.DisplayName, node.ID); err != nil {
			return fmt.Errorf("update imported history node metadata %s: %w", node.ID, err)
		}
	}
	return nil
}

func sameServiceDefinition(left, right model.Service) bool {
	return left.ID == right.ID && left.Name == right.Name && left.Icon == right.Icon &&
		left.DisplayURL == right.DisplayURL && left.ProbeURL == right.ProbeURL
}

func sameAlertRuleDefinition(left, right alerts.AlertRule) bool {
	return left.ID == right.ID && left.Name == right.Name && left.ResourceType == right.ResourceType &&
		left.NodeSelector == right.NodeSelector && left.ResourceSelector == right.ResourceSelector &&
		left.Metric == right.Metric && left.Operator == right.Operator && left.Threshold == right.Threshold &&
		left.For == right.For && left.Severity == right.Severity && left.Cooldown == right.Cooldown &&
		left.Enabled == right.Enabled
}
