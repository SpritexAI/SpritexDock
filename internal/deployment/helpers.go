package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/SpritexAI/SpritexDock/internal/db"
)

var (
	// ErrDeployQueued signals the application already has an in-progress deployment.
	ErrDeployQueued = errors.New("application has a queued or in-progress deployment")
	// ErrNoCurrentRun signals the application has no current running deployment.
	ErrNoCurrentRun = errors.New("application has no current running deployment")
	// ErrNoContainerID signals the deployment has no container ID attached.
	ErrNoContainerID = errors.New("deployment has no container id")
)

// ActiveDeploymentIDs returns deployment IDs that are still in progress for the
// given application (queued, building, deploying).
func ActiveDeploymentIDs(ctx context.Context, state *db.DB, appID string) ([]string, error) {
	rows, err := state.QueryContext(ctx, `
		SELECT id FROM deployments
		WHERE application_id = ? AND status IN (?, ?, ?)
		ORDER BY created_at ASC
	`, appID, StatusQueued, StatusBuilding, StatusDeploying)
	if err != nil {
		return nil, fmt.Errorf("active deployment ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan active deployment id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active deployments: %w", err)
	}
	return ids, nil
}

// GetLatestForApp returns the most recently created deployment for the given
// application, regardless of status.
func GetLatestForApp(ctx context.Context, state *db.DB, appID string) (string, error) {
	var id string
	err := state.QueryRowContext(ctx, `
		SELECT id FROM deployments
		WHERE application_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, appID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get latest deployment for app: %w", err)
	}
	return id, nil
}

// ListByApplication returns all deployments for an application, newest first.
func ListByApplication(ctx context.Context, state *db.DB, appID string) ([]*Deployment, error) {
	rows, err := state.QueryContext(ctx, `
		SELECT id, application_id, revision_commit_sha, trigger_type, status,
		       image_reference, container_id, started_at, finished_at, failure_reason, created_at
		FROM deployments
		WHERE application_id = ?
		ORDER BY created_at DESC
	`, appID)
	if err != nil {
		return nil, fmt.Errorf("list deployments by application: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []*Deployment
	for rows.Next() {
		item := &Deployment{}
		if err := rows.Scan(
			&item.ID, &item.ApplicationID, &item.RevisionCommitSHA, &item.TriggerType, &item.Status,
			&item.ImageReference, &item.ContainerID, &item.StartedAt, &item.FinishedAt, &item.FailureReason, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deployments: %w", err)
	}
	return items, nil
}

// GetByApplicationStatus returns the most recently created deployment for app
// with status. Returns ErrNotFound if there are none.
func GetByApplicationStatus(ctx context.Context, state *db.DB, appID, status string) (*Deployment, error) {
	item := &Deployment{}
	err := state.QueryRowContext(ctx, `
		SELECT id, application_id, revision_commit_sha, trigger_type, status,
		       image_reference, started_at, finished_at, failure_reason, created_at, container_id
		FROM deployments
		WHERE application_id = ? AND status = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, appID, status).Scan(
		&item.ID, &item.ApplicationID, &item.RevisionCommitSHA, &item.TriggerType, &item.Status,
		&item.ImageReference, &item.StartedAt, &item.FinishedAt, &item.FailureReason, &item.CreatedAt,
		&item.ContainerID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment by app status: %w", err)
	}
	return item, nil
}

// SetContainerID records the container ID created for a deployment.
func SetContainerID(ctx context.Context, state *db.DB, deploymentID, containerID string) error {
	if deploymentID == "" || containerID == "" {
		return fmt.Errorf("deployment ID and container ID must not be empty")
	}
	res, err := state.ExecContext(ctx, `
		UPDATE deployments SET container_id = ? WHERE id = ?
	`, containerID, deploymentID)
	if err != nil {
		return fmt.Errorf("set container id: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set container id: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("set container id: no row updated")
	}
	return nil
}

// SetCurrentDeployment updates the application's current deployment pointer and
// updates the application's updated_at timestamp.
func SetCurrentDeployment(ctx context.Context, state *db.DB, appID, deploymentID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := state.ExecContext(ctx, `
		UPDATE applications
		SET current_deployment_id = ?, updated_at = ?
		WHERE id = ?
	`, deploymentID, now, appID)
	if err != nil {
		return fmt.Errorf("set current deployment: %w", err)
	}
	return nil
}
