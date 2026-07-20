package alerts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type Repository interface {
	ListAlertRules(context.Context) ([]AlertRule, error)
	LoadAlertState(context.Context, AlertKey) (AlertState, bool, error)
	ApplyAlertTransition(context.Context, Transition) error
}

type Engine struct {
	repository      Repository
	clock           Clock
	deliveryEnabled bool
}

type EngineOptions struct {
	DeliveryEnabled bool
}

type EvaluationResult struct {
	Evaluated  int
	Pending    int
	Firing     int
	Resolved   int
	Deliveries int
}

func NewEngine(repository Repository, clock Clock) (*Engine, error) {
	return NewEngineWithOptions(repository, clock, EngineOptions{DeliveryEnabled: true})
}

func NewEngineWithOptions(repository Repository, clock Clock, options EngineOptions) (*Engine, error) {
	if repository == nil {
		return nil, errors.New("alerts: repository is required")
	}
	if clock == nil {
		clock = ClockFunc(time.Now)
	}
	return &Engine{repository: repository, clock: clock, deliveryEnabled: options.DeliveryEnabled}, nil
}

// Evaluate compares one complete set of samples with all enabled rules. The
// caller should provide at most one sample for each node/resource/metric; if a
// duplicate exists, the sample with the latest ObservedAt wins.
func (e *Engine) Evaluate(ctx context.Context, samples []Sample) (EvaluationResult, error) {
	rules, err := e.repository.ListAlertRules(ctx)
	if err != nil {
		return EvaluationResult{}, fmt.Errorf("alerts: list rules: %w", err)
	}
	for i := range rules {
		rules[i] = NormalizeRule(rules[i])
		if err := ValidateRule(rules[i]); err != nil {
			return EvaluationResult{}, fmt.Errorf("alerts: stored rule %q: %w", rules[i].ID, err)
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })

	unique, err := normalizeSamples(samples)
	if err != nil {
		return EvaluationResult{}, err
	}
	now := e.clock.Now().UTC()
	result := EvaluationResult{}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		for _, sample := range unique {
			if !matches(rule, sample) {
				continue
			}
			transition, err := e.evaluateOne(ctx, rule, sample, now)
			if err != nil {
				return result, err
			}
			if err := e.repository.ApplyAlertTransition(ctx, transition); errors.Is(err, ErrStaleTransition) {
				continue
			} else if err != nil {
				return result, fmt.Errorf("alerts: persist %s/%s/%s: %w",
					rule.ID, sample.NodeID, sample.ResourceID, err)
			}
			result.Evaluated++
			switch transition.State.Status {
			case StatusPending:
				result.Pending++
			case StatusFiring:
				result.Firing++
			case StatusResolved:
				result.Resolved++
			}
			if transition.Delivery != nil {
				result.Deliveries++
			}
		}
	}
	return result, nil
}

func (e *Engine) evaluateOne(ctx context.Context, rule AlertRule, sample Sample, now time.Time) (Transition, error) {
	key := AlertKey{
		RuleID: rule.ID, NodeID: sample.NodeID, ResourceType: sample.ResourceType, ResourceID: sample.ResourceID,
	}
	state, found, err := e.repository.LoadAlertState(ctx, key)
	if err != nil {
		return Transition{}, fmt.Errorf("alerts: load state: %w", err)
	}
	violating := rule.Operator.Compare(sample.Value, rule.Threshold)
	if !found {
		state = AlertState{AlertKey: key, Status: StatusResolved}
	}
	if state.SilencedUntil != nil && !now.Before(*state.SilencedUntil) {
		state.SilencedUntil = nil
		state.SilencedBy = ""
	}
	state.LastEvaluatedAt = now
	state.LastValue = sample.Value
	expectedRevision := state.Revision
	var transition Transition
	if violating {
		transition = violatingTransition(rule, state, now)
	} else {
		transition = cleanTransition(rule, state, found, now)
	}
	transition.ExpectedRevision = expectedRevision
	transition.ExpectedRuleUpdatedAt = rule.UpdatedAt
	transition.State.Revision = expectedRevision + 1
	return e.withDeliveryPolicy(transition), nil
}

func (e *Engine) withDeliveryPolicy(transition Transition) Transition {
	if e.deliveryEnabled || transition.Delivery == nil {
		return transition
	}
	transition.Delivery = nil
	return transition
}

func violatingTransition(rule AlertRule, state AlertState, now time.Time) Transition {
	transition := Transition{State: state}
	switch state.Status {
	case StatusResolved:
		transition.IncidentStarted = true
		transition.State.Status = StatusPending
		transition.State.PendingSince = timePointer(now)
		transition.State.FiringSince = nil
		transition.State.ResolvedAt = nil
		transition.State.LastNotifiedAt = nil
		transition.State.CleanEvaluations = 0
		transition.State.AcknowledgedAt = nil
		transition.State.AcknowledgedBy = ""
		transition.Event = newEvent(rule, transition.State, EventPending, now, "threshold violation pending")
	case StatusPending:
		transition.State.CleanEvaluations = 0
		if transition.State.PendingSince == nil {
			transition.State.PendingSince = timePointer(now)
		}
	case StatusFiring:
		transition.State.CleanEvaluations = 0
		if shouldNotifyFiring(rule, transition.State, now) {
			transition.Event = newEvent(rule, transition.State, EventFiring, now, "threshold violation continues")
			transition.Delivery = newDelivery(rule, transition.State, DeliveryFiring, now)
			transition.State.LastNotifiedAt = timePointer(now)
		}
	}

	if transition.State.Status == StatusPending &&
		transition.State.PendingSince != nil && now.Sub(*transition.State.PendingSince) >= rule.For {
		transition.State.Status = StatusFiring
		transition.State.FiringSince = timePointer(now)
		transition.State.ResolvedAt = nil
		transition.Event = newEvent(rule, transition.State, EventFiring, now, "threshold violation firing")
		if !isSilenced(transition.State, now) {
			transition.Delivery = newDelivery(rule, transition.State, DeliveryFiring, now)
			transition.State.LastNotifiedAt = timePointer(now)
		}
	}
	return transition
}

func cleanTransition(rule AlertRule, state AlertState, found bool, now time.Time) Transition {
	transition := Transition{State: state}
	if !found || state.Status == StatusResolved {
		transition.State.Status = StatusResolved
		transition.State.CleanEvaluations = 0
		return transition
	}
	transition.State.CleanEvaluations++
	if transition.State.CleanEvaluations < 2 {
		return transition
	}

	wasFiring := state.Status == StatusFiring
	wasNotified := state.LastNotifiedAt != nil
	transition.State.Status = StatusResolved
	transition.State.PendingSince = nil
	transition.State.FiringSince = nil
	transition.State.ResolvedAt = timePointer(now)
	transition.State.CleanEvaluations = 0
	transition.State.AcknowledgedAt = nil
	transition.State.AcknowledgedBy = ""
	transition.Event = newEvent(rule, transition.State, EventResolved, now, "threshold is healthy")
	if wasFiring && wasNotified && !isSilenced(state, now) {
		transition.Delivery = newDelivery(rule, transition.State, DeliveryResolved, now)
		transition.State.LastNotifiedAt = timePointer(now)
	}
	return transition
}

func shouldNotifyFiring(rule AlertRule, state AlertState, now time.Time) bool {
	if state.AcknowledgedAt != nil || isSilenced(state, now) {
		return false
	}
	return state.LastNotifiedAt == nil || now.Sub(*state.LastNotifiedAt) >= rule.Cooldown
}

func isSilenced(state AlertState, now time.Time) bool {
	return state.SilencedUntil != nil && now.Before(*state.SilencedUntil)
}

func newEvent(rule AlertRule, state AlertState, eventType EventType, now time.Time, reason string) *AlertEvent {
	return &AlertEvent{
		AlertKey: state.AlertKey,
		Type:     eventType, Status: state.Status, Severity: rule.Severity, Value: state.LastValue,
		Message: fmt.Sprintf("%s on %s/%s: %s (value %.2f %s %.2f)",
			rule.Name, state.NodeID, state.ResourceID, reason, state.LastValue, rule.Operator, rule.Threshold),
		OccurredAt: now,
	}
}

func newDelivery(rule AlertRule, state AlertState, kind DeliveryKind, now time.Time) *Delivery {
	verb := "firing"
	if kind == DeliveryResolved {
		verb = "resolved"
	}
	return &Delivery{
		AlertKey: state.AlertKey,
		Kind:     kind, Severity: rule.Severity,
		Title:   fmt.Sprintf("[%s] %s %s", strings.ToUpper(string(rule.Severity)), rule.Name, verb),
		Message: fmt.Sprintf("%s on %s/%s is %s (value %.2f).", rule.Name, state.NodeID, state.ResourceID, verb, state.LastValue),
		Status:  DeliveryPending, NextAttemptAt: now, CreatedAt: now,
	}
}

func matches(rule AlertRule, sample Sample) bool {
	return rule.ResourceType == sample.ResourceType && rule.Metric == sample.Metric &&
		selectorMatches(rule.NodeSelector, sample.NodeID) && selectorMatches(rule.ResourceSelector, sample.ResourceID)
}

func selectorMatches(selector, value string) bool {
	return selector == WildcardSelector || selector == value
}

func normalizeSamples(samples []Sample) ([]Sample, error) {
	byKey := make(map[string]Sample, len(samples))
	for _, sample := range samples {
		if err := ValidateSample(sample); err != nil {
			return nil, fmt.Errorf("alerts: sample %s/%s/%s: %w",
				sample.NodeID, sample.ResourceID, sample.Metric, err)
		}
		key := sample.NodeID + "\x00" + sample.ResourceType + "\x00" + sample.ResourceID + "\x00" + sample.Metric
		current, exists := byKey[key]
		if !exists || sample.ObservedAt.After(current.ObservedAt) {
			byKey[key] = sample
		}
	}
	result := make([]Sample, 0, len(byKey))
	for _, sample := range byKey {
		result = append(result, sample)
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i].NodeID + "\x00" + result[i].ResourceType + "\x00" + result[i].ResourceID + "\x00" + result[i].Metric
		right := result[j].NodeID + "\x00" + result[j].ResourceType + "\x00" + result[j].ResourceID + "\x00" + result[j].Metric
		return left < right
	})
	return result, nil
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}
