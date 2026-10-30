package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/documentimport"
)

type documentImportCloudRunStarter struct {
	handler *Handler
}

func (s *documentImportCloudRunStarter) Start(ctx context.Context, ownerID, projectID, documentID, attemptID string) (string, error) {
	if s == nil || s.handler == nil || strings.TrimSpace(s.handler.documentImportJobURL) == "" {
		return "", errors.New("document import job is unavailable")
	}
	token, err := s.handler.getMetadataAccessToken(ctx)
	if err != nil {
		return "", errors.New("document import job authentication is unavailable")
	}
	body, err := json.Marshal(map[string]any{
		"overrides": map[string]any{
			"containerOverrides": []map[string]any{{
				"env": []map[string]string{
					{"name": "DOCUMENT_IMPORT_OWNER_ID", "value": ownerID},
					{"name": "DOCUMENT_IMPORT_PROJECT_ID", "value": projectID},
					{"name": "DOCUMENT_IMPORT_DOCUMENT_ID", "value": documentID},
					{"name": "DOCUMENT_IMPORT_ATTEMPT_ID", "value": attemptID},
				},
			}},
		},
	})
	if err != nil {
		return "", errors.New("document import job request could not be prepared")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.handler.documentImportJobURL, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("document import job request could not be prepared")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.handler.pipelineHTTPClient().Do(req)
	if err != nil {
		return "", errors.New("document import job invocation outcome is unknown")
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(response) > 1<<20 {
		return "", errors.New("document import job response is invalid")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("document import job invocation failed with status %d", resp.StatusCode)
	}
	executionID, err := cloudRunExecutionIDFromRunResponse(response)
	if err != nil {
		return "", errors.New("document import job invocation outcome is unknown")
	}
	return executionID, nil
}

var _ documentimport.JobStarter = (*documentImportCloudRunStarter)(nil)
