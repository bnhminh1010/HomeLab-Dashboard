package history

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAutomaticResolutions(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	hostCases := []struct {
		span time.Duration
		want Resolution
	}{
		{48 * time.Hour, ResolutionRaw},
		{7 * 24 * time.Hour, Resolution1m},
		{90 * 24 * time.Hour, Resolution15m},
	}
	for _, test := range hostCases {
		_, got, err := ResolveHost(Query{From: now.Add(-test.span), To: now, Resolution: ResolutionAuto})
		if err != nil || got != test.want {
			t.Fatalf("resolve host span %s: got=%s want=%s err=%v", test.span, got, test.want, err)
		}
	}
	containerCases := []struct {
		span time.Duration
		want Resolution
	}{
		{48 * time.Hour, ResolutionRaw},
		{7 * 24 * time.Hour, Resolution5m},
		{90 * 24 * time.Hour, Resolution1h},
	}
	for _, test := range containerCases {
		_, got, err := ResolveContainer(Query{
			InstanceID: "container-a", From: now.Add(-test.span), To: now,
			Resolution: ResolutionAuto,
		})
		if err != nil || got != test.want {
			t.Fatalf("resolve container span %s: got=%s want=%s err=%v", test.span, got, test.want, err)
		}
	}
}

func TestResolutionValidation(t *testing.T) {
	now := time.Now()
	if _, _, err := ResolveHost(Query{From: now, To: now}); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("expected invalid range, got %v", err)
	}
	if _, _, err := ResolveHost(Query{
		From: now.Add(-time.Hour), To: now, Resolution: Resolution5m,
	}); !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("expected invalid resolution, got %v", err)
	}
	if _, _, err := ResolveContainer(Query{
		From: now.Add(-time.Hour), To: now, Resolution: ResolutionRaw,
	}); err == nil {
		t.Fatal("expected missing container instance error")
	}
}

func TestWriterBackpressureIsNonBlocking(t *testing.T) {
	repository := &fakeRepository{}
	writer, err := NewWriter(repository, WriterOptions{QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	sample := HostSample{CollectedAt: time.Now()}
	if !writer.RecordHost(sample) {
		t.Fatal("first record should fit in queue")
	}
	if writer.RecordHost(sample) {
		t.Fatal("second record should be dropped instead of blocking")
	}
	stats := writer.Stats()
	if stats.Submitted != 2 || stats.DroppedBackpressure != 1 {
		t.Fatalf("unexpected writer stats: %+v", stats)
	}
}

func TestWriterDropsRawAtQuotaButKeepsTransitions(t *testing.T) {
	repository := &fakeRepository{sizeBytes: 100, writes: make(chan Batch, 1)}
	writer, err := NewWriter(repository, WriterOptions{
		QueueCapacity: 8, BatchSize: 3, FlushInterval: time.Second, SoftQuotaBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- writer.Run(ctx) }()

	now := time.Now()
	writer.RecordHost(HostSample{CollectedAt: now})
	writer.RecordContainer(ContainerSample{InstanceID: "c1", Name: "one", CollectedAt: now})
	writer.RecordServiceTransition(ServiceTransition{
		ServiceID: "svc1", State: ServiceUp, ObservedAt: now,
	})
	select {
	case batch := <-repository.writes:
		if len(batch.Hosts) != 0 || len(batch.Containers) != 0 || len(batch.ServiceTransitions) != 1 {
			t.Fatalf("quota-filtered batch: %+v", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not flush quota-filtered batch")
	}
	// fakeRepository signals after WriteHistoryBatch receives the batch, which
	// happens just before Writer.flush records its successful-write counter.
	// Wait for that final bookkeeping step instead of depending on scheduling.
	deadline := time.Now().Add(time.Second)
	for {
		stats := writer.Stats()
		if stats.DroppedQuota == 2 && stats.Written == 1 && stats.Quota.Warning && stats.Quota.Full {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unexpected quota stats: %+v", stats)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("writer exit: %v", err)
	}
}

func TestWriterFlushesOnBoundedInterval(t *testing.T) {
	repository := &fakeRepository{writes: make(chan Batch, 1)}
	writer, err := NewWriter(repository, WriterOptions{
		QueueCapacity: 2, BatchSize: 100, FlushInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go writer.Run(ctx)
	writer.RecordHost(HostSample{CollectedAt: time.Now()})
	select {
	case batch := <-repository.writes:
		if len(batch.Hosts) != 1 {
			t.Fatalf("unexpected interval batch: %+v", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("writer exceeded bounded flush interval")
	}
	if _, err := NewWriter(repository, WriterOptions{FlushInterval: 11 * time.Second}); err == nil {
		t.Fatal("expected intervals over ten seconds to be rejected")
	}
}

func TestWriterDrainsAcceptedRecordsOnShutdown(t *testing.T) {
	repository := &fakeRepository{writes: make(chan Batch, 2)}
	writer, err := NewWriter(repository, WriterOptions{
		QueueCapacity: 4, BatchSize: 2, FlushInterval: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	writer.RecordHost(HostSample{CollectedAt: time.Now()})
	writer.RecordHost(HostSample{CollectedAt: time.Now().Add(time.Second)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writer.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("writer exit: %v", err)
	}
	select {
	case batch := <-repository.writes:
		if len(batch.Hosts) != 2 {
			t.Fatalf("shutdown batch: %+v", batch)
		}
	default:
		t.Fatal("accepted records were not drained on shutdown")
	}
}

func TestMaintenanceUsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 34, 56, 0, time.FixedZone("test", 7*60*60))
	repository := &fakeRepository{}
	maintenance, err := NewMaintenance(repository, MaintenanceOptions{Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.jobs) != 2 || repository.jobs[0].name != "rollup" || repository.jobs[1].name != "retain" {
		t.Fatalf("unexpected maintenance order: %+v", repository.jobs)
	}
	if !repository.jobs[0].at.Equal(now.UTC()) || !repository.jobs[1].at.Equal(now.UTC()) {
		t.Fatalf("maintenance did not use fake clock: %+v", repository.jobs)
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fakeJob struct {
	name string
	at   time.Time
}

type fakeRepository struct {
	mu        sync.Mutex
	sizeBytes int64
	writes    chan Batch
	jobs      []fakeJob
}

func (r *fakeRepository) WriteHistoryBatch(_ context.Context, batch Batch) error {
	if r.writes != nil {
		r.writes <- batch
	}
	return nil
}

func (r *fakeRepository) RollupHistory(_ context.Context, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, fakeJob{name: "rollup", at: at})
	return nil
}

func (r *fakeRepository) RetainHistory(_ context.Context, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, fakeJob{name: "retain", at: at})
	return nil
}

func (r *fakeRepository) QueryHostHistory(context.Context, Query) ([]HostPoint, Resolution, error) {
	return nil, ResolutionRaw, nil
}

func (r *fakeRepository) QueryContainerHistory(context.Context, Query) ([]ContainerPoint, Resolution, error) {
	return nil, ResolutionRaw, nil
}

func (r *fakeRepository) QueryServiceUptime(context.Context, string, string, time.Time, time.Time) ([]ServiceUptimePoint, error) {
	return nil, nil
}

func (r *fakeRepository) GetContainerInstance(context.Context, string, string) (ContainerInstance, error) {
	return ContainerInstance{}, nil
}

func (r *fakeRepository) ListContainerInstances(context.Context, string) ([]ContainerInstance, error) {
	return nil, nil
}

func (r *fakeRepository) ListServiceSeries(context.Context, string) ([]ServiceSeries, error) {
	return nil, nil
}

func (r *fakeRepository) HistorySizeBytes(context.Context) (int64, error) {
	return r.sizeBytes, nil
}
