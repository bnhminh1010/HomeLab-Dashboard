package nodeagent

import (
	"errors"
	"fmt"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
)

var (
	ErrInboundNode      = errors.New("node agent: command node does not match credentials")
	ErrInboundSequence  = errors.New("node agent: stale or out-of-order command")
	ErrInboundTimestamp = errors.New("node agent: command timestamp is outside the accepted window")
	ErrCommandType      = errors.New("node agent: unsupported command type")
	ErrBackpressure     = errors.New("node agent: outbound queue is full")
)

type MessageValidator struct {
	nodeID string
	now    func() time.Time
	window time.Duration
	last   uint64
}

func NewMessageValidator(nodeID string, now func() time.Time) (*MessageValidator, error) {
	if nodeID == "" {
		return nil, errors.New("node agent: validator node ID is required")
	}
	if now == nil {
		now = time.Now
	}
	return &MessageValidator{nodeID: nodeID, now: now, window: 2 * time.Minute}, nil
}

func (validator *MessageValidator) Validate(message nodes.Message) error {
	if message.Version != nodes.ProtocolVersion {
		return nodes.ErrProtocolVersion
	}
	if message.NodeID != validator.nodeID {
		return ErrInboundNode
	}
	if message.RequestID == "" || len(message.RequestID) > 128 {
		return errors.New("node agent: command request ID is required")
	}
	if !agentCommandType(message.Type) {
		return fmt.Errorf("%w: %s", ErrCommandType, message.Type)
	}
	now := validator.now().UTC()
	if message.Timestamp.Before(now.Add(-validator.window)) || message.Timestamp.After(now.Add(validator.window)) {
		return ErrInboundTimestamp
	}
	if message.Sequence == 0 || message.Sequence <= validator.last {
		return ErrInboundSequence
	}
	validator.last = message.Sequence
	return nil
}

func agentCommandType(messageType string) bool {
	switch messageType {
	case nodes.MessageLogsOpen, nodes.MessageLogsCancel, nodes.MessageExecOpen,
		nodes.MessageHostOpen, nodes.MessageStreamInput, nodes.MessageStreamResize,
		nodes.MessageStreamCancel, nodes.MessageContainerRestart, nodes.MessageContainerStop:
		return true
	default:
		return false
	}
}
