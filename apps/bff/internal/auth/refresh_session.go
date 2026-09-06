package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	refreshSessionsCollection       = "auth_refresh_sessions"
	refreshSessionReplaysCollection = "auth_refresh_session_replays"
	maxRefreshTokenBytes            = 16 << 10
)

// RefreshSessionMigrationMode controls whether a valid pre-LWC-320 refresh
// JWT may be imported into the durable authority on its first refresh.
type RefreshSessionMigrationMode string

const (
	RefreshSessionMigrationDisabled          RefreshSessionMigrationMode = "disabled"
	RefreshSessionMigrationLegacyReadThrough RefreshSessionMigrationMode = "legacy_read_through"
)

var (
	ErrRefreshSessionInvalid     = errors.New("invalid refresh session")
	ErrRefreshSessionReplay      = errors.New("refresh session replay")
	ErrRefreshSessionRevoked     = errors.New("refresh session revoked")
	ErrRefreshSessionExpired     = errors.New("refresh session expired")
	ErrRefreshSessionUnavailable = errors.New("refresh session authority unavailable")
)

// SessionAuthorityConfig is intentionally explicit: a session database must
// never be shared accidentally by two deployment environments.
type SessionAuthorityConfig struct {
	Environment string
	Migration   RefreshSessionMigrationMode
}

// RefreshSessionRotation contains the new application refresh material. The
// token is returned only to the caller and is never written to Firestore.
type RefreshSessionRotation struct {
	Token     string
	UserID    string
	Role      string
	ExpiresAt time.Time
}

type refreshSessionDocument struct {
	SessionID   string    `firestore:"session_id"`
	Environment string    `firestore:"environment"`
	UserID      string    `firestore:"user_id"`
	Role        string    `firestore:"role,omitempty"`
	TokenHash   string    `firestore:"token_hash"`
	Status      string    `firestore:"status"`
	IssuedAt    time.Time `firestore:"issued_at"`
	ExpiresAt   time.Time `firestore:"expires_at"`
	CreatedAt   time.Time `firestore:"created_at"`
	UpdatedAt   time.Time `firestore:"updated_at"`
	RevokedAt   time.Time `firestore:"revoked_at,omitempty"`
}

type refreshSessionReplayDocument struct {
	Environment string    `firestore:"environment"`
	SessionID   string    `firestore:"session_id"`
	TokenHash   string    `firestore:"token_hash"`
	ExpiresAt   time.Time `firestore:"expires_at"`
}

const (
	refreshSessionStatusActive        = "active"
	refreshSessionStatusRevoked       = "revoked"
	refreshSessionStatusExpired       = "expired"
	refreshSessionMaxEnvironmentBytes = 128
)

// RefreshSessionAuthority is the single durable refresh-session owner shared
// by password and federated authentication. Its environment is part of every
// record and document identity, so a token from another environment cannot be
// looked up even when signing secrets are accidentally equal.
type RefreshSessionAuthority struct {
	fs          *firestore.Client
	environment string
	migration   RefreshSessionMigrationMode
	now         func() time.Time
}

// NewRefreshSessionAuthority constructs an authority with migration disabled.
func NewRefreshSessionAuthority(fs *firestore.Client, environment string) *RefreshSessionAuthority {
	return NewRefreshSessionAuthorityWithConfig(fs, SessionAuthorityConfig{Environment: environment, Migration: RefreshSessionMigrationDisabled})
}

// NewRefreshSessionAuthorityWithConfig constructs an authority. Invalid
// configuration is retained as an unavailable authority and fails closed on
// all operations; production callers must not silently fall back to memory.
func NewRefreshSessionAuthorityWithConfig(fs *firestore.Client, cfg SessionAuthorityConfig) *RefreshSessionAuthority {
	migration := cfg.Migration
	if migration == "" {
		migration = RefreshSessionMigrationDisabled
	}
	if migration != RefreshSessionMigrationDisabled && migration != RefreshSessionMigrationLegacyReadThrough {
		migration = RefreshSessionMigrationDisabled
	}
	return &RefreshSessionAuthority{
		fs: fs, environment: strings.TrimSpace(cfg.Environment), migration: migration, now: time.Now,
	}
}

func (a *RefreshSessionAuthority) ready() bool {
	return a != nil && a.fs != nil && a.environment != "" && len(a.environment) <= refreshSessionMaxEnvironmentBytes
}

func (a *RefreshSessionAuthority) collection() *firestore.CollectionRef {
	return a.fs.Collection(refreshSessionsCollection)
}

func (a *RefreshSessionAuthority) sessionRef(sessionID string) *firestore.DocumentRef {
	return a.collection().Doc(refreshSessionDocumentID(a.environment, sessionID))
}

func (a *RefreshSessionAuthority) replayRef(sessionID, tokenHash string) *firestore.DocumentRef {
	return a.fs.Collection(refreshSessionReplaysCollection).Doc(refreshSessionDocumentID(a.environment, sessionID+"\x00"+tokenHash))
}

func refreshSessionDocumentID(environment, sessionID string) string {
	digest := sha256.Sum256([]byte(environment + "\x00" + sessionID))
	return hex.EncodeToString(digest[:])
}

func hashRefreshToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func issueRefreshSession(ctx context.Context, authority *RefreshSessionAuthority, userID, role, secret string) (string, error) {
	if authority != nil {
		return authority.Issue(ctx, userID, role, secret)
	}
	return GenerateRefreshToken(userID, role, secret)
}

// Issue creates a durable session and returns a signed refresh JWT carrying
// only a durable session identifier. The raw token exists only in memory long
// enough to set the HttpOnly cookie.
func (a *RefreshSessionAuthority) Issue(ctx context.Context, userID, role, secret string) (string, error) {
	if !a.ready() || strings.TrimSpace(secret) == "" || !ValidPathSegment(strings.TrimSpace(userID)) {
		return "", ErrRefreshSessionUnavailable
	}
	userID = strings.TrimSpace(userID)
	sessionID, err := randomTokenID()
	if err != nil {
		return "", err
	}
	now := a.now().UTC()
	token, err := generateRefreshTokenAt(userID, role, secret, sessionID, now)
	if err != nil {
		return "", err
	}
	doc := refreshSessionDocument{
		SessionID: sessionID, Environment: a.environment, UserID: userID, Role: role,
		TokenHash: hashRefreshToken(token), Status: refreshSessionStatusActive,
		IssuedAt: now, ExpiresAt: now.Add(refreshTokenTTL), CreatedAt: now, UpdatedAt: now,
	}
	if _, err := a.sessionRef(sessionID).Create(ctx, doc); err != nil {
		return "", err
	}
	return token, nil
}

// Rotate atomically consumes the current token hash and replaces it. A
// concurrent caller rereads the changed document and deterministically gets
// ErrRefreshSessionReplay; it cannot issue a second token.
func (a *RefreshSessionAuthority) Rotate(ctx context.Context, rawToken, secret string) (RefreshSessionRotation, error) {
	rawToken = strings.TrimSpace(rawToken)
	if !a.ready() || strings.TrimSpace(secret) == "" || rawToken == "" || len(rawToken) > maxRefreshTokenBytes {
		return RefreshSessionRotation{}, ErrRefreshSessionInvalid
	}
	claims, err := parseRefreshToken(rawToken, secret)
	if err != nil {
		return RefreshSessionRotation{}, ErrRefreshSessionInvalid
	}
	sessionID := strings.TrimSpace(claims.SessionID)
	legacy := false
	if sessionID == "" {
		sessionID = strings.TrimSpace(claims.ID)
		legacy = sessionID != ""
	}
	if !ValidPathSegment(sessionID) || strings.TrimSpace(claims.Sub) == "" {
		return RefreshSessionRotation{}, ErrRefreshSessionInvalid
	}

	var result RefreshSessionRotation
	var terminalErr error
	err = a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		terminalErr = nil
		ref := a.sessionRef(sessionID)
		tokenHash := hashRefreshToken(rawToken)
		replayRef := a.replayRef(sessionID, tokenHash)
		replaySnapshot, replayErr := tx.Get(replayRef)
		if replayErr == nil && replaySnapshot.Exists() {
			return ErrRefreshSessionReplay
		}
		if replayErr != nil && status.Code(replayErr) != codes.NotFound {
			return replayErr
		}
		snapshot, getErr := tx.Get(ref)
		if getErr != nil {
			if status.Code(getErr) != codes.NotFound {
				return getErr
			}
			if a.migration != RefreshSessionMigrationLegacyReadThrough || !legacy {
				return ErrRefreshSessionInvalid
			}
			return a.migrateAndRotate(tx, ref, replayRef, sessionID, tokenHash, claims, &result, secret)
		}

		var session refreshSessionDocument
		if err := snapshot.DataTo(&session); err != nil {
			return ErrRefreshSessionInvalid
		}
		if session.Environment != a.environment || session.SessionID != sessionID || session.UserID != claims.Sub {
			return ErrRefreshSessionInvalid
		}
		now := a.now().UTC()
		if session.Status == refreshSessionStatusRevoked || !session.RevokedAt.IsZero() {
			return ErrRefreshSessionRevoked
		}
		if session.Status == refreshSessionStatusExpired || !now.Before(session.ExpiresAt) {
			if err := tx.Update(ref, []firestore.Update{{Path: "status", Value: refreshSessionStatusExpired}, {Path: "updated_at", Value: now}}); err != nil {
				return err
			}
			terminalErr = ErrRefreshSessionExpired
			return nil
		}
		if session.TokenHash != tokenHash {
			return ErrRefreshSessionReplay
		}
		return a.rotateExisting(tx, ref, replayRef, session, &result, now, secret)
	})
	if err == nil && terminalErr != nil {
		err = terminalErr
	}
	if err != nil {
		return RefreshSessionRotation{}, err
	}
	return result, nil
}

func (a *RefreshSessionAuthority) migrateAndRotate(tx *firestore.Transaction, ref, replayRef *firestore.DocumentRef, sessionID, tokenHash string, claims *Claims, result *RefreshSessionRotation, secret string) error {
	now := a.now().UTC()
	if claims.ExpiresAt == nil || !now.Before(claims.ExpiresAt.Time) {
		return ErrRefreshSessionExpired
	}
	role := claims.Role
	token, err := generateRefreshTokenAt(claims.Sub, role, secret, sessionID, now)
	if err != nil {
		return err
	}
	// The legacy token was already signature/expiry validated. Its only durable
	// trace is this digest, allowing rollback/migration without retaining token
	// material or a process-local registry.
	doc := refreshSessionDocument{
		SessionID: sessionID, Environment: a.environment, UserID: claims.Sub, Role: role,
		TokenHash: hashRefreshToken(token), Status: refreshSessionStatusActive,
		IssuedAt: now, ExpiresAt: now.Add(refreshTokenTTL), CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(ref, doc); err != nil {
		return err
	}
	if err := tx.Create(replayRef, refreshSessionReplayDocument{Environment: a.environment, SessionID: sessionID, TokenHash: tokenHash, ExpiresAt: doc.ExpiresAt}); err != nil {
		return err
	}
	*result = RefreshSessionRotation{Token: token, UserID: claims.Sub, Role: role, ExpiresAt: doc.ExpiresAt}
	return nil
}

func (a *RefreshSessionAuthority) rotateExisting(tx *firestore.Transaction, ref, replayRef *firestore.DocumentRef, session refreshSessionDocument, result *RefreshSessionRotation, now time.Time, secret string) error {
	token, err := generateRefreshTokenAt(session.UserID, session.Role, secret, session.SessionID, now)
	if err != nil {
		return err
	}
	expiresAt := now.Add(refreshTokenTTL)
	if err := tx.Create(replayRef, refreshSessionReplayDocument{Environment: a.environment, SessionID: session.SessionID, TokenHash: session.TokenHash, ExpiresAt: expiresAt}); err != nil {
		return err
	}
	if err := tx.Update(ref, []firestore.Update{
		{Path: "token_hash", Value: hashRefreshToken(token)},
		{Path: "status", Value: refreshSessionStatusActive},
		{Path: "issued_at", Value: now},
		{Path: "expires_at", Value: expiresAt},
		{Path: "updated_at", Value: now},
	}); err != nil {
		return err
	}
	*result = RefreshSessionRotation{Token: token, UserID: session.UserID, Role: session.Role, ExpiresAt: expiresAt}
	return nil
}

// Revoke invalidates the durable session represented by rawToken. It is safe
// to call for an already-revoked, missing, or legacy token during logout.
func (a *RefreshSessionAuthority) Revoke(ctx context.Context, rawToken, secret string) error {
	if !a.ready() || strings.TrimSpace(rawToken) == "" {
		return nil
	}
	claims, err := parseRefreshToken(rawToken, secret)
	if err != nil {
		return nil
	}
	sessionID := strings.TrimSpace(claims.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(claims.ID)
	}
	if !ValidPathSegment(sessionID) {
		return nil
	}
	ref := a.sessionRef(sessionID)
	return a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil
			}
			return err
		}
		var session refreshSessionDocument
		if err := snapshot.DataTo(&session); err != nil || session.Environment != a.environment || session.SessionID != sessionID {
			return nil
		}
		if session.Status == refreshSessionStatusRevoked {
			return nil
		}
		now := a.now().UTC()
		return tx.Update(ref, []firestore.Update{{Path: "status", Value: refreshSessionStatusRevoked}, {Path: "revoked_at", Value: now}, {Path: "updated_at", Value: now}})
	})
}

// RevokeUserSessions is the account-wide invalidation seam for future account
// lifecycle policy. It revokes only this authority's environment records.
func (a *RefreshSessionAuthority) RevokeUserSessions(ctx context.Context, userID string) (int, error) {
	userID = strings.TrimSpace(userID)
	if !a.ready() || !ValidPathSegment(userID) {
		return 0, ErrRefreshSessionUnavailable
	}
	iter := a.collection().Where("environment", "==", a.environment).Documents(ctx)
	defer iter.Stop()
	var refs []*firestore.DocumentRef
	for {
		snapshot, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return 0, err
		}
		var session refreshSessionDocument
		if snapshot.DataTo(&session) == nil && session.UserID == userID && session.Status != refreshSessionStatusRevoked {
			refs = append(refs, snapshot.Ref)
		}
	}
	now := a.now().UTC()
	for start := 0; start < len(refs); start += 500 {
		end := start + 500
		if end > len(refs) {
			end = len(refs)
		}
		batch := a.fs.Batch()
		for _, ref := range refs[start:end] {
			batch.Update(ref, []firestore.Update{{Path: "status", Value: refreshSessionStatusRevoked}, {Path: "revoked_at", Value: now}, {Path: "updated_at", Value: now}})
		}
		if _, err := batch.Commit(ctx); err != nil {
			return 0, err
		}
	}
	return len(refs), nil
}

// CleanupExpired deletes expired sessions for this environment. Expiration is
// always enforced by Rotate; cleanup is bounded maintenance only.
func (a *RefreshSessionAuthority) CleanupExpired(ctx context.Context, now time.Time, limit int) (int, error) {
	if !a.ready() {
		return 0, ErrRefreshSessionUnavailable
	}
	if limit <= 0 {
		limit = 500
	}
	if now.IsZero() {
		now = a.now().UTC()
	}
	iter := a.collection().Where("environment", "==", a.environment).Documents(ctx)
	defer iter.Stop()
	var refs []*firestore.DocumentRef
	for len(refs) < limit {
		snapshot, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return 0, err
		}
		var session refreshSessionDocument
		if snapshot.DataTo(&session) == nil && !session.ExpiresAt.After(now) {
			refs = append(refs, snapshot.Ref)
		}
	}
	removed := 0
	for start := 0; start < len(refs); start += 500 {
		end := start + 500
		if end > len(refs) {
			end = len(refs)
		}
		batch := a.fs.Batch()
		for _, ref := range refs[start:end] {
			batch.Delete(ref)
		}
		if _, err := batch.Commit(ctx); err != nil {
			return removed, err
		}
		removed += end - start
	}
	if removed < limit {
		iter := a.fs.Collection(refreshSessionReplaysCollection).Where("environment", "==", a.environment).Documents(ctx)
		defer iter.Stop()
		var replayRefs []*firestore.DocumentRef
		for removed+len(replayRefs) < limit {
			snapshot, err := iter.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				return removed, err
			}
			var replay refreshSessionReplayDocument
			if snapshot.DataTo(&replay) == nil && !replay.ExpiresAt.After(now) {
				replayRefs = append(replayRefs, snapshot.Ref)
			}
		}
		for start := 0; start < len(replayRefs); start += 500 {
			end := start + 500
			if end > len(replayRefs) {
				end = len(replayRefs)
			}
			batch := a.fs.Batch()
			for _, ref := range replayRefs[start:end] {
				batch.Delete(ref)
			}
			if _, err := batch.Commit(ctx); err != nil {
				return removed, err
			}
		}
	}
	return removed, nil
}
