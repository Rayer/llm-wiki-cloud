package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEmulatorPasswordRotationChangesOnlyPasswordAndKeepsSession(t *testing.T) {
	host := strings.TrimSpace(os.Getenv("LWC366_FIRESTORE_EMULATOR_HOST"))
	if host == "" {
		t.Skip("LWC366_FIRESTORE_EMULATOR_HOST is not set")
	}
	if err := validateLoopbackHost(host); err != nil {
		t.Fatal("emulator test requires an explicit numeric loopback endpoint")
	}
	t.Setenv("FIRESTORE_EMULATOR_HOST", "")
	targetValue := target{project: emulatorProjectID, database: emulatorDatabase, userID: emulatorUserID, emulatorHost: host}
	if err := validateTarget(targetValue); err != nil {
		t.Fatal("synthetic emulator target failed preflight")
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	rotationStore, closeClient, err := newFirestoreStore(ctx, targetValue)
	if err != nil {
		t.Fatal("could not connect to the explicitly configured local emulator")
	}
	defer closeClient()
	store := rotationStore.(*firestoreRotationStore)
	client := store.client
	repo := auth.NewIdentityRepository(client)
	if err := repo.ReserveCanonicalEmail(ctx, emulatorUserID, emulatorFixtureEmail, emulatorFixtureEmail); err != nil {
		t.Fatalf("could not prepare synthetic email reservation: %v", err)
	}
	oldHash, err := bcrypt.GenerateFromPassword([]byte(fixtureOldPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal("could not prepare synthetic password hash")
	}
	beforeTime := time.Date(2025, time.March, 4, 5, 6, 7, 0, time.UTC)
	userRef := client.Collection("users").Doc(emulatorUserID)
	projectRef := userRef.Collection("projects").Doc("rotation-fixture")
	environment := fmt.Sprintf("lwc366-rotation-fixture-%d", time.Now().UnixNano())
	defer func() {
		cleanupRotationFixture(ctx, client, emulatorUserID, emulatorFixtureEmail, environment, userRef, projectRef)
	}()
	if _, err := userRef.Set(ctx, map[string]interface{}{
		"email": emulatorFixtureEmail, "email_canonical": emulatorFixtureEmail, "password_hash": string(oldHash),
		"role": "demo", "status": auth.AccountActive, "auth_version": int64(7), "auth_invalid_before": beforeTime,
		"email_verified": true, "project_count": 1, "default_project": "rotation-fixture",
		"extension": map[string]interface{}{"preserved": true},
	}); err != nil {
		t.Fatal("could not prepare synthetic existing user")
	}
	if _, err := projectRef.Set(ctx, map[string]interface{}{"name": "Keep this project", "owner_id": emulatorUserID}); err != nil {
		t.Fatal("could not prepare synthetic owned project")
	}
	beforeSnapshot, err := userRef.Get(ctx)
	if err != nil {
		t.Fatal("could not read synthetic user baseline")
	}
	projectBefore, err := projectRef.Get(ctx)
	if err != nil {
		t.Fatal("could not read synthetic project baseline")
	}

	sessions := auth.NewRefreshSessionAuthority(client, environment)
	if _, err := sessions.Issue(ctx, emulatorUserID, "demo", fixtureJWTSecret, 7); err != nil {
		t.Fatal("could not prepare an existing synthetic durable session")
	}
	sessionBefore, err := emulatorSessionSnapshot(ctx, client, environment, emulatorUserID)
	if err != nil {
		t.Fatal("could not read synthetic durable session baseline")
	}

	gin.SetMode(gin.TestMode)
	args := []string{"--project", emulatorProjectID, "--database", emulatorDatabase, "--user-id", emulatorUserID,
		"--emulator-host", host, "--apply", "--password-fd", "3"}
	var output bytes.Buffer
	err = run(args, func(int) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(fixtureNewPassword)), nil
	}, newFirestoreStore, &output)
	if err != nil {
		t.Fatal("emulator password-only transaction did not complete")
	}
	var receipt rotationReceipt
	if json.Unmarshal(output.Bytes(), &receipt) != nil || receipt.Outcome != "replaced" || receipt.UserID != emulatorUserID {
		t.Fatal("emulator operation did not produce the expected bounded receipt")
	}
	if strings.Contains(output.String(), fixtureOldPassword) || strings.Contains(output.String(), fixtureNewPassword) {
		t.Fatal("emulator receipt exposed a synthetic password")
	}

	afterSnapshot, err := userRef.Get(ctx)
	if err != nil {
		t.Fatal("could not read back synthetic user after replacement")
	}
	if !reflect.DeepEqual(withoutPasswordHash(beforeSnapshot.Data()), withoutPasswordHash(afterSnapshot.Data())) {
		t.Fatal("emulator operation changed a user field other than password_hash")
	}
	projectAfter, err := projectRef.Get(ctx)
	if err != nil || !reflect.DeepEqual(projectBefore.Data(), projectAfter.Data()) {
		t.Fatal("emulator operation changed the existing owned project")
	}
	reservation, err := repo.GetCanonicalEmailReservation(ctx, emulatorFixtureEmail)
	if err != nil || reservation == nil || reservation.UserID != emulatorUserID || reservation.CanonicalEmail != emulatorFixtureEmail {
		t.Fatal("emulator operation changed the UID/email reservation")
	}
	sessionAfter, err := readEmulatorSessionSnapshot(ctx, sessionBefore)
	if err != nil || !sameEmulatorSessionSnapshot(sessionBefore, sessionAfter) {
		t.Fatal("emulator operation revoked or changed the preexisting durable session")
	}

	for name, policy := range map[string]auth.RefreshCookiePolicy{
		"Auth": auth.HostRefreshCookiePolicy(), "BFF": auth.LegacyRefreshCookiePolicy(),
	} {
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.POST("/login", auth.LoginHandlerWithRepositoryAndSessionAuthority(repo, fixtureJWTSecret, policy, sessions))
			login := func(password string) *httptest.ResponseRecorder {
				body, _ := json.Marshal(auth.LoginRequest{Email: emulatorFixtureEmail, Password: password})
				request := httptest.NewRequest(http.MethodPost, "https://fixture.example.test/login", bytes.NewReader(body))
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			if oldResponse := login(fixtureOldPassword); oldResponse.Code != http.StatusUnauthorized || len(oldResponse.Result().Cookies()) != 0 {
				t.Fatal("emulator login accepted the old synthetic password")
			}
			newResponse := login(fixtureNewPassword)
			if newResponse.Code != http.StatusOK || len(newResponse.Result().Cookies()) != 1 {
				t.Fatal("emulator login did not accept the replacement through the existing durable-session contract")
			}
			var result auth.LoginResponse
			if json.Unmarshal(newResponse.Body.Bytes(), &result) != nil || result.User.ID != emulatorUserID || result.User.Email != emulatorFixtureEmail {
				t.Fatal("emulator login changed the existing UID or email")
			}
		})
	}
	sessionFinal, err := readEmulatorSessionSnapshot(ctx, sessionBefore)
	if err != nil || !sameEmulatorSessionSnapshot(sessionBefore, sessionFinal) {
		t.Fatal("login fixture or rotation altered the preexisting durable session")
	}
}

const emulatorFixtureEmail = "demo.rotation@lwc366.example.test"

type emulatorSessionRecord struct {
	ref        *firestore.DocumentRef
	path       string
	data       map[string]interface{}
	updateTime time.Time
}

func emulatorSessionSnapshot(ctx context.Context, client *firestore.Client, environment, userID string) (*emulatorSessionRecord, error) {
	snapshots, err := client.Collection("auth_refresh_sessions").Where("user_id", "==", userID).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("session fixture unavailable")
	}
	var match *emulatorSessionRecord
	for _, snapshot := range snapshots {
		if snapshot.Data()["environment"] == environment {
			if match != nil {
				return nil, fmt.Errorf("session fixture unavailable")
			}
			match = &emulatorSessionRecord{
				ref: snapshot.Ref, path: snapshot.Ref.Path, data: snapshot.Data(), updateTime: snapshot.UpdateTime,
			}
		}
	}
	if match == nil {
		return nil, fmt.Errorf("session fixture unavailable")
	}
	return match, nil
}

func readEmulatorSessionSnapshot(ctx context.Context, before *emulatorSessionRecord) (*emulatorSessionRecord, error) {
	if before == nil || before.ref == nil || before.path == "" {
		return nil, fmt.Errorf("session fixture unavailable")
	}
	snapshot, err := before.ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session fixture unavailable")
	}
	return &emulatorSessionRecord{
		ref: snapshot.Ref, path: snapshot.Ref.Path, data: snapshot.Data(), updateTime: snapshot.UpdateTime,
	}, nil
}

func sameEmulatorSessionSnapshot(before, after *emulatorSessionRecord) bool {
	return before != nil && after != nil && before.path != "" && !before.updateTime.IsZero() && before.path == after.path &&
		before.updateTime.Equal(after.updateTime) && reflect.DeepEqual(before.data, after.data)
}

func TestEmulatorBaselineSessionComparisonRejectsMutationDeletionOrReplacement(t *testing.T) {
	baselineTime := time.Date(2025, time.March, 4, 5, 6, 7, 0, time.UTC)
	baseline := &emulatorSessionRecord{
		path:       "auth_refresh_sessions/baseline-session",
		data:       map[string]interface{}{"session_id": "baseline-session", "status": "active", "auth_version": int64(7)},
		updateTime: baselineTime,
	}
	unchanged := &emulatorSessionRecord{path: baseline.path, data: cloneMap(baseline.data), updateTime: baselineTime}
	if !sameEmulatorSessionSnapshot(baseline, unchanged) {
		t.Fatal("unchanged baseline session was rejected")
	}
	mutated := &emulatorSessionRecord{path: baseline.path, data: cloneMap(baseline.data), updateTime: baselineTime}
	mutated.data["status"] = "revoked"
	if sameEmulatorSessionSnapshot(baseline, mutated) {
		t.Fatal("mutation of the baseline session was accepted")
	}
	if sameEmulatorSessionSnapshot(baseline, nil) {
		t.Fatal("deletion of the baseline session was accepted")
	}
	replacement := &emulatorSessionRecord{path: "auth_refresh_sessions/new-login-session", data: cloneMap(baseline.data)}
	if sameEmulatorSessionSnapshot(baseline, replacement) {
		t.Fatal("a different session document was accepted as the baseline session")
	}
	recreated := &emulatorSessionRecord{
		path: baseline.path, data: cloneMap(baseline.data), updateTime: baselineTime.Add(time.Second),
	}
	if sameEmulatorSessionSnapshot(baseline, recreated) {
		t.Fatal("a recreated baseline document was accepted as the original session")
	}
}

func cleanupRotationFixture(ctx context.Context, client *firestore.Client, userID, email, environment string, userRef, projectRef *firestore.DocumentRef) {
	if sessions, err := client.Collection("auth_refresh_sessions").Where("user_id", "==", userID).Documents(ctx).GetAll(); err == nil {
		for _, snapshot := range sessions {
			if snapshot.Data()["environment"] == environment {
				_, _ = snapshot.Ref.Delete(ctx)
			}
		}
	}
	if reservations, err := client.Collection(auth.EmailReservationsCollection).Where("canonical_email", "==", email).Documents(ctx).GetAll(); err == nil {
		for _, snapshot := range reservations {
			if snapshot.Data()["user_id"] == userID {
				_, _ = snapshot.Ref.Delete(ctx)
			}
		}
	}
	_, _ = projectRef.Delete(ctx)
	_, _ = userRef.Delete(ctx)
}

func TestEmulatorTargetCannotUseProductionOrDefaultDatabase(t *testing.T) {
	host := strings.TrimSpace(os.Getenv("LWC366_FIRESTORE_EMULATOR_HOST"))
	if host == "" {
		t.Skip("LWC366_FIRESTORE_EMULATOR_HOST is not set")
	}
	t.Setenv("FIRESTORE_EMULATOR_HOST", "")
	for _, candidate := range []target{
		{project: devProjectID, database: devDatabaseID, userID: devDemoUserID, emulatorHost: host},
		{project: emulatorProjectID, database: "(default)", userID: emulatorUserID, emulatorHost: host},
		{project: emulatorProjectID, database: emulatorDatabase, userID: devDemoUserID, emulatorHost: host},
	} {
		if !errors.Is(validateTarget(candidate), errTargetRejected) {
			t.Fatal("emulator preflight accepted a live, default-database, or unapproved identity tuple")
		}
	}
}
