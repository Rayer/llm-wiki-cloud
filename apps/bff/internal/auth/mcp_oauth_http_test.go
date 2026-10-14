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

	"github.com/gin-gonic/gin"
)

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
		{name: "custom redirect scheme", body: `{"client_name":"Test Client","redirect_uris":["cursor://callback/private"]}`, wantStatus: http.StatusBadRequest, wantReason: "redirect_rejected", wantCalls: 1},
		{name: "duplicate redirect", body: `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:43123/callback","http://127.0.0.1:43123/callback"]}`, wantStatus: http.StatusBadRequest, wantReason: "duplicate_redirect_rejected", wantCalls: 1},
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
			if test.name == "last duplicate known value wins" && !strings.Contains(response.Body.String(), `"client_name":"Test Client"`) {
				t.Fatalf("duplicate known field behavior changed: %s", response.Body.String())
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
		"reason=redirect_rejected", "trace_id=0123456789abcdef0123456789abcdef",
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
