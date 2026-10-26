package auth

import (
	"context"
	"errors"
	"net/http"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrPasswordSelfServiceUnavailable = errors.New("password self-service unavailable")
	ErrPasswordSessionInvalid         = errors.New("account session is no longer valid")
)

type PasswordSelfServiceStatus struct {
	Allowed    bool `json:"allowed"`
	Configured bool `json:"configured"`
}

type changePasswordRequest struct {
	NewPassword     string `json:"new_password" binding:"required"`
	ConfirmPassword string `json:"confirm_password" binding:"required"`
}

func boolPointer(value bool) *bool { return &value }

func isPasswordConfigured(user *UserRecord) bool {
	if user == nil {
		return false
	}
	if user.PasswordConfigured != nil {
		return *user.PasswordConfigured
	}
	// Older password accounts predate the explicit flag; empty-hash OAuth
	// accounts remain unset, and future generated hashes can carry false.
	return user.PasswordHash != ""
}

func PasswordSelfServiceStatusHandler(fs *firestore.Client, demoUserID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if fs == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "password settings unavailable"})
			return
		}
		userID := c.GetString("userID")
		if !ValidPathSegment(userID) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		user, err := GetUser(c.Request.Context(), fs, userID)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
				return
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "password settings unavailable"})
			return
		}
		if !user.Active() {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, PasswordSelfServiceStatus{
			Allowed: userID != demoUserID, Configured: isPasswordConfigured(user),
		})
	}
}

func ChangePasswordHandler(fs *firestore.Client, demoUserID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if fs == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "password update unavailable"})
			return
		}
		var request changePasswordRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "new password and confirmation are required"})
			return
		}
		if request.NewPassword != request.ConfirmPassword {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password confirmation does not match"})
			return
		}
		if len(request.NewPassword) < 8 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 8 characters"})
			return
		}
		userID := c.GetString("userID")
		if userID == demoUserID {
			c.JSON(http.StatusForbidden, gin.H{"error": ErrPasswordSelfServiceUnavailable.Error()})
			return
		}
		version, ok := c.Get("authVersion")
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		authVersion, ok := version.(int64)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(request.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password could not be accepted"})
			return
		}
		if err := UpdateOwnPassword(c.Request.Context(), fs, userID, authVersion, demoUserID, string(hash)); err != nil {
			switch {
			case errors.Is(err, ErrPasswordSelfServiceUnavailable):
				c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			case errors.Is(err, ErrPasswordSessionInvalid):
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			default:
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "password update unavailable"})
			}
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"status": "updated"})
	}
}

// UpdateOwnPassword changes only password state. The token's auth_version is
// checked again in the transaction so a concurrent suspension cannot race a
// successful password write; other credentials remain valid.
func UpdateOwnPassword(ctx context.Context, fs *firestore.Client, userID string, expectedVersion int64, demoUserID, passwordHash string) error {
	if userID == demoUserID {
		return ErrPasswordSelfServiceUnavailable
	}
	if fs == nil || !ValidPathSegment(userID) || passwordHash == "" || expectedVersion < 0 {
		return ErrPasswordSessionInvalid
	}
	return fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		userRef := scopedfirestore.Collection(fs, "users").Doc(userID)
		userSnapshot, err := tx.Get(userRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrPasswordSessionInvalid
			}
			return err
		}
		var user UserRecord
		if err := userSnapshot.DataTo(&user); err != nil {
			return err
		}
		if !user.AllowsVersion(expectedVersion) {
			return ErrPasswordSessionInvalid
		}
		return tx.Update(userRef, []firestore.Update{
			{Path: "password_hash", Value: passwordHash},
			{Path: "password_configured", Value: true},
		})
	})
}
