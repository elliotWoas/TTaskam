package domain

import (
	"context"
	"time"
)

// ConversationContext holds the bounded, turn-by-turn state for an active multi-turn exchange.
// It stores only the pending title or task ID, the last relevant prompt, and the expected reply.
// It does NOT store arbitrary long-term chat history or all user tasks.
type ConversationContext struct {
	UserID        int64             `json:"user_id" bson:"_id"`
	State         ConversationState `json:"state" bson:"state"`
	PendingTitle  string            `json:"pending_title,omitempty" bson:"pending_title,omitempty"`
	PendingTaskID string            `json:"pending_task_id,omitempty" bson:"pending_task_id,omitempty"`
	LastPrompt    string            `json:"last_prompt,omitempty" bson:"last_prompt,omitempty"`
	ExpectedReply string            `json:"expected_reply,omitempty" bson:"expected_reply,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at" bson:"updated_at"`
}

// IsStale returns true if the context has exceeded the maximum inactivity TTL (e.g. 30 minutes).
func (c *ConversationContext) IsStale(now time.Time, ttl time.Duration) bool {
	if c == nil || c.UpdatedAt.IsZero() {
		return true
	}
	return now.Sub(c.UpdatedAt) > ttl
}

// ConversationStore provides persistence for per-user bounded conversation contexts.
type ConversationStore interface {
	Get(ctx context.Context, userID int64) (*ConversationContext, error)
	Set(ctx context.Context, cCtx *ConversationContext) error
	Clear(ctx context.Context, userID int64) error
}
