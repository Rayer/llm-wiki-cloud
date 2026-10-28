package config

import "errors"

// LegacyLWCBridgeVerifier retains the bounded schema-2 LWC verifier identity
// without importing or projecting BFF-owned configuration.
func LegacyLWCBridgeVerifier(environment, authURL string) (AuthConsumerVerifier, error) {
	var expectedAuthURL, audience, resource, caller string
	switch environment {
	case "dev":
		expectedAuthURL, audience, resource, caller = "https://auth.dev.rayer.idv.tw", "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app", "https://llm-wiki-bff-dev-580854833715.asia-east1.run.app", "lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com"
	case "prod":
		expectedAuthURL, audience, resource, caller = "https://auth.rayer.idv.tw", "https://llm-wiki-auth-a5nkmux6pq-de.a.run.app", "https://llm-wiki-bff-580854833715.asia-east1.run.app", "lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com"
	default:
		return AuthConsumerVerifier{}, errors.New("schema-2 Auth bridge environment is unsupported")
	}
	if authURL != expectedAuthURL {
		return AuthConsumerVerifier{}, errors.New("schema-2 Auth bridge identity is invalid")
	}
	return AuthConsumerVerifier{ID: "lwc-bff", Environment: environment, CallerServiceAccount: caller, Audience: audience,
		Resource: resource, AllowedCredentialKinds: []string{"web-access", "cli-access", "mcp-oauth"},
		AuthorityModes: []string{"legacy-shadow", "central"}}, nil
}
