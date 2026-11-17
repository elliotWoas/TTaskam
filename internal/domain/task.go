package domain

import (
	"fmt"
	"strings"
	"time"
)

type TaskStatus string

const (
	TaskStatusCreated   TaskStatus = "CREATED"
	TaskStatusStarted   TaskStatus = "STARTED"
	TaskStatusCompleted TaskStatus = "COMPLETED"
	TaskStatusArchived  TaskStatus = "ARCHIVED"

	TaskDecayDuration = 72 * time.Hour
)

type Subtask struct {
	ID        string    `json:"id" bson:"id"`
	Title     string    `json:"title" bson:"title"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
}

type Task struct {
	ID          string     `json:"id" bson:"_id,omitempty"`
	UserID      int64      `json:"user_id" bson:"user_id"`
	Title       string     `json:"title" bson:"title"`
	MicroStart  string     `json:"micro_start" bson:"micro_start"`
	Subtasks    []Subtask  `json:"subtasks,omitempty" bson:"subtasks,omitempty"`
	Status      TaskStatus `json:"status" bson:"status"`
	CreatedAt   time.Time  `json:"created_at" bson:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at" bson:"expires_at"`
	StartedAt   *time.Time `json:"started_at,omitempty" bson:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty" bson:"completed_at,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty" bson:"archived_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at" bson:"updated_at"`
}

// NewTask constructs a new Task in CREATED status with a 72-hour decay deadline.
func NewTask(userID int64, title, microStart string, now time.Time) (*Task, error) {
	trimmedTitle := strings.TrimSpace(title)
	if trimmedTitle == "" {
		return nil, ErrEmptyTitle
	}

	trimmedMicroStart := strings.TrimSpace(microStart)

	nowUTC := now.UTC()
	return &Task{
		UserID:     userID,
		Title:      trimmedTitle,
		MicroStart: trimmedMicroStart,
		Status:     TaskStatusCreated,
		CreatedAt:  nowUTC,
		ExpiresAt:  nowUTC.Add(TaskDecayDuration),
		UpdatedAt:  nowUTC,
	}, nil
}

// SetMicroStart updates the concrete first action on an uncompleted task.
func (t *Task) SetMicroStart(action string, now time.Time) error {
	trimmed := strings.TrimSpace(action)
	if trimmed == "" {
		return ErrEmptyMicroStart
	}
	if t.Status == TaskStatusCompleted || t.Status == TaskStatusArchived {
		return ErrInvalidTaskState
	}
	t.MicroStart = trimmed
	t.UpdatedAt = now.UTC()
	return nil
}

// AddSubtask adds a child subtask to an uncompleted parent task.
func (t *Task) AddSubtask(title string, now time.Time) (*Subtask, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return nil, ErrEmptyTitle
	}
	if t.Status == TaskStatusCompleted || t.Status == TaskStatusArchived {
		return nil, ErrInvalidTaskState
	}
	nowUTC := now.UTC()
	sub := Subtask{
		ID:        fmt.Sprintf("sub-%d-%d", t.UserID, time.Now().UnixNano()),
		Title:     trimmed,
		CreatedAt: nowUTC,
	}
	t.Subtasks = append(t.Subtasks, sub)
	t.UpdatedAt = nowUTC
	return &sub, nil
}

// IsExpired checks whether this task has expired under the 72-hour decay rule.
// Critical rule: 72-hour decay applies ONLY to never-started tasks (status == CREATED).
// An unstarted task expires when now >= expires_at (exact cutoff).
// Tasks that have already been started (status == STARTED) do NOT expire under this rule.
func (t *Task) IsExpired(now time.Time) bool {
	if t.Status != TaskStatusCreated {
		return false
	}
	if t.ExpiresAt.IsZero() {
		return false
	}
	// now >= ExpiresAt (i.e., !now.Before(ExpiresAt))
	return !now.Before(t.ExpiresAt)
}

// CanStart validates whether this task can enter a focus session at the given time.
func (t *Task) CanStart(now time.Time) error {
	if t.Status == TaskStatusCompleted || t.Status == TaskStatusArchived {
		return ErrInvalidTaskState
	}
	if strings.TrimSpace(t.MicroStart) == "" {
		return ErrEmptyMicroStart
	}
	if t.IsExpired(now) {
		return ErrTaskExpired
	}
	return nil
}

// MarkStarted transitions a task from CREATED to STARTED upon its first focus session.
// If already STARTED, it updates UpdatedAt and remains STARTED.
func (t *Task) MarkStarted(now time.Time) error {
	if err := t.CanStart(now); err != nil {
		return err
	}
	nowUTC := now.UTC()
	if t.Status == TaskStatusCreated {
		t.Status = TaskStatusStarted
		if t.StartedAt == nil {
			t.StartedAt = &nowUTC
		}
	}
	t.UpdatedAt = nowUTC
	return nil
}

// MarkCompleted marks the task COMPLETED.
func (t *Task) MarkCompleted(now time.Time) error {
	if t.Status == TaskStatusCompleted || t.Status == TaskStatusArchived {
		return ErrInvalidTaskState
	}
	nowUTC := now.UTC()
	t.Status = TaskStatusCompleted
	t.CompletedAt = &nowUTC
	t.UpdatedAt = nowUTC
	return nil
}

// Archive transitions an unstarted expired task to ARCHIVED.
func (t *Task) Archive(now time.Time) error {
	if t.Status == TaskStatusCompleted {
		return ErrInvalidTaskState
	}
	nowUTC := now.UTC()
	t.Status = TaskStatusArchived
	t.ArchivedAt = &nowUTC
	t.UpdatedAt = nowUTC
	return nil
}
