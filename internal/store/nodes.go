package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/nodes"
)

func (s *Store) CreateEnrollment(ctx context.Context, record nodes.EnrollmentRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO node_enrollments(id, token_hash, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, record.ID, record.TokenHash[:], record.CreatedBy,
		record.CreatedAt.UTC().Unix(), record.ExpiresAt.UTC().Unix())
	if err != nil {
		return fmt.Errorf("create node enrollment: %w", err)
	}
	return nil
}

func (s *Store) ConsumeEnrollment(ctx context.Context, tokenHash [32]byte, now time.Time, record nodes.NodeRecord, limit int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin node enrollment: %w", err)
	}
	defer tx.Rollback()
	var enrollmentID string
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM node_enrollments WHERE token_hash = ? AND expires_at > ?`,
		tokenHash[:], now.UTC().Unix()).Scan(&enrollmentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nodes.ErrEnrollmentInvalid
		}
		return fmt.Errorf("read node enrollment: %w", err)
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE revoked_at IS NULL`).Scan(&active); err != nil {
		return fmt.Errorf("count active nodes: %w", err)
	}
	if limit <= 0 || active >= limit {
		return nodes.ErrNodeLimit
	}
	created := record.CreatedAt.UTC().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nodes(id, display_name, hostname, credential_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, record.ID, record.DisplayName, record.Hostname,
		record.CredentialHash[:], created, record.UpdatedAt.UTC().Unix()); err != nil {
		return fmt.Errorf("insert node: %w", err)
	}
	// History uses its own compact metadata table so retaining or pruning metrics
	// never exposes a node credential.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO history_nodes(id, display_name, created_at) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET display_name = excluded.display_name`,
		record.ID, record.DisplayName, created); err != nil {
		return fmt.Errorf("register history node: %w", err)
	}
	if result, err := tx.ExecContext(ctx, `DELETE FROM node_enrollments WHERE id = ?`, enrollmentID); err != nil {
		return fmt.Errorf("consume node enrollment: %w", err)
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		return nodes.ErrEnrollmentInvalid
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit node enrollment: %w", err)
	}
	return nil
}

func (s *Store) GetNode(ctx context.Context, id string) (nodes.NodeRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, display_name, hostname, credential_hash, last_seen_at,
		       created_at, updated_at, revoked_at
		FROM nodes WHERE id = ?`, id)
	return scanNode(row)
}

func (s *Store) ListNodes(ctx context.Context) ([]nodes.Node, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, display_name, hostname, credential_hash, last_seen_at,
		       created_at, updated_at, revoked_at
		FROM nodes WHERE revoked_at IS NULL ORDER BY lower(display_name), id`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()
	result := make([]nodes.Node, 0)
	for rows.Next() {
		record, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record.Node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	return result, nil
}

func (s *Store) TouchNode(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE nodes SET last_seen_at = ?, updated_at = ?
		WHERE id = ? AND revoked_at IS NULL`, now.UTC().Unix(), now.UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("touch node: %w", err)
	}
	return requireOneNode(result)
}

func (s *Store) RevokeNode(ctx context.Context, id string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin node revocation: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE nodes SET revoked_at = ?, updated_at = ?
		WHERE id = ? AND revoked_at IS NULL`, now.UTC().Unix(), now.UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("revoke node: %w", err)
	}
	if err := requireOneNode(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM alert_states WHERE node_id = ?", id); err != nil {
		return fmt.Errorf("clear revoked node alert state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE alert_deliveries
		SET status = 'superseded', last_error = 'node revoked'
		WHERE node_id = ? AND status IN ('pending', 'processing')`, id); err != nil {
		return fmt.Errorf("supersede revoked node alert deliveries: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE dashboard_ui_preferences SET default_node_id = 'local'
		WHERE singleton_id = 1 AND default_node_id = ?`, id); err != nil {
		return fmt.Errorf("reset revoked default node: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit node revocation: %w", err)
	}
	return nil
}

func (s *Store) DeleteExpiredEnrollments(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM node_enrollments WHERE expires_at <= ?`, before.UTC().Unix()); err != nil {
		return fmt.Errorf("delete expired node enrollments: %w", err)
	}
	return nil
}

func requireOneNode(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read node update result: %w", err)
	}
	if affected != 1 {
		return nodes.ErrNodeNotFound
	}
	return nil
}

func scanNode(row scanner) (nodes.NodeRecord, error) {
	var record nodes.NodeRecord
	var credential []byte
	var lastSeen, revoked sql.NullInt64
	var created, updated int64
	if err := row.Scan(&record.ID, &record.DisplayName, &record.Hostname, &credential,
		&lastSeen, &created, &updated, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nodes.NodeRecord{}, nodes.ErrNodeNotFound
		}
		return nodes.NodeRecord{}, fmt.Errorf("scan node: %w", err)
	}
	if len(credential) != len(record.CredentialHash) {
		return nodes.NodeRecord{}, errors.New("scan node: invalid credential hash")
	}
	copy(record.CredentialHash[:], credential)
	record.CreatedAt = time.Unix(created, 0).UTC()
	record.UpdatedAt = time.Unix(updated, 0).UTC()
	if lastSeen.Valid {
		value := time.Unix(lastSeen.Int64, 0).UTC()
		record.LastSeenAt = &value
	}
	if revoked.Valid {
		value := time.Unix(revoked.Int64, 0).UTC()
		record.RevokedAt = &value
	}
	return record, nil
}
