package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

const MaxServices = 100

var ErrServiceLimit = errors.New("services: service limit reached")

type Repository interface {
	ListServices(context.Context) ([]model.Service, error)
	GetService(context.Context, string) (model.Service, error)
	CreateService(context.Context, model.Service) (model.Service, error)
	UpdateService(context.Context, string, model.ServiceInput) (model.Service, error)
	DeleteService(context.Context, string) error
}

type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "service input is invalid" }

type healthState struct {
	status       model.ServiceStatus
	lastChecked  time.Time
	latencyMS    int64
	failureCount int
}

type Manager struct {
	repository Repository

	mu     sync.RWMutex
	health map[string]healthState
}

func NewManager(repository Repository) *Manager {
	return &Manager{repository: repository, health: make(map[string]healthState)}
}

// InvalidateHealth clears probe results after an out-of-band configuration
// import. A changed URL must not inherit the previous endpoint's health until
// the scheduler probes the new definition.
func (m *Manager) InvalidateHealth() {
	m.mu.Lock()
	clear(m.health)
	m.mu.Unlock()
}

func (m *Manager) ListServices(ctx context.Context) ([]model.Service, error) {
	services, err := m.repository.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for index := range services {
		state, ok := m.health[services[index].ID]
		if !ok {
			services[index].Status = model.ServiceStatusUnknown
			continue
		}
		services[index].Status = state.status
		services[index].ConsecutiveFailures = state.failureCount
		checked := state.lastChecked
		latency := state.latencyMS
		services[index].LastCheckedAt = &checked
		services[index].LatencyMS = &latency
	}
	return services, nil
}

func (m *Manager) Get(ctx context.Context, id string) (model.Service, error) {
	service, err := m.repository.GetService(ctx, id)
	if err != nil {
		return model.Service{}, err
	}
	m.mu.RLock()
	state, ok := m.health[id]
	m.mu.RUnlock()
	if ok {
		service.Status = state.status
		service.ConsecutiveFailures = state.failureCount
		checked, latency := state.lastChecked, state.latencyMS
		service.LastCheckedAt, service.LatencyMS = &checked, &latency
	}
	return service, nil
}

func (m *Manager) Create(ctx context.Context, input model.ServiceInput) (model.Service, error) {
	input = normalize(input)
	if err := ValidateInput(input); err != nil {
		return model.Service{}, err
	}
	existing, err := m.repository.ListServices(ctx)
	if err != nil {
		return model.Service{}, err
	}
	if len(existing) >= MaxServices {
		return model.Service{}, ErrServiceLimit
	}
	id, err := serviceID()
	if err != nil {
		return model.Service{}, err
	}
	return m.repository.CreateService(ctx, model.Service{
		ID: id, Name: input.Name, Icon: input.Icon,
		DisplayURL: input.DisplayURL, ProbeURL: input.ProbeURL,
		Status: model.ServiceStatusUnknown,
	})
}

func (m *Manager) Update(ctx context.Context, id string, input model.ServiceInput) (model.Service, error) {
	input = normalize(input)
	if err := ValidateInput(input); err != nil {
		return model.Service{}, err
	}
	service, err := m.repository.UpdateService(ctx, id, input)
	if err != nil {
		return model.Service{}, err
	}
	m.mu.Lock()
	delete(m.health, id)
	m.mu.Unlock()
	return service, nil
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	if err := m.repository.DeleteService(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.health, id)
	m.mu.Unlock()
	return nil
}

func (m *Manager) recordProbe(id string, status model.ServiceStatus, latencyMS int64, checkedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.health[id]
	state.lastChecked = checkedAt.UTC()
	state.latencyMS = latencyMS
	if status == model.ServiceStatusDown {
		state.failureCount++
		if state.failureCount >= 2 {
			state.status = model.ServiceStatusDown
		} else if state.status == "" {
			state.status = model.ServiceStatusUnknown
		}
	} else {
		state.failureCount = 0
		state.status = status
	}
	m.health[id] = state
}

func (m *Manager) recordUnknown(id string) {
	m.mu.Lock()
	delete(m.health, id)
	m.mu.Unlock()
}

func ValidateInput(input model.ServiceInput) error {
	fields := make(map[string]string)
	if count := utf8.RuneCountInString(input.Name); count < 1 || count > 40 {
		fields["name"] = "must contain between 1 and 40 characters"
	}
	if utf8.RuneCountInString(input.Icon) > 8 || len(input.Icon) > 32 {
		fields["icon"] = "must not exceed 8 characters or 32 bytes"
	}
	if err := validateHTTPURL(input.DisplayURL); err != nil {
		fields["displayUrl"] = err.Error()
	}
	if input.ProbeURL != "" {
		if err := validateProbeURL(input.ProbeURL); err != nil {
			fields["probeUrl"] = err.Error()
		}
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func validateProbeURL(value string) error {
	parsed, err := url.Parse(value)
	if err == nil && strings.EqualFold(parsed.Scheme, "tcp") {
		return validateTCPURL(value)
	}
	return validateHTTPURL(value)
}

func validateHTTPURL(value string) error {
	if len(value) > 2048 {
		return fmt.Errorf("must not exceed 2048 bytes")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || !parsed.IsAbs() {
		return fmt.Errorf("must be an absolute HTTP or HTTPS URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return fmt.Errorf("must not contain credentials")
	}
	return nil
}

func normalize(input model.ServiceInput) model.ServiceInput {
	input.Name = strings.TrimSpace(input.Name)
	input.Icon = strings.TrimSpace(input.Icon)
	input.DisplayURL = strings.TrimSpace(input.DisplayURL)
	if input.DisplayURL == "" {
		input.DisplayURL = strings.TrimSpace(input.URL)
	}
	if input.DisplayURL == "" {
		input.DisplayURL = strings.TrimSpace(input.Port)
	}
	input.URL = ""
	input.Port = ""
	input.ProbeURL = strings.TrimSpace(input.ProbeURL)
	return input
}

func serviceID() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate service ID: %w", err)
	}
	return "svc_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}
