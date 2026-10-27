//go:build byokprofilefixture

package queryruntime_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"github.com/rayer/llm-wiki-bff/internal/queryruntime"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type profileFixtureCorpusReader struct{ runtimeReader }

func (r *profileFixtureCorpusReader) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch path {
	case cache.GCSPath:
		return []byte(`{"slug":"byok-profile-routing","title":"BYOK Profile Routing","body":"BYOK profile routing uses the selected Project profile for model calls."}` + "\n"), nil
	case "cache/id_map.json":
		return []byte(`{"concept":{"000000000001":"byok-profile-routing"}}`), nil
	default:
		return nil, os.ErrNotExist
	}
}

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

func TestQueryRuntimePreservesRetrievalAndReportsSyntheticProjectProfileAuthenticationFailure(t *testing.T) {
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	path := filepath.Join(t.TempDir(), "provider-receipts.tsv")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE", path)
	previousMeterProvider := otel.GetMeterProvider()
	metricReader := sdkmetric.NewManualReader()
	metricProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricReader))
	otel.SetMeterProvider(metricProvider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previousMeterProvider)
		_ = metricProvider.Shutdown(context.Background())
	})

	config := runtimeConfig(t)
	config.Stages.AnswerSynthesizer.NoEvidencePolicy = queryconfig.ModelPriorFallbackPolicy
	config.ConfigDigest = ""
	config, err := queryconfig.Seal(config)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := queryruntime.NewExecutor(config, cache.New(), &countingProvider{}, &countingLegacy{}, runtimeSynthesizer())
	if err != nil {
		t.Fatal(err)
	}
	reader := &profileFixtureCorpusReader{runtimeReader: runtimeReader{identity: storage.QueryGenerationIdentity{
		ProjectID: "project-a", GenerationID: "generation-7", ConceptsDigest: "sha256:" + strings.Repeat("a", 64),
	}}}
	request := query.Request{Query: "How does BYOK profile routing work?", Mode: "full"}

	invalid := newFixtureProfile(t, "invalid-project-profile", "synthetic-invalid-fixture-key")
	unselected := newFixtureProfile(t, "unselected-profile", "synthetic-unselected-fixture-key")
	invalidPool, err := llm.NewProfilePoolClient([]*llm.ProfileClient{invalid, unselected}, nil)
	if err != nil {
		invalid.Close()
		unselected.Close()
		t.Fatal(err)
	}
	invalidCtx, invalidReceipt := query.WithReceipt(context.Background())
	invalidCtx = llm.WithProjectChatClient(invalidCtx, invalidPool)
	invalidResult, err := runtime.Execute(invalidCtx, reader, request)
	if err != nil {
		t.Fatalf("invalid-profile Execute() error = %v", err)
	}
	query.FinishReceipt(invalidReceipt)
	invalidPool.Close()
	if invalidResult.Status != "ok" || invalidResult.Reason != "qualified_evidence" || len(invalidResult.Results) != 1 ||
		invalidResult.Results[0].Slug != "byok-profile-routing" || invalidResult.AISynth != "" || len(invalidResult.Citations) != 0 {
		t.Fatalf("invalid-profile result = %+v, want retrieved result retained without AI answer or citations", invalidResult)
	}
	wantFailures := []query.AIOperationFailure{
		{Stage: query.AIOperationQueryExpansion, Class: query.AIFailureAuthentication, Fallback: query.AIFallbackDeterministicPlan},
		{Stage: query.AIOperationAnswerSynthesis, Class: query.AIFailureAuthentication, Fallback: query.AIFallbackRetrievalOnly},
	}
	if !reflect.DeepEqual(invalidResult.AIOperationFailures, wantFailures) {
		t.Fatalf("invalid-profile AI failures = %+v, want %+v", invalidResult.AIOperationFailures, wantFailures)
	}
	invalidHostCalls := invalidReceipt.Receipt().HostCalls
	if len(invalidHostCalls) < 2 {
		t.Fatalf("invalid-profile host calls = %+v, want expansion and synthesis HTTP calls", invalidHostCalls)
	}
	for _, call := range invalidHostCalls {
		if call.ProfileID != "invalid-project-profile" || call.Outcome != string(llm.ProviderAuthentication) {
			t.Fatalf("invalid-profile call = %+v, want typed authentication failure from selected profile only", call)
		}
	}
	stageFailures := map[string]query.StageReceipt{}
	for _, stage := range invalidReceipt.Receipt().Stages {
		if stage.FailureClass != "" {
			stageFailures[stage.Name] = stage
		}
	}
	if len(stageFailures) != 2 || stageFailures[query.AIOperationQueryExpansion].FailureClass != "authentication" ||
		stageFailures[query.AIOperationQueryExpansion].FallbackOutcome != "deterministic_plan" ||
		stageFailures[query.AIOperationAnswerSynthesis].FailureClass != "authentication" ||
		stageFailures[query.AIOperationAnswerSynthesis].FallbackOutcome != "retrieval_only" {
		t.Fatalf("invalid-profile stage receipts = %+v, want bounded failure class and fallback for both AI stages", invalidReceipt.Receipt().Stages)
	}
	var metrics metricdata.ResourceMetrics
	if err := metricReader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	metricCounts := map[string]int64{}
	for _, scope := range metrics.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name != "lwc.query.ai_operation_failures" {
				continue
			}
			sum, ok := instrument.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("query failure metric data = %T, want sum", instrument.Data)
			}
			for _, point := range sum.DataPoints {
				stage, _ := point.Attributes.Value(attribute.Key("stage"))
				class, _ := point.Attributes.Value(attribute.Key("failure_class"))
				fallback, _ := point.Attributes.Value(attribute.Key("fallback_outcome"))
				metricCounts[stage.AsString()+"/"+class.AsString()+"/"+fallback.AsString()] = point.Value
			}
		}
	}
	if metricCounts["query_expansion/authentication/deterministic_plan"] != 1 ||
		metricCounts["answer_synthesis/authentication/retrieval_only"] != 1 {
		t.Fatalf("recovered Query failure metric counts = %#v, want both typed stage/fallback series", metricCounts)
	}

	valid := newFixtureProfile(t, "valid-project-profile", "synthetic-valid-fixture-key")
	validPool, err := llm.NewProfilePoolClient([]*llm.ProfileClient{valid}, nil)
	if err != nil {
		valid.Close()
		t.Fatal(err)
	}
	defer validPool.Close()
	validCtx, validReceipt := query.WithReceipt(context.Background())
	validCtx = llm.WithProjectChatClient(validCtx, validPool)
	validResult, err := runtime.Execute(validCtx, reader, request)
	if err != nil {
		t.Fatalf("valid-profile Execute() error = %v", err)
	}
	query.FinishReceipt(validReceipt)
	if len(validResult.Results) != 1 || validResult.Results[0].Slug != invalidResult.Results[0].Slug || strings.TrimSpace(validResult.AISynth) == "" ||
		len(validResult.Citations) != 1 || validResult.Citations[0].Slug != "byok-profile-routing" || len(validResult.AIOperationFailures) != 0 || validResult.DisclosureRequired {
		t.Fatalf("valid-profile result = %+v, want same retrieval plus cited evidence answer and no failure warning", validResult)
	}

	expansionProfile := newFixtureProfile(t, "invalid-expansion-profile", "synthetic-invalid-expansion-fixture-key")
	expansionPool, err := llm.NewProfilePoolClient([]*llm.ProfileClient{expansionProfile}, nil)
	if err != nil {
		expansionProfile.Close()
		t.Fatal(err)
	}
	defer expansionPool.Close()
	expansionCtx, expansionReceipt := query.WithReceipt(context.Background())
	expansionCtx = llm.WithProjectChatClient(expansionCtx, expansionPool)
	expansionResult, err := runtime.Execute(expansionCtx, reader, request)
	if err != nil {
		t.Fatalf("expansion-failure Execute() error = %v", err)
	}
	query.FinishReceipt(expansionReceipt)
	wantExpansionFailure := []query.AIOperationFailure{{
		Stage: query.AIOperationQueryExpansion, Class: query.AIFailureInvalidResponse, Fallback: query.AIFallbackDeterministicPlan,
	}}
	if expansionResult.Status != "ok" || len(expansionResult.Results) != 1 || expansionResult.Results[0].Slug != "byok-profile-routing" ||
		strings.TrimSpace(expansionResult.AISynth) == "" || len(expansionResult.Citations) != 1 ||
		!reflect.DeepEqual(expansionResult.AIOperationFailures, wantExpansionFailure) {
		t.Fatalf("expansion-failure result = %+v, want cited synthesis with only the failed expansion disclosed", expansionResult)
	}
	expansionHostCalls := expansionReceipt.Receipt().HostCalls
	seenExpansionFailure, seenSynthesisSuccess := false, false
	for _, call := range expansionHostCalls {
		if call.ProfileID != "invalid-expansion-profile" {
			t.Fatalf("expansion-only failure used unexpected profile: %+v", call)
		}
		if call.Stage == "query_expansion" && call.Outcome == string(llm.ProviderResponseInvalid) {
			seenExpansionFailure = true
		}
		if call.Stage == "answer_synthesis" && call.Outcome == "success" {
			seenSynthesisSuccess = true
		}
	}
	if !seenExpansionFailure || !seenSynthesisSuccess {
		t.Fatalf("expansion-only host calls = %+v, want malformed expansion response followed by successful synthesis", expansionHostCalls)
	}

	providerReceipts, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(providerReceipts), "synthetic-invalid-fixture-key") || strings.Contains(string(providerReceipts), "synthetic-valid-fixture-key") ||
		!strings.Contains(string(providerReceipts), "/v1/chat/completions") {
		t.Fatalf("provider fixture receipts must prove HTTP calls without exposing keys: %q", providerReceipts)
	}
}

func newFixtureProfile(t *testing.T, id, apiKey string) *llm.ProfileClient {
	t.Helper()
	profile, err := llm.NewProfileClient(llm.ProfileConfig{
		ID: id, Revision: 1, Provider: "custom", Protocol: llm.ProtocolOpenAIChatCompletions,
		Endpoint: "https://fixture.example/v1", Model: "fixture-model",
	}, apiKey, llm.ProfileClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}
