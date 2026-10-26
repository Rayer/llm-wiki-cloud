package auth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
)

func TestManagementAuthorityGrantRemoveAndRegrantInvalidatesOldCaps(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	for id, role := range map[string]string{"actor": "admin", "target": "user"} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: role, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	seedManagementPrincipal(t, fs, "dev", "actor", map[string]ManagementScopeState{
		ManagementScopeAuthAdmin: {State: "granted", Revision: 3, Source: "migration", UpdatedAt: now},
		ManagementScopeLWCAdmin:  {State: "granted", Revision: 2, Source: "migration", UpdatedAt: now},
	})
	authority := NewManagementAuthority(fs, "dev")
	actorCaps := map[string]int64{ManagementScopeAuthAdmin: 3}
	mutation := ManagementMutation{OperationID: "grant-1", TargetID: "target", Scope: ManagementScopeLWCAdmin, Action: "grant"}
	result, err := authority.MutateGrant(ctx, "actor", actorCaps, mutation)
	if err != nil || result.State != "granted" || result.Revision != 1 {
		t.Fatalf("grant result=%+v err=%v", result, err)
	}
	if repeated, err := authority.MutateGrant(ctx, "actor", actorCaps, mutation); err != nil || repeated != result {
		t.Fatalf("idempotent replay=%+v err=%v, want %+v", repeated, err, result)
	}
	if _, err := authority.MutateGrant(ctx, "actor", actorCaps, ManagementMutation{OperationID: "grant-1", TargetID: "actor", Scope: ManagementScopeLWCAdmin, Action: "remove"}); !errors.Is(err, ErrManagementConflict) {
		t.Fatalf("operation ID conflict err=%v", err)
	}
	if got, err := authority.EffectiveScopes(ctx, "target", map[string]int64{ManagementScopeLWCAdmin: result.Revision}); err != nil || len(got) != 1 || got[0] != ManagementScopeLWCAdmin {
		t.Fatalf("fresh target capability=%v err=%v", got, err)
	}
	removed, err := authority.MutateGrant(ctx, "actor", actorCaps, ManagementMutation{OperationID: "remove-1", TargetID: "target", Scope: ManagementScopeLWCAdmin, Action: "remove"})
	if err != nil || removed.State != "revoked" || removed.Revision != 2 {
		t.Fatalf("remove result=%+v err=%v", removed, err)
	}
	if got, err := authority.EffectiveScopes(ctx, "target", map[string]int64{ManagementScopeLWCAdmin: result.Revision}); err != nil || len(got) != 0 {
		t.Fatalf("removed capability remains effective: %v err=%v", got, err)
	}
	regranted, err := authority.MutateGrant(ctx, "actor", actorCaps, ManagementMutation{OperationID: "grant-2", TargetID: "target", Scope: ManagementScopeLWCAdmin, Action: "grant"})
	if err != nil || regranted.Revision != 3 {
		t.Fatalf("regrant result=%+v err=%v", regranted, err)
	}
	if got, err := authority.EffectiveScopes(ctx, "target", map[string]int64{ManagementScopeLWCAdmin: result.Revision}); err != nil || len(got) != 0 {
		t.Fatalf("old capability revived after regrant: %v err=%v", got, err)
	}
	if got, err := authority.EffectiveScopes(ctx, "target", map[string]int64{ManagementScopeLWCAdmin: regranted.Revision}); err != nil || len(got) != 1 || got[0] != ManagementScopeLWCAdmin {
		t.Fatalf("new capability not effective: %v err=%v", got, err)
	}
}

func TestManagementAuthorityRejectsLastActiveAdminRemovalAndUnauthorisedScope(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	if _, err := fs.Collection("users").Doc("actor").Set(ctx, UserRecord{Role: "admin", AuthVersion: 1}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seedManagementPrincipal(t, fs, "dev", "actor", map[string]ManagementScopeState{
		ManagementScopeAuthAdmin: {State: "granted", Revision: 1, Source: "migration", UpdatedAt: now},
	})
	authority := NewManagementAuthority(fs, "dev")
	caps := map[string]int64{ManagementScopeAuthAdmin: 1}
	if _, err := authority.MutateGrant(ctx, "actor", caps, ManagementMutation{OperationID: "remove-last", TargetID: "actor", Scope: ManagementScopeAuthAdmin, Action: "remove"}); !errors.Is(err, ErrManagementLastAdmin) {
		t.Fatalf("last auth:admin removal err=%v", err)
	}
	if _, err := authority.MutateGrant(ctx, "actor", caps, ManagementMutation{OperationID: "unknown", TargetID: "actor", Scope: "lwc:delete-all", Action: "grant"}); !errors.Is(err, ErrManagementInvalid) {
		t.Fatalf("unknown management scope err=%v", err)
	}
	if _, err := authority.MutateGrant(ctx, "actor", nil, ManagementMutation{OperationID: "no-cap", TargetID: "actor", Scope: ManagementScopeLWCAdmin, Action: "grant"}); !errors.Is(err, ErrManagementDenied) {
		t.Fatalf("missing auth:admin capability err=%v", err)
	}
}

func TestLegacyAdminMigrationPreviewApplyReadbackAndTombstone(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	if _, err := fs.Collection("users").Doc("legacy-admin").Set(ctx, UserRecord{Role: "admin", Status: AccountActive, AuthVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Collection("users").Doc("operator").Set(ctx, UserRecord{Role: "user", Status: AccountActive, AuthVersion: 1}); err != nil {
		t.Fatal(err)
	}
	seedManagementPrincipal(t, fs, "dev", "operator", map[string]ManagementScopeState{
		ManagementScopeAuthAdmin: {State: "granted", Revision: 3, Source: "operator", UpdatedAt: time.Now().UTC()},
	})
	authority := NewManagementAuthority(fs, "dev")
	targets, err := authority.PreviewLegacyAdminMigration(ctx)
	if err != nil || len(targets) != 1 || targets[0].UserID != "legacy-admin" || len(targets[0].Scopes) != 2 {
		t.Fatalf("preview targets=%+v err=%v", targets, err)
	}
	target := targets[0]
	for _, scope := range target.Scopes {
		result, err := authority.ApplyLegacyMigrationScope(ctx, "migration-run-1", target, scope)
		if err != nil || result.Disposition != "applied" || result.Revision != 1 {
			t.Fatalf("apply %s result=%+v err=%v", scope, result, err)
		}
		readback, err := authority.ReadLegacyMigrationScope(ctx, "migration-run-1", target.UserID, scope)
		if err != nil || readback != result {
			t.Fatalf("readback=%+v err=%v want %+v", readback, err, result)
		}
		if repeated, err := authority.ApplyLegacyMigrationScope(ctx, "migration-run-1", target, scope); err != nil || repeated != result {
			t.Fatalf("idempotent replay=%+v err=%v want %+v", repeated, err, result)
		}
	}
	if _, err := authority.MutateGrant(ctx, "operator", map[string]int64{ManagementScopeAuthAdmin: 3}, ManagementMutation{
		OperationID: "remove-migrated-auth", TargetID: target.UserID, Scope: ManagementScopeAuthAdmin, Action: "remove",
	}); err != nil {
		t.Fatalf("remove migrated capability: %v", err)
	}
	// A fresh preview plus a new run must respect the per-scope removal marker.
	refreshed, err := authority.PreviewLegacyAdminMigration(ctx)
	if err != nil || len(refreshed) != 1 {
		t.Fatalf("second preview=%+v err=%v", refreshed, err)
	}
	result, err := authority.ApplyLegacyMigrationScope(ctx, "migration-run-2", refreshed[0], ManagementScopeAuthAdmin)
	if err != nil || result.Disposition != "skipped" {
		t.Fatalf("tombstoned migration result=%+v err=%v", result, err)
	}
	states, err := authority.ScopeStates(ctx, target.UserID)
	if err != nil || states[ManagementScopeAuthAdmin].State != "revoked" || states[ManagementScopeLWCAdmin].State != "granted" {
		t.Fatalf("scope states after rerun=%+v err=%v", states, err)
	}
}

func TestLegacyAdminMigrationSkipsChangedSourceSnapshot(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	ref := fs.Collection("users").Doc("legacy-admin")
	if _, err := ref.Set(ctx, UserRecord{Role: "admin", Status: AccountActive, AuthVersion: 1}); err != nil {
		t.Fatal(err)
	}
	authority := NewManagementAuthority(fs, "dev")
	targets, err := authority.PreviewLegacyAdminMigration(ctx)
	if err != nil || len(targets) != 1 {
		t.Fatalf("preview=%+v err=%v", targets, err)
	}
	if _, err := ref.Update(ctx, []firestore.Update{{Path: "role", Value: "user"}}); err != nil {
		t.Fatal(err)
	}
	result, err := authority.ApplyLegacyMigrationScope(ctx, "migration-run-stale", targets[0], ManagementScopeAuthAdmin)
	if err != nil || result.Disposition != "skipped" {
		t.Fatalf("stale snapshot result=%+v err=%v", result, err)
	}
	states, err := authority.ScopeStates(ctx, "legacy-admin")
	if err != nil || len(states) != 0 {
		t.Fatalf("stale snapshot mutated scopes=%+v err=%v", states, err)
	}
}

func TestLegacyAdminMigrationCutoverRequiresCompletePreviewAndReadback(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	environment := "cutover-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	configID := "sha256:" + strings.Repeat("a", 64)
	userID := "cutover-admin-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := fs.Collection("users").Doc(userID).Set(ctx, UserRecord{Role: "admin", Status: AccountActive, AuthVersion: 1}); err != nil {
		t.Fatal(err)
	}
	authority := NewManagementAuthority(fs, environment)
	if _, err := authority.InitializeLegacyShadow(ctx, configID); err != nil {
		t.Fatal(err)
	}
	targets, err := authority.PreviewLegacyAdminMigration(ctx)
	if err != nil || len(targets) != 1 || targets[0].UserID != userID {
		t.Fatalf("preview targets=%+v err=%v", targets, err)
	}
	if _, err := authority.CutoverToCentral(ctx, "cutover-run", configID, []ManagementMigrationTarget{}); !errors.Is(err, ErrManagementConflict) {
		t.Fatalf("cutover accepted an incomplete preview manifest: %v", err)
	}
	for _, scope := range targets[0].Scopes {
		if _, err := authority.ApplyLegacyMigrationScope(ctx, "cutover-run", targets[0], scope); err != nil {
			t.Fatalf("apply %s: %v", scope, err)
		}
	}
	if _, err := authority.CutoverToCentral(ctx, "cutover-run", configID, targets); err != nil {
		t.Fatalf("central cutover: %v", err)
	}
	epoch, err := authority.AuthorityEpoch(ctx)
	if err != nil || epoch.Mode != "central" || epoch.Epoch != 2 || epoch.MigrationRunID != "cutover-run" || epoch.ConfigID != configID {
		t.Fatalf("central authority epoch=%+v err=%v", epoch, err)
	}
	caps, err := authority.GrantedScopeVersions(ctx, userID)
	if err != nil || caps[ManagementScopeAuthAdmin] != 1 || caps[ManagementScopeLWCAdmin] != 1 {
		t.Fatalf("central capability versions=%v err=%v", caps, err)
	}
	if _, err := authority.CutoverToCentral(ctx, "cutover-run", configID, targets); err != nil {
		t.Fatalf("idempotent cutover retry: %v", err)
	}
}

func TestManagementAuthoritySerializesConcurrentGrantAndLastAdminRemoval(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	environment := "concurrent-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	authority := NewManagementAuthority(fs, environment)
	if _, err := authority.InitializeLegacyShadow(ctx, ""); err != nil {
		t.Fatal(err)
	}
	actorA, actorB := "operator-a-"+strconv.FormatInt(time.Now().UnixNano(), 10), "operator-b-"+strconv.FormatInt(time.Now().UnixNano(), 10)
	target := "grant-target-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	for id, role := range map[string]string{actorA: "admin", actorB: "admin", target: "member"} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: role, Status: AccountActive, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for _, actor := range []string{actorA, actorB} {
		seedManagementPrincipal(t, fs, environment, actor, map[string]ManagementScopeState{
			ManagementScopeAuthAdmin: {State: "granted", Revision: 1, Source: "operator", UpdatedAt: now},
			ManagementScopeLWCAdmin:  {State: "granted", Revision: 1, Source: "operator", UpdatedAt: now},
		})
	}
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	for _, actor := range []string{actorA, actorB} {
		actor := actor
		go func() {
			<-start
			_, err := authority.MutateGrant(ctx, actor, map[string]int64{ManagementScopeAuthAdmin: 1}, ManagementMutation{
				OperationID: "remove-" + actor, TargetID: actor, Scope: ManagementScopeAuthAdmin, Action: "remove",
			})
			errorsOut <- err
		}()
	}
	close(start)
	successes, lastAdminRejections := 0, 0
	for range 2 {
		err := <-errorsOut
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrManagementLastAdmin):
			lastAdminRejections++
		default:
			t.Fatalf("concurrent last-admin removal error: %v", err)
		}
	}
	if successes != 1 || lastAdminRejections != 1 {
		t.Fatalf("concurrent last-admin outcomes: success=%d last-admin=%d", successes, lastAdminRejections)
	}
	activeAdmins := 0
	for _, actor := range []string{actorA, actorB} {
		states, err := authority.ScopeStates(ctx, actor)
		if err != nil {
			t.Fatal(err)
		}
		if states[ManagementScopeAuthAdmin].State == "granted" {
			activeAdmins++
		}
	}
	if activeAdmins != 1 {
		t.Fatalf("concurrent removal left %d explicit auth admins, want one", activeAdmins)
	}

	remainingActor := actorA
	if states, _ := authority.ScopeStates(ctx, actorA); states[ManagementScopeAuthAdmin].State != "granted" {
		remainingActor = actorB
	}
	actorCaps := map[string]int64{ManagementScopeAuthAdmin: 1}
	grantResult := make(chan error, 1)
	statusResult := make(chan error, 1)
	start = make(chan struct{})
	go func() {
		<-start
		_, err := authority.MutateGrant(ctx, remainingActor, actorCaps, ManagementMutation{
			OperationID: "grant-target-" + target, TargetID: target, Scope: ManagementScopeLWCAdmin, Action: "grant",
		})
		grantResult <- err
	}()
	go func() {
		<-start
		err := authority.UpdateAccountStatus(ctx, remainingActor, actorCaps, target, AccountSuspended)
		statusResult <- err
	}()
	close(start)
	grantError, statusError := <-grantResult, <-statusResult
	if grantError != nil && !errors.Is(grantError, ErrManagementTarget) {
		t.Fatalf("concurrent grant error=%v", grantError)
	}
	if statusError != nil {
		t.Fatalf("concurrent suspension error=%v", statusError)
	}
	user, err := GetUser(ctx, fs, target)
	if err != nil || user.Status != AccountSuspended {
		t.Fatalf("concurrent target status=%+v err=%v", user, err)
	}
	effective, err := authority.EffectiveScopes(ctx, target, map[string]int64{ManagementScopeLWCAdmin: 1})
	if err != nil || len(effective) != 0 {
		t.Fatalf("suspended target retained effective management authority: scopes=%v err=%v", effective, err)
	}
}

func seedManagementPrincipal(t *testing.T, fs *firestore.Client, environment, userID string, states map[string]ManagementScopeState) {
	t.Helper()
	principal := managementPrincipal{SchemaVersion: managementSchemaVersion, ScopeStates: states,
		GrantedScopeIDs: managementGrantedIDs(states), DocumentRevision: 1}
	if _, err := fs.Collection("auth_management").Doc(environment).Collection("principals").Doc(userID).Set(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
}
