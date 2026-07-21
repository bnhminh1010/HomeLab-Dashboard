package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/topology"
)

func TestTopologyDependenciesAreManualNodeScopedDirectedEdges(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "topology.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, id := range []string{"svc-web", "svc-db", "svc-cache"} {
		if _, err := database.CreateService(ctx, model.Service{
			ID: id, Name: id, DisplayURL: "http://" + id + ".local",
		}); err != nil {
			t.Fatalf("create service %q: %v", id, err)
		}
	}

	webToDB, err := database.CreateTopologyDependency(ctx, topology.DependencyInput{
		NodeID: "local", DependentServiceID: "svc-web", DependencyServiceID: "svc-db", Label: " database ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if webToDB.Label != "database" {
		t.Fatalf("normalized label = %q", webToDB.Label)
	}
	if _, err := database.CreateTopologyDependency(ctx, topology.DependencyInput{
		NodeID: "local", DependentServiceID: "svc-db", DependencyServiceID: "svc-web",
	}); err != nil {
		t.Fatalf("directed cycle must be permitted: %v", err)
	}
	if _, err := database.CreateTopologyDependency(ctx, topology.DependencyInput{
		NodeID: "local", DependentServiceID: "svc-web", DependencyServiceID: "svc-db",
	}); !errors.Is(err, topology.ErrDuplicateDependency) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := database.CreateTopologyDependency(ctx, topology.DependencyInput{
		NodeID: "local", DependentServiceID: "svc-web", DependencyServiceID: "svc-web",
	}); !errors.Is(err, topology.ErrSelfDependency) {
		t.Fatalf("self edge error = %v", err)
	}
	if _, err := database.CreateTopologyDependency(ctx, topology.DependencyInput{
		NodeID: "local", DependentServiceID: "svc-web", DependencyServiceID: "missing",
	}); !errors.Is(err, topology.ErrServiceNotFound) {
		t.Fatalf("unknown service error = %v", err)
	}
	if _, err := database.CreateTopologyDependency(ctx, topology.DependencyInput{
		NodeID: "compute-one", DependentServiceID: "svc-web", DependencyServiceID: "svc-cache",
	}); !errors.Is(err, topology.ErrNodeNotFound) {
		t.Fatalf("unenrolled node error = %v", err)
	}

	local, err := database.ListTopologyDependencies(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 2 {
		t.Fatalf("local edges = %#v", local)
	}
	other, err := database.ListTopologyDependencies(ctx, "compute-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("other node edges = %#v", other)
	}
	if err := database.DeleteTopologyDependency(ctx, "compute-one", webToDB.ID); !errors.Is(err, topology.ErrDependencyNotFound) {
		t.Fatalf("cross-node delete error = %v", err)
	}
	if err := database.DeleteTopologyDependency(ctx, "local", webToDB.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteTopologyDependency(ctx, "local", webToDB.ID); !errors.Is(err, topology.ErrDependencyNotFound) {
		t.Fatalf("missing dependency delete error = %v", err)
	}
}

func TestTopologyDependenciesCascadeWhenServiceIsDeleted(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "topology-cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, id := range []string{"svc-api", "svc-db"} {
		if _, err := database.CreateService(ctx, model.Service{ID: id, Name: id, DisplayURL: "http://" + id + ".local"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.CreateTopologyDependency(ctx, topology.DependencyInput{
		NodeID: "local", DependentServiceID: "svc-api", DependencyServiceID: "svc-db",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteService(ctx, "svc-db"); err != nil {
		t.Fatal(err)
	}
	edges, err := database.ListTopologyDependencies(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatalf("cascaded edges = %#v", edges)
	}
}
