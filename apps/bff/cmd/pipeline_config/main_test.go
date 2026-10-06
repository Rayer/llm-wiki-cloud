package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/option"
	"google.golang.org/api/secretmanager/v1"
)

type fakeSecretReader struct {
	resource      string
	resolved      string
	value         []byte
	err           error
	identity      projectIdentity
	identityErr   error
	identityCalls int
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (r *fakeSecretReader) Access(_ context.Context, resource string) (string, []byte, error) {
	r.resource = resource
	if r.resolved == "" {
		r.resolved = resource
	}
	return r.resolved, r.value, r.err
}

func (r *fakeSecretReader) ProjectIdentity(_ context.Context, project string) (projectIdentity, error) {
	r.identityCalls++
	if r.identityErr != nil {
		return projectIdentity{}, r.identityErr
	}
	if !projectIdentityMatchesRequest(project, r.identity) {
		return projectIdentity{}, errProjectIdentityMismatch
	}
	return r.identity, nil
}

func TestEnvironmentBindingDoesNotInitializeSecretManager(t *testing.T) {
	t.Setenv("LWC_TEST_ONLY_KEY", "TEST_ONLY_SECRET_PAYLOAD")
	called := false
	value, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "environment", Target: "DEEPSEEK_API_KEY", EnvName: "LWC_TEST_ONLY_KEY",
	}, func(context.Context) (secretReader, error) {
		called = true
		return nil, fmt.Errorf("unexpected Secret Manager initialization")
	})
	if err != nil || called || string(value) != "TEST_ONLY_SECRET_PAYLOAD" {
		t.Fatalf("resolveBinding() = (%q, %v), Secret Manager initialized=%t", value, err, called)
	}
}

func TestMissingLocalEnvironmentSecretFailsWithoutSecretManager(t *testing.T) {
	t.Setenv("LWC_TEST_ONLY_MISSING_KEY", "")
	called := false
	_, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "environment", Target: "DEEPSEEK_API_KEY", EnvName: "LWC_TEST_ONLY_MISSING_KEY",
	}, func(context.Context) (secretReader, error) {
		called = true
		return nil, errors.New("unexpected Secret Manager initialization")
	})
	if err == nil || !strings.Contains(err.Error(), "required local Pipeline secret is missing") || called {
		t.Fatalf("missing local key error=%v Secret Manager initialized=%t", err, called)
	}
}

func TestSecretManagerBindingUsesOnlySelectedVersion(t *testing.T) {
	reader := &fakeSecretReader{value: []byte("TEST_ONLY_GSM_PAYLOAD")}
	const resource = "projects/test-project/secrets/test-secret/versions/7"
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resource,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || string(value) != "TEST_ONLY_GSM_PAYLOAD" || reader.resource != resource || resolved != resource {
		t.Fatalf("selected Secret Manager version was not resolved: value=%q resource=%q err=%v", value, reader.resource, err)
	}
	clear(value)
	for _, b := range value {
		if b != 0 {
			t.Fatal("resolved synthetic secret buffer was not cleared")
		}
	}
}

func TestLatestSecretManagerBindingIsPinnedToResolvedNumericVersion(t *testing.T) {
	reader := &fakeSecretReader{resolved: "projects/test-project/secrets/test-secret/versions/19", value: []byte("TEST_ONLY_GSM_PAYLOAD")}
	const latest = "projects/test-project/secrets/test-secret/versions/latest"
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: latest,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || resolved != reader.resolved || string(value) != "TEST_ONLY_GSM_PAYLOAD" {
		t.Fatalf("latest Secret Manager version was not pinned: resolved=%q err=%v", resolved, err)
	}
	clear(value)
}

func TestSecretManagerBindingAcceptsAuthoritativeNumericProjectAlias(t *testing.T) {
	const requested = "projects/test-project/secrets/test-secret/versions/latest"
	const numericResponse = "projects/580854833715/secrets/test-secret/versions/19"
	reader := &fakeSecretReader{
		resolved: numericResponse, value: []byte("TEST_ONLY_GSM_PAYLOAD"),
		identity: projectIdentity{ProjectID: "test-project", ProjectNumber: "580854833715"},
	}
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: requested,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || string(value) != "TEST_ONLY_GSM_PAYLOAD" || reader.resource != requested ||
		reader.identityCalls != 1 || resolved != "projects/test-project/secrets/test-secret/versions/19" {
		t.Fatalf("numeric project alias was not authoritatively resolved: resource=%q version=%q identity_calls=%d err=%v",
			reader.resource, resolved, reader.identityCalls, err)
	}
	clear(value)
}

func TestSecretManagerNumericProjectAliasRejectsUnboundResponses(t *testing.T) {
	const requested = "projects/test-project/secrets/test-secret/versions/11"
	identity := projectIdentity{ProjectID: "test-project", ProjectNumber: "580854833715"}
	for _, tc := range []struct {
		name           string
		resolved       string
		identity       projectIdentity
		identityErr    error
		emptyPayload   bool
		wantIdentity   int
		wantDiagnostic string
	}{
		{name: "wrong numeric project", resolved: "projects/999999999999/secrets/test-secret/versions/11", identity: identity, wantIdentity: 1},
		{name: "wrong secret", resolved: "projects/580854833715/secrets/other-secret/versions/11", identity: identity},
		{name: "wrong version", resolved: "projects/580854833715/secrets/test-secret/versions/12", identity: identity},
		{name: "malformed response", resolved: "projects/580854833715/secrets/test-secret/versions/latest", identity: identity},
		{name: "metadata identity mismatch", resolved: "projects/580854833715/secrets/test-secret/versions/11",
			identity: projectIdentity{ProjectID: "other-project", ProjectNumber: "580854833715"}, wantIdentity: 1,
			wantDiagnostic: "does not identify the selected project"},
		{name: "metadata unavailable", resolved: "projects/580854833715/secrets/test-secret/versions/11",
			identity: identity, identityErr: errors.New("TEST_ONLY_METADATA_FAILURE"), wantIdentity: 1,
			wantDiagnostic: "project identity lookup failed"},
		{name: "empty payload precedes metadata lookup", resolved: "projects/580854833715/secrets/test-secret/versions/11",
			identity: identity, emptyPayload: true, wantDiagnostic: "payload is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("TEST_ONLY_GSM_PAYLOAD")
			if tc.emptyPayload {
				payload = nil
			}
			reader := &fakeSecretReader{
				resolved: tc.resolved, value: payload,
				identity: tc.identity, identityErr: tc.identityErr,
			}
			_, _, err := resolveBinding(context.Background(), secretBinding{
				Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: requested,
			}, func(context.Context) (secretReader, error) { return reader, nil })
			if err == nil || strings.Contains(err.Error(), "TEST_ONLY_GSM_PAYLOAD") ||
				strings.Contains(err.Error(), "TEST_ONLY_METADATA_FAILURE") || reader.identityCalls != tc.wantIdentity {
				t.Fatalf("unbound response was accepted or leaked data: identity_calls=%d err=%v", reader.identityCalls, err)
			}
			if tc.wantDiagnostic != "" && !strings.Contains(err.Error(), tc.wantDiagnostic) {
				t.Fatalf("metadata failure lost its diagnostic class: %v", err)
			}
			for _, b := range reader.value {
				if b != 0 {
					t.Fatal("rejected synthetic secret buffer was not cleared")
				}
			}
		})
	}
}

func TestPreparedSecretReferenceUpdatesOnlyPublicAndPrivateBindings(t *testing.T) {
	temp := t.TempDir()
	path := filepath.Join(temp, "pipeline.json")
	if err := os.WriteFile(path, []byte(`{"environment":"dev","secret":{"source":"secret-manager","target":"DEEPSEEK_API_KEY","envName":"","resource":"projects/test-project/secrets/test-secret/versions/latest"},"synto":{"ok":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	const resolved = "projects/test-project/secrets/test-secret/versions/19"
	if err := bindResolvedSecretReference(temp, secretBinding{Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resolved}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	var binding secretBinding
	if err := json.Unmarshal(output["secret"], &binding); err != nil || binding.Resource != resolved {
		t.Fatalf("public prepared binding = %+v, err=%v", binding, err)
	}
	var synto map[string]bool
	if err := json.Unmarshal(output["synto"], &synto); err != nil || !synto["ok"] {
		t.Fatalf("unrelated public config changed: %s err=%v", output["synto"], err)
	}
}

func TestSecretManagerFailureIsExplicitAndDoesNotExposePayload(t *testing.T) {
	reader := &fakeSecretReader{err: fmt.Errorf("TEST_ONLY_SECRET_PAYLOAD denied")}
	_, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY",
		Resource: "projects/test-project/secrets/test-secret/versions/3",
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil || !strings.Contains(err.Error(), "access failed") || strings.Contains(err.Error(), "TEST_ONLY_SECRET_PAYLOAD") {
		t.Fatalf("secret access failure was not explicit and redacted: %v", err)
	}
}

func TestInvalidSecretVersionFailsBeforeClientInitialization(t *testing.T) {
	called := false
	_, _, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Resource: "projects/test/secrets/test/versions/0",
	}, func(context.Context) (secretReader, error) {
		called = true
		return nil, nil
	})
	if err == nil || called || !strings.Contains(err.Error(), "full secret version resource path") {
		t.Fatalf("invalid secret version was accepted: err=%v initialized=%t", err, called)
	}
}

func TestRunTimeoutMustBeExplicit(t *testing.T) {
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "")
	if _, err := runTimeoutFromEnvironment(); err == nil || !strings.Contains(err.Error(), "verified Pipeline Job timeout") {
		t.Fatalf("missing timeout was not rejected clearly: %v", err)
	}
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "0")
	if _, err := runTimeoutFromEnvironment(); err == nil {
		t.Fatal("zero timeout was accepted")
	}
}

func TestRunPrepareRendersOnlyThePreparedProjection(t *testing.T) {
	realPKL := strings.TrimSpace(os.Getenv("LWC_TEST_REAL_PKL"))
	if realPKL == "" {
		if found, err := exec.LookPath("pkl"); err == nil {
			realPKL = found
		} else if _, err := os.Stat("/Users/rayer/.local/bin/pkl"); err == nil {
			realPKL = "/Users/rayer/.local/bin/pkl"
		}
	}
	if realPKL == "" {
		t.Skip("Pkl is unavailable")
	}
	cacheDir := strings.TrimSpace(os.Getenv("PKL_CACHE_DIR"))
	if cacheDir == "" {
		if _, err := os.Stat("/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages"); err == nil {
			cacheDir = "/Users/rayer/.hermes/profiles/lwc-tpm/cache/pkl-packages"
		}
	}
	if cacheDir == "" {
		t.Skip("locked Pkl package cache is unavailable")
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Skip("locked Pkl package cache is unavailable")
	}

	sourceDir := filepath.Join("..", "..", "..", "..", "deploy", "cac")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cac"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		data, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "deploy", "cac", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wrapper := filepath.Join(root, "pkl-wrapper")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nset -eu\nlast=\nfor arg do last=\"$arg\"; done\nif [ \"$last\" = \"$LWC_REPOSITORY_ROOT/deploy/cac/ssot.pkl\" ]; then\n  \"$LWC_TEST_REAL_PKL\" \"$@\"\n  printf '%s\\n' 'module mutated' > \"$LWC_REPOSITORY_ROOT/deploy/cac/ssot.pkl\"\nelse\n  \"$LWC_TEST_REAL_PKL\" \"$@\"\nfi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", wrapper)
	t.Setenv("LWC_TEST_REAL_PKL", realPKL)
	t.Setenv("PKL_CACHE_DIR", cacheDir)
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "23")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "projects/test-project/secrets/test-secret/versions/latest")
	const payload = "TEST_ONLY_LOCAL_GSM_PAYLOAD"
	reader := &fakeSecretReader{resolved: "projects/test-project/secrets/test-secret/versions/7", value: []byte(payload)}
	if err := runPrepare(context.Background(), "local", output,
		func(context.Context) (secretReader, error) { return reader, nil }); err != nil {
		t.Fatalf("runPrepare() with synthetic local GSM input: %v", err)
	}
	mutated, err := os.ReadFile(filepath.Join(root, "deploy", "cac", "ssot.pkl"))
	if err != nil || string(mutated) != "module mutated\n" {
		t.Fatalf("SSOT fixture was not mutated between evaluate and render: %q err=%v", mutated, err)
	}
	tomlBytes, err := os.ReadFile(filepath.Join(output, "synto.toml"))
	if err != nil || !strings.Contains(string(tomlBytes), "article_max_tokens = 32768") ||
		!strings.Contains(string(tomlBytes), "run_timeout_seconds = 23") || strings.Contains(string(tomlBytes), payload) {
		t.Fatalf("render did not use the prepared projection/defaults: %s err=%v", tomlBytes, err)
	}
	publicBytes, err := os.ReadFile(filepath.Join(output, "pipeline.json"))
	if err != nil || strings.Contains(string(publicBytes), payload) {
		t.Fatalf("public config contains local secret payload: err=%v", err)
	}
	privateInfo, err := os.Stat(filepath.Join(output, "private-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private bindings mode=%v, want 0600", privateInfo.Mode())
	}
	privateBytes, err := os.ReadFile(filepath.Join(output, "private-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var private privateBindings
	if err := json.Unmarshal(privateBytes, &private); err != nil || string(private.LocalAPIKey) != payload {
		t.Fatalf("private local binding payload=%q err=%v", private.LocalAPIKey, err)
	}
	clear(private.LocalAPIKey)
}

func TestGoogleSecretManagerSDKAccessPath(t *testing.T) {
	const resource = "projects/test-project/secrets/test-secret/versions/11"
	const payload = "TEST_ONLY_SDK_PAYLOAD"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/"+resource+":access" {
			return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		body := fmt.Sprintf(`{"name":"%s","payload":{"data":"%s"}}`, resource, base64.StdEncoding.EncodeToString([]byte(payload)))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	service, err := secretmanager.NewService(context.Background(), option.WithEndpoint("https://secretmanager.test/"), option.WithHTTPClient(client), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resource,
	}, func(context.Context) (secretReader, error) {
		return googleSecretReader{service: service}, nil
	})
	if err != nil || string(value) != payload || resolved != resource {
		t.Fatalf("Google Secret Manager client response = %q version=%q, %v", value, resolved, err)
	}
	clear(value)
}

func TestGoogleSecretManagerSDKResolvesCanonicalNumericProjectName(t *testing.T) {
	const requested = "projects/test-project/secrets/test-secret/versions/11"
	const canonical = "projects/580854833715/secrets/test-secret/versions/11"
	const payload = "TEST_ONLY_SDK_PAYLOAD"
	accessCalls, projectCalls := 0, 0
	secretHTTP := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		accessCalls++
		if request.Method != http.MethodGet || request.URL.Path != "/v1/"+requested+":access" {
			return nil, fmt.Errorf("unexpected Secret Manager request %s %s", request.Method, request.URL.Path)
		}
		body := fmt.Sprintf(`{"name":%q,"payload":{"data":%q}}`, canonical,
			base64.StdEncoding.EncodeToString([]byte(payload)))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	projectHTTP := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		projectCalls++
		if request.Method != http.MethodGet || request.URL.Path != "/v3/projects/test-project" {
			return nil, fmt.Errorf("unexpected Cloud Resource Manager request %s %s", request.Method, request.URL.Path)
		}
		body := `{"name":"projects/580854833715","projectId":"test-project","state":"ACTIVE"}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	secretService, err := secretmanager.NewService(context.Background(), option.WithEndpoint("https://secretmanager.test/"),
		option.WithHTTPClient(secretHTTP), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	projectService, err := cloudresourcemanager.NewService(context.Background(), option.WithEndpoint("https://cloudresourcemanager.test/"),
		option.WithHTTPClient(projectHTTP), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	reader := googleSecretReader{service: secretService, projects: projectService}
	value, resolved, err := resolveBinding(context.Background(), secretBinding{
		Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: requested,
	}, func(context.Context) (secretReader, error) { return reader, nil })
	if err != nil || string(value) != payload || accessCalls != 1 || projectCalls != 1 ||
		resolved != requested {
		t.Fatalf("SDK canonical project response was not safely pinned: version=%q access_calls=%d project_calls=%d err=%v",
			resolved, accessCalls, projectCalls, err)
	}
	clear(value)
}

func TestRunPreparePreservesBoundedSecretManagerSDKMessage(t *testing.T) {
	const resource = "projects/test-project/secrets/test-secret/versions/11"
	const serverBodyMarker = "TEST_ONLY_RAW_SECRET_MANAGER_ERROR_BODY"
	message := "Permission denied for the selected Secret Manager version: " + strings.Repeat("x", 700)
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code": 403, "message": message,
			"details": []any{map[string]any{"message": serverBodyMarker}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := secretManagerResponseReader(t, resource, http.StatusForbidden, string(body))
	root := prepareRootForSecretManager(t, resource)
	err = runPrepare(context.Background(), "dev", filepath.Join(root, "out"),
		func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil {
		t.Fatal("runPrepare() accepted a Secret Manager access failure")
	}
	diagnostic := err.Error()
	if !strings.Contains(diagnostic, "Google API HTTP 403") ||
		!strings.Contains(diagnostic, "Permission denied for the selected Secret Manager version") ||
		!strings.Contains(diagnostic, "message_truncated=true") {
		t.Fatalf("runPrepare() lost the bounded SDK diagnostic: %q", diagnostic)
	}
	if strings.Contains(diagnostic, serverBodyMarker) || len(diagnostic) > 900 {
		t.Fatalf("runPrepare() exposed raw SDK body or an unbounded message: %q", diagnostic)
	}
}

func TestRunPrepareDistinguishesEmptySecretManagerPayload(t *testing.T) {
	const resource = "projects/test-project/secrets/test-secret/versions/11"
	body := fmt.Sprintf(`{"name":%q,"payload":{"data":""}}`, resource)
	reader := secretManagerResponseReader(t, resource, http.StatusOK, body)
	root := prepareRootForSecretManager(t, resource)
	err := runPrepare(context.Background(), "dev", filepath.Join(root, "out"),
		func(context.Context) (secretReader, error) { return reader, nil })
	if err == nil || !strings.Contains(err.Error(), "payload is empty") || strings.Contains(err.Error(), "access failed") {
		t.Fatalf("empty payload was not distinguished from access failure: %v", err)
	}
}

func TestRunPrepareDistinguishesSecretManagerClientInitializationFailure(t *testing.T) {
	root := prepareRootForSecretManager(t, "projects/test-project/secrets/test-secret/versions/11")
	const detail = "TEST_ONLY_INITIALIZATION_DETAIL"
	err := runPrepare(context.Background(), "dev", filepath.Join(root, "out"),
		func(context.Context) (secretReader, error) { return nil, errors.New(detail) })
	if err == nil || !strings.Contains(err.Error(), "initialize Secret Manager resolver") ||
		!strings.Contains(err.Error(), "error type") || strings.Contains(err.Error(), detail) {
		t.Fatalf("client initialization failure was not safely classified: %v", err)
	}
}

func secretManagerResponseReader(t *testing.T, resource string, status int, body string) secretReader {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/"+resource+":access" {
			return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	service, err := secretmanager.NewService(context.Background(), option.WithEndpoint("https://secretmanager.test/"), option.WithHTTPClient(client), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	return googleSecretReader{service: service}
}

func prepareRootForSecretManager(t *testing.T, resource string) string {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "deploy", "cac")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssot.pkl", "synto.pkl"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("offline fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config, err := json.Marshal(pipelineConfig{
		Environment: "dev", RunTimeoutSeconds: 23,
		Secret: secretBinding{Source: "secret-manager", Target: "DEEPSEEK_API_KEY", Resource: resource},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkl := filepath.Join(root, "pkl-fixture")
	script := "#!/bin/sh\ncat <<'SSOT_JSON'\n" + string(config) + "\nSSOT_JSON\n"
	if err := os.WriteFile(pkl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	t.Setenv("PKL_BIN", pkl)
	t.Setenv("PKL_CACHE_DIR", "")
	t.Setenv("LWC_PIPELINE_RUN_TIMEOUT_SECONDS", "23")
	t.Setenv("LWC_PIPELINE_LOCAL_SECRET_VERSION_RESOURCE", "")
	return root
}
