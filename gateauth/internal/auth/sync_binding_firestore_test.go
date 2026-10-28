package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestSyncBindingAuthorityCreatesChecksReauthorizesAndRevokes(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, projectID, host = "binding-owner", "project-346", "https://auth.example.test"
	if _, err := fs.Collection("projects").Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "name": "Wiki",
	}); err != nil {
		t.Fatal(err)
	}
	authority := NewSyncBindingAuthority(fs, "lwc-346-binding", host)
	created, err := authority.CreateBinding(ctx, userID, projectID, "wiki-1", host)
	if err != nil || created.ID == "" || created.Status != syncBindingStatusActive || created.AuthorizedBy != userID {
		t.Fatalf("created binding=%#v error=%v", created, err)
	}
	if err := authority.CheckSyncBinding(ctx, userID, projectID, created.ID, "wiki-1", host); err != nil {
		t.Fatalf("check active binding = %v", err)
	}
	for _, check := range []struct {
		user, project, binding, wiki, host string
	}{
		{"someone-else", projectID, created.ID, "wiki-1", host},
		{userID, "missing-project", created.ID, "wiki-1", host},
		{userID, projectID, "other-binding", "wiki-1", host},
		{userID, projectID, created.ID, "copied-wiki", host},
		{userID, projectID, created.ID, "wiki-1", "https://other.example.test"},
	} {
		if err := authority.CheckSyncBinding(ctx, check.user, check.project, check.binding, check.wiki, check.host); !errors.Is(err, ErrSyncBindingUnauthorized) {
			t.Fatalf("mismatched binding check error = %v, want unauthorized", err)
		}
	}
	if _, err := authority.CreateBinding(ctx, userID, projectID, "wiki-other", host); !errors.Is(err, ErrSyncBindingAlreadyBound) {
		t.Fatalf("second active binding error = %v, want already bound", err)
	}

	listed, err := authority.ListBindings(ctx, userID)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed bindings=%#v error=%v", listed, err)
	}

	replaced, err := authority.ReauthorizeBinding(ctx, userID, projectID, created.ID, "wiki-2", host)
	if err != nil || replaced.ID == created.ID || replaced.Status != syncBindingStatusActive {
		t.Fatalf("reauthorized binding=%#v error=%v", replaced, err)
	}
	if err := authority.CheckSyncBinding(ctx, userID, projectID, created.ID, "wiki-1", host); !errors.Is(err, ErrSyncBindingUnauthorized) {
		t.Fatalf("replaced binding remains authorized: %v", err)
	}
	if err := authority.RevokeBinding(ctx, userID, projectID, created.ID, "user_revoked"); !errors.Is(err, ErrSyncBindingNotFound) {
		t.Fatalf("revoke replaced binding error = %v, want not found", err)
	}
	if err := authority.RevokeBinding(ctx, userID, projectID, replaced.ID, "user_revoked"); err != nil {
		t.Fatalf("revoke active binding = %v", err)
	}
	if err := authority.CheckSyncBinding(ctx, userID, projectID, replaced.ID, "wiki-2", host); !errors.Is(err, ErrSyncBindingUnauthorized) {
		t.Fatalf("revoked binding remains authorized: %v", err)
	}
	if _, err := authority.CreateBinding(ctx, userID, projectID, "wiki-3", host); !errors.Is(err, ErrSyncBindingReauthorizationRequired) {
		t.Fatalf("recreate revoked binding error = %v, want explicit reauthorization", err)
	}
	reauthorized, err := authority.ReauthorizeBinding(ctx, userID, projectID, replaced.ID, "wiki-3", host)
	if err != nil || reauthorized.ID == replaced.ID {
		t.Fatalf("explicit reauthorization=%#v error=%v", reauthorized, err)
	}

	snapshot, err := fs.Collection("projects").Doc(userID + "_" + projectID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored := fmt.Sprint(snapshot.Data()["sync_binding"])
	if strings.Contains(stored, "access_token") || strings.Contains(stored, "refresh_token") {
		t.Fatalf("binding authority contains credentials: %s", stored)
	}
	audits, err := fs.Collection(syncBindingAuditCollection).Where("project_id", "==", projectID).Documents(ctx).GetAll()
	if err != nil || len(audits) < 4 {
		t.Fatalf("binding audit records=%d error=%v", len(audits), err)
	}
	for _, event := range audits {
		data := event.Data()
		for _, forbidden := range []string{"access_token", "refresh_token", "wiki_id", "host", "user_code"} {
			if _, found := data[forbidden]; found {
				t.Fatalf("audit event contains non-ID secret/detail field %q: %#v", forbidden, data)
			}
		}
	}
}

func TestSyncBindingCreateIsAtomicForConcurrentDifferentWikis(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, projectID, host = "binding-race-owner", "project-race", "https://auth.example.test"
	if _, err := fs.Collection("projects").Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{"user_id": userID, "project_id": projectID}); err != nil {
		t.Fatal(err)
	}
	authority := NewSyncBindingAuthority(fs, "lwc-346-binding-race", host)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, wikiID := range []string{"wiki-one", "wiki-two"} {
		wikiID := wikiID
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := authority.CreateBinding(ctx, userID, projectID, wikiID, host)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrSyncBindingAlreadyBound) {
			conflicts++
		} else {
			t.Fatalf("concurrent binding error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent binding successes=%d conflicts=%d", successes, conflicts)
	}
}
