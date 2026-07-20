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
	for _, since := range []string{"invalid", "-1s", "0s", "24h1s"} {
		if _, err := manager.Create("viewer@example.com", false, CreateRequest{Mode: ModeLogs, ContainerID: "app", Since: since}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid log since %q error = %v", since, err)
		}
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

func TestCreateHostEnforcesAuthorizationAvailabilityAndLimits(t *testing.T) {
	host := &fakeHostBackend{stream: newFakeHostStream()}
	manager, err := NewManagerWithHost(&fakeBackend{}, host, ManagerOptions{
		Random:         bytes.NewReader(bytes.Repeat([]byte{3}, 256)),
		MaxHostPerUser: 1,
		MaxHostGlobal:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateHost(context.Background(), "viewer", false, HostCreateRequest{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized CreateHost() error = %v", err)
	}
	first, err := manager.CreateHost(context.Background(), "admin", true, HostCreateRequest{Cols: 100, Rows: 25})
	if err != nil || first.Mode != ModeHost || first.ReadOnly || first.ContainerID != "" {
		t.Fatalf("first CreateHost() = %#v, %v", first, err)
	}
	if _, err := manager.CreateHost(context.Background(), "admin", true, HostCreateRequest{}); !errors.Is(err, ErrHostLimit) {
		t.Fatalf("same-user CreateHost() error = %v", err)
	}

	host.probeErr = errors.New("agent down")
	if _, err := manager.CreateHost(context.Background(), "other-admin", true, HostCreateRequest{}); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("unavailable CreateHost() error = %v", err)
	}
}

func TestServeLogsStreamsOutputAndIsReadOnly(t *testing.T) {
	backend := &fakeBackend{logs: io.NopCloser(bytes.NewBufferString("hello log\n"))}
	manager := newTestManager(t, backend, ManagerOptions{})
	session, err := manager.Create("viewer", false, CreateRequest{
		Mode: ModeLogs, ContainerID: "app", Tail: 25, Follow: false, Since: "6h", Timestamps: true,
	})
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
	if backend.logOptions.Tail != 25 || backend.logOptions.Follow || backend.logOptions.Since != 6*time.Hour || !backend.logOptions.Timestamps {
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

func TestServeHostReportsMetadataAndHandlesInputAndResize(t *testing.T) {
	stream := newFakeHostStream()
	host := &fakeHostBackend{stream: stream}
	manager, err := NewManagerWithHost(&fakeBackend{}, host, ManagerOptions{Random: bytes.NewReader(bytes.Repeat([]byte{4}, 128))})
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.CreateHost(context.Background(), "admin", true, HostCreateRequest{Cols: 100, Rows: 25})
	if err != nil {
		t.Fatal(err)
	}
	peer := &fakePeer{messages: []ClientMessage{
		{Type: ClientInput, Data: []byte("whoami\n")},
		{Type: ClientResize, Cols: 140, Rows: 40},
		{Type: ClientClose},
	}}
	result, err := manager.ServeDetailed(context.Background(), session.ID, "admin", peer)
	if err != nil {
		t.Fatalf("ServeDetailed() error = %v", err)
	}
	if result.Mode != ModeHost || result.Host != stream.info || result.CloseReason != "completed" {
		t.Fatalf("ServeDetailed() result = %#v", result)
	}
	if got := stream.written(); got != "whoami\n" {
		t.Fatalf("host input = %q", got)
	}
	if stream.size != (HostSize{Cols: 140, Rows: 40}) {
		t.Fatalf("host resize = %#v", stream.size)
	}
	controls := peer.controlsSnapshot()
	if len(controls) == 0 || controls[0].Type != ControlReady || controls[0].Hostname != "homelab" || controls[0].User != "binhminh" || controls[0].Shell != "/bin/bash" {
		t.Fatalf("host ready control = %#v", controls)
	}
}

func TestServeHostReportsExitCode(t *testing.T) {
	stream := &exitingHostStream{reader: bytes.NewReader([]byte("bye\n")), code: 7}
	host := &fakeHostBackend{stream: stream}
	manager, err := NewManagerWithHost(&fakeBackend{}, host, ManagerOptions{Random: bytes.NewReader(bytes.Repeat([]byte{5}, 64))})
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.CreateHost(context.Background(), "admin", true, HostCreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	peer := &fakePeer{}
	result, err := manager.ServeDetailed(context.Background(), session.ID, "admin", peer)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatalf("host exit code = %#v", result.ExitCode)
	}
	controls := peer.controlsSnapshot()
	last := controls[len(controls)-1]
	if last.Type != ControlExit || last.ExitCode == nil || *last.ExitCode != 7 {
		t.Fatalf("host exit control = %#v", last)
	}
}

func TestBlockedInputWriteCannotDefeatIdleTimeout(t *testing.T) {
	stream := newBlockingWriteStream()
	backend := &fakeBackend{stream: stream}
	manager := newTestManager(t, backend, ManagerOptions{IdleTimeout: 15 * time.Millisecond})
	session, err := manager.Create("admin", true, CreateRequest{Mode: ModeExec, ContainerID: "app"})
	if err != nil {
		t.Fatal(err)
	}
	peer := &fakePeer{messages: []ClientMessage{{Type: ClientInput, Data: []byte("blocked")}}}
	done := make(chan error, 1)
	go func() { done <- manager.Serve(context.Background(), session.ID, "admin", peer) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrIdleTimeout) {
			t.Fatalf("Serve() error = %v, want ErrIdleTimeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked stream write prevented the idle timeout")
	}
}

func TestRemoteNodeUsesTypedBackendAndPreservesLocalDefault(t *testing.T) {
	remote := &fakeRemoteBackend{stream: &fakeRemoteStream{reader: bytes.NewReader([]byte("remote log\n"))}}
	manager, err := NewManagerWithRemote(&fakeBackend{}, nil, remote, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.Create("admin", true, CreateRequest{
		Mode: ModeLogs, NodeID: "node_compute", ContainerID: "app", Tail: 50, Since: "1h", Timestamps: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := &fakePeer{}
	if err := manager.Serve(context.Background(), session.ID, "admin", peer); err != nil {
		t.Fatal(err)
	}
	if remote.nodeID != "node_compute" || remote.containerID != "app" || remote.logs.Tail != 50 || remote.logs.Since != time.Hour || !remote.logs.Timestamps {
		t.Fatalf("remote request = node %q container %q options %#v", remote.nodeID, remote.containerID, remote.logs)
	}
	if outputs := peer.binary(); len(outputs) != 1 || string(outputs[0]) != "remote log\n" {
		t.Fatalf("remote outputs = %q", outputs)
	}

	local, err := manager.Create("admin", true, CreateRequest{Mode: ModeLogs, ContainerID: "local-app"})
	if err != nil || local.NodeID != localNodeID {
		t.Fatalf("local default session = %#v, %v", local, err)
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

type fakeHostBackend struct {
	probeErr error
	openErr  error
	stream   HostStream
}

type fakeRemoteBackend struct {
	nodeID      string
	containerID string
	logs        podman.LogsOptions
	stream      RemoteStream
}

func (backend *fakeRemoteBackend) Probe(_ context.Context, nodeID string) error {
	backend.nodeID = nodeID
	return nil
}

func (backend *fakeRemoteBackend) OpenLogs(_ context.Context, nodeID, containerID string, options podman.LogsOptions) (RemoteStream, error) {
	backend.nodeID, backend.containerID, backend.logs = nodeID, containerID, options
	return backend.stream, nil
}

func (backend *fakeRemoteBackend) OpenExec(_ context.Context, nodeID, containerID string, _ HostSize) (RemoteStream, error) {
	backend.nodeID, backend.containerID = nodeID, containerID
	return backend.stream, nil
}

func (backend *fakeRemoteBackend) OpenHost(_ context.Context, nodeID string, _ HostSize) (RemoteStream, error) {
	backend.nodeID = nodeID
	return backend.stream, nil
}

type fakeRemoteStream struct {
	reader *bytes.Reader
	writes bytes.Buffer
	info   HostInfo
	exit   int
}

func (stream *fakeRemoteStream) Read(target []byte) (int, error) { return stream.reader.Read(target) }
func (stream *fakeRemoteStream) Write(payload []byte) (int, error) {
	return stream.writes.Write(payload)
}
func (*fakeRemoteStream) Close() error                           { return nil }
func (*fakeRemoteStream) Resize(context.Context, HostSize) error { return nil }
func (stream *fakeRemoteStream) Info() HostInfo                  { return stream.info }
func (stream *fakeRemoteStream) ExitCode() (int, bool)           { return stream.exit, true }

func (b *fakeHostBackend) Probe(context.Context) error { return b.probeErr }

func (b *fakeHostBackend) Open(context.Context, HostSize) (HostStream, error) {
	if b.openErr != nil {
		return nil, b.openErr
	}
	return b.stream, nil
}

type fakeHostStream struct {
	*blockingStream
	info HostInfo
	size HostSize
}

func newFakeHostStream() *fakeHostStream {
	return &fakeHostStream{
		blockingStream: newBlockingStream(),
		info:           HostInfo{Hostname: "homelab", User: "binhminh", Shell: "/bin/bash"},
	}
}

func (s *fakeHostStream) Resize(_ context.Context, size HostSize) error {
	s.size = size
	return nil
}

func (s *fakeHostStream) Info() HostInfo        { return s.info }
func (s *fakeHostStream) ExitCode() (int, bool) { return 0, false }

type exitingHostStream struct {
	reader *bytes.Reader
	code   int
}

func (s *exitingHostStream) Read(target []byte) (int, error) { return s.reader.Read(target) }
func (*exitingHostStream) Write(payload []byte) (int, error) { return len(payload), nil }
func (*exitingHostStream) Close() error                      { return nil }
func (*exitingHostStream) Resize(context.Context, HostSize) error {
	return nil
}
func (*exitingHostStream) Info() HostInfo {
	return HostInfo{Hostname: "homelab", User: "binhminh", Shell: "/bin/bash"}
}
func (s *exitingHostStream) ExitCode() (int, bool) { return s.code, true }

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

type blockingWriteStream struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingWriteStream() *blockingWriteStream {
	return &blockingWriteStream{closed: make(chan struct{})}
}

func (s *blockingWriteStream) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.EOF
}

func (s *blockingWriteStream) Write([]byte) (int, error) {
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *blockingWriteStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
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
