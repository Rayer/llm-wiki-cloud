package authconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientVerifiesThroughTrustedRuntimeAndChecksStrictResponse(t *testing.T) {
	const userCredential = "user-access-credential"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime-identity" || r.Header.Get("X-LWC-User-Credential") != userCredential {
			t.Errorf("unexpected headers: authorization=%q user-credential=%q", r.Header.Get("Authorization"), r.Header.Get("X-LWC-User-Credential"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request["schema_version"] != float64(1) || request["resource"] != "https://bff.example.test" || request["project_id"] != "project-a" {
			t.Errorf("unexpected verifier request: %#v", request)
		}
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(Identity{
			SchemaVersion: 1, Subject: "user-a", CredentialKind: "web-access", Environment: "dev",
			Issuer: "https://auth.example.test", Resource: "https://bff.example.test", ExpiresAt: time.Now().Add(time.Minute).Unix(),
			AuthVersion: 7, Scopes: []string{}, ManagementScopes: []string{"lwc:admin"},
		})
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(_ context.Context, audience string) (string, error) {
		if audience != "https://auth-runtime.example.test" {
			t.Errorf("runtime audience=%q", audience)
		}
		return "runtime-identity", nil
	})
	identity, err := client.Verify(context.Background(), userCredential, "project-a")
	if err != nil || identity.Subject != "user-a" || len(identity.ManagementScopes) != 1 || identity.ManagementScopes[0] != "lwc:admin" {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
}

func TestClientVerifyResourceRequiresMatchingOpaqueMCPIdentity(t *testing.T) {
	const resource = "https://bff.example.test/mcp/projects/project-a"
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requestCount++
		wantProject := "project-a"
		if requestCount == 2 {
			wantProject = "project-b"
		}
		if request["resource"] != resource || request["project_id"] != wantProject {
			t.Errorf("MCP verifier request was not resource-bound: %#v", request)
		}
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(Identity{SchemaVersion: 1, Subject: "user-a", CredentialKind: "mcp-oauth",
			Environment: "dev", Issuer: "https://auth.example.test", Resource: resource, ExpiresAt: time.Now().Add(time.Minute).Unix(),
			Scopes: []string{"mcp:query"}, ManagementScopes: []string{}, GrantID: "grant-a", ClientID: "client-a", ProjectID: "project-a"})
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	identity, err := client.VerifyResource(context.Background(), "lwc_mcp_at_opaque", resource, "project-a")
	if err != nil || identity.CredentialKind != "mcp-oauth" || identity.ProjectID != "project-a" || identity.GrantID != "grant-a" {
		t.Fatalf("MCP identity=%+v err=%v", identity, err)
	}
	if _, err := client.VerifyResource(context.Background(), "lwc_mcp_at_opaque", resource, "project-b"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("project-bound response error=%v, want unavailable", err)
	}
}

func TestClientMapsInvalidCredentialAndRejectsMalformedAuthorityResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want error
	}{
		{name: "invalid credential", code: http.StatusUnauthorized, body: `{"error":"invalid"}`, want: ErrInvalidCredential},
		{name: "schema mismatch", code: http.StatusOK, body: `{"schema_version":2,"subject":"user-a"}`, want: ErrUnavailable},
		{name: "unknown field", code: http.StatusOK, body: `{"schema_version":1,"subject":"user-a","extra":true}`, want: ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
			_, err := client.Verify(context.Background(), "credential", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestClientAccountCheckUsesSameTrustedRuntimeAndRequiresExactSubject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != checkPath || r.Header.Get("Authorization") != "Bearer runtime" || r.Header.Get("X-LWC-User-Credential") != "" {
			t.Errorf("unexpected account-check request: path=%s auth=%q credential=%q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-LWC-User-Credential"))
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"schema_version":1,"subject":"user-a","active":true,"auth_version":3}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	account, err := client.CheckAccount(context.Background(), "user-a")
	if err != nil || !account.Active || account.AuthVersion != 3 {
		t.Fatalf("account=%+v err=%v", account, err)
	}
	if _, err := client.CheckAccount(context.Background(), "user-b"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("subject mismatch error=%v, want unavailable", err)
	}
}

func TestClientProjectGrantListKeepsOptionalRevocationDataStrict(t *testing.T) {
	const revokedAt = "2026-10-29T01:02:03Z"
	for _, tc := range []struct {
		name string
		body string
		want error
	}{
		{name: "active and revoked", body: `{"schema_version":1,"grants":[{"id":"active","client_name":"Agent","project_id":"project-a","resource":"https://bff.example.test/mcp/projects/project-a","scope":"mcp:query","status":"active","created_at":"2026-10-28T01:00:00Z","expires_at":"2026-11-28T01:00:00Z"},{"id":"revoked","client_name":"Agent","project_id":"project-a","resource":"https://bff.example.test/mcp/projects/project-a","scope":"mcp:query","status":"revoked","created_at":"2026-10-28T01:00:00Z","expires_at":"2026-11-28T01:00:00Z","revoked_at":"` + revokedAt + `"}]}`},
		{name: "unknown field remains rejected", body: `{"schema_version":1,"grants":[{"id":"active","client_name":"Agent","project_id":"project-a","resource":"https://bff.example.test/mcp/projects/project-a","scope":"mcp:query","status":"active","created_at":"2026-10-28T01:00:00Z","expires_at":"2026-11-28T01:00:00Z","future_field":true}]}`, want: ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/auth/consumers/lwc/mcp-consent/grants/list" || r.Method != http.MethodPost {
					t.Errorf("unexpected grant-list request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Cache-Control", "no-store")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
			grants, err := client.ListProjectMCPGrants(context.Background(), "user-credential", "user-a", 1, "project-a")
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("error=%v, want %v", err, tc.want)
				}
				return
			}
			if err != nil || len(grants) != 2 || grants[0].RevokedAt != nil || grants[1].RevokedAt == nil ||
				grants[1].RevokedAt.UTC().Format(time.RFC3339) != revokedAt {
				t.Fatalf("grants=%+v err=%v", grants, err)
			}
		})
	}
}

func TestClientPreservesExpiredConsentStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/consumers/lwc/mcp-consent/transactions/lookup" {
			t.Errorf("unexpected expired consent path=%q", r.URL.Path)
		}
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":"transaction_expired"}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	transactionID := "lwc_mcp_tx_" + strings.Repeat("A", 43)
	_, err := client.LookupMCPConsent(context.Background(), "credential", transactionID, "user-a", 2)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expired consent error=%v, want %v", err, ErrExpired)
	}
}

func TestClientRegistrationSettingsUsesAuthAuthorityAndStrictSchema(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer runtime" {
			t.Errorf("missing trusted runtime identity: %q", r.Header.Get("Authorization"))
		}
		if requests == 1 {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/auth/consumers/lwc/registration/settings" || r.Header.Get("X-LWC-User-Credential") != "" {
				t.Errorf("public settings request=%s %s credential=%q", r.Method, r.URL.Path, r.Header.Get("X-LWC-User-Credential"))
			}
		} else {
			if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/auth/consumers/lwc/management/registration-settings" || r.Header.Get("X-LWC-User-Credential") != "admin-credential" {
				t.Errorf("admin settings request=%s %s credential=%q", r.Method, r.URL.Path, r.Header.Get("X-LWC-User-Credential"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["schema_version"] != float64(1) || body["registration_enabled"] != false {
				t.Errorf("unexpected admin settings body=%#v err=%v", body, err)
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"schema_version":1,"registration_enabled":false,"email_registration_enabled":true,"google_registration_enabled":false}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	settings, err := client.PublicRegistrationSettings(context.Background())
	if err != nil || settings.RegistrationEnabled || !settings.EmailRegistrationEnabled || settings.GoogleRegistrationEnabled {
		t.Fatalf("public settings=%+v err=%v", settings, err)
	}
	settings, err = client.UpdateManagementRegistrationSettings(context.Background(), "admin-credential", RegistrationSettingsPatch{RegistrationEnabled: boolPointer(false)})
	if err != nil || settings.RegistrationEnabled || requests != 2 {
		t.Fatalf("updated settings=%+v requests=%d err=%v", settings, requests, err)
	}
}

func boolPointer(value bool) *bool { return &value }

func testClient(t *testing.T, baseURL string, token RuntimeTokenSource) *Client {
	t.Helper()
	client, err := New(Config{BaseURL: baseURL, Environment: "dev", Resource: "https://bff.example.test",
		Audience: "https://auth-runtime.example.test", Timeout: 5 * time.Second}, nil, token)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
