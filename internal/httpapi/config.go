package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/binhminh/HomeLab-Minh/internal/auth"
	"github.com/binhminh/HomeLab-Minh/internal/dashboardconfig"
	"github.com/gin-gonic/gin"
)

func (s *Server) exportDashboardConfig(c *gin.Context) {
	if principalFromContext(c).Role != auth.RoleAdmin {
		writeError(c, http.StatusForbidden, "admin_required", "Administrator access is required.", nil)
		return
	}
	payload, err := s.options.DashboardConfig.Export(c.Request.Context())
	if err != nil {
		writeDashboardConfigError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="homelab-dashboard.config.json"`)
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

func (s *Server) previewDashboardConfig(c *gin.Context) {
	_, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	payload, ok := readDashboardConfig(c)
	if !ok {
		return
	}
	preview, err := s.options.DashboardConfig.Preview(c.Request.Context(), payload, dashboardconfig.ImportMode(strings.TrimSpace(c.DefaultQuery("mode", "merge"))))
	if err != nil {
		writeDashboardConfigError(c, err)
		return
	}
	c.Header("ETag", `"`+preview.Revision+`"`)
	c.JSON(http.StatusOK, preview)
}

func (s *Server) applyDashboardConfig(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	payload, ok := readDashboardConfig(c)
	if !ok {
		return
	}
	expectedRevision := strings.TrimSpace(c.GetHeader("If-Match"))
	if len(expectedRevision) >= 2 && strings.HasPrefix(expectedRevision, `"`) && strings.HasSuffix(expectedRevision, `"`) {
		expectedRevision = strings.TrimSuffix(strings.TrimPrefix(expectedRevision, `"`), `"`)
	}
	result, err := s.options.DashboardConfig.Apply(c.Request.Context(), payload,
		dashboardconfig.ImportMode(strings.TrimSpace(c.DefaultQuery("mode", "merge"))), principal.Login, expectedRevision)
	if err != nil {
		writeDashboardConfigError(c, err)
		return
	}
	if s.options.DashboardConfigApplied != nil {
		s.options.DashboardConfigApplied()
	}
	c.JSON(http.StatusOK, result)
}

func readDashboardConfig(c *gin.Context) ([]byte, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, dashboardconfig.MaxDocumentBytes+1)
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(c, http.StatusRequestEntityTooLarge, "config_too_large", "Dashboard configuration must not exceed 1 MiB.", nil)
		} else {
			writeError(c, http.StatusBadRequest, "config_read_failed", "Unable to read the dashboard configuration.", nil)
		}
		return nil, false
	}
	return payload, true
}

func writeDashboardConfigError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, dashboardconfig.ErrDocumentTooLarge):
		writeError(c, http.StatusRequestEntityTooLarge, "config_too_large", "Dashboard configuration must not exceed 1 MiB.", nil)
	case errors.Is(err, dashboardconfig.ErrInvalidImportMode):
		writeError(c, http.StatusBadRequest, "invalid_import_mode", "Import mode must be merge or replace.", nil)
	case errors.Is(err, dashboardconfig.ErrRevisionRequired):
		writeError(c, http.StatusPreconditionRequired, "config_preview_required", "Preview this configuration before applying it.", nil)
	case errors.Is(err, dashboardconfig.ErrRevisionConflict):
		writeError(c, http.StatusPreconditionFailed, "config_revision_conflict", "Dashboard configuration, import payload, or mode changed after preview; run preview again.", nil)
	case errors.Is(err, dashboardconfig.ErrInvalidDocument), errors.Is(err, dashboardconfig.ErrUnsupportedVersion):
		fields := map[string]string(nil)
		var validation *dashboardconfig.ValidationError
		if errors.As(err, &validation) && validation.Path != "" {
			fields = map[string]string{validation.Path: validation.Message}
		}
		writeError(c, http.StatusUnprocessableEntity, "invalid_dashboard_config", "Dashboard configuration is invalid or unsupported.", fields)
	default:
		writeError(c, http.StatusInternalServerError, "dashboard_config_failed", "Unable to process dashboard configuration.", nil)
	}
}
