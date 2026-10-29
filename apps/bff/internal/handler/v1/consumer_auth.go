package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type consumerVerifier interface {
	Verify(context.Context, string, string) (authconsumer.Identity, error)
	CheckAccount(context.Context, string) (authconsumer.Account, error)
}

type onboardingConsumer interface {
	OnboardingIdentity(context.Context, string) (authconsumer.OnboardingIdentity, error)
}

type mcpConsentConsumer interface {
	LookupMCPConsent(context.Context, string, string, string, int64) (authconsumer.MCPConsentTransaction, error)
	DecideMCPConsent(context.Context, string, authconsumer.MCPConsentDecisionRequest) (authconsumer.MCPConsentDecisionResult, error)
	ListProjectMCPGrants(context.Context, string, string, int64, string) ([]authconsumer.MCPGrant, error)
}

type managementConsumer interface {
	ManagementCatalog(context.Context, string) (authconsumer.ManagementCatalog, error)
	ManagementUsers(context.Context, string) ([]authconsumer.ManagementUser, error)
	GrantManagement(context.Context, string, authconsumer.ManagementGrantRequest) (authconsumer.ManagementMutationResult, error)
	ReadManagementOperation(context.Context, string, string) (authconsumer.ManagementMutationResult, error)
	UpdateAccountStatus(context.Context, string, string, string) error
	DeleteAccount(context.Context, string, string) error
}

type registrationManagementConsumer interface {
	ManagementRegistrationSettings(context.Context, string) (authconsumer.RegistrationSettings, error)
	UpdateManagementRegistrationSettings(context.Context, string, authconsumer.RegistrationSettingsPatch) (authconsumer.RegistrationSettings, error)
}

type legacyAccountManagementConsumer interface {
	UpdateLegacyAccount(context.Context, string, string, *string, *string) error
}

func (h *Handler) AdminGetRegistrationSettings(c *gin.Context) {
	management, ok := h.consumerVerifier.(registrationManagementConsumer)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Auth registration authority unavailable"})
		return
	}
	settings, err := management.ManagementRegistrationSettings(c.Request.Context(), requestUserCredential(c))
	if err != nil {
		statusCode, message := managementConsumerError(err)
		c.JSON(statusCode, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h *Handler) AdminPatchRegistrationSettings(c *gin.Context) {
	management, ok := h.consumerVerifier.(registrationManagementConsumer)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Auth registration authority unavailable"})
		return
	}
	var patch authconsumer.RegistrationSettingsPatch
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil ||
		(patch.RegistrationEnabled == nil && patch.EmailRegistrationEnabled == nil && patch.GoogleRegistrationEnabled == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid settings are required"})
		return
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid settings are required"})
		return
	}
	settings, err := management.UpdateManagementRegistrationSettings(c.Request.Context(), requestUserCredential(c), patch)
	if err != nil {
		statusCode, message := managementConsumerError(err)
		c.JSON(statusCode, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h *Handler) AdminManagementCatalog(c *gin.Context) {
	management, ok := h.consumerVerifier.(managementConsumer)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Auth management authority unavailable"})
		return
	}
	catalog, err := management.ManagementCatalog(c.Request.Context(), requestUserCredential(c))
	if err != nil {
		statusCode, message := managementConsumerError(err)
		c.JSON(statusCode, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, catalog)
}

func (h *Handler) consumerAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		identity, err := h.consumerVerifier.Verify(c.Request.Context(), strings.TrimSpace(parts[1]), strings.TrimSpace(c.GetHeader("X-Project-ID")))
		if errors.Is(err, authconsumer.ErrInvalidCredential) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "account access unavailable"})
			return
		}
		if !auth.ValidPathSegment(identity.Subject) {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "Auth consumer response is invalid"})
			return
		}
		clientKind := ""
		switch identity.CredentialKind {
		case "web-access":
		case "cli-access":
			clientKind = "cli"
		case "mcp-oauth":
			clientKind = "mcp-oauth"
		default:
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "Auth consumer response is invalid"})
			return
		}
		if clientKind == "cli" {
			projectID := strings.TrimSpace(c.GetHeader("X-Project-ID"))
			if projectID != "" {
				if !auth.ValidPathSegment(projectID) {
					c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid project"})
					return
				}
				if h.cliProjectAuthorizer == nil {
					c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "project access authority unavailable"})
					return
				}
				if err := h.cliProjectAuthorizer(c.Request.Context(), identity.Subject, projectID); err != nil {
					statusCode, message := http.StatusServiceUnavailable, "project access authority unavailable"
					if errors.Is(err, auth.ErrProjectPermissionDenied) {
						statusCode, message = http.StatusForbidden, "project access denied"
					}
					c.AbortWithStatusJSON(statusCode, gin.H{"error": message})
					return
				}
			}
		}
		c.Set("userID", identity.Subject)
		c.Set("userRole", "")
		c.Set("authVersion", identity.AuthVersion)
		c.Set("clientKind", clientKind)
		c.Set("sessionID", identity.SessionID)
		c.Set("scopes", identity.Scopes)
		c.Set("managementScopes", identity.ManagementScopes)
		c.Set("authConsumerIdentity", identity)
		c.Next()
	}
}

// ManagementScopeOnly enforces a consumer-verified management capability.
// Role fields and legacy account roles never satisfy this check.
func ManagementScopeOnly(scope string) gin.HandlerFunc {
	return ManagementScopesRequired(scope)
}

func ManagementScopesRequired(scopes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		grantedScopes := c.GetStringSlice("managementScopes")
		for _, required := range scopes {
			found := false
			for _, granted := range grantedScopes {
				if granted == required {
					found = true
					break
				}
			}
			if !found {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "management scope required"})
				return
			}
		}
		c.Next()
	}
}

func remoteAccountLookup(verifier consumerVerifier) auth.AccountLookup {
	return func(ctx context.Context, userID string) (*auth.UserRecord, error) {
		account, err := verifier.CheckAccount(ctx, userID)
		if errors.Is(err, authconsumer.ErrInactiveAccount) {
			return &auth.UserRecord{Status: auth.AccountSuspended, AuthVersion: account.AuthVersion}, nil
		}
		if err != nil {
			return nil, status.Error(codes.Unavailable, auth.ErrAccountUnavailable.Error())
		}
		return &auth.UserRecord{Status: auth.AccountActive, AuthVersion: account.AuthVersion}, nil
	}
}

func requestUserCredential(c *gin.Context) string {
	parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func managementConsumerError(err error) (int, string) {
	switch {
	case errors.Is(err, authconsumer.ErrInvalidCredential):
		return http.StatusUnauthorized, "invalid token"
	case errors.Is(err, authconsumer.ErrForbidden):
		return http.StatusForbidden, "management scope required"
	case errors.Is(err, authconsumer.ErrConflict):
		return http.StatusConflict, "management operation conflicted"
	case errors.Is(err, authconsumer.ErrInvalidRequest):
		return http.StatusBadRequest, "invalid management request"
	case errors.Is(err, authconsumer.ErrNotFound):
		return http.StatusNotFound, "management operation not found"
	default:
		return http.StatusServiceUnavailable, "Auth management authority unavailable"
	}
}

func convertManagementScopeStates(states map[string]authconsumer.ManagementScope) map[string]auth.ManagementScopeState {
	result := make(map[string]auth.ManagementScopeState, len(states))
	for scope, state := range states {
		result[scope] = auth.ManagementScopeState{State: state.State, Revision: state.Revision, Source: state.Source, UpdatedAt: state.UpdatedAt}
	}
	return result
}
