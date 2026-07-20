package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/hostagent"
	"github.com/binhminh/HomeLab-Minh/internal/nodes"
	"github.com/binhminh/HomeLab-Minh/internal/terminal"
)

func TestHostSessionAdapterMapsAgentTimeouts(t *testing.T) {
	tests := []struct {
		name string
		from error
		want error
	}{
		{name: "idle", from: hostagent.ErrIdleTimeout, want: terminal.ErrIdleTimeout},
		{name: "maximum duration", from: hostagent.ErrHardTimeout, want: terminal.ErrHardTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := hostSessionAdapter{Session: stubHostSession{readErr: test.from}}
			_, err := adapter.Read(make([]byte, 1))
			if !errors.Is(err, test.want) {
				t.Fatalf("Read() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestLoadSecretFileRequiresOneProtectedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ntfy-token")
	if err := os.WriteFile(path, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := loadSecretFile(path); err != nil || value != "secret-token" {
		t.Fatalf("loadSecretFile() = %q, %v", value, err)
	}
	if err := os.WriteFile(path, []byte("first\nsecond"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSecretFile(path); err == nil {
		t.Fatal("multi-line token was accepted")
	}
	if err := os.WriteFile(path, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSecretFile(path); err == nil {
		t.Fatal("group-readable token was accepted")
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if value, err := loadSecretFile(path); err != nil || value != "secret-token" {
		t.Fatalf("owner-read-only secret = %q, %v", value, err)
	}
}

func TestRunOperationalRetentionRunsImmediatelyAndRepeats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repository := &stubOperationalRetainer{calls: make(chan time.Time, 16)}
	done := make(chan error, 1)
	go func() {
		done <- runOperationalRetention(ctx, repository, time.Millisecond)
	}()

	for call := 0; call < 2; call++ {
		select {
		case retainedAt := <-repository.calls:
			if retainedAt.IsZero() || retainedAt.Location() != time.UTC {
				t.Fatalf("retention call timestamp = %v", retainedAt)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for retention call %d", call+1)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runOperationalRetention() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("operational retention worker did not stop")
	}
}

func TestRunOperationalRetentionReturnsStartupFailure(t *testing.T) {
	want := errors.New("database unavailable")
	repository := &stubOperationalRetainer{calls: make(chan time.Time, 1), err: want}
	err := runOperationalRetention(context.Background(), repository, time.Hour)
	if !errors.Is(err, want) {
		t.Fatalf("runOperationalRetention() error = %v, want %v", err, want)
	}
}

func TestShouldEvaluateNodeAvailabilityHonorsInitialConnectionGrace(t *testing.T) {
	now := time.Date(2026, 7, 20, 7, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-time.Second)
	tests := []struct {
		name  string
		state nodes.NodeState
		want  bool
	}{
		{
			name: "fresh enrollment is skipped",
			state: nodes.NodeState{Node: nodes.Node{
				ID: "fresh", CreatedAt: now.Add(-initialNodeConnectionGrace + time.Nanosecond),
			}},
			want: false,
		},
		{
			name: "never connected is offline at grace boundary",
			state: nodes.NodeState{Node: nodes.Node{
				ID: "expired", CreatedAt: now.Add(-initialNodeConnectionGrace),
			}},
			want: true,
		},
		{
			name:  "previously connected offline node remains evaluated",
			state: nodes.NodeState{Node: nodes.Node{ID: "seen", CreatedAt: now}, LastSeenAt: &lastSeen},
			want:  true,
		},
		{
			name:  "online node remains evaluated",
			state: nodes.NodeState{Node: nodes.Node{ID: "online", CreatedAt: now}, Online: true},
			want:  true,
		},
		{
			name:  "invalid node is skipped",
			state: nodes.NodeState{Node: nodes.Node{CreatedAt: now.Add(-time.Hour)}},
			want:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldEvaluateNodeAvailability(test.state, now); got != test.want {
				t.Fatalf("shouldEvaluateNodeAvailability() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWorkerGroupWaitHonorsShutdownDeadline(t *testing.T) {
	release := make(chan struct{})
	group := &workerGroup{}
	group.Go(context.Background(), make(chan error, 1), "blocking worker", func(context.Context) error {
		<-release
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := group.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := group.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() after worker exit: %v", err)
	}
}

type stubHostSession struct {
	readErr error
}

type stubOperationalRetainer struct {
	calls chan time.Time
	err   error
}

func (stub *stubOperationalRetainer) RetainOperationalData(_ context.Context, now time.Time) error {
	stub.calls <- now
	return stub.err
}

func (session stubHostSession) Read([]byte) (int, error)             { return 0, session.readErr }
func (stubHostSession) Write(data []byte) (int, error)               { return len(data), nil }
func (stubHostSession) Close() error                                 { return nil }
func (stubHostSession) Resize(context.Context, hostagent.Size) error { return nil }
func (stubHostSession) Info() hostagent.Info                         { return hostagent.Info{} }
func (stubHostSession) ExitCode() (int, bool)                        { return 0, false }

var _ hostagent.Session = stubHostSession{}
var _ io.ReadWriteCloser = stubHostSession{}
