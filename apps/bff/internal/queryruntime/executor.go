// Package queryruntime owns the immutable production runtime wiring for sealed
// query stage configurations.
package queryruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"github.com/rayer/llm-wiki-bff/internal/queryquality"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var ErrIdentityProviderRequired = errors.New("query runtime identity provider required")
var ErrIdentityUnavailable = errors.New("query runtime identity unavailable")

type Executor struct {
	resolver            *queryconfig.Resolver
	conceptCache        *cache.Cache
	services            map[string]query.Executor
	compositionServices map[string]query.Executor
	readback            query.RuntimeConfigReadback
}

type stageCallIdentityRecorder struct {
	mu           sync.Mutex
	expansion    llm.ModelIdentity
	synthesis    llm.ModelIdentity
	lastProfile  llm.ProfileIdentity
	hasExpand    bool
	hasSynthesis bool
	hasProfile   bool
}

func (r *stageCallIdentityRecorder) RecordCallIdentity(ctx context.Context, model llm.ModelIdentity, profile llm.ProfileIdentity) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch llm.CallPurpose(ctx) {
	case "expansion":
		r.expansion, r.hasExpand = model, true
	case "evidence_answer", "model_prior":
		r.synthesis, r.hasSynthesis = model, true
	}
	if profile.ID != "" {
		r.lastProfile, r.hasProfile = profile, true
	}
}

func (r *stageCallIdentityRecorder) snapshot() (llm.ModelIdentity, bool, llm.ModelIdentity, bool, llm.ProfileIdentity, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.expansion, r.hasExpand, r.synthesis, r.hasSynthesis, r.lastProfile, r.hasProfile
}

func NewExecutor(config queryconfig.Config, conceptCache *cache.Cache, expansionProvider queryquality.ChatProvider, legacy query.Executor, synthesizer query.Synthesizer) (*Executor, error) {
	return newExecutor(config, conceptCache, expansionProvider, legacy, synthesizer, queryquality.NewStrictProductionExecutorWithQueryServiceConfig)
}

type productionExecutorFactory func(*cache.Cache, queryquality.ChatProvider, query.Executor, query.Synthesizer, queryquality.RetrievalProfile, string, queryquality.Options, query.RuntimeConfigIdentity) (query.Executor, error)

func newExecutor(config queryconfig.Config, conceptCache *cache.Cache, expansionProvider queryquality.ChatProvider, legacy query.Executor, synthesizer query.Synthesizer, factory productionExecutorFactory) (*Executor, error) {
	resolver, err := queryconfig.NewResolver(config)
	if err != nil {
		return nil, err
	}
	effectiveConfigs := resolver.EffectiveConfigs()
	for _, effective := range effectiveConfigs {
		if err := validateIdentities(effective, expansionProvider, synthesizer); err != nil {
			return nil, err
		}
	}
	services := make(map[string]query.Executor)
	compositionServices := make(map[string]query.Executor)
	for _, effective := range effectiveConfigs {
		composition := serviceKey(effective)
		service, exists := compositionServices[composition]
		if !exists {
			service, err = factory(
				conceptCache, expansionProvider, legacy, synthesizer,
				effective.Profile, effective.PromptID, effective.Options, effective.RuntimeConfigIdentity(),
			)
			if err != nil {
				return nil, err
			}
			compositionServices[composition] = service
		}
		services[effective.EffectiveConfigDigest] = service
	}
	defaultConfig := effectiveConfigs[0]
	return &Executor{
		resolver: resolver, conceptCache: conceptCache, services: services, compositionServices: compositionServices,
		readback: query.RuntimeConfigReadback{
			SchemaVersion: defaultConfig.SchemaVersion, ConfigRevision: defaultConfig.ConfigRevision, ConfigDigest: defaultConfig.ConfigDigest,
			QueryServiceImplementation: defaultConfig.QueryServiceImplementation,
			DefaultProfileID:           defaultConfig.Profile.ID, DefaultProfileDigest: defaultConfig.ProfileDigest,
			DefaultPromptID: defaultConfig.PromptID, DefaultPromptDigest: defaultConfig.PromptDigest,
			ExpansionProvider: defaultConfig.ExpansionProvider, ExpansionModel: defaultConfig.ExpansionModel,
			ExpansionReasoning: defaultConfig.ExpansionReasoning, ExpansionTemperature: defaultConfig.ExpansionTemperature,
			SynthesisProvider: defaultConfig.SynthesisProvider, SynthesisModel: defaultConfig.SynthesisModel,
			SynthesisReasoning: defaultConfig.SynthesisReasoning, SynthesisTemperature: defaultConfig.SynthesisTemperature,
			NoEvidencePolicy: defaultConfig.NoEvidencePolicy,
			Options: query.RuntimeQueryOptions{
				SelectionLimit: defaultConfig.Options.SelectionLimit, ExplorationSlots: defaultConfig.Options.ExplorationSlots,
				EvidenceThreshold: defaultConfig.Options.EvidenceThreshold, KeywordsPerAttempt: defaultConfig.Options.KeywordsPerAttempt,
				ExpansionAttempts: defaultConfig.Options.ExpansionAttempts, RareDocumentFrequency: defaultConfig.Options.RareDocumentFrequency,
			},
			BindingCount: len(config.ProjectBindings), DistinctServiceCompositionCount: len(compositionServices),
		},
	}, nil
}

func validateIdentities(effective queryconfig.EffectiveConfig, expansionProvider queryquality.ChatProvider, synthesizer query.Synthesizer) error {
	provider, ok := expansionProvider.(llm.ModelIdentityProvider)
	if !ok || provider == nil {
		return fmt.Errorf("%w: expansion provider identity unavailable", ErrIdentityUnavailable)
	}
	expansion, ok := provider.ModelIdentity()
	if !ok {
		return fmt.Errorf("%w: expansion provider identity unavailable", ErrIdentityUnavailable)
	}
	if expansion.Provider != effective.ExpansionProvider || expansion.Model != effective.ExpansionModel || expansion.Reasoning != effective.ExpansionReasoning || expansion.Temperature != effective.ExpansionTemperature {
		return fmt.Errorf("%w: expansion provider identity mismatch", ErrIdentityUnavailable)
	}
	if synthesizer == nil {
		return fmt.Errorf("%w: synthesizer identity unavailable", ErrIdentityUnavailable)
	}
	synthesis, ok := synthesizer.ModelIdentity()
	if !ok {
		return fmt.Errorf("%w: synthesizer identity unavailable", ErrIdentityUnavailable)
	}
	if synthesis.Provider != effective.SynthesisProvider || synthesis.Model != effective.SynthesisModel || synthesis.Reasoning != effective.SynthesisReasoning || synthesis.Temperature != effective.SynthesisTemperature {
		return fmt.Errorf("%w: synthesizer identity mismatch", ErrIdentityUnavailable)
	}
	return nil
}

func New(config queryconfig.Config, conceptCache *cache.Cache, expansionProvider queryquality.ChatProvider, legacy query.Executor, synthesizer query.Synthesizer) (*Executor, error) {
	return NewExecutor(config, conceptCache, expansionProvider, legacy, synthesizer)
}

func (e *Executor) ServiceCount() int {
	if e == nil {
		return 0
	}
	return len(e.services)
}

func (e *Executor) Readback() query.RuntimeConfigReadback {
	if e == nil {
		return query.RuntimeConfigReadback{}
	}
	return e.readback
}

func (e *Executor) Execute(ctx context.Context, reader cache.Reader, request query.Request) (query.Result, error) {
	if e == nil || e.resolver == nil {
		return query.Result{}, ErrIdentityUnavailable
	}
	resolveCtx, resolveSpan := otel.Tracer("llm-wiki-bff/query").Start(ctx, "query.runtime.resolve")
	finishResolve := func(err error) {
		outcome := "success"
		if err != nil {
			outcome = "failure"
			if errors.Is(err, context.Canceled) {
				outcome = "canceled"
			} else if errors.Is(err, context.DeadlineExceeded) {
				outcome = "timeout"
			}
			resolveSpan.SetStatus(codes.Error, outcome)
		}
		resolveSpan.SetAttributes(attribute.String("query.outcome", outcome))
		resolveSpan.End()
	}
	identityReader, ok := reader.(storage.QueryGenerationIdentityProvider)
	if !ok || identityReader == nil {
		finishResolve(ErrIdentityProviderRequired)
		return query.Result{}, ErrIdentityProviderRequired
	}
	storageIdentity, err := identityReader.QueryGenerationIdentity(resolveCtx)
	if err != nil {
		finishResolve(err)
		return query.Result{}, fmt.Errorf("%w: %w", ErrIdentityUnavailable, err)
	}
	identity := queryconfig.GenerationIdentity{ProjectID: storageIdentity.ProjectID, GenerationID: storageIdentity.GenerationID, ConceptsDigest: storageIdentity.ConceptsDigest}
	effective, err := e.resolver.Resolve(identity)
	if err != nil {
		finishResolve(err)
		return query.Result{}, err
	}
	service, ok := e.services[effective.EffectiveConfigDigest]
	if !ok {
		service, ok = e.compositionServices[serviceKey(effective)]
	}
	if !ok {
		finishResolve(ErrIdentityUnavailable)
		return query.Result{}, ErrIdentityUnavailable
	}
	projectClient, hasProjectClient := llm.ProjectChatClientFromContext(ctx)
	runtimeIdentity := effective.RuntimeConfigIdentity()
	if hasProjectClient {
		modelIdentity, ok := projectClient.ModelIdentity()
		if !ok || strings.TrimSpace(modelIdentity.Provider) == "" || strings.TrimSpace(modelIdentity.Model) == "" {
			finishResolve(ErrIdentityUnavailable)
			return query.Result{}, ErrIdentityUnavailable
		}
		runtimeIdentity.ExpansionProvider = modelIdentity.Provider
		runtimeIdentity.ExpansionModel = modelIdentity.Model
		runtimeIdentity.ExpansionReasoning = modelIdentity.Reasoning
		runtimeIdentity.ExpansionTemperature = modelIdentity.Temperature
		runtimeIdentity.SynthesisProvider = modelIdentity.Provider
		runtimeIdentity.SynthesisModel = modelIdentity.Model
		runtimeIdentity.SynthesisReasoning = modelIdentity.Reasoning
		runtimeIdentity.SynthesisTemperature = modelIdentity.Temperature
		if profileIdentity, ok := projectClient.(llm.ProfileIdentityProvider); ok {
			identity := profileIdentity.ProfileIdentity()
			runtimeIdentity.LLMProfileID = identity.ID
			runtimeIdentity.LLMProfileRevision = identity.Revision
			runtimeIdentity.LLMProtocol = identity.Protocol
		}
		synthesizer := query.NewProjectService(e.conceptCache, projectClient)
		service, err = queryquality.NewStrictProductionExecutorWithQueryServiceConfig(
			e.conceptCache, projectClient, nil, synthesizer, effective.Profile, effective.PromptID, effective.Options, runtimeIdentity,
		)
		if err != nil {
			finishResolve(err)
			return query.Result{}, err
		}
	}
	finishResolve(nil)
	callIdentities := &stageCallIdentityRecorder{}
	executeCtx := llm.WithCallIdentityRecorder(ctx, callIdentities)
	result, err := service.Execute(query.WithRuntimeConfigIdentity(executeCtx, runtimeIdentity), reader, request)
	if err != nil {
		return query.Result{}, err
	}
	if hasProjectClient {
		expansionIdentity, hasExpansion, synthesisIdentity, hasSynthesis, lastProfile, hasProfile := callIdentities.snapshot()
		if hasExpansion {
			runtimeIdentity.ExpansionProvider, runtimeIdentity.ExpansionModel = expansionIdentity.Provider, expansionIdentity.Model
			runtimeIdentity.ExpansionReasoning, runtimeIdentity.ExpansionTemperature = expansionIdentity.Reasoning, expansionIdentity.Temperature
		}
		if hasSynthesis {
			runtimeIdentity.SynthesisProvider, runtimeIdentity.SynthesisModel = synthesisIdentity.Provider, synthesisIdentity.Model
			runtimeIdentity.SynthesisReasoning, runtimeIdentity.SynthesisTemperature = synthesisIdentity.Reasoning, synthesisIdentity.Temperature
		}
		if hasProfile {
			identity := lastProfile
			runtimeIdentity.LLMProfileID = identity.ID
			runtimeIdentity.LLMProfileRevision = identity.Revision
			runtimeIdentity.LLMProtocol = identity.Protocol
		} else if profileIdentity, ok := projectClient.(llm.ProfileIdentityProvider); ok {
			identity := profileIdentity.ProfileIdentity()
			runtimeIdentity.LLMProfileID = identity.ID
			runtimeIdentity.LLMProfileRevision = identity.Revision
			runtimeIdentity.LLMProtocol = identity.Protocol
		}
	}
	result.RuntimeConfigIdentity = query.CloneRuntimeConfigIdentity(&runtimeIdentity)
	return result, nil
}

func serviceKey(config queryconfig.EffectiveConfig) string {
	// The production service is independent of pinned corpus and binding provenance.
	return config.EffectiveConfigDigestWithoutGeneration()
}
