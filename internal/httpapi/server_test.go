package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/auth"
	"github.com/binhminh/HomeLab-Minh/internal/metrics"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/podman"
	"github.com/binhminh/HomeLab-Minh/internal/services"
	"github.com/binhminh/HomeLab-Minh/internal/store"
	"github.com/binhminh/HomeLab-Minh/internal/terminal"
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
		Auth: auth.NewManager([]string{"admin@example.com"}, false), Metrics: hub,
		Services: serviceManager, Terminal: terminalManager, Static: static,
	})
	if err != nil {
		t.Fatal(err)
	}

	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	sessionRequest.Header.Set("Tailscale-User-Login", "admin@example.com")
	sessionResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}
	var sessionBody struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &sessionBody); err != nil {
		t.Fatal(err)
	}
	cookie := sessionResponse.Result().Cookies()[0]

	createBody := []byte(`{"name":"Immich","icon":"📸","displayUrl":"https://immich.example.ts.net"}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/services", bytes.NewReader(createBody))
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
	compatibilityRequest := httptest.NewRequest(http.MethodPost, "/api/services", bytes.NewReader(compatibilityBody))
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
	terminalRequest := httptest.NewRequest(http.MethodPost, "/api/v1/terminal/sessions", bytes.NewReader(terminalBody))
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
	cancelRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/terminal/sessions/"+terminalSession.ID, nil)
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

	snapshotRequest := httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil)
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
	authManager := auth.NewManager([]string{"admin@example.com"}, false)
	server, _ := New(Options{Auth: authManager, Metrics: hub, Services: serviceManager, Terminal: terminalManager, Static: static})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	r.Header.Set("Tailscale-User-Login", "viewer@example.com")
	server.Handler().ServeHTTP(w, r)
	var body struct {
		CSRF string `json:"csrfToken"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)

	mutation := httptest.NewRequest(http.MethodPost, "/api/v1/services", bytes.NewReader([]byte(`{"name":"x","displayUrl":"https://example.ts.net"}`)))
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
