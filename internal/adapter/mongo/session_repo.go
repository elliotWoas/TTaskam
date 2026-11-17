package mongo

import (
	"context"
	"time"

	"ttaskam/internal/domain"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type SessionRepo struct {
	db *mongo.Database
}

func NewSessionRepo(client *mongo.Client, dbName string) *SessionRepo {
	return &SessionRepo{
		db: client.Database(dbName),
	}
}

type sessionDoc struct {
	ID             primitive.ObjectID   `bson:"_id,omitempty"`
	UserID         int64                `bson:"user_id"`
	TaskID         primitive.ObjectID   `bson:"task_id"`
	Status         domain.SessionStatus `bson:"status"`
	StartedAt      time.Time            `bson:"started_at"`
	PausedAt       *time.Time           `bson:"paused_at,omitempty"`
	EndedAt        *time.Time           `bson:"ended_at,omitempty"`
	TotalActiveSec int64                `bson:"total_active_sec"`
	Version        int                  `bson:"version"`
	ExitReason     string               `bson:"exit_reason,omitempty"`
	CreatedAt      time.Time            `bson:"created_at"`
	UpdatedAt      time.Time            `bson:"updated_at"`
}

func (r *SessionRepo) GetOpenSession(ctx context.Context, userID int64) (*domain.FocusSession, error) {
	filter := bson.M{
		"user_id": userID,
		"status": bson.M{
			"$in": []domain.SessionStatus{domain.SessionStatusActive, domain.SessionStatusPaused},
		},
	}

	var doc sessionDoc
	err := r.db.Collection("focus_sessions").FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	return docToSession(&doc), nil
}

func (r *SessionRepo) GetSessionByID(ctx context.Context, id string, userID int64) (*domain.FocusSession, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, domain.ErrNoActiveSession
	}

	var doc sessionDoc
	err = r.db.Collection("focus_sessions").FindOne(ctx, bson.M{"_id": objID, "user_id": userID}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, domain.ErrNoActiveSession
		}
		return nil, err
	}

	return docToSession(&doc), nil
}

func (r *SessionRepo) SetExitReason(ctx context.Context, sessionID string, userID int64, reason string, now time.Time) error {
	objID, err := primitive.ObjectIDFromHex(sessionID)
	if err != nil {
		return nil // Safe fail: optional exit reason must never block
	}

	filter := bson.M{
		"_id":     objID,
		"user_id": userID,
		"status":  domain.SessionStatusEnded,
	}

	update := bson.M{
		"$set": bson.M{
			"exit_reason": reason,
			"updated_at":  now.UTC(),
		},
	}

	_, _ = r.db.Collection("focus_sessions").UpdateOne(ctx, filter, update)
	return nil
}

func docToSession(doc *sessionDoc) *domain.FocusSession {
	return &domain.FocusSession{
		ID:             doc.ID.Hex(),
		UserID:         doc.UserID,
		TaskID:         doc.TaskID.Hex(),
		Status:         doc.Status,
		StartedAt:      doc.StartedAt,
		PausedAt:       doc.PausedAt,
		EndedAt:        doc.EndedAt,
		TotalActiveSec: doc.TotalActiveSec,
		Version:        doc.Version,
		ExitReason:     doc.ExitReason,
		CreatedAt:      doc.CreatedAt,
		UpdatedAt:      doc.UpdatedAt,
	}
}
