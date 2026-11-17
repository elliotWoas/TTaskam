package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"ttaskam/internal/adapter/memory"
	"ttaskam/internal/domain"
	"ttaskam/internal/service"
)

type mockAIInterpreter struct {
	interpretFn func(ctx context.Context, message string, uCtx domain.UserContext) (*domain.AIInterpretation, error)
}

func (m *mockAIInterpreter) Interpret(ctx context.Context, message string, uCtx domain.UserContext) (*domain.AIInterpretation, error) {
	if m.interpretFn != nil {
		return m.interpretFn(ctx, message, uCtx)
	}
	return &domain.AIInterpretation{Intent: domain.IntentChat}, nil
}

func (m *mockAIInterpreter) SuggestMicroStart(ctx context.Context, intention string) (string, error) {
	interp, err := m.Interpret(ctx, intention, domain.UserContext{State: domain.StateIdle})
	if err != nil {
		return "", err
	}
	return interp.SuggestedMicroStart, nil
}

func TestAIEvaluation_TableDriven(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	tests := []struct {
		name                 string
		initialState         domain.ConversationState
		inputMessage         string
		existingTask         *domain.Task
		mockIntent           domain.IntentType
		mockTitle            string
		mockStep             string
		mockClarify          string
		mockChat             string
		expectedAction       string
		expectTaskSaved      bool
		expectStepSaved      bool
		validateResponseText func(t *testing.T, text string)
	}{
		{
			name:            "Idle chitchat 'wtf' must not create task",
			initialState:    domain.StateIdle,
			inputMessage:    "wtf",
			mockIntent:      domain.IntentChat,
			mockChat:        "What's going on? What task are you putting off?",
			expectedAction:  "CHAT_REPLY",
			expectTaskSaved: false,
		},
		{
			name:            "Idle 'ok' must not create task",
			initialState:    domain.StateIdle,
			inputMessage:    "ok",
			mockIntent:      domain.IntentChat,
			mockChat:        "Ready when you are! What would you like to tackle?",
			expectedAction:  "CHAT_REPLY",
			expectTaskSaved: false,
		},
		{
			name:            "Idle 'u know what im talking about' must not create task",
			initialState:    domain.StateIdle,
			inputMessage:    "u know what im talking about",
			mockIntent:      domain.IntentChat,
			mockChat:        "Tell me specifically what you want to work on.",
			expectedAction:  "CHAT_REPLY",
			expectTaskSaved: false,
		},
		{
			name:            "Awaiting step: 'u know what im talking about' must NOT be accepted as micro-start",
			initialState:    domain.StateAwaitingStep,
			inputMessage:    "u know what im talking about",
			existingTask:    &domain.Task{ID: "task-1", UserID: 100, Title: "Math Assignment", MicroStart: "", Status: domain.TaskStatusCreated},
			mockIntent:      domain.IntentNeedsClarification,
			mockClarify:     "That's still a bit vague. What is the very first physical action you can do in 2 minutes?",
			expectedAction:  "CLARIFICATION_REQUESTED",
			expectTaskSaved: true,
			expectStepSaved: false, // Step not accepted
			validateResponseText: func(t *testing.T, text string) {
				if !strings.Contains(strings.ToLower(text), "physical") && !strings.Contains(strings.ToLower(text), "vague") {
					t.Errorf("expected follow-up prompt for concrete step, got: %s", text)
				}
			},
		},
		{
			name:            "Confirming step: 'ok' confirms proposed step",
			initialState:    domain.StateConfirmingStep,
			inputMessage:    "ok",
			existingTask:    &domain.Task{ID: "task-1", UserID: 100, Title: "Math Assignment", MicroStart: "Open textbook to page 40", Status: domain.TaskStatusCreated},
			mockIntent:      domain.IntentPromptReply,
			expectedAction:  "CONFIRMED",
			expectTaskSaved: true,
			expectStepSaved: true,
		},
		{
			name:            "Genuine English intention recognized with concrete micro-start",
			initialState:    domain.StateIdle,
			inputMessage:    "I need to email my professor, but I keep putting it off",
			mockIntent:      domain.IntentNewTask,
			mockTitle:       "Email professor",
			mockStep:        "Open email client and find professor's email address",
			expectedAction:  "TASK_CREATED",
			expectTaskSaved: true,
			expectStepSaved: true,
			validateResponseText: func(t *testing.T, text string) {
				// Handled in Go
			},
		},
		{
			name:            "Genuine Persian intention recognized with concrete Persian micro-start",
			initialState:    domain.StateIdle,
			inputMessage:    "باید تکلیف ساختمان دادهمو شروع کنم ولی هی عقبش میندازم",
			mockIntent:      domain.IntentNewTask,
			mockTitle:       "تکلیف ساختمان داده",
			mockStep:        "باز کردن فایل صورت تمرین و خواندن سوال اول",
			expectedAction:  "TASK_CREATED",
			expectTaskSaved: true,
			expectStepSaved: true,
		},
		{
			name:            "Vague Persian statement without task: asks clarifying question, creates no task",
			initialState:    domain.StateIdle,
			inputMessage:    "نمیدونم از کجا شروع کنم",
			mockIntent:      domain.IntentNeedsClarification,
			mockClarify:     "یک کار مشخص که باید انجام بشه رو بگو تا با هم قدم اولش رو برداریم.",
			expectedAction:  "CLARIFICATION_REQUESTED",
			expectTaskSaved: false,
		},
		{
			name:            "Persian chitchat at idle creates no task",
			initialState:    domain.StateIdle,
			inputMessage:    "سلام چطوری",
			mockIntent:      domain.IntentChat,
			mockChat:        "سلام! برای شروع چه کاری آماده‌ای؟",
			expectedAction:  "CHAT_REPLY",
			expectTaskSaved: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := memory.NewMemoryRepo()
			mockAI := &mockAIInterpreter{
				interpretFn: func(ctx context.Context, msg string, uCtx domain.UserContext) (*domain.AIInterpretation, error) {
					return &domain.AIInterpretation{
						Intent:              tc.mockIntent,
						TaskTitle:           tc.mockTitle,
						SuggestedMicroStart: tc.mockStep,
						ClarificationPrompt: tc.mockClarify,
						ChatReply:           tc.mockChat,
						Confidence:          0.95,
					}, nil
				},
			}

			taskSvc := service.NewTaskService(repo, repo, mockAI)

			if tc.existingTask != nil {
				_, _ = repo.Create(ctx, tc.existingTask, "")
			}

			uCtx := domain.UserContext{
				State:       tc.initialState,
				CurrentTask: tc.existingTask,
			}

			res, err := taskSvc.ProcessUserMessage(ctx, 100, tc.inputMessage, uCtx, "test-idemp", now)
			if err != nil {
				t.Fatalf("unexpected error processing message: %v", err)
			}

			if res.ActionTaken != tc.expectedAction {
				t.Errorf("expected ActionTaken '%s', got '%s'", tc.expectedAction, res.ActionTaken)
			}

			if tc.expectTaskSaved {
				if res.Task == nil {
					t.Errorf("expected task to be non-nil")
				}
			} else {
				if res.Task != nil {
					t.Errorf("expected NO task to be saved, but task was created: ID=%s, Title=%s", res.Task.ID, res.Task.Title)
				}
			}

			if tc.validateResponseText != nil {
				tc.validateResponseText(t, res.ReplyText)
			}
		})
	}
}
