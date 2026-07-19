package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestListStatsAndLogs(t *testing.T) {
	var logQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/v5.0.0/libpod/containers/json", func(response http.ResponseWriter, request *http.Request) {
		if got := request.URL.Query().Get("all"); got != "true" {
			t.Errorf("all query = %q, want true", got)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `[
		  {"Id":"visible","Names":["/app"],"Image":"app:latest","State":"running","Status":"Up","Created":"2026-07-17T12:00:00Z","Labels":{},"Ports":[{"host_ip":"127.0.0.1","container_port":8080,"host_port":18080,"range":1,"protocol":"tcp"}]},
		  {"Id":"hidden","Names":["/sidecar"],"Labels":{"io.homelab.dashboard.hidden":"1"}}
		]`)
	})
	mux.HandleFunc("/v5.0.0/libpod/containers/stats", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"Error":null,"Stats":[{"ContainerID":"visible","Name":"app","CPU":2.5,"MemUsage":128,"MemLimit":1024,"Network":{"eth0":{"RxBytes":11,"TxBytes":12}},"BlockInput":13,"BlockOutput":14,"PIDs":2,"UpTime":90000000000}]}`)
	})
	mux.HandleFunc("/v5.0.0/libpod/containers/visible/logs", func(response http.ResponseWriter, request *http.Request) {
		logQuery = request.URL.RawQuery
		_, _ = io.WriteString(response, "line one\nline two\n")
	})
	mux.HandleFunc("/v5.0.0/libpod/containers/visible/json", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"Id":"visible","Name":"/app","State":{"Status":"running","Running":true},"Config":{"Labels":{}}}`)
	})
	client := newFakeClient(t, mux)

	containers, err := client.ListContainers(context.Background(), true)
	if err != nil {
		t.Fatalf("ListContainers() error = %v", err)
	}
	if len(containers) != 1 || containers[0].Name != "app" || containers[0].Protected {
		t.Fatalf("ListContainers() = %#v", containers)
	}
	if len(containers[0].Ports) != 1 || containers[0].Ports[0].ContainerPort != 8080 || containers[0].Ports[0].HostPort != 18080 {
		t.Fatalf("container ports = %#v", containers[0].Ports)
	}

	stats, err := client.Stats(context.Background(), true)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	wantStats := []ContainerStats{{
		ID: "visible", Name: "app", CPUPercent: 2.5, MemoryUsage: 128, MemoryLimit: 1024,
		NetworkIn: 11, NetworkOut: 12, BlockInput: 13, BlockOutput: 14, PIDs: 2, UpTime: 90,
	}}
	if !reflect.DeepEqual(stats, wantStats) {
		t.Fatalf("Stats() = %#v, want %#v", stats, wantStats)
	}

	logs, err := client.Logs(context.Background(), "visible", LogsOptions{Tail: 25, Follow: true})
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	payload, err := io.ReadAll(logs)
	logs.Close()
	if err != nil || string(payload) != "line one\nline two\n" {
		t.Fatalf("Logs() = %q, %v", payload, err)
	}
	for _, part := range []string{"follow=true", "stderr=true", "stdout=true", "tail=25"} {
		if !strings.Contains(logQuery, part) {
			t.Errorf("logs query %q does not contain %q", logQuery, part)
		}
	}
	if _, err := client.Logs(context.Background(), "visible", LogsOptions{Tail: 501}); err == nil {
		t.Fatal("Logs() accepted more than 500 lines")
	}
}

func TestLogsRejectProtectedContainer(t *testing.T) {
	logsCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/v5.0.0/libpod/containers/system/json", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"Id":"system","State":{"Status":"running","Running":true},"Config":{"Labels":{"io.homelab.dashboard.hidden":"true"}}}`)
	})
	mux.HandleFunc("/v5.0.0/libpod/containers/system/logs", func(response http.ResponseWriter, _ *http.Request) {
		logsCalled = true
		response.WriteHeader(http.StatusOK)
	})
	client := newFakeClient(t, mux)
	_, err := client.Logs(context.Background(), "system", LogsOptions{})
	if !errors.Is(err, ErrProtectedContainer) {
		t.Fatalf("Logs() error = %v, want ErrProtectedContainer", err)
	}
	if logsCalled {
		t.Fatal("protected container reached logs endpoint")
	}
}

func TestExecLifecycleUsesFixedShellAndUpgrade(t *testing.T) {
	var mu sync.Mutex
	var createPayload map[string]any
	var resized, removed bool
	inputReceived := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/v5.0.0/libpod/containers/app/json", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"Id":"app","Name":"/app","State":{"Status":"running","Running":true},"Config":{"Labels":{}}}`)
	})
	mux.HandleFunc("/v5.0.0/libpod/containers/app/exec", func(response http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&createPayload); err != nil {
			t.Errorf("decode exec create: %v", err)
		}
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, `{"Id":"exec-1"}`)
	})
	mux.HandleFunc("/v5.0.0/libpod/exec/exec-1/start", func(response http.ResponseWriter, request *http.Request) {
		if !strings.EqualFold(request.Header.Get("Connection"), "Upgrade") || request.Header.Get("Upgrade") != "tcp" {
			t.Errorf("upgrade headers = %#v", request.Header)
		}
		var startBody map[string]any
		if err := json.NewDecoder(request.Body).Decode(&startBody); err != nil {
			t.Errorf("decode exec start: %v", err)
		}
		hijacker, ok := response.(http.Hijacker)
		if !ok {
			t.Error("response does not support hijack")
			return
		}
		connection, rw, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer connection.Close()
		_, _ = io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\nhello")
		_ = rw.Flush()
		buffer := make([]byte, 4)
		if _, err := io.ReadFull(rw, buffer); err == nil {
			inputReceived <- string(buffer)
		}
	})
	mux.HandleFunc("/v5.0.0/libpod/exec/exec-1/resize", func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		resized = request.URL.Query().Get("w") == "140" && request.URL.Query().Get("h") == "40"
		mu.Unlock()
		response.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/v5.0.0/libpod/exec/exec-1/remove", func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			Force bool `json:"Force"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		mu.Lock()
		removed = body.Force
		mu.Unlock()
		response.WriteHeader(http.StatusOK)
	})
	client := newFakeClient(t, mux)

	execID, err := client.CreateShellExec(context.Background(), "app", ShellSH, TerminalSize{Cols: 120, Rows: 30})
	if err != nil {
		t.Fatalf("CreateShellExec() error = %v", err)
	}
	if execID != "exec-1" {
		t.Fatalf("CreateShellExec() = %q", execID)
	}
	if got := createPayload["Cmd"]; !reflect.DeepEqual(got, []any{"/bin/sh"}) {
		t.Fatalf("Cmd = %#v, want only /bin/sh", got)
	}
	for _, forbidden := range []string{"User", "Env", "WorkingDir"} {
		if _, exists := createPayload[forbidden]; exists {
			t.Errorf("exec create unexpectedly includes %s", forbidden)
		}
	}
	if privileged, _ := createPayload["Privileged"].(bool); privileged {
		t.Error("exec create is privileged")
	}

	stream, err := client.StartExec(context.Background(), execID, TerminalSize{Cols: 120, Rows: 30})
	if err != nil {
		t.Fatalf("StartExec() error = %v", err)
	}
	buffer := make([]byte, 5)
	if _, err := io.ReadFull(stream, buffer); err != nil || string(buffer) != "hello" {
		t.Fatalf("exec output = %q, %v", buffer, err)
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("exec input: %v", err)
	}
	select {
	case got := <-inputReceived:
		if got != "ping" {
			t.Fatalf("server input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive exec input")
	}
	stream.Close()

	if err := client.ResizeExec(context.Background(), execID, TerminalSize{Cols: 140, Rows: 40}); err != nil {
		t.Fatalf("ResizeExec() error = %v", err)
	}
	if err := client.RemoveExec(context.Background(), execID, true); err != nil {
		t.Fatalf("RemoveExec() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !resized || !removed {
		t.Fatalf("resized=%v removed=%v", resized, removed)
	}
}

func TestCreateShellExecRejectsProtectedContainer(t *testing.T) {
	execCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/v5.0.0/libpod/containers/system/json", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"Id":"system","State":{"Status":"running","Running":true},"Config":{"Labels":{"io.homelab.dashboard.protected":"true"}}}`)
	})
	mux.HandleFunc("/v5.0.0/libpod/containers/system/exec", func(response http.ResponseWriter, _ *http.Request) {
		execCalled = true
		response.WriteHeader(http.StatusCreated)
	})
	client := newFakeClient(t, mux)

	_, err := client.CreateShellExec(context.Background(), "system", ShellSH, TerminalSize{})
	if !errors.Is(err, ErrProtectedContainer) {
		t.Fatalf("CreateShellExec() error = %v, want ErrProtectedContainer", err)
	}
	if execCalled {
		t.Fatal("protected container reached exec create endpoint")
	}
}

func TestAPIErrorIsTyped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v5.0.0/libpod/containers/json", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(response, `{"message":"service unavailable"}`)
	})
	client := newFakeClient(t, mux)
	_, err := client.ListContainers(context.Background(), true)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ListContainers() error = %#v", err)
	}
}

func newFakeClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "podman.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(fmt.Sprintf("fake Podman server: %v", err))
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	client, err := NewClient(socketPath)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}
