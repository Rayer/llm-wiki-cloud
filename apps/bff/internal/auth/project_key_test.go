package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
)

const syntheticProjectKey = "lwc_pk_00112233445566778899aabbccddeeff.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

type memoryProjectKeyStore struct {
	records             map[string]ProjectKeyRecord
	lastCreated         ProjectKeyRecord
	createErr           error
	commitCreateOnError bool
	revokeErr           error
	commitRevokeOnError bool
	readErr             error
	createCalls         int
	readCalls           int
	revokeCalls         int
}

func newMemoryProjectKeyStore() *memoryProjectKeyStore {
	return &memoryProjectKeyStore{records: make(map[string]ProjectKeyRecord)}
}

func (s *memoryProjectKeyStore) Create(_ context.Context, record ProjectKeyRecord) (time.Time, error) {
	s.createCalls++
	if _, exists := s.records[record.KeyID]; exists {
		return time.Time{}, errors.New("key collision")
	}
	record.CreatedAt = time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	s.lastCreated = record
	if s.createErr != nil {
		if s.commitCreateOnError {
			s.records[record.KeyID] = record
		}
		return time.Time{}, s.createErr
	}
	s.records[record.KeyID] = record
	return record.CreatedAt, nil
}

func (s *memoryProjectKeyStore) Read(_ context.Context, id string) (ProjectKeyRecord, error) {
	s.readCalls++
	if s.readErr != nil {
		return ProjectKeyRecord{}, s.readErr
	}
	record, ok := s.records[id]
	if !ok {
		return ProjectKeyRecord{}, ErrProjectKeyNotFound
	}
	return record, nil
}

func (s *memoryProjectKeyStore) List(_ context.Context, userID string) ([]ProjectKeyRecord, error) {
	records := make([]ProjectKeyRecord, 0)
	for _, record := range s.records {
		if record.UserID == userID {
			records = append(records, record)
		}
	}
	return records, nil
}

func (s *memoryProjectKeyStore) Revoke(_ context.Context, userID, projectID, id string) error {
	s.revokeCalls++
	record, ok := s.records[id]
	if !ok || record.UserID != userID || record.ProjectID != projectID {
		return ErrProjectKeyNotFound
	}
	if record.State == "active" {
		record.State = "revoked"
		record.RevokedAt = time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
		s.records[id] = record
	}
	if s.revokeErr != nil {
		if s.commitRevokeOnError {
			return s.revokeErr
		}
		record.State = "active"
		record.RevokedAt = time.Time{}
		s.records[id] = record
		return s.revokeErr
	}
	return nil
}

func testProjectKeyService(store *memoryProjectKeyStore) *ProjectKeyService {
	lookup := func(context.Context, string) (*UserRecord, error) {
		return &UserRecord{Status: AccountActive}, nil
	}
	authorize := func(_ context.Context, userID, projectID string) error {
		if userID == "owner" && projectID == "project-a" {
			return nil
		}
		return ErrProjectPermissionDenied
	}
	return newProjectKeyService(store, lookup, authorize)
}

func TestProjectKeyFormatAndCanonicalSecret(t *testing.T) {
	id, digest, err := parseProjectKey(syntheticProjectKey)
	if err != nil || id != "00112233445566778899aabbccddeeff" {
		t.Fatalf("parse fixture id=%q err=%v", id, err)
	}
	fixtureBytes := make([]byte, projectKeySecretBytes)
	for i := range fixtureBytes {
		fixtureBytes[i] = byte(i)
	}
	wantDigest := sha256.Sum256(fixtureBytes)
	if hex.EncodeToString(wantDigest[:]) != "630dcd2966c4336691125448bbb25b4ff412a49c732db2c8abc1b8581bd710dd" || digest != wantDigest {
		t.Fatalf("fixture hash = %x, want %x", digest, wantDigest)
	}

	generatedID, generated, storedHash, err := generateProjectKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != 83 || !strings.HasPrefix(generated, "lwc_pk_") || !validProjectKeyID(generatedID) {
		t.Fatalf("generated token/id shape: token length=%d id=%q", len(generated), generatedID)
	}
	parsedID, parsedDigest, err := parseProjectKey(generated)
	if err != nil || parsedID != generatedID || hex.EncodeToString(parsedDigest[:]) != storedHash {
		t.Fatalf("generated token parse id=%q hash=%x err=%v", parsedID, parsedDigest, err)
	}

	badCanonical := strings.TrimSuffix(syntheticProjectKey, "8") + "9"
	badSecret := strings.Replace(syntheticProjectKey, ".AAE", ".BAE", 1)
	for name, token := range map[string]string{
		"prefix":             "LWC" + syntheticProjectKey[3:],
		"uppercase id":       strings.Replace(syntheticProjectKey, "001122", "0011AA", 1),
		"short token":        syntheticProjectKey[:len(syntheticProjectKey)-1],
		"invalid alphabet":   strings.Replace(syntheticProjectKey, "AAE", "AA*", 1),
		"noncanonical bits":  badCanonical,
		"extra separator":    syntheticProjectKey + ".x",
		"leading whitespace": " " + syntheticProjectKey,
	} {
		if _, _, err := parseProjectKey(token); err == nil {
			t.Errorf("%s token parsed successfully", name)
		}
	}
	if _, _, err := parseProjectKey(badSecret); err != nil {
		t.Fatalf("well-formed wrong secret was rejected as malformed: %v", err)
	}
	if !validProjectKeyID("00112233445566778899aabbccddeeff") || validProjectKeyID("00112233445566778899AABBCCDDEEFF") {
		t.Fatal("key ID validation did not enforce lowercase hex")
	}
}

func TestProjectKeyCreateAuthenticateListAndRevoke(t *testing.T) {
	ctx := context.Background()
	store := newMemoryProjectKeyStore()
	service := testProjectKeyService(store)
	metadata, secret, outcomeID, err := service.Create(ctx, "owner", "project-a", " Client α ")
	if err != nil || secret == "" || outcomeID != "" || metadata.Name != "Client α" || metadata.State != "active" || len(metadata.Capabilities) != 1 || metadata.Capabilities[0] != "query" {
		t.Fatalf("create metadata=%#v secretPresent=%v outcomeID=%q err=%v", metadata, secret != "", outcomeID, err)
	}
	if len(secret) != 83 || store.records[metadata.KeyID].SecretHash == secret {
		t.Fatalf("unexpected stored credential or token length: length=%d", len(secret))
	}
	if _, err := service.Authenticate(ctx, secret, ""); err != nil {
		t.Fatalf("authenticate without project header: %v", err)
	}
	if _, err := service.Authenticate(ctx, secret, "project-b"); !errors.Is(err, ErrProjectKeyAccessDenied) {
		t.Fatalf("mismatched project error = %v", err)
	}
	parts := strings.SplitN(secret, ".", 2)
	wrongFirstChar := byte('A')
	if parts[1][0] == wrongFirstChar {
		wrongFirstChar = 'B'
	}
	wrongSecret := parts[0] + "." + string(wrongFirstChar) + parts[1][1:]
	if _, err := service.Authenticate(ctx, wrongSecret, ""); !errors.Is(err, ErrProjectKeyInvalid) {
		t.Fatalf("wrong secret error = %v", err)
	}
	listed, err := service.List(ctx, "owner", "project-a")
	if err != nil || len(listed) != 1 || listed[0].KeyID != metadata.KeyID {
		t.Fatalf("list=%#v err=%v", listed, err)
	}
	encoded, _ := json.Marshal(listed)
	if strings.Contains(string(encoded), "secret_hash") || strings.Contains(string(encoded), secret) {
		t.Fatalf("list metadata leaked a credential: %s", encoded)
	}
	if _, err := service.List(ctx, "other", "project-a"); !errors.Is(err, ErrProjectKeyAccessDenied) {
		t.Fatalf("foreign list error = %v", err)
	}
	revoked, err := service.Revoke(ctx, "owner", "project-a", metadata.KeyID)
	if err != nil || revoked.State != "revoked" || revoked.RevokedAt == nil {
		t.Fatalf("revoke metadata=%#v err=%v", revoked, err)
	}
	if _, err := service.Revoke(ctx, "owner", "project-a", metadata.KeyID); err != nil {
		t.Fatalf("repeat revoke: %v", err)
	}
	if _, err := service.Authenticate(ctx, secret, ""); !errors.Is(err, ErrProjectKeyInvalid) {
		t.Fatalf("revoked key error = %v", err)
	}
	encoded, _ = json.Marshal(revoked)
	if strings.Contains(string(encoded), "secret_hash") || strings.Contains(string(encoded), secret) {
		t.Fatalf("revoke response leaked a credential: %s", encoded)
	}
}

func TestProjectKeyListSortsAndFiltersByProject(t *testing.T) {
	createdAt := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	store := newMemoryProjectKeyStore()
	store.records = map[string]ProjectKeyRecord{
		"00000000000000000000000000000003": {KeyID: "00000000000000000000000000000003", UserID: "owner", ProjectID: "project-a", Name: "later tie", State: "active", CreatedAt: createdAt},
		"00000000000000000000000000000002": {KeyID: "00000000000000000000000000000002", UserID: "owner", ProjectID: "project-a", Name: "same time", State: "active", CreatedAt: createdAt},
		"00000000000000000000000000000001": {KeyID: "00000000000000000000000000000001", UserID: "owner", ProjectID: "project-a", Name: "newest", State: "active", CreatedAt: createdAt.Add(time.Minute)},
		"00000000000000000000000000000004": {KeyID: "00000000000000000000000000000004", UserID: "owner", ProjectID: "project-b", Name: "other project", State: "active", CreatedAt: createdAt.Add(time.Hour)},
	}
	keys, err := testProjectKeyService(store).List(context.Background(), "owner", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"00000000000000000000000000000001",
		"00000000000000000000000000000002",
		"00000000000000000000000000000003",
	}
	if len(keys) != len(want) {
		t.Fatalf("listed %d keys, want %d project-scoped keys", len(keys), len(want))
	}
	for index, key := range keys {
		if key.KeyID != want[index] {
			t.Errorf("keys[%d].key_id=%q, want %q", index, key.KeyID, want[index])
		}
	}
}

func TestProjectKeyAuthorityAndStorageFailuresFailClosed(t *testing.T) {
	ctx := context.Background()
	store := newMemoryProjectKeyStore()
	service := testProjectKeyService(store)
	metadata, secret, _, err := service.Create(ctx, "owner", "project-a", "test")
	if err != nil {
		t.Fatal(err)
	}

	service.lookup = func(context.Context, string) (*UserRecord, error) {
		return &UserRecord{Status: AccountSuspended}, nil
	}
	if _, err := service.Authenticate(ctx, secret, ""); !errors.Is(err, ErrProjectKeyAccessDenied) {
		t.Fatalf("suspended account error = %v", err)
	}
	service.lookup = func(context.Context, string) (*UserRecord, error) {
		return nil, errors.New("account storage unavailable")
	}
	if _, err := service.Authenticate(ctx, secret, ""); !errors.Is(err, ErrProjectKeyUnavailable) {
		t.Fatalf("account storage error = %v", err)
	}
	service.lookup = func(context.Context, string) (*UserRecord, error) {
		return &UserRecord{Status: AccountActive}, nil
	}
	service.authorize = func(context.Context, string, string) error { return ErrProjectPermissionUnavailable }
	if _, err := service.Authenticate(ctx, secret, ""); !errors.Is(err, ErrProjectKeyUnavailable) {
		t.Fatalf("project storage error = %v", err)
	}
	service.authorize = func(context.Context, string, string) error { return ErrProjectPermissionDenied }
	if _, err := service.Authenticate(ctx, secret, ""); !errors.Is(err, ErrProjectKeyAccessDenied) {
		t.Fatalf("ownership loss error = %v", err)
	}
	service.authorize = func(context.Context, string, string) error { return nil }
	store.readErr = errors.New("key storage unavailable")
	if _, err := service.Authenticate(ctx, secret, ""); !errors.Is(err, ErrProjectKeyUnavailable) {
		t.Fatalf("key storage error = %v", err)
	}
	store.readErr = nil
	if got := store.records[metadata.KeyID].State; got != "active" {
		t.Fatalf("read failure mutated key state to %q", got)
	}
}

func TestProjectKeyAmbiguousCreateAndRevokeRecovery(t *testing.T) {
	ctx := context.Background()
	store := newMemoryProjectKeyStore()
	service := testProjectKeyService(store)
	store.createErr = errors.New("ambiguous create response")
	store.commitCreateOnError = true
	metadata, secret, outcomeID, err := service.Create(ctx, "owner", "project-a", "recoverable")
	if err != nil || secret == "" || outcomeID != "" || metadata.State != "active" {
		t.Fatalf("matching create readback metadata=%#v secretPresent=%v outcomeID=%q err=%v", metadata, secret != "", outcomeID, err)
	}
	if store.createCalls != 1 {
		t.Fatalf("ambiguous create was retried %d times", store.createCalls)
	}

	absentStore := newMemoryProjectKeyStore()
	absentService := testProjectKeyService(absentStore)
	absentStore.createErr = errors.New("ambiguous create response")
	_, absentSecret, absentID, err := absentService.Create(ctx, "owner", "project-a", "late commit")
	if !errors.Is(err, ErrProjectKeyCreateUnknown) || absentSecret != "" || !validProjectKeyID(absentID) || absentStore.createCalls != 1 {
		t.Fatalf("absent readback secretPresent=%v id=%q calls=%d err=%v", absentSecret != "", absentID, absentStore.createCalls, err)
	}
	// The absent readback is not proof that the write can never appear.
	absentStore.records[absentID] = absentStore.lastCreated
	listed, err := absentService.List(ctx, "owner", "project-a")
	if err != nil || len(listed) != 1 || listed[0].KeyID != absentID {
		t.Fatalf("late-visible key list=%#v err=%v", listed, err)
	}
	absentStore.revokeErr = errors.New("ambiguous revoke response")
	absentStore.commitRevokeOnError = true
	revoked, err := absentService.Revoke(ctx, "owner", "project-a", absentID)
	if err != nil || revoked.State != "revoked" {
		t.Fatalf("committed revoke readback metadata=%#v err=%v", revoked, err)
	}
	if _, err := absentService.Authenticate(ctx, absentStore.lastCreatedSecretForTest(), ""); err == nil {
		t.Fatal("revoked key authenticated")
	}
}

func (s *memoryProjectKeyStore) lastCreatedSecretForTest() string {
	// The service intentionally does not retain secrets in the store. This
	// helper makes a token from the synthetic record only for revoked-state testing.
	secret := make([]byte, projectKeySecretBytes)
	for i := range secret {
		secret[i] = byte(i)
	}
	return projectKeyPrefix + s.lastCreated.KeyID + "." + base64.RawURLEncoding.EncodeToString(secret)
}

func TestProjectKeyNameAndRecordValidation(t *testing.T) {
	if name, ok := validProjectKeyName("  αβ  "); !ok || name != "αβ" {
		t.Fatalf("trimmed unicode name=%q valid=%v", name, ok)
	}
	if _, ok := validProjectKeyName(strings.Repeat("界", 65)); ok {
		t.Fatal("65-codepoint key label was accepted")
	}
	_, digest, _ := parseProjectKey(syntheticProjectKey)
	data := map[string]interface{}{
		"schema_version": 1,
		"key_id":         "00112233445566778899aabbccddeeff",
		"user_id":        "owner",
		"project_id":     "project-a",
		"name":           "test",
		"secret_hash":    hex.EncodeToString(digest[:]),
		"capabilities":   []string{"query"},
		"state":          "active",
		"created_at":     time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
	}
	if _, err := validateProjectKeyRecord("00112233445566778899aabbccddeeff", data); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	data["expires_at"] = time.Time{}
	if _, err := validateProjectKeyRecord("00112233445566778899aabbccddeeff", data); !errors.Is(err, ErrProjectKeyUnavailable) {
		t.Fatalf("record with unsupported field error = %v", err)
	}
}

func TestProjectKeyFirestoreLifecycleUsesConfiguredCollection(t *testing.T) {
	fs := accountEmulator(t)
	if err := scopedfirestore.RegisterLocalScope(fs, ""); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := fs.Collection("users").Doc("project-key-owner").Set(ctx, map[string]interface{}{
		"email": "owner@example.test", "role": "member", "status": AccountActive,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Collection("projects").Doc("project-key-owner_project-key-project").Set(ctx, map[string]interface{}{
		"user_id": "project-key-owner", "project_id": "project-key-project", "name": "Local project",
	}); err != nil {
		t.Fatal(err)
	}

	service := NewProjectKeyService(fs)
	metadata, secret, _, err := service.Create(ctx, "project-key-owner", "project-key-project", "local emulator")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := scopedfirestore.Collection(fs, "project_keys").Doc(metadata.KeyID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Data()) != 9 || stored.Data()["secret"] != nil || stored.Data()["expires_at"] != nil {
		t.Fatalf("unexpected active record shape: field_count=%d has_secret=%v has_expiry=%v", len(stored.Data()), stored.Data()["secret"] != nil, stored.Data()["expires_at"] != nil)
	}
	if _, err := service.Authenticate(ctx, secret, "project-key-project"); err != nil {
		t.Fatalf("emulator key auth: %v", err)
	}
	listed, err := service.List(ctx, "project-key-owner", "project-key-project")
	if err != nil || len(listed) != 1 || listed[0].KeyID != metadata.KeyID {
		t.Fatalf("emulator list=%#v err=%v", listed, err)
	}
	revoked, err := service.Revoke(ctx, "project-key-owner", "project-key-project", metadata.KeyID)
	if err != nil || revoked.State != "revoked" {
		t.Fatalf("emulator revoke=%#v err=%v", revoked, err)
	}
	if _, err := service.Authenticate(ctx, secret, ""); !errors.Is(err, ErrProjectKeyInvalid) {
		t.Fatalf("emulator revoked key error=%v", err)
	}
}

func TestSyntheticFixtureEncodingIsCanonical(t *testing.T) {
	secret := make([]byte, projectKeySecretBytes)
	for i := range secret {
		secret[i] = byte(i)
	}
	if base64.RawURLEncoding.EncodeToString(secret) != strings.SplitN(syntheticProjectKey, ".", 2)[1] {
		t.Fatal("synthetic fixture no longer encodes bytes 0x00 through 0x1f")
	}
}

var _ projectKeyStore = (*memoryProjectKeyStore)(nil)
