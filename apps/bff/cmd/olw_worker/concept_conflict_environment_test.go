package main

import (
	"strings"
	"testing"
)

func TestAllowlistedSyntoEnvironmentPassesConflictSourceVault(t *testing.T) {
	input, err := prepareOLWEnvironment(workerConfig{
		ConceptResolveJSON:         `{}`,
		ConceptConflictStorageView: "legacy",
		ConceptConflictSourceVault: "/private/tmp/lwc341-vault",
		ProjectID:                  "fixture-project",
		ExecutionID:                "fixture-execution",
	})
	if err != nil {
		t.Fatal(err)
	}
	input = append(input, "UNTRUSTED_OVERRIDE=ignored")
	got := allowlistedSyntoEnvironment(input)
	values := map[string]string{}
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	if values["LWC_CONFLICT_SOURCE_VAULT"] != "/private/tmp/lwc341-vault" {
		t.Fatalf("conflict source vault env = %q, want trusted mounted vault path", values["LWC_CONFLICT_SOURCE_VAULT"])
	}
	if values["LWC_CONFLICT_STORAGE_VIEW"] != "legacy" {
		t.Fatalf("conflict storage view env = %q, want legacy", values["LWC_CONFLICT_STORAGE_VIEW"])
	}
	if _, ok := values["UNTRUSTED_OVERRIDE"]; ok {
		t.Fatal("untrusted environment override passed through")
	}
}
