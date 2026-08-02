package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	homelab "github.com/bnhminh1010/homelab-dashboard"
	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
	"github.com/bnhminh1010/homelab-dashboard/internal/auth"
	"github.com/bnhminh1010/homelab-dashboard/internal/config"
	"github.com/bnhminh1010/homelab-dashboard/internal/containers"
	"github.com/bnhminh1010/homelab-dashboard/internal/dashboardconfig"
	"github.com/bnhminh1010/homelab-dashboard/internal/healthchecks"
	"github.com/bnhminh1010/homelab-dashboard/internal/history"
	"github.com/bnhminh1010/homelab-dashboard/internal/hostagent"
	"github.com/bnhminh1010/homelab-dashboard/internal/httpapi"
	"github.com/bnhminh1010/homelab-dashboard/internal/logging"
	"github.com/bnhminh1010/homelab-dashboard/internal/logs"
	"github.com/bnhminh1010/homelab-dashboard/internal/metrics"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/monitoring"
	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
	"github.com/bnhminh1010/homelab-dashboard/internal/operations"
	"github.com/bnhminh1010/homelab-dashboard/internal/podman"
	"github.com/bnhminh1010/homelab-dashboard/internal/services"
	"github.com/bnhminh1010/homelab-dashboard/internal/slo"
	"github.com/bnhminh1010/homelab-dashboard/internal/store"
	"github.com/bnhminh1010/homelab-dashboard/internal/terminal"
)

func main() {
	logging.Configure("dashboard")
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		url := "http://127.0.0.1:8082/health/live"
		if len(os.Args) > 2 {
			url = os.Args[2]
		}
		if err := healthcheck(url); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("dashboard stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	var logReader logs.Reader
	if cfg.LogsBackend == logs.BackendLoki {
		logReader, err = logs.NewLoki(cfg.LokiURL, nil)
		if err != nil {
			return fmt.Errorf("configure Loki logs: %w", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := store.Open(ctx, cfg.DataPath)
	if err != nil {
		return err
	}
	databaseSafeToClose := true
	defer func() {
		if databaseSafeToClose {
			_ = database.Close()
		}
	}()

	serviceManager := services.NewManager(database)
	sloService, err := slo.NewService(database, database, slo.Options{})
	if err != nil {
		return fmt.Errorf("configure service objectives: %w", err)
	}
	dashboardConfig, err := dashboardconfig.NewService(database)
	if err != nil {
		return fmt.Errorf("configure dashboard import/export: %w", err)
	}
	prober, err := services.NewProber(services.ProbePolicy{
		AllowedPrefixes: cfg.ProbeAllowCIDRs,
		SOCKS5Address:   cfg.TailscaleSOCKS5Address,
	})
	if err != nil {
		return fmt.Errorf("configure service probes: %w", err)
	}
	tlsInspector, err := services.NewTLSInspector(services.ProbePolicy{
		AllowedPrefixes: cfg.ProbeAllowCIDRs,
		SOCKS5Address:   cfg.TailscaleSOCKS5Address,
	})
	if err != nil {
		return fmt.Errorf("configure TLS inspection: %w", err)
	}
	tlsScanner, err := services.NewTLSScanner(serviceManager, tlsInspector, database)
	if err != nil {
		return fmt.Errorf("configure TLS scanner: %w", err)
	}
	backupSource, err := healthchecks.NewBackupFileSource(cfg.BackupStatusFile)
	if err != nil {
		return fmt.Errorf("configure backup status source: %w", err)
	}
	probeScheduler := services.NewScheduler(serviceManager, prober, services.SchedulerOptions{
		Interval: cfg.ProbeInterval, Timeout: cfg.ProbeTimeout, Concurrency: cfg.ProbeConcurrency,
	})

	hostCollector, err := metrics.NewLinuxCollector(metrics.CollectorOptions{
		ProcPath: cfg.HostProcPath, SysPath: cfg.HostSysPath, RootPath: cfg.HostRootPath,
		NetworkInterface: cfg.NetworkInterface,
	})
	if err != nil {
		return fmt.Errorf("configure host metrics: %w", err)
	}
	podmanClient, err := podman.NewClient(cfg.PodmanSocket)
	if err != nil {
		return err
	}
	defer podmanClient.CloseIdleConnections()
	runtimeSource := newPodmanSource(containers.New(podmanClient), runtime.NumCPU())
	dashboardAlerts := dashboardAlertSource{runtime: runtimeSource, repository: database}
	hub := metrics.NewHub(metrics.Sources{
		Host:       hostCollector,
		Services:   serviceManager,
		Containers: metrics.ContainerSourceFunc(runtimeSource.containers),
		Backups:    backupSource,
		Alerts:     metrics.AlertSourceFunc(dashboardAlerts.alertsSnapshot),
	}, cfg.MetricsInterval)

	historyWriter, err := history.NewWriter(database, history.WriterOptions{SoftQuotaBytes: cfg.HistoryQuotaBytes})
	if err != nil {
		return fmt.Errorf("configure metric history: %w", err)
	}
	historyMaintenance, err := history.NewMaintenance(database, history.MaintenanceOptions{})
	if err != nil {
		return fmt.Errorf("configure metric history maintenance: %w", err)
	}
	if err := seedDefaultAlertRules(ctx, database); err != nil {
		return err
	}
	activeAlertStates, err := database.ListActiveAlertStatesForReconciliation(ctx)
	if err != nil {
		return fmt.Errorf("load active alert state for reconciliation: %w", err)
	}
	alertEngine, err := alerts.NewEngineWithOptions(database, nil, alerts.EngineOptions{DeliveryEnabled: cfg.NTFYURL != ""})
	if err != nil {
		return fmt.Errorf("configure alert engine: %w", err)
	}
	pipeline, err := monitoring.New(monitoring.Options{
		History: historyWriter, Alerts: alertEngine, Operations: database, Backups: database, ActiveAlertStates: activeAlertStates,
		OnError: func(err error) { slog.Warn("monitoring snapshot processing failed", "error", err) },
	})
	if err != nil {
		return err
	}
	nodeService, err := nodes.NewService(database, nodes.Options{})
	if err != nil {
		return fmt.Errorf("configure node enrollment: %w", err)
	}
	nodeRegistry, err := nodes.NewRegistry(nodeService, nodes.RegistryOptions{
		OnConnectionOpen: func(nodeID string) {
			if _, err := database.RecordOperationalEvent(context.Background(), operations.Event{
				Type: operations.EventNodeConnected, Source: operations.SourceAutomatic,
				Title: "Node connected", Summary: "Monitoring agent connected", NodeID: nodeID,
			}); err != nil {
				slog.Warn("record node connection event", "node", nodeID, "error", err)
			}
		},
		OnSnapshot: func(snapshotContext context.Context, nodeID string, snapshot model.SnapshotEnvelope) error {
			if err := pipeline.Handle(snapshotContext, nodeID, snapshot); err != nil {
				slog.Warn("remote monitoring snapshot processing failed", "node", nodeID, "error", err)
			}
			return nil
		},
		OnConnectionLost: func(nodeID string) {
			if ctx.Err() != nil {
				return
			}
			offlineContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := pipeline.HandleNodeOffline(offlineContext, nodeID, time.Now().UTC()); err != nil {
				slog.Warn("remote node offline alert failed", "node", nodeID, "error", err)
			}
			if _, err := database.RecordOperationalEvent(offlineContext, operations.Event{
				Type: operations.EventNodeDisconnected, Source: operations.SourceAutomatic,
				Title: "Node disconnected", Summary: "Monitoring agent connection closed", NodeID: nodeID,
			}); err != nil {
				slog.Warn("record node disconnection event", "node", nodeID, "error", err)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("configure node registry: %w", err)
	}

	var notificationSender *alerts.NTFYSender
	var deliveryProcessor *alerts.DeliveryProcessor
	ntfyTokenSet := false
	if cfg.NTFYURL != "" {
		token, err := loadSecretFile(cfg.NTFYTokenFile)
		if err != nil {
			return fmt.Errorf("load ntfy token: %w", err)
		}
		ntfyTokenSet = token != ""
		notificationSender, err = alerts.NewNTFYSender(alerts.NTFYConfig{
			URL: cfg.NTFYURL, Topic: cfg.NTFYTopic, Token: token,
		}, nil)
		if err != nil {
			return fmt.Errorf("configure ntfy: %w", err)
		}
		deliveryProcessor, err = alerts.NewDeliveryProcessor(database, notificationSender, nil)
		if err != nil {
			return fmt.Errorf("configure alert delivery: %w", err)
		}
	}

	var hostBackend terminal.HostBackend
	if cfg.HostShellEnabled {
		hostClient, err := hostagent.NewClient(cfg.HostAgentSocket)
		if err != nil {
			return fmt.Errorf("configure host shell agent: %w", err)
		}
		hostBackend = hostAgentAdapter{client: hostClient}
	}
	terminalManager, err := terminal.NewManagerWithRemote(podmanClient, hostBackend, nodeTerminalAdapter{registry: nodeRegistry}, terminal.ManagerOptions{})
	if err != nil {
		return err
	}
	assets, err := homelab.Static()
	if err != nil {
		return fmt.Errorf("open embedded frontend: %w", err)
	}
	staticHandler, err := httpapi.NewStaticHandler(assets)
	if err != nil {
		return err
	}
	authManager := auth.NewManager(cfg.AdminUsers, cfg.TrustTailscaleHeaders, cfg.TrustTailscaleHeaders)
	api, err := httpapi.New(httpapi.Options{
		Auth: authManager, Metrics: hub, Services: serviceManager, Audit: database,
		Terminal: terminalManager, Static: staticHandler, SecureOrigin: cfg.TrustTailscaleHeaders,
		HostShellEnabled: cfg.HostShellEnabled, HostShellUsers: cfg.HostShellUsers,
		Nodes: nodeService, NodeRegistry: nodeRegistry,
		History: database, HistoryQuota: historyWriter.QuotaState,
		Alerts: database, Notifications: notificationSender, NTFYURL: cfg.NTFYURL, NTFYTopic: cfg.NTFYTopic, NTFYTokenSet: ntfyTokenSet,
		DashboardConfig:        dashboardConfig,
		DashboardConfigApplied: serviceManager.InvalidateHealth,
		Preferences:            database,
		SLO:                    sloService,
		Operations:             database,
		Topology:               database,
		Checks:                 database,
		Logs:                   logReader,
		Ready: func(readyContext context.Context) error {
			if err := database.Ping(readyContext); err != nil {
				return err
			}
			maxAge := cfg.MetricsInterval * 3
			if maxAge < 10*time.Second {
				maxAge = 10 * time.Second
			}
			return hub.Ready(maxAge)
		},
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       75 * time.Second,
		MaxHeaderBytes:    32 << 10,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	workerErrors := make(chan error, 8)
	collectorContext, cancelCollectors := context.WithCancel(context.Background())
	pipelineContext, cancelPipeline := context.WithCancel(context.Background())
	backgroundContext, cancelBackground := context.WithCancel(context.Background())
	writerContext, cancelWriter := context.WithCancel(context.Background())
	var collectors, pipelineWorkers, backgroundWorkers, writerWorkers workerGroup
	collectors.Go(collectorContext, workerErrors, "metrics hub", hub.Run)
	collectors.Go(collectorContext, workerErrors, "probe scheduler", probeScheduler.Run)
	writerWorkers.Go(writerContext, workerErrors, "history writer", historyWriter.Run)
	backgroundWorkers.Go(backgroundContext, workerErrors, "history maintenance", historyMaintenance.Run)
	backgroundWorkers.Go(backgroundContext, workerErrors, "TLS certificate scanner", tlsScanner.Run)
	backgroundWorkers.Go(backgroundContext, workerErrors, "operational retention", func(ctx context.Context) error {
		return runOperationalRetention(ctx, database, 24*time.Hour)
	})
	backgroundWorkers.Go(backgroundContext, workerErrors, "node availability", func(ctx context.Context) error {
		return runNodeAvailability(ctx, nodeRegistry, pipeline, 10*time.Second)
	})
	backgroundWorkers.Go(backgroundContext, workerErrors, "backup freshness", func(ctx context.Context) error {
		return runBackupFreshness(ctx, database, pipeline, 30*time.Second)
	})
	pipelineWorkers.Go(pipelineContext, workerErrors, "monitoring pipeline", func(ctx context.Context) error {
		return runMonitoringPipeline(ctx, pipeline, hub)
	})
	if deliveryProcessor != nil {
		backgroundWorkers.Go(backgroundContext, workerErrors, "alert delivery", func(ctx context.Context) error {
			return runAlertDelivery(ctx, deliveryProcessor)
		})
	}
	httpDone := make(chan struct{})
	go func() {
		defer close(httpDone)
		slog.Info("dashboard listening", "address", cfg.ListenAddress)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case workerErrors <- fmt.Errorf("HTTP server: %w", err):
			case <-ctx.Done():
			}
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-workerErrors:
		stop()
	}

	// Stop accepting requests and close hijacked WebSockets before draining the
	// worker pipeline. All stages share one deadline; a timeout leaves SQLite
	// open for the operating system to reclaim rather than racing a live worker.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	httpShutdown := make(chan error, 1)
	go func() { httpShutdown <- httpServer.Shutdown(shutdownCtx) }()
	websocketShutdown := make(chan error, 1)
	go func() { websocketShutdown <- api.Shutdown(shutdownCtx) }()

	cancelCollectors()
	shutdownErr := collectors.Wait(shutdownCtx)
	if shutdownErr == nil {
		shutdownErr = waitShutdownResult(shutdownCtx, websocketShutdown)
	}
	cancelPipeline()
	if shutdownErr == nil {
		shutdownErr = pipelineWorkers.Wait(shutdownCtx)
	}
	cancelBackground()
	if shutdownErr == nil {
		shutdownErr = backgroundWorkers.Wait(shutdownCtx)
	}
	cancelWriter()
	if shutdownErr == nil {
		shutdownErr = writerWorkers.Wait(shutdownCtx)
	}
	if shutdownErr == nil {
		shutdownErr = waitShutdownResult(shutdownCtx, httpShutdown)
	}
	if shutdownErr == nil {
		shutdownErr = waitShutdownDone(shutdownCtx, httpDone)
	}
	if shutdownErr != nil {
		// Ensure every cooperative worker observes cancellation even when an
		// earlier stage consumed the deadline. Do not close the database while
		// an uncooperative handler or worker may still reference it.
		cancelCollectors()
		cancelPipeline()
		cancelBackground()
		cancelWriter()
		databaseSafeToClose = false
		if closeErr := httpServer.Close(); closeErr != nil {
			shutdownErr = errors.Join(shutdownErr, fmt.Errorf("force close HTTP server: %w", closeErr))
		}
		runErr = errors.Join(runErr, fmt.Errorf("graceful shutdown: %w", shutdownErr))
	}
	return runErr
}

func waitShutdownResult(ctx context.Context, result <-chan error) error {
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitShutdownDone(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func seedDefaultAlertRules(ctx context.Context, database *store.Store) error {
	if err := database.SeedDefaultAlertRules(ctx, alerts.DefaultRules()); err != nil {
		return fmt.Errorf("seed default alert rules: %w", err)
	}
	return nil
}

func runAlertDelivery(ctx context.Context, processor *alerts.DeliveryProcessor) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if _, err := processor.RunOnce(ctx, 20); err != nil && ctx.Err() == nil {
			slog.Warn("alert notification delivery failed; retry scheduled", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type operationalRetainer interface {
	RetainOperationalData(context.Context, time.Time) error
}

func runOperationalRetention(ctx context.Context, repository operationalRetainer, interval time.Duration) error {
	if repository == nil {
		return errors.New("operational retention requires a repository")
	}
	if interval <= 0 {
		return errors.New("operational retention interval must be positive")
	}
	retain := func() error {
		if err := repository.RetainOperationalData(ctx, time.Now().UTC()); err != nil {
			return fmt.Errorf("retain operational data: %w", err)
		}
		return nil
	}
	if err := retain(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := retain(); err != nil {
				return err
			}
		}
	}
}

type nodeStateSource interface {
	States(context.Context) ([]nodes.NodeState, error)
}

type backupFreshnessSource interface {
	ListNodes(context.Context) ([]nodes.Node, error)
	ListBackupObservations(context.Context, string) ([]healthchecks.BackupObservation, error)
}

const initialNodeConnectionGrace = 2 * time.Minute

func shouldEvaluateNodeAvailability(state nodes.NodeState, at time.Time) bool {
	if state.Node.ID == "" {
		return false
	}
	if state.Online || state.LastSeenAt != nil || state.Snapshot != nil {
		return true
	}
	if state.Node.CreatedAt.IsZero() {
		return true
	}
	return !at.Before(state.Node.CreatedAt.Add(initialNodeConnectionGrace))
}

func runNodeAvailability(ctx context.Context, source nodeStateSource, pipeline *monitoring.Pipeline, interval time.Duration) error {
	if source == nil || pipeline == nil || interval <= 0 {
		return errors.New("node availability requires a source, pipeline, and positive interval")
	}
	evaluate := func(at time.Time) error {
		states, err := source.States(ctx)
		if err != nil {
			return fmt.Errorf("list node availability: %w", err)
		}
		for _, state := range states {
			if !shouldEvaluateNodeAvailability(state, at) {
				continue
			}
			if err := pipeline.HandleNodeAvailability(ctx, state.Node.ID, state.Online, at); err != nil {
				return fmt.Errorf("evaluate node availability %s: %w", state.Node.ID, err)
			}
		}
		return nil
	}
	if err := evaluate(time.Now().UTC()); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case at := <-ticker.C:
			if err := evaluate(at.UTC()); err != nil {
				return err
			}
		}
	}
}

// runBackupFreshness evaluates persisted reports independently from incoming
// snapshots. This lets a missed report become an alert while retaining the
// latest known report, including reports from every active remote node.
func runBackupFreshness(ctx context.Context, source backupFreshnessSource, pipeline *monitoring.Pipeline, interval time.Duration) error {
	if source == nil || pipeline == nil || interval <= 0 {
		return errors.New("backup freshness requires a source, pipeline, and positive interval")
	}
	evaluate := func(at time.Time) error {
		return evaluateBackupFreshness(ctx, source, pipeline, at)
	}
	if err := evaluate(time.Now().UTC()); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case at := <-ticker.C:
			if err := evaluate(at.UTC()); err != nil {
				return err
			}
		}
	}
}

func evaluateBackupFreshness(ctx context.Context, source backupFreshnessSource, pipeline *monitoring.Pipeline, at time.Time) error {
	activeNodes, err := source.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("list active monitoring nodes for backup freshness: %w", err)
	}
	monitoredNodes := make(map[string]struct{}, len(activeNodes)+1)
	monitoredNodes[history.LocalNodeID] = struct{}{}
	for _, node := range activeNodes {
		monitoredNodes[node.ID] = struct{}{}
	}
	observations, err := source.ListBackupObservations(ctx, "")
	if err != nil {
		return fmt.Errorf("list backup observations for freshness: %w", err)
	}
	activeObservations := make([]healthchecks.BackupObservation, 0, len(observations))
	for _, observation := range observations {
		if _, active := monitoredNodes[observation.NodeID]; active {
			activeObservations = append(activeObservations, observation)
		}
	}
	if err := pipeline.HandleBackupFreshness(ctx, activeObservations, at); err != nil {
		return fmt.Errorf("evaluate backup freshness: %w", err)
	}
	return nil
}

// runMonitoringPipeline drains snapshots already queued by the metrics hub
// after cancellation. This closes the producer side before the history writer
// is told to flush and exit.
func runMonitoringPipeline(ctx context.Context, pipeline *monitoring.Pipeline, hub *metrics.Hub) error {
	if pipeline == nil || hub == nil {
		return errors.New("monitoring pipeline and metrics hub are required")
	}
	updates, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	handle := func(handleContext context.Context, snapshot model.SnapshotEnvelope) {
		if err := pipeline.Handle(handleContext, history.LocalNodeID, snapshot); err != nil {
			slog.Warn("monitoring snapshot processing failed", "error", err)
		}
	}
	for {
		select {
		case snapshot, ok := <-updates:
			if !ok {
				return nil
			}
			handle(ctx, snapshot)
		case <-ctx.Done():
			drainContext := context.WithoutCancel(ctx)
			for {
				select {
				case snapshot, ok := <-updates:
					if !ok {
						return ctx.Err()
					}
					handle(drainContext, snapshot)
				default:
					return ctx.Err()
				}
			}
		}
	}
}

func loadSecretFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<10 || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("secret file must be regular, no larger than 8 KiB, and inaccessible to group or others")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(contents))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return "", errors.New("secret file must contain one non-empty line")
	}
	return secret, nil
}

type hostAgentAdapter struct {
	client *hostagent.Client
}

func (adapter hostAgentAdapter) Probe(ctx context.Context) error {
	return adapter.client.Probe(ctx)
}

func (adapter hostAgentAdapter) Open(ctx context.Context, size terminal.HostSize) (terminal.HostStream, error) {
	session, err := adapter.client.Open(ctx, hostagent.Size{Cols: size.Cols, Rows: size.Rows})
	if err != nil {
		return nil, err
	}
	return hostSessionAdapter{Session: session}, nil
}

type hostSessionAdapter struct {
	hostagent.Session
}

type nodeTerminalAdapter struct {
	registry *nodes.Registry
}

func (adapter nodeTerminalAdapter) Probe(_ context.Context, nodeID string) error {
	return adapter.registry.Probe(nodeID)
}

func (adapter nodeTerminalAdapter) OpenLogs(ctx context.Context, nodeID, containerID string, options podman.LogsOptions) (terminal.RemoteStream, error) {
	since := ""
	if options.Since > 0 {
		since = options.Since.String()
	}
	return adapter.open(ctx, nodeID, nodes.MessageLogsOpen, nodes.LogsOpen{
		ContainerID: containerID, Tail: int(options.Tail), Since: since,
		Follow: options.Follow, Timestamps: options.Timestamps,
	}, true)
}

func (adapter nodeTerminalAdapter) OpenExec(ctx context.Context, nodeID, containerID string, size terminal.HostSize) (terminal.RemoteStream, error) {
	return adapter.open(ctx, nodeID, nodes.MessageExecOpen, nodes.ShellOpen{
		ContainerID: containerID, Cols: uint16(size.Cols), Rows: uint16(size.Rows),
	}, false)
}

func (adapter nodeTerminalAdapter) OpenHost(ctx context.Context, nodeID string, size terminal.HostSize) (terminal.RemoteStream, error) {
	return adapter.open(ctx, nodeID, nodes.MessageHostOpen, nodes.ShellOpen{
		Cols: uint16(size.Cols), Rows: uint16(size.Rows),
	}, false)
}

func (adapter nodeTerminalAdapter) open(ctx context.Context, nodeID, messageType string, payload any, readOnly bool) (terminal.RemoteStream, error) {
	stream, err := adapter.registry.OpenStream(ctx, nodeID, messageType, payload, readOnly)
	if err != nil {
		return nil, err
	}
	return nodeStreamAdapter{Stream: stream}, nil
}

type nodeStreamAdapter struct{ *nodes.Stream }

func (adapter nodeStreamAdapter) Resize(ctx context.Context, size terminal.HostSize) error {
	return adapter.Stream.Resize(ctx, uint16(size.Cols), uint16(size.Rows))
}

func (adapter nodeStreamAdapter) Info() terminal.HostInfo {
	info := adapter.Stream.Info()
	return terminal.HostInfo{Hostname: info.Hostname, User: info.User, Shell: info.Shell}
}

func (adapter hostSessionAdapter) Read(target []byte) (int, error) {
	read, err := adapter.Session.Read(target)
	switch {
	case errors.Is(err, hostagent.ErrIdleTimeout):
		return read, terminal.ErrIdleTimeout
	case errors.Is(err, hostagent.ErrHardTimeout):
		return read, terminal.ErrHardTimeout
	default:
		return read, err
	}
}

func (adapter hostSessionAdapter) Resize(ctx context.Context, size terminal.HostSize) error {
	return adapter.Session.Resize(ctx, hostagent.Size{Cols: size.Cols, Rows: size.Rows})
}

func (adapter hostSessionAdapter) Info() terminal.HostInfo {
	info := adapter.Session.Info()
	return terminal.HostInfo{Hostname: info.Hostname, User: info.User, Shell: info.Shell}
}

func runWorker(ctx context.Context, errorsOut chan<- error, name string, worker func(context.Context) error) {
	if err := worker(ctx); err != nil && !errors.Is(err, context.Canceled) {
		select {
		case errorsOut <- fmt.Errorf("%s: %w", name, err):
		case <-ctx.Done():
		}
	}
}

type workerGroup struct {
	wait sync.WaitGroup
}

func (group *workerGroup) Go(ctx context.Context, errorsOut chan<- error, name string, worker func(context.Context) error) {
	group.wait.Add(1)
	go func() {
		defer group.wait.Done()
		runWorker(ctx, errorsOut, name, worker)
	}()
}

func (group *workerGroup) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		group.wait.Wait()
		close(done)
	}()
	return waitShutdownDone(ctx, done)
}

func healthcheck(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	return nil
}

type podmanSource struct {
	collector *containers.Collector
	cores     int

	mu     sync.RWMutex
	alerts []model.Alert
}

type dashboardAlertSource struct {
	runtime    *podmanSource
	repository *store.Store
}

func (source dashboardAlertSource) alertsSnapshot(ctx context.Context) ([]model.Alert, error) {
	runtimeAlerts, err := source.runtime.alertsSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := source.repository.ListAlertRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list dashboard alert rules: %w", err)
	}
	states, err := source.repository.ListAlertStates(ctx, alerts.StateFilter{ActiveOnly: true, Limit: 100})
	if err != nil {
		return nil, fmt.Errorf("list dashboard alert states: %w", err)
	}
	result := append(runtimeAlerts, alerts.SnapshotAlerts(rules, states)...)
	activeNodes, nodesErr := source.repository.ListNodes(ctx)
	if nodesErr != nil {
		return nil, fmt.Errorf("list active monitoring nodes for backup alerts: %w", nodesErr)
	}
	monitoredNodes := make(map[string]struct{}, len(activeNodes)+1)
	monitoredNodes[history.LocalNodeID] = struct{}{}
	for _, node := range activeNodes {
		monitoredNodes[node.ID] = struct{}{}
	}
	backupObservations, backupErr := source.repository.ListBackupObservations(ctx, "")
	if backupErr != nil {
		return nil, fmt.Errorf("list backup observations for alerts: %w", backupErr)
	}
	backupAlerts := make([]model.Alert, 0, len(backupObservations))
	now := time.Now().UTC()
	for _, observation := range backupObservations {
		if _, monitored := monitoredNodes[observation.NodeID]; !monitored {
			continue
		}
		if alert, unhealthy := backupAlertForNode(observation.NodeID, observation.Status, now); unhealthy {
			backupAlerts = append(backupAlerts, alert)
		}
	}
	result = append(result, backupAlerts...)
	certificates, certErr := source.repository.ListCertificateObservations(ctx)
	if certErr != nil {
		return nil, fmt.Errorf("list certificate observations for alerts: %w", certErr)
	}
	for _, certificate := range certificates {
		level, message := "", ""
		switch {
		case certificate.Error != "":
			level, message = "error", "TLS certificate check failed"
		case !certificate.NotAfter.IsZero() && certificate.NotAfter.Before(now.Add(7*24*time.Hour)):
			level, message = "error", "TLS certificate expires in less than 7 days"
		case !certificate.NotAfter.IsZero() && certificate.NotAfter.Before(now.Add(30*24*time.Hour)):
			level, message = "warning", "TLS certificate expires in less than 30 days"
		}
		if level != "" {
			result = append(result, model.Alert{ID: "tls:" + certificate.ServiceID, Level: level, Source: "tls", Message: message, OccurredAt: now})
		}
	}
	return result, nil
}

func backupAlertForNode(nodeID string, status model.BackupStatus, now time.Time) (model.Alert, bool) {
	healthy, _, reason := healthchecks.BackupFreshness(status, now)
	if healthy {
		return model.Alert{}, false
	}
	message := "Backup " + status.Job + " on " + nodeID + " needs attention"
	if reason != "" {
		message += ": " + reason
	}
	return model.Alert{
		ID: "backup:" + nodeID + ":" + status.Job, Level: "warning", Source: nodeID + "/backup",
		Message: message, OccurredAt: now.UTC(),
	}, true
}

func newPodmanSource(collector *containers.Collector, cores int) *podmanSource {
	if cores < 1 {
		cores = 1
	}
	return &podmanSource{collector: collector, cores: cores, alerts: make([]model.Alert, 0)}
}

func (s *podmanSource) containers(ctx context.Context) ([]model.Container, error) {
	items, alerts, err := s.collector.Collect(ctx, s.cores)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.alerts = append(s.alerts[:0], alerts...)
	s.mu.Unlock()
	return items, nil
}

func (s *podmanSource) alertsSnapshot(context.Context) ([]model.Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.Alert(nil), s.alerts...), nil
}
