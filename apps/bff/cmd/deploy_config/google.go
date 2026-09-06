package main

import (
	"errors"
	"reflect"
	"regexp"
)

// GoogleConfig contains only public configuration and a pinned Secret Manager
// reference. A disabled block must be otherwise empty; absence preserves the
// existing artifact-only contract (including Production).
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
	if environment != "development" || g.Enabled == nil {
		return errors.New("auth.google requires explicit development enablement")
	}
	if c.Auth.ServiceName != "llm-wiki-auth-dev" || c.Auth.FirestoreDatabaseID != "llm-wiki-cloud-dev" ||
		c.Auth.RuntimeServiceAccount != "lwc-auth-dev@llm-wiki-cloud.iam.gserviceaccount.com" || c.Auth.PublicDomain != "auth.dev.rayer.idv.tw" ||
		c.Frontend.AuthURL != "https://auth.dev.rayer.idv.tw" || c.Frontend.ProjectName != "llm-wiki-frontend-dev" ||
		!reflect.DeepEqual(c.Auth.AllowedHosts, []string{"auth.dev.rayer.idv.tw", "auth-dev.rayer.idv.tw"}) ||
		!reflect.DeepEqual(c.Auth.AllowedOrigins, []string{"https://wiki.dev.rayer.idv.tw", "https://llm-wiki-frontend-dev.vercel.app", "http://localhost:3000"}) {
		return errors.New("auth.google requires the reviewed DEV environment and origins")
	}
	if !*g.Enabled {
		if *g != (GoogleConfig{Enabled: g.Enabled}) {
			return errors.New("disabled auth.google must contain no provider configuration")
		}
		return nil
	}
	if !regexp.MustCompile(`^[0-9]+-[A-Za-z0-9_-]+\.apps\.googleusercontent\.com$`).MatchString(g.ClientID) ||
		g.ClientSecretReference != "google-oauth-client-dev" || !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(g.ClientSecretVersion) {
		return errors.New("Google enablement requires the provisioned DEV client ID and google-oauth-client-dev numeric secret version")
	}
	if g.Issuer != "https://accounts.google.com" || g.JWKSURL != "https://www.googleapis.com/oauth2/v3/certs" || g.TokenURL != "https://oauth2.googleapis.com/token" ||
		g.LoginRedirectURL != "https://auth.dev.rayer.idv.tw/api/v1/auth/google/callback" ||
		g.LinkRedirectURL != "https://auth.dev.rayer.idv.tw/api/v1/auth/google/link/callback" ||
		g.CompletionURL != "https://wiki.dev.rayer.idv.tw/login" {
		return errors.New("Google endpoints and DEV callback/completion URLs must exactly match the reviewed contract")
	}
	return nil
}
