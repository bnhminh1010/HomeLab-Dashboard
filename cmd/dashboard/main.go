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
	"sync"
	"syscall"
	"time"

	homelab "github.com/binhminh/HomeLab-Minh"
	"github.com/binhminh/HomeLab-Minh/internal/auth"
	"github.com/binhminh/HomeLab-Minh/internal/config"
	"github.com/binhminh/HomeLab-Minh/internal/containers"
	"github.com/binhminh/HomeLab-Minh/internal/httpapi"
	"github.com/binhminh/HomeLab-Minh/internal/metrics"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/podman"
	"github.com/binhminh/HomeLab-Minh/internal/services"
	"github.com/binhminh/HomeLab-Minh/internal/store"
	"github.com/binhminh/HomeLab-Minh/internal/terminal"
)

func main() {
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := store.Open(ctx, cfg.DataPath)
	if err != nil {
		return err
	}
	defer database.Close()

	serviceManager := services.NewManager(database)
	prober, err := services.NewProber(services.ProbePolicy{
		AllowedPrefixes: cfg.ProbeAllowCIDRs,
		SOCKS5Address:   cfg.TailscaleSOCKS5Address,
	})
	if err != nil {
		return fmt.Errorf("configure service probes: %w", err)
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
	hub := metrics.NewHub(metrics.Sources{
		Host:       hostCollector,
		Services:   serviceManager,
		Containers: metrics.ContainerSourceFunc(runtimeSource.containers),
		Alerts:     metrics.AlertSourceFunc(runtimeSource.alertsSnapshot),
	}, cfg.MetricsInterval)

	terminalManager, err := terminal.NewManager(podmanClient, terminal.ManagerOptions{})
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
	authManager := auth.NewManager(cfg.AdminUsers, cfg.TrustTailscaleHeaders)
	api, err := httpapi.New(httpapi.Options{
		Auth: authManager, Metrics: hub, Services: serviceManager, Audit: database,
		Terminal: terminalManager, Static: staticHandler, SecureOrigin: cfg.TrustTailscaleHeaders,
		Ready: database.Ping,
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

	workerErrors := make(chan error, 3)
	go runWorker(ctx, workerErrors, "metrics hub", hub.Run)
	go runWorker(ctx, workerErrors, "probe scheduler", probeScheduler.Run)
	go func() {
		slog.Info("dashboard listening", "address", cfg.ListenAddress)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			workerErrors <- fmt.Errorf("HTTP server: %w", err)
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-workerErrors:
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shutdown HTTP server: %w", err)
	}
	return runErr
}

func runWorker(ctx context.Context, errorsOut chan<- error, name string, worker func(context.Context) error) {
	if err := worker(ctx); err != nil && !errors.Is(err, context.Canceled) {
		select {
		case errorsOut <- fmt.Errorf("%s: %w", name, err):
		case <-ctx.Done():
		}
	}
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
