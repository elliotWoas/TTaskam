package mongo

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsureIndexes creates the necessary MongoDB indexes to enforce domain invariants.
func EnsureIndexes(ctx context.Context, db *mongo.Database) error {
	// 1. Focus Sessions: Unique partial index enforcing exactly one open session (ACTIVE or PAUSED) per user
	sessionIndex := mongo.IndexModel{
		Keys: bson.D{{Key: "user_id", Value: 1}},
		Options: options.Index().
			SetUnique(true).
			SetName("unique_open_session_per_user").
			SetPartialFilterExpression(bson.M{
				"status": bson.M{"$in": []string{"ACTIVE", "PAUSED"}},
			}),
	}
	if _, err := db.Collection("focus_sessions").Indexes().CreateOne(ctx, sessionIndex); err != nil {
		return err
	}

	// 2. Task Idempotency: Unique compound index on {user_id: 1, idempotency_key: 1}
	idempotencyIndex := mongo.IndexModel{
		Keys: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "idempotency_key", Value: 1},
		},
		Options: options.Index().
			SetUnique(true).
			SetName("unique_user_idempotency_key"),
	}
	if _, err := db.Collection("task_idempotency").Indexes().CreateOne(ctx, idempotencyIndex); err != nil {
		return err
	}

	// 3. Task Idempotency: TTL index on created_at (MongoDB requires TTL on a single field)
	idempotencyTTL := mongo.IndexModel{
		Keys: bson.D{
			{Key: "created_at", Value: 1},
		},
		Options: options.Index().
			SetName("idempotency_ttl_24h").
			SetExpireAfterSeconds(24 * 3600),
	}
	if _, err := db.Collection("task_idempotency").Indexes().CreateOne(ctx, idempotencyTTL); err != nil {
		return err
	}

	// 4. Tasks: Compound index on {user_id: 1, status: 1}
	taskIndex := mongo.IndexModel{
		Keys: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "status", Value: 1},
		},
		Options: options.Index().SetName("user_task_status"),
	}
	if _, err := db.Collection("tasks").Indexes().CreateOne(ctx, taskIndex); err != nil {
		return err
	}

	// 5. Tasks: Index on {user_id: 1, created_at: 1} for history lookups
	taskCreatedIndex := mongo.IndexModel{
		Keys: bson.D{
			{Key: "user_id", Value: 1},
			{Key: "created_at", Value: 1},
		},
		Options: options.Index().SetName("user_task_created_at"),
	}
	if _, err := db.Collection("tasks").Indexes().CreateOne(ctx, taskCreatedIndex); err != nil {
		return err
	}

	// 6. Tasks: TTL index for cleanup janitor on expired unstarted tasks
	taskExpiryIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "expires_at", Value: 1}},
		Options: options.Index().SetName("task_expires_at"),
	}
	if _, err := db.Collection("tasks").Indexes().CreateOne(ctx, taskExpiryIndex); err != nil {
		return err
	}

	// 7. Conversation Contexts: TTL index on updated_at
	convTTLIndex := mongo.IndexModel{
		Keys:    bson.D{{Key: "updated_at", Value: 1}},
		Options: options.Index().SetName("conv_ttl_1h").SetExpireAfterSeconds(3600),
	}
	_, err := db.Collection("conversation_contexts").Indexes().CreateOne(ctx, convTTLIndex)
	return err
}
