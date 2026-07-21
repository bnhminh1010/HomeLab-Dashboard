// Package slo calculates service-level objectives from the dashboard's
// persisted availability observations. It deliberately has no HTTP or storage
// dependency so callers can use the same rules in APIs, alerts, and tests.
package slo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/history"
)

const (
	DefaultTargetPercent = 99.5
	DefaultWindowDays    = 30
	MaxTargetPercent     = 99.999
	AtRiskBudgetPercent  = 20
)

var (
	ErrInvalidPolicy = errors.New("slo: policy is invalid")
	ErrInvalidWindow = errors.New("slo: window must be 7, 30, or 90 days")
)

// Policy is an availability target for a single configured service. A missing
// stored policy is represented with Configured=false and product defaults.
type Policy struct {
	ServiceID     string     `json:"serviceId"`
	TargetPercent float64    `json:"targetPercent"`
	WindowDays    int        `json:"windowDays"`
	Configured    bool       `json:"configured"`
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
}

// Input is the editable part of a Policy. The service ID comes from the URL
// or caller context rather than untrusted request data.
type Input struct {
	TargetPercent float64 `json:"targetPercent"`
	WindowDays    int     `json:"windowDays"`
}

// ServiceRef keeps SLO reporting independent from the service catalog model.
// This lets remote-node and imported service sources use the same calculation.
type ServiceRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Report is the calculated SLO state for one service over a fixed window.
// Unknown seconds are presented for transparency but are excluded from the
// availability denominator, as required for an observation-based SLO.
type Report struct {
	ServiceID string `json:"serviceId"`
	Name      string `json:"name,omitempty"`
	NodeID    string `json:"nodeId"`
	Policy    Policy `json:"policy"`

	From time.Time `json:"from"`
	To   time.Time `json:"to"`

	UpSeconds       int64 `json:"upSeconds"`
	DownSeconds     int64 `json:"downSeconds"`
	DegradedSeconds int64 `json:"degradedSeconds"`
	UnknownSeconds  int64 `json:"unknownSeconds"`
	ObservedSeconds int64 `json:"observedSeconds"`

	Known                       bool     `json:"known"`
	AvailabilityPercent         *float64 `json:"availabilityPercent,omitempty"`
	ErrorBudgetTotalSeconds     *float64 `json:"errorBudgetTotalSeconds,omitempty"`
	ErrorBudgetRemainingSeconds *float64 `json:"errorBudgetRemainingSeconds,omitempty"`
	ErrorBudgetRemainingPercent *float64 `json:"errorBudgetRemainingPercent,omitempty"`
	BurnRate                    *float64 `json:"burnRate,omitempty"`
	AtRisk                      bool     `json:"atRisk"`
	ErrorBudgetExhausted        bool     `json:"errorBudgetExhausted"`
}

// PolicyRepository is the small persistence boundary required by Service.
// Callers own service existence validation before saving a policy.
type PolicyRepository interface {
	ListSLOPolicies(context.Context) ([]Policy, error)
	UpsertSLOPolicy(context.Context, Policy) (Policy, error)
}

// HistoryReader is intentionally aligned with history.Reader's service
// method, making *store.Store a valid implementation without an adapter.
type HistoryReader interface {
	QueryServiceUptime(context.Context, string, string, time.Time, time.Time) ([]history.ServiceUptimePoint, error)
}

// Clock makes report windows deterministic in tests and backfills.
type Clock interface {
	Now() time.Time
}

type Options struct {
	Clock Clock
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Validate rejects values that make a meaningful error budget impossible or
// cannot be represented by the supported dashboard range selectors.
func (p Policy) Validate() error {
	if p.ServiceID == "" {
		return fmt.Errorf("%w: service id is required", ErrInvalidPolicy)
	}
	if math.IsNaN(p.TargetPercent) || math.IsInf(p.TargetPercent, 0) || p.TargetPercent < 90 || p.TargetPercent > MaxTargetPercent {
		return fmt.Errorf("%w: target percent must be between 90 and %.3f", ErrInvalidPolicy, MaxTargetPercent)
	}
	if !validWindowDays(p.WindowDays) {
		return ErrInvalidWindow
	}
	return nil
}

func validWindowDays(days int) bool {
	switch days {
	case 7, 30, 90:
		return true
	default:
		return false
	}
}

func defaultPolicy(serviceID string) Policy {
	return Policy{
		ServiceID:     serviceID,
		TargetPercent: DefaultTargetPercent,
		WindowDays:    DefaultWindowDays,
		Configured:    false,
	}
}
