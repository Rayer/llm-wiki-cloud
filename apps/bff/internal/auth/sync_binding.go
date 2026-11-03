package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	ErrSyncBindingConflict                = errors.New("sync binding target changed")
	ErrSyncBindingOperationConflict       = errors.New("sync binding operation ID conflict")
)

const syncBindingAdminReceiptCollection = "auth_sync_binding_admin_receipts"

var syncBindingAdminOperationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type syncBindingRecord struct {
	ID               string    `firestore:"binding_id" json:"binding_id"`
	Host             string    `firestore:"host" json:"host"`
	WikiID           string    `firestore:"wiki_id" json:"wiki_id"`
	ProjectID        string    `firestore:"project_id" json:"project_id"`
	Environment      string    `firestore:"environment" json:"environment"`
	AuthorizedBy     string    `firestore:"authorized_by" json:"authorized_by"`
	Status           string    `firestore:"status" json:"status"`
	CreatedAt        time.Time `firestore:"created_at" json:"created_at"`
	UpdatedAt        time.Time `firestore:"updated_at" json:"updated_at"`
	RevokedAt        time.Time `firestore:"revoked_at,omitempty" json:"revoked_at,omitempty"`
	RevokedBy        string    `firestore:"revoked_by,omitempty" json:"revoked_by,omitempty"`
	RevocationReason string    `firestore:"revocation_reason,omitempty" json:"revocation_reason,omitempty"`
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
	Action      string    `firestore:"action"`
	ActorID     string    `firestore:"actor_id"`
	SubjectID   string    `firestore:"subject_id,omitempty"`
	ProjectID   string    `firestore:"project_id"`
	BindingID   string    `firestore:"binding_id"`
	OperationID string    `firestore:"operation_id,omitempty"`
	Reason      string    `firestore:"reason"`
	Result      string    `firestore:"result"`
	CreatedAt   time.Time `firestore:"created_at"`
}

type AdminSyncBindingView struct {
	BindingID        string     `json:"binding_id"`
	Host             string     `json:"host"`
	WikiID           string     `json:"wiki_id"`
	ProjectID        string     `json:"project_id"`
	Environment      string     `json:"environment"`
	AuthorizedBy     string     `json:"authorized_by"`
	Status           string     `json:"status"`
	CreatedAt        *time.Time `json:"created_at"`
	UpdatedAt        *time.Time `json:"updated_at"`
	RevokedAt        *time.Time `json:"revoked_at"`
	RevokedBy        string     `json:"revoked_by"`
	RevocationReason string     `json:"revocation_reason"`
	Revision         string     `json:"revision"`
}

type AdminSyncBindingRevocationRequest struct {
	ActorID          string
	SubjectID        string
	ProjectRecordID  string
	ProjectID        string
	BindingID        string
	OperationID      string
	Environment      string
	Reason           string
	ExpectedRevision string
}

type AdminSyncBindingRevocationReceipt struct {
	SchemaVersion          int       `json:"schema_version" firestore:"schema_version"`
	OperationID            string    `json:"operation_id" firestore:"operation_id"`
	Authority              string    `json:"authority" firestore:"authority"`
	Environment            string    `json:"environment" firestore:"environment"`
	ActorID                string    `json:"actor_id" firestore:"actor_id"`
	SubjectID              string    `json:"subject" firestore:"subject"`
	TargetKind             string    `json:"target_kind" firestore:"target_kind"`
	TargetID               *string   `json:"target_id" firestore:"target_id"`
	ClientID               *string   `json:"client_id" firestore:"client_id"`
	ProjectID              *string   `json:"project_id" firestore:"project_id"`
	Reason                 string    `json:"reason" firestore:"reason"`
	Result                 string    `json:"result" firestore:"result"`
	CommittedAt            time.Time `json:"committed_at" firestore:"committed_at"`
	ManagementAuthorizedAt time.Time `json:"management_authorized_at" firestore:"management_authorized_at"`
	PreviousCLIEpoch       *int64    `json:"previous_cli_epoch" firestore:"previous_cli_epoch"`
	CurrentCLIEpoch        *int64    `json:"current_cli_epoch" firestore:"current_cli_epoch"`
	AffectedCount          *int64    `json:"affected_count" firestore:"affected_count"`
	CountComplete          bool      `json:"count_complete" firestore:"count_complete"`
	Fingerprint            string    `json:"-" firestore:"fingerprint"`
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

func (a *SyncBindingAuthority) Environment() string {
	if a == nil {
		return ""
	}
	return a.environment
}

func (a *SyncBindingAuthority) projectRef(userID, projectID string) *firestore.DocumentRef {
	return scopedfirestore.Collection(a.fs, "projects").Doc(projectDocumentID(userID, projectID))
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
		return tx.Create(scopedfirestore.Collection(a.fs, syncBindingAuditCollection).NewDoc(), syncBindingAuditEvent{
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
		return tx.Create(scopedfirestore.Collection(a.fs, syncBindingAuditCollection).NewDoc(), syncBindingAuditEvent{
			Action: "sync_binding_revoke", ActorID: userID, ProjectID: projectID, BindingID: bindingID,
			Reason: reason, Result: syncBindingStatusRevoked, CreatedAt: now,
		})
	})
}

// AdminBinding reads only the binding attached to the resolved authoritative
// Project document. A missing binding is a successful nil result.
func (a *SyncBindingAuthority) AdminBinding(ctx context.Context, recordID, ownerID, projectID string) (*AdminSyncBindingView, string, error) {
	if !a.ready() || !ValidPathSegment(recordID) || !ValidPathSegment(ownerID) || !ValidPathSegment(projectID) ||
		projectDocumentID(ownerID, projectID) != recordID {
		return nil, "", ErrSyncBindingInvalid
	}
	snapshot, err := a.projectRef(ownerID, projectID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, "", ErrSyncBindingNotFound
		}
		return nil, "", ErrSyncBindingUnavailable
	}
	if snapshot.Ref.ID != recordID {
		return nil, "", ErrSyncBindingNotFound
	}
	if _, ok := projectFromFirestoreDocument(ownerID, snapshot.Ref.ID, snapshot.Data()); !ok {
		return nil, "", ErrSyncBindingNotFound
	}
	revision := snapshot.UpdateTime.UTC().Format(time.RFC3339Nano)
	binding, present, err := bindingFromProject(snapshot.Data())
	if err != nil {
		return nil, "", ErrSyncBindingUnavailable
	}
	if !present {
		return nil, revision, nil
	}
	if binding.ProjectID != projectID || binding.Environment != a.environment || binding.Host != a.host {
		return nil, "", ErrSyncBindingUnavailable
	}
	view := adminSyncBindingView(binding, revision)
	return &view, revision, nil
}

// AdminRevokeBinding performs a subject-scoped, revision-checked admin revoke.
// authorize is called from inside every Firestore transaction attempt so a
// retry must obtain fresh management authority again.
func (a *SyncBindingAuthority) AdminRevokeBinding(ctx context.Context, request AdminSyncBindingRevocationRequest,
	authorize func(context.Context) (time.Time, error)) (AdminSyncBindingRevocationReceipt, error) {
	request.ActorID, request.SubjectID = strings.TrimSpace(request.ActorID), strings.TrimSpace(request.SubjectID)
	request.ProjectRecordID, request.ProjectID = strings.TrimSpace(request.ProjectRecordID), strings.TrimSpace(request.ProjectID)
	request.BindingID, request.Reason = strings.TrimSpace(request.BindingID), strings.TrimSpace(request.Reason)
	if !a.ready() || authorize == nil || request.Environment != a.environment || !ValidPathSegment(request.ActorID) ||
		!ValidPathSegment(request.SubjectID) || request.SubjectID == "" || !ValidPathSegment(request.ProjectRecordID) ||
		!ValidPathSegment(request.ProjectID) || projectDocumentID(request.SubjectID, request.ProjectID) != request.ProjectRecordID ||
		!ValidPathSegment(request.BindingID) || !validSyncBindingOperationID(request.OperationID) ||
		!validSyncBindingAdminReason(request.Reason) || request.ExpectedRevision == "" {
		return AdminSyncBindingRevocationReceipt{}, ErrSyncBindingInvalid
	}
	fingerprint := syncBindingAdminFingerprint(request)
	receiptRef := a.adminRevocationReceiptRef(request.OperationID)
	var result AdminSyncBindingRevocationReceipt
	err := a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = AdminSyncBindingRevocationReceipt{}
		managementAuthorizedAt, err := authorize(ctx)
		if err != nil {
			return err
		}
		if managementAuthorizedAt.IsZero() {
			return ErrSyncBindingUnauthorized
		}
		prior, err := tx.Get(receiptRef)
		if err == nil {
			var receipt AdminSyncBindingRevocationReceipt
			if prior.DataTo(&receipt) != nil || receipt.SchemaVersion != 1 || receipt.OperationID != request.OperationID {
				return ErrSyncBindingUnavailable
			}
			if receipt.Fingerprint != fingerprint || receipt.ActorID != request.ActorID {
				return ErrSyncBindingOperationConflict
			}
			result = receipt
			return nil
		}
		if status.Code(err) != codes.NotFound {
			return ErrSyncBindingUnavailable
		}

		projectRef := a.projectRef(request.SubjectID, request.ProjectID)
		snapshot, err := tx.Get(projectRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrSyncBindingNotFound
			}
			return ErrSyncBindingUnavailable
		}
		if snapshot.Ref.ID != request.ProjectRecordID {
			return ErrSyncBindingNotFound
		}
		if _, ok := projectFromFirestoreDocument(request.SubjectID, snapshot.Ref.ID, snapshot.Data()); !ok {
			return ErrSyncBindingNotFound
		}
		current, present, err := bindingFromProject(snapshot.Data())
		if err != nil {
			return ErrSyncBindingUnavailable
		}
		if !present {
			return ErrSyncBindingNotFound
		}
		if current.ID != request.BindingID {
			return ErrSyncBindingConflict
		}
		if current.ProjectID != request.ProjectID || current.Environment != a.environment || current.Host != a.host {
			return ErrSyncBindingUnavailable
		}
		if snapshot.UpdateTime.UTC().Format(time.RFC3339Nano) != request.ExpectedRevision {
			return ErrSyncBindingConflict
		}
		now := time.Now().UTC()
		receipt := AdminSyncBindingRevocationReceipt{SchemaVersion: 1, OperationID: request.OperationID, Authority: "lwc",
			Environment: a.environment, ActorID: request.ActorID, SubjectID: request.SubjectID, TargetKind: "sync_binding",
			TargetID: syncStringPointer(request.BindingID), ProjectID: syncStringPointer(request.ProjectID), Reason: request.Reason,
			CommittedAt: now, ManagementAuthorizedAt: managementAuthorizedAt, AffectedCount: syncInt64Pointer(0), CountComplete: true,
			Fingerprint: fingerprint}
		resultStatus := "already_revoked"
		if current.Status == syncBindingStatusActive {
			current.Status = syncBindingStatusRevoked
			current.UpdatedAt, current.RevokedAt = now, now
			current.RevokedBy, current.RevocationReason = request.ActorID, request.Reason
			if err := tx.Update(projectRef, []firestore.Update{{Path: "sync_binding", Value: current}}); err != nil {
				return ErrSyncBindingUnavailable
			}
			receipt.AffectedCount = syncInt64Pointer(1)
			resultStatus = "revoked"
		}
		receipt.Result = resultStatus
		audit := syncBindingAuditEvent{Action: "sync_binding_admin_revoke", ActorID: request.ActorID, SubjectID: request.SubjectID,
			ProjectID: request.ProjectID, BindingID: request.BindingID, OperationID: request.OperationID, Reason: request.Reason,
			Result: resultStatus, CreatedAt: now}
		if err := tx.Create(scopedfirestore.Collection(a.fs, syncBindingAuditCollection).NewDoc(), audit); err != nil {
			return ErrSyncBindingUnavailable
		}
		if err := tx.Create(receiptRef, receipt); err != nil {
			return ErrSyncBindingUnavailable
		}
		result = receipt
		return nil
	})
	if err != nil {
		return AdminSyncBindingRevocationReceipt{}, err
	}
	return result, nil
}

func (a *SyncBindingAuthority) AdminReadBindingRevocationReceipt(ctx context.Context, actorID, operationID string) (AdminSyncBindingRevocationReceipt, error) {
	if !a.ready() || !ValidPathSegment(actorID) || !validSyncBindingOperationID(operationID) {
		return AdminSyncBindingRevocationReceipt{}, ErrSyncBindingInvalid
	}
	snapshot, err := a.adminRevocationReceiptRef(operationID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return AdminSyncBindingRevocationReceipt{}, ErrSyncBindingNotFound
		}
		return AdminSyncBindingRevocationReceipt{}, ErrSyncBindingUnavailable
	}
	var receipt AdminSyncBindingRevocationReceipt
	if snapshot.DataTo(&receipt) != nil || receipt.SchemaVersion != 1 || receipt.OperationID != operationID || receipt.ActorID != actorID {
		return AdminSyncBindingRevocationReceipt{}, ErrSyncBindingNotFound
	}
	return receipt, nil
}

func (a *SyncBindingAuthority) adminRevocationReceiptRef(operationID string) *firestore.DocumentRef {
	sum := sha256.Sum256([]byte(a.environment + "\x00" + operationID))
	return scopedfirestore.Collection(a.fs, syncBindingAdminReceiptCollection).Doc(hex.EncodeToString(sum[:]))
}

func adminSyncBindingView(binding syncBindingRecord, revision string) AdminSyncBindingView {
	return AdminSyncBindingView{BindingID: binding.ID, Host: binding.Host, WikiID: binding.WikiID, ProjectID: binding.ProjectID,
		Environment: binding.Environment, AuthorizedBy: binding.AuthorizedBy, Status: binding.Status,
		CreatedAt: syncOptionalTime(binding.CreatedAt), UpdatedAt: syncOptionalTime(binding.UpdatedAt), RevokedAt: syncOptionalTime(binding.RevokedAt),
		RevokedBy: binding.RevokedBy, RevocationReason: binding.RevocationReason, Revision: revision}
}

func syncOptionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func syncStringPointer(value string) *string { return &value }

func syncInt64Pointer(value int64) *int64 { return &value }

func validSyncBindingOperationID(value string) bool {
	return syncBindingAdminOperationIDPattern.MatchString(value)
}

func validSyncBindingAdminReason(value string) bool {
	if utf8.RuneCountInString(value) == 0 || utf8.RuneCountInString(value) > 256 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func syncBindingAdminFingerprint(request AdminSyncBindingRevocationRequest) string {
	data := strings.Join([]string{"lwc/admin-binding-revocation/v1", request.ActorID, request.Environment, request.SubjectID,
		request.ProjectRecordID, request.ProjectID, request.BindingID, request.OperationID, request.Reason, request.ExpectedRevision}, "\x00")
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
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
	it := scopedfirestore.Collection(a.fs, "projects").Documents(ctx)
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
		binding.RevokedBy = readString("revoked_by")
		binding.RevocationReason = readString("revocation_reason")
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
