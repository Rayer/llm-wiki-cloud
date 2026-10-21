package urlintake

import "testing"

func TestNormalizeMarkdownOnlyChangesKnownStructure(t *testing.T) {
	input := "Skip to content\n\n\\# Title\n\n\\- one\nText with \\# in line and 0\\.9 `\\# code` [link](https://example.com/a\\?x=1)\n\n```md\n\\# kept in fence\n```\n\n給個讚\n"
	want := "\n# Title\n\n- one\nText with \\# in line and 0\\.9 `\\# code` [link](https://example.com/a\\?x=1)\n\n```md\n\\# kept in fence\n```\n\n"
	if got := NormalizeMarkdown(input); got != want {
		t.Fatalf("NormalizeMarkdown() = %q, want %q", got, want)
	}
}

func TestNormalizeMarkdownPreservesIndentedCodeAndFenceLength(t *testing.T) {
	input := "    Skip to content\n    \\# literal indented code\n\n````md\n```\n\\# literal fenced code\n````\n\n\\# outside code\n"
	want := "    Skip to content\n    \\# literal indented code\n\n````md\n```\n\\# literal fenced code\n````\n\n# outside code\n"
	if got := NormalizeMarkdown(input); got != want {
		t.Fatalf("NormalizeMarkdown() = %q, want %q", got, want)
	}
}
