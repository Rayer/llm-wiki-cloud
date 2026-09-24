package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	syncBindingStatusActive    = "active"
	syncBindingStatusRevoked   = "revoked"
	syncBindingAuditCollection = "auth_sync_binding_audit"
)

var (
	ErrSyncBindingUnauthorized            = errors.New("sync binding unauthorized")
	ErrSyncBindingAlreadyBound            = errors.New("project already has a sync binding")
	ErrSyncBindingReauthorizationRequired = errors.New("explicit sync binding reauthorization required")
	ErrSyncBindingNotFound                = errors.New("sync binding not found")
	ErrSyncBindingUnavailable             = errors.New("sync binding authority unavailable")
	ErrSyncBindingInvalid                 = errors.New("invalid sync binding")
)

type syncBindingRecord struct {
	ID           string    `firestore:"binding_id" json:"binding_id"`
	Host         string    `firestore:"host" json:"host"`
	WikiID       string    `firestore:"wiki_id" json:"wiki_id"`
	ProjectID    string    `firestore:"project_id" json:"project_id"`
	Environment  string    `firestore:"environment" json:"environment"`
	AuthorizedBy string    `firestore:"authorized_by" json:"authorized_by"`
	Status       string    `firestore:"status" json:"status"`
	CreatedAt    time.Time `firestore:"created_at" json:"created_at"`
	UpdatedAt    time.Time `firestore:"updated_at" json:"updated_at"`
	RevokedAt    time.Time `firestore:"revoked_at,omitempty" json:"revoked_at,omitempty"`
}

// SyncBinding is the public control-plane view for one current project binding.
type SyncBinding struct {
	ID           string    `json:"binding_id"`
	Host         string    `json:"host"`
	WikiID       string    `json:"wiki_id"`
	ProjectID    string    `json:"project_id"`
	Environment  string    `json:"environment"`
	AuthorizedBy string    `json:"authorized_by"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	RevokedAt    time.Time `json:"revoked_at,omitempty"`
}

type syncBindingAuditEvent struct {
	Action    string    `firestore:"action"`
	ActorID   string    `firestore:"actor_id"`
	ProjectID string    `firestore:"project_id"`
	BindingID string    `firestore:"binding_id"`
	Reason    string    `firestore:"reason"`
	Result    string    `firestore:"result"`
	CreatedAt time.Time `firestore:"created_at"`
}

// SyncBindingAuthority stores the current binding on the project document,
// alongside the existing project owner record. It does not own sync data.
type SyncBindingAuthority struct {
	fs          *firestore.Client
	environment string
	host        string
	now         func() time.Time
}

func NewSyncBindingAuthority(fs *firestore.Client, environment, host string) *SyncBindingAuthority {
	return &SyncBindingAuthority{fs: fs, environment: strings.TrimSpace(environment), host: normalizeBindingHost(host), now: time.Now}
}

func (a *SyncBindingAuthority) ready() bool {
	return a != nil && a.fs != nil && strings.TrimSpace(a.environment) != "" && a.host != ""
}

func (a *SyncBindingAuthority) projectRef(userID, projectID string) *firestore.DocumentRef {
	return a.fs.Collection("projects").Doc(projectDocumentID(userID, projectID))
}

func (a *SyncBindingAuthority) CreateBinding(ctx context.Context, userID, projectID, wikiID, host string) (SyncBinding, error) {
	return a.writeBinding(ctx, "create", userID, projectID, "", wikiID, host)
}

// ReauthorizeBinding explicitly replaces the current binding after the caller
// confirms the target wiki and project. The prior binding ID must still match.
func (a *SyncBindingAuthority) ReauthorizeBinding(ctx context.Context, userID, projectID, currentBindingID, wikiID, host string) (SyncBinding, error) {
	if strings.TrimSpace(currentBindingID) == "" {
		return SyncBinding{}, ErrSyncBindingInvalid
	}
	return a.writeBinding(ctx, "reauthorize", userID, projectID, currentBindingID, wikiID, host)
}

func (a *SyncBindingAuthority) writeBinding(ctx context.Context, action, userID, projectID, oldBindingID, wikiID, host string) (SyncBinding, error) {
	userID, projectID, wikiID = strings.TrimSpace(userID), strings.TrimSpace(projectID), strings.TrimSpace(wikiID)
	if !a.ready() || !ValidPathSegment(userID) || !ValidPathSegment(projectID) || !ValidPathSegment(wikiID) || normalizeBindingHost(host) != a.host {
		return SyncBinding{}, ErrSyncBindingInvalid
	}
	bindingID, err := randomTokenID()
	if err != nil {
		return SyncBinding{}, ErrSyncBindingUnavailable
	}
	now := a.now().UTC()
	newBinding := syncBindingRecord{ID: bindingID, Host: a.host, WikiID: wikiID, ProjectID: projectID, Environment: a.environment, AuthorizedBy: userID, Status: syncBindingStatusActive, CreatedAt: now, UpdatedAt: now}
	ref := a.projectRef(userID, projectID)
	var result SyncBinding
	err = a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrProjectPermissionDenied
			}
			return ErrSyncBindingUnavailable
		}
		if _, ok := projectFromFirestoreDocument(userID, snapshot.Ref.ID, snapshot.Data()); !ok {
			return ErrProjectPermissionDenied
		}
		current, present, err := bindingFromProject(snapshot.Data())
		if err != nil {
			return ErrSyncBindingUnavailable
		}
		if action == "create" {
			if present {
				if current.Status == syncBindingStatusActive {
					if current.WikiID == wikiID && current.Host == a.host && current.Environment == a.environment {
						result = publicSyncBinding(current)
						return nil
					}
					return ErrSyncBindingAlreadyBound
				}
				return ErrSyncBindingReauthorizationRequired
			}
		} else if !present || current.ID != oldBindingID || current.ProjectID != projectID || current.Environment != a.environment {
			return ErrSyncBindingNotFound
		}
		if err := tx.Update(ref, []firestore.Update{{Path: "sync_binding", Value: newBinding}}); err != nil {
			return ErrSyncBindingUnavailable
		}
		result = publicSyncBinding(newBinding)
		reason := "user_authorized"
		if action == "reauthorize" {
			reason = "user_reauthorized"
		}
		return tx.Create(a.fs.Collection(syncBindingAuditCollection).NewDoc(), syncBindingAuditEvent{
			Action: "sync_binding_" + action, ActorID: userID, ProjectID: projectID, BindingID: bindingID,
			Reason: reason, Result: syncBindingStatusActive, CreatedAt: now,
		})
	})
	if err != nil {
		return SyncBinding{}, err
	}
	return result, nil
}

func (a *SyncBindingAuthority) RevokeBinding(ctx context.Context, userID, projectID, bindingID, reason string) error {
	userID, projectID, bindingID, reason = strings.TrimSpace(userID), strings.TrimSpace(projectID), strings.TrimSpace(bindingID), strings.TrimSpace(reason)
	if !a.ready() || !ValidPathSegment(userID) || !ValidPathSegment(projectID) || !ValidPathSegment(bindingID) || (reason != "user_revoked" && reason != "security_revoked") {
		return ErrSyncBindingInvalid
	}
	now := a.now().UTC()
	ref := a.projectRef(userID, projectID)
	return a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrSyncBindingNotFound
			}
			return ErrSyncBindingUnavailable
		}
		if _, ok := projectFromFirestoreDocument(userID, snapshot.Ref.ID, snapshot.Data()); !ok {
			return ErrProjectPermissionDenied
		}
		current, present, err := bindingFromProject(snapshot.Data())
		if err != nil {
			return ErrSyncBindingUnavailable
		}
		if !present || current.ID != bindingID || current.ProjectID != projectID || current.Environment != a.environment || current.Status != syncBindingStatusActive {
			return ErrSyncBindingNotFound
		}
		current.Status = syncBindingStatusRevoked
		current.UpdatedAt = now
		current.RevokedAt = now
		if err := tx.Update(ref, []firestore.Update{{Path: "sync_binding", Value: current}}); err != nil {
			return ErrSyncBindingUnavailable
		}
		return tx.Create(a.fs.Collection(syncBindingAuditCollection).NewDoc(), syncBindingAuditEvent{
			Action: "sync_binding_revoke", ActorID: userID, ProjectID: projectID, BindingID: bindingID,
			Reason: reason, Result: syncBindingStatusRevoked, CreatedAt: now,
		})
	})
}

// CheckSyncBinding is the future sync endpoint's authorization seam. It
// re-reads owner and binding state for every call; the binding ID is not a key.
func (a *SyncBindingAuthority) CheckSyncBinding(ctx context.Context, userID, projectID, bindingID, wikiID, host string) error {
	userID, projectID, bindingID, wikiID = strings.TrimSpace(userID), strings.TrimSpace(projectID), strings.TrimSpace(bindingID), strings.TrimSpace(wikiID)
	if !a.ready() || !ValidPathSegment(userID) || !ValidPathSegment(projectID) || !ValidPathSegment(bindingID) || !ValidPathSegment(wikiID) || normalizeBindingHost(host) != a.host {
		return ErrSyncBindingUnauthorized
	}
	snapshot, err := a.projectRef(userID, projectID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return ErrSyncBindingUnauthorized
		}
		return ErrSyncBindingUnavailable
	}
	if _, ok := projectFromFirestoreDocument(userID, snapshot.Ref.ID, snapshot.Data()); !ok {
		return ErrSyncBindingUnauthorized
	}
	current, present, err := bindingFromProject(snapshot.Data())
	if err != nil {
		return ErrSyncBindingUnavailable
	}
	if !present || current.ID != bindingID || current.ProjectID != projectID || current.WikiID != wikiID || current.Host != a.host || current.Environment != a.environment || current.Status != syncBindingStatusActive {
		return ErrSyncBindingUnauthorized
	}
	return nil
}

func (a *SyncBindingAuthority) ListBindings(ctx context.Context, userID string) ([]SyncBinding, error) {
	userID = strings.TrimSpace(userID)
	if !a.ready() || !ValidPathSegment(userID) {
		return nil, ErrSyncBindingUnavailable
	}
	it := a.fs.Collection("projects").Documents(ctx)
	defer it.Stop()
	var result []SyncBinding
	for {
		doc, err := it.Next()
		if err != nil {
			if errors.Is(err, iterator.Done) {
				break
			}
			return nil, ErrSyncBindingUnavailable
		}
		project, ok := projectFromFirestoreDocument(userID, doc.Ref.ID, doc.Data())
		if !ok {
			continue
		}
		binding, present, err := bindingFromProject(doc.Data())
		if err != nil {
			return nil, ErrSyncBindingUnavailable
		}
		if !present || binding.Environment != a.environment {
			continue
		}
		if binding.ProjectID == "" {
			binding.ProjectID = project.ID
		}
		result = append(result, publicSyncBinding(binding))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ProjectID < result[j].ProjectID })
	return result, nil
}

func normalizeBindingHost(raw string) string {
	u, err := cliVerificationOrigin(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func bindingFromProject(data map[string]interface{}) (syncBindingRecord, bool, error) {
	raw, ok := data["sync_binding"]
	if !ok || raw == nil {
		return syncBindingRecord{}, false, nil
	}
	var binding syncBindingRecord
	switch value := raw.(type) {
	case map[string]interface{}:
		readString := func(key string) string { value, _ := value[key].(string); return value }
		binding.ID = readString("binding_id")
		binding.Host = readString("host")
		binding.WikiID = readString("wiki_id")
		binding.ProjectID = readString("project_id")
		binding.Environment = readString("environment")
		binding.AuthorizedBy = readString("authorized_by")
		binding.Status = readString("status")
		binding.CreatedAt, _ = value["created_at"].(time.Time)
		binding.UpdatedAt, _ = value["updated_at"].(time.Time)
		binding.RevokedAt, _ = value["revoked_at"].(time.Time)
	case syncBindingRecord:
		binding = value
	default:
		return syncBindingRecord{}, false, errors.New("invalid sync binding record")
	}
	if !ValidPathSegment(binding.ID) || !ValidPathSegment(binding.ProjectID) || !ValidPathSegment(binding.WikiID) || binding.Host == "" || (binding.Status != syncBindingStatusActive && binding.Status != syncBindingStatusRevoked) {
		return syncBindingRecord{}, false, errors.New("invalid sync binding fields")
	}
	return binding, true, nil
}
func publicSyncBinding(binding syncBindingRecord) SyncBinding {
	return SyncBinding{ID: binding.ID, Host: binding.Host, WikiID: binding.WikiID, ProjectID: binding.ProjectID, Environment: binding.Environment, AuthorizedBy: binding.AuthorizedBy, Status: binding.Status, CreatedAt: binding.CreatedAt, UpdatedAt: binding.UpdatedAt, RevokedAt: binding.RevokedAt}
}
