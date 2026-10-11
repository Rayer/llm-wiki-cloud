package auth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	mcpOAuthScope         = "mcp:query"
	mcpOAuthCodePrefix    = "lwc_mcp_code_"
	mcpOAuthAccessPrefix  = "lwc_mcp_at_"
	mcpOAuthRefreshPrefix = "lwc_mcp_rt_"
	mcpOAuthClientPrefix  = "lwc_mcp_client_"
	mcpOAuthAuthTTL       = 10 * time.Minute
	mcpOAuthCodeTTL       = 5 * time.Minute
	mcpOAuthAccessTTL     = 10 * time.Minute
	mcpOAuthFamilyTTL     = 30 * 24 * time.Hour
	mcpOAuthRetryWindow   = 30 * time.Second
	mcpOAuthSecretBytes   = 32
	mcpOAuthRecordSchema  = 1
)

var (
	ErrMCPOAuthInvalid     = errors.New("invalid MCP OAuth credential")
	ErrMCPOAuthDenied      = errors.New("MCP OAuth access denied")
	ErrMCPOAuthUnavailable = errors.New("MCP OAuth authority unavailable")
	ErrMCPOAuthNotFound    = errors.New("MCP OAuth record not found")
	ErrMCPOAuthExpired     = errors.New("MCP OAuth request expired")
	ErrMCPOAuthReplay      = errors.New("MCP OAuth credential replayed")
	ErrMCPOAuthBadRequest  = errors.New("invalid MCP OAuth request")
	ErrMCPOAuthUnsupported = errors.New("unsupported MCP OAuth request")
)

type MCPOAuthService struct {
	fs             *firestore.Client
	environment    string
	issuer         string
	resourceOrigin string
	frontendOrigin string
	jwtSecret      string
	lookup         AccountLookup
	authorize      ProjectOwnerAuthorizer
	now            func() time.Time
}

type MCPGrantAuthority struct {
	fs             *firestore.Client
	environment    string
	resourceOrigin string
	lookup         AccountLookup
	authorize      ProjectOwnerAuthorizer
	now            func() time.Time
}

type MCPOAuthIdentity struct {
	UserID    string
	ProjectID string
	GrantID   string
	ClientID  string
	Resource  string
	Scope     string
}

type MCPOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

type MCPOAuthConsentDetails struct {
	TransactionID string `json:"transaction_id"`
	AccountEmail  string `json:"account_email"`
	ProjectID     string `json:"project_id"`
	ProjectName   string `json:"project_name"`
	ClientName    string `json:"client_name"`
	Resource      string `json:"resource"`
	Scope         string `json:"scope"`
	ExpiresAt     string `json:"expires_at"`
}

type MCPOAuthGrantInfo struct {
	ID         string     `json:"id"`
	ClientName string     `json:"client_name"`
	ProjectID  string     `json:"project_id"`
	Resource   string     `json:"resource"`
	Scope      string     `json:"scope"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type mcpOAuthClientRecord struct {
	SchemaVersion int       `firestore:"schema_version"`
	Environment   string    `firestore:"environment"`
	ClientID      string    `firestore:"client_id"`
	ClientName    string    `firestore:"client_name"`
	RedirectURIs  []string  `firestore:"redirect_uris"`
	CreatedAt     time.Time `firestore:"created_at"`
}

type mcpOAuthTransactionRecord struct {
	SchemaVersion         int       `firestore:"schema_version"`
	Environment           string    `firestore:"environment"`
	TransactionIDHash     string    `firestore:"transaction_id_hash"`
	ClientID              string    `firestore:"client_id"`
	RedirectURI           string    `firestore:"redirect_uri"`
	CodeChallenge         string    `firestore:"code_challenge"`
	State                 string    `firestore:"state"`
	Resource              string    `firestore:"resource"`
	ProjectID             string    `firestore:"project_id"`
	Scope                 string    `firestore:"scope"`
	Status                string    `firestore:"status"`
	UserID                string    `firestore:"user_id,omitempty"`
	AuthVersion           int64     `firestore:"auth_version,omitempty"`
	CreatedAt             time.Time `firestore:"created_at"`
	ExpiresAt             time.Time `firestore:"expires_at"`
	AuthorizationCodeHash string    `firestore:"authorization_code_hash,omitempty"`
}

type mcpOAuthCodeRecord struct {
	SchemaVersion int       `firestore:"schema_version"`
	Environment   string    `firestore:"environment"`
	CodeHash      string    `firestore:"code_hash"`
	ClientID      string    `firestore:"client_id"`
	RedirectURI   string    `firestore:"redirect_uri"`
	CodeChallenge string    `firestore:"code_challenge"`
	Resource      string    `firestore:"resource"`
	ProjectID     string    `firestore:"project_id"`
	Scope         string    `firestore:"scope"`
	UserID        string    `firestore:"user_id"`
	AuthVersion   int64     `firestore:"auth_version"`
	Status        string    `firestore:"status"`
	CreatedAt     time.Time `firestore:"created_at"`
	ExpiresAt     time.Time `firestore:"expires_at"`
	RedeemedAt    time.Time `firestore:"redeemed_at,omitempty"`
}

type mcpOAuthGrantRecord struct {
	SchemaVersion      int       `firestore:"schema_version"`
	Environment        string    `firestore:"environment"`
	GrantID            string    `firestore:"grant_id"`
	UserID             string    `firestore:"user_id"`
	AuthVersion        int64     `firestore:"auth_version"`
	ProjectID          string    `firestore:"project_id"`
	ClientID           string    `firestore:"client_id"`
	Resource           string    `firestore:"resource"`
	Scope              string    `firestore:"scope"`
	Status             string    `firestore:"status"`
	CurrentRefreshHash string    `firestore:"current_refresh_hash"`
	CreatedAt          time.Time `firestore:"created_at"`
	ExpiresAt          time.Time `firestore:"expires_at"`
	RevokedAt          time.Time `firestore:"revoked_at,omitempty"`
}

type mcpOAuthAccessRecord struct {
	SchemaVersion int       `firestore:"schema_version"`
	Environment   string    `firestore:"environment"`
	TokenHash     string    `firestore:"token_hash"`
	GrantID       string    `firestore:"grant_id"`
	ExpiresAt     time.Time `firestore:"expires_at"`
	CreatedAt     time.Time `firestore:"created_at"`
}

type mcpOAuthRefreshRecord struct {
	SchemaVersion       int       `firestore:"schema_version"`
	Environment         string    `firestore:"environment"`
	TokenHash           string    `firestore:"token_hash"`
	GrantID             string    `firestore:"grant_id"`
	Generation          int64     `firestore:"generation"`
	Status              string    `firestore:"status"`
	CreatedAt           time.Time `firestore:"created_at"`
	ExpiresAt           time.Time `firestore:"expires_at"`
	RotatedToHash       string    `firestore:"rotated_to_hash,omitempty"`
	RetryUntil          time.Time `firestore:"retry_until,omitempty"`
	RetryMaterialCipher string    `firestore:"retry_material_cipher,omitempty"`
}

type mcpOAuthRefreshResult struct {
	TokenResponse MCPOAuthTokenResponse `json:"token_response"`
	ExpiresAt     time.Time             `json:"expires_at"`
}

type mcpOAuthDecision struct {
	RedirectURL string
}

func NewMCPOAuthService(fs *firestore.Client, environment, issuer, resourceOrigin, frontendOrigin, jwtSecret string) *MCPOAuthService {
	return &MCPOAuthService{
		fs: fs, environment: strings.TrimSpace(environment), issuer: trimOrigin(issuer),
		resourceOrigin: trimOrigin(resourceOrigin), frontendOrigin: trimOrigin(frontendOrigin),
		jwtSecret: jwtSecret, lookup: FirestoreAccountLookup(fs),
		authorize: FirestoreProjectOwnerAuthorizer(fs),
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func NewMCPGrantAuthority(fs *firestore.Client, environment, resourceOrigin string) *MCPGrantAuthority {
	return &MCPGrantAuthority{
		fs: fs, environment: strings.TrimSpace(environment), resourceOrigin: trimOrigin(resourceOrigin),
		lookup: FirestoreAccountLookup(fs), authorize: FirestoreProjectOwnerAuthorizer(fs),
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *MCPOAuthService) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

func (a *MCPGrantAuthority) SetClock(now func() time.Time) {
	if a != nil && now != nil {
		a.now = now
	}
}

func (s *MCPOAuthService) ResourceURL(projectID string) string {
	if !ValidPathSegment(projectID) || s == nil || s.resourceOrigin == "" {
		return ""
	}
	return s.resourceOrigin + "/mcp/projects/" + projectID
}

func (s *MCPOAuthService) ResourceMetadataURL(projectID string) string {
	if s.ResourceURL(projectID) == "" {
		return ""
	}
	return s.resourceOrigin + "/.well-known/oauth-protected-resource/mcp/projects/" + projectID
}

func (s *MCPOAuthService) collection(name string) *firestore.CollectionRef {
	if s == nil || s.fs == nil {
		return nil
	}
	return mcpOAuthCollection(s.fs, s.environment, name)
}

func (a *MCPGrantAuthority) collection(name string) *firestore.CollectionRef {
	if a == nil || a.fs == nil {
		return nil
	}
	return mcpOAuthCollection(a.fs, a.environment, name)
}

func mcpOAuthCollection(fs *firestore.Client, environment, name string) *firestore.CollectionRef {
	sum := sha256.Sum256([]byte(environment))
	return scopedfirestore.Collection(fs, "mcp_oauth_"+name+"_"+hex.EncodeToString(sum[:6]))
}

func trimOrigin(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (s *MCPOAuthService) validResource(resource string) (string, bool) {
	if s == nil || s.resourceOrigin == "" || !strings.HasPrefix(resource, s.resourceOrigin+"/mcp/projects/") {
		return "", false
	}
	parsed, err := url.Parse(resource)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	const prefix = "/mcp/projects/"
	if !strings.HasPrefix(parsed.EscapedPath(), prefix) {
		return "", false
	}
	projectID := strings.TrimPrefix(parsed.EscapedPath(), prefix)
	if !ValidPathSegment(projectID) || strings.Contains(projectID, "/") || s.ResourceURL(projectID) != resource {
		return "", false
	}
	return projectID, true
}

func validMCPOAuthRedirectURI(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00") || strings.Contains(raw, "*") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	switch parsed.Scheme {
	case "http":
		host := strings.ToLower(parsed.Hostname())
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return false
		}
		port := parsed.Port()
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 || parsed.RawPath != "" {
			return false
		}
		return parsed.Path == "" || strings.HasPrefix(parsed.Path, "/")
	case "https":
		return parsed.Hostname() != "" && parsed.RawPath == "" && !strings.Contains(parsed.Path, "/../")
	default:
		return false
	}
}

func validMCPCodeChallenge(challenge string) bool {
	if len(challenge) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(decoded) == sha256.Size && base64.RawURLEncoding.EncodeToString(decoded) == challenge
}

func verifyMCPCodeVerifier(challenge, verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, char := range verifier {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~", char)) {
			return false
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(expected), []byte(challenge)) == 1
}

func generateMCPOpaque(prefix string) (string, error) {
	raw := make([]byte, mcpOAuthSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func validMCPOpaque(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, prefix)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(raw) == mcpOAuthSecretBytes && base64.RawURLEncoding.EncodeToString(raw) == encoded
}

func mcpOAuthHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mcpOAuthID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func mcpOAuthDecode(data map[string]interface{}, target interface{}) error {
	targetValue := reflect.ValueOf(target)
	if targetValue.Kind() != reflect.Pointer || targetValue.IsNil() || targetValue.Elem().Kind() != reflect.Struct {
		return ErrMCPOAuthUnavailable
	}
	typeOfTarget := targetValue.Elem().Type()
	fields := make(map[string]interface{}, len(data))
	known := make(map[string]struct{}, typeOfTarget.NumField())
	for index := 0; index < typeOfTarget.NumField(); index++ {
		field := typeOfTarget.Field(index)
		tag := strings.Split(field.Tag.Get("firestore"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		known[tag] = struct{}{}
		if value, exists := data[tag]; exists {
			fields[field.Name] = value
		}
	}
	for key := range data {
		if _, ok := known[key]; !ok {
			return ErrMCPOAuthUnavailable
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (s *MCPOAuthService) RegisterClient(ctx context.Context, name string, redirectURIs []string) (mcpOAuthClientRecord, error) {
	if s == nil || s.fs == nil || s.environment == "" || len(redirectURIs) == 0 || len(redirectURIs) > 12 {
		return mcpOAuthClientRecord{}, ErrMCPOAuthUnavailable
	}
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 128 || strings.ContainsAny(name, "\r\n\x00") {
		return mcpOAuthClientRecord{}, ErrMCPOAuthBadRequest
	}
	seen := make(map[string]struct{}, len(redirectURIs))
	uris := make([]string, 0, len(redirectURIs))
	for _, raw := range redirectURIs {
		if !validMCPOAuthRedirectURI(raw) {
			return mcpOAuthClientRecord{}, ErrMCPOAuthBadRequest
		}
		if _, exists := seen[raw]; exists {
			return mcpOAuthClientRecord{}, ErrMCPOAuthBadRequest
		}
		seen[raw] = struct{}{}
		uris = append(uris, raw)
	}
	clientID, err := generateMCPOpaque(mcpOAuthClientPrefix)
	if err != nil {
		return mcpOAuthClientRecord{}, ErrMCPOAuthUnavailable
	}
	record := mcpOAuthClientRecord{
		SchemaVersion: mcpOAuthRecordSchema, Environment: s.environment,
		ClientID: clientID, ClientName: name, RedirectURIs: uris, CreatedAt: s.now(),
	}
	if _, err := s.collection("clients").Doc(clientID).Create(ctx, record); err != nil {
		return mcpOAuthClientRecord{}, ErrMCPOAuthUnavailable
	}
	return record, nil
}

func (s *MCPOAuthService) readClient(ctx context.Context, clientID string) (mcpOAuthClientRecord, error) {
	if !validMCPOpaque(clientID, mcpOAuthClientPrefix) || s == nil || s.fs == nil {
		return mcpOAuthClientRecord{}, ErrMCPOAuthInvalid
	}
	snapshot, err := s.collection("clients").Doc(clientID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return mcpOAuthClientRecord{}, ErrMCPOAuthInvalid
		}
		return mcpOAuthClientRecord{}, ErrMCPOAuthUnavailable
	}
	var record mcpOAuthClientRecord
	if mcpOAuthDecode(snapshot.Data(), &record) != nil || record.SchemaVersion != mcpOAuthRecordSchema ||
		record.Environment != s.environment || record.ClientID != clientID || len(record.RedirectURIs) == 0 ||
		record.CreatedAt.IsZero() {
		return mcpOAuthClientRecord{}, ErrMCPOAuthUnavailable
	}
	for _, uri := range record.RedirectURIs {
		if !validMCPOAuthRedirectURI(uri) {
			return mcpOAuthClientRecord{}, ErrMCPOAuthUnavailable
		}
	}
	return record, nil
}

func (s *MCPOAuthService) BeginAuthorization(ctx context.Context, clientID, redirectURI, responseType, challenge, challengeMethod, state, resource, scope string) (string, error) {
	if s == nil || s.fs == nil || s.frontendOrigin == "" {
		return "", ErrMCPOAuthUnavailable
	}
	client, err := s.readClient(ctx, clientID)
	if err != nil {
		return "", err
	}
	if responseType != "code" || !containsString(client.RedirectURIs, redirectURI) ||
		!validMCPOAuthRedirectURI(redirectURI) || challengeMethod != "S256" ||
		!validMCPCodeChallenge(challenge) || state == "" || len(state) > 1024 ||
		strings.ContainsAny(state, "\r\n\x00") {
		return "", ErrMCPOAuthBadRequest
	}
	projectID, validResource := s.validResource(resource)
	if !validResource {
		return "", ErrMCPOAuthDenied
	}
	if scope == "" {
		scope = mcpOAuthScope
	}
	if scope != mcpOAuthScope {
		return "", ErrMCPOAuthUnsupported
	}
	transactionID, err := generateMCPOpaque("lwc_mcp_tx_")
	if err != nil {
		return "", ErrMCPOAuthUnavailable
	}
	now := s.now()
	record := mcpOAuthTransactionRecord{
		SchemaVersion: mcpOAuthRecordSchema, Environment: s.environment,
		TransactionIDHash: mcpOAuthHash(transactionID), ClientID: clientID,
		RedirectURI: redirectURI, CodeChallenge: challenge, State: state,
		Resource: resource, ProjectID: projectID, Scope: scope, Status: "pending",
		CreatedAt: now, ExpiresAt: now.Add(mcpOAuthAuthTTL),
	}
	if _, err := s.collection("transactions").Doc(record.TransactionIDHash).Create(ctx, record); err != nil {
		return "", ErrMCPOAuthUnavailable
	}
	return s.frontendOrigin + "/oauth/consent?transaction_id=" + url.QueryEscape(transactionID), nil
}

func (s *MCPOAuthService) ConsentDetails(ctx context.Context, userID, transactionID string) (MCPOAuthConsentDetails, error) {
	if !validMCPOpaque(transactionID, "lwc_mcp_tx_") || !ValidPathSegment(userID) || s == nil || s.fs == nil {
		return MCPOAuthConsentDetails{}, ErrMCPOAuthInvalid
	}
	txHash := mcpOAuthHash(transactionID)
	snapshot, err := s.collection("transactions").Doc(txHash).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return MCPOAuthConsentDetails{}, ErrMCPOAuthNotFound
		}
		return MCPOAuthConsentDetails{}, ErrMCPOAuthUnavailable
	}
	var tx mcpOAuthTransactionRecord
	if mcpOAuthDecode(snapshot.Data(), &tx) != nil || tx.SchemaVersion != mcpOAuthRecordSchema ||
		tx.Environment != s.environment || tx.TransactionIDHash != txHash {
		return MCPOAuthConsentDetails{}, ErrMCPOAuthUnavailable
	}
	if tx.Status != "pending" {
		return MCPOAuthConsentDetails{}, ErrMCPOAuthDenied
	}
	if !s.now().Before(tx.ExpiresAt) {
		return MCPOAuthConsentDetails{}, ErrMCPOAuthExpired
	}
	user, err := s.lookup(ctx, userID)
	if err != nil {
		return MCPOAuthConsentDetails{}, ErrMCPOAuthUnavailable
	}
	if user == nil || !user.Active() {
		return MCPOAuthConsentDetails{}, ErrAccountInactive
	}
	if err := s.requireProjectOwner(ctx, userID, tx.ProjectID); err != nil {
		return MCPOAuthConsentDetails{}, err
	}
	client, err := s.readClient(ctx, tx.ClientID)
	if err != nil {
		return MCPOAuthConsentDetails{}, err
	}
	project, err := getOwnedProject(ctx, s.fs, userID, tx.ProjectID)
	if err != nil {
		return MCPOAuthConsentDetails{}, err
	}
	name := client.ClientName
	if name == "" {
		name = "Unnamed MCP client"
	}
	return MCPOAuthConsentDetails{
		TransactionID: transactionID, AccountEmail: user.Email,
		ProjectID: tx.ProjectID, ProjectName: project.Name, ClientName: name,
		Resource: tx.Resource, Scope: tx.Scope, ExpiresAt: tx.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}

func (s *MCPOAuthService) DecideConsent(ctx context.Context, userID string, authVersion int64, transactionID string, allow bool) (mcpOAuthDecision, error) {
	if !validMCPOpaque(transactionID, "lwc_mcp_tx_") || !ValidPathSegment(userID) || s == nil || s.fs == nil {
		return mcpOAuthDecision{}, ErrMCPOAuthInvalid
	}
	code, err := generateMCPOpaque(mcpOAuthCodePrefix)
	if err != nil {
		return mcpOAuthDecision{}, ErrMCPOAuthUnavailable
	}
	now := s.now()
	txHash := mcpOAuthHash(transactionID)
	txRef := s.collection("transactions").Doc(txHash)
	codeHash := mcpOAuthHash(code)
	var result mcpOAuthDecision
	expired := false
	err = s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		txSnapshot, err := tx.Get(txRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrMCPOAuthNotFound
			}
			return ErrMCPOAuthUnavailable
		}
		var request mcpOAuthTransactionRecord
		if mcpOAuthDecode(txSnapshot.Data(), &request) != nil ||
			request.SchemaVersion != mcpOAuthRecordSchema || request.Environment != s.environment ||
			request.TransactionIDHash != txHash {
			return ErrMCPOAuthUnavailable
		}
		if request.Status != "pending" {
			return ErrMCPOAuthDenied
		}
		if !now.Before(request.ExpiresAt) {
			if err := tx.Update(txRef, []firestore.Update{{Path: "status", Value: "expired"}}); err != nil {
				return ErrMCPOAuthUnavailable
			}
			expired = true
			return nil
		}
		clientRef := s.collection("clients").Doc(request.ClientID)
		clientSnapshot, err := tx.Get(clientRef)
		if err != nil {
			return ErrMCPOAuthUnavailable
		}
		var client mcpOAuthClientRecord
		if mcpOAuthDecode(clientSnapshot.Data(), &client) != nil ||
			client.SchemaVersion != mcpOAuthRecordSchema || client.Environment != s.environment ||
			client.ClientID != request.ClientID || !containsString(client.RedirectURIs, request.RedirectURI) {
			return ErrMCPOAuthUnavailable
		}
		user, project, err := readMCPAccountAndProjectInTransaction(ctx, tx, s.fs, userID, request.ProjectID)
		if err != nil {
			return err
		}
		if !user.AllowsVersion(authVersion) {
			return ErrAccountInactive
		}
		if !allow {
			if err := tx.Update(txRef, []firestore.Update{
				{Path: "status", Value: "denied"},
				{Path: "user_id", Value: userID},
				{Path: "auth_version", Value: authVersion},
			}); err != nil {
				return ErrMCPOAuthUnavailable
			}
			result.RedirectURL = oauthRedirectWithError(request.RedirectURI, "access_denied", request.State)
			return nil
		}
		if project.ID != request.ProjectID || request.Resource != s.ResourceURL(project.ID) ||
			request.Scope != mcpOAuthScope || !validMCPCodeChallenge(request.CodeChallenge) {
			return ErrMCPOAuthDenied
		}
		codeRecord := mcpOAuthCodeRecord{
			SchemaVersion: mcpOAuthRecordSchema, Environment: s.environment,
			CodeHash: codeHash, ClientID: request.ClientID, RedirectURI: request.RedirectURI,
			CodeChallenge: request.CodeChallenge, Resource: request.Resource, ProjectID: request.ProjectID,
			Scope: request.Scope, UserID: userID, AuthVersion: authVersion,
			Status: "active", CreatedAt: now, ExpiresAt: now.Add(mcpOAuthCodeTTL),
		}
		if err := tx.Create(s.collection("codes").Doc(codeHash), codeRecord); err != nil {
			return ErrMCPOAuthUnavailable
		}
		if err := tx.Update(txRef, []firestore.Update{
			{Path: "status", Value: "approved"},
			{Path: "user_id", Value: userID},
			{Path: "auth_version", Value: authVersion},
			{Path: "authorization_code_hash", Value: codeHash},
		}); err != nil {
			return ErrMCPOAuthUnavailable
		}
		result.RedirectURL = oauthRedirectWithCode(request.RedirectURI, code, request.State)
		return nil
	})
	if err != nil {
		return mcpOAuthDecision{}, normalizeMCPFirestoreError(err)
	}
	if expired {
		return mcpOAuthDecision{}, ErrMCPOAuthExpired
	}
	return result, nil
}

func (s *MCPOAuthService) ExchangeCode(ctx context.Context, clientID, code, redirectURI, verifier, resource string) (MCPOAuthTokenResponse, error) {
	if s == nil || s.fs == nil || !validMCPOpaque(code, mcpOAuthCodePrefix) {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthInvalid
	}
	codeHash := mcpOAuthHash(code)
	codeRef := s.collection("codes").Doc(codeHash)
	snapshot, err := codeRef.Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return MCPOAuthTokenResponse{}, ErrMCPOAuthInvalid
		}
		return MCPOAuthTokenResponse{}, ErrMCPOAuthUnavailable
	}
	var initial mcpOAuthCodeRecord
	if mcpOAuthDecode(snapshot.Data(), &initial) != nil ||
		initial.SchemaVersion != mcpOAuthRecordSchema || initial.Environment != s.environment ||
		initial.CodeHash != codeHash || initial.Status != "active" {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthInvalid
	}
	if initial.ClientID != clientID || initial.RedirectURI != redirectURI ||
		initial.Resource != resource || initial.Scope != mcpOAuthScope ||
		!verifyMCPCodeVerifier(initial.CodeChallenge, verifier) ||
		resource != s.ResourceURL(initial.ProjectID) {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthInvalid
	}
	accessToken, err := generateMCPOpaque(mcpOAuthAccessPrefix)
	if err != nil {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthUnavailable
	}
	refreshToken, err := generateMCPOpaque(mcpOAuthRefreshPrefix)
	if err != nil {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthUnavailable
	}
	now := s.now()
	grantID, err := mcpOAuthID()
	if err != nil {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthUnavailable
	}
	response := MCPOAuthTokenResponse{
		AccessToken: accessToken, TokenType: "Bearer",
		ExpiresIn: int(mcpOAuthAccessTTL.Seconds()), RefreshToken: refreshToken,
		Scope: mcpOAuthScope,
	}
	accessHash, refreshHash := mcpOAuthHash(accessToken), mcpOAuthHash(refreshToken)
	grantRef := s.collection("grants").Doc(grantID)
	accessRef := s.collection("access").Doc(accessHash)
	refreshRef := s.collection("refresh").Doc(refreshHash)
	err = s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		codeSnapshot, err := tx.Get(codeRef)
		if err != nil {
			return ErrMCPOAuthInvalid
		}
		var record mcpOAuthCodeRecord
		if mcpOAuthDecode(codeSnapshot.Data(), &record) != nil ||
			record.SchemaVersion != mcpOAuthRecordSchema || record.Environment != s.environment ||
			record.CodeHash != codeHash || record.Status != "active" {
			return ErrMCPOAuthReplay
		}
		if record.ClientID != clientID || record.RedirectURI != redirectURI ||
			record.Resource != resource || record.Scope != mcpOAuthScope ||
			!verifyMCPCodeVerifier(record.CodeChallenge, verifier) ||
			record.Resource != s.ResourceURL(record.ProjectID) || !now.Before(record.ExpiresAt) {
			return ErrMCPOAuthInvalid
		}
		clientRef := s.collection("clients").Doc(record.ClientID)
		clientSnapshot, err := tx.Get(clientRef)
		if err != nil {
			return ErrMCPOAuthInvalid
		}
		var client mcpOAuthClientRecord
		if mcpOAuthDecode(clientSnapshot.Data(), &client) != nil ||
			client.SchemaVersion != mcpOAuthRecordSchema || client.Environment != s.environment ||
			client.ClientID != record.ClientID || !containsString(client.RedirectURIs, record.RedirectURI) {
			return ErrMCPOAuthInvalid
		}
		user, project, err := readMCPAccountAndProjectInTransaction(ctx, tx, s.fs, record.UserID, record.ProjectID)
		if err != nil {
			return err
		}
		if !user.AllowsVersion(record.AuthVersion) || project.ID != record.ProjectID {
			return ErrMCPOAuthDenied
		}
		familyExpires := now.Add(mcpOAuthFamilyTTL)
		grant := mcpOAuthGrantRecord{
			SchemaVersion: mcpOAuthRecordSchema, Environment: s.environment,
			GrantID: grantID, UserID: record.UserID, AuthVersion: record.AuthVersion,
			ProjectID: record.ProjectID, ClientID: record.ClientID,
			Resource: record.Resource, Scope: record.Scope, Status: "active",
			CurrentRefreshHash: refreshHash, CreatedAt: now, ExpiresAt: familyExpires,
		}
		access := mcpOAuthAccessRecord{
			SchemaVersion: mcpOAuthRecordSchema, Environment: s.environment,
			TokenHash: accessHash, GrantID: grantID, ExpiresAt: now.Add(mcpOAuthAccessTTL), CreatedAt: now,
		}
		refresh := mcpOAuthRefreshRecord{
			SchemaVersion: mcpOAuthRecordSchema, Environment: s.environment,
			TokenHash: refreshHash, GrantID: grantID, Generation: 1, Status: "active",
			CreatedAt: now, ExpiresAt: familyExpires,
		}
		if err := tx.Create(grantRef, grant); err != nil {
			return ErrMCPOAuthUnavailable
		}
		if err := tx.Create(accessRef, access); err != nil {
			return ErrMCPOAuthUnavailable
		}
		if err := tx.Create(refreshRef, refresh); err != nil {
			return ErrMCPOAuthUnavailable
		}
		if err := tx.Update(codeRef, []firestore.Update{
			{Path: "status", Value: "redeemed"},
			{Path: "redeemed_at", Value: now},
		}); err != nil {
			return ErrMCPOAuthUnavailable
		}
		return nil
	})
	if err != nil {
		return MCPOAuthTokenResponse{}, normalizeMCPFirestoreError(err)
	}
	return response, nil
}

func (s *MCPOAuthService) Refresh(ctx context.Context, clientID, refreshToken, resource string) (MCPOAuthTokenResponse, error) {
	if s == nil || s.fs == nil || !validMCPOpaque(refreshToken, mcpOAuthRefreshPrefix) {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthInvalid
	}
	refreshHash := mcpOAuthHash(refreshToken)
	refreshRef := s.collection("refresh").Doc(refreshHash)
	initialSnapshot, err := refreshRef.Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return MCPOAuthTokenResponse{}, ErrMCPOAuthInvalid
		}
		return MCPOAuthTokenResponse{}, ErrMCPOAuthUnavailable
	}
	var initial mcpOAuthRefreshRecord
	if mcpOAuthDecode(initialSnapshot.Data(), &initial) != nil ||
		initial.SchemaVersion != mcpOAuthRecordSchema || initial.Environment != s.environment ||
		initial.TokenHash != refreshHash {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthUnavailable
	}
	nextAccess, err := generateMCPOpaque(mcpOAuthAccessPrefix)
	if err != nil {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthUnavailable
	}
	nextRefresh, err := generateMCPOpaque(mcpOAuthRefreshPrefix)
	if err != nil {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthUnavailable
	}
	now := s.now()
	nextAccessHash, nextRefreshHash := mcpOAuthHash(nextAccess), mcpOAuthHash(nextRefresh)
	nextResponse := MCPOAuthTokenResponse{
		AccessToken: nextAccess, TokenType: "Bearer", ExpiresIn: int(mcpOAuthAccessTTL.Seconds()),
		RefreshToken: nextRefresh, Scope: mcpOAuthScope,
	}
	retryResult := mcpOAuthRefreshResult{TokenResponse: nextResponse, ExpiresAt: initial.ExpiresAt}
	encryptedRetry, err := s.encryptRefreshRetry(retryResult)
	if err != nil {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthUnavailable
	}
	var result MCPOAuthTokenResponse
	expired, replayed := false, false
	err = s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		refreshSnapshot, err := tx.Get(refreshRef)
		if err != nil {
			return ErrMCPOAuthInvalid
		}
		var currentToken mcpOAuthRefreshRecord
		if mcpOAuthDecode(refreshSnapshot.Data(), &currentToken) != nil ||
			currentToken.SchemaVersion != mcpOAuthRecordSchema || currentToken.Environment != s.environment ||
			currentToken.TokenHash != refreshHash {
			return ErrMCPOAuthUnavailable
		}
		grantRef := s.collection("grants").Doc(currentToken.GrantID)
		grantSnapshot, err := tx.Get(grantRef)
		if err != nil {
			return ErrMCPOAuthInvalid
		}
		var grant mcpOAuthGrantRecord
		if mcpOAuthDecode(grantSnapshot.Data(), &grant) != nil ||
			grant.SchemaVersion != mcpOAuthRecordSchema || grant.Environment != s.environment ||
			grant.GrantID != currentToken.GrantID {
			return ErrMCPOAuthUnavailable
		}
		userRef := scopedfirestore.Collection(s.fs, "users").Doc(grant.UserID)
		userSnapshot, err := tx.Get(userRef)
		if err != nil {
			return ErrMCPOAuthDenied
		}
		var user UserRecord
		if userSnapshot.DataTo(&user) != nil || !user.AllowsVersion(grant.AuthVersion) {
			return ErrMCPOAuthDenied
		}
		project, err := readMCPProjectInTransaction(ctx, tx, s.fs, grant.UserID, grant.ProjectID)
		if err != nil {
			return err
		}
		if project.ID != grant.ProjectID || grant.Resource != resource ||
			resource != s.ResourceURL(grant.ProjectID) || grant.Scope != mcpOAuthScope ||
			grant.ClientID != clientID {
			return ErrMCPOAuthInvalid
		}
		if grant.Status != "active" {
			return ErrMCPOAuthInvalid
		}
		if !now.Before(grant.ExpiresAt) || !now.Before(currentToken.ExpiresAt) {
			if !now.Before(grant.ExpiresAt) {
				if err := tx.Update(grantRef, []firestore.Update{{Path: "status", Value: "expired"}}); err != nil {
					return ErrMCPOAuthUnavailable
				}
			}
			expired = true
			return nil
		}
		if currentToken.Status == "rotated" {
			if currentToken.RotatedToHash == grant.CurrentRefreshHash &&
				now.Before(currentToken.RetryUntil) && currentToken.RetryMaterialCipher != "" {
				retry, err := s.decryptRefreshRetry(currentToken.RetryMaterialCipher)
				if err != nil || !now.Before(retry.ExpiresAt) {
					return ErrMCPOAuthInvalid
				}
				result = retry.TokenResponse
				return nil
			}
			if err := tx.Update(grantRef, []firestore.Update{
				{Path: "status", Value: "revoked"},
				{Path: "revoked_at", Value: now},
			}); err != nil {
				return ErrMCPOAuthUnavailable
			}
			replayed = true
			return nil
		}
		if currentToken.Status != "active" || currentToken.TokenHash != grant.CurrentRefreshHash {
			return ErrMCPOAuthInvalid
		}
		nextAccessRecord := mcpOAuthAccessRecord{
			SchemaVersion: mcpOAuthRecordSchema, Environment: s.environment,
			TokenHash: nextAccessHash, GrantID: grant.GrantID,
			ExpiresAt: minTime(now.Add(mcpOAuthAccessTTL), grant.ExpiresAt), CreatedAt: now,
		}
		nextRefreshRecord := mcpOAuthRefreshRecord{
			SchemaVersion: mcpOAuthRecordSchema, Environment: s.environment,
			TokenHash: nextRefreshHash, GrantID: grant.GrantID,
			Generation: currentToken.Generation + 1, Status: "active",
			CreatedAt: now, ExpiresAt: grant.ExpiresAt,
		}
		if err := tx.Create(s.collection("access").Doc(nextAccessHash), nextAccessRecord); err != nil {
			return ErrMCPOAuthUnavailable
		}
		if err := tx.Create(s.collection("refresh").Doc(nextRefreshHash), nextRefreshRecord); err != nil {
			return ErrMCPOAuthUnavailable
		}
		if err := tx.Update(refreshRef, []firestore.Update{
			{Path: "status", Value: "rotated"},
			{Path: "rotated_to_hash", Value: nextRefreshHash},
			{Path: "retry_until", Value: now.Add(mcpOAuthRetryWindow)},
			{Path: "retry_material_cipher", Value: encryptedRetry},
		}); err != nil {
			return ErrMCPOAuthUnavailable
		}
		if err := tx.Update(grantRef, []firestore.Update{{Path: "current_refresh_hash", Value: nextRefreshHash}}); err != nil {
			return ErrMCPOAuthUnavailable
		}
		result = nextResponse
		return nil
	})
	if err != nil {
		return MCPOAuthTokenResponse{}, normalizeMCPFirestoreError(err)
	}
	if expired {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthExpired
	}
	if replayed {
		return MCPOAuthTokenResponse{}, ErrMCPOAuthReplay
	}
	return result, nil
}

func (s *MCPOAuthService) ListGrants(ctx context.Context, userID, projectID string) ([]MCPOAuthGrantInfo, error) {
	if s == nil || s.fs == nil || !ValidPathSegment(userID) || !ValidPathSegment(projectID) {
		return nil, ErrMCPOAuthUnavailable
	}
	if err := s.requireProjectOwner(ctx, userID, projectID); err != nil {
		return nil, err
	}
	iter := s.collection("grants").Where("user_id", "==", userID).Documents(ctx)
	defer iter.Stop()
	grants := make([]MCPOAuthGrantInfo, 0)
	for {
		doc, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, ErrMCPOAuthUnavailable
		}
		var grant mcpOAuthGrantRecord
		if mcpOAuthDecode(doc.Data(), &grant) != nil || grant.SchemaVersion != mcpOAuthRecordSchema ||
			grant.Environment != s.environment || grant.GrantID != doc.Ref.ID || grant.UserID != userID {
			return nil, ErrMCPOAuthUnavailable
		}
		if grant.ProjectID != projectID {
			continue
		}
		client, err := s.readClient(ctx, grant.ClientID)
		if err != nil {
			return nil, err
		}
		state := grant.Status
		if state == "active" && !s.now().Before(grant.ExpiresAt) {
			state = "expired"
		}
		name := client.ClientName
		if name == "" {
			name = "Unnamed MCP client"
		}
		info := MCPOAuthGrantInfo{
			ID: grant.GrantID, ClientName: name, ProjectID: grant.ProjectID,
			Resource: grant.Resource, Scope: grant.Scope, Status: state,
			CreatedAt: grant.CreatedAt, ExpiresAt: grant.ExpiresAt,
		}
		if !grant.RevokedAt.IsZero() {
			revokedAt := grant.RevokedAt
			info.RevokedAt = &revokedAt
		}
		grants = append(grants, info)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].CreatedAt.After(grants[j].CreatedAt) })
	return grants, nil
}

func (s *MCPOAuthService) RevokeGrant(ctx context.Context, userID string, authVersion int64, grantID string) error {
	if s == nil || s.fs == nil || !ValidPathSegment(userID) || !validMCPGrantID(grantID) {
		return ErrMCPOAuthNotFound
	}
	now := s.now()
	grantRef := s.collection("grants").Doc(grantID)
	err := s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, err := tx.Get(grantRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return ErrMCPOAuthNotFound
			}
			return ErrMCPOAuthUnavailable
		}
		var grant mcpOAuthGrantRecord
		if mcpOAuthDecode(snapshot.Data(), &grant) != nil || grant.SchemaVersion != mcpOAuthRecordSchema ||
			grant.Environment != s.environment || grant.GrantID != grantID ||
			grant.UserID != userID || !ValidPathSegment(grant.ProjectID) {
			return ErrMCPOAuthNotFound
		}
		user, project, err := readMCPAccountAndProjectInTransaction(ctx, tx, s.fs, userID, grant.ProjectID)
		if err != nil {
			return err
		}
		if !user.AllowsVersion(authVersion) || project.ID != grant.ProjectID {
			return ErrAccountInactive
		}
		if grant.Status == "revoked" {
			return nil
		}
		return tx.Update(grantRef, []firestore.Update{
			{Path: "status", Value: "revoked"},
			{Path: "revoked_at", Value: now},
		})
	})
	if err != nil {
		return normalizeMCPFirestoreError(err)
	}
	return nil
}

func (a *MCPGrantAuthority) AuthenticateAccessToken(ctx context.Context, token, projectID, resource string) (MCPOAuthIdentity, error) {
	if a == nil || a.fs == nil || a.environment == "" || !ValidPathSegment(projectID) ||
		!validMCPOpaque(token, mcpOAuthAccessPrefix) {
		return MCPOAuthIdentity{}, ErrMCPOAuthInvalid
	}
	tokenHash := mcpOAuthHash(token)
	snapshot, err := a.collection("access").Doc(tokenHash).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return MCPOAuthIdentity{}, ErrMCPOAuthInvalid
		}
		return MCPOAuthIdentity{}, ErrMCPOAuthUnavailable
	}
	var access mcpOAuthAccessRecord
	if mcpOAuthDecode(snapshot.Data(), &access) != nil || access.SchemaVersion != mcpOAuthRecordSchema ||
		access.Environment != a.environment || access.TokenHash != tokenHash {
		return MCPOAuthIdentity{}, ErrMCPOAuthUnavailable
	}
	if !a.now().Before(access.ExpiresAt) {
		return MCPOAuthIdentity{}, ErrMCPOAuthExpired
	}
	grantSnapshot, err := a.collection("grants").Doc(access.GrantID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return MCPOAuthIdentity{}, ErrMCPOAuthInvalid
		}
		return MCPOAuthIdentity{}, ErrMCPOAuthUnavailable
	}
	var grant mcpOAuthGrantRecord
	if mcpOAuthDecode(grantSnapshot.Data(), &grant) != nil ||
		grant.SchemaVersion != mcpOAuthRecordSchema || grant.Environment != a.environment ||
		grant.GrantID != access.GrantID {
		return MCPOAuthIdentity{}, ErrMCPOAuthUnavailable
	}
	if grant.Status != "active" || !a.now().Before(grant.ExpiresAt) ||
		grant.ProjectID != projectID || grant.Scope != mcpOAuthScope ||
		grant.Resource != resource || resource != a.resourceOrigin+"/mcp/projects/"+projectID {
		return MCPOAuthIdentity{}, ErrMCPOAuthDenied
	}
	user, err := a.lookup(ctx, grant.UserID)
	if err != nil {
		return MCPOAuthIdentity{}, ErrMCPOAuthUnavailable
	}
	if user == nil || !user.AllowsVersion(grant.AuthVersion) {
		return MCPOAuthIdentity{}, ErrMCPOAuthDenied
	}
	if err := a.authorize(ctx, grant.UserID, grant.ProjectID); err != nil {
		if errors.Is(err, ErrProjectPermissionDenied) {
			return MCPOAuthIdentity{}, ErrMCPOAuthDenied
		}
		return MCPOAuthIdentity{}, ErrMCPOAuthUnavailable
	}
	return MCPOAuthIdentity{
		UserID: grant.UserID, ProjectID: grant.ProjectID, GrantID: grant.GrantID,
		ClientID: grant.ClientID, Resource: grant.Resource, Scope: grant.Scope,
	}, nil
}

func (s *MCPOAuthService) requireProjectOwner(ctx context.Context, userID, projectID string) error {
	if s == nil || s.fs == nil || s.authorize == nil || !ValidPathSegment(userID) || !ValidPathSegment(projectID) {
		return ErrMCPOAuthUnavailable
	}
	err := s.authorize(ctx, userID, projectID)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrProjectPermissionDenied) {
		return ErrMCPOAuthDenied
	}
	return ErrMCPOAuthUnavailable
}

func getOwnedProject(ctx context.Context, fs *firestore.Client, userID, projectID string) (ProjectSummary, error) {
	if fs == nil || !ValidPathSegment(userID) || !ValidPathSegment(projectID) {
		return ProjectSummary{}, ErrMCPOAuthUnavailable
	}
	collection := scopedfirestore.Collection(fs, "projects")
	doc, err := collection.Doc(projectDocumentID(userID, projectID)).Get(ctx)
	if err == nil {
		if project, ok := projectFromFirestoreDocument(userID, doc.Ref.ID, doc.Data()); ok && project.ID == projectID {
			return project, nil
		}
		return ProjectSummary{}, ErrMCPOAuthDenied
	}
	if status.Code(err) != codes.NotFound {
		return ProjectSummary{}, ErrMCPOAuthUnavailable
	}
	iter := collection.Documents(ctx)
	defer iter.Stop()
	var found *ProjectSummary
	for {
		document, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return ProjectSummary{}, ErrMCPOAuthUnavailable
		}
		project, ok := projectFromFirestoreDocument(userID, document.Ref.ID, document.Data())
		if ok && project.ID == projectID {
			if found != nil {
				return ProjectSummary{}, ErrMCPOAuthDenied
			}
			found = &project
		}
	}
	if found == nil {
		return ProjectSummary{}, ErrMCPOAuthDenied
	}
	return *found, nil
}

func readMCPAccountAndProjectInTransaction(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, userID, projectID string) (UserRecord, ProjectSummary, error) {
	userRef := scopedfirestore.Collection(fs, "users").Doc(userID)
	userSnapshot, err := tx.Get(userRef)
	if err != nil {
		return UserRecord{}, ProjectSummary{}, ErrAccountInactive
	}
	var user UserRecord
	if userSnapshot.DataTo(&user) != nil || !user.Active() {
		return UserRecord{}, ProjectSummary{}, ErrAccountInactive
	}
	project, err := readMCPProjectInTransaction(ctx, tx, fs, userID, projectID)
	if err != nil {
		return UserRecord{}, ProjectSummary{}, err
	}
	return user, project, nil
}

func readMCPProjectInTransaction(ctx context.Context, tx *firestore.Transaction, fs *firestore.Client, userID, projectID string) (ProjectSummary, error) {
	if !ValidPathSegment(userID) || !ValidPathSegment(projectID) {
		return ProjectSummary{}, ErrMCPOAuthDenied
	}
	collection := scopedfirestore.Collection(fs, "projects")
	doc, err := tx.Get(collection.Doc(projectDocumentID(userID, projectID)))
	if err == nil {
		project, ok := projectFromFirestoreDocument(userID, doc.Ref.ID, doc.Data())
		if !ok || project.ID != projectID {
			return ProjectSummary{}, ErrMCPOAuthDenied
		}
		return project, nil
	}
	if status.Code(err) != codes.NotFound {
		return ProjectSummary{}, ErrMCPOAuthUnavailable
	}
	docs, err := tx.Documents(collection).GetAll()
	if err != nil {
		return ProjectSummary{}, ErrMCPOAuthUnavailable
	}
	var found *ProjectSummary
	for _, candidate := range docs {
		project, ok := projectFromFirestoreDocument(userID, candidate.Ref.ID, candidate.Data())
		if ok && project.ID == projectID {
			if found != nil {
				return ProjectSummary{}, ErrMCPOAuthDenied
			}
			found = &project
		}
	}
	if found == nil {
		return ProjectSummary{}, ErrMCPOAuthDenied
	}
	return *found, nil
}

func (s *MCPOAuthService) encryptRefreshRetry(result mcpOAuthRefreshResult) (string, error) {
	if s == nil || s.jwtSecret == "" || s.environment == "" {
		return "", ErrMCPOAuthUnavailable
	}
	plain, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte("lwc-mcp-refresh-retry\x00" + s.environment + "\x00" + s.jwtSecret))
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
	ciphertext := gcm.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func (s *MCPOAuthService) decryptRefreshRetry(encoded string) (mcpOAuthRefreshResult, error) {
	if s == nil || s.jwtSecret == "" || s.environment == "" {
		return mcpOAuthRefreshResult{}, ErrMCPOAuthUnavailable
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return mcpOAuthRefreshResult{}, err
	}
	key := sha256.Sum256([]byte("lwc-mcp-refresh-retry\x00" + s.environment + "\x00" + s.jwtSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return mcpOAuthRefreshResult{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return mcpOAuthRefreshResult{}, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return mcpOAuthRefreshResult{}, ErrMCPOAuthInvalid
	}
	nonce, sealed := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return mcpOAuthRefreshResult{}, err
	}
	var result mcpOAuthRefreshResult
	if json.Unmarshal(plain, &result) != nil ||
		!validMCPOpaque(result.TokenResponse.AccessToken, mcpOAuthAccessPrefix) ||
		!validMCPOpaque(result.TokenResponse.RefreshToken, mcpOAuthRefreshPrefix) ||
		result.TokenResponse.TokenType != "Bearer" || result.TokenResponse.Scope != mcpOAuthScope {
		return mcpOAuthRefreshResult{}, ErrMCPOAuthInvalid
	}
	return result, nil
}

func normalizeMCPFirestoreError(err error) error {
	switch {
	case errors.Is(err, ErrMCPOAuthInvalid), errors.Is(err, ErrMCPOAuthDenied),
		errors.Is(err, ErrMCPOAuthUnavailable), errors.Is(err, ErrMCPOAuthNotFound),
		errors.Is(err, ErrMCPOAuthExpired), errors.Is(err, ErrMCPOAuthReplay),
		errors.Is(err, ErrMCPOAuthBadRequest), errors.Is(err, ErrMCPOAuthUnsupported),
		errors.Is(err, ErrAccountInactive):
		return err
	default:
		return ErrMCPOAuthUnavailable
	}
}

func validMCPGrantID(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 16 && hex.EncodeToString(raw) == value
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func oauthRedirectWithCode(raw, code, state string) string {
	return oauthRedirect(raw, code, "", state)
}

func oauthRedirectWithError(raw, oauthError, state string) string {
	return oauthRedirect(raw, "", oauthError, state)
}

func oauthRedirect(raw, code, oauthError, state string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	if code != "" {
		query.Set("code", code)
	}
	if oauthError != "" {
		query.Set("error", oauthError)
	}
	if state != "" {
		query.Set("state", state)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
