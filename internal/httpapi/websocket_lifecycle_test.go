package httpapi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWebsocketTrackerCloseIsBoundedAndRejectsNewConnections(t *testing.T) {
	tracker := &websocketTracker{}
	first := newTrackedCloser()
	untrack, ok := tracker.track(first)
	if !ok {
		t.Fatal("initial connection was rejected")
	}

	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := tracker.closeAndWait(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("closeAndWait() error = %v, want deadline exceeded", err)
	}
	select {
	case <-first.closed:
	default:
		t.Fatal("tracked connection was not closed")
	}

	second := newTrackedCloser()
	if _, ok := tracker.track(second); ok {
		t.Fatal("connection was accepted after shutdown began")
	}
	select {
	case <-second.closed:
	default:
		t.Fatal("connection rejected during shutdown was not closed")
	}

	untrack()
	if err := tracker.closeAndWait(context.Background()); err != nil {
		t.Fatalf("closeAndWait() after handler exit: %v", err)
	}
}

type trackedCloser struct {
	once   sync.Once
	closed chan struct{}
}

func newTrackedCloser() *trackedCloser {
	return &trackedCloser{closed: make(chan struct{})}
}

func (closer *trackedCloser) Close() error {
	closer.once.Do(func() { close(closer.closed) })
	return nil
}
