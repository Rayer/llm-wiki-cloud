package gcs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
)

func TestObjectRelativePathTrimsProjectPrefix(t *testing.T) {
	client := &Client{userID: "u1", projectID: "p1"}

	got, ok := client.objectRelativePath("users/u1/projects/p1/wiki/page.md", "")

	if !ok {
		t.Fatal("objectRelativePath returned ok=false")
	}
	if got != "wiki/page.md" {
		t.Fatalf("relative path = %q, want %q", got, "wiki/page.md")
	}
}

func TestObjectRelativePathKeepsRequestedSubPrefix(t *testing.T) {
	client := &Client{userID: "u1", projectID: "p1"}

	got, ok := client.objectRelativePath("users/u1/projects/p1/wiki/page.md", "wiki")

	if !ok {
		t.Fatal("objectRelativePath returned ok=false")
	}
	if got != "wiki/page.md" {
		t.Fatalf("relative path = %q, want %q", got, "wiki/page.md")
	}
}

func TestObjectRelativePathRejectsProjectDirectoryMarker(t *testing.T) {
	client := &Client{userID: "u1", projectID: "p1"}

	if got, ok := client.objectRelativePath("users/u1/projects/p1/", ""); ok {
		t.Fatalf("objectRelativePath = %q, true; want false", got)
	}
}

func TestRawFileNameFromObjectKeepsDirectRawChildren(t *testing.T) {
	client := &Client{userID: "u1", projectID: "p1"}

	name, ok := client.rawFileNameFromObject("users/u1/projects/p1/raw/article.md")
	if !ok {
		t.Fatal("rawFileNameFromObject returned ok=false")
	}
	if name != "article.md" {
		t.Fatalf("name = %q, want article.md", name)
	}
}

func TestRawFileNameFromObjectRejectsNestedAndMarkers(t *testing.T) {
	client := &Client{userID: "u1", projectID: "p1"}

	tests := []string{
		"users/u1/projects/p1/raw/",
		"users/u1/projects/p1/raw/nested/article.md",
		"users/u1/projects/p1/wiki/article.md",
		"users/u2/projects/p1/raw/article.md",
	}
	for _, objectName := range tests {
		if name, ok := client.rawFileNameFromObject(objectName); ok {
			t.Fatalf("rawFileNameFromObject(%q) = %q, true; want false", objectName, name)
		}
	}
}

func TestListObjectMetaEnforcesCountAndByteBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		bytes int64
	}{
		{name: "exact count", count: generation.MaxFiles, bytes: generation.MaxFiles},
		{name: "count plus one", count: generation.MaxFiles + 1, bytes: generation.MaxFiles + 1},
		{name: "exact bytes", count: 2, bytes: generation.MaxTotalSize},
		{name: "bytes plus one", count: 2, bytes: generation.MaxTotalSize + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, backend := newMemoryClient()
			for i := 0; i < tc.count; i++ {
				backend.put(projectObject(fmt.Sprintf("raw/%05d.md", i)), nil, int64(i+1), nil)
			}
			backend.mu.Lock()
			for i := 0; i < tc.count; i++ {
				name := projectObject(fmt.Sprintf("raw/%05d.md", i))
				object := backend.objects[name]
				object.Size = 1
				backend.objects[name] = object
			}
			first := backend.objects[projectObject("raw/00000.md")]
			first.Size = tc.bytes - int64(tc.count-1)
			backend.objects[first.Name] = first
			backend.mu.Unlock()
			_, err := client.ListObjectMeta(context.Background(), "raw/")
			if tc.name == "exact count" || tc.name == "exact bytes" {
				if err != nil {
					t.Fatalf("exact boundary error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "object list exceeds limit") {
				t.Fatalf("bounded listing error = %v", err)
			}
		})
	}
}

func TestListProjectsUsesScopedObjectAndCreatedAtPaths(t *testing.T) {
	client, backend := newMemoryClient()
	client.localScope = localcloud.Scope("scope-a")
	created := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	backend.put("local_scopes/scope-a/users/user/projects/a/index.md", []byte("A"), 1, nil)
	backend.put("local_scopes/scope-a/users/user/projects/b/index.md", []byte("B"), 2, nil)
	backend.put("local_scopes/scope-b/users/user/projects/foreign/index.md", []byte("foreign"), 3, nil)
	backend.put("users/user/projects/unscoped/index.md", []byte("unscoped"), 4, nil)
	backend.mu.Lock()
	object := backend.objects["local_scopes/scope-a/users/user/projects/a/index.md"]
	object.Created = created
	backend.objects[object.Name] = object
	backend.mu.Unlock()

	projects, err := client.ListProjects(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0].ID != "a" || projects[1].ID != "b" {
		t.Fatalf("scoped projects = %#v; want only a and b", projects)
	}
	if projects[0].CreatedAt != created.Format(time.RFC3339) {
		t.Fatalf("scoped project created_at = %q; want %q", projects[0].CreatedAt, created.Format(time.RFC3339))
	}
	if len(backend.listPrefixes) != 1 || backend.listPrefixes[0] != "local_scopes/scope-a/users/user/projects/" {
		t.Fatalf("project listing prefixes = %v", backend.listPrefixes)
	}
	for _, requested := range backend.requests {
		if requested.Name == "" {
			t.Errorf("unexpected nameless object read: %#v", requested)
		}
	}
}

func TestListObjectMetaRejectsNegativeAndHugeSizes(t *testing.T) {
	for _, size := range []int64{-1, generation.MaxTotalSize + 1} {
		client, backend := newMemoryClient()
		name := projectObject("raw/bad.md")
		backend.put(name, nil, 1, nil)
		backend.mu.Lock()
		object := backend.objects[name]
		object.Size = size
		backend.objects[name] = object
		backend.mu.Unlock()
		if _, err := client.ListObjectMeta(context.Background(), "raw/"); err == nil || !strings.Contains(err.Error(), "object list exceeds limit") {
			t.Fatalf("size %d error = %v", size, err)
		}
	}
}
