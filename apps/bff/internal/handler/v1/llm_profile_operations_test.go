package v1

import (
	"errors"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/llm"
)

func TestDraftProfileOperationUsesOnlyApprovedAdminReferences(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil)
	const approved = "projects/fixture/secrets/provider/versions/3"
	h.SetBYOKDeploymentCredentials([]BYOKDeploymentCredential{{Reference: approved, Label: "Fixture key", Value: []byte("synthetic-admin-reference-key")}})
	input := llmProfileOperationInput{
		Provider: "custom", Protocol: string(llm.ProtocolOpenAIChatCompletions), Endpoint: "https://fixture.example/v1",
		Models: []byte(`{"fast":"fast-model","heavy":"heavy-model"}`), Role: string(llm.ProfileRoleFast), CredentialReference: approved,
	}
	config, key, err := h.resolveLLMProfileOperation(t.Context(), "platform", true, input, llm.ProfileRoleFast, true)
	if err != nil || string(key) != "synthetic-admin-reference-key" || config.Model != "fast-model" {
		t.Fatalf("approved Admin draft reference resolved config=%+v err=%v", config, err)
	}
	clear(key)
	if _, _, err := h.resolveLLMProfileOperation(t.Context(), "user-a", false, input, llm.ProfileRoleFast, true); !errors.Is(err, errLLMProfileCredentialUnavailable) {
		t.Fatalf("personal operation accepted an Admin reference: %v", err)
	}
	input.CredentialReference = "projects/fixture/secrets/unapproved/versions/3"
	if _, _, err := h.resolveLLMProfileOperation(t.Context(), "platform", true, input, llm.ProfileRoleFast, true); !errors.Is(err, errLLMProfileCredentialUnavailable) {
		t.Fatalf("Admin operation accepted an unapproved reference: %v", err)
	}
	input.CredentialReference = approved
	input.APIKey = "synthetic-inline-secret"
	if _, _, err := h.resolveLLMProfileOperation(t.Context(), "platform", true, input, llm.ProfileRoleFast, true); !errors.Is(err, errLLMProfileCredentialUnavailable) {
		t.Fatalf("Admin reference operation accepted an inline secret: %v", err)
	}
}
