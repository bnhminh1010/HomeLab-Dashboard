package nodeagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/metrics"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

func TestLocalCollectorKeepsLastValidComponent(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	hostCalls := 0
	host := hostCollectorFunc(func(context.Context) (metrics.HostSnapshot, error) {
		hostCalls++
		if hostCalls > 1 {
			return metrics.HostSnapshot{}, errors.New("proc unavailable")
		}
		return metrics.HostSnapshot{System: model.SystemStats{Hostname: "rack-1"}}, nil
	})
	containers := &containerCollectorFunc{items: []model.Container{{ID: "c1", Name: "app"}}}
	collector := &LocalCollector{host: host, containers: containers, cores: 4, now: func() time.Time { return now }}
	first, err := collector.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || first.Data.System.Hostname != "rack-1" || len(first.Data.Containers) != 1 {
		t.Fatalf("unexpected first snapshot: %#v", first)
	}
	second, err := collector.Collect(t.Context())
	if err == nil {
		t.Fatal("partial collection did not report an error")
	}
	if second.Sequence != 2 || second.Data.System.Hostname != "rack-1" || len(second.Data.Alerts) != 1 {
		t.Fatalf("last host component was not retained: %#v", second)
	}
	if len(second.StaleSources) != 1 || second.StaleSources[0] != "host" {
		t.Fatalf("stale source metadata = %v", second.StaleSources)
	}
}

type hostCollectorFunc func(context.Context) (metrics.HostSnapshot, error)

func (function hostCollectorFunc) Collect(ctx context.Context) (metrics.HostSnapshot, error) {
	return function(ctx)
}

type containerCollectorFunc struct {
	items []model.Container
	err   error
}

func (collector *containerCollectorFunc) Collect(context.Context, int) ([]model.Container, []model.Alert, error) {
	return collector.items, nil, collector.err
}
