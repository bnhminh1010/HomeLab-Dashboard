package alerts

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type deliveryMemoryStore struct {
	deliveries  []Delivery
	deliveredAt *time.Time
	retryAt     *time.Time
	retryError  string
	deadError   string
	claimActive bool
}

func (store *deliveryMemoryStore) ClaimDueAlertDeliveries(context.Context, time.Time, int) ([]Delivery, error) {
	return append([]Delivery(nil), store.deliveries...), nil
}

func (store *deliveryMemoryStore) IsAlertDeliveryClaimActive(context.Context, int64, int) (bool, error) {
	return store.claimActive, nil
}

func (store *deliveryMemoryStore) MarkAlertDeliveryDelivered(_ context.Context, _ int64, _ int, at time.Time) error {
	store.deliveredAt = &at
	return nil
}

func (store *deliveryMemoryStore) RescheduleAlertDelivery(_ context.Context, _ int64, _ int, at time.Time, message string) error {
	store.retryAt = &at
	store.retryError = message
	return nil
}

func (store *deliveryMemoryStore) MarkAlertDeliveryDead(_ context.Context, _ int64, _ int, message string) error {
	store.deadError = message
	return nil
}

type senderFunc func(context.Context, Delivery) error

func (sender senderFunc) Send(ctx context.Context, delivery Delivery) error {
	return sender(ctx, delivery)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestDeliveryProcessorSuccessRetryAndDeadLetter(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	delivery := Delivery{ID: 1, Attempts: 1}
	store := &deliveryMemoryStore{deliveries: []Delivery{delivery}, claimActive: true}
	processor, err := NewDeliveryProcessor(store, senderFunc(func(context.Context, Delivery) error { return nil }), clock)
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.RunOnce(context.Background(), 10)
	if err != nil || result.Delivered != 1 || store.deliveredAt == nil || !store.deliveredAt.Equal(now) {
		t.Fatalf("successful delivery: result=%+v err=%v store=%+v", result, err, store)
	}

	store = &deliveryMemoryStore{deliveries: []Delivery{delivery}, claimActive: true}
	processor, _ = NewDeliveryProcessor(store, senderFunc(func(context.Context, Delivery) error {
		return errors.New("temporary failure")
	}), clock)
	result, err = processor.RunOnce(context.Background(), 10)
	if err == nil || result.Retried != 1 || store.retryAt == nil || !store.retryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("retry: result=%+v err=%v store=%+v", result, err, store)
	}

	delivery.Attempts = MaxDeliveryAttempts
	store = &deliveryMemoryStore{deliveries: []Delivery{delivery}, claimActive: true}
	processor, _ = NewDeliveryProcessor(store, senderFunc(func(context.Context, Delivery) error {
		return errors.New("permanent failure")
	}), clock)
	result, err = processor.RunOnce(context.Background(), 10)
	if err == nil || result.Dead != 1 || store.deadError != "permanent failure" || store.retryAt != nil {
		t.Fatalf("dead letter: result=%+v err=%v store=%+v", result, err, store)
	}

	if RetryDelay(1) != time.Minute || RetryDelay(2) != 5*time.Minute ||
		RetryDelay(3) != 15*time.Minute || RetryDelay(4) != time.Hour {
		t.Fatal("unexpected retry schedule")
	}
}

func TestDeliveryProcessorSkipsSupersededClaimBeforeSend(t *testing.T) {
	delivery := Delivery{ID: 7, Attempts: 1, Status: DeliveryProcessing}
	store := &deliveryMemoryStore{deliveries: []Delivery{delivery}, claimActive: false}
	sent := false
	processor, err := NewDeliveryProcessor(store, senderFunc(func(context.Context, Delivery) error {
		sent = true
		return nil
	}), &fakeClock{now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	result, err := processor.RunOnce(context.Background(), 10)
	if err != nil || sent || result.Claimed != 1 || result.Superseded != 1 || result.Delivered != 0 {
		t.Fatalf("superseded claim: result=%+v sent=%v err=%v", result, sent, err)
	}
}

func TestDeliveryProcessorStopsLeaseSupersededDuringSend(t *testing.T) {
	delivery := Delivery{ID: 8, Attempts: 1, Status: DeliveryProcessing}
	store := &deliveryMemoryStore{deliveries: []Delivery{delivery}, claimActive: true}
	processor, err := NewDeliveryProcessor(store, senderFunc(func(context.Context, Delivery) error {
		store.claimActive = false
		return errors.New("request canceled by resolution")
	}), &fakeClock{now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	result, err := processor.RunOnce(context.Background(), 10)
	if err != nil || result.Superseded != 1 || result.Retried != 0 || result.Dead != 0 ||
		store.retryAt != nil || store.deadError != "" {
		t.Fatalf("superseded during send: result=%+v store=%+v err=%v", result, store, err)
	}
}

func TestNTFYSenderRequest(t *testing.T) {
	var gotPath, gotToken, gotTitle, gotPriority, gotBody string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotPath = request.URL.Path
		gotToken = request.Header.Get("Authorization")
		gotTitle = request.Header.Get("Title")
		gotPriority = request.Header.Get("Priority")
		body, _ := io.ReadAll(request.Body)
		gotBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    request,
		}, nil
	})}

	sender, err := NewNTFYSender(NTFYConfig{URL: "https://ntfy.example/base", Topic: "homelab", Token: "secret"}, client)
	if err != nil {
		t.Fatal(err)
	}
	delivery := Delivery{Title: "Disk full", Message: "Disk is at 96%", Severity: SeverityCritical}
	if err := sender.Send(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/base/homelab" || gotToken != "Bearer secret" || gotTitle != delivery.Title ||
		gotPriority != "urgent" || gotBody != delivery.Message {
		t.Fatalf("unexpected ntfy request: path=%q token=%q title=%q priority=%q body=%q",
			gotPath, gotToken, gotTitle, gotPriority, gotBody)
	}
}

func TestNTFYSenderValidationAndHTTPFailure(t *testing.T) {
	for _, config := range []NTFYConfig{
		{URL: "ftp://ntfy.example", Topic: "homelab"},
		{URL: "https://user:pass@ntfy.example", Topic: "homelab"},
		{URL: "https://ntfy.example?token=bad", Topic: "homelab"},
		{URL: "https://ntfy.example", Topic: "bad/topic"},
	} {
		if _, err := NewNTFYSender(config, nil); err == nil {
			t.Fatalf("accepted invalid config: %+v", config)
		}
	}

	called := false
	guardedClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected request")
	})}
	guardedSender, _ := NewNTFYSender(NTFYConfig{URL: "https://ntfy.example", Topic: "homelab"}, guardedClient)
	if err := guardedSender.Send(context.Background(), Delivery{Title: "unsafe\r\ntitle", Message: "test"}); err == nil || called {
		t.Fatalf("unsafe ntfy title: called=%v err=%v", called, err)
	}

	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 5000))),
			Request:    request,
		}, nil
	})}
	sender, _ := NewNTFYSender(NTFYConfig{URL: "https://ntfy.example", Topic: "homelab"}, client)
	err := sender.Send(context.Background(), Delivery{Title: "test", Message: "test"})
	if err == nil || !strings.Contains(err.Error(), "503") || len(err.Error()) > 4200 {
		t.Fatalf("unexpected ntfy error: len=%d err=%v", len(err.Error()), err)
	}
}
