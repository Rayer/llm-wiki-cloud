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
	if err := writeLocalPublicationReceipt(context.Background(), objects, productionPrefix, production, manifest, 1); err != nil {
		t.Fatalf("unscoped production no-op: %v", err)
	}
	objects.mu.Lock()
	defer objects.mu.Unlock()
	if len(objects.objects) != 1 {
		t.Fatalf("receipt objects=%v; want only local scoped receipt", objects.objects)
	}
}
