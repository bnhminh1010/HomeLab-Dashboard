package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/binhminh/HomeLab-Minh/internal/topology"
)

// ListTopologyDependencies returns manual topology edges in stable creation
// order for one logical node. Node IDs include the special local node.
func (s *Store) ListTopologyDependencies(ctx context.Context, nodeID string) ([]topology.Dependency, error) {
	nodeID = topology.NormalizeInput(topology.DependencyInput{NodeID: nodeID}).NodeID
	if err := topology.ValidateNodeID(nodeID); err != nil {
		return nil, fmt.Errorf("list topology dependencies: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, node_id, dependent_service_id, dependency_service_id, label, created_at, updated_at
		FROM topology_dependencies
		WHERE node_id = ?
		ORDER BY id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list topology dependencies: %w", err)
	}
	defer rows.Close()
	dependencies := make([]topology.Dependency, 0)
	for rows.Next() {
		dependency, err := scanTopologyDependency(rows)
		if err != nil {
			return nil, err
		}
		dependencies = append(dependencies, dependency)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list topology dependencies: %w", err)
	}
	return dependencies, nil
}

// CreateTopologyDependency persists one manually curated edge. Both service
// ends must exist in the local catalog; directed cycles remain valid.
func (s *Store) CreateTopologyDependency(ctx context.Context, input topology.DependencyInput) (topology.Dependency, error) {
	input = topology.NormalizeInput(input)
	if err := topology.ValidateInput(input); err != nil {
		return topology.Dependency{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return topology.Dependency{}, fmt.Errorf("begin create topology dependency: %w", err)
	}
	defer tx.Rollback()
	if input.NodeID != "local" {
		var activeNodeCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id = ? AND revoked_at IS NULL`, input.NodeID).Scan(&activeNodeCount); err != nil {
			return topology.Dependency{}, fmt.Errorf("check topology node: %w", err)
		}
		if activeNodeCount != 1 {
			return topology.Dependency{}, topology.ErrNodeNotFound
		}
	}

	var serviceCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE id IN (?, ?)`,
		input.DependentServiceID, input.DependencyServiceID).Scan(&serviceCount); err != nil {
		return topology.Dependency{}, fmt.Errorf("check topology services: %w", err)
	}
	if serviceCount != 2 {
		return topology.Dependency{}, topology.ErrServiceNotFound
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM topology_dependencies
		WHERE node_id = ? AND dependent_service_id = ? AND dependency_service_id = ?`,
		input.NodeID, input.DependentServiceID, input.DependencyServiceID).Scan(&existing); err != nil {
		return topology.Dependency{}, fmt.Errorf("check duplicate topology dependency: %w", err)
	}
	if existing != 0 {
		return topology.Dependency{}, topology.ErrDuplicateDependency
	}

	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO topology_dependencies(
			node_id, dependent_service_id, dependency_service_id, label, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
		input.NodeID, input.DependentServiceID, input.DependencyServiceID, input.Label,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return topology.Dependency{}, fmt.Errorf("create topology dependency: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return topology.Dependency{}, fmt.Errorf("read topology dependency id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return topology.Dependency{}, fmt.Errorf("commit topology dependency: %w", err)
	}
	return topology.Dependency{
		ID: id, NodeID: input.NodeID, DependentServiceID: input.DependentServiceID,
		DependencyServiceID: input.DependencyServiceID, Label: input.Label,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// DeleteTopologyDependency deletes a dependency only when it belongs to the
// requested logical node, preventing a node-scoped UI from removing another
// node's relationship by ID.
func (s *Store) DeleteTopologyDependency(ctx context.Context, nodeID string, id int64) error {
	nodeID = topology.NormalizeInput(topology.DependencyInput{NodeID: nodeID}).NodeID
	if id <= 0 {
		return topology.ErrDependencyNotFound
	}
	if err := topology.ValidateNodeID(nodeID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM topology_dependencies WHERE id = ? AND node_id = ?`, id, nodeID)
	if err != nil {
		return fmt.Errorf("delete topology dependency: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete topology dependency: %w", err)
	}
	if affected != 1 {
		return topology.ErrDependencyNotFound
	}
	return nil
}

func scanTopologyDependency(row scanner) (topology.Dependency, error) {
	var dependency topology.Dependency
	var createdAt, updatedAt string
	if err := row.Scan(&dependency.ID, &dependency.NodeID, &dependency.DependentServiceID,
		&dependency.DependencyServiceID, &dependency.Label, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return topology.Dependency{}, topology.ErrDependencyNotFound
		}
		return topology.Dependency{}, fmt.Errorf("scan topology dependency: %w", err)
	}
	var err error
	dependency.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return topology.Dependency{}, fmt.Errorf("parse topology dependency creation time: %w", err)
	}
	dependency.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return topology.Dependency{}, fmt.Errorf("parse topology dependency update time: %w", err)
	}
	return dependency, nil
}
