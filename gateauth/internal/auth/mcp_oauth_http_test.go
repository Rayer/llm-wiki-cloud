package auth

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/config"
	"google.golang.org/api/idtoken"
)

func TestMCPOAuthMetadataAdvertisesRawWriteScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/.well-known/oauth-authorization-server", MCPOAuthMetadataHandler("https://auth.example.test"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://attacker.example.test/.well-known/oauth-authorization-server", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mcp:query"`) ||
		!strings.Contains(response.Body.String(), `"mcp:raw:write"`) {
		t.Fatalf("OAuth metadata status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMCPOAuthMetadataUsesEnabledLoadedPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resources := config.LegacyAuthOAuthResources("https://bff.example.test")
	resources = append(resources, config.AuthOAuthResource{ID: "daily-brief", Kind: "fixed", Enabled: false,
		URI: "https://brief.rayer.idv.tw/mcp", Name: "Daily Brief",
		Scopes:           []config.AuthOAuthScope{{Name: "brief:manage", Description: "synthetic"}},
		AllowedScopeSets: [][]string{{"brief:manage"}}, DefaultScopeSet: []string{}})
	serve := func() string {
		router := gin.New()
		router.GET("/metadata", MCPOAuthMetadataHandler("https://auth.example.test", resources))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metadata", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("metadata status=%d", response.Code)
		}
		return response.Body.String()
	}
	if body := serve(); !strings.Contains(body, `"mcp:raw:write"`) || strings.Contains(body, `"brief:manage"`) {
		t.Fatalf("disabled fixed scope appeared in metadata: %s", body)
	}
	resources[1].Enabled = true
	if body := serve(); !strings.Contains(body, `"brief:manage"`) {
		t.Fatalf("enabled fixed scope missing from metadata: %s", body)
	}
}

func TestMCPOAuthGrantListRequiresExactlyOneRecognizedFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/grants", MCPOAuthGrantsHandler(nil))
	for _, path := range []string{
		"/grants", "/grants?project_id=project-a&resource=https%3A%2F%2Fbrief.rayer.idv.tw%2Fmcp",
		"/grants?project_id=project-a&project_id=project-b", "/grants?resource=", "/grants?resource=https%3A%2F%2Fbrief.rayer.idv.tw%2Fmcp&user_id=other",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestDailyBriefCredentialVerifierFailsClosedAndRequiresVerifiedCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const audience = "https://auth.fixture.example.test"
	const (
		devResourceURI        = "https://brief.dev.rayer.idv.tw/mcp"
		productionResourceURI = "https://brief.rayer.idv.tw/mcp"
		devCaller             = "daily-brief-dev-runtime@daily-brief-service.iam.gserviceaccount.com"
		productionCaller      = "daily-brief-runtime@daily-brief-service.iam.gserviceaccount.com"
	)
	policy := config.AuthOAuthResource{
		ID: "daily-brief", Kind: "fixed", Enabled: true, URI: devResourceURI, Name: "Daily Brief",
		Scopes:           []config.AuthOAuthScope{{Name: "brief:manage", Description: "Read and manage Daily Brief reports and annotations in your private account space."}},
		AllowedScopeSets: [][]string{{"brief:manage"}}, DefaultScopeSet: []string{},
		VerifierCallerServiceAccount: devCaller, VerifierAudience: audience,
	}
	resources := append(config.LegacyAuthOAuthResources("https://bff.example.test"), policy)
	cfg := config.Config{AuthEnvironment: "dev", AuthOAuthResources: resources}
	service := NewMCPOAuthServiceWithPolicy(nil, "llm-wiki-cloud-dev", "dev", "https://auth.fixture.example.test",
		"https://bff.example.test", "https://wiki.example.test", "fixture-jwt-secret", resources)
	if !service.validStoredResource(devResourceURI, "", "brief:manage", true) {
		t.Fatal("DEV verifier policy rejected the configured Daily Brief resource")
	}
	if service.validStoredResource(productionResourceURI, "", "brief:manage", true) {
		t.Fatal("DEV verifier policy accepted the Production Daily Brief resource")
	}
	validated := 0
	validator := func(_ context.Context, token, gotAudience string) (*idtoken.Payload, error) {
		validated++
		if gotAudience != audience {
			return nil, ErrMCPOAuthInvalid
		}
		payload := &idtoken.Payload{
			Issuer: "https://accounts.google.com", Audience: audience, Expires: time.Now().Add(time.Minute).Unix(),
			Claims: map[string]interface{}{"email": policy.VerifierCallerServiceAccount, "email_verified": true},
		}
		switch token {
		case "wrong-issuer":
			payload.Issuer = "https://issuer.example.test"
		case "wrong-audience":
			payload.Audience = "https://wrong.example.test"
		case "legacy-dev-audience":
			payload.Audience = "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app"
		case "expired-caller":
			payload.Expires = time.Now().Add(-time.Minute).Unix()
		case "unverified-email":
			payload.Claims["email_verified"] = false
		case "wrong-email":
			payload.Claims["email"] = "another-runtime@llm-wiki-cloud.iam.gserviceaccount.com"
		case "production-caller":
			payload.Claims["email"] = productionCaller
		case "google-id-token":
		default:
			return nil, ErrMCPOAuthInvalid
		}
		return payload, nil
	}
	validCredential, err := generateMCPOpaque(mcpOAuthAccessPrefix)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/verify", MCPOAuthCredentialVerifierHandlerWithValidator(service, cfg, validator))
	serve := func(userCredential, classHeader, callerToken string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/verify", nil)
		request.Header.Set("Authorization", "Bearer "+callerToken)
		if userCredential != "" {
			request.Header.Set("X-LWC-User-Credential", userCredential)
		}
		if classHeader != "" {
			request.Header.Set("X-LWC-Credential-Class", classHeader)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	before := validated
	missing := serve("", "", "google-id-token")
	if missing.Code != http.StatusBadRequest || validated != before || missing.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing user token status=%d validator delta=%d cache=%q", missing.Code, validated-before, missing.Header().Get("Cache-Control"))
	}
	class := serve(validCredential, "mcp-bearer", "google-id-token")
	if class.Code != http.StatusBadRequest || validated != before {
		t.Fatalf("caller-supplied class status=%d validator delta=%d", class.Code, validated-before)
	}
	valid := serve(validCredential, "", "google-id-token")
	if valid.Code != http.StatusUnauthorized || validated != before+1 || strings.Contains(valid.Body.String(), validCredential) {
		t.Fatalf("synthetic user token should reach Auth validation then fail closed: status=%d calls=%d body=%s", valid.Code, validated-before, valid.Body.String())
	}
	for _, invalid := range []string{"wrong-issuer", "wrong-audience", "legacy-dev-audience", "expired-caller", "unverified-email", "wrong-email", "production-caller"} {
		response := serve(validCredential, "", invalid)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "untrusted_caller") {
			t.Errorf("caller %q status=%d body=%s", invalid, response.Code, response.Body.String())
		}
	}
}

func TestMCPOAuthRegisterHandlerIgnoresUnknownMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type registration struct {
		name      string
		redirects []string
	}
	var registered []registration
	register := func(_ context.Context, name string, redirects []string) (mcpOAuthClientRecord, error) {
		registered = append(registered, registration{name: name, redirects: append([]string(nil), redirects...)})
		return mcpOAuthClientRecord{
			ClientID: "fixture-client", ClientName: name, RedirectURIs: append([]string(nil), redirects...),
			CreatedAt: time.Unix(1, 0).UTC(),
		}, nil
	}
	baseline := `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback"]}`
	withUnknown := `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback"],"client_uri":"https://private.example/sentinel","software_id":"ID_SENTINEL","software_version":"VERSION_SENTINEL","x_private_name":"NAME_SENTINEL","x_custom":{"value":"VALUE_SENTINEL"}}`

	baselineResponse := serveMCPOAuthRegister(t, mcpOAuthRegisterHandler(register), baseline, "")
	unknownResponse := serveMCPOAuthRegister(t, mcpOAuthRegisterHandler(register), withUnknown, "")
	if baselineResponse.Code != http.StatusCreated || unknownResponse.Code != http.StatusCreated {
		t.Fatalf("baseline/unknown registration statuses = %d/%d, want 201/201", baselineResponse.Code, unknownResponse.Code)
	}
	if len(registered) != 2 || registered[0].name != registered[1].name ||
		strings.Join(registered[0].redirects, "\x00") != strings.Join(registered[1].redirects, "\x00") {
		t.Fatalf("unknown metadata changed persisted registration inputs: %+v", registered)
	}
	for _, sentinel := range []string{"private.example", "ID_SENTINEL", "VERSION_SENTINEL", "NAME_SENTINEL", "VALUE_SENTINEL", "client_uri", "software_id", "x_private_name", "x_custom"} {
		if strings.Contains(unknownResponse.Body.String(), sentinel) {
			t.Errorf("registration response echoed unknown metadata marker %q: %s", sentinel, unknownResponse.Body.String())
		}
	}
}

func TestMCPOAuthRegisterHandlerPreservesKnownFieldAndJSONPolicies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registerCalls := 0
	register := func(_ context.Context, name string, redirects []string) (mcpOAuthClientRecord, error) {
		registerCalls++
		name = strings.TrimSpace(name)
		if utf8.RuneCountInString(name) > 128 || strings.ContainsAny(name, "\r\n\x00") {
			return mcpOAuthClientRecord{}, ErrMCPOAuthBadRequest
		}
		if len(redirects) == 0 || len(redirects) > 12 {
			return mcpOAuthClientRecord{}, ErrMCPOAuthUnavailable
		}
		seen := make(map[string]struct{}, len(redirects))
		for _, redirect := range redirects {
			if !validMCPOAuthRedirectURI(redirect) {
				return mcpOAuthClientRecord{}, ErrMCPOAuthBadRequest
			}
			if _, exists := seen[redirect]; exists {
				return mcpOAuthClientRecord{}, ErrMCPOAuthBadRequest
			}
			seen[redirect] = struct{}{}
		}
		return mcpOAuthClientRecord{
			ClientID: "fixture-client", ClientName: name, RedirectURIs: append([]string(nil), redirects...), CreatedAt: time.Unix(1, 0).UTC(),
		}, nil
	}
	base := `"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback"]`
	longBody := `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback"],"x":"` + strings.Repeat("x", mcpOAuthRequestLimit) + `"}`
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantReason string
		wantScheme string
		wantCalls  int
	}{
		{name: "malformed JSON", body: "{", wantStatus: http.StatusBadRequest, wantReason: "malformed_json"},
		{name: "wrong top-level shape", body: `[]`, wantStatus: http.StatusBadRequest, wantReason: "invalid_json_shape"},
		{name: "wrong client name type", body: `{"client_name":17,"redirect_uris":["http://127.0.0.1:43123/callback"]}`, wantStatus: http.StatusBadRequest, wantReason: "known_field_type"},
		{name: "wrong redirect type", body: `{"client_name":"Test Client","redirect_uris":"http://127.0.0.1:43123/callback"}`, wantStatus: http.StatusBadRequest, wantReason: "known_field_type"},
		{name: "missing client name", body: `{"redirect_uris":["http://127.0.0.1:43123/callback"]}`, wantStatus: http.StatusBadRequest, wantReason: "missing_client_name"},
		{name: "unsupported auth method", body: `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback"],"token_endpoint_auth_method":"client_secret_basic"}`, wantStatus: http.StatusBadRequest, wantReason: "unsupported_metadata"},
		{name: "unsupported grant", body: `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback"],"grant_types":["client_credentials"]}`, wantStatus: http.StatusBadRequest, wantReason: "unsupported_metadata"},
		{name: "unsupported response type", body: `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback"],"response_types":["token"]}`, wantStatus: http.StatusBadRequest, wantReason: "unsupported_metadata"},
		{name: "unsupported scope", body: `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback"],"scope":"other"}`, wantStatus: http.StatusBadRequest, wantReason: "unsupported_metadata"},
		{name: "supported query and raw scope", body: `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback"],"scope":"mcp:query mcp:raw:write"}`, wantStatus: http.StatusCreated, wantCalls: 1},
		{name: "custom redirect scheme", body: `{"client_name":"Test Client","redirect_uris":["cursor://callback/private"]}`, wantStatus: http.StatusBadRequest, wantReason: "registration_rejected", wantScheme: "custom", wantCalls: 1},
		{name: "invalid client name precedes custom redirect", body: `{"client_name":"Invalid\nName","redirect_uris":["cursor://callback/private"]}`, wantStatus: http.StatusBadRequest, wantReason: "registration_rejected", wantScheme: "custom", wantCalls: 1},
		{name: "duplicate redirect", body: `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback","http://127.0.0.1:43123/callback"]}`, wantStatus: http.StatusBadRequest, wantReason: "registration_rejected", wantScheme: "http_loopback", wantCalls: 1},
		{name: "trailing JSON", body: `{` + base + `}{"private":"TRAILING_SENTINEL"}`, wantStatus: http.StatusBadRequest, wantReason: "trailing_data"},
		{name: "body limit", body: longBody, wantStatus: http.StatusBadRequest, wantReason: "body_too_large"},
		{name: "last duplicate known value wins", body: `{"client_name":"First","` + strings.TrimPrefix(base, `"`) + `}`, wantStatus: http.StatusCreated, wantCalls: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			before := registerCalls
			var output bytes.Buffer
			previousWriter, previousFlags := log.Writer(), log.Flags()
			log.SetOutput(&output)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags) })
			response := serveMCPOAuthRegister(t, mcpOAuthRegisterHandler(register), test.body, "0123456789abcdef0123456789abcdef/1;o=1")
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if registerCalls-before != test.wantCalls {
				t.Fatalf("register call count delta = %d, want %d", registerCalls-before, test.wantCalls)
			}
			if test.wantReason != "" && !strings.Contains(output.String(), "reason="+test.wantReason) {
				t.Fatalf("log %q does not contain fixed reason %q", output.String(), test.wantReason)
			}
			if test.wantScheme != "" && !strings.Contains(output.String(), "redirect_scheme_category="+test.wantScheme) {
				t.Fatalf("log %q does not contain scheme category %q", output.String(), test.wantScheme)
			}
			if test.name == "last duplicate known value wins" && !strings.Contains(response.Body.String(), `"client_name":"Test Client"`) {
				t.Fatalf("duplicate known field behavior changed: %s", response.Body.String())
			}
			if test.name == "supported query and raw scope" && !strings.Contains(response.Body.String(), `"scope":"mcp:query mcp:raw:write"`) {
				t.Fatalf("write scope was not returned by registration: %s", response.Body.String())
			}
		})
	}
}

func TestMCPOAuthRegisterDiagnosticsAreFiniteAndDoNotLogRequestValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags) })
	register := func(_ context.Context, name string, redirects []string) (mcpOAuthClientRecord, error) {
		return mcpOAuthClientRecord{}, ErrMCPOAuthBadRequest
	}
	body := `{"client_name":"CLIENT_NAME_PRIVATE_SENTINEL","redirect_uris":["cursor://HOST_PRIVATE_SENTINEL/PATH_PRIVATE_SENTINEL?QUERY_PRIVATE_SENTINEL"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"scope":"mcp:query","private_field_name_SENTINEL":"UNKNOWN_VALUE_PRIVATE_SENTINEL"}`
	response := serveMCPOAuthRegister(t, mcpOAuthRegisterHandler(register), body, "0123456789abcdef0123456789abcdef/1;o=1")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("custom redirect status = %d, want 400", response.Code)
	}
	line := output.String()
	for _, sentinel := range []string{"CLIENT_NAME_PRIVATE_SENTINEL", "HOST_PRIVATE_SENTINEL", "PATH_PRIVATE_SENTINEL", "QUERY_PRIVATE_SENTINEL", "private_field_name_SENTINEL", "UNKNOWN_VALUE_PRIVATE_SENTINEL", "cursor://"} {
		if strings.Contains(line, sentinel) {
			t.Errorf("diagnostic leaked request data %q: %s", sentinel, line)
		}
	}
	for _, safe := range []string{
		"reason=registration_rejected", "trace_id=0123456789abcdef0123456789abcdef",
		"client_name_present=true", "redirect_uris_present=true", "redirect_count=1",
		"token_auth_method_present=true", "grant_types_present=true", "grant_type_count=2",
		"response_types_present=true", "response_type_count=1", "scope_present=true",
		"unknown_field_count=1", "redirect_scheme_category=custom",
	} {
		if !strings.Contains(line, safe) {
			t.Errorf("diagnostic is missing safe classification %q: %s", safe, line)
		}
	}
}

func TestMCPOAuthRegisterUnavailableAndSharedDecoderRemainStrict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags) })
	response := serveMCPOAuthRegister(t, MCPOAuthRegisterHandler(nil), `{"private":"DO_NOT_LOG"}`, "invalid-trace-marker")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(output.String(), "reason=registration_unavailable") ||
		!strings.Contains(output.String(), "trace_id=none") || strings.Contains(output.String(), "DO_NOT_LOG") {
		t.Fatalf("unavailable registration response/log changed: status=%d log=%s", response.Code, output.String())
	}

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/decision", strings.NewReader(`{"decision":"allow","unknown":"value"}`))
	var request mcpOAuthConsentDecisionRequest
	if err := decodeMCPOAuthJSON(context, &request); err == nil {
		t.Fatal("shared strict decoder accepted an unknown field")
	}
}

func serveMCPOAuthRegister(t *testing.T, handler gin.HandlerFunc, body, traceContext string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.POST("/api/v1/auth/mcp/oauth/register", handler)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/mcp/oauth/register", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if traceContext != "" {
		request.Header.Set("X-Cloud-Trace-Context", traceContext)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
