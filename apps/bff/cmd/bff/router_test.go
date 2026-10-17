package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/observability/otlpfixture"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestProductionRouterKeepsAuthCompatibilityLane(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := newProductionRouter(
		config.Config{JWTSecret: "test-secret"},
		true,
		nil,
		nil,
		handlerv1.New(nil, nil, nil, nil, nil, nil),
		&syssettings.FakeStore{Enabled: true},
		nil,
	)

	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register", "/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		if !hasRoute(router, http.MethodPost, path) {
			t.Fatalf("BFF production router is missing compatibility route POST %s", path)
		}
	}

	request := func(path string, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "http://localhost"+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		return recorder
	}
	if got := request("/api/v1/auth/login", `{"email":"demo@llm-wiki.dev","password":"demo123456"}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("local compatibility login without Firestore status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	badHost := httptest.NewRequest(http.MethodPost, "http://example.test/api/v1/auth/login", strings.NewReader(`{}`))
	badHostRecorder := httptest.NewRecorder()
	router.ServeHTTP(badHostRecorder, badHost)
	if badHostRecorder.Code != http.StatusBadRequest {
		t.Fatalf("local compatibility auth accepted Host %q: status=%d, want 400", badHost.Host, badHostRecorder.Code)
	}
	if got := request("/api/v1/auth/register", `{}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("local compatibility register status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := request("/api/v1/auth/refresh", "").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("local compatibility refresh without Firestore status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := request("/api/v1/auth/logout", "").Code; got != http.StatusOK {
		t.Fatalf("local compatibility logout status = %d, want %d", got, http.StatusOK)
	}
	if got := request("/api/v1/auth/login", `{}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("local compatibility login without Firestore status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := request("/api/v1/auth/login", `{"email":"demo@llm-wiki.dev","password":"`+strings.Repeat("x", 64<<10)+`"}`).Code; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized local compatibility login status = %d, want %d", got, http.StatusRequestEntityTooLarge)
	}

	unavailable := newProductionRouter(
		config.Config{JWTSecret: "test-secret", AllowedOrigins: []string{"https://frontend.example"}},
		false,
		nil,
		nil,
		handlerv1.New(nil, nil, nil, nil, nil, nil),
		&syssettings.FakeStore{Enabled: true},
		nil,
	)
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register", "/api/v1/auth/refresh"} {
		if got := servePost(unavailable, path).Code; got != http.StatusServiceUnavailable {
			t.Fatalf("unavailable compatibility %s status = %d, want %d", path, got, http.StatusServiceUnavailable)
		}
	}
	logout := servePost(unavailable, "/api/v1/auth/logout")
	if logout.Code != http.StatusOK {
		t.Fatalf("unavailable compatibility logout status = %d, want %d", logout.Code, http.StatusOK)
	}
	cookies := logout.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("legacy compatibility logout returned %d cookies, want one", len(cookies))
	}
	if cookies[0].Name != "refresh_token" || cookies[0].Domain != "rayer.idv.tw" || cookies[0].Path != "/" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].MaxAge != -1 {
		t.Fatalf("legacy compatibility logout cookie attributes: count=%d name=%q domain=%q path=%q secure=%v httpOnly=%v sameSite=%v maxAge=%d", len(cookies), cookies[0].Name, cookies[0].Domain, cookies[0].Path, cookies[0].Secure, cookies[0].HttpOnly, cookies[0].SameSite, cookies[0].MaxAge)
	}

	for _, path := range []string{"/api/v1/public/config", "/api/v1/public/version", "/api/v1/query/config"} {
		if !hasRoute(router, http.MethodGet, path) {
			t.Fatalf("BFF production router is missing GET %s", path)
		}
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/exports"},
		{http.MethodGet, "/api/v1/exports"},
		{http.MethodGet, "/api/v1/exports/:exportID/status"},
		{http.MethodPost, "/api/v1/exports/:exportID/download"},
		{http.MethodGet, "/api/v1/projects/:pid/profile"},
		{http.MethodPut, "/api/v1/projects/:pid/profile"},
		{http.MethodPost, "/api/v1/projects/:pid/profile/candidates/:candidateID/confirm"},
		{http.MethodPost, "/api/v1/projects/:pid/profile/candidates/:candidateID/retry"},
		{http.MethodPost, "/api/v1/projects/:pid/profile/derivation/retry"},
		{http.MethodGet, "/api/v1/projects/:pid/profile/jobs/:jobID"},
	} {
		if !hasRoute(router, route.method, route.path) {
			t.Fatalf("BFF production router is missing %s %s", route.method, route.path)
		}
	}
	if got := serveGet(router, "/api/v1/query/config").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("legacy public query config status=%d, want %d", got, http.StatusServiceUnavailable)
	}
}

func TestProductionRouterReturnsFreshRequestIDsForSuccessAndControlResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(config.Config{JWTSecret: "test-secret", AllowedOrigins: []string{"https://frontend.example"}}, true,
		nil, nil, handlerv1.New(nil, nil, nil, nil, nil, nil), &syssettings.FakeStore{Enabled: true}, nil)
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

	first := serve(http.MethodGet, "/api/v1/public/version")
	second := serve(http.MethodGet, "/api/v1/public/version")
	if !strings.EqualFold(first.Header().Get("Access-Control-Expose-Headers"), "X-Request-ID") {
		t.Fatalf("BFF CORS does not expose the response request ID: %v", first.Header())
	}
	if first.Header().Get("X-Request-ID") == second.Header().Get("X-Request-ID") {
		t.Fatal("separate API exchanges reused the same request ID")
	}
	if fallback := serve(http.MethodGet, "/private/fallback"); fallback.Code != http.StatusNotFound {
		t.Fatalf("fallback status=%d, want 404", fallback.Code)
	}
	if preflight := serve(http.MethodOptions, "/api/v1/public/version"); preflight.Code < 200 || preflight.Code >= 300 {
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
	router := newProductionRouter(config.Config{JWTSecret: "test-secret"}, true,
		nil, nil, handlerv1.New(nil, nil, nil, nil, nil, nil), &syssettings.FakeStore{Enabled: true}, nil)
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

func TestProductionRouterFinalizesPanicsBeforeGinRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousGinWriter, previousGinErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = io.Discard, io.Discard
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = previousGinWriter, previousGinErrorWriter })
	previousProvider := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})
	previousLogger := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	router := newProductionRouter(config.Config{JWTSecret: "test-secret"}, true,
		nil, nil, handlerv1.New(nil, nil, nil, nil, nil, nil), &syssettings.FakeStore{Enabled: true}, nil)
	// Add a test-only route to the real production router so Gin's existing
	// Recovery middleware exercises the same request lifecycle chain.
	router.GET("/test/request-lifecycle-panic", func(*gin.Context) { panic("PRIVATE_PANIC_CANARY") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost/test/request-lifecycle-panic", nil))
	requestID := response.Header().Get("X-Request-ID")
	if response.Code != http.StatusInternalServerError || uuid.Validate(requestID) != nil || strings.Contains(response.Body.String(), "PRIVATE_PANIC_CANARY") {
		t.Fatalf("production-router recovery response changed or lost request ID: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "http.request" {
		t.Fatalf("production-router panic did not finalize exactly one HTTP root: %+v", spans)
	}
	var statusCode int64
	var requestOutcome, phase string
	for _, attr := range spans[0].Attributes() {
		switch string(attr.Key) {
		case "http.response.status_code":
			statusCode = attr.Value.AsInt64()
		case "request.outcome":
			requestOutcome = attr.Value.AsString()
		case "request.phase":
			phase = attr.Value.AsString()
		}
	}
	if statusCode != http.StatusInternalServerError || requestOutcome != "failure" || phase != "terminal" {
		t.Fatalf("production-router panic root lost truthful failure terminal: status=%d outcome=%q phase=%q", statusCode, requestOutcome, phase)
	}
	if strings.Contains(logs.String(), "PRIVATE_PANIC_CANARY") || strings.Count(logs.String(), `"msg":"http.request.terminal"`) != 1 || !strings.Contains(logs.String(), `"status_code":500`) || !strings.Contains(logs.String(), `"outcome":"failure"`) {
		t.Fatalf("production-router panic terminal missing, duplicated, or leaked panic text: %s", logs.String())
	}
}

func TestTrialDemoRestrictionCoversProfileAndExportForExactUserIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handlerv1.New(nil, nil, nil, nil, nil, nil)
	h.SetPipelineQuotaConfig(2, 3600, 1, []string{"trial-user"})
	h.SetAccountLookup(func(_ context.Context, _ string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Role: "member"}, nil
	})
	router := newProductionRouter(
		config.Config{JWTSecret: "test-secret"}, true, nil, nil, h,
		&syssettings.FakeStore{Enabled: true}, nil,
	)
	request := func(userID, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		token, err := auth.GenerateAccessToken(userID, "member", "test-secret")
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Project-ID", "project-a")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects/project-a/profile"},
		{http.MethodPost, "/api/v1/projects/project-a/profile/candidates/candidate-a/confirm"},
		{http.MethodGet, "/api/v1/exports"},
		{http.MethodPost, "/api/v1/exports"},
	} {
		if got := request("trial-user", route.method, route.path).Code; got != http.StatusForbidden {
			t.Errorf("trial user %s %s status = %d, want %d", route.method, route.path, got, http.StatusForbidden)
		}
	}

	if got := request("non-listed-user", http.MethodGet, "/api/v1/projects/project-a/profile").Code; got == http.StatusForbidden {
		t.Fatal("non-listed user was blocked from Profile")
	}
	if got := request("non-listed-user", http.MethodGet, "/api/v1/exports").Code; got == http.StatusForbidden {
		t.Fatal("non-listed user was blocked from Export")
	}
}

func TestProfileSavePreflightAllowsFrontendPutOnlyFromConfiguredOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const allowedOrigin = "https://wiki.dev.rayer.idv.tw"
	router := newProductionRouter(
		config.Config{JWTSecret: "test-secret", AllowedOrigins: []string{allowedOrigin}},
		false,
		nil,
		nil,
		handlerv1.New(nil, nil, nil, nil, nil, nil),
		&syssettings.FakeStore{Enabled: true},
		nil,
	)
	preflight := func(origin string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/api/v1/projects/project-1/profile", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPut)
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-project-id")
		router.ServeHTTP(recorder, req)
		return recorder
	}

	allowed := preflight(allowedOrigin)
	if allowed.Code != http.StatusNoContent {
		t.Fatalf("allowed Profile PUT preflight status = %d, want %d", allowed.Code, http.StatusNoContent)
	}
	if got := allowed.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	methods := strings.Split(allowed.Header().Get("Access-Control-Allow-Methods"), ",")
	if !containsFold(methods, http.MethodPut) {
		t.Fatalf("Access-Control-Allow-Methods = %q, want PUT", allowed.Header().Get("Access-Control-Allow-Methods"))
	}
	for _, name := range []string{"authorization", "content-type", "x-project-id"} {
		if !containsFold(strings.Split(allowed.Header().Get("Access-Control-Allow-Headers"), ","), name) {
			t.Errorf("Access-Control-Allow-Headers = %q, missing %s", allowed.Header().Get("Access-Control-Allow-Headers"), name)
		}
	}

	rejected := preflight("https://unlisted.example")
	if got := rejected.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unlisted origin received Access-Control-Allow-Origin %q", got)
	}
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

func TestProductionRouterUsesSharedCLIAndWebAuthAuthorities(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("local Firestore emulator required")
	}
	if !strings.HasPrefix(endpoint, "127.0.0.1:") && !strings.HasPrefix(endpoint, "localhost:") {
		t.Fatal("production router auth test requires a loopback Firestore emulator")
	}
	gin.SetMode(gin.TestMode)
	projectName := fmt.Sprintf("demo-lwc346-router-%d", time.Now().UnixNano())
	fsClient, err := firestore.NewClientWithDatabase(projectName, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsClient.Raw().Close() })
	ctx := context.Background()
	userID := fmt.Sprintf("routeruser%d", time.Now().UnixNano())
	projectID := "owned"
	if _, err := fsClient.Raw().Collection("users").Doc(userID).Set(ctx, map[string]any{
		"status": auth.AccountActive, "auth_version": int64(0), "role": "member",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fsClient.Raw().Collection("projects").Doc(userID+"_"+projectID).Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "name": "owned project",
	}); err != nil {
		t.Fatal(err)
	}
	const jwtSecret, environment = "lwc346-router-secret", "lwc346-router-emulator"
	sessions := auth.NewRefreshSessionAuthority(fsClient.Raw(), environment)
	cli, err := sessions.IssueCLISession(ctx, userID, "test client", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	web, err := auth.GenerateToken(userID, jwtSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{JWTSecret: jwtSecret, AuthSessionEnvironment: environment, AllowedOrigins: []string{"https://wiki.example.test"}}
	router := newProductionRouter(cfg, false, nil, fsClient, handlerv1.New(nil, fsClient, nil, nil, nil, nil), &syssettings.FakeStore{Enabled: true}, nil)
	request := func(token, pid string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Project-ID", pid)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder.Code
	}
	if got := request(cli.AccessToken, projectID); got != http.StatusOK {
		t.Fatalf("valid CLI request status=%d, want %d", got, http.StatusOK)
	}
	if got := request(cli.AccessToken, "not-owned"); got != http.StatusForbidden {
		t.Fatalf("CLI request for unowned project status=%d, want %d", got, http.StatusForbidden)
	}
	if err := sessions.RevokeCLISession(ctx, userID, cli.SessionID); err != nil {
		t.Fatal(err)
	}
	if got := request(cli.AccessToken, projectID); got != http.StatusUnauthorized {
		t.Fatalf("revoked CLI access status=%d, want %d", got, http.StatusUnauthorized)
	}
	if got := request(web, "not-owned"); got != http.StatusOK {
		t.Fatalf("valid Web request changed by CLI authority wiring: status=%d, want %d", got, http.StatusOK)
	}
}

func servePost(router http.Handler, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
	return recorder
}

func serveGet(router http.Handler, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func hasRoute(router interface{ Routes() gin.RoutesInfo }, method, path string) bool {
	for _, route := range router.Routes() {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
