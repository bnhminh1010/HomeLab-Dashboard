package nodes

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestRemoteStreamOpenDataInputResizeAndClose(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := newMemoryRepository()
	service, _ := NewService(repository, Options{Now: func() time.Time { return now }, Random: &cyclingReader{}})
	enrollment, _ := service.CreateEnrollment(ctx, "admin")
	node, credential, _ := service.Enroll(ctx, enrollment.Token, "Compute", "compute")
	registry, _ := NewRegistry(service, RegistryOptions{Now: func() time.Time { return now }})
	sender := &memorySender{}
	_, _, _ = registry.Attach(ctx, node.ID, credential, sender)

	result := make(chan struct {
		stream *Stream
		err    error
	}, 1)
	go func() {
		stream, err := registry.OpenStream(ctx, node.ID, MessageExecOpen, ShellOpen{ContainerID: "app", Cols: 80, Rows: 24}, false)
		result <- struct {
			stream *Stream
			err    error
		}{stream, err}
	}()
	request := waitForSentMessage(t, sender, 1)
	ready, _ := NewMessage(node.ID, MessageCommandResult, 1, now, request.RequestID, CommandResult{OK: true})
	if err := registry.Accept(ctx, node.ID, ready); err != nil {
		t.Fatal(err)
	}
	opened := <-result
	if opened.err != nil {
		t.Fatal(opened.err)
	}
	data, _ := NewMessage(node.ID, MessageStreamData, 2, now, request.RequestID, StreamData{Data: "hello"})
	if err := registry.Accept(ctx, node.ID, data); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 5)
	if read, err := io.ReadFull(opened.stream, buffer); err != nil || read != 5 || string(buffer) != "hello" {
		t.Fatalf("Read() = %d, %q, %v", read, buffer, err)
	}
	if _, err := opened.stream.Write([]byte("id\n")); err != nil {
		t.Fatal(err)
	}
	if err := opened.stream.Resize(ctx, 100, 30); err != nil {
		t.Fatal(err)
	}
	exit := 0
	closed, _ := NewMessage(node.ID, MessageStreamClosed, 3, now, request.RequestID, CommandResult{OK: true, ExitCode: &exit})
	if err := registry.Accept(ctx, node.ID, closed); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.stream.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("closed stream read error = %v", err)
	}
	if code, ok := opened.stream.ExitCode(); !ok || code != 0 {
		t.Fatalf("exit code = %d, %t", code, ok)
	}
}

func waitForSentMessage(t *testing.T, sender *memorySender, count int) Message {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sender.mu.Lock()
		if len(sender.messages) >= count {
			message := sender.messages[count-1]
			sender.mu.Unlock()
			return message
		}
		sender.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d sent messages", count)
	return Message{}
}
