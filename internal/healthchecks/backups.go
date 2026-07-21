// Package healthchecks contains compact, dependency-free checks that enrich
// the dashboard without becoming another monitoring runtime.
package healthchecks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
)

const maxBackupReportBytes = 64 << 10

// BackupFileSource reads a report written atomically by any backup job. The
// expected document is either one report object or {"backups":[...]}.
// Jobs should write a temporary file and rename it into place after success.
type BackupFileSource struct {
	path string
}

func NewBackupFileSource(path string) (*BackupFileSource, error) {
	path = strings.TrimSpace(path)
	if path != "" && !filepath.IsAbs(path) {
		return nil, errors.New("backup status file must be an absolute path")
	}
	return &BackupFileSource{path: path}, nil
}

func (s *BackupFileSource) Backups(ctx context.Context) ([]model.BackupStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.path == "" {
		return []model.BackupStatus{}, nil
	}
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []model.BackupStatus{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open backup report: %w", err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxBackupReportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read backup report: %w", err)
	}
	if len(contents) > maxBackupReportBytes {
		return nil, errors.New("backup report exceeds 64 KiB")
	}
	items, err := DecodeBackupReports(contents)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func DecodeBackupReports(raw []byte) ([]model.BackupStatus, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, errors.New("backup report is empty")
	}
	var wrapper struct {
		Backups []model.BackupStatus `json:"backups"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Backups != nil {
		return ValidateBackupReports(wrapper.Backups)
	}
	var one model.BackupStatus
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, fmt.Errorf("decode backup report: %w", err)
	}
	return ValidateBackupReports([]model.BackupStatus{one})
}

// ValidateBackupReports normalizes and validates backup reports from every
// ingestion boundary. Store writers use the same function as the file source
// so a remote agent cannot persist a status the local status-file contract
// would reject.
func ValidateBackupReports(items []model.BackupStatus) ([]model.BackupStatus, error) {
	if len(items) == 0 || len(items) > 50 {
		return nil, errors.New("backup report must contain between 1 and 50 jobs")
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]model.BackupStatus, 0, len(items))
	for _, item := range items {
		item.Job = strings.TrimSpace(item.Job)
		item.Status = strings.ToLower(strings.TrimSpace(item.Status))
		item.Message = strings.TrimSpace(item.Message)
		if !validBackupText(item.Job, 120, true) || !validBackupText(item.Message, 800, false) {
			return nil, errors.New("backup report contains invalid job or message text")
		}
		if _, exists := seen[item.Job]; exists {
			return nil, fmt.Errorf("backup report repeats job %q", item.Job)
		}
		seen[item.Job] = struct{}{}
		switch item.Status {
		case "success", "failed", "running", "unknown":
		default:
			return nil, fmt.Errorf("backup report has invalid status %q", item.Status)
		}
		if item.ExpectedWithinSeconds < 0 || item.ExpectedWithinSeconds > int64((365*24*time.Hour).Seconds()) {
			return nil, errors.New("backup report has invalid expectedWithinSeconds")
		}
		if !item.CompletedAt.IsZero() {
			item.CompletedAt = item.CompletedAt.UTC()
		}
		result = append(result, item)
	}
	return result, nil
}

// ValidateBackupStatus is the single-item form used by persistence writers.
func ValidateBackupStatus(item model.BackupStatus) (model.BackupStatus, error) {
	items, err := ValidateBackupReports([]model.BackupStatus{item})
	if err != nil {
		return model.BackupStatus{}, err
	}
	return items[0], nil
}

func validBackupText(value string, maxRunes int, required bool) bool {
	if (required && value == "") || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, character := range value {
		if character == '\n' || character == '\r' || character == 0 {
			return false
		}
	}
	return true
}

func BackupFreshness(status model.BackupStatus, now time.Time) (healthy bool, age time.Duration, reason string) {
	if status.Status != "success" {
		return false, 0, "last report is not successful"
	}
	if status.CompletedAt.IsZero() {
		return false, 0, "successful report has no completion time"
	}
	age = now.UTC().Sub(status.CompletedAt.UTC())
	if age < 0 {
		return false, age, "completion time is in the future"
	}
	if status.ExpectedWithinSeconds > 0 && age > time.Duration(status.ExpectedWithinSeconds)*time.Second {
		return false, age, "backup is overdue"
	}
	return true, age, ""
}

func BackupAlerts(items []model.BackupStatus, now time.Time) []model.Alert {
	alerts := make([]model.Alert, 0)
	for _, item := range items {
		healthy, _, reason := BackupFreshness(item, now)
		if healthy {
			continue
		}
		message := "Backup " + item.Job + " needs attention"
		if reason != "" {
			message += ": " + reason
		}
		alerts = append(alerts, model.Alert{
			ID: "backup:" + item.Job, Level: "warning", Source: "backup", Message: message, OccurredAt: now.UTC(),
		})
	}
	return alerts
}

type BackupObservation struct {
	NodeID     string             `json:"nodeId"`
	ObservedAt time.Time          `json:"observedAt"`
	Status     model.BackupStatus `json:"status"`
}
