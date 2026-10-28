package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"golang.org/x/crypto/bcrypt"

	"github.com/rayer/gateauth/internal/auth"
	"github.com/rayer/gateauth/internal/config"
	firestoreclient "github.com/rayer/gateauth/internal/firestore"
	"github.com/rayer/gateauth/internal/observability/otlpfixture"
	"github.com/rayer/gateauth/internal/syssettings"
)

func TestProductionRouterExposesOnlyAuthPublicSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true}, true)

	want := map[string]bool{
		http.MethodDelete + " /api/v1/auth/mcp/oauth/grants/:grantID":                                    true,
		http.MethodDelete + " /api/v1/auth/cli/bindings/:projectID/:bindingID":                           true,
		http.MethodDelete + " /api/v1/auth/cli/sessions/:id":                                             true,
		http.MethodGet + " /.well-known/oauth-authorization-server":                                      true,
		http.MethodGet + " /api/v1/auth/mcp/oauth/authorize":                                             true,
		http.MethodGet + " /api/v1/auth/mcp/oauth/consent/:transactionID":                                true,
		http.MethodGet + " /api/v1/auth/mcp/oauth/grants":                                                true,
		http.MethodGet + " /api/v1/auth/cli/bindings":                                                    true,
		http.MethodGet + " /api/v1/auth/cli/projects":                                                    true,
		http.MethodGet + " /api/v1/auth/cli/sessions":                                                    true,
		http.MethodGet + " /api/v1/auth/cli/status":                                                      true,
		http.MethodGet + " /api/v1/auth/cli/sync-service":                                                true,
		http.MethodGet + " /api/v1/auth/account/password":                                                true,
		http.MethodGet + " /api/v1/public/healthz":                                                       true,
		http.MethodGet + " /api/v1/public/version":                                                       true,
		http.MethodPost + " /api/v1/auth/cli/bindings":                                                   true,
		http.MethodPost + " /api/v1/auth/cli/bindings/:projectID/:bindingID/revoke":                      true,
		http.MethodPost + " /api/v1/auth/cli/bindings/:projectID/reauthorize":                            true,
		http.MethodPost + " /api/v1/auth/cli/logout":                                                     true,
		http.MethodPost + " /api/v1/auth/cli/pairing/decision":                                           true,
		http.MethodPost + " /api/v1/auth/cli/pairing/poll":                                               true,
		http.MethodPost + " /api/v1/auth/cli/pairing/start":                                              true,
		http.MethodPost + " /api/v1/auth/cli/refresh":                                                    true,
		http.MethodPost + " /api/v1/auth/cli/sessions/:id/revoke":                                        true,
		http.MethodPost + " /api/v1/auth/register":                                                       true,
		http.MethodPost + " /api/v1/auth/login":                                                          true,
		http.MethodPost + " /api/v1/auth/refresh":                                                        true,
		http.MethodPost + " /api/v1/auth/logout":                                                         true,
		http.MethodPost + " /api/v1/auth/account/password":                                               true,
		http.MethodPost + " /api/v1/auth/demo":                                                           true,
		http.MethodPost + " /api/v1/auth/mcp/oauth/consent/:transactionID/decision":                      true,
		http.MethodPost + " /api/v1/auth/consumers/daily-brief/verify-credential":                        true,
		http.MethodPost + " /api/v1/auth/consumers/lwc/verify-credential":                                true,
		http.MethodPost + " /api/v1/auth/consumers/lwc/onboarding/identity":                              true,
		http.MethodPost + " /api/v1/auth/consumers/lwc/accounts/check":                                   true,
		http.MethodPost + " /api/v1/auth/consumers/lwc/mcp-consent/transactions/lookup":                  true,
		http.MethodPost + " /api/v1/auth/consumers/lwc/mcp-consent/transactions/:transactionID/decision": true,
		http.MethodPost + " /api/v1/auth/consumers/lwc/mcp-consent/grants/list":                          true,
		http.MethodGet + " /api/v1/auth/consumers/lwc/management/catalog":                                true,
		http.MethodGet + " /api/v1/auth/consumers/lwc/registration/settings":                             true,
		http.MethodGet + " /api/v1/auth/consumers/lwc/management/registration-settings":                  true,
		http.MethodGet + " /api/v1/auth/consumers/lwc/management/me":                                     true,
		http.MethodGet + " /api/v1/auth/consumers/lwc/management/users":                                  true,
		http.MethodPost + " /api/v1/auth/consumers/lwc/management/grants":                                true,
		http.MethodGet + " /api/v1/auth/consumers/lwc/management/operations/:operationID":                true,
		http.MethodPatch + " /api/v1/auth/consumers/lwc/management/users/:userID":                        true,
		http.MethodPatch + " /api/v1/auth/consumers/lwc/management/users/:userID/legacy":                 true,
		http.MethodPatch + " /api/v1/auth/consumers/lwc/management/registration-settings":                true,
		http.MethodDelete + " /api/v1/auth/consumers/lwc/management/users/:userID":                       true,
		http.MethodPost + " /api/v1/auth/mcp/oauth/register":                                             true,
		http.MethodPost + " /api/v1/auth/mcp/oauth/token":                                                true,
	}

	got := make(map[string]bool)
	for _, route := range router.Routes() {
		got[route.Method+" "+route.Path] = true
	}
	if len(got) != len(want) {
		t.Fatalf("Auth production routes = %#v, want exactly %#v", got, want)
	}
	for route := range want {
		if !got[route] {
			t.Errorf("Auth production router is missing %s", route)
		}
	}
	for route := range got {
		if !want[route] {
			t.Errorf("Auth production router exposed unapproved route %s", route)
		}
	}
}

func TestLWCConsumerPolicySchema2BridgeUsesFrozenBFFIdentity(t *testing.T) {
	policy := lwcConsumerPolicy(config.Config{ConfigSchemaVersion: 2, AuthEnvironment: "dev",
		AuthServiceURL: "https://auth.dev.rayer.idv.tw", SyncServiceURL: "https://untrusted.example"})
	if !policy.Enabled || policy.Resource != "https://llm-wiki-bff-dev-580854833715.asia-east1.run.app" ||
		policy.CallerServiceAccount != "lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com" ||
		policy.VerifierAudience != "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app" {
		t.Fatalf("schema-2 bridge policy=%+v", policy)
	}
	if got := lwcConsumerPolicy(config.Config{ConfigSchemaVersion: 2, AuthEnvironment: "dev", AuthServiceURL: "https://auth.dev.attacker.test"}); got.Enabled {
		t.Fatalf("untrusted schema-2 config enabled consumer policy: %+v", got)
	}
}

func TestProductionRouterReturnsFreshRequestIDsAndExposesThemToCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{JWTSecret: "test-secret", AllowedOrigins: []string{"https://frontend.example"}}, true, nil,
		&syssettings.FakeStore{Enabled: true}, true)
	serve := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://localhost"+path, nil)
		req.Header.Set("X-Request-ID", "caller-controlled-id")
		req.Header.Set("Origin", "https://frontend.example")
		if method == http.MethodOptions {
			req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		id := response.Header().Get("X-Request-ID")
		if uuid.Validate(id) != nil || id == "caller-controlled-id" {
			t.Fatalf("%s %s returned non-server request ID %q (status %d)", method, path, id, response.Code)
		}
		return response
	}
	health := serve(http.MethodGet, "/api/v1/public/healthz")
	if !strings.EqualFold(health.Header().Get("Access-Control-Expose-Headers"), "X-Request-ID") {
		t.Fatalf("Auth CORS does not expose the response request ID: %v", health.Header())
	}
	if fallback := serve(http.MethodGet, "/private/fallback"); fallback.Code != http.StatusNotFound {
		t.Fatalf("fallback status=%d, want 404", fallback.Code)
	}
	if preflight := serve(http.MethodOptions, "/api/v1/public/healthz"); preflight.Code < 200 || preflight.Code >= 300 {
		t.Fatalf("preflight status=%d, want successful CORS control response", preflight.Code)
	}
}

func TestProductionRouterCoversAutomaticRedirectsWithOneIncomingRoot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	router := newProductionRouter(config.Config{JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true}, true)
	for _, test := range []struct {
		path, wantLocation string
		status             int
	}{
		{path: "/api/v1/public/version", status: http.StatusOK},
		{path: "/api/v1/public/version/", wantLocation: "/api/v1/public/version", status: http.StatusMovedPermanently},
	} {
		traceID, parentID, traceParent := otlpfixture.NewParent(t)
		baselineRequest := httptest.NewRequest(http.MethodGet, test.path, nil)
		baselineRequest.Host = "localhost"
		baseline := httptest.NewRecorder()
		router.Engine.ServeHTTP(baseline, baselineRequest)
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Host = "localhost"
		request.Header.Set("traceparent", traceParent)
		request.Header.Set("X-Request-ID", "caller-controlled-id")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		requestID := response.Header().Get("X-Request-ID")
		if uuid.Validate(requestID) != nil || requestID == "caller-controlled-id" {
			t.Fatalf("%s returned invalid request ID %q", test.path, requestID)
		}
		if response.Code != test.status || response.Header().Get("Location") != test.wantLocation {
			t.Fatalf("%s response status=%d location=%q, want %d and %q", test.path, response.Code, response.Header().Get("Location"), test.status, test.wantLocation)
		}
		if response.Code != baseline.Code || response.Header().Get("Location") != baseline.Header().Get("Location") || response.Body.String() != baseline.Body.String() {
			t.Fatalf("%s lifecycle wrapper changed Gin response: wrapped=%d/%q/%q baseline=%d/%q/%q", test.path, response.Code, response.Header().Get("Location"), response.Body.String(), baseline.Code, baseline.Header().Get("Location"), baseline.Body.String())
		}
		matches := 0
		for _, span := range recorder.Ended() {
			if span.Name() != "http.request" || span.InstrumentationScope().Name != "llm-wiki-http/request" {
				continue
			}
			attrs := make(map[string]attribute.Value)
			for _, item := range span.Attributes() {
				attrs[string(item.Key)] = item.Value
			}
			if attrs["request.id"].AsString() != requestID {
				continue
			}
			matches++
			if span.SpanContext().TraceID().String() != traceID || span.Parent().SpanID().String() != parentID || attrs["http.response.status_code"].AsInt64() != int64(test.status) {
				t.Fatalf("%s lifecycle root lost its incoming parent or actual status", test.path)
			}
			wantRoute := "control.redirect"
			if test.wantLocation == "" {
				wantRoute = "/api/v1/public/version"
			}
			if attrs["http.route"].AsString() != wantRoute || attrs["request.phase"].AsString() != "terminal" {
				t.Fatalf("%s lifecycle attributes route=%q phase=%q", test.path, attrs["http.route"].AsString(), attrs["request.phase"].AsString())
			}
		}
		if matches != 1 {
			t.Fatalf("%s emitted %d lifecycle roots for request %s, want exactly one", test.path, matches, requestID)
		}
	}
}

func TestProductionRouterCapturesRejectedHostBeforeGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	router := newProductionRouter(config.Config{JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"}}, false, nil, &syssettings.FakeStore{Enabled: true}, true)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`))
	request.Host = "rejected.example.test"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("host guard status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	for _, span := range recorder.Ended() {
		if span.InstrumentationScope().Name != "llm-wiki-auth/operations" || span.Name() != "auth.login" {
			continue
		}
		attrs := map[string]string{}
		for _, attr := range span.Attributes() {
			if attr.Value.Type() == attribute.STRING {
				attrs[string(attr.Key)] = attr.Value.AsString()
			}
		}
		if attrs["auth.guard.stage"] != "host" || attrs["auth.guard.outcome"] != "rejected" {
			t.Fatalf("host guard span attributes = %#v", attrs)
		}
		return
	}
	t.Fatal("production Auth router did not capture the early host rejection")
}

func TestProductionRouterCapturesOversizedBodyGuardOutcome(t *testing.T) {
	const bodyCanary = "PRIVATE_AUTH_BODY_CANARY"
	gin.SetMode(gin.TestMode)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	router := newProductionRouter(config.Config{JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"}}, false, nil, &syssettings.FakeStore{Enabled: true}, true)
	body := bytes.Repeat([]byte(bodyCanary), int(auth.MaxRequestBodyBytes/int64(len(bodyCanary))+1))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	request.Host = "auth.example.test"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("body guard status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	for _, span := range recorder.Ended() {
		if span.InstrumentationScope().Name != "llm-wiki-auth/operations" || span.Name() != "auth.register" {
			continue
		}
		attrs := map[string]string{}
		for _, attr := range span.Attributes() {
			if attr.Value.Type() == attribute.STRING {
				attrs[string(attr.Key)] = attr.Value.AsString()
				if strings.Contains(attr.Value.AsString(), bodyCanary) {
					t.Fatal("raw Auth request body reached span attributes")
				}
			}
		}
		if attrs["auth.guard.host.outcome"] != "passed" || attrs["auth.guard.body.outcome"] != "rejected" {
			t.Fatalf("body guard span attributes = %#v", attrs)
		}
		return
	}
	t.Fatal("production Auth router did not capture the oversized body guard")
}

func TestProductionRouterUsesRegisteredActionRootsAndMCPTokenClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	router := newProductionRouter(config.Config{
		JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"},
	}, false, nil, &syssettings.FakeStore{Enabled: true}, true)
	requests := []struct {
		method, path, wantSpan, wantOperation string
	}{
		{http.MethodPost, "/api/v1/auth/login", "auth.login", "login"},
		{http.MethodPost, "/api/v1/auth/register", "auth.register", "register"},
		{http.MethodPost, "/api/v1/auth/refresh", "auth.refresh", "refresh"},
		{http.MethodPost, "/api/v1/auth/logout", "auth.logout", "logout"},
		{http.MethodPost, "/api/v1/auth/cli/pairing/start", "auth.pairing", "pairing_start"},
		{http.MethodPost, "/api/v1/auth/cli/pairing/poll", "auth.pairing", "pairing_poll"},
		{http.MethodGet, "/api/v1/auth/cli/sessions", "auth.session", "session_list"},
		{http.MethodDelete, "/api/v1/auth/cli/sessions/session-canary", "auth.session", "session_revoke"},
		{http.MethodGet, "/api/v1/auth/cli/bindings", "auth.binding", "binding_list"},
		{http.MethodPost, "/api/v1/auth/cli/bindings", "auth.binding", "binding_create"},
		{http.MethodPost, "/api/v1/auth/mcp/oauth/register", "auth.mcp.register", "mcp_register"},
		{http.MethodGet, "/api/v1/auth/mcp/oauth/authorize", "auth.mcp.authorize", "mcp_authorize"},
		{http.MethodPost, "/api/v1/auth/mcp/oauth/token", "auth.mcp.token", "mcp_token"},
		{http.MethodPost, "/api/v1/auth/consumers/daily-brief/verify-credential", "auth.mcp.verify", "mcp_verify_credential"},
		{http.MethodGet, "/api/v1/auth/mcp/oauth/consent/transaction-canary", "auth.mcp.consent", "mcp_consent_read"},
		{http.MethodPost, "/api/v1/auth/mcp/oauth/consent/transaction-canary/decision", "auth.mcp.consent", "mcp_consent_decision"},
		{http.MethodGet, "/api/v1/auth/mcp/oauth/grants", "auth.mcp.grants", "mcp_grants_list"},
		{http.MethodDelete, "/api/v1/auth/mcp/oauth/grants/grant-canary", "auth.mcp.grants", "mcp_grants_revoke"},
	}
	for _, test := range requests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			before := len(recorder.Ended())
			request := httptest.NewRequest(test.method, "http://auth.example.test"+test.path, strings.NewReader(`{}`))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			spans := recorder.Ended()
			for _, span := range spans[before:] {
				if span.InstrumentationScope().Name != "llm-wiki-auth/operations" || span.Name() != test.wantSpan {
					continue
				}
				attrs := map[string]string{}
				for _, attr := range span.Attributes() {
					if attr.Value.Type() == attribute.STRING {
						attrs[string(attr.Key)] = attr.Value.AsString()
					}
				}
				if attrs["auth.operation"] != test.wantOperation {
					t.Fatalf("registered route %q operation = %q, want %q", test.path, attrs["auth.operation"], test.wantOperation)
				}
				if strings.Contains(span.Name(), "canary") || strings.Contains(attrs["auth.operation"], "canary") {
					t.Fatalf("route value reached action identity: name=%q attrs=%#v", span.Name(), attrs)
				}
				return
			}
			t.Fatalf("registered route %s did not emit %s with operation %q; status=%d spans=%#v", test.method, test.wantSpan, test.wantOperation, response.Code, spans[before:])
		})
	}
}

func TestMCPOAuthBrowserCORSAllowsGrantRevocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret: "cors-test-key", AllowedOrigins: []string{"http://localhost:13000"},
	}, true, nil, &syssettings.FakeStore{Enabled: true}, true)
	request := httptest.NewRequest(http.MethodOptions, "http://localhost:18081/api/v1/auth/mcp/oauth/grants/grant-fixture", nil)
	request.Header.Set("Origin", "http://localhost:13000")
	request.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	request.Header.Set("Access-Control-Request-Headers", "authorization")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "http://localhost:13000" ||
		!strings.Contains(response.Header().Get("Access-Control-Allow-Methods"), http.MethodDelete) {
		t.Fatalf("MCP revoke preflight status=%d origin=%q methods=%q", response.Code,
			response.Header().Get("Access-Control-Allow-Origin"), response.Header().Get("Access-Control-Allow-Methods"))
	}
}

func TestDailyBriefCredentialVerifierDoesNotEnableBrowserCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret: "test-secret", AllowedOrigins: []string{"http://localhost:13000"},
	}, true, nil, &syssettings.FakeStore{Enabled: true}, true)
	request := httptest.NewRequest(http.MethodOptions, "http://localhost:18081/api/v1/auth/consumers/daily-brief/verify-credential", nil)
	request.Header.Set("Origin", "http://localhost:13000")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Authorization, X-LWC-User-Credential")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Header().Get("Access-Control-Allow-Origin") != "" || response.Header().Get("Access-Control-Allow-Headers") != "" {
		t.Fatalf("credential verifier preflight unexpectedly enabled browser CORS: %v", response.Header())
	}
}

func TestMCPOAuthPublicRoutesLimitBurstsBeforeAdditionalAuthorityWrites(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	database := fmt.Sprintf("lwc-389-rate-%d", time.Now().UnixNano())
	client, err := firestoreclient.NewClientWithDatabaseAndScope("lwc-389-rate-test", database, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	environment := "lwc-389-rate-limit"
	cfg := config.Config{
		JWTSecret: "rate-limit-test-key", FirestoreDatabaseID: database, AuthSessionEnvironment: environment,
		AuthServiceURL: "http://localhost:18081", SyncServiceURL: "http://localhost:18080",
		AllowedOrigins: []string{"http://localhost:13000"},
	}
	router := newProductionRouter(cfg, true, client, &syssettings.FakeStore{Enabled: true}, true)
	registrationBody := `{"client_name":"Hermes test","redirect_uris":["http://127.0.0.1:43123/oauth/callback"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"scope":"mcp:query"}`
	for index := 0; index < 11; index++ {
		request := httptest.NewRequest(http.MethodPost, "http://localhost:18081/api/v1/auth/mcp/oauth/register", strings.NewReader(registrationBody))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		want := http.StatusCreated
		if index == 10 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("registration request %d status=%d want=%d body=%s", index+1, response.Code, want, response.Body.String())
		}
	}
	countCollection := func(name string) int {
		t.Helper()
		sum := sha256.Sum256([]byte(environment))
		collection := "mcp_oauth_" + name + "_" + hex.EncodeToString(sum[:6])
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		docs, err := client.Raw().Collection(collection).Documents(ctx).GetAll()
		if err != nil {
			t.Fatalf("read MCP OAuth %s authority count: %v", name, err)
		}
		return len(docs)
	}
	if count := countCollection("clients"); count != 10 {
		t.Fatalf("registered client count=%d, want exactly 10 successful requests", count)
	}
	for _, endpoint := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/v1/auth/mcp/oauth/authorize"},
		{method: http.MethodPost, path: "/api/v1/auth/mcp/oauth/token", body: "grant_type=unsupported"},
	} {
		for index := 0; index < 11; index++ {
			request := httptest.NewRequest(endpoint.method, "http://localhost:18081"+endpoint.path, strings.NewReader(endpoint.body))
			if endpoint.method == http.MethodPost {
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			want := http.StatusUnauthorized
			if endpoint.method == http.MethodPost {
				want = http.StatusBadRequest
			}
			if index == 10 {
				want = http.StatusTooManyRequests
			}
			if response.Code != want {
				t.Fatalf("%s %s request %d status=%d want=%d", endpoint.method, endpoint.path, index+1, response.Code, want)
			}
		}
	}
	if clients, transactions := countCollection("clients"), countCollection("transactions"); clients != 10 || transactions != 0 {
		t.Fatalf("public route burst wrote unexpected authority records: clients=%d transactions=%d", clients, transactions)
	}
}

func TestLocalAuthDemoRouteIsMountedAndFailsClosedWithoutIdentityStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true}, true)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/v1/auth/demo", nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || len(response.Result().Cookies()) != 0 {
		t.Fatalf("passwordless Demo route status=%d cookies=%d; want 503 without cookie", response.Code, len(response.Result().Cookies()))
	}
}

func TestDeployedDemoRouteIsMountedAndFailsClosedWithoutIdentityStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"}, AuthDemoUserID: "configured-demo-user", AuthDemoUserEmail: "demo@example.test", AuthDemoUserRole: "member"}, false, nil, &syssettings.FakeStore{Enabled: true}, true)
	registered := false
	for _, route := range router.Routes() {
		if route.Method == http.MethodPost && route.Path == "/api/v1/auth/demo" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("deployed formal Demo route is not registered")
	}

	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "unavailable identity store", want: http.StatusServiceUnavailable},
		{name: "identity input rejected", body: `{"user_id":"attacker"}`, want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/demo", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.want || len(recorder.Result().Cookies()) != 0 {
				t.Fatalf("Demo status=%d cookies=%d body=%s; want %d without cookie", recorder.Code, len(recorder.Result().Cookies()), recorder.Body.String(), test.want)
			}
		})
	}
}

func TestProductionRouterWiresGoogleAuthRoutesWhenConfigured(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	client, err := firestoreclient.NewClientWithDatabase("lwc-315-test", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	router := newProductionRouter(config.Config{
		JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"}, AuthServiceURL: "https://auth.example.test",
		GoogleClientID: "client", GoogleClientSecret: "secret", GoogleIssuer: "https://accounts.google.com",
		GoogleJWKSURL: "https://www.googleapis.com/oauth2/v3/certs", GoogleTokenURL: "https://oauth2.googleapis.com/token",
		GoogleLoginRedirectURL: "https://auth.example.test/api/v1/auth/google/login/callback",
		GoogleLinkRedirectURL:  "https://auth.example.test/api/v1/auth/google/link/callback",
		GoogleCompletionURL:    "https://frontend.example/login",
	}, false, client, &syssettings.FakeStore{Enabled: true}, true)
	want := map[string]bool{
		"POST /api/v1/auth/google/start": true, "GET /api/v1/auth/google/start": true,
		"POST /api/v1/auth/google/login/start": true, "GET /api/v1/auth/google/login/start": true, "POST /api/v1/auth/google/link/start": true,
		"GET /api/v1/auth/google/callback": true, "GET /api/v1/auth/google/login/callback": true,
		"GET /api/v1/auth/google/link/callback": true, "POST /api/v1/auth/google/link/confirm": true,
		"POST /api/v1/auth/google/link/cancel": true, "GET /api/v1/auth/google/link/complete": true,
		"GET /api/v1/auth/google/complete": true, "GET /api/v1/auth/google/identity": true,
	}
	got := make(map[string]bool)
	for _, route := range router.Routes() {
		if strings.Contains(route.Path, "/google/") || strings.HasSuffix(route.Path, "/google/start") {
			got[route.Method+" "+route.Path] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("Google auth routes=%#v want=%#v", got, want)
	}
	for route := range want {
		if !got[route] {
			t.Errorf("missing Google auth route %s", route)
		}
	}
	// This is the consumer shape of Continue with Google: a normal browser
	// navigation uses GET and must reach the registered login-start handler.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://auth.example.test/api/v1/auth/google/login/start", nil)
	request.Header.Set("Origin", "https://frontend.example")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound || !strings.HasPrefix(recorder.Header().Get("Location"), "https://accounts.google.com/") {
		t.Fatalf("browser login start status=%d location=%q body=%s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
}

func TestProductionRouterWiresFormalLocalAuthHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true}, true)
	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register", "/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		if !registered[http.MethodPost+" "+path] {
			t.Fatalf("local Auth route %s is missing", path)
		}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/v1/auth/login", bytes.NewBufferString(`{"email":"demo@llm-wiki.dev","password":"demo123456"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || len(recorder.Result().Cookies()) != 0 {
		t.Fatalf("local login without Firestore status=%d cookies=%d; want 503 without cookie", recorder.Code, len(recorder.Result().Cookies()))
	}
	if strings.Contains(recorder.Body.String(), "access_token") {
		t.Fatal("unavailable login unexpectedly returned an access token")
	}
}

func TestProductionRouterFailsClosedAndRegistersCLIControlPlanePaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"}, AuthServiceURL: "https://auth.example.test",
	}, false, nil, &syssettings.FakeStore{Enabled: true}, true)
	want := []string{
		"POST /api/v1/auth/cli/pairing/start",
		"POST /api/v1/auth/cli/pairing/poll",
		"POST /api/v1/auth/cli/pairing/decision",
		"POST /api/v1/auth/cli/refresh",
		"POST /api/v1/auth/cli/logout",
		"GET /api/v1/auth/cli/status",
		"GET /api/v1/auth/cli/projects",
		"GET /api/v1/auth/cli/sessions",
		"DELETE /api/v1/auth/cli/sessions/:id",
		"POST /api/v1/auth/cli/sessions/:id/revoke",
		"GET /api/v1/auth/cli/bindings",
		"POST /api/v1/auth/cli/bindings",
		"POST /api/v1/auth/cli/bindings/:projectID/reauthorize",
		"DELETE /api/v1/auth/cli/bindings/:projectID/:bindingID",
		"POST /api/v1/auth/cli/bindings/:projectID/:bindingID/revoke",
	}
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range want {
		if !registered[route] {
			t.Errorf("missing CLI auth route %s", route)
		}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/cli/pairing/poll", strings.NewReader(`{"pairing_id":"test","polling_secret":"test"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("CLI pairing route without Firestore status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for range 30 {
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/cli/pairing/poll", strings.NewReader(`{"pairing_id":"test","polling_secret":"test"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("CF-Connecting-IP", "192.0.2.30")
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("poll request before rate limit status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/cli/pairing/poll", strings.NewReader(`{"pairing_id":"test","polling_secret":"test"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("CF-Connecting-IP", "192.0.2.30")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("CLI poll rate limit status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCLISessionRevokePostPassesExistingCORSPreflightContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret: "test-secret", AllowedHosts: []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"}, AuthServiceURL: "https://auth.example.test",
	}, false, nil, &syssettings.FakeStore{Enabled: true}, true)
	for _, path := range []string{
		"/api/v1/auth/cli/sessions/session-1/revoke",
		"/api/v1/auth/cli/bindings/project-1/binding-1/revoke",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodOptions, "http://auth.example.test"+path, nil)
		request.Header.Set("Origin", "https://frontend.example")
		request.Header.Set("Access-Control-Request-Method", http.MethodPost)
		request.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent || recorder.Header().Get("Access-Control-Allow-Methods") != "GET,POST,PATCH,DELETE,OPTIONS" || recorder.Header().Get("Access-Control-Allow-Origin") != "https://frontend.example" {
			t.Fatalf("revoke preflight for %s status=%d headers=%#v body=%s", path, recorder.Code, recorder.Header(), recorder.Body.String())
		}
	}
}

func TestProductionRouterCLIApprovalAndStatelessProductProxy(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	gin.SetMode(gin.TestMode)
	var proxiedPath, proxiedAuthorization, proxiedBody, proxiedSubject, proxiedUserID, proxiedRole string
	bff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxiedPath, proxiedAuthorization = r.URL.RequestURI(), r.Header.Get("Authorization")
		proxiedSubject, proxiedUserID, proxiedRole = r.Header.Get("X-LWC-Subject"), r.Header.Get("X-User-ID"), r.Header.Get("X-Role")
		body, _ := io.ReadAll(r.Body)
		proxiedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer bff.Close()
	client, err := firestoreclient.NewClientWithDatabase("lwc-346-auth-router", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := t.Context()
	userID := fmt.Sprintf("lwc-346-router-user-%d", time.Now().UnixNano())
	if _, err := client.Raw().Collection("users").Doc(userID).Set(ctx, map[string]interface{}{
		"email": "owner@example.test", "role": "user", "status": auth.AccountActive, "auth_version": int64(0),
	}); err != nil {
		t.Fatal(err)
	}
	const jwtSecret = "lwc-346-router-jwt-secret"
	cfg := config.Config{
		JWTSecret: jwtSecret, FirestoreDatabaseID: "lwc-346-auth-router", AuthServiceURL: "https://auth.example.test", SyncServiceURL: bff.URL,
		AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"},
	}
	router := newProductionRouter(cfg, false, client, &syssettings.FakeStore{Enabled: true}, true)
	webToken, err := auth.GenerateAccessToken(userID, "user", jwtSecret, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, "http://auth.example.test"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}
	start := request(http.MethodPost, "/api/v1/auth/cli/pairing/start", `{"client_name":"test-cli"}`, "")
	if start.Code != http.StatusCreated {
		t.Fatalf("pairing start status=%d body=%s", start.Code, start.Body.String())
	}
	var pairing auth.CLIStartPairingResponse
	if err := json.Unmarshal(start.Body.Bytes(), &pairing); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pairing.VerificationURL, "https://frontend.example/cli-pairing?user_code=") {
		t.Fatalf("verification URL=%q", pairing.VerificationURL)
	}
	if got := request(http.MethodGet, "/api/v1/auth/cli/pairing/decision?user_code="+pairing.UserCode, "", webToken); got.Code != http.StatusNotFound {
		t.Fatalf("GET verification unexpectedly had a decision route: %d", got.Code)
	}
	pending := request(http.MethodPost, "/api/v1/auth/cli/pairing/poll", fmt.Sprintf(`{"pairing_id":%q,"polling_secret":%q}`, pairing.PairingID, pairing.PollingSecret), "")
	if pending.Code != http.StatusAccepted {
		t.Fatalf("pairing poll before approval status=%d body=%s", pending.Code, pending.Body.String())
	}
	decisionBody := fmt.Sprintf(`{"user_code":%q,"decision":"approve"}`, pairing.UserCode)
	if decision := request(http.MethodPost, "/api/v1/auth/cli/pairing/decision", decisionBody, webToken); decision.Code != http.StatusOK {
		t.Fatalf("Web approval status=%d body=%s", decision.Code, decision.Body.String())
	}
	grant := request(http.MethodPost, "/api/v1/auth/cli/pairing/poll", fmt.Sprintf(`{"pairing_id":%q,"polling_secret":%q}`, pairing.PairingID, pairing.PollingSecret), "")
	if grant.Code != http.StatusOK {
		t.Fatalf("pairing grant status=%d body=%s", grant.Code, grant.Body.String())
	}
	var result auth.CLIPairingPollResult
	if err := json.Unmarshal(grant.Body.Bytes(), &result); err != nil || result.Credentials == nil {
		t.Fatalf("pairing grant=%#v error=%v", result, err)
	}
	cliToken := result.Credentials.AccessToken
	if status := request(http.MethodGet, "/api/v1/auth/cli/status", "", cliToken); status.Code != http.StatusOK || !strings.Contains(status.Body.String(), result.Credentials.SessionID) {
		t.Fatalf("CLI status=%d body=%s", status.Code, status.Body.String())
	}

	proxyRequest := httptest.NewRequest(http.MethodGet, "http://auth.example.test/api/v1/auth/cli/projects?page_size=1", nil)
	proxyRequest.Header.Set("Authorization", "Bearer "+cliToken)
	proxyRequest.Header.Set("X-LWC-Subject", "spoofed-subject")
	proxyRequest.Header.Set("X-User-ID", "spoofed-user")
	proxyRequest.Header.Set("X-Role", "admin")
	proxyResponse := httptest.NewRecorder()
	router.ServeHTTP(proxyResponse, proxyRequest)
	if proxyResponse.Code != http.StatusOK || proxiedPath != "/api/v1/auth/cli/projects?page_size=1" || proxiedAuthorization != "Bearer "+cliToken {
		t.Fatalf("legacy proxy status=%d body=%s path=%q authorization-preserved=%v", proxyResponse.Code, proxyResponse.Body.String(), proxiedPath, proxiedAuthorization == "Bearer "+cliToken)
	}
	if proxiedSubject != "" || proxiedUserID != "" || proxiedRole != "" {
		t.Fatalf("legacy proxy forwarded naked identity headers: subject=%q user=%q role=%q", proxiedSubject, proxiedUserID, proxiedRole)
	}
	proxiedPath, proxiedBody = "", ""
	bindingBody := `{"project_id":"opaque-project","wiki_id":"wiki-1","host":"https://auth.example.test"}`
	bindingRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/cli/bindings", strings.NewReader(bindingBody))
	bindingRequest.Header.Set("Content-Type", "application/json")
	bindingRequest.Header.Set("Authorization", "Bearer "+cliToken)
	bindingResponse := httptest.NewRecorder()
	router.ServeHTTP(bindingResponse, bindingRequest)
	if bindingResponse.Code != http.StatusOK || proxiedPath != "/api/v1/auth/cli/bindings" || proxiedBody != bindingBody {
		t.Fatalf("legacy binding proxy status=%d body=%s path=%q forwarded-body=%q", bindingResponse.Code, bindingResponse.Body.String(), proxiedPath, proxiedBody)
	}

	secondPairing := request(http.MethodPost, "/api/v1/auth/cli/pairing/start", `{"client_name":"second-cli"}`, "")
	if secondPairing.Code != http.StatusCreated {
		t.Fatalf("second pairing start status=%d body=%s", secondPairing.Code, secondPairing.Body.String())
	}
	var another auth.CLIStartPairingResponse
	if err := json.Unmarshal(secondPairing.Body.Bytes(), &another); err != nil {
		t.Fatal(err)
	}
	cliDecision := request(http.MethodPost, "/api/v1/auth/cli/pairing/decision", fmt.Sprintf(`{"user_code":%q,"decision":"approve"}`, another.UserCode), cliToken)
	if cliDecision.Code != http.StatusForbidden {
		t.Fatalf("CLI credential reached Web approval: status=%d body=%s", cliDecision.Code, cliDecision.Body.String())
	}
	secondPending := request(http.MethodPost, "/api/v1/auth/cli/pairing/poll", fmt.Sprintf(`{"pairing_id":%q,"polling_secret":%q}`, another.PairingID, another.PollingSecret), "")
	if secondPending.Code != http.StatusAccepted {
		t.Fatalf("CLI attempted approval changed pairing state: status=%d", secondPending.Code)
	}
	if revokeSession := request(http.MethodPost, "/api/v1/auth/cli/sessions/"+result.Credentials.SessionID+"/revoke", "", webToken); revokeSession.Code != http.StatusOK {
		t.Fatalf("Web session revoke status=%d body=%s", revokeSession.Code, revokeSession.Body.String())
	}
	if revoked := request(http.MethodGet, "/api/v1/auth/cli/status", "", cliToken); revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked CLI access status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestProductionRouterUsesHostOnlyRefreshCookiePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret:      "test-secret",
		AllowedHosts:   []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"},
	}, false, nil, &syssettings.FakeStore{Enabled: true}, true)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/logout", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("deployed logout status = %d, want %d", recorder.Code, http.StatusOK)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("deployed logout returned %d cookies, want one", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "__Host-lwc_refresh" || cookie.Domain != "" || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != -1 {
		t.Fatalf("deployed logout cookie attributes: name=%q domain=%q path=%q secure=%v httpOnly=%v sameSite=%v maxAge=%d", cookie.Name, cookie.Domain, cookie.Path, cookie.Secure, cookie.HttpOnly, cookie.SameSite, cookie.MaxAge)
	}
}

func TestProductionRouterUsesDurableRefreshAuthorityAcrossRouterInstances(t *testing.T) {
	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	gin.SetMode(gin.TestMode)
	client, err := firestoreclient.NewClientWithDatabase("lwc-320-router", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	repo := auth.NewIdentityRepository(client.Raw())
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("passphrase-320"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("router-durable-user-320-%d", suffix)
	email := fmt.Sprintf("router-durable-320-%d@example.test", suffix)
	if err := repo.ProvisionPasswordUser(t.Context(), auth.PasswordUserProvisioning{
		UserID: userID, DisplayEmail: email, CanonicalEmail: email,
		PasswordHash: string(passwordHash), ProjectID: "default",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{JWTSecret: "router-key-320", FirestoreDatabaseID: "lwc-320-router", AllowedHosts: []string{"auth.example.test"}, AllowedOrigins: []string{"https://frontend.example"}}
	first := newProductionRouter(cfg, false, client, &syssettings.FakeStore{Enabled: true}, true)
	loginRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":"passphrase-320"}`, email)))
	loginRequest.Header.Set("Content-Type", "application/json")
	login := httptest.NewRecorder()
	first.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK {
		t.Fatalf("durable login status=%d body=%s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Host-lwc_refresh" || cookies[0].Value == "" {
		t.Fatalf("durable login cookies=%#v", cookies)
	}

	second := newProductionRouter(cfg, false, client, &syssettings.FakeStore{Enabled: true}, true)
	refreshRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/refresh", nil)
	refreshRequest.AddCookie(cookies[0])
	refresh := httptest.NewRecorder()
	second.ServeHTTP(refresh, refreshRequest)
	if refresh.Code != http.StatusOK {
		t.Fatalf("durable refresh status=%d body=%s", refresh.Code, refresh.Body.String())
	}
	var rotated *http.Cookie
	for _, cookie := range refresh.Result().Cookies() {
		if cookie.Name == "__Host-lwc_refresh" {
			rotated = cookie
			break
		}
	}
	if rotated == nil || rotated.Value == cookies[0].Value {
		t.Fatalf("durable refresh cookie=%#v, want rotated cookie", rotated)
	}
	logoutRequest := httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(rotated)
	logout := httptest.NewRecorder()
	second.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusOK {
		t.Fatalf("durable logout status=%d body=%s", logout.Code, logout.Body.String())
	}
	revokedRefresh := httptest.NewRecorder()
	refreshRequest = httptest.NewRequest(http.MethodPost, "http://auth.example.test/api/v1/auth/refresh", nil)
	refreshRequest.AddCookie(rotated)
	second.ServeHTTP(revokedRefresh, refreshRequest)
	if revokedRefresh.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after durable logout status=%d body=%s", revokedRefresh.Code, revokedRefresh.Body.String())
	}
}

func TestAuthHostAllowlistRejectsBeforeRouteHandling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret:      "test-secret",
		AllowedHosts:   []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"},
	}, false, nil, &syssettings.FakeStore{Enabled: true}, true)

	recorder := httptest.NewRecorder()
	for _, rawURL := range []string{"http://wrong.example.test/api/v1/public/healthz", "http://auth.example.test./api/v1/public/healthz"} {
		recorder = httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, rawURL, nil)
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("wrong Host %q status = %d, want %d", rawURL, recorder.Code, http.StatusBadRequest)
		}
		if strings.Contains(recorder.Body.String(), "listening") {
			t.Fatalf("wrong Host %q reached route handling", rawURL)
		}
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://auth.example.test/api/v1/public/healthz", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("allowed Host status = %d, want %d", recorder.Code, http.StatusOK)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "http://auth.example.test/healthz", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("former public health path status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestAuthLocalHostAllowlistAcceptsLocalhostOnlyInLocalMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	localRouter := newProductionRouter(config.Config{JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true}, true)
	for _, host := range []string{"localhost:8081", "127.0.0.1:8081"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/v1/public/healthz", nil)
		localRouter.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("local Host %q status = %d, want %d", host, recorder.Code, http.StatusOK)
		}
	}

	productionRouter := newProductionRouter(config.Config{JWTSecret: "test-secret", AllowedOrigins: []string{"https://frontend.example"}}, false, nil, &syssettings.FakeStore{Enabled: true}, true)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/public/healthz", nil)
	productionRouter.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("production localhost status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestAuthCORSAllowsConfiguredMethodsAndHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{
		JWTSecret:      "test-secret",
		AllowedHosts:   []string{"auth.example.test"},
		AllowedOrigins: []string{"https://frontend.example"},
	}, false, nil, &syssettings.FakeStore{Enabled: true}, true)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "http://auth.example.test/api/v1/auth/login", nil)
	request.Header.Set("Origin", "https://frontend.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Content-Type, Authorization")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("CORS preflight status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Methods"); got != "GET,POST,PATCH,DELETE,OPTIONS" {
		t.Fatalf("Access-Control-Allow-Methods = %q, want %q", got, "GET,POST,PATCH,DELETE,OPTIONS")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type,Authorization,X-Lwc-User-Credential" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, "Content-Type,Authorization,X-Lwc-User-Credential")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://frontend.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want configured origin", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want true", got)
	}
}

func TestAuthRequestBodyLimitRejectsOversizedLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true}, true)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/v1/auth/login", strings.NewReader(`{"email":"demo@llm-wiki.dev","password":"`+strings.Repeat("x", 64<<10)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized login status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestAuthRequestBodyLimitPreservesSmallMalformedLoginStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{JWTSecret: "test-secret"}, true, nil, &syssettings.FakeStore{Enabled: true}, true)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/v1/auth/login", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("login without Firestore status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}
