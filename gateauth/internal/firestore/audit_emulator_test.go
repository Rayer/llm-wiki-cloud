package firestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
)

func TestLocalFirestoreAuditCapturesSDKDocumentQueryAndTransactionReads(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("requires an explicitly owned Firestore emulator")
	}

	auditPath := filepath.Join(t.TempDir(), "firestore-audit.log")
	t.Setenv("GAT4_FIRESTORE_AUDIT_FILE", auditPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := newFirestoreClient(ctx, "demo-gat4-audit", "")
	if err != nil {
		t.Fatalf("create instrumented Firestore SDK client: %v", err)
	}
	defer client.Close()

	projectID := fmt.Sprintf("gat4-audit-negative-control-%d", time.Now().UnixNano())
	ref := client.Collection("projects").Doc(projectID)
	if _, err := ref.Set(ctx, map[string]string{"qualification_marker": "synthetic"}); err != nil {
		t.Fatalf("write synthetic project negative control: %v", err)
	}
	if _, err := ref.Get(ctx); err != nil {
		t.Fatalf("read synthetic project document negative control: %v", err)
	}
	if _, err := client.Collection("projects").Where("qualification_marker", "==", "synthetic").Documents(ctx).GetAll(); err != nil {
		t.Fatalf("query synthetic projects negative control: %v", err)
	}
	if err := client.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		_, err := tx.Get(ref)
		return err
	}); err != nil {
		t.Fatalf("read synthetic project transaction negative control: %v", err)
	}

	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read SDK audit log: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, "rpc=/google.firestore.v1.Firestore/Commit") {
		t.Fatalf("real SDK write was not audited: %s", log)
	}
	if strings.Count(log, "rpc=/google.firestore.v1.Firestore/BatchGetDocuments") < 2 {
		t.Fatalf("real SDK document and transaction reads were not audited: %s", log)
	}
	if !strings.Contains(log, "rpc=/google.firestore.v1.Firestore/RunQuery") {
		t.Fatalf("real SDK query was not audited: %s", log)
	}
	if !strings.Contains(log, "forbidden=projects") {
		t.Fatalf("real SDK projects access was not identified: %s", log)
	}
	if strings.Contains(log, projectID) || strings.Contains(log, "qualification_marker") || strings.Contains(log, "synthetic") {
		t.Fatalf("SDK audit log contains a document identifier or field value: %s", log)
	}
}
