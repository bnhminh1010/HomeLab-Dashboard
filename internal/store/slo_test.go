package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/slo"
)

func TestSLOPoliciesPersistAndFollowServiceLifecycle(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.CreateService(ctx, model.Service{
		ID: "svc_immich", Name: "Immich", DisplayURL: "https://immich.example",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := database.UpsertSLOPolicy(ctx, slo.Policy{
		ServiceID: "svc_immich", TargetPercent: 99.9, WindowDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.UpdatedAt == nil || created.UpdatedAt.IsZero() {
		t.Fatalf("saved policy missing updated timestamp: %+v", created)
	}
	policies, err := database.ListSLOPolicies(ctx)
	if err != nil || len(policies) != 1 || policies[0].TargetPercent != 99.9 {
		t.Fatalf("policies = %+v, err = %v", policies, err)
	}
	if err := database.DeleteService(ctx, "svc_immich"); err != nil {
		t.Fatal(err)
	}
	policies, err = database.ListSLOPolicies(ctx)
	if err != nil || len(policies) != 0 {
		t.Fatalf("policy should cascade on service delete: %+v, err=%v", policies, err)
	}
}

func TestSLOPolicyRejectsMissingServiceAndInvalidValues(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.UpsertSLOPolicy(ctx, slo.Policy{ServiceID: "missing", TargetPercent: 99.5, WindowDays: 30})
	if err == nil {
		t.Fatal("expected missing service foreign-key error")
	}
	if _, err := database.UpsertSLOPolicy(ctx, slo.Policy{ServiceID: "missing", TargetPercent: 100, WindowDays: 30}); !errors.Is(err, slo.ErrInvalidPolicy) {
		t.Fatalf("invalid policy error = %v", err)
	}
}
