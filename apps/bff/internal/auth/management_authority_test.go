package auth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func seedCentralManagementAuthority(t *testing.T, ctx context.Context, authority *ManagementAuthority, runID string) {
	t.Helper()
	if _, err := authority.InitializeLegacyShadow(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.authorityRef().Set(ctx, ManagementAuthorityEpoch{
		SchemaVersion: 1, Mode: "central", Epoch: 2, MigrationRunID: runID, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

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
	seedCentralManagementAuthority(t, ctx, authority, "test-grant-run")
	actorCaps := map[string]int64{ManagementScopeAuthAdmin: 3}
	mutation := ManagementMutation{OperationID: "grant-1", TargetID: "target", Scope: ManagementScopeLWCAdmin, Action: "grant"}
	result, err := authority.MutateGrant(ctx, "actor", actorCaps, 1, mutation)
	if err != nil || result.State != "granted" || result.Revision != 1 {
		t.Fatalf("grant result=%+v err=%v", result, err)
	}
	if repeated, err := authority.MutateGrant(ctx, "actor", actorCaps, 1, mutation); err != nil || repeated != result {
		t.Fatalf("idempotent replay=%+v err=%v, want %+v", repeated, err, result)
	}
	if _, err := authority.MutateGrant(ctx, "actor", actorCaps, 1, ManagementMutation{OperationID: "grant-1", TargetID: "actor", Scope: ManagementScopeLWCAdmin, Action: "remove"}); !errors.Is(err, ErrManagementConflict) {
		t.Fatalf("operation ID conflict err=%v", err)
	}
	if got, err := authority.EffectiveScopes(ctx, "target", map[string]int64{ManagementScopeLWCAdmin: result.Revision}); err != nil || len(got) != 1 || got[0] != ManagementScopeLWCAdmin {
		t.Fatalf("fresh target capability=%v err=%v", got, err)
	}
	removed, err := authority.MutateGrant(ctx, "actor", actorCaps, 1, ManagementMutation{OperationID: "remove-1", TargetID: "target", Scope: ManagementScopeLWCAdmin, Action: "remove"})
	if err != nil || removed.State != "revoked" || removed.Revision != 2 {
		t.Fatalf("remove result=%+v err=%v", removed, err)
	}
	if got, err := authority.EffectiveScopes(ctx, "target", map[string]int64{ManagementScopeLWCAdmin: result.Revision}); err != nil || len(got) != 0 {
		t.Fatalf("removed capability remains effective: %v err=%v", got, err)
	}
	regranted, err := authority.MutateGrant(ctx, "actor", actorCaps, 1, ManagementMutation{OperationID: "grant-2", TargetID: "target", Scope: ManagementScopeLWCAdmin, Action: "grant"})
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
	seedCentralManagementAuthority(t, ctx, authority, "test-last-admin-run")
	caps := map[string]int64{ManagementScopeAuthAdmin: 1}
	if _, err := authority.MutateGrant(ctx, "actor", caps, 1, ManagementMutation{OperationID: "remove-last", TargetID: "actor", Scope: ManagementScopeAuthAdmin, Action: "remove"}); !errors.Is(err, ErrManagementLastAdmin) {
		t.Fatalf("last auth:admin removal err=%v", err)
	}
	if _, err := authority.MutateGrant(ctx, "actor", caps, 1, ManagementMutation{OperationID: "unknown", TargetID: "actor", Scope: "lwc:delete-all", Action: "grant"}); !errors.Is(err, ErrManagementInvalid) {
		t.Fatalf("unknown management scope err=%v", err)
	}
	if _, err := authority.MutateGrant(ctx, "actor", nil, 1, ManagementMutation{OperationID: "no-cap", TargetID: "actor", Scope: ManagementScopeLWCAdmin, Action: "grant"}); !errors.Is(err, ErrManagementDenied) {
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
	// A prior explicit revocation is a tombstone and cannot be overwritten by migration.
	principal := managementPrincipal{SchemaVersion: managementSchemaVersion, ScopeStates: map[string]ManagementScopeState{
		ManagementScopeAuthAdmin: {State: "revoked", Revision: 2, Source: "manual", UpdatedAt: time.Now().UTC()},
		ManagementScopeLWCAdmin:  {State: "granted", Revision: 1, Source: "migration", UpdatedAt: time.Now().UTC()},
	}}
	principal.GrantedScopeIDs = managementGrantedIDs(principal.ScopeStates)
	if _, err := authority.principalRef(target.UserID).Set(ctx, principal); err != nil {
		t.Fatal(err)
	}
	// A fresh preview plus a new run must respect the per-scope removal tombstone.
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
	seedCentralManagementAuthority(t, ctx, authority, "concurrent-central")
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
			_, err := authority.MutateGrant(ctx, actor, map[string]int64{ManagementScopeAuthAdmin: 1}, 1, ManagementMutation{
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
		_, err := authority.MutateGrant(ctx, remainingActor, actorCaps, 1, ManagementMutation{
			OperationID: "grant-target-" + target, TargetID: target, Scope: ManagementScopeLWCAdmin, Action: "grant",
		})
		grantResult <- err
	}()
	go func() {
		<-start
		err := authority.UpdateAccountStatus(ctx, remainingActor, actorCaps, 1, target, AccountSuspended)
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

func TestLegacyShadowKeepsImplicitScopesPerScopeAndBlocksDailyGrantRemove(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	environment := "shadow-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	actorID, targetID := "shadow-admin-"+environment, "shadow-target-"+environment
	for id, role := range map[string]string{actorID: "admin", targetID: "member"} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: role, Status: AccountActive, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	authority := NewManagementAuthority(fs, environment)
	epoch, err := authority.InitializeLegacyShadow(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	caps, err := authority.GrantedScopeVersions(ctx, actorID)
	if err != nil || caps[ManagementScopeAuthAdmin] != epoch.Epoch || caps[ManagementScopeLWCAdmin] != epoch.Epoch {
		t.Fatalf("legacy shadow caps=%v err=%v", caps, err)
	}
	for _, action := range []string{"grant", "remove"} {
		_, err := authority.MutateGrant(ctx, actorID, caps, 1, ManagementMutation{
			OperationID: "shadow-" + action, TargetID: targetID, Scope: ManagementScopeAuthAdmin, Action: action,
		})
		if !errors.Is(err, ErrManagementConflict) {
			t.Fatalf("shadow %s err=%v, want central-only rejection", action, err)
		}
	}
	if states, err := authority.ScopeStates(ctx, targetID); err != nil || len(states) != 0 {
		t.Fatalf("shadow daily grant wrote target state: %+v err=%v", states, err)
	}
	if scopes, err := authority.EffectiveScopes(ctx, actorID, caps); err != nil || len(scopes) != 2 {
		t.Fatalf("shadow admin lost effective scopes after denied requests: %v err=%v", scopes, err)
	}
}

func TestLegacyShadowLastAdminGuardsCountPartialScopeMembers(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	environment := "partial-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	actorID, targetID := "partial-operator-"+environment, "partial-legacy-"+environment
	for id, role := range map[string]string{actorID: "member", targetID: "admin"} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: role, Status: AccountActive, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	authority := NewManagementAuthority(fs, environment)
	if _, err := authority.InitializeLegacyShadow(ctx, ""); err != nil {
		t.Fatal(err)
	}
	seedManagementPrincipal(t, fs, environment, actorID, map[string]ManagementScopeState{
		ManagementScopeAuthAdmin: {State: "granted", Revision: 1, Source: "migration", UpdatedAt: time.Now().UTC()},
	})
	actorCaps := map[string]int64{ManagementScopeAuthAdmin: 1}
	if err := authority.UpdateAccountStatus(ctx, actorID, actorCaps, 1, targetID, AccountSuspended); !errors.Is(err, ErrManagementLastAdmin) {
		t.Fatalf("suspend last implicit lwc:admin err=%v", err)
	}
	if err := authority.DeleteAccount(ctx, actorID, actorCaps, 1, targetID); !errors.Is(err, ErrManagementLastAdmin) {
		t.Fatalf("delete last implicit lwc:admin err=%v", err)
	}
	if user, err := GetUser(ctx, fs, targetID); err != nil || !user.Active() {
		t.Fatalf("last legacy admin was modified: user=%+v err=%v", user, err)
	}
}

func TestCentralCutoverRequiresAnActiveMemberForEachScope(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	environment := "empty-cutover-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	authority := NewManagementAuthority(fs, environment)
	if _, err := authority.InitializeLegacyShadow(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CutoverToCentral(ctx, "empty-run", "", []ManagementMigrationTarget{}); !errors.Is(err, ErrManagementLastAdmin) {
		t.Fatalf("empty management authority cut over to central: %v", err)
	}
	epoch, err := authority.AuthorityEpoch(ctx)
	if err != nil || epoch.Mode != "legacy-shadow" {
		t.Fatalf("empty cutover changed epoch: %+v err=%v", epoch, err)
	}
}

func TestStaleMigrationSnapshotIsRetryableButIneligibleTombstoneIsTerminal(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	environment := "stale-migration-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	staleID, inactiveID := "stale-"+environment, "inactive-"+environment
	for _, id := range []string{staleID, inactiveID} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: "admin", Status: AccountActive, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	authority := NewManagementAuthority(fs, environment)
	if _, err := authority.InitializeLegacyShadow(ctx, ""); err != nil {
		t.Fatal(err)
	}
	preview, err := authority.PreviewLegacyAdminMigration(ctx)
	if err != nil || len(preview) != 2 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	byID := map[string]ManagementMigrationTarget{}
	for _, target := range preview {
		byID[target.UserID] = target
	}
	if _, err := fs.Collection("users").Doc(staleID).Update(ctx, []firestore.Update{{Path: "name", Value: "changed-after-preview"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Collection("users").Doc(inactiveID).Update(ctx, []firestore.Update{{Path: "status", Value: AccountSuspended}}); err != nil {
		t.Fatal(err)
	}
	for _, targetID := range []string{staleID, inactiveID} {
		for _, scope := range []string{ManagementScopeAuthAdmin, ManagementScopeLWCAdmin} {
			result, err := authority.ApplyLegacyMigrationScope(ctx, "first-run", byID[targetID], scope)
			if err != nil || result.Disposition != "skipped" {
				t.Fatalf("first apply user=%s scope=%s result=%+v err=%v", targetID, scope, result, err)
			}
		}
	}
	stalePrincipal, err := authority.principalRef(staleID).Get(ctx)
	if err == nil {
		var stale managementPrincipal
		if err := stalePrincipal.DataTo(&stale); err != nil || len(stale.LegacyMigration) != 0 || len(stale.ScopeStates) != 0 {
			t.Fatalf("stale preview created permanent authority state: %+v err=%v", stale, err)
		}
	} else if status.Code(err) != codes.NotFound {
		t.Fatalf("read stale principal: %v", err)
	}
	shadowCaps, err := authority.GrantedScopeVersions(ctx, staleID)
	if err != nil || len(shadowCaps) != 2 {
		t.Fatalf("stale preview removed shadow eligibility: caps=%v err=%v", shadowCaps, err)
	}
	freshPreview, err := authority.PreviewLegacyAdminMigration(ctx)
	if err != nil || len(freshPreview) != 2 {
		t.Fatalf("fresh preview=%+v err=%v", freshPreview, err)
	}
	for _, target := range freshPreview {
		if target.UserID != staleID {
			continue
		}
		for _, scope := range target.Scopes {
			result, err := authority.ApplyLegacyMigrationScope(ctx, "fresh-run", target, scope)
			if err != nil || result.Disposition != "applied" {
				t.Fatalf("fresh apply scope=%s result=%+v err=%v", scope, result, err)
			}
		}
	}
	if caps, err := authority.GrantedScopeVersions(ctx, inactiveID); err == nil && len(caps) != 0 {
		t.Fatalf("ineligible target unexpectedly retains shadow caps: %v", caps)
	}
	if _, err := fs.Collection("users").Doc(inactiveID).Update(ctx, []firestore.Update{{Path: "status", Value: AccountActive}}); err != nil {
		t.Fatal(err)
	}
	restoredPreview, err := authority.PreviewLegacyAdminMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range restoredPreview {
		if target.UserID != inactiveID {
			continue
		}
		result, err := authority.ApplyLegacyMigrationScope(ctx, "restored-run", target, ManagementScopeAuthAdmin)
		if err != nil || result.Disposition != "skipped" || result.Reason != "already-processed" {
			t.Fatalf("restored ineligible target result=%+v err=%v", result, err)
		}
	}
	if caps, err := authority.GrantedScopeVersions(ctx, inactiveID); err != nil || len(caps) != 0 {
		t.Fatalf("restored ineligible target became shadow eligible: %v err=%v", caps, err)
	}
	for _, target := range restoredPreview {
		for _, scope := range target.Scopes {
			if _, err := authority.ApplyLegacyMigrationScope(ctx, "cutover-run", target, scope); err != nil {
				t.Fatalf("record final readback target=%s scope=%s: %v", target.UserID, scope, err)
			}
		}
	}
	epoch, err := authority.CutoverToCentral(ctx, "cutover-run", "", restoredPreview)
	if err != nil || epoch.Mode != "central" {
		t.Fatalf("fresh reviewed manifest did not cut over with active admins: epoch=%+v err=%v", epoch, err)
	}
}

func TestManagementMutationRejectsActorAuthVersionAfterSuspendRestore(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	environment := "actor-version-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	actorID, otherID, targetID := "version-actor-"+environment, "version-other-"+environment, "version-target-"+environment
	for id := range map[string]bool{actorID: true, otherID: true, targetID: true} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, UserRecord{Role: "member", Status: AccountActive, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	authority := NewManagementAuthority(fs, environment)
	seedCentralManagementAuthority(t, ctx, authority, "actor-version-run")
	for _, id := range []string{actorID, otherID} {
		seedManagementPrincipal(t, fs, environment, id, map[string]ManagementScopeState{
			ManagementScopeAuthAdmin: {State: "granted", Revision: 1, Source: "test", UpdatedAt: time.Now().UTC()},
		})
	}
	caps := map[string]int64{ManagementScopeAuthAdmin: 1}
	if err := authority.UpdateAccountStatus(ctx, otherID, caps, 1, actorID, AccountSuspended); err != nil {
		t.Fatalf("suspend actor: %v", err)
	}
	if err := authority.UpdateAccountStatus(ctx, otherID, caps, 1, actorID, AccountActive); err != nil {
		t.Fatalf("restore actor: %v", err)
	}
	mutation := ManagementMutation{OperationID: "stale-actor-mutation", TargetID: targetID, Scope: ManagementScopeLWCAdmin, Action: "grant"}
	if _, err := authority.MutateGrant(ctx, actorID, caps, 1, mutation); !errors.Is(err, ErrManagementDenied) {
		t.Fatalf("pre-suspension auth_version committed a mutation after restore: %v", err)
	}
	if _, err := authority.operationRef(mutation.OperationID).Get(ctx); err == nil {
		t.Fatal("stale actor mutation created an operation record")
	}
	if _, err := authority.MutateGrant(ctx, actorID, caps, 2, mutation); err != nil {
		t.Fatalf("fresh actor auth_version was rejected: %v", err)
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
