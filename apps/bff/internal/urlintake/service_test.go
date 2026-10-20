package urlintake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/rawintake"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

type fetcherFunc func(context.Context, string) (Page, error)

func (f fetcherFunc) Fetch(ctx context.Context, url string) (Page, error) { return f(ctx, url) }

type evaluatorFunc func(context.Context, QualityInput) (QualityEvaluation, error)

func (f evaluatorFunc) Evaluate(ctx context.Context, input QualityInput) (QualityEvaluation, error) {
	return f(ctx, input)
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

func previewExistingBytes(t *testing.T, store *localfs.Client, rawPath string) string {
	t.Helper()
	data, err := store.ReadFile(context.Background(), rawPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestServiceProviderOutageAndExplicitTruncationNeverAutoSave(t *testing.T) {
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

	unavailable, unavailableStore := newTestService(t, fetcherFunc(func(context.Context, string) (Page, error) {
		return Page{Title: "Page", Markdown: "Body"}, nil
	}), evaluatorFunc(func(context.Context, QualityInput) (QualityEvaluation, error) {
		return QualityEvaluation{}, errors.New("provider offline")
	}))
	result, err = unavailable.Import(context.Background(), ActorProject{"actor-1", "project-1"}, ImportRequest{URL: "https://example.com/page"})
	files, listErr := markdownRawFiles(unavailableStore)
	if err != nil || result.Status != "unavailable" || listErr != nil || len(files) != 0 {
		t.Fatalf("Jev outage result=%#v err=%v files=%#v listErr=%v", result, err, files, listErr)
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
	var statuses []string
	for range 2 {
		result := <-results
		err := <-errorsOut
		if err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, result.Status)
	}
	files, err := markdownRawFiles(store)
	if err != nil || len(files) != 1 {
		t.Fatalf("concurrent raw files = %#v, %v", files, err)
	}
	if fmt.Sprint(statuses) != "[stored conflict]" && fmt.Sprint(statuses) != "[conflict stored]" {
		t.Fatalf("concurrent statuses = %v", statuses)
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
