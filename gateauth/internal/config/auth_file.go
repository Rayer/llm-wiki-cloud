package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/viper"

	"github.com/rayer/gateauth/internal/localcloud"
)

const MaxAuthConfigBytes = 64 << 10

var authConfigIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var authSourceSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var authConfigPortPattern = regexp.MustCompile(`^[0-9]{1,5}$`)
var authDemoUserIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var authDemoEmailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
var authDemoRolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var authOAuthIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var authOAuthScopePattern = regexp.MustCompile(`^[a-z][a-z0-9:_-]{0,63}$`)
var authServiceAccountPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{4,62}@[a-z0-9.-]+\.iam\.gserviceaccount\.com$`)

const authDailyBriefDevVerifierAudience = "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app"
const authDailyBriefProdVerifierAudience = "https://llm-wiki-auth-a5nkmux6pq-de.a.run.app"

type AuthFile struct {
	SchemaVersion            int                           `json:"schema_version"`
	Target                   string                        `json:"target"`
	Environment              string                        `json:"environment"`
	ConfigID                 string                        `json:"config_id"`
	GCPProject               string                        `json:"gcp_project"`
	FirestoreDatabaseID      string                        `json:"firestore_database_id"`
	LocalCloudScope          string                        `json:"local_cloud_scope"`
	AuthServiceURL           string                        `json:"auth_service_url"`
	SyncServiceURL           string                        `json:"sync_service_url"`
	AllowedHosts             []string                      `json:"allowed_hosts"`
	AllowedOrigins           []string                      `json:"allowed_origins"`
	AuthSessionEnvironment   string                        `json:"auth_session_environment"`
	AuthSessionMigration     string                        `json:"auth_session_migration"`
	RegistrationEnabled      *bool                         `json:"registration_enabled"`
	AuthDemoUserID           string                        `json:"auth_demo_user_id"`
	AuthDemoUserEmail        string                        `json:"auth_demo_user_email"`
	AuthDemoUserRole         string                        `json:"auth_demo_user_role"`
	JWTSecret                string                        `json:"jwt_secret"`
	Google                   AuthFileGoogle                `json:"google"`
	OAuthResources           []AuthOAuthResource           `json:"oauth_resources,omitempty"`
	ManagementScopes         []AuthManagementScope         `json:"management_scopes,omitempty"`
	ConsumerVerifiers        *[]AuthConsumerVerifier       `json:"consumer_verifiers,omitempty"`
	ConsumerPublicProjection *AuthConsumerPublicProjection `json:"consumer_public_projection,omitempty"`
}

type AuthLocalizedText struct {
	En   string `json:"en"`
	ZhTW string `json:"zh_tw"`
}

type AuthManagementScope struct {
	ID            string            `json:"id"`
	Product       string            `json:"product"`
	Resource      string            `json:"resource"`
	GrantMode     string            `json:"grant_mode"`
	Name          AuthLocalizedText `json:"name"`
	Description   AuthLocalizedText `json:"description"`
	GrantorScopes []string          `json:"grantor_scopes"`
}

type AuthConsumerVerifier struct {
	ID                     string   `json:"id"`
	Environment            string   `json:"environment"`
	CallerServiceAccount   string   `json:"caller_service_account"`
	Audience               string   `json:"audience"`
	Resource               string   `json:"resource"`
	AllowedCredentialKinds []string `json:"allowed_credential_kinds"`
	AuthorityModes         []string `json:"authority_modes"`
}

type AuthConsumerPublicProjection struct {
	Issuer           string                `json:"issuer"`
	Resource         string                `json:"resource"`
	ManagementScopes []AuthManagementScope `json:"management_scopes"`
}

// AuthOAuthResource is the single CaC-owned policy entry consumed by Auth's
// OAuth handlers. Schema-1 files omit the registry and retain legacy policy.
type AuthOAuthResource struct {
	ID                           string           `json:"id"`
	Kind                         string           `json:"kind"`
	Enabled                      bool             `json:"enabled"`
	URI                          string           `json:"uri"`
	Name                         string           `json:"name"`
	Scopes                       []AuthOAuthScope `json:"scopes"`
	AllowedScopeSets             [][]string       `json:"allowed_scope_sets"`
	DefaultScopeSet              []string         `json:"default_scope_set"`
	VerifierCallerServiceAccount string           `json:"verifier_caller_service_account"`
	VerifierAudience             string           `json:"verifier_audience"`
}

type AuthOAuthScope struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type AuthFileGoogle struct {
	Enabled          bool   `json:"enabled"`
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret"`
	Issuer           string `json:"issuer"`
	JWKSURL          string `json:"jwks_url"`
	TokenURL         string `json:"token_url"`
	LoginRedirectURL string `json:"login_redirect_url"`
	LinkRedirectURL  string `json:"link_redirect_url"`
	CompletionURL    string `json:"completion_url"`
}

// AuthInputSnapshot is the nonsecret Stage 1 identity retained with an Auth
// receipt. Credential references are already pinned to numeric versions.
type AuthInputSnapshot struct {
	SchemaVersion            int                           `json:"schema_version"`
	Environment              string                        `json:"environment"`
	Target                   string                        `json:"target"`
	SourceSHA                string                        `json:"source_sha"`
	ConfigID                 string                        `json:"config_id"`
	GCPProject               string                        `json:"gcp_project"`
	FirestoreDatabaseID      string                        `json:"firestore_database_id"`
	LocalCloudScope          string                        `json:"local_cloud_scope"`
	AuthServiceURL           string                        `json:"auth_service_url"`
	SyncServiceURL           string                        `json:"sync_service_url"`
	AllowedHosts             []string                      `json:"allowed_hosts"`
	AllowedOrigins           []string                      `json:"allowed_origins"`
	AuthSessionEnvironment   string                        `json:"auth_session_environment"`
	AuthSessionMigration     string                        `json:"auth_session_migration"`
	RegistrationEnabled      *bool                         `json:"registration_enabled"`
	AuthDemoUserID           string                        `json:"auth_demo_user_id"`
	AuthDemoUserEmail        string                        `json:"auth_demo_user_email"`
	AuthDemoUserRole         string                        `json:"auth_demo_user_role"`
	JWTSecretVersion         string                        `json:"jwt_secret_version"`
	Google                   AuthInputGoogle               `json:"google"`
	ConfigSecretResource     string                        `json:"config_secret_resource"`
	OAuthResources           []AuthOAuthResource           `json:"oauth_resources,omitempty"`
	ManagementScopes         []AuthManagementScope         `json:"management_scopes,omitempty"`
	ConsumerVerifiers        *[]AuthConsumerVerifier       `json:"consumer_verifiers,omitempty"`
	ConsumerPublicProjection *AuthConsumerPublicProjection `json:"consumer_public_projection,omitempty"`
}

type AuthInputGoogle struct {
	Enabled             bool   `json:"enabled"`
	ClientID            string `json:"client_id"`
	ClientSecretVersion string `json:"client_secret_version"`
	Issuer              string `json:"issuer"`
	JWKSURL             string `json:"jwks_url"`
	TokenURL            string `json:"token_url"`
	LoginRedirectURL    string `json:"login_redirect_url"`
	LinkRedirectURL     string `json:"link_redirect_url"`
	CompletionURL       string `json:"completion_url"`
}

// AuthInputConfigID binds only the source SHA, selected nonsecret fields, and
// numeric credential references. It never hashes credential payloads.
func AuthInputConfigID(inputs AuthInputSnapshot) (string, error) {
	inputs.ConfigID = ""
	canonical, err := json.Marshal(inputs)
	if err != nil {
		return "", errors.New("encode Auth input identity")
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// DecodeAuthInputSnapshot reads the immutable nonsecret Stage 1 contract used
// by Stage 2. Requiring its full key set prevents omitted optional fields from
// being silently supplied by the current checkout.
func DecodeAuthInputSnapshot(data []byte) (AuthInputSnapshot, error) {
	if len(data) == 0 || len(data) > MaxAuthConfigBytes {
		return AuthInputSnapshot{}, errors.New("Auth input snapshot size is invalid")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return AuthInputSnapshot{}, errors.New("Auth input snapshot contains malformed or duplicate JSON keys")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return AuthInputSnapshot{}, errors.New("Auth input snapshot is malformed")
	}
	version := authSchemaVersion(fields["schema_version"])
	keys := authInputSnapshotKeys
	if version == 2 {
		keys = append(append([]string(nil), authInputSnapshotKeys...), "oauth_resources")
	} else if version == 3 {
		keys = append(append([]string(nil), authInputSnapshotKeys...), "oauth_resources", "management_scopes", "consumer_verifiers", "consumer_public_projection")
	}
	if err := requireJSONKeys(fields, keys); err != nil {
		return AuthInputSnapshot{}, errors.New("Auth input snapshot has missing or unknown keys")
	}
	for _, field := range []struct {
		name string
		kind string
	}{
		{"schema_version", "integer"}, {"environment", "string"}, {"target", "string"},
		{"source_sha", "string"}, {"config_id", "string"}, {"gcp_project", "string"},
		{"firestore_database_id", "string"}, {"local_cloud_scope", "string"},
		{"auth_service_url", "string"}, {"sync_service_url", "string"}, {"allowed_hosts", "string_array"},
		{"allowed_origins", "string_array"}, {"auth_session_environment", "string"},
		{"auth_session_migration", "string"}, {"auth_demo_user_id", "string"},
		{"auth_demo_user_email", "string"}, {"auth_demo_user_role", "string"},
		{"jwt_secret_version", "string"}, {"config_secret_resource", "string"}, {"google", "object"},
	} {
		if err := requireAuthJSONType("inputs."+field.name, fields[field.name], field.kind); err != nil {
			return AuthInputSnapshot{}, err
		}
	}
	if version == 2 {
		if err := validateOAuthResourcesJSON("inputs.oauth_resources", fields["oauth_resources"]); err != nil {
			return AuthInputSnapshot{}, err
		}
	} else if version == 3 {
		if err := validateOAuthResourcesJSON("inputs.oauth_resources", fields["oauth_resources"]); err != nil {
			return AuthInputSnapshot{}, err
		}
		if err := validateAuthPublicPolicyJSON(fields); err != nil {
			return AuthInputSnapshot{}, err
		}
	}
	if string(bytes.TrimSpace(fields["registration_enabled"])) != "null" {
		if err := requireAuthJSONType("inputs.registration_enabled", fields["registration_enabled"], "boolean"); err != nil {
			return AuthInputSnapshot{}, err
		}
	}
	var google map[string]json.RawMessage
	if err := json.Unmarshal(fields["google"], &google); err != nil || google == nil ||
		requireJSONKeys(google, authInputGoogleKeys) != nil {
		return AuthInputSnapshot{}, errors.New("Auth input snapshot Google object has missing or unknown keys")
	}
	for _, field := range []struct {
		name string
		kind string
	}{
		{"enabled", "boolean"}, {"client_id", "string"}, {"client_secret_version", "string"},
		{"issuer", "string"}, {"jwks_url", "string"}, {"token_url", "string"},
		{"login_redirect_url", "string"}, {"link_redirect_url", "string"}, {"completion_url", "string"},
	} {
		if err := requireAuthJSONType("inputs.google."+field.name, google[field.name], field.kind); err != nil {
			return AuthInputSnapshot{}, err
		}
	}
	var inputs AuthInputSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inputs); err != nil {
		return AuthInputSnapshot{}, errors.New("Auth input snapshot fields have invalid types or names")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return AuthInputSnapshot{}, errors.New("Auth input snapshot has trailing data")
	}
	if err := ValidateAuthInputSnapshot(inputs); err != nil {
		return AuthInputSnapshot{}, err
	}
	return inputs, nil
}

var authInputSnapshotKeys = []string{
	"schema_version", "environment", "target", "source_sha", "config_id", "gcp_project",
	"firestore_database_id", "local_cloud_scope", "auth_service_url", "sync_service_url", "allowed_hosts", "allowed_origins",
	"auth_session_environment", "auth_session_migration", "registration_enabled", "auth_demo_user_id",
	"auth_demo_user_email", "auth_demo_user_role",
	"jwt_secret_version", "google", "config_secret_resource",
}

func authSchemaVersion(raw json.RawMessage) int {
	var version int
	_ = json.Unmarshal(raw, &version)
	return version
}

func jsonKeySetDifference(fields map[string]json.RawMessage, expected []string) (missing, unknown []string) {
	allowed := make(map[string]struct{}, len(expected))
	for _, key := range expected {
		allowed[key] = struct{}{}
		if _, ok := fields[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	return missing, unknown
}

func validateAuthPublicPolicyJSON(fields map[string]json.RawMessage) error {
	for _, field := range []struct{ name, kind string }{
		{"management_scopes", "object_array"}, {"consumer_verifiers", "object_array"},
		{"consumer_public_projection", "object"},
	} {
		if err := requireAuthJSONType("inputs."+field.name, fields[field.name], field.kind); err != nil {
			return err
		}
	}
	var projection map[string]json.RawMessage
	if json.Unmarshal(fields["consumer_public_projection"], &projection) != nil || projection == nil ||
		requireJSONKeys(projection, []string{"issuer", "resource", "management_scopes"}) != nil {
		return errors.New("Auth consumer_public_projection has missing or unknown keys")
	}
	if err := requireAuthJSONType("inputs.consumer_public_projection.issuer", projection["issuer"], "string"); err != nil {
		return err
	}
	if err := requireAuthJSONType("inputs.consumer_public_projection.resource", projection["resource"], "string"); err != nil {
		return err
	}
	var scopes []json.RawMessage
	if json.Unmarshal(projection["management_scopes"], &scopes) != nil || scopes == nil {
		return errors.New("Auth consumer_public_projection management scopes are invalid")
	}
	return nil
}

var authInputGoogleKeys = []string{
	"enabled", "client_id", "client_secret_version", "issuer", "jwks_url", "token_url",
	"login_redirect_url", "link_redirect_url", "completion_url",
}

func requireAuthJSONType(name string, raw json.RawMessage, kind string) error {
	var valid bool
	switch kind {
	case "integer":
		var value int
		valid = string(bytes.TrimSpace(raw)) != "null" && json.Unmarshal(raw, &value) == nil
	case "string":
		var value *string
		valid = json.Unmarshal(raw, &value) == nil && value != nil
	case "boolean":
		value := string(bytes.TrimSpace(raw))
		valid = value == "true" || value == "false"
	case "object":
		var value map[string]json.RawMessage
		valid = json.Unmarshal(raw, &value) == nil && value != nil
	case "string_array":
		var value []string
		valid = json.Unmarshal(raw, &value) == nil && value != nil
	case "object_array":
		var value []map[string]json.RawMessage
		valid = json.Unmarshal(raw, &value) == nil && value != nil
	}
	if !valid {
		return fmt.Errorf("Auth input snapshot %s has an invalid type", name)
	}
	return nil
}

func validateOAuthResourcesJSON(name string, raw json.RawMessage) error {
	var resources []json.RawMessage
	if json.Unmarshal(raw, &resources) != nil || resources == nil {
		return fmt.Errorf("%s has an invalid type", name)
	}
	for i, item := range resources {
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil || fields == nil || requireJSONKeys(fields, []string{
			"id", "kind", "enabled", "uri", "name", "scopes", "allowed_scope_sets", "default_scope_set",
			"verifier_caller_service_account", "verifier_audience",
		}) != nil {
			return fmt.Errorf("%s[%d] has missing or unknown keys", name, i)
		}
		for _, field := range []struct{ name, kind string }{
			{"id", "string"}, {"kind", "string"}, {"enabled", "boolean"}, {"uri", "string"}, {"name", "string"},
			{"verifier_caller_service_account", "string"}, {"verifier_audience", "string"},
		} {
			if err := requireAuthJSONType(name+"."+field.name, fields[field.name], field.kind); err != nil {
				return err
			}
		}
		var scopes []json.RawMessage
		if json.Unmarshal(fields["scopes"], &scopes) != nil || scopes == nil {
			return fmt.Errorf("%s[%d].scopes has an invalid type", name, i)
		}
		for j, scopeRaw := range scopes {
			var scope map[string]json.RawMessage
			if json.Unmarshal(scopeRaw, &scope) != nil || scope == nil || requireJSONKeys(scope, []string{"name", "description"}) != nil ||
				requireAuthJSONType(name+".scopes.name", scope["name"], "string") != nil ||
				requireAuthJSONType(name+".scopes.description", scope["description"], "string") != nil {
				return fmt.Errorf("%s[%d].scopes[%d] has an invalid shape", name, i, j)
			}
		}
		var sets [][]string
		if json.Unmarshal(fields["allowed_scope_sets"], &sets) != nil || sets == nil {
			return fmt.Errorf("%s[%d].allowed_scope_sets has an invalid type", name, i)
		}
		var defaults []string
		if json.Unmarshal(fields["default_scope_set"], &defaults) != nil || defaults == nil {
			return fmt.Errorf("%s[%d].default_scope_set has an invalid type", name, i)
		}
	}
	return nil
}

func validateAuthOAuthResources(file AuthFile) error {
	if len(file.OAuthResources) != 2 {
		return errors.New("Auth OAuth resource registry is incomplete")
	}
	ids, scopeNames := map[string]bool{}, map[string]bool{}
	var project, brief *AuthOAuthResource
	for i := range file.OAuthResources {
		resource := &file.OAuthResources[i]
		if !authOAuthIDPattern.MatchString(resource.ID) || ids[resource.ID] ||
			(resource.Kind != "project-template" && resource.Kind != "fixed") ||
			strings.TrimSpace(resource.Name) == "" || resource.URI == "" || resource.Scopes == nil ||
			resource.AllowedScopeSets == nil || resource.DefaultScopeSet == nil {
			return errors.New("Auth OAuth resource identity or required policy is invalid")
		}
		ids[resource.ID] = true
		declared := map[string]bool{}
		for _, scope := range resource.Scopes {
			if !authOAuthScopePattern.MatchString(scope.Name) || strings.TrimSpace(scope.Description) == "" || scopeNames[scope.Name] {
				return errors.New("Auth OAuth scope declaration is invalid or duplicated")
			}
			declared[scope.Name], scopeNames[scope.Name] = true, true
		}
		accepted := map[string]bool{}
		for _, set := range resource.AllowedScopeSets {
			if len(set) == 0 {
				return errors.New("Auth OAuth accepted scope set is empty")
			}
			seen := map[string]bool{}
			for _, name := range set {
				if !declared[name] || seen[name] {
					return errors.New("Auth OAuth accepted scope set is invalid")
				}
				seen[name] = true
			}
			key := canonicalScopeSetKey(set)
			if accepted[key] {
				return errors.New("Auth OAuth accepted scope sets are duplicated")
			}
			accepted[key] = true
		}
		if len(resource.DefaultScopeSet) > 0 && !accepted[canonicalScopeSetKey(resource.DefaultScopeSet)] {
			return errors.New("Auth OAuth default scope set is not accepted")
		}
		switch resource.Kind {
		case "project-template":
			if resource.ID != "lwc-project" || resource.URI != trimAuthOrigin(file.SyncServiceURL)+"/mcp/projects/{project_id}" ||
				resource.VerifierAudience != "" || resource.VerifierCallerServiceAccount != "" {
				return errors.New("Auth project OAuth resource binding is invalid")
			}
			project = resource
		case "fixed":
			if resource.ID != "daily-brief" || resource.URI != "https://brief.rayer.idv.tw/mcp" ||
				resource.Name != "Daily Brief" || !validAuthOAuthVerifierAudience(file.Environment, resource.VerifierAudience) {
				return errors.New("Auth fixed OAuth resource binding is invalid")
			}
			if resource.Enabled && (resource.VerifierAudience == "" ||
				!authServiceAccountPattern.MatchString(resource.VerifierCallerServiceAccount)) {
				return errors.New("Auth fixed OAuth verifier caller is not configured")
			}
			if !resource.Enabled && resource.VerifierCallerServiceAccount != "" && !authServiceAccountPattern.MatchString(resource.VerifierCallerServiceAccount) {
				return errors.New("Auth fixed OAuth verifier caller is invalid")
			}
			brief = resource
		}
	}
	if project == nil || brief == nil || !project.Enabled ||
		!equalStringSlices(project.ScopesNames(), []string{"mcp:query", "mcp:raw:write"}) ||
		!equalStringSetSlices(project.AllowedScopeSets, [][]string{{"mcp:query"}, {"mcp:query", "mcp:raw:write"}}) ||
		!equalStringSlices(project.DefaultScopeSet, []string{"mcp:query"}) ||
		!equalStringSlices(brief.ScopesNames(), []string{"brief:manage"}) ||
		!equalStringSetSlices(brief.AllowedScopeSets, [][]string{{"brief:manage"}}) || len(brief.DefaultScopeSet) != 0 {
		return errors.New("Auth OAuth resource scope contract is invalid")
	}
	if brief.Scopes[0].Description != "Read and manage Daily Brief reports and annotations in your private account space." {
		return errors.New("Auth Daily Brief consent description is invalid")
	}
	return nil
}

func validateAuthPublicPolicy(scopes []AuthManagementScope, verifiers []AuthConsumerVerifier, projection AuthConsumerPublicProjection,
	environment, issuer, resource string) error {
	if len(scopes) != 2 || projection.Issuer != issuer || projection.Resource != resource ||
		!reflect.DeepEqual(projection.ManagementScopes, scopes) {
		return errors.New("Auth consumer public projection does not match its registered policy")
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if !authOAuthScopePattern.MatchString(scope.ID) || seen[scope.ID] ||
			(scope.ID != "auth:admin" && scope.ID != "lwc:admin") ||
			(scope.Product != "auth" && scope.Product != "lwc") ||
			scope.Resource != resource || scope.GrantMode != "operator-only" ||
			strings.TrimSpace(scope.Name.En) == "" || strings.TrimSpace(scope.Name.ZhTW) == "" ||
			strings.TrimSpace(scope.Description.En) == "" || strings.TrimSpace(scope.Description.ZhTW) == "" ||
			len(scope.GrantorScopes) != 1 || scope.GrantorScopes[0] != "auth:admin" {
			return errors.New("Auth management scope registry is invalid")
		}
		seen[scope.ID] = true
		if (scope.ID == "auth:admin") != (scope.Product == "auth") || (scope.ID == "lwc:admin") != (scope.Product == "lwc") {
			return errors.New("Auth management scope product binding is invalid")
		}
	}
	if !seen["auth:admin"] || !seen["lwc:admin"] {
		return errors.New("Auth management scope registry is incomplete")
	}
	expectedVerifierCount := 1
	if environment == "local" {
		expectedVerifierCount = 0
	}
	if len(verifiers) != expectedVerifierCount {
		return errors.New("Auth consumer verifier registry is invalid")
	}
	for _, verifier := range verifiers {
		if verifier.ID != "lwc-bff" || verifier.Environment != environment || !authServiceAccountPattern.MatchString(verifier.CallerServiceAccount) ||
			!validAuthVerifierAudience(verifier.Audience) || verifier.Resource != resource ||
			!equalStringSlices(verifier.AllowedCredentialKinds, []string{"web-access", "cli-access", "mcp-oauth"}) ||
			!equalStringSet(verifier.AuthorityModes, []string{"legacy-shadow", "central"}) {
			return errors.New("Auth consumer verifier identity is invalid")
		}
	}
	return nil
}

func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	return reflect.DeepEqual(left, right)
}

// ValidateAuthPublicPolicy exposes the strict CaC policy check to the
// pre-materialization source projection validator.
func ValidateAuthPublicPolicy(scopes []AuthManagementScope, verifiers []AuthConsumerVerifier,
	projection AuthConsumerPublicProjection, environment, issuer, resource string) error {
	return validateAuthPublicPolicy(scopes, verifiers, projection, environment, issuer, resource)
}

func validAuthVerifierAudience(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil &&
		(u.Path == "" || u.Path == "/") && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func validAuthOAuthVerifierAudience(environment, audience string) bool {
	switch environment {
	case "local":
		return audience == "" || validateRuntimeOrigin(audience, true) == nil
	case "dev":
		return audience == authDailyBriefDevVerifierAudience
	case "prod":
		return audience == "" || audience == authDailyBriefProdVerifierAudience
	default:
		return false
	}
}

func (r AuthOAuthResource) ScopesNames() []string {
	values := make([]string, len(r.Scopes))
	for i, scope := range r.Scopes {
		values[i] = scope.Name
	}
	return values
}

func trimAuthOrigin(value string) string { return strings.TrimRight(value, "/") }

func canonicalScopeSetKey(values []string) string {
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	return strings.Join(copyValues, " ")
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringSetSlices(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		left[i] = canonicalScopeSetKey(a[i])
	}
	for i := range b {
		right[i] = canonicalScopeSetKey(b[i])
	}
	sort.Strings(left)
	sort.Strings(right)
	return equalStringSlices(left, right)
}

// ValidateAuthInputSnapshot ensures Stage 2 receives a complete immutable
// nonsecret snapshot and exact numeric credential versions.
func ValidateAuthInputSnapshot(inputs AuthInputSnapshot) error {
	if (inputs.SchemaVersion != 1 && inputs.SchemaVersion != 2 && inputs.SchemaVersion != 3) || inputs.Target != "auth" ||
		(inputs.Environment != "dev" && inputs.Environment != "prod") ||
		!authSourceSHAPattern.MatchString(inputs.SourceSHA) || !authConfigIDPattern.MatchString(inputs.ConfigID) ||
		inputs.GCPProject != "llm-wiki-cloud" || inputs.LocalCloudScope != "" ||
		!regexp.MustCompile(`^projects/llm-wiki-cloud/secrets/jwt-secret-(?:dev|prod)/versions/[1-9][0-9]*$`).MatchString(inputs.JWTSecretVersion) ||
		!regexp.MustCompile(`^projects/llm-wiki-cloud/secrets/lwc-auth-app-config-(?:dev|prod)$`).MatchString(inputs.ConfigSecretResource) {
		return errors.New("Auth input snapshot identity is invalid")
	}
	expected := "dev"
	if inputs.Environment == "prod" {
		expected = "prod"
	}
	if !strings.Contains(inputs.JWTSecretVersion, "/jwt-secret-"+expected+"/") ||
		!strings.HasSuffix(inputs.ConfigSecretResource, "-"+expected) {
		return errors.New("Auth input snapshot environment binding is invalid")
	}
	if !inputs.Google.Enabled && inputs.Google != (AuthInputGoogle{}) {
		return errors.New("Auth input snapshot disabled Google settings must be empty")
	}
	if inputs.Google.Enabled && !regexp.MustCompile(`^projects/llm-wiki-cloud/secrets/google-oauth-client-`+expected+`/versions/[1-9][0-9]*$`).MatchString(inputs.Google.ClientSecretVersion) {
		return errors.New("Auth input snapshot Google credential reference is invalid")
	}
	if !inputs.Google.Enabled && inputs.Google.ClientSecretVersion != "" {
		return errors.New("Auth input snapshot disabled Google credential reference must be empty")
	}
	if got, err := AuthInputConfigID(inputs); err != nil || got != inputs.ConfigID {
		return errors.New("Auth input snapshot config_id does not match its source inputs")
	}
	file := AuthFile{
		SchemaVersion: inputs.SchemaVersion, Target: "auth", Environment: inputs.Environment, ConfigID: inputs.ConfigID,
		GCPProject: inputs.GCPProject, FirestoreDatabaseID: inputs.FirestoreDatabaseID,
		LocalCloudScope: inputs.LocalCloudScope, AuthServiceURL: inputs.AuthServiceURL,
		SyncServiceURL: inputs.SyncServiceURL,
		AllowedHosts:   inputs.AllowedHosts, AllowedOrigins: inputs.AllowedOrigins,
		AuthSessionEnvironment: inputs.AuthSessionEnvironment, AuthSessionMigration: inputs.AuthSessionMigration,
		RegistrationEnabled: inputs.RegistrationEnabled, AuthDemoUserID: inputs.AuthDemoUserID,
		AuthDemoUserEmail: inputs.AuthDemoUserEmail, AuthDemoUserRole: inputs.AuthDemoUserRole,
		JWTSecret: "nonsecret-validation-placeholder",
		Google: AuthFileGoogle{
			Enabled: inputs.Google.Enabled, ClientID: inputs.Google.ClientID,
			ClientSecret: valueWhen(inputs.Google.Enabled, "nonsecret-validation-placeholder"),
			Issuer:       inputs.Google.Issuer, JWKSURL: inputs.Google.JWKSURL, TokenURL: inputs.Google.TokenURL,
			LoginRedirectURL: inputs.Google.LoginRedirectURL, LinkRedirectURL: inputs.Google.LinkRedirectURL,
			CompletionURL: inputs.Google.CompletionURL,
		},
		OAuthResources:   inputs.OAuthResources,
		ManagementScopes: inputs.ManagementScopes, ConsumerVerifiers: inputs.ConsumerVerifiers,
		ConsumerPublicProjection: inputs.ConsumerPublicProjection,
	}
	return validateAuthFile(file)
}

func valueWhen(enabled bool, value string) string {
	if !enabled {
		return ""
	}
	return value
}

func authConsumerProjectionOrZero(projection *AuthConsumerPublicProjection) AuthConsumerPublicProjection {
	if projection == nil {
		return AuthConsumerPublicProjection{}
	}
	return *projection
}

func authConsumerVerifiersOrEmpty(verifiers *[]AuthConsumerVerifier) []AuthConsumerVerifier {
	if verifiers == nil {
		return nil
	}
	return *verifiers
}

var authFileKeys = []string{
	"schema_version", "target", "environment", "config_id", "gcp_project", "firestore_database_id",
	"local_cloud_scope", "auth_service_url", "sync_service_url", "allowed_hosts", "allowed_origins", "auth_session_environment",
	"auth_session_migration", "registration_enabled", "auth_demo_user_id", "auth_demo_user_email",
	"auth_demo_user_role", "jwt_secret", "google",
}

func authFileKeysForVersion(version int) []string {
	if version == 2 {
		return append(append([]string(nil), authFileKeys...), "oauth_resources")
	} else if version == 3 {
		return append(append([]string(nil), authFileKeys...), "oauth_resources", "management_scopes", "consumer_verifiers", "consumer_public_projection")
	}
	return authFileKeys
}

// LoadAuthFile reads and validates the required Auth configuration file. The
// file is the only source for migrated application settings; PORT remains the
// platform listener input.
func LoadAuthFile(path string) (Config, error) {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return Config{}, errors.New("Auth config path must be an absolute file path")
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("Auth config file is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Config{}, errors.New("Auth config path is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxAuthConfigBytes+1))
	if err != nil || len(data) > MaxAuthConfigBytes {
		return Config{}, errors.New("Auth config file size is invalid")
	}
	file, err := DecodeAuthFile(data)
	clear(data)
	if err != nil {
		return Config{}, err
	}
	if file.LocalCloudScope != "" {
		legacyEnv := viper.New()
		legacyEnv.Set("dev_jwt", os.Getenv("DEV_JWT"))
		if legacyEnv.GetBool("dev_jwt") || strings.TrimSpace(os.Getenv("LOCAL_DATA_DIR")) != "" {
			return Config{}, errors.New("local cloud does not allow DEV_JWT or LOCAL_DATA_DIR")
		}
	}
	port := "8080"
	if override, ok := os.LookupEnv("PORT"); ok && strings.TrimSpace(override) != "" {
		port = strings.TrimSpace(override)
		if !authConfigPortPattern.MatchString(port) {
			return Config{}, errors.New("invalid PORT")
		}
		portNumber, _ := strconv.Atoi(port)
		if portNumber < 1 || portNumber > 65535 {
			return Config{}, errors.New("invalid PORT")
		}
	}
	return Config{
		ConfigID: file.ConfigID, ConfigSchemaVersion: file.SchemaVersion,
		AuthEnvironment: file.Environment,
		GCPProject:      file.GCPProject, FirestoreDatabaseID: file.FirestoreDatabaseID,
		LocalCloudScope: file.LocalCloudScope, Port: port, JWTSecret: file.JWTSecret,
		AuthServiceURL: file.AuthServiceURL, SyncServiceURL: file.SyncServiceURL,
		AllowedHosts: file.AllowedHosts, AllowedOrigins: file.AllowedOrigins,
		AuthSessionEnvironment: file.AuthSessionEnvironment, AuthSessionMigration: file.AuthSessionMigration,
		RegistrationEnabled: file.RegistrationEnabled, AuthDemoUserID: file.AuthDemoUserID,
		AuthDemoUserEmail: file.AuthDemoUserEmail, AuthDemoUserRole: file.AuthDemoUserRole,
		GoogleClientID: file.Google.ClientID, GoogleClientSecret: file.Google.ClientSecret,
		GoogleIssuer: file.Google.Issuer, GoogleJWKSURL: file.Google.JWKSURL, GoogleTokenURL: file.Google.TokenURL,
		GoogleLoginRedirectURL: file.Google.LoginRedirectURL, GoogleLinkRedirectURL: file.Google.LinkRedirectURL,
		GoogleCompletionURL: file.Google.CompletionURL,
		AuthOAuthResources:  runtimeAuthOAuthResources(file),
		ManagementScopes:    file.ManagementScopes, AuthConsumerVerifiers: authConsumerVerifiersOrEmpty(file.ConsumerVerifiers),
		AuthConsumerPublicProjection: authConsumerProjectionOrZero(file.ConsumerPublicProjection),
	}, nil
}

func runtimeAuthOAuthResources(file AuthFile) []AuthOAuthResource {
	if file.SchemaVersion >= 2 {
		return file.OAuthResources
	}
	return LegacyAuthOAuthResources(file.SyncServiceURL)
}

// LegacyAuthOAuthResources preserves the pre-registry project policy for
// retained schema-1 files and direct constructors that predate schema 2.
func LegacyAuthOAuthResources(syncOrigin string) []AuthOAuthResource {
	return []AuthOAuthResource{{
		ID: "lwc-project", Kind: "project-template", Enabled: true,
		URI: trimAuthOrigin(syncOrigin) + "/mcp/projects/{project_id}", Name: "LWC Project",
		Scopes:           []AuthOAuthScope{{Name: "mcp:query", Description: "Query project knowledge."}, {Name: "mcp:raw:write", Description: "Add permitted raw sources."}},
		AllowedScopeSets: [][]string{{"mcp:query"}, {"mcp:query", "mcp:raw:write"}}, DefaultScopeSet: []string{"mcp:query"},
	}}
}

// DecodeAuthFile validates the complete materialized schema before projecting
// it into the runtime config. Diagnostics deliberately omit values.
func DecodeAuthFile(data []byte) (AuthFile, error) {
	if len(data) == 0 || len(data) > MaxAuthConfigBytes {
		return AuthFile{}, errors.New("Auth config size is invalid")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return AuthFile{}, errors.New("Auth config contains malformed or duplicate JSON keys")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return AuthFile{}, errors.New("Auth config document is malformed")
	}
	version := authSchemaVersion(fields["schema_version"])
	expectedKeys := authFileKeysForVersion(version)
	if err := requireJSONKeys(fields, expectedKeys); err != nil {
		missing, unknown := jsonKeySetDifference(fields, expectedKeys)
		return AuthFile{}, fmt.Errorf("Auth config has missing or unknown keys (missing=%s unknown=%s)", strings.Join(missing, ","), strings.Join(unknown, ","))
	}
	if err := validateAuthFileTypes(fields); err != nil {
		return AuthFile{}, err
	}
	var file AuthFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return AuthFile{}, errors.New("Auth config fields have invalid types or names")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return AuthFile{}, errors.New("Auth config has trailing data")
	}
	if err := validateAuthFile(file); err != nil {
		return AuthFile{}, err
	}
	return file, nil
}

func validateAuthFileTypes(fields map[string]json.RawMessage) error {
	for _, field := range []struct {
		name     string
		kind     string
		nullable bool
	}{
		{"schema_version", "integer", false}, {"target", "string", false}, {"environment", "string", false},
		{"config_id", "string", false}, {"gcp_project", "string", false}, {"firestore_database_id", "string", false},
		{"local_cloud_scope", "string", false}, {"auth_service_url", "string", false},
		{"sync_service_url", "string", false},
		{"allowed_hosts", "string_array", false}, {"allowed_origins", "string_array", false},
		{"auth_session_environment", "string", false}, {"auth_session_migration", "string", false},
		{"registration_enabled", "boolean", true}, {"auth_demo_user_id", "string", false},
		{"auth_demo_user_email", "string", false}, {"auth_demo_user_role", "string", false},
		{"jwt_secret", "string", false}, {"google", "object", false},
	} {
		if err := requireAuthFileJSONType("auth."+field.name, fields[field.name], field.kind, field.nullable); err != nil {
			return err
		}
	}
	version := authSchemaVersion(fields["schema_version"])
	if version >= 2 {
		if err := validateOAuthResourcesJSON("auth.oauth_resources", fields["oauth_resources"]); err != nil {
			return err
		}
	}
	if version == 3 {
		if err := validateAuthPublicPolicyJSON(fields); err != nil {
			return err
		}
	}
	var google map[string]json.RawMessage
	if err := json.Unmarshal(fields["google"], &google); err != nil || google == nil {
		return errors.New("Auth config google object is invalid")
	}
	if err := requireJSONKeys(google, []string{
		"enabled", "client_id", "client_secret", "issuer", "jwks_url", "token_url",
		"login_redirect_url", "link_redirect_url", "completion_url",
	}); err != nil {
		return errors.New("Auth config google object has missing or unknown keys")
	}
	for _, field := range []string{"enabled", "client_id", "client_secret", "issuer", "jwks_url", "token_url", "login_redirect_url", "link_redirect_url", "completion_url"} {
		kind := "string"
		if field == "enabled" {
			kind = "boolean"
		}
		if err := requireAuthFileJSONType("auth.google."+field, google[field], kind, false); err != nil {
			return err
		}
	}
	return nil
}

func validateAuthFile(file AuthFile) error {
	if (file.SchemaVersion != 1 && file.SchemaVersion != 2 && file.SchemaVersion != 3) || file.Target != "auth" ||
		(file.Environment != "local" && file.Environment != "dev" && file.Environment != "prod") ||
		!authConfigIDPattern.MatchString(file.ConfigID) {
		return errors.New("Auth config identity is invalid")
	}
	if strings.TrimSpace(file.GCPProject) == "" || strings.TrimSpace(file.AuthSessionEnvironment) == "" ||
		strings.TrimSpace(file.JWTSecret) == "" {
		return errors.New("Auth config required value is empty")
	}
	if file.AuthSessionMigration != "disabled" && file.AuthSessionMigration != "legacy_read_through" {
		return errors.New("Auth config auth_session_migration is invalid")
	}
	if file.AuthDemoUserID != "" && !authDemoUserIDPattern.MatchString(file.AuthDemoUserID) {
		return errors.New("Auth config auth_demo_user_id is invalid")
	}
	if file.AuthDemoUserID != "" && (file.AuthDemoUserEmail != strings.TrimSpace(file.AuthDemoUserEmail) ||
		!authDemoEmailPattern.MatchString(file.AuthDemoUserEmail) ||
		strings.ToLower(file.AuthDemoUserEmail) != file.AuthDemoUserEmail ||
		!authDemoRolePattern.MatchString(file.AuthDemoUserRole) || strings.EqualFold(file.AuthDemoUserRole, "admin")) {
		return errors.New("Auth config Demo identity is invalid")
	}
	localMode := file.Environment == "local"
	scope, err := localcloud.Parse(file.LocalCloudScope)
	if err != nil || (localMode && scope == "") || (!localMode && scope != "") {
		return errors.New("Auth config local_cloud_scope is invalid")
	}
	if err := validateRuntimeURL(file.AuthServiceURL, localMode); err != nil {
		return errors.New("Auth config auth_service_url is invalid")
	}
	if err := validateRuntimeOrigin(file.SyncServiceURL, localMode); err != nil {
		return errors.New("Auth config sync_service_url is invalid")
	}
	if file.AllowedHosts == nil || file.AllowedOrigins == nil || len(file.AllowedHosts) == 0 || len(file.AllowedOrigins) == 0 {
		return errors.New("Auth config allowlists are invalid")
	}
	if file.SchemaVersion == 1 {
		if len(file.OAuthResources) != 0 {
			return errors.New("Auth schema-1 config must not contain OAuth resource policy")
		}
	} else if err := validateAuthOAuthResources(file); err != nil {
		return err
	}
	if file.SchemaVersion < 3 {
		if len(file.ManagementScopes) != 0 || file.ConsumerVerifiers != nil ||
			file.ConsumerPublicProjection != nil {
			return errors.New("Auth legacy config must not contain consumer policy")
		}
	} else if file.ConsumerPublicProjection == nil || file.ConsumerVerifiers == nil {
		return errors.New("Auth consumer public projection is missing")
	} else if err := validateAuthPublicPolicy(file.ManagementScopes, *file.ConsumerVerifiers,
		*file.ConsumerPublicProjection, file.Environment, file.AuthServiceURL, trimAuthOrigin(file.SyncServiceURL)); err != nil {
		return err
	}
	for _, host := range file.AllowedHosts {
		if strings.TrimSpace(host) == "" || strings.Contains(host, "*") || strings.ContainsAny(host, " /\\\r\n\t") ||
			(localMode && host != "localhost" && host != "127.0.0.1") {
			return errors.New("Auth config allowed_hosts is invalid")
		}
	}
	for _, origin := range file.AllowedOrigins {
		if err := validateOrigin(origin); err != nil {
			return errors.New("Auth config allowed_origins is invalid")
		}
		u, _ := url.Parse(origin)
		if localMode && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1")) {
			return errors.New("Auth config local allowed_origins must use loopback")
		}
	}
	if localMode {
		if file.GCPProject != "llm-wiki-cloud" || file.FirestoreDatabaseID != "llm-wiki-cloud-local" {
			return errors.New("Auth config local resource identity is invalid")
		}
		decoded, err := hex.DecodeString(file.JWTSecret)
		if err != nil || len(decoded) != 32 {
			clear(decoded)
			return errors.New("Auth config local jwt_secret is invalid")
		}
		clear(decoded)
	} else if file.FirestoreDatabaseID == "" {
		// Empty selects Firestore's established default database.
	}
	google := file.Google
	googleValues := []string{google.ClientID, google.ClientSecret, google.Issuer, google.JWKSURL, google.TokenURL,
		google.LoginRedirectURL, google.LinkRedirectURL, google.CompletionURL}
	if !google.Enabled {
		for _, value := range googleValues {
			if value != "" {
				return errors.New("Auth config disabled Google settings must be empty")
			}
		}
		return nil
	}
	if err := ValidateGoogleConfig(google.ClientID, google.ClientSecret, google.Issuer, google.JWKSURL, google.TokenURL,
		google.LoginRedirectURL, google.LinkRedirectURL, google.CompletionURL,
		GoogleRuntimeValidation{AuthServiceURL: file.AuthServiceURL, AllowedOrigins: file.AllowedOrigins}); err != nil {
		return fmt.Errorf("Auth config Google settings are invalid: %w", err)
	}
	return nil
}

func validateRuntimeOrigin(raw string, local bool) error {
	if err := validateRuntimeURL(raw, local); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid origin")
	}
	return nil
}
