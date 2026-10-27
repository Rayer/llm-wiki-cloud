//go:build byokprofilefixture

package queryruntime_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"github.com/rayer/llm-wiki-bff/internal/queryruntime"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestQueryRuntimeUsesProjectProfileFixtureTransport(t *testing.T) {
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	receiptPath := filepath.Join(t.TempDir(), "provider-receipts.tsv")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE", receiptPath)

	profile, err := llm.NewProfileClient(llm.ProfileConfig{
		ID: "created-profile", Revision: 2, Provider: "custom", Protocol: llm.ProtocolOpenAIChatCompletions,
		Endpoint: "https://fixture.example/v1", Model: "fixture-model",
	}, "synthetic-fixture-secret", llm.ProfileClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := llm.NewProfilePoolClient([]*llm.ProfileClient{profile}, nil)
	if err != nil {
		profile.Close()
		t.Fatal(err)
	}
	defer pool.Close()

	config := runtimeConfig(t)
	config.Stages.AnswerSynthesizer.NoEvidencePolicy = queryconfig.ModelPriorFallbackPolicy
	config.ConfigDigest = ""
	config, err = queryconfig.Seal(config)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := queryruntime.NewExecutor(config, cache.New(), &countingProvider{}, &countingLegacy{}, runtimeSynthesizer())
	if err != nil {
		t.Fatal(err)
	}
	ctx, receipt := query.WithReceipt(context.Background())
	ctx = llm.WithProjectChatClient(ctx, pool)
	reader := &runtimeReader{identity: storage.QueryGenerationIdentity{
		ProjectID: "project-a", GenerationID: "generation-7", ConceptsDigest: "sha256:" + strings.Repeat("a", 64),
	}}
	result, err := runtime.Execute(ctx, reader, query.Request{Query: "deploy docs", Mode: "full"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	query.FinishReceipt(receipt)
	identity := result.RuntimeConfigIdentity
	if identity == nil || identity.ExpansionModel != "fixture-model" || identity.SynthesisModel != "fixture-model" ||
		identity.LLMProfileID != "created-profile" || identity.LLMProfileRevision != 2 || identity.LLMProtocol != string(llm.ProtocolOpenAIChatCompletions) {
		t.Fatalf("Query result provenance=%+v, want the selected Project profile fixture identity", identity)
	}
	if strings.TrimSpace(result.AISynth) == "" {
		t.Fatalf("Query returned no answer: %+v", result)
	}
	hostCalls := receipt.Receipt().HostCalls
	if len(hostCalls) < 2 {
		t.Fatalf("Query host calls=%+v, want actual expansion and synthesis provider calls", hostCalls)
	}
	for _, hostCall := range hostCalls {
		if hostCall.ProfileID != "created-profile" || hostCall.Model != "fixture-model" || hostCall.Outcome != "success" {
			t.Fatalf("Query host call used unexpected profile: %+v", hostCall)
		}
	}

	providerReceipts, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(providerReceipts)), "\n")
	if len(lines) < 2 || !strings.Contains(string(providerReceipts), "openai_chat_completions\tfixture-model\t/v1/chat/completions") ||
		strings.Contains(string(providerReceipts), "synthetic-fixture-secret") {
		t.Fatalf("sanitized provider transport receipts=%q", providerReceipts)
	}
}
