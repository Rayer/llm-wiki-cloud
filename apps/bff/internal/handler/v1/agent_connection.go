package v1

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const agentConnectionSetupTTL = 24 * time.Hour

type agentConnectionSetup struct {
	SchemaVersion     int        `firestore:"schema_version"`
	SetupID           string     `firestore:"setup_id"`
	ConfirmationToken string     `firestore:"confirmation_token"`
	CreatedAt         time.Time  `firestore:"created_at"`
	ExpiresAt         time.Time  `firestore:"expires_at"`
	ConfirmedAt       *time.Time `firestore:"confirmed_at"`
}

type agentConnectionSetupResponse struct {
	SetupID           string     `json:"setup_id,omitempty"`
	ProjectID         string     `json:"project_id"`
	Status            string     `json:"status"`
	CreatedAt         *time.Time `json:"created_at,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	ConfirmedAt       *time.Time `json:"confirmed_at"`
	ConfirmationToken string     `json:"confirmation_token,omitempty"`
}

type confirmConnectionInput struct {
	SetupID           string `json:"setup_id"`
	ConfirmationToken string `json:"confirmation_token"`
}

type confirmConnectionResult struct {
	SetupID     string    `json:"setup_id"`
	ProjectID   string    `json:"project_id"`
	Status      string    `json:"status"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

var confirmConnectionInputSchema = json.RawMessage(`{"type":"object","properties":{"setup_id":{"type":"string","minLength":1,"maxLength":64},"confirmation_token":{"type":"string","minLength":1,"maxLength":128}},"required":["setup_id","confirmation_token"],"additionalProperties":false}`)
var confirmConnectionOutputSchema = json.RawMessage(`{"type":"object","properties":{"setup_id":{"type":"string"},"project_id":{"type":"string"},"status":{"type":"string","const":"confirmed"},"confirmed_at":{"type":"string","format":"date-time"}},"required":["setup_id","project_id","status","confirmed_at"],"additionalProperties":false}`)

const confirmConnectionDescription = "Confirm only that this authenticated Agent client received the current Wiki connection setup prompt. This records bounded setup metadata; it does not run a Query, read Wiki content, or start a Pipeline."
const confirmConnectionInstructions = "If the user is completing Agent connection setup and provides setup_id and confirmation_token, call confirm_connection when the tool is available and report its actual result. Do not describe confirmation as a successful Query. Without a setup identifier, do not guess or repeatedly call the tool."

func (h *Handler) CreateAgentConnectionCheck(c *gin.Context) {
	ownerID, projectID, ok := h.authorizeAgentConnectionRequest(c)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 2))
	if err != nil || len(body) != 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request body must be empty"})
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "connection setup is unavailable"})
		return
	}
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "connection setup is unavailable"})
		return
	}
	now := time.Now().UTC()
	setup := agentConnectionSetup{
		SchemaVersion: 1, SetupID: uuid.NewString(),
		ConfirmationToken: base64.RawURLEncoding.EncodeToString(tokenBytes[:]),
		CreatedAt:         now, ExpiresAt: now.Add(agentConnectionSetupTTL),
	}
	projectRef := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(ownerID, projectID))
	currentRef := projectRef.Collection("agent_connection").Doc("current")
	err = h.firestore.Raw().RunTransaction(c.Request.Context(), func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		project, err := tx.Get(projectRef)
		if err != nil {
			return err
		}
		if project.Data()["user_id"] != ownerID || project.Data()["project_id"] != projectID || project.Data()["status"] != "ready" {
			return errProfileProjectNotFound
		}
		return tx.Set(currentRef, setup)
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "connection setup is unavailable"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, agentConnectionSetupResponse{
		SetupID: setup.SetupID, ProjectID: projectID, Status: "waiting",
		CreatedAt: &setup.CreatedAt, ExpiresAt: &setup.ExpiresAt, ConfirmedAt: nil,
		ConfirmationToken: setup.ConfirmationToken,
	})
}

func (h *Handler) GetAgentConnectionCheck(c *gin.Context) {
	ownerID, projectID, ok := h.authorizeAgentConnectionRequest(c)
	if !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "connection setup is unavailable"})
		return
	}
	currentRef := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(ownerID, projectID)).Collection("agent_connection").Doc("current")
	snapshot, err := currentRef.Get(c.Request.Context())
	if status.Code(err) == codes.NotFound {
		c.JSON(http.StatusOK, agentConnectionSetupResponse{ProjectID: projectID, Status: "not_started", ConfirmedAt: nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "connection setup is unavailable"})
		return
	}
	var setup agentConnectionSetup
	if err := snapshot.DataTo(&setup); err != nil || setup.SchemaVersion != 1 || setup.SetupID == "" || setup.CreatedAt.IsZero() || setup.ExpiresAt.IsZero() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "connection setup is unavailable"})
		return
	}
	state := "waiting"
	if setup.ConfirmedAt != nil {
		state = "confirmed"
	} else if !time.Now().UTC().Before(setup.ExpiresAt) {
		state = "expired"
	}
	response := agentConnectionSetupResponse{
		SetupID: setup.SetupID, ProjectID: projectID, Status: state,
		CreatedAt: &setup.CreatedAt, ExpiresAt: &setup.ExpiresAt, ConfirmedAt: setup.ConfirmedAt,
	}
	if state == "waiting" {
		response.ConfirmationToken = setup.ConfirmationToken
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) authorizeAgentConnectionRequest(c *gin.Context) (string, string, bool) {
	ownerID := strings.TrimSpace(c.GetString("userID"))
	projectID := strings.TrimSpace(c.Param("pid"))
	if ownerID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return "", "", false
	}
	if projectID == "" || projectID != strings.TrimSpace(c.GetString("projectID")) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project scope is invalid"})
		return "", "", false
	}
	if _, err := h.AuthorizeProject(c.Request.Context(), ownerID, projectID, ProjectRead); err != nil {
		if errors.Is(err, errProfileProjectNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		} else {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "connection setup is unavailable"})
		}
		return "", "", false
	}
	return ownerID, projectID, true
}

func (h *Handler) callMCPConfirmConnection(ctx context.Context, _ *mcp.CallToolRequest, input confirmConnectionInput) (*mcp.CallToolResult, any, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || identity.userID == "" || identity.projectID == "" || !hasMCPCapability(identity.capabilities, mcpQueryCapability) {
		return connectionToolError("unauthorized"), nil, nil
	}
	if identity.revalidate == nil || identity.revalidate(ctx) != nil {
		return connectionToolError("unauthorized"), nil, nil
	}
	input.SetupID = strings.TrimSpace(input.SetupID)
	if len(input.SetupID) > 64 || input.SetupID == "" || len(input.ConfirmationToken) != 43 {
		return connectionToolError("invalid_setup"), nil, nil
	}
	if _, err := h.AuthorizeProject(ctx, identity.userID, identity.projectID, ProjectRead); err != nil {
		return connectionToolError("unauthorized"), nil, nil
	}
	confirmedAt, err := h.confirmAgentConnection(ctx, identity.userID, identity.projectID, input)
	if err != nil {
		if errors.Is(err, errAgentConnectionInvalidSetup) {
			return connectionToolError("invalid_setup"), nil, nil
		}
		return connectionToolError("unavailable"), nil, nil
	}
	result := confirmConnectionResult{SetupID: input.SetupID, ProjectID: identity.projectID, Status: "confirmed", ConfirmedAt: confirmedAt}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Connection setup confirmed."}}, StructuredContent: result}, result, nil
}

var errAgentConnectionInvalidSetup = errors.New("invalid setup")

func (h *Handler) confirmAgentConnection(ctx context.Context, ownerID, projectID string, input confirmConnectionInput) (time.Time, error) {
	if h.firestore == nil || h.firestore.Raw() == nil {
		return time.Time{}, errors.New("firestore unavailable")
	}
	projectRef := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(ownerID, projectID))
	currentRef := projectRef.Collection("agent_connection").Doc("current")
	now := time.Now().UTC()
	var confirmedAt time.Time
	err := h.firestore.Raw().RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		project, err := tx.Get(projectRef)
		if err != nil {
			return err
		}
		if project.Data()["user_id"] != ownerID || project.Data()["project_id"] != projectID || project.Data()["status"] != "ready" {
			return errAgentConnectionInvalidSetup
		}
		current, err := tx.Get(currentRef)
		if err != nil {
			return errAgentConnectionInvalidSetup
		}
		var setup agentConnectionSetup
		if err := current.DataTo(&setup); err != nil || setup.SchemaVersion != 1 || setup.SetupID != input.SetupID ||
			subtle.ConstantTimeCompare([]byte(setup.ConfirmationToken), []byte(input.ConfirmationToken)) != 1 {
			return errAgentConnectionInvalidSetup
		}
		if setup.ConfirmedAt != nil {
			confirmedAt = setup.ConfirmedAt.UTC()
			return nil
		}
		if setup.ExpiresAt.IsZero() || !now.Before(setup.ExpiresAt) {
			return errAgentConnectionInvalidSetup
		}
		confirmedAt = now
		return tx.Update(currentRef, []cloudfirestore.Update{{Path: "confirmed_at", Value: now}})
	})
	return confirmedAt, err
}

func connectionToolError(code string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: code}}}
}
