package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// These collections deliberately use opaque digest document IDs. The values
	// remain fields for repository reads, but neither PII nor provider subjects
	// are placed in Firestore document paths.
	EmailReservationsCollection  = "email_reservations"
	ExternalIdentitiesCollection = "external_identities"
	defaultProjectID             = "default"
)

var (
	ErrCanonicalEmailConflict        = errors.New("canonical email is already reserved")
	ErrExternalIdentityConflict      = errors.New("external identity is already linked")
	ErrMalformedIdentityRecord       = errors.New("malformed identity record")
	ErrIdentityRepositoryUnavailable = errors.New("identity repository unavailable")
	ErrInvalidIdentityInput          = errors.New("invalid identity input")
	errPasswordLoginUnavailable      = errors.New("password login unavailable")
)

// CanonicalizeEmail is intentionally limited to the accepted contract:
// surrounding whitespace is removed and Unicode case is lowercased. It does
// not apply Gmail dot, plus-tag, or provider-specific rewriting.
func CanonicalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// EmailReservation is the uniqueness authority for one canonical email.
type EmailReservation struct {
	CanonicalEmail string    `firestore:"canonical_email"`
	UserID         string    `firestore:"user_id"`
	DisplayEmail   string    `firestore:"display_email,omitempty"`
	CreatedAt      time.Time `firestore:"created_at,omitempty"`
}

// ExternalIdentity is the provider-neutral ownership mapping used by future
// OIDC providers. Provider subject values are never used as document IDs.
type ExternalIdentity struct {
	Provider              string    `firestore:"provider"`
	Issuer                string    `firestore:"issuer"`
	Subject               string    `firestore:"subject"`
	UserID                string    `firestore:"user_id"`
	ProviderEmail         string    `firestore:"provider_email,omitempty"`
	ProviderEmailVerified bool      `firestore:"provider_email_verified"`
	CreatedAt             time.Time `firestore:"created_at,omitempty"`
}

// PasswordUserProvisioning contains all records that must commit together for
// a new password registration.
type PasswordUserProvisioning struct {
	UserID         string
	DisplayEmail   string
	CanonicalEmail string
	PasswordHash   string
	ProjectID      string
}

// ExternalUserProvisioning contains the records required for a new
// passwordless external user. It deliberately contains no provider HTTP or
// token concerns; callers pass an already accepted identity tuple.
type ExternalUserProvisioning struct {
	UserID                string
	DisplayEmail          string
	CanonicalEmail        string
	EmailVerified         bool
	Provider              string
	Issuer                string
	Subject               string
	ProviderEmail         string
	ProviderEmailVerified bool
	ProjectID             string
}

// IdentityRepository is the Firestore identity boundary shared by password
// registration and future provider login/linking.
type IdentityRepository struct {
	fs *firestore.Client
}

// NewIdentityRepository constructs the identity repository over Firestore.
func NewIdentityRepository(fs *firestore.Client) *IdentityRepository {
	return &IdentityRepository{fs: fs}
}

// IdentityTransaction exposes transaction-scoped identity operations. Reads
// happen before queued writes are flushed so callers can safely reserve and
// link more than one identity in a single Firestore transaction.
type IdentityTransaction struct {
	tx     *firestore.Transaction
	client *firestore.Client

	writes             []func(*firestore.Transaction) error
	emailReservations  map[string]*EmailReservation
	externalIdentities map[string]*ExternalIdentity
}

// RunTransaction executes identity operations atomically.
func (r *IdentityRepository) RunTransaction(ctx context.Context, fn func(*IdentityTransaction) error) error {
	if r == nil || r.fs == nil || fn == nil {
		return ErrIdentityRepositoryUnavailable
	}
	return r.fs.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		op := &IdentityTransaction{
			tx:                 tx,
			client:             r.fs,
			emailReservations:  make(map[string]*EmailReservation),
			externalIdentities: make(map[string]*ExternalIdentity),
		}
		if err := fn(op); err != nil {
			return err
		}
		for _, write := range op.writes {
			if err := write(tx); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetCanonicalEmailReservation reads a reservation by its canonical key.
func (r *IdentityRepository) GetCanonicalEmailReservation(ctx context.Context, email string) (*EmailReservation, error) {
	if r == nil || r.fs == nil {
		return nil, ErrIdentityRepositoryUnavailable
	}
	canonical, err := normalizedCanonicalEmail(email)
	if err != nil {
		return nil, err
	}
	snapshot, err := r.fs.Collection(EmailReservationsCollection).Doc(emailReservationDocumentID(canonical)).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, err
	}
	reservation, err := decodeEmailReservation(snapshot)
	if err != nil || reservation == nil || reservation.CanonicalEmail != canonical {
		return nil, ErrMalformedIdentityRecord
	}
	return reservation, nil
}

// GetPasswordUserByEmail resolves the current primary password identity. New
// users use the reservation directly; the bounded legacy scan keeps existing
// users login-compatible until the explicit audit/backfill is applied.
func (r *IdentityRepository) GetPasswordUserByEmail(ctx context.Context, email string) (string, *UserRecord, error) {
	if r == nil || r.fs == nil {
		return "", nil, ErrIdentityRepositoryUnavailable
	}
	canonical, err := normalizedCanonicalEmail(email)
	if err != nil {
		return "", nil, err
	}
	reservation, err := r.GetCanonicalEmailReservation(ctx, canonical)
	if err != nil {
		return "", nil, err
	}
	if reservation != nil {
		snapshot, getErr := r.fs.Collection("users").Doc(reservation.UserID).Get(ctx)
		if getErr != nil {
			return "", nil, ErrMalformedIdentityRecord
		}
		user, decodeErr := decodeUserRecord(snapshot)
		if decodeErr != nil || CanonicalizeEmail(user.Email) != canonical || (user.EmailCanonical != "" && user.EmailCanonical != canonical) {
			return "", nil, ErrMalformedIdentityRecord
		}
		if user.PasswordHash == "" {
			return "", nil, errPasswordLoginUnavailable
		}
		return reservation.UserID, user, nil
	}

	iter := r.fs.Collection("users").Documents(ctx)
	defer iter.Stop()
	var matchID string
	var match *UserRecord
	for {
		snapshot, iterErr := iter.Next()
		if iterErr != nil {
			if errors.Is(iterErr, iterator.Done) {
				break
			}
			return "", nil, iterErr
		}
		user, decodeErr := decodeUserRecord(snapshot)
		if decodeErr != nil || CanonicalizeEmail(user.Email) != canonical {
			continue
		}
		if match != nil {
			return "", nil, ErrCanonicalEmailConflict
		}
		matchID, match = snapshot.Ref.ID, user
	}
	if match == nil {
		return "", nil, ErrMalformedIdentityRecord
	}
	if match.PasswordHash == "" {
		return "", nil, errPasswordLoginUnavailable
	}
	return matchID, match, nil
}

// ReserveCanonicalEmail atomically claims a canonical email for userID. A
// repeated claim by the same user is idempotent; another owner is a conflict.
func (r *IdentityRepository) ReserveCanonicalEmail(ctx context.Context, userID, email, displayEmail string) error {
	return r.RunTransaction(ctx, func(tx *IdentityTransaction) error {
		return tx.ReserveCanonicalEmail(userID, email, displayEmail)
	})
}

// GetExternalIdentity reads a provider/issuer/subject ownership mapping.
func (r *IdentityRepository) GetExternalIdentity(ctx context.Context, provider, issuer, subject string) (*ExternalIdentity, error) {
	if r == nil || r.fs == nil {
		return nil, ErrIdentityRepositoryUnavailable
	}
	provider, issuer, subject, err := normalizedExternalIdentity(provider, issuer, subject)
	if err != nil {
		return nil, err
	}
	snapshot, err := r.fs.Collection(ExternalIdentitiesCollection).Doc(externalIdentityDocumentID(provider, issuer, subject)).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, err
	}
	identity, err := decodeExternalIdentity(snapshot)
	if err != nil || identity == nil || identity.Provider != provider || identity.Issuer != issuer || identity.Subject != subject {
		return nil, ErrMalformedIdentityRecord
	}
	return identity, nil
}

// LinkExternalIdentity atomically claims one provider identity. A repeated
// link by the same user is idempotent; another owner is a conflict.
func (r *IdentityRepository) LinkExternalIdentity(ctx context.Context, provider, issuer, subject, userID string) error {
	return r.RunTransaction(ctx, func(tx *IdentityTransaction) error {
		return tx.LinkExternalIdentity(provider, issuer, subject, userID)
	})
}

// GetCanonicalEmailReservation reads a reservation inside an existing
// transaction without exposing the underlying Firestore client.
func (tx *IdentityTransaction) GetCanonicalEmailReservation(email string) (*EmailReservation, error) {
	if tx == nil || tx.tx == nil {
		return nil, ErrIdentityRepositoryUnavailable
	}
	canonical, err := normalizedCanonicalEmail(email)
	if err != nil {
		return nil, err
	}
	key := emailReservationDocumentID(canonical)
	if reservation, ok := tx.emailReservations[key]; ok {
		copy := *reservation
		return &copy, nil
	}
	snapshot, err := optionalTransactionGet(tx.tx, tx.txClientCollection(EmailReservationsCollection).Doc(key))
	if err != nil || snapshot == nil {
		return nil, err
	}
	reservation, err := decodeEmailReservation(snapshot)
	if err != nil || reservation == nil || reservation.CanonicalEmail != canonical {
		return nil, ErrMalformedIdentityRecord
	}
	return reservation, nil
}

// ReserveCanonicalEmail queues a canonical email reservation in the current
// transaction.
func (tx *IdentityTransaction) ReserveCanonicalEmail(userID, email, displayEmail string) error {
	if tx == nil || tx.tx == nil {
		return ErrIdentityRepositoryUnavailable
	}
	canonical, err := normalizedCanonicalEmail(email)
	if err != nil || strings.TrimSpace(userID) == "" {
		return ErrInvalidIdentityInput
	}
	if err := validateReservationDisplay(canonical, displayEmail); err != nil {
		return err
	}
	key := emailReservationDocumentID(canonical)
	if existing, ok := tx.emailReservations[key]; ok {
		if existing.UserID != userID {
			return ErrCanonicalEmailConflict
		}
		return nil
	}
	ref := tx.txClientCollection(EmailReservationsCollection).Doc(key)
	snapshot, err := optionalTransactionGet(tx.tx, ref)
	if err != nil {
		return err
	}
	if snapshot != nil {
		reservation, decodeErr := decodeEmailReservation(snapshot)
		if decodeErr != nil {
			return decodeErr
		}
		if reservation.CanonicalEmail != canonical {
			return ErrMalformedIdentityRecord
		}
		if reservation.UserID != userID {
			return ErrCanonicalEmailConflict
		}
		tx.emailReservations[key] = reservation
		return nil
	}
	reservation := &EmailReservation{
		CanonicalEmail: canonical,
		UserID:         userID,
		DisplayEmail:   strings.TrimSpace(displayEmail),
		CreatedAt:      time.Now().UTC(),
	}
	if reservation.DisplayEmail == "" {
		reservation.DisplayEmail = canonical
	}
	tx.emailReservations[key] = reservation
	tx.writes = append(tx.writes, func(transaction *firestore.Transaction) error {
		return transaction.Create(ref, reservation)
	})
	return nil
}

// GetExternalIdentity reads an external identity inside an existing
// transaction.
func (tx *IdentityTransaction) GetExternalIdentity(provider, issuer, subject string) (*ExternalIdentity, error) {
	if tx == nil || tx.tx == nil {
		return nil, ErrIdentityRepositoryUnavailable
	}
	provider, issuer, subject, err := normalizedExternalIdentity(provider, issuer, subject)
	if err != nil {
		return nil, err
	}
	key := externalIdentityDocumentID(provider, issuer, subject)
	if identity, ok := tx.externalIdentities[key]; ok {
		copy := *identity
		return &copy, nil
	}
	snapshot, err := optionalTransactionGet(tx.tx, tx.txClientCollection(ExternalIdentitiesCollection).Doc(key))
	if err != nil || snapshot == nil {
		return nil, err
	}
	identity, err := decodeExternalIdentity(snapshot)
	if err != nil || identity == nil || identity.Provider != provider || identity.Issuer != issuer || identity.Subject != subject {
		return nil, ErrMalformedIdentityRecord
	}
	return identity, nil
}

// LinkExternalIdentity queues an external identity mapping in the current
// transaction.
func (tx *IdentityTransaction) LinkExternalIdentity(provider, issuer, subject, userID string) error {
	if tx == nil || tx.tx == nil {
		return ErrIdentityRepositoryUnavailable
	}
	provider, issuer, subject, err := normalizedExternalIdentity(provider, issuer, subject)
	if err != nil || strings.TrimSpace(userID) == "" {
		return ErrInvalidIdentityInput
	}
	key := externalIdentityDocumentID(provider, issuer, subject)
	if existing, ok := tx.externalIdentities[key]; ok {
		if existing.UserID != userID {
			return ErrExternalIdentityConflict
		}
		return nil
	}
	ref := tx.txClientCollection(ExternalIdentitiesCollection).Doc(key)
	snapshot, err := optionalTransactionGet(tx.tx, ref)
	if err != nil {
		return err
	}
	if snapshot != nil {
		identity, decodeErr := decodeExternalIdentity(snapshot)
		if decodeErr != nil {
			return decodeErr
		}
		if identity.Provider != provider || identity.Issuer != issuer || identity.Subject != subject {
			return ErrMalformedIdentityRecord
		}
		if identity.UserID != userID {
			return ErrExternalIdentityConflict
		}
		tx.externalIdentities[key] = identity
		return nil
	}
	identity := &ExternalIdentity{
		Provider:  provider,
		Issuer:    issuer,
		Subject:   subject,
		UserID:    userID,
		CreatedAt: time.Now().UTC(),
	}
	tx.externalIdentities[key] = identity
	tx.writes = append(tx.writes, func(transaction *firestore.Transaction) error {
		return transaction.Create(ref, identity)
	})
	return nil
}

// ProvisionPasswordUser atomically creates a user, canonical reservation, and
// Default Project metadata. Repeating the exact operation is idempotent.
func (r *IdentityRepository) ProvisionPasswordUser(ctx context.Context, input PasswordUserProvisioning) error {
	return r.RunTransaction(ctx, func(tx *IdentityTransaction) error {
		return tx.ProvisionPasswordUser(input)
	})
}

// ProvisionExternalUser atomically creates a passwordless user, canonical
// reservation, external identity mapping, and Default Project metadata.
// Repeating the complete operation for the same owner and identity is safe;
// partial or cross-owner state is rejected without repair or transfer.
func (r *IdentityRepository) ProvisionExternalUser(ctx context.Context, input ExternalUserProvisioning) error {
	return r.RunTransaction(ctx, func(tx *IdentityTransaction) error {
		return tx.ProvisionExternalUser(input)
	})
}

// ProvisionExternalUser queues a complete passwordless external-user
// provisioning operation in an existing transaction.
func (tx *IdentityTransaction) ProvisionExternalUser(input ExternalUserProvisioning) error {
	if tx == nil || tx.tx == nil {
		return ErrIdentityRepositoryUnavailable
	}
	input.DisplayEmail = strings.TrimSpace(input.DisplayEmail)
	input.CanonicalEmail = CanonicalizeEmail(input.CanonicalEmail)
	provider, issuer, subject, err := normalizedExternalIdentity(input.Provider, input.Issuer, input.Subject)
	if err != nil {
		return err
	}
	input.Provider, input.Issuer, input.Subject = provider, issuer, subject
	if input.ProjectID == "" {
		input.ProjectID = defaultProjectID
	}
	if strings.TrimSpace(input.UserID) == "" || input.DisplayEmail == "" || input.CanonicalEmail == "" || CanonicalizeEmail(input.DisplayEmail) != input.CanonicalEmail || !input.EmailVerified {
		return ErrInvalidIdentityInput
	}

	userRef := tx.txClientCollection("users").Doc(input.UserID)
	reservationRef := tx.txClientCollection(EmailReservationsCollection).Doc(emailReservationDocumentID(input.CanonicalEmail))
	identityRef := tx.txClientCollection(ExternalIdentitiesCollection).Doc(externalIdentityDocumentID(input.Provider, input.Issuer, input.Subject))
	projectRef := userRef.Collection("projects").Doc(input.ProjectID)
	snapshots, err := tx.tx.GetAll([]*firestore.DocumentRef{userRef, reservationRef, identityRef, projectRef})
	if err != nil {
		return err
	}
	userSnapshot, reservationSnapshot, identitySnapshot, projectSnapshot := snapshots[0], snapshots[1], snapshots[2], snapshots[3]
	allExist := userSnapshot.Exists() && reservationSnapshot.Exists() && identitySnapshot.Exists() && projectSnapshot.Exists()
	anyExist := userSnapshot.Exists() || reservationSnapshot.Exists() || identitySnapshot.Exists() || projectSnapshot.Exists()

	if reservationSnapshot.Exists() {
		reservation, decodeErr := decodeEmailReservation(reservationSnapshot)
		if decodeErr != nil {
			return decodeErr
		}
		if reservation.CanonicalEmail != input.CanonicalEmail {
			return ErrMalformedIdentityRecord
		}
		if reservation.UserID != input.UserID {
			return ErrCanonicalEmailConflict
		}
	}
	if identitySnapshot.Exists() {
		identity, decodeErr := decodeExternalIdentity(identitySnapshot)
		if decodeErr != nil {
			return decodeErr
		}
		if identity.Provider != input.Provider || identity.Issuer != input.Issuer || identity.Subject != input.Subject {
			return ErrMalformedIdentityRecord
		}
		if identity.UserID != input.UserID {
			return ErrExternalIdentityConflict
		}
	}
	if userSnapshot.Exists() {
		user, decodeErr := decodeUserRecord(userSnapshot)
		if decodeErr != nil {
			return decodeErr
		}
		if CanonicalizeEmail(user.Email) != input.CanonicalEmail || (user.EmailCanonical != "" && user.EmailCanonical != input.CanonicalEmail) || user.EmailVerified != input.EmailVerified {
			return ErrCanonicalEmailConflict
		}
		if user.PasswordHash != "" {
			return ErrCanonicalEmailConflict
		}
	}

	if allExist {
		return nil
	}
	if anyExist {
		return ErrMalformedIdentityRecord
	}
	if err := tx.rejectLegacyCanonicalCollision(input.CanonicalEmail, input.UserID); err != nil {
		return err
	}

	user := UserRecord{
		Email:          input.DisplayEmail,
		EmailCanonical: input.CanonicalEmail,
		EmailVerified:  input.EmailVerified,
		ProjectCount:   0,
		DefaultProject: input.ProjectID,
	}
	reservation := EmailReservation{
		CanonicalEmail: input.CanonicalEmail,
		UserID:         input.UserID,
		DisplayEmail:   input.DisplayEmail,
		CreatedAt:      time.Now().UTC(),
	}
	identity := ExternalIdentity{
		Provider:              input.Provider,
		Issuer:                input.Issuer,
		Subject:               input.Subject,
		UserID:                input.UserID,
		ProviderEmail:         strings.TrimSpace(input.ProviderEmail),
		ProviderEmailVerified: input.ProviderEmailVerified,
		CreatedAt:             time.Now().UTC(),
	}
	project := map[string]interface{}{
		"name":       "My First Wiki",
		"created_at": nil,
	}
	tx.writes = append(tx.writes,
		func(transaction *firestore.Transaction) error { return transaction.Create(userRef, user) },
		func(transaction *firestore.Transaction) error { return transaction.Create(reservationRef, reservation) },
		func(transaction *firestore.Transaction) error { return transaction.Create(identityRef, identity) },
		func(transaction *firestore.Transaction) error { return transaction.Create(projectRef, project) },
	)
	return nil
}

// ProvisionPasswordUser queues all password registration writes in an existing
// transaction.
func (tx *IdentityTransaction) ProvisionPasswordUser(input PasswordUserProvisioning) error {
	if tx == nil || tx.tx == nil {
		return ErrIdentityRepositoryUnavailable
	}
	input.DisplayEmail = strings.TrimSpace(input.DisplayEmail)
	input.CanonicalEmail = CanonicalizeEmail(input.CanonicalEmail)
	if input.ProjectID == "" {
		input.ProjectID = defaultProjectID
	}
	if input.UserID == "" || input.DisplayEmail == "" || input.PasswordHash == "" || input.CanonicalEmail == "" || CanonicalizeEmail(input.DisplayEmail) != input.CanonicalEmail {
		return ErrInvalidIdentityInput
	}
	userRef := tx.txClientCollection("users").Doc(input.UserID)
	reservationRef := tx.txClientCollection(EmailReservationsCollection).Doc(emailReservationDocumentID(input.CanonicalEmail))
	projectRef := userRef.Collection("projects").Doc(input.ProjectID)
	snapshots, err := tx.tx.GetAll([]*firestore.DocumentRef{userRef, reservationRef, projectRef})
	if err != nil {
		return err
	}
	userSnapshot, reservationSnapshot, projectSnapshot := snapshots[0], snapshots[1], snapshots[2]

	if userSnapshot.Exists() {
		user, decodeErr := decodeUserRecord(userSnapshot)
		if decodeErr != nil {
			return decodeErr
		}
		if CanonicalizeEmail(user.Email) != input.CanonicalEmail || (user.EmailCanonical != "" && user.EmailCanonical != input.CanonicalEmail) || user.PasswordHash != input.PasswordHash {
			return ErrCanonicalEmailConflict
		}
	}
	if reservationSnapshot.Exists() {
		reservation, decodeErr := decodeEmailReservation(reservationSnapshot)
		if decodeErr != nil {
			return decodeErr
		}
		if reservation.CanonicalEmail != input.CanonicalEmail || reservation.UserID != input.UserID {
			return ErrCanonicalEmailConflict
		}
	}
	if !userSnapshot.Exists() && (reservationSnapshot.Exists() || projectSnapshot.Exists()) {
		return ErrMalformedIdentityRecord
	}
	if !userSnapshot.Exists() && !reservationSnapshot.Exists() {
		if err := tx.rejectLegacyCanonicalCollision(input.CanonicalEmail, input.UserID); err != nil {
			return err
		}
	}
	if !userSnapshot.Exists() {
		user := UserRecord{
			Email:          input.DisplayEmail,
			EmailCanonical: input.CanonicalEmail,
			PasswordHash:   input.PasswordHash,
			EmailVerified:  false,
			ProjectCount:   0,
			DefaultProject: input.ProjectID,
		}
		tx.writes = append(tx.writes, func(transaction *firestore.Transaction) error {
			return transaction.Create(userRef, user)
		})
	}
	if !reservationSnapshot.Exists() {
		reservation := EmailReservation{
			CanonicalEmail: input.CanonicalEmail,
			UserID:         input.UserID,
			DisplayEmail:   input.DisplayEmail,
			CreatedAt:      time.Now().UTC(),
		}
		tx.writes = append(tx.writes, func(transaction *firestore.Transaction) error {
			return transaction.Create(reservationRef, reservation)
		})
	}
	if !projectSnapshot.Exists() {
		project := map[string]interface{}{
			"name":       "My First Wiki",
			"created_at": nil,
		}
		tx.writes = append(tx.writes, func(transaction *firestore.Transaction) error {
			return transaction.Create(projectRef, project)
		})
	}
	return nil
}

func normalizedCanonicalEmail(email string) (string, error) {
	canonical := CanonicalizeEmail(email)
	if canonical == "" {
		return "", ErrInvalidIdentityInput
	}
	return canonical, nil
}

func normalizedExternalIdentity(provider, issuer, subject string) (string, string, string, error) {
	provider = strings.TrimSpace(provider)
	issuer = strings.TrimSpace(issuer)
	if provider == "" || issuer == "" || strings.TrimSpace(subject) == "" {
		return "", "", "", ErrInvalidIdentityInput
	}
	return provider, issuer, subject, nil
}

func emailReservationDocumentID(canonical string) string {
	return opaqueDigest("email\x00" + canonical)
}

func externalIdentityDocumentID(provider, issuer, subject string) string {
	value, _ := json.Marshal([]string{provider, issuer, subject})
	return opaqueDigest("external\x00" + string(value))
}

func opaqueDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (tx *IdentityTransaction) txClientCollection(name string) *firestore.CollectionRef {
	return tx.client.Collection(name)
}

// rejectLegacyCanonicalCollision keeps registration fail-closed during the
// migration window, before every legacy user has a reservation. The audit
// command remains the durable rollout path; this scan is intentionally a
// temporary compatibility guard and should disappear once all users are
// backfilled. TODO(LWC-315 rollout): remove this O(users) guard after complete
// backfill; reservations then become the sole lookup path.
func (tx *IdentityTransaction) rejectLegacyCanonicalCollision(canonical, userID string) error {
	iter := tx.tx.Documents(tx.txClientCollection("users"))
	defer iter.Stop()
	for {
		snapshot, err := iter.Next()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				return nil
			}
			return err
		}
		if snapshot.Ref.ID == userID {
			continue
		}
		user, decodeErr := decodeUserRecord(snapshot)
		if decodeErr != nil {
			return ErrMalformedIdentityRecord
		}
		if CanonicalizeEmail(user.Email) == canonical {
			return ErrCanonicalEmailConflict
		}
	}
}

func optionalTransactionGet(tx *firestore.Transaction, ref *firestore.DocumentRef) (*firestore.DocumentSnapshot, error) {
	snapshot, err := tx.Get(ref)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, err
	}
	return snapshot, nil
}

func validateReservationDisplay(canonical, displayEmail string) error {
	displayEmail = strings.TrimSpace(displayEmail)
	if displayEmail != "" && CanonicalizeEmail(displayEmail) != canonical {
		return ErrInvalidIdentityInput
	}
	return nil
}

func decodeEmailReservation(snapshot *firestore.DocumentSnapshot) (*EmailReservation, error) {
	if snapshot == nil || !snapshot.Exists() {
		return nil, nil
	}
	var reservation EmailReservation
	if err := snapshot.DataTo(&reservation); err != nil || reservation.UserID == "" || reservation.CanonicalEmail == "" || CanonicalizeEmail(reservation.CanonicalEmail) != reservation.CanonicalEmail {
		return nil, ErrMalformedIdentityRecord
	}
	return &reservation, nil
}

func decodeExternalIdentity(snapshot *firestore.DocumentSnapshot) (*ExternalIdentity, error) {
	if snapshot == nil || !snapshot.Exists() {
		return nil, nil
	}
	var identity ExternalIdentity
	if err := snapshot.DataTo(&identity); err != nil || identity.Provider == "" || identity.Issuer == "" || identity.Subject == "" || identity.UserID == "" {
		return nil, ErrMalformedIdentityRecord
	}
	return &identity, nil
}

func decodeUserRecord(snapshot *firestore.DocumentSnapshot) (*UserRecord, error) {
	if snapshot == nil || !snapshot.Exists() {
		return nil, ErrMalformedIdentityRecord
	}
	var user UserRecord
	if err := snapshot.DataTo(&user); err != nil || strings.TrimSpace(user.Email) == "" {
		return nil, ErrMalformedIdentityRecord
	}
	return &user, nil
}

// AuditReport is safe to print as evidence: it contains counts and opaque
// references only, never canonical emails, provider subjects, or secrets.
type AuditReport struct {
	Mode                     string   `json:"mode"`
	UsersScanned             int      `json:"users_scanned"`
	ReservationsPresent      int      `json:"reservations_present"`
	ReservationsMissing      int      `json:"reservations_missing"`
	ReservationsCreated      int      `json:"reservations_created"`
	CanonicalCollisionCount  int      `json:"canonical_collision_count"`
	MalformedRecordCount     int      `json:"malformed_record_count"`
	ReservationConflictCount int      `json:"reservation_conflict_count"`
	OpaqueConflictRefs       []string `json:"opaque_conflict_refs,omitempty"`
}

// AuditValidationError indicates that apply was refused before any write.
type AuditValidationError struct {
	Report AuditReport
}

func (e *AuditValidationError) Error() string {
	return "identity audit validation failed"
}

type auditUser struct {
	id        string
	canonical string
}

// AuditAndBackfill validates every user and its corresponding reservation. In
// apply mode, writes begin only after the complete scan has passed validation.
func (r *IdentityRepository) AuditAndBackfill(ctx context.Context, apply bool) (AuditReport, error) {
	report := AuditReport{Mode: "dry-run"}
	if apply {
		report.Mode = "apply"
	}
	if r == nil || r.fs == nil {
		return report, ErrIdentityRepositoryUnavailable
	}
	users := make([]auditUser, 0)
	canonicalOwners := make(map[string]string)
	iter := r.fs.Collection("users").Documents(ctx)
	defer iter.Stop()
	for {
		snapshot, err := iter.Next()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				break
			}
			return report, err
		}
		report.UsersScanned++
		user, decodeErr := decodeUserRecord(snapshot)
		if decodeErr != nil {
			report.MalformedRecordCount++
			report.OpaqueConflictRefs = appendOpaqueRef(report.OpaqueConflictRefs, snapshot.Ref.ID)
			continue
		}
		canonical := CanonicalizeEmail(user.Email)
		if user.EmailCanonical != "" && user.EmailCanonical != canonical {
			report.MalformedRecordCount++
			report.OpaqueConflictRefs = appendOpaqueRef(report.OpaqueConflictRefs, snapshot.Ref.ID)
			continue
		}
		if prior, ok := canonicalOwners[canonical]; ok && prior != snapshot.Ref.ID {
			report.CanonicalCollisionCount++
			report.OpaqueConflictRefs = appendOpaqueRef(report.OpaqueConflictRefs, prior, snapshot.Ref.ID)
			continue
		}
		canonicalOwners[canonical] = snapshot.Ref.ID
		users = append(users, auditUser{id: snapshot.Ref.ID, canonical: canonical})
	}
	if report.MalformedRecordCount != 0 || report.CanonicalCollisionCount != 0 {
		return report, &AuditValidationError{Report: report}
	}

	reservationRefs := make([]*firestore.DocumentRef, 0, len(users))
	missing := make([]auditUser, 0)
	for _, user := range users {
		ref := r.fs.Collection(EmailReservationsCollection).Doc(emailReservationDocumentID(user.canonical))
		snapshot, err := ref.Get(ctx)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				reservationRefs = append(reservationRefs, ref)
				missing = append(missing, user)
				continue
			}
			return report, err
		}
		reservation, decodeErr := decodeEmailReservation(snapshot)
		if decodeErr != nil {
			report.MalformedRecordCount++
			report.OpaqueConflictRefs = appendOpaqueRef(report.OpaqueConflictRefs, snapshot.Ref.ID)
			continue
		}
		if reservation.UserID != user.id || reservation.CanonicalEmail != user.canonical {
			report.ReservationConflictCount++
			report.OpaqueConflictRefs = appendOpaqueRef(report.OpaqueConflictRefs, user.id, reservation.UserID)
			continue
		}
		report.ReservationsPresent++
	}
	report.ReservationsMissing = len(missing)
	if report.MalformedRecordCount != 0 || report.ReservationConflictCount != 0 {
		return report, &AuditValidationError{Report: report}
	}
	if !apply || len(missing) == 0 {
		return report, nil
	}

	// One transaction reads every planned reservation before creating any. A
	// concurrent registration therefore aborts the apply instead of allowing a
	// stale audit to overwrite ownership.
	err := r.fs.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		snapshots, err := tx.GetAll(reservationRefs)
		if err != nil {
			return err
		}
		for i, snapshot := range snapshots {
			if snapshot.Exists() {
				return ErrCanonicalEmailConflict
			}
			reservation := EmailReservation{
				CanonicalEmail: missing[i].canonical,
				UserID:         missing[i].id,
				DisplayEmail:   missing[i].canonical,
				CreatedAt:      time.Now().UTC(),
			}
			if err := tx.Create(reservationRefs[i], reservation); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrCanonicalEmailConflict) {
			report.ReservationConflictCount++
			return report, &AuditValidationError{Report: report}
		}
		return report, err
	}
	report.ReservationsCreated = len(missing)
	return report, nil
}

func appendOpaqueRef(refs []string, values ...string) []string {
	seen := make(map[string]struct{}, len(refs)+len(values))
	for _, ref := range refs {
		seen[ref] = struct{}{}
	}
	for _, value := range values {
		ref := opaqueDigest("ref\x00" + value)[:16]
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}
