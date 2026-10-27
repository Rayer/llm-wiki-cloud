//go:build byokprofilefixture

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/byok"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
)

// The actual Jev evaluator receives the Project client's encrypted profile
// through the same context seam used by ProfileDispatcher.execute.
func TestJevEvaluatorUsesPersistedProjectProfile(t *testing.T) {
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	receiptPath := filepath.Join(t.TempDir(), "jev-profile-receipt.tsv")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE", receiptPath)
	gin.SetMode(gin.TestMode)
	f := newRuntimeFixture(t)
	box, err := byok.NewCredentialBox(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.dispatcher.Handler.SetBYOKCredentialBox(box)
	f.dispatcher.Handler.SetBYOKCredentialEnvironment("jev-fixture-environment")

	user := func(c *gin.Context) {
		c.Set("userID", f.user)
		c.Next()
	}
	router := gin.New()
	router.POST("/api/v1/llm/profiles", user, f.dispatcher.Handler.CreateUserLLMProfile)
	router.GET("/api/v1/projects/:pid/llm-binding", user, f.dispatcher.Handler.ProjectLLMBinding)
	router.PUT("/api/v1/projects/:pid/llm-binding", user, f.dispatcher.Handler.SetProjectLLMBinding)

	const syntheticCredential = "synthetic-jev-project-secret"
	profileRequest := httptest.NewRequest(http.MethodPost, "/api/v1/llm/profiles", strings.NewReader(`{"name":"Jev fixture","provider":"custom","protocol":"openai_chat_completions","endpoint":"https://fixture.example/v1","model":"fixture-jev-model","api_key":"`+syntheticCredential+`"}`))
	profileRequest.Header.Set("Content-Type", "application/json")
	profileResponse := httptest.NewRecorder()
	router.ServeHTTP(profileResponse, profileRequest)
	var profile struct {
		ID               string `json:"id"`
		Revision         int64  `json:"revision"`
		SecretConfigured bool   `json:"secret_configured"`
	}
	if err := json.Unmarshal(profileResponse.Body.Bytes(), &profile); profileResponse.Code != http.StatusCreated || err != nil || profile.ID == "" || profile.Revision < 1 || !profile.SecretConfigured || strings.Contains(profileResponse.Body.String(), syntheticCredential) {
		t.Fatalf("create Jev Project profile status=%d response=%s decodeErr=%v", profileResponse.Code, profileResponse.Body.String(), err)
	}

	bindingPath := "/api/v1/projects/" + f.project + "/llm-binding"
	initial := httptest.NewRecorder()
	router.ServeHTTP(initial, httptest.NewRequest(http.MethodGet, bindingPath, nil))
	var binding llmBindingView
	if err := json.Unmarshal(initial.Body.Bytes(), &binding); initial.Code != http.StatusOK || err != nil || binding.Source != "shared" {
		t.Fatalf("initial Project binding status=%d body=%s err=%v", initial.Code, initial.Body.String(), err)
	}
	body, _ := json.Marshal(llmBindingInput{Source: "personal", ProfileID: profile.ID, Revision: binding.Revision})
	bindRequest := httptest.NewRequest(http.MethodPut, bindingPath, bytes.NewReader(body))
	bindRequest.Header.Set("Content-Type", "application/json")
	bound := httptest.NewRecorder()
	router.ServeHTTP(bound, bindRequest)
	var saved llmBindingView
	if err := json.Unmarshal(bound.Body.Bytes(), &saved); bound.Code != http.StatusOK || err != nil || saved.Source != "personal" || saved.ProfileID != profile.ID || saved.ProfileRevision != profile.Revision {
		t.Fatalf("save Project binding status=%d body=%s err=%v", bound.Code, bound.Body.String(), err)
	}

	projectClient, source, err := f.dispatcher.Handler.projectLLMClient(f.ctx, f.user, f.project)
	if err != nil || source != "personal" || projectClient == nil {
		t.Fatalf("resolve saved Project profile client source=%q client=%T err=%v", source, projectClient, err)
	}
	defer func() {
		if closer, ok := projectClient.(interface{ Close() }); ok {
			closer.Close()
		}
	}()
	identity, ok := projectClient.(llm.ProfileIdentityProvider)
	if !ok || identity.ProfileIdentity().ID != profile.ID || identity.ProfileIdentity().Revision != profile.Revision || identity.ProfileIdentity().Model != "fixture-jev-model" {
		t.Fatalf("resolved Project profile identity=%+v provider=%T", identity, projectClient)
	}

	staticFallbackCalls := 0
	evaluator := profiletags.NewJevEvaluator("must-not-be-used")
	evaluator.Client = &http.Client{Transport: runtimeRoundTripper(func(*http.Request) (*http.Response, error) {
		staticFallbackCalls++
		return nil, context.Canceled
	})}
	item := profiletags.Item{Kind: profiletags.Concept, StableID: "synthetic-concept", Content: []byte("synthetic project record")}
	tag := profiletags.Tag{Definition: "project record", MatchRule: "explicit match", NonMatchRule: "explicit non-match", UnknownRule: "ambiguous"}
	result, err := evaluator.Evaluate(llm.WithProjectChatClient(f.ctx, projectClient), item, tag, profiletags.ProviderPolicy{PromptVersion: profiletags.JevPromptVersion})
	if err != nil || result.Judgment != profiletags.Match || result.ReturnedModel != "fixture-jev-model" || staticFallbackCalls != 0 {
		t.Fatalf("Jev Project evaluation=%+v err=%v staticFallbackCalls=%d", result, err, staticFallbackCalls)
	}
	receipt, err := os.ReadFile(receiptPath)
	if err != nil || !strings.Contains(string(receipt), "openai_chat_completions\tfixture-jev-model\t/v1/chat/completions") || strings.Contains(string(receipt), syntheticCredential) {
		t.Fatalf("sanitized Project-profile Jev receipt=%q err=%v", receipt, err)
	}
	if strings.Contains(profileResponse.Body.String(), syntheticCredential) {
		t.Fatal("profile API returned the submitted credential")
	}
}
