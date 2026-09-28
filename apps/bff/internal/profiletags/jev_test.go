package profiletags

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJevHTTPTransportUsesIndependentExactInputAndFailsClosed(t *testing.T) {
	response := `{"model":"jev-1.13.0","answers":{"applicable":{"type":"noul","noul":0.9},"known":{"type":"noul","noul":0.9},"match":{"type":"noul","noul":0.9}},"usage":{"input_tokens":1,"output_tokens":1}}`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer explicit-fake-key" {
			t.Error("incorrect provider request")
		}
		var request struct {
			Model     string                     `json:"model"`
			State     []string                   `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "jev-latest" || len(request.State) != 4 || request.State[0] != "concept" || request.State[1] != "c1" || request.State[3] != "exact concept row" || len(request.Questions) != 3 {
			t.Errorf("incorrect independent input: %+v", request)
		}
		for _, raw := range request.Questions {
			var q struct {
				Instructions string `json:"instructions"`
			}
			if err := json.Unmarshal(raw, &q); err != nil {
				t.Fatal(err)
			}
			for _, exact := range []string{"domain", "positive", "negative", "ambiguous"} {
				if !strings.Contains(q.Instructions, exact) {
					t.Errorf("prompt omitted rule %q", exact)
				}
			}
		}
		fmt.Fprint(w, response)
	}))
	defer server.Close()
	e := NewJevEvaluator("explicit-fake-key")
	e.Endpoint = server.URL
	e.Client = server.Client()
	policy := ProviderPolicy{ConfiguredModel: "jev-latest", AcceptedReturnedModels: []string{"jev-1.13.0"}, PromptVersion: JevPromptVersion, SchemaVersion: DecisionSchema}
	item := Item{Kind: Concept, StableID: "c1", Content: []byte("exact concept row")}
	tag := Tag{ID: "test", Definition: "domain", MatchRule: "positive", NonMatchRule: "negative", UnknownRule: "ambiguous"}
	result, err := e.Evaluate(context.Background(), item, tag, policy)
	if err != nil || result.Judgment != Match || result.Confidence != nil || calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	original := response
	response = strings.Replace(original, `"known":{"type":"noul","noul":0.9}`, `"known":{"type":"noul","noul":0.1}`, 1)
	result, err = e.Evaluate(context.Background(), item, tag, policy)
	if err != nil || result.Judgment != Unknown {
		t.Fatalf("missing evidence = %+v %v", result, err)
	}
	for _, bad := range []string{`{}`, strings.Replace(original, "jev-1.13.0", "unapproved", 1), strings.Replace(original, `"noul":0.9`, `"noul":null`, 1), strings.Replace(original, `"model":`, `"model":"duplicate","model":`, 1), original + `{}`, strings.Repeat("x", 1048577)} {
		response = bad
		if _, err = e.Evaluate(context.Background(), item, tag, policy); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = e.Evaluate(ctx, item, tag, policy); err == nil {
		t.Fatal("canceled call accepted")
	}
}

func TestJevTransportDoesNotReturnProviderBodyOrKey(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, "secret-provider-body")
	}))
	defer s.Close()
	e := NewJevEvaluator("secret-test-key")
	e.Endpoint = s.URL
	e.Client = s.Client()
	_, err := e.Evaluate(context.Background(), Item{Kind: Source, StableID: "s", Content: []byte("s")}, Tag{}, ProviderPolicy{PromptVersion: JevPromptVersion})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}
