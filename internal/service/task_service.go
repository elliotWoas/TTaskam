package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"ttaskam/internal/domain"
)

type AIHandlingResult struct {
	Interpretation *domain.AIInterpretation `json:"interpretation,omitempty"`
	Task           *domain.Task             `json:"task,omitempty"`
	Subtask        *domain.Subtask          `json:"subtask,omitempty"`
	ActionTaken    string                   `json:"action_taken"`
	ReplyText      string                   `json:"reply_text"`
	NextState      domain.ConversationState `json:"next_state,omitempty"`
	PendingTitle   string                   `json:"pending_title,omitempty"`
	PendingTaskID  string                   `json:"pending_task_id,omitempty"`
}

type TaskService struct {
	tasks    domain.TaskRepository
	sessions domain.SessionRepository
	ai       domain.AIInterpreter // Optional
}

func NewTaskService(
	tasks domain.TaskRepository,
	sessions domain.SessionRepository,
	ai domain.AIInterpreter,
) *TaskService {
	return &TaskService{
		tasks:    tasks,
		sessions: sessions,
		ai:       ai,
	}
}

// CreateTask captures an intention.
// If the user has an open focus session, creating new tasks is blocked by the Focus Tunnel.
func (s *TaskService) CreateTask(
	ctx context.Context,
	userID int64,
	title string,
	microStart string,
	idempotencyKey string,
	now time.Time,
) (*domain.Task, error) {
	// 1. Focus tunnel guard: block new task creation while focused
	openSession, err := s.sessions.GetOpenSession(ctx, userID)
	if err != nil {
		return nil, err
	}
	if openSession != nil && openSession.IsOpen() {
		return nil, domain.ErrFocusTunnelActive
	}

	// 2. Build task
	task, err := domain.NewTask(userID, title, microStart, now)
	if err != nil {
		return nil, err
	}

	// 3. Atomically persist task with idempotency key
	created, err := s.tasks.Create(ctx, task, idempotencyKey)
	if err != nil {
		return nil, err
	}

	return created, nil
}

// AddSubtask adds a child subtask to an existing uncompleted parent task.
func (s *TaskService) AddSubtask(
	ctx context.Context,
	userID int64,
	parentTaskID string,
	subtaskTitle string,
	now time.Time,
) (*domain.Task, *domain.Subtask, error) {
	parent, err := s.tasks.GetByID(ctx, parentTaskID, userID)
	if err != nil {
		return nil, nil, err
	}

	sub, err := parent.AddSubtask(subtaskTitle, now)
	if err != nil {
		return nil, nil, err
	}

	if err := s.tasks.Update(ctx, parent); err != nil {
		return nil, nil, err
	}

	return parent, sub, nil
}

// SetMicroStart updates the concrete first action on an uncompleted task.
func (s *TaskService) SetMicroStart(
	ctx context.Context,
	userID int64,
	taskID string,
	microStart string,
	now time.Time,
) (*domain.Task, error) {
	task, err := s.tasks.GetByID(ctx, taskID, userID)
	if err != nil {
		return nil, err
	}

	if err := task.SetMicroStart(microStart, now); err != nil {
		return nil, err
	}

	if err := s.tasks.Update(ctx, task); err != nil {
		return nil, err
	}

	return task, nil
}

// ListActiveTasks lists tasks that are not completed and not archived.
// It lazily evaluates the 72h decay on unstarted tasks.
func (s *TaskService) ListActiveTasks(
	ctx context.Context,
	userID int64,
	now time.Time,
) ([]*domain.Task, error) {
	// Focus tunnel guard: browsing backlog is blocked while focused
	openSession, err := s.sessions.GetOpenSession(ctx, userID)
	if err != nil {
		return nil, err
	}
	if openSession != nil && openSession.IsOpen() {
		return nil, domain.ErrFocusTunnelActive
	}

	tasks, err := s.tasks.ListActive(ctx, userID)
	if err != nil {
		return nil, err
	}

	active := make([]*domain.Task, 0, len(tasks))
	for _, t := range tasks {
		if t.IsExpired(now) {
			_ = t.Archive(now)
			_ = s.tasks.Update(ctx, t)
			continue
		}
		active = append(active, t)
	}

	return active, nil
}

// GetTask retrieves a task by ID for a user.
func (s *TaskService) GetTask(ctx context.Context, id string, userID int64) (*domain.Task, error) {
	return s.tasks.GetByID(ctx, id, userID)
}

// GetYesterdayTasks returns the tasks created during yesterday's calendar day in the user's timezone.
// Documented default timezone is "Asia/Tehran" (UTC+3:30).
func (s *TaskService) GetYesterdayTasks(
	ctx context.Context,
	userID int64,
	userTimezone string,
	now time.Time,
) ([]*domain.Task, error) {
	loc := defaultTimezone(userTimezone)
	nowInLoc := now.In(loc)
	todayStart := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), 0, 0, 0, 0, loc)
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	yesterdayEnd := todayStart

	return s.tasks.GetByTimeRange(ctx, userID, yesterdayStart, yesterdayEnd)
}

func defaultTimezone(userTz string) *time.Location {
	if userTz != "" {
		if loc, err := time.LoadLocation(userTz); err == nil {
			return loc
		}
	}
	loc, err := time.LoadLocation("Asia/Tehran")
	if err == nil {
		return loc
	}
	return time.FixedZone("IRST", 3*3600+30*60)
}

// SuggestMicroStart invokes the optional AI provider for suggestions.
// If AI is unavailable or fails, returns empty string without error.
func (s *TaskService) SuggestMicroStart(ctx context.Context, intention string) string {
	if s.ai == nil {
		return ""
	}
	if suggester, ok := s.ai.(domain.ActionSuggester); ok {
		suggestion, err := suggester.SuggestMicroStart(ctx, intention)
		if err == nil {
			return suggestion
		}
	}
	interp, err := s.ai.Interpret(ctx, intention, domain.UserContext{State: domain.StateIdle})
	if err != nil {
		return ""
	}
	return interp.SuggestedMicroStart
}

// ProcessUserMessage processes incoming text with contextual AI interpretation and strict Go validation.
// Focus tunnel is checked first in Go without calling AI.
// Ambiguous messages NEVER create tasks.
func (s *TaskService) ProcessUserMessage(
	ctx context.Context,
	userID int64,
	message string,
	uCtx domain.UserContext,
	idempotencyKey string,
	now time.Time,
) (*AIHandlingResult, error) {
	// 1. Focus tunnel rule: if session open, block immediately without AI
	openSession, err := s.sessions.GetOpenSession(ctx, userID)
	if err != nil {
		return nil, err
	}
	if openSession != nil && openSession.IsOpen() {
		return nil, domain.ErrFocusTunnelActive
	}

	cleanMsg := strings.TrimSpace(message)
	if cleanMsg == "" {
		return &AIHandlingResult{
			ActionTaken: "CHAT_REPLY",
			ReplyText:   "I'm here! What task do you want to tackle today?",
		}, nil
	}

	// 2. Deterministic History Query check (Go runs scoped query, AI is never invoked to hallucinate history)
	if isQuery, scope := isHistoryQuery(cleanMsg); isQuery {
		return s.handleHistoryQuery(ctx, userID, uCtx, scope, now)
	}

	// 3. Deterministic multi-turn state handling for AwaitingSubtask
	if uCtx.State == domain.StateAwaitingSubtask && uCtx.PendingTaskID != "" {
		return s.handleSubtaskInput(ctx, userID, cleanMsg, uCtx, now)
	}

	// 4. If AI is not configured or disabled, fallback to deterministic manual flow
	if s.ai == nil {
		return s.handleManualFallback(ctx, userID, cleanMsg, uCtx, idempotencyKey, now)
	}

	// 5. Call AI interpreter with timeout context
	interpretCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	interp, err := s.ai.Interpret(interpretCtx, cleanMsg, uCtx)
	if err != nil {
		// On AI failure or timeout, fall back cleanly to manual flow
		return s.handleManualFallback(ctx, userID, cleanMsg, uCtx, idempotencyKey, now)
	}

	// 6. Validate AI interpretation and enforce Go business rules
	return s.applyAIGuardrails(ctx, userID, cleanMsg, interp, uCtx, idempotencyKey, now)
}

func (s *TaskService) handleHistoryQuery(
	ctx context.Context,
	userID int64,
	uCtx domain.UserContext,
	scope string,
	now time.Time,
) (*AIHandlingResult, error) {
	tasks, err := s.GetYesterdayTasks(ctx, userID, uCtx.LanguageHint, now)
	if err != nil {
		return nil, err
	}

	if len(tasks) == 0 {
		msg := "دیروز هیچ تسکی ثبت نکرده بودی."
		if uCtx.LanguageHint == "EN" {
			msg = "You didn't create any tasks yesterday."
		}
		return &AIHandlingResult{
			ActionTaken: "TASK_QUERY_RESULT",
			ReplyText:   msg,
			NextState:   domain.StateIdle,
		}, nil
	}

	if len(tasks) == 1 {
		msg := fmt.Sprintf("تسک دیروز شما: «%s»", tasks[0].Title)
		if uCtx.LanguageHint == "EN" {
			msg = fmt.Sprintf("Your task from yesterday: '%s'", tasks[0].Title)
		}
		return &AIHandlingResult{
			ActionTaken: "TASK_QUERY_RESULT",
			ReplyText:   msg,
			NextState:   domain.StateIdle,
		}, nil
	}

	var sb strings.Builder
	if uCtx.LanguageHint == "EN" {
		sb.WriteString(fmt.Sprintf("Your tasks created yesterday (%d tasks):\n", len(tasks)))
		for i, t := range tasks {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, t.Title))
		}
	} else {
		sb.WriteString(fmt.Sprintf("تسک‌های ثبت‌شده‌ی دیروز شما (%d مورد):\n", len(tasks)))
		for i, t := range tasks {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, t.Title))
		}
	}

	return &AIHandlingResult{
		ActionTaken: "TASK_QUERY_RESULT",
		ReplyText:   strings.TrimSpace(sb.String()),
		NextState:   domain.StateIdle,
	}, nil
}

func (s *TaskService) handleSubtaskInput(
	ctx context.Context,
	userID int64,
	subtaskTitle string,
	uCtx domain.UserContext,
	now time.Time,
) (*AIHandlingResult, error) {
	parentTask, sub, err := s.AddSubtask(ctx, userID, uCtx.PendingTaskID, subtaskTitle, now)
	if err != nil {
		return nil, err
	}
	return &AIHandlingResult{
		Task:          parentTask,
		Subtask:       sub,
		ActionTaken:   "SUBTASK_ADDED",
		ReplyText:     fmt.Sprintf("زیرکار «%s» برای تسک «%s» ذخیره شد.\n\n_قبل از شروع فوکوس، یک ریزاقدام (< ۲ دقیقه) مشخص کنید._", sub.Title, parentTask.Title),
		NextState:     domain.StateIdle,
		PendingTaskID: parentTask.ID,
	}, nil
}

func (s *TaskService) applyAIGuardrails(
	ctx context.Context,
	userID int64,
	rawMessage string,
	interp *domain.AIInterpretation,
	uCtx domain.UserContext,
	idempotencyKey string,
	now time.Time,
) (*AIHandlingResult, error) {
	// 1. History query
	if interp.Intent == domain.IntentQueryTasks {
		scope := interp.QueryScope
		if scope == "" {
			scope = "yesterday"
		}
		return s.handleHistoryQuery(ctx, userID, uCtx, scope, now)
	}

	switch uCtx.State {

	case domain.StateAwaitingClarification:
		// User previously sent ambiguous phrase (e.g. "دیوار هنر").
		// At step 3: resolve "واسش" against pending title, create exactly one task titled pendingTitle, ask for first subtask.
		if interp.Intent == domain.IntentCreateTaskWithSubtasks || interp.WantsSubtasks || isSubtaskRequest(rawMessage) {
			title := uCtx.PendingTitle
			if title == "" {
				title = strings.TrimSpace(interp.TaskTitle)
			}
			if title == "" {
				title = rawMessage
			}

			// Create exactly one task titled pendingTitle without micro-start, no focus start
			createdTask, err := s.CreateTask(ctx, userID, title, "", idempotencyKey, now)
			if err != nil {
				return nil, err
			}

			return &AIHandlingResult{
				Interpretation: interp,
				Task:           createdTask,
				ActionTaken:    "TASK_CREATED_AWAITING_SUBTASK",
				ReplyText:      fmt.Sprintf("تسک «%s» با موفقیت ذخیره شد. اولین زیرکار (subtask) آن چیست؟", createdTask.Title),
				NextState:      domain.StateAwaitingSubtask,
				PendingTaskID:  createdTask.ID,
				PendingTitle:   createdTask.Title,
			}, nil
		}

	case domain.StateAwaitingSubtask:
		if uCtx.PendingTaskID != "" {
			return s.handleSubtaskInput(ctx, userID, rawMessage, uCtx, now)
		}

	case domain.StateAwaitingStep:
		// User was explicitly asked for a concrete micro-start (<2 min)
		// Rule: Vague statements ("u know what im talking about", "idk") MUST NOT be accepted as micro-starts.
		if interp.Intent == domain.IntentNeedsClarification || interp.Intent == domain.IntentChat || isVagueStep(rawMessage) {
			clarification := interp.ClarificationPrompt
			if clarification == "" {
				clarification = "That's still a bit broad or vague. What is the very first physical action you can do in under 2 minutes? (e.g. open the doc, find the email)"
			}
			return &AIHandlingResult{
				Interpretation: interp,
				Task:           uCtx.CurrentTask,
				ActionTaken:    "CLARIFICATION_REQUESTED",
				ReplyText:      clarification,
				NextState:      domain.StateAwaitingStep,
				PendingTaskID:  uCtx.PendingTaskID,
			}, nil
		}

		// User gave a concrete action
		concreteStep := interp.SuggestedMicroStart
		if concreteStep == "" {
			concreteStep = rawMessage
		}
		if uCtx.CurrentTask != nil {
			updated, err := s.SetMicroStart(ctx, userID, uCtx.CurrentTask.ID, concreteStep, now)
			if err != nil {
				return nil, err
			}
			return &AIHandlingResult{
				Interpretation: interp,
				Task:           updated,
				ActionTaken:    "STEP_UPDATED",
				ReplyText:      "Step saved! Ready to focus.",
				NextState:      domain.StateIdle,
			}, nil
		}

	case domain.StateConfirmingStep:
		// User was shown a suggested step and asked to confirm/edit
		if interp.Intent == domain.IntentPromptReply || isAffirmative(rawMessage) {
			return &AIHandlingResult{
				Interpretation: interp,
				Task:           uCtx.CurrentTask,
				ActionTaken:    "CONFIRMED",
				ReplyText:      "Step confirmed! Ready to start.",
				NextState:      domain.StateIdle,
			}, nil
		}

	case domain.StateIdle:
		fallthrough
	default:
		// Hard Go-side guardrails: regardless of what AI returns, casual chat NEVER creates a task!
		if interp.Intent == domain.IntentChat || isCasualChat(rawMessage) {
			reply := interp.ChatReply
			if reply == "" {
				reply = "Ready when you are! What is one task you've been putting off that we can start right now?"
			}
			return &AIHandlingResult{
				Interpretation: interp,
				Task:           nil, // No task saved
				ActionTaken:    "CHAT_REPLY",
				ReplyText:      reply,
				NextState:      domain.StateIdle,
			}, nil
		}

		if interp.Intent == domain.IntentNeedsClarification || isAmbiguousIntention(rawMessage) || isAmbiguousPhrase(rawMessage) {
			pendingTitle := strings.TrimSpace(interp.TaskTitle)
			if pendingTitle == "" {
				pendingTitle = rawMessage
			}

			prompt := interp.ClarificationPrompt
			if prompt == "" {
				if containsPersian(rawMessage) {
					prompt = fmt.Sprintf("منظورت از «%s» چیه؟ میخوای چی کار کنی؟", pendingTitle)
				} else {
					prompt = fmt.Sprintf("What do you want to do with '%s'?", pendingTitle)
				}
			}
			return &AIHandlingResult{
				Interpretation: interp,
				Task:           nil, // No task saved
				ActionTaken:    "CLARIFICATION_REQUESTED",
				ReplyText:      prompt,
				NextState:      domain.StateAwaitingClarification,
				PendingTitle:   pendingTitle,
			}, nil
		}

		// Genuine intention -> Extract title and suggested micro-start, create task in CREATED status
		if interp.Intent == domain.IntentNewTask {
			title := strings.TrimSpace(interp.TaskTitle)
			if title == "" {
				title = rawMessage
			}

			step := strings.TrimSpace(interp.SuggestedMicroStart)
			createdTask, err := s.CreateTask(ctx, userID, title, step, idempotencyKey, now)
			if err != nil {
				return nil, err
			}

			return &AIHandlingResult{
				Interpretation: interp,
				Task:           createdTask,
				ActionTaken:    "TASK_CREATED",
				ReplyText:      "Task created with concrete first step.",
				NextState:      domain.StateIdle,
			}, nil
		}
	}

	// Default fallback
	return &AIHandlingResult{
		Interpretation: interp,
		ActionTaken:    "CHAT_REPLY",
		ReplyText:      "What task would you like to start today?",
		NextState:      domain.StateIdle,
	}, nil
}

func (s *TaskService) handleManualFallback(
	ctx context.Context,
	userID int64,
	rawMessage string,
	uCtx domain.UserContext,
	idempotencyKey string,
	now time.Time,
) (*AIHandlingResult, error) {
	// History query check in fallback
	if isQuery, scope := isHistoryQuery(rawMessage); isQuery {
		return s.handleHistoryQuery(ctx, userID, uCtx, scope, now)
	}

	if uCtx.State == domain.StateAwaitingClarification {
		if isSubtaskRequest(rawMessage) {
			title := uCtx.PendingTitle
			if title == "" {
				title = rawMessage
			}
			createdTask, err := s.CreateTask(ctx, userID, title, "", idempotencyKey, now)
			if err != nil {
				return nil, err
			}
			return &AIHandlingResult{
				Task:          createdTask,
				ActionTaken:   "TASK_CREATED_AWAITING_SUBTASK",
				ReplyText:     fmt.Sprintf("تسک «%s» با موفقیت ذخیره شد. اولین زیرکار (subtask) آن چیست؟", createdTask.Title),
				NextState:     domain.StateAwaitingSubtask,
				PendingTaskID: createdTask.ID,
				PendingTitle:  createdTask.Title,
			}, nil
		}
	}

	if uCtx.State == domain.StateAwaitingSubtask && uCtx.PendingTaskID != "" {
		return s.handleSubtaskInput(ctx, userID, rawMessage, uCtx, now)
	}

	if uCtx.State == domain.StateAwaitingStep && uCtx.CurrentTask != nil {
		if isVagueStep(rawMessage) {
			return &AIHandlingResult{
				Task:          uCtx.CurrentTask,
				ActionTaken:   "CLARIFICATION_REQUESTED",
				ReplyText:     "That's still a bit broad or vague. What is the very first physical action you can do in under 2 minutes?",
				NextState:     domain.StateAwaitingStep,
				PendingTaskID: uCtx.CurrentTask.ID,
			}, nil
		}
		updated, err := s.SetMicroStart(ctx, userID, uCtx.CurrentTask.ID, rawMessage, now)
		if err != nil {
			return nil, err
		}
		return &AIHandlingResult{
			Task:        updated,
			ActionTaken: "STEP_UPDATED",
			ReplyText:   "Step saved.",
			NextState:   domain.StateIdle,
		}, nil
	}

	// Ambiguous phrase at idle
	if isAmbiguousPhrase(rawMessage) || isAmbiguousIntention(rawMessage) {
		prompt := fmt.Sprintf("منظورت از «%s» چیه؟ میخوای چی کار کنی؟", rawMessage)
		if !containsPersian(rawMessage) {
			prompt = fmt.Sprintf("What do you want to do with '%s'?", rawMessage)
		}
		return &AIHandlingResult{
			Task:         nil,
			ActionTaken:  "CLARIFICATION_REQUESTED",
			ReplyText:    prompt,
			NextState:    domain.StateAwaitingClarification,
			PendingTitle: rawMessage,
		}, nil
	}

	// Idle state fallback: NEVER turn arbitrary free text into a task on fallback!
	reply := "I'm having trouble analyzing that message right now. To add a task, use `/new <task name>` or tell me what specific task you want to tackle."
	if containsPersian(rawMessage) {
		reply = "در حال حاضر امکان پردازش این پیام وجود ندارد. برای ثبت کار از دستور `new/ <نام کار>` استفاده کنید یا کار مشخصی را بفرمایید."
	}
	return &AIHandlingResult{
		Task:        nil,
		ActionTaken: "CHAT_REPLY",
		ReplyText:   reply,
		NextState:   domain.StateIdle,
	}, nil
}

func isHistoryQuery(s string) (bool, string) {
	lower := strings.ToLower(strings.TrimSpace(s))
	historyPatterns := []string{
		"تسک دیروزم چی بود",
		"تسک دیروزم",
		"تسک های دیروزم",
		"تسک‌های دیروزم",
		"تسک های دیروز",
		"تسک‌های دیروز",
		"تسک دیروز چی بود",
		"تسک دیروز",
		"کارهای دیروزم",
		"کارهای دیروز",
		"کار دیروزم",
		"دیروز چه تسکی",
		"دیروز چی داشتم",
		"دیروز چیکار کردم",
		"yesterday's task",
		"yesterday tasks",
		"tasks yesterday",
		"what was my task yesterday",
		"what did i do yesterday",
	}
	for _, p := range historyPatterns {
		if strings.Contains(lower, p) {
			return true, "yesterday"
		}
	}
	return false, ""
}

func isSubtaskRequest(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	subtaskPatterns := []string{
		"زیر شاخه",
		"زیرشاخه",
		"زیر کار",
		"زیرکار",
		"subtask",
		"sub-task",
		"تسک باشه",
		"تسکش کن",
	}
	for _, p := range subtaskPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func isAmbiguousPhrase(s string) bool {
	trimmed := strings.TrimSpace(s)
	words := strings.Fields(trimmed)
	if len(words) >= 1 && len(words) <= 3 && len([]rune(trimmed)) <= 25 {
		lower := strings.ToLower(trimmed)
		actionPrefixes := []string{"باید", "میخوام", "می‌خوام", "لازمه", "i need", "i want", "i have to"}
		hasAction := false
		for _, p := range actionPrefixes {
			if strings.HasPrefix(lower, p) {
				hasAction = true
				break
			}
		}
		if !hasAction {
			return true
		}
	}
	return false
}

func isCasualChat(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	casual := []string{
		"wtf", "ok", "okay", "hello", "hi", "hey", "bye", "thanks", "thx", "cool",
		"u know what im talking about", "you know what i mean", "test",
		"سلام", "درود", "خداحافظ", "دمت گرم", "مرسی", "ممنون", "باشه", "خب",
	}
	for _, c := range casual {
		if lower == c {
			return true
		}
	}
	return false
}

func isVagueStep(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	vague := []string{
		"u know what im talking about",
		"you know what i mean",
		"idk",
		"whatever",
		"stuff",
		"things",
		"ok",
		"okay",
		"کار",
		"خودت میدونی",
		"همون دیگه",
	}
	for _, v := range vague {
		if lower == v {
			return true
		}
	}
	return false
}

func isAffirmative(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	affirmatives := []string{
		"ok", "okay", "yes", "yep", "sure", "let's go", "lets go", "start",
		"حله", "باشه", "اره", "آره", "بزن بریم", "شروع کنیم",
	}
	for _, a := range affirmatives {
		if lower == a {
			return true
		}
	}
	return false
}

func isAmbiguousIntention(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	ambiguous := []string{
		"i don't know where to start",
		"i dont know where to start",
		"idk where to start",
		"i'm overwhelmed",
		"so much to do",
		"نمیدونم از کجا شروع کنم",
		"نمی‌دونم از کجا شروع کنم",
		"گیج شدم",
		"خیلی کار دارم",
	}
	for _, a := range ambiguous {
		if strings.Contains(lower, a) {
			return true
		}
	}
	return false
}

func containsPersian(s string) bool {
	for _, r := range s {
		if (r >= 0x0600 && r <= 0x06FF) || (r >= 0xFB50 && r <= 0xFDFF) || (r >= 0xFE70 && r <= 0xFEFF) {
			return true
		}
	}
	return false
}
