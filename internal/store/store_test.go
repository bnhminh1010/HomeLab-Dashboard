package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/services"
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
