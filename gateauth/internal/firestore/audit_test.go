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

func TestLocalFirestoreAuditRequiresNumericLoopbackAndWritesCollectionOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firestore-audit.log")
	t.Setenv("FIRESTORE_EMULATOR_HOST", "127.0.0.1:62371")
	t.Setenv("GAT4_FIRESTORE_AUDIT_FILE", path)
	if _, enabled, err := newLocalFirestoreAuditOption(); err != nil || !enabled {
		t.Fatalf("local loopback audit option enabled=%t err=%v", enabled, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("audit file mode=%v err=%v", info, err)
	}

	t.Setenv("FIRESTORE_EMULATOR_HOST", "firestore.googleapis.com:443")
	if _, enabled, err := newLocalFirestoreAuditOption(); err == nil || enabled {
		t.Fatalf("cloud Firestore audit target was accepted: enabled=%t err=%v", enabled, err)
	}
	t.Setenv("FIRESTORE_EMULATOR_HOST", "localhost:62371")
	if _, enabled, err := newLocalFirestoreAuditOption(); err == nil || enabled {
		t.Fatalf("hostname emulator target was accepted: enabled=%t err=%v", enabled, err)
	}
}
