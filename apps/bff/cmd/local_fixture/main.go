package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/firestore"
	"gopkg.in/yaml.v3"
)

func main() {
	configPath := flag.String("config", "", "generated local fixture YAML")
	flag.Parse()
	if *configPath == "" {
		log.Fatal("--config is required")
	}
	fixture, err := readLocalFixture(*configPath)
	if err != nil {
		log.Fatalf("local fixture config: %v", err)
	}
	if fixture.Email != "admin-local@llm.wiki.dev" || len(fixture.Password) < 8 {
		log.Fatal("local fixture config has an invalid account identity")
	}

	cfg, err := config.Load(".")
	if err != nil {
		log.Fatalf("local fixture configuration: %v", err)
	}
	if cfg.LocalCloudScope == "" || cfg.GCPProject != "llm-wiki-cloud" || cfg.FirestoreDatabaseID != "llm-wiki-cloud-local" {
		log.Fatal("local fixture requires the configured local cloud scope and target database")
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
	userID, created, err := auth.EnsureLocalPasswordFixture(ctx, client.Raw(), fixture.Email, fixture.Password)
	if err != nil {
		log.Fatalf("local fixture account: %v", err)
	}
	fmt.Printf("local account ready: user_id=%s created=%t\n", userID, created)
}

type localFixtureConfig struct {
	Email    string `yaml:"email"`
	Password string `yaml:"password"`
}

func readLocalFixture(path string) (localFixtureConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return localFixtureConfig{}, err
	}
	defer file.Close()
	var fixture localFixtureConfig
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&fixture); err != nil {
		return localFixtureConfig{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return localFixtureConfig{}, fmt.Errorf("expected exactly one YAML document")
	}
	if fixture.Email == "" || fixture.Password == "" {
		return localFixtureConfig{}, fmt.Errorf("email and password are required")
	}
	return fixture, nil
}
