package main

import (
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"github.com/rayer/llm-wiki-bff/internal/middleware"
)

func registerUnavailableCLIRoutes(routes *gin.RouterGroup) {
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
	routes.GET("/cli/projects", unavailable)
	routes.GET("/cli/bindings", unavailable)
	routes.POST("/cli/bindings", unavailable)
	routes.POST("/cli/bindings/:projectID/reauthorize", unavailable)
	routes.DELETE("/cli/bindings/:projectID/:bindingID", unavailable)
	routes.POST("/cli/bindings/:projectID/:bindingID/revoke", unavailable)
}

func registerCLIAuthRoutes(routes *gin.RouterGroup, cfg config.Config, fs *firestore.Client, sessions *auth.RefreshSessionAuthority, environment string) {
	accountLookup := auth.FirestoreAccountLookup(fs)
	ownerAuthorizer := auth.FirestoreProjectOwnerAuthorizer(fs)
	verificationOrigin := ""
	for _, origin := range cfg.AllowedOrigins {
		if origin = strings.TrimSpace(origin); origin != "" {
			verificationOrigin = origin
			break
		}
	}
	service := auth.NewCLIAuthService(fs, sessions, cfg.JWTSecret, verificationOrigin)
	service.SetSyncBindingAuthority(auth.NewSyncBindingAuthority(fs, environment, cfg.AuthServiceURL))

	public := routes.Group("/cli")
	public.Use(auth.RequestBodyLimit())
	public.POST("/pairing/start", middleware.NewRateLimiter(10, time.Minute), service.StartPairingHandler())
	public.POST("/pairing/poll", middleware.NewRateLimiter(30, time.Minute), service.PollPairingHandler())
	public.POST("/refresh", middleware.NewRateLimiter(30, time.Minute), service.RefreshHandler())
	public.POST("/logout", service.LogoutHandler())

	web := routes.Group("/cli")
	web.Use(auth.RequestBodyLimit(), auth.JWTAuthWithAccountLookupAndSessionVerifier(cfg, accountLookup, sessions.VerifyCLIAccessSession, ownerAuthorizer), auth.WebOnly())
	web.POST("/pairing/decision", middleware.NewRateLimiter(30, time.Minute), service.DecidePairingHandler())
	web.GET("/sessions", service.ListSessionsHandler())
	web.DELETE("/sessions/:id", service.RevokeSessionHandler())
	web.POST("/sessions/:id/revoke", service.RevokeSessionHandler())

	protected := routes.Group("/cli")
	protected.Use(auth.RequestBodyLimit(), auth.JWTAuthWithAccountLookupAndSessionVerifier(cfg, accountLookup, sessions.VerifyCLIAccessSession, ownerAuthorizer))
	protected.GET("/status", auth.CLIOnly(), service.StatusHandler())
	protected.GET("/projects", auth.CLIOnly(), service.ListProjectsHandler())
	protected.GET("/bindings", service.ListBindingsHandler())
	protected.POST("/bindings", service.CreateBindingHandler())
	protected.POST("/bindings/:projectID/reauthorize", service.ReauthorizeBindingHandler())
	protected.DELETE("/bindings/:projectID/:bindingID", service.RevokeBindingHandler())
	protected.POST("/bindings/:projectID/:bindingID/revoke", service.RevokeBindingHandler())
}
