//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/auth"
	"github.com/binhminh/HomeLab-Minh/internal/metrics"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/podman"
	"github.com/binhminh/HomeLab-Minh/internal/services"
	"github.com/binhminh/HomeLab-Minh/internal/terminal"
	"github.com/gorilla/websocket"
)

type streamingTerminalBackend struct{}

func (streamingTerminalBackend) Logs(context.Context, string, podman.LogsOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("service ready\n")), nil
}
func (streamingTerminalBackend) CreateShellExec(context.Context, string, podman.Shell, podman.TerminalSize) (string, error) {
	return "exec-id", nil
}
func (streamingTerminalBackend) StartExec(context.Context, string, podman.TerminalSize) (io.ReadWriteCloser, error) {
	return nil, io.EOF
}
func (streamingTerminalBackend) ResizeExec(context.Context, string, podman.TerminalSize) error {
	return nil
}
func (streamingTerminalBackend) RemoveExec(context.Context, string, bool) error { return nil }

type liveTestServer struct {
	URL    string
	Cookie *http.Cookie
	CSRF   string
	Client *http.Client
	Close  func()
}

func newLiveTestServer(t *testing.T) liveTestServer {
	t.Helper()
	repository := &memoryServices{items: make(map[string]model.Service)}
	serviceManager := services.NewManager(repository)
	hub := metrics.NewHub(metrics.Sources{Host: fixedHost{}, Services: serviceManager}, time.Hour)
	if _, err := hub.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminalManager, err := terminal.NewManager(streamingTerminalBackend{}, terminal.ManagerOptions{})
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
	live := httptest.NewServer(server.Handler())

	request, _ := http.NewRequest(http.MethodGet, live.URL+"/api/v1/session", nil)
	request.Header.Set("Tailscale-User-Login", "admin@example.com")
	response, err := live.Client().Do(request)
	if err != nil {
		live.Close()
		t.Fatal(err)
	}
	defer response.Body.Close()
	var session struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		live.Close()
		t.Fatal(err)
	}
	return liveTestServer{URL: live.URL, Cookie: response.Cookies()[0], CSRF: session.CSRF, Client: live.Client(), Close: live.Close}
}

func (s liveTestServer) headers() http.Header {
	header := make(http.Header)
	header.Set("Origin", s.URL)
	header.Set("Tailscale-User-Login", "admin@example.com")
	header.Set("Cookie", s.Cookie.String())
	return header
}

func websocketURL(httpURL, path string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + path
}

func TestCompatibleMetricsWebSocketAndOrigin(t *testing.T) {
	live := newLiveTestServer(t)
	defer live.Close()

	connection, response, err := websocket.DefaultDialer.Dial(websocketURL(live.URL, "/ws/metrics"), live.headers())
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatal(err)
	}
	defer connection.Close()
	_, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		System struct {
			Hostname string `json:"hostname"`
		} `json:"system"`
	}
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.System.Hostname != "test-host" {
		t.Fatalf("legacy metrics hostname=%q", snapshot.System.Hostname)
	}

	badHeaders := live.headers()
	badHeaders.Set("Origin", "http://evil.example")
	badConnection, badResponse, badErr := websocket.DefaultDialer.Dial(websocketURL(live.URL, "/ws/metrics"), badHeaders)
	if badConnection != nil {
		badConnection.Close()
	}
	if badErr == nil || badResponse == nil || badResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin websocket err=%v status=%v", badErr, badResponse)
	}
	badResponse.Body.Close()
}

func TestTerminalSessionStreamsOverCompatibleWebSocket(t *testing.T) {
	live := newLiveTestServer(t)
	defer live.Close()

	body := bytes.NewBufferString(`{"mode":"logs","containerId":"container-1","tail":20,"follow":false,"cols":80,"rows":24}`)
	request, _ := http.NewRequest(http.MethodPost, live.URL+"/api/v1/terminal/sessions", body)
	request.Header = live.headers()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", live.CSRF)
	response, err := live.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("terminal create status=%d body=%s", response.StatusCode, payload)
	}
	var created struct {
		WebSocketURL string `json:"websocketUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.WebSocketURL, "/ws/terminal?session=") {
		t.Fatalf("unexpected compatible terminal URL %q", created.WebSocketURL)
	}

	connection, _, err := websocket.DefaultDialer.Dial(websocketURL(live.URL, created.WebSocketURL), live.headers())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	messageType, ready, err := connection.ReadMessage()
	if err != nil || messageType != websocket.TextMessage || !bytes.Contains(ready, []byte(`"type":"ready"`)) {
		t.Fatalf("terminal ready type=%d payload=%s err=%v", messageType, ready, err)
	}
	messageType, output, err := connection.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage || string(output) != "service ready\n" {
		t.Fatalf("terminal output type=%d payload=%q err=%v", messageType, output, err)
	}
}
