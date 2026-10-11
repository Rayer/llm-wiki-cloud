package v1

import (
	"errors"
	"net/http"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
)

// MCPProjectOAuthHandler serves the project-scoped OAuth resource with the
// same stateless MCP tool implementation used by /mcp.
func (h *Handler) MCPProjectOAuthHandler(environment, resourceOrigin string) gin.HandlerFunc {
	var rawFirestore *firestore.Client
	if h != nil && h.firestore != nil {
		rawFirestore = h.firestore.Raw()
	}
	authority := auth.NewMCPGrantAuthority(rawFirestore, environment, resourceOrigin)
	sdk := h.mcpSDKHandler()
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
		identity, err := authority.AuthenticateAccessToken(c.Request.Context(), token, projectID, resource)
		if err != nil {
			writeMCPProjectOAuthError(c, metadata, err)
			return
		}
		if requestedProject := c.GetHeader("X-Project-ID"); requestedProject != "" && requestedProject != projectID {
			writeMCPProjectOAuthError(c, metadata, auth.ErrMCPOAuthDenied)
			return
		}
		requestIdentity := mcpProjectKeyIdentity{
			userID: identity.UserID, projectID: identity.ProjectID, keyID: identity.GrantID,
		}
		h.serveAuthenticatedMCP(sdk, requestIdentity, c.Writer, c.Request)
	}
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
			"scopes_supported":         []string{"mcp:query"},
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
