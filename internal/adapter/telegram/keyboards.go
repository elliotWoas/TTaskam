package telegram

import (
	"fmt"
	"strings"

	"ttaskam/internal/domain"

	tele "gopkg.in/telebot.v3"
)

func BuildFocusKeyboard(session *domain.FocusSession) *tele.ReplyMarkup {
	markup := &tele.ReplyMarkup{}

	var row1 []tele.Btn
	if session.Status == domain.SessionStatusActive {
		pauseBtn := markup.Data("⏸️ Pause", fmt.Sprintf("focus:pause:%s:%d", session.ID, session.Version))
		row1 = append(row1, pauseBtn)
	} else if session.Status == domain.SessionStatusPaused {
		resumeBtn := markup.Data("▶️ Resume", fmt.Sprintf("focus:resume:%s:%d", session.ID, session.Version))
		row1 = append(row1, resumeBtn)
	}

	doneBtn := markup.Data("✅ Done", fmt.Sprintf("focus:done:%s:%d", session.ID, session.Version))
	endBtn := markup.Data("⏹️ End", fmt.Sprintf("focus:end:%s:%d", session.ID, session.Version))
	row1 = append(row1, doneBtn, endBtn)

	markup.Inline(row1)
	return markup
}

func BuildExitReasonKeyboard(sessionID string) *tele.ReplyMarkup {
	markup := &tele.ReplyMarkup{}

	btnDistracted := markup.Data("🌀 Distracted", fmt.Sprintf("reason:%s:%s", sessionID, domain.ExitReasonDistracted))
	btnBlocked := markup.Data("🚧 Blocked", fmt.Sprintf("reason:%s:%s", sessionID, domain.ExitReasonBlocked))
	btnTired := markup.Data("🥱 Tired", fmt.Sprintf("reason:%s:%s", sessionID, domain.ExitReasonTired))
	btnDoneForNow := markup.Data("🛑 Done For Now", fmt.Sprintf("reason:%s:%s", sessionID, domain.ExitReasonDoneForNow))
	btnSkip := markup.Data("⏩ Skip", fmt.Sprintf("reason:%s:%s", sessionID, domain.ExitReasonSkipped))

	markup.Inline(
		markup.Row(btnDistracted, btnBlocked),
		markup.Row(btnTired, btnDoneForNow),
		markup.Row(btnSkip),
	)
	return markup
}

func BuildTaskReadyKeyboard(taskID string) *tele.ReplyMarkup {
	markup := &tele.ReplyMarkup{}

	startBtn := markup.Data("🟢 Start Focus", fmt.Sprintf("task:start:%s", taskID))
	editBtn := markup.Data("✏️ Edit Step", fmt.Sprintf("task:edit:%s", taskID))
	cancelBtn := markup.Data("❌ Cancel", fmt.Sprintf("task:cancel:%s", taskID))

	markup.Inline(
		markup.Row(startBtn),
		markup.Row(editBtn, cancelBtn),
	)
	return markup
}

func BuildMicroStartPromptKeyboard(taskID string) *tele.ReplyMarkup {
	markup := &tele.ReplyMarkup{}

	defaultBtn := markup.Data("⚡ Use default: 'Open workspace & review'", fmt.Sprintf("task:default:%s", taskID))
	cancelBtn := markup.Data("❌ Cancel", fmt.Sprintf("task:cancel:%s", taskID))

	markup.Inline(
		markup.Row(defaultBtn),
		markup.Row(cancelBtn),
	)
	return markup
}

func BuildTaskListKeyboard(tasks []*domain.Task) *tele.ReplyMarkup {
	markup := &tele.ReplyMarkup{}
	var rows []tele.Row

	for _, t := range tasks {
		label := t.Title
		if len(label) > 20 {
			label = label[:17] + "..."
		}
		statusIcon := "📝"
		if t.Status == domain.TaskStatusStarted {
			statusIcon = "🔄"
		}
		btn := markup.Data(fmt.Sprintf("%s Start: %s", statusIcon, label), fmt.Sprintf("task:start:%s", t.ID))
		rows = append(rows, markup.Row(btn))
	}

	markup.Inline(rows...)
	return markup
}

func CleanCallbackData(data string) (prefix, action, targetID string, version int, err error) {
	// format: "type:action:id:version" or "type:action:id"
	parts := strings.Split(data, ":")
	if len(parts) < 3 {
		return "", "", "", 0, fmt.Errorf("invalid callback data: %s", data)
	}
	prefix = parts[0]
	action = parts[1]
	targetID = parts[2]
	if len(parts) >= 4 {
		_, _ = fmt.Sscanf(parts[3], "%d", &version)
	}
	return prefix, action, targetID, version, nil
}
