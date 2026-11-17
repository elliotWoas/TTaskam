package mongo

import (
	"context"
	"time"

	"ttaskam/internal/domain"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type TaskRepo struct {
	client *mongo.Client
	db     *mongo.Database
}

func NewTaskRepo(client *mongo.Client, dbName string) *TaskRepo {
	return &TaskRepo{
		client: client,
		db:     client.Database(dbName),
	}
}

type idempotencyDoc struct {
	UserID         int64              `bson:"user_id"`
	IdempotencyKey string             `bson:"idempotency_key"`
	TaskID         primitive.ObjectID `bson:"task_id"`
	CreatedAt      time.Time          `bson:"created_at"`
}

type subtaskDoc struct {
	ID        string    `bson:"id"`
	Title     string    `bson:"title"`
	CreatedAt time.Time `bson:"created_at"`
}

type taskDoc struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"`
	UserID      int64              `bson:"user_id"`
	Title       string             `bson:"title"`
	MicroStart  string             `bson:"micro_start"`
	Subtasks    []subtaskDoc       `bson:"subtasks,omitempty"`
	Status      domain.TaskStatus  `bson:"status"`
	CreatedAt   time.Time          `bson:"created_at"`
	ExpiresAt   time.Time          `bson:"expires_at"`
	StartedAt   *time.Time         `bson:"started_at,omitempty"`
	CompletedAt *time.Time         `bson:"completed_at,omitempty"`
	ArchivedAt  *time.Time         `bson:"archived_at,omitempty"`
	UpdatedAt   time.Time          `bson:"updated_at"`
}

func (r *TaskRepo) Create(ctx context.Context, task *domain.Task, idempotencyKey string) (*domain.Task, error) {
	tasksColl := r.db.Collection("tasks")
	idempColl := r.db.Collection("task_idempotency")

	var subDocs []subtaskDoc
	for _, s := range task.Subtasks {
		subDocs = append(subDocs, subtaskDoc{
			ID:        s.ID,
			Title:     s.Title,
			CreatedAt: s.CreatedAt,
		})
	}

	doc := taskDoc{
		ID:          primitive.NewObjectID(),
		UserID:      task.UserID,
		Title:       task.Title,
		MicroStart:  task.MicroStart,
		Subtasks:    subDocs,
		Status:      task.Status,
		CreatedAt:   task.CreatedAt,
		ExpiresAt:   task.ExpiresAt,
		StartedAt:   task.StartedAt,
		CompletedAt: task.CompletedAt,
		ArchivedAt:  task.ArchivedAt,
		UpdatedAt:   task.UpdatedAt,
	}

	if idempotencyKey == "" {
		res, err := tasksColl.InsertOne(ctx, doc)
		if err != nil {
			return nil, err
		}
		task.ID = res.InsertedID.(primitive.ObjectID).Hex()
		return task, nil
	}

	// Transaction to guarantee deduplication record never exists without its task
	session, err := r.client.StartSession()
	if err != nil {
		return nil, err
	}
	defer session.EndSession(ctx)

	var resultTask *domain.Task
	err = mongo.WithSession(ctx, session, func(sc mongo.SessionContext) error {
		if err := session.StartTransaction(); err != nil {
			return err
		}

		// Check if idempotency record exists
		var existingIdemp idempotencyDoc
		filter := bson.M{"user_id": task.UserID, "idempotency_key": idempotencyKey}
		err := idempColl.FindOne(sc, filter).Decode(&existingIdemp)
		if err == nil {
			// Already exists! Return existing task
			var existingDoc taskDoc
			if err := tasksColl.FindOne(sc, bson.M{"_id": existingIdemp.TaskID, "user_id": task.UserID}).Decode(&existingDoc); err != nil {
				_ = session.AbortTransaction(sc)
				return err
			}
			resultTask = docToTask(&existingDoc)
			return session.CommitTransaction(sc)
		} else if err != mongo.ErrNoDocuments {
			_ = session.AbortTransaction(sc)
			return err
		}

		// Insert task
		if _, err := tasksColl.InsertOne(sc, doc); err != nil {
			_ = session.AbortTransaction(sc)
			return err
		}

		// Insert idempotency record
		idemp := idempotencyDoc{
			UserID:         task.UserID,
			IdempotencyKey: idempotencyKey,
			TaskID:         doc.ID,
			CreatedAt:      task.CreatedAt,
		}
		if _, err := idempColl.InsertOne(sc, idemp); err != nil {
			_ = session.AbortTransaction(sc)
			return err
		}

		if err := session.CommitTransaction(sc); err != nil {
			return err
		}

		resultTask = docToTask(&doc)
		return nil
	})

	if err != nil {
		return nil, err
	}
	return resultTask, nil
}

func (r *TaskRepo) GetByID(ctx context.Context, id string, userID int64) (*domain.Task, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, domain.ErrTaskNotFound
	}

	var doc taskDoc
	err = r.db.Collection("tasks").FindOne(ctx, bson.M{"_id": objID, "user_id": userID}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, domain.ErrTaskNotFound
		}
		return nil, err
	}

	return docToTask(&doc), nil
}

func (r *TaskRepo) Update(ctx context.Context, task *domain.Task) error {
	objID, err := primitive.ObjectIDFromHex(task.ID)
	if err != nil {
		return domain.ErrTaskNotFound
	}

	var subDocs []subtaskDoc
	for _, s := range task.Subtasks {
		subDocs = append(subDocs, subtaskDoc{
			ID:        s.ID,
			Title:     s.Title,
			CreatedAt: s.CreatedAt,
		})
	}

	update := bson.M{
		"$set": bson.M{
			"title":        task.Title,
			"micro_start":  task.MicroStart,
			"subtasks":     subDocs,
			"status":       task.Status,
			"started_at":   task.StartedAt,
			"completed_at": task.CompletedAt,
			"archived_at":  task.ArchivedAt,
			"updated_at":   task.UpdatedAt,
		},
	}

	res, err := r.db.Collection("tasks").UpdateOne(ctx, bson.M{"_id": objID, "user_id": task.UserID}, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return domain.ErrTaskNotFound
	}
	return nil
}

func (r *TaskRepo) ArchiveExpired(ctx context.Context, userID int64, now time.Time) (int64, error) {
	filter := bson.M{
		"user_id":    userID,
		"status":     domain.TaskStatusCreated,
		"expires_at": bson.M{"$lte": now.UTC()},
	}

	nowUTC := now.UTC()
	update := bson.M{
		"$set": bson.M{
			"status":      domain.TaskStatusArchived,
			"archived_at": nowUTC,
			"updated_at":  nowUTC,
		},
	}

	res, err := r.db.Collection("tasks").UpdateMany(ctx, filter, update)
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

func (r *TaskRepo) ListActive(ctx context.Context, userID int64) ([]*domain.Task, error) {
	filter := bson.M{
		"user_id": userID,
		"status": bson.M{
			"$in": []domain.TaskStatus{domain.TaskStatusCreated, domain.TaskStatusStarted},
		},
	}

	cursor, err := r.db.Collection("tasks").Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var tasks []*domain.Task
	for cursor.Next(ctx) {
		var doc taskDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		tasks = append(tasks, docToTask(&doc))
	}

	return tasks, cursor.Err()
}

func (r *TaskRepo) GetByTimeRange(ctx context.Context, userID int64, start, end time.Time) ([]*domain.Task, error) {
	filter := bson.M{
		"user_id": userID,
		"created_at": bson.M{
			"$gte": start.UTC(),
			"$lt":  end.UTC(),
		},
	}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}})
	cursor, err := r.db.Collection("tasks").Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var tasks []*domain.Task
	for cursor.Next(ctx) {
		var doc taskDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		tasks = append(tasks, docToTask(&doc))
	}

	return tasks, cursor.Err()
}

func docToTask(doc *taskDoc) *domain.Task {
	var subtasks []domain.Subtask
	for _, s := range doc.Subtasks {
		subtasks = append(subtasks, domain.Subtask{
			ID:        s.ID,
			Title:     s.Title,
			CreatedAt: s.CreatedAt,
		})
	}

	return &domain.Task{
		ID:          doc.ID.Hex(),
		UserID:      doc.UserID,
		Title:       doc.Title,
		MicroStart:  doc.MicroStart,
		Subtasks:    subtasks,
		Status:      doc.Status,
		CreatedAt:   doc.CreatedAt,
		ExpiresAt:   doc.ExpiresAt,
		StartedAt:   doc.StartedAt,
		CompletedAt: doc.CompletedAt,
		ArchivedAt:  doc.ArchivedAt,
		UpdatedAt:   doc.UpdatedAt,
	}
}
