package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/alerts"
	"github.com/binhminh/HomeLab-Minh/internal/auth"
	"github.com/binhminh/HomeLab-Minh/internal/dashboardconfig"
	"github.com/binhminh/HomeLab-Minh/internal/history"
	"github.com/binhminh/HomeLab-Minh/internal/metrics"
	"github.com/binhminh/HomeLab-Minh/internal/nodes"
	"github.com/binhminh/HomeLab-Minh/internal/services"
	"github.com/binhminh/HomeLab-Minh/internal/store"
	"github.com/binhminh/HomeLab-Minh/internal/terminal"
)

func TestHistoryAndAlertAPIs(t *testing.T) {
	server, database, notifications := newExtensionTestServer(t)
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := database.WriteHistoryBatch(context.Background(), history.Batch{
		Hosts: []history.HostSample{{
			NodeID: history.LocalNodeID, CollectedAt: now, CPUPercent: 44,
			MemoryUsedBytes: 50, MemoryTotalBytes: 100,
		}},
		Containers: []history.ContainerSample{{
			InstanceID: "archived-container", Name: "Archived worker", Image: "example/worker:old",
			CollectedAt: now, MemoryLimitBytes: 100,
		}},
		ServiceTransitions: []history.ServiceTransition{{
			ServiceID: "archived-service", State: history.ServiceDown, ObservedAt: now,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	csrf, cookie, _ := startTestBrowserSession(t, server, "admin@example.com")

	historyRequest := authenticatedRead("/api/v1/history/system?range=1h", "admin@example.com", cookie)
	historyResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(historyResponse, historyRequest)
	if historyResponse.Code != http.StatusOK || !strings.Contains(historyResponse.Body.String(), `"resolution":"raw"`) || !strings.Contains(historyResponse.Body.String(), `"cpuPercent":44`) {
		t.Fatalf("history status=%d body=%s", historyResponse.Code, historyResponse.Body.String())
	}
	invalidHistoryRequest := authenticatedRead("/api/v1/history/system?range=1h&maxPoints=0", "admin@example.com", cookie)
	invalidHistoryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidHistoryResponse, invalidHistoryRequest)
	if invalidHistoryResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid history point limit status=%d body=%s", invalidHistoryResponse.Code, invalidHistoryResponse.Body.String())
	}
	resourcesRequest := authenticatedRead("/api/v1/history/resources?node=local", "admin@example.com", cookie)
	resourcesResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(resourcesResponse, resourcesRequest)
	if resourcesResponse.Code != http.StatusOK ||
		!strings.Contains(resourcesResponse.Body.String(), `"instanceId":"archived-container"`) ||
		!strings.Contains(resourcesResponse.Body.String(), `"serviceId":"archived-service"`) {
		t.Fatalf("history resources status=%d body=%s", resourcesResponse.Code, resourcesResponse.Body.String())
	}
	invalidResourcesRequest := authenticatedRead("/api/v1/history/resources?node=bad%0Anode", "admin@example.com", cookie)
	invalidResourcesResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidResourcesResponse, invalidResourcesRequest)
	if invalidResourcesResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid history resource node status=%d body=%s", invalidResourcesResponse.Code, invalidResourcesResponse.Body.String())
	}
	invalidSystemNodeRequest := authenticatedRead("/api/v1/history/system?range=1h&node=bad%0Anode", "admin@example.com", cookie)
	invalidSystemNodeResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidSystemNodeResponse, invalidSystemNodeRequest)
	if invalidSystemNodeResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid system history node status=%d body=%s", invalidSystemNodeResponse.Code, invalidSystemNodeResponse.Body.String())
	}
	invalidServiceRequest := authenticatedRead("/api/v1/history/services/bad%0Aid?range=1h", "admin@example.com", cookie)
	invalidServiceResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidServiceResponse, invalidServiceRequest)
	if invalidServiceResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid service history id status=%d body=%s", invalidServiceResponse.Code, invalidServiceResponse.Body.String())
	}

	ruleBody := `{"name":"CPU smoke","resourceType":"host","nodeSelector":"*","resourceSelector":"*","metric":"system.cpu.percent","operator":"gt","threshold":80,"forSeconds":60,"severity":"warning","cooldownSeconds":1800,"enabled":true}`
	create := authenticatedMutation(http.MethodPost, "/api/v1/alert-rules", ruleBody, "admin@example.com", csrf, cookie)
	createResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated || !strings.Contains(createResponse.Body.String(), `"id":"rule_`) {
		t.Fatalf("alert rule create status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	overflowBody := strings.Replace(ruleBody, `"forSeconds":60`, `"forSeconds":9223372036854775807`, 1)
	overflow := authenticatedMutation(http.MethodPost, "/api/v1/alert-rules", overflowBody, "admin@example.com", csrf, cookie)
	overflowResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(overflowResponse, overflow)
	if overflowResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("overflowing alert duration status=%d body=%s", overflowResponse.Code, overflowResponse.Body.String())
	}
	list := authenticatedRead("/api/v1/alert-rules", "admin@example.com", cookie)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), "CPU smoke") {
		t.Fatalf("alert rule list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	testNotification := authenticatedMutation(http.MethodPost, "/api/v1/notifications/ntfy/test", `{}`, "admin@example.com", csrf, cookie)
	notificationResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(notificationResponse, testNotification)
	if notificationResponse.Code != http.StatusOK || notifications.sent != 1 {
		t.Fatalf("ntfy test status=%d sent=%d body=%s", notificationResponse.Code, notifications.sent, notificationResponse.Body.String())
	}

	exportRequest := authenticatedRead("/api/v1/config/export", "admin@example.com", cookie)
	exportResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(exportResponse, exportRequest)
	if exportResponse.Code != http.StatusOK || !strings.Contains(exportResponse.Body.String(), `"version": "homelab-dashboard.config/v1"`) {
		t.Fatalf("config export status=%d body=%s", exportResponse.Code, exportResponse.Body.String())
	}
	for _, forbidden := range []string{"credential", "ntfy", "audit_events", "session"} {
		if strings.Contains(strings.ToLower(exportResponse.Body.String()), forbidden) {
			t.Fatalf("config export leaked %q: %s", forbidden, exportResponse.Body.String())
		}
	}
	previewRequest := authenticatedMutation(http.MethodPost, "/api/v1/config/import/preview?mode=merge", exportResponse.Body.String(), "admin@example.com", csrf, cookie)
	previewResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK || !strings.Contains(previewResponse.Body.String(), `"mode":"merge"`) {
		t.Fatalf("config preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var configPreview dashboardconfig.Preview
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &configPreview); err != nil || configPreview.Revision == "" {
		t.Fatalf("decode config preview revision: %+v err=%v", configPreview, err)
	}
	if previewResponse.Header().Get("ETag") != `"`+configPreview.Revision+`"` {
		t.Fatalf("config preview ETag = %q", previewResponse.Header().Get("ETag"))
	}
	missingRevision := authenticatedMutation(http.MethodPost, "/api/v1/config/import/apply?mode=merge", exportResponse.Body.String(), "admin@example.com", csrf, cookie)
	missingRevisionResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(missingRevisionResponse, missingRevision)
	if missingRevisionResponse.Code != http.StatusPreconditionRequired {
		t.Fatalf("config apply without preview status=%d body=%s", missingRevisionResponse.Code, missingRevisionResponse.Body.String())
	}
	var changedDocument dashboardconfig.Document
	if err := json.Unmarshal(exportResponse.Body.Bytes(), &changedDocument); err != nil {
		t.Fatal(err)
	}
	changedDocument.UIPreferences.TerminalHeight++
	changedPayload, _ := json.Marshal(changedDocument)
	changedRequest := authenticatedMutation(http.MethodPost, "/api/v1/config/import/apply?mode=merge", string(changedPayload), "admin@example.com", csrf, cookie)
	changedRequest.Header.Set("If-Match", `"`+configPreview.Revision+`"`)
	changedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(changedResponse, changedRequest)
	if changedResponse.Code != http.StatusPreconditionFailed {
		t.Fatalf("config apply with different payload status=%d body=%s", changedResponse.Code, changedResponse.Body.String())
	}
	changedModeRequest := authenticatedMutation(http.MethodPost, "/api/v1/config/import/apply?mode=replace", exportResponse.Body.String(), "admin@example.com", csrf, cookie)
	changedModeRequest.Header.Set("If-Match", `"`+configPreview.Revision+`"`)
	changedModeResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(changedModeResponse, changedModeRequest)
	if changedModeResponse.Code != http.StatusPreconditionFailed {
		t.Fatalf("config apply with different mode status=%d body=%s", changedModeResponse.Code, changedModeResponse.Body.String())
	}
	applyRequest := authenticatedMutation(http.MethodPost, "/api/v1/config/import/apply?mode=merge", exportResponse.Body.String(), "admin@example.com", csrf, cookie)
	applyRequest.Header.Set("If-Match", `"`+configPreview.Revision+`"`)
	applyResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(applyResponse, applyRequest)
	if applyResponse.Code != http.StatusOK {
		t.Fatalf("config apply status=%d body=%s", applyResponse.Code, applyResponse.Body.String())
	}
	preferencesRequest := authenticatedMutation(http.MethodPatch, "/api/v1/preferences", `{"historyRange":"7d","defaultNodeId":"local"}`, "admin@example.com", csrf, cookie)
	preferencesResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(preferencesResponse, preferencesRequest)
	if preferencesResponse.Code != http.StatusOK || !strings.Contains(preferencesResponse.Body.String(), `"historyRange":"7d"`) {
		t.Fatalf("preferences update status=%d body=%s", preferencesResponse.Code, preferencesResponse.Body.String())
	}
}

func TestNTFYStatusRedactsDestinationForViewer(t *testing.T) {
	server, _, _ := newExtensionTestServer(t)

	_, adminCookie, _ := startTestBrowserSession(t, server, "admin@example.com")
	adminRequest := authenticatedRead("/api/v1/notifications/ntfy", "admin@example.com", adminCookie)
	adminResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusOK {
		t.Fatalf("admin ntfy status=%d body=%s", adminResponse.Code, adminResponse.Body.String())
	}
	var adminStatus map[string]any
	if err := json.Unmarshal(adminResponse.Body.Bytes(), &adminStatus); err != nil {
		t.Fatal(err)
	}
	if adminStatus["url"] != "https://ntfy.example.com" || adminStatus["topic"] != "homelab" {
		t.Fatalf("admin ntfy destination = %+v", adminStatus)
	}

	_, viewerCookie, _ := startTestBrowserSession(t, server, "viewer@example.com")
	viewerRequest := authenticatedRead("/api/v1/notifications/ntfy", "viewer@example.com", viewerCookie)
	viewerResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusOK {
		t.Fatalf("viewer ntfy status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}
	var viewerStatus map[string]any
	if err := json.Unmarshal(viewerResponse.Body.Bytes(), &viewerStatus); err != nil {
		t.Fatal(err)
	}
	if _, exposed := viewerStatus["url"]; exposed {
		t.Fatalf("viewer ntfy URL leaked: %+v", viewerStatus)
	}
	if _, exposed := viewerStatus["topic"]; exposed {
		t.Fatalf("viewer ntfy topic leaked: %+v", viewerStatus)
	}
	if viewerStatus["configured"] != true || viewerStatus["tokenConfigured"] != true {
		t.Fatalf("viewer ntfy capability flags = %+v", viewerStatus)
	}
}

func TestAgentEnrollmentNeedsOnlyOneTimeTokenAndNodeCredential(t *testing.T) {
	server, database, _ := newExtensionTestServer(t)
	service, err := nodes.NewService(database, nodes.Options{})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := service.CreateEnrollment(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": enrollment.Token, "displayName": "Compute", "hostname": "compute-1"})
	request := loopbackRequest(httptest.NewRequest(http.MethodPost, "/api/v1/agents/enroll", bytes.NewReader(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"credential":"nodekey_`) {
		t.Fatalf("agent enrollment status=%d body=%s", response.Code, response.Body.String())
	}
	reuse := loopbackRequest(httptest.NewRequest(http.MethodPost, "/api/v1/agents/enroll", bytes.NewReader(body)))
	reuse.Header.Set("Content-Type", "application/json")
	reuseResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(reuseResponse, reuse)
	if reuseResponse.Code != http.StatusUnauthorized {
		t.Fatalf("reused enrollment status=%d body=%s", reuseResponse.Code, reuseResponse.Body.String())
	}
}

func newExtensionTestServer(t *testing.T) (*Server, *store.Store, *memoryNotificationSender) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "extensions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	serviceManager := services.NewManager(database)
	hub := metrics.NewHub(metrics.Sources{Host: fixedHost{}, Services: serviceManager}, time.Hour)
	_, _ = hub.CollectOnce(context.Background())
	terminalManager, _ := terminal.NewManager(unusedTerminalBackend{}, terminal.ManagerOptions{})
	static, _ := NewStaticHandler(fstest.MapFS{"index.html": {Data: []byte("dashboard")}})
	nodeService, _ := nodes.NewService(database, nodes.Options{})
	nodeRegistry, _ := nodes.NewRegistry(nodeService, nodes.RegistryOptions{})
	configService, _ := dashboardconfig.NewService(database)
	notifications := &memoryNotificationSender{}
	server, err := New(Options{
		Auth: auth.NewManager([]string{"admin@example.com"}, true, false), Metrics: hub,
		Services: serviceManager, Terminal: terminalManager, Static: static, Audit: database,
		History: database, HistoryQuota: func() history.QuotaState { return history.QuotaState{LimitBytes: history.DefaultSoftQuota} },
		Alerts: database, Notifications: notifications, NTFYURL: "https://ntfy.example.com", NTFYTopic: "homelab", NTFYTokenSet: true,
		Nodes: nodeService, NodeRegistry: nodeRegistry,
		DashboardConfig: configService,
		Preferences:     database,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, database, notifications
}

func authenticatedRead(path, login string, cookie *http.Cookie) *http.Request {
	request := loopbackRequest(httptest.NewRequest(http.MethodGet, path, nil))
	request.Header.Set("Tailscale-User-Login", login)
	request.AddCookie(cookie)
	return request
}

type memoryNotificationSender struct{ sent int }

func (sender *memoryNotificationSender) Send(_ context.Context, delivery alerts.Delivery) error {
	if delivery.Title != "" {
		sender.sent++
	}
	return nil
}
