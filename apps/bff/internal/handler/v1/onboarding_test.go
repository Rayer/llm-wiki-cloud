package v1

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
)

func TestDefaultProjectIdentityDoesNotUseDisplayName(t *testing.T) {
	if canRecoverProjectFromOwnerSuffix("owner_other", "owner", "default") {
		t.Fatal("a different owner-prefixed suffix was treated as the default Project")
	}
	if !canRecoverProjectFromOwnerSuffix("owner_default", "owner", "default") {
		t.Fatal("the exact owner-prefixed stable ID suffix was not recoverable")
	}
	if _, _, ok := projectResponseFromFirestoreDoc("owner_My First Wiki", map[string]interface{}{
		"user_id": "owner", "project_id": "default", "name": "My First Wiki",
	}); !ok {
		t.Fatal("a display-label suffix with an explicit stable Project ID was not parsed")
	}
}

func TestEnsureDefaultProjectPreservesOtherLegacyProjectsInFirestore(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	projects := scopedfirestore.Collection(client.Raw(), "projects")
	users := scopedfirestore.Collection(client.Raw(), "users")
	createdAt := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name          string
		seedDefault   bool
		seedOtherOnly bool
	}{
		{name: "only unrelated legacy Project", seedOtherOnly: true},
		{name: "default and unrelated legacy Project", seedDefault: true, seedOtherOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := "owner-" + strings.ReplaceAll(tc.name, " ", "-") + fmt.Sprintf("-%d", time.Now().UnixNano())
			defaultRef := projects.Doc(projectDocID(owner, "default"))
			metadataRef := users.Doc(owner).Collection("projects").Doc("default")
			otherRef := projects.Doc(projectDocID(owner, "notes"))
			if tc.seedDefault {
				if _, err := defaultRef.Set(ctx, map[string]interface{}{
					"user_id": owner, "project_id": "default", "name": "Existing default", "status": "ready", "created_at": createdAt,
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := metadataRef.Set(ctx, map[string]interface{}{"name": "Existing default", "created_at": createdAt}); err != nil {
					t.Fatal(err)
				}
			}
			var otherBefore map[string]interface{}
			if tc.seedOtherOnly {
				if _, err := otherRef.Set(ctx, map[string]interface{}{
					"user_id": owner, "name": "Notes", "status": "ready", "created_at": createdAt,
				}); err != nil {
					t.Fatal(err)
				}
				snapshot, err := otherRef.Get(ctx)
				if err != nil {
					t.Fatal(err)
				}
				otherBefore = snapshot.Data()
			}

			if err := ensureDefaultProject(ctx, client.Raw(), owner, "default"); err != nil {
				t.Fatalf("ensure default Project: %v", err)
			}
			defaultSnapshot, err := defaultRef.Get(ctx)
			if err != nil {
				t.Fatalf("read ensured default Project: %v", err)
			}
			defaultData := defaultSnapshot.Data()
			if defaultData["user_id"] != owner || defaultData["project_id"] != "default" || defaultData["status"] != "ready" {
				t.Fatalf("default Project identity/status=%v", defaultData)
			}
			metadataSnapshot, err := metadataRef.Get(ctx)
			if err != nil || metadataSnapshot.Data()["name"] == "" {
				t.Fatalf("default metadata=%v err=%v", metadataSnapshot, err)
			}
			if tc.seedDefault && defaultData["name"] != "Existing default" {
				t.Fatalf("existing default was rewritten: %v", defaultData)
			}
			if tc.seedOtherOnly {
				after, err := otherRef.Get(ctx)
				if err != nil || !reflect.DeepEqual(otherBefore, after.Data()) {
					t.Fatalf("unrelated legacy Project changed: before=%v after=%v err=%v", otherBefore, after.Data(), err)
				}
			}
		})
	}
}

func TestEnsureDefaultProjectRejectsTargetConflictAndDuplicateWithoutWrites(t *testing.T) {
	client := newOnboardingFirestore(t)
	ctx := context.Background()
	projects := scopedfirestore.Collection(client.Raw(), "projects")
	users := scopedfirestore.Collection(client.Raw(), "users")
	createdAt := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		seed func(t *testing.T, owner string, metadata, projects *firestore.CollectionRef)
	}{
		{name: "conflicting owner metadata", seed: func(t *testing.T, owner string, metadata, projects *firestore.CollectionRef) {
			if _, err := projects.Doc(projectDocID(owner, "default")).Set(ctx, map[string]interface{}{
				"user_id": owner, "project_id": "default", "name": "Default", "status": "ready", "created_at": createdAt,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := metadata.Doc("default").Set(ctx, map[string]interface{}{
				"user_id": "another-owner", "project_id": "default", "name": "Conflict", "created_at": createdAt,
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "duplicate target identity", seed: func(t *testing.T, owner string, metadata, projects *firestore.CollectionRef) {
			for _, suffix := range []string{"default", "My First Wiki"} {
				if _, err := projects.Doc(projectDocID(owner, suffix)).Set(ctx, map[string]interface{}{
					"user_id": owner, "project_id": "default", "name": suffix, "status": "ready", "created_at": createdAt,
				}); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := "conflict-" + strings.ReplaceAll(tc.name, " ", "-") + fmt.Sprintf("-%d", time.Now().UnixNano())
			tc.seed(t, owner, users.Doc(owner).Collection("projects"), projects)
			beforeProjects, err := projects.Documents(ctx).GetAll()
			if err != nil {
				t.Fatal(err)
			}
			beforeMetadata, err := users.Doc(owner).Collection("projects").Documents(ctx).GetAll()
			if err != nil {
				t.Fatal(err)
			}
			beforeDocs := make(map[string]map[string]interface{})
			for _, doc := range beforeProjects {
				if strings.HasPrefix(doc.Ref.ID, owner+"_") {
					beforeDocs[doc.Ref.ID] = doc.Data()
				}
			}
			beforeMeta := make(map[string]map[string]interface{})
			for _, doc := range beforeMetadata {
				beforeMeta[doc.Ref.ID] = doc.Data()
			}
			if err := ensureDefaultProject(ctx, client.Raw(), owner, "default"); !errors.Is(err, errDefaultProjectSetupConflict) {
				t.Fatalf("target conflict result=%v, want setup conflict", err)
			}
			afterProjects, err := projects.Documents(ctx).GetAll()
			if err != nil {
				t.Fatal(err)
			}
			afterDocs := make(map[string]map[string]interface{})
			for _, doc := range afterProjects {
				if strings.HasPrefix(doc.Ref.ID, owner+"_") {
					afterDocs[doc.Ref.ID] = doc.Data()
				}
			}
			afterMetadata, err := users.Doc(owner).Collection("projects").Documents(ctx).GetAll()
			if err != nil {
				t.Fatal(err)
			}
			afterMeta := make(map[string]map[string]interface{})
			for _, doc := range afterMetadata {
				afterMeta[doc.Ref.ID] = doc.Data()
			}
			if !reflect.DeepEqual(beforeDocs, afterDocs) || !reflect.DeepEqual(beforeMeta, afterMeta) {
				t.Fatalf("conflict path wrote Project state: projects before=%v after=%v metadata before=%v after=%v", beforeDocs, afterDocs, beforeMeta, afterMeta)
			}
		})
	}
}

func newOnboardingFirestore(t *testing.T) *scopedfirestore.Client {
	t.Helper()
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires an owned loopback Firestore emulator")
	}
	scope := fmt.Sprintf("gat4-onboarding-%d", time.Now().UnixNano())
	project := "gat4-onboarding"
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", scope)
	if err != nil {
		t.Fatalf("create scoped emulator client: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close scoped emulator client: %v", err)
		}
	})
	return client
}
