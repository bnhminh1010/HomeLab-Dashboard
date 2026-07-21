package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/containers"
	"github.com/binhminh/HomeLab-Minh/internal/healthchecks"
	"github.com/binhminh/HomeLab-Minh/internal/metrics"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/nodes"
	"github.com/binhminh/HomeLab-Minh/internal/podman"
	"github.com/gorilla/websocket"
)

const (
	defaultMetricsInterval = 10 * time.Second
	heartbeatInterval      = 10 * time.Second
	serverSilenceTimeout   = 45 * time.Second
	outboundQueueSize      = 128
)

type RunOptions struct {
	StatePath        string
	PodmanSocket     string
	ProcPath         string
	SysPath          string
	RootPath         string
	NetworkInterface string
	BackupStatusFile string
	MaxSessions      int
	AgentVersion     string
}

func Run(ctx context.Context, options RunOptions) error {
	if os.Geteuid() == 0 {
		return errors.New("node agent: refusing to run as root; use the rootless Podman account")
	}
	credentials, err := LoadCredentials(options.StatePath)
	if err != nil {
		return err
	}
	if err := applyRunDefaults(&options); err != nil {
		return err
	}
	hostCollector, err := metrics.NewLinuxCollector(metrics.CollectorOptions{
		ProcPath: options.ProcPath, SysPath: options.SysPath, RootPath: options.RootPath,
		NetworkInterface: options.NetworkInterface,
	})
	if err != nil {
		return fmt.Errorf("node agent: configure host collector: %w", err)
	}
	podmanClient, err := podman.NewClient(options.PodmanSocket)
	if err != nil {
		return err
	}
	defer podmanClient.CloseIdleConnections()
	backupSource, err := healthchecks.NewBackupFileSource(options.BackupStatusFile)
	if err != nil {
		return fmt.Errorf("node agent: configure backup status source: %w", err)
	}
	collector, err := NewLocalCollector(hostCollector, containers.New(podmanClient), backupSource)
	if err != nil {
		return err
	}
	runner := &runner{
		credentials: credentials, collector: collector, backend: podmanClient,
		host: LocalHostShell{}, maxSessions: options.MaxSessions,
		now: time.Now, startedAt: time.Now().UTC(), hostname: hostName(),
		agentVersion: options.AgentVersion, dialer: websocket.DefaultDialer,
	}
	return runner.run(ctx)
}

func applyRunDefaults(options *RunOptions) error {
	if options.StatePath == "" || !filepath.IsAbs(options.StatePath) {
		return errors.New("node agent: state path must be absolute")
	}
	if options.PodmanSocket == "" {
		runtimeDirectory := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDirectory == "" {
			runtimeDirectory = filepath.Join("/run/user", strconv.Itoa(os.Geteuid()))
		}
		options.PodmanSocket = filepath.Join(runtimeDirectory, "podman", "podman.sock")
	}
	if options.ProcPath == "" {
		options.ProcPath = "/proc"
	}
	if options.SysPath == "" {
		options.SysPath = "/sys"
	}
	if options.RootPath == "" {
		options.RootPath = "/"
	}
	for name, value := range map[string]string{
		"Podman socket": options.PodmanSocket, "proc path": options.ProcPath,
		"sys path": options.SysPath, "root path": options.RootPath,
	} {
		if !filepath.IsAbs(value) {
			return fmt.Errorf("node agent: %s must be absolute", name)
		}
	}
	if options.MaxSessions == 0 {
		options.MaxSessions = defaultMaxSessions
	}
	if options.MaxSessions < 1 || options.MaxSessions > 8 {
		return errors.New("node agent: maximum sessions must be between 1 and 8")
	}
	if options.AgentVersion == "" {
		options.AgentVersion = "dev"
	}
	return nil
}

type websocketDialer interface {
	DialContext(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error)
}

type runner struct {
	credentials  Credentials
	collector    SnapshotCollector
	backend      RuntimeBackend
	host         HostShellOpener
	maxSessions  int
	now          func() time.Time
	startedAt    time.Time
	hostname     string
	agentVersion string
	dialer       websocketDialer
}

func (agent *runner) run(ctx context.Context) error {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		connectedAt := agent.now()
		err := agent.connect(ctx)
		if ctx.Err() != nil {
			return nil
		}
		attempt = nextReconnectAttempt(attempt, agent.now().Sub(connectedAt))
		delay := reconnectDelay(attempt)
		slog.Warn("node agent connection lost; retrying", "delay", delay, "error", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		attempt++
	}
}

// A connection that survived at least one server-silence window is considered
// stable. Resetting the exponential backoff prevents an old outage from making
// every later, unrelated reconnect wait at the 30-second cap.
func nextReconnectAttempt(previous int, connectedFor time.Duration) int {
	if connectedFor >= serverSilenceTimeout {
		return 0
	}
	if previous < 0 {
		return 0
	}
	return previous
}

func (agent *runner) connect(ctx context.Context) error {
	server, err := validateServerURL(agent.credentials.ServerURL)
	if err != nil {
		return err
	}
	endpoint, err := resolveWebsocketURL(server, agent.credentials.WebsocketURL)
	if err != nil {
		return err
	}
	headers := make(http.Header)
	headers.Set("X-Node-ID", agent.credentials.NodeID)
	headers.Set("Authorization", "Bearer "+agent.credentials.Credential)
	connection, response, err := agent.dialer.DialContext(ctx, endpoint.String(), headers)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
			return fmt.Errorf("node agent: websocket rejected with HTTP %d: %w", response.StatusCode, err)
		}
		return fmt.Errorf("node agent: connect websocket: %w", err)
	}
	defer connection.Close()
	slog.Info("node agent connected", "node", agent.credentials.NodeID, "server", server.Host)
	return agent.serveConnection(ctx, connection)
}

func (agent *runner) serveConnection(parent context.Context, connection *websocket.Conn) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	sink := newConnectionSink(ctx, agent.credentials.NodeID, agent.now)
	manager, err := NewSessionManager(agent.backend, agent.host, sink, agent.maxSessions)
	if err != nil {
		return err
	}
	defer func() {
		sink.Close()
		manager.CloseAll()
	}()
	validator, err := NewMessageValidator(agent.credentials.NodeID, agent.now)
	if err != nil {
		return err
	}
	connection.SetReadLimit(nodes.MaxMessageBytes)
	_ = connection.SetReadDeadline(agent.now().Add(serverSilenceTimeout))
	connection.SetPingHandler(func(data string) error {
		_ = connection.SetReadDeadline(agent.now().Add(serverSilenceTimeout))
		return connection.WriteControl(websocket.PongMessage, []byte(data), agent.now().Add(5*time.Second))
	})

	connectionErrors := make(chan error, 3)
	go writeMessages(ctx, connection, sink.queue, connectionErrors)
	go func() {
		select {
		case <-ctx.Done():
		case <-connectionErrors:
			cancel()
		}
		_ = connection.Close()
	}()
	go func() {
		if err := agent.runHeartbeat(ctx, sink); err != nil {
			reportConnectionError(ctx, connectionErrors, err)
		}
	}()
	go func() {
		if err := agent.runMetrics(ctx, sink); err != nil {
			reportConnectionError(ctx, connectionErrors, err)
		}
	}()

	for {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("node agent: read websocket: %w", err)
		}
		if messageType != websocket.TextMessage {
			return errors.New("node agent: dashboard sent a non-JSON websocket frame")
		}
		message, err := nodes.DecodeMessage(bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if err := validator.Validate(message); err != nil {
			return err
		}
		// A rejected typed command is reported with command.result but does not
		// tear down monitoring for the node.
		_ = manager.Handle(ctx, message)
	}
}

func (agent *runner) runHeartbeat(ctx context.Context, sink *connectionSink) error {
	send := func() error {
		uptime := uint64(0)
		if elapsed := agent.now().Sub(agent.startedAt); elapsed > 0 {
			uptime = uint64(elapsed.Seconds())
		}
		return sink.Send(nodes.MessageHeartbeat, "", nodes.Heartbeat{
			AgentVersion: agent.agentVersion, Hostname: agent.hostname, Uptime: uptime,
		})
	}
	if err := send(); err != nil {
		return err
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := send(); err != nil {
				return err
			}
		}
	}
}

func (agent *runner) runMetrics(ctx context.Context, sink *connectionSink) error {
	send := func() error {
		collectionContext, cancel := context.WithTimeout(ctx, 8*time.Second)
		snapshot, collectionErr := agent.collector.Collect(collectionContext)
		cancel()
		if collectionErr != nil {
			slog.Warn("node agent snapshot is partial", "error", collectionErr)
		}
		return sendSnapshot(sink, snapshot)
	}
	if err := send(); err != nil {
		return err
	}
	ticker := time.NewTicker(defaultMetricsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := send(); err != nil {
				return err
			}
		}
	}
}

func reportConnectionError(ctx context.Context, errorsOut chan<- error, err error) {
	select {
	case errorsOut <- err:
	case <-ctx.Done():
	}
}

func sendSnapshot(sink *connectionSink, snapshot model.SnapshotEnvelope) error {
	for {
		err := sink.Send(nodes.MessageMetricsSnapshot, "", nodes.MetricsPayload{Snapshot: snapshot})
		if !errors.Is(err, nodes.ErrProtocolSize) {
			return err
		}
		snapshot.Truncated = true
		switch {
		case len(snapshot.Data.Containers) > 0:
			snapshot.TruncatedSources = appendUnique(snapshot.TruncatedSources, "containers")
			snapshot.Data.Containers = snapshot.Data.Containers[:len(snapshot.Data.Containers)/2]
		case len(snapshot.Data.Alerts) > 0:
			snapshot.TruncatedSources = appendUnique(snapshot.TruncatedSources, "alerts")
			snapshot.Data.Alerts = snapshot.Data.Alerts[:len(snapshot.Data.Alerts)/2]
		default:
			return nodes.ErrProtocolSize
		}
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

type connectionSink struct {
	ctx    context.Context
	nodeID string
	now    func() time.Time
	queue  chan nodes.Message

	mu       sync.Mutex
	sequence uint64
	closed   bool
}

func newConnectionSink(ctx context.Context, nodeID string, now func() time.Time) *connectionSink {
	return &connectionSink{ctx: ctx, nodeID: nodeID, now: now, queue: make(chan nodes.Message, outboundQueueSize)}
}

func (sink *connectionSink) Send(messageType, requestID string, payload any) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed || sink.ctx.Err() != nil {
		return errors.New("node agent: connection is closed")
	}
	nextSequence := sink.sequence + 1
	message, err := nodes.NewMessage(sink.nodeID, messageType, nextSequence, sink.now().UTC(), requestID, payload)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(encoded) > nodes.MaxMessageBytes {
		return nodes.ErrProtocolSize
	}
	select {
	case sink.queue <- message:
		sink.sequence = nextSequence
		return nil
	default:
		return ErrBackpressure
	}
}

func (sink *connectionSink) Close() {
	sink.mu.Lock()
	sink.closed = true
	sink.mu.Unlock()
}

func writeMessages(ctx context.Context, connection *websocket.Conn, queue <-chan nodes.Message, errorsOut chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case message := <-queue:
			payload, err := json.Marshal(message)
			if err == nil {
				_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
				err = connection.WriteMessage(websocket.TextMessage, payload)
			}
			if err != nil {
				select {
				case errorsOut <- err:
				default:
				}
				return
			}
		}
	}
}

func reconnectDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Second
	for range attempt {
		if delay >= 30*time.Second {
			return 30 * time.Second
		}
		delay *= 2
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func hostName() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return "unknown"
	}
	return hostname
}
