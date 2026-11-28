package localfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

const workerLeasePath = ".olw/lwc-worker-lease.json"

var ErrWorkerLeaseHeld = errors.New("local worker lease is held")

type workerLeaseRecord struct {
	Owner       string `json:"owner"`
	ExecutionID string `json:"execution_id,omitempty"`
	StartedAt   string `json:"started_at"`
	ExpiresAt   string `json:"expires_at"`
}

// WithWorkerReadLease makes a LocalFS read participate in the same exclusive
// lease used by the pipeline worker, so a cooperating publication cannot swap
// the files and database while the callback reads one snapshot.
func (c *Client) WithWorkerReadLease(ctx context.Context, read func() error) (retErr error) {
	if read == nil {
		return errors.New("local worker read callback is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	projectRoot, err := c.projectRoot()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(".olw", 0o755); err != nil {
		return err
	}
	now := time.Now().UTC()
	owner := fmt.Sprintf("reader:%d:%d", os.Getpid(), now.UnixNano())
	record := workerLeaseRecord{
		Owner: owner, ExecutionID: "concept-conflict-read",
		StartedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(36 * time.Hour).Format(time.RFC3339),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(workerLeasePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create local worker read lease: %w", err)
		}
		return localWorkerLeaseHeld(root)
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = root.Remove(workerLeasePath)
		return fmt.Errorf("write local worker read lease: %w", err)
	}
	defer func() {
		leaseData, readErr := root.ReadFile(workerLeasePath)
		var current workerLeaseRecord
		if readErr != nil || json.Unmarshal(leaseData, &current) != nil || current.Owner != owner {
			if retErr == nil {
				retErr = errors.New("local worker read lease owner changed")
			}
			return
		}
		if removeErr := root.Remove(workerLeasePath); removeErr != nil && retErr == nil {
			retErr = fmt.Errorf("remove local worker read lease: %w", removeErr)
		}
	}()
	return read()
}

func localWorkerLeaseHeld(root *os.Root) error {
	data, err := root.ReadFile(workerLeasePath)
	if err != nil {
		return fmt.Errorf("%w: existing lease cannot be read", ErrWorkerLeaseHeld)
	}
	var current workerLeaseRecord
	if json.Unmarshal(data, &current) != nil || current.Owner == "" {
		return fmt.Errorf("%w: existing lease metadata is invalid", ErrWorkerLeaseHeld)
	}
	return fmt.Errorf("%w: %s", ErrWorkerLeaseHeld, current.Owner)
}
