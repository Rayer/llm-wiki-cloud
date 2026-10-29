package firestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
)

func TestFirestoreRequestAuditFindsOnlyPostDocumentsCollections(t *testing.T) {
	request := &firestorepb.GetDocumentRequest{
		Name: "projects/cloud-project/databases/auth-db/documents/local_scopes/synthetic-scope/users/synthetic-user",
	}
	collections, forbidden := firestoreRequestCollections(request)
	if strings.Join(collections, ",") != "local_scopes,users" || len(forbidden) != 0 {
		t.Fatalf("Auth user read was misclassified: collections=%v forbidden=%v", collections, forbidden)
	}

	request.Name += "/projects/synthetic-project"
	line := formatFirestoreAudit("/google.firestore.v1.Firestore/GetDocument", request)
	if !strings.Contains(line, "forbidden=projects") || strings.Contains(line, "synthetic-user") || strings.Contains(line, "synthetic-project") {
		t.Fatalf("project path was not safely identified: %s", line)
	}
}

func TestFirestoreRequestAuditDoesNotPersistQueryOrReferenceValues(t *testing.T) {
	values := []*firestorepb.Value{
		{ValueType: &firestorepb.Value_StringValue{StringValue: "https://example.test/documents/private_literal_value/doc"}},
		{ValueType: &firestorepb.Value_ReferenceValue{ReferenceValue: "projects/demo/databases/(default)/documents/projects/private"}},
	}
	for _, value := range values {
		request := &firestorepb.RunQueryRequest{
			Parent: "projects/demo/databases/(default)/documents",
			QueryType: &firestorepb.RunQueryRequest_StructuredQuery{StructuredQuery: &firestorepb.StructuredQuery{
				From: []*firestorepb.StructuredQuery_CollectionSelector{{CollectionId: "users"}},
				Where: &firestorepb.StructuredQuery_Filter{FilterType: &firestorepb.StructuredQuery_Filter_FieldFilter{
					FieldFilter: &firestorepb.StructuredQuery_FieldFilter{
						Field: &firestorepb.StructuredQuery_FieldReference{FieldPath: "profile"}, Value: value,
					},
				}},
			}},
		}
		line := formatFirestoreAudit("/google.firestore.v1.Firestore/RunQuery", request)
		if !strings.Contains(line, "collections=users forbidden=") || strings.Contains(line, "private_literal_value") || strings.Contains(line, "forbidden=projects") {
			t.Fatalf("query literal or reference value leaked into audit: %s", line)
		}
	}
}

func TestLocalFirestoreAuditRequiresNumericLoopbackAndWritesCollectionOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firestore-audit.log")
	t.Setenv("FIRESTORE_EMULATOR_HOST", "127.0.0.1:62371")
	t.Setenv("GAT4_FIRESTORE_AUDIT_FILE", path)
	conn, enabled, err := newLocalFirestoreAuditConnection()
	if err != nil || !enabled {
		t.Fatalf("local loopback audit option enabled=%t err=%v", enabled, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close audit connection: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("audit file mode=%v err=%v", info, err)
	}

	t.Setenv("FIRESTORE_EMULATOR_HOST", "firestore.googleapis.com:443")
	if _, enabled, err := newLocalFirestoreAuditConnection(); err == nil || enabled {
		t.Fatalf("cloud Firestore audit target was accepted: enabled=%t err=%v", enabled, err)
	}
	t.Setenv("FIRESTORE_EMULATOR_HOST", "localhost:62371")
	if _, enabled, err := newLocalFirestoreAuditConnection(); err == nil || enabled {
		t.Fatalf("hostname emulator target was accepted: enabled=%t err=%v", enabled, err)
	}
}
