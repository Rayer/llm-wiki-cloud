package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/documentimport"
	"github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/sourceautomation"
)

func main() {
	if err := run(); err != nil {
		log.Print("document import worker did not complete")
		os.Exit(1)
	}
}

func run() error {
	ownerID := strings.TrimSpace(os.Getenv("DOCUMENT_IMPORT_OWNER_ID"))
	projectID := strings.TrimSpace(os.Getenv("DOCUMENT_IMPORT_PROJECT_ID"))
	documentID := strings.TrimSpace(os.Getenv("DOCUMENT_IMPORT_DOCUMENT_ID"))
	attemptID := strings.TrimSpace(os.Getenv("DOCUMENT_IMPORT_ATTEMPT_ID"))
	cloudProject := strings.TrimSpace(os.Getenv("GCP_PROJECT"))
	databaseID := strings.TrimSpace(os.Getenv("FIRESTORE_DATABASE_ID"))
	bucket := strings.TrimSpace(os.Getenv("BUCKET"))
	if ownerID == "" || projectID == "" || documentID == "" || attemptID == "" || cloudProject == "" || bucket == "" {
		return fmt.Errorf("worker configuration is incomplete")
	}
	objects, err := gcs.NewClientWithScope(bucket, os.Getenv("LOCAL_CLOUD_SCOPE"))
	if err != nil {
		return fmt.Errorf("storage unavailable")
	}
	defer objects.Close()
	fs, err := firestore.NewClientWithDatabaseAndScope(cloudProject, databaseID, "", "", os.Getenv("LOCAL_CLOUD_SCOPE"))
	if err != nil {
		return fmt.Errorf("firestore unavailable")
	}
	defer fs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	projectObjects, ok := objects.Scope(ownerID, projectID).(documentimport.ObjectStore)
	if !ok {
		return fmt.Errorf("storage interface unavailable")
	}
	lockDigest := strings.TrimSpace(os.Getenv("DOCUMENT_IMPORT_LOCK_DIGEST"))
	if lockDigest == "" {
		lockDigest, err = documentimport.LockFileDigest("/app/requirements.lock")
		if err != nil {
			return fmt.Errorf("converter lock is unavailable")
		}
	}
	processor := documentimport.Processor{
		Repository:           documentimport.NewFirestoreRepository(fs.Raw()),
		Objects:              projectObjects,
		Converter:            documentimport.MarkItDownConverter{PythonPath: os.Getenv("DOCUMENT_IMPORT_PYTHON"), ScriptPath: os.Getenv("DOCUMENT_IMPORT_CONVERTER_SCRIPT")},
		Journal:              sourceautomation.NewJournal(fs.Raw(), objects),
		DependencyLockDigest: lockDigest,
	}
	return processor.Process(ctx, ownerID, projectID, documentID, attemptID)
}
