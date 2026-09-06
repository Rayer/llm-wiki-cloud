// Command auth_identity_audit validates existing users and, only with an
// explicit --apply, creates missing canonical-email reservations.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	firestoreclient "github.com/rayer/llm-wiki-bff/internal/firestore"
)

func main() {
	dryRun := flag.Bool("dry-run", true, "validate and report without writes (default)")
	apply := flag.Bool("apply", false, "apply validated canonical-email reservations")
	flag.Parse()
	if !*dryRun && !*apply {
		fail("refusing to run without --dry-run or --apply")
	}
	if *apply {
		*dryRun = false
	}

	cfg, err := config.Load(".")
	if err != nil {
		fail("unable to load configuration")
	}
	client, err := firestoreclient.NewClientWithDatabase(cfg.GCPProject, cfg.FirestoreDatabaseID, "", "")
	if err != nil {
		fail("unable to open Firestore")
	}
	defer client.Close()

	report, auditErr := auth.NewIdentityRepository(client.Raw()).AuditAndBackfill(context.Background(), *apply)
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fail("unable to write report")
	}
	if auditErr != nil {
		fail("identity audit validation failed")
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
