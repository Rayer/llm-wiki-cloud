package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type methodRegistrationGate struct {
	master        *bool
	email, google bool
	err           error
	calls         []string
}

func (g *methodRegistrationGate) IsRegistrationEnabled(_ context.Context, method string) (bool, error) {
	g.calls = append(g.calls, method)
	enabled := g.master == nil || *g.master
	if method == "email" {
		return enabled && g.email, g.err
	}
	if method == "google" {
		return enabled && g.google, g.err
	}
	return false, errors.New("unknown method")
}

func TestEmailRegistrationMethodsAndExistingLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, master := range []bool{false, true} {
		for _, email := range []bool{false, true} {
			for _, google := range []bool{false, true} {
				t.Run(fmt.Sprintf("master=%t/email=%t/google=%t", master, email, google), func(t *testing.T) {
					gate := &methodRegistrationGate{master: &master, email: email, google: google}
					repo := &recordingRegistrationStore{}
					router := gin.New()
					router.POST("/register", RegisterHandlerWithRepository(repo, "test-secret", gate))
					router.POST("/login", LoginHandlerWithRepository(&recordingLoginStore{userID: "existing-user", user: &UserRecord{Email: "existing@example.test", PasswordHash: mustHashForIdentityTest(t, "password123")}}, "test-secret", LegacyRefreshCookiePolicy()))
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"email":"new@example.test","password":"password123"}`)))
					want, calls := http.StatusForbidden, 0
					if master && email {
						want, calls = http.StatusCreated, 1
					}
					if rec.Code != want || repo.calls != calls || strings.Join(gate.calls, ",") != "email" {
						t.Fatalf("registration status=%d calls=%d gate=%v", rec.Code, repo.calls, gate.calls)
					}
					if !(master && email) && (len(rec.Result().Cookies()) != 0 || strings.Contains(rec.Body.String(), "token")) {
						t.Fatal("closed signup issued session")
					}
					rec = httptest.NewRecorder()
					router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"email":"existing@example.test","password":"password123"}`)))
					if rec.Code != http.StatusOK || len(gate.calls) != 1 {
						t.Fatalf("existing login status=%d gate=%v", rec.Code, gate.calls)
					}
				})
			}
		}
	}
}

func TestEmailRegistrationGateReadFailureCreatesNothing(t *testing.T) {
	repo := &recordingRegistrationStore{}
	router := gin.New()
	router.POST("/register", RegisterHandlerWithRepository(repo, "test-secret", &methodRegistrationGate{email: true, err: errors.New("settings unavailable")}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"email":"new@example.test","password":"password123"}`)))
	if rec.Code != http.StatusInternalServerError || repo.calls != 0 || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("status=%d calls=%d", rec.Code, repo.calls)
	}
}
