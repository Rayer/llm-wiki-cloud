package contentcontext

import (
	"testing"
	"time"
)

func TestStateDocumentRejectsReadyStateWithoutMatchingSummary(t *testing.T) {
	state := validReadyStateDocument()
	if err := state.Validate(); err != nil {
		t.Fatalf("valid ready state rejected: %v", err)
	}

	withoutSummary := state
	withoutSummary.LastGood = nil
	if err := withoutSummary.Validate(); err == nil {
		t.Fatal("ready state without last-good summary was accepted")
	}

	wrongDigest := state
	wrongDigest.LastGood = cloneLastGood(state.LastGood)
	wrongDigest.LastGood.ConceptsSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := wrongDigest.Validate(); err == nil {
		t.Fatal("ready state with a different summary digest was accepted")
	}
}

func TestStateDocumentRejectsInconsistentAttemptBudgetAndIdentity(t *testing.T) {
	state := validReadyStateDocument()
	state.AttemptID = "attempt-1"
	state.AttemptStartedAt = ptrTime(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	state.AttemptFinishedAt = ptrTime(time.Date(2026, 10, 30, 12, 1, 0, 0, time.UTC))
	state.SummaryWork.AttemptID = "attempt-1"
	state.SummaryWork.LeaseUntil = ptrTime(time.Date(2026, 10, 30, 12, 2, 0, 0, time.UTC))
	if err := state.Validate(); err != nil {
		t.Fatalf("valid completed attempt rejected: %v", err)
	}

	wrongAttempt := state
	wrongAttempt.SummaryWork = cloneSummaryWork(state.SummaryWork)
	wrongAttempt.SummaryWork.AttemptID = "different-attempt"
	if err := wrongAttempt.Validate(); err == nil {
		t.Fatal("mismatched attempt IDs were accepted")
	}

	exhaustedBudget := state
	exhaustedBudget.SummaryWork = cloneSummaryWork(state.SummaryWork)
	exhaustedBudget.SummaryWork.RetryCount = SummaryMaxClaims
	if err := exhaustedBudget.Validate(); err == nil {
		t.Fatal("retry count beyond the claim budget was accepted")
	}
}

func validReadyStateDocument() StateDocument {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	publication := Publication{Generation: "G1", ConceptsDigest: digest, ManifestIdentity: "manifest-G1"}
	completion := testSummaryCompletion("ready summary")
	now := time.Date(2026, 10, 30, 12, 1, 0, 0, time.UTC)
	return StateDocument{
		SchemaVersion: StateSchemaVersion, TargetGeneration: publication.Generation,
		TargetConceptsSHA: digest, Status: StatusReady, TargetSequence: 1,
		LatestPublication: &PublishedHead{Generation: publication.Generation, ConceptsSHA256: digest, ManifestIdentity: publication.ManifestIdentity},
		SummaryWork: &SummaryWorkRecord{WorkID: SummaryWorkID("owner", "project", digest, SummaryPromptRevision, SummarySelectionVersion),
			ConceptsSHA256: digest, SourceGeneration: publication.Generation, Status: StatusReady},
		LastGood: &LastGood{Text: completion.Text, SourceGeneration: publication.Generation, ConceptsSHA256: digest,
			GeneratedAt: now, PromptRevision: completion.PromptRevision, ProfileSource: completion.ProfileSource, ProfileID: completion.ProfileID,
			ProfileRevision: completion.ProfileRevision, Role: completion.Role, RequestedModel: completion.RequestedModel,
			ReturnedModel: completion.ReturnedModel, InputDigest: completion.InputDigest, SelectionVersion: completion.SelectionVersion},
	}
}

func cloneLastGood(value *LastGood) *LastGood {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneSummaryWork(value *SummaryWorkRecord) *SummaryWorkRecord {
	if value == nil {
		return nil
	}
	copy := *value
	if value.LeaseUntil != nil {
		copy.LeaseUntil = ptrTime(*value.LeaseUntil)
	}
	return &copy
}

func ptrTime(value time.Time) *time.Time { return &value }
