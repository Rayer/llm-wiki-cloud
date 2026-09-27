package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFirestoreProjectOwnerAuthorizerAndPagination(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	created := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	for id, data := range map[string]map[string]interface{}{
		"owner_beta":        {"user_id": "owner", "project_id": "beta", "name": "Beta", "created_at": created},
		"owner_alpha":       {"user_id": "owner", "project_id": "alpha", "name": "Alpha", "created_at": created},
		"owner_request-key": {"user_id": "owner", "project_id": "alpha", "name": "Marker", "idempotency_key": "request-key"},
		"other_secret":      {"user_id": "other", "project_id": "secret", "name": "Private"},
	} {
		if _, err := fs.Collection("projects").Doc(id).Set(ctx, data); err != nil {
			t.Fatal(err)
		}
	}

	authorize := FirestoreProjectOwnerAuthorizer(fs)
	if err := authorize(ctx, "owner", "alpha"); err != nil {
		t.Fatalf("owner project authorization = %v", err)
	}
	if err := authorize(ctx, "other", "alpha"); !errors.Is(err, ErrProjectPermissionDenied) {
		t.Fatalf("foreign owner error = %v, want permission denied", err)
	}
	if err := authorize(ctx, "owner", "missing"); !errors.Is(err, ErrProjectPermissionDenied) {
		t.Fatalf("missing project error = %v, want permission denied", err)
	}
	if err := FirestoreProjectOwnerAuthorizer(nil)(ctx, "owner", "alpha"); !errors.Is(err, ErrProjectPermissionUnavailable) {
		t.Fatalf("missing authority error = %v, want unavailable", err)
	}

	page, next, err := ListOwnedProjectsPage(ctx, fs, "owner", 1, "")
	if err != nil || len(page) != 1 || page[0].ID != "alpha" || page[0].Name != "Alpha" || next == "" {
		t.Fatalf("first page=%#v next=%q error=%v", page, next, err)
	}
	page, next, err = ListOwnedProjectsPage(ctx, fs, "owner", 1, next)
	if err != nil || len(page) != 1 || page[0].ID != "beta" || next != "" {
		t.Fatalf("second page=%#v next=%q error=%v", page, next, err)
	}
	if _, _, err := ListOwnedProjectsPage(ctx, fs, "owner", 1, "%%%"); !errors.Is(err, ErrProjectPageTokenInvalid) {
		t.Fatalf("invalid cursor error = %v, want invalid token", err)
	}
}
