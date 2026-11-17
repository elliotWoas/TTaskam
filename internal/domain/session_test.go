package domain_test

import (
	"testing"
	"time"
	"ttaskam/internal/domain"
)

func TestSession_LifecycleAndVersion(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	session := domain.NewFocusSession(123, "task-1", t0)

	if session.Status != domain.SessionStatusActive {
		t.Fatalf("expected status ACTIVE, got %s", session.Status)
	}
	if session.Version != 1 {
		t.Fatalf("expected initial version 1, got %d", session.Version)
	}
	if !session.IsOpen() {
		t.Fatalf("session should be open")
	}

	// 1. Pause after 100 seconds
	tPause := t0.Add(100 * time.Second)
	if err := session.Pause(1, tPause); err != nil {
		t.Fatalf("unexpected error pausing: %v", err)
	}
	if session.Status != domain.SessionStatusPaused {
		t.Errorf("expected status PAUSED, got %s", session.Status)
	}
	if session.Version != 2 {
		t.Errorf("expected version 2, got %d", session.Version)
	}
	if session.TotalActiveSec != 100 {
		t.Errorf("expected 100 total active seconds, got %d", session.TotalActiveSec)
	}
	if !session.IsOpen() {
		t.Errorf("session in PAUSED must remain open (slot reserved)")
	}

	// 2. Reject stale callback version
	if err := session.Resume(1, tPause.Add(10*time.Second)); err != domain.ErrVersionMismatch {
		t.Errorf("expected ErrVersionMismatch, got %v", err)
	}

	// 3. Resume with correct version (2)
	tResume := tPause.Add(50 * time.Second)
	if err := session.Resume(2, tResume); err != nil {
		t.Fatalf("unexpected error resuming: %v", err)
	}
	if session.Status != domain.SessionStatusActive {
		t.Errorf("expected status ACTIVE, got %s", session.Status)
	}
	if session.Version != 3 {
		t.Errorf("expected version 3, got %d", session.Version)
	}

	// 4. End session after another 200 seconds of active work
	tEnd := tResume.Add(200 * time.Second)
	if err := session.End(3, tEnd); err != nil {
		t.Fatalf("unexpected error ending: %v", err)
	}
	if session.Status != domain.SessionStatusEnded {
		t.Errorf("expected status ENDED, got %s", session.Status)
	}
	if session.Version != 4 {
		t.Errorf("expected version 4, got %d", session.Version)
	}
	if session.TotalActiveSec != 300 {
		t.Errorf("expected 300 total active seconds (100 + 200), got %d", session.TotalActiveSec)
	}
	// Slot immediately released!
	if session.IsOpen() {
		t.Errorf("ended session must NOT be open")
	}

	// 5. Attach optional exit reason
	if err := session.SetExitReason(domain.ExitReasonDistracted, tEnd); err != nil {
		t.Errorf("unexpected error setting exit reason: %v", err)
	}
	if session.ExitReason != domain.ExitReasonDistracted {
		t.Errorf("expected exit reason DISTRACTED, got %s", session.ExitReason)
	}
}

func TestSession_CannotSetExitReasonWhileActive(t *testing.T) {
	now := time.Now().UTC()
	session := domain.NewFocusSession(123, "task-1", now)

	// Attempt to set exit reason on ACTIVE session
	if err := session.SetExitReason(domain.ExitReasonTired, now); err != domain.ErrInvalidSessionState {
		t.Errorf("expected ErrInvalidSessionState, got: %v", err)
	}
}

func TestSession_CannotCompleteAlreadyEnded(t *testing.T) {
	now := time.Now().UTC()
	session := domain.NewFocusSession(123, "task-1", now)
	_ = session.End(1, now)

	if err := session.Complete(2, now); err != domain.ErrInvalidSessionState {
		t.Errorf("expected ErrInvalidSessionState, got: %v", err)
	}
}
