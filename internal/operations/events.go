// Package operations defines concise, user-facing records for the dashboard's
// operational timeline. It deliberately does not reuse audit records, which
// can contain request metadata unsuitable for the operations UI.
package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Retention        = 90 * 24 * time.Hour
	DefaultListLimit = 100
	MaxListLimit     = 500
)

var (
	ErrInvalidEvent  = errors.New("operations: invalid event")
	ErrInvalidFilter = errors.New("operations: invalid event filter")
)

type Source string

const (
	SourceAutomatic Source = "automatic"
	SourceManual    Source = "manual"
)

type Visibility string

const (
	VisibilityNormal    Visibility = "normal"
	VisibilitySensitive Visibility = "sensitive"
)

const (
	EventDeploy      = "deploy"
	EventMaintenance = "maintenance"
	EventNote        = "note"

	EventServiceCreated        = "service.created"
	EventServiceUpdated        = "service.updated"
	EventServiceDeleted        = "service.deleted"
	EventServiceSLOChanged     = "service.slo_changed"
	EventServiceHealthChanged  = "service.health_changed"
	EventAlertRuleChanged      = "alert_rule.changed"
	EventConfigurationImported = "configuration.imported"
	EventTopologyChanged       = "topology.dependency.changed"
	EventNodeConnected         = "node.connected"
	EventNodeDisconnected      = "node.disconnected"
	EventContainerRestarted    = "container.restarted"
	EventContainerStopped      = "container.stopped"
	EventBackupReported        = "backup.reported"
)

// Event contains only the information safe and useful to show in the
// operational timeline. Detailed audit metadata belongs to audit_events.
type Event struct {
	ID          int64      `json:"id"`
	Type        string     `json:"type"`
	Source      Source     `json:"source"`
	Visibility  Visibility `json:"visibility"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary,omitempty"`
	NodeID      string     `json:"nodeId,omitempty"`
	ServiceID   string     `json:"serviceId,omitempty"`
	ContainerID string     `json:"containerId,omitempty"`
	Actor       string     `json:"actor,omitempty"`
	OccurredAt  time.Time  `json:"occurredAt"`
}

// Filter keeps timeline reads bounded and supports the dimensions used by the
// overview, history charts, and service/node detail views.
type Filter struct {
	From       time.Time
	To         time.Time
	Type       string
	NodeID     string
	ServiceID  string
	Source     Source
	Visibility Visibility
	Limit      int
}

// Repository is the narrow storage contract used by HTTP handlers, mutation
// hooks, and monitoring pipeline integrations.
type Repository interface {
	RecordOperationalEvent(context.Context, Event) (Event, error)
	CreateManualOperationalEvent(context.Context, Event) (Event, error)
	ListOperationalEvents(context.Context, Filter) ([]Event, error)
	PurgeOperationalEvents(context.Context, time.Time) (int64, error)
}

func NormalizeEvent(event Event) Event {
	event.Type = strings.TrimSpace(event.Type)
	event.Title = strings.TrimSpace(event.Title)
	event.Summary = strings.TrimSpace(event.Summary)
	event.NodeID = strings.TrimSpace(event.NodeID)
	event.ServiceID = strings.TrimSpace(event.ServiceID)
	event.ContainerID = strings.TrimSpace(event.ContainerID)
	event.Actor = strings.TrimSpace(event.Actor)
	if event.Visibility == "" {
		event.Visibility = VisibilityNormal
	}
	event.OccurredAt = event.OccurredAt.UTC()
	return event
}

func ValidateEvent(event Event) error {
	event = NormalizeEvent(event)
	if !validType(event.Type) {
		return fmt.Errorf("%w: type is required and must use lowercase letters, digits, dots, underscores, or dashes", ErrInvalidEvent)
	}
	if event.Source != SourceAutomatic && event.Source != SourceManual {
		return fmt.Errorf("%w: source must be automatic or manual", ErrInvalidEvent)
	}
	if event.Source == SourceManual && !isManualType(event.Type) {
		return fmt.Errorf("%w: manual type must be deploy, maintenance, or note", ErrInvalidEvent)
	}
	if event.Visibility != VisibilityNormal && event.Visibility != VisibilitySensitive {
		return fmt.Errorf("%w: visibility must be normal or sensitive", ErrInvalidEvent)
	}
	if !validSingleLine(event.Title, 180, true) {
		return fmt.Errorf("%w: title is required, single-line UTF-8, and at most 180 bytes", ErrInvalidEvent)
	}
	if !validSingleLine(event.Summary, 600, false) {
		return fmt.Errorf("%w: summary must be single-line UTF-8 and at most 600 bytes", ErrInvalidEvent)
	}
	for _, value := range []struct {
		name  string
		value string
	}{
		{"node id", event.NodeID}, {"service id", event.ServiceID}, {"container id", event.ContainerID},
	} {
		if value.value != "" && !validIdentifier(value.value, 128) {
			return fmt.Errorf("%w: %s is invalid", ErrInvalidEvent, value.name)
		}
	}
	if !validSingleLine(event.Actor, 320, false) {
		return fmt.Errorf("%w: actor must be single-line UTF-8 and at most 320 bytes", ErrInvalidEvent)
	}
	if !event.OccurredAt.IsZero() && event.OccurredAt.Year() < 2000 {
		return fmt.Errorf("%w: occurred at is invalid", ErrInvalidEvent)
	}
	return nil
}

func NormalizeFilter(filter Filter) (Filter, error) {
	filter.Type = strings.TrimSpace(filter.Type)
	filter.NodeID = strings.TrimSpace(filter.NodeID)
	filter.ServiceID = strings.TrimSpace(filter.ServiceID)
	filter.From = filter.From.UTC()
	filter.To = filter.To.UTC()
	if !filter.From.IsZero() && !filter.To.IsZero() && !filter.From.Before(filter.To) {
		return Filter{}, fmt.Errorf("%w: from must be before to", ErrInvalidFilter)
	}
	if filter.Type != "" && !validType(filter.Type) {
		return Filter{}, fmt.Errorf("%w: type is invalid", ErrInvalidFilter)
	}
	if filter.NodeID != "" && !validIdentifier(filter.NodeID, 128) {
		return Filter{}, fmt.Errorf("%w: node id is invalid", ErrInvalidFilter)
	}
	if filter.ServiceID != "" && !validIdentifier(filter.ServiceID, 128) {
		return Filter{}, fmt.Errorf("%w: service id is invalid", ErrInvalidFilter)
	}
	if filter.Source != "" && filter.Source != SourceAutomatic && filter.Source != SourceManual {
		return Filter{}, fmt.Errorf("%w: source is invalid", ErrInvalidFilter)
	}
	if filter.Visibility != "" && filter.Visibility != VisibilityNormal && filter.Visibility != VisibilitySensitive {
		return Filter{}, fmt.Errorf("%w: visibility is invalid", ErrInvalidFilter)
	}
	if filter.Limit <= 0 {
		filter.Limit = DefaultListLimit
	}
	if filter.Limit > MaxListLimit {
		filter.Limit = MaxListLimit
	}
	return filter, nil
}

func isManualType(eventType string) bool {
	switch eventType {
	case EventDeploy, EventMaintenance, EventNote:
		return true
	default:
		return false
	}
}

func validType(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validIdentifier(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '.' || char == '_' || char == '-' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func validSingleLine(value string, maximum int, required bool) bool {
	if required && value == "" {
		return false
	}
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char == '\n' || char == '\r' || unicode.IsControl(char) {
			return false
		}
	}
	return true
}
