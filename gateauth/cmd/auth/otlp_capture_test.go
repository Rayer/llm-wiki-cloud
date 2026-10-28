package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/auth"
	"github.com/rayer/gateauth/internal/config"
	"github.com/rayer/gateauth/internal/observability"
	"github.com/rayer/gateauth/internal/observability/otlpfixture"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestOTLPCaptureAuthSafeMetadataAndRejectOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	capture := otlpfixture.New(t)
	project := "synthetic-project"
	if otlpfixture.CloudMode() {
		project = os.Getenv("LWC_OTLP_QA_PROJECT")
	}
	provider := capture.Provider(t, "lwc-qa-auth", "local-fixture", project)
	router := gin.New()
	router.Use(observability.TraceContextMiddleware(), observability.AuthHTTPMiddleware())
	router.GET("/.well-known/oauth-authorization-server", auth.MCPOAuthMetadataHandler("https://auth.example.test"))
	router.POST("/api/v1/auth/mcp/oauth/register", auth.MCPOAuthRegisterHandler(&auth.MCPOAuthService{}))
	router.GET("/api/v1/auth/google/identity", auth.JWTAuth(config.Config{JWTSecret: "synthetic-only-secret"}), (&auth.GoogleOAuthService{}).IdentitySummaryHandler())

	cases := []struct {
		name, method, path, body, span string
		status                         int
	}{
		{name: "metadata", method: http.MethodGet, path: "/.well-known/oauth-authorization-server", span: "auth.mcp.metadata", status: http.StatusOK},
		{name: "invalid_registration", method: http.MethodPost, path: "/api/v1/auth/mcp/oauth/register", body: `{}`, span: "auth.mcp.register", status: http.StatusBadRequest},
		{name: "unauthenticated_identity", method: http.MethodGet, path: "/api/v1/auth/google/identity", span: "auth.google.identity", status: http.StatusUnauthorized},
	}
	wants := make(map[string]struct{ traceID, parentID string })
	for _, tc := range cases {
		traceID, parentID, traceParent := otlpfixture.NewParent(t)
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		request.Header.Set("traceparent", traceParent)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("%s status=%d, want %d", tc.name, response.Code, tc.status)
		}
		wants[tc.span] = struct{ traceID, parentID string }{traceID, parentID}
		t.Logf("OTLP_QA_CASE=UC1 name=%s trace_id=%s parent_id=%s expected_span=%s outcome=%d", tc.name, traceID, parentID, tc.span, tc.status)
	}
	capture.Flush(t, provider)
	capture.LogIdentities(t, "UC1")
	if otlpfixture.CloudMode() {
		return
	}
	got := make(map[string]*tracev1.Span)
	for _, span := range capture.Spans() {
		if _, expected := wants[span.Name]; expected {
			if got[span.Name] != nil {
				t.Fatalf("duplicate Auth span %q", span.Name)
			}
			got[span.Name] = span
		}
	}
	for name, want := range wants {
		span := got[name]
		if span == nil || fmt.Sprintf("%x", span.TraceId) != want.traceID || fmt.Sprintf("%x", span.ParentSpanId) != want.parentID || fmt.Sprintf("%x", span.SpanId) == want.parentID {
			t.Fatalf("Auth span %q has wrong causal identity: %+v want=%+v", name, span, want)
		}
	}
}
