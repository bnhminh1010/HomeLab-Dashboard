// Package dashboardconfig owns the portable, versioned dashboard configuration
// document. Its DTOs intentionally contain configuration only: runtime state,
// history, audit data, sessions, credentials, and notification secrets have no
// representation in this package and therefore cannot be exported accidentally.
package dashboardconfig

import (
	"context"
	"errors"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
	"github.com/bnhminh1010/homelab-dashboard/internal/slo"
	"github.com/bnhminh1010/homelab-dashboard/internal/topology"
)

const (
	// DocumentVersion is emitted for every new export. Decode upgrades the
	// previous portable schema so existing v1 backup files remain importable.
	DocumentVersion       = "homelab-dashboard.config/v2"
	legacyDocumentVersion = "homelab-dashboard.config/v1"
	MaxDocumentBytes      = 1 << 20
)

var (
	ErrDocumentTooLarge   = errors.New("dashboard config document exceeds 1 MiB")
	ErrInvalidDocument    = errors.New("dashboard config document is invalid")
	ErrUnsupportedVersion = errors.New("dashboard config version is unsupported")
	ErrInvalidImportMode  = errors.New("dashboard config import mode is invalid")
	ErrRevisionRequired   = errors.New("dashboard config preview revision is required")
	ErrRevisionConflict   = errors.New("dashboard config, import payload, or mode changed after preview")
)

type ImportMode string

const (
	ImportMerge   ImportMode = "merge"
	ImportReplace ImportMode = "replace"
)

func (mode ImportMode) Valid() bool {
	return mode == ImportMerge || mode == ImportReplace
}

// Document is the complete public schema. Do not add runtime or secret-bearing
// fields here. Adding a field is a schema change and must be reviewed as such.
type Document struct {
	Version       string             `json:"version"`
	Services      []ServiceConfig    `json:"services"`
	AlertRules    []AlertRuleConfig  `json:"alertRules"`
	SLOPolicies   []SLOPolicyConfig  `json:"sloPolicies"`
	Dependencies  []DependencyConfig `json:"topologyDependencies"`
	UIPreferences UIPreferences      `json:"uiPreferences"`
	Nodes         []NodeMetadata     `json:"nodes"`

	// legacyV1 is set only by Decode. It lets import preserve portable sections
	// introduced after v1 instead of treating their absence as a destructive
	// replace request. It is deliberately not part of the JSON schema.
	legacyV1 bool
}

type ServiceConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Icon       string `json:"icon,omitempty"`
	DisplayURL string `json:"displayUrl"`
	ProbeURL   string `json:"probeUrl,omitempty"`
}

type AlertRuleConfig struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	ResourceType     string          `json:"resourceType"`
	NodeSelector     string          `json:"nodeSelector"`
	ResourceSelector string          `json:"resourceSelector"`
	Metric           string          `json:"metric"`
	Operator         alerts.Operator `json:"operator"`
	Threshold        float64         `json:"threshold"`
	ForMilliseconds  int64           `json:"forMs"`
	Severity         alerts.Severity `json:"severity"`
	CooldownMS       int64           `json:"cooldownMs"`
	Enabled          bool            `json:"enabled"`
}

// SLOPolicyConfig includes only durable, non-secret target settings. Runtime
// availability, remaining error budget, and observation timestamps belong to
// history and therefore are never exported with dashboard configuration.
type SLOPolicyConfig struct {
	ServiceID     string  `json:"serviceId"`
	TargetPercent float64 `json:"targetPercent"`
	WindowDays    int     `json:"windowDays"`
}

// DependencyConfig is one manually curated service-to-service edge. IDs and
// timestamps are storage/runtime details, so portable topology remains stable
// across imports into another dashboard database.
type DependencyConfig struct {
	NodeID              string `json:"nodeId"`
	DependentServiceID  string `json:"dependentServiceId"`
	DependencyServiceID string `json:"dependencyServiceId"`
	Label               string `json:"label,omitempty"`
}

type UIPreferences struct {
	TerminalHeight    int    `json:"terminalHeight"`
	TerminalCollapsed bool   `json:"terminalCollapsed"`
	HistoryRange      string `json:"historyRange"`
	DefaultNodeID     string `json:"defaultNodeId"`
}

func DefaultUIPreferences() UIPreferences {
	return UIPreferences{
		TerminalHeight: 200,
		HistoryRange:   "24h",
		DefaultNodeID:  "local",
	}
}

// NodeMetadata deliberately omits the credential hash, enrollment tokens,
// liveness timestamps, and revocation state. Import only changes display data
// for an already enrolled node; it never creates or authenticates a node.
type NodeMetadata struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Hostname    string `json:"hostname"`
}

// Snapshot is the repository boundary. It uses existing domain types on the
// trusted side and is projected into the sanitized Document before encoding.
type Snapshot struct {
	Services      []model.Service
	AlertRules    []alerts.AlertRule
	SLOPolicies   []slo.Policy
	Dependencies  []topology.Dependency
	UIPreferences UIPreferences
	Nodes         []nodes.Node
}

type Repository interface {
	LoadDashboardConfig(context.Context) (Snapshot, error)
	ApplyDashboardConfig(context.Context, Snapshot, ImportMode, string, string) error
}

type ChangeAction string

const (
	ChangeAdd       ChangeAction = "add"
	ChangeUpdate    ChangeAction = "update"
	ChangeDelete    ChangeAction = "delete"
	ChangeUnchanged ChangeAction = "unchanged"
	ChangeSkipped   ChangeAction = "skipped"
)

type Change struct {
	Section string       `json:"section"`
	ID      string       `json:"id"`
	Action  ChangeAction `json:"action"`
	Reason  string       `json:"reason,omitempty"`
}

type ChangeCounts struct {
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Deleted   int `json:"deleted"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
}

type Preview struct {
	Version  string                  `json:"version"`
	Mode     ImportMode              `json:"mode"`
	Revision string                  `json:"revision"` // Opaque token bound to current state, payload, and mode.
	Summary  map[string]ChangeCounts `json:"summary"`
	Changes  []Change                `json:"changes"`
	Warnings []string                `json:"warnings,omitempty"`
}

type ApplyResult struct {
	Preview Preview `json:"preview"`
}

func alertRuleFromDomain(rule alerts.AlertRule) AlertRuleConfig {
	return AlertRuleConfig{
		ID: rule.ID, Name: rule.Name, ResourceType: rule.ResourceType,
		NodeSelector: rule.NodeSelector, ResourceSelector: rule.ResourceSelector,
		Metric: rule.Metric, Operator: rule.Operator, Threshold: rule.Threshold,
		ForMilliseconds: rule.For.Milliseconds(), Severity: rule.Severity,
		CooldownMS: rule.Cooldown.Milliseconds(), Enabled: rule.Enabled,
	}
}

func (rule AlertRuleConfig) domain() alerts.AlertRule {
	return alerts.AlertRule{
		ID: rule.ID, Name: rule.Name, ResourceType: rule.ResourceType,
		NodeSelector: rule.NodeSelector, ResourceSelector: rule.ResourceSelector,
		Metric: rule.Metric, Operator: rule.Operator, Threshold: rule.Threshold,
		For:      time.Duration(rule.ForMilliseconds) * time.Millisecond,
		Severity: rule.Severity, Cooldown: time.Duration(rule.CooldownMS) * time.Millisecond,
		Enabled: rule.Enabled,
	}
}
