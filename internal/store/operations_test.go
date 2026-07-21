package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/operations"
)

func TestOperationalEventStoreRecordsFiltersAndBoundsTimeline(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	base := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return base }

	created, err := database.RecordOperationalEvent(ctx, operations.Event{
		Type: operations.EventServiceHealthChanged, Source: operations.SourceAutomatic,
		Title: "Immich probe changed to down", NodeID: "local", ServiceID: "immich",
	})
	if err != nil || created.ID == 0 || !created.OccurredAt.Equal(base) || created.Visibility != operations.VisibilityNormal {
		t.Fatalf("record automatic = %#v, err = %v", created, err)
	}
	base = base.Add(time.Minute)
	manual, err := database.CreateManualOperationalEvent(ctx, operations.Event{
		Type: operations.EventDeploy, Title: "Deployed immich v1.132.0", ServiceID: "immich", Actor: "admin@example.com",
	})
	if err != nil || manual.Source != operations.SourceManual || manual.ID <= created.ID {
		t.Fatalf("record manual = %#v, err = %v", manual, err)
	}
	if _, err := database.CreateManualOperationalEvent(ctx, operations.Event{
		Type: operations.EventServiceHealthChanged, Title: "invalid manual type",
	}); !errors.Is(err, operations.ErrInvalidEvent) {
		t.Fatalf("invalid manual event error = %v", err)
	}

	events, err := database.ListOperationalEvents(ctx, operations.Filter{ServiceID: "immich"})
	if err != nil || len(events) != 2 || events[0].ID != manual.ID || events[1].ID != created.ID {
		t.Fatalf("service events = %#v, err = %v", events, err)
	}
	events, err = database.ListOperationalEvents(ctx, operations.Filter{Type: operations.EventDeploy})
	if err != nil || len(events) != 1 || events[0].ID != manual.ID {
		t.Fatalf("deploy events = %#v, err = %v", events, err)
	}
	events, err = database.ListOperationalEvents(ctx, operations.Filter{NodeID: "local"})
	if err != nil || len(events) != 1 || events[0].ID != created.ID {
		t.Fatalf("node events = %#v, err = %v", events, err)
	}
	events, err = database.ListOperationalEvents(ctx, operations.Filter{
		From: base.Add(-30 * time.Second), To: base.Add(30 * time.Second), Limit: 1,
	})
	if err != nil || len(events) != 1 || events[0].ID != manual.ID {
		t.Fatalf("time-bounded event = %#v, err = %v", events, err)
	}
}

func TestOperationalEventStorePurgeKeepsRecentRecords(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "operations-retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	for _, occurredAt := range []time.Time{now.Add(-operations.Retention - time.Second), now.Add(-operations.Retention + time.Second)} {
		if _, err := database.RecordOperationalEvent(ctx, operations.Event{
			Type: operations.EventNodeConnected, Source: operations.SourceAutomatic,
			Title: "Node connected", NodeID: "node-one", OccurredAt: occurredAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := database.PurgeOperationalEvents(ctx, now.Add(-operations.Retention))
	if err != nil || deleted != 1 {
		t.Fatalf("purge = %d, err = %v", deleted, err)
	}
	events, err := database.ListOperationalEvents(ctx, operations.Filter{})
	if err != nil || len(events) != 1 {
		t.Fatalf("remaining events = %#v, err = %v", events, err)
	}
}

func TestOperationalEventsMigrationIsApplied(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "operations-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err := database.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = '010_operational_events.sql'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("operational migration count = %d, err = %v", count, err)
	}
}
