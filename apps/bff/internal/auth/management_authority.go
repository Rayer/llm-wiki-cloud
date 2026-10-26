package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/config"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	ManagementScopeAuthAdmin = "auth:admin"
	ManagementScopeLWCAdmin  = "lwc:admin"
	managementSchemaVersion  = 1
)

var managementConfigIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var (
	ErrManagementUnavailable            = errors.New("management authority unavailable")
	ErrManagementAuthorityUninitialized = errors.New("management authority epoch is not initialized")
	ErrManagementDenied                 = errors.New("auth:admin capability required")
	ErrManagementInvalid                = errors.New("invalid management operation")
	ErrManagementConflict               = errors.New("management operation ID conflict")
	ErrManagementLastAdmin              = errors.New("at least one active grant must remain")
	ErrManagementTarget                 = errors.New("management target must be an active account")
	ErrManagementOperationNotFound      = errors.New("management operation not found")
)

type ManagementAuthority struct {
	fs          *firestore.Client
	environment string
	scopes      []config.AuthManagementScope
}

type ManagementAuthorityEpoch struct {
	SchemaVersion  int       `firestore:"schema_version" json:"schema_version"`
	Mode           string    `firestore:"mode" json:"mode"`
	Epoch          int64     `firestore:"epoch" json:"epoch"`
	MigrationRunID string    `firestore:"migration_run_id" json:"migration_run_id"`
	ConfigID       string    `firestore:"config_id" json:"config_id"`
	UpdatedAt      time.Time `firestore:"updated_at" json:"updated_at"`
}

type ManagementScopeState struct {
	State     string    `firestore:"state" json:"state"`
	Revision  int64     `firestore:"revision" json:"revision"`
	Source    string    `firestore:"source" json:"source"`
	UpdatedAt time.Time `firestore:"updated_at" json:"updated_at"`
}

type managementPrincipal struct {
	SchemaVersion    int                                  `firestore:"schema_version"`
	ScopeStates      map[string]ManagementScopeState      `firestore:"scope_states"`
	GrantedScopeIDs  []string                             `firestore:"granted_scope_ids"`
	LegacyMigration  map[string]ManagementMigrationMarker `firestore:"legacy_migration"`
	DocumentRevision int64                                `firestore:"document_revision"`
}

type ManagementMigrationMarker struct {
	RunID           string    `firestore:"run_id" json:"run_id"`
	SourceTimestamp time.Time `firestore:"source_timestamp" json:"source_timestamp"`
	Disposition     string    `firestore:"disposition" json:"disposition"`
	Reason          string    `firestore:"reason,omitempty" json:"reason,omitempty"`
}

type ManagementMigrationTarget struct {
	UserID           string    `json:"user_id"`
	Role             string    `json:"source_role"`
	Status           string    `json:"source_status"`
	SourceUpdateTime time.Time `json:"source_update_time"`
	Scopes           []string  `json:"scopes"`
}

type ManagementMigrationResult struct {
	RunID       string `json:"run_id"`
	TargetID    string `json:"target_user_id"`
	Scope       string `json:"scope"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason,omitempty"`
	Revision    int64  `json:"revision,omitempty"`
}

type ManagementMutation struct {
	OperationID string
	TargetID    string
	Scope       string
	Action      string
}

type ManagementMutationResult struct {
	OperationID string `json:"operation_id"`
	TargetID    string `json:"target_user_id"`
	Scope       string `json:"scope"`
	State       string `json:"state"`
	Revision    int64  `json:"revision"`
}

type managementOperation struct {
	SchemaVersion   int                        `firestore:"schema_version"`
	OperationID     string                     `firestore:"operation_id"`
	ActorID         string                     `firestore:"actor_id"`
	TargetID        string                     `firestore:"target_id"`
	Scope           string                     `firestore:"scope"`
	Action          string                     `firestore:"action"`
	Fingerprint     string                     `firestore:"fingerprint"`
	Result          ManagementMutationResult   `firestore:"result"`
	MigrationResult *ManagementMigrationResult `firestore:"migration_result,omitempty"`
	CreatedAt       time.Time                  `firestore:"created_at"`
}

func NewManagementAuthority(fs *firestore.Client, environment string) *ManagementAuthority {
	return NewManagementAuthorityWithCatalog(fs, environment, []config.AuthManagementScope{
		{ID: ManagementScopeAuthAdmin, Product: "auth", GrantMode: "operator-only", GrantorScopes: []string{ManagementScopeAuthAdmin}},
		{ID: ManagementScopeLWCAdmin, Product: "lwc", GrantMode: "operator-only", GrantorScopes: []string{ManagementScopeAuthAdmin}},
	})
}

func NewManagementAuthorityWithCatalog(fs *firestore.Client, environment string, scopes []config.AuthManagementScope) *ManagementAuthority {
	return &ManagementAuthority{fs: fs, environment: strings.TrimSpace(environment), scopes: append([]config.AuthManagementScope(nil), scopes...)}
}

func (a *ManagementAuthority) ready() bool {
	return a != nil && a.fs != nil && ValidPathSegment(a.environment)
}

func (a *ManagementAuthority) namespace() *firestore.DocumentRef {
	return scopedfirestore.Collection(a.fs, "auth_management").Doc(a.environment)
}

func (a *ManagementAuthority) authorityRef() *firestore.DocumentRef {
	return a.namespace().Collection("authority").Doc("current")
}

func (a *ManagementAuthority) AuthorityEpoch(ctx context.Context) (ManagementAuthorityEpoch, error) {
	if !a.ready() {
		return ManagementAuthorityEpoch{}, ErrManagementUnavailable
	}
	doc, err := a.authorityRef().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return ManagementAuthorityEpoch{}, ErrManagementAuthorityUninitialized
	}
	if err != nil {
		return ManagementAuthorityEpoch{}, ErrManagementUnavailable
	}
	epoch, err := decodeManagementAuthorityEpoch(doc)
	if err != nil {
		return ManagementAuthorityEpoch{}, ErrManagementUnavailable
	}
	return epoch, nil
}

func (a *ManagementAuthority) authorityEpochInTransaction(ctx context.Context, tx *firestore.Transaction) (ManagementAuthorityEpoch, error) {
	doc, err := tx.Get(a.authorityRef())
	if status.Code(err) == codes.NotFound {
		return ManagementAuthorityEpoch{}, ErrManagementAuthorityUninitialized
	}
	if err != nil {
		return ManagementAuthorityEpoch{}, ErrManagementUnavailable
	}
	epoch, err := decodeManagementAuthorityEpoch(doc)
	if err != nil {
		return ManagementAuthorityEpoch{}, ErrManagementUnavailable
	}
	return epoch, nil
}

func decodeManagementAuthorityEpoch(doc *firestore.DocumentSnapshot) (ManagementAuthorityEpoch, error) {
	var epoch ManagementAuthorityEpoch
	if doc == nil || doc.DataTo(&epoch) != nil || epoch.SchemaVersion != 1 ||
		(epoch.Mode != "legacy-shadow" && epoch.Mode != "central") || epoch.Epoch < 1 || epoch.UpdatedAt.IsZero() {
		return ManagementAuthorityEpoch{}, ErrManagementUnavailable
	}
	if epoch.ConfigID != "" && !managementConfigIDPattern.MatchString(epoch.ConfigID) {
		return ManagementAuthorityEpoch{}, ErrManagementUnavailable
	}
	if epoch.Mode == "central" && !ValidPathSegment(epoch.MigrationRunID) {
		return ManagementAuthorityEpoch{}, ErrManagementUnavailable
	}
	return epoch, nil
}

func (a *ManagementAuthority) InitializeLegacyShadow(ctx context.Context, configID string) (ManagementAuthorityEpoch, error) {
	if !a.ready() || (configID != "" && !managementConfigIDPattern.MatchString(configID)) {
		return ManagementAuthorityEpoch{}, ErrManagementInvalid
	}
	var result ManagementAuthorityEpoch
	err := a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		createAuthority := false
		doc, err := tx.Get(a.authorityRef())
		if err == nil {
			result, err = decodeManagementAuthorityEpoch(doc)
			if err != nil || result.Mode != "legacy-shadow" {
				return ErrManagementConflict
			}
		} else if status.Code(err) == codes.NotFound {
			result = ManagementAuthorityEpoch{SchemaVersion: 1, Mode: "legacy-shadow", Epoch: 1, ConfigID: configID, UpdatedAt: time.Now().UTC()}
			createAuthority = true
		} else {
			return ErrManagementUnavailable
		}
		locks := make(map[string]int64, 2)
		missing := make([]string, 0, 2)
		for _, scope := range []string{ManagementScopeAuthAdmin, ManagementScopeLWCAdmin} {
			lockDoc, lockErr := tx.Get(a.lockRef(scope))
			if status.Code(lockErr) == codes.NotFound {
				locks[scope] = 0
				missing = append(missing, scope)
				continue
			}
			if lockErr != nil {
				return ErrManagementUnavailable
			}
			locks[scope], lockErr = readManagementLockRevision(lockDoc)
			if lockErr != nil {
				return ErrManagementUnavailable
			}
		}
		if createAuthority {
			if err := tx.Create(a.authorityRef(), result); err != nil {
				return err
			}
		}
		for _, scope := range missing {
			if err := tx.Create(a.lockRef(scope), map[string]any{"schema_version": 1, "scope": scope, "revision": locks[scope], "updated_at": time.Now().UTC()}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ManagementAuthorityEpoch{}, err
	}
	return result, nil
}

func (a *ManagementAuthority) CutoverToCentral(ctx context.Context, runID, configID string, targets []ManagementMigrationTarget) (ManagementAuthorityEpoch, error) {
	if !a.ready() || !ValidPathSegment(runID) || (configID != "" && !managementConfigIDPattern.MatchString(configID)) || targets == nil {
		return ManagementAuthorityEpoch{}, ErrManagementInvalid
	}
	manifestTargets := make(map[string]ManagementMigrationTarget, len(targets))
	for _, target := range targets {
		if !ValidPathSegment(target.UserID) || target.Role != "admin" || target.SourceUpdateTime.IsZero() ||
			!equalStringSet(target.Scopes, []string{ManagementScopeAuthAdmin, ManagementScopeLWCAdmin}) {
			return ManagementAuthorityEpoch{}, ErrManagementInvalid
		}
		if _, duplicate := manifestTargets[target.UserID]; duplicate {
			return ManagementAuthorityEpoch{}, ErrManagementInvalid
		}
		manifestTargets[target.UserID] = target
		for _, scope := range target.Scopes {
			result, err := a.ReadLegacyMigrationScope(ctx, runID, target.UserID, scope)
			if err != nil || (result.Disposition != "applied" && result.Disposition != "skipped") {
				return ManagementAuthorityEpoch{}, ErrManagementConflict
			}
		}
	}
	var result ManagementAuthorityEpoch
	err := a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(a.authorityRef())
		if status.Code(err) == codes.NotFound {
			return ErrManagementAuthorityUninitialized
		}
		if err != nil {
			return ErrManagementUnavailable
		}
		current, err := decodeManagementAuthorityEpoch(doc)
		if err != nil {
			return ErrManagementUnavailable
		}
		if current.Mode == "central" {
			if current.MigrationRunID == runID {
				result = current
				return nil
			}
			return ErrManagementConflict
		}
		legacyAdmins, err := tx.Documents(scopedfirestore.Collection(a.fs, "users").Where("role", "==", "admin")).GetAll()
		if err != nil {
			return ErrManagementUnavailable
		}
		if len(legacyAdmins) != len(manifestTargets) {
			return ErrManagementConflict
		}
		for _, doc := range legacyAdmins {
			target, ok := manifestTargets[doc.Ref.ID]
			var user UserRecord
			if !ok || doc.DataTo(&user) != nil || user.Role != target.Role || user.Status != target.Status ||
				!doc.UpdateTime.Equal(target.SourceUpdateTime) {
				return ErrManagementConflict
			}
		}
		locks := make(map[string]int64, 2)
		for _, scope := range []string{ManagementScopeAuthAdmin, ManagementScopeLWCAdmin} {
			lockDoc, lockErr := tx.Get(a.lockRef(scope))
			if lockErr == nil {
				locks[scope], lockErr = readManagementLockRevision(lockDoc)
				if lockErr != nil {
					return ErrManagementUnavailable
				}
			} else if status.Code(lockErr) == codes.NotFound {
				return ErrManagementUnavailable
			} else {
				return ErrManagementUnavailable
			}
		}
		for scope, revision := range locks {
			if revision == math.MaxInt64 {
				return ErrManagementUnavailable
			}
			if err := tx.Update(a.lockRef(scope), []firestore.Update{{Path: "revision", Value: revision + 1}, {Path: "updated_at", Value: time.Now().UTC()}}); err != nil {
				return err
			}
		}
		if current.Epoch == math.MaxInt64 {
			return ErrManagementUnavailable
		}
		result = ManagementAuthorityEpoch{SchemaVersion: 1, Mode: "central", Epoch: current.Epoch + 1,
			MigrationRunID: runID, ConfigID: configID, UpdatedAt: time.Now().UTC()}
		return tx.Set(a.authorityRef(), result)
	})
	if err != nil {
		return ManagementAuthorityEpoch{}, err
	}
	return result, nil
}

func (a *ManagementAuthority) principalRef(userID string) *firestore.DocumentRef {
	return a.namespace().Collection("principals").Doc(userID)
}

func (a *ManagementAuthority) lockRef(scope string) *firestore.DocumentRef {
	return a.namespace().Collection("scope_locks").Doc(scope)
}

func (a *ManagementAuthority) operationRef(operationID string) *firestore.DocumentRef {
	return a.namespace().Collection("operations").Doc(operationID)
}

// GrantedScopeVersions returns current explicit grants. During the bounded
// legacy-shadow epoch only an untouched legacy admin retains its two old
// capabilities; any principal or migration marker switches to explicit state.
func (a *ManagementAuthority) GrantedScopeVersions(ctx context.Context, userID string) (map[string]int64, error) {
	if !a.ready() || !ValidPathSegment(userID) {
		return nil, ErrManagementUnavailable
	}
	user, err := GetUser(ctx, a.fs, userID)
	if err != nil || !user.Active() {
		return nil, ErrManagementUnavailable
	}
	epoch, err := a.AuthorityEpoch(ctx)
	if errors.Is(err, ErrManagementAuthorityUninitialized) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := a.principalRef(userID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		if epoch.Mode == "legacy-shadow" && user.Role == "admin" {
			return map[string]int64{ManagementScopeAuthAdmin: epoch.Epoch, ManagementScopeLWCAdmin: epoch.Epoch}, nil
		}
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, ErrManagementUnavailable
	}
	principal, err := decodeManagementPrincipal(doc)
	if err != nil {
		return nil, ErrManagementUnavailable
	}
	if epoch.Mode == "legacy-shadow" && user.Role == "admin" && !managementPrincipalHasExplicitAuthority(principal) {
		return map[string]int64{ManagementScopeAuthAdmin: epoch.Epoch, ManagementScopeLWCAdmin: epoch.Epoch}, nil
	}
	versions := make(map[string]int64)
	for scope, state := range principal.ScopeStates {
		if managementScopeAllowed(scope) && state.State == "granted" && state.Revision > 0 {
			versions[scope] = state.Revision
		}
	}
	return versions, nil
}

func (a *ManagementAuthority) ScopeStates(ctx context.Context, userID string) (map[string]ManagementScopeState, error) {
	if !a.ready() || !ValidPathSegment(userID) {
		return nil, ErrManagementUnavailable
	}
	doc, err := a.principalRef(userID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return map[string]ManagementScopeState{}, nil
	}
	if err != nil {
		return nil, ErrManagementUnavailable
	}
	principal, err := decodeManagementPrincipal(doc)
	if err != nil {
		return nil, ErrManagementUnavailable
	}
	states := make(map[string]ManagementScopeState, len(principal.ScopeStates))
	for scope, state := range principal.ScopeStates {
		states[scope] = state
	}
	return states, nil
}

func (a *ManagementAuthority) UpdateAccountStatus(ctx context.Context, actorID string, actorCaps map[string]int64, targetID, nextStatus string) error {
	if !a.ready() || !ValidPathSegment(actorID) || !ValidPathSegment(targetID) ||
		(nextStatus != AccountActive && nextStatus != AccountSuspended) {
		return ErrManagementInvalid
	}
	if actorID == targetID && nextStatus == AccountSuspended {
		return ErrAccountSelfSuspension
	}
	return a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		epoch, err := a.authorityEpochInTransaction(ctx, tx)
		if err != nil {
			return err
		}
		actorDoc, err := tx.Get(scopedfirestore.Collection(a.fs, "users").Doc(actorID))
		if err != nil {
			return ErrManagementDenied
		}
		var actor UserRecord
		if actorDoc.DataTo(&actor) != nil || !actor.Active() {
			return ErrManagementDenied
		}
		actorPrincipal := managementPrincipal{SchemaVersion: managementSchemaVersion, ScopeStates: map[string]ManagementScopeState{}}
		actorPrincipalDoc, actorPrincipalErr := tx.Get(a.principalRef(actorID))
		if actorPrincipalErr == nil {
			actorPrincipal, err = decodeManagementPrincipal(actorPrincipalDoc)
			if err != nil {
				return ErrManagementDenied
			}
		} else if status.Code(actorPrincipalErr) != codes.NotFound {
			return ErrManagementUnavailable
		}
		if !a.hasCurrentManagementCap(actorPrincipal, &actor, actorCaps, ManagementScopeAuthAdmin, epoch) {
			return ErrManagementDenied
		}
		targetRef := scopedfirestore.Collection(a.fs, "users").Doc(targetID)
		targetDoc, err := tx.Get(targetRef)
		if err != nil {
			return ErrManagementTarget
		}
		var target UserRecord
		if targetDoc.DataTo(&target) != nil {
			return ErrManagementUnavailable
		}
		if target.Status == nextStatus || (target.Status == "" && nextStatus == AccountActive) {
			return nil
		}
		principalDoc, err := tx.Get(a.principalRef(targetID))
		var principal managementPrincipal
		if err == nil {
			principal, err = decodeManagementPrincipal(principalDoc)
			if err != nil {
				return ErrManagementUnavailable
			}
		} else if status.Code(err) == codes.NotFound {
			principal = managementPrincipal{SchemaVersion: managementSchemaVersion, ScopeStates: map[string]ManagementScopeState{}}
		} else {
			return ErrManagementUnavailable
		}
		scopes := []string{ManagementScopeAuthAdmin, ManagementScopeLWCAdmin}
		locks := make(map[string]int64, len(scopes))
		for _, scope := range scopes {
			lockDoc, lockErr := tx.Get(a.lockRef(scope))
			if lockErr == nil {
				locks[scope], err = readManagementLockRevision(lockDoc)
				if err != nil {
					return ErrManagementUnavailable
				}
			} else if status.Code(lockErr) == codes.NotFound {
				return ErrManagementUnavailable
			} else {
				return ErrManagementUnavailable
			}
		}
		for _, scope := range scopes {
			state := principal.ScopeStates[scope]
			if state.State != "granted" || nextStatus != AccountSuspended || target.Status == AccountSuspended {
				continue
			}
			active, err := a.activeManagementMemberCount(ctx, tx, scope)
			if err != nil {
				return err
			}
			if active <= 1 {
				return ErrManagementLastAdmin
			}
		}
		updates := []firestore.Update{{Path: "status", Value: nextStatus}}
		if nextStatus == AccountSuspended && target.Status != AccountSuspended {
			if target.AuthVersion < 0 || target.AuthVersion == math.MaxInt64 {
				return ErrManagementInvalid
			}
			updates = append(updates, firestore.Update{Path: "auth_version", Value: target.AuthVersion + 1}, firestore.Update{Path: "auth_invalid_before", Value: firestore.ServerTimestamp})
		} else if nextStatus == AccountActive && target.Status == AccountSuspended {
			updates = append(updates, firestore.Update{Path: "auth_invalid_before", Value: targetDoc.UpdateTime})
		}
		if err := tx.Update(targetRef, updates); err != nil {
			return err
		}
		if nextStatus == AccountSuspended {
			for _, scope := range scopes {
				if locks[scope] == math.MaxInt64 {
					return ErrManagementUnavailable
				}
				if err := tx.Update(a.lockRef(scope), []firestore.Update{{Path: "revision", Value: locks[scope] + 1}, {Path: "updated_at", Value: time.Now().UTC()}}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (a *ManagementAuthority) DeleteAccount(ctx context.Context, actorID string, actorCaps map[string]int64, targetID string) error {
	if !a.ready() || !ValidPathSegment(actorID) || !ValidPathSegment(targetID) {
		return ErrManagementInvalid
	}
	return a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		epoch, err := a.authorityEpochInTransaction(ctx, tx)
		if err != nil {
			return err
		}
		actorDoc, err := tx.Get(scopedfirestore.Collection(a.fs, "users").Doc(actorID))
		if err != nil {
			return ErrManagementDenied
		}
		var actor UserRecord
		if actorDoc.DataTo(&actor) != nil || !actor.Active() {
			return ErrManagementDenied
		}
		actorPrincipal := managementPrincipal{SchemaVersion: managementSchemaVersion, ScopeStates: map[string]ManagementScopeState{}}
		actorPrincipalDoc, actorPrincipalErr := tx.Get(a.principalRef(actorID))
		if actorPrincipalErr == nil {
			actorPrincipal, err = decodeManagementPrincipal(actorPrincipalDoc)
			if err != nil {
				return ErrManagementDenied
			}
		} else if status.Code(actorPrincipalErr) != codes.NotFound {
			return ErrManagementUnavailable
		}
		if !a.hasCurrentManagementCap(actorPrincipal, &actor, actorCaps, ManagementScopeAuthAdmin, epoch) {
			return ErrManagementDenied
		}
		targetRef := scopedfirestore.Collection(a.fs, "users").Doc(targetID)
		targetDoc, targetErr := tx.Get(targetRef)
		principalDoc, principalErr := tx.Get(a.principalRef(targetID))
		var principal managementPrincipal
		if principalErr == nil {
			principal, err = decodeManagementPrincipal(principalDoc)
			if err != nil {
				return ErrManagementUnavailable
			}
		} else if status.Code(principalErr) == codes.NotFound {
			principal = managementPrincipal{SchemaVersion: managementSchemaVersion, ScopeStates: map[string]ManagementScopeState{}}
		} else {
			return ErrManagementUnavailable
		}
		if status.Code(targetErr) == codes.NotFound {
			if principal.LegacyMigration != nil && principal.LegacyMigration[ManagementScopeAuthAdmin].Disposition == "account-deleted" &&
				principal.LegacyMigration[ManagementScopeLWCAdmin].Disposition == "account-deleted" {
				return nil
			}
			return ErrManagementTarget
		}
		if targetErr != nil {
			return ErrManagementUnavailable
		}
		var target UserRecord
		if targetDoc.DataTo(&target) != nil {
			return ErrManagementUnavailable
		}
		scopes := []string{ManagementScopeAuthAdmin, ManagementScopeLWCAdmin}
		locks := make(map[string]int64, len(scopes))
		for _, scope := range scopes {
			lockDoc, lockErr := tx.Get(a.lockRef(scope))
			if lockErr == nil {
				locks[scope], err = readManagementLockRevision(lockDoc)
				if err != nil {
					return ErrManagementUnavailable
				}
			} else if status.Code(lockErr) == codes.NotFound {
				return ErrManagementUnavailable
			} else {
				return ErrManagementUnavailable
			}
		}
		if principal.LegacyMigration == nil {
			principal.LegacyMigration = map[string]ManagementMigrationMarker{}
		}
		for _, scope := range scopes {
			state := principal.ScopeStates[scope]
			if state.State == "granted" && target.Active() {
				active, err := a.activeManagementMemberCount(ctx, tx, scope)
				if err != nil {
					return err
				}
				if active <= 1 {
					return ErrManagementLastAdmin
				}
			}
		}
		for _, scope := range scopes {
			state := principal.ScopeStates[scope]
			if state.State == "granted" {
				if state.Revision == math.MaxInt64 || locks[scope] == math.MaxInt64 {
					return ErrManagementUnavailable
				}
				state.State, state.Source, state.Revision, state.UpdatedAt = "revoked", "account-deleted", state.Revision+1, time.Now().UTC()
				principal.ScopeStates[scope] = state
				if err := tx.Update(a.lockRef(scope), []firestore.Update{{Path: "revision", Value: locks[scope] + 1}, {Path: "updated_at", Value: time.Now().UTC()}}); err != nil {
					return err
				}
			}
			principal.LegacyMigration[scope] = ManagementMigrationMarker{RunID: "account-deleted", SourceTimestamp: targetDoc.UpdateTime, Disposition: "account-deleted"}
		}
		principal.SchemaVersion = managementSchemaVersion
		principal.DocumentRevision++
		principal.GrantedScopeIDs = managementGrantedIDs(principal.ScopeStates)
		if err := tx.Set(a.principalRef(targetID), principal); err != nil {
			return err
		}
		return tx.Delete(targetRef)
	})
}

func (a *ManagementAuthority) activeManagementMemberCount(ctx context.Context, tx *firestore.Transaction, scope string) (int, error) {
	epoch, err := a.authorityEpochInTransaction(ctx, tx)
	if err != nil {
		return 0, err
	}
	docs, err := tx.Documents(a.namespace().Collection("principals").Where("granted_scope_ids", "array-contains", scope)).GetAll()
	if err != nil {
		return 0, ErrManagementUnavailable
	}
	principals := make(map[string]managementPrincipal, len(docs))
	for _, doc := range docs {
		principal, err := decodeManagementPrincipal(doc)
		if err != nil {
			return 0, ErrManagementUnavailable
		}
		principals[doc.Ref.ID] = principal
	}
	ids := make(map[string]bool, len(docs))
	for _, doc := range docs {
		ids[doc.Ref.ID] = true
	}
	if epoch.Mode == "legacy-shadow" {
		legacyDocs, err := tx.Documents(scopedfirestore.Collection(a.fs, "users").Where("role", "==", "admin")).GetAll()
		if err != nil {
			return 0, ErrManagementUnavailable
		}
		for _, doc := range legacyDocs {
			userID := doc.Ref.ID
			ids[userID] = true
			if _, loaded := principals[userID]; loaded {
				continue
			}
			principalDoc, principalErr := tx.Get(a.principalRef(userID))
			if status.Code(principalErr) == codes.NotFound {
				continue
			}
			if principalErr != nil {
				return 0, ErrManagementUnavailable
			}
			principal, decodeErr := decodeManagementPrincipal(principalDoc)
			if decodeErr != nil {
				return 0, ErrManagementUnavailable
			}
			principals[userID] = principal
		}
	}
	active := 0
	for userID := range ids {
		userDoc, err := tx.Get(scopedfirestore.Collection(a.fs, "users").Doc(userID))
		if status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return 0, ErrManagementUnavailable
		}
		var user UserRecord
		if userDoc.DataTo(&user) != nil {
			return 0, ErrManagementUnavailable
		}
		principal, hasPrincipal := principals[userID]
		state, hasState := principal.ScopeStates[scope]
		explicitGrant := hasPrincipal && hasState && state.State == "granted"
		legacyGrant := epoch.Mode == "legacy-shadow" && user.Role == "admin" && (!hasPrincipal || !managementPrincipalHasExplicitAuthority(principal))
		if user.Active() && (explicitGrant || legacyGrant) {
			active++
		}
	}
	return active, nil
}

func issueManagedWebAccessToken(ctx context.Context, management *ManagementAuthority, resource, userID, role, secret string, version int64) (string, error) {
	if management == nil {
		return GenerateAccessToken(userID, role, secret, version)
	}
	if strings.TrimSpace(resource) == "" {
		// Legacy schema-1/2 configurations and direct unit fixtures do not
		// declare a first-party consumer. Schema 3 requires it at load time.
		return GenerateAccessToken(userID, role, secret, version)
	}
	caps, err := management.GrantedScopeVersions(ctx, userID)
	if err != nil {
		return "", err
	}
	return GenerateFirstPartyAccessToken(userID, role, secret, resource, caps, version)
}

func issueManagedCLIAccessToken(ctx context.Context, management *ManagementAuthority, resource, userID, role, sessionID, secret string, version int64) (string, error) {
	if management == nil {
		return GenerateCLIAccessToken(userID, role, sessionID, secret, version)
	}
	if strings.TrimSpace(resource) == "" {
		return GenerateCLIAccessToken(userID, role, sessionID, secret, version)
	}
	caps, err := management.GrantedScopeVersions(ctx, userID)
	if err != nil {
		return "", err
	}
	return GenerateFirstPartyCLIAccessToken(userID, role, sessionID, secret, resource, caps, version)
}

// EffectiveScopes verifies signed token revisions against the latest
// entitlement state. A removed and later regranted scope cannot revive an
// older token because the new entitlement has a larger revision.
func (a *ManagementAuthority) EffectiveScopes(ctx context.Context, userID string, caps map[string]int64) ([]string, error) {
	if !a.ready() || !ValidPathSegment(userID) {
		return nil, ErrManagementUnavailable
	}
	user, err := GetUser(ctx, a.fs, userID)
	if err != nil {
		return nil, ErrManagementUnavailable
	}
	if !user.Active() {
		return []string{}, nil
	}
	epoch, err := a.AuthorityEpoch(ctx)
	if errors.Is(err, ErrManagementAuthorityUninitialized) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	doc, err := a.principalRef(userID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		if epoch.Mode == "legacy-shadow" && user.Role == "admin" {
			return shadowManagementScopes(caps, epoch.Epoch), nil
		}
		return []string{}, nil
	}
	if err != nil {
		return nil, ErrManagementUnavailable
	}
	principal, err := decodeManagementPrincipal(doc)
	if err != nil {
		return nil, ErrManagementUnavailable
	}
	if epoch.Mode == "legacy-shadow" && user.Role == "admin" && !managementPrincipalHasExplicitAuthority(principal) {
		return shadowManagementScopes(caps, epoch.Epoch), nil
	}
	scopes := make([]string, 0, len(caps))
	for scope, revision := range caps {
		state, ok := principal.ScopeStates[scope]
		if ok && managementScopeAllowed(scope) && state.State == "granted" && revision == state.Revision {
			scopes = append(scopes, scope)
		}
	}
	sort.Strings(scopes)
	return scopes, nil
}

// MutateGrant serializes grant/removal by scope lock, checks the actor's
// current auth:admin capability in the transaction, and protects the last
// active member of each registered management scope.
func (a *ManagementAuthority) MutateGrant(ctx context.Context, actorID string, actorCaps map[string]int64, mutation ManagementMutation) (ManagementMutationResult, error) {
	if !a.ready() || !ValidPathSegment(actorID) || !ValidPathSegment(mutation.OperationID) ||
		!ValidPathSegment(mutation.TargetID) || !managementScopeAllowed(mutation.Scope) ||
		(mutation.Action != "grant" && mutation.Action != "remove") {
		return ManagementMutationResult{}, ErrManagementInvalid
	}
	fingerprint := managementMutationFingerprint(mutation)
	var result ManagementMutationResult
	err := a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		opRef := a.operationRef(mutation.OperationID)
		opDoc, err := tx.Get(opRef)
		if err == nil {
			var prior managementOperation
			if opDoc.DataTo(&prior) != nil || prior.SchemaVersion != managementSchemaVersion || prior.Fingerprint != fingerprint {
				return ErrManagementConflict
			}
			result = prior.Result
			return nil
		}
		if status.Code(err) != codes.NotFound {
			return ErrManagementUnavailable
		}
		epoch, err := a.authorityEpochInTransaction(ctx, tx)
		if err != nil {
			return err
		}

		actorUserDoc, err := tx.Get(scopedfirestore.Collection(a.fs, "users").Doc(actorID))
		if err != nil {
			return ErrManagementDenied
		}
		var actorUser UserRecord
		if actorUserDoc.DataTo(&actorUser) != nil || !actorUser.Active() {
			return ErrManagementDenied
		}
		actorPrincipal := managementPrincipal{SchemaVersion: managementSchemaVersion, ScopeStates: map[string]ManagementScopeState{}}
		actorPrincipalDoc, actorPrincipalErr := tx.Get(a.principalRef(actorID))
		if actorPrincipalErr == nil {
			actorPrincipal, err = decodeManagementPrincipal(actorPrincipalDoc)
			if err != nil {
				return ErrManagementDenied
			}
		} else if status.Code(actorPrincipalErr) != codes.NotFound {
			return ErrManagementUnavailable
		}
		if !a.hasCurrentManagementCap(actorPrincipal, &actorUser, actorCaps, ManagementScopeAuthAdmin, epoch) {
			return ErrManagementDenied
		}

		targetRef := a.principalRef(mutation.TargetID)
		targetUserDoc, err := tx.Get(scopedfirestore.Collection(a.fs, "users").Doc(mutation.TargetID))
		if err != nil {
			return ErrManagementTarget
		}
		var targetUser UserRecord
		if targetUserDoc.DataTo(&targetUser) != nil || !targetUser.Active() {
			return ErrManagementTarget
		}
		targetPrincipalDoc, err := tx.Get(targetRef)
		var targetPrincipal managementPrincipal
		if err == nil {
			targetPrincipal, err = decodeManagementPrincipal(targetPrincipalDoc)
			if err != nil {
				return ErrManagementUnavailable
			}
		} else if status.Code(err) == codes.NotFound {
			targetPrincipal = managementPrincipal{SchemaVersion: managementSchemaVersion, ScopeStates: map[string]ManagementScopeState{}}
		} else {
			return ErrManagementUnavailable
		}

		lockRef := a.lockRef(mutation.Scope)
		lockDoc, lockErr := tx.Get(lockRef)
		lockRevision := int64(0)
		if lockErr == nil {
			lockRevision, err = readManagementLockRevision(lockDoc)
			if err != nil {
				return ErrManagementUnavailable
			}
		} else if status.Code(lockErr) == codes.NotFound {
			return ErrManagementUnavailable
		} else {
			return ErrManagementUnavailable
		}

		activeMembers, err := a.activeManagementMemberCount(ctx, tx, mutation.Scope)
		if err != nil {
			return err
		}

		current := targetPrincipal.ScopeStates[mutation.Scope]
		requestedState := "granted"
		if mutation.Action == "remove" {
			requestedState = "revoked"
		}
		if current.State == requestedState {
			result = ManagementMutationResult{OperationID: mutation.OperationID, TargetID: mutation.TargetID,
				Scope: mutation.Scope, State: current.State, Revision: current.Revision}
			return tx.Create(opRef, managementOperation{SchemaVersion: managementSchemaVersion, OperationID: mutation.OperationID,
				ActorID: actorID, TargetID: mutation.TargetID, Scope: mutation.Scope, Action: mutation.Action,
				Fingerprint: fingerprint, Result: result, CreatedAt: time.Now().UTC()})
		}
		if mutation.Action == "remove" && current.State == "granted" && activeMembers <= 1 {
			return ErrManagementLastAdmin
		}
		if current.Revision == math.MaxInt64 || lockRevision == math.MaxInt64 || targetPrincipal.DocumentRevision == math.MaxInt64 {
			return ErrManagementUnavailable
		}
		nextRevision := current.Revision + 1
		if nextRevision < 1 {
			nextRevision = 1
		}
		targetPrincipal.SchemaVersion = managementSchemaVersion
		if targetPrincipal.ScopeStates == nil {
			targetPrincipal.ScopeStates = map[string]ManagementScopeState{}
		}
		targetPrincipal.ScopeStates[mutation.Scope] = ManagementScopeState{State: requestedState, Revision: nextRevision, Source: "manual", UpdatedAt: time.Now().UTC()}
		targetPrincipal.DocumentRevision++
		targetPrincipal.GrantedScopeIDs = managementGrantedIDs(targetPrincipal.ScopeStates)
		result = ManagementMutationResult{OperationID: mutation.OperationID, TargetID: mutation.TargetID,
			Scope: mutation.Scope, State: requestedState, Revision: nextRevision}
		if err := tx.Set(targetRef, targetPrincipal); err != nil {
			return err
		}
		if err := tx.Update(lockRef, []firestore.Update{{Path: "revision", Value: lockRevision + 1}, {Path: "updated_at", Value: time.Now().UTC()}}); err != nil {
			return err
		}
		return tx.Create(opRef, managementOperation{SchemaVersion: managementSchemaVersion, OperationID: mutation.OperationID,
			ActorID: actorID, TargetID: mutation.TargetID, Scope: mutation.Scope, Action: mutation.Action,
			Fingerprint: fingerprint, Result: result, CreatedAt: time.Now().UTC()})
	})
	if err != nil {
		// A client can lose the transaction response after Firestore commits.
		// Read the stable operation ID before asking callers to decide whether
		// they need to retry; never blindly repeat a grant/remove mutation.
		doc, readErr := a.operationRef(mutation.OperationID).Get(ctx)
		if readErr == nil {
			var prior managementOperation
			if doc.DataTo(&prior) != nil || prior.SchemaVersion != managementSchemaVersion {
				return ManagementMutationResult{}, ErrManagementUnavailable
			}
			if prior.Fingerprint != fingerprint {
				return ManagementMutationResult{}, ErrManagementConflict
			}
			return prior.Result, nil
		}
		if status.Code(readErr) != codes.NotFound {
			return ManagementMutationResult{}, ErrManagementUnavailable
		}
		if target, targetErr := GetUser(ctx, a.fs, mutation.TargetID); status.Code(targetErr) == codes.NotFound || (targetErr == nil && !target.Active()) {
			return ManagementMutationResult{}, ErrManagementTarget
		}
		return ManagementMutationResult{}, err
	}
	return result, nil
}

func (a *ManagementAuthority) ReadOperation(ctx context.Context, operationID string) (ManagementMutationResult, error) {
	if !a.ready() || !ValidPathSegment(operationID) {
		return ManagementMutationResult{}, ErrManagementInvalid
	}
	doc, err := a.operationRef(operationID).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return ManagementMutationResult{}, ErrManagementOperationNotFound
	}
	if err != nil {
		return ManagementMutationResult{}, ErrManagementUnavailable
	}
	var operation managementOperation
	if doc.DataTo(&operation) != nil || operation.SchemaVersion != managementSchemaVersion || operation.OperationID != operationID {
		return ManagementMutationResult{}, ErrManagementUnavailable
	}
	return operation.Result, nil
}

// ApplyLegacyMigrationTarget converts one frozen preview row into the new
// scope authority. It rechecks the source account timestamp and state in the
// same transaction as the principal, scope locks, and idempotency record.
func (a *ManagementAuthority) ApplyLegacyMigrationScope(ctx context.Context, runID string, target ManagementMigrationTarget, scope string) (ManagementMigrationResult, error) {
	if !a.ready() || !ValidPathSegment(runID) || !ValidPathSegment(target.UserID) || target.Role != "admin" ||
		target.SourceUpdateTime.IsZero() || !managementScopeAllowed(scope) ||
		!equalStringSet(target.Scopes, []string{ManagementScopeAuthAdmin, ManagementScopeLWCAdmin}) {
		return ManagementMigrationResult{}, ErrManagementInvalid
	}
	operationID, fingerprint := migrationOperationID(runID, target.UserID, scope), migrationFingerprint(runID, target, scope)
	var result ManagementMigrationResult
	err := a.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		epoch, err := a.authorityEpochInTransaction(ctx, tx)
		if err != nil {
			return err
		}
		if epoch.Mode != "legacy-shadow" {
			return ErrManagementConflict
		}
		userRef := scopedfirestore.Collection(a.fs, "users").Doc(target.UserID)
		userDoc, userErr := tx.Get(userRef)
		principalRef := a.principalRef(target.UserID)
		principalDoc, principalErr := tx.Get(principalRef)
		lockRef := a.lockRef(scope)
		lockDoc, lockErr := tx.Get(lockRef)
		opRef := a.operationRef(operationID)
		opDoc, opErr := tx.Get(opRef)
		if opErr == nil {
			var prior managementOperation
			if opDoc.DataTo(&prior) != nil || prior.Fingerprint != fingerprint || prior.MigrationResult == nil {
				return ErrManagementConflict
			}
			result = *prior.MigrationResult
			return nil
		}
		if status.Code(opErr) != codes.NotFound {
			return ErrManagementUnavailable
		}
		principal := managementPrincipal{SchemaVersion: managementSchemaVersion, ScopeStates: map[string]ManagementScopeState{}}
		if principalErr == nil {
			var err error
			principal, err = decodeManagementPrincipal(principalDoc)
			if err != nil {
				return ErrManagementUnavailable
			}
		} else if status.Code(principalErr) != codes.NotFound {
			return ErrManagementUnavailable
		}
		lockRevision := int64(0)
		if lockErr == nil {
			lockRevision, lockErr = readManagementLockRevision(lockDoc)
			if lockErr != nil {
				return ErrManagementUnavailable
			}
		} else if status.Code(lockErr) == codes.NotFound {
			return ErrManagementUnavailable
		} else {
			return ErrManagementUnavailable
		}
		var user UserRecord
		accountDeleted := status.Code(userErr) == codes.NotFound
		accountFound := userErr == nil && userDoc.DataTo(&user) == nil
		if userErr != nil && !accountDeleted {
			return ErrManagementUnavailable
		}
		current := principal.ScopeStates[scope]
		marker, marked := principal.LegacyMigration[scope]
		result = ManagementMigrationResult{RunID: runID, TargetID: target.UserID, Scope: scope}
		switch {
		case marked:
			result.Disposition, result.Reason = "skipped", "already-processed"
			if marker.Disposition == "account-deleted" {
				result.Reason = "account-deleted"
			}
		case accountDeleted:
			result.Disposition, result.Reason = "skipped", "account-deleted"
		case !accountFound || user.Role != target.Role || user.Status != target.Status || !user.Active():
			result.Disposition, result.Reason = "skipped", "source-account-ineligible"
		case !userDoc.UpdateTime.Equal(target.SourceUpdateTime):
			result.Disposition, result.Reason = "skipped", "source-account-changed"
		case current.State == "granted":
			result.Disposition, result.Reason, result.Revision = "skipped", "already-granted", current.Revision
		case current.State == "revoked":
			result.Disposition, result.Reason, result.Revision = "skipped", "revoked-tombstone", current.Revision
		default:
			if current.Revision == math.MaxInt64 || lockRevision == math.MaxInt64 || principal.DocumentRevision == math.MaxInt64 {
				return ErrManagementUnavailable
			}
			current = ManagementScopeState{State: "granted", Revision: current.Revision + 1, Source: "migration", UpdatedAt: time.Now().UTC()}
			principal.ScopeStates[scope] = current
			result.Disposition, result.Revision = "applied", current.Revision
		}
		if !marked {
			if principal.LegacyMigration == nil {
				principal.LegacyMigration = map[string]ManagementMigrationMarker{}
			}
			principal.LegacyMigration[scope] = ManagementMigrationMarker{RunID: runID, SourceTimestamp: target.SourceUpdateTime,
				Disposition: result.Disposition, Reason: result.Reason}
			principal.DocumentRevision++
			principal.GrantedScopeIDs = managementGrantedIDs(principal.ScopeStates)
			if err := tx.Set(principalRef, principal); err != nil {
				return err
			}
		}
		if result.Disposition == "applied" {
			if err := tx.Update(lockRef, []firestore.Update{{Path: "revision", Value: lockRevision + 1}, {Path: "updated_at", Value: time.Now().UTC()}}); err != nil {
				return err
			}
		}
		resultCopy := result
		return tx.Create(opRef, managementOperation{SchemaVersion: managementSchemaVersion, OperationID: operationID,
			TargetID: target.UserID, Scope: scope, Action: "migration", Fingerprint: fingerprint,
			MigrationResult: &resultCopy, CreatedAt: time.Now().UTC()})
	})
	if err != nil {
		return ManagementMigrationResult{}, err
	}
	return result, nil
}

// PreviewLegacyAdminMigration lists current role=admin source accounts and
// captures their exact Firestore update times. It is read-only; applying a
// manifest later rechecks each captured value transactionally.
func (a *ManagementAuthority) PreviewLegacyAdminMigration(ctx context.Context) ([]ManagementMigrationTarget, error) {
	if !a.ready() {
		return nil, ErrManagementUnavailable
	}
	docs, err := scopedfirestore.Collection(a.fs, "users").Where("role", "==", "admin").Documents(ctx).GetAll()
	if err != nil {
		return nil, ErrManagementUnavailable
	}
	targets := make([]ManagementMigrationTarget, 0, len(docs))
	for _, doc := range docs {
		var user UserRecord
		if doc.DataTo(&user) != nil {
			return nil, ErrManagementUnavailable
		}
		targets = append(targets, ManagementMigrationTarget{UserID: doc.Ref.ID, Role: user.Role, Status: user.Status,
			SourceUpdateTime: doc.UpdateTime, Scopes: []string{ManagementScopeAuthAdmin, ManagementScopeLWCAdmin}})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].UserID < targets[j].UserID })
	return targets, nil
}

func (a *ManagementAuthority) ReadLegacyMigrationScope(ctx context.Context, runID, userID, scope string) (ManagementMigrationResult, error) {
	if !a.ready() || !ValidPathSegment(runID) || !ValidPathSegment(userID) || !managementScopeAllowed(scope) {
		return ManagementMigrationResult{}, ErrManagementInvalid
	}
	doc, err := a.operationRef(migrationOperationID(runID, userID, scope)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return ManagementMigrationResult{}, ErrManagementOperationNotFound
	}
	if err != nil {
		return ManagementMigrationResult{}, ErrManagementUnavailable
	}
	var operation managementOperation
	if doc.DataTo(&operation) != nil || operation.Fingerprint == "" || operation.MigrationResult == nil ||
		operation.MigrationResult.RunID != runID || operation.MigrationResult.TargetID != userID || operation.MigrationResult.Scope != scope {
		return ManagementMigrationResult{}, ErrManagementUnavailable
	}
	return *operation.MigrationResult, nil
}

func migrationOperationID(runID, userID, scope string) string {
	return runID + "_" + userID + "_" + strings.ReplaceAll(scope, ":", "_")
}

func migrationFingerprint(runID string, target ManagementMigrationTarget, scope string) string {
	scopes := append([]string(nil), target.Scopes...)
	sort.Strings(scopes)
	data := strings.Join([]string{runID, target.UserID, target.Role, target.Status, target.SourceUpdateTime.UTC().Format(time.RFC3339Nano), scope, strings.Join(scopes, ",")}, "\x00")
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func equalStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

func (a *ManagementAuthority) hasCurrentManagementCap(principal managementPrincipal, user *UserRecord, caps map[string]int64, scope string, epoch ManagementAuthorityEpoch) bool {
	state, ok := principal.ScopeStates[scope]
	if ok && state.State == "granted" && state.Revision > 0 && caps[scope] == state.Revision {
		return true
	}
	return epoch.Mode == "legacy-shadow" && user != nil && user.Role == "admin" &&
		!managementPrincipalHasExplicitAuthority(principal) && caps[scope] == epoch.Epoch && managementScopeAllowed(scope)
}

func managementPrincipalHasExplicitAuthority(principal managementPrincipal) bool {
	return len(principal.ScopeStates) != 0 || len(principal.LegacyMigration) != 0
}

func shadowManagementScopes(caps map[string]int64, epoch int64) []string {
	scopes := make([]string, 0, 2)
	for _, scope := range []string{ManagementScopeAuthAdmin, ManagementScopeLWCAdmin} {
		if caps[scope] == epoch {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

func managementScopeAllowed(scope string) bool {
	return scope == ManagementScopeAuthAdmin || scope == ManagementScopeLWCAdmin
}

func managementGrantedIDs(states map[string]ManagementScopeState) []string {
	ids := make([]string, 0, len(states))
	for scope, state := range states {
		if managementScopeAllowed(scope) && state.State == "granted" {
			ids = append(ids, scope)
		}
	}
	sort.Strings(ids)
	return ids
}

func decodeManagementPrincipal(doc *firestore.DocumentSnapshot) (managementPrincipal, error) {
	var principal managementPrincipal
	if doc == nil || doc.DataTo(&principal) != nil || principal.SchemaVersion != managementSchemaVersion || principal.ScopeStates == nil {
		return managementPrincipal{}, ErrManagementUnavailable
	}
	for scope, state := range principal.ScopeStates {
		if !managementScopeAllowed(scope) || (state.State != "granted" && state.State != "revoked") || state.Revision < 1 || state.UpdatedAt.IsZero() {
			return managementPrincipal{}, ErrManagementUnavailable
		}
	}
	for scope, marker := range principal.LegacyMigration {
		if !managementScopeAllowed(scope) || !ValidPathSegment(marker.RunID) || marker.SourceTimestamp.IsZero() ||
			(marker.Disposition != "applied" && marker.Disposition != "skipped" && marker.Disposition != "account-deleted") {
			return managementPrincipal{}, ErrManagementUnavailable
		}
	}
	expected := managementGrantedIDs(principal.ScopeStates)
	actual := append([]string(nil), principal.GrantedScopeIDs...)
	sort.Strings(actual)
	if strings.Join(actual, "\x00") != strings.Join(expected, "\x00") {
		return managementPrincipal{}, ErrManagementUnavailable
	}
	return principal, nil
}

func readManagementLockRevision(doc *firestore.DocumentSnapshot) (int64, error) {
	data := doc.Data()
	version, ok := data["schema_version"].(int64)
	if !ok || version != 1 {
		return 0, ErrManagementUnavailable
	}
	revision, ok := data["revision"].(int64)
	if !ok || revision < 0 {
		return 0, ErrManagementUnavailable
	}
	return revision, nil
}

func managementMutationFingerprint(mutation ManagementMutation) string {
	data := mutation.OperationID + "\x00" + mutation.TargetID + "\x00" + mutation.Scope + "\x00" + mutation.Action
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func managementMutationErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrManagementDenied):
		return http.StatusForbidden
	case errors.Is(err, ErrManagementInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrManagementConflict), errors.Is(err, ErrManagementLastAdmin):
		return http.StatusConflict
	case errors.Is(err, ErrManagementTarget):
		return http.StatusConflict
	case errors.Is(err, ErrManagementOperationNotFound):
		return http.StatusNotFound
	default:
		return http.StatusServiceUnavailable
	}
}

func managementMutationErrorMessage(err error) string {
	if errors.Is(err, ErrManagementDenied) || errors.Is(err, ErrManagementInvalid) || errors.Is(err, ErrManagementConflict) ||
		errors.Is(err, ErrManagementLastAdmin) || errors.Is(err, ErrManagementTarget) || errors.Is(err, ErrManagementOperationNotFound) {
		return err.Error()
	}
	return fmt.Sprint(ErrManagementUnavailable)
}
