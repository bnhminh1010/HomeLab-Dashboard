package httpapi

import (
	"context"
	"io"
	"sync"
)

// websocketTracker owns upgraded connections because net/http no longer does
// so after a handler hijacks them. Its zero value is ready for use.
type websocketTracker struct {
	mu          sync.Mutex
	connections map[io.Closer]struct{}
	closing     bool
	drained     chan struct{}
	drainOnce   sync.Once
}

func (tracker *websocketTracker) track(connection io.Closer) (func(), bool) {
	if connection == nil {
		return func() {}, false
	}
	tracker.mu.Lock()
	if tracker.closing {
		tracker.mu.Unlock()
		_ = connection.Close()
		return func() {}, false
	}
	if tracker.connections == nil {
		tracker.connections = make(map[io.Closer]struct{})
	}
	tracker.connections[connection] = struct{}{}
	tracker.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			tracker.mu.Lock()
			delete(tracker.connections, connection)
			if tracker.closing && len(tracker.connections) == 0 {
				tracker.drainOnce.Do(func() { close(tracker.drained) })
			}
			tracker.mu.Unlock()
		})
	}, true
}

func (tracker *websocketTracker) closeAndWait(ctx context.Context) error {
	tracker.mu.Lock()
	if !tracker.closing {
		tracker.closing = true
		tracker.drained = make(chan struct{})
		if len(tracker.connections) == 0 {
			tracker.drainOnce.Do(func() { close(tracker.drained) })
		}
	}
	drained := tracker.drained
	connections := make([]io.Closer, 0, len(tracker.connections))
	for connection := range tracker.connections {
		connections = append(connections, connection)
	}
	tracker.mu.Unlock()

	for _, connection := range connections {
		_ = connection.Close()
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
