package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/gateauth/internal/config"
	scopedfirestore "github.com/rayer/gateauth/internal/firestore"
	"google.golang.org/api/iterator"
)

type managementActor struct {
	claims *Claims
	user   *UserRecord
	scopes []string
}

type managementUserSummary struct {
	ID          string                          `json:"id"`
	Name        string                          `json:"name"`
	Email       string                          `json:"email"`
	Role        string                          `json:"role,omitempty"`
	Status      string                          `json:"status"`
	ScopeStates map[string]ManagementScopeState `json:"scope_states"`
}

type managementGrantRequest struct {
	SchemaVersion int    `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	TargetUserID  string `json:"target_user_id"`
	Scope         string `json:"scope"`
	Action        string `json:"action"`
}

type managementRegistrationSettingsRequest struct {
	SchemaVersion             int   `json:"schema_version"`
	RegistrationEnabled       *bool `json:"registration_enabled,omitempty"`
	EmailRegistrationEnabled  *bool `json:"email_registration_enabled,omitempty"`
	GoogleRegistrationEnabled *bool `json:"google_registration_enabled,omitempty"`
}

func ConsumerRegistrationSettingsHandler(store RegistrationSettingsAuthority, policy LWCConsumerPolicy, validate OIDCCallerValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		if store == nil || !policy.ready() || validate == nil || !authorizeLWCConsumer(c, policy, validate) {
			if !c.IsAborted() {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "consumer authority unavailable"})
			}
			return
		}
		settings, err := store.GetRegistrationSettings(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "registration settings unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "registration_enabled": settings.RegistrationEnabled,
			"email_registration_enabled": settings.EmailRegistrationEnabled, "google_registration_enabled": settings.GoogleRegistrationEnabled})
	}
}

func ManagementRegistrationSettingsGetHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	validate OIDCCallerValidator, verifySession CLIAccessSessionVerifier, management *ManagementAuthority,
	store RegistrationSettingsAuthority) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		actor, statusCode, message := authenticateManagementActor(c, cfg, fs, policy, validate, verifySession, management)
		if statusCode != 0 {
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		if !containsString(actor.scopes, ManagementScopeAuthAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": ErrManagementDenied.Error()})
			return
		}
		if store == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "registration settings unavailable"})
			return
		}
		settings, err := store.GetRegistrationSettings(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "registration settings unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "registration_enabled": settings.RegistrationEnabled,
			"email_registration_enabled": settings.EmailRegistrationEnabled, "google_registration_enabled": settings.GoogleRegistrationEnabled})
	}
}

func ManagementRegistrationSettingsPatchHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	validate OIDCCallerValidator, verifySession CLIAccessSessionVerifier, management *ManagementAuthority,
	store RegistrationSettingsAuthority) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		actor, statusCode, message := authenticateManagementActor(c, cfg, fs, policy, validate, verifySession, management)
		if statusCode != 0 {
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		if !containsString(actor.scopes, ManagementScopeAuthAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": ErrManagementDenied.Error()})
			return
		}
		if store == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "registration settings unavailable"})
			return
		}
		var request managementRegistrationSettingsRequest
		if !decodeStrictManagementBody(c, &request) || request.SchemaVersion != 1 ||
			(request.RegistrationEnabled == nil && request.EmailRegistrationEnabled == nil && request.GoogleRegistrationEnabled == nil) {
			c.JSON(http.StatusBadRequest, gin.H{"error": ErrManagementInvalid.Error()})
			return
		}
		settings, err := management.UpdateRegistrationSettings(c.Request.Context(), actor.claims.Sub,
			actor.claims.ManagementScopeVersions, actor.claims.AuthVersion, store, request.RegistrationEnabled,
			request.EmailRegistrationEnabled, request.GoogleRegistrationEnabled)
		if err != nil {
			c.JSON(managementMutationErrorStatus(err), gin.H{"error": managementMutationErrorMessage(err)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "registration_enabled": settings.RegistrationEnabled,
			"email_registration_enabled": settings.EmailRegistrationEnabled, "google_registration_enabled": settings.GoogleRegistrationEnabled})
	}
}

// ManagementCatalogHandler returns the fixed, nonsecret public projection of
// registered management capabilities.
func ManagementCatalogHandler(policy LWCConsumerPolicy, projection config.AuthConsumerPublicProjection, validate OIDCCallerValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		if !policy.ready() || validate == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "consumer authority unavailable"})
			return
		}
		if !authorizeLWCConsumer(c, policy, validate) {
			return
		}
		if projection.Issuer != policy.Issuer || projection.Resource != policy.Resource || len(projection.ManagementScopes) == 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "management catalog unavailable"})
			return
		}
		scopes := make([]gin.H, 0, len(projection.ManagementScopes))
		for _, scope := range projection.ManagementScopes {
			scopes = append(scopes, gin.H{"id": scope.ID, "product": scope.Product, "resource": projection.Resource,
				"grant_mode": scope.GrantMode, "display_name": gin.H{"en": scope.Name.En, "zh-TW": scope.Name.ZhTW},
				"description": gin.H{"en": scope.Description.En, "zh-TW": scope.Description.ZhTW}})
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "issuer": projection.Issuer, "resource": projection.Resource, "scopes": scopes})
	}
}

// ManagementMeHandler reports current and credential-valid grants for the
// first-party caller without inferring access from a role field.
func ManagementMeHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy, validate OIDCCallerValidator,
	verifySession CLIAccessSessionVerifier, management *ManagementAuthority) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		actor, statusCode, message := authenticateManagementActor(c, cfg, fs, policy, validate, verifySession, management)
		if statusCode != 0 {
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		credentialScopes := actor.claims.ManagementScopeVersions
		if credentialScopes == nil {
			credentialScopes = map[string]int64{}
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "subject": actor.claims.Sub,
			"granted_scopes": actor.scopes, "credential_scopes": credentialScopes})
	}
}

// ManagementUsersHandler is the Auth-owned account lookup used by the BFF
// adapter. It never returns credential material or arbitrary account fields.
func ManagementUsersHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy, validate OIDCCallerValidator,
	verifySession CLIAccessSessionVerifier, management *ManagementAuthority) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		actor, statusCode, message := authenticateManagementActor(c, cfg, fs, policy, validate, verifySession, management)
		if statusCode != 0 {
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		if !containsString(actor.scopes, ManagementScopeAuthAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": ErrManagementDenied.Error()})
			return
		}
		epoch, err := management.AuthorityEpoch(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "management authority unavailable"})
			return
		}
		iter := scopedfirestore.Collection(fs, "users").Documents(c.Request.Context())
		defer iter.Stop()
		users := make([]managementUserSummary, 0)
		for {
			doc, err := iter.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authority unavailable"})
				return
			}
			var user UserRecord
			if doc.DataTo(&user) != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authority unavailable"})
				return
			}
			name, _ := doc.Data()["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" && user.Email != "" {
				name = user.Email
				if at := strings.Index(name, "@"); at > 0 {
					name = name[:at]
				}
			}
			states, err := management.ScopeStates(c.Request.Context(), doc.Ref.ID)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "management authority unavailable"})
				return
			}
			accountStatus := user.Status
			if accountStatus == "" {
				accountStatus = AccountActive
			}
			userSummary := managementUserSummary{ID: doc.Ref.ID, Name: name, Email: user.Email, Status: accountStatus, ScopeStates: states}
			if epoch.Mode == "legacy-shadow" {
				userSummary.Role = user.Role
			}
			users = append(users, userSummary)
		}
		sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "users": users})
	}
}

// ManagementGrantHandler mutates only a catalogued management scope. Auth
// checks the actor's current cap and the target/account revisions in its
// Firestore transaction.
func ManagementGrantHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy, validate OIDCCallerValidator,
	verifySession CLIAccessSessionVerifier, management *ManagementAuthority) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		actor, statusCode, message := authenticateManagementActor(c, cfg, fs, policy, validate, verifySession, management)
		if statusCode != 0 {
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		if !containsString(actor.scopes, ManagementScopeAuthAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": ErrManagementDenied.Error()})
			return
		}
		var request managementGrantRequest
		if !decodeStrictManagementBody(c, &request) || request.SchemaVersion != 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": ErrManagementInvalid.Error()})
			return
		}
		result, err := management.MutateGrant(c.Request.Context(), actor.claims.Sub, actor.claims.ManagementScopeVersions, actor.claims.AuthVersion,
			ManagementMutation{OperationID: request.OperationID, TargetID: request.TargetUserID, Scope: request.Scope, Action: request.Action})
		if err != nil {
			c.JSON(managementMutationErrorStatus(err), gin.H{"error": managementMutationErrorMessage(err)})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

// ManagementOperationHandler supports safe readback after a client loses a
// mutation response. It never resubmits an operation.
func ManagementOperationHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy, validate OIDCCallerValidator,
	verifySession CLIAccessSessionVerifier, management *ManagementAuthority) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		actor, statusCode, message := authenticateManagementActor(c, cfg, fs, policy, validate, verifySession, management)
		if statusCode != 0 {
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		if !containsString(actor.scopes, ManagementScopeAuthAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": ErrManagementDenied.Error()})
			return
		}
		result, err := management.ReadOperation(c.Request.Context(), c.Param("operationID"))
		if err != nil {
			c.JSON(managementMutationErrorStatus(err), gin.H{"error": managementMutationErrorMessage(err)})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func ManagementAccountStatusHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy, validate OIDCCallerValidator,
	verifySession CLIAccessSessionVerifier, management *ManagementAuthority) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		actor, statusCode, message := authenticateManagementActor(c, cfg, fs, policy, validate, verifySession, management)
		if statusCode != 0 {
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		if !containsString(actor.scopes, ManagementScopeAuthAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": ErrManagementDenied.Error()})
			return
		}
		userID := c.Param("userID")
		var request struct {
			SchemaVersion int    `json:"schema_version"`
			Status        string `json:"status"`
		}
		if !ValidPathSegment(userID) || !decodeStrictManagementBody(c, &request) || request.SchemaVersion != 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid management request"})
			return
		}
		if err := management.UpdateAccountStatus(c.Request.Context(), actor.claims.Sub, actor.claims.ManagementScopeVersions, actor.claims.AuthVersion, userID, request.Status); err != nil {
			statusCode := managementMutationErrorStatus(err)
			if errors.Is(err, ErrAccountSelfSuspension) {
				statusCode = http.StatusConflict
			}
			c.JSON(statusCode, gin.H{"error": managementMutationErrorMessage(err)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "status": "updated", "id": userID})
	}
}

func ManagementLegacyAccountHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy, validate OIDCCallerValidator,
	verifySession CLIAccessSessionVerifier, management *ManagementAuthority) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		actor, statusCode, message := authenticateManagementActor(c, cfg, fs, policy, validate, verifySession, management)
		if statusCode != 0 {
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		if !containsString(actor.scopes, ManagementScopeAuthAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": ErrManagementDenied.Error()})
			return
		}
		userID := c.Param("userID")
		var request struct {
			SchemaVersion int     `json:"schema_version"`
			Role          *string `json:"role"`
			Status        *string `json:"status"`
		}
		if !ValidPathSegment(userID) || !decodeStrictManagementBody(c, &request) || request.SchemaVersion != 1 ||
			(request.Role == nil && request.Status == nil) {
			c.JSON(http.StatusBadRequest, gin.H{"error": ErrManagementInvalid.Error()})
			return
		}
		if request.Role != nil {
			role := strings.TrimSpace(*request.Role)
			request.Role = &role
		}
		if request.Status != nil {
			state := strings.TrimSpace(*request.Status)
			request.Status = &state
		}
		if err := management.UpdateLegacyAccount(c.Request.Context(), actor.claims.Sub, actor.claims.ManagementScopeVersions,
			actor.claims.AuthVersion, userID, request.Role, request.Status); err != nil {
			code := managementMutationErrorStatus(err)
			if errors.Is(err, ErrAccountSelfSuspension) {
				code = http.StatusConflict
			}
			c.JSON(code, gin.H{"error": managementMutationErrorMessage(err)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "status": "updated", "id": userID})
	}
}

func ManagementAccountDeleteHandler(cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy, validate OIDCCallerValidator,
	verifySession CLIAccessSessionVerifier, management *ManagementAuthority) gin.HandlerFunc {
	return func(c *gin.Context) {
		consumerNoStore(c)
		actor, statusCode, message := authenticateManagementActor(c, cfg, fs, policy, validate, verifySession, management)
		if statusCode != 0 {
			c.JSON(statusCode, gin.H{"error": message})
			return
		}
		if !containsString(actor.scopes, ManagementScopeAuthAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": ErrManagementDenied.Error()})
			return
		}
		userID := c.Param("userID")
		if !ValidPathSegment(userID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
			return
		}
		if err := management.DeleteAccount(c.Request.Context(), actor.claims.Sub, actor.claims.ManagementScopeVersions, actor.claims.AuthVersion, userID); err != nil {
			c.JSON(managementMutationErrorStatus(err), gin.H{"error": managementMutationErrorMessage(err)})
			return
		}
		c.JSON(http.StatusOK, gin.H{"schema_version": 1, "status": "deleted", "id": userID})
	}
}

func authenticateManagementActor(c *gin.Context, cfg config.Config, fs *firestore.Client, policy LWCConsumerPolicy,
	validate OIDCCallerValidator, verifySession CLIAccessSessionVerifier, management *ManagementAuthority) (*managementActor, int, string) {
	if !policy.ready() || fs == nil || management == nil || validate == nil {
		return nil, http.StatusServiceUnavailable, "consumer authority unavailable"
	}
	callerParts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
	if len(callerParts) != 2 || !strings.EqualFold(callerParts[0], "Bearer") ||
		!validLWCConsumerCaller(c.Request.Context(), policy, validate, strings.TrimSpace(callerParts[1])) {
		return nil, http.StatusForbidden, "untrusted consumer"
	}
	credential := strings.TrimSpace(c.GetHeader("X-LWC-User-Credential"))
	claims, err := ValidateToken(credential, cfg.JWTSecret)
	if err != nil || claims.TokenType != accessTokenType || !ValidPathSegment(claims.Sub) || claims.ExpiresAt == nil ||
		(len(claims.Audience) > 0 && !consumerAudienceContains(claims.Audience, policy.Resource)) ||
		(claims.Issuer != "" && claims.Issuer != policy.Issuer) {
		return nil, http.StatusUnauthorized, "invalid credential"
	}
	if claims.ClientKind == cliClientKind {
		if verifySession == nil {
			return nil, http.StatusServiceUnavailable, "session authority unavailable"
		}
		if err := verifySession(c.Request.Context(), claims.Sub, claims.SessionID, claims.AuthVersion); err != nil {
			if cliSessionHTTPStatus(err) == http.StatusServiceUnavailable {
				return nil, http.StatusServiceUnavailable, "session authority unavailable"
			}
			return nil, http.StatusUnauthorized, "invalid credential"
		}
	} else if claims.ClientKind != "" {
		return nil, http.StatusUnauthorized, "invalid credential"
	}
	user, err := FirestoreAccountLookup(fs)(c.Request.Context(), claims.Sub)
	if err != nil {
		return nil, http.StatusServiceUnavailable, "account authority unavailable"
	}
	if !user.AllowsVersion(claims.AuthVersion) {
		return nil, http.StatusUnauthorized, "invalid credential"
	}
	scopes, err := management.EffectiveScopes(c.Request.Context(), claims.Sub, claims.ManagementScopeVersions)
	if err != nil {
		return nil, http.StatusServiceUnavailable, "management authority unavailable"
	}
	return &managementActor{claims: claims, user: user, scopes: scopes}, 0, ""
}

func decodeStrictManagementBody(c *gin.Context, target any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}
