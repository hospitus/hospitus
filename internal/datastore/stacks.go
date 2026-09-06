package datastore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// StackRecord represents a persisted stack in the database.
//
// CreatedAt and UpdatedAt are read-only: the datastore sets them on write
// (CreateStack/UpdateStack), so any value supplied by the caller is ignored on
// insert/update and only populated when a record is read back.
type StackRecord struct {
	Name      string
	Status    string
	Manifest  string // JSON-encoded stack manifest
	CreatedAt time.Time
	UpdatedAt time.Time
}

// StackInstanceRecord represents a persisted instance within a stack.
type StackInstanceRecord struct {
	StackName    string
	InstanceName string
	InstanceID   string
	Provider     string
	DependsOn    []string // JSON-encoded list
	Status       string
	Health       string
	Handle       string // JSON-encoded provider.InstanceHandle
	DeployOrder  int
}

// CreateStack inserts a new stack record into the database.
func (ds *Datastore) CreateStack(ctx context.Context, stack *StackRecord) error {
	query := `
		INSERT INTO stacks (name, status, manifest, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`
	now := time.Now()
	_, err := ds.db.ExecContext(ctx, query,
		stack.Name,
		stack.Status,
		stack.Manifest,
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("failed to create stack %s: %w", stack.Name, err)
	}

	return nil
}

// GetStack retrieves a stack record by name.
func (ds *Datastore) GetStack(ctx context.Context, name string) (*StackRecord, error) {
	query := `
		SELECT name, status, manifest, created_at, updated_at
		FROM stacks
		WHERE name = ?
	`
	var stack StackRecord
	err := ds.db.QueryRowContext(ctx, query, name).Scan(
		&stack.Name,
		&stack.Status,
		&stack.Manifest,
		&stack.CreatedAt,
		&stack.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get stack %s: %w", name, err)
	}

	return &stack, nil
}

// ListStacks retrieves all stack records.
func (ds *Datastore) ListStacks(ctx context.Context) ([]*StackRecord, error) {
	query := `
		SELECT name, status, manifest, created_at, updated_at
		FROM stacks
		ORDER BY created_at
	`
	rows, err := ds.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list stacks: %w", err)
	}
	defer rows.Close()

	var stacks []*StackRecord
	for rows.Next() {
		var stack StackRecord
		if err := rows.Scan(&stack.Name, &stack.Status, &stack.Manifest, &stack.CreatedAt, &stack.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan stack row: %w", err)
		}
		stacks = append(stacks, &stack)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating stack rows: %w", err)
	}

	return stacks, nil
}

// UpdateStackStatus updates the status and updated_at timestamp of a stack.
func (ds *Datastore) UpdateStackStatus(ctx context.Context, name, status string) error {
	query := `
		UPDATE stacks
		SET status = ?, updated_at = ?
		WHERE name = ?
	`
	result, err := ds.db.ExecContext(ctx, query, status, time.Now(), name)
	if err != nil {
		return fmt.Errorf("failed to update stack status %s: %w", name, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check stack update result: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("stack %s not found", name)
	}

	return nil
}

// DeleteStack removes a stack record and its instances (cascade).
func (ds *Datastore) DeleteStack(ctx context.Context, name string) error {
	query := `DELETE FROM stacks WHERE name = ?`
	result, err := ds.db.ExecContext(ctx, query, name)
	if err != nil {
		return fmt.Errorf("failed to delete stack %s: %w", name, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check stack delete result: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("stack %s not found", name)
	}

	return nil
}

// CreateStackInstance inserts a new stack instance record.
func (ds *Datastore) CreateStackInstance(ctx context.Context, inst *StackInstanceRecord) error {
	dependsOnJSON := "[]"
	if len(inst.DependsOn) > 0 {
		data, err := json.Marshal(inst.DependsOn)
		if err != nil {
			return fmt.Errorf("failed to marshal depends_on: %w", err)
		}
		dependsOnJSON = string(data)
	}

	query := `
		INSERT INTO stack_instances
			(stack_name, instance_name, instance_id, provider, depends_on, status, health, handle, deploy_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := ds.db.ExecContext(ctx, query,
		inst.StackName,
		inst.InstanceName,
		inst.InstanceID,
		inst.Provider,
		dependsOnJSON,
		inst.Status,
		inst.Health,
		inst.Handle,
		inst.DeployOrder,
	)
	if err != nil {
		return fmt.Errorf("failed to create stack instance %s/%s: %w", inst.StackName, inst.InstanceName, err)
	}

	return nil
}

// GetStackInstances retrieves all instances for a given stack.
func (ds *Datastore) GetStackInstances(ctx context.Context, stackName string) ([]*StackInstanceRecord, error) {
	query := `
		SELECT stack_name, instance_name, instance_id, provider, depends_on, status, health, handle, deploy_order
		FROM stack_instances
		WHERE stack_name = ?
		ORDER BY deploy_order
	`
	rows, err := ds.db.QueryContext(ctx, query, stackName)
	if err != nil {
		return nil, fmt.Errorf("failed to get stack instances for %s: %w", stackName, err)
	}
	defer rows.Close()

	var instances []*StackInstanceRecord
	for rows.Next() {
		var inst StackInstanceRecord
		var dependsOnJSON string
		if err := rows.Scan(
			&inst.StackName,
			&inst.InstanceName,
			&inst.InstanceID,
			&inst.Provider,
			&dependsOnJSON,
			&inst.Status,
			&inst.Health,
			&inst.Handle,
			&inst.DeployOrder,
		); err != nil {
			return nil, fmt.Errorf("failed to scan stack instance row: %w", err)
		}

		if dependsOnJSON != "" && dependsOnJSON != "null" {
			if err := json.Unmarshal([]byte(dependsOnJSON), &inst.DependsOn); err != nil {
				return nil, fmt.Errorf("failed to unmarshal depends_on for %s: %w", inst.InstanceName, err)
			}
		}

		instances = append(instances, &inst)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating stack instance rows: %w", err)
	}

	return instances, nil
}

// UpdateStackInstanceStatus updates the status of a stack instance.
func (ds *Datastore) UpdateStackInstanceStatus(ctx context.Context, stackName, instanceName, status string) error {
	query := `
		UPDATE stack_instances
		SET status = ?
		WHERE stack_name = ? AND instance_name = ?
	`
	result, err := ds.db.ExecContext(ctx, query, status, stackName, instanceName)
	if err != nil {
		return fmt.Errorf("failed to update stack instance status %s/%s: %w", stackName, instanceName, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check stack instance update result: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("stack instance %s/%s not found", stackName, instanceName)
	}

	return nil
}

// UpdateStackInstanceHealth updates the health status of a stack instance.
func (ds *Datastore) UpdateStackInstanceHealth(ctx context.Context, stackName, instanceName, health string) error {
	query := `
		UPDATE stack_instances
		SET health = ?
		WHERE stack_name = ? AND instance_name = ?
	`
	result, err := ds.db.ExecContext(ctx, query, health, stackName, instanceName)
	if err != nil {
		return fmt.Errorf("failed to update stack instance health %s/%s: %w", stackName, instanceName, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check stack instance health update result: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("stack instance %s/%s not found", stackName, instanceName)
	}

	return nil
}

// DeleteStackInstance removes one instance record from a stack.
//
// A deployment that fails partway rolls back the instances it created. Without
// this their rows survive, and a stack listing instances that no longer exist
// cannot be destroyed afterwards.
func (ds *Datastore) DeleteStackInstance(ctx context.Context, stackName, instanceName string) error {
	query := `DELETE FROM stack_instances WHERE stack_name = ? AND instance_name = ?`
	if _, err := ds.db.ExecContext(ctx, query, stackName, instanceName); err != nil {
		return fmt.Errorf("failed to delete stack instance %s/%s: %w", stackName, instanceName, err)
	}
	return nil
}

// DeleteStackInstances removes all instance records for a given stack.
func (ds *Datastore) DeleteStackInstances(ctx context.Context, stackName string) error {
	query := `DELETE FROM stack_instances WHERE stack_name = ?`
	_, err := ds.db.ExecContext(ctx, query, stackName)
	if err != nil {
		return fmt.Errorf("failed to delete stack instances for %s: %w", stackName, err)
	}

	return nil
}
