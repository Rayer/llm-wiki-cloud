package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/config"
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
	seedCentralManagementAuthority(t, ctx, NewManagementAuthority(fs, "dev"), "management-http-test-cutover")
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

func TestManagementHTTPRoutesSchema2RoleStatusMutationThroughAuthTransaction(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	actorID, targetID := "legacy-actor", "legacy-target"
	for _, id := range []string{actorID, targetID} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: "admin", Status: AccountActive, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	policy := managementHTTPTestPolicy()
	management := NewManagementAuthority(fs, "dev")
	epoch, err := management.AuthorityEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secret := "management-http-test-secret"
	actorToken, err := GenerateFirstPartyAccessToken(actorID, "admin", secret, policy.Resource,
		map[string]int64{ManagementScopeAuthAdmin: epoch.Epoch}, 1)
	if err != nil {
		t.Fatal(err)
	}
	validator := managementHTTPTestValidator(policy)
	router := gin.New()
	router.PATCH("/users/:userID/legacy", ManagementLegacyAccountHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))
	body := `{"schema_version":1,"role":"member","status":"suspended"}`
	response := managementRequest(router, http.MethodPatch, "/users/"+targetID+"/legacy", body, actorToken)
	if response.Code != http.StatusOK {
		t.Fatalf("legacy Auth mutation status/body=%d/%s", response.Code, response.Body.String())
	}
	target, err := GetUser(ctx, fs, targetID)
	if err != nil || target.Role != "member" || target.Status != AccountSuspended || target.AuthVersion != 2 {
		t.Fatalf("Auth transaction did not apply role/status atomically: target=%+v err=%v", target, err)
	}
	if _, err := fs.Collection("users").Doc(targetID).Update(ctx, []firestore.Update{{Path: "status", Value: AccountActive}}); err != nil {
		t.Fatal(err)
	}
	if caps, err := management.GrantedScopeVersions(ctx, targetID); err != nil || len(caps) != 0 {
		t.Fatalf("role change restored revoked legacy scope membership: caps=%v err=%v", caps, err)
	}
}

func TestManagementHTTPBlocksShadowGrantRemoveAndPartialScopeLastAdminChanges(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	users := map[string]string{
		"shadow-admin": "admin", "grant-target": "member", "partial-operator": "member", "partial-target": "admin",
	}
	for id, role := range users {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: role, Status: AccountActive, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	policy := managementHTTPTestPolicy()
	management := NewManagementAuthority(fs, "dev")
	epoch, err := management.AuthorityEpoch(ctx)
	if err != nil || epoch.Mode != "legacy-shadow" {
		t.Fatalf("authority epoch=%+v err=%v", epoch, err)
	}
	seedManagementPrincipal(t, fs, "dev", "partial-operator", map[string]ManagementScopeState{
		ManagementScopeAuthAdmin: {State: "granted", Revision: 1, Source: "test", UpdatedAt: time.Now().UTC()},
	})
	seedManagementPrincipal(t, fs, "dev", "partial-target", map[string]ManagementScopeState{
		ManagementScopeAuthAdmin: {State: "granted", Revision: 2, Source: "migration", UpdatedAt: time.Now().UTC()},
	})
	secret := "management-http-test-secret"
	shadowToken, err := GenerateFirstPartyAccessToken("shadow-admin", "admin", secret, policy.Resource,
		map[string]int64{ManagementScopeAuthAdmin: epoch.Epoch, ManagementScopeLWCAdmin: epoch.Epoch}, 1)
	if err != nil {
		t.Fatal(err)
	}
	partialToken, err := GenerateFirstPartyAccessToken("partial-operator", "member", secret, policy.Resource,
		map[string]int64{ManagementScopeAuthAdmin: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	validator := managementHTTPTestValidator(policy)
	router := gin.New()
	router.POST("/grants", ManagementGrantHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))
	router.PATCH("/users/:userID", ManagementAccountStatusHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))
	router.DELETE("/users/:userID", ManagementAccountDeleteHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))

	for _, body := range []string{
		`{"schema_version":1,"operation_id":"shadow-self-remove","target_user_id":"shadow-admin","scope":"auth:admin","action":"remove"}`,
		`{"schema_version":1,"operation_id":"shadow-single-grant","target_user_id":"grant-target","scope":"lwc:admin","action":"grant"}`,
	} {
		response := managementRequest(router, http.MethodPost, "/grants", body, shadowToken)
		if response.Code != http.StatusConflict {
			t.Fatalf("legacy-shadow grant mutation status/body=%d/%s", response.Code, response.Body.String())
		}
	}
	for _, operationID := range []string{"shadow-self-remove", "shadow-single-grant"} {
		if _, err := management.operationRef(operationID).Get(ctx); err == nil {
			t.Fatalf("legacy-shadow mutation %s wrote an operation", operationID)
		}
	}
	if caps, err := management.GrantedScopeVersions(ctx, "shadow-admin"); err != nil ||
		caps[ManagementScopeAuthAdmin] != epoch.Epoch || caps[ManagementScopeLWCAdmin] != epoch.Epoch {
		t.Fatalf("denied shadow self-removal changed effective scopes: caps=%v err=%v", caps, err)
	}
	response := managementRequest(router, http.MethodPatch, "/users/shadow-admin", `{"schema_version":1,"status":"suspended"}`, partialToken)
	if response.Code != http.StatusOK {
		t.Fatalf("suspend other shadow admin before partial-scope check status/body=%d/%s", response.Code, response.Body.String())
	}

	if caps, err := management.GrantedScopeVersions(ctx, "partial-target"); err != nil ||
		caps[ManagementScopeAuthAdmin] != 2 || caps[ManagementScopeLWCAdmin] != epoch.Epoch {
		t.Fatalf("partial target scopes=%v err=%v", caps, err)
	}
	response = managementRequest(router, http.MethodPatch, "/users/partial-target", `{"schema_version":1,"status":"suspended"}`, partialToken)
	if response.Code != http.StatusConflict {
		t.Fatalf("suspend last implicit lwc:admin status/body=%d/%s", response.Code, response.Body.String())
	}
	response = managementRequest(router, http.MethodDelete, "/users/partial-target", "", partialToken)
	if response.Code != http.StatusConflict {
		t.Fatalf("delete last implicit lwc:admin status/body=%d/%s", response.Code, response.Body.String())
	}
	if user, err := GetUser(ctx, fs, "partial-target"); err != nil || !user.Active() {
		t.Fatalf("denied last-admin endpoint changed target: user=%+v err=%v", user, err)
	}
}

func TestManagementHTTPRejectsAuthVersionInvalidatedAfterCredentialValidation(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	actorID, otherID, targetID := "barrier-actor", "barrier-other", "barrier-target"
	for _, id := range []string{actorID, otherID, targetID} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: "member", Status: AccountActive, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	policy := managementHTTPTestPolicy()
	management := NewManagementAuthority(fs, "dev")
	seedCentralManagementAuthority(t, ctx, management, "auth-version-http-test")
	for _, id := range []string{actorID, otherID} {
		seedManagementPrincipal(t, fs, "dev", id, map[string]ManagementScopeState{
			ManagementScopeAuthAdmin: {State: "granted", Revision: 1, Source: "test", UpdatedAt: time.Now().UTC()},
		})
	}
	secret := "management-http-test-secret"
	staleToken, err := GenerateFirstPartyAccessToken(actorID, "member", secret, policy.Resource,
		map[string]int64{ManagementScopeAuthAdmin: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	validator := managementHTTPTestValidator(policy)
	router := gin.New()
	router.POST("/grants", ManagementGrantHandler(config.Config{JWTSecret: secret}, fs, policy, validator, nil, management))
	body := `{"schema_version":1,"operation_id":"auth-version-barrier-grant","target_user_id":"barrier-target","scope":"lwc:admin","action":"grant"}`
	requestBody := &authVersionBarrierReader{reader: strings.NewReader(body), beforeRead: func() error {
		if err := management.UpdateAccountStatus(ctx, otherID, map[string]int64{ManagementScopeAuthAdmin: 1}, 1, actorID, AccountSuspended); err != nil {
			return err
		}
		return management.UpdateAccountStatus(ctx, otherID, map[string]int64{ManagementScopeAuthAdmin: 1}, 1, actorID, AccountActive)
	}}
	request := httptest.NewRequest(http.MethodPost, "/grants", requestBody)
	request.Header.Set("Authorization", "Bearer runtime-token")
	request.Header.Set("X-LWC-User-Credential", staleToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if requestBody.err != nil {
		t.Fatalf("suspend/restore barrier: %v", requestBody.err)
	}
	if response.Code != http.StatusForbidden {
		t.Fatalf("pre-suspension credential status/body=%d/%s", response.Code, response.Body.String())
	}
	if _, err := management.operationRef("auth-version-barrier-grant").Get(ctx); err == nil {
		t.Fatal("stale credential wrote an operation record")
	}
	if states, err := management.ScopeStates(ctx, targetID); err != nil || len(states) != 0 {
		t.Fatalf("stale credential changed target scopes: states=%v err=%v", states, err)
	}
	actor, err := GetUser(ctx, fs, actorID)
	if err != nil || !actor.Active() || actor.AuthVersion <= 1 {
		t.Fatalf("actor was not restored with invalidated credential: actor=%+v err=%v", actor, err)
	}
	freshToken, err := GenerateFirstPartyAccessToken(actorID, "member", secret, policy.Resource,
		map[string]int64{ManagementScopeAuthAdmin: 1}, actor.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	responseRecorder := managementRequest(router, http.MethodPost, "/grants", body, freshToken)
	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("fresh credential status/body=%d/%s", responseRecorder.Code, responseRecorder.Body.String())
	}
	states, err := management.ScopeStates(ctx, targetID)
	if err != nil || states[ManagementScopeLWCAdmin].State != "granted" {
		t.Fatalf("fresh credential did not apply target scope: states=%v err=%v", states, err)
	}
}

type authVersionBarrierReader struct {
	reader     *strings.Reader
	beforeRead func() error
	once       sync.Once
	err        error
}

func (r *authVersionBarrierReader) Read(p []byte) (int, error) {
	r.once.Do(func() { r.err = r.beforeRead() })
	return r.reader.Read(p)
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
