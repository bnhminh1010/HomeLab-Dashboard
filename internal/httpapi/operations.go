package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/auth"
	"github.com/binhminh/HomeLab-Minh/internal/healthchecks"
	"github.com/binhminh/HomeLab-Minh/internal/history"
	"github.com/binhminh/HomeLab-Minh/internal/operations"
	"github.com/binhminh/HomeLab-Minh/internal/slo"
	"github.com/binhminh/HomeLab-Minh/internal/topology"
	"github.com/gin-gonic/gin"
)

func (s *Server) recordAutomaticEvent(ctx context.Context, event operations.Event) {
	if s.options.Operations == nil {
		return
	}
	event.Source = operations.SourceAutomatic
	if event.Visibility == "" {
		event.Visibility = operations.VisibilityNormal
	}
	_, _ = s.options.Operations.RecordOperationalEvent(ctx, event)
}

func (s *Server) listSLOs(c *gin.Context) {
	window, ok := parseSLOWindow(c)
	if !ok {
		return
	}
	items, err := s.options.Services.ListServices(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "services_unavailable", "Unable to list services for SLO reporting.", nil)
		return
	}
	references := make([]slo.ServiceRef, 0, len(items))
	for _, item := range items {
		references = append(references, slo.ServiceRef{ID: item.ID, Name: item.Name})
	}
	nodeID := strings.TrimSpace(c.DefaultQuery("node", "local"))
	reports, err := s.options.SLO.List(c.Request.Context(), nodeID, references, window)
	if err != nil {
		writeSLOError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": reports})
}

func (s *Server) updateServiceSLO(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	serviceID := strings.TrimSpace(c.Param("id"))
	service, err := s.options.Services.Get(c.Request.Context(), serviceID)
	if err != nil {
		s.writeServiceError(c, err)
		return
	}
	var input slo.Input
	if !decodeJSON(c, &input) {
		return
	}
	policy, err := s.options.SLO.UpdatePolicy(c.Request.Context(), serviceID, input)
	if err != nil {
		writeSLOError(c, err)
		return
	}
	s.audit(c, principal, "service.slo.update", serviceID, "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: operations.EventServiceSLOChanged, Title: "Service objective updated", Summary: service.Name,
		NodeID: history.LocalNodeID, ServiceID: serviceID, Actor: principal.Login,
	})
	c.JSON(http.StatusOK, policy)
}

func parseSLOWindow(c *gin.Context) (int, bool) {
	raw := strings.TrimSpace(c.Query("window"))
	if raw == "" {
		return 0, true
	}
	window, err := strconv.Atoi(raw)
	if err != nil || (window != 7 && window != 30 && window != 90) {
		writeError(c, http.StatusBadRequest, "invalid_slo_window", "SLO window must be 7, 30, or 90 days.", nil)
		return 0, false
	}
	return window, true
}

func writeSLOError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, slo.ErrInvalidPolicy), errors.Is(err, slo.ErrInvalidWindow):
		writeError(c, http.StatusUnprocessableEntity, "invalid_slo_policy", "SLO target must be 90–99.999% and the window must be 7, 30, or 90 days.", nil)
	default:
		writeError(c, http.StatusInternalServerError, "slo_unavailable", "Unable to calculate the service objective.", nil)
	}
}

func (s *Server) listOperationalEvents(c *gin.Context) {
	filter, ok := parseEventFilter(c)
	if !ok {
		return
	}
	// The standard timeline is available to all authenticated viewers. Keep
	// sensitive records private by default even if a future internal writer
	// uses the enum; a separate, explicitly authorized endpoint can expose
	// those records later if the product needs it.
	filter.Visibility = operations.VisibilityNormal
	items, err := s.options.Operations.ListOperationalEvents(c.Request.Context(), filter)
	if err != nil {
		writeOperationsError(c, err)
		return
	}
	if principalFromContext(c).Role != auth.RoleAdmin {
		for index := range items {
			items[index].Actor = ""
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) createOperationalEvent(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var event operations.Event
	if !decodeJSON(c, &event) {
		return
	}
	event.Source = operations.SourceManual
	event.Visibility = operations.VisibilityNormal
	event.Actor = principal.Login
	// Timeline entries describe the dashboard action that has just occurred.
	// Do not let a browser backdate or pre-seed a future operational record.
	event.OccurredAt = time.Time{}
	created, err := s.options.Operations.CreateManualOperationalEvent(c.Request.Context(), event)
	if err != nil {
		writeOperationsError(c, err)
		return
	}
	s.audit(c, principal, "operations.event.create", strconv.FormatInt(created.ID, 10), "success")
	c.JSON(http.StatusCreated, created)
}

func parseEventFilter(c *gin.Context) (operations.Filter, bool) {
	filter := operations.Filter{
		Type: strings.TrimSpace(c.Query("type")), NodeID: strings.TrimSpace(c.Query("node")),
		ServiceID: strings.TrimSpace(c.Query("service")), Source: operations.Source(strings.TrimSpace(c.Query("source"))),
		Limit: 100,
	}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid_event_filter", "Event limit must be an integer.", nil)
			return operations.Filter{}, false
		}
		filter.Limit = value
	}
	for key, target := range map[string]*time.Time{"from": &filter.From, "to": &filter.To} {
		if raw := strings.TrimSpace(c.Query(key)); raw != "" {
			value, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeError(c, http.StatusBadRequest, "invalid_event_filter", "Event timestamps must use RFC3339 format.", nil)
				return operations.Filter{}, false
			}
			*target = value
		}
	}
	return filter, true
}

func writeOperationsError(c *gin.Context, err error) {
	if errors.Is(err, operations.ErrInvalidEvent) || errors.Is(err, operations.ErrInvalidFilter) {
		writeError(c, http.StatusUnprocessableEntity, "invalid_operational_event", "The operational event or filter is invalid.", nil)
		return
	}
	writeError(c, http.StatusInternalServerError, "operations_unavailable", "Unable to access the operational timeline.", nil)
}

func (s *Server) listTopologyDependencies(c *gin.Context) {
	nodeID := strings.TrimSpace(c.DefaultQuery("node", "local"))
	items, err := s.options.Topology.ListTopologyDependencies(c.Request.Context(), nodeID)
	if err != nil {
		writeTopologyError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) createTopologyDependency(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	var input topology.DependencyInput
	if !decodeJSON(c, &input) {
		return
	}
	if strings.TrimSpace(input.NodeID) == "" {
		input.NodeID = "local"
	}
	item, err := s.options.Topology.CreateTopologyDependency(c.Request.Context(), input)
	if err != nil {
		writeTopologyError(c, err)
		return
	}
	s.audit(c, principal, "topology.dependency.create", strconv.FormatInt(item.ID, 10), "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: operations.EventTopologyChanged, Title: "Topology dependency added",
		Summary: item.DependentServiceID + " depends on " + item.DependencyServiceID,
		NodeID:  item.NodeID, ServiceID: item.DependentServiceID, Actor: principal.Login,
	})
	c.JSON(http.StatusCreated, item)
}

func (s *Server) deleteTopologyDependency(c *gin.Context) {
	principal, ok := s.authorizeMutation(c, true)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_dependency_id", "Topology dependency id is invalid.", nil)
		return
	}
	nodeID := strings.TrimSpace(c.DefaultQuery("node", "local"))
	if err := s.options.Topology.DeleteTopologyDependency(c.Request.Context(), nodeID, id); err != nil {
		writeTopologyError(c, err)
		return
	}
	s.audit(c, principal, "topology.dependency.delete", strconv.FormatInt(id, 10), "success")
	s.recordAutomaticEvent(c.Request.Context(), operations.Event{
		Type: operations.EventTopologyChanged, Title: "Topology dependency removed",
		Summary: "Dependency " + strconv.FormatInt(id, 10), NodeID: nodeID, Actor: principal.Login,
	})
	c.Status(http.StatusNoContent)
}

func writeTopologyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, topology.ErrDependencyNotFound):
		writeError(c, http.StatusNotFound, "topology_dependency_not_found", "The topology dependency does not exist.", nil)
	case errors.Is(err, topology.ErrSelfDependency), errors.Is(err, topology.ErrDuplicateDependency), errors.Is(err, topology.ErrServiceNotFound), errors.Is(err, topology.ErrNodeNotFound), errors.Is(err, topology.ErrInvalidDependency):
		writeError(c, http.StatusUnprocessableEntity, "invalid_topology_dependency", "The topology dependency is invalid or references an unknown service.", nil)
	default:
		writeError(c, http.StatusInternalServerError, "topology_unavailable", "Unable to update service topology.", nil)
	}
}

type certificateCheck struct {
	healthchecks.CertificateObservation
	ServiceName string `json:"serviceName,omitempty"`
	DisplayURL  string `json:"displayUrl,omitempty"`
	DaysLeft    *int   `json:"daysLeft,omitempty"`
	Level       string `json:"level"`
}

type backupCheck struct {
	healthchecks.BackupObservation
	Healthy bool   `json:"healthy"`
	Age     int64  `json:"ageSeconds"`
	Reason  string `json:"reason,omitempty"`
}

func (s *Server) listOperationalChecks(c *gin.Context) {
	certificates, err := s.options.Checks.ListCertificateObservations(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "checks_unavailable", "Unable to list certificate checks.", nil)
		return
	}
	services, err := s.options.Services.ListServices(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "checks_unavailable", "Unable to list configured services.", nil)
		return
	}
	byID := make(map[string]struct{ name, url string }, len(services))
	for _, service := range services {
		byID[service.ID] = struct{ name, url string }{service.Name, service.DisplayURL}
	}
	now := time.Now().UTC()
	certificateItems := make([]certificateCheck, 0, len(certificates))
	for _, item := range certificates {
		service := byID[item.ServiceID]
		check := certificateCheck{CertificateObservation: item, ServiceName: service.name, DisplayURL: service.url, Level: "ok"}
		if item.Error != "" {
			check.Level = "critical"
		} else if !item.NotAfter.IsZero() {
			days := int(item.NotAfter.Sub(now).Hours() / 24)
			check.DaysLeft = &days
			switch {
			case days < 7:
				check.Level = "critical"
			case days < 30:
				check.Level = "warning"
			}
		}
		certificateItems = append(certificateItems, check)
	}
	nodeID := strings.TrimSpace(c.Query("node"))
	backups, err := s.options.Checks.ListBackupObservations(c.Request.Context(), nodeID)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "checks_unavailable", "Unable to list backup checks.", nil)
		return
	}
	backupItems := make([]backupCheck, 0, len(backups))
	for _, item := range backups {
		healthy, age, reason := healthchecks.BackupFreshness(item.Status, now)
		backupItems = append(backupItems, backupCheck{BackupObservation: item, Healthy: healthy, Age: int64(age.Seconds()), Reason: reason})
	}
	c.JSON(http.StatusOK, gin.H{"certificates": certificateItems, "backups": backupItems})
}
