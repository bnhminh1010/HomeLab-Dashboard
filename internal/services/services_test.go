package services

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

type memoryRepository struct {
	mu       sync.Mutex
	services map[string]model.Service
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{services: make(map[string]model.Service)}
}

func (r *memoryRepository) ListServices(context.Context) ([]model.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]model.Service, 0, len(r.services))
	for _, service := range r.services {
		result = append(result, service)
	}
	return result, nil
}

func (r *memoryRepository) GetService(_ context.Context, id string) (model.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	service, ok := r.services[id]
	if !ok {
		return model.Service{}, errors.New("not found")
	}
	return service, nil
}

func (r *memoryRepository) CreateService(_ context.Context, service model.Service) (model.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[service.ID] = service
	return service, nil
}

func (r *memoryRepository) UpdateService(_ context.Context, id string, input model.ServiceInput) (model.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	service := r.services[id]
	service.Name, service.Icon = input.Name, input.Icon
	service.DisplayURL, service.ProbeURL = input.DisplayURL, input.ProbeURL
	r.services[id] = service
	return service, nil
}

func (r *memoryRepository) DeleteService(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.services, id)
	return nil
}

func TestManagerValidatesNormalizesAndCreates(t *testing.T) {
	manager := NewManager(newMemoryRepository())
	created, err := manager.Create(context.Background(), model.ServiceInput{
		Name: "  Immich  ", Icon: "  photo ", DisplayURL: " https://immich.example ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, "svc_") || created.Name != "Immich" || created.Icon != "photo" {
		t.Fatalf("unexpected created service: %+v", created)
	}

	_, err = manager.Create(context.Background(), model.ServiceInput{
		Name: "   ", DisplayURL: "javascript:alert(1)", ProbeURL: "http://user:pass@10.0.0.2",
	})
	var validation *ValidationError
	if !errors.As(err, &validation) || len(validation.Fields) != 3 {
		t.Fatalf("expected field validation errors, got %v", err)
	}
}

func TestManagerExposesConsecutiveProbeFailures(t *testing.T) {
	manager := NewManager(newMemoryRepository())
	created, err := manager.Create(context.Background(), model.ServiceInput{
		Name: "Probe", DisplayURL: "http://10.0.0.2", ProbeURL: "http://10.0.0.2/health",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manager.recordProbe(created.ID, model.ServiceStatusDown, 20, now)
	services, err := manager.ListServices(context.Background())
	if err != nil || len(services) != 1 {
		t.Fatalf("ListServices() = %#v, %v", services, err)
	}
	if services[0].ConsecutiveFailures != 1 || services[0].Status != model.ServiceStatusUnknown {
		t.Fatalf("first probe failure state = %#v", services[0])
	}
	manager.recordProbe(created.ID, model.ServiceStatusDown, 20, now.Add(time.Second))
	services, _ = manager.ListServices(context.Background())
	if services[0].ConsecutiveFailures != 2 || services[0].Status != model.ServiceStatusDown {
		t.Fatalf("second probe failure state = %#v", services[0])
	}
	manager.recordProbe(created.ID, model.ServiceStatusUp, 10, now.Add(2*time.Second))
	services, _ = manager.ListServices(context.Background())
	if services[0].ConsecutiveFailures != 0 || services[0].Status != model.ServiceStatusUp {
		t.Fatalf("successful probe did not reset failures: %#v", services[0])
	}
	manager.InvalidateHealth()
	services, _ = manager.ListServices(context.Background())
	if services[0].Status != model.ServiceStatusUnknown || services[0].LastCheckedAt != nil {
		t.Fatalf("health invalidation retained a prior endpoint result: %#v", services[0])
	}
}

type fakeResolver map[string][]netip.Addr

func (r fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

func TestResolveAllowedRejectsSSRFAndMixedDNS(t *testing.T) {
	resolver := fakeResolver{
		"private.test": {netip.MustParseAddr("10.0.0.2")},
		"public.test":  {netip.MustParseAddr("8.8.8.8")},
		"mixed.test":   {netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("8.8.8.8")},
	}
	allowed := DefaultAllowedPrefixes()
	addresses, err := resolveAllowed(context.Background(), resolver, "private.test", allowed)
	if err != nil || len(addresses) != 1 {
		t.Fatalf("private address rejected: %v", err)
	}
	for _, host := range []string{"public.test", "mixed.test", "169.254.169.254", "100.100.100.200", "127.0.0.1"} {
		if _, err := resolveAllowed(context.Background(), resolver, host, allowed); err == nil {
			t.Fatalf("expected %s to be rejected", host)
		}
	}
}

func TestTailscaleProxyAddressValidationAndRouting(t *testing.T) {
	for _, address := range []string{"127.0.0.1:1055", "localhost:1055", "[::1]:1055"} {
		if err := validateSOCKS5Address(address); err != nil {
			t.Fatalf("safe SOCKS5 address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"10.0.0.2:1055", "127.0.0.1", "127.0.0.1:invalid"} {
		if err := validateSOCKS5Address(address); err == nil {
			t.Fatalf("unsafe SOCKS5 address %q accepted", address)
		}
	}

	for address, expected := range map[string]bool{
		"100.64.0.1":          true,
		"100.127.255.254":     true,
		"fd7a:115c:a1e0::123": true,
		"10.0.0.2":            false,
		"fd00::1":             false,
	} {
		if actual := isTailscaleAddress(netip.MustParseAddr(address)); actual != expected {
			t.Fatalf("isTailscaleAddress(%s)=%v want %v", address, actual, expected)
		}
	}

	if _, err := NewProber(ProbePolicy{
		AllowedPrefixes: DefaultAllowedPrefixes(), SOCKS5Address: "10.0.0.2:1055",
	}); err == nil {
		t.Fatal("expected NewProber to reject a non-loopback proxy")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestProberClassifiesHTTPStatusWithoutReadingLargeBodies(t *testing.T) {
	for code, expected := range map[int]model.ServiceStatus{
		204: model.ServiceStatusUp,
		302: model.ServiceStatusUp,
		404: model.ServiceStatusDegraded,
		503: model.ServiceStatusDown,
	} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			prober := &Prober{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: code, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 70<<10))),
				}, nil
			})}}
			if result := prober.Probe(context.Background(), "http://10.0.0.2/health"); result.Status != expected {
				t.Fatalf("got %s want %s", result.Status, expected)
			}
		})
	}
}

type countingBody struct {
	reader io.Reader
	read   int
}

func (b *countingBody) Read(target []byte) (int, error) {
	count, err := b.reader.Read(target)
	b.read += count
	return count, err
}

func (*countingBody) Close() error { return nil }

func TestProberCapsResponseBodyAtFourKiB(t *testing.T) {
	body := &countingBody{reader: strings.NewReader(strings.Repeat("x", 32<<10))}
	prober := &Prober{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})}}
	result := prober.Probe(context.Background(), "http://10.0.0.2/health")
	if result.Status != model.ServiceStatusUp || body.read != 4<<10 {
		t.Fatalf("status=%s body bytes read=%d", result.Status, body.read)
	}
}

func TestValidateProbeURLAcceptsTCPAndRejectsUnsafeForms(t *testing.T) {
	for _, value := range []string{"tcp://redis:6379", "tcp://[fd7a:115c:a1e0::1]:5432"} {
		if err := validateProbeURL(value); err != nil {
			t.Fatalf("valid TCP probe %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"tcp://redis", "tcp://redis:0", "tcp://redis:65536", "tcp://user:pass@redis:6379", "tcp://redis:6379/health", "tcp://redis:6379?query=1", "udp://redis:6379"} {
		if err := validateProbeURL(value); err == nil {
			t.Fatalf("unsafe or invalid TCP probe %q accepted", value)
		}
	}
}

func TestProberTCPProbeUsesPolicyDialer(t *testing.T) {
	var dialedNetwork, dialedAddress string
	prober := &Prober{dialContext: func(_ context.Context, network, address string) (net.Conn, error) {
		dialedNetwork, dialedAddress = network, address
		connection, peer := net.Pipe()
		_ = peer.Close()
		return connection, nil
	}}
	result := prober.Probe(context.Background(), "tcp://redis:6379")
	if result.Status != model.ServiceStatusUp {
		t.Fatalf("TCP probe status = %s, want up", result.Status)
	}
	if dialedNetwork != "tcp" || dialedAddress != "redis:6379" {
		t.Fatalf("TCP dial = %s %s, want tcp redis:6379", dialedNetwork, dialedAddress)
	}
}

type staticProber struct{ status model.ServiceStatus }

func (p staticProber) Probe(context.Context, string) ProbeResult {
	return ProbeResult{Status: p.status, LatencyMS: 12}
}

func TestSchedulerRequiresTwoFailuresBeforeDown(t *testing.T) {
	repository := newMemoryRepository()
	repository.services["svc_1"] = model.Service{ID: "svc_1", ProbeURL: "http://10.0.0.2/ping"}
	manager := NewManager(repository)
	now := time.Unix(100, 0)
	scheduler := NewScheduler(manager, staticProber{status: model.ServiceStatusDown}, SchedulerOptions{
		Timeout: time.Second, Concurrency: 1, Now: func() time.Time { return now },
	})
	if err := scheduler.ProbeOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	services, _ := manager.ListServices(context.Background())
	if services[0].Status != model.ServiceStatusUnknown {
		t.Fatalf("first failure should remain unknown, got %s", services[0].Status)
	}
	now = now.Add(time.Second)
	if err := scheduler.ProbeOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	services, _ = manager.ListServices(context.Background())
	if services[0].Status != model.ServiceStatusDown || services[0].LatencyMS == nil || *services[0].LatencyMS != 12 {
		t.Fatalf("second failure should mark down: %+v", services[0])
	}
}
