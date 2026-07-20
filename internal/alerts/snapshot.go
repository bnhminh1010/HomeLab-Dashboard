package alerts

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/model"
)

// SnapshotAlerts projects active rule state into the compact alert shape used
// by the live dashboard. Alert rules remain the source of severity and name;
// secrets and delivery metadata never enter metrics snapshots.
func SnapshotAlerts(rules []AlertRule, states []AlertState) []model.Alert {
	byID := make(map[string]AlertRule, len(rules))
	for _, rule := range rules {
		byID[rule.ID] = rule
	}
	result := make([]model.Alert, 0, len(states))
	for _, state := range states {
		if state.Status == StatusResolved {
			continue
		}
		rule, ok := byID[state.RuleID]
		if !ok {
			continue
		}
		occurredAt := state.LastEvaluatedAt
		if state.FiringSince != nil {
			occurredAt = *state.FiringSince
		} else if state.PendingSince != nil {
			occurredAt = *state.PendingSince
		}
		if occurredAt.IsZero() {
			occurredAt = time.Unix(0, 0).UTC()
		}
		resource := strings.Trim(state.ResourceType+"/"+state.ResourceID, "/")
		result = append(result, model.Alert{
			ID:    state.RuleID + ":" + state.NodeID + ":" + state.ResourceType + ":" + state.ResourceID,
			Level: string(rule.Severity), Source: state.NodeID + "/" + resource,
			Message:    fmt.Sprintf("%s is %s (value %.2f)", rule.Name, state.Status, state.LastValue),
			OccurredAt: occurredAt.UTC(),
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Level != result[j].Level {
			return severityRank(Severity(result[i].Level)) < severityRank(Severity(result[j].Level))
		}
		return result[i].OccurredAt.After(result[j].OccurredAt)
	})
	return result
}

func severityRank(severity Severity) int {
	switch severity {
	case SeverityCritical:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}
