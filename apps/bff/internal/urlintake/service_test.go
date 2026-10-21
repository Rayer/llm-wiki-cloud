package urlintake

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
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
