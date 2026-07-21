package slo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/history"
)

func TestCalculateExcludesUnknownFromAvailability(t *testing.T) {
	policy := Policy{ServiceID: "immich", TargetPercent: 99.5, WindowDays: 30}
	report := Calculate("immich", "Immich", "local", policy,
		time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		[]history.ServiceUptimePoint{{UpSeconds: 900, DownSeconds: 90, DegradedSeconds: 10, UnknownSeconds: 3_600}})

	if !report.Known || report.ObservedSeconds != 1_000 || report.UnknownSeconds != 3_600 {
		t.Fatalf("unexpected observation accounting: %+v", report)
	}
	if report.AvailabilityPercent == nil || *report.AvailabilityPercent != 90 {
		t.Fatalf("availability = %v, want 90", report.AvailabilityPercent)
	}
	if !report.ErrorBudgetExhausted || !report.AtRisk {
		t.Fatalf("unexpected budget state: %+v", report)
	}
}

func TestCalculateNoObservedTimeIsUnknown(t *testing.T) {
	report := Calculate("svc", "Service", "local", Policy{
		ServiceID: "svc", TargetPercent: DefaultTargetPercent, WindowDays: DefaultWindowDays,
	}, time.Now().Add(-time.Hour), time.Now(), []history.ServiceUptimePoint{{UnknownSeconds: 3600}})
	if report.Known || report.AvailabilityPercent != nil || report.ErrorBudgetRemainingPercent != nil {
		t.Fatalf("unknown-only service should not have an SLO result: %+v", report)
	}
}

func TestCalculateInvalidPolicyDoesNotProduceMetrics(t *testing.T) {
	report := Calculate("svc", "Service", "local", Policy{
		ServiceID: "svc", TargetPercent: 100, WindowDays: 30,
	}, time.Now().Add(-time.Hour), time.Now(), []history.ServiceUptimePoint{{UpSeconds: 3600}})
	if report.Known || report.AvailabilityPercent != nil || report.BurnRate != nil {
		t.Fatalf("invalid policy should not produce metrics: %+v", report)
	}
}

func TestServiceListUsesDefaultsAndWindowOverride(t *testing.T) {
	now := time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC)
	repository := &memoryPolicies{policies: []Policy{{
		ServiceID: "custom", TargetPercent: 99, WindowDays: 7,
	}}}
	historyReader := &recordingHistory{}
	service, err := NewService(repository, historyReader, Options{Clock: testClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	reports, err := service.List(context.Background(), "local", []ServiceRef{
		{ID: "default", Name: "Default"}, {ID: "custom", Name: "Custom"}, {ID: "custom", Name: "Duplicate"},
	}, 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || len(historyReader.calls) != 2 {
		t.Fatalf("reports = %+v", reports)
	}
	if reports[0].ServiceID != "custom" || reports[0].Policy.TargetPercent != 99 || reports[0].Policy.WindowDays != 7 {
		t.Fatalf("custom report = %+v", reports[0])
	}
	if reports[1].ServiceID != "default" || reports[1].Policy.TargetPercent != DefaultTargetPercent || reports[1].Policy.WindowDays != DefaultWindowDays {
		t.Fatalf("default report = %+v", reports[1])
	}
	for _, call := range historyReader.calls {
		if call.from != now.AddDate(0, 0, -90) || call.to != now {
			t.Fatalf("window override not applied: %+v", call)
		}
	}
}

func TestServiceListAlignsWindowToCompletedHourlyBuckets(t *testing.T) {
	now := time.Date(2026, 7, 22, 15, 37, 42, 0, time.UTC)
	historyReader := &recordingHistory{}
	service, err := NewService(&memoryPolicies{}, historyReader, Options{Clock: testClock{now: now}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(context.Background(), "local", []ServiceRef{{ID: "svc", Name: "Service"}}, 7); err != nil {
		t.Fatal(err)
	}
	if len(historyReader.calls) != 1 {
		t.Fatalf("history calls = %+v", historyReader.calls)
	}
	wantTo := time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC)
	wantFrom := wantTo.AddDate(0, 0, -7)
	if got := historyReader.calls[0]; !got.from.Equal(wantFrom) || !got.to.Equal(wantTo) {
		t.Fatalf("hour-aligned range = %+v; want from=%s to=%s", got, wantFrom, wantTo)
	}
}

func TestUpdatePolicyValidatesInput(t *testing.T) {
	service, err := NewService(&memoryPolicies{}, &recordingHistory{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdatePolicy(context.Background(), "svc", Input{TargetPercent: 100, WindowDays: 30}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("expected invalid policy, got %v", err)
	}
	if _, err := service.UpdatePolicy(context.Background(), "svc", Input{TargetPercent: MaxTargetPercent, WindowDays: 30}); err != nil {
		t.Fatalf("maximum supported target was rejected: %v", err)
	}
	if _, err := service.UpdatePolicy(context.Background(), "svc", Input{TargetPercent: MaxTargetPercent + 0.0001, WindowDays: 30}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("target above product maximum was accepted: %v", err)
	}
	if _, err := service.UpdatePolicy(context.Background(), "svc", Input{TargetPercent: 99.5, WindowDays: 1}); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("expected invalid window, got %v", err)
	}
}

type testClock struct{ now time.Time }

func (c testClock) Now() time.Time { return c.now }

type memoryPolicies struct{ policies []Policy }

func (m *memoryPolicies) ListSLOPolicies(context.Context) ([]Policy, error) {
	return append([]Policy(nil), m.policies...), nil
}

func (m *memoryPolicies) UpsertSLOPolicy(_ context.Context, policy Policy) (Policy, error) {
	return policy, nil
}

type historyCall struct {
	from time.Time
	to   time.Time
}

type recordingHistory struct{ calls []historyCall }

func (r *recordingHistory) QueryServiceUptime(_ context.Context, _ string, _ string, from, to time.Time) ([]history.ServiceUptimePoint, error) {
	r.calls = append(r.calls, historyCall{from: from, to: to})
	return nil, nil
}
