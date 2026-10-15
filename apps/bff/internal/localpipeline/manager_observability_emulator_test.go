package localpipeline

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestPendingResultsAndReservationsShareReconciliationDetailBudget(t *testing.T) {
	emulator := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if emulator == "" {
		t.Skip("requires loopback Firestore emulator")
	}
	host, _, err := net.SplitHostPort(emulator)
	if err != nil {
		t.Fatalf("FIRESTORE_EMULATOR_HOST must be host:port: %v", err)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		t.Fatalf("resolve Firestore emulator host: %v", err)
	}
	loopback := false
	for _, ip := range ips {
		loopback = loopback || ip.IsLoopback()
	}
	if !loopback {
		t.Fatal("FIRESTORE_EMULATOR_HOST must resolve to loopback")
	}
	connection, err := net.DialTimeout("tcp", emulator, time.Second)
	if err != nil {
		t.Fatalf("connect to loopback Firestore emulator: %v", err)
	}
	_ = connection.Close()

	// GCS object reads in this sweep are only used to resolve execution state or
	// check an invocation-failure marker. This fixture returns a normal missing
	// object response without touching a shared emulator bucket.
	storageFixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":404,"message":"Not Found"}}`)
	}))
	t.Cleanup(storageFixture.Close)

	const project = "lwc394-recovery-observability"
	const database = "(default)"
	scope := fmt.Sprintf("lwc394-recovery-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("STORAGE_EMULATOR_HOST", storageFixture.URL)

	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create scoped Firestore client: %v", err)
	}
	storageClient, err := gcs.NewClientWithScope("unused-bucket", scope)
	if err != nil {
		_ = fsClient.Close()
		t.Fatalf("create local GCS client: %v", err)
	}
	manager, err := New(Config{Firestore: fsClient.Raw(), Storage: storageClient, Worker: "fixture", Scope: scope})
	if err != nil {
		_ = storageClient.Close()
		_ = fsClient.Close()
		t.Fatalf("create recovery manager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const resultCount = 5
	const reservationCount = 20
	var executionIDs, resultExecutionIDs, lockIDs, reservationIDs, quotaIDs []string
	executionCollection := scopedfirestore.Collection(fsClient.Raw(), "executions")
	lockCollection := scopedfirestore.Collection(fsClient.Raw(), "local_pipeline_locks")
	reservationCollection := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota_reservations")
	quotaCollection := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, id := range executionIDs {
			_, _ = executionCollection.Doc(id).Delete(cleanupCtx)
		}
		for _, id := range lockIDs {
			_, _ = lockCollection.Doc(id).Delete(cleanupCtx)
		}
		for _, id := range reservationIDs {
			_, _ = reservationCollection.Doc(id).Delete(cleanupCtx)
		}
		for _, id := range quotaIDs {
			_, _ = quotaCollection.Doc(id).Delete(cleanupCtx)
		}
		_ = storageClient.Close()
		_ = fsClient.Close()
	})
	for index := 0; index < resultCount; index++ {
		suffix := fmt.Sprintf("%d-%d", time.Now().UnixNano(), index)
		executionID := "obs-result-" + suffix
		userID := "obs-user-" + suffix
		projectID := "obs-project-" + suffix
		lockID := userID + "__" + projectID
		executionIDs = append(executionIDs, executionID)
		resultExecutionIDs = append(resultExecutionIDs, executionID)
		lockIDs = append(lockIDs, lockID)
		if _, err := executionCollection.Doc(executionID).Set(ctx, map[string]any{
			"native": true, "user_id": userID, "project_id": projectID,
			"quota_reservation_id": "", "status": "RUNNING", "started_at": time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed pending execution: %v", err)
		}
		if _, err := lockCollection.Doc(lockID).Set(ctx, map[string]any{
			"execution_id": executionID, "user_id": userID, "project_id": projectID, "status": "RUNNING",
		}); err != nil {
			t.Fatalf("seed pending execution lock: %v", err)
		}
		manager.pendingResults[executionID] = processResult{
			executionID: executionID, userID: userID, projectID: projectID, spawnFailed: true,
		}
	}

	expectedSettlements := make(map[string]string, reservationCount)
	expectedStatuses := make(map[string]string, reservationCount)
	quotaIDsByExecution := make(map[string]string, reservationCount)
	for index := 0; index < reservationCount; index++ {
		suffix := fmt.Sprintf("%d-%d", time.Now().UnixNano(), index)
		reservationID := "obs-reservation-" + suffix
		executionID := "obs-settlement-execution-" + suffix
		userID := "obs-settlement-user-" + suffix
		projectID := "obs-settlement-project-" + suffix
		quotaID := userID + "__" + projectID
		state, settlement := "SUCCEEDED", "charged"
		if index%2 == 1 {
			state, settlement = "FAILED", "refunded"
		}
		reservedAt := time.Now().UTC().Truncate(time.Microsecond)
		dayKey := reservedAt.Format("2006-01-02")
		executionIDs = append(executionIDs, executionID)
		reservationIDs = append(reservationIDs, reservationID)
		quotaIDs = append(quotaIDs, quotaID)
		expectedSettlements[reservationID] = settlement
		expectedStatuses[executionID] = state
		quotaIDsByExecution[executionID] = quotaID
		if _, err := executionCollection.Doc(executionID).Set(ctx, map[string]any{
			"native": true, "user_id": userID, "project_id": projectID,
			"quota_reservation_id": reservationID, "status": state,
			"failure_reason": "", "finished_at": reservedAt,
		}); err != nil {
			t.Fatalf("seed settleable native execution: %v", err)
		}
		if _, err := reservationCollection.Doc(reservationID).Set(ctx, map[string]any{
			"user_id": userID, "project_id": projectID, "execution_id": executionID,
			"settlement_status": "pending", "reserved_at": reservedAt, "day_key": dayKey,
		}); err != nil {
			t.Fatalf("seed pending quota reservation: %v", err)
		}
		if _, err := quotaCollection.Doc(quotaID).Set(ctx, map[string]any{
			"runs_today": int64(1), "day_key": dayKey, "last_run_at": reservedAt,
		}); err != nil {
			t.Fatalf("seed quota state for settleable reservation: %v", err)
		}
	}

	exporter := tracetest.NewInMemoryExporter()
	traceProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(traceProvider)
	defer func() {
		otel.SetTracerProvider(previous)
		_ = traceProvider.Shutdown(context.Background())
	}()

	if err := manager.ReconcilePendingQuotaSettlements(ctx); err != nil {
		t.Fatalf("ReconcilePendingQuotaSettlements() error = %v", err)
	}
	manager.mu.Lock()
	remainingResults := len(manager.pendingResults)
	manager.mu.Unlock()
	if remainingResults != 0 {
		t.Fatalf("pending results remaining=%d, want zero after actual reconciliation", remainingResults)
	}

	var root *tracetest.SpanStub
	detailCount, transactionCount := 0, 0
	for _, span := range exporter.GetSpans() {
		switch span.Name {
		case "pipeline.quota.reconcile":
			copy := span
			root = &copy
		case "pipeline.recovery.result", "pipeline.recovery.lookup", "pipeline.recovery.transaction":
			detailCount++
			if span.Name == "pipeline.recovery.transaction" {
				transactionCount++
			}
		}
	}
	if root == nil {
		t.Fatalf("reconciliation root span missing: %+v", exporter.GetSpans())
	}
	counts := map[string]int64{}
	for _, attr := range root.Attributes {
		if attr.Value.Type() == attribute.INT64 {
			counts[string(attr.Key)] = attr.Value.AsInt64()
		}
	}
	wantSuppressed := reservationCount*2 - 1 // each actual transaction and each result after the cap
	if counts["pipeline.processed_count"] != int64(resultCount+reservationCount) || counts["pipeline.suppressed_count"] != int64(wantSuppressed) {
		t.Fatalf("reconciliation counts=%v, want processed=%d suppressed=%d", counts, resultCount+reservationCount, wantSuppressed)
	}
	if detailCount != 16 || transactionCount != resultCount {
		t.Fatalf("detail spans=%d transactions=%d, want shared 16-detail cap and %d real transactions", detailCount, transactionCount, resultCount)
	}

	for index, executionID := range resultExecutionIDs {
		doc, err := executionCollection.Doc(executionID).Get(ctx)
		if err != nil || doc.Data()["status"] != "FAILED" || doc.Data()["worker_result_observed"] != true {
			t.Errorf("execution %d result was not settled: data=%v err=%v", index, func() map[string]any {
				if doc == nil {
					return nil
				}
				return doc.Data()
			}(), err)
		}
	}
	for reservationID, wantSettlement := range expectedSettlements {
		reservation, err := reservationCollection.Doc(reservationID).Get(ctx)
		if err != nil || reservation.Data()["settlement_status"] != wantSettlement {
			t.Errorf("reservation %s settlement=%v err=%v, want %s after observation budget exhaustion", reservationID, func() any {
				if reservation == nil {
					return nil
				}
				return reservation.Data()["settlement_status"]
			}(), err, wantSettlement)
		}
	}
	for executionID, wantStatus := range expectedStatuses {
		doc, err := executionCollection.Doc(executionID).Get(ctx)
		if err != nil || doc.Data()["status"] != wantStatus {
			t.Errorf("execution %s status=%v err=%v, want %s", executionID, func() any {
				if doc == nil {
					return nil
				}
				return doc.Data()["status"]
			}(), err, wantStatus)
		}
		quotaID := quotaIDsByExecution[executionID]
		quota, quotaErr := quotaCollection.Doc(quotaID).Get(ctx)
		wantRuns := int64(1)
		if wantStatus == "FAILED" {
			wantRuns = 0
		}
		gotRuns := "<missing>"
		if quota != nil {
			gotRuns = fmt.Sprint(quota.Data()["runs_today"])
		}
		if quotaErr != nil || gotRuns != fmt.Sprint(wantRuns) {
			t.Errorf("settled quota %s runs=%v err=%v, want %d", quotaID, func() any {
				if quota == nil {
					return nil
				}
				return quota.Data()["runs_today"]
			}(), quotaErr, wantRuns)
		}
	}

}

func TestReconciliationTraceRetainsCommittedTerminalStatus(t *testing.T) {
	emulator := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if emulator == "" {
		t.Skip("requires loopback Firestore emulator")
	}
	host, _, err := net.SplitHostPort(emulator)
	if err != nil {
		t.Fatalf("FIRESTORE_EMULATOR_HOST must be host:port: %v", err)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		t.Fatalf("resolve Firestore emulator host: %v", err)
	}
	loopback := false
	for _, ip := range ips {
		loopback = loopback || ip.IsLoopback()
	}
	if !loopback {
		t.Fatal("FIRESTORE_EMULATOR_HOST must resolve to loopback")
	}
	connection, err := net.DialTimeout("tcp", emulator, time.Second)
	if err != nil {
		t.Fatalf("connect to loopback Firestore emulator: %v", err)
	}
	_ = connection.Close()

	storageFixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":404,"message":"Not Found"}}`)
	}))
	t.Cleanup(storageFixture.Close)

	const project = "lwc394-retained-status-observability"
	const database = "(default)"
	scope := fmt.Sprintf("lwc394-retained-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", project)
	t.Setenv("FIRESTORE_DATABASE_ID", database)
	t.Setenv("STORAGE_EMULATOR_HOST", storageFixture.URL)
	fsClient, err := scopedfirestore.NewClientWithDatabaseAndScope(project, database, "", "", scope)
	if err != nil {
		t.Fatalf("create scoped Firestore client: %v", err)
	}
	storageClient, err := gcs.NewClientWithScope("unused-bucket", scope)
	if err != nil {
		_ = fsClient.Close()
		t.Fatalf("create local GCS client: %v", err)
	}
	manager, err := New(Config{Firestore: fsClient.Raw(), Storage: storageClient, Worker: "fixture", Scope: scope})
	if err != nil {
		_ = storageClient.Close()
		_ = fsClient.Close()
		t.Fatalf("create recovery manager: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	executionID := "retained-execution-" + suffix
	userID := "retained-user-" + suffix
	projectID := "retained-project-" + suffix
	execution := scopedfirestore.Collection(fsClient.Raw(), "executions").Doc(executionID)
	lock := scopedfirestore.Collection(fsClient.Raw(), "local_pipeline_locks").Doc(userID + "__" + projectID)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = execution.Delete(cleanupCtx)
		_, _ = lock.Delete(cleanupCtx)
		_ = storageClient.Close()
		_ = fsClient.Close()
	})
	if _, err := execution.Set(ctx, map[string]any{
		"native": true, "user_id": userID, "project_id": projectID,
		"quota_reservation_id": "", "status": "SUCCEEDED", "failure_reason": "",
		"finished_at": time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed committed terminal execution: %v", err)
	}
	if _, err := lock.Set(ctx, map[string]any{"execution_id": executionID, "status": "SUCCEEDED"}); err != nil {
		t.Fatalf("seed committed terminal lock: %v", err)
	}
	manager.pendingResults[executionID] = processResult{
		executionID: executionID, userID: userID, projectID: projectID, spawnFailed: true,
	}

	exporter := tracetest.NewInMemoryExporter()
	traceProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(traceProvider)
	defer func() {
		otel.SetTracerProvider(previous)
		_ = traceProvider.Shutdown(context.Background())
	}()
	if err := manager.ReconcilePendingQuotaSettlements(ctx); err != nil {
		t.Fatalf("ReconcilePendingQuotaSettlements() error = %v", err)
	}
	doc, err := execution.Get(ctx)
	if err != nil || doc.Data()["status"] != "SUCCEEDED" || doc.Data()["worker_result_observed"] != true {
		t.Fatalf("retained business status=%v err=%v, want SUCCEEDED with observed worker result", func() map[string]any {
			if doc == nil {
				return nil
			}
			return doc.Data()
		}(), err)
	}

	var resultSpans, transactionSpans int
	var reconcile *tracetest.SpanStub
	for _, span := range exporter.GetSpans() {
		switch span.Name {
		case "pipeline.recovery.result", "pipeline.recovery.transaction":
			if span.Name == "pipeline.recovery.result" {
				resultSpans++
			} else {
				transactionSpans++
			}
			attrs := map[string]string{}
			for _, attr := range span.Attributes {
				attrs[string(attr.Key)] = attr.Value.AsString()
			}
			if attrs["pipeline.recovery.transaction.outcome"] != "retained" || attrs["pipeline.recovery.status"] != "success" || attrs["pipeline.settlement.outcome"] != "not_applicable" {
				t.Errorf("%s reports stale or nonfinite transaction state: %v", span.Name, attrs)
			}
		case "pipeline.quota.reconcile":
			copy := span
			reconcile = &copy
		}
	}
	if resultSpans != 1 || transactionSpans != 1 || reconcile == nil {
		t.Fatalf("result spans=%d transaction spans=%d reconcile=%v", resultSpans, transactionSpans, reconcile != nil)
	}
	counts := map[string]int64{}
	for _, attr := range reconcile.Attributes {
		if attr.Value.Type() == attribute.INT64 {
			counts[string(attr.Key)] = attr.Value.AsInt64()
		}
	}
	if counts["pipeline.processed_count"] != 1 || counts["pipeline.suppressed_count"] != 0 {
		t.Fatalf("reconcile counts=%v, want one processed and no suppressed detail", counts)
	}

	missingID := "missing-execution-" + suffix
	missingRef := scopedfirestore.Collection(fsClient.Raw(), "executions").Doc(missingID)
	spanStart := len(exporter.GetSpans())
	if err := manager.persistRecoveredExecution(ctx, missingRef, lock, missingID, "", "FAILED", "stale candidate"); err != nil {
		t.Fatalf("persistRecoveredExecution(missing): %v", err)
	}
	var missingTransactionSpans int
	for _, span := range exporter.GetSpans()[spanStart:] {
		if span.Name != "pipeline.recovery.transaction" {
			continue
		}
		attrs := map[string]string{}
		for _, attr := range span.Attributes {
			attrs[string(attr.Key)] = attr.Value.AsString()
		}
		if attrs["pipeline.recovery.transaction.outcome"] != "no_op" || attrs["pipeline.recovery.status"] != "unknown" || attrs["pipeline.settlement.outcome"] != "no_op" {
			t.Errorf("missing execution trace claims stale transaction state: %v", attrs)
		}
		missingTransactionSpans++
	}
	if missingTransactionSpans != 1 {
		t.Fatalf("missing execution transaction spans=%d, want 1", missingTransactionSpans)
	}
	lockDoc, err := lock.Get(ctx)
	if err != nil || lockDoc.Data()["execution_id"] != executionID || lockDoc.Data()["status"] != "SUCCEEDED" {
		t.Fatalf("missing execution changed existing lock: data=%v err=%v", func() map[string]any {
			if lockDoc == nil {
				return nil
			}
			return lockDoc.Data()
		}(), err)
	}
}
