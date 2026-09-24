package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/suggestedqueries"
)

func TestSuggestedDiagnosticsSurviveFallbackWithoutPrivateText(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		err       error
		category  string
	}{
		{"transport", "", errors.New("private provider endpoint?key=private-key"), "provider_transport_or_response_error"},
		{"rate", "", &llm.HTTPStatusError{StatusCode: 429}, "provider_rate_limited"},
		{"schema", `{"private-provider-body":true}`, nil, "provider_schema_invalid"},
		{"count", `{"candidates":[]}`, nil, "candidate_cardinality_invalid"},
		{"anchors", twentySuggestedQueriesRaw("unknown-private-anchor"), nil, "candidate_anchor_unknown"},
		{"valid", twentySuggestedQueriesRaw("alpha-id"), nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := t.TempDir()
			mustWriteFile(t, filepath.Join(vault, "cache/concepts.jsonl"), []byte(`{"slug":"alpha","title":"Alpha","body":"Alpha","frontmatter":{"id":"alpha-id"}}`+"\n"))
			mustWriteFile(t, filepath.Join(vault, "wiki/alpha.md"), []byte("Alpha"))
			provider := &testSuggestedQueryProvider{raw: tc.raw, err: tc.err}
			category := ""
			if err := writeSuggestedQueriesObserved(context.Background(), vault, provider, func(value string) { category = value }); err != nil {
				t.Fatal(err)
			}
			if category != tc.category {
				t.Fatalf("category %q want %q", category, tc.category)
			}
			data, err := os.ReadFile(filepath.Join(vault, suggestedqueries.Path))
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := suggestedqueries.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if category != "" {
				if len(artifact.Queries) != 0 {
					t.Fatal("failed fresh generation was published")
				}
				var output bytes.Buffer
				localSuggestedFailure(&output, category)
				if strings.Contains(output.String(), "private") || !strings.Contains(output.String(), "suggested_"+category) {
					t.Fatal("unsafe or missing child diagnostic")
				}
				// Existing non-experiment last-known-good behavior remains unchanged.
				prior := []byte(`{"preserve":"last-known-good"}`)
				mustWriteFile(t, filepath.Join(vault, suggestedqueries.Path), prior)
				if err := writeSuggestedQueries(context.Background(), vault, provider); err != nil {
					t.Fatal(err)
				}
				got, _ := os.ReadFile(filepath.Join(vault, suggestedqueries.Path))
				if !bytes.Equal(got, prior) {
					t.Fatal("last-known-good changed")
				}
			} else if len(artifact.Queries) != 20 {
				t.Fatal("valid provider did not publish exactly 20")
			}
		})
	}
}

func TestSuggestedCorpusFailureIsDistinctAndMakesNoProviderCall(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		vault := t.TempDir()
		if malformed {
			mustWriteFile(t, filepath.Join(vault, "cache/concepts.jsonl"), []byte("private-invalid-json"))
		}
		provider := &testSuggestedQueryProvider{}
		category := ""
		err := writeSuggestedQueriesObserved(context.Background(), vault, provider, func(value string) { category = value })
		want := "corpus_empty"
		if malformed {
			want = "corpus_decode_failed"
			if err == nil {
				t.Fatal("malformed corpus accepted")
			}
		}
		if category != want || provider.calls != 0 {
			t.Fatalf("category=%s calls=%d", category, provider.calls)
		}
	}
}

// Optional read-only validation against the owner's frozen stage50 corpus.
func TestFrozenSuggestedCorpusAcceptsValidMockProvider(t *testing.T) {
	snapshot := os.Getenv("LWC_E2E_SUGGESTED_SNAPSHOT")
	if snapshot == "" {
		t.Skip("optional frozen local snapshot")
	}
	data, err := readBoundedRegularFileWithin(snapshot, "cache/concepts.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := decodeSuggestedQueryConcepts(data)
	if err != nil {
		t.Fatal(err)
	}
	mtimes, err := listConceptMtTimes(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	concepts := suggestedqueries.RepresentativeConcepts(entries, mtimes)
	if len(concepts) == 0 {
		t.Fatal("no generation corpus")
	}
	provider := &testSuggestedQueryProvider{raw: twentySuggestedQueriesRaw(concepts[0].ID)}
	artifact, err := suggestedqueries.Generate(context.Background(), provider, "", entries, mtimes, suggestedqueries.GenerationMetadata{Model: suggestedQueryModel, PromptVersion: suggestedqueries.PromptVersion}, time.Now())
	if err != nil || len(artifact.Queries) != 20 || provider.calls != 1 {
		t.Fatal("frozen corpus failed valid mocked generation", err)
	}
	var input struct {
		Concepts []suggestedqueries.ConceptEvidence `json:"concepts"`
	}
	if err := json.Unmarshal([]byte(provider.user), &input); err != nil || len(input.Concepts) != len(concepts) {
		t.Fatal("provider corpus mismatch")
	}
}

type rejectedSuggestionTransport struct{}

func (rejectedSuggestionTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private-provider-body"))}, nil
}
func TestLocalSuggestedCommandEmitsBoundedJSONDiagnostic(t *testing.T) {
	vault := t.TempDir()
	mustWriteFile(t, filepath.Join(vault, ".lwc-experiment-workspace"), []byte("exclusive local test"))
	mustWriteFile(t, filepath.Join(vault, "cache/concepts.jsonl"), []byte(`{"slug":"alpha","title":"Alpha","body":"Alpha","frontmatter":{"id":"alpha-id"}}`+"\n"))
	mustWriteFile(t, filepath.Join(vault, "wiki/alpha.md"), []byte("Alpha"))
	old := http.DefaultTransport
	http.DefaultTransport = rejectedSuggestionTransport{}
	t.Cleanup(func() { http.DefaultTransport = old })
	oldLog := log.Writer()
	t.Cleanup(func() { log.SetOutput(oldLog) })
	t.Setenv("DEEPSEEK_API_KEY", "explicit-mock-private-key")
	cmd := newLocalExperimentCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--local-vault", vault, "suggested"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("failed fresh inference claimed success")
	}
	var diagnostic map[string]string
	if err := json.Unmarshal(output.Bytes(), &diagnostic); err != nil {
		t.Fatal("missing child JSON category", err)
	}
	if diagnostic["error_type"] != "suggested_provider_rate_limited" || strings.Contains(output.String(), "private") {
		t.Fatal("incorrect or unsafe diagnostic")
	}
}

func TestLocalIndexUsesSelectedPublicSyntoExecutable(t *testing.T) {
	originalLog := log.Writer()
	t.Cleanup(func() { log.SetOutput(originalLog) })
	vault := t.TempDir()
	privateConfig := t.TempDir()
	record := filepath.Join(vault, "public-args.txt")
	binary := filepath.Join(t.TempDir(), "public-synto")
	mustWriteFile(t, filepath.Join(vault, ".lwc-experiment-workspace"), []byte("exclusive test\n"))
	mustWriteFile(t, filepath.Join(vault, "synto.toml"), []byte("[pipeline]\nauto_commit = false\nauto_maintain = false\n"))
	// The public process deliberately fails after recording argv. The worker must
	// preserve that failure, not invoke Python or consume a stale INDEX artifact.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + record + "'\nenv > '" + record + ".env'\nexit 23\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", privateConfig)
	t.Setenv("DEEPSEEK_API_KEY", "must-not-cross-export-boundary")
	cmd := newLocalExperimentCommand()
	cmd.SetArgs([]string{"--local-vault", vault, "--synto-bin", binary, "index"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("accepted failed public export")
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "pack\nexport\n--target\nagents\n--out\n") || strings.Contains(string(data), "python") || strings.Contains(string(data), "-c\n") {
		t.Fatalf("wrong public command: %s", data)
	}
	env, err := os.ReadFile(record + ".env")
	if err != nil || !strings.Contains(string(env), "PYTHONDONTWRITEBYTECODE=1") || !strings.Contains(string(env), "HOME="+filepath.Join(privateConfig, "home")) || strings.Contains(string(env), "must-not-cross-export-boundary") {
		t.Fatal("public export environment lost isolation", err)
	}
	if !strings.Contains(string(env), "TMPDIR="+filepath.Join(privateConfig, "home", "tmp")) || !strings.Contains(string(env), "XDG_CACHE_HOME="+filepath.Join(privateConfig, "home", ".cache")) {
		t.Fatal("public export temporary/cache files escape experiment directory")
	}
	if _, err := os.Stat(filepath.Join(vault, ".synto/INDEX.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed export published INDEX")
	}
}
