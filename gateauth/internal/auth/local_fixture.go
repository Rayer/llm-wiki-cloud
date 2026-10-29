package auth

import (
	"context"
	"errors"
	"strings"

	"cloud.google.com/go/firestore"
	"golang.org/x/crypto/bcrypt"
)

// EnsureLocalPasswordFixture provisions the standard local test account only
// when no account owns its canonical email. Existing password, role, and
// project data are read-only here.
func EnsureLocalPasswordFixture(ctx context.Context, fs *firestore.Client, email, password string) (string, bool, error) {
	email = strings.TrimSpace(email)
	canonical := CanonicalizeEmail(email)
	if fs == nil || canonical == "" || len(password) < 8 {
		return "", false, ErrInvalidIdentityInput
	}
	repo := NewIdentityRepository(fs)
	if userID, user, found, err := repo.FindPasswordUserByEmail(ctx, canonical); err != nil {
		return "", false, err
	} else if found {
		if user.PasswordHash == "" {
			return "", false, errors.New("local fixture email belongs to an account without password login")
		}
		return userID, false, nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", false, err
	}
	userID := generateUserID()
	input := PasswordUserProvisioning{
		UserID: userID, DisplayEmail: email, CanonicalEmail: canonical,
		PasswordHash: string(hash), ProjectID: defaultProjectID, Role: "admin",
	}
	if err := repo.ProvisionPasswordUser(ctx, input); err != nil {
		if !errors.Is(err, ErrCanonicalEmailConflict) {
			return "", false, err
		}
		// A parallel Make start may win the email reservation after our lookup.
		// Read it back; never interpret a network or malformed-data failure as
		// permission to overwrite the winning account.
		if existingID, user, found, readErr := repo.FindPasswordUserByEmail(ctx, canonical); readErr == nil && found && user.PasswordHash != "" {
			return existingID, false, nil
		} else if readErr != nil {
			return "", false, readErr
		}
		return "", false, err
	}
	return userID, true, nil
}
