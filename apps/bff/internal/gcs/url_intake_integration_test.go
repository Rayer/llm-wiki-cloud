package gcs

import (
	"bytes"
	cloudstorage "cloud.google.com/go/storage"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/rawintake"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/urlintake"
)

type urlIntakeFixtureFetcher struct {
	mu    sync.Mutex
	pages []urlintake.Page
	calls int
}

func (f *urlIntakeFixtureFetcher) Fetch(context.Context, string) (urlintake.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := f.calls
	f.calls++
	if index >= len(f.pages) {
		index = len(f.pages) - 1
	}
	return f.pages[index], nil
}

type urlIntakeFixtureEvaluator struct{}

func (urlIntakeFixtureEvaluator) Evaluate(context.Context, urlintake.QualityInput) (urlintake.QualityEvaluation, error) {
	return urlintake.QualityEvaluation{ShellProbability: 0.01, ContentProbability: 0.99}, nil
}

type urlIntakeFixtureResolver struct{}

func (urlIntakeFixtureResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func newGCSURLIntakeService(client *Client, fetcher urlintake.Fetcher) *urlintake.Service {
	service := urlintake.NewService(fetcher, urlIntakeFixtureEvaluator{}, func(scope urlintake.ActorProject) (urlintake.ProjectStore, error) {
		return client.WithScope(scope.ActorID, scope.ProjectID), nil
	}, urlintake.DefaultQualityThresholds)
	service.Resolver = urlIntakeFixtureResolver{}
	service.Now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	return service
}

func TestURLIntakeGCSAdapterIdentityLifecycleAndProjectIsolation(t *testing.T) {
	client, _ := newMemoryClient()
	fetcher := &urlIntakeFixtureFetcher{pages: []urlintake.Page{
		{Title: "GCS Article", Markdown: "# GCS Article\n\nFirst body."},
		{Title: "GCS Article", Markdown: "# GCS Article\n\nUpdated body."},
		{Title: "GCS Article", Markdown: "# GCS Article\n\nUpdated body."},
		{Title: "GCS Article", Markdown: "# GCS Article\n\nProject B body."},
	}}
	service := newGCSURLIntakeService(client, fetcher)
	ctx := context.Background()
	scopeA := urlintake.ActorProject{ActorID: "user", ProjectID: "project-a"}
	firstURL := "https://example.com/article?utm_source=mail&q=1"

	created, err := service.Import(ctx, scopeA, urlintake.ImportRequest{URL: firstURL, Tags: []string{"seed"}})
	if err != nil || created.Status != "stored" || created.Storage == nil || created.Storage.Status != string(rawintake.StatusCreated) {
		t.Fatalf("first GCS import = %#v, %v", created, err)
	}
	projectA := client.WithScope(scopeA.ActorID, scopeA.ProjectID)
	identityHash := fmt.Sprintf("%x", sha256.Sum256([]byte("https://example.com/article?q=1")))
	identityPath := "cache/raw_url_identity/v1/" + identityHash + ".json"
	identityData, _, err := projectA.ReadFileWithGeneration(ctx, identityPath)
	if err != nil || !containsJSONField(identityData, `"raw_path":"raw/GCS-Article.md"`) {
		t.Fatalf("GCS identity reservation = %s, %v", identityData, err)
	}

	refreshed, err := service.Import(ctx, scopeA, urlintake.ImportRequest{URL: "https://EXAMPLE.com/article?q=1"})
	if err != nil || refreshed.Status != "stored" || refreshed.Storage == nil || refreshed.Storage.Status != string(rawintake.StatusReplaced) || refreshed.Storage.Path != created.Storage.Path {
		t.Fatalf("GCS refresh = %#v, %v; created = %#v", refreshed, err, created)
	}
	unchangedBytes, err := projectA.ReadFile(ctx, created.Storage.Path)
	if err != nil || !bytesContains(identityData, []byte(`"raw_path":"raw/GCS-Article.md"`)) || !bytesContains(unchangedBytes, []byte("Updated body.")) {
		t.Fatalf("refreshed GCS raw = %q, %v", unchangedBytes, err)
	}
	unchanged, err := service.Import(ctx, scopeA, urlintake.ImportRequest{URL: firstURL})
	if err != nil || unchanged.Status != "stored" || unchanged.Storage == nil || unchanged.Storage.Status != string(rawintake.StatusAlreadyExists) {
		t.Fatalf("identical GCS refresh = %#v, %v", unchanged, err)
	}

	scopeB := urlintake.ActorProject{ActorID: "user", ProjectID: "project-b"}
	createdB, err := service.Import(ctx, scopeB, urlintake.ImportRequest{URL: firstURL})
	if err != nil || createdB.Status != "stored" || createdB.Storage == nil || createdB.Storage.Status != string(rawintake.StatusCreated) {
		t.Fatalf("Project B import = %#v, %v", createdB, err)
	}
	projectB := client.WithScope(scopeB.ActorID, scopeB.ProjectID)
	if _, _, err := projectB.ReadFileWithGeneration(ctx, identityPath); err != nil {
		t.Fatalf("Project B identity is missing: %v", err)
	}
	projectBRaw, err := projectB.ReadFile(ctx, created.Storage.Path)
	if err != nil || !bytesContains(projectBRaw, []byte("Project B body")) || bytesContains(projectBRaw, []byte("Updated body.")) {
		t.Fatalf("Project B raw crossed scope: %q, %v", projectBRaw, err)
	}
}

func TestURLIntakeGCSAdapterConcurrentIdentityAndUnknownCacheDenial(t *testing.T) {
	client, _ := newMemoryClient()
	page := urlintake.Page{Title: "Concurrent GCS", Markdown: "# Concurrent GCS\n\nSame body."}
	service := newGCSURLIntakeService(client, &urlIntakeFixtureFetcher{pages: []urlintake.Page{page, page}})
	scope := urlintake.ActorProject{ActorID: "user", ProjectID: "project"}
	project := client.WithScope(scope.ActorID, scope.ProjectID)
	ctx := context.Background()

	results := make(chan urlintake.Result, 2)
	errorsOut := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			result, err := service.Import(ctx, scope, urlintake.ImportRequest{URL: "https://example.com/concurrent"})
			results <- result
			errorsOut <- err
		}()
	}
	close(start)
	var path, digest string
	created, alreadyExists, conflict := 0, 0, 0
	for range 2 {
		result := <-results
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
		switch result.Status {
		case "stored":
			if result.Storage == nil {
				t.Fatalf("stored GCS concurrent result lacks storage outcome: %#v", result)
			}
			if path == "" {
				path, digest = result.Storage.Path, result.Storage.SHA256
			} else if path != result.Storage.Path || digest != result.Storage.SHA256 {
				t.Fatalf("concurrent GCS imports diverged: %#v", result)
			}
			switch result.Storage.Status {
			case string(rawintake.StatusCreated):
				created++
			case string(rawintake.StatusAlreadyExists):
				alreadyExists++
			default:
				t.Fatalf("unexpected concurrent storage status: %#v", result.Storage)
			}
		case "conflict":
			conflict++
		default:
			t.Fatalf("unexpected concurrent GCS result: %#v", result)
		}
	}
	if created != 1 || alreadyExists+conflict != 1 || (alreadyExists != 1 && conflict != 1) {
		t.Fatalf("concurrent GCS results: created=%d already_exists=%d conflict=%d", created, alreadyExists, conflict)
	}
	files, err := project.ListRawFiles(ctx)
	if err != nil || len(files) != 1 || path != files[0].Path {
		t.Fatalf("concurrent GCS raw files = %#v, %v", files, err)
	}
	for _, unknown := range []string{"cache/other.json", "cache/raw_url_identity/v1/short.json", "cache/raw_url_identity/v1/../bad.json"} {
		if _, _, err := project.ReadFileWithGeneration(ctx, unknown); !errors.Is(err, storage.ErrDeclaredObjectUnavailable) {
			t.Errorf("unknown cache path %q error = %v, want ErrDeclaredObjectUnavailable", unknown, err)
		}
	}
}

func TestURLIntakeGCSAdapterDurableCandidateLifecycle(t *testing.T) {
	client, _ := newMemoryClient()
	ctx := context.Background()
	scope := urlintake.ActorProject{ActorID: "user", ProjectID: "candidate-project"}
	page := urlintake.Page{Title: "Candidate article", Markdown: "# Candidate article\n\nExact candidate body."}
	service := newGCSURLIntakeService(client, &urlIntakeFixtureFetcher{pages: []urlintake.Page{page}})
	prepared, err := service.Prepare(ctx, scope, urlintake.ImportRequest{URL: "https://example.com/candidate"})
	if err != nil || prepared.Status != "preview" || prepared.CandidateID == "" || prepared.CandidateRevision == "" {
		t.Fatalf("GCS prepare = %#v err=%v", prepared, err)
	}
	project := client.WithScope(scope.ActorID, scope.ProjectID)
	candidatePath := "cache/raw_url_candidate/v1/" + prepared.CandidateID + ".json"
	if _, _, err := project.ReadFileWithGeneration(ctx, candidatePath); err != nil {
		t.Fatalf("candidate receipt is not readable through GCS adapter: %v", err)
	}
	if _, err := project.ListRawFiles(ctx); err != nil {
		t.Fatal(err)
	}
	files, err := project.ListRawFiles(ctx)
	if err != nil || len(files) != 0 {
		t.Fatalf("prepare wrote raw files: %#v err=%v", files, err)
	}
	secondService := newGCSURLIntakeService(client, &urlIntakeFixtureFetcher{pages: []urlintake.Page{page}})
	stored, err := secondService.Accept(ctx, scope, prepared.CandidateID, prepared.CandidateRevision, "Reviewed candidate")
	if err != nil || stored.Status != "stored" || stored.Storage == nil || stored.Storage.SHA256 != prepared.Preview.CandidateSHA256 {
		// The title edit changes frontmatter and therefore the final digest.
		if err != nil || stored.Status != "stored" || stored.Storage == nil {
			t.Fatalf("GCS accept = %#v err=%v", stored, err)
		}
	}
	raw, err := project.ReadFile(ctx, stored.Storage.Path)
	if err != nil || !bytesContains(raw, []byte("Reviewed candidate")) || !bytesContains(raw, []byte("Exact candidate body.")) {
		t.Fatalf("GCS accepted candidate bytes = %q err=%v", raw, err)
	}
	other := client.WithScope(scope.ActorID, "other-project")
	if _, err := secondService.Resolve(ctx, urlintake.ActorProject{ActorID: scope.ActorID, ProjectID: "other-project"}, prepared.CandidateID); err != nil {
		t.Fatalf("cross-Project lookup returned an internal error: %v", err)
	} else if _, _, err := other.ReadFileWithGeneration(ctx, candidatePath); !errors.Is(err, cloudstorage.ErrObjectNotExist) {
		t.Fatalf("candidate receipt crossed project scope: %v", err)
	}
	if _, _, err := project.ReadFileWithGeneration(ctx, "cache/raw_url_candidate/v1/not-a-digest.json"); !errors.Is(err, storage.ErrDeclaredObjectUnavailable) {
		t.Fatalf("unknown candidate cache key was not denied: %v", err)
	}
	if _, err := project.WriteFileIfGeneration(ctx, []byte("unapproved"), "cache/raw_url_candidate/v1/not-a-digest.json", 0); !errors.Is(err, storage.ErrDeclaredObjectUnavailable) {
		t.Fatalf("unknown candidate cache key write was not denied: %v", err)
	}
}

func containsJSONField(data []byte, fragment string) bool {
	return bytesContains(data, []byte(fragment))
}

func bytesContains(data, fragment []byte) bool {
	return bytes.Contains(data, fragment)
}
