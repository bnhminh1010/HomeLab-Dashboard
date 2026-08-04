package services

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type ProbePolicy struct {
	AllowedPrefixes []netip.Prefix
	Resolver        Resolver
	Dialer          *net.Dialer
	SOCKS5Address   string
}

type ProbeResult struct {
	Status    model.ServiceStatus
	LatencyMS int64
}

type ProbeClient interface {
	Probe(context.Context, string) ProbeResult
}

type Prober struct {
	client      *http.Client
	dialContext func(context.Context, string, string) (net.Conn, error)
}

var tailscalePrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

func NewProber(policy ProbePolicy) (*Prober, error) {
	if len(policy.AllowedPrefixes) == 0 {
		return nil, fmt.Errorf("at least one allowed probe CIDR is required")
	}
	if policy.Resolver == nil {
		policy.Resolver = net.DefaultResolver
	}
	if policy.Dialer == nil {
		policy.Dialer = &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	}
	transport, err := newProbeTransport(policy)
	if err != nil {
		return nil, err
	}
	return &Prober{client: &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, dialContext: transport.DialContext}, nil
}

func validateSOCKS5Address(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse Tailscale SOCKS5 address: %w", err)
	}
	if port == "" {
		return fmt.Errorf("Tailscale SOCKS5 address requires a port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("Tailscale SOCKS5 address has an invalid port")
	}
	ip, parseErr := netip.ParseAddr(host)
	if host != "localhost" && (parseErr != nil || !ip.IsLoopback()) {
		return fmt.Errorf("Tailscale SOCKS5 proxy must listen on loopback")
	}
	return nil
}

func isTailscaleAddress(address netip.Addr) bool {
	address = address.Unmap()
	for _, prefix := range tailscalePrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (p *Prober) Probe(ctx context.Context, rawURL string) ProbeResult {
	started := time.Now()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ProbeResult{Status: model.ServiceStatusDown}
	}
	if strings.EqualFold(parsed.Scheme, "tcp") {
		if err := validateTCPURL(rawURL); err != nil || p == nil || p.dialContext == nil {
			return ProbeResult{Status: model.ServiceStatusDown}
		}
		connection, err := p.dialContext(ctx, "tcp", parsed.Host)
		latency := time.Since(started).Milliseconds()
		if err != nil {
			return ProbeResult{Status: model.ServiceStatusDown, LatencyMS: latency}
		}
		_ = connection.Close()
		return ProbeResult{Status: model.ServiceStatusUp, LatencyMS: latency}
	}
	if err := validateHTTPURL(rawURL); err != nil || p == nil || p.client == nil {
		return ProbeResult{Status: model.ServiceStatusDown}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ProbeResult{Status: model.ServiceStatusDown}
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("User-Agent", "HomeLab-Dashboard/1")
	response, err := p.client.Do(request)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return ProbeResult{Status: model.ServiceStatusDown, LatencyMS: latency}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	status := model.ServiceStatusDown
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 400:
		status = model.ServiceStatusUp
	case response.StatusCode >= 400 && response.StatusCode < 500:
		status = model.ServiceStatusDegraded
	}
	return ProbeResult{Status: status, LatencyMS: latency}
}

func validateTCPURL(value string) error {
	if len(value) > 2048 {
		return fmt.Errorf("must not exceed 2048 bytes")
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "tcp") || parsed.Hostname() == "" || parsed.Port() == "" || parsed.User != nil {
		return fmt.Errorf("must be an absolute TCP endpoint in the form tcp://host:port")
	}
	if parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("TCP probe must not contain a path, query, or fragment")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("TCP probe port must be between 1 and 65535")
	}
	if _, _, err := net.SplitHostPort(parsed.Host); err != nil {
		return fmt.Errorf("TCP probe must include a valid host and port")
	}
	return nil
}

func resolveAllowed(ctx context.Context, resolver Resolver, host string, allowed []netip.Prefix) ([]netip.Addr, error) {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.Trim(host, "[]")
	}
	var addresses []netip.Addr
	if literal, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{literal.Unmap()}
	} else {
		resolved, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve probe destination: %w", err)
		}
		addresses = resolved
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("probe destination resolved to no addresses")
	}
	for index := range addresses {
		addresses[index] = addresses[index].Unmap()
		address := addresses[index]
		if address == netip.AddrFrom4([4]byte{100, 100, 100, 200}) ||
			address.IsUnspecified() || address.IsLoopback() || address.IsLinkLocalUnicast() ||
			address.IsLinkLocalMulticast() || address.IsMulticast() {
			return nil, fmt.Errorf("probe destination %s is not allowed", address)
		}
		permitted := false
		for _, prefix := range allowed {
			if prefix.Contains(address) {
				permitted = true
				break
			}
		}
		if !permitted {
			return nil, fmt.Errorf("probe destination %s is outside the allowlist", address)
		}
	}
	return addresses, nil
}

type SchedulerOptions struct {
	Interval    time.Duration
	Timeout     time.Duration
	Concurrency int
	Now         func() time.Time
}

type Scheduler struct {
	manager *Manager
	prober  ProbeClient
	options SchedulerOptions
}

func NewScheduler(manager *Manager, prober ProbeClient, options SchedulerOptions) *Scheduler {
	if options.Interval <= 0 {
		options.Interval = 15 * time.Second
	}
	if options.Timeout <= 0 {
		options.Timeout = 3 * time.Second
	}
	if options.Concurrency <= 0 {
		options.Concurrency = 4
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Scheduler{manager: manager, prober: prober, options: options}
}

func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.ProbeOnce(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	ticker := time.NewTicker(s.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_ = s.ProbeOnce(ctx)
		}
	}
}

func (s *Scheduler) ProbeOnce(ctx context.Context) error {
	services, err := s.manager.repository.ListServices(ctx)
	if err != nil {
		return err
	}
	semaphore := make(chan struct{}, s.options.Concurrency)
	var wg sync.WaitGroup
	for _, service := range services {
		service := service
		if service.ProbeURL == "" {
			s.manager.recordUnknown(service.ID)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()
			probeCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
			result := s.prober.Probe(probeCtx, service.ProbeURL)
			cancel()
			s.manager.recordProbe(service.ID, result.Status, result.LatencyMS, s.options.Now())
		}()
	}
	wg.Wait()
	return ctx.Err()
}

func DefaultAllowedPrefixes() []netip.Prefix {
	values := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"}
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, _ := netip.ParsePrefix(value)
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}
