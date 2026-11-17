package mongo

import (
	"context"
	"time"

	"ttaskam/internal/domain"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type FocusTxManager struct {
	client *mongo.Client
	db     *mongo.Database
}

func NewFocusTxManager(client *mongo.Client, dbName string) *FocusTxManager {
	return &FocusTxManager{
		client: client,
		db:     client.Database(dbName),
	}
}

func (m *FocusTxManager) StartFocusAtomic(
	ctx context.Context,
	userID int64,
	taskID string,
	now time.Time,
) (*domain.FocusSession, *domain.Task, error) {
	taskObjID, err := primitive.ObjectIDFromHex(taskID)
	if err != nil {
		return nil, nil, domain.ErrTaskNotFound
	}

	session, err := m.client.StartSession()
	if err != nil {
		return nil, nil, err
	}
	defer session.EndSession(ctx)

	var (
		resultSession *domain.FocusSession
		resultTask    *domain.Task
	)

	err = mongo.WithSession(ctx, session, func(sc mongo.SessionContext) error {
		if err := session.StartTransaction(); err != nil {
			return err
		}

		// 1. One-open-session check
		var openSess sessionDoc
		filterOpen := bson.M{
			"user_id": userID,
			"status": bson.M{
				"$in": []domain.SessionStatus{domain.SessionStatusActive, domain.SessionStatusPaused},
			},
		}
		err := m.db.Collection("focus_sessions").FindOne(sc, filterOpen).Decode(&openSess)
		if err == nil {
			_ = session.AbortTransaction(sc)
			return domain.ErrFocusTunnelActive
		} else if err != mongo.ErrNoDocuments {
			_ = session.AbortTransaction(sc)
			return err
		}

		// 2. Task validation
		var tDoc taskDoc
		err = m.db.Collection("tasks").FindOne(sc, bson.M{"_id": taskObjID, "user_id": userID}).Decode(&tDoc)
		if err != nil {
			_ = session.AbortTransaction(sc)
			if err == mongo.ErrNoDocuments {
				return domain.ErrTaskNotFound
			}
			return err
		}

		task := docToTask(&tDoc)
		if err := task.CanStart(now); err != nil {
			_ = session.AbortTransaction(sc)
			return err
		}

		// 3. Update task to STARTED atomically
		nowUTC := now.UTC()
		taskUpdate := bson.M{
			"$set": bson.M{
				"status":     domain.TaskStatusStarted,
				"updated_at": nowUTC,
			},
		}
		if tDoc.StartedAt == nil {
			taskUpdate["$set"].(bson.M)["started_at"] = nowUTC
		}

		resTask, err := m.db.Collection("tasks").UpdateOne(
			sc,
			bson.M{"_id": taskObjID, "user_id": userID, "status": tDoc.Status},
			taskUpdate,
		)
		if err != nil || resTask.MatchedCount == 0 {
			_ = session.AbortTransaction(sc)
			return domain.ErrTransactionAborted
		}

		// 4. Insert new focus session
		newSessDoc := sessionDoc{
			ID:             primitive.NewObjectID(),
			UserID:         userID,
			TaskID:         taskObjID,
			Status:         domain.SessionStatusActive,
			StartedAt:      nowUTC,
			TotalActiveSec: 0,
			Version:        1,
			CreatedAt:      nowUTC,
			UpdatedAt:      nowUTC,
		}

		if _, err := m.db.Collection("focus_sessions").InsertOne(sc, newSessDoc); err != nil {
			_ = session.AbortTransaction(sc)
			if mongo.IsDuplicateKeyError(err) {
				return domain.ErrFocusTunnelActive
			}
			return err
		}

		if err := session.CommitTransaction(sc); err != nil {
			return err
		}

		resultSession = docToSession(&newSessDoc)
		_ = task.MarkStarted(now)
		resultTask = task
		return nil
	})

	if err != nil {
		return nil, nil, err
	}
	return resultSession, resultTask, nil
}

func (m *FocusTxManager) CompleteFocusAtomic(
	ctx context.Context,
	userID int64,
	sessionID string,
	expectedVersion int,
	now time.Time,
) (*domain.FocusSession, *domain.Task, error) {
	sessObjID, err := primitive.ObjectIDFromHex(sessionID)
	if err != nil {
		return nil, nil, domain.ErrNoActiveSession
	}

	session, err := m.client.StartSession()
	if err != nil {
		return nil, nil, err
	}
	defer session.EndSession(ctx)

	var (
		resultSession *domain.FocusSession
		resultTask    *domain.Task
	)

	err = mongo.WithSession(ctx, session, func(sc mongo.SessionContext) error {
		if err := session.StartTransaction(); err != nil {
			return err
		}

		// 1. Find existing session to verify version and calculate elapsed
		var oldSess sessionDoc
		err := m.db.Collection("focus_sessions").FindOne(sc, bson.M{"_id": sessObjID, "user_id": userID}).Decode(&oldSess)
		if err != nil {
			_ = session.AbortTransaction(sc)
			if err == mongo.ErrNoDocuments {
				return domain.ErrNoActiveSession
			}
			return err
		}

		if oldSess.Status != domain.SessionStatusActive && oldSess.Status != domain.SessionStatusPaused {
			_ = session.AbortTransaction(sc)
			return domain.ErrNoActiveSession
		}

		if oldSess.Version != expectedVersion {
			_ = session.AbortTransaction(sc)
			return domain.ErrVersionMismatch
		}

		nowUTC := now.UTC()
		elapsed := oldSess.TotalActiveSec
		if oldSess.Status == domain.SessionStatusActive {
			diff := int64(nowUTC.Sub(oldSess.StartedAt).Seconds())
			if diff > 0 {
				elapsed += diff
			}
		}

		// 2. Update session to COMPLETED
		filterSess := bson.M{
			"_id":     sessObjID,
			"user_id": userID,
			"version": expectedVersion,
			"status": bson.M{
				"$in": []domain.SessionStatus{domain.SessionStatusActive, domain.SessionStatusPaused},
			},
		}
		updateSess := bson.M{
			"$set": bson.M{
				"status":           domain.SessionStatusCompleted,
				"ended_at":         nowUTC,
				"total_active_sec": elapsed,
				"updated_at":       nowUTC,
			},
			"$inc": bson.M{"version": 1},
		}

		resSess, err := m.db.Collection("focus_sessions").UpdateOne(sc, filterSess, updateSess)
		if err != nil || resSess.MatchedCount == 0 {
			_ = session.AbortTransaction(sc)
			return domain.ErrVersionMismatch
		}

		// 3. Update task to COMPLETED
		resTask, err := m.db.Collection("tasks").UpdateOne(
			sc,
			bson.M{"_id": oldSess.TaskID, "user_id": userID},
			bson.M{
				"$set": bson.M{
					"status":       domain.TaskStatusCompleted,
					"completed_at": nowUTC,
					"updated_at":   nowUTC,
				},
			},
		)
		if err != nil || resTask.MatchedCount == 0 {
			_ = session.AbortTransaction(sc)
			return domain.ErrTransactionAborted
		}

		// 4. Fetch updated task for return
		var updatedTaskDoc taskDoc
		if err := m.db.Collection("tasks").FindOne(sc, bson.M{"_id": oldSess.TaskID, "user_id": userID}).Decode(&updatedTaskDoc); err != nil {
			_ = session.AbortTransaction(sc)
			return err
		}

		if err := session.CommitTransaction(sc); err != nil {
			return err
		}

		oldSess.Status = domain.SessionStatusCompleted
		oldSess.EndedAt = &nowUTC
		oldSess.TotalActiveSec = elapsed
		oldSess.Version++
		resultSession = docToSession(&oldSess)
		resultTask = docToTask(&updatedTaskDoc)
		return nil
	})

	if err != nil {
		return nil, nil, err
	}
	return resultSession, resultTask, nil
}

func (m *FocusTxManager) PauseFocus(
	ctx context.Context,
	userID int64,
	sessionID string,
	expectedVersion int,
	now time.Time,
) (*domain.FocusSession, error) {
	sessObjID, err := primitive.ObjectIDFromHex(sessionID)
	if err != nil {
		return nil, domain.ErrNoActiveSession
	}

	var oldSess sessionDoc
	err = m.db.Collection("focus_sessions").FindOne(ctx, bson.M{"_id": sessObjID, "user_id": userID}).Decode(&oldSess)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, domain.ErrNoActiveSession
		}
		return nil, err
	}

	if oldSess.Status != domain.SessionStatusActive {
		return nil, domain.ErrInvalidSessionState
	}
	if oldSess.Version != expectedVersion {
		return nil, domain.ErrVersionMismatch
	}

	nowUTC := now.UTC()
	elapsed := oldSess.TotalActiveSec
	diff := int64(nowUTC.Sub(oldSess.StartedAt).Seconds())
	if diff > 0 {
		elapsed += diff
	}

	filter := bson.M{
		"_id":     sessObjID,
		"user_id": userID,
		"version": expectedVersion,
		"status":  domain.SessionStatusActive,
	}
	update := bson.M{
		"$set": bson.M{
			"status":           domain.SessionStatusPaused,
			"paused_at":        nowUTC,
			"total_active_sec": elapsed,
			"updated_at":       nowUTC,
		},
		"$inc": bson.M{"version": 1},
	}

	res, err := m.db.Collection("focus_sessions").UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, domain.ErrVersionMismatch
	}

	oldSess.Status = domain.SessionStatusPaused
	oldSess.PausedAt = &nowUTC
	oldSess.TotalActiveSec = elapsed
	oldSess.Version++
	return docToSession(&oldSess), nil
}

func (m *FocusTxManager) ResumeFocus(
	ctx context.Context,
	userID int64,
	sessionID string,
	expectedVersion int,
	now time.Time,
) (*domain.FocusSession, error) {
	sessObjID, err := primitive.ObjectIDFromHex(sessionID)
	if err != nil {
		return nil, domain.ErrNoActiveSession
	}

	var oldSess sessionDoc
	err = m.db.Collection("focus_sessions").FindOne(ctx, bson.M{"_id": sessObjID, "user_id": userID}).Decode(&oldSess)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, domain.ErrNoActiveSession
		}
		return nil, err
	}

	if oldSess.Status != domain.SessionStatusPaused {
		return nil, domain.ErrInvalidSessionState
	}
	if oldSess.Version != expectedVersion {
		return nil, domain.ErrVersionMismatch
	}

	nowUTC := now.UTC()
	filter := bson.M{
		"_id":     sessObjID,
		"user_id": userID,
		"version": expectedVersion,
		"status":  domain.SessionStatusPaused,
	}
	update := bson.M{
		"$set": bson.M{
			"status":     domain.SessionStatusActive,
			"started_at": nowUTC,
			"paused_at":  nil,
			"updated_at": nowUTC,
		},
		"$inc": bson.M{"version": 1},
	}

	res, err := m.db.Collection("focus_sessions").UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, domain.ErrVersionMismatch
	}

	oldSess.Status = domain.SessionStatusActive
	oldSess.StartedAt = nowUTC
	oldSess.PausedAt = nil
	oldSess.Version++
	return docToSession(&oldSess), nil
}

func (m *FocusTxManager) EndFocus(
	ctx context.Context,
	userID int64,
	sessionID string,
	expectedVersion int,
	now time.Time,
) (*domain.FocusSession, error) {
	sessObjID, err := primitive.ObjectIDFromHex(sessionID)
	if err != nil {
		return nil, domain.ErrNoActiveSession
	}

	var oldSess sessionDoc
	err = m.db.Collection("focus_sessions").FindOne(ctx, bson.M{"_id": sessObjID, "user_id": userID}).Decode(&oldSess)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, domain.ErrNoActiveSession
		}
		return nil, err
	}

	if oldSess.Status != domain.SessionStatusActive && oldSess.Status != domain.SessionStatusPaused {
		return nil, domain.ErrNoActiveSession
	}
	if oldSess.Version != expectedVersion {
		return nil, domain.ErrVersionMismatch
	}

	nowUTC := now.UTC()
	elapsed := oldSess.TotalActiveSec
	if oldSess.Status == domain.SessionStatusActive {
		diff := int64(nowUTC.Sub(oldSess.StartedAt).Seconds())
		if diff > 0 {
			elapsed += diff
		}
	}

	filter := bson.M{
		"_id":     sessObjID,
		"user_id": userID,
		"version": expectedVersion,
		"status": bson.M{
			"$in": []domain.SessionStatus{domain.SessionStatusActive, domain.SessionStatusPaused},
		},
	}
	update := bson.M{
		"$set": bson.M{
			"status":           domain.SessionStatusEnded,
			"ended_at":         nowUTC,
			"total_active_sec": elapsed,
			"updated_at":       nowUTC,
		},
		"$inc": bson.M{"version": 1},
	}

	res, err := m.db.Collection("focus_sessions").UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, domain.ErrVersionMismatch
	}

	// Session is now ENDED; slot is freed immediately!
	oldSess.Status = domain.SessionStatusEnded
	oldSess.EndedAt = &nowUTC
	oldSess.TotalActiveSec = elapsed
	oldSess.Version++
	return docToSession(&oldSess), nil
}
