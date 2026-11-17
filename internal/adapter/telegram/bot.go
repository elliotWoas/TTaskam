package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ttaskam/internal/domain"
	"ttaskam/internal/service"

	tele "gopkg.in/telebot.v3"
)

type Bot struct {
	teleBot   *tele.Bot
	focusSvc  *service.FocusService
	taskSvc   *service.TaskService
	convStore domain.ConversationStore

	mu          sync.RWMutex
	pendingStep map[int64]string // userID -> taskID (waiting for manual micro-start input)
}

func NewBot(token string, focusSvc *service.FocusService, taskSvc *service.TaskService, convStore domain.ConversationStore) (*Bot, error) {
	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}

	b, err := tele.NewBot(pref)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	bot := &Bot{
		teleBot:     b,
		focusSvc:    focusSvc,
		taskSvc:     taskSvc,
		convStore:   convStore,
		pendingStep: make(map[int64]string),
	}

	bot.registerRoutes()
	return bot, nil
}

func (b *Bot) Start() {
	b.teleBot.Start()
}

func (b *Bot) Stop() {
	b.teleBot.Stop()
}

func (b *Bot) registerRoutes() {
	b.teleBot.Handle("/start", b.handleStart)
	b.teleBot.Handle("/help", b.handleHelp)
	b.teleBot.Handle("/new", b.handleNew)
	b.teleBot.Handle("/status", b.handleStatus)
	b.teleBot.Handle("/tasks", b.handleTasks)
	b.teleBot.Handle("/pause", b.handlePause)
	b.teleBot.Handle("/resume", b.handleResume)
	b.teleBot.Handle("/done", b.handleDone)
	b.teleBot.Handle("/end", b.handleEnd)

	b.teleBot.Handle(tele.OnText, b.handleText)
	b.teleBot.Handle(tele.OnCallback, b.handleCallback)
}

func (b *Bot) handleNew(c tele.Context) error {
	ctx := context.Background()
	userID := c.Sender().ID
	_ = b.convStore.Clear(ctx, userID)
	now := time.Now().UTC()
	payload := strings.TrimSpace(c.Message().Payload)
	if payload == "" {
		return c.Send("Please provide a task title, e.g.:\n`/new Draft status update`", &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	}

	idempotencyKey := fmt.Sprintf("cmd-new-%d-%d", c.Message().ID, userID)
	task, err := b.taskSvc.CreateTask(ctx, userID, payload, "", idempotencyKey, now)
	if err != nil {
		if errors.Is(err, domain.ErrFocusTunnelActive) {
			session, t, _ := b.focusSvc.GetStatus(ctx, userID)
			return b.sendFocusTunnelWarning(c, session, t)
		}
		return c.Send("⚠️ Could not create task.")
	}

	b.mu.Lock()
	b.pendingStep[userID] = task.ID
	b.mu.Unlock()

	msg := fmt.Sprintf("📝 *Task Created:* %s\n\nWhat is your *concrete first action* (< 2 minutes) to start immediately?\n_Send your step as a message, or tap below to use a default:_", task.Title)
	return c.Send(msg, BuildMicroStartPromptKeyboard(task.ID), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

// ---------------- Handlers ----------------

func (b *Bot) handleStart(c tele.Context) error {
	ctx := context.Background()
	userID := c.Sender().ID
	_ = b.convStore.Clear(ctx, userID)

	session, task, err := b.focusSvc.GetStatus(ctx, userID)
	if err == nil && session != nil && session.IsOpen() {
		return b.sendActiveFocusBanner(c, session, task)
	}

	welcome := `👋 *Welcome to your Anti-Procrastination Coach!*

My goal is simple: *Get you to take the first step.*
No endless planning or hoarding backlogs.

*Core Loop:*
1. Send me a task you are putting off.
2. Define *one concrete micro-start* (< 2 minutes).
3. Enter the *Focus Tunnel* and begin immediately.

Type any task intention below to start!`

	return c.Send(welcome, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

func (b *Bot) handleHelp(c tele.Context) error {
	help := `*Available Commands:*
• /status - View current focus status or idle state
• /tasks - View your active tasks backlog
• /pause - Pause your current focus timer
• /resume - Resume your paused focus timer
• /done - Mark the active task and session complete
• /end - Deliberately end focus (frees slot immediately)
• /help - Show this guide

To create a new task, just type what you intend to do!`
	return c.Send(help, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

func (b *Bot) handleStatus(c tele.Context) error {
	ctx := context.Background()
	userID := c.Sender().ID

	session, task, err := b.focusSvc.GetStatus(ctx, userID)
	if err != nil {
		return c.Send("⚠️ Unable to load status right now. Please try again.")
	}

	if session != nil && session.IsOpen() {
		return b.sendActiveFocusBanner(c, session, task)
	}

	return c.Send("🟢 You are currently idle (no open focus session).\nUse /tasks to pick an existing task, or type a new intention to begin.")
}

func (b *Bot) handleTasks(c tele.Context) error {
	ctx := context.Background()
	userID := c.Sender().ID
	now := time.Now().UTC()

	tasks, err := b.taskSvc.ListActiveTasks(ctx, userID, now)
	if err != nil {
		if errors.Is(err, domain.ErrFocusTunnelActive) {
			session, task, _ := b.focusSvc.GetStatus(ctx, userID)
			return b.sendFocusTunnelWarning(c, session, task)
		}
		return c.Send("⚠️ Unable to load tasks right now.")
	}

	if len(tasks) == 0 {
		return c.Send("📭 You have no active tasks. Send an intention to create one!")
	}

	var sb strings.Builder
	sb.WriteString("📋 *Your Active Tasks:*\n\n")
	for i, t := range tasks {
		statusLabel := "Unstarted (72h decay)"
		if t.Status == domain.TaskStatusStarted {
			statusLabel = "In Progress"
		}
		sb.WriteString(fmt.Sprintf("%d. *%s* _(%s)_\n   ↳ Step: _%s_\n", i+1, t.Title, statusLabel, t.MicroStart))
	}
	sb.WriteString("\nTap a button below to focus:")

	return c.Send(sb.String(), BuildTaskListKeyboard(tasks), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

func (b *Bot) handlePause(c tele.Context) error {
	ctx := context.Background()
	userID := c.Sender().ID
	now := time.Now().UTC()

	session, _, err := b.focusSvc.GetStatus(ctx, userID)
	if err != nil || session == nil || !session.IsOpen() {
		return c.Send("⚠️ No open focus session to pause.")
	}
	if session.Status == domain.SessionStatusPaused {
		return c.Send("⏸️ Focus session is already paused.")
	}

	updated, err := b.focusSvc.Pause(ctx, userID, session.ID, session.Version, now)
	if err != nil {
		return c.Send("⚠️ Could not pause session. Please check /status.")
	}

	return c.Send(fmt.Sprintf("⏸️ *Focus Paused*\nTotal focused time: %ds.\nTunnel remains reserved for this task.", updated.TotalActiveSec), BuildFocusKeyboard(updated), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

func (b *Bot) handleResume(c tele.Context) error {
	ctx := context.Background()
	userID := c.Sender().ID
	now := time.Now().UTC()

	session, _, err := b.focusSvc.GetStatus(ctx, userID)
	if err != nil || session == nil || !session.IsOpen() {
		return c.Send("⚠️ No open focus session to resume.")
	}
	if session.Status == domain.SessionStatusActive {
		return c.Send("🟢 Focus session is already active.")
	}

	updated, err := b.focusSvc.Resume(ctx, userID, session.ID, session.Version, now)
	if err != nil {
		return c.Send("⚠️ Could not resume session. Please check /status.")
	}

	return c.Send("🟢 *Focus Resumed!*\nTimer running.", BuildFocusKeyboard(updated), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

func (b *Bot) handleDone(c tele.Context) error {
	ctx := context.Background()
	userID := c.Sender().ID
	now := time.Now().UTC()

	_, completedTask, err := b.focusSvc.Done(ctx, userID, "", 0, now)
	if err != nil {
		if errors.Is(err, domain.ErrNoActiveSession) {
			return c.Send("⚠️ No open focus session found. Use /tasks to start a task first.")
		}
		return c.Send("⚠️ Could not mark session complete.")
	}

	msg := fmt.Sprintf("🎉 *Task Completed!*\nAwesome work on taking action on *%s*.\n\nFocus slot released.", completedTask.Title)
	return c.Send(msg, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

func (b *Bot) handleEnd(c tele.Context) error {
	ctx := context.Background()
	userID := c.Sender().ID
	now := time.Now().UTC()

	session, task, err := b.focusSvc.GetStatus(ctx, userID)
	if err != nil || session == nil || !session.IsOpen() {
		return c.Send("⚠️ No open focus session to end.")
	}

	endedSess, err := b.focusSvc.End(ctx, userID, session.ID, session.Version, now)
	if err != nil {
		return c.Send("⚠️ Could not end session.")
	}

	taskTitle := "your task"
	if task != nil {
		taskTitle = task.Title
	}

	msg := fmt.Sprintf("⏹️ *Focus Session Ended*\n*Task:* %s remains saved in progress.\nTotal active work: %ds.\n\n_Slot released immediately. (Optional) Quick reason for stopping:_", taskTitle, endedSess.TotalActiveSec)
	return c.Send(msg, BuildExitReasonKeyboard(endedSess.ID), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

// ---------------- Text & Callback Dispatch ----------------

func (b *Bot) handleText(c tele.Context) error {
	ctx := context.Background()
	userID := c.Sender().ID
	text := strings.TrimSpace(c.Text())
	now := time.Now().UTC()

	// 1. Focus tunnel guard: checked directly in Go without calling AI
	session, task, _ := b.focusSvc.GetStatus(ctx, userID)
	if session != nil && session.IsOpen() {
		return b.sendFocusTunnelWarning(c, session, task)
	}

	// 2. Identify conversation state from bounded ConversationStore
	var uCtx domain.UserContext
	conv, _ := b.convStore.Get(ctx, userID)
	if conv != nil && conv.IsStale(now, 30*time.Minute) {
		_ = b.convStore.Clear(ctx, userID)
		conv = nil
	}

	if conv != nil && conv.State != "" && conv.State != domain.StateIdle {
		var pTask *domain.Task
		if conv.PendingTaskID != "" {
			pTask, _ = b.taskSvc.GetTask(ctx, conv.PendingTaskID, userID)
		}
		uCtx = domain.UserContext{
			State:         conv.State,
			CurrentTask:   pTask,
			PendingTitle:  conv.PendingTitle,
			PendingTaskID: conv.PendingTaskID,
			LastPrompt:    conv.LastPrompt,
			ExpectedReply: conv.ExpectedReply,
		}
	} else {
		// Fallback check on pendingStep map
		b.mu.RLock()
		pendingTaskID, hasPending := b.pendingStep[userID]
		b.mu.RUnlock()

		if hasPending {
			pTask, err := b.taskSvc.GetTask(ctx, pendingTaskID, userID)
			if err == nil && pTask != nil {
				uCtx = domain.UserContext{
					State:       domain.StateAwaitingStep,
					CurrentTask: pTask,
				}
			} else {
				uCtx = domain.UserContext{State: domain.StateIdle}
			}
		} else {
			uCtx = domain.UserContext{State: domain.StateIdle}
		}
	}

	idempotencyKey := fmt.Sprintf("msg-%d-%d", c.Message().ID, userID)
	result, err := b.taskSvc.ProcessUserMessage(ctx, userID, text, uCtx, idempotencyKey, now)
	if err != nil {
		if errors.Is(err, domain.ErrFocusTunnelActive) {
			return b.sendFocusTunnelWarning(c, session, task)
		}
		return c.Send("⚠️ خطا در پردازش پیام یا پایگاه داده.")
	}

	// 3. Update bounded conversation context based on next state
	if result.NextState != "" && result.NextState != domain.StateIdle {
		_ = b.convStore.Set(ctx, &domain.ConversationContext{
			UserID:        userID,
			State:         result.NextState,
			PendingTitle:  result.PendingTitle,
			PendingTaskID: result.PendingTaskID,
			LastPrompt:    result.ReplyText,
			UpdatedAt:     now,
		})
	} else if result.ActionTaken == "SUBTASK_ADDED" || result.ActionTaken == "STEP_UPDATED" || result.ActionTaken == "CONFIRMED" || result.ActionTaken == "TASK_QUERY_RESULT" {
		_ = b.convStore.Clear(ctx, userID)
	}

	switch result.ActionTaken {
	case "TASK_QUERY_RESULT", "CHAT_REPLY", "CLARIFICATION_REQUESTED", "TASK_CREATED_AWAITING_SUBTASK":
		// Direct conversational reply
		return c.Send(result.ReplyText)

	case "SUBTASK_ADDED":
		targetTask := result.Task
		return c.Send(result.ReplyText, BuildTaskReadyKeyboard(targetTask.ID), &tele.SendOptions{ParseMode: tele.ModeMarkdown})

	case "CONFIRMED":
		if uCtx.CurrentTask != nil {
			msg := fmt.Sprintf("🎯 *Ready to Focus!*\n*Task:* %s\n*First Step:* %s", uCtx.CurrentTask.Title, uCtx.CurrentTask.MicroStart)
			return c.Send(msg, BuildTaskReadyKeyboard(uCtx.CurrentTask.ID), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
		}
		return c.Send(result.ReplyText)

	case "STEP_UPDATED":
		b.mu.Lock()
		delete(b.pendingStep, userID)
		b.mu.Unlock()

		targetTask := result.Task
		if targetTask == nil {
			targetTask = uCtx.CurrentTask
		}
		msg := fmt.Sprintf("🎯 *Ready to Focus!*\n*Task:* %s\n*First Step:* %s", targetTask.Title, targetTask.MicroStart)
		return c.Send(msg, BuildTaskReadyKeyboard(targetTask.ID), &tele.SendOptions{ParseMode: tele.ModeMarkdown})

	case "TASK_CREATED":
		if result.Task.MicroStart != "" {
			msg := fmt.Sprintf("🎯 *Task Identified!*\n*Task:* %s\n*Suggested First Step (<2 min):* %s", result.Task.Title, result.Task.MicroStart)
			return c.Send(msg, BuildTaskReadyKeyboard(result.Task.ID), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
		}

		b.mu.Lock()
		b.pendingStep[userID] = result.Task.ID
		b.mu.Unlock()

		msg := fmt.Sprintf("📝 *Task Created:* %s\n\nWhat is your *concrete first action* (< 2 minutes) to start immediately?\n_Send your step as a message, or tap below to use a default:_", result.Task.Title)
		return c.Send(msg, BuildMicroStartPromptKeyboard(result.Task.ID), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	}

	return c.Send(result.ReplyText)
}

func (b *Bot) handleCallback(c tele.Context) error {
	userID := c.Sender().ID
	data := c.Callback().Data
	now := time.Now().UTC()

	prefix, action, targetID, version, err := CleanCallbackData(data)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: "Invalid button action."})
	}

	switch prefix {
	case "focus":
		return b.handleFocusCallback(c, userID, action, targetID, version, now)
	case "task":
		return b.handleTaskCallback(c, userID, action, targetID, now)
	case "reason":
		return b.handleReasonCallback(c, userID, targetID, action, now)
	}

	return c.Respond()
}

func (b *Bot) handleFocusCallback(c tele.Context, userID int64, action, sessionID string, expectedVersion int, now time.Time) error {
	ctx := context.Background()

	switch action {
	case "pause":
		sess, err := b.focusSvc.Pause(ctx, userID, sessionID, expectedVersion, now)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Session state has changed or button is stale."})
		}
		_ = c.Respond(&tele.CallbackResponse{Text: "Paused"})
		return c.Edit(fmt.Sprintf("⏸️ *Focus Paused*\nTotal work: %ds.\nSlot remains reserved.", sess.TotalActiveSec), BuildFocusKeyboard(sess), &tele.SendOptions{ParseMode: tele.ModeMarkdown})

	case "resume":
		sess, err := b.focusSvc.Resume(ctx, userID, sessionID, expectedVersion, now)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Session state has changed or button is stale."})
		}
		_ = c.Respond(&tele.CallbackResponse{Text: "Resumed"})
		return c.Edit("🟢 *Focus Resumed!*\nTimer running.", BuildFocusKeyboard(sess), &tele.SendOptions{ParseMode: tele.ModeMarkdown})

	case "done":
		_, task, err := b.focusSvc.Done(ctx, userID, sessionID, expectedVersion, now)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Unable to complete session (stale button or closed)."})
		}
		_ = c.Respond(&tele.CallbackResponse{Text: "Completed!"})
		return c.Edit(fmt.Sprintf("🎉 *Task Completed!*\nAwesome job completing *%s*.\nFocus slot released.", task.Title), &tele.SendOptions{ParseMode: tele.ModeMarkdown})

	case "end":
		sess, err := b.focusSvc.End(ctx, userID, sessionID, expectedVersion, now)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Unable to end session (already ended)."})
		}
		_ = c.Respond(&tele.CallbackResponse{Text: "Ended"})
		msg := fmt.Sprintf("⏹️ *Focus Session Ended*\nTotal active work: %ds.\nTask remains in progress.\n\n_Tunnel released. (Optional) Quick reason for stopping:_", sess.TotalActiveSec)
		return c.Edit(msg, BuildExitReasonKeyboard(sess.ID), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	}

	return c.Respond()
}

func (b *Bot) handleTaskCallback(c tele.Context, userID int64, action, taskID string, now time.Time) error {
	ctx := context.Background()

	switch action {
	case "start":
		sess, task, err := b.focusSvc.Start(ctx, userID, taskID, now)
		if err != nil {
			if errors.Is(err, domain.ErrFocusTunnelActive) {
				return c.Respond(&tele.CallbackResponse{Text: "Focus tunnel active: you already have an open session!"})
			}
			if errors.Is(err, domain.ErrTaskExpired) {
				return c.Respond(&tele.CallbackResponse{Text: "This task expired after 72h unstarted."})
			}
			if errors.Is(err, domain.ErrEmptyMicroStart) {
				return c.Respond(&tele.CallbackResponse{Text: "Please set a micro-start step before starting focus."})
			}
			return c.Respond(&tele.CallbackResponse{Text: "Could not start task."})
		}
		_ = c.Respond(&tele.CallbackResponse{Text: "Focus started!"})
		msg := fmt.Sprintf("🟢 *Focus Active!*\n*Task:* %s\n*Current Step:* %s", task.Title, task.MicroStart)
		return c.Edit(msg, BuildFocusKeyboard(sess), &tele.SendOptions{ParseMode: tele.ModeMarkdown})

	case "default":
		task, err := b.taskSvc.SetMicroStart(ctx, userID, taskID, "Open workspace and review outline", now)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Error setting step."})
		}
		b.mu.Lock()
		delete(b.pendingStep, userID)
		b.mu.Unlock()

		_ = c.Respond(&tele.CallbackResponse{Text: "Default step set"})
		msg := fmt.Sprintf("🎯 *Ready to Focus!*\n*Task:* %s\n*First Step:* %s", task.Title, task.MicroStart)
		return c.Edit(msg, BuildTaskReadyKeyboard(task.ID), &tele.SendOptions{ParseMode: tele.ModeMarkdown})

	case "edit":
		b.mu.Lock()
		b.pendingStep[userID] = taskID
		b.mu.Unlock()

		_ = c.Respond(&tele.CallbackResponse{Text: "Send your new micro-start"})
		return c.Send("✏️ Send your new *concrete first step* as a message:", &tele.SendOptions{ParseMode: tele.ModeMarkdown})

	case "cancel":
		_ = b.convStore.Clear(ctx, userID)
		b.mu.Lock()
		delete(b.pendingStep, userID)
		b.mu.Unlock()

		_ = c.Respond(&tele.CallbackResponse{Text: "Cancelled"})
		return c.Edit("❌ Task setup cancelled.", &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	}

	return c.Respond()
}

func (b *Bot) handleReasonCallback(c tele.Context, userID int64, sessionID, reasonCode string, now time.Time) error {
	ctx := context.Background()

	_ = b.focusSvc.RecordExitReason(ctx, userID, sessionID, reasonCode, now)
	_ = c.Respond(&tele.CallbackResponse{Text: "Reason recorded"})

	displayReason := strings.ToLower(strings.ReplaceAll(reasonCode, "_", " "))
	return c.Edit(fmt.Sprintf("⏹️ Focus session concluded (Reason: *%s*).\nTunnel released. Start a new task whenever you are ready!", displayReason), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

// ---------------- UI Helpers ----------------

func (b *Bot) sendActiveFocusBanner(c tele.Context, session *domain.FocusSession, task *domain.Task) error {
	statusIcon := "🟢"
	statusText := "ACTIVE"
	if session.Status == domain.SessionStatusPaused {
		statusIcon = "⏸️"
		statusText = "PAUSED"
	}

	title := "Active Task"
	step := "First action"
	if task != nil {
		title = task.Title
		step = task.MicroStart
	}

	msg := fmt.Sprintf("%s *Focus %s*\n*Task:* %s\n*Current Step:* %s\n*Total Elapsed:* %ds", statusIcon, statusText, title, step, session.TotalActiveSec)
	return c.Send(msg, BuildFocusKeyboard(session), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}

func (b *Bot) sendFocusTunnelWarning(c tele.Context, session *domain.FocusSession, task *domain.Task) error {
	taskTitle := "current task"
	if task != nil {
		taskTitle = task.Title
	}

	msg := fmt.Sprintf("⛔ *Focus Tunnel Active*\nYou are currently focused on *%s*.\n\nFinish or end this session before browsing backlog or starting a new task.", taskTitle)
	if session != nil {
		return c.Send(msg, BuildFocusKeyboard(session), &tele.SendOptions{ParseMode: tele.ModeMarkdown})
	}
	return c.Send(msg, &tele.SendOptions{ParseMode: tele.ModeMarkdown})
}
