package service_test

import (
	"context"
	"testing"
	"time"

	"ttaskam/internal/adapter/memory"
	"ttaskam/internal/domain"
	"ttaskam/internal/service"
)

func setupFocusTest() (*service.FocusService, *service.TaskService, *memory.MemoryRepo) {
	repo := memory.NewMemoryRepo()
	focusSvc := service.NewFocusService(repo, repo, repo)
	taskSvc := service.NewTaskService(repo, repo, nil)
	return focusSvc, taskSvc, repo
}

// AT-09: Partial-write failure test
// Invariant: Keep Start and Done atomic across task and session writes.
// If a write fails before commit, no partial state leaks.
func TestFocusService_AtomicStart_RollbackOnFailure(t *testing.T) {
	ctx := context.Background()
	focusSvc, taskSvc, repo := setupFocusTest()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	// Create task
	task, err := taskSvc.CreateTask(ctx, 100, "Prepare presentation", "Open slide 1", "key-1", now)
	if err != nil {
		t.Fatalf("unexpected error creating task: %v", err)
	}

	// Inject transaction failure
	repo.SimulateFail = true

	// Attempt start: must fail atomically
	_, _, err = focusSvc.Start(ctx, 100, task.ID, now)
	if err != domain.ErrTransactionAborted {
		t.Fatalf("expected ErrTransactionAborted, got: %v", err)
	}

	// Verify task remains strictly CREATED (no partial update to STARTED)
	savedTask, err := repo.GetByID(ctx, task.ID, 100)
	if err != nil {
		t.Fatalf("unexpected error fetching task: %v", err)
	}
	if savedTask.Status != domain.TaskStatusCreated {
		t.Errorf("task status must remain CREATED after abort, got: %s", savedTask.Status)
	}
	if savedTask.StartedAt != nil {
		t.Errorf("started_at must be nil after abort")
	}

	// Verify no session exists
	openSession, err := repo.GetOpenSession(ctx, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if openSession != nil {
		t.Errorf("no focus session should exist after abort")
	}
}

// AT-10 & Refinement: 72-hour exact cutoff & Started exemption
func TestFocusService_ExpiryCutoffAndStartedExemption(t *testing.T) {
	ctx := context.Background()
	focusSvc, taskSvc, _ := setupFocusTest()
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	// Task 1: Never started
	task1, _ := taskSvc.CreateTask(ctx, 100, "Unstarted Task", "First step", "k1", t0)

	// Attempt start at exactly 72h: must fail
	exactCutoff := t0.Add(72 * time.Hour)
	_, _, err := focusSvc.Start(ctx, 100, task1.ID, exactCutoff)
	if err != domain.ErrTaskExpired {
		t.Fatalf("expected ErrTaskExpired at exact 72h cutoff, got: %v", err)
	}

	// Task 2: Started at T0 + 1h, then session ended
	task2, _ := taskSvc.CreateTask(ctx, 200, "Started Task", "First step", "k2", t0)
	sess, t2Started, err := focusSvc.Start(ctx, 200, task2.ID, t0.Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error starting task: %v", err)
	}
	if t2Started.Status != domain.TaskStatusStarted {
		t.Fatalf("expected status STARTED, got %s", t2Started.Status)
	}

	// End focus session (task remains STARTED)
	_, err = focusSvc.End(ctx, 200, sess.ID, sess.Version, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error ending session: %v", err)
	}

	// At T0 + 100 hours (well past 72h): Invariant: previously started task CAN still be started again
	t100 := t0.Add(100 * time.Hour)
	sess2, t2Resumed, err := focusSvc.Start(ctx, 200, task2.ID, t100)
	if err != nil {
		t.Fatalf("expected previously started task to start successfully after 72h, got: %v", err)
	}
	if sess2.Status != domain.SessionStatusActive {
		t.Errorf("expected session to be ACTIVE, got %s", sess2.Status)
	}
	if t2Resumed.Status != domain.TaskStatusStarted {
		t.Errorf("expected task to remain STARTED, got %s", t2Resumed.Status)
	}
}

// AT-12: Expected session ID and version in callback-driven writes
func TestFocusService_VersionAndStaleButtonGuards(t *testing.T) {
	ctx := context.Background()
	focusSvc, taskSvc, _ := setupFocusTest()
	now := time.Now().UTC()

	// 1. Start Session 1 on Task 1
	task1, _ := taskSvc.CreateTask(ctx, 100, "Task 1", "Step 1", "k1", now)
	sess1, _, err := focusSvc.Start(ctx, 100, task1.ID, now)
	if err != nil {
		t.Fatalf("unexpected error starting session 1: %v", err)
	}

	// 2. End Session 1
	_, err = focusSvc.End(ctx, 100, sess1.ID, sess1.Version, now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("unexpected error ending session 1: %v", err)
	}

	// 3. Start Session 2 on Task 2
	task2, _ := taskSvc.CreateTask(ctx, 100, "Task 2", "Step 2", "k2", now.Add(15*time.Minute))
	sess2, _, err := focusSvc.Start(ctx, 100, task2.ID, now.Add(15*time.Minute))
	if err != nil {
		t.Fatalf("unexpected error starting session 2: %v", err)
	}

	// 4. Stale callback: User clicks [Pause] from Session 1 (sess1, ver: 1)
	_, err = focusSvc.Pause(ctx, 100, sess1.ID, 1, now.Add(20*time.Minute))
	if err != domain.ErrNoActiveSession && err != domain.ErrVersionMismatch {
		t.Errorf("expected rejection for stale session 1, got: %v", err)
	}

	// Verify Session 2 is untouched and still ACTIVE
	statusSess, statusTask, err := focusSvc.GetStatus(ctx, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statusSess.ID != sess2.ID || statusSess.Status != domain.SessionStatusActive {
		t.Errorf("session 2 should remain ACTIVE, got ID %s, status %s", statusSess.ID, statusSess.Status)
	}
	if statusTask.ID != task2.ID {
		t.Errorf("active task should be task 2, got %s", statusTask.ID)
	}

	// 5. Version mismatch on Session 2: User sends wrong version
	_, err = focusSvc.Pause(ctx, 100, sess2.ID, 999, now.Add(25*time.Minute))
	if err != domain.ErrVersionMismatch {
		t.Errorf("expected ErrVersionMismatch, got: %v", err)
	}
}

// AT-14: Ending session releases slot immediately & optional exit feedback never blocks next task
func TestFocusService_EndReleasesSlot_ExitReasonNeverBlocks(t *testing.T) {
	ctx := context.Background()
	focusSvc, taskSvc, repo := setupFocusTest()
	now := time.Now().UTC()

	// 1. Create and start Task 1
	task1, _ := taskSvc.CreateTask(ctx, 100, "Task 1", "Step 1", "k1", now)
	sess, _, _ := focusSvc.Start(ctx, 100, task1.ID, now)

	// 2. End session deliberately
	endedSess, err := focusSvc.End(ctx, 100, sess.ID, sess.Version, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("unexpected error ending session: %v", err)
	}

	// Invariant: Slot is immediately released
	openSess, err := repo.GetOpenSession(ctx, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if openSess != nil {
		t.Fatalf("expected no open session after End, got: %v", openSess)
	}

	// Invariant: User can IMMEDIATELY create and start a new task without answering exit reason
	task2, err := taskSvc.CreateTask(ctx, 100, "Task 2", "Step 2", "k2", now.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("creating new task must NOT be blocked after end: %v", err)
	}

	sess2, _, err := focusSvc.Start(ctx, 100, task2.ID, now.Add(7*time.Minute))
	if err != nil {
		t.Fatalf("starting new task must NOT be blocked: %v", err)
	}
	if sess2.Status != domain.SessionStatusActive {
		t.Errorf("expected new session to be ACTIVE, got %s", sess2.Status)
	}

	// Invariant: Optional exit reason can be safely recorded on the first session later without affecting current session
	err = focusSvc.RecordExitReason(ctx, 100, endedSess.ID, domain.ExitReasonDistracted, now.Add(10*time.Minute))
	if err != nil {
		t.Errorf("unexpected error recording exit reason: %v", err)
	}

	savedSess1, _ := repo.GetSessionByID(ctx, endedSess.ID, 100)
	if savedSess1.ExitReason != domain.ExitReasonDistracted {
		t.Errorf("expected exit reason DISTRACTED, got: %s", savedSess1.ExitReason)
	}
}

// Done without open session does not affect previously ended tasks
func TestFocusService_DoneWithoutSession(t *testing.T) {
	ctx := context.Background()
	focusSvc, taskSvc, repo := setupFocusTest()
	now := time.Now().UTC()

	// Task 1 started and ended (NOT completed)
	task1, _ := taskSvc.CreateTask(ctx, 100, "Task 1", "Step 1", "k1", now)
	sess, _, _ := focusSvc.Start(ctx, 100, task1.ID, now)
	_, _ = focusSvc.End(ctx, 100, sess.ID, sess.Version, now.Add(5*time.Minute))

	// User calls /done when no open session exists
	_, _, err := focusSvc.Done(ctx, 100, "", 0, now.Add(10*time.Minute))
	if err != domain.ErrNoActiveSession {
		t.Fatalf("expected ErrNoActiveSession, got: %v", err)
	}

	// Verify task 1 remains STARTED (not COMPLETED)
	t1, _ := repo.GetByID(ctx, task1.ID, 100)
	if t1.Status != domain.TaskStatusStarted {
		t.Errorf("task 1 must remain STARTED, got: %s", t1.Status)
	}
}

// Focus Tunnel Guard: Only one open session allowed
func TestFocusService_OnlyOneOpenSession(t *testing.T) {
	ctx := context.Background()
	focusSvc, taskSvc, _ := setupFocusTest()
	now := time.Now().UTC()

	task1, _ := taskSvc.CreateTask(ctx, 100, "Task 1", "Step 1", "k1", now)
	task2, _ := taskSvc.CreateTask(ctx, 100, "Task 2", "Step 2", "k2", now)

	// Start task 1
	_, _, err := focusSvc.Start(ctx, 100, task1.ID, now)
	if err != nil {
		t.Fatalf("unexpected error starting task 1: %v", err)
	}

	// Attempt to start task 2 while task 1 is active: must fail
	_, _, err = focusSvc.Start(ctx, 100, task2.ID, now)
	if err != domain.ErrFocusTunnelActive {
		t.Fatalf("expected ErrFocusTunnelActive, got: %v", err)
	}

	// Attempt to create a new task while task 1 is active: must fail
	_, err = taskSvc.CreateTask(ctx, 100, "Task 3", "Step 3", "k3", now)
	if err != domain.ErrFocusTunnelActive {
		t.Fatalf("expected ErrFocusTunnelActive on task creation during focus, got: %v", err)
	}
}
