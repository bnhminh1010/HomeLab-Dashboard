// Package smartagent provides the small, restricted host-side SMART protocol.
// It intentionally exposes a checker interface instead of shell execution so
// the metrics collector can be tested without a real disk or smartctl binary.
package smartagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	StatusPassed      = "PASSED"
	StatusFailed      = "FAILED"
	StatusStandby     = "STANDBY"
	StatusUnavailable = "UNAVAILABLE"
	StatusTimeout     = "TIMEOUT"
	maxRequestBytes   = 4096
	maxResponseBytes  = 1 << 20
)

type Result struct {
	Status             string   `json:"smartStatus"`
	TemperatureCelsius *float64 `json:"temperatureCelsius,omitempty"`
	Message            string   `json:"message,omitempty"`
}

type Checker interface {
	Check(context.Context, string) (Result, error)
}

type request struct {
	Device string `json:"device"`
}

type Client struct {
	SocketPath string
	Timeout    time.Duration
}

func (client Client) Check(ctx context.Context, device string) (Result, error) {
	if strings.TrimSpace(client.SocketPath) == "" {
		return Result{Status: StatusUnavailable, Message: "SMART helper is not configured"}, nil
	}
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(callCtx, "unix", client.SocketPath)
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return Result{Status: StatusTimeout, Message: "SMART helper connection timed out"}, nil
		}
		return Result{Status: StatusUnavailable, Message: "SMART helper is unavailable"}, nil
	}
	defer connection.Close()
	if deadline, ok := callCtx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if err := json.NewEncoder(connection).Encode(request{Device: device}); err != nil {
		return Result{}, err
	}
	decoder := json.NewDecoder(&ioLimitReader{Reader: connection, Limit: maxResponseBytes})
	var result Result
	if err := decoder.Decode(&result); err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return Result{Status: StatusTimeout, Message: "SMART helper timed out"}, nil
		}
		return Result{}, fmt.Errorf("read SMART helper response: %w", err)
	}
	if result.Status == "" {
		result.Status = StatusUnavailable
	}
	return result, nil
}

type Options struct {
	SocketPath     string
	MountsPath     string
	Binary         string
	CommandTimeout time.Duration
	AllowedDevices []string
	MaxConcurrency int
}

type Server struct {
	options  Options
	sem      chan struct{}
	mu       sync.Mutex
	listener *net.UnixListener
}

func NewServer(options Options) (*Server, error) {
	if options.SocketPath == "" || !filepath.IsAbs(options.SocketPath) {
		return nil, fmt.Errorf("smartagent: socket path must be absolute")
	}
	if options.MountsPath == "" {
		options.MountsPath = "/proc/1/mounts"
	}
	if options.Binary == "" {
		options.Binary = "smartctl"
	}
	if options.CommandTimeout <= 0 {
		options.CommandTimeout = 3 * time.Second
	}
	if options.MaxConcurrency <= 0 {
		options.MaxConcurrency = 2
	}
	if options.MaxConcurrency > 16 {
		return nil, fmt.Errorf("smartagent: max concurrency must be at most 16")
	}
	return &Server{options: options, sem: make(chan struct{}, options.MaxConcurrency)}, nil
}

func (server *Server) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(server.options.SocketPath), 0o750); err != nil {
		return fmt.Errorf("smartagent: create socket directory: %w", err)
	}
	if err := os.Remove(server.options.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("smartagent: remove stale socket: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: server.options.SocketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("smartagent: listen: %w", err)
	}
	if err := os.Chmod(server.options.SocketPath, 0o600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("smartagent: secure socket: %w", err)
	}
	server.mu.Lock()
	server.listener = listener
	server.mu.Unlock()
	defer func() {
		_ = listener.Close()
		_ = os.Remove(server.options.SocketPath)
	}()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("smartagent: accept: %w", err)
		}
		go server.handle(ctx, connection)
	}
}

func (server *Server) handle(ctx context.Context, connection *net.UnixConn) {
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	decoder := json.NewDecoder(&ioLimitReader{Reader: connection, Limit: maxRequestBytes})
	var input request
	if err := decoder.Decode(&input); err != nil {
		_ = json.NewEncoder(connection).Encode(Result{Status: StatusUnavailable, Message: "invalid SMART request"})
		return
	}
	if !server.allowed(input.Device) {
		_ = json.NewEncoder(connection).Encode(Result{Status: StatusUnavailable, Message: "device is not allowlisted"})
		return
	}
	select {
	case server.sem <- struct{}{}:
		defer func() { <-server.sem }()
	case <-ctx.Done():
		return
	}
	result := server.run(ctx, input.Device)
	_ = connection.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_ = json.NewEncoder(connection).Encode(result)
}

func (server *Server) allowed(device string) bool {
	device = filepath.Clean(strings.TrimSpace(device))
	if !strings.HasPrefix(device, "/dev/") || strings.Contains(device, "..") {
		return false
	}
	if len(server.options.AllowedDevices) > 0 {
		for _, allowed := range server.options.AllowedDevices {
			if filepath.Clean(allowed) == device {
				return true
			}
		}
		return false
	}
	file, err := os.Open(server.options.MountsPath)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 1 && strings.ReplaceAll(fields[0], `\040`, " ") == device {
			return true
		}
	}
	return false
}

func (server *Server) run(parent context.Context, device string) Result {
	ctx, cancel := context.WithTimeout(parent, server.options.CommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, server.options.Binary, "--json", "--all", "-n", "standby", device)
	output, err := command.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{Status: StatusTimeout, Message: "smartctl timed out"}
	}
	result := parse(output)
	if result.Status != StatusUnavailable {
		return result
	}
	if err != nil {
		return Result{Status: StatusUnavailable, Message: "smartctl unavailable"}
	}
	return result
}

func parse(data []byte) Result {
	var payload struct {
		PowerMode   string `json:"power_mode"`
		SmartStatus struct {
			Passed *bool `json:"passed"`
		} `json:"smart_status"`
		Temperature struct {
			Current *float64 `json:"current"`
		} `json:"temperature"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Result{Status: StatusUnavailable, Message: "invalid smartctl JSON"}
	}
	if strings.EqualFold(payload.PowerMode, "STANDBY") {
		return Result{Status: StatusStandby, Message: "disk is in standby"}
	}
	if payload.SmartStatus.Passed == nil {
		return Result{Status: StatusUnavailable, Message: "SMART health is unavailable"}
	}
	result := Result{Status: StatusFailed}
	if *payload.SmartStatus.Passed {
		result.Status = StatusPassed
	}
	if payload.Temperature.Current != nil {
		value := *payload.Temperature.Current
		result.TemperatureCelsius = &value
	}
	return result
}

type ioLimitReader struct {
	Reader io.Reader
	Limit  int64
}

func (reader *ioLimitReader) Read(buffer []byte) (int, error) {
	if reader.Limit <= 0 {
		return 0, fmt.Errorf("SMART payload exceeds limit")
	}
	if int64(len(buffer)) > reader.Limit {
		buffer = buffer[:reader.Limit]
	}
	count, err := reader.Reader.Read(buffer)
	reader.Limit -= int64(count)
	return count, err
}
