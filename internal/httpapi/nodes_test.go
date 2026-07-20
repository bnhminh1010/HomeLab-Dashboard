package httpapi

import "testing"

func TestBearerTokenIsStrict(t *testing.T) {
	for value, expected := range map[string]string{
		"Bearer nodekey_secret": "nodekey_secret",
		"bearer token":          "token",
		"token":                 "",
		"Bearer":                "",
		"Bearer one two":        "",
	} {
		if actual := bearerToken(value); actual != expected {
			t.Fatalf("bearerToken(%q) = %q, want %q", value, actual, expected)
		}
	}
}

func TestAgentRoutesBypassOnlyBrowserIdentityMiddleware(t *testing.T) {
	if !isAgentRoute("/api/v1/agents/enroll") || !isAgentRoute("/ws/v1/agents/connect") {
		t.Fatal("agent routes were not recognized")
	}
	for _, path := range []string{"/api/v1/nodes", "/api/v1/agents/enroll/extra", "/ws/v1/agents/connect/extra"} {
		if isAgentRoute(path) {
			t.Fatalf("unexpected identity bypass for %q", path)
		}
	}
}
