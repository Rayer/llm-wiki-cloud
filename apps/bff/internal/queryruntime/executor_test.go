package queryruntime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"github.com/rayer/llm-wiki-bff/internal/queryquality"
	"github.com/rayer/llm-wiki-bff/internal/queryruntime"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type runtimeReader struct {
	mu            sync.Mutex
	identity      storage.QueryGenerationIdentity
	reads         int
	identityReads int
}

func (r *runtimeReader) Prefix() string { return "users/test/projects/" + r.identity.ProjectID }
func (r *runtimeReader) ReadFile(_ context.Context, path string) ([]byte, error) {
	r.mu.Lock()
	r.reads++
	r.mu.Unlock()
	if path == "cache/id_map.json" {
		return []byte(`{"concept":{"abcdef123456":"deploy"}}`), nil
	}
	return []byte(`{"slug":"deploy","title":"Deploy","body":"deploy docs"}` + "\n"), nil
}
func (r *runtimeReader) ListConcepts(context.Context, bool) ([]gcs.WikiPage, error) { return nil, nil }
func (r *runtimeReader) GetPage(context.Context, string, string) (*gcs.WikiPage, []byte, error) {
	return nil, nil, errors.New("unexpected page read")
}
func (r *runtimeReader) QueryGenerationIdentity(context.Context) (storage.QueryGenerationIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.identityReads++
	return r.identity, nil
}

type countingProvider struct {
	mu           sync.Mutex
	calls        int
	system, user string
}

type failingProvider struct{}

func (failingProvider) Chat(context.Context, string, string) (string, error) {
	return "", errors.New("provider failed")
}

func (failingProvider) ModelIdentity() (llm.ModelIdentity, bool) {
	return llm.ModelIdentity{Provider: queryconfig.ProviderDeepSeek, Model: "deepseek-flash", Reasoning: "none", Temperature: 0}, true
}

func (p *countingProvider) Chat(_ context.Context, system, user string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.system, p.user = system, user
	return `{"raw_query":"deploy docs","required":[],"excluded":[],"preferred":[{"kind":"topic","value":"deploy","terms":["deploy"],"proof":"lexical"}],"goals":[],"supporting_dimensions":[],"acceptable_alternatives":[],"ambiguity":[],"fallback":false}`, nil
}

func (p *countingProvider) ModelIdentity() (llm.ModelIdentity, bool) {
	return llm.ModelIdentity{Provider: queryconfig.ProviderDeepSeek, Model: "deepseek-flash", Reasoning: "none", Temperature: 0}, true
}

type countingLegacy struct {
	mu    sync.Mutex
	calls int
}

func (e *countingLegacy) Execute(_ context.Context, _ cache.Reader, _ query.Request) (query.Result, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return query.Result{Status: "legacy"}, nil
}

func TestRuntimePrebuildsAndRoutesExactAndDefault(t *testing.T) {
	config := runtimeConfig(t)
	provider := &countingProvider{}
	legacy := &countingLegacy{}
	runtime, err := queryruntime.NewExecutor(config, cache.New(), provider, legacy, runtimeSynthesizer())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ServiceCount() != 2 {
		t.Fatalf("service count=%d, want 2", runtime.ServiceCount())
	}
	exactReader := &runtimeReader{identity: storage.QueryGenerationIdentity{ProjectID: "project-a", GenerationID: "generation-7", ConceptsDigest: "sha256:" + strings.Repeat("a", 64)}}
	result, err := runtime.Execute(context.Background(), exactReader, query.Request{Query: "deploy docs", Mode: "wiki"})
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeConfigIdentity == nil || !result.RuntimeConfigIdentity.ExactBinding || result.RuntimeConfigIdentity.ProfileID != "technical-v1" {
		t.Fatalf("identity=%+v", result.RuntimeConfigIdentity)
	}
	if provider.calls != 3 || legacy.calls != 0 {
		t.Fatalf("provider=%d legacy=%d", provider.calls, legacy.calls)
	}
	defaultReader := &runtimeReader{identity: storage.QueryGenerationIdentity{ProjectID: "unlisted", GenerationID: "legacy"}}
	result, err = runtime.Execute(context.Background(), defaultReader, query.Request{Query: "deploy docs", Mode: "wiki"})
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeConfigIdentity == nil || result.RuntimeConfigIdentity.ExactBinding || result.RuntimeConfigIdentity.BindingSource != queryconfig.SourceLegacyCompatibility {
		t.Fatalf("default identity=%+v", result.RuntimeConfigIdentity)
	}
	if result.RuntimeConfigIdentity.GenerationID != "legacy" {
		t.Fatalf("default generation=%q", result.RuntimeConfigIdentity.GenerationID)
	}
}

func TestRuntimeResolveSpanUsesRequestParentAndReadsPinnedIdentityOnce(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	runtime, err := queryruntime.NewExecutor(runtimeConfig(t), cache.New(), &countingProvider{}, &countingLegacy{}, runtimeSynthesizer())
	if err != nil {
		t.Fatal(err)
	}
	reader := &runtimeReader{identity: storage.QueryGenerationIdentity{ProjectID: "project-a", GenerationID: "generation-7", ConceptsDigest: "sha256:" + strings.Repeat("a", 64)}}
	rootCtx, root := otel.Tracer("llm-wiki-bff/query").Start(context.Background(), "query.request")
	if _, err := runtime.Execute(rootCtx, reader, query.Request{Query: "deploy docs", Mode: "wiki"}); err != nil {
		t.Fatal(err)
	}
	root.End()

	var resolve, cacheStage tracetest.SpanStub
	storageParents := make([]trace.SpanID, 0)
	for _, span := range exporter.GetSpans() {
		switch span.Name {
		case "query.runtime.resolve":
			resolve = span
		case "query.stage.cache_load":
			cacheStage = span
		case "query.storage.read":
			storageParents = append(storageParents, span.Parent.SpanID())
		}
	}
	if !resolve.SpanContext.SpanID().IsValid() || !cacheStage.SpanContext.SpanID().IsValid() {
		t.Fatalf("runtime resolve/cache stage spans missing: %+v", exporter.GetSpans())
	}
	if resolve.Parent.SpanID() != root.SpanContext().SpanID() || cacheStage.Parent.SpanID() != root.SpanContext().SpanID() {
		t.Fatalf("runtime resolution leaked into query work parentage: root=%s resolveParent=%s cacheParent=%s", root.SpanContext().SpanID(), resolve.Parent.SpanID(), cacheStage.Parent.SpanID())
	}
	storageReadsUnderResolve := 0
	for _, parentID := range storageParents {
		if parentID == resolve.SpanContext.SpanID() {
			storageReadsUnderResolve++
		}
	}
	if reader.identityReads != 1 || storageReadsUnderResolve != 0 {
		t.Fatalf("pinned identity reads=%d storage children of resolution=%d, want one local identity lookup and no manifest reread", reader.identityReads, storageReadsUnderResolve)
	}
	for _, attr := range resolve.Attributes {
		if string(attr.Key) == "query.outcome" && attr.Value.AsString() != "success" {
			t.Fatalf("runtime resolution outcome=%q", attr.Value.AsString())
		}
	}
}

func TestRuntimeMismatchAndUnknownReaderHaveZeroPipelineEffects(t *testing.T) {
	runtime, err := queryruntime.NewExecutor(runtimeConfig(t), cache.New(), &countingProvider{}, &countingLegacy{}, runtimeSynthesizer())
	if err != nil {
		t.Fatal(err)
	}
	reader := &runtimeReader{identity: storage.QueryGenerationIdentity{ProjectID: "project-a", GenerationID: "wrong-generation", ConceptsDigest: "sha256:" + strings.Repeat("a", 64)}}
	if _, err := runtime.Execute(context.Background(), reader, query.Request{Query: "private-query"}); !errors.Is(err, queryconfig.ErrBindingMismatch) {
		t.Fatalf("err=%v", err)
	}
	reader.mu.Lock()
	reads := reader.reads
	reader.mu.Unlock()
	if reads != 0 {
		t.Fatalf("cache reads=%d, want 0", reads)
	}
	if _, err := runtime.Execute(context.Background(), &unknownReader{}, query.Request{Query: "private-query"}); !errors.Is(err, queryruntime.ErrIdentityProviderRequired) {
		t.Fatalf("unknown reader err=%v", err)
	}
}

func TestConfiguredRuntimeExpansionFailureDoesNotCallLegacyExecutor(t *testing.T) {
	legacy := &countingLegacy{}
	runtime, err := queryruntime.NewExecutor(runtimeConfig(t), cache.New(), failingProvider{}, legacy, runtimeSynthesizer())
	if err != nil {
		t.Fatal(err)
	}
	reader := &runtimeReader{identity: storage.QueryGenerationIdentity{ProjectID: "project-a", GenerationID: "generation-7", ConceptsDigest: "sha256:" + strings.Repeat("a", 64)}}
	if _, err := runtime.Execute(context.Background(), reader, query.Request{Query: "deploy docs", Mode: "wiki"}); err != nil {
		t.Fatal(err)
	}
	if legacy.calls != 0 {
		t.Fatalf("legacy calls=%d, want zero", legacy.calls)
	}
}

type unknownReader struct{}

func (*unknownReader) Prefix() string                                             { return "unknown" }
func (*unknownReader) ListConcepts(context.Context, bool) ([]gcs.WikiPage, error) { return nil, nil }
func (*unknownReader) GetPage(context.Context, string, string) (*gcs.WikiPage, []byte, error) {
	return nil, nil, errors.New("unexpected")
}

func TestRuntimeTechnicalPromptMatchesSharedProductionRenderer(t *testing.T) {
	config := runtimeConfig(t)
	provider := &countingProvider{}
	runtime, err := queryruntime.NewExecutor(config, cache.New(), provider, &countingLegacy{}, runtimeSynthesizer())
	if err != nil {
		t.Fatal(err)
	}
	reader := &runtimeReader{identity: storage.QueryGenerationIdentity{ProjectID: "project-a", GenerationID: "generation-7", ConceptsDigest: "sha256:" + strings.Repeat("a", 64)}}
	if _, err := runtime.Execute(context.Background(), reader, query.Request{Query: "deploy docs", Mode: "wiki"}); err != nil {
		t.Fatal(err)
	}
	profile := queryquality.RetrievalProfile{ID: "technical-v1", CriterionPolicy: queryquality.CriterionPolicy{RequiredWhenExplicit: []string{"component"}, PreferredByDefault: []string{"technology"}, GoalsToExpand: []string{"explanation"}}}
	want, err := queryquality.RenderPrompt(queryquality.DomainNeutralTechnicalPromptID, "deploy docs", profile.CriterionPolicy, 24)
	if err != nil {
		t.Fatal(err)
	}
	if provider.system != want.System || provider.user != want.User {
		t.Fatalf("prompt mismatch system=%q user=%q", provider.system, provider.user)
	}
}

type quotaSwitchProjectClient struct {
	mu             sync.Mutex
	currentModel   llm.ModelIdentity
	currentProfile llm.ProfileIdentity
}

var (
	quotaProfileA = llm.ProfileIdentity{ID: "profile-a", Revision: 1, Provider: "custom", Protocol: string(llm.ProtocolOpenAIChatCompletions), Model: "model-a"}
	quotaProfileB = llm.ProfileIdentity{ID: "profile-b", Revision: 2, Provider: "custom", Protocol: string(llm.ProtocolOpenAIChatCompletions), Model: "model-b"}
)

func newQuotaSwitchProjectClient() *quotaSwitchProjectClient {
	return &quotaSwitchProjectClient{
		currentModel:   llm.ModelIdentity{Provider: "custom", Model: quotaProfileA.Model},
		currentProfile: quotaProfileA,
	}
}

func (c *quotaSwitchProjectClient) ModelIdentity() (llm.ModelIdentity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentModel, true
}

func (c *quotaSwitchProjectClient) ProfileIdentity() llm.ProfileIdentity {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentProfile
}

func (c *quotaSwitchProjectClient) Chat(ctx context.Context, _, _ string) (string, error) {
	recorder := query.ReceiptRecorderFromContext(ctx)
	call := func(profile llm.ProfileIdentity, outcome string) {
		if recorder == nil {
			return
		}
		finish := recorder.StartProfileCallAtContext(ctx, llm.ProfileConfig{
			ID: profile.ID, Revision: profile.Revision, Provider: profile.Provider,
			Protocol: llm.Protocol(profile.Protocol), Endpoint: "https://" + profile.Model + ".example/v1", Model: profile.Model,
		}, llm.CallStage(ctx), "https://"+profile.Model+".example/v1/chat/completions")
		finish(outcome, outcome, time.Now())
	}
	switch llm.CallPurpose(ctx) {
	case "expansion":
		call(quotaProfileA, "success")
		llm.RecordCallIdentity(ctx, llm.ModelIdentity{Provider: "custom", Model: quotaProfileA.Model}, quotaProfileA)
		return `{"raw_query":"deploy docs","required":[],"excluded":[],"preferred":[{"kind":"topic","value":"deploy docs","terms":["deploy","docs"],"proof":"lexical"}],"goals":[],"supporting_dimensions":[],"acceptable_alternatives":[],"ambiguity":[],"fallback":false}`, nil
	case "model_prior", "evidence_answer":
		call(quotaProfileA, "quota_exhausted")
		call(quotaProfileB, "success")
		c.mu.Lock()
		c.currentModel = llm.ModelIdentity{Provider: "custom", Model: quotaProfileB.Model}
		c.currentProfile = quotaProfileB
		c.mu.Unlock()
		llm.RecordCallIdentity(ctx, llm.ModelIdentity{Provider: "custom", Model: quotaProfileB.Model}, quotaProfileB)
		return "Answer from profile B.", nil
	default:
		return "", errors.New("unexpected Project provider call purpose")
	}
}

// TestRuntimeAggregatesInjectedExpansionAndSynthesisIdentityAcrossQuotaFallback
// exercises the runtime's aggregation/consumer behavior with a stub that
// injects host-call and identity records; provider transport is covered in
// the llm package's ProfilePoolClient transport regression.
func TestRuntimeAggregatesInjectedExpansionAndSynthesisIdentityAcrossQuotaFallback(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	config := runtimeConfig(t)
	config.Stages.AnswerSynthesizer.NoEvidencePolicy = queryconfig.ModelPriorFallbackPolicy
	config.ConfigDigest = ""
	var err error
	config, err = queryconfig.Seal(config)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := queryruntime.NewExecutor(config, cache.New(), &countingProvider{}, &countingLegacy{}, runtimeSynthesizer())
	if err != nil {
		t.Fatal(err)
	}
	projectClient := newQuotaSwitchProjectClient()
	ctx, receipt := query.WithReceipt(context.Background())
	ctx = llm.WithProjectChatClient(ctx, projectClient)
	reader := &runtimeReader{identity: storage.QueryGenerationIdentity{ProjectID: "project-a", GenerationID: "generation-7", ConceptsDigest: "sha256:" + strings.Repeat("a", 64)}}
	result, err := runtime.Execute(ctx, reader, query.Request{Query: "deploy docs", Mode: "full"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	query.FinishReceipt(receipt)
	identity := result.RuntimeConfigIdentity
	if identity == nil || identity.ExpansionProvider != "custom" || identity.ExpansionModel != "model-a" || identity.SynthesisProvider != "custom" || identity.SynthesisModel != "model-b" {
		t.Fatalf("response stage identity = %+v, want expansion A and synthesis B", identity)
	}
	if result.AISynth != "Answer from profile B." {
		t.Fatalf("response synthesis = %q", result.AISynth)
	}

	hostCalls := receipt.Receipt().HostCalls
	seenExpansionA, seenQuotaA, seenSynthesisB := false, false, false
	for _, hostCall := range hostCalls {
		switch {
		case hostCall.Stage == "query_expansion" && hostCall.ProfileID == quotaProfileA.ID && hostCall.Model == quotaProfileA.Model:
			seenExpansionA = true
		case hostCall.Stage == "answer_synthesis" && hostCall.ProfileID == quotaProfileA.ID && hostCall.Outcome == "quota_exhausted":
			seenQuotaA = true
		case hostCall.Stage == "answer_synthesis" && hostCall.ProfileID == quotaProfileB.ID && hostCall.Model == quotaProfileB.Model && hostCall.Outcome == "success":
			seenSynthesisB = true
		}
	}
	if !seenExpansionA || !seenQuotaA || !seenSynthesisB {
		t.Fatalf("host-call receipt missed A/B fallback: %+v", hostCalls)
	}

	seenTraceA, seenTraceB := false, false
	for _, span := range exporter.GetSpans() {
		if span.Name != "query.host_call" {
			continue
		}
		profileID, model := "", ""
		for _, attr := range span.Attributes {
			switch string(attr.Key) {
			case "llm.profile.id":
				profileID = attr.Value.AsString()
			case "query.model":
				model = attr.Value.AsString()
			}
		}
		if profileID == quotaProfileA.ID && model == quotaProfileA.Model {
			seenTraceA = true
		}
		if profileID == quotaProfileB.ID && model == quotaProfileB.Model {
			seenTraceB = true
		}
	}
	if !seenTraceA || !seenTraceB {
		t.Fatalf("trace host-call spans missed A/B profiles: %+v", exporter.GetSpans())
	}
}

func TestRuntimeConcurrentMixedGenerationAndRollbackRoutingIsRaceSafe(t *testing.T) {
	provider := &countingProvider{}
	runtime, err := queryruntime.NewExecutor(runtimeConfig(t), cache.New(), provider, &countingLegacy{}, runtimeSynthesizer())
	if err != nil {
		t.Fatal(err)
	}
	identities := []storage.QueryGenerationIdentity{
		{ProjectID: "project-a", GenerationID: "generation-7", ConceptsDigest: "sha256:" + strings.Repeat("a", 64)},
		{ProjectID: "unlisted", GenerationID: "legacy"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		identity := identities[i%len(identities)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader := &runtimeReader{identity: identity}
			result, err := runtime.Execute(context.Background(), reader, query.Request{Query: "deploy docs", Mode: "wiki"})
			if err != nil {
				errs <- err
				return
			}
			if result.RuntimeConfigIdentity == nil || result.RuntimeConfigIdentity.GenerationID != identity.GenerationID {
				errs <- errors.New("concurrent route returned the wrong generation identity")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func runtimeConfig(t *testing.T) queryconfig.Config {
	t.Helper()
	base := queryquality.DefaultRetrievalProfile()
	defaultDigest, _ := base.Digest()
	technical := queryquality.RetrievalProfile{ID: "technical-v1", CriterionPolicy: queryquality.CriterionPolicy{RequiredWhenExplicit: []string{"component"}, PreferredByDefault: []string{"technology"}, GoalsToExpand: []string{"explanation"}}}
	technicalDigest, _ := technical.Digest()
	lifestylePrompt, _ := queryquality.LookupPrompt(queryquality.StructuredPlanPromptID)
	technicalPrompt, _ := queryquality.LookupPrompt(queryquality.DomainNeutralTechnicalPromptID)
	sealed, err := queryconfig.Seal(queryconfig.Config{SchemaVersion: 2, ConfigRevision: "rev", QueryServiceImplementation: queryconfig.QueryServiceImplementation,
		Stages:   queryconfig.Stages{QueryExpander: queryconfig.QueryExpanderStage{Provider: queryconfig.ProviderDeepSeek, Implementation: queryconfig.QueryExpanderImplementation, Model: "deepseek-flash", Reasoning: "none", Temperature: 0, DefaultProfileID: base.ID, DefaultProfileDigest: defaultDigest, DefaultPromptID: lifestylePrompt.ID, DefaultPromptDigest: lifestylePrompt.TemplateDigest, KeywordsPerAttempt: 24, Attempts: 3}, CandidateMatcher: queryconfig.CandidateMatcherStage{Implementation: queryconfig.CandidateMatcherImplementation, EvidenceThreshold: 2, RareKeywordMaxDocumentFrequency: 1}, ResultSelector: queryconfig.ResultSelectorStage{Implementation: queryconfig.ResultSelectorImplementation, Limit: 10, ExplorationSlots: 1, SeedPolicy: queryconfig.SeedPolicy}, AnswerSynthesizer: queryconfig.AnswerSynthesizerStage{Provider: queryconfig.ProviderDeepSeek, Implementation: queryconfig.AnswerSynthesizerImplementation, Model: "deepseek-flash", Reasoning: "none", Temperature: 0, NoEvidencePolicy: queryconfig.NoEvidencePolicy}},
		Profiles: []queryconfig.Profile{{ID: base.ID, CriterionPolicy: base.CriterionPolicy, ProfileDigest: defaultDigest}, {ID: technical.ID, CriterionPolicy: technical.CriterionPolicy, ProfileDigest: technicalDigest}}, ProjectBindings: []queryconfig.ProjectBinding{{ProjectID: "project-a", GenerationID: "generation-7", ConceptsDigest: "sha256:" + strings.Repeat("a", 64), ProfileID: technical.ID, ProfileDigest: technicalDigest, PromptID: technicalPrompt.ID, PromptDigest: technicalPrompt.TemplateDigest, Source: queryconfig.SourceCorpusDerivedApproximation}}})
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

type offlineSynthesizer struct{}

func (offlineSynthesizer) SynthesizeWithError(_ context.Context, _ cache.Reader, _ query.Request, result query.Result) (query.Result, error) {
	return result, nil
}

func (offlineSynthesizer) ModelIdentity() (llm.ModelIdentity, bool) {
	return llm.ModelIdentity{Provider: queryconfig.ProviderDeepSeek, Model: "deepseek-flash", Reasoning: "none", Temperature: 0}, true
}

func runtimeSynthesizer() query.Synthesizer {
	return offlineSynthesizer{}
}
