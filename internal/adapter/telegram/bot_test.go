package telegram_test

import (
	"testing"
	"time"

	"ttaskam/internal/adapter/telegram"
	"ttaskam/internal/domain"
)

func TestTelegram_CleanCallbackData(t *testing.T) {
	// Focus callback with version
	prefix, action, id, version, err := telegram.CleanCallbackData("focus:pause:sess-123:2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prefix != "focus" || action != "pause" || id != "sess-123" || version != 2 {
		t.Errorf("unexpected parsed values: %s, %s, %s, %d", prefix, action, id, version)
	}

	// Reason callback
	prefix, action, id, _, err = telegram.CleanCallbackData("reason:sess-456:DISTRACTED")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prefix != "reason" || action != "sess-456" || id != "DISTRACTED" {
		t.Errorf("unexpected parsed values: %s, %s, %s", prefix, action, id)
	}

	// Task callback
	prefix, action, id, _, err = telegram.CleanCallbackData("task:start:task-789")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prefix != "task" || action != "start" || id != "task-789" {
		t.Errorf("unexpected parsed values: %s, %s, %s", prefix, action, id)
	}

	// Invalid callback
	_, _, _, _, err = telegram.CleanCallbackData("invalid")
	if err == nil {
		t.Errorf("expected error for malformed callback string")
	}
}

func TestTelegram_Keyboards(t *testing.T) {
	now := time.Now().UTC()
	session := domain.NewFocusSession(123, "task-1", now)

	// Active focus keyboard: contains Pause, Done, End
	kbActive := telegram.BuildFocusKeyboard(session)
	if kbActive == nil || len(kbActive.InlineKeyboard) == 0 {
		t.Fatalf("expected active keyboard with rows")
	}

	// Paused focus keyboard: contains Resume, Done, End
	_ = session.Pause(1, now.Add(time.Minute))
	kbPaused := telegram.BuildFocusKeyboard(session)
	if kbPaused == nil || len(kbPaused.InlineKeyboard) == 0 {
		t.Fatalf("expected paused keyboard with rows")
	}

	// Exit reason keyboard
	kbReason := telegram.BuildExitReasonKeyboard(session.ID)
	if kbReason == nil || len(kbReason.InlineKeyboard) != 3 {
		t.Fatalf("expected exit reason keyboard with 3 rows, got %d", len(kbReason.InlineKeyboard))
	}
}
