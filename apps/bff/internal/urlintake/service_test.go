package urlintake

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/rawintake"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type fetcherFunc func(context.Context, string) (Page, error)

func (f fetcherFunc) Fetch(ctx context.Context, url string) (Page, error) { return f(ctx, url) }

type evaluatorFunc func(context.Context, QualityInput) (QualityEvaluation, error)

func (f evaluatorFunc) Evaluate(ctx context.Context, input QualityInput) (QualityEvaluation, error) {
	return f(ctx, input)
}

type candidateReadFaultStore struct {
	ProjectStore
	failNextCandidateRead atomic.Bool
}

type candidateWriteFaultStore struct{ ProjectStore }

func (s candidateWriteFaultStore) WriteFileIfGeneration(ctx context.Context, data []byte, path string, expected int64) (int64, error) {
	if strings.HasPrefix(path, candidateObjectPrefix) {
		return 0, errors.New("candidate receipt write failed")
	}
	return s.ProjectStore.WriteFileIfGeneration(ctx, data, path, expected)
}

func (s *candidateReadFaultStore) ReadFileWithGeneration(ctx context.Context, path string) ([]byte, int64, error) {
	if strings.HasPrefix(path, candidateObjectPrefix) && s.failNextCandidateRead.CompareAndSwap(true, false) {
		return nil, 0, errors.New("temporary candidate receipt read outage")
	}
	return s.ProjectStore.ReadFileWithGeneration(ctx, path)
}

func newTestService(t *testing.T, fetcher Fetcher, evaluator QualityEvaluator) (*Service, *localfs.Client) {
	t.Helper()
	store := localfs.New(t.TempDir()).WithScope("actor-1", "project-1")
	service := NewService(fetcher, evaluator, func(scope ActorProject) (ProjectStore, error) {
		if scope != (ActorProject{ActorID: "actor-1", ProjectID: "project-1"}) && scope != (ActorProject{ActorID: "actor-2", ProjectID: "project-1"}) {
			return nil, ErrInvalidScope
		}
		return store, nil
	}, DefaultQualityThresholds)
	service.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	service.Now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	return service, store
}

func usableEvaluator(context.Context, QualityInput) (QualityEvaluation, error) {
	return QualityEvaluation{ShellProbability: .02, ContentProbability: .98, ReturnedModel: "jev-1.13.0"}, nil
}

func uncertainEvaluator(context.Context, QualityInput) (QualityEvaluation, error) {
	return QualityEvaluation{ShellProbability: .42, ContentProbability: .61, ReturnedModel: "jev-1.13.0"}, nil
}

func TestServiceCreatesRefreshesAndNoOpsWithNormalizedTrackingIdentity(t *testing.T) {
	pages := []Page{
		{Title: "Shared Article", Markdown: "# Shared Article\n\nfirst body\n"},
		{Title: "Shared Article", Markdown: "# Shared Article\n\nsecond body\n"},
		{Title: "Shared Article", Markdown: "# Shared Article\n\nsecond body\n"},
	}
	var calls atomic.Int32
	fetcher := fetcherFunc(func(context.Context, string) (Page, error) {
		index := int(calls.Add(1)) - 1
		if index >= len(pages) {
			index = len(pages) - 1
		}
		return pages[index], nil
	})
	service, store := newTestService(t, fetcher, evaluatorFunc(usableEvaluator))
	firstURL := "https://example.com/article?utm_source=mail&q=1"
	first, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: firstURL, Tags: []string{"dev"}})
	if err != nil || first.Status != "stored" || first.Storage == nil || first.Storage.Status != string(rawintake.StatusCreated) {
		t.Fatalf("first import = %#v, %v", first, err)
	}
	second, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://EXAMPLE.com/article?q=1"})
	if err != nil || second.Status != "stored" || second.Storage == nil || second.Storage.Status != string(rawintake.StatusReplaced) {
		t.Fatalf("refresh = %#v, %v", second, err)
	}
	if second.Storage.Path != first.Storage.Path {
		t.Fatalf("refresh path = %q, first path = %q", second.Storage.Path, first.Storage.Path)
	}
	path := first.Storage.Path
	updated, err := store.ReadFile(context.Background(), path)
	if err != nil || !bytes.Contains(updated, []byte("second body")) || bytes.Contains(updated, []byte("first body")) {
		t.Fatalf("refreshed raw = %q, %v", updated, err)
	}
	metadata, _, _, err := parseRawFrontmatter(updated)
	if err != nil || metadata["source"] != firstURL {
		t.Fatalf("refresh lost original provenance: %#v, %v", metadata, err)
	}
	beforeNoop := append([]byte(nil), updated...)
	third, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: firstURL})
	if err != nil || third.Status != "stored" || third.Storage == nil || third.Storage.Status != string(rawintake.StatusAlreadyExists) {
		t.Fatalf("identical refresh = %#v, %v", third, err)
	}
	afterNoop, err := store.ReadFile(context.Background(), path)
	if err != nil || !bytes.Equal(beforeNoop, afterNoop) {
		t.Fatalf("identical refresh changed persisted bytes: %v", err)
	}
	files, err := markdownRawFiles(store)
	if err != nil || len(files) != 1 {
		t.Fatalf("raw files = %#v, %v; want one stable source", files, err)
	}
}

func TestServiceReturnsNotConfiguredFetchDiagnosticWithoutProviderCall(t *testing.T) {
	calls := 0
	fetcher := NewDeepSeekFetcher("")
	fetcher.Client = &http.Client{Transport: urlintakeRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected provider call")
	})}
	service, store := newTestService(t, fetcher, nil)
	result, err := service.Prepare(context.Background(), ActorProject{ActorID: "actor-1", ProjectID: "project-1"}, ImportRequest{URL: "https://example.com/article"})
	files, listErr := store.ListRawFiles(context.Background())
	if err != nil || result.Status != "unavailable" || result.FailureStage != "fetch" || result.FailureDiagnostic == nil ||
		result.FailureDiagnostic.Dependency != DeepSeekFetchProvider || result.FailureDiagnostic.Category != FailureCategoryNotConfigured ||
		!strings.Contains(result.Message, "no provider request was made") || calls != 0 || listErr != nil || len(files) != 0 {
		t.Fatalf("not-configured Fetch result=%#v err=%v calls=%d files=%#v listErr=%v", result, err, calls, files, listErr)
	}
}

func TestFetchFailureMessagesDistinguishProviderAndLocalTimeouts(t *testing.T) {
	tests := []struct {
		name     string
		failure  *ProviderFailure
		category FailureCategory
		status   int
		want     []string
		notWant  []string
	}{
		{
			name: "content unavailable 404",
			failure: &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureProvider,
				Category: FailureCategoryContentUnavailable, StatusCode: http.StatusNotFound},
			category: FailureCategoryContentUnavailable, status: http.StatusNotFound,
			want: []string{"DeepSeek Fetch", "could not retrieve usable page content", "upstream HTTP 404",
				"target access restrictions or target errors", "unsupported content type", "extraction yielding no usable content",
				"does not establish that the target website returned HTTP 404"},
		},
		{
			name: "provider downstream timeout 504",
			failure: &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureProvider,
				Category: FailureCategoryProviderTimeout, StatusCode: http.StatusGatewayTimeout},
			category: FailureCategoryProviderTimeout, status: http.StatusGatewayTimeout,
			want:    []string{"DeepSeek Fetch", "downstream fetch timed out", "upstream HTTP 504"},
			notWant: []string{"local transport timed out"},
		},
		{
			name: "rate limited 429",
			failure: &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureRateLimited,
				Category: FailureCategoryRateLimited, StatusCode: http.StatusTooManyRequests},
			category: FailureCategoryRateLimited, status: http.StatusTooManyRequests,
			want:    []string{"DeepSeek Fetch", "rate limited", "upstream HTTP 429"},
			notWant: []string{"timed out", "content unavailable"},
		},
		{
			name: "request timeout 408",
			failure: &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureProvider,
				Category: FailureCategoryProviderTimeout, StatusCode: http.StatusRequestTimeout},
			category: FailureCategoryProviderTimeout, status: http.StatusRequestTimeout,
			want:    []string{"DeepSeek Fetch", "Request Timeout", "complete request", "upstream HTTP 408"},
			notWant: []string{"downstream fetch timed out"},
		},
		{
			name: "unexpected status 418",
			failure: &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureProvider,
				Category: FailureCategoryUnexpectedStatus, StatusCode: http.StatusTeapot},
			category: FailureCategoryUnexpectedStatus, status: http.StatusTeapot,
			want:    []string{"DeepSeek Fetch", "unexpected HTTP status", "upstream HTTP 418"},
			notWant: []string{"downstream fetch timed out", "local transport timed out"},
		},
		{
			name: "local transport timeout after HTTP 200",
			failure: &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTransport,
				StatusCode: http.StatusOK, Cause: context.DeadlineExceeded},
			category: FailureCategoryTransportTimeout, status: http.StatusOK,
			want:    []string{"local LWC-to-DeepSeek Fetch transport timed out", "upstream HTTP 200"},
			notWant: []string{"downstream fetch timed out", "HTTP 504"},
		},
		{
			name: "local transport timeout without upstream status",
			failure: &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTransport,
				Cause: context.DeadlineExceeded},
			category: FailureCategoryTransportTimeout,
			want:     []string{"local LWC-to-DeepSeek Fetch transport timed out"},
			notWant:  []string{"upstream HTTP", "downstream fetch timed out", "HTTP 504"},
		},
		{
			name: "provider timeout without known status",
			failure: &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureProvider,
				Category: FailureCategoryProviderTimeout},
			category: FailureCategoryProviderTimeout,
			want:     []string{"DeepSeek Fetch", "reported a request timeout"},
			notWant:  []string{"downstream fetch timed out", "upstream HTTP"},
		},
		{
			name: "local request cancellation",
			failure: &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTransport,
				Cause: context.Canceled},
			category: FailureCategoryTransportCancelled,
			want:     []string{"local LWC-to-DeepSeek Fetch request was cancelled"},
			notWant:  []string{"upstream HTTP", "downstream fetch"},
		},
		{
			name:     "unknown provider status",
			failure:  &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureProvider, Category: FailureCategoryUnknown},
			category: FailureCategoryUnknown,
			want:     []string{"DeepSeek Fetch failed with an unknown cause"},
			notWant:  []string{"upstream HTTP", "no response was received"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := failureResult(test.failure)
			if result.FailureDiagnostic == nil || result.FailureDiagnostic.Category != test.category ||
				result.FailureDiagnostic.UpstreamStatus != test.status {
				t.Fatalf("diagnostic = %#v, want category=%s upstream_status=%d", result.FailureDiagnostic, test.category, test.status)
			}
			for _, fragment := range test.want {
				if !strings.Contains(result.Message, fragment) {
					t.Errorf("message %q does not contain %q", result.Message, fragment)
				}
			}
			for _, fragment := range test.notWant {
				if strings.Contains(result.Message, fragment) {
					t.Errorf("message %q must not contain %q", result.Message, fragment)
				}
			}
		})
	}
}

type deadlineReader struct{}

func (deadlineReader) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

func TestDeepSeekBodyReadTimeoutKeepsHTTP200AndLocalMeaning(t *testing.T) {
	fetcher := NewDeepSeekFetcher("fixture-key")
	fetcher.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	fetcher.Client = &http.Client{Transport: urlintakeRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(deadlineReader{}), Request: request,
		}, nil
	})}
	_, err := fetcher.Fetch(context.Background(), "https://example.com/article")
	var failure *ProviderFailure
	if !errors.As(err, &failure) || !errors.Is(err, context.DeadlineExceeded) || failure.StatusCode != http.StatusOK {
		t.Fatalf("Fetch failure = %#v, want preserved local body timeout after upstream HTTP 200", err)
	}
	result := failureResult(err)
	if result.FailureDiagnostic == nil || result.FailureDiagnostic.Category != FailureCategoryTransportTimeout ||
		result.FailureDiagnostic.UpstreamStatus != http.StatusOK ||
		!strings.Contains(result.Message, "local LWC-to-DeepSeek Fetch transport timed out") ||
		!strings.Contains(result.Message, "upstream HTTP 200") || strings.Contains(result.Message, "HTTP 504") ||
		strings.Contains(result.Message, "downstream fetch timed out") {
		t.Fatalf("body timeout result = %#v", result)
	}
}

func TestImportStrictCommitsTheExactMarkdownThatWasAssessed(t *testing.T) {
	const sourceURL = "https://example.com/short-notice"
	const fetched = "\\# Office notice\n\nThe reading room opens at 10 AM.\n"
	var fetches atomic.Int32
	var evaluations atomic.Int32
	var assessed string
	service, store := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		fetches.Add(1)
		return Page{Title: "Office notice", Markdown: fetched}, nil
	}), evaluatorFunc(func(_ context.Context, input QualityInput) (QualityEvaluation, error) {
		evaluations.Add(1)
		if !input.RequireTitleBodyConsistency || input.Title != "Office notice" {
			t.Fatalf("strict assessment input = %#v", input)
		}
		assessed = input.Markdown
		return QualityEvaluation{ShellProbability: .02, ContentProbability: .98}, nil
	}))
	result, err := service.ImportStrict(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: sourceURL, Tags: []string{"notice"}})
	if err != nil || result.Status != "stored" || result.Storage == nil || result.Storage.Status != string(rawintake.StatusCreated) || result.ProcessingStatus != "not_started" {
		t.Fatalf("strict import = %#v err=%v", result, err)
	}
	if fetches.Load() != 1 || evaluations.Load() != 1 {
		t.Fatalf("fetches=%d evaluations=%d; assessment must use one fetch and one quality check", fetches.Load(), evaluations.Load())
	}
	stored, err := store.ReadFile(context.Background(), result.Storage.Path)
	metadata, body, hasFrontmatter, parseErr := parseRawFrontmatter(stored)
	if err != nil || parseErr != nil || !hasFrontmatter || string(body) != assessed || !strings.HasPrefix(assessed, "# Office notice") {
		t.Fatalf("assessed=%q stored body=%q err=%v parseErr=%v", assessed, body, err, parseErr)
	}
	if metadata["title"] != "Office notice" || metadata["source"] != sourceURL || !strings.Contains(string(stored), "notice") {
		t.Fatalf("stored metadata/content = %#v %q", metadata, stored)
	}
	if result.Storage.SHA256 != digestString(stored) {
		t.Fatalf("stored digest=%q actual=%q", result.Storage.SHA256, digestString(stored))
	}
	second, err := service.ImportStrict(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: sourceURL, Tags: []string{"notice"}})
	if err != nil || second.Status != "stored" || second.Storage == nil || second.Storage.Status != string(rawintake.StatusAlreadyExists) || second.Storage.Path != result.Storage.Path {
		t.Fatalf("same URL retry = %#v err=%v", second, err)
	}
}

func TestImportStrictRefusesUncertainTruncatedUnavailableAndMalformedQuality(t *testing.T) {
	tests := []struct {
		name       string
		page       Page
		evaluator  QualityEvaluator
		wantStatus string
		wantEval   int32
	}{
		{name: "ambiguous title body match", page: Page{Title: "Mars weather", Markdown: "Readable text about coastal tides."}, evaluator: evaluatorFunc(func(_ context.Context, input QualityInput) (QualityEvaluation, error) {
			if !input.RequireTitleBodyConsistency {
				t.Fatalf("strict title/body assessment missing: %#v", input)
			}
			return uncertainEvaluator(context.Background(), input)
		}), wantStatus: "rejected", wantEval: 1},
		{name: "blocking shell", page: Page{Title: "Sign in", Markdown: "Please sign in to continue."}, evaluator: evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
			return QualityEvaluation{ShellProbability: .99, ContentProbability: .01}, nil
		}), wantStatus: "rejected", wantEval: 1},
		{name: "provider truncation", page: Page{Title: "Partial", Markdown: "Readable but incomplete", Truncated: boolPtr(true)}, evaluator: evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
			t.Fatal("truncated content must be refused before quality evaluation")
			return QualityEvaluation{}, nil
		}), wantStatus: "rejected", wantEval: 0},
		{name: "missing evaluator", page: Page{Title: "Article", Markdown: "Readable body"}, evaluator: nil, wantStatus: "unavailable", wantEval: 0},
		{name: "quality timeout", page: Page{Title: "Article", Markdown: "Readable body"}, evaluator: evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
			return QualityEvaluation{}, &QualityProviderFailure{Kind: "timeout"}
		}), wantStatus: "unavailable", wantEval: 1},
		{name: "malformed numeric response", page: Page{Title: "Article", Markdown: "Readable body"}, evaluator: evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
			return QualityEvaluation{ShellProbability: math.NaN(), ContentProbability: .99}, nil
		}), wantStatus: "unavailable", wantEval: 1},
		{name: "legitimate short text", page: Page{Title: "Scheduled maintenance", Markdown: "The service will be offline Sunday from 2–3 AM."}, evaluator: evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
			return QualityEvaluation{ShellProbability: .01, ContentProbability: .99}, nil
		}), wantStatus: "stored", wantEval: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var evaluations atomic.Int32
			service, store := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) { return test.page, nil }), test.evaluator)
			if test.evaluator != nil {
				wrapped := test.evaluator
				service.Evaluator = evaluatorFunc(func(ctx context.Context, input QualityInput) (QualityEvaluation, error) {
					evaluations.Add(1)
					return wrapped.Evaluate(ctx, input)
				})
			}
			result, err := service.ImportStrict(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://example.com/case"})
			if err != nil || result.Status != test.wantStatus {
				t.Fatalf("strict result = %#v err=%v, want status %q", result, err, test.wantStatus)
			}
			if evaluations.Load() != test.wantEval {
				t.Fatalf("quality evaluations=%d, want %d", evaluations.Load(), test.wantEval)
			}
			files, err := markdownRawFiles(store)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantStatus == "stored" {
				if len(files) != 1 || result.Storage == nil {
					t.Fatalf("usable short text not stored: files=%#v result=%#v", files, result)
				}
			} else if len(files) != 0 || result.FailureStage != "quality" || result.Message == "" {
				t.Fatalf("refused result wrote raw or omitted its reason: files=%#v result=%#v", files, result)
			}
		})
	}
}

func TestServiceKeepsDistinctContentQueriesSeparate(t *testing.T) {
	fetcher := fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Shared Article", Markdown: "# Shared Article\n\nbody\n"}, nil
	})
	service, store := newTestService(t, fetcher, evaluatorFunc(usableEvaluator))
	for _, query := range []string{"one", "two"} {
		result, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://example.com/article?q=" + query})
		if err != nil || result.Status != "stored" {
			t.Fatalf("import query %q = %#v, %v", query, result, err)
		}
	}
	files, err := markdownRawFiles(store)
	if err != nil || len(files) != 2 || files[0].Path == files[1].Path {
		t.Fatalf("content queries merged: files=%#v err=%v", files, err)
	}
}

func TestServiceConfirmationCommitsExactCandidateWithoutRefetch(t *testing.T) {
	var fetches atomic.Int32
	fetcher := fetcherFunc(func(context.Context, string) (Page, error) {
		fetches.Add(1)
		return Page{Title: "Needs Review", Markdown: "# Needs Review\n\nExact candidate body."}, nil
	})
	service, store := newTestService(t, fetcher, evaluatorFunc(uncertainEvaluator))
	scope := ActorProject{"actor-1", "project-1"}
	preview, err := service.Import(context.Background(), scope, ImportRequest{URL: "https://example.com/review"})
	if err != nil || preview.Status != "needs_confirmation" || preview.ConfirmationRef == "" || preview.Preview == nil {
		t.Fatalf("preview = %#v, %v", preview, err)
	}
	files, err := markdownRawFiles(store)
	if err != nil || len(files) != 0 {
		t.Fatalf("uncertain candidate wrote raw data: files=%#v err=%v", files, err)
	}
	confirmed, err := service.Confirm(context.Background(), scope, preview.ConfirmationRef)
	if err != nil || confirmed.Status != "stored" || confirmed.Storage == nil {
		t.Fatalf("confirm = %#v, %v", confirmed, err)
	}
	if fetches.Load() != 1 {
		t.Fatalf("provider fetch count = %d, want exactly one", fetches.Load())
	}
	data, err := store.ReadFile(context.Background(), confirmed.Storage.Path)
	if err != nil || string(data) != preview.Preview.Markdown || digestString(data) != preview.Preview.CandidateSHA256 {
		t.Fatalf("confirmed bytes do not match reviewed candidate: err=%v", err)
	}
}

func TestPreparedCandidateIsDurableExactTitleBoundAndCrossInstance(t *testing.T) {
	root := t.TempDir()
	scope := ActorProject{"actor-1", "project-1"}
	var fetches atomic.Int32
	factory := func(scope ActorProject) (ProjectStore, error) {
		if strings.TrimSpace(scope.ActorID) == "" || strings.TrimSpace(scope.ProjectID) == "" {
			return nil, ErrInvalidScope
		}
		return localfs.New(root).WithScope(scope.ActorID, scope.ProjectID), nil
	}
	fetcher := fetcherFunc(func(context.Context, string) (Page, error) {
		fetches.Add(1)
		return Page{Title: "Fetched title", Markdown: "\\# Original heading\n\nKeep `\\# inline` and [this](https://example.com).\n"}, nil
	})
	serviceA := NewService(fetcher, evaluatorFunc(usableEvaluator), factory, DefaultQualityThresholds)
	serviceA.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	prepared, err := serviceA.Prepare(context.Background(), scope, ImportRequest{URL: "https://example.com/article"})
	if err != nil || prepared.Status != "preview" || prepared.CandidateID == "" || prepared.CandidateRevision == "" || prepared.Preview == nil {
		t.Fatalf("prepared = %#v err=%v", prepared, err)
	}
	if !prepared.Preview.QualityChecked || prepared.Preview.QualityStatus != "usable" {
		t.Fatalf("configured quality result = %#v, want quality_checked usable", prepared.Preview)
	}
	if !strings.HasPrefix(prepared.Preview.Markdown, "# Original heading") || !strings.Contains(prepared.Preview.Markdown, "`\\# inline`") || !strings.Contains(prepared.Preview.Markdown, "[this](https://example.com)") {
		t.Fatalf("normalized preview lost content/intentional escapes: %q", prepared.Preview.Markdown)
	}
	files, err := markdownRawFiles(localfs.New(root).WithScope(scope.ActorID, scope.ProjectID))
	if err != nil || len(files) != 0 {
		t.Fatalf("prepare wrote raw content: %#v err=%v", files, err)
	}

	serviceB := NewService(fetcher, evaluatorFunc(usableEvaluator), factory, DefaultQualityThresholds)
	serviceB.Now = serviceA.Now
	stored, err := serviceB.Accept(context.Background(), scope, prepared.CandidateID, prepared.CandidateRevision, "Edited safe title")
	if err != nil || stored.Status != "stored" || stored.Storage == nil {
		t.Fatalf("accept = %#v err=%v", stored, err)
	}
	if fetches.Load() != 1 {
		t.Fatalf("fetches=%d; accepting must not refetch", fetches.Load())
	}
	raw, err := localfs.New(root).WithScope(scope.ActorID, scope.ProjectID).ReadFile(context.Background(), stored.Storage.Path)
	if err != nil || digestString(raw) != stored.Storage.SHA256 || strings.Contains(string(raw), "Fetched title") || !strings.Contains(string(raw), "Edited safe title") || !strings.Contains(string(raw), "# Original heading") {
		t.Fatalf("saved bytes/title/digest mismatch: %v", err)
	}
	replayed, err := serviceA.Accept(context.Background(), scope, prepared.CandidateID, prepared.CandidateRevision, "Edited safe title")
	if err != nil || replayed.Storage == nil || replayed.Storage.Path != stored.Storage.Path || replayed.Storage.SHA256 != stored.Storage.SHA256 {
		t.Fatalf("duplicate accept did not return original durable result: %#v err=%v", replayed, err)
	}
	resolved, err := serviceA.Resolve(context.Background(), scope, prepared.CandidateID)
	if err != nil || resolved.Storage == nil || resolved.Storage.SHA256 != stored.Storage.SHA256 {
		t.Fatalf("resolve = %#v err=%v", resolved, err)
	}
	otherProject, err := serviceA.Resolve(context.Background(), ActorProject{"actor-1", "project-2"}, prepared.CandidateID)
	if err != nil || otherProject.Status != "rejected" {
		t.Fatalf("cross-Project candidate resolution = %#v err=%v", otherProject, err)
	}
}

func TestPreparedCandidateDeclineAcceptRaceHasOneTerminalOutcome(t *testing.T) {
	root := t.TempDir()
	factory := func(scope ActorProject) (ProjectStore, error) {
		return localfs.New(root).WithScope(scope.ActorID, scope.ProjectID), nil
	}
	scope := ActorProject{"actor-1", "project-1"}
	fetcher := fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Race", Markdown: "# Race\n\nCandidate body."}, nil
	})
	newService := func() *Service {
		service := NewService(fetcher, evaluatorFunc(usableEvaluator), factory, DefaultQualityThresholds)
		service.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
		return service
	}
	first, second := newService(), newService()
	prepared, err := first.Prepare(context.Background(), scope, ImportRequest{URL: "https://example.com/race"})
	if err != nil || prepared.CandidateID == "" {
		t.Fatalf("prepare = %#v err=%v", prepared, err)
	}
	start := make(chan struct{})
	results := make(chan Result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		result, _ := first.Accept(context.Background(), scope, prepared.CandidateID, prepared.CandidateRevision, "Race")
		results <- result
	}()
	go func() {
		defer wg.Done()
		<-start
		result, _ := second.Reject(context.Background(), scope, prepared.CandidateID, prepared.CandidateRevision)
		results <- result
	}()
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		if result.Status != "stored" && result.Status != "rejected" && result.Status != "uncertain" {
			t.Fatalf("race returned unexpected result: %#v", result)
		}
	}
	terminal, err := first.Resolve(context.Background(), scope, prepared.CandidateID)
	if err != nil || (terminal.Status != "stored" && terminal.Status != "rejected") {
		t.Fatalf("race terminal = %#v err=%v", terminal, err)
	}
	files, err := markdownRawFiles(localfs.New(root).WithScope(scope.ActorID, scope.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status == "stored" && len(files) != 1 {
		t.Fatalf("accepted race wrote %d raw files", len(files))
	}
	if terminal.Status == "rejected" && len(files) != 0 {
		t.Fatalf("declined race wrote raw files: %#v", files)
	}
}

func TestCandidateReceiptReadOutageRemainsUncertainAndRecoverable(t *testing.T) {
	root := t.TempDir()
	scope := ActorProject{"actor-1", "project-1"}
	rawStore := localfs.New(root).WithScope(scope.ActorID, scope.ProjectID)
	store := &candidateReadFaultStore{ProjectStore: rawStore}
	factory := func(got ActorProject) (ProjectStore, error) {
		if got != scope {
			return nil, ErrInvalidScope
		}
		return store, nil
	}
	var fetches atomic.Int32
	service := NewService(fetcherFunc(func(context.Context, string) (Page, error) {
		fetches.Add(1)
		return Page{Title: "Review", Markdown: "# Review\n\nExact candidate body."}, nil
	}), evaluatorFunc(usableEvaluator), factory, DefaultQualityThresholds)
	service.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}

	prepared, err := service.Prepare(context.Background(), scope, ImportRequest{URL: "https://example.com/read-failure-accept"})
	if err != nil || prepared.Status != "preview" {
		t.Fatalf("prepare = %#v err=%v", prepared, err)
	}
	store.failNextCandidateRead.Store(true)
	uncertain, err := service.Accept(context.Background(), scope, prepared.CandidateID, prepared.CandidateRevision, prepared.Preview.Title)
	if err != nil || uncertain.Status != "uncertain" || uncertain.CandidateID != prepared.CandidateID {
		t.Fatalf("accept read outage = %#v err=%v; want candidate-bound uncertain", uncertain, err)
	}
	files, err := markdownRawFiles(rawStore)
	if err != nil || len(files) != 0 {
		t.Fatalf("uncertain accept wrote raw: files=%#v err=%v", files, err)
	}
	resolvedPreview, err := service.Resolve(context.Background(), scope, prepared.CandidateID)
	if err != nil || resolvedPreview.Status != "preview" || resolvedPreview.CandidateID != prepared.CandidateID {
		t.Fatalf("recover prepared candidate = %#v err=%v", resolvedPreview, err)
	}
	stored, err := service.Accept(context.Background(), scope, prepared.CandidateID, prepared.CandidateRevision, prepared.Preview.Title)
	if err != nil || stored.Status != "stored" || stored.Storage == nil {
		t.Fatalf("accept after read recovery = %#v err=%v", stored, err)
	}
	store.failNextCandidateRead.Store(true)
	uncertain, err = service.Resolve(context.Background(), scope, prepared.CandidateID)
	if err != nil || uncertain.Status != "uncertain" || uncertain.CandidateID != prepared.CandidateID {
		t.Fatalf("resolve read outage = %#v err=%v; want candidate-bound uncertain", uncertain, err)
	}
	resolved, err := service.Resolve(context.Background(), scope, prepared.CandidateID)
	if err != nil || resolved.Status != "stored" || resolved.Storage == nil || resolved.Storage.Path != stored.Storage.Path || resolved.Storage.SHA256 != stored.Storage.SHA256 {
		t.Fatalf("recover stored candidate = %#v err=%v", resolved, err)
	}
	if fetches.Load() != 1 {
		t.Fatalf("provider fetch count=%d; status recovery must not refetch", fetches.Load())
	}

	declineCandidate, err := service.Prepare(context.Background(), scope, ImportRequest{URL: "https://example.com/read-failure-reject"})
	if err != nil || declineCandidate.Status != "preview" {
		t.Fatalf("prepare decline candidate = %#v err=%v", declineCandidate, err)
	}
	store.failNextCandidateRead.Store(true)
	uncertain, err = service.Reject(context.Background(), scope, declineCandidate.CandidateID, declineCandidate.CandidateRevision)
	if err != nil || uncertain.Status != "uncertain" || uncertain.CandidateID != declineCandidate.CandidateID {
		t.Fatalf("reject read outage = %#v err=%v; want candidate-bound uncertain", uncertain, err)
	}
	resolvedPreview, err = service.Resolve(context.Background(), scope, declineCandidate.CandidateID)
	if err != nil || resolvedPreview.Status != "preview" {
		t.Fatalf("recover candidate after reject read outage = %#v err=%v", resolvedPreview, err)
	}
	declined, err := service.Reject(context.Background(), scope, declineCandidate.CandidateID, declineCandidate.CandidateRevision)
	if err != nil || declined.Status != "rejected" {
		t.Fatalf("reject after read recovery = %#v err=%v", declined, err)
	}
	missing, err := service.Resolve(context.Background(), scope, strings.Repeat("a", 64))
	if err != nil || missing.Status != "rejected" {
		t.Fatalf("missing candidate = %#v err=%v; want definitive rejection", missing, err)
	}
	files, err = markdownRawFiles(rawStore)
	if err != nil || len(files) != 1 || files[0].Path != stored.Storage.Path {
		t.Fatalf("terminal candidate writes = %#v err=%v; want only accepted path %q", files, err, stored.Storage.Path)
	}
}

func TestPreparedURLFlowRefreshesStableIdentityAndNoOpsIdenticalBytes(t *testing.T) {
	root := t.TempDir()
	scope := ActorProject{"actor-1", "project-1"}
	storeFactory := func(scope ActorProject) (ProjectStore, error) {
		return localfs.New(root).WithScope(scope.ActorID, scope.ProjectID), nil
	}
	pages := []Page{{Title: "Stable URL", Markdown: "\\# Stable URL\n\nfirst body"}, {Title: "Stable URL", Markdown: "\\# Stable URL\n\nsecond body"}, {Title: "Stable URL", Markdown: "\\# Stable URL\n\nsecond body"}}
	var calls atomic.Int32
	fetcher := fetcherFunc(func(context.Context, string) (Page, error) { return pages[min(int(calls.Add(1))-1, len(pages)-1)], nil })
	service := NewService(fetcher, evaluatorFunc(usableEvaluator), storeFactory, DefaultQualityThresholds)
	service.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	accept := func() Result {
		prepared, err := service.Prepare(context.Background(), scope, ImportRequest{URL: "https://example.com/stable"})
		if err != nil || prepared.Status != "preview" {
			t.Fatalf("prepare = %#v err=%v", prepared, err)
		}
		stored, err := service.Accept(context.Background(), scope, prepared.CandidateID, prepared.CandidateRevision, prepared.Preview.Title)
		if err != nil || stored.Status != "stored" {
			t.Fatalf("accept = %#v err=%v", stored, err)
		}
		if stored.Storage == nil || stored.Storage.SHA256 != prepared.Preview.CandidateSHA256 {
			t.Fatalf("accepted bytes differ from preview candidate: %#v vs %#v", stored.Storage, prepared.Preview)
		}
		return stored
	}
	created := accept()
	refreshed := accept()
	unchanged := accept()
	if created.Storage.Status != "created" || refreshed.Storage.Status != "replaced" || unchanged.Storage.Status != "already_exists" || created.Storage.Path != refreshed.Storage.Path || refreshed.Storage.Path != unchanged.Storage.Path {
		t.Fatalf("prepared URL lifecycle create/refresh/no-op = %#v / %#v / %#v", created.Storage, refreshed.Storage, unchanged.Storage)
	}
	data, err := localfs.New(root).WithScope(scope.ActorID, scope.ProjectID).ReadFile(context.Background(), created.Storage.Path)
	if err != nil || !bytes.Contains(data, []byte("second body")) || bytes.Contains(data, []byte("first body")) {
		t.Fatalf("refreshed bytes=%q err=%v", data, err)
	}
}

func TestEditedTitleSizeLimitRejectsBeforeChangingExistingRaw(t *testing.T) {
	const sourceURL = "https://example.com/size-boundary"
	var pageBody string
	service, store := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "X", Markdown: pageBody}, nil
	}), evaluatorFunc(usableEvaluator))
	old := []byte("---\ntitle: Previous\nsource: " + sourceURL + "\ncreated: 2020-01-01\nprivate_note: " + strings.Repeat("m", 16*1024) + "\n---\nold body\n")
	relativePath := safeTitleFilename("Previous")
	if _, err := rawintake.Commit(context.Background(), store, relativePath, old, false); err != nil {
		t.Fatal(err)
	}
	identity := digestString([]byte(sourceURL))
	if _, err := reserveIdentity(context.Background(), store, identity, relativePath); err != nil {
		t.Fatal(err)
	}
	empty, _, err := MergeRawMarkdown(old, SourceMetadata{Title: "X", SourceURL: sourceURL}, "", service.now())
	if err != nil {
		t.Fatal(err)
	}
	pageBody = strings.Repeat("x", MaxCandidateBytes-len(empty)-20)
	prepared, err := service.Prepare(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: sourceURL})
	if err != nil || prepared.Status != "preview" || len(prepared.Preview.Markdown) == 0 {
		t.Fatalf("near-boundary prepare = %#v err=%v", prepared, err)
	}
	if len(prepared.Preview.Markdown)+len(empty) > MaxCandidateBytes {
		t.Fatalf("fixture candidate exceeds max: body=%d frontmatter=%d", len(prepared.Preview.Markdown), len(empty))
	}
	result, err := service.Accept(context.Background(), ActorProject{"actor-1", "project-1"}, prepared.CandidateID, prepared.CandidateRevision, strings.Repeat("T", 200))
	current, readErr := store.ReadFile(context.Background(), "raw/"+relativePath)
	if err != nil || result.Status != "rejected" || readErr != nil || !bytes.Equal(current, old) {
		t.Fatalf("oversize edited title result=%#v err=%v oldChanged=%t readErr=%v", result, err, !bytes.Equal(current, old), readErr)
	}
}

func TestServiceRejectsWrongScopeAndExpiredConfirmations(t *testing.T) {
	service, _ := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Review", Markdown: "Review body"}, nil
	}), evaluatorFunc(uncertainEvaluator))
	scope := ActorProject{"actor-1", "project-1"}
	preview, err := service.Import(context.Background(), scope, ImportRequest{URL: "https://example.com/review"})
	if err != nil || preview.Status != "needs_confirmation" {
		t.Fatalf("preview = %#v, %v", preview, err)
	}
	wrongActor, err := service.Confirm(context.Background(), ActorProject{"actor-2", "project-1"}, preview.ConfirmationRef)
	if err != nil || wrongActor.Status != "rejected" {
		t.Fatalf("wrong actor confirm = %#v, %v", wrongActor, err)
	}
	confirmed, err := service.Confirm(context.Background(), scope, preview.ConfirmationRef)
	if err != nil || confirmed.Status != "stored" {
		t.Fatalf("confirmation after wrong-scope attempt = %#v, %v", confirmed, err)
	}
	replayed, err := service.Confirm(context.Background(), scope, preview.ConfirmationRef)
	if err != nil || replayed.Status != "rejected" {
		t.Fatalf("replayed confirmation = %#v, %v", replayed, err)
	}

	expired, err := service.Import(context.Background(), scope, ImportRequest{URL: "https://example.com/expired"})
	if err != nil || expired.Status != "needs_confirmation" {
		t.Fatalf("second preview = %#v, %v", expired, err)
	}
	service.Now = func() time.Time { return time.Date(2026, 10, 21, 12, 16, 0, 0, time.UTC) }
	result, err := service.Confirm(context.Background(), scope, expired.ConfirmationRef)
	if err != nil || result.Status != "rejected" {
		t.Fatalf("expired confirmation = %#v, %v", result, err)
	}
}

func TestServiceRejectsChangedGenerationDuringConfirmation(t *testing.T) {
	var evaluations atomic.Int32
	evaluator := evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
		if evaluations.Add(1) == 1 {
			return usableEvaluator(context.Background(), QualityInput{})
		}
		return uncertainEvaluator(context.Background(), QualityInput{})
	})
	var pages atomic.Int32
	fetcher := fetcherFunc(func(context.Context, string) (Page, error) {
		if pages.Add(1) == 1 {
			return Page{Title: "Article", Markdown: "# Article\n\nfirst"}, nil
		}
		return Page{Title: "Article", Markdown: "# Article\n\nreview candidate"}, nil
	})
	service, store := newTestService(t, fetcher, evaluator)
	scope := ActorProject{"actor-1", "project-1"}
	first, err := service.Import(context.Background(), scope, ImportRequest{URL: "https://example.com/article"})
	if err != nil || first.Status != "stored" {
		t.Fatalf("initial import = %#v, %v", first, err)
	}
	preview, err := service.Import(context.Background(), scope, ImportRequest{URL: "https://example.com/article"})
	if err != nil || preview.Status != "needs_confirmation" {
		t.Fatalf("refresh preview = %#v, %v", preview, err)
	}
	external, _, err := MergeRawMarkdown([]byte(previewExistingBytes(t, store, first.Storage.Path)), SourceMetadata{Title: "External update"}, "external concurrent update", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawintake.Commit(context.Background(), store, strings.TrimPrefix(first.Storage.Path, "raw/"), external, true); err != nil {
		t.Fatal(err)
	}
	result, err := service.Confirm(context.Background(), scope, preview.ConfirmationRef)
	if err != nil || result.Status != "conflict" {
		t.Fatalf("stale confirmation = %#v, %v", result, err)
	}
	current, err := store.ReadFile(context.Background(), first.Storage.Path)
	if err != nil || !bytes.Contains(current, []byte("external concurrent update")) || bytes.Contains(current, []byte("review candidate")) {
		t.Fatalf("stale confirmation overwrote concurrent update: %v", err)
	}
}

func TestUncheckedCandidateConflictPreservesChangedRawBytes(t *testing.T) {
	var evaluations atomic.Int32
	evaluator := evaluatorFunc(func(ctx context.Context, input QualityInput) (QualityEvaluation, error) {
		if evaluations.Add(1) == 1 {
			return usableEvaluator(ctx, input)
		}
		return QualityEvaluation{}, errors.New("provider unavailable")
	})
	var pages atomic.Int32
	fetcher := fetcherFunc(func(context.Context, string) (Page, error) {
		pages.Add(1)
		return Page{Title: "Article", Markdown: "# Article\n\nreview candidate"}, nil
	})
	service, store := newTestService(t, fetcher, evaluator)
	scope := ActorProject{"actor-1", "project-1"}
	first, err := service.Import(context.Background(), scope, ImportRequest{URL: "https://example.com/article"})
	if err != nil || first.Status != "stored" || first.Storage == nil {
		t.Fatalf("initial import = %#v, %v", first, err)
	}
	preview, err := service.Prepare(context.Background(), scope, ImportRequest{URL: "https://example.com/article"})
	if err != nil || preview.Status != "preview" || preview.Preview == nil || preview.Preview.QualityStatus != "not_checked" {
		t.Fatalf("unchecked refresh preview = %#v, %v", preview, err)
	}

	external, _, err := MergeRawMarkdown([]byte(previewExistingBytes(t, store, first.Storage.Path)), SourceMetadata{Title: "External update"}, "external concurrent update", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawintake.Commit(context.Background(), store, strings.TrimPrefix(first.Storage.Path, "raw/"), external, true); err != nil {
		t.Fatal(err)
	}
	want, err := store.ReadFile(context.Background(), first.Storage.Path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Accept(context.Background(), scope, preview.CandidateID, preview.CandidateRevision, preview.Preview.Title)
	if err != nil || result.Status != "conflict" || !strings.Contains(result.Message, "no raw article was changed") {
		t.Fatalf("stale unchecked accept = %#v, %v", result, err)
	}
	got, err := store.ReadFile(context.Background(), first.Storage.Path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("stale unchecked accept changed current raw bytes: err=%v equal=%t", err, bytes.Equal(got, want))
	}
	if pages.Load() != 2 {
		t.Fatalf("page fetches=%d; accept must use the saved candidate without refetch", pages.Load())
	}
}

func previewExistingBytes(t *testing.T, store *localfs.Client, rawPath string) string {
	t.Helper()
	data, err := store.ReadFile(context.Background(), rawPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestServiceProviderOutageFallsBackToDurableUncheckedCandidate(t *testing.T) {
	service, store := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Partial", Markdown: "# Partial\n\nusable but incomplete", Truncated: boolPtr(true)}, nil
	}), evaluatorFunc(usableEvaluator))
	result, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://example.com/partial"})
	if err != nil || result.Status != "needs_confirmation" || result.Preview == nil || result.Preview.Warning == "" {
		t.Fatalf("truncated import = %#v, %v", result, err)
	}
	files, err := markdownRawFiles(store)
	if err != nil || len(files) != 0 {
		t.Fatalf("truncated content wrote raw data: %#v, %v", files, err)
	}

	var fetches atomic.Int32
	service, store = newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		fetches.Add(1)
		return Page{Title: "Page", Markdown: "Body"}, nil
	}), evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
		return QualityEvaluation{}, errors.New("provider offline")
	}))
	scope := ActorProject{"actor-1", "project-1"}
	result, err = service.Import(context.Background(), scope, ImportRequest{URL: "https://example.com/page"})
	files, listErr := markdownRawFiles(store)
	if err != nil || result.Status != "preview" || result.Preview == nil || result.Preview.QualityChecked || result.Preview.QualityStatus != "not_checked" || result.CandidateID == "" || listErr != nil || len(files) != 0 {
		t.Fatalf("Jev outage result=%#v err=%v files=%#v listErr=%v", result, err, files, listErr)
	}

	secondInstance := NewService(fetcherFunc(func(context.Context, string) (Page, error) {
		fetches.Add(1)
		return Page{}, errors.New("candidate acceptance must not refetch")
	}), evaluatorFunc(usableEvaluator), func(ActorProject) (ProjectStore, error) { return store, nil }, DefaultQualityThresholds)
	secondInstance.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	secondInstance.Now = service.Now
	saved, err := secondInstance.Accept(context.Background(), scope, result.CandidateID, result.CandidateRevision, result.Preview.Title)
	if err != nil || saved.Status != "stored" || saved.Preview == nil || saved.Preview.QualityChecked || saved.Preview.QualityStatus != "not_checked" || saved.Storage == nil {
		t.Fatalf("fallback acceptance = %#v err=%v", saved, err)
	}
	if fetches.Load() != 1 {
		t.Fatalf("fetches=%d; accepting the fallback candidate must not refetch", fetches.Load())
	}
	data, err := store.ReadFile(context.Background(), saved.Storage.Path)
	if err != nil || !strings.Contains(string(data), "Body") {
		t.Fatalf("accepted fallback content = %q err=%v", data, err)
	}
}

func TestQualityFallbackAndNonFallbackStorageFailuresReachOTelCollector(t *testing.T) {
	ctx := context.Background()
	previous := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(ctx)
	})

	scope := ActorProject{"actor-1", "project-1"}
	service, _ := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Article", Markdown: "Readable body."}, nil
	}), NewJevEvaluator(""))
	fallback, err := service.Prepare(ctx, scope, ImportRequest{URL: "https://example.com/article"})
	if err != nil || fallback.Status != "preview" || fallback.Preview == nil || fallback.Preview.QualityChecked || fallback.Preview.QualityStatus != "not_checked" {
		t.Fatalf("quality fallback = %#v err=%v", fallback, err)
	}

	root := t.TempDir()
	failingStore := candidateWriteFaultStore{ProjectStore: localfs.New(root).WithScope(scope.ActorID, scope.ProjectID)}
	failing := NewService(fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Article", Markdown: "Readable body."}, nil
	}), nil, func(ActorProject) (ProjectStore, error) { return failingStore, nil }, DefaultQualityThresholds)
	failing.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	failedCandidate, err := failing.Prepare(ctx, scope, ImportRequest{URL: "https://example.com/candidate-failure"})
	if err != nil || failedCandidate.Status != "unavailable" || failedCandidate.FailureStage != "candidate" {
		t.Fatalf("candidate storage failure = %#v err=%v", failedCandidate, err)
	}
	recordFetchFailure(ctx, &ProviderFailure{Provider: DeepSeekFetchProvider, Kind: FailureTooLarge}, &FailureDiagnostic{
		Dependency: DeepSeekFetchProvider, Operation: "fetch", Category: FailureCategoryContentTooLarge,
	})

	var data metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, scopeMetrics := range data.ScopeMetrics {
		for _, metric := range scopeMetrics.Metrics {
			if metric.Name != "lwc.url_intake.internal_events" {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("URL intake metric data = %T", metric.Data)
			}
			for _, point := range sum.DataPoints {
				stage, _ := point.Attributes.Value(attribute.Key("stage"))
				class, _ := point.Attributes.Value(attribute.Key("error_class"))
				outcome, _ := point.Attributes.Value(attribute.Key("outcome"))
				counts[stage.AsString()+"/"+class.AsString()+"/"+outcome.AsString()] += point.Value
			}
		}
	}
	if counts["quality/configuration/failure"] != 2 || counts["quality/configuration/fallback"] != 2 || counts["candidate/storage/failure"] != 1 {
		t.Fatalf("URL intake internal events = %#v", counts)
	}
}

func TestServiceRejectsMergedCandidateOverSizeBeforeCreate(t *testing.T) {
	largeMarkdown := strings.Repeat("x", MaxCandidateBytes-32)
	service, store := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Near Limit", Markdown: largeMarkdown}, nil
	}), evaluatorFunc(usableEvaluator))
	if len(largeMarkdown) > MaxCandidateBytes {
		t.Fatal("create fixture exceeds the Fetch candidate limit")
	}
	result, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://example.com/near-limit"})
	files, listErr := markdownRawFiles(store)
	if err != nil || result.Status != "rejected" || listErr != nil || len(files) != 0 {
		t.Fatalf("near-limit create = %#v, %v; raw files=%#v listErr=%v", result, err, files, listErr)
	}
}

func TestServiceRejectsOversizeRefreshPreservingExistingRaw(t *testing.T) {
	const sourceURL = "https://example.com/large-refresh"
	service, store := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Large Refresh", Markdown: strings.Repeat("n", MaxCandidateBytes-1024)}, nil
	}), evaluatorFunc(usableEvaluator))
	old := []byte("---\ntitle: Large Refresh\nsource: " + sourceURL + "\ncreated: 2020-01-01\nprivate_note: " + strings.Repeat("m", 16*1024) + "\n---\nold body\n")
	if len(old) >= MaxCandidateBytes {
		t.Fatal("refresh fixture exceeds the persistence limit")
	}
	relativePath := safeTitleFilename("Large Refresh")
	if _, err := rawintake.Commit(context.Background(), store, relativePath, old, false); err != nil {
		t.Fatal(err)
	}
	identity := digestString([]byte(sourceURL))
	if _, err := reserveIdentity(context.Background(), store, identity, relativePath); err != nil {
		t.Fatal(err)
	}
	pageMarkdown := strings.Repeat("n", MaxCandidateBytes-1024)
	merged, _, err := MergeRawMarkdown(old, SourceMetadata{Title: "Large Refresh", SourceURL: sourceURL}, pageMarkdown, service.now())
	if err != nil || len(pageMarkdown) > MaxCandidateBytes || len(merged) <= MaxCandidateBytes {
		t.Fatalf("refresh fixture lengths: markdown=%d merged=%d err=%v", len(pageMarkdown), len(merged), err)
	}
	result, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: sourceURL})
	current, readErr := store.ReadFile(context.Background(), "raw/"+relativePath)
	if err != nil || result.Status != "rejected" || readErr != nil || !bytes.Equal(current, old) {
		t.Fatalf("oversize refresh = %#v, %v; prior raw changed=%t readErr=%v", result, err, !bytes.Equal(current, old), readErr)
	}
}

func TestServiceRejectsEmptyBodyAndClearlyClassifiedShellWithoutWriting(t *testing.T) {
	service, store := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Empty", Markdown: "  \n"}, nil
	}), evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
		t.Fatal("empty body must be rejected before Jev evaluation")
		return QualityEvaluation{}, nil
	}))
	result, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://example.com/empty"})
	files, listErr := markdownRawFiles(store)
	if err != nil || result.Status != "rejected" || listErr != nil || len(files) != 0 {
		t.Fatalf("empty page result=%#v err=%v files=%#v listErr=%v", result, err, files, listErr)
	}

	service, store = newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Sign in", Markdown: "Sign in to continue"}, nil
	}), evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
		return QualityEvaluation{ShellProbability: .99, ContentProbability: .01}, nil
	}))
	result, err = service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://example.com/login"})
	files, listErr = markdownRawFiles(store)
	if err != nil || result.Status != "rejected" || listErr != nil || len(files) != 0 {
		t.Fatalf("classified shell result=%#v err=%v files=%#v listErr=%v", result, err, files, listErr)
	}
}

func TestServiceRejectsUnsafeURLBeforeProviderCall(t *testing.T) {
	var fetches atomic.Int32
	service, _ := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		fetches.Add(1)
		return Page{}, nil
	}), evaluatorFunc(usableEvaluator))
	service.Resolver = resolverStub{netip.MustParseAddr("10.0.0.1")}
	result, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://internal.example/page"})
	if err != nil || result.Status != "rejected" || fetches.Load() != 0 {
		t.Fatalf("unsafe URL result=%#v err=%v fetches=%d", result, err, fetches.Load())
	}
}

func TestServiceResolvesConcurrentSameURLToOnePath(t *testing.T) {
	service, store := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Concurrent", Markdown: "# Concurrent\n\nbody"}, nil
	}), evaluatorFunc(usableEvaluator))
	start := make(chan struct{})
	results := make(chan Result, 2)
	errorsOut := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			result, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://example.com/concurrent"})
			results <- result
			errorsOut <- err
		}()
	}
	close(start)
	var resultsGot []Result
	for range 2 {
		result := <-results
		err := <-errorsOut
		if err != nil {
			t.Fatal(err)
		}
		resultsGot = append(resultsGot, result)
	}
	files, err := markdownRawFiles(store)
	if err != nil || len(files) != 1 {
		t.Fatalf("concurrent raw files = %#v, %v", files, err)
	}
	created, alreadyExists, conflicts := 0, 0, 0
	var path, digest string
	for _, result := range resultsGot {
		switch result.Status {
		case "stored":
			if result.Storage == nil {
				t.Fatalf("stored concurrent result has no storage outcome: %#v", result)
			}
			if path == "" {
				path, digest = result.Storage.Path, result.Storage.SHA256
			} else if result.Storage.Path != path || result.Storage.SHA256 != digest {
				t.Fatalf("concurrent imports diverged: %#v", resultsGot)
			}
			switch result.Storage.Status {
			case string(rawintake.StatusCreated):
				created++
			case string(rawintake.StatusAlreadyExists):
				alreadyExists++
			default:
				t.Fatalf("unexpected concurrent storage outcome: %#v", result.Storage)
			}
		case "conflict":
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %#v", result)
		}
	}
	if created != 1 || (alreadyExists != 1 && conflicts != 1) || alreadyExists+conflicts != 1 {
		t.Fatalf("concurrent results = %#v; want one create and one identical no-op or conflict", resultsGot)
	}
	if path != files[0].Path {
		t.Fatalf("concurrent path = %q, raw file = %q", path, files[0].Path)
	}
}

func TestServiceReportsAmbiguousLegacyIdentityWithoutWriting(t *testing.T) {
	store := localfs.New(t.TempDir()).WithScope("actor-1", "project-1")
	first := []byte("---\ntitle: first\nsource: https://example.com/article?utm_source=one\ncreated: 2020-01-01\n---\nfirst")
	second := []byte("---\ntitle: second\nsource: https://example.com/article?utm_source=two\ncreated: 2020-01-02\n---\nsecond")
	for name, data := range map[string][]byte{"first.md": first, "second.md": second} {
		if _, err := rawintake.Commit(context.Background(), store, name, data, false); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "New", Markdown: "new"}, nil
	}), evaluatorFunc(usableEvaluator), func(ActorProject) (ProjectStore, error) { return store, nil }, DefaultQualityThresholds)
	service.Resolver = resolverStub{netip.MustParseAddr("93.184.216.34")}
	result, err := service.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://example.com/article"})
	if err != nil || result.Status != "conflict" {
		t.Fatalf("ambiguous legacy import = %#v, %v", result, err)
	}
	gotFirst, err := store.ReadFile(context.Background(), "raw/first.md")
	if err != nil || !bytes.Equal(gotFirst, first) {
		t.Fatalf("first legacy raw changed: %v", err)
	}
	gotSecond, err := store.ReadFile(context.Background(), "raw/second.md")
	if err != nil || !bytes.Equal(gotSecond, second) {
		t.Fatalf("second legacy raw changed: %v", err)
	}
}

func boolPtr(value bool) *bool { return &value }

func markdownRawFiles(store *localfs.Client) ([]storage.RawFile, error) {
	files, err := store.ListRawFiles(context.Background())
	if err != nil {
		return nil, err
	}
	filtered := make([]storage.RawFile, 0, len(files))
	for _, file := range files {
		if strings.HasSuffix(strings.ToLower(file.Path), ".md") {
			filtered = append(filtered, file)
		}
	}
	return filtered, nil
}
