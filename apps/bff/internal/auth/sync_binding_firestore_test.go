package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
)

func TestSyncBindingAuthorityCreatesChecksReauthorizesAndRevokes(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, projectID, host = "binding-owner", "project-346", "https://auth.example.test"
	if _, err := fs.Collection("projects").Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{
		"user_id": userID, "project_id": projectID, "name": "Wiki",
	}); err != nil {
		t.Fatal(err)
	}
	authority := NewSyncBindingAuthority(fs, "lwc-346-binding", host)
	created, err := authority.CreateBinding(ctx, userID, projectID, "wiki-1", host)
	if err != nil || created.ID == "" || created.Status != syncBindingStatusActive || created.AuthorizedBy != userID {
		t.Fatalf("created binding=%#v error=%v", created, err)
	}
	if err := authority.CheckSyncBinding(ctx, userID, projectID, created.ID, "wiki-1", host); err != nil {
		t.Fatalf("check active binding = %v", err)
	}
	for _, check := range []struct {
		user, project, binding, wiki, host string
	}{
		{"someone-else", projectID, created.ID, "wiki-1", host},
		{userID, "missing-project", created.ID, "wiki-1", host},
		{userID, projectID, "other-binding", "wiki-1", host},
		{userID, projectID, created.ID, "copied-wiki", host},
		{userID, projectID, created.ID, "wiki-1", "https://other.example.test"},
	} {
		if err := authority.CheckSyncBinding(ctx, check.user, check.project, check.binding, check.wiki, check.host); !errors.Is(err, ErrSyncBindingUnauthorized) {
			t.Fatalf("mismatched binding check error = %v, want unauthorized", err)
		}
	}
	if _, err := authority.CreateBinding(ctx, userID, projectID, "wiki-other", host); !errors.Is(err, ErrSyncBindingAlreadyBound) {
		t.Fatalf("second active binding error = %v, want already bound", err)
	}

	listed, err := authority.ListBindings(ctx, userID)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed bindings=%#v error=%v", listed, err)
	}

	replaced, err := authority.ReauthorizeBinding(ctx, userID, projectID, created.ID, "wiki-2", host)
	if err != nil || replaced.ID == created.ID || replaced.Status != syncBindingStatusActive {
		t.Fatalf("reauthorized binding=%#v error=%v", replaced, err)
	}
	if err := authority.CheckSyncBinding(ctx, userID, projectID, created.ID, "wiki-1", host); !errors.Is(err, ErrSyncBindingUnauthorized) {
		t.Fatalf("replaced binding remains authorized: %v", err)
	}
	if err := authority.RevokeBinding(ctx, userID, projectID, created.ID, "user_revoked"); !errors.Is(err, ErrSyncBindingNotFound) {
		t.Fatalf("revoke replaced binding error = %v, want not found", err)
	}
	if err := authority.RevokeBinding(ctx, userID, projectID, replaced.ID, "user_revoked"); err != nil {
		t.Fatalf("revoke active binding = %v", err)
	}
	if err := authority.CheckSyncBinding(ctx, userID, projectID, replaced.ID, "wiki-2", host); !errors.Is(err, ErrSyncBindingUnauthorized) {
		t.Fatalf("revoked binding remains authorized: %v", err)
	}
	if _, err := authority.CreateBinding(ctx, userID, projectID, "wiki-3", host); !errors.Is(err, ErrSyncBindingReauthorizationRequired) {
		t.Fatalf("recreate revoked binding error = %v, want explicit reauthorization", err)
	}
	reauthorized, err := authority.ReauthorizeBinding(ctx, userID, projectID, replaced.ID, "wiki-3", host)
	if err != nil || reauthorized.ID == replaced.ID {
		t.Fatalf("explicit reauthorization=%#v error=%v", reauthorized, err)
	}

	snapshot, err := fs.Collection("projects").Doc(userID + "_" + projectID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored := fmt.Sprint(snapshot.Data()["sync_binding"])
	if strings.Contains(stored, "access_token") || strings.Contains(stored, "refresh_token") {
		t.Fatalf("binding authority contains credentials: %s", stored)
	}
	audits, err := fs.Collection(syncBindingAuditCollection).Where("project_id", "==", projectID).Documents(ctx).GetAll()
	if err != nil || len(audits) < 4 {
		t.Fatalf("binding audit records=%d error=%v", len(audits), err)
	}
	for _, event := range audits {
		data := event.Data()
		for _, forbidden := range []string{"access_token", "refresh_token", "wiki_id", "host", "user_code"} {
			if _, found := data[forbidden]; found {
				t.Fatalf("audit event contains non-ID secret/detail field %q: %#v", forbidden, data)
			}
		}
	}
}

func TestSyncBindingCreateIsAtomicForConcurrentDifferentWikis(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const userID, projectID, host = "binding-race-owner", "project-race", "https://auth.example.test"
	if _, err := fs.Collection("projects").Doc(userID+"_"+projectID).Set(ctx, map[string]interface{}{"user_id": userID, "project_id": projectID}); err != nil {
		t.Fatal(err)
	}
	authority := NewSyncBindingAuthority(fs, "lwc-346-binding-race", host)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, wikiID := range []string{"wiki-one", "wiki-two"} {
		wikiID := wikiID
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := authority.CreateBinding(ctx, userID, projectID, wikiID, host)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrSyncBindingAlreadyBound) {
			conflicts++
		} else {
			t.Fatalf("concurrent binding error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent binding successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestAdminSyncBindingQueryRevokeReceiptAndReplacementFence(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const ownerID, projectID, actorID, environment, host = "gat21-binding-owner", "gat21-project", "gat21-binding-admin", "gat21-binding-env", "https://auth.example.test"
	if _, err := fs.Collection("projects").Doc(ownerID+"_"+projectID).Set(ctx, map[string]interface{}{
		"user_id": ownerID, "project_id": projectID, "name": "Project",
	}); err != nil {
		t.Fatal(err)
	}
	authority := NewSyncBindingAuthority(fs, environment, host)
	missing, revision, err := authority.AdminBinding(ctx, ownerID+"_"+projectID, ownerID, projectID)
	if err != nil || missing != nil || revision == "" {
		t.Fatalf("missing binding query=(%+v, %q) error=%v", missing, revision, err)
	}
	created, err := authority.CreateBinding(ctx, ownerID, projectID, "gat21-wiki", host)
	if err != nil {
		t.Fatal(err)
	}
	view, revision, err := authority.AdminBinding(ctx, ownerID+"_"+projectID, ownerID, projectID)
	if err != nil || view == nil || view.BindingID != created.ID || view.Revision != revision || view.Status != syncBindingStatusActive {
		t.Fatalf("binding query=(%+v, %q) error=%v", view, revision, err)
	}
	request := AdminSyncBindingRevocationRequest{ActorID: actorID, SubjectID: ownerID, ProjectRecordID: ownerID + "_" + projectID,
		ProjectID: projectID, BindingID: created.ID, OperationID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Environment: environment, Reason: "administrator confirmed", ExpectedRevision: revision}
	authorizeCalls := 0
	authorize := func(context.Context) (time.Time, error) { authorizeCalls++; return time.Now().UTC(), nil }
	receipt, err := authority.AdminRevokeBinding(ctx, request, authorize)
	if err != nil || receipt.Result != "revoked" || receipt.ActorID != actorID || receipt.SubjectID != ownerID ||
		receipt.AffectedCount == nil || *receipt.AffectedCount != 1 {
		t.Fatalf("binding revoke receipt=%+v error=%v", receipt, err)
	}
	auditSnapshot, err := authority.adminRevocationAuditRef(request.OperationID).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var audit syncBindingAuditEvent
	if auditSnapshot.DataTo(&audit) != nil || !validSyncBindingAdminAudit(audit, request.OperationID, environment) ||
		audit.Receipt == nil || !reflect.DeepEqual(*audit.Receipt, receipt) {
		t.Fatalf("canonical audit operation=%+v does not contain receipt=%+v", audit, receipt)
	}
	if err := authority.CheckSyncBinding(ctx, ownerID, projectID, created.ID, "gat21-wiki", host); !errors.Is(err, ErrSyncBindingUnauthorized) {
		t.Fatalf("revoked binding check=%v, want unauthorized", err)
	}
	readback, _, err := authority.AdminBinding(ctx, ownerID+"_"+projectID, ownerID, projectID)
	if err != nil || readback == nil || readback.Status != syncBindingStatusRevoked || readback.RevokedBy != actorID || readback.RevocationReason != request.Reason {
		t.Fatalf("binding readback=%+v error=%v", readback, err)
	}
	if again, err := authority.AdminRevokeBinding(ctx, request, authorize); err != nil || !reflect.DeepEqual(again, receipt) {
		t.Fatalf("idempotent receipt=%+v error=%v; want %+v", again, err, receipt)
	}
	if authorizeCalls != 2 {
		t.Fatalf("authorize calls=%d, want fresh check on transaction retry", authorizeCalls)
	}
	if fetched, err := authority.AdminReadBindingRevocationReceipt(ctx, actorID, request.OperationID); err != nil || !reflect.DeepEqual(fetched, receipt) {
		t.Fatalf("receipt readback=%+v error=%v", fetched, err)
	}
	if _, err := authority.AdminReadBindingRevocationReceipt(ctx, "other-admin", request.OperationID); !errors.Is(err, ErrSyncBindingNotFound) {
		t.Fatalf("other actor receipt lookup error=%v, want not found", err)
	}

	request.OperationID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	request.ExpectedRevision = readback.Revision
	alreadyRevoked, err := authority.AdminRevokeBinding(ctx, request, authorize)
	if err != nil || alreadyRevoked.Result != "already_revoked" || alreadyRevoked.AffectedCount == nil || *alreadyRevoked.AffectedCount != 0 {
		t.Fatalf("already-revoked receipt=%+v error=%v", alreadyRevoked, err)
	}
	_, err = authority.ReauthorizeBinding(ctx, ownerID, projectID, created.ID, "gat21-wiki-new", host)
	if err != nil {
		t.Fatal(err)
	}
	_, replacementRevision, err := authority.AdminBinding(ctx, ownerID+"_"+projectID, ownerID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	request.OperationID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	request.ExpectedRevision = replacementRevision
	if _, err := authority.AdminRevokeBinding(ctx, request, authorize); !errors.Is(err, ErrSyncBindingConflict) {
		t.Fatalf("old binding revoke after replacement error=%v, want target changed", err)
	}
}

func TestAdminSyncBindingCanonicalReceiptRejectsCorruptionAndPaddedTarget(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const host, environment = "https://auth.example.test", "gat21-binding-receipt-integrity"
	authority := NewSyncBindingAuthority(fs, environment, host)
	authorize := func(context.Context) (time.Time, error) { return time.Now().UTC(), nil }
	fields := []struct {
		projectID, operationID, field, value string
	}{
		{"corrupt-environment", "11111111-1111-4111-8111-111111111111", "environment", "other-environment"},
		{"corrupt-subject", "22222222-2222-4222-8222-222222222222", "subject", "other-owner"},
		{"corrupt-result", "33333333-3333-4333-8333-333333333333", "result", "unknown"},
		{"corrupt-target-kind", "44444444-4444-4444-8444-444444444444", "target_kind", "other"},
	}
	for _, test := range fields {
		t.Run(test.field, func(t *testing.T) {
			ownerID, projectID := "gat21-integrity-owner", test.projectID
			if _, err := fs.Collection("projects").Doc(ownerID+"_"+projectID).Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID}); err != nil {
				t.Fatal(err)
			}
			binding, err := authority.CreateBinding(ctx, ownerID, projectID, "wiki-integrity", host)
			if err != nil {
				t.Fatal(err)
			}
			_, revision, err := authority.AdminBinding(ctx, ownerID+"_"+projectID, ownerID, projectID)
			if err != nil {
				t.Fatal(err)
			}
			request := AdminSyncBindingRevocationRequest{ActorID: "gat21-integrity-admin", SubjectID: ownerID,
				ProjectRecordID: ownerID + "_" + projectID, ProjectID: projectID, BindingID: binding.ID,
				OperationID: test.operationID, Environment: environment, Reason: "integrity test", ExpectedRevision: revision}
			if _, err := authority.AdminRevokeBinding(ctx, request, authorize); err != nil {
				t.Fatal(err)
			}
			if _, err := authority.adminRevocationAuditRef(test.operationID).Update(ctx, []cloudfirestore.Update{{Path: "receipt." + test.field, Value: test.value}}); err != nil {
				t.Fatal(err)
			}
			if _, err := authority.AdminReadBindingRevocationReceipt(ctx, request.ActorID, test.operationID); !errors.Is(err, ErrSyncBindingUnavailable) {
				t.Fatalf("corrupt receipt read error=%v, want unavailable", err)
			}
			if _, err := authority.AdminRevokeBinding(ctx, request, authorize); !errors.Is(err, ErrSyncBindingUnavailable) {
				t.Fatalf("corrupt receipt replay error=%v, want unavailable", err)
			}
		})
	}

	ownerID, projectID := "gat21-padding-owner", "padding-project"
	if _, err := fs.Collection("projects").Doc(ownerID+"_"+projectID).Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID}); err != nil {
		t.Fatal(err)
	}
	binding, err := authority.CreateBinding(ctx, ownerID, projectID, "wiki-padding", host)
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := authority.AdminBinding(ctx, ownerID+"_"+projectID, ownerID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	request := AdminSyncBindingRevocationRequest{ActorID: "gat21-padding-admin", SubjectID: ownerID,
		ProjectRecordID: ownerID + "_" + projectID, ProjectID: projectID, BindingID: " " + binding.ID + " ",
		OperationID: "55555555-5555-4555-8555-555555555555", Environment: environment, Reason: "padding test", ExpectedRevision: revision}
	if _, err := authority.AdminRevokeBinding(ctx, request, authorize); !errors.Is(err, ErrSyncBindingInvalid) && !errors.Is(err, ErrSyncBindingConflict) {
		t.Fatalf("padded binding target error=%v, want invalid or literal-target conflict", err)
	}
	current, _, err := authority.AdminBinding(ctx, ownerID+"_"+projectID, ownerID, projectID)
	if err != nil || current == nil || current.BindingID != binding.ID || current.Status != syncBindingStatusActive {
		t.Fatalf("padded target mutated current binding: view=%+v error=%v", current, err)
	}
}

func TestAdminSyncBindingRejectsOperationReuseAndInvalidAuthority(t *testing.T) {
	fs := accountEmulator(t)
	ctx := context.Background()
	const ownerID, projectID, environment, host = "gat21-binding-conflict-owner", "gat21-binding-conflict-project", "gat21-binding-conflict-env", "https://auth.example.test"
	if _, err := fs.Collection("projects").Doc(ownerID+"_"+projectID).Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID}); err != nil {
		t.Fatal(err)
	}
	authority := NewSyncBindingAuthority(fs, environment, host)
	created, err := authority.CreateBinding(ctx, ownerID, projectID, "gat21-wiki", host)
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := authority.AdminBinding(ctx, ownerID+"_"+projectID, ownerID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	request := AdminSyncBindingRevocationRequest{ActorID: "gat21-admin", SubjectID: ownerID, ProjectRecordID: ownerID + "_" + projectID,
		ProjectID: projectID, BindingID: created.ID, OperationID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", Environment: environment,
		Reason: "confirmed", ExpectedRevision: revision}
	authorize := func(context.Context) (time.Time, error) { return time.Now().UTC(), nil }
	if _, err := authority.AdminRevokeBinding(ctx, request, authorize); err != nil {
		t.Fatal(err)
	}
	request.Reason = "changed reason"
	if _, err := authority.AdminRevokeBinding(ctx, request, authorize); !errors.Is(err, ErrSyncBindingOperationConflict) {
		t.Fatalf("operation reuse error=%v, want operation conflict", err)
	}
	request.OperationID = "invalid"
	if _, err := authority.AdminRevokeBinding(ctx, request, authorize); !errors.Is(err, ErrSyncBindingInvalid) {
		t.Fatalf("invalid operation ID error=%v, want invalid", err)
	}
}
