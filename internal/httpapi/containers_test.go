package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/auth"
	"github.com/bnhminh1010/homelab-dashboard/internal/metrics"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/operations"
	"github.com/bnhminh1010/homelab-dashboard/internal/podman"
	"github.com/bnhminh1010/homelab-dashboard/internal/services"
	"github.com/bnhminh1010/homelab-dashboard/internal/terminal"
)

type recordedContainerLifecycle struct {
	restartNode string
	restartID   string
	stopNode    string
	stopID      string
	err         error
}

func (lifecycle *recordedContainerLifecycle) Restart(_ context.Context, nodeID, containerID string) error {
	lifecycle.restartNode, lifecycle.restartID = nodeID, containerID
	return lifecycle.err
}

func (lifecycle *recordedContainerLifecycle) Stop(_ context.Context, nodeID, containerID string) error {
	lifecycle.stopNode, lifecycle.stopID = nodeID, containerID
	return lifecycle.err
}

type recordedOperationalEvents struct {
	events []operations.Event
}

func (repository *recordedOperationalEvents) RecordOperationalEvent(_ context.Context, event operations.Event) (operations.Event, error) {
	event = operations.NormalizeEvent(event)
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	if err := operations.ValidateEvent(event); err != nil {
		return operations.Event{}, err
	}
	event.ID = int64(len(repository.events) + 1)
	repository.events = append(repository.events, event)
	return event, nil
}

func (repository *recordedOperationalEvents) CreateManualOperationalEvent(ctx context.Context, event operations.Event) (operations.Event, error) {
	event.Source = operations.SourceManual
	return repository.RecordOperationalEvent(ctx, event)
}

func (repository *recordedOperationalEvents) ListOperationalEvents(context.Context, operations.Filter) ([]operations.Event, error) {
	return append([]operations.Event(nil), repository.events...), nil
}

func (*recordedOperationalEvents) PurgeOperationalEvents(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (repository *recordedOperationalEvents) snapshot() []operations.Event {
	return append([]operations.Event(nil), repository.events...)
}

func newContainerLifecycleTestServer(t *testing.T, lifecycle ContainerLifecycle, audit AuditWriter) (*Server, *recordedOperationalEvents) {
	t.Helper()
	operationalEvents := &recordedOperationalEvents{}
	repository := &memoryServices{items: make(map[string]model.Service)}
	serviceManager := services.NewManager(repository)
	hub := metrics.NewHub(metrics.Sources{Host: fixedHost{}}, time.Hour)
	if _, err := hub.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminalManager, err := terminal.NewManager(unusedTerminalBackend{}, terminal.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	static, err := NewStaticHandler(fstest.MapFS{"index.html": {Data: []byte("dashboard")}})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Auth: auth.NewManager([]string{"admin@example.com"}, true, false), Metrics: hub,
		Services: serviceManager, Terminal: terminalManager, Static: static, Audit: audit,
		ContainerLifecycle: lifecycle, Operations: operationalEvents,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, operationalEvents
}

func TestContainerLifecycleRequiresAdminCSRFAndAudit(t *testing.T) {
	lifecycle := &recordedContainerLifecycle{}
	audit := &memoryAudit{}
	server, _ := newContainerLifecycleTestServer(t, lifecycle, audit)

	adminCSRF, adminCookie, capabilities := startTestBrowserSession(t, server, "admin@example.com")
	if !capabilities.ManageContainers {
		t.Fatal("admin session did not expose manageContainers")
	}
	viewerCSRF, viewerCookie, viewerCapabilities := startTestBrowserSession(t, server, "viewer@example.com")
	if viewerCapabilities.ManageContainers {
		t.Fatal("viewer session exposed manageContainers")
	}

	viewer := authenticatedMutation(http.MethodPost, "/api/v1/containers/app/restart", `{"nodeId":"local"}`, "viewer@example.com", viewerCSRF, viewerCookie)
	viewerResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(viewerResponse, viewer)
	if viewerResponse.Code != http.StatusForbidden || lifecycle.restartID != "" {
		t.Fatalf("viewer restart status=%d calls=%+v", viewerResponse.Code, lifecycle)
	}

	missingCSRF := authenticatedMutation(http.MethodPost, "/api/v1/containers/app/restart", `{"nodeId":"local"}`, "admin@example.com", "", adminCookie)
	missingCSRFResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(missingCSRFResponse, missingCSRF)
	if missingCSRFResponse.Code != http.StatusForbidden || lifecycle.restartID != "" {
		t.Fatalf("missing csrf restart status=%d calls=%+v", missingCSRFResponse.Code, lifecycle)
	}

	audit.err = errors.New("audit down")
	auditUnavailable := authenticatedMutation(http.MethodPost, "/api/v1/containers/app/restart", `{"nodeId":"local"}`, "admin@example.com", adminCSRF, adminCookie)
	auditUnavailableResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(auditUnavailableResponse, auditUnavailable)
	if auditUnavailableResponse.Code != http.StatusServiceUnavailable || lifecycle.restartID != "" {
		t.Fatalf("audit unavailable restart status=%d calls=%+v", auditUnavailableResponse.Code, lifecycle)
	}

	audit.err = nil
	restart := authenticatedMutation(http.MethodPost, "/api/v1/containers/app/restart", `{"nodeId":"edge-1"}`, "admin@example.com", adminCSRF, adminCookie)
	restartResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(restartResponse, restart)
	if restartResponse.Code != http.StatusOK || lifecycle.restartID != "app" || lifecycle.restartNode != "edge-1" {
		t.Fatalf("restart status=%d calls=%+v body=%s", restartResponse.Code, lifecycle, restartResponse.Body.String())
	}
	events := audit.snapshot()
	if len(events) != 2 || events[0].Outcome != "requested" || events[1].Outcome != "success" {
		t.Fatalf("restart audits = %#v", events)
	}
}

func TestContainerLifecycleMapsRuntimeErrors(t *testing.T) {
	lifecycle := &recordedContainerLifecycle{err: podman.ErrContainerNotRunning}
	server, operationalEvents := newContainerLifecycleTestServer(t, lifecycle, &memoryAudit{})
	csrf, cookie, _ := startTestBrowserSession(t, server, "admin@example.com")

	request := authenticatedMutation(http.MethodPost, "/api/v1/containers/app/stop", `{}`, "admin@example.com", csrf, cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict || lifecycle.stopID != "app" || lifecycle.stopNode != "local" {
		t.Fatalf("stop status=%d calls=%+v body=%s", response.Code, lifecycle, response.Body.String())
	}
	events := server.options.Audit.(*memoryAudit).snapshot()
	if len(events) != 2 || events[1].Outcome != "denied" {
		t.Fatalf("stop audits = %#v", events)
	}
	if events := operationalEvents.snapshot(); len(events) != 0 {
		t.Fatalf("failed stop operational events = %#v", events)
	}
}

func TestContainerLifecyclePersistsSuccessfulRestartAndStopEvents(t *testing.T) {
	lifecycle := &recordedContainerLifecycle{}
	server, operationalEvents := newContainerLifecycleTestServer(t, lifecycle, &memoryAudit{})
	csrf, cookie, _ := startTestBrowserSession(t, server, "admin@example.com")

	for _, endpoint := range []string{
		"/api/v1/containers/app/restart",
		"/api/v1/containers/worker/stop",
	} {
		request := authenticatedMutation(http.MethodPost, endpoint, `{"nodeId":"edge-1"}`, "admin@example.com", csrf, cookie)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body.String())
		}
	}

	events := operationalEvents.snapshot()
	if len(events) != 2 {
		t.Fatalf("operational events = %#v", events)
	}
	assertContainerEvent(t, events[0], operations.EventContainerRestarted, "app")
	assertContainerEvent(t, events[1], operations.EventContainerStopped, "worker")
}

func assertContainerEvent(t *testing.T, event operations.Event, eventType, containerID string) {
	t.Helper()
	if event.Type != eventType || event.Source != operations.SourceAutomatic || event.Visibility != operations.VisibilityNormal ||
		event.NodeID != "edge-1" || event.ContainerID != containerID || event.Actor != "admin@example.com" || event.OccurredAt.IsZero() {
		t.Fatalf("container event = %#v", event)
	}
}
