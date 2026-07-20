package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/model"
)

const (
	ProtocolVersion = 1
	MaxMessageBytes = 256 << 10
)

const (
	MessageHeartbeat       = "agent.heartbeat"
	MessageMetricsSnapshot = "metrics.snapshot"
	MessageCommandResult   = "command.result"
	MessageStreamData      = "stream.data"
	MessageStreamClosed    = "stream.closed"

	MessageLogsOpen     = "logs.open"
	MessageLogsCancel   = "logs.cancel"
	MessageExecOpen     = "exec.open"
	MessageHostOpen     = "host.open"
	MessageStreamInput  = "stream.input"
	MessageStreamResize = "stream.resize"
	MessageStreamCancel = "stream.cancel"
)

var (
	ErrProtocolVersion = errors.New("nodes: unsupported protocol version")
	ErrProtocolType    = errors.New("nodes: unsupported message type")
	ErrProtocolNode    = errors.New("nodes: protocol node mismatch")
	ErrProtocolSize    = errors.New("nodes: protocol message is too large")
)

type Message struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	NodeID    string          `json:"nodeId"`
	Sequence  uint64          `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	RequestID string          `json:"requestId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type Heartbeat struct {
	AgentVersion string `json:"agentVersion"`
	Hostname     string `json:"hostname"`
	Uptime       uint64 `json:"uptimeSeconds,omitempty"`
}

type MetricsPayload struct {
	Snapshot model.SnapshotEnvelope `json:"snapshot"`
}

type LogsOpen struct {
	ContainerID string `json:"containerId"`
	Tail        int    `json:"tail"`
	Since       string `json:"since,omitempty"`
	Follow      bool   `json:"follow"`
	Timestamps  bool   `json:"timestamps"`
}

type ShellOpen struct {
	ContainerID string `json:"containerId,omitempty"`
	Cols        uint16 `json:"cols"`
	Rows        uint16 `json:"rows"`
}

type StreamInput struct {
	Data string `json:"data"`
}

type StreamResize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type StreamData struct {
	Data string `json:"data"`
}

type CommandResult struct {
	OK       bool   `json:"ok"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	User     string `json:"user,omitempty"`
	Shell    string `json:"shell,omitempty"`
}

func NewMessage(nodeID, messageType string, sequence uint64, timestamp time.Time, requestID string, payload any) (Message, error) {
	if !knownMessageType(messageType) {
		return Message{}, ErrProtocolType
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("nodes: encode message payload: %w", err)
	}
	if string(encoded) == "null" {
		encoded = nil
	}
	return Message{
		Version: ProtocolVersion, Type: messageType, NodeID: nodeID,
		Sequence: sequence, Timestamp: timestamp.UTC(), RequestID: requestID, Payload: encoded,
	}, nil
}

func DecodeMessage(reader io.Reader) (Message, error) {
	limited := io.LimitReader(reader, MaxMessageBytes+1)
	contents, err := io.ReadAll(limited)
	if err != nil {
		return Message{}, fmt.Errorf("nodes: read protocol message: %w", err)
	}
	if len(contents) > MaxMessageBytes {
		return Message{}, ErrProtocolSize
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var message Message
	if err := decoder.Decode(&message); err != nil {
		return Message{}, fmt.Errorf("nodes: decode protocol message: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Message{}, errors.New("nodes: protocol message must contain one JSON value")
	}
	if message.Version != ProtocolVersion {
		return Message{}, ErrProtocolVersion
	}
	if !knownMessageType(message.Type) {
		return Message{}, ErrProtocolType
	}
	if message.NodeID == "" || message.Sequence == 0 || message.Timestamp.IsZero() {
		return Message{}, errors.New("nodes: protocol message is missing required metadata")
	}
	return message, nil
}

func DecodePayload[T any](message Message) (T, error) {
	var result T
	if len(message.Payload) == 0 {
		return result, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(message.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("nodes: decode %s payload: %w", message.Type, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return result, fmt.Errorf("nodes: decode %s payload: trailing JSON", message.Type)
	}
	return result, nil
}

func knownMessageType(messageType string) bool {
	switch messageType {
	case MessageHeartbeat, MessageMetricsSnapshot, MessageCommandResult,
		MessageStreamData, MessageStreamClosed, MessageLogsOpen, MessageLogsCancel,
		MessageExecOpen, MessageHostOpen, MessageStreamInput, MessageStreamResize,
		MessageStreamCancel:
		return true
	default:
		return false
	}
}
