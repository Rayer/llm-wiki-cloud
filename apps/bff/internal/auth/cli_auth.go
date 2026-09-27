package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	cliPairingsCollection           = "auth_cli_pairings"
	cliPairingStatusPending         = "pending"
	cliPairingStatusApproved        = "approved"
	cliPairingStatusDenied          = "denied"
	cliPairingStatusExpired         = "expired"
	cliPairingStatusRedeemed        = "redeemed"
	cliPairingStatusLocked          = "locked"
	cliPairingLifetime              = 10 * time.Minute
	cliPairingPollIntervalSeconds   = 3
	cliPairingMaxPollSecretFailures = 5
	cliProjectDefaultPageSize       = 50
	cliProjectMaxPageSize           = 100
)

var (
	ErrCLIPairingInvalid     = errors.New("invalid CLI pairing")
	ErrCLIPairingExpired     = errors.New("CLI pairing expired")
	ErrCLIPairingDenied      = errors.New("CLI pairing denied")
	ErrCLIPairingRedeemed    = errors.New("CLI pairing already redeemed")
	ErrCLIPairingUnavailable = errors.New("CLI pairing authority unavailable")
	ErrCLIPairingDecision    = errors.New("invalid CLI pairing decision")
)

type cliPairingDocument struct {
	PairingID           string    `firestore:"pairing_id"`
	Environment         string    `firestore:"environment"`
	UserCodeHash        string    `firestore:"user_code_hash"`
	PollSecretHash      string    `firestore:"poll_secret_hash"`
	ClientName          string    `firestore:"client_name"`
	Status              string    `firestore:"status"`
	CreatedAt           time.Time `firestore:"created_at"`
	ExpiresAt           time.Time `firestore:"expires_at"`
	PollSecretFailures  int       `firestore:"poll_secret_failures"`
	ApprovedUserID      string    `firestore:"approved_user_id,omitempty"`
	ApprovedAuthVersion int64     `firestore:"approved_auth_version,omitempty"`
	ApprovedAt          time.Time `firestore:"approved_at,omitempty"`
	DecidedBy           string    `firestore:"decided_by,omitempty"`
	DecisionReason      string    `firestore:"decision_reason,omitempty"`
	RedeemedAt          time.Time `firestore:"redeemed_at,omitempty"`
}

// CLIStartPairingResponse gives the CLI one short user code and one private
// polling secret. Neither value is persisted in plaintext.
type CLIStartPairingResponse struct {
	PairingID           string    `json:"pairing_id"`
	UserCode            string    `json:"user_code"`
	VerificationURL     string    `json:"verification_url"`
	PollingSecret       string    `json:"polling_secret"`
	ExpiresAt           time.Time `json:"expires_at"`
	PollIntervalSeconds int       `json:"poll_interval_seconds"`
}

// CLIPairingPollResult is pending until a Web user approves the pairing; a
// successful redemption carries a single CLI session grant.
type CLIPairingPollResult struct {
	Status      string                `json:"status"`
	Credentials *CLISessionCredential `json:"credentials,omitempty"`
}

type cliPairingAuditEvent struct {
	Action    string    `firestore:"action"`
	ActorID   string    `firestore:"actor_id"`
	PairingID string    `firestore:"pairing_id"`
	Reason    string    `firestore:"reason"`
	Result    string    `firestore:"result"`
	CreatedAt time.Time `firestore:"created_at"`
}

// CLIAuthService coordinates short-lived pairing requests and the shared
// durable refresh-session authority. It does not own a second session store.
type CLIAuthService struct {
	fs                 *firestore.Client
	sessions           *RefreshSessionAuthority
	bindings           *SyncBindingAuthority
	jwtSecret          string
	verificationOrigin string
	now                func() time.Time
}

func NewCLIAuthService(fs *firestore.Client, sessions *RefreshSessionAuthority, jwtSecret, verificationOrigin string) *CLIAuthService {
	return &CLIAuthService{fs: fs, sessions: sessions, jwtSecret: strings.TrimSpace(jwtSecret), verificationOrigin: strings.TrimSpace(verificationOrigin), now: time.Now}
}

func (s *CLIAuthService) ready() bool {
	return s != nil && s.fs != nil && s.sessions != nil && s.sessions.ready() && s.jwtSecret != ""
}

// SetSyncBindingAuthority installs the project-scoped binding authority.
func (s *CLIAuthService) SetSyncBindingAuthority(authority *SyncBindingAuthority) {
	s.bindings = authority
}

func (s *CLIAuthService) pairingRef(pairingID string) *firestore.DocumentRef {
	return s.fs.Collection(cliPairingsCollection).Doc(refreshSessionDocumentID(s.sessions.environment, pairingID))
}

// StartPairing stores a short-lived request using only hashes of its code and
// polling secret. Callers must rate-limit this endpoint.
func (s *CLIAuthService) StartPairing(ctx context.Context, clientName string) (CLIStartPairingResponse, error) {
	if !s.ready() {
		return CLIStartPairingResponse{}, ErrCLIPairingUnavailable
	}
	origin, err := cliVerificationOrigin(s.verificationOrigin)
	if err != nil {
		return CLIStartPairingResponse{}, ErrCLIPairingUnavailable
	}
	for range 5 {
		code, err := randomCLIUserCode()
		if err != nil {
			return CLIStartPairingResponse{}, err
		}
		pollSecret, err := randomOpaqueSecret()
		if err != nil {
			return CLIStartPairingResponse{}, err
		}
		pairingID := s.hashUserCode(code)
		now := s.now().UTC()
		doc := cliPairingDocument{
			PairingID: pairingID, Environment: s.sessions.environment,
			UserCodeHash: pairingID, PollSecretHash: hashRefreshToken(pollSecret),
			ClientName: normalizeCLIClientName(clientName), Status: cliPairingStatusPending,
			CreatedAt: now, ExpiresAt: now.Add(cliPairingLifetime),
		}
		err = s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
			if err := tx.Create(s.pairingRef(pairingID), doc); err != nil {
				return err
			}
			return tx.Create(s.auditRef(), cliPairingAuditEvent{
				Action: "cli_pairing_started", PairingID: pairingID, Reason: "user_requested", Result: cliPairingStatusPending, CreatedAt: now,
			})
		})
		if status.Code(err) == codes.AlreadyExists {
			continue
		}
		if err != nil {
			return CLIStartPairingResponse{}, ErrCLIPairingUnavailable
		}
		verificationURL := *origin
		verificationURL.Path = "/cli-pairing"
		query := verificationURL.Query()
		query.Set("user_code", code)
		verificationURL.RawQuery = query.Encode()
		return CLIStartPairingResponse{
			PairingID: pairingID, UserCode: code, VerificationURL: verificationURL.String(),
			PollingSecret: pollSecret, ExpiresAt: doc.ExpiresAt,
			PollIntervalSeconds: cliPairingPollIntervalSeconds,
		}, nil
	}
	return CLIStartPairingResponse{}, ErrCLIPairingUnavailable
}

// DecidePairing records an authenticated Web user's explicit approval or
// denial. It never accepts an unauthenticated CLI grant.
func (s *CLIAuthService) DecidePairing(ctx context.Context, userCode, decision, actorID string) error {
	if !s.ready() {
		return ErrCLIPairingUnavailable
	}
	userCode = strings.ToUpper(strings.TrimSpace(userCode))
	decision = strings.ToLower(strings.TrimSpace(decision))
	actorID = strings.TrimSpace(actorID)
	if !validCLIUserCode(userCode) || !ValidPathSegment(actorID) || (decision != "approve" && decision != "deny") {
		return ErrCLIPairingDecision
	}
	pairingID := s.hashUserCode(userCode)
	var terminalErr error
	err := s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		terminalErr = nil
		snapshot, err := tx.Get(s.pairingRef(pairingID))
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrCLIPairingInvalid
			}
			return ErrCLIPairingUnavailable
		}
		var pair cliPairingDocument
		if err := snapshot.DataTo(&pair); err != nil || pair.Environment != s.sessions.environment || pair.UserCodeHash != pairingID {
			return ErrCLIPairingInvalid
		}
		now := s.now().UTC()
		if !now.Before(pair.ExpiresAt) {
			if pair.Status == cliPairingStatusPending || pair.Status == cliPairingStatusApproved {
				if err := tx.Update(snapshot.Ref, []firestore.Update{{Path: "status", Value: cliPairingStatusExpired}}); err != nil {
					return err
				}
			}
			terminalErr = ErrCLIPairingExpired
			return nil
		}
		if pair.Status != cliPairingStatusPending {
			switch pair.Status {
			case cliPairingStatusDenied:
				return ErrCLIPairingDenied
			case cliPairingStatusExpired:
				return ErrCLIPairingExpired
			default:
				return ErrCLIPairingInvalid
			}
		}
		actorSnapshot, err := tx.Get(s.fs.Collection("users").Doc(actorID))
		if err != nil {
			return ErrCLISessionUnavailable
		}
		var actor UserRecord
		if err := actorSnapshot.DataTo(&actor); err != nil {
			return ErrCLISessionUnavailable
		}
		if !actor.Active() {
			return ErrCLISessionRevoked
		}
		updates := []firestore.Update{
			{Path: "status", Value: cliPairingStatusDenied},
			{Path: "decided_by", Value: actorID},
			{Path: "decision_reason", Value: "user_denied"},
		}
		result := cliPairingStatusDenied
		if decision == "approve" {
			updates[0].Value = cliPairingStatusApproved
			updates[2].Value = "user_approved"
			updates = append(updates,
				firestore.Update{Path: "approved_user_id", Value: actorID},
				firestore.Update{Path: "approved_auth_version", Value: actor.AuthVersion},
				firestore.Update{Path: "approved_at", Value: now},
			)
			result = cliPairingStatusApproved
		}
		if err := tx.Update(snapshot.Ref, updates); err != nil {
			return err
		}
		reason := "user_denied"
		if decision == "approve" {
			reason = "user_approved"
		}
		return tx.Create(s.auditRef(), cliPairingAuditEvent{Action: "cli_pairing_decision", ActorID: actorID, PairingID: pairingID, Reason: reason, Result: result, CreatedAt: now})
	})
	if err != nil {
		return err
	}
	return terminalErr
}

// PollPairing redeems an approved request once and creates the CLI session in
// the same Firestore transaction that marks the pairing consumed.
func (s *CLIAuthService) PollPairing(ctx context.Context, pairingID, pollSecret string) (CLIPairingPollResult, error) {
	if !s.ready() || !validPairingID(pairingID) || strings.TrimSpace(pollSecret) == "" {
		return CLIPairingPollResult{}, ErrCLIPairingInvalid
	}
	sessionID, err := randomTokenID()
	if err != nil {
		return CLIPairingPollResult{}, err
	}
	refreshToken, err := newCLIRefreshToken(sessionID)
	if err != nil {
		return CLIPairingPollResult{}, err
	}
	now := s.now().UTC()
	var result CLIPairingPollResult
	var terminalErr error
	err = s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		terminalErr = nil
		snapshot, err := tx.Get(s.pairingRef(pairingID))
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrCLIPairingInvalid
			}
			return ErrCLIPairingUnavailable
		}
		var pair cliPairingDocument
		if err := snapshot.DataTo(&pair); err != nil || pair.Environment != s.sessions.environment || pair.PairingID != pairingID {
			return ErrCLIPairingInvalid
		}
		if pair.Status == cliPairingStatusLocked {
			return ErrCLIPairingInvalid
		}
		if !hmac.Equal([]byte(pair.PollSecretHash), []byte(hashRefreshToken(pollSecret))) {
			if pair.PollSecretFailures < cliPairingMaxPollSecretFailures {
				pair.PollSecretFailures++
				updates := []firestore.Update{{Path: "poll_secret_failures", Value: pair.PollSecretFailures}}
				if pair.PollSecretFailures >= cliPairingMaxPollSecretFailures {
					updates = append(updates, firestore.Update{Path: "status", Value: cliPairingStatusLocked})
				}
				if err := tx.Update(snapshot.Ref, updates); err != nil {
					return err
				}
			}
			terminalErr = ErrCLIPairingInvalid
			return nil
		}
		if !now.Before(pair.ExpiresAt) {
			if pair.Status == cliPairingStatusPending || pair.Status == cliPairingStatusApproved {
				if err := tx.Update(snapshot.Ref, []firestore.Update{{Path: "status", Value: cliPairingStatusExpired}}); err != nil {
					return err
				}
			}
			terminalErr = ErrCLIPairingExpired
			return nil
		}
		switch pair.Status {
		case cliPairingStatusPending:
			result = CLIPairingPollResult{Status: cliPairingStatusPending}
			return nil
		case cliPairingStatusDenied:
			return ErrCLIPairingDenied
		case cliPairingStatusExpired:
			return ErrCLIPairingExpired
		case cliPairingStatusRedeemed:
			return ErrCLIPairingRedeemed
		case cliPairingStatusApproved:
		default:
			return ErrCLIPairingInvalid
		}
		accountSnapshot, err := tx.Get(s.fs.Collection("users").Doc(pair.ApprovedUserID))
		if err != nil {
			return ErrCLISessionUnavailable
		}
		var user UserRecord
		if err := accountSnapshot.DataTo(&user); err != nil {
			return ErrCLISessionUnavailable
		}
		if !user.AllowsVersion(pair.ApprovedAuthVersion) {
			if err := tx.Update(snapshot.Ref, []firestore.Update{{Path: "status", Value: cliPairingStatusDenied}, {Path: "decision_reason", Value: "account_changed"}}); err != nil {
				return err
			}
			terminalErr = ErrCLIPairingDenied
			return nil
		}
		credential, err := s.sessions.createCLISessionInTransaction(tx, pair.ApprovedUserID, &user, pair.ClientName, s.jwtSecret, sessionID, refreshToken, now)
		if err != nil {
			return err
		}
		if err := tx.Update(snapshot.Ref, []firestore.Update{{Path: "status", Value: cliPairingStatusRedeemed}, {Path: "redeemed_at", Value: now}}); err != nil {
			return err
		}
		if err := tx.Create(s.auditRef(), cliPairingAuditEvent{Action: "cli_pairing_redeemed", ActorID: pair.ApprovedUserID, PairingID: pairingID, Reason: "approved_cli_grant", Result: "issued", CreatedAt: now}); err != nil {
			return err
		}
		result = CLIPairingPollResult{Status: cliPairingStatusRedeemed, Credentials: &credential}
		return nil
	})
	if err != nil {
		return CLIPairingPollResult{}, err
	}
	if terminalErr != nil {
		return CLIPairingPollResult{}, terminalErr
	}
	return result, nil
}

func (s *CLIAuthService) auditRef() *firestore.DocumentRef {
	id, err := randomTokenID()
	if err != nil {
		id = fmt.Sprintf("audit-%d", s.now().UnixNano())
	}
	return s.fs.Collection("auth_audit_events").Doc(refreshSessionDocumentID(s.sessions.environment, id))
}

func (s *CLIAuthService) hashUserCode(code string) string {
	mac := hmac.New(sha256.New, []byte(s.jwtSecret))
	_, _ = mac.Write([]byte("lwc-cli-pairing-code\x00" + strings.ToUpper(code)))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomCLIUserCode() (string, error) {
	const alphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"
	code := make([]byte, 8)
	read := make([]byte, 16)
	for i := range code {
		for {
			if _, err := rand.Read(read); err != nil {
				return "", err
			}
			accepted := false
			for _, value := range read {
				if value >= 240 {
					continue
				}
				code[i] = alphabet[int(value)%len(alphabet)]
				accepted = true
				break
			}
			if accepted {
				break
			}
		}
	}
	return string(code), nil
}

func validCLIUserCode(code string) bool {
	if len(code) != 8 {
		return false
	}
	for _, value := range code {
		if !strings.ContainsRune("23456789ABCDEFGHJKMNPQRSTVWXYZ", value) {
			return false
		}
	}
	return true
}

func validPairingID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cliVerificationOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("invalid CLI verification origin")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if scheme != "https" && scheme != "http" {
		return nil, fmt.Errorf("invalid CLI verification origin")
	}
	if scheme == "http" {
		hostname := strings.ToLower(u.Hostname())
		ip := net.ParseIP(hostname)
		if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("invalid CLI verification origin")
		}
	}
	return &url.URL{Scheme: scheme, Host: host}, nil
}

// StartPairingHandler creates a short-lived pairing request. Router wiring
// must attach an IP rate limiter to this handler.
func (s *CLIAuthService) StartPairingHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			ClientName string `json:"client_name"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pairing request"})
			return
		}
		result, err := s.StartPairing(c.Request.Context(), request.ClientName)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pairing unavailable"})
			return
		}
		c.JSON(http.StatusCreated, result)
	}
}

func (s *CLIAuthService) PollPairingHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			PairingID     string `json:"pairing_id"`
			PollingSecret string `json:"polling_secret"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pairing request"})
			return
		}
		result, err := s.PollPairing(c.Request.Context(), request.PairingID, request.PollingSecret)
		if err != nil {
			statusCode := http.StatusUnauthorized
			message := "pairing request invalid"
			switch {
			case errors.Is(err, ErrCLIPairingExpired):
				statusCode, message = http.StatusGone, "pairing request expired"
			case errors.Is(err, ErrCLIPairingDenied):
				statusCode, message = http.StatusForbidden, "pairing request denied"
			case errors.Is(err, ErrCLIPairingRedeemed):
				statusCode, message = http.StatusConflict, "pairing request already redeemed"
			case errors.Is(err, ErrCLIPairingUnavailable), errors.Is(err, ErrCLISessionUnavailable):
				statusCode, message = http.StatusServiceUnavailable, "pairing unavailable"
			}
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		statusCode := http.StatusOK
		if result.Status == cliPairingStatusPending {
			statusCode = http.StatusAccepted
		}
		c.JSON(statusCode, result)
	}
}

func (s *CLIAuthService) DecidePairingHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			UserCode string `json:"user_code"`
			Decision string `json:"decision"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pairing decision"})
			return
		}
		if err := s.DecidePairing(c.Request.Context(), request.UserCode, request.Decision, c.GetString("userID")); err != nil {
			statusCode, message := cliPairingDecisionHTTPStatus(err)
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		status := "denied"
		if strings.EqualFold(strings.TrimSpace(request.Decision), "approve") {
			status = "approved"
		}
		c.JSON(http.StatusOK, gin.H{"status": status})
	}
}

func cliPairingDecisionHTTPStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrCLIPairingInvalid), errors.Is(err, ErrCLIPairingDecision):
		return http.StatusNotFound, "pairing request not found"
	case errors.Is(err, ErrCLIPairingDenied):
		return http.StatusConflict, "pairing request already denied"
	case errors.Is(err, ErrCLIPairingExpired):
		return http.StatusGone, "pairing request expired"
	case errors.Is(err, ErrCLISessionRevoked):
		return http.StatusUnauthorized, "account is not active"
	case errors.Is(err, ErrCLISessionUnavailable), errors.Is(err, ErrCLIPairingUnavailable):
		return http.StatusServiceUnavailable, "pairing unavailable"
	default:
		return http.StatusServiceUnavailable, "pairing unavailable"
	}
}

func (s *CLIAuthService) ListSessionsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := s.sessions.ListCLISessions(c.Request.Context(), c.GetString("userID"))
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CLI sessions unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"sessions": items})
	}
}

func (s *CLIAuthService) RevokeSessionHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := s.sessions.RevokeCLISession(c.Request.Context(), c.GetString("userID"), c.Param("id")); err != nil {
			if errors.Is(err, ErrCLISessionNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "CLI session not found"})
				return
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CLI session unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func (s *CLIAuthService) RefreshHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.RefreshToken) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "refresh token is required"})
			return
		}
		credential, err := s.sessions.RotateCLISession(c.Request.Context(), request.RefreshToken, s.jwtSecret)
		if err != nil {
			if errors.Is(err, ErrCLISessionUnavailable) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CLI session unavailable"})
				return
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token; log in again"})
			return
		}
		c.JSON(http.StatusOK, credential)
	}
}

func (s *CLIAuthService) LogoutHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.RefreshToken) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "refresh token is required"})
			return
		}
		if err := s.sessions.RevokeCLISessionWithRefreshToken(c.Request.Context(), request.RefreshToken); err != nil {
			if errors.Is(err, ErrCLISessionUnavailable) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CLI session unavailable"})
				return
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "CLI session could not be revoked"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func (s *CLIAuthService) StatusHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"user_id": c.GetString("userID"), "session_id": c.GetString("sessionID"), "role": c.GetString("userRole")})
	}
}

func (s *CLIAuthService) ListProjectsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		pageSize := cliProjectDefaultPageSize
		if raw := strings.TrimSpace(c.Query("page_size")); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > cliProjectMaxPageSize {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page_size"})
				return
			}
			pageSize = value
		}
		projects, next, err := ListOwnedProjectsPage(c.Request.Context(), s.fs, c.GetString("userID"), pageSize, c.Query("page_token"))
		if err != nil {
			if errors.Is(err, ErrProjectPageTokenInvalid) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page_token"})
				return
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "projects unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"projects": projects, "next_page_token": next})
	}
}

func (s *CLIAuthService) ListBindingsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.bindings == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sync bindings unavailable"})
			return
		}
		items, err := s.bindings.ListBindings(c.Request.Context(), c.GetString("userID"))
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sync bindings unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"bindings": items})
	}
}

func (s *CLIAuthService) CreateBindingHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			ProjectID string `json:"project_id"`
			WikiID    string `json:"wiki_id"`
			Host      string `json:"host"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sync binding request"})
			return
		}
		if s.bindings == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sync bindings unavailable"})
			return
		}
		binding, err := s.bindings.CreateBinding(c.Request.Context(), c.GetString("userID"), request.ProjectID, request.WikiID, request.Host)
		if err != nil {
			writeSyncBindingError(c, err)
			return
		}
		c.JSON(http.StatusCreated, binding)
	}
}

func (s *CLIAuthService) ReauthorizeBindingHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			BindingID string `json:"binding_id"`
			WikiID    string `json:"wiki_id"`
			Host      string `json:"host"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sync binding request"})
			return
		}
		if s.bindings == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sync bindings unavailable"})
			return
		}
		binding, err := s.bindings.ReauthorizeBinding(c.Request.Context(), c.GetString("userID"), c.Param("projectID"), request.BindingID, request.WikiID, request.Host)
		if err != nil {
			writeSyncBindingError(c, err)
			return
		}
		c.JSON(http.StatusOK, binding)
	}
}

func (s *CLIAuthService) RevokeBindingHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.bindings == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sync bindings unavailable"})
			return
		}
		if err := s.bindings.RevokeBinding(c.Request.Context(), c.GetString("userID"), c.Param("projectID"), c.Param("bindingID"), "user_revoked"); err != nil {
			writeSyncBindingError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func writeSyncBindingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrProjectPermissionDenied), errors.Is(err, ErrSyncBindingUnauthorized):
		c.JSON(http.StatusForbidden, gin.H{"error": "project access denied"})
	case errors.Is(err, ErrSyncBindingInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sync binding"})
	case errors.Is(err, ErrSyncBindingAlreadyBound), errors.Is(err, ErrSyncBindingReauthorizationRequired):
		c.JSON(http.StatusConflict, gin.H{"error": "project already has a sync binding; explicitly reauthorize it"})
	case errors.Is(err, ErrSyncBindingNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "sync binding not found"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sync binding authority unavailable"})
	}
}
