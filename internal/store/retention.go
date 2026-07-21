package store

import (
	"context"
	"fmt"
	"time"
)

const (
	operationalEventRetention    = 90 * 24 * time.Hour
	operationalDeliveryRetention = 30 * 24 * time.Hour
)

// RetainOperationalData bounds non-metric tables that share the history
// SQLite database. Active incidents and queued work are never removed.
func (s *Store) RetainOperationalData(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("retain operational data: timestamp is required")
	}
	now = now.UTC()
	eventCutoff := now.Add(-operationalEventRetention).Format(time.RFC3339Nano)
	deliveryCutoff := now.Add(-operationalDeliveryRetention).Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin operational retention: %w", err)
	}
	defer tx.Rollback()
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM alert_events WHERE occurred_at < ?`, []any{eventCutoff}},
		{`DELETE FROM alert_deliveries
		  WHERE status IN ('delivered', 'dead', 'superseded') AND created_at < ?`, []any{deliveryCutoff}},
		{`DELETE FROM alert_states
		  WHERE status = 'resolved' AND last_evaluated_at < ?`, []any{eventCutoff}},
		{`DELETE FROM audit_events WHERE created_at < ?`, []any{eventCutoff}},
		{`DELETE FROM operational_events WHERE occurred_at < ?`, []any{eventCutoff}},
		{`DELETE FROM certificate_observations
		  WHERE checked_at < ?
		     OR NOT EXISTS (
		       SELECT 1 FROM services
		       WHERE services.id = certificate_observations.service_id
		         AND lower(services.display_url) LIKE 'https://%'
		         AND services.display_url = certificate_observations.endpoint_url
		     )`, []any{eventCutoff}},
		{`DELETE FROM backup_observations
		  WHERE observed_at < ?
		     OR (node_id <> 'local' AND NOT EXISTS (
		       SELECT 1 FROM nodes WHERE nodes.id = backup_observations.node_id AND nodes.revoked_at IS NULL
		     ))`, []any{eventCutoff}},
		{`DELETE FROM node_enrollments WHERE expires_at <= ?`, []any{now.Unix()}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("retain operational data: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit operational retention: %w", err)
	}
	return nil
}
