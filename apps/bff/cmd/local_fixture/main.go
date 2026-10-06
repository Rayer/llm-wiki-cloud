package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/firestore"
)

func main() {
	cfg, err := config.Load(".")
	if err != nil {
		log.Fatalf("local fixture configuration: %v", err)
	}
	if cfg.LocalCloudScope == "" || cfg.GCPProject != "llm-wiki-cloud" || cfg.FirestoreDatabaseID != "llm-wiki-cloud-local" {
		log.Fatal("local fixture requires the configured local cloud scope and target database")
	}
	email, password := os.Getenv("LOCAL_LOGIN_EMAIL"), os.Getenv("LOCAL_LOGIN_PASSWORD")
	if email == "" || password == "" {
		log.Fatal("LOCAL_LOGIN_EMAIL and LOCAL_LOGIN_PASSWORD are required")
	}
	client, err := firestore.NewClientWithDatabase(cfg.GCPProject, cfg.FirestoreDatabaseID, "", "")
	if err != nil {
		log.Fatalf("local Firestore client: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.CheckAvailable(ctx); err != nil {
		log.Fatalf("local Firestore target unavailable: %v", err)
	}
	userID, created, err := auth.EnsureLocalPasswordFixture(ctx, client.Raw(), email, password)
	if err != nil {
		log.Fatalf("local fixture account: %v", err)
	}
	fmt.Printf("local account ready: user_id=%s created=%t\n", userID, created)
}
