package auth

import (
	"errors"
	"net/http"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/config"
)

type mcpConsentLookupRequest struct {
	SchemaVersion int    `json:"schema_version"`
	TransactionID string `json:"transaction_id"`
	Subject       string `json:"subject"`
	AuthVersion   int64  `json:"auth_version"`
}

type mcpConsentDecisionBody struct {
	SchemaVersion  int      `json:"schema_version"`
	Subject        string   `json:"subject"`
	AuthVersion    int64    `json:"auth_version"`
	Decision       string   `json:"decision"`
	ApprovedScopes []string `json:"approved_scopes"`
}

type mcpProjectGrantsRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Subject       string `json:"subject"`
	AuthVersion   int64  `json:"auth_version"`
	ProjectID     string `json:"project_id"`
}

// RegisterMCPOAuthConsumerRoutes exposes transaction and Project-grant facts
// only to the authenticated LWC BFF service identity.
func RegisterMCPOAuthConsumerRoutes(routes *gin.RouterGroup, cfg config.Config, fs *firestore.Client,
	policy LWCConsumerPolicy, validate OIDCCallerValidator, verifySession CLIAccessSessionVerifier, service *MCPOAuthService) {
	if routes == nil {
		return
	}
	routes.POST("/mcp-consent/transactions/lookup", trustedMCPConsentLookupHandler(cfg, fs, policy, validate, verifySession, service))
	routes.POST("/mcp-consent/transactions/:transactionID/decision", trustedMCPConsentDecisionHandler(cfg, fs, policy, validate, verifySession, service))
	routes.POST("/mcp-consent/grants/list", trustedMCPProjectGrantsHandler(cfg, fs, policy, validate, verifySession, service))
}

func trustedMCPConsentLookupHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	validate OIDCCallerValidator, verifySession CLIAccessSessionVerifier, service *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		if service == nil || fs == nil || validate == nil || !authorizeLWCConsumer(c, policy, validate) {
			if service == nil || fs == nil || validate == nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "temporarily_unavailable"})
			}
			return
		}
		var request mcpConsentLookupRequest
		if !decodeStrictConsumerBody(c, &request) || request.SchemaVersion != 1 || !ValidPathSegment(request.Subject) || request.AuthVersion < 0 ||
			!validMCPOpaque(request.TransactionID, "lwc_mcp_tx_") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
			return
		}
		subject, version, ok := trustedMCPUserCredential(c, cfg, fs, policy, verifySession)
		if !ok || subject != request.Subject || version != request.AuthVersion {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credential"})
			return
		}
		result, err := service.LookupConsentTransaction(c.Request.Context(), subject, version, request.TransactionID)
		if err != nil {
			writeTrustedMCPError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func trustedMCPConsentDecisionHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	validate OIDCCallerValidator, verifySession CLIAccessSessionVerifier, service *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		if service == nil || fs == nil || validate == nil || !authorizeLWCConsumer(c, policy, validate) {
			if service == nil || fs == nil || validate == nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "temporarily_unavailable"})
			}
			return
		}
		var body mcpConsentDecisionBody
		if !decodeStrictConsumerBody(c, &body) || body.SchemaVersion != 1 || !ValidPathSegment(body.Subject) || body.AuthVersion < 0 ||
			(body.Decision != "allow" && body.Decision != "deny" && body.Decision != "continue") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
			return
		}
		subject, version, ok := trustedMCPUserCredential(c, cfg, fs, policy, verifySession)
		if !ok || subject != body.Subject || version != body.AuthVersion {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credential"})
			return
		}
		decision := MCPOAuthTrustedDecision{SchemaVersion: 1, TransactionID: c.Param("transactionID"), Subject: subject,
			AuthVersion: version, Decision: body.Decision, ApprovedScopes: body.ApprovedScopes}
		result, err := service.DecideConsentTrusted(c.Request.Context(), decision)
		if err != nil {
			writeTrustedMCPError(c, err)
			return
		}
		statusValue := "completed"
		if result.RequiresConsent {
			statusValue = "pending"
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "status": statusValue,
			"redirect_url": result.RedirectURL, "requires_consent": result.RequiresConsent})
	}
}

func trustedMCPProjectGrantsHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	validate OIDCCallerValidator, verifySession CLIAccessSessionVerifier, service *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		if service == nil || fs == nil || validate == nil || !authorizeLWCConsumer(c, policy, validate) {
			if service == nil || fs == nil || validate == nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "temporarily_unavailable"})
			}
			return
		}
		var request mcpProjectGrantsRequest
		if !decodeStrictConsumerBody(c, &request) || request.SchemaVersion != 1 || !ValidPathSegment(request.Subject) ||
			request.AuthVersion < 0 || !ValidPathSegment(request.ProjectID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
			return
		}
		subject, version, ok := trustedMCPUserCredential(c, cfg, fs, policy, verifySession)
		if !ok || subject != request.Subject || version != request.AuthVersion {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credential"})
			return
		}
		grants, err := service.ListGrants(c.Request.Context(), subject, request.ProjectID)
		if err != nil {
			writeTrustedMCPError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "grants": grants})
	}
}

func trustedMCPUserCredential(c *gin.Context, cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	verifySession CLIAccessSessionVerifier) (string, int64, bool) {
	credential := strings.TrimSpace(c.GetHeader("X-LWC-User-Credential"))
	if credential == "" || strings.ContainsAny(credential, " \t\r\n") {
		return "", 0, false
	}
	claims, err := ValidateToken(credential, cfg.JWTSecret)
	if err != nil || claims.TokenType != accessTokenType || !ValidPathSegment(claims.Sub) ||
		claims.ExpiresAt == nil || (len(claims.Audience) > 0 && !consumerAudienceContains(claims.Audience, policy.Resource)) ||
		(claims.Issuer != "" && claims.Issuer != policy.Issuer) {
		return "", 0, false
	}
	kind := "web-access"
	if claims.ClientKind == cliClientKind {
		kind = "cli-access"
		if verifySession == nil || !ValidPathSegment(claims.SessionID) ||
			verifySession(c.Request.Context(), claims.Sub, claims.SessionID, claims.AuthVersion) != nil {
			return "", 0, false
		}
	}
	if !policy.allows(kind) {
		return "", 0, false
	}
	user, err := FirestoreAccountLookup(fs)(c.Request.Context(), claims.Sub)
	if err != nil || user == nil || !user.AllowsVersion(claims.AuthVersion) {
		return "", 0, false
	}
	return claims.Sub, user.AuthVersion, true
}

func writeTrustedMCPError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrMCPOAuthNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "transaction_not_found"})
	case errors.Is(err, ErrMCPOAuthExpired):
		c.JSON(http.StatusGone, gin.H{"error": "transaction_expired"})
	case errors.Is(err, ErrMCPOAuthConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "decision_conflict"})
	case errors.Is(err, ErrMCPOAuthDenied):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	case errors.Is(err, ErrMCPOAuthInvalid), errors.Is(err, ErrAccountInactive):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credential"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "temporarily_unavailable"})
	}
}
