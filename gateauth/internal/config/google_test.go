package config

import "testing"

func TestValidateGoogleConfigRejectsProviderAndRedirectURLsWithoutHTTPS(t *testing.T) {
	if err := ValidateGoogleConfig("client", "secret", "http://issuer", "https://issuer/keys", "https://issuer/token", "https://auth/login", "https://auth/link", "https://wiki/login"); err == nil {
		t.Fatal("ValidateGoogleConfig accepted an HTTP issuer")
	}
}

func TestValidateGoogleConfigBindsProductionEndpointsAndDestinations(t *testing.T) {
	runtime := GoogleRuntimeValidation{AuthServiceURL: "https://auth.dev.example", AllowedOrigins: []string{"https://wiki.dev.example"}}
	validate := func(issuer, jwks, token, login, link, completion string) error {
		return ValidateGoogleConfig("client", "secret", issuer, jwks, token, login, link, completion, runtime)
	}
	valid := []string{
		GoogleIssuerURL, GoogleJWKSURL, GoogleTokenURL,
		"https://auth.dev.example/api/v1/auth/google/login/callback",
		"https://auth.dev.example/api/v1/auth/google/link/callback",
		"https://wiki.dev.example/login",
	}
	if err := validate(valid[0], valid[1], valid[2], valid[3], valid[4], valid[5]); err != nil {
		t.Fatalf("valid Google configuration rejected: %v", err)
	}
	for _, test := range []struct {
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
	} {
		t.Run(test.name, func(t *testing.T) {
			values := append([]string(nil), valid...)
			values[test.index] = test.value
			if err := validate(values[0], values[1], values[2], values[3], values[4], values[5]); err == nil {
				t.Fatalf("configuration mismatch %q was accepted", test.value)
			}
		})
	}
}
