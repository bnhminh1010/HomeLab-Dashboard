package nodeagent

import (
	"errors"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/nodes"
)

func TestMessageValidatorRejectsReplayStaleAndNonCommandMessages(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	validator, err := NewMessageValidator("node_1", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	valid, _ := nodes.NewMessage("node_1", nodes.MessageLogsOpen, 1, now, "request-1", nodes.LogsOpen{ContainerID: "app"})
	if err := validator.Validate(valid); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	if err := validator.Validate(valid); !errors.Is(err, ErrInboundSequence) {
		t.Fatalf("replayed sequence error = %v", err)
	}
	wrongNode := valid
	wrongNode.Sequence = 2
	wrongNode.NodeID = "node_2"
	if err := validator.Validate(wrongNode); !errors.Is(err, ErrInboundNode) {
		t.Fatalf("node mismatch error = %v", err)
	}
	stale := valid
	stale.Sequence = 2
	stale.Timestamp = now.Add(-3 * time.Minute)
	if err := validator.Validate(stale); !errors.Is(err, ErrInboundTimestamp) {
		t.Fatalf("stale timestamp error = %v", err)
	}
	nonCommand, _ := nodes.NewMessage("node_1", nodes.MessageHeartbeat, 2, now, "request-2", nodes.Heartbeat{})
	if err := validator.Validate(nonCommand); !errors.Is(err, ErrCommandType) {
		t.Fatalf("non-command error = %v", err)
	}
	missingRequest := valid
	missingRequest.Sequence = 2
	missingRequest.RequestID = ""
	if err := validator.Validate(missingRequest); err == nil {
		t.Fatal("validator accepted a command without request ID")
	}
}

func TestReconnectDelayIsExponentialAndCapped(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for attempt, expected := range want {
		if actual := reconnectDelay(attempt); actual != expected {
			t.Fatalf("reconnectDelay(%d) = %s, want %s", attempt, actual, expected)
		}
	}
}

func TestConnectionSinkHasBoundedBackpressureAndMonotonicSequence(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	sink := newConnectionSink(t.Context(), "node_1", func() time.Time { return now })
	for index := 0; index < outboundQueueSize; index++ {
		if err := sink.Send(nodes.MessageHeartbeat, "", nodes.Heartbeat{}); err != nil {
			t.Fatalf("enqueue %d: %v", index, err)
		}
	}
	if err := sink.Send(nodes.MessageHeartbeat, "", nodes.Heartbeat{}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("full queue error = %v", err)
	}
	for expected := uint64(1); expected <= outboundQueueSize; expected++ {
		message := <-sink.queue
		if message.Sequence != expected {
			t.Fatalf("sequence = %d, want %d", message.Sequence, expected)
		}
	}
	sink.Close()
	if err := sink.Send(nodes.MessageHeartbeat, "", nodes.Heartbeat{}); err == nil {
		t.Fatal("closed sink accepted a message")
	}
}
