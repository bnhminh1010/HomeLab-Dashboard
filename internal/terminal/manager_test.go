package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/podman"
)

func TestCreateEnforcesRoleLimitsAndExpiration(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &fakeBackend{}, ManagerOptions{
		Now:            func() time.Time { return now },
		Random:         bytes.NewReader(bytes.Repeat([]byte{7}, 256)),
		MaxExecPerUser: 1,
		MaxExecGlobal:  2,
	})

	if _, err := manager.Create("viewer@example.com", false, CreateRequest{Mode: ModeExec, ContainerID: "app"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("viewer exec error = %v", err)
	}
	if _, err := manager.Create("viewer@example.com", false, CreateRequest{Mode: ModeLogs, ContainerID: "app", Tail: 501}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversized log tail error = %v", err)
	}
	first, err := manager.Create("admin@example.com", true, CreateRequest{Mode: ModeExec, ContainerID: "app"})
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if first.ID == "" || first.ReadOnly {
		t.Fatalf("first session = %#v", first)
	}
	if _, err := manager.Create("admin@example.com", true, CreateRequest{Mode: ModeExec, ContainerID: "other"}); !errors.Is(err, ErrExecLimit) {
		t.Fatalf("second same-user exec error = %v", err)
	}

	now = now.Add(31 * time.Second)
	if _, err := manager.Get(first.ID, "admin@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired Get() error = %v", err)
	}
	if _, err := manager.Create("admin@example.com", true, CreateRequest{Mode: ModeExec, ContainerID: "other"}); err != nil {
		t.Fatalf("Create() after expiration error = %v", err)
	}
}

func TestCreateLimitsReadOnlyLogReservations(t *testing.T) {
	manager := newTestManager(t, &fakeBackend{}, ManagerOptions{
		MaxSessionsPerUser: 2,
		MaxSessionsGlobal:  3,
		MaxExecPerUser:     1,
		MaxExecGlobal:      2,
		Random: bytes.NewReader(append(
			bytes.Repeat([]byte{1}, 32),
			bytes.Repeat([]byte{2}, 32)...,
		)),
	})
	for _, container := range []string{"one", "two"} {
		if _, err := manager.Create("viewer", false, CreateRequest{Mode: ModeLogs, ContainerID: container}); err != nil {
			t.Fatalf("Create(%s): %v", container, err)
		}
	}
	if _, err := manager.Create("viewer", false, CreateRequest{Mode: ModeLogs, ContainerID: "three"}); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("third log reservation error = %v", err)
	}
}

func TestServeLogsStreamsOutputAndIsReadOnly(t *testing.T) {
	backend := &fakeBackend{logs: io.NopCloser(bytes.NewBufferString("hello log\n"))}
	manager := newTestManager(t, backend, ManagerOptions{})
	session, err := manager.Create("viewer", false, CreateRequest{Mode: ModeLogs, ContainerID: "app", Tail: 25, Follow: false})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	peer := &fakePeer{}
	if err := manager.Serve(context.Background(), session.ID, "viewer", peer); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got := string(bytes.Join(peer.binary(), nil)); got != "hello log\n" {
		t.Fatalf("output = %q", got)
	}
	controls := peer.controlsSnapshot()
	if len(controls) < 2 || controls[0].Type != ControlReady || !controls[0].ReadOnly || controls[len(controls)-1].Type != ControlExit {
		t.Fatalf("controls = %#v", controls)
	}
	if backend.logOptions.Tail != 25 || backend.logOptions.Follow {
		t.Fatalf("log options = %#v", backend.logOptions)
	}
}

func TestServePreservesFinalBytesReturnedWithEOF(t *testing.T) {
	backend := &fakeBackend{logs: &finalEOFReader{payload: []byte("last line\n")}}
	manager := newTestManager(t, backend, ManagerOptions{})
	session, err := manager.Create("viewer", false, CreateRequest{Mode: ModeLogs, ContainerID: "app"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	peer := &fakePeer{}
	if err := manager.Serve(context.Background(), session.ID, "viewer", peer); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got := string(bytes.Join(peer.binary(), nil)); got != "last line\n" {
		t.Fatalf("final output = %q", got)
	}
}

func TestServeEnforcesIdleTimeout(t *testing.T) {
	backend := &fakeBackend{logs: newBlockingStream()}
	manager := newTestManager(t, backend, ManagerOptions{IdleTimeout: 10 * time.Millisecond})
	session, err := manager.Create("viewer", false, CreateRequest{Mode: ModeLogs, ContainerID: "app"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	err = manager.Serve(context.Background(), session.ID, "viewer", &fakePeer{})
	if !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("Serve() error = %v, want ErrIdleTimeout", err)
	}
}

func TestServeExecFallsBackToBashAndHandlesTypedInput(t *testing.T) {
	stream := newBlockingStream()
	backend := &fakeBackend{stream: stream, failStart: map[string]error{"exec-sh": errors.New("sh missing")}}
	manager := newTestManager(t, backend, ManagerOptions{})
	session, err := manager.Create("admin", true, CreateRequest{Mode: ModeExec, ContainerID: "app", Cols: 100, Rows: 25})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	peer := &fakePeer{messages: []ClientMessage{
		{Type: ClientInput, Data: []byte("echo safe\n")},
		{Type: ClientResize, Cols: 140, Rows: 40},
		{Type: ClientClose},
	}}
	if err := manager.Serve(context.Background(), session.ID, "admin", peer); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if got := stream.written(); got != "echo safe\n" {
		t.Fatalf("stream input = %q", got)
	}
	if want := []podman.Shell{podman.ShellSH, podman.ShellBash}; !equalShells(backend.shellsSnapshot(), want) {
		t.Fatalf("shell attempts = %#v, want %#v", backend.shellsSnapshot(), want)
	}
	if backend.resize != (podman.TerminalSize{Cols: 140, Rows: 40}) {
		t.Fatalf("resize = %#v", backend.resize)
	}
	if removed := backend.removedSnapshot(); len(removed) != 2 || removed[0] != "exec-sh" || removed[1] != "exec-bash" {
		t.Fatalf("removed exec IDs = %#v", removed)
	}
}

func TestServeRejectsInputForLogSession(t *testing.T) {
	stream := newBlockingStream()
	backend := &fakeBackend{logs: stream}
	manager := newTestManager(t, backend, ManagerOptions{})
	session, err := manager.Create("viewer", false, CreateRequest{Mode: ModeLogs, ContainerID: "app"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	peer := &fakePeer{messages: []ClientMessage{{Type: ClientInput, Data: []byte("whoami\n")}}}
	err = manager.Serve(context.Background(), session.ID, "viewer", peer)
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Serve() error = %v, want ErrReadOnly", err)
	}
}

func TestSessionOwnershipAndSingleClaim(t *testing.T) {
	backend := &fakeBackend{logs: newBlockingStream()}
	manager := newTestManager(t, backend, ManagerOptions{})
	session, err := manager.Create("alice", false, CreateRequest{Mode: ModeLogs, ContainerID: "app"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := manager.Get(session.ID, "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Get() error = %v", err)
	}
	claimed, err := manager.claim(session.ID, "alice")
	if err != nil || claimed.ID != session.ID {
		t.Fatalf("claim() = %#v, %v", claimed, err)
	}
	if _, err := manager.claim(session.ID, "alice"); !errors.Is(err, ErrSessionClaimed) {
		t.Fatalf("second claim error = %v", err)
	}
}

func newTestManager(t *testing.T, backend Backend, options ManagerOptions) *Manager {
	t.Helper()
	if options.Random == nil {
		options.Random = bytes.NewReader(bytes.Repeat([]byte{9}, 512))
	}
	manager, err := NewManager(backend, options)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

type fakeBackend struct {
	mu         sync.Mutex
	logs       io.ReadCloser
	logOptions podman.LogsOptions
	shells     []podman.Shell
	failStart  map[string]error
	stream     io.ReadWriteCloser
	resize     podman.TerminalSize
	removed    []string
}

func (b *fakeBackend) Logs(_ context.Context, _ string, options podman.LogsOptions) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logOptions = options
	if b.logs == nil {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	return b.logs, nil
}

func (b *fakeBackend) CreateShellExec(_ context.Context, _ string, shell podman.Shell, _ podman.TerminalSize) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shells = append(b.shells, shell)
	if shell == podman.ShellSH {
		return "exec-sh", nil
	}
	return "exec-bash", nil
}

func (b *fakeBackend) StartExec(_ context.Context, execID string, _ podman.TerminalSize) (io.ReadWriteCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.failStart[execID]; err != nil {
		return nil, err
	}
	if b.stream == nil {
		b.stream = newBlockingStream()
	}
	return b.stream, nil
}

func (b *fakeBackend) ResizeExec(_ context.Context, _ string, size podman.TerminalSize) error {
	b.mu.Lock()
	b.resize = size
	b.mu.Unlock()
	return nil
}

func (b *fakeBackend) RemoveExec(_ context.Context, execID string, _ bool) error {
	b.mu.Lock()
	b.removed = append(b.removed, execID)
	b.mu.Unlock()
	return nil
}

func (b *fakeBackend) shellsSnapshot() []podman.Shell {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]podman.Shell(nil), b.shells...)
}

func (b *fakeBackend) removedSnapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.removed...)
}

type blockingStream struct {
	mu     sync.Mutex
	writes bytes.Buffer
	closed chan struct{}
	once   sync.Once
}

type finalEOFReader struct {
	payload []byte
	done    bool
}

func (r *finalEOFReader) Read(buffer []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(buffer, r.payload), io.EOF
}

func (*finalEOFReader) Close() error { return nil }

func newBlockingStream() *blockingStream {
	return &blockingStream{closed: make(chan struct{})}
}

func (s *blockingStream) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.EOF
}

func (s *blockingStream) Write(payload []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes.Write(payload)
}

func (s *blockingStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *blockingStream) written() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes.String()
}

type fakePeer struct {
	mu       sync.Mutex
	messages []ClientMessage
	index    int
	outputs  [][]byte
	controls []Control
	closed   chan struct{}
	once     sync.Once
}

func (p *fakePeer) Read(ctx context.Context) (ClientMessage, error) {
	p.mu.Lock()
	if p.index < len(p.messages) {
		message := p.messages[p.index]
		p.index++
		p.mu.Unlock()
		return message, nil
	}
	if p.closed == nil {
		p.closed = make(chan struct{})
	}
	closed := p.closed
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		return ClientMessage{}, ctx.Err()
	case <-closed:
		return ClientMessage{}, io.EOF
	}
}

func (p *fakePeer) WriteBinary(_ context.Context, payload []byte) error {
	p.mu.Lock()
	p.outputs = append(p.outputs, append([]byte(nil), payload...))
	p.mu.Unlock()
	return nil
}

func (p *fakePeer) WriteControl(_ context.Context, control Control) error {
	p.mu.Lock()
	p.controls = append(p.controls, control)
	p.mu.Unlock()
	return nil
}

func (p *fakePeer) Close() error {
	p.mu.Lock()
	if p.closed == nil {
		p.closed = make(chan struct{})
	}
	closed := p.closed
	p.mu.Unlock()
	p.once.Do(func() { close(closed) })
	return nil
}

func (p *fakePeer) binary() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.outputs...)
}

func (p *fakePeer) controlsSnapshot() []Control {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Control(nil), p.controls...)
}

func equalShells(left, right []podman.Shell) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
