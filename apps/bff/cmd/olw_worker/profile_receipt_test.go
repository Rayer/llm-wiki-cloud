package main

import (
	"cloud.google.com/go/firestore"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
)

func TestProfileRequirementsDigestContract(t *testing.T) {
	requirements := []profileGuidanceRequirement{{ID: "r1", Text: "write clearly"}}
	want := profileartifacts.SHA256([]byte(`[{"id":"r1","text":"write clearly"}]`))
	if got := profileRequirementsDigest(requirements); got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	if profileRequirementsDigest(nil) != profileartifacts.SHA256([]byte("[]")) {
		t.Fatal("nil requirements must have empty-array digest")
	}
}

// All provider and storage output below is synthetic, using the actual worker
// execution and publisher path with an in-memory object store.
func TestProfileReceiptActualPublisherCallback(t *testing.T) {
	for _, mode := range []string{"success", "confirmed_readback", "execution_failure", "ambiguous", "receipt_failure"} {
		t.Run(mode, func(t *testing.T) {
			oldResolver, oldExec, oldPersist := resolveProfileGuidanceAtCompileStart, execOLW, persistProfileCompileReceipt
			t.Cleanup(func() {
				resolveProfileGuidanceAtCompileStart, execOLW, persistProfileCompileReceipt = oldResolver, oldExec, oldPersist
			})
			objects := newMemoryObjects()
			prefix := "users/user/projects/project/"
			seedCloudSource(t, objects, prefix, "raw-start", "", priorCloudReceipt())
			cfg := cloudCfgFor("user", "project", "profile-receipt")
			cfg.SuggestedQueries = false
			digest := profileRequirementsDigest([]profileGuidanceRequirement{{ID: "r1", Text: "clear writing"}})
			bootstrap := profileartifacts.BootstrapGuidanceRef{Revision: strings.Repeat("a", 64), ProfileRevision: 3, InputDigest: digest, ModelVersion: "fake", PromptVersion: "fake", SchemaVersion: profileartifacts.BootstrapGuidanceSchema}
			pin := &profileGuidancePin{ProfileRevision: 3, RequirementsDigest: digest, Revision: bootstrap.Revision, BootstrapRef: &bootstrap}
			resolveProfileGuidanceAtCompileStart = func(context.Context, workerConfig, objectStore) (*profileGuidancePin, error) { return pin, nil }
			execOLW = func(_ context.Context, vault string, _ []string, _ []string, _, _ io.Writer) error {
				// An edit after compile start must not change the recorded consumed reference.
				pin.ProfileRevision = 4
				bootstrap.ProfileRevision = 4
				if mode == "execution_failure" {
					return errors.New("synthetic execution failure")
				}
				writeCloudRequiredOutputs(t, vault)
				return nil
			}
			calls := 0
			persistProfileCompileReceipt = func(ctx context.Context, receipt profileruntime.CompileReceipt) error {
				calls++
				data, attrs, err := objects.Read(ctx, prefix+generation.ManifestPath, 0, generation.MaxManifestBytes)
				if err != nil {
					t.Fatal(err)
				}
				if receipt.ManifestSHA256 != profileartifacts.SHA256(data) || receipt.ManifestGeneration != attrs.Generation {
					t.Fatal("receipt is not actual committed publisher result")
				}
				if receipt.ProfileRevision != 3 || receipt.RequirementsDigest != digest || receipt.ConsumedBootstrapGuidance.ProfileRevision != 3 {
					t.Fatalf("compile-start pin changed: %+v", receipt)
				}
				if receipt.UserID != cfg.UserID || receipt.ProjectID != cfg.ProjectID || receipt.ExecutionID != cfg.ExecutionID {
					t.Fatal("receipt scope mismatch")
				}
				if mode == "receipt_failure" {
					return errors.New("synthetic receipt failure")
				}
				return nil
			}
			var store objectStore = objects
			if mode == "ambiguous" || mode == "confirmed_readback" {
				store = &commitThenErrorStore{objectStore: objects, manifest: prefix + generation.ManifestPath, unknown: mode == "ambiguous"}
			}
			err := runCloudWorkerBatch(context.Background(), cfg, [][]string{{"run"}}, store)
			wantCalls := 1
			if mode == "execution_failure" || mode == "ambiguous" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("receipt calls=%d want=%d", calls, wantCalls)
			}
			if mode == "success" || mode == "confirmed_readback" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestProfileReceiptFirestoreAtomicOutboxAndDuplicate(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("local Firestore emulator required")
	}
	if os.Getenv("FIRESTORE_EMULATOR_HOST") != "127.0.0.1:8585" {
		t.Fatal("only loopback emulator allowed")
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:8585", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	t.Setenv("GOOGLE_CLOUD_PROJECT", fmt.Sprintf("receipt-%d", time.Now().UnixNano()))
	t.Setenv("FIRESTORE_DATABASE_ID", "profile-receipt-test")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	manifest := generation.Manifest{Version: generation.Version, GenerationID: "generation-receipt", CreatedAt: "2026-09-25T00:00:00Z", InputFingerprint: "synthetic", Files: []generation.File{
		{Path: "cache/concepts.jsonl", Size: 0, SHA256: profileartifacts.SHA256(nil), Generation: 1},
		{Path: "cache/id_map.json", Size: 2, SHA256: profileartifacts.SHA256([]byte("{}")), Generation: 2},
	}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	receipt := profileruntime.CompileReceipt{UserID: "owner", ProjectID: "project", ExecutionID: "synthetic-execution", ProfileRevision: 1, RequirementsDigest: profileartifacts.SHA256([]byte("[]")), ContentGeneration: manifest.GenerationID, ManifestSHA256: profileartifacts.SHA256(data), CanonicalConceptsDigest: manifestConceptDigest(manifest), ManifestGeneration: 3, CreatedAt: time.Now().UTC()}
	if err = writeProfileCompileReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	client, err := newProfileFirestoreClient(ctx, os.Getenv("GOOGLE_CLOUD_PROJECT"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ref := client.Collection("projects").Doc("owner_project").Collection("profile").Doc("state").Collection(profileruntime.CompileReceiptsCollection).Doc(manifest.GenerationID)
	if _, err = ref.Get(ctx); err != nil {
		t.Fatal(err)
	}
	work := profileruntime.Work{UserID: "owner", ProjectID: "project", Kind: "compile", ID: manifest.GenerationID}
	workRef := client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	snap, err := workRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var queued profileruntime.Work
	if err = snap.DataTo(&queued); err != nil {
		t.Fatal(err)
	}
	if !queued.Pending || queued.Revision != 1 || !queued.Due.Equal(receipt.CreatedAt.Truncate(time.Microsecond)) {
		t.Fatalf("unexpected queue: %+v", queued)
	}
	if _, err = workRef.Update(ctx, []firestore.Update{{Path: "attempts", Value: 2}, {Path: "token", Value: "already-claimed"}}); err != nil {
		t.Fatal(err)
	}
	receipt.CreatedAt = receipt.CreatedAt.Add(time.Minute)
	if err = writeProfileCompileReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	snap, err = workRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Data()["attempts"] != int64(2) || snap.Data()["token"] != "already-claimed" {
		t.Fatal("duplicate receipt reset queue")
	}
	receipt.ManifestGeneration++
	if err = writeProfileCompileReceipt(ctx, receipt); err == nil {
		t.Fatal("conflicting receipt accepted")
	}
}
