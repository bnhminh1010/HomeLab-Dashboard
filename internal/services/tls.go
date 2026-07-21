package services

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/healthchecks"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	xproxy "golang.org/x/net/proxy"
)

// TLSInspection is intentionally limited to configured HTTPS service URLs.
// It is not a general network scanner and inherits the same CIDR allow-list
// and optional Tailscale SOCKS route as the ordinary service prober.
type TLSInspection struct {
	CheckedAt time.Time
	NotAfter  time.Time
	Issuer    string
	Error     string
}

type TLSInspector struct {
	client *http.Client
}

func NewTLSInspector(policy ProbePolicy) (*TLSInspector, error) {
	transport, err := newProbeTransport(policy)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &TLSInspector{client: &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

func (i *TLSInspector) Inspect(ctx context.Context, rawURL string) TLSInspection {
	result := TLSInspection{CheckedAt: time.Now().UTC()}
	if i == nil || i.client == nil {
		result.Error = "TLS inspector is unavailable"
		return result
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil {
		result.Error = "configured URL is not a valid HTTPS endpoint"
		return result
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, parsed.String(), nil)
	if err != nil {
		result.Error = "unable to build TLS request"
		return result
	}
	request.Header.Set("User-Agent", "HomeLab-Dashboard/1")
	response, err := i.client.Do(request)
	if err != nil {
		result.Error = sanitizeTLSError(err)
		return result
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.TLS == nil || len(response.TLS.PeerCertificates) == 0 {
		result.Error = "endpoint did not present a TLS certificate"
		return result
	}
	certificate := response.TLS.PeerCertificates[0]
	result.NotAfter = certificate.NotAfter.UTC()
	result.Issuer = certificate.Issuer.String()
	return result
}

func sanitizeTLSError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "TLS handshake failed"
	}
	if len(message) > 240 {
		return message[:240]
	}
	return message
}

type TLSServiceSource interface {
	ListServices(context.Context) ([]model.Service, error)
}

type CertificateRepository interface {
	UpsertCertificateObservation(context.Context, healthchecks.CertificateObservation) error
	ReconcileCertificateObservations(context.Context) error
}

// TLSScanner performs bounded, periodic certificate checks. A failed one
// service check does not prevent remaining configured services from being
// observed on the next pass.
type TLSScanner struct {
	services   TLSServiceSource
	inspector  *TLSInspector
	repository CertificateRepository
	interval   time.Duration
	timeout    time.Duration
}

func NewTLSScanner(source TLSServiceSource, inspector *TLSInspector, repository CertificateRepository) (*TLSScanner, error) {
	if source == nil || inspector == nil || repository == nil {
		return nil, errors.New("TLS scanner requires services, inspector, and repository")
	}
	return &TLSScanner{services: source, inspector: inspector, repository: repository, interval: 12 * time.Hour, timeout: 8 * time.Second}, nil
}

func (s *TLSScanner) Run(ctx context.Context) error {
	if err := s.ScanOnce(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_ = s.ScanOnce(ctx)
		}
	}
}

func (s *TLSScanner) ScanOnce(ctx context.Context) error {
	items, err := s.services.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list services for TLS scan: %w", err)
	}
	var failures []error
	for _, service := range items {
		endpoint := strings.TrimSpace(service.DisplayURL)
		if !isHTTPSURL(endpoint) || strings.TrimSpace(service.ID) == "" {
			continue
		}
		checkContext, cancel := context.WithTimeout(ctx, s.timeout)
		result := s.inspector.Inspect(checkContext, endpoint)
		cancel()
		observation := healthchecks.CertificateObservation{
			ServiceID: service.ID, CheckedAt: result.CheckedAt,
			NotAfter: result.NotAfter, Issuer: result.Issuer, Error: result.Error,
		}
		if err := s.repository.UpsertCertificateObservation(ctx, observation); err != nil {
			failures = append(failures, fmt.Errorf("store %s: %w", service.ID, err))
		}
	}
	if err := s.repository.ReconcileCertificateObservations(ctx); err != nil {
		failures = append(failures, fmt.Errorf("reconcile certificate observations: %w", err))
	}
	return errors.Join(failures...)
}

func isHTTPSURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && strings.EqualFold(parsed.Scheme, "https") && parsed.Hostname() != "" && parsed.User == nil
}

// probeTransport owns the policy-bound dialer shared by HTTP probes and TLS
// inspection. Keeping the policy in one place prevents the certificate check
// from becoming an SSRF bypass around normal endpoint probing.
func newProbeTransport(policy ProbePolicy) (*http.Transport, error) {
	if len(policy.AllowedPrefixes) == 0 {
		return nil, fmt.Errorf("at least one allowed probe CIDR is required")
	}
	if policy.Resolver == nil {
		policy.Resolver = net.DefaultResolver
	}
	if policy.Dialer == nil {
		policy.Dialer = &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	}
	var tailnetDialer interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	}
	if policy.SOCKS5Address != "" {
		if err := validateSOCKS5Address(policy.SOCKS5Address); err != nil {
			return nil, err
		}
		dialer, err := newSOCKS5Dialer(policy)
		if err != nil {
			return nil, err
		}
		tailnetDialer = dialer
	}
	return &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      false,
		MaxResponseHeaderBytes: 32 << 10,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("parse probe destination: %w", err)
			}
			addresses, err := resolveAllowed(ctx, policy.Resolver, host, policy.AllowedPrefixes)
			if err != nil {
				return nil, err
			}
			var failures []error
			for _, ip := range addresses {
				destination := net.JoinHostPort(ip.String(), port)
				var connection net.Conn
				if tailnetDialer != nil && isTailscaleAddress(ip) {
					connection, err = tailnetDialer.DialContext(ctx, network, destination)
				} else {
					connection, err = policy.Dialer.DialContext(ctx, network, destination)
				}
				if err == nil {
					return connection, nil
				}
				failures = append(failures, err)
			}
			return nil, errors.Join(failures...)
		},
	}, nil
}

func newSOCKS5Dialer(policy ProbePolicy) (xproxy.ContextDialer, error) {
	dialer, err := xproxy.SOCKS5("tcp", policy.SOCKS5Address, nil, policy.Dialer)
	if err != nil {
		return nil, fmt.Errorf("configure Tailscale SOCKS5 proxy: %w", err)
	}
	contextDialer, ok := dialer.(xproxy.ContextDialer)
	if !ok {
		return nil, errors.New("Tailscale SOCKS5 proxy does not support context cancellation")
	}
	return contextDialer, nil
}
