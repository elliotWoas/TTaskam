package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ttaskam/internal/domain"
)

type Config struct {
	BaseURL string        `json:"base_url"`
	APIKey  string        `json:"api_key"`
	Model   string        `json:"model"`
	Timeout time.Duration `json:"timeout"`
}

type Client struct {
	cfg        Config
	httpClient *http.Client
}

func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.avalai.ir/v1"
	}
	// Trim trailing slash
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	if cfg.Model == "" {
		cfg.Model = "deepseek-flash"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}

	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Ensure Client implements domain.AIInterpreter and domain.ActionSuggester
var _ domain.AIInterpreter = (*Client)(nil)
var _ domain.ActionSuggester = (*Client)(nil)

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

const systemPrompt = `You are an expert Anti-Procrastination Productivity Coach.
Your primary mission is to get users who get stuck planning to TAKE THE FIRST ACTION (< 2 minutes), not capture more tasks.

You have TWO jobs only:
1. Classify the user's message in context of their current conversation state into one of:
   - "NEW_TASK": User expressed a real intention/task they need or want to do.
   - "PROMPT_REPLY": User is responding to an active prompt (e.g. providing a micro-start, confirming an action, or providing a subtask).
   - "NEEDS_CLARIFICATION": User's message is too vague, overwhelmed, or ambiguous to act on yet (e.g. just a noun phrase like "دیوار هنر" or "I don't know where to start").
   - "QUERY_TASKS": User is asking to retrieve or check past saved tasks (e.g. "تسک دیروزم چی بود", "what did I do yesterday?", "کارهای دیروز من"). NEVER invent task titles; the database will be queried by Go.
   - "CREATE_TASK_WITH_SUBTASKS": User is responding to an ambiguous title clarification by saying they want it to be a task with subtasks (e.g. "میخوام تسک باشه و زیر شاخه بزاریم واسش").
   - "CHAT": Chitchat, greetings, frustration ("wtf", "ok", "hello"), or random remarks.

2. If a new task is detected, suggest ONE immediate, physical, concrete first action ("suggested_micro_start") taking 2 minutes or less (e.g., "Open document and write first heading", not "work on project").

Strict Rules:
- When user asks about saved/past tasks (e.g., "تسک دیروزم چی بود", "what was my task yesterday?"):
  - Mark "QUERY_TASKS". Set "query_scope": "yesterday". NEVER invent titles or generate database queries.
- When State is "IDLE":
  - "wtf", "ok", "u know what im talking about", "hello", "hi", "bye" are "CHAT". NEVER "NEW_TASK".
  - "I don't know where to start" or "نمیدونم از کجا شروع کنم" is "NEEDS_CLARIFICATION". Ask ONE short clarifying question. NEVER "NEW_TASK".
  - Ambiguous noun phrases without action (e.g., "دیوار هنر", "پروژه عکاسی") are "NEEDS_CLARIFICATION". Ask what they want to do with it (e.g. "منظورت از «دیوار هنر» چیه؟ میخوای چی کار کنی؟").
  - Only genuine intentions (e.g., "I need to email my professor...", "باید تکلیف ساختمان دادهمو شروع کنم...") are "NEW_TASK".
- When State is "AWAITING_CLARIFICATION":
  - The context has "pending_title".
  - If user clarifies they want it as a task and want subtasks (e.g. "میخوام تسک باشه و زیر شاخه بزاریم واسش", "make it a task and add subtasks"):
    Mark "CREATE_TASK_WITH_SUBTASKS". Set "task_title" to "pending_title". Set "wants_subtasks": true.
- When State is "AWAITING_SUBTASK":
  - The context has "pending_task_title".
  - If user provides a subtask (e.g., "آماده کردن متن تماس با کافهها"):
    Mark "PROMPT_REPLY". Set "task_title" to the user's subtask text.
- When State is "AWAITING_STEP":
  - If user gives vague/evasive input (e.g. "u know what im talking about", "idk", "stuff"): Mark "NEEDS_CLARIFICATION" and prompt for a concrete physical step.
  - If user gives a concrete step: Mark "PROMPT_REPLY".
- When State is "CONFIRMING_STEP":
  - "ok", "yes", "sure", "let's go", "حله", "باشه" are "PROMPT_REPLY".
- Match the user's language (Persian / Farsi for Persian input, English for English input).

You MUST respond in valid JSON with these exact fields:
{
  "intent": "NEW_TASK" | "PROMPT_REPLY" | "NEEDS_CLARIFICATION" | "QUERY_TASKS" | "CREATE_TASK_WITH_SUBTASKS" | "CHAT",
  "task_title": "string or empty",
  "suggested_micro_start": "concrete <2 min action string or empty",
  "clarification_prompt": "short clarifying question if NEEDS_CLARIFICATION or empty",
  "chat_reply": "short coach response if CHAT or empty",
  "wants_subtasks": false,
  "query_scope": "yesterday" | "today" | "all" | "",
  "confidence": 0.0 to 1.0
}`

func (c *Client) Interpret(ctx context.Context, message string, uCtx domain.UserContext) (*domain.AIInterpretation, error) {
	if c.cfg.APIKey == "" {
		return nil, errors.New("llm api key is empty")
	}

	userPayload := map[string]interface{}{
		"current_state": uCtx.State,
		"user_message":  message,
	}
	if uCtx.CurrentTask != nil {
		userPayload["current_task_title"] = uCtx.CurrentTask.Title
		userPayload["current_task_micro_start"] = uCtx.CurrentTask.MicroStart
	}
	if uCtx.PendingTitle != "" {
		userPayload["pending_title"] = uCtx.PendingTitle
	}
	if uCtx.PendingTaskID != "" {
		userPayload["pending_task_id"] = uCtx.PendingTaskID
	}
	if uCtx.LastPrompt != "" {
		userPayload["last_prompt"] = uCtx.LastPrompt
	}
	if uCtx.ExpectedReply != "" {
		userPayload["expected_reply"] = uCtx.ExpectedReply
	}
	if uCtx.LanguageHint != "" {
		userPayload["language_hint"] = uCtx.LanguageHint
	}

	userJSON, err := json.Marshal(userPayload)
	if err != nil {
		return nil, err
	}

	reqBody := chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
		Temperature:    0.1,
		MaxTokens:      500,
		ResponseFormat: &responseFormat{Type: "json_object"},
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/chat/completions", c.cfg.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.cfg.APIKey))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read llm response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm returned error HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse llm chat response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, errors.New("llm returned zero choices")
	}

	content := cleanJSON(chatResp.Choices[0].Message.Content)

	var interpretation domain.AIInterpretation
	if err := json.Unmarshal([]byte(content), &interpretation); err != nil {
		return nil, fmt.Errorf("failed to unmarshal interpretation JSON: %w (raw: %s)", err, content)
	}

	return &interpretation, nil
}

// SuggestMicroStart implements domain.ActionSuggester
func (c *Client) SuggestMicroStart(ctx context.Context, intention string) (string, error) {
	uCtx := domain.UserContext{
		State: domain.StateIdle,
	}
	interp, err := c.Interpret(ctx, intention, uCtx)
	if err != nil {
		return "", err
	}
	return interp.SuggestedMicroStart, nil
}

// Verify sends a lightweight test probe to the configured LLM endpoint to confirm connectivity and response parsing.
func (c *Client) Verify(ctx context.Context) (*domain.AIInterpretation, error) {
	uCtx := domain.UserContext{
		State: domain.StateIdle,
	}
	return c.Interpret(ctx, "hello coach", uCtx)
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	first := strings.Index(s, "{")
	last := strings.LastIndex(s, "}")
	if first != -1 && last != -1 && last > first {
		return s[first : last+1]
	}
	return s
}
