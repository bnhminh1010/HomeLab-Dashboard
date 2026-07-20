package terminal

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/podman"
)

var (
	ErrUnauthorized      = errors.New("terminal: administrator access is required")
	ErrNotFound          = errors.New("terminal: session not found")
	ErrSessionClaimed    = errors.New("terminal: session is already connected")
	ErrSessionExpired    = errors.New("terminal: session expired")
	ErrExecLimit         = errors.New("terminal: interactive exec limit reached")
	ErrHostLimit         = errors.New("terminal: host shell limit reached")
	ErrHostUnavailable   = errors.New("terminal: host shell agent is unavailable")
	ErrRemoteUnavailable = errors.New("terminal: remote node is unavailable")
	ErrSessionLimit      = errors.New("terminal: session limit reached")
	ErrInvalidRequest    = errors.New("terminal: invalid request")
	ErrInputTooLarge     = errors.New("terminal: input frame exceeds limit")
	ErrReadOnly          = errors.New("terminal: log sessions are read-only")
	ErrIdleTimeout       = errors.New("terminal: idle timeout")
	ErrHardTimeout       = errors.New("terminal: maximum duration reached")
	ErrPeerDisconnected  = errors.New("terminal: peer disconnected")
)

type Mode string

const (
	ModeLogs Mode = "logs"
	ModeExec Mode = "exec"
	ModeHost Mode = "host"
)

type CreateRequest struct {
	Mode        Mode   `json:"mode"`
	NodeID      string `json:"nodeId,omitempty"`
	ContainerID string `json:"containerId"`
	Tail        uint   `json:"tail,omitempty"`
	Follow      bool   `json:"follow,omitempty"`
	Since       string `json:"since,omitempty"`
	Timestamps  bool   `json:"timestamps,omitempty"`
	Cols        uint   `json:"cols,omitempty"`
	Rows        uint   `json:"rows,omitempty"`
}

type HostCreateRequest struct {
	NodeID string `json:"nodeId,omitempty"`
	Cols   uint   `json:"cols,omitempty"`
	Rows   uint   `json:"rows,omitempty"`
}

type Session struct {
	ID          string    `json:"id"`
	Mode        Mode      `json:"mode"`
	ContainerID string    `json:"containerId,omitempty"`
	NodeID      string    `json:"nodeId"`
	User        string    `json:"-"`
	ReadOnly    bool      `json:"readOnly"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`

	tail       uint
	follow     bool
	since      time.Duration
	timestamps bool
	cols       uint
	rows       uint
	state      sessionState
	execID     string
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

type HostSize struct {
	Cols uint
	Rows uint
}

type HostInfo struct {
	Hostname string
	User     string
	Shell    string
}

type HostStream interface {
	io.ReadWriteCloser
	Resize(context.Context, HostSize) error
	Info() HostInfo
	ExitCode() (int, bool)
}

type HostBackend interface {
	Probe(context.Context) error
	Open(context.Context, HostSize) (HostStream, error)
}

type RemoteStream interface {
	io.ReadWriteCloser
	Resize(context.Context, HostSize) error
	Info() HostInfo
	ExitCode() (int, bool)
}

type RemoteBackend interface {
	Probe(context.Context, string) error
	OpenLogs(context.Context, string, string, podman.LogsOptions) (RemoteStream, error)
	OpenExec(context.Context, string, string, HostSize) (RemoteStream, error)
	OpenHost(context.Context, string, HostSize) (RemoteStream, error)
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
	Hostname string      `json:"hostname,omitempty"`
	User     string      `json:"user,omitempty"`
	Shell    string      `json:"shell,omitempty"`
	ExitCode *int        `json:"exitCode,omitempty"`
	Code     string      `json:"code,omitempty"`
	Message  string      `json:"message,omitempty"`
}

type ServeResult struct {
	Mode        Mode
	Host        HostInfo
	StartedAt   time.Time
	ClosedAt    time.Time
	CloseReason string
	ExitCode    *int
}

type ManagerOptions struct {
	PendingTTL         time.Duration
	IdleTimeout        time.Duration
	HardTimeout        time.Duration
	MaxExecPerUser     int
	MaxExecGlobal      int
	MaxHostPerUser     int
	MaxHostGlobal      int
	MaxSessionsPerUser int
	MaxSessionsGlobal  int
	MaxInputFrame      int
	OutputChunkSize    int
	OutputQueueSize    int
	Random             io.Reader
	Now                func() time.Time
}
