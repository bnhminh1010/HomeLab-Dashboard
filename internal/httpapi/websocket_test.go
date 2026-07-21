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
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/auth"
	"github.com/bnhminh1010/homelab-dashboard/internal/metrics"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/podman"
	"github.com/bnhminh1010/homelab-dashboard/internal/services"
	"github.com/bnhminh1010/homelab-dashboard/internal/terminal"
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
	Server *Server
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
		Auth: auth.NewManager([]string{"admin@example.com"}, true, false), Metrics: hub,
		Services: serviceManager, Terminal: terminalManager, Static: static,
	})
	if err != nil {
		t.Fatal(err)
	}
	live := httptest.NewServer(server.Handler())

	request, _ := http.NewRequest(http.MethodPost, live.URL+"/api/v1/session", nil)
	request.Header.Set("Origin", live.URL)
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
	return liveTestServer{URL: live.URL, Cookie: response.Cookies()[0], CSRF: session.CSRF, Client: live.Client(), Server: server, Close: live.Close}
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

func TestServerShutdownClosesMetricsWebSocketHandler(t *testing.T) {
	live := newLiveTestServer(t)
	defer live.Close()

	connection, _, err := websocket.DefaultDialer.Dial(websocketURL(live.URL, "/ws/metrics"), live.headers())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, _, err := connection.ReadMessage(); err != nil {
		t.Fatalf("read initial metrics snapshot: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := live.Server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown WebSockets: %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := connection.ReadMessage(); err == nil {
		t.Fatal("metrics WebSocket remained open after shutdown")
	}
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

func TestHostTerminalStreamsMetadataAndAuditsClose(t *testing.T) {
	stream := newWebsocketHostStream()
	audit := &memoryAudit{}
	live := newHostLiveTestServer(t, stream, audit)
	defer live.Close()

	body := bytes.NewBufferString(`{"cols":100,"rows":25}`)
	request, _ := http.NewRequest(http.MethodPost, live.URL+"/api/v1/terminal/host-sessions", body)
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
		t.Fatalf("host terminal create status=%d body=%s", response.StatusCode, payload)
	}
	var created struct {
		ID           string `json:"id"`
		WebSocketURL string `json:"websocketUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.WebSocketURL != "/ws/v1/terminal/"+created.ID {
		t.Fatalf("host terminal URL = %q", created.WebSocketURL)
	}

	connection, _, err := websocket.DefaultDialer.Dial(websocketURL(live.URL, created.WebSocketURL), live.headers())
	if err != nil {
		t.Fatal(err)
	}
	_, ready, err := connection.ReadMessage()
	if err != nil || !bytes.Contains(ready, []byte(`"hostname":"test-host"`)) || !bytes.Contains(ready, []byte(`"user":"binhminh"`)) || !bytes.Contains(ready, []byte(`"shell":"/bin/bash"`)) {
		t.Fatalf("host terminal ready=%s err=%v", ready, err)
	}
	if err := connection.WriteMessage(websocket.BinaryMessage, []byte("whoami\n")); err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteJSON(map[string]any{"type": "resize", "cols": 140, "rows": 40}); err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteJSON(map[string]any{"type": "close"}); err != nil {
		t.Fatal(err)
	}
	_, _, _ = connection.ReadMessage()
	_ = connection.Close()

	deadline := time.Now().Add(time.Second)
	for len(audit.snapshot()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	events := audit.snapshot()
	if len(events) != 2 || events[1].Action != "terminal.host.close" || events[1].TargetID != "test-host" || events[1].Outcome != "success" || events[1].Metadata["closeReason"] != "completed" {
		t.Fatalf("host terminal audits = %#v", events)
	}
	encodedAudit, _ := json.Marshal(events)
	if bytes.Contains(encodedAudit, []byte(created.ID)) {
		t.Fatal("host terminal audit contains bearer session ID")
	}
	if stream.written() != "whoami\n" || stream.sizeSnapshot() != (terminal.HostSize{Cols: 140, Rows: 40}) {
		t.Fatalf("host terminal stream input=%q resize=%#v", stream.written(), stream.sizeSnapshot())
	}
}

func TestHostTerminalHeartbeatClosesSilentPeer(t *testing.T) {
	stream := newWebsocketHostStream()
	audit := &memoryAudit{}
	live := newHostLiveTestServerWithHeartbeat(t, stream, audit, 25*time.Millisecond, 100*time.Millisecond)
	defer live.Close()

	body := bytes.NewBufferString(`{"cols":100,"rows":25}`)
	request, _ := http.NewRequest(http.MethodPost, live.URL+"/api/v1/terminal/host-sessions", body)
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
		t.Fatalf("host terminal create status=%d body=%s", response.StatusCode, payload)
	}
	var created struct {
		WebSocketURL string `json:"websocketUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	connection, _, err := websocket.DefaultDialer.Dial(websocketURL(live.URL, created.WebSocketURL), live.headers())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, _, err := connection.ReadMessage(); err != nil {
		t.Fatalf("read ready message: %v", err)
	}
	// Stop reading so the client cannot process the server ping and return a pong.
	select {
	case <-stream.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("host stream remained open after the WebSocket heartbeat deadline")
	}

	deadline := time.Now().Add(time.Second)
	for len(audit.snapshot()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	events := audit.snapshot()
	if len(events) != 2 || events[1].Outcome != "interrupted" || events[1].Metadata["closeReason"] != "peer_disconnected" {
		t.Fatalf("host terminal heartbeat audit = %#v", events)
	}
}

func TestServerShutdownClosesTerminalStreamAndHandler(t *testing.T) {
	stream := newWebsocketHostStream()
	live := newHostLiveTestServer(t, stream, &memoryAudit{})
	defer live.Close()

	body := bytes.NewBufferString(`{"cols":100,"rows":25}`)
	request, _ := http.NewRequest(http.MethodPost, live.URL+"/api/v1/terminal/host-sessions", body)
	request.Header = live.headers()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", live.CSRF)
	response, err := live.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var created struct {
		WebSocketURL string `json:"websocketUrl"`
	}
	if response.StatusCode != http.StatusCreated || json.NewDecoder(response.Body).Decode(&created) != nil {
		t.Fatalf("create host terminal status = %d", response.StatusCode)
	}
	connection, _, err := websocket.DefaultDialer.Dial(websocketURL(live.URL, created.WebSocketURL), live.headers())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, _, err := connection.ReadMessage(); err != nil {
		t.Fatalf("read terminal ready message: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := live.Server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown WebSockets: %v", err)
	}
	select {
	case <-stream.closed:
	default:
		t.Fatal("terminal backend remained open after shutdown")
	}
}

func newHostLiveTestServer(t *testing.T, stream terminal.HostStream, audit AuditWriter) liveTestServer {
	return newHostLiveTestServerWithHeartbeat(t, stream, audit, defaultTerminalPingPeriod, defaultTerminalPongWait)
}

func newHostLiveTestServerWithHeartbeat(t *testing.T, stream terminal.HostStream, audit AuditWriter, pingPeriod, pongWait time.Duration) liveTestServer {
	t.Helper()
	repository := &memoryServices{items: make(map[string]model.Service)}
	serviceManager := services.NewManager(repository)
	hub := metrics.NewHub(metrics.Sources{Host: fixedHost{}, Services: serviceManager}, time.Hour)
	if _, err := hub.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminalManager, err := terminal.NewManagerWithHost(streamingTerminalBackend{}, &testHostBackend{stream: stream}, terminal.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	static, _ := NewStaticHandler(fstest.MapFS{"index.html": {Data: []byte("dashboard")}})
	server, err := New(Options{
		Auth: auth.NewManager([]string{"admin@example.com"}, true, false), Metrics: hub,
		Services: serviceManager, Terminal: terminalManager, Static: static, Audit: audit,
		HostShellEnabled: true, HostShellUsers: []string{"admin@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.terminalPingPeriod = pingPeriod
	server.terminalPongWait = pongWait
	live := httptest.NewServer(server.Handler())
	request, _ := http.NewRequest(http.MethodPost, live.URL+"/api/v1/session", nil)
	request.Header.Set("Origin", live.URL)
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
	return liveTestServer{URL: live.URL, Cookie: response.Cookies()[0], CSRF: session.CSRF, Client: live.Client(), Server: server, Close: live.Close}
}

type websocketHostStream struct {
	mu     sync.Mutex
	writes bytes.Buffer
	size   terminal.HostSize
	closed chan struct{}
	once   sync.Once
}

func newWebsocketHostStream() *websocketHostStream {
	return &websocketHostStream{closed: make(chan struct{})}
}

func (stream *websocketHostStream) Read([]byte) (int, error) {
	<-stream.closed
	return 0, io.EOF
}

func (stream *websocketHostStream) Write(payload []byte) (int, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.writes.Write(payload)
}

func (stream *websocketHostStream) Close() error {
	stream.once.Do(func() { close(stream.closed) })
	return nil
}

func (stream *websocketHostStream) Resize(_ context.Context, size terminal.HostSize) error {
	stream.mu.Lock()
	stream.size = size
	stream.mu.Unlock()
	return nil
}

func (*websocketHostStream) Info() terminal.HostInfo {
	return terminal.HostInfo{Hostname: "test-host", User: "binhminh", Shell: "/bin/bash"}
}

func (*websocketHostStream) ExitCode() (int, bool) { return 0, false }

func (stream *websocketHostStream) written() string {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.writes.String()
}

func (stream *websocketHostStream) sizeSnapshot() terminal.HostSize {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.size
}
