package nodeagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollAndCredentialFilePermissions(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/dashboard/api/v1/agents/enroll" {
			t.Fatalf("unexpected enrollment request: %s %s", request.Method, request.URL.Path)
		}
		var input struct {
			Token    string `json:"token"`
			Hostname string `json:"hostname"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Token != "one-time-code" || input.Hostname != "rack-1" {
			t.Fatalf("unexpected enrollment input: %#v", input)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
			"node":{"id":"node_1","displayName":"Rack 1","hostname":"rack-1","createdAt":"2026-07-19T00:00:00Z","updatedAt":"2026-07-19T00:00:00Z"},
			"credential":"nodekey_secret","protocolVersion":1,"websocketUrl":"/ws/v1/agents/connect"
		}`)),
			Request: request,
		}, nil
	})}

	credentials, err := Enroll(context.Background(), EnrollmentOptions{
		ServerURL: "http://127.0.0.1:8082/dashboard", Token: "one-time-code", Hostname: "rack-1", DisplayName: "Rack 1",
		HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if credentials.NodeID != "node_1" || credentials.WebsocketURL != "ws://127.0.0.1:8082/ws/v1/agents/connect" {
		t.Fatalf("unexpected credentials: %#v", credentials)
	}

	statePath := filepath.Join(t.TempDir(), "state", "credentials.json")
	if err := SaveCredentials(statePath, credentials); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %04o, want 0600", info.Mode().Perm())
	}
	loaded, err := LoadCredentials(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Credential != "nodekey_secret" || loaded.NodeID != "node_1" {
		t.Fatalf("loaded credentials do not match: %#v", loaded)
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(statePath), ".credentials-*")); len(matches) != 0 {
		t.Fatalf("temporary credential files were left behind: %v", matches)
	}

	if err := os.Chmod(statePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(statePath); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("LoadCredentials accepted insecure permissions: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestCredentialsRequireSecureSameHostEndpoints(t *testing.T) {
	tests := []Credentials{
		{ServerURL: "http://192.0.2.10", WebsocketURL: "ws://192.0.2.10/ws", NodeID: "n", Credential: "c", ProtocolVersion: 1},
		{ServerURL: "https://dashboard.example", WebsocketURL: "wss://other.example/ws", NodeID: "n", Credential: "c", ProtocolVersion: 1},
		{ServerURL: "https://dashboard.example", WebsocketURL: "/ws", NodeID: "n", Credential: "c", ProtocolVersion: 2},
	}
	for _, credentials := range tests {
		if err := credentials.Validate(); err == nil {
			t.Fatalf("Validate accepted unsafe credentials: %#v", credentials)
		}
	}
	valid := Credentials{
		ServerURL: "https://dashboard.example", WebsocketURL: "/ws/v1/agents/connect",
		NodeID: "node_1", Credential: "nodekey_secret", ProtocolVersion: 1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
}
