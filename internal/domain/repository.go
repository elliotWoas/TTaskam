package domain

import (
	"context"
	"time"
)

type TaskRepository interface {
	// Create persists a new task. If an idempotencyKey is non-empty and has already been processed for this user,
	// it returns the previously created task without creating a duplicate.
	// Task and idempotency records must be atomically coupled.
	Create(ctx context.Context, task *Task, idempotencyKey string) (*Task, error)
	GetByID(ctx context.Context, id string, userID int64) (*Task, error)
	Update(ctx context.Context, task *Task) error
	ArchiveExpired(ctx context.Context, userID int64, now time.Time) (int64, error)
	ListActive(ctx context.Context, userID int64) ([]*Task, error)
	GetByTimeRange(ctx context.Context, userID int64, start, end time.Time) ([]*Task, error)
}

type SessionRepository interface {
	GetOpenSession(ctx context.Context, userID int64) (*FocusSession, error)
	GetSessionByID(ctx context.Context, id string, userID int64) (*FocusSession, error)
	SetExitReason(ctx context.Context, sessionID string, userID int64, reason string, now time.Time) error
}

type FocusTransactionManager interface {
	// StartFocusAtomic atomically verifies that:
	// 1. User has no open focus session (status ACTIVE or PAUSED).
	// 2. Task exists, belongs to user, has a valid micro-start, is not completed/archived, and is not expired (now < expires_at if unstarted).
	// 3. Transitions task to STARTED (if CREATED) and creates a new ACTIVE session.
	StartFocusAtomic(ctx context.Context, userID int64, taskID string, now time.Time) (*FocusSession, *Task, error)

	// CompleteFocusAtomic atomically:
	// 1. Closes open session matching expected sessionID and version (status -> COMPLETED).
	// 2. Marks associated task COMPLETED.
	CompleteFocusAtomic(ctx context.Context, userID int64, sessionID string, expectedVersion int, now time.Time) (*FocusSession, *Task, error)

	// PauseFocus transitions ACTIVE session to PAUSED with expected version check.
	PauseFocus(ctx context.Context, userID int64, sessionID string, expectedVersion int, now time.Time) (*FocusSession, error)

	// ResumeFocus transitions PAUSED session to ACTIVE with expected version check.
	ResumeFocus(ctx context.Context, userID int64, sessionID string, expectedVersion int, now time.Time) (*FocusSession, error)

	// EndFocus transitions ACTIVE or PAUSED session to ENDED with expected version check.
	// Immediately releases the focus slot without completing the task.
	EndFocus(ctx context.Context, userID int64, sessionID string, expectedVersion int, now time.Time) (*FocusSession, error)
}
