package config

import (
	"testing"
)

func TestLoadGoogleConfigRequiresEveryRuntimeField(t *testing.T) {
	for _, name := range []string{
		"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_ISSUER", "GOOGLE_JWKS_URL", "GOOGLE_TOKEN_URL",
		"GOOGLE_LOGIN_REDIRECT_URL", "GOOGLE_LINK_REDIRECT_URL", "GOOGLE_COMPLETION_URL",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("GOOGLE_CLIENT_ID", "client")
	if _, err := Load(writeConfig(t, "dev_jwt = false\n")); err == nil {
		t.Fatal("Load accepted partial Google configuration")
	}
}

func TestValidateGoogleConfigRejectsProviderAndRedirectURLsWithoutHTTPS(t *testing.T) {
	if err := ValidateGoogleConfig("client", "secret", "http://issuer", "https://issuer/keys", "https://issuer/token", "https://auth/login", "https://auth/link", "https://wiki/login"); err == nil {
		t.Fatal("ValidateGoogleConfig accepted an HTTP issuer")
	}
}

func TestValidateGoogleConfigBindsProductionEndpointsAndDestinations(t *testing.T) {
	runtime := GoogleRuntimeValidation{
		AuthServiceURL: "https://auth.dev.example",
		AllowedOrigins: []string{"https://wiki.dev.example"},
	}
	valid := func(issuer, jwks, token, login, link, completion string) error {
		return ValidateGoogleConfig("client", "secret", issuer, jwks, token, login, link, completion, runtime)
	}
	base := []string{
		GoogleIssuerURL, GoogleJWKSURL, GoogleTokenURL,
		"https://auth.dev.example/api/v1/auth/google/login/callback",
		"https://auth.dev.example/api/v1/auth/google/link/callback",
		"https://wiki.dev.example/login",
	}
	if err := valid(base[0], base[1], base[2], base[3], base[4], base[5]); err != nil {
		t.Fatalf("valid production Google configuration rejected: %v", err)
	}
	tests := []struct {
		name  string
		index int
		value string
	}{
		{name: "issuer", index: 0, value: "https://accounts.google.example"},
		{name: "jwks", index: 1, value: "https://accounts.google.example/keys"},
		{name: "token", index: 2, value: "https://accounts.google.example/token"},
		{name: "login origin", index: 3, value: "https://attacker.example/api/v1/auth/google/login/callback"},
		{name: "login path", index: 3, value: "https://auth.dev.example/other/callback"},
		{name: "link path", index: 4, value: "https://auth.dev.example/api/v1/auth/google/callback"},
		{name: "completion origin", index: 5, value: "https://attacker.example/login"},
		{name: "completion query", index: 5, value: "https://wiki.dev.example/login?next=https://attacker.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := append([]string(nil), base...)
			values[test.index] = test.value
			if err := valid(values[0], values[1], values[2], values[3], values[4], values[5]); err == nil {
				t.Fatalf("configuration mismatch %q was accepted", test.value)
			}
		})
	}
}

func TestLoadGoogleConfigSeparatesAuthAndFrontendEnvironment(t *testing.T) {
	for name, value := range map[string]string{
		"GOOGLE_CLIENT_ID":          "client",
		"GOOGLE_CLIENT_SECRET":      "secret",
		"GOOGLE_ISSUER":             GoogleIssuerURL,
		"GOOGLE_JWKS_URL":           GoogleJWKSURL,
		"GOOGLE_TOKEN_URL":          GoogleTokenURL,
		"GOOGLE_LOGIN_REDIRECT_URL": "https://auth.dev.example/api/v1/auth/google/login/callback",
		"GOOGLE_LINK_REDIRECT_URL":  "https://auth.dev.example/api/v1/auth/google/link/callback",
		"GOOGLE_COMPLETION_URL":     "https://wiki.dev.example/login",
		"AUTH_SERVICE_URL":          "https://auth.dev.example",
		"ALLOWED_ORIGINS":           "https://wiki.dev.example",
	} {
		t.Setenv(name, value)
	}
	if _, err := Load(writeConfig(t, "dev_jwt = false\n")); err != nil {
		t.Fatalf("valid separated environment rejected: %v", err)
	}
	t.Setenv("GOOGLE_COMPLETION_URL", "https://wiki.prod.example/login")
	if _, err := Load(writeConfig(t, "dev_jwt = false\n")); err == nil {
		t.Fatal("Load accepted completion destination outside configured frontend origins")
	}
	t.Setenv("GOOGLE_COMPLETION_URL", "https://wiki.dev.example/login")
	t.Setenv("GOOGLE_LOGIN_REDIRECT_URL", "https://auth.prod.example/api/v1/auth/google/login/callback")
	if _, err := Load(writeConfig(t, "dev_jwt = false\n")); err == nil {
		t.Fatal("Load accepted callback destination outside configured Auth service origin")
	}
}
