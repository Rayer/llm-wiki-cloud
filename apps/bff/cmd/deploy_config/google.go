package main

import (
	"errors"
	"reflect"
	"regexp"
)

// GoogleConfig contains only public configuration and a pinned Secret Manager
// reference. A disabled block must be otherwise empty; absence preserves the
// existing artifact-only contract. Production enablement needs its own reviewed metadata.
type GoogleConfig struct {
	Enabled               *bool  `yaml:"enabled" json:"enabled"`
	ClientID              string `yaml:"client_id" json:"client_id"`
	ClientSecretReference string `yaml:"client_secret_reference" json:"client_secret_reference"`
	ClientSecretVersion   string `yaml:"client_secret_version" json:"client_secret_version"`
	Issuer                string `yaml:"issuer" json:"issuer"`
	JWKSURL               string `yaml:"jwks_url" json:"jwks_url"`
	TokenURL              string `yaml:"token_url" json:"token_url"`
	LoginRedirectURL      string `yaml:"login_redirect_url" json:"login_redirect_url"`
	LinkRedirectURL       string `yaml:"link_redirect_url" json:"link_redirect_url"`
	CompletionURL         string `yaml:"completion_url" json:"completion_url"`
}

func validateGoogleDeployment(environment string, c EnvironmentConfig) error {
	g := c.Auth.Google
	if g == nil {
		return nil
	}
	if (environment != "development" && environment != "production") || g.Enabled == nil {
		return errors.New("auth.google requires explicit environment enablement")
	}
	authService, database, account, domain, frontend, secret := "llm-wiki-auth-dev", "llm-wiki-cloud-dev", "lwc-auth-dev@llm-wiki-cloud.iam.gserviceaccount.com", "auth.dev.rayer.idv.tw", "llm-wiki-frontend-dev", "google-oauth-client-dev"
	hosts := []string{"auth.dev.rayer.idv.tw", "auth-dev.rayer.idv.tw"}
	origins := []string{"https://wiki.dev.rayer.idv.tw", "https://llm-wiki-frontend-dev.vercel.app", "http://localhost:3000"}
	completion := "https://wiki.dev.rayer.idv.tw/login"
	if environment == "production" {
		authService, database, account, domain, frontend, secret = "llm-wiki-auth", "llm-wiki-cloud-prod", "lwc-auth-prod@llm-wiki-cloud.iam.gserviceaccount.com", "auth.rayer.idv.tw", "llm-wiki-frontend", "google-oauth-client-prod"
		hosts = []string{"auth.rayer.idv.tw"}
		origins = []string{"https://wiki.rayer.idv.tw", "https://llm-wiki-frontend.vercel.app"}
		completion = "https://wiki.rayer.idv.tw/login"
		if c.BFF.ServiceName != "llm-wiki-bff" || c.BFF.FirestoreDatabaseID != database ||
			c.BFF.RuntimeServiceAccount != "lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com" ||
			c.BFF.AuthServiceURL != "https://"+domain || !reflect.DeepEqual(c.BFF.AllowedOrigins, origins) ||
			c.Frontend.APIURL != "https://llm-wiki-bff-580854833715.asia-east1.run.app" ||
			!reflect.DeepEqual(c.Frontend.StableAliases, []string{"wiki.rayer.idv.tw", "llm-wiki-frontend.vercel.app"}) {
			return errors.New("auth.google requires the reviewed Production BFF and frontend bindings")
		}
		if g.ClientID == "580854833715-vo7fg6f7f15g1kkgchk1ulccllbc24qg.apps.googleusercontent.com" {
			return errors.New("Production must not reuse the provisioned DEV Google client")
		}
	}
	if c.Auth.ServiceName != authService || c.Auth.FirestoreDatabaseID != database ||
		c.Auth.RuntimeServiceAccount != account || c.Auth.PublicDomain != domain ||
		c.Frontend.AuthURL != "https://"+domain || c.Frontend.ProjectName != frontend ||
		!reflect.DeepEqual(c.Auth.AllowedHosts, hosts) || !reflect.DeepEqual(c.Auth.AllowedOrigins, origins) {
		return errors.New("auth.google requires the reviewed environment and origins")
	}
	if !*g.Enabled {
		if *g != (GoogleConfig{Enabled: g.Enabled}) {
			return errors.New("disabled auth.google must contain no provider configuration")
		}
		return nil
	}
	if !regexp.MustCompile(`^[0-9]+-[A-Za-z0-9_-]+\.apps\.googleusercontent\.com$`).MatchString(g.ClientID) ||
		g.ClientSecretReference != secret || !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(g.ClientSecretVersion) {
		return errors.New("Google enablement requires a client ID and the environment-specific secret with a numeric version")
	}
	if g.Issuer != "https://accounts.google.com" || g.JWKSURL != "https://www.googleapis.com/oauth2/v3/certs" || g.TokenURL != "https://oauth2.googleapis.com/token" ||
		g.LoginRedirectURL != "https://"+domain+"/api/v1/auth/google/callback" ||
		g.LinkRedirectURL != "https://"+domain+"/api/v1/auth/google/link/callback" ||
		g.CompletionURL != completion {
		return errors.New("Google endpoints and environment callback/completion URLs must exactly match the reviewed contract")
	}
	return nil
}
