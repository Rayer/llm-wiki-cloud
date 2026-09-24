package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"github.com/rayer/llm-wiki-bff/internal/queryquality"
	"github.com/rayer/llm-wiki-bff/internal/search"
)

type frozenCitationTransport struct {
	answer  string
	prompts []string
	t       *testing.T
}

func (f *frozenCitationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	data, _ := io.ReadAll(req.Body)
	var input struct {
		Model    string `json:"model"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		f.t.Fatal(err)
	}
	if input.Model != "deepseek-flash" || !strings.Contains(input.Messages[len(input.Messages)-1].Content, "[CITATION_REF_") {
		f.t.Fatal("downstream replay called expansion")
	}
	f.prompts = append(f.prompts, input.Messages[len(input.Messages)-1].Content)
	data, _ = json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": f.answer}}}})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func checkStagedAndDirectCitationParity(t *testing.T, req localRequest, answer string, want int) {
	t.Helper()
	transport := &frozenCitationTransport{answer: answer, t: t}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	t.Setenv("DEEPSEEK_API_KEY", "explicit-mock-citation-key")
	req.Operation, req.Stage = "query", 100
	data, _ := json.Marshal(req)
	var output bytes.Buffer
	if err := localPlatformMain(bytes.NewReader(data), &output); err != nil {
		t.Fatal(err)
	}
	var results map[string]localCaseResult
	if err := json.Unmarshal(output.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected frozen cases")
	}
	for id, result := range results {
		if want >= 0 && len(result.Result.Citations) != want {
			t.Fatalf("staged citations=%d want=%d", len(result.Result.Citations), want)
		}
		if want == -1 && result.Status != "execution_failure" {
			t.Fatal("rejected route was not honestly classified")
		}
		if want > 0 && result.Status != "success" {
			t.Fatal("safe synthetic control failed")
		}
		prepared, err := preflightSnapshot(context.Background(), req.Snapshot)
		if err != nil {
			t.Fatal(err)
		}
		var selected []search.Result
		for _, entry := range req.Saved[id].Replay.Selection.Selected {
			if entry.Selected {
				selected = append(selected, search.Result{Slug: entry.Slug, Title: entry.Title, Type: "concept"})
			}
		}
		service := query.NewService(prepared.cache, nil, llm.NewClientWithOptions("explicit-mock-citation-key", llm.ClientOptions{Model: "deepseek-v4-pro"}))
		direct, err := service.SynthesizeWithError(context.Background(), prepared.reader, query.Request{Query: req.Saved[id].Replay.Plan.RawQuery, Mode: "wiki"}, query.Result{Results: selected, Status: "ok", Reason: "qualified_evidence"})
		if want == -1 {
			if err == nil || !strings.Contains(err.Error(), "citation identity:") {
				t.Fatalf("expected explicit mapping failure, got %v", err)
			}
			t.Logf("%s: rejected existing map: %v; no LLM calls, no new answer; frozen inline label remains unresolved", id, err)
			if dir := os.Getenv("LWC335_REPLAY_OUTPUT"); dir != "" {
				data, _ := json.MarshalIndent(result, "", "  ")
				if err := os.WriteFile(filepath.Join(dir, "rejected-"+id+".json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			continue
		}
		if err != nil || len(direct.Citations) != want || !reflect.DeepEqual(direct.Citations, result.Result.Citations) || direct.AISynth != result.Result.AISynth {
			t.Fatal("staged adapter differs from direct production synthesizer", err)
		}
		labels := map[string]bool{}
		for _, citation := range result.Result.Citations {
			labels[citation.Text] = true
			collection := "concepts"
			if citation.Type == "source" {
				collection = "sources"
			}
			_, body, err := prepared.reader.GetPage(context.Background(), citation.Slug, collection)
			if err != nil || len(bytes.TrimSpace(body)) == 0 {
				t.Fatalf("citation article unavailable: %s %v", citation.ID, err)
			}
			if citation.ID == "" {
				t.Fatal("missing canonical ID")
			}
		}
		var unresolved []string
		for _, m := range regexp.MustCompile(`\[([^\]\n]+)\]`).FindAllStringSubmatch(result.Result.AISynth, -1) {
			if !labels[m[1]] {
				unresolved = append(unresolved, m[1])
			}
		}
		t.Logf("%s: citations=%d unresolved inline=%v", id, len(labels), unresolved)
		if len(unresolved) > 0 {
			t.Errorf("unresolved inline citations: %v", unresolved)
		}
		if dir := os.Getenv("LWC335_REPLAY_OUTPUT"); dir != "" {
			data, _ := json.MarshalIndent(result, "", "  ")
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%s.json", filepath.Base(req.Snapshot), id)), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	expectedCalls := 2 * len(results)
	if want == -1 {
		expectedCalls = 0
	}
	if len(transport.prompts) != expectedCalls {
		t.Fatal("unexpected provider call count")
	}
	for _, prompt := range transport.prompts {
		if strings.Contains(prompt, "[CITATION_REF_") != (want > 0) {
			t.Fatal("citation issuance not explained by route eligibility")
		}
	}
}

func TestStagedCitationMappedWhitespaceMatchesProduction(t *testing.T) {
	for _, slug := range []string{"stable alpha", "stable-alpha", "台北 café"} {
		t.Run(slug, func(t *testing.T) {
			root := t.TempDir()
			os.MkdirAll(filepath.Join(root, "cache"), 0700)
			os.MkdirAll(filepath.Join(root, "wiki"), 0700)
			// The former RED fixture lacked an ID map and rejected whitespace.
			// Canonical fixtures now include the normal Synto map contract.
			ids, _ := json.Marshal(map[string]any{"concept": map[string]string{"01ARZ3NDEKTSV4RRFFQ69G5FAV": slug}})
			os.WriteFile(filepath.Join(root, "cache/id_map.json"), ids, 0600)
			entry, _ := json.Marshal(map[string]string{"slug": slug, "title": "stable alpha", "body": "Stable Alpha is a migration verification baseline."})
			os.WriteFile(filepath.Join(root, conceptsPath), append(entry, '\n'), 0600)
			os.WriteFile(filepath.Join(root, "wiki", slug+".md"), []byte("Stable Alpha is a migration verification baseline."), 0600)
			cases := filepath.Join(root, "cases.jsonl")
			os.WriteFile(cases, []byte(`{"id":"stable-alpha","query":"What role does Stable Alpha play in migration verification?","mode":"wiki"}`+"\n"), 0600)
			saved := localCaseResult{Replay: queryquality.StageReplay{Stage: 90, Plan: &queryquality.QueryPlan{RawQuery: "What role does Stable Alpha play in migration verification?", Preferred: []queryquality.Criterion{{Kind: "entity", Value: "Stable Alpha", Terms: []string{"Stable Alpha"}, Proof: "lexical"}}}, Matching: &queryquality.EligibilityResult{}, Selection: &queryquality.SelectionResult{Selected: []queryquality.SelectedCandidate{{Slug: slug, Title: "stable alpha", Selected: true}}}}}
			want := 1
			checkStagedAndDirectCitationParity(t, localRequest{Snapshot: root, Cases: cases, Config: "../../../../scripts/local_e2e/fixtures/query-dns.json", Project: "test-project", Generation: "local-test", Saved: map[string]localCaseResult{"stable-alpha": saved}}, "Migration verification baseline. [stable alpha]", want)
			for _, failure := range []string{"ambiguous", "missing"} {
				t.Run(failure, func(t *testing.T) {
					mapPath := filepath.Join(root, "cache/id_map.json")
					if failure == "ambiguous" {
						data, _ := json.Marshal(map[string]any{"concept": map[string]string{"abcdef123456": slug, "123456abcdef": slug}})
						if err := os.WriteFile(mapPath, data, 0600); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Remove(mapPath); err != nil {
						t.Fatal(err)
					}
					checkStagedAndDirectCitationParity(t, localRequest{Snapshot: root, Cases: cases, Config: "../../../../scripts/local_e2e/fixtures/query-dns.json", Project: "test-project", Generation: "local-test", Saved: map[string]localCaseResult{"stable-alpha": saved}}, "Migration verification baseline. [stable alpha]", -1)
				})
			}
		})
	}
}

func TestFrozenCitationMapMatchesProduction(t *testing.T) {
	run := os.Getenv("LWC_E2E_CITATION_RUN")
	if run == "" {
		t.Skip("optional frozen owner run")
	}
	outcome := os.Getenv("LWC_E2E_CITATION_EXPECT")
	if outcome != "mapped" && outcome != "rejected" {
		t.Fatal("set LWC_E2E_CITATION_EXPECT=mapped (selected citation count) or rejected (identity failure)")
	}
	checkFrozenCitationRun(t, run, outcome)
}

func checkFrozenCitationRun(t *testing.T, run, outcome string) {
	t.Helper()
	config := filepath.Join(run, "query-config.json")
	cfg, err := queryconfig.LoadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProjectBindings) != 1 {
		t.Fatal("expected exact frozen project binding")
	}
	var saved, completed struct {
		Data map[string]localCaseResult `json:"data"`
	}
	for path, target := range map[string]any{"90": &saved, "100": &completed} {
		data, err := os.ReadFile(filepath.Join(run, "artifacts", path+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, target); err != nil {
			t.Fatal(err)
		}
	}
	if len(completed.Data) == 0 {
		t.Fatal("expected frozen cases")
	}
	for id, result := range completed.Data {
		want := 0
		if outcome == "rejected" {
			want = -1
		} else {
			for _, selected := range saved.Data[id].Replay.Selection.Selected {
				if selected.Selected {
					want++
				}
			}
		}
		singleCases := filepath.Join(t.TempDir(), "cases.jsonl")
		caseData, _ := os.ReadFile(filepath.Join(run, "cases.jsonl"))
		for _, line := range strings.Split(string(caseData), "\n") {
			var c struct {
				ID string `json:"id"`
			}
			if json.Unmarshal([]byte(line), &c) == nil && c.ID == id {
				os.WriteFile(singleCases, []byte(line+"\n"), 0600)
			}
		}
		checkStagedAndDirectCitationParity(t, localRequest{Snapshot: filepath.Join(run, "snapshots/90"), Cases: singleCases, Config: config, Project: cfg.ProjectBindings[0].ProjectID, Generation: cfg.ProjectBindings[0].GenerationID, Saved: saved.Data}, result.Result.AISynth, want)
	}
}
