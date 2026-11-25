package v1

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
)

func writeConceptConflictStateDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".synto"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, ".synto", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE wiki_articles(path TEXT PRIMARY KEY,status TEXT,content_hash TEXT,article_id TEXT,entity_id TEXT)`,
		`CREATE TABLE lwc341_conflicts(conflict_id TEXT PRIMARY KEY,first_seen_run TEXT,status TEXT)`,
		`CREATE TABLE lwc341_run_state(singleton INTEGER PRIMARY KEY,run_id TEXT,new_conflicts_json TEXT,pending_conflicts_json TEXT,min_confidence REAL)`,
		`CREATE TABLE lwc341_operations(principal_id TEXT,operation_id TEXT,payload_sha256 TEXT,result_json TEXT,created_at TEXT,PRIMARY KEY(principal_id,operation_id))`,
		`CREATE TABLE lwc341_operation_attempts(principal_id TEXT,operation_id TEXT,payload_sha256 TEXT,result_json TEXT,created_at TEXT,PRIMARY KEY(principal_id,operation_id,payload_sha256))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func TestConceptConflictInventoryAndCompareAreReadOnly(t *testing.T) {
	root := t.TempDir()
	base := localfs.New(root)
	scoped := base.Scope("user", "project")
	ctx := context.Background()
	published := []byte("---\ntitle: published\n---\nOriginal body\n")
	candidate := []byte("---\ntitle: candidate\n---\nCandidate body\n")
	nonConflict := []byte("---\ntitle: other\n---\nOther draft\n")
	for path, data := range map[string][]byte{
		"wiki/alpha.md":         published,
		"wiki/.drafts/alpha.md": candidate,
		"wiki/.drafts/other.md": nonConflict,
	} {
		if _, err := scoped.WriteBytes(ctx, data, path); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}

	listed, err := listConceptConflicts(ctx, "project", scoped)
	if err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || len(listed.Conflicts) != 1 {
		t.Fatalf("inventory = %+v, want only the same-path pair", listed)
	}
	entry := listed.Conflicts[0]
	if entry.Published.Path != "wiki/alpha.md" || entry.Published.SHA256 != conceptConflictHash(published) {
		t.Fatalf("published identity = %+v", entry.Published)
	}
	if entry.Candidate.Path != "wiki/.drafts/alpha.md" || entry.Candidate.SHA256 != conceptConflictHash(candidate) {
		t.Fatalf("candidate identity = %+v", entry.Candidate)
	}

	compared, err := compareConceptConflict(ctx, "project", scoped, "wiki/alpha.md")
	if err != nil {
		t.Fatal(err)
	}
	if compared.Published.Content != string(published) || compared.Candidate.Content != string(candidate) {
		t.Fatalf("comparison contents do not match fixture: %+v", compared)
	}
	if compared.PairSHA256 == "" || listed.PairSHA256 == "" {
		t.Fatal("comparison and inventory must identify the observed byte pair")
	}
	for path, want := range map[string][]byte{
		"wiki/alpha.md":         published,
		"wiki/.drafts/alpha.md": candidate,
	} {
		got, err := scoped.ReadFile(ctx, path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("read-only API helper changed %s: err=%v got=%q", path, err, got)
		}
	}
}

func TestConceptConflictCompareSupportsNestedRelativePath(t *testing.T) {
	scoped := localfs.New(t.TempDir()).Scope("user", "project")
	published := []byte("Published nested page")
	candidate := []byte("Candidate nested page")
	for path, data := range map[string][]byte{
		"wiki/guides/alpha.md":         published,
		"wiki/.drafts/guides/alpha.md": candidate,
	} {
		if _, err := scoped.WriteBytes(context.Background(), data, path); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	got, err := compareConceptConflict(context.Background(), "project", scoped, "wiki/guides/alpha.md")
	if err != nil || got.Published.Content != string(published) || got.Candidate.Content != string(candidate) {
		t.Fatalf("nested compare=%+v err=%v", got, err)
	}
}

func TestNormalizePublishedConceptPathRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{
		"", "wiki/../raw.md", "../wiki/alpha.md", "/wiki/alpha.md",
		"wiki/.drafts/alpha.md", "wiki/alpha\\beta.md", "wiki/alpha.txt", "wiki//alpha.md",
	} {
		if normalized, ok := normalizePublishedConceptPath(path); ok {
			t.Errorf("accepted unsafe path %q as %q", path, normalized)
		}
	}
}

func TestConceptConflictCompareFailsClosedWithoutProjectReadAuthorization(t *testing.T) {
	root := t.TempDir()
	secret := []byte("private candidate")
	file := filepath.Join(root, "users", "user", "projects", "project", "wiki/.drafts/alpha.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, secret, 0o600); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	h := New(localfs.New(root), nil, nil, nil, nil, nil)

	for _, tc := range []struct {
		name      string
		userID    string
		projectID string
		status    int
	}{
		{name: "unauthenticated", projectID: "project", status: http.StatusUnauthorized},
		{name: "project-read-authority-unavailable", userID: "user", projectID: "project", status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/concept-conflicts/compare?path=wiki/alpha.md", nil)
			c.Set("userID", tc.userID)
			c.Set("projectID", tc.projectID)
			h.CompareConceptConflict(c)
			if recorder.Code != tc.status {
				t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), tc.status)
			}
			if recorder.Body.Len() > 0 && bytes.Contains(recorder.Body.Bytes(), secret) {
				t.Fatal("unauthorized comparison response exposed candidate content")
			}
		})
	}
}

func TestConceptConflictInventoryKeepsNewPendingAndHeldBackDistinct(t *testing.T) {
	root := t.TempDir()
	scoped := localfs.New(root).Scope("user", "project")
	ctx := context.Background()
	files := map[string][]byte{
		"wiki/alpha.md":         []byte("---\ntitle: alpha\n---\nPublished alpha\n"),
		"wiki/.drafts/alpha.md": []byte("---\ntitle: alpha\nconfidence: 0.9\n---\nCandidate alpha\n"),
		"wiki/beta.md":          []byte("---\ntitle: beta\n---\nPublished beta\n"),
		"wiki/.drafts/beta.md":  []byte("---\ntitle: beta\nconfidence: 0.2\n---\nCandidate beta\n"),
		".synto/INDEX.json":     []byte(`{"articles":[]}`),
	}
	for path, content := range files {
		if _, err := scoped.WriteBytes(ctx, content, path); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	db := writeConceptConflictStateDB(t, filepath.Join(root, "users", "user", "projects", "project"))
	defer db.Close()
	for path, identity := range map[string]conceptArticleIdentity{
		"wiki/alpha.md":         {Exists: true, Status: "published", ContentHash: "body-alpha", ArticleID: "article-alpha", EntityID: "entity-alpha"},
		"wiki/.drafts/alpha.md": {Exists: true, Status: "draft", ContentHash: "body-candidate", ArticleID: "candidate-alpha", EntityID: "candidate-entity-alpha"},
		"wiki/beta.md":          {Exists: true, Status: "published", ContentHash: "body-beta", ArticleID: "article-beta", EntityID: "entity-beta"},
		"wiki/.drafts/beta.md":  {Exists: true, Status: "draft", ContentHash: "body-candidate-beta", ArticleID: "candidate-beta", EntityID: "candidate-entity-beta"},
	} {
		if _, err := db.Exec(`INSERT INTO wiki_articles(path,status,content_hash,article_id,entity_id) VALUES(?,?,?,?,?)`, path, identity.Status, identity.ContentHash, identity.ArticleID, identity.EntityID); err != nil {
			t.Fatal(err)
		}
	}
	alphaID := conceptConflictID("project", "wiki/alpha.md", conceptConflictHash(files["wiki/alpha.md"]), conceptConflictHash(files["wiki/.drafts/alpha.md"]))
	newIDs, _ := json.Marshal([]string{alphaID})
	if _, err := db.Exec(`INSERT INTO lwc341_conflicts(conflict_id,first_seen_run,status) VALUES(?,?,?)`, alphaID, "run-first", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO lwc341_run_state(singleton,run_id,new_conflicts_json,pending_conflicts_json,min_confidence) VALUES(1,?,?,?,?)`, "run-current", string(newIDs), string(newIDs), 0.7); err != nil {
		t.Fatal(err)
	}
	listed, err := listConceptConflicts(ctx, "project", scoped)
	if err != nil {
		t.Fatal(err)
	}
	if listed.NewCount != 1 || listed.PendingCount != 1 || listed.Count != 1 || listed.HeldBackCount != 1 || len(listed.HeldBack) != 1 {
		t.Fatalf("inventory new=%d pending=%d held=%d; want 1,1,1: %+v", listed.NewCount, listed.PendingCount, listed.HeldBackCount, listed)
	}
	if listed.Conflicts[0].ConflictID != alphaID || listed.Conflicts[0].FirstSeenRun != "run-first" || !listed.Conflicts[0].IsNew {
		t.Fatalf("qualified conflict metadata = %+v", listed.Conflicts[0])
	}
	if listed.HeldBack[0].Eligible || listed.HeldBack[0].Confidence >= listed.HeldBack[0].MinConfidence {
		t.Fatalf("below-threshold pair was not held back: %+v", listed.HeldBack[0])
	}
	compared, err := compareConceptConflict(ctx, "project", scoped, "wiki/alpha.md")
	if err != nil || compared.Published.Identity.ArticleID != "article-alpha" || compared.Candidate.Identity.EntityID != "candidate-entity-alpha" {
		t.Fatalf("comparison identities = %+v, err=%v", compared, err)
	}
}

func TestConceptConflictReceiptLookupIsPrincipalScopedAndPayloadBound(t *testing.T) {
	root := t.TempDir()
	dbFile := writeConceptConflictStateDB(t, root)
	defer dbFile.Close()
	receipt := `{"status":"committed","action":"keep_published"}`
	attempt := `{"status":"stale","action":"publish_candidate"}`
	if _, err := dbFile.Exec(`INSERT INTO lwc341_operations VALUES(?,?,?,?,?)`, "owner", "operation-123", "payload-a", receipt, "2026-11-25T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := dbFile.Exec(`INSERT INTO lwc341_operation_attempts VALUES(?,?,?,?,?)`, "owner", "operation-456", "payload-b", attempt, "2026-11-25T00:00:01Z"); err != nil {
		t.Fatal(err)
	}
	state := &conceptConflictDatabase{DB: dbFile}
	if got, found, err := state.operationResult(context.Background(), "owner", "operation-123", "payload-a"); err != nil || !found || got["status"] != "committed" {
		t.Fatalf("receipt=%v found=%v err=%v", got, found, err)
	}
	if _, found, err := state.operationResult(context.Background(), "other", "operation-123", "payload-a"); err != nil || found {
		t.Fatalf("cross-principal receipt leaked: found=%v err=%v", found, err)
	}
	if found, err := state.hasOperationID(context.Background(), "owner", "operation-456"); err != nil || !found {
		t.Fatalf("attempt operation found=%v err=%v", found, err)
	}
	if found, err := state.hasOperationID(context.Background(), "other", "operation-456"); err != nil || found {
		t.Fatalf("cross-principal operation ID found=%v err=%v", found, err)
	}
}

func TestConceptConflictOperationFingerprintBindsDecisionAndSnapshot(t *testing.T) {
	request := conceptConflictResolveRequest{
		OperationID: "operation-123", Action: "keep_published", ConflictID: strings.Repeat("a", 64),
		Path: "wiki/alpha.md", StorageView: "manifest-42", PairSHA256: strings.Repeat("b", 64),
	}
	want := conceptConflictPayloadSHA256("owner", "project", request)
	if want == "" || want != conceptConflictPayloadSHA256("owner", "project", request) {
		t.Fatal("same decision fingerprint must be stable")
	}
	changed := request
	changed.Action = "publish_candidate"
	if got := conceptConflictPayloadSHA256("owner", "project", changed); got == want {
		t.Fatal("action change was not bound into operation payload")
	}
	changed = request
	changed.PairSHA256 = strings.Repeat("c", 64)
	if got := conceptConflictPayloadSHA256("owner", "project", changed); got == want {
		t.Fatal("snapshot change was not bound into operation payload")
	}
	if got := conceptConflictPayloadSHA256("other", "project", request); got == want {
		t.Fatal("principal change was not bound into operation payload")
	}
}
