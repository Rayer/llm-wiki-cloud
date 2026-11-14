package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/contentcontext"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
)

type purposeComparisonTestClient struct {
	result llm.CompletionResult
	system string
	input  string
	calls  int
}

func (c *purposeComparisonTestClient) Complete(_ context.Context, systemPrompt, input string) (llm.CompletionResult, error) {
	c.calls++
	c.system, c.input = systemPrompt, input
	return c.result, nil
}
func (*purposeComparisonTestClient) Close() {}

func TestWorkerRecoversPersistedSummaryAndAppliesComparisonThroughCommonNudgeStore(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid Firestore emulator host %q", endpoint)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ownerID, projectID := "purpose-worker-owner", fmt.Sprintf("purpose-worker-%d", time.Now().UnixNano())
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(fmt.Sprintf("purpose-worker-%d", time.Now().UnixNano()), "(default)", "", "", "")
	if err != nil {
		t.Fatalf("create local Firestore emulator client: %v", err)
	}
	defer fsClient.Close()
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(ownerID + "_" + projectID)
	defer func() { _, _ = projectRef.Delete(context.Background()) }()
	if _, err := projectRef.Set(ctx, map[string]interface{}{
		"user_id": ownerID, "project_id": projectID, "name": "Research archive",
		"description": "Published engineering research", "description_revision": int64(2),
	}); err != nil {
		t.Fatal(err)
	}

	digest := strings.Repeat("a", 64)
	publication := contentcontext.Publication{Generation: "published-G1", ConceptsDigest: digest, ManifestIdentity: "manifest-G1"}
	authority := &contentContextTestPublicationAuthority{publication: publication}
	repository := contentcontext.NewFirestoreRepository(fsClient.Raw())
	if _, err := repository.ReconcileCurrent(ctx, ownerID, projectID, authority); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.Claim(ctx, ownerID, projectID, authority)
	if err != nil || claim == nil {
		t.Fatalf("summary claim=%+v err=%v", claim, err)
	}
	completion := contentcontext.SummaryCompletion{
		Text: "Published engineering and product topics", PromptRevision: contentcontext.SummaryPromptRevision,
		ProfileSource: "shared", ProfileID: "fast-profile", ProfileRevision: 7, Role: contentcontext.SummaryRole,
		RequestedModel: "fast-requested", ReturnedModel: "fast-returned", InputDigest: strings.Repeat("e", 64),
		SelectionVersion: contentcontext.SummarySelectionVersion, OverviewSchemaVersion: 1,
		ShortTitle: "Engineering topics", TopicTags: []string{"Engineering", "Product"},
		TotalConcepts: 3, SelectedConcepts: 2, SelectedSlugs: []string{"engineering", "product"}, Sampled: true,
	}
	if saved, err := repository.Complete(ctx, ownerID, projectID, authority, *claim, completion); err != nil || !saved {
		t.Fatalf("save summary=%t err=%v", saved, err)
	}

	client := &purposeComparisonTestClient{result: llm.CompletionResult{Text: `{"verdict":"clear_conflict","basis":"The saved overview is about engineering while the purpose is local history."}`,
		Metadata: llm.CompletionMetadata{ProfileID: "fast-profile", ProfileRevision: 7, Provider: "openai",
			Protocol: string(llm.ProtocolOpenAIChatCompletions), Role: string(llm.ProfileRoleFast),
			RequestedModel: "fast-model", ReturnedModel: "fast-model-returned"}}}
	identity := contentContextFastIdentity{source: "shared", profileID: "fast-profile", profileRevision: 7,
		provider: "openai", protocol: string(llm.ProtocolOpenAIChatCompletions), model: "fast-model"}
	cfg := workerConfig{UserID: ownerID, ProjectID: projectID}
	if err := runPurposeComparison(ctx, cfg, repository, authority, client, identity); err != nil {
		t.Fatalf("worker purpose comparison: %v", err)
	}
	if client.calls != 1 || client.system != contentcontext.PurposeCompareSystemPrompt {
		t.Fatalf("comparator calls=%d system=%q", client.calls, client.system)
	}
	var input contentcontext.PurposeCompareInput
	if err := json.Unmarshal([]byte(client.input), &input); err != nil {
		t.Fatalf("decode bounded comparator input: %v", err)
	}
	if input.Description != "Published engineering research" || input.Summary != completion.Text || input.Coverage.TotalConcepts != 3 ||
		input.Coverage.Selected != 2 || !input.Coverage.Sampled || len(input.Coverage.Slugs) != 2 {
		t.Fatalf("comparator did not receive the exact persisted pair and coverage: %+v", input)
	}
	view, err := nudge.NewRepository(fsClient.Raw()).Read(ctx, ownerID, nil, nil)
	if err != nil || len(view.Items) != 1 || view.Items[0].Category != nudge.CategoryPurposeGap || view.Items[0].AttentionState != nudge.AttentionUnread {
		t.Fatalf("common Nudge result=%+v err=%v", view, err)
	}
	observation := view.Items[0].Observations[0].Observation
	if observation == nil || observation.ProjectName != "Research archive" || observation.DeclaredPurpose != input.Description ||
		observation.PublishedSummary != input.Summary || !observation.Coverage.Sampled || len(observation.Actions) != 1 ||
		observation.Actions[0].CopyText != completion.Text || observation.Actions[0].Target == nil || observation.Actions[0].Target.ProjectID != projectID {
		t.Fatalf("Nudge did not preserve exact source-to-target pairing: %+v", observation)
	}
	declaration, comparison, err := repository.ReadPurposeDocuments(ctx, ownerID, projectID)
	if err != nil || declaration == nil || comparison == nil || comparison.DeliveryStatus != "applied" || comparison.SummaryTextDigest != contentcontext.PurposeTextDigest(completion.Text) {
		t.Fatalf("persisted purpose readback declaration=%+v comparison=%+v err=%v", declaration, comparison, err)
	}
}
