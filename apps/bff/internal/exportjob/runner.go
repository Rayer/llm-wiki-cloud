package exportjob

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "modernc.org/sqlite"
)

const (
	maxExportObjects     = 20_000
	maxExportFiles       = 10_000
	maxExportSourceBytes = int64(10 << 30)
)

type exportSource struct {
	client *storage.Client
	bucket string
	fs     *firestore.Client
}

// RunExport claims one queued job, snapshots its project objects by GCS
// generation, streams the ZIP to GCS, verifies it, and only then makes it ready.
func RunExport(ctx context.Context, repo Repository, archives *CloudArchiveStore, userID, projectID string, job Job, fs *firestore.Client) error {
	if repo == nil || archives == nil {
		return errors.New("export worker unavailable")
	}
	if err := repo.MarkRunning(ctx, userID, projectID, job.ExportID); err != nil {
		return err
	}
	workCtx, cancel := context.WithTimeout(ctx, HardTimeout)
	defer cancel()
	source := exportSource{client: archives.client, bucket: archives.bucket, fs: fs}
	snapshotAt, files, err := source.files(workCtx, userID, projectID, job.Scope)
	if err == nil {
		var completed time.Time
		var size int64
		completed, size, err = archives.writeAndPublish(workCtx, userID, projectID, job, snapshotAt, files)
		if err == nil {
			err = repo.Complete(workCtx, userID, projectID, job.ExportID, snapshotAt, completed, size)
		}
	}
	if err != nil {
		failCtx, failCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer failCancel()
		_ = repo.Fail(failCtx, userID, projectID, job.ExportID, "worker_failure", "Export could not be completed. You can try again.")
		return err
	}
	return nil
}

func (s exportSource) files(ctx context.Context, userID, projectID string, scope Scope) (time.Time, []FileSource, error) {
	if !scope.Valid() || !validSegment(userID) || !validSegment(projectID) {
		return time.Time{}, nil, ErrInvalidPath
	}
	snapshotAt := time.Now().UTC()
	prefix := "users/" + userID + "/projects/" + projectID + "/"
	objects := make(map[string]*storage.ObjectAttrs)
	it := s.client.Bucket(s.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return time.Time{}, nil, err
		}
		if attrs.Name == prefix || attrs.Size < 0 || !safeArchivePath(strings.TrimPrefix(attrs.Name, prefix)) {
			continue
		}
		objects[strings.TrimPrefix(attrs.Name, prefix)] = attrs
		if len(objects) > maxExportObjects {
			return time.Time{}, nil, errors.New("project export exceeds the object listing limit")
		}
	}
	files := make([]FileSource, 0, len(objects))
	versions := map[string]string{}
	var current generation.Manifest
	manifestPresent := false
	if scope != ScopeRaw {
		if attrs := objects[generation.ManifestPath]; attrs != nil {
			if attrs.Size > generation.MaxManifestBytes {
				return time.Time{}, nil, errors.New("project generation manifest exceeds its limit")
			}
			data, err := readPinnedObject(ctx, s.client.Bucket(s.bucket).Object(attrs.Name).Generation(attrs.Generation), generation.MaxManifestBytes)
			if err != nil {
				return time.Time{}, nil, err
			}
			manifest, err := generation.Decode(data)
			if err != nil {
				return time.Time{}, nil, err
			}
			current, manifestPresent = manifest, true
			versions[generation.ManifestPath] = manifest.GenerationID + "@" + fmt.Sprint(attrs.Generation)
		}
	}
	add := func(relative string, attrs *storage.ObjectAttrs, version string) error {
		if !safeArchivePath(relative) {
			return ErrInvalidPath
		}
		object := s.client.Bucket(s.bucket).Object(attrs.Name).Generation(attrs.Generation)
		if version == "" {
			version = fmt.Sprint(attrs.Generation)
		}
		if isProjectTOMLPath(relative) {
			if attrs.Size > maxProjectTOMLBytes {
				return errors.New("project TOML exceeds its size limit")
			}
			data, err := readPinnedObject(ctx, object, maxProjectTOMLBytes)
			if err != nil {
				return fmt.Errorf("read project config %q: %w", relative, err)
			}
			sanitized, redacted, err := sanitizeProjectTOML(data, relative)
			if err != nil {
				return fmt.Errorf("sanitize project config %q: %w", relative, err)
			}
			files = append(files, FileSource{Path: relative, Version: version, RedactedFields: redacted, Open: func(context.Context) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(sanitized)), nil
			}})
			return nil
		}
		files = append(files, FileSource{Path: relative, Version: version, Open: func(ctx context.Context) (io.ReadCloser, error) {
			return object.NewReader(ctx)
		}})
		return nil
	}
	for relative, attrs := range objects {
		if strings.HasPrefix(relative, "raw/") {
			if err := add(relative, attrs, ""); err != nil {
				return time.Time{}, nil, err
			}
		}
	}
	if scope != ScopeRaw {
		if manifestPresent {
			for _, generationFile := range current.Files {
				if !includeGenerationPath(generationFile.Path, scope) {
					continue
				}
				name := prefix + current.ObjectPath(generationFile)
				object := s.client.Bucket(s.bucket).Object(name).Generation(generationFile.Generation)
				attrs, err := object.Attrs(ctx)
				if err != nil {
					return time.Time{}, nil, fmt.Errorf("read current project generation file %q: %w", generationFile.Path, err)
				}
				if attrs.Size != generationFile.Size || attrs.Generation != generationFile.Generation {
					return time.Time{}, nil, fmt.Errorf("current project generation file %q does not match its manifest", generationFile.Path)
				}
				version := current.GenerationID + "@" + fmt.Sprint(generationFile.Generation)
				if isStateDatabasePath(generationFile.Path) {
					files = append(files, sanitizedStateSource(object, generationFile.Path, version))
					continue
				}
				if err := add(generationFile.Path, attrs, version); err != nil {
					return time.Time{}, nil, err
				}
			}
		} else {
			// Before the first atomic publish, top-level wiki objects are canonical.
			for relative, attrs := range objects {
				if strings.HasPrefix(relative, "wiki/") {
					if err := add(relative, attrs, ""); err != nil {
						return time.Time{}, nil, err
					}
				} else if scope == ScopeRawFullMetadata && includeLegacyMetadataPath(relative) {
					if isStateDatabasePath(relative) {
						if attrs.Size > generation.MaxTotalSize {
							return time.Time{}, nil, errors.New("project state database exceeds its limit")
						}
						object := s.client.Bucket(s.bucket).Object(attrs.Name).Generation(attrs.Generation)
						files = append(files, sanitizedStateSource(object, relative, fmt.Sprint(attrs.Generation)))
					} else if err := add(relative, attrs, ""); err != nil {
						return time.Time{}, nil, err
					}
				}
			}
		}
	}
	if scope == ScopeRawFullMetadata {
		if attrs := objects[generation.ManifestPath]; attrs != nil {
			if err := add(generation.ManifestPath, attrs, versions[generation.ManifestPath]); err != nil {
				return time.Time{}, nil, err
			}
		}
		for _, metadataPath := range []string{".synto/vault-schema.md", "vault-schema.md"} {
			if attrs := objects[metadataPath]; attrs != nil {
				if err := add(metadataPath, attrs, ""); err != nil {
					return time.Time{}, nil, err
				}
			}
		}
		if s.fs != nil {
			if err := addProjectRecord(ctx, s.fs, userID, projectID, &files); err != nil {
				return time.Time{}, nil, err
			}
		}
	}
	if len(files) == 0 {
		return time.Time{}, nil, errors.New("project export contains no source files")
	}
	if len(files) > maxExportFiles {
		return time.Time{}, nil, errors.New("project export exceeds the file count limit")
	}
	var total int64
	for _, attrs := range objects {
		relative := strings.TrimPrefix(attrs.Name, prefix)
		includeListedMetadata := relative == generation.ManifestPath || relative == ".synto/vault-schema.md" || relative == "vault-schema.md" || (!manifestPresent && includeLegacyMetadataPath(relative))
		if strings.HasPrefix(relative, "raw/") || (scope == ScopeRawFullMetadata && includeListedMetadata) {
			if attrs.Size < 0 || total > maxExportSourceBytes-attrs.Size {
				return time.Time{}, nil, errors.New("project export exceeds the source size limit")
			}
			total += attrs.Size
		}
	}
	if manifestPresent && scope != ScopeRaw {
		for _, generationFile := range current.Files {
			if includeGenerationPath(generationFile.Path, scope) {
				if generationFile.Size < 0 || total > maxExportSourceBytes-generationFile.Size {
					return time.Time{}, nil, errors.New("project export exceeds the source size limit")
				}
				total += generationFile.Size
			}
		}
	} else if scope != ScopeRaw {
		for _, attrs := range objects {
			relative := strings.TrimPrefix(attrs.Name, prefix)
			if strings.HasPrefix(relative, "wiki/") {
				if attrs.Size < 0 || total > maxExportSourceBytes-attrs.Size {
					return time.Time{}, nil, errors.New("project export exceeds the source size limit")
				}
				total += attrs.Size
			}
		}
	}
	return snapshotAt, files, nil
}

func isStateDatabasePath(value string) bool {
	return value == ".synto/state.db" || value == ".olw/state.db"
}

func includeGenerationPath(relative string, scope Scope) bool {
	if strings.HasPrefix(relative, "wiki/") {
		return scope == ScopeRawFull || scope == ScopeRawFullMetadata
	}
	if scope != ScopeRawFullMetadata {
		return false
	}
	return relative == ".synto/INDEX.json" || isStateDatabasePath(relative) ||
		isProjectTOMLPath(relative) ||
		relative == "cache/id_map.json" || relative == "cache/concepts.jsonl" || relative == "cache/dormant_concepts.jsonl" ||
		relative == "cache/raw_status.json" || relative == "cache/suggested_queries.json"
}

func includeLegacyMetadataPath(relative string) bool {
	return isStateDatabasePath(relative) || isProjectTOMLPath(relative) || relative == ".synto/INDEX.json" || relative == ".synto/vault-schema.md" ||
		relative == "vault-schema.md" || relative == "cache/id_map.json" || relative == "cache/concepts.jsonl" ||
		relative == "cache/dormant_concepts.jsonl" || relative == "cache/raw_status.json" || relative == "cache/suggested_queries.json"
}

func readPinnedObject(ctx context.Context, object *storage.ObjectHandle, limit int64) ([]byte, error) {
	reader, err := object.NewReader(ctx)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, limit+1))
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("project metadata object exceeds its limit")
	}
	return data, nil
}

func includeScopePath(relative string, scope Scope) bool {
	if !scope.Valid() {
		return false
	}
	if strings.HasPrefix(relative, "raw/") {
		return true
	}
	if scope == ScopeRaw {
		return false
	}
	if strings.HasPrefix(relative, "wiki/") {
		return true // Includes unpublished drafts and hand-edited corrections.
	}
	return scope == ScopeRawFullMetadata && (relative == generation.ManifestPath || isProjectTOMLPath(relative) || relative == ".synto/vault-schema.md" || relative == "vault-schema.md")
}

func addProjectRecord(ctx context.Context, fs *firestore.Client, userID, projectID string, files *[]FileSource) error {
	snapshot, err := fs.Collection("projects").Doc(userID + "_" + projectID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return errors.New("project export metadata is unavailable")
	}
	if err != nil {
		return err
	}
	data := snapshot.Data()
	record := map[string]any{"project_id": projectID}
	for _, key := range []string{"name", "description", "created_at", "updated_at", "profile", "settings"} {
		if value, ok := data[key]; ok {
			record[key] = redactValue(value)
		}
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	version := snapshot.UpdateTime.UTC().Format(time.RFC3339Nano)
	*files = append(*files, FileSource{Path: "project-metadata/project.json", Version: version, Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(encoded))), nil
	}})
	return nil
}

func redactValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(value))
		for key, item := range value {
			if !sensitiveIdentifier(key) {
				copy[key] = redactValue(item)
			}
		}
		return copy
	case []any:
		copy := make([]any, len(value))
		for i, item := range value {
			copy[i] = redactValue(item)
		}
		return copy
	default:
		return value
	}
}

func isProjectTOMLPath(value string) bool {
	return value == "wiki.toml" || value == "synto.toml"
}

type removeOnClose struct {
	*os.File
	path string
}

func sanitizedStateSource(object *storage.ObjectHandle, archivePath, version string) FileSource {
	return FileSource{Path: archivePath, Version: version, Open: func(ctx context.Context) (io.ReadCloser, error) {
		database, err := sanitizeSQLiteObject(ctx, object)
		if err != nil {
			return nil, fmt.Errorf("sanitize project state database: %w", err)
		}
		file, err := os.Open(database)
		if err != nil {
			_ = os.Remove(database)
			return nil, err
		}
		return removeOnClose{File: file, path: database}, nil
	}}
}

func (r removeOnClose) Close() error {
	return errors.Join(r.File.Close(), os.Remove(r.path))
}

func sanitizeSQLiteObject(ctx context.Context, object *storage.ObjectHandle) (string, error) {
	reader, err := object.NewReader(ctx)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "lwc-export-state-*.db")
	if err != nil {
		_ = reader.Close()
		return "", err
	}
	pathName := tmp.Name()
	if _, err := io.Copy(tmp, reader); err != nil {
		_ = tmp.Close()
		_ = reader.Close()
		_ = os.Remove(pathName)
		return "", err
	}
	if err := errors.Join(tmp.Close(), reader.Close()); err != nil {
		_ = os.Remove(pathName)
		return "", err
	}
	db, err := sql.Open("sqlite", pathName)
	if err != nil {
		_ = os.Remove(pathName)
		return "", err
	}
	defer db.Close()
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		_ = os.Remove(pathName)
		if err == nil {
			err = errors.New("project state database failed integrity check")
		}
		return "", err
	}
	if err := redactSQLiteSecrets(db); err != nil {
		_ = os.Remove(pathName)
		return "", err
	}
	return pathName, nil
}

func redactSQLiteSecrets(db *sql.DB) error {
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA secure_delete=ON"); err != nil {
		return err
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, table := range tables {
		if sensitiveIdentifier(table) {
			if _, err := db.Exec("DELETE FROM " + quoteIdentifier(table)); err != nil {
				return err
			}
			continue
		}
		columns, err := db.Query("PRAGMA table_info(" + quoteIdentifier(table) + ")")
		if err != nil {
			return err
		}
		var sensitive []string
		var jsonColumns []string
		var keyColumns []string
		var valueColumns []string
		for columns.Next() {
			var cid, notnull, pk int
			var name, kind string
			var defaultValue any
			if err := columns.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
				_ = columns.Close()
				return err
			}
			if sensitiveIdentifier(name) {
				value := "=NULL"
				if notnull != 0 {
					value = `=''`
				}
				sensitive = append(sensitive, quoteIdentifier(name)+value)
			} else if likelyJSONColumn(name, kind) {
				jsonColumns = append(jsonColumns, name)
			}
			if isSettingKeyColumn(name) {
				keyColumns = append(keyColumns, name)
			}
			if isSettingValueColumn(name) {
				valueColumns = append(valueColumns, name)
			}
		}
		if err := errors.Join(columns.Err(), columns.Close()); err != nil {
			return err
		}
		if len(sensitive) > 0 {
			if _, err := db.Exec("UPDATE " + quoteIdentifier(table) + " SET " + strings.Join(sensitive, ",")); err != nil {
				return err
			}
		}
		for _, name := range jsonColumns {
			if err := redactSQLiteJSONColumn(db, table, name); err != nil {
				return err
			}
		}
		if len(keyColumns) == 1 && len(valueColumns) == 1 {
			if err := redactSQLiteKeyValueRows(db, table, keyColumns[0]); err != nil {
				return err
			}
		}
	}
	// UPDATE/DELETE alone may leave the previous cell bytes in free pages. VACUUM
	// rebuilds the exported file after secure_delete has overwritten removed cells.
	if _, err := db.Exec("VACUUM"); err != nil {
		return err
	}
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return errors.New("sanitized project state database failed integrity check")
	}
	return nil
}

func likelyJSONColumn(name, kind string) bool {
	name, kind = strings.ToLower(name), strings.ToLower(kind)
	if kind != "" && !strings.Contains(kind, "text") && !strings.Contains(kind, "json") && !strings.Contains(kind, "blob") {
		return false
	}
	for _, part := range []string{"json", "data", "config", "settings", "metadata", "payload", "value"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

func isSettingKeyColumn(name string) bool {
	switch strings.ToLower(name) {
	case "key", "name", "setting_key", "config_key", "property":
		return true
	default:
		return false
	}
}

func isSettingValueColumn(name string) bool {
	switch strings.ToLower(name) {
	case "value", "setting_value", "config_value":
		return true
	default:
		return false
	}
}

func redactSQLiteKeyValueRows(db *sql.DB, table, keyColumn string) error {
	rows, err := db.Query("SELECT rowid, " + quoteIdentifier(keyColumn) + " FROM " + quoteIdentifier(table))
	if err != nil {
		return err // Fail closed if sensitive setting rows cannot be identified safely.
	}
	type keyedRow struct {
		id  int64
		key string
	}
	var sensitiveRows []keyedRow
	for rows.Next() {
		var item keyedRow
		if err := rows.Scan(&item.id, &item.key); err != nil {
			_ = rows.Close()
			return err
		}
		if sensitiveIdentifier(item.key) {
			sensitiveRows = append(sensitiveRows, item)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range sensitiveRows {
		if _, err := db.Exec("DELETE FROM "+quoteIdentifier(table)+" WHERE rowid=?", item.id); err != nil {
			return err
		}
	}
	return nil
}

func redactSQLiteJSONColumn(db *sql.DB, table, column string) error {
	rows, err := db.Query("SELECT rowid, " + quoteIdentifier(column) + " FROM " + quoteIdentifier(table) + " WHERE " + quoteIdentifier(column) + " IS NOT NULL")
	if err != nil {
		return err // Fail closed for tables whose row identity cannot be safely preserved.
	}
	type row struct {
		id    int64
		value any
	}
	var values []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.value); err != nil {
			_ = rows.Close()
			return err
		}
		values = append(values, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range values {
		var raw []byte
		switch value := item.value.(type) {
		case string:
			raw = []byte(value)
		case []byte:
			raw = value
		default:
			continue
		}
		var parsed any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			if strings.Contains(strings.ToLower(column), "json") {
				if _, err := db.Exec("UPDATE "+quoteIdentifier(table)+" SET "+quoteIdentifier(column)+"=NULL WHERE rowid=?", item.id); err != nil {
					return err
				}
			}
			continue
		}
		encoded, err := json.Marshal(redactValue(parsed))
		if err != nil {
			return err
		}
		if _, err := db.Exec("UPDATE "+quoteIdentifier(table)+" SET "+quoteIdentifier(column)+"=? WHERE rowid=?", string(encoded), item.id); err != nil {
			return err
		}
	}
	return nil
}

func sensitiveIdentifier(value string) bool {
	value = strings.ToLower(value)
	for _, part := range []string{"secret", "credential", "authorization", "token", "api_key", "access_key", "private_key", "signing_key", "service_account", "keypair", "key_pair", "password", "session", "oauth", "sync_key"} {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
