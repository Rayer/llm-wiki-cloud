package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

// This connected fixture exercises the real full-mode HTTP entry point,
// Firestore reservation/work record, ProfileDispatcher, provider adapter,
// and immutable run-bound artifact writer. Provider output and local Pipeline
// invocation are synthetic; no cloud or model service is contacted.
func TestLWC427FullRecompilePreparesFrozenProfileAndContinuesOnce(t *testing.T) {
	f := newRuntimeFixture(t)
	h := f.dispatcher.Handler
	storageObjects := map[string]taggingGCSObject{}
	storageClient, _, _ := newTaggingGCSClient(t, storageObjects)
	if endpoint := os.Getenv("STORAGE_EMULATOR_HOST"); !strings.HasPrefix(endpoint, "http://127.0.0.1:") {
		t.Fatalf("storage emulator must use numeric loopback, got %q", endpoint)
	}
	root := &lwc427ArtifactRoot{RootStore: localfs.New(t.TempDir()), artifacts: storageClient}
	h.store = root
	h.SetPipelineQuotaConfig(5, 1, 1, nil)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	if _, err := root.Scope(f.user, f.project).WriteBytes(f.ctx, []byte("# local source\n\nSynthetic fixture."), "raw/lwc427-source.md"); err != nil {
		t.Fatalf("write disposable raw source: %v", err)
	}

	v1 := []ProfileRequirement{{ID: "r1", Text: "Write concise English summaries."}}
	v2 := []ProfileRequirement{{ID: "r2", Text: "Use Traditional Chinese and retain place names."}}
	v1State, err := f.repo.SaveProfile(f.ctx, f.user, f.project, 0, v1)
	if err != nil {
		t.Fatal(err)
	}
	v2State, err := f.repo.SaveProfile(f.ctx, f.user, f.project, v1State.Revision, v2)
	if err != nil {
		t.Fatal(err)
	}
	v1Digest := profileRequirementsDigest(v1)
	activeGuidance := ProfileDerivedRef{Revision: strings.Repeat("1", 64), InputDigest: v1Digest, ModelVersion: "old-model", PromptVersion: "old-prompt", SchemaVersion: profileartifacts.GuidanceSchema}
	activeCandidate := ProfileCandidate{
		CandidateID: "active-v1", Source: "manual", BaseRevision: v1State.Revision,
		RequirementsDigest: v1Digest, ContentGeneration: "legacy-generation-v1",
		Dictionary: ProfileDerivedRef{Revision: strings.Repeat("2", 64), InputDigest: v1Digest, ModelVersion: "old-model", PromptVersion: "old-prompt", SchemaVersion: profileartifacts.DictionarySchema},
		Guidance:   activeGuidance,
	}
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	if _, err := stateRef.Update(f.ctx, []firestore.Update{{Path: "active", Value: ProfileActive{
		CandidateID: activeCandidate.CandidateID, ContentGeneration: activeCandidate.ContentGeneration,
		DictionaryRevision: activeCandidate.Dictionary.Revision, TagSetRevision: strings.Repeat("3", 64),
		QueryRuleRevision: strings.Repeat("4", 64), GuidanceRevision: activeCandidate.Guidance.Revision,
	}}}); err != nil {
		t.Fatalf("seed existing active V1 pointer: %v", err)
	}
	if _, err := stateRef.Collection("candidates").Doc(activeCandidate.CandidateID).Set(f.ctx, activeCandidate); err != nil {
		t.Fatalf("seed existing active V1 candidate: %v", err)
	}

	// The API's Cloud Run activity lookup is intercepted and returns an empty
	// local fixture response. No request is allowed to leave this transport.
	cloudRequests := 0
	h.SetPipelineJobURL("https://run.googleapis.com/v2/projects/lwc427-local/locations/test/jobs/pipeline:run")
	h.metadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	h.httpClient = &http.Client{Transport: runtimeRoundTripper(func(request *http.Request) (*http.Response, error) {
		cloudRequests++
		if request.URL.Host == "metadata.google.internal" && request.URL.Path == "/computeMetadata/v1/instance/service-accounts/default/token" {
			return lwc427HTTPResponse(http.StatusOK, `{"access_token":"synthetic-loopback-token"}`), nil
		}
		if request.URL.Host == "run.googleapis.com" && strings.HasSuffix(request.URL.Path, "/executions") {
			return lwc427HTTPResponse(http.StatusOK, `{"executions":[]}`), nil
		}
		return nil, fmt.Errorf("unexpected nonlocal request: %s", request.URL)
	})}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+f.project+"/recompile-all", nil)
	c.Request.Header.Set("X-Project-ID", f.project)
	c.Set("userID", f.user)
	c.Params = gin.Params{{Key: "pid", Value: f.project}}
	h.RecompileAll(c)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("RecompileAll response=%d %s", recorder.Code, recorder.Body.String())
	}
	var accepted struct {
		IntentID     string `json:"intent_id"`
		IntentStatus string `json:"intent_status"`
		CompileMode  string `json:"compile_mode"`
	}
	if err := jsonUnmarshalLWC427(recorder.Body.Bytes(), &accepted); err != nil || accepted.IntentID == "" || accepted.IntentStatus != "preparing" || accepted.CompileMode != string(pipelinequota.ModeFull) {
		t.Fatalf("accepted full intent=%+v decode=%v body=%s", accepted, err, recorder.Body.String())
	}
	intentStore := h.firestore
	acceptedIntent, exists, err := intentStore.GetPipelineIntent(f.ctx, f.user, f.project, accepted.IntentID)
	if err != nil || !exists || acceptedIntent.IntentStatus != "preparing" || acceptedIntent.ProfileRevision != v2State.Revision || acceptedIntent.RequirementsDigest != profileRequirementsDigest(v2) || acceptedIntent.ConsumedActiveGuidance != nil {
		t.Fatalf("accepted frozen V2 intent=%+v exists=%t err=%v", acceptedIntent, exists, err)
	}

	provider := &lwc427InterleavingProvider{beforeResponse: func(ctx context.Context) error {
		later, err := f.repo.SaveProfile(ctx, f.user, f.project, v2State.Revision, []ProfileRequirement{{ID: "r3", Text: "Later V3 remains for the next run."}})
		if err != nil {
			return err
		}
		if later.Revision != v2State.Revision+1 {
			return fmt.Errorf("interleaved Profile revision=%d", later.Revision)
		}
		return nil
	}}
	f.dispatcher.Provider = profilederive.NewProvider(provider)
	pipeline := &lwc427LocalPipeline{t: t, handler: h, root: root, userID: f.user, projectID: f.project}
	h.SetLocalPipelineExecutor(pipeline)
	f.clock = time.Now().UTC().Add(time.Minute)
	f.request(t, http.MethodPost, "synthetic-valid-token", http.StatusOK, 1)
	work := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "pipeline", ID: accepted.IntentID}
	workRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	currentIntent, intentExists, intentErr := intentStore.GetPipelineIntent(f.ctx, f.user, f.project, accepted.IntentID)
	if work := f.work(t, workRef); work.Pending || work.Status != "complete" {
		t.Fatalf("automatic Pipeline continuation work=%+v", work)
	}
	if intentErr != nil || !intentExists || currentIntent.IntentStatus != "compiling" {
		t.Fatalf("automatic continuation intent=%+v exists=%t err=%v", currentIntent, intentExists, intentErr)
	}
	if provider.calls != 1 || !strings.Contains(provider.input, "Use Traditional Chinese and retain place names") {
		t.Fatalf("synthetic provider calls=%d input=%q; want frozen V2 requirements", provider.calls, provider.input)
	}
	if pipeline.calls != 1 || pipeline.cleanRebuild != true || pipeline.stage != pipelineStageFull || pipeline.reservationID != accepted.IntentID || pipeline.guidance != "Synthetic run guidance for r2" {
		t.Fatalf("local Pipeline invocation=%+v; want one full run consuming prepared V2 guidance", pipeline)
	}
	if cloudRequests == 0 {
		t.Fatal("local Cloud Run activity lookup did not pass through the bounded synthetic transport")
	}
	latest, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || latest.Revision != v2State.Revision+1 || len(latest.Requirements) != 1 || latest.Requirements[0].ID != "r3" || latest.Active == nil || latest.Active.CandidateID != activeCandidate.CandidateID {
		t.Fatalf("run-bound V2 preparation altered newer Profile state=%+v err=%v", latest, err)
	}
	finalIntent, exists, err := intentStore.GetPipelineIntent(f.ctx, f.user, f.project, accepted.IntentID)
	if err != nil || !exists || finalIntent.IntentStatus != "compiling" || finalIntent.ProfileRevision != v2State.Revision || finalIntent.RequirementsDigest != profileRequirementsDigest(v2) || finalIntent.ConsumedGuidanceKind != "bootstrap" || finalIntent.ConsumedBootstrapGuidance == nil {
		t.Fatalf("continued V2 intent=%+v exists=%t err=%v", finalIntent, exists, err)
	}
	f.request(t, http.MethodPost, "synthetic-valid-token", http.StatusOK, 0)
	if pipeline.calls != 1 || provider.calls != 1 {
		t.Fatalf("duplicate continuation repeated work: pipeline calls=%d provider calls=%d", pipeline.calls, provider.calls)
	}
	if _, err := intentStore.SettleQuotaReservation(f.ctx, accepted.IntentID, "SUCCEEDED"); err != nil {
		t.Fatalf("settle first synthetic local execution: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	secondContext.Request = httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+f.project+"/recompile-all", nil)
	h.pipelineRunForMode(secondContext, f.user, f.project, pipelinequota.ModeFull)
	if secondRecorder.Code != http.StatusAccepted {
		t.Fatalf("subsequent full-mode request=%d %s", secondRecorder.Code, secondRecorder.Body.String())
	}
	var secondAccepted struct {
		IntentID     string `json:"intent_id"`
		IntentStatus string `json:"intent_status"`
	}
	if err := jsonUnmarshalLWC427(secondRecorder.Body.Bytes(), &secondAccepted); err != nil || secondAccepted.IntentID == "" || secondAccepted.IntentStatus != "preparing" {
		t.Fatalf("subsequent full intent=%+v decode=%v body=%s", secondAccepted, err, secondRecorder.Body.String())
	}
	provider.beforeResponse = nil
	f.clock = time.Now().UTC().Add(time.Minute)
	f.request(t, http.MethodPost, "synthetic-valid-token", http.StatusOK, 1)
	if provider.calls != 2 || !strings.Contains(provider.input, "Later V3 remains for the next run") || pipeline.calls != 2 || pipeline.guidance != "Synthetic run guidance for r3" {
		t.Fatalf("subsequent full run calls provider=%d pipeline=%+v input=%q", provider.calls, pipeline, provider.input)
	}
	secondIntent, exists, err := intentStore.GetPipelineIntent(f.ctx, f.user, f.project, secondAccepted.IntentID)
	if err != nil || !exists || secondIntent.IntentStatus != "compiling" || secondIntent.ProfileRevision != latest.Revision || secondIntent.RequirementsDigest != profileRequirementsDigest(latest.Requirements) || secondIntent.ConsumedBootstrapGuidance == nil {
		t.Fatalf("subsequent full run intent=%+v exists=%t err=%v", secondIntent, exists, err)
	}
	if _, err := intentStore.SettleQuotaReservation(f.ctx, secondAccepted.IntentID, "SUCCEEDED"); err != nil {
		t.Fatalf("settle subsequent synthetic local execution: %v", err)
	}
}

type lwc427InterleavingProvider struct {
	calls          int
	input          string
	beforeResponse func(context.Context) error
}

func (p *lwc427InterleavingProvider) ChatWithMetadata(ctx context.Context, _, input string) (string, string, error) {
	p.calls++
	p.input = input
	if p.beforeResponse != nil {
		if err := p.beforeResponse(ctx); err != nil {
			return "", "", err
		}
	}
	var prompt struct {
		Requirements []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"requirements"`
	}
	if err := json.Unmarshal([]byte(input), &prompt); err != nil || len(prompt.Requirements) != 1 {
		if err != nil {
			return "", "", fmt.Errorf("decode synthetic run-bound prompt: %w", err)
		}
		return "", "", errors.New("synthetic run-bound prompt has no single requirement")
	}
	guidance := "Synthetic run guidance for " + prompt.Requirements[0].ID
	output, err := json.Marshal(map[string]interface{}{
		"compile_guidance": guidance, "guidance_diff": "Run-bound synthetic guidance.",
		"requirements": []map[string]string{{"id": prompt.Requirements[0].ID, "disposition": "compile_guidance", "explanation": prompt.Requirements[0].Text}},
	})
	if err != nil {
		return "", "", err
	}
	return string(output), "synthetic-lwc427-model-v1", nil
}

type lwc427LocalPipeline struct {
	t             *testing.T
	handler       *Handler
	root          *lwc427ArtifactRoot
	userID        string
	projectID     string
	calls         int
	stage         string
	cleanRebuild  bool
	reservationID string
	guidance      string
}

func (p *lwc427LocalPipeline) Start(ctx context.Context, userID, projectID, stage string, cleanRebuild bool, reservationID string) (string, error) {
	p.calls++
	p.stage, p.cleanRebuild, p.reservationID = stage, cleanRebuild, reservationID
	if userID != p.userID || projectID != p.projectID {
		return "", errors.New("local Pipeline identity mismatch")
	}
	intent, exists, err := p.handler.firestore.GetPipelineIntent(ctx, userID, projectID, reservationID)
	if err != nil || !exists || intent.IntentStatus != "invoking" || intent.ConsumedBootstrapGuidance == nil || intent.ConsumedBootstrapGuidance.ProfileRevision != intent.ProfileRevision {
		return "", fmt.Errorf("prepared reservation is not the active invoking snapshot: intent=%+v exists=%t err=%v", intent, exists, err)
	}
	objects, ok := p.root.Scope(userID, projectID).(profileartifacts.ObjectStore)
	if !ok {
		return "", errors.New("local artifact store unavailable")
	}
	data, err := objects.ReadFileLimited(ctx, profileartifacts.BootstrapObjectPath(intent.ConsumedBootstrapGuidance.Revision), profileartifacts.MaxArtifactBytes)
	if err != nil {
		return "", err
	}
	envelope, err := profileartifacts.ValidateBootstrapGuidance(data, *intent.ConsumedBootstrapGuidance)
	if err != nil {
		return "", err
	}
	p.guidance = envelope.CompileGuidance
	executionID := "local-execution-" + reservationID
	if err := p.handler.firestore.LinkQuotaReservation(ctx, reservationID, executionID); err != nil {
		return "", err
	}
	return executionID, nil
}

func (*lwc427LocalPipeline) Status(context.Context, string, string, string) (*handler.PipelineExecutionResponse, error) {
	return nil, nil
}

func (*lwc427LocalPipeline) Running(context.Context, string, string) (bool, error) { return false, nil }

type lwc427ArtifactRoot struct {
	storage.RootStore
	artifacts *gcs.Client
}

func (r *lwc427ArtifactRoot) Scope(userID, projectID string) storage.Store {
	return &lwc427ArtifactScope{Store: r.RootStore.Scope(userID, projectID), artifacts: r.artifacts.WithScope(userID, projectID)}
}

type lwc427ArtifactScope struct {
	storage.Store
	artifacts *gcs.Client
}

func (s *lwc427ArtifactScope) ReadFileLimited(ctx context.Context, path string, limit int64) ([]byte, error) {
	return s.artifacts.ReadFileLimited(ctx, path, limit)
}

func (s *lwc427ArtifactScope) StatFile(ctx context.Context, path string) (int64, error) {
	return s.artifacts.StatFile(ctx, path)
}

func (s *lwc427ArtifactScope) WriteFileIfGeneration(ctx context.Context, data []byte, path string, expected int64) (int64, error) {
	return s.artifacts.WriteFileIfGeneration(ctx, data, path, expected)
}

func lwc427HTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func jsonUnmarshalLWC427(data []byte, target interface{}) error {
	return json.Unmarshal(data, target)
}

var _ profileartifacts.ObjectStore = (*lwc427ArtifactScope)(nil)
var _ LocalPipelineExecutor = (*lwc427LocalPipeline)(nil)
