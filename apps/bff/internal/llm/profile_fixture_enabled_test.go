//go:build byokprofilefixture

package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileFixtureTransportNeverContactsConfiguredHost(t *testing.T) {
	receipt := filepath.Join(t.TempDir(), "fixture-receipts.log")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_TRANSPORT", "1")
	t.Setenv("LWC_BYOK_PROFILE_FIXTURE_RECEIPT_FILE", receipt)
	client, err := NewProfileClient(ProfileConfig{
		ID: "fixture-profile", Revision: 2, Provider: "custom", Protocol: ProtocolOpenAIChatCompletions,
		Endpoint: "https://fixture.example/v1", Model: "fixture-no-network-model",
	}, "synthetic-fixture-key", ProfileClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Complete(context.Background(), "Use a synthetic fixture.", "What profile is selected?")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if result.Text != "The Project uses its selected BYOK profile and model for this chat call." || result.Metadata.RequestedModel != "fixture-no-network-model" {
		t.Fatalf("fixture result = %+v", result)
	}
	data, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); !strings.Contains(text, "openai_chat_completions\tfixture-no-network-model\t/v1/chat/completions") || strings.Contains(text, "synthetic-fixture-key") {
		t.Fatalf("fixture receipt is missing safe route metadata or contains a key: %q", text)
	}
}

func TestProfileFixtureQueryPlanPreservesJSONEncodedRawQuery(t *testing.T) {
	const rawQuery = "How does the BYOK profile fixture route requests?"
	encoded, err := json.Marshal(rawQuery)
	if err != nil {
		t.Fatal(err)
	}
	content := profileFixtureContent(
		"You produce a retrieval plan. The object fields and exact types are: raw_query string; required array of Criterion.",
		"Raw query: "+string(encoded)+"\nCriterion policy: {}",
	)
	var plan struct {
		RawQuery string `json:"raw_query"`
	}
	if err := json.Unmarshal([]byte(content), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.RawQuery != rawQuery {
		t.Fatalf("fixture raw_query = %q, want %q", plan.RawQuery, rawQuery)
	}
}
