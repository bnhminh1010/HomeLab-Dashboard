package config

import (
	"testing"
	"time"
)

func TestLoadFromDefaultsAndOverrides(t *testing.T) {
	values := map[string]string{
		"ADMIN_USERS":           "Alice@Example.com, bob@example.com, alice@example.com",
		"HOST_SHELL_ENABLED":    "true",
		"HOST_SHELL_USERS":      "alice@example.com",
		"HOST_AGENT_SOCKET":     "/run/user/1000/homelab/agent.sock",
		"METRICS_INTERVAL":      "5s",
		"PROBE_CONCURRENCY":     "8",
		"TAILSCALE_SOCKS5_ADDR": "127.0.0.1:1055",
		"HISTORY_QUOTA_BYTES":   "1073741824",
		"NTFY_URL":              "https://ntfy.example.com",
		"NTFY_TOPIC":            "homelab-alerts",
		"NTFY_TOKEN_FILE":       "/run/secrets/ntfy-token",
		"LOGS_BACKEND":          "loki",
		"LOKI_URL":              "http://loki:3100",
	}
	cfg, err := LoadFrom(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != "127.0.0.1:8082" || cfg.MetricsInterval != 5*time.Second {
		t.Fatalf("unexpected defaults/override: %+v", cfg)
	}
	if len(cfg.AdminUsers) != 2 || cfg.AdminUsers[0] != "alice@example.com" {
		t.Fatalf("admin normalization failed: %v", cfg.AdminUsers)
	}
	if !cfg.HostShellEnabled || len(cfg.HostShellUsers) != 1 || cfg.HostShellUsers[0] != "alice@example.com" {
		t.Fatalf("host shell config = enabled %t users %v", cfg.HostShellEnabled, cfg.HostShellUsers)
	}
	if cfg.HostAgentSocket != "/run/user/1000/homelab/agent.sock" {
		t.Fatalf("host agent socket = %q", cfg.HostAgentSocket)
	}
	if len(cfg.ProbeAllowCIDRs) != 5 || cfg.ProbeConcurrency != 8 {
		t.Fatalf("unexpected probe config: %+v", cfg)
	}
	if cfg.TailscaleSOCKS5Address != "127.0.0.1:1055" {
		t.Fatalf("unexpected SOCKS5 address: %q", cfg.TailscaleSOCKS5Address)
	}
	if cfg.HistoryQuotaBytes != 1<<30 || cfg.NTFYURL != "https://ntfy.example.com" || cfg.NTFYTokenFile != "/run/secrets/ntfy-token" {
		t.Fatalf("unexpected history/ntfy config: %+v", cfg)
	}
	if cfg.LogsBackend != "loki" || cfg.LokiURL != "http://loki:3100" {
		t.Fatalf("unexpected logs config: %+v", cfg)
	}
}

func TestLoadFromRejectsUnsafeIdentityProxyConfiguration(t *testing.T) {
	_, err := LoadFrom(func(key string) string {
		if key == "DASHBOARD_ADDR" {
			return "0.0.0.0:8082"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected non-loopback listener with trusted headers to fail")
	}

	cfg, err := LoadFrom(func(key string) string {
		switch key {
		case "DASHBOARD_ADDR":
			return "0.0.0.0:8082"
		case "TRUST_TAILSCALE_HEADERS":
			return "false"
		default:
			return ""
		}
	})
	if err != nil || cfg.TrustTailscaleHeaders {
		t.Fatalf("explicitly untrusted non-loopback config = %+v, %v", cfg, err)
	}
}

func TestLoadFromValidatesHostShellAllowlist(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"empty enabled allowlist": {
			"HOST_SHELL_ENABLED": "true",
			"ADMIN_USERS":        "admin@example.com",
		},
		"non-admin host user": {
			"HOST_SHELL_ENABLED": "true",
			"ADMIN_USERS":        "admin@example.com",
			"HOST_SHELL_USERS":   "viewer@example.com",
		},
		"relative agent socket": {
			"HOST_AGENT_SOCKET": "agent.sock",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFrom(func(key string) string { return values[key] })
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadFromRejectsBadValues(t *testing.T) {
	for key, value := range map[string]string{
		"METRICS_INTERVAL":      "0s",
		"PROBE_CONCURRENCY":     "99",
		"PROBE_ALLOW_CIDRS":     "not-a-network",
		"TAILSCALE_SOCKS5_ADDR": "10.0.0.2:1055",
		"HISTORY_QUOTA_BYTES":   "1024",
		"NTFY_TOKEN_FILE":       "token.txt",
		"LOGS_BACKEND":          "unsupported",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := LoadFrom(func(candidate string) string {
				if candidate == key {
					return value
				}
				return ""
			})
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadFromRequiresHTTPURLForLoki(t *testing.T) {
	for _, values := range []map[string]string{
		{"LOGS_BACKEND": "loki"},
		{"LOGS_BACKEND": "loki", "LOKI_URL": "file:///tmp/loki"},
	} {
		if _, err := LoadFrom(func(key string) string { return values[key] }); err == nil {
			t.Fatalf("expected invalid Loki configuration: %#v", values)
		}
	}
}

func TestLoadFromRequiresCompleteNTFYConfiguration(t *testing.T) {
	for _, values := range []map[string]string{
		{"NTFY_URL": "https://ntfy.example.com"},
		{"NTFY_TOPIC": "homelab"},
		{"NTFY_URL": "https://ntfy.example.com", "NTFY_TOPIC": "bad/topic"},
	} {
		if _, err := LoadFrom(func(key string) string { return values[key] }); err == nil {
			t.Fatalf("expected invalid ntfy config: %#v", values)
		}
	}
}
