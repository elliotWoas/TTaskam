package memory

import (
	"context"
	"fmt"
	"sync"
	"time"
	"ttaskam/internal/domain"
)

type MemoryRepo struct {
	mu           sync.Mutex
	tasks        map[string]*domain.Task
	sessions     map[string]*domain.FocusSession
	idempotency  map[string]string // key: "userID:idempotencyKey" -> taskID
	convs        map[int64]*domain.ConversationContext
	nextTaskID   int
	nextSessID   int
	SimulateFail bool // Injected failure to test partial-write handling
}

func NewMemoryRepo() *MemoryRepo {
	return &MemoryRepo{
		tasks:       make(map[string]*domain.Task),
		sessions:    make(map[string]*domain.FocusSession),
		idempotency: make(map[string]string),
		convs:       make(map[int64]*domain.ConversationContext),
	}
}

// ---------------- TaskRepository ----------------

func (r *MemoryRepo) Create(ctx context.Context, task *domain.Task, idempotencyKey string) (*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.SimulateFail {
		return nil, domain.ErrDatabaseUnavailable
	}

	if idempotencyKey != "" {
		dedupKey := fmt.Sprintf("%d:%s", task.UserID, idempotencyKey)
		if existingID, ok := r.idempotency[dedupKey]; ok {
			if existingTask, exists := r.tasks[existingID]; exists {
				return existingTask, nil
			}
		}
	}

	r.nextTaskID++
	task.ID = fmt.Sprintf("task-%d", r.nextTaskID)
	cloned := *task
	r.tasks[task.ID] = &cloned

	if idempotencyKey != "" {
		dedupKey := fmt.Sprintf("%d:%s", task.UserID, idempotencyKey)
		r.idempotency[dedupKey] = task.ID
	}

	return &cloned, nil
}

func (r *MemoryRepo) GetByID(ctx context.Context, id string, userID int64) (*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	task, ok := r.tasks[id]
	if !ok || task.UserID != userID {
		return nil, domain.ErrTaskNotFound
	}
	cloned := *task
	return &cloned, nil
}

func (r *MemoryRepo) Update(ctx context.Context, task *domain.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.tasks[task.ID]
	if !ok || existing.UserID != task.UserID {
		return domain.ErrTaskNotFound
	}
	cloned := *task
	r.tasks[task.ID] = &cloned
	return nil
}

func (r *MemoryRepo) ArchiveExpired(ctx context.Context, userID int64, now time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var count int64
	for _, t := range r.tasks {
		if t.UserID == userID && t.IsExpired(now) {
			_ = t.Archive(now)
			count++
		}
	}
	return count, nil
}

func (r *MemoryRepo) ListActive(ctx context.Context, userID int64) ([]*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var list []*domain.Task
	for _, t := range r.tasks {
		if t.UserID == userID && t.Status != domain.TaskStatusCompleted && t.Status != domain.TaskStatusArchived {
			cloned := *t
			list = append(list, &cloned)
		}
	}
	return list, nil
}

func (r *MemoryRepo) GetByTimeRange(ctx context.Context, userID int64, start, end time.Time) ([]*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.SimulateFail {
		return nil, domain.ErrDatabaseUnavailable
	}

	var list []*domain.Task
	for _, t := range r.tasks {
		if t.UserID == userID && !t.CreatedAt.Before(start) && t.CreatedAt.Before(end) {
			cloned := *t
			list = append(list, &cloned)
		}
	}
	return list, nil
}

// ---------------- SessionRepository ----------------

func (r *MemoryRepo) GetOpenSession(ctx context.Context, userID int64) (*domain.FocusSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range r.sessions {
		if s.UserID == userID && s.IsOpen() {
			cloned := *s
			return &cloned, nil
		}
	}
	return nil, nil
}

func (r *MemoryRepo) GetSessionByID(ctx context.Context, id string, userID int64) (*domain.FocusSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[id]
	if !ok || s.UserID != userID {
		return nil, domain.ErrNoActiveSession
	}
	cloned := *s
	return &cloned, nil
}

func (r *MemoryRepo) SetExitReason(ctx context.Context, sessionID string, userID int64, reason string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[sessionID]
	if !ok || s.UserID != userID {
		return nil // Safe fail: optional exit reason must never block
	}
	_ = s.SetExitReason(reason, now)
	return nil
}

// ---------------- FocusTransactionManager ----------------

func (r *MemoryRepo) StartFocusAtomic(ctx context.Context, userID int64, taskID string, now time.Time) (*domain.FocusSession, *domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 1. One-open-session invariant
	for _, s := range r.sessions {
		if s.UserID == userID && s.IsOpen() {
			return nil, nil, domain.ErrFocusTunnelActive
		}
	}

	// 2. Task validation
	task, ok := r.tasks[taskID]
	if !ok || task.UserID != userID {
		return nil, nil, domain.ErrTaskNotFound
	}
	if err := task.CanStart(now); err != nil {
		return nil, nil, err
	}

	// Injected failure check to simulate partial write failure / transaction abort
	if r.SimulateFail {
		return nil, nil, domain.ErrTransactionAborted
	}

	// 3. Update task
	taskCopy := *task
	if err := taskCopy.MarkStarted(now); err != nil {
		return nil, nil, err
	}

	// 4. Create session
	r.nextSessID++
	session := domain.NewFocusSession(userID, taskID, now)
	session.ID = fmt.Sprintf("sess-%d", r.nextSessID)

	// Commit
	r.tasks[taskID] = &taskCopy
	sessionCopy := *session
	r.sessions[session.ID] = &sessionCopy

	return session, &taskCopy, nil
}

func (r *MemoryRepo) CompleteFocusAtomic(ctx context.Context, userID int64, sessionID string, expectedVersion int, now time.Time) (*domain.FocusSession, *domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.sessions[sessionID]
	if !ok || session.UserID != userID {
		return nil, nil, domain.ErrNoActiveSession
	}
	if !session.IsOpen() {
		return nil, nil, domain.ErrNoActiveSession
	}
	if session.Version != expectedVersion {
		return nil, nil, domain.ErrVersionMismatch
	}

	task, ok := r.tasks[session.TaskID]
	if !ok || task.UserID != userID {
		return nil, nil, domain.ErrTaskNotFound
	}

	if r.SimulateFail {
		return nil, nil, domain.ErrTransactionAborted
	}

	sessCopy := *session
	if err := sessCopy.Complete(expectedVersion, now); err != nil {
		return nil, nil, err
	}

	taskCopy := *task
	if err := taskCopy.MarkCompleted(now); err != nil {
		return nil, nil, err
	}

	// Commit
	r.sessions[sessionID] = &sessCopy
	r.tasks[task.ID] = &taskCopy

	return &sessCopy, &taskCopy, nil
}

func (r *MemoryRepo) PauseFocus(ctx context.Context, userID int64, sessionID string, expectedVersion int, now time.Time) (*domain.FocusSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.sessions[sessionID]
	if !ok || session.UserID != userID {
		return nil, domain.ErrNoActiveSession
	}
	if session.Version != expectedVersion {
		return nil, domain.ErrVersionMismatch
	}

	sessCopy := *session
	if err := sessCopy.Pause(expectedVersion, now); err != nil {
		return nil, err
	}

	r.sessions[sessionID] = &sessCopy
	return &sessCopy, nil
}

func (r *MemoryRepo) ResumeFocus(ctx context.Context, userID int64, sessionID string, expectedVersion int, now time.Time) (*domain.FocusSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.sessions[sessionID]
	if !ok || session.UserID != userID {
		return nil, domain.ErrNoActiveSession
	}
	if session.Version != expectedVersion {
		return nil, domain.ErrVersionMismatch
	}

	sessCopy := *session
	if err := sessCopy.Resume(expectedVersion, now); err != nil {
		return nil, err
	}

	r.sessions[sessionID] = &sessCopy
	return &sessCopy, nil
}

func (r *MemoryRepo) EndFocus(ctx context.Context, userID int64, sessionID string, expectedVersion int, now time.Time) (*domain.FocusSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.sessions[sessionID]
	if !ok || session.UserID != userID {
		return nil, domain.ErrNoActiveSession
	}
	if session.Version != expectedVersion {
		return nil, domain.ErrVersionMismatch
	}

	sessCopy := *session
	if err := sessCopy.End(expectedVersion, now); err != nil {
		return nil, err
	}

	// Release slot immediately
	r.sessions[sessionID] = &sessCopy
	return &sessCopy, nil
}

// ---------------- ConversationStore ----------------

func (r *MemoryRepo) Get(ctx context.Context, userID int64) (*domain.ConversationContext, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.convs[userID]
	if !ok {
		return nil, nil
	}
	cloned := *c
	return &cloned, nil
}

func (r *MemoryRepo) Set(ctx context.Context, cCtx *domain.ConversationContext) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cCtx == nil {
		delete(r.convs, cCtx.UserID)
		return nil
	}
	cloned := *cCtx
	r.convs[cCtx.UserID] = &cloned
	return nil
}

func (r *MemoryRepo) Clear(ctx context.Context, userID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.convs, userID)
	return nil
}
