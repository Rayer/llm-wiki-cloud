package auth

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/config"
	"github.com/rayer/gateauth/internal/middleware"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/idtoken"
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
func MCPOAuthMetadataHandler(issuer string, policies ...[]config.AuthOAuthResource) gin.HandlerFunc {
	issuer = trimOrigin(issuer)
	return func(c *gin.Context) {
		if issuer == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth metadata unavailable"})
			return
		}
		c.Header("Cache-Control", "no-store")
		scopes := []string{mcpOAuthScope, mcpOAuthRawWriteScope}
		if len(policies) > 0 {
			scopes = supportedOAuthScopes(policies[0])
		}
		c.JSON(http.StatusOK, gin.H{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/api/v1/auth/mcp/oauth/authorize",
			"token_endpoint":                        issuer + "/api/v1/auth/mcp/oauth/token",
			"registration_endpoint":                 issuer + "/api/v1/auth/mcp/oauth/register",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
			"scopes_supported":                      scopes,
		})
	}
}

func MCPOAuthRegisterHandler(service *MCPOAuthService) gin.HandlerFunc {
	if service == nil {
		return mcpOAuthRegisterUnavailableHandler()
	}
	return mcpOAuthRegisterHandlerWithScope(service.RegisterClient, service.normalizeRegistrationScope)
}

func mcpOAuthRegisterHandler(registerClient func(context.Context, string, []string) (mcpOAuthClientRecord, error)) gin.HandlerFunc {
	return mcpOAuthRegisterHandlerWithScope(registerClient, func(scope string) (string, bool) {
		value, _, ok := normalizeMCPOAuthScope(scope)
		return value, ok
	})
}

func mcpOAuthRegisterHandlerWithScope(registerClient func(context.Context, string, []string) (mcpOAuthClientRecord, error), normalizeScope func(string) (string, bool)) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request mcpOAuthRegistrationRequest
		shape, decodeReason := decodeMCPOAuthRegistrationJSON(c, &request)
		if decodeReason != "" {
			logMCPOAuthRegistrationRejection(c, decodeReason, request, shape)
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_client_metadata")
			return
		}
		if strings.TrimSpace(request.ClientName) == "" {
			logMCPOAuthRegistrationRejection(c, "missing_client_name", request, shape)
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_client_metadata")
			return
		}
		requestedScope, supportedScope := normalizeScope(request.Scope)
		if request.TokenEndpointAuthMethod != "" && request.TokenEndpointAuthMethod != "none" ||
			!mcpOAuthSetSupported(request.GrantTypes, "authorization_code", "refresh_token") ||
			!mcpOAuthSetSupported(request.ResponseTypes, "code") ||
			!supportedScope {
			logMCPOAuthRegistrationRejection(c, "unsupported_metadata", request, shape)
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_client_metadata")
			return
		}
		client, err := registerClient(c.Request.Context(), request.ClientName, request.RedirectURIs)
		if err != nil {
			logMCPOAuthRegistrationRejection(c, mcpOAuthRegistrationServiceReason(err), request, shape)
			writeMCPOAuthServiceError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusCreated, gin.H{
			"client_id": client.ClientID, "client_id_issued_at": client.CreatedAt.Unix(),
			"client_name": client.ClientName, "redirect_uris": client.RedirectURIs,
			"token_endpoint_auth_method": "none",
			"grant_types":                []string{"authorization_code", "refresh_token"},
			"response_types":             []string{"code"}, "scope": requestedScope,
		})
	}
}

func mcpOAuthRegisterUnavailableHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		logMCPOAuthRegistrationRejection(c, "registration_unavailable", mcpOAuthRegistrationRequest{}, mcpOAuthRegistrationShape{})
		writeMCPOAuthOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

type mcpOAuthRegistrationShape struct {
	clientNamePresent      bool
	redirectURIsPresent    bool
	tokenAuthMethodPresent bool
	grantTypesPresent      bool
	responseTypesPresent   bool
	scopePresent           bool
	unknownFieldCount      int
}

func decodeMCPOAuthRegistrationJSON(c *gin.Context, request *mcpOAuthRegistrationRequest) (mcpOAuthRegistrationShape, string) {
	if c.Request.Body == nil {
		return mcpOAuthRegistrationShape{}, "empty_body"
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, mcpOAuthRequestLimit+1))
	if err != nil {
		return mcpOAuthRegistrationShape{}, "body_read_error"
	}
	defer clear(body)
	if len(body) == 0 {
		return mcpOAuthRegistrationShape{}, "empty_body"
	}
	if len(body) > mcpOAuthRequestLimit {
		return mcpOAuthRegistrationShape{}, "body_too_large"
	}

	shape, shapeOK := scanMCPOAuthRegistrationShape(body)
	if !shapeOK {
		shape = mcpOAuthRegistrationShape{}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(request); err != nil {
		return shape, mcpOAuthRegistrationDecodeReason(err)
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return shape, "trailing_data"
	}
	return shape, ""
}

func mcpOAuthRegistrationDecodeReason(err error) string {
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		if typeError.Field == "" {
			return "invalid_json_shape"
		}
		return "known_field_type"
	}
	return "malformed_json"
}

func scanMCPOAuthRegistrationShape(body []byte) (mcpOAuthRegistrationShape, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return mcpOAuthRegistrationShape{}, false
	}
	var shape mcpOAuthRegistrationShape
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return mcpOAuthRegistrationShape{}, false
		}
		field, ok := token.(string)
		if !ok {
			return mcpOAuthRegistrationShape{}, false
		}
		switch field {
		case "client_name":
			shape.clientNamePresent = true
		case "redirect_uris":
			shape.redirectURIsPresent = true
		case "token_endpoint_auth_method":
			shape.tokenAuthMethodPresent = true
		case "grant_types":
			shape.grantTypesPresent = true
		case "response_types":
			shape.responseTypesPresent = true
		case "scope":
			shape.scopePresent = true
		default:
			if shape.unknownFieldCount < 1024 {
				shape.unknownFieldCount++
			}
		}
		if err := skipMCPOAuthJSONValue(decoder); err != nil {
			return mcpOAuthRegistrationShape{}, false
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return mcpOAuthRegistrationShape{}, false
	}
	return shape, true
}

func skipMCPOAuthJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		for decoder.More() {
			if _, err := decoder.Token(); err != nil {
				return err
			}
			if err := skipMCPOAuthJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := skipMCPOAuthJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func logMCPOAuthRegistrationRejection(c *gin.Context, reason string, request mcpOAuthRegistrationRequest, shape mcpOAuthRegistrationShape) {
	log.Printf("mcp_oauth_registration_rejected reason=%s trace_id=%s client_name_present=%t redirect_uris_present=%t redirect_count=%d token_auth_method_present=%t grant_types_present=%t grant_type_count=%d response_types_present=%t response_type_count=%d scope_present=%t unknown_field_count=%d redirect_scheme_category=%s",
		reason, mcpOAuthRequestTraceID(c), shape.clientNamePresent, shape.redirectURIsPresent,
		boundedMCPOAuthDiagnosticCount(len(request.RedirectURIs), 32), shape.tokenAuthMethodPresent,
		shape.grantTypesPresent, boundedMCPOAuthDiagnosticCount(len(request.GrantTypes), 32),
		shape.responseTypesPresent, boundedMCPOAuthDiagnosticCount(len(request.ResponseTypes), 32),
		shape.scopePresent, shape.unknownFieldCount, mcpOAuthRedirectSchemeCategory(request.RedirectURIs))
}

func boundedMCPOAuthDiagnosticCount(value, limit int) int {
	if value > limit {
		return limit
	}
	return value
}

func mcpOAuthRequestTraceID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return "none"
	}
	spanContext := trace.SpanFromContext(c.Request.Context()).SpanContext()
	if spanContext.IsValid() {
		return spanContext.TraceID().String()
	}
	traceID, _, _ := strings.Cut(c.GetHeader("X-Cloud-Trace-Context"), "/")
	if len(traceID) != 32 {
		return "none"
	}
	decoded, err := hex.DecodeString(traceID)
	if err != nil {
		return "none"
	}
	return hex.EncodeToString(decoded)
}

func mcpOAuthRegistrationServiceReason(err error) string {
	if errors.Is(err, ErrMCPOAuthUnavailable) {
		return "registration_unavailable"
	}
	if errors.Is(err, ErrMCPOAuthBadRequest) {
		return "registration_rejected"
	}
	return "registration_error"
}

func mcpOAuthRedirectSchemeCategory(redirectURIs []string) string {
	if len(redirectURIs) == 0 {
		return "none"
	}
	category := ""
	for _, redirectURI := range redirectURIs {
		parsed, err := url.Parse(redirectURI)
		current := "invalid"
		if err == nil && parsed.Scheme != "" {
			switch strings.ToLower(parsed.Scheme) {
			case "http":
				host := strings.ToLower(parsed.Hostname())
				if host == "localhost" || host == "127.0.0.1" || host == "::1" {
					current = "http_loopback"
				} else {
					current = "http_other"
				}
			case "https":
				current = "https"
			default:
				current = "custom"
			}
		}
		if current == "custom" {
			return current
		}
		if category == "" {
			category = current
		} else if category != current {
			category = "mixed"
		}
	}
	return category
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
		userID, authVersion, ok := mcpOAuthAuthenticatedWebIdentity(c, service.jwtSecret)
		if !ok {
			writeMCPOAuthOAuthError(c, http.StatusUnauthorized, "invalid_token")
			return
		}
		classification, err := service.PublicConsentClassification(c.Request.Context(), userID, authVersion, c.Param("transactionID"))
		if err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		if classification.ResourceKind == "project-template" {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusOK, classification)
			return
		}
		details, err := service.ConsentDetails(c.Request.Context(), userID, authVersion, c.Param("transactionID"))
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
		if err := decodeMCPOAuthJSON(c, &request); err != nil ||
			(request.Decision != "allow" && request.Decision != "deny" && request.Decision != "continue") {
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_request")
			return
		}
		userID, authVersion, ok := mcpOAuthAuthenticatedWebIdentity(c, jwtSecret)
		if !ok {
			writeMCPOAuthOAuthError(c, http.StatusUnauthorized, "invalid_token")
			return
		}
		classification, err := service.PublicConsentClassification(c.Request.Context(), userID, authVersion, c.Param("transactionID"))
		if err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		if classification.ResourceKind == "project-template" {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusForbidden, gin.H{"error": "product_decision_required"})
			return
		}
		var decision mcpOAuthDecision
		if request.Decision == "continue" {
			decision, err = service.ContinueConsent(c.Request.Context(), userID, authVersion, c.Param("transactionID"))
		} else {
			decision, err = service.DecideConsent(c.Request.Context(), userID, authVersion,
				c.Param("transactionID"), request.Decision == "allow")
		}
		if err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"redirect_url": decision.RedirectURL, "requires_consent": decision.RequiresConsent})
	}
}

func MCPOAuthGrantsHandler(service *MCPOAuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Request.URL.Query()
		if len(query) != 1 {
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_request")
			return
		}
		projectValues, hasProject := query["project_id"]
		resourceValues, hasResource := query["resource"]
		if hasProject == hasResource {
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_request")
			return
		}
		if hasProject {
			if len(projectValues) != 1 || projectValues[0] == "" {
				writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_request")
				return
			}
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusForbidden, gin.H{"error": "product_decision_required"})
			return
		}
		var grants []MCPOAuthGrantInfo
		var err error
		if hasProject && len(projectValues) == 1 && projectValues[0] != "" {
			grants, err = service.ListGrants(c.Request.Context(), c.GetString("userID"), projectValues[0])
		} else if hasResource && len(resourceValues) == 1 && resourceValues[0] != "" {
			grants, err = service.ListResourceGrants(c.Request.Context(), c.GetString("userID"), resourceValues[0])
		} else {
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_request")
			return
		}
		if err != nil {
			writeMCPOAuthServiceError(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"grants": grants})
	}
}

func MCPOAuthCredentialVerifierHandler(service *MCPOAuthService, cfg config.Config) gin.HandlerFunc {
	return MCPOAuthCredentialVerifierHandlerWithValidator(service, cfg, idtoken.Validate)
}

func MCPOAuthCredentialVerifierHandlerWithValidator(service *MCPOAuthService, cfg config.Config,
	validate func(context.Context, string, string) (*idtoken.Payload, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		var policy *config.AuthOAuthResource
		for i := range cfg.AuthOAuthResources {
			if cfg.AuthOAuthResources[i].ID == "daily-brief" {
				policy = &cfg.AuthOAuthResources[i]
				break
			}
		}
		if service == nil || policy == nil || !policy.Enabled || policy.VerifierCallerServiceAccount == "" ||
			policy.VerifierAudience == "" || validate == nil {
			writeMCPOAuthOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable")
			return
		}
		callerToken, ok := bearerToken(c.GetHeader("Authorization"))
		userCredential := c.GetHeader("X-LWC-User-Credential")
		bodyPresent := false
		if c.Request.Body != nil {
			body, bodyErr := io.ReadAll(io.LimitReader(c.Request.Body, 1))
			bodyPresent = bodyErr != nil || len(body) != 0
			clear(body)
		}
		if !ok || !validMCPOpaque(userCredential, mcpOAuthAccessPrefix) || c.GetHeader("X-LWC-Credential-Class") != "" || bodyPresent {
			writeMCPOAuthOAuthError(c, http.StatusBadRequest, "invalid_request")
			return
		}
		payload, err := validate(c.Request.Context(), callerToken, policy.VerifierAudience)
		if err != nil || payload == nil || payload.Issuer != "https://accounts.google.com" ||
			payload.Audience != policy.VerifierAudience || payload.Expires <= time.Now().Unix() {
			writeMCPOAuthOAuthError(c, http.StatusForbidden, "untrusted_caller")
			return
		}
		email, _ := payload.Claims["email"].(string)
		verifiedEmail, _ := payload.Claims["email_verified"].(bool)
		if !verifiedEmail || email != policy.VerifierCallerServiceAccount {
			writeMCPOAuthOAuthError(c, http.StatusForbidden, "untrusted_caller")
			return
		}
		result, err := service.VerifyAccessToken(c.Request.Context(), userCredential)
		if err != nil {
			if errors.Is(err, ErrMCPOAuthUnavailable) {
				writeMCPOAuthOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable")
			} else {
				writeMCPOAuthOAuthError(c, http.StatusUnauthorized, "invalid_token")
			}
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func supportedOAuthScopes(policies []config.AuthOAuthResource) []string {
	seen := map[string]bool{}
	var scopes []string
	for _, resource := range policies {
		if !resource.Enabled {
			continue
		}
		for _, scope := range resource.Scopes {
			if !seen[scope.Name] {
				seen[scope.Name] = true
				scopes = append(scopes, scope.Name)
			}
		}
	}
	return scopes
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
	resources := cfg.AuthOAuthResources
	if resources == nil {
		resources = config.LegacyAuthOAuthResources(cfg.SyncServiceURL)
	}
	cfg.AuthOAuthResources = resources
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
		service = NewMCPOAuthServiceWithPolicy(fs, environment, cfg.AuthEnvironment, cfg.AuthServiceURL, cfg.SyncServiceURL,
			frontendOrigin, cfg.JWTSecret, resources)
	}
	router.GET("/.well-known/oauth-authorization-server", MCPOAuthMetadataHandler(cfg.AuthServiceURL, cfg.AuthOAuthResources))
	if service == nil {
		unavailable := func(c *gin.Context) {
			writeMCPOAuthOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable")
		}
		authRoutes.POST("/mcp/oauth/register", middleware.NewRateLimiter(10, time.Minute), mcpOAuthRegisterUnavailableHandler())
		authRoutes.GET("/mcp/oauth/authorize", middleware.NewRateLimiter(10, time.Minute), unavailable)
		authRoutes.POST("/mcp/oauth/token", middleware.NewRateLimiter(10, time.Minute), unavailable)
		authRoutes.POST("/consumers/daily-brief/verify-credential", middleware.NewRateLimiter(60, time.Minute), MCPOAuthCredentialVerifierHandler(nil, cfg))
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
	authRoutes.POST("/consumers/daily-brief/verify-credential", middleware.NewRateLimiter(60, time.Minute), MCPOAuthCredentialVerifierHandler(service, cfg))
	protected := authRoutes.Group("/mcp/oauth")
	protected.Use(JWTAuthWithAccountLookup(cfg, FirestoreAccountLookup(fs)))
	protected.GET("/consent/:transactionID", MCPOAuthConsentDetailsHandler(service))
	protected.POST("/consent/:transactionID/decision", MCPOAuthConsentDecisionHandler(service, cfg.JWTSecret))
	protected.GET("/grants", MCPOAuthGrantsHandler(service))
	protected.DELETE("/grants/:grantID", MCPOAuthRevokeGrantHandler(service, cfg.JWTSecret))
}
