package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
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
	AuthVersion int64
	Token       string
	UserID      string
	Role        string
	ExpiresAt   time.Time
}

// CLISessionCredential contains one CLI session's access and refresh tokens.
// RefreshToken is returned once and only its hash is retained by the authority.
type CLISessionCredential struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	SessionID    string `json:"session_id"`
	UserID       string `json:"user_id"`
	Role         string `json:"role"`
	AuthVersion  int64  `json:"auth_version"`
}

// CLISessionInfo is the non-secret self-service view of a CLI session.
type CLISessionInfo struct {
	ID         string    `json:"id"`
	ClientName string    `json:"client_name"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type refreshSessionDocument struct {
	SessionID          string    `firestore:"session_id"`
	Environment        string    `firestore:"environment"`
	UserID             string    `firestore:"user_id"`
	Role               string    `firestore:"role,omitempty"`
	ClientKind         string    `firestore:"client_kind,omitempty"`
	ClientName         string    `firestore:"client_name,omitempty"`
	AuthVersion        int64     `firestore:"auth_version,omitempty"`
	TokenHash          string    `firestore:"token_hash"`
	PreviousTokenHash  string    `firestore:"previous_token_hash,omitempty"`
	RetryTokenNonce    string    `firestore:"retry_token_nonce,omitempty"`
	RetryUntil         time.Time `firestore:"retry_until,omitempty"`
	RotationGeneration int64     `firestore:"rotation_generation,omitempty"`
	Status             string    `firestore:"status"`
	IssuedAt           time.Time `firestore:"issued_at"`
	ExpiresAt          time.Time `firestore:"expires_at,omitempty"`
	CreatedAt          time.Time `firestore:"created_at"`
	UpdatedAt          time.Time `firestore:"updated_at"`
	RevokedAt          time.Time `firestore:"revoked_at,omitempty"`
}

type refreshSessionReplayDocument struct {
	Environment string    `firestore:"environment"`
	SessionID   string    `firestore:"session_id"`
	TokenHash   string    `firestore:"token_hash"`
	ExpiresAt   time.Time `firestore:"expires_at"`
}

type cliSessionAuditEvent struct {
	Action    string    `firestore:"action"`
	ActorID   string    `firestore:"actor_id"`
	SessionID string    `firestore:"session_id"`
	Reason    string    `firestore:"reason"`
	Result    string    `firestore:"result"`
	CreatedAt time.Time `firestore:"created_at"`
}

type refreshSessionCleanupCandidate struct {
	ref        *firestore.DocumentRef
	updateTime time.Time
}

const (
	refreshSessionStatusActive        = "active"
	refreshSessionStatusRevoked       = "revoked"
	refreshSessionStatusExpired       = "expired"
	refreshSessionMaxEnvironmentBytes = 128
	cliRefreshRetryWindow             = 30 * time.Second
)

// RefreshSessionAuthority is the single durable refresh-session owner shared
// by password and federated authentication. Its environment is part of every
// record and document identity, so a token from another environment cannot be
// looked up even when signing secrets are accidentally equal.
type RefreshSessionAuthority struct {
	fs                  *firestore.Client
	environment         string
	migration           RefreshSessionMigrationMode
	now                 func() time.Time
	beforeCleanupDelete func()
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

func issueRefreshSession(ctx context.Context, authority *RefreshSessionAuthority, userID, role, secret string, version ...int64) (string, error) {
	if authority != nil {
		return authority.Issue(ctx, userID, role, secret, version...)
	}
	return GenerateRefreshToken(userID, role, secret, version...)
}

// Issue creates a durable session and returns a signed refresh JWT carrying
// only a durable session identifier. The raw token exists only in memory long
// enough to set the HttpOnly cookie.
func (a *RefreshSessionAuthority) Issue(ctx context.Context, userID, role, secret string, version ...int64) (string, error) {
	if !a.ready() || strings.TrimSpace(secret) == "" || !ValidPathSegment(strings.TrimSpace(userID)) {
		return "", ErrRefreshSessionUnavailable
	}
	userID = strings.TrimSpace(userID)
	sessionID, err := randomTokenID()
	if err != nil {
		return "", err
	}
	now := a.now().UTC()
	token, err := generateRefreshTokenAt(userID, role, secret, sessionID, now, version...)
	if err != nil {
		return "", err
	}
	doc := refreshSessionDocument{
		SessionID: sessionID, Environment: a.environment, UserID: userID, Role: role,
		TokenHash: hashRefreshToken(token), Status: refreshSessionStatusActive,
		IssuedAt: now, ExpiresAt: now.Add(refreshTokenTTL), CreatedAt: now, UpdatedAt: now,
	}
	if err := a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(a.fs.Collection("users").Doc(userID))
		if err != nil {
			return ErrAccountUnavailable
		}
		var user UserRecord
		expected := int64(0)
		if len(version) > 0 {
			expected = version[0]
		}
		if snapshot.DataTo(&user) != nil || !user.AllowsVersion(expected) {
			return ErrAccountUnavailable
		}
		return tx.Create(a.sessionRef(sessionID), doc)
	}); err != nil {
		return "", err
	}
	return token, nil
}

// IssueCLISession creates a non-expiring, revocable CLI session in the shared
// durable session collection. Its opaque refresh token is stored only as a
// hash; session expiry is controlled by explicit revocation and account state.
func (a *RefreshSessionAuthority) IssueCLISession(ctx context.Context, userID, clientName, jwtSecret string) (CLISessionCredential, error) {
	userID = strings.TrimSpace(userID)
	if !a.ready() || strings.TrimSpace(jwtSecret) == "" || !ValidPathSegment(userID) {
		return CLISessionCredential{}, ErrCLISessionUnavailable
	}
	sessionID, err := randomTokenID()
	if err != nil {
		return CLISessionCredential{}, err
	}
	refreshToken, err := newCLIRefreshToken(sessionID)
	if err != nil {
		return CLISessionCredential{}, err
	}
	now := a.now().UTC()
	var result CLISessionCredential
	err = a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		account, err := tx.Get(a.fs.Collection("users").Doc(userID))
		if err != nil {
			return ErrCLISessionUnavailable
		}
		var user UserRecord
		if err := account.DataTo(&user); err != nil {
			return ErrCLISessionUnavailable
		}
		if !user.Active() {
			return ErrCLISessionRevoked
		}
		credential, err := a.createCLISessionInTransaction(tx, userID, &user, clientName, jwtSecret, sessionID, refreshToken, now)
		if err != nil {
			return err
		}
		result = credential
		return nil
	})
	if err != nil {
		return CLISessionCredential{}, err
	}
	return result, nil
}

func (a *RefreshSessionAuthority) createCLISessionInTransaction(tx *firestore.Transaction, userID string, user *UserRecord, clientName, jwtSecret, sessionID, refreshToken string, now time.Time) (CLISessionCredential, error) {
	if user == nil || !user.Active() || !ValidPathSegment(userID) || !ValidPathSegment(sessionID) || !a.ready() {
		return CLISessionCredential{}, ErrCLISessionRevoked
	}
	accessToken, err := GenerateCLIAccessToken(userID, user.Role, sessionID, jwtSecret, user.AuthVersion)
	if err != nil {
		return CLISessionCredential{}, err
	}
	session := refreshSessionDocument{
		SessionID: sessionID, Environment: a.environment, UserID: userID, Role: user.Role,
		ClientKind: cliClientKind, ClientName: normalizeCLIClientName(clientName), AuthVersion: user.AuthVersion,
		TokenHash: hashRefreshToken(refreshToken), Status: refreshSessionStatusActive,
		IssuedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(a.sessionRef(sessionID), session); err != nil {
		return CLISessionCredential{}, err
	}
	return CLISessionCredential{AccessToken: accessToken, RefreshToken: refreshToken, SessionID: sessionID, UserID: userID, Role: user.Role, AuthVersion: user.AuthVersion}, nil
}

// RotateCLISession rotates an opaque CLI refresh token. The immediately
// previous token can recover the same next token for a short, fixed retry
// window; it cannot advance the session to another generation.
func (a *RefreshSessionAuthority) RotateCLISession(ctx context.Context, rawToken, jwtSecret string) (CLISessionCredential, error) {
	rawToken = strings.TrimSpace(rawToken)
	sessionID, valid := cliRefreshSessionID(rawToken)
	if !a.ready() || strings.TrimSpace(jwtSecret) == "" || !valid || len(rawToken) > maxRefreshTokenBytes {
		return CLISessionCredential{}, ErrCLISessionInvalid
	}
	tokenHash := hashRefreshToken(rawToken)
	ref := a.sessionRef(sessionID)
	var result CLISessionCredential
	err := a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrCLISessionInvalid
			}
			return ErrCLISessionUnavailable
		}
		var session refreshSessionDocument
		if err := snapshot.DataTo(&session); err != nil {
			return ErrCLISessionUnavailable
		}
		if session.Environment != a.environment || session.SessionID != sessionID || session.ClientKind != cliClientKind {
			return ErrCLISessionInvalid
		}
		if session.Status != refreshSessionStatusActive || !session.RevokedAt.IsZero() {
			return ErrCLISessionRevoked
		}
		account, err := tx.Get(a.fs.Collection("users").Doc(session.UserID))
		if err != nil {
			return ErrCLISessionUnavailable
		}
		var user UserRecord
		if err := account.DataTo(&user); err != nil {
			return ErrCLISessionUnavailable
		}
		if !user.AllowsVersion(session.AuthVersion) {
			return ErrCLISessionRevoked
		}
		now := a.now().UTC()
		if session.TokenHash != tokenHash {
			if session.PreviousTokenHash != tokenHash || session.RetryUntil.IsZero() || !now.Before(session.RetryUntil) || session.RetryTokenNonce == "" || session.RotationGeneration <= 0 {
				return ErrCLISessionReplay
			}
			retryToken, err := deriveCLIRetryToken(jwtSecret, sessionID, session.RetryTokenNonce, uint64(session.RotationGeneration))
			if err != nil || hashRefreshToken(retryToken) != session.TokenHash {
				return ErrCLISessionReplay
			}
			accessToken, err := GenerateCLIAccessToken(session.UserID, user.Role, sessionID, jwtSecret, user.AuthVersion)
			if err != nil {
				return err
			}
			result = CLISessionCredential{AccessToken: accessToken, RefreshToken: retryToken, SessionID: sessionID, UserID: session.UserID, Role: user.Role, AuthVersion: user.AuthVersion}
			return nil
		}
		nonce, err := randomOpaqueSecret()
		if err != nil {
			return err
		}
		generation := session.RotationGeneration + 1
		if generation <= 0 {
			return ErrCLISessionUnavailable
		}
		newToken, err := deriveCLIRetryToken(jwtSecret, sessionID, nonce, uint64(generation))
		if err != nil {
			return err
		}
		accessToken, err := GenerateCLIAccessToken(session.UserID, user.Role, sessionID, jwtSecret, user.AuthVersion)
		if err != nil {
			return err
		}
		if err := tx.Update(ref, []firestore.Update{
			{Path: "token_hash", Value: hashRefreshToken(newToken)},
			{Path: "previous_token_hash", Value: tokenHash},
			{Path: "retry_token_nonce", Value: nonce},
			{Path: "retry_until", Value: now.Add(cliRefreshRetryWindow)},
			{Path: "rotation_generation", Value: generation},
			{Path: "role", Value: user.Role},
			{Path: "auth_version", Value: user.AuthVersion},
			{Path: "issued_at", Value: now},
			{Path: "updated_at", Value: now},
		}); err != nil {
			return err
		}
		result = CLISessionCredential{AccessToken: accessToken, RefreshToken: newToken, SessionID: sessionID, UserID: session.UserID, Role: user.Role, AuthVersion: user.AuthVersion}
		return nil
	})
	if err != nil {
		return CLISessionCredential{}, err
	}
	return result, nil
}

// VerifyCLIAccessSession checks current durable session state on every CLI
// request. It deliberately does not use a positive cache.
func (a *RefreshSessionAuthority) VerifyCLIAccessSession(ctx context.Context, userID, sessionID string, authVersion int64) error {
	userID, sessionID = strings.TrimSpace(userID), strings.TrimSpace(sessionID)
	if !a.ready() {
		return ErrCLISessionUnavailable
	}
	if !ValidPathSegment(userID) || !ValidPathSegment(sessionID) {
		return ErrCLISessionInvalid
	}
	snapshot, err := a.sessionRef(sessionID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ErrCLISessionRevoked
		}
		return ErrCLISessionUnavailable
	}
	var session refreshSessionDocument
	if err := snapshot.DataTo(&session); err != nil {
		return ErrCLISessionUnavailable
	}
	if session.Environment != a.environment || session.SessionID != sessionID || session.UserID != userID || session.ClientKind != cliClientKind || session.AuthVersion != authVersion {
		return ErrCLISessionRevoked
	}
	if session.Status != refreshSessionStatusActive || !session.RevokedAt.IsZero() {
		return ErrCLISessionRevoked
	}
	return nil
}

// ListCLISessions returns only CLI sessions for the requested account.
func (a *RefreshSessionAuthority) ListCLISessions(ctx context.Context, userID string) ([]CLISessionInfo, error) {
	userID = strings.TrimSpace(userID)
	if !a.ready() || !ValidPathSegment(userID) {
		return nil, ErrCLISessionUnavailable
	}
	iter := a.collection().Where("environment", "==", a.environment).Documents(ctx)
	defer iter.Stop()
	items := make([]CLISessionInfo, 0)
	for {
		snapshot, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, ErrCLISessionUnavailable
		}
		var session refreshSessionDocument
		if err := snapshot.DataTo(&session); err != nil {
			return nil, ErrCLISessionUnavailable
		}
		if session.UserID != userID || session.ClientKind != cliClientKind {
			continue
		}
		items = append(items, CLISessionInfo{ID: session.SessionID, ClientName: session.ClientName, Status: session.Status, CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

// RevokeCLISession revokes one CLI session owned by the authenticated account.
func (a *RefreshSessionAuthority) RevokeCLISession(ctx context.Context, userID, sessionID string) error {
	userID, sessionID = strings.TrimSpace(userID), strings.TrimSpace(sessionID)
	if !a.ready() || !ValidPathSegment(userID) || !ValidPathSegment(sessionID) {
		return ErrCLISessionInvalid
	}
	auditID, err := randomTokenID()
	if err != nil {
		return ErrCLISessionUnavailable
	}
	auditRef := a.fs.Collection("auth_audit_events").Doc(refreshSessionDocumentID(a.environment, "cli-session-revoked-"+auditID))
	ref := a.sessionRef(sessionID)
	return a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrCLISessionNotFound
			}
			return ErrCLISessionUnavailable
		}
		var session refreshSessionDocument
		if err := snapshot.DataTo(&session); err != nil {
			return ErrCLISessionUnavailable
		}
		if session.Environment != a.environment || session.UserID != userID || session.ClientKind != cliClientKind {
			return ErrCLISessionNotFound
		}
		if session.Status == refreshSessionStatusRevoked {
			return nil
		}
		now := a.now().UTC()
		if err := tx.Update(ref, []firestore.Update{{Path: "status", Value: refreshSessionStatusRevoked}, {Path: "revoked_at", Value: now}, {Path: "updated_at", Value: now}}); err != nil {
			return err
		}
		return tx.Create(auditRef, cliSessionAuditEvent{Action: "cli_session_revoked", ActorID: userID, SessionID: sessionID, Reason: "user_revoked", Result: refreshSessionStatusRevoked, CreatedAt: now})
	})
}

// RevokeCLISessionWithRefreshToken lets the CLI log out after its access token
// expires while still requiring possession of the current refresh token.
func (a *RefreshSessionAuthority) RevokeCLISessionWithRefreshToken(ctx context.Context, rawToken string) error {
	rawToken = strings.TrimSpace(rawToken)
	sessionID, valid := cliRefreshSessionID(rawToken)
	if !a.ready() {
		return ErrCLISessionUnavailable
	}
	if !valid || len(rawToken) > maxRefreshTokenBytes {
		return ErrCLISessionInvalid
	}
	auditID, err := randomTokenID()
	if err != nil {
		return ErrCLISessionUnavailable
	}
	auditRef := a.fs.Collection("auth_audit_events").Doc(refreshSessionDocumentID(a.environment, "cli-session-logout-"+auditID))
	ref := a.sessionRef(sessionID)
	return a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrCLISessionInvalid
			}
			return ErrCLISessionUnavailable
		}
		var session refreshSessionDocument
		if err := snapshot.DataTo(&session); err != nil {
			return ErrCLISessionUnavailable
		}
		if session.Environment != a.environment || session.SessionID != sessionID || session.ClientKind != cliClientKind {
			return ErrCLISessionInvalid
		}
		if session.Status != refreshSessionStatusActive || !session.RevokedAt.IsZero() {
			return ErrCLISessionRevoked
		}
		if session.TokenHash != hashRefreshToken(rawToken) {
			return ErrCLISessionReplay
		}
		now := a.now().UTC()
		if err := tx.Update(ref, []firestore.Update{{Path: "status", Value: refreshSessionStatusRevoked}, {Path: "revoked_at", Value: now}, {Path: "updated_at", Value: now}}); err != nil {
			return err
		}
		return tx.Create(auditRef, cliSessionAuditEvent{Action: "cli_session_revoked", ActorID: session.UserID, SessionID: sessionID, Reason: "user_logout", Result: refreshSessionStatusRevoked, CreatedAt: now})
	})
}

func normalizeCLIClientName(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if len(value) > 120 {
		value = value[:120]
	}
	if value == "" {
		return "CLI"
	}
	return value
}

func newCLIRefreshToken(sessionID string) (string, error) {
	secret, err := randomOpaqueSecret()
	if err != nil {
		return "", err
	}
	return sessionID + "." + secret, nil
}

func randomOpaqueSecret() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}

func cliRefreshSessionID(token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 || !ValidPathSegment(parts[0]) {
		return "", false
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[1])
	return parts[0], err == nil && len(secret) == 32
}

func deriveCLIRetryToken(jwtSecret, sessionID, nonce string, generation uint64) (string, error) {
	if strings.TrimSpace(jwtSecret) == "" || !ValidPathSegment(sessionID) || strings.TrimSpace(nonce) == "" || generation == 0 {
		return "", ErrCLISessionInvalid
	}
	mac := hmac.New(sha256.New, []byte(jwtSecret))
	_, _ = mac.Write([]byte("lwc-cli-refresh-rotation/v1\x00"))
	_, _ = mac.Write([]byte(sessionID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(nonce))
	_, _ = mac.Write([]byte{0})
	var encodedGeneration [8]byte
	binary.BigEndian.PutUint64(encodedGeneration[:], generation)
	_, _ = mac.Write(encodedGeneration[:])
	return sessionID + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
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
		account, err := tx.Get(a.fs.Collection("users").Doc(claims.Sub))
		if err != nil {
			return ErrAccountUnavailable
		}
		var user UserRecord
		if account.DataTo(&user) != nil || !user.AllowsVersion(claims.AuthVersion) {
			return ErrAccountUnavailable
		}
		claims.Role = user.Role
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
		if session.Environment != a.environment || session.SessionID != sessionID || session.UserID != claims.Sub || session.ClientKind == cliClientKind {
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
		session.Role = user.Role
		return a.rotateExisting(tx, ref, replayRef, session, &result, now, secret, user.AuthVersion)
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
	token, err := generateRefreshTokenAt(claims.Sub, role, secret, sessionID, now, claims.AuthVersion)
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
	*result = RefreshSessionRotation{AuthVersion: claims.AuthVersion, Token: token, UserID: claims.Sub, Role: role, ExpiresAt: doc.ExpiresAt}
	return nil
}

func (a *RefreshSessionAuthority) rotateExisting(tx *firestore.Transaction, ref, replayRef *firestore.DocumentRef, session refreshSessionDocument, result *RefreshSessionRotation, now time.Time, secret string, version int64) error {
	token, err := generateRefreshTokenAt(session.UserID, session.Role, secret, session.SessionID, now, version)
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
	*result = RefreshSessionRotation{AuthVersion: version, Token: token, UserID: session.UserID, Role: session.Role, ExpiresAt: expiresAt}
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
	var refs []refreshSessionCleanupCandidate
	for len(refs) < limit {
		snapshot, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return 0, err
		}
		var session refreshSessionDocument
		if snapshot.DataTo(&session) == nil && !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(now) {
			refs = append(refs, refreshSessionCleanupCandidate{ref: snapshot.Ref, updateTime: snapshot.UpdateTime})
		}
	}
	removed := 0
	if a.beforeCleanupDelete != nil && len(refs) > 0 {
		a.beforeCleanupDelete()
	}
	for start := 0; start < len(refs); start += 500 {
		end := start + 500
		if end > len(refs) {
			end = len(refs)
		}
		batch := a.fs.Batch()
		for _, candidate := range refs[start:end] {
			batch.Delete(candidate.ref, firestore.LastUpdateTime(candidate.updateTime))
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
			removed += end - start
		}
	}
	return removed, nil
}
