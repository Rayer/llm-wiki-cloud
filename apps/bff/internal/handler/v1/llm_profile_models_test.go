package v1

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestLegacyProfileModelNormalizesReadWithoutMutatingStoredDocument(t *testing.T) {
	stored := llmProfileDocument{Model: "legacy-model", Revision: 7}
	models, ok := llmProfileRoleModels(stored)
	if !ok || models != (llmProfileModels{Fast: "legacy-model", Heavy: "legacy-model"}) {
		t.Fatalf("legacy role normalization = %+v, %t", models, ok)
	}
	if stored.Models != (llmProfileModels{}) || stored.Revision != 7 || stored.Model != "legacy-model" {
		t.Fatalf("read normalization mutated the stored profile: %+v", stored)
	}
}

func TestLegacyProfileWriteCannotCollapseDistinctRoles(t *testing.T) {
	current := llmProfileDocument{Model: "heavy-model", Models: llmProfileModels{Fast: "fast-model", Heavy: "heavy-model"}}
	_, err := profileInputModels(llmProfileInput{Model: "replacement-model"}, &current)
	if !errors.Is(err, errLLMProfileRoleModelsRequired) {
		t.Fatalf("legacy write error = %v, want role_models_required", err)
	}

	legacyCurrent := llmProfileDocument{Model: "same-model"}
	got, err := profileInputModels(llmProfileInput{Model: "next-model"}, &legacyCurrent)
	if err != nil || got != (llmProfileModels{Fast: "next-model", Heavy: "next-model"}) {
		t.Fatalf("same-role legacy write = %+v, %v", got, err)
	}
}

func TestProfileModelsStrictlyValidateRolesAndLegacyHeavyAlias(t *testing.T) {
	valid := llmProfileInput{Model: "heavy-model", Models: json.RawMessage(`{"fast":"fast-model","heavy":"heavy-model"}`)}
	got, err := profileInputModels(valid, nil)
	if err != nil || got != (llmProfileModels{Fast: "fast-model", Heavy: "heavy-model"}) {
		t.Fatalf("role models = %+v, %v", got, err)
	}
	for _, input := range []llmProfileInput{
		{Models: json.RawMessage(`{"fast":"fast-model","heavy":"heavy-model","other":"ignored"}`)},
		{Model: "other-heavy", Models: json.RawMessage(`{"fast":"fast-model","heavy":"heavy-model"}`)},
		{Models: json.RawMessage(`{"fast":" ","heavy":"heavy-model"}`)},
		{Models: json.RawMessage(`{"fast":"fast-model","heavy":"heavy-model"} {}`)},
	} {
		if _, err := profileInputModels(input, nil); !errors.Is(err, errLLMProfileInvalid) {
			t.Errorf("invalid models payload %+v error = %v, want invalid profile", input, err)
		}
	}
}
