package mongo

import (
	"context"
	"time"

	"ttaskam/internal/domain"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type ConversationRepo struct {
	coll *mongo.Collection
}

func NewConversationRepo(client *mongo.Client, dbName string) *ConversationRepo {
	return &ConversationRepo{
		coll: client.Database(dbName).Collection("conversation_contexts"),
	}
}

type conversationDoc struct {
	UserID        int64                    `bson:"_id"`
	State         domain.ConversationState `bson:"state"`
	PendingTitle  string                   `bson:"pending_title,omitempty"`
	PendingTaskID string                   `bson:"pending_task_id,omitempty"`
	LastPrompt    string                   `bson:"last_prompt,omitempty"`
	ExpectedReply string                   `bson:"expected_reply,omitempty"`
	UpdatedAt     time.Time                `bson:"updated_at"`
}

func (r *ConversationRepo) Get(ctx context.Context, userID int64) (*domain.ConversationContext, error) {
	var doc conversationDoc
	err := r.coll.FindOne(ctx, bson.M{"_id": userID}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}
	return &domain.ConversationContext{
		UserID:        doc.UserID,
		State:         doc.State,
		PendingTitle:  doc.PendingTitle,
		PendingTaskID: doc.PendingTaskID,
		LastPrompt:    doc.LastPrompt,
		ExpectedReply: doc.ExpectedReply,
		UpdatedAt:     doc.UpdatedAt,
	}, nil
}

func (r *ConversationRepo) Set(ctx context.Context, cCtx *domain.ConversationContext) error {
	doc := conversationDoc{
		UserID:        cCtx.UserID,
		State:         cCtx.State,
		PendingTitle:  cCtx.PendingTitle,
		PendingTaskID: cCtx.PendingTaskID,
		LastPrompt:    cCtx.LastPrompt,
		ExpectedReply: cCtx.ExpectedReply,
		UpdatedAt:     cCtx.UpdatedAt,
	}
	opts := options.Replace().SetUpsert(true)
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": cCtx.UserID}, doc, opts)
	return err
}

func (r *ConversationRepo) Clear(ctx context.Context, userID int64) error {
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": userID})
	return err
}
