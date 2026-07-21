package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bnhminh1010/homelab-dashboard/internal/operations"
)

// RecordOperationalEvent persists an automatic or validated manual timeline
// entry. It intentionally stores no arbitrary metadata, unlike audit events.
func (s *Store) RecordOperationalEvent(ctx context.Context, event operations.Event) (operations.Event, error) {
	event = operations.NormalizeEvent(event)
	if event.OccurredAt.IsZero() {
		event.OccurredAt = s.now().UTC()
	}
	if err := operations.ValidateEvent(event); err != nil {
		return operations.Event{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO operational_events(
			event_type, source, visibility, title, summary, node_id, service_id,
			container_id, actor, occurred_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.Type, event.Source, event.Visibility, event.Title, event.Summary,
		event.NodeID, event.ServiceID, event.ContainerID, event.Actor,
		event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return operations.Event{}, fmt.Errorf("record operational event: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return operations.Event{}, fmt.Errorf("record operational event id: %w", err)
	}
	event.ID = id
	return event, nil
}

// CreateManualOperationalEvent restricts dashboard-authored entries to the
// human changes that make incident timelines understandable.
func (s *Store) CreateManualOperationalEvent(ctx context.Context, event operations.Event) (operations.Event, error) {
	event.Source = operations.SourceManual
	return s.RecordOperationalEvent(ctx, event)
}

func (s *Store) ListOperationalEvents(ctx context.Context, filter operations.Filter) ([]operations.Event, error) {
	filter, err := operations.NormalizeFilter(filter)
	if err != nil {
		return nil, err
	}
	query := strings.Builder{}
	query.WriteString(`
		SELECT id, event_type, source, visibility, title, summary, node_id,
			service_id, container_id, actor, occurred_at
		FROM operational_events`)
	where := make([]string, 0, 6)
	args := make([]any, 0, 7)
	if !filter.From.IsZero() {
		where = append(where, "occurred_at >= ?")
		args = append(args, filter.From.Format(time.RFC3339Nano))
	}
	if !filter.To.IsZero() {
		where = append(where, "occurred_at < ?")
		args = append(args, filter.To.Format(time.RFC3339Nano))
	}
	if filter.Type != "" {
		where = append(where, "event_type = ?")
		args = append(args, filter.Type)
	}
	if filter.NodeID != "" {
		where = append(where, "node_id = ?")
		args = append(args, filter.NodeID)
	}
	if filter.ServiceID != "" {
		where = append(where, "service_id = ?")
		args = append(args, filter.ServiceID)
	}
	if filter.Source != "" {
		where = append(where, "source = ?")
		args = append(args, filter.Source)
	}
	if filter.Visibility != "" {
		where = append(where, "visibility = ?")
		args = append(args, filter.Visibility)
	}
	if len(where) != 0 {
		query.WriteString(" WHERE ")
		query.WriteString(strings.Join(where, " AND "))
	}
	query.WriteString(" ORDER BY occurred_at DESC, id DESC LIMIT ?")
	args = append(args, filter.Limit)

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list operational events: %w", err)
	}
	defer rows.Close()
	events := make([]operations.Event, 0)
	for rows.Next() {
		event, err := scanOperationalEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list operational events: %w", err)
	}
	return events, nil
}

// PurgeOperationalEvents removes timeline records older than before. The
// scheduler owns when it is called; callers normally pass now - 90 days.
func (s *Store) PurgeOperationalEvents(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, fmt.Errorf("purge operational events: cutoff is required")
	}
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM operational_events WHERE occurred_at < ?", before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("purge operational events: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge operational events rows affected: %w", err)
	}
	return deleted, nil
}

type operationalEventScanner interface {
	Scan(...any) error
}

func scanOperationalEvent(scanner operationalEventScanner) (operations.Event, error) {
	var event operations.Event
	var occurredAt string
	if err := scanner.Scan(
		&event.ID, &event.Type, &event.Source, &event.Visibility, &event.Title,
		&event.Summary, &event.NodeID, &event.ServiceID, &event.ContainerID,
		&event.Actor, &occurredAt,
	); err != nil {
		return operations.Event{}, fmt.Errorf("scan operational event: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return operations.Event{}, fmt.Errorf("parse operational event timestamp: %w", err)
	}
	event.OccurredAt = parsed.UTC()
	return event, nil
}

var _ operations.Repository = (*Store)(nil)
