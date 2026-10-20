package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/urlintake"
)

type intakeTestResolver struct{}

func (intakeTestResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type intakeTestFetcher func(context.Context, string) (urlintake.Page, error)

func (f intakeTestFetcher) Fetch(ctx context.Context, rawURL string) (urlintake.Page, error) {
	return f(ctx, rawURL)
}

type intakeTestEvaluator func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error)

func (f intakeTestEvaluator) Evaluate(ctx context.Context, input urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
	return f(ctx, input)
}

func TestScrapeRawConfirmationUsesVerifiedProjectAndStoredCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := localfs.New(t.TempDir()).WithScope("actor-1", "project-1")
	var fetchCalls atomic.Int32
	service := urlintake.NewService(
		intakeTestFetcher(func(context.Context, string) (urlintake.Page, error) {
			fetchCalls.Add(1)
			return urlintake.Page{Title: "Example Article", Markdown: "# Example Article\n\nReadable article body.\n"}, nil
		}),
		intakeTestEvaluator(func(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
			return urlintake.QualityEvaluation{ShellProbability: .5, ContentProbability: .5}, nil
		}),
		func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
			if scope != (urlintake.ActorProject{ActorID: "actor-1", ProjectID: "project-1"}) {
				t.Fatalf("store scope = %#v", scope)
			}
			return store, nil
		},
		urlintake.DefaultQualityThresholds,
	)
	service.Resolver = intakeTestResolver{}
	service.Now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	h := NewWithURLIntake(nil, nil, service)

	previewResponse := callScrapeRaw(t, h, "actor-1", "project-1", `{"url":"https://example.com/article"}`)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview HTTP status = %d, body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview ScrapeResponse
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Status != "needs_confirmation" || preview.Preview == nil || preview.ConfirmationRef == "" {
		t.Fatalf("preview response = %#v", preview)
	}
	if preview.Preview.Markdown != "---\ncreated: \"2026-10-21\"\nsource: https://example.com/article\ntitle: Example Article\n---\n# Example Article\n\nReadable article body.\n" {
		t.Fatalf("candidate preview Markdown = %q", preview.Preview.Markdown)
	}
	files, err := store.ListRawFiles(context.Background())
	markdownFiles := 0
	for _, file := range files {
		if strings.HasSuffix(file.Name, ".md") {
			markdownFiles++
		}
	}
	if err != nil || markdownFiles != 0 {
		t.Fatalf("unconfirmed candidate wrote raw files: %#v, %v", files, err)
	}

	wrongProject := callScrapeRaw(t, h, "actor-1", "project-2", `{"confirmation_ref":"`+preview.ConfirmationRef+`"}`)
	if wrongProject.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong-project confirmation HTTP status = %d, body=%s", wrongProject.Code, wrongProject.Body.String())
	}
	if fetchCalls.Load() != 1 {
		t.Fatalf("fetch calls after wrong-project confirmation = %d, want 1", fetchCalls.Load())
	}

	confirmed := callScrapeRaw(t, h, "actor-1", "project-1", `{"confirmation_ref":"`+preview.ConfirmationRef+`"}`)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirmation HTTP status = %d, body=%s", confirmed.Code, confirmed.Body.String())
	}
	var saved ScrapeResponse
	if err := json.Unmarshal(confirmed.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "stored" || saved.Storage == nil || saved.Storage.Path != "raw/Example-Article.md" {
		t.Fatalf("confirmation result = %#v", saved)
	}
	if fetchCalls.Load() != 1 {
		t.Fatalf("fetch calls after confirmation = %d, want no refetch", fetchCalls.Load())
	}
	data, err := store.ReadFile(context.Background(), saved.Storage.Path)
	if err != nil || !bytes.Contains(data, []byte("Readable article body.")) {
		t.Fatalf("confirmed raw content = %q, %v", data, err)
	}
}

func callScrapeRaw(t *testing.T, h *Handler, actorID, projectID, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/raw/scrape", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	context.Set("userID", actorID)
	context.Set("projectID", projectID)
	h.ScrapeRaw(context)
	return recorder
}
