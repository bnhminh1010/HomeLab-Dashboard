package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPrincipalRolesAreNormalized(t *testing.T) {
	m := NewManager([]string{"Admin@Example.com"}, true, false)
	r := httptest.NewRequest(http.MethodGet, "http://dashboard.test/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Tailscale-User-Login", " ADMIN@example.COM ")
	principal, err := m.PrincipalFromRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Role != RoleAdmin || principal.Login != "admin@example.com" {
		t.Fatalf("unexpected principal: %#v", principal)
	}
}

func TestSessionMutationRequiresOriginAndCSRF(t *testing.T) {
	m := NewManager([]string{"admin@example.com"}, false, false)
	principal := Principal{Login: "admin@example.com", Role: RoleAdmin}
	w := httptest.NewRecorder()
	session, err := m.Start(w, principal)
	if err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	r := httptest.NewRequest(http.MethodPost, "http://dashboard.test/api/v1/services", nil)
	r.Host = "dashboard.test"
	r.AddCookie(cookie)
	r.Header.Set("Origin", "http://dashboard.test")
	r.Header.Set("X-CSRF-Token", session.CSRF)
	if _, err := m.ValidateMutation(r, principal); err != nil {
		t.Fatalf("valid mutation rejected: %v", err)
	}

	r.Header.Set("Origin", "http://evil.test")
	if _, err := m.ValidateMutation(r, principal); err != ErrInvalidOrigin {
		t.Fatalf("cross-origin mutation error = %v", err)
	}
}

func TestMissingIdentityIsRejected(t *testing.T) {
	m := NewManager(nil, true, false)
	r := httptest.NewRequest(http.MethodGet, "http://dashboard.test/", nil)
	r.RemoteAddr = "[::1]:12345"
	_, err := m.PrincipalFromRequest(r)
	if err != ErrMissingIdentity {
		t.Fatalf("error = %v", err)
	}
}

func TestIdentityHeadersRequireTrustedLoopbackProxy(t *testing.T) {
	for name, testCase := range map[string]struct {
		manager *Manager
		remote  string
	}{
		"trust disabled": {NewManager([]string{"admin@example.com"}, false, false), "127.0.0.1:12345"},
		"remote peer":    {NewManager([]string{"admin@example.com"}, true, false), "100.64.0.2:12345"},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://dashboard.test/", nil)
			r.RemoteAddr = testCase.remote
			r.Header.Set("Tailscale-User-Login", "admin@example.com")
			if _, err := testCase.manager.PrincipalFromRequest(r); err != ErrUntrustedIdentity {
				t.Fatalf("PrincipalFromRequest() error = %v", err)
			}
		})
	}
}

func TestStartingSessionsCapsEachLogin(t *testing.T) {
	m := NewManager([]string{"admin@example.com"}, false, false)
	principal := Principal{Login: "admin@example.com", Role: RoleAdmin}
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	responses := make([]*httptest.ResponseRecorder, 4)
	for index := range responses {
		responses[index] = httptest.NewRecorder()
		if _, err := m.Start(responses[index], principal); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	oldRequest := httptest.NewRequest(http.MethodGet, "http://dashboard.test/api/v1/snapshot", nil)
	oldRequest.AddCookie(responses[0].Result().Cookies()[0])
	if _, err := m.Validate(oldRequest, principal); err != ErrInvalidSession {
		t.Fatalf("replaced session error = %v", err)
	}
	newRequest := httptest.NewRequest(http.MethodGet, "http://dashboard.test/api/v1/snapshot", nil)
	newRequest.AddCookie(responses[3].Result().Cookies()[0])
	if _, err := m.Validate(newRequest, principal); err != nil {
		t.Fatalf("new session rejected: %v", err)
	}
}
