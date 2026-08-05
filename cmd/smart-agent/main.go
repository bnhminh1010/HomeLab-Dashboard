package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/smartagent"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("homelab-smart-agent", flag.ContinueOnError)
	socket := flags.String("socket", valueOr(os.Getenv("SMART_AGENT_SOCKET"), defaultSocket()), "absolute Unix socket path")
	mounts := flags.String("mounts", valueOr(os.Getenv("SMART_AGENT_MOUNTS"), "/proc/1/mounts"), "mount table path")
	binary := flags.String("smartctl", valueOr(os.Getenv("SMARTCTL_BINARY"), "smartctl"), "smartctl executable")
	timeout := flags.Duration("timeout", 3*time.Second, "smartctl command timeout")
	concurrency := flags.Int("concurrency", 2, "maximum concurrent smartctl commands")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("run does not accept positional arguments")
	}
	server, err := smartagent.NewServer(smartagent.Options{
		SocketPath: *socket, MountsPath: *mounts, Binary: *binary,
		CommandTimeout: *timeout, MaxConcurrency: *concurrency,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Serve(ctx)
}

func defaultSocket() string {
	runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if runtimeDir == "" {
		runtimeDir = "/run/user/" + fmt.Sprint(os.Getuid())
	}
	return runtimeDir + "/homelab-dashboard/smart-agent.sock"
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
