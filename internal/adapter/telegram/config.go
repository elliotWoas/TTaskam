package telegram

import (
	"bufio"
	"os"
	"strings"
)

type Config struct {
	TelegramBotToken string
	MongoURI         string
	MongoDBName      string
	LLMAPIKey        string
	LLMBaseURL       string
	LLMModel         string
}

// LoadConfig loads configuration from environment variables, automatically loading from .env if present.
func LoadConfig() Config {
	loadDotEnv(".env")

	dbName := os.Getenv("MONGO_DB_NAME")
	if dbName == "" {
		dbName = "ttaskam"
	}

	baseURL := os.Getenv("LLM_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("AVALAI_BASE_URL")
	}
	if baseURL == "" {
		baseURL = "https://api.avalai.ir/v1"
	}

	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = "deepseek-flash"
	}

	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("AVALAI_API_KEY")
	}

	return Config{
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		MongoURI:         os.Getenv("MONGO_URI"),
		MongoDBName:      dbName,
		LLMAPIKey:        apiKey,
		LLMBaseURL:       baseURL,
		LLMModel:         model,
	}
}

// loadDotEnv parses a local .env file without overriding existing process environment variables.
func loadDotEnv(filepath string) {
	paths := []string{filepath, "../.env", "../../.env", "../../../.env"}
	var file *os.File
	var err error
	for _, p := range paths {
		file, err = os.Open(p)
		if err == nil {
			break
		}
	}
	if file == nil {
		return // File does not exist, silently proceed
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
		val = strings.Trim(val, `"'`) // remove surrounding quotes

		// Only set if not already present in the environment
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}
