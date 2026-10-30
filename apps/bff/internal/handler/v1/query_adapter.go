package v1

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/search"
)

func setQueryIdentityHeaders(c *gin.Context, identity *query.RuntimeConfigIdentity) {
	if identity == nil {
		return
	}
	values := map[string]string{
		"X-Query-Config-Revision":         identity.ConfigRevision,
		"X-Query-Config-Digest":           identity.ConfigDigest,
		"X-Query-Effective-Config-Digest": identity.EffectiveConfigDigest,
		"X-Query-Service-Implementation":  identity.QueryServiceImplementation,
		"X-Query-Profile-ID":              identity.ProfileID,
		"X-Query-Profile-Digest":          identity.ProfileDigest,
		"X-Query-Prompt-ID":               identity.PromptID,
		"X-Query-Prompt-Digest":           identity.PromptDigest,
		"X-Query-Binding-Source":          identity.BindingSource,
		"X-Query-Binding-Exact":           strconv.FormatBool(identity.ExactBinding),
		"X-Query-Generation-ID":           identity.GenerationID,
		"X-Query-Concepts-Digest":         identity.ConceptsDigest,
		"X-LLM-Profile-ID":                identity.LLMProfileID,
		"X-LLM-Fast-Profile-ID":           identity.LLMFastProfileID,
		"X-LLM-Fast-Profile-Revision":     strconv.FormatInt(identity.LLMFastProfileRevision, 10),
		"X-LLM-Heavy-Profile-ID":          identity.LLMHeavyProfileID,
		"X-LLM-Heavy-Profile-Revision":    strconv.FormatInt(identity.LLMHeavyProfileRevision, 10),
		"X-LLM-Profile-Revision":          strconv.FormatInt(identity.LLMProfileRevision, 10),
		"X-LLM-Protocol":                  identity.LLMProtocol,
		"X-LLM-Provider":                  identity.ExpansionProvider,
		"X-LLM-Model":                     identity.ExpansionModel,
		"X-LLM-Fast-Model":                identity.ExpansionModel,
		"X-LLM-Heavy-Model":               identity.SynthesisModel,
		"X-LLM-Fast-Returned-Model":       identity.ExpansionReturnedModel,
		"X-LLM-Heavy-Returned-Model":      identity.SynthesisReturnedModel,
	}
	for key, value := range values {
		c.Header(key, value)
	}
}

func mapQueryResult(result query.Result) handler.QueryResponse {
	results := make([]search.Result, len(result.Results))
	for i, item := range result.Results {
		item.Snippet = ""
		results[i] = item
	}
	return handler.QueryResponse{
		Query:               result.Query,
		Mode:                result.Mode,
		Results:             results,
		Expand:              result.Expand,
		AIOperationFailures: append([]query.AIOperationFailure(nil), result.AIOperationFailures...),
		AISynth:             result.AISynth,
		Citations:           result.Citations,
		Status:              result.Status,
		Reason:              result.Reason,
		AnswerBasis:         result.AnswerBasis, WikiEvidenceStatus: result.WikiEvidenceStatus, DisclosureRequired: result.DisclosureRequired,
	}
}
