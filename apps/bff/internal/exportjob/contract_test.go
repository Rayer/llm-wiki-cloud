package exportjob

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExportContractJSONKeepsNullableStateAndScopeVocabulary(t *testing.T) {
	reason := RejectInProgress
	state := State{Eligible: false, RejectionReason: &reason}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"latest_job":null`, `"current":null`, `"previous":null`, `"eligible":false`, `"rejection_reason":"in_progress"`, `"next_allowed_at":null`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON %s is missing %s", data, want)
		}
	}
	for _, scope := range []Scope{ScopeRaw, ScopeRawFull, ScopeRawFullMetadata} {
		if !scope.Valid() {
			t.Errorf("scope %q is not valid", scope)
		}
	}
	if Scope("raw-metadata").Valid() {
		t.Fatal("unknown scope accepted")
	}

	jobJSON, err := json.Marshal(Job{ExportID: "x", Scope: ScopeRaw, Status: StatusFailed})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"snapshot_at":null`, `"completed_at":null`, `"expires_at":null`, `"next_allowed_at":null`, `"size_bytes":null`, `"error_code":null`, `"error_message":null`} {
		if !strings.Contains(string(jobJSON), want) {
			t.Errorf("job JSON %s is missing %s", jobJSON, want)
		}
	}
}

func TestArchiveExpiryIsDerivedFromCustomTime(t *testing.T) {
	completed := time.Date(2026, 9, 24, 3, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	got := ExpiryFromCustomTime(completed)
	want := time.Date(2026, 9, 26, 19, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("ExpiryFromCustomTime() = %s, want %s", got, want)
	}
}

func TestObjectPathsAreOwnerScopedAndSeparateReadyFromTemporary(t *testing.T) {
	ready, err := ReadyObject("user-a", "project-a", "export-a")
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := TempPrefix("user-a", "project-a", "export-a")
	if err != nil {
		t.Fatal(err)
	}
	if ready != "exports/ready/user-a/project-a/export-a/archive.zip" {
		t.Fatalf("ready object = %q", ready)
	}
	if tmp != "exports/tmp/user-a/project-a/export-a/" {
		t.Fatalf("temporary prefix = %q", tmp)
	}
	otherOwner, err := ReadyObject("user-b", "project-a", "export-a")
	if err != nil {
		t.Fatal(err)
	}
	if ready == otherOwner {
		t.Fatal("different owners share the same archive object path")
	}
	if _, err := ReadyObject("user/../other", "project-a", "export-a"); err == nil {
		t.Fatal("path traversal owner was accepted")
	}
	if _, err := ReadyObject(strings.Repeat("u", 257), "project-a", "export-a"); err == nil {
		t.Fatal("oversized owner segment was accepted")
	}
}
