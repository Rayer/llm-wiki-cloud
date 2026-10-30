//go:build byokprofilefixture

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/suggestedqueries"
)

func TestStandaloneSuggestedQueriesPublishesSelectedProfileFixtureGeneration(t *testing.T) {
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	receiptPath := filepath.Join(t.TempDir(), "provider-receipts.tsv")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE", receiptPath)
	profile := testResolverProfile("created-profile", "fixture-model", 2)

	var received pipelineLLMProfileResolverRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode resolver request: %v", err)
		}
		writePipelineProfileWire(w, "personal", []pipelineLLMProfile{profile})
	}))
	defer server.Close()

	vault := testSuggestedQueriesVault(t)
	err := runSuggestedQueriesCommand(context.Background(), workerConfig{
		VaultPath: vault, ExecutionID: "fixture-profile-suggested", SuggestedQueries: true,
		LocalCloudScope: "test-scope", ProfileResolverURL: server.URL + "/internal/pipeline/llm-profile",
		PipelineReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture",
		APIKey: "platform-fallback-must-not-run", apiKeySet: true,
	})
	if err != nil {
		t.Fatalf("runSuggestedQueriesCommand() error = %v", err)
	}
	if received != (pipelineLLMProfileResolverRequest{ReservationID: "reservation-fixture", UserID: "user-fixture", ProjectID: "project-fixture", IncludeCredentials: true}) {
		t.Fatalf("resolver request=%+v, want the disposable Project binding scope", received)
	}
	artifactData, err := os.ReadFile(filepath.Join(vault, suggestedqueries.Path))
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := suggestedqueries.Decode(artifactData)
	if err != nil {
		t.Fatalf("decode generated artifact: %v", err)
	}
	if len(artifact.Candidates) != suggestedqueries.RequiredQueries {
		t.Fatalf("candidate count=%d, want %d", len(artifact.Candidates), suggestedqueries.RequiredQueries)
	}
	metadata := artifact.Candidates[0].Generation
	if metadata.ProfileID != "created-profile" || metadata.ProfileRevision != 2 || metadata.Provider != "custom" ||
		metadata.Protocol != string(llm.ProtocolOpenAIChatCompletions) || metadata.Model != "fixture-model" {
		t.Fatalf("published artifact provenance=%+v, want the selected Project profile", metadata)
	}
	providerReceipts, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(providerReceipts), "openai_chat_completions\tfixture-model\t/v1/chat/completions") ||
		strings.Contains(string(providerReceipts), string(profile.APIKey)) {
		t.Fatalf("sanitized provider transport receipts=%q", providerReceipts)
	}
}
