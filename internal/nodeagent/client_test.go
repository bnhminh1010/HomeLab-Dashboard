package nodeagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
)

func TestProducersSurfaceBackpressureForConnectionReconnect(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	newFullSink := func() *connectionSink {
		sink := newConnectionSink(t.Context(), "node_1", func() time.Time { return now })
		for range outboundQueueSize {
			sink.queue <- nodes.Message{}
		}
		return sink
	}
	agent := &runner{
		now: nowTime(now), startedAt: now.Add(-time.Minute), hostname: "rack-1", agentVersion: "test",
		collector: snapshotCollectorFunc(func(context.Context) (model.SnapshotEnvelope, error) {
			return model.SnapshotEnvelope{
				Version: 1, Type: "metrics.snapshot", Sequence: 1, CollectedAt: now,
				Data: model.SnapshotData{Disks: []model.DiskStats{}, Services: []model.Service{}, Containers: []model.Container{}, Alerts: []model.Alert{}},
			}, nil
		}),
	}
	if err := agent.runHeartbeat(t.Context(), newFullSink()); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("heartbeat producer error = %v", err)
	}
	if err := agent.runMetrics(t.Context(), newFullSink()); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("metrics producer error = %v", err)
	}
}

func TestReconnectBackoffResetsAfterStableConnection(t *testing.T) {
	if got := nextReconnectAttempt(8, serverSilenceTimeout-time.Second); got != 8 {
		t.Fatalf("short connection reset attempt to %d", got)
	}
	if got := nextReconnectAttempt(8, serverSilenceTimeout); got != 0 {
		t.Fatalf("stable connection kept attempt %d", got)
	}
}

type snapshotCollectorFunc func(context.Context) (model.SnapshotEnvelope, error)

func (function snapshotCollectorFunc) Collect(ctx context.Context) (model.SnapshotEnvelope, error) {
	return function(ctx)
}

func nowTime(value time.Time) func() time.Time {
	return func() time.Time { return value }
}
