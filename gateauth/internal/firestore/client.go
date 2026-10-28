package firestore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Client wraps Firestore operations for pipeline status.
type Client struct {
	fs         *firestore.Client
	databaseID string
	lockID     string
	locks      *firestore.CollectionRef
}

// Status represents the current pipeline state.
type Status struct {
	Locked     bool      `json:"locked"`
	LockExpiry time.Time `json:"lock_expiry,omitempty"`
	Worker     string    `json:"worker,omitempty"`
}

// NewClient creates a Firestore client.
func NewClient(project, userID, projectID string) (*Client, error) {
	return NewClientWithDatabase(project, "", userID, projectID)
}

// NewClientWithDatabase creates a Firestore client for databaseID. An empty
// databaseID preserves the default database behavior of NewClient.
func NewClientWithDatabase(project, databaseID, userID, projectID string) (*Client, error) {
	return NewClientWithDatabaseAndScope(project, databaseID, userID, projectID, configuredScope())
}

// NewClientWithDatabaseAndScope selects the local worktree namespace explicitly.
func NewClientWithDatabaseAndScope(project, databaseID, userID, projectID, rawScope string) (*Client, error) {
	ctx := context.Background()
	databaseID = strings.TrimSpace(databaseID)
	fs, err := newFirestoreClient(ctx, project, databaseID)
	if err != nil {
		return nil, fmt.Errorf("firestore client: %w", err)
	}
	if err := RegisterLocalScope(fs, rawScope); err != nil {
		_ = fs.Close()
		return nil, fmt.Errorf("local cloud scope: %w", err)
	}
	if databaseID == "" {
		databaseID = firestore.DefaultDatabaseID
	}

	lockID := fmt.Sprintf("%s__%s", userID, projectID)
	return &Client{
		fs:         fs,
		databaseID: databaseID,
		lockID:     lockID,
		locks:      Collection(fs, "locks"),
	}, nil
}

var newFirestoreClient = func(ctx context.Context, project, databaseID string) (*firestore.Client, error) {
	if databaseID == "" {
		return firestore.NewClient(ctx, project)
	}
	return firestore.NewClientWithDatabase(ctx, project, databaseID)
}

// GetStatus returns the current pipeline lock status.
func (c *Client) GetStatus(ctx context.Context) (*Status, error) {
	doc, err := c.locks.Doc(c.lockID).Get(ctx)
	if err != nil {
		return &Status{Locked: false}, nil
	}

	data := doc.Data()
	expiresAt, ok := activeLockUntil(data, time.Now())
	if !ok {
		return &Status{Locked: false}, nil
	}

	s := &Status{Locked: true, LockExpiry: expiresAt}
	if w, ok := data["worker"].(string); ok {
		s.Worker = w
	}
	return s, nil
}

// LockDataActive reports whether Firestore lock document fields represent a held lock at now.
func LockDataActive(data map[string]interface{}, now time.Time) bool {
	_, ok := activeLockUntil(data, now)
	return ok
}

// CountActiveLocks returns the number of active pipeline locks.
func (c *Client) CountActiveLocks(ctx context.Context) (int, error) {
	iter := c.locks.Where("status", "==", "active").Documents(ctx)
	defer iter.Stop()
	count := 0
	now := time.Now()
	for {
		doc, err := iter.Next()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				break
			}
			return count, err
		}
		if _, ok := activeLockUntil(doc.Data(), now); ok {
			count++
		}
	}
	return count, nil
}

// ExecutionRecord represents one pipeline execution for metrics.
type ExecutionRecord struct {
	UserID      string    `json:"user_id"`
	ProjectID   string    `json:"project_id"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	DurationSec float64   `json:"duration_sec,omitempty"`
	Status      string    `json:"status"`
}

// WriteExecutionStart records a pipeline execution start.
func (c *Client) WriteExecutionStart(ctx context.Context, userID, projectID string, startedAt time.Time) (string, error) {
	doc := Collection(c.fs, "executions").NewDoc()
	_, err := doc.Set(ctx, map[string]interface{}{
		"user_id":    userID,
		"project_id": projectID,
		"started_at": startedAt,
		"status":     "running",
	})
	if err != nil {
		return "", err
	}
	return doc.ID, nil
}

// WriteExecutionEnd updates a pipeline execution with completion data.
func (c *Client) WriteExecutionEnd(ctx context.Context, docID string, finishedAt time.Time, status string) error {
	doc := Collection(c.fs, "executions").Doc(docID)
	dsnap, err := doc.Get(ctx)
	if err != nil {
		return err
	}
	startedAt, _ := firestoreTimestamp(dsnap.Data()["started_at"])
	durationSec := finishedAt.Sub(startedAt).Seconds()

	_, err = doc.Update(ctx, []firestore.Update{
		{Path: "finished_at", Value: finishedAt},
		{Path: "duration_sec", Value: durationSec},
		{Path: "status", Value: status},
	})
	return err
}

// ListRecentExecutions returns recent pipeline execution records for metrics.
func (c *Client) ListRecentExecutions(ctx context.Context, limit int) ([]ExecutionRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	iter := Collection(c.fs, "executions").OrderBy("started_at", firestore.Desc).Limit(limit).Documents(ctx)
	defer iter.Stop()
	var records []ExecutionRecord
	for {
		doc, err := iter.Next()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				break
			}
			return records, err
		}
		data := doc.Data()
		r := ExecutionRecord{}
		if v, ok := data["user_id"].(string); ok {
			r.UserID = v
		}
		if v, ok := data["project_id"].(string); ok {
			r.ProjectID = v
		}
		if v, ok := firestoreTimestamp(data["started_at"]); ok {
			r.StartedAt = v
		}
		if v, ok := firestoreTimestamp(data["finished_at"]); ok {
			r.FinishedAt = v
		}
		if v, ok := data["duration_sec"].(float64); ok {
			r.DurationSec = v
		}
		if v, ok := data["status"].(string); ok {
			r.Status = v
		}
		records = append(records, r)
	}
	return records, nil
}

func activeLockUntil(data map[string]interface{}, now time.Time) (time.Time, bool) {
	status, _ := data["status"].(string)
	if status != "active" {
		return time.Time{}, false
	}
	expiresAt, ok := firestoreTimestamp(data["expires_at"])
	if !ok || !expiresAt.After(now) {
		return time.Time{}, false
	}
	return expiresAt, true
}

func firestoreTimestamp(value interface{}) (time.Time, bool) {
	switch t := value.(type) {
	case time.Time:
		return t, true
	case *timestamppb.Timestamp:
		if t == nil {
			return time.Time{}, false
		}
		return t.AsTime(), true
	case timestamppb.Timestamp:
		return t.AsTime(), true
	default:
		return time.Time{}, false
	}
}

// Close closes the Firestore client.
func (c *Client) Close() error {
	forgetLocalScope(c.fs)
	return c.fs.Close()
}

// Raw exposes the underlying firestore.Client for direct operations.
func (c *Client) Raw() *firestore.Client {
	return c.fs
}

// CheckAvailable performs a scoped read so a client constructor cannot be
// mistaken for proof that the configured project and database are reachable.
func (c *Client) CheckAvailable(ctx context.Context) error {
	_, err := Collection(c.fs, "users").Limit(1).Documents(ctx).GetAll()
	return err
}
