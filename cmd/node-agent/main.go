package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/bnhminh1010/homelab-dashboard/internal/nodeagent"
)

var version = "dev"

func main() {
	if err := execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		slog.Error("node agent stopped", "error", err)
		os.Exit(1)
	}
}

func execute(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if os.Geteuid() == 0 {
		return errors.New("refusing to run homelab-node-agent as root; use the rootless Podman account")
	}
	if len(args) == 0 {
		return errors.New("usage: homelab-node-agent <enroll|run|version>")
	}
	switch args[0] {
	case "enroll":
		return enroll(args[1:], stdin, stdout, stderr)
	case "run":
		return run(args[1:], stderr)
	case "version":
		_, err := fmt.Fprintln(stdout, version)
		return err
	default:
		return fmt.Errorf("unknown command %q; expected enroll, run, or version", args[0])
	}
}

func enroll(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	flags.SetOutput(stderr)
	serverURL := flags.String("server", strings.TrimSpace(os.Getenv("HOMELAB_DASHBOARD_URL")), "dashboard HTTPS URL")
	statePath := flags.String("state", defaultStatePath(), "absolute credential state path")
	displayName := flags.String("display-name", "", "node display name")
	hostname := flags.String("hostname", localHostname(), "node hostname")
	code := flags.String("code", "", "one-time enrollment code (prefer --code-stdin)")
	codeStdin := flags.Bool("code-stdin", false, "read the enrollment code from stdin")
	force := flags.Bool("force", false, "replace an existing node credential")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("enroll does not accept positional arguments")
	}
	if *codeStdin && *code != "" {
		return errors.New("use only one of --code or --code-stdin")
	}
	if !*force {
		if _, err := os.Stat(*statePath); err == nil {
			return errors.New("credential state already exists; use --force only when intentionally re-enrolling")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect existing credential state: %w", err)
		}
	}
	enrollmentCode := strings.TrimSpace(*code)
	if *codeStdin {
		contents, err := io.ReadAll(io.LimitReader(stdin, 4097))
		if err != nil {
			return fmt.Errorf("read enrollment code: %w", err)
		}
		if len(contents) > 4096 {
			return errors.New("enrollment code is too large")
		}
		enrollmentCode = strings.TrimSpace(string(contents))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	credentials, err := nodeagent.Enroll(ctx, nodeagent.EnrollmentOptions{
		ServerURL: *serverURL, Token: enrollmentCode, Hostname: *hostname, DisplayName: *displayName,
	})
	if err != nil {
		return err
	}
	if err := nodeagent.SaveCredentials(*statePath, credentials); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Enrolled node %s; credentials saved to %s\n", credentials.NodeID, *statePath)
	return err
}

func run(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	maxSessionsDefault := 4
	if configured := strings.TrimSpace(os.Getenv("NODE_AGENT_MAX_SESSIONS")); configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil {
			return errors.New("NODE_AGENT_MAX_SESSIONS must be an integer")
		}
		maxSessionsDefault = parsed
	}
	statePath := flags.String("state", defaultStatePath(), "absolute credential state path")
	podmanSocket := flags.String("podman-socket", strings.TrimSpace(os.Getenv("PODMAN_SOCKET")), "rootless Podman socket")
	procPath := flags.String("proc-path", valueOr(os.Getenv("HOST_PROC_PATH"), "/proc"), "host proc path")
	sysPath := flags.String("sys-path", valueOr(os.Getenv("HOST_SYS_PATH"), "/sys"), "host sys path")
	rootPath := flags.String("root-path", valueOr(os.Getenv("HOST_ROOT_PATH"), "/"), "host root path")
	networkInterface := flags.String("network-interface", strings.TrimSpace(os.Getenv("NETWORK_INTERFACE")), "network interface override")
	backupStatusFile := flags.String("backup-status-file", strings.TrimSpace(os.Getenv("BACKUP_STATUS_FILE")), "absolute backup status JSON path")
	maxSessions := flags.Int("max-sessions", maxSessionsDefault, "maximum concurrent remote streams")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("run does not accept positional arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return nodeagent.Run(ctx, nodeagent.RunOptions{
		StatePath: *statePath, PodmanSocket: *podmanSocket,
		ProcPath: *procPath, SysPath: *sysPath, RootPath: *rootPath,
		NetworkInterface: *networkInterface, MaxSessions: *maxSessions, AgentVersion: version,
		BackupStatusFile: *backupStatusFile,
	})
}

func defaultStatePath() string {
	if configured := strings.TrimSpace(os.Getenv("HOMELAB_NODE_AGENT_STATE")); configured != "" {
		return configured
	}
	configDirectory := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if configDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "credentials.json"
		}
		configDirectory = filepath.Join(home, ".config")
	}
	return filepath.Join(configDirectory, "homelab-node-agent", "credentials.json")
}

func localHostname() string {
	value, err := os.Hostname()
	if err != nil {
		return ""
	}
	return value
}

func valueOr(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}
