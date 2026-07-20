package nodeagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"sync"

	"github.com/creack/pty"
)

type LocalHostShell struct{}

func (LocalHostShell) Open(ctx context.Context, cols, rows uint16) (HostStream, error) {
	if os.Geteuid() == 0 {
		return nil, errors.New("node agent: refusing to open a host shell as root")
	}
	command := exec.CommandContext(ctx, "/bin/bash", "--login")
	command.Env = os.Environ()
	if current, err := user.Current(); err == nil && current.HomeDir != "" {
		command.Dir = current.HomeDir
	}
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	stream := &ptyHostStream{file: terminal, command: command, waitDone: make(chan struct{})}
	go stream.wait()
	return stream, nil
}

type ptyHostStream struct {
	file     *os.File
	command  *exec.Cmd
	once     sync.Once
	waitDone chan struct{}

	statusMu sync.Mutex
	exitCode *int
}

func (stream *ptyHostStream) Read(buffer []byte) (int, error)  { return stream.file.Read(buffer) }
func (stream *ptyHostStream) Write(buffer []byte) (int, error) { return stream.file.Write(buffer) }

func (stream *ptyHostStream) Resize(cols, rows uint16) error {
	return pty.Setsize(stream.file, &pty.Winsize{Cols: cols, Rows: rows})
}

func (stream *ptyHostStream) Close() error {
	var closeErr error
	stream.once.Do(func() {
		closeErr = stream.file.Close()
		if stream.command.Process != nil {
			_ = stream.command.Process.Kill()
		}
	})
	return closeErr
}

func (stream *ptyHostStream) wait() {
	_ = stream.command.Wait()
	exitCode := -1
	if stream.command.ProcessState != nil {
		exitCode = stream.command.ProcessState.ExitCode()
	}
	stream.statusMu.Lock()
	stream.exitCode = &exitCode
	stream.statusMu.Unlock()
	close(stream.waitDone)
	_ = stream.Close()
}

func (stream *ptyHostStream) ExitCode() *int {
	<-stream.waitDone
	stream.statusMu.Lock()
	defer stream.statusMu.Unlock()
	if stream.exitCode == nil {
		return nil
	}
	value := *stream.exitCode
	return &value
}

func hostShellIdentity() (string, string) {
	hostname, _ := os.Hostname()
	username := ""
	if current, err := user.Current(); err == nil {
		username = current.Username
	}
	return hostname, username
}
