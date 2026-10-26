package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
	"github.com/rayer/llm-wiki-bff/internal/config"
)

type registrationAuthorityStub struct {
	identity authconsumer.Identity
	updates  int
	patch    authconsumer.RegistrationSettingsPatch
}

type bridgeManagementStub struct {
	registrationAuthorityStub
	legacyUpdates int
	grants        int
	role          *string
	state         *string
}

func (s *registrationAuthorityStub) Verify(context.Context, string, string) (authconsumer.Identity, error) {
	return s.identity, nil
}

func (*registrationAuthorityStub) CheckAccount(context.Context, string) (authconsumer.Account, error) {
	return authconsumer.Account{}, nil
}

func (*registrationAuthorityStub) ManagementRegistrationSettings(context.Context, string) (authconsumer.RegistrationSettings, error) {
	return authconsumer.RegistrationSettings{RegistrationEnabled: true, EmailRegistrationEnabled: true, GoogleRegistrationEnabled: false}, nil
}

func (s *registrationAuthorityStub) UpdateManagementRegistrationSettings(_ context.Context, credential string,
	patch authconsumer.RegistrationSettingsPatch) (authconsumer.RegistrationSettings, error) {
	if credential != "actor-credential" {
		return authconsumer.RegistrationSettings{}, authconsumer.ErrInvalidCredential
	}
	s.updates++
	s.patch = patch
	return authconsumer.RegistrationSettings{RegistrationEnabled: *patch.RegistrationEnabled, EmailRegistrationEnabled: true, GoogleRegistrationEnabled: false}, nil
}

func (*bridgeManagementStub) ManagementCatalog(context.Context, string) (authconsumer.ManagementCatalog, error) {
	return authconsumer.ManagementCatalog{}, nil
}

func (*bridgeManagementStub) ManagementUsers(context.Context, string) ([]authconsumer.ManagementUser, error) {
	return []authconsumer.ManagementUser{}, nil
}

func (s *bridgeManagementStub) GrantManagement(context.Context, string, authconsumer.ManagementGrantRequest) (authconsumer.ManagementMutationResult, error) {
	s.grants++
	return authconsumer.ManagementMutationResult{}, nil
}

func (*bridgeManagementStub) ReadManagementOperation(context.Context, string, string) (authconsumer.ManagementMutationResult, error) {
	return authconsumer.ManagementMutationResult{}, authconsumer.ErrNotFound
}

func (*bridgeManagementStub) UpdateAccountStatus(context.Context, string, string, string) error {
	return nil
}
func (*bridgeManagementStub) DeleteAccount(context.Context, string, string) error { return nil }

func (s *bridgeManagementStub) UpdateLegacyAccount(_ context.Context, credential, _ string, role, state *string) error {
	if credential != "actor-credential" {
		return authconsumer.ErrInvalidCredential
	}
	s.legacyUpdates++
	s.role, s.state = role, state
	return nil
}

func TestAdminRegistrationSettingsRoutesThroughAuthScopeBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &registrationAuthorityStub{identity: authconsumer.Identity{SchemaVersion: 1, Subject: "actor", CredentialKind: "web-access",
		Environment: "dev", Issuer: "https://auth.example.test", Resource: "https://bff.example.test",
		ExpiresAt: time.Now().Add(time.Minute).Unix(), Scopes: []string{}, ManagementScopes: []string{auth.ManagementScopeLWCAdmin}}}
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetConsumerVerifier(stub)
	router := gin.New()
	admin := router.Group("/settings")
	admin.Use(h.AccountAuth(config.Config{}), ManagementScopeOnly(auth.ManagementScopeAuthAdmin))
	admin.GET("", h.AdminGetRegistrationSettings)
	admin.PATCH("", h.AdminPatchRegistrationSettings)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/settings", strings.NewReader(`{"registration_enabled":false}`))
	req.Header.Set("Authorization", "Bearer actor-credential")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || stub.updates != 0 {
		t.Fatalf("lwc:admin updated Auth registration settings: status=%d updates=%d body=%s", rec.Code, stub.updates, rec.Body.String())
	}

	stub.identity.ManagementScopes = []string{auth.ManagementScopeAuthAdmin}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/settings", strings.NewReader(`{"registration_enabled":false}`))
	req.Header.Set("Authorization", "Bearer actor-credential")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || stub.updates != 1 || stub.patch.RegistrationEnabled == nil || *stub.patch.RegistrationEnabled {
		t.Fatalf("auth:admin did not update through Auth: status=%d updates=%d body=%s", rec.Code, stub.updates, rec.Body.String())
	}
	var result authconsumer.RegistrationSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.RegistrationEnabled {
		t.Fatalf("response=%s result=%+v err=%v", rec.Body.String(), result, err)
	}
}

func TestSchema2BridgeRoutesRoleAndStatusToAuthAndBlocksScopeMutations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &bridgeManagementStub{registrationAuthorityStub: registrationAuthorityStub{identity: authconsumer.Identity{
		SchemaVersion: 1, Subject: "actor", CredentialKind: "web-access", Environment: "dev", Issuer: "https://auth.example.test",
		Resource: "https://bff.example.test", ExpiresAt: time.Now().Add(time.Minute).Unix(), Scopes: []string{},
		ManagementScopes: []string{auth.ManagementScopeAuthAdmin},
	}}}
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetConsumerVerifier(stub)
	h.SetAuthManagementBridge(true)
	router := gin.New()
	router.PATCH("/users/:id", h.AccountAuth(config.Config{}), ManagementScopeOnly(auth.ManagementScopeAuthAdmin), h.AdminUpdateUser)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/users/target", strings.NewReader(`{"role":"member","status":"suspended"}`))
	req.Header.Set("Authorization", "Bearer actor-credential")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || stub.legacyUpdates != 1 || stub.role == nil || *stub.role != "member" || stub.state == nil || *stub.state != "suspended" {
		t.Fatalf("schema-2 legacy update did not use Auth: status=%d calls=%d role=%v status=%v body=%s", rec.Code, stub.legacyUpdates, stub.role, stub.state, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/users/target", strings.NewReader(`{"scope":"lwc:admin","action":"grant","operation_id":"daily-grant"}`))
	req.Header.Set("Authorization", "Bearer actor-credential")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || stub.grants != 0 {
		t.Fatalf("schema-2 bridge allowed a daily scope change: status=%d grants=%d body=%s", rec.Code, stub.grants, rec.Body.String())
	}
}
