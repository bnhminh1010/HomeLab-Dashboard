package smartagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSMARTResults(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		status string
		temp   *float64
	}{
		{name: "passed", input: `{"smart_status":{"passed":true},"temperature":{"current":35}}`, status: StatusPassed, temp: ptr(35)},
		{name: "failed", input: `{"smart_status":{"passed":false}}`, status: StatusFailed},
		{name: "standby", input: `{"power_mode":"STANDBY"}`, status: StatusStandby},
		{name: "missing", input: `{}`, status: StatusUnavailable},
		{name: "invalid", input: `{`, status: StatusUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := parse([]byte(test.input))
			if result.Status != test.status {
				t.Fatalf("status=%q want %q", result.Status, test.status)
			}
			if test.temp == nil && result.TemperatureCelsius != nil {
				t.Fatalf("unexpected temperature %v", *result.TemperatureCelsius)
			}
			if test.temp != nil && (result.TemperatureCelsius == nil || *result.TemperatureCelsius != *test.temp) {
				t.Fatalf("temperature=%v want %v", result.TemperatureCelsius, *test.temp)
			}
		})
	}
}

func TestServerRejectsDeviceOutsideMountTable(t *testing.T) {
	root := t.TempDir()
	mounts := filepath.Join(root, "mounts")
	if err := os.WriteFile(mounts, []byte("/dev/sda1 / ext4 rw 0 0\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Options{SocketPath: filepath.Join(root, "smart.sock"), MountsPath: mounts})
	if err != nil {
		t.Fatal(err)
	}
	if server.allowed("/dev/nvme0n1") {
		t.Fatal("unmounted device must not be allowlisted")
	}
	if !server.allowed("/dev/sda1") {
		t.Fatal("mounted device should be allowlisted")
	}
	if server.allowed("/dev/../etc/passwd") {
		t.Fatal("path traversal must be rejected")
	}
}

func TestClientRoundTrip(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "smart.sock")
	mounts := filepath.Join(root, "mounts")
	if err := os.WriteFile(mounts, []byte("/dev/sda1 / ext4 rw 0 0\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Options{SocketPath: socket, MountsPath: mounts, Binary: "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	result, err := (Client{SocketPath: socket}).Check(context.Background(), "/dev/sda1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUnavailable {
		t.Fatalf("status=%q want unavailable for empty smartctl output", result.Status)
	}
}

func ptr(value float64) *float64 { return &value }
