package store

import (
	"context"
	"fmt"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/slo"
)

// ListSLOPolicies implements slo.PolicyRepository. Services with no row use
// slo's default policy at read time rather than being eagerly materialized.
func (s *Store) ListSLOPolicies(ctx context.Context) ([]slo.Policy, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT service_id, target_percent, window_days, updated_at
		FROM service_slo_policies
		ORDER BY service_id`)
	if err != nil {
		return nil, fmt.Errorf("list SLO policies: %w", err)
	}
	defer rows.Close()

	policies := make([]slo.Policy, 0)
	for rows.Next() {
		policy, err := scanSLOPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SLO policies: %w", err)
	}
	return policies, nil
}

// UpsertSLOPolicy persists an explicitly configured policy. The service
// foreign key prevents dangling SLO settings after a service is deleted.
func (s *Store) UpsertSLOPolicy(ctx context.Context, policy slo.Policy) (slo.Policy, error) {
	if err := policy.Validate(); err != nil {
		return slo.Policy{}, err
	}
	updatedAt := s.now().UTC()
	policy.UpdatedAt = &updatedAt
	policy.Configured = true
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO service_slo_policies(service_id, target_percent, window_days, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(service_id) DO UPDATE SET
			target_percent = excluded.target_percent,
			window_days = excluded.window_days,
			updated_at = excluded.updated_at`,
		policy.ServiceID, policy.TargetPercent, policy.WindowDays, updatedAt.Unix())
	if err != nil {
		return slo.Policy{}, fmt.Errorf("upsert SLO policy: %w", err)
	}
	return policy, nil
}

type sloPolicyScanner interface {
	Scan(...any) error
}

func scanSLOPolicy(scanner sloPolicyScanner) (slo.Policy, error) {
	var policy slo.Policy
	var updatedAt int64
	if err := scanner.Scan(&policy.ServiceID, &policy.TargetPercent, &policy.WindowDays, &updatedAt); err != nil {
		return slo.Policy{}, fmt.Errorf("scan SLO policy: %w", err)
	}
	at := time.Unix(updatedAt, 0).UTC()
	policy.UpdatedAt = &at
	policy.Configured = true
	return policy, nil
}
