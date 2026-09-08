package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rayer/llm-wiki-bff/internal/config"
)

// Claims is the JWT claims structure for HS256 tokens.
// Sub is kept at the outer level for backward compatibility (claims.Sub).
// RegisteredClaims is embedded for standard JWT fields (exp, iat, etc.).
type Claims struct {
	AuthVersion int64  `json:"av,omitempty"`
	Sub         string `json:"sub"`
	Role        string `json:"role,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
	SessionID   string `json:"sid,omitempty"`
	TokenID     string `json:"tid,omitempty"`
	jwt.RegisteredClaims
}

const (
	accessTokenTTL         = 15 * time.Minute
	refreshTokenTTL        = 7 * 24 * time.Hour
	accessTokenType        = "access"
	refreshTokenType       = "refresh"
	refreshTokenCookieName = "refresh_token"
	refreshTokenCookiePath = "/"
	refreshTokenDomain     = "rayer.idv.tw"
)

var refreshTokenStore = struct {
	sync.Mutex
	active map[string]time.Time
}{
	active: make(map[string]time.Time),
}

// JWTAuth returns a Gin middleware that validates a JWT from the Authorization header.
// Config-driven: uses cfg.JWTSecret for HS256 verification.
// DEV mode: if cfg.DevJWT is set AND no Authorization header is present,
// it injects cfg.DefaultUserID into the context.
func JWTAuth(cfg config.Config) gin.HandlerFunc {
	return jwtAuth(cfg, nil, false)
}

// JWTAuthWithAccountLookup validates current account access on every request.
func JWTAuthWithAccountLookup(cfg config.Config, lookup AccountLookup) gin.HandlerFunc {
	return jwtAuth(cfg, lookup, true)
}

func jwtAuth(cfg config.Config, lookup AccountLookup, enforce bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")

		// DEV mode: inject user from X-User-ID header when DevJWT is configured and no auth header
		if cfg.DevJWT && !enforce && authHeader == "" {
			userID := strings.TrimSpace(c.GetHeader("X-User-ID"))
			if !ValidPathSegment(userID) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid user ID"})
				return
			}
			userRole := strings.TrimSpace(c.GetHeader("X-User-Role"))
			if userRole == "" {
				userRole = "admin"
			}
			c.Set("userID", userID)
			c.Set("userRole", userRole)
			c.Next()
			return
		}

		// Production / normal mode: require Bearer token
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing Authorization header"})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid Authorization header format, expected: Bearer <token>"})
			return
		}

		claims, err := ValidateToken(parts[1], cfg.JWTSecret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		claims.Sub = strings.TrimSpace(claims.Sub)
		if !ValidPathSegment(claims.Sub) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}

		if enforce {
			if lookup == nil {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "account access unavailable"})
				return
			}
			user, err := lookup(c.Request.Context(), claims.Sub)
			if err != nil || !user.AllowsVersion(claims.AuthVersion) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
				return
			}
			claims.Role = user.Role
		}
		c.Set("userID", claims.Sub)
		c.Set("userRole", claims.Role)
		c.Next()
	}
}

// AdminOnly returns a Gin middleware that allows only users with the admin role.
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("userRole") != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin role required"})
			return
		}
		c.Next()
	}
}

// ValidPathSegment reports whether value can be safely used as one path segment.
func ValidPathSegment(value string) bool {
	return value != "" &&
		value != "." &&
		value != ".." &&
		!strings.ContainsAny(value, `/\`+"\x00")
}

// GenerateToken creates a self-signed HS256 JWT for development/testing.
// Exported for use in tests and dev tooling. Not used in production flows.
func GenerateToken(userID, secret string, ttl time.Duration) (string, error) {
	return GenerateTokenWithRole(userID, "", secret, ttl)
}

// GenerateTokenWithRole creates a self-signed HS256 JWT with a role claim.
func GenerateTokenWithRole(userID, role, secret string, ttl time.Duration) (string, error) {
	return generateToken(userID, role, secret, ttl, "", "")
}

// GenerateAccessToken creates a short-lived HS256 JWT for API authorization.
func GenerateAccessToken(userID, role, secret string, version ...int64) (string, error) {
	return generateTokenAt(userID, role, secret, accessTokenTTL, accessTokenType, "", "", "", time.Now(), version...)
}

// GenerateRefreshToken is retained for the local/test compatibility lane. The
// production routers use RefreshSessionAuthority.Issue, which persists only a
// token hash in Firestore. Local mode has no durable Firestore authority.
func GenerateRefreshToken(userID, role, secret string, version ...int64) (string, error) {
	jti, err := randomTokenID()
	if err != nil {
		return "", err
	}
	token, err := generateTokenAt(userID, role, secret, refreshTokenTTL, refreshTokenType, jti, "", "", time.Now(), version...)
	if err != nil {
		return "", err
	}
	refreshTokenStore.Lock()
	refreshTokenStore.active[jti] = time.Now().Add(refreshTokenTTL)
	refreshTokenStore.Unlock()
	return token, nil
}

// ValidateToken validates an HS256 access token and returns its claims.
func ValidateToken(tokenString, secret string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	if !token.Valid || claims.TokenType == refreshTokenType {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

func validateRefreshToken(tokenString, secret string) (*Claims, error) {
	claims, err := parseRefreshToken(tokenString, secret)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	refreshTokenStore.Lock()
	expiresAt, ok := refreshTokenStore.active[claims.ID]
	if ok {
		delete(refreshTokenStore.active, claims.ID)
	}
	refreshTokenStore.Unlock()
	if !ok || now.After(expiresAt) {
		return nil, fmt.Errorf("invalid refresh token")
	}
	return claims, nil
}

// parseRefreshToken validates only the signed wire token. Durable authority
// state is checked separately by RefreshSessionAuthority.Rotate.
func parseRefreshToken(tokenString, secret string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid || claims.TokenType != refreshTokenType || claims.ID == "" {
		return nil, fmt.Errorf("invalid refresh token")
	}
	return claims, nil
}

func generateToken(userID, role, secret string, ttl time.Duration, tokenType, jti string) (string, error) {
	return generateTokenAt(userID, role, secret, ttl, tokenType, jti, "", "", time.Now())
}

func generateRefreshTokenAt(userID, role, secret, sessionID string, now time.Time, version ...int64) (string, error) {
	tokenID, err := randomTokenID()
	if err != nil {
		return "", err
	}
	return generateTokenAt(userID, role, secret, refreshTokenTTL, refreshTokenType, sessionID, sessionID, tokenID, now, version...)
}

func generateTokenAt(userID, role, secret string, ttl time.Duration, tokenType, jti, sessionID, tokenID string, now time.Time, version ...int64) (string, error) {
	claims := &Claims{
		Sub: userID, Role: role, TokenType: tokenType, SessionID: sessionID, TokenID: tokenID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        jti,
		},
	}

	if len(version) > 0 {
		claims.AuthVersion = version[0]
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func randomTokenID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func resetRefreshTokensForTest() {
	refreshTokenStore.Lock()
	refreshTokenStore.active = make(map[string]time.Time)
	refreshTokenStore.Unlock()
}
