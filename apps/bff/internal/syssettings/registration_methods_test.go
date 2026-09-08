package syssettings

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/config"
)

func TestRegistrationMethodsFourCombinations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, email := range []bool{false, true} {
		for _, google := range []bool{false, true} {
			t.Run(fmt.Sprintf("email=%t/google=%t", email, google), func(t *testing.T) {
				store := &FakeStore{Enabled: true}
				router := gin.New()
				router.PATCH("/settings", AdminPatchSettingsHandler(store))
				router.GET("/settings", AdminGetSettingsHandler(store))
				router.GET("/config", PublicConfigHandler(store))
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/settings", strings.NewReader(fmt.Sprintf(`{"email_registration_enabled":%t,"google_registration_enabled":%t}`, email, google))))
				if rec.Code != http.StatusOK {
					t.Fatalf("PATCH status=%d body=%s", rec.Code, rec.Body.String())
				}
				for _, path := range []string{"/settings", "/config"} {
					rec = httptest.NewRecorder()
					router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
					var body map[string]any
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if body["email_registration_enabled"] != email || body["google_registration_enabled"] != google {
						t.Fatalf("%s returned %v", path, body)
					}
				}
			})
		}
	}
}

func TestRegistrationMethodResolutionPreservesLegacyClosedPosture(t *testing.T) {
	envOpen, envClosed := true, false
	tests := []struct {
		name          string
		data          map[string]interface{}
		env           *bool
		email, google bool
	}{
		{"legacy closed beats environment", map[string]interface{}{"registration_enabled": false}, &envOpen, false, false},
		{"legacy open", map[string]interface{}{"registration_enabled": true}, &envClosed, true, true},
		{"closed environment", nil, &envClosed, false, false},
		{"open environment", nil, &envOpen, true, true},
		{"document missing fields", map[string]interface{}{}, &envOpen, false, false},
		{"malformed legacy", map[string]interface{}{"registration_enabled": "true"}, &envOpen, false, false},
		{"partial migration closed sibling", map[string]interface{}{"registration_enabled": false, "email_registration_enabled": true}, &envOpen, true, false},
		{"explicit methods override legacy", map[string]interface{}{"registration_enabled": false, "email_registration_enabled": true, "google_registration_enabled": true}, &envClosed, true, true},
		{"invalid explicit fields do not inherit open", map[string]interface{}{"registration_enabled": true, "email_registration_enabled": nil, "google_registration_enabled": "true"}, &envOpen, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSettings(tt.data, tt.env)
			if got.EmailRegistrationEnabled != tt.email || got.GoogleRegistrationEnabled != tt.google || got.RegistrationEnabled != (tt.email && tt.google) {
				t.Fatalf("resolved %+v", got)
			}
		})
	}
}

func TestRegistrationMethodPartialUpdatesAndLegacyCompatibility(t *testing.T) {
	store := &FakeStore{Enabled: false}
	router := gin.New()
	router.PATCH("/settings", AdminPatchSettingsHandler(store))
	for _, tt := range []struct {
		body          string
		email, google bool
	}{
		{`{"email_registration_enabled":true}`, true, false},
		{`{"google_registration_enabled":true}`, true, true},
		{`{"email_registration_enabled":false}`, false, true},
		{`{"registration_enabled":false}`, false, false},
		{`{"registration_enabled":true}`, true, true},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/settings", strings.NewReader(tt.body)))
		got, err := store.GetSettings(t.Context())
		if rec.Code != http.StatusOK || err != nil || got.EmailRegistrationEnabled != tt.email || got.GoogleRegistrationEnabled != tt.google {
			t.Fatalf("%s status=%d settings=%+v error=%v", tt.body, rec.Code, got, err)
		}
	}
	for _, body := range []string{`{}`, `null`, `{"email_registration_enabled":"true"}`, `{"google_registration_enabled":null}`, `{"email_registration_enabled":null,"google_registration_enabled":true}`, `{"email_registration_enabled":true,"registration_enabled":true}`, `{"unknown":true}`, `{"email_registration_enabled":false}{}`} {
		before := store.SetCalls
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/settings", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest || store.SetCalls != before {
			t.Fatalf("invalid %s status=%d mutated=%t", body, rec.Code, store.SetCalls != before)
		}
	}
}

func TestRegistrationMethodsAdminAuthorizationAndReadFailure(t *testing.T) {
	store := &FakeStore{Enabled: true}
	router := gin.New()
	router.GET("/config", PublicConfigHandler(store))
	admin := router.Group("/admin")
	admin.Use(auth.JWTAuth(config.Config{JWTSecret: "test-secret"}), auth.AdminOnly())
	admin.PATCH("/settings", AdminPatchSettingsHandler(store))
	userToken, err := auth.GenerateAccessToken("user-1", "", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", userToken} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/admin/settings", strings.NewReader(`{"email_registration_enabled":false,"google_registration_enabled":false}`))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		router.ServeHTTP(rec, req)
		want := http.StatusUnauthorized
		if token != "" {
			want = http.StatusForbidden
		}
		if rec.Code != want || store.SetCalls != 0 {
			t.Fatalf("non-admin status=%d mutations=%d", rec.Code, store.SetCalls)
		}
	}
	store.Err = fmt.Errorf("settings unavailable")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config", nil))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "true") {
		t.Fatalf("read failure exposed open capability: %s", rec.Body.String())
	}
}
