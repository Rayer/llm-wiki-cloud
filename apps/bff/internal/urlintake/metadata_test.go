package urlintake

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMergeRawMarkdownPreservesUserMetadataAndIsAByteNoop(t *testing.T) {
	original := []byte("---\ntitle: Earlier title\nsource: https://example.com/article?utm_source=old\ncreated: 2021-02-03\nauthor: A. Writer\ntags:\n  - reference\ncustom:\n  owner: user\n---\n\nold body\n")
	now := time.Date(2026, time.October, 21, 12, 0, 0, 0, time.UTC)
	incoming := SourceMetadata{
		Title: "Current title", SourceURL: "https://example.com/article?utm_source=new",
		Description: "Known page description", Tags: []string{"reference", "Engineering"},
	}
	merged, unchanged, err := MergeRawMarkdown(original, incoming, "# Current title\n\nnew body\n", now)
	if err != nil || unchanged {
		t.Fatalf("MergeRawMarkdown() = unchanged %t, err %v", unchanged, err)
	}
	metadata, body, hasFrontmatter, err := parseRawFrontmatter(merged)
	if err != nil || !hasFrontmatter || string(body) != "# Current title\n\nnew body\n" {
		t.Fatalf("parsed merge = %#v, %q, %t, %v", metadata, body, hasFrontmatter, err)
	}
	if metadata["title"] != "Current title" || metadata["source"] != "https://example.com/article?utm_source=old" {
		t.Fatalf("source-owned metadata = %#v", metadata)
	}
	created := metadata["created"]
	if parsed, ok := created.(time.Time); ok {
		created = parsed.Format("2006-01-02")
	}
	if fmt.Sprint(created) != "2021-02-03" || metadata["author"] != "A. Writer" || metadata["description"] != "Known page description" {
		t.Fatalf("preserved optional metadata = %#v", metadata)
	}
	tags, ok := metadata["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "reference" || tags[1] != "Engineering" {
		t.Fatalf("merged source tags = %#v", metadata["tags"])
	}
	custom, ok := metadata["custom"].(map[string]any)
	if !ok || custom["owner"] != "user" {
		t.Fatalf("unrecognized user metadata = %#v", metadata["custom"])
	}
	again, unchanged, err := MergeRawMarkdown(merged, incoming, "# Current title\n\nnew body\n", now.Add(time.Hour))
	if err != nil || !unchanged || !bytes.Equal(again, merged) {
		t.Fatalf("identical refresh changed bytes: unchanged=%t err=%v", unchanged, err)
	}
}

func TestMergeRawMarkdownAddsOnlyKnownMetadataAndRejectsMalformedFrontmatter(t *testing.T) {
	createdAt := time.Date(2026, time.October, 21, 12, 0, 0, 0, time.UTC)
	data, _, err := MergeRawMarkdown(nil, SourceMetadata{Title: "Page", SourceURL: "https://example.com/"}, "Page body", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _, _, err := parseRawFrontmatter(data)
	if err != nil {
		t.Fatal(err)
	}
	if metadata["author"] != nil || metadata["published"] != nil || metadata["description"] != nil || metadata["tags"] != nil {
		t.Fatalf("unavailable metadata was invented: %#v", metadata)
	}
	if !strings.Contains(fmt.Sprint(metadata["created"]), "2026-10-21") {
		t.Fatalf("created date missing: %#v", metadata["created"])
	}
	if _, _, err := MergeRawMarkdown([]byte("---\ntitle: one\ntitle: two\n---\nbody"), SourceMetadata{Title: "new"}, "new", createdAt); err == nil {
		t.Fatal("duplicate YAML key unexpectedly accepted")
	}
}
