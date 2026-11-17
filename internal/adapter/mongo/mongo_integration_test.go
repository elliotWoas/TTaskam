package mongo_test

import (
	"context"
	"testing"
	"time"

	adapterMongo "ttaskam/internal/adapter/mongo"
	"ttaskam/internal/adapter/telegram"
	"ttaskam/internal/domain"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestMongo_Integration(t *testing.T) {
	cfg := telegram.LoadConfig()
	mongoURI := cfg.MongoURI
	if mongoURI == "" {
		t.Skip("MONGO_URI is not set. Skipping live MongoDB Atlas integration tests.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		t.Fatalf("failed to connect to MongoDB: %v", err)
	}
	defer client.Disconnect(ctx)

	testDBName := "test_ttaskam_" + time.Now().Format("20060102150405")
	db := client.Database(testDBName)
	defer db.Drop(ctx)

	// Ensure Indexes
	if err := adapterMongo.EnsureIndexes(ctx, db); err != nil {
		t.Fatalf("failed to ensure indexes: %v", err)
	}

	taskRepo := adapterMongo.NewTaskRepo(client, testDBName)
	sessionRepo := adapterMongo.NewSessionRepo(client, testDBName)
	txManager := adapterMongo.NewFocusTxManager(client, testDBName)

	now := time.Now().UTC()
	userID := int64(99901)

	// 1. Task Creation with Idempotency
	task, err := domain.NewTask(userID, "Mongo Task 1", "Step 1", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	createdTask, err := taskRepo.Create(ctx, task, "idem-101")
	if err != nil {
		t.Fatalf("failed to create task in mongo: %v", err)
	}

	// Replay creation with same idempotency key
	replayedTask, err := taskRepo.Create(ctx, task, "idem-101")
	if err != nil {
		t.Fatalf("failed to replay idempotent task creation: %v", err)
	}
	if createdTask.ID != replayedTask.ID {
		t.Errorf("expected same task ID on idempotent replay in Mongo, got %s vs %s", createdTask.ID, replayedTask.ID)
	}

	// 2. Atomic Start
	sess, updatedTask, err := txManager.StartFocusAtomic(ctx, userID, createdTask.ID, now)
	if err != nil {
		t.Fatalf("failed to start focus in Mongo: %v", err)
	}
	if sess.Status != domain.SessionStatusActive {
		t.Errorf("expected session ACTIVE, got %s", sess.Status)
	}
	if updatedTask.Status != domain.TaskStatusStarted {
		t.Errorf("expected task STARTED, got %s", updatedTask.Status)
	}

	// 3. One-Open-Session Rule Enforced by Partial Unique Index
	task2, _ := domain.NewTask(userID, "Mongo Task 2", "Step 2", now)
	createdTask2, _ := taskRepo.Create(ctx, task2, "idem-102")
	_, _, err = txManager.StartFocusAtomic(ctx, userID, createdTask2.ID, now)
	if err != domain.ErrFocusTunnelActive {
		t.Errorf("expected ErrFocusTunnelActive, got: %v", err)
	}

	// 4. Pause and Resume with Version Check
	pausedSess, err := txManager.PauseFocus(ctx, userID, sess.ID, sess.Version, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("failed to pause session: %v", err)
	}
	if pausedSess.Status != domain.SessionStatusPaused {
		t.Errorf("expected PAUSED, got %s", pausedSess.Status)
	}

	resumedSess, err := txManager.ResumeFocus(ctx, userID, sess.ID, pausedSess.Version, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("failed to resume session: %v", err)
	}
	if resumedSess.Status != domain.SessionStatusActive {
		t.Errorf("expected ACTIVE, got %s", resumedSess.Status)
	}

	// 5. Atomic Complete
	completedSess, completedTask, err := txManager.CompleteFocusAtomic(ctx, userID, sess.ID, resumedSess.Version, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("failed to complete focus atomically: %v", err)
	}
	if completedSess.Status != domain.SessionStatusCompleted {
		t.Errorf("expected session COMPLETED, got %s", completedSess.Status)
	}
	if completedTask.Status != domain.TaskStatusCompleted {
		t.Errorf("expected task COMPLETED, got %s", completedTask.Status)
	}

	// Verify open session is now nil
	openSess, err := sessionRepo.GetOpenSession(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if openSess != nil {
		t.Errorf("expected no open session after completion")
	}

	// 6. Subtask Persistence and Isolation
	parentTask, _ := domain.NewTask(userID, "دیوار هنر", "", now)
	createdParent, err := taskRepo.Create(ctx, parentTask, "idem-subtask-test")
	if err != nil {
		t.Fatalf("failed to create parent task in mongo: %v", err)
	}

	_, err = createdParent.AddSubtask("آماده کردن متن تماس با کافهها", now)
	if err != nil {
		t.Fatalf("failed to add subtask to domain model: %v", err)
	}
	if err := taskRepo.Update(ctx, createdParent); err != nil {
		t.Fatalf("failed to update parent task with subtask in mongo: %v", err)
	}

	// Re-fetch parent task from Mongo to verify persistence across restarts
	refetchedParent, err := taskRepo.GetByID(ctx, createdParent.ID, userID)
	if err != nil {
		t.Fatalf("failed to refetch parent task from mongo: %v", err)
	}
	if len(refetchedParent.Subtasks) != 1 {
		t.Fatalf("expected 1 subtask persisted in mongo, got %d", len(refetchedParent.Subtasks))
	}
	if refetchedParent.Subtasks[0].Title != "آماده کردن متن تماس با کافهها" {
		t.Errorf("expected subtask title 'آماده کردن متن تماس با کافهها', got '%s'", refetchedParent.Subtasks[0].Title)
	}

	// Verify subtask is not returned as a separate top-level task in ListActive
	activeTasks, err := taskRepo.ListActive(ctx, userID)
	if err != nil {
		t.Fatalf("failed to list active tasks: %v", err)
	}
	// There is task2 and parentTask = 2 active tasks total for this user
	parentCount := 0
	for _, taskItem := range activeTasks {
		if taskItem.Title == "دیوار هنر" {
			parentCount++
		}
		if taskItem.Title == "آماده کردن متن تماس با کافهها" {
			t.Errorf("subtask must NEVER be listed as a top-level active task")
		}
	}
	if parentCount != 1 {
		t.Errorf("expected exactly 1 parent task 'دیوار هنر', got %d", parentCount)
	}

	// 7. GetByTimeRange for Calendar Day Scoped Query
	yesterdayStart := now.Add(-24 * time.Hour)
	yesterdayEnd := now.Add(-1 * time.Hour)
	yesterdayTasks, err := taskRepo.GetByTimeRange(ctx, userID, yesterdayStart, yesterdayEnd)
	if err != nil {
		t.Fatalf("failed to query time range in mongo: %v", err)
	}
	// No tasks were created in that range
	if len(yesterdayTasks) != 0 {
		t.Errorf("expected 0 tasks in past time range, got %d", len(yesterdayTasks))
	}

	// Query matching now
	currentTasks, err := taskRepo.GetByTimeRange(ctx, userID, now.Add(-10*time.Minute), now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("failed to query current time range: %v", err)
	}
	if len(currentTasks) == 0 {
		t.Errorf("expected tasks found in current time range, got 0")
	}

	// 8. ConversationRepo Context Persistence in Mongo
	convRepo := adapterMongo.NewConversationRepo(client, testDBName)
	cCtx := &domain.ConversationContext{
		UserID:        userID,
		State:         domain.StateAwaitingClarification,
		PendingTitle:  "دیوار هنر",
		LastPrompt:    "منظورت از «دیوار هنر» چیه؟ میخوای چی کار کنی؟",
		ExpectedReply: "CLARIFY_INTENTION",
		UpdatedAt:     now,
	}
	if err := convRepo.Set(ctx, cCtx); err != nil {
		t.Fatalf("failed to set conversation context in mongo: %v", err)
	}

	fetchedConv, err := convRepo.Get(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get conversation context: %v", err)
	}
	if fetchedConv == nil || fetchedConv.PendingTitle != "دیوار هنر" || fetchedConv.State != domain.StateAwaitingClarification {
		t.Fatalf("expected preserved conversation context in mongo, got %+v", fetchedConv)
	}

	if err := convRepo.Clear(ctx, userID); err != nil {
		t.Fatalf("failed to clear conversation context: %v", err)
	}
	clearedConv, err := convRepo.Get(ctx, userID)
	if err != nil || clearedConv != nil {
		t.Errorf("expected nil conversation context after clear, got %+v", clearedConv)
	}
}
