package authconsumer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/api/idtoken"
)

const (
	verifyPath              = "/api/v1/auth/consumers/lwc/verify-credential"
	checkPath               = "/api/v1/auth/consumers/lwc/accounts/check"
	maxBody                 = 64 << 10
	LocalFixtureOIDCToken   = "gateauth-local-fixture:oidc-v1"
	LocalFixtureCallerEmail = "bff-local@localhost"
	onboardingIdentityPath  = "/api/v1/auth/consumers/lwc/onboarding/identity"
)

var (
	ErrInvalidCredential = errors.New("user credential is invalid")
	ErrInactiveAccount   = errors.New("account is inactive or missing")
	ErrForbidden         = errors.New("Auth consumer denied the requested action")
	ErrConflict          = errors.New("Auth consumer rejected a conflicting operation")
	ErrExpired           = errors.New("Auth consumer transaction expired")
	ErrInvalidRequest    = errors.New("Auth consumer rejected the request")
	ErrNotFound          = errors.New("Auth management operation was not found")
	ErrUnavailable       = errors.New("Auth consumer is unavailable")
)

type Config struct {
	BaseURL          string
	Environment      string
	Issuer           string
	Resource         string
	Audience         string
	VerifierPath     string
	AccountCheckPath string
	CatalogPath      string
	Timeout          time.Duration
}

type Identity struct {
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

type Account struct {
	SchemaVersion int    `json:"schema_version"`
	Subject       string `json:"subject"`
	Active        bool   `json:"active"`
	AuthVersion   int64  `json:"auth_version"`
}

type OnboardingIdentity struct {
	SchemaVersion    int    `json:"schema_version"`
	Subject          string `json:"subject"`
	AuthVersion      int64  `json:"auth_version"`
	DefaultProjectID string `json:"default_project_id"`
}

type MCPConsentResult struct {
	RedirectURL     string `json:"redirect_url"`
	RequiresConsent bool   `json:"requires_consent"`
}

type MCPConsentTransaction struct {
	SchemaVersion   int               `json:"schema_version"`
	Status          string            `json:"status"`
	ResourceKind    string            `json:"resource_kind"`
	TransactionID   string            `json:"transaction_id"`
	Resource        string            `json:"resource"`
	ProjectID       string            `json:"project_id,omitempty"`
	RequestedScopes []string          `json:"requested_scopes,omitempty"`
	ExpiresAt       string            `json:"expires_at,omitempty"`
	CanContinue     bool              `json:"can_continue"`
	ClientName      string            `json:"client_name,omitempty"`
	AccountEmail    string            `json:"account_email,omitempty"`
	DecisionResult  *MCPConsentResult `json:"decision_result,omitempty"`
}

type MCPConsentDecisionResult struct {
	SchemaVersion   int    `json:"schema_version"`
	Status          string `json:"status"`
	RedirectURL     string `json:"redirect_url"`
	RequiresConsent bool   `json:"requires_consent"`
}

type MCPGrant struct {
	ID         string     `json:"id"`
	ClientName string     `json:"client_name"`
	ProjectID  string     `json:"project_id"`
	Resource   string     `json:"resource"`
	Scope      string     `json:"scope"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type ManagementUser struct {
	ID          string                     `json:"id"`
	Name        string                     `json:"name"`
	Email       string                     `json:"email"`
	Role        string                     `json:"role,omitempty"`
	Status      string                     `json:"status"`
	ScopeStates map[string]ManagementScope `json:"scope_states"`
}

type RegistrationSettings struct {
	RegistrationEnabled       bool `json:"registration_enabled"`
	EmailRegistrationEnabled  bool `json:"email_registration_enabled"`
	GoogleRegistrationEnabled bool `json:"google_registration_enabled"`
}

type RegistrationSettingsPatch struct {
	RegistrationEnabled       *bool `json:"registration_enabled,omitempty"`
	EmailRegistrationEnabled  *bool `json:"email_registration_enabled,omitempty"`
	GoogleRegistrationEnabled *bool `json:"google_registration_enabled,omitempty"`
}

type ManagementScope struct {
	State     string    `json:"state"`
	Revision  int64     `json:"revision"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ManagementMeResult struct {
	SchemaVersion    int              `json:"schema_version"`
	Subject          string           `json:"subject"`
	GrantedScopes    []string         `json:"granted_scopes"`
	CredentialScopes map[string]int64 `json:"credential_scopes"`
}

type ManagementCatalogScope struct {
	ID          string            `json:"id"`
	Product     string            `json:"product"`
	Resource    string            `json:"resource"`
	GrantMode   string            `json:"grant_mode"`
	DisplayName map[string]string `json:"display_name"`
	Description map[string]string `json:"description"`
}

type ManagementCatalog struct {
	SchemaVersion int                      `json:"schema_version"`
	Issuer        string                   `json:"issuer"`
	Resource      string                   `json:"resource"`
	Scopes        []ManagementCatalogScope `json:"scopes"`
}

type ManagementGrantRequest struct {
	SchemaVersion int    `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	TargetUserID  string `json:"target_user_id"`
	Scope         string `json:"scope"`
	Action        string `json:"action"`
}

type ManagementMutationResult struct {
	OperationID string `json:"operation_id"`
	TargetID    string `json:"target_user_id"`
	Scope       string `json:"scope"`
	State       string `json:"state"`
	Revision    int64  `json:"revision"`
}

type AccountMutationResult struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	ID            string `json:"id"`
}

type RuntimeTokenSource func(context.Context, string) (string, error)

type Client struct {
	cfg        Config
	httpClient *http.Client
	token      RuntimeTokenSource
}

func New(cfg Config, httpClient *http.Client, tokenSource RuntimeTokenSource) (*Client, error) {
	if cfg.VerifierPath == "" {
		cfg.VerifierPath = "/api/v1/auth/consumers/lwc/verify-credential"
	}
	if cfg.AccountCheckPath == "" {
		cfg.AccountCheckPath = "/api/v1/auth/consumers/lwc/accounts/check"
	}
	if cfg.CatalogPath == "" {
		cfg.CatalogPath = "/api/v1/auth/consumers/lwc/management/catalog"
	}
	base, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "https" && base.Scheme != "http") || strings.TrimSpace(cfg.Environment) == "" ||
		strings.TrimSpace(cfg.Resource) == "" || strings.TrimSpace(cfg.Audience) == "" {
		return nil, errors.New("Auth consumer config is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 5*time.Second {
		return nil, errors.New("Auth consumer timeout is invalid")
	}
	if cfg.VerifierPath != "/api/v1/auth/consumers/lwc/verify-credential" ||
		cfg.AccountCheckPath != "/api/v1/auth/consumers/lwc/accounts/check" ||
		cfg.CatalogPath != "/api/v1/auth/consumers/lwc/management/catalog" {
		return nil, errors.New("Auth consumer endpoint locator is invalid")
	}
	if tokenSource == nil {
		return nil, errors.New("Auth runtime token source is required")
	}
	return &Client{cfg: cfg, httpClient: httpClient, token: tokenSource}, nil
}

// NewGoogleRuntime uses the BFF's ambient runtime identity to mint the exact
// Auth audience token. It does not read key files or create service accounts.
func NewGoogleRuntime(_ context.Context, cfg Config, httpClient *http.Client) (*Client, error) {
	return New(cfg, httpClient, func(callCtx context.Context, requestedAudience string) (string, error) {
		source, err := idtoken.NewTokenSource(callCtx, requestedAudience)
		if err != nil {
			return "", err
		}
		token, err := source.Token()
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	})
}

func (c *Client) Verify(ctx context.Context, credential, projectID string) (Identity, error) {
	if c == nil {
		return Identity{}, ErrUnavailable
	}
	return c.VerifyResource(ctx, credential, c.cfg.Resource, projectID)
}

// VerifyResource binds an opaque Project OAuth credential to the exact
// protected-resource URL selected by the BFF route. Ordinary Web and CLI
// credentials continue to use the configured BFF resource.
func (c *Client) VerifyResource(ctx context.Context, credential, resource, projectID string) (Identity, error) {
	var identity Identity
	if c == nil || strings.TrimSpace(credential) == "" || strings.TrimSpace(resource) == "" {
		return identity, ErrUnavailable
	}
	body := struct {
		SchemaVersion int    `json:"schema_version"`
		Resource      string `json:"resource"`
		ProjectID     string `json:"project_id,omitempty"`
	}{SchemaVersion: 1, Resource: resource, ProjectID: projectID}
	if err := c.request(ctx, http.MethodPost, c.cfg.VerifierPath, credential, body, &identity); err != nil {
		return Identity{}, err
	}
	if identity.SchemaVersion != 1 || strings.TrimSpace(identity.Subject) == "" ||
		identity.Environment != c.cfg.Environment || identity.Resource != resource ||
		(c.cfg.Issuer != "" && identity.Issuer != c.cfg.Issuer) ||
		identity.ExpiresAt <= time.Now().Unix() || !validKind(identity.CredentialKind) ||
		identity.Scopes == nil || identity.ManagementScopes == nil {
		return Identity{}, ErrUnavailable
	}
	switch identity.CredentialKind {
	case "web-access":
		if resource != c.cfg.Resource || identity.SessionID != "" || identity.GrantID != "" || identity.ClientID != "" || identity.ProjectID != "" {
			return Identity{}, ErrUnavailable
		}
	case "cli-access":
		if resource != c.cfg.Resource || identity.SessionID == "" || identity.GrantID != "" || identity.ClientID != "" || identity.ProjectID != "" {
			return Identity{}, ErrUnavailable
		}
	case "mcp-oauth":
		if projectID == "" || identity.ProjectID != projectID || identity.GrantID == "" || identity.ClientID == "" ||
			identity.SessionID != "" || len(identity.ManagementScopes) != 0 {
			return Identity{}, ErrUnavailable
		}
	}
	return identity, nil
}

func (c *Client) CheckAccount(ctx context.Context, userID string) (Account, error) {
	var account Account
	if c == nil || strings.TrimSpace(userID) == "" {
		return account, ErrUnavailable
	}
	body := struct {
		SchemaVersion int    `json:"schema_version"`
		UserID        string `json:"user_id"`
	}{SchemaVersion: 1, UserID: userID}
	if err := c.request(ctx, http.MethodPost, c.cfg.AccountCheckPath, "", body, &account); err != nil {
		if errors.Is(err, ErrInvalidCredential) {
			return Account{}, ErrInactiveAccount
		}
		return Account{}, err
	}
	if account.SchemaVersion != 1 || account.Subject != userID || !account.Active {
		return Account{}, ErrUnavailable
	}
	return account, nil
}

func (c *Client) OnboardingIdentity(ctx context.Context, credential string) (OnboardingIdentity, error) {
	var identity OnboardingIdentity
	if strings.TrimSpace(credential) == "" {
		return identity, ErrInvalidCredential
	}
	request := struct {
		SchemaVersion int `json:"schema_version"`
	}{SchemaVersion: 1}
	if err := c.request(ctx, http.MethodPost, onboardingIdentityPath, credential, request, &identity); err != nil {
		return OnboardingIdentity{}, err
	}
	if identity.SchemaVersion != 1 || !validPathSegment(identity.Subject) || identity.AuthVersion < 0 ||
		!validPathSegment(identity.DefaultProjectID) {
		return OnboardingIdentity{}, ErrUnavailable
	}
	return identity, nil
}

func (c *Client) LookupMCPConsent(ctx context.Context, credential, transactionID, subject string, authVersion int64) (MCPConsentTransaction, error) {
	var result MCPConsentTransaction
	if !validPathSegment(transactionID) || !validPathSegment(subject) || authVersion < 0 {
		return result, ErrInvalidRequest
	}
	body := struct {
		SchemaVersion int    `json:"schema_version"`
		TransactionID string `json:"transaction_id"`
		Subject       string `json:"subject"`
		AuthVersion   int64  `json:"auth_version"`
	}{1, transactionID, subject, authVersion}
	if err := c.request(ctx, http.MethodPost, "/api/v1/auth/consumers/lwc/mcp-consent/transactions/lookup", credential, body, &result); err != nil {
		return MCPConsentTransaction{}, err
	}
	if result.SchemaVersion != 1 || result.TransactionID != transactionID || result.Resource == "" ||
		result.ResourceKind != "project-template" || (result.Status != "pending" && result.Status != "completed") {
		return MCPConsentTransaction{}, ErrUnavailable
	}
	if result.Status == "pending" && (!validPathSegment(result.ProjectID) || len(result.RequestedScopes) == 0 ||
		result.ClientName == "" || result.AccountEmail == "" || result.ExpiresAt == "") {
		return MCPConsentTransaction{}, ErrUnavailable
	}
	if result.Status == "completed" && (result.DecisionResult == nil || result.DecisionResult.RequiresConsent || result.DecisionResult.RedirectURL == "") {
		return MCPConsentTransaction{}, ErrUnavailable
	}
	return result, nil
}

func (c *Client) DecideMCPConsent(ctx context.Context, credential string, request MCPConsentDecisionRequest) (MCPConsentDecisionResult, error) {
	var result MCPConsentDecisionResult
	if !validPathSegment(request.TransactionID) || !validPathSegment(request.Subject) || request.AuthVersion < 0 ||
		(request.Decision != "allow" && request.Decision != "deny" && request.Decision != "continue") {
		return result, ErrInvalidRequest
	}
	body := struct {
		SchemaVersion  int      `json:"schema_version"`
		Subject        string   `json:"subject"`
		AuthVersion    int64    `json:"auth_version"`
		Decision       string   `json:"decision"`
		ApprovedScopes []string `json:"approved_scopes"`
	}{1, request.Subject, request.AuthVersion, request.Decision, request.ApprovedScopes}
	path := "/api/v1/auth/consumers/lwc/mcp-consent/transactions/" + url.PathEscape(request.TransactionID) + "/decision"
	if err := c.request(ctx, http.MethodPost, path, credential, body, &result); err != nil {
		return MCPConsentDecisionResult{}, err
	}
	if result.SchemaVersion != 1 || (result.Status != "completed" && result.Status != "pending") ||
		(result.Status == "pending" && (!result.RequiresConsent || result.RedirectURL != "")) ||
		(result.Status == "completed" && (result.RequiresConsent || result.RedirectURL == "")) {
		return MCPConsentDecisionResult{}, ErrUnavailable
	}
	return result, nil
}

type MCPConsentDecisionRequest struct {
	TransactionID  string
	Subject        string
	AuthVersion    int64
	Decision       string
	ApprovedScopes []string
}

func (c *Client) ListProjectMCPGrants(ctx context.Context, credential, subject string, authVersion int64, projectID string) ([]MCPGrant, error) {
	var response struct {
		SchemaVersion int        `json:"schema_version"`
		Grants        []MCPGrant `json:"grants"`
	}
	if !validPathSegment(subject) || authVersion < 0 || !validPathSegment(projectID) {
		return nil, ErrInvalidRequest
	}
	body := struct {
		SchemaVersion int    `json:"schema_version"`
		Subject       string `json:"subject"`
		AuthVersion   int64  `json:"auth_version"`
		ProjectID     string `json:"project_id"`
	}{1, subject, authVersion, projectID}
	if err := c.request(ctx, http.MethodPost, "/api/v1/auth/consumers/lwc/mcp-consent/grants/list", credential, body, &response); err != nil {
		return nil, err
	}
	if response.SchemaVersion != 1 || response.Grants == nil {
		return nil, ErrUnavailable
	}
	for _, grant := range response.Grants {
		if grant.ID == "" || grant.ProjectID != projectID || grant.Resource == "" || grant.Scope == "" {
			return nil, ErrUnavailable
		}
	}
	return response.Grants, nil
}

// PublicRegistrationSettings reads dynamic self-service policy from Auth.
func (c *Client) PublicRegistrationSettings(ctx context.Context) (RegistrationSettings, error) {
	return c.registrationSettings(ctx, "GET", "/api/v1/auth/consumers/lwc/registration/settings", "", nil)
}

// ManagementRegistrationSettings reads registration policy for an authenticated Auth administrator.
func (c *Client) ManagementRegistrationSettings(ctx context.Context, credential string) (RegistrationSettings, error) {
	return c.registrationSettings(ctx, http.MethodGet, "/api/v1/auth/consumers/lwc/management/registration-settings", credential, nil)
}

func (c *Client) UpdateManagementRegistrationSettings(ctx context.Context, credential string, patch RegistrationSettingsPatch) (RegistrationSettings, error) {
	if patch.RegistrationEnabled == nil && patch.EmailRegistrationEnabled == nil && patch.GoogleRegistrationEnabled == nil {
		return RegistrationSettings{}, ErrInvalidRequest
	}
	return c.registrationSettings(ctx, http.MethodPatch, "/api/v1/auth/consumers/lwc/management/registration-settings", credential,
		struct {
			SchemaVersion int `json:"schema_version"`
			RegistrationSettingsPatch
		}{SchemaVersion: 1, RegistrationSettingsPatch: patch})
}

func (c *Client) registrationSettings(ctx context.Context, method, path, credential string, requestBody any) (RegistrationSettings, error) {
	var response struct {
		SchemaVersion int `json:"schema_version"`
		RegistrationSettings
	}
	if err := c.request(ctx, method, path, credential, requestBody, &response); err != nil {
		return RegistrationSettings{}, err
	}
	if response.SchemaVersion != 1 {
		return RegistrationSettings{}, ErrUnavailable
	}
	return response.RegistrationSettings, nil
}

func (c *Client) UpdateLegacyAccount(ctx context.Context, credential, userID string, role, state *string) error {
	if !validPathSegment(userID) || (role == nil && state == nil) {
		return ErrInvalidRequest
	}
	var result AccountMutationResult
	path := "/api/v1/auth/consumers/lwc/management/users/" + url.PathEscape(userID) + "/legacy"
	body := struct {
		SchemaVersion int     `json:"schema_version"`
		Role          *string `json:"role,omitempty"`
		Status        *string `json:"status,omitempty"`
	}{SchemaVersion: 1, Role: role, Status: state}
	if err := c.request(ctx, http.MethodPatch, path, credential, body, &result); err != nil {
		return err
	}
	if result.SchemaVersion != 1 || result.Status != "updated" || result.ID != userID {
		return ErrUnavailable
	}
	return nil
}

func (c *Client) ManagementUsers(ctx context.Context, credential string) ([]ManagementUser, error) {
	var response struct {
		SchemaVersion int              `json:"schema_version"`
		Users         []ManagementUser `json:"users"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/v1/auth/consumers/lwc/management/users", credential, nil, &response); err != nil {
		return nil, err
	}
	if response.SchemaVersion != 1 || response.Users == nil {
		return nil, ErrUnavailable
	}
	return response.Users, nil
}

func (c *Client) ManagementMe(ctx context.Context, credential string) (ManagementMeResult, error) {
	var response ManagementMeResult
	if err := c.request(ctx, http.MethodGet, "/api/v1/auth/consumers/lwc/management/me", credential, nil, &response); err != nil {
		return ManagementMeResult{}, err
	}
	if response.SchemaVersion != 1 || response.Subject == "" || response.GrantedScopes == nil || response.CredentialScopes == nil {
		return ManagementMeResult{}, ErrUnavailable
	}
	return response, nil
}

func (c *Client) ManagementCatalog(ctx context.Context, credential string) (ManagementCatalog, error) {
	var result ManagementCatalog
	if err := c.request(ctx, http.MethodGet, c.cfg.CatalogPath, credential, nil, &result); err != nil {
		return ManagementCatalog{}, err
	}
	if result.SchemaVersion != 1 || result.Issuer != c.cfg.Issuer || result.Resource != c.cfg.Resource || len(result.Scopes) == 0 {
		return ManagementCatalog{}, ErrUnavailable
	}
	for _, scope := range result.Scopes {
		if (scope.ID != "auth:admin" && scope.ID != "lwc:admin") || scope.Resource != c.cfg.Resource || scope.GrantMode != "operator-only" ||
			strings.TrimSpace(scope.DisplayName["en"]) == "" || strings.TrimSpace(scope.DisplayName["zh-TW"]) == "" ||
			strings.TrimSpace(scope.Description["en"]) == "" || strings.TrimSpace(scope.Description["zh-TW"]) == "" {
			return ManagementCatalog{}, ErrUnavailable
		}
	}
	return result, nil
}

func (c *Client) GrantManagement(ctx context.Context, credential string, request ManagementGrantRequest) (ManagementMutationResult, error) {
	var result ManagementMutationResult
	if request.SchemaVersion != 1 || !validPathSegment(request.OperationID) || !validPathSegment(request.TargetUserID) {
		return result, ErrInvalidRequest
	}
	if err := c.request(ctx, http.MethodPost, "/api/v1/auth/consumers/lwc/management/grants", credential, request, &result); err != nil {
		return ManagementMutationResult{}, err
	}
	if result.OperationID != request.OperationID || result.TargetID != request.TargetUserID || result.Scope != request.Scope ||
		(result.State != "granted" && result.State != "revoked") || result.Revision < 1 {
		return ManagementMutationResult{}, ErrUnavailable
	}
	return result, nil
}

func (c *Client) ReadManagementOperation(ctx context.Context, credential, operationID string) (ManagementMutationResult, error) {
	var result ManagementMutationResult
	if !validPathSegment(operationID) {
		return result, ErrInvalidRequest
	}
	path := "/api/v1/auth/consumers/lwc/management/operations/" + url.PathEscape(operationID)
	if err := c.request(ctx, http.MethodGet, path, credential, nil, &result); err != nil {
		return ManagementMutationResult{}, err
	}
	if result.OperationID != operationID || result.TargetID == "" || result.Scope == "" ||
		(result.State != "granted" && result.State != "revoked") || result.Revision < 1 {
		return ManagementMutationResult{}, ErrUnavailable
	}
	return result, nil
}

func (c *Client) UpdateAccountStatus(ctx context.Context, credential, userID, state string) error {
	if !validPathSegment(userID) || (state != "active" && state != "suspended") {
		return ErrInvalidRequest
	}
	var result AccountMutationResult
	path := "/api/v1/auth/consumers/lwc/management/users/" + url.PathEscape(userID)
	body := struct {
		SchemaVersion int    `json:"schema_version"`
		Status        string `json:"status"`
	}{SchemaVersion: 1, Status: state}
	if err := c.request(ctx, http.MethodPatch, path, credential, body, &result); err != nil {
		return err
	}
	if result.SchemaVersion != 1 || result.Status != "updated" || result.ID != userID {
		return ErrUnavailable
	}
	return nil
}

func (c *Client) DeleteAccount(ctx context.Context, credential, userID string) error {
	if !validPathSegment(userID) {
		return ErrInvalidRequest
	}
	var result AccountMutationResult
	path := "/api/v1/auth/consumers/lwc/management/users/" + url.PathEscape(userID)
	if err := c.request(ctx, http.MethodDelete, path, credential, nil, &result); err != nil {
		return err
	}
	if result.SchemaVersion != 1 || result.Status != "deleted" || result.ID != userID {
		return ErrUnavailable
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path, userCredential string, requestBody, target any) error {
	if c == nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	runtimeCredential, err := c.token(ctx, c.cfg.Audience)
	if err != nil || strings.TrimSpace(runtimeCredential) == "" {
		return ErrUnavailable
	}
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return ErrUnavailable
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+runtimeCredential)
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if userCredential != "" {
		req.Header.Set("X-LWC-User-Credential", userCredential)
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return ErrInvalidCredential
	}
	if response.StatusCode == http.StatusForbidden {
		return ErrForbidden
	}
	if response.StatusCode == http.StatusConflict {
		return ErrConflict
	}
	if response.StatusCode == http.StatusBadRequest {
		return ErrInvalidRequest
	}
	if response.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if response.StatusCode == http.StatusGone {
		return ErrExpired
	}
	if response.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		return ErrUnavailable
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBody+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrUnavailable
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrUnavailable
	}
	return nil
}

func validKind(kind string) bool {
	switch kind {
	case "web-access", "cli-access", "mcp-oauth":
		return true
	default:
		return false
	}
}

func validPathSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00\r\n\t ")
}

func (c Config) String() string {
	return fmt.Sprintf("AuthConsumer{environment:%s resource:%s timeout:%s}", c.Environment, c.Resource, c.Timeout)
}
