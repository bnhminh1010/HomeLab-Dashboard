package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/binhminh/HomeLab-Minh/internal/hostagent"
)

func main() {
	if err := run(); err != nil {
		slog.Error("host agent stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	socketPath, err := hostAgentSocketPath()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("host agent starting", "socket", socketPath)
	return hostagent.Run(ctx, hostagent.Options{SocketPath: socketPath})
}

func hostAgentSocketPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("HOST_AGENT_SOCKET")); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("HOST_AGENT_SOCKET must be an absolute path")
		}
		return configured, nil
	}
	runtimeDirectory := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if runtimeDirectory == "" || !filepath.IsAbs(runtimeDirectory) {
		return "", fmt.Errorf("HOST_AGENT_SOCKET or an absolute XDG_RUNTIME_DIR is required")
	}
	return filepath.Join(runtimeDirectory, "homelab-dashboard", "agent.sock"), nil
}
