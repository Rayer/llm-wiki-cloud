package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/buildinfo"
	"github.com/rayer/llm-wiki-bff/internal/config"
	firestoreclient "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/middleware"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
)

func main() {
	cfg, err := config.Load(".")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	localMode := cfg.LocalCloudScope != ""
	if localMode && strings.TrimSpace(cfg.JWTSecret) == "" {
		log.Fatal("local cloud signing key is missing")
	}

	fsClient, err := firestoreclient.NewClientWithDatabase(cfg.GCPProject, cfg.FirestoreDatabaseID, "", "")
	if err != nil {
		log.Fatalf("Firestore client unavailable: %v", err)
	}
	if localMode {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = fsClient.CheckAvailable(ctx)
		cancel()
		if err != nil {
			_ = fsClient.Close()
			log.Fatalf("local Firestore target is unavailable: %v", err)
		}
	}
	demoReady := ensureDemoAccountAtStartup(cfg, fsClient.Raw())

	settingsStore := syssettings.NewStore(fsClient.Raw(), cfg.RegistrationEnabled)

	provider, err := observability.InitMetrics(context.Background(), observabilityServiceName(os.Getenv("K_SERVICE")), observability.GetProjectID())
	if err != nil {
		log.Printf("[observability] WARNING: metrics init failed (continuing): %v", err)
	} else {
		defer func() {
			if err := provider.Shutdown(context.Background()); err != nil {
				log.Printf("[observability] metrics shutdown error: %v", err)
			}
		}()
	}

	r := newProductionRouter(cfg, localMode, fsClient, settingsStore, demoReady)

	listenHost := ":"
	if localMode {
		listenHost = "127.0.0.1:"
	}
	log.Printf("Auth service listening on %s%s", listenHost, cfg.Port)
	log.Fatal(r.Run(listenHost + cfg.Port))
}

func newProductionRouter(cfg config.Config, localMode bool, fsClient *firestoreclient.Client, settingsStore syssettings.RegistrationGate, demoReady bool) *gin.Engine {
	r := gin.Default()
	r.Use(authHostAllowlist(cfg, localMode))
	r.Use(middleware.SecurityHeaders(true))
	r.Use(middleware.LatencyMiddleware())

	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOriginsFor(localMode),
		AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowHeaders:     []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	}))

	authRoutes := r.Group("/api/v1/auth")
	authRoutes.Use(auth.RequestBodyLimit())
	cookiePolicy := auth.HostRefreshCookiePolicy()
	if localMode {
		cookiePolicy = auth.LocalRefreshCookiePolicy()
	}
	var demoUsers auth.DemoUserLookup
	var sessions *auth.RefreshSessionAuthority
	if fsClient == nil || fsClient.Raw() == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth routes require Firestore"})
		}
		oversizedBody := auth.RejectOversizedRequestBody()
		authRoutes.POST("/login", oversizedBody, unavailable)
		authRoutes.POST("/register", oversizedBody, unavailable)
		authRoutes.POST("/refresh", oversizedBody, unavailable)
		authRoutes.POST("/logout", auth.LogoutHandlerWithCookiePolicy(cookiePolicy))
		registerUnavailableCLIRoutes(authRoutes)
	} else {
		identityRepository := auth.NewIdentityRepository(fsClient.Raw())
		sessionEnvironment := strings.TrimSpace(cfg.AuthSessionEnvironment)
		if sessionEnvironment == "" {
			sessionEnvironment = strings.TrimSpace(cfg.FirestoreDatabaseID)
			if sessionEnvironment == "" {
				sessionEnvironment = "default"
			}
		}
		sessions = auth.NewRefreshSessionAuthorityWithConfig(fsClient.Raw(), auth.SessionAuthorityConfig{
			Environment: sessionEnvironment, Migration: auth.RefreshSessionMigrationMode(cfg.AuthSessionMigration),
		})
		demoUsers = identityRepository
		registerCLIAuthRoutes(authRoutes, cfg, fsClient.Raw(), sessions, sessionEnvironment)
		authRoutes.POST("/login", middleware.NewRateLimiter(10, time.Minute), auth.LoginHandlerWithRepositoryAndSessionAuthority(identityRepository, cfg.JWTSecret, cookiePolicy, sessions))
		authRoutes.POST("/register", middleware.NewRateLimiter(5, time.Minute), auth.RegisterHandlerWithRepository(identityRepository, cfg.JWTSecret, settingsStore))
		authRoutes.POST("/refresh", auth.RefreshHandlerWithSessionAuthority(sessions, cfg.JWTSecret, cookiePolicy))
		authRoutes.POST("/logout", auth.LogoutHandlerWithSessionAuthority(sessions, cfg.JWTSecret, cookiePolicy))
		if cfg.GoogleClientID != "" {
			google := auth.NewGoogleOAuthServiceWithSessionAuthority(auth.GoogleConfig{
				ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret, Issuer: cfg.GoogleIssuer,
				JWKSURL: cfg.GoogleJWKSURL, TokenURL: cfg.GoogleTokenURL,
				LoginRedirectURL: cfg.GoogleLoginRedirectURL, LinkRedirectURL: cfg.GoogleLinkRedirectURL,
				CompletionURL: cfg.GoogleCompletionURL, AuthServiceURL: cfg.AuthServiceURL, AllowedOrigins: cfg.AllowedOrigins,
			}, fsClient.Raw(), identityRepository, settingsStore, cfg.JWTSecret, sessions)
			authRoutes.POST("/google/start", google.StartHandler(""))
			authRoutes.GET("/google/start", google.StartHandler(""))
			authRoutes.POST("/google/login/start", google.StartHandler(auth.OAuthFlowLogin))
			authRoutes.GET("/google/login/start", google.StartHandler(auth.OAuthFlowLogin))
			authRoutes.POST("/google/link/start", auth.JWTAuthWithAccountLookup(cfg, auth.FirestoreAccountLookup(fsClient.Raw())), google.PrepareStartHandler(auth.OAuthFlowLink))
			authRoutes.GET("/google/callback", google.CallbackHandler(auth.OAuthFlowLogin))
			authRoutes.GET("/google/login/callback", google.CallbackHandler(auth.OAuthFlowLogin))
			authRoutes.GET("/google/link/callback", google.CallbackHandler(auth.OAuthFlowLink))
			linkRoutes := authRoutes.Group("/google/link")
			linkRoutes.Use(auth.JWTAuthWithAccountLookup(cfg, auth.FirestoreAccountLookup(fsClient.Raw())))
			linkRoutes.POST("/confirm", google.ConfirmLinkHandler())
			linkRoutes.POST("/cancel", google.CancelLinkHandler())
			linkRoutes.GET("/complete", google.CompletionReadHandler())
			authRoutes.GET("/google/complete", google.CompletionHandler())
			authRoutes.GET("/google/identity", auth.JWTAuthWithAccountLookup(cfg, auth.FirestoreAccountLookup(fsClient.Raw())), google.IdentitySummaryHandler())
		}
	}
	demoHandler := auth.DemoLoginHandlerWithRepository(
		demoUsers, cfg.AuthDemoUserID, cfg.AuthDemoUserEmail, cfg.AuthDemoUserRole, cfg.JWTSecret, cookiePolicy, sessions,
	)
	if !demoReady {
		demoHandler = func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo login unavailable"})
		}
	}
	authRoutes.POST("/demo", middleware.NewRateLimiter(10, time.Minute), demoHandler)

	r.GET("/api/v1/public/healthz", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.GET("/api/v1/public/version", buildinfo.Handler())

	return r
}

func ensureDemoAccountAtStartup(cfg config.Config, fsClient *firestore.Client) bool {
	if strings.TrimSpace(cfg.AuthDemoUserID) == "" && strings.TrimSpace(cfg.AuthDemoUserEmail) == "" && strings.TrimSpace(cfg.AuthDemoUserRole) == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	created, err := auth.EnsureDemoAccount(ctx, fsClient, cfg.AuthDemoUserID, cfg.AuthDemoUserEmail, cfg.AuthDemoUserRole)
	if err != nil {
		log.Printf("[demo] startup ensure unavailable: %v", err)
		return false
	}
	log.Printf("[demo] startup identity ready (created=%t)", created)
	return true
}

func authHostAllowlist(cfg config.Config, localMode bool) gin.HandlerFunc {
	allowed := make(map[string]struct{})
	for _, host := range cfg.AllowedHostsFor(localMode) {
		allowed[strings.ToLower(host)] = struct{}{}
	}
	return func(c *gin.Context) {
		host := requestHost(c.Request.Host)
		if (!localMode && isLocalHost(host)) || host == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid Host header"})
			return
		}
		if _, ok := allowed[host]; !ok {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid Host header"})
			return
		}
		c.Next()
	}
}

func requestHost(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return host
	}
	return raw
}

func isLocalHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1"
}

const legacyAuthObservabilityServiceName = "llm-wiki-auth-dev"

func observabilityServiceName(kService string) string {
	if serviceName := strings.TrimSpace(kService); serviceName != "" {
		return serviceName
	}
	return legacyAuthObservabilityServiceName
}
