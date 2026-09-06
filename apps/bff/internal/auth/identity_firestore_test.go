package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"google.golang.org/api/option"
)

func TestRegistrationHandlerWithFirestoreCommitsIdentityBoundary(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	gin.SetMode(gin.TestMode)
	email := fmt.Sprintf("Handler-%d@example.test", time.Now().UnixNano())
	router := gin.New()
	router.POST("/register", RegisterHandlerWithRepository(repo, "test-secret", nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/register", bytes.NewBufferString(`{"email":"`+email+`","password":"password123"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("registration status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response RegisterResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode registration response: %v", err)
	}
	t.Cleanup(func() { cleanupIdentityFixtures(t, client, response.UserID, "", email) })
	if response.Email != email || response.ProjectID != defaultProjectID || response.UserID == "" {
		t.Fatalf("registration response = %+v", response)
	}
	reservation, err := repo.GetCanonicalEmailReservation(context.Background(), email)
	if err != nil || reservation == nil || reservation.UserID != response.UserID {
		t.Fatalf("reservation = %#v, error = %v", reservation, err)
	}
	user, err := client.Collection("users").Doc(response.UserID).Get(context.Background())
	if err != nil || user == nil || !user.Exists() {
		t.Fatalf("user exists=%v, error=%v", user != nil && user.Exists(), err)
	}
	project, err := client.Collection("users").Doc(response.UserID).Collection("projects").Doc(defaultProjectID).Get(context.Background())
	if err != nil || project == nil || !project.Exists() {
		t.Fatalf("project exists=%v, error=%v", project != nil && project.Exists(), err)
	}
}

func TestIdentityRepositoryProvisionExternalUserCreatesPasswordlessBoundary(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	input := ExternalUserProvisioning{
		UserID:         "external-success-user-315",
		DisplayEmail:   "Display.External@example.test",
		CanonicalEmail: "display.external@example.test",
		Provider:       "test-provider",
		Issuer:         "https://issuer.example.test",
		Subject:        "external-success-subject-315",
		EmailVerified:  true,
	}
	cleanupExternalFixture(t, client, input)

	if err := repo.ProvisionExternalUser(ctx, input); err != nil {
		t.Fatalf("external provisioning: %v", err)
	}
	userSnapshot, err := client.Collection("users").Doc(input.UserID).Get(ctx)
	if err != nil {
		t.Fatalf("read external user: %v", err)
	}
	user, err := decodeUserRecord(userSnapshot)
	if err != nil || user.Email != input.DisplayEmail || user.EmailCanonical != input.CanonicalEmail || user.PasswordHash != "" || !user.EmailVerified {
		t.Fatalf("external user = %#v, error = %v", user, err)
	}
	reservation, err := repo.GetCanonicalEmailReservation(ctx, input.CanonicalEmail)
	if err != nil || reservation == nil || reservation.UserID != input.UserID {
		t.Fatalf("external reservation = %#v, error = %v", reservation, err)
	}
	identity, err := repo.GetExternalIdentity(ctx, input.Provider, input.Issuer, input.Subject)
	if err != nil || identity == nil || identity.UserID != input.UserID {
		t.Fatalf("external identity = %#v, error = %v", identity, err)
	}
	project, err := client.Collection("users").Doc(input.UserID).Collection("projects").Doc(defaultProjectID).Get(ctx)
	if err != nil || !project.Exists() {
		t.Fatalf("external default project exists=%v, error=%v", project.Exists(), err)
	}
	if _, _, err := repo.GetPasswordUserByEmail(ctx, input.CanonicalEmail); !errors.Is(err, errPasswordLoginUnavailable) {
		t.Fatalf("password lookup for external user error = %v, want unavailable", err)
	}
}

func TestIdentityRepositoryRejectsUnverifiedExternalProvisioningWithoutWrites(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	input := ExternalUserProvisioning{
		UserID:         "external-unverified-user-315",
		DisplayEmail:   "unverified.external@example.test",
		CanonicalEmail: "unverified.external@example.test",
		Provider:       "unverified-provider",
		Issuer:         "https://unverified-issuer.example.test",
		Subject:        "unverified-subject-315",
	}
	cleanupExternalFixture(t, client, input)

	if err := repo.ProvisionExternalUser(ctx, input); !errors.Is(err, ErrInvalidIdentityInput) {
		t.Fatalf("unverified external provisioning error = %v, want invalid input", err)
	}
	if got := countExistingExternalBoundary(ctx, client, input); got != 0 {
		t.Fatalf("unverified external boundary records = %d, want zero", got)
	}
}

func TestIdentityRepositoryConcurrentExternalProvisioningConflictsEmailAndIdentity(t *testing.T) {
	tests := []struct {
		name         string
		inputs       []ExternalUserProvisioning
		wantConflict error
	}{
		{
			name: "canonical email",
			inputs: []ExternalUserProvisioning{
				{UserID: "external-email-a-315", DisplayEmail: "same.external@example.test", CanonicalEmail: "same.external@example.test", Provider: "provider-a", Issuer: "https://issuer-a.example.test", Subject: "subject-a-315", EmailVerified: true},
				{UserID: "external-email-b-315", DisplayEmail: "SAME.EXTERNAL@example.test", CanonicalEmail: "same.external@example.test", Provider: "provider-b", Issuer: "https://issuer-b.example.test", Subject: "subject-b-315", EmailVerified: true},
			},
			wantConflict: ErrCanonicalEmailConflict,
		},
		{
			name: "external identity",
			inputs: []ExternalUserProvisioning{
				{UserID: "external-identity-a-315", DisplayEmail: "identity-a@example.test", CanonicalEmail: "identity-a@example.test", Provider: "provider-shared", Issuer: "https://issuer-shared.example.test", Subject: "subject-shared-315", EmailVerified: true},
				{UserID: "external-identity-b-315", DisplayEmail: "identity-b@example.test", CanonicalEmail: "identity-b@example.test", Provider: "provider-shared", Issuer: "https://issuer-shared.example.test", Subject: "subject-shared-315", EmailVerified: true},
			},
			wantConflict: ErrExternalIdentityConflict,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, client := newIdentityEmulatorRepository(t)
			defer client.Close()
			ctx := context.Background()
			for _, input := range test.inputs {
				cleanupExternalFixture(t, client, input)
			}

			start := make(chan struct{})
			errs := make(chan error, len(test.inputs))
			var wait sync.WaitGroup
			for _, input := range test.inputs {
				input := input
				wait.Add(1)
				go func() {
					defer wait.Done()
					<-start
					errs <- repo.ProvisionExternalUser(ctx, input)
				}()
			}
			close(start)
			wait.Wait()
			close(errs)

			got := make([]error, 0, len(test.inputs))
			for err := range errs {
				got = append(got, err)
			}
			if len(got) != len(test.inputs) {
				t.Fatalf("concurrent result count = %d, want %d", len(got), len(test.inputs))
			}
			successes, conflicts := 0, 0
			for _, err := range got {
				if err == nil {
					successes++
				} else if errors.Is(err, test.wantConflict) {
					conflicts++
				} else {
					t.Fatalf("concurrent external provisioning error = %v", err)
				}
			}
			if successes != 1 || conflicts != 1 {
				t.Fatalf("concurrent external provisioning successes=%d conflicts=%d, want one each", successes, conflicts)
			}
		})
	}
}

func TestIdentityRepositoryExternalProvisioningRetryAndRollbackAreAtomic(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	input := ExternalUserProvisioning{
		UserID:         "external-retry-user-315",
		DisplayEmail:   "retry.external@example.test",
		CanonicalEmail: "retry.external@example.test",
		Provider:       "retry-provider",
		Issuer:         "https://retry-issuer.example.test",
		Subject:        "retry-subject-315",
		EmailVerified:  true,
	}
	cleanupExternalFixture(t, client, input)
	if err := repo.ProvisionExternalUser(ctx, input); err != nil {
		t.Fatalf("first external provisioning: %v", err)
	}
	if err := repo.ProvisionExternalUser(ctx, input); err != nil {
		t.Fatalf("exact external retry: %v", err)
	}
	if got := countExistingExternalBoundary(ctx, client, input); got != 4 {
		t.Fatalf("external boundary records after retry = %d, want 4", got)
	}
	if _, err := client.Collection("users").Doc(input.UserID).Update(ctx, []firestore.Update{{Path: "email_verified", Value: false}}); err != nil {
		t.Fatalf("invalidate persisted email verification: %v", err)
	}
	if err := repo.ProvisionExternalUser(ctx, input); !errors.Is(err, ErrCanonicalEmailConflict) {
		t.Fatalf("retry with unverified persisted user error = %v, want conflict", err)
	}

	rollback := ExternalUserProvisioning{
		UserID:         "external-rollback-user-315",
		DisplayEmail:   "rollback.external@example.test",
		CanonicalEmail: "rollback.external@example.test",
		Provider:       "rollback-provider",
		Issuer:         "https://rollback-issuer.example.test",
		Subject:        "rollback-subject-315",
		EmailVerified:  true,
	}
	cleanupExternalFixture(t, client, rollback)
	err := repo.RunTransaction(ctx, func(tx *IdentityTransaction) error {
		if err := tx.ProvisionExternalUser(rollback); err != nil {
			return err
		}
		return errors.New("abort external provisioning test transaction")
	})
	if err == nil {
		t.Fatal("external rollback transaction succeeded, want abort")
	}
	if got := countExistingExternalBoundary(ctx, client, rollback); got != 0 {
		t.Fatalf("external boundary records after rollback = %d, want zero", got)
	}
}

func TestIdentityAuditAcceptsPasswordlessExternalUser(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	input := ExternalUserProvisioning{
		UserID:         "external-audit-user-315",
		DisplayEmail:   "audit.external@example.test",
		CanonicalEmail: "audit.external@example.test",
		Provider:       "audit-provider",
		Issuer:         "https://audit-issuer.example.test",
		Subject:        "audit-subject-315",
		EmailVerified:  true,
	}
	cleanupExternalFixture(t, client, input)
	cleanupIdentityFixtures(t, client, "collision-a-315", "collision-b-315", "collision@example.test")
	t.Cleanup(func() {
		cleanupExternalFixture(t, client, input)
		cleanupIdentityFixtures(t, client, "collision-a-315", "collision-b-315", "collision@example.test")
	})
	if err := repo.ProvisionExternalUser(ctx, input); err != nil {
		t.Fatalf("external audit fixture: %v", err)
	}
	report, err := repo.AuditAndBackfill(ctx, false)
	if err != nil {
		t.Fatalf("passwordless external audit: %v", err)
	}
	if report.UsersScanned < 1 || report.ReservationsPresent < 1 || report.MalformedRecordCount != 0 {
		t.Fatalf("passwordless external audit report = %+v", report)
	}
}

func TestIdentityRepositoryConcurrentProvisioningReservesOneCanonicalEmail(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	canonical := fmt.Sprintf("concurrent-%d@example.test", time.Now().UnixNano())
	inputs := []PasswordUserProvisioning{
		{UserID: "user-a-315", DisplayEmail: canonical, CanonicalEmail: canonical, PasswordHash: "hash-a"},
		{UserID: "user-b-315", DisplayEmail: strings.ToUpper(canonical), CanonicalEmail: canonical, PasswordHash: "hash-b"},
	}
	cleanupIdentityFixtures(t, client, inputs[0].UserID, inputs[1].UserID, canonical)

	start := make(chan struct{})
	errs := make(chan error, len(inputs))
	var wait sync.WaitGroup
	for _, input := range inputs {
		input := input
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errs <- repo.ProvisionPasswordUser(ctx, input)
		}()
	}
	close(start)
	wait.Wait()
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrCanonicalEmailConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent provisioning error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent provisioning successes=%d conflicts=%d, want one each", successes, conflicts)
	}
	reservation, err := repo.GetCanonicalEmailReservation(ctx, canonical)
	if err != nil || reservation == nil {
		t.Fatalf("reservation = %#v, error = %v", reservation, err)
	}
	if got := countExistingUsers(ctx, client, inputs); got != 1 {
		t.Fatalf("user documents created = %d, want one", got)
	}
}

func TestIdentityRepositoryProvisioningIsIdempotentAndRollbackIsWriteFree(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	input := PasswordUserProvisioning{
		UserID:         "idempotent-user-315",
		DisplayEmail:   "Display@example.test",
		CanonicalEmail: "display@example.test",
		PasswordHash:   "hash",
	}
	cleanupIdentityFixtures(t, client, input.UserID, "", input.CanonicalEmail)

	if err := repo.ProvisionPasswordUser(ctx, input); err != nil {
		t.Fatalf("first provisioning: %v", err)
	}
	if err := repo.ProvisionPasswordUser(ctx, input); err != nil {
		t.Fatalf("idempotent provisioning: %v", err)
	}
	if got := countExistingUsers(ctx, client, []PasswordUserProvisioning{input}); got != 1 {
		t.Fatalf("user documents after retry = %d, want one", got)
	}
	project, err := client.Collection("users").Doc(input.UserID).Collection("projects").Doc(defaultProjectID).Get(ctx)
	if err != nil || project == nil || !project.Exists() {
		t.Fatalf("default project exists=%v, error=%v", project != nil && project.Exists(), err)
	}

	rollbackEmail := "rollback@example.test"
	cleanupIdentityFixtures(t, client, "rollback-user-315", "", rollbackEmail)
	err = repo.RunTransaction(ctx, func(tx *IdentityTransaction) error {
		if err := tx.ProvisionPasswordUser(PasswordUserProvisioning{
			UserID:         "rollback-user-315",
			DisplayEmail:   rollbackEmail,
			CanonicalEmail: rollbackEmail,
			PasswordHash:   "rollback-hash",
		}); err != nil {
			return err
		}
		return errors.New("abort test transaction")
	})
	if err == nil {
		t.Fatal("rollback transaction succeeded, want abort")
	}
	reservation, err := repo.GetCanonicalEmailReservation(ctx, rollbackEmail)
	if err != nil {
		t.Fatalf("read rolled-back reservation: %v", err)
	}
	if reservation != nil {
		t.Fatal("aborted transaction left an email reservation")
	}
	if _, err := client.Collection("users").Doc("rollback-user-315").Get(ctx); err == nil {
		t.Fatal("aborted transaction left a user")
	}
}

func TestIdentityRepositoryConcurrentExternalLinkingHasOneOwner(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	provider, issuer, subject := "google", "https://issuer.example.test", fmt.Sprintf("subject-%d", time.Now().UnixNano())
	ref := client.Collection(ExternalIdentitiesCollection).Doc(externalIdentityDocumentID(provider, issuer, subject))
	defer ref.Delete(ctx)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, userID := range []string{"external-user-a-315", "external-user-b-315"} {
		userID := userID
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errs <- repo.LinkExternalIdentity(ctx, provider, issuer, subject, userID)
		}()
	}
	close(start)
	wait.Wait()
	close(errs)

	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrExternalIdentityConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent external linking error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("external linking successes=%d conflicts=%d, want one each", successes, conflicts)
	}
	identity, err := repo.GetExternalIdentity(ctx, provider, issuer, subject)
	if err != nil || identity == nil {
		t.Fatalf("external identity = %#v, error = %v", identity, err)
	}
}

func TestIdentityAuditDryRunApplyAndCollisionAreSafe(t *testing.T) {
	repo, client := newIdentityEmulatorRepository(t)
	defer client.Close()
	ctx := context.Background()
	userID := "audit-user-315"
	email := "Audit.User@example.test"
	cleanupIdentityFixtures(t, client, userID, "", email)
	cleanupIdentityFixtures(t, client, "collision-a-315", "collision-b-315", "collision@example.test")
	_, err := client.Collection("users").Doc(userID).Set(ctx, UserRecord{Email: email, PasswordHash: "hash"})
	if err != nil {
		t.Fatalf("seed legacy user: %v", err)
	}
	if err := repo.ProvisionPasswordUser(ctx, PasswordUserProvisioning{
		UserID:         "audit-new-user-315",
		DisplayEmail:   "audit.user@example.test",
		CanonicalEmail: "audit.user@example.test",
		PasswordHash:   "different-hash",
	}); !errors.Is(err, ErrCanonicalEmailConflict) {
		t.Fatalf("legacy canonical collision error = %v, want conflict", err)
	}

	report, err := repo.AuditAndBackfill(ctx, false)
	if err != nil {
		t.Fatalf("dry-run audit: %v", err)
	}
	if report.UsersScanned < 1 || report.ReservationsMissing < 1 || report.ReservationsCreated != 0 {
		t.Fatalf("dry-run report = %+v", report)
	}
	if reservation, readErr := repo.GetCanonicalEmailReservation(ctx, email); readErr != nil || reservation != nil {
		t.Fatalf("dry-run reservation=%#v error=%v, want no write", reservation, readErr)
	}

	report, err = repo.AuditAndBackfill(ctx, true)
	if err != nil || report.ReservationsCreated < 1 {
		t.Fatalf("apply report=%+v error=%v", report, err)
	}
	report, err = repo.AuditAndBackfill(ctx, true)
	if err != nil || report.ReservationsMissing != 0 || report.ReservationsCreated != 0 {
		t.Fatalf("idempotent apply report=%+v error=%v", report, err)
	}

	collisionA, collisionB := "collision-a-315", "collision-b-315"
	collisionEmail := "collision@example.test"
	cleanupIdentityFixtures(t, client, collisionA, collisionB, collisionEmail)
	t.Cleanup(func() { cleanupIdentityFixtures(t, client, collisionA, collisionB, collisionEmail) })
	for _, id := range []string{collisionA, collisionB} {
		if _, err := client.Collection("users").Doc(id).Set(ctx, UserRecord{Email: collisionEmail, PasswordHash: "hash"}); err != nil {
			t.Fatalf("seed collision user: %v", err)
		}
	}
	if _, err := repo.AuditAndBackfill(ctx, true); err == nil {
		t.Fatal("collision apply succeeded, want validation failure")
	}
	if reservation, readErr := repo.GetCanonicalEmailReservation(ctx, collisionEmail); readErr != nil || reservation != nil {
		t.Fatalf("collision apply reservation=%#v error=%v, want zero writes", reservation, readErr)
	}
}

func newIdentityEmulatorRepository(t *testing.T) (*IdentityRepository, *firestore.Client) {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid FIRESTORE_EMULATOR_HOST")
	}
	probe, err := net.DialTimeout("tcp", parsed.Host, 2*time.Second)
	if err != nil {
		t.Fatalf("Firestore emulator at %s is unreachable: %v", parsed.Host, err)
	}
	_ = probe.Close()
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "lwc-315-test", option.WithEndpoint(endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	return NewIdentityRepository(client), client
}

func cleanupIdentityFixtures(t *testing.T, client *firestore.Client, userA, userB, email string) {
	t.Helper()
	ctx := context.Background()
	for _, userID := range []string{userA, userB} {
		if userID == "" {
			continue
		}
		_, _ = client.Collection("users").Doc(userID).Collection("projects").Doc(defaultProjectID).Delete(ctx)
		_, _ = client.Collection("users").Doc(userID).Delete(ctx)
	}
	if email != "" {
		_, _ = client.Collection(EmailReservationsCollection).Doc(emailReservationDocumentID(CanonicalizeEmail(email))).Delete(ctx)
	}
}

func cleanupExternalFixture(t *testing.T, client *firestore.Client, input ExternalUserProvisioning) {
	t.Helper()
	cleanupIdentityFixtures(t, client, input.UserID, "", input.CanonicalEmail)
	ctx := context.Background()
	_, _ = client.Collection(ExternalIdentitiesCollection).Doc(externalIdentityDocumentID(input.Provider, input.Issuer, input.Subject)).Delete(ctx)
}

func countExistingExternalBoundary(ctx context.Context, client *firestore.Client, input ExternalUserProvisioning) int {
	count := 0
	refs := []*firestore.DocumentRef{
		client.Collection("users").Doc(input.UserID),
		client.Collection(EmailReservationsCollection).Doc(emailReservationDocumentID(input.CanonicalEmail)),
		client.Collection(ExternalIdentitiesCollection).Doc(externalIdentityDocumentID(input.Provider, input.Issuer, input.Subject)),
		client.Collection("users").Doc(input.UserID).Collection("projects").Doc(defaultProjectID),
	}
	for _, ref := range refs {
		if snapshot, err := ref.Get(ctx); err == nil && snapshot.Exists() {
			count++
		}
	}
	return count
}

func countExistingUsers(ctx context.Context, client *firestore.Client, inputs []PasswordUserProvisioning) int {
	count := 0
	for _, input := range inputs {
		if _, err := client.Collection("users").Doc(input.UserID).Get(ctx); err == nil {
			count++
		}
	}
	return count
}
