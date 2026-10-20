package storage

import (
	"strings"
	"testing"
)

func TestValidateRawSyncPath(t *testing.T) {
	for _, path := range []string{"note.md", "topic/notes/公園.md", "a..b.md"} {
		if err := ValidateRawSyncPath(path); err != nil {
			t.Errorf("ValidateRawSyncPath(%q) = %v", path, err)
		}
	}

	for _, path := range []string{
		"", "/absolute.md", `folder\\note.md`, "../note.md", "folder/../note.md",
		"folder//note.md", "folder/./note.md", "C:/note.md", "bad\x7fname.md",
		strings.Repeat("a", MaxRawSyncPathBytes+1), string([]byte{0xff, '.', 'm', 'd'}),
	} {
		if err := ValidateRawSyncPath(path); err == nil {
			t.Errorf("ValidateRawSyncPath(%q) unexpectedly succeeded", path)
		}
	}
}
