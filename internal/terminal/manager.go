package terminal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/podman"
)

const (
	defaultCols = 120
	defaultRows = 30
	minCols     = 20
	maxCols     = 300
	minRows     = 5
	maxRows     = 100
)

type Manager struct {
	backend     Backend
	hostBackend HostBackend
	options     ManagerOptions

	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager(backend Backend, options ManagerOptions) (*Manager, error) {
	return NewManagerWithHost(backend, nil, options)
}

func NewManagerWithHost(backend Backend, hostBackend HostBackend, options ManagerOptions) (*Manager, error) {
	if backend == nil {
		return nil, errors.New("terminal: backend is required")
	}
	withManagerDefaults(&options)
	if options.MaxExecPerUser < 1 || options.MaxExecGlobal < 1 || options.MaxExecPerUser > options.MaxExecGlobal {
		return nil, errors.New("terminal: invalid exec limits")
	}
	if options.MaxHostPerUser < 1 || options.MaxHostGlobal < 1 || options.MaxHostPerUser > options.MaxHostGlobal {
		return nil, errors.New("terminal: invalid host shell limits")
	}
	if options.MaxSessionsPerUser < options.MaxExecPerUser || options.MaxSessionsGlobal < options.MaxExecGlobal || options.MaxSessionsPerUser > options.MaxSessionsGlobal {
		return nil, errors.New("terminal: invalid session limits")
	}
	if options.MaxSessionsPerUser < options.MaxHostPerUser || options.MaxSessionsGlobal < options.MaxHostGlobal {
		return nil, errors.New("terminal: host shell limits exceed session limits")
	}
	if options.PendingTTL < 0 || options.IdleTimeout < 0 || options.HardTimeout < 0 {
		return nil, errors.New("terminal: invalid session timeouts")
	}
	if options.MaxInputFrame < 1 || options.OutputChunkSize < 1 || options.OutputQueueSize < options.OutputChunkSize {
		return nil, errors.New("terminal: invalid stream limits")
	}
	return &Manager{backend: backend, hostBackend: hostBackend, options: options, sessions: make(map[string]*Session)}, nil
}

func withManagerDefaults(options *ManagerOptions) {
	if options.PendingTTL == 0 {
		options.PendingTTL = 30 * time.Second
	}
	if options.IdleTimeout == 0 {
		options.IdleTimeout = 15 * time.Minute
	}
	if options.HardTimeout == 0 {
		options.HardTimeout = time.Hour
	}
	if options.MaxExecPerUser == 0 {
		options.MaxExecPerUser = 1
	}
	if options.MaxExecGlobal == 0 {
		options.MaxExecGlobal = 4
	}
	if options.MaxHostPerUser == 0 {
		options.MaxHostPerUser = 1
	}
	if options.MaxHostGlobal == 0 {
		options.MaxHostGlobal = 2
	}
	if options.MaxSessionsPerUser == 0 {
		options.MaxSessionsPerUser = 4
	}
	if options.MaxSessionsGlobal == 0 {
		options.MaxSessionsGlobal = 16
	}
	if options.MaxInputFrame == 0 {
		options.MaxInputFrame = 16 << 10
	}
	if options.OutputChunkSize == 0 {
		options.OutputChunkSize = 32 << 10
	}
	if options.OutputQueueSize == 0 {
		options.OutputQueueSize = 1 << 20
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Now == nil {
		options.Now = time.Now
	}
}

func (m *Manager) Create(user string, isAdmin bool, request CreateRequest) (Session, error) {
	user = strings.TrimSpace(user)
	if user == "" || !validContainerID(request.ContainerID) {
		return Session{}, ErrInvalidRequest
	}
	if request.Mode != ModeLogs && request.Mode != ModeExec {
		return Session{}, ErrInvalidRequest
	}
	if request.Mode == ModeExec && !isAdmin {
		return Session{}, ErrUnauthorized
	}
	if request.Tail == 0 {
		request.Tail = 200
	}
	if request.Tail > 500 {
		return Session{}, ErrInvalidRequest
	}
	if request.Cols == 0 {
		request.Cols = defaultCols
	}
	if request.Rows == 0 {
		request.Rows = defaultRows
	}
	if !validSize(request.Cols, request.Rows) {
		return Session{}, ErrInvalidRequest
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.options.Now()
	m.pruneExpiredLocked(now)
	if !m.canReserveSessionLocked(user) {
		return Session{}, ErrSessionLimit
	}
	if request.Mode == ModeExec && !m.canReserveExecLocked(user) {
		return Session{}, ErrExecLimit
	}
	id, err := m.newIDLocked()
	if err != nil {
		return Session{}, fmt.Errorf("terminal: generate session ID: %w", err)
	}
	session := &Session{
		ID:          id,
		Mode:        request.Mode,
		ContainerID: request.ContainerID,
		User:        user,
		ReadOnly:    request.Mode == ModeLogs,
		CreatedAt:   now,
		ExpiresAt:   now.Add(m.options.PendingTTL),
		tail:        request.Tail,
		follow:      request.Follow,
		cols:        request.Cols,
		rows:        request.Rows,
		state:       sessionPending,
	}
	m.sessions[id] = session
	return *session, nil
}

func (m *Manager) CreateHost(ctx context.Context, user string, authorized bool, request HostCreateRequest) (Session, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return Session{}, ErrInvalidRequest
	}
	if !authorized {
		return Session{}, ErrUnauthorized
	}
	if request.Cols == 0 {
		request.Cols = defaultCols
	}
	if request.Rows == 0 {
		request.Rows = defaultRows
	}
	if !validSize(request.Cols, request.Rows) {
		return Session{}, ErrInvalidRequest
	}
	if m.hostBackend == nil {
		return Session{}, ErrHostUnavailable
	}
	if err := m.hostBackend.Probe(ctx); err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrHostUnavailable, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.options.Now()
	m.pruneExpiredLocked(now)
	if !m.canReserveSessionLocked(user) {
		return Session{}, ErrSessionLimit
	}
	if !m.canReserveHostLocked(user) {
		return Session{}, ErrHostLimit
	}
	id, err := m.newIDLocked()
	if err != nil {
		return Session{}, fmt.Errorf("terminal: generate session ID: %w", err)
	}
	session := &Session{
		ID:        id,
		Mode:      ModeHost,
		User:      user,
		CreatedAt: now,
		ExpiresAt: now.Add(m.options.PendingTTL),
		cols:      request.Cols,
		rows:      request.Rows,
		state:     sessionPending,
	}
	m.sessions[id] = session
	return *session, nil
}

func (m *Manager) Get(id, user string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked(m.options.Now())
	session, ok := m.sessions[id]
	if !ok || session.User != user {
		return Session{}, ErrNotFound
	}
	return *session, nil
}

// Cancel releases a pending reservation, for example when a browser abandons
// the HTTP-created session before opening its WebSocket.
func (m *Manager) Cancel(id, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	if !ok || session.User != user {
		return ErrNotFound
	}
	if session.state != sessionPending {
		return ErrSessionClaimed
	}
	delete(m.sessions, id)
	return nil
}

func (m *Manager) Serve(ctx context.Context, id, user string, peer Peer) error {
	_, err := m.ServeDetailed(ctx, id, user, peer)
	return err
}

func (m *Manager) ServeDetailed(ctx context.Context, id, user string, peer Peer) (result ServeResult, returnErr error) {
	if peer == nil {
		return result, ErrInvalidRequest
	}
	session, err := m.claim(id, user)
	if err != nil {
		return result, err
	}
	result.Mode = session.Mode
	result.StartedAt = m.options.Now()
	defer func() {
		result.ClosedAt = m.options.Now()
		if result.CloseReason == "" {
			result.CloseReason = closeReason(returnErr)
		}
	}()

	serveCtx, cancel := context.WithCancel(ctx)
	defer peer.Close()
	defer m.release(session.ID)

	opened, err := m.openStream(serveCtx, session)
	if err != nil {
		cancel()
		message := "Unable to open container session."
		if session.Mode == ModeHost {
			message = "Unable to open host shell."
		}
		_ = peer.WriteControl(ctx, Control{Type: ControlError, Code: "open_failed", Message: message})
		return result, err
	}
	if opened.cleanup != nil {
		defer opened.cleanup()
	}
	defer opened.stream.Close()
	defer cancel()
	result.Host = opened.info
	if err := peer.WriteControl(serveCtx, Control{
		Type: ControlReady, ReadOnly: session.ReadOnly,
		Hostname: opened.info.Hostname, User: opened.info.User, Shell: opened.info.Shell,
	}); err != nil {
		return result, fmt.Errorf("terminal: write ready control: %w", err)
	}

	err = m.bridge(serveCtx, session, opened, peer)
	if opened.exitCode != nil {
		if code, ok := opened.exitCode(); ok {
			result.ExitCode = &code
		}
	}
	result.CloseReason = closeReason(err)
	switch {
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, ErrPeerDisconnected), errors.Is(err, context.Canceled):
		return result, nil
	case errors.Is(err, ErrIdleTimeout):
		_ = peer.WriteControl(ctx, Control{Type: ControlError, Code: "idle_timeout", Message: "Terminal closed after being idle."})
	case errors.Is(err, ErrHardTimeout):
		_ = peer.WriteControl(ctx, Control{Type: ControlError, Code: "maximum_duration", Message: "Terminal reached its maximum duration."})
	case errors.Is(err, ErrReadOnly):
		_ = peer.WriteControl(ctx, Control{Type: ControlError, Code: "read_only", Message: "Log sessions do not accept input."})
	case errors.Is(err, ErrInputTooLarge):
		_ = peer.WriteControl(ctx, Control{Type: ControlError, Code: "input_too_large", Message: "Terminal input frame is too large."})
	default:
		_ = peer.WriteControl(ctx, Control{Type: ControlError, Code: "stream_error", Message: "Terminal stream closed unexpectedly."})
	}
	return result, err
}

func (m *Manager) claim(id, user string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.options.Now()
	session, ok := m.sessions[id]
	if !ok || session.User != user {
		return nil, ErrNotFound
	}
	if session.state != sessionPending {
		return nil, ErrSessionClaimed
	}
	if !now.Before(session.ExpiresAt) {
		delete(m.sessions, id)
		return nil, ErrSessionExpired
	}
	session.state = sessionRunning
	session.ExpiresAt = now.Add(m.options.HardTimeout)
	copy := *session
	return &copy, nil
}

type openedStream struct {
	stream   io.ReadWriteCloser
	resize   func(context.Context, HostSize) error
	cleanup  func()
	info     HostInfo
	exitCode func() (int, bool)
}

func (m *Manager) openStream(ctx context.Context, session *Session) (*openedStream, error) {
	if session.Mode == ModeLogs {
		logs, err := m.backend.Logs(ctx, session.ContainerID, podman.LogsOptions{Tail: session.tail, Follow: session.follow})
		if err != nil {
			return nil, err
		}
		return &openedStream{stream: readOnlyStream{ReadCloser: logs}}, nil
	}
	if session.Mode == ModeHost {
		if m.hostBackend == nil {
			return nil, ErrHostUnavailable
		}
		stream, err := m.hostBackend.Open(ctx, HostSize{Cols: session.cols, Rows: session.rows})
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrHostUnavailable, err)
		}
		return &openedStream{
			stream:   stream,
			resize:   stream.Resize,
			info:     stream.Info(),
			exitCode: stream.ExitCode,
		}, nil
	}

	size := podman.TerminalSize{Cols: session.cols, Rows: session.rows}
	var lastErr error
	for _, shell := range []podman.Shell{podman.ShellSH, podman.ShellBash} {
		execID, err := m.backend.CreateShellExec(ctx, session.ContainerID, shell, size)
		if err != nil {
			lastErr = err
			continue
		}
		stream, err := m.backend.StartExec(ctx, execID, size)
		if err == nil {
			session.execID = execID
			return &openedStream{
				stream: stream,
				resize: func(ctx context.Context, hostSize HostSize) error {
					return m.backend.ResizeExec(ctx, execID, podman.TerminalSize{Cols: hostSize.Cols, Rows: hostSize.Rows})
				},
				cleanup: func() { m.removeExec(execID) },
			}, nil
		}
		lastErr = err
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = m.backend.RemoveExec(cleanupCtx, execID, true)
		cancel()
	}
	if lastErr == nil {
		lastErr = errors.New("terminal: no supported shell")
	}
	return nil, lastErr
}

func (m *Manager) bridge(ctx context.Context, session *Session, opened *openedStream, peer Peer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	queueCapacity := m.options.OutputQueueSize / m.options.OutputChunkSize
	if queueCapacity < 1 {
		queueCapacity = 1
	}
	backendEvents := make(chan backendEvent, queueCapacity)
	peerInput := make(chan ClientMessage)
	peerErr := make(chan error, 1)

	go readBackend(ctx, opened.stream, m.options.OutputChunkSize, backendEvents)
	go readPeer(ctx, peer, peerInput, peerErr)
	writeResults := make(chan error, 1)
	activePeerInput := (<-chan ClientMessage)(peerInput)

	idle := time.NewTimer(m.options.IdleTimeout)
	defer idle.Stop()
	hard := time.NewTimer(m.options.HardTimeout)
	defer hard.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle.C:
			return ErrIdleTimeout
		case <-hard.C:
			return ErrHardTimeout
		case event := <-backendEvents:
			if len(event.data) > 0 {
				if err := peer.WriteBinary(ctx, event.data); err != nil {
					return fmt.Errorf("terminal: write output: %w", err)
				}
				resetTimer(idle, m.options.IdleTimeout)
			}
			if event.err == nil {
				continue
			}
			if errors.Is(event.err, io.EOF) {
				control := Control{Type: ControlExit}
				if opened.exitCode != nil {
					if code, ok := opened.exitCode(); ok {
						control.ExitCode = &code
					}
				}
				_ = peer.WriteControl(ctx, control)
				return nil
			}
			return fmt.Errorf("terminal: read backend: %w", event.err)
		case message := <-activePeerInput:
			resetTimer(idle, m.options.IdleTimeout)
			switch message.Type {
			case ClientClose:
				return nil
			case ClientInput:
				if session.ReadOnly {
					return ErrReadOnly
				}
				if len(message.Data) > m.options.MaxInputFrame {
					return ErrInputTooLarge
				}
				payload := append([]byte(nil), message.Data...)
				activePeerInput = nil
				go func() { writeResults <- writeAll(opened.stream, payload) }()
			case ClientResize:
				if session.ReadOnly || opened.resize == nil {
					continue
				}
				if !validSize(message.Cols, message.Rows) {
					return ErrInvalidRequest
				}
				if err := opened.resize(ctx, HostSize{Cols: message.Cols, Rows: message.Rows}); err != nil {
					return fmt.Errorf("terminal: resize: %w", err)
				}
			default:
				return ErrInvalidRequest
			}
		case err := <-peerErr:
			if err == nil || errors.Is(err, io.EOF) {
				return ErrPeerDisconnected
			}
			return fmt.Errorf("terminal: read peer: %w", err)
		case err := <-writeResults:
			if err != nil {
				return fmt.Errorf("terminal: write input: %w", err)
			}
			activePeerInput = peerInput
		}
	}
}

func closeReason(err error) string {
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return "completed"
	case errors.Is(err, ErrPeerDisconnected):
		return "peer_disconnected"
	case errors.Is(err, ErrIdleTimeout):
		return "idle_timeout"
	case errors.Is(err, ErrHardTimeout):
		return "maximum_duration"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "failed"
	}
}

func (m *Manager) removeExec(execID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.backend.RemoveExec(ctx, execID, true)
}

func (m *Manager) release(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func (m *Manager) canReserveExecLocked(user string) bool {
	userCount := 0
	globalCount := 0
	for _, session := range m.sessions {
		if session.Mode != ModeExec {
			continue
		}
		globalCount++
		if session.User == user {
			userCount++
		}
	}
	return userCount < m.options.MaxExecPerUser && globalCount < m.options.MaxExecGlobal
}

func (m *Manager) canReserveHostLocked(user string) bool {
	userCount := 0
	globalCount := 0
	for _, session := range m.sessions {
		if session.Mode != ModeHost {
			continue
		}
		globalCount++
		if session.User == user {
			userCount++
		}
	}
	return userCount < m.options.MaxHostPerUser && globalCount < m.options.MaxHostGlobal
}

func (m *Manager) canReserveSessionLocked(user string) bool {
	userCount := 0
	for _, session := range m.sessions {
		if session.User == user {
			userCount++
		}
	}
	return userCount < m.options.MaxSessionsPerUser && len(m.sessions) < m.options.MaxSessionsGlobal
}

func (m *Manager) pruneExpiredLocked(now time.Time) {
	for id, session := range m.sessions {
		if session.state == sessionPending && !now.Before(session.ExpiresAt) {
			delete(m.sessions, id)
		}
	}
}

func (m *Manager) newIDLocked() (string, error) {
	for range 3 {
		buffer := make([]byte, 32)
		if _, err := io.ReadFull(m.options.Random, buffer); err != nil {
			return "", err
		}
		id := base64.RawURLEncoding.EncodeToString(buffer)
		if _, exists := m.sessions[id]; !exists {
			return id, nil
		}
	}
	return "", errors.New("session ID collision")
}

func validContainerID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '.' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validSize(cols, rows uint) bool {
	return cols >= minCols && cols <= maxCols && rows >= minRows && rows <= maxRows
}

type backendEvent struct {
	data []byte
	err  error
}

func readBackend(ctx context.Context, stream io.Reader, chunkSize int, events chan<- backendEvent) {
	for {
		buffer := make([]byte, chunkSize)
		count, err := stream.Read(buffer)
		if count > 0 || err != nil {
			select {
			case events <- backendEvent{data: buffer[:count], err: err}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func readPeer(ctx context.Context, peer Peer, messages chan<- ClientMessage, result chan<- error) {
	for {
		message, err := peer.Read(ctx)
		if err != nil {
			select {
			case result <- err:
			case <-ctx.Done():
			}
			return
		}
		select {
		case messages <- message:
		case <-ctx.Done():
			return
		}
	}
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		count, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		payload = payload[count:]
	}
	return nil
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

type readOnlyStream struct {
	io.ReadCloser
}

func (readOnlyStream) Write([]byte) (int, error) {
	return 0, ErrReadOnly
}
