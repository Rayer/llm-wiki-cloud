package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/rayer/gateauth/internal/auth"
	"github.com/rayer/gateauth/internal/buildinfo"
	"github.com/rayer/gateauth/internal/config"
	firestoreclient "github.com/rayer/gateauth/internal/firestore"
	"github.com/rayer/gateauth/internal/middleware"
	"github.com/rayer/gateauth/internal/observability"
	"github.com/rayer/gateauth/internal/syssettings"
	"google.golang.org/api/idtoken"
)

func main() {
	if err := run(); err != nil {
		log.Printf("auth service failed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	build := buildinfo.Current()
	cfg, err := config.LoadAuthFile(strings.TrimSpace(os.Getenv("LWC_APP_CONFIG_PATH")))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	traceProvider, traceErr := observability.InitTracesWithProject("lwc-auth", build.Revision, cfg.GCPProject)
	if traceErr != nil {
		log.Printf("[observability] WARNING: trace init failed (continuing)")
	} else {
		defer observability.ShutdownTraces(traceProvider)
	}

	localMode := cfg.LocalCloudScope != ""
	if localMode && strings.TrimSpace(cfg.JWTSecret) == "" {
		return fmt.Errorf("local cloud signing key is missing")
	}

	fsClient, err := firestoreclient.NewClientWithDatabaseAndScope(cfg.GCPProject, cfg.FirestoreDatabaseID, "", "", cfg.LocalCloudScope)
	if err != nil {
		return fmt.Errorf("Firestore client unavailable: %w", err)
	}
	if localMode {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = fsClient.CheckAvailable(ctx)
		cancel()
		if err != nil {
			_ = fsClient.Close()
			return fmt.Errorf("local Firestore target is unavailable: %w", err)
		}
	}
	demoReady := ensureDemoAccountAtStartup(cfg, fsClient.Raw())

	settingsStore := syssettings.NewStore(fsClient.Raw(), cfg.RegistrationEnabled)

	if provider, err := observability.InitMetricsForMode(context.Background(), observabilityServiceName(os.Getenv("K_SERVICE")), cfg.GCPProject, localMode); err != nil {
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
	return r.Run(listenHost + cfg.Port)
}

func newProductionRouter(cfg config.Config, localMode bool, fsClient *firestoreclient.Client, settingsStore syssettings.RegistrationGate, demoReady bool) *observability.RequestLifecycleRouter {
	r := gin.Default()
	r.Use(observability.RequestLifecycleMiddleware())
	r.Use(observability.AuthHTTPMiddleware())
	r.Use(authHostAllowlist(cfg, localMode))
	r.Use(middleware.SecurityHeaders(true))
	r.Use(middleware.LatencyMiddleware())

	corsHandler := cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOriginsFor(localMode),
		AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowHeaders:     []string{"Content-Type", "Authorization", "X-LWC-User-Credential"},
		ExposeHeaders:    []string{"X-Request-ID"},
		AllowCredentials: true,
	})
	r.Use(observability.AuthCORSObserver(func(c *gin.Context) {
		if c.Request.URL.Path == "/api/v1/auth/consumers/daily-brief/verify-credential" {
			c.Next()
			return
		}
		corsHandler(c)
	}))

	authRoutes := r.Group("/api/v1/auth")
	authRoutes.Use(auth.RequestBodyLimit())
	cookiePolicy := auth.HostRefreshCookiePolicy()
	if localMode {
		cookiePolicy = auth.LocalRefreshCookiePolicy()
	}
	var demoUsers auth.DemoUserLookup
	var sessions *auth.RefreshSessionAuthority
	var management *auth.ManagementAuthority
	consumerResource := strings.TrimSpace(cfg.AuthConsumerPublicProjection.Resource)
	if consumerResource == "" {
		consumerResource = cfg.SyncServiceURL
	}
	if fsClient == nil || fsClient.Raw() == nil {
		unavailable := func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth routes require Firestore"})
		}
		oversizedBody := auth.RejectOversizedRequestBody()
		authRoutes.POST("/login", oversizedBody, unavailable)
		authRoutes.POST("/register", oversizedBody, unavailable)
		authRoutes.POST("/refresh", oversizedBody, unavailable)
		authRoutes.POST("/logout", auth.LogoutHandlerWithCookiePolicy(cookiePolicy))
		registerUnavailableCLIRoutes(authRoutes, cfg)
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
		management = auth.NewManagementAuthorityWithCatalog(fsClient.Raw(), sessionEnvironment, cfg.ManagementScopes)
		sessions.SetManagementAuthority(management, consumerResource)
		demoUsers = identityRepository
		registerCLIAuthRoutes(authRoutes, cfg, fsClient.Raw(), sessions)
		authRoutes.POST("/login", middleware.NewRateLimiter(10, time.Minute), auth.LoginHandlerWithManagement(identityRepository, cfg.JWTSecret, cookiePolicy, sessions, management, consumerResource))
		authRoutes.POST("/register", middleware.NewRateLimiter(5, time.Minute), auth.RegisterHandlerWithRepository(identityRepository, cfg.JWTSecret, settingsStore))
		authRoutes.POST("/refresh", auth.RefreshHandlerWithManagement(sessions, cfg.JWTSecret, cookiePolicy, management, consumerResource))
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
	demoHandler := auth.DemoLoginHandlerWithManagement(
		demoUsers, cfg.AuthDemoUserID, cfg.AuthDemoUserEmail, cfg.AuthDemoUserRole, cfg.JWTSecret, cookiePolicy, sessions, management, consumerResource,
	)
	if !demoReady {
		demoHandler = func(c *gin.Context) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo login unavailable"})
		}
	}
	authRoutes.POST("/demo", middleware.NewRateLimiter(10, time.Minute), demoHandler)
	var rawFirestore *firestore.Client
	if fsClient != nil {
		rawFirestore = fsClient.Raw()
	}
	consumerPolicy := lwcConsumerPolicy(cfg)
	callerValidator := auth.OIDCCallerValidator(idtoken.Validate)
	if localMode {
		callerValidator = auth.LocalFixtureOIDCValidator
	}
	var verifyCLISession auth.CLIAccessSessionVerifier
	var mcpOAuth *auth.MCPOAuthService
	if sessions != nil {
		verifyCLISession = sessions.VerifyCLIAccessSession
	}
	if rawFirestore != nil {
		databaseEnvironment := strings.TrimSpace(cfg.AuthSessionEnvironment)
		if databaseEnvironment == "" {
			databaseEnvironment = strings.TrimSpace(cfg.FirestoreDatabaseID)
		}
		if databaseEnvironment == "" {
			databaseEnvironment = "default"
		}
		mcpOAuth = auth.NewMCPOAuthServiceWithPolicy(rawFirestore, databaseEnvironment, cfg.AuthEnvironment,
			cfg.AuthServiceURL, cfg.SyncServiceURL, "", cfg.JWTSecret, cfg.AuthOAuthResources)
	}
	var settingsAuthority auth.RegistrationSettingsAuthority
	if settingsStore, ok := settingsStore.(*syssettings.Store); ok && settingsStore != nil {
		settingsAuthority = authRegistrationSettingsStore{store: settingsStore}
	}
	consumerRoutes := authRoutes.Group("/consumers/lwc")
	consumerRoutes.Use(middleware.NewRateLimiter(60, time.Minute))
	consumerRoutes.POST("/verify-credential", auth.LWCConsumerVerifyHandlerWithAuthorities(cfg, rawFirestore, consumerPolicy,
		callerValidator, verifyCLISession, management, mcpOAuth))
	consumerRoutes.POST("/onboarding/identity", auth.LWCOnboardingIdentityHandler(cfg, rawFirestore, consumerPolicy,
		callerValidator, verifyCLISession))
	consumerRoutes.POST("/accounts/check", auth.LWCConsumerAccountCheckHandler(rawFirestore, consumerPolicy, callerValidator))
	auth.RegisterMCPOAuthConsumerRoutes(consumerRoutes, cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, mcpOAuth)
	consumerRoutes.GET("/registration/settings", auth.ConsumerRegistrationSettingsHandler(settingsAuthority, consumerPolicy, callerValidator))
	consumerRoutes.GET("/management/catalog", auth.ManagementCatalogHandler(consumerPolicy, cfg.AuthConsumerPublicProjection, callerValidator))
	consumerRoutes.GET("/management/me", auth.ManagementMeHandler(cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, management))
	consumerRoutes.GET("/management/users", auth.ManagementUsersHandler(cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, management))
	consumerRoutes.POST("/management/grants", auth.ManagementGrantHandler(cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, management))
	consumerRoutes.GET("/management/operations/:operationID", auth.ManagementOperationHandler(cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, management))
	consumerRoutes.PATCH("/management/users/:userID", auth.ManagementAccountStatusHandler(cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, management))
	consumerRoutes.PATCH("/management/users/:userID/legacy", auth.ManagementLegacyAccountHandler(cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, management))
	consumerRoutes.GET("/management/registration-settings", auth.ManagementRegistrationSettingsGetHandler(cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, management, settingsAuthority))
	consumerRoutes.PATCH("/management/registration-settings", auth.ManagementRegistrationSettingsPatchHandler(cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, management, settingsAuthority))
	consumerRoutes.DELETE("/management/users/:userID", auth.ManagementAccountDeleteHandler(cfg, rawFirestore, consumerPolicy, callerValidator, verifyCLISession, management))
	passwordRoutes := authRoutes.Group("/account")
	if rawFirestore != nil {
		passwordRoutes.Use(auth.JWTAuthWithAccountLookup(cfg, auth.FirestoreAccountLookup(rawFirestore)), auth.WebOnly())
	}
	passwordRoutes.GET("/password", auth.PasswordSelfServiceStatusHandler(rawFirestore, cfg.AuthDemoUserID))
	passwordRoutes.POST("/password", middleware.NewRateLimiter(5, time.Minute), auth.ChangePasswordHandler(rawFirestore, cfg.AuthDemoUserID))
	auth.RegisterMCPOAuthRoutes(r, authRoutes, cfg, rawFirestore)

	r.GET("/api/v1/public/healthz", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.GET("/api/v1/public/version", authVersionHandler(cfg))

	return observability.WrapRequestLifecycleRouter(r)
}

type authRegistrationSettingsStore struct {
	store *syssettings.Store
}

func (s authRegistrationSettingsStore) GetRegistrationSettings(ctx context.Context) (auth.ManagementRegistrationSettings, error) {
	if s.store == nil {
		return auth.ManagementRegistrationSettings{}, fmt.Errorf("registration settings store unavailable")
	}
	settings, err := s.store.GetRegistrationSettings(ctx)
	return auth.ManagementRegistrationSettings{RegistrationEnabled: settings.RegistrationEnabled,
		EmailRegistrationEnabled: settings.EmailRegistrationEnabled, GoogleRegistrationEnabled: settings.GoogleRegistrationEnabled}, err
}

func (s authRegistrationSettingsStore) SetRegistrationSettingsInTransaction(ctx context.Context, tx *firestore.Transaction,
	master, email, google *bool) (auth.ManagementRegistrationSettings, error) {
	if s.store == nil {
		return auth.ManagementRegistrationSettings{}, fmt.Errorf("registration settings store unavailable")
	}
	settings, err := s.store.SetRegistrationSettingsInTransaction(ctx, tx, master, email, google)
	return auth.ManagementRegistrationSettings{RegistrationEnabled: settings.RegistrationEnabled,
		EmailRegistrationEnabled: settings.EmailRegistrationEnabled, GoogleRegistrationEnabled: settings.GoogleRegistrationEnabled}, err
}

func lwcConsumerPolicy(cfg config.Config) auth.LWCConsumerPolicy {
	if cfg.AuthEnvironment == "local" && cfg.LocalCloudScope != "" {
		return auth.LWCConsumerPolicy{
			Enabled: true, Environment: "local", Resource: strings.TrimSpace(cfg.AuthConsumerPublicProjection.Resource),
			CallerServiceAccount: auth.LocalFixtureCallerEmail, VerifierAudience: strings.TrimSpace(cfg.AuthServiceURL),
			Issuer: strings.TrimSpace(cfg.AuthServiceURL), AllowedCredentialKinds: []string{"web-access", "cli-access", "mcp-oauth"},
		}
	}
	if cfg.ConfigSchemaVersion == 2 && cfg.AuthEnvironment != "local" {
		verifier, err := config.LegacyLWCBridgeVerifier(cfg.AuthEnvironment, cfg.AuthServiceURL)
		if err != nil {
			return auth.LWCConsumerPolicy{}
		}
		return auth.LWCConsumerPolicy{
			Enabled: true, Environment: verifier.Environment, Resource: verifier.Resource,
			CallerServiceAccount: verifier.CallerServiceAccount, VerifierAudience: verifier.Audience,
			Issuer: cfg.AuthServiceURL, AllowedCredentialKinds: append([]string(nil), verifier.AllowedCredentialKinds...),
		}
	}
	var verifier config.AuthConsumerVerifier
	for _, candidate := range cfg.AuthConsumerVerifiers {
		if candidate.ID == "lwc-bff" && candidate.Environment == cfg.AuthEnvironment {
			verifier = candidate
			break
		}
	}
	return auth.LWCConsumerPolicy{
		Enabled:     cfg.ConfigSchemaVersion >= 3 && cfg.AuthEnvironment != "local" && verifier.ID == "lwc-bff",
		Environment: cfg.AuthEnvironment, Resource: verifier.Resource,
		CallerServiceAccount: verifier.CallerServiceAccount, VerifierAudience: verifier.Audience,
		Issuer: cfg.AuthServiceURL, AllowedCredentialKinds: append([]string(nil), verifier.AllowedCredentialKinds...),
	}
}

func authVersionHandler(cfg config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		info := buildinfo.Current()
		c.JSON(http.StatusOK, gin.H{
			"product_version": info.ProductVersion, "commit": info.Commit, "branch": info.Branch,
			"tag": info.Tag, "image_tag": info.ImageTag, "service": info.Service, "revision": info.Revision,
			"config_schema_version": cfg.ConfigSchemaVersion, "config_id": cfg.ConfigID,
		})
	}
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
			observability.RecordAuthGuard(c.Request.Context(), "host", "rejected")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid Host header"})
			return
		}
		if _, ok := allowed[host]; !ok {
			observability.RecordAuthGuard(c.Request.Context(), "host", "rejected")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid Host header"})
			return
		}
		observability.RecordAuthGuard(c.Request.Context(), "host", "passed")
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
