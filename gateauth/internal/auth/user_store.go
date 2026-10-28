package auth

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/gateauth/internal/firestore"
)

// UserRecord is a user document stored in Firestore.
type UserRecord struct {
	AuthInvalidBefore  time.Time `firestore:"auth_invalid_before,omitempty"`
	Status             string    `firestore:"status,omitempty"`
	AuthVersion        int64     `firestore:"auth_version,omitempty"`
	Email              string    `firestore:"email"`
	EmailCanonical     string    `firestore:"email_canonical,omitempty"`
	PasswordHash       string    `firestore:"password_hash,omitempty"`
	PasswordConfigured *bool     `firestore:"password_configured,omitempty"`
	Role               string    `firestore:"role,omitempty"`
	EmailVerified      bool      `firestore:"email_verified"`
	ProjectCount       int       `firestore:"project_count"`
	DefaultProject     string    `firestore:"default_project"`
}

// GetUser fetches a user record from Firestore by ID.
func GetUser(ctx context.Context, fs *firestore.Client, userID string) (*UserRecord, error) {
	doc, err := scopedfirestore.Collection(fs, "users").Doc(userID).Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("get user %s: %w", userID, err)
	}
	var u UserRecord
	if err := doc.DataTo(&u); err != nil {
		return nil, err
	}
	return &u, nil
}
