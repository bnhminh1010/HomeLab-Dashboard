package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/auth"
	"github.com/bnhminh1010/homelab-dashboard/internal/metrics"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/podman"
	"github.com/bnhminh1010/homelab-dashboard/internal/services"
	"github.com/bnhminh1010/homelab-dashboard/internal/store"
	"github.com/bnhminh1010/homelab-dashboard/internal/terminal"
)

type memoryServices struct {
	mu    sync.Mutex
	items map[string]model.Service
}

func (m *memoryServices) ListServices(context.Context) ([]model.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]model.Service, 0, len(m.items))
	for _, item := range m.items {
		result = append(result, item)
	}
	return result, nil
}
func (m *memoryServices) GetService(_ context.Context, id string) (model.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[id]
	if !ok {
		return model.Service{}, store.ErrNotFound
	}
	return item, nil
}
func (m *memoryServices) CreateService(_ context.Context, item model.Service) (model.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[item.ID] = item
	return item, nil
}
func (m *memoryServices) UpdateService(_ context.Context, id string, input model.ServiceInput) (model.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[id]
	if !ok {
		return model.Service{}, store.ErrNotFound
	}
	item.Name, item.Icon, item.DisplayURL, item.ProbeURL = input.Name, input.Icon, input.DisplayURL, input.ProbeURL
	m.items[id] = item
	return item, nil
}
func (m *memoryServices) DeleteService(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return store.ErrNotFound
	}
	delete(m.items, id)
	return nil
}

type fixedHost struct{}

func (fixedHost) Collect(context.Context) (metrics.HostSnapshot, error) {
	return metrics.HostSnapshot{System: model.SystemStats{Hostname: "test-host"}}, nil
}

type unusedTerminalBackend struct{}

func (unusedTerminalBackend) Logs(context.Context, string, podman.LogsOptions) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (unusedTerminalBackend) CreateShellExec(context.Context, string, podman.Shell, podman.TerminalSize) (string, error) {
	return "exec-id", nil
}
func (unusedTerminalBackend) StartExec(context.Context, string, podman.TerminalSize) (io.ReadWriteCloser, error) {
	return nil, io.EOF
}
func (unusedTerminalBackend) ResizeExec(context.Context, string, podman.TerminalSize) error {
	return nil
}
func (unusedTerminalBackend) RemoveExec(context.Context, string, bool) error { return nil }

type testHostBackend struct {
	probeErr error
	stream   terminal.HostStream
}

func (backend *testHostBackend) Probe(context.Context) error { return backend.probeErr }
func (backend *testHostBackend) Open(context.Context, terminal.HostSize) (terminal.HostStream, error) {
	if backend.stream == nil {
		return nil, io.EOF
	}
	return backend.stream, nil
}

type memoryAudit struct {
	mu     sync.Mutex
	err    error
	events []model.AuditEvent
}

func (audit *memoryAudit) AppendAudit(_ context.Context, event model.AuditEvent) error {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	if audit.err != nil {
		return audit.err
	}
	audit.events = append(audit.events, event)
	return nil
}

func (audit *memoryAudit) snapshot() []model.AuditEvent {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	return append([]model.AuditEvent(nil), audit.events...)
}

func TestSessionAndAdminServiceCRUD(t *testing.T) {
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
	server, err := New(Options{
		Auth: auth.NewManager([]string{"admin@example.com"}, true, false), Metrics: hub,
		Services: serviceManager, Terminal: terminalManager, Static: static,
	})
	if err != nil {
		t.Fatal(err)
	}

	sessionRequest := newBrowserSessionRequest("admin@example.com")
	sessionResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}
	var sessionBody struct {
		CSRF         string `json:"csrfToken"`
		Capabilities struct {
			ManageServices bool `json:"manageServices"`
			ContainerExec  bool `json:"containerExec"`
			HostShell      bool `json:"hostShell"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &sessionBody); err != nil {
		t.Fatal(err)
	}
	if !sessionBody.Capabilities.ManageServices || !sessionBody.Capabilities.ContainerExec || sessionBody.Capabilities.HostShell {
		t.Fatalf("unexpected default capabilities: %+v", sessionBody.Capabilities)
	}
	cookie := sessionResponse.Result().Cookies()[0]

	createBody := []byte(`{"name":"Immich","icon":"📸","displayUrl":"https://immich.example.ts.net"}`)
	createRequest := loopbackRequest(httptest.NewRequest(http.MethodPost, "/api/v1/services", bytes.NewReader(createBody)))
	createRequest.Host = "dashboard.test"
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Origin", "http://dashboard.test")
	createRequest.Header.Set("X-CSRF-Token", sessionBody.CSRF)
	createRequest.Header.Set("Tailscale-User-Login", "admin@example.com")
	createRequest.AddCookie(cookie)
	createResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}

	compatibilityBody := []byte(`{"name":"Port only","url":"9090"}`)
	compatibilityRequest := loopbackRequest(httptest.NewRequest(http.MethodPost, "/api/services", bytes.NewReader(compatibilityBody)))
	compatibilityRequest.Host = "dashboard.test"
	compatibilityRequest.Header.Set("Content-Type", "application/json")
	compatibilityRequest.Header.Set("Origin", "http://dashboard.test")
	compatibilityRequest.Header.Set("X-CSRF-Token", sessionBody.CSRF)
	compatibilityRequest.Header.Set("Tailscale-User-Login", "admin@example.com")
	compatibilityRequest.AddCookie(cookie)
	compatibilityResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(compatibilityResponse, compatibilityRequest)
	if compatibilityResponse.Code != http.StatusCreated {
		t.Fatalf("compatibility create status=%d body=%s", compatibilityResponse.Code, compatibilityResponse.Body.String())
	}
	var compatibilityService model.Service
	if err := json.Unmarshal(compatibilityResponse.Body.Bytes(), &compatibilityService); err != nil {
		t.Fatal(err)
	}
	if compatibilityService.DisplayURL != "http://dashboard.test:9090" {
		t.Fatalf("compatibility port shorthand display URL=%q", compatibilityService.DisplayURL)
	}

	terminalBody := []byte(`{"mode":"logs","containerId":"container-1","tail":20,"follow":true,"cols":80,"rows":24}`)
	terminalRequest := loopbackRequest(httptest.NewRequest(http.MethodPost, "/api/v1/terminal/sessions", bytes.NewReader(terminalBody)))
	terminalRequest.Host = "dashboard.test"
	terminalRequest.Header.Set("Content-Type", "application/json")
	terminalRequest.Header.Set("Origin", "http://dashboard.test")
	terminalRequest.Header.Set("X-CSRF-Token", sessionBody.CSRF)
	terminalRequest.Header.Set("Tailscale-User-Login", "admin@example.com")
	terminalRequest.AddCookie(cookie)
	terminalResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(terminalResponse, terminalRequest)
	if terminalResponse.Code != http.StatusCreated {
		t.Fatalf("terminal create status=%d body=%s", terminalResponse.Code, terminalResponse.Body.String())
	}
	var terminalSession struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(terminalResponse.Body.Bytes(), &terminalSession); err != nil {
		t.Fatal(err)
	}
	cancelRequest := loopbackRequest(httptest.NewRequest(http.MethodDelete, "/api/v1/terminal/sessions/"+terminalSession.ID, nil))
	cancelRequest.Host = "dashboard.test"
	cancelRequest.Header.Set("Origin", "http://dashboard.test")
	cancelRequest.Header.Set("X-CSRF-Token", sessionBody.CSRF)
	cancelRequest.Header.Set("Tailscale-User-Login", "admin@example.com")
	cancelRequest.AddCookie(cookie)
	cancelResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusNoContent {
		t.Fatalf("terminal cancel status=%d body=%s", cancelResponse.Code, cancelResponse.Body.String())
	}

	snapshotRequest := loopbackRequest(httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	snapshotRequest.Header.Set("Tailscale-User-Login", "admin@example.com")
	snapshotRequest.AddCookie(cookie)
	snapshotResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(snapshotResponse, snapshotRequest)
	if snapshotResponse.Code != http.StatusOK || snapshotResponse.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("snapshot status=%d", snapshotResponse.Code)
	}
}

func TestViewerCannotMutateServices(t *testing.T) {
	repository := &memoryServices{items: make(map[string]model.Service)}
	serviceManager := services.NewManager(repository)
	hub := metrics.NewHub(metrics.Sources{Host: fixedHost{}}, time.Hour)
	_, _ = hub.CollectOnce(context.Background())
	terminalManager, _ := terminal.NewManager(unusedTerminalBackend{}, terminal.ManagerOptions{})
	static, _ := NewStaticHandler(fstest.MapFS{"index.html": {Data: []byte("dashboard")}})
	authManager := auth.NewManager([]string{"admin@example.com"}, true, false)
	server, _ := New(Options{Auth: authManager, Metrics: hub, Services: serviceManager, Terminal: terminalManager, Static: static})

	w := httptest.NewRecorder()
	r := newBrowserSessionRequest("viewer@example.com")
	server.Handler().ServeHTTP(w, r)
	var body struct {
		CSRF string `json:"csrfToken"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)

	mutation := loopbackRequest(httptest.NewRequest(http.MethodPost, "/api/v1/services", bytes.NewReader([]byte(`{"name":"x","displayUrl":"https://example.ts.net"}`))))
	mutation.Host = "dashboard.test"
	mutation.Header.Set("Origin", "http://dashboard.test")
	mutation.Header.Set("X-CSRF-Token", body.CSRF)
	mutation.Header.Set("Tailscale-User-Login", "viewer@example.com")
	mutation.AddCookie(w.Result().Cookies()[0])
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, mutation)
	if response.Code != http.StatusForbidden {
		t.Fatalf("viewer mutation status=%d", response.Code)
	}
}

func TestSessionBootstrapRequiresPostAndSameOrigin(t *testing.T) {
	server := newHostShellTestServer(t, &testHostBackend{}, &memoryAudit{})

	getRequest := loopbackRequest(httptest.NewRequest(http.MethodGet, "/api/v1/session", nil))
	getRequest.Host = "dashboard.test"
	getRequest.Header.Set("Tailscale-User-Login", "admin@example.com")
	getResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(getResponse, getRequest)
	if getResponse.Code == http.StatusOK {
		t.Fatal("GET unexpectedly created a browser session")
	}

	for _, origin := range []string{"", "https://evil.example"} {
		request := loopbackRequest(httptest.NewRequest(http.MethodPost, "/api/v1/session", nil))
		request.Host = "dashboard.test"
		request.Header.Set("Origin", origin)
		request.Header.Set("Tailscale-User-Login", "admin@example.com")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "invalid_origin") {
			t.Fatalf("origin %q status=%d body=%s", origin, response.Code, response.Body.String())
		}
	}

	request := newBrowserSessionRequest("admin@example.com")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 1 {
		t.Fatalf("same-origin session status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHostShellReservationIsAllowlistedAuditedAndCanonical(t *testing.T) {
	host := &testHostBackend{}
	audit := &memoryAudit{}
	server := newHostShellTestServer(t, host, audit)

	csrf, cookie, capabilities := startTestBrowserSession(t, server, "admin@example.com")
	if !capabilities.HostShell || !capabilities.ManageServices || !capabilities.ContainerExec {
		t.Fatalf("admin capabilities = %+v", capabilities)
	}

	request := authenticatedMutation(http.MethodPost, "/api/v1/terminal/host-sessions", `{"cols":100,"rows":25}`, "admin@example.com", csrf, cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("host reservation status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		ID           string `json:"id"`
		WebsocketURL string `json:"websocketUrl"`
		ReadOnly     bool   `json:"readOnly"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.WebsocketURL != "/ws/v1/terminal/"+created.ID || created.ReadOnly {
		t.Fatalf("created host session = %+v", created)
	}
	events := audit.snapshot()
	if len(events) != 1 || events[0].Action != "terminal.host.reserve" || events[0].TargetID != "" {
		t.Fatalf("reservation audits = %#v", events)
	}
	if strings.Contains(events[0].TargetID, created.ID) {
		t.Fatal("audit event contains the bearer session ID")
	}
	cancel := authenticatedMutation(http.MethodDelete, "/api/v1/terminal/sessions/"+created.ID, "", "admin@example.com", csrf, cookie)
	cancelResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(cancelResponse, cancel)
	if cancelResponse.Code != http.StatusNoContent {
		t.Fatalf("host cancel status=%d body=%s", cancelResponse.Code, cancelResponse.Body.String())
	}
	events = audit.snapshot()
	if len(events) != 2 || events[1].Action != "terminal.host.cancel" || events[1].TargetType != "host_shell" || events[1].TargetID != "" {
		t.Fatalf("host cancel audits = %#v", events)
	}
	encodedEvents, _ := json.Marshal(events)
	if bytes.Contains(encodedEvents, []byte(created.ID)) {
		t.Fatal("host cancel audit contains the bearer session ID")
	}

	otherCSRF, otherCookie, otherCapabilities := startTestBrowserSession(t, server, "other-admin@example.com")
	if otherCapabilities.HostShell {
		t.Fatal("non-allowlisted admin received host shell capability")
	}
	denied := authenticatedMutation(http.MethodPost, "/api/v1/terminal/host-sessions", `{}`, "other-admin@example.com", otherCSRF, otherCookie)
	deniedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden || !strings.Contains(deniedResponse.Body.String(), "host_shell_forbidden") {
		t.Fatalf("non-allowlisted host shell status=%d body=%s", deniedResponse.Code, deniedResponse.Body.String())
	}
}

func TestHostShellReservationFailsClosedAndMapsAvailability(t *testing.T) {
	host := &testHostBackend{}
	audit := &memoryAudit{err: errors.New("database unavailable")}
	server := newHostShellTestServer(t, host, audit)
	csrf, cookie, _ := startTestBrowserSession(t, server, "admin@example.com")

	request := authenticatedMutation(http.MethodPost, "/api/v1/terminal/host-sessions", `{}`, "admin@example.com", csrf, cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "audit_unavailable") {
		t.Fatalf("audit failure status=%d body=%s", response.Code, response.Body.String())
	}

	audit.err = nil
	retry := authenticatedMutation(http.MethodPost, "/api/v1/terminal/host-sessions", `{}`, "admin@example.com", csrf, cookie)
	retryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusCreated {
		t.Fatalf("reservation was not released after audit failure: status=%d body=%s", retryResponse.Code, retryResponse.Body.String())
	}

	limited := authenticatedMutation(http.MethodPost, "/api/v1/terminal/host-sessions", `{}`, "admin@example.com", csrf, cookie)
	limitedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(limitedResponse, limited)
	if limitedResponse.Code != http.StatusTooManyRequests || !strings.Contains(limitedResponse.Body.String(), "host_shell_limit") {
		t.Fatalf("host limit status=%d body=%s", limitedResponse.Code, limitedResponse.Body.String())
	}

	secondHost := &testHostBackend{probeErr: errors.New("agent offline")}
	secondServer := newHostShellTestServer(t, secondHost, &memoryAudit{})
	secondCSRF, secondCookie, _ := startTestBrowserSession(t, secondServer, "admin@example.com")
	unavailable := authenticatedMutation(http.MethodPost, "/api/v1/terminal/host-sessions", `{}`, "admin@example.com", secondCSRF, secondCookie)
	unavailableResponse := httptest.NewRecorder()
	secondServer.Handler().ServeHTTP(unavailableResponse, unavailable)
	if unavailableResponse.Code != http.StatusServiceUnavailable || !strings.Contains(unavailableResponse.Body.String(), "host_agent_unavailable") {
		t.Fatalf("agent unavailable status=%d body=%s", unavailableResponse.Code, unavailableResponse.Body.String())
	}
}

type testCapabilities struct {
	ManageServices bool `json:"manageServices"`
	ContainerExec  bool `json:"containerExec"`
	HostShell      bool `json:"hostShell"`
}

func newHostShellTestServer(t *testing.T, host terminal.HostBackend, audit AuditWriter) *Server {
	t.Helper()
	repository := &memoryServices{items: make(map[string]model.Service)}
	serviceManager := services.NewManager(repository)
	hub := metrics.NewHub(metrics.Sources{Host: fixedHost{}}, time.Hour)
	_, _ = hub.CollectOnce(context.Background())
	terminalManager, err := terminal.NewManagerWithHost(unusedTerminalBackend{}, host, terminal.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	static, _ := NewStaticHandler(fstest.MapFS{"index.html": {Data: []byte("dashboard")}})
	server, err := New(Options{
		Auth:    auth.NewManager([]string{"admin@example.com", "other-admin@example.com"}, true, false),
		Metrics: hub, Services: serviceManager, Terminal: terminalManager, Static: static, Audit: audit,
		HostShellEnabled: true, HostShellUsers: []string{"admin@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func startTestBrowserSession(t *testing.T, server *Server, login string) (string, *http.Cookie, testCapabilities) {
	t.Helper()
	request := newBrowserSessionRequest(login)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("start session status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		CSRF         string           `json:"csrfToken"`
		Capabilities testCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.CSRF, response.Result().Cookies()[0], body.Capabilities
}

func newBrowserSessionRequest(login string) *http.Request {
	request := loopbackRequest(httptest.NewRequest(http.MethodPost, "/api/v1/session", nil))
	request.Host = "dashboard.test"
	request.Header.Set("Origin", "http://dashboard.test")
	request.Header.Set("Tailscale-User-Login", login)
	return request
}

func authenticatedMutation(method, path, body, login, csrf string, cookie *http.Cookie) *http.Request {
	request := loopbackRequest(httptest.NewRequest(method, path, strings.NewReader(body)))
	request.Host = "dashboard.test"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://dashboard.test")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Tailscale-User-Login", login)
	request.AddCookie(cookie)
	return request
}

func loopbackRequest(request *http.Request) *http.Request {
	request.RemoteAddr = "127.0.0.1:12345"
	return request
}
