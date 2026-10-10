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
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/viper"

	"github.com/rayer/llm-wiki-bff/internal/localcloud"
)

const MaxAuthConfigBytes = 64 << 10

var authConfigIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var authSourceSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var authConfigPortPattern = regexp.MustCompile(`^[0-9]{1,5}$`)
var authDemoUserIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var authDemoEmailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
var authDemoRolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type AuthFile struct {
	SchemaVersion          int            `json:"schema_version"`
	Target                 string         `json:"target"`
	Environment            string         `json:"environment"`
	ConfigID               string         `json:"config_id"`
	GCPProject             string         `json:"gcp_project"`
	FirestoreDatabaseID    string         `json:"firestore_database_id"`
	LocalCloudScope        string         `json:"local_cloud_scope"`
	AuthServiceURL         string         `json:"auth_service_url"`
	SyncServiceURL         string         `json:"sync_service_url"`
	AllowedHosts           []string       `json:"allowed_hosts"`
	AllowedOrigins         []string       `json:"allowed_origins"`
	AuthSessionEnvironment string         `json:"auth_session_environment"`
	AuthSessionMigration   string         `json:"auth_session_migration"`
	RegistrationEnabled    *bool          `json:"registration_enabled"`
	AuthDemoUserID         string         `json:"auth_demo_user_id"`
	AuthDemoUserEmail      string         `json:"auth_demo_user_email"`
	AuthDemoUserRole       string         `json:"auth_demo_user_role"`
	JWTSecret              string         `json:"jwt_secret"`
	Google                 AuthFileGoogle `json:"google"`
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
	SchemaVersion          int             `json:"schema_version"`
	Environment            string          `json:"environment"`
	Target                 string          `json:"target"`
	SourceSHA              string          `json:"source_sha"`
	ConfigID               string          `json:"config_id"`
	GCPProject             string          `json:"gcp_project"`
	FirestoreDatabaseID    string          `json:"firestore_database_id"`
	LocalCloudScope        string          `json:"local_cloud_scope"`
	AuthServiceURL         string          `json:"auth_service_url"`
	SyncServiceURL         string          `json:"sync_service_url"`
	AllowedHosts           []string        `json:"allowed_hosts"`
	AllowedOrigins         []string        `json:"allowed_origins"`
	AuthSessionEnvironment string          `json:"auth_session_environment"`
	AuthSessionMigration   string          `json:"auth_session_migration"`
	RegistrationEnabled    *bool           `json:"registration_enabled"`
	AuthDemoUserID         string          `json:"auth_demo_user_id"`
	AuthDemoUserEmail      string          `json:"auth_demo_user_email"`
	AuthDemoUserRole       string          `json:"auth_demo_user_role"`
	JWTSecretVersion       string          `json:"jwt_secret_version"`
	Google                 AuthInputGoogle `json:"google"`
	ConfigSecretResource   string          `json:"config_secret_resource"`
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
	if err := requireJSONKeys(fields, authInputSnapshotKeys); err != nil {
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
	}
	if !valid {
		return fmt.Errorf("Auth input snapshot %s has an invalid type", name)
	}
	return nil
}

// ValidateAuthInputSnapshot ensures Stage 2 receives a complete immutable
// nonsecret snapshot and exact numeric credential versions.
func ValidateAuthInputSnapshot(inputs AuthInputSnapshot) error {
	if inputs.SchemaVersion != 1 || inputs.Target != "auth" ||
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
		SchemaVersion: 1, Target: "auth", Environment: inputs.Environment, ConfigID: inputs.ConfigID,
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
	}
	return validateAuthFile(file)
}

func valueWhen(enabled bool, value string) string {
	if !enabled {
		return ""
	}
	return value
}

var authFileKeys = []string{
	"schema_version", "target", "environment", "config_id", "gcp_project", "firestore_database_id",
	"local_cloud_scope", "auth_service_url", "sync_service_url", "allowed_hosts", "allowed_origins", "auth_session_environment",
	"auth_session_migration", "registration_enabled", "auth_demo_user_id", "auth_demo_user_email",
	"auth_demo_user_role", "jwt_secret", "google",
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
		GCPProject: file.GCPProject, FirestoreDatabaseID: file.FirestoreDatabaseID,
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
	}, nil
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
	if err := requireJSONKeys(fields, authFileKeys); err != nil {
		return AuthFile{}, errors.New("Auth config has missing or unknown keys")
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
		if err := requireBFFJSONType("auth."+field.name, fields[field.name], field.kind, field.nullable); err != nil {
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
		if err := requireBFFJSONType("auth.google."+field, google[field], kind, false); err != nil {
			return err
		}
	}
	return nil
}

func validateAuthFile(file AuthFile) error {
	if file.SchemaVersion != 1 || file.Target != "auth" ||
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
