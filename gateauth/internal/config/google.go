package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	GoogleIssuerURL = "https://accounts.google.com"
	GoogleJWKSURL   = "https://www.googleapis.com/oauth2/v3/certs"
	GoogleTokenURL  = "https://oauth2.googleapis.com/token"
)

// GoogleRuntimeValidation binds fixed callback and completion destinations to
// the deployed Auth service and the already configured frontend origins.
type GoogleRuntimeValidation struct {
	AuthServiceURL string
	AllowedOrigins []string
}

// ValidateGoogleConfig validates the complete runtime Google OIDC contract.
// Tests inject fake provider endpoints directly into the OAuth service instead
// of weakening this production configuration boundary.
func ValidateGoogleConfig(clientID, clientSecret, issuer, jwksURL, tokenURL, loginRedirectURL, linkRedirectURL, completionURL string, runtime ...GoogleRuntimeValidation) error {
	values := map[string]string{
		"client_id":          clientID,
		"client_secret":      clientSecret,
		"issuer":             issuer,
		"jwks_url":           jwksURL,
		"token_url":          tokenURL,
		"login_redirect_url": loginRedirectURL,
		"link_redirect_url":  linkRedirectURL,
		"completion_url":     completionURL,
	}
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, raw := range map[string]string{
		"issuer":             issuer,
		"jwks_url":           jwksURL,
		"token_url":          tokenURL,
		"login_redirect_url": loginRedirectURL,
		"link_redirect_url":  linkRedirectURL,
		"completion_url":     completionURL,
	} {
		if err := validateFixedHTTPSURL(raw); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if issuer != GoogleIssuerURL || jwksURL != GoogleJWKSURL || tokenURL != GoogleTokenURL {
		return errors.New("issuer, jwks_url, and token_url must be the approved Google endpoints")
	}
	if strings.TrimSpace(loginRedirectURL) == strings.TrimSpace(linkRedirectURL) {
		return errors.New("login_redirect_url and link_redirect_url must differ")
	}
	if len(runtime) > 0 {
		if err := validateGoogleDestinations(loginRedirectURL, linkRedirectURL, completionURL, runtime[0]); err != nil {
			return err
		}
	}
	return nil
}

func validateGoogleDestinations(loginRedirectURL, linkRedirectURL, completionURL string, runtime GoogleRuntimeValidation) error {
	authOrigin, err := fixedOrigin(runtime.AuthServiceURL)
	if err != nil {
		return fmt.Errorf("auth_service_url: %w", err)
	}
	login, err := parseFixedHTTPSURL(loginRedirectURL)
	if err != nil || fixedOriginValue(login) != authOrigin || (login.Path != "/api/v1/auth/google/callback" && login.Path != "/api/v1/auth/google/login/callback") {
		return errors.New("login_redirect_url must be on Auth service origin with the fixed Google callback path")
	}
	link, err := parseFixedHTTPSURL(linkRedirectURL)
	if err != nil || fixedOriginValue(link) != authOrigin || link.Path != "/api/v1/auth/google/link/callback" {
		return errors.New("link_redirect_url must be on Auth service origin with the fixed Google link callback path")
	}
	completion, err := parseFixedHTTPSURL(completionURL)
	if err != nil || !originInAllowedList(fixedOriginValue(completion), runtime.AllowedOrigins) {
		return errors.New("completion_url must be on an allowed frontend origin")
	}
	return nil
}

func parseFixedHTTPSURL(raw string) (*url.URL, error) {
	if err := validateFixedHTTPSURL(raw); err != nil {
		return nil, err
	}
	u, _ := url.Parse(strings.TrimSpace(raw))
	return u, nil
}

func fixedOrigin(raw string) (string, error) {
	u, err := parseFixedHTTPSURL(raw)
	if err != nil || (u.Path != "" && u.Path != "/") {
		return "", errors.New("must be an HTTPS origin")
	}
	return fixedOriginValue(u), nil
}

func fixedOriginValue(u *url.URL) string { return u.Scheme + "://" + u.Host }

func originInAllowedList(origin string, allowed []string) bool {
	for _, raw := range allowed {
		if parsed, err := fixedOrigin(raw); err == nil && parsed == origin {
			return true
		}
	}
	return false
}

func googleConfigSet(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func validateFixedHTTPSURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("must be an HTTPS URL without userinfo")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("query and fragment are not allowed")
	}
	return nil
}
