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

	"github.com/binhminh/HomeLab-Minh/internal/auth"
	"github.com/binhminh/HomeLab-Minh/internal/metrics"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/services"
	"github.com/binhminh/HomeLab-Minh/internal/store"
	"github.com/binhminh/HomeLab-Minh/internal/terminal"
	"github.com/gin-gonic/gin"
)

const principalKey = "principal"

type AuditWriter interface {
	AppendAudit(context.Context, model.AuditEvent) error
}

type Options struct {
	Auth         *auth.Manager
	Metrics      *metrics.Hub
	Services     *services.Manager
	Audit        AuditWriter
	Terminal     *terminal.Manager
	Static       http.Handler
	Ready        func(context.Context) error
	SecureOrigin bool
}

type Server struct {
	options Options
	router  *gin.Engine
}

func New(options Options) (*Server, error) {
	if options.Auth == nil || options.Metrics == nil || options.Services == nil || options.Terminal == nil || options.Static == nil {
		return nil, errors.New("httpapi: auth, metrics, services, terminal and static handlers are required")
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	_ = router.SetTrustedProxies(nil)
	server := &Server{options: options, router: router}
	server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.router }

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
	api.GET("/session", s.createSession)
	authenticatedAPI := api.Group("")
	authenticatedAPI.Use(s.requireSession())
	authenticatedAPI.GET("/snapshot", s.getSnapshot)
	authenticatedAPI.GET("/services", s.listServices)
	authenticatedAPI.POST("/services", s.createService)
	authenticatedAPI.PATCH("/services/:id", s.updateService)
	authenticatedAPI.DELETE("/services/:id", s.deleteService)
	authenticatedAPI.POST("/terminal/sessions", s.createTerminalSession)
	authenticatedAPI.DELETE("/terminal/sessions/:id", s.cancelTerminalSession)
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
		if strings.HasPrefix(c.Request.URL.Path, "/health/") {
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
		"websocketUrl": "/ws/terminal?session=" + created.ID,
		"expiresAt":    created.ExpiresAt,
		"readOnly":     created.ReadOnly,
	})
}

func (s *Server) cancelTerminalSession(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, false)
	if !ok {
		return
	}
	id := c.Param("id")
	if err := s.options.Terminal.Cancel(id, principal.Login); err != nil {
		switch {
		case errors.Is(err, terminal.ErrNotFound):
			writeError(c, http.StatusNotFound, "terminal_not_found", "Terminal session not found or expired.", nil)
		case errors.Is(err, terminal.ErrSessionClaimed):
			writeError(c, http.StatusConflict, "terminal_connected", "A connected terminal must be closed through its WebSocket.", nil)
		default:
			writeError(c, http.StatusInternalServerError, "terminal_cancel_failed", "Unable to cancel the terminal session.", nil)
		}
		s.audit(c, principal, "terminal.cancel", id, "denied")
		return
	}
	s.audit(c, principal, "terminal.cancel", id, "success")
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

func (s *Server) writeServiceError(c *gin.Context, err error) {
	var validation *services.ValidationError
	switch {
	case errors.As(err, &validation):
		writeError(c, http.StatusUnprocessableEntity, "validation_failed", "Service input is invalid.", validation.Fields)
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
	default:
		return http.StatusBadGateway, "terminal_unavailable"
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
