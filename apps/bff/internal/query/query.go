// Package query contains the transport-independent wiki query application service.
package query

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type runtimeConfigIdentityContextKey struct{}

const (
	ModelPriorFallbackPolicy = "full-model-prior-fallback-v1"
	ModelPriorPromptID       = "full-model-prior-v1"
)

func WithRuntimeConfigIdentity(ctx context.Context, identity RuntimeConfigIdentity) context.Context {
	return context.WithValue(ctx, runtimeConfigIdentityContextKey{}, identity)
}

func RuntimeConfigIdentityFromContext(ctx context.Context) (RuntimeConfigIdentity, bool) {
	identity, ok := ctx.Value(runtimeConfigIdentityContextKey{}).(RuntimeConfigIdentity)
	return identity, ok
}

// ErrCacheNotConfigured reports that the query service has no concept cache.
var ErrCacheNotConfigured = errors.New("concept cache is not configured")

// Request is the application input for one query.
type Request struct {
	Query          string
	Mode           string
	RequiredTagIDs []string
	Profile        *ProfileSnapshot
}

// Result is the application result for one query. Its fields are domain
// values; HTTP adapters are responsible for choosing a wire representation.
type Result struct {
	Query                 string
	Mode                  string
	Results               []search.Result
	Expand                *llm.ExpandResult
	AIOperationFailures   []AIOperationFailure `json:"ai_operation_failures,omitempty"`
	AISynth               string
	Citations             []search.Citation
	Status                string
	Reason                string
	AnswerBasis           string                 `json:"answer_basis,omitempty"`
	WikiEvidenceStatus    string                 `json:"wiki_evidence_status,omitempty"`
	DisclosureRequired    bool                   `json:"disclosure_required,omitempty"`
	RuntimeConfigIdentity *RuntimeConfigIdentity `json:"-"`
}

const (
	AIOperationQueryExpansion  = "query_expansion"
	AIOperationAnswerSynthesis = "answer_synthesis"

	AIFailureAuthentication      = "authentication"
	AIFailureRateLimited         = "rate_limited"
	AIFailureQuotaExhausted      = "quota_exhausted"
	AIFailureBilling             = "billing"
	AIFailureTimeout             = "timeout"
	AIFailureInvalidResponse     = "invalid_response"
	AIFailureOutputTruncated     = "output_truncated"
	AIFailureProviderUnavailable = "provider_unavailable"
	AIFailureProvider            = "provider_error"
	AIFailureContextUnavailable  = "context_unavailable"
	AIFailureCanceled            = "canceled"

	AIFallbackNone              = "none"
	AIFallbackRawQuery          = "raw_query"
	AIFallbackDeterministicPlan = "deterministic_plan"
	AIFallbackRetrievalOnly     = "retrieval_only"

	AIOperationSourcePersonal = "personal"
	AIOperationSourceShared   = "shared"
	AIOperationSourceUnknown  = "unknown"
)

// AIOperationFailure records a safe, bounded failure from an optional AI step
// and the fallback the query used to keep useful retrieval results available.
type AIOperationFailure struct {
	Stage    string `json:"stage"`
	Class    string `json:"class"`
	Fallback string `json:"fallback"`
	Source   string `json:"source,omitempty"`
}

func NewAIOperationFailure(stage string, err error, fallback string) (AIOperationFailure, bool) {
	return AIOperationFailureFromCategory(stage, AIOperationFailureClass(err), fallback)
}

func AIOperationFailureFromCategory(stage, category, fallback string) (AIOperationFailure, bool) {
	if stage != AIOperationQueryExpansion && stage != AIOperationAnswerSynthesis {
		return AIOperationFailure{}, false
	}
	class := AIOperationFailureClassFromCategory(category)
	if class == "" {
		return AIOperationFailure{}, false
	}
	switch fallback {
	case AIFallbackNone, AIFallbackRawQuery, AIFallbackDeterministicPlan, AIFallbackRetrievalOnly:
	default:
		return AIOperationFailure{}, false
	}
	return AIOperationFailure{Stage: stage, Class: class, Fallback: fallback}, true
}

func AIOperationFailureClass(err error) string {
	if errors.Is(err, context.Canceled) {
		return AIFailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return AIFailureTimeout
	}
	var profileErr *llm.ProfileCallError
	if errors.As(err, &profileErr) {
		switch profileErr.Kind {
		case llm.ProviderAuthentication:
			return AIFailureAuthentication
		case llm.ProviderRateLimited:
			return AIFailureRateLimited
		case llm.ProviderTimeout:
			return AIFailureTimeout
		case llm.ProviderBilling:
			return AIFailureBilling
		case llm.ProviderQuotaExhausted:
			return AIFailureQuotaExhausted
		case llm.ProviderResponseInvalid:
			return AIFailureInvalidResponse
		case llm.ProviderOutputTruncated:
			return AIFailureOutputTruncated
		default:
			return AIFailureProvider
		}
	}
	var providerErr *llm.ProviderCallError
	if errors.As(err, &providerErr) {
		return AIOperationFailureClassFromCategory(llm.SafeErrorCategory(err))
	}
	return AIFailureProvider
}

func AIOperationFailureClassFromCategory(category string) string {
	switch category {
	case "provider_auth_rejected", "provider_authentication", AIFailureAuthentication:
		return AIFailureAuthentication
	case "provider_rate_limited", AIFailureRateLimited:
		return AIFailureRateLimited
	case "quota_exhausted":
		return AIFailureQuotaExhausted
	case "provider_billing", AIFailureBilling:
		return AIFailureBilling
	case "provider_timeout", AIFailureTimeout:
		return AIFailureTimeout
	case AIFailureCanceled:
		return AIFailureCanceled
	case "provider_response_invalid", "provider_response_invalid_json", "invalid_plan", AIFailureInvalidResponse:
		return AIFailureInvalidResponse
	case "provider_output_truncated", AIFailureOutputTruncated:
		return AIFailureOutputTruncated
	case "provider_server_error", "provider_transport_or_response_error":
		return AIFailureProviderUnavailable
	case AIFailureProviderUnavailable:
		return AIFailureProviderUnavailable
	case AIFailureProvider:
		return AIFailureProvider
	case AIFailureContextUnavailable:
		return AIFailureContextUnavailable
	case "provider_not_configured":
		return ""
	default:
		return AIFailureProvider
	}
}

func (r *Result) addAIOperationFailure(failure AIOperationFailure) {
	if r == nil {
		return
	}
	for _, existing := range r.AIOperationFailures {
		if existing == failure {
			return
		}
	}
	r.AIOperationFailures = append(r.AIOperationFailures, failure)
}

// RuntimeConfigIdentity is the privacy-safe identity of the sealed runtime
// composition that produced a result. It intentionally contains no request,
// corpus, tenant, credential, or prompt-body data.
type RuntimeConfigIdentity struct {
	SchemaVersion              int     `json:"schema_version"`
	ConfigRevision             string  `json:"config_revision"`
	ConfigDigest               string  `json:"config_digest"`
	EffectiveConfigDigest      string  `json:"effective_config_digest"`
	QueryServiceImplementation string  `json:"query_service_implementation"`
	ProfileID                  string  `json:"profile_id"`
	ProfileDigest              string  `json:"profile_digest"`
	PromptID                   string  `json:"prompt_id"`
	PromptDigest               string  `json:"prompt_digest"`
	BindingSource              string  `json:"binding_source"`
	ExactBinding               bool    `json:"exact_binding"`
	GenerationID               string  `json:"generation_id"`
	ConceptsDigest             string  `json:"concepts_digest"`
	ExpansionProvider          string  `json:"expansion_provider"`
	ExpansionImplementation    string  `json:"expansion_implementation"`
	ExpansionModel             string  `json:"expansion_model"`
	ExpansionReasoning         string  `json:"expansion_reasoning"`
	ExpansionTemperature       float64 `json:"expansion_temperature"`
	SynthesisImplementation    string  `json:"synthesis_implementation"`
	SynthesisModel             string  `json:"synthesis_model"`
	SynthesisReasoning         string  `json:"synthesis_reasoning"`
	SynthesisTemperature       float64 `json:"synthesis_temperature"`
	SelectionLimit             int     `json:"selection_limit"`
	ExplorationSlots           int     `json:"exploration_slots"`
	EvidenceThreshold          int     `json:"evidence_threshold"`
	KeywordsPerAttempt         int     `json:"keywords_per_attempt"`
	ExpansionAttempts          int     `json:"expansion_attempts"`
	RareDocumentFrequency      int     `json:"rare_document_frequency"`
	SynthesisProvider          string  `json:"synthesis_provider"`
	NoEvidencePolicy           string  `json:"no_evidence_policy"`
	LLMProfileID               string  `json:"llm_profile_id,omitempty"`
	LLMProfileRevision         int64   `json:"llm_profile_revision,omitempty"`
	LLMProtocol                string  `json:"llm_protocol,omitempty"`
}

// RuntimeConfigReadback is the sanitized global identity of a sealed runtime.
// It deliberately has no project, generation, corpus, request, or prompt-body fields.
type RuntimeConfigReadback struct {
	SchemaVersion                   int                 `json:"schema_version"`
	ConfigRevision                  string              `json:"config_revision"`
	ConfigDigest                    string              `json:"config_digest"`
	QueryServiceImplementation      string              `json:"query_service_implementation"`
	DefaultProfileID                string              `json:"default_profile_id"`
	DefaultProfileDigest            string              `json:"default_profile_digest"`
	DefaultPromptID                 string              `json:"default_prompt_id"`
	DefaultPromptDigest             string              `json:"default_prompt_digest"`
	ExpansionProvider               string              `json:"expansion_provider"`
	ExpansionModel                  string              `json:"expansion_model"`
	ExpansionReasoning              string              `json:"expansion_reasoning"`
	ExpansionTemperature            float64             `json:"expansion_temperature"`
	SynthesisProvider               string              `json:"synthesis_provider"`
	SynthesisModel                  string              `json:"synthesis_model"`
	SynthesisReasoning              string              `json:"synthesis_reasoning"`
	SynthesisTemperature            float64             `json:"synthesis_temperature"`
	NoEvidencePolicy                string              `json:"no_evidence_policy"`
	Options                         RuntimeQueryOptions `json:"options"`
	BindingCount                    int                 `json:"binding_count"`
	DistinctServiceCompositionCount int                 `json:"distinct_service_composition_count"`
}

type RuntimeQueryOptions struct {
	SelectionLimit        int `json:"selection_limit"`
	ExplorationSlots      int `json:"exploration_slots"`
	EvidenceThreshold     int `json:"evidence_threshold"`
	KeywordsPerAttempt    int `json:"keywords_per_attempt"`
	ExpansionAttempts     int `json:"expansion_attempts"`
	RareDocumentFrequency int `json:"rare_document_frequency"`
}

// RuntimeReadbackProvider is implemented only by the configured immutable runtime.
type RuntimeReadbackProvider interface {
	Readback() RuntimeConfigReadback
}

func CloneRuntimeConfigIdentity(identity *RuntimeConfigIdentity) *RuntimeConfigIdentity {
	if identity == nil {
		return nil
	}
	copy := *identity
	return &copy
}

// Executor is the narrow seam used by transport adapters.
type Executor interface {
	Execute(context.Context, cache.Reader, Request) (Result, error)
}

type Synthesizer interface {
	SynthesizeWithError(context.Context, cache.Reader, Request, Result) (Result, error)
	ModelIdentity() (llm.ModelIdentity, bool)
}

// Service runs query expansion, cache search, and optional citation synthesis.
type Service struct {
	cache    *cache.Cache
	expander *llm.QueryExpander
	llm      llm.ProjectChatClient
}

// NewService creates a query application service.
func NewService(conceptCache *cache.Cache, expander *llm.QueryExpander, llmClient *llm.Client) *Service {
	if llmClient == nil {
		return &Service{cache: conceptCache, expander: expander}
	}
	return &Service{cache: conceptCache, expander: expander, llm: llmClient}
}

func NewProjectService(conceptCache *cache.Cache, client llm.ProjectChatClient) *Service {
	return &Service{cache: conceptCache, llm: client}
}

func (s *Service) ModelIdentity() (llm.ModelIdentity, bool) {
	if s == nil || s.llm == nil {
		return llm.ModelIdentity{}, false
	}
	return s.llm.ModelIdentity()
}

// Execute runs the production query pipeline.
func (s *Service) Execute(ctx context.Context, reader cache.Reader, request Request) (Result, error) {
	if err := ValidateProfileRequest(request); err != nil {
		return Result{}, err
	}
	searchQuery := request.Query
	var expandResult *llm.ExpandResult

	if s.expander != nil {
		if result, err := s.expander.Expand(ctx, request.Query); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Result{}, ctxErr
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return Result{}, err
			}
			log.Printf("[expander] query expansion failed: %v — falling back to raw query", err)
		} else if result != nil {
			expandResult = result
			searchQuery = strings.Join(result.Keywords, " ")
		}
	}

	if s.cache == nil {
		return Result{}, ErrCacheNotConfigured
	}

	results, cacheSource, err := s.cache.SearchWithSource(ctx, reader, searchQuery, 10)
	if err != nil {
		return Result{}, err
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("query.cache_source", cacheSource), attribute.Int("query.corpus.count", s.cache.EntryCount(reader)))
	log.Printf("Search query completed: terms=%d results=%d", len(strings.Fields(searchQuery)), len(results))

	response := Result{
		Query:   request.Query,
		Mode:    request.Mode,
		Results: results,
		Expand:  expandResult,
	}
	result, err := s.SynthesizeWithError(ctx, reader, request, response)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Service) Synthesize(ctx context.Context, reader cache.Reader, request Request, response Result) Result {
	result, err := s.SynthesizeWithError(ctx, reader, request, response)
	if err != nil {
		return response
	}
	return result
}

func (s *Service) SynthesizeWithError(ctx context.Context, reader cache.Reader, request Request, response Result) (Result, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return response, ctxErr
	}
	identity, _ := RuntimeConfigIdentityFromContext(ctx)
	modelPrior := request.Mode == "full" && response.Status == "insufficient_evidence" && response.Reason == "no_qualified_evidence" && identity.NoEvidencePolicy == ModelPriorFallbackPolicy
	if s.llm == nil || (len(response.Results) == 0 && !modelPrior) {
		return response, nil
	}
	if modelPrior {
		synthesisCtx := ctx
		modelIdentity, _ := s.llm.ModelIdentity()
		if recorder := ReceiptRecorderFromContext(ctx); recorder != nil {
			synthesisCtx = recorder.StartStage(ctx, "answer_synthesis_model_prior", modelIdentity.Provider, modelIdentity.Model, modelIdentity.Reasoning)
		}
		synthesisCtx = llm.WithCallPurpose(synthesisCtx, "model_prior")
		answer, err := s.llm.Chat(synthesisCtx, buildModelPriorSystemPrompt(), buildModelPriorUserPrompt(request.Query))
		if err != nil {
			RecordStageFailure(synthesisCtx, AIOperationFailureClass(err), AIFallbackNone)
			FinishStage(synthesisCtx, "failure")
			if ctxErr := ctx.Err(); ctxErr != nil {
				return response, ctxErr
			}
			return response, errors.New("model-prior synthesis failed")
		}
		answer = strings.TrimSpace(search.NeutralizeCitationReferences(answer))
		if answer == "" {
			RecordStageFailure(synthesisCtx, AIFailureInvalidResponse, AIFallbackNone)
			FinishStage(synthesisCtx, "failure")
			return response, errors.New("model-prior synthesis returned blank content")
		}
		FinishStage(synthesisCtx, "success")
		response.AISynth = answer
		response.AnswerBasis = "model_prior"
		response.WikiEvidenceStatus = "no_relevant_evidence"
		response.DisclosureRequired = true
		response.Citations = []search.Citation{}
		return response, nil
	}

	synthesisCtx := ctx
	modelIdentity, _ := s.llm.ModelIdentity()
	if recorder := ReceiptRecorderFromContext(ctx); recorder != nil {
		synthesisCtx = recorder.StartStage(ctx, "answer_synthesis", modelIdentity.Provider, modelIdentity.Model, modelIdentity.Reasoning)
	}
	synthesisCtx = llm.WithCallPurpose(synthesisCtx, "evidence_answer")
	authority, err := search.NewCitationAuthority(response.Results)
	if err != nil {
		log.Printf("citation capability issuance failed: %v", err)
		failure, _ := AIOperationFailureFromCategory(AIOperationAnswerSynthesis, AIFailureContextUnavailable, AIFallbackRetrievalOnly)
		response.addAIOperationFailure(failure)
		RecordStageFailure(synthesisCtx, failure.Class, failure.Fallback)
		FinishStage(synthesisCtx, "degraded")
		return response, nil
	}
	contextCtx, contextSpan := otel.Tracer("llm-wiki-bff/query").Start(synthesisCtx, "query.context.acquire")
	contextCtx = observability.WithStorageReadBudget(contextCtx)
	var contexts []string
	if request.Profile != nil {
		contexts, err = buildProfileContexts(contextCtx, response.Results[:min(10, len(response.Results))], authority, request.Profile)
	} else {
		contexts, err = s.buildContexts(contextCtx, reader, response.Results[:min(10, len(response.Results))], authority)
	}
	if err != nil {
		finishContextAcquire(contextSpan, err, 0)
		if ctxErr := ctx.Err(); ctxErr != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			failureClass := AIOperationFailureClass(err)
			if ctxErr != nil {
				failureClass = AIOperationFailureClass(ctxErr)
			}
			RecordStageFailure(synthesisCtx, failureClass, AIFallbackNone)
			FinishStage(synthesisCtx, "failure")
			if ctxErr != nil {
				return response, ctxErr
			}
			return response, err
		}
		failure, _ := AIOperationFailureFromCategory(AIOperationAnswerSynthesis, AIFailureContextUnavailable, AIFallbackRetrievalOnly)
		response.addAIOperationFailure(failure)
		RecordStageFailure(synthesisCtx, failure.Class, failure.Fallback)
		FinishStage(synthesisCtx, "degraded")
		return response, nil
	}
	finishContextAcquire(contextSpan, nil, len(contexts))
	if len(contexts) == 0 {
		failure, _ := AIOperationFailureFromCategory(AIOperationAnswerSynthesis, AIFailureContextUnavailable, AIFallbackRetrievalOnly)
		response.addAIOperationFailure(failure)
		RecordStageFailure(synthesisCtx, failure.Class, failure.Fallback)
		FinishStage(synthesisCtx, "degraded")
		return response, nil
	}
	answer, err := s.llm.Chat(synthesisCtx, buildSystemPrompt(request.Mode), buildUserPrompt(request.Query, contexts))
	outcome := "success"
	if err != nil {
		outcome = "degraded"
		if ctx.Err() != nil {
			outcome = "failure"
		}
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			RecordStageFailure(synthesisCtx, AIOperationFailureClass(ctxErr), AIFallbackNone)
			FinishStage(synthesisCtx, outcome)
			return response, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			RecordStageFailure(synthesisCtx, AIOperationFailureClass(err), AIFallbackNone)
			FinishStage(synthesisCtx, "failure")
			return response, err
		}
		log.Printf("LLM synthesis failed: %v", err)
		failure, _ := NewAIOperationFailure(AIOperationAnswerSynthesis, err, AIFallbackRetrievalOnly)
		response.addAIOperationFailure(failure)
		RecordStageFailure(synthesisCtx, failure.Class, failure.Fallback)
		FinishStage(synthesisCtx, "degraded")
		return response, nil
	}

	answer, resolvedCitations, authorizedResults := authority.Resolve(answer)
	citationOutcome := "none"
	if len(resolvedCitations) > 0 {
		citationOutcome = "resolved"
	}
	// Zero resolved citations reports only the resolver result; it does not infer a rejection reason.
	trace.SpanFromContext(synthesisCtx).AddEvent("query.citation.validation", trace.WithAttributes(
		attribute.String("query.citation.outcome", citationOutcome),
		attribute.Int("query.citation.resolved_count", len(resolvedCitations)),
		attribute.Int("query.citation.authorized_result_count", len(authorizedResults)),
	))
	FinishStage(synthesisCtx, outcome)
	response.AISynth = answer
	response.Citations = authority.IssuedCitations()
	return response, nil
}

func (s *Service) buildContexts(ctx context.Context, reader cache.Reader, results []search.Result, authority *search.CitationAuthority) ([]string, error) {
	ids, err := LoadCitationIDMap(ctx, reader)
	if err != nil {
		return nil, err
	}
	resolver, err := newCitationIdentityResolver(ids)
	if err != nil {
		return nil, err
	}
	contexts := make([]string, 0, len(results))
	for rank, result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if result.Type == "source" {
			resolved, err := resolver.resolve(result)
			if err != nil {
				return nil, err
			}
			readCtx, finishRead := observability.StartStorageRead(ctx, "other")
			_, body, err := reader.GetPage(readCtx, result.Slug, "sources")
			if err != nil {
				finishRead(err, "not_required", "none")
			} else {
				finishRead(nil, "not_required", "none")
			}
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			results[rank].ID = resolved.ID
			contexts = append(contexts, authority.AddContext(rank, resolved, string(body)))
			continue
		}
		entry, ok := s.cache.Entry(reader, result.Slug)
		if !ok {
			if _, err := s.cache.Build(ctx, reader); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
			} else {
				entry, ok = s.cache.Entry(reader, result.Slug)
			}
		}
		if !ok {
			continue
		}
		sourceContext := "Sources: none listed"
		if len(entry.Sources) > 0 {
			sourceContext = "Sources: [" + strings.Join(entry.Sources, ", ") + "]"
		}
		result, err = resolver.resolve(result)
		if err != nil {
			return nil, err
		}
		results[rank].ID = result.ID
		contexts = append(contexts, authority.AddContext(rank, result, sourceContext+"\n\n"+entry.Body))
	}
	return contexts, nil
}

func buildSystemPrompt(mode string) string {
	base := "CRITICAL: If the user asks about a specific location (city, district, area), ONLY include results relevant to that location. Ignore results from other locations even if they match on topic keywords." +
		"\n\nCITATION FORMAT RULES (mandatory):" +
		"\n- Each wiki block includes one server-issued internal reference in brackets. Use that exact reference in brackets when citing the block; the server will replace it with the canonical title." +
		"\n- Never invent, alter, or reuse a citation reference for a different wiki block" +
		"\n- EVERY factual claim from wiki content MUST have a bracketed citation: [Exact Source Name]" +
		"\n- Use the EXACT full title from the wiki content inside brackets" +
		"\n- Never use **bold** instead of brackets" +
		"\n- Never append source names as plain text without brackets" +
		"\n- Correct example: 「...適合親子放電。[中和員山公園遊逸之丘]」" +
		"\n- Wrong example: 「...適合親子放電。中和員山公園遊逸之丘」" +
		"\n- Each paragraph referencing a source MUST end with its bracketed citation. "
	if mode == "full" {
		return "You are a knowledgeable assistant with access to a personal wiki. Treat the wiki as supplementary reference material — NOT as a constraint." +
			"\n- If the wiki content is RELEVANT to the user's question (same location, topic, or category), use it and cite with [Source Name]." +
			"\n- If the wiki content is NOT relevant (wrong city, different topic, etc.), IGNORE it completely and answer from your own knowledge — exactly as if you were asked this question directly with no wiki." +
			"\n- NEVER say 'I cannot find this in the wiki' or apologize for missing information. Just answer the question." +
			"\n- When mixing wiki and general knowledge, make it seamless — don't call out which is which in the text." +
			"\n\nCITATION FORMAT RULES (mandatory):" +
			"\n- Each wiki block includes one server-issued internal reference in brackets. Use that exact reference in brackets when citing the block; the server will replace it with the canonical title." +
			"\n- Never invent, alter, or reuse a citation reference for a different wiki block" +
			"\n- EVERY factual claim from wiki content MUST have a bracketed citation: [Exact Source Name]" +
			"\n- Use the EXACT full title from the wiki content inside brackets" +
			"\n- Never use **bold** instead of brackets" +
			"\n- Correct example: 「...適合親子放電。[中和員山公園遊逸之丘]」" +
			"\n- Wrong example: 「...適合親子放電。中和員山公園遊逸之丘」"
	}
	return base + "You are a wiki Q&A assistant. Answer ONLY using the wiki content provided below. Do not use external knowledge. Cite every claim using [Source Name]."
}

func buildUserPrompt(query string, contexts []string) string {
	var builder strings.Builder
	builder.WriteString("User question: ")
	builder.WriteString(search.NeutralizeCitationReferences(query))
	builder.WriteString("\n\nWiki content:\n")
	for _, ctx := range contexts {
		builder.WriteString("\n---\n")
		builder.WriteString(ctx)
	}
	return builder.String()
}

func buildModelPriorSystemPrompt() string {
	return "Prompt contract: " + ModelPriorPromptID + ". You are a knowledgeable assistant answering from general model knowledge. No Wiki content or Wiki evidence is available. Answer the user's question directly and cautiously. Do not claim or imply that any statement is supported by a Wiki, and do not provide citations or citation-like references."
}

func buildModelPriorUserPrompt(query string) string {
	return "User question: " + search.NeutralizeCitationReferences(query)
}

func finishContextAcquire(span trace.Span, err error, count int) {
	if err != nil {
		outcome := "failure"
		if errors.Is(err, context.Canceled) {
			outcome = "canceled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			outcome = "timeout"
		}
		span.SetAttributes(attribute.String("query.outcome", outcome), attribute.Int("query.context.count", count))
		span.SetStatus(codes.Error, outcome)
	} else {
		span.SetAttributes(attribute.String("query.outcome", "success"), attribute.Int("query.context.count", count))
	}
	span.End()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
