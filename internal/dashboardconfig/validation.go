package dashboardconfig

import (
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/services"
	"github.com/bnhminh1010/homelab-dashboard/internal/slo"
	"github.com/bnhminh1010/homelab-dashboard/internal/topology"
)

const (
	maxServices = services.MaxServices
	maxRules    = 1000
	// The document contains remote node metadata only; the built-in local node
	// is the fifth node and is never imported or exported.
	maxNodes  = 4
	maxRuleMS = int64((30 * 24 * time.Hour) / time.Millisecond)
)

var validLaunchpadIcons = map[string]struct{}{
	"link": {}, "server": {}, "storage": {}, "network": {},
}

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
	if document.SLOPolicies == nil {
		return invalid("sloPolicies", "field is required and must be an array")
	}
	if document.Dependencies == nil {
		return invalid("topologyDependencies", "field is required and must be an array")
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
	if len(document.SLOPolicies) > maxServices {
		return invalid("sloPolicies", fmt.Sprintf("must contain at most %d entries", maxServices))
	}
	if len(document.LaunchpadBookmarks) > 24 {
		return invalid("launchpadBookmarks", "must contain at most 24 entries")
	}
	bookmarkIDs := make(map[string]struct{}, len(document.LaunchpadBookmarks))
	for index, bookmark := range document.LaunchpadBookmarks {
		path := fmt.Sprintf("launchpadBookmarks[%d]", index)
		if !validID(bookmark.ID, 128) {
			return invalid(path+".id", "must be a non-empty identifier of at most 128 bytes")
		}
		if _, duplicate := bookmarkIDs[bookmark.ID]; duplicate {
			return invalid(path+".id", "duplicate bookmark id")
		}
		bookmarkIDs[bookmark.ID] = struct{}{}
		if bookmark.Title != strings.TrimSpace(bookmark.Title) || utf8.RuneCountInString(bookmark.Title) < 1 || utf8.RuneCountInString(bookmark.Title) > 80 || strings.ContainsAny(bookmark.Title, "\x00\r\n") {
			return invalid(path+".title", "must be trimmed, single-line, and at most 80 characters")
		}
		if bookmark.Tag != strings.TrimSpace(bookmark.Tag) || utf8.RuneCountInString(bookmark.Tag) > 16 || strings.ContainsAny(bookmark.Tag, "\x00\r\n") {
			return invalid(path+".tag", "must be trimmed, single-line, and at most 16 characters")
		}
		parsedURL, parseErr := url.ParseRequestURI(bookmark.URL)
		if parseErr != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return invalid(path+".url", "must use HTTP or HTTPS")
		}
		if _, allowed := validLaunchpadIcons[bookmark.Icon]; !allowed {
			return invalid(path+".icon", "must be one of link, server, storage, or network")
		}
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
			DisplayURL: service.DisplayURL, ProbeURL: service.ProbeURL, Category: service.Category, Tags: service.Tags,
		}); err != nil {
			return invalid(path, err.Error())
		}
	}

	policyServiceIDs := make(map[string]struct{}, len(document.SLOPolicies))
	for index, policy := range document.SLOPolicies {
		path := fmt.Sprintf("sloPolicies[%d]", index)
		if !validID(policy.ServiceID, 128) {
			return invalid(path+".serviceId", "must be a non-empty identifier of at most 128 bytes")
		}
		if _, duplicate := policyServiceIDs[policy.ServiceID]; duplicate {
			return invalid(path+".serviceId", "duplicate service id")
		}
		policyServiceIDs[policy.ServiceID] = struct{}{}
		if err := (slo.Policy{
			ServiceID: policy.ServiceID, TargetPercent: policy.TargetPercent, WindowDays: policy.WindowDays,
		}).Validate(); err != nil {
			return invalid(path, err.Error())
		}
	}

	dependencyIDs := make(map[string]struct{}, len(document.Dependencies))
	for index, dependency := range document.Dependencies {
		path := fmt.Sprintf("topologyDependencies[%d]", index)
		input := topology.DependencyInput{
			NodeID: dependency.NodeID, DependentServiceID: dependency.DependentServiceID,
			DependencyServiceID: dependency.DependencyServiceID, Label: dependency.Label,
		}
		if input != topology.NormalizeInput(input) {
			return invalid(path, "values must be trimmed")
		}
		if err := topology.ValidateInput(input); err != nil {
			return invalid(path, err.Error())
		}
		key := topologyDependencyID(dependency)
		if _, duplicate := dependencyIDs[key]; duplicate {
			return invalid(path, "duplicate dependency")
		}
		dependencyIDs[key] = struct{}{}
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
	validWorkspaces := make(map[string]struct{}, len(canonicalWorkspaceOrder))
	for _, workspace := range canonicalWorkspaceOrder {
		validWorkspaces[workspace] = struct{}{}
	}
	if preferences.HiddenWorkspaces != nil {
		hidden := make(map[string]struct{}, len(preferences.HiddenWorkspaces))
		for index, workspace := range preferences.HiddenWorkspaces {
			if _, ok := validWorkspaces[workspace]; !ok {
				return invalid(fmt.Sprintf("uiPreferences.hiddenWorkspaces[%d]", index), "must be a valid workspace id")
			}
			if workspace == WorkspaceOverview {
				return invalid(fmt.Sprintf("uiPreferences.hiddenWorkspaces[%d]", index), "overview cannot be hidden")
			}
			if _, duplicate := hidden[workspace]; duplicate {
				return invalid(fmt.Sprintf("uiPreferences.hiddenWorkspaces[%d]", index), "duplicate workspace id")
			}
			hidden[workspace] = struct{}{}
		}
	}
	if preferences.WorkspaceOrder != nil {
		if len(preferences.WorkspaceOrder) != len(canonicalWorkspaceOrder) {
			return invalid("uiPreferences.workspaceOrder", "must include every workspace exactly once")
		}
		ordered := make(map[string]struct{}, len(preferences.WorkspaceOrder))
		for index, workspace := range preferences.WorkspaceOrder {
			if _, ok := validWorkspaces[workspace]; !ok {
				return invalid(fmt.Sprintf("uiPreferences.workspaceOrder[%d]", index), "must be a valid workspace id")
			}
			if _, duplicate := ordered[workspace]; duplicate {
				return invalid(fmt.Sprintf("uiPreferences.workspaceOrder[%d]", index), "duplicate workspace id")
			}
			ordered[workspace] = struct{}{}
		}
	}
	validWidgets := make(map[string]struct{}, len(canonicalOverviewWidgetOrder))
	for _, widget := range canonicalOverviewWidgetOrder {
		validWidgets[widget] = struct{}{}
	}
	if preferences.HiddenOverviewWidgets != nil {
		hidden := make(map[string]struct{}, len(preferences.HiddenOverviewWidgets))
		for index, widget := range preferences.HiddenOverviewWidgets {
			if _, ok := validWidgets[widget]; !ok {
				return invalid(fmt.Sprintf("uiPreferences.hiddenOverviewWidgets[%d]", index), "must be a valid overview widget id")
			}
			if widget == OverviewWidgetAttention {
				return invalid(fmt.Sprintf("uiPreferences.hiddenOverviewWidgets[%d]", index), "overview attention cannot be hidden")
			}
			if _, duplicate := hidden[widget]; duplicate {
				return invalid(fmt.Sprintf("uiPreferences.hiddenOverviewWidgets[%d]", index), "duplicate overview widget id")
			}
			hidden[widget] = struct{}{}
		}
	}
	if preferences.OverviewWidgetSizes != nil {
		if len(preferences.OverviewWidgetSizes) != len(canonicalOverviewWidgetOrder) {
			return invalid("uiPreferences.overviewWidgetSizes", "must define a size for every overview widget")
		}
		for _, widget := range canonicalOverviewWidgetOrder {
			size, ok := preferences.OverviewWidgetSizes[widget]
			if !ok {
				return invalid("uiPreferences.overviewWidgetSizes", "must define a size for every overview widget")
			}
			switch size {
			case OverviewWidgetSizeSmall, OverviewWidgetSizeMedium, OverviewWidgetSizeFull:
			default:
				return invalid("uiPreferences.overviewWidgetSizes."+widget, "must be small, medium, or full")
			}
		}
		for widget := range preferences.OverviewWidgetSizes {
			if _, ok := validWidgets[widget]; !ok {
				return invalid("uiPreferences.overviewWidgetSizes."+widget, "must be a valid overview widget id")
			}
		}
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
