package main

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/config"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
)

func TestProductionRouterRegistersRecompileAllAndBootstrapRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := newProductionRouter(
		config.Config{DevJWT: true, JWTSecret: "test-secret"},
		true,
		nil,
		nil,
		handlerv1.New(nil, nil, nil, nil, nil, nil),
		&syssettings.FakeStore{Enabled: true},
		nil,
	)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects/:pid/recompile-all/capability"},
		{http.MethodPost, "/api/v1/projects/:pid/recompile-all"},
		{http.MethodGet, "/api/v1/projects/:pid/profile/bootstrap-guidance"},
		{http.MethodGet, "/api/v1/projects/:pid/profile/guidance/:revision"},
		{http.MethodPost, "/api/v1/projects/:pid/profile/bootstrap-guidance/:revision/confirm"},
	} {
		if !hasRoute(router, route.method, route.path) {
			t.Fatalf("BFF production router is missing %s %s", route.method, route.path)
		}
	}
}
