package llm_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ttaskam/internal/adapter/llm"
	"ttaskam/internal/adapter/memory"
	"ttaskam/internal/adapter/telegram"
	"ttaskam/internal/domain"
	"ttaskam/internal/service"
)

func TestLiveLLM_Evaluation(t *testing.T) {
	// Automatically load .env if present
	cfg := telegram.LoadConfig()

	apiKey := cfg.LLMAPIKey
	if apiKey == "" {
		t.Skip("LLM_API_KEY is not set. Skipping live model evaluation. (Define LLM_API_KEY in .env to run live model scorecard)")
	}

	baseURL := cfg.LLMBaseURL
	model := cfg.LLMModel

	client := llm.NewClient(llm.Config{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		Timeout: 10 * time.Second,
	})

	repo := memory.NewMemoryRepo()
	taskSvc := service.NewTaskService(repo, repo, client)
	ctx := context.Background()

	type evalCase struct {
		name            string
		state           domain.ConversationState
		input           string
		currentTask     *domain.Task
		expectedIntent  domain.IntentType
		expectTaskSaved bool
		expectStepSaved bool
	}

	cases := []evalCase{
		{
			name:            "Idle: 'wtf'",
			state:           domain.StateIdle,
			input:           "wtf",
			expectedIntent:  domain.IntentChat,
			expectTaskSaved: false,
			expectStepSaved: false,
		},
		{
			name:            "Idle: 'ok'",
			state:           domain.StateIdle,
			input:           "ok",
			expectedIntent:  domain.IntentChat,
			expectTaskSaved: false,
			expectStepSaved: false,
		},
		{
			name:            "Idle: 'u know what im talking about'",
			state:           domain.StateIdle,
			input:           "u know what im talking about",
			expectedIntent:  domain.IntentChat,
			expectTaskSaved: false,
			expectStepSaved: false,
		},
		{
			name:            "Awaiting Step: 'u know what im talking about'",
			state:           domain.StateAwaitingStep,
			input:           "u know what im talking about",
			currentTask:     &domain.Task{ID: "task-1", UserID: 101, Title: "Math Assignment", MicroStart: "", Status: domain.TaskStatusCreated},
			expectedIntent:  domain.IntentNeedsClarification,
			expectTaskSaved: false,
			expectStepSaved: false,
		},
		{
			name:            "Confirming Step: 'ok'",
			state:           domain.StateConfirmingStep,
			input:           "ok",
			currentTask:     &domain.Task{ID: "task-1", UserID: 101, Title: "Math Assignment", MicroStart: "Open textbook to page 40", Status: domain.TaskStatusCreated},
			expectedIntent:  domain.IntentPromptReply,
			expectTaskSaved: false,
			expectStepSaved: true,
		},
		{
			name:            "English intention: Professor email",
			state:           domain.StateIdle,
			input:           "I need to email my professor, but I keep putting it off",
			expectedIntent:  domain.IntentNewTask,
			expectTaskSaved: true,
			expectStepSaved: true,
		},
		{
			name:            "Persian intention: Data structures assignment",
			state:           domain.StateIdle,
			input:           "باید تکلیف ساختمان دادهمو شروع کنم ولی هی عقبش میندازم",
			expectedIntent:  domain.IntentNewTask,
			expectTaskSaved: true,
			expectStepSaved: true,
		},
		{
			name:            "Persian ambiguous: 'نمیدونم از کجا شروع کنم'",
			state:           domain.StateIdle,
			input:           "نمیدونم از کجا شروع کنم",
			expectedIntent:  domain.IntentNeedsClarification,
			expectTaskSaved: false,
			expectStepSaved: false,
		},
		{
			name:            "Persian chitchat: 'سلام چطوری چه خبرا'",
			state:           domain.StateIdle,
			input:           "سلام چطوری چه خبرا",
			expectedIntent:  domain.IntentChat,
			expectTaskSaved: false,
			expectStepSaved: false,
		},
		{
			name:            "English ambiguous: 'I feel so overwhelmed with life'",
			state:           domain.StateIdle,
			input:           "I feel so overwhelmed with life",
			expectedIntent:  domain.IntentNeedsClarification,
			expectTaskSaved: false,
			expectStepSaved: false,
		},
	}

	fmt.Println("\n========================= REAL LLM EVALUATION SCORECARD =========================")
	fmt.Printf("%-32s | %-12s | %-12s | %-7s | %-10s | %-10s\n", "Case", "Expected", "Actual", "Latency", "Task Saved", "Status")
	fmt.Println(strings.Repeat("-", 95))

	for _, c := range cases {
		now := time.Now().UTC()
		start := time.Now()

		if c.currentTask != nil {
			_ = repo.Update(ctx, c.currentTask)
		}

		uCtx := domain.UserContext{
			State:       c.state,
			CurrentTask: c.currentTask,
		}

		res, err := taskSvc.ProcessUserMessage(ctx, 101, c.input, uCtx, fmt.Sprintf("eval-%d", time.Now().UnixNano()), now)
		latency := time.Since(start)

		if err != nil {
			t.Errorf("[%s] FAILED with error: %v", c.name, err)
			continue
		}

		actualIntent := domain.IntentChat
		if res.Interpretation != nil {
			actualIntent = res.Interpretation.Intent
		}

		taskSaved := res.Task != nil && (c.state == domain.StateIdle)
		status := "PASS"

		// Evaluate classification
		if actualIntent != c.expectedIntent {
			// Some models return CHAT instead of NEEDS_CLARIFICATION or vice versa for greetings/vague statements.
			// The critical safety rule: Task MUST NOT be saved for non-tasks!
			if c.expectedIntent == domain.IntentNewTask && actualIntent != domain.IntentNewTask {
				status = "MISCLASSIFIED"
				t.Errorf("[%s] Expected Intent %s, got %s", c.name, c.expectedIntent, actualIntent)
			} else if (c.expectedIntent == domain.IntentChat || c.expectedIntent == domain.IntentNeedsClarification) && actualIntent == domain.IntentNewTask {
				status = "UNSAFE_WRITE"
				t.Errorf("[%s] Critical failure: Non-task was classified as NEW_TASK", c.name)
			} else {
				status = "ACCEPTABLE"
			}
		}

		if c.expectTaskSaved != taskSaved {
			status = "WRITE_MISMATCH"
			t.Errorf("[%s] Expected TaskSaved=%v, got %v", c.name, c.expectTaskSaved, taskSaved)
		}

		latencyStr := fmt.Sprintf("%dms", latency.Milliseconds())
		if latency > 2500*time.Millisecond {
			latencyStr += " (SLOW)"
		}

		fmt.Printf("%-32s | %-12s | %-12s | %-7s | %-10v | %-10s\n", c.name, c.expectedIntent, actualIntent, latencyStr, taskSaved, status)
		if res.Interpretation != nil && res.Interpretation.SuggestedMicroStart != "" {
			fmt.Printf("   ↳ Suggested Step: %s\n", res.Interpretation.SuggestedMicroStart)
		}
		if res.ActionTaken == "CLARIFICATION_REQUESTED" {
			fmt.Printf("   ↳ Clarification Asked: %s\n", res.ReplyText)
		}
	}
	fmt.Println(strings.Repeat("=", 95))
}

// Live end-to-end evaluation of the two specific conversations requested by user using real AvalAI deepseek-flash model
func TestLiveLLM_Conversations_YesterdayAndDevarHonar(t *testing.T) {
	cfg := telegram.LoadConfig()
	if cfg.LLMAPIKey == "" {
		t.Skip("LLM_API_KEY is not set. Skipping live test.")
	}

	client := llm.NewClient(llm.Config{
		BaseURL: cfg.LLMBaseURL,
		APIKey:  cfg.LLMAPIKey,
		Model:   cfg.LLMModel,
		Timeout: 10 * time.Second,
	})

	repo := memory.NewMemoryRepo()
	taskSvc := service.NewTaskService(repo, repo, client)
	ctx := context.Background()
	now := time.Now().UTC()
	userID := int64(77701)

	// Conversation 1: History retrieval for yesterday
	t.Run("Live Model: 'تسک دیروزم چی بود' performs scoped query without creating tasks", func(t *testing.T) {
		// Seed a task created yesterday
		yesterday := now.Add(-24 * time.Hour)
		_, err := taskSvc.CreateTask(ctx, userID, "طراحی بنر نمایشگاه", "باز کردن ایلوستریتور", "seed-yest", yesterday)
		if err != nil {
			t.Fatalf("failed to seed task: %v", err)
		}

		res, err := taskSvc.ProcessUserMessage(ctx, userID, "تسک دیروزم چی بود", domain.UserContext{State: domain.StateIdle}, "msg-hist-live", now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.ActionTaken != "TASK_QUERY_RESULT" {
			t.Errorf("expected TASK_QUERY_RESULT, got %s", res.ActionTaken)
		}
		if !strings.Contains(res.ReplyText, "طراحی بنر نمایشگاه") {
			t.Errorf("expected reply to contain seeded title 'طراحی بنر نمایشگاه', got: %s", res.ReplyText)
		}
		if res.Task != nil {
			t.Errorf("history query must NEVER create a task")
		}
	})

	// Conversation 2: Multi-turn "دیوار هنر" -> "میخوام تسک باشه و زیر شاخه بزاریم واسش" -> "آماده کردن متن تماس با کافهها"
	t.Run("Live Model: Multi-turn subtask sequence across 3 turns", func(t *testing.T) {
		// Turn 1: User sends ambiguous "دیوار هنر"
		uCtx1 := domain.UserContext{State: domain.StateIdle}
		res1, err := taskSvc.ProcessUserMessage(ctx, userID, "دیوار هنر", uCtx1, "msg-turn1-live", now)
		if err != nil {
			t.Fatalf("turn 1 error: %v", err)
		}
		if res1.ActionTaken != "CLARIFICATION_REQUESTED" {
			t.Errorf("turn 1 expected CLARIFICATION_REQUESTED, got %s", res1.ActionTaken)
		}
		if res1.NextState != domain.StateAwaitingClarification {
			t.Errorf("turn 1 expected NextState AWAITING_CLARIFICATION, got %s", res1.NextState)
		}
		if res1.PendingTitle != "دیوار هنر" {
			t.Errorf("turn 1 expected PendingTitle 'دیوار هنر', got '%s'", res1.PendingTitle)
		}
		fmt.Printf("[Turn 1 Live Reply] %s\n", res1.ReplyText)

		// Turn 2: User clarifies "میخوام تسک باشه و زیر شاخه بزاریم واسش"
		uCtx2 := domain.UserContext{
			State:        res1.NextState,
			PendingTitle: res1.PendingTitle,
			LastPrompt:   res1.ReplyText,
		}
		res2, err := taskSvc.ProcessUserMessage(ctx, userID, "میخوام تسک باشه و زیر شاخه بزاریم واسش", uCtx2, "msg-turn2-live", now)
		if err != nil {
			t.Fatalf("turn 2 error: %v", err)
		}
		if res2.ActionTaken != "TASK_CREATED_AWAITING_SUBTASK" {
			t.Errorf("turn 2 expected TASK_CREATED_AWAITING_SUBTASK, got %s", res2.ActionTaken)
		}
		if res2.Task == nil || res2.Task.Title != "دیوار هنر" {
			t.Fatalf("turn 2 expected task 'دیوار هنر' created, got %+v", res2.Task)
		}
		if res2.NextState != domain.StateAwaitingSubtask {
			t.Errorf("turn 2 expected NextState AWAITING_SUBTASK, got %s", res2.NextState)
		}
		fmt.Printf("[Turn 2 Live Reply] %s\n", res2.ReplyText)

		// Turn 3: User replies with subtask "آماده کردن متن تماس با کافهها"
		uCtx3 := domain.UserContext{
			State:         res2.NextState,
			PendingTaskID: res2.Task.ID,
			PendingTitle:  res2.Task.Title,
			LastPrompt:    res2.ReplyText,
		}
		res3, err := taskSvc.ProcessUserMessage(ctx, userID, "آماده کردن متن تماس با کافهها", uCtx3, "msg-turn3-live", now)
		if err != nil {
			t.Fatalf("turn 3 error: %v", err)
		}
		if res3.ActionTaken != "SUBTASK_ADDED" {
			t.Errorf("turn 3 expected SUBTASK_ADDED, got %s", res3.ActionTaken)
		}
		if res3.Subtask == nil || res3.Subtask.Title != "آماده کردن متن تماس با کافهها" {
			t.Fatalf("turn 3 expected subtask 'آماده کردن متن تماس با کافهها', got %+v", res3.Subtask)
		}
		fmt.Printf("[Turn 3 Live Reply] %s\n", res3.ReplyText)

		// Verify database state: Exactly 1 top-level task in active list, with 1 subtask
		parent, err := taskSvc.GetTask(ctx, res2.Task.ID, userID)
		if err != nil {
			t.Fatalf("failed to retrieve parent task: %v", err)
		}
		if len(parent.Subtasks) != 1 {
			t.Fatalf("expected exactly 1 subtask in parent task, got %d", len(parent.Subtasks))
		}
		if parent.Subtasks[0].Title != "آماده کردن متن تماس با کافهها" {
			t.Errorf("expected subtask 'آماده کردن متن تماس با کافهها', got '%s'", parent.Subtasks[0].Title)
		}

		activeTasks, _ := taskSvc.ListActiveTasks(ctx, userID, now)
		// Active tasks count: 1 from yesterday + 1 parent task = 2 total (subtask is NOT a top-level task)
		if len(activeTasks) != 2 {
			t.Errorf("expected 2 active top-level tasks (1 yesterday + 1 today), got %d", len(activeTasks))
		}
	})
}
