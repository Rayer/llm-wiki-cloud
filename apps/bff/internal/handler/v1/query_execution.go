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
}

func queryStoreFailure(err error) *queryFailure {
	if errors.Is(err, errProfileProjectNotFound) {
		return &queryFailure{status: 404, message: "project not found"}
	}
	return &queryFailure{status: 500, message: "generated data unavailable"}
}

func queryExecutorFailure(err error) *queryFailure {
	if errors.Is(err, query.ErrUnsupportedRequired) {
		return &queryFailure{status: 422, message: "required Profile condition is unsupported"}
	}
	if errors.Is(err, query.ErrCacheNotConfigured) {
		return &queryFailure{status: 500, message: "concept cache is not configured"}
	}
	return &queryFailure{status: 500, message: "generated data unavailable"}
}

// executeQuery is the shared HTTP/MCP consumer path. Both adapters resolve
// the same pinned store and profile before calling it, and use the same
// privacy-safe QueryResponse projection.
func (h *Handler) executeQuery(ctx context.Context, reader storage.Store, profile *query.ProfileSnapshot, request query.Request) (handler.QueryResponse, *query.RuntimeConfigIdentity, *queryFailure) {
	if h.queryExecutor == nil {
		return handler.QueryResponse{}, nil, &queryFailure{status: 500, message: "generated data unavailable"}
	}
	request.Profile = profile
	result, err := h.queryExecutor.Execute(ctx, reader, request)
	if err != nil {
		return handler.QueryResponse{}, nil, queryExecutorFailure(err)
	}
	return mapQueryResult(result), result.RuntimeConfigIdentity, nil
}
