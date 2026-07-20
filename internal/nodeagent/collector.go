package nodeagent

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/containers"
	"github.com/binhminh/HomeLab-Minh/internal/metrics"
	"github.com/binhminh/HomeLab-Minh/internal/model"
)

type ContainerCollector interface {
	Collect(context.Context, int) ([]model.Container, []model.Alert, error)
}

type SnapshotCollector interface {
	Collect(context.Context) (model.SnapshotEnvelope, error)
}

type LocalCollector struct {
	host       metrics.HostCollector
	containers ContainerCollector
	cores      int
	now        func() time.Time

	mu       sync.Mutex
	sequence uint64
	previous model.SnapshotData
}

func NewLocalCollector(host metrics.HostCollector, containerCollector *containers.Collector) (*LocalCollector, error) {
	if host == nil || containerCollector == nil {
		return nil, fmt.Errorf("node agent: host and container collectors are required")
	}
	return &LocalCollector{host: host, containers: containerCollector, cores: runtime.NumCPU(), now: time.Now}, nil
}

// Collect keeps the last valid component when either /proc or Podman is
// temporarily unavailable. The alert in the same snapshot makes that staleness
// explicit to the dashboard.
func (collector *LocalCollector) Collect(ctx context.Context) (model.SnapshotEnvelope, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()

	data := cloneSnapshotData(collector.previous)
	data.Services = make([]model.Service, 0)
	data.Alerts = make([]model.Alert, 0)
	now := collector.now().UTC()
	var collectionErrors []error
	staleSources := make([]string, 0, 2)

	hostSnapshot, err := collector.host.Collect(ctx)
	if err != nil {
		collectionErrors = append(collectionErrors, fmt.Errorf("host metrics: %w", err))
		staleSources = append(staleSources, "host")
		data.Alerts = append(data.Alerts, collectionAlert("host", now))
	} else {
		data.System = hostSnapshot.System
		data.Disks = hostSnapshot.Disks
		data.Network = hostSnapshot.Network
	}
	containerItems, containerAlerts, err := collector.containers.Collect(ctx, collector.cores)
	if err != nil {
		collectionErrors = append(collectionErrors, fmt.Errorf("Podman metrics: %w", err))
		staleSources = append(staleSources, "containers")
		data.Alerts = append(data.Alerts, collectionAlert("podman", now))
	} else {
		data.Containers = containerItems
		data.Alerts = append(data.Alerts, containerAlerts...)
	}
	if data.Disks == nil {
		data.Disks = make([]model.DiskStats, 0)
	}
	if data.Containers == nil {
		data.Containers = make([]model.Container, 0)
	}
	collector.previous = cloneSnapshotData(data)
	collector.sequence++
	snapshot := model.SnapshotEnvelope{
		Version: 1, Type: "metrics.snapshot", Sequence: collector.sequence,
		CollectedAt: now, StaleSources: staleSources, Data: data,
	}
	if len(collectionErrors) > 0 {
		return snapshot, fmt.Errorf("node agent: %d collector(s) unavailable", len(collectionErrors))
	}
	return snapshot, nil
}

func collectionAlert(source string, now time.Time) model.Alert {
	return model.Alert{
		ID: "node-agent:" + source, Level: "error", Source: source,
		Message: "Unable to collect " + source + " data", OccurredAt: now,
	}
}

func cloneSnapshotData(data model.SnapshotData) model.SnapshotData {
	data.Disks = append([]model.DiskStats(nil), data.Disks...)
	data.Services = append([]model.Service(nil), data.Services...)
	data.Containers = append([]model.Container(nil), data.Containers...)
	data.Alerts = append([]model.Alert(nil), data.Alerts...)
	for index := range data.Containers {
		data.Containers[index].Ports = append([]string(nil), data.Containers[index].Ports...)
	}
	return data
}
