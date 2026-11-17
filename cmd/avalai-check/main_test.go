package main

import (
	"encoding/json"
	"testing"
)

func TestAvalAICheck_IsStepConcrete(t *testing.T) {
	concreteSteps := []string{
		"Open email client and click Compose",
		"Open textbook to chapter 3 problem 1",
		"باز کردن فایل صورت تمرین و خواندن سوال اول",
		"Download bank statement PDF",
	}

	for _, step := range concreteSteps {
		if !isStepConcrete(step) {
			t.Errorf("expected step '%s' to be considered concrete", step)
		}
	}

	vagueSteps := []string{
		"plan my day",
		"think about what to do",
		"برنامه‌ریزی برای پروژه",
		"فکر کن در مورد درس",
		"work",
		"do it",
	}

	for _, step := range vagueSteps {
		if isStepConcrete(step) {
			t.Errorf("expected vague step '%s' to be rejected as not concrete", step)
		}
	}
}

func TestAvalAICheck_InterpretationParsing(t *testing.T) {
	rawJSON := `{
		"intent": "NEW_TASK",
		"task_title": "Email professor",
		"suggested_micro_start": "Open Gmail and click Compose",
		"clarification_prompt": "",
		"chat_reply": ""
	}`

	var interp CoachInterpretation
	if err := json.Unmarshal([]byte(rawJSON), &interp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if interp.Intent != "NEW_TASK" {
		t.Errorf("expected intent NEW_TASK, got %s", interp.Intent)
	}
	if interp.TaskTitle != "Email professor" {
		t.Errorf("expected task title 'Email professor', got %s", interp.TaskTitle)
	}
	if interp.SuggestedMicroStart != "Open Gmail and click Compose" {
		t.Errorf("unexpected micro start: %s", interp.SuggestedMicroStart)
	}
}
