package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/healthchecks"
	"github.com/binhminh/HomeLab-Minh/internal/model"
)

func (s *Store) UpsertCertificateObservation(ctx context.Context, observation healthchecks.CertificateObservation) error {
	observation.ServiceID = strings.TrimSpace(observation.ServiceID)
	if observation.ServiceID == "" || observation.CheckedAt.IsZero() {
		return fmt.Errorf("certificate observation requires service id and checked time")
	}
	var endpointURL string
	if err := s.db.QueryRowContext(ctx, `SELECT display_url FROM services WHERE id = ?`, observation.ServiceID).Scan(&endpointURL); err != nil {
		return fmt.Errorf("load certificate endpoint: %w", err)
	}
	if !isHTTPSDisplayURL(endpointURL) {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM certificate_observations WHERE service_id = ?`, observation.ServiceID); err != nil {
			return fmt.Errorf("clear non-HTTPS certificate observation: %w", err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO certificate_observations(service_id, endpoint_url, checked_at, not_after, issuer, error_message)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(service_id) DO UPDATE SET
			endpoint_url = excluded.endpoint_url, checked_at = excluded.checked_at, not_after = excluded.not_after,
			issuer = excluded.issuer, error_message = excluded.error_message`,
		observation.ServiceID, endpointURL, observation.CheckedAt.UTC().Format(time.RFC3339Nano), nullableTime(observation.NotAfter),
		observation.Issuer, observation.Error)
	if err != nil {
		return fmt.Errorf("upsert certificate observation: %w", err)
	}
	return nil
}

func (s *Store) ListCertificateObservations(ctx context.Context) ([]healthchecks.CertificateObservation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT observation.service_id, observation.checked_at, observation.not_after,
			observation.issuer, observation.error_message
		FROM certificate_observations AS observation
		INNER JOIN services AS service ON service.id = observation.service_id
		WHERE lower(service.display_url) LIKE 'https://%'
			AND observation.endpoint_url = service.display_url
		ORDER BY observation.checked_at DESC, observation.service_id`)
	if err != nil {
		return nil, fmt.Errorf("list certificate observations: %w", err)
	}
	defer rows.Close()
	items := make([]healthchecks.CertificateObservation, 0)
	for rows.Next() {
		var item healthchecks.CertificateObservation
		var checkedAt string
		var notAfter sql.NullString
		if err := rows.Scan(&item.ServiceID, &checkedAt, &notAfter, &item.Issuer, &item.Error); err != nil {
			return nil, fmt.Errorf("scan certificate observation: %w", err)
		}
		var err error
		item.CheckedAt, err = time.Parse(time.RFC3339Nano, checkedAt)
		if err != nil {
			return nil, fmt.Errorf("parse certificate checked time: %w", err)
		}
		if notAfter.Valid && notAfter.String != "" {
			item.NotAfter, err = time.Parse(time.RFC3339Nano, notAfter.String)
			if err != nil {
				return nil, fmt.Errorf("parse certificate expiry: %w", err)
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ReconcileCertificateObservations removes observations that no longer belong
// to the exact configured HTTPS endpoint. The read query uses the same scope
// immediately, while this cleanup keeps the small latest-result table honest.
func (s *Store) ReconcileCertificateObservations(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM certificate_observations AS observation
		WHERE NOT EXISTS (
			SELECT 1 FROM services AS service
			WHERE service.id = observation.service_id
				AND lower(service.display_url) LIKE 'https://%'
				AND service.display_url = observation.endpoint_url
		)`)
	if err != nil {
		return fmt.Errorf("reconcile certificate observations: %w", err)
	}
	return nil
}

func (s *Store) UpsertBackupObservation(ctx context.Context, nodeID string, status model.BackupStatus, observedAt time.Time) error {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" || observedAt.IsZero() {
		return fmt.Errorf("backup observation requires node, job, and observed time")
	}
	var err error
	status, err = healthchecks.ValidateBackupStatus(status)
	if err != nil {
		return fmt.Errorf("validate backup observation: %w", err)
	}
	if status.Bytes > math.MaxInt64 {
		return fmt.Errorf("backup observation bytes exceed SQLite integer range")
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO backup_observations(
			node_id, job, status, completed_at, expected_within_seconds, bytes, message, observed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, job) DO UPDATE SET
			status = excluded.status, completed_at = excluded.completed_at,
			expected_within_seconds = excluded.expected_within_seconds, bytes = excluded.bytes,
			message = excluded.message, observed_at = excluded.observed_at`,
		nodeID, status.Job, status.Status, nullableTime(status.CompletedAt), status.ExpectedWithinSeconds,
		int64(status.Bytes), status.Message, observedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert backup observation: %w", err)
	}
	return nil
}

func (s *Store) ListBackupObservations(ctx context.Context, nodeID string) ([]healthchecks.BackupObservation, error) {
	query := `SELECT node_id, job, status, completed_at, expected_within_seconds, bytes, message, observed_at
		FROM backup_observations`
	args := []any(nil)
	if nodeID != "" {
		query += ` WHERE node_id = ?`
		args = append(args, nodeID)
	}
	query += ` ORDER BY node_id, job`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list backup observations: %w", err)
	}
	defer rows.Close()
	items := make([]healthchecks.BackupObservation, 0)
	for rows.Next() {
		var item healthchecks.BackupObservation
		var completedAt sql.NullString
		var bytes int64
		var observedAt string
		if err := rows.Scan(&item.NodeID, &item.Status.Job, &item.Status.Status, &completedAt,
			&item.Status.ExpectedWithinSeconds, &bytes, &item.Status.Message, &observedAt); err != nil {
			return nil, fmt.Errorf("scan backup observation: %w", err)
		}
		if bytes < 0 {
			return nil, fmt.Errorf("backup observation has negative byte count")
		}
		item.Status.Bytes = uint64(bytes)
		var err error
		item.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return nil, fmt.Errorf("parse backup observed time: %w", err)
		}
		if completedAt.Valid && completedAt.String != "" {
			item.Status.CompletedAt, err = time.Parse(time.RFC3339Nano, completedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse backup completed time: %w", err)
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func isHTTPSDisplayURL(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "https://")
}
