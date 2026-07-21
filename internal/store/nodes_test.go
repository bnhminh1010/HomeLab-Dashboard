package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/dashboardconfig"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/nodes"
	"github.com/binhminh/HomeLab-Minh/internal/topology"
)

func TestNodeEnrollmentPersistsOnlyCredentialHash(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "nodes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service, err := nodes.NewService(database, nodes.Options{Now: func() time.Time { return now }, Random: &storeCyclingReader{}})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := service.CreateEnrollment(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	node, credential, err := service.Enroll(ctx, enrollment.Token, "Compute One", "compute-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, node.ID, credential); err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err := database.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM nodes
		WHERE CAST(credential_hash AS TEXT) = ? OR CAST(credential_hash AS TEXT) = ?`,
		credential, enrollment.Token).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("plain enrollment or node credential was persisted")
	}
	if _, _, err := service.Enroll(ctx, enrollment.Token, "Again", "again"); !errors.Is(err, nodes.ErrEnrollmentInvalid) {
		t.Fatalf("reused token error = %v", err)
	}
	preferences := dashboardconfig.DefaultUIPreferences()
	preferences.DefaultNodeID = node.ID
	if _, err := database.UpdateDashboardUIPreferences(ctx, preferences, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []model.Service{
		{ID: "svc_node_api", Name: "API", DisplayURL: "https://api.example"},
		{ID: "svc_node_db", Name: "Database", DisplayURL: "https://db.example"},
	} {
		if _, err := database.CreateService(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.CreateTopologyDependency(ctx, topology.DependencyInput{
		NodeID: node.ID, DependentServiceID: "svc_node_api", DependencyServiceID: "svc_node_db",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertBackupObservation(ctx, node.ID, model.BackupStatus{
		Job: "nightly", Status: "success", CompletedAt: now, ExpectedWithinSeconds: 86400,
	}, now); err != nil {
		t.Fatal(err)
	}
	alertTime := now.Format(time.RFC3339Nano)
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO alert_states(rule_id, node_id, resource_type, resource_id, status, last_evaluated_at, last_value)
		VALUES ('rule-node', ?, 'node', ?, 'firing', ?, 0)`, node.ID, node.ID, alertTime); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO alert_deliveries(
			rule_id, node_id, resource_type, resource_id, kind, severity, title, message,
			status, next_attempt_at, created_at
		) VALUES ('rule-node', ?, 'node', ?, 'firing', 'critical', 'offline', 'offline',
			'pending', ?, ?)`, node.ID, node.ID, alertTime, alertTime); err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	var alertStates int
	if err := database.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM alert_states WHERE node_id = ?", node.ID).Scan(&alertStates); err != nil || alertStates != 0 {
		t.Fatalf("revoked alert states = %d err=%v", alertStates, err)
	}
	var deliveryStatus string
	if err := database.db.QueryRowContext(ctx, "SELECT status FROM alert_deliveries WHERE node_id = ?", node.ID).Scan(&deliveryStatus); err != nil || deliveryStatus != "superseded" {
		t.Fatalf("revoked delivery status = %q err=%v", deliveryStatus, err)
	}
	storedPreferences, err := database.GetDashboardUIPreferences(ctx)
	if err != nil || storedPreferences.DefaultNodeID != "local" {
		t.Fatalf("revoked default node = %q err=%v", storedPreferences.DefaultNodeID, err)
	}
	if _, err := service.Authenticate(ctx, node.ID, credential); !errors.Is(err, nodes.ErrNodeUnauthorized) {
		t.Fatalf("revoked credential error = %v", err)
	}
	listed, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("revoked nodes must not appear in active inventory: %#v", listed)
	}
	if dependencies, err := database.ListTopologyDependencies(ctx, node.ID); err != nil || len(dependencies) != 0 {
		t.Fatalf("revoked node topology = %#v err=%v", dependencies, err)
	}
	if backups, err := database.ListBackupObservations(ctx, node.ID); err != nil || len(backups) != 0 {
		t.Fatalf("revoked node backups = %#v err=%v", backups, err)
	}
	replacementEnrollment, err := service.CreateEnrollment(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Enroll(ctx, replacementEnrollment.Token, "Replacement", "replacement"); err != nil {
		t.Fatalf("revoked node did not release enrollment capacity: %v", err)
	}
}

type storeCyclingReader struct{ value byte }

func (reader *storeCyclingReader) Read(target []byte) (int, error) {
	for index := range target {
		reader.value++
		target[index] = reader.value
	}
	return len(target), nil
}
