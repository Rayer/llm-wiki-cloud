package firestore

import (
	"context"
	"strings"
	"testing"

	cloudfirestore "cloud.google.com/go/firestore"
	"google.golang.org/api/option"
)

func TestCollectionUsesOneRootPerLocalScope(t *testing.T) {
	newClient := func() *cloudfirestore.Client {
		client, err := cloudfirestore.NewClient(context.Background(), "scope-test", option.WithEndpoint("localhost:8787"), option.WithoutAuthentication())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	first, second, deployed := newClient(), newClient(), newClient()
	if err := RegisterLocalScope(first, "worktree-one"); err != nil {
		t.Fatal(err)
	}
	if err := RegisterLocalScope(second, "worktree-two"); err != nil {
		t.Fatal(err)
	}
	firstPath := Collection(first, "users").Doc("same-user").Collection("projects").Path
	secondPath := Collection(second, "users").Doc("same-user").Collection("projects").Path
	deployedPath := Collection(deployed, "users").Doc("same-user").Collection("projects").Path
	if firstPath == secondPath || !strings.Contains(firstPath, "local_scopes/worktree-one/users/same-user/projects") || !strings.Contains(secondPath, "local_scopes/worktree-two/users/same-user/projects") {
		t.Fatalf("scoped paths overlap or missed their root: first=%q second=%q", firstPath, secondPath)
	}
	if strings.Contains(deployedPath, "local_scopes/") || !strings.Contains(deployedPath, "documents/users/same-user/projects") {
		t.Fatalf("deployed collection layout changed: %q", deployedPath)
	}
	if err := RegisterLocalScope(first, ""); err != nil {
		t.Fatal(err)
	}
	if got := Collection(first, "users").Path; !strings.Contains(got, "documents/users") {
		t.Fatalf("cleared scope still routes into local root: %q", got)
	}
}
