package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadLocalFixtureReadsGeneratedYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local_fixture.yaml")
	if err := os.WriteFile(path, []byte("email: admin-local@llm.wiki.dev\npassword: fixture-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture, err := readLocalFixture(path)
	if err != nil || fixture.Email != "admin-local@llm.wiki.dev" || fixture.Password != "fixture-password" {
		t.Fatalf("readLocalFixture=(%+v,%v)", fixture, err)
	}
}

func TestReadLocalFixtureRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	for name, input := range map[string]string{
		"unknown field":      "email: admin-local@llm.wiki.dev\npassword: fixture-password\nrole: admin\n",
		"multiple documents": "email: admin-local@llm.wiki.dev\npassword: fixture-password\n---\nemail: other@example.test\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "local_fixture.yaml")
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readLocalFixture(path); err == nil {
				t.Fatal("readLocalFixture accepted an unexpected config shape")
			}
		})
	}
}
