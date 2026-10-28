package v1

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
)

type mcpProjectConsentDecisionRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Decision      string `json:"decision"`
}

func (h *Handler) MCPProjectConsentDetails(c *gin.Context) {
	transactionID := c.Param("transactionID")
	identity, consumer, ok := h.mcpConsentContext(c)
	if !ok || !validMCPTransactionID(transactionID) {
		if ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		}
		return
	}
	lookup, err := consumer.LookupMCPConsent(c.Request.Context(), requestUserCredential(c), transactionID, identity.Subject, identity.AuthVersion)
	if err != nil {
		writeMCPConsentConsumerError(c, err)
		return
	}
	if lookup.ResourceKind != "project-template" {
		c.JSON(http.StatusConflict, gin.H{"error": "wrong_resource_kind"})
		return
	}
	if lookup.Status == "completed" {
		if lookup.DecisionResult == nil || lookup.DecisionResult.RequiresConsent || lookup.DecisionResult.RedirectURL == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "transaction_result_unavailable"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "status": "completed",
			"redirect_url": lookup.DecisionResult.RedirectURL, "requires_consent": false})
		return
	}
	if lookup.Status != "pending" || !auth.ValidPathSegment(lookup.ProjectID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "transaction_not_found"})
		return
	}
	project, err := h.authorizedMCPProject(c, identity.Subject, lookup.ProjectID)
	if err != nil {
		writeMCPProjectAuthorizationError(c, err)
		return
	}
	scope := strings.Join(lookup.RequestedScopes, " ")
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"schema_version": 1, "status": "pending", "transaction_id": transactionID,
		"can_continue": lookup.CanContinue, "account_email": lookup.AccountEmail,
		"project_id": lookup.ProjectID, "project_name": project.Name,
		"client_name": lookup.ClientName, "resource": lookup.Resource,
		"resource_kind": "project", "resource_name": project.Name,
		"scope_details": []any{}, "scope": scope, "expires_at": lookup.ExpiresAt,
	})
}

func (h *Handler) MCPProjectConsentDecision(c *gin.Context) {
	transactionID := c.Param("transactionID")
	identity, consumer, ok := h.mcpConsentContext(c)
	if !ok || !validMCPTransactionID(transactionID) {
		if ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		}
		return
	}
	var request mcpProjectConsentDecisionRequest
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.SchemaVersion != 1 ||
		(request.Decision != "allow" && request.Decision != "deny" && request.Decision != "continue") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	lookup, err := consumer.LookupMCPConsent(c.Request.Context(), requestUserCredential(c), transactionID, identity.Subject, identity.AuthVersion)
	if err != nil {
		writeMCPConsentConsumerError(c, err)
		return
	}
	if lookup.ResourceKind != "project-template" {
		c.JSON(http.StatusConflict, gin.H{"error": "wrong_resource_kind"})
		return
	}
	if lookup.Status == "pending" {
		if request.Decision != "deny" {
			if !auth.ValidPathSegment(lookup.ProjectID) {
				c.JSON(http.StatusNotFound, gin.H{"error": "transaction_not_found"})
				return
			}
			if _, err := h.authorizedMCPProject(c, identity.Subject, lookup.ProjectID); err != nil {
				writeMCPProjectAuthorizationError(c, err)
				return
			}
		}
	} else if lookup.Status != "completed" {
		c.JSON(http.StatusNotFound, gin.H{"error": "transaction_not_found"})
		return
	}
	approvedScopes := []string{}
	if request.Decision == "allow" || request.Decision == "continue" {
		approvedScopes = lookup.RequestedScopes
	}
	result, err := consumer.DecideMCPConsent(c.Request.Context(), requestUserCredential(c), authconsumer.MCPConsentDecisionRequest{
		TransactionID: transactionID, Subject: identity.Subject, AuthVersion: identity.AuthVersion,
		Decision: request.Decision, ApprovedScopes: approvedScopes,
	})
	if err != nil {
		writeMCPConsentConsumerError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"redirect_url": result.RedirectURL, "requires_consent": result.RequiresConsent})
}

func (h *Handler) MCPProjectGrants(c *gin.Context) {
	identity, consumer, ok := h.mcpConsentContext(c)
	projectID := strings.TrimSpace(c.Query("project_id"))
	if !ok {
		return
	}
	if projectID == "" || len(c.Request.URL.Query()) != 1 || !auth.ValidPathSegment(projectID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if _, err := h.authorizedMCPProject(c, identity.Subject, projectID); err != nil {
		writeMCPProjectAuthorizationError(c, err)
		return
	}
	grants, err := consumer.ListProjectMCPGrants(c.Request.Context(), requestUserCredential(c), identity.Subject, identity.AuthVersion, projectID)
	if err != nil {
		writeMCPConsentConsumerError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"grants": grants})
}

func (h *Handler) mcpConsentContext(c *gin.Context) (authconsumer.Identity, mcpConsentConsumer, bool) {
	raw, exists := c.Get("authConsumerIdentity")
	identity, ok := raw.(authconsumer.Identity)
	consumer, hasConsumer := h.consumerVerifier.(mcpConsentConsumer)
	if !exists || !ok || !hasConsumer || strings.TrimSpace(requestUserCredential(c)) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP consent authority unavailable"})
		return authconsumer.Identity{}, nil, false
	}
	if identity.CredentialKind != "web-access" && identity.CredentialKind != "cli-access" {
		c.JSON(http.StatusForbidden, gin.H{"error": "credential kind is not allowed"})
		return authconsumer.Identity{}, nil, false
	}
	return identity, consumer, true
}

func (h *Handler) authorizedMCPProject(c *gin.Context, subject, projectID string) (struct {
	ID   string
	Name string
}, error) {
	if h.cliProjectAuthorizer == nil || h.firestore == nil || h.firestore.Raw() == nil {
		return struct {
			ID   string
			Name string
		}{}, auth.ErrProjectPermissionUnavailable
	}
	if err := h.cliProjectAuthorizer(c.Request.Context(), subject, projectID); err != nil {
		return struct {
			ID   string
			Name string
		}{}, err
	}
	projects, err := h.listFirestoreProjects(c.Request.Context(), subject)
	if err != nil {
		return struct {
			ID   string
			Name string
		}{}, auth.ErrProjectPermissionUnavailable
	}
	for _, project := range projects {
		if project.ID == projectID {
			return struct {
				ID   string
				Name string
			}{ID: project.ID, Name: project.Name}, nil
		}
	}
	return struct {
		ID   string
		Name string
	}{}, auth.ErrProjectPermissionDenied
}

func writeMCPProjectAuthorizationError(c *gin.Context, err error) {
	if errors.Is(err, auth.ErrProjectPermissionDenied) {
		c.JSON(http.StatusForbidden, gin.H{"error": "project access denied"})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "project access unavailable"})
}

func writeMCPConsentConsumerError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, authconsumer.ErrInvalidCredential), errors.Is(err, authconsumer.ErrInactiveAccount):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
	case errors.Is(err, authconsumer.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "not allowed"})
	case errors.Is(err, authconsumer.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "transaction_not_found"})
	case errors.Is(err, authconsumer.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "decision_conflict"})
	case errors.Is(err, authconsumer.ErrInvalidRequest):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP consent authority unavailable"})
	}
}

func validMCPTransactionID(value string) bool {
	const prefix = "lwc_mcp_tx_"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+43 {
		return false
	}
	encoded := strings.TrimPrefix(value, prefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == encoded
}
