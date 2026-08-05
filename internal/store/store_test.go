package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/services"
)

func TestServiceCRUDAndAudit(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.CreateService(ctx, model.Service{
		ID: "svc_1", Name: "Immich", Icon: "camera",
		DisplayURL: "https://immich.example", ProbeURL: "http://10.0.0.2/ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedAt.IsZero() || created.Status != model.ServiceStatusUnknown {
		t.Fatalf("unexpected created service: %+v", created)
	}

	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 {
		t.Fatalf("list services: len=%d err=%v", len(services), err)
	}
	updated, err := store.UpdateService(ctx, "svc_1", model.ServiceInput{
		Name: "Immich Photos", DisplayURL: "https://photos.example",
	})
	if err != nil || updated.Name != "Immich Photos" {
		t.Fatalf("update service: %+v err=%v", updated, err)
	}

	if err := store.AppendAudit(ctx, model.AuditEvent{
		Actor: "admin@example.com", Action: "service.update", TargetType: "service",
		TargetID: "svc_1", Outcome: "success", Metadata: map[string]any{"name": updated.Name},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ListAudit(ctx, 10)
	if err != nil || len(events) != 1 || events[0].Actor != "admin@example.com" {
		t.Fatalf("audit events: %+v err=%v", events, err)
	}

	if err := store.DeleteService(ctx, "svc_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetService(ctx, "svc_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.db")
	for range 2 {
		store, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreEnforcesServiceCapacityAtomically(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for index := 0; index < services.MaxServices; index++ {
		if _, err := database.CreateService(ctx, model.Service{
			ID: fmt.Sprintf("svc_%03d", index), Name: fmt.Sprintf("Service %d", index),
			DisplayURL: fmt.Sprintf("https://service-%d.example", index),
		}); err != nil {
			t.Fatalf("create service %d: %v", index, err)
		}
	}
	if _, err := database.CreateService(ctx, model.Service{
		ID: "svc_overflow", Name: "Overflow", DisplayURL: "https://overflow.example",
	}); !errors.Is(err, services.ErrServiceLimit) {
		t.Fatalf("overflow service error = %v", err)
	}
}

func TestWidgetContentRevisionAndPersistence(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "widgets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	items, revision, err := database.ListLaunchpadBookmarks(ctx)
	if err != nil || len(items) != 0 || revision != 0 {
		t.Fatalf("initial launchpad = %#v revision=%d err=%v", items, revision, err)
	}
	bookmark := model.LaunchpadBookmark{ID: "router", Title: "Router", URL: "https://router.example", Icon: "network", Tag: "NET"}
	nextRevision, err := database.ReplaceLaunchpadBookmarks(ctx, []model.LaunchpadBookmark{bookmark}, revision, "admin")
	if err != nil || nextRevision != 1 {
		t.Fatalf("save launchpad revision=%d err=%v", nextRevision, err)
	}
	if _, err := database.ReplaceLaunchpadBookmarks(ctx, nil, revision, "admin"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale launchpad write error=%v", err)
	}
	items, revision, err = database.ListLaunchpadBookmarks(ctx)
	if err != nil || revision != 1 || len(items) != 1 || items[0].URL != bookmark.URL {
		t.Fatalf("saved launchpad = %#v revision=%d err=%v", items, revision, err)
	}

	note, err := database.GetOperatorNote(ctx)
	if err != nil || note.Revision != 0 || note.Text != "" {
		t.Fatalf("initial operator note = %#v err=%v", note, err)
	}
	saved, err := database.UpdateOperatorNote(ctx, "Check backup retention", note.Revision, "admin")
	if err != nil || saved.Revision != 1 || saved.Text != "Check backup retention" {
		t.Fatalf("save operator note = %#v err=%v", saved, err)
	}
	if _, err := database.UpdateOperatorNote(ctx, "stale", note.Revision, "admin"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale operator note write error=%v", err)
	}
}
