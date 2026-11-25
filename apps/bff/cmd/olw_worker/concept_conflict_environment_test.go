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
		UserID:                     "trusted-user",
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
	if values["USER_ID"] != "trusted-user" || values["PROJECT_ID"] != "fixture-project" || values["LWC_PROJECT_ID"] != "fixture-project" {
		t.Fatalf("trusted worker identity did not reach Synto: USER_ID=%q PROJECT_ID=%q LWC_PROJECT_ID=%q", values["USER_ID"], values["PROJECT_ID"], values["LWC_PROJECT_ID"])
	}
	if _, ok := values["UNTRUSTED_OVERRIDE"]; ok {
		t.Fatal("untrusted environment override passed through")
	}
}

func TestConceptConflictEnvironmentRequiresTrustedIdentity(t *testing.T) {
	if _, err := prepareOLWEnvironment(workerConfig{ConceptResolveJSON: `{}`, ProjectID: "project"}); err == nil {
		t.Fatal("resolve environment was prepared without an authenticated principal")
	}
}

func TestAllowlistedSyntoEnvironmentCarriesTrustedProjectForDiscovery(t *testing.T) {
	prepared, err := prepareOLWEnvironment(workerConfig{ProjectID: "trusted-project"})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, item := range allowlistedSyntoEnvironment(append(prepared, "UNTRUSTED_PROJECT_ID=attacker")) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	if values["LWC_PROJECT_ID"] != "trusted-project" {
		t.Fatalf("trusted project did not reach normal Synto discovery: %q", values["LWC_PROJECT_ID"])
	}
	if _, ok := values["UNTRUSTED_PROJECT_ID"]; ok {
		t.Fatal("untrusted project override passed through the Synto allowlist")
	}
}
