package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// LogoutResponse confirms that the client refresh token was cleared.
type LogoutResponse struct {
	OK bool `json:"ok"`
}

// LogoutHandler clears the refresh token cookie.
//
//	@Summary		Log out
//	@Description	Clears the runtime-specific refresh cookie. No access token or request body is required.
//	@Tags			auth
//	@Produce		json
//	@Success		200	{object}	LogoutResponse
//	@Header			200	{string}	Set-Cookie	"BFF compatibility: refresh_token; Path=/; Domain=rayer.idv.tw; Max-Age=0; HttpOnly; Secure; SameSite=Lax"
func LogoutHandler() gin.HandlerFunc {
	return LogoutHandlerWithCookiePolicy(LegacyRefreshCookiePolicy())
}

// LogoutHandlerWithCookiePolicy returns a logout handler using the supplied immutable cookie policy.
func LogoutHandlerWithCookiePolicy(cookiePolicy RefreshCookiePolicy) gin.HandlerFunc {
	return func(c *gin.Context) {
		setRefreshTokenCookieWithPolicy(c, "", -1, cookiePolicy)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// LogoutHandlerWithSessionAuthority revokes the durable session represented
// by the refresh cookie and always clears the existing cookie contract.
func LogoutHandlerWithSessionAuthority(authority *RefreshSessionAuthority, jwtSecret string, cookiePolicy RefreshCookiePolicy) gin.HandlerFunc {
	return func(c *gin.Context) {
		var revokeErr error
		if authority != nil {
			if cookie, err := c.Request.Cookie(cookiePolicy.Name); err == nil && cookie.Value != "" {
				revokeErr = authority.Revoke(c.Request.Context(), cookie.Value, jwtSecret)
			}
		}
		setRefreshTokenCookieWithPolicy(c, "", -1, cookiePolicy)
		if revokeErr != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth persistence unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}
