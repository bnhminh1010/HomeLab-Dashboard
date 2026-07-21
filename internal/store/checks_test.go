package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/healthchecks"
	"github.com/binhminh/HomeLab-Minh/internal/model"
)

func TestCheckObservationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "checks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := model.Service{ID: "svc_tls", Name: "TLS", DisplayURL: "https://example.test"}
	if _, err := database.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	if err := database.UpsertCertificateObservation(ctx, healthchecks.CertificateObservation{ServiceID: service.ID, CheckedAt: now, NotAfter: now.Add(14 * 24 * time.Hour), Issuer: "test"}); err != nil {
		t.Fatal(err)
	}
	certificates, err := database.ListCertificateObservations(ctx)
	if err != nil || len(certificates) != 1 || certificates[0].Issuer != "test" {
		t.Fatalf("certificates = %#v, %v", certificates, err)
	}
	if err := database.UpsertBackupObservation(ctx, "local", model.BackupStatus{Job: "nightly", Status: "success", CompletedAt: now, ExpectedWithinSeconds: 86400, Bytes: 42}, now); err != nil {
		t.Fatal(err)
	}
	backups, err := database.ListBackupObservations(ctx, "local")
	if err != nil || len(backups) != 1 || backups[0].Status.Bytes != 42 {
		t.Fatalf("backups = %#v, %v", backups, err)
	}
	if err := database.UpsertBackupObservation(ctx, "local", model.BackupStatus{Job: "invalid", Status: "completed"}, now); err == nil {
		t.Fatal("invalid backup status was persisted")
	}
}

func TestCertificateObservationsFollowConfiguredHTTPSDisplayURL(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "certificate-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := model.Service{ID: "svc_tls_scope", Name: "TLS", DisplayURL: "https://old.example.test"}
	if _, err := database.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	if err := database.UpsertCertificateObservation(ctx, healthchecks.CertificateObservation{ServiceID: service.ID, CheckedAt: now, NotAfter: now.Add(14 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if items, err := database.ListCertificateObservations(ctx); err != nil || len(items) != 1 {
		t.Fatalf("initial certificate observations = %#v, %v", items, err)
	}
	if _, err := database.UpdateService(ctx, service.ID, model.ServiceInput{Name: service.Name, DisplayURL: "https://new.example.test"}); err != nil {
		t.Fatal(err)
	}
	if items, err := database.ListCertificateObservations(ctx); err != nil || len(items) != 0 {
		t.Fatalf("old HTTPS endpoint was returned: %#v, %v", items, err)
	}
	if err := database.ReconcileCertificateObservations(ctx); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_observations`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("stale endpoint observations = %d, %v", stored, err)
	}
	if err := database.UpsertCertificateObservation(ctx, healthchecks.CertificateObservation{ServiceID: service.ID, CheckedAt: now, NotAfter: now.Add(14 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateService(ctx, service.ID, model.ServiceInput{Name: service.Name, DisplayURL: "http://new.example.test"}); err != nil {
		t.Fatal(err)
	}
	if items, err := database.ListCertificateObservations(ctx); err != nil || len(items) != 0 {
		t.Fatalf("HTTP service retained a TLS observation: %#v, %v", items, err)
	}
	if err := database.ReconcileCertificateObservations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_observations`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("HTTP observation was not cleaned up: %d, %v", stored, err)
	}
}
