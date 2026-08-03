package alerts

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (clock *fakeClock) Now() time.Time                 { return clock.now }
func (clock *fakeClock) Advance(duration time.Duration) { clock.now = clock.now.Add(duration) }

type memoryRepository struct {
	rules       []AlertRule
	states      map[AlertKey]AlertState
	transitions []Transition
	listErr     error
}

func newMemoryRepository(rules ...AlertRule) *memoryRepository {
	return &memoryRepository{rules: rules, states: make(map[AlertKey]AlertState)}
}

func (repository *memoryRepository) ListAlertRules(context.Context) ([]AlertRule, error) {
	return append([]AlertRule(nil), repository.rules...), repository.listErr
}

func (repository *memoryRepository) LoadAlertState(_ context.Context, key AlertKey) (AlertState, bool, error) {
	state, found := repository.states[key]
	return state, found, nil
}

func (repository *memoryRepository) ApplyAlertTransition(_ context.Context, transition Transition) error {
	repository.states[transition.State.AlertKey] = transition.State
	repository.transitions = append(repository.transitions, transition)
	return nil
}

func TestEngineLifecycleCooldownAndResolution(t *testing.T) {
	start := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	rule := testRule()
	repository := newMemoryRepository(rule)
	engine, err := NewEngine(repository, clock)
	if err != nil {
		t.Fatal(err)
	}
	sample := testSample(95)

	result := evaluate(t, engine, sample)
	assertResult(t, result, StatusPending, 0)
	transition := lastTransition(repository)
	if transition.Event == nil || transition.Event.Type != EventPending || transition.Delivery != nil {
		t.Fatalf("unexpected initial transition: %+v", transition)
	}

	clock.Advance(4 * time.Minute)
	result = evaluate(t, engine, sample)
	assertResult(t, result, StatusPending, 0)

	clock.Advance(time.Minute)
	result = evaluate(t, engine, sample)
	assertResult(t, result, StatusFiring, 1)
	transition = lastTransition(repository)
	if transition.Event == nil || transition.Event.Type != EventFiring || transition.Delivery.Kind != DeliveryFiring {
		t.Fatalf("unexpected firing transition: %+v", transition)
	}

	clock.Advance(10 * time.Minute)
	result = evaluate(t, engine, sample)
	assertResult(t, result, StatusFiring, 0)
	if lastTransition(repository).Event != nil {
		t.Fatal("sustained alert inside cooldown emitted a duplicate event")
	}

	clock.Advance(20 * time.Minute)
	result = evaluate(t, engine, sample)
	assertResult(t, result, StatusFiring, 1)

	sample.Value = 40
	clock.Advance(time.Minute)
	result = evaluate(t, engine, sample)
	assertResult(t, result, StatusFiring, 0)
	if lastTransition(repository).State.CleanEvaluations != 1 {
		t.Fatalf("first clean evaluation should not resolve: %+v", lastTransition(repository).State)
	}

	clock.Advance(time.Minute)
	result = evaluate(t, engine, sample)
	assertResult(t, result, StatusResolved, 1)
	transition = lastTransition(repository)
	if transition.Event == nil || transition.Event.Type != EventResolved || transition.Delivery.Kind != DeliveryResolved {
		t.Fatalf("unexpected resolved transition: %+v", transition)
	}
}

func TestEngineDoesNotQueueDeliveriesWhenNotificationsAreDisabled(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)}
	rule := testRule()
	rule.For = 0
	repository := newMemoryRepository(rule)
	engine, err := NewEngineWithOptions(repository, clock, EngineOptions{DeliveryEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	result := evaluate(t, engine, testSample(95))
	assertResult(t, result, StatusFiring, 0)
	transition := lastTransition(repository)
	if transition.Delivery != nil || transition.State.LastNotifiedAt == nil || transition.Event == nil {
		t.Fatalf("disabled notification transition = %+v", transition)
	}
}

func TestEngineDisabledDeliveryPreservesFiringCooldown(t *testing.T) {
	start := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	rule := testRule()
	rule.For = 0
	repository := newMemoryRepository(rule)
	engine, err := NewEngineWithOptions(repository, clock, EngineOptions{DeliveryEnabled: false})
	if err != nil {
		t.Fatal(err)
	}

	evaluate(t, engine, testSample(95))
	first := lastTransition(repository)
	if first.State.LastNotifiedAt == nil || !first.State.LastNotifiedAt.Equal(start) {
		t.Fatalf("initial cooldown marker = %v, want %v", first.State.LastNotifiedAt, start)
	}

	clock.Advance(time.Second)
	evaluate(t, engine, testSample(95))
	insideCooldown := lastTransition(repository)
	if insideCooldown.Event != nil || insideCooldown.Delivery != nil {
		t.Fatalf("disabled delivery repeated firing inside cooldown: %+v", insideCooldown)
	}
	if insideCooldown.State.LastNotifiedAt == nil || !insideCooldown.State.LastNotifiedAt.Equal(start) {
		t.Fatalf("cooldown marker changed inside cooldown: %v", insideCooldown.State.LastNotifiedAt)
	}

	clock.Advance(rule.Cooldown - time.Second)
	evaluate(t, engine, testSample(95))
	afterCooldown := lastTransition(repository)
	if afterCooldown.Event == nil || afterCooldown.Event.Type != EventFiring || afterCooldown.Delivery != nil {
		t.Fatalf("cooldown reminder transition = %+v", afterCooldown)
	}
	if afterCooldown.State.LastNotifiedAt == nil || !afterCooldown.State.LastNotifiedAt.Equal(clock.Now()) {
		t.Fatalf("renewed cooldown marker = %v, want %v", afterCooldown.State.LastNotifiedAt, clock.Now())
	}
}

func TestEngineAcknowledgementAndSilenceSuppressFiringReminders(t *testing.T) {
	start := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	rule := testRule()
	rule.For = 0
	repository := newMemoryRepository(rule)
	engine, _ := NewEngine(repository, clock)
	sample := testSample(95)

	result := evaluate(t, engine, sample)
	assertResult(t, result, StatusFiring, 1)
	key := lastTransition(repository).State.AlertKey
	state := repository.states[key]
	state.AcknowledgedAt = timePointer(clock.Now())
	state.AcknowledgedBy = "admin@example.com"
	repository.states[key] = state
	clock.Advance(rule.Cooldown)
	result = evaluate(t, engine, sample)
	assertResult(t, result, StatusFiring, 0)

	state = repository.states[key]
	state.AcknowledgedAt = nil
	state.AcknowledgedBy = ""
	state.LastNotifiedAt = nil
	until := clock.Now().Add(time.Hour)
	state.SilencedUntil = &until
	repository.states[key] = state
	result = evaluate(t, engine, sample)
	assertResult(t, result, StatusFiring, 0)

	clock.Advance(time.Hour)
	result = evaluate(t, engine, sample)
	assertResult(t, result, StatusFiring, 1)
	state = lastTransition(repository).State
	if state.SilencedUntil != nil || state.SilencedBy != "" {
		t.Fatalf("expired silence was not cleared: %+v", state)
	}
}

func TestEngineRequiresTwoCleanEvaluationsForPendingAlert(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)}
	repository := newMemoryRepository(testRule())
	engine, _ := NewEngine(repository, clock)
	evaluate(t, engine, testSample(95))

	result := evaluate(t, engine, testSample(10))
	assertResult(t, result, StatusPending, 0)
	result = evaluate(t, engine, testSample(10))
	assertResult(t, result, StatusResolved, 0)
	if lastTransition(repository).Event == nil || lastTransition(repository).Event.Type != EventResolved {
		t.Fatal("pending alert resolution was not added to event history")
	}
}

func TestEngineSelectorsDisabledRulesAndDuplicateSamples(t *testing.T) {
	rule := testRule()
	rule.NodeSelector = "node-a"
	disabled := testRule()
	disabled.ID = "disabled"
	disabled.Enabled = false
	repository := newMemoryRepository(disabled, rule)
	clock := &fakeClock{now: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)}
	engine, _ := NewEngine(repository, clock)
	older := testSample(99)
	older.ObservedAt = clock.Now()
	newer := testSample(20)
	newer.ObservedAt = clock.Now().Add(time.Second)
	unmatched := testSample(99)
	unmatched.NodeID = "node-b"

	result, err := engine.Evaluate(context.Background(), []Sample{older, newer, unmatched})
	if err != nil {
		t.Fatal(err)
	}
	if result.Evaluated != 1 || result.Resolved != 1 || len(repository.transitions) != 1 {
		t.Fatalf("selector/deduplication failed: result=%+v transitions=%d", result, len(repository.transitions))
	}
	if lastTransition(repository).State.LastValue != 20 {
		t.Fatalf("latest duplicate did not win: %+v", lastTransition(repository).State)
	}
}

func TestEngineRejectsInvalidInputAndRepositoryErrors(t *testing.T) {
	clock := &fakeClock{now: time.Now()}
	repository := newMemoryRepository(testRule())
	engine, _ := NewEngine(repository, clock)
	invalid := testSample(1)
	invalid.NodeID = ""
	if _, err := engine.Evaluate(context.Background(), []Sample{invalid}); !errors.Is(err, ErrInvalidSample) {
		t.Fatalf("expected ErrInvalidSample, got %v", err)
	}
	repository.listErr = errors.New("database down")
	if _, err := engine.Evaluate(context.Background(), nil); err == nil {
		t.Fatal("expected repository error")
	}
}

func TestDefaultsAreValidAndIndependent(t *testing.T) {
	first := DefaultRules()
	second := DefaultRules()
	if len(first) != 10 {
		t.Fatalf("expected 10 defaults, got %d", len(first))
	}
	seen := make(map[string]bool)
	for _, rule := range first {
		if err := ValidateRule(rule); err != nil {
			t.Fatalf("invalid default %s: %v", rule.ID, err)
		}
		if seen[rule.ID] {
			t.Fatalf("duplicate default id %q", rule.ID)
		}
		seen[rule.ID] = true
	}
	nodeOffline := first[0]
	if nodeOffline.ID != "default_node_offline" || nodeOffline.ResourceType != "node" ||
		nodeOffline.Metric != MetricNodeOnline || nodeOffline.Operator != OperatorLessThan ||
		nodeOffline.Threshold != 1 || nodeOffline.For != 0 || nodeOffline.Severity != SeverityCritical {
		t.Fatalf("unexpected node offline default: %+v", nodeOffline)
	}
	backup := first[len(first)-1]
	if backup.ID != "default_backup_unhealthy" || backup.ResourceType != "backup" ||
		backup.Metric != MetricBackupHealthy || backup.Operator != OperatorLessThan ||
		backup.Threshold != 1 || backup.For != 0 || backup.Severity != SeverityWarning {
		t.Fatalf("unexpected backup unhealthy default: %+v", backup)
	}
	first[0].Name = "changed"
	if second[0].Name == "changed" {
		t.Fatal("DefaultRules returned shared values")
	}
}

func TestRuleValidationAndOperators(t *testing.T) {
	rule := testRule()
	rule.NodeSelector = ""
	rule.ResourceSelector = ""
	normalized := NormalizeRule(rule)
	if normalized.NodeSelector != WildcardSelector || normalized.ResourceSelector != WildcardSelector {
		t.Fatalf("selectors were not defaulted: %+v", normalized)
	}
	if err := ValidateRule(normalized); err != nil {
		t.Fatal(err)
	}
	if !OperatorGreaterThan.Compare(2, 1) || OperatorLessThan.Compare(2, 1) || Operator("bad").Compare(1, 1) {
		t.Fatal("operator comparison failed")
	}
	if err := ValidateSilenceDuration(6 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ValidateSilenceDuration(2*time.Hour), ErrInvalidSilence) {
		t.Fatal("unexpected silence duration accepted")
	}
	for _, name := range []string{"line\nbreak", "contains\x00nul", string([]byte{0xff})} {
		invalid := normalized
		invalid.Name = name
		if !errors.Is(ValidateRule(invalid), ErrInvalidRule) {
			t.Fatalf("unsafe alert name accepted: %q", name)
		}
	}
	invalidMetric := normalized
	invalidMetric.Metric = MetricDiskUsedPercent
	if !errors.Is(ValidateRule(invalidMetric), ErrInvalidRule) {
		t.Fatal("metric/resource mismatch was accepted")
	}
	invalidResource := normalized
	invalidResource.ResourceType = "custom"
	if !errors.Is(ValidateRule(invalidResource), ErrInvalidRule) {
		t.Fatal("resource type without emitted metrics was accepted")
	}
	credentialURL := normalized
	credentialURL.RunbookURL = "https://operator:secret@runbooks.example.test/cpu"
	if !errors.Is(ValidateRule(credentialURL), ErrInvalidRule) {
		t.Fatal("runbook URL with credentials was accepted")
	}
}

func testRule() AlertRule {
	return AlertRule{
		ID: "cpu_high", Name: "CPU high", ResourceType: "host",
		NodeSelector: WildcardSelector, ResourceSelector: WildcardSelector,
		Metric: MetricCPUPercent, Operator: OperatorGreaterThan, Threshold: 90,
		For: 5 * time.Minute, Severity: SeverityWarning, Cooldown: 30 * time.Minute, Enabled: true,
	}
}

func testSample(value float64) Sample {
	return Sample{NodeID: "node-a", ResourceType: "host", ResourceID: "node-a", Metric: MetricCPUPercent, Value: value}
}

func evaluate(t *testing.T, engine *Engine, sample Sample) EvaluationResult {
	t.Helper()
	result, err := engine.Evaluate(context.Background(), []Sample{sample})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertResult(t *testing.T, result EvaluationResult, status AlertStatus, deliveries int) {
	t.Helper()
	if result.Evaluated != 1 || result.Deliveries != deliveries {
		t.Fatalf("unexpected result: %+v", result)
	}
	counts := map[AlertStatus]int{StatusPending: result.Pending, StatusFiring: result.Firing, StatusResolved: result.Resolved}
	if counts[status] != 1 {
		t.Fatalf("expected status %s, result=%+v", status, result)
	}
}

func lastTransition(repository *memoryRepository) Transition {
	return repository.transitions[len(repository.transitions)-1]
}

type maintenanceRepository struct{ windows []MaintenanceWindow }

func (repository maintenanceRepository) ListMaintenanceWindows(context.Context) ([]MaintenanceWindow, error) {
	return repository.windows, nil
}

func TestMaintenanceWindowSuppressesDeliveryWithoutHidingAlertState(t *testing.T) {
	start := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	rule := testRule()
	rule.For = 0
	repository := newMemoryRepository(rule)
	window := MaintenanceWindow{ID: "weekly", Name: "Weekly patching", NodeSelector: "node-a", ResourceType: "host",
		ResourceSelector: "node-a", Weekdays: []time.Weekday{start.Weekday()}, StartMinute: 12 * 60, Duration: time.Hour, Timezone: "UTC", Enabled: true}
	engine, err := NewEngineWithOptions(repository, clock, EngineOptions{DeliveryEnabled: true, Maintenance: maintenanceRepository{windows: []MaintenanceWindow{window}}})
	if err != nil {
		t.Fatal(err)
	}
	result := evaluate(t, engine, testSample(95))
	assertResult(t, result, StatusFiring, 0)
	transition := lastTransition(repository)
	if transition.State.Status != StatusFiring || transition.Delivery != nil || transition.State.LastNotifiedAt != nil {
		t.Fatalf("maintenance must preserve firing state but suppress delivery: %+v", transition)
	}
}

func TestMaintenanceWindowRejectsUnsafeSchedule(t *testing.T) {
	window := MaintenanceWindow{ID: "bad", Name: "Bad", ResourceType: "host", Weekdays: []time.Weekday{time.Monday}, StartMinute: 0, Duration: time.Hour, Timezone: "not/a-zone", Enabled: true}
	if !errors.Is(ValidateMaintenance(window), ErrInvalidMaintenance) {
		t.Fatal("invalid timezone accepted")
	}
}
