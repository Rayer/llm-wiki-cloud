package pipelinediagnostic

import "testing"

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
