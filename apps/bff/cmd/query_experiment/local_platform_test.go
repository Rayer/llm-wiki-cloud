package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
)

type importReader struct {
	files   map[string][]byte
	fail    string
	corrupt string
	reads   []string
}

func (r *importReader) ReadFile(_ context.Context, path string) ([]byte, error) {
	r.reads = append(r.reads, path)
	if path == r.fail {
		return nil, errors.New("private remote failure")
	}
	if path == r.corrupt {
		return []byte("changed"), nil
	}
	return r.files[path], nil
}
func importFixture(t *testing.T) (*importReader, gcs.GenerationSnapshot) {
	t.Helper()
	r := &importReader{files: map[string][]byte{"cache/concepts.jsonl": []byte(`{"slug":"alice","title":"Alice","body":"Alice"}` + "\n"), "wiki/alice.md": []byte("Alice")}}
	m := generation.Manifest{Version: 1, GenerationID: "test-generation", CreatedAt: "2026-09-19T00:00:00Z", InputFingerprint: "frozen-input"}
	paths := []string{}
	for p := range r.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		row, err := generation.NewFile(p, r.files[p], 7)
		if err != nil {
			t.Fatal(err)
		}
		m.Files = append(m.Files, row)
	}
	data, _ := json.Marshal(m)
	return r, gcs.GenerationSnapshot{Manifest: m, ManifestGeneration: 9, ManifestSHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
}
func TestMaterializePinnedSnapshotCompleteAndReadOnly(t *testing.T) {
	r, s := importFixture(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	out := filepath.Join(root, "snapshot")
	if err := materializePinnedSnapshot(context.Background(), r, s, out); err != nil {
		t.Fatal(err)
	}
	for p, want := range r.files {
		info, err := os.Stat(filepath.Join(out, p))
		if err != nil {
			t.Fatal(err)
		}
		expected, _ := time.Parse(time.RFC3339, s.Manifest.CreatedAt)
		if !info.ModTime().Equal(expected) {
			t.Fatalf("unrecorded copy-time sampling input for %s", p)
		}
		got, err := os.ReadFile(filepath.Join(out, p))
		if err != nil || string(got) != string(want) {
			t.Fatalf("copy %s: %q %v", p, got, err)
		}
	}
	if len(r.reads) != len(s.Manifest.Files) {
		t.Fatal("not all manifest objects materialized")
	}
	data, err := os.ReadFile(filepath.Join(out, "import-provenance.json"))
	if err != nil || !strings.Contains(string(data), `"ManifestGeneration":9`) {
		t.Fatalf("provenance %s %v", data, err)
	}
	// Reader deliberately has no write, list, or latest-manifest method.
	if err := materializePinnedSnapshot(context.Background(), r, s, out); err == nil {
		t.Fatal("overwrote destination")
	}
}
func TestIncompleteOrChangedImportNeverPublishesAndCleansTemporaryFiles(t *testing.T) {
	for _, kind := range []string{"missing", "changed", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			r, s := importFixture(t)
			if kind == "missing" {
				r.fail = "wiki/alice.md"
			}
			if kind == "changed" {
				r.corrupt = "wiki/alice.md"
			}
			if kind == "manifest" {
				s.Manifest.Files[0].Path = "../escape"
			}
			root, _ := filepath.EvalSymlinks(t.TempDir())
			out := filepath.Join(root, "snapshot")
			err := materializePinnedSnapshot(context.Background(), r, s, out)
			if err == nil {
				t.Fatal("accepted inconsistent source")
			}
			if strings.Contains(err.Error(), "private remote") {
				t.Fatal("leaked backend diagnostic")
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 0 {
				t.Fatalf("partial import published or leaked: %v", entries)
			}
		})
	}
}
func TestLocalIdentityReaderPreservesPersistedCacheContract(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "cache"), 0700)
	os.WriteFile(filepath.Join(root, conceptsPath), []byte("body"), 0600)
	reader := localIdentityReader{Reader: newSnapshotReader(root)}
	data, err := reader.ReadFile(context.Background(), conceptsPath)
	if err != nil || string(data) != "body" {
		t.Fatalf("reader lost ReadFile: %s %v", data, err)
	}
}

type platformTransport struct {
	expansion, synthesis atomic.Int32
	t                    *testing.T
}

func (p *platformTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	data, _ := io.ReadAll(req.Body)
	var call struct {
		Model    string `json:"model"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &call); err != nil {
		p.t.Fatal(err)
	}
	answer := `{"raw_query":"Alice","preferred":[{"kind":"entity","value":"Alice","terms":["Alice"],"proof":"lexical"}],"required":[],"excluded":[],"goals":[],"supporting_dimensions":[],"acceptable_alternatives":[],"ambiguity":[],"fallback":false}`
	if call.Model == "deepseek-v4-pro" {
		p.synthesis.Add(1)
		answer = ""
		for _, m := range call.Messages {
			if start := strings.Index(m.Content, "[CITATION_REF_"); start >= 0 {
				rest := m.Content[start:]
				answer = "Alice. " + rest[:strings.Index(rest, "]")+1] + " explicit-mock-key"
			}
		}
		if answer == "" {
			p.t.Fatal("missing production citation token")
		}
	} else {
		p.expansion.Add(1)
	}
	response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": answer}}}})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response))}, nil
}
func TestLocalPlatformUsesSealedProductionRuntimeThroughAllQueryStages(t *testing.T) {
	transport := &platformTransport{t: t}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	t.Setenv("DEEPSEEK_API_KEY", "explicit-mock-key")
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "cache"), 0700)
	os.MkdirAll(filepath.Join(root, "wiki"), 0700)
	os.WriteFile(filepath.Join(root, conceptsPath), []byte(`{"slug":"alice","title":"Alice","body":"Alice follows a rabbit"}`+"\n"), 0600)
	os.WriteFile(filepath.Join(root, "cache/id_map.json"), []byte(`{"concept":{"abcdef123456":"alice"}}`), 0600)
	os.WriteFile(filepath.Join(root, "wiki/alice.md"), []byte("Alice follows a rabbit."), 0600)
	cases := filepath.Join(root, "cases.jsonl")
	os.WriteFile(cases, []byte(`{"id":"alice","query":"Alice","mode":"wiki"}`+"\n"), 0600)
	snapshot, expectedSlug := root, "alice"
	if imported := os.Getenv("LWC_E2E_SNAPSHOT"); imported != "" {
		snapshot = imported
		prepared, err := preflightSnapshot(context.Background(), snapshot)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := prepared.cache.All(context.Background(), prepared.reader)
		if err != nil {
			t.Fatal(err)
		}
		expectedSlug = ""
		for _, entry := range entries {
			if entry.Title == "Alice" {
				expectedSlug = entry.Slug
			}
		}
		if expectedSlug == "" {
			t.Fatal("native producer did not materialize Alice")
		}
	}
	request := localRequest{Operation: "query", Snapshot: snapshot, Cases: cases, Config: "../../configs/query/dev/query-dev-2026-08-31.1.json", Project: "test-project", Generation: "local-test"}
	bindingRequest := request
	bindingRequest.Operation, bindingRequest.Profile, bindingRequest.Prompt = "bind-config", "corpus-derived-tech-document-v1", "domain-neutral-technical-v1"
	bindingRequest.Config = "../../../../scripts/local_e2e/fixtures/query-dns.json"
	input, _ := json.Marshal(bindingRequest)
	var bound bytes.Buffer
	if err := localPlatformMain(bytes.NewReader(input), &bound); err != nil {
		t.Fatal(err)
	}
	request.Config = filepath.Join(t.TempDir(), "bound.json")
	if err := os.WriteFile(request.Config, bound.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	sealed, err := queryconfig.LoadFile(request.Config)
	if err != nil || len(sealed.ProjectBindings) != 1 || sealed.ProjectBindings[0].ProfileID != bindingRequest.Profile {
		t.Fatal("missing exact production binding", err)
	}
	for _, stage := range []int{70, 80, 90, 100} {
		request.Stage = stage
		input, _ := json.Marshal(request)
		var output bytes.Buffer
		if err := localPlatformMain(bytes.NewReader(input), &output); err != nil {
			t.Fatalf("stage %d: %v", stage, err)
		}
		if bytes.Contains(output.Bytes(), []byte("explicit-mock-key")) {
			t.Fatal("credential leaked")
		}
		if err := json.Unmarshal(output.Bytes(), &request.Saved); err != nil {
			t.Fatal(err)
		}
		if request.Saved["alice"].Status != "success" {
			t.Fatalf("stage %d: %s", stage, output.String())
		}
		if transport.expansion.Load() != 3 {
			t.Fatalf("stage %d reran provider", stage)
		}
	}
	result := request.Saved["alice"].Result
	if identity := request.Saved["alice"].Identity; identity == nil || identity.ProfileID != "corpus-derived-tech-document-v1" {
		t.Fatal("production runtime did not use explicit technical profile")
	}
	if result.AISynth == "" || len(result.Citations) != 1 || result.Citations[0].Slug != expectedSlug || strings.Contains(result.AISynth, "CITATION_REF_") {
		t.Fatalf("missing grounded production result: %#v", result)
	}
	if transport.synthesis.Load() != 1 {
		t.Fatal("synthesis was not isolated")
	}
	// A legitimate empty selection is an observation, not an execution failure.
	observation := request.Saved["alice"]
	observation.Replay.Selection.Selected = nil
	request.Saved["alice"] = observation
	input, _ = json.Marshal(request)
	var empty bytes.Buffer
	if err := localPlatformMain(bytes.NewReader(input), &empty); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(empty.Bytes(), &request.Saved); err != nil {
		t.Fatal(err)
	}
	if request.Saved["alice"].Status != "no_grounded_answer" || request.Saved["alice"].Result.Status != "insufficient_evidence" {
		t.Fatal("lost truthful no-evidence observation")
	}
	if transport.synthesis.Load() != 1 || transport.expansion.Load() != 3 {
		t.Fatal("empty selection unexpectedly invoked a provider")
	}
}

func TestImportManifestBoundsRejectBeforeAnyObjectRead(t *testing.T) {
	for _, kind := range []string{"files", "object", "total"} {
		t.Run(kind, func(t *testing.T) {
			reader, snapshot := importFixture(t)
			switch kind {
			case "files":
				for len(snapshot.Manifest.Files) <= generation.MaxFiles {
					snapshot.Manifest.Files = append(snapshot.Manifest.Files, snapshot.Manifest.Files[0])
				}
			case "object":
				snapshot.Manifest.Files[0].Size = generation.MaxFileBytes + 1
			case "total":
				row := snapshot.Manifest.Files[0]
				snapshot.Manifest.Files = nil
				for i := 0; i < 9; i++ {
					row.Path = fmt.Sprintf("wiki/page-%02d.md", i)
					row.Size = generation.MaxFileBytes
					snapshot.Manifest.Files = append(snapshot.Manifest.Files, row)
				}
			}
			root, _ := filepath.EvalSymlinks(t.TempDir())
			if err := materializePinnedSnapshot(context.Background(), reader, snapshot, filepath.Join(root, "snapshot")); err == nil {
				t.Fatal("accepted oversized manifest")
			}
			if len(reader.reads) != 0 {
				t.Fatal("read object before rejecting manifest bounds")
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 0 {
				t.Fatal("created import before bounds validation")
			}
		})
	}
}
