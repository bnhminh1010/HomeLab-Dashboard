package history

import (
	"context"
	"fmt"
	"time"
)

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type Maintenance struct {
	repository MaintenanceRepository
	clock      Clock
	interval   time.Duration
}

type MaintenanceOptions struct {
	Clock    Clock
	Interval time.Duration
}

func NewMaintenance(repository MaintenanceRepository, options MaintenanceOptions) (*Maintenance, error) {
	if repository == nil {
		return nil, fmt.Errorf("history maintenance requires a repository")
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	if options.Interval <= 0 {
		options.Interval = time.Minute
	}
	return &Maintenance{repository: repository, clock: options.Clock, interval: options.Interval}, nil
}

func (m *Maintenance) RunOnce(ctx context.Context) error {
	now := m.clock.Now().UTC()
	if err := m.repository.RollupHistory(ctx, now); err != nil {
		return fmt.Errorf("roll up history: %w", err)
	}
	if err := m.repository.RetainHistory(ctx, now); err != nil {
		return fmt.Errorf("retain history: %w", err)
	}
	return nil
}

func (m *Maintenance) Run(ctx context.Context) error {
	if err := m.RunOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := m.RunOnce(ctx); err != nil {
				return err
			}
		}
	}
}
