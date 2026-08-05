package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/model"
	servicecatalog "github.com/bnhminh1010/homelab-dashboard/internal/services"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("database path must not be empty")
	}
	if !strings.HasPrefix(path, "file:") && path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, now: time.Now}
	if err := store.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context) error {
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("apply sqlite setting: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied int
		if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", entry.Name()).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if applied != 0 {
			continue
		}
		contents, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, string(contents)); err == nil {
			_, err = tx.ExecContext(ctx,
				"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
				entry.Name(), s.now().UTC().Format(time.RFC3339Nano))
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) ListServices(ctx context.Context) ([]model.Service, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, icon, display_url, probe_url, category, tags_json, created_at, updated_at
		FROM services ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()
	services := make([]model.Service, 0)
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		services = append(services, service)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	return services, nil
}

func (s *Store) GetService(ctx context.Context, id string) (model.Service, error) {
	service, err := scanService(s.db.QueryRowContext(ctx, `
		SELECT id, name, icon, display_url, probe_url, category, tags_json, created_at, updated_at
		FROM services WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Service{}, ErrNotFound
	}
	return service, err
}

func (s *Store) CreateService(ctx context.Context, service model.Service) (model.Service, error) {
	now := s.now().UTC()
	service.CreatedAt = now
	service.UpdatedAt = now
	service.Status = model.ServiceStatusUnknown
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Service{}, fmt.Errorf("begin create service: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM services").Scan(&count); err != nil {
		return model.Service{}, fmt.Errorf("count services: %w", err)
	}
	if count >= servicecatalog.MaxServices {
		return model.Service{}, servicecatalog.ErrServiceLimit
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO services(id, name, icon, display_url, probe_url, category, tags_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		service.ID, service.Name, service.Icon, service.DisplayURL, service.ProbeURL, service.Category, encodeTags(service.Tags),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return model.Service{}, fmt.Errorf("create service: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Service{}, fmt.Errorf("commit create service: %w", err)
	}
	return service, nil
}

func (s *Store) UpdateService(ctx context.Context, id string, input model.ServiceInput) (model.Service, error) {
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE services SET name = ?, icon = ?, display_url = ?, probe_url = ?, category = ?, tags_json = ?, updated_at = ?
		WHERE id = ?`, input.Name, input.Icon, input.DisplayURL, input.ProbeURL, input.Category, encodeTags(input.Tags),
		now.Format(time.RFC3339Nano), id)
	if err != nil {
		return model.Service{}, fmt.Errorf("update service: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return model.Service{}, fmt.Errorf("update service: %w", err)
	}
	if affected == 0 {
		return model.Service{}, ErrNotFound
	}
	return s.GetService(ctx, id)
}

func (s *Store) DeleteService(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM services WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AppendAudit(ctx context.Context, event model.AuditEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now().UTC()
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_events(actor, action, target_type, target_id, outcome, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, event.Actor, event.Action, event.TargetType,
		event.TargetID, event.Outcome, string(metadata), event.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]model.AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, actor, action, target_type, target_id, outcome, metadata_json, created_at
		FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	events := make([]model.AuditEvent, 0)
	for rows.Next() {
		var event model.AuditEvent
		var metadata, created string
		if err := rows.Scan(&event.ID, &event.Actor, &event.Action, &event.TargetType,
			&event.TargetID, &event.Outcome, &metadata, &created); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		if err := json.Unmarshal([]byte(metadata), &event.Metadata); err != nil {
			return nil, fmt.Errorf("decode audit metadata: %w", err)
		}
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse audit timestamp: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanService(row scanner) (model.Service, error) {
	var service model.Service
	var created, updated, tagsJSON string
	if err := row.Scan(&service.ID, &service.Name, &service.Icon, &service.DisplayURL,
		&service.ProbeURL, &service.Category, &tagsJSON, &created, &updated); err != nil {
		return model.Service{}, err
	}
	if service.Category == "" {
		service.Category = "Uncategorized"
	}
	if err := json.Unmarshal([]byte(tagsJSON), &service.Tags); err != nil {
		return model.Service{}, fmt.Errorf("decode service tags: %w", err)
	}
	if service.Tags == nil {
		service.Tags = []string{}
	}
	var err error
	service.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return model.Service{}, fmt.Errorf("parse service created_at: %w", err)
	}
	service.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return model.Service{}, fmt.Errorf("parse service updated_at: %w", err)
	}
	service.Status = model.ServiceStatusUnknown
	return service, nil
}

func encodeTags(tags []string) string {
	if tags == nil {
		tags = []string{}
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func (s *Store) ListLaunchpadBookmarks(ctx context.Context) ([]model.LaunchpadBookmark, int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,url,icon,tag,sort_order,created_at,updated_at FROM launchpad_bookmarks ORDER BY sort_order,id`)
	if err != nil {
		return nil, 0, fmt.Errorf("list launchpad bookmarks: %w", err)
	}
	defer rows.Close()
	items := []model.LaunchpadBookmark{}
	for rows.Next() {
		var item model.LaunchpadBookmark
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Title, &item.URL, &item.Icon, &item.Tag, &item.SortOrder, &created, &updated); err != nil {
			return nil, 0, err
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, 0, err
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var revision int64
	if err := s.db.QueryRowContext(ctx, `SELECT launchpad_revision FROM widget_content_meta WHERE singleton_id=1`).Scan(&revision); err != nil {
		return nil, 0, err
	}
	return items, revision, nil
}

func (s *Store) ReplaceLaunchpadBookmarks(ctx context.Context, items []model.LaunchpadBookmark, expectedRevision int64, actor string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT launchpad_revision FROM widget_content_meta WHERE singleton_id=1`).Scan(&revision); err != nil {
		return 0, err
	}
	if revision != expectedRevision {
		return 0, ErrRevisionConflict
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `DELETE FROM launchpad_bookmarks`); err != nil {
		return 0, err
	}
	for i := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO launchpad_bookmarks(id,title,url,icon,tag,sort_order,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, items[i].ID, items[i].Title, items[i].URL, items[i].Icon, items[i].Tag, i, now, now); err != nil {
			return 0, err
		}
	}
	revision++
	if _, err := tx.ExecContext(ctx, `UPDATE widget_content_meta SET launchpad_revision=? WHERE singleton_id=1`, revision); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return revision, nil
}

func (s *Store) GetOperatorNote(ctx context.Context) (model.OperatorNote, error) {
	var note model.OperatorNote
	var updated string
	if err := s.db.QueryRowContext(ctx, `SELECT text,revision,updated_at,updated_by FROM operator_notes WHERE singleton_id=1`).Scan(&note.Text, &note.Revision, &updated, &note.UpdatedBy); err != nil {
		return note, err
	}
	var err error
	note.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return note, err
}

var ErrRevisionConflict = errors.New("widget content revision conflict")

func (s *Store) UpdateOperatorNote(ctx context.Context, text string, expectedRevision int64, actor string) (model.OperatorNote, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.OperatorNote{}, err
	}
	defer tx.Rollback()
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM operator_notes WHERE singleton_id=1`).Scan(&revision); err != nil {
		return model.OperatorNote{}, err
	}
	if revision != expectedRevision {
		return model.OperatorNote{}, ErrRevisionConflict
	}
	revision++
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE operator_notes SET text=?,revision=?,updated_at=?,updated_by=? WHERE singleton_id=1`, text, revision, now.Format(time.RFC3339Nano), actor); err != nil {
		return model.OperatorNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.OperatorNote{}, err
	}
	return model.OperatorNote{Text: text, Revision: revision, UpdatedAt: now, UpdatedBy: actor}, nil
}
