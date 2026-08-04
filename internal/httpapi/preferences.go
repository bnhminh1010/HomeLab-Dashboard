package httpapi

import (
	"errors"
	"net/http"

	"github.com/bnhminh1010/homelab-dashboard/internal/dashboardconfig"
	"github.com/gin-gonic/gin"
)

type preferencesPatch struct {
	TerminalHeight    *int      `json:"terminalHeight,omitempty"`
	TerminalCollapsed *bool     `json:"terminalCollapsed,omitempty"`
	HistoryRange      *string   `json:"historyRange,omitempty"`
	DefaultNodeID     *string   `json:"defaultNodeId,omitempty"`
	HiddenWorkspaces  *[]string `json:"hiddenWorkspaces,omitempty"`
	WorkspaceOrder    *[]string `json:"workspaceOrder,omitempty"`
}

func (s *Server) getDashboardPreferences(c *gin.Context) {
	preferences, err := s.options.Preferences.GetDashboardUIPreferences(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "preferences_unavailable", "Unable to load dashboard preferences.", nil)
		return
	}
	c.JSON(http.StatusOK, preferences)
}

func (s *Server) updateDashboardPreferences(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var patch preferencesPatch
	if !decodeJSON(c, &patch) {
		return
	}
	preferences, err := s.options.Preferences.GetDashboardUIPreferences(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "preferences_unavailable", "Unable to load dashboard preferences.", nil)
		return
	}
	if patch.TerminalHeight != nil {
		preferences.TerminalHeight = *patch.TerminalHeight
	}
	if patch.TerminalCollapsed != nil {
		preferences.TerminalCollapsed = *patch.TerminalCollapsed
	}
	if patch.HistoryRange != nil {
		preferences.HistoryRange = *patch.HistoryRange
	}
	if patch.DefaultNodeID != nil {
		preferences.DefaultNodeID = *patch.DefaultNodeID
	}
	if patch.HiddenWorkspaces != nil {
		preferences.HiddenWorkspaces = append([]string(nil), (*patch.HiddenWorkspaces)...)
	}
	if patch.WorkspaceOrder != nil {
		preferences.WorkspaceOrder = append([]string(nil), (*patch.WorkspaceOrder)...)
	}
	updated, err := s.options.Preferences.UpdateDashboardUIPreferences(c.Request.Context(), preferences, principal.Login)
	if err != nil {
		if errors.Is(err, dashboardconfig.ErrInvalidDocument) {
			writeError(c, http.StatusUnprocessableEntity, "invalid_preferences", "Dashboard preferences are invalid.", nil)
		} else {
			writeError(c, http.StatusInternalServerError, "preferences_update_failed", "Unable to save dashboard preferences.", nil)
		}
		return
	}
	c.JSON(http.StatusOK, updated)
}
