package v1

import (
	"bytes"
	cloudstorage "cloud.google.com/go/storage"
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
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/api/option"
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

type sdkDictionaryProfileStore struct {
	*profileQueryStore
	bucket *cloudstorage.BucketHandle
}

func (s *sdkDictionaryProfileStore) ReadFileLimited(ctx context.Context, path string, limit int64) ([]byte, error) {
	if path != profileartifacts.DictionaryObjectPath(s.dictionaryRef.Revision) {
		return s.profileQueryStore.ReadFileLimited(ctx, path, limit)
	}
	finish := func(string) {}
	if recorder := llm.HostCallRecorderFromContext(ctx); recorder != nil {
		if contextual, ok := recorder.(llm.ContextHostCallRecorder); ok {
			finish = contextual.StartHostCallContext(ctx, llm.CallStage(ctx), "https", "storage.googleapis.com")
		} else {
			finish = recorder.StartHostCall(llm.CallStage(ctx), "https", "storage.googleapis.com")
		}
	}
	reader, err := s.bucket.Object(s.prefix + "/" + path).NewReader(ctx)
	if err != nil {
		finish("provider_error")
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit))
	if err != nil {
		finish("provider_error")
		_ = reader.Close()
		return nil, err
	}
	finish("success")
	if err := reader.Close(); err != nil {
		return nil, err
	}
	return data, nil
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

func TestProfileCandidateVerificationSpanParentsRequestAndEndsBeforePin(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})

	transport := &profileQueryTransport{}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	generationStore, active := profileQueryFixture(t, "u/p", "G1-retained")
	scope := &profileQueryStore{retained: map[string]*profileQueryStore{"G1-retained": generationStore}, latest: generationStore}
	rootStore := &profileQueryRoot{scopes: map[string]*profileQueryStore{"u/p": scope}}
	h := profileQueryHandler(t, cache.New(), rootStore, active)
	response := profileQueryRequest(h, "u", `{"q":"coffee","mode":"wiki"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("profile query status=%d body=%s", response.Code, response.Body)
	}
	spans := exporter.GetSpans()
	root, rootOK := findSpan(spans, "query.request")
	candidate, candidateOK := findSpan(spans, "query.profile.candidate")
	pin, pinOK := findSpan(spans, "query.generation.pin")
	if !rootOK || !candidateOK || !pinOK {
		t.Fatalf("profile trace missing root/candidate/pin: %+v", spans)
	}
	if candidate.Parent.SpanID() != root.SpanContext.SpanID() || candidate.EndTime.After(pin.StartTime) {
		t.Fatalf("candidate span parent/lifetime did not cover active tuple validation: root=%+v candidate=%+v pin=%+v", root, candidate, pin)
	}
	if got := spanStringAttribute(candidate.Attributes, "query.outcome"); got != "success" {
		t.Fatalf("candidate validation outcome=%q, want success", got)
	}
	if generationStore.reads[cache.GCSPath] != 1 || h.profileRepository.(*profileQueryRepository).reads != 1 {
		t.Fatalf("profile reads=%d candidate reads=%d, want one pinned corpus and one active tuple", generationStore.reads[cache.GCSPath], h.profileRepository.(*profileQueryRepository).reads)
	}

	exporter.Reset()
	badStore, badActive := profileQueryFixture(t, "u/p", "G1-retained")
	badRoot := &profileQueryRoot{scopes: map[string]*profileQueryStore{"u/p": {retained: map[string]*profileQueryStore{"G1-retained": badStore}, latest: badStore}}}
	badHandler := profileQueryHandler(t, cache.New(), badRoot, badActive)
	badHandler.profileRepository.(*profileQueryRepository).candidate.ContentGeneration = "wrong-generation"
	failed := profileQueryRequest(badHandler, "u", `{"q":"coffee","mode":"wiki"}`)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("mismatched active tuple status=%d body=%s", failed.Code, failed.Body)
	}
	spans = exporter.GetSpans()
	candidate, candidateOK = findSpan(spans, "query.profile.candidate")
	if !candidateOK || spanStringAttribute(candidate.Attributes, "query.outcome") != "failure" || hasSpan(spans, "query.generation.pin") {
		t.Fatalf("mismatched active tuple did not close candidate before stopping pin: %+v", spans)
	}
}

func TestProfileSourceInventorySpanEndsOnConceptIdentityMismatch(t *testing.T) {
	storeFixture, active := profileQueryFixture(t, "u/p", "G1-retained")
	var ids wikiindex.IDMap
	if err := json.Unmarshal(storeFixture.objects["cache/id_map.json"], &ids); err != nil {
		t.Fatal(err)
	}
	const stableID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	ids.Concept[stableID] = "wrong-slug"
	updatedIDMap, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	storeFixture.objects["cache/id_map.json"] = updatedIDMap
	storeFixture.snapshot.IDMapDigest = generation.Digest(updatedIDMap)
	_, snapshotDigest, err := generation.EncodeSourceSnapshot(storeFixture.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	storeFixture.manifest.SourceSnapshotDigest = snapshotDigest
	for index, file := range storeFixture.manifest.Files {
		if file.Path == "cache/id_map.json" {
			file.Size = int64(len(updatedIDMap))
			file.SHA256 = generation.Digest(updatedIDMap)
			storeFixture.manifest.Files[index] = file
		}
	}

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	ctx, root := otel.Tracer("llm-wiki-bff/query").Start(context.Background(), "query.request")
	_, err = query.LoadProfile(ctx, cache.New(), storeFixture, storeFixture.manifest,
		profiletags.ActiveRef{ContentGeneration: active.ContentGeneration, DictionaryRevision: active.DictionaryRevision, TagSetRevision: active.TagSetRevision, QueryRuleRevision: active.QueryRuleRevision},
		storeFixture.dictionaryRef, []string{"r1"})
	root.End()
	if err == nil || err.Error() != "profile concept identity mismatch" {
		t.Fatalf("LoadProfile() error=%v, want concept identity mismatch", err)
	}
	var idMapSpan, sourceSpan tracetest.SpanStub
	for _, span := range exporter.GetSpans() {
		if span.Name != "query.storage.read" {
			continue
		}
		switch spanStringAttribute(span.Attributes, "query.object_role") {
		case "id_map":
			idMapSpan = span
		case "source_inventory":
			sourceSpan = span
		}
	}
	if !idMapSpan.SpanContext.SpanID().IsValid() || !sourceSpan.SpanContext.SpanID().IsValid() {
		t.Fatalf("identity rejection omitted read spans: %+v", exporter.GetSpans())
	}
	if spanStringAttribute(idMapSpan.Attributes, "query.outcome") != "success" || spanStringAttribute(sourceSpan.Attributes, "query.outcome") != "failure" || spanStringAttribute(sourceSpan.Attributes, "query.validation_reason") != "identity" {
		t.Fatalf("identity validation span outcomes are wrong: id_map=%+v source_inventory=%+v", idMapSpan, sourceSpan)
	}
}

type invalidDictionaryRoundTripper struct{}

func (invalidDictionaryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	const body = `{"invalid":true}`
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK",
		Header:        http.Header{"Content-Type": []string{"application/octet-stream"}, "Content-Length": []string{fmt.Sprint(len(body))}},
		ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body)), Request: req,
	}, nil
}

func TestActualSDKDictionaryReadSuccessRemainsDistinctFromValidationFailure(t *testing.T) {
	storeFixture, active := profileQueryFixture(t, "users/u/projects/p", "G1-retained")
	storageClient, err := cloudstorage.NewClient(context.Background(),
		option.WithoutAuthentication(),
		option.WithHTTPClient(&http.Client{Transport: invalidDictionaryRoundTripper{}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer storageClient.Close()
	reader := &sdkDictionaryProfileStore{profileQueryStore: storeFixture, bucket: storageClient.Bucket("memory-only-bucket")}

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	rootCtx, root := otel.Tracer("llm-wiki-bff/query").Start(context.Background(), "query.request")
	ctx, recorder := query.WithReceipt(rootCtx)
	stageCtx := recorder.StartStage(ctx, "profile_snapshot", "", "", "")
	_, err = query.LoadProfile(stageCtx, cache.New(), reader, storeFixture.manifest,
		profiletags.ActiveRef{ContentGeneration: active.ContentGeneration, DictionaryRevision: active.DictionaryRevision, TagSetRevision: active.TagSetRevision, QueryRuleRevision: active.QueryRuleRevision},
		storeFixture.dictionaryRef, []string{"r1"})
	query.FinishStage(stageCtx, "failure")
	root.End()
	if err == nil {
		t.Fatal("corrupt actual SDK dictionary unexpectedly validated")
	}
	receipt := recorder.Receipt()
	if len(receipt.HostCalls) != 1 || receipt.HostCalls[0].Outcome != "success" || receipt.HostCalls[0].Host != "storage.googleapis.com" {
		t.Fatalf("successful storage transport receipt=%+v, want one safe successful host call", receipt.HostCalls)
	}
	var dictionarySpan, hostSpan tracetest.SpanStub
	for _, span := range exporter.GetSpans() {
		switch span.Name {
		case "query.storage.read":
			if spanStringAttribute(span.Attributes, "query.object_role") == "dictionary" {
				dictionarySpan = span
			}
		case "query.host_call":
			hostSpan = span
		}
	}
	if !dictionarySpan.SpanContext.SpanID().IsValid() || !hostSpan.SpanContext.SpanID().IsValid() {
		t.Fatalf("actual SDK verification trace missing dictionary/host: %+v", exporter.GetSpans())
	}
	if spanStringAttribute(hostSpan.Attributes, "query.outcome") != "success" || spanStringAttribute(dictionarySpan.Attributes, "query.outcome") != "failure" || spanStringAttribute(dictionarySpan.Attributes, "query.validation_reason") != "decode" || hostSpan.Parent.SpanID() != dictionarySpan.SpanContext.SpanID() {
		t.Fatalf("transport and validation outcomes/parent were conflated: host=%+v dictionary=%+v", hostSpan, dictionarySpan)
	}
}

func spanStringAttribute(attrs []attribute.KeyValue, key string) string {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

func TestMCPQueryUsesSamePinnedProfileAndSerializedResponseAsHTTP(t *testing.T) {
	g1, active := profileQueryFixture(t, "u/p", "G1-retained")
	g2, _ := profileQueryFixture(t, "u/p", "G2-current")
	scope := &profileQueryStore{retained: map[string]*profileQueryStore{"G1-retained": g1}, latest: g2}
	root := &profileQueryRoot{scopes: map[string]*profileQueryStore{"u/p": scope}}
	transport := &profileQueryTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	h := profileQueryHandler(t, cache.New(), root, active)

	httpResponse := profileQueryRequest(h, "u", `{"q":"coffee","mode":"full"}`)
	if httpResponse.Code != http.StatusOK {
		t.Fatalf("HTTP Query status=%d body=%s", httpResponse.Code, httpResponse.Body)
	}
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: "u", projectID: "p", keyID: "fixture-key-id",
	})
	mcpResponse, _, err := h.callMCPQuery(ctx, nil, queryProjectInput{Query: "coffee", Mode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if mcpResponse.IsError || len(mcpResponse.Content) != 1 {
		t.Fatalf("MCP Query result = %+v", mcpResponse)
	}
	text, ok := mcpResponse.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("MCP content type=%T", mcpResponse.Content[0])
	}
	if text.Text != strings.TrimSpace(httpResponse.Body.String()) {
		t.Fatalf("HTTP and MCP QueryResponse differ\nHTTP: %s\nMCP:  %s", httpResponse.Body.String(), text.Text)
	}
	if len(scope.pins) != 2 || scope.pins[0] != "G1-retained" || scope.pins[1] != "G1-retained" {
		t.Fatalf("HTTP/MCP did not each pin the active generation: %v", scope.pins)
	}
	if h.profileRepository.(*profileQueryRepository).reads != 2 {
		t.Fatalf("active profile candidate reads=%d, want 2", h.profileRepository.(*profileQueryRepository).reads)
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
			assertQueryProblem(t, w, http.StatusInternalServerError, "profile_unavailable")
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
		assertQueryProblem(t, w, http.StatusInternalServerError, "profile_unavailable")
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
	if w.Code != 422 {
		t.Fatalf("connected required safe gate: %d %s", w.Code, w.Body)
	}
	assertQueryProblem(t, w, http.StatusUnprocessableEntity, "unsupported_profile_condition")
}
