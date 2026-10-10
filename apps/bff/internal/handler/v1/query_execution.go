package v1

import (
	"context"
	"errors"

	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/storage"
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
func (h *Handler) executeQuery(ctx context.Context, reader storage.Store, profile *query.ProfileSnapshot, request query.Request) (handler.QueryResponse, *query.RuntimeConfigIdentity, *queryFailure) {
	if h.queryExecutor == nil {
		return handler.QueryResponse{}, nil, queryFailureFor("runtime", errors.New("query executor is not configured"), reader)
	}
	request.Profile = profile
	stageCtx, span := startQueryStage(ctx, "runtime.execute")
	result, err := h.queryExecutor.Execute(stageCtx, reader, request)
	finishQueryStage(span, err)
	if err != nil {
		return handler.QueryResponse{}, nil, queryExecutorFailure(queryStageFailure("runtime", err), reader)
	}
	return mapQueryResult(result), result.RuntimeConfigIdentity, nil
}
