package documentimport

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ProcessStarter starts the dedicated worker binary used by the native local
// stack. Its child environment is an allowlist so provider credentials from the
// interactive shell never cross into a conversion job.
type ProcessStarter struct {
	Path          string
	Project       string
	Bucket        string
	DatabaseID    string
	Scope         string
	PythonPath    string
	LockDigest    string
	StorageHost   string
	FirestoreHost string
}

func (s ProcessStarter) Start(_ context.Context, ownerID, projectID, documentID, attemptID string) (string, error) {
	path := strings.TrimSpace(s.Path)
	if path == "" {
		return "", fmt.Errorf("local document import worker is unavailable")
	}
	cmd := exec.Command(path)
	cmd.Env = s.environment(ownerID, projectID, documentID, attemptID)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("local document import worker could not start")
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return "", fmt.Errorf("local document import worker could not detach")
	}
	return fmt.Sprintf("local-%d", pid), nil
}

func (s ProcessStarter) environment(ownerID, projectID, documentID, attemptID string) []string {
	env := []string{
		"DOCUMENT_IMPORT_OWNER_ID=" + ownerID,
		"DOCUMENT_IMPORT_PROJECT_ID=" + projectID,
		"DOCUMENT_IMPORT_DOCUMENT_ID=" + documentID,
		"DOCUMENT_IMPORT_ATTEMPT_ID=" + attemptID,
		"GCP_PROJECT=" + s.Project,
		"GOOGLE_CLOUD_PROJECT=" + s.Project,
		"BUCKET=" + s.Bucket,
		"FIRESTORE_DATABASE_ID=" + s.DatabaseID,
		"LOCAL_CLOUD_SCOPE=" + s.Scope,
	}
	for _, key := range []string{"PATH", "TMPDIR", "LANG", "LC_ALL"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	if s.PythonPath != "" {
		env = append(env, "DOCUMENT_IMPORT_PYTHON="+s.PythonPath)
	}
	if s.LockDigest != "" {
		env = append(env, "DOCUMENT_IMPORT_LOCK_DIGEST="+s.LockDigest)
	}
	if s.StorageHost != "" {
		env = append(env, "STORAGE_EMULATOR_HOST="+s.StorageHost)
	}
	if s.FirestoreHost != "" {
		env = append(env, "FIRESTORE_EMULATOR_HOST="+s.FirestoreHost)
	}
	return env
}

var _ JobStarter = ProcessStarter{}
