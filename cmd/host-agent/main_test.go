package main

import (
	"path/filepath"
	"testing"
)

func TestHostAgentSocketPath(t *testing.T) {
	t.Setenv("HOST_AGENT_SOCKET", "/tmp/homelab-agent.sock")
	got, err := hostAgentSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/homelab-agent.sock" {
		t.Fatalf("unexpected path: %s", got)
	}

	t.Setenv("HOST_AGENT_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	got, err = hostAgentSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/run/user/1000", "homelab-dashboard", "agent.sock")
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
