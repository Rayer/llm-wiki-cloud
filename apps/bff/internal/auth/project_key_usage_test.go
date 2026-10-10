package auth

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type memoryProjectKeyUsageStore struct {
	mu            sync.Mutex
	records       map[string]projectKeyUsageRecord
	recordErr     error
	listErr       error
	recordCalls   int
	lastListedIDs []string
	bounded       bool
}

func newMemoryProjectKeyUsageStore() *memoryProjectKeyUsageStore {
	return &memoryProjectKeyUsageStore{records: make(map[string]projectKeyUsageRecord)}
}

func (s *memoryProjectKeyUsageStore) Record(ctx context.Context, record projectKeyUsageRecord) error {
	deadline, ok := ctx.Deadline()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCalls++
	s.bounded = ok && time.Until(deadline) <= projectKeyUsageWriteTimeout
	if s.recordErr != nil {
		return s.recordErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stored, exists := s.records[record.KeyID]
	if exists && (stored.UserID != record.UserID || stored.ProjectID != record.ProjectID) {
		return errProjectKeyUsageUnavailable
	}
	if !exists || stored.LastUsedAt.Before(record.LastUsedAt) {
		s.records[record.KeyID] = record
	}
	return nil
}

func (s *memoryProjectKeyUsageStore) List(_ context.Context, userID, projectID string, keyIDs []string) (map[string]projectKeyUsageStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastListedIDs = append([]string(nil), keyIDs...)
	if s.listErr != nil {
		return nil, s.listErr
	}
	statuses := make(map[string]projectKeyUsageStatus, len(keyIDs))
	for _, keyID := range keyIDs {
		record, exists := s.records[keyID]
		switch {
		case !exists:
			statuses[keyID] = projectKeyUsageStatus{Status: projectKeyUsageNoRecord}
		case record.UserID != userID || record.ProjectID != projectID:
			statuses[keyID] = projectKeyUsageStatus{Status: projectKeyUsageUnavailable}
		default:
			lastUsedAt := record.LastUsedAt
			statuses[keyID] = projectKeyUsageStatus{LastUsedAt: &lastUsedAt, Status: projectKeyUsageAvailable}
		}
	}
	return statuses, nil
}

type blockingProjectKeyUsageStore struct{ called chan struct{} }

func (s blockingProjectKeyUsageStore) Record(ctx context.Context, _ projectKeyUsageRecord) error {
	s.called <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

func (blockingProjectKeyUsageStore) List(context.Context, string, string, []string) (map[string]projectKeyUsageStatus, error) {
	return nil, errProjectKeyUsageUnavailable
}

func TestProjectKeyAuthenticateUsageIsSuccessOnlyAndBestEffort(t *testing.T) {
	authority := newMemoryProjectKeyStore()
	usage := newMemoryProjectKeyUsageStore()
	service := testProjectKeyService(authority)
	service.usage = usage
	metadata, secret, _, err := service.Create(context.Background(), "owner", "project-a", "auth boundary")
	if err != nil {
		t.Fatal(err)
	}
	if usage.recordCalls != 0 {
		t.Fatal("creating a key must not count as authorized use")
	}

	wrongSecret := secret[:len(secret)-1] + "A"
	if wrongSecret == secret {
		wrongSecret = secret[:len(secret)-1] + "B"
	}
	if _, err := service.Authenticate(context.Background(), wrongSecret, ""); !errors.Is(err, ErrProjectKeyInvalid) {
		t.Fatalf("wrong-secret error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), secret, "project-b"); !errors.Is(err, ErrProjectKeyAccessDenied) {
		t.Fatalf("mismatched-project error = %v", err)
	}
	service.lookup = func(context.Context, string) (*UserRecord, error) {
		return &UserRecord{Status: AccountSuspended}, nil
	}
	if _, err := service.Authenticate(context.Background(), secret, ""); !errors.Is(err, ErrProjectKeyAccessDenied) {
		t.Fatalf("suspended-account error = %v", err)
	}
	service.lookup = func(context.Context, string) (*UserRecord, error) {
		return &UserRecord{Status: AccountActive}, nil
	}
	service.authorize = func(context.Context, string, string) error { return ErrProjectPermissionDenied }
	if _, err := service.Authenticate(context.Background(), secret, ""); !errors.Is(err, ErrProjectKeyAccessDenied) {
		t.Fatalf("lost-owner error = %v", err)
	}
	if usage.recordCalls != 0 {
		t.Fatalf("rejected authority attempted %d usage writes", usage.recordCalls)
	}

	service.authorize = func(context.Context, string, string) error { return nil }
	usage.recordErr = errors.New("synthetic usage write failure")
	if _, err := service.Authenticate(context.Background(), secret, ""); err != nil {
		t.Fatalf("usage failure changed authorization result: %v", err)
	}
	if usage.recordCalls != 1 || !usage.bounded {
		t.Fatalf("usage calls=%d boundedContext=%v", usage.recordCalls, usage.bounded)
	}
	usage.recordErr = nil
	if _, err := service.Authenticate(context.Background(), secret, ""); err != nil {
		t.Fatalf("valid authority failed: %v", err)
	}
	stored := usage.records[metadata.KeyID]
	if stored.SchemaVersion != projectKeyUsageSchema || stored.KeyID != metadata.KeyID || stored.UserID != "owner" ||
		stored.ProjectID != "project-a" || stored.LastUsedAt.IsZero() || stored.LastUsedAt.Location() != time.UTC {
		t.Fatalf("unexpected usage record: %#v", stored)
	}

	future := stored.LastUsedAt.Add(time.Hour)
	if err := usage.Record(context.Background(), projectKeyUsageRecord{
		SchemaVersion: projectKeyUsageSchema, KeyID: metadata.KeyID, UserID: "owner", ProjectID: "project-a", LastUsedAt: future,
	}); err != nil {
		t.Fatal(err)
	}
	if err := usage.Record(context.Background(), projectKeyUsageRecord{
		SchemaVersion: projectKeyUsageSchema, KeyID: metadata.KeyID, UserID: "owner", ProjectID: "project-a", LastUsedAt: stored.LastUsedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if got := usage.records[metadata.KeyID].LastUsedAt; !got.Equal(future) {
		t.Fatalf("older timestamp replaced newer usage: got %s want %s", got, future)
	}
	if err := usage.Record(context.Background(), projectKeyUsageRecord{
		SchemaVersion: projectKeyUsageSchema, KeyID: metadata.KeyID, UserID: "other", ProjectID: "project-b", LastUsedAt: future.Add(time.Hour),
	}); err == nil {
		t.Fatal("usage identity mismatch was accepted")
	}
	if got := usage.records[metadata.KeyID]; got.UserID != "owner" || got.ProjectID != "project-a" || !got.LastUsedAt.Equal(future) {
		t.Fatalf("identity mismatch overwrote usage: %#v", got)
	}
}

func TestProjectKeyUsageWriteIsBoundedWithoutChangingAuthorityResult(t *testing.T) {
	service := testProjectKeyService(newMemoryProjectKeyStore())
	_, secret, _, err := service.Create(context.Background(), "owner", "project-a", "bounded")
	if err != nil {
		t.Fatal(err)
	}
	blocking := blockingProjectKeyUsageStore{called: make(chan struct{}, 1)}
	service.usage = blocking
	started := time.Now()
	if _, err := service.Authenticate(context.Background(), secret, ""); err != nil {
		t.Fatalf("usage deadline changed authorization result: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("usage write exceeded its 500ms deadline: %s", elapsed)
	}
	select {
	case <-blocking.called:
	default:
		t.Fatal("usage writer was not called after successful authority")
	}
}

func TestProjectKeyUsageListFailureIsOptionalAndScoped(t *testing.T) {
	authority := newMemoryProjectKeyStore()
	usage := newMemoryProjectKeyUsageStore()
	service := testProjectKeyService(authority)
	service.usage = usage
	createdAt := time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC)
	authority.records = map[string]ProjectKeyRecord{
		"00000000000000000000000000000001": {KeyID: "00000000000000000000000000000001", UserID: "owner", ProjectID: "project-a", Name: "A", State: "active", CreatedAt: createdAt},
		"00000000000000000000000000000002": {KeyID: "00000000000000000000000000000002", UserID: "owner", ProjectID: "project-b", Name: "B", State: "active", CreatedAt: createdAt},
	}
	keys, err := service.List(context.Background(), "owner", "project-a")
	if err != nil || len(keys) != 1 || keys[0].LastUsedStatus != projectKeyUsageNoRecord {
		t.Fatalf("missing usage status = %#v, err=%v", keys, err)
	}
	if !reflect.DeepEqual(usage.lastListedIDs, []string{"00000000000000000000000000000001"}) {
		t.Fatalf("usage lookup escaped selected Project: %v", usage.lastListedIDs)
	}
	usage.listErr = errors.New("synthetic usage read failure")
	keys, err = service.List(context.Background(), "owner", "project-a")
	if err != nil || len(keys) != 1 || keys[0].LastUsedStatus != projectKeyUsageUnavailable || keys[0].LastUsedAt != nil {
		t.Fatalf("usage read failure changed key listing: %#v, err=%v", keys, err)
	}
	if _, _, _, err := service.Create(context.Background(), "owner", "project-a", "still writable"); err != nil {
		t.Fatalf("usage listing outage blocked creation: %v", err)
	}
	if _, err := service.Revoke(context.Background(), "owner", "project-a", "00000000000000000000000000000001"); err != nil {
		t.Fatalf("usage listing outage blocked revocation: %v", err)
	}
}

func TestProjectKeyUsageRecordRejectsUnknownFieldsAndBadIdentity(t *testing.T) {
	keyID := "00112233445566778899aabbccddeeff"
	data := map[string]interface{}{
		"schema_version": 1, "key_id": keyID, "user_id": "owner", "project_id": "project-a",
		"last_used_at": time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC),
	}
	if _, err := validateProjectKeyUsageRecord(keyID, data); err != nil {
		t.Fatalf("valid usage record rejected: %v", err)
	}
	data["secret"] = "must not be accepted"
	if _, err := validateProjectKeyUsageRecord(keyID, data); !errors.Is(err, errProjectKeyUsageUnavailable) {
		t.Fatalf("usage record with unknown field error = %v", err)
	}
	delete(data, "secret")
	if _, err := validateProjectKeyUsageRecord("ffeeddccbbaa99887766554433221100", data); !errors.Is(err, errProjectKeyUsageUnavailable) {
		t.Fatalf("usage record with different doc ID error = %v", err)
	}
}

func TestProjectKeyUsageFirestoreIsScopedStrictAndMonotonic(t *testing.T) {
	fs := accountEmulator(t)
	const scope = "lwc381_usage_test"
	if err := scopedfirestore.RegisterLocalScope(fs, scope); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scopedfirestore.RegisterLocalScope(fs, "") })
	ctx := context.Background()
	userID, projectID := "lwc381-owner", "lwc381-project"
	if _, err := scopedfirestore.Collection(fs, "users").Doc(userID).Set(ctx, map[string]interface{}{
		"email": "owner@example.test", "role": "member", "status": AccountActive,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := scopedfirestore.Collection(fs, "projects").Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "name": "Scoped project",
	}); err != nil {
		t.Fatal(err)
	}
	service := NewProjectKeyService(fs)
	metadata, secret, _, err := service.Create(ctx, userID, projectID, "scoped usage")
	if err != nil {
		t.Fatal(err)
	}
	authorityRef := scopedfirestore.Collection(fs, "project_keys").Doc(metadata.KeyID)
	authorityBefore, err := authorityRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, secret, ""); err != nil {
		t.Fatalf("valid authority: %v", err)
	}
	authorityAfter, err := authorityRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(authorityBefore.Data(), authorityAfter.Data()) {
		t.Fatalf("usage changed authority record: before=%#v after=%#v", authorityBefore.Data(), authorityAfter.Data())
	}
	if _, err := validateProjectKeyRecord(authorityAfter.Ref.ID, authorityAfter.Data()); err != nil {
		t.Fatalf("strict old reader rejected unchanged authority: %v", err)
	}

	usage := firestoreProjectKeyUsageStore{fs: fs}
	usageRef := scopedfirestore.Collection(fs, projectKeyUsageCollection).Doc(metadata.KeyID)
	usageDoc, err := usageRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(usageDoc.Data()) != 5 || usageDoc.Data()["secret"] != nil || usageDoc.Data()["secret_hash"] != nil {
		t.Fatalf("usage record contains unexpected fields: %#v", usageDoc.Data())
	}
	if _, err := fs.Collection(projectKeyUsageCollection).Doc(metadata.KeyID).Get(ctx); status.Code(err) != codes.NotFound {
		t.Fatalf("usage record escaped local scope: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	latest := base.Add(9 * time.Minute)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 10)
	for index := 0; index < 10; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			errorsCh <- usage.Record(ctx, projectKeyUsageRecord{
				SchemaVersion: projectKeyUsageSchema, KeyID: metadata.KeyID, UserID: userID, ProjectID: projectID,
				LastUsedAt: base.Add(time.Duration(index) * time.Minute),
			})
		}(index)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent usage update: %v", err)
		}
	}
	if err := usage.Record(ctx, projectKeyUsageRecord{
		SchemaVersion: projectKeyUsageSchema, KeyID: metadata.KeyID, UserID: userID, ProjectID: projectID, LastUsedAt: latest,
	}); err != nil {
		t.Fatal(err)
	}
	if err := usage.Record(ctx, projectKeyUsageRecord{
		SchemaVersion: projectKeyUsageSchema, KeyID: metadata.KeyID, UserID: userID, ProjectID: projectID, LastUsedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	usageDoc, err = usageRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := usageDoc.Data()["last_used_at"].(time.Time); !ok || !got.Equal(latest) {
		t.Fatalf("usage timestamp regressed: got=%v want=%s", usageDoc.Data()["last_used_at"], latest)
	}
	if err := usage.Record(ctx, projectKeyUsageRecord{
		SchemaVersion: projectKeyUsageSchema, KeyID: metadata.KeyID, UserID: "other-owner", ProjectID: "other-project",
		LastUsedAt: latest.Add(time.Hour),
	}); err == nil {
		t.Fatal("usage write with a different authority identity was accepted")
	}
	usageDoc, err = usageRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := usageDoc.Data()["user_id"]; got != userID {
		t.Fatalf("identity mismatch changed usage owner: %v", got)
	}
	if got := usageDoc.Data()["project_id"]; got != projectID {
		t.Fatalf("identity mismatch changed usage Project: %v", got)
	}

	listed, err := service.List(ctx, userID, projectID)
	if err != nil || len(listed) != 1 || listed[0].LastUsedStatus != projectKeyUsageAvailable ||
		listed[0].LastUsedAt == nil || !listed[0].LastUsedAt.Equal(latest) {
		t.Fatalf("usage metadata = %#v, err=%v", listed, err)
	}
	otherProject, err := usage.List(ctx, userID, "other-project", []string{metadata.KeyID})
	if err != nil || otherProject[metadata.KeyID].Status != projectKeyUsageUnavailable {
		t.Fatalf("mismatched usage identity was not unavailable: %#v, err=%v", otherProject, err)
	}
	encoded, err := json.Marshal(listed)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("usage metadata did not encode safely: %v", err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("usage metadata contains secret material")
	}
}

var _ projectKeyUsageStore = (*memoryProjectKeyUsageStore)(nil)
var _ projectKeyUsageStore = firestoreProjectKeyUsageStore{}
