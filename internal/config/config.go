package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/logs"
)

type Config struct {
	ListenAddress          string
	DataPath               string
	HostProcPath           string
	HostSysPath            string
	HostRootPath           string
	PodmanSocket           string
	AdminUsers             []string
	HostShellEnabled       bool
	HostShellUsers         []string
	HostAgentSocket        string
	ProbeAllowCIDRs        []netip.Prefix
	MetricsInterval        time.Duration
	ProbeInterval          time.Duration
	ProbeTimeout           time.Duration
	ProbeConcurrency       int
	NetworkInterface       string
	TailscaleSOCKS5Address string
	TrustTailscaleHeaders  bool
	HistoryQuotaBytes      int64
	NTFYURL                string
	NTFYTopic              string
	NTFYTokenFile          string
	BackupStatusFile       string
	LogsBackend            string
	LokiURL                string
}

func Load() (Config, error) {
	return LoadFrom(os.Getenv)
}

func LoadFrom(getenv func(string) string) (Config, error) {
	cfg := Config{
		ListenAddress:          valueOr(getenv("DASHBOARD_ADDR"), "127.0.0.1:8082"),
		DataPath:               valueOr(getenv("DATA_PATH"), "/data/dashboard.db"),
		HostProcPath:           valueOr(getenv("HOST_PROC_PATH"), "/host/proc"),
		HostSysPath:            valueOr(getenv("HOST_SYS_PATH"), "/host/sys"),
		HostRootPath:           valueOr(getenv("HOST_ROOT_PATH"), "/host/root"),
		PodmanSocket:           valueOr(getenv("PODMAN_SOCKET"), "/run/podman/podman.sock"),
		AdminUsers:             splitList(getenv("ADMIN_USERS")),
		HostShellUsers:         splitList(getenv("HOST_SHELL_USERS")),
		HostAgentSocket:        valueOr(getenv("HOST_AGENT_SOCKET"), "/run/host-agent/agent.sock"),
		MetricsInterval:        2 * time.Second,
		ProbeInterval:          15 * time.Second,
		ProbeTimeout:           3 * time.Second,
		ProbeConcurrency:       4,
		NetworkInterface:       strings.TrimSpace(getenv("NETWORK_INTERFACE")),
		TailscaleSOCKS5Address: strings.TrimSpace(getenv("TAILSCALE_SOCKS5_ADDR")),
		TrustTailscaleHeaders:  true,
		HistoryQuotaBytes:      2 << 30,
		NTFYURL:                strings.TrimSpace(getenv("NTFY_URL")),
		NTFYTopic:              strings.TrimSpace(getenv("NTFY_TOPIC")),
		NTFYTokenFile:          strings.TrimSpace(getenv("NTFY_TOKEN_FILE")),
		BackupStatusFile:       strings.TrimSpace(getenv("BACKUP_STATUS_FILE")),
		LogsBackend:            strings.ToLower(strings.TrimSpace(valueOr(getenv("LOGS_BACKEND"), logs.BackendDisabled))),
		LokiURL:                strings.TrimSpace(getenv("LOKI_URL")),
	}

	var err error
	if cfg.MetricsInterval, err = duration(getenv("METRICS_INTERVAL"), cfg.MetricsInterval); err != nil {
		return Config{}, fmt.Errorf("METRICS_INTERVAL: %w", err)
	}
	if cfg.ProbeInterval, err = duration(getenv("PROBE_INTERVAL"), cfg.ProbeInterval); err != nil {
		return Config{}, fmt.Errorf("PROBE_INTERVAL: %w", err)
	}
	if cfg.ProbeTimeout, err = duration(getenv("PROBE_TIMEOUT"), cfg.ProbeTimeout); err != nil {
		return Config{}, fmt.Errorf("PROBE_TIMEOUT: %w", err)
	}
	if value := strings.TrimSpace(getenv("PROBE_CONCURRENCY")); value != "" {
		cfg.ProbeConcurrency, err = strconv.Atoi(value)
		if err != nil || cfg.ProbeConcurrency < 1 || cfg.ProbeConcurrency > 32 {
			return Config{}, fmt.Errorf("PROBE_CONCURRENCY must be between 1 and 32")
		}
	}
	if value := strings.TrimSpace(getenv("TRUST_TAILSCALE_HEADERS")); value != "" {
		cfg.TrustTailscaleHeaders, err = strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("TRUST_TAILSCALE_HEADERS: %w", err)
		}
	}
	if value := strings.TrimSpace(getenv("HOST_SHELL_ENABLED")); value != "" {
		cfg.HostShellEnabled, err = strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("HOST_SHELL_ENABLED: %w", err)
		}
	}
	if value := strings.TrimSpace(getenv("HISTORY_QUOTA_BYTES")); value != "" {
		cfg.HistoryQuotaBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || cfg.HistoryQuotaBytes < 64<<20 || cfg.HistoryQuotaBytes > 16<<30 {
			return Config{}, fmt.Errorf("HISTORY_QUOTA_BYTES must be between 67108864 and 17179869184")
		}
	}

	defaultCIDRs := "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,fc00::/7"
	if cfg.ProbeAllowCIDRs, err = parsePrefixes(valueOr(getenv("PROBE_ALLOW_CIDRS"), defaultCIDRs)); err != nil {
		return Config{}, fmt.Errorf("PROBE_ALLOW_CIDRS: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ListenAddress) == "" {
		return fmt.Errorf("DASHBOARD_ADDR must not be empty")
	}
	for name, path := range map[string]string{
		"DATA_PATH": c.DataPath, "HOST_PROC_PATH": c.HostProcPath,
		"HOST_SYS_PATH": c.HostSysPath, "HOST_ROOT_PATH": c.HostRootPath,
		"PODMAN_SOCKET": c.PodmanSocket, "HOST_AGENT_SOCKET": c.HostAgentSocket,
	} {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if c.NTFYTokenFile != "" && !filepath.IsAbs(c.NTFYTokenFile) {
		return fmt.Errorf("NTFY_TOKEN_FILE must be an absolute path")
	}
	if c.BackupStatusFile != "" && !filepath.IsAbs(c.BackupStatusFile) {
		return fmt.Errorf("BACKUP_STATUS_FILE must be an absolute path")
	}
	if c.LogsBackend != logs.BackendDisabled && c.LogsBackend != logs.BackendLoki {
		return fmt.Errorf("LOGS_BACKEND must be disabled or loki")
	}
	if c.LogsBackend == logs.BackendLoki {
		endpoint, err := url.Parse(c.LokiURL)
		if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return fmt.Errorf("LOKI_URL must be an absolute HTTP or HTTPS URL when LOGS_BACKEND is loki")
		}
	}
	if (c.NTFYURL == "") != (c.NTFYTopic == "") {
		return fmt.Errorf("NTFY_URL and NTFY_TOPIC must be configured together")
	}
	if c.NTFYTopic != "" && !validNTFYTopic(c.NTFYTopic) {
		return fmt.Errorf("NTFY_TOPIC is invalid")
	}
	if c.HistoryQuotaBytes < 64<<20 || c.HistoryQuotaBytes > 16<<30 {
		return fmt.Errorf("HISTORY_QUOTA_BYTES must be between 67108864 and 17179869184")
	}
	if c.TrustTailscaleHeaders && !listenAddressIsLoopback(c.ListenAddress) {
		return fmt.Errorf("TRUST_TAILSCALE_HEADERS requires DASHBOARD_ADDR to use a loopback address")
	}
	adminUsers := make(map[string]struct{}, len(c.AdminUsers))
	for _, login := range c.AdminUsers {
		adminUsers[strings.ToLower(strings.TrimSpace(login))] = struct{}{}
	}
	for _, login := range c.HostShellUsers {
		if _, ok := adminUsers[strings.ToLower(strings.TrimSpace(login))]; !ok {
			return fmt.Errorf("HOST_SHELL_USERS must be a subset of ADMIN_USERS")
		}
	}
	if c.HostShellEnabled && len(c.HostShellUsers) == 0 {
		return fmt.Errorf("HOST_SHELL_USERS must contain at least one login when HOST_SHELL_ENABLED is true")
	}
	if c.MetricsInterval <= 0 || c.ProbeInterval <= 0 || c.ProbeTimeout <= 0 {
		return fmt.Errorf("intervals and timeouts must be positive")
	}
	if c.TailscaleSOCKS5Address != "" {
		host, port, err := net.SplitHostPort(c.TailscaleSOCKS5Address)
		if err != nil || port == "" {
			return fmt.Errorf("TAILSCALE_SOCKS5_ADDR must be a loopback host:port")
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("TAILSCALE_SOCKS5_ADDR has an invalid port")
		}
		ip, parseErr := netip.ParseAddr(host)
		if host != "localhost" && (parseErr != nil || !ip.IsLoopback()) {
			return fmt.Errorf("TAILSCALE_SOCKS5_ADDR must use a loopback host")
		}
	}
	return nil
}

func validNTFYTopic(topic string) bool {
	if topic == "" || len(topic) > 64 {
		return false
	}
	for _, char := range topic {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func listenAddressIsLoopback(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func valueOr(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; !ok {
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

func duration(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("must be a positive duration")
	}
	return parsed, nil
}

func parsePrefixes(value string) ([]netip.Prefix, error) {
	var result []netip.Prefix
	for _, raw := range strings.Split(value, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q", raw)
		}
		result = append(result, prefix.Masked())
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("at least one CIDR is required")
	}
	return result, nil
}
