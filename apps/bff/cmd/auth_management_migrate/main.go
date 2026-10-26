package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	firestoreclient "github.com/rayer/llm-wiki-bff/internal/firestore"
)

const maxManifestBytes = 4 << 20

type migrationManifest struct {
	SchemaVersion     int                              `json:"schema_version"`
	RunID             string                           `json:"run_id"`
	SourceEnvironment string                           `json:"source_environment"`
	CreatedAt         time.Time                        `json:"created_at"`
	Targets           []auth.ManagementMigrationTarget `json:"targets"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("auth-management-migrate", flag.ContinueOnError)
	mode := flags.String("mode", "", "preview, initialize-shadow, apply, readback, or central-cutover")
	runID := flags.String("run-id", "", "stable migration run identifier")
	manifestPath := flags.String("manifest", "", "reviewed manifest path for apply/readback/central-cutover")
	allowLive := flags.String("authorize-live-target", "", "exact configured Auth environment for a separately authorized live mutation")
	ackLegacyRetired := flags.String("ack-legacy-writers-retired", "", "migration run ID after old independent role writers and in-flight updates have stopped")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	configPath := strings.TrimSpace(os.Getenv("LWC_APP_CONFIG_PATH"))
	cfg, err := config.LoadAuthFile(configPath)
	if err != nil {
		return fmt.Errorf("load Auth config: %w", err)
	}
	environment := strings.TrimSpace(cfg.AuthSessionEnvironment)
	if environment == "" {
		return errors.New("Auth session environment is required")
	}
	fs, err := firestoreclient.NewClientWithDatabaseAndScope(cfg.GCPProject, cfg.FirestoreDatabaseID, "", "", cfg.LocalCloudScope)
	if err != nil {
		return fmt.Errorf("initialize Auth database: %w", err)
	}
	defer fs.Close()
	authority := auth.NewManagementAuthorityWithCatalog(fs.Raw(), environment, cfg.ManagementScopes)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch *mode {
	case "preview":
		if !validRunID(*runID) {
			return errors.New("preview requires a stable run-id")
		}
		targets, err := authority.PreviewLegacyAdminMigration(ctx)
		if err != nil {
			return fmt.Errorf("preview legacy admins: %w", err)
		}
		manifest := migrationManifest{SchemaVersion: 1, RunID: *runID, SourceEnvironment: environment, CreatedAt: time.Now().UTC(), Targets: targets}
		return json.NewEncoder(os.Stdout).Encode(manifest)
	case "initialize-shadow":
		if cfg.LocalCloudScope == "" && *allowLive != cfg.AuthEnvironment {
			return errors.New("nonlocal authority initialization requires separate authorization and an exact authorize-live-target value")
		}
		epoch, err := authority.InitializeLegacyShadow(ctx, cfg.ConfigID)
		if err != nil {
			return fmt.Errorf("initialize legacy-shadow authority: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(epoch)
	case "apply", "readback", "central-cutover":
		manifest, err := readManifest(*manifestPath)
		if err != nil {
			return err
		}
		if manifest.SourceEnvironment != environment {
			return errors.New("manifest source environment does not match the loaded Auth config")
		}
		if *mode == "apply" || *mode == "central-cutover" {
			if cfg.LocalCloudScope == "" && *allowLive != cfg.AuthEnvironment {
				return errors.New("nonlocal apply requires separate authorization and an exact authorize-live-target value")
			}
			if *mode == "central-cutover" {
				if *ackLegacyRetired != manifest.RunID {
					return errors.New("central cutover requires ack-legacy-writers-retired equal to the reviewed manifest run ID")
				}
				epoch, err := authority.CutoverToCentral(ctx, manifest.RunID, cfg.ConfigID, manifest.Targets)
				if err != nil {
					return fmt.Errorf("cut over Auth management authority: %w", err)
				}
				return json.NewEncoder(os.Stdout).Encode(epoch)
			}
			results := make([]auth.ManagementMigrationResult, 0, len(manifest.Targets)*2)
			for _, target := range manifest.Targets {
				for _, scope := range target.Scopes {
					result, err := authority.ApplyLegacyMigrationScope(ctx, manifest.RunID, target, scope)
					if err != nil {
						return fmt.Errorf("apply migration for target %s scope %s: %w", target.UserID, scope, err)
					}
					results = append(results, result)
				}
			}
			return json.NewEncoder(os.Stdout).Encode(struct {
				SchemaVersion int                              `json:"schema_version"`
				RunID         string                           `json:"run_id"`
				Results       []auth.ManagementMigrationResult `json:"results"`
			}{1, manifest.RunID, results})
		}
		results := make([]auth.ManagementMigrationResult, 0, len(manifest.Targets)*2)
		for _, target := range manifest.Targets {
			for _, scope := range target.Scopes {
				result, err := authority.ReadLegacyMigrationScopeForTarget(ctx, manifest.RunID, target, scope)
				if err != nil {
					return fmt.Errorf("read migration result for target %s scope %s: %w", target.UserID, scope, err)
				}
				results = append(results, result)
			}
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			SchemaVersion int                              `json:"schema_version"`
			RunID         string                           `json:"run_id"`
			Results       []auth.ManagementMigrationResult `json:"results"`
		}{1, manifest.RunID, results})
	default:
		return errors.New("mode must be preview, initialize-shadow, apply, readback, or central-cutover")
	}
}

func readManifest(path string) (migrationManifest, error) {
	if strings.TrimSpace(path) == "" {
		return migrationManifest{}, errors.New("apply/readback requires a manifest path")
	}
	f, err := os.Open(path)
	if err != nil {
		return migrationManifest{}, errors.New("migration manifest is unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxManifestBytes {
		return migrationManifest{}, errors.New("migration manifest size is invalid")
	}
	var manifest migrationManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return migrationManifest{}, errors.New("migration manifest is invalid")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return migrationManifest{}, errors.New("migration manifest has trailing data")
	}
	if manifest.SchemaVersion != 1 || !validRunID(manifest.RunID) || strings.TrimSpace(manifest.SourceEnvironment) == "" || manifest.CreatedAt.IsZero() || manifest.Targets == nil {
		return migrationManifest{}, errors.New("migration manifest identity is invalid")
	}
	seen := map[string]bool{}
	for _, target := range manifest.Targets {
		if !validRunID(target.UserID) || seen[target.UserID] || target.Role != "admin" || target.SourceUpdateTime.IsZero() || len(target.Scopes) != 2 ||
			!sameScopes(target.Scopes, []string{auth.ManagementScopeAuthAdmin, auth.ManagementScopeLWCAdmin}) {
			return migrationManifest{}, errors.New("migration manifest target mapping is invalid")
		}
		seen[target.UserID] = true
	}
	return manifest, nil
}

func validRunID(value string) bool {
	if len(value) < 3 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func sameScopes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, value := range a {
		seen[value] = true
	}
	for _, value := range b {
		if !seen[value] {
			return false
		}
	}
	return true
}
