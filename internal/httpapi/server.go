package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
	"github.com/bnhminh1010/homelab-dashboard/internal/auth"
	"github.com/bnhminh1010/homelab-dashboard/internal/dashboardconfig"
	"github.com/bnhminh1010/homelab-dashboard/internal/healthchecks"
	"github.com/bnhminh1010/homelab-dashboard/internal/history"
	"github.com/bnhminh1010/homelab-dashboard/internal/logs"
	"github.com/bnhminh1010/homelab-dashboard/internal/metrics"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
	"github.com/bnhminh1010/homelab-dashboard/internal/operations"
	"github.com/bnhminh1010/homelab-dashboard/internal/services"
	"github.com/bnhminh1010/homelab-dashboard/internal/slo"
	"github.com/bnhminh1010/homelab-dashboard/internal/store"
	"github.com/bnhminh1010/homelab-dashboard/internal/terminal"
	"github.com/bnhminh1010/homelab-dashboard/internal/topology"
	"github.com/gin-gonic/gin"
)

const principalKey = "principal"

type AuditWriter interface {
	AppendAudit(context.Context, model.AuditEvent) error
}

type AlertRepository interface {
	CreateAlertRule(context.Context, alerts.AlertRule) (alerts.AlertRule, error)
	GetAlertRule(context.Context, string) (alerts.AlertRule, error)
	ListAlertRules(context.Context) ([]alerts.AlertRule, error)
	UpdateAlertRule(context.Context, string, alerts.AlertRule) (alerts.AlertRule, error)
	DeleteAlertRule(context.Context, string) error
	ListMaintenanceWindows(context.Context) ([]alerts.MaintenanceWindow, error)
	CreateMaintenanceWindow(context.Context, alerts.MaintenanceWindow) (alerts.MaintenanceWindow, error)
	UpdateMaintenanceWindow(context.Context, string, alerts.MaintenanceWindow) (alerts.MaintenanceWindow, error)
	DeleteMaintenanceWindow(context.Context, string) error
	ListAlertStates(context.Context, alerts.StateFilter) ([]alerts.AlertState, error)
	ListAlertEvents(context.Context, alerts.EventFilter) ([]alerts.AlertEvent, error)
	AcknowledgeAlert(context.Context, alerts.AlertKey, string, time.Time) (alerts.AlertState, error)
	SilenceAlert(context.Context, alerts.AlertKey, string, time.Duration, time.Time) (alerts.AlertState, error)
}

type NotificationSender interface {
	Send(context.Context, alerts.Delivery) error
}

type PreferencesRepository interface {
	GetDashboardUIPreferences(context.Context) (dashboardconfig.UIPreferences, error)
	UpdateDashboardUIPreferences(context.Context, dashboardconfig.UIPreferences, string) (dashboardconfig.UIPreferences, error)
}

type TopologyRepository interface {
	ListTopologyDependencies(context.Context, string) ([]topology.Dependency, error)
	CreateTopologyDependency(context.Context, topology.DependencyInput) (topology.Dependency, error)
	DeleteTopologyDependency(context.Context, string, int64) error
}

type CheckRepository interface {
	ListCertificateObservations(context.Context) ([]healthchecks.CertificateObservation, error)
	ListBackupObservations(context.Context, string) ([]healthchecks.BackupObservation, error)
}

// ContainerLifecycle is the narrow mutation boundary for the two explicitly
// supported Podman lifecycle operations. Implementations must enforce runtime
// labels again because snapshot state is not authoritative.
type ContainerLifecycle interface {
	Restart(context.Context, string, string) error
	Stop(context.Context, string, string) error
}

type Options struct {
	Auth                   *auth.Manager
	Metrics                *metrics.Hub
	Services               *services.Manager
	Audit                  AuditWriter
	Terminal               *terminal.Manager
	Static                 http.Handler
	Ready                  func(context.Context) error
	SecureOrigin           bool
	HostShellEnabled       bool
	HostShellUsers         []string
	Nodes                  *nodes.Service
	NodeRegistry           *nodes.Registry
	History                history.Reader
	HistoryQuota           func() history.QuotaState
	Alerts                 AlertRepository
	Notifications          NotificationSender
	NTFYNotifications      NotificationSender
	WebhookNotifications   NotificationSender
	NTFYURL                string
	NTFYTopic              string
	NTFYTokenSet           bool
	WebhookURL             string
	WebhookSecretSet       bool
	DashboardConfig        *dashboardconfig.Service
	DashboardConfigApplied func()
	Preferences            PreferencesRepository
	SLO                    *slo.Service
	Operations             operations.Repository
	Topology               TopologyRepository
	Checks                 CheckRepository
	Logs                   logs.Reader
	ContainerLifecycle     ContainerLifecycle
}

type Server struct {
	options            Options
	router             *gin.Engine
	hostShellUsers     map[string]struct{}
	terminalPingPeriod time.Duration
	terminalPongWait   time.Duration
	websockets         websocketTracker
}

func New(options Options) (*Server, error) {
	if options.Auth == nil || options.Metrics == nil || options.Services == nil || options.Terminal == nil || options.Static == nil {
		return nil, errors.New("httpapi: auth, metrics, services, terminal and static handlers are required")
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	_ = router.SetTrustedProxies(nil)
	hostShellUsers := make(map[string]struct{}, len(options.HostShellUsers))
	for _, login := range options.HostShellUsers {
		if normalized := strings.ToLower(strings.TrimSpace(login)); normalized != "" {
			hostShellUsers[normalized] = struct{}{}
		}
	}
	server := &Server{
		options: options, router: router, hostShellUsers: hostShellUsers,
		terminalPingPeriod: defaultTerminalPingPeriod,
		terminalPongWait:   defaultTerminalPongWait,
	}
	server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.router }

// Shutdown closes every upgraded WebSocket and waits for its handler to
// return. net/http.Server.Shutdown deliberately does not manage hijacked
// connections, so callers must invoke both shutdown methods under one
// deadline before closing shared dependencies such as the database.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.websockets.closeAndWait(ctx)
}

func (s *Server) routes() {
	s.router.Use(securityMiddleware(), s.identityMiddleware())
	s.router.GET("/health/live", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	s.router.GET("/health/ready", func(c *gin.Context) {
		if s.options.Ready != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
			defer cancel()
			if err := s.options.Ready(ctx); err != nil {
				writeError(c, http.StatusServiceUnavailable, "not_ready", "Dashboard dependencies are not ready.", nil)
				return
			}
		}
		if _, ok := s.options.Metrics.Latest(); !ok {
			writeError(c, http.StatusServiceUnavailable, "metrics_not_ready", "The first metrics snapshot is not ready.", nil)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	api := s.router.Group("/api/v1")
	api.POST("/session", s.createSession)
	authenticatedAPI := api.Group("")
	authenticatedAPI.Use(s.requireSession())
	authenticatedAPI.GET("/snapshot", s.getSnapshot)
	authenticatedAPI.GET("/services", s.listServices)
	authenticatedAPI.POST("/services", s.createService)
	authenticatedAPI.PATCH("/services/:id", s.updateService)
	authenticatedAPI.DELETE("/services/:id", s.deleteService)
	if s.options.SLO != nil {
		authenticatedAPI.GET("/slos", s.listSLOs)
		authenticatedAPI.PATCH("/services/:id/slo", s.updateServiceSLO)
	}
	if s.options.Operations != nil {
		authenticatedAPI.GET("/events", s.listOperationalEvents)
		authenticatedAPI.POST("/events", s.createOperationalEvent)
	}
	if s.options.Topology != nil {
		authenticatedAPI.GET("/topology/dependencies", s.listTopologyDependencies)
		authenticatedAPI.POST("/topology/dependencies", s.createTopologyDependency)
		authenticatedAPI.DELETE("/topology/dependencies/:id", s.deleteTopologyDependency)
	}
	if s.options.Checks != nil {
		authenticatedAPI.GET("/operations/checks", s.listOperationalChecks)
	}
	authenticatedAPI.POST("/terminal/sessions", s.createTerminalSession)
	authenticatedAPI.POST("/terminal/host-sessions", s.createHostTerminalSession)
	authenticatedAPI.DELETE("/terminal/sessions/:id", s.cancelTerminalSession)
	if s.options.Nodes != nil && s.options.NodeRegistry != nil {
		authenticatedAPI.GET("/nodes", s.listNodes)
		authenticatedAPI.POST("/nodes/enrollment-tokens", s.createNodeEnrollment)
		authenticatedAPI.DELETE("/nodes/:id", s.revokeNode)
		api.POST("/agents/enroll", s.enrollAgent)
		s.router.GET("/ws/v1/agents/connect", s.serveAgentWS)
	}
	if s.options.History != nil {
		authenticatedAPI.GET("/history/resources", s.getHistoryResources)
		authenticatedAPI.GET("/history/system", s.getSystemHistory)
		authenticatedAPI.GET("/history/containers/:id", s.getContainerHistory)
		authenticatedAPI.GET("/history/services/:id", s.getServiceHistory)
	}
	authenticatedAPI.GET("/logs/status", s.getLogsStatus)
	authenticatedAPI.GET("/logs/query", s.queryLogs)
	if s.options.ContainerLifecycle != nil {
		authenticatedAPI.POST("/containers/:id/restart", s.restartContainer)
		authenticatedAPI.POST("/containers/:id/stop", s.stopContainer)
	}
	if s.options.Alerts != nil {
		authenticatedAPI.GET("/alert-rules", s.listAlertRules)
		authenticatedAPI.POST("/alert-rules", s.createAlertRule)
		authenticatedAPI.PATCH("/alert-rules/:id", s.updateAlertRule)
		authenticatedAPI.DELETE("/alert-rules/:id", s.deleteAlertRule)
		authenticatedAPI.GET("/alerts", s.listAlertStates)
		authenticatedAPI.GET("/alerts/events", s.listAlertEvents)
		authenticatedAPI.POST("/alerts/acknowledge", s.acknowledgeAlert)
		authenticatedAPI.POST("/alerts/silence", s.silenceAlert)
		authenticatedAPI.GET("/maintenance-windows", s.listMaintenanceWindows)
		authenticatedAPI.POST("/maintenance-windows", s.createMaintenanceWindow)
		authenticatedAPI.PATCH("/maintenance-windows/:id", s.updateMaintenanceWindow)
		authenticatedAPI.DELETE("/maintenance-windows/:id", s.deleteMaintenanceWindow)
		authenticatedAPI.GET("/notifications/ntfy", s.getNTFYStatus)
		authenticatedAPI.POST("/notifications/ntfy/test", s.testNTFY)
		authenticatedAPI.GET("/notifications/webhook", s.getWebhookStatus)
		authenticatedAPI.POST("/notifications/webhook/test", s.testWebhook)
	}
	if s.options.DashboardConfig != nil {
		authenticatedAPI.GET("/config/export", s.exportDashboardConfig)
		authenticatedAPI.POST("/config/import/preview", s.previewDashboardConfig)
		authenticatedAPI.POST("/config/import/apply", s.applyDashboardConfig)
	}
	if s.options.Preferences != nil {
		authenticatedAPI.GET("/preferences", s.getDashboardPreferences)
		authenticatedAPI.PATCH("/preferences", s.updateDashboardPreferences)
	}
	compatibilityAPI := s.router.Group("/api")
	compatibilityAPI.Use(s.requireSession())
	compatibilityAPI.GET("/services", s.listServices)
	compatibilityAPI.POST("/services", s.createService)
	compatibilityAPI.PATCH("/services/:id", s.updateService)
	compatibilityAPI.DELETE("/services/:id", s.deleteService)

	ws := s.router.Group("/ws/v1")
	ws.Use(s.requireSession())
	ws.GET("/metrics", s.serveMetricsWS)
	ws.GET("/terminal/:id", s.serveTerminalWS)
	compatibilityWS := s.router.Group("/ws")
	compatibilityWS.Use(s.requireSession())
	compatibilityWS.GET("/metrics", s.serveCompatibleMetricsWS)
	compatibilityWS.GET("/terminal", s.serveTerminalWS)
	compatibilityWS.GET("/terminal/:id", s.serveTerminalWS)

	serveStatic := func(c *gin.Context) { s.options.Static.ServeHTTP(c.Writer, c.Request) }
	for _, route := range []string{"/", "/index.html", "/css/*filepath", "/js/*filepath", "/lib/*filepath"} {
		s.router.GET(route, serveStatic)
		s.router.HEAD(route, serveStatic)
	}
	s.router.NoRoute(func(c *gin.Context) {
		http.NotFound(c.Writer, c.Request)
	})
}

func (s *Server) identityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/health/") || isAgentRoute(c.Request.URL.Path) {
			c.Next()
			return
		}
		principal, err := s.options.Auth.PrincipalFromRequest(c.Request)
		if err != nil {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") || strings.HasPrefix(c.Request.URL.Path, "/ws/") {
				writeError(c, http.StatusUnauthorized, "identity_required", "A Tailscale user identity is required.", nil)
			} else {
				c.String(http.StatusUnauthorized, "Tailscale identity required")
			}
			c.Abort()
			return
		}
		c.Set(principalKey, principal)
		c.Next()
	}
}

func isAgentRoute(path string) bool {
	return path == "/api/v1/agents/enroll" || path == "/ws/v1/agents/connect"
}

func (s *Server) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		principal := principalFromContext(c)
		if _, err := s.options.Auth.Validate(c.Request, principal); err != nil {
			writeError(c, http.StatusUnauthorized, "session_required", "Start a dashboard session before using this endpoint.", nil)
			c.Abort()
			return
		}
		c.Next()
	}
}

func (s *Server) createSession(c *gin.Context) {
	if err := auth.ValidateSameOrigin(c.Request, s.options.SecureOrigin); err != nil {
		writeError(c, http.StatusForbidden, "invalid_origin", "Session origin is not allowed.", nil)
		return
	}
	principal := principalFromContext(c)
	session, err := s.options.Auth.Start(c.Writer, principal)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "session_failed", "Unable to create a browser session.", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"identity":  principal,
		"role":      principal.Role,
		"csrfToken": session.CSRF,
		"capabilities": gin.H{
			"manageServices":   principal.Role == auth.RoleAdmin,
			"containerExec":    principal.Role == auth.RoleAdmin,
			"hostShell":        s.hostShellAllowed(principal),
			"manageAlerts":     principal.Role == auth.RoleAdmin && s.options.Alerts != nil,
			"manageNodes":      principal.Role == auth.RoleAdmin && s.options.Nodes != nil,
			"manageConfig":     principal.Role == auth.RoleAdmin && s.options.DashboardConfig != nil,
			"history":          s.options.History != nil,
			"multiNode":        s.options.NodeRegistry != nil,
			"slo":              s.options.SLO != nil,
			"operations":       s.options.Operations != nil,
			"topology":         s.options.Topology != nil,
			"healthChecks":     s.options.Checks != nil,
			"manageContainers": principal.Role == auth.RoleAdmin && s.options.ContainerLifecycle != nil,
		},
	})
}

func (s *Server) getSnapshot(c *gin.Context) {
	snapshot, ok := s.options.Metrics.Latest()
	if !ok {
		writeError(c, http.StatusServiceUnavailable, "metrics_not_ready", "The first metrics snapshot is not ready.", nil)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func (s *Server) listServices(c *gin.Context) {
	items, err := s.options.Services.ListServices(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "services_unavailable", "Unable to list services.", nil)
		return
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) createService(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var input model.ServiceInput
	if !decodeJSON(c, &input) {
		return
	}
	resolvePortShorthand(c.Request, &input)
	created, err := s.options.Services.Create(c.Request.Context(), input)
	if err != nil {
		s.writeServiceError(c, err)
		s.audit(c, principal, "service.create", "", "denied")
		return
	}
	s.audit(c, principal, "service.create", created.ID, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: operations.EventServiceCreated, Title: "Service added", Summary: created.Name,
		ServiceID: created.ID, Actor: principal.Login,
	})
	c.JSON(http.StatusCreated, created)
}

func (s *Server) updateService(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var input model.ServiceInput
	if !decodeJSON(c, &input) {
		return
	}
	resolvePortShorthand(c.Request, &input)
	updated, err := s.options.Services.Update(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		s.writeServiceError(c, err)
		s.audit(c, principal, "service.update", c.Param("id"), "denied")
		return
	}
	s.audit(c, principal, "service.update", updated.ID, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: operations.EventServiceUpdated, Title: "Service updated", Summary: updated.Name,
		ServiceID: updated.ID, Actor: principal.Login,
	})
	c.JSON(http.StatusOK, updated)
}

func (s *Server) deleteService(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := s.options.Services.Delete(c.Request.Context(), id); err != nil {
		s.writeServiceError(c, err)
		s.audit(c, principal, "service.delete", id, "denied")
		return
	}
	s.audit(c, principal, "service.delete", id, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: operations.EventServiceDeleted, Title: "Service removed", Summary: id,
		ServiceID: id, Actor: principal.Login,
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) createTerminalSession(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, false)
	if !ok {
		return
	}
	var request terminal.CreateRequest
	if !decodeJSON(c, &request) {
		return
	}
	created, err := s.options.Terminal.Create(principal.Login, principal.Role == auth.RoleAdmin, request)
	if err != nil {
		status, code := terminalErrorStatus(err)
		writeError(c, status, code, "Unable to create the requested terminal session.", nil)
		s.audit(c, principal, "terminal.create", request.ContainerID, "denied")
		return
	}
	s.audit(c, principal, "terminal.create", request.ContainerID, "success")
	c.JSON(http.StatusCreated, gin.H{
		"id":           created.ID,
		"nodeId":       created.NodeID,
		"websocketUrl": "/ws/terminal?session=" + created.ID,
		"expiresAt":    created.ExpiresAt,
		"readOnly":     created.ReadOnly,
	})
}

func (s *Server) createHostTerminalSession(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, false)
	if !ok {
		return
	}
	if !s.hostShellAllowed(principal) {
		writeError(c, http.StatusForbidden, "host_shell_forbidden", "Host shell access is not allowed for this account.", nil)
		return
	}
	var request terminal.HostCreateRequest
	if !decodeJSON(c, &request) {
		return
	}
	created, err := s.options.Terminal.CreateHost(c.Request.Context(), principal.Login, true, request)
	if err != nil {
		status, code := hostTerminalErrorStatus(err)
		writeError(c, status, code, "Unable to reserve a host shell session.", nil)
		return
	}
	targetID := ""
	if created.NodeID != "local" {
		targetID = created.NodeID
	}
	if err := s.appendAudit(c.Request.Context(), model.AuditEvent{
		Actor: principal.Login, Action: "terminal.host.reserve", TargetType: "host_shell",
		TargetID: targetID, Outcome: "success",
	}); err != nil {
		_ = s.options.Terminal.Cancel(created.ID, principal.Login)
		writeError(c, http.StatusServiceUnavailable, "audit_unavailable", "Host shell access is unavailable because the security audit could not be recorded.", nil)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id":           created.ID,
		"nodeId":       created.NodeID,
		"websocketUrl": "/ws/v1/terminal/" + created.ID,
		"expiresAt":    created.ExpiresAt,
		"readOnly":     false,
	})
}

func (s *Server) cancelTerminalSession(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, false)
	if !ok {
		return
	}
	id := c.Param("id")
	session, lookupErr := s.options.Terminal.Get(id, principal.Login)
	var auditSession *terminal.Session
	if lookupErr == nil {
		auditSession = &session
	}
	if err := s.options.Terminal.Cancel(id, principal.Login); err != nil {
		switch {
		case errors.Is(err, terminal.ErrNotFound):
			writeError(c, http.StatusNotFound, "terminal_not_found", "Terminal session not found or expired.", nil)
		case errors.Is(err, terminal.ErrSessionClaimed):
			writeError(c, http.StatusConflict, "terminal_connected", "A connected terminal must be closed through its WebSocket.", nil)
		default:
			writeError(c, http.StatusInternalServerError, "terminal_cancel_failed", "Unable to cancel the terminal session.", nil)
		}
		s.auditTerminalCancel(c, principal, auditSession, "denied")
		return
	}
	s.auditTerminalCancel(c, principal, auditSession, "success")
	c.Status(http.StatusNoContent)
}

func (s *Server) authorizeMutation(c *gin.Context, adminOnly bool) (auth.Principal, bool) {
	principal := principalFromContext(c)
	if _, err := s.options.Auth.ValidateMutation(c.Request, principal); err != nil {
		writeError(c, http.StatusForbidden, "request_forbidden", "The request failed same-origin or CSRF validation.", nil)
		return auth.Principal{}, false
	}
	if adminOnly && principal.Role != auth.RoleAdmin {
		writeError(c, http.StatusForbidden, "admin_required", "Administrator access is required.", nil)
		return auth.Principal{}, false
	}
	return principal, true
}

func (s *Server) hostShellAllowed(principal auth.Principal) bool {
	if !s.options.HostShellEnabled || principal.Role != auth.RoleAdmin {
		return false
	}
	_, ok := s.hostShellUsers[strings.ToLower(strings.TrimSpace(principal.Login))]
	return ok
}

func (s *Server) writeServiceError(c *gin.Context, err error) {
	var validation *services.ValidationError
	switch {
	case errors.As(err, &validation):
		writeError(c, http.StatusUnprocessableEntity, "validation_failed", "Service input is invalid.", validation.Fields)
	case errors.Is(err, services.ErrServiceLimit):
		writeError(c, http.StatusConflict, "service_limit_reached", "The dashboard supports at most 100 services.", nil)
	case errors.Is(err, store.ErrNotFound):
		writeError(c, http.StatusNotFound, "service_not_found", "The service does not exist.", nil)
	default:
		writeError(c, http.StatusInternalServerError, "service_failed", "Unable to save the service.", nil)
	}
}

func (s *Server) audit(c *gin.Context, principal auth.Principal, action, targetID, outcome string) {
	if s.options.Audit == nil {
		return
	}
	_ = s.options.Audit.AppendAudit(c.Request.Context(), model.AuditEvent{
		Actor: principal.Login, Action: action, TargetType: strings.SplitN(action, ".", 2)[0],
		TargetID: targetID, Outcome: outcome,
	})
}

func (s *Server) auditTerminalCancel(c *gin.Context, principal auth.Principal, session *terminal.Session, outcome string) {
	if s.options.Audit == nil {
		return
	}
	event := model.AuditEvent{
		Actor: principal.Login, Action: "terminal.cancel", TargetType: "terminal",
		Outcome: outcome,
	}
	if session != nil {
		if session.Mode == terminal.ModeHost {
			event.Action = "terminal.host.cancel"
			event.TargetType = "host_shell"
		} else {
			event.TargetType = "container"
			event.TargetID = session.ContainerID
		}
	}
	_ = s.options.Audit.AppendAudit(c.Request.Context(), event)
}

func (s *Server) appendAudit(ctx context.Context, event model.AuditEvent) error {
	if s.options.Audit == nil {
		return errors.New("audit writer is unavailable")
	}
	return s.options.Audit.AppendAudit(ctx, event)
}

func decodeJSON(c *gin.Context, target any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON with known fields.", nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(c, http.StatusBadRequest, "invalid_json", "Request body must contain one JSON object.", nil)
		return false
	}
	return true
}

func resolvePortShorthand(request *http.Request, input *model.ServiceInput) {
	value := strings.TrimSpace(input.DisplayURL)
	if value == "" {
		value = strings.TrimSpace(input.URL)
	}
	if value == "" {
		value = strings.TrimSpace(input.Port)
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return
	}
	host := request.Host
	if parsedHost, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		host = "localhost"
	}
	input.DisplayURL = "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	input.URL = ""
	input.Port = ""
}

func principalFromContext(c *gin.Context) auth.Principal {
	value, _ := c.Get(principalKey)
	principal, _ := value.(auth.Principal)
	return principal
}

func terminalErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, terminal.ErrUnauthorized):
		return http.StatusForbidden, "admin_required"
	case errors.Is(err, terminal.ErrExecLimit), errors.Is(err, terminal.ErrSessionLimit):
		return http.StatusTooManyRequests, "terminal_limit"
	case errors.Is(err, terminal.ErrInvalidRequest):
		return http.StatusUnprocessableEntity, "invalid_terminal_request"
	case errors.Is(err, terminal.ErrRemoteUnavailable):
		return http.StatusServiceUnavailable, "node_unavailable"
	default:
		return http.StatusBadGateway, "terminal_unavailable"
	}
}

func hostTerminalErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, terminal.ErrUnauthorized):
		return http.StatusForbidden, "host_shell_forbidden"
	case errors.Is(err, terminal.ErrHostLimit), errors.Is(err, terminal.ErrSessionLimit):
		return http.StatusTooManyRequests, "host_shell_limit"
	case errors.Is(err, terminal.ErrInvalidRequest):
		return http.StatusUnprocessableEntity, "invalid_terminal_request"
	case errors.Is(err, terminal.ErrHostUnavailable):
		return http.StatusServiceUnavailable, "host_agent_unavailable"
	case errors.Is(err, terminal.ErrRemoteUnavailable):
		return http.StatusServiceUnavailable, "node_unavailable"
	default:
		return http.StatusServiceUnavailable, "host_shell_unavailable"
	}
}

func writeError(c *gin.Context, status int, code, message string, fields map[string]string) {
	errorBody := gin.H{"code": code, "message": message}
	if len(fields) > 0 {
		errorBody["fields"] = fields
	}
	c.AbortWithStatusJSON(status, gin.H{"error": errorBody})
}

func securityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		setSecurityHeaders(c.Writer.Header())
		c.Next()
	}
}
