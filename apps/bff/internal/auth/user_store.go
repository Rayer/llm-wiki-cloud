package auth

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

// UserRecord is a user document stored in Firestore.
type UserRecord struct {
	Email          string `firestore:"email"`
	EmailCanonical string `firestore:"email_canonical,omitempty"`
	PasswordHash   string `firestore:"password_hash,omitempty"`
	Role           string `firestore:"role,omitempty"`
	EmailVerified  bool   `firestore:"email_verified"`
	ProjectCount   int    `firestore:"project_count"`
	DefaultProject string `firestore:"default_project"`
}

// CountProjects returns the number of projects a user has in Firestore.
func CountProjects(ctx context.Context, fs *firestore.Client, userID string) (int, error) {
	iter := fs.Collection("users").Doc(userID).Collection("projects").Documents(ctx)
	defer iter.Stop()
	count := 0
	for {
		_, err := iter.Next()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				break
			}
			return count, err
		}
		count++
	}
	return count, nil
}

// GetUser fetches a user record from Firestore by ID.
func GetUser(ctx context.Context, fs *firestore.Client, userID string) (*UserRecord, error) {
	doc, err := fs.Collection("users").Doc(userID).Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("get user %s: %w", userID, err)
	}
	var u UserRecord
	if err := doc.DataTo(&u); err != nil {
		return nil, err
	}
	return &u, nil
}
