package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/auth"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/terminal"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	maxMetricsFrame           = 50 << 10
	defaultTerminalPingPeriod = 20 * time.Second
	defaultTerminalPongWait   = 60 * time.Second
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout:  5 * time.Second,
	EnableCompression: true,
	CheckOrigin:       func(*http.Request) bool { return true },
}

func (s *Server) serveMetricsWS(c *gin.Context) {
	s.serveMetricsStream(c, marshalSnapshot)
}

func (s *Server) serveCompatibleMetricsWS(c *gin.Context) {
	s.serveMetricsStream(c, marshalLegacySnapshot)
}

func (s *Server) serveMetricsStream(c *gin.Context, marshal func(model.SnapshotEnvelope, int) ([]byte, error)) {
	if err := auth.ValidateSameOrigin(c.Request, s.options.SecureOrigin); err != nil {
		writeError(c, http.StatusForbidden, "invalid_origin", "WebSocket origin is not allowed.", nil)
		return
	}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	untrack, tracked := s.websockets.track(connection)
	if !tracked {
		return
	}
	defer untrack()
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Time{})
	connection.SetReadLimit(1024)
	_ = connection.SetReadDeadline(time.Now().Add(60 * time.Second))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	readDone := make(chan error, 1)
	go func() {
		for {
			messageType, _, err := connection.ReadMessage()
			if err != nil {
				readDone <- err
				return
			}
			if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
				readDone <- errors.New("metrics WebSocket is server-only")
				return
			}
		}
	}()

	updates, unsubscribe := s.options.Metrics.Subscribe()
	defer unsubscribe()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-readDone:
			return
		case snapshot, ok := <-updates:
			if !ok {
				return
			}
			payload, err := marshal(snapshot, maxMetricsFrame)
			if err != nil {
				_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "snapshot too large"), time.Now().Add(2*time.Second))
				return
			}
			_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := connection.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ping.C:
			if err := connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}

func marshalSnapshot(snapshot model.SnapshotEnvelope, limit int) ([]byte, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	for len(payload) > limit {
		snapshot.Truncated = true
		switch {
		case len(snapshot.Data.Containers) > 0:
			snapshot.TruncatedSources = appendSnapshotSource(snapshot.TruncatedSources, "containers")
			snapshot.Data.Containers = snapshot.Data.Containers[:len(snapshot.Data.Containers)/2]
		case len(snapshot.Data.Services) > 0:
			snapshot.TruncatedSources = appendSnapshotSource(snapshot.TruncatedSources, "services")
			snapshot.Data.Services = snapshot.Data.Services[:len(snapshot.Data.Services)/2]
		case len(snapshot.Data.Alerts) > 0:
			snapshot.TruncatedSources = appendSnapshotSource(snapshot.TruncatedSources, "alerts")
			snapshot.Data.Alerts = snapshot.Data.Alerts[:len(snapshot.Data.Alerts)/2]
		default:
			return nil, fmt.Errorf("metrics snapshot exceeds %d bytes without optional items", limit)
		}
		payload, err = json.Marshal(snapshot)
		if err != nil {
			return nil, err
		}
	}
	return payload, nil
}

func appendSnapshotSource(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (s *Server) serveTerminalWS(c *gin.Context) {
	if err := auth.ValidateSameOrigin(c.Request, s.options.SecureOrigin); err != nil {
		writeError(c, http.StatusForbidden, "invalid_origin", "WebSocket origin is not allowed.", nil)
		return
	}
	principal := principalFromContext(c)
	sessionID := c.Param("id")
	if sessionID == "" {
		sessionID = c.Query("session")
	}
	session, err := s.options.Terminal.Get(sessionID, principal.Login)
	if err != nil {
		writeError(c, http.StatusNotFound, "terminal_not_found", "Terminal session not found or expired.", nil)
		return
	}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	untrack, tracked := s.websockets.track(connection)
	if !tracked {
		return
	}
	defer untrack()
	defer connection.Close()
	connection.SetReadLimit(20 << 10)
	peer := &websocketPeer{connection: connection}
	if err := peer.enableHeartbeat(s.terminalPongWait); err != nil {
		return
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(c.Request.Context())
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		peer.runHeartbeat(heartbeatCtx, s.terminalPingPeriod)
	}()
	result, err := s.options.Terminal.ServeDetailed(c.Request.Context(), sessionID, principal.Login, peer)
	stopHeartbeat()
	<-heartbeatDone
	outcome := terminalCloseOutcome(result.CloseReason, err)
	if session.Mode != terminal.ModeHost {
		s.audit(c, principal, "terminal.close", session.ContainerID, outcome)
		return
	}
	metadata := map[string]any{
		"closeReason": result.CloseReason,
		"durationMs":  max(result.ClosedAt.Sub(result.StartedAt).Milliseconds(), 0),
	}
	if result.Host.User != "" {
		metadata["unixUser"] = result.Host.User
	}
	if result.Host.Shell != "" {
		metadata["shell"] = result.Host.Shell
	}
	if result.ExitCode != nil {
		metadata["exitCode"] = *result.ExitCode
	}
	auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.appendAudit(auditCtx, model.AuditEvent{
		Actor: principal.Login, Action: "terminal.host.close", TargetType: "host_shell",
		TargetID: result.Host.Hostname, Outcome: outcome, Metadata: metadata,
	})
}

func terminalCloseOutcome(closeReason string, err error) string {
	if closeReason == "completed" && err == nil {
		return "success"
	}
	switch closeReason {
	case "peer_disconnected", "idle_timeout", "maximum_duration", "canceled":
		return "interrupted"
	default:
		return "failed"
	}
}

type websocketPeer struct {
	connection *websocket.Conn
	writeMu    sync.Mutex
}

func (p *websocketPeer) Read(context.Context) (terminal.ClientMessage, error) {
	messageType, payload, err := p.connection.ReadMessage()
	if err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return terminal.ClientMessage{}, io.EOF
		}
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
			return terminal.ClientMessage{}, io.EOF
		}
		return terminal.ClientMessage{}, err
	}
	switch messageType {
	case websocket.BinaryMessage:
		return terminal.ClientMessage{Type: terminal.ClientInput, Data: payload}, nil
	case websocket.TextMessage:
		var control struct {
			Type string `json:"type"`
			Cols uint   `json:"cols,omitempty"`
			Rows uint   `json:"rows,omitempty"`
		}
		if len(payload) > 4096 || json.Unmarshal(payload, &control) != nil {
			return terminal.ClientMessage{}, terminal.ErrInvalidRequest
		}
		switch control.Type {
		case "resize", "terminal.resize":
			return terminal.ClientMessage{Type: terminal.ClientResize, Cols: control.Cols, Rows: control.Rows}, nil
		case "close", "terminal.close":
			return terminal.ClientMessage{Type: terminal.ClientClose}, nil
		default:
			return terminal.ClientMessage{}, terminal.ErrInvalidRequest
		}
	default:
		return terminal.ClientMessage{}, terminal.ErrInvalidRequest
	}
}

func (p *websocketPeer) enableHeartbeat(pongWait time.Duration) error {
	if pongWait <= 0 {
		return errors.New("terminal WebSocket pong wait must be positive")
	}
	if err := p.connection.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		return err
	}
	p.connection.SetPongHandler(func(string) error {
		return p.connection.SetReadDeadline(time.Now().Add(pongWait))
	})
	return nil
}

func (p *websocketPeer) runHeartbeat(ctx context.Context, period time.Duration) {
	if period <= 0 {
		p.abort()
		return
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.writeMu.Lock()
			_ = p.connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err := p.connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			p.writeMu.Unlock()
			if err != nil {
				p.abort()
				return
			}
		}
	}
}

func (p *websocketPeer) abort() {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.connection.Close()
}

func (p *websocketPeer) WriteBinary(_ context.Context, payload []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return p.connection.WriteMessage(websocket.BinaryMessage, payload)
}

func (p *websocketPeer) WriteControl(_ context.Context, control terminal.Control) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return p.connection.WriteJSON(control)
}

func (p *websocketPeer) Close() error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session closed"), time.Now().Add(time.Second))
	return p.connection.Close()
}
