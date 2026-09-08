package auth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/rayer/llm-wiki-bff/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func TestSuspendedPasswordLoginCannotIssueSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	// Decode a persisted account status; before LWC-325 it is silently ignored.
	var user UserRecord
	if err := json.Unmarshal([]byte(`{"Status":"suspended"}`), &user); err != nil {
		t.Fatal(err)
	}
	user.Email, user.PasswordHash = "suspended@example.test", string(hash)
	repo := &recordingLoginStore{user: &user}
	router := gin.New()
	router.POST("/login", LoginHandlerWithRepository(repo, "test-secret", HostRefreshCookiePolicy()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"email":"suspended@example.test","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("suspended password login = %d, want 401", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("suspended login issued a refresh cookie")
	}
}

func TestAccountAuthRejectsSuspensionOldVersionAndStaleAdminRole(t *testing.T) {
	for _, tc := range []struct {
		name string
		user *UserRecord
		err  error
		want int
	}{
		{"legacy active", &UserRecord{Role: "admin"}, nil, 200},
		{"suspended", &UserRecord{Status: AccountSuspended, Role: "admin"}, nil, 401},
		{"restored old token", &UserRecord{Status: AccountActive, AuthVersion: 1, Role: "admin"}, nil, 401},
		{"missing user", nil, nil, 401},
		{"authority down", nil, errors.New("offline"), 401},
		{"unknown status", &UserRecord{Status: "pending", Role: "admin"}, nil, 401},
		{"stale admin role", &UserRecord{Role: "user"}, nil, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, _ := GenerateAccessToken("account", "admin", "test-secret")
			router := gin.New()
			router.GET("/protected", JWTAuthWithAccountLookup(config.Config{JWTSecret: "test-secret"}, func(context.Context, string) (*UserRecord, error) { return tc.user, tc.err }), AdminOnly(), func(c *gin.Context) { c.Status(200) })
			request := httptest.NewRequest("GET", "/protected", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("got %d want %d", recorder.Code, tc.want)
			}
		})
	}
}

func TestCompatibilitySessionCarriesAccountVersion(t *testing.T) {
	token, err := issueRefreshSession(context.Background(), nil, "owner", "user", "test-secret", 3)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := parseRefreshToken(token, "test-secret")
	if err != nil || claims.AuthVersion != 3 {
		t.Fatalf("compatibility refresh version: %v", err)
	}
}
