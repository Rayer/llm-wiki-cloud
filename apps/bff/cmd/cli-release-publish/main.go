package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/clirelease"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "CLI release publish:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("cli-release-publish", flag.ContinueOnError)
	bucket := flags.String("bucket", "", "approved CLI release bucket")
	publicBaseURL := flags.String("public-base-url", "", "verified anonymous HTTPS download base URL")
	releaseDir := flags.String("release-dir", "", "directory containing the packaged version release")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *bucket == "" || *publicBaseURL == "" || *releaseDir == "" || flags.NArg() != 0 {
		return errors.New("--bucket, --public-base-url, and --release-dir are required")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(*releaseDir, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest clirelease.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("read package manifest: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("create GCS client: %w", err)
	}
	defer client.Close()
	store := clirelease.NewGCSStore(client, *bucket)
	if err := clirelease.Publish(ctx, store, manifest, *releaseDir, *publicBaseURL, clirelease.VerifyPublicAsset); err != nil {
		return err
	}
	fmt.Printf("Published %s (%s) to the configured release objects.\n", manifest.Version, manifest.Channel)
	return nil
}
