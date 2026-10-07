package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/spf13/viper"
)

// Default pipeline quota limits (LWC-138).
const (
	DefaultPipelineDailyLimit                                         = 2
	DefaultPipelineCooldownSeconds                                    = 3600
	DefaultPipelineMinNewRaw                                          = 1
	DefaultPipelineJobURL                                             = "https://run.googleapis.com/v2/projects/llm-wiki-cloud/locations/asia-east1/jobs/olw-pipeline:run"
	DefaultAuthServiceURL                                             = "https://auth.dev.rayer.idv.tw"
	DefaultQueryExpansionModel                                        = "deepseek-flash"
	DefaultAnswerSynthesisModel                                       = "deepseek-flash"
	DefaultQueryExpansionReasoning                      llm.Reasoning = llm.ReasoningNone
	DefaultAnswerSynthesisReasoning                     llm.Reasoning = llm.ReasoningNone
	DefaultQuerySelectionLimit                                        = 10
	DefaultQuerySelectionExplorationSlots                             = 1
	DefaultQuerySelectionEvidenceThreshold                            = 2
	DefaultQueryExpansionKeywordsPerAttempt                           = 24
	DefaultQueryExpansionAttempts                                     = 3
	DefaultQueryMatchingRareKeywordMaxDocumentFrequency               = 1
	MaxQuerySelectionLimit                                            = 1000
	MaxQueryExpansionKeywordsPerAttempt                               = 100
	MaxQueryExpansionAttempts                                         = 10
	MaxQuerySelectionEvidenceThreshold                                = 100
	MaxQueryMatchingRareKeywordDocumentFrequency                      = 1000
)

var ErrMixedQueryConfigAuthority = errors.New("query stage config path cannot be combined with explicit legacy query settings")

var defaultAllowedOrigins = []string{
	"https://wiki.rayer.idv.tw",
	"https://llm-wiki-frontend.vercel.app",
	"https://llm-wiki-bff-dev.rayer.idv.tw",
}

// Config holds application configuration loaded from config.toml.
type Config struct {
	GCPProject                  string
	Bucket                      string
	FirestoreDatabaseID         string
	LocalCloudScope             string
	UserID                      string
	ProjectID                   string
	Port                        string
	DeepSeekAPIKey              string
	QueryExpansionModel         string
	QueryExpansionReasoning     llm.Reasoning
	AnswerSynthesisModel        string
	AnswerSynthesisReasoning    llm.Reasoning
	JWTSecret                   string
	PipelineJobURL              string
	ExportJobURL                string
	ExportSigningServiceAccount string
	AllowedOrigins              []string
	AllowedHosts                []string
	Users                       []UserConfig

	// Pipeline quota (LWC-138). Env: PIPELINE_DAILY_LIMIT, PIPELINE_COOLDOWN_SECONDS,
	// PIPELINE_MIN_NEW_RAW, PIPELINE_DEMO_USER_IDS (comma-separated).
	PipelineDailyLimit      int
	PipelineCooldownSeconds int
	PipelineMinNewRaw       int
	PipelineDemoUserIDs     []string
	// AuthDemoUserID, AuthDemoUserEmail, and AuthDemoUserRole identify the
	// environment's Demo account for startup ensure and passwordless login.
	AuthDemoUserID    string
	AuthDemoUserEmail string
	AuthDemoUserRole  string

	// Registration gate (LWC-149). Env: REGISTRATION_ENABLED (true/false/1/0).
	// Nil means unset; resolution falls back to default true when Firestore doc is absent.
	RegistrationEnabled *bool

	// AuthServiceURL is the public URL of the dedicated auth service (LWC-258).
	// Env: AUTH_SERVICE_URL. Default: https://auth.dev.rayer.idv.tw
	AuthServiceURL string

	// AuthSessionEnvironment scopes durable refresh sessions. Env:
	// AUTH_SESSION_ENVIRONMENT. When unset, routers derive it from the selected
	// Firestore database (or "default" for the default database).
	AuthSessionEnvironment string
	// AuthSessionMigration controls legacy refresh-token import. Env:
	// AUTH_REFRESH_SESSION_MIGRATION. Valid values: disabled, legacy_read_through.
	AuthSessionMigration string
	// Google OIDC configuration (LWC-316). These values are required together
	// when any Google setting is provided.
	GoogleClientID         string
	GoogleClientSecret     string
	GoogleIssuer           string
	GoogleJWKSURL          string
	GoogleTokenURL         string
	GoogleLoginRedirectURL string
	GoogleLinkRedirectURL  string
	GoogleCompletionURL    string

	// QueryStageConfigPath selects the immutable external query composition.
	QueryStageConfigPath string

	// Query retrieval contract. Env: QUERY_SELECTION_LIMIT,
	// QUERY_SELECTION_EXPLORATION_SLOTS, QUERY_SELECTION_EVIDENCE_THRESHOLD,
	// QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT, QUERY_EXPANSION_ATTEMPTS,
	// QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY.
	QuerySelectionLimit                          int
	QuerySelectionExplorationSlots               int
	QuerySelectionEvidenceThreshold              int
	QueryExpansionKeywordsPerAttempt             int
	QueryExpansionAttempts                       int
	QueryMatchingRareKeywordMaxDocumentFrequency int
}

// UserConfig holds a hardcoded user for authentication.
type UserConfig struct {
	ID           string
	Email        string
	PasswordHash string
}

// Load reads config.toml from the given path and returns a Config.
func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("toml")
	v.AddConfigPath(path)
	v.SetDefault("port", "8080")
	v.SetDefault("pipeline_daily_limit", DefaultPipelineDailyLimit)
	v.SetDefault("pipeline_cooldown_seconds", DefaultPipelineCooldownSeconds)
	v.SetDefault("pipeline_min_new_raw", DefaultPipelineMinNewRaw)
	v.SetDefault("pipeline_job_url", DefaultPipelineJobURL)
	v.SetDefault("query_expansion_model", DefaultQueryExpansionModel)
	v.SetDefault("query_expansion_reasoning", DefaultQueryExpansionReasoning)
	v.SetDefault("answer_synthesis_model", DefaultAnswerSynthesisModel)
	v.SetDefault("answer_synthesis_reasoning", DefaultAnswerSynthesisReasoning)
	v.SetDefault("query_selection_limit", DefaultQuerySelectionLimit)
	v.SetDefault("query_selection_exploration_slots", DefaultQuerySelectionExplorationSlots)
	v.SetDefault("query_selection_evidence_threshold", DefaultQuerySelectionEvidenceThreshold)
	v.SetDefault("query_expansion_keywords_per_attempt", DefaultQueryExpansionKeywordsPerAttempt)
	v.SetDefault("query_expansion_attempts", DefaultQueryExpansionAttempts)
	v.SetDefault("query_matching_rare_keyword_max_document_frequency", DefaultQueryMatchingRareKeywordMaxDocumentFrequency)
	v.AutomaticEnv()
	v.BindEnv("deepseek_api_key")
	v.BindEnv("firestore_database_id", "FIRESTORE_DATABASE_ID")
	v.BindEnv("local_cloud_scope", "LOCAL_CLOUD_SCOPE")
	v.BindEnv("local_cloud_jwt_secret_file", "LOCAL_CLOUD_JWT_SECRET_FILE")
	v.BindEnv("pipeline_job_url", "PIPELINE_JOB_URL")
	v.BindEnv("export_job_url", "EXPORT_JOB_URL")
	v.BindEnv("export_signing_service_account", "EXPORT_SIGNING_SERVICE_ACCOUNT")
	v.BindEnv("allowed_origins", "ALLOWED_ORIGINS")
	v.BindEnv("allowed_hosts", "ALLOWED_HOSTS")
	v.BindEnv("pipeline_daily_limit", "PIPELINE_DAILY_LIMIT")
	v.BindEnv("pipeline_cooldown_seconds", "PIPELINE_COOLDOWN_SECONDS")
	v.BindEnv("pipeline_min_new_raw", "PIPELINE_MIN_NEW_RAW")
	v.BindEnv("pipeline_demo_user_ids", "PIPELINE_DEMO_USER_IDS")
	v.BindEnv("auth_demo_user_id", "AUTH_DEMO_USER_ID")
	v.BindEnv("auth_demo_user_email", "AUTH_DEMO_USER_EMAIL")
	v.BindEnv("auth_demo_user_role", "AUTH_DEMO_USER_ROLE")
	v.BindEnv("registration_enabled", "REGISTRATION_ENABLED")
	v.BindEnv("auth_service_url", "AUTH_SERVICE_URL")
	v.BindEnv("auth_session_environment", "AUTH_SESSION_ENVIRONMENT")
	v.BindEnv("auth_session_migration", "AUTH_REFRESH_SESSION_MIGRATION")
	v.BindEnv("google_client_id", "GOOGLE_CLIENT_ID")
	v.BindEnv("google_client_secret", "GOOGLE_CLIENT_SECRET")
	v.BindEnv("google_issuer", "GOOGLE_ISSUER")
	v.BindEnv("google_jwks_url", "GOOGLE_JWKS_URL")
	v.BindEnv("google_token_url", "GOOGLE_TOKEN_URL")
	v.BindEnv("google_login_redirect_url", "GOOGLE_LOGIN_REDIRECT_URL")
	v.BindEnv("google_link_redirect_url", "GOOGLE_LINK_REDIRECT_URL")
	v.BindEnv("google_completion_url", "GOOGLE_COMPLETION_URL")
	v.BindEnv("query_stage_config_path", "QUERY_STAGE_CONFIG_PATH")
	v.BindEnv("query_expansion_model", "QUERY_EXPANSION_MODEL")
	v.BindEnv("query_expansion_reasoning", "QUERY_EXPANSION_REASONING")
	v.BindEnv("answer_synthesis_model", "ANSWER_SYNTHESIS_MODEL")
	v.BindEnv("answer_synthesis_reasoning", "ANSWER_SYNTHESIS_REASONING")
	v.BindEnv("query_selection_limit", "QUERY_SELECTION_LIMIT")
	v.BindEnv("query_selection_exploration_slots", "QUERY_SELECTION_EXPLORATION_SLOTS")
	v.BindEnv("query_selection_evidence_threshold", "QUERY_SELECTION_EVIDENCE_THRESHOLD")
	v.BindEnv("query_expansion_keywords_per_attempt", "QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT")
	v.BindEnv("query_expansion_attempts", "QUERY_EXPANSION_ATTEMPTS")
	v.BindEnv("query_matching_rare_keyword_max_document_frequency", "QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY")

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return Config{}, err
		}
	}
	if err := validateKnownQuerySettings(v); err != nil {
		return Config{}, err
	}
	queryStageConfigPath := strings.TrimSpace(v.GetString("query_stage_config_path"))
	if queryStageConfigPath != "" && explicitLegacyQuerySetting(v) {
		return Config{}, ErrMixedQueryConfigAuthority
	}

	dailyLimit := v.GetInt("pipeline_daily_limit")
	if dailyLimit <= 0 {
		dailyLimit = DefaultPipelineDailyLimit
	}
	cooldownSeconds := v.GetInt("pipeline_cooldown_seconds")
	if cooldownSeconds <= 0 {
		cooldownSeconds = DefaultPipelineCooldownSeconds
	}
	minNewRaw := v.GetInt("pipeline_min_new_raw")
	if minNewRaw <= 0 {
		minNewRaw = DefaultPipelineMinNewRaw
	}
	pipelineJobURL := strings.TrimSpace(v.GetString("pipeline_job_url"))
	if pipelineJobURL == "" {
		pipelineJobURL = DefaultPipelineJobURL
	}
	if err := validatePipelineJobURL(pipelineJobURL); err != nil {
		return Config{}, fmt.Errorf("invalid pipeline_job_url: %w", err)
	}
	allowedHosts, err := parseAllowedHosts(v.GetString("allowed_hosts"))
	if err != nil {
		return Config{}, fmt.Errorf("invalid allowed_hosts: %w", err)
	}
	localCloudScope, err := localcloud.Parse(v.GetString("local_cloud_scope"))
	if err != nil {
		return Config{}, err
	}
	if localCloudScope != "" {
		if len(allowedHosts) == 0 {
			return Config{}, errors.New("local cloud requires an explicit loopback ALLOWED_HOSTS value")
		}
		for _, host := range allowedHosts {
			if host != "localhost" && host != "127.0.0.1" {
				return Config{}, errors.New("local cloud ALLOWED_HOSTS must contain only localhost loopback hosts")
			}
		}
		if strings.TrimSpace(v.GetString("gcp_project")) != "llm-wiki-cloud" || strings.TrimSpace(v.GetString("bucket")) != "llm-wiki-cloud-local" || strings.TrimSpace(v.GetString("firestore_database_id")) != "llm-wiki-cloud-local" {
			return Config{}, errors.New("local cloud requires project llm-wiki-cloud, bucket llm-wiki-cloud-local, and Firestore database llm-wiki-cloud-local")
		}
		if v.GetBool("dev_jwt") || strings.TrimSpace(v.GetString("local_data_dir")) != "" || strings.TrimSpace(os.Getenv("LOCAL_DATA_DIR")) != "" {
			return Config{}, errors.New("local cloud does not allow DEV_JWT or LOCAL_DATA_DIR")
		}
		origins := parseAllowedOrigins(v.GetString("allowed_origins"))
		if len(origins) != 1 || !strings.HasPrefix(origins[0], "http://localhost:") {
			return Config{}, errors.New("local cloud requires one explicit localhost Frontend origin")
		}
		secretPath := strings.TrimSpace(v.GetString("local_cloud_jwt_secret_file"))
		if secretPath == "" || strings.TrimSpace(v.GetString("jwt_secret")) != "" {
			return Config{}, errors.New("local cloud requires a dedicated LOCAL_CLOUD_JWT_SECRET_FILE")
		}
		secretBytes, err := os.ReadFile(secretPath)
		if err != nil {
			return Config{}, errors.New("local cloud signing key file is unavailable")
		}
		secret := strings.TrimSpace(string(secretBytes))
		decoded, err := hex.DecodeString(secret)
		if err != nil || len(decoded) != 32 {
			return Config{}, errors.New("local cloud signing key file is invalid")
		}
		jwtSecret := secret
		v.Set("jwt_secret", jwtSecret)
	} else if strings.TrimSpace(v.GetString("local_cloud_jwt_secret_file")) != "" {
		return Config{}, errors.New("LOCAL_CLOUD_JWT_SECRET_FILE requires LOCAL_CLOUD_SCOPE")
	}

	var registrationEnabled *bool
	if raw := strings.TrimSpace(v.GetString("registration_enabled")); raw != "" {
		enabled, _ := parseBoolEnv(raw) // Invalid explicit configuration must not reopen signup.
		registrationEnabled = &enabled
	}

	authServiceURL := strings.TrimSpace(v.GetString("auth_service_url"))
	if authServiceURL == "" {
		authServiceURL = DefaultAuthServiceURL
	}
	authSessionEnvironment := strings.TrimSpace(v.GetString("auth_session_environment"))
	authSessionMigration := strings.TrimSpace(strings.ToLower(v.GetString("auth_session_migration")))
	if authSessionMigration == "" {
		authSessionMigration = "disabled"
	}
	if authSessionMigration != "disabled" && authSessionMigration != "legacy_read_through" {
		return Config{}, fmt.Errorf("invalid auth_session_migration: must be disabled or legacy_read_through")
	}
	googleClientID := strings.TrimSpace(v.GetString("google_client_id"))
	googleClientSecret := strings.TrimSpace(v.GetString("google_client_secret"))
	googleIssuer := strings.TrimSpace(v.GetString("google_issuer"))
	googleJWKSURL := strings.TrimSpace(v.GetString("google_jwks_url"))
	googleTokenURL := strings.TrimSpace(v.GetString("google_token_url"))
	googleLoginRedirectURL := strings.TrimSpace(v.GetString("google_login_redirect_url"))
	googleLinkRedirectURL := strings.TrimSpace(v.GetString("google_link_redirect_url"))
	googleCompletionURL := strings.TrimSpace(v.GetString("google_completion_url"))
	allowedOrigins := parseAllowedOrigins(v.GetString("allowed_origins"))
	if googleConfigSet(googleClientID, googleClientSecret, googleIssuer, googleJWKSURL, googleTokenURL, googleLoginRedirectURL, googleLinkRedirectURL, googleCompletionURL) {
		if err := ValidateGoogleConfig(googleClientID, googleClientSecret, googleIssuer, googleJWKSURL, googleTokenURL, googleLoginRedirectURL, googleLinkRedirectURL, googleCompletionURL, GoogleRuntimeValidation{AuthServiceURL: authServiceURL, AllowedOrigins: allowedOrigins}); err != nil {
			return Config{}, fmt.Errorf("invalid Google OAuth configuration: %w", err)
		}
	}
	queryExpansionModel := strings.TrimSpace(v.GetString("query_expansion_model"))
	if queryExpansionModel == "" {
		queryExpansionModel = DefaultQueryExpansionModel
	}
	if queryExpansionModel != DefaultQueryExpansionModel && queryExpansionModel != "deepseek-v4-flash" {
		return Config{}, fmt.Errorf("query_expansion_model must be %s", DefaultQueryExpansionModel)
	}
	queryExpansionReasoning := llm.Reasoning(strings.TrimSpace(v.GetString("query_expansion_reasoning")))
	answerSynthesisModel := strings.TrimSpace(v.GetString("answer_synthesis_model"))
	answerSynthesisReasoning := llm.Reasoning(strings.TrimSpace(v.GetString("answer_synthesis_reasoning")))
	if queryExpansionReasoning != DefaultQueryExpansionReasoning {
		return Config{}, fmt.Errorf("query_expansion_reasoning must be none")
	}
	if answerSynthesisModel != DefaultAnswerSynthesisModel && answerSynthesisModel != "deepseek-v4-pro" {
		return Config{}, fmt.Errorf("answer_synthesis_model must be %s", DefaultAnswerSynthesisModel)
	}
	if !answerSynthesisReasoning.Valid() {
		return Config{}, fmt.Errorf("answer_synthesis_reasoning must be none, low, high, or max")
	}
	selectionLimit, err := configuredInt(v, "query_selection_limit", DefaultQuerySelectionLimit, 1, MaxQuerySelectionLimit)
	if err != nil {
		return Config{}, err
	}
	explorationSlots, err := configuredInt(v, "query_selection_exploration_slots", DefaultQuerySelectionExplorationSlots, 0, selectionLimit)
	if err != nil {
		return Config{}, err
	}
	evidenceThreshold, err := configuredInt(v, "query_selection_evidence_threshold", DefaultQuerySelectionEvidenceThreshold, 1, 0)
	if err != nil {
		return Config{}, err
	}
	if evidenceThreshold > MaxQuerySelectionEvidenceThreshold {
		return Config{}, fmt.Errorf("invalid query_selection_evidence_threshold: must be at most %d", MaxQuerySelectionEvidenceThreshold)
	}
	keywordsPerAttempt, err := configuredInt(v, "query_expansion_keywords_per_attempt", DefaultQueryExpansionKeywordsPerAttempt, 1, MaxQueryExpansionKeywordsPerAttempt)
	if err != nil {
		return Config{}, err
	}
	attempts, err := configuredInt(v, "query_expansion_attempts", DefaultQueryExpansionAttempts, 1, MaxQueryExpansionAttempts)
	if err != nil {
		return Config{}, err
	}
	rareKeywordMaxDocumentFrequency, err := configuredInt(v, "query_matching_rare_keyword_max_document_frequency", DefaultQueryMatchingRareKeywordMaxDocumentFrequency, 1, MaxQueryMatchingRareKeywordDocumentFrequency)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		GCPProject:                       v.GetString("gcp_project"),
		Bucket:                           v.GetString("bucket"),
		FirestoreDatabaseID:              strings.TrimSpace(v.GetString("firestore_database_id")),
		LocalCloudScope:                  string(localCloudScope),
		UserID:                           v.GetString("user_id"),
		ProjectID:                        v.GetString("project_id"),
		Port:                             v.GetString("port"),
		DeepSeekAPIKey:                   v.GetString("deepseek_api_key"),
		QueryExpansionModel:              llm.CanonicalDeepSeekModel(queryExpansionModel),
		QueryExpansionReasoning:          queryExpansionReasoning,
		AnswerSynthesisModel:             llm.CanonicalDeepSeekModel(answerSynthesisModel),
		AnswerSynthesisReasoning:         answerSynthesisReasoning,
		JWTSecret:                        v.GetString("jwt_secret"),
		PipelineJobURL:                   pipelineJobURL,
		ExportJobURL:                     strings.TrimSpace(v.GetString("export_job_url")),
		ExportSigningServiceAccount:      strings.TrimSpace(v.GetString("export_signing_service_account")),
		AllowedOrigins:                   allowedOrigins,
		AllowedHosts:                     allowedHosts,
		PipelineDailyLimit:               dailyLimit,
		PipelineCooldownSeconds:          cooldownSeconds,
		PipelineMinNewRaw:                minNewRaw,
		PipelineDemoUserIDs:              splitCommaList(v.GetString("pipeline_demo_user_ids")),
		AuthDemoUserID:                   strings.TrimSpace(v.GetString("auth_demo_user_id")),
		AuthDemoUserEmail:                strings.TrimSpace(v.GetString("auth_demo_user_email")),
		AuthDemoUserRole:                 strings.TrimSpace(v.GetString("auth_demo_user_role")),
		RegistrationEnabled:              registrationEnabled,
		AuthServiceURL:                   authServiceURL,
		AuthSessionEnvironment:           authSessionEnvironment,
		AuthSessionMigration:             authSessionMigration,
		GoogleClientID:                   googleClientID,
		GoogleClientSecret:               googleClientSecret,
		GoogleIssuer:                     googleIssuer,
		GoogleJWKSURL:                    googleJWKSURL,
		GoogleTokenURL:                   googleTokenURL,
		GoogleLoginRedirectURL:           googleLoginRedirectURL,
		GoogleLinkRedirectURL:            googleLinkRedirectURL,
		GoogleCompletionURL:              googleCompletionURL,
		QueryStageConfigPath:             queryStageConfigPath,
		QuerySelectionLimit:              selectionLimit,
		QuerySelectionExplorationSlots:   explorationSlots,
		QuerySelectionEvidenceThreshold:  evidenceThreshold,
		QueryExpansionKeywordsPerAttempt: keywordsPerAttempt,
		QueryExpansionAttempts:           attempts,
		QueryMatchingRareKeywordMaxDocumentFrequency: rareKeywordMaxDocumentFrequency,
	}
	return cfg, nil
}

func explicitLegacyQuerySetting(v *viper.Viper) bool {
	settings := map[string]string{
		"query_expansion_model": "QUERY_EXPANSION_MODEL", "query_expansion_reasoning": "QUERY_EXPANSION_REASONING",
		"answer_synthesis_model": "ANSWER_SYNTHESIS_MODEL", "answer_synthesis_reasoning": "ANSWER_SYNTHESIS_REASONING",
		"query_selection_limit": "QUERY_SELECTION_LIMIT", "query_selection_exploration_slots": "QUERY_SELECTION_EXPLORATION_SLOTS",
		"query_selection_evidence_threshold": "QUERY_SELECTION_EVIDENCE_THRESHOLD", "query_expansion_keywords_per_attempt": "QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT",
		"query_expansion_attempts": "QUERY_EXPANSION_ATTEMPTS", "query_matching_rare_keyword_max_document_frequency": "QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY",
	}
	for key, env := range settings {
		if v.InConfig(key) {
			return true
		}
		if value, ok := os.LookupEnv(env); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func validateKnownQuerySettings(v *viper.Viper) error {
	known := map[string]struct{}{
		"query_expansion_model": {}, "query_expansion_reasoning": {}, "query_expansion_keywords_per_attempt": {}, "query_expansion_attempts": {},
		"query_selection_limit": {}, "query_selection_exploration_slots": {}, "query_selection_evidence_threshold": {},
		"query_matching_rare_keyword_max_document_frequency": {},
	}
	for key := range v.AllSettings() {
		if !strings.HasPrefix(key, "query_expansion_") && !strings.HasPrefix(key, "query_selection_") && !strings.HasPrefix(key, "query_matching_") {
			continue
		}
		if _, ok := known[key]; !ok {
			return fmt.Errorf("unknown query configuration %q", key)
		}
	}
	return nil
}

func configuredInt(v *viper.Viper, key string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(v.GetString(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: must be an integer", key)
	}
	if value < minimum {
		return 0, fmt.Errorf("invalid %s: must be at least %d", key, minimum)
	}
	if maximum > 0 && value > maximum {
		return 0, fmt.Errorf("invalid %s: must be at most %d", key, maximum)
	}
	return value, nil
}

func validatePipelineJobURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme must be https")
	}
	if u.Host != "run.googleapis.com" {
		return fmt.Errorf("host must be run.googleapis.com")
	}
	if u.User != nil {
		return fmt.Errorf("userinfo is not allowed")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf("query is not allowed")
	}
	if u.Fragment != "" {
		return fmt.Errorf("fragment is not allowed")
	}
	if u.RawPath != "" {
		return fmt.Errorf("escaped paths are not allowed")
	}

	parts := strings.Split(u.Path, "/")
	if len(parts) != 8 || parts[1] != "v2" || parts[2] != "projects" || parts[4] != "locations" || parts[6] != "jobs" {
		return fmt.Errorf("path must be /v2/projects/{project}/locations/{location}/jobs/{job}:run")
	}
	if !isSafePipelinePathSegment(parts[3]) || !isSafePipelinePathSegment(parts[5]) {
		return fmt.Errorf("project and location path segments must be non-empty and safe")
	}
	if !strings.HasSuffix(parts[7], ":run") || !isSafePipelinePathSegment(strings.TrimSuffix(parts[7], ":run")) {
		return fmt.Errorf("job path segment must end with :run and be safe")
	}
	return nil
}

func isSafePipelinePathSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for _, r := range segment {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// AllowedOriginsFor returns configured origins and adds local development
// origins when the BFF is running in local mode.
func (c Config) AllowedOriginsFor(localMode bool) []string {
	origins := append([]string(nil), c.AllowedOrigins...)
	if localMode {
		if len(origins) == 0 {
			return []string{"http://localhost:3000"}
		}
		return origins
	}
	return uniqueAllowedOrigins(origins)
}

// AllowedHostsFor returns only configured hosts in deployed mode. Local mode
// accepts configured loopback hosts; tests with hand-built Config values get
// the documented loopback defaults.
func (c Config) AllowedHostsFor(localMode bool) []string {
	if localMode {
		if len(c.AllowedHosts) > 0 {
			return uniqueAllowedHosts(append([]string(nil), c.AllowedHosts...))
		}
		return []string{"localhost", "127.0.0.1"}
	}
	return uniqueAllowedHosts(append([]string(nil), c.AllowedHosts...))
}

func parseBoolEnv(raw string) (bool, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	switch raw {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
	}
}

func splitCommaList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseAllowedOrigins(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return append([]string(nil), defaultAllowedOrigins...)
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin == "" || origin == "*" {
			continue
		}
		origins = append(origins, origin)
	}
	return uniqueAllowedOrigins(origins)
}

func parseAllowedHosts(raw string) ([]string, error) {
	parts := splitCommaList(raw)
	for _, host := range parts {
		if strings.Contains(host, "*") {
			return nil, fmt.Errorf("wildcards are not allowed")
		}
	}
	return uniqueAllowedHosts(parts), nil
}

func uniqueAllowedOrigins(origins []string) []string {
	seen := make(map[string]struct{}, len(origins))
	unique := make([]string, 0, len(origins))
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" || origin == "*" {
			continue
		}
		if _, ok := seen[origin]; ok {
			continue
		}
		seen[origin] = struct{}{}
		unique = append(unique, origin)
	}
	return unique
}

func uniqueAllowedHosts(hosts []string) []string {
	seen := make(map[string]struct{}, len(hosts))
	unique := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		unique = append(unique, host)
	}
	return unique
}
