package service

import (
	"context"
	"time"
	"ttaskam/internal/domain"
)

type FocusService struct {
	txManager domain.FocusTransactionManager
	sessions  domain.SessionRepository
	tasks     domain.TaskRepository
}

func NewFocusService(
	txManager domain.FocusTransactionManager,
	sessions domain.SessionRepository,
	tasks domain.TaskRepository,
) *FocusService {
	return &FocusService{
		txManager: txManager,
		sessions:  sessions,
		tasks:     tasks,
	}
}

// Start initiates a focus session for the specified task atomically.
// Verifies no other session is open, task is eligible, valid micro-start present, and task unexpired.
func (s *FocusService) Start(
	ctx context.Context,
	userID int64,
	taskID string,
	now time.Time,
) (*domain.FocusSession, *domain.Task, error) {
	return s.txManager.StartFocusAtomic(ctx, userID, taskID, now)
}

// Pause pauses an active focus session.
func (s *FocusService) Pause(
	ctx context.Context,
	userID int64,
	sessionID string,
	expectedVersion int,
	now time.Time,
) (*domain.FocusSession, error) {
	return s.txManager.PauseFocus(ctx, userID, sessionID, expectedVersion, now)
}

// Resume resumes a paused focus session.
func (s *FocusService) Resume(
	ctx context.Context,
	userID int64,
	sessionID string,
	expectedVersion int,
	now time.Time,
) (*domain.FocusSession, error) {
	return s.txManager.ResumeFocus(ctx, userID, sessionID, expectedVersion, now)
}

// End deliberately ends an open focus session.
// CRITICAL: Immediately releases the focus slot without completing the task.
func (s *FocusService) End(
	ctx context.Context,
	userID int64,
	sessionID string,
	expectedVersion int,
	now time.Time,
) (*domain.FocusSession, error) {
	return s.txManager.EndFocus(ctx, userID, sessionID, expectedVersion, now)
}

// Done atomically marks the open focus session and its associated task as COMPLETED.
// If there is no open session, returns domain.ErrNoActiveSession.
func (s *FocusService) Done(
	ctx context.Context,
	userID int64,
	sessionID string,
	expectedVersion int,
	now time.Time,
) (*domain.FocusSession, *domain.Task, error) {
	if sessionID == "" {
		openSession, err := s.sessions.GetOpenSession(ctx, userID)
		if err != nil {
			return nil, nil, err
		}
		if openSession == nil || !openSession.IsOpen() {
			return nil, nil, domain.ErrNoActiveSession
		}
		sessionID = openSession.ID
		expectedVersion = openSession.Version
	}
	return s.txManager.CompleteFocusAtomic(ctx, userID, sessionID, expectedVersion, now)
}

// RecordExitReason attaches an optional predefined exit reason code to an ended session.
// Never blocks task creation or normal commands. Fails safely if session does not exist.
func (s *FocusService) RecordExitReason(
	ctx context.Context,
	userID int64,
	sessionID string,
	reason string,
	now time.Time,
) error {
	return s.sessions.SetExitReason(ctx, sessionID, userID, reason, now)
}

// GetStatus returns the currently open session and associated task, if any.
func (s *FocusService) GetStatus(
	ctx context.Context,
	userID int64,
) (*domain.FocusSession, *domain.Task, error) {
	session, err := s.sessions.GetOpenSession(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if session == nil || !session.IsOpen() {
		return nil, nil, nil
	}

	task, err := s.tasks.GetByID(ctx, session.TaskID, userID)
	if err != nil {
		return session, nil, err
	}
	return session, task, nil
}
