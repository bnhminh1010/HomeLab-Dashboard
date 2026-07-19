package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/model"
)

type hostCollectorFunc func(context.Context) (HostSnapshot, error)

func (f hostCollectorFunc) Collect(ctx context.Context) (HostSnapshot, error) { return f(ctx) }

type serviceSourceFunc func(context.Context) ([]model.Service, error)

func (f serviceSourceFunc) ListServices(ctx context.Context) ([]model.Service, error) { return f(ctx) }

func TestHubFansOutOneSnapshotAndKeepsLastValidComponent(t *testing.T) {
	fail := false
	hostCalls := 0
	hub := NewHub(Sources{
		Host: hostCollectorFunc(func(context.Context) (HostSnapshot, error) {
			hostCalls++
			if fail {
				return HostSnapshot{}, errors.New("host unavailable")
			}
			return HostSnapshot{System: model.SystemStats{Hostname: "debian"}}, nil
		}),
		Services: serviceSourceFunc(func(context.Context) ([]model.Service, error) {
			return []model.Service{{ID: "svc_1"}}, nil
		}),
	}, time.Second)
	now := time.Unix(100, 0)
	hub.now = func() time.Time { return now }

	subscription, cancel := hub.Subscribe()
	defer cancel()
	first, err := hub.CollectOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || first.Data.System.Hostname != "debian" || hostCalls != 1 {
		t.Fatalf("unexpected first snapshot: %+v calls=%d", first, hostCalls)
	}
	if received := <-subscription; received.Sequence != first.Sequence {
		t.Fatalf("subscriber got sequence %d", received.Sequence)
	}

	fail = true
	now = now.Add(time.Second)
	second, err := hub.CollectOnce(context.Background())
	if err == nil {
		t.Fatal("expected partial collection error")
	}
	if second.Data.System.Hostname != "debian" || len(second.Data.Alerts) != 1 {
		t.Fatalf("last-known data was not preserved: %+v", second.Data)
	}
}

func TestHubSubscriberDropsSupersededSnapshot(t *testing.T) {
	hub := NewHub(Sources{}, time.Second)
	channel, cancel := hub.Subscribe()
	defer cancel()
	for range 3 {
		if _, err := hub.CollectOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := <-channel; got.Sequence != 3 {
		t.Fatalf("got sequence %d, want latest sequence 3", got.Sequence)
	}
}

func TestHubReadsDerivedAlertsAfterContainers(t *testing.T) {
	containersCollected := false
	hub := NewHub(Sources{
		Containers: ContainerSourceFunc(func(context.Context) ([]model.Container, error) {
			containersCollected = true
			return []model.Container{{ID: "restart-loop", State: "crashed"}}, nil
		}),
		Alerts: AlertSourceFunc(func(context.Context) ([]model.Alert, error) {
			if !containersCollected {
				return nil, errors.New("alerts read before containers")
			}
			return []model.Alert{{ID: "container:restart-loop", Level: "error"}}, nil
		}),
	}, time.Second)

	snapshot, err := hub.CollectOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Data.Alerts) != 1 || snapshot.Data.Alerts[0].ID != "container:restart-loop" {
		t.Fatalf("derived alerts missing from current snapshot: %+v", snapshot.Data.Alerts)
	}
}
