package dashboardconfig

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/binhminh/HomeLab-Minh/internal/alerts"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/services"
)

const (
	maxServices = services.MaxServices
	maxRules    = 1000
	// The document contains remote node metadata only; the built-in local node
	// is the fifth node and is never imported or exported.
	maxNodes  = 4
	maxRuleMS = int64((30 * 24 * time.Hour) / time.Millisecond)
)

var allowedHistoryRanges = map[string]struct{}{
	"1h": {}, "6h": {}, "24h": {}, "7d": {}, "30d": {}, "90d": {},
}

type ValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (err *ValidationError) Error() string {
	if err.Path == "" {
		return fmt.Sprintf("%v: %s", ErrInvalidDocument, err.Message)
	}
	return fmt.Sprintf("%v at %s: %s", ErrInvalidDocument, err.Path, err.Message)
}

func (err *ValidationError) Unwrap() error { return ErrInvalidDocument }

func validateDocument(document Document) error {
	if document.Version != DocumentVersion {
		return fmt.Errorf("%w: got %q", ErrUnsupportedVersion, document.Version)
	}
	if document.Services == nil {
		return invalid("services", "field is required and must be an array")
	}
	if document.AlertRules == nil {
		return invalid("alertRules", "field is required and must be an array")
	}
	if document.Nodes == nil {
		return invalid("nodes", "field is required and must be an array")
	}
	if len(document.Services) > maxServices {
		return invalid("services", fmt.Sprintf("must contain at most %d entries", maxServices))
	}
	if len(document.AlertRules) > maxRules {
		return invalid("alertRules", "must contain at most 1000 entries")
	}
	if len(document.Nodes) > maxNodes {
		return invalid("nodes", "must contain at most 4 remote entries")
	}

	serviceIDs := make(map[string]struct{}, len(document.Services))
	for index, service := range document.Services {
		path := fmt.Sprintf("services[%d]", index)
		if !validID(service.ID, 128) {
			return invalid(path+".id", "must be a non-empty identifier of at most 128 bytes")
		}
		if _, duplicate := serviceIDs[service.ID]; duplicate {
			return invalid(path+".id", "duplicate service id")
		}
		serviceIDs[service.ID] = struct{}{}
		if service.Name != strings.TrimSpace(service.Name) || strings.ContainsAny(service.Name, "\x00\r\n") {
			return invalid(path+".name", "must be trimmed and single-line")
		}
		if service.Icon != strings.TrimSpace(service.Icon) || strings.ContainsAny(service.Icon, "\x00\r\n") {
			return invalid(path+".icon", "must be trimmed and single-line")
		}
		if err := services.ValidateInput(model.ServiceInput{
			Name: service.Name, Icon: service.Icon,
			DisplayURL: service.DisplayURL, ProbeURL: service.ProbeURL,
		}); err != nil {
			return invalid(path, err.Error())
		}
	}

	ruleIDs := make(map[string]struct{}, len(document.AlertRules))
	for index, rule := range document.AlertRules {
		path := fmt.Sprintf("alertRules[%d]", index)
		if rule.Name != strings.TrimSpace(rule.Name) || strings.ContainsAny(rule.Name, "\x00\r\n") {
			return invalid(path+".name", "must be trimmed and single-line")
		}
		if rule.NodeSelector == "" || rule.ResourceSelector == "" ||
			rule.NodeSelector != strings.TrimSpace(rule.NodeSelector) ||
			rule.ResourceSelector != strings.TrimSpace(rule.ResourceSelector) {
			return invalid(path, "nodeSelector and resourceSelector must be trimmed, explicit identifiers or *")
		}
		if rule.ForMilliseconds < 0 || rule.ForMilliseconds > maxRuleMS {
			return invalid(path+".forMs", "must be between 0 and 2592000000")
		}
		if rule.CooldownMS < 0 || rule.CooldownMS > maxRuleMS {
			return invalid(path+".cooldownMs", "must be between 0 and 2592000000")
		}
		if math.IsNaN(rule.Threshold) || math.IsInf(rule.Threshold, 0) {
			return invalid(path+".threshold", "must be finite")
		}
		if _, duplicate := ruleIDs[rule.ID]; duplicate {
			return invalid(path+".id", "duplicate alert rule id")
		}
		ruleIDs[rule.ID] = struct{}{}
		if err := alertsValidateRule(rule); err != nil {
			return invalid(path, err.Error())
		}
	}

	if err := validateUIPreferences(document.UIPreferences); err != nil {
		return err
	}
	nodeIDs := make(map[string]struct{}, len(document.Nodes))
	for index, node := range document.Nodes {
		path := fmt.Sprintf("nodes[%d]", index)
		if !validID(node.ID, 128) {
			return invalid(path+".id", "must be a non-empty identifier of at most 128 bytes")
		}
		if _, duplicate := nodeIDs[node.ID]; duplicate {
			return invalid(path+".id", "duplicate node id")
		}
		nodeIDs[node.ID] = struct{}{}
		if node.DisplayName != strings.TrimSpace(node.DisplayName) || strings.ContainsAny(node.DisplayName, "\x00\r\n") {
			return invalid(path+".displayName", "must be trimmed and single-line")
		}
		if count := utf8.RuneCountInString(node.DisplayName); count < 1 || count > 80 {
			return invalid(path+".displayName", "must contain between 1 and 80 characters")
		}
		hostname := strings.TrimSpace(node.Hostname)
		if hostname == "" || hostname != node.Hostname || len(hostname) > 255 || strings.ContainsAny(hostname, "\x00\r\n") {
			return invalid(path+".hostname", "must be non-empty, trimmed, single-line, and at most 255 bytes")
		}
	}
	return nil
}

func alertsValidateRule(rule AlertRuleConfig) error {
	return alerts.ValidateRule(rule.domain())
}

func validateUIPreferences(preferences UIPreferences) error {
	if preferences.TerminalHeight < 120 || preferences.TerminalHeight > 1000 {
		return invalid("uiPreferences.terminalHeight", "must be between 120 and 1000 pixels")
	}
	if _, ok := allowedHistoryRanges[preferences.HistoryRange]; !ok {
		return invalid("uiPreferences.historyRange", "must be one of 1h, 6h, 24h, 7d, 30d, or 90d")
	}
	if !validID(preferences.DefaultNodeID, 128) {
		return invalid("uiPreferences.defaultNodeId", "must be a non-empty identifier of at most 128 bytes")
	}
	return nil
}

// ValidateUIPreferences exposes the portable UI preference contract to the
// authenticated preferences API without exposing document internals.
func ValidateUIPreferences(preferences UIPreferences) error {
	return validateUIPreferences(preferences)
}

func validID(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("_.-", character) {
			continue
		}
		return false
	}
	return true
}

func invalid(path, message string) error {
	return &ValidationError{Path: path, Message: message}
}
