package dashboardconfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/bnhminh1010/homelab-dashboard/internal/alerts"
	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
	"github.com/bnhminh1010/homelab-dashboard/internal/slo"
	"github.com/bnhminh1010/homelab-dashboard/internal/topology"
)

type Service struct {
	repository Repository
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("dashboard config repository is required")
	}
	return &Service{repository: repository}, nil
}

// Export produces a deterministic, human-readable document. Only the
// sanitized DTO fields in Document are encoded.
func (manager *Service) Export(ctx context.Context) ([]byte, error) {
	snapshot, err := manager.repository.LoadDashboardConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load dashboard config: %w", err)
	}
	document := documentFromSnapshot(snapshot)
	if err := validateDocument(document); err != nil {
		return nil, fmt.Errorf("validate exported dashboard config: %w", err)
	}
	if err := validatePortableReferences(snapshot, document, ImportMerge); err != nil {
		return nil, fmt.Errorf("validate exported dashboard config references: %w", err)
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode dashboard config: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > MaxDocumentBytes {
		return nil, ErrDocumentTooLarge
	}
	return encoded, nil
}

// Preview validates a document and reports exactly what the chosen mode would
// mutate. An empty mode always means merge; replace must be explicit.
func (manager *Service) Preview(ctx context.Context, raw []byte, mode ImportMode) (Preview, error) {
	document, err := Decode(raw)
	if err != nil {
		return Preview{}, err
	}
	mode, err = normalizeMode(mode)
	if err != nil {
		return Preview{}, err
	}
	current, err := manager.repository.LoadDashboardConfig(ctx)
	if err != nil {
		return Preview{}, fmt.Errorf("load current dashboard config: %w", err)
	}
	revision, err := Revision(current)
	if err != nil {
		return Preview{}, err
	}
	document, warnings := normalizeDefaultNode(document, current)
	document, skippedTopology, topologyWarnings := skipUnenrolledTopologyDependencies(document, current)
	document = preserveLegacyV1Sections(document, current)
	if err := validateServiceCapacity(current, document, mode); err != nil {
		return Preview{}, err
	}
	if err := validatePortableReferences(current, document, mode); err != nil {
		return Preview{}, err
	}
	previewToken, err := previewRevision(revision, document, mode)
	if err != nil {
		return Preview{}, err
	}
	preview := buildPreview(documentFromSnapshot(current), document, mode, previewToken)
	preview.Warnings = append(preview.Warnings, warnings...)
	appendSkippedTopologyPreview(&preview, skippedTopology, topologyWarnings)
	return preview, nil
}

// Apply performs the same validation as Preview, then delegates one atomic
// transaction to the repository. Actor is mandatory so the transaction can
// write an audit event without a second, non-atomic operation.
func (manager *Service) Apply(ctx context.Context, raw []byte, mode ImportMode, actor, expectedRevision string) (ApplyResult, error) {
	document, err := Decode(raw)
	if err != nil {
		return ApplyResult{}, err
	}
	mode, err = normalizeMode(mode)
	if err != nil {
		return ApplyResult{}, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" || len(actor) > 320 || strings.ContainsAny(actor, "\x00\r\n") {
		return ApplyResult{}, invalid("actor", "must be non-empty, single-line, and at most 320 bytes")
	}
	expectedRevision = strings.TrimSpace(expectedRevision)
	if expectedRevision == "" {
		return ApplyResult{}, ErrRevisionRequired
	}
	current, err := manager.repository.LoadDashboardConfig(ctx)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("load current dashboard config: %w", err)
	}
	currentRevision, err := Revision(current)
	if err != nil {
		return ApplyResult{}, err
	}
	document, warnings := normalizeDefaultNode(document, current)
	document, skippedTopology, topologyWarnings := skipUnenrolledTopologyDependencies(document, current)
	document = preserveLegacyV1Sections(document, current)
	if err := validateServiceCapacity(current, document, mode); err != nil {
		return ApplyResult{}, err
	}
	if err := validatePortableReferences(current, document, mode); err != nil {
		return ApplyResult{}, err
	}
	// expectedRevision is an opaque preview token, not the raw current-state
	// revision. Validate it after normalizing the exact import document.
	previewToken, err := previewRevision(currentRevision, document, mode)
	if err != nil {
		return ApplyResult{}, err
	}
	if previewToken != expectedRevision {
		return ApplyResult{}, ErrRevisionConflict
	}
	preview := buildPreview(documentFromSnapshot(current), document, mode, previewToken)
	preview.Warnings = append(preview.Warnings, warnings...)
	appendSkippedTopologyPreview(&preview, skippedTopology, topologyWarnings)
	if err := manager.repository.ApplyDashboardConfig(ctx, snapshotFromDocument(document), mode, actor, currentRevision); err != nil {
		return ApplyResult{}, fmt.Errorf("apply dashboard config: %w", err)
	}
	return ApplyResult{Preview: preview}, nil
}

func validateServiceCapacity(current Snapshot, incoming Document, mode ImportMode) error {
	if mode == ImportReplace {
		return nil // The document-level validator already bounds this list.
	}
	serviceIDs := make(map[string]struct{}, len(current.Services)+len(incoming.Services))
	for _, service := range current.Services {
		serviceIDs[service.ID] = struct{}{}
	}
	for _, service := range incoming.Services {
		serviceIDs[service.ID] = struct{}{}
	}
	if len(serviceIDs) > maxServices {
		return invalid("services", fmt.Sprintf("merge would exceed the %d-service dashboard limit", maxServices))
	}
	return nil
}

// preserveLegacyV1Sections keeps a v1 import scoped to the configuration that
// schema knew about. In particular, a v1 replace must not silently erase
// SLO policies or manual topology authored after the dashboard was upgraded.
// Workspace and overview widget layout fields are also preserved when absent
// from an older v1 or v2 document so importing an existing backup cannot reset
// a newer sidebar or overview arrangement.
func preserveLegacyV1Sections(document Document, current Snapshot) Document {
	currentDocument := documentFromSnapshot(current)
	if document.legacyV1 {
		document.SLOPolicies = currentDocument.SLOPolicies
		document.Dependencies = currentDocument.Dependencies
	}
	if document.UIPreferences.HiddenWorkspaces == nil {
		document.UIPreferences.HiddenWorkspaces = append([]string(nil), currentDocument.UIPreferences.HiddenWorkspaces...)
	}
	if document.UIPreferences.WorkspaceOrder == nil {
		document.UIPreferences.WorkspaceOrder = append([]string(nil), currentDocument.UIPreferences.WorkspaceOrder...)
	}
	if document.UIPreferences.HiddenOverviewWidgets == nil {
		document.UIPreferences.HiddenOverviewWidgets = append([]string(nil), currentDocument.UIPreferences.HiddenOverviewWidgets...)
	}
	if document.UIPreferences.OverviewWidgetSizes == nil {
		document.UIPreferences.OverviewWidgetSizes = cloneOverviewWidgetSizes(currentDocument.UIPreferences.OverviewWidgetSizes)
	}
	if document.legacyContent {
		document.LaunchpadBookmarks = append([]model.LaunchpadBookmark(nil), current.LaunchpadBookmarks...)
	}
	return document
}

func cloneOverviewWidgetSizes(sizes map[string]string) map[string]string {
	if sizes == nil {
		return nil
	}
	clone := make(map[string]string, len(sizes))
	for widget, size := range sizes {
		clone[widget] = size
	}
	return clone
}

// skipUnenrolledTopologyDependencies prevents an import from creating an edge
// for a node the dashboard cannot currently select. In replace mode, an
// existing edge for that unavailable node is retained so an export/import
// round-trip never deletes configuration that was merely unavailable.
func skipUnenrolledTopologyDependencies(document Document, current Snapshot) (Document, []Change, []string) {
	if document.legacyV1 || len(document.Dependencies) == 0 {
		return document, nil, nil
	}
	available := map[string]struct{}{"local": {}}
	for _, node := range current.Nodes {
		available[node.ID] = struct{}{}
	}
	currentByID := make(map[string]DependencyConfig, len(current.Dependencies))
	for _, dependency := range documentFromSnapshot(current).Dependencies {
		currentByID[topologyDependencyID(dependency)] = dependency
	}
	kept := make([]DependencyConfig, 0, len(document.Dependencies))
	skipped := make([]Change, 0)
	warnings := make([]string, 0)
	for _, dependency := range document.Dependencies {
		if _, enrolled := available[dependency.NodeID]; enrolled {
			kept = append(kept, dependency)
			continue
		}
		id := topologyDependencyID(dependency)
		if existing, exists := currentByID[id]; exists {
			kept = append(kept, existing)
			warnings = append(warnings, fmt.Sprintf(
				"Topology edge %s was retained without changes because node %s is not actively enrolled.", id, dependency.NodeID,
			))
			continue
		}
		skipped = append(skipped, Change{
			Section: "topologyDependencies", ID: id, Action: ChangeSkipped,
			Reason: "node is not actively enrolled; topology edge was skipped",
		})
		warnings = append(warnings, fmt.Sprintf(
			"Topology edge %s was skipped because node %s is not actively enrolled.", id, dependency.NodeID,
		))
	}
	document.Dependencies = kept
	return document, skipped, warnings
}

// validatePortableReferences keeps an import from referring to a service that
// cannot exist after the selected merge/replace operation. It complements
// document-level syntax validation with the current catalog state.
func validatePortableReferences(current Snapshot, incoming Document, mode ImportMode) error {
	available := make(map[string]struct{}, len(current.Services)+len(incoming.Services))
	if mode == ImportMerge {
		for _, service := range current.Services {
			available[service.ID] = struct{}{}
		}
	}
	for _, service := range incoming.Services {
		available[service.ID] = struct{}{}
	}
	for index, policy := range incoming.SLOPolicies {
		if _, exists := available[policy.ServiceID]; !exists {
			return invalid(fmt.Sprintf("sloPolicies[%d].serviceId", index), "must reference a configured service")
		}
	}
	for index, dependency := range incoming.Dependencies {
		if _, exists := available[dependency.DependentServiceID]; !exists {
			return invalid(fmt.Sprintf("topologyDependencies[%d].dependentServiceId", index), "must reference a configured service")
		}
		if _, exists := available[dependency.DependencyServiceID]; !exists {
			return invalid(fmt.Sprintf("topologyDependencies[%d].dependencyServiceId", index), "must reference a configured service")
		}
	}
	return nil
}

// previewRevision binds an import preview to the current portable state, the
// normalized import document, and the selected mode. The result is an opaque
// token used as the preview ETag and must not be interpreted by clients.
func previewRevision(currentRevision string, document Document, mode ImportMode) (string, error) {
	canonicalDocument := documentFromSnapshot(snapshotFromDocument(document))
	encoded, err := json.Marshal(struct {
		Version         string     `json:"version"`
		CurrentRevision string     `json:"currentRevision"`
		Mode            ImportMode `json:"mode"`
		Document        Document   `json:"document"`
	}{
		Version:         "homelab-dashboard.config-preview/v1",
		CurrentRevision: currentRevision,
		Mode:            mode,
		Document:        canonicalDocument,
	})
	if err != nil {
		return "", fmt.Errorf("encode dashboard config preview revision: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest), nil
}

// Revision returns a deterministic digest of portable configuration only.
// Runtime state, secrets, timestamps, and audit data never influence it.
func Revision(snapshot Snapshot) (string, error) {
	encoded, err := json.Marshal(documentFromSnapshot(snapshot))
	if err != nil {
		return "", fmt.Errorf("encode dashboard config revision: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest), nil
}

func normalizeDefaultNode(document Document, current Snapshot) (Document, []string) {
	if document.UIPreferences.DefaultNodeID == "local" {
		return document, nil
	}
	for _, node := range current.Nodes {
		if node.ID == document.UIPreferences.DefaultNodeID {
			return document, nil
		}
	}
	missing := document.UIPreferences.DefaultNodeID
	document.UIPreferences.DefaultNodeID = "local"
	return document, []string{fmt.Sprintf(
		"Default node %s is not actively enrolled; the imported preference was reset to local.", missing,
	)}
}

// Decode enforces the 1 MiB limit, rejects unknown fields and trailing JSON,
// and validates every portable value. v1 documents are upgraded in memory to
// v3 with explicit empty new sections; callers always receive the current DTO.
func Decode(raw []byte) (Document, error) {
	if len(raw) > MaxDocumentBytes {
		return Document{}, ErrDocumentTooLarge
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return Document{}, invalid("", "document is empty")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("%w: decode JSON: %v", ErrInvalidDocument, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Document{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidDocument)
		}
		return Document{}, fmt.Errorf("%w: trailing JSON: %v", ErrInvalidDocument, err)
	}
	switch document.Version {
	case legacyDocumentVersion:
		// v1 had no SLO/topology sections. Keep its old required sections
		// strict, then represent the absent portable settings explicitly. A
		// mixed-version payload must not silently discard user configuration.
		if document.SLOPolicies != nil || document.Dependencies != nil {
			return Document{}, invalid("version", "v1 documents must not include v2 sections")
		}
		document.Version = DocumentVersion
		document.SLOPolicies = []SLOPolicyConfig{}
		document.Dependencies = []DependencyConfig{}
		document.legacyV1 = true
		document.legacyContent = true
	case previousDocumentVersion:
		document.Version = DocumentVersion
		document.legacyContent = true
	case DocumentVersion:
		// v3 validation below requires every portable section to be explicit.
	default:
		return Document{}, fmt.Errorf("%w: got %q", ErrUnsupportedVersion, document.Version)
	}
	if err := validateDocument(document); err != nil {
		return Document{}, err
	}
	return document, nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("duplicate object field %q", key)
			}
			keys[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func normalizeMode(mode ImportMode) (ImportMode, error) {
	if mode == "" {
		return ImportMerge, nil
	}
	if !mode.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidImportMode, mode)
	}
	return mode, nil
}

func documentFromSnapshot(snapshot Snapshot) Document {
	document := Document{
		Version: DocumentVersion, Services: make([]ServiceConfig, 0, len(snapshot.Services)),
		AlertRules:    make([]AlertRuleConfig, 0, len(snapshot.AlertRules)),
		SLOPolicies:   make([]SLOPolicyConfig, 0, len(snapshot.SLOPolicies)),
		Dependencies:  make([]DependencyConfig, 0, len(snapshot.Dependencies)),
		UIPreferences: NormalizeUIPreferences(snapshot.UIPreferences),
		Nodes:         make([]NodeMetadata, 0, len(snapshot.Nodes)),
	}
	for _, service := range snapshot.Services {
		document.Services = append(document.Services, ServiceConfig{
			ID: service.ID, Name: service.Name, Icon: service.Icon,
			DisplayURL: service.DisplayURL, ProbeURL: service.ProbeURL,
			Category: service.Category, Tags: append([]string(nil), service.Tags...),
		})
	}
	document.LaunchpadBookmarks = append([]model.LaunchpadBookmark(nil), snapshot.LaunchpadBookmarks...)
	for _, rule := range snapshot.AlertRules {
		document.AlertRules = append(document.AlertRules, alertRuleFromDomain(rule))
	}
	for _, policy := range snapshot.SLOPolicies {
		document.SLOPolicies = append(document.SLOPolicies, SLOPolicyConfig{
			ServiceID: policy.ServiceID, TargetPercent: policy.TargetPercent, WindowDays: policy.WindowDays,
		})
	}
	for _, dependency := range snapshot.Dependencies {
		document.Dependencies = append(document.Dependencies, DependencyConfig{
			NodeID: dependency.NodeID, DependentServiceID: dependency.DependentServiceID,
			DependencyServiceID: dependency.DependencyServiceID, Label: dependency.Label,
		})
	}
	for _, node := range snapshot.Nodes {
		document.Nodes = append(document.Nodes, NodeMetadata{
			ID: node.ID, DisplayName: node.DisplayName, Hostname: node.Hostname,
		})
	}
	sort.Slice(document.Services, func(i, j int) bool { return document.Services[i].ID < document.Services[j].ID })
	sort.Slice(document.AlertRules, func(i, j int) bool { return document.AlertRules[i].ID < document.AlertRules[j].ID })
	sort.Slice(document.SLOPolicies, func(i, j int) bool { return document.SLOPolicies[i].ServiceID < document.SLOPolicies[j].ServiceID })
	sort.Slice(document.Dependencies, func(i, j int) bool {
		left, right := document.Dependencies[i], document.Dependencies[j]
		if left.NodeID != right.NodeID {
			return left.NodeID < right.NodeID
		}
		if left.DependentServiceID != right.DependentServiceID {
			return left.DependentServiceID < right.DependentServiceID
		}
		return left.DependencyServiceID < right.DependencyServiceID
	})
	sort.Slice(document.Nodes, func(i, j int) bool { return document.Nodes[i].ID < document.Nodes[j].ID })
	return document
}

func snapshotFromDocument(document Document) Snapshot {
	snapshot := Snapshot{
		Services:      make([]model.Service, 0, len(document.Services)),
		AlertRules:    make([]alerts.AlertRule, 0, len(document.AlertRules)),
		SLOPolicies:   make([]slo.Policy, 0, len(document.SLOPolicies)),
		Dependencies:  make([]topology.Dependency, 0, len(document.Dependencies)),
		UIPreferences: NormalizeUIPreferences(document.UIPreferences),
		Nodes:         make([]nodes.Node, 0, len(document.Nodes)),
	}
	for _, service := range document.Services {
		snapshot.Services = append(snapshot.Services, model.Service{
			ID: service.ID, Name: service.Name, Icon: service.Icon,
			DisplayURL: service.DisplayURL, ProbeURL: service.ProbeURL,
			Category: service.Category, Tags: append([]string(nil), service.Tags...),
			Status: model.ServiceStatusUnknown,
		})
	}
	snapshot.LaunchpadBookmarks = append([]model.LaunchpadBookmark(nil), document.LaunchpadBookmarks...)
	for _, rule := range document.AlertRules {
		snapshot.AlertRules = append(snapshot.AlertRules, rule.domain())
	}
	for _, policy := range document.SLOPolicies {
		snapshot.SLOPolicies = append(snapshot.SLOPolicies, slo.Policy{
			ServiceID: policy.ServiceID, TargetPercent: policy.TargetPercent,
			WindowDays: policy.WindowDays, Configured: true,
		})
	}
	for _, dependency := range document.Dependencies {
		snapshot.Dependencies = append(snapshot.Dependencies, topology.Dependency{
			NodeID: dependency.NodeID, DependentServiceID: dependency.DependentServiceID,
			DependencyServiceID: dependency.DependencyServiceID, Label: dependency.Label,
		})
	}
	for _, node := range document.Nodes {
		snapshot.Nodes = append(snapshot.Nodes, nodes.Node{
			ID: node.ID, DisplayName: node.DisplayName, Hostname: node.Hostname,
		})
	}
	return snapshot
}

func buildPreview(current, incoming Document, mode ImportMode, revision string) Preview {
	preview := Preview{
		Version:  DocumentVersion,
		Mode:     mode,
		Revision: revision,
		Summary: map[string]ChangeCounts{
			"services": {}, "alertRules": {}, "sloPolicies": {}, "topologyDependencies": {},
			"uiPreferences": {}, "nodes": {},
		},
		Changes: make([]Change, 0),
	}
	preview.Changes = append(preview.Changes, diffValues("services", current.Services, incoming.Services, mode)...)
	preview.Changes = append(preview.Changes, diffValues("alertRules", current.AlertRules, incoming.AlertRules, mode)...)
	preview.Changes = append(preview.Changes, diffValues("sloPolicies", current.SLOPolicies, incoming.SLOPolicies, mode)...)
	preview.Changes = append(preview.Changes, diffValues("topologyDependencies", current.Dependencies, incoming.Dependencies, mode)...)
	uiAction := ChangeUnchanged
	if !EqualUIPreferences(current.UIPreferences, incoming.UIPreferences) {
		uiAction = ChangeUpdate
	}
	preview.Changes = append(preview.Changes, Change{Section: "uiPreferences", ID: "preferences", Action: uiAction})

	currentNodes := indexValues(current.Nodes)
	for _, node := range incoming.Nodes {
		existing, exists := currentNodes[node.ID]
		switch {
		case !exists:
			preview.Changes = append(preview.Changes, Change{
				Section: "nodes", ID: node.ID, Action: ChangeSkipped,
				Reason: "node is not enrolled; credentials are never imported",
			})
			preview.Warnings = append(preview.Warnings,
				fmt.Sprintf("Node %s is not enrolled and its display metadata will be skipped.", node.ID))
		case reflect.DeepEqual(existing, node):
			preview.Changes = append(preview.Changes, Change{Section: "nodes", ID: node.ID, Action: ChangeUnchanged})
		default:
			preview.Changes = append(preview.Changes, Change{Section: "nodes", ID: node.ID, Action: ChangeUpdate})
		}
	}
	for _, node := range current.Nodes {
		if _, included := indexValues(incoming.Nodes)[node.ID]; !included {
			preview.Changes = append(preview.Changes, Change{
				Section: "nodes", ID: node.ID, Action: ChangeUnchanged,
				Reason: "node registrations are retained in every import mode",
			})
		}
	}
	if mode == ImportReplace {
		preview.Warnings = append(preview.Warnings,
			"Replace deletes services, alert rules, SLO policies, and topology dependencies omitted from the document; node registrations are always retained.")
	}
	sort.SliceStable(preview.Changes, func(i, j int) bool {
		if preview.Changes[i].Section == preview.Changes[j].Section {
			return preview.Changes[i].ID < preview.Changes[j].ID
		}
		return sectionOrder(preview.Changes[i].Section) < sectionOrder(preview.Changes[j].Section)
	})
	for _, change := range preview.Changes {
		counts := preview.Summary[change.Section]
		switch change.Action {
		case ChangeAdd:
			counts.Added++
		case ChangeUpdate:
			counts.Updated++
		case ChangeDelete:
			counts.Deleted++
		case ChangeUnchanged:
			counts.Unchanged++
		case ChangeSkipped:
			counts.Skipped++
		}
		preview.Summary[change.Section] = counts
	}
	return preview
}

func appendSkippedTopologyPreview(preview *Preview, skipped []Change, warnings []string) {
	if len(skipped) == 0 {
		return
	}
	preview.Changes = append(preview.Changes, skipped...)
	counts := preview.Summary["topologyDependencies"]
	counts.Skipped += len(skipped)
	preview.Summary["topologyDependencies"] = counts
	preview.Warnings = append(preview.Warnings, warnings...)
	sort.SliceStable(preview.Changes, func(i, j int) bool {
		if preview.Changes[i].Section == preview.Changes[j].Section {
			return preview.Changes[i].ID < preview.Changes[j].ID
		}
		return sectionOrder(preview.Changes[i].Section) < sectionOrder(preview.Changes[j].Section)
	})
}

type identifiable interface {
	ServiceConfig | AlertRuleConfig | SLOPolicyConfig | DependencyConfig | NodeMetadata
}

func diffValues[T identifiable](section string, current, incoming []T, mode ImportMode) []Change {
	currentByID := indexValues(current)
	incomingByID := indexValues(incoming)
	changes := make([]Change, 0, len(current)+len(incoming))
	for _, value := range incoming {
		id := valueID(value)
		existing, exists := currentByID[id]
		action := ChangeAdd
		if exists {
			action = ChangeUpdate
			if reflect.DeepEqual(existing, value) {
				action = ChangeUnchanged
			}
		}
		changes = append(changes, Change{Section: section, ID: id, Action: action})
	}
	for _, value := range current {
		id := valueID(value)
		if _, exists := incomingByID[id]; exists {
			continue
		}
		action := ChangeUnchanged
		if mode == ImportReplace {
			action = ChangeDelete
		}
		changes = append(changes, Change{Section: section, ID: id, Action: action})
	}
	return changes
}

func indexValues[T identifiable](values []T) map[string]T {
	result := make(map[string]T, len(values))
	for _, value := range values {
		result[valueID(value)] = value
	}
	return result
}

func valueID[T identifiable](value T) string {
	switch typed := any(value).(type) {
	case ServiceConfig:
		return typed.ID
	case AlertRuleConfig:
		return typed.ID
	case SLOPolicyConfig:
		return typed.ServiceID
	case DependencyConfig:
		return topologyDependencyID(typed)
	case NodeMetadata:
		return typed.ID
	default:
		panic("unsupported dashboard config value")
	}
}

func topologyDependencyID(dependency DependencyConfig) string {
	return dependency.NodeID + "/" + dependency.DependentServiceID + "->" + dependency.DependencyServiceID
}

func sectionOrder(section string) int {
	switch section {
	case "services":
		return 0
	case "alertRules":
		return 1
	case "sloPolicies":
		return 2
	case "topologyDependencies":
		return 3
	case "uiPreferences":
		return 4
	case "nodes":
		return 5
	default:
		return 6
	}
}
