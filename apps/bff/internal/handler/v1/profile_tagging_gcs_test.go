package v1

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
)

// This is a local HTTP GCS protocol fake. STORAGE_EMULATOR_HOST prevents ADC
// lookup and routes the production public entrypoint exclusively to localhost.
func TestRunProfileTaggingPublicGCSImmutableSnapshot(t *testing.T) {
	reader, manifest := taggingInventoryFixture(t)
	h, repo, artifacts, _, evaluator, policy := taggingRunnerFixture(t)
	var dictionary profileartifacts.DictionaryEnvelope
	if err := json.Unmarshal(artifacts[profileartifacts.DictionaryObjectPath(repo.state.Candidate.Dictionary.Revision)], &dictionary); err != nil {
		t.Fatal(err)
	}
	dictionary.CanonicalConceptsDigest = generation.Digest(reader.files["cache/concepts.jsonl"])
	data, ref, err := profileartifacts.EncodeDictionary(dictionary, []string{"r1"})
	if err != nil {
		t.Fatal(err)
	}
	repo.state.Candidate.Dictionary = profileDerivedRefFromArtifact(ref)
	objects := map[string]taggingGCSObject{}
	prefix := "users/u/projects/p/"
	for _, f := range manifest.Files {
		objects[prefix+manifest.ObjectPath(f)] = taggingGCSObject{reader.files[f.Path], f.Generation}
	}
	for p, b := range reader.files {
		if strings.HasPrefix(p, ".lwc/") {
			g := reader.generations[p]
			if g == 0 {
				g = 17
			}
			objects[prefix+p] = taggingGCSObject{b, g}
		}
	}
	archive, err := generation.ArchivedManifestPath(manifest.GenerationID)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	objects[prefix+archive] = taggingGCSObject{manifestBytes, 21}
	objects[prefix+profileartifacts.DictionaryObjectPath(ref.Revision)] = taggingGCSObject{data, 22}
	client, mu, reads := newTaggingGCSClient(t, objects)
	h.store = client
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Exercise create-only writes and byte-identical retry through the real client.
	mu.Lock()
	delete(objects, prefix+profileartifacts.DictionaryObjectPath(ref.Revision))
	mu.Unlock()
	artifactStore := profileTagObjectStore{client.WithScope("u", "p")}
	for i := 0; i < 2; i++ {
		if err := artifactStore.Create(ctx, profileartifacts.DictionaryObjectPath(ref.Revision), data); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.RunProfileTagging(ctx, "u", "p", 1, "candidate", "job", evaluator, policy); err != nil {
		t.Fatal(err)
	}
	if !repo.coverage || repo.state.Job.Status != profileJobReady || evaluator.calls[profiletags.Source] != 1 || evaluator.calls[profiletags.Concept] != 1 {
		t.Fatalf("public run=%+v calls=%v", repo, evaluator.calls)
	}
	// Simulate duplicate delivery of an idempotent running claim under its lease.
	if err := h.RunProfileTagging(ctx, "u", "p", 1, "candidate", "job", evaluator, policy); err != nil {
		t.Fatal(err)
	}
	if evaluator.calls[profiletags.Source] != 1 || evaluator.calls[profiletags.Concept] != 1 {
		t.Fatalf("duplicate reevaluated: %v", evaluator.calls)
	}
	mu.Lock()
	for path, object := range objects {
		if strings.Contains(path, "/tags/source-bytes/v1/") {
			object.gen++
			objects[path] = object
		}
	}
	mu.Unlock()
	if err := h.RunProfileTagging(ctx, "u", "p", 1, "candidate", "job", evaluator, policy); err == nil {
		t.Fatal("replaced source object generation accepted")
	}
	if evaluator.calls[profiletags.Source] != 1 || evaluator.calls[profiletags.Concept] != 1 {
		t.Fatal("invalid source version reached provider")
	}
	mu.Lock()
	defer mu.Unlock()
	if reads[prefix+generation.ManifestPath] != 0 {
		t.Fatal("read moving current manifest")
	}
	for p := range reads {
		if strings.HasPrefix(p, prefix+"raw/") || strings.Contains(p, "/wiki/") {
			t.Fatalf("unexpected mutable or concept page read: %s", p)
		}
	}
}

type taggingGCSObject struct {
	data []byte
	gen  int64
}

func newTaggingGCSClient(t *testing.T, objects map[string]taggingGCSObject) (*gcs.Client, *sync.Mutex, map[string]int) {
	t.Helper()
	var mu sync.Mutex
	reads := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			if r.URL.Query().Get("ifGenerationMatch") != "0" {
				t.Errorf("non-create-only write: %s", r.URL)
				http.Error(w, "precondition required", 400)
				return
			}
			_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || params["boundary"] == "" {
				t.Errorf("unexpected upload %s %s", r.URL, r.Header.Get("Content-Type"))
				http.Error(w, "multipart required", 400)
				return
			}
			parts := multipart.NewReader(r.Body, params["boundary"])
			metadata, err := parts.NextPart()
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			var attrs struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(metadata).Decode(&attrs); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			content, err := parts.NextPart()
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			body, err := io.ReadAll(content)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if _, exists := objects[attrs.Name]; exists {
				http.Error(w, "exists", http.StatusPreconditionFailed)
				return
			}
			objects[attrs.Name] = taggingGCSObject{body, 100 + int64(len(objects))}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"name": attrs.Name, "bucket": "tagging-local", "generation": strconv.FormatInt(objects[attrs.Name].gen, 10), "size": strconv.Itoa(len(body))})
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/tagging-local/")
		if i := strings.Index(r.URL.Path, "/o/"); i >= 0 {
			name = r.URL.Path[i+3:]
		}
		if decoded, err := url.PathUnescape(name); err == nil {
			name = decoded
		}
		reads[name]++
		o, ok := objects[name]
		if !ok {
			http.Error(w, "missing", 404)
			return
		}
		if strings.Contains(name, "/cache/") && r.URL.Query().Get("generation") == "" {
			t.Errorf("unpinned content read: %s", name)
			http.Error(w, "exact generation required", 400)
			return
		}
		if g := r.URL.Query().Get("generation"); g != "" && g != strconv.FormatInt(o.gen, 10) {
			http.Error(w, "wrong generation", 404)
			return
		}
		if strings.Contains(r.URL.Path, "/storage/v1/") && r.URL.Query().Get("alt") != "media" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"name": name, "bucket": "tagging-local", "generation": strconv.FormatInt(o.gen, 10), "size": strconv.Itoa(len(o.data))})
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(o.data)))
		w.Header().Set("X-Goog-Generation", strconv.FormatInt(o.gen, 10))
		w.Header().Set("Last-Modified", time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat))
		_, _ = w.Write(o.data)
	}))
	t.Cleanup(server.Close)
	t.Setenv("STORAGE_EMULATOR_HOST", server.URL)
	client, err := gcs.NewClient("tagging-local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, &mu, reads
}
