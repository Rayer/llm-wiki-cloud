package sourceautomation

import (
	"strings"
	"testing"
)

func TestPrepareRequestValidatesRawCommitBoundary(t *testing.T) {
	digest := strings.Repeat("a", 64)
	tests := []struct {
		name    string
		request PrepareRequest
		want    bool
	}{
		{name: "ui upload", request: PrepareRequest{Entrypoint: "ui_upload", RawPath: "notes/a.md", ExpectedSHA256: digest}, want: true},
		{name: "document import", request: PrepareRequest{Entrypoint: "document_import", RawPath: "notes/a.md", ExpectedSHA256: digest}, want: true},
		{name: "unknown producer", request: PrepareRequest{Entrypoint: "profile_update", RawPath: "notes/a.md", ExpectedSHA256: digest}},
		{name: "unsafe path", request: PrepareRequest{Entrypoint: "ui_upload", RawPath: "../a.md", ExpectedSHA256: digest}},
		{name: "invalid digest", request: PrepareRequest{Entrypoint: "ui_upload", RawPath: "a.md", ExpectedSHA256: "bad"}},
		{name: "invalid operation id", request: PrepareRequest{OperationID: "../event", Entrypoint: "ui_upload", RawPath: "a.md", ExpectedSHA256: digest}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := validPrepareRequest(&test.request)
			if got != test.want {
				t.Fatalf("validPrepareRequest() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestGenerationOrderingUsesStorageGeneration(t *testing.T) {
	if !generationAtLeast("12", "11") || !generationAtLeast("11", "11") || generationAtLeast("10", "11") {
		t.Fatal("generationAtLeast did not preserve monotonic storage generations")
	}
}
