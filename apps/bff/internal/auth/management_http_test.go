package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"google.golang.org/api/idtoken"
)

func TestManagementHTTPUsesAuthCapsForCatalogUsersGrantAndReadback(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	for id, role := range map[string]string{"actor": "admin", "target": "user"} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Email: id + "@example.test", Role: role, AuthVersion: 2}); err != nil {
			t.Fatal(err)
		}
	}
	policy := managementHTTPTestPolicy()
	management := NewManagementAuthority(fs, "dev")
	seedManagementPrincipal(t, fs, "dev", "actor", map[string]ManagementScopeState{
		ManagementScopeAuthAdmin: {State: "granted", Revision: 4, Source: "migration", UpdatedAt: time.Now().UTC()},
	})
	secret := "management-http-test-secret"
	actorToken, err := GenerateFirstPartyAccessToken("actor", "admin", secret, policy.Resource, map[string]int64{ManagementScopeAuthAdmin: 4}, 2)
	if err != nil {
		t.Fatal(err)
	}
	validator := managementHTTPTestValidator(policy)
	router := gin.New()
	router.GET("/catalog", ManagementCatalogHandler(policy, managementHTTPTestProjection(policy), validator))
	router.GET("/me", ManagementMeHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))
	router.GET("/users", ManagementUsersHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))
	router.POST("/grants", ManagementGrantHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))
	router.GET("/operations/:operationID", ManagementOperationHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))

	response := managementRequest(router, http.MethodGet, "/catalog", "", actorToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), ManagementScopeAuthAdmin) || !strings.Contains(response.Body.String(), ManagementScopeLWCAdmin) {
		t.Fatalf("catalog status/body=%d/%s", response.Code, response.Body.String())
	}
	response = managementRequest(router, http.MethodGet, "/me", "", actorToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"auth:admin"`) {
		t.Fatalf("me status/body=%d/%s", response.Code, response.Body.String())
	}
	response = managementRequest(router, http.MethodGet, "/users", "", actorToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"target"`) || strings.Contains(response.Body.String(), "password_hash") {
		t.Fatalf("users status/body=%d/%s", response.Code, response.Body.String())
	}

	grantBody := `{"schema_version":1,"operation_id":"grant-user-lwc","target_user_id":"target","scope":"lwc:admin","action":"grant"}`
	response = managementRequest(router, http.MethodPost, "/grants", grantBody, actorToken)
	if response.Code != http.StatusOK {
		t.Fatalf("grant status/body=%d/%s", response.Code, response.Body.String())
	}
	var result ManagementMutationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.State != "granted" || result.Revision != 1 {
		t.Fatalf("grant result=%+v err=%v", result, err)
	}
	response = managementRequest(router, http.MethodGet, "/operations/grant-user-lwc", "", actorToken)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"revision":1`) {
		t.Fatalf("operation readback status/body=%d/%s", response.Code, response.Body.String())
	}
}

func managementHTTPTestProjection(policy LWCConsumerPolicy) config.AuthConsumerPublicProjection {
	return config.AuthConsumerPublicProjection{Issuer: policy.Issuer, Resource: policy.Resource, ManagementScopes: []config.AuthManagementScope{
		{ID: ManagementScopeAuthAdmin, Product: "auth", Resource: policy.Resource, GrantMode: "operator-only",
			Name:        config.AuthLocalizedText{En: "Platform administrator", ZhTW: "平台管理員"},
			Description: config.AuthLocalizedText{En: "Manage accounts and registered scopes.", ZhTW: "管理帳號與已登錄權限。"}, GrantorScopes: []string{ManagementScopeAuthAdmin}},
		{ID: ManagementScopeLWCAdmin, Product: "lwc", Resource: policy.Resource, GrantMode: "operator-only",
			Name:        config.AuthLocalizedText{En: "LWC administrator", ZhTW: "LWC 管理員"},
			Description: config.AuthLocalizedText{En: "Operate LWC projects.", ZhTW: "管理 LWC 專案。"}, GrantorScopes: []string{ManagementScopeAuthAdmin}},
	}}
}

func TestManagementHTTPRejectsRoleOnlyAndLWCOnlyGrantors(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	if _, err := fs.Collection("auth_management").Doc("dev").Collection("authority").Doc("current").Set(ctx, ManagementAuthorityEpoch{
		SchemaVersion: 1, Mode: "central", Epoch: 2, MigrationRunID: "migration-cutover", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for id := range map[string]bool{"role-only": true, "lwc-only": true, "target": true} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: "admin", AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	policy := managementHTTPTestPolicy()
	management := NewManagementAuthority(fs, "dev")
	seedManagementPrincipal(t, fs, "dev", "lwc-only", map[string]ManagementScopeState{
		ManagementScopeLWCAdmin: {State: "granted", Revision: 2, Source: "migration", UpdatedAt: time.Now().UTC()},
	})
	secret := "management-http-test-secret"
	roleOnlyToken, _ := GenerateFirstPartyAccessToken("role-only", "admin", secret, policy.Resource, nil, 1)
	lwcOnlyToken, _ := GenerateFirstPartyAccessToken("lwc-only", "admin", secret, policy.Resource, map[string]int64{ManagementScopeLWCAdmin: 2}, 1)
	validator := managementHTTPTestValidator(policy)
	router := gin.New()
	router.POST("/grants", ManagementGrantHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))
	body := `{"schema_version":1,"operation_id":"try-grant","target_user_id":"target","scope":"auth:admin","action":"grant"}`
	for _, token := range []string{roleOnlyToken, lwcOnlyToken} {
		response := managementRequest(router, http.MethodPost, "/grants", body, token)
		if response.Code != http.StatusForbidden {
			t.Fatalf("unauthorized grant status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func managementHTTPTestPolicy() LWCConsumerPolicy {
	return LWCConsumerPolicy{Enabled: true, Environment: "dev", Resource: "https://bff.dev.example.test",
		CallerServiceAccount: "lwc-bff-dev@example.iam.gserviceaccount.com", VerifierAudience: "https://auth-runtime.example.test",
		Issuer: "https://auth.dev.example.test", AllowedCredentialKinds: []string{"web-access", "cli-access"}}
}

func managementHTTPTestValidator(policy LWCConsumerPolicy) OIDCCallerValidator {
	return func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
		if token != "runtime-token" || audience != policy.VerifierAudience {
			return nil, context.Canceled
		}
		return &idtoken.Payload{Issuer: "https://accounts.google.com", Audience: audience, Expires: time.Now().Add(time.Hour).Unix(),
			Claims: map[string]interface{}{"email": policy.CallerServiceAccount, "email_verified": true}}, nil
	}
}

func managementRequest(router http.Handler, method, path, body, credential string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer runtime-token")
	request.Header.Set("X-LWC-User-Credential", credential)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
