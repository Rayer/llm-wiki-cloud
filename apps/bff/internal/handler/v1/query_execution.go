package v1

import (
	"context"
	"errors"
	"net/http"

	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"go.opentelemetry.io/otel/attribute"
)

type queryFailure struct {
	status  int
	message string
	code    string
	stage   string
	reason  string
	cause   error
}

func queryStoreFailure(err error) *queryFailure {
	return queryFailureFor("profile_resolution", err, nil)
}

func queryExecutorFailure(err error, reader storage.Store) *queryFailure {
	return queryFailureFor("runtime", err, reader)
}

// executeQuery is the shared HTTP/MCP consumer path. Both adapters resolve
// the same pinned store and profile before calling it, and use the same
// privacy-safe QueryResponse projection.
func (h *Handler) executeQuery(ctx context.Context, reader storage.Store, profile *query.ProfileSnapshot, request query.Request, scope ...string) (handler.QueryResponse, *query.RuntimeConfigIdentity, *queryFailure) {
	if h.queryExecutor == nil {
		return handler.QueryResponse{}, nil, queryFailureFor("runtime", errors.New("query executor is not configured"), reader)
	}
	var selectedClient llm.ProjectChatClient
	selectedSource := ""
	if len(scope) == 2 {
		client, source, err := h.projectLLMClient(ctx, scope[0], scope[1])
		if err != nil {
			if errors.Is(err, errProfileProjectNotFound) {
				return handler.QueryResponse{}, nil, queryFailureFor("profile_resolution", err, reader)
			}
			return handler.QueryResponse{}, nil, &queryFailure{status: http.StatusServiceUnavailable,
				message: "The selected LLM source is unavailable. Check Project settings or its BYOK profile.",
				code:    "llm_source_unavailable", stage: "profile_resolution", cause: err}
		}
		selectedClient = client
		selectedSource = source
		if selectedClient != nil {
			ctx = llm.WithProjectChatClient(ctx, selectedClient)
			defer func() {
				if closer, ok := selectedClient.(interface{ Close() }); ok {
					closer.Close()
				}
			}()
		}
	}
	request.Profile = profile
	stageCtx, span := startQueryStage(ctx, "runtime.execute")
	if selectedSource != "" {
		span.SetAttributes(attribute.String("llm.source", selectedSource))
	}
	result, err := h.queryExecutor.Execute(stageCtx, reader, request)
	if err != nil {
		finishQueryStage(span, err)
		return handler.QueryResponse{}, nil, queryExecutorFailure(queryStageFailure("runtime", err), reader)
	}
	if identity := result.RuntimeConfigIdentity; identity != nil && identity.LLMProfileID != "" {
		span.SetAttributes(
			attribute.String("llm.profile.id", identity.LLMProfileID),
			attribute.Int64("llm.profile.revision", identity.LLMProfileRevision),
			attribute.String("llm.provider", identity.ExpansionProvider),
			attribute.String("llm.protocol", identity.LLMProtocol),
			attribute.String("llm.model", identity.ExpansionModel),
		)
	}
	finishQueryStage(span, nil)
	response := mapQueryResult(result)
	if len(response.AIOperationFailures) > 0 {
		source := queryAIOperationSource(selectedSource)
		for i := range response.AIOperationFailures {
			response.AIOperationFailures[i].Source = source
		}
	}
	return response, result.RuntimeConfigIdentity, nil
}

func queryAIOperationSource(source string) string {
	switch source {
	case "personal":
		return query.AIOperationSourcePersonal
	case "shared", "bootstrap":
		return query.AIOperationSourceShared
	default:
		return query.AIOperationSourceUnknown
	}
}
