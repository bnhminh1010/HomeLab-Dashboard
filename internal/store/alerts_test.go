package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/alerts"
	_ "modernc.org/sqlite"
)

func TestAlertRuleCRUD(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	rule := alertTestRule()
	rule.NodeSelector = ""
	rule.ResourceSelector = ""

	created, err := store.CreateAlertRule(ctx, rule)
	if err != nil {
		t.Fatal(err)
	}
	if created.NodeSelector != alerts.WildcardSelector || created.ResourceSelector != alerts.WildcardSelector ||
		!created.CreatedAt.Equal(now) || !created.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected created rule: %+v", created)
	}
	got, err := store.GetAlertRule(ctx, rule.ID)
	if err != nil || got.For != rule.For || got.Cooldown != rule.Cooldown {
		t.Fatalf("get alert rule: %+v err=%v", got, err)
	}
	rules, err := store.ListAlertRules(ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("list alert rules: %+v err=%v", rules, err)
	}

	now = now.Add(time.Minute)
	got.Name = "CPU usage critical"
	got.Enabled = false
	updated, err := store.UpdateAlertRule(ctx, got.ID, got)
	if err != nil || updated.Name != got.Name || updated.Enabled || !updated.UpdatedAt.Equal(now) ||
		!updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("update alert rule: %+v err=%v", updated, err)
	}
	if _, err := store.UpdateAlertRule(ctx, "missing", got); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected update ErrNotFound, got %v", err)
	}

	if err := store.DeleteAlertRule(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAlertRule(ctx, got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected get ErrNotFound, got %v", err)
	}
	if err := store.DeleteAlertRule(ctx, got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected delete ErrNotFound, got %v", err)
	}
}

func TestAlertEngineStoreLifecycleAckSilenceAndQueue(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	rule := alertTestRule()
	rule.For = 0
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	clock := alerts.ClockFunc(func() time.Time { return now })
	engine, err := alerts.NewEngine(store, clock)
	if err != nil {
		t.Fatal(err)
	}
	sample := alerts.Sample{
		NodeID: "local", ResourceType: "host", ResourceID: "local",
		Metric: alerts.MetricCPUPercent, Value: 95, ObservedAt: now,
	}
	result, err := engine.Evaluate(ctx, []alerts.Sample{sample})
	if err != nil || result.Firing != 1 || result.Deliveries != 1 {
		t.Fatalf("fire alert: result=%+v err=%v", result, err)
	}
	key := alerts.AlertKey{RuleID: rule.ID, NodeID: "local", ResourceType: "host", ResourceID: "local"}
	state, found, err := store.LoadAlertState(ctx, key)
	if err != nil || !found || state.Status != alerts.StatusFiring {
		t.Fatalf("load firing state: found=%v state=%+v err=%v", found, state, err)
	}

	state, err = store.AcknowledgeAlert(ctx, key, "admin@example.com", now.Add(time.Minute))
	if err != nil || state.AcknowledgedBy != "admin@example.com" || state.AcknowledgedAt == nil {
		t.Fatalf("acknowledge: state=%+v err=%v", state, err)
	}
	state, err = store.SilenceAlert(ctx, key, "admin@example.com", 6*time.Hour, now.Add(2*time.Minute))
	if err != nil || state.SilencedUntil == nil || state.SilencedBy != "admin@example.com" {
		t.Fatalf("silence: state=%+v err=%v", state, err)
	}
	if _, err := store.SilenceAlert(ctx, key, "admin@example.com", 2*time.Hour, now); !errors.Is(err, alerts.ErrInvalidSilence) {
		t.Fatalf("expected invalid silence error, got %v", err)
	}

	events, err := store.ListAlertEvents(ctx, alerts.EventFilter{RuleID: rule.ID, Limit: 10})
	if err != nil || len(events) != 3 || events[0].Type != alerts.EventSilenced ||
		events[1].Type != alerts.EventAcknowledged || events[2].Type != alerts.EventFiring {
		t.Fatalf("unexpected events: %+v err=%v", events, err)
	}
	deliveries, err := store.ListAlertDeliveries(ctx, alerts.DeliverySuperseded, 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Kind != alerts.DeliveryFiring ||
		deliveries[0].LastError != "alert acknowledged" {
		t.Fatalf("superseded deliveries: %+v err=%v", deliveries, err)
	}
	claimed, err := store.ClaimDueAlertDeliveries(ctx, now.Add(24*time.Hour), 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("superseded delivery was reclaimed: %+v err=%v", claimed, err)
	}
}

func TestAlertDeliveryQueueRetryAndDelivery(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rule := alertTestRule()
	rule.For = 0
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	engine, _ := alerts.NewEngine(store, alerts.ClockFunc(func() time.Time { return now }))
	sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 95}
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDueAlertDeliveries(ctx, now, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 || claimed[0].Status != alerts.DeliveryProcessing {
		t.Fatalf("claim delivery: %+v err=%v", claimed, err)
	}
	active, err := store.IsAlertDeliveryClaimActive(ctx, claimed[0].ID, claimed[0].Attempts)
	if err != nil || !active {
		t.Fatalf("claimed delivery active=%v err=%v", active, err)
	}
	next := now.Add(time.Minute)
	if err := store.RescheduleAlertDelivery(ctx, claimed[0].ID, claimed[0].Attempts, next, "temporary"); err != nil {
		t.Fatal(err)
	}
	if claimed, err = store.ClaimDueAlertDeliveries(ctx, now, 10); err != nil || len(claimed) != 0 {
		t.Fatalf("delivery claimed before retry: %+v err=%v", claimed, err)
	}
	claimed, err = store.ClaimDueAlertDeliveries(ctx, next, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 2 {
		t.Fatalf("retry claim: %+v err=%v", claimed, err)
	}
	if err := store.MarkAlertDeliveryDelivered(ctx, claimed[0].ID, claimed[0].Attempts, next); err != nil {
		t.Fatal(err)
	}
	delivered, err := store.ListAlertDeliveries(ctx, alerts.DeliveryDelivered, 10)
	if err != nil || len(delivered) != 1 || delivered[0].DeliveredAt == nil {
		t.Fatalf("delivered queue: %+v err=%v", delivered, err)
	}
}

func TestAcknowledgeAndSilenceSupersedeFiringDelivery(t *testing.T) {
	tests := []struct {
		name       string
		claimFirst bool
		reason     string
		act        func(context.Context, *Store, alerts.AlertKey, time.Time) error
	}{
		{
			name: "acknowledge claimed delivery", claimFirst: true, reason: "alert acknowledged",
			act: func(ctx context.Context, store *Store, key alerts.AlertKey, at time.Time) error {
				_, err := store.AcknowledgeAlert(ctx, key, "admin@example.com", at)
				return err
			},
		},
		{
			name: "silence pending delivery", reason: "alert silenced",
			act: func(ctx context.Context, store *Store, key alerts.AlertKey, at time.Time) error {
				_, err := store.SilenceAlert(ctx, key, "admin@example.com", time.Hour, at)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := openAlertTestStore(t)
			now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
			rule := alertTestRule()
			rule.For = 0
			if _, err := store.CreateAlertRule(ctx, rule); err != nil {
				t.Fatal(err)
			}
			engine, _ := alerts.NewEngine(store, alerts.ClockFunc(func() time.Time { return now }))
			sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 95}
			if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
				t.Fatal(err)
			}
			pending, err := store.ListAlertDeliveries(ctx, alerts.DeliveryPending, 10)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending delivery: %+v err=%v", pending, err)
			}
			deliveryID := pending[0].ID
			if test.claimFirst {
				claimed, err := store.ClaimDueAlertDeliveries(ctx, now, 10)
				if err != nil || len(claimed) != 1 || claimed[0].ID != deliveryID {
					t.Fatalf("claim before lifecycle action: %+v err=%v", claimed, err)
				}
			}

			key := alerts.AlertKey{RuleID: rule.ID, NodeID: "local", ResourceType: "host", ResourceID: "local"}
			if err := test.act(ctx, store, key, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			attempt := pending[0].Attempts
			if test.claimFirst {
				attempt++
			}
			active, err := store.IsAlertDeliveryClaimActive(ctx, deliveryID, attempt)
			if err != nil || active {
				t.Fatalf("superseded delivery active=%v err=%v", active, err)
			}
			superseded, err := store.ListAlertDeliveries(ctx, alerts.DeliverySuperseded, 10)
			if err != nil || len(superseded) != 1 || superseded[0].ID != deliveryID ||
				superseded[0].LastError != test.reason {
				t.Fatalf("superseded delivery: %+v err=%v", superseded, err)
			}
			if test.claimFirst {
				if err := store.MarkAlertDeliveryDelivered(ctx, deliveryID, attempt, now.Add(2*time.Minute)); err != nil {
					t.Fatalf("late delivery completion broke superseded lease: %v", err)
				}
				if err := store.RescheduleAlertDelivery(ctx, deliveryID, attempt, now.Add(time.Hour), "late retry"); err != nil {
					t.Fatalf("late retry broke superseded lease: %v", err)
				}
				if err := store.MarkAlertDeliveryDead(ctx, deliveryID, attempt, "late failure"); err != nil {
					t.Fatalf("late failure broke superseded lease: %v", err)
				}
				superseded, err = store.ListAlertDeliveries(ctx, alerts.DeliverySuperseded, 10)
				if err != nil || len(superseded) != 1 || superseded[0].LastError != test.reason {
					t.Fatalf("late worker mutated superseded delivery: %+v err=%v", superseded, err)
				}
			}
			claimed, err := store.ClaimDueAlertDeliveries(ctx, now.Add(24*time.Hour), 10)
			if err != nil || len(claimed) != 0 {
				t.Fatalf("superseded delivery reclaimed: %+v err=%v", claimed, err)
			}
		})
	}
}

func TestAlertResolutionNeedsTwoCleanSamplesAndResolvedCannotBeAcknowledged(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rule := alertTestRule()
	rule.For = 0
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	engine, _ := alerts.NewEngine(store, alerts.ClockFunc(func() time.Time { return now }))
	sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 95}
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	sample.Value = 20
	if result, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil || result.Firing != 1 {
		t.Fatalf("first clean evaluation: %+v err=%v", result, err)
	}
	now = now.Add(time.Minute)
	if result, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil || result.Resolved != 1 || result.Deliveries != 1 {
		t.Fatalf("second clean evaluation: %+v err=%v", result, err)
	}
	key := alerts.AlertKey{RuleID: rule.ID, NodeID: "local", ResourceType: "host", ResourceID: "local"}
	if _, err := store.AcknowledgeAlert(ctx, key, "admin@example.com", now); !errors.Is(err, alerts.ErrAlertResolved) {
		t.Fatalf("expected ErrAlertResolved, got %v", err)
	}
	all, err := store.ListAlertDeliveries(ctx, "", 10)
	if err != nil || len(all) != 2 || all[0].Kind != alerts.DeliveryResolved ||
		all[0].Status != alerts.DeliveryPending || all[1].Kind != alerts.DeliveryFiring ||
		all[1].Status != alerts.DeliverySuperseded || all[1].LastError != "alert resolved" {
		t.Fatalf("resolution delivery ordering: %+v err=%v", all, err)
	}
	claimed, err := store.ClaimDueAlertDeliveries(ctx, now, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Kind != alerts.DeliveryResolved {
		t.Fatalf("resolution claimed stale firing: %+v err=%v", claimed, err)
	}
}

func TestNewIncidentSupersedesStaleResolvedDelivery(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rule := alertTestRule()
	rule.For = 0
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	engine, _ := alerts.NewEngine(store, alerts.ClockFunc(func() time.Time { return now }))
	sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 95}
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	firing, err := store.ClaimDueAlertDeliveries(ctx, now, 10)
	if err != nil || len(firing) != 1 {
		t.Fatalf("claim initial firing: %+v err=%v", firing, err)
	}
	if err := store.MarkAlertDeliveryDelivered(ctx, firing[0].ID, firing[0].Attempts, now); err != nil {
		t.Fatal(err)
	}

	sample.Value = 20
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ClaimDueAlertDeliveries(ctx, now, 10)
	if err != nil || len(resolved) != 1 || resolved[0].Kind != alerts.DeliveryResolved {
		t.Fatalf("claim resolved delivery: %+v err=%v", resolved, err)
	}
	if err := store.RescheduleAlertDelivery(ctx, resolved[0].ID, resolved[0].Attempts, now.Add(time.Hour), "ntfy unavailable"); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute)
	sample.Value = 99
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	superseded, err := store.ListAlertDeliveries(ctx, alerts.DeliverySuperseded, 10)
	if err != nil || len(superseded) != 1 || superseded[0].ID != resolved[0].ID ||
		superseded[0].Kind != alerts.DeliveryResolved || superseded[0].LastError != "new alert incident started" {
		t.Fatalf("stale resolved delivery: %+v err=%v", superseded, err)
	}
	due, err := store.ClaimDueAlertDeliveries(ctx, now.Add(2*time.Hour), 10)
	if err != nil || len(due) != 1 || due[0].Kind != alerts.DeliveryFiring {
		t.Fatalf("new incident delivery ordering: %+v err=%v", due, err)
	}
}

type pausedAlertRepository struct {
	*Store
	loaded chan struct{}
	resume chan struct{}
}

func (repository *pausedAlertRepository) LoadAlertState(ctx context.Context, key alerts.AlertKey) (alerts.AlertState, bool, error) {
	state, found, err := repository.Store.LoadAlertState(ctx, key)
	repository.loaded <- struct{}{}
	<-repository.resume
	return state, found, err
}

func TestAlertTransitionCASPreservesConcurrentAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	rule := alertTestRule()
	rule.For = 0
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	clock := alerts.ClockFunc(func() time.Time { return now })
	engine, _ := alerts.NewEngine(store, clock)
	sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 99}
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(rule.Cooldown)
	repository := &pausedAlertRepository{Store: store, loaded: make(chan struct{}, 1), resume: make(chan struct{})}
	pausedEngine, _ := alerts.NewEngine(repository, clock)
	type evaluation struct {
		result alerts.EvaluationResult
		err    error
	}
	completed := make(chan evaluation, 1)
	go func() {
		result, err := pausedEngine.Evaluate(ctx, []alerts.Sample{sample})
		completed <- evaluation{result: result, err: err}
	}()
	<-repository.loaded
	key := alerts.AlertKey{RuleID: rule.ID, NodeID: "local", ResourceType: "host", ResourceID: "local"}
	acknowledged, err := store.AcknowledgeAlert(ctx, key, "admin@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	close(repository.resume)
	outcome := <-completed
	if outcome.err != nil || outcome.result.Evaluated != 0 {
		t.Fatalf("stale evaluation outcome: %+v err=%v", outcome.result, outcome.err)
	}
	state, found, err := store.LoadAlertState(ctx, key)
	if err != nil || !found || state.AcknowledgedBy != "admin@example.com" ||
		state.AcknowledgedAt == nil || state.Revision != acknowledged.Revision {
		t.Fatalf("acknowledgement overwritten: found=%v state=%+v err=%v", found, state, err)
	}
	events, err := store.ListAlertEvents(ctx, alerts.EventFilter{RuleID: rule.ID, Limit: 10})
	if err != nil || len(events) != 2 || events[0].Type != alerts.EventAcknowledged || events[1].Type != alerts.EventFiring {
		t.Fatalf("stale firing event committed after ack: %+v err=%v", events, err)
	}
	pending, err := store.ListAlertDeliveries(ctx, alerts.DeliveryPending, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("stale firing delivery committed after ack: %+v err=%v", pending, err)
	}
}

func TestAlertTransitionCASPreservesConcurrentSilence(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	rule := alertTestRule()
	rule.For = 0
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	clock := alerts.ClockFunc(func() time.Time { return now })
	engine, _ := alerts.NewEngine(store, clock)
	sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 99}
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(rule.Cooldown)
	repository := &pausedAlertRepository{Store: store, loaded: make(chan struct{}, 1), resume: make(chan struct{})}
	pausedEngine, _ := alerts.NewEngine(repository, clock)
	completed := make(chan error, 1)
	go func() {
		_, err := pausedEngine.Evaluate(ctx, []alerts.Sample{sample})
		completed <- err
	}()
	<-repository.loaded
	key := alerts.AlertKey{RuleID: rule.ID, NodeID: "local", ResourceType: "host", ResourceID: "local"}
	silenced, err := store.SilenceAlert(ctx, key, "admin@example.com", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	close(repository.resume)
	if err := <-completed; err != nil {
		t.Fatalf("stale evaluation returned error: %v", err)
	}
	state, found, err := store.LoadAlertState(ctx, key)
	if err != nil || !found || state.SilencedBy != "admin@example.com" || state.SilencedUntil == nil ||
		state.Revision != silenced.Revision {
		t.Fatalf("silence overwritten: found=%v state=%+v err=%v", found, state, err)
	}
	events, err := store.ListAlertEvents(ctx, alerts.EventFilter{RuleID: rule.ID, Limit: 10})
	if err != nil || len(events) != 2 || events[0].Type != alerts.EventSilenced || events[1].Type != alerts.EventFiring {
		t.Fatalf("stale firing event committed after silence: %+v err=%v", events, err)
	}
	pending, err := store.ListAlertDeliveries(ctx, alerts.DeliveryPending, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("stale firing delivery committed after silence: %+v err=%v", pending, err)
	}
}

func TestAlertTransitionCASRejectsConcurrentRuleUpdateAndDelete(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			store := openAlertTestStore(t)
			now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
			store.now = func() time.Time { return now }
			rule := alertTestRule()
			rule.For = 0
			if _, err := store.CreateAlertRule(ctx, rule); err != nil {
				t.Fatal(err)
			}
			clock := alerts.ClockFunc(func() time.Time { return now })
			engine, _ := alerts.NewEngine(store, clock)
			sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 99}
			if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(rule.Cooldown)
			repository := &pausedAlertRepository{Store: store, loaded: make(chan struct{}, 1), resume: make(chan struct{})}
			pausedEngine, _ := alerts.NewEngine(repository, clock)
			completed := make(chan error, 1)
			go func() {
				_, err := pausedEngine.Evaluate(ctx, []alerts.Sample{sample})
				completed <- err
			}()
			<-repository.loaded
			if action == "update" {
				rule.Name = "Updated while evaluation was paused"
				if _, err := store.UpdateAlertRule(ctx, rule.ID, rule); err != nil {
					t.Fatal(err)
				}
			} else if err := store.DeleteAlertRule(ctx, rule.ID); err != nil {
				t.Fatal(err)
			}
			close(repository.resume)
			if err := <-completed; err != nil {
				t.Fatalf("stale evaluation returned error: %v", err)
			}
			states, err := store.ListAlertStates(ctx, alerts.StateFilter{RuleID: rule.ID})
			if err != nil || len(states) != 0 {
				t.Fatalf("stale evaluation recreated state: %+v err=%v", states, err)
			}
			pending, err := store.ListAlertDeliveries(ctx, alerts.DeliveryPending, 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("stale evaluation recreated delivery: %+v err=%v", pending, err)
			}
		})
	}
}

func TestDeleteRuleKeepsHistoryAndSupersedesQueuedDelivery(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rule := alertTestRule()
	rule.For = 0
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	engine, _ := alerts.NewEngine(store, alerts.ClockFunc(func() time.Time { return now }))
	sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 99}
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAlertRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	states, err := store.ListAlertStates(ctx, alerts.StateFilter{RuleID: rule.ID})
	if err != nil || len(states) != 0 {
		t.Fatalf("live states not removed: %+v err=%v", states, err)
	}
	deliveries, err := store.ListAlertDeliveries(ctx, alerts.DeliveryPending, 10)
	if err != nil || len(deliveries) != 0 {
		t.Fatalf("queued deliveries not removed: %+v err=%v", deliveries, err)
	}
	superseded, err := store.ListAlertDeliveries(ctx, alerts.DeliverySuperseded, 10)
	if err != nil || len(superseded) != 1 || superseded[0].LastError != "alert rule deleted" {
		t.Fatalf("delivery history not superseded: %+v err=%v", superseded, err)
	}
	events, err := store.ListAlertEvents(ctx, alerts.EventFilter{RuleID: rule.ID})
	if err != nil || len(events) != 1 {
		t.Fatalf("history should survive rule deletion: %+v err=%v", events, err)
	}
}

func TestUpdateRuleResetsStaleStateAndSupersedesQueuedDelivery(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rule := alertTestRule()
	rule.For = 0
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	engine, _ := alerts.NewEngine(store, alerts.ClockFunc(func() time.Time { return now }))
	sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 99}
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDueAlertDeliveries(ctx, now, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Status != alerts.DeliveryProcessing {
		t.Fatalf("claim delivery before rule update: %+v err=%v", claimed, err)
	}
	rule.Name = "Updated CPU rule"
	if _, err := store.UpdateAlertRule(ctx, rule.ID, rule); err != nil {
		t.Fatal(err)
	}
	states, err := store.ListAlertStates(ctx, alerts.StateFilter{RuleID: rule.ID})
	if err != nil || len(states) != 0 {
		t.Fatalf("stale states not reset: %+v err=%v", states, err)
	}
	deliveries, err := store.ListAlertDeliveries(ctx, alerts.DeliveryPending, 10)
	if err != nil || len(deliveries) != 0 {
		t.Fatalf("stale deliveries not reset: %+v err=%v", deliveries, err)
	}
	superseded, err := store.ListAlertDeliveries(ctx, alerts.DeliverySuperseded, 10)
	if err != nil || len(superseded) != 1 || superseded[0].LastError != "alert rule updated" {
		t.Fatalf("stale delivery history not superseded: %+v err=%v", superseded, err)
	}
	events, err := store.ListAlertEvents(ctx, alerts.EventFilter{RuleID: rule.ID})
	if err != nil || len(events) != 1 {
		t.Fatalf("history should survive rule update: %+v err=%v", events, err)
	}
}

func TestDeliveryClaimLeaseRecoversAbandonedWork(t *testing.T) {
	ctx := context.Background()
	store := openAlertTestStore(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rule := alertTestRule()
	rule.For = 0
	if _, err := store.CreateAlertRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	engine, _ := alerts.NewEngine(store, alerts.ClockFunc(func() time.Time { return now }))
	sample := alerts.Sample{NodeID: "local", ResourceType: "host", ResourceID: "local", Metric: alerts.MetricCPUPercent, Value: 99}
	if _, err := engine.Evaluate(ctx, []alerts.Sample{sample}); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimDueAlertDeliveries(ctx, now, 10)
	if err != nil || len(first) != 1 || first[0].Attempts != 1 {
		t.Fatalf("initial claim: %+v err=%v", first, err)
	}
	claimed, err := store.ClaimDueAlertDeliveries(ctx, now.Add(4*time.Minute), 10)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("active lease was stolen: %+v err=%v", claimed, err)
	}
	claimed, err = store.ClaimDueAlertDeliveries(ctx, now.Add(5*time.Minute), 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != first[0].ID || claimed[0].Attempts != 2 {
		t.Fatalf("abandoned lease was not recovered: %+v err=%v", claimed, err)
	}
	oldActive, err := store.IsAlertDeliveryClaimActive(ctx, first[0].ID, first[0].Attempts)
	if err != nil || oldActive {
		t.Fatalf("expired lease active=%v err=%v", oldActive, err)
	}
	newActive, err := store.IsAlertDeliveryClaimActive(ctx, claimed[0].ID, claimed[0].Attempts)
	if err != nil || !newActive {
		t.Fatalf("replacement lease active=%v err=%v", newActive, err)
	}
	if err := store.MarkAlertDeliveryDelivered(ctx, first[0].ID, first[0].Attempts, now.Add(5*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired lease mutated replacement: %v", err)
	}
	newActive, err = store.IsAlertDeliveryClaimActive(ctx, claimed[0].ID, claimed[0].Attempts)
	if err != nil || !newActive {
		t.Fatalf("replacement lease changed by expired worker: active=%v err=%v", newActive, err)
	}
}

func TestAlertMigrationToleratesUnrelated002Record(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES ('002_history.sql', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, version := range []string{"003_alerts.sql", "007_alert_state_revision.sql"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s migration missing: count=%d err=%v", version, count, err)
		}
	}
	if _, err := store.ListAlertRules(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultAlertRulesAreSeededOnlyOnce(t *testing.T) {
	ctx := context.Background()
	database := openAlertTestStore(t)
	defaults := alerts.DefaultRules()
	if err := database.SeedDefaultAlertRules(ctx, defaults); err != nil {
		t.Fatal(err)
	}
	rules, err := database.ListAlertRules(ctx)
	if err != nil || len(rules) != len(defaults) {
		t.Fatalf("seeded rules = %d, error = %v", len(rules), err)
	}
	for _, rule := range rules {
		if err := database.DeleteAlertRule(ctx, rule.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.SeedDefaultAlertRules(ctx, defaults); err != nil {
		t.Fatal(err)
	}
	rules, err = database.ListAlertRules(ctx)
	if err != nil || len(rules) != 0 {
		t.Fatalf("intentional empty rule set was re-seeded: %d rules, error = %v", len(rules), err)
	}
}

func TestDefaultAlertRuleUpgradeAddsMissingRulesOnlyOnce(t *testing.T) {
	ctx := context.Background()
	database := openAlertTestStore(t)
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO app_state(key, value, updated_at) VALUES (?, '1', ?)`,
		alertDefaultsSeededKey, formatAlertTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := database.SeedDefaultAlertRules(ctx, alerts.DefaultRules()); err != nil {
		t.Fatal(err)
	}
	rules, err := database.ListAlertRules(ctx)
	if err != nil || len(rules) != 2 {
		t.Fatalf("upgraded default rules = %+v, error = %v", rules, err)
	}
	for _, rule := range rules {
		if rule.ID != "default_node_offline" && rule.ID != "default_backup_unhealthy" {
			t.Fatalf("unexpected upgraded default rule = %+v", rule)
		}
		if err := database.DeleteAlertRule(ctx, rule.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.SeedDefaultAlertRules(ctx, alerts.DefaultRules()); err != nil {
		t.Fatal(err)
	}
	rules, err = database.ListAlertRules(ctx)
	if err != nil || len(rules) != 0 {
		t.Fatalf("deleted upgraded default reappeared: %+v, error = %v", rules, err)
	}
}

func TestDefaultAlertRuleUpgradeAddsBackupRuleForVersionTwo(t *testing.T) {
	ctx := context.Background()
	database := openAlertTestStore(t)
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO app_state(key, value, updated_at) VALUES (?, '2', ?)`,
		alertDefaultsSeededKey, formatAlertTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := database.SeedDefaultAlertRules(ctx, alerts.DefaultRules()); err != nil {
		t.Fatal(err)
	}
	rules, err := database.ListAlertRules(ctx)
	if err != nil || len(rules) != 1 || rules[0].ID != "default_backup_unhealthy" {
		t.Fatalf("version two upgrade = %+v, error = %v", rules, err)
	}
}

func openAlertTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func alertTestRule() alerts.AlertRule {
	return alerts.AlertRule{
		ID: "cpu_high", Name: "CPU high", ResourceType: "host",
		NodeSelector: alerts.WildcardSelector, ResourceSelector: alerts.WildcardSelector,
		Metric: alerts.MetricCPUPercent, Operator: alerts.OperatorGreaterThan, Threshold: 90,
		For: 5 * time.Minute, Severity: alerts.SeverityWarning, Cooldown: 30 * time.Minute, Enabled: true,
	}
}
