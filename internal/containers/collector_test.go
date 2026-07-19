package containers

import (
	"context"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/podman"
)

type fakeClient struct {
	containers []podman.Container
	stats      []podman.ContainerStats
	details    map[string]podman.ContainerDetails
}

func (f fakeClient) ListContainers(context.Context, bool) ([]podman.Container, error) {
	return f.containers, nil
}
func (f fakeClient) Stats(context.Context, bool) ([]podman.ContainerStats, error) {
	return f.stats, nil
}
func (f fakeClient) InspectContainer(_ context.Context, id string) (podman.ContainerDetails, error) {
	return f.details[id], nil
}

func TestCollectorNormalizesCPUAndFlagsRestartLoops(t *testing.T) {
	client := fakeClient{
		containers: []podman.Container{{
			ID: "one", Name: "immich", Image: "immich:latest", State: "running", Protected: true,
			Ports: []podman.Port{{ContainerPort: 2283, HostPort: 2283, HostIP: "0.0.0.0", Protocol: "tcp"}},
		}},
		stats: []podman.ContainerStats{{ID: "one", CPUPercent: 200, MemoryUsage: 10, MemoryLimit: 100}},
		details: map[string]podman.ContainerDetails{"one": {
			Container: podman.Container{State: "running"}, Running: true, RestartCount: 5,
			StartedAt: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC),
		}},
	}
	collector := New(client)
	collector.now = func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) }
	items, alerts, err := collector.Collect(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].CPUNormalizedPercent != 50 || items[0].State != "crashed" || items[0].Actions.Exec || items[0].Actions.Logs {
		t.Fatalf("unexpected container model: %+v", items)
	}
	if items[0].UptimeSeconds != 2*60*60 {
		t.Fatalf("wall-clock uptime=%d", items[0].UptimeSeconds)
	}
	if len(alerts) != 1 || alerts[0].Level != "error" {
		t.Fatalf("restart loop should create an error alert: %+v", alerts)
	}
	if got := items[0].Ports[0]; got != "2283/tcp → 0.0.0.0:2283" {
		t.Fatalf("port=%q", got)
	}
}
