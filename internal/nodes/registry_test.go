package nodes

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/model"
)

func TestRegistryRejectsStaleFramesAndMarksOffline(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service, _ := NewService(repository, Options{Now: func() time.Time { return now }, Random: &cyclingReader{}})
	enrollment, _ := service.CreateEnrollment(ctx, "admin")
	node, credential, _ := service.Enroll(ctx, enrollment.Token, "Compute", "compute")
	registry, _ := NewRegistry(service, RegistryOptions{Now: func() time.Time { return now }})
	sender := &memorySender{}
	_, generation, err := registry.Attach(ctx, node.ID, credential, sender)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, _ := NewMessage(node.ID, MessageHeartbeat, 1, now, "", Heartbeat{AgentVersion: "v1", Hostname: "compute"})
	if err := registry.Accept(ctx, node.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	if err := registry.Accept(ctx, node.ID, heartbeat); !errors.Is(err, ErrSequenceStale) {
		t.Fatalf("duplicate sequence error = %v", err)
	}
	wrong := heartbeat
	wrong.NodeID = "other"
	wrong.Sequence = 2
	if err := registry.Accept(ctx, node.ID, wrong); !errors.Is(err, ErrProtocolNode) {
		t.Fatalf("node mismatch error = %v", err)
	}
	now = now.Add(31 * time.Second)
	states, err := registry.States(ctx)
	if err != nil || len(states) != 1 || states[0].Online {
		t.Fatalf("states = %#v, error = %v", states, err)
	}
	registry.Detach(node.ID, generation)
}

func TestRegistryStoresSnapshotsAndWhitelistsCommands(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service, _ := NewService(repository, Options{Now: func() time.Time { return now }, Random: &cyclingReader{}})
	enrollment, _ := service.CreateEnrollment(ctx, "admin")
	node, credential, _ := service.Enroll(ctx, enrollment.Token, "Compute", "compute")
	var received model.SnapshotEnvelope
	registry, _ := NewRegistry(service, RegistryOptions{
		Now: func() time.Time { return now },
		OnSnapshot: func(_ context.Context, nodeID string, snapshot model.SnapshotEnvelope) error {
			if nodeID != node.ID {
				t.Fatalf("nodeID = %q", nodeID)
			}
			received = snapshot
			return nil
		},
	})
	sender := &memorySender{}
	_, _, _ = registry.Attach(ctx, node.ID, credential, sender)
	snapshot := model.SnapshotEnvelope{Version: 1, Type: "metrics.snapshot", Sequence: 9, CollectedAt: now}
	message, _ := NewMessage(node.ID, MessageMetricsSnapshot, 1, now, "", MetricsPayload{Snapshot: snapshot})
	if err := registry.Accept(ctx, node.ID, message); err != nil || received.Sequence != 9 {
		t.Fatalf("Accept(snapshot) received %#v, err = %v", received, err)
	}
	if err := registry.Send(ctx, node.ID, MessageExecOpen, "request-1", ShellOpen{ContainerID: "safe-id", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Send(ctx, node.ID, "shell.command", "request-2", map[string]string{"command": "rm -rf /"}); !errors.Is(err, ErrProtocolType) {
		t.Fatalf("arbitrary command error = %v", err)
	}
	if len(sender.messages) != 1 || sender.messages[0].Type != MessageExecOpen {
		t.Fatalf("sent messages = %#v", sender.messages)
	}
}

func TestRegistryRejectsFramesFromReplacedConnection(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service, _ := NewService(repository, Options{Now: func() time.Time { return now }, Random: &cyclingReader{}})
	enrollment, _ := service.CreateEnrollment(ctx, "admin")
	node, credential, _ := service.Enroll(ctx, enrollment.Token, "Compute", "compute")
	registry, _ := NewRegistry(service, RegistryOptions{Now: func() time.Time { return now }})
	_, oldGeneration, err := registry.Attach(ctx, node.ID, credential, &memorySender{})
	if err != nil {
		t.Fatal(err)
	}
	_, newGeneration, err := registry.Attach(ctx, node.ID, credential, &memorySender{})
	if err != nil {
		t.Fatal(err)
	}
	message, _ := NewMessage(node.ID, MessageHeartbeat, 1, now, "", Heartbeat{AgentVersion: "v1"})
	if err := registry.AcceptConnection(ctx, node.ID, oldGeneration, message); !errors.Is(err, ErrConnectionReplaced) {
		t.Fatalf("old connection error = %v", err)
	}
	if err := registry.AcceptConnection(ctx, node.ID, newGeneration, message); err != nil {
		t.Fatalf("new connection frame rejected: %v", err)
	}
}

func TestRegistryKeepsLastSnapshotAcrossDisconnectAndReconnect(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service, _ := NewService(repository, Options{Now: func() time.Time { return now }, Random: &cyclingReader{}})
	enrollment, _ := service.CreateEnrollment(ctx, "admin")
	node, credential, _ := service.Enroll(ctx, enrollment.Token, "Compute", "compute")
	registry, _ := NewRegistry(service, RegistryOptions{Now: func() time.Time { return now }})

	_, firstGeneration, err := registry.Attach(ctx, node.ID, credential, &memorySender{})
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot := model.SnapshotEnvelope{
		Version: 1, Type: "metrics.snapshot", Sequence: 11, CollectedAt: now,
		Data: model.SnapshotData{System: model.SystemStats{Hostname: "compute"}},
	}
	message, _ := NewMessage(node.ID, MessageMetricsSnapshot, 1, now, "", MetricsPayload{Snapshot: firstSnapshot})
	if err := registry.AcceptConnection(ctx, node.ID, firstGeneration, message); err != nil {
		t.Fatal(err)
	}
	assertSnapshotState(t, registry, ctx, true, false, 11)

	registry.Detach(node.ID, firstGeneration)
	assertSnapshotState(t, registry, ctx, false, true, 11)

	_, secondGeneration, err := registry.Attach(ctx, node.ID, credential, &memorySender{})
	if err != nil {
		t.Fatal(err)
	}
	// The previous snapshot remains visible during reconnect, but it cannot be
	// mistaken for data produced by the new connection generation.
	assertSnapshotState(t, registry, ctx, true, true, 11)

	secondSnapshot := firstSnapshot
	secondSnapshot.Sequence = 12
	secondSnapshot.CollectedAt = now.Add(time.Second)
	message, _ = NewMessage(node.ID, MessageMetricsSnapshot, 1, now, "", MetricsPayload{Snapshot: secondSnapshot})
	if err := registry.AcceptConnection(ctx, node.ID, secondGeneration, message); err != nil {
		t.Fatal(err)
	}
	assertSnapshotState(t, registry, ctx, true, false, 12)

	if err := registry.Revoke(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	registry.mu.RLock()
	_, cached := registry.cache[node.ID]
	registry.mu.RUnlock()
	if cached {
		t.Fatal("revoked node retained its last-known snapshot cache")
	}
}

func TestRegistryRevalidatesGenerationAfterDecodeBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name        string
		messageType string
		payload     any
	}{
		{name: "heartbeat", messageType: MessageHeartbeat, payload: Heartbeat{AgentVersion: "old-agent"}},
		{name: "snapshot", messageType: MessageMetricsSnapshot, payload: MetricsPayload{Snapshot: model.SnapshotEnvelope{Version: 1, Type: "metrics.snapshot", Sequence: 9, CollectedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)}}},
		{name: "command result", messageType: MessageCommandResult, payload: CommandResult{OK: true}},
		{name: "stream data", messageType: MessageStreamData, payload: StreamData{Data: "must-not-arrive"}},
		{name: "stream closed", messageType: MessageStreamClosed, payload: CommandResult{OK: true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
			repository := newMemoryRepository()
			service, _ := NewService(repository, Options{Now: func() time.Time { return now }, Random: &cyclingReader{}})
			enrollment, _ := service.CreateEnrollment(ctx, "admin")
			node, credential, _ := service.Enroll(ctx, enrollment.Token, "Compute", "compute")
			var snapshotCallbacks atomic.Int32
			registry, _ := NewRegistry(service, RegistryOptions{
				Now: func() time.Time { return now },
				OnSnapshot: func(context.Context, string, model.SnapshotEnvelope) error {
					snapshotCallbacks.Add(1)
					return nil
				},
			})
			_, oldGeneration, err := registry.Attach(ctx, node.ID, credential, &memorySender{})
			if err != nil {
				t.Fatal(err)
			}

			requestID := "request-race"
			stream := &Stream{
				registry: registry, nodeID: node.ID, id: requestID,
				ready: make(chan error, 1), output: make(chan streamChunk, remoteOutputBuffers),
			}
			registry.mu.Lock()
			registry.streams[requestID] = stream
			registry.mu.Unlock()

			decoded := make(chan struct{})
			resume := make(chan struct{})
			registry.afterDecode = func(Message) {
				close(decoded)
				<-resume
			}
			message, err := NewMessage(node.ID, test.messageType, 1, now, requestID, test.payload)
			if err != nil {
				t.Fatal(err)
			}
			accepted := make(chan error, 1)
			go func() {
				accepted <- registry.AcceptConnection(ctx, node.ID, oldGeneration, message)
			}()
			<-decoded
			if _, _, err := registry.Attach(ctx, node.ID, credential, &memorySender{}); err != nil {
				t.Fatal(err)
			}
			close(resume)
			if err := <-accepted; !errors.Is(err, ErrConnectionReplaced) {
				t.Fatalf("replacement-during-decode error = %v", err)
			}

			if snapshotCallbacks.Load() != 0 {
				t.Fatal("replaced connection published a snapshot callback")
			}
			if len(stream.ready) != 0 || len(stream.output) != 0 || stream.isClosed() {
				t.Fatal("replaced connection mutated a command or stream callback")
			}
			states, err := registry.States(ctx)
			if err != nil || len(states) != 1 {
				t.Fatalf("States() = %#v, %v", states, err)
			}
			if states[0].LastSequence != 0 || states[0].AgentVersion != "" || states[0].Snapshot != nil {
				t.Fatalf("replaced frame leaked into state: %#v", states[0])
			}
		})
	}
}

func TestRegistryReplacementClosesStreamsFromDisplacedGeneration(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service, _ := NewService(repository, Options{Now: func() time.Time { return now }, Random: &cyclingReader{}})
	enrollment, _ := service.CreateEnrollment(ctx, "admin")
	node, credential, _ := service.Enroll(ctx, enrollment.Token, "Compute", "compute")
	registry, _ := NewRegistry(service, RegistryOptions{Now: func() time.Time { return now }})
	_, oldGeneration, err := registry.Attach(ctx, node.ID, credential, &memorySender{})
	if err != nil {
		t.Fatal(err)
	}
	stream := &Stream{
		registry: registry, nodeID: node.ID, generation: oldGeneration, id: "old-stream",
		ready: make(chan error, 1), output: make(chan streamChunk, remoteOutputBuffers),
	}
	registry.mu.Lock()
	registry.streams[stream.id] = stream
	registry.mu.Unlock()

	if _, _, err := registry.Attach(ctx, node.ID, credential, &memorySender{}); err != nil {
		t.Fatal(err)
	}
	if !stream.isClosed() {
		t.Fatal("stream owned by displaced connection remained open")
	}
	buffer := make([]byte, 1)
	if _, err := stream.Read(buffer); !errors.Is(err, ErrConnectionReplaced) {
		t.Fatalf("stream close error = %v, want ErrConnectionReplaced", err)
	}
}

func TestRegistryRejectsSnapshotWithUntrustedInnerTimestamp(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service, _ := NewService(repository, Options{Now: func() time.Time { return now }, Random: &cyclingReader{}})
	enrollment, _ := service.CreateEnrollment(ctx, "admin")
	node, credential, _ := service.Enroll(ctx, enrollment.Token, "Compute", "compute")
	registry, _ := NewRegistry(service, RegistryOptions{Now: func() time.Time { return now }})
	_, generation, err := registry.Attach(ctx, node.ID, credential, &memorySender{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := model.SnapshotEnvelope{
		Version: 1, Type: "metrics.snapshot", Sequence: 1, CollectedAt: now.Add(24 * time.Hour),
	}
	message, _ := NewMessage(node.ID, MessageMetricsSnapshot, 1, now, "", MetricsPayload{Snapshot: snapshot})
	if err := registry.AcceptConnection(ctx, node.ID, generation, message); !errors.Is(err, ErrTimestampStale) {
		t.Fatalf("future snapshot timestamp error = %v", err)
	}
}

func assertSnapshotState(t *testing.T, registry *Registry, ctx context.Context, online, stale bool, sequence uint64) {
	t.Helper()
	states, err := registry.States(ctx)
	if err != nil || len(states) != 1 {
		t.Fatalf("States() = %#v, %v", states, err)
	}
	state := states[0]
	if state.Online != online || state.Stale != stale || state.Snapshot == nil || state.Snapshot.Sequence != sequence {
		t.Fatalf("snapshot state = %#v, want online=%t stale=%t snapshot seq=%d", state, online, stale, sequence)
	}
}

type cyclingReader struct {
	mu    sync.Mutex
	value byte
}

func (reader *cyclingReader) Read(target []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	for index := range target {
		reader.value++
		target[index] = reader.value
	}
	return len(target), nil
}

type memorySender struct {
	mu       sync.Mutex
	messages []Message
	closed   bool
}

func (sender *memorySender) Send(_ context.Context, message Message) error {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	sender.messages = append(sender.messages, message)
	return nil
}

func (sender *memorySender) Close() error {
	sender.mu.Lock()
	sender.closed = true
	sender.mu.Unlock()
	return nil
}
