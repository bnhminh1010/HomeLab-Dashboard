package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOperationalRetentionBoundsTerminalDataAndKeepsActiveWork(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	oldEvent := now.Add(-operationalEventRetention - time.Second).Format(time.RFC3339Nano)
	newEvent := now.Add(-operationalEventRetention + time.Second).Format(time.RFC3339Nano)
	oldDelivery := now.Add(-operationalDeliveryRetention - time.Second).Format(time.RFC3339Nano)

	for _, at := range []string{oldEvent, newEvent} {
		if _, err := database.db.ExecContext(ctx, `
			INSERT INTO alert_events(
				rule_id, node_id, resource_type, resource_id, event_type, status,
				severity, value, message, occurred_at
			) VALUES ('rule', 'local', 'host', 'host', 'firing', 'firing',
				'warning', 1, 'event', ?)`, at); err != nil {
			t.Fatal(err)
		}
		if _, err := database.db.ExecContext(ctx, `
			INSERT INTO audit_events(actor, action, target_type, target_id, outcome, created_at)
			VALUES ('admin', 'test', 'test', 'one', 'success', ?)`, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{"delivered", "dead", "superseded", "pending", "processing"} {
		if _, err := database.db.ExecContext(ctx, `
			INSERT INTO alert_deliveries(
				rule_id, node_id, resource_type, resource_id, kind, severity, title,
				message, status, next_attempt_at, created_at
			) VALUES ('rule', 'local', 'host', 'host', 'firing', 'warning', 'title',
				'message', ?, ?, ?)`, status, oldDelivery, oldDelivery); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []struct {
		status string
		at     string
	}{
		{"resolved", oldEvent}, {"resolved", newEvent}, {"firing", oldEvent},
	} {
		if _, err := database.db.ExecContext(ctx, `
			INSERT INTO alert_states(
				rule_id, node_id, resource_type, resource_id, status,
				last_evaluated_at, last_value
			) VALUES (?, 'local', 'host', 'host', ?, ?, 1)`,
			"rule-"+state.status+state.at, state.status, state.at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO node_enrollments(id, token_hash, created_by, created_at, expires_at)
		VALUES ('expired', zeroblob(32), 'admin', ?, ?),
		       ('active', randomblob(32), 'admin', ?, ?)`,
		now.Add(-time.Hour).Unix(), now.Add(-time.Second).Unix(), now.Unix(), now.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	if err := database.RetainOperationalData(ctx, now); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, database.db, "alert_events", 1)
	assertTableCount(t, database.db, "audit_events", 1)
	assertTableCount(t, database.db, "alert_deliveries", 2)
	assertTableCount(t, database.db, "alert_states", 2)
	assertTableCount(t, database.db, "node_enrollments", 1)
	var activeStatuses int
	if err := database.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM alert_deliveries WHERE status IN ('pending', 'processing')`).Scan(&activeStatuses); err != nil || activeStatuses != 2 {
		t.Fatalf("active deliveries = %d err=%v", activeStatuses, err)
	}
	var firingStates int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alert_states WHERE status = 'firing'`).Scan(&firingStates); err != nil || firingStates != 1 {
		t.Fatalf("firing states = %d err=%v", firingStates, err)
	}
}
