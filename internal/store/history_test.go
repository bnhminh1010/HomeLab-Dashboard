package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/history"
)

func TestHistoryMigrationUpgradesInitialDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dashboard.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version TEXT PRIMARY KEY, applied_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	initial, err := migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(initial)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO schema_migrations(version, applied_at) VALUES ('001_initial.sql', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO services(id, name, icon, display_url, probe_url, created_at, updated_at)
		VALUES ('svc-existing', 'Existing', '', 'https://existing.test', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.GetService(ctx, "svc-existing"); err != nil {
		t.Fatalf("initial service was not preserved: %v", err)
	}
	var localNodes, historyMigration int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM history_nodes WHERE id = 'local'`).Scan(&localNodes); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations WHERE version = '002_history.sql'`).Scan(&historyMigration); err != nil {
		t.Fatal(err)
	}
	if localNodes != 1 || historyMigration != 1 {
		t.Fatalf("history migration missing: local=%d migration=%d", localNodes, historyMigration)
	}
}

func TestHistoryWriteRollupAndQueries(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hour := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO services(id, name, icon, display_url, probe_url, created_at, updated_at)
		VALUES ('svc-a', 'Service A', '', 'https://service-a.example', '', ?, ?)`,
		hour.Format(time.RFC3339Nano), hour.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	temperature := 50.0
	batch := history.Batch{
		Hosts: []history.HostSample{
			hostSample(hour.Add(10*time.Second), 10, &temperature),
			hostSample(hour.Add(50*time.Second), 30, nil),
		},
		Containers: []history.ContainerSample{
			containerSample(hour.Add(10*time.Second), "instance-a", 10, 100),
			containerSample(hour.Add(50*time.Second), "instance-a", 30, 300),
		},
		ServiceTransitions: []history.ServiceTransition{
			{ServiceID: "svc-a", State: history.ServiceUp, ObservedAt: hour.Add(10 * time.Minute)},
			{ServiceID: "svc-a", State: history.ServiceDown, ObservedAt: hour.Add(40 * time.Minute)},
		},
	}
	if err := store.WriteHistoryBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	rollupAt := hour.Add(80 * time.Minute)
	if err := store.RollupHistory(ctx, rollupAt); err != nil {
		t.Fatal(err)
	}

	raw, resolution, err := store.QueryHostHistory(ctx, history.Query{
		From: hour, To: hour.Add(time.Hour), Resolution: history.ResolutionRaw,
	})
	if err != nil || resolution != history.ResolutionRaw || len(raw) != 2 {
		t.Fatalf("raw host history: len=%d resolution=%s err=%v", len(raw), resolution, err)
	}
	minute, resolution, err := store.QueryHostHistory(ctx, history.Query{
		From: hour, To: hour.Add(time.Hour), Resolution: history.Resolution1m,
	})
	if err != nil || resolution != history.Resolution1m || len(minute) != 1 {
		t.Fatalf("minute host history: len=%d resolution=%s err=%v", len(minute), resolution, err)
	}
	if minute[0].SampleCount != 2 || math.Abs(minute[0].CPUPercent-20) > 0.001 {
		t.Fatalf("unexpected weighted host rollup: %+v", minute[0])
	}
	if minute[0].TemperatureCelsius == nil || *minute[0].TemperatureCelsius != temperature {
		t.Fatalf("nullable temperature was not preserved: %+v", minute[0])
	}

	containers, resolution, err := store.QueryContainerHistory(ctx, history.Query{
		InstanceID: "instance-a", From: hour, To: hour.Add(time.Hour),
		Resolution: history.Resolution5m,
	})
	if err != nil || resolution != history.Resolution5m || len(containers) != 1 {
		t.Fatalf("container history: len=%d resolution=%s err=%v", len(containers), resolution, err)
	}
	if containers[0].SampleCount != 2 || containers[0].MemoryUsageBytes != 200 {
		t.Fatalf("unexpected container rollup: %+v", containers[0])
	}
	instance, err := store.GetContainerInstance(ctx, "", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	if instance.Name != "container-a" || !instance.FirstSeenAt.Equal(hour.Add(10*time.Second)) ||
		!instance.LastSeenAt.Equal(hour.Add(50*time.Second)) {
		t.Fatalf("unexpected container identity: %+v", instance)
	}
	instances, err := store.ListContainerInstances(ctx, "")
	if err != nil || len(instances) != 1 || instances[0].InstanceID != "instance-a" {
		t.Fatalf("container history catalog: %+v err=%v", instances, err)
	}
	serviceSeries, err := store.ListServiceSeries(ctx, "")
	if err != nil || len(serviceSeries) != 1 || serviceSeries[0].ServiceID != "svc-a" || serviceSeries[0].Name != "Service A" {
		t.Fatalf("service history catalog: %+v err=%v", serviceSeries, err)
	}

	uptime, err := store.QueryServiceUptime(ctx, "", "svc-a", hour, hour.Add(2*time.Hour))
	if err != nil || len(uptime) != 2 {
		t.Fatalf("service uptime: len=%d err=%v", len(uptime), err)
	}
	if uptime[0].UnknownSeconds != 3360 || uptime[0].UpSeconds != 120 ||
		uptime[0].DownSeconds != 120 || uptime[0].TransitionCount != 4 {
		t.Fatalf("unexpected complete uptime hour: %+v", uptime[0])
	}
	if uptime[1].UnknownSeconds != 1200 || uptime[1].DownSeconds != 0 || uptime[1].TransitionCount != 0 {
		t.Fatalf("unexpected partial uptime hour: %+v", uptime[1])
	}
}

func TestHistoryResourceCatalogIsBoundedAndNewestFirst(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	base := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return base.Add(2 * time.Hour) }
	batch := history.Batch{
		Containers:         make([]history.ContainerSample, 0, history.MaxResourceCatalogEntries+2),
		ServiceTransitions: make([]history.ServiceTransition, 0, history.MaxResourceCatalogEntries+1),
	}
	batch.Containers = append(batch.Containers,
		containerSample(base.Add(-history.ContainerHourRetention-time.Hour), "expired-resource", 0, 0))
	for index := 0; index <= history.MaxResourceCatalogEntries; index++ {
		at := base.Add(time.Duration(index) * time.Second)
		id := fmt.Sprintf("resource-%03d", index)
		batch.Containers = append(batch.Containers, containerSample(at, id, float64(index), uint64(index)))
		batch.ServiceTransitions = append(batch.ServiceTransitions, history.ServiceTransition{
			ServiceID: id, State: history.ServiceUp, ObservedAt: at,
		})
	}
	if err := database.WriteHistoryBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	containers, err := database.ListContainerInstances(ctx, "local")
	if err != nil || len(containers) != history.MaxResourceCatalogEntries {
		t.Fatalf("container catalog len=%d err=%v", len(containers), err)
	}
	if containers[0].InstanceID != "resource-500" || containers[len(containers)-1].InstanceID != "resource-001" {
		t.Fatalf("container catalog order/boundary: first=%q last=%q", containers[0].InstanceID, containers[len(containers)-1].InstanceID)
	}
	services, err := database.ListServiceSeries(ctx, "local")
	if err != nil || len(services) != history.MaxResourceCatalogEntries {
		t.Fatalf("service catalog len=%d err=%v", len(services), err)
	}
	if services[0].ServiceID != "resource-500" || services[len(services)-1].ServiceID != "resource-001" {
		t.Fatalf("service catalog order/boundary: first=%q last=%q", services[0].ServiceID, services[len(services)-1].ServiceID)
	}
}

func TestServiceUptimeKeepsFrequentObservationsContinuous(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "service-observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	hour := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	batch := history.Batch{}
	for at := hour; at.Before(hour.Add(10 * time.Minute)); at = at.Add(time.Minute) {
		batch.ServiceTransitions = append(batch.ServiceTransitions, history.ServiceTransition{
			ServiceID: "svc", State: history.ServiceUp, ObservedAt: at,
		})
	}
	if err := database.WriteHistoryBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := database.RollupHistory(ctx, hour.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	points, err := database.QueryServiceUptime(ctx, "local", "svc", hour, hour.Add(20*time.Minute))
	if err != nil || len(points) != 1 {
		t.Fatalf("uptime points = %#v, error = %v", points, err)
	}
	if points[0].UpSeconds != 11*60 || points[0].UnknownSeconds != 9*60 {
		t.Fatalf("observation expiry was not applied: %+v", points[0])
	}
}

func TestServiceTransitionRollupLoadsOnlyAnchorAndActiveWindow(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	batch := history.Batch{ServiceTransitions: make([]history.ServiceTransition, 0, 102)}
	for index := 0; index < 100; index++ {
		batch.ServiceTransitions = append(batch.ServiceTransitions, history.ServiceTransition{
			NodeID: "local", ServiceID: "svc", State: history.ServiceUp,
			ObservedAt: now.Add(-10*time.Hour + time.Duration(index)*time.Minute),
		})
	}
	for _, offset := range []time.Duration{-30 * time.Minute, -time.Minute} {
		batch.ServiceTransitions = append(batch.ServiceTransitions, history.ServiceTransition{
			NodeID: "local", ServiceID: "svc", State: history.ServiceDown, ObservedAt: now.Add(offset),
		})
	}
	if err := database.WriteHistoryBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	tx, err := database.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	transitions, err := loadServiceTransitions(ctx, tx, serviceSeries{nodeID: "local", serviceID: "svc"}, now.Add(-time.Hour).Unix(), now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 3 {
		t.Fatalf("loaded %d transitions, want one anchor plus two active-window observations", len(transitions))
	}
}

func TestHistoryRetentionTiersAndTransitionAnchor(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-100 * 24 * time.Hour)
	recent := now.Add(-10 * 24 * time.Hour)
	batch := history.Batch{
		Hosts: []history.HostSample{hostSample(old, 10, nil), hostSample(recent, 20, nil)},
		Containers: []history.ContainerSample{
			containerSample(old, "old-instance", 10, 100),
			containerSample(recent, "recent-instance", 20, 200),
		},
		ServiceTransitions: []history.ServiceTransition{
			{ServiceID: "svc-a", State: history.ServiceUp, ObservedAt: now.Add(-110 * 24 * time.Hour)},
			{ServiceID: "svc-a", State: history.ServiceDown, ObservedAt: old},
			{ServiceID: "svc-a", State: history.ServiceUp, ObservedAt: recent},
		},
	}
	if err := store.WriteHistoryBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupHistory(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainHistory(ctx, now); err != nil {
		t.Fatal(err)
	}

	assertTableCount(t, store.db, "history_host_raw", 0)
	assertTableCount(t, store.db, "history_host_rollup_1m", 1)
	assertTableCount(t, store.db, "history_host_rollup_15m", 1)
	assertTableCount(t, store.db, "history_container_raw", 0)
	assertTableCount(t, store.db, "history_container_rollup_5m", 1)
	assertTableCount(t, store.db, "history_container_rollup_1h", 1)
	assertTableCount(t, store.db, "history_service_transitions", 2)
	assertTableCount(t, store.db, "history_container_instances", 1)
	if _, err := store.GetContainerInstance(ctx, "", "old-instance"); err != ErrNotFound {
		t.Fatalf("old container identity should be removed, got %v", err)
	}
	var anchorState string
	if err := store.db.QueryRowContext(ctx, `
		SELECT state FROM history_service_transitions
		WHERE observed_at < ? ORDER BY observed_at DESC LIMIT 1`,
		now.Add(-history.ServiceRetention).Unix()).Scan(&anchorState); err != nil {
		t.Fatal(err)
	}
	if anchorState != string(history.ServiceDown) {
		t.Fatalf("wrong service transition anchor: %s", anchorState)
	}
}

func TestIncrementalRollupRebuildsTheWholeOverlappingBucket(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hour := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if err := store.WriteHistoryBatch(ctx, history.Batch{
		Hosts: []history.HostSample{hostSample(hour.Add(10*time.Second), 10, nil)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupHistory(ctx, hour.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteHistoryBatch(ctx, history.Batch{
		Hosts: []history.HostSample{hostSample(hour.Add(50*time.Second), 30, nil)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupHistory(ctx, hour.Add(70*time.Second)); err != nil {
		t.Fatal(err)
	}
	minute, _, err := store.QueryHostHistory(ctx, history.Query{
		From: hour, To: hour.Add(time.Minute), Resolution: history.Resolution1m,
	})
	if err != nil || len(minute) != 1 {
		t.Fatalf("incremental minute history: len=%d err=%v", len(minute), err)
	}
	if minute[0].SampleCount != 2 || minute[0].CPUPercent != 20 {
		t.Fatalf("overlapping rollup lost early samples: %+v", minute[0])
	}
	quarter, _, err := store.QueryHostHistory(ctx, history.Query{
		From: hour, To: hour.Add(15 * time.Minute), Resolution: history.Resolution15m,
	})
	if err != nil || len(quarter) != 1 || quarter[0].SampleCount != 2 || quarter[0].CPUPercent != 20 {
		t.Fatalf("overlapping higher rollup lost early samples: %+v err=%v", quarter, err)
	}
}

func TestHostMinuteRollupIncludesLateRemoteSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hour := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	bucket := hour.Add(8 * time.Minute)
	if err := store.WriteHistoryBatch(ctx, history.Batch{
		Hosts: []history.HostSample{hostSample(bucket.Add(10*time.Second), 10, nil)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupHistory(ctx, hour.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	// This observation arrives after the rollup cursor advanced, but its
	// collected timestamp is 90 seconds behind the next maintenance run.
	if err := store.WriteHistoryBatch(ctx, history.Batch{
		Hosts: []history.HostSample{hostSample(bucket.Add(40*time.Second), 30, nil)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupHistory(ctx, hour.Add(10*time.Minute+10*time.Second)); err != nil {
		t.Fatal(err)
	}

	points, _, err := store.QueryHostHistory(ctx, history.Query{
		From: bucket, To: bucket.Add(time.Minute), Resolution: history.Resolution1m,
	})
	if err != nil || len(points) != 1 {
		t.Fatalf("late minute history: len=%d err=%v", len(points), err)
	}
	if points[0].SampleCount != 2 || points[0].CPUPercent != 20 {
		t.Fatalf("late snapshot was not folded into rollup: %+v", points[0])
	}
}

func TestTemperatureRollupWeightsOnlyAvailableSamples(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hour := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	fifty, hundred := 50.0, 100.0
	if err := store.WriteHistoryBatch(ctx, history.Batch{Hosts: []history.HostSample{
		hostSample(hour.Add(10*time.Second), 10, &fifty),
		hostSample(hour.Add(20*time.Second), 10, nil),
		hostSample(hour.Add(70*time.Second), 10, &hundred),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupHistory(ctx, hour.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	points, _, err := store.QueryHostHistory(ctx, history.Query{
		From: hour, To: hour.Add(15 * time.Minute), Resolution: history.Resolution15m,
	})
	if err != nil || len(points) != 1 || points[0].TemperatureCelsius == nil {
		t.Fatalf("temperature rollup: %+v err=%v", points, err)
	}
	if *points[0].TemperatureCelsius != 75 {
		t.Fatalf("missing temperatures biased rollup: got=%v want=75", *points[0].TemperatureCelsius)
	}
}

func TestHistorySizeAndValidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if size, err := store.HistorySizeBytes(ctx); err != nil || size <= 0 {
		t.Fatalf("history size: size=%d err=%v", size, err)
	}
	if err := store.WriteHistoryBatch(ctx, history.Batch{Hosts: []history.HostSample{{}}}); err == nil {
		t.Fatal("expected missing timestamp validation")
	}
	if err := store.WriteHistoryBatch(ctx, history.Batch{Containers: []history.ContainerSample{{
		InstanceID: "id", Name: "name", CollectedAt: time.Now(), MemoryUsageBytes: math.MaxUint64,
	}}}); err == nil {
		t.Fatal("expected sqlite integer overflow validation")
	}
}

func hostSample(at time.Time, cpu float64, temperature *float64) history.HostSample {
	return history.HostSample{
		CollectedAt: at, CPUPercent: cpu, MemoryUsedBytes: 100, MemoryTotalBytes: 1000,
		DiskUsedBytes: 200, DiskTotalBytes: 2000, NetworkRXBytesPerSecond: 10,
		NetworkTXBytesPerSecond: 20, LoadOne: 0.5, TemperatureCelsius: temperature,
	}
}

func containerSample(at time.Time, instanceID string, cpu float64, memory uint64) history.ContainerSample {
	return history.ContainerSample{
		InstanceID: instanceID, Name: "container-a", Image: "example:latest",
		CollectedAt: at, CPUPercent: cpu, MemoryUsageBytes: memory,
		MemoryLimitBytes: 1000, RestartCount: 2,
	}
}

func assertTableCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count: got=%d want=%d", table, got, want)
	}
}
