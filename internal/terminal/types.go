package terminal

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/podman"
)

var (
	ErrUnauthorized     = errors.New("terminal: administrator access is required")
	ErrNotFound         = errors.New("terminal: session not found")
	ErrSessionClaimed   = errors.New("terminal: session is already connected")
	ErrSessionExpired   = errors.New("terminal: session expired")
	ErrExecLimit        = errors.New("terminal: interactive exec limit reached")
	ErrSessionLimit     = errors.New("terminal: session limit reached")
	ErrInvalidRequest   = errors.New("terminal: invalid request")
	ErrInputTooLarge    = errors.New("terminal: input frame exceeds limit")
	ErrReadOnly         = errors.New("terminal: log sessions are read-only")
	ErrIdleTimeout      = errors.New("terminal: idle timeout")
	ErrHardTimeout      = errors.New("terminal: maximum duration reached")
	ErrPeerDisconnected = errors.New("terminal: peer disconnected")
)

type Mode string

const (
	ModeLogs Mode = "logs"
	ModeExec Mode = "exec"
)

type CreateRequest struct {
	Mode        Mode   `json:"mode"`
	ContainerID string `json:"containerId"`
	Tail        uint   `json:"tail,omitempty"`
	Follow      bool   `json:"follow,omitempty"`
	Cols        uint   `json:"cols,omitempty"`
	Rows        uint   `json:"rows,omitempty"`
}

type Session struct {
	ID          string    `json:"id"`
	Mode        Mode      `json:"mode"`
	ContainerID string    `json:"containerId"`
	User        string    `json:"-"`
	ReadOnly    bool      `json:"readOnly"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`

	tail   uint
	follow bool
	cols   uint
	rows   uint
	state  sessionState
	execID string
}

type sessionState uint8

const (
	sessionPending sessionState = iota
	sessionRunning
)

type Backend interface {
	Logs(context.Context, string, podman.LogsOptions) (io.ReadCloser, error)
	CreateShellExec(context.Context, string, podman.Shell, podman.TerminalSize) (string, error)
	StartExec(context.Context, string, podman.TerminalSize) (io.ReadWriteCloser, error)
	ResizeExec(context.Context, string, podman.TerminalSize) error
	RemoveExec(context.Context, string, bool) error
}

// Peer is a WebSocket-neutral boundary. The HTTP layer translates its chosen
// WebSocket library into typed messages without exposing commands or argv.
type Peer interface {
	Read(context.Context) (ClientMessage, error)
	WriteBinary(context.Context, []byte) error
	WriteControl(context.Context, Control) error
	Close() error
}

type ClientMessageType string

const (
	ClientInput  ClientMessageType = "input"
	ClientResize ClientMessageType = "resize"
	ClientClose  ClientMessageType = "close"
)

type ClientMessage struct {
	Type ClientMessageType `json:"type"`
	Data []byte            `json:"-"`
	Cols uint              `json:"cols,omitempty"`
	Rows uint              `json:"rows,omitempty"`
}

type ControlType string

const (
	ControlReady ControlType = "ready"
	ControlExit  ControlType = "exit"
	ControlError ControlType = "error"
)

type Control struct {
	Type     ControlType `json:"type"`
	ReadOnly bool        `json:"readOnly,omitempty"`
	ExitCode *int        `json:"exitCode,omitempty"`
	Code     string      `json:"code,omitempty"`
	Message  string      `json:"message,omitempty"`
}

type ManagerOptions struct {
	PendingTTL         time.Duration
	IdleTimeout        time.Duration
	HardTimeout        time.Duration
	MaxExecPerUser     int
	MaxExecGlobal      int
	MaxSessionsPerUser int
	MaxSessionsGlobal  int
	MaxInputFrame      int
	OutputChunkSize    int
	OutputQueueSize    int
	Random             io.Reader
	Now                func() time.Time
}
