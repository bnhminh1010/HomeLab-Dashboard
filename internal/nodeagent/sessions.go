package nodeagent

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/binhminh/HomeLab-Minh/internal/nodes"
	"github.com/binhminh/HomeLab-Minh/internal/podman"
)

const (
	defaultMaxSessions = 4
	outputChunkSize    = 16 << 10
	maxInputBytes      = 64 << 10
)

type RuntimeBackend interface {
	Logs(context.Context, string, podman.LogsOptions) (io.ReadCloser, error)
	CreateShellExec(context.Context, string, podman.Shell, podman.TerminalSize) (string, error)
	StartExec(context.Context, string, podman.TerminalSize) (io.ReadWriteCloser, error)
	ResizeExec(context.Context, string, podman.TerminalSize) error
	RemoveExec(context.Context, string, bool) error
}

type HostStream interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

type HostShellOpener interface {
	Open(context.Context, uint16, uint16) (HostStream, error)
}

type MessageSink interface {
	Send(string, string, any) error
}

type session struct {
	kind     string
	reader   io.ReadCloser
	writer   io.Writer
	resize   func(uint16, uint16) error
	cancel   context.CancelFunc
	cleanup  func()
	exitCode func() *int

	writeMu sync.Mutex
	once    sync.Once
}

func (stream *session) close() {
	stream.once.Do(func() {
		stream.cancel()
		_ = stream.reader.Close()
		if stream.cleanup != nil {
			stream.cleanup()
		}
	})
}

type SessionManager struct {
	backend RuntimeBackend
	host    HostShellOpener
	sink    MessageSink
	max     int

	mu       sync.Mutex
	sessions map[string]*session
}

func NewSessionManager(backend RuntimeBackend, host HostShellOpener, sink MessageSink, maxSessions int) (*SessionManager, error) {
	if backend == nil || host == nil || sink == nil {
		return nil, errors.New("node agent: runtime, host shell, and message sink are required")
	}
	if maxSessions == 0 {
		maxSessions = defaultMaxSessions
	}
	if maxSessions < 1 || maxSessions > 8 {
		return nil, errors.New("node agent: maximum sessions must be between 1 and 8")
	}
	return &SessionManager{backend: backend, host: host, sink: sink, max: maxSessions, sessions: make(map[string]*session)}, nil
}

func (manager *SessionManager) Handle(ctx context.Context, message nodes.Message) error {
	switch message.Type {
	case nodes.MessageLogsOpen:
		request, err := nodes.DecodePayload[nodes.LogsOpen](message)
		if err != nil {
			return manager.reject(message.RequestID, "invalid_request", err)
		}
		return manager.openLogs(ctx, message.RequestID, request)
	case nodes.MessageExecOpen:
		request, err := nodes.DecodePayload[nodes.ShellOpen](message)
		if err != nil {
			return manager.reject(message.RequestID, "invalid_request", err)
		}
		return manager.openExec(ctx, message.RequestID, request)
	case nodes.MessageHostOpen:
		request, err := nodes.DecodePayload[nodes.ShellOpen](message)
		if err != nil {
			return manager.reject(message.RequestID, "invalid_request", err)
		}
		return manager.openHost(ctx, message.RequestID, request)
	case nodes.MessageLogsCancel:
		if !manager.cancel(message.RequestID, "logs") {
			return manager.reject(message.RequestID, "session_not_found", errors.New("log stream is not active"))
		}
		return nil
	case nodes.MessageStreamCancel:
		if !manager.cancel(message.RequestID, "") {
			return manager.reject(message.RequestID, "session_not_found", errors.New("stream is not active"))
		}
		return nil
	case nodes.MessageStreamInput:
		request, err := nodes.DecodePayload[nodes.StreamInput](message)
		if err != nil {
			return manager.reject(message.RequestID, "invalid_request", err)
		}
		return manager.input(message.RequestID, request.Data)
	case nodes.MessageStreamResize:
		request, err := nodes.DecodePayload[nodes.StreamResize](message)
		if err != nil {
			return manager.reject(message.RequestID, "invalid_request", err)
		}
		return manager.resize(message.RequestID, request.Cols, request.Rows)
	default:
		return manager.reject(message.RequestID, "unsupported_command", ErrCommandType)
	}
}

func (manager *SessionManager) openLogs(ctx context.Context, requestID string, request nodes.LogsOpen) error {
	if request.ContainerID == "" || request.Tail < 0 || request.Tail > 500 {
		return manager.reject(requestID, "invalid_request", errors.New("container ID and tail from 0 to 500 are required"))
	}
	since, err := parseLogSince(request.Since)
	if err != nil {
		return manager.reject(requestID, "invalid_request", err)
	}
	streamContext, cancel, reservation, err := manager.reserve(ctx, requestID, "logs")
	if err != nil {
		return manager.reject(requestID, "session_limit", err)
	}
	reader, err := manager.backend.Logs(streamContext, request.ContainerID, podman.LogsOptions{
		Tail: uint(request.Tail), Follow: request.Follow, Since: since, Timestamps: request.Timestamps,
	})
	if err != nil {
		cancel()
		manager.releaseReservation(requestID, reservation)
		return manager.reject(requestID, runtimeErrorCode(err), err)
	}
	return manager.activate(requestID, reservation, &session{kind: "logs", reader: reader, cancel: cancel}, nodes.CommandResult{OK: true})
}

func (manager *SessionManager) openExec(ctx context.Context, requestID string, request nodes.ShellOpen) error {
	if request.ContainerID == "" {
		return manager.reject(requestID, "invalid_request", errors.New("container ID is required"))
	}
	cols, rows := normalizeSize(request.Cols, request.Rows)
	streamContext, cancel, reservation, err := manager.reserve(ctx, requestID, "exec")
	if err != nil {
		return manager.reject(requestID, "session_limit", err)
	}
	// The protocol intentionally has no command field. Remote exec is always a
	// non-privileged /bin/sh inside a container that passed protected checks.
	execID, err := manager.backend.CreateShellExec(streamContext, request.ContainerID, podman.ShellSH, podman.TerminalSize{Cols: uint(cols), Rows: uint(rows)})
	if err != nil {
		cancel()
		manager.releaseReservation(requestID, reservation)
		return manager.reject(requestID, runtimeErrorCode(err), err)
	}
	runtimeStream, err := manager.backend.StartExec(streamContext, execID, podman.TerminalSize{Cols: uint(cols), Rows: uint(rows)})
	if err != nil {
		cancel()
		manager.releaseReservation(requestID, reservation)
		manager.removeExec(execID)
		return manager.reject(requestID, runtimeErrorCode(err), err)
	}
	return manager.activate(requestID, reservation, &session{
		kind: "exec", reader: runtimeStream, writer: runtimeStream, cancel: cancel,
		resize: func(cols, rows uint16) error {
			return manager.backend.ResizeExec(streamContext, execID, podman.TerminalSize{Cols: uint(cols), Rows: uint(rows)})
		},
		cleanup: func() { manager.removeExec(execID) },
	}, nodes.CommandResult{OK: true, Shell: string(podman.ShellSH)})
}

func (manager *SessionManager) openHost(ctx context.Context, requestID string, request nodes.ShellOpen) error {
	if request.ContainerID != "" {
		return manager.reject(requestID, "invalid_request", errors.New("host shell must not include a container ID"))
	}
	cols, rows := normalizeSize(request.Cols, request.Rows)
	streamContext, cancel, reservation, err := manager.reserve(ctx, requestID, "host")
	if err != nil {
		return manager.reject(requestID, "session_limit", err)
	}
	stream, err := manager.host.Open(streamContext, cols, rows)
	if err != nil {
		cancel()
		manager.releaseReservation(requestID, reservation)
		return manager.reject(requestID, "host_shell_unavailable", err)
	}
	hostname, username := hostShellIdentity()
	active := &session{
		kind: "host", reader: stream, writer: stream, cancel: cancel,
		resize: stream.Resize,
	}
	if status, ok := stream.(interface{ ExitCode() *int }); ok {
		active.exitCode = status.ExitCode
	}
	return manager.activate(requestID, reservation, active, nodes.CommandResult{OK: true, Hostname: hostname, User: username, Shell: "/bin/bash"})
}

func (manager *SessionManager) reserve(ctx context.Context, requestID, kind string) (context.Context, context.CancelFunc, *session, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if requestID == "" || len(requestID) > 128 {
		return nil, nil, nil, errors.New("stream request ID is required")
	}
	if _, exists := manager.sessions[requestID]; exists {
		return nil, nil, nil, errors.New("stream request is already active")
	}
	if len(manager.sessions) >= manager.max {
		return nil, nil, nil, errors.New("node stream limit reached")
	}
	streamContext, cancel := context.WithCancel(ctx)
	reservation := &session{kind: kind, reader: closedReader{}, cancel: cancel}
	manager.sessions[requestID] = reservation
	return streamContext, cancel, reservation, nil
}

func (manager *SessionManager) activate(requestID string, reservation, stream *session, ready nodes.CommandResult) error {
	manager.mu.Lock()
	current, exists := manager.sessions[requestID]
	if exists && current == reservation {
		manager.sessions[requestID] = stream
	}
	manager.mu.Unlock()
	if !exists || current != reservation {
		stream.close()
		return errors.New("node agent: stream was cancelled while opening")
	}
	if err := manager.sink.Send(nodes.MessageCommandResult, requestID, ready); err != nil {
		manager.cancel(requestID, "")
		return err
	}
	go manager.pump(requestID, stream)
	return nil
}

func (manager *SessionManager) pump(requestID string, stream *session) {
	buffer := make([]byte, outputChunkSize)
	pendingUTF8 := make([]byte, 0, utf8.UTFMax)
	result := nodes.CommandResult{OK: true}
	for {
		read, err := stream.reader.Read(buffer)
		if read > 0 {
			combined := append(pendingUTF8, buffer[:read]...)
			complete, pending := splitIncompleteUTF8(combined)
			pendingUTF8 = append(pendingUTF8[:0], pending...)
			data := strings.ToValidUTF8(string(complete), "�")
			if data != "" {
				if sendErr := manager.sink.Send(nodes.MessageStreamData, requestID, nodes.StreamData{Data: data}); sendErr != nil {
					result = nodes.CommandResult{OK: false, Code: "backpressure", Message: "stream closed because the dashboard did not consume output"}
					break
				}
			}
		}
		if err != nil {
			if len(pendingUTF8) > 0 {
				data := strings.ToValidUTF8(string(pendingUTF8), "�")
				if sendErr := manager.sink.Send(nodes.MessageStreamData, requestID, nodes.StreamData{Data: data}); sendErr != nil {
					result = nodes.CommandResult{OK: false, Code: "backpressure", Message: "stream closed because the dashboard did not consume output"}
				}
			}
			if !cleanStreamEnd(err) {
				result = nodes.CommandResult{OK: false, Code: "stream_read_failed", Message: "stream closed unexpectedly"}
			}
			break
		}
	}
	if stream.exitCode != nil {
		result.ExitCode = stream.exitCode()
	}
	manager.finish(requestID, stream)
	_ = manager.sink.Send(nodes.MessageStreamClosed, requestID, result)
}

func cleanStreamEnd(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EIO)
}

func splitIncompleteUTF8(contents []byte) ([]byte, []byte) {
	if len(contents) == 0 || utf8.Valid(contents) {
		return contents, nil
	}
	lowerBound := len(contents) - utf8.UTFMax
	if lowerBound < 0 {
		lowerBound = 0
	}
	start := len(contents) - 1
	for start > lowerBound && !utf8.RuneStart(contents[start]) {
		start--
	}
	if utf8.RuneStart(contents[start]) && !utf8.FullRune(contents[start:]) {
		return contents[:start], contents[start:]
	}
	return contents, nil
}

func (manager *SessionManager) input(requestID, data string) error {
	if len(data) > maxInputBytes || !utf8.ValidString(data) {
		return manager.reject(requestID, "invalid_input", errors.New("stream input must be valid UTF-8 and no larger than 64 KiB"))
	}
	stream := manager.get(requestID)
	if stream == nil {
		return manager.reject(requestID, "session_not_found", errors.New("stream is not active"))
	}
	if stream.writer == nil {
		return manager.reject(requestID, "read_only", errors.New("log streams are read-only"))
	}
	stream.writeMu.Lock()
	defer stream.writeMu.Unlock()
	if err := writeStringAll(stream.writer, data); err != nil {
		return manager.reject(requestID, "stream_write_failed", err)
	}
	return nil
}

func writeStringAll(writer io.Writer, data string) error {
	for len(data) > 0 {
		written, err := io.WriteString(writer, data)
		if written < 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (manager *SessionManager) resize(requestID string, cols, rows uint16) error {
	if cols == 0 || rows == 0 || cols > 1000 || rows > 500 {
		return manager.reject(requestID, "invalid_size", errors.New("terminal size is outside the supported range"))
	}
	stream := manager.get(requestID)
	if stream == nil {
		return manager.reject(requestID, "session_not_found", errors.New("stream is not active"))
	}
	if stream.resize == nil {
		return manager.reject(requestID, "read_only", errors.New("log streams cannot be resized"))
	}
	if err := stream.resize(cols, rows); err != nil {
		return manager.reject(requestID, "resize_failed", err)
	}
	return nil
}

func (manager *SessionManager) cancel(requestID, kind string) bool {
	manager.mu.Lock()
	stream, exists := manager.sessions[requestID]
	if exists && (kind == "" || stream.kind == kind) {
		delete(manager.sessions, requestID)
	} else {
		exists = false
	}
	manager.mu.Unlock()
	if !exists {
		return false
	}
	stream.close()
	return true
}

func (manager *SessionManager) CloseAll() {
	manager.mu.Lock()
	streams := make([]*session, 0, len(manager.sessions))
	for _, stream := range manager.sessions {
		streams = append(streams, stream)
	}
	manager.sessions = make(map[string]*session)
	manager.mu.Unlock()
	for _, stream := range streams {
		stream.close()
	}
}

func (manager *SessionManager) get(requestID string) *session {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.sessions[requestID]
}

func (manager *SessionManager) finish(requestID string, stream *session) {
	manager.mu.Lock()
	if manager.sessions[requestID] == stream {
		delete(manager.sessions, requestID)
	}
	manager.mu.Unlock()
	stream.close()
}

func (manager *SessionManager) releaseReservation(requestID string, reservation *session) {
	manager.mu.Lock()
	if manager.sessions[requestID] == reservation {
		delete(manager.sessions, requestID)
	}
	manager.mu.Unlock()
}

func (manager *SessionManager) reject(requestID, code string, reason error) error {
	message := "Command rejected."
	if reason != nil {
		message = reason.Error()
	}
	if err := manager.sink.Send(nodes.MessageCommandResult, requestID, nodes.CommandResult{OK: false, Code: code, Message: message}); err != nil {
		return err
	}
	return reason
}

func (manager *SessionManager) removeExec(execID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = manager.backend.RemoveExec(ctx, execID, true)
}

func parseLogSince(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 || duration > 24*time.Hour {
		return 0, errors.New("log since must be a duration no greater than 24h")
	}
	return duration, nil
}

func normalizeSize(cols, rows uint16) (uint16, uint16) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	if cols > 1000 {
		cols = 1000
	}
	if rows > 500 {
		rows = 500
	}
	return cols, rows
}

func runtimeErrorCode(err error) string {
	switch {
	case errors.Is(err, podman.ErrProtectedContainer):
		return "container_protected"
	case errors.Is(err, podman.ErrContainerNotRunning):
		return "container_not_running"
	default:
		return "runtime_unavailable"
	}
}

type closedReader struct{}

func (closedReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (closedReader) Close() error             { return nil }
