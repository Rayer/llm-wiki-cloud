package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func validAuthInputSnapshotForTest() AuthInputSnapshot {
	inputs := AuthInputSnapshot{
		SchemaVersion: 1, Environment: "dev", Target: "auth", SourceSHA: strings.Repeat("b", 40),
		GCPProject: "llm-wiki-cloud", FirestoreDatabaseID: "llm-wiki-cloud-dev",
		AuthServiceURL: "https://auth.dev.example.test", SyncServiceURL: "https://bff.dev.example.test",
		AllowedHosts:   []string{"auth.dev.example.test"},
		AllowedOrigins: []string{"https://wiki.example.test"}, AuthSessionEnvironment: "llm-wiki-cloud-dev",
		AuthSessionMigration: "disabled", AuthDemoUserID: "demo-user",
		AuthDemoUserEmail: "demo@example.test", AuthDemoUserRole: "member",
		JWTSecretVersion:     "projects/llm-wiki-cloud/secrets/jwt-secret-dev/versions/3",
		ConfigSecretResource: "projects/llm-wiki-cloud/secrets/lwc-auth-app-config-dev",
	}
	inputs.ConfigID, _ = AuthInputConfigID(inputs)
	return inputs
}

func TestDecodeAuthInputSnapshotRequiresCompleteImmutableContract(t *testing.T) {
	inputs := validAuthInputSnapshotForTest()
	data, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAuthInputSnapshot(data); err != nil {
		t.Fatalf("DecodeAuthInputSnapshot(valid): %v", err)
	}

	for name, malformed := range map[string]string{
		"missing required empty-capable key": strings.Replace(string(data), `"firestore_database_id":"llm-wiki-cloud-dev",`, "", 1),
		"unknown key":                        strings.TrimSuffix(string(data), "}") + `,"payload":"synthetic"}`,
		"duplicate key":                      strings.TrimSuffix(string(data), "}") + `,"target":"auth"}`,
		"null boolean":                       strings.Replace(string(data), `"enabled":false`, `"enabled":null`, 1),
		"null list":                          strings.Replace(string(data), `"allowed_hosts":["auth.dev.example.test"]`, `"allowed_hosts":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAuthInputSnapshot([]byte(malformed)); err == nil {
				t.Fatal("DecodeAuthInputSnapshot() accepted malformed snapshot")
			}
		})
	}

	bad := inputs
	bad.JWTSecretVersion = "projects/llm-wiki-cloud/secrets/jwt-secret-dev/versions/latest"
	bad.ConfigID, _ = AuthInputConfigID(bad)
	encoded, _ := json.Marshal(bad)
	if _, err := DecodeAuthInputSnapshot(encoded); err == nil {
		t.Fatal("DecodeAuthInputSnapshot() accepted a latest input credential reference")
	}
}
