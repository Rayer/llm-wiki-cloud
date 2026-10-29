package config

// Config is the Auth process runtime projection of the independently
// materialized Auth input file. Product configuration is intentionally absent.
type Config struct {
	ConfigID                     string
	ConfigSchemaVersion          int
	AuthEnvironment              string
	GCPProject                   string
	FirestoreDatabaseID          string
	LocalCloudScope              string
	Port                         string
	JWTSecret                    string
	AuthServiceURL               string
	SyncServiceURL               string
	AllowedHosts                 []string
	AllowedOrigins               []string
	AuthSessionEnvironment       string
	AuthSessionMigration         string
	RegistrationEnabled          *bool
	AuthDemoUserID               string
	AuthDemoUserEmail            string
	AuthDemoUserRole             string
	GoogleClientID               string
	GoogleClientSecret           string
	GoogleIssuer                 string
	GoogleJWKSURL                string
	GoogleTokenURL               string
	GoogleLoginRedirectURL       string
	GoogleLinkRedirectURL        string
	GoogleCompletionURL          string
	AuthOAuthResources           []AuthOAuthResource
	ManagementScopes             []AuthManagementScope
	AuthConsumerVerifiers        []AuthConsumerVerifier
	AuthConsumerPublicProjection AuthConsumerPublicProjection
}

func (c Config) AllowedHostsFor(local bool) []string {
	if local && len(c.AllowedHosts) == 0 {
		return []string{"localhost", "127.0.0.1"}
	}
	return append([]string(nil), c.AllowedHosts...)
}

func (c Config) AllowedOriginsFor(local bool) []string {
	if local && len(c.AllowedOrigins) == 0 {
		return []string{"http://localhost:3000"}
	}
	return append([]string(nil), c.AllowedOrigins...)
}
