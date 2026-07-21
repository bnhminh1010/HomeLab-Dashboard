package metrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

type ServiceSource interface {
	ListServices(context.Context) ([]model.Service, error)
}

type ServiceSourceFunc func(context.Context) ([]model.Service, error)

func (f ServiceSourceFunc) ListServices(ctx context.Context) ([]model.Service, error) {
	return f(ctx)
}

type ContainerSource interface {
	Containers(context.Context) ([]model.Container, error)
}

type ContainerSourceFunc func(context.Context) ([]model.Container, error)

func (f ContainerSourceFunc) Containers(ctx context.Context) ([]model.Container, error) {
	return f(ctx)
}

type AlertSource interface {
	Alerts(context.Context) ([]model.Alert, error)
}

type AlertSourceFunc func(context.Context) ([]model.Alert, error)

func (f AlertSourceFunc) Alerts(ctx context.Context) ([]model.Alert, error) {
	return f(ctx)
}

type BackupSource interface {
	Backups(context.Context) ([]model.BackupStatus, error)
}

type BackupSourceFunc func(context.Context) ([]model.BackupStatus, error)

func (f BackupSourceFunc) Backups(ctx context.Context) ([]model.BackupStatus, error) {
	return f(ctx)
}

type Sources struct {
	Host       HostCollector
	Services   ServiceSource
	Containers ContainerSource
	Backups    BackupSource
	Alerts     AlertSource
}

// Hub samples providers once per interval and fans the same immutable snapshot
// out to every subscriber. A failed provider keeps its last valid component.
type Hub struct {
	sources  Sources
	interval time.Duration
	now      func() time.Time

	mu          sync.RWMutex
	latest      model.SnapshotEnvelope
	hasLatest   bool
	subscribers map[uint64]chan model.SnapshotEnvelope
	nextID      uint64
	lastSuccess time.Time
	lastAttempt time.Time
	lastError   string
}

func NewHub(sources Sources, interval time.Duration) *Hub {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Hub{
		sources: sources, interval: interval, now: time.Now,
		subscribers: make(map[uint64]chan model.SnapshotEnvelope),
	}
}

func (h *Hub) Run(ctx context.Context) error {
	if _, err := h.CollectOnce(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_, _ = h.CollectOnce(ctx)
		}
	}
}

func (h *Hub) CollectOnce(ctx context.Context) (model.SnapshotEnvelope, error) {
	h.mu.RLock()
	previous := h.latest
	hasPrevious := h.hasLatest
	h.mu.RUnlock()
	data := cloneSnapshotData(previous.Data)
	if !hasPrevious {
		data.Services = make([]model.Service, 0)
		data.Containers = make([]model.Container, 0)
		data.Backups = make([]model.BackupStatus, 0)
		data.Disks = make([]model.DiskStats, 0)
		data.Alerts = make([]model.Alert, 0)
	}

	type collectionError struct {
		source string
		err    error
	}
	var (
		wg               sync.WaitGroup
		resultMu         sync.Mutex
		errorsOut        []collectionError
		truncatedSources = make(map[string]struct{})
	)
	recordError := func(source string, err error) {
		resultMu.Lock()
		errorsOut = append(errorsOut, collectionError{source: source, err: err})
		resultMu.Unlock()
	}
	if h.sources.Host != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := h.sources.Host.Collect(ctx)
			resultMu.Lock()
			defer resultMu.Unlock()
			if err != nil {
				errorsOut = append(errorsOut, collectionError{source: "host", err: err})
				return
			}
			data.System, data.Disks, data.Network = value.System, value.Disks, value.Network
		}()
	}
	if h.sources.Services != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := h.sources.Services.ListServices(ctx)
			if err != nil {
				recordError("services", err)
				return
			}
			resultMu.Lock()
			if len(value) > 100 {
				truncatedSources["services"] = struct{}{}
			}
			data.Services = capSlice(value, 100)
			resultMu.Unlock()
		}()
	}
	if h.sources.Containers != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := h.sources.Containers.Containers(ctx)
			if err != nil {
				recordError("containers", err)
				return
			}
			resultMu.Lock()
			if len(value) > 100 {
				truncatedSources["containers"] = struct{}{}
			}
			data.Containers = capSlice(value, 100)
			resultMu.Unlock()
		}()
	}
	if h.sources.Backups != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := h.sources.Backups.Backups(ctx)
			if err != nil {
				recordError("backups", err)
				return
			}
			resultMu.Lock()
			if len(value) > 50 {
				truncatedSources["backups"] = struct{}{}
			}
			data.Backups = capSlice(value, 50)
			resultMu.Unlock()
		}()
	}
	wg.Wait()
	// Alerts may be derived from the container collection (for example restart
	// loops). Read them after the concurrent collectors finish so a snapshot
	// never pairs current container state with alerts from the previous tick.
	if h.sources.Alerts != nil {
		value, err := h.sources.Alerts.Alerts(ctx)
		if err != nil {
			recordError("alerts", err)
		} else {
			if len(value) > 50 {
				truncatedSources["alerts"] = struct{}{}
			}
			data.Alerts = capSlice(value, 50)
		}
	}
	now := h.now().UTC()

	staleSources := make([]string, 0, len(errorsOut))
	for _, item := range errorsOut {
		staleSources = append(staleSources, item.source)
		data.Alerts = append(data.Alerts, model.Alert{
			ID: "collector-" + item.source, Level: "error", Source: item.source,
			Message: fmt.Sprintf("Unable to collect %s data", item.source), OccurredAt: now,
		})
	}
	sort.Strings(staleSources)
	if len(data.Alerts) > 50 {
		truncatedSources["alerts"] = struct{}{}
	}
	data.Alerts = capSlice(data.Alerts, 50)
	truncated := make([]string, 0, len(truncatedSources))
	for source := range truncatedSources {
		truncated = append(truncated, source)
	}
	sort.Strings(truncated)
	sequence := previous.Sequence + 1
	envelope := model.SnapshotEnvelope{
		Version: 1, Type: "metrics.snapshot", Sequence: sequence,
		CollectedAt: now, Truncated: len(truncated) > 0, TruncatedSources: truncated,
		StaleSources: staleSources, Data: data,
	}
	h.publish(envelope)
	if len(errorsOut) > 0 {
		err := fmt.Errorf("%d snapshot provider(s) failed", len(errorsOut))
		h.recordAttempt(now, err)
		return envelope, err
	}
	h.recordAttempt(now, nil)
	return envelope, nil
}

type Health struct {
	Ready       bool      `json:"ready"`
	LastAttempt time.Time `json:"lastAttempt,omitempty"`
	LastSuccess time.Time `json:"lastSuccess,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
}

func (h *Hub) Health(maxAge time.Duration) Health {
	if maxAge <= 0 {
		maxAge = h.interval * 3
	}
	h.mu.RLock()
	result := Health{LastAttempt: h.lastAttempt, LastSuccess: h.lastSuccess, LastError: h.lastError}
	h.mu.RUnlock()
	result.Ready = !result.LastSuccess.IsZero() && h.now().UTC().Sub(result.LastSuccess) <= maxAge
	return result
}

func (h *Hub) Ready(maxAge time.Duration) error {
	health := h.Health(maxAge)
	if health.Ready {
		return nil
	}
	if health.LastSuccess.IsZero() {
		return errors.New("metrics: no successful snapshot has been collected")
	}
	return fmt.Errorf("metrics: last successful snapshot is stale: %s", health.LastError)
}

func (h *Hub) recordAttempt(at time.Time, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastAttempt = at
	if err == nil {
		h.lastSuccess = at
		h.lastError = ""
		return
	}
	h.lastError = err.Error()
}

func (h *Hub) Latest() (model.SnapshotEnvelope, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.latest, h.hasLatest
}

func (h *Hub) Subscribe() (<-chan model.SnapshotEnvelope, func()) {
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	channel := make(chan model.SnapshotEnvelope, 1)
	h.subscribers[id] = channel
	if h.hasLatest {
		channel <- h.latest
	}
	h.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers, id)
			close(channel)
			h.mu.Unlock()
		})
	}
	return channel, cancel
}

func (h *Hub) publish(snapshot model.SnapshotEnvelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latest = snapshot
	h.hasLatest = true
	for _, subscriber := range h.subscribers {
		select {
		case subscriber <- snapshot:
		default:
			select {
			case <-subscriber:
			default:
			}
			subscriber <- snapshot
		}
	}
}

func capSlice[T any](values []T, limit int) []T {
	if values == nil {
		return make([]T, 0)
	}
	if len(values) > limit {
		values = values[:limit]
	}
	return append(make([]T, 0, len(values)), values...)
}

func cloneSnapshotData(data model.SnapshotData) model.SnapshotData {
	data.Disks = capSlice(data.Disks, len(data.Disks))
	data.Services = capSlice(data.Services, len(data.Services))
	data.Containers = capSlice(data.Containers, len(data.Containers))
	data.Backups = capSlice(data.Backups, len(data.Backups))
	data.Alerts = capSlice(data.Alerts, len(data.Alerts))
	for index := range data.Containers {
		data.Containers[index].Ports = capSlice(data.Containers[index].Ports, len(data.Containers[index].Ports))
	}
	return data
}
