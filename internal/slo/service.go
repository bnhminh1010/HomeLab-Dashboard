package slo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/history"
)

// Service coordinates persisted policies with the availability history.
type Service struct {
	policies PolicyRepository
	history  HistoryReader
	clock    Clock
}

func NewService(policies PolicyRepository, historyReader HistoryReader, options Options) (*Service, error) {
	if policies == nil {
		return nil, fmt.Errorf("slo: policy repository is required")
	}
	if historyReader == nil {
		return nil, fmt.Errorf("slo: history reader is required")
	}
	clock := options.Clock
	if clock == nil {
		clock = realClock{}
	}
	return &Service{policies: policies, history: historyReader, clock: clock}, nil
}

// UpdatePolicy saves a policy after the caller verifies that serviceID belongs
// to a configured service. It returns the canonical, persisted policy.
func (s *Service) UpdatePolicy(ctx context.Context, serviceID string, input Input) (Policy, error) {
	policy := Policy{
		ServiceID:     strings.TrimSpace(serviceID),
		TargetPercent: input.TargetPercent,
		WindowDays:    input.WindowDays,
		Configured:    true,
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return s.policies.UpsertSLOPolicy(ctx, policy)
}

// List calculates a current report for every configured service. An optional
// window override supports the 7/30/90-day history control without changing
// the persisted policy target or default window.
func (s *Service) List(ctx context.Context, nodeID string, services []ServiceRef, windowDays int) ([]Report, error) {
	if windowDays != 0 && !validWindowDays(windowDays) {
		return nil, ErrInvalidWindow
	}
	policies, err := s.policies.ListSLOPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list SLO policies: %w", err)
	}
	byService := make(map[string]Policy, len(policies))
	for _, policy := range policies {
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("invalid persisted SLO policy %q: %w", policy.ServiceID, err)
		}
		policy.Configured = true
		byService[policy.ServiceID] = policy
	}

	// History is stored in one-hour buckets. Use the most recent completed
	// bucket for both bounds so a report never counts the part of the first
	// bucket that predates its advertised 7/30/90-day window.
	to := s.clock.Now().UTC().Truncate(time.Hour)
	reports := make([]Report, 0, len(services))
	seen := make(map[string]struct{}, len(services))
	for _, service := range services {
		service.ID = strings.TrimSpace(service.ID)
		if service.ID == "" {
			continue
		}
		if _, duplicate := seen[service.ID]; duplicate {
			continue
		}
		seen[service.ID] = struct{}{}
		policy, exists := byService[service.ID]
		if !exists {
			policy = defaultPolicy(service.ID)
		}
		effectiveWindow := policy.WindowDays
		if windowDays != 0 {
			effectiveWindow = windowDays
		}
		from := to.AddDate(0, 0, -effectiveWindow)
		points, err := s.history.QueryServiceUptime(ctx, nodeID, service.ID, from, to)
		if err != nil {
			return nil, fmt.Errorf("query SLO history for service %q: %w", service.ID, err)
		}
		report := Calculate(service.ID, service.Name, nodeID, policy, from, to, points)
		reports = append(reports, report)
	}
	sort.SliceStable(reports, func(left, right int) bool {
		return strings.ToLower(reports[left].Name) < strings.ToLower(reports[right].Name)
	})
	return reports, nil
}

// Calculate is pure so alert engines and unit tests can apply the exact same
// semantics. Degraded time consumes the budget; unknown time does not.
func Calculate(
	serviceID, name, nodeID string,
	policy Policy,
	from, to time.Time,
	points []history.ServiceUptimePoint,
) Report {
	if policy.ServiceID == "" {
		policy.ServiceID = serviceID
	}
	report := Report{
		ServiceID: serviceID,
		Name:      name,
		NodeID:    nodeID,
		Policy:    policy,
		From:      from.UTC(),
		To:        to.UTC(),
	}
	// Calculate is exported for alerting and backfill callers. Keep invalid
	// direct input from producing NaN/Inf values that cannot be JSON encoded.
	if err := policy.Validate(); err != nil {
		return report
	}
	for _, point := range points {
		report.UpSeconds += nonNegative(point.UpSeconds)
		report.DownSeconds += nonNegative(point.DownSeconds)
		report.DegradedSeconds += nonNegative(point.DegradedSeconds)
		report.UnknownSeconds += nonNegative(point.UnknownSeconds)
	}
	report.ObservedSeconds = report.UpSeconds + report.DownSeconds + report.DegradedSeconds
	if report.ObservedSeconds == 0 {
		return report
	}

	report.Known = true
	availability := 100 * float64(report.UpSeconds) / float64(report.ObservedSeconds)
	report.AvailabilityPercent = float64Pointer(availability)

	allowedFailureRatio := 1 - (policy.TargetPercent / 100)
	budgetTotal := float64(report.ObservedSeconds) * allowedFailureRatio
	badSeconds := float64(report.DownSeconds + report.DegradedSeconds)
	budgetRemaining := budgetTotal - badSeconds
	budgetRemainingPercent := 100 * budgetRemaining / budgetTotal
	burnRate := (badSeconds / float64(report.ObservedSeconds)) / allowedFailureRatio

	report.ErrorBudgetTotalSeconds = float64Pointer(budgetTotal)
	report.ErrorBudgetRemainingSeconds = float64Pointer(budgetRemaining)
	report.ErrorBudgetRemainingPercent = float64Pointer(budgetRemainingPercent)
	report.BurnRate = float64Pointer(burnRate)
	report.ErrorBudgetExhausted = budgetRemaining <= 0
	report.AtRisk = budgetRemainingPercent <= AtRiskBudgetPercent
	return report
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func float64Pointer(value float64) *float64 { return &value }
