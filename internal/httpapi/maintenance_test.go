package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaintenanceWindowReadForViewerAndMutationForAdmin(t *testing.T) {
	server, _, _ := newExtensionTestServer(t)
	adminCSRF, adminCookie, _ := startTestBrowserSession(t, server, "admin@example.com")
	_, viewerCookie, _ := startTestBrowserSession(t, server, "viewer@example.com")

	viewerRead := httptest.NewRecorder()
	server.Handler().ServeHTTP(viewerRead, authenticatedRead("/api/v1/maintenance-windows", "viewer@example.com", viewerCookie))
	if viewerRead.Code != http.StatusOK {
		t.Fatalf("viewer read status=%d body=%s", viewerRead.Code, viewerRead.Body.String())
	}

	body := `{"name":"Weekly updates","nodeSelector":"local","resourceType":"container","resourceSelector":"*","weekdays":[1],"startMinute":120,"durationMinutes":30,"timezone":"UTC","enabled":true}`
	create := httptest.NewRecorder()
	server.Handler().ServeHTTP(create, authenticatedMutation(http.MethodPost, "/api/v1/maintenance-windows", body, "admin@example.com", adminCSRF, adminCookie))
	if create.Code != http.StatusCreated || !strings.Contains(create.Body.String(), `"timezone":"UTC"`) {
		t.Fatalf("admin create status=%d body=%s", create.Code, create.Body.String())
	}

	viewerMutation := httptest.NewRecorder()
	server.Handler().ServeHTTP(viewerMutation, authenticatedMutation(http.MethodPost, "/api/v1/maintenance-windows", body, "viewer@example.com", "", viewerCookie))
	if viewerMutation.Code != http.StatusForbidden {
		t.Fatalf("viewer mutation status=%d body=%s", viewerMutation.Code, viewerMutation.Body.String())
	}
}
