package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"google.golang.org/api/idtoken"
)

type runtimeFixture struct {
	ctx           context.Context
	repo          *firestoreProfileRepository
	dispatcher    *ProfileDispatcher
	clock         time.Time
	user, project string
	objects       *profileDerivationIntegrationStore
}

func newRuntimeFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	endpoint := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	if endpoint != "127.0.0.1:8585" {
		t.Fatalf("these tests require loopback emulator 127.0.0.1:8585, got %q", endpoint)
	}
	connection, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err != nil {
		t.Fatalf("local Firestore emulator unavailable: %v", err)
	}
	connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := &runtimeFixture{ctx: ctx, user: "owner", project: "runtime", clock: time.Now().UTC().Truncate(time.Second)}
	fs, err := internalfirestore.NewClient(fmt.Sprintf("runtime-%d", time.Now().UnixNano()), f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	_, err = fs.Raw().Collection("projects").Doc(projectDocID(f.user, f.project)).Set(ctx, map[string]interface{}{"user_id": f.user, "project_id": f.project, "status": "ready"})
	if err != nil {
		t.Fatal(err)
	}
	f.objects = &profileDerivationIntegrationStore{objects: map[string][]byte{}}
	root := &profileDerivationIntegrationRoot{RootStore: localfs.New(t.TempDir()), scoped: f.objects}
	h := New(root, fs, nil, nil, nil, nil)
	f.repo = h.profileRepository.(*firestoreProfileRepository)
	f.repo.now = func() time.Time { return f.clock }
	f.dispatcher = &ProfileDispatcher{Handler: h, Provider: profilederive.NewProvider(&deterministicFakeProfileProvider{}), Audience: "https://runtime.invalid/dispatch", ServiceAccount: "scheduler@example.invalid"}
	f.dispatcher.ValidateIdentity = func(_ context.Context, token, audience string) (*idtoken.Payload, error) {
		if token != "synthetic-valid-token" {
			return nil, errors.New("invalid fake identity")
		}
		return &idtoken.Payload{Audience: audience, Claims: map[string]interface{}{"email": f.dispatcher.ServiceAccount, "email_verified": true}}, nil
	}
	return f
}

func (f *runtimeFixture) save(t *testing.T, revision int64, text string) (ProfileState, *firestore.DocumentRef) {
	t.Helper()
	state, err := f.repo.SaveProfile(f.ctx, f.user, f.project, revision, []ProfileRequirement{{ID: "r1", Text: text}})
	if err != nil {
		t.Fatal(err)
	}
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	snapshot, err := profileDerivationIntentRef(stateRef).Get(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := snapshot.Data()["attempt_id"].(string)
	work := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "derive", ID: id}
	return state, f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
}
func (f *runtimeFixture) work(t *testing.T, ref *firestore.DocumentRef) profileruntime.Work {
	t.Helper()
	snapshot, err := ref.Get(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var w profileruntime.Work
	if err = snapshot.DataTo(&w); err != nil {
		t.Fatal(err)
	}
	return w
}
func (f *runtimeFixture) request(t *testing.T, method, token string, wantStatus, wantCount int) {
	t.Helper()
	req := httptest.NewRequest(method, "https://runtime.invalid/dispatch", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	f.dispatcher.ServeHTTP(recorder, req)
	if recorder.Code != wantStatus {
		t.Fatalf("HTTP %d want %d: %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	if wantStatus == 200 {
		var result struct {
			Processed int `json:"processed"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || result.Processed != wantCount {
			t.Fatalf("dispatch result %s err=%v", recorder.Body.String(), err)
		}
	}
}
func (f *runtimeFixture) noActivation(t *testing.T) ProfileState {
	t.Helper()
	state, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if state.Active != nil || state.Candidate != nil || state.Job != nil {
		t.Fatalf("bootstrap unexpectedly activated generation state: %+v", state)
	}
	return state
}

type runtimeRoundTripper func(*http.Request) (*http.Response, error)

func (fn runtimeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestProfileRuntimeSaveDebounceHTTPProvider(t *testing.T) {
	f := newRuntimeFixture(t)
	state, ref := f.save(t, 0, "keep source notes concise")
	work := f.work(t, ref)
	if !work.Pending || work.Revision != state.Revision || work.Attempts != 0 || !work.Due.Equal(f.clock.Add(3*time.Minute)) {
		t.Fatalf("SaveProfile outbox: %+v", work)
	}
	calls := 0
	// The real llm.Client uses an http.Client with nil Transport, so intercept its
	// default transport. No request can reach the live provider. Tests are serial.
	prior := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prior })
	http.DefaultTransport = runtimeRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.deepseek.com" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Fatalf("unexpected provider request %s", r.URL)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "keep source notes concise") {
			t.Fatalf("full requirements absent from provider input: %s", body)
		}
		text := `{"compile_guidance":"Write concise source notes.","guidance_diff":"Added concise guidance.","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"Used for writing."}]}`
		payload, _ := json.Marshal(map[string]interface{}{"model": "synthetic-provider-model", "choices": []interface{}{map[string]interface{}{"message": map[string]string{"content": text}}}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})
	f.dispatcher.Provider = profilederive.NewProvider(llm.NewClient("synthetic-key"))
	f.clock = f.clock.Add(3*time.Minute - time.Nanosecond)
	f.request(t, "POST", "synthetic-valid-token", 200, 0)
	if calls != 0 || f.work(t, ref).Attempts != 0 {
		t.Fatal("provider ran before debounce")
	}
	f.clock = f.clock.Add(time.Nanosecond)
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	if calls != 1 {
		t.Fatalf("provider calls=%d", calls)
	}
	final := f.noActivation(t)
	if final.BootstrapGuidance == nil || final.BootstrapGuidance.Status != profileBootstrapPreviewReady || final.BootstrapGuidance.ModelVersion != "synthetic-provider-model" {
		t.Fatalf("missing preview/provenance: %+v", final)
	}
	if w := f.work(t, ref); w.Pending || w.Status != "complete" || w.Attempts != 1 {
		t.Fatalf("unfinished work: %+v", w)
	}
	f.request(t, "POST", "synthetic-valid-token", 200, 0)
	if calls != 1 {
		t.Fatal("completed work ran twice")
	}
}

func TestProfileRuntimeHTTPAuthentication(t *testing.T) {
	f := newRuntimeFixture(t)
	_, ref := f.save(t, 0, "write concisely")
	f.clock = f.clock.Add(3 * time.Minute)
	f.request(t, "GET", "synthetic-valid-token", 405, 0)
	f.request(t, "POST", "", 401, 0)
	f.request(t, "POST", "invalid", 401, 0)
	for _, bad := range []string{"audience", "email", "verified", "nil"} {
		f.dispatcher.ValidateIdentity = func(context.Context, string, string) (*idtoken.Payload, error) {
			p := &idtoken.Payload{Audience: f.dispatcher.Audience, Claims: map[string]interface{}{"email": f.dispatcher.ServiceAccount, "email_verified": true}}
			switch bad {
			case "audience":
				p.Audience = "other"
			case "email":
				p.Claims["email"] = "intruder"
			case "verified":
				p.Claims["email_verified"] = "true"
			case "nil":
				return nil, nil
			}
			return p, nil
		}
		f.request(t, "POST", "synthetic-valid-token", 401, 0)
	}
	if w := f.work(t, ref); w.Attempts != 0 || !w.Pending {
		t.Fatalf("unauthenticated request claimed work %+v", w)
	}
	if len(f.objects.objects) != 0 {
		t.Fatal("unauthorized request wrote artifacts")
	}
	f.noActivation(t)
}

func TestProfileRuntimeCrashRecoveryFencesOldLease(t *testing.T) {
	f := newRuntimeFixture(t)
	_, ref := f.save(t, 0, "write concisely")
	f.clock = f.clock.Add(3 * time.Minute)
	old, claimed, err := f.dispatcher.claim(f.ctx, f.repo, ref)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	oldctx := context.WithValue(f.ctx, profileRuntimeLeaseKey{}, profileRuntimeLease{ref, old.Token, old.UserID, old.ProjectID})
	if err = prepareProfileRuntimeAttempt(oldctx, f.repo, old); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.ClaimProfileDerivation(oldctx, f.user, f.project, old.Revision, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err = f.dispatcher.claim(f.ctx, f.repo, ref); err != nil || claimed {
		t.Fatalf("live lease reclaimed %v %v", claimed, err)
	}
	f.clock = f.clock.Add(profileruntime.LeaseDuration)
	replacement, claimed, err := f.dispatcher.claim(f.ctx, f.repo, ref)
	if err != nil || !claimed || replacement.Token == old.Token || replacement.Attempts != 2 {
		t.Fatalf("crash recovery %+v %v %v", replacement, claimed, err)
	}
	if err = prepareProfileRuntimeAttempt(oldctx, f.repo, old); !errors.Is(err, errProfileRuntimeLease) {
		t.Fatalf("old execution not fenced: %v", err)
	}
	if err = f.dispatcher.finish(f.ctx, f.repo, ref, old, nil); !errors.Is(err, errProfileRuntimeLease) {
		t.Fatalf("old finish not fenced: %v", err)
	}
	newctx := context.WithValue(f.ctx, profileRuntimeLeaseKey{}, profileRuntimeLease{ref, replacement.Token, replacement.UserID, replacement.ProjectID})
	if err = f.dispatcher.execute(newctx, f.repo, replacement); err != nil {
		t.Fatal(err)
	}
	if err = f.dispatcher.finish(f.ctx, f.repo, ref, replacement, nil); err != nil {
		t.Fatal(err)
	}
	if w := f.work(t, ref); w.Status != "complete" || w.Pending {
		t.Fatalf("recovered work %+v", w)
	}
	f.noActivation(t)
}

func TestProfileRuntimeRetriesExhaustAndStaleSave(t *testing.T) {
	t.Run("max_attempts", func(t *testing.T) {
		f := newRuntimeFixture(t)
		_, ref := f.save(t, 0, "write concisely")
		f.clock = f.clock.Add(3 * time.Minute)
		f.dispatcher.Provider = nil
		for attempt := 1; attempt <= profileruntime.MaxAttempts; attempt++ {
			f.request(t, "POST", "synthetic-valid-token", 200, 1)
			w := f.work(t, ref)
			if w.Attempts != attempt {
				t.Fatalf("attempts %+v", w)
			}
			if attempt < profileruntime.MaxAttempts {
				if !w.Pending || w.Status != "retry_wait" {
					t.Fatalf("retry %+v", w)
				}
				f.clock = w.Due
			} else if w.Pending || w.Status != "exhausted" {
				t.Fatalf("exhaustion %+v", w)
			}
		}
		f.clock = f.clock.Add(time.Hour)
		f.request(t, "POST", "synthetic-valid-token", 200, 0)
		f.noActivation(t)
	})
	t.Run("stale_save", func(t *testing.T) {
		f := newRuntimeFixture(t)
		_, oldref := f.save(t, 0, "old text")
		f.clock = f.clock.Add(time.Minute)
		_, newref := f.save(t, 1, "new text")
		f.clock = f.clock.Add(2 * time.Minute)
		f.request(t, "POST", "synthetic-valid-token", 200, 1)
		if w := f.work(t, oldref); w.Status != "superseded" || w.Pending {
			t.Fatalf("stale work %+v", w)
		}
		if w := f.work(t, newref); w.Attempts != 0 || !w.Pending {
			t.Fatalf("new revision ran before debounce %+v", w)
		}
		state := f.noActivation(t)
		if state.Revision != 2 || state.BootstrapGuidance != nil {
			t.Fatalf("stale save changed state %+v", state)
		}
	})
}

func TestProfileRuntimeFinalExecutionExhaustionIsVisible(t *testing.T) {
	f := newRuntimeFixture(t)
	state, ref := f.save(t, 0, "write concisely")
	// The executor returns before it can persist a failure transition when its
	// generation store is unavailable.
	f.dispatcher.Handler.store = nil
	f.clock = f.clock.Add(profileDebounce)
	for attempt := 1; attempt <= profileruntime.MaxAttempts; attempt++ {
		f.request(t, "POST", "synthetic-valid-token", 200, 1)
		work := f.work(t, ref)
		if work.Attempts != attempt {
			t.Fatalf("attempts = %d, want %d", work.Attempts, attempt)
		}
		if attempt < profileruntime.MaxAttempts {
			if !work.Pending || work.Status != "retry_wait" {
				t.Fatalf("retry work = %+v", work)
			}
			f.clock = work.Due
		}
	}

	work := f.work(t, ref)
	state, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if work.Pending || work.Status != "exhausted" || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationFailed || state.DerivationErrorCode == nil || *state.DerivationErrorCode != "runtime_retry_exhausted" || state.ScheduledFor != nil {
		t.Fatalf("exhausted execution is not visible: work=%+v state=%+v", work, state)
	}
}

func TestProfileRuntimeFinalTagExhaustionIsVisible(t *testing.T) {
	f := newRuntimeFixture(t)
	state, deriveRef := f.save(t, 0, "write concisely")
	f.clock = f.clock.Add(profileDebounce)
	deriveWork := f.work(t, deriveRef)
	if _, err := f.repo.ClaimProfileDerivation(f.ctx, f.user, f.project, state.Revision, deriveWork.ID); err != nil {
		t.Fatal(err)
	}
	digest := profileRequirementsDigest(state.Requirements)
	dictionary := ProfileDerivedRef{
		Revision: strings.Repeat("a", 64), InputDigest: digest, ModelVersion: "model-v1",
		PromptVersion: "dictionary-v1", SchemaVersion: "profile.dictionary.v1",
	}
	guidance := ProfileDerivedRef{
		Revision: strings.Repeat("b", 64), InputDigest: digest, ModelVersion: "model-v1",
		PromptVersion: "guidance-v1", SchemaVersion: "profile.guidance.v1",
	}
	state, err := f.repo.ProfileDerivationSucceeded(f.ctx, f.user, f.project, state.Revision, deriveWork.ID, digest, "generation-1", dictionary, guidance, ProfilePreview{})
	if err != nil {
		t.Fatal(err)
	}
	deriveWork.Pending, deriveWork.Status = false, "complete"
	if _, err := deriveRef.Set(f.ctx, deriveWork); err != nil {
		t.Fatal(err)
	}
	state, err = f.repo.ConfirmProfileCandidate(f.ctx, f.user, f.project, state.Candidate.CandidateID, state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	tagWork := profileruntime.Work{
		UserID: f.user, ProjectID: f.project, Kind: "tag", Revision: state.Revision,
		ID: state.Job.JobID, CandidateID: state.Candidate.CandidateID,
	}
	tagRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(tagWork))
	f.dispatcher.Handler.store = nil
	for attempt := 1; attempt <= profileruntime.MaxAttempts; attempt++ {
		f.request(t, "POST", "synthetic-valid-token", 200, 1)
		work := f.work(t, tagRef)
		if attempt < profileruntime.MaxAttempts {
			if !work.Pending || work.Status != "retry_wait" {
				t.Fatalf("tag retry %d work = %+v", attempt, work)
			}
			f.clock = work.Due
		}
	}
	work := f.work(t, tagRef)
	state, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if work.Pending || work.Status != "exhausted" || state.Job == nil || state.Job.Status != profileJobIncomplete ||
		state.Job.ErrorCode == nil || *state.Job.ErrorCode != "runtime_retry_exhausted" || state.Active != nil {
		t.Fatalf("tag exhaustion is not visible: work=%+v state=%+v", work, state)
	}
}

func TestProfileRuntimeCompileExhaustionIsVisibleAndRetryable(t *testing.T) {
	f := newRuntimeFixture(t)
	requirements := []ProfileRequirement{{ID: "r1", Text: "write concisely"}}
	digest := profileRequirementsDigest(requirements)
	bootstrap := &ProfileBootstrapGuidance{
		Revision: strings.Repeat("c", 64), InputDigest: digest, ProfileRevision: 1,
		Status: profileBootstrapConfirmed, ModelVersion: "model-v1", PromptVersion: "bootstrap-v1",
		SchemaVersion: "profile.bootstrap-guidance.v1", ConfirmedAt: stringPtr(f.clock.Format(time.RFC3339Nano)),
	}
	active := ProfileActive{
		CandidateID: "active-candidate", ContentGeneration: "active-generation",
		DictionaryRevision: strings.Repeat("d", 64), TagSetRevision: strings.Repeat("e", 64),
		QueryRuleRevision: strings.Repeat("f", 64), GuidanceRevision: strings.Repeat("a", 64),
	}
	state := ProfileState{
		ProjectID: f.project, Revision: 1, Requirements: requirements,
		DerivationStatus: stringPtr(profileDerivationReady), BootstrapGuidance: bootstrap, Active: &active,
	}
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	if _, err := stateRef.Set(f.ctx, state); err != nil {
		t.Fatal(err)
	}
	work := profileruntime.Work{
		UserID: f.user, ProjectID: f.project, Kind: "compile", Revision: state.Revision, ID: "generation-failure",
		Due: f.clock.Add(-time.Second), Pending: true, Attempts: profileruntime.MaxAttempts - 1, Status: "retry_wait",
	}
	workRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	if _, err := workRef.Set(f.ctx, work); err != nil {
		t.Fatal(err)
	}
	receipt := profileruntime.CompileReceipt{
		UserID: f.user, ProjectID: f.project, ExecutionID: "synthetic-compile", ProfileRevision: state.Revision,
		RequirementsDigest: digest, ContentGeneration: work.ID, ManifestSHA256: strings.Repeat("a", 64),
		CanonicalConceptsDigest: strings.Repeat("b", 64), ManifestGeneration: 42, CreatedAt: f.clock,
	}
	if _, err := stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(work.ID).Set(f.ctx, receipt); err != nil {
		t.Fatal(err)
	}
	// The store failure occurs after a successful compile receipt but before a
	// Profile reconcile record exists, so the last runtime attempt has no status
	// transition to attach to the user-facing Profile state.
	f.dispatcher.Handler.store = nil
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	work = f.work(t, workRef)
	current, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if work.Pending || work.Status != "exhausted" || current.Revision != state.Revision || current.DerivationStatus == nil ||
		*current.DerivationStatus != profileDerivationFailed || current.DerivationErrorCode == nil || *current.DerivationErrorCode != "runtime_retry_exhausted" ||
		current.CompileRetryGeneration != work.ID || current.Candidate != nil || current.Job != nil || current.Active == nil || *current.Active != active {
		t.Fatalf("compile exhaustion evidence: work=%+v Profile=%+v", work, current)
	}

	retried, err := f.repo.RetryProfileDerivation(f.ctx, f.user, f.project, state.Revision)
	if err != nil || retried.DerivationStatus == nil || *retried.DerivationStatus != profileDerivationPending || retried.DerivationErrorCode != nil || retried.Active == nil || *retried.Active != active {
		t.Fatalf("compile retry was not visible and active-preserving: state=%+v err=%v", retried, err)
	}
	firstRetryWork := f.work(t, workRef)
	if !firstRetryWork.Pending || firstRetryWork.Status != "pending" || firstRetryWork.Attempts != 0 || firstRetryWork.ID != work.ID {
		t.Fatalf("compile retry did not requeue the successful receipt idempotently: %+v", firstRetryWork)
	}
	retriedAgain, err := f.repo.RetryProfileDerivation(f.ctx, f.user, f.project, state.Revision)
	if err != nil || retriedAgain.ScheduledFor == nil || *retriedAgain.ScheduledFor != *retried.ScheduledFor {
		t.Fatalf("repeated compile retry changed its schedule: state=%+v err=%v", retriedAgain, err)
	}
	secondRetryWork := f.work(t, workRef)
	if secondRetryWork.Attempts != firstRetryWork.Attempts || !secondRetryWork.Due.Equal(firstRetryWork.Due) || secondRetryWork.Status != firstRetryWork.Status {
		t.Fatalf("repeated compile retry duplicated queue work: first=%+v second=%+v", firstRetryWork, secondRetryWork)
	}
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	secondRetryWork = f.work(t, workRef)
	current, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || !secondRetryWork.Pending || secondRetryWork.Status != "retry_wait" || secondRetryWork.Attempts != 1 || current.Active == nil || *current.Active != active {
		t.Fatalf("explicit retry did not re-enter bounded runtime work: work=%+v state=%+v err=%v", secondRetryWork, current, err)
	}
	if _, err = f.repo.SaveProfile(f.ctx, f.user, f.project, state.Revision, []ProfileRequirement{{ID: "r1", Text: "new revision"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.RetryProfileDerivation(f.ctx, f.user, f.project, state.Revision); err == nil {
		t.Fatal("stale Profile revision retried an exhausted compile receipt")
	}
	if current, err = f.repo.GetProfile(f.ctx, f.user, f.project); err != nil || current.CompileRetryGeneration != "" || current.Active == nil || *current.Active != active {
		t.Fatalf("new Profile revision retained stale compile retry state: state=%+v err=%v", current, err)
	}
}

func TestProfileRuntimeCompileExhaustionIgnoresStaleReceipt(t *testing.T) {
	f := newRuntimeFixture(t)
	requirements := []ProfileRequirement{{ID: "r1", Text: "write concisely"}}
	digest := profileRequirementsDigest(requirements)
	active := ProfileActive{CandidateID: "active-candidate", ContentGeneration: "active-generation", DictionaryRevision: strings.Repeat("d", 64), TagSetRevision: strings.Repeat("e", 64), QueryRuleRevision: strings.Repeat("f", 64), GuidanceRevision: strings.Repeat("a", 64)}
	state := ProfileState{ProjectID: f.project, Revision: 1, Requirements: requirements, DerivationStatus: stringPtr(profileDerivationReady), Active: &active}
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	if _, err := stateRef.Set(f.ctx, state); err != nil {
		t.Fatal(err)
	}
	work := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "compile", Revision: state.Revision, ID: "stale-generation", Due: f.clock.Add(-time.Second), Pending: true, Attempts: profileruntime.MaxAttempts, Status: "retry_wait"}
	workRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	if _, err := workRef.Set(f.ctx, work); err != nil {
		t.Fatal(err)
	}
	receipt := profileruntime.CompileReceipt{UserID: f.user, ProjectID: f.project, ExecutionID: "stale-compile", ProfileRevision: state.Revision - 1, RequirementsDigest: digest, ContentGeneration: work.ID, ManifestSHA256: strings.Repeat("a", 64), CanonicalConceptsDigest: strings.Repeat("b", 64), ManifestGeneration: 42, CreatedAt: f.clock}
	if _, err := stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(work.ID).Set(f.ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := f.dispatcher.claim(f.ctx, f.repo, workRef); err != nil || claimed {
		t.Fatalf("stale compile work claim = %v, %v", claimed, err)
	}
	work = f.work(t, workRef)
	current, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || work.Pending || work.Status != "exhausted" || current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationReady || current.DerivationErrorCode != nil || current.CompileRetryGeneration != "" || current.Active == nil || *current.Active != active {
		t.Fatalf("stale receipt changed Profile state: work=%+v state=%+v err=%v", work, current, err)
	}
}

func TestProfileRuntimeCompileExhaustionPreservesUnactivatedCandidate(t *testing.T) {
	f := newRuntimeFixture(t)
	requirements := []ProfileRequirement{{ID: "r1", Text: "write concisely"}}
	digest := profileRequirementsDigest(requirements)
	active := ProfileActive{CandidateID: "active-candidate", ContentGeneration: "active-generation", DictionaryRevision: strings.Repeat("d", 64), TagSetRevision: strings.Repeat("e", 64), QueryRuleRevision: strings.Repeat("f", 64), GuidanceRevision: strings.Repeat("a", 64)}
	candidate := ProfileCandidate{CandidateID: "pending-candidate", Source: "manual", BaseRevision: 1, RequirementsDigest: digest, ContentGeneration: "active-generation"}
	state := ProfileState{ProjectID: f.project, Revision: 1, Requirements: requirements, DerivationStatus: stringPtr(profileDerivationReady), Candidate: &candidate, Active: &active}
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	if _, err := stateRef.Set(f.ctx, state); err != nil {
		t.Fatal(err)
	}
	work := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "compile", Revision: state.Revision, ID: "generation-pending-candidate", Due: f.clock, Status: "exhausted"}
	workRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	if _, err := workRef.Set(f.ctx, work); err != nil {
		t.Fatal(err)
	}
	receipt := profileruntime.CompileReceipt{UserID: f.user, ProjectID: f.project, ExecutionID: "synthetic-compile", ProfileRevision: state.Revision, RequirementsDigest: digest, ContentGeneration: work.ID, ManifestSHA256: strings.Repeat("a", 64), CanonicalConceptsDigest: strings.Repeat("b", 64), ManifestGeneration: 42, CreatedAt: f.clock}
	if _, err := stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(work.ID).Set(f.ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.client.RunTransaction(f.ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		return markProfileRuntimeExhausted(ctx, tx, f.repo, work)
	}); err != nil {
		t.Fatal(err)
	}
	current, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationReady || current.DerivationErrorCode != nil || current.CompileRetryGeneration != "" || current.Candidate == nil || current.Candidate.CandidateID != candidate.CandidateID || current.Active == nil || *current.Active != active {
		t.Fatalf("exhaustion replaced a pending candidate or Active: state=%+v err=%v", current, err)
	}

	// Even if a stale exhaustion marker exists, retry must not proceed while an
	// independent manual candidate is still awaiting activation.
	state.DerivationStatus = stringPtr(profileDerivationFailed)
	state.DerivationErrorCode = stringPtr("runtime_retry_exhausted")
	state.CompileRetryGeneration = work.ID
	if _, err := stateRef.Set(f.ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.RetryProfileDerivation(f.ctx, f.user, f.project, state.Revision); err == nil {
		t.Fatal("compile retry proceeded with an unactivated manual candidate")
	}
	current, err = f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationFailed || current.CompileRetryGeneration != work.ID || current.Candidate == nil || current.Candidate.CandidateID != candidate.CandidateID || current.Active == nil || *current.Active != active {
		t.Fatalf("rejected retry changed pending candidate or Active: state=%+v err=%v", current, err)
	}
}

func TestProfileRuntimeSupersededCompileRetryClearsOnlyMatchingState(t *testing.T) {
	f := newRuntimeFixture(t)
	requirements := []ProfileRequirement{{ID: "r1", Text: "write concisely"}}
	active := ProfileActive{CandidateID: "active-candidate", ContentGeneration: "active-generation", DictionaryRevision: strings.Repeat("d", 64), TagSetRevision: strings.Repeat("e", 64), QueryRuleRevision: strings.Repeat("f", 64), GuidanceRevision: strings.Repeat("a", 64)}
	state := ProfileState{ProjectID: f.project, Revision: 1, Requirements: requirements, DerivationStatus: stringPtr(profileDerivationPending), Active: &active, CompileRetryGeneration: "stale-generation"}
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	if _, err := stateRef.Set(f.ctx, state); err != nil {
		t.Fatal(err)
	}
	work := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "compile", Revision: state.Revision, ID: state.CompileRetryGeneration, Due: f.clock, Pending: true, Attempts: 1, Token: "lease", LeaseUntil: f.clock.Add(time.Minute), Status: "running"}
	workRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	if _, err := workRef.Set(f.ctx, work); err != nil {
		t.Fatal(err)
	}
	if err := f.dispatcher.finish(f.ctx, f.repo, workRef, work, errProfileCandidateNotCurrent); err != nil {
		t.Fatal(err)
	}
	work = f.work(t, workRef)
	current, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || work.Pending || work.Status != "superseded" || current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationReady || current.CompileRetryGeneration != "" || current.Active == nil || *current.Active != active {
		t.Fatalf("superseded compile receipt changed or stranded Profile state: work=%+v state=%+v err=%v", work, current, err)
	}
}

func TestProfileRuntimeQueueCannotCrossProjectBoundary(t *testing.T) {
	f := newRuntimeFixture(t)
	_, ref := f.save(t, 0, "write concisely")
	original := f.work(t, ref)
	// Alter a durable row without changing its identity key: reject before provider.
	forged := original
	forged.UserID = "intruder"
	if _, err := ref.Set(f.ctx, forged); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(3 * time.Minute)
	f.request(t, "POST", "synthetic-valid-token", 200, 0)
	if w := f.work(t, ref); w.Pending || w.Status != "invalid" || w.Attempts != 0 {
		t.Fatalf("forged queue identity accepted: %+v", w)
	}
	if len(f.objects.objects) != 0 {
		t.Fatal("forged queue wrote artifacts")
	}
	f.noActivation(t)
	// A correctly keyed row still cannot bypass persisted Project ownership.
	forged.Pending = true
	forged.Status = "pending"
	forgedRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(forged))
	if _, err := forgedRef.Set(f.ctx, forged); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "synthetic-valid-token", 200, 1)
	if w := f.work(t, forgedRef); w.Status == "complete" {
		t.Fatalf("foreign Project work completed: %+v", w)
	}
	if len(f.objects.objects) != 0 {
		t.Fatal("foreign Project wrote artifacts")
	}
	f.noActivation(t)
}

func TestProfileRuntimeAbandonedLastLeaseBecomesVisibleFailure(t *testing.T) {
	f := newRuntimeFixture(t)
	_, ref := f.save(t, 0, "write clearly")
	f.clock = f.clock.Add(3 * time.Minute)
	var last profileruntime.Work
	for attempt := 0; attempt < profileruntime.MaxAttempts; attempt++ {
		w, claimed, err := f.dispatcher.claim(f.ctx, f.repo, ref)
		if err != nil || !claimed {
			t.Fatalf("claim %d: %v %v", attempt, claimed, err)
		}
		ctx := context.WithValue(f.ctx, profileRuntimeLeaseKey{}, profileRuntimeLease{ref, w.Token, w.UserID, w.ProjectID})
		if err := prepareProfileRuntimeAttempt(ctx, f.repo, w); err != nil {
			t.Fatal(err)
		}
		if _, err := f.dispatcher.Handler.ClaimProfileDerivation(ctx, w.UserID, w.ProjectID, w.Revision, w.ID); err != nil {
			t.Fatal(err)
		}
		last = w
		f.clock = f.clock.Add(profileruntime.LeaseDuration + time.Second)
	}
	if _, claimed, err := f.dispatcher.claim(f.ctx, f.repo, ref); err != nil || claimed {
		t.Fatalf("exhausted claim: %v %v", claimed, err)
	}
	state, err := f.repo.GetProfile(f.ctx, last.UserID, last.ProjectID)
	if err != nil || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationFailed || state.DerivationErrorCode == nil || *state.DerivationErrorCode != "runtime_retry_exhausted" || state.Active != nil {
		t.Fatalf("exhausted state: %+v %v", state, err)
	}
}

func TestProfileRuntimeConfirmationReschedulesFailedTagsOnce(t *testing.T) {
	f := newRuntimeFixture(t)
	state, workRef := f.save(t, 0, "write clearly")
	f.clock = f.clock.Add(3 * time.Minute)
	work := f.work(t, workRef)
	if _, err := f.repo.ClaimProfileDerivation(f.ctx, f.user, f.project, state.Revision, work.ID); err != nil {
		t.Fatal(err)
	}
	digest := profileRequirementsDigest(state.Requirements)
	ref := ProfileDerivedRef{Revision: strings.Repeat("a", 64), InputDigest: digest}
	state, err := f.repo.ProfileDerivationSucceeded(f.ctx, f.user, f.project, state.Revision, work.ID, digest, "generation-1", ref, ref, ProfilePreview{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.ClaimProfileJob(f.ctx, f.user, f.project, state.Revision, state.Candidate.CandidateID, state.Job.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.ProfileJobTransition(f.ctx, f.user, f.project, state.Revision, state.Candidate.CandidateID, state.Job.JobID, profileJobIncomplete, 1, "test_provider_failed", "", "", false); err != nil {
		t.Fatal(err)
	}
	tagWork := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "tag", ID: state.Job.JobID}
	tagRef := f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(tagWork))
	if _, err = tagRef.Update(f.ctx, []firestore.Update{{Path: "pending", Value: false}, {Path: "attempts", Value: 3}, {Path: "status", Value: "exhausted"}}); err != nil {
		t.Fatal(err)
	}
	state, err = f.repo.ConfirmProfileCandidate(f.ctx, f.user, f.project, state.Candidate.CandidateID, state.Revision)
	if err != nil || state.Job.Status != profileJobScheduled || state.Active != nil {
		t.Fatalf("confirmation did not queue tags: %+v %v", state, err)
	}
	claimed, ok, err := f.dispatcher.claim(f.ctx, f.repo, tagRef)
	if err != nil || !ok {
		t.Fatalf("tag claim: %v %v", ok, err)
	}
	if _, err = f.repo.ConfirmProfileCandidate(f.ctx, f.user, f.project, state.Candidate.CandidateID, state.Revision); err != nil {
		t.Fatal(err)
	}
	if got := f.work(t, tagRef); got.Token != claimed.Token || got.Attempts != 1 {
		t.Fatalf("duplicate confirmation reset lease: %+v", got)
	}
}
