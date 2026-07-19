package hostagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidSize  = errors.New("hostagent: invalid terminal size")
	ErrUnavailable  = errors.New("hostagent: unavailable")
	ErrSessionLimit = errors.New("hostagent: session limit reached")
	ErrProtocol     = errors.New("hostagent: protocol error")
	ErrIdleTimeout  = errors.New("hostagent: idle timeout")
	ErrHardTimeout  = errors.New("hostagent: maximum duration reached")
)

type Size struct {
	Cols uint
	Rows uint
}

type Info struct {
	Hostname string
	User     string
	Shell    string
}

type Session interface {
	io.ReadWriteCloser
	Resize(context.Context, Size) error
	Info() Info
	ExitCode() (int, bool)
}

type RemoteError struct {
	Code    string
	Message string
}

func (err *RemoteError) Error() string {
	if err.Message == "" {
		return "hostagent: " + err.Code
	}
	return "hostagent: " + err.Code + ": " + err.Message
}

func (err *RemoteError) Is(target error) bool {
	switch err.Code {
	case "session_limit":
		return target == ErrSessionLimit
	case "invalid_size":
		return target == ErrInvalidSize
	case "protocol_error":
		return target == ErrProtocol
	case "unavailable":
		return target == ErrUnavailable
	case "idle_timeout":
		return target == ErrIdleTimeout
	case "maximum_duration":
		return target == ErrHardTimeout
	default:
		return false
	}
}

type Client struct {
	socketPath string
}

func NewClient(socketPath string) (*Client, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" || !filepath.IsAbs(socketPath) {
		return nil, fmt.Errorf("hostagent: socket path must be absolute")
	}
	return &Client{socketPath: socketPath}, nil
}

func (client *Client) Probe(ctx context.Context) error {
	connection, err := client.dial(ctx)
	if err != nil {
		return err
	}
	return connection.Close()
}

func (client *Client) Open(ctx context.Context, size Size) (Session, error) {
	payload, err := encodeSize(size)
	if err != nil {
		return nil, err
	}
	connection, err := client.dial(ctx)
	if err != nil {
		return nil, err
	}
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = connection.Close()
		close(cancelDone)
	})
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if err := writeFrame(connection, frameOpen, payload); err != nil {
		_ = connection.Close()
		return nil, clientError(ctx, "open host shell", err)
	}
	response, err := readFrame(connection)
	if err != nil {
		_ = connection.Close()
		return nil, clientError(ctx, "read host shell response", err)
	}
	if response.typeID == frameError {
		_ = connection.Close()
		return nil, decodeRemoteError(response.payload)
	}
	if response.typeID != frameReady {
		_ = connection.Close()
		return nil, fmt.Errorf("%w: expected ready frame", ErrProtocol)
	}
	info, err := decodeReady(response.payload)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if !stop() {
		<-cancelDone
		_ = connection.Close()
		return nil, ctx.Err()
	}
	_ = connection.SetDeadline(time.Time{})
	reader, writer := io.Pipe()
	session := &clientSession{
		connection: connection,
		reader:     reader,
		writer:     writer,
		info:       info,
	}
	go session.readLoop()
	return session, nil
}

func (client *Client) dial(ctx context.Context) (*net.UnixConn, error) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", client.socketPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, fmt.Errorf("%w: unexpected socket type", ErrProtocol)
	}
	return unixConnection, nil
}

func clientError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("hostagent: %s: %w", operation, err)
}

type clientSession struct {
	connection *net.UnixConn
	reader     *io.PipeReader
	writer     *io.PipeWriter
	info       Info

	writeMu sync.Mutex
	stateMu sync.RWMutex
	closed  bool
	exit    int
	exited  bool
}

func (session *clientSession) Read(target []byte) (int, error) {
	return session.reader.Read(target)
}

func (session *clientSession) Write(data []byte) (int, error) {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if session.isClosed() {
		return 0, io.ErrClosedPipe
	}
	written := 0
	for len(data) > 0 {
		chunkSize := min(len(data), maxInputPayload)
		if err := session.writeLocked(context.Background(), frameInput, data[:chunkSize]); err != nil {
			return written, err
		}
		written += chunkSize
		data = data[chunkSize:]
	}
	return written, nil
}

func (session *clientSession) Resize(ctx context.Context, size Size) error {
	payload, err := encodeSize(size)
	if err != nil {
		return err
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if session.isClosed() {
		return io.ErrClosedPipe
	}
	return session.writeLocked(ctx, frameResize, payload)
}

func (session *clientSession) Info() Info {
	return session.info
}

func (session *clientSession) ExitCode() (int, bool) {
	session.stateMu.RLock()
	defer session.stateMu.RUnlock()
	return session.exit, session.exited
}

func (session *clientSession) Close() error {
	session.stateMu.Lock()
	if session.closed {
		session.stateMu.Unlock()
		return nil
	}
	session.closed = true
	session.stateMu.Unlock()
	if session.writeMu.TryLock() {
		_ = session.connection.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
		_ = writeFrame(session.connection, frameClose, nil)
		session.writeMu.Unlock()
	}
	err := session.connection.Close()
	_ = session.writer.Close()
	return err
}

func (session *clientSession) readLoop() {
	defer session.connection.Close()
	for {
		message, err := readFrame(session.connection)
		if err != nil {
			if !session.isClosed() {
				session.markClosed()
				_ = session.writer.CloseWithError(fmt.Errorf("hostagent: read stream: %w", err))
			} else {
				_ = session.writer.Close()
			}
			return
		}
		switch message.typeID {
		case frameOutput:
			if len(message.payload) == 0 {
				continue
			}
			if _, err := session.writer.Write(message.payload); err != nil {
				session.markClosed()
				_ = session.connection.Close()
				return
			}
		case frameExit:
			code, err := decodeExit(message.payload)
			if err != nil {
				session.markClosed()
				_ = session.writer.CloseWithError(err)
				_ = session.connection.Close()
				return
			}
			session.stateMu.Lock()
			session.exit = code
			session.exited = true
			session.closed = true
			session.stateMu.Unlock()
			_ = session.writer.Close()
			_ = session.connection.Close()
			return
		case frameError:
			remote := decodeRemoteError(message.payload)
			session.markClosed()
			_ = session.writer.CloseWithError(remote)
			_ = session.connection.Close()
			return
		default:
			session.markClosed()
			_ = session.writer.CloseWithError(fmt.Errorf("%w: unexpected frame type %d", ErrProtocol, message.typeID))
			_ = session.connection.Close()
			return
		}
	}
}

func (session *clientSession) writeLocked(ctx context.Context, typeID frameType, payload []byte) error {
	deadline := time.Now().Add(10 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	stop := context.AfterFunc(ctx, func() { _ = session.connection.SetWriteDeadline(time.Now()) })
	defer stop()
	defer session.connection.SetWriteDeadline(time.Time{})
	_ = session.connection.SetWriteDeadline(deadline)
	if err := writeFrame(session.connection, typeID, payload); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

func (session *clientSession) isClosed() bool {
	session.stateMu.RLock()
	defer session.stateMu.RUnlock()
	return session.closed
}

func (session *clientSession) markClosed() {
	session.stateMu.Lock()
	session.closed = true
	session.stateMu.Unlock()
}

func validSize(size Size) bool {
	return size.Cols >= 20 && size.Cols <= 300 && size.Rows >= 5 && size.Rows <= 100
}
