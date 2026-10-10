package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/config"
	secretmanager "google.golang.org/api/secretmanager/v1"
)

const authDocumentPath = "auth/auth.json"

var authSecretReferencePattern = regexp.MustCompile(`^projects/[A-Za-z0-9.-]+/secrets/[A-Za-z0-9_-]+/versions/(?:latest|[1-9][0-9]*)$`)
var authNumericSecretReferencePattern = regexp.MustCompile(`^projects/[A-Za-z0-9.-]+/secrets/[A-Za-z0-9_-]+/versions/[1-9][0-9]*$`)
var authOutputSecretPattern = regexp.MustCompile(`^projects/llm-wiki-cloud/secrets/lwc-auth-app-config-(?:dev|prod)$`)

type authSourceProjection struct {
	SchemaVersion          int              `json:"schema_version"`
	Environment            string           `json:"environment"`
	Target                 string           `json:"target"`
	GCPProject             string           `json:"gcp_project"`
	FirestoreDatabaseID    string           `json:"firestore_database_id"`
	LocalCloudScope        string           `json:"local_cloud_scope"`
	AuthServiceURL         string           `json:"auth_service_url"`
	SyncServiceURL         string           `json:"sync_service_url"`
	AllowedHosts           []string         `json:"allowed_hosts"`
	AllowedOrigins         []string         `json:"allowed_origins"`
	AuthSessionEnvironment string           `json:"auth_session_environment"`
	AuthSessionMigration   string           `json:"auth_session_migration"`
	RegistrationEnabled    *bool            `json:"registration_enabled"`
	AuthDemoUserID         string           `json:"auth_demo_user_id"`
	AuthDemoUserEmail      string           `json:"auth_demo_user_email"`
	AuthDemoUserRole       string           `json:"auth_demo_user_role"`
	JWTSecretReference     string           `json:"jwt_secret_reference"`
	Google                 authSourceGoogle `json:"google"`
	ConfigSecretResource   string           `json:"config_secret_resource"`
}

type authSourceGoogle struct {
	Enabled               bool   `json:"enabled"`
	ClientID              string `json:"client_id"`
	ClientSecretReference string `json:"client_secret_reference"`
	Issuer                string `json:"issuer"`
	JWKSURL               string `json:"jwks_url"`
	TokenURL              string `json:"token_url"`
	LoginRedirectURL      string `json:"login_redirect_url"`
	LinkRedirectURL       string `json:"link_redirect_url"`
	CompletionURL         string `json:"completion_url"`
}

type secretVersionResolver interface {
	ResolveVersion(context.Context, string) (string, error)
}

type authFileManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Target        string `json:"target"`
	Environment   string `json:"environment"`
	ConfigID      string `json:"config_id"`
	SourceSHA     string `json:"source_sha"`
}

func (r googleSecretReader) ResolveVersion(ctx context.Context, resource string) (string, error) {
	if !authSecretReferencePattern.MatchString(resource) {
		return "", errors.New("Auth credential reference is invalid")
	}
	parts := strings.Split(resource, "/")
	requestedProject, secretName, version := parts[1], parts[3], parts[5]
	parent := strings.Join(parts[:4], "/")
	var selected *secretmanager.SecretVersion
	if version == "latest" {
		pageToken := ""
		for {
			response, err := r.service.Projects.Secrets.Versions.List(parent).PageSize(1000).PageToken(pageToken).Context(ctx).Do()
			if err != nil {
				return "", err
			}
			for _, candidate := range response.Versions {
				if candidate == nil || !secretVersionParentMatches(candidate.Name, requestedProject, secretName) {
					continue
				}
				candidateParts := strings.Split(candidate.Name, "/")
				candidateID := candidateParts[5]
				candidateNumber, parseErr := strconv.ParseUint(candidateID, 10, 64)
				if parseErr != nil || candidateID == "" {
					return "", errors.New("Auth credential metadata omitted a numeric version")
				}
				if selected == nil {
					selected = candidate
					continue
				}
				selectedNameParts := strings.Split(selected.Name, "/")
				selectedNumber, selectedErr := strconv.ParseUint(selectedNameParts[len(selectedNameParts)-1], 10, 64)
				if selectedErr != nil || candidateNumber > selectedNumber {
					selected = candidate
				}
			}
			pageToken = response.NextPageToken
			if pageToken == "" {
				break
			}
		}
		if selected == nil {
			return "", errors.New("Auth credential has no versions")
		}
	} else {
		response, err := r.service.Projects.Secrets.Versions.Get(resource).Context(ctx).Do()
		if err != nil {
			return "", err
		}
		selected = response
	}
	if selected.State != "ENABLED" {
		return "", errors.New("selected Auth credential version is not enabled")
	}
	nameParts := strings.Split(selected.Name, "/")
	if len(nameParts) != 6 || nameParts[0] != "projects" || nameParts[2] != "secrets" ||
		nameParts[3] != secretName || nameParts[4] != "versions" {
		return "", errors.New("Auth credential metadata identity is invalid")
	}
	if _, err := strconv.ParseUint(nameParts[5], 10, 64); err != nil || nameParts[5] == "0" {
		return "", errors.New("Auth credential metadata omitted a numeric version")
	}
	if nameParts[1] != requestedProject {
		identityReader, ok := interface{}(r).(projectIdentityReader)
		if !ok {
			return "", errors.New("Auth credential project identity is unavailable")
		}
		identity, err := identityReader.ProjectIdentity(ctx, requestedProject)
		if err != nil || !projectIdentityMatchesRequest(requestedProject, identity) ||
			(nameParts[1] != identity.ProjectID && nameParts[1] != identity.ProjectNumber) {
			return "", errors.New("Auth credential metadata project does not match the selected project")
		}
	}
	return strings.Join([]string{"projects", requestedProject, "secrets", secretName, "versions", nameParts[5]}, "/"), nil
}

func secretVersionParentMatches(name, project, secret string) bool {
	parts := strings.Split(name, "/")
	return len(parts) == 6 && parts[0] == "projects" &&
		(parts[1] == project || projectNumberPattern.MatchString(parts[1])) &&
		parts[2] == "secrets" && parts[3] == secret && parts[4] == "versions"
}

func decodeAuthSourceProjection(data []byte, environment string) (authSourceProjection, error) {
	if err := config.RejectDuplicateJSONKeys(data); err != nil {
		return authSourceProjection{}, errors.New("selected Auth SSOT is malformed or contains duplicate keys")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return authSourceProjection{}, errors.New("selected Auth SSOT is malformed")
	}
	required := []string{
		"schema_version", "environment", "target", "gcp_project", "firestore_database_id", "local_cloud_scope",
		"auth_service_url", "sync_service_url", "allowed_hosts", "allowed_origins", "auth_session_environment",
		"auth_session_migration", "registration_enabled", "auth_demo_user_id", "jwt_secret_reference",
		"auth_demo_user_email", "auth_demo_user_role", "google", "config_secret_resource",
	}
	if len(fields) != len(required) {
		return authSourceProjection{}, errors.New("selected Auth SSOT has an invalid shape")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return authSourceProjection{}, fmt.Errorf("selected Auth SSOT is missing %s", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var projection authSourceProjection
	if err := decoder.Decode(&projection); err != nil {
		return authSourceProjection{}, errors.New("selected Auth SSOT has an invalid type")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return authSourceProjection{}, errors.New("selected Auth SSOT has trailing data")
	}
	var googleFields map[string]json.RawMessage
	if json.Unmarshal(fields["google"], &googleFields) != nil || googleFields == nil || len(googleFields) != 9 {
		return authSourceProjection{}, errors.New("selected Auth SSOT Google object has an invalid shape")
	}
	for _, key := range []string{"enabled", "client_id", "client_secret_reference", "issuer", "jwks_url", "token_url", "login_redirect_url", "link_redirect_url", "completion_url"} {
		if _, ok := googleFields[key]; !ok {
			return authSourceProjection{}, errors.New("selected Auth SSOT Google object is incomplete")
		}
	}
	if err := validateAuthSourceProjection(projection, environment); err != nil {
		return authSourceProjection{}, err
	}
	return projection, nil
}

func validateAuthSourceProjection(p authSourceProjection, environment string) error {
	if p.SchemaVersion != 1 || p.Target != "auth" || p.Environment != environment ||
		(p.Environment != "local" && p.Environment != "dev" && p.Environment != "prod") ||
		strings.TrimSpace(p.GCPProject) == "" || p.AllowedHosts == nil || p.AllowedOrigins == nil {
		return errors.New("selected Auth SSOT identity or required value is invalid")
	}
	file := config.AuthFile{
		SchemaVersion: 1, Target: "auth", Environment: p.Environment, ConfigID: "sha256:" + strings.Repeat("0", 64),
		GCPProject: p.GCPProject, FirestoreDatabaseID: p.FirestoreDatabaseID, LocalCloudScope: p.LocalCloudScope,
		AuthServiceURL: p.AuthServiceURL, SyncServiceURL: p.SyncServiceURL,
		AllowedHosts: p.AllowedHosts, AllowedOrigins: p.AllowedOrigins,
		AuthSessionEnvironment: p.AuthSessionEnvironment, AuthSessionMigration: p.AuthSessionMigration,
		RegistrationEnabled: p.RegistrationEnabled, AuthDemoUserID: p.AuthDemoUserID,
		AuthDemoUserEmail: p.AuthDemoUserEmail, AuthDemoUserRole: p.AuthDemoUserRole,
		JWTSecret: "synthetic-validation-value",
	}
	if p.Environment == "local" {
		if p.JWTSecretReference == "" || !filepath.IsAbs(p.JWTSecretReference) || p.ConfigSecretResource != "" {
			return errors.New("selected local Auth key reference is invalid")
		}
		file.JWTSecret = strings.Repeat("a", 64)
	} else {
		if p.LocalCloudScope != "" || !authOutputSecretPattern.MatchString(p.ConfigSecretResource) ||
			!authSecretReferencePattern.MatchString(p.JWTSecretReference) {
			return errors.New("selected cloud Auth credential or output resource reference is invalid")
		}
		expected := "dev"
		if p.Environment == "prod" {
			expected = "prod"
		}
		if !strings.Contains(p.JWTSecretReference, "/jwt-secret-"+expected+"/") || !strings.HasSuffix(p.ConfigSecretResource, "-"+expected) {
			return errors.New("selected Auth credential references do not match the environment")
		}
	}
	if p.Google.Enabled {
		if p.Environment == "local" || !authSecretReferencePattern.MatchString(p.Google.ClientSecretReference) {
			return errors.New("selected Auth Google credential reference is invalid")
		}
		file.Google = config.AuthFileGoogle{
			Enabled: true, ClientID: p.Google.ClientID, ClientSecret: "synthetic-validation-value",
			Issuer: p.Google.Issuer, JWKSURL: p.Google.JWKSURL, TokenURL: p.Google.TokenURL,
			LoginRedirectURL: p.Google.LoginRedirectURL, LinkRedirectURL: p.Google.LinkRedirectURL,
			CompletionURL: p.Google.CompletionURL,
		}
	} else if p.Google != (authSourceGoogle{}) {
		return errors.New("selected disabled Auth Google settings must be empty")
	}
	data, err := json.Marshal(file)
	if err != nil {
		return errors.New("encode selected Auth SSOT validation")
	}
	if _, err := config.DecodeAuthFile(data); err != nil {
		return fmt.Errorf("selected Auth SSOT values are invalid: %w", err)
	}
	if p.Environment != "local" && p.Google.Enabled {
		expected := "dev"
		if p.Environment == "prod" {
			expected = "prod"
		}
		if !regexp.MustCompile(`^projects/llm-wiki-cloud/secrets/google-oauth-client-` + expected + `/versions/[1-9][0-9]*$`).MatchString(p.Google.ClientSecretReference) {
			return errors.New("selected Auth Google credential reference does not match the environment")
		}
	}
	return nil
}

func prepareAuthInputs(ctx context.Context, projection authSourceProjection, sourceSHA string, newReader readerFactory) (config.AuthInputSnapshot, error) {
	if projection.Environment == "local" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sourceSHA) {
		return config.AuthInputSnapshot{}, errors.New("cloud Auth input snapshot source identity is invalid")
	}
	reader, err := newReader(ctx)
	if err != nil {
		return config.AuthInputSnapshot{}, fmt.Errorf("initialize Auth metadata resolver failed: %s", googleAPIErrorSummary(err))
	}
	resolver, ok := reader.(secretVersionResolver)
	if !ok {
		return config.AuthInputSnapshot{}, errors.New("Auth metadata resolver does not support numeric version lookup")
	}
	jwtVersion, err := resolver.ResolveVersion(ctx, projection.JWTSecretReference)
	if err != nil || !authNumericSecretReferencePattern.MatchString(jwtVersion) {
		return config.AuthInputSnapshot{}, errors.New("selected Auth JWT reference could not be pinned to an enabled numeric version")
	}
	google := config.AuthInputGoogle{}
	if projection.Google.Enabled {
		google = config.AuthInputGoogle{
			Enabled: true, ClientID: projection.Google.ClientID, Issuer: projection.Google.Issuer,
			JWKSURL: projection.Google.JWKSURL, TokenURL: projection.Google.TokenURL,
			LoginRedirectURL: projection.Google.LoginRedirectURL, LinkRedirectURL: projection.Google.LinkRedirectURL,
			CompletionURL: projection.Google.CompletionURL,
		}
		google.ClientSecretVersion, err = resolver.ResolveVersion(ctx, projection.Google.ClientSecretReference)
		if err != nil || !authNumericSecretReferencePattern.MatchString(google.ClientSecretVersion) {
			return config.AuthInputSnapshot{}, errors.New("selected Auth Google reference could not be pinned to an enabled numeric version")
		}
	}
	inputs := config.AuthInputSnapshot{
		SchemaVersion: 1, Environment: projection.Environment, Target: "auth", SourceSHA: sourceSHA,
		GCPProject: projection.GCPProject, FirestoreDatabaseID: projection.FirestoreDatabaseID,
		LocalCloudScope: projection.LocalCloudScope, AuthServiceURL: projection.AuthServiceURL,
		SyncServiceURL: projection.SyncServiceURL,
		AllowedHosts:   projection.AllowedHosts, AllowedOrigins: projection.AllowedOrigins,
		AuthSessionEnvironment: projection.AuthSessionEnvironment, AuthSessionMigration: projection.AuthSessionMigration,
		RegistrationEnabled: projection.RegistrationEnabled, AuthDemoUserID: projection.AuthDemoUserID,
		AuthDemoUserEmail: projection.AuthDemoUserEmail, AuthDemoUserRole: projection.AuthDemoUserRole,
		JWTSecretVersion: jwtVersion, Google: google, ConfigSecretResource: projection.ConfigSecretResource,
	}
	inputs.ConfigID, err = config.AuthInputConfigID(inputs)
	if err != nil {
		return config.AuthInputSnapshot{}, err
	}
	if err := config.ValidateAuthInputSnapshot(inputs); err != nil {
		return config.AuthInputSnapshot{}, err
	}
	return inputs, nil
}

func invalidateAuthOutput(output, environment string) error {
	output = filepath.Clean(output)
	if environment == "local" {
		authDir := filepath.Join(output, "auth")
		if info, err := os.Lstat(authDir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("local Auth config directory must be a real directory")
		}
		for _, name := range []string{"auth.json", "success.json"} {
			if err := os.Remove(filepath.Join(authDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.New("invalidate previous local Auth config")
			}
		}
		return nil
	}
	for _, name := range []string{"auth-inputs.json", "auth-source.json"} {
		if err := os.Remove(filepath.Join(output, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("invalidate previous Auth input projection")
		}
	}
	return nil
}

func writeAuthSourceProjection(output string, projection authSourceProjection) error {
	data, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return errors.New("encode Auth source projection")
	}
	defer clear(data)
	data = append(data, '\n')
	if err := os.MkdirAll(output, 0o700); err != nil {
		return fmt.Errorf("create Auth source directory: %w", err)
	}
	if err := writePrivateAtomic(filepath.Join(output, "auth-source.json"), data); err != nil {
		return fmt.Errorf("write Auth source projection: %w", err)
	}
	fmt.Printf("prepared Auth source projection environment=%s target=auth\n", projection.Environment)
	return nil
}

func runMaterializeAuth(ctx context.Context, inputsPath, output string, newReader readerFactory) error {
	inputs, err := loadAuthInputSnapshot(inputsPath)
	if err != nil {
		return err
	}
	return materializeAuthSnapshot(ctx, inputs, output, newReader)
}

func prepareAuthLocal(ctx context.Context, root, output string, projection authSourceProjection, sourceSHA string) error {
	if err := validateAuthSourceProjection(projection, "local"); err != nil {
		return err
	}
	if sourceSHA == "" {
		var err error
		sourceSHA, err = repositorySourceSHA(root)
		if err != nil {
			return err
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sourceSHA) {
		return errors.New("local Auth source identity is invalid")
	}
	key, err := readLocalAuthKey(projection.JWTSecretReference)
	if err != nil {
		return err
	}
	defer clear(key)
	file := authFileFromSource(projection, string(key))
	file.ConfigID, err = localAuthConfigID(file, sourceSHA)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return errors.New("encode generated Auth config")
	}
	data = append(data, '\n')
	defer clear(data)
	if len(data) == 0 || len(data) > config.MaxAuthConfigBytes {
		return errors.New("generated Auth config size is invalid")
	}
	if _, err := config.DecodeAuthFile(data); err != nil {
		return fmt.Errorf("generated Auth config is invalid: %w", err)
	}
	authDir := filepath.Join(output, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return fmt.Errorf("create private Auth config directory: %w", err)
	}
	if err := os.Chmod(authDir, 0o700); err != nil {
		return fmt.Errorf("secure private Auth config directory: %w", err)
	}
	if err := writePrivateAtomic(filepath.Join(authDir, "auth.json"), data); err != nil {
		return fmt.Errorf("publish generated Auth config: %w", err)
	}
	manifest, err := json.MarshalIndent(authFileManifest{SchemaVersion: 1, Target: "auth", Environment: "local", ConfigID: file.ConfigID, SourceSHA: sourceSHA}, "", "  ")
	if err != nil {
		return errors.New("encode local Auth success manifest")
	}
	defer clear(manifest)
	if err := writePrivateAtomic(filepath.Join(authDir, "success.json"), append(manifest, '\n')); err != nil {
		return fmt.Errorf("publish local Auth success manifest: %w", err)
	}
	fmt.Printf("prepared Auth config schema=1 environment=local target=auth config_id=%s\n", file.ConfigID)
	return nil
}

func authFileFromSource(p authSourceProjection, jwt string) config.AuthFile {
	file := config.AuthFile{
		SchemaVersion: 1, Target: "auth", Environment: p.Environment, GCPProject: p.GCPProject,
		FirestoreDatabaseID: p.FirestoreDatabaseID, LocalCloudScope: p.LocalCloudScope,
		AuthServiceURL: p.AuthServiceURL, SyncServiceURL: p.SyncServiceURL,
		AllowedHosts: p.AllowedHosts, AllowedOrigins: p.AllowedOrigins,
		AuthSessionEnvironment: p.AuthSessionEnvironment, AuthSessionMigration: p.AuthSessionMigration,
		RegistrationEnabled: p.RegistrationEnabled, AuthDemoUserID: p.AuthDemoUserID,
		AuthDemoUserEmail: p.AuthDemoUserEmail, AuthDemoUserRole: p.AuthDemoUserRole, JWTSecret: jwt,
	}
	if p.Google.Enabled {
		file.Google = config.AuthFileGoogle{
			Enabled: true, ClientID: p.Google.ClientID, Issuer: p.Google.Issuer, JWKSURL: p.Google.JWKSURL,
			TokenURL: p.Google.TokenURL, LoginRedirectURL: p.Google.LoginRedirectURL,
			LinkRedirectURL: p.Google.LinkRedirectURL, CompletionURL: p.Google.CompletionURL,
		}
	}
	return file
}

func materializeAuthSnapshot(ctx context.Context, inputs config.AuthInputSnapshot, output string, newReader readerFactory) error {
	if err := config.ValidateAuthInputSnapshot(inputs); err != nil {
		return err
	}
	reader, err := newReader(ctx)
	if err != nil {
		return fmt.Errorf("initialize Auth secret resolver failed: %s", googleAPIErrorSummary(err))
	}
	jwt, _, err := resolveBindingFromReader(ctx, reader, "JWT_SECRET", inputs.JWTSecretVersion)
	if err != nil {
		return fmt.Errorf("materialize Auth JWT credential failed: %s", err)
	}
	defer clear(jwt)
	file := config.AuthFile{
		SchemaVersion: 1, Target: "auth", Environment: inputs.Environment, ConfigID: inputs.ConfigID,
		GCPProject: inputs.GCPProject, FirestoreDatabaseID: inputs.FirestoreDatabaseID,
		LocalCloudScope: inputs.LocalCloudScope, AuthServiceURL: inputs.AuthServiceURL,
		SyncServiceURL: inputs.SyncServiceURL,
		AllowedHosts:   inputs.AllowedHosts, AllowedOrigins: inputs.AllowedOrigins,
		AuthSessionEnvironment: inputs.AuthSessionEnvironment, AuthSessionMigration: inputs.AuthSessionMigration,
		RegistrationEnabled: inputs.RegistrationEnabled, AuthDemoUserID: inputs.AuthDemoUserID,
		AuthDemoUserEmail: inputs.AuthDemoUserEmail, AuthDemoUserRole: inputs.AuthDemoUserRole,
		JWTSecret: string(jwt),
		Google: config.AuthFileGoogle{
			Enabled: inputs.Google.Enabled, ClientID: inputs.Google.ClientID, Issuer: inputs.Google.Issuer,
			JWKSURL: inputs.Google.JWKSURL, TokenURL: inputs.Google.TokenURL,
			LoginRedirectURL: inputs.Google.LoginRedirectURL, LinkRedirectURL: inputs.Google.LinkRedirectURL,
			CompletionURL: inputs.Google.CompletionURL,
		},
	}
	if inputs.Google.Enabled {
		googleSecret, _, err := resolveBindingFromReader(ctx, reader, "GOOGLE_CLIENT_SECRET", inputs.Google.ClientSecretVersion)
		if err != nil {
			return fmt.Errorf("materialize Auth Google credential failed: %s", err)
		}
		defer clear(googleSecret)
		file.Google.ClientSecret = string(googleSecret)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return errors.New("encode generated Auth config")
	}
	data = append(data, '\n')
	defer clear(data)
	if len(data) == 0 || len(data) > config.MaxAuthConfigBytes {
		return errors.New("generated Auth config size is invalid")
	}
	if _, err := config.DecodeAuthFile(data); err != nil {
		return fmt.Errorf("generated Auth config is invalid: %w", err)
	}
	if err := writePrivateAtomic(output, data); err != nil {
		return fmt.Errorf("write private Auth config: %w", err)
	}
	return nil
}

func resolveBindingFromReader(ctx context.Context, reader secretReader, target, resource string) ([]byte, string, error) {
	if !authNumericSecretReferencePattern.MatchString(resource) {
		return nil, "", errors.New("credential reference must be pinned to a numeric version")
	}
	value, resolved, err := resolveBinding(ctx, secretBinding{Source: "secret-manager", Target: target, Resource: resource}, func(context.Context) (secretReader, error) {
		return reader, nil
	})
	if err != nil {
		clear(value)
		return nil, "", fmt.Errorf("selected secret version could not be accessed: %s", err)
	}
	return value, resolved, nil
}

func readLocalAuthKey(path string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return nil, errors.New("local Auth signing key reference is invalid")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("local Auth signing key file is unavailable")
	}
	secret := strings.TrimSpace(string(value))
	clear(value)
	decoded, err := hex.DecodeString(secret)
	if err != nil || len(decoded) != 32 {
		clear(decoded)
		return nil, errors.New("local Auth signing key file is invalid")
	}
	clear(decoded)
	return []byte(secret), nil
}

func localAuthConfigID(file config.AuthFile, sourceSHA string) (string, error) {
	file.ConfigID = ""
	file.JWTSecret = ""
	file.Google.ClientSecret = ""
	canonical, err := json.Marshal(struct {
		SourceSHA string          `json:"source_sha"`
		Auth      config.AuthFile `json:"auth"`
	}{SourceSHA: sourceSHA, Auth: file})
	if err != nil {
		return "", errors.New("encode local Auth identity")
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func repositorySourceSHA(root string) (string, error) {
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = root
	value, err := command.Output()
	if err != nil {
		return "", errors.New("read local Auth source identity")
	}
	sha := strings.TrimSpace(string(value))
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sha) {
		return "", errors.New("local Auth source identity is invalid")
	}
	return sha, nil
}

func loadAuthInputSnapshot(path string) (config.AuthInputSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxConfigBytes {
		return config.AuthInputSnapshot{}, errors.New("Auth input snapshot file is unavailable or oversized")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config.AuthInputSnapshot{}, errors.New("Auth input snapshot file is unavailable")
	}
	defer clear(data)
	return config.DecodeAuthInputSnapshot(data)
}

func writeAuthInputSnapshot(output string, inputs config.AuthInputSnapshot) error {
	data, err := json.MarshalIndent(inputs, "", "  ")
	if err != nil {
		return errors.New("encode Auth input snapshot")
	}
	defer clear(data)
	data = append(data, '\n')
	if err := os.MkdirAll(output, 0o700); err != nil {
		return fmt.Errorf("create Auth input directory: %w", err)
	}
	if err := writePrivateAtomic(filepath.Join(output, "auth-inputs.json"), data); err != nil {
		return fmt.Errorf("write Auth input snapshot: %w", err)
	}
	fmt.Printf("prepared Auth input snapshot schema=1 environment=%s target=auth config_id=%s\n", inputs.Environment, inputs.ConfigID)
	return nil
}

func writePrivateAtomic(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".auth-config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
