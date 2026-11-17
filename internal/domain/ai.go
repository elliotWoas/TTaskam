package domain

import "context"

type IntentType string

const (
	IntentNewTask                  IntentType = "NEW_TASK"
	IntentPromptReply              IntentType = "PROMPT_REPLY"
	IntentNeedsClarification       IntentType = "NEEDS_CLARIFICATION"
	IntentChat                     IntentType = "CHAT"
	IntentQueryTasks               IntentType = "QUERY_TASKS"
	IntentCreateTaskWithSubtasks   IntentType = "CREATE_TASK_WITH_SUBTASKS"
)

type ConversationState string

const (
	StateIdle                  ConversationState = "IDLE"                   // User is not focused, no pending task
	StateAwaitingStep          ConversationState = "AWAITING_STEP"          // User created a task, bot is awaiting micro-start
	StateConfirmingStep        ConversationState = "CONFIRMING_STEP"        // Bot proposed a micro-start, awaiting confirmation/edit
	StateAwaitingClarification ConversationState = "AWAITING_CLARIFICATION" // Bot asked for clarification on ambiguous input
	StateAwaitingSubtask       ConversationState = "AWAITING_SUBTASK"       // Task created, bot awaiting first/next subtask
)

// UserContext carries the minimal state required for the AI to interpret the message.
type UserContext struct {
	State         ConversationState `json:"state"`
	CurrentTask   *Task             `json:"current_task,omitempty"`
	PendingTitle  string            `json:"pending_title,omitempty"`
	PendingTaskID string            `json:"pending_task_id,omitempty"`
	LastPrompt    string            `json:"last_prompt,omitempty"`
	ExpectedReply string            `json:"expected_reply,omitempty"`
	LanguageHint  string            `json:"language_hint,omitempty"`
}

// AIInterpretation is the strictly typed result returned by the LLM provider.
type AIInterpretation struct {
	Intent              IntentType `json:"intent"`
	TaskTitle           string     `json:"task_title,omitempty"`
	SuggestedMicroStart string     `json:"suggested_micro_start,omitempty"`
	ClarificationPrompt string     `json:"clarification_prompt,omitempty"`
	ChatReply           string     `json:"chat_reply,omitempty"`
	WantsSubtasks       bool       `json:"wants_subtasks,omitempty"`
	QueryScope          string     `json:"query_scope,omitempty"` // "yesterday", "today", "all"
	Confidence          float64    `json:"confidence,omitempty"`
}

// AIInterpreter interprets a user message in context and proposes actions.
type AIInterpreter interface {
	Interpret(ctx context.Context, message string, uCtx UserContext) (*AIInterpretation, error)
}

// ActionSuggester provides a standalone micro-start suggestion if needed.
type ActionSuggester interface {
	SuggestMicroStart(ctx context.Context, intention string) (string, error)
}
