package pipelinediagnostic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicationOutcomeForStage(t *testing.T) {
	tests := []struct {
		stage Stage
		want  PublicationOutcome
	}{
		{stage: StageSyntoConfigValidation, want: PublicationOutcomeAbsent},
		{stage: StageSyntoRun, want: PublicationOutcomeAbsent},
		{stage: StagePostprocess, want: PublicationOutcomeAbsent},
		{stage: StageGenerationPublish, want: PublicationOutcomeUnknown},
		{stage: StageReceiptRecording, want: PublicationOutcomeUnknown},
		{stage: StageLeaseCleanup, want: PublicationOutcomeCommitted},
		{stage: StageUnknown, want: PublicationOutcomeUnknown},
	}
	for _, test := range tests {
		t.Run(string(test.stage), func(t *testing.T) {
			if got := PublicationOutcomeForStage(test.stage); got != test.want {
				t.Fatalf("PublicationOutcomeForStage(%q)=%v, want %v", test.stage, got, test.want)
			}
		})
	}
}

func TestConfirmedInvokeFailureSupportsLegacyAndScheduledMarkersStrictly(t *testing.T) {
	base := ConfirmedInvokeFailure{
		Version: 1, Status: "failed", Stage: StageUnknown, ErrorClass: ErrorClassUnknown,
		Execution: "reservation-1", Message: ConfirmedInvokeFailureMessage,
	}
	manualBytes, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manualBytes), `"attempt_id"`) {
		t.Fatalf("empty manual marker serialized attempt_id: %s", manualBytes)
	}
	manual, err := DecodeConfirmedInvokeFailure(manualBytes)
	if err != nil || !manual.Confirms("reservation-1", "", false) {
		t.Fatalf("legacy/manual marker=%+v err=%v", manual, err)
	}

	base.AttemptID = "scheduled-incarnation-1"
	scheduledBytes, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := DecodeConfirmedInvokeFailure(scheduledBytes)
	if err != nil || !scheduled.Confirms("reservation-1", "scheduled-incarnation-1", true) {
		t.Fatalf("scheduled marker=%+v err=%v", scheduled, err)
	}
	if scheduled.Confirms("reservation-1", "scheduled-incarnation-2", true) {
		t.Fatal("stale scheduled incarnation matched a newer reservation")
	}

	unknownField := strings.TrimSuffix(string(manualBytes), "}") + `,"future_field":true}`
	if _, err := DecodeConfirmedInvokeFailure([]byte(unknownField)); err == nil {
		t.Fatalf("strict decoder accepted unknown marker field: %s", unknownField)
	}
	if _, err := DecodeConfirmedInvokeFailure(append(manualBytes, manualBytes...)); err == nil {
		t.Fatal("strict decoder accepted trailing JSON value")
	}
}
