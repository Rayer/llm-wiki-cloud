package config

import (
	"reflect"
	"testing"
)

func gateAuthProjectCatalogForTest(t *testing.T, schemaVersion int, newCatalog bool) AuthFile {
	t.Helper()
	if schemaVersion == 2 {
		file := validAuthFileForTest()
		file.SchemaVersion = schemaVersion
		file.OAuthResources = authOAuthResourcesForTest(file.SyncServiceURL, file.Environment)
		setGateAuthProjectCatalogForTest(&file.OAuthResources[0], newCatalog)
		return file
	}
	const origin = "https://llm-wiki-bff-dev-580854833715.asia-east1.run.app"
	inputs := validAuthInputSnapshotV4ForTest(origin)
	inputs.SchemaVersion = schemaVersion
	inputs.OAuthResources = authOAuthResourcesForTest(origin, inputs.Environment)
	setGateAuthProjectCatalogForTest(&inputs.OAuthResources[0], newCatalog)
	if schemaVersion == 3 {
		inputs.Portal = nil
	}
	configID, err := AuthInputConfigID(inputs)
	if err != nil {
		t.Fatal(err)
	}
	inputs.ConfigID = configID
	return AuthFile{
		SchemaVersion: inputs.SchemaVersion, Target: inputs.Target, Environment: inputs.Environment, ConfigID: inputs.ConfigID,
		GCPProject: inputs.GCPProject, FirestoreDatabaseID: inputs.FirestoreDatabaseID,
		LocalCloudScope: inputs.LocalCloudScope, AuthServiceURL: inputs.AuthServiceURL, SyncServiceURL: inputs.SyncServiceURL,
		AllowedHosts: inputs.AllowedHosts, AllowedOrigins: inputs.AllowedOrigins,
		AuthSessionEnvironment: inputs.AuthSessionEnvironment, AuthSessionMigration: inputs.AuthSessionMigration,
		AuthDemoUserID: inputs.AuthDemoUserID, AuthDemoUserEmail: inputs.AuthDemoUserEmail, AuthDemoUserRole: inputs.AuthDemoUserRole,
		JWTSecret: "synthetic-validation-secret", OAuthResources: inputs.OAuthResources,
		ManagementScopes: inputs.ManagementScopes, ConsumerVerifiers: inputs.ConsumerVerifiers,
		ConsumerPublicProjection: inputs.ConsumerPublicProjection, Portal: authInputPortalToFile(inputs.Portal),
	}
}

func setGateAuthProjectCatalogForTest(project *AuthOAuthResource, newCatalog bool) {
	project.DefaultScopeSet = []string{"mcp:query"}
	if newCatalog {
		project.Scopes = []AuthOAuthScope{
			{Name: "mcp:query", Description: "Query project knowledge."},
			{Name: "mcp:content:read", Description: "List and read published Concepts, Sources, and retained Source text in this Project."},
			{Name: "mcp:raw:write", Description: "Add permitted raw sources."},
		}
		project.AllowedScopeSets = [][]string{
			{"mcp:query"},
			{"mcp:query", "mcp:content:read"},
			{"mcp:query", "mcp:raw:write"},
			{"mcp:query", "mcp:content:read", "mcp:raw:write"},
		}
		return
	}
	project.Scopes = []AuthOAuthScope{
		{Name: "mcp:query", Description: "Query project knowledge."},
		{Name: "mcp:raw:write", Description: "Add permitted raw sources."},
	}
	project.AllowedScopeSets = [][]string{{"mcp:query"}, {"mcp:query", "mcp:raw:write"}}
}

func TestLoadAuthFileAcceptsExactlyOldAndNewProjectCatalogs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		schemaVersion int
		newCatalog    bool
		wantScopes    []string
		wantSets      [][]string
	}{
		{
			name:          "schema 2 previous catalog",
			schemaVersion: 2,
			wantScopes:    []string{"mcp:query", "mcp:raw:write"},
			wantSets:      [][]string{{"mcp:query"}, {"mcp:query", "mcp:raw:write"}},
		},
		{
			name:          "schema 3 previous catalog remains readable",
			schemaVersion: 3,
			wantScopes:    []string{"mcp:query", "mcp:raw:write"},
			wantSets:      [][]string{{"mcp:query"}, {"mcp:query", "mcp:raw:write"}},
		},
		{
			name:          "schema 3 content read catalog",
			schemaVersion: 3,
			newCatalog:    true,
			wantScopes:    []string{"mcp:query", "mcp:content:read", "mcp:raw:write"},
			wantSets: [][]string{
				{"mcp:query"},
				{"mcp:query", "mcp:content:read"},
				{"mcp:query", "mcp:raw:write"},
				{"mcp:query", "mcp:content:read", "mcp:raw:write"},
			},
		},
		{
			name:          "schema 4 content read catalog",
			schemaVersion: 4,
			newCatalog:    true,
			wantScopes:    []string{"mcp:query", "mcp:content:read", "mcp:raw:write"},
			wantSets: [][]string{
				{"mcp:query"},
				{"mcp:query", "mcp:content:read"},
				{"mcp:query", "mcp:raw:write"},
				{"mcp:query", "mcp:content:read", "mcp:raw:write"},
			},
		},
		{
			name:          "schema 4 previous catalog remains readable",
			schemaVersion: 4,
			wantScopes:    []string{"mcp:query", "mcp:raw:write"},
			wantSets:      [][]string{{"mcp:query"}, {"mcp:query", "mcp:raw:write"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := gateAuthProjectCatalogForTest(t, tc.schemaVersion, tc.newCatalog)
			loaded, err := LoadAuthFile(writeAuthFileForTest(t, file))
			if err != nil {
				t.Fatalf("LoadAuthFile(): %v", err)
			}
			if loaded.ConfigSchemaVersion != tc.schemaVersion || len(loaded.AuthOAuthResources) != 2 {
				t.Fatalf("loaded schema/resources = %d/%d", loaded.ConfigSchemaVersion, len(loaded.AuthOAuthResources))
			}
			project := loaded.AuthOAuthResources[0]
			if got := project.ScopesNames(); !reflect.DeepEqual(got, tc.wantScopes) {
				t.Fatalf("Project scopes = %v, want %v", got, tc.wantScopes)
			}
			if !reflect.DeepEqual(project.AllowedScopeSets, tc.wantSets) {
				t.Fatalf("Project accepted sets = %v, want %v", project.AllowedScopeSets, tc.wantSets)
			}
			if !reflect.DeepEqual(project.DefaultScopeSet, []string{"mcp:query"}) {
				t.Fatalf("Project default = %v, want query only", project.DefaultScopeSet)
			}
		})
	}
}

func TestLoadAuthFileRejectsMixedOrExpandedProjectCatalogs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		schemaVersion int
		newCatalog    bool
		mutate        func(*AuthOAuthResource)
	}{
		{
			name:          "schema 3 new names with previous accepted sets",
			schemaVersion: 3,
			newCatalog:    true,
			mutate: func(project *AuthOAuthResource) {
				project.AllowedScopeSets = [][]string{{"mcp:query"}, {"mcp:query", "mcp:raw:write"}}
			},
		},
		{
			name:          "schema 3 arbitrary extra scope",
			schemaVersion: 3,
			newCatalog:    true,
			mutate: func(project *AuthOAuthResource) {
				project.Scopes = append(project.Scopes, AuthOAuthScope{Name: "mcp:admin", Description: "Unexpected."})
			},
		},
		{
			name:          "schema 3 read scope becomes default",
			schemaVersion: 3,
			newCatalog:    true,
			mutate: func(project *AuthOAuthResource) {
				project.DefaultScopeSet = []string{"mcp:query", "mcp:content:read"}
			},
		},
		{
			name:          "schema 3 write alone becomes accepted",
			schemaVersion: 3,
			newCatalog:    true,
			mutate: func(project *AuthOAuthResource) {
				project.AllowedScopeSets = append(project.AllowedScopeSets, []string{"mcp:raw:write"})
			},
		},
		{
			name:          "schema 2 cannot introduce content read",
			schemaVersion: 2,
			newCatalog:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := gateAuthProjectCatalogForTest(t, tc.schemaVersion, tc.newCatalog)
			if tc.mutate != nil {
				tc.mutate(&file.OAuthResources[0])
			}
			if _, err := LoadAuthFile(writeAuthFileForTest(t, file)); err == nil {
				t.Fatal("LoadAuthFile() accepted a mixed or expanded Project catalog")
			}
		})
	}
}
