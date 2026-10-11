package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/middleware"
)

const mcpOAuthRequestLimit = 16 << 10

type mcpOAuthRegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
}

type mcpOAuthConsentDecisionRequest struct {
	Decision string `json:"decision"`
}

// MCPOAuthMetadataHandler serves authorization-server metadata from the
// configured issuer origin; request Host is never used to build endpoints.
func MCPOAuthMetadataHandler(issuer string) gin.HandlerFunc {
	issuer = trimOrigin(issuer)
	return func(c *gin.Context) {
		if issuer == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth metadata unavailable"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/api/v1/auth/mcp/oauth/authorize",
			"token_endpoint":                        issuer + "/api/v1/auth/mcp/oauth/token",
			"registration_endpoint":                 issuer + "/api/v1/auth/mcp/oauth/register",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
			"scopes_supported":                      []string{mcpOAuthScope},
		})
	}
}

func MCPOAuthRegisterHandler(service *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request mcpOAuthRegistrationRequest
		if err := decodeMCPOAuthJSON(c, &request); err != nil || strings.TrimSpace(request.ClientName) == "" {
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_client_metadata")
			return
		}
		if request.TokenEndpointAuthMethod != "" && request.TokenEndpointAuthMethod != "none" ||
			!mcpOAuthSetSupported(request.GrantTypes, "authorization_code", "refresh_token") ||
			!mcpOAuthSetSupported(request.ResponseTypes, "code") ||
			(request.Scope != "" && request.Scope != mcpOAuthScope) {
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_client_metadata")
			return
		}
		client, err := service.RegisterClient(c.Request.Context(), request.ClientName, request.RedirectURIs)
		if err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusCreated, gin.H{
			"client_id": client.ClientID, "client_id_issued_at": client.CreatedAt.Unix(),
			"client_name": client.ClientName, "redirect_uris": client.RedirectURIs,
			"token_endpoint_auth_method": "none",
			"grant_types":                []string{"authorization_code", "refresh_token"},
			"response_types":             []string{"code"}, "scope": mcpOAuthScope,
		})
	}
}

func MCPOAuthAuthorizeHandler(service *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		redirect, err := service.BeginAuthorization(c.Request.Context(), c.Query("client_id"), c.Query("redirect_uri"),
			c.Query("response_type"), c.Query("code_challenge"), c.Query("code_challenge_method"),
			c.Query("state"), c.Query("resource"), c.Query("scope"))
		if err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Redirect(http.StatusFound, redirect)
	}
}

func MCPOAuthTokenHandler(service *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := c.Request.ParseForm(); err != nil {
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_request")
			return
		}
		form := c.Request.Form
		clientID, grantType, resource := form.Get("client_id"), form.Get("grant_type"), form.Get("resource")
		var response MCPOAuthTokenResponse
		var err error
		switch grantType {
		case "authorization_code":
			response, err = service.ExchangeCode(c.Request.Context(), clientID, form.Get("code"),
				form.Get("redirect_uri"), form.Get("code_verifier"), resource)
		case "refresh_token":
			response, err = service.Refresh(c.Request.Context(), clientID, form.Get("refresh_token"), resource)
		default:
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "unsupported_grant_type")
			return
		}
		if err != nil {
			writeMCPOAuthTokenError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		c.JSON(http.StatusOK, response)
	}
}

func MCPOAuthConsentDetailsHandler(service *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		details, err := service.ConsentDetails(c.Request.Context(), c.GetString("userID"), c.Param("transactionID"))
		if err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, details)
	}
}

func MCPOAuthConsentDecisionHandler(service *MCPOAuthService, jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request mcpOAuthConsentDecisionRequest
		if err := decodeMCPOAuthJSON(c, &request); err != nil || (request.Decision != "allow" && request.Decision != "deny") {
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_request")
			return
		}
		userID, authVersion, ok := mcpOAuthAuthenticatedWebIdentity(c, jwtSecret)
		if !ok {
			writeMCPOAuthOAuthError(c, http.StatusUnauthorized, "invalid_token")
			return
		}
		decision, err := service.DecideConsent(c.Request.Context(), userID, authVersion,
			c.Param("transactionID"), request.Decision == "allow")
		if err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"redirect_url": decision.RedirectURL})
	}
}

func MCPOAuthGrantsHandler(service *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		grants, err := service.ListGrants(c.Request.Context(), c.GetString("userID"), c.Query("project_id"))
		if err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"grants": grants})
	}
}

func MCPOAuthRevokeGrantHandler(service *MCPOAuthService, jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, authVersion, ok := mcpOAuthAuthenticatedWebIdentity(c, jwtSecret)
		if !ok {
			writeMCPOAuthOAuthError(c, http.StatusUnauthorized, "invalid_token")
			return
		}
		if err := service.RevokeGrant(c.Request.Context(), userID, authVersion, c.Param("grantID")); err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func decodeMCPOAuthJSON(c *gin.Context, target any) error {
	if c.Request.Body == nil {
		return ErrMCPOAuthBadRequest
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, mcpOAuthRequestLimit+1))
	if err != nil || len(body) == 0 || len(body) > mcpOAuthRequestLimit {
		return ErrMCPOAuthBadRequest
	}
	defer clear(body)
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrMCPOAuthBadRequest
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return ErrMCPOAuthBadRequest
	}
	return nil
}

func mcpOAuthSetSupported(values []string, supported ...string) bool {
	if len(values) == 0 {
		return true
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		found := false
		for _, expected := range supported {
			found = found || value == expected
		}
		if !found || len(values) > len(supported) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func mcpOAuthAuthenticatedWebIdentity(c *gin.Context, jwtSecret string) (string, int64, bool) {
	token, ok := bearerToken(c.GetHeader("Authorization"))
	if !ok || jwtSecret == "" {
		return "", 0, false
	}
	claims, err := ValidateToken(token, jwtSecret)
	if err != nil || claims.Sub != c.GetString("userID") || claims.TokenType != accessTokenType || claims.ClientKind != "" {
		return "", 0, false
	}
	return claims.Sub, claims.AuthVersion, true
}

func bearerToken(header string) (string, bool) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func writeMCPOAuthServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrMCPOAuthUnavailable):
		writeMCPOAuthOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable")
	case errors.Is(err, ErrMCPOAuthNotFound):
		writeMCPOAuthOAuthError(c, http.StatusNotFound, "invalid_request")
	case errors.Is(err, ErrMCPOAuthExpired):
		writeMCPOAuthOAuthError(c, http.StatusGone, "invalid_request")
	case errors.Is(err, ErrMCPOAuthDenied):
		writeMCPOAuthOAuthError(c, http.StatusForbidden, "access_denied")
	case errors.Is(err, ErrMCPOAuthInvalid), errors.Is(err, ErrAccountInactive):
		writeMCPOAuthOAuthError(c, http.StatusUnauthorized, "invalid_request")
	case errors.Is(err, ErrMCPOAuthUnsupported):
		writeMCPOAuthOAuthError(c, http.StatusBadRequest, "unsupported_response_type")
	default:
		writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_request")
	}
}

func writeMCPOAuthTokenError(c *gin.Context, err error) {
	if errors.Is(err, ErrMCPOAuthUnavailable) {
		writeMCPOAuthOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_grant")
}

func writeMCPOAuthOAuthError(c *gin.Context, status int, code string) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(status, gin.H{"error": code})
}

// RegisterMCPOAuthRoutes mounts the resource authorization endpoints on the
// existing Auth API group. Passing no Firestore client retains existing Auth
// behavior and makes the new routes fail closed with service_unavailable.
func RegisterMCPOAuthRoutes(router *gin.Engine, authRoutes *gin.RouterGroup, cfg config.Config, fs *firestore.Client) {
	var service *MCPOAuthService
	if fs != nil {
		environment := strings.TrimSpace(cfg.AuthSessionEnvironment)
		if environment == "" {
			environment = strings.TrimSpace(cfg.FirestoreDatabaseID)
		}
		if environment == "" {
			environment = "default"
		}
		frontendOrigin := ""
		if len(cfg.AllowedOrigins) > 0 {
			frontendOrigin = cfg.AllowedOrigins[0]
			if parsed, err := url.Parse(frontendOrigin); err == nil && parsed.Host != "" {
				frontendOrigin = parsed.Scheme + "://" + parsed.Host
			}
		}
		service = NewMCPOAuthService(fs, environment, cfg.AuthServiceURL, cfg.SyncServiceURL,
			frontendOrigin, cfg.JWTSecret)
	}
	router.GET("/.well-known/oauth-authorization-server", MCPOAuthMetadataHandler(cfg.AuthServiceURL))
	if service == nil {
		unavailable := func(c *gin.Context) {
			writeMCPOAuthOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable")
		}
		authRoutes.POST("/mcp/oauth/register", middleware.NewRateLimiter(10, time.Minute), unavailable)
		authRoutes.GET("/mcp/oauth/authorize", middleware.NewRateLimiter(10, time.Minute), unavailable)
		authRoutes.POST("/mcp/oauth/token", middleware.NewRateLimiter(10, time.Minute), unavailable)
		protected := authRoutes.Group("/mcp/oauth")
		protected.GET("/consent/:transactionID", unavailable)
		protected.POST("/consent/:transactionID/decision", unavailable)
		protected.GET("/grants", unavailable)
		protected.DELETE("/grants/:grantID", unavailable)
		return
	}
	authRoutes.POST("/mcp/oauth/register", middleware.NewRateLimiter(10, time.Minute), MCPOAuthRegisterHandler(service))
	authRoutes.GET("/mcp/oauth/authorize", middleware.NewRateLimiter(10, time.Minute), MCPOAuthAuthorizeHandler(service))
	authRoutes.POST("/mcp/oauth/token", middleware.NewRateLimiter(10, time.Minute), MCPOAuthTokenHandler(service))
	protected := authRoutes.Group("/mcp/oauth")
	protected.Use(JWTAuthWithAccountLookup(cfg, FirestoreAccountLookup(fs)))
	protected.GET("/consent/:transactionID", MCPOAuthConsentDetailsHandler(service))
	protected.POST("/consent/:transactionID/decision", MCPOAuthConsentDecisionHandler(service, cfg.JWTSecret))
	protected.GET("/grants", MCPOAuthGrantsHandler(service))
	protected.DELETE("/grants/:grantID", MCPOAuthRevokeGrantHandler(service, cfg.JWTSecret))
}
