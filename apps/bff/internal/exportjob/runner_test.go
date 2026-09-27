package exportjob

import (
	"bytes"
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	_ "modernc.org/sqlite"
)

func TestIncludeScopePathPreservesUserContentAndIncludesSanitizedConfig(t *testing.T) {
	for _, tc := range []struct {
		path  string
		scope Scope
		want  bool
	}{
		{"raw/input.md", ScopeRaw, true},
		{"wiki/.drafts/unpublished.md", ScopeRawFull, true},
		{"wiki/.drafts/unpublished.md", ScopeRaw, false},
		{"wiki/tokenization.md", ScopeRawFullMetadata, true},
		{"wiki/session-notes.md", ScopeRawFullMetadata, true},
		{".synto/state.db", ScopeRawFullMetadata, false}, // Rewritten through the sanitizer.
		{".olw/state.db", ScopeRawFullMetadata, false},
		{".synto/INDEX.json", ScopeRawFullMetadata, false}, // Read from the current generation.
		{"wiki.toml", ScopeRawFullMetadata, true},
		{"synto.toml", ScopeRawFullMetadata, true},
		{".lwc/publish/current.json", ScopeRawFullMetadata, true},
		{".lwc/publish/lease.json", ScopeRawFullMetadata, false},
		{"raw/provider-token.txt", ScopeRawFullMetadata, true},
		{"raw/service_account_key.json", ScopeRawFullMetadata, true},
		{"raw/.env", ScopeRawFullMetadata, true},
	} {
		if got := includeScopePath(tc.path, tc.scope); got != tc.want {
			t.Errorf("includeScopePath(%q, %q) = %v, want %v", tc.path, tc.scope, got, tc.want)
		}
	}
}

func TestCurrentGenerationSelectionUsesManifestAndMetadataAllowlist(t *testing.T) {
	for _, tc := range []struct {
		path  string
		scope Scope
		want  bool
	}{
		{"wiki/.drafts/manual.md", ScopeRawFull, true},
		{"wiki/manual-correction.md", ScopeRawFullMetadata, true},
		{"wiki.toml", ScopeRawFullMetadata, true},
		{"synto.toml", ScopeRawFullMetadata, true},
		{".synto/INDEX.json", ScopeRawFullMetadata, true},
		{".synto/state.db", ScopeRawFullMetadata, true},
		{"cache/id_map.json", ScopeRawFullMetadata, true},
		{"cache/suggested_queries.json", ScopeRawFullMetadata, true},
		{"cache/unrecognized.json", ScopeRawFullMetadata, false},
		{".synto/INDEX.json", ScopeRawFull, false},
	} {
		if got := includeGenerationPath(tc.path, tc.scope); got != tc.want {
			t.Errorf("includeGenerationPath(%q, %q) = %v, want %v", tc.path, tc.scope, got, tc.want)
		}
	}
}

func TestSanitizedSQLiteFileDoesNotRetainSecretBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	marker := []byte("LWC346_DELETE_ME_sqlite_provider_marker_4815162342")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE oauth_sessions (id TEXT, token TEXT); INSERT INTO oauth_sessions VALUES ('s1', '" + string(marker) + "')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(before, marker) {
		t.Fatalf("fixture marker not present in source SQLite bytes: present=%v err=%v", bytes.Contains(before, marker), err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := redactSQLiteSecrets(db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(after, marker) {
		t.Fatal("sanitized SQLite file still contains deleted secret marker bytes")
	}
}

func TestRedactSQLiteSecretsRemovesSecretTablesAndColumns(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE project_state (id INTEGER PRIMARY KEY, title TEXT, api_key TEXT NOT NULL, settings_json TEXT); INSERT INTO project_state VALUES (1, 'keep', 'provider-secret', '{\"label\":\"keep\",\"oauth\":{\"refresh_token\":\"session-secret\"}}'); CREATE TABLE oauth_sessions (id TEXT, token TEXT); INSERT INTO oauth_sessions VALUES ('s1', 'session-secret'); CREATE TABLE app_settings (name TEXT PRIMARY KEY, value TEXT); INSERT INTO app_settings VALUES ('project_title', 'keep'), ('deepseek_api_key', 'provider-secret')"); err != nil {
		t.Fatal(err)
	}
	if err := redactSQLiteSecrets(db); err != nil {
		t.Fatal(err)
	}
	var title string
	var apiKey *string
	if err := db.QueryRow("SELECT title, api_key FROM project_state WHERE id=1").Scan(&title, &apiKey); err != nil {
		t.Fatal(err)
	}
	if title != "keep" || apiKey == nil || *apiKey != "" {
		t.Fatalf("sanitized project state=(%q,%v)", title, apiKey)
	}
	var settings string
	if err := db.QueryRow("SELECT settings_json FROM project_state WHERE id=1").Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if settings != `{"label":"keep"}` {
		t.Fatalf("sanitized settings JSON = %s", settings)
	}
	var remaining int
	if err := db.QueryRow("SELECT count(*) FROM oauth_sessions").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("secret-bearing OAuth table remains in exported state")
	}
	var keptValue string
	if err := db.QueryRow("SELECT value FROM app_settings WHERE name='project_title'").Scan(&keptValue); err != nil || keptValue != "keep" {
		t.Fatalf("ordinary project setting = %q, %v", keptValue, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM app_settings WHERE name='deepseek_api_key'").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("secret setting remains in exported state: count=%d err=%v", remaining, err)
	}
}

func TestCloudRunStarterSendsOnlyBoundedJobIdentity(t *testing.T) {
	starter := NewCloudRunJobStarter("https://run.googleapis.com/v2/projects/p/locations/r/jobs/export:run")
	starter.tokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-access"})
	starter.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer test-access" {
			t.Fatalf("job request method/auth = %s / %q", request.Method, request.Header.Get("Authorization"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{`"name":"EXPORT_USER_ID","value":"user-1"`, `"name":"EXPORT_PROJECT_ID","value":"project-1"`, `"name":"EXPORT_ID","value":"export-1"`, `"name":"EXPORT_SCOPE","value":"raw-full"`} {
			if !strings.Contains(string(body), expected) {
				t.Fatalf("job overrides missing %s: %s", expected, body)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	if err := starter.Start(t.Context(), "user-1", "project-1", Job{ExportID: "export-1", Scope: ScopeRawFull}); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestRedactValueFiltersNestedAuthorizationFields(t *testing.T) {
	got := redactValue(map[string]any{"display": "preserved", "token": "secret", "profile": map[string]any{"refresh_token": "secret", "title": "kept"}}).(map[string]any)
	if got["token"] != nil || got["display"] != "preserved" {
		t.Fatalf("redacted project value = %#v", got)
	}
	profile := got["profile"].(map[string]any)
	if profile["refresh_token"] != nil || profile["title"] != "kept" {
		t.Fatalf("redacted nested profile = %#v", profile)
	}
}

func TestSafeContentDispositionPreservesUnicodeFilename(t *testing.T) {
	filename := "專案_raw_20260924.zip"
	header := safeContentDisposition(filename)
	if !strings.Contains(header, `filename*=UTF-8''%E5%B0%88%E6%A1%88_raw_20260924.zip`) || strings.ContainsAny(header, "\r\n") {
		t.Fatalf("Content-Disposition = %q", header)
	}
}
