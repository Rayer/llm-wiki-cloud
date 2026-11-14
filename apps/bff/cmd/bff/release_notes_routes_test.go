package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/config"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
)

func TestPublicReleaseNotesRoutesAreUnauthenticatedNoStoreAndObservable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(
		config.Config{JWTSecret: "test-secret", AllowedOrigins: []string{"https://frontend.example"}}, true,
		nil, nil, handlerv1.New(nil, nil, nil, nil, nil, nil), &syssettings.FakeStore{Enabled: true}, nil)

	for _, path := range []string{
		"/api/v1/public/release-notes",
		"/api/v1/public/release-notes/1.2.3?locale=en",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Origin", "https://frontend.example")
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s status = %d, want unavailable without a configured store", path, recorder.Code)
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", path, recorder.Header().Get("Cache-Control"))
		}
		if recorder.Header().Get("X-Request-ID") == "" {
			t.Errorf("GET %s did not expose a request ID", path)
		}
		if recorder.Header().Get("Access-Control-Allow-Origin") != "https://frontend.example" {
			t.Errorf("GET %s Access-Control-Allow-Origin = %q", path, recorder.Header().Get("Access-Control-Allow-Origin"))
		}
	}
}
