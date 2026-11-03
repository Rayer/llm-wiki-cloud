package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"google.golang.org/api/option"
)

type adminBindingVerifierFixture struct {
	management  authconsumer.ManagementMeResult
	verifyCalls int
	meCalls     int
}

func (f *adminBindingVerifierFixture) Verify(_ context.Context, credential, _ string) (authconsumer.Identity, error) {
	f.verifyCalls++
	if credential == "" {
		return authconsumer.Identity{}, authconsumer.ErrInvalidCredential
	}
	return authconsumer.Identity{SchemaVersion: 1, Subject: "gat21-binding-actor", CredentialKind: "web-access",
		Environment: "dev", Issuer: "https://auth.example.test", Resource: "https://lwc.example.test",
		ExpiresAt: time.Now().Add(time.Hour).Unix(), AuthVersion: 1, Scopes: []string{}, ManagementScopes: []string{auth.ManagementScopeLWCAdmin}}, nil
}

func (f *adminBindingVerifierFixture) CheckAccount(context.Context, string) (authconsumer.Account, error) {
	return authconsumer.Account{}, nil
}

func (f *adminBindingVerifierFixture) ManagementMe(_ context.Context, credential string) (authconsumer.ManagementMeResult, error) {
	f.meCalls++
	if credential == "" {
		return authconsumer.ManagementMeResult{}, authconsumer.ErrInvalidCredential
	}
	return f.management, nil
}

func TestAdminSyncBindingHTTPRequiresFreshScopeAndExactBrowserOrigin(t *testing.T) {
	fs := adminBindingHTTPEmulator(t)
	ctx := context.Background()
	const ownerID, projectID, actorID, host = "gat21-http-owner", "gat21-http-project", "gat21-binding-actor", "https://auth.example.test"
	environment := fmt.Sprintf("gat21-http-env-%d", time.Now().UnixNano())
	docID := ownerID + "_" + projectID
	if _, err := fs.Collection("projects").Doc(docID).Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": "Project"}); err != nil {
		t.Fatal(err)
	}
	authority := auth.NewSyncBindingAuthority(fs, environment, host)
	binding, err := authority.CreateBinding(ctx, ownerID, projectID, "gat21-http-wiki", host)
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := authority.AdminBinding(ctx, docID, ownerID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &adminBindingVerifierFixture{management: authconsumer.ManagementMeResult{SchemaVersion: 1, Subject: actorID,
		GrantedScopes: []string{auth.ManagementScopeLWCAdmin}, CredentialScopes: map[string]int64{auth.ManagementScopeLWCAdmin: 2}}}
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetConsumerVerifier(verifier)
	h.SetSyncBindingAuthority(authority)
	h.SetSyncBindingWireEnvironment("dev")
	h.SetAdminMutationOrigins([]string{"https://lwc.example.test"})
	h.adminProjectRecordLoader = func(context.Context, string) (adminProjectRecord, error) {
		return adminProjectRecord{id: docID, userID: ownerID, projectID: projectID}, nil
	}
	router := gin.New()
	router.GET("/api/v1/admin/projects/:id/sync-binding", h.AccountAuth(config.Config{}), ManagementScopeOnly(auth.ManagementScopeLWCAdmin),
		h.AdminWebAccessOnly(), h.FreshLWCAdminScope(), h.AdminSyncBinding)
	router.POST("/api/v1/admin/projects/:id/sync-binding/:bindingID/revoke", h.AccountAuth(config.Config{}), ManagementScopeOnly(auth.ManagementScopeLWCAdmin),
		h.AdminWebAccessOnly(), h.AdminBindingMutationGuard(), h.AdminRevokeSyncBinding)
	router.GET("/api/v1/admin/sync-binding-revocations/:operationID", h.AccountAuth(config.Config{}), ManagementScopeOnly(auth.ManagementScopeLWCAdmin),
		h.AdminWebAccessOnly(), h.FreshLWCAdminScope(), h.AdminReadSyncBindingRevocation)

	query := httptest.NewRecorder()
	queryRequest := httptest.NewRequest(http.MethodGet, "/api/v1/admin/projects/"+docID+"/sync-binding", nil)
	queryRequest.Header.Set("Authorization", "Bearer web-test-token")
	router.ServeHTTP(query, queryRequest)
	if query.Code != http.StatusOK || query.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("binding query status/header=%d/%v body=%s", query.Code, query.Header(), query.Body.String())
	}
	var queryPayload struct {
		Environment     string                     `json:"environment"`
		WireEnvironment string                     `json:"wire_environment"`
		OwnerID         string                     `json:"owner_id"`
		ProjectID       string                     `json:"project_id"`
		RecordID        string                     `json:"project_record_id"`
		Binding         *auth.AdminSyncBindingView `json:"binding"`
	}
	if err := json.Unmarshal(query.Body.Bytes(), &queryPayload); err != nil || queryPayload.Environment != environment ||
		queryPayload.WireEnvironment != "dev" || queryPayload.OwnerID != ownerID || queryPayload.ProjectID != projectID ||
		queryPayload.RecordID != docID || queryPayload.Binding == nil || queryPayload.Binding.BindingID != binding.ID || queryPayload.Binding.Revision != revision {
		t.Fatalf("binding query payload=%+v error=%v body=%s", queryPayload, err, query.Body.String())
	}
	if verifier.meCalls != 1 {
		t.Fatalf("fresh management reads after query=%d, want 1", verifier.meCalls)
	}

	body := `{"schema_version":1,"operation_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","environment":"` + environment + `","subject":"gat21-http-owner","reason":"administrator confirmed","expected_revision":"` + revision + `"}`
	makePost := func(origin string, requestBody string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/projects/"+docID+"/sync-binding/"+binding.ID+"/revoke", strings.NewReader(requestBody))
		request.Header.Set("Authorization", "Bearer web-test-token")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-GateAuth-Admin-Browser", "1")
		request.Header.Set("Origin", origin)
		request.Header.Set("Sec-Fetch-Site", "cross-site")
		router.ServeHTTP(recorder, request)
		return recorder
	}
	badOrigin := makePost("https://attacker.example.test", body)
	if badOrigin.Code != http.StatusForbidden || verifier.meCalls != 1 {
		t.Fatalf("untrusted origin status=%d management reads=%d body=%s", badOrigin.Code, verifier.meCalls, badOrigin.Body.String())
	}
	rejectedPayload := makePost("https://lwc.example.test", strings.TrimSuffix(body, "}")+`,"extra":true}`)
	if rejectedPayload.Code != http.StatusBadRequest || verifier.meCalls != 1 {
		t.Fatalf("unknown mutation field status=%d management reads=%d body=%s", rejectedPayload.Code, verifier.meCalls, rejectedPayload.Body.String())
	}
	denied := verifier.management
	verifier.management.GrantedScopes = []string{}
	noCurrentScope := makePost("https://lwc.example.test", body)
	verifier.management = denied
	if noCurrentScope.Code != http.StatusForbidden {
		t.Fatalf("stale cached scope without fresh grant status=%d body=%s", noCurrentScope.Code, noCurrentScope.Body.String())
	}
	stillActive, _, err := authority.AdminBinding(ctx, docID, ownerID, projectID)
	if err != nil || stillActive == nil || stillActive.Status != "active" {
		t.Fatalf("binding changed even though fresh ManagementMe denied the actor: view=%+v error=%v", stillActive, err)
	}
	if verifier.meCalls != 2 {
		t.Fatalf("fresh management reads after denial=%d, want 2", verifier.meCalls)
	}

	verifier.meCalls = 0
	revoked := makePost("https://lwc.example.test", body)
	if revoked.Code != http.StatusOK {
		t.Fatalf("binding revoke status=%d body=%s", revoked.Code, revoked.Body.String())
	}
	var receipt auth.AdminSyncBindingRevocationReceipt
	if err := json.Unmarshal(revoked.Body.Bytes(), &receipt); err != nil || receipt.Result != "revoked" || receipt.ActorID != actorID || receipt.SubjectID != ownerID {
		t.Fatalf("binding receipt=%+v error=%v body=%s", receipt, err, revoked.Body.String())
	}
	if verifier.meCalls != 1 {
		t.Fatalf("fresh management reads for committed mutation=%d, want 1", verifier.meCalls)
	}
	if err := authority.CheckSyncBinding(ctx, ownerID, projectID, binding.ID, "gat21-http-wiki", host); err == nil {
		t.Fatal("next binding access succeeded after revocation")
	}
	readback := httptest.NewRecorder()
	readRequest := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sync-binding-revocations/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", nil)
	readRequest.Header.Set("Authorization", "Bearer web-test-token")
	router.ServeHTTP(readback, readRequest)
	if readback.Code != http.StatusOK || !strings.Contains(readback.Body.String(), `"operation_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`) {
		t.Fatalf("receipt readback status=%d body=%s", readback.Code, readback.Body.String())
	}
}

func adminBindingHTTPEmulator(t *testing.T) *cloudfirestore.Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("local Firestore emulator required")
	}
	if !strings.HasPrefix(endpoint, "127.0.0.1:") && !strings.HasPrefix(endpoint, "localhost:") {
		t.Fatal("binding admin tests require numeric loopback emulator")
	}
	client, err := cloudfirestore.NewClient(context.Background(), "demo-gat21", option.WithEndpoint("http://"+endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
