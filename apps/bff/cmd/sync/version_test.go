package main

import (
	"bytes"
	"testing"
)

func TestHandleVersionCommand(t *testing.T) {
	original := version
	version = "1.2.3-rc.1"
	t.Cleanup(func() { version = original })

	var stdout bytes.Buffer
	if !handleVersionCommand([]string{"--version"}, &stdout) {
		t.Fatal("--version was not handled")
	}
	if got, want := stdout.String(), "lwc-sync 1.2.3-rc.1\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestHandleVersionCommandRequiresExactTopLevelArgument(t *testing.T) {
	for _, args := range [][]string{{}, {"push", "--version"}, {"--version", "push"}} {
		var stdout bytes.Buffer
		if handleVersionCommand(args, &stdout) {
			t.Fatalf("args %q unexpectedly handled", args)
		}
		if stdout.Len() != 0 {
			t.Fatalf("args %q wrote output %q", args, stdout.String())
		}
	}
}
