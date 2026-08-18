package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/SpritexAI/SpritexDock/internal/db"
)

const (
	StatusQueued    = "queued"
	StatusBuilding  = "building"
	StatusDeploying = "deploying"
	StatusRunning   = "running"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

var (
	ErrNotFound          = errors.New("deployment not found")
	ErrInvalidTransition = errors.New("invalid deployment transition")
)

// Deployment is the durable state for one build/deployment attempt.
type Deployment struct {
	ID                string  `json:"id"`
	ApplicationID     string  `json:"application_id"`
	RevisionCommitSHA string  `json:"revision_commit_sha"`
	TriggerType       string  `json:"trigger_type"`
	Status            string  `json:"status"`
	ImageReference    *string `json:"image_reference,omitempty"`
	ContainerID       *string `json:"container_id,omitempty"`
	StartedAt         *string `json:"started_at,omitempty"`
	FinishedAt        *string `json:"finished_at,omitempty"`
	FailureReason     *string `json:"failure_reason,omitempty"`
	CreatedAt         string  `json:"created_at"`
}

// CreateQueued persists a new deployment request without starting work.
func CreateQueued(ctx context.Context, state *db.DB, applicationID, revisionSHA, triggerType string) (*Deployment, error) {
	if strings.TrimSpace(applicationID) == "" || strings.TrimSpace(revisionSHA) == "" || strings.TrimSpace(triggerType) == "" {
		return nil, fmt.Errorf("application, revision, and trigger type are required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	item := &Deployment{
		ID:                uuid.NewString(),
		ApplicationID:     applicationID,
		RevisionCommitSHA: revisionSHA,
		TriggerType:       triggerType,
		Status:            StatusQueued,
		CreatedAt:         now,
	}
	_, err := state.ExecContext(ctx, `
		INSERT INTO deployments
		(id, application_id, revision_commit_sha, trigger_type, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, item.ID, item.ApplicationID, item.RevisionCommitSHA, item.TriggerType, item.Status, item.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create deployment: %w", err)
	}
	return item, nil
}

// Get returns one deployment by ID.
func Get(ctx context.Context, state *db.DB, id string) (*Deployment, error) {
	item := &Deployment{}
	err := state.QueryRowContext(ctx, `
		SELECT id, application_id, revision_commit_sha, trigger_type, status,
		       image_reference, container_id, started_at, finished_at, failure_reason, created_at
		FROM deployments WHERE id = ?
	`, id).Scan(
		&item.ID, &item.ApplicationID, &item.RevisionCommitSHA, &item.TriggerType, &item.Status,
		&item.ImageReference, &item.ContainerID, &item.StartedAt, &item.FinishedAt, &item.FailureReason, &item.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get deployment: %w", err)
	}
	return item, nil
}

// Transition applies one legal lifecycle transition and records its metadata.
func Transition(ctx context.Context, state *db.DB, id, nextStatus, imageReference, failureReason string) error {
	if id == "" || !validStatus(nextStatus) {
		return ErrInvalidTransition
	}
	current, err := Get(ctx, state, id)
	if err != nil {
		return err
	}
	if !allowedTransition(current.Status, nextStatus) {
		return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, current.Status, nextStatus)
	}
	if nextStatus == StatusDeploying && strings.TrimSpace(imageReference) == "" {
		return fmt.Errorf("%w: deploying requires an image reference", ErrInvalidTransition)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var query string
	var args []any
	switch nextStatus {
	case StatusBuilding:
		query = "UPDATE deployments SET status = ?, started_at = ? WHERE id = ? AND status = ?"
		args = []any{nextStatus, now, id, current.Status}
	case StatusFailed, StatusCancelled:
		query = "UPDATE deployments SET status = ?, finished_at = ?, failure_reason = ? WHERE id = ? AND status = ?"
		args = []any{nextStatus, now, sanitizeReason(failureReason), id, current.Status}
	case StatusDeploying:
		query = "UPDATE deployments SET status = ?, image_reference = ? WHERE id = ? AND status = ?"
		args = []any{nextStatus, imageReference, id, current.Status}
	case StatusRunning:
		query = "UPDATE deployments SET status = ?, finished_at = ? WHERE id = ? AND status = ?"
		args = []any{nextStatus, now, id, current.Status}
	default:
		return ErrInvalidTransition
	}
	result, err := state.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("transition deployment: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("transition deployment: %w", err)
	}
	if count != 1 {
		return ErrInvalidTransition
	}
	return nil
}

// RecoverInterrupted marks work that cannot survive a process restart as failed.
func RecoverInterrupted(ctx context.Context, state *db.DB) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := state.ExecContext(ctx, `
		UPDATE deployments
		SET status = ?, finished_at = ?, failure_reason = ?
		WHERE status IN (?, ?)
	`, StatusFailed, now, "deployment interrupted by control-plane restart", StatusQueued, StatusBuilding)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted deployments: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("recover interrupted deployments: %w", err)
	}
	return count, nil
}

func validStatus(status string) bool {
	switch status {
	case StatusQueued, StatusBuilding, StatusDeploying, StatusRunning, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

func allowedTransition(current, next string) bool {
	switch current {
	case StatusQueued:
		return next == StatusBuilding || next == StatusCancelled
	case StatusBuilding:
		return next == StatusDeploying || next == StatusFailed || next == StatusCancelled
	case StatusDeploying:
		return next == StatusRunning || next == StatusFailed || next == StatusCancelled
	default:
		return false
	}
}

// IsNotFound reports whether err is ErrNotFound.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func sanitizeReason(reason string) string {
	reason = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' || r >= 0x20 {
			return r
		}
		return -1
	}, strings.TrimSpace(reason))
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	return reason
}
