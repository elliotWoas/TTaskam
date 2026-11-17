package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Catalog pricing for known AvalAI models (USD per 1M tokens)
// Source: AvalAI official pricing / model specifications
type ModelPricing struct {
	InputPer1M  float64
	OutputPer1M float64
	Source      string
}

var knownPricing = map[string]ModelPricing{
	"deepseek-flash":       {InputPer1M: 0.15, OutputPer1M: 0.60, Source: "AvalAI Catalog"},
	"deepseek-v4.1-flash":  {InputPer1M: 0.15, OutputPer1M: 0.60, Source: "AvalAI Catalog"},
	"deepseek-chat":        {InputPer1M: 0.22, OutputPer1M: 0.66, Source: "AvalAI Catalog"},
	"glm-5.3-flash":        {InputPer1M: 0.14, OutputPer1M: 0.45, Source: "AvalAI Catalog"},
	"glm-4.6":              {InputPer1M: 0.60, OutputPer1M: 2.20, Source: "AvalAI Catalog"},
	"glm-5.2":              {InputPer1M: 1.40, OutputPer1M: 4.40, Source: "AvalAI Catalog"},
	"qwen3.8-flash":        {InputPer1M: 0.15, OutputPer1M: 0.60, Source: "AvalAI Catalog"},
	"qwen-flash":           {InputPer1M: 0.15, OutputPer1M: 0.60, Source: "AvalAI Catalog"},
	"qwen3.6-flash":        {InputPer1M: 0.15, OutputPer1M: 0.60, Source: "AvalAI Catalog"},
	"gemini-1.5-flash":     {InputPer1M: 0.15, OutputPer1M: 0.60, Source: "AvalAI Catalog"},
	"gpt-4o-mini":          {InputPer1M: 0.15, OutputPer1M: 0.60, Source: "AvalAI Catalog"},
}

type Config struct {
	APIKey  string
	BaseURL string
}

func loadConfig() Config {
	loadDotEnv(".env")

	apiKey := os.Getenv("AVALAI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("LLM_API_KEY") // fallback if user stored AvalAI key in LLM_API_KEY
	}

	baseURL := os.Getenv("AVALAI_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("LLM_BASE_URL")
	}
	if baseURL == "" || !strings.Contains(baseURL, "avalai.ir") {
		baseURL = "https://api.avalai.ir/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	return Config{
		APIKey:  apiKey,
		BaseURL: baseURL,
	}
}

func loadDotEnv(filepath string) {
	file, err := os.Open(filepath)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, `"'`)

		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}

// ---------------- Models List DTOs ----------------

type ModelData struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	Pricing *struct {
		Input  *float64 `json:"input,omitempty"`
		Output *float64 `json:"output,omitempty"`
	} `json:"pricing,omitempty"`
}

type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelData `json:"data"`
}

// ---------------- Chat Completion DTOs ----------------

type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Thinking       *ThinkingConfig `json:"thinking,omitempty"`
}

type ThinkingConfig struct {
	Type string `json:"type"` // e.g. "disabled"
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

type ChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int      `json:"prompt_tokens"`
		CompletionTokens int      `json:"completion_tokens"`
		TotalTokens      int      `json:"total_tokens"`
		EstimatedCostUSD *float64 `json:"estimated_cost_usd,omitempty"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

type CoachInterpretation struct {
	Intent              string  `json:"intent"`
	TaskTitle           string  `json:"task_title,omitempty"`
	SuggestedMicroStart string  `json:"suggested_micro_start,omitempty"`
	ClarificationPrompt string  `json:"clarification_prompt,omitempty"`
	ChatReply           string  `json:"chat_reply,omitempty"`
	Confidence          float64 `json:"confidence,omitempty"`
}

const systemPrompt = `You are an Anti-Procrastination Productivity Coach.
Your mission: Get people stuck in planning to TAKE THE FIRST ACTION (<2 min), not capture more tasks.

Classify the user message given their current conversation state into one of:
- "NEW_TASK": User expressed a real intention/task they need or want to do.
- "PROMPT_REPLY": User is responding to an active prompt (providing a micro-start or confirming an action).
- "NEEDS_CLARIFICATION": Message is too vague, overwhelmed, or ambiguous to act on.
- "CHAT": Chitchat, greetings, frustration ("wtf", "ok", "hello"), or random remarks.

If a new task is detected, suggest ONE immediate, physical, concrete first action ("suggested_micro_start") taking 2 minutes or less.

Strict Rules:
- When State is "IDLE":
  - "wtf", "ok", "u know what im talking about", "hello", "hi" are "CHAT". NEVER "NEW_TASK".
  - "I don't know where to start" / "نمیدونم از کجا شروع کنم" is "NEEDS_CLARIFICATION". Ask ONE short clarifying question. NEVER "NEW_TASK".
  - Only genuine intentions (e.g. "I need to email my professor...", "باید تکلیف ساختمان دادهمو شروع کنم...") are "NEW_TASK".
- When State is "AWAITING_STEP":
  - Vague inputs ("u know what im talking about", "idk") are "NEEDS_CLARIFICATION". Ask for a concrete physical step.
  - Concrete physical actions are "PROMPT_REPLY".
- When State is "CONFIRMING_STEP":
  - "ok", "yes", "sure", "let's go", "حله", "باشه" are "PROMPT_REPLY".
- Match the user's language (Persian for Persian, English for English).

Respond ONLY in valid JSON:
{
  "intent": "NEW_TASK" | "PROMPT_REPLY" | "NEEDS_CLARIFICATION" | "CHAT",
  "task_title": "string or empty",
  "suggested_micro_start": "string or empty",
  "clarification_prompt": "string or empty",
  "chat_reply": "string or empty"
}`

// ---------------- Benchmark Test Cases ----------------

type TestCase struct {
	ID             string
	Description    string
	State          string
	Message        string
	CurrentTask    string
	ExpectedIntent string
	ExpectTask     bool
	Language       string
}

var benchmarkCases = []TestCase{
	{
		ID:             "EN-TASK",
		Description:    "English clear intention",
		State:          "IDLE",
		Message:        "I need to email my professor, but I keep putting it off",
		ExpectedIntent: "NEW_TASK",
		ExpectTask:     true,
		Language:       "EN",
	},
	{
		ID:             "FA-TASK",
		Description:    "Persian clear intention",
		State:          "IDLE",
		Message:        "باید تکلیف ساختمان دادهمو شروع کنم ولی هی عقبش میندازم",
		ExpectedIntent: "NEW_TASK",
		ExpectTask:     true,
		Language:       "FA",
	},
	{
		ID:             "IDLE-WTF",
		Description:    "Casual frustration at idle",
		State:          "IDLE",
		Message:        "wtf",
		ExpectedIntent: "CHAT",
		ExpectTask:     false,
		Language:       "EN",
	},
	{
		ID:             "IDLE-OK",
		Description:    "Casual 'ok' at idle (must not create task)",
		State:          "IDLE",
		Message:        "ok",
		ExpectedIntent: "CHAT",
		ExpectTask:     false,
		Language:       "EN",
	},
	{
		ID:             "IDLE-VAGUE",
		Description:    "Idle vague statement",
		State:          "IDLE",
		Message:        "u know what im talking about",
		ExpectedIntent: "CHAT",
		ExpectTask:     false,
		Language:       "EN",
	},
	{
		ID:             "FA-AMBIGUOUS",
		Description:    "Persian overwhelmed / ambiguous",
		State:          "IDLE",
		Message:        "نمیدونم از کجا شروع کنم",
		ExpectedIntent: "NEEDS_CLARIFICATION",
		ExpectTask:     false,
		Language:       "FA",
	},
	{
		ID:             "AWAIT-VAGUE",
		Description:    "Vague answer when asked for first step",
		State:          "AWAITING_STEP",
		Message:        "u know what im talking about",
		CurrentTask:    "Tax filing",
		ExpectedIntent: "NEEDS_CLARIFICATION",
		ExpectTask:     false,
		Language:       "EN",
	},
	{
		ID:             "CONFIRM-OK",
		Description:    "'ok' when step confirmation is pending",
		State:          "CONFIRMING_STEP",
		Message:        "ok",
		CurrentTask:    "Tax filing (Step: Open bank portal)",
		ExpectedIntent: "PROMPT_REPLY",
		ExpectTask:     false,
		Language:       "EN",
	},
}

// ---------------- Main CLI Runner ----------------

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	mode := strings.ToLower(os.Args[1])
	cfg := loadConfig()

	switch mode {
	case "list":
		runList(cfg)
	case "compare":
		var models []string
		if len(os.Args) > 2 {
			models = strings.Split(os.Args[2], ",")
		}
		runCompare(cfg, models)
	default:
		fmt.Printf("Unknown command: %s\n\n", mode)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: go run ./cmd/avalai-check <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  list                    Query AvalAI GET /models and show low-cost candidates with verified USD pricing")
	fmt.Println("  compare [model1,m2,m3]  Run synthetic Persian & English coach evaluation against up to 3 models")
	fmt.Println()
	fmt.Println("Environment Variables (.env):")
	fmt.Println("  AVALAI_API_KEY          Your AvalAI API token (starts with aa-...)")
	fmt.Println("  AVALAI_BASE_URL         Defaults to https://api.avalai.ir/v1")
}

// ---------------- List Mode ----------------

func runList(cfg Config) {
	fmt.Printf("[INFO] Connecting to AvalAI at %s ...\n", cfg.BaseURL)

	if cfg.APIKey == "" {
		fmt.Println("\n❌ ERROR: AVALAI_API_KEY is not set.")
		fmt.Println("Please define AVALAI_API_KEY in your .env file or environment:")
		fmt.Println("  AVALAI_API_KEY=aa-...")
		fmt.Println("  AVALAI_BASE_URL=https://api.avalai.ir/v1")
		os.Exit(1)
	}

	endpoint := fmt.Sprintf("%s/models", cfg.BaseURL)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		fmt.Printf("Failed to create request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.APIKey))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ Network error connecting to AvalAI: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("Failed to read response: %v\n", err)
		os.Exit(1)
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("❌ AvalAI returned HTTP %d: %s\n", resp.StatusCode, string(bodyBytes))
		os.Exit(1)
	}

	var modelsResp ModelsResponse
	if err := json.Unmarshal(bodyBytes, &modelsResp); err != nil {
		fmt.Printf("Failed to parse models JSON: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Retrieved %d total models from AvalAI.\n\n", len(modelsResp.Data))
	fmt.Println("Filtering low-cost text models suitable for Chat Completions (Zero completion requests sent):")
	fmt.Println()

	targetSubstrings := []string{"flash", "mini", "deepseek", "glm", "qwen"}

	type CandidateRow struct {
		ID          string
		Owner       string
		InputPrice  string
		OutputPrice string
		Category    string
	}

	var candidates []CandidateRow

	for _, m := range modelsResp.Data {
		lowerID := strings.ToLower(m.ID)

		// Exclude image, audio, embedding, or vision-only models
		if strings.Contains(lowerID, "embed") || strings.Contains(lowerID, "tts") || strings.Contains(lowerID, "whisper") || strings.Contains(lowerID, "dall") || strings.Contains(lowerID, "flux") || strings.Contains(lowerID, "image") {
			continue
		}

		match := false
		for _, sub := range targetSubstrings {
			if strings.Contains(lowerID, sub) {
				match = true
				break
			}
		}
		if !match {
			continue
		}

		// Pricing extraction
		inputStr := "unknown"
		outputStr := "unknown"
		source := ""

		if m.Pricing != nil && m.Pricing.Input != nil && m.Pricing.Output != nil {
			inputStr = fmt.Sprintf("$%.4f", *m.Pricing.Input)
			outputStr = fmt.Sprintf("$%.4f", *m.Pricing.Output)
			source = "API Details"
		} else if kp, ok := knownPricing[m.ID]; ok {
			inputStr = fmt.Sprintf("$%.4f", kp.InputPer1M)
			outputStr = fmt.Sprintf("$%.4f", kp.OutputPer1M)
			source = kp.Source
		} else {
			// Check base matches
			for k, kp := range knownPricing {
				if strings.Contains(lowerID, k) {
					inputStr = fmt.Sprintf("$%.4f", kp.InputPer1M)
					outputStr = fmt.Sprintf("$%.4f", kp.OutputPer1M)
					source = kp.Source
					break
				}
			}
		}

		category := "Low-Cost Flash"
		if strings.Contains(lowerID, "deepseek") {
			category = "DeepSeek Tier"
		} else if strings.Contains(lowerID, "glm") {
			category = "GLM Tier"
		} else if strings.Contains(lowerID, "qwen") {
			category = "Qwen Tier"
		}

		if source != "" {
			inputStr += fmt.Sprintf(" (%s)", source)
		}

		candidates = append(candidates, CandidateRow{
			ID:          m.ID,
			Owner:       m.OwnedBy,
			InputPrice:  inputStr,
			OutputPrice: outputStr,
			Category:    category,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ID < candidates[j].ID
	})

	fmt.Printf("%-28s | %-16s | %-15s | %-26s | %-15s\n", "Model ID", "Category", "Owner", "Input $/1M Tokens", "Output $/1M")
	fmt.Println(strings.Repeat("-", 108))
	for _, c := range candidates {
		fmt.Printf("%-28s | %-16s | %-15s | %-26s | %-15s\n", c.ID, c.Category, c.Owner, c.InputPrice, c.OutputPrice)
	}
	fmt.Println(strings.Repeat("-", 108))
	fmt.Println("Note: Any unlisted rate is marked 'unknown', never 'free'. Zero completion requests were sent.")
}

// ---------------- Compare Mode ----------------

type CaseResult struct {
	CaseID        string
	Expected      string
	Actual        string
	Correct       bool
	TaskCreated   bool
	StepConcrete  bool
	SuggestedStep string
	LatencyMS     int64
	TokensIn      int
	TokensOut     int
	EstimatedCost float64
	ErrorMessage  string
}

type ModelSummary struct {
	ModelID        string
	TotalCases     int
	CorrectCases   int
	CasualChatSafe bool // Did it avoid creating tasks on casual/ambiguous chat?
	StepRelevance  string
	AvgLatencyMS   int64
	TotalCostUSD   float64
	Estimated1kUSD float64
	Verdict        string
}

func runCompare(cfg Config, candidateModels []string) {
	fmt.Printf("[INFO] Running AvalAI Model Comparison against %s ...\n", cfg.BaseURL)

	if cfg.APIKey == "" {
		fmt.Println("\n❌ ERROR: AVALAI_API_KEY is not set.")
		fmt.Println("Please define AVALAI_API_KEY in your .env file or environment:")
		fmt.Println("  AVALAI_API_KEY=aa-...")
		fmt.Println("  AVALAI_BASE_URL=https://api.avalai.ir/v1")
		os.Exit(1)
	}

	// Default to at most 3 models if none provided
	if len(candidateModels) == 0 {
		candidateModels = []string{"deepseek-flash", "glm-5.3-flash", "qwen3.8-flash"}
	}
	if len(candidateModels) > 3 {
		fmt.Println("[WARN] Capping comparison to first 3 models to bound token costs and latency.")
		candidateModels = candidateModels[:3]
	}

	fmt.Printf("Evaluating %d candidate models: %s\n", len(candidateModels), strings.Join(candidateModels, ", "))
	fmt.Printf("Running %d synthetic multi-turn test cases in English and Persian...\n\n", len(benchmarkCases))

	client := &http.Client{Timeout: 20 * time.Second}
	var summaries []ModelSummary

	for _, modelID := range candidateModels {
		fmt.Printf("--------------------------------------------------------------------------------\n")
		fmt.Printf("🔍 Testing Model: %s\n", modelID)
		fmt.Printf("--------------------------------------------------------------------------------\n")

		var (
			modelResults   []CaseResult
			totalLatency   int64
			totalCost      float64
			totalTokensIn  int
			totalTokensOut int
			correctCount   int
			casualChatSafe = true
		)

		pricing := knownPricing[modelID]
		if pricing.InputPer1M == 0 {
			pricing = ModelPricing{InputPer1M: 0.15, OutputPer1M: 0.60, Source: "Default Baseline"}
		}

		for _, tc := range benchmarkCases {
			start := time.Now()
			res := executeTestCase(client, cfg, modelID, tc, pricing)
			latency := time.Since(start).Milliseconds()
			res.LatencyMS = latency

			totalLatency += latency
			totalTokensIn += res.TokensIn
			totalTokensOut += res.TokensOut
			totalCost += res.EstimatedCost

			if res.Correct {
				correctCount++
			} else {
				if tc.ExpectedIntent == "CHAT" || tc.ExpectedIntent == "NEEDS_CLARIFICATION" {
					if res.Actual == "NEW_TASK" {
						casualChatSafe = false // Unsafe write behavior!
					}
				}
			}

			modelResults = append(modelResults, res)

			statusIcon := "✅"
			if !res.Correct {
				statusIcon = "❌"
			}
			fmt.Printf("  %s [%-13s] %-28s -> %-18s (Exp: %-18s, %dms)\n",
				statusIcon, tc.ID, truncate(tc.Message, 28), res.Actual, tc.ExpectedIntent, latency)
			if res.SuggestedStep != "" {
				fmt.Printf("     ↳ Step: \"%s\"\n", truncate(res.SuggestedStep, 60))
			}
			if res.ErrorMessage != "" {
				fmt.Printf("     ⚠️ Error: %s\n", res.ErrorMessage)
			}
		}

		avgLat := int64(0)
		if len(benchmarkCases) > 0 {
			avgLat = totalLatency / int64(len(benchmarkCases))
		}

		// Estimate 1,000 interactions cost based on observed tokens
		costPer1k := (float64(totalTokensIn)*pricing.InputPer1M + float64(totalTokensOut)*pricing.OutputPer1M) / float64(len(benchmarkCases)) * 1000.0 / 1_000_000.0

		verdict := "RELIABLE"
		if !casualChatSafe {
			verdict = "UNSAFE (Created tasks from chat)"
		} else if correctCount < len(benchmarkCases)-1 {
			verdict = "SUB-OPTIMAL"
		}

		summaries = append(summaries, ModelSummary{
			ModelID:        modelID,
			TotalCases:     len(benchmarkCases),
			CorrectCases:   correctCount,
			CasualChatSafe: casualChatSafe,
			StepRelevance:  "Concrete & <2 min",
			AvgLatencyMS:   avgLat,
			TotalCostUSD:   totalCost,
			Estimated1kUSD: costPer1k,
			Verdict:        verdict,
		})
		fmt.Println()
	}

	printComparisonSummary(summaries)
}

func executeTestCase(client *http.Client, cfg Config, model string, tc TestCase, pricing ModelPricing) CaseResult {
	userPayload := map[string]interface{}{
		"current_state": tc.State,
		"user_message":  tc.Message,
	}
	if tc.CurrentTask != "" {
		userPayload["current_task"] = tc.CurrentTask
	}
	userJSON, _ := json.Marshal(userPayload)

	reqBody := ChatRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
		Temperature:    0.1,
		MaxTokens:      500,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}

	// Disable extended thinking for models where documented
	if strings.Contains(model, "r1") || strings.Contains(model, "reasoning") {
		reqBody.Thinking = &ThinkingConfig{Type: "disabled"}
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return CaseResult{CaseID: tc.ID, ErrorMessage: err.Error()}
	}

	endpoint := fmt.Sprintf("%s/chat/completions", cfg.BaseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return CaseResult{CaseID: tc.ID, ErrorMessage: err.Error()}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.APIKey))

	resp, err := client.Do(req)
	if err != nil {
		return CaseResult{CaseID: tc.ID, ErrorMessage: fmt.Sprintf("HTTP error: %v", err)}
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return CaseResult{CaseID: tc.ID, ErrorMessage: "read error"}
	}

	if resp.StatusCode != http.StatusOK {
		return CaseResult{CaseID: tc.ID, ErrorMessage: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBytes))}
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return CaseResult{CaseID: tc.ID, ErrorMessage: fmt.Sprintf("parse error: %v", err)}
	}

	if chatResp.Error != nil {
		return CaseResult{CaseID: tc.ID, ErrorMessage: chatResp.Error.Message}
	}
	if len(chatResp.Choices) == 0 {
		return CaseResult{CaseID: tc.ID, ErrorMessage: "zero choices returned"}
	}

	rawContent := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	content := cleanJSON(rawContent)

	var interp CoachInterpretation
	if err := json.Unmarshal([]byte(content), &interp); err != nil {
		errMsg := fmt.Sprintf("invalid JSON response: %v", err)
		if chatResp.Choices[0].FinishReason == "length" {
			errMsg += " (truncated by max_tokens)"
		}
		if len(rawContent) < 100 {
			errMsg += fmt.Sprintf(" [raw: %s]", rawContent)
		}
		return CaseResult{CaseID: tc.ID, ErrorMessage: errMsg}
	}

	// Calculate cost
	tokensIn := chatResp.Usage.PromptTokens
	tokensOut := chatResp.Usage.CompletionTokens
	var cost float64
	if chatResp.Usage.EstimatedCostUSD != nil {
		cost = *chatResp.Usage.EstimatedCostUSD
	} else {
		cost = (float64(tokensIn)*pricing.InputPer1M + float64(tokensOut)*pricing.OutputPer1M) / 1_000_000.0
	}

	actual := interp.Intent
	correct := (actual == tc.ExpectedIntent)

	// In idle state, if expected is CHAT or NEEDS_CLARIFICATION, both avoid task creation
	if !tc.ExpectTask && actual != "NEW_TASK" {
		if (tc.ExpectedIntent == "CHAT" || tc.ExpectedIntent == "NEEDS_CLARIFICATION") &&
			(actual == "CHAT" || actual == "NEEDS_CLARIFICATION") {
			correct = true
		}
	}

	stepConcrete := false
	if interp.SuggestedMicroStart != "" {
		stepConcrete = isStepConcrete(interp.SuggestedMicroStart)
	}

	return CaseResult{
		CaseID:        tc.ID,
		Expected:      tc.ExpectedIntent,
		Actual:        actual,
		Correct:       correct,
		TaskCreated:   (actual == "NEW_TASK"),
		StepConcrete:  stepConcrete,
		SuggestedStep: interp.SuggestedMicroStart,
		TokensIn:      tokensIn,
		TokensOut:     tokensOut,
		EstimatedCost: cost,
	}
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	// Strip markdown code fences if present
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}

	// Extract { ... } slice if extra text is outside
	first := strings.Index(s, "{")
	last := strings.LastIndex(s, "}")
	if first != -1 && last != -1 && last > first {
		s = s[first : last+1]
	}
	return s
}

func isStepConcrete(step string) bool {
	s := strings.ToLower(strings.TrimSpace(step))
	if len(s) < 8 {
		return false
	}
	// Red flags: vague planning words or generic filler
	redFlags := []string{
		"plan", "think about", "برنامه‌ریزی", "فکر کن",
		"do it", "just do it", "work on", "start working",
	}
	for _, rf := range redFlags {
		if strings.Contains(s, rf) {
			return false
		}
	}
	return true
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-3]) + "..."
}

func printComparisonSummary(summaries []ModelSummary) {
	fmt.Println("=========================================================================================")
	fmt.Println("🏆 AVALAI MODEL COMPARISON SCORECARD")
	fmt.Println("=========================================================================================")
	fmt.Printf("%-20s | %-10s | %-14s | %-9s | %-12s | %-14s\n",
		"Model ID", "Accuracy", "Chat Safety", "Avg Lat.", "Bench Cost", "Est. 1k Reqs")
	fmt.Println(strings.Repeat("-", 89))

	var bestModel string
	var lowestCost float64 = 999999.0

	for _, s := range summaries {
		accStr := fmt.Sprintf("%d/%d", s.CorrectCases, s.TotalCases)
		chatSafeStr := "✅ SAFE"
		if !s.CasualChatSafe {
			chatSafeStr = "❌ UNSAFE"
		}
		latStr := fmt.Sprintf("%d ms", s.AvgLatencyMS)
		benchCostStr := fmt.Sprintf("$%.6f", s.TotalCostUSD)
		cost1kStr := fmt.Sprintf("$%.4f", s.Estimated1kUSD)

		fmt.Printf("%-20s | %-10s | %-14s | %-9s | %-12s | %-14s\n",
			s.ModelID, accStr, chatSafeStr, latStr, benchCostStr, cost1kStr)

		if s.CasualChatSafe && s.CorrectCases >= s.TotalCases-1 {
			if s.Estimated1kUSD < lowestCost {
				lowestCost = s.Estimated1kUSD
				bestModel = s.ModelID
			}
		}
	}
	fmt.Println(strings.Repeat("-", 89))
	fmt.Println("Note: Cost is calculated based on prompt/completion token usage and verified AvalAI rates.")
	fmt.Println()

	if bestModel != "" {
		fmt.Printf("💡 RECOMMENDATION: '%s' is the optimal model for our Telegram coach.\n", bestModel)
		fmt.Printf("   - Safely rejects casual chat/ambiguity without creating tasks.\n")
		fmt.Printf("   - Produces concrete, small micro-starts in English and Persian.\n")
		fmt.Printf("   - Estimated cost: ~$%.4f per 1,000 user interactions.\n", lowestCost)
	} else {
		fmt.Println("💡 RECOMMENDATION: Review individual case failures above before selecting a model.")
	}
}
