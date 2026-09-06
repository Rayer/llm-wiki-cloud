package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	appconfig "github.com/rayer/llm-wiki-bff/internal/config"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/api/iterator"
)

const (
	OAuthFlowLogin OAuthFlowKind = "login"
	OAuthFlowLink  OAuthFlowKind = "link"

	googleProvider = "google"

	oauthTransactionsCollection = "oauth_transactions"
	oauthBrowsersCollection     = "oauth_browsers"
	oauthPendingLinksCollection = "oauth_pending_links"
	oauthCompletionsCollection  = "oauth_completions"
	oauthCookieName             = "__Host-lwc_oauth"
	oauthPendingCookieName      = "__Host-lwc_oauth_pending"
	oauthCompletionCookieName   = "__Host-lwc_oauth_completion"
	oauthTransactionTTL         = 10 * time.Minute
	oauthClockSkew              = 120 * time.Second
	oauthHTTPTimeout            = 5 * time.Second
	maxJWKSBytes                = 1 << 20
	maxJWKSKeys                 = 32
	maxRSAModulusBytes          = 512
	maxOAuthBodyBytes           = 1 << 20
	maxOAuthCodeBytes           = 2048
	maxOIDCTokenBytes           = 64 << 10
	maxProviderSubjectBytes     = 256
	maxProviderEmailBytes       = 320

	oauthStatusActive              = "active"
	oauthStatusConsumed            = "consumed"
	oauthStatusSuperseded          = "superseded"
	oauthStatusPendingConfirmation = "pending_confirmation"
	oauthStatusConfirmed           = "confirmed"
	oauthStatusCancelled           = "cancelled"
	oauthStatusExpired             = "expired"
	OAuthConfirmationRequired      = "confirmation_required"
	oauthCompletionStateActive     = "active"
	oauthCompletionStateConsumed   = "consumed"
	oauthCompletionTTL             = 5 * time.Minute
)

// OAuthFlowKind identifies the server-owned purpose of a transaction.
type OAuthFlowKind string

const OAuthFlowUnknown OAuthFlowKind = "unknown"

// GoogleConfig is the complete runtime contract for Google OIDC. AuthorizationURL
// is optional and defaults to Google's standard authorization endpoint.
type GoogleConfig struct {
	ClientID         string
	ClientSecret     string
	Issuer           string
	JWKSURL          string
	TokenURL         string
	AuthorizationURL string
	AuthServiceURL   string
	LoginRedirectURL string
	LinkRedirectURL  string
	CompletionURL    string
	AllowedOrigins   []string
}

// Validate verifies that Google OIDC is either fully and explicitly configured
// or rejected before any provider I/O can occur.
func (cfg GoogleConfig) Validate() error {
	var err error
	if strings.TrimSpace(cfg.AuthServiceURL) != "" || len(cfg.AllowedOrigins) > 0 {
		err = appconfig.ValidateGoogleConfig(cfg.ClientID, cfg.ClientSecret, cfg.Issuer, cfg.JWKSURL, cfg.TokenURL, cfg.LoginRedirectURL, cfg.LinkRedirectURL, cfg.CompletionURL, appconfig.GoogleRuntimeValidation{AuthServiceURL: cfg.AuthServiceURL, AllowedOrigins: cfg.AllowedOrigins})
	} else {
		err = appconfig.ValidateGoogleConfig(cfg.ClientID, cfg.ClientSecret, cfg.Issuer, cfg.JWKSURL, cfg.TokenURL, cfg.LoginRedirectURL, cfg.LinkRedirectURL, cfg.CompletionURL)
	}
	if err != nil {
		return err
	}
	if endpoint := strings.TrimSpace(cfg.AuthorizationURL); endpoint != "" {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return errors.New("authorization_url must be an HTTPS URL without userinfo, query, or fragment")
		}
	}
	return nil
}

type oauthTransaction struct {
	StateHash         string    `firestore:"state_hash"`
	BrowserHash       string    `firestore:"browser_hash"`
	FlowKind          string    `firestore:"flow_kind"`
	Nonce             string    `firestore:"nonce"`
	PKCEVerifier      string    `firestore:"pkce_verifier"`
	RedirectURL       string    `firestore:"redirect_url"`
	UserID            string    `firestore:"user_id,omitempty"`
	PasswordProofHash string    `firestore:"password_proof_hash,omitempty"`
	Status            string    `firestore:"status"`
	CreatedAt         time.Time `firestore:"created_at"`
	ExpiresAt         time.Time `firestore:"expires_at"`
}

type oauthBrowserLock struct {
	TransactionID string    `firestore:"transaction_id"`
	UpdatedAt     time.Time `firestore:"updated_at"`
}

type oauthPendingLink struct {
	UserID            string    `firestore:"user_id"`
	Provider          string    `firestore:"provider"`
	Issuer            string    `firestore:"issuer"`
	Subject           string    `firestore:"subject"`
	ProviderEmail     string    `firestore:"provider_email,omitempty"`
	ProviderVerified  bool      `firestore:"provider_email_verified"`
	CurrentEmail      string    `firestore:"current_email,omitempty"`
	PasswordProofHash string    `firestore:"password_proof_hash"`
	Status            string    `firestore:"status"`
	CreatedAt         time.Time `firestore:"created_at"`
	ExpiresAt         time.Time `firestore:"expires_at"`
}

// OAuthStartRequest is accepted by the generic start endpoint. Dedicated
// login/link routes bind the flow kind from the route and ignore this field.
type OAuthStartRequest struct {
	FlowKind        OAuthFlowKind `json:"flow_kind,omitempty"`
	CurrentPassword string        `json:"current_password,omitempty"`
	Password        string        `json:"password,omitempty"`
}

type oauthStartResponse struct {
	AuthorizationURL string `json:"authorization_url"`
}

// OAuthConfirmationRequest is the only client input accepted for explicit
// account linking completion. Provider identity values are never client-owned.
type OAuthConfirmationRequest struct {
	ConfirmationID string `json:"confirmation_id" binding:"required"`
}

type oauthCompletionResponse struct {
	Status                string `json:"status"`
	ConfirmationID        string `json:"confirmation_id,omitempty"`
	Provider              string `json:"provider,omitempty"`
	ProviderEmail         string `json:"provider_email,omitempty"`
	ProviderEmailVerified bool   `json:"provider_email_verified,omitempty"`
	CurrentEmail          string `json:"current_email,omitempty"`
	Error                 string `json:"error,omitempty"`
	SupportRef            string `json:"support_ref,omitempty"`
}

type oauthCompletionResult struct {
	BrowserHash    string    `firestore:"browser_hash"`
	FlowKind       string    `firestore:"flow_kind"`
	Status         string    `firestore:"status"`
	State          string    `firestore:"state"`
	Outcome        string    `firestore:"outcome"`
	SupportRef     string    `firestore:"support_ref,omitempty"`
	JITProvisioned bool      `firestore:"jit_provisioned"`
	CreatedAt      time.Time `firestore:"created_at"`
	ExpiresAt      time.Time `firestore:"expires_at"`
}

type oauthCompletionResultResponse struct {
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	SupportRef     string `json:"support_ref,omitempty"`
	JITProvisioned bool   `json:"jit_provisioned"`
}

type ProviderIdentitySummary struct {
	Provider              string `json:"provider"`
	ProviderEmail         string `json:"provider_email,omitempty"`
	ProviderEmailVerified bool   `json:"provider_email_verified"`
}

type IdentitySummaryResponse struct {
	PrimaryEmail    string                    `json:"primary_email"`
	LinkedProviders []ProviderIdentitySummary `json:"linked_providers"`
}

type oauthFailureResponse struct {
	Error      string `json:"error"`
	SupportRef string `json:"support_ref"`
}

type oauthOutcome string

const (
	oauthOutcomeInvalidRequest       oauthOutcome = "invalid_request"
	oauthOutcomeCrossSiteStart       oauthOutcome = "cross_site_start"
	oauthOutcomeUnavailable          oauthOutcome = "unavailable"
	oauthOutcomeExpired              oauthOutcome = "expired"
	oauthOutcomeReplay               oauthOutcome = "replay"
	oauthOutcomeWrongBrowser         oauthOutcome = "wrong_browser"
	oauthOutcomeWrongKind            oauthOutcome = "wrong_kind"
	oauthOutcomeProvider             oauthOutcome = "provider_failure"
	oauthOutcomeTokenInvalid         oauthOutcome = "token_invalid"
	oauthOutcomeRegistrationDisabled oauthOutcome = "registration_disabled"
	oauthOutcomeCanonicalConflict    oauthOutcome = "canonical_conflict"
	oauthOutcomeLinkConflict         oauthOutcome = "link_conflict"
	oauthOutcomeCancelled            oauthOutcome = "cancelled"
	oauthOutcomePasswordProof        oauthOutcome = "password_proof_failed"
)

// writeOAuthFailure writes only fixed copy and an opaque support reference.
// It deliberately accepts an http.ResponseWriter so tests can assert the
// privacy boundary without constructing a Gin context.
func writeOAuthFailure(w http.ResponseWriter, code int, outcome oauthOutcome, flowKinds ...OAuthFlowKind) {
	flowKind := OAuthFlowUnknown
	if len(flowKinds) > 0 {
		flowKind = boundedFlowKind(flowKinds[0])
	}
	ref := opaqueSupportReference()
	message := oauthOutcomeMessage(outcome)
	if message == "" {
		message = "Unable to continue with Google sign-in."
	}
	log.Printf(`{"event":"auth_oauth","provider":"google","flow_kind":%q,"outcome":%q,"support_ref":%q,"timestamp":%q}`, flowKind, outcome, ref, time.Now().UTC().Format(time.RFC3339))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(oauthFailureResponse{Error: message, SupportRef: ref})
}

func oauthOutcomeMessage(outcome oauthOutcome) string {
	return map[oauthOutcome]string{
		oauthOutcomeInvalidRequest:       "Unable to continue with Google sign-in.",
		oauthOutcomeCrossSiteStart:       "Unable to continue with Google sign-in.",
		oauthOutcomeUnavailable:          "Unable to continue with Google sign-in.",
		oauthOutcomeExpired:              "This Google sign-in has expired. Start again.",
		oauthOutcomeReplay:               "Unable to continue with Google sign-in.",
		oauthOutcomeWrongBrowser:         "Unable to continue with Google sign-in.",
		oauthOutcomeWrongKind:            "Unable to continue with Google sign-in.",
		oauthOutcomeProvider:             "Unable to continue with Google sign-in.",
		oauthOutcomeTokenInvalid:         "Unable to continue with Google sign-in.",
		oauthOutcomeRegistrationDisabled: "Registration is currently unavailable.",
		oauthOutcomeCanonicalConflict:    "Unable to continue with Google sign-in.",
		oauthOutcomeLinkConflict:         "Unable to link this Google account.",
		oauthOutcomeCancelled:            "已取消使用 Google 登入。",
		oauthOutcomePasswordProof:        "Unable to link this Google account.",
	}[outcome]
}

func boundedFlowKind(kind OAuthFlowKind) OAuthFlowKind {
	if kind == OAuthFlowLogin || kind == OAuthFlowLink {
		return kind
	}
	return OAuthFlowUnknown
}

func opaqueSupportReference() string {
	value, err := randomOpaqueValue(16)
	if err != nil {
		return "unavailable"
	}
	return value
}

// GoogleOAuthService owns the durable transaction and provider boundaries.
type GoogleOAuthService struct {
	cfg       GoogleConfig
	fs        *firestore.Client
	repo      *IdentityRepository
	gate      RegistrationGate
	jwtSecret string
	client    *http.Client
	verifier  *googleOIDCVerifier
	now       func() time.Time
}

// NewGoogleOAuthService constructs a bounded provider client. It does not
// contact Google until a callback is exchanged.
func NewGoogleOAuthService(cfg GoogleConfig, fs *firestore.Client, repo *IdentityRepository, gate RegistrationGate, jwtSecret string) *GoogleOAuthService {
	client := boundedOAuthHTTPClient()
	return &GoogleOAuthService{
		cfg: cfg, fs: fs, repo: repo, gate: gate, jwtSecret: jwtSecret,
		client: client, verifier: newGoogleOIDCVerifier(cfg, client), now: time.Now,
	}
}

// StartHandler creates a server-owned authorization transaction and redirects
// to the fixed provider authorization endpoint. GET login is intentionally a
// browser-navigation contract; link preparation uses PrepareStartHandler so a
// bearer header and current-password proof stay in the JSON request.
func (s *GoogleOAuthService) StartHandler(routeKind OAuthFlowKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		authorizationURL, code, outcome, kind := s.prepareAuthorization(c, routeKind)
		if code != 0 {
			writeOAuthFailure(c.Writer, code, outcome, kind)
			return
		}
		c.Redirect(http.StatusFound, authorizationURL)
	}
}

// PrepareStartHandler returns only a server-generated provider URL. It is the
// browser-compatible authenticated link-start contract: the password and
// bearer authorization are sent in the JSON request, never in navigation URL
// or browser storage.
func (s *GoogleOAuthService) PrepareStartHandler(routeKind OAuthFlowKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		authorizationURL, code, outcome, kind := s.prepareAuthorization(c, routeKind)
		if code != 0 {
			writeOAuthFailure(c.Writer, code, outcome, kind)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, oauthStartResponse{AuthorizationURL: authorizationURL})
	}
}

func (s *GoogleOAuthService) prepareAuthorization(c *gin.Context, routeKind OAuthFlowKind) (string, int, oauthOutcome, OAuthFlowKind) {
	if s == nil || !s.sameSiteOrigin(c.Request) {
		return "", http.StatusForbidden, oauthOutcomeCrossSiteStart, OAuthFlowUnknown
	}
	if !s.configReady() || s.fs == nil || s.repo == nil {
		return "", http.StatusServiceUnavailable, oauthOutcomeUnavailable, boundedFlowKind(routeKind)
	}
	kind := boundedFlowKind(routeKind)
	var req OAuthStartRequest
	if c.Request.Method != http.MethodGet || c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			return "", http.StatusBadRequest, oauthOutcomeInvalidRequest, OAuthFlowUnknown
		}
	}
	if kind == OAuthFlowUnknown {
		kind = req.FlowKind
		if kind == "" && c.Request.Method == http.MethodGet {
			kind = OAuthFlowLogin
		}
	}
	if kind != OAuthFlowLogin && kind != OAuthFlowLink {
		return "", http.StatusBadRequest, oauthOutcomeInvalidRequest, OAuthFlowUnknown
	}

	userID := ""
	proofHash := ""
	if kind == OAuthFlowLink {
		userID = strings.TrimSpace(c.GetString("userID"))
		currentPassword := req.CurrentPassword
		if currentPassword == "" {
			currentPassword = req.Password
		}
		if !ValidPathSegment(userID) || currentPassword == "" {
			return "", http.StatusUnauthorized, oauthOutcomePasswordProof, kind
		}
		user, err := GetUser(c.Request.Context(), s.fs, userID)
		if err != nil || user == nil || user.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)) != nil {
			return "", http.StatusUnauthorized, oauthOutcomePasswordProof, kind
		}
		proofHash = passwordProofHash(user.PasswordHash)
	}

	browserCookie, err := c.Request.Cookie(oauthCookieName)
	browserValue := ""
	if err == nil {
		browserValue = strings.TrimSpace(browserCookie.Value)
	}
	if browserValue == "" {
		browserValue, err = randomOpaqueValue(32)
		if err != nil {
			return "", http.StatusInternalServerError, oauthOutcomeUnavailable, kind
		}
	}
	state, err := randomOpaqueValue(32)
	if err != nil {
		return "", http.StatusInternalServerError, oauthOutcomeUnavailable, kind
	}
	nonce, err := randomOpaqueValue(32)
	if err != nil {
		return "", http.StatusInternalServerError, oauthOutcomeUnavailable, kind
	}
	verifier, err := randomOpaqueValue(32)
	if err != nil {
		return "", http.StatusInternalServerError, oauthOutcomeUnavailable, kind
	}
	transactionID, err := randomOpaqueValue(24)
	if err != nil {
		return "", http.StatusInternalServerError, oauthOutcomeUnavailable, kind
	}
	authorizationURL, err := s.authorizationURL(kind, state, nonce, pkceChallenge(verifier))
	if err != nil {
		return "", http.StatusInternalServerError, oauthOutcomeUnavailable, kind
	}
	created := s.now().UTC()
	transaction := oauthTransaction{
		StateHash: hashOAuthValue(state), BrowserHash: hashOAuthValue(browserValue), FlowKind: string(kind),
		Nonce: nonce, PKCEVerifier: verifier, RedirectURL: s.redirectURL(kind), UserID: userID,
		PasswordProofHash: proofHash, Status: oauthStatusActive, CreatedAt: created, ExpiresAt: created.Add(oauthTransactionTTL),
	}
	if err := s.createTransaction(c.Request.Context(), transactionID, browserValue, transaction); err != nil {
		return "", http.StatusServiceUnavailable, oauthOutcomeUnavailable, kind
	}
	setOAuthCookie(c, browserValue, int(oauthTransactionTTL.Seconds()))
	return authorizationURL, 0, "", kind
}

// CallbackHandler consumes the transaction before any provider exchange.
func (s *GoogleOAuthService) CallbackHandler(routeKind OAuthFlowKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		kind := boundedFlowKind(routeKind)
		if kind == OAuthFlowUnknown {
			kind = OAuthFlowLogin
		}
		if s == nil || !s.configReady() || s.fs == nil || s.repo == nil || s.verifier == nil {
			writeOAuthFailure(c.Writer, http.StatusServiceUnavailable, oauthOutcomeUnavailable, kind)
			return
		}
		state := strings.TrimSpace(c.Query("state"))
		browserCookie, err := c.Request.Cookie(oauthCookieName)
		if err != nil || strings.TrimSpace(browserCookie.Value) == "" || state == "" || len(state) > maxOAuthCodeBytes {
			writeOAuthFailure(c.Writer, http.StatusBadRequest, oauthOutcomeInvalidRequest, kind)
			return
		}
		transactionID, transaction, err := s.consumeTransaction(c.Request.Context(), state, browserCookie.Value, kind)
		if err != nil {
			if transactionID != "" && subtle.ConstantTimeCompare([]byte(transaction.BrowserHash), []byte(hashOAuthValue(browserCookie.Value))) == 1 {
				s.redirectOAuthFailure(c, browserCookie.Value, kind, oauthConsumeOutcome(err))
				return
			}
			// No exact state+browser transaction means no safe completion binding;
			// fail closed without creating an attacker-controlled result.
			writeOAuthFailure(c.Writer, oauthConsumeStatus(err), oauthConsumeOutcome(err), kind)
			return
		}
		if providerError := strings.TrimSpace(c.Query("error")); providerError != "" {
			s.redirectOAuthFailure(c, browserCookie.Value, kind, oauthOutcomeCancelled)
			return
		}
		code := strings.TrimSpace(c.Query("code"))
		if code == "" || len(code) > maxOAuthCodeBytes {
			s.redirectOAuthFailure(c, browserCookie.Value, kind, oauthOutcomeInvalidRequest)
			return
		}
		idToken, err := s.exchangeCode(c.Request.Context(), transaction, code)
		if err != nil || len(idToken) > maxOIDCTokenBytes {
			s.redirectOAuthFailure(c, browserCookie.Value, kind, oauthOutcomeProvider)
			return
		}
		claims, err := s.verifier.Verify(c.Request.Context(), idToken, transaction.Nonce)
		if err != nil {
			s.redirectOAuthFailure(c, browserCookie.Value, kind, oauthOutcomeTokenInvalid)
			return
		}
		if kind == OAuthFlowLink {
			s.completeLinkCallback(c, browserCookie.Value, transactionID, transaction, claims)
			return
		}
		s.completeLoginCallback(c, browserCookie.Value, transaction, claims)
	}
}

// ConfirmLinkHandler performs the one mapping-writing link decision. It must
// be mounted behind JWTAuth; route intent, not client JSON, selects confirm.
func (s *GoogleOAuthService) ConfirmLinkHandler() gin.HandlerFunc {
	return s.linkDecisionHandler(true)
}

// CancelLinkHandler cancels a pending link without ever creating an identity.
func (s *GoogleOAuthService) CancelLinkHandler() gin.HandlerFunc {
	return s.linkDecisionHandler(false)
}

func (s *GoogleOAuthService) linkDecisionHandler(confirmed bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := strings.TrimSpace(c.GetString("userID"))
		if !ValidPathSegment(userID) || s == nil || s.fs == nil || s.repo == nil {
			writeOAuthFailure(c.Writer, http.StatusUnauthorized, oauthOutcomeInvalidRequest, OAuthFlowLink)
			return
		}
		var req OAuthConfirmationRequest
		if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.ConfirmationID) == "" {
			writeOAuthFailure(c.Writer, http.StatusBadRequest, oauthOutcomeInvalidRequest, OAuthFlowLink)
			return
		}
		var result oauthCompletionResponse
		var err error
		if confirmed {
			result, err = s.confirmLink(c.Request.Context(), userID, req.ConfirmationID)
		} else {
			result, err = s.cancelLink(c.Request.Context(), userID, req.ConfirmationID)
		}
		if err != nil {
			code, outcome := oauthLinkStatus(err)
			writeOAuthFailure(c.Writer, code, outcome, OAuthFlowLink)
			return
		}
		if !confirmed {
			result.Error = oauthOutcomeMessage(oauthOutcomeCancelled)
			result.SupportRef = opaqueSupportReference()
			log.Printf(`{"event":"auth_oauth","provider":"google","flow_kind":"link","outcome":%q,"support_ref":%q,"timestamp":%q}`, oauthOutcomeCancelled, result.SupportRef, time.Now().UTC().Format(time.RFC3339))
		}
		c.Header("Cache-Control", "no-store")
		clearOAuthPendingCookie(c)
		c.JSON(http.StatusOK, result)
	}
}

// CompletionReadHandler returns the pending-link display metadata associated
// with the opaque browser cookie. It is authenticated and strictly read-only.
func (s *GoogleOAuthService) CompletionReadHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := strings.TrimSpace(c.GetString("userID"))
		if s == nil || s.fs == nil || !ValidPathSegment(userID) {
			writeOAuthFailure(c.Writer, http.StatusUnauthorized, oauthOutcomeInvalidRequest, OAuthFlowLink)
			return
		}
		cookie, err := c.Request.Cookie(oauthPendingCookieName)
		if err != nil || !ValidPathSegment(cookie.Value) {
			writeOAuthFailure(c.Writer, http.StatusBadRequest, oauthOutcomeInvalidRequest, OAuthFlowLink)
			return
		}
		snapshot, err := s.fs.Collection(oauthPendingLinksCollection).Doc(cookie.Value).Get(c.Request.Context())
		if err != nil || !snapshot.Exists() {
			writeOAuthFailure(c.Writer, http.StatusBadRequest, oauthOutcomeInvalidRequest, OAuthFlowLink)
			return
		}
		var pending oauthPendingLink
		if err := snapshot.DataTo(&pending); err != nil || pending.UserID != userID {
			writeOAuthFailure(c.Writer, http.StatusBadRequest, oauthOutcomeInvalidRequest, OAuthFlowLink)
			return
		}
		if pending.Status != oauthStatusActive {
			writeOAuthFailure(c.Writer, http.StatusConflict, oauthOutcomeReplay, OAuthFlowLink)
			return
		}
		if !s.now().UTC().Before(pending.ExpiresAt) {
			writeOAuthFailure(c.Writer, http.StatusGone, oauthOutcomeExpired, OAuthFlowLink)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, oauthCompletionResponse{
			Status: OAuthConfirmationRequired, ConfirmationID: cookie.Value, Provider: pending.Provider,
			ProviderEmail: pending.ProviderEmail, ProviderEmailVerified: pending.ProviderVerified, CurrentEmail: pending.CurrentEmail,
		})
	}
}

// CompletionResultHandler consumes the short-lived, cookie-bound callback
// result. It is intentionally unauthenticated: provider cancellation and
// failed login have no LWC session to authenticate, while the opaque cookie and
// stored browser hash still bind the read to the initiating browser.
func (s *GoogleOAuthService) CompletionResultHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || s.fs == nil {
			writeOAuthFailure(c.Writer, http.StatusServiceUnavailable, oauthOutcomeUnavailable)
			return
		}
		cookie, err := c.Request.Cookie(oauthCompletionCookieName)
		if err != nil || !ValidPathSegment(cookie.Value) || len(cookie.Value) < 20 {
			writeOAuthFailure(c.Writer, http.StatusBadRequest, oauthOutcomeInvalidRequest)
			return
		}
		var result oauthCompletionResult
		var terminalErr error
		err = s.fs.RunTransaction(c.Request.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
			resultRef := s.fs.Collection(oauthCompletionsCollection).Doc(cookie.Value)
			snapshot, err := optionalTransactionGet(tx, resultRef)
			if err != nil {
				return err
			}
			if snapshot == nil {
				return errOAuthCompletionNotFound
			}
			if err := snapshot.DataTo(&result); err != nil || result.BrowserHash == "" {
				return errOAuthCompletionNotFound
			}
			browserCookie, err := c.Request.Cookie(oauthCookieName)
			if err != nil || subtle.ConstantTimeCompare([]byte(result.BrowserHash), []byte(hashOAuthValue(browserCookie.Value))) != 1 {
				return errOAuthCompletionWrongBrowser
			}
			if result.State != oauthCompletionStateActive {
				return errOAuthCompletionReplay
			}
			if !s.now().UTC().Before(result.ExpiresAt) {
				if err := tx.Update(resultRef, []firestore.Update{{Path: "state", Value: oauthCompletionStateConsumed}}); err != nil {
					return err
				}
				terminalErr = errOAuthCompletionExpired
				return nil
			}
			if err := tx.Update(resultRef, []firestore.Update{{Path: "state", Value: oauthCompletionStateConsumed}}); err != nil {
				return err
			}
			return nil
		})
		if err == nil && terminalErr != nil {
			err = terminalErr
		}
		clearOAuthCompletionCookie(c)
		clearOAuthCookie(c)
		if err != nil {
			writeOAuthFailure(c.Writer, oauthCompletionStatus(err), oauthCompletionOutcome(err))
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, oauthCompletionResultResponse{
			Status: result.Status, Error: resultErrorMessage(result), SupportRef: result.SupportRef,
			JITProvisioned: result.JITProvisioned,
		})
	}
}

// CompletionHandler is the fixed callback-result read surface used by the
// production router. Link display metadata remains behind JWTAuth at
// CompletionReadHandler.
func (s *GoogleOAuthService) CompletionHandler() gin.HandlerFunc {
	return s.CompletionResultHandler()
}

// IdentitySummaryHandler is the authenticated Account Settings read. It
// projects only primary email and provider display metadata; issuer, subject,
// OAuth tokens, and other persistence fields never cross this boundary.
func (s *GoogleOAuthService) IdentitySummaryHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := strings.TrimSpace(c.GetString("userID"))
		if s == nil || s.fs == nil || !ValidPathSegment(userID) {
			writeOAuthFailure(c.Writer, http.StatusUnauthorized, oauthOutcomeInvalidRequest, OAuthFlowUnknown)
			return
		}
		user, err := GetUser(c.Request.Context(), s.fs, userID)
		if err != nil || user == nil {
			writeOAuthFailure(c.Writer, http.StatusInternalServerError, oauthOutcomeUnavailable, OAuthFlowUnknown)
			return
		}
		c.Header("Cache-Control", "no-store")
		providers := make([]ProviderIdentitySummary, 0, 1)
		iter := s.fs.Collection(ExternalIdentitiesCollection).Where("user_id", "==", userID).Documents(c.Request.Context())
		defer iter.Stop()
		for {
			snapshot, err := iter.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				writeOAuthFailure(c.Writer, http.StatusInternalServerError, oauthOutcomeUnavailable, OAuthFlowUnknown)
				return
			}
			identity, err := decodeExternalIdentity(snapshot)
			if err != nil || identity == nil || identity.UserID != userID {
				writeOAuthFailure(c.Writer, http.StatusInternalServerError, oauthOutcomeUnavailable, OAuthFlowUnknown)
				return
			}
			providers = append(providers, ProviderIdentitySummary{
				Provider: identity.Provider, ProviderEmail: identity.ProviderEmail,
				ProviderEmailVerified: identity.ProviderEmailVerified,
			})
		}
		c.JSON(http.StatusOK, IdentitySummaryResponse{PrimaryEmail: user.Email, LinkedProviders: providers})
	}
}

func (s *GoogleOAuthService) completeLoginCallback(c *gin.Context, browserValue string, transaction oauthTransaction, claims *googleClaims) {
	identity, err := s.repo.GetExternalIdentity(c.Request.Context(), googleProvider, claims.Issuer, claims.Subject)
	if err != nil {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeUnavailable)
		return
	}
	if identity != nil {
		user, err := GetUser(c.Request.Context(), s.fs, identity.UserID)
		if err != nil || user == nil {
			s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeInvalidRequest)
			return
		}
		if usableProviderEmail(claims.Email) {
			if _, err := s.fs.Collection(ExternalIdentitiesCollection).Doc(externalIdentityDocumentID(googleProvider, claims.Issuer, claims.Subject)).Update(c.Request.Context(), []firestore.Update{
				{Path: "provider_email", Value: strings.TrimSpace(claims.Email)},
				{Path: "provider_email_verified", Value: claims.EmailVerified},
			}); err != nil {
				s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeUnavailable)
				return
			}
		}
		s.issueSession(c, browserValue, identity.UserID, user, false)
		return
	}
	if s.gate != nil {
		enabled, err := s.gate.IsRegistrationEnabled(c.Request.Context())
		if err != nil {
			s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeUnavailable)
			return
		}
		if !enabled {
			s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeRegistrationDisabled)
			return
		}
	}
	if !claims.EmailVerified || !usableProviderEmail(claims.Email) {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeInvalidRequest)
		return
	}
	reservation, err := s.repo.GetCanonicalEmailReservation(c.Request.Context(), claims.Email)
	if err != nil {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeUnavailable)
		return
	}
	if reservation != nil {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeCanonicalConflict)
		return
	}
	userID := generateUserID()
	if err := s.repo.ProvisionExternalUser(c.Request.Context(), ExternalUserProvisioning{
		UserID: userID, DisplayEmail: strings.TrimSpace(claims.Email), CanonicalEmail: CanonicalizeEmail(claims.Email),
		EmailVerified: true, Provider: googleProvider, Issuer: claims.Issuer, Subject: claims.Subject,
		ProviderEmail: strings.TrimSpace(claims.Email), ProviderEmailVerified: claims.EmailVerified, ProjectID: defaultProjectID,
	}); err != nil {
		if errors.Is(err, ErrCanonicalEmailConflict) {
			s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeCanonicalConflict)
			return
		}
		if errors.Is(err, ErrExternalIdentityConflict) {
			s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeLinkConflict)
			return
		}
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeUnavailable)
		return
	}
	user, err := GetUser(c.Request.Context(), s.fs, userID)
	if err != nil || user == nil {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeUnavailable)
		return
	}
	s.issueSession(c, browserValue, userID, user, true)
}

func (s *GoogleOAuthService) completeLinkCallback(c *gin.Context, browserValue, transactionID string, transaction oauthTransaction, claims *googleClaims) {
	user, err := GetUser(c.Request.Context(), s.fs, transaction.UserID)
	if err != nil || user == nil || user.PasswordHash == "" || passwordProofHash(user.PasswordHash) != transaction.PasswordProofHash {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLink, oauthOutcomePasswordProof)
		return
	}
	identity, err := s.repo.GetExternalIdentity(c.Request.Context(), googleProvider, claims.Issuer, claims.Subject)
	if err != nil {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLink, oauthOutcomeUnavailable)
		return
	}
	if identity != nil {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLink, oauthOutcomeLinkConflict)
		return
	}
	confirmationID, err := randomOpaqueValue(24)
	if err != nil {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLink, oauthOutcomeUnavailable)
		return
	}
	now := s.now().UTC()
	providerEmail := strings.TrimSpace(claims.Email)
	if !usableProviderEmail(providerEmail) {
		providerEmail = ""
	}
	pending := oauthPendingLink{
		UserID: transaction.UserID, Provider: googleProvider, Issuer: claims.Issuer, Subject: claims.Subject,
		ProviderEmail: providerEmail, ProviderVerified: claims.EmailVerified, CurrentEmail: user.Email,
		PasswordProofHash: transaction.PasswordProofHash, Status: oauthStatusActive, CreatedAt: now, ExpiresAt: now.Add(oauthTransactionTTL),
	}
	if err := s.fs.RunTransaction(c.Request.Context(), func(ctx context.Context, tx *firestore.Transaction) error {
		identityRef := s.fs.Collection(ExternalIdentitiesCollection).Doc(externalIdentityDocumentID(googleProvider, claims.Issuer, claims.Subject))
		identitySnapshot, err := optionalTransactionGet(tx, identityRef)
		if err != nil {
			return err
		}
		if identitySnapshot != nil {
			return ErrExternalIdentityConflict
		}
		return tx.Create(s.fs.Collection(oauthPendingLinksCollection).Doc(confirmationID), pending)
	}); err != nil {
		if errors.Is(err, ErrExternalIdentityConflict) {
			s.redirectOAuthFailure(c, browserValue, OAuthFlowLink, oauthOutcomeLinkConflict)
			return
		}
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLink, oauthOutcomeUnavailable)
		return
	}
	_ = transactionID // retained in the durable transaction for forensic correlation, never returned.
	setOAuthPendingCookie(c, confirmationID, int(oauthTransactionTTL.Seconds()))
	s.redirectOAuthResult(c, browserValue, OAuthFlowLink, "confirmation_required", "", false, "")
}

func (s *GoogleOAuthService) issueSession(c *gin.Context, browserValue, userID string, user *UserRecord, jitProvisioned bool) {
	refreshToken, err := GenerateRefreshToken(userID, user.Role, s.jwtSecret)
	if err != nil {
		s.redirectOAuthFailure(c, browserValue, OAuthFlowLogin, oauthOutcomeUnavailable)
		return
	}
	s.redirectOAuthResult(c, browserValue, OAuthFlowLogin, "success", "", jitProvisioned, refreshToken)
}

func (s *GoogleOAuthService) redirectOAuthFailure(c *gin.Context, browserValue string, kind OAuthFlowKind, outcome oauthOutcome) {
	status := "failure"
	if outcome == oauthOutcomeCancelled {
		status = "cancelled"
	}
	s.redirectOAuthResult(c, browserValue, kind, status, outcome, false, "")
}

func (s *GoogleOAuthService) redirectOAuthResult(c *gin.Context, browserValue string, kind OAuthFlowKind, status string, outcome oauthOutcome, jitProvisioned bool, refreshToken string) {
	if s == nil || s.fs == nil || browserValue == "" {
		writeOAuthFailure(c.Writer, http.StatusServiceUnavailable, oauthOutcomeUnavailable, kind)
		return
	}
	completionURL, err := s.fixedCompletionURL()
	if err != nil {
		writeOAuthFailure(c.Writer, http.StatusServiceUnavailable, oauthOutcomeUnavailable, kind)
		return
	}
	resultID, err := randomOpaqueValue(24)
	if err != nil {
		writeOAuthFailure(c.Writer, http.StatusInternalServerError, oauthOutcomeUnavailable, kind)
		return
	}
	supportRef := ""
	if outcome != "" {
		supportRef = opaqueSupportReference()
	}
	now := s.now().UTC()
	result := oauthCompletionResult{
		BrowserHash: hashOAuthValue(browserValue), FlowKind: string(boundedFlowKind(kind)), Status: status,
		State: oauthCompletionStateActive, Outcome: string(outcome), SupportRef: supportRef,
		JITProvisioned: jitProvisioned, CreatedAt: now, ExpiresAt: now.Add(oauthCompletionTTL),
	}
	if _, err := s.fs.Collection(oauthCompletionsCollection).Doc(resultID).Create(c.Request.Context(), result); err != nil {
		writeOAuthFailure(c.Writer, http.StatusServiceUnavailable, oauthOutcomeUnavailable, kind)
		return
	}
	log.Printf(`{"event":"auth_oauth","provider":"google","flow_kind":%q,"outcome":%q,"support_ref":%q,"timestamp":%q}`, boundedFlowKind(kind), outcome, supportRef, time.Now().UTC().Format(time.RFC3339))
	setOAuthCompletionCookie(c, resultID, int(oauthCompletionTTL.Seconds()))
	c.Header("Cache-Control", "no-store")
	if refreshToken != "" {
		setRefreshTokenCookieWithPolicy(c, refreshToken, int(refreshTokenTTL.Seconds()), HostRefreshCookiePolicy())
	}
	c.Redirect(http.StatusFound, completionURL)
}

func (s *GoogleOAuthService) fixedCompletionURL() (string, error) {
	raw := strings.TrimSpace(s.cfg.CompletionURL)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("invalid completion URL")
	}
	return raw, nil
}

func (s *GoogleOAuthService) createTransaction(ctx context.Context, transactionID, browserValue string, transaction oauthTransaction) error {
	browserHash := transaction.BrowserHash
	browserRef := s.fs.Collection(oauthBrowsersCollection).Doc(browserHash)
	transactionRef := s.fs.Collection(oauthTransactionsCollection).Doc(transactionID)
	return s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		lockSnapshot, err := optionalTransactionGet(tx, browserRef)
		if err != nil {
			return err
		}
		if lockSnapshot != nil {
			var lock oauthBrowserLock
			if err := lockSnapshot.DataTo(&lock); err != nil {
				return err
			}
			if lock.TransactionID != "" && lock.TransactionID != transactionID {
				oldRef := s.fs.Collection(oauthTransactionsCollection).Doc(lock.TransactionID)
				oldSnapshot, err := optionalTransactionGet(tx, oldRef)
				if err != nil {
					return err
				}
				if oldSnapshot != nil {
					var old oauthTransaction
					if err := oldSnapshot.DataTo(&old); err != nil {
						return err
					}
					if old.Status == oauthStatusActive && s.now().UTC().Before(old.ExpiresAt) {
						if err := tx.Update(oldRef, []firestore.Update{{Path: "status", Value: oauthStatusSuperseded}}); err != nil {
							return err
						}
					}
				}
			}
		}
		if err := tx.Create(transactionRef, transaction); err != nil {
			return err
		}
		return tx.Set(browserRef, oauthBrowserLock{TransactionID: transactionID, UpdatedAt: s.now().UTC()})
	})
}

func (s *GoogleOAuthService) consumeTransaction(ctx context.Context, state, browserValue string, kind OAuthFlowKind) (string, oauthTransaction, error) {
	transactionID := ""
	var consumed oauthTransaction
	var terminalErr error
	err := s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		stateHash := hashOAuthValue(state)
		query := s.fs.Collection(oauthTransactionsCollection).Where("state_hash", "==", stateHash).Limit(1)
		iter := tx.Documents(query)
		snapshot, err := iter.Next()
		iter.Stop()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				return errOAuthTransactionNotFound
			}
			return err
		}
		var transaction oauthTransaction
		if err := snapshot.DataTo(&transaction); err != nil {
			return errOAuthTransactionNotFound
		}
		transactionID, consumed = snapshot.Ref.ID, transaction
		if subtle.ConstantTimeCompare([]byte(transaction.BrowserHash), []byte(hashOAuthValue(browserValue))) != 1 {
			return errOAuthWrongBrowser
		}
		if transaction.FlowKind != string(kind) {
			return errOAuthWrongKind
		}
		now := s.now().UTC()
		if transaction.Status == oauthStatusSuperseded {
			return errOAuthSuperseded
		}
		if transaction.Status != oauthStatusActive {
			return errOAuthReplay
		}
		browserRef := s.fs.Collection(oauthBrowsersCollection).Doc(transaction.BrowserHash)
		lockSnapshot, err := optionalTransactionGet(tx, browserRef)
		if err != nil {
			return err
		}
		if !now.Before(transaction.ExpiresAt) {
			if err := tx.Update(snapshot.Ref, []firestore.Update{{Path: "status", Value: oauthStatusExpired}}); err != nil {
				return err
			}
			if lockSnapshot != nil {
				var lock oauthBrowserLock
				if err := lockSnapshot.DataTo(&lock); err == nil && lock.TransactionID == snapshot.Ref.ID {
					if err := tx.Delete(browserRef); err != nil {
						return err
					}
				}
			}
			terminalErr = errOAuthExpired
			return nil
		}
		if err := tx.Update(snapshot.Ref, []firestore.Update{{Path: "status", Value: oauthStatusConsumed}}); err != nil {
			return err
		}
		if lockSnapshot != nil {
			var lock oauthBrowserLock
			if err := lockSnapshot.DataTo(&lock); err == nil && lock.TransactionID == snapshot.Ref.ID {
				if err := tx.Delete(browserRef); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil && terminalErr != nil {
		err = terminalErr
	}
	return transactionID, consumed, err
}

var (
	errOAuthTransactionNotFound    = errors.New("oauth transaction not found")
	errOAuthWrongBrowser           = errors.New("oauth transaction browser mismatch")
	errOAuthWrongKind              = errors.New("oauth transaction flow mismatch")
	errOAuthSuperseded             = errors.New("oauth transaction superseded")
	errOAuthReplay                 = errors.New("oauth transaction already consumed")
	errOAuthExpired                = errors.New("oauth transaction expired")
	errOAuthPendingNotFound        = errors.New("oauth confirmation not found")
	errOAuthPendingExpired         = errors.New("oauth confirmation expired")
	errOAuthPendingReplay          = errors.New("oauth confirmation already used")
	errOAuthCompletionNotFound     = errors.New("oauth completion not found")
	errOAuthCompletionWrongBrowser = errors.New("oauth completion browser mismatch")
	errOAuthCompletionReplay       = errors.New("oauth completion already used")
	errOAuthCompletionExpired      = errors.New("oauth completion expired")
)

func (s *GoogleOAuthService) confirmLink(ctx context.Context, userID, confirmationID string) (oauthCompletionResponse, error) {
	if !ValidPathSegment(confirmationID) || len(confirmationID) < 20 {
		return oauthCompletionResponse{}, errOAuthPendingNotFound
	}
	var result oauthCompletionResponse
	var terminalErr error
	err := s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		pendingRef := s.fs.Collection(oauthPendingLinksCollection).Doc(confirmationID)
		snapshot, err := optionalTransactionGet(tx, pendingRef)
		if err != nil {
			return err
		}
		if snapshot == nil {
			return errOAuthPendingNotFound
		}
		var pending oauthPendingLink
		if err := snapshot.DataTo(&pending); err != nil || pending.UserID != userID {
			return errOAuthPendingNotFound
		}
		now := s.now().UTC()
		if pending.Status != oauthStatusActive {
			return errOAuthPendingReplay
		}
		if !now.Before(pending.ExpiresAt) {
			if err := tx.Update(pendingRef, []firestore.Update{{Path: "status", Value: oauthStatusExpired}}); err != nil {
				return err
			}
			terminalErr = errOAuthPendingExpired
			return nil
		}
		userSnapshot, err := tx.Get(s.fs.Collection("users").Doc(userID))
		if err != nil {
			return errOAuthPendingNotFound
		}
		var user UserRecord
		if err := userSnapshot.DataTo(&user); err != nil || user.PasswordHash == "" || passwordProofHash(user.PasswordHash) != pending.PasswordProofHash {
			return errOAuthPasswordChanged
		}
		identityRef := s.fs.Collection(ExternalIdentitiesCollection).Doc(externalIdentityDocumentID(pending.Provider, pending.Issuer, pending.Subject))
		identitySnapshot, err := optionalTransactionGet(tx, identityRef)
		if err != nil {
			return err
		}
		if identitySnapshot != nil {
			if err := tx.Update(pendingRef, []firestore.Update{{Path: "status", Value: oauthStatusExpired}}); err != nil {
				return err
			}
			terminalErr = ErrExternalIdentityConflict
			return nil
		}
		identity := ExternalIdentity{
			Provider: pending.Provider, Issuer: pending.Issuer, Subject: pending.Subject, UserID: userID,
			ProviderEmail: pending.ProviderEmail, ProviderEmailVerified: pending.ProviderVerified, CreatedAt: now,
		}
		if err := tx.Create(identityRef, identity); err != nil {
			return err
		}
		if err := tx.Update(pendingRef, []firestore.Update{{Path: "status", Value: oauthStatusConfirmed}}); err != nil {
			return err
		}
		result = oauthCompletionResponse{Status: "linked"}
		return nil
	})
	if err == nil && terminalErr != nil {
		err = terminalErr
	}
	return result, err
}

func (s *GoogleOAuthService) cancelLink(ctx context.Context, userID, confirmationID string) (oauthCompletionResponse, error) {
	if !ValidPathSegment(confirmationID) || len(confirmationID) < 20 {
		return oauthCompletionResponse{}, errOAuthPendingNotFound
	}
	var result oauthCompletionResponse
	var terminalErr error
	err := s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		pendingRef := s.fs.Collection(oauthPendingLinksCollection).Doc(confirmationID)
		snapshot, err := optionalTransactionGet(tx, pendingRef)
		if err != nil {
			return err
		}
		if snapshot == nil {
			return errOAuthPendingNotFound
		}
		var pending oauthPendingLink
		if err := snapshot.DataTo(&pending); err != nil || pending.UserID != userID {
			return errOAuthPendingNotFound
		}
		if pending.Status != oauthStatusActive {
			return errOAuthPendingReplay
		}
		if !s.now().UTC().Before(pending.ExpiresAt) {
			if err := tx.Update(pendingRef, []firestore.Update{{Path: "status", Value: oauthStatusExpired}}); err != nil {
				return err
			}
			terminalErr = errOAuthPendingExpired
			return nil
		}
		if err := tx.Update(pendingRef, []firestore.Update{{Path: "status", Value: oauthStatusCancelled}}); err != nil {
			return err
		}
		result = oauthCompletionResponse{Status: oauthStatusCancelled}
		return nil
	})
	if err == nil && terminalErr != nil {
		err = terminalErr
	}
	return result, err
}

var errOAuthPasswordChanged = errors.New("password proof changed")

func (s *GoogleOAuthService) exchangeCode(ctx context.Context, transaction oauthTransaction, code string) (string, error) {
	form := url.Values{
		"client_id": {s.cfg.ClientID}, "client_secret": {s.cfg.ClientSecret}, "code": {code},
		"code_verifier": {transaction.PKCEVerifier}, "grant_type": {"authorization_code"}, "redirect_uri": {transaction.RedirectURL},
	}
	requestCtx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, s.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", errors.New("provider token exchange failed")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthBodyBytes+1))
	if err != nil || len(data) > maxOAuthBodyBytes {
		return "", errors.New("provider token response invalid")
	}
	var payload struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || strings.TrimSpace(payload.IDToken) == "" {
		return "", errors.New("provider token response invalid")
	}
	return payload.IDToken, nil
}

func (s *GoogleOAuthService) authorizationURL(kind OAuthFlowKind, state, nonce, challenge string) (string, error) {
	endpoint := strings.TrimSpace(s.cfg.AuthorizationURL)
	if endpoint == "" {
		if s.cfg.Issuer == "https://accounts.google.com" || s.cfg.Issuer == "accounts.google.com" {
			endpoint = "https://accounts.google.com/o/oauth2/v2/auth"
		} else {
			endpoint = strings.TrimRight(s.cfg.Issuer, "/") + "/authorize"
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid authorization endpoint")
	}
	query := u.Query()
	query.Set("client_id", s.cfg.ClientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", s.redirectURL(kind))
	query.Set("scope", "openid email profile")
	query.Set("state", state)
	query.Set("nonce", nonce)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	if kind == OAuthFlowLink {
		query.Set("prompt", "select_account")
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (s *GoogleOAuthService) redirectURL(kind OAuthFlowKind) string {
	if kind == OAuthFlowLink {
		return s.cfg.LinkRedirectURL
	}
	return s.cfg.LoginRedirectURL
}

func (s *GoogleOAuthService) configReady() bool {
	if s == nil {
		return false
	}
	if _, err := s.fixedCompletionURL(); err != nil {
		return false
	}
	// AuthServiceURL is populated by production config. Directly injected
	// fake-provider services intentionally leave it empty for causal tests.
	if strings.TrimSpace(s.cfg.AuthServiceURL) != "" {
		if err := s.cfg.Validate(); err != nil {
			return false
		}
	}
	return strings.TrimSpace(s.cfg.ClientID) != "" && strings.TrimSpace(s.cfg.ClientSecret) != "" &&
		strings.TrimSpace(s.cfg.Issuer) != "" && strings.TrimSpace(s.cfg.JWKSURL) != "" && strings.TrimSpace(s.cfg.TokenURL) != "" &&
		strings.TrimSpace(s.cfg.LoginRedirectURL) != "" && strings.TrimSpace(s.cfg.LinkRedirectURL) != "" &&
		strings.TrimSpace(s.cfg.CompletionURL) != "" && s.cfg.LoginRedirectURL != s.cfg.LinkRedirectURL
}

func (s *GoogleOAuthService) sameSiteOrigin(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		if referer := strings.TrimSpace(request.Header.Get("Referer")); referer != "" {
			if parsed, err := url.Parse(referer); err == nil {
				origin = parsed.Scheme + "://" + parsed.Host
			}
		}
	}
	if origin == "" {
		return false
	}
	for _, allowed := range s.cfg.AllowedOrigins {
		if subtle.ConstantTimeCompare([]byte(origin), []byte(strings.TrimSpace(allowed))) == 1 {
			return true
		}
	}
	return false
}

func setOAuthCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{Name: oauthCookieName, Value: value, Path: "/", MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func clearOAuthCookie(c *gin.Context) {
	setOAuthCookie(c, "", -1)
}

func setOAuthCompletionCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{Name: oauthCompletionCookieName, Value: value, Path: "/", MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func clearOAuthCompletionCookie(c *gin.Context) {
	setOAuthCompletionCookie(c, "", -1)
}

func setOAuthPendingCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{Name: oauthPendingCookieName, Value: value, Path: "/", MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func clearOAuthPendingCookie(c *gin.Context) {
	setOAuthPendingCookie(c, "", -1)
}

func hashOAuthValue(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func passwordProofHash(passwordHash string) string { return hashOAuthValue(passwordHash) }

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func randomOpaqueValue(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func oauthConsumeStatus(err error) int {
	switch {
	case errors.Is(err, errOAuthWrongBrowser), errors.Is(err, errOAuthWrongKind), errors.Is(err, errOAuthReplay), errors.Is(err, errOAuthSuperseded):
		return http.StatusBadRequest
	case errors.Is(err, errOAuthExpired):
		return http.StatusGone
	default:
		return http.StatusBadRequest
	}
}

func oauthConsumeOutcome(err error) oauthOutcome {
	switch {
	case errors.Is(err, errOAuthWrongBrowser):
		return oauthOutcomeWrongBrowser
	case errors.Is(err, errOAuthWrongKind):
		return oauthOutcomeWrongKind
	case errors.Is(err, errOAuthReplay), errors.Is(err, errOAuthSuperseded), errors.Is(err, errOAuthTransactionNotFound):
		return oauthOutcomeReplay
	case errors.Is(err, errOAuthExpired):
		return oauthOutcomeExpired
	default:
		return oauthOutcomeInvalidRequest
	}
}

func oauthLinkStatus(err error) (int, oauthOutcome) {
	switch {
	case errors.Is(err, errOAuthPendingExpired):
		return http.StatusGone, oauthOutcomeExpired
	case errors.Is(err, errOAuthPendingReplay):
		return http.StatusConflict, oauthOutcomeReplay
	case errors.Is(err, ErrExternalIdentityConflict):
		return http.StatusConflict, oauthOutcomeLinkConflict
	case errors.Is(err, errOAuthPasswordChanged):
		return http.StatusUnauthorized, oauthOutcomePasswordProof
	case errors.Is(err, errOAuthPendingNotFound):
		return http.StatusBadRequest, oauthOutcomeInvalidRequest
	default:
		return http.StatusInternalServerError, oauthOutcomeUnavailable
	}
}

func oauthCompletionStatus(err error) int {
	switch {
	case errors.Is(err, errOAuthCompletionExpired):
		return http.StatusGone
	case errors.Is(err, errOAuthCompletionReplay):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func oauthCompletionOutcome(err error) oauthOutcome {
	switch {
	case errors.Is(err, errOAuthCompletionExpired):
		return oauthOutcomeExpired
	case errors.Is(err, errOAuthCompletionReplay):
		return oauthOutcomeReplay
	case errors.Is(err, errOAuthCompletionWrongBrowser):
		return oauthOutcomeWrongBrowser
	default:
		return oauthOutcomeInvalidRequest
	}
}

func resultErrorMessage(result oauthCompletionResult) string {
	if result.Status != "failure" && result.Status != "cancelled" {
		return ""
	}
	message := oauthOutcomeMessage(oauthOutcome(result.Outcome))
	if message == "" {
		return "Unable to continue with Google sign-in."
	}
	return message
}

// googleClaims contains only identity claims needed for completion.
type googleClaims struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
}

type googleOIDCVerifier struct {
	cfg        GoogleConfig
	client     *http.Client
	mu         sync.Mutex
	keys       map[string]*rsa.PublicKey
	fetchedAt  time.Time
	freshUntil time.Time
	now        func() time.Time
}

func newGoogleOIDCVerifier(cfg GoogleConfig, client *http.Client) *googleOIDCVerifier {
	if client == nil {
		client = boundedOAuthHTTPClient()
	}
	return &googleOIDCVerifier{cfg: cfg, client: client, now: time.Now}
}

func boundedOAuthHTTPClient() *http.Client {
	return &http.Client{
		Timeout: oauthHTTPTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (v *googleOIDCVerifier) Verify(ctx context.Context, tokenString, nonce string) (*googleClaims, error) {
	if strings.TrimSpace(tokenString) == "" || len(tokenString) > maxOIDCTokenBytes {
		return nil, errors.New("invalid token")
	}
	parser := jwt.NewParser()
	unsigned, _, err := parser.ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return nil, errors.New("invalid token")
	}
	if unsigned.Method.Alg() != jwt.SigningMethodRS256.Alg() {
		return nil, errors.New("invalid algorithm")
	}
	kid, ok := unsigned.Header["kid"].(string)
	if !ok || strings.TrimSpace(kid) == "" {
		return nil, errors.New("missing key id")
	}
	key, err := v.keyFor(ctx, kid)
	if err != nil {
		return nil, err
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, errors.New("invalid algorithm")
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithoutClaimsValidation())
	if err != nil || parsed == nil || !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return validateGoogleClaims(claims, v.cfg, nonce, v.now().UTC())
}

func (v *googleOIDCVerifier) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	now := v.now().UTC()
	v.mu.Lock()
	fresh := !v.freshUntil.IsZero() && now.Before(v.freshUntil)
	key := v.keys[kid]
	v.mu.Unlock()
	if fresh && key != nil {
		return key, nil
	}
	if fresh {
		// A warm cache missing the key permits exactly one synchronous refresh.
		keys, fetchedAt, freshUntil, err := v.fetchKeys(ctx)
		if err != nil {
			return nil, err
		}
		v.mu.Lock()
		v.keys, v.fetchedAt, v.freshUntil = keys, fetchedAt, freshUntil
		key = keys[kid]
		v.mu.Unlock()
		if key == nil {
			return nil, errors.New("unknown key")
		}
		return key, nil
	}
	keys, fetchedAt, freshUntil, err := v.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.keys, v.fetchedAt, v.freshUntil = keys, fetchedAt, freshUntil
	key = keys[kid]
	v.mu.Unlock()
	if key == nil {
		// Exactly one synchronous refresh is permitted for an unknown kid.
		keys, fetchedAt, freshUntil, err = v.fetchKeys(ctx)
		if err != nil {
			return nil, err
		}
		v.mu.Lock()
		v.keys, v.fetchedAt, v.freshUntil = keys, fetchedAt, freshUntil
		key = keys[kid]
		v.mu.Unlock()
	}
	if key == nil {
		return nil, errors.New("unknown key")
	}
	return key, nil
}

func (v *googleOIDCVerifier) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, time.Time, time.Time, error) {
	requestCtx, cancel := context.WithTimeout(ctx, oauthHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	response, err := v.client.Do(request)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 || response.ContentLength > maxJWKSBytes {
		return nil, time.Time{}, time.Time{}, errors.New("invalid keyset response")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxJWKSBytes+1))
	if err != nil || len(data) > maxJWKSBytes {
		return nil, time.Time{}, time.Time{}, errors.New("invalid keyset response")
	}
	var payload struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || len(payload.Keys) == 0 || len(payload.Keys) > maxJWKSKeys {
		return nil, time.Time{}, time.Time{}, errors.New("invalid keyset")
	}
	keys := make(map[string]*rsa.PublicKey, len(payload.Keys))
	for _, raw := range payload.Keys {
		var item struct {
			KTY string `json:"kty"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		}
		if json.Unmarshal(raw, &item) != nil || item.KTY != "RSA" || item.Alg != "RS256" || (item.Use != "" && item.Use != "sig") || item.Kid == "" || item.N == "" || item.E == "" {
			return nil, time.Time{}, time.Time{}, errors.New("invalid keyset")
		}
		modulus, err := base64.RawURLEncoding.DecodeString(item.N)
		if err != nil || len(modulus) == 0 || len(modulus) > maxRSAModulusBytes {
			return nil, time.Time{}, time.Time{}, errors.New("invalid keyset")
		}
		exponentBytes, err := base64.RawURLEncoding.DecodeString(item.E)
		if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
			return nil, time.Time{}, time.Time{}, errors.New("invalid keyset")
		}
		exponent := 0
		for _, b := range exponentBytes {
			exponent = exponent<<8 | int(b)
		}
		if exponent < 2 || exponent > math.MaxInt32 {
			return nil, time.Time{}, time.Time{}, errors.New("invalid keyset")
		}
		if _, exists := keys[item.Kid]; exists {
			return nil, time.Time{}, time.Time{}, errors.New("invalid keyset")
		}
		modulusInt := new(big.Int).SetBytes(modulus)
		if modulusInt.Sign() <= 0 {
			return nil, time.Time{}, time.Time{}, errors.New("invalid keyset")
		}
		keys[item.Kid] = &rsa.PublicKey{N: modulusInt, E: exponent}
	}
	fetchedAt := v.now().UTC()
	freshFor := 6 * time.Hour
	for _, directive := range strings.Split(strings.ToLower(response.Header.Get("Cache-Control")), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(directive), "=")
		if !ok || strings.TrimSpace(key) != "max-age" {
			continue
		}
		seconds, parseErr := time.ParseDuration(strings.TrimSpace(value) + "s")
		if parseErr == nil && seconds >= 0 && seconds < freshFor {
			freshFor = seconds
		}
	}
	return keys, fetchedAt, fetchedAt.Add(freshFor), nil
}

func validateGoogleClaims(claims jwt.MapClaims, cfg GoogleConfig, nonce string, now time.Time) (*googleClaims, error) {
	issuer, ok := claims["iss"].(string)
	if !ok || !issuerAllowed(issuer, cfg.Issuer) {
		return nil, errors.New("invalid issuer")
	}
	subject, ok := claims["sub"].(string)
	if !ok || strings.TrimSpace(subject) == "" || len(subject) > maxProviderSubjectBytes {
		return nil, errors.New("invalid subject")
	}
	audiences, ok := claimAudiences(claims["aud"])
	if !ok || len(audiences) == 0 {
		return nil, errors.New("invalid audience")
	}
	hasClientAudience := false
	for _, audience := range audiences {
		if audience == cfg.ClientID {
			hasClientAudience = true
		}
	}
	if !hasClientAudience {
		return nil, errors.New("invalid audience")
	}
	azp := ""
	if rawAZP, present := claims["azp"]; present {
		var ok bool
		azp, ok = rawAZP.(string)
		if !ok || azp == "" || azp != cfg.ClientID {
			return nil, errors.New("invalid authorized party")
		}
	}
	if len(audiences) > 1 && azp != cfg.ClientID {
		return nil, errors.New("missing authorized party")
	}
	nowSeconds := float64(now.UnixNano()) / float64(time.Second)
	skewSeconds := oauthClockSkew.Seconds()
	exp, ok := claimNumber(claims["exp"])
	if !ok || exp < nowSeconds-skewSeconds {
		return nil, errors.New("expired token")
	}
	iat, ok := claimNumber(claims["iat"])
	if !ok || iat > nowSeconds+skewSeconds {
		return nil, errors.New("invalid issued-at")
	}
	if tokenNonce, ok := claims["nonce"].(string); !ok || subtle.ConstantTimeCompare([]byte(tokenNonce), []byte(nonce)) != 1 {
		return nil, errors.New("invalid nonce")
	}
	email, _ := claims["email"].(string)
	emailVerified, _ := claims["email_verified"].(bool)
	return &googleClaims{Issuer: issuer, Subject: subject, Email: email, EmailVerified: emailVerified}, nil
}

func usableProviderEmail(email string) bool {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > maxProviderEmailBytes || strings.Count(email, "@") != 1 || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return false
	}
	for _, r := range email {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func issuerAllowed(actual, configured string) bool {
	if configured == "https://accounts.google.com" || configured == "accounts.google.com" {
		return actual == "https://accounts.google.com" || actual == "accounts.google.com"
	}
	return actual == configured
}

func claimAudiences(value interface{}) ([]string, bool) {
	switch value := value.(type) {
	case string:
		return []string{value}, value != ""
	case []string:
		if len(value) == 0 {
			return nil, false
		}
		for _, audience := range value {
			if audience == "" {
				return nil, false
			}
		}
		return append([]string(nil), value...), true
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			audience, ok := item.(string)
			if !ok || audience == "" {
				return nil, false
			}
			out = append(out, audience)
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

func claimNumber(value interface{}) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number > 0 && math.Trunc(number) == number
}
