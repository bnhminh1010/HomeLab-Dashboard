package nodes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	remoteOpenTimeout   = 10 * time.Second
	remoteOutputBuffers = 16
)

type commandRequest struct {
	nodeID     string
	generation uint64
	result     chan commandResponse
}

type commandResponse struct {
	result CommandResult
	err    error
}

var ErrStreamBackpressure = errors.New("nodes: remote stream output exceeded its buffer")

type Stream struct {
	registry   *Registry
	nodeID     string
	generation uint64
	id         string
	readOnly   bool
	ready      chan error
	output     chan streamChunk
	readyOnce  sync.Once
	closeOnce  sync.Once

	readMu      sync.Mutex
	pending     []byte
	stateMu     sync.RWMutex
	closed      bool
	exit        *int
	terminalErr error
	info        StreamInfo
}

type StreamInfo struct {
	Hostname string
	User     string
	Shell    string
}

type streamChunk struct {
	data []byte
	err  error
}

func (r *Registry) OpenStream(ctx context.Context, nodeID, messageType string, payload any, readOnly bool) (*Stream, error) {
	switch messageType {
	case MessageLogsOpen:
		if !readOnly {
			return nil, errors.New("nodes: log streams must be read-only")
		}
	case MessageExecOpen, MessageHostOpen:
		if readOnly {
			return nil, errors.New("nodes: shell streams cannot be read-only")
		}
	default:
		return nil, ErrProtocolType
	}
	requestID, err := randomRequestID()
	if err != nil {
		return nil, err
	}
	// Bind the stream to the exact connection generation used for its open
	// command. A reconnect must close streams owned by the displaced socket,
	// while leaving streams opened on the replacement connection alone.
	r.lifecycle.RLock()
	r.mu.RLock()
	connection := r.connections[nodeID]
	if connection == nil || r.isOffline(connection, r.now().UTC()) {
		r.mu.RUnlock()
		r.lifecycle.RUnlock()
		return nil, ErrNodeOffline
	}
	generation := connection.generation
	r.mu.RUnlock()
	stream := &Stream{
		registry: r, nodeID: nodeID, generation: generation, id: requestID, readOnly: readOnly,
		ready: make(chan error, 1), output: make(chan streamChunk, remoteOutputBuffers),
	}
	r.mu.Lock()
	if _, exists := r.streams[requestID]; exists {
		r.mu.Unlock()
		r.lifecycle.RUnlock()
		return nil, errors.New("nodes: stream request collision")
	}
	r.streams[requestID] = stream
	r.mu.Unlock()
	err = r.Send(ctx, nodeID, messageType, requestID, payload)
	r.lifecycle.RUnlock()
	if err != nil {
		r.unregisterStream(stream)
		return nil, err
	}
	timer := time.NewTimer(remoteOpenTimeout)
	defer timer.Stop()
	select {
	case err := <-stream.ready:
		if err != nil {
			r.unregisterStream(stream)
			return nil, err
		}
		return stream, nil
	case <-ctx.Done():
		stream.closeWith(ctx.Err())
		return nil, ctx.Err()
	case <-timer.C:
		stream.closeWith(errors.New("nodes: remote stream open timed out"))
		return nil, errors.New("nodes: remote stream open timed out")
	}
}

// Execute sends one bounded, non-interactive container lifecycle command to a
// connected node agent and waits for its typed command.result response.
func (r *Registry) Execute(ctx context.Context, nodeID, messageType string, payload any) (CommandResult, error) {
	switch messageType {
	case MessageContainerRestart, MessageContainerStop:
	default:
		return CommandResult{}, ErrProtocolType
	}
	requestID, err := randomRequestID()
	if err != nil {
		return CommandResult{}, err
	}
	r.lifecycle.RLock()
	r.mu.RLock()
	connection := r.connections[nodeID]
	if connection == nil || r.isOffline(connection, r.now().UTC()) {
		r.mu.RUnlock()
		r.lifecycle.RUnlock()
		return CommandResult{}, ErrNodeOffline
	}
	generation := connection.generation
	r.mu.RUnlock()
	request := &commandRequest{nodeID: nodeID, generation: generation, result: make(chan commandResponse, 1)}
	r.mu.Lock()
	r.commands[requestID] = request
	r.mu.Unlock()
	err = r.Send(ctx, nodeID, messageType, requestID, payload)
	r.lifecycle.RUnlock()
	if err != nil {
		r.unregisterCommand(requestID, request)
		return CommandResult{}, err
	}
	timer := time.NewTimer(remoteOpenTimeout)
	defer timer.Stop()
	select {
	case response := <-request.result:
		if response.err != nil {
			return CommandResult{}, response.err
		}
		if !response.result.OK {
			message := response.result.Message
			if message == "" {
				message = "remote container action was rejected"
			}
			return CommandResult{}, fmt.Errorf("nodes: %s: %s", response.result.Code, message)
		}
		return response.result, nil
	case <-ctx.Done():
		r.unregisterCommand(requestID, request)
		return CommandResult{}, ctx.Err()
	case <-timer.C:
		r.unregisterCommand(requestID, request)
		return CommandResult{}, errors.New("nodes: remote container action timed out")
	}
}

func (stream *Stream) ID() string { return stream.id }

func (stream *Stream) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	stream.readMu.Lock()
	defer stream.readMu.Unlock()
	if len(stream.pending) > 0 {
		read := copy(target, stream.pending)
		stream.pending = stream.pending[read:]
		return read, nil
	}
	var chunk streamChunk
	select {
	case chunk = <-stream.output:
	default:
		stream.stateMu.RLock()
		closed, terminalErr := stream.closed, stream.terminalErr
		stream.stateMu.RUnlock()
		if closed {
			if terminalErr == nil {
				terminalErr = io.EOF
			}
			return 0, terminalErr
		}
		chunk = <-stream.output
	}
	if len(chunk.data) > 0 {
		read := copy(target, chunk.data)
		stream.pending = append(stream.pending[:0], chunk.data[read:]...)
		return read, chunk.err
	}
	if chunk.err == nil {
		return 0, io.EOF
	}
	return 0, chunk.err
}

func (stream *Stream) Write(payload []byte) (int, error) {
	if stream.readOnly {
		return 0, errors.New("nodes: remote log stream is read-only")
	}
	if stream.isClosed() {
		return 0, io.ErrClosedPipe
	}
	if len(payload) > MaxMessageBytes/2 {
		return 0, ErrProtocolSize
	}
	err := stream.registry.Send(context.Background(), stream.nodeID, MessageStreamInput, stream.id, StreamInput{Data: string(payload)})
	if err != nil {
		return 0, err
	}
	return len(payload), nil
}

func (stream *Stream) Resize(ctx context.Context, cols, rows uint16) error {
	if stream.readOnly || cols == 0 || rows == 0 || stream.isClosed() {
		return errors.New("nodes: remote stream cannot be resized")
	}
	return stream.registry.Send(ctx, stream.nodeID, MessageStreamResize, stream.id, StreamResize{Cols: cols, Rows: rows})
}

func (stream *Stream) ExitCode() (int, bool) {
	stream.stateMu.RLock()
	defer stream.stateMu.RUnlock()
	if stream.exit == nil {
		return 0, false
	}
	return *stream.exit, true
}

func (stream *Stream) Info() StreamInfo {
	stream.stateMu.RLock()
	defer stream.stateMu.RUnlock()
	return stream.info
}

func (stream *Stream) Close() error {
	stream.closeOnce.Do(func() {
		stream.stateMu.Lock()
		stream.closed = true
		stream.terminalErr = io.EOF
		stream.stateMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = stream.registry.Send(ctx, stream.nodeID, MessageStreamCancel, stream.id, nil)
		cancel()
		stream.registry.unregisterStream(stream)
		stream.signalTerminal(io.EOF)
	})
	return nil
}

func (stream *Stream) isClosed() bool {
	stream.stateMu.RLock()
	defer stream.stateMu.RUnlock()
	return stream.closed
}

func (stream *Stream) acceptReady(result CommandResult) {
	stream.readyOnce.Do(func() {
		if result.OK {
			stream.stateMu.Lock()
			stream.info = StreamInfo{Hostname: result.Hostname, User: result.User, Shell: result.Shell}
			stream.stateMu.Unlock()
			stream.ready <- nil
			return
		}
		message := result.Message
		if message == "" {
			message = "remote stream was rejected"
		}
		stream.ready <- fmt.Errorf("nodes: %s: %s", result.Code, message)
	})
}

func (stream *Stream) push(payload []byte) {
	if len(payload) == 0 || stream.isClosed() {
		return
	}
	copy := append([]byte(nil), payload...)
	select {
	case stream.output <- streamChunk{data: copy}:
	default:
		stream.closeWith(ErrStreamBackpressure)
	}
}

func (stream *Stream) finish(result CommandResult) {
	stream.stateMu.Lock()
	if result.ExitCode != nil {
		value := *result.ExitCode
		stream.exit = &value
	}
	stream.stateMu.Unlock()
	if result.OK {
		stream.closeWith(io.EOF)
		return
	}
	message := result.Message
	if message == "" {
		message = "remote stream closed"
	}
	stream.closeWith(fmt.Errorf("nodes: %s: %s", result.Code, message))
}

func (stream *Stream) closeWith(err error) {
	stream.closeOnce.Do(func() {
		stream.stateMu.Lock()
		stream.closed = true
		stream.terminalErr = err
		stream.stateMu.Unlock()
		stream.readyOnce.Do(func() { stream.ready <- err })
		stream.registry.unregisterStream(stream)
		stream.signalTerminal(err)
	})
}

func (stream *Stream) signalTerminal(err error) {
	chunk := streamChunk{err: err}
	select {
	case stream.output <- chunk:
		return
	default:
	}
	// Bound memory and guarantee that a reader eventually observes closure.
	select {
	case <-stream.output:
	default:
	}
	select {
	case stream.output <- chunk:
	default:
	}
}

func (r *Registry) closeNodeCommands(nodeID string, err error) {
	r.closeNodeCommandsGeneration(nodeID, 0, err)
}

func (r *Registry) closeNodeCommandsGeneration(nodeID string, generation uint64, err error) {
	r.mu.Lock()
	requests := make([]*commandRequest, 0)
	for requestID, request := range r.commands {
		if request.nodeID == nodeID && (generation == 0 || request.generation == generation) {
			delete(r.commands, requestID)
			requests = append(requests, request)
		}
	}
	r.mu.Unlock()
	for _, request := range requests {
		request.result <- commandResponse{err: err}
	}
}

func (r *Registry) deliverCommandResult(requestID string, result CommandResult) {
	if request := r.takeCommand(requestID); request != nil {
		request.result <- commandResponse{result: result}
		return
	}
	if stream := r.lookupStream(requestID); stream != nil {
		stream.acceptReady(result)
	}
}

func (r *Registry) takeCommand(requestID string) *commandRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	request := r.commands[requestID]
	delete(r.commands, requestID)
	return request
}

func (r *Registry) unregisterCommand(requestID string, expected *commandRequest) {
	r.mu.Lock()
	if r.commands[requestID] == expected {
		delete(r.commands, requestID)
	}
	r.mu.Unlock()
}

func (r *Registry) deliverStreamData(requestID string, payload []byte) {
	if stream := r.lookupStream(requestID); stream != nil {
		stream.push(payload)
	}
}

func (r *Registry) closeRemoteStream(requestID string, result CommandResult) {
	if stream := r.lookupStream(requestID); stream != nil {
		stream.finish(result)
	}
}

func (r *Registry) lookupStream(requestID string) *Stream {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.streams[requestID]
}

func (r *Registry) unregisterStream(stream *Stream) {
	r.mu.Lock()
	if r.streams[stream.id] == stream {
		delete(r.streams, stream.id)
	}
	r.mu.Unlock()
}

func (r *Registry) closeNodeStreams(nodeID string, err error) {
	r.closeNodeStreamsGeneration(nodeID, 0, err)
}

func (r *Registry) closeNodeStreamsGeneration(nodeID string, generation uint64, err error) {
	r.mu.RLock()
	streams := make([]*Stream, 0)
	for _, stream := range r.streams {
		if stream.nodeID == nodeID && (generation == 0 || stream.generation == generation) {
			streams = append(streams, stream)
		}
	}
	r.mu.RUnlock()
	for _, stream := range streams {
		stream.closeWith(err)
	}
}

func randomRequestID() (string, error) {
	buffer := make([]byte, 18)
	if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
		return "", fmt.Errorf("nodes: generate stream request id: %w", err)
	}
	return "stream_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}
