package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/buildinfo"
	"github.com/rayer/llm-wiki-bff/internal/exportjob"
	"github.com/rayer/llm-wiki-bff/internal/observability"
)

func main() {
	if err := run(); err != nil {
		log.Printf("export worker failed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	build := buildinfo.Current()
	traceProvider, traceErr := observability.InitExportWorkerTraces(build.Revision)
	if traceErr != nil {
		log.Printf("[observability] WARNING: export trace init failed (continuing): %v", traceErr)
	} else {
		defer observability.ShutdownTraces(traceProvider)
	}
	ctx, cancel := context.WithTimeout(context.Background(), exportjob.HardTimeout)
	defer cancel()
	ctx = observability.ExtractWorkerTraceContext(ctx, os.Getenv("LWC_TRACEPARENT"))
	userID, projectID, exportID := strings.TrimSpace(os.Getenv("EXPORT_USER_ID")), strings.TrimSpace(os.Getenv("EXPORT_PROJECT_ID")), strings.TrimSpace(os.Getenv("EXPORT_ID"))
	scope := exportjob.Scope(strings.TrimSpace(os.Getenv("EXPORT_SCOPE")))
	project := strings.TrimSpace(os.Getenv("GCP_PROJECT"))
	bucket := strings.TrimSpace(os.Getenv("BUCKET"))
	database := strings.TrimSpace(os.Getenv("FIRESTORE_DATABASE_ID"))
	signerID := strings.TrimSpace(os.Getenv("EXPORT_SIGNING_SERVICE_ACCOUNT"))
	if userID == "" || projectID == "" || exportID == "" || !scope.Valid() || project == "" || bucket == "" || signerID == "" {
		return fmt.Errorf("export worker configuration is incomplete")
	}
	var fs *firestore.Client
	var err error
	if database == "" {
		fs, err = firestore.NewClient(ctx, project)
	} else {
		fs, err = firestore.NewClientWithDatabase(ctx, project, database)
	}
	if err != nil {
		return fmt.Errorf("create export Firestore client: %w", err)
	}
	defer fs.Close()
	archives, err := exportjob.NewCloudArchiveStore(ctx, bucket, signerID)
	if err != nil {
		return err
	}
	defer archives.Close()
	repo := exportjob.NewFirestoreRepository(fs)
	job, err := repo.Get(ctx, userID, projectID, exportID)
	if err != nil {
		return fmt.Errorf("read export job: %w", err)
	}
	if job.Scope != scope {
		return fmt.Errorf("export job scope does not match its invocation")
	}
	return exportjob.RunExport(ctx, repo, archives, userID, projectID, job, fs)
}
