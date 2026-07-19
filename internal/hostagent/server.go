package hostagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const shellPath = "/bin/bash"

type Options struct {
	SocketPath      string
	MaxSessions     int
	IdleTimeout     time.Duration
	HardTimeout     time.Duration
	OutputChunkSize int
	OutputQueueSize int
}

type Server struct {
	options Options
	uid     uint32
	home    string
	info    Info
	env     []string

	sessionSlots chan struct{}
	connections  connectionSet
}

type connectionSet struct {
	sync.Mutex
	items map[*net.UnixConn]struct{}
}

func Run(ctx context.Context, options Options) error {
	server, err := NewServer(options)
	if err != nil {
		return err
	}
	return server.Serve(ctx)
}

func NewServer(options Options) (*Server, error) {
	withServerDefaults(&options)
	if options.SocketPath == "" || !filepath.IsAbs(options.SocketPath) {
		return nil, errors.New("hostagent: socket path must be absolute")
	}
	if options.MaxSessions < 1 || options.IdleTimeout <= 0 || options.HardTimeout <= 0 || options.OutputChunkSize < 1 || options.OutputChunkSize > maxOutputPayload || options.OutputQueueSize < options.OutputChunkSize {
		return nil, errors.New("hostagent: invalid server options")
	}
	current, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("hostagent: resolve current user: %w", err)
	}
	uid, err := validateEffectiveUID(current.Uid, os.Geteuid())
	if err != nil {
		return nil, err
	}
	if current.HomeDir == "" || !filepath.IsAbs(current.HomeDir) {
		return nil, errors.New("hostagent: current user has no absolute home directory")
	}
	if stat, err := os.Stat(shellPath); err != nil || stat.IsDir() || stat.Mode()&0111 == 0 {
		return nil, fmt.Errorf("hostagent: %s is not executable", shellPath)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("hostagent: resolve hostname: %w", err)
	}
	info := Info{Hostname: hostname, User: current.Username, Shell: shellPath}
	return &Server{
		options:      options,
		uid:          uid,
		home:         current.HomeDir,
		info:         info,
		env:          safeEnvironment(current, hostname),
		sessionSlots: make(chan struct{}, options.MaxSessions),
		connections:  connectionSet{items: make(map[*net.UnixConn]struct{})},
	}, nil
}

func validateEffectiveUID(userUID string, effectiveUID int) (uint32, error) {
	if effectiveUID == 0 {
		return 0, errors.New("hostagent: refusing to run as root")
	}
	if effectiveUID < 0 {
		return 0, errors.New("hostagent: invalid effective UID")
	}
	uid, err := strconv.ParseUint(userUID, 10, 32)
	if err != nil || uint32(uid) != uint32(effectiveUID) {
		return 0, errors.New("hostagent: current user does not match effective UID")
	}
	return uint32(uid), nil
}

func withServerDefaults(options *Options) {
	if options.MaxSessions == 0 {
		options.MaxSessions = 2
	}
	if options.IdleTimeout == 0 {
		options.IdleTimeout = 15 * time.Minute
	}
	if options.HardTimeout == 0 {
		options.HardTimeout = time.Hour
	}
	if options.OutputChunkSize == 0 {
		options.OutputChunkSize = maxOutputPayload
	}
	if options.OutputQueueSize == 0 {
		options.OutputQueueSize = 1 << 20
	}
}

func (server *Server) Serve(ctx context.Context) error {
	if err := prepareSocketDirectory(server.options.SocketPath, server.uid); err != nil {
		return err
	}
	if err := removeStaleSocket(server.options.SocketPath, server.uid); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: server.options.SocketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("hostagent: listen on Unix socket: %w", err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(server.options.SocketPath, 0600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("hostagent: secure Unix socket: %w", err)
	}

	var handlers sync.WaitGroup
	shutdownDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
			server.closeConnections()
		case <-shutdownDone:
		}
	}()

	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			close(shutdownDone)
			_ = listener.Close()
			server.closeConnections()
			handlers.Wait()
			return fmt.Errorf("hostagent: accept Unix connection: %w", err)
		}
		if ctx.Err() != nil {
			_ = connection.Close()
			break
		}
		server.addConnection(connection)
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer server.removeConnection(connection)
			server.handleConnection(ctx, connection)
		}()
	}
	close(shutdownDone)
	server.closeConnections()
	handlers.Wait()
	return nil
}

func (server *Server) handleConnection(ctx context.Context, connection *net.UnixConn) {
	defer connection.Close()
	peerUID, err := unixPeerUID(connection)
	if err != nil || peerUID != server.uid {
		_ = server.writeError(connection, "peer_forbidden", "Unix peer credentials were rejected.")
		return
	}
	_ = connection.SetReadDeadline(time.Now().Add(10 * time.Second))
	request, err := readFrame(connection)
	if err != nil {
		if errors.Is(err, ErrProtocol) {
			_ = server.writeError(connection, "protocol_error", "The opening frame is invalid.")
		}
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	if request.typeID != frameOpen {
		_ = server.writeError(connection, "protocol_error", "The first frame must open a shell.")
		return
	}
	size, err := decodeSize(request.payload)
	if err != nil {
		_ = server.writeError(connection, "invalid_size", "The requested terminal size is invalid.")
		return
	}
	select {
	case server.sessionSlots <- struct{}{}:
		defer func() { <-server.sessionSlots }()
	default:
		_ = server.writeError(connection, "session_limit", "The host shell session limit has been reached.")
		return
	}

	shell, err := server.startShell(size)
	if err != nil {
		_ = server.writeError(connection, "unavailable", "Unable to start the host shell.")
		return
	}
	defer shell.close()
	ready, err := encodeReady(server.info)
	if err != nil || server.write(connection, frameReady, ready) != nil {
		return
	}
	code, message, bridgeErr := server.bridge(ctx, connection, shell)
	shell.close()
	if bridgeErr != nil && code != "peer_disconnected" {
		_ = server.writeError(connection, code, message)
	}
}

func (server *Server) bridge(ctx context.Context, connection *net.UnixConn, shell *shellSession) (string, string, error) {
	bridgeCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		_ = connection.CloseRead()
		workers.Wait()
	}()

	queueCapacity := server.options.OutputQueueSize / server.options.OutputChunkSize
	if queueCapacity < 1 {
		queueCapacity = 1
	}
	output := make(chan streamEvent, queueCapacity)
	input := make(chan frame)
	inputErr := make(chan error, 1)
	ptyInput := make(chan []byte, 4)
	ptyWriteErr := make(chan error, 1)
	workers.Add(3)
	go func() {
		defer workers.Done()
		readPTY(bridgeCtx, shell.pty, server.options.OutputChunkSize, output)
	}()
	go func() {
		defer workers.Done()
		readClient(bridgeCtx, connection, input, inputErr)
	}()
	go func() {
		defer workers.Done()
		writePTYLoop(bridgeCtx, shell.pty, ptyInput, ptyWriteErr)
	}()

	idle := time.NewTimer(server.options.IdleTimeout)
	hard := time.NewTimer(server.options.HardTimeout)
	defer idle.Stop()
	defer hard.Stop()

	processDone := shell.done
	var processExit *int
	outputOpen := true
	for {
		if processExit != nil && !outputOpen {
			if err := server.write(connection, frameExit, encodeExit(*processExit)); err != nil {
				return "peer_disconnected", "The terminal connection was lost.", err
			}
			return "", "", nil
		}
		select {
		case <-bridgeCtx.Done():
			return "peer_disconnected", "The terminal connection was lost.", bridgeCtx.Err()
		case <-idle.C:
			return "idle_timeout", "The host shell closed after being idle.", errors.New("hostagent: idle timeout")
		case <-hard.C:
			return "maximum_duration", "The host shell reached its maximum duration.", errors.New("hostagent: maximum duration")
		case <-processDone:
			code := shell.exitCode()
			processExit = &code
			processDone = nil
		case event := <-output:
			if event.err != nil {
				if errors.Is(event.err, io.EOF) {
					outputOpen = false
					output = nil
					continue
				}
				return "stream_error", "Unable to read from the host shell.", event.err
			}
			if len(event.data) > 0 {
				if err := server.write(connection, frameOutput, event.data); err != nil {
					return "peer_disconnected", "The terminal connection was lost.", err
				}
				resetServerTimer(idle, server.options.IdleTimeout)
			}
		case request := <-input:
			resetServerTimer(idle, server.options.IdleTimeout)
			switch request.typeID {
			case frameInput:
				if len(request.payload) > maxInputPayload {
					return "input_too_large", "The terminal input frame is too large.", ErrProtocol
				}
				if len(request.payload) > 0 {
					select {
					case ptyInput <- request.payload:
					default:
						return "input_backpressure", "The host shell is not accepting input.", errors.New("hostagent: input queue is full")
					}
				}
			case frameResize:
				size, err := decodeSize(request.payload)
				if err != nil {
					return "invalid_size", "The requested terminal size is invalid.", err
				}
				if err := pty.Setsize(shell.pty, &pty.Winsize{Cols: uint16(size.Cols), Rows: uint16(size.Rows)}); err != nil {
					return "stream_error", "Unable to resize the host shell.", err
				}
			case frameClose:
				if len(request.payload) != 0 {
					return "protocol_error", "The close frame is invalid.", ErrProtocol
				}
				return "", "", nil
			default:
				return "protocol_error", "Unexpected client frame.", ErrProtocol
			}
		case err := <-inputErr:
			if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return "", "", nil
			}
			return "peer_disconnected", "The terminal connection was lost.", err
		case err := <-ptyWriteErr:
			return "stream_error", "Unable to write to the host shell.", err
		}
	}
}

func (server *Server) startShell(size Size) (*shellSession, error) {
	command := exec.Command(shellPath, "--login")
	command.Dir = server.home
	command.Env = append([]string(nil), server.env...)
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: uint16(size.Cols), Rows: uint16(size.Rows)})
	if err != nil {
		return nil, fmt.Errorf("hostagent: start Bash PTY: %w", err)
	}
	shell := &shellSession{command: command, pty: terminal, done: make(chan struct{})}
	go shell.wait()
	if err := unix.SetNonblock(int(terminal.Fd()), true); err != nil {
		shell.close()
		return nil, fmt.Errorf("hostagent: configure nonblocking PTY: %w", err)
	}
	return shell, nil
}

func (server *Server) write(connection *net.UnixConn, typeID frameType, payload []byte) error {
	_ = connection.SetWriteDeadline(time.Now().Add(time.Second))
	defer connection.SetWriteDeadline(time.Time{})
	return writeFrame(connection, typeID, payload)
}

func (server *Server) writeError(connection *net.UnixConn, code, message string) error {
	return server.write(connection, frameError, encodeRemoteError(code, message))
}

func (server *Server) addConnection(connection *net.UnixConn) {
	server.connections.Lock()
	server.connections.items[connection] = struct{}{}
	server.connections.Unlock()
}

func (server *Server) removeConnection(connection *net.UnixConn) {
	server.connections.Lock()
	delete(server.connections.items, connection)
	server.connections.Unlock()
}

func (server *Server) closeConnections() {
	server.connections.Lock()
	defer server.connections.Unlock()
	for connection := range server.connections.items {
		_ = connection.Close()
	}
}

type shellSession struct {
	command *exec.Cmd
	pty     *os.File
	done    chan struct{}

	closeOnce sync.Once
	stateMu   sync.RWMutex
	exit      int
}

func (shell *shellSession) wait() {
	_ = shell.command.Wait()
	shell.stateMu.Lock()
	shell.exit = shell.command.ProcessState.ExitCode()
	shell.stateMu.Unlock()
	close(shell.done)
}

func (shell *shellSession) exitCode() int {
	shell.stateMu.RLock()
	defer shell.stateMu.RUnlock()
	return shell.exit
}

func (shell *shellSession) close() {
	shell.closeOnce.Do(func() {
		if !shell.waitFor(0) {
			shell.signal(syscall.SIGHUP)
		}
		if !shell.waitFor(250 * time.Millisecond) {
			shell.signal(syscall.SIGTERM)
		}
		if !shell.waitFor(500 * time.Millisecond) {
			shell.signal(syscall.SIGKILL)
			<-shell.done
		}
		_ = shell.pty.Close()
	})
}

func (shell *shellSession) waitFor(timeout time.Duration) bool {
	if timeout == 0 {
		select {
		case <-shell.done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-shell.done:
		return true
	case <-timer.C:
		return false
	}
}

func (shell *shellSession) signal(signal syscall.Signal) {
	if shell.command.Process == nil {
		return
	}
	_ = syscall.Kill(-shell.command.Process.Pid, signal)
}

type streamEvent struct {
	data []byte
	err  error
}

func readPTY(ctx context.Context, terminal *os.File, chunkSize int, events chan<- streamEvent) {
	buffer := make([]byte, chunkSize)
	fd := int(terminal.Fd())
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR}}
		_, err := unix.Poll(poll, 250)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			sendStreamEvent(ctx, events, streamEvent{err: err})
			return
		}
		if poll[0].Revents == 0 {
			continue
		}
		read, err := unix.Read(fd, buffer)
		if read > 0 {
			data := append([]byte(nil), buffer[:read]...)
			if !sendStreamEvent(ctx, events, streamEvent{data: data}) {
				return
			}
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
			if poll[0].Revents&unix.POLLHUP != 0 && read == 0 {
				sendStreamEvent(ctx, events, streamEvent{err: io.EOF})
				return
			}
			continue
		}
		if errors.Is(err, unix.EIO) || (err == nil && read == 0) {
			sendStreamEvent(ctx, events, streamEvent{err: io.EOF})
			return
		}
		if err != nil {
			sendStreamEvent(ctx, events, streamEvent{err: err})
			return
		}
	}
}

func sendStreamEvent(ctx context.Context, events chan<- streamEvent, event streamEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func readClient(ctx context.Context, connection *net.UnixConn, messages chan<- frame, failures chan<- error) {
	for {
		message, err := readFrame(connection)
		if err != nil {
			select {
			case failures <- err:
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

func writePTY(ctx context.Context, terminal *os.File, data []byte) error {
	fd := int(terminal.Fd())
	for len(data) > 0 {
		written, err := unix.Write(fd, data)
		if written > 0 {
			data = data[written:]
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			if _, pollErr := unix.Poll(poll, 100); pollErr != nil && !errors.Is(pollErr, unix.EINTR) {
				return pollErr
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			continue
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func writePTYLoop(ctx context.Context, terminal *os.File, input <-chan []byte, failures chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-input:
			if err := writePTY(ctx, terminal, data); err != nil {
				select {
				case failures <- err:
				case <-ctx.Done():
				}
				return
			}
		}
	}
}

func resetServerTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func unixPeerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if socketErr != nil {
		return 0, socketErr
	}
	return credentials.Uid, nil
}

func prepareSocketDirectory(socketPath string, uid uint32) error {
	directory := filepath.Dir(socketPath)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("hostagent: create socket directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return fmt.Errorf("hostagent: inspect socket directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uid {
		return errors.New("hostagent: socket directory must belong to the current user")
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("hostagent: socket directory permissions must be 0700 or stricter")
	}
	return nil
}

func removeStaleSocket(socketPath string, uid uint32) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("hostagent: inspect existing socket: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uid || info.Mode()&os.ModeSocket == 0 {
		return errors.New("hostagent: refusing to replace an unsafe socket path")
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("hostagent: remove stale socket: %w", err)
	}
	return nil
}

func safeEnvironment(current *user.User, hostname string) []string {
	runtimeDirectory := "/run/user/" + current.Uid
	return []string{
		"HOME=" + current.HomeDir,
		"USER=" + current.Username,
		"LOGNAME=" + current.Username,
		"SHELL=" + shellPath,
		"HOSTNAME=" + hostname,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"LANG=C.UTF-8",
		"XDG_RUNTIME_DIR=" + runtimeDirectory,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDirectory + "/bus",
	}
}
