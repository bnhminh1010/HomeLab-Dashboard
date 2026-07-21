package healthchecks

import (
	"testing"
	"time"
)

func TestDecodeBackupReportsAcceptsSingleAndWrapper(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"job":"nightly","status":"success","completedAt":"2026-07-22T00:00:00Z","expectedWithinSeconds":86400}`),
		[]byte(`{"backups":[{"job":"nightly","status":"failed","message":"disk full"}]}`),
	} {
		items, err := DecodeBackupReports(raw)
		if err != nil || len(items) != 1 {
			t.Fatalf("DecodeBackupReports() = %#v, %v", items, err)
		}
	}
}

func TestBackupFreshnessRejectsOverdueAndFuture(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	items, err := DecodeBackupReports([]byte(`{"job":"nightly","status":"success","completedAt":"2026-07-20T00:00:00Z","expectedWithinSeconds":86400}`))
	if err != nil {
		t.Fatal(err)
	}
	if healthy, _, _ := BackupFreshness(items[0], now); healthy {
		t.Fatal("overdue backup reported healthy")
	}
}
