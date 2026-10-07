package main

import (
	"context"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
)

func TestLocalPublicationReceiptIsCreateOnlyAndScoped(t *testing.T) {
	objects := newMemoryObjects()
	cfg := cloudCfgFor("user-a", "project-a", "local-execution-a")
	cfg.LocalCloudScope = "scope-a"
	prefix := workerProjectObjectPrefix(cfg)
	manifest := generation.Manifest{GenerationID: "generation-a"}
	if err := writeLocalPublicationReceipt(context.Background(), objects, prefix, cfg, manifest, 42); err != nil {
		t.Fatal(err)
	}
	relPath, err := localcloud.PublicationReceiptPath(cfg.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	data, attrs, err := objects.Read(context.Background(), prefix+relPath, 0, 4096)
	if err != nil || attrs.Generation <= 0 {
		t.Fatalf("local publication receipt readback attrs=%+v err=%v", attrs, err)
	}
	receipt, err := localcloud.DecodePublicationReceipt(data)
	if err != nil || receipt.ExecutionID != cfg.ExecutionID || receipt.GenerationID != manifest.GenerationID || receipt.ManifestGeneration != 42 {
		t.Fatalf("local publication receipt=%+v err=%v", receipt, err)
	}
	if err := writeLocalPublicationReceipt(context.Background(), objects, prefix, cfg, manifest, 43); err == nil {
		t.Fatal("receipt overwrite succeeded; want create-only conflict")
	}

	production := cloudCfgFor("user-b", "project-b", "execution-b")
	productionPrefix := workerProjectObjectPrefix(production)
	productionManifest := generation.Manifest{GenerationID: "generation-prod", LocalExecutionID: localExecutionIDFor(production)}
	if productionManifest.LocalExecutionID != production.ExecutionID {
		t.Fatalf("unscoped production manifest execution=%q, want %q", productionManifest.LocalExecutionID, production.ExecutionID)
	}
	if err := writeLocalPublicationReceipt(context.Background(), objects, productionPrefix, production, productionManifest, 1); err != nil {
		t.Fatalf("write unscoped production receipt: %v", err)
	}
	productionReceiptPath, err := localcloud.PublicationReceiptPath(production.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err = objects.Read(context.Background(), productionPrefix+productionReceiptPath, 0, 4096)
	if err != nil {
		t.Fatalf("read production receipt: %v", err)
	}
	receipt, err = localcloud.DecodePublicationReceipt(data)
	if err != nil || receipt.ExecutionID != production.ExecutionID || receipt.GenerationID != productionManifest.GenerationID {
		t.Fatalf("production receipt=%+v err=%v", receipt, err)
	}
}
