//go:build lwc_local_pipeline_fixture

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/suggestedqueries"
)

// This build-tagged executor is reserved for the no-cost local pipeline
// contract test. Cloud materialization, reconciliation, and GCS publication
// remain production worker code. A normal manifest write is acknowledged by
// GCS; explicit readback happens only when the commit response is ambiguous.
func init() {
	execOLW = execLocalCloudFixture
	// The contract fixture never calls an LLM; nil keeps the worker's normal
	// last-known-good/empty suggested-query behavior deterministic.
	suggestedQueryProvider = func(workerConfig) suggestedqueries.Provider { return nil }
}

func execLocalCloudFixture(ctx context.Context, vault string, command, _ []string, _, _ io.Writer) error {
	if len(command) == 0 || command[0] != "run" {
		return errors.New("local pipeline fixture only supports the run command")
	}
	if _, err := os.Stat(filepath.Join(vault, "raw", "fixture-fail.md")); err == nil {
		return errors.New("fixture compile failure")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read fixture failure input: %w", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "raw", "fixture-timeout.md")); err == nil {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return context.DeadlineExceeded
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read fixture timeout input: %w", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "raw", "fixture-outage.md")); err == nil {
		parent := strings.TrimSpace(os.Getenv("WORKSPACE_DIR"))
		if parent == "" {
			parent = filepath.Dir(vault)
		}
		ready := filepath.Join(parent, "fixture-outage-ready")
		release := filepath.Join(parent, "fixture-outage-release")
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			return fmt.Errorf("write fixture outage readiness: %w", err)
		}
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(release); err == nil {
				return errors.New("fixture controlled outage failure")
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("read fixture outage release: %w", err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read fixture outage input: %w", err)
	}
	// Keep a short observable RUNNING window for duplicate-trigger contract tests.
	time.Sleep(750 * time.Millisecond)
	for path, content := range map[string]string{
		"wiki/alpha.md":     "---\nid: c5d9e3f1a028\n---\nAlpha fixture output\n",
		".synto/INDEX.json": `{"schema_version":1,"pack":{"id":"fixture","name":"fixture","version":"0","language":["en"],"capabilities":["articles","concepts"]},"articles":[{"id":"article","entity_id":"01JAZ5N7Y3K8M2Q4R6T9VWXAC8","name":"alpha","path":"wiki/alpha.md","summary":null,"tags":[],"aliases":[],"confidence":"high"}],"terms":[],"papers":[],"sources":[],"source_concepts":[],"synthesis":[],"stats":{"article_count":1,"draft_count":0,"concept_count":1,"alias_count":0,"knowledge_item_count":0,"source_count":0,"source_segment_count":0,"failed_note_count":0,"failed_concept_count":0}}`,
	} {
		if err := writeLocalFixtureFile(vault, path, []byte(content)); err != nil {
			return err
		}
	}
	if err := writeLocalFixtureStateDB(vault); err != nil {
		return err
	}
	return nil
}

func writeLocalFixtureStateDB(root string) error {
	path := filepath.Join(root, ".synto", "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open fixture Synto state: %w", err)
	}
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS fixture_state (id INTEGER PRIMARY KEY)"); err != nil {
		_ = db.Close()
		return fmt.Errorf("create fixture Synto state: %w", err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close fixture Synto state: %w", err)
	}
	return nil
}

func writeLocalFixtureFile(root, rel string, data []byte) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create local fixture output: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write local fixture output: %w", err)
	}
	return nil
}
