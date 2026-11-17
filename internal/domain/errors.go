package domain

import "errors"

var (
	// Task errors
	ErrTaskNotFound     = errors.New("task not found")
	ErrTaskExpired      = errors.New("task expired after 72 hours unstarted")
	ErrInvalidTaskState = errors.New("invalid task state for operation")
	ErrEmptyTitle       = errors.New("task title cannot be empty")
	ErrEmptyMicroStart  = errors.New("micro-start action cannot be empty")
	ErrDuplicateTask    = errors.New("duplicate task creation")

	// Session errors
	ErrFocusTunnelActive    = errors.New("focus tunnel active: existing open session in progress")
	ErrNoActiveSession      = errors.New("no active or open focus session found")
	ErrInvalidSessionState  = errors.New("invalid session state for operation")
	ErrVersionMismatch      = errors.New("session version mismatch: stale action")
	ErrSessionAlreadyClosed = errors.New("focus session is already closed")

	// Common errors
	ErrUnauthorized        = errors.New("unauthorized access to resource")
	ErrTransactionAborted  = errors.New("transaction aborted due to condition failure")
	ErrDatabaseUnavailable = errors.New("database is temporarily unavailable")
)
