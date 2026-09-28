// Package profileruntime contains durable Profile execution wire contracts.
package profileruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"cloud.google.com/go/firestore"
)

const WorkCollection = "profile_runtime_work"
const MaxAttempts = 3
const ExecutionTimeout = 4 * time.Minute
const LeaseDuration = 5 * time.Minute

type Work struct {
	UserID      string    `firestore:"user_id"`
	ProjectID   string    `firestore:"project_id"`
	Kind        string    `firestore:"kind"`
	Revision    int64     `firestore:"revision"`
	ID          string    `firestore:"id"`
	CandidateID string    `firestore:"candidate_id"`
	Due         time.Time `firestore:"due"`
	Pending     bool      `firestore:"pending"`
	Attempts    int       `firestore:"attempts"`
	Token       string    `firestore:"token"`
	LeaseUntil  time.Time `firestore:"lease_until"`
	Status      string    `firestore:"status"`
}

func WorkID(w Work) string {
	sum := sha256.Sum256([]byte(w.UserID + "\x00" + w.ProjectID + "\x00" + w.Kind + "\x00" + w.ID))
	return hex.EncodeToString(sum[:])
}

// Enqueue is part of the same transaction that creates the intent/job/receipt.
func Enqueue(tx *firestore.Transaction, client *firestore.Client, w Work) error {
	w.Pending, w.Status = true, "pending"
	return tx.Set(client.Collection(WorkCollection).Doc(WorkID(w)), w)
}
