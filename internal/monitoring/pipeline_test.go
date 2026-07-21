package monitoring

import (
	"context"
	"testing"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
	"github.com/bnhminh1010/homelab-dashboard/internal/healthchecks"
	"github.com/bnhminh1010/homelab-dashboard/internal/history"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

func TestPipelineAppliesTierCadenceAndServiceTransitions(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	w := &memoryHistoryWriter{}
	e := &memoryAlertEngine{}
	pipeline, err := New(Options{History: w, Alerts: e})
	if err != nil {
		t.Fatal(err)
	}
	checked := now
	snapshot := testSnapshot(now, checked, model.ServiceStatusDown)
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CollectedAt = now.Add(2 * time.Second)
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	if len(w.hosts) != 1 || len(w.containers) != 1 || len(w.transitions) != 1 {
		t.Fatalf("history counts = host %d container %d transition %d", len(w.hosts), len(w.containers), len(w.transitions))
	}
	if value := sampleValue(e.samples[1], alerts.MetricServiceConsecutiveFailures); value != 2 {
		t.Fatalf("duplicate probe snapshot incremented failures to %.0f", value)
	}
	snapshot.CollectedAt = now.Add(31 * time.Second)
	nextCheck := now.Add(15 * time.Second)
	snapshot.Data.Services[0].LastCheckedAt = &nextCheck
	snapshot.Data.Services[0].Status = model.ServiceStatusUp
	snapshot.Data.Services[0].ConsecutiveFailures = 0
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	if len(w.hosts) != 2 || len(w.containers) != 2 || len(w.transitions) != 2 {
		t.Fatalf("history counts after cadence = host %d container %d transition %d", len(w.hosts), len(w.containers), len(w.transitions))
	}
	if value := sampleValue(e.samples[2], alerts.MetricServiceConsecutiveFailures); value != 0 {
		t.Fatalf("healthy check failure count = %.0f", value)
	}
}

func TestPipelineSkipsStaleComponents(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	w := &memoryHistoryWriter{}
	e := &memoryAlertEngine{}
	pipeline, err := New(Options{History: w, Alerts: e})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(now, now, model.ServiceStatusDown)
	snapshot.StaleSources = []string{"host", "containers", "services"}
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	if len(w.hosts) != 0 || len(w.containers) != 0 || len(w.transitions) != 0 {
		t.Fatalf("stale data entered history: %+v", w)
	}
	if len(e.samples) != 1 || len(e.samples[0]) != 1 ||
		sampleValue(e.samples[0], alerts.MetricNodeOnline) != 1 {
		t.Fatalf("stale data entered alert evaluation: %#v", e.samples)
	}
}

func TestPipelineDoesNotReconcileTruncatedInventories(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	e := &memoryAlertEngine{}
	pipeline, err := New(Options{Alerts: e})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(now, now, model.ServiceStatusUp)
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CollectedAt = now.Add(time.Second)
	snapshot.Truncated = true
	snapshot.TruncatedSources = []string{"containers", "services"}
	snapshot.Data.Containers = nil
	snapshot.Data.Services = nil
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{alerts.MetricContainerHealthy, alerts.MetricContainerRestarts, alerts.MetricServiceConsecutiveFailures} {
		if got := sampleValue(e.samples[1], metric); got != -1 {
			t.Fatalf("truncated inventory emitted a synthetic clean %s sample %.1f", metric, got)
		}
	}
}

func TestPipelineRecordsServiceObservationsAndMissingAsUnknown(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	w := &memoryHistoryWriter{}
	pipeline, err := New(Options{History: w})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(now, now, model.ServiceStatusUp)
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CollectedAt = now.Add(history.ServiceObservationInterval)
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CollectedAt = snapshot.CollectedAt.Add(time.Second)
	snapshot.Data.Services = nil
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CollectedAt = snapshot.CollectedAt.Add(history.ServiceObservationInterval)
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	if len(w.transitions) != 3 || w.transitions[0].State != history.ServiceUp ||
		w.transitions[1].State != history.ServiceUp || w.transitions[2].State != history.ServiceUnknown {
		t.Fatalf("service observations = %#v", w.transitions)
	}
}

func TestPipelineEmitsTwoCleanSamplesForRemovedResources(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	e := &memoryAlertEngine{}
	pipeline, err := New(Options{Alerts: e})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(now, now, model.ServiceStatusDown)
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CollectedAt = now.Add(2 * time.Second)
	snapshot.Data.System.CPU.TemperatureCelsius = nil
	snapshot.Data.Disks = nil
	snapshot.Data.Services = nil
	snapshot.Data.Containers = nil
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CollectedAt = now.Add(4 * time.Second)
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.CollectedAt = now.Add(6 * time.Second)
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{1, 2} {
		checks := map[string]float64{
			alerts.MetricTemperatureCelsius:         0,
			alerts.MetricDiskUsedPercent:            0,
			alerts.MetricServiceConsecutiveFailures: 0,
			alerts.MetricContainerHealthy:           1,
			alerts.MetricContainerRestarts:          0,
		}
		for metric, want := range checks {
			if got := sampleValue(e.samples[index], metric); got != want {
				t.Fatalf("clean sample %d metric %s = %.1f, want %.1f", index, metric, got, want)
			}
		}
	}
	for _, metric := range []string{
		alerts.MetricTemperatureCelsius,
		alerts.MetricDiskUsedPercent,
		alerts.MetricServiceConsecutiveFailures,
		alerts.MetricContainerHealthy,
		alerts.MetricContainerRestarts,
	} {
		if got := sampleValue(e.samples[3], metric); got != -1 {
			t.Fatalf("resource cleanup metric %s continued after two samples: %.1f", metric, got)
		}
	}
}

func TestPipelineReconcilesPersistedMissingResourceAfterRestart(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	e := &memoryAlertEngine{}
	pipeline, err := New(Options{
		Alerts: e,
		ActiveAlertStates: []alerts.AlertState{{
			AlertKey: alerts.AlertKey{NodeID: "node-a", ResourceType: "container", ResourceID: "removed"},
			Status:   alerts.StatusFiring,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(now, now, model.ServiceStatusUp)
	snapshot.Data.Containers = nil
	for index := 0; index < 3; index++ {
		snapshot.CollectedAt = now.Add(time.Duration(index) * time.Second)
		if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	for _, index := range []int{0, 1} {
		if got := sampleValue(e.samples[index], alerts.MetricContainerHealthy); got != 1 {
			t.Fatalf("restart reconciliation sample %d = %.1f", index, got)
		}
	}
	if got := sampleValue(e.samples[2], alerts.MetricContainerHealthy); got != -1 {
		t.Fatalf("restart reconciliation continued after two samples: %.1f", got)
	}
}

func TestPipelineDoesNotResolveServiceBeforeFirstFreshProbe(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	e := &memoryAlertEngine{}
	pipeline, err := New(Options{
		Alerts: e,
		ActiveAlertStates: []alerts.AlertState{{
			AlertKey: alerts.AlertKey{NodeID: "node-a", ResourceType: "service", ResourceID: "svc"},
			Status:   alerts.StatusFiring,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(now, now, model.ServiceStatusUnknown)
	snapshot.Data.Services[0].LastCheckedAt = nil
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	if got := sampleValue(e.samples[0], alerts.MetricServiceConsecutiveFailures); got != -1 {
		t.Fatalf("unprobed service emitted synthetic value %.1f", got)
	}
}

func TestPipelineTreatsIntentionalContainerStopAsHealthy(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	e := &memoryAlertEngine{}
	pipeline, err := New(Options{Alerts: e})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(now, now, model.ServiceStatusUp)
	snapshot.Data.Containers[0].State = "stopped"
	snapshot.Data.Containers[0].RestartCount = 0
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	if got := sampleValue(e.samples[0], alerts.MetricContainerHealthy); got != 1 {
		t.Fatalf("stopped container healthy metric = %.1f, want 1", got)
	}
	snapshot.CollectedAt = now.Add(time.Second)
	snapshot.Data.Containers[0].State = "crashed"
	if err := pipeline.Handle(context.Background(), "node-a", snapshot); err != nil {
		t.Fatal(err)
	}
	if got := sampleValue(e.samples[1], alerts.MetricContainerHealthy); got != 0 {
		t.Fatalf("crashed container healthy metric = %.1f, want 0", got)
	}
}

func TestPipelineEvaluatesBackupFreshnessPerNodeAndJob(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	engine := &memoryAlertEngine{}
	pipeline, err := New(Options{Alerts: engine})
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.HandleBackupFreshness(context.Background(), []healthchecks.BackupObservation{
		{
			NodeID: "local",
			Status: model.BackupStatus{Job: "nightly", Status: "success", CompletedAt: now.Add(-48 * time.Hour), ExpectedWithinSeconds: 24 * 60 * 60},
		},
		{
			NodeID: "remote-a",
			Status: model.BackupStatus{Job: "nightly", Status: "success", CompletedAt: now.Add(-time.Hour), ExpectedWithinSeconds: 24 * 60 * 60},
		},
	}, now); err != nil {
		t.Fatal(err)
	}
	if len(engine.samples) != 1 || len(engine.samples[0]) != 2 {
		t.Fatalf("backup samples = %#v", engine.samples)
	}
	values := make(map[string]float64, len(engine.samples[0]))
	for _, sample := range engine.samples[0] {
		if sample.ResourceType != "backup" || sample.ResourceID != "nightly" || sample.Metric != alerts.MetricBackupHealthy {
			t.Fatalf("unexpected backup sample = %#v", sample)
		}
		values[sample.NodeID] = sample.Value
	}
	if values["local"] != 0 || values["remote-a"] != 1 {
		t.Fatalf("backup freshness values = %#v", values)
	}
}

func TestPipelineReportsNodeOfflineAndRecoversOnSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	e := &memoryAlertEngine{}
	pipeline, err := New(Options{Alerts: e})
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.HandleNodeOffline(context.Background(), "node-a", now); err != nil {
		t.Fatal(err)
	}
	if got := sampleValue(e.samples[0], alerts.MetricNodeOnline); got != 0 {
		t.Fatalf("offline node sample = %.1f, want 0", got)
	}
	if err := pipeline.Handle(context.Background(), "node-a", testSnapshot(now.Add(time.Second), now, model.ServiceStatusUp)); err != nil {
		t.Fatal(err)
	}
	if got := sampleValue(e.samples[1], alerts.MetricNodeOnline); got != 1 {
		t.Fatalf("reconnected node sample = %.1f, want 1", got)
	}
}

func testSnapshot(at, checked time.Time, status model.ServiceStatus) model.SnapshotEnvelope {
	temperature := 82.0
	failures := 0
	if status == model.ServiceStatusDown {
		failures = 2
	}
	return model.SnapshotEnvelope{
		Version: 1, Type: "metrics.snapshot", CollectedAt: at,
		Data: model.SnapshotData{
			System:     model.SystemStats{CPU: model.CPUStats{UsagePercent: 95, TemperatureCelsius: &temperature}, Memory: model.MemoryStats{UsedBytes: 90, TotalBytes: 100}},
			Disks:      []model.DiskStats{{MountPoint: "/", UsedBytes: 95, TotalBytes: 100, UsagePercent: 95}},
			Services:   []model.Service{{ID: "svc", Status: status, ConsecutiveFailures: failures, LastCheckedAt: &checked}},
			Containers: []model.Container{{ID: "cont", Name: "container", State: "running", RestartCount: 4}},
		},
	}
}

func sampleValue(samples []alerts.Sample, metric string) float64 {
	for _, sample := range samples {
		if sample.Metric == metric {
			return sample.Value
		}
	}
	return -1
}

type memoryHistoryWriter struct {
	hosts       []history.HostSample
	containers  []history.ContainerSample
	transitions []history.ServiceTransition
}

func (writer *memoryHistoryWriter) RecordHost(sample history.HostSample) bool {
	writer.hosts = append(writer.hosts, sample)
	return true
}

func (writer *memoryHistoryWriter) RecordContainer(sample history.ContainerSample) bool {
	writer.containers = append(writer.containers, sample)
	return true
}

func (writer *memoryHistoryWriter) RecordServiceTransition(sample history.ServiceTransition) bool {
	writer.transitions = append(writer.transitions, sample)
	return true
}

type memoryAlertEngine struct{ samples [][]alerts.Sample }

func (engine *memoryAlertEngine) Evaluate(_ context.Context, samples []alerts.Sample) (alerts.EvaluationResult, error) {
	engine.samples = append(engine.samples, append([]alerts.Sample(nil), samples...))
	return alerts.EvaluationResult{}, nil
}
