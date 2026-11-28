package nudge

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestResolveTargetUsesClosedFirstPartyRoutes(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		want   string
		bad    bool
	}{
		{"pipeline exact run", Target{Kind: "pipeline-run", ProjectID: "project-a", ExecutionID: "run-a"}, "/status?project_id=project-a&execution_id=run-a", false},
		{"description exact project", Target{Kind: "project-description", ProjectID: "project-a"}, "/profile?project_id=project-a#project-description", false},
		{"Project AI service settings", Target{Kind: "project-ai-service", ProjectID: "project-a"}, "/profile?project_id=project-a#project-ai-service", false},
		{"current notes", Target{Kind: "release-notes", Mode: "current"}, "/release-notes?version=current", false},
		{"published notes", Target{Kind: "release-notes", Mode: "published-version", Version: "2026.11.4"}, "/release-notes?version=2026.11.4", false},
		{"extra pipeline mode", Target{Kind: "pipeline-run", ProjectID: "project-a", ExecutionID: "run-a", Mode: "full"}, "", true},
		{"arbitrary target", Target{Kind: "external", Version: "https://example.invalid"}, "", true},
		{"current notes with supplied version", Target{Kind: "release-notes", Mode: "current", Version: "2026.11.4"}, "", true},
		{"invalid published version", Target{Kind: "release-notes", Mode: "published-version", Version: "../current"}, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveTarget(test.target)
			if test.bad {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("ResolveTarget error=%v, want invalid input", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("ResolveTarget=%q err=%v, want %q", got, err, test.want)
			}
		})
	}
}

func TestBYOKWitnessOrdersSettingsRevisionsAndLateOperations(t *testing.T) {
	started := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	failure := ProducerWitness{Kind: "byok-failure", ProjectID: "project-a", ProfileID: "profile-a", ProfileRevision: 1,
		BindingRevision: 2, FailureReason: "authentication", Trigger: "query", AttemptStartedAt: started}
	newSettings := failure
	newSettings.Kind, newSettings.ProfileRevision = "byok-settings", 2
	newSettings.FailureReason, newSettings.Trigger, newSettings.AttemptStartedAt = "", "", time.Time{}
	if CompareWitness(CategoryBYOKActionable, failure, newSettings) >= 0 {
		t.Fatal("old profile revision beat settings update")
	}
	newFailure := failure
	newFailure.ProfileRevision, newFailure.AttemptStartedAt = 2, started.Add(time.Second)
	if CompareWitness(CategoryBYOKActionable, newFailure, newSettings) <= 0 {
		t.Fatal("actionable failure after settings update could not recreate the Nudge")
	}
	lateOldBinding := newFailure
	lateOldBinding.ProfileRevision, lateOldBinding.BindingRevision = 3, 1
	currentBinding := newFailure
	currentBinding.ProfileRevision, currentBinding.BindingRevision = 3, 2
	if CompareWitness(CategoryBYOKActionable, lateOldBinding, currentBinding) >= 0 {
		t.Fatal("late operation from an old Project binding beat the current binding")
	}
	earlierRun, laterRun := failure, failure
	earlierRun.Trigger, earlierRun.ExecutionID, earlierRun.ProjectPipelineSequence = "pipeline", "run-1", 3
	laterRun.Trigger, laterRun.ExecutionID, laterRun.ProjectPipelineSequence = "pipeline", "run-2", 4
	if CompareWitness(CategoryBYOKActionable, earlierRun, laterRun) >= 0 || CompareWitness(CategoryBYOKActionable, laterRun, earlierRun) <= 0 {
		t.Fatal("same-time pipeline BYOK failures were not ordered by Project sequence")
	}
	otherRun := earlierRun
	otherRun.ExecutionID = "run-0"
	if CompareWitness(CategoryBYOKActionable, earlierRun, otherRun) <= 0 || CompareWitness(CategoryBYOKActionable, otherRun, earlierRun) >= 0 {
		t.Fatal("same-time, same-sequence BYOK failures did not have a deterministic event tie break")
	}
}

func TestBYOKSettingsClearAcceptsOnlySettingsUpdateProof(t *testing.T) {
	clear := ClearInput{Category: CategoryBYOKActionable, SourceKey: "byok:project-a", ExpectedSourceRevision: 1,
		ProducerWitness: ProducerWitness{Kind: "byok-settings", ProjectID: "project-a", ProfileID: "profile-a", ProfileRevision: 2, BindingRevision: 1},
		Proof:           "confirmed-settings-update"}
	if err := ValidateClearInput(clear); err != nil {
		t.Fatalf("valid settings update clear rejected: %v", err)
	}
	clear.Proof = "confirmed-recovery"
	if !errors.Is(ValidateClearInput(clear), ErrInvalidInput) {
		t.Fatal("Pipeline recovery proof cleared a BYOK reminder")
	}
	pipelineClear := ClearInput{Category: CategoryPipelineFailure, SourceKey: "pipeline-fail:project-a", Proof: "confirmed-settings-update"}
	if !errors.Is(ValidateClearInput(pipelineClear), ErrInvalidInput) {
		t.Fatal("BYOK settings proof cleared a Pipeline reminder")
	}
}

func TestBYOKObservationRejectsTransientAndUnscopedWitnesses(t *testing.T) {
	base := ObservationInput{
		Category: CategoryBYOKActionable, SourceKey: "byok:project-a",
		PayloadDigest: strings.Repeat("a", 64), AttentionMaterialRevision: "profile-revision:1",
		ProducerWitness: ProducerWitness{Kind: "byok-failure", ProjectID: "project-a", ProfileID: "profile-a", ProfileRevision: 1,
			BindingRevision: 1, FailureReason: "authentication", Trigger: "query", AttemptStartedAt: time.Now().UTC()},
		Observation: Observation{ProjectID: "project-a", Title: "Personal LLM needs attention", Body: "Review AI service settings.", Severity: SeverityAttention,
			Actions: []Action{{ID: "review-ai-settings", Kind: "link", Label: "Review AI settings", Target: &Target{Kind: "project-ai-service", ProjectID: "project-a"}}}},
	}
	if err := ValidateObservationInput(base); err != nil {
		t.Fatalf("valid BYOK observation rejected: %v", err)
	}
	transient := base
	transient.ProducerWitness = base.ProducerWitness
	transient.ProducerWitness.FailureReason = "timeout"
	if err := ValidateObservationInput(transient); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("transient BYOK failure accepted: %v", err)
	}
	unscoped := base
	unscoped.SourceKey = "byok:project-b"
	if err := ValidateObservationInput(unscoped); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-Project source accepted: %v", err)
	}
}

func TestPipelineWitnessUsesOneProjectSlotAndRejectsEqualSequenceCollision(t *testing.T) {
	witness := ProducerWitness{Kind: "pipeline-run", ProjectID: "project-a", ProjectPipelineSequence: 4,
		ReservationID: "reservation-4", AttemptID: "attempt-4", ExecutionID: "run-4", Trigger: "manual",
		Mode: "full", TerminalClass: "failed"}
	input := ObservationInput{Category: CategoryPipelineFailure, SourceKey: "pipeline-fail:project-a", ExpectedSourceRevision: 0,
		ProducerWitness: witness, PayloadDigest: strings.Repeat("a", 64), AttentionMaterialRevision: "failed-run-4",
		Observation: Observation{ProjectID: "project-a", ProjectName: "Paper archive", ExecutionID: "run-4",
			Title: "Pipeline did not publish", Body: "The latest run ended before publication.", Severity: SeverityAttention,
			Actions: []Action{{ID: "open-run", Kind: "link", Label: "Open run", Target: &Target{Kind: "pipeline-run", ProjectID: "project-a", ExecutionID: "run-4"}}}}}
	if err := ValidateObservationInput(input); err != nil {
		t.Fatalf("valid Pipeline input rejected: %v", err)
	}
	if CompareWitness(CategoryPipelineFailure, witness, ProducerWitness{Kind: witness.Kind, ProjectID: witness.ProjectID,
		ProjectPipelineSequence: witness.ProjectPipelineSequence, ReservationID: "reservation-other", AttemptID: "attempt-other",
		ExecutionID: "run-other", Trigger: "manual", Mode: "full", TerminalClass: "failed"}) >= 0 {
		t.Fatal("different run reused the same Project sequence")
	}
	input.SourceKey = "pipeline-run:project-a:run-4"
	if !errors.Is(ValidateObservationInput(input), ErrInvalidInput) {
		t.Fatal("per-run key bypassed the single Project slot")
	}
}

func TestPipelineLegacyBootstrapWitnessKeepsSequenceFloor(t *testing.T) {
	witness := ProducerWitness{Kind: "pipeline-run", ProjectID: "project-a", ProjectPipelineSequence: 7,
		BootstrapFloor: 7, BootstrapDigest: strings.Repeat("a", 64), ExecutionID: "legacy-run", TerminalClass: "failed"}
	input := ObservationInput{Category: CategoryPipelineFailure, SourceKey: "pipeline-fail:project-a", ExpectedSourceRevision: 0,
		ProducerWitness: witness, PayloadDigest: strings.Repeat("b", 64), AttentionMaterialRevision: "legacy-failure",
		Observation: Observation{ProjectID: "project-a", ExecutionID: "legacy-run", Title: "Pipeline did not publish",
			Body: "A legacy run ended without publishing.", Severity: SeverityAttention}}
	if err := ValidateObservationInput(input); err != nil {
		t.Fatalf("valid sequence floor witness rejected: %v", err)
	}
	newer := ProducerWitness{Kind: "pipeline-run", ProjectID: "project-a", ProjectPipelineSequence: 8,
		BootstrapFloor: 7, BootstrapDigest: witness.BootstrapDigest, ReservationID: "reservation-8", AttemptID: "attempt-8",
		ExecutionID: "run-8", Trigger: "manual", Mode: "full", TerminalClass: "failed"}
	if CompareWitness(CategoryPipelineFailure, newer, witness) <= 0 {
		t.Fatal("sequenced run above the bootstrap floor did not win")
	}
	legacyReplay := witness
	legacyReplay.ExecutionID = "other-legacy-run"
	if CompareWitness(CategoryPipelineFailure, legacyReplay, newer) >= 0 {
		t.Fatal("legacy replay at the bootstrap floor beat a later sequenced run")
	}
}

func TestPurposeCooldownAndWitnessOrderingUseAttemptTime(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	older := ProducerWitness{Kind: "purpose-comparison", ProjectID: "project-a", AttemptID: "attempt-a", ConfirmationID: "confirmation-a",
		PairKey: "pair", DescriptionRevision: 2, PublicationGeneration: "generation-1", ConceptsDigest: strings.Repeat("a", 64),
		SummaryInputDigest: strings.Repeat("b", 64), AttemptStartedAt: base}
	newer := older
	newer.AttemptID, newer.ConfirmationID, newer.AttemptStartedAt = "attempt-b", "confirmation-b", base.Add(time.Second)
	if CompareWitness(CategoryPurposeGap, older, newer) >= 0 || CompareWitness(CategoryPurposeGap, newer, older) <= 0 {
		t.Fatal("purpose witness did not order by attempt start")
	}
	if PurposeCooldown != 7*24*time.Hour {
		t.Fatalf("purpose cooldown=%s", PurposeCooldown)
	}
}

func TestPurposeCooldownRequiresAConfirmationStartedAndCompletedAfterExpiry(t *testing.T) {
	lastNotified := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	expires := lastNotified.Add(PurposeCooldown)
	if !purposeNotificationEligible(nil, lastNotified, lastNotified) {
		t.Fatal("first confirmed conflict was suppressed")
	}
	if !purposeNotificationEligible(&lastNotified, expires, expires) {
		t.Fatal("same-payload fresh confirmation at expiry was suppressed")
	}
	if purposeNotificationEligible(&lastNotified, expires.Add(-time.Second), expires.Add(time.Hour)) {
		t.Fatal("attempt begun before expiry notified after completing late")
	}
	if purposeNotificationEligible(&lastNotified, expires, expires.Add(-time.Second)) {
		t.Fatal("confirmation notified before cooldown expiry")
	}
}
