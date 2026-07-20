package dashboardconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/alerts"
	"github.com/binhminh/HomeLab-Minh/internal/model"
	"github.com/binhminh/HomeLab-Minh/internal/nodes"
)

func TestExportUsesSanitizedVersionedSchema(t *testing.T) {
	repository := &fakeRepository{
		secret:   "node-credential-and-ntfy-token-must-never-leak",
		snapshot: testSnapshot(),
	}
	manager, err := NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manager.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(repository.secret)) || bytes.Contains(raw, []byte("credentialHash")) ||
		bytes.Contains(raw, []byte("ntfy")) || bytes.Contains(raw, []byte("lastSeenAt")) {
		t.Fatalf("export leaked a forbidden field or value: %s", raw)
	}
	document, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != DocumentVersion || len(document.Services) != 2 ||
		len(document.AlertRules) != 1 || len(document.Nodes) != 1 {
		t.Fatalf("unexpected exported document: %+v", document)
	}
	if document.Services[0].ID != "svc_keep" || document.Services[1].ID != "svc_old" {
		t.Fatalf("export is not deterministic by id: %+v", document.Services)
	}
}

func TestDecodeEnforcesSizeSchemaAndStrictFields(t *testing.T) {
	oversized := bytes.Repeat([]byte{'x'}, MaxDocumentBytes+1)
	if _, err := Decode(oversized); !errors.Is(err, ErrDocumentTooLarge) {
		t.Fatalf("oversized document error = %v", err)
	}
	base := testDocument()
	base.Version = "homelab-dashboard.config/v2"
	if _, err := Decode(mustJSON(t, base)); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("unsupported version error = %v", err)
	}
	unknown := strings.Replace(string(mustJSON(t, testDocument())),
		`"version":"homelab-dashboard.config/v1"`,
		`"version":"homelab-dashboard.config/v1","ntfyToken":"secret"`, 1)
	if _, err := Decode([]byte(unknown)); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("unknown secret field error = %v", err)
	}
	duplicate := strings.Replace(string(mustJSON(t, testDocument())),
		`"version":"homelab-dashboard.config/v1"`,
		`"version":"homelab-dashboard.config/v1","version":"homelab-dashboard.config/v1"`, 1)
	if _, err := Decode([]byte(duplicate)); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("duplicate field error = %v", err)
	}
	missingSections := []byte(`{"version":"homelab-dashboard.config/v1","uiPreferences":{"terminalHeight":200,"terminalCollapsed":false,"historyRange":"24h","defaultNodeId":"local"}}`)
	if _, err := Decode(missingSections); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("missing section error = %v", err)
	}
	trailing := append(mustJSON(t, testDocument()), []byte(` {}`)...)
	if _, err := Decode(trailing); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("trailing JSON error = %v", err)
	}
}

func TestExportHonorsEncodedSizeCap(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Services = make([]model.Service, 0, maxServices)
	for index := 0; index < maxServices; index++ {
		snapshot.Services = append(snapshot.Services, model.Service{
			ID: fmt.Sprintf("svc_%04d", index), Name: fmt.Sprintf("Service %d", index),
			DisplayURL: "https://example.com/" + strings.Repeat("a", 1900),
		})
	}
	snapshot.AlertRules = make([]alerts.AlertRule, 0, maxRules)
	for index := 0; index < maxRules; index++ {
		rule := alerts.DefaultRules()[1]
		rule.ID = fmt.Sprintf("rule_%04d", index)
		rule.Name = strings.Repeat("n", 160)
		rule.NodeSelector = strings.Repeat("a", 256)
		rule.ResourceSelector = strings.Repeat("b", 256)
		snapshot.AlertRules = append(snapshot.AlertRules, rule)
	}
	manager, _ := NewService(&fakeRepository{snapshot: snapshot})
	if _, err := manager.Export(context.Background()); !errors.Is(err, ErrDocumentTooLarge) {
		t.Fatalf("large encoded export error = %v", err)
	}
}

func TestPreviewRejectsMergeBeyondServiceCapacity(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Services = make([]model.Service, 0, maxServices)
	for index := 0; index < maxServices; index++ {
		snapshot.Services = append(snapshot.Services, model.Service{
			ID: fmt.Sprintf("svc_%03d", index), Name: fmt.Sprintf("Service %d", index),
			DisplayURL: fmt.Sprintf("https://service-%d.example", index),
		})
	}
	manager, _ := NewService(&fakeRepository{snapshot: snapshot})
	document := testDocument()
	document.Services = []ServiceConfig{{ID: "svc_new", Name: "New", DisplayURL: "https://new.example"}}
	if _, err := manager.Preview(context.Background(), mustJSON(t, document), ImportMerge); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("over-capacity merge error = %v", err)
	}
	if _, err := manager.Preview(context.Background(), mustJSON(t, document), ImportReplace); err != nil {
		t.Fatalf("bounded replace was rejected: %v", err)
	}
}

func TestPreviewReportsMergeReplaceAndCredentialBoundNodes(t *testing.T) {
	repository := &fakeRepository{snapshot: testSnapshot()}
	manager, _ := NewService(repository)
	incoming := testDocument()
	incoming.Services = []ServiceConfig{
		{ID: "svc_keep", Name: "Keep Updated", DisplayURL: "https://keep.example"},
		{ID: "svc_new", Name: "New", DisplayURL: "https://new.example"},
	}
	incoming.AlertRules = []AlertRuleConfig{}
	incoming.Nodes = []NodeMetadata{
		{ID: "node_one", DisplayName: "Renamed", Hostname: "node-one"},
		{ID: "node_unknown", DisplayName: "Unknown", Hostname: "unknown"},
	}

	merge, err := manager.Preview(context.Background(), mustJSON(t, incoming), "")
	if err != nil {
		t.Fatal(err)
	}
	if merge.Mode != ImportMerge || merge.Summary["services"] != (ChangeCounts{Added: 1, Updated: 1, Unchanged: 1}) ||
		merge.Summary["alertRules"].Unchanged != 1 || merge.Summary["nodes"].Updated != 1 ||
		merge.Summary["nodes"].Skipped != 1 {
		t.Fatalf("unexpected merge preview: %+v", merge)
	}
	replace, err := manager.Preview(context.Background(), mustJSON(t, incoming), ImportReplace)
	if err != nil {
		t.Fatal(err)
	}
	if replace.Summary["services"] != (ChangeCounts{Added: 1, Updated: 1, Deleted: 1}) ||
		replace.Summary["alertRules"].Deleted != 1 || len(replace.Warnings) < 2 {
		t.Fatalf("unexpected replace preview: %+v", replace)
	}
}

func TestApplyValidatesBeforeRepositoryAndKeepsMergeDefault(t *testing.T) {
	repository := &fakeRepository{snapshot: testSnapshot()}
	manager, _ := NewService(repository)
	document := testDocument()
	preview, err := manager.Preview(context.Background(), mustJSON(t, document), "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Apply(context.Background(), mustJSON(t, document), "", "admin@example.com", preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if repository.applied != 1 || repository.mode != ImportMerge || repository.actor != "admin@example.com" ||
		result.Preview.Mode != ImportMerge {
		t.Fatalf("unexpected apply: repo=%+v result=%+v", repository, result)
	}
	if _, err := manager.Apply(context.Background(), []byte(`{}`), ImportReplace, "admin@example.com", preview.Revision); err == nil {
		t.Fatal("invalid document was applied")
	}
	if repository.applied != 1 {
		t.Fatalf("repository called after validation failure: %d", repository.applied)
	}
	if _, err := manager.Apply(context.Background(), mustJSON(t, document), "replace-ish", "admin@example.com", preview.Revision); !errors.Is(err, ErrInvalidImportMode) {
		t.Fatalf("invalid mode error = %v", err)
	}
}

func TestApplyRejectsStalePreviewAndNormalizesMissingDefaultNode(t *testing.T) {
	repository := &fakeRepository{snapshot: testSnapshot()}
	manager, _ := NewService(repository)
	document := testDocument()
	document.UIPreferences.DefaultNodeID = "node_missing"
	preview, err := manager.Preview(context.Background(), mustJSON(t, document), ImportReplace)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Revision == "" || len(preview.Warnings) == 0 {
		t.Fatalf("preview lacks revision/default-node warning: %+v", preview)
	}
	repository.snapshot.Services = append(repository.snapshot.Services, model.Service{
		ID: "svc_concurrent", Name: "Concurrent", DisplayURL: "https://concurrent.example",
	})
	if _, err := manager.Apply(context.Background(), mustJSON(t, document), ImportReplace, "admin@example.com", preview.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale preview error = %v", err)
	}
	if repository.applied != 0 {
		t.Fatalf("stale preview reached repository: %d", repository.applied)
	}
}

func TestApplyBindsPreviewToNormalizedPayloadAndMode(t *testing.T) {
	repository := &fakeRepository{snapshot: testSnapshot()}
	manager, _ := NewService(repository)
	document := testDocument()
	preview, err := manager.Preview(context.Background(), mustJSON(t, document), ImportMerge)
	if err != nil {
		t.Fatal(err)
	}

	changed := document
	changed.Services = append([]ServiceConfig(nil), document.Services...)
	changed.Services[0].Name = "Different payload"
	if _, err := manager.Apply(context.Background(), mustJSON(t, changed), ImportMerge,
		"admin@example.com", preview.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("changed payload error = %v", err)
	}
	if _, err := manager.Apply(context.Background(), mustJSON(t, document), ImportReplace,
		"admin@example.com", preview.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("changed mode error = %v", err)
	}
	if repository.applied != 0 {
		t.Fatalf("mismatched preview reached repository: %d", repository.applied)
	}

	// Array ordering is not configuration state, so equivalent decoded input
	// receives the same canonical payload binding.
	reordered := document
	reordered.Services = append([]ServiceConfig(nil), document.Services...)
	reordered.Services[0], reordered.Services[1] = reordered.Services[1], reordered.Services[0]
	if _, err := manager.Apply(context.Background(), mustJSON(t, reordered), ImportMerge,
		"admin@example.com", preview.Revision); err != nil {
		t.Fatalf("equivalent reordered payload: %v", err)
	}
	if repository.applied != 1 {
		t.Fatalf("canonical payload apply count = %d", repository.applied)
	}
}

func testSnapshot() Snapshot {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rule := alerts.DefaultRules()[0]
	rule.ID = "rule_cpu"
	return Snapshot{
		Services: []model.Service{
			{ID: "svc_old", Name: "Old", DisplayURL: "https://old.example", CreatedAt: now, UpdatedAt: now},
			{ID: "svc_keep", Name: "Keep", DisplayURL: "https://keep.example", CreatedAt: now, UpdatedAt: now},
		},
		AlertRules:    []alerts.AlertRule{rule},
		UIPreferences: DefaultUIPreferences(),
		Nodes: []nodes.Node{{
			ID: "node_one", DisplayName: "Node One", Hostname: "node-one", LastSeenAt: &now,
			CreatedAt: now, UpdatedAt: now,
		}},
	}
}

func testDocument() Document {
	return documentFromSnapshot(testSnapshot())
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type fakeRepository struct {
	snapshot Snapshot
	secret   string
	applied  int
	mode     ImportMode
	actor    string
}

func (repository *fakeRepository) LoadDashboardConfig(context.Context) (Snapshot, error) {
	return repository.snapshot, nil
}

func (repository *fakeRepository) ApplyDashboardConfig(_ context.Context, snapshot Snapshot, mode ImportMode, actor, expectedRevision string) error {
	currentRevision, err := Revision(repository.snapshot)
	if err != nil {
		return err
	}
	if currentRevision != expectedRevision {
		return ErrRevisionConflict
	}
	repository.applied++
	repository.snapshot = snapshot
	repository.mode = mode
	repository.actor = actor
	return nil
}
