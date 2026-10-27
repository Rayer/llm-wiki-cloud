//go:build byokprofilefixture

package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type profileFixtureRoundTripper struct{}

func profileFixtureTransport() http.RoundTripper {
	if os.Getenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT") != "1" {
		return nil
	}
	return profileFixtureRoundTripper{}
}

func (profileFixtureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Scheme != "https" || request.URL.Hostname() != "fixture.example" {
		return nil, errors.New("profile fixture rejected an unconfigured endpoint")
	}
	defer request.Body.Close()
	var input struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<20)).Decode(&input); err != nil || input.Model == "" {
		return nil, errors.New("profile fixture received an invalid request")
	}
	system, user := "", ""
	for _, message := range input.Messages {
		switch message.Role {
		case "system":
			system = message.Content
		case "user":
			user = message.Content
		}
	}
	content := profileFixtureContent(system, user)
	writeProfileFixtureReceipt("openai_chat_completions", input.Model, request.URL.Path)
	responseBody, err := json.Marshal(map[string]any{
		"id": "fixture-request", "object": "chat.completion", "created": 1, "model": input.Model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	})
	if err != nil {
		return nil, errors.New("profile fixture could not encode a response")
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(bytes.NewReader(responseBody)), ContentLength: int64(len(responseBody)), Request: request,
	}, nil
}

func profileFixtureContent(system, user string) string {
	switch {
	case strings.Contains(system, "raw_query string") && strings.Contains(system, "Criterion"):
		rawQuery := "disposable profile routing"
		if start := strings.Index(user, "Raw query: "); start >= 0 {
			encoded := strings.TrimSpace(strings.SplitN(user[start+len("Raw query: "):], "\n", 2)[0])
			rawQuery = encoded
			var decoded string
			if json.Unmarshal([]byte(encoded), &decoded) == nil {
				rawQuery = decoded
			}
		}
		data, _ := json.Marshal(map[string]any{
			"raw_query": rawQuery,
			"required":  []any{}, "excluded": []any{},
			"preferred": []any{map[string]any{"kind": "topic", "value": "BYOK profile routing", "terms": []string{"BYOK", "profile", "routing"}, "proof": "lexical"}},
			"goals":     []any{}, "supporting_dimensions": []any{}, "acceptable_alternatives": []any{},
			"ambiguity": []string{}, "fallback": false,
		})
		return string(data)
	case strings.Contains(system, `"summary"`) && strings.Contains(system, `"concepts"`):
		return `{"summary":"A disposable source describes BYOK profile routing for a Project.","concepts":[{"name":"BYOK Profile Routing","aliases":["BYOK","Project profile"]}],"suggested_topics":["BYOK Profile Routing"],"named_references":[],"quality":"high","language":"en"}`
	case strings.Contains(system, `"articles"`) && strings.Contains(system, `"mocs_to_update"`):
		return `{"articles":[{"title":"BYOK Profile Routing","action":"create","path":"byok-profile-routing.md","reasoning":"The source explains Project profile selection.","source_paths":["raw/lwc-byok-disposable-source.md"]}],"mocs_to_update":[]}`
	case strings.Contains(system, `"content"`) && strings.Contains(system, `"tags"`) && strings.Contains(system, `"title"`):
		return `{"title":"BYOK Profile Routing","content":"## Project profile routing\n\nA Project uses its saved BYOK profile for Query and Pipeline chat calls. The profile keeps its endpoint, model, protocol, and encrypted credential revision together.","tags":["byok","profile-routing"]}`
	case strings.Contains(system, `"terms"`) && strings.Contains(system, `"definition"`):
		return `{"terms":[]}`
	case strings.Contains(system, `"relations"`) && strings.Contains(system, `"subject"`):
		return `{"relations":[]}`
	default:
		return "The Project uses its selected BYOK profile and model for this chat call."
	}
}

func writeProfileFixtureReceipt(protocol, model, path string) {
	receipt := strings.TrimSpace(os.Getenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE"))
	if receipt == "" {
		return
	}
	file, err := os.OpenFile(receipt, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_ = file.Chmod(0o600)
	_, _ = fmt.Fprintf(file, "%s\t%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339Nano), protocol, model, path)
}

var _ http.RoundTripper = profileFixtureRoundTripper{}
