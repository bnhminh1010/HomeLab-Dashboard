package nodes

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

var (
	ErrSequenceStale      = errors.New("nodes: stale or out-of-order sequence")
	ErrTimestampStale     = errors.New("nodes: message timestamp is outside the accepted window")
	ErrNodeOffline        = errors.New("nodes: node is offline")
	ErrConnectionReplaced = errors.New("nodes: agent connection was replaced")
)

type Sender interface {
	Send(context.Context, Message) error
	Close() error
}

type RegistryOptions struct {
	Now              func() time.Time
	OfflineAfter     time.Duration
	TimestampWindow  time.Duration
	OnSnapshot       func(context.Context, string, model.SnapshotEnvelope) error
	OnConnectionOpen func(string)
	OnConnectionLost func(string)
}

type NodeState struct {
	Node         Node                    `json:"node"`
	Online       bool                    `json:"online"`
	Stale        bool                    `json:"stale"`
	LastSeenAt   *time.Time              `json:"lastSeenAt,omitempty"`
	LastSequence uint64                  `json:"lastSequence"`
	Snapshot     *model.SnapshotEnvelope `json:"snapshot,omitempty"`
	AgentVersion string                  `json:"agentVersion,omitempty"`
}

type nodeConnection struct {
	sender       Sender
	generation   uint64
	lastSequence uint64
	lastSeen     time.Time
	agentVersion string
}

// cachedNodeState is intentionally separate from nodeConnection. A live
// connection is disposable; the latest validated observation remains useful
// while an agent reconnects or is offline.
type cachedNodeState struct {
	lastSequence       uint64
	lastSeen           time.Time
	snapshot           *model.SnapshotEnvelope
	snapshotGeneration uint64
	agentVersion       string
}

type Registry struct {
	service         *Service
	now             func() time.Time
	offlineAfter    time.Duration
	timestampWindow time.Duration
	onSnapshot      func(context.Context, string, model.SnapshotEnvelope) error
	onOpen          func(string)
	onLost          func(string)

	// lifecycle serializes a connection replacement with the side effects of a
	// decoded frame. Decode happens before taking the read lock so a reconnect
	// can replace a slow reader; the frame must then pass the generation check
	// again before publishing anything.
	lifecycle sync.RWMutex

	mu          sync.RWMutex
	connections map[string]*nodeConnection
	cache       map[string]*cachedNodeState
	nextGen     uint64
	commandSeq  uint64
	streams     map[string]*Stream
	commands    map[string]*commandRequest

	// afterDecode is a deterministic test seam for the replacement-during-
	// decode regression. Production registries leave it nil.
	afterDecode func(Message)
}

func NewRegistry(service *Service, options RegistryOptions) (*Registry, error) {
	if service == nil {
		return nil, errors.New("nodes: enrollment service is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.OfflineAfter == 0 {
		options.OfflineAfter = 30 * time.Second
	}
	if options.TimestampWindow == 0 {
		options.TimestampWindow = 2 * time.Minute
	}
	if options.OfflineAfter < 10*time.Second || options.TimestampWindow < 30*time.Second {
		return nil, errors.New("nodes: invalid registry timing policy")
	}
	return &Registry{
		service: service, now: options.Now, offlineAfter: options.OfflineAfter,
		timestampWindow: options.TimestampWindow, onSnapshot: options.OnSnapshot,
		onOpen: options.OnConnectionOpen, onLost: options.OnConnectionLost, connections: make(map[string]*nodeConnection),
		cache: make(map[string]*cachedNodeState), streams: make(map[string]*Stream), commands: make(map[string]*commandRequest),
	}, nil
}

// Attach registers one authenticated outbound agent connection. A reconnect
// atomically replaces and closes the prior connection for the same node.
func (r *Registry) Attach(ctx context.Context, nodeID, credential string, sender Sender) (Node, uint64, error) {
	if sender == nil {
		return Node{}, 0, errors.New("nodes: sender is required")
	}
	node, err := r.service.Authenticate(ctx, nodeID, credential)
	if err != nil {
		return Node{}, 0, err
	}
	now := r.now().UTC()
	r.lifecycle.Lock()
	r.mu.Lock()
	r.nextGen++
	generation := r.nextGen
	previous := r.connections[node.ID]
	r.connections[node.ID] = &nodeConnection{sender: sender, generation: generation, lastSeen: now}
	r.mu.Unlock()
	r.lifecycle.Unlock()
	if previous != nil {
		_ = previous.sender.Close()
		r.closeNodeStreamsGeneration(node.ID, previous.generation, ErrConnectionReplaced)
		r.closeNodeCommandsGeneration(node.ID, previous.generation, ErrConnectionReplaced)
	}
	_ = r.service.Touch(ctx, node.ID)
	if r.onOpen != nil {
		r.onOpen(node.ID)
	}
	return node, generation, nil
}

func (r *Registry) Detach(nodeID string, generation uint64) {
	r.lifecycle.Lock()
	r.mu.Lock()
	connection, ok := r.connections[nodeID]
	if ok && connection.generation == generation {
		delete(r.connections, nodeID)
	}
	r.mu.Unlock()
	r.lifecycle.Unlock()
	if ok && connection.generation == generation && r.onLost != nil {
		r.onLost(nodeID)
	}
	if ok && connection.generation == generation {
		r.closeNodeStreams(nodeID, ErrNodeOffline)
		r.closeNodeCommandsGeneration(nodeID, generation, ErrNodeOffline)
	}
}

func (r *Registry) Revoke(ctx context.Context, nodeID string) error {
	r.lifecycle.Lock()
	if err := r.service.Revoke(ctx, nodeID); err != nil {
		r.lifecycle.Unlock()
		return err
	}
	r.mu.Lock()
	connection := r.connections[nodeID]
	delete(r.connections, nodeID)
	delete(r.cache, nodeID)
	r.mu.Unlock()
	r.lifecycle.Unlock()
	if connection != nil {
		_ = connection.sender.Close()
	}
	r.closeNodeStreams(nodeID, ErrNodeUnauthorized)
	r.closeNodeCommands(nodeID, ErrNodeUnauthorized)
	return nil
}

func (r *Registry) Accept(ctx context.Context, authenticatedNodeID string, message Message) error {
	return r.accept(ctx, authenticatedNodeID, 0, message)
}

// AcceptConnection binds an incoming frame to the exact Attach generation
// that authenticated its WebSocket. A displaced socket must not be able to
// advance sequence numbers or publish data into its replacement connection.
func (r *Registry) AcceptConnection(ctx context.Context, authenticatedNodeID string, generation uint64, message Message) error {
	if generation == 0 {
		return ErrConnectionReplaced
	}
	return r.accept(ctx, authenticatedNodeID, generation, message)
}

func (r *Registry) accept(ctx context.Context, authenticatedNodeID string, generation uint64, message Message) error {
	if message.NodeID != authenticatedNodeID {
		return ErrProtocolNode
	}
	now := r.now().UTC()
	if message.Timestamp.Before(now.Add(-r.timestampWindow)) || message.Timestamp.After(now.Add(r.timestampWindow)) {
		return ErrTimestampStale
	}
	r.mu.RLock()
	connection, ok := r.connections[authenticatedNodeID]
	if !ok {
		r.mu.RUnlock()
		return ErrNodeOffline
	}
	if generation != 0 && connection.generation != generation {
		r.mu.RUnlock()
		return ErrConnectionReplaced
	}
	if message.Sequence <= connection.lastSequence {
		r.mu.RUnlock()
		return ErrSequenceStale
	}
	r.mu.RUnlock()

	var (
		heartbeat  Heartbeat
		snapshot   model.SnapshotEnvelope
		result     CommandResult
		streamData []byte
	)

	switch message.Type {
	case MessageHeartbeat:
		decoded, err := DecodePayload[Heartbeat](message)
		if err != nil {
			return err
		}
		heartbeat = decoded
	case MessageMetricsSnapshot:
		payload, err := DecodePayload[MetricsPayload](message)
		if err != nil {
			return err
		}
		if payload.Snapshot.Type != "metrics.snapshot" || payload.Snapshot.Version != 1 {
			return errors.New("nodes: invalid metrics snapshot envelope")
		}
		if payload.Snapshot.CollectedAt.IsZero() ||
			payload.Snapshot.CollectedAt.Before(now.Add(-r.timestampWindow)) ||
			payload.Snapshot.CollectedAt.After(now.Add(r.timestampWindow)) {
			return ErrTimestampStale
		}
		snapshot = payload.Snapshot
	case MessageCommandResult:
		decoded, err := DecodePayload[CommandResult](message)
		if err != nil {
			return err
		}
		result = decoded
	case MessageStreamData:
		payload, err := DecodePayload[StreamData](message)
		if err != nil {
			return err
		}
		streamData = []byte(payload.Data)
	case MessageStreamClosed:
		decoded, err := DecodePayload[CommandResult](message)
		if err != nil {
			return err
		}
		result = decoded
	default:
		return fmt.Errorf("%w: agent cannot send %s", ErrProtocolType, message.Type)
	}

	if r.afterDecode != nil {
		r.afterDecode(message)
	}

	// A reconnect may have replaced connection while the payload was decoded.
	// Hold the lifecycle read lock from the second generation check through all
	// callbacks, so replacement cannot slip between validation and publication.
	r.lifecycle.RLock()
	defer r.lifecycle.RUnlock()
	if err := r.commitFrame(authenticatedNodeID, generation, connection, message, now, heartbeat, snapshot); err != nil {
		return err
	}

	switch message.Type {
	case MessageHeartbeat:
		return r.service.Touch(ctx, authenticatedNodeID)
	case MessageMetricsSnapshot:
		if r.onSnapshot != nil {
			return r.onSnapshot(ctx, authenticatedNodeID, snapshot)
		}
	case MessageCommandResult:
		r.deliverCommandResult(message.RequestID, result)
	case MessageStreamData:
		r.deliverStreamData(message.RequestID, streamData)
	case MessageStreamClosed:
		r.closeRemoteStream(message.RequestID, result)
	}
	return nil
}

func (r *Registry) commitFrame(
	nodeID string,
	generation uint64,
	expected *nodeConnection,
	message Message,
	now time.Time,
	heartbeat Heartbeat,
	snapshot model.SnapshotEnvelope,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.connections[nodeID]
	if current == nil {
		return ErrNodeOffline
	}
	if current != expected || (generation != 0 && current.generation != generation) {
		return ErrConnectionReplaced
	}
	if message.Sequence <= current.lastSequence {
		return ErrSequenceStale
	}

	current.lastSequence = message.Sequence
	current.lastSeen = now
	cached := r.cache[nodeID]
	if cached == nil {
		cached = &cachedNodeState{}
		r.cache[nodeID] = cached
	}
	cached.lastSequence = message.Sequence
	cached.lastSeen = now

	switch message.Type {
	case MessageHeartbeat:
		current.agentVersion = heartbeat.AgentVersion
		cached.agentVersion = heartbeat.AgentVersion
	case MessageMetricsSnapshot:
		copy := snapshot
		cached.snapshot = &copy
		cached.snapshotGeneration = current.generation
	}
	return nil
}

func (r *Registry) Send(ctx context.Context, nodeID, messageType, requestID string, payload any) error {
	switch messageType {
	case MessageLogsOpen, MessageLogsCancel, MessageExecOpen, MessageHostOpen,
		MessageStreamInput, MessageStreamResize, MessageStreamCancel,
		MessageContainerRestart, MessageContainerStop:
	default:
		return ErrProtocolType
	}
	r.mu.Lock()
	connection, ok := r.connections[nodeID]
	if !ok || r.isOffline(connection, r.now().UTC()) {
		r.mu.Unlock()
		return ErrNodeOffline
	}
	r.commandSeq++
	message, err := NewMessage(nodeID, messageType, r.commandSeq, r.now().UTC(), requestID, payload)
	sender := connection.sender
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return sender.Send(ctx, message)
}

func (r *Registry) Probe(nodeID string) error {
	r.mu.RLock()
	connection, ok := r.connections[nodeID]
	if !ok || r.isOffline(connection, r.now().UTC()) {
		r.mu.RUnlock()
		return ErrNodeOffline
	}
	r.mu.RUnlock()
	return nil
}

func (r *Registry) States(ctx context.Context) ([]NodeState, error) {
	nodes, err := r.service.List(ctx)
	if err != nil {
		return nil, err
	}
	now := r.now().UTC()
	r.mu.RLock()
	defer r.mu.RUnlock()
	states := make([]NodeState, 0, len(nodes))
	for _, node := range nodes {
		state := NodeState{Node: node, LastSeenAt: node.LastSeenAt}
		cached := r.cache[node.ID]
		if cached != nil {
			lastSeen := cached.lastSeen
			state.LastSeenAt = &lastSeen
			state.LastSequence = cached.lastSequence
			state.AgentVersion = cached.agentVersion
			if cached.snapshot != nil {
				copy := *cached.snapshot
				state.Snapshot = &copy
				state.Stale = true
			}
		}
		if connection := r.connections[node.ID]; connection != nil {
			lastSeen := connection.lastSeen
			state.LastSeenAt = &lastSeen
			state.Online = !r.isOffline(connection, now)
			state.LastSequence = connection.lastSequence
			state.AgentVersion = connection.agentVersion
			if state.Snapshot != nil && cached != nil {
				state.Stale = !state.Online || cached.snapshotGeneration != connection.generation
			}
		}
		states = append(states, state)
	}
	return states, nil
}

func (r *Registry) isOffline(connection *nodeConnection, now time.Time) bool {
	return now.Sub(connection.lastSeen) >= r.offlineAfter
}
