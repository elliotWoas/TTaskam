package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ttaskam/internal/adapter/memory"
	"ttaskam/internal/domain"
	"ttaskam/internal/service"
)

type mockAISuggester struct {
	suggestion string
	err        error
}

func (m *mockAISuggester) SuggestMicroStart(ctx context.Context, intention string) (string, error) {
	return m.suggestion, m.err
}

func (m *mockAISuggester) Interpret(ctx context.Context, message string, uCtx domain.UserContext) (*domain.AIInterpretation, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &domain.AIInterpretation{
		Intent:              domain.IntentNewTask,
		SuggestedMicroStart: m.suggestion,
	}, nil
}

// AT-11: Duplicate creation after restart / idempotency
// Invariant: Make retries of task creation safe across restarts without allowing a deduplication record to exist without its task.
func TestTaskService_Idempotency(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewMemoryRepo()
	taskSvc := service.NewTaskService(repo, repo, nil)
	now := time.Now().UTC()

	// 1. Initial creation with idempotency key
	task1, err := taskSvc.CreateTask(ctx, 100, "Clean desk", "Throw away old papers", "update-1001", now)
	if err != nil {
		t.Fatalf("unexpected error creating task: %v", err)
	}

	// 2. Replay with the same idempotency key (simulating restart / network retry)
	task2, err := taskSvc.CreateTask(ctx, 100, "Clean desk", "Throw away old papers", "update-1001", now)
	if err != nil {
		t.Fatalf("unexpected error on retry: %v", err)
	}

	// Invariant: Returns existing task; no duplicate task created
	if task1.ID != task2.ID {
		t.Fatalf("expected identical task ID on idempotent replay, got %s and %s", task1.ID, task2.ID)
	}

	activeTasks, err := taskSvc.ListActiveTasks(ctx, 100, now)
	if err != nil {
		t.Fatalf("unexpected error listing tasks: %v", err)
	}
	if len(activeTasks) != 1 {
		t.Fatalf("expected exactly 1 task in active list, got %d", len(activeTasks))
	}
}

// Injected failure prevents dangling idempotency record
func TestTaskService_IdempotencyFailureAtomic(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewMemoryRepo()
	taskSvc := service.NewTaskService(repo, repo, nil)
	now := time.Now().UTC()

	repo.SimulateFail = true
	_, err := taskSvc.CreateTask(ctx, 100, "Clean desk", "Throw away old papers", "update-1002", now)
	if err == nil {
		t.Fatalf("expected failure when repo fails")
	}

	// Recover repo
	repo.SimulateFail = false

	// Retry creation: must succeed normally (no dangling idempotency record blocking it)
	task, err := taskSvc.CreateTask(ctx, 100, "Clean desk", "Throw away old papers", "update-1002", now)
	if err != nil {
		t.Fatalf("expected successful creation on retry after previous failure, got: %v", err)
	}
	if task == nil {
		t.Fatalf("expected valid task returned")
	}
}

// Micro-start editing on draft task
func TestTaskService_SetMicroStart(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewMemoryRepo()
	taskSvc := service.NewTaskService(repo, repo, nil)
	now := time.Now().UTC()

	task, _ := taskSvc.CreateTask(ctx, 100, "Write article", "", "k1", now)
	if task.MicroStart != "" {
		t.Errorf("expected empty micro-start initially")
	}

	updated, err := taskSvc.SetMicroStart(ctx, 100, task.ID, "Write opening paragraph", now)
	if err != nil {
		t.Fatalf("unexpected error setting micro-start: %v", err)
	}
	if updated.MicroStart != "Write opening paragraph" {
		t.Errorf("expected updated micro-start, got: %s", updated.MicroStart)
	}
}

// Optional AI Suggester interface testing
func TestTaskService_OptionalAI(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewMemoryRepo()

	// 1. With working AI
	ai := &mockAISuggester{suggestion: "Open editor and write outline"}
	svcWithAI := service.NewTaskService(repo, repo, ai)
	sug := svcWithAI.SuggestMicroStart(ctx, "Write book chapter")
	if sug != "Open editor and write outline" {
		t.Errorf("expected AI suggestion, got: %s", sug)
	}

	// 2. With failing AI (graceful fallback)
	failingAI := &mockAISuggester{err: errors.New("rate limited")}
	svcFailingAI := service.NewTaskService(repo, repo, failingAI)
	sugFail := svcFailingAI.SuggestMicroStart(ctx, "Write book chapter")
	if sugFail != "" {
		t.Errorf("expected empty string on AI failure, got: %s", sugFail)
	}

	// 3. Without AI (nil)
	svcNoAI := service.NewTaskService(repo, repo, nil)
	sugNil := svcNoAI.SuggestMicroStart(ctx, "Write book chapter")
	if sugNil != "" {
		t.Errorf("expected empty string when AI is nil, got: %s", sugNil)
	}
}

// Regression Test: Provider timeout, API error, malformed response, or disabled AI
// CANNOT silently turn arbitrary free text into a task.
// Explicit /new and focus commands must continue working without AI.
func TestTaskService_AIFallbackSafetyAndRegression(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	// 1. In Idle state: Provider timeout / deadline exceeded
	t.Run("Idle state on AI timeout must not create task", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		timeoutAI := &mockAISuggester{err: context.DeadlineExceeded}
		svc := service.NewTaskService(repo, repo, timeoutAI)

		for _, input := range []string{"ok", "wtf", "hello", "random thoughts"} {
			uCtx := domain.UserContext{State: domain.StateIdle}
			res, err := svc.ProcessUserMessage(ctx, 200, input, uCtx, "idemp-timeout-"+input, now)
			if err != nil {
				t.Fatalf("unexpected error processing message: %v", err)
			}
			if res.Task != nil {
				t.Errorf("expected NO task created on timeout for input '%s', but got task ID: %s", input, res.Task.ID)
			}
			if res.ActionTaken == "TASK_CREATED" {
				t.Errorf("expected non-creation action on timeout for input '%s', got: %s", input, res.ActionTaken)
			}
		}

		tasks, _ := svc.ListActiveTasks(ctx, 200, now)
		if len(tasks) != 0 {
			t.Errorf("expected 0 active tasks in repo, got %d", len(tasks))
		}
	})

	// 2. In Idle state: Provider API error (e.g. 500 error / rate limit)
	t.Run("Idle state on API error must not create task", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		failingAI := &mockAISuggester{err: errors.New("500 internal server error")}
		svc := service.NewTaskService(repo, repo, failingAI)

		uCtx := domain.UserContext{State: domain.StateIdle}
		res, err := svc.ProcessUserMessage(ctx, 200, "free text message", uCtx, "idemp-err", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Task != nil {
			t.Errorf("expected NO task created on API error, got task ID: %s", res.Task.ID)
		}

		tasks, _ := svc.ListActiveTasks(ctx, 200, now)
		if len(tasks) != 0 {
			t.Errorf("expected 0 active tasks in repo, got %d", len(tasks))
		}
	})

	// 3. In Idle state: AI disabled (nil)
	t.Run("Idle state with AI nil must not create task from free text", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		svc := service.NewTaskService(repo, repo, nil)

		uCtx := domain.UserContext{State: domain.StateIdle}
		res, err := svc.ProcessUserMessage(ctx, 200, "random chatter", uCtx, "idemp-nil", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Task != nil {
			t.Errorf("expected NO task created when AI is nil, got task ID: %s", res.Task.ID)
		}

		tasks, _ := svc.ListActiveTasks(ctx, 200, now)
		if len(tasks) != 0 {
			t.Errorf("expected 0 active tasks in repo, got %d", len(tasks))
		}
	})

	// 4. Explicit task creation (/new) works without AI
	t.Run("Explicit task creation works cleanly without AI", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		svc := service.NewTaskService(repo, repo, nil)

		task, err := svc.CreateTask(ctx, 200, "Draft status update", "", "cmd-new-1", now)
		if err != nil {
			t.Fatalf("unexpected error creating task via explicit command: %v", err)
		}
		if task == nil || task.Title != "Draft status update" {
			t.Fatalf("expected created task with title 'Draft status update'")
		}
		if task.Status != domain.TaskStatusCreated {
			t.Errorf("expected status TaskStatusCreated, got: %s", task.Status)
		}

		tasks, _ := svc.ListActiveTasks(ctx, 200, now)
		if len(tasks) != 1 {
			t.Fatalf("expected 1 active task, got %d", len(tasks))
		}
	})

	// 5. Focus commands work completely without AI
	t.Run("Focus lifecycle works completely without AI", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		focusSvc := service.NewFocusService(repo, repo, repo)
		taskSvc := service.NewTaskService(repo, repo, nil)

		// Create task explicitly and set micro-start
		task, _ := taskSvc.CreateTask(ctx, 200, "Draft status update", "", "cmd-new-2", now)
		_, err := taskSvc.SetMicroStart(ctx, 200, task.ID, "Open doc and write bullet 1", now)
		if err != nil {
			t.Fatalf("failed to set micro-start: %v", err)
		}

		// Start focus
		sess, _, err := focusSvc.Start(ctx, 200, task.ID, now)
		if err != nil {
			t.Fatalf("failed to start focus: %v", err)
		}
		if sess.Status != domain.SessionStatusActive {
			t.Errorf("expected session active, got: %s", sess.Status)
		}

		// Pause focus
		paused, err := focusSvc.Pause(ctx, 200, sess.ID, sess.Version, now.Add(10*time.Second))
		if err != nil {
			t.Fatalf("failed to pause focus: %v", err)
		}
		if paused.Status != domain.SessionStatusPaused {
			t.Errorf("expected session paused, got: %s", paused.Status)
		}

		// Resume focus
		resumed, err := focusSvc.Resume(ctx, 200, sess.ID, paused.Version, now.Add(20*time.Second))
		if err != nil {
			t.Fatalf("failed to resume focus: %v", err)
		}
		if resumed.Status != domain.SessionStatusActive {
			t.Errorf("expected session active, got: %s", resumed.Status)
		}

		// End focus (frees slot)
		ended, err := focusSvc.End(ctx, 200, sess.ID, resumed.Version, now.Add(30*time.Second))
		if err != nil {
			t.Fatalf("failed to end focus: %v", err)
		}
		if ended.Status != domain.SessionStatusEnded {
			t.Errorf("expected session ended, got: %s", ended.Status)
		}

		// Verify slot is freed: can start focus again
		sess2, _, err := focusSvc.Start(ctx, 200, task.ID, now.Add(35*time.Second))
		if err != nil {
			t.Fatalf("failed to start focus after ending previous session: %v", err)
		}

		// Complete task via Done
		_, completedTask, err := focusSvc.Done(ctx, 200, sess2.ID, sess2.Version, now.Add(60*time.Second))
		if err != nil {
			t.Fatalf("failed to complete task: %v", err)
		}
		if completedTask.Status != domain.TaskStatusCompleted {
			t.Errorf("expected task completed, got: %s", completedTask.Status)
		}
	})

	// 6. StateAwaitingStep: vague step rejected even if AI times out
	t.Run("AwaitingStep: vague step rejected on AI timeout", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		timeoutAI := &mockAISuggester{err: context.DeadlineExceeded}
		svc := service.NewTaskService(repo, repo, timeoutAI)

		task, _ := svc.CreateTask(ctx, 200, "Math homework", "", "k-hw", now)
		uCtx := domain.UserContext{
			State:       domain.StateAwaitingStep,
			CurrentTask: task,
		}

		res, err := svc.ProcessUserMessage(ctx, 200, "u know what im talking about", uCtx, "step-vague", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ActionTaken != "CLARIFICATION_REQUESTED" {
			t.Errorf("expected CLARIFICATION_REQUESTED for vague step, got: %s", res.ActionTaken)
		}

		// Verify step was NOT updated on task
		freshTask, _ := svc.GetTask(ctx, task.ID, 200)
		if freshTask.MicroStart != "" {
			t.Errorf("expected micro-start to remain empty, got: %s", freshTask.MicroStart)
		}
	})

	// 7. StateAwaitingStep: concrete manual step saved on AI timeout
	t.Run("AwaitingStep: concrete step saved on AI timeout", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		timeoutAI := &mockAISuggester{err: context.DeadlineExceeded}
		svc := service.NewTaskService(repo, repo, timeoutAI)

		task, _ := svc.CreateTask(ctx, 200, "Math homework", "", "k-hw-2", now)
		uCtx := domain.UserContext{
			State:       domain.StateAwaitingStep,
			CurrentTask: task,
		}

		res, err := svc.ProcessUserMessage(ctx, 200, "Open textbook to page 45 and solve problem 1", uCtx, "step-concrete", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ActionTaken != "STEP_UPDATED" {
			t.Errorf("expected STEP_UPDATED for concrete step, got: %s", res.ActionTaken)
		}

		freshTask, _ := svc.GetTask(ctx, task.ID, 200)
		if freshTask.MicroStart != "Open textbook to page 45 and solve problem 1" {
			t.Errorf("expected micro-start updated, got: %s", freshTask.MicroStart)
		}
	})
}

// Tests for Requirement 1: Make saved tasks retrievable
func TestTaskService_YesterdayTasksRetrieval(t *testing.T) {
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Tehran")
	// Base time in Tehran: 2026-11-17 14:00:00 (UTC 10:30)
	now := time.Date(2026, 11, 17, 14, 0, 0, 0, loc)

	t.Run("Zero tasks created yesterday returns clear message without error", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		svc := service.NewTaskService(repo, repo, nil)

		// Create a task created TODAY, not yesterday
		_, _ = svc.CreateTask(ctx, 301, "Today Task", "micro", "id-today", now)

		res, err := svc.ProcessUserMessage(ctx, 301, "تسک دیروزم چی بود", domain.UserContext{State: domain.StateIdle}, "msg-q1", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ActionTaken != "TASK_QUERY_RESULT" {
			t.Errorf("expected TASK_QUERY_RESULT, got %s", res.ActionTaken)
		}
		if res.Task != nil {
			t.Errorf("history query must NEVER create or return a newly created task")
		}
		if res.ReplyText != "دیروز هیچ تسکی ثبت نکرده بودی." {
			t.Errorf("expected zero results message, got: %s", res.ReplyText)
		}

		// Verify task count in repo is still 1 (today's task), no task created
		tasks, _ := svc.ListActiveTasks(ctx, 301, now)
		if len(tasks) != 1 {
			t.Errorf("expected 1 task in active list, got %d", len(tasks))
		}
	})

	t.Run("Exactly one task created yesterday returns actual title", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		svc := service.NewTaskService(repo, repo, nil)

		// Create a task yesterday (2026-11-16 15:00 Tehran)
		yesterdayTime := time.Date(2026, 11, 16, 15, 0, 0, 0, loc)
		_, err := svc.CreateTask(ctx, 302, "آماده‌سازی گزارش ماهانه", "باز کردن فایل اکسل", "id-y1", yesterdayTime)
		if err != nil {
			t.Fatalf("failed to create yesterday task: %v", err)
		}

		res, err := svc.ProcessUserMessage(ctx, 302, "تسک دیروزم چی بود", domain.UserContext{State: domain.StateIdle}, "msg-q2", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ActionTaken != "TASK_QUERY_RESULT" {
			t.Errorf("expected TASK_QUERY_RESULT, got %s", res.ActionTaken)
		}
		expectedTitle := "تسک دیروز شما: «آماده‌سازی گزارش ماهانه»"
		if res.ReplyText != expectedTitle {
			t.Errorf("expected '%s', got '%s'", expectedTitle, res.ReplyText)
		}
	})

	t.Run("Multiple tasks created yesterday lists all titles", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		svc := service.NewTaskService(repo, repo, nil)

		yesterdayTime1 := time.Date(2026, 11, 16, 10, 0, 0, 0, loc)
		yesterdayTime2 := time.Date(2026, 11, 16, 16, 30, 0, 0, loc)
		_, _ = svc.CreateTask(ctx, 303, "بررسی کد پول ریکوئست", "باز کردن گیت‌هاب", "id-y2", yesterdayTime1)
		_, _ = svc.CreateTask(ctx, 303, "تماس با پشتیبانی دیتابیس", "پیدا کردن شماره تیکت", "id-y3", yesterdayTime2)

		res, err := svc.ProcessUserMessage(ctx, 303, "کارهای دیروزم چی بودن", domain.UserContext{State: domain.StateIdle}, "msg-q3", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ActionTaken != "TASK_QUERY_RESULT" {
			t.Errorf("expected TASK_QUERY_RESULT, got %s", res.ActionTaken)
		}
		if !contains(res.ReplyText, "بررسی کد پول ریکوئست") || !contains(res.ReplyText, "تماس با پشتیبانی دیتابیس") {
			t.Errorf("expected both task titles in list output, got:\n%s", res.ReplyText)
		}
	})

	t.Run("Database error returns error and does NOT produce false success", func(t *testing.T) {
		repo := memory.NewMemoryRepo()
		repo.SimulateFail = true
		svc := service.NewTaskService(repo, repo, nil)

		_, err := svc.ProcessUserMessage(ctx, 304, "تسک دیروزم چی بود", domain.UserContext{State: domain.StateIdle}, "msg-q4", now)
		if err == nil {
			t.Fatalf("expected error when database fails")
		}
		if !errors.Is(err, domain.ErrDatabaseUnavailable) {
			t.Errorf("expected ErrDatabaseUnavailable, got: %v", err)
		}
	})
}

// Tests for Requirement 2 & 3: Multi-turn sequence with context and subtasks
func TestTaskService_MultiTurnSequence_DevarHonar(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repo := memory.NewMemoryRepo()
	svc := service.NewTaskService(repo, repo, nil)
	userID := int64(401)

	// Turn 1: User sends ambiguous input "دیوار هنر"
	uCtx1 := domain.UserContext{State: domain.StateIdle}
	res1, err := svc.ProcessUserMessage(ctx, userID, "دیوار هنر", uCtx1, "msg-turn-1", now)
	if err != nil {
		t.Fatalf("unexpected error at turn 1: %v", err)
	}
	if res1.ActionTaken != "CLARIFICATION_REQUESTED" {
		t.Errorf("expected CLARIFICATION_REQUESTED, got %s", res1.ActionTaken)
	}
	if res1.NextState != domain.StateAwaitingClarification {
		t.Errorf("expected NextState AWAITING_CLARIFICATION, got %s", res1.NextState)
	}
	if res1.PendingTitle != "دیوار هنر" {
		t.Errorf("expected PendingTitle 'دیوار هنر', got '%s'", res1.PendingTitle)
	}
	// Invariant: Turn 1 must NOT create any task!
	tasksAfterTurn1, _ := svc.ListActiveTasks(ctx, userID, now)
	if len(tasksAfterTurn1) != 0 {
		t.Fatalf("expected 0 tasks created after turn 1, got %d", len(tasksAfterTurn1))
	}

	// Turn 2: User clarifies: "میخوام تسک باشه و زیر شاخه بزاریم واسش"
	uCtx2 := domain.UserContext{
		State:        res1.NextState,
		PendingTitle: res1.PendingTitle,
	}
	res2, err := svc.ProcessUserMessage(ctx, userID, "میخوام تسک باشه و زیر شاخه بزاریم واسش", uCtx2, "msg-turn-2", now)
	if err != nil {
		t.Fatalf("unexpected error at turn 2: %v", err)
	}
	if res2.ActionTaken != "TASK_CREATED_AWAITING_SUBTASK" {
		t.Errorf("expected TASK_CREATED_AWAITING_SUBTASK, got %s", res2.ActionTaken)
	}
	if res2.Task == nil {
		t.Fatalf("expected created task to be non-nil")
	}
	if res2.Task.Title != "دیوار هنر" {
		t.Errorf("expected task title 'دیوار هنر', got '%s'", res2.Task.Title)
	}
	if res2.Task.MicroStart != "" {
		t.Errorf("expected microStart to be empty initially before focus, got '%s'", res2.Task.MicroStart)
	}
	if res2.NextState != domain.StateAwaitingSubtask {
		t.Errorf("expected NextState AWAITING_SUBTASK, got %s", res2.NextState)
	}
	// Verify in database: Exactly 1 task exists
	tasksAfterTurn2, _ := svc.ListActiveTasks(ctx, userID, now)
	if len(tasksAfterTurn2) != 1 {
		t.Fatalf("expected exactly 1 task in database, got %d", len(tasksAfterTurn2))
	}
	if tasksAfterTurn2[0].Title != "دیوار هنر" {
		t.Errorf("expected task title 'دیوار هنر' in database, got '%s'", tasksAfterTurn2[0].Title)
	}

	// Repeat delivery of Turn 2: Same idempotency key must NOT create duplicate task!
	res2Replay, err := svc.ProcessUserMessage(ctx, userID, "میخوام تسک باشه و زیر شاخه بزاریم واسش", uCtx2, "msg-turn-2", now)
	if err != nil {
		t.Fatalf("unexpected error on turn 2 replay: %v", err)
	}
	if res2Replay.Task.ID != res2.Task.ID {
		t.Errorf("expected same task ID on replayed delivery, got %s and %s", res2.Task.ID, res2Replay.Task.ID)
	}
	tasksAfterReplay, _ := svc.ListActiveTasks(ctx, userID, now)
	if len(tasksAfterReplay) != 1 {
		t.Fatalf("expected still exactly 1 task after idempotent replay, got %d", len(tasksAfterReplay))
	}

	// Turn 3: User replies with subtask title "آماده کردن متن تماس با کافهها"
	uCtx3 := domain.UserContext{
		State:         res2.NextState,
		PendingTaskID: res2.Task.ID,
		PendingTitle:  res2.Task.Title,
	}
	res3, err := svc.ProcessUserMessage(ctx, userID, "آماده کردن متن تماس با کافهها", uCtx3, "msg-turn-3", now)
	if err != nil {
		t.Fatalf("unexpected error at turn 3: %v", err)
	}
	if res3.ActionTaken != "SUBTASK_ADDED" {
		t.Errorf("expected SUBTASK_ADDED, got %s", res3.ActionTaken)
	}
	if res3.Subtask == nil {
		t.Fatalf("expected subtask to be non-nil")
	}
	if res3.Subtask.Title != "آماده کردن متن تماس با کافهها" {
		t.Errorf("expected subtask title 'آماده کردن متن تماس با کافهها', got '%s'", res3.Subtask.Title)
	}

	// Verify in database:
	// 1. Task still has title "دیوار هنر"
	// 2. Task has exactly 1 subtask
	// 3. Subtask is NOT a separate top-level task in ListActiveTasks
	freshParent, err := svc.GetTask(ctx, res2.Task.ID, userID)
	if err != nil {
		t.Fatalf("failed to retrieve parent task: %v", err)
	}
	if len(freshParent.Subtasks) != 1 {
		t.Fatalf("expected 1 subtask inside parent task, got %d", len(freshParent.Subtasks))
	}
	if freshParent.Subtasks[0].Title != "آماده کردن متن تماس با کافهها" {
		t.Errorf("expected subtask title 'آماده کردن متن تماس با کافهها', got '%s'", freshParent.Subtasks[0].Title)
	}

	activeTopLevel, _ := svc.ListActiveTasks(ctx, userID, now)
	if len(activeTopLevel) != 1 {
		t.Errorf("subtask must NOT be a separate top-level task; expected 1 top-level task, got %d", len(activeTopLevel))
	}
}

func TestTaskService_FailedWrite_ProducesNoSuccess(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repo := memory.NewMemoryRepo()
	repo.SimulateFail = true
	svc := service.NewTaskService(repo, repo, nil)

	uCtx := domain.UserContext{
		State:        domain.StateAwaitingClarification,
		PendingTitle: "دیوار هنر",
	}

	res, err := svc.ProcessUserMessage(ctx, 501, "میخوام تسک باشه و زیر شاخه بزاریم واسش", uCtx, "msg-fail", now)
	if err == nil {
		t.Fatalf("expected error when database write fails")
	}
	if res != nil {
		t.Errorf("expected nil result on failed write, got: %+v", res)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
