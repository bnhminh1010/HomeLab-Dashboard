package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/model"
)

func TestMarshalSnapshotIdentifiesEveryTruncatedSource(t *testing.T) {
	base := model.SnapshotEnvelope{
		Version: 1, Type: "metrics.snapshot", Sequence: 1, CollectedAt: time.Now().UTC(),
		Data: model.SnapshotData{System: model.SystemStats{Hostname: "test-host"}},
	}
	minimal, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	limit := len(minimal) + 200

	tests := []struct {
		name   string
		source string
		add    func(*model.SnapshotEnvelope)
	}{
		{
			name: "containers", source: "containers",
			add: func(snapshot *model.SnapshotEnvelope) {
				snapshot.Data.Containers = []model.Container{{ID: "container-1", Name: strings.Repeat("c", 800)}}
			},
		},
		{
			name: "services", source: "services",
			add: func(snapshot *model.SnapshotEnvelope) {
				snapshot.Data.Services = []model.Service{{ID: "service-1", Name: strings.Repeat("s", 800)}}
			},
		},
		{
			name: "alerts", source: "alerts",
			add: func(snapshot *model.SnapshotEnvelope) {
				snapshot.Data.Alerts = []model.Alert{{ID: "alert-1", Message: strings.Repeat("a", 800)}}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			test.add(&snapshot)
			payload, err := marshalSnapshot(snapshot, limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(payload) > limit {
				t.Fatalf("payload size = %d, limit = %d", len(payload), limit)
			}
			var decoded model.SnapshotEnvelope
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatal(err)
			}
			if !decoded.Truncated || len(decoded.TruncatedSources) != 1 || decoded.TruncatedSources[0] != test.source {
				t.Fatalf("truncation metadata = %#v", decoded)
			}
		})
	}
}
