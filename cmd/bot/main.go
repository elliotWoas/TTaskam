package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ttaskam/internal/adapter/llm"
	mongoAdapter "ttaskam/internal/adapter/mongo"
	"ttaskam/internal/adapter/telegram"
	"ttaskam/internal/domain"
	"ttaskam/internal/service"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func main() {
	cfg := telegram.LoadConfig()

	if cfg.TelegramBotToken == "" {
		log.Fatal("FATAL: TELEGRAM_BOT_TOKEN is not set. Please export it or define it in .env (see .env.example).")
	}
	if cfg.MongoURI == "" {
		log.Fatal("FATAL: MONGO_URI is not set. Please export it or define it in .env (see .env.example).")
	}

	log.Println("[INFO] Connecting to MongoDB Atlas...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize MongoDB client: %v", err)
	}

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		log.Fatalf("FATAL: Failed to ping MongoDB Atlas: %v", err)
	}
	log.Printf("[INFO] Connected to MongoDB Atlas successfully (Database: %s)", cfg.MongoDBName)

	db := client.Database(cfg.MongoDBName)

	// Ensure domain indexes (partial unique index, idempotency TTL)
	log.Println("[INFO] Ensuring MongoDB indexes...")
	if err := mongoAdapter.EnsureIndexes(ctx, db); err != nil {
		log.Fatalf("FATAL: Failed to ensure MongoDB indexes: %v", err)
	}
	log.Println("[INFO] MongoDB indexes verified.")

	// Wire repositories & transaction manager
	taskRepo := mongoAdapter.NewTaskRepo(client, cfg.MongoDBName)
	sessionRepo := mongoAdapter.NewSessionRepo(client, cfg.MongoDBName)
	txManager := mongoAdapter.NewFocusTxManager(client, cfg.MongoDBName)
	convRepo := mongoAdapter.NewConversationRepo(client, cfg.MongoDBName)

	// Wire AI provider if configured
	var ai domain.AIInterpreter
	if cfg.LLMAPIKey != "" {
		log.Printf("[INFO] Initializing LLM provider (Model: %s, BaseURL: %s)...", cfg.LLMModel, cfg.LLMBaseURL)
		llmClient := llm.NewClient(llm.Config{
			BaseURL: cfg.LLMBaseURL,
			APIKey:  cfg.LLMAPIKey,
			Model:   cfg.LLMModel,
		})

		log.Println("[INFO] Verifying live LLM API connection with AvalAI...")
		verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 15*time.Second)
		interp, err := llmClient.Verify(verifyCtx)
		verifyCancel()
		if err != nil {
			log.Fatalf("FATAL: Live LLM verification failed for %s (%s): %v", cfg.LLMBaseURL, cfg.LLMModel, err)
		}
		log.Printf("[INFO] LLM provider verified successfully via live API response (Model: %s, Response Intent: %s)", cfg.LLMModel, interp.Intent)
		ai = llmClient
	} else {
		log.Println("[INFO] No LLM API key detected. Running in pure deterministic manual mode (AI disabled).")
	}

	// Wire application services
	focusSvc := service.NewFocusService(txManager, sessionRepo, taskRepo)
	taskSvc := service.NewTaskService(taskRepo, sessionRepo, ai)

	// Wire Telegram Bot adapter
	log.Println("[INFO] Initializing Telegram bot long poller...")
	bot, err := telegram.NewBot(cfg.TelegramBotToken, focusSvc, taskSvc, convRepo)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize Telegram bot: %v", err)
	}

	// Graceful shutdown channel
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Println("[INFO] Telegram productivity coach is running (long polling started). Press Ctrl+C to stop.")
		bot.Start()
	}()

	<-stopChan
	log.Println("[INFO] Shutdown signal received. Stopping bot gracefully...")

	bot.Stop()

	disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer disconnectCancel()
	_ = client.Disconnect(disconnectCtx)

	log.Println("[INFO] Bot stopped cleanly. Exiting.")
	fmt.Println("Goodbye!")
}
