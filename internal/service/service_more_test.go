package service_test

import (
	"context"
	"testing"
	"time"

	"ttaskam/internal/domain"
)

func TestTaskService_ListActiveTasks_LazyDecay(t *testing.T) {
	ctx := context.Background()
	focusSvc, taskSvc, repo := setupFocusTest()
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	// Task 1: Unstarted, created at T0
	t1, err := taskSvc.CreateTask(ctx, 100, "Unstarted Task", "Step 1", "k1", t0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Task 2: Started at T0 + 1h, ended at T0 + 2h
	t2, _ := taskSvc.CreateTask(ctx, 100, "Started Task", "Step 2", "k2", t0)
	sess, _, _ := focusSvc.Start(ctx, 100, t2.ID, t0.Add(time.Hour))
	_, _ = focusSvc.End(ctx, 100, sess.ID, sess.Version, t0.Add(2*time.Hour))

	// Query at T0 + 73h (T1 should be archived lazily, T2 must remain active)
	now := t0.Add(73 * time.Hour)
	active, err := taskSvc.ListActiveTasks(ctx, 100, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(active) != 1 {
		t.Fatalf("expected exactly 1 active task (the started one), got %d", len(active))
	}
	if active[0].ID != t2.ID {
		t.Errorf("expected active task to be T2, got %s", active[0].ID)
	}

	// Check repo state of T1
	savedT1, _ := repo.GetByID(ctx, t1.ID, 100)
	if savedT1.Status != domain.TaskStatusArchived {
		t.Errorf("T1 must be archived in storage, got: %s", savedT1.Status)
	}
}

func TestFocusService_PauseAndResume(t *testing.T) {
	ctx := context.Background()
	focusSvc, taskSvc, _ := setupFocusTest()
	now := time.Now().UTC()

	task, _ := taskSvc.CreateTask(ctx, 100, "Task 1", "Step 1", "k1", now)
	sess, _, err := focusSvc.Start(ctx, 100, task.ID, now)
	if err != nil {
		t.Fatalf("unexpected error starting: %v", err)
	}

	// Pause
	paused, err := focusSvc.Pause(ctx, 100, sess.ID, sess.Version, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("unexpected error pausing: %v", err)
	}
	if paused.Status != domain.SessionStatusPaused {
		t.Errorf("expected status PAUSED, got %s", paused.Status)
	}

	// Resume
	resumed, err := focusSvc.Resume(ctx, 100, sess.ID, paused.Version, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("unexpected error resuming: %v", err)
	}
	if resumed.Status != domain.SessionStatusActive {
		t.Errorf("expected status ACTIVE, got %s", resumed.Status)
	}

	// Complete from active
	completedSess, completedTask, err := focusSvc.Done(ctx, 100, sess.ID, resumed.Version, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("unexpected error completing: %v", err)
	}
	if completedSess.Status != domain.SessionStatusCompleted {
		t.Errorf("expected session COMPLETED, got %s", completedSess.Status)
	}
	if completedTask.Status != domain.TaskStatusCompleted {
		t.Errorf("expected task COMPLETED, got %s", completedTask.Status)
	}
}
