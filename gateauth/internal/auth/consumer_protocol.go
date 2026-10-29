package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/config"
	"google.golang.org/api/idtoken"
)

const (
	LWCConsumerVerifyPath     = "/api/v1/auth/consumers/lwc/verify-credential"
	LWCConsumerCheckPath      = "/api/v1/auth/consumers/lwc/accounts/check"
	LWCOnboardingIdentityPath = "/api/v1/auth/consumers/lwc/onboarding/identity"
)

const LocalFixtureCallerToken = "gateauth-local-fixture:oidc-v1"
const LocalFixtureCallerEmail = "bff-local@localhost"

// LocalFixtureOIDCValidator is only wired when the Auth process has an
// explicit loopback LocalCloudScope. It validates a synthetic test identity;
// it is not Google OIDC validation or proof of a deployed service identity.
func LocalFixtureOIDCValidator(_ context.Context, token, audience string) (*idtoken.Payload, error) {
	u, err := url.Parse(audience)
	if token != LocalFixtureCallerToken || err != nil || u.Scheme != "http" ||
		(u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") || u.User != nil ||
		u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("local synthetic OIDC identity rejected")
	}
	return &idtoken.Payload{
		Issuer: "https://accounts.google.com", Audience: audience, Expires: time.Now().Add(5 * time.Minute).Unix(),
		Claims: map[string]interface{}{"email": LocalFixtureCallerEmail, "email_verified": true},
	}, nil
}

// LWCConsumerPolicy is the Auth-owned allowlist for the first-party BFF.
// The caller identity and audience are selected by typed environment config.
type LWCConsumerPolicy struct {
	Enabled                bool
	Environment            string
	Resource               string
	CallerServiceAccount   string
	VerifierAudience       string
	Issuer                 string
	AllowedCredentialKinds []string
}

type LWCConsumerVerifyRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Resource      string `json:"resource"`
	ProjectID     string `json:"project_id,omitempty"`
}

type LWCConsumerIdentity struct {
	SchemaVersion    int      `json:"schema_version"`
	Subject          string   `json:"subject"`
	CredentialKind   string   `json:"credential_kind"`
	Environment      string   `json:"environment"`
	Issuer           string   `json:"issuer"`
	Resource         string   `json:"resource"`
	ExpiresAt        int64    `json:"expires_at"`
	AuthVersion      int64    `json:"auth_version"`
	Scopes           []string `json:"scopes"`
	ManagementScopes []string `json:"management_scopes"`
	SessionID        string   `json:"session_id,omitempty"`
	GrantID          string   `json:"grant_id,omitempty"`
	ClientID         string   `json:"client_id,omitempty"`
	ProjectID        string   `json:"project_id,omitempty"`
}

type LWCOnboardingIdentity struct {
	SchemaVersion    int    `json:"schema_version"`
	Subject          string `json:"subject"`
	AuthVersion      int64  `json:"auth_version"`
	DefaultProjectID string `json:"default_project_id"`
}

type LWCOnboardingIdentityRequest struct {
	SchemaVersion int `json:"schema_version"`
}

type LWCConsumerAccountRequest struct {
	SchemaVersion int    `json:"schema_version"`
	UserID        string `json:"user_id"`
}

type LWCConsumerAccountResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Subject       string `json:"subject"`
	Active        bool   `json:"active"`
	AuthVersion   int64  `json:"auth_version"`
}

type OIDCCallerValidator func(context.Context, string, string) (*idtoken.Payload, error)

// LWCConsumerVerifyHandler validates a first-party caller and then verifies
// the original user credential against Auth-owned signing/account/session state.
func LWCConsumerVerifyHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	verifyCaller OIDCCallerValidator, verifySession CLIAccessSessionVerifier) gin.HandlerFunc {
	return LWCConsumerVerifyHandlerWithManagement(cfg, fs, policy, verifyCaller, verifySession, nil)
}

func LWCConsumerVerifyHandlerWithManagement(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	verifyCaller OIDCCallerValidator, verifySession CLIAccessSessionVerifier, management *ManagementAuthority) gin.HandlerFunc {
	return LWCConsumerVerifyHandlerWithAuthorities(cfg, fs, policy, verifyCaller, verifySession, management, nil)
}

func LWCConsumerVerifyHandlerWithAuthorities(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	verifyCaller OIDCCallerValidator, verifySession CLIAccessSessionVerifier, management *ManagementAuthority,
	mcpOAuth *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		if !policy.ready() || fs == nil || verifyCaller == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "consumer authority unavailable"})
			return
		}
		if !authorizeLWCConsumer(c, policy, verifyCaller) {
			return
		}
		var request LWCConsumerVerifyRequest
		if !decodeStrictConsumerBody(c, &request) || request.SchemaVersion != 1 || strings.TrimSpace(request.Resource) == "" ||
			(request.ProjectID != "" && !ValidPathSegment(request.ProjectID)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid consumer request"})
			return
		}
		credential := strings.TrimSpace(c.GetHeader("X-LWC-User-Credential"))
		if credential == "" || strings.ContainsAny(credential, " \t\r\n") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		if strings.HasPrefix(credential, mcpOAuthAccessPrefix) {
			if mcpOAuth == nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth authority unavailable"})
				return
			}
			verified, err := mcpOAuth.VerifyAccessToken(c.Request.Context(), credential)
			if errors.Is(err, ErrMCPOAuthInvalid) || errors.Is(err, ErrMCPOAuthExpired) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
				return
			}
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth authority unavailable"})
				return
			}
			if verified.Environment != policy.Environment || verified.Resource != request.Resource || verified.ProjectID != request.ProjectID ||
				verified.ProjectID == "" || !policy.allows("mcp-oauth") {
				c.JSON(http.StatusForbidden, gin.H{"error": "credential resource is not allowed"})
				return
			}
			c.JSON(http.StatusOK, LWCConsumerIdentity{SchemaVersion: 1, Subject: verified.UserID, CredentialKind: "mcp-oauth",
				Environment: policy.Environment, Issuer: policy.Issuer, Resource: verified.Resource,
				ExpiresAt: verified.ExpiresAt.Unix(), AuthVersion: verified.AuthVersion, Scopes: verified.Scopes,
				ManagementScopes: []string{}, GrantID: verified.GrantID, ClientID: verified.ClientID, ProjectID: verified.ProjectID})
			return
		}
		if request.Resource != policy.Resource {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid consumer resource"})
			return
		}
		claims, err := ValidateToken(credential, cfg.JWTSecret)
		if err != nil || claims.TokenType != accessTokenType || !ValidPathSegment(strings.TrimSpace(claims.Sub)) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		if len(claims.Audience) > 0 && !consumerAudienceContains(claims.Audience, policy.Resource) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		if claims.ExpiresAt == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		kind := "web-access"
		if claims.ClientKind == cliClientKind {
			kind = "cli-access"
			if verifySession == nil || !ValidPathSegment(claims.SessionID) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session authority unavailable"})
				return
			}
			if err := verifySession(c.Request.Context(), claims.Sub, claims.SessionID, claims.AuthVersion); err != nil {
				statusCode := cliSessionHTTPStatus(err)
				message := "invalid credential"
				if statusCode == http.StatusServiceUnavailable {
					message = "session authority unavailable"
				}
				c.JSON(statusCode, gin.H{"error": message})
				return
			}
		}
		user, err := FirestoreAccountLookup(fs)(c.Request.Context(), claims.Sub)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authority unavailable"})
			return
		}
		if !user.AllowsVersion(claims.AuthVersion) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		if !policy.allows(kind) {
			c.JSON(http.StatusForbidden, gin.H{"error": "credential kind is not allowed"})
			return
		}
		if claims.Issuer != "" && claims.Issuer != policy.Issuer {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		managementScopes := []string{}
		if management != nil {
			managementScopes, err = management.EffectiveScopes(c.Request.Context(), claims.Sub, claims.ManagementScopeVersions)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "management authority unavailable"})
				return
			}
		}
		response := LWCConsumerIdentity{
			SchemaVersion: 1, Subject: claims.Sub, CredentialKind: kind,
			Environment: policy.Environment, Issuer: policy.Issuer, Resource: policy.Resource,
			ExpiresAt: claims.ExpiresAt.Unix(), AuthVersion: user.AuthVersion,
			Scopes: []string{}, ManagementScopes: managementScopes,
		}
		if kind == "cli-access" {
			response.SessionID = claims.SessionID
		}
		c.JSON(http.StatusOK, response)
	}
}

// LWCConsumerAccountCheckHandler only returns the small account-state result
// needed by BFF-owned project-key and owner checks.
func LWCConsumerAccountCheckHandler(fs *firestore.Client, policy LWCConsumerPolicy, verifyCaller OIDCCallerValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		if !policy.ready() || fs == nil || verifyCaller == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "consumer authority unavailable"})
			return
		}
		if !authorizeLWCConsumer(c, policy, verifyCaller) {
			return
		}
		var request LWCConsumerAccountRequest
		if !decodeStrictConsumerBody(c, &request) || request.SchemaVersion != 1 || !ValidPathSegment(request.UserID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid consumer request"})
			return
		}
		user, err := FirestoreAccountLookup(fs)(c.Request.Context(), request.UserID)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authority unavailable"})
			return
		}
		if !user.Active() {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "account inactive"})
			return
		}
		c.JSON(http.StatusOK, LWCConsumerAccountResponse{SchemaVersion: 1, Subject: request.UserID, Active: true, AuthVersion: user.AuthVersion})
	}
}

// LWCOnboardingIdentityHandler returns the Auth-owned identity pointer needed
// by LWC's idempotent Project initializer. The v1 verify response stays fixed.
func LWCOnboardingIdentityHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	verifyCaller OIDCCallerValidator, verifySession CLIAccessSessionVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		if !policy.ready() || fs == nil || verifyCaller == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "consumer authority unavailable"})
			return
		}
		if !authorizeLWCConsumer(c, policy, verifyCaller) {
			return
		}
		var request LWCOnboardingIdentityRequest
		if !decodeStrictConsumerBody(c, &request) || request.SchemaVersion != 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid consumer request"})
			return
		}
		credential := strings.TrimSpace(c.GetHeader("X-LWC-User-Credential"))
		if credential == "" || strings.ContainsAny(credential, " \t\r\n") || strings.HasPrefix(credential, mcpOAuthAccessPrefix) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		claims, err := ValidateToken(credential, cfg.JWTSecret)
		if err != nil || claims.TokenType != accessTokenType || !ValidPathSegment(strings.TrimSpace(claims.Sub)) ||
			(claims.Issuer != "" && claims.Issuer != policy.Issuer) ||
			(len(claims.Audience) > 0 && !consumerAudienceContains(claims.Audience, policy.Resource)) || claims.ExpiresAt == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		if claims.ClientKind == cliClientKind {
			if verifySession == nil || !ValidPathSegment(claims.SessionID) {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "session authority unavailable"})
				return
			}
			if err := verifySession(c.Request.Context(), claims.Sub, claims.SessionID, claims.AuthVersion); err != nil {
				statusCode, message := cliSessionHTTPStatus(err), "invalid credential"
				if statusCode == http.StatusServiceUnavailable {
					message = "session authority unavailable"
				}
				c.JSON(statusCode, gin.H{"error": message})
				return
			}
		} else if claims.ClientKind != "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		user, err := FirestoreAccountLookup(fs)(c.Request.Context(), claims.Sub)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authority unavailable"})
			return
		}
		if !user.AllowsVersion(claims.AuthVersion) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credential"})
			return
		}
		if !policy.allows(claimsCredentialKind(claims)) {
			c.JSON(http.StatusForbidden, gin.H{"error": "credential kind is not allowed"})
			return
		}
		if !ValidPathSegment(user.DefaultProject) {
			c.JSON(http.StatusConflict, gin.H{"error": "setup_conflict"})
			return
		}
		c.JSON(http.StatusOK, LWCOnboardingIdentity{
			SchemaVersion: 1, Subject: claims.Sub, AuthVersion: user.AuthVersion, DefaultProjectID: user.DefaultProject,
		})
	}
}

func claimsCredentialKind(claims *Claims) string {
	if claims != nil && claims.ClientKind == cliClientKind {
		return "cli-access"
	}
	return "web-access"
}

func (p LWCConsumerPolicy) ready() bool {
	return p.Enabled && strings.TrimSpace(p.Environment) != "" && strings.TrimSpace(p.Resource) != "" &&
		strings.TrimSpace(p.CallerServiceAccount) != "" && strings.TrimSpace(p.VerifierAudience) != "" && strings.TrimSpace(p.Issuer) != ""
}

func (p LWCConsumerPolicy) allows(kind string) bool {
	for _, allowed := range p.AllowedCredentialKinds {
		if allowed == kind {
			return true
		}
	}
	return false
}

func authorizeLWCConsumer(c *gin.Context, policy LWCConsumerPolicy, validate OIDCCallerValidator) bool {
	parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "untrusted consumer"})
		return false
	}
	if !validLWCConsumerCaller(c.Request.Context(), policy, validate, strings.TrimSpace(parts[1])) {
		c.JSON(http.StatusForbidden, gin.H{"error": "untrusted consumer"})
		return false
	}
	return true
}

func validLWCConsumerCaller(ctx context.Context, policy LWCConsumerPolicy, validate OIDCCallerValidator, callerToken string) bool {
	payload, err := validate(ctx, callerToken, policy.VerifierAudience)
	if err != nil || payload == nil || payload.Audience != policy.VerifierAudience || payload.Issuer != "https://accounts.google.com" || payload.Expires <= time.Now().Unix() {
		return false
	}
	email, _ := payload.Claims["email"].(string)
	verified, _ := payload.Claims["email_verified"].(bool)
	if !verified || !strings.EqualFold(strings.TrimSpace(email), policy.CallerServiceAccount) {
		return false
	}
	return true
}

func consumerNoStore(c *gin.Context) { c.Header("Cache-Control", "no-store") }

func consumerAudienceContains(audiences []string, expected string) bool {
	for _, audience := range audiences {
		if audience == expected {
			return true
		}
	}
	return false
}

func decodeStrictConsumerBody(c *gin.Context, target any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}
