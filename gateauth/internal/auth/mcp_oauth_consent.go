package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type MCPOAuthConsentLookup struct {
	SchemaVersion   int                     `json:"schema_version"`
	Status          string                  `json:"status"`
	ResourceKind    string                  `json:"resource_kind"`
	TransactionID   string                  `json:"transaction_id"`
	Resource        string                  `json:"resource"`
	ProjectID       string                  `json:"project_id,omitempty"`
	RequestedScopes []string                `json:"requested_scopes,omitempty"`
	ExpiresAt       string                  `json:"expires_at,omitempty"`
	CanContinue     bool                    `json:"can_continue"`
	ClientName      string                  `json:"client_name,omitempty"`
	AccountEmail    string                  `json:"account_email,omitempty"`
	DecisionResult  *MCPOAuthDecisionResult `json:"decision_result,omitempty"`
}

type MCPOAuthDecisionResult struct {
	RedirectURL     string `json:"redirect_url"`
	RequiresConsent bool   `json:"requires_consent"`
}

type MCPOAuthPublicClassification struct {
	SchemaVersion int    `json:"schema_version"`
	ResourceKind  string `json:"resource_kind"`
	TransactionID string `json:"transaction_id"`
	Resource      string `json:"resource"`
}

type MCPOAuthTrustedDecision struct {
	SchemaVersion  int      `json:"schema_version"`
	TransactionID  string   `json:"transaction_id"`
	Subject        string   `json:"subject"`
	AuthVersion    int64    `json:"auth_version"`
	Decision       string   `json:"decision"`
	ApprovedScopes []string `json:"approved_scopes"`
}

func (s *MCPOAuthService) LookupConsentTransaction(ctx context.Context, subject string, authVersion int64, transactionID string) (MCPOAuthConsentLookup, error) {
	if s == nil || s.fs == nil || !ValidPathSegment(subject) || authVersion < 0 || !validMCPOpaque(transactionID, "lwc_mcp_tx_") {
		return MCPOAuthConsentLookup{}, ErrMCPOAuthInvalid
	}
	txHash := mcpOAuthHash(transactionID)
	txRef := s.collection("transactions").Doc(txHash)
	var request mcpOAuthTransactionRecord
	var user UserRecord
	var response MCPOAuthConsentLookup
	err := s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(txRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrMCPOAuthNotFound
			}
			return ErrMCPOAuthUnavailable
		}
		if mcpOAuthDecode(snapshot.Data(), &request) != nil || request.SchemaVersion != mcpOAuthRecordSchema ||
			request.Environment != s.environment || request.TransactionIDHash != txHash ||
			!s.validStoredResource(request.Resource, request.ProjectID, request.Scope, false) {
			return ErrMCPOAuthUnavailable
		}
		user, err = readMCPAccountInTransaction(ctx, tx, s.fs, subject)
		if err != nil {
			return err
		}
		if !user.AllowsVersion(authVersion) {
			return ErrAccountInactive
		}
		if request.UserID != "" && (request.UserID != subject || request.AuthVersion != authVersion) {
			return ErrMCPOAuthDenied
		}
		if request.Status == "approved" || request.Status == "denied" {
			if request.UserID != subject || request.AuthVersion != authVersion || request.DecisionFingerprint == "" || request.DecisionResultCipher == "" {
				return ErrMCPOAuthDenied
			}
			result, err := s.decryptConsentResult(txHash, request.DecisionResultCipher)
			if err != nil {
				return ErrMCPOAuthUnavailable
			}
			resourceKind := "fixed"
			var requestedScopes []string
			if policy, _, ok := s.policyForResource(request.Resource); ok && policy.Kind == "project-template" {
				resourceKind = "project-template"
				_, requestedScopes, _ = normalizeScopeForPolicy(request.Scope, policy)
			}
			response = MCPOAuthConsentLookup{SchemaVersion: 1, Status: "completed", ResourceKind: resourceKind,
				TransactionID: transactionID, Resource: request.Resource, ProjectID: request.ProjectID, RequestedScopes: requestedScopes,
				DecisionResult: &MCPOAuthDecisionResult{RedirectURL: result.RedirectURL, RequiresConsent: false}}
			return nil
		}
		if request.Status != "pending" {
			return ErrMCPOAuthDenied
		}
		if !s.now().Before(request.ExpiresAt) {
			return ErrMCPOAuthExpired
		}
		if request.UserID == "" {
			if err := tx.Update(txRef, []firestore.Update{{Path: "user_id", Value: subject}, {Path: "auth_version", Value: authVersion}}); err != nil {
				return ErrMCPOAuthUnavailable
			}
			request.UserID, request.AuthVersion = subject, authVersion
		}
		return nil
	})
	if err != nil {
		return MCPOAuthConsentLookup{}, normalizeMCPFirestoreError(err)
	}
	policy, _, ok := s.policyForResource(request.Resource)
	if !ok || !policy.Enabled || (policy.Kind == "project-template" && request.ProjectID == "") {
		return MCPOAuthConsentLookup{}, ErrMCPOAuthNotFound
	}
	resourceKind := "fixed"
	if policy.Kind == "project-template" {
		resourceKind = "project-template"
	}
	if response.Status == "completed" {
		return response, nil
	}
	client, err := s.readClient(ctx, request.ClientID)
	if err != nil {
		return MCPOAuthConsentLookup{}, err
	}
	_, scopes, valid := normalizeScopeForPolicy(request.Scope, policy)
	if !valid {
		return MCPOAuthConsentLookup{}, ErrMCPOAuthUnavailable
	}
	reusable, err := s.hasReusableGrant(ctx, nil, request, authVersion, s.now())
	if err != nil {
		return MCPOAuthConsentLookup{}, err
	}
	response = MCPOAuthConsentLookup{SchemaVersion: 1, Status: "pending", ResourceKind: resourceKind,
		TransactionID: transactionID, Resource: request.Resource, ProjectID: request.ProjectID,
		RequestedScopes: scopes, ExpiresAt: request.ExpiresAt.UTC().Format(time.RFC3339),
		CanContinue: reusable != nil, ClientName: client.ClientName, AccountEmail: user.Email}
	if strings.TrimSpace(response.ClientName) == "" {
		response.ClientName = "Unnamed MCP client"
	}
	return response, nil
}

func (s *MCPOAuthService) PublicConsentClassification(ctx context.Context, subject string, authVersion int64, transactionID string) (MCPOAuthPublicClassification, error) {
	if s == nil || s.fs == nil || !ValidPathSegment(subject) || !validMCPOpaque(transactionID, "lwc_mcp_tx_") {
		return MCPOAuthPublicClassification{}, ErrMCPOAuthInvalid
	}
	txHash := mcpOAuthHash(transactionID)
	snapshot, err := s.collection("transactions").Doc(txHash).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return MCPOAuthPublicClassification{}, ErrMCPOAuthNotFound
		}
		return MCPOAuthPublicClassification{}, ErrMCPOAuthUnavailable
	}
	var request mcpOAuthTransactionRecord
	if mcpOAuthDecode(snapshot.Data(), &request) != nil || request.SchemaVersion != mcpOAuthRecordSchema ||
		request.Environment != s.environment || request.TransactionIDHash != txHash {
		return MCPOAuthPublicClassification{}, ErrMCPOAuthUnavailable
	}
	policy, _, ok := s.policyForResource(request.Resource)
	if !ok || !s.validStoredResource(request.Resource, request.ProjectID, request.Scope, false) {
		return MCPOAuthPublicClassification{}, ErrMCPOAuthUnavailable
	}
	resourceKind := "fixed"
	if policy.Kind != "project-template" {
		return MCPOAuthPublicClassification{SchemaVersion: 1, ResourceKind: resourceKind, TransactionID: transactionID, Resource: request.Resource}, nil
	}
	lookup, err := s.LookupConsentTransaction(ctx, subject, authVersion, transactionID)
	if err != nil {
		return MCPOAuthPublicClassification{}, err
	}
	return MCPOAuthPublicClassification{SchemaVersion: 1, ResourceKind: lookup.ResourceKind,
		TransactionID: transactionID, Resource: lookup.Resource}, nil
}

func (s *MCPOAuthService) DecideConsentTrusted(ctx context.Context, request MCPOAuthTrustedDecision) (MCPOAuthDecisionResult, error) {
	if s == nil || s.fs == nil || request.SchemaVersion != 1 || !ValidPathSegment(request.Subject) || request.AuthVersion < 0 ||
		!validMCPOpaque(request.TransactionID, "lwc_mcp_tx_") {
		return MCPOAuthDecisionResult{}, ErrMCPOAuthInvalid
	}
	if request.Decision != "allow" && request.Decision != "deny" && request.Decision != "continue" {
		return MCPOAuthDecisionResult{}, ErrMCPOAuthBadRequest
	}
	txHash := mcpOAuthHash(request.TransactionID)
	fingerprint, err := consentDecisionFingerprint(request.Decision, request.ApprovedScopes)
	if err != nil {
		return MCPOAuthDecisionResult{}, ErrMCPOAuthBadRequest
	}
	code, err := generateMCPOpaque(mcpOAuthCodePrefix)
	if err != nil {
		return MCPOAuthDecisionResult{}, ErrMCPOAuthUnavailable
	}
	codeHash := mcpOAuthHash(code)
	now := s.now()
	txRef := s.collection("transactions").Doc(txHash)
	var result MCPOAuthDecisionResult
	expired := false
	err = s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = MCPOAuthDecisionResult{}
		snapshot, err := tx.Get(txRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrMCPOAuthNotFound
			}
			return ErrMCPOAuthUnavailable
		}
		var transaction mcpOAuthTransactionRecord
		if mcpOAuthDecode(snapshot.Data(), &transaction) != nil || transaction.SchemaVersion != mcpOAuthRecordSchema ||
			transaction.Environment != s.environment || transaction.TransactionIDHash != txHash ||
			!s.validStoredResource(transaction.Resource, transaction.ProjectID, transaction.Scope, false) {
			return ErrMCPOAuthUnavailable
		}
		if transaction.Status == "approved" || transaction.Status == "denied" {
			if transaction.UserID != request.Subject || transaction.AuthVersion != request.AuthVersion {
				return ErrMCPOAuthDenied
			}
			if transaction.DecisionFingerprint != fingerprint || transaction.DecisionResultCipher == "" {
				return ErrMCPOAuthConflict
			}
			stored, err := s.decryptConsentResult(txHash, transaction.DecisionResultCipher)
			if err != nil {
				return ErrMCPOAuthUnavailable
			}
			result = MCPOAuthDecisionResult{RedirectURL: stored.RedirectURL, RequiresConsent: false}
			return nil
		}
		if transaction.Status != "pending" {
			return ErrMCPOAuthDenied
		}
		if !now.Before(transaction.ExpiresAt) {
			if err := tx.Update(txRef, []firestore.Update{{Path: "status", Value: "expired"}}); err != nil {
				return ErrMCPOAuthUnavailable
			}
			expired = true
			return nil
		}
		if transaction.UserID != request.Subject || transaction.AuthVersion != request.AuthVersion {
			return ErrMCPOAuthDenied
		}
		policy, actualProjectID, valid := s.policyForResource(transaction.Resource)
		if !valid || !policy.Enabled || policy.Kind != "project-template" || actualProjectID != transaction.ProjectID ||
			!s.validStoredResource(transaction.Resource, transaction.ProjectID, transaction.Scope, true) {
			return ErrMCPOAuthDenied
		}
		requestedScope, requestedScopes, valid := normalizeScopeForPolicy(transaction.Scope, policy)
		if !valid || requestedScope != transaction.Scope {
			return ErrMCPOAuthDenied
		}
		if request.Decision == "deny" {
			if len(request.ApprovedScopes) != 0 {
				return ErrMCPOAuthBadRequest
			}
			result.RedirectURL = oauthRedirectWithError(transaction.RedirectURI, "access_denied", transaction.State)
			return s.commitConsentResult(tx, txRef, txHash, fingerprint, "denied", "", result)
		}
		if !sameStringSlice(request.ApprovedScopes, requestedScopes) {
			return ErrMCPOAuthDenied
		}
		clientRef := s.collection("clients").Doc(transaction.ClientID)
		clientSnapshot, err := tx.Get(clientRef)
		if err != nil {
			return ErrMCPOAuthUnavailable
		}
		var client mcpOAuthClientRecord
		if mcpOAuthDecode(clientSnapshot.Data(), &client) != nil || client.SchemaVersion != mcpOAuthRecordSchema ||
			client.Environment != s.environment || client.ClientID != transaction.ClientID ||
			!containsString(client.RedirectURIs, transaction.RedirectURI) {
			return ErrMCPOAuthUnavailable
		}
		var user UserRecord
		user, err = readMCPAccountInTransaction(ctx, tx, s.fs, request.Subject)
		if err != nil {
			return err
		}
		if !user.AllowsVersion(request.AuthVersion) {
			return ErrAccountInactive
		}
		sourceGrantID := ""
		if request.Decision == "continue" {
			reusable, err := s.hasReusableGrant(ctx, tx, transaction, request.AuthVersion, now)
			if err != nil {
				return err
			}
			if reusable == nil {
				result = MCPOAuthDecisionResult{RequiresConsent: true}
				return nil
			}
			sourceGrantID = reusable.GrantID
		}
		if !validMCPCodeChallenge(transaction.CodeChallenge) {
			return ErrMCPOAuthDenied
		}
		codeRecord := mcpOAuthCodeRecord{SchemaVersion: mcpOAuthRecordSchema, Environment: s.environment,
			CodeHash: codeHash, ClientID: transaction.ClientID, RedirectURI: transaction.RedirectURI,
			CodeChallenge: transaction.CodeChallenge, Resource: transaction.Resource, ProjectID: transaction.ProjectID,
			Scope: transaction.Scope, UserID: request.Subject, AuthVersion: request.AuthVersion, SourceGrantID: sourceGrantID,
			Status: "active", CreatedAt: now, ExpiresAt: now.Add(mcpOAuthCodeTTL)}
		if err := tx.Create(s.collection("codes").Doc(codeHash), codeRecord); err != nil {
			return ErrMCPOAuthUnavailable
		}
		result.RedirectURL = oauthRedirectWithCode(transaction.RedirectURI, code, transaction.State)
		return s.commitConsentResult(tx, txRef, txHash, fingerprint, "approved", codeHash, result)
	})
	if err != nil {
		return MCPOAuthDecisionResult{}, normalizeMCPFirestoreError(err)
	}
	if expired {
		return MCPOAuthDecisionResult{}, ErrMCPOAuthExpired
	}
	return result, nil
}

func (s *MCPOAuthService) commitConsentResult(tx *firestore.Transaction, txRef *firestore.DocumentRef,
	txHash, fingerprint, statusValue, codeHash string,
	result MCPOAuthDecisionResult) error {
	ciphertext, err := s.encryptConsentResult(txHash, result)
	if err != nil {
		return ErrMCPOAuthUnavailable
	}
	updates := []firestore.Update{{Path: "status", Value: statusValue},
		{Path: "decision_fingerprint", Value: fingerprint}, {Path: "decision_result_cipher", Value: ciphertext}}
	if codeHash != "" {
		updates = append(updates, firestore.Update{Path: "authorization_code_hash", Value: codeHash})
	}
	return tx.Update(txRef, updates)
}

func consentDecisionFingerprint(decision string, scopes []string) (string, error) {
	canonical := append([]string(nil), scopes...)
	encoded, err := json.Marshal(struct {
		Decision string   `json:"decision"`
		Scopes   []string `json:"approved_scopes"`
	}{decision, canonical})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func (s *MCPOAuthService) encryptConsentResult(txHash string, result MCPOAuthDecisionResult) (string, error) {
	if s == nil || s.jwtSecret == "" || s.environment == "" || len(txHash) != sha256.Size*2 {
		return "", ErrMCPOAuthUnavailable
	}
	plain, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte("lwc-mcp-consent-result\x00" + s.environment + "\x00" + s.jwtSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plain, []byte(txHash))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *MCPOAuthService) decryptConsentResult(txHash, encoded string) (MCPOAuthDecisionResult, error) {
	var result MCPOAuthDecisionResult
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return result, err
	}
	key := sha256.Sum256([]byte("lwc-mcp-consent-result\x00" + s.environment + "\x00" + s.jwtSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return result, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(sealed) < gcm.NonceSize() {
		return result, ErrMCPOAuthInvalid
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(txHash))
	if err != nil || json.Unmarshal(plain, &result) != nil || result.RequiresConsent || result.RedirectURL == "" ||
		!validMCPDecisionRedirect(result.RedirectURL) {
		return MCPOAuthDecisionResult{}, ErrMCPOAuthInvalid
	}
	return result, nil
}

func validMCPDecisionRedirect(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery == "" {
		return false
	}
	return validMCPOAuthRedirectURI(parsed.Scheme + "://" + parsed.Host + parsed.Path)
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
