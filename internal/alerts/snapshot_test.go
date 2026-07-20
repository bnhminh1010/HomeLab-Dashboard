package alerts

import (
	"testing"
	"time"
)

func TestSnapshotAlertsProjectsOnlyActiveKnownRules(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	rules := []AlertRule{{ID: "cpu", Name: "CPU high", Severity: SeverityCritical}}
	states := []AlertState{
		{AlertKey: AlertKey{RuleID: "cpu", NodeID: "local", ResourceType: "host", ResourceID: "host"}, Status: StatusFiring, LastValue: 97, FiringSince: &now, LastEvaluatedAt: now},
		{AlertKey: AlertKey{RuleID: "missing", NodeID: "local", ResourceType: "host", ResourceID: "host"}, Status: StatusFiring, LastEvaluatedAt: now},
		{AlertKey: AlertKey{RuleID: "cpu", NodeID: "local", ResourceType: "host", ResourceID: "old"}, Status: StatusResolved, LastEvaluatedAt: now},
	}
	result := SnapshotAlerts(rules, states)
	if len(result) != 1 || result[0].Level != "critical" || result[0].Source != "local/host/host" {
		t.Fatalf("snapshot alerts = %#v", result)
	}
}
