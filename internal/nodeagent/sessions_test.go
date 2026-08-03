package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
	"github.com/bnhminh1010/homelab-dashboard/internal/podman"
)

func TestSessionManagerUsesFixedShellsAndRejectsArbitraryPayload(t *testing.T) {
	backend := &fakeRuntime{execStream: newBlockingStream(), logsStream: newBlockingStream()}
	host := &fakeHostOpener{stream: newBlockingStream()}
	sink := &recordingSink{}
	manager, err := NewSessionManager(backend, host, sink, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.CloseAll()

	execMessage := commandMessage(t, nodes.MessageExecOpen, "exec-1", nodes.ShellOpen{ContainerID: "app", Cols: 100, Rows: 30})
	if err := manager.Handle(t.Context(), execMessage); err != nil {
		t.Fatal(err)
	}
	if backend.shell != podman.ShellSH || backend.containerID != "app" {
		t.Fatalf("exec request = container %q shell %q", backend.containerID, backend.shell)
	}
	if result := sink.lastResult("exec-1"); !result.OK || result.Shell != "/bin/sh" {
		t.Fatalf("exec ready metadata = %#v", result)
	}

	malicious := commandMessage(t, nodes.MessageExecOpen, "exec-2", nodes.ShellOpen{ContainerID: "app"})
	malicious.Payload = json.RawMessage(`{"containerId":"app","command":["id"]}`)
	if err := manager.Handle(t.Context(), malicious); err == nil {
		t.Fatal("arbitrary command field was accepted")
	}
	if backend.createCalls != 1 {
		t.Fatalf("backend create calls = %d, want 1", backend.createCalls)
	}

	hostWithContainer := commandMessage(t, nodes.MessageHostOpen, "host-bad", nodes.ShellOpen{ContainerID: "app"})
	if err := manager.Handle(t.Context(), hostWithContainer); err == nil {
		t.Fatal("host shell accepted a container ID")
	}
	hostMessage := commandMessage(t, nodes.MessageHostOpen, "host-1", nodes.ShellOpen{Cols: 90, Rows: 28})
	if err := manager.Handle(t.Context(), hostMessage); err != nil {
		t.Fatal(err)
	}
	if host.opens != 1 || host.cols != 90 || host.rows != 28 {
		t.Fatalf("host opener calls=%d size=%dx%d", host.opens, host.cols, host.rows)
	}
	if result := sink.lastResult("host-1"); !result.OK || result.Shell != "/bin/bash" || result.Hostname == "" {
		t.Fatalf("host ready metadata = %#v", result)
	}
}

func TestSessionManagerValidatesLogsAndEnforcesSessionLimit(t *testing.T) {
	backend := &fakeRuntime{logsStream: newBlockingStream(), execStream: newBlockingStream()}
	host := &fakeHostOpener{stream: newBlockingStream()}
	sink := &recordingSink{}
	manager, err := NewSessionManager(backend, host, sink, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.CloseAll()

	invalid := commandMessage(t, nodes.MessageLogsOpen, "logs-invalid", nodes.LogsOpen{ContainerID: "app", Since: "25h"})
	if err := manager.Handle(t.Context(), invalid); err == nil {
		t.Fatal("logs since beyond 24h was accepted")
	}
	valid := commandMessage(t, nodes.MessageLogsOpen, "logs-1", nodes.LogsOpen{
		ContainerID: "app", Tail: 300, Since: "6h", Follow: true, Timestamps: true,
	})
	if err := manager.Handle(t.Context(), valid); err != nil {
		t.Fatal(err)
	}
	if backend.logCalls != 1 || backend.logOptions.Tail != 300 || backend.logOptions.Since != 6*time.Hour || !backend.logOptions.Follow || !backend.logOptions.Timestamps {
		t.Fatalf("log options = %#v (calls %d)", backend.logOptions, backend.logCalls)
	}
	second := commandMessage(t, nodes.MessageExecOpen, "exec-1", nodes.ShellOpen{ContainerID: "app"})
	if err := manager.Handle(t.Context(), second); err == nil {
		t.Fatal("session limit was not enforced")
	}
	input := commandMessage(t, nodes.MessageStreamInput, "logs-1", nodes.StreamInput{Data: "whoami\n"})
	if err := manager.Handle(t.Context(), input); err == nil {
		t.Fatal("read-only log stream accepted input")
	}
	cancel := commandMessage(t, nodes.MessageLogsCancel, "logs-1", nil)
	if err := manager.Handle(t.Context(), cancel); err != nil {
		t.Fatal(err)
	}
}

func TestSessionManagerMapsProtectedContainerAndClosesOnBackpressure(t *testing.T) {
	backend := &fakeRuntime{createErr: podman.ErrProtectedContainer, logsStream: io.NopCloser(bytes.NewBufferString("line\n")), execStream: newBlockingStream()}
	sink := &recordingSink{failData: true}
	manager, err := NewSessionManager(backend, &fakeHostOpener{stream: newBlockingStream()}, sink, 2)
	if err != nil {
		t.Fatal(err)
	}
	protected := commandMessage(t, nodes.MessageExecOpen, "exec-protected", nodes.ShellOpen{ContainerID: "infra"})
	if err := manager.Handle(t.Context(), protected); !errors.Is(err, podman.ErrProtectedContainer) {
		t.Fatalf("protected exec error = %v", err)
	}
	if result := sink.lastResult("exec-protected"); result.Code != "container_protected" || result.OK {
		t.Fatalf("protected result = %#v", result)
	}

	logs := commandMessage(t, nodes.MessageLogsOpen, "logs-1", nodes.LogsOpen{ContainerID: "app"})
	if err := manager.Handle(t.Context(), logs); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if result := sink.lastClosed("logs-1"); result.Code == "backpressure" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stream did not close after outbound backpressure")
}

func TestSessionManagerRunsOnlyTypedContainerLifecycleActions(t *testing.T) {
	backend := &fakeRuntime{logsStream: newBlockingStream(), execStream: newBlockingStream()}
	sink := &recordingSink{}
	manager, err := NewSessionManager(backend, &fakeHostOpener{stream: newBlockingStream()}, sink, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.CloseAll()

	restart := commandMessage(t, nodes.MessageContainerRestart, "restart-1", nodes.ContainerAction{ContainerID: "app"})
	if err := manager.Handle(t.Context(), restart); err != nil {
		t.Fatal(err)
	}
	if backend.restartCalls != 1 || backend.containerID != "app" || !sink.lastResult("restart-1").OK {
		t.Fatalf("restart request = calls %d container %q result %#v", backend.restartCalls, backend.containerID, sink.lastResult("restart-1"))
	}

	backend.stopErr = podman.ErrProtectedContainer
	stop := commandMessage(t, nodes.MessageContainerStop, "stop-1", nodes.ContainerAction{ContainerID: "infra"})
	if err := manager.Handle(t.Context(), stop); !errors.Is(err, podman.ErrProtectedContainer) {
		t.Fatalf("stop error = %v", err)
	}
	if result := sink.lastResult("stop-1"); result.OK || result.Code != "container_protected" {
		t.Fatalf("stop result = %#v", result)
	}
}

func TestSplitIncompleteUTF8PreservesTrailingRune(t *testing.T) {
	contents := []byte{'o', 'k', ' ', 0xe2, 0x82}
	complete, pending := splitIncompleteUTF8(contents)
	if string(complete) != "ok " || !bytes.Equal(pending, []byte{0xe2, 0x82}) {
		t.Fatalf("complete=%q pending=%x", complete, pending)
	}
	complete, pending = splitIncompleteUTF8(append(pending, 0xac))
	if string(complete) != "€" || len(pending) != 0 {
		t.Fatalf("completed rune=%q pending=%x", complete, pending)
	}
}

func TestWriteStringAllHandlesShortWrites(t *testing.T) {
	writer := &shortWriter{maximum: 2}
	if err := writeStringAll(writer, "abcdef"); err != nil {
		t.Fatal(err)
	}
	if got := writer.buffer.String(); got != "abcdef" {
		t.Fatalf("written data = %q", got)
	}
	if err := writeStringAll(zeroWriter{}, "x"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress error = %v", err)
	}
}

type shortWriter struct {
	buffer  bytes.Buffer
	maximum int
}

func (writer *shortWriter) Write(data []byte) (int, error) {
	if len(data) > writer.maximum {
		data = data[:writer.maximum]
	}
	return writer.buffer.Write(data)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func commandMessage(t *testing.T, messageType, requestID string, payload any) nodes.Message {
	t.Helper()
	message, err := nodes.NewMessage("node_1", messageType, 1, time.Now(), requestID, payload)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

type fakeRuntime struct {
	mu           sync.Mutex
	logsStream   io.ReadCloser
	execStream   io.ReadWriteCloser
	createErr    error
	containerID  string
	shell        podman.Shell
	createCalls  int
	logCalls     int
	logOptions   podman.LogsOptions
	removedExecs int
	restartCalls int
	stopCalls    int
	restartErr   error
	stopErr      error
}

func (runtime *fakeRuntime) Restart(_ context.Context, containerID string) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.containerID = containerID
	runtime.restartCalls++
	return runtime.restartErr
}

func (runtime *fakeRuntime) Stop(_ context.Context, containerID string) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.containerID = containerID
	runtime.stopCalls++
	return runtime.stopErr
}

func (runtime *fakeRuntime) Logs(_ context.Context, containerID string, options podman.LogsOptions) (io.ReadCloser, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.containerID = containerID
	runtime.logCalls++
	runtime.logOptions = options
	return runtime.logsStream, nil
}

func (runtime *fakeRuntime) CreateShellExec(_ context.Context, containerID string, shell podman.Shell, _ podman.TerminalSize) (string, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.containerID = containerID
	runtime.shell = shell
	runtime.createCalls++
	return "exec-1", runtime.createErr
}

func (runtime *fakeRuntime) StartExec(context.Context, string, podman.TerminalSize) (io.ReadWriteCloser, error) {
	return runtime.execStream, nil
}

func (*fakeRuntime) ResizeExec(context.Context, string, podman.TerminalSize) error { return nil }
func (runtime *fakeRuntime) RemoveExec(context.Context, string, bool) error {
	runtime.mu.Lock()
	runtime.removedExecs++
	runtime.mu.Unlock()
	return nil
}

type blockingStream struct {
	mu     sync.Mutex
	closed chan struct{}
	once   sync.Once
	writes bytes.Buffer
}

func newBlockingStream() *blockingStream { return &blockingStream{closed: make(chan struct{})} }
func (stream *blockingStream) Read([]byte) (int, error) {
	<-stream.closed
	return 0, io.EOF
}
func (stream *blockingStream) Write(contents []byte) (int, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.writes.Write(contents)
}
func (stream *blockingStream) Close() error {
	stream.once.Do(func() { close(stream.closed) })
	return nil
}
func (*blockingStream) Resize(uint16, uint16) error { return nil }

type fakeHostOpener struct {
	stream *blockingStream
	opens  int
	cols   uint16
	rows   uint16
}

func (opener *fakeHostOpener) Open(_ context.Context, cols, rows uint16) (HostStream, error) {
	opener.opens++
	opener.cols, opener.rows = cols, rows
	return opener.stream, nil
}

type recordedMessage struct {
	messageType string
	requestID   string
	payload     any
}

type recordingSink struct {
	mu       sync.Mutex
	messages []recordedMessage
	failData bool
}

func (sink *recordingSink) Send(messageType, requestID string, payload any) error {
	if sink.failData && messageType == nodes.MessageStreamData {
		return ErrBackpressure
	}
	sink.mu.Lock()
	sink.messages = append(sink.messages, recordedMessage{messageType: messageType, requestID: requestID, payload: payload})
	sink.mu.Unlock()
	return nil
}

func (sink *recordingSink) lastResult(requestID string) nodes.CommandResult {
	return sink.find(requestID, nodes.MessageCommandResult)
}

func (sink *recordingSink) lastClosed(requestID string) nodes.CommandResult {
	return sink.find(requestID, nodes.MessageStreamClosed)
}

func (sink *recordingSink) find(requestID, messageType string) nodes.CommandResult {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for index := len(sink.messages) - 1; index >= 0; index-- {
		message := sink.messages[index]
		if message.requestID == requestID && message.messageType == messageType {
			if result, ok := message.payload.(nodes.CommandResult); ok {
				return result
			}
		}
	}
	return nodes.CommandResult{}
}
