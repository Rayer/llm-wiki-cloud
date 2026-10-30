// Package profileruntime contains durable Profile execution wire contracts.
package profileruntime

import (
	"crypto/sha256"
	"encoding/hex"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const WorkCollection = "profile_runtime_work"
const MaxAttempts = 3
const ExecutionTimeout = 4 * time.Minute
const LeaseDuration = 5 * time.Minute

type Work struct {
	UserID          string                       `firestore:"user_id"`
	ProjectID       string                       `firestore:"project_id"`
	Kind            string                       `firestore:"kind"`
	Revision        int64                        `firestore:"revision"`
	ID              string                       `firestore:"id"`
	RequestID       string                       `firestore:"request_id,omitempty" json:"-"`
	CandidateID     string                       `firestore:"candidate_id"`
	Due             time.Time                    `firestore:"due"`
	Pending         bool                         `firestore:"pending"`
	Attempts        int                          `firestore:"attempts"`
	Token           string                       `firestore:"token"`
	LeaseUntil      time.Time                    `firestore:"lease_until"`
	Status          string                       `firestore:"status"`
	DocumentID      string                       `firestore:"document_id,omitempty"`
	ProfileSnapshot llm.OperationProfileSnapshot `firestore:"llm_profile_snapshot,omitempty"`
}

func WorkID(w Work) string {
	sum := sha256.Sum256([]byte(w.UserID + "\x00" + w.ProjectID + "\x00" + w.Kind + "\x00" + w.ID))
	return hex.EncodeToString(sum[:])
}

// Enqueue is part of the same transaction that creates the intent/job/receipt.
func Enqueue(tx *firestore.Transaction, client *firestore.Client, w Work) error {
	w.Pending, w.Status = true, "pending"
	return tx.Set(scopedfirestore.Collection(client, WorkCollection).Doc(WorkID(w)), w)
}

// Requeue preserves the existing retry budget and does not steal an active
// lease when a document worker schedules source-save recovery.
func Requeue(tx *firestore.Transaction, client *firestore.Client, w Work, now time.Time) error {
	ref := scopedfirestore.Collection(client, WorkCollection).Doc(WorkID(w))
	snapshot, err := tx.Get(ref)
	if err != nil && status.Code(err) != codes.NotFound {
		return err
	}
	if err == nil {
		var prior Work
		if err := snapshot.DataTo(&prior); err != nil {
			return err
		}
		if prior.Pending && prior.LeaseUntil.After(now) {
			return nil
		}
		w.Attempts = prior.Attempts
	}
	w.Pending, w.Status, w.Token, w.LeaseUntil = true, "pending", "", time.Time{}
	return tx.Set(ref, w)
}
