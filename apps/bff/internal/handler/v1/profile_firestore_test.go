package v1

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/api/option"
)

func assertProfileAttemptHistoryStatus(t *testing.T, ctx context.Context, intentRef *firestore.DocumentRef, attemptID, want string) {
	t.Helper()
	got, err := intentRef.Collection("attempts").Doc(attemptID).Get(ctx)
	if err != nil {
		t.Fatalf("read attempt history %q: %v", attemptID, err)
	}
	if got.Data()["status"] != want {
		t.Fatalf("attempt history %q status = %v, want %q", attemptID, got.Data()["status"], want)
	}
}

func TestFirestoreProfileTransactionsAndTransitions(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid Firestore emulator host")
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	projectNumber := fmt.Sprintf("lwc209-%d", time.Now().UnixNano())
	client, err := firestore.NewClient(ctx, projectNumber, option.WithEndpoint(endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer client.Close()

	userID := "owner"
	projectID := fmt.Sprintf("p%d", time.Now().UnixNano())
	projectRef := client.Collection("projects").Doc(projectDocID(userID, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatalf("create Project metadata: %v", err)
	}
	clock := time.Date(2026, 9, 25, 1, 0, 0, 0, time.UTC)
	repo := newFirestoreProfileRepository(client)
	repo.now = func() time.Time { return clock }

	state, err := repo.GetProfile(ctx, userID, projectID)
	if err != nil || state.Revision != 0 || len(state.Requirements) != 0 || state.Requirements == nil || state.Candidate != nil || state.Active != nil || state.Job != nil {
		t.Fatalf("blank Firestore state = %+v, err=%v", state, err)
	}
	emptyProjectID := projectID + "empty"
	emptyProjectRef := client.Collection("projects").Doc(projectDocID(userID, emptyProjectID))
	if _, err := emptyProjectRef.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": emptyProjectID, "status": "ready"}); err != nil {
		t.Fatalf("create empty Project metadata: %v", err)
	}
	emptyState, err := repo.SaveProfile(ctx, userID, emptyProjectID, 0, []ProfileRequirement{})
	if err != nil || emptyState.Revision != 1 || emptyState.DerivationStatus != nil || emptyState.ScheduledFor != nil || emptyState.Job != nil {
		t.Fatalf("empty bootstrap save created work: state=%+v err=%v", emptyState, err)
	}
	emptyState, err = repo.SaveProfile(ctx, userID, emptyProjectID, 1, []ProfileRequirement{{ID: "r1", Text: "later non-empty"}})
	if err != nil || emptyState.Revision != 2 || emptyState.DerivationStatus == nil || *emptyState.DerivationStatus != profileDerivationPending {
		t.Fatalf("non-empty save after blank bootstrap: state=%+v err=%v", emptyState, err)
	}
	wideProjectID := projectID + "wide"
	wideProjectRef := client.Collection("projects").Doc(projectDocID(userID, wideProjectID))
	if _, err := wideProjectRef.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": wideProjectID, "status": "ready"}); err != nil {
		t.Fatalf("create wide-revision Project metadata: %v", err)
	}
	const wideRevision = int64(1<<53) + 17
	wideStateRef := wideProjectRef.Collection("profile").Doc("state")
	if _, err := wideStateRef.Set(ctx, ProfileState{ProjectID: wideProjectID, Revision: wideRevision, Requirements: []ProfileRequirement{}}); err != nil {
		t.Fatalf("seed wide revision: %v", err)
	}
	wideState, err := repo.SaveProfile(ctx, userID, wideProjectID, wideRevision, []ProfileRequirement{})
	if err != nil || wideState.Revision != wideRevision+1 {
		t.Fatalf("wide revision save = %+v err=%v", wideState, err)
	}
	wideStored, err := wideStateRef.Get(ctx)
	if err != nil || wideStored.Data()["revision"] != wideRevision+1 {
		t.Fatalf("Firestore revision precision lost: value=%T(%v) err=%v", wideStored.Data()["revision"], wideStored.Data()["revision"], err)
	}
	requirements := []ProfileRequirement{{ID: "r1", Text: " Keep spacing "}}
	state, err = repo.SaveProfile(ctx, userID, projectID, 0, requirements)
	if err != nil || state.Revision != 1 || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationPending {
		t.Fatalf("saved Firestore state = %+v, err=%v", state, err)
	}
	stored, err := projectRef.Collection("profile").Doc("state").Get(ctx)
	if err != nil {
		t.Fatalf("read Firestore state: %v", err)
	}
	if got := stored.Data()["revision"]; got != int64(1) {
		t.Fatalf("stored revision type/value = %T(%v), want int64(1)", got, got)
	}
	intentRef := profileDerivationIntentRef(projectRef.Collection("profile").Doc("state"))
	intentSnapshot, err := intentRef.Get(ctx)
	if err != nil {
		t.Fatalf("read derivation intent: %v", err)
	}
	attemptID, _ := intentSnapshot.Data()["attempt_id"].(string)
	if attemptID == "" || intentSnapshot.Data()["scheduled_for"] != *state.ScheduledFor {
		t.Fatalf("derivation intent was not atomically scheduled: %+v", intentSnapshot.Data())
	}
	if _, err := repo.GetProfile(ctx, "attacker", projectID); !errors.Is(err, errProfileProjectNotFound) {
		t.Fatalf("cross-user read error = %v", err)
	}
	var conflict *profileRevisionConflict
	if _, err := repo.SaveProfile(ctx, userID, projectID, 0, requirements); !errors.As(err, &conflict) || conflict.Latest != 1 {
		t.Fatalf("stale revision error = %v, want latest=1", err)
	}

	clock = clock.Add(profileDebounce)
	state, err = repo.ClaimProfileDerivation(ctx, userID, projectID, 1, attemptID)
	if err != nil || state.Revision != 1 {
		t.Fatalf("claim attempt 1: state=%+v err=%v", state, err)
	}
	assertProfileAttemptHistoryStatus(t, ctx, intentRef, attemptID, "running")
	digest := profileRequirementsDigest(requirements)
	if _, err := repo.ProfileDerivationFailed(ctx, userID, projectID, 1, attemptID, digest, "temporary_failure"); err != nil {
		t.Fatalf("fail attempt 1: %v", err)
	}
	assertProfileAttemptHistoryStatus(t, ctx, intentRef, attemptID, profileDerivationFailed)
	state, err = repo.RetryProfileDerivation(ctx, userID, projectID, 1)
	if err != nil || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationPending {
		t.Fatalf("retry attempt 1: state=%+v err=%v", state, err)
	}
	secondIntent, err := intentRef.Get(ctx)
	if err != nil {
		t.Fatalf("read retry intent: %v", err)
	}
	secondAttemptID, _ := secondIntent.Data()["attempt_id"].(string)
	firstRetrySchedule, _ := secondIntent.Data()["scheduled_for"].(string)
	if secondAttemptID == "" || secondAttemptID == attemptID {
		t.Fatalf("retry did not allocate a new attempt ID: first=%q second=%q", attemptID, secondAttemptID)
	}
	state, err = repo.RetryProfileDerivation(ctx, userID, projectID, 1)
	if err != nil || state.ScheduledFor == nil || *state.ScheduledFor != firstRetrySchedule {
		t.Fatalf("duplicate derivation retry: state=%+v err=%v", state, err)
	}
	if _, err := repo.ProfileDerivationFailed(ctx, userID, projectID, 1, attemptID, digest, "stale_failure"); !errors.Is(err, errProfileCandidateNotCurrent) {
		t.Fatalf("stale attempt failure error = %v", err)
	}
	clock = clock.Add(time.Second)
	if _, err := repo.ClaimProfileDerivation(ctx, userID, projectID, 1, secondAttemptID); err != nil {
		t.Fatalf("claim attempt 2: %v", err)
	}
	assertProfileAttemptHistoryStatus(t, ctx, intentRef, secondAttemptID, "running")
	if _, err := repo.ProfileDerivationFailed(ctx, userID, projectID, 1, secondAttemptID, digest, "temporary_failure_again"); err != nil {
		t.Fatalf("fail attempt 2: %v", err)
	}
	assertProfileAttemptHistoryStatus(t, ctx, intentRef, secondAttemptID, profileDerivationFailed)
	state, err = repo.RetryProfileDerivation(ctx, userID, projectID, 1)
	if err != nil || state.ScheduledFor == nil {
		t.Fatalf("retry attempt 2: state=%+v err=%v", state, err)
	}
	thirdIntent, err := intentRef.Get(ctx)
	if err != nil {
		t.Fatalf("read second retry intent: %v", err)
	}
	thirdAttemptID, _ := thirdIntent.Data()["attempt_id"].(string)
	thirdSchedule, _ := thirdIntent.Data()["scheduled_for"].(string)
	if thirdAttemptID == "" || thirdAttemptID == secondAttemptID {
		t.Fatalf("second failed attempt did not create a new attempt ID: second=%q third=%q", secondAttemptID, thirdAttemptID)
	}
	state, err = repo.RetryProfileDerivation(ctx, userID, projectID, 1)
	if err != nil || state.ScheduledFor == nil || *state.ScheduledFor != thirdSchedule {
		t.Fatalf("duplicate second retry changed schedule: state=%+v err=%v", state, err)
	}
	derived := ProfileDerivedRef{Revision: "dictionary-1", InputDigest: digest, ModelVersion: "m1", PromptVersion: "p1", SchemaVersion: "s1"}
	guidance := ProfileDerivedRef{Revision: "guidance-1", InputDigest: digest, ModelVersion: "m1", PromptVersion: "p1", SchemaVersion: "s1"}
	if _, err := repo.ProfileDerivationSucceeded(ctx, userID, projectID, 1, secondAttemptID, digest, "stale-generation", derived, guidance, ProfilePreview{}); !errors.Is(err, errProfileCandidateNotCurrent) {
		t.Fatalf("stale attempt success error = %v", err)
	}
	clock = clock.Add(time.Second)
	if _, err := repo.ClaimProfileDerivation(ctx, userID, projectID, 1, thirdAttemptID); err != nil {
		t.Fatalf("claim attempt 3: %v", err)
	}
	assertProfileAttemptHistoryStatus(t, ctx, intentRef, thirdAttemptID, "running")
	state, err = repo.ProfileDerivationSucceeded(ctx, userID, projectID, 1, thirdAttemptID, digest, "g1", derived, guidance, ProfilePreview{DictionaryDiff: "new tags", GuidanceDiff: "new guidance"})
	if err != nil || state.Candidate == nil || state.Job == nil || state.Candidate.Source != "manual" {
		t.Fatalf("complete derivation: state=%+v err=%v", state, err)
	}
	assertProfileAttemptHistoryStatus(t, ctx, intentRef, thirdAttemptID, profileDerivationReady)
	if _, err := repo.ProfileDerivationFailed(ctx, userID, projectID, 1, thirdAttemptID, digest, "stale_after_success"); !errors.Is(err, errProfileCandidateNotCurrent) {
		t.Fatalf("completed attempt was allowed to fail later: %v", err)
	}

	jobID, candidateID := state.Job.JobID, state.Candidate.CandidateID
	if _, err := repo.ClaimProfileJob(ctx, userID, projectID, 1, candidateID, jobID); err != nil {
		t.Fatalf("claim tagging job: %v", err)
	}
	state, err = repo.ProfileJobTransition(ctx, userID, projectID, 1, candidateID, jobID, profileJobReady, 0, "", "tags-1", "rules-1", true)
	if err != nil || state.Active != nil || state.Job == nil || state.Job.Status != profileJobReady {
		t.Fatalf("ready before confirm: state=%+v err=%v", state, err)
	}
	state, err = repo.ConfirmProfileCandidate(ctx, userID, projectID, candidateID, 1)
	if err != nil || state.Active == nil || state.Active.CandidateID != candidateID {
		t.Fatalf("confirm ready candidate: state=%+v err=%v", state, err)
	}
	state, err = repo.ConfirmProfileCandidate(ctx, userID, projectID, candidateID, 1)
	if err != nil || state.Active == nil || state.Job.Status != profileJobReady {
		t.Fatalf("duplicate confirm regressed active/job: state=%+v err=%v", state, err)
	}
	state, err = repo.ProfileJobTransition(ctx, userID, projectID, 1, candidateID, jobID, profileJobReady, 0, "", "tags-1", "rules-1", true)
	if err != nil || state.Active == nil || state.Job.Status != profileJobReady {
		t.Fatalf("duplicate ready transition regressed active/job: state=%+v err=%v", state, err)
	}

	if err := repo.ProfileCompileReconcileRequested(ctx, userID, projectID, "g2", 1, digest); err != nil {
		t.Fatalf("schedule compile reconcile: %v", err)
	}
	compileDictionary := ProfileDerivedRef{Revision: "dictionary-2", InputDigest: digest, ModelVersion: "m2", PromptVersion: "p2", SchemaVersion: "s2"}
	state, err = repo.ProfileCompileCandidateReady(ctx, userID, projectID, "g2", 1, digest, compileDictionary, ProfilePreview{DictionaryDiff: "tag diff", GuidanceDiff: "must be ignored"})
	if err != nil || state.Candidate == nil || state.Candidate.Source != "compile_auto" || state.Candidate.Guidance.Revision != "guidance-1" || state.Candidate.Preview.GuidanceDiff != "" || state.Job == nil {
		t.Fatalf("compile-auto candidate changed guidance: state=%+v err=%v", state, err)
	}
	compileJobID, compileCandidateID := state.Job.JobID, state.Candidate.CandidateID
	if _, err := repo.ClaimProfileJob(ctx, userID, projectID, 1, compileCandidateID, compileJobID); err != nil {
		t.Fatalf("claim compile-auto tagging job: %v", err)
	}
	state, err = repo.ProfileJobTransition(ctx, userID, projectID, 1, compileCandidateID, compileJobID, profileJobReady, 0, "", "tags-2", "rules-2", true)
	if err != nil || state.Active == nil || state.Active.CandidateID != compileCandidateID || state.Active.GuidanceRevision != "guidance-1" {
		t.Fatalf("compile-auto activation: state=%+v err=%v", state, err)
	}

	if err := repo.ProfileCompileReconcileRequested(ctx, userID, projectID, "g3", 1, digest); err != nil {
		t.Fatalf("schedule racing compile reconcile: %v", err)
	}
	nextRequirements := []ProfileRequirement{{ID: "r1", Text: "updated during compile"}}
	state, err = repo.SaveProfile(ctx, userID, projectID, 1, nextRequirements)
	if err != nil || state.Revision != 2 || state.Active == nil || state.Active.CandidateID != compileCandidateID {
		t.Fatalf("save during compile lost active tuple: state=%+v err=%v", state, err)
	}
	nextDigest := profileRequirementsDigest(nextRequirements)
	if _, err := repo.ProfileCompileCandidateReady(ctx, userID, projectID, "g3", 1, digest, compileDictionary, ProfilePreview{}); !errors.As(err, &conflict) || conflict.Latest != 2 {
		t.Fatalf("stale compile result error = %v, want latest revision 2", err)
	}
	reconcileSnapshot, err := profileCompileReconcileRef(projectRef.Collection("profile").Doc("state"), "g3").Get(ctx)
	if err != nil {
		t.Fatalf("read coalesced reconcile intent: %v", err)
	}
	if reconcileSnapshot.Data()["latest_revision"] != int64(2) || reconcileSnapshot.Data()["requirements_digest"] != nextDigest || reconcileSnapshot.Data()["status"] != "scheduled" {
		t.Fatalf("stale compile did not advance reconcile waterline: %+v", reconcileSnapshot.Data())
	}
	nextCompileDictionary := ProfileDerivedRef{Revision: "dictionary-3", InputDigest: nextDigest, ModelVersion: "m3", PromptVersion: "p3", SchemaVersion: "s3"}
	state, err = repo.ProfileCompileCandidateReady(ctx, userID, projectID, "g3", 2, nextDigest, nextCompileDictionary, ProfilePreview{DictionaryDiff: "next tags", GuidanceDiff: "ignored"})
	if err != nil || state.Candidate != nil || state.Job != nil || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationPending || state.Active == nil || state.Active.CandidateID != compileCandidateID {
		t.Fatalf("compile must wait for current manual derivation: state=%+v err=%v", state, err)
	}
	reconcileSnapshot, err = profileCompileReconcileRef(projectRef.Collection("profile").Doc("state"), "g3").Get(ctx)
	if err != nil {
		t.Fatalf("read waiting compile reconcile: %v", err)
	}
	if reconcileSnapshot.Data()["status"] != "waiting_manual" {
		t.Fatalf("compile reconcile should wait for manual revision: data=%v", reconcileSnapshot.Data())
	}
	thirdRequirements := []ProfileRequirement{{ID: "r1", Text: "updated after compile"}}
	state, err = repo.SaveProfile(ctx, userID, projectID, 2, thirdRequirements)
	if err != nil || state.Revision != 3 || state.Active == nil || state.Active.CandidateID != compileCandidateID {
		t.Fatalf("save next manual revision: state=%+v err=%v", state, err)
	}
	thirdDigest := profileRequirementsDigest(thirdRequirements)
	latestIntent, err := intentRef.Get(ctx)
	if err != nil {
		t.Fatalf("read next manual intent: %v", err)
	}
	thirdAttemptID, _ = latestIntent.Data()["attempt_id"].(string)
	clock = clock.Add(profileDebounce)
	if _, err := repo.ClaimProfileDerivation(ctx, userID, projectID, 3, thirdAttemptID); err != nil {
		t.Fatalf("claim next manual derivation: %v", err)
	}
	manualDictionary := ProfileDerivedRef{Revision: "dictionary-manual-3", InputDigest: thirdDigest, ModelVersion: "m3", PromptVersion: "p3", SchemaVersion: "s3"}
	manualGuidance := ProfileDerivedRef{Revision: "guidance-manual-3", InputDigest: thirdDigest, ModelVersion: "m3", PromptVersion: "p3", SchemaVersion: "s3"}
	state, err = repo.ProfileDerivationSucceeded(ctx, userID, projectID, 3, thirdAttemptID, thirdDigest, "g3", manualDictionary, manualGuidance, ProfilePreview{})
	if err != nil || state.Candidate == nil || state.Job == nil {
		t.Fatalf("complete next manual derivation: state=%+v err=%v", state, err)
	}
	manualCandidateID, manualJobID := state.Candidate.CandidateID, state.Job.JobID
	state, err = repo.ConfirmProfileCandidate(ctx, userID, projectID, manualCandidateID, 3)
	if err != nil || state.ConfirmedCandidateID == nil || *state.ConfirmedCandidateID != manualCandidateID || state.Active == nil || state.Active.CandidateID != compileCandidateID {
		t.Fatalf("confirm-before-job-ready changed active prematurely: state=%+v err=%v", state, err)
	}
	if _, err := repo.ClaimProfileJob(ctx, userID, projectID, 3, manualCandidateID, manualJobID); err != nil {
		t.Fatalf("claim confirmed manual job: %v", err)
	}
	state, err = repo.ProfileJobTransition(ctx, userID, projectID, 3, manualCandidateID, manualJobID, profileJobReady, 0, "", "tags-manual-3", "rules-manual-3", true)
	if err != nil || state.Active == nil || state.Active.CandidateID != manualCandidateID {
		t.Fatalf("confirmed manual candidate did not activate after job ready: state=%+v err=%v", state, err)
	}
	if _, err := repo.ProfileCompileCandidateReady(ctx, userID, projectID, "g3", 2, nextDigest, nextCompileDictionary, ProfilePreview{}); !errors.As(err, &conflict) || conflict.Latest != 3 {
		t.Fatalf("waiting compile should advance to settled manual revision: err=%v", err)
	}
	latestCompileDictionary := ProfileDerivedRef{Revision: "dictionary-g3-r3", InputDigest: thirdDigest, ModelVersion: "m3", PromptVersion: "p3", SchemaVersion: "s3"}
	state, err = repo.ProfileCompileCandidateReady(ctx, userID, projectID, "g3", 3, thirdDigest, latestCompileDictionary, ProfilePreview{})
	if err != nil || state.Candidate == nil || state.Candidate.Source != "compile_auto" || state.Candidate.Guidance.Revision != manualGuidance.Revision || state.Active == nil || state.Active.CandidateID != manualCandidateID {
		t.Fatalf("resume compile after manual activation: state=%+v err=%v", state, err)
	}
	state, err = repo.SaveProfile(ctx, userID, projectID, 3, []ProfileRequirement{})
	if err != nil || state.Revision != 4 || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationPending || state.ScheduledFor == nil || state.Candidate != nil || state.Job != nil || state.Active == nil || state.Active.CandidateID != manualCandidateID {
		t.Fatalf("clearing requirements did not schedule neutral work while retaining active: state=%+v err=%v", state, err)
	}
	emptyDigest := profileRequirementsDigest(state.Requirements)
	clearIntentSnapshot, err := intentRef.Get(ctx)
	if err != nil {
		t.Fatalf("read empty-clear derivation intent: %v", err)
	}
	clearAttemptID, _ := clearIntentSnapshot.Data()["attempt_id"].(string)
	if clearAttemptID == "" || clearIntentSnapshot.Data()["requirements_digest"] != emptyDigest || clearIntentSnapshot.Data()["status"] != "scheduled" {
		t.Fatalf("empty digest was not scheduled for neutral derivation: %+v", clearIntentSnapshot.Data())
	}
	clock = clock.Add(profileDebounce)
	if _, err := repo.ClaimProfileDerivation(ctx, userID, projectID, 4, clearAttemptID); err != nil {
		t.Fatalf("claim neutral empty derivation: %v", err)
	}
	neutralDictionary := ProfileDerivedRef{Revision: "neutral-dictionary-empty", InputDigest: emptyDigest, ModelVersion: "fixture", PromptVersion: "fixture", SchemaVersion: "fixture"}
	neutralGuidance := ProfileDerivedRef{Revision: "neutral-guidance-empty", InputDigest: emptyDigest, ModelVersion: "fixture", PromptVersion: "fixture", SchemaVersion: "fixture"}
	state, err = repo.ProfileDerivationSucceeded(ctx, userID, projectID, 4, clearAttemptID, emptyDigest, "neutral-g4", neutralDictionary, neutralGuidance, ProfilePreview{})
	if err != nil || state.Candidate == nil || state.Candidate.Source != "manual" || state.Candidate.RequirementsDigest != emptyDigest || state.Active == nil || state.Active.CandidateID != manualCandidateID {
		t.Fatalf("complete neutral candidate before confirmation: state=%+v err=%v", state, err)
	}
	neutralCandidateID, neutralJobID := state.Candidate.CandidateID, state.Job.JobID
	state, err = repo.ConfirmProfileCandidate(ctx, userID, projectID, neutralCandidateID, 4)
	if err != nil || state.Active == nil || state.Active.CandidateID != manualCandidateID {
		t.Fatalf("confirm neutral candidate changed active before job ready: state=%+v err=%v", state, err)
	}
	clock = clock.Add(time.Second)
	if _, err := repo.ClaimProfileJob(ctx, userID, projectID, 4, neutralCandidateID, neutralJobID); err != nil {
		t.Fatalf("claim neutral candidate job: %v", err)
	}
	state, err = repo.ProfileJobTransition(ctx, userID, projectID, 4, neutralCandidateID, neutralJobID, profileJobReady, 0, "", "neutral-tags", "neutral-rules", true)
	if err != nil || state.Active == nil || state.Active.CandidateID != neutralCandidateID || state.Active.GuidanceRevision != neutralGuidance.Revision {
		t.Fatalf("activate confirmed neutral candidate: state=%+v err=%v", state, err)
	}

	job, err := repo.GetProfileJob(ctx, userID, projectID, compileJobID)
	if err != nil || job.Status != profileJobSuperseded {
		t.Fatalf("exact job GET: job=%+v err=%v", job, err)
	}
	if _, err := repo.GetProfileJob(ctx, "owner", "other-project", compileJobID); !errors.Is(err, errProfileProjectNotFound) {
		t.Fatalf("cross-project job lookup = %v", err)
	}
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": "foreign", "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatalf("corrupt project metadata: %v", err)
	}
	if _, err := repo.GetProfile(ctx, userID, projectID); !errors.Is(err, errProfileProjectNotFound) {
		t.Fatalf("metadata reauthorization error = %v", err)
	}
}

func TestFirestoreProfileBootstrapConfirmationAndSupersededAttempt(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatal("invalid Firestore emulator host")
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := firestore.NewClient(ctx, fmt.Sprintf("lwc352-%d", time.Now().UnixNano()), option.WithEndpoint(endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer client.Close()
	userID, projectID := "owner", fmt.Sprintf("bootstrap-%d", time.Now().UnixNano())
	projectRef := client.Collection("projects").Doc(projectDocID(userID, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatalf("create Project metadata: %v", err)
	}
	clock := time.Date(2026, 9, 25, 1, 0, 0, 0, time.UTC)
	repo := newFirestoreProfileRepository(client)
	repo.now = func() time.Time { return clock }

	requirements := []ProfileRequirement{{ID: "r1", Text: "write compact notes"}, {ID: "r2", Text: "prefer family-friendly search"}}
	state, err := repo.SaveProfile(ctx, userID, projectID, 0, requirements)
	if err != nil || state.Revision != 1 || state.BootstrapGuidance != nil || state.Active != nil {
		t.Fatalf("save bootstrap requirements: state=%+v err=%v", state, err)
	}
	intentRef := profileDerivationIntentRef(projectRef.Collection("profile").Doc("state"))
	intentSnapshot, err := intentRef.Get(ctx)
	if err != nil {
		t.Fatalf("read attempt intent: %v", err)
	}
	attemptID, _ := intentSnapshot.Data()["attempt_id"].(string)
	clock = clock.Add(profileDebounce)
	if _, err := repo.ClaimProfileDerivation(ctx, userID, projectID, 1, attemptID); err != nil {
		t.Fatalf("claim bootstrap attempt: %v", err)
	}
	assertProfileAttemptHistoryStatus(t, ctx, intentRef, attemptID, "running")
	digest := profileRequirementsDigest(requirements)
	guidance := ProfileBootstrapGuidance{
		Revision: strings.Repeat("a", 64), InputDigest: digest, ProfileRevision: 1,
		Status: profileBootstrapPreviewReady, ModelVersion: "fake-http-model", PromptVersion: "profile-bootstrap-v1",
		SchemaVersion: "profile.bootstrap-guidance.v1", Preview: ProfileBootstrapPreview{
			GuidanceDiff: "Adds compact writing guidance.", Requirements: []ProfileRequirementAccounting{
				{ID: "r1", Disposition: "compile_guidance", Explanation: "Applied to writing."},
				{ID: "r2", Disposition: "dictionary_or_query", Explanation: "Reserved for later dictionary/query activation."},
			},
		},
	}
	state, err = repo.ProfileBootstrapDerivationSucceeded(ctx, userID, projectID, 1, attemptID, digest, guidance)
	if err != nil || state.BootstrapGuidance == nil || state.BootstrapGuidance.Status != profileBootstrapPreviewReady || state.Candidate != nil || state.Job != nil || state.Active != nil || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationReady {
		t.Fatalf("bootstrap completion fabricated generation state or lost preview: state=%+v err=%v", state, err)
	}
	assertProfileAttemptHistoryStatus(t, ctx, intentRef, attemptID, profileDerivationReady)
	state, err = repo.ConfirmProfileBootstrapGuidance(ctx, userID, projectID, guidance.Revision, digest, 1)
	if err != nil || state.BootstrapGuidance.Status != profileBootstrapConfirmed || state.BootstrapGuidance.ConfirmedAt == nil {
		t.Fatalf("confirm bootstrap preview: state=%+v err=%v", state, err)
	}
	confirmedAt := *state.BootstrapGuidance.ConfirmedAt
	state, err = repo.ConfirmProfileBootstrapGuidance(ctx, userID, projectID, guidance.Revision, digest, 1)
	if err != nil || state.BootstrapGuidance == nil || state.BootstrapGuidance.ConfirmedAt == nil || *state.BootstrapGuidance.ConfirmedAt != confirmedAt {
		t.Fatalf("repeated bootstrap confirmation was not idempotent: state=%+v err=%v", state, err)
	}
	if _, err := repo.ConfirmProfileBootstrapGuidance(ctx, userID, projectID, strings.Repeat("b", 64), digest, 1); !errors.Is(err, errProfileBootstrapNotCurrent) {
		t.Fatalf("wrong immutable bootstrap revision error = %v", err)
	}

	state, err = repo.SaveProfile(ctx, userID, projectID, 1, []ProfileRequirement{{ID: "r1", Text: "new writing intent"}})
	if err != nil || state.Revision != 2 || state.BootstrapGuidance != nil || state.Active != nil {
		t.Fatalf("new save did not supersede bootstrap ref: state=%+v err=%v", state, err)
	}
	oldAttemptSnapshot, err := intentRef.Collection("attempts").Doc(attemptID).Get(ctx)
	if err != nil || oldAttemptSnapshot.Data()["status"] != profileDerivationReady {
		t.Fatalf("completed attempt history changed unexpectedly: data=%v err=%v", oldAttemptSnapshot.Data(), err)
	}
	var conflict *profileRevisionConflict
	if _, err := repo.ConfirmProfileBootstrapGuidance(ctx, userID, projectID, guidance.Revision, digest, 1); !errors.As(err, &conflict) {
		t.Fatalf("stale confirmation after save error = %v", err)
	}
	if _, err := repo.ProfileBootstrapDerivationSucceeded(ctx, userID, projectID, 1, attemptID, digest, guidance); !errors.As(err, &conflict) {
		t.Fatalf("stale bootstrap completion after save error = %v", err)
	}
	currentIntentSnapshot, err := intentRef.Get(ctx)
	if err != nil {
		t.Fatalf("read current revision 2 intent: %v", err)
	}
	secondAttemptID, _ := currentIntentSnapshot.Data()["attempt_id"].(string)
	state, err = repo.SaveProfile(ctx, userID, projectID, 2, []ProfileRequirement{{ID: "r1", Text: "newer writing intent"}})
	if err != nil || state.Revision != 3 {
		t.Fatalf("save revision 3: state=%+v err=%v", state, err)
	}
	secondAttemptSnapshot, err := intentRef.Collection("attempts").Doc(secondAttemptID).Get(ctx)
	if err != nil || secondAttemptSnapshot.Data()["status"] != profileJobSuperseded {
		t.Fatalf("pending attempt was not persistently superseded: data=%v err=%v", secondAttemptSnapshot.Data(), err)
	}
	if _, err := repo.ClaimProfileDerivation(ctx, userID, projectID, 2, secondAttemptID); !errors.As(err, &conflict) {
		t.Fatalf("stale scheduled attempt claim = %v", err)
	}
}

type profileDerivationIntegrationRoot struct {
	store.RootStore
	scoped *profileDerivationIntegrationStore
}

func (r *profileDerivationIntegrationRoot) Scope(string, string) store.Store { return r.scoped }

type profileDerivationIntegrationStore struct {
	store.Store
	objects map[string][]byte
	checks  int
}

func (s *profileDerivationIntegrationStore) HasCurrentManifest(context.Context) (bool, error) {
	s.checks++
	return false, nil
}

func (s *profileDerivationIntegrationStore) PinCurrentGeneration(context.Context) (*gcs.Client, gcs.GenerationSnapshot, error) {
	return nil, gcs.GenerationSnapshot{}, errors.New("unexpected generation pin")
}

func (s *profileDerivationIntegrationStore) ReadFileLimited(_ context.Context, path string, limit int64) ([]byte, error) {
	data, ok := s.objects[path]
	if !ok {
		return nil, errors.New("object not found")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("object exceeds read limit")
	}
	return append([]byte(nil), data...), nil
}

func (s *profileDerivationIntegrationStore) StatFile(_ context.Context, path string) (int64, error) {
	data, ok := s.objects[path]
	if !ok {
		return 0, errors.New("object not found")
	}
	return int64(len(data)), nil
}

func (s *profileDerivationIntegrationStore) WriteFileIfGeneration(_ context.Context, data []byte, path string, expected int64) (int64, error) {
	if expected != 0 {
		return 0, errors.New("create-only write expected generation zero")
	}
	if _, exists := s.objects[path]; exists {
		return 0, errors.New("object already exists")
	}
	s.objects[path] = append([]byte(nil), data...)
	return 1, nil
}

// deterministicFakeProfileProvider supplies an explicitly synthetic provider
// response so this entrypoint integration never contacts a live model service.
type deterministicFakeProfileProvider struct {
	calls int
	input string
}

func (p *deterministicFakeProfileProvider) ChatWithMetadata(_ context.Context, _, input string) (string, string, error) {
	p.calls++
	p.input = input
	return `{"compile_guidance":"Write concise source notes.","guidance_diff":"Added concise note guidance.","requirements":[{"id":"r1","disposition":"compile_guidance","explanation":"Applied to future writing."}]}`, "fake-profile-model-v1", nil
}

func TestRunProfileDerivationPersistsBootstrapWithDeterministicFakeProvider(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatal("invalid Firestore emulator host")
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	userID, projectID := "owner", fmt.Sprintf("run-derivation-%d", time.Now().UnixNano())
	fs, err := internalfirestore.NewClient(fmt.Sprintf("lwc352-entrypoint-%d", time.Now().UnixNano()), userID, projectID)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer fs.Close()
	projectRef := fs.Raw().Collection("projects").Doc(projectDocID(userID, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatalf("create Project metadata: %v", err)
	}

	scoped := &profileDerivationIntegrationStore{objects: map[string][]byte{}}
	root := &profileDerivationIntegrationRoot{RootStore: localfs.New(t.TempDir()), scoped: scoped}
	h := New(root, fs, nil, nil, nil, nil)
	profileRepo := h.profileRepository.(*firestoreProfileRepository)
	clock := time.Now().UTC().Add(-10 * time.Minute)
	profileRepo.now = func() time.Time { return clock }
	requirements := []ProfileRequirement{{ID: "r1", Text: "keep source notes concise"}}
	state, err := profileRepo.SaveProfile(ctx, userID, projectID, 0, requirements)
	if err != nil || state.Revision != 1 || state.Active != nil || state.Candidate != nil {
		t.Fatalf("save initial Profile: state=%+v err=%v", state, err)
	}
	intent, err := profileDerivationIntentRef(projectRef.Collection("profile").Doc("state")).Get(ctx)
	if err != nil {
		t.Fatalf("read scheduled attempt: %v", err)
	}
	attemptID, _ := intent.Data()["attempt_id"].(string)
	if attemptID == "" {
		t.Fatal("saved Profile has no durable derivation attempt ID")
	}
	clock = clock.Add(profileDebounce)
	provider := &deterministicFakeProfileProvider{}
	result, err := h.RunProfileDerivation(ctx, userID, projectID, state.Revision, attemptID, profilederive.NewProvider(provider))
	if err != nil {
		t.Fatalf("run Profile derivation entrypoint: %v", err)
	}
	if result.Mode != "bootstrap_guidance" || result.BootstrapRef == nil || provider.calls != 1 || scoped.checks != 1 || !strings.Contains(provider.input, "keep source notes concise") {
		t.Fatalf("entrypoint result=%+v provider_calls=%d content_checks=%d prompt=%q", result, provider.calls, scoped.checks, provider.input)
	}
	state, err = profileRepo.GetProfile(ctx, userID, projectID)
	if err != nil || state.BootstrapGuidance == nil || state.BootstrapGuidance.Status != profileBootstrapPreviewReady || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationReady || state.Candidate != nil || state.Job != nil || state.Active != nil {
		t.Fatalf("persisted bootstrap Profile=%+v err=%v", state, err)
	}
	path := profileartifacts.BootstrapObjectPath(result.BootstrapRef.Revision)
	envelope, err := profileartifacts.ValidateBootstrapGuidance(scoped.objects[path], *result.BootstrapRef)
	if err != nil || envelope.CompileGuidance != "Write concise source notes." || envelope.ModelVersion != "fake-profile-model-v1" {
		t.Fatalf("persisted immutable bootstrap=%+v err=%v", envelope, err)
	}
}

func TestFirestorePromotesConfirmedBootstrapAfterFirstCompile(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatal("invalid Firestore emulator host")
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	projectNumber := fmt.Sprintf("lwc352-promote-%d", time.Now().UnixNano())
	client, err := firestore.NewClient(ctx, projectNumber, option.WithEndpoint(endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer client.Close()

	userID, projectID := "owner", fmt.Sprintf("promote-%d", time.Now().UnixNano())
	projectRef := client.Collection("projects").Doc(projectDocID(userID, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatalf("create Project metadata: %v", err)
	}
	clock := time.Now().UTC().Add(-10 * time.Minute)
	repo := newFirestoreProfileRepository(client)
	repo.now = func() time.Time { return clock }
	requirements := []ProfileRequirement{{ID: "r1", Text: "keep notes concise"}}
	state, err := repo.SaveProfile(ctx, userID, projectID, 0, requirements)
	if err != nil {
		t.Fatalf("save Profile: %v", err)
	}
	intentSnapshot, err := profileDerivationIntentRef(projectRef.Collection("profile").Doc("state")).Get(ctx)
	if err != nil {
		t.Fatalf("read derivation attempt: %v", err)
	}
	attemptID, _ := intentSnapshot.Data()["attempt_id"].(string)
	clock = clock.Add(profileDebounce)
	claimed, err := repo.ClaimProfileDerivation(ctx, userID, projectID, state.Revision, attemptID)
	if err != nil || !claimed.derivationClaimAcquired {
		t.Fatalf("claim initial bootstrap: %v", err)
	}
	duplicateClaim, err := repo.ClaimProfileDerivation(ctx, userID, projectID, state.Revision, attemptID)
	if err != nil || duplicateClaim.derivationClaimAcquired {
		t.Fatalf("duplicate running attempt claim = acquired:%t err:%v", duplicateClaim.derivationClaimAcquired, err)
	}
	digest := profileRequirementsDigest(requirements)
	bootstrap := ProfileBootstrapGuidance{
		Revision: strings.Repeat("a", 64), InputDigest: digest, ProfileRevision: state.Revision,
		Status: profileBootstrapPreviewReady, ModelVersion: "fake-bootstrap-model", PromptVersion: "bootstrap-v1",
		SchemaVersion: profileartifacts.BootstrapGuidanceSchema, Preview: ProfileBootstrapPreview{Requirements: []ProfileRequirementAccounting{
			{ID: "r1", Disposition: "compile_guidance", Explanation: "Used in first compile."},
		}},
	}
	if _, err := repo.ProfileBootstrapDerivationSucceeded(ctx, userID, projectID, state.Revision, attemptID, digest, bootstrap); err != nil {
		t.Fatalf("persist bootstrap preview: %v", err)
	}
	bootstrapRef := profileartifacts.BootstrapGuidanceRef{
		Revision: bootstrap.Revision, ProfileRevision: bootstrap.ProfileRevision, InputDigest: bootstrap.InputDigest,
		ModelVersion: bootstrap.ModelVersion, PromptVersion: bootstrap.PromptVersion, SchemaVersion: bootstrap.SchemaVersion,
	}
	if err := repo.ProfileCompileReconcileRequested(ctx, userID, projectID, "G1", state.Revision, digest); err != nil {
		t.Fatalf("request first-generation reconcile: %v", err)
	}
	dictionary := ProfileDerivedRef{Revision: strings.Repeat("c", 64), InputDigest: digest, ModelVersion: "fake-dictionary-model", PromptVersion: "compile-auto-v1", SchemaVersion: "profile.dictionary.v1"}
	guidance := ProfileDerivedRef{Revision: strings.Repeat("d", 64), InputDigest: digest, ModelVersion: bootstrap.ModelVersion, PromptVersion: bootstrap.PromptVersion, SchemaVersion: "profile.guidance.v1"}
	preview := ProfilePreview{DictionaryDiff: "Created G1 dictionary", GuidanceDiff: "must be cleared", Requirements: []ProfileRequirementAccounting{
		{ID: "r1", Disposition: "both", Explanation: "Writing guidance is retained and dictionary rules are staged."},
	}}
	if _, err := repo.ProfileBootstrapCompileCandidateReady(ctx, userID, projectID, "G1", state.Revision, digest, &bootstrapRef, dictionary, guidance, preview); !errors.Is(err, errProfileBootstrapNotCurrent) {
		t.Fatalf("unconfirmed bootstrap compile promotion error = %v", err)
	}
	unchanged, err := repo.GetProfile(ctx, userID, projectID)
	if err != nil || unchanged.Candidate != nil || unchanged.Active != nil || unchanged.BootstrapGuidance == nil || unchanged.BootstrapGuidance.Status != profileBootstrapPreviewReady {
		t.Fatalf("unconfirmed promotion mutated Profile state: state=%+v err=%v", unchanged, err)
	}
	if _, err := repo.ConfirmProfileBootstrapGuidance(ctx, userID, projectID, bootstrap.Revision, digest, state.Revision); err != nil {
		t.Fatalf("confirm bootstrap guidance: %v", err)
	}
	if _, err := repo.ProfileBootstrapCompileCandidateReady(ctx, userID, projectID, "G1", state.Revision, digest, nil, dictionary, guidance, preview); !errors.Is(err, errProfileBootstrapNotCurrent) {
		t.Fatalf("missing consumed bootstrap pin error = %v", err)
	}
	mismatchedRef := bootstrapRef
	mismatchedRef.Revision = strings.Repeat("b", 64)
	if _, err := repo.ProfileBootstrapCompileCandidateReady(ctx, userID, projectID, "G1", state.Revision, digest, &mismatchedRef, dictionary, guidance, preview); !errors.Is(err, errProfileBootstrapNotCurrent) {
		t.Fatalf("mismatched consumed bootstrap pin error = %v", err)
	}
	unchanged, err = repo.GetProfile(ctx, userID, projectID)
	if err != nil || unchanged.Candidate != nil || unchanged.Active != nil || unchanged.BootstrapGuidance == nil || unchanged.BootstrapGuidance.Status != profileBootstrapConfirmed {
		t.Fatalf("rejected consumed pin mutated Profile state: state=%+v err=%v", unchanged, err)
	}
	state, err = repo.ProfileBootstrapCompileCandidateReady(ctx, userID, projectID, "G1", state.Revision, digest, &bootstrapRef, dictionary, guidance, preview)
	if err != nil || state.Candidate == nil || state.Candidate.Source != "compile_auto" || state.Candidate.ContentGeneration != "G1" ||
		state.Candidate.Guidance.Revision != guidance.Revision || state.Candidate.Preview.GuidanceDiff != "" || state.Job == nil || state.Active != nil {
		t.Fatalf("first generation candidate=%+v job=%+v active=%+v err=%v", state.Candidate, state.Job, state.Active, err)
	}
	candidateID := state.Candidate.CandidateID
	state, err = repo.ProfileBootstrapCompileCandidateReady(ctx, userID, projectID, "G1", state.Revision, digest, &bootstrapRef, dictionary, guidance, preview)
	if err != nil || state.Candidate == nil || state.Candidate.CandidateID != candidateID {
		t.Fatalf("repeated first-generation reconcile was not idempotent: candidate=%+v err=%v", state.Candidate, err)
	}
	state, err = repo.SaveProfile(ctx, userID, projectID, state.Revision, []ProfileRequirement{{ID: "r1", Text: "changed while compile was running"}})
	if err != nil || state.Revision != 2 || state.Candidate != nil || state.BootstrapGuidance != nil || state.Active != nil {
		t.Fatalf("edit after compile pin: state=%+v err=%v", state, err)
	}
	var conflict *profileRevisionConflict
	if _, err := repo.ProfileBootstrapCompileCandidateReady(ctx, userID, projectID, "G1", 1, digest, &bootstrapRef, dictionary, guidance, preview); !errors.As(err, &conflict) {
		t.Fatalf("edit-during-compile promotion error = %v", err)
	}
	unchanged, err = repo.GetProfile(ctx, userID, projectID)
	if err != nil || unchanged.Revision != 2 || unchanged.Candidate != nil || unchanged.Active != nil {
		t.Fatalf("stale promotion mutated Profile pointers: state=%+v err=%v", unchanged, err)
	}
}

func TestFirestoreProfileCompileWaitsForManualWork(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid Firestore emulator host")
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	projectNumber := fmt.Sprintf("lwc209-compile-wait-%d", time.Now().UnixNano())
	client, err := firestore.NewClient(ctx, projectNumber, option.WithEndpoint(endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer client.Close()

	userID := "owner"
	projectID := fmt.Sprintf("compile-wait-%d", time.Now().UnixNano())
	projectRef := client.Collection("projects").Doc(projectDocID(userID, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatalf("create Project metadata: %v", err)
	}
	clock := time.Date(2026, 9, 25, 1, 0, 0, 0, time.UTC)
	repo := newFirestoreProfileRepository(client)
	repo.now = func() time.Time { return clock }
	stateRef := projectRef.Collection("profile").Doc("state")

	readReconcileStatus := func(generation string) string {
		t.Helper()
		snapshot, err := profileCompileReconcileRef(stateRef, generation).Get(ctx)
		if err != nil {
			t.Fatalf("read reconcile %q: %v", generation, err)
		}
		status, _ := snapshot.Data()["status"].(string)
		return status
	}
	runCompile := func(generation string, revision int64, digest, dictionaryRevision string) (ProfileState, error) {
		t.Helper()
		if err := repo.ProfileCompileReconcileRequested(ctx, userID, projectID, generation, revision, digest); err != nil {
			return ProfileState{}, err
		}
		dictionary := ProfileDerivedRef{Revision: dictionaryRevision, InputDigest: digest, ModelVersion: "m", PromptVersion: "p", SchemaVersion: "s"}
		return repo.ProfileCompileCandidateReady(ctx, userID, projectID, generation, revision, digest, dictionary, ProfilePreview{DictionaryDiff: "compile tags", GuidanceDiff: "must be ignored"})
	}

	state, err := repo.SaveProfile(ctx, userID, projectID, 0, []ProfileRequirement{{ID: "r1", Text: "manual requirement"}})
	if err != nil || state.Revision != 1 || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationPending {
		t.Fatalf("start manual derivation: state=%+v err=%v", state, err)
	}
	digest := profileRequirementsDigest(state.Requirements)
	compileDictionary := "compile-pending"
	state, err = runCompile("g-pending", 1, digest, compileDictionary)
	if err != nil || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationPending || state.Candidate != nil || state.Job != nil {
		t.Fatalf("compile must preserve no-active pending manual derivation: state=%+v err=%v", state, err)
	}
	if got := readReconcileStatus("g-pending"); got != "waiting_manual" {
		t.Fatalf("pending manual reconcile status = %q, want waiting_manual", got)
	}

	intentRef := profileDerivationIntentRef(stateRef)
	intentSnapshot, err := intentRef.Get(ctx)
	if err != nil {
		t.Fatalf("read pending derivation intent: %v", err)
	}
	attemptID, _ := intentSnapshot.Data()["attempt_id"].(string)
	clock = clock.Add(profileDebounce)
	if _, err := repo.ClaimProfileDerivation(ctx, userID, projectID, 1, attemptID); err != nil {
		t.Fatalf("claim manual derivation: %v", err)
	}
	manualDictionary := ProfileDerivedRef{Revision: "dictionary-manual-1", InputDigest: digest, ModelVersion: "m", PromptVersion: "p", SchemaVersion: "s"}
	manualGuidance := ProfileDerivedRef{Revision: "guidance-manual-1", InputDigest: digest, ModelVersion: "m", PromptVersion: "p", SchemaVersion: "s"}
	state, err = repo.ProfileDerivationSucceeded(ctx, userID, projectID, 1, attemptID, digest, "manual-g1", manualDictionary, manualGuidance, ProfilePreview{})
	if err != nil || state.Candidate == nil || state.Candidate.Source != "manual" || state.ConfirmedCandidateID != nil || state.Active != nil {
		t.Fatalf("complete unconfirmed manual candidate: state=%+v err=%v", state, err)
	}
	manualCandidateID, manualJobID := state.Candidate.CandidateID, state.Job.JobID
	state, err = runCompile("g-unconfirmed", 1, digest, "compile-unconfirmed")
	if err != nil || state.Candidate == nil || state.Candidate.CandidateID != manualCandidateID || state.Candidate.Source != "manual" || state.Job == nil || state.Job.JobID != manualJobID || state.Active != nil {
		t.Fatalf("compile must preserve unconfirmed manual candidate: state=%+v err=%v", state, err)
	}
	if got := readReconcileStatus("g-unconfirmed"); got != "waiting_manual" {
		t.Fatalf("unconfirmed manual reconcile status = %q, want waiting_manual", got)
	}

	state, err = repo.ConfirmProfileCandidate(ctx, userID, projectID, manualCandidateID, 1)
	if err != nil || state.ConfirmedCandidateID == nil || *state.ConfirmedCandidateID != manualCandidateID || state.Active != nil {
		t.Fatalf("confirm manual candidate before tagging: state=%+v err=%v", state, err)
	}
	state, err = runCompile("g-resume-first", 1, digest, "compile-resume-first")
	if err != nil || state.Candidate == nil || state.Candidate.CandidateID != manualCandidateID || state.Active != nil {
		t.Fatalf("compile must preserve confirmed but not-yet-active manual candidate: state=%+v err=%v", state, err)
	}
	if got := readReconcileStatus("g-resume-first"); got != "waiting_manual" {
		t.Fatalf("confirmed-not-active reconcile status = %q, want waiting_manual", got)
	}
	if _, err := repo.ClaimProfileJob(ctx, userID, projectID, 1, manualCandidateID, manualJobID); err != nil {
		t.Fatalf("claim manual tagging job: %v", err)
	}
	state, err = repo.ProfileJobTransition(ctx, userID, projectID, 1, manualCandidateID, manualJobID, profileJobReady, 0, "", "tags-manual-1", "rules-manual-1", true)
	if err != nil || state.Active == nil || state.Active.CandidateID != manualCandidateID {
		t.Fatalf("activate confirmed manual candidate: state=%+v err=%v", state, err)
	}
	state, err = repo.ProfileCompileCandidateReady(ctx, userID, projectID, "g-resume-first", 1, digest,
		ProfileDerivedRef{Revision: "compile-resumed-guidance-check", InputDigest: digest, ModelVersion: "m", PromptVersion: "p", SchemaVersion: "s"}, ProfilePreview{})
	if err != nil || state.Candidate == nil || state.Candidate.Source != "compile_auto" || state.Candidate.Guidance.Revision != manualGuidance.Revision {
		t.Fatalf("resume compile from active manual guidance: state=%+v err=%v", state, err)
	}

	updatedRequirements := []ProfileRequirement{{ID: "r1", Text: "new manual edit"}}
	state, err = repo.SaveProfile(ctx, userID, projectID, 1, updatedRequirements)
	if err != nil || state.Revision != 2 || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationPending || state.Candidate != nil || state.Job != nil || state.Active == nil || state.Active.CandidateID != manualCandidateID {
		t.Fatalf("start manual edit while retaining active tuple: state=%+v err=%v", state, err)
	}
	updatedDigest := profileRequirementsDigest(updatedRequirements)
	state, err = runCompile("g-active-manual-edit", 2, updatedDigest, "compile-active-manual-edit")
	if err != nil || state.Revision != 2 || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationPending || state.Candidate != nil || state.Job != nil || state.Active == nil || state.Active.CandidateID != manualCandidateID {
		t.Fatalf("compile must preserve pending manual edit over active tuple: state=%+v err=%v", state, err)
	}
	if got := readReconcileStatus("g-active-manual-edit"); got != "waiting_manual" {
		t.Fatalf("active manual edit reconcile status = %q, want waiting_manual", got)
	}

	intentSnapshot, err = intentRef.Get(ctx)
	if err != nil {
		t.Fatalf("read second manual derivation intent: %v", err)
	}
	secondAttemptID, _ := intentSnapshot.Data()["attempt_id"].(string)
	clock = clock.Add(profileDebounce)
	if _, err := repo.ClaimProfileDerivation(ctx, userID, projectID, 2, secondAttemptID); err != nil {
		t.Fatalf("claim second manual derivation: %v", err)
	}
	secondManualDictionary := ProfileDerivedRef{Revision: "dictionary-manual-2", InputDigest: updatedDigest, ModelVersion: "m2", PromptVersion: "p2", SchemaVersion: "s2"}
	secondManualGuidance := ProfileDerivedRef{Revision: "guidance-manual-2", InputDigest: updatedDigest, ModelVersion: "m2", PromptVersion: "p2", SchemaVersion: "s2"}
	state, err = repo.ProfileDerivationSucceeded(ctx, userID, projectID, 2, secondAttemptID, updatedDigest, "manual-g2", secondManualDictionary, secondManualGuidance, ProfilePreview{})
	if err != nil || state.Candidate == nil || state.Candidate.Source != "manual" {
		t.Fatalf("complete second manual derivation: state=%+v err=%v", state, err)
	}
	secondManualCandidateID, secondManualJobID := state.Candidate.CandidateID, state.Job.JobID
	if _, err := repo.ConfirmProfileCandidate(ctx, userID, projectID, secondManualCandidateID, 2); err != nil {
		t.Fatalf("confirm second manual candidate: %v", err)
	}
	if _, err := repo.ClaimProfileJob(ctx, userID, projectID, 2, secondManualCandidateID, secondManualJobID); err != nil {
		t.Fatalf("claim second manual tagging job: %v", err)
	}
	state, err = repo.ProfileJobTransition(ctx, userID, projectID, 2, secondManualCandidateID, secondManualJobID, profileJobReady, 0, "", "tags-manual-2", "rules-manual-2", true)
	if err != nil || state.Active == nil || state.Active.CandidateID != secondManualCandidateID {
		t.Fatalf("activate second manual candidate: state=%+v err=%v", state, err)
	}
	state, err = repo.ProfileCompileCandidateReady(ctx, userID, projectID, "g-active-manual-edit", 2, updatedDigest,
		ProfileDerivedRef{Revision: "compile-resumed-after-edit", InputDigest: updatedDigest, ModelVersion: "m2", PromptVersion: "p2", SchemaVersion: "s2"}, ProfilePreview{})
	if err != nil || state.Candidate == nil || state.Candidate.Source != "compile_auto" || state.Candidate.Guidance.Revision != secondManualGuidance.Revision {
		t.Fatalf("resume compile after manual edit settles: state=%+v err=%v", state, err)
	}
}
