package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type demoUserLookupFixture struct {
	user *UserRecord
	err  error
	ids  []string
}

func (f *demoUserLookupFixture) GetUserByID(_ context.Context, id string) (*UserRecord, error) {
	f.ids = append(f.ids, id)
	return f.user, f.err
}

type demoSessionIssuerFixture struct {
	userID  string
	role    string
	version int64
	calls   int
	err     error
}

func (f *demoSessionIssuerFixture) Issue(_ context.Context, userID, role, _ string, version ...int64) (string, error) {
	f.calls++
	f.userID, f.role = userID, role
	if len(version) > 0 {
		f.version = version[0]
	}
	if f.err != nil {
		return "", f.err
	}
	return "refresh-session-fixture", nil
}

func newDemoTestRouter(handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestBodyLimit())
	router.POST("/demo", handler)
	return router
}

func TestDemoLoginAcceptsOnlyEmptyRequestAndIssuesConfiguredIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		body []byte
	}{
		{name: "no body"},
		{name: "empty object", body: []byte("{}")},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookup := &demoUserLookupFixture{user: &UserRecord{
				Email: "demo@example.test", Role: "editor", AuthVersion: 4,
			}}
			sessions := &demoSessionIssuerFixture{}
			router := newDemoTestRouter(demoLoginHandlerWithIssuer(lookup, "configured-demo-user", "fixture-jwt-key", HostRefreshCookiePolicy(), sessions))
			var body io.Reader
			if test.body != nil {
				body = bytes.NewReader(test.body)
			}
			request := httptest.NewRequest(http.MethodPost, "/demo", body)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("Demo login status=%d, want %d", recorder.Code, http.StatusOK)
			}
			var response LoginResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode Demo login response: %v", err)
			}
			if response.User.ID != "configured-demo-user" || response.User.Email != "demo@example.test" || response.User.Role != "editor" || response.AccessToken == "" {
				t.Fatalf("Demo response user=%#v access_token_present=%v", response.User, response.AccessToken != "")
			}
			claims, err := ValidateToken(response.AccessToken, "fixture-jwt-key")
			if err != nil || claims.Sub != "configured-demo-user" || claims.Role != "editor" || claims.AuthVersion != 4 {
				t.Fatalf("access claims=%#v validation_error=%v", claims, err)
			}
			if len(lookup.ids) != 1 || lookup.ids[0] != "configured-demo-user" {
				t.Fatalf("lookup IDs=%#v", lookup.ids)
			}
			if sessions.calls != 1 || sessions.userID != "configured-demo-user" || sessions.role != "editor" || sessions.version != 4 {
				t.Fatalf("durable session issue calls=%d user=%q role=%q version=%d", sessions.calls, sessions.userID, sessions.role, sessions.version)
			}
			cookies := recorder.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != "__Host-lwc_refresh" || cookies[0].Value != "refresh-session-fixture" || cookies[0].Domain != "" || cookies[0].Path != "/" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].MaxAge != int(refreshTokenTTL.Seconds()) {
				t.Fatalf("host refresh cookie attributes invalid (count=%d)", len(cookies))
			}
		})
	}
}

func TestDemoLoginRejectsIdentityInputMalformedAndTrailingJSON(t *testing.T) {
	for _, body := range []string{
		`{"identity":"other"}`, `null`, `[]`, `true`, `{`, `{} {}`, `{} trailing`,
	} {
		t.Run(body, func(t *testing.T) {
			lookup := &demoUserLookupFixture{user: &UserRecord{Email: "demo@example.test"}}
			sessions := &demoSessionIssuerFixture{}
			router := newDemoTestRouter(demoLoginHandlerWithIssuer(lookup, "configured-demo-user", "fixture-jwt-key", HostRefreshCookiePolicy(), sessions))
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/demo", strings.NewReader(body)))
			if recorder.Code != http.StatusBadRequest || len(lookup.ids) != 0 || sessions.calls != 0 || len(recorder.Result().Cookies()) != 0 {
				t.Fatalf("invalid Demo body status=%d lookup_calls=%d issue_calls=%d cookie_count=%d", recorder.Code, len(lookup.ids), sessions.calls, len(recorder.Result().Cookies()))
			}
		})
	}
}

func TestDemoLoginReturns413ForKnownAndUnknownLengthBodies(t *testing.T) {
	for _, knownLength := range []bool{true, false} {
		lookup := &demoUserLookupFixture{user: &UserRecord{Email: "demo@example.test"}}
		router := newDemoTestRouter(demoLoginHandlerWithIssuer(lookup, "configured-demo-user", "fixture-jwt-key", HostRefreshCookiePolicy(), &demoSessionIssuerFixture{}))
		body := strings.Repeat("x", int(MaxRequestBodyBytes)+1)
		request := httptest.NewRequest(http.MethodPost, "/demo", strings.NewReader(body))
		if !knownLength {
			request.ContentLength = -1
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusRequestEntityTooLarge || len(lookup.ids) != 0 {
			t.Fatalf("known_length=%v oversized status=%d lookup_calls=%d", knownLength, recorder.Code, len(lookup.ids))
		}
	}
}

func TestDemoLoginFailsClosedForMissingInactiveUnavailableAndSessionErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		uid  string
		user *UserRecord
		err  error
		want int
	}{
		{name: "missing configured UID", user: &UserRecord{Email: "demo@example.test"}, want: http.StatusServiceUnavailable},
		{name: "missing user", uid: "configured-demo-user", want: http.StatusServiceUnavailable},
		{name: "storage unavailable", uid: "configured-demo-user", err: errors.New("storage unavailable"), want: http.StatusServiceUnavailable},
		{name: "inactive user", uid: "configured-demo-user", user: &UserRecord{Email: "demo@example.test", Status: AccountSuspended}, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookup := &demoUserLookupFixture{user: test.user, err: test.err}
			sessions := &demoSessionIssuerFixture{}
			router := newDemoTestRouter(demoLoginHandlerWithIssuer(lookup, test.uid, "fixture-jwt-key", HostRefreshCookiePolicy(), sessions))
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/demo", nil))
			if recorder.Code != test.want || sessions.calls != 0 || len(recorder.Result().Cookies()) != 0 {
				t.Fatalf("Demo status=%d issue_calls=%d cookie_count=%d, want %d", recorder.Code, sessions.calls, len(recorder.Result().Cookies()), test.want)
			}
			if test.uid == "" && len(lookup.ids) != 0 {
				t.Fatalf("missing configured UID triggered lookup: %#v", lookup.ids)
			}
		})
	}

	lookup := &demoUserLookupFixture{user: &UserRecord{Email: "demo@example.test"}}
	failedSessions := &demoSessionIssuerFixture{err: errors.New("session unavailable")}
	router := newDemoTestRouter(demoLoginHandlerWithIssuer(lookup, "configured-demo-user", "fixture-jwt-key", HostRefreshCookiePolicy(), failedSessions))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/demo", nil))
	if recorder.Code != http.StatusInternalServerError || len(recorder.Result().Cookies()) != 0 {
		t.Fatalf("session failure status=%d cookie_count=%d", recorder.Code, len(recorder.Result().Cookies()))
	}

	router = newDemoTestRouter(DemoLoginHandlerWithRepository(lookup, "configured-demo-user", "fixture-jwt-key", HostRefreshCookiePolicy(), nil))
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/demo", nil))
	if recorder.Code != http.StatusInternalServerError || len(recorder.Result().Cookies()) != 0 {
		t.Fatalf("nil session authority status=%d cookie_count=%d", recorder.Code, len(recorder.Result().Cookies()))
	}
}

func TestDemoLoginIssuesDurableSessionWithFirestoreEmulator(t *testing.T) {
	client := accountEmulator(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	userID := "lwc366-demo-fixture-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	user := UserRecord{Email: "demo@example.test", Role: "editor", AuthVersion: 4}
	userRef := client.Collection("users").Doc(userID)
	if _, err := userRef.Set(ctx, user); err != nil {
		t.Fatal("seed emulator Demo user")
	}
	t.Cleanup(func() { _, _ = userRef.Delete(context.Background()) })

	sessions := NewRefreshSessionAuthorityWithConfig(client, SessionAuthorityConfig{Environment: "lwc-366-demo-fixture"})
	router := newDemoTestRouter(DemoLoginHandlerWithRepository(NewIdentityRepository(client), userID, "fixture-jwt-key", HostRefreshCookiePolicy(), sessions))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/demo", nil).WithContext(ctx))
	if recorder.Code != http.StatusOK {
		t.Fatalf("emulator Demo login status=%d, want %d", recorder.Code, http.StatusOK)
	}
	var response LoginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode emulator Demo response: %v", err)
	}
	accessClaims, err := ValidateToken(response.AccessToken, "fixture-jwt-key")
	if err != nil || accessClaims.Sub != userID || accessClaims.Role != user.Role || accessClaims.AuthVersion != user.AuthVersion {
		t.Fatalf("emulator access claims=%#v validation_error=%v", accessClaims, err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("emulator refresh cookie count=%d, want one", len(cookies))
	}
	refreshClaims, err := parseRefreshToken(cookies[0].Value, "fixture-jwt-key")
	if err != nil || refreshClaims.Sub != userID || refreshClaims.Role != user.Role || refreshClaims.AuthVersion != user.AuthVersion || refreshClaims.SessionID == "" {
		t.Fatalf("emulator refresh claims valid=%v error=%v", err == nil, err)
	}
	snapshot, err := sessions.sessionRef(refreshClaims.SessionID).Get(ctx)
	if err != nil {
		t.Fatal("read durable emulator session")
	}
	var stored refreshSessionDocument
	if err := snapshot.DataTo(&stored); err != nil || stored.UserID != userID || stored.Role != user.Role || stored.Status != refreshSessionStatusActive || stored.TokenHash != hashRefreshToken(cookies[0].Value) {
		t.Fatalf("durable emulator session mismatch (decode_error=%v)", err)
	}
	t.Cleanup(func() { _, _ = sessions.sessionRef(refreshClaims.SessionID).Delete(context.Background()) })
}

type rotatingPasswordLoginFixture struct {
	userID string
	user   *UserRecord
}

func (f *rotatingPasswordLoginFixture) GetPasswordUserByEmail(_ context.Context, email string) (string, *UserRecord, error) {
	if CanonicalizeEmail(f.user.Email) != email {
		return "", nil, errors.New("user unavailable")
	}
	return f.userID, f.user, nil
}

func TestSyntheticPasswordRotationKeepsUIDAcrossAuthAndBFFCompatibility(t *testing.T) {
	const (
		userID = "fixture-preserved-user"
		email  = "demo-fixture@example.test"
		old    = "fixture-old-password"
		fresh  = "fixture-new-password"
	)
	hash, err := bcrypt.GenerateFromPassword([]byte(old), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	store := &rotatingPasswordLoginFixture{userID: userID, user: &UserRecord{Email: email, Role: "member", PasswordHash: string(hash)}}
	authRouter := gin.New()
	authRouter.POST("/login", LoginHandlerWithRepository(store, "fixture-jwt-key", HostRefreshCookiePolicy()))
	bffRouter := gin.New()
	bffRouter.POST("/login", LoginHandlerWithRepository(store, "fixture-jwt-key", LegacyRefreshCookiePolicy()))

	login := func(router *gin.Engine, password string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		body, _ := json.Marshal(LoginRequest{Email: email, Password: password})
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body)))
		return recorder
	}
	for name, router := range map[string]*gin.Engine{"Auth": authRouter, "BFF compatibility": bffRouter} {
		t.Run(name+" pre-rotation", func(t *testing.T) {
			response := login(router, old)
			if response.Code != http.StatusOK {
				t.Fatalf("fixture pre-rotation login status=%d", response.Code)
			}
			var result LoginResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.User.ID != userID {
				t.Fatalf("fixture pre-rotation user ID=%q decode_error=%v", result.User.ID, err)
			}
		})
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(fresh), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	store.user.PasswordHash = string(newHash)
	for name, router := range map[string]*gin.Engine{"Auth": authRouter, "BFF compatibility": bffRouter} {
		t.Run(name+" after rotation", func(t *testing.T) {
			oldResponse := login(router, old)
			if oldResponse.Code != http.StatusUnauthorized || len(oldResponse.Result().Cookies()) != 0 {
				t.Fatalf("fixture old-pair status=%d cookie_count=%d, want 401 and no cookie", oldResponse.Code, len(oldResponse.Result().Cookies()))
			}
			newResponse := login(router, fresh)
			if newResponse.Code != http.StatusOK {
				t.Fatalf("fixture new-pair status=%d, want %d", newResponse.Code, http.StatusOK)
			}
			var result LoginResponse
			if err := json.Unmarshal(newResponse.Body.Bytes(), &result); err != nil || result.User.ID != userID {
				t.Fatalf("fixture new-pair user ID=%q decode_error=%v", result.User.ID, err)
			}
		})
	}
}
