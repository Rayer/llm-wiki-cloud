package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"

	"cloud.google.com/go/firestore"
	"golang.org/x/crypto/bcrypt"
)

var ErrDemoIdentityConflict = errors.New("configured Demo identity conflicts with existing account data")

// EnsureDemoAccount creates the configured non-admin Demo identity only when
// its UID and canonical email are both unclaimed. Existing accounts are read
// only; the generated password is never returned or stored in plaintext.
func EnsureDemoAccount(ctx context.Context, fs *firestore.Client, userID, email, role string) (bool, error) {
	userID = strings.TrimSpace(userID)
	email = strings.TrimSpace(email)
	role = strings.TrimSpace(role)
	canonicalEmail := CanonicalizeEmail(email)
	if fs == nil || !ValidPathSegment(userID) || canonicalEmail == "" || role == "" || strings.EqualFold(role, "admin") {
		return false, ErrInvalidIdentityInput
	}

	var password [32]byte
	if _, err := rand.Read(password[:]); err != nil {
		return false, err
	}
	passwordHash, err := bcrypt.GenerateFromPassword(password[:], bcrypt.DefaultCost)
	clear(password[:])
	if err != nil {
		return false, err
	}

	input := demoAccountProvisioning{
		UserID: userID, DisplayEmail: email, CanonicalEmail: canonicalEmail,
		Role: role, PasswordHash: string(passwordHash),
	}
	created := false
	err = NewIdentityRepository(fs).RunTransaction(ctx, func(tx *IdentityTransaction) error {
		var err error
		created, err = tx.ensureDemoAccount(input)
		return err
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

type demoAccountProvisioning struct {
	UserID, DisplayEmail, CanonicalEmail, Role, PasswordHash string
}

func (tx *IdentityTransaction) ensureDemoAccount(input demoAccountProvisioning) (bool, error) {
	userRef := tx.txClientCollection("users").Doc(input.UserID)
	reservationRef := tx.txClientCollection(EmailReservationsCollection).Doc(emailReservationDocumentID(input.CanonicalEmail))
	projectRef := userRef.Collection("projects").Doc(defaultProjectID)
	snapshots, err := tx.tx.GetAll([]*firestore.DocumentRef{userRef, reservationRef, projectRef})
	if err != nil {
		return false, err
	}
	userSnapshot, reservationSnapshot, projectSnapshot := snapshots[0], snapshots[1], snapshots[2]

	if userSnapshot.Exists() {
		user, err := decodeUserRecord(userSnapshot)
		if err != nil {
			return false, err
		}
		if CanonicalizeEmail(user.Email) != input.CanonicalEmail ||
			(user.EmailCanonical != "" && user.EmailCanonical != input.CanonicalEmail) ||
			!user.Active() || user.Role == "" || strings.EqualFold(user.Role, "admin") {
			return false, ErrDemoIdentityConflict
		}
		if reservationSnapshot.Exists() {
			reservation, err := decodeEmailReservation(reservationSnapshot)
			if err != nil {
				return false, err
			}
			if reservation.UserID != input.UserID || reservation.CanonicalEmail != input.CanonicalEmail {
				return false, ErrDemoIdentityConflict
			}
		}
		return false, nil
	}

	if reservationSnapshot.Exists() {
		reservation, err := decodeEmailReservation(reservationSnapshot)
		if err != nil {
			return false, err
		}
		if reservation.UserID != input.UserID || reservation.CanonicalEmail != input.CanonicalEmail {
			return false, ErrDemoIdentityConflict
		}
		return false, ErrMalformedIdentityRecord
	}
	if projectSnapshot.Exists() {
		return false, ErrMalformedIdentityRecord
	}
	if err := tx.rejectLegacyCanonicalCollision(input.CanonicalEmail, input.UserID); err != nil {
		if errors.Is(err, ErrCanonicalEmailConflict) {
			return false, ErrDemoIdentityConflict
		}
		return false, err
	}

	user := UserRecord{
		Email: input.DisplayEmail, EmailCanonical: input.CanonicalEmail,
		PasswordHash: input.PasswordHash, Role: input.Role,
		ProjectCount: 0, DefaultProject: defaultProjectID,
	}
	reservation := EmailReservation{
		CanonicalEmail: input.CanonicalEmail, UserID: input.UserID,
		DisplayEmail: input.DisplayEmail,
	}
	project := map[string]interface{}{"name": "My First Wiki", "created_at": nil}
	tx.writes = append(tx.writes,
		func(transaction *firestore.Transaction) error { return transaction.Create(userRef, user) },
		func(transaction *firestore.Transaction) error { return transaction.Create(reservationRef, reservation) },
		func(transaction *firestore.Transaction) error { return transaction.Create(projectRef, project) },
	)
	return true, nil
}
