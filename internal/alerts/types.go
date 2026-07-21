package alerts

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	WildcardSelector    = "*"
	MaxDeliveryAttempts = 5
)

var (
	ErrInvalidRule     = errors.New("alerts: invalid rule")
	ErrInvalidSample   = errors.New("alerts: invalid sample")
	ErrAlertResolved   = errors.New("alerts: alert is already resolved")
	ErrInvalidSilence  = errors.New("alerts: silence must be 1h, 6h, or 24h")
	ErrStaleTransition = errors.New("alerts: stale transition")
)

type Operator string

const (
	OperatorGreaterThan        Operator = "gt"
	OperatorGreaterThanOrEqual Operator = "gte"
	OperatorLessThan           Operator = "lt"
	OperatorLessThanOrEqual    Operator = "lte"
	OperatorEqual              Operator = "eq"
	OperatorNotEqual           Operator = "neq"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type AlertStatus string

const (
	StatusPending  AlertStatus = "pending"
	StatusFiring   AlertStatus = "firing"
	StatusResolved AlertStatus = "resolved"
)

type EventType string

const (
	EventPending      EventType = "pending"
	EventFiring       EventType = "firing"
	EventResolved     EventType = "resolved"
	EventAcknowledged EventType = "acknowledged"
	EventSilenced     EventType = "silenced"
)

type DeliveryKind string

const (
	DeliveryFiring   DeliveryKind = "firing"
	DeliveryResolved DeliveryKind = "resolved"
)

type DeliveryStatus string

const (
	DeliveryPending    DeliveryStatus = "pending"
	DeliveryProcessing DeliveryStatus = "processing"
	DeliveryDelivered  DeliveryStatus = "delivered"
	DeliveryDead       DeliveryStatus = "dead"
	DeliverySuperseded DeliveryStatus = "superseded"
)

var AllowedSilenceDurations = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}

// AlertRule describes one threshold rule. Selectors support either an exact
// identifier or "*"; ResourceType is always exact.
type AlertRule struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	ResourceType     string        `json:"resourceType"`
	NodeSelector     string        `json:"nodeSelector"`
	ResourceSelector string        `json:"resourceSelector"`
	Metric           string        `json:"metric"`
	Operator         Operator      `json:"operator"`
	Threshold        float64       `json:"threshold"`
	For              time.Duration `json:"for"`
	Severity         Severity      `json:"severity"`
	Cooldown         time.Duration `json:"cooldown"`
	Enabled          bool          `json:"enabled"`
	CreatedAt        time.Time     `json:"createdAt"`
	UpdatedAt        time.Time     `json:"updatedAt"`
}

type Sample struct {
	NodeID       string    `json:"nodeId"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Metric       string    `json:"metric"`
	Value        float64   `json:"value"`
	ObservedAt   time.Time `json:"observedAt"`
}

type AlertKey struct {
	RuleID       string `json:"ruleId"`
	NodeID       string `json:"nodeId"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
}

type AlertState struct {
	AlertKey
	Status           AlertStatus `json:"status"`
	PendingSince     *time.Time  `json:"pendingSince,omitempty"`
	FiringSince      *time.Time  `json:"firingSince,omitempty"`
	ResolvedAt       *time.Time  `json:"resolvedAt,omitempty"`
	LastEvaluatedAt  time.Time   `json:"lastEvaluatedAt"`
	LastNotifiedAt   *time.Time  `json:"lastNotifiedAt,omitempty"`
	LastValue        float64     `json:"lastValue"`
	CleanEvaluations int         `json:"cleanEvaluations"`
	AcknowledgedAt   *time.Time  `json:"acknowledgedAt,omitempty"`
	AcknowledgedBy   string      `json:"acknowledgedBy,omitempty"`
	SilencedUntil    *time.Time  `json:"silencedUntil,omitempty"`
	SilencedBy       string      `json:"silencedBy,omitempty"`
	Revision         int64       `json:"-"`
}

type AlertEvent struct {
	ID int64 `json:"id"`
	AlertKey
	Type       EventType   `json:"type"`
	Status     AlertStatus `json:"status"`
	Severity   Severity    `json:"severity"`
	Value      float64     `json:"value"`
	Message    string      `json:"message"`
	Actor      string      `json:"actor,omitempty"`
	OccurredAt time.Time   `json:"occurredAt"`
}

type Delivery struct {
	ID int64 `json:"id"`
	AlertKey
	Kind          DeliveryKind   `json:"kind"`
	Severity      Severity       `json:"severity"`
	Title         string         `json:"title"`
	Message       string         `json:"message"`
	Status        DeliveryStatus `json:"status"`
	Attempts      int            `json:"attempts"`
	NextAttemptAt time.Time      `json:"nextAttemptAt"`
	LastError     string         `json:"lastError,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
	DeliveredAt   *time.Time     `json:"deliveredAt,omitempty"`
}

// Transition is persisted atomically so a firing/resolved event cannot be
// committed without its matching delivery queue item.
type Transition struct {
	State                 AlertState
	Event                 *AlertEvent
	Delivery              *Delivery
	ExpectedRevision      int64
	ExpectedRuleUpdatedAt time.Time
	IncidentStarted       bool
}

type StateFilter struct {
	RuleID     string
	NodeID     string
	Status     AlertStatus
	ActiveOnly bool
	Limit      int
}

type EventFilter struct {
	RuleID     string
	NodeID     string
	ResourceID string
	Type       EventType
	Limit      int
}

func NormalizeRule(rule AlertRule) AlertRule {
	rule.ID = strings.TrimSpace(rule.ID)
	rule.Name = strings.TrimSpace(rule.Name)
	rule.ResourceType = strings.TrimSpace(rule.ResourceType)
	rule.NodeSelector = strings.TrimSpace(rule.NodeSelector)
	rule.ResourceSelector = strings.TrimSpace(rule.ResourceSelector)
	rule.Metric = strings.TrimSpace(rule.Metric)
	if rule.NodeSelector == "" {
		rule.NodeSelector = WildcardSelector
	}
	if rule.ResourceSelector == "" {
		rule.ResourceSelector = WildcardSelector
	}
	return rule
}

func ValidateRule(rule AlertRule) error {
	rule = NormalizeRule(rule)
	if !validIdentifier(rule.ID, 128) {
		return fmt.Errorf("%w: invalid id", ErrInvalidRule)
	}
	if !validSingleLineText(rule.Name, 160) {
		return fmt.Errorf("%w: name is required, single-line UTF-8, and at most 160 bytes", ErrInvalidRule)
	}
	if !validIdentifier(rule.ResourceType, 64) {
		return fmt.Errorf("%w: invalid resource type", ErrInvalidRule)
	}
	if !validSelector(rule.NodeSelector) || !validSelector(rule.ResourceSelector) {
		return fmt.Errorf("%w: selectors must be an exact identifier or *", ErrInvalidRule)
	}
	if !validIdentifier(rule.Metric, 128) {
		return fmt.Errorf("%w: invalid metric", ErrInvalidRule)
	}
	if !SupportedMetric(rule.ResourceType, rule.Metric) {
		return fmt.Errorf("%w: metric %q is not emitted for resource type %q", ErrInvalidRule, rule.Metric, rule.ResourceType)
	}
	if !rule.Operator.Valid() {
		return fmt.Errorf("%w: invalid operator", ErrInvalidRule)
	}
	if math.IsNaN(rule.Threshold) || math.IsInf(rule.Threshold, 0) {
		return fmt.Errorf("%w: threshold must be finite", ErrInvalidRule)
	}
	if rule.For < 0 || rule.For > 30*24*time.Hour {
		return fmt.Errorf("%w: duration must be between 0 and 30 days", ErrInvalidRule)
	}
	if !rule.Severity.Valid() {
		return fmt.Errorf("%w: invalid severity", ErrInvalidRule)
	}
	if rule.Cooldown < 0 || rule.Cooldown > 30*24*time.Hour {
		return fmt.Errorf("%w: cooldown must be between 0 and 30 days", ErrInvalidRule)
	}
	return nil
}

// SupportedMetric is the backend source of truth for rule authoring and
// imports. Accepting a syntactically valid but never-emitted metric would leave
// an apparently enabled rule that can never transition.
func SupportedMetric(resourceType, metric string) bool {
	switch resourceType {
	case "node":
		return metric == MetricNodeOnline
	case "host":
		return metric == MetricCPUPercent || metric == MetricMemoryPercent || metric == MetricTemperatureCelsius
	case "disk":
		return metric == MetricDiskUsedPercent
	case "service":
		return metric == MetricServiceConsecutiveFailures
	case "container":
		return metric == MetricContainerHealthy || metric == MetricContainerRestarts
	case "backup":
		return metric == MetricBackupHealthy
	default:
		return false
	}
}

func ValidateSample(sample Sample) error {
	if !validSelectorValue(sample.NodeID, 256) || !validIdentifier(sample.ResourceType, 64) ||
		!validSelectorValue(sample.ResourceID, 256) || !validIdentifier(sample.Metric, 128) ||
		math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
		return ErrInvalidSample
	}
	return nil
}

func ValidateSilenceDuration(duration time.Duration) error {
	for _, allowed := range AllowedSilenceDurations {
		if duration == allowed {
			return nil
		}
	}
	return ErrInvalidSilence
}

func (o Operator) Valid() bool {
	switch o {
	case OperatorGreaterThan, OperatorGreaterThanOrEqual, OperatorLessThan,
		OperatorLessThanOrEqual, OperatorEqual, OperatorNotEqual:
		return true
	default:
		return false
	}
}

func (o Operator) Compare(value, threshold float64) bool {
	switch o {
	case OperatorGreaterThan:
		return value > threshold
	case OperatorGreaterThanOrEqual:
		return value >= threshold
	case OperatorLessThan:
		return value < threshold
	case OperatorLessThanOrEqual:
		return value <= threshold
	case OperatorEqual:
		return value == threshold
	case OperatorNotEqual:
		return value != threshold
	default:
		return false
	}
}

func (s Severity) Valid() bool {
	return s == SeverityInfo || s == SeverityWarning || s == SeverityCritical
}

func validSelector(value string) bool {
	return value == WildcardSelector || validSelectorValue(value, 256)
}

func validSelectorValue(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= max && !strings.ContainsAny(value, "\x00\r\n")
}

func validIdentifier(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func validSingleLineText(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}
