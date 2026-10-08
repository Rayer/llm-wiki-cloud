package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"golang.org/x/crypto/bcrypt"
)

func TestEnsureDemoAccountCreatesMissingAndPreservesExistingData(t *testing.T) {
	_, client := newIdentityEmulatorRepository(t)
	ctx := context.Background()
	scope := fmt.Sprintf("lwc372-demo-%d", time.Now().UnixNano())
	if err := scopedfirestore.RegisterLocalScope(client, scope); err != nil {
		t.Fatal(err)
	}
	userID := "demo-lwc372-" + fmt.Sprint(time.Now().UnixNano())
	email := fmt.Sprintf("demo-lwc372-%d@example.test", time.Now().UnixNano())
	users := scopedfirestore.Collection(client, "users")
	reservations := scopedfirestore.Collection(client, EmailReservationsCollection)
	projectRef := users.Doc(userID).Collection("projects").Doc(defaultProjectID)
	reservationRef := reservations.Doc(emailReservationDocumentID(CanonicalizeEmail(email)))
	defer func() {
		_, _ = projectRef.Collection("wiki").Doc("preserved").Delete(ctx)
		_, _ = projectRef.Delete(ctx)
		_, _ = users.Doc(userID).Delete(ctx)
		_, _ = reservationRef.Delete(ctx)
		_ = scopedfirestore.RegisterLocalScope(client, "")
		_ = client.Close()
	}()

	created, err := EnsureDemoAccount(ctx, client, userID, email, "member")
	if err != nil || !created {
		t.Fatalf("first ensure=(%t,%v), want a new Demo account", created, err)
	}
	userSnapshot, err := users.Doc(userID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	user, err := decodeUserRecord(userSnapshot)
	if err != nil || user.Email != email || user.EmailCanonical != CanonicalizeEmail(email) ||
		user.Role != "member" || user.DefaultProject != defaultProjectID || user.PasswordHash == "" {
		t.Fatalf("new Demo user=%+v error=%v", user, err)
	}
	if _, err := bcrypt.Cost([]byte(user.PasswordHash)); err != nil {
		t.Fatalf("new Demo password was not stored as a bcrypt hash: %v", err)
	}
	projectSnapshot, err := projectRef.Get(ctx)
	if err != nil || projectSnapshot.Data()["name"] != "My First Wiki" {
		t.Fatalf("default project=%v error=%v", projectSnapshot, err)
	}
	if count, err := CountProjects(ctx, client, userID); err != nil || count != 1 {
		t.Fatalf("default project count=%d error=%v, want exactly one", count, err)
	}
	reservation, err := reservationRef.Get(ctx)
	if err != nil || !reservation.Exists() {
		t.Fatalf("email reservation exists=%v error=%v", reservation != nil && reservation.Exists(), err)
	}

	if _, err := projectRef.Set(ctx, map[string]any{"name": "Existing project", "custom": "preserve"}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	contentRef := projectRef.Collection("wiki").Doc("preserved")
	if _, err := contentRef.Set(ctx, map[string]any{"body": "existing content"}); err != nil {
		t.Fatal(err)
	}
	if created, err := EnsureDemoAccount(ctx, client, userID, email, "member"); err != nil || created {
		t.Fatalf("repeat ensure=(%t,%v), want read-only reuse", created, err)
	}
	userAfter, err := users.Doc(userID).Get(ctx)
	if err != nil || userAfter.Data()["password_hash"] != user.PasswordHash || userAfter.Data()["role"] != "member" {
		t.Fatalf("existing Demo user changed: data=%v error=%v", userAfter.Data(), err)
	}
	projectAfter, err := projectRef.Get(ctx)
	if err != nil || projectAfter.Data()["name"] != "Existing project" || projectAfter.Data()["custom"] != "preserve" {
		t.Fatalf("existing project changed: data=%v error=%v", projectAfter.Data(), err)
	}
	contentAfter, err := contentRef.Get(ctx)
	if err != nil || contentAfter.Data()["body"] != "existing content" {
		t.Fatalf("existing project content changed: data=%v error=%v", contentAfter.Data(), err)
	}
}

func TestEnsureDemoAccountRejectsLegacyEmailCollisionWithoutMutation(t *testing.T) {
	_, client := newIdentityEmulatorRepository(t)
	ctx := context.Background()
	scope := fmt.Sprintf("lwc372-collision-%d", time.Now().UnixNano())
	if err := scopedfirestore.RegisterLocalScope(client, scope); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = scopedfirestore.RegisterLocalScope(client, "")
		_ = client.Close()
	}()

	legacyID := "legacy-demo-admin-" + fmt.Sprint(time.Now().UnixNano())
	configuredID := "demo-lwc372-" + fmt.Sprint(time.Now().UnixNano())
	email := fmt.Sprintf("collision-lwc372-%d@example.test", time.Now().UnixNano())
	users := scopedfirestore.Collection(client, "users")
	reservationRef := scopedfirestore.Collection(client, EmailReservationsCollection).Doc(emailReservationDocumentID(CanonicalizeEmail(email)))
	if _, err := users.Doc(legacyID).Set(ctx, UserRecord{Email: email, Role: "admin", DefaultProject: defaultProjectID}); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = users.Doc(legacyID).Delete(ctx); _, _ = reservationRef.Delete(ctx) }()

	if created, err := EnsureDemoAccount(ctx, client, configuredID, email, "member"); !errors.Is(err, ErrDemoIdentityConflict) || created {
		t.Fatalf("collision ensure=(%t,%v), want conflict and no create", created, err)
	}
	if snapshot, err := users.Doc(configuredID).Get(ctx); err == nil || snapshot.Exists() {
		t.Fatalf("conflicting configured UID was created: snapshot=%v error=%v", snapshot, err)
	}
	legacy, err := users.Doc(legacyID).Get(ctx)
	if err != nil || legacy.Data()["role"] != "admin" || legacy.Data()["email"] != email {
		t.Fatalf("legacy account changed: data=%v error=%v", legacy.Data(), err)
	}
}

func TestEnsureDemoAccountConcurrentStartupCreatesOneIdentity(t *testing.T) {
	_, client := newIdentityEmulatorRepository(t)
	ctx := context.Background()
	scope := fmt.Sprintf("lwc372-concurrent-%d", time.Now().UnixNano())
	if err := scopedfirestore.RegisterLocalScope(client, scope); err != nil {
		t.Fatal(err)
	}
	userID := "demo-lwc372-" + fmt.Sprint(time.Now().UnixNano())
	email := fmt.Sprintf("concurrent-lwc372-%d@example.test", time.Now().UnixNano())
	users := scopedfirestore.Collection(client, "users")
	reservationRef := scopedfirestore.Collection(client, EmailReservationsCollection).Doc(emailReservationDocumentID(CanonicalizeEmail(email)))
	projectRef := users.Doc(userID).Collection("projects").Doc(defaultProjectID)
	defer func() {
		_, _ = projectRef.Delete(ctx)
		_, _ = users.Doc(userID).Delete(ctx)
		_, _ = reservationRef.Delete(ctx)
		_ = scopedfirestore.RegisterLocalScope(client, "")
		_ = client.Close()
	}()

	start := make(chan struct{})
	created := make(chan bool, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			wasCreated, err := EnsureDemoAccount(ctx, client, userID, email, "member")
			created <- wasCreated
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(created)
	close(errs)
	createdCount := 0
	for wasCreated := range created {
		if wasCreated {
			createdCount++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensure failed: %v", err)
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent creation count=%d, want exactly one", createdCount)
	}
}
