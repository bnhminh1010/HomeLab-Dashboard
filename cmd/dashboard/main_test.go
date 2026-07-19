package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/binhminh/HomeLab-Minh/internal/hostagent"
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

type stubHostSession struct {
	readErr error
}

func (session stubHostSession) Read([]byte) (int, error)             { return 0, session.readErr }
func (stubHostSession) Write(data []byte) (int, error)               { return len(data), nil }
func (stubHostSession) Close() error                                 { return nil }
func (stubHostSession) Resize(context.Context, hostagent.Size) error { return nil }
func (stubHostSession) Info() hostagent.Info                         { return hostagent.Info{} }
func (stubHostSession) ExitCode() (int, bool)                        { return 0, false }

var _ hostagent.Session = stubHostSession{}
var _ io.ReadWriteCloser = stubHostSession{}
