package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
	"github.com/bnhminh1010/homelab-dashboard/internal/dashboardconfig"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/slo"
	"github.com/bnhminh1010/homelab-dashboard/internal/topology"
)

func TestDashboardConfigStoreExportMergeReplaceAndSecretExclusion(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for _, service := range []model.Service{
		{ID: "svc_keep", Name: "Keep", DisplayURL: "https://keep.example"},
		{ID: "svc_old", Name: "Old", DisplayURL: "https://old.example"},
	} {
		if _, err := store.CreateService(ctx, service); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UpsertSLOPolicy(ctx, slo.Policy{
		ServiceID: "svc_keep", TargetPercent: 99.9, WindowDays: 30,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTopologyDependency(ctx, topology.DependencyInput{
		NodeID: "local", DependentServiceID: "svc_keep", DependencyServiceID: "svc_old", Label: "database",
	}); err != nil {
		t.Fatal(err)
	}
	rule := alerts.DefaultRules()[0]
	rule.ID = "rule_cpu"
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	credential := []byte("node-credential-secret-000000000")
	if len(credential) != 32 {
		t.Fatalf("test credential length = %d", len(credential))
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO nodes(id, display_name, hostname, credential_hash, created_at, updated_at)
		VALUES ('node_one', 'Node One', 'node-one', ?, ?, ?)`, credential, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO history_nodes(id, display_name, created_at) VALUES ('node_one', 'Node One', ?)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAudit(ctx, model.AuditEvent{
		Actor: "admin@example.com", Action: "secret.test", TargetType: "test", TargetID: "one",
		Outcome: "success", Metadata: map[string]any{"secret": "audit-secret-must-not-export"},
	}); err != nil {
		t.Fatal(err)
	}

	manager, err := dashboardconfig.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := manager.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		credential, []byte("audit-secret-must-not-export"), []byte("credential_hash"),
		[]byte("ntfy"), []byte("session"), []byte("audit_events"),
	} {
		if bytes.Contains(exported, forbidden) {
			t.Fatalf("export contains forbidden value %q: %s", forbidden, exported)
		}
	}
	document, err := dashboardconfig.Decode(exported)
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != dashboardconfig.DocumentVersion || len(document.SLOPolicies) != 1 ||
		len(document.Dependencies) != 1 || document.Dependencies[0].Label != "database" {
		t.Fatalf("exported v2 reliability config = %+v", document)
	}
	document.Services = []dashboardconfig.ServiceConfig{
		{ID: "svc_keep", Name: "Keep Updated", DisplayURL: "https://keep.example"},
		{ID: "svc_new", Name: "New", DisplayURL: "https://new.example"},
	}
	document.AlertRules = []dashboardconfig.AlertRuleConfig{}
	document.SLOPolicies = []dashboardconfig.SLOPolicyConfig{
		{ServiceID: "svc_keep", TargetPercent: 99.5, WindowDays: 7},
		{ServiceID: "svc_new", TargetPercent: 99.0, WindowDays: 30},
	}
	document.Dependencies = []dashboardconfig.DependencyConfig{
		{NodeID: "local", DependentServiceID: "svc_keep", DependencyServiceID: "svc_new", Label: "cache"},
	}
	document.UIPreferences.TerminalHeight = 280
	document.Nodes = []dashboardconfig.NodeMetadata{
		{ID: "node_one", DisplayName: "Compute One", Hostname: "compute-one"},
		{ID: "node_unknown", DisplayName: "Unknown", Hostname: "unknown"},
	}
	mergeRaw, _ := json.Marshal(document)
	mergePreview, err := manager.Preview(ctx, mergeRaw, "")
	if err != nil {
		t.Fatal(err)
	}
	mergeResult, err := manager.Apply(ctx, mergeRaw, "", "admin@example.com", mergePreview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if mergeResult.Preview.Mode != dashboardconfig.ImportMerge {
		t.Fatalf("default mode = %q", mergeResult.Preview.Mode)
	}
	snapshot, err := store.LoadDashboardConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Services) != 3 || len(snapshot.AlertRules) != 1 || len(snapshot.SLOPolicies) != 2 ||
		len(snapshot.Dependencies) != 2 ||
		snapshot.UIPreferences.TerminalHeight != 280 || len(snapshot.Nodes) != 1 ||
		snapshot.Nodes[0].DisplayName != "Compute One" || snapshot.Nodes[0].Hostname != "compute-one" {
		t.Fatalf("unexpected merged snapshot: %+v", snapshot)
	}
	var storedCredential []byte
	if err := store.db.QueryRowContext(ctx, "SELECT credential_hash FROM nodes WHERE id = 'node_one'").Scan(&storedCredential); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedCredential, credential) {
		t.Fatalf("node credential changed: got %q want %q", storedCredential, credential)
	}

	replaceDocument := document
	replaceDocument.Services = []dashboardconfig.ServiceConfig{
		{ID: "svc_new", Name: "New", DisplayURL: "https://new.example"},
	}
	replaceDocument.AlertRules = []dashboardconfig.AlertRuleConfig{}
	replaceDocument.SLOPolicies = []dashboardconfig.SLOPolicyConfig{
		{ServiceID: "svc_new", TargetPercent: 99.0, WindowDays: 30},
	}
	replaceDocument.Dependencies = []dashboardconfig.DependencyConfig{}
	replaceDocument.Nodes = []dashboardconfig.NodeMetadata{}
	replaceRaw, _ := json.Marshal(replaceDocument)
	replacePreview, err := manager.Preview(ctx, replaceRaw, dashboardconfig.ImportReplace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ctx, replaceRaw, dashboardconfig.ImportReplace, "admin@example.com", replacePreview.Revision); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.LoadDashboardConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Services) != 1 || snapshot.Services[0].ID != "svc_new" ||
		len(snapshot.AlertRules) != 0 || len(snapshot.SLOPolicies) != 1 ||
		len(snapshot.Dependencies) != 0 || len(snapshot.Nodes) != 1 {
		t.Fatalf("unexpected replaced snapshot: %+v", snapshot)
	}
	events, err := store.ListAudit(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	var imports int
	for _, event := range events {
		if event.Action == "config.import" && event.Actor == "admin@example.com" {
			imports++
		}
	}
	if imports != 2 {
		t.Fatalf("config import audit count = %d; events=%+v", imports, events)
	}
}

func TestDashboardConfigStoreRejectsStaleRevisionInsideTransaction(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	current, err := database.LoadDashboardConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := dashboardconfig.Revision(current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateService(ctx, model.Service{
		ID: "svc_concurrent", Name: "Concurrent", DisplayURL: "https://concurrent.example",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.ApplyDashboardConfig(ctx, current, dashboardconfig.ImportReplace, "admin@example.com", revision); !errors.Is(err, dashboardconfig.ErrRevisionConflict) {
		t.Fatalf("stale transaction revision error = %v", err)
	}
	services, err := database.ListServices(ctx)
	if err != nil || len(services) != 1 || services[0].ID != "svc_concurrent" {
		t.Fatalf("concurrent service was mutated: %+v err=%v", services, err)
	}
}

func TestDashboardPreferencesRejectMissingOrRevokedDefaultNode(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	preferences := dashboardconfig.DefaultUIPreferences()
	preferences.DefaultNodeID = "missing"
	if _, err := database.UpdateDashboardUIPreferences(ctx, preferences, "admin@example.com"); !errors.Is(err, dashboardconfig.ErrInvalidDocument) {
		t.Fatalf("missing default node error = %v", err)
	}
	preferences.DefaultNodeID = "local"
	if _, err := database.UpdateDashboardUIPreferences(ctx, preferences, "admin@example.com"); err != nil {
		t.Fatalf("local default node: %v", err)
	}
}

func TestDashboardPreferencesPersistWorkspaceLayout(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	preferences := dashboardconfig.DefaultUIPreferences()
	preferences.HiddenWorkspaces = []string{dashboardconfig.WorkspaceTopology, dashboardconfig.WorkspaceNodes}
	preferences.WorkspaceOrder = []string{
		dashboardconfig.WorkspaceOverview, dashboardconfig.WorkspaceAlerts, dashboardconfig.WorkspaceServices,
		dashboardconfig.WorkspaceContainers, dashboardconfig.WorkspaceNodes, dashboardconfig.WorkspaceHistory,
		dashboardconfig.WorkspaceLogs, dashboardconfig.WorkspaceTopology,
	}
	preferences.HiddenOverviewWidgets = []string{dashboardconfig.OverviewWidgetRecentChanges}
	preferences.OverviewWidgetSizes[dashboardconfig.OverviewWidgetTrend] = dashboardconfig.OverviewWidgetSizeFull
	updated, err := database.UpdateDashboardUIPreferences(ctx, preferences, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetDashboardUIPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dashboardconfig.EqualUIPreferences(updated, stored) {
		t.Fatalf("stored workspace preferences = %+v, want %+v", stored, updated)
	}
	snapshot, err := database.LoadDashboardConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dashboardconfig.EqualUIPreferences(snapshot.UIPreferences, preferences) {
		t.Fatalf("snapshot workspace preferences = %+v, want %+v", snapshot.UIPreferences, preferences)
	}
}

func TestDashboardConfigStoreRejectsTopologyForRevokedNodeInsideApplyTransaction(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "revoked-topology.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, service := range []model.Service{
		{ID: "svc_api", Name: "API", DisplayURL: "https://api.example"},
		{ID: "svc_db", Name: "Database", DisplayURL: "https://db.example"},
	} {
		if _, err := database.CreateService(ctx, service); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO nodes(id, display_name, hostname, credential_hash, created_at, updated_at)
		VALUES ('node_revoked', 'Revoked', 'revoked', zeroblob(32), ?, ?)`, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := database.RevokeNode(ctx, "node_revoked", now); err != nil {
		t.Fatal(err)
	}
	current, err := database.LoadDashboardConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := dashboardconfig.Revision(current)
	if err != nil {
		t.Fatal(err)
	}
	current.Dependencies = []topology.Dependency{{
		NodeID: "node_revoked", DependentServiceID: "svc_api", DependencyServiceID: "svc_db",
	}}
	if err := database.ApplyDashboardConfig(ctx, current, dashboardconfig.ImportMerge, "admin@example.com", revision); !errors.Is(err, dashboardconfig.ErrInvalidDocument) {
		t.Fatalf("revoked topology import error = %v", err)
	}
	dependencies, err := database.ListTopologyDependencies(ctx, "node_revoked")
	if err != nil || len(dependencies) != 0 {
		t.Fatalf("revoked topology dependency persisted: %#v, %v", dependencies, err)
	}
}

func TestDashboardConfigStoreRollsBackWholeImport(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateService(ctx, model.Service{
		ID: "svc_existing", Name: "Existing", DisplayURL: "https://existing.example",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER fail_dashboard_config_import
		BEFORE INSERT ON services
		WHEN NEW.id = 'svc_fail'
		BEGIN
			SELECT RAISE(ABORT, 'forced import failure');
		END`); err != nil {
		t.Fatal(err)
	}
	document := dashboardconfig.Document{
		Version: dashboardconfig.DocumentVersion,
		Services: []dashboardconfig.ServiceConfig{
			{ID: "svc_ok", Name: "Would Persist Without Rollback", DisplayURL: "https://ok.example"},
			{ID: "svc_fail", Name: "Failure", DisplayURL: "https://fail.example"},
		},
		AlertRules:    []dashboardconfig.AlertRuleConfig{},
		SLOPolicies:   []dashboardconfig.SLOPolicyConfig{},
		Dependencies:  []dashboardconfig.DependencyConfig{},
		UIPreferences: dashboardconfig.DefaultUIPreferences(),
		Nodes:         []dashboardconfig.NodeMetadata{},
	}
	raw, _ := json.Marshal(document)
	manager, _ := dashboardconfig.NewService(store)
	preview, err := manager.Preview(ctx, raw, dashboardconfig.ImportReplace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ctx, raw, dashboardconfig.ImportReplace, "admin@example.com", preview.Revision); err == nil {
		t.Fatal("forced import unexpectedly succeeded")
	}
	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].ID != "svc_existing" {
		t.Fatalf("partial service mutations survived rollback: %+v", services)
	}
	events, err := store.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("audit event survived failed transaction: %+v", events)
	}
}

func TestDashboardConfigStoreRollsBackSLOAndTopologyWithImport(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateService(ctx, model.Service{
		ID: "svc_existing", Name: "Existing", DisplayURL: "https://existing.example",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER fail_imported_topology
		BEFORE INSERT ON topology_dependencies
		BEGIN
			SELECT RAISE(ABORT, 'forced topology import failure');
		END`); err != nil {
		t.Fatal(err)
	}
	document := dashboardconfig.Document{
		Version: dashboardconfig.DocumentVersion,
		Services: []dashboardconfig.ServiceConfig{
			{ID: "svc_api", Name: "API", DisplayURL: "https://api.example"},
			{ID: "svc_db", Name: "Database", DisplayURL: "https://db.example"},
		},
		AlertRules: []dashboardconfig.AlertRuleConfig{},
		SLOPolicies: []dashboardconfig.SLOPolicyConfig{
			{ServiceID: "svc_api", TargetPercent: 99.5, WindowDays: 30},
		},
		Dependencies: []dashboardconfig.DependencyConfig{
			{NodeID: "local", DependentServiceID: "svc_api", DependencyServiceID: "svc_db"},
		},
		UIPreferences: dashboardconfig.DefaultUIPreferences(),
		Nodes:         []dashboardconfig.NodeMetadata{},
	}
	raw, _ := json.Marshal(document)
	manager, _ := dashboardconfig.NewService(store)
	preview, err := manager.Preview(ctx, raw, dashboardconfig.ImportReplace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ctx, raw, dashboardconfig.ImportReplace, "admin@example.com", preview.Revision); err == nil {
		t.Fatal("topology failure unexpectedly committed import")
	}
	snapshot, err := store.LoadDashboardConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Services) != 1 || snapshot.Services[0].ID != "svc_existing" ||
		len(snapshot.SLOPolicies) != 0 || len(snapshot.Dependencies) != 0 {
		t.Fatalf("SLO/topology import was not atomic: %+v", snapshot)
	}
}
