package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type DemoUserLookup interface {
	GetUserByID(context.Context, string) (*UserRecord, error)
}

type refreshSessionIssuer interface {
	Issue(context.Context, string, string, string, ...int64) (string, error)
}

// DemoLoginHandlerWithRepository signs in the configured existing Demo user.
func DemoLoginHandlerWithRepository(repo DemoUserLookup, userID, jwtSecret string, cookiePolicy RefreshCookiePolicy, sessions *RefreshSessionAuthority) gin.HandlerFunc {
	var issuer refreshSessionIssuer
	if sessions != nil {
		issuer = sessions
	}
	return demoLoginHandlerWithIssuer(repo, userID, jwtSecret, cookiePolicy, issuer)
}

func demoLoginHandlerWithIssuer(repo DemoUserLookup, userID, jwtSecret string, cookiePolicy RefreshCookiePolicy, sessions refreshSessionIssuer) gin.HandlerFunc {
	configuredUserID := strings.TrimSpace(userID)
	return func(c *gin.Context) {
		if !validateDemoLoginBody(c) {
			return
		}

		if repo == nil || !ValidPathSegment(configuredUserID) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo login unavailable"})
			return
		}
		user, err := repo.GetUserByID(c.Request.Context(), configuredUserID)
		if err != nil || user == nil || !user.Active() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo login unavailable"})
			return
		}
		if sessions == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
			return
		}

		accessToken, err := GenerateAccessToken(configuredUserID, user.Role, jwtSecret, user.AuthVersion)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
			return
		}
		refreshToken, err := sessions.Issue(c.Request.Context(), configuredUserID, user.Role, jwtSecret, user.AuthVersion)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
			return
		}
		setRefreshTokenCookieWithPolicy(c, refreshToken, int(refreshTokenTTL.Seconds()), cookiePolicy)
		c.JSON(http.StatusOK, LoginResponse{
			AccessToken: accessToken,
			User:        User{ID: configuredUserID, Email: user.Email, Role: user.Role},
		})
	}
}

func validateDemoLoginBody(c *gin.Context) bool {
	if c.Request.Body == nil {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, MaxRequestBodyBytes+1))
	var maxBytesErr *http.MaxBytesError
	if int64(len(body)) > MaxRequestBodyBytes || errors.As(err, &maxBytesErr) {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
		return false
	}
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return false
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return true
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil || payload == nil || len(payload) != 0 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return false
	}
	return true
}
