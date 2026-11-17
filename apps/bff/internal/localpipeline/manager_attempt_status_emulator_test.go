package localpipeline

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
)

func TestLocalStatusProjectsExactArchivedQuotaAttemptEmulator(t *testing.T) {
	firestoreEndpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	storageEndpoint := strings.TrimSpace(os.Getenv("STORAGE_EMULATOR_HOST"))
	if firestoreEndpoint == "" || storageEndpoint == "" {
		t.Skip("requires loopback Firestore and Storage emulators")
	}
	host, _, err := net.SplitHostPort(firestoreEndpoint)
	if err != nil || net.ParseIP(strings.Trim(host, "[]")) == nil || !net.ParseIP(strings.Trim(host, "[]")).IsLoopback() {
		t.Fatalf("Firestore emulator must use numeric loopback, got %q", firestoreEndpoint)
	}
	unique := fmt.Sprint(time.Now().UnixNano())
	const project = "llm-wiki-cloud"
	scope := "lwc493-local-archive-" + unique
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", "(default)")
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", scope)
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	storageClient, err := gcs.NewClientWithScope("lwc493-local-archive-unused", scope)
	if err != nil {
		_ = fsClient.Close()
		t.Fatalf("create loopback Storage client: %v", err)
	}
	manager, err := New(Config{Firestore: fsClient.Raw(), Storage: storageClient, Worker: "unused-worker", Scope: scope})
	if err != nil {
		_ = storageClient.Close()
		_ = fsClient.Close()
		t.Fatalf("create local status manager: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := manager.Close(cleanupCtx); err != nil {
			t.Errorf("close local status manager: %v", err)
		}
		for _, id := range []string{"execution-a", "execution-b"} {
			_, _ = scopedfirestore.Collection(fsClient.Raw(), "executions").Doc(id).Delete(cleanupCtx)
		}
		reservationRef := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc("daily-reservation")
		_, _ = reservationRef.Collection("attempts").Doc("attempt-a").Delete(cleanupCtx)
		_, _ = reservationRef.Delete(cleanupCtx)
		_ = storageClient.Close()
		_ = fsClient.Close()
	})
	userID, projectID := "local-owner-"+unique, "local-project-"+unique
	const reservationID, attemptA, attemptB = "daily-reservation", "attempt-a", "attempt-b"
	executions := scopedfirestore.Collection(fsClient.Raw(), "executions")
	for _, execution := range []struct {
		id, attempt, status string
		started             time.Time
	}{{"execution-a", attemptA, "FAILED", time.Now().UTC().Add(-2 * time.Minute)}, {"execution-b", attemptB, "RUNNING", time.Now().UTC()}} {
		if _, err := executions.Doc(execution.id).Set(ctx, map[string]any{
			"user_id": userID, "project_id": projectID, "execution_id": execution.id, "native": true, "task_type": "pipeline",
			"status": execution.status, "started_at": execution.started, "quota_reservation_id": reservationID, "quota_attempt_id": execution.attempt,
		}); err != nil {
			t.Fatalf("seed local execution %s: %v", execution.id, err)
		}
	}
	reservation := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations").Doc(reservationID)
	if _, err := reservation.Set(ctx, map[string]any{"user_id": userID, "project_id": projectID, "attempt_id": attemptB,
		"execution_id": "execution-b", "trigger": "scheduled_daily", "intent_status": "compiling", "settlement_status": "pending"}); err != nil {
		t.Fatalf("seed live local B reservation: %v", err)
	}
	if _, err := reservation.Collection("attempts").Doc(attemptA).Set(ctx, map[string]any{"user_id": userID, "project_id": projectID,
		"attempt_id": attemptA, "execution_id": "execution-a", "trigger": "scheduled_daily", "intent_status": "failed", "settlement_status": "refunded"}); err != nil {
		t.Fatalf("seed archived local A reservation: %v", err)
	}
	statusA, reservationA, gotAttemptA, err := manager.StatusWithIncarnation(ctx, userID, projectID, "execution-a")
	if err != nil || statusA == nil || reservationA != reservationID || gotAttemptA != attemptA || statusA.QuotaSettlement != "refunded" {
		t.Fatalf("local historical A status=%+v reservation=%q attempt=%q err=%v; want archived refund", statusA, reservationA, gotAttemptA, err)
	}
	statusB, reservationB, gotAttemptB, err := manager.StatusWithIncarnation(ctx, userID, projectID, "execution-b")
	if err != nil || statusB == nil || reservationB != reservationID || gotAttemptB != attemptB || statusB.QuotaSettlement != "pending" {
		t.Fatalf("local current B status=%+v reservation=%q attempt=%q err=%v; want live pending settlement", statusB, reservationB, gotAttemptB, err)
	}
}
