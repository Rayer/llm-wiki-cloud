package v1

import (
	"fmt"
	"testing"
)

func TestPipelineNudgeProjectBookkeepingClassification(t *testing.T) {
	const ownerID = "nudge-owner"
	canonical := pipelineNudgeProjectRecord{docID: ownerID + "_project-a", data: map[string]interface{}{
		"user_id": ownerID, "project_id": "project-a", "name": "Project A",
	}}
	bookkeeping := pipelineNudgeProjectRecord{docID: ownerID + "_request-key", data: map[string]interface{}{
		"user_id": ownerID, "project_id": "project-a", "idempotency_key": "request-key",
	}}
	compatibleCanonical := pipelineNudgeProjectRecord{docID: ownerID + "_project-a", data: map[string]interface{}{
		"user_id": ownerID, "project_id": "project-a", "idempotency_key": "different-valid-key",
	}}

	if got := pipelineNudgeBookkeepingProjectDocuments(ownerID, []pipelineNudgeProjectRecord{canonical}); len(got) != 0 {
		t.Fatalf("canonical-only list unexpectedly excluded records: %v", got)
	}
	if got := pipelineNudgeBookkeepingProjectDocuments(ownerID, []pipelineNudgeProjectRecord{canonical, bookkeeping}); len(got) != 1 {
		t.Fatalf("verified creation bookkeeping was not excluded: %v", got)
	} else if _, ok := got[bookkeeping.docID]; !ok {
		t.Fatalf("bookkeeping document %q was not excluded: %v", bookkeeping.docID, got)
	}
	if got := pipelineNudgeBookkeepingProjectDocuments(ownerID, []pipelineNudgeProjectRecord{bookkeeping}); len(got) != 0 {
		t.Fatalf("bookkeeping without its canonical Project was excluded: %v", got)
	}
	if id, ok := pipelineNudgeProjectIdentity(ownerID, compatibleCanonical); !ok || id != "project-a" {
		t.Fatalf("valid owner-list compatibility changed: projectID=%q valid=%t", id, ok)
	}
	if got := pipelineNudgeBookkeepingProjectDocuments(ownerID, []pipelineNudgeProjectRecord{compatibleCanonical}); len(got) != 0 {
		t.Fatalf("valid canonical owner-list record was treated as bookkeeping: %v", got)
	}

	invalidRecords := []struct {
		name   string
		record pipelineNudgeProjectRecord
	}{
		{name: "wrong owner", record: pipelineNudgeProjectRecord{docID: bookkeeping.docID, data: map[string]interface{}{
			"user_id": "another-owner", "project_id": "project-a", "idempotency_key": "request-key",
		}}},
		{name: "bad marker type", record: pipelineNudgeProjectRecord{docID: bookkeeping.docID, data: map[string]interface{}{
			"user_id": ownerID, "project_id": "project-a", "idempotency_key": []string{"request-key"},
		}}},
		{name: "missing project identity", record: pipelineNudgeProjectRecord{docID: ownerID + "_request-key", data: map[string]interface{}{
			"user_id": ownerID, "idempotency_key": "request-key",
		}}},
		{name: "malformed project identity", record: pipelineNudgeProjectRecord{docID: ownerID + "_request-key", data: map[string]interface{}{
			"user_id": ownerID, "project_id": "../project-a", "idempotency_key": "request-key",
		}}},
		{name: "marker does not match suffix", record: pipelineNudgeProjectRecord{docID: ownerID + "_request-key", data: map[string]interface{}{
			"user_id": ownerID, "project_id": "project-a", "idempotency_key": "other-key",
		}}},
	}
	for _, test := range invalidRecords {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := pipelineNudgeBookkeepingProjectID(ownerID, test.record); ok {
				t.Fatal("record was classified as creation bookkeeping")
			}
			if _, ok := pipelineNudgeProjectIdentity(ownerID, test.record); ok {
				t.Fatal("noncanonical or malformed record passed canonical identity validation")
			}
			if got := pipelineNudgeBookkeepingProjectDocuments(ownerID, []pipelineNudgeProjectRecord{canonical, test.record}); len(got) != 0 {
				t.Fatalf("invalid record was excluded from error handling: %v", got)
			}
		})
	}
}

func TestBoundPipelineNudgeProjectRecordsKeepsTruncationHonest(t *testing.T) {
	projects := make([]pipelineNudgeProjectRecord, pipelineNudgeProjectLimit+1)
	for index := range projects {
		projects[index] = pipelineNudgeProjectRecord{docID: fmt.Sprintf("owner_project-%03d", index)}
	}
	bounded, truncated := boundPipelineNudgeProjectRecords(projects)
	if !truncated || len(bounded) != pipelineNudgeProjectLimit {
		t.Fatalf("bounded project list len=%d truncated=%t, want %d and true", len(bounded), truncated, pipelineNudgeProjectLimit)
	}
}

func TestPipelineNudgeBookkeepingAtProjectLimitUsesTheBoundedSentinel(t *testing.T) {
	const ownerID = "nudge-owner"
	projects := make([]pipelineNudgeProjectRecord, pipelineNudgeProjectLimit+1)
	for index := 0; index < pipelineNudgeProjectLimit-1; index++ {
		projects[index] = pipelineNudgeProjectRecord{docID: fmt.Sprintf("%s_project-%03d", ownerID, index), data: map[string]interface{}{
			"user_id": ownerID, "project_id": fmt.Sprintf("project-%03d", index),
		}}
	}
	bookkeeping := pipelineNudgeProjectRecord{docID: ownerID + "_request-key", data: map[string]interface{}{
		"user_id": ownerID, "project_id": "project-at-limit", "idempotency_key": "request-key",
	}}
	canonical := pipelineNudgeProjectRecord{docID: ownerID + "_project-at-limit", data: map[string]interface{}{
		"user_id": ownerID, "project_id": "project-at-limit",
	}}
	projects[pipelineNudgeProjectLimit-1] = bookkeeping
	projects[pipelineNudgeProjectLimit] = canonical

	bookkeepingDocuments := pipelineNudgeBookkeepingProjectDocuments(ownerID, projects)
	bounded, truncated := boundPipelineNudgeProjectRecords(projects)
	if !truncated || len(bounded) != pipelineNudgeProjectLimit {
		t.Fatalf("bounded project list len=%d truncated=%t, want %d and true", len(bounded), truncated, pipelineNudgeProjectLimit)
	}
	if _, recognized := bookkeepingDocuments[bookkeeping.docID]; !recognized {
		t.Fatal("bookkeeping companion in the processed prefix was not recognized by its canonical sentinel")
	}
}
