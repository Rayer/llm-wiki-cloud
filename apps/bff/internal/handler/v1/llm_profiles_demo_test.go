package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/byok"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
)

func TestDemoProjectLLMSourceUsesSharedPoolInsteadOfAccountPersonalProfile(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetPipelineQuotaConfig(2, 3600, 1, []string{"trial-user"})

	for _, test := range []struct {
		owner  string
		source string
		want   string
	}{
		{owner: "trial-user", source: "personal", want: "shared"},
		{owner: "trial-user", source: "shared", want: "shared"},
		{owner: "ordinary-user", source: "personal", want: "personal"},
	} {
		if got := h.effectiveProjectLLMSource(test.owner, test.source); got != test.want {
			t.Errorf("effectiveProjectLLMSource(%q, %q) = %q, want %q", test.owner, test.source, got, test.want)
		}
	}
}

func TestLLMProfileMetadataUpdatePreservesCredentialAndRevisionBinding(t *testing.T) {
	box, err := byok.NewCredentialBox(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetBYOKCredentialBox(box)
	h.SetBYOKCredentialEnvironment("test-environment")

	created, err := h.buildLLMProfile("owner-a", "profile-a", llmProfileInput{
		Name: "OpenAI", Provider: "openai", Model: "gpt-fixture", APIKey: "first-synthetic-key",
	}, nil, false)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	disabled := false
	updated, err := h.buildLLMProfile("owner-a", "profile-a", llmProfileInput{
		Name: "OpenAI renamed", Provider: "openai", Model: "gpt-fixture-v2", Revision: created.Revision, Enabled: &disabled,
	}, &created, false)
	if err != nil {
		t.Fatalf("metadata update without api_key: %v", err)
	}
	if updated.Revision != created.Revision+1 || updated.CredentialRevision != created.CredentialRevision || updated.EncryptedAPIKey != created.EncryptedAPIKey || updated.Enabled {
		t.Fatalf("metadata update changed credential or revision incorrectly: created=%+v updated=%+v", created, updated)
	}
	plaintext, err := box.Decrypt(updated.EncryptedAPIKey, h.llmProfileAAD("owner-a", updated))
	if err != nil || string(plaintext) != "first-synthetic-key" {
		t.Fatalf("preserved credential decrypt = %q, %v", plaintext, err)
	}
	clear(plaintext)

	replaced, err := h.buildLLMProfile("owner-a", "profile-a", llmProfileInput{
		Name: updated.Name, Provider: "openai", Model: updated.Model, APIKey: "replacement-synthetic-key", Revision: updated.Revision,
	}, &updated, false)
	if err != nil {
		t.Fatalf("replace profile credential: %v", err)
	}
	if replaced.CredentialRevision != replaced.Revision {
		t.Fatalf("replacement credential revision = %d, want profile revision %d", replaced.CredentialRevision, replaced.Revision)
	}
	if _, err := box.Decrypt(created.EncryptedAPIKey, h.llmProfileAAD("owner-a", replaced)); err == nil {
		t.Fatal("prior ciphertext decrypted under the replacement credential revision")
	}
	if _, err := box.Decrypt(replaced.EncryptedAPIKey, h.llmProfileAAD("owner-b", replaced)); err == nil {
		t.Fatal("credential ciphertext decrypted under another owner")
	}
	otherEnvironment := New(nil, nil, nil, nil, nil, nil)
	otherEnvironment.SetBYOKCredentialEnvironment("other-environment")
	if _, err := box.Decrypt(replaced.EncryptedAPIKey, otherEnvironment.llmProfileAAD("owner-a", replaced)); err == nil {
		t.Fatal("credential ciphertext decrypted under another environment")
	}
}

func TestAdminProfileCanReferenceReadOnlyDeploymentCredential(t *testing.T) {
	box, err := byok.NewCredentialBox(bytes.Repeat([]byte{0x4a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetBYOKCredentialBox(box)
	h.SetBYOKCredentialEnvironment("test-environment")
	const reference = "projects/test-project/secrets/deepseek-key/versions/7"
	h.SetBYOKDeploymentCredentials([]BYOKDeploymentCredential{{Reference: reference, Label: "Existing deployment key", Value: []byte("synthetic-deployment-key")}})
	input := llmProfileInput{Name: "Deployment-backed", Provider: "openai", Protocol: "openai_chat_completions", Model: "fixture-model", CredentialReference: reference}
	created, err := h.buildLLMProfile("platform", "shared-a", input, nil, true)
	if err != nil {
		t.Fatalf("create deployment-backed shared profile: %v", err)
	}
	if created.CredentialOrigin != "deployment" || created.CredentialReference != reference || created.EncryptedAPIKey != "" || !created.SecretConfigured {
		t.Fatalf("deployment credential was not stored as a read-only reference: %+v", created)
	}
	key, err := h.profileCredential(created, "platform")
	if err != nil || string(key) != "synthetic-deployment-key" {
		t.Fatalf("resolve deployment credential = %q, %v", key, err)
	}
	clear(key)

	input.Revision = created.Revision
	input.Name = "Deployment-backed metadata update"
	updated, err := h.buildLLMProfile("platform", "shared-a", input, &created, true)
	if err != nil {
		t.Fatalf("metadata update of deployment-backed profile: %v", err)
	}
	if updated.CredentialReference != reference || updated.CredentialRevision != created.CredentialRevision || updated.EncryptedAPIKey != "" {
		t.Fatalf("metadata update changed deployment credential reference: %+v", updated)
	}
	input.Revision = updated.Revision
	input.APIKey = "replacement-secret"
	if _, err := h.buildLLMProfile("platform", "shared-a", input, &updated, true); err == nil {
		t.Fatal("deployment-owned credential accepted a secret replacement")
	}
	input.APIKey = ""
	input.CredentialReference = "projects/test-project/secrets/other-key/versions/1"
	if _, err := h.buildLLMProfile("platform", "shared-a", input, &updated, true); err == nil {
		t.Fatal("deployment-owned credential accepted a reference change")
	}

	h.SetBYOKDeploymentCredentials(nil)
	if _, err := h.profileCredential(created, "platform"); err == nil {
		t.Fatal("missing deployment reference did not fail closed")
	}
}

func TestDeletingLastSharedProfileKeepsPoolManagedAndStopsBootstrap(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("local Firestore emulator required")
	}
	const project = "lwc434-shared-pool-state-test"
	const database = "(default)"
	scope := fmt.Sprintf("lwc434-shared-pool-state-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	box, err := byok.NewCredentialBox(bytes.Repeat([]byte{0x33}, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := New(nil, client, nil, nil, nil, nil)
	h.SetBYOKCredentialBox(box)
	h.SetBYOKCredentialEnvironment(scope)
	ctx := context.Background()
	if source, profiles, err := h.resolvePipelineLLMProfiles(ctx, "owner-a", "project-a"); err != nil || source != "bootstrap" || len(profiles) != 0 {
		t.Fatalf("never-initialized shared pool = (%q, %d, %v), want compatible bootstrap", source, len(profiles), err)
	}
	projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(projectDocID("owner-a", "project-a"))
	if _, err := projectRef.Set(ctx, map[string]any{"user_id": "owner-a", "project_id": "project-a", "name": "fixture"}); err != nil {
		t.Fatalf("seed disposable Project: %v", err)
	}
	if _, source, err := h.projectLLMClient(ctx, "owner-a", "project-a"); err != nil || source != "bootstrap" {
		t.Fatalf("Query before managed-pool takeover = (%q, %v), want bootstrap", source, err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/admin/llm/profiles", h.CreateAdminLLMProfile)
	router.DELETE("/admin/llm/profiles/:profileID", h.DeleteAdminLLMProfile)
	create := httptest.NewRecorder()
	router.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/admin/llm/profiles", strings.NewReader(`{"name":"Fixture shared","provider":"openai","protocol":"openai_chat_completions","model":"fixture-model","api_key":"synthetic-shared-key","enabled":true,"available_to_public":true,"priority":1}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create shared profile status=%d body=%s", create.Code, create.Body.String())
	}
	var created llmProfileView
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode created profile: id=%q err=%v", created.ID, err)
	}
	remove := httptest.NewRecorder()
	router.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/admin/llm/profiles/"+created.ID, nil))
	if remove.Code != http.StatusNoContent {
		t.Fatalf("delete last shared profile status=%d body=%s", remove.Code, remove.Body.String())
	}
	if initialized, err := h.sharedLLMPoolInitialized(ctx); err != nil || !initialized {
		t.Fatalf("managed-pool takeover state = (%t, %v), want initialized", initialized, err)
	}
	if source, profiles, err := h.resolvePipelineLLMProfiles(ctx, "owner-a", "project-a"); err == nil || source == "bootstrap" || len(profiles) != 0 {
		t.Fatalf("Pipeline after deleting last shared profile = (%q, %d, %v), want shared unavailable", source, len(profiles), err)
	}
	if _, source, err := h.projectLLMClient(ctx, "owner-a", "project-a"); err == nil || source == "bootstrap" {
		t.Fatalf("Query after deleting last shared profile = (%q, %v), want shared unavailable", source, err)
	}
}

func TestDemoRejectsPersonalBindingButCanUseSharedSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetPipelineQuotaConfig(2, 3600, 1, []string{"trial-user"})
	router := gin.New()
	router.PUT("/api/v1/projects/:pid/llm-binding", func(c *gin.Context) {
		c.Set("userID", "trial-user")
		h.SetProjectLLMBinding(c)
	})
	request := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/v1/projects/project-a/llm-binding", strings.NewReader(body)))
		return recorder
	}

	personal := request(`{"source":"personal","profile_id":"profile-a","revision":0}`)
	if personal.Code != http.StatusForbidden {
		t.Fatalf("personal binding status=%d body=%s, want %d", personal.Code, personal.Body.String(), http.StatusForbidden)
	}
	shared := request(`{"source":"shared","revision":0}`)
	if shared.Code == http.StatusForbidden {
		t.Fatalf("shared source was incorrectly blocked for Demo: %s", shared.Body.String())
	}
}
