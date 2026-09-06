package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func TestCanonicalizeEmailUsesOnlyTrimAndLowercase(t *testing.T) {
	tests := map[string]string{
		"  Alice@Example.COM  ": "alice@example.com",
		"Alice+tag@gmail.com":   "alice+tag@gmail.com",
		"User.Name@gmail.com":   "user.name@gmail.com",
	}
	for input, want := range tests {
		if got := CanonicalizeEmail(input); got != want {
			t.Fatalf("CanonicalizeEmail(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIdentityDocumentIDsDoNotContainSensitiveTupleValues(t *testing.T) {
	canonical := "alice@example.com"
	emailID := emailReservationDocumentID(canonical)
	if emailID == canonical || strings.Contains(emailID, "@") {
		t.Fatalf("email reservation document ID %q contains the canonical email", emailID)
	}
	provider, issuer, subject := "google", "https://accounts.example.test", "opaque-provider-subject"
	identityID := externalIdentityDocumentID(provider, issuer, subject)
	for _, value := range []string{provider, issuer, subject} {
		if strings.Contains(identityID, value) {
			t.Fatalf("external identity document ID %q contains tuple value %q", identityID, value)
		}
	}
}

func TestRegisterHandlerPassesCanonicalAndDisplayEmailToAtomicProvisioning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &recordingRegistrationStore{}
	router := gin.New()
	router.POST("/register", RegisterHandlerWithRepository(store, "test-secret", nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"email":"  Alice@Example.COM  ","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if store.input.DisplayEmail != "Alice@Example.COM" || store.input.CanonicalEmail != "alice@example.com" {
		t.Fatalf("provision input = %+v, want display/canonical split", store.input)
	}
	if store.input.ProjectID != defaultProjectID || store.input.UserID == "" || store.input.PasswordHash == "" {
		t.Fatalf("provision input missing atomic registration fields: %+v", store.input)
	}
}

func TestRegistrationConflictIsSafeAndDoesNotCreateASecondUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &recordingRegistrationStore{err: ErrCanonicalEmailConflict}
	router := gin.New()
	router.POST("/register", RegisterHandlerWithRepository(store, "test-secret", nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"email":"user@example.com","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if store.calls != 1 {
		t.Fatalf("provision calls = %d, want one atomic attempt", store.calls)
	}
}

func TestRegistrationAtomicStoreFailureDoesNotReportSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &recordingRegistrationStore{err: errors.New("transaction unavailable")}
	router := gin.New()
	router.POST("/register", RegisterHandlerWithRepository(store, "test-secret", nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"email":"user@example.com","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestLoginHandlerResolvesCanonicalPrimaryEmail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &recordingLoginStore{userID: "stable-user", user: &UserRecord{Email: "Display@Example.com", PasswordHash: mustHashForIdentityTest(t, "password123")}}
	router := gin.New()
	router.POST("/login", LoginHandlerWithRepository(store, "test-secret", LegacyRefreshCookiePolicy()))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"email":"  DISPLAY@example.COM ","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if store.canonicalEmail != "display@example.com" {
		t.Fatalf("login canonical email = %q", store.canonicalEmail)
	}
}

type recordingRegistrationStore struct {
	input PasswordUserProvisioning
	err   error
	calls int
}

type recordingLoginStore struct {
	canonicalEmail string
	userID         string
	user           *UserRecord
}

func (s *recordingLoginStore) GetPasswordUserByEmail(_ context.Context, canonicalEmail string) (string, *UserRecord, error) {
	s.canonicalEmail = canonicalEmail
	return s.userID, s.user, nil
}

func mustHashForIdentityTest(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash test password: %v", err)
	}
	return string(hash)
}

func (s *recordingRegistrationStore) ProvisionPasswordUser(_ context.Context, input PasswordUserProvisioning) error {
	s.calls++
	s.input = input
	return s.err
}
