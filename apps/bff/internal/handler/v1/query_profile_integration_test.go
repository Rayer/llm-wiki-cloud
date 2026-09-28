package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryquality"
	"github.com/rayer/llm-wiki-bff/internal/sourcestatus"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

// These object-store, evaluator, and HTTP responses are synthetic test evidence;
// the handler, artifact engine, retrieval executor, and citation synthesis are real.
type profileQueryStore struct {
	storage.Store
	candidate     ProfileCandidate
	dictionaryRef profileartifacts.DerivedRef
	prefix, id    string
	objects       map[string][]byte
	manifest      generation.Manifest
	snapshot      generation.SourceSnapshotManifest
	snapshotErr   error
	reads         map[string]int
	pages, lists  int
	retained      map[string]*profileQueryStore
	latest        *profileQueryStore
	pins          []string
}

func (s *profileQueryStore) Prefix() string    { return s.prefix }
func (s *profileQueryStore) ViewToken() string { return s.prefix + ":" + s.id }
func (s *profileQueryStore) QueryGenerationIdentity(context.Context) (storage.QueryGenerationIdentity, error) {
	f, _ := s.manifest.File(cache.GCSPath)
	return storage.QueryGenerationIdentity{ProjectID: "p", GenerationID: s.id, ConceptsDigest: "sha256:" + f.SHA256}, nil
}
func (s *profileQueryStore) Pin(context.Context) (storage.Store, error) { return s.latest, nil }
func (s *profileQueryStore) PinQueryGeneration(_ context.Context, id string) (storage.Store, generation.Manifest, error) {
	s.pins = append(s.pins, id)
	p := s.retained[id]
	if p == nil {
		return nil, generation.Manifest{}, fs.ErrNotExist
	}
	return p, p.manifest, nil
}
func (s *profileQueryStore) ReadSourceSnapshot(context.Context) (generation.SourceSnapshotManifest, error) {
	return s.snapshot, s.snapshotErr
}
func (s *profileQueryStore) ReadFile(_ context.Context, p string) ([]byte, error) {
	s.reads[p]++
	if strings.HasSuffix(p, ".md") {
		s.pages++
	}
	b, ok := s.objects[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (s *profileQueryStore) ReadFileLimited(ctx context.Context, p string, n int64) ([]byte, error) {
	b, e := s.ReadFile(ctx, p)
	if int64(len(b)) > n {
		return nil, fmt.Errorf("fake object too large")
	}
	return b, e
}
func (s *profileQueryStore) Read(ctx context.Context, p string, n int) ([]byte, error) {
	return s.ReadFileLimited(ctx, p, int64(n))
}
func (s *profileQueryStore) Create(_ context.Context, p string, b []byte) error {
	if old, ok := s.objects[p]; ok && !bytes.Equal(old, b) {
		return fmt.Errorf("immutable conflict")
	}
	s.objects[p] = append([]byte(nil), b...)
	return nil
}
func (s *profileQueryStore) GetPage(context.Context, string, string) (*storage.WikiPage, []byte, error) {
	s.pages++
	return nil, nil, fmt.Errorf("unexpected page scan")
}
func (s *profileQueryStore) ListConcepts(context.Context, bool) ([]storage.WikiPage, error) {
	s.lists++
	return nil, fmt.Errorf("unexpected concept list")
}
func (s *profileQueryStore) ListConceptsFromCache(context.Context) ([]storage.WikiPage, error) {
	s.lists++
	return nil, fmt.Errorf("unexpected concept cache list")
}
func (s *profileQueryStore) ListSources(context.Context) ([]storage.WikiPage, error) {
	s.lists++
	return nil, fmt.Errorf("unexpected source list")
}
func (s *profileQueryStore) ListSourcesFromCache(context.Context) ([]storage.WikiPage, error) {
	s.lists++
	return nil, fmt.Errorf("unexpected source cache list")
}
func (s *profileQueryStore) ListMarkdownFiles(context.Context, string) ([]storage.MarkdownFile, error) {
	s.lists++
	return nil, fmt.Errorf("unexpected markdown list")
}

type profileQueryRoot struct {
	storage.Store
	scopes map[string]*profileQueryStore
}

func (r *profileQueryRoot) Scope(u, p string) storage.Store { return r.scopes[u+"/"+p] }

type profileQueryEvaluator struct{}

func (profileQueryEvaluator) Evaluate(_ context.Context, i profiletags.Item, _ profiletags.Tag, _ profiletags.ProviderPolicy) (profiletags.Evaluation, error) {
	j := profiletags.NoMatch
	if i.Kind == profiletags.Source {
		j = profiletags.Match
	}
	return profiletags.Evaluation{Judgment: j, ReturnedModel: "synthetic-model"}, nil
}

func profileQueryFixture(t *testing.T, prefix, id string) (*profileQueryStore, *ProfileActive) {
	t.Helper()
	s := &profileQueryStore{prefix: prefix, id: id, objects: map[string][]byte{}, reads: map[string]int{}}
	const stable = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	row := []byte(fmt.Sprintf(`{"slug":"coffee","title":"Coffee %s","body":"coffee espresso %s retained body","sources":["abcdef123456"],"frontmatter":{"id":"%s"}}`, id, id, stable))
	s.objects[cache.GCSPath] = append(append([]byte(nil), row...), '\n')
	marshal := func(v any) []byte {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	s.objects["cache/id_map.json"] = marshal(wikiindex.IDMap{Redirects: map[string][]string{}, Concept: map[string]string{stable: "coffee"}, Source: map[string]string{"abcdef123456": "abcdef123456"}})
	raw := []byte("synthetic source coffee")
	rawDigest := generation.Digest(raw)
	s.objects[sourcestatus.Path] = marshal(sourcestatus.Artifact{Version: 1, Sources: map[string]sourcestatus.Receipt{"abcdef123456": {RawPath: "raw/source.txt", LastIngestedRawSHA256: rawDigest}}})
	s.snapshot = generation.SourceSnapshotManifest{SchemaVersion: generation.SourceSnapshotSchema, ContentGeneration: id, IDMapDigest: generation.Digest(s.objects["cache/id_map.json"]), SourceStatusDigest: generation.Digest(s.objects[sourcestatus.Path]), Rows: []generation.SourceSnapshotRow{{StableID: "abcdef123456", RawPath: "raw/source.txt", ContentDigest: rawDigest, ObjectGeneration: 1}}}
	_, snapshotDigest, e := generation.EncodeSourceSnapshot(s.snapshot)
	if e != nil {
		t.Fatal(e)
	}
	s.manifest = generation.Manifest{GenerationID: id, SourceSnapshotDigest: snapshotDigest}
	for _, p := range []string{cache.GCSPath, "cache/id_map.json"} {
		b := s.objects[p]
		s.manifest.Files = append(s.manifest.Files, generation.File{Path: p, Size: int64(len(b)), SHA256: generation.Digest(b), Generation: 1})
	}
	tags := []profileartifacts.Tag{{ID: "required_place", Definition: "place", AppliesTo: []string{"source", "concept"}, MatchRule: "yes", NonMatchRule: "no", UnknownRule: "unclear", RequirementIDs: []string{"r1"}, QueryUse: "required_candidate"}, {ID: "source_preference", Definition: "source preference", AppliesTo: []string{"source", "concept"}, MatchRule: "yes", NonMatchRule: "no", UnknownRule: "unclear", RequirementIDs: []string{"r1"}, QueryUse: "preferred"}}
	dict, ref, e := profileartifacts.EncodeDictionary(profileartifacts.DictionaryEnvelope{SchemaVersion: profileartifacts.DictionarySchema, InputDigest: generation.Digest([]byte("requirements")), ContentGeneration: id, CanonicalConceptsDigest: generation.Digest(s.objects[cache.GCSPath]), ModelVersion: "synthetic-model", PromptVersion: "synthetic-v1", Tags: tags}, []string{"r1"})
	if e != nil {
		t.Fatal(e)
	}
	s.objects[profileartifacts.DictionaryObjectPath(ref.Revision)] = dict
	s.dictionaryRef = ref
	s.candidate = ProfileCandidate{CandidateID: id + "-candidate", ContentGeneration: id, Dictionary: ProfileDerivedRef{Revision: ref.Revision, InputDigest: ref.InputDigest, ModelVersion: ref.ModelVersion, PromptVersion: ref.PromptVersion, SchemaVersion: ref.SchemaVersion}, Preview: ProfilePreview{Requirements: []ProfileRequirementAccounting{{ID: "r1"}}}}
	dictionary := profiletags.Dictionary{Revision: ref.Revision}
	for _, tag := range tags {
		dictionary.Tags = append(dictionary.Tags, profiletags.Tag{ID: tag.ID, Definition: tag.Definition, AppliesTo: []profiletags.Kind{profiletags.Source, profiletags.Concept}, MatchRule: tag.MatchRule, NonMatchRule: tag.NonMatchRule, UnknownRule: tag.UnknownRule, QueryUse: tag.QueryUse})
	}
	// Build is the current production profiletags engine entry point.
	built, e := profiletags.Build(context.Background(), s, profileQueryEvaluator{}, profiletags.Input{Inventory: profiletags.Inventory{ContentGeneration: id, ConceptsDigest: generation.Digest(s.objects[cache.GCSPath]), IDMapDigest: s.snapshot.IDMapDigest, SourceSnapshotDigest: snapshotDigest, Items: []profiletags.Item{{Kind: profiletags.Concept, StableID: stable, Content: row}, {Kind: profiletags.Source, StableID: "abcdef123456", Content: raw}}}, Dictionary: dictionary, Provider: profiletags.ProviderPolicy{ConfiguredModel: "synthetic-model", AcceptedReturnedModels: []string{"synthetic-model"}, PromptVersion: "synthetic-v1", SchemaVersion: "eval.v1"}, MaxAttempts: 1})
	if e != nil {
		t.Fatal(e)
	}
	s.reads = map[string]int{}
	return s, &ProfileActive{CandidateID: s.candidate.CandidateID, ContentGeneration: id, DictionaryRevision: ref.Revision, TagSetRevision: built.SetRevision, QueryRuleRevision: built.RulesRevision}
}

type profileQueryRepository struct {
	*profileRepositoryStub
	candidate ProfileCandidate
	reads     int
}

func (r *profileQueryRepository) GetProfileCandidate(_ context.Context, u, p, id string) (ProfileCandidate, error) {
	r.reads++
	if id != r.candidate.CandidateID {
		return ProfileCandidate{}, fs.ErrNotExist
	}
	return r.candidate, nil
}

type profileQueryTransport struct {
	prompts []string
}

func (f *profileQueryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return nil, e
	}
	var req struct {
		Messages []struct{ Role, Content string }
	}
	if e = json.Unmarshal(b, &req); e != nil {
		return nil, e
	}
	prompt := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			prompt = m.Content
		}
	}
	f.prompts = append(f.prompts, prompt)
	start := strings.Index(prompt, "[CITATION_REF_")
	if start < 0 {
		return nil, fmt.Errorf("synthetic synthesis missing citation")
	}
	end := strings.IndexByte(prompt[start:], ']')
	token := prompt[start : start+end+1]
	out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "Synthetic retained answer " + token}}}})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(out))}, nil
}
func profileQueryHandler(t *testing.T, cc *cache.Cache, root *profileQueryRoot, active *ProfileActive) *Handler {
	t.Helper()
	repo := &profileQueryRepository{profileRepositoryStub: &profileRepositoryStub{state: ProfileState{ProjectID: "p", Active: active, Candidate: &ProfileCandidate{CandidateID: "new-G2-candidate", ContentGeneration: "G2-current"}}}}
	if active != nil {
		for _, scope := range root.scopes {
			if g := scope.retained[active.ContentGeneration]; g != nil {
				repo.candidate = g.candidate
				break
			}
		}
	}
	h := &Handler{store: root, cache: cc, profileRepository: repo}
	executor, e := queryquality.NewStrictProductionExecutorWithQueryServiceConfig(cc, nil, nil, query.NewService(cc, nil, llm.NewClient("synthetic-no-network")), queryquality.DefaultRetrievalProfile(), queryquality.StructuredPlanPromptID, queryquality.DefaultOptions(), query.RuntimeConfigIdentity{})
	if e != nil {
		t.Fatal(e)
	}
	h.SetQueryExecutor(executor)
	return h
}
func profileQueryRequest(h *Handler, user, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router := gin.New()
	router.POST("/api/v1/query", func(c *gin.Context) { c.Set("userID", user); c.Set("projectID", "p"); h.Query(c) })
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(body)))
	return w
}

func TestProfileQueryProductionPinnedGeneration(t *testing.T) {
	g1, active := profileQueryFixture(t, "u/p", "G1-retained")
	g2, _ := profileQueryFixture(t, "u/p", "G2-current")
	scope := &profileQueryStore{retained: map[string]*profileQueryStore{"G1-retained": g1}, latest: g2}
	root := &profileQueryRoot{scopes: map[string]*profileQueryStore{"u/p": scope}}
	transport := &profileQueryTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	cc := cache.New()
	h := profileQueryHandler(t, cc, root, active)
	for i := 0; i < 2; i++ {
		w := profileQueryRequest(h, "u", `{"q":"coffee","mode":"wiki"}`)
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
		var got handler.QueryResponse
		if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
			t.Fatal(e)
		}
		if len(got.Results) != 1 || got.Results[0].Title != "Coffee G1-retained" || len(got.Citations) != 1 || got.Citations[0].Text != "Coffee G1-retained" || got.AISynth == "" {
			t.Fatalf("response=%+v", got)
		}
	}
	if len(scope.pins) != 2 || scope.pins[0] != "G1-retained" || scope.pins[1] != "G1-retained" {
		t.Fatalf("pins=%v", scope.pins)
	}
	if h.profileRepository.(*profileQueryRepository).reads != 2 {
		t.Fatal("historical candidate not read once per request")
	}
	if len(transport.prompts) != 2 {
		t.Fatalf("synthesis calls=%d", len(transport.prompts))
	}
	for _, p := range transport.prompts {
		if !strings.Contains(p, "G1-retained retained body") || strings.Contains(p, "G2-current retained body") {
			t.Fatalf("wrong body: %s", p)
		}
	}
	if g1.reads[cache.GCSPath] != 1 || g2.reads[cache.GCSPath] != 0 || g1.pages+g1.lists+g2.pages+g2.lists != 0 {
		t.Fatalf("reads G1=%v G2=%v scans=%d", g1.reads, g2.reads, g1.pages+g1.lists+g2.pages+g2.lists)
	}
	for p := range g1.reads {
		if strings.Contains(p, "/decisions/") || strings.Contains(p, "/source-bytes/") || strings.HasPrefix(p, "raw/") || p == sourcestatus.Path {
			t.Fatalf("small decision read: %s", p)
		}
	}
	snapshot, e := query.LoadProfile(context.Background(), cc, g1, g1.manifest, profiletags.ActiveRef{ContentGeneration: active.ContentGeneration, DictionaryRevision: active.DictionaryRevision, TagSetRevision: active.TagSetRevision, QueryRuleRevision: active.QueryRuleRevision}, g1.dictionaryRef, []string{"r1"})
	if e != nil {
		t.Fatal(e)
	}
	if len(snapshot.PreferenceScores()) != 0 {
		t.Fatalf("source match inherited by concept: %v", snapshot.PreferenceScores())
	}
	w := profileQueryRequest(h, "u", `{"q":"coffee","required_tag_ids":["required_place"]}`)
	if w.Code != 422 {
		t.Fatalf("required status=%d body=%s", w.Code, w.Body)
	}
}

func TestProfileQueryProductionNoProfileAndScopeIsolation(t *testing.T) {
	cc := cache.New()
	a, _ := profileQueryFixture(t, "a/p", "G2-current")
	b, _ := profileQueryFixture(t, "b/p", "G2-current")
	b.objects[cache.GCSPath] = bytes.ReplaceAll(b.objects[cache.GCSPath], []byte("G2-current"), []byte("tenant B"))
	root := &profileQueryRoot{scopes: map[string]*profileQueryStore{"a/p": {latest: a}, "b/p": {latest: b}}}
	transport := &profileQueryTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	h := profileQueryHandler(t, cc, root, nil)
	for _, tc := range []struct{ user, title string }{{"a", "Coffee G2-current"}, {"b", "Coffee tenant B"}} {
		w := profileQueryRequest(h, tc.user, `{"q":"coffee"}`)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.title) {
			t.Fatalf("%s: %d %s", tc.user, w.Code, w.Body)
		}
	}
	h.profileRepository = nil
	w := profileQueryRequest(h, "a", `{"q":"coffee"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Coffee G2-current") {
		t.Fatalf("legacy: %d %s", w.Code, w.Body)
	}
	if len(root.scopes["a/p"].pins)+len(root.scopes["b/p"].pins) != 0 {
		t.Fatal("no-profile query pinned an active generation")
	}
}

func TestProfileQueryProductionRejectsIncompleteArtifacts(t *testing.T) {
	for _, kind := range []string{"missing snapshot", "corpus digest", "ID map digest", "dictionary digest", "tag set digest", "rules digest", "snapshot identity", "source membership", "historical candidate"} {
		t.Run(kind, func(t *testing.T) {
			g, active := profileQueryFixture(t, "u/p", "G1-retained")
			switch kind {
			case "missing snapshot":
				g.snapshotErr = fs.ErrNotExist
			case "corpus digest":
				g.objects[cache.GCSPath] = append(g.objects[cache.GCSPath], ' ')
			case "ID map digest":
				g.objects["cache/id_map.json"] = []byte(`{}`)
			case "dictionary digest":
				p := profileartifacts.DictionaryObjectPath(active.DictionaryRevision)
				g.objects[p] = append(g.objects[p], ' ')
			case "rules digest":
				p := profiletags.RulesPath(active.QueryRuleRevision)
				g.objects[p] = append(g.objects[p], ' ')
			case "source membership":
				g.snapshot.Rows[0].StableID = "other-source"
			case "snapshot identity":
				g.snapshot.ContentGeneration = "G2-current"
			case "historical candidate":
				g.candidate.ContentGeneration = "G2-current"
			case "tag set digest":
				p := profiletags.SetPath(active.TagSetRevision)
				g.objects[p] = append(g.objects[p], ' ')
			}
			root := &profileQueryRoot{scopes: map[string]*profileQueryStore{"u/p": {retained: map[string]*profileQueryStore{"G1-retained": g}, latest: g}}}
			h := profileQueryHandler(t, cache.New(), root, active)
			w := profileQueryRequest(h, "u", `{"q":"coffee"}`)
			if w.Code != 500 || !strings.Contains(w.Body.String(), "generated data unavailable") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if g.pages+g.lists != 0 {
				t.Fatal("invalid artifacts triggered scan")
			}
		})
	}
}

func TestProfileQueryReadFailureAndExplicitNoProfileRequirement(t *testing.T) {
	g, _ := profileQueryFixture(t, "u/p", "G1-retained")
	root := &profileQueryRoot{scopes: map[string]*profileQueryStore{"u/p": {latest: g}}}
	h := profileQueryHandler(t, cache.New(), root, nil)
	if w := profileQueryRequest(h, "u", `{"q":"coffee","required_tag_ids":["required_place"]}`); w.Code != 422 {
		t.Fatalf("unsupported without Profile: %d %s", w.Code, w.Body)
	}
	repo := h.profileRepository.(*profileQueryRepository)
	repo.getErr = errors.New("synthetic repository unavailable")
	if w := profileQueryRequest(h, "u", `{"q":"coffee"}`); w.Code != 500 {
		t.Fatalf("read failure downgraded: %d %s", w.Code, w.Body)
	}
	if len(g.reads) != 0 {
		t.Fatalf("read failure used baseline: %v", g.reads)
	}
	repo.getErr = errProfileProjectNotFound
	if w := profileQueryRequest(h, "u", `{"q":"coffee"}`); w.Code != 404 {
		t.Fatalf("unauthorized project: %d %s", w.Code, w.Body)
	}
}

// Observe the request at the real executor boundary without replacing retrieval
// or synthesis. The connected runtime fixture supplies the actual active state.
type runtimeQueryObserver struct {
	query.Executor
	t      *testing.T
	active bool
}

func (o runtimeQueryObserver) Execute(ctx context.Context, reader cache.Reader, request query.Request) (query.Result, error) {
	o.t.Helper()
	if (request.Profile != nil) != o.active {
		o.t.Fatalf("active snapshot presence = %v, want %v", request.Profile != nil, o.active)
	}
	if request.Profile != nil && request.Profile.PreferenceScores()["alpha"] != 1 {
		o.t.Fatalf("runtime concept preference absent: %v", request.Profile.PreferenceScores())
	}
	return o.Executor.Execute(ctx, reader, request)
}

func assertRuntimeQuery(t *testing.T, f *runtimeFixture, active bool, title string, status int) {
	t.Helper()
	cc := cache.New()
	h := &Handler{store: f.dispatcher.Handler.store, cache: cc, profileRepository: f.repo}
	executor, err := queryquality.NewStrictProductionExecutorWithQueryServiceConfig(cc, nil, nil, query.NewService(cc, nil, llm.NewClient("synthetic-no-network")), queryquality.DefaultRetrievalProfile(), queryquality.StructuredPlanPromptID, queryquality.DefaultOptions(), query.RuntimeConfigIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	h.SetQueryExecutor(runtimeQueryObserver{Executor: executor, t: t, active: active})
	transport := &profileQueryTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = runtimeRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Host, "127.0.0.1:") {
			return old.RoundTrip(r)
		}
		if r.URL.Host != "api.deepseek.com" {
			return nil, fmt.Errorf("unexpected nonlocal query host %s", r.URL.Host)
		}
		return transport.RoundTrip(r)
	})
	defer func() { http.DefaultTransport = old }()
	router := gin.New()
	router.POST("/api/v1/query", func(c *gin.Context) { c.Set("userID", f.user); c.Set("projectID", f.project); h.Query(c) })
	request := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(body)))
		return w
	}
	w := request(`{"q":"alpha"}`)
	if w.Code != status {
		t.Fatalf("connected Query status=%d want=%d body=%s", w.Code, status, w.Body)
	}
	if status != 200 {
		if w.Body.String() != `{"error":"generated data unavailable"}` {
			t.Fatalf("snapshot error: %s", w.Body)
		}
		if len(transport.prompts) != 0 {
			t.Fatal("unavailable snapshot reached synthesis")
		}
		return
	}
	var response handler.QueryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Title != title || len(response.Citations) != 1 || response.Citations[0].Text != title || response.Results[0].ID != "01JAZ5N7Y3K8M2Q4R6T9VWXABC" || response.Citations[0].ID != response.Results[0].ID || response.Citations[0].Slug != "alpha" || response.AISynth == "" {
		t.Fatalf("connected Query result/citations: %+v", response)
	}
	if len(transport.prompts) != 1 || !strings.Contains(transport.prompts[0], "Alpha retained body") || strings.Contains(transport.prompts[0], "Changed Alpha") {
		t.Fatalf("connected synthesis not pinned: %v", transport.prompts)
	}
	w = request(`{"q":"alpha","required_tag_ids":["place"]}`)
	if w.Code != 422 || w.Body.String() != `{"error":"required Profile condition is unsupported"}` {
		t.Fatalf("connected required safe gate: %d %s", w.Code, w.Body)
	}
}
