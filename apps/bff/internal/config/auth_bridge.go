package config

import "errors"

const (
	legacyDevAuthURL       = "https://auth.dev.rayer.idv.tw"
	legacyProdAuthURL      = "https://auth.rayer.idv.tw"
	legacyDevAuthAudience  = "https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app"
	legacyProdAuthAudience = "https://llm-wiki-auth-a5nkmux6pq-de.a.run.app"
	legacyDevBFFResource   = "https://llm-wiki-bff-dev-580854833715.asia-east1.run.app"
	legacyProdBFFResource  = "https://llm-wiki-bff-580854833715.asia-east1.run.app"
	legacyDevBFFCaller     = "lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com"
	legacyProdBFFCaller    = "lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com"
)

// LegacyLWCBridgeConsumer describes only the bounded schema-2 bridge. Schema-3
// callers must use the typed Auth/BFF consumer projections instead.
func LegacyLWCBridgeConsumer(environment, authURL, resource string) (BFFAuthConsumer, AuthConsumerVerifier, error) {
	var expectedAuthURL, audience, expectedResource, caller string
	switch environment {
	case "dev":
		expectedAuthURL, audience, expectedResource, caller = legacyDevAuthURL, legacyDevAuthAudience, legacyDevBFFResource, legacyDevBFFCaller
	case "prod":
		expectedAuthURL, audience, expectedResource, caller = legacyProdAuthURL, legacyProdAuthAudience, legacyProdBFFResource, legacyProdBFFCaller
	default:
		return BFFAuthConsumer{}, AuthConsumerVerifier{}, errors.New("schema-2 Auth bridge environment is unsupported")
	}
	if authURL != expectedAuthURL || (resource != "" && resource != expectedResource) {
		return BFFAuthConsumer{}, AuthConsumerVerifier{}, errors.New("schema-2 Auth bridge identity is invalid")
	}
	consumer := BFFAuthConsumer{
		SchemaVersion: 1, Environment: environment, AuthURL: expectedAuthURL, Issuer: expectedAuthURL,
		RuntimeServiceAccount: caller, VerifierAudience: audience, Resource: expectedResource,
		VerifierPath:     "/api/v1/auth/consumers/lwc/verify-credential",
		AccountCheckPath: "/api/v1/auth/consumers/lwc/accounts/check",
		CatalogPath:      "/api/v1/auth/consumers/lwc/management/catalog",
		AuthorityModes:   []string{"legacy-shadow", "central"}, TimeoutSeconds: 5,
	}
	verifier := AuthConsumerVerifier{
		ID: "lwc-bff", Environment: environment, CallerServiceAccount: caller, Audience: audience,
		Resource: expectedResource, AllowedCredentialKinds: []string{"web-access", "cli-access", "mcp-oauth"},
		AuthorityModes: []string{"legacy-shadow", "central"},
	}
	return consumer, verifier, nil
}
