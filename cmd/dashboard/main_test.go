package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
	"github.com/bnhminh1010/homelab-dashboard/internal/hostagent"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/monitoring"
	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
	"github.com/bnhminh1010/homelab-dashboard/internal/store"
	"github.com/bnhminh1010/homelab-dashboard/internal/terminal"
)

func TestHostSessionAdapterMapsAgentTimeouts(t *testing.T) {
	tests := []struct {
		name string
		from error
		want error
	}{
		{name: "idle", from: hostagent.ErrIdleTimeout, want: terminal.ErrIdleTimeout},
		{name: "maximum duration", from: hostagent.ErrHardTimeout, want: terminal.ErrHardTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := hostSessionAdapter{Session: stubHostSession{readErr: test.from}}
			_, err := adapter.Read(make([]byte, 1))
			if !errors.Is(err, test.want) {
				t.Fatalf("Read() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestLoadSecretFileRequiresOneProtectedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ntfy-token")
	if err := os.WriteFile(path, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := loadSecretFile(path); err != nil || value != "secret-token" {
		t.Fatalf("loadSecretFile() = %q, %v", value, err)
	}
	if err := os.WriteFile(path, []byte("first\nsecond"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSecretFile(path); err == nil {
		t.Fatal("multi-line token was accepted")
	}
	if err := os.WriteFile(path, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSecretFile(path); err == nil {
		t.Fatal("group-readable token was accepted")
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if value, err := loadSecretFile(path); err != nil || value != "secret-token" {
		t.Fatalf("owner-read-only secret = %q, %v", value, err)
	}
}

func TestRunOperationalRetentionRunsImmediatelyAndRepeats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repository := &stubOperationalRetainer{calls: make(chan time.Time, 16)}
	done := make(chan error, 1)
	go func() {
		done <- runOperationalRetention(ctx, repository, time.Millisecond)
	}()

	for call := 0; call < 2; call++ {
		select {
		case retainedAt := <-repository.calls:
			if retainedAt.IsZero() || retainedAt.Location() != time.UTC {
				t.Fatalf("retention call timestamp = %v", retainedAt)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for retention call %d", call+1)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runOperationalRetention() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("operational retention worker did not stop")
	}
}

func TestRunOperationalRetentionReturnsStartupFailure(t *testing.T) {
	want := errors.New("database unavailable")
	repository := &stubOperationalRetainer{calls: make(chan time.Time, 1), err: want}
	err := runOperationalRetention(context.Background(), repository, time.Hour)
	if !errors.Is(err, want) {
		t.Fatalf("runOperationalRetention() error = %v, want %v", err, want)
	}
}

func TestShouldEvaluateNodeAvailabilityHonorsInitialConnectionGrace(t *testing.T) {
	now := time.Date(2026, 7, 20, 7, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-time.Second)
	tests := []struct {
		name  string
		state nodes.NodeState
		want  bool
	}{
		{
			name: "fresh enrollment is skipped",
			state: nodes.NodeState{Node: nodes.Node{
				ID: "fresh", CreatedAt: now.Add(-initialNodeConnectionGrace + time.Nanosecond),
			}},
			want: false,
		},
		{
			name: "never connected is offline at grace boundary",
			state: nodes.NodeState{Node: nodes.Node{
				ID: "expired", CreatedAt: now.Add(-initialNodeConnectionGrace),
			}},
			want: true,
		},
		{
			name:  "previously connected offline node remains evaluated",
			state: nodes.NodeState{Node: nodes.Node{ID: "seen", CreatedAt: now}, LastSeenAt: &lastSeen},
			want:  true,
		},
		{
			name:  "online node remains evaluated",
			state: nodes.NodeState{Node: nodes.Node{ID: "online", CreatedAt: now}, Online: true},
			want:  true,
		},
		{
			name:  "invalid node is skipped",
			state: nodes.NodeState{Node: nodes.Node{CreatedAt: now.Add(-time.Hour)}},
			want:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldEvaluateNodeAvailability(test.state, now); got != test.want {
				t.Fatalf("shouldEvaluateNodeAvailability() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWorkerGroupWaitHonorsShutdownDeadline(t *testing.T) {
	release := make(chan struct{})
	group := &workerGroup{}
	group.Go(context.Background(), make(chan error, 1), "blocking worker", func(context.Context) error {
		<-release
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := group.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := group.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() after worker exit: %v", err)
	}
}

func TestDashboardAlertSourceRetainsLastBackupAlertWhenReportDisappears(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC()
	if err := database.UpsertBackupObservation(context.Background(), "local", model.BackupStatus{
		Job: "nightly", Status: "success", CompletedAt: now.Add(-48 * time.Hour), ExpectedWithinSeconds: 24 * 60 * 60,
	}, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	source := dashboardAlertSource{runtime: &podmanSource{}, repository: database}
	items, err := source.alertsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == "backup:local:nightly" && item.Source == "local/backup" {
			return
		}
	}
	t.Fatalf("persisted overdue backup alert disappeared: %#v", items)
}

func TestDashboardAlertSourceIncludesOverdueBackupOnActiveRemoteNode(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	nodeService, err := nodes.NewService(database, nodes.Options{})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := nodeService.CreateEnrollment(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	remote, _, err := nodeService.Enroll(context.Background(), enrollment.Token, "Storage node", "storage-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := database.UpsertBackupObservation(context.Background(), remote.ID, model.BackupStatus{
		Job: "nightly", Status: "success", CompletedAt: now.Add(-48 * time.Hour), ExpectedWithinSeconds: 24 * 60 * 60,
	}, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	source := dashboardAlertSource{runtime: &podmanSource{}, repository: database}
	items, err := source.alertsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantID := "backup:" + remote.ID + ":nightly"
	wantSource := remote.ID + "/backup"
	for _, item := range items {
		if item.ID == wantID && item.Source == wantSource {
			return
		}
	}
	t.Fatalf("active remote overdue backup alert was omitted: %#v", items)
}

func TestBackupFreshnessQueuesNodeScopedDeliveriesForLocalAndActiveRemote(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "backup-freshness.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.SeedDefaultAlertRules(ctx, alerts.DefaultRules()); err != nil {
		t.Fatal(err)
	}

	nodeService, err := nodes.NewService(database, nodes.Options{})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := nodeService.CreateEnrollment(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	remote, _, err := nodeService.Enroll(ctx, enrollment.Token, "Storage node", "storage-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for _, nodeID := range []string{"local", remote.ID, "revoked-or-unknown"} {
		if err := database.UpsertBackupObservation(ctx, nodeID, model.BackupStatus{
			Job: "nightly", Status: "success", CompletedAt: now.Add(-48 * time.Hour), ExpectedWithinSeconds: 24 * 60 * 60,
		}, now.Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	engine, err := alerts.NewEngineWithOptions(database, alerts.ClockFunc(func() time.Time { return now }), alerts.EngineOptions{DeliveryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := monitoring.New(monitoring.Options{Alerts: engine})
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluateBackupFreshness(ctx, database, pipeline, now); err != nil {
		t.Fatal(err)
	}

	deliveries, err := database.ClaimDueAlertDeliveries(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("backup deliveries = %#v", deliveries)
	}
	for _, delivery := range deliveries {
		if delivery.RuleID != "default_backup_unhealthy" || delivery.ResourceType != "backup" || delivery.ResourceID != "nightly" {
			t.Fatalf("unexpected backup delivery = %#v", delivery)
		}
		if delivery.NodeID != "local" && delivery.NodeID != remote.ID {
			t.Fatalf("inactive node was queued for delivery = %#v", delivery)
		}
	}
}

type stubHostSession struct {
	readErr error
}

type stubOperationalRetainer struct {
	calls chan time.Time
	err   error
}

func (stub *stubOperationalRetainer) RetainOperationalData(_ context.Context, now time.Time) error {
	stub.calls <- now
	return stub.err
}

func (session stubHostSession) Read([]byte) (int, error)             { return 0, session.readErr }
func (stubHostSession) Write(data []byte) (int, error)               { return len(data), nil }
func (stubHostSession) Close() error                                 { return nil }
func (stubHostSession) Resize(context.Context, hostagent.Size) error { return nil }
func (stubHostSession) Info() hostagent.Info                         { return hostagent.Info{} }
func (stubHostSession) ExitCode() (int, bool)                        { return 0, false }

var _ hostagent.Session = stubHostSession{}
var _ io.ReadWriteCloser = stubHostSession{}
