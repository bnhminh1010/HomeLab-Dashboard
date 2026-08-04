package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/auth"
	"github.com/bnhminh1010/homelab-dashboard/internal/logs"
	"github.com/bnhminh1010/homelab-dashboard/internal/metrics"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/services"
	"github.com/bnhminh1010/homelab-dashboard/internal/terminal"
)

type testLogsReader struct {
	query logs.Query
}

func (reader *testLogsReader) Status() logs.Status {
	return logs.Status{Enabled: true, Backend: logs.BackendLoki, NodeID: logs.LocalNodeID, RetentionHours: 168}
}

func (reader *testLogsReader) Query(_ context.Context, query logs.Query) (logs.Result, error) {
	reader.query = query
	return logs.Result{Entries: []logs.Entry{{Timestamp: time.Unix(1, 0).UTC(), Line: "healthy", Labels: map[string]string{"node": "local"}}}}, nil
}

func logsTestServer(t *testing.T, reader logs.Reader) *Server {
	t.Helper()
	repository := &memoryServices{items: make(map[string]model.Service)}
	serviceManager := services.NewManager(repository)
	hub := metrics.NewHub(metrics.Sources{Host: fixedHost{}, Services: serviceManager}, time.Hour)
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
	server, err := New(Options{Auth: auth.NewManager([]string{"viewer@example.com"}, true, false), Metrics: hub, Services: serviceManager, Terminal: terminalManager, Static: static, Logs: reader})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func authenticatedLogRequest(path string, cookie *http.Cookie) *http.Request {
	request := loopbackRequest(httptest.NewRequest(http.MethodGet, path, nil))
	request.Host = "dashboard.test"
	request.Header.Set("Tailscale-User-Login", "viewer@example.com")
	request.AddCookie(cookie)
	return request
}

func TestLogsReadEndpointsAllowViewerAndBoundQuery(t *testing.T) {
	reader := &testLogsReader{}
	server := logsTestServer(t, reader)
	session := httptest.NewRecorder()
	server.Handler().ServeHTTP(session, newBrowserSessionRequest("viewer@example.com"))
	if session.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", session.Code, session.Body.String())
	}
	cookie := session.Result().Cookies()[0]

	status := httptest.NewRecorder()
	server.Handler().ServeHTTP(status, authenticatedLogRequest("/api/v1/logs/status", cookie))
	if status.Code != http.StatusOK || !json.Valid(status.Body.Bytes()) {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}

	query := httptest.NewRecorder()
	server.Handler().ServeHTTP(query, authenticatedLogRequest("/api/v1/logs/query?from=2026-08-01T00:00:00Z&to=2026-08-01T01:00:00Z&service=dashboard&limit=20&regex=true", cookie))
	if query.Code != http.StatusOK {
		t.Fatalf("query status=%d body=%s", query.Code, query.Body.String())
	}
	if reader.query.Service != "dashboard" || reader.query.Limit != 20 || reader.query.NodeID != logs.LocalNodeID || !reader.query.IsRegex {
		t.Fatalf("unexpected query: %+v", reader.query)
	}
}

func TestLogsQueryReturnsDisabledWithoutBackend(t *testing.T) {
	server := logsTestServer(t, nil)
	session := httptest.NewRecorder()
	server.Handler().ServeHTTP(session, newBrowserSessionRequest("viewer@example.com"))
	cookie := session.Result().Cookies()[0]
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedLogRequest("/api/v1/logs/query", cookie))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
