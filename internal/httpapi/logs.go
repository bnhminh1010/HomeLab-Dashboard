package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/logs"
	"github.com/gin-gonic/gin"
)

func (s *Server) getLogsStatus(c *gin.Context) {
	status := logs.Status{Backend: logs.BackendDisabled, NodeID: logs.LocalNodeID}
	if s.options.Logs != nil {
		status = s.options.Logs.Status()
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) queryLogs(c *gin.Context) {
	if s.options.Logs == nil || !s.options.Logs.Status().Enabled {
		writeError(c, http.StatusServiceUnavailable, "logs_disabled", "Historical logs are not configured on this dashboard.", nil)
		return
	}
	query, ok := logQuery(c)
	if !ok {
		return
	}
	result, err := s.options.Logs.Query(c.Request.Context(), query)
	if err != nil {
		if errors.Is(err, logs.ErrInvalid) {
			writeError(c, http.StatusBadRequest, "invalid_logs_query", "The logs query is invalid or exceeds the 7 day window.", nil)
			return
		}
		writeError(c, http.StatusServiceUnavailable, "logs_unavailable", "Historical logs are temporarily unavailable.", nil)
		return
	}
	c.JSON(http.StatusOK, result)
}

func logQuery(c *gin.Context) (logs.Query, bool) {
	now := time.Now().UTC()
	query := logs.Query{
		NodeID: strings.TrimSpace(c.DefaultQuery("node", logs.LocalNodeID)),
		From:   now.Add(-time.Hour), To: now,
		Service:   strings.TrimSpace(c.Query("service")),
		Container: strings.TrimSpace(c.Query("container")),
		Level:     strings.TrimSpace(c.Query("level")),
		Text:      strings.TrimSpace(c.Query("q")),
		IsRegex:   c.Query("regex") == "true",
		Limit:     logs.DefaultLimit,
	}
	for key, destination := range map[string]*time.Time{"from": &query.From, "to": &query.To} {
		value := strings.TrimSpace(c.Query(key))
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid_logs_query", "Log timestamps must use RFC3339.", nil)
			return logs.Query{}, false
		}
		*destination = parsed.UTC()
	}
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid_logs_query", "Log limit must be an integer.", nil)
			return logs.Query{}, false
		}
		query.Limit = parsed
	}
	return query, true
}
