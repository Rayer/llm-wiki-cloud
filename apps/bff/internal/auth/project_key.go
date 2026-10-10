package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/config"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	projectKeyPrefix       = "lwc_pk_"
	projectKeySchema       = 1
	projectKeyIDBytes      = 16
	projectKeySecretBytes  = 32
	projectKeyCapability   = "query"
	projectKeyMaximumTries = 3
)

var (
	ErrProjectKeyInvalid        = errors.New("invalid project key")
	ErrProjectKeyAccessDenied   = errors.New("project key access denied")
	ErrProjectKeyUnavailable    = errors.New("project key authority unavailable")
	ErrProjectKeyNotFound       = errors.New("project key not found")
	ErrProjectKeyInvalidInput   = errors.New("invalid project key input")
	ErrProjectKeyCreateUnknown  = errors.New("project key creation outcome unknown")
	projectKeyAllowedRecordKeys = map[string]struct{}{
		"schema_version": {}, "key_id": {}, "user_id": {}, "project_id": {}, "name": {},
		"secret_hash": {}, "capabilities": {}, "state": {}, "created_at": {}, "revoked_at": {},
	}
)

// ProjectKeyService stores opaque, project-bound query credentials in the
// configured Firestore database. Dynamic keys are data, not application config.
type ProjectKeyService struct {
	store     projectKeyStore
	usage     projectKeyUsageStore
	lookup    AccountLookup
	authorize ProjectOwnerAuthorizer
}

type projectKeyStore interface {
	Create(context.Context, ProjectKeyRecord) (time.Time, error)
	Read(context.Context, string) (ProjectKeyRecord, error)
	List(context.Context, string) ([]ProjectKeyRecord, error)
	Revoke(context.Context, string, string, string) error
}

type firestoreProjectKeyStore struct{ fs *firestore.Client }

type ProjectKeyRecord struct {
	SchemaVersion int       `firestore:"schema_version" json:"schema_version"`
	KeyID         string    `firestore:"key_id" json:"key_id"`
	UserID        string    `firestore:"user_id" json:"user_id"`
	ProjectID     string    `firestore:"project_id" json:"project_id"`
	Name          string    `firestore:"name" json:"name"`
	SecretHash    string    `firestore:"secret_hash" json:"secret_hash"`
	Capabilities  []string  `firestore:"capabilities" json:"capabilities"`
	State         string    `firestore:"state" json:"state"`
	CreatedAt     time.Time `firestore:"created_at" json:"created_at"`
	RevokedAt     time.Time `firestore:"revoked_at,omitempty" json:"revoked_at,omitempty"`
}

// ProjectKeyMetadata is safe to return after creation or from later reads.
type ProjectKeyMetadata struct {
	KeyID          string     `json:"key_id"`
	Name           string     `json:"name"`
	ProjectID      string     `json:"project_id"`
	Capabilities   []string   `json:"capabilities"`
	State          string     `json:"state"`
	CreatedAt      time.Time  `json:"created_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	LastUsedStatus string     `json:"last_used_status,omitempty"`
}

func NewProjectKeyService(fs *firestore.Client) *ProjectKeyService {
	return &ProjectKeyService{
		store:     firestoreProjectKeyStore{fs: fs},
		usage:     firestoreProjectKeyUsageStore{fs: fs},
		lookup:    FirestoreAccountLookup(fs),
		authorize: FirestoreProjectOwnerAuthorizer(fs),
	}
}

func newProjectKeyService(store projectKeyStore, usage projectKeyUsageStore, lookup AccountLookup, authorize ProjectOwnerAuthorizer) *ProjectKeyService {
	return &ProjectKeyService{store: store, usage: usage, lookup: lookup, authorize: authorize}
}

func (r ProjectKeyRecord) Metadata() ProjectKeyMetadata {
	metadata := ProjectKeyMetadata{
		KeyID: r.KeyID, Name: r.Name, ProjectID: r.ProjectID,
		Capabilities: []string{projectKeyCapability}, State: r.State, CreatedAt: r.CreatedAt,
	}
	if !r.RevokedAt.IsZero() {
		revokedAt := r.RevokedAt
		metadata.RevokedAt = &revokedAt
	}
	return metadata
}

func generateProjectKey() (id, token, secretHash string, err error) {
	idBytes := make([]byte, projectKeyIDBytes)
	secretBytes := make([]byte, projectKeySecretBytes)
	if _, err = rand.Read(idBytes); err != nil {
		return "", "", "", err
	}
	if _, err = rand.Read(secretBytes); err != nil {
		return "", "", "", err
	}
	id = hex.EncodeToString(idBytes)
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	digest := sha256.Sum256(secretBytes)
	return id, projectKeyPrefix + id + "." + secret, hex.EncodeToString(digest[:]), nil
}

func parseProjectKey(token string) (id string, secretDigest [sha256.Size]byte, err error) {
	if len(token) != len(projectKeyPrefix)+projectKeyIDBytes*2+1+43 || !strings.HasPrefix(token, projectKeyPrefix) {
		return "", secretDigest, ErrProjectKeyInvalid
	}
	rest := strings.TrimPrefix(token, projectKeyPrefix)
	id, secret, found := strings.Cut(rest, ".")
	if !found || len(id) != projectKeyIDBytes*2 || len(secret) != 43 {
		return "", secretDigest, ErrProjectKeyInvalid
	}
	idBytes, decodeErr := hex.DecodeString(id)
	if decodeErr != nil || len(idBytes) != projectKeyIDBytes || hex.EncodeToString(idBytes) != id {
		return "", secretDigest, ErrProjectKeyInvalid
	}
	secretBytes, decodeErr := base64.RawURLEncoding.DecodeString(secret)
	if decodeErr != nil || len(secretBytes) != projectKeySecretBytes || base64.RawURLEncoding.EncodeToString(secretBytes) != secret {
		return "", secretDigest, ErrProjectKeyInvalid
	}
	return id, sha256.Sum256(secretBytes), nil
}

func validProjectKeyID(id string) bool {
	if len(id) != projectKeyIDBytes*2 {
		return false
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == projectKeyIDBytes && hex.EncodeToString(decoded) == id
}

func validProjectKeyName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	count := utf8.RuneCountInString(name)
	return name, count >= 1 && count <= 64
}

func validateProjectKeyRecord(docID string, data map[string]interface{}) (ProjectKeyRecord, error) {
	for field := range data {
		if _, ok := projectKeyAllowedRecordKeys[field]; !ok {
			return ProjectKeyRecord{}, ErrProjectKeyUnavailable
		}
	}
	var record ProjectKeyRecord
	if err := firestoreDataTo(data, &record); err != nil {
		return ProjectKeyRecord{}, ErrProjectKeyUnavailable
	}
	name, validName := validProjectKeyName(record.Name)
	digest, digestErr := hex.DecodeString(record.SecretHash)
	if record.SchemaVersion != projectKeySchema || !validProjectKeyID(docID) || record.KeyID != docID ||
		!ValidPathSegment(record.UserID) || !ValidPathSegment(record.ProjectID) || !validName || name != record.Name ||
		digestErr != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != record.SecretHash ||
		len(record.Capabilities) != 1 || record.Capabilities[0] != projectKeyCapability || record.CreatedAt.IsZero() {
		return ProjectKeyRecord{}, ErrProjectKeyUnavailable
	}
	switch record.State {
	case "active":
		if !record.RevokedAt.IsZero() {
			return ProjectKeyRecord{}, ErrProjectKeyUnavailable
		}
	case "revoked":
		if record.RevokedAt.IsZero() {
			return ProjectKeyRecord{}, ErrProjectKeyUnavailable
		}
	default:
		return ProjectKeyRecord{}, ErrProjectKeyUnavailable
	}
	return record, nil
}

// firestoreDataTo keeps record decoding local without exposing a Firestore
// document's unrecognized fields through the public metadata projection.
func firestoreDataTo(data map[string]interface{}, target interface{}) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func (s firestoreProjectKeyStore) collection() *firestore.CollectionRef {
	return scopedfirestore.Collection(s.fs, "project_keys")
}

func (s firestoreProjectKeyStore) Create(ctx context.Context, record ProjectKeyRecord) (time.Time, error) {
	if s.fs == nil {
		return time.Time{}, ErrProjectKeyUnavailable
	}
	result, err := s.collection().Doc(record.KeyID).Create(ctx, map[string]interface{}{
		"schema_version": record.SchemaVersion,
		"key_id":         record.KeyID,
		"user_id":        record.UserID,
		"project_id":     record.ProjectID,
		"name":           record.Name,
		"secret_hash":    record.SecretHash,
		"capabilities":   []string{projectKeyCapability},
		"state":          "active",
		"created_at":     firestore.ServerTimestamp,
	})
	if err != nil {
		return time.Time{}, err
	}
	if result == nil {
		return time.Time{}, nil
	}
	return result.UpdateTime, nil
}

func (s firestoreProjectKeyStore) Read(ctx context.Context, id string) (ProjectKeyRecord, error) {
	if s.fs == nil {
		return ProjectKeyRecord{}, ErrProjectKeyUnavailable
	}
	snapshot, err := s.collection().Doc(id).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ProjectKeyRecord{}, ErrProjectKeyNotFound
		}
		return ProjectKeyRecord{}, ErrProjectKeyUnavailable
	}
	return validateProjectKeyRecord(snapshot.Ref.ID, snapshot.Data())
}

func (s firestoreProjectKeyStore) List(ctx context.Context, userID string) ([]ProjectKeyRecord, error) {
	if s.fs == nil {
		return nil, ErrProjectKeyUnavailable
	}
	iter := s.collection().Where("user_id", "==", userID).Documents(ctx)
	defer iter.Stop()
	records := make([]ProjectKeyRecord, 0)
	for {
		doc, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, ErrProjectKeyUnavailable
		}
		record, err := validateProjectKeyRecord(doc.Ref.ID, doc.Data())
		if err != nil || record.UserID != userID {
			return nil, ErrProjectKeyUnavailable
		}
		records = append(records, record)
	}
	return records, nil
}

func (s firestoreProjectKeyStore) Revoke(ctx context.Context, userID, projectID, id string) error {
	if s.fs == nil {
		return ErrProjectKeyUnavailable
	}
	ref := s.collection().Doc(id)
	return s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(ref)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrProjectKeyNotFound
			}
			return err
		}
		record, err := validateProjectKeyRecord(doc.Ref.ID, doc.Data())
		if err != nil {
			return err
		}
		if record.UserID != userID || record.ProjectID != projectID {
			return ErrProjectKeyNotFound
		}
		if record.State == "revoked" {
			return nil
		}
		return tx.Update(ref, []firestore.Update{
			{Path: "state", Value: "revoked"},
			{Path: "revoked_at", Value: firestore.ServerTimestamp},
		})
	})
}

func (s *ProjectKeyService) requireOwner(ctx context.Context, userID, projectID string) error {
	if s == nil || s.store == nil || s.authorize == nil {
		return ErrProjectKeyUnavailable
	}
	if !ValidPathSegment(userID) || !ValidPathSegment(projectID) {
		return ErrProjectKeyInvalidInput
	}
	if err := s.authorize(ctx, userID, projectID); err != nil {
		if errors.Is(err, ErrProjectPermissionDenied) {
			return ErrProjectKeyAccessDenied
		}
		return ErrProjectKeyUnavailable
	}
	return nil
}

// Authenticate validates one query credential and rechecks live account and
// project ownership for each request. requestedProject may be empty; when
// present, it must equal the project's fixed ID.
func (s *ProjectKeyService) Authenticate(ctx context.Context, token, requestedProject string) (ProjectKeyRecord, error) {
	id, expectedDigest, err := parseProjectKey(token)
	if err != nil {
		return ProjectKeyRecord{}, ErrProjectKeyInvalid
	}
	if s == nil || s.store == nil || s.lookup == nil || s.authorize == nil {
		return ProjectKeyRecord{}, ErrProjectKeyUnavailable
	}
	record, err := s.store.Read(ctx, id)
	if errors.Is(err, ErrProjectKeyNotFound) {
		return ProjectKeyRecord{}, ErrProjectKeyInvalid
	}
	if err != nil {
		return ProjectKeyRecord{}, ErrProjectKeyUnavailable
	}
	if record.State != "active" {
		return ProjectKeyRecord{}, ErrProjectKeyInvalid
	}
	storedDigest, _ := hex.DecodeString(record.SecretHash)
	if subtle.ConstantTimeCompare(storedDigest, expectedDigest[:]) != 1 {
		return ProjectKeyRecord{}, ErrProjectKeyInvalid
	}
	if requestedProject != "" && requestedProject != record.ProjectID {
		return ProjectKeyRecord{}, ErrProjectKeyAccessDenied
	}
	user, err := s.lookup(ctx, record.UserID)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ProjectKeyRecord{}, ErrProjectKeyAccessDenied
		}
		return ProjectKeyRecord{}, ErrProjectKeyUnavailable
	}
	if !user.Active() {
		return ProjectKeyRecord{}, ErrProjectKeyAccessDenied
	}
	if err := s.authorize(ctx, record.UserID, record.ProjectID); err != nil {
		if errors.Is(err, ErrProjectPermissionDenied) {
			return ProjectKeyRecord{}, ErrProjectKeyAccessDenied
		}
		return ProjectKeyRecord{}, ErrProjectKeyUnavailable
	}
	if s.usage != nil {
		usedAt := time.Now().UTC()
		usageCtx, cancel := context.WithTimeout(ctx, projectKeyUsageWriteTimeout)
		_ = s.usage.Record(usageCtx, projectKeyUsageRecord{
			SchemaVersion: projectKeyUsageSchema,
			KeyID:         record.KeyID,
			UserID:        record.UserID,
			ProjectID:     record.ProjectID,
			LastUsedAt:    usedAt,
		})
		cancel()
	}
	return record, nil
}

// List returns only safe metadata for keys owned by the current project owner.
func (s *ProjectKeyService) List(ctx context.Context, userID, projectID string) ([]ProjectKeyMetadata, error) {
	if err := s.requireOwner(ctx, userID, projectID); err != nil {
		return nil, err
	}
	records, err := s.store.List(ctx, userID)
	if err != nil {
		return nil, ErrProjectKeyUnavailable
	}
	keys := make([]ProjectKeyMetadata, 0)
	for _, record := range records {
		if record.UserID != userID {
			return nil, ErrProjectKeyUnavailable
		}
		if record.ProjectID == projectID {
			keys = append(keys, record.Metadata())
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].CreatedAt.Equal(keys[j].CreatedAt) {
			return keys[i].KeyID < keys[j].KeyID
		}
		return keys[i].CreatedAt.After(keys[j].CreatedAt)
	})
	if len(keys) > 0 {
		keyIDs := make([]string, len(keys))
		for index := range keys {
			keyIDs[index] = keys[index].KeyID
		}
		usageByKey := map[string]projectKeyUsageStatus(nil)
		var usageErr error
		if s.usage == nil {
			usageErr = errProjectKeyUsageUnavailable
		} else {
			usageByKey, usageErr = s.usage.List(ctx, userID, projectID, keyIDs)
		}
		for index := range keys {
			if usageErr != nil {
				keys[index].LastUsedStatus = projectKeyUsageUnavailable
				continue
			}
			usage, ok := usageByKey[keys[index].KeyID]
			if !ok {
				keys[index].LastUsedStatus = projectKeyUsageUnavailable
				continue
			}
			switch usage.Status {
			case projectKeyUsageAvailable:
				if usage.LastUsedAt == nil || usage.LastUsedAt.IsZero() {
					keys[index].LastUsedStatus = projectKeyUsageUnavailable
					continue
				}
				lastUsedAt := usage.LastUsedAt.UTC()
				keys[index].LastUsedAt = &lastUsedAt
				keys[index].LastUsedStatus = projectKeyUsageAvailable
			case projectKeyUsageNoRecord:
				keys[index].LastUsedStatus = projectKeyUsageNoRecord
			default:
				keys[index].LastUsedStatus = projectKeyUsageUnavailable
			}
		}
	}
	return keys, nil
}

// Create issues a 256-bit secret once. The third return value is only a safe
// correlation ID when an ambiguous write could not be reconciled.
func (s *ProjectKeyService) Create(ctx context.Context, userID, projectID, name string) (metadata ProjectKeyMetadata, secret string, outcomeKeyID string, err error) {
	if err := s.requireOwner(ctx, userID, projectID); err != nil {
		return ProjectKeyMetadata{}, "", "", err
	}
	name, ok := validProjectKeyName(name)
	if !ok {
		return ProjectKeyMetadata{}, "", "", ErrProjectKeyInvalidInput
	}
	for attempt := 0; attempt < projectKeyMaximumTries; attempt++ {
		id, token, digest, err := generateProjectKey()
		if err != nil {
			return ProjectKeyMetadata{}, "", "", ErrProjectKeyUnavailable
		}
		record := ProjectKeyRecord{
			SchemaVersion: projectKeySchema, KeyID: id, UserID: userID, ProjectID: projectID,
			Name: name, SecretHash: digest, Capabilities: []string{projectKeyCapability}, State: "active",
		}
		createdAt, createErr := s.store.Create(ctx, record)
		if createErr == nil {
			if createdAt.IsZero() {
				record, readErr := s.store.Read(ctx, id)
				if readErr != nil || !projectKeyCreateMatches(record, id, userID, projectID, name, digest) {
					return ProjectKeyMetadata{}, "", id, ErrProjectKeyCreateUnknown
				}
				return record.Metadata(), token, "", nil
			}
			record.CreatedAt = createdAt
			return record.Metadata(), token, "", nil
		}
		if status.Code(createErr) == codes.AlreadyExists {
			continue
		}
		record, readErr := s.store.Read(ctx, id)
		if readErr == nil && projectKeyCreateMatches(record, id, userID, projectID, name, digest) {
			return record.Metadata(), token, "", nil
		}
		return ProjectKeyMetadata{}, "", id, ErrProjectKeyCreateUnknown
	}
	return ProjectKeyMetadata{}, "", "", ErrProjectKeyUnavailable
}

func projectKeyCreateMatches(record ProjectKeyRecord, id, userID, projectID, name, digest string) bool {
	return record.KeyID == id && record.UserID == userID && record.ProjectID == projectID &&
		record.Name == name && record.SecretHash == digest && record.State == "active" &&
		record.SchemaVersion == projectKeySchema && len(record.Capabilities) == 1 &&
		record.Capabilities[0] == projectKeyCapability
}

// Revoke is idempotent and never deletes or reactivates a project key.
func (s *ProjectKeyService) Revoke(ctx context.Context, userID, projectID, id string) (ProjectKeyMetadata, error) {
	if err := s.requireOwner(ctx, userID, projectID); err != nil {
		return ProjectKeyMetadata{}, err
	}
	if !validProjectKeyID(id) {
		return ProjectKeyMetadata{}, ErrProjectKeyNotFound
	}
	err := s.store.Revoke(ctx, userID, projectID, id)
	if errors.Is(err, ErrProjectKeyNotFound) {
		return ProjectKeyMetadata{}, ErrProjectKeyNotFound
	}
	// A transaction error can follow a committed revoke; only the readback
	// decides whether the owner can be told that the transition completed.
	record, readErr := s.store.Read(ctx, id)
	if errors.Is(readErr, ErrProjectKeyNotFound) {
		return ProjectKeyMetadata{}, ErrProjectKeyNotFound
	}
	if readErr == nil {
		if record.UserID != userID || record.ProjectID != projectID {
			return ProjectKeyMetadata{}, ErrProjectKeyNotFound
		}
		if record.State == "revoked" {
			return record.Metadata(), nil
		}
	}
	return ProjectKeyMetadata{}, ErrProjectKeyUnavailable
}

// ProjectKeyAuth accepts project keys only on a route where the caller mounts
// this middleware. The record, not the supplied project header, sets authority.
func ProjectKeyAuth(service *ProjectKeyService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := projectKeyBearer(c.GetHeader("Authorization"))
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": ErrProjectKeyInvalid.Error()})
			return
		}
		if service == nil || service.store == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": ErrProjectKeyUnavailable.Error()})
			return
		}
		record, err := service.Authenticate(c.Request.Context(), token, c.GetHeader("X-Project-ID"))
		switch {
		case errors.Is(err, ErrProjectKeyInvalid):
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": ErrProjectKeyInvalid.Error()})
			return
		case errors.Is(err, ErrProjectKeyAccessDenied):
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": ErrProjectKeyAccessDenied.Error()})
			return
		case err != nil:
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": ErrProjectKeyUnavailable.Error()})
			return
		}
		c.Set("userID", record.UserID)
		c.Set("projectID", record.ProjectID)
		c.Set("clientKind", "project-key")
		if c.GetHeader("X-Project-ID") == "" {
			c.Request.Header.Set("X-Project-ID", record.ProjectID)
		}
		c.Next()
	}
}

// ProjectKeyOrJWTAuth runs the existing JWT middleware for every ordinary
// Bearer token and the project-key middleware only for the exact key prefix.
func ProjectKeyOrJWTAuth(jwtAuth, projectKeyAuth gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token, ok := projectKeyBearer(c.GetHeader("Authorization")); ok && strings.HasPrefix(token, projectKeyPrefix) {
			projectKeyAuth(c)
			return
		}
		jwtAuth(c)
	}
}

func projectKeyBearer(value string) (string, bool) {
	parts := strings.SplitN(value, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// ProjectKeyManagementJWTAuth preserves ordinary invalid-token behavior while
// exposing a temporary account lookup failure as 503 on the new management
// routes only. CLI session validation and project authorization are unchanged.
func ProjectKeyManagementJWTAuth(cfg config.Config, lookup AccountLookup, verifySession CLIAccessSessionVerifier, authorizeProject ...ProjectOwnerAuthorizer) gin.HandlerFunc {
	var projectAuthorizer ProjectOwnerAuthorizer
	if len(authorizeProject) > 0 {
		projectAuthorizer = authorizeProject[0]
	}
	return jwtAuth(cfg, lookup, true, verifySession, projectAuthorizer, http.StatusServiceUnavailable)
}

func ProjectKeyErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrProjectKeyInvalid):
		return http.StatusUnauthorized
	case errors.Is(err, ErrProjectKeyAccessDenied):
		return http.StatusForbidden
	case errors.Is(err, ErrProjectKeyNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrProjectKeyInvalidInput):
		return http.StatusBadRequest
	default:
		return http.StatusServiceUnavailable
	}
}

func ProjectKeySafeError(err error) string {
	if errors.Is(err, ErrProjectKeyInvalid) {
		return ErrProjectKeyInvalid.Error()
	}
	if errors.Is(err, ErrProjectKeyAccessDenied) {
		return ErrProjectKeyAccessDenied.Error()
	}
	if errors.Is(err, ErrProjectKeyNotFound) {
		return ErrProjectKeyNotFound.Error()
	}
	if errors.Is(err, ErrProjectKeyInvalidInput) {
		return ErrProjectKeyInvalidInput.Error()
	}
	return ErrProjectKeyUnavailable.Error()
}
