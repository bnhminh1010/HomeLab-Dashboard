package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/alerts"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/nodes"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func (s *Server) listNodes(c *gin.Context) {
	states, err := s.options.NodeRegistry.States(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "nodes_unavailable", "Unable to list monitoring nodes.", nil)
		return
	}
	if s.options.Alerts != nil {
		rules, rulesErr := s.options.Alerts.ListAlertRules(c.Request.Context())
		active, statesErr := s.options.Alerts.ListAlertStates(c.Request.Context(), alerts.StateFilter{ActiveOnly: true, Limit: 100})
		if rulesErr == nil && statesErr == nil {
			for index := range states {
				nodeStates := make([]alerts.AlertState, 0)
				for _, state := range active {
					if state.NodeID == states[index].Node.ID {
						nodeStates = append(nodeStates, state)
					}
				}
				if states[index].Snapshot == nil && len(nodeStates) == 0 {
					continue
				}
				var envelope model.SnapshotEnvelope
				if states[index].Snapshot != nil {
					envelope = *states[index].Snapshot
					envelope.Data.Alerts = append([]model.Alert(nil), envelope.Data.Alerts...)
				} else {
					var collectedAt time.Time
					for _, state := range nodeStates {
						if state.LastEvaluatedAt.After(collectedAt) {
							collectedAt = state.LastEvaluatedAt
						}
					}
					if collectedAt.IsZero() {
						collectedAt = time.Now().UTC()
					}
					envelope = model.SnapshotEnvelope{
						Version: 1, Type: "metrics.snapshot", Sequence: states[index].LastSequence,
						CollectedAt: collectedAt,
						Data:        model.SnapshotData{System: model.SystemStats{Hostname: states[index].Node.Hostname}},
					}
					states[index].Stale = true
				}
				envelope.Data.Alerts = append(envelope.Data.Alerts, alerts.SnapshotAlerts(rules, nodeStates)...)
				states[index].Snapshot = &envelope
			}
		}
	}
	c.JSON(http.StatusOK, states)
}

func (s *Server) createNodeEnrollment(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	enrollment, err := s.options.Nodes.CreateEnrollment(c.Request.Context(), principal.Login)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "node_enrollment_failed", "Unable to create a node enrollment token.", nil)
		s.audit(c, principal, "node.enrollment.create", "", "failed")
		return
	}
	s.audit(c, principal, "node.enrollment.create", enrollment.ID, "success")
	c.JSON(http.StatusCreated, enrollment)
}

func (s *Server) enrollAgent(c *gin.Context) {
	var request struct {
		Token       string `json:"token"`
		DisplayName string `json:"displayName"`
		Hostname    string `json:"hostname"`
	}
	if !decodeJSON(c, &request) {
		return
	}
	node, credential, err := s.options.Nodes.Enroll(c.Request.Context(), request.Token, request.DisplayName, request.Hostname)
	if err != nil {
		status, code := http.StatusUnauthorized, "invalid_enrollment"
		if errors.Is(err, nodes.ErrNodeLimit) {
			status, code = http.StatusConflict, "node_limit"
		}
		writeError(c, status, code, "The enrollment token is invalid, expired, or cannot add another node.", nil)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"node": node, "credential": credential, "protocolVersion": nodes.ProtocolVersion,
		"websocketUrl": "/ws/v1/agents/connect",
	})
}

func (s *Server) revokeNode(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if err := s.options.NodeRegistry.Revoke(c.Request.Context(), id); err != nil {
		if errors.Is(err, nodes.ErrNodeNotFound) {
			writeError(c, http.StatusNotFound, "node_not_found", "The monitoring node does not exist or is already revoked.", nil)
		} else {
			writeError(c, http.StatusInternalServerError, "node_revoke_failed", "Unable to revoke the monitoring node.", nil)
		}
		s.audit(c, principal, "node.revoke", id, "failed")
		return
	}
	s.audit(c, principal, "node.revoke", id, "success")
	c.Status(http.StatusNoContent)
}

func (s *Server) serveAgentWS(c *gin.Context) {
	nodeID := strings.TrimSpace(c.GetHeader("X-Node-ID"))
	credential := bearerToken(c.GetHeader("Authorization"))
	if nodeID == "" || credential == "" {
		writeError(c, http.StatusUnauthorized, "node_credentials_required", "Node credentials are required.", nil)
		return
	}
	if _, err := s.options.Nodes.Authenticate(c.Request.Context(), nodeID, credential); err != nil {
		writeError(c, http.StatusUnauthorized, "node_credentials_invalid", "Node credentials are invalid or revoked.", nil)
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
	sender := &agentWebsocketSender{connection: connection}
	_, generation, err := s.options.NodeRegistry.Attach(c.Request.Context(), nodeID, credential, sender)
	if err != nil {
		_ = sender.Close()
		return
	}
	defer s.options.NodeRegistry.Detach(nodeID, generation)
	defer sender.Close()
	connection.SetReadLimit(nodes.MaxMessageBytes)
	_ = connection.SetReadDeadline(time.Now().Add(45 * time.Second))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(45 * time.Second))
	})

	pingContext, stopPing := context.WithCancel(c.Request.Context())
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingContext.Done():
				return
			case <-ticker.C:
				if sender.Ping() != nil {
					_ = sender.Close()
					return
				}
			}
		}
	}()
	for {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			break
		}
		if messageType != websocket.TextMessage {
			_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "JSON text frames required"), time.Now().Add(time.Second))
			break
		}
		message, err := nodes.DecodeMessage(bytes.NewReader(payload))
		if err != nil || s.options.NodeRegistry.AcceptConnection(c.Request.Context(), nodeID, generation, message) != nil {
			_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid agent message"), time.Now().Add(time.Second))
			break
		}
	}
	stopPing()
	_ = sender.Close()
	<-pingDone
}

func bearerToken(value string) string {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

type agentWebsocketSender struct {
	connection *websocket.Conn
	mu         sync.Mutex
	closed     bool
}

func (sender *agentWebsocketSender) Send(ctx context.Context, message nodes.Message) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(payload) > nodes.MaxMessageBytes {
		return nodes.ErrProtocolSize
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.closed {
		return io.ErrClosedPipe
	}
	deadline := time.Now().Add(10 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = sender.connection.SetWriteDeadline(deadline)
	return sender.connection.WriteMessage(websocket.TextMessage, payload)
}

func (sender *agentWebsocketSender) Ping() error {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.closed {
		return io.ErrClosedPipe
	}
	return sender.connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
}

func (sender *agentWebsocketSender) Close() error {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.closed {
		return nil
	}
	sender.closed = true
	return sender.connection.Close()
}
