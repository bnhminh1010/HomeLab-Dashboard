package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
)

const (
	alertDefaultsSeededKey = "alert_defaults_seeded"
	alertDefaultsVersion   = "3"
)

// SeedDefaultAlertRules installs the built-in rules at most once for a
// database. Once marked, an intentionally empty rule set remains empty across
// restarts and replace imports.
func (s *Store) SeedDefaultAlertRules(ctx context.Context, defaults []alerts.AlertRule) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin default alert rule seed: %w", err)
	}
	defer tx.Rollback()
	var marker string
	err = tx.QueryRowContext(ctx, "SELECT value FROM app_state WHERE key = ?", alertDefaultsSeededKey).Scan(&marker)
	if err == nil && marker == alertDefaultsVersion {
		return tx.Commit()
	}
	if err == nil && marker != "1" && marker != "2" {
		return fmt.Errorf("unsupported default alert rule version %q", marker)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read default alert rule seed marker: %w", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM alert_rules").Scan(&count); err != nil {
		return fmt.Errorf("count alert rules before seed: %w", err)
	}
	now := s.now().UTC()
	seedInitial := errors.Is(err, sql.ErrNoRows) && count == 0
	upgradeLegacyRules := err == nil && (marker == "1" || marker == "2")
	if seedInitial || upgradeLegacyRules {
		for _, candidate := range defaults {
			rule := alerts.NormalizeRule(candidate)
			if marker == "1" && rule.ID != "default_node_offline" && rule.ID != "default_backup_unhealthy" {
				continue
			}
			if marker == "2" && rule.ID != "default_backup_unhealthy" {
				continue
			}
			if err := alerts.ValidateRule(rule); err != nil {
				return fmt.Errorf("validate default alert rule %s: %w", rule.ID, err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO alert_rules (
					id, name, resource_type, node_selector, resource_selector, metric, operator,
					threshold, duration_ms, severity, cooldown_ms, runbook_url, enabled, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				rule.ID, rule.Name, rule.ResourceType, rule.NodeSelector, rule.ResourceSelector,
				rule.Metric, rule.Operator, rule.Threshold, rule.For.Milliseconds(), rule.Severity,
				rule.Cooldown.Milliseconds(), rule.RunbookURL, rule.Enabled, formatAlertTime(now), formatAlertTime(now)); err != nil {
				return fmt.Errorf("insert default alert rule %s: %w", rule.ID, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO app_state(key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		alertDefaultsSeededKey, alertDefaultsVersion, formatAlertTime(now)); err != nil {
		return fmt.Errorf("mark default alert rules seeded: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit default alert rule seed: %w", err)
	}
	return nil
}

func (s *Store) CreateAlertRule(ctx context.Context, rule alerts.AlertRule) (alerts.AlertRule, error) {
	rule = alerts.NormalizeRule(rule)
	if err := alerts.ValidateRule(rule); err != nil {
		return alerts.AlertRule{}, err
	}
	now := s.now().UTC()
	rule.CreatedAt = now
	rule.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_rules (
			id, name, resource_type, node_selector, resource_selector, metric, operator,
			threshold, duration_ms, severity, cooldown_ms, runbook_url, enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rule.ID, rule.Name, rule.ResourceType, rule.NodeSelector, rule.ResourceSelector,
		rule.Metric, rule.Operator, rule.Threshold, rule.For.Milliseconds(), rule.Severity,
		rule.Cooldown.Milliseconds(), rule.RunbookURL, rule.Enabled, formatAlertTime(now), formatAlertTime(now))
	if err != nil {
		return alerts.AlertRule{}, fmt.Errorf("create alert rule: %w", err)
	}
	return rule, nil
}

func (s *Store) GetAlertRule(ctx context.Context, id string) (alerts.AlertRule, error) {
	rule, err := scanAlertRule(s.db.QueryRowContext(ctx, `
		SELECT id, name, resource_type, node_selector, resource_selector, metric, operator,
			threshold, duration_ms, severity, cooldown_ms, runbook_url, enabled, created_at, updated_at
		FROM alert_rules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return alerts.AlertRule{}, ErrNotFound
	}
	if err != nil {
		return alerts.AlertRule{}, fmt.Errorf("get alert rule: %w", err)
	}
	return rule, nil
}

func (s *Store) ListAlertRules(ctx context.Context) ([]alerts.AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, resource_type, node_selector, resource_selector, metric, operator,
			threshold, duration_ms, severity, cooldown_ms, runbook_url, enabled, created_at, updated_at
		FROM alert_rules ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	defer rows.Close()
	rules := make([]alerts.AlertRule, 0)
	for rows.Next() {
		rule, err := scanAlertRule(rows)
		if err != nil {
			return nil, fmt.Errorf("list alert rules: %w", err)
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	return rules, nil
}

func (s *Store) UpdateAlertRule(ctx context.Context, id string, rule alerts.AlertRule) (alerts.AlertRule, error) {
	rule.ID = id
	rule = alerts.NormalizeRule(rule)
	if err := alerts.ValidateRule(rule); err != nil {
		return alerts.AlertRule{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return alerts.AlertRule{}, fmt.Errorf("update alert rule: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE alert_rules SET name = ?, resource_type = ?, node_selector = ?,
			resource_selector = ?, metric = ?, operator = ?, threshold = ?, duration_ms = ?,
			severity = ?, cooldown_ms = ?, runbook_url = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		rule.Name, rule.ResourceType, rule.NodeSelector, rule.ResourceSelector, rule.Metric,
		rule.Operator, rule.Threshold, rule.For.Milliseconds(), rule.Severity,
		rule.Cooldown.Milliseconds(), rule.RunbookURL, rule.Enabled, formatAlertTime(now), id)
	if err != nil {
		return alerts.AlertRule{}, fmt.Errorf("update alert rule: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return alerts.AlertRule{}, fmt.Errorf("update alert rule: %w", err)
	}
	if affected == 0 {
		return alerts.AlertRule{}, ErrNotFound
	}
	// Reset live evaluations whenever the rule definition changes. Otherwise a
	// disabled rule or a changed selector can leave a stale firing state forever.
	if _, err := tx.ExecContext(ctx, "DELETE FROM alert_states WHERE rule_id = ?", id); err != nil {
		return alerts.AlertRule{}, fmt.Errorf("reset alert rule states: %w", err)
	}
	if err := supersedeAlertRuleDeliveries(ctx, tx, id, "alert rule updated"); err != nil {
		return alerts.AlertRule{}, fmt.Errorf("reset alert rule deliveries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return alerts.AlertRule{}, fmt.Errorf("update alert rule: %w", err)
	}
	return s.GetAlertRule(ctx, id)
}

func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM alert_rules WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM alert_states WHERE rule_id = ?", id); err != nil {
		return fmt.Errorf("delete alert rule states: %w", err)
	}
	if err := supersedeAlertRuleDeliveries(ctx, tx, id, "alert rule deleted"); err != nil {
		return fmt.Errorf("delete alert rule deliveries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	return nil
}

func (s *Store) LoadAlertState(ctx context.Context, key alerts.AlertKey) (alerts.AlertState, bool, error) {
	state, err := scanAlertState(s.db.QueryRowContext(ctx, alertStateSelect+`
		WHERE rule_id = ? AND node_id = ? AND resource_type = ? AND resource_id = ?`,
		key.RuleID, key.NodeID, key.ResourceType, key.ResourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return alerts.AlertState{}, false, nil
	}
	if err != nil {
		return alerts.AlertState{}, false, fmt.Errorf("load alert state: %w", err)
	}
	return state, true, nil
}

func (s *Store) ListAlertStates(ctx context.Context, filter alerts.StateFilter) ([]alerts.AlertState, error) {
	query := alertStateSelect + " WHERE 1 = 1"
	args := make([]any, 0, 5)
	if filter.RuleID != "" {
		query += " AND rule_id = ?"
		args = append(args, filter.RuleID)
	}
	if filter.NodeID != "" {
		query += " AND node_id = ?"
		args = append(args, filter.NodeID)
	}
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	if filter.ActiveOnly {
		query += " AND status != 'resolved'"
	}
	limit := boundedAlertLimit(filter.Limit)
	query += " ORDER BY last_evaluated_at DESC, rule_id, node_id, resource_id LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list alert states: %w", err)
	}
	defer rows.Close()
	states := make([]alerts.AlertState, 0)
	for rows.Next() {
		state, err := scanAlertState(rows)
		if err != nil {
			return nil, fmt.Errorf("list alert states: %w", err)
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

// ListActiveAlertStatesForReconciliation is an internal startup query without
// the public API's response-size limit. The monitoring pipeline uses it once to
// resolve persisted incidents whose resource disappeared while the dashboard
// was stopped.
func (s *Store) ListActiveAlertStatesForReconciliation(ctx context.Context) ([]alerts.AlertState, error) {
	rows, err := s.db.QueryContext(ctx, alertStateSelect+`
		WHERE status != 'resolved'
		ORDER BY node_id, resource_type, resource_id, rule_id`)
	if err != nil {
		return nil, fmt.Errorf("list active alert states for reconciliation: %w", err)
	}
	defer rows.Close()
	states := make([]alerts.AlertState, 0)
	for rows.Next() {
		state, err := scanAlertState(rows)
		if err != nil {
			return nil, fmt.Errorf("list active alert states for reconciliation: %w", err)
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (s *Store) ApplyAlertTransition(ctx context.Context, transition alerts.Transition) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply alert transition: %w", err)
	}
	defer tx.Rollback()
	state := transition.State
	state.Revision = transition.ExpectedRevision + 1
	var currentRuleUpdatedAt string
	err = tx.QueryRowContext(ctx, "SELECT updated_at FROM alert_rules WHERE id = ?", state.RuleID).Scan(&currentRuleUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return alerts.ErrStaleTransition
	}
	if err != nil {
		return fmt.Errorf("load alert rule version: %w", err)
	}
	parsedRuleUpdatedAt, err := parseAlertTime(currentRuleUpdatedAt)
	if err != nil {
		return fmt.Errorf("load alert rule version: %w", err)
	}
	if !parsedRuleUpdatedAt.Equal(transition.ExpectedRuleUpdatedAt) {
		return alerts.ErrStaleTransition
	}

	var currentRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM alert_states
		WHERE rule_id = ? AND node_id = ? AND resource_type = ? AND resource_id = ?`,
		state.RuleID, state.NodeID, state.ResourceType, state.ResourceID).Scan(&currentRevision)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if transition.ExpectedRevision != 0 {
			return alerts.ErrStaleTransition
		}
		if err := insertAlertState(ctx, tx, state); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("load alert state revision: %w", err)
	case currentRevision != transition.ExpectedRevision:
		return alerts.ErrStaleTransition
	default:
		if err := updateAlertState(ctx, tx, state, transition.ExpectedRevision); err != nil {
			return err
		}
	}
	if transition.IncidentStarted {
		if err := supersedeAlertKeyResolvedDeliveries(ctx, tx, state.AlertKey, "new alert incident started"); err != nil {
			return fmt.Errorf("supersede stale resolved deliveries: %w", err)
		}
	}
	if transition.Event != nil && transition.Event.Type == alerts.EventResolved {
		if err := supersedeAlertKeyFiringDeliveries(ctx, tx, state.AlertKey, "alert resolved"); err != nil {
			return fmt.Errorf("supersede resolved alert deliveries: %w", err)
		}
	}
	if transition.Event != nil {
		if err := insertAlertEvent(ctx, tx, *transition.Event); err != nil {
			return err
		}
	}
	if transition.Delivery != nil {
		if err := insertAlertDelivery(ctx, tx, *transition.Delivery); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apply alert transition: %w", err)
	}
	return nil
}

func (s *Store) AcknowledgeAlert(ctx context.Context, key alerts.AlertKey, actor string, at time.Time) (alerts.AlertState, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return alerts.AlertState{}, errors.New("acknowledge alert: actor is required")
	}
	if at.IsZero() {
		at = s.now()
	}
	at = at.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return alerts.AlertState{}, fmt.Errorf("acknowledge alert: %w", err)
	}
	defer tx.Rollback()
	state, severity, err := loadStateAndSeverity(ctx, tx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return alerts.AlertState{}, ErrNotFound
	}
	if err != nil {
		return alerts.AlertState{}, fmt.Errorf("acknowledge alert: %w", err)
	}
	if state.Status == alerts.StatusResolved {
		return alerts.AlertState{}, alerts.ErrAlertResolved
	}
	state.AcknowledgedAt = &at
	state.AcknowledgedBy = actor
	result, err := tx.ExecContext(ctx, `UPDATE alert_states
		SET acknowledged_at = ?, acknowledged_by = ?, revision = revision + 1
		WHERE rule_id = ? AND node_id = ? AND resource_type = ? AND resource_id = ? AND revision = ?`,
		formatAlertTime(at), actor, key.RuleID, key.NodeID, key.ResourceType, key.ResourceID, state.Revision)
	if err != nil {
		return alerts.AlertState{}, fmt.Errorf("acknowledge alert: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return alerts.AlertState{}, fmt.Errorf("acknowledge alert: %w", err)
	}
	if affected != 1 {
		return alerts.AlertState{}, alerts.ErrStaleTransition
	}
	state.Revision++
	event := alerts.AlertEvent{
		AlertKey: key, Type: alerts.EventAcknowledged, Status: state.Status, Severity: severity,
		Value: state.LastValue, Message: "Alert acknowledged by " + actor, Actor: actor, OccurredAt: at,
	}
	if err := insertAlertEvent(ctx, tx, event); err != nil {
		return alerts.AlertState{}, err
	}
	if err := supersedeAlertKeyFiringDeliveries(ctx, tx, key, "alert acknowledged"); err != nil {
		return alerts.AlertState{}, fmt.Errorf("acknowledge alert deliveries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return alerts.AlertState{}, fmt.Errorf("acknowledge alert: %w", err)
	}
	return state, nil
}

func (s *Store) SilenceAlert(ctx context.Context, key alerts.AlertKey, actor string, duration time.Duration, at time.Time) (alerts.AlertState, error) {
	if err := alerts.ValidateSilenceDuration(duration); err != nil {
		return alerts.AlertState{}, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return alerts.AlertState{}, errors.New("silence alert: actor is required")
	}
	if at.IsZero() {
		at = s.now()
	}
	at = at.UTC()
	until := at.Add(duration)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return alerts.AlertState{}, fmt.Errorf("silence alert: %w", err)
	}
	defer tx.Rollback()
	state, severity, err := loadStateAndSeverity(ctx, tx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return alerts.AlertState{}, ErrNotFound
	}
	if err != nil {
		return alerts.AlertState{}, fmt.Errorf("silence alert: %w", err)
	}
	if state.Status == alerts.StatusResolved {
		return alerts.AlertState{}, alerts.ErrAlertResolved
	}
	state.SilencedUntil = &until
	state.SilencedBy = actor
	result, err := tx.ExecContext(ctx, `UPDATE alert_states
		SET silenced_until = ?, silenced_by = ?, revision = revision + 1
		WHERE rule_id = ? AND node_id = ? AND resource_type = ? AND resource_id = ? AND revision = ?`,
		formatAlertTime(until), actor, key.RuleID, key.NodeID, key.ResourceType, key.ResourceID, state.Revision)
	if err != nil {
		return alerts.AlertState{}, fmt.Errorf("silence alert: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return alerts.AlertState{}, fmt.Errorf("silence alert: %w", err)
	}
	if affected != 1 {
		return alerts.AlertState{}, alerts.ErrStaleTransition
	}
	state.Revision++
	event := alerts.AlertEvent{
		AlertKey: key, Type: alerts.EventSilenced, Status: state.Status, Severity: severity,
		Value: state.LastValue, Message: fmt.Sprintf("Alert silenced by %s until %s", actor, formatAlertTime(until)),
		Actor: actor, OccurredAt: at,
	}
	if err := insertAlertEvent(ctx, tx, event); err != nil {
		return alerts.AlertState{}, err
	}
	if err := supersedeAlertKeyFiringDeliveries(ctx, tx, key, "alert silenced"); err != nil {
		return alerts.AlertState{}, fmt.Errorf("silence alert deliveries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return alerts.AlertState{}, fmt.Errorf("silence alert: %w", err)
	}
	return state, nil
}

func (s *Store) ListAlertEvents(ctx context.Context, filter alerts.EventFilter) ([]alerts.AlertEvent, error) {
	query := `SELECT id, rule_id, node_id, resource_type, resource_id, event_type, status,
		severity, value, message, actor, occurred_at FROM alert_events WHERE 1 = 1`
	args := make([]any, 0, 5)
	if filter.RuleID != "" {
		query += " AND rule_id = ?"
		args = append(args, filter.RuleID)
	}
	if filter.NodeID != "" {
		query += " AND node_id = ?"
		args = append(args, filter.NodeID)
	}
	if filter.ResourceID != "" {
		query += " AND resource_id = ?"
		args = append(args, filter.ResourceID)
	}
	if filter.Type != "" {
		query += " AND event_type = ?"
		args = append(args, filter.Type)
	}
	query += " ORDER BY occurred_at DESC, id DESC LIMIT ?"
	args = append(args, boundedAlertLimit(filter.Limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list alert events: %w", err)
	}
	defer rows.Close()
	events := make([]alerts.AlertEvent, 0)
	for rows.Next() {
		event, err := scanAlertEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("list alert events: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) ClaimDueAlertDeliveries(ctx context.Context, now time.Time, limit int) ([]alerts.Delivery, error) {
	limit = boundedAlertLimit(limit)
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("claim alert deliveries: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE alert_deliveries
		SET status = 'dead', last_error = 'delivery lease expired after maximum attempts'
		WHERE status IN ('pending', 'processing') AND attempts >= ? AND next_attempt_at <= ?`,
		alerts.MaxDeliveryAttempts, formatAlertTime(now)); err != nil {
		return nil, fmt.Errorf("expire alert deliveries: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM alert_deliveries
		WHERE status IN ('pending', 'processing') AND attempts < ? AND next_attempt_at <= ?
		ORDER BY next_attempt_at, id LIMIT ?`,
		alerts.MaxDeliveryAttempts, formatAlertTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("claim alert deliveries: %w", err)
	}
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("claim alert deliveries: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("claim alert deliveries: %w", err)
	}
	deliveries := make([]alerts.Delivery, 0, len(ids))
	leaseUntil := now.Add(5 * time.Minute)
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE alert_deliveries
			SET status = 'processing', attempts = attempts + 1, next_attempt_at = ?
			WHERE id = ? AND status IN ('pending', 'processing') AND next_attempt_at <= ?`,
			formatAlertTime(leaseUntil), id, formatAlertTime(now))
		if err != nil {
			return nil, fmt.Errorf("claim alert delivery %d: %w", id, err)
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			continue
		}
		delivery, err := scanAlertDelivery(tx.QueryRowContext(ctx, alertDeliverySelect+" WHERE id = ?", id))
		if err != nil {
			return nil, fmt.Errorf("load claimed alert delivery %d: %w", id, err)
		}
		deliveries = append(deliveries, delivery)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim alert deliveries: %w", err)
	}
	return deliveries, nil
}

func (s *Store) MarkAlertDeliveryDelivered(ctx context.Context, id int64, attempt int, at time.Time) error {
	return s.updateAlertDelivery(ctx, id, attempt, `status = 'delivered', delivered_at = ?, last_error = ''`, formatAlertTime(at.UTC()))
}

func (s *Store) IsAlertDeliveryClaimActive(ctx context.Context, id int64, attempt int) (bool, error) {
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM alert_deliveries WHERE id = ? AND status = 'processing' AND attempts = ?
	)`, id, attempt).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("validate alert delivery claim: %w", err)
	}
	return active != 0, nil
}

func (s *Store) RescheduleAlertDelivery(ctx context.Context, id int64, attempt int, next time.Time, message string) error {
	return s.updateAlertDelivery(ctx, id, attempt, `status = 'pending', next_attempt_at = ?, last_error = ?`,
		formatAlertTime(next.UTC()), message)
}

func (s *Store) MarkAlertDeliveryDead(ctx context.Context, id int64, attempt int, message string) error {
	return s.updateAlertDelivery(ctx, id, attempt, `status = 'dead', last_error = ?`, message)
}

func (s *Store) ListAlertDeliveries(ctx context.Context, status alerts.DeliveryStatus, limit int) ([]alerts.Delivery, error) {
	query := alertDeliverySelect
	args := make([]any, 0, 2)
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, boundedAlertLimit(limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list alert deliveries: %w", err)
	}
	defer rows.Close()
	result := make([]alerts.Delivery, 0)
	for rows.Next() {
		delivery, err := scanAlertDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("list alert deliveries: %w", err)
		}
		result = append(result, delivery)
	}
	return result, rows.Err()
}

const alertStateSelect = `SELECT rule_id, node_id, resource_type, resource_id, status,
	pending_since, firing_since, resolved_at, last_evaluated_at, last_notified_at,
	last_value, clean_evaluations, acknowledged_at, acknowledged_by, silenced_until, silenced_by, revision
	FROM alert_states`

const alertDeliverySelect = `SELECT id, rule_id, node_id, resource_type, resource_id, kind,
	severity, title, message, status, attempts, next_attempt_at, last_error, created_at, delivered_at
	FROM alert_deliveries`

func scanAlertRule(row scanner) (alerts.AlertRule, error) {
	var rule alerts.AlertRule
	var durationMS, cooldownMS int64
	var createdAt, updatedAt string
	if err := row.Scan(&rule.ID, &rule.Name, &rule.ResourceType, &rule.NodeSelector,
		&rule.ResourceSelector, &rule.Metric, &rule.Operator, &rule.Threshold, &durationMS,
		&rule.Severity, &cooldownMS, &rule.RunbookURL, &rule.Enabled, &createdAt, &updatedAt); err != nil {
		return alerts.AlertRule{}, err
	}
	var err error
	rule.CreatedAt, err = parseAlertTime(createdAt)
	if err != nil {
		return alerts.AlertRule{}, err
	}
	rule.UpdatedAt, err = parseAlertTime(updatedAt)
	if err != nil {
		return alerts.AlertRule{}, err
	}
	rule.For = time.Duration(durationMS) * time.Millisecond
	rule.Cooldown = time.Duration(cooldownMS) * time.Millisecond
	return rule, nil
}

func (s *Store) ListMaintenanceWindows(ctx context.Context) ([]alerts.MaintenanceWindow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, node_selector, resource_type, resource_selector,
		weekdays_json, start_minute, duration_ms, timezone, enabled, created_at, updated_at
		FROM alert_maintenance_windows ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list maintenance windows: %w", err)
	}
	defer rows.Close()
	windows := make([]alerts.MaintenanceWindow, 0)
	for rows.Next() {
		window, scanErr := scanMaintenanceWindow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list maintenance windows: %w", scanErr)
		}
		windows = append(windows, window)
	}
	return windows, rows.Err()
}

func (s *Store) CreateMaintenanceWindow(ctx context.Context, window alerts.MaintenanceWindow) (alerts.MaintenanceWindow, error) {
	window = alerts.NormalizeMaintenance(window)
	if err := alerts.ValidateMaintenance(window); err != nil {
		return alerts.MaintenanceWindow{}, err
	}
	weekdays, err := json.Marshal(window.Weekdays)
	if err != nil {
		return alerts.MaintenanceWindow{}, fmt.Errorf("encode maintenance weekdays: %w", err)
	}
	now := s.now().UTC()
	window.CreatedAt, window.UpdatedAt = now, now
	_, err = s.db.ExecContext(ctx, `INSERT INTO alert_maintenance_windows(
		id, name, node_selector, resource_type, resource_selector, weekdays_json, start_minute, duration_ms, timezone, enabled, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, window.ID, window.Name, window.NodeSelector, window.ResourceType,
		window.ResourceSelector, string(weekdays), window.StartMinute, window.Duration.Milliseconds(), window.Timezone, window.Enabled,
		formatAlertTime(now), formatAlertTime(now))
	if err != nil {
		return alerts.MaintenanceWindow{}, fmt.Errorf("create maintenance window: %w", err)
	}
	return window, nil
}

func (s *Store) UpdateMaintenanceWindow(ctx context.Context, id string, window alerts.MaintenanceWindow) (alerts.MaintenanceWindow, error) {
	window.ID = id
	window = alerts.NormalizeMaintenance(window)
	if err := alerts.ValidateMaintenance(window); err != nil {
		return alerts.MaintenanceWindow{}, err
	}
	weekdays, err := json.Marshal(window.Weekdays)
	if err != nil {
		return alerts.MaintenanceWindow{}, fmt.Errorf("encode maintenance weekdays: %w", err)
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE alert_maintenance_windows SET name = ?, node_selector = ?, resource_type = ?, resource_selector = ?,
		weekdays_json = ?, start_minute = ?, duration_ms = ?, timezone = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		window.Name, window.NodeSelector, window.ResourceType, window.ResourceSelector, string(weekdays), window.StartMinute,
		window.Duration.Milliseconds(), window.Timezone, window.Enabled, formatAlertTime(now), id)
	if err != nil {
		return alerts.MaintenanceWindow{}, fmt.Errorf("update maintenance window: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return alerts.MaintenanceWindow{}, fmt.Errorf("update maintenance window: %w", err)
	}
	if count == 0 {
		return alerts.MaintenanceWindow{}, ErrNotFound
	}
	window.CreatedAt = time.Time{}
	window.UpdatedAt = now
	return window, nil
}

func (s *Store) DeleteMaintenanceWindow(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM alert_maintenance_windows WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete maintenance window: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete maintenance window: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func scanMaintenanceWindow(row scanner) (alerts.MaintenanceWindow, error) {
	var window alerts.MaintenanceWindow
	var weekdays, createdAt, updatedAt string
	var durationMS int64
	if err := row.Scan(&window.ID, &window.Name, &window.NodeSelector, &window.ResourceType, &window.ResourceSelector,
		&weekdays, &window.StartMinute, &durationMS, &window.Timezone, &window.Enabled, &createdAt, &updatedAt); err != nil {
		return alerts.MaintenanceWindow{}, err
	}
	if err := json.Unmarshal([]byte(weekdays), &window.Weekdays); err != nil {
		return alerts.MaintenanceWindow{}, fmt.Errorf("decode maintenance weekdays: %w", err)
	}
	var err error
	if window.CreatedAt, err = parseAlertTime(createdAt); err != nil {
		return alerts.MaintenanceWindow{}, err
	}
	if window.UpdatedAt, err = parseAlertTime(updatedAt); err != nil {
		return alerts.MaintenanceWindow{}, err
	}
	window.Duration = time.Duration(durationMS) * time.Millisecond
	if err := alerts.ValidateMaintenance(window); err != nil {
		return alerts.MaintenanceWindow{}, err
	}
	return window, nil
}

func scanAlertState(row scanner) (alerts.AlertState, error) {
	var state alerts.AlertState
	var pending, firing, resolved, evaluated, notified, acknowledged, silenced sql.NullString
	if err := row.Scan(&state.RuleID, &state.NodeID, &state.ResourceType, &state.ResourceID,
		&state.Status, &pending, &firing, &resolved, &evaluated, &notified, &state.LastValue,
		&state.CleanEvaluations, &acknowledged, &state.AcknowledgedBy, &silenced, &state.SilencedBy,
		&state.Revision); err != nil {
		return alerts.AlertState{}, err
	}
	var err error
	if state.PendingSince, err = parseNullableAlertTime(pending); err != nil {
		return alerts.AlertState{}, err
	}
	if state.FiringSince, err = parseNullableAlertTime(firing); err != nil {
		return alerts.AlertState{}, err
	}
	if state.ResolvedAt, err = parseNullableAlertTime(resolved); err != nil {
		return alerts.AlertState{}, err
	}
	if !evaluated.Valid {
		return alerts.AlertState{}, errors.New("alert state last_evaluated_at is null")
	}
	if state.LastEvaluatedAt, err = parseAlertTime(evaluated.String); err != nil {
		return alerts.AlertState{}, err
	}
	if state.LastNotifiedAt, err = parseNullableAlertTime(notified); err != nil {
		return alerts.AlertState{}, err
	}
	if state.AcknowledgedAt, err = parseNullableAlertTime(acknowledged); err != nil {
		return alerts.AlertState{}, err
	}
	if state.SilencedUntil, err = parseNullableAlertTime(silenced); err != nil {
		return alerts.AlertState{}, err
	}
	return state, nil
}

func scanAlertEvent(row scanner) (alerts.AlertEvent, error) {
	var event alerts.AlertEvent
	var occurredAt string
	if err := row.Scan(&event.ID, &event.RuleID, &event.NodeID, &event.ResourceType,
		&event.ResourceID, &event.Type, &event.Status, &event.Severity, &event.Value,
		&event.Message, &event.Actor, &occurredAt); err != nil {
		return alerts.AlertEvent{}, err
	}
	var err error
	event.OccurredAt, err = parseAlertTime(occurredAt)
	return event, err
}

func scanAlertDelivery(row scanner) (alerts.Delivery, error) {
	var delivery alerts.Delivery
	var next, created string
	var delivered sql.NullString
	if err := row.Scan(&delivery.ID, &delivery.RuleID, &delivery.NodeID, &delivery.ResourceType,
		&delivery.ResourceID, &delivery.Kind, &delivery.Severity, &delivery.Title,
		&delivery.Message, &delivery.Status, &delivery.Attempts, &next, &delivery.LastError,
		&created, &delivered); err != nil {
		return alerts.Delivery{}, err
	}
	var err error
	delivery.NextAttemptAt, err = parseAlertTime(next)
	if err != nil {
		return alerts.Delivery{}, err
	}
	delivery.CreatedAt, err = parseAlertTime(created)
	if err != nil {
		return alerts.Delivery{}, err
	}
	delivery.DeliveredAt, err = parseNullableAlertTime(delivered)
	return delivery, err
}

func loadStateAndSeverity(ctx context.Context, tx *sql.Tx, key alerts.AlertKey) (alerts.AlertState, alerts.Severity, error) {
	state, err := scanAlertState(tx.QueryRowContext(ctx, alertStateSelect+`
		WHERE rule_id = ? AND node_id = ? AND resource_type = ? AND resource_id = ?`,
		key.RuleID, key.NodeID, key.ResourceType, key.ResourceID))
	if err != nil {
		return alerts.AlertState{}, "", err
	}
	var severity alerts.Severity
	if err := tx.QueryRowContext(ctx, "SELECT severity FROM alert_rules WHERE id = ?", key.RuleID).Scan(&severity); err != nil {
		return alerts.AlertState{}, "", err
	}
	return state, severity, nil
}

func insertAlertState(ctx context.Context, tx *sql.Tx, state alerts.AlertState) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO alert_states (
		rule_id, node_id, resource_type, resource_id, status, pending_since, firing_since,
		resolved_at, last_evaluated_at, last_notified_at, last_value, clean_evaluations,
		acknowledged_at, acknowledged_by, silenced_until, silenced_by, revision
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		state.RuleID, state.NodeID, state.ResourceType, state.ResourceID, state.Status,
		nullableAlertTime(state.PendingSince), nullableAlertTime(state.FiringSince),
		nullableAlertTime(state.ResolvedAt), formatAlertTime(state.LastEvaluatedAt),
		nullableAlertTime(state.LastNotifiedAt), state.LastValue, state.CleanEvaluations,
		nullableAlertTime(state.AcknowledgedAt), state.AcknowledgedBy,
		nullableAlertTime(state.SilencedUntil), state.SilencedBy, state.Revision)
	if err != nil {
		return fmt.Errorf("insert alert state: %w", err)
	}
	return nil
}

func updateAlertState(ctx context.Context, tx *sql.Tx, state alerts.AlertState, expectedRevision int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE alert_states SET
		status = ?, pending_since = ?, firing_since = ?, resolved_at = ?,
		last_evaluated_at = ?, last_notified_at = ?, last_value = ?, clean_evaluations = ?,
		acknowledged_at = ?, acknowledged_by = ?, silenced_until = ?, silenced_by = ?, revision = ?
		WHERE rule_id = ? AND node_id = ? AND resource_type = ? AND resource_id = ? AND revision = ?`,
		state.Status, nullableAlertTime(state.PendingSince), nullableAlertTime(state.FiringSince),
		nullableAlertTime(state.ResolvedAt), formatAlertTime(state.LastEvaluatedAt),
		nullableAlertTime(state.LastNotifiedAt), state.LastValue, state.CleanEvaluations,
		nullableAlertTime(state.AcknowledgedAt), state.AcknowledgedBy,
		nullableAlertTime(state.SilencedUntil), state.SilencedBy, state.Revision,
		state.RuleID, state.NodeID, state.ResourceType, state.ResourceID, expectedRevision)
	if err != nil {
		return fmt.Errorf("update alert state: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update alert state: %w", err)
	}
	if affected != 1 {
		return alerts.ErrStaleTransition
	}
	return nil
}

func insertAlertEvent(ctx context.Context, tx *sql.Tx, event alerts.AlertEvent) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO alert_events (
		rule_id, node_id, resource_type, resource_id, event_type, status, severity,
		value, message, actor, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RuleID, event.NodeID, event.ResourceType, event.ResourceID, event.Type,
		event.Status, event.Severity, event.Value, event.Message, event.Actor,
		formatAlertTime(event.OccurredAt))
	if err != nil {
		return fmt.Errorf("insert alert event: %w", err)
	}
	return nil
}

func insertAlertDelivery(ctx context.Context, tx *sql.Tx, delivery alerts.Delivery) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO alert_deliveries (
		rule_id, node_id, resource_type, resource_id, kind, severity, title, message,
		status, attempts, next_attempt_at, last_error, created_at, delivered_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		delivery.RuleID, delivery.NodeID, delivery.ResourceType, delivery.ResourceID,
		delivery.Kind, delivery.Severity, delivery.Title, delivery.Message, delivery.Status,
		delivery.Attempts, formatAlertTime(delivery.NextAttemptAt), delivery.LastError,
		formatAlertTime(delivery.CreatedAt), nullableAlertTime(delivery.DeliveredAt))
	if err != nil {
		return fmt.Errorf("insert alert delivery: %w", err)
	}
	return nil
}

func supersedeAlertKeyFiringDeliveries(ctx context.Context, tx *sql.Tx, key alerts.AlertKey, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE alert_deliveries
		SET status = 'superseded', last_error = ?
		WHERE rule_id = ? AND node_id = ? AND resource_type = ? AND resource_id = ?
			AND kind = 'firing' AND status IN ('pending', 'processing')`,
		reason, key.RuleID, key.NodeID, key.ResourceType, key.ResourceID)
	return err
}

func supersedeAlertKeyResolvedDeliveries(ctx context.Context, tx *sql.Tx, key alerts.AlertKey, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE alert_deliveries
		SET status = 'superseded', last_error = ?
		WHERE rule_id = ? AND node_id = ? AND resource_type = ? AND resource_id = ?
			AND kind = 'resolved' AND status IN ('pending', 'processing')`,
		reason, key.RuleID, key.NodeID, key.ResourceType, key.ResourceID)
	return err
}

func supersedeAlertRuleDeliveries(ctx context.Context, tx *sql.Tx, ruleID, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE alert_deliveries
		SET status = 'superseded', last_error = ?
		WHERE rule_id = ? AND status IN ('pending', 'processing')`, reason, ruleID)
	return err
}

func (s *Store) updateAlertDelivery(ctx context.Context, id int64, attempt int, setClause string, args ...any) error {
	args = append(args, id, attempt)
	result, err := s.db.ExecContext(ctx, "UPDATE alert_deliveries SET "+setClause+" WHERE id = ? AND attempts = ? AND status = 'processing'", args...)
	if err != nil {
		return fmt.Errorf("update alert delivery: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update alert delivery: %w", err)
	}
	if affected == 0 {
		var status alerts.DeliveryStatus
		err := s.db.QueryRowContext(ctx, "SELECT status FROM alert_deliveries WHERE id = ?", id).Scan(&status)
		if err == nil && status == alerts.DeliverySuperseded {
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("load alert delivery after update conflict: %w", err)
		}
		return ErrNotFound
	}
	return nil
}

func formatAlertTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableAlertTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatAlertTime(*value)
}

func parseAlertTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse alert timestamp: %w", err)
	}
	return parsed, nil
}

func parseNullableAlertTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseAlertTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func boundedAlertLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 100
	}
	return limit
}
