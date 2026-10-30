package v1

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
)

// MCPProjectOAuthHandler serves the project-scoped OAuth resource with the
// same stateless MCP tool implementation used by /mcp.
func (h *Handler) MCPProjectOAuthHandler(environment, resourceOrigin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		projectID := c.Param("projectID")
		if !auth.ValidPathSegment(projectID) || strings.Contains(projectID, "/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
			return
		}
		if strings.TrimSpace(resourceOrigin) == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth resource authority unavailable"})
			return
		}
		resource := strings.TrimRight(resourceOrigin, "/") + "/mcp/projects/" + projectID
		metadata := strings.TrimRight(resourceOrigin, "/") + "/.well-known/oauth-protected-resource/mcp/projects/" + projectID
		token, ok := mcpBearerToken(c.GetHeader("Authorization"))
		if !ok {
			c.Header("WWW-Authenticate", `Bearer resource_metadata="`+metadata+`"`)
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "access token required"})
			return
		}
		verifier, ok := h.consumerVerifier.(interface {
			VerifyResource(context.Context, string, string, string) (authconsumer.Identity, error)
		})
		if !ok {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth resource authority unavailable"})
			return
		}
		identity, err := verifier.VerifyResource(c.Request.Context(), token, resource, projectID)
		if err != nil {
			writeMCPConsumerError(c, metadata, err)
			return
		}
		if identity.CredentialKind != "mcp-oauth" || identity.Environment != environment || identity.Subject == "" || identity.ProjectID != projectID ||
			identity.GrantID == "" || identity.ClientID == "" || identity.Resource != resource || len(identity.ManagementScopes) != 0 ||
			!hasMCPCapability(identity.Scopes, mcpQueryCapability) {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth resource authority unavailable"})
			return
		}
		if h.cliProjectAuthorizer == nil {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "project access authority unavailable"})
			return
		}
		if err := h.cliProjectAuthorizer(c.Request.Context(), identity.Subject, projectID); err != nil {
			if errors.Is(err, auth.ErrProjectPermissionDenied) {
				writeMCPProjectOAuthError(c, metadata, auth.ErrMCPOAuthDenied)
			} else {
				writeMCPProjectOAuthError(c, metadata, auth.ErrMCPOAuthUnavailable)
			}
			return
		}
		if requestedProject := c.GetHeader("X-Project-ID"); requestedProject != "" && requestedProject != projectID {
			writeMCPProjectOAuthError(c, metadata, auth.ErrMCPOAuthDenied)
			return
		}
		requestIdentity := mcpProjectKeyIdentity{userID: identity.Subject, projectID: identity.ProjectID, keyID: identity.GrantID,
			capabilities: append([]string(nil), identity.Scopes...)}
		requestIdentity.revalidate = func(ctx context.Context) error {
			fresh, err := verifier.VerifyResource(ctx, token, resource, projectID)
			if err != nil {
				return normalizeMCPConsumerError(err)
			}
			if fresh.CredentialKind != "mcp-oauth" || fresh.Environment != environment || fresh.Subject != identity.Subject ||
				fresh.ProjectID != identity.ProjectID || fresh.GrantID != identity.GrantID ||
				fresh.ClientID != identity.ClientID || fresh.Resource != identity.Resource || !sameMCPStringSet(fresh.Scopes, identity.Scopes) ||
				!hasMCPCapability(fresh.Scopes, mcpQueryCapability) {
				return auth.ErrMCPOAuthDenied
			}
			if err := h.cliProjectAuthorizer(ctx, fresh.Subject, fresh.ProjectID); err != nil {
				if errors.Is(err, auth.ErrProjectPermissionDenied) {
					return auth.ErrMCPOAuthDenied
				}
				return auth.ErrMCPOAuthUnavailable
			}
			return nil
		}
		h.serveAuthenticatedMCP(h.mcpSDKHandlerWithContext(c.Request.Context(), requestIdentity), requestIdentity, c.Writer, c.Request)
	}
}

func writeMCPConsumerError(c *gin.Context, metadataURL string, err error) {
	switch {
	case errors.Is(err, authconsumer.ErrInvalidCredential):
		writeMCPProjectOAuthError(c, metadataURL, auth.ErrMCPOAuthInvalid)
	case errors.Is(err, authconsumer.ErrForbidden):
		writeMCPProjectOAuthError(c, metadataURL, auth.ErrMCPOAuthDenied)
	default:
		writeMCPProjectOAuthError(c, metadataURL, auth.ErrMCPOAuthUnavailable)
	}
}

func normalizeMCPConsumerError(err error) error {
	switch {
	case errors.Is(err, authconsumer.ErrInvalidCredential):
		return auth.ErrMCPOAuthInvalid
	case errors.Is(err, authconsumer.ErrForbidden):
		return auth.ErrMCPOAuthDenied
	default:
		return auth.ErrMCPOAuthUnavailable
	}
}

func sameMCPStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, value := range left {
		seen[value]++
	}
	for _, value := range right {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

func MCPProjectResourceMetadataHandler(resourceOrigin, authorizationServer string) gin.HandlerFunc {
	resourceOrigin = strings.TrimRight(strings.TrimSpace(resourceOrigin), "/")
	authorizationServer = strings.TrimRight(strings.TrimSpace(authorizationServer), "/")
	return func(c *gin.Context) {
		projectID := c.Param("projectID")
		if !auth.ValidPathSegment(projectID) || strings.Contains(projectID, "/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
			return
		}
		if resourceOrigin == "" || authorizationServer == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth metadata unavailable"})
			return
		}
		c.Header("Cache-Control", "public, max-age=300")
		c.JSON(http.StatusOK, gin.H{
			"resource":                 resourceOrigin + "/mcp/projects/" + projectID,
			"authorization_servers":    []string{authorizationServer},
			"scopes_supported":         []string{mcpQueryCapability, mcpRawWriteCapability},
			"bearer_methods_supported": []string{"header"},
		})
	}
}

func writeMCPProjectOAuthError(c *gin.Context, metadataURL string, err error) {
	c.Header("Cache-Control", "no-store")
	switch {
	case errors.Is(err, auth.ErrMCPOAuthUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth resource authority unavailable"})
	case errors.Is(err, auth.ErrMCPOAuthDenied):
		c.Header("WWW-Authenticate", `Bearer resource_metadata="`+metadataURL+`", error="insufficient_scope"`)
		c.JSON(http.StatusForbidden, gin.H{"error": "project resource access denied"})
	case errors.Is(err, auth.ErrMCPOAuthExpired), errors.Is(err, auth.ErrMCPOAuthInvalid):
		c.Header("WWW-Authenticate", `Bearer resource_metadata="`+metadataURL+`", error="invalid_token"`)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid access token"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth resource authority unavailable"})
	}
}
