package podman

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// LabelProtected prevents interactive exec sessions in a container.
	LabelProtected = "io.homelab.dashboard.protected"
	// LabelHidden removes infrastructure containers from the dashboard list.
	LabelHidden = "io.homelab.dashboard.hidden"
)

var (
	ErrProtectedContainer  = errors.New("podman: container is protected")
	ErrContainerNotRunning = errors.New("podman: container is not running")
	ErrInvalidShell        = errors.New("podman: invalid shell")
)

// APIError is returned when the Podman service responds with a non-2xx status.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("podman: API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("podman: API returned HTTP %d: %s", e.StatusCode, e.Message)
}

type Port struct {
	HostIP        string `json:"hostIp"`
	ContainerPort uint16 `json:"containerPort"`
	HostPort      uint16 `json:"hostPort"`
	Range         uint16 `json:"range"`
	Protocol      string `json:"protocol"`
}

type Container struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	State     string            `json:"state"`
	Status    string            `json:"status"`
	Created   int64             `json:"created"`
	Labels    map[string]string `json:"labels,omitempty"`
	Ports     []Port            `json:"ports,omitempty"`
	Protected bool              `json:"protected"`
}

type ContainerDetails struct {
	Container
	Running      bool      `json:"running"`
	Health       string    `json:"health,omitempty"`
	RestartCount uint64    `json:"restartCount"`
	StartedAt    time.Time `json:"startedAt,omitempty"`
}

type ContainerStats struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryUsage uint64  `json:"memoryUsageBytes"`
	MemoryLimit uint64  `json:"memoryLimitBytes"`
	NetworkIn   uint64  `json:"networkInBytes"`
	NetworkOut  uint64  `json:"networkOutBytes"`
	BlockInput  uint64  `json:"blockInputBytes"`
	BlockOutput uint64  `json:"blockOutputBytes"`
	PIDs        uint64  `json:"pids"`
	UpTime      uint64  `json:"upTimeSeconds"`
}

type LogsOptions struct {
	Tail       uint
	Follow     bool
	Since      time.Duration
	Timestamps bool
}

type Shell string

const (
	ShellSH   Shell = "/bin/sh"
	ShellBash Shell = "/bin/bash"
)

func (s Shell) valid() bool {
	return s == ShellSH || s == ShellBash
}

type TerminalSize struct {
	Cols uint
	Rows uint
}

func IsProtected(labels map[string]string) bool {
	return labelEnabled(labels[LabelProtected])
}

func IsHidden(labels map[string]string) bool {
	return labelEnabled(labels[LabelHidden])
}

func labelEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
