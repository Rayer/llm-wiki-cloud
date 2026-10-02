package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

const (
	fixtureEmail       = "demo.rotation@example.test"
	fixtureOldPassword = "synthetic-old-password"
	fixtureNewPassword = "synthetic-new-password"
	fixtureJWTSecret   = "synthetic-test-signing-key"
)

type memoryRotationStore struct {
	user           auth.UserRecord
	data           map[string]interface{}
	sidecars       map[string]interface{}
	missing        bool
	readErrorAt    int
	readError      error
	reads          int
	updateCalls    int
	updateError    error
	commitOnError  bool
	afterFirstRead func(*memoryRotationStore)
}

func newMemoryRotationStore(t *testing.T) *memoryRotationStore {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(fixtureOldPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal("could not prepare synthetic password fixture")
	}
	before := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	data := map[string]interface{}{
		"email": fixtureEmail, "email_canonical": fixtureEmail, "password_hash": string(hash),
		"role": "demo", "status": auth.AccountActive, "auth_version": int64(4),
		"auth_invalid_before": before, "email_verified": true, "project_count": 2,
		"default_project": "default", "extension": map[string]interface{}{"kept": "yes"},
	}
	return &memoryRotationStore{
		user: auth.UserRecord{
			Email: fixtureEmail, EmailCanonical: fixtureEmail, PasswordHash: string(hash),
			Role: "demo", Status: auth.AccountActive, AuthVersion: 4,
			AuthInvalidBefore: before, EmailVerified: true, ProjectCount: 2, DefaultProject: "default",
		},
		data: data,
		sidecars: map[string]interface{}{
			"email_reservation": map[string]interface{}{"canonical_email": fixtureEmail, "user_id": devDemoUserID},
			"project":           map[string]interface{}{"name": "Fixture project", "owner_id": devDemoUserID},
			"refresh_session":   map[string]interface{}{"status": "active", "auth_version": int64(4)},
		},
	}
}

func (s *memoryRotationStore) readUser(_ context.Context, userID string) (rotationSnapshot, error) {
	s.reads++
	if s.readError != nil && s.reads == s.readErrorAt {
		return rotationSnapshot{}, s.readError
	}
	if s.missing {
		return rotationSnapshot{}, errUserMissing
	}
	if userID != devDemoUserID {
		return rotationSnapshot{}, errUserMissing
	}
	snapshot := rotationSnapshot{user: s.user, data: cloneMap(s.data)}
	if s.reads == 1 && s.afterFirstRead != nil {
		s.afterFirstRead(s)
	}
	return snapshot, nil
}

func (s *memoryRotationStore) updatePasswordHash(_ context.Context, userID string, expected rotationSnapshot, passwordHash string) error {
	s.updateCalls++
	if userID != devDemoUserID || s.missing || !s.user.Active() ||
		s.user.PasswordHash != expected.user.PasswordHash ||
		!reflect.DeepEqual(withoutPasswordHash(s.data), withoutPasswordHash(expected.data)) {
		return errRotationConflict
	}
	if s.updateError != nil && !s.commitOnError {
		return s.updateError
	}
	s.user.PasswordHash = passwordHash
	s.data["password_hash"] = passwordHash
	if s.updateError != nil {
		return s.updateError
	}
	return nil
}

func (s *memoryRotationStore) GetPasswordUserByEmail(_ context.Context, email string) (string, *auth.UserRecord, error) {
	if s.missing || auth.CanonicalizeEmail(s.user.Email) != email {
		return "", nil, errors.New("fixture login identity unavailable")
	}
	user := s.user
	return devDemoUserID, &user, nil
}

func cloneMap(source map[string]interface{}) map[string]interface{} {
	copy := make(map[string]interface{}, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func devTargetArgs() []string {
	return []string{"--project", devProjectID, "--database", devDatabaseID, "--user-id", devDemoUserID}
}

func TestRunDefaultsToReadOnlyDryRun(t *testing.T) {
	t.Setenv("FIRESTORE_EMULATOR_HOST", "")
	store := newMemoryRotationStore(t)
	openCalled, factoryCalled := false, false
	var output bytes.Buffer
	err := run(devTargetArgs(), func(int) (io.ReadCloser, error) {
		openCalled = true
		return nil, errPasswordInput
	}, func(_ context.Context, got target) (rotationStore, func(), error) {
		factoryCalled = true
		if got.project != devProjectID || got.database != devDatabaseID || got.userID != devDemoUserID {
			t.Fatal("dry-run factory received an unexpected target")
		}
		return store, nil, nil
	}, &output)
	if err != nil || openCalled || !factoryCalled || store.updateCalls != 0 {
		t.Fatal("default command did not complete as a read-only dry-run")
	}
	var receipt rotationReceipt
	if json.Unmarshal(output.Bytes(), &receipt) != nil || receipt.Action != "dry_run" || receipt.Outcome != "ready" || receipt.UserID != devDemoUserID {
		t.Fatal("dry-run receipt did not identify the verified target")
	}
}

func TestRunRejectsUnapprovedTargetsBeforeOpeningInputOrStore(t *testing.T) {
	t.Setenv("FIRESTORE_EMULATOR_HOST", "")
	tests := []struct {
		name string
		args []string
	}{
		{name: "blank project", args: []string{"--database", devDatabaseID, "--user-id", devDemoUserID}},
		{name: "blank database", args: []string{"--project", devProjectID, "--user-id", devDemoUserID}},
		{name: "default database", args: []string{"--project", devProjectID, "--database", "(default)", "--user-id", devDemoUserID}},
		{name: "production database", args: []string{"--project", devProjectID, "--database", "llm-wiki-cloud-prod", "--user-id", devDemoUserID}},
		{name: "other project", args: []string{"--project", "other-project", "--database", devDatabaseID, "--user-id", devDemoUserID}},
		{name: "other identity", args: []string{"--project", devProjectID, "--database", devDatabaseID, "--user-id", "other-demo-user"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			openCalled, factoryCalled := false, false
			args := append(append([]string{}, test.args...), "--apply", "--password-fd", "3")
			err := run(args, func(int) (io.ReadCloser, error) {
				openCalled = true
				return io.NopCloser(strings.NewReader(fixtureNewPassword)), nil
			}, func(context.Context, target) (rotationStore, func(), error) {
				factoryCalled = true
				return nil, nil, nil
			}, io.Discard)
			if !errors.Is(err, errTargetRejected) || openCalled || factoryCalled {
				t.Fatal("unapproved target was not rejected before password or store access")
			}
		})
	}
}

func TestTargetValidationRequiresExplicitSyntheticLoopbackForEmulator(t *testing.T) {
	t.Setenv("FIRESTORE_EMULATOR_HOST", "")
	emulator := target{project: emulatorProjectID, database: emulatorDatabase, userID: emulatorUserID, emulatorHost: "127.0.0.1:8080"}
	if err := validateTarget(emulator); err != nil {
		t.Fatal("explicit synthetic loopback target was rejected")
	}
	for _, badHost := range []string{"localhost:8080", "192.0.2.4:8080", "127.0.0.1"} {
		bad := emulator
		bad.emulatorHost = badHost
		if !errors.Is(validateTarget(bad), errTargetRejected) {
			t.Fatal("non-numeric or non-loopback emulator address was accepted")
		}
	}
	badIdentity := emulator
	badIdentity.userID = devDemoUserID
	if !errors.Is(validateTarget(badIdentity), errTargetRejected) {
		t.Fatal("live identity was accepted with emulator target")
	}
	t.Setenv("FIRESTORE_EMULATOR_HOST", "127.0.0.1:9090")
	if !errors.Is(validateTarget(emulator), errTargetRejected) {
		t.Fatal("conflicting inherited emulator endpoint was accepted")
	}
}

func TestOptionsDoNotAcceptPasswordValues(t *testing.T) {
	if _, err := parseOptions(append(devTargetArgs(), "--password", fixtureNewPassword)); !errors.Is(err, errArguments) {
		t.Fatal("command accepted a password value flag")
	}
	if _, err := parseOptions(append(devTargetArgs(), "--apply")); !errors.Is(err, errArguments) {
		t.Fatal("apply was accepted without an explicit password file descriptor")
	}
}

func TestRotationRejectsMissingInactiveAndInvalidIdentity(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*memoryRotationStore)
		wantError error
	}{
		{name: "missing", setup: func(s *memoryRotationStore) { s.missing = true }, wantError: errUserMissing},
		{name: "inactive", setup: func(s *memoryRotationStore) { s.user.Status = auth.AccountSuspended }, wantError: errUserInactive},
		{name: "invalid canonical email", setup: func(s *memoryRotationStore) { s.user.EmailCanonical = "wrong@example.test" }, wantError: errIdentityInvalid},
		{name: "no password login", setup: func(s *memoryRotationStore) { s.user.PasswordHash = ""; s.data["password_hash"] = "" }, wantError: errNoPasswordLogin},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryRotationStore(t)
			test.setup(store)
			_, err := rotate(context.Background(), target{userID: devDemoUserID}, store, true, []byte(fixtureNewPassword))
			if !errors.Is(err, test.wantError) || store.updateCalls != 0 {
				t.Fatal("invalid existing identity reached the update path")
			}
		})
	}
}

func TestRunRejectsInvalidPasswordBeforeOpeningStore(t *testing.T) {
	t.Setenv("FIRESTORE_EMULATOR_HOST", "")
	for _, test := range []struct {
		name     string
		password string
	}{
		{name: "missing", password: ""},
		{name: "too short", password: "short"},
		{name: "too long", password: strings.Repeat("x", maxPasswordBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			factoryCalled := false
			args := append(devTargetArgs(), "--apply", "--password-fd", "3")
			err := run(args, func(int) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(test.password)), nil
			}, func(context.Context, target) (rotationStore, func(), error) {
				factoryCalled = true
				return nil, nil, nil
			}, io.Discard)
			if !errors.Is(err, errPasswordInvalid) || factoryCalled {
				t.Fatal("invalid password reached the identity store")
			}
		})
	}
}

func TestRotationConflictStopsOnConcurrentIdentityDrift(t *testing.T) {
	store := newMemoryRotationStore(t)
	store.afterFirstRead = func(s *memoryRotationStore) {
		s.user.Role = "changed-concurrently"
		s.data["role"] = "changed-concurrently"
	}
	receipt, err := rotate(context.Background(), target{userID: devDemoUserID}, store, true, []byte(fixtureNewPassword))
	if !errors.Is(err, errRotationConflict) || receipt.Outcome != "conflict" || store.updateCalls != 1 {
		t.Fatal("concurrent identity drift was not reported as a transaction conflict")
	}
	if store.user.PasswordHash != store.data["password_hash"] {
		t.Fatal("conflicting transaction changed the password hash")
	}
}

func TestApplyChangesOnlyPasswordHashAndPreservesLoginIdentityAndSessions(t *testing.T) {
	t.Setenv("FIRESTORE_EMULATOR_HOST", "")
	gin.SetMode(gin.TestMode)
	store := newMemoryRotationStore(t)
	before := cloneMap(store.data)
	sidecarsBefore := cloneMap(store.sidecars)
	var output bytes.Buffer
	args := append(devTargetArgs(), "--apply", "--password-fd", "3")
	err := run(args, func(fd int) (io.ReadCloser, error) {
		if fd != 3 {
			t.Fatal("password input was opened through an unexpected descriptor")
		}
		return io.NopCloser(strings.NewReader(fixtureNewPassword)), nil
	}, func(context.Context, target) (rotationStore, func(), error) {
		return store, nil, nil
	}, &output)
	if err != nil || store.updateCalls != 1 {
		t.Fatal("explicit replacement did not complete exactly one transaction")
	}
	var receipt rotationReceipt
	if json.Unmarshal(output.Bytes(), &receipt) != nil || receipt.Outcome != "replaced" || receipt.Action != "password_replace" || receipt.UserID != devDemoUserID {
		t.Fatal("replacement did not emit a bounded success receipt")
	}
	if strings.Contains(output.String(), fixtureOldPassword) || strings.Contains(output.String(), fixtureNewPassword) || strings.Contains(output.String(), store.user.PasswordHash) {
		t.Fatal("receipt exposed a password or password hash")
	}
	if bcrypt.CompareHashAndPassword([]byte(store.user.PasswordHash), []byte(fixtureOldPassword)) == nil ||
		bcrypt.CompareHashAndPassword([]byte(store.user.PasswordHash), []byte(fixtureNewPassword)) != nil {
		t.Fatal("stored password hash did not accept only the replacement fixture")
	}
	if !reflect.DeepEqual(withoutPasswordHash(before), withoutPasswordHash(store.data)) || !reflect.DeepEqual(sidecarsBefore, store.sidecars) {
		t.Fatal("replacement changed user fields other than password_hash or changed related records")
	}

	for name, policy := range map[string]auth.RefreshCookiePolicy{
		"Auth": auth.HostRefreshCookiePolicy(), "BFF": auth.LegacyRefreshCookiePolicy(),
	} {
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.POST("/login", auth.LoginHandlerWithRepository(store, fixtureJWTSecret, policy))
			login := func(password string) *httptest.ResponseRecorder {
				body, _ := json.Marshal(auth.LoginRequest{Email: fixtureEmail, Password: password})
				request := httptest.NewRequest(http.MethodPost, "https://fixture.example.test/login", bytes.NewReader(body))
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			if oldResponse := login(fixtureOldPassword); oldResponse.Code != http.StatusUnauthorized || len(oldResponse.Result().Cookies()) != 0 {
				t.Fatal("old synthetic password remained accepted")
			}
			newResponse := login(fixtureNewPassword)
			if newResponse.Code != http.StatusOK || len(newResponse.Result().Cookies()) != 1 {
				t.Fatal("replacement password did not use the existing successful login and refresh-cookie contract")
			}
			var loginResult auth.LoginResponse
			if json.Unmarshal(newResponse.Body.Bytes(), &loginResult) != nil || loginResult.User.ID != devDemoUserID || loginResult.User.Email != fixtureEmail || loginResult.User.Role != "demo" {
				t.Fatal("successful login did not preserve the existing UID, email, and role")
			}
		})
	}
	if !reflect.DeepEqual(sidecarsBefore, store.sidecars) {
		t.Fatal("password rotation altered the fixture refresh-session record")
	}
}

func TestNoopAndAmbiguousOutcomesNeverClaimSuccessOrRetry(t *testing.T) {
	t.Run("same password is a no-op", func(t *testing.T) {
		store := newMemoryRotationStore(t)
		receipt, err := rotate(context.Background(), target{userID: devDemoUserID}, store, true, []byte(fixtureOldPassword))
		if !errors.Is(err, errPasswordNoop) || receipt.Outcome != "rejected" || store.updateCalls != 0 {
			t.Fatal("same-password request was not rejected without a write")
		}
	})

	t.Run("transaction error after possible commit", func(t *testing.T) {
		store := newMemoryRotationStore(t)
		store.updateError = errors.New("synthetic password and hash details must stay private")
		store.commitOnError = true
		var output bytes.Buffer
		args := append(devTargetArgs(), "--apply", "--password-fd", "3")
		err := run(args, func(int) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(fixtureNewPassword)), nil
		}, func(context.Context, target) (rotationStore, func(), error) {
			return store, nil, nil
		}, &output)
		var receipt rotationReceipt
		if json.Unmarshal(output.Bytes(), &receipt) != nil || !errors.Is(err, errRotationUnknown) || receipt.Outcome != "unknown" || store.updateCalls != 1 || store.reads != 2 {
			t.Fatal("ambiguous transaction was reported as success or retried")
		}
		if strings.Contains(err.Error(), "synthetic") || strings.Contains(output.String(), "synthetic") {
			t.Fatal("store error disclosed synthetic password/hash details")
		}
	})

	t.Run("readback failure", func(t *testing.T) {
		t.Setenv("FIRESTORE_EMULATOR_HOST", "")
		store := newMemoryRotationStore(t)
		store.readErrorAt = 2
		store.readError = errors.New("synthetic readback detail")
		var output bytes.Buffer
		args := append(devTargetArgs(), "--apply", "--password-fd", "3")
		err := run(args, func(int) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(fixtureNewPassword)), nil
		}, func(context.Context, target) (rotationStore, func(), error) {
			return store, nil, nil
		}, &output)
		var receipt rotationReceipt
		if json.Unmarshal(output.Bytes(), &receipt) != nil || !errors.Is(err, errRotationUnknown) || receipt.Outcome != "unknown" || store.updateCalls != 1 || store.reads != 2 {
			t.Fatal("failed readback was reported as success or caused another transaction")
		}
		if strings.Contains(err.Error(), "synthetic") || strings.Contains(output.String(), "synthetic") || receipt.Outcome == "replaced" {
			t.Fatal("failed readback disclosed an error or claimed a completed replacement")
		}
	})
}
