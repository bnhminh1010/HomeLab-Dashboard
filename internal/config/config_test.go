package config

import (
	"testing"
	"time"
)

func TestLoadFromDefaultsAndOverrides(t *testing.T) {
	values := map[string]string{
		"ADMIN_USERS":           "Alice@Example.com, bob@example.com, alice@example.com",
		"METRICS_INTERVAL":      "5s",
		"PROBE_CONCURRENCY":     "8",
		"TAILSCALE_SOCKS5_ADDR": "127.0.0.1:1055",
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
	if len(cfg.ProbeAllowCIDRs) != 5 || cfg.ProbeConcurrency != 8 {
		t.Fatalf("unexpected probe config: %+v", cfg)
	}
	if cfg.TailscaleSOCKS5Address != "127.0.0.1:1055" {
		t.Fatalf("unexpected SOCKS5 address: %q", cfg.TailscaleSOCKS5Address)
	}
}

func TestLoadFromRejectsBadValues(t *testing.T) {
	for key, value := range map[string]string{
		"METRICS_INTERVAL":      "0s",
		"PROBE_CONCURRENCY":     "99",
		"PROBE_ALLOW_CIDRS":     "not-a-network",
		"TAILSCALE_SOCKS5_ADDR": "10.0.0.2:1055",
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
