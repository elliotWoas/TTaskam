package domain

import (
	"strings"
	"time"
)

type SessionStatus string

const (
	SessionStatusActive    SessionStatus = "ACTIVE"
	SessionStatusPaused    SessionStatus = "PAUSED"
	SessionStatusEnded     SessionStatus = "ENDED"
	SessionStatusCompleted SessionStatus = "COMPLETED"
)

// Predefined exit reason codes (optional, non-blocking)
const (
	ExitReasonDistracted = "DISTRACTED"
	ExitReasonBlocked    = "BLOCKED"
	ExitReasonTired      = "TIRED"
	ExitReasonDoneForNow = "DONE_FOR_NOW"
	ExitReasonSkipped    = "SKIPPED"
)

type FocusSession struct {
	ID             string        `json:"id" bson:"_id,omitempty"`
	UserID         int64         `json:"user_id" bson:"user_id"`
	TaskID         string        `json:"task_id" bson:"task_id"`
	Status         SessionStatus `json:"status" bson:"status"`
	StartedAt      time.Time     `json:"started_at" bson:"started_at"`
	PausedAt       *time.Time    `json:"paused_at,omitempty" bson:"paused_at,omitempty"`
	EndedAt        *time.Time    `json:"ended_at,omitempty" bson:"ended_at,omitempty"`
	TotalActiveSec int64         `json:"total_active_sec" bson:"total_active_sec"`
	Version        int           `json:"version" bson:"version"`
	ExitReason     string        `json:"exit_reason,omitempty" bson:"exit_reason,omitempty"`
	CreatedAt      time.Time     `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at" bson:"updated_at"`
}

// NewFocusSession creates an initial session in ACTIVE status at Version 1.
func NewFocusSession(userID int64, taskID string, now time.Time) *FocusSession {
	nowUTC := now.UTC()
	return &FocusSession{
		UserID:         userID,
		TaskID:         taskID,
		Status:         SessionStatusActive,
		StartedAt:      nowUTC,
		TotalActiveSec: 0,
		Version:        1,
		CreatedAt:      nowUTC,
		UpdatedAt:      nowUTC,
	}
}

// IsOpen returns true if the session is currently holding the single focus tunnel slot (ACTIVE or PAUSED).
func (s *FocusSession) IsOpen() bool {
	return s.Status == SessionStatusActive || s.Status == SessionStatusPaused
}

// Pause transitions an ACTIVE session to PAUSED.
// It verifies the expected version and accumulates active elapsed time.
func (s *FocusSession) Pause(expectedVersion int, now time.Time) error {
	if s.Version != expectedVersion {
		return ErrVersionMismatch
	}
	if s.Status != SessionStatusActive {
		return ErrInvalidSessionState
	}

	nowUTC := now.UTC()
	elapsed := int64(nowUTC.Sub(s.StartedAt).Seconds())
	if elapsed > 0 {
		s.TotalActiveSec += elapsed
	}

	s.Status = SessionStatusPaused
	s.PausedAt = &nowUTC
	s.Version++
	s.UpdatedAt = nowUTC
	return nil
}

// Resume transitions a PAUSED session back to ACTIVE.
// It verifies the expected version and resets the StartedAt timer checkpoint.
func (s *FocusSession) Resume(expectedVersion int, now time.Time) error {
	if s.Version != expectedVersion {
		return ErrVersionMismatch
	}
	if s.Status != SessionStatusPaused {
		return ErrInvalidSessionState
	}

	nowUTC := now.UTC()
	s.Status = SessionStatusActive
	s.StartedAt = nowUTC
	s.PausedAt = nil
	s.Version++
	s.UpdatedAt = nowUTC
	return nil
}

// End transitions an ACTIVE or PAUSED session to ENDED.
// This immediately releases the user's focus tunnel slot.
// Does NOT complete the task.
func (s *FocusSession) End(expectedVersion int, now time.Time) error {
	if s.Version != expectedVersion {
		return ErrVersionMismatch
	}
	if !s.IsOpen() {
		return ErrInvalidSessionState
	}

	nowUTC := now.UTC()
	if s.Status == SessionStatusActive {
		elapsed := int64(nowUTC.Sub(s.StartedAt).Seconds())
		if elapsed > 0 {
			s.TotalActiveSec += elapsed
		}
	}

	s.Status = SessionStatusEnded
	s.EndedAt = &nowUTC
	s.Version++
	s.UpdatedAt = nowUTC
	return nil
}

// Complete transitions an open session to COMPLETED upon task completion.
func (s *FocusSession) Complete(expectedVersion int, now time.Time) error {
	if s.Version != expectedVersion {
		return ErrVersionMismatch
	}
	if !s.IsOpen() {
		return ErrInvalidSessionState
	}

	nowUTC := now.UTC()
	if s.Status == SessionStatusActive {
		elapsed := int64(nowUTC.Sub(s.StartedAt).Seconds())
		if elapsed > 0 {
			s.TotalActiveSec += elapsed
		}
	}

	s.Status = SessionStatusCompleted
	s.EndedAt = &nowUTC
	s.Version++
	s.UpdatedAt = nowUTC
	return nil
}

// SetExitReason records an optional reason on an already-ended session.
func (s *FocusSession) SetExitReason(reason string, now time.Time) error {
	if s.Status != SessionStatusEnded {
		return ErrInvalidSessionState
	}
	s.ExitReason = strings.TrimSpace(reason)
	s.UpdatedAt = now.UTC()
	return nil
}
