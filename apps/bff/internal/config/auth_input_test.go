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

func authOAuthResourcesForTest(syncOrigin, environment string) []AuthOAuthResource {
	return []AuthOAuthResource{
		{
			ID: "lwc-project", Kind: "project-template", Enabled: true,
			URI: strings.TrimRight(syncOrigin, "/") + "/mcp/projects/{project_id}", Name: "LWC Project",
			Scopes:           []AuthOAuthScope{{Name: "mcp:query", Description: "Query project knowledge."}, {Name: "mcp:content:read", Description: "List and read published Concepts, Sources, and retained Source text in this Project."}, {Name: "mcp:raw:write", Description: "Add permitted raw sources."}},
			AllowedScopeSets: [][]string{{"mcp:query"}, {"mcp:query", "mcp:content:read"}, {"mcp:query", "mcp:raw:write"}, {"mcp:query", "mcp:content:read", "mcp:raw:write"}}, DefaultScopeSet: []string{"mcp:query"},
		},
		{
			ID: "daily-brief", Kind: "fixed", Enabled: false,
			URI: authDailyBriefResourceURI(environment), Name: "Daily Brief",
			Scopes:           []AuthOAuthScope{{Name: "brief:manage", Description: "Read and manage Daily Brief reports and annotations in your private account space."}},
			AllowedScopeSets: [][]string{{"brief:manage"}}, DefaultScopeSet: []string{},
			VerifierAudience: "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app",
		},
	}
}

func validAuthInputSnapshotV2ForTest() AuthInputSnapshot {
	inputs := validAuthInputSnapshotForTest()
	inputs.SchemaVersion = 2
	inputs.OAuthResources = authOAuthResourcesForTest(inputs.SyncServiceURL, inputs.Environment)
	inputs.ConfigID, _ = AuthInputConfigID(inputs)
	return inputs
}

func authManagementScopesForTest(resource string) []AuthManagementScope {
	return []AuthManagementScope{
		{ID: "auth:admin", Product: "auth", Resource: resource, GrantMode: "operator-only",
			Name:          AuthLocalizedText{En: "Platform administrator", ZhTW: "平台管理員"},
			Description:   AuthLocalizedText{En: "Manage accounts and registered scopes.", ZhTW: "管理帳號與已登錄權限。"},
			GrantorScopes: []string{"auth:admin"}},
		{ID: "lwc:admin", Product: "lwc", Resource: resource, GrantMode: "operator-only",
			Name:          AuthLocalizedText{En: "LWC administrator", ZhTW: "LWC 管理員"},
			Description:   AuthLocalizedText{En: "Operate LWC projects.", ZhTW: "管理 LWC 專案。"},
			GrantorScopes: []string{"auth:admin"}},
	}
}

func validAuthInputSnapshotV4ForTest(origin string) AuthInputSnapshot {
	inputs := validAuthInputSnapshotV2ForTest()
	inputs.SchemaVersion = 4
	inputs.SyncServiceURL = origin
	inputs.OAuthResources = authOAuthResourcesForTest(origin, inputs.Environment)
	inputs.ManagementScopes = authManagementScopesForTest(origin)
	verifiers := []AuthConsumerVerifier{{ID: "lwc-bff", Environment: inputs.Environment,
		CallerServiceAccount: "lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com", Audience: "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app",
		Resource: origin, AllowedCredentialKinds: []string{"web-access", "cli-access", "mcp-oauth"},
		AuthorityModes: []string{"legacy-shadow", "central"}}}
	inputs.ConsumerVerifiers = &verifiers
	projection := AuthConsumerPublicProjection{Issuer: inputs.AuthServiceURL, Resource: origin, ManagementScopes: inputs.ManagementScopes}
	inputs.ConsumerPublicProjection = &projection
	inputs.Portal = &AuthInputPortal{LWCBFFOrigin: origin}
	inputs.ConfigID, _ = AuthInputConfigID(inputs)
	return inputs
}

func TestSchema4PortalOriginIsIncludedInImmutableInputAndFile(t *testing.T) {
	const devOrigin = "https://llm-wiki-bff-dev-580854833715.asia-east1.run.app"
	inputs := validAuthInputSnapshotV4ForTest(devOrigin)
	data, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAuthInputSnapshot(data)
	if err != nil || decoded.Portal == nil || decoded.Portal.LWCBFFOrigin != devOrigin {
		t.Fatalf("schema-4 input portal=%+v err=%v", decoded.Portal, err)
	}
	file := AuthFile{
		SchemaVersion: decoded.SchemaVersion, Target: decoded.Target, Environment: decoded.Environment, ConfigID: decoded.ConfigID,
		GCPProject: decoded.GCPProject, FirestoreDatabaseID: decoded.FirestoreDatabaseID,
		LocalCloudScope: decoded.LocalCloudScope, AuthServiceURL: decoded.AuthServiceURL, SyncServiceURL: decoded.SyncServiceURL,
		AllowedHosts: decoded.AllowedHosts, AllowedOrigins: decoded.AllowedOrigins,
		AuthSessionEnvironment: decoded.AuthSessionEnvironment, AuthSessionMigration: decoded.AuthSessionMigration,
		AuthDemoUserID: decoded.AuthDemoUserID, AuthDemoUserEmail: decoded.AuthDemoUserEmail, AuthDemoUserRole: decoded.AuthDemoUserRole,
		JWTSecret: "synthetic-validation-secret", OAuthResources: decoded.OAuthResources,
		ManagementScopes: decoded.ManagementScopes, ConsumerVerifiers: decoded.ConsumerVerifiers,
		ConsumerPublicProjection: decoded.ConsumerPublicProjection,
		Portal:                   &AuthFilePortal{LWCBFFOrigin: decoded.Portal.LWCBFFOrigin},
	}
	fileBytes, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := DecodeAuthFile(fileBytes)
	if err != nil || loaded.Portal == nil || loaded.Portal.LWCBFFOrigin != devOrigin {
		t.Fatalf("schema-4 file portal=%+v err=%v", loaded.Portal, err)
	}
	withoutPortal := inputs
	withoutPortal.Portal = nil
	withoutPortal.ConfigID, _ = AuthInputConfigID(withoutPortal)
	if err := ValidateAuthInputSnapshot(withoutPortal); err == nil {
		t.Fatal("schema-4 input without portal origin was accepted")
	}

	bad := validAuthInputSnapshotV4ForTest("https://llm-wiki-bff-580854833715.asia-east1.run.app")
	if err := ValidateAuthInputSnapshot(bad); err == nil {
		t.Fatal("DEV schema-4 input accepted the production BFF origin")
	}
	changed := inputs
	changed.Portal = &AuthInputPortal{LWCBFFOrigin: devOrigin + "/other"}
	changed.ConfigID, _ = AuthInputConfigID(changed)
	if err := ValidateAuthInputSnapshot(changed); err == nil {
		t.Fatal("schema-4 input accepted a portal path")
	}
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

func TestSchema2AuthInputSnapshotAdoptsEveryOAuthRegistryField(t *testing.T) {
	inputs := validAuthInputSnapshotV2ForTest()
	data, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAuthInputSnapshot(data); err != nil {
		t.Fatalf("DecodeAuthInputSnapshot(schema 2): %v", err)
	}
	base := inputs.ConfigID
	mutations := map[string]func(*AuthInputSnapshot){
		"resource id":   func(v *AuthInputSnapshot) { v.OAuthResources[1].ID = "daily-brief-next" },
		"resource kind": func(v *AuthInputSnapshot) { v.OAuthResources[1].Kind = "project-template" },
		"enabled":       func(v *AuthInputSnapshot) { v.OAuthResources[1].Enabled = true },
		"uri":           func(v *AuthInputSnapshot) { v.OAuthResources[1].URI += "/changed" },
		"resource name": func(v *AuthInputSnapshot) { v.OAuthResources[1].Name = "Brief renamed" },
		"account-selection client allowlist": func(v *AuthInputSnapshot) {
			v.OAuthResources[1].AccountSelectionClientIDs = []string{"lwc_mcp_client_" + strings.Repeat("A", 43)}
		},
		"scope name":         func(v *AuthInputSnapshot) { v.OAuthResources[1].Scopes[0].Name = "brief:read" },
		"scope description":  func(v *AuthInputSnapshot) { v.OAuthResources[1].Scopes[0].Description += " changed" },
		"accepted scope set": func(v *AuthInputSnapshot) { v.OAuthResources[1].AllowedScopeSets[0][0] = "brief:read" },
		"default scope set":  func(v *AuthInputSnapshot) { v.OAuthResources[1].DefaultScopeSet = []string{"brief:manage"} },
		"caller service account": func(v *AuthInputSnapshot) {
			v.OAuthResources[1].VerifierCallerServiceAccount = "brief-runtime@llm-wiki-cloud.iam.gserviceaccount.com"
		},
		"verifier audience": func(v *AuthInputSnapshot) { v.OAuthResources[1].VerifierAudience += "/changed" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := validAuthInputSnapshotV2ForTest()
			mutate(&changed)
			changed.ConfigID = ""
			got, err := AuthInputConfigID(changed)
			if err != nil || got == base {
				t.Fatalf("registry change config_id=%q err=%v; want identity change", got, err)
			}
		})
	}
}
