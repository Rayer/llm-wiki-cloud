package localcloud

import "testing"

func TestScopeValidatesAndBuildsSharedRoots(t *testing.T) {
	scope, err := Parse("worktree-a9f0")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := scope.FirestoreCollection("users"), "local_scopes/worktree-a9f0/users"; got != want {
		t.Fatalf("Firestore root=%q, want %q", got, want)
	}
	if got, want := scope.ObjectPrefix(), "local_scopes/worktree-a9f0/"; got != want {
		t.Fatalf("GCS root=%q, want %q", got, want)
	}
	if got, err := Parse(""); err != nil || got != "" {
		t.Fatalf("empty deployed scope=%q, err=%v", got, err)
	}
	for _, invalid := range []string{"../shared", "bad/scope", "space scope", "scope?x", string(make([]byte, 101))} {
		if _, err := Parse(invalid); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", invalid)
		}
	}
}
