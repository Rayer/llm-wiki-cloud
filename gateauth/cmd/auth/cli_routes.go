package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/auth"
	"github.com/rayer/gateauth/internal/config"
	"github.com/rayer/gateauth/internal/middleware"
)

func registerUnavailableCLIRoutes(routes *gin.RouterGroup, cfg config.Config) {
	unavailable := func(c *gin.Context) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CLI auth requires Firestore"})
	}
	routes.POST("/cli/pairing/start", middleware.NewRateLimiter(10, time.Minute), unavailable)
	routes.POST("/cli/pairing/poll", middleware.NewRateLimiter(30, time.Minute), unavailable)
	routes.POST("/cli/pairing/decision", middleware.NewRateLimiter(30, time.Minute), unavailable)
	routes.POST("/cli/refresh", unavailable)
	routes.POST("/cli/logout", unavailable)
	routes.GET("/cli/status", unavailable)
	routes.GET("/cli/sessions", unavailable)
	routes.DELETE("/cli/sessions/:id", unavailable)
	routes.POST("/cli/sessions/:id/revoke", unavailable)
	registerLegacyCLIProductProxy(routes, cfg.SyncServiceURL)
}

func registerCLIAuthRoutes(routes *gin.RouterGroup, cfg config.Config, fs *firestore.Client, sessions *auth.RefreshSessionAuthority) {
	accountLookup := auth.FirestoreAccountLookup(fs)
	service := auth.NewCLIAuthService(fs, sessions, cfg.JWTSecret, cliVerificationOrigin(cfg))

	public := routes.Group("/cli")
	public.Use(auth.RequestBodyLimit())
	public.POST("/pairing/start", middleware.NewRateLimiter(10, time.Minute), service.StartPairingHandler())
	public.POST("/pairing/poll", middleware.NewRateLimiter(30, time.Minute), service.PollPairingHandler())
	public.POST("/refresh", middleware.NewRateLimiter(30, time.Minute), service.RefreshHandler())
	public.POST("/logout", service.LogoutHandler())

	web := routes.Group("/cli")
	web.Use(auth.RequestBodyLimit(), auth.JWTAuthWithAccountLookupAndSessionVerifier(cfg, accountLookup, sessions.VerifyCLIAccessSession), auth.WebOnly())
	web.POST("/pairing/decision", middleware.NewRateLimiter(30, time.Minute), service.DecidePairingHandler())
	web.GET("/sessions", service.ListSessionsHandler())
	web.DELETE("/sessions/:id", service.RevokeSessionHandler())
	web.POST("/sessions/:id/revoke", service.RevokeSessionHandler())

	protected := routes.Group("/cli")
	protected.Use(auth.RequestBodyLimit(), auth.JWTAuthWithAccountLookupAndSessionVerifier(cfg, accountLookup, sessions.VerifyCLIAccessSession))
	protected.GET("/status", auth.CLIOnly(), service.StatusHandler())
	registerLegacyCLIProductProxy(routes, cfg.SyncServiceURL)
}

func cliVerificationOrigin(cfg config.Config) string {
	if len(cfg.AllowedOrigins) == 0 {
		return ""
	}
	return strings.TrimSpace(cfg.AllowedOrigins[0])
}

// Auth retains these old path locators only as a stateless, same-environment
// compatibility proxy. The BFF verifies the original bearer and owns all
// Project, binding, and product decisions.
func registerLegacyCLIProductProxy(routes *gin.RouterGroup, bffOrigin string) {
	proxy := legacyCLIProductProxy(bffOrigin)
	legacy := routes.Group("/cli")
	legacy.Use(auth.RequestBodyLimit())
	legacy.GET("/projects", proxy)
	legacy.GET("/bindings", proxy)
	legacy.GET("/sync-service", proxy)
	legacy.POST("/bindings", proxy)
	legacy.POST("/bindings/:projectID/reauthorize", proxy)
	legacy.DELETE("/bindings/:projectID/:bindingID", proxy)
	legacy.POST("/bindings/:projectID/:bindingID/revoke", proxy)
}

func legacyCLIProductProxy(bffOrigin string) gin.HandlerFunc {
	target, err := url.Parse(strings.TrimRight(strings.TrimSpace(bffOrigin), "/"))
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
		(target.Scheme != "https" && target.Scheme != "http") {
		return func(c *gin.Context) { c.JSON(http.StatusServiceUnavailable, gin.H{"error": "product API unavailable"}) }
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, `{"error":"product API unavailable"}`, http.StatusServiceUnavailable)
	}
	proxy.Director = func(request *http.Request) {
		request.URL.Scheme = target.Scheme
		request.URL.Host = target.Host
		request.Host = target.Host
		request.Header.Del("X-LWC-Subject")
		request.Header.Del("X-User-ID")
		request.Header.Del("X-Role")
	}
	return gin.WrapH(proxy)
}
