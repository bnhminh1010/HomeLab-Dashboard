package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
	"github.com/bnhminh1010/homelab-dashboard/internal/auth"
	"github.com/bnhminh1010/homelab-dashboard/internal/operations"
	"github.com/bnhminh1010/homelab-dashboard/internal/store"
	"github.com/gin-gonic/gin"
)

type alertRulePayload struct {
	ID               string          `json:"id,omitempty"`
	Name             string          `json:"name"`
	ResourceType     string          `json:"resourceType"`
	NodeSelector     string          `json:"nodeSelector"`
	ResourceSelector string          `json:"resourceSelector"`
	Metric           string          `json:"metric"`
	Operator         alerts.Operator `json:"operator"`
	Threshold        float64         `json:"threshold"`
	ForSeconds       int64           `json:"forSeconds"`
	Severity         alerts.Severity `json:"severity"`
	CooldownSeconds  int64           `json:"cooldownSeconds"`
	RunbookURL       string          `json:"runbookUrl,omitempty"`
	Enabled          bool            `json:"enabled"`
	CreatedAt        time.Time       `json:"createdAt,omitempty"`
	UpdatedAt        time.Time       `json:"updatedAt,omitempty"`
}

const maxAlertRuleSeconds = int64((30 * 24 * time.Hour) / time.Second)

func (payload alertRulePayload) durationsValid() bool {
	return payload.ForSeconds >= 0 && payload.ForSeconds <= maxAlertRuleSeconds &&
		payload.CooldownSeconds >= 0 && payload.CooldownSeconds <= maxAlertRuleSeconds
}

func (payload alertRulePayload) rule() alerts.AlertRule {
	return alerts.AlertRule{
		ID: payload.ID, Name: payload.Name, ResourceType: payload.ResourceType,
		NodeSelector: payload.NodeSelector, ResourceSelector: payload.ResourceSelector,
		Metric: payload.Metric, Operator: payload.Operator, Threshold: payload.Threshold,
		For: time.Duration(payload.ForSeconds) * time.Second, Severity: payload.Severity,
		Cooldown: time.Duration(payload.CooldownSeconds) * time.Second, RunbookURL: payload.RunbookURL, Enabled: payload.Enabled,
	}
}

func alertRuleView(rule alerts.AlertRule) alertRulePayload {
	return alertRulePayload{
		ID: rule.ID, Name: rule.Name, ResourceType: rule.ResourceType,
		NodeSelector: rule.NodeSelector, ResourceSelector: rule.ResourceSelector,
		Metric: rule.Metric, Operator: rule.Operator, Threshold: rule.Threshold,
		ForSeconds: int64(rule.For / time.Second), Severity: rule.Severity,
		CooldownSeconds: int64(rule.Cooldown / time.Second), RunbookURL: rule.RunbookURL, Enabled: rule.Enabled,
		CreatedAt: rule.CreatedAt, UpdatedAt: rule.UpdatedAt,
	}
}

type maintenanceWindowPayload struct {
	ID               string    `json:"id,omitempty"`
	Name             string    `json:"name"`
	NodeSelector     string    `json:"nodeSelector"`
	ResourceType     string    `json:"resourceType"`
	ResourceSelector string    `json:"resourceSelector"`
	Weekdays         []int     `json:"weekdays"`
	StartMinute      int       `json:"startMinute"`
	DurationMinutes  int       `json:"durationMinutes"`
	Timezone         string    `json:"timezone"`
	Enabled          bool      `json:"enabled"`
	CreatedAt        time.Time `json:"createdAt,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt,omitempty"`
}

func (payload maintenanceWindowPayload) window() alerts.MaintenanceWindow {
	weekdays := make([]time.Weekday, len(payload.Weekdays))
	for index, day := range payload.Weekdays {
		weekdays[index] = time.Weekday(day)
	}
	return alerts.MaintenanceWindow{ID: payload.ID, Name: payload.Name, NodeSelector: payload.NodeSelector,
		ResourceType: payload.ResourceType, ResourceSelector: payload.ResourceSelector, Weekdays: weekdays,
		StartMinute: payload.StartMinute, Duration: time.Duration(payload.DurationMinutes) * time.Minute,
		Timezone: payload.Timezone, Enabled: payload.Enabled}
}

func maintenanceWindowView(window alerts.MaintenanceWindow) maintenanceWindowPayload {
	weekdays := make([]int, len(window.Weekdays))
	for index, day := range window.Weekdays {
		weekdays[index] = int(day)
	}
	return maintenanceWindowPayload{ID: window.ID, Name: window.Name, NodeSelector: window.NodeSelector,
		ResourceType: window.ResourceType, ResourceSelector: window.ResourceSelector, Weekdays: weekdays,
		StartMinute: window.StartMinute, DurationMinutes: int(window.Duration / time.Minute), Timezone: window.Timezone,
		Enabled: window.Enabled, CreatedAt: window.CreatedAt, UpdatedAt: window.UpdatedAt}
}

func (s *Server) listAlertRules(c *gin.Context) {
	rules, err := s.options.Alerts.ListAlertRules(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "alert_rules_unavailable", "Unable to list alert rules.", nil)
		return
	}
	views := make([]alertRulePayload, 0, len(rules))
	for _, rule := range rules {
		views = append(views, alertRuleView(rule))
	}
	c.JSON(http.StatusOK, views)
}

func (s *Server) listMaintenanceWindows(c *gin.Context) {
	windows, err := s.options.Alerts.ListMaintenanceWindows(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "maintenance_windows_unavailable", "Unable to list maintenance windows.", nil)
		return
	}
	result := make([]maintenanceWindowPayload, 0, len(windows))
	for _, window := range windows {
		result = append(result, maintenanceWindowView(window))
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) createMaintenanceWindow(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var payload maintenanceWindowPayload
	if !decodeJSON(c, &payload) {
		return
	}
	window := payload.window()
	if window.ID == "" {
		window.ID = "maintenance_" + randomAlertRuleID()
	}
	created, err := s.options.Alerts.CreateMaintenanceWindow(c.Request.Context(), window)
	if err != nil {
		s.writeMaintenanceError(c, err)
		s.audit(c, principal, "maintenance_window.create", window.ID, "failed")
		return
	}
	s.audit(c, principal, "maintenance_window.create", created.ID, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{Type: operations.EventMaintenance, Title: "Maintenance window created", Summary: created.Name, Actor: principal.Login})
	c.JSON(http.StatusCreated, maintenanceWindowView(created))
}

func (s *Server) updateMaintenanceWindow(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var payload maintenanceWindowPayload
	if !decodeJSON(c, &payload) {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	updated, err := s.options.Alerts.UpdateMaintenanceWindow(c.Request.Context(), id, payload.window())
	if err != nil {
		s.writeMaintenanceError(c, err)
		s.audit(c, principal, "maintenance_window.update", id, "failed")
		return
	}
	s.audit(c, principal, "maintenance_window.update", id, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{Type: operations.EventMaintenance, Title: "Maintenance window updated", Summary: updated.Name, Actor: principal.Login})
	c.JSON(http.StatusOK, maintenanceWindowView(updated))
}

func (s *Server) deleteMaintenanceWindow(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if err := s.options.Alerts.DeleteMaintenanceWindow(c.Request.Context(), id); err != nil {
		s.writeMaintenanceError(c, err)
		s.audit(c, principal, "maintenance_window.delete", id, "failed")
		return
	}
	s.audit(c, principal, "maintenance_window.delete", id, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{Type: operations.EventMaintenance, Title: "Maintenance window removed", Summary: id, Actor: principal.Login})
	c.Status(http.StatusNoContent)
}

func (s *Server) createAlertRule(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var payload alertRulePayload
	if !decodeJSON(c, &payload) {
		return
	}
	if !payload.durationsValid() {
		s.writeAlertRuleError(c, alerts.ErrInvalidRule)
		return
	}
	rule := payload.rule()
	if rule.ID == "" {
		rule.ID = randomAlertRuleID()
	}
	created, err := s.options.Alerts.CreateAlertRule(c.Request.Context(), rule)
	if err != nil {
		s.writeAlertRuleError(c, err)
		s.audit(c, principal, "alert_rule.create", rule.ID, "failed")
		return
	}
	s.audit(c, principal, "alert_rule.create", created.ID, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: operations.EventAlertRuleChanged, Title: "Alert rule created", Summary: created.Name,
		Actor: principal.Login,
	})
	c.JSON(http.StatusCreated, alertRuleView(created))
}

func (s *Server) updateAlertRule(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var payload alertRulePayload
	if !decodeJSON(c, &payload) {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if !payload.durationsValid() {
		s.writeAlertRuleError(c, alerts.ErrInvalidRule)
		s.audit(c, principal, "alert_rule.update", id, "failed")
		return
	}
	rule := payload.rule()
	rule.ID = id
	updated, err := s.options.Alerts.UpdateAlertRule(c.Request.Context(), id, rule)
	if err != nil {
		s.writeAlertRuleError(c, err)
		s.audit(c, principal, "alert_rule.update", id, "failed")
		return
	}
	s.audit(c, principal, "alert_rule.update", id, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: operations.EventAlertRuleChanged, Title: "Alert rule updated", Summary: updated.Name,
		Actor: principal.Login,
	})
	c.JSON(http.StatusOK, alertRuleView(updated))
}

func (s *Server) deleteAlertRule(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if err := s.options.Alerts.DeleteAlertRule(c.Request.Context(), id); err != nil {
		s.writeAlertRuleError(c, err)
		s.audit(c, principal, "alert_rule.delete", id, "failed")
		return
	}
	s.audit(c, principal, "alert_rule.delete", id, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: operations.EventAlertRuleChanged, Title: "Alert rule removed", Summary: id,
		Actor: principal.Login,
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) listAlertStates(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	filter := alerts.StateFilter{
		RuleID: strings.TrimSpace(c.Query("ruleId")), NodeID: strings.TrimSpace(c.Query("node")),
		Status: alerts.AlertStatus(strings.TrimSpace(c.Query("status"))), Limit: limit,
		ActiveOnly: c.DefaultQuery("active", "true") != "false",
	}
	states, err := s.options.Alerts.ListAlertStates(c.Request.Context(), filter)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "alerts_unavailable", "Unable to list alerts.", nil)
		return
	}
	c.JSON(http.StatusOK, states)
}

func (s *Server) listAlertEvents(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	events, err := s.options.Alerts.ListAlertEvents(c.Request.Context(), alerts.EventFilter{
		RuleID: strings.TrimSpace(c.Query("ruleId")), NodeID: strings.TrimSpace(c.Query("node")),
		ResourceID: strings.TrimSpace(c.Query("resourceId")),
		Type:       alerts.EventType(strings.TrimSpace(c.Query("type"))), Limit: limit,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "alert_events_unavailable", "Unable to list alert events.", nil)
		return
	}
	c.JSON(http.StatusOK, events)
}

type alertActionPayload struct {
	RuleID       string `json:"ruleId"`
	NodeID       string `json:"nodeId"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	Duration     string `json:"duration,omitempty"`
}

func (payload alertActionPayload) key() alerts.AlertKey {
	return alerts.AlertKey{RuleID: payload.RuleID, NodeID: payload.NodeID, ResourceType: payload.ResourceType, ResourceID: payload.ResourceID}
}

func (s *Server) acknowledgeAlert(c *gin.Context) {
	s.alertAction(c, false)
}

func (s *Server) silenceAlert(c *gin.Context) {
	s.alertAction(c, true)
}

func (s *Server) alertAction(c *gin.Context, silence bool) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var payload alertActionPayload
	if !decodeJSON(c, &payload) {
		return
	}
	var state alerts.AlertState
	var err error
	action := "alert.acknowledge"
	if silence {
		duration, parseErr := time.ParseDuration(payload.Duration)
		if parseErr != nil || alerts.ValidateSilenceDuration(duration) != nil {
			writeError(c, http.StatusUnprocessableEntity, "invalid_silence", "Silence duration must be 1h, 6h, or 24h.", nil)
			return
		}
		state, err = s.options.Alerts.SilenceAlert(c.Request.Context(), payload.key(), principal.Login, duration, time.Now().UTC())
		action = "alert.silence"
	} else {
		state, err = s.options.Alerts.AcknowledgeAlert(c.Request.Context(), payload.key(), principal.Login, time.Now().UTC())
	}
	if err != nil {
		status, code := http.StatusInternalServerError, "alert_action_failed"
		if errors.Is(err, store.ErrNotFound) {
			status, code = http.StatusNotFound, "alert_not_found"
		} else if errors.Is(err, alerts.ErrAlertResolved) {
			status, code = http.StatusConflict, "alert_resolved"
		}
		writeError(c, status, code, "Unable to update the alert.", nil)
		s.audit(c, principal, action, payload.RuleID, "failed")
		return
	}
	s.audit(c, principal, action, payload.RuleID, "success")
	c.JSON(http.StatusOK, state)
}

func (s *Server) getNTFYStatus(c *gin.Context) {
	ntfySender := s.options.NTFYNotifications
	// Keep compatibility with embedders that only set Notifications while
	// explicitly advertising an ntfy destination; the dashboard runtime sets
	// the provider-specific sender fields.
	if ntfySender == nil && s.options.NTFYURL != "" {
		ntfySender = s.options.Notifications
	}
	status := gin.H{
		"configured":      ntfySender != nil,
		"tokenConfigured": s.options.NTFYTokenSet,
	}
	if principalFromContext(c).Role == auth.RoleAdmin {
		status["url"] = strings.TrimSpace(s.options.NTFYURL)
		status["topic"] = strings.TrimSpace(s.options.NTFYTopic)
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) getWebhookStatus(c *gin.Context) {
	status := gin.H{
		"configured":       s.options.WebhookURL != "" && s.options.WebhookSecretSet,
		"secretConfigured": s.options.WebhookSecretSet,
	}
	if principalFromContext(c).Role == auth.RoleAdmin {
		status["url"] = strings.TrimSpace(s.options.WebhookURL)
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) testWebhook(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	if s.options.WebhookNotifications == nil || s.options.WebhookURL == "" || !s.options.WebhookSecretSet {
		writeError(c, http.StatusConflict, "webhook_not_configured", "Webhook notifications are not configured.", nil)
		return
	}
	err := s.options.WebhookNotifications.Send(c.Request.Context(), alerts.Delivery{
		Kind: alerts.DeliveryFiring, Severity: alerts.SeverityInfo,
		Title: "Homelab dashboard test", Message: "Webhook notifications are configured correctly.",
	})
	if err != nil {
		writeError(c, http.StatusBadGateway, "webhook_test_failed", "The webhook test notification could not be delivered.", nil)
		s.audit(c, principal, "notification.webhook.test", "", "failed")
		return
	}
	s.audit(c, principal, "notification.webhook.test", "", "success")
	c.JSON(http.StatusOK, gin.H{"status": "delivered"})
}

func (s *Server) testNTFY(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	ntfySender := s.options.NTFYNotifications
	if ntfySender == nil && s.options.NTFYURL != "" {
		ntfySender = s.options.Notifications
	}
	if ntfySender == nil {
		writeError(c, http.StatusConflict, "ntfy_not_configured", "ntfy is not configured.", nil)
		return
	}
	err := ntfySender.Send(c.Request.Context(), alerts.Delivery{
		Kind: alerts.DeliveryFiring, Severity: alerts.SeverityInfo,
		Title: "Homelab dashboard test", Message: "ntfy notifications are configured correctly.",
	})
	if err != nil {
		writeError(c, http.StatusBadGateway, "ntfy_test_failed", "The ntfy test notification could not be delivered.", nil)
		s.audit(c, principal, "notification.ntfy.test", "", "failed")
		return
	}
	s.audit(c, principal, "notification.ntfy.test", "", "success")
	c.JSON(http.StatusOK, gin.H{"status": "delivered"})
}

func (s *Server) writeAlertRuleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, alerts.ErrInvalidRule):
		writeError(c, http.StatusUnprocessableEntity, "invalid_alert_rule", "The alert rule is invalid.", nil)
	case errors.Is(err, store.ErrNotFound):
		writeError(c, http.StatusNotFound, "alert_rule_not_found", "The alert rule does not exist.", nil)
	default:
		writeError(c, http.StatusInternalServerError, "alert_rule_failed", "Unable to save the alert rule.", nil)
	}
}

func (s *Server) writeMaintenanceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, alerts.ErrInvalidMaintenance):
		writeError(c, http.StatusUnprocessableEntity, "invalid_maintenance_window", "The maintenance window is invalid.", nil)
	case errors.Is(err, store.ErrNotFound):
		writeError(c, http.StatusNotFound, "maintenance_window_not_found", "The maintenance window does not exist.", nil)
	default:
		writeError(c, http.StatusInternalServerError, "maintenance_window_failed", "Unable to save the maintenance window.", nil)
	}
}

func randomAlertRuleID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "rule_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "rule_" + hex.EncodeToString(buffer)
}
