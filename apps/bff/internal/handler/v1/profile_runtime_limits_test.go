package v1

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profilelimits"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
)

func TestProfileRuntimeDurablySettlesOverLimitDerivationFailures(t *testing.T) {
	const shortRequirement = "Preserve concise source notes."
	active := ProfileActive{
		CandidateID: "retained-candidate", ContentGeneration: "retained-generation",
		DictionaryRevision: strings.Repeat("d", 64), TagSetRevision: strings.Repeat("t", 64),
		QueryRuleRevision: strings.Repeat("q", 64), GuidanceRevision: strings.Repeat("g", 64),
	}
	shortAccounting := []profilederive.RequirementAccounting{{ID: "r1", Disposition: "compile_guidance", Explanation: "Applied to writing."}}
	long := strings.Repeat("🙂", profilelimits.MaxGuidanceScalars+1)
	bootstrapLong, err := json.Marshal(profilederive.BootstrapOutput{
		CompileGuidance: long, GuidanceDiff: "Over limit.", Requirements: shortAccounting,
	})
	if err != nil {
		t.Fatal(err)
	}
	manualLong, err := json.Marshal(profilederive.ManualOutput{
		Tags: []profileartifacts.Tag{}, CompileGuidance: long, DictionaryDiff: "No dictionary change.",
		GuidanceDiff: "Over limit.", Requirements: shortAccounting,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapValid, err := json.Marshal(profilederive.BootstrapOutput{
		CompileGuidance: "Write concise source notes.", GuidanceDiff: "Keep notes concise.", Requirements: shortAccounting,
	})
	if err != nil {
		t.Fatal(err)
	}
	manualValid, err := json.Marshal(profilederive.ManualOutput{
		Tags: []profileartifacts.Tag{}, CompileGuidance: "Write concise source notes.", DictionaryDiff: "No dictionary change.",
		GuidanceDiff: "Keep notes concise.", Requirements: shortAccounting,
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name           string
		kind           string
		wantCode       string
		wantDiagnostic profilederive.FailureDiagnostic
		wantProvider   int
		active         bool
		longResponse   string
		validResponse  string
	}{
		{
			name: "historical oversized requirements require editing",
			kind: "historical", wantCode: profilederive.FailureCodeRequirementsOverLimit,
			wantDiagnostic: profilederive.FailureDiagnostic{Stage: profilederive.FailureStageRequirements, Class: profilederive.FailureClassInputInvalid},
			active:         true, validResponse: string(manualValid),
		},
		{
			name: "bootstrap output preserves explicit retry",
			kind: "bootstrap", wantCode: profilederive.FailureCodeGuidanceTooLong,
			wantDiagnostic: profilederive.FailureDiagnostic{Stage: profilederive.FailureStageBootstrapValidate, Class: profilederive.FailureClassModelAccountingInvalid},
			wantProvider:   1, longResponse: string(bootstrapLong), validResponse: string(bootstrapValid),
		},
		{
			name: "manual output preserves explicit retry",
			kind: "manual", wantCode: profilederive.FailureCodeGuidanceTooLong,
			wantDiagnostic: profilederive.FailureDiagnostic{Stage: profilederive.FailureStageManualValidate, Class: profilederive.FailureClassModelAccountingInvalid},
			wantProvider:   1, active: true, longResponse: string(manualLong), validResponse: string(manualValid),
		},
	}

	for _, test := range cases {
		t.Run(test.kind, func(t *testing.T) {
			f := newRuntimeFixture(t)
			requirements := []ProfileRequirement{{ID: "r1", Text: shortRequirement}}
			if test.kind == "historical" {
				requirements = []ProfileRequirement{{ID: "r1", Text: strings.Repeat("x", profilelimits.MaxRequirementTextScalars+1)}}
			}
			state, workRef := f.save(t, 0, shortRequirement)
			var generationObjects map[string]taggingGCSObject
			if test.kind == "manual" || test.kind == "historical" {
				generationObjects = installRuntimeCurrentGeneration(t, f)
			}
			var retainedActive *ProfileActive
			if test.active {
				copy := active
				retainedActive = &copy
			}
			state = rewriteRuntimeProfileForLegacyAttempt(t, f, state, requirements, retainedActive)
			provider := &deterministicFakeProfileProvider{text: test.longResponse}
			f.dispatcher.Provider = profilederive.NewProvider(provider)
			f.clock = f.clock.Add(profileDebounce)
			beforeArtifacts := runtimeArtifactKeys(f, generationObjects)
			f.request(t, "POST", "synthetic-valid-token", 200, 1)

			work := f.work(t, workRef)
			failed := assertRuntimeDerivationFailure(t, f, work, test.wantCode, test.wantDiagnostic)
			if work.Pending || work.Status != "failed" || work.Attempts != 1 || work.Token != "" || !work.LeaseUntil.IsZero() {
				t.Fatalf("committed over-limit rejection did not settle its actual work row: %+v", work)
			}
			if failed.Candidate != nil || failed.BootstrapGuidance != nil || !sameRuntimeActive(failed.Active, retainedActive) {
				t.Fatalf("over-limit rejection changed candidate/bootstrap/active state: %+v", failed)
			}
			if provider.calls != test.wantProvider {
				t.Fatalf("provider calls=%d want %d", provider.calls, test.wantProvider)
			}
			if after := runtimeArtifactKeys(f, generationObjects); !sameStringSet(beforeArtifacts, after) {
				t.Fatalf("over-limit rejection wrote new artifacts: before=%v after=%v", beforeArtifacts, after)
			}

			// A later sweep beyond every automatic retry/exhaustion point must not
			// clear or replace the committed specific failure.
			f.clock = f.clock.Add(10*profileruntime.LeaseDuration + time.Hour)
			f.request(t, "POST", "synthetic-valid-token", 200, 0)
			work = f.work(t, workRef)
			if work.Pending || work.Status != "failed" || work.Attempts != 1 {
				t.Fatalf("later dispatch retried a committed rejection: %+v", work)
			}
			failed = assertRuntimeDerivationFailure(t, f, work, test.wantCode, test.wantDiagnostic)
			if !sameRuntimeActive(failed.Active, retainedActive) {
				t.Fatalf("later dispatch changed Active: got=%+v want=%+v", failed.Active, retainedActive)
			}

			provider.text = test.validResponse
			var recoveryWorkRef *firestore.DocumentRef
			if test.kind == "historical" {
				// The accepted recovery is an edit/save at the next revision.
				_, recoveryWorkRef = f.save(t, failed.Revision, shortRequirement)
				f.clock = f.clock.Add(profileDebounce)
			} else {
				// Bootstrap/manual generated-output recovery remains the existing
				// explicit same-revision retry action.
				retried, retryErr := f.repo.RetryProfileDerivation(f.ctx, f.user, f.project, failed.Revision)
				if retryErr != nil || retried.Revision != failed.Revision || retried.DerivationStatus == nil || *retried.DerivationStatus != profileDerivationPending {
					t.Fatalf("explicit same-revision output retry failed: state=%+v err=%v", retried, retryErr)
				}
				intent, history := readRuntimeDerivationIntentAndHistory(t, f, work.ID)
				if intent.AttemptID == work.ID || history.ErrorCode == nil || *history.ErrorCode != test.wantCode || history.Status != profileDerivationFailed {
					t.Fatalf("same-revision retry changed the rejected attempt history: current=%+v old=%+v", intent, history)
				}
				currentIntent, err := profileDerivationIntentRef(profileStateRef(f)).Get(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				var current profileDerivationIntent
				if err := currentIntent.DataTo(&current); err != nil {
					t.Fatal(err)
				}
				newWork := profileruntime.Work{UserID: f.user, ProjectID: f.project, Kind: "derive", Revision: failed.Revision, ID: current.AttemptID}
				recoveryWorkRef = f.repo.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(newWork))
			}
			f.request(t, "POST", "synthetic-valid-token", 200, 1)
			recovered := f.work(t, recoveryWorkRef)
			if recovered.Pending || recovered.Status != "complete" || provider.calls != test.wantProvider+1 {
				t.Fatalf("valid recovery did not finish one explicit attempt: work=%+v provider_calls=%d", recovered, provider.calls)
			}
			recoveredState, err := f.repo.GetProfile(f.ctx, f.user, f.project)
			if err != nil {
				t.Fatal(err)
			}
			if !sameRuntimeActive(recoveredState.Active, retainedActive) {
				t.Fatalf("valid recovery changed Active before confirmation: got=%+v want=%+v", recoveredState.Active, retainedActive)
			}
			if test.kind == "historical" {
				if recoveredState.Revision != failed.Revision+1 || recoveredState.Candidate == nil {
					t.Fatalf("edit-to-valid recovery did not derive a candidate at the new revision: %+v", recoveredState)
				}
			} else if test.kind == "bootstrap" {
				if recoveredState.Revision != failed.Revision || recoveredState.BootstrapGuidance == nil || recoveredState.Candidate != nil {
					t.Fatalf("bootstrap retry did not recover same revision: %+v", recoveredState)
				}
			} else if recoveredState.Revision != failed.Revision || recoveredState.Candidate == nil {
				t.Fatalf("manual retry did not recover same revision: %+v", recoveredState)
			}
		})
	}
}

func TestProfileRuntimeOverLimitFailureRejectsStaleLeaseBeforeMutation(t *testing.T) {
	f := newRuntimeFixture(t)
	state, workRef := f.save(t, 0, "Preserve concise source notes.")
	f.clock = f.clock.Add(profileDebounce)
	work, claimed, err := f.dispatcher.claim(f.ctx, f.repo, workRef)
	if err != nil || !claimed {
		t.Fatalf("claim derive work: claimed=%t err=%v", claimed, err)
	}
	runCtx := context.WithValue(f.ctx, profileRuntimeLeaseKey{}, profileRuntimeLease{
		ref: workRef, token: work.Token, userID: work.UserID, projectID: work.ProjectID,
	})
	if err := prepareProfileRuntimeAttempt(runCtx, f.repo, work); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dispatcher.Handler.ClaimProfileDerivation(runCtx, f.user, f.project, work.Revision, work.ID); err != nil {
		t.Fatalf("claim Profile derivation attempt: %v", err)
	}
	staleCtx := context.WithValue(f.ctx, profileRuntimeLeaseKey{}, profileRuntimeLease{
		ref: workRef, token: "expired-stale-token", userID: work.UserID, projectID: work.ProjectID,
	})
	_, err = f.dispatcher.Handler.FailProfileDerivation(staleCtx, f.user, f.project, state.Revision, work.ID,
		profileRequirementsDigest(state.Requirements), profilederive.FailureCodeRequirementsOverLimit,
		profilederive.FailureDiagnostic{Stage: profilederive.FailureStageRequirements, Class: profilederive.FailureClassInputInvalid})
	if !errors.Is(err, errProfileRuntimeLease) {
		t.Fatalf("stale limit rejection error=%v; want lease fence", err)
	}
	storedWork := f.work(t, workRef)
	storedState, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	intent, history := readRuntimeDerivationIntentAndHistory(t, f, work.ID)
	if storedWork.Status != "running" || !storedWork.Pending || storedWork.Token != work.Token || storedState.DerivationErrorCode != nil ||
		storedState.DerivationStatus == nil || *storedState.DerivationStatus != profileDerivationPending ||
		intent.Status != "running" || intent.ErrorCode != nil || history.Status != "running" || history.ErrorCode != nil {
		t.Fatalf("stale failure mutated leased work or Profile state: work=%+v state=%+v intent=%+v history=%+v", storedWork, storedState, intent, history)
	}
}

func TestProfileRuntimeCommittedLimitFailureAtomicallySettlesLeasedWork(t *testing.T) {
	f := newRuntimeFixture(t)
	state, workRef := f.save(t, 0, "Preserve concise source notes.")
	f.clock = f.clock.Add(profileDebounce)
	work, claimed, err := f.dispatcher.claim(f.ctx, f.repo, workRef)
	if err != nil || !claimed {
		t.Fatalf("claim derive work: claimed=%t err=%v", claimed, err)
	}
	runCtx := context.WithValue(f.ctx, profileRuntimeLeaseKey{}, profileRuntimeLease{
		ref: workRef, token: work.Token, userID: work.UserID, projectID: work.ProjectID,
	})
	if err := prepareProfileRuntimeAttempt(runCtx, f.repo, work); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dispatcher.Handler.ClaimProfileDerivation(runCtx, f.user, f.project, work.Revision, work.ID); err != nil {
		t.Fatalf("claim Profile derivation attempt: %v", err)
	}
	if _, err := f.dispatcher.Handler.FailProfileDerivation(runCtx, f.user, f.project, state.Revision, work.ID,
		profileRequirementsDigest(state.Requirements), profilederive.FailureCodeRequirementsOverLimit,
		profilederive.FailureDiagnostic{Stage: profilederive.FailureStageRequirements, Class: profilederive.FailureClassInputInvalid}); err != nil {
		t.Fatalf("commit fenced over-limit rejection: %v", err)
	}
	settled := f.work(t, workRef)
	failed, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	intent, history := readRuntimeDerivationIntentAndHistory(t, f, work.ID)
	if settled.Pending || settled.Status != "failed" || settled.Attempts != work.Attempts || settled.Token != "" || !settled.LeaseUntil.IsZero() ||
		failed.DerivationStatus == nil || *failed.DerivationStatus != profileDerivationFailed || failed.DerivationErrorCode == nil || *failed.DerivationErrorCode != profilederive.FailureCodeRequirementsOverLimit ||
		intent.Status != profileDerivationFailed || intent.ErrorCode == nil || *intent.ErrorCode != profilederive.FailureCodeRequirementsOverLimit ||
		history.Status != profileDerivationFailed || history.ErrorCode == nil || *history.ErrorCode != profilederive.FailureCodeRequirementsOverLimit {
		t.Fatalf("Profile error and its leased work row did not settle together: work=%+v profile=%+v intent=%+v history=%+v", settled, failed, intent, history)
	}
}

func rewriteRuntimeProfileForLegacyAttempt(t *testing.T, f *runtimeFixture, saved ProfileState, requirements []ProfileRequirement, active *ProfileActive) ProfileState {
	t.Helper()
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	err := f.repo.client.RunTransaction(f.ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		state, err := readProfileState(ctx, tx, stateRef, f.project)
		if err != nil {
			return err
		}
		intent, err := readCurrentProfileDerivationIntent(ctx, tx, stateRef)
		if err != nil {
			return err
		}
		historyRef, history, err := readProfileDerivationAttemptHistory(ctx, tx, stateRef, intent.AttemptID)
		if err != nil {
			return err
		}
		state.Requirements = requirements
		state.Active = active
		digest := profileRequirementsDigest(requirements)
		intent.RequirementsDigest = digest
		history.RequirementsDigest = digest
		if err := tx.Set(historyRef, history); err != nil {
			return err
		}
		if err := tx.Set(profileDerivationIntentRef(stateRef), intent); err != nil {
			return err
		}
		return setProfileState(tx, stateRef, state)
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil || state.Revision != saved.Revision {
		t.Fatalf("seed legacy Profile revision: state=%+v err=%v", state, err)
	}
	return state
}

func installRuntimeCurrentGeneration(t *testing.T, f *runtimeFixture) map[string]taggingGCSObject {
	t.Helper()
	reader, manifest := taggingInventoryFixture(t)
	objects := map[string]taggingGCSObject{}
	client, mu, _ := newTaggingGCSClient(t, objects)
	f.dispatcher.Handler.store = client
	prefix := "users/" + f.user + "/projects/" + f.project + "/"
	mu.Lock()
	for _, file := range manifest.Files {
		objects[prefix+manifest.ObjectPath(file)] = taggingGCSObject{data: reader.files[file.Path], gen: file.Generation}
	}
	for path, data := range reader.files {
		if strings.HasPrefix(path, ".lwc/") {
			version := reader.generations[path]
			if version == 0 {
				version = 17
			}
			objects[prefix+path] = taggingGCSObject{data: data, gen: version}
		}
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	archive, err := generation.ArchivedManifestPath(manifest.GenerationID)
	if err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	objects[prefix+archive] = taggingGCSObject{data: manifestData, gen: 22}
	objects[prefix+generation.ManifestPath] = taggingGCSObject{data: manifestData, gen: 21}
	mu.Unlock()
	return objects
}

func assertRuntimeDerivationFailure(t *testing.T, f *runtimeFixture, work profileruntime.Work, code string, diagnostic profilederive.FailureDiagnostic) ProfileState {
	t.Helper()
	state, err := f.repo.GetProfile(f.ctx, f.user, f.project)
	if err != nil {
		t.Fatal(err)
	}
	if state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationFailed || state.DerivationErrorCode == nil || *state.DerivationErrorCode != code ||
		state.DerivationDiagnostic == nil || *state.DerivationDiagnostic != diagnostic || state.ScheduledFor != nil {
		t.Fatalf("specific Profile rejection was not durably visible: %+v", state)
	}
	intent, history := readRuntimeDerivationIntentAndHistory(t, f, work.ID)
	if intent.AttemptID != work.ID || intent.Status != profileDerivationFailed || intent.ErrorCode == nil || *intent.ErrorCode != code ||
		history.Status != profileDerivationFailed || history.ErrorCode == nil || *history.ErrorCode != code {
		t.Fatalf("specific rejection missing from durable intent/history: current=%+v attempt=%+v", intent, history)
	}
	return state
}

func readRuntimeDerivationIntentAndHistory(t *testing.T, f *runtimeFixture, attemptID string) (profileDerivationIntent, profileDerivationIntent) {
	t.Helper()
	stateRef := profileStateRef(f)
	intentSnapshot, err := profileDerivationIntentRef(stateRef).Get(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var intent profileDerivationIntent
	if err := intentSnapshot.DataTo(&intent); err != nil {
		t.Fatal(err)
	}
	historySnapshot, err := profileDerivationIntentRef(stateRef).Collection("attempts").Doc(attemptID).Get(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var history profileDerivationIntent
	if err := historySnapshot.DataTo(&history); err != nil {
		t.Fatal(err)
	}
	return intent, history
}

func profileStateRef(f *runtimeFixture) *firestore.DocumentRef {
	_, stateRef := f.repo.profileRefs(f.user, f.project)
	return stateRef
}

func runtimeArtifactKeys(f *runtimeFixture, generationObjects map[string]taggingGCSObject) map[string]bool {
	keys := make(map[string]bool)
	if generationObjects == nil {
		for key := range f.objects.objects {
			keys[key] = true
		}
		return keys
	}
	for key := range generationObjects {
		keys[key] = true
	}
	return keys
}

func sameStringSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for value := range a {
		if !b[value] {
			return false
		}
	}
	return true
}

func sameRuntimeActive(a, b *ProfileActive) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
