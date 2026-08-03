package hostagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHostShellRoundTripResizeAndExit(t *testing.T) {
	client := startTestServer(t, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Open(ctx, Size{Cols: 120, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	info := session.Info()
	if info.User != current.Username || info.Shell != shellPath || info.Hostname == "" {
		t.Fatalf("unexpected host info: %#v", info)
	}
	if err := session.Resize(ctx, Size{Cols: 100, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	command := "printf '__USER__:%s\\n' \"$USER\"; " +
		"printf '__PWD__:%s\\n' \"$PWD\"; " +
		"printf '__TTY__:%s\\n' \"$(tty)\"; " +
		"printf '__SIZE__:%s\\n' \"$(stty size)\"; exit 7\n"
	if _, err := io.WriteString(session, command); err != nil {
		t.Fatal(err)
	}
	output := readSession(t, session, 10*time.Second)
	for _, expected := range []string{
		"__USER__:" + current.Username,
		"__PWD__:" + current.HomeDir,
		"__TTY__:/dev/pts/",
		"__SIZE__:24 100",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("terminal output does not contain %q:\n%s", expected, output)
		}
	}
	if code, ok := session.ExitCode(); !ok || code != 7 {
		t.Fatalf("unexpected exit state: code=%d available=%v", code, ok)
	}
}

func TestServerEnforcesSessionLimit(t *testing.T) {
	client := startTestServer(t, Options{MaxSessions: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := client.Open(ctx, Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := client.Open(ctx, Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	third, err := client.Open(ctx, Size{Cols: 80, Rows: 24})
	if third != nil {
		_ = third.Close()
	}
	if !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("expected session limit, got %v", err)
	}
}

func TestClosingClientTerminatesShell(t *testing.T) {
	client := startTestServer(t, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Open(ctx, Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(session, "printf '__PID__:%s\\n' \"$$\"\n"); err != nil {
		t.Fatal(err)
	}
	pid := readPID(t, session)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Bash process %d remained alive after the client disconnected", pid)
}

func TestIdleTimeoutClosesSession(t *testing.T) {
	client := startTestServer(t, Options{IdleTimeout: 150 * time.Millisecond, HardTimeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Open(ctx, Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	_, err = io.ReadAll(session)
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != "idle_timeout" {
		t.Fatalf("expected idle timeout, got %v", err)
	}
}

func TestHardTimeoutClosesActiveSession(t *testing.T) {
	client := startTestServer(t, Options{IdleTimeout: 5 * time.Second, HardTimeout: 150 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Open(ctx, Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	_, err = io.ReadAll(session)
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != "maximum_duration" {
		t.Fatalf("expected maximum duration, got %v", err)
	}
}

func TestSafeEnvironmentDoesNotInheritProcessSecrets(t *testing.T) {
	t.Setenv("HOST_AGENT_TEST_SECRET", "must-not-leak")
	environment := safeEnvironment(&user.User{Uid: "1000", Username: "tester", HomeDir: "/home/tester"}, "test-host")
	joined := strings.Join(environment, "\n")
	for _, expected := range []string{"HOME=/home/tester", "USER=tester", "SHELL=/bin/bash", "TERM=xterm-256color", "XDG_RUNTIME_DIR=/run/user/1000"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("safe environment does not contain %q: %v", expected, environment)
		}
	}
	if strings.Contains(joined, "HOST_AGENT_TEST_SECRET") || strings.Contains(joined, "must-not-leak") {
		t.Fatalf("process secret leaked into shell environment: %v", environment)
	}
}

func TestValidateEffectiveUID(t *testing.T) {
	tests := []struct {
		name         string
		userUID      string
		effectiveUID int
		wantUID      uint32
		wantError    string
	}{
		{name: "current root", userUID: "0", effectiveUID: 0, wantError: "refusing to run as root"},
		{name: "root despite mismatched account", userUID: "1000", effectiveUID: 0, wantError: "refusing to run as root"},
		{name: "negative", userUID: "1000", effectiveUID: -1, wantError: "invalid effective UID"},
		{name: "invalid account UID", userUID: "not-a-uid", effectiveUID: 1000, wantError: "does not match effective UID"},
		{name: "mismatched account", userUID: "1001", effectiveUID: 1000, wantError: "does not match effective UID"},
		{name: "rootless user", userUID: "1000", effectiveUID: 1000, wantUID: 1000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			uid, err := validateEffectiveUID(test.userUID, test.effectiveUID)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected error containing %q, got %v", test.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if uid != test.wantUID {
				t.Fatalf("got UID %d, want %d", uid, test.wantUID)
			}
		})
	}
}

func TestSocketPermissions(t *testing.T) {
	directory := secureTempDir(t)
	socketPath := filepath.Join(directory, "agent.sock")
	server, err := NewServer(Options{SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, socketPath, done)
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("unexpected socket mode: %v", info.Mode())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func startTestServer(t *testing.T, options Options) *Client {
	t.Helper()
	options.SocketPath = filepath.Join(secureTempDir(t), "agent.sock")
	if options.IdleTimeout == 0 {
		// A login shell can spend several seconds in user startup hooks when the
		// race detector is active. Dedicated idle-timeout tests pass an explicit
		// short value; the general test fixture must not compete with shell start.
		options.IdleTimeout = 30 * time.Second
	}
	if options.HardTimeout == 0 {
		options.HardTimeout = 10 * time.Second
	}
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, options.SocketPath, done)
	client, err := NewClient(options.SocketPath)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), time.Second)
	defer probeCancel()
	if err := client.Probe(probeCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not shut down")
		}
	})
	return client
}

func secureTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func waitForSocket(t *testing.T, socketPath string, serverDone <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-serverDone:
			t.Fatalf("server stopped before creating socket: %v", err)
		default:
		}
		if info, err := os.Lstat(socketPath); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Unix socket %s did not appear", socketPath)
}

func readSession(t *testing.T, session Session, timeout time.Duration) string {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(session)
		done <- result{data: data, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("read terminal: %v\n%s", result.err, result.data)
		}
		return string(result.data)
	case <-time.After(timeout):
		_ = session.Close()
		t.Fatal("timed out waiting for terminal output")
		return ""
	}
}

func readPID(t *testing.T, session Session) int {
	t.Helper()
	expression := regexp.MustCompile(`__PID__:(\d+)`)
	buffer := make([]byte, 4096)
	var output strings.Builder
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		read, err := session.Read(buffer)
		if read > 0 {
			output.Write(buffer[:read])
			if match := expression.FindStringSubmatch(output.String()); len(match) == 2 {
				pid, conversionErr := strconv.Atoi(match[1])
				if conversionErr != nil {
					t.Fatal(conversionErr)
				}
				return pid
			}
		}
		if err != nil {
			t.Fatalf("read PID: %v; output=%q", err, output.String())
		}
	}
	t.Fatal(fmt.Sprintf("did not receive Bash PID; output=%q", output.String()))
	return 0
}
