package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/urlintake"
)

type mcpURLIntakeFetcher func(context.Context, string) (urlintake.Page, error)

func (f mcpURLIntakeFetcher) Fetch(ctx context.Context, sourceURL string) (urlintake.Page, error) {
	return f(ctx, sourceURL)
}

type mcpURLIntakeEvaluator func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error)

func (f mcpURLIntakeEvaluator) Evaluate(ctx context.Context, input urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
	return f(ctx, input)
}

type mcpURLIntakeResolver struct{}

func (mcpURLIntakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func newMCPURLIntakeAdapterTest(t *testing.T, fetcher urlintake.Fetcher, evaluator urlintake.QualityEvaluator) (MCPURLImportService, *localfs.Client) {
	t.Helper()
	root := localfs.New(t.TempDir())
	service := urlintake.NewService(fetcher, evaluator, func(actor urlintake.ActorProject) (urlintake.ProjectStore, error) {
		return root.WithScope(actor.ActorID, actor.ProjectID), nil
	}, urlintake.DefaultQualityThresholds)
	service.Resolver = mcpURLIntakeResolver{}
	return NewURLIntakeMCPAdapter(service), root
}

func TestMCPURLIntakeAdapterMapsCreateRefreshAndIdenticalRefresh(t *testing.T) {
	pages := []urlintake.Page{
		{Title: "Article", Markdown: "# Article\n\nfirst content\n"},
		{Title: "Article", Markdown: "# Article\n\nupdated content\n"},
		{Title: "Article", Markdown: "# Article\n\nupdated content\n"},
	}
	index := 0
	fetcher := mcpURLIntakeFetcher(func(context.Context, string) (urlintake.Page, error) {
		page := pages[index]
		index++
		return page, nil
	})
	evaluator := mcpURLIntakeEvaluator(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
		return urlintake.QualityEvaluation{ShellProbability: .02, ContentProbability: .98, ReturnedModel: "jev-1.13.0"}, nil
	})
	adapter, root := newMCPURLIntakeAdapterTest(t, fetcher, evaluator)
	actor := MCPURLImportActorProject{UserID: "actor-one", ProjectID: "project-one"}

	created, err := adapter.Import(context.Background(), actor, MCPURLImportRequest{URL: "https://example.test/article?utm_source=mail", Tags: []string{"reading"}})
	if err != nil || created.Status != "created" || created.Path == "" || created.SHA256 == "" || created.SourceURL != "https://example.test/article?utm_source=mail" {
		t.Fatalf("first URL import = %+v, %v", created, err)
	}
	updated, err := adapter.Import(context.Background(), actor, MCPURLImportRequest{URL: "https://EXAMPLE.test/article"})
	if err != nil || updated.Status != "updated" || updated.Path != created.Path || updated.SHA256 == created.SHA256 {
		t.Fatalf("same normalized URL refresh = %+v, %v; initial=%+v", updated, err, created)
	}
	unchanged, err := adapter.Import(context.Background(), actor, MCPURLImportRequest{URL: "https://example.test/article"})
	if err != nil || unchanged.Status != "unchanged" || unchanged.Path != created.Path || unchanged.SHA256 != updated.SHA256 {
		t.Fatalf("identical URL refresh = %+v, %v; updated=%+v", unchanged, err, updated)
	}
	stored, err := root.WithScope("actor-one", "project-one").ReadFile(context.Background(), created.Path)
	if err != nil || !strings.Contains(string(stored), "updated content") || strings.Contains(string(stored), "first content") {
		t.Fatalf("shared URL service did not persist refreshed bytes: err=%v data=%q", err, stored)
	}
}

func TestMCPURLIntakeAdapterConfirmsExactCandidateWithoutRefetch(t *testing.T) {
	fetches := 0
	fetcher := mcpURLIntakeFetcher(func(context.Context, string) (urlintake.Page, error) {
		fetches++
		return urlintake.Page{Title: "Review me", Markdown: "# Review me\n\nexact candidate\n"}, nil
	})
	evaluator := mcpURLIntakeEvaluator(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
		return urlintake.QualityEvaluation{ShellProbability: .42, ContentProbability: .61, ReturnedModel: "jev-1.13.0"}, nil
	})
	adapter, root := newMCPURLIntakeAdapterTest(t, fetcher, evaluator)
	actor := MCPURLImportActorProject{UserID: "actor-one", ProjectID: "project-one"}
	preview, err := adapter.Import(context.Background(), actor, MCPURLImportRequest{URL: "https://example.test/review"})
	if err != nil || preview.Status != "needs_confirmation" || preview.ConfirmationRef == "" || preview.Title != "Review me" || !strings.Contains(preview.Preview, "exact candidate") {
		t.Fatalf("uncertain quality result = %+v, %v", preview, err)
	}
	if files, err := root.WithScope("actor-one", "project-one").ListRawFiles(context.Background()); err != nil || len(files) != 0 {
		t.Fatalf("unconfirmed URL candidate wrote raw data: files=%v err=%v", files, err)
	}
	wrongActor, err := adapter.Confirm(context.Background(), MCPURLImportActorProject{UserID: "other", ProjectID: "project-one"}, preview.ConfirmationRef)
	if err != nil || wrongActor.Status != "rejected" {
		t.Fatalf("wrong-actor confirmation = %+v, %v", wrongActor, err)
	}
	confirmed, err := adapter.Confirm(context.Background(), actor, preview.ConfirmationRef)
	if err != nil || confirmed.Status != "created" || confirmed.Path == "" || confirmed.SHA256 == "" {
		t.Fatalf("exact candidate confirmation = %+v, %v", confirmed, err)
	}
	if fetches != 1 {
		t.Fatalf("fetch calls = %d, want 1 (confirmation must not refetch)", fetches)
	}
	stored, err := root.WithScope("actor-one", "project-one").ReadFile(context.Background(), confirmed.Path)
	if err != nil || string(stored) != preview.Preview || confirmed.SHA256 != digestMCPURLTest(stored) {
		t.Fatalf("confirmed bytes differ from reviewed candidate: err=%v bytes=%q", err, stored)
	}
}

func TestMCPURLImportToolCallPreservesTruncationBeforeExactConfirmation(t *testing.T) {
	fetches := 0
	truncated := true
	fetcher := mcpURLIntakeFetcher(func(context.Context, string) (urlintake.Page, error) {
		fetches++
		return urlintake.Page{Title: "Truncated candidate", Markdown: "# Truncated candidate\n\nVisible prefix.", Truncated: &truncated}, nil
	})
	evaluator := mcpURLIntakeEvaluator(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
		return urlintake.QualityEvaluation{ShellProbability: .42, ContentProbability: .61, ReturnedModel: "jev-1.13.0"}, nil
	})
	adapter, root := newMCPURLIntakeAdapterTest(t, fetcher, evaluator)
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetMCPURLImportService(adapter)
	identity := mcpProjectKeyIdentity{userID: "actor-one", projectID: "project-one", keyID: "grant-write", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}

	review := serveMCPRequest(t, h, identity, mcpToolCallBody(1, "import_url", `{"url":"https://example.test/truncated"}`))
	reviewPayload := mcpStructuredResult(t, review)
	if reviewPayload["status"] != "needs_confirmation" || reviewPayload["provider_truncated"] != true ||
		reviewPayload["warning"] != "The Fetch provider explicitly marked this content as truncated." ||
		reviewPayload["reason"] != "The page could not be classified as clearly substantive and complete." ||
		!strings.Contains(fmt.Sprint(reviewPayload["message"]), "explicitly marked this content as truncated") ||
		reviewPayload["processing_status"] != "not_started" {
		t.Fatalf("MCP review omitted the shared service completeness details: %#v", reviewPayload)
	}
	confirmationRef, _ := reviewPayload["confirmation_ref"].(string)
	if confirmationRef == "" || fetches != 1 {
		t.Fatalf("review returned no confirmation or fetched unexpected number of times: ref=%q fetches=%d", confirmationRef, fetches)
	}
	if files, err := root.WithScope("actor-one", "project-one").ListRawFiles(context.Background()); err != nil || len(files) != 0 {
		t.Fatalf("unconfirmed truncated candidate wrote raw data: files=%v err=%v", files, err)
	}

	confirmed := serveMCPRequest(t, h, identity, mcpToolCallBody(2, "import_url", fmt.Sprintf(`{"confirmation_ref":%q}`, confirmationRef)))
	confirmedPayload := mcpStructuredResult(t, confirmed)
	path, _ := confirmedPayload["path"].(string)
	if confirmedPayload["status"] != "created" || path == "" || confirmedPayload["processing_status"] != "not_started" || fetches != 1 {
		t.Fatalf("confirmation did not commit the exact reviewed candidate without refetch: payload=%#v fetches=%d", confirmedPayload, fetches)
	}
	stored, err := root.WithScope("actor-one", "project-one").ReadFile(context.Background(), path)
	if err != nil || !strings.Contains(string(stored), "Visible prefix.") || !strings.Contains(string(stored), "Truncated candidate") {
		t.Fatalf("confirmed truncated candidate readback failed: path=%q err=%v", path, err)
	}
}

func TestMCPURLIntakeAdapterPreservesUncertainCommitOutcome(t *testing.T) {
	fetches := 0
	root := localfs.New(t.TempDir())
	var store *mcpUncertainURLProjectStore
	service := urlintake.NewService(
		mcpURLIntakeFetcher(func(context.Context, string) (urlintake.Page, error) {
			fetches++
			return urlintake.Page{Title: "Ambiguous write", Markdown: "# Ambiguous write\n\nCandidate"}, nil
		}),
		mcpURLIntakeEvaluator(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
			return urlintake.QualityEvaluation{ShellProbability: .02, ContentProbability: .98, ReturnedModel: "jev-1.13.0"}, nil
		}),
		func(actor urlintake.ActorProject) (urlintake.ProjectStore, error) {
			store = &mcpUncertainURLProjectStore{ProjectStore: root.WithScope(actor.ActorID, actor.ProjectID)}
			return store, nil
		},
		urlintake.DefaultQualityThresholds,
	)
	service.Resolver = mcpURLIntakeResolver{}
	h := New(nil, nil, nil, nil, nil, nil)
	h.SetMCPURLImportService(NewURLIntakeMCPAdapter(service))
	identity := mcpProjectKeyIdentity{userID: "actor", projectID: "project", keyID: "grant", capabilities: []string{mcpQueryCapability, mcpRawWriteCapability}}
	response := serveMCPRequest(t, h, identity, mcpToolCallBody(1, "import_url", `{"url":"https://example.test/article?tracking=private"}`))
	payload := mcpStructuredResult(t, response)
	message, _ := payload["message"].(string)
	if payload["status"] != "unavailable" || payload["commit_status"] != "uncertain" ||
		payload["processing_status"] != "not_started" || payload["source_url"] != nil || payload["preview"] != nil ||
		strings.Contains(message, "tracking=private") {
		t.Fatalf("uncertain storage result was not represented safely: %#v", payload)
	}
	if fetches != 1 || store == nil || store.rawReads != 4 {
		t.Fatalf("fetches=%d raw readbacks=%v; expected one fetch and one uncertain commit reconciliation", fetches, store)
	}
}

type mcpUncertainURLProjectStore struct {
	urlintake.ProjectStore
	rawReads int
}

func (s *mcpUncertainURLProjectStore) ReadFileWithGeneration(ctx context.Context, path string) ([]byte, int64, error) {
	if strings.HasPrefix(path, "raw/") {
		s.rawReads++
		if s.rawReads == 4 {
			return nil, 0, errors.New("storage readback unavailable")
		}
	}
	return s.ProjectStore.ReadFileWithGeneration(ctx, path)
}

func (s *mcpUncertainURLProjectStore) WriteFileIfGeneration(ctx context.Context, data []byte, path string, generation int64) (int64, error) {
	if strings.HasPrefix(path, "raw/") {
		return 0, errors.New("raw write result unavailable")
	}
	return s.ProjectStore.WriteFileIfGeneration(ctx, data, path, generation)
}

func digestMCPURLTest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
