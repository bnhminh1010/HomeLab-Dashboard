package alerts

import (
	"context"
	"errors"
)

// MultiSender fans one alert delivery out to every configured provider. A
// failure is returned after all providers have been attempted, so one broken
// integration cannot prevent another channel from receiving the alert.
type MultiSender struct {
	senders []Sender
}

func NewMultiSender(senders ...Sender) (Sender, error) {
	filtered := make([]Sender, 0, len(senders))
	for _, sender := range senders {
		if sender != nil {
			filtered = append(filtered, sender)
		}
	}
	if len(filtered) == 0 {
		return nil, errors.New("alerts: at least one notification sender is required")
	}
	if len(filtered) == 1 {
		return filtered[0], nil
	}
	return &MultiSender{senders: filtered}, nil
}

func (s *MultiSender) Send(ctx context.Context, delivery Delivery) error {
	var sendErrors []error
	for _, sender := range s.senders {
		if err := sender.Send(ctx, delivery); err != nil {
			sendErrors = append(sendErrors, err)
		}
	}
	return errors.Join(sendErrors...)
}
