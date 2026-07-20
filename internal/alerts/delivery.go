package alerts

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Sender interface {
	Send(context.Context, Delivery) error
}

type DeliveryStore interface {
	ClaimDueAlertDeliveries(context.Context, time.Time, int) ([]Delivery, error)
	IsAlertDeliveryClaimActive(context.Context, int64, int) (bool, error)
	MarkAlertDeliveryDelivered(context.Context, int64, int, time.Time) error
	RescheduleAlertDelivery(context.Context, int64, int, time.Time, string) error
	MarkAlertDeliveryDead(context.Context, int64, int, string) error
}

type DeliveryProcessor struct {
	store  DeliveryStore
	sender Sender
	clock  Clock
}

type DeliveryRunResult struct {
	Claimed    int
	Delivered  int
	Retried    int
	Dead       int
	Superseded int
}

func NewDeliveryProcessor(store DeliveryStore, sender Sender, clock Clock) (*DeliveryProcessor, error) {
	if store == nil || sender == nil {
		return nil, errors.New("alerts: delivery store and sender are required")
	}
	if clock == nil {
		clock = ClockFunc(time.Now)
	}
	return &DeliveryProcessor{store: store, sender: sender, clock: clock}, nil
}

// RunOnce processes at most limit due items. A send error is returned after
// the corresponding item has safely been retried or marked dead, unless a
// newer alert lifecycle transition superseded that claim.
func (p *DeliveryProcessor) RunOnce(ctx context.Context, limit int) (DeliveryRunResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	now := p.clock.Now().UTC()
	deliveries, err := p.store.ClaimDueAlertDeliveries(ctx, now, limit)
	if err != nil {
		return DeliveryRunResult{}, fmt.Errorf("alerts: claim deliveries: %w", err)
	}
	result := DeliveryRunResult{Claimed: len(deliveries)}
	var runErrors []error
	for _, delivery := range deliveries {
		active, err := p.store.IsAlertDeliveryClaimActive(ctx, delivery.ID, delivery.Attempts)
		if err != nil {
			runErrors = append(runErrors, fmt.Errorf("delivery %d validate claim: %w", delivery.ID, err))
			continue
		}
		if !active {
			result.Superseded++
			continue
		}
		sendErr := p.sender.Send(ctx, delivery)
		active, err = p.store.IsAlertDeliveryClaimActive(ctx, delivery.ID, delivery.Attempts)
		if err != nil {
			runErrors = append(runErrors, fmt.Errorf("delivery %d revalidate claim: %w", delivery.ID, err))
			continue
		}
		if !active {
			result.Superseded++
			continue
		}
		if sendErr == nil {
			if markErr := p.store.MarkAlertDeliveryDelivered(ctx, delivery.ID, delivery.Attempts, now); markErr != nil {
				runErrors = append(runErrors, fmt.Errorf("delivery %d mark delivered: %w", delivery.ID, markErr))
				continue
			}
			result.Delivered++
			continue
		} else {
			message := truncateError(sendErr.Error(), 1000)
			if delivery.Attempts >= MaxDeliveryAttempts {
				if markErr := p.store.MarkAlertDeliveryDead(ctx, delivery.ID, delivery.Attempts, message); markErr != nil {
					runErrors = append(runErrors, fmt.Errorf("delivery %d mark dead: %w", delivery.ID, markErr))
					continue
				}
				result.Dead++
			} else {
				next := now.Add(RetryDelay(delivery.Attempts))
				if retryErr := p.store.RescheduleAlertDelivery(ctx, delivery.ID, delivery.Attempts, next, message); retryErr != nil {
					runErrors = append(runErrors, fmt.Errorf("delivery %d reschedule: %w", delivery.ID, retryErr))
					continue
				}
				result.Retried++
			}
			runErrors = append(runErrors, fmt.Errorf("delivery %d: %w", delivery.ID, sendErr))
		}
	}
	return result, errors.Join(runErrors...)
}

func RetryDelay(attempt int) time.Duration {
	switch attempt {
	case 0, 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	default:
		return time.Hour
	}
}

func truncateError(message string, limit int) string {
	if len(message) <= limit {
		return message
	}
	return message[:limit]
}
