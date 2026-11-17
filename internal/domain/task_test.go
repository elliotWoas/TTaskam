package domain_test

import (
	"testing"
	"time"
	"ttaskam/internal/domain"
)

func TestTask_NewTask(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	// Valid task
	task, err := domain.NewTask(123, "Write report", "Open document and write title", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Status != domain.TaskStatusCreated {
		t.Errorf("expected status CREATED, got %s", task.Status)
	}
	if task.ExpiresAt != now.Add(72*time.Hour) {
		t.Errorf("expected expires_at %v, got %v", now.Add(72*time.Hour), task.ExpiresAt)
	}

	// Empty title rejected
	_, err = domain.NewTask(123, "   ", "Open doc", now)
	if err != domain.ErrEmptyTitle {
		t.Errorf("expected ErrEmptyTitle, got %v", err)
	}
}

func TestTask_ExpiryExactCutoff(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	task, err := domain.NewTask(123, "Write report", "Open doc", t0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1 second before 72h: Not expired
	justBefore := t0.Add(72*time.Hour - time.Second)
	if task.IsExpired(justBefore) {
		t.Errorf("task should NOT be expired 1 second before 72 hours")
	}
	if err := task.CanStart(justBefore); err != nil {
		t.Errorf("task should be able to start before 72 hours, got: %v", err)
	}

	// Exactly 72h: Expired (now >= expires_at)
	exactCutoff := t0.Add(72 * time.Hour)
	if !task.IsExpired(exactCutoff) {
		t.Errorf("task MUST be expired at exactly 72 hours (now >= expires_at)")
	}
	if err := task.CanStart(exactCutoff); err != domain.ErrTaskExpired {
		t.Errorf("expected ErrTaskExpired at exact cutoff, got: %v", err)
	}

	// 1 second after 72h: Expired
	justAfter := t0.Add(72*time.Hour + time.Second)
	if !task.IsExpired(justAfter) {
		t.Errorf("task MUST be expired after 72 hours")
	}
	if err := task.CanStart(justAfter); err != domain.ErrTaskExpired {
		t.Errorf("expected ErrTaskExpired, got: %v", err)
	}
}

func TestTask_StartedExemptionFromDecay(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	task, err := domain.NewTask(123, "Write report", "Open doc", t0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Start task at T0 + 1 hour
	tStart := t0.Add(time.Hour)
	if err := task.MarkStarted(tStart); err != nil {
		t.Fatalf("unexpected error marking started: %v", err)
	}
	if task.Status != domain.TaskStatusStarted {
		t.Fatalf("expected status STARTED, got %s", task.Status)
	}

	// Check at T0 + 100 hours (well past 72h)
	tFarFuture := t0.Add(100 * time.Hour)

	// Invariant: 72-hour decay applies ONLY to never-started tasks.
	if task.IsExpired(tFarFuture) {
		t.Errorf("started task must NOT expire under the 72h decay rule")
	}

	// Invariant: A previously started task can still be started again after 72 hours
	if err := task.CanStart(tFarFuture); err != nil {
		t.Errorf("previously started task should be able to start after 72h, got: %v", err)
	}
}

func TestTask_CompletedAndArchivedCannotStart(t *testing.T) {
	now := time.Now().UTC()
	task, _ := domain.NewTask(123, "Test", "Step", now)

	// Completed task cannot start
	_ = task.MarkCompleted(now)
	if err := task.CanStart(now); err != domain.ErrInvalidTaskState {
		t.Errorf("expected ErrInvalidTaskState for completed task, got: %v", err)
	}

	// Archived task cannot start
	task2, _ := domain.NewTask(123, "Test 2", "Step", now)
	_ = task2.Archive(now)
	if err := task2.CanStart(now); err != domain.ErrInvalidTaskState {
		t.Errorf("expected ErrInvalidTaskState for archived task, got: %v", err)
	}
}

func TestTask_MicroStartValidation(t *testing.T) {
	now := time.Now().UTC()
	// Task without micro-start cannot start
	task, _ := domain.NewTask(123, "Test", "", now)
	if err := task.CanStart(now); err != domain.ErrEmptyMicroStart {
		t.Errorf("expected ErrEmptyMicroStart, got: %v", err)
	}

	// Setting valid micro-start enables start
	if err := task.SetMicroStart("Open browser to localhost", now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := task.CanStart(now); err != nil {
		t.Errorf("expected task to be startable, got: %v", err)
	}

	// Setting empty micro-start rejected
	if err := task.SetMicroStart("   ", now); err != domain.ErrEmptyMicroStart {
		t.Errorf("expected ErrEmptyMicroStart, got: %v", err)
	}
}
