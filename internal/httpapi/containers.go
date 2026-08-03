package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
	"github.com/bnhminh1010/homelab-dashboard/internal/operations"
	"github.com/bnhminh1010/homelab-dashboard/internal/podman"
	"github.com/gin-gonic/gin"
)

type containerLifecycleRequest struct {
	NodeID string `json:"nodeId"`
}

func (s *Server) restartContainer(c *gin.Context) { s.containerLifecycle(c, "restart") }
func (s *Server) stopContainer(c *gin.Context)    { s.containerLifecycle(c, "stop") }

func (s *Server) containerLifecycle(c *gin.Context, action string) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	containerID := strings.TrimSpace(c.Param("id"))
	if !validContainerID(containerID) {
		writeError(c, http.StatusBadRequest, "invalid_container", "The container id is invalid.", nil)
		return
	}
	var request containerLifecycleRequest
	if !decodeJSON(c, &request) {
		return
	}
	nodeID := strings.TrimSpace(request.NodeID)
	if nodeID == "" {
		nodeID = "local"
	}
	if !validContainerID(nodeID) {
		writeError(c, http.StatusBadRequest, "invalid_node", "The node id is invalid.", nil)
		return
	}
	if err := s.appendAudit(c.Request.Context(), model.AuditEvent{
		Actor: principal.Login, Action: "container." + action, TargetType: "container", TargetID: containerID,
		Outcome: "requested", Metadata: map[string]any{"nodeId": nodeID},
	}); err != nil {
		writeError(c, http.StatusServiceUnavailable, "audit_unavailable", "Container lifecycle actions require an available audit log.", nil)
		return
	}
	var err error
	if action == "restart" {
		err = s.options.ContainerLifecycle.Restart(c.Request.Context(), nodeID, containerID)
	} else {
		err = s.options.ContainerLifecycle.Stop(c.Request.Context(), nodeID, containerID)
	}
	if err != nil {
		_ = s.appendAudit(c.Request.Context(), model.AuditEvent{
			Actor: principal.Login, Action: "container." + action, TargetType: "container", TargetID: containerID,
			Outcome: "denied", Metadata: map[string]any{"nodeId": nodeID, "error": err.Error()},
		})
		writeContainerLifecycleError(c, err)
		return
	}
	eventType := operations.EventContainerStopped
	eventTitle := "Container " + containerID + " stopped"
	if action == "restart" {
		eventType = operations.EventContainerRestarted
		eventTitle = "Container " + containerID + " restarted"
	}
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: eventType, Visibility: operations.VisibilityNormal,
		Title: eventTitle, NodeID: nodeID, ContainerID: containerID,
		Actor: principal.Login, OccurredAt: time.Now().UTC(),
	})
	if err := s.appendAudit(c.Request.Context(), model.AuditEvent{
		Actor: principal.Login, Action: "container." + action, TargetType: "container", TargetID: containerID,
		Outcome: "success", Metadata: map[string]any{"nodeId": nodeID},
	}); err != nil {
		writeError(c, http.StatusServiceUnavailable, "audit_unavailable", "Container action completed but its outcome could not be recorded.", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "action": action, "nodeId": nodeID, "containerId": containerID})
}

func writeContainerLifecycleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, podman.ErrProtectedContainer):
		writeError(c, http.StatusForbidden, "container_protected", "Protected or hidden containers cannot be changed from the dashboard.", nil)
	case errors.Is(err, podman.ErrContainerNotRunning):
		writeError(c, http.StatusConflict, "container_not_running", "The container is not running.", nil)
	case errors.Is(err, nodes.ErrNodeOffline):
		writeError(c, http.StatusServiceUnavailable, "node_offline", "The selected node is offline.", nil)
	default:
		writeError(c, http.StatusBadGateway, "container_action_failed", "The container action could not be completed.", nil)
	}
}

func validContainerID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' || character == '.' || character == '-' || character == ':' {
			continue
		}
		return false
	}
	return true
}
