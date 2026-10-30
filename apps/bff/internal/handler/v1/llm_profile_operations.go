package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/llm"
)

const profileTestPrompt = "Reply with one short sentence confirming that this provider connection can answer a message."

type llmProfileOperationInput struct {
	ProfileID           string          `json:"profile_id,omitempty"`
	Revision            int64           `json:"revision,omitempty"`
	Provider            string          `json:"provider"`
	Protocol            string          `json:"protocol"`
	Endpoint            string          `json:"endpoint"`
	Model               string          `json:"model,omitempty"`
	Models              json.RawMessage `json:"models,omitempty"`
	Role                string          `json:"role"`
	CredentialMode      string          `json:"credential_mode"`
	APIKey              string          `json:"api_key,omitempty"`
	CredentialReference string          `json:"credential_reference,omitempty"`
}

func (h *Handler) DiscoverUserLLMProfileModels(c *gin.Context) {
	h.runLLMProfileOperation(c, false, false)
}
func (h *Handler) TestUserLLMProfileMessage(c *gin.Context) { h.runLLMProfileOperation(c, false, true) }
func (h *Handler) DiscoverAdminLLMProfileModels(c *gin.Context) {
	h.runLLMProfileOperation(c, true, false)
}
func (h *Handler) TestAdminLLMProfileMessage(c *gin.Context) { h.runLLMProfileOperation(c, true, true) }

func (h *Handler) runLLMProfileOperation(c *gin.Context, shared, testMessage bool) {
	if !h.requireBYOK(c) {
		return
	}
	owner := c.GetString("userID")
	if shared {
		owner = "platform"
	} else if !auth.ValidPathSegment(owner) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authenticated account is required"})
		return
	}
	var input llmProfileOperationInput
	if err := decodeStrictLLMJSON(c, maxLLMProfileRequestBytes, &input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid LLM profile operation"})
		return
	}
	role := llm.ProfileRole(strings.TrimSpace(input.Role))
	if role != llm.ProfileRoleFast && role != llm.ProfileRoleHeavy {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid LLM profile role"})
		return
	}
	config, key, err := h.resolveLLMProfileOperation(c.Request.Context(), owner, shared, input, role, testMessage)
	if err != nil {
		writeLLMProfileOperationError(c, err)
		return
	}
	defer clear(key)

	if testMessage {
		client, err := llm.NewProfileClient(config, string(key), llm.ProfileClientOptions{Timeout: 20 * time.Second, MaxOutputTokens: 256})
		if err != nil {
			writeLLMProfileOperationError(c, err)
			return
		}
		defer client.Close()
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		result, err := client.Complete(ctx, "You are checking a provider connection. Keep the reply brief and do not ask questions.", profileTestPrompt)
		if err != nil {
			writeLLMProfileOperationError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "role": role, "model": config.Model, "returned_model": result.Metadata.ReturnedModel, "text": result.Text})
		return
	}
	client, err := llm.NewProfileDiscoveryClient(config, string(key), llm.ProfileClientOptions{Timeout: 15 * time.Second})
	if err != nil {
		writeLLMProfileOperationError(c, err)
		return
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	models, err := client.DiscoverModels(ctx)
	if err != nil {
		var profileErr *llm.ProfileCallError
		if errors.As(err, &profileErr) && profileErr.Kind == llm.ProviderDiscoveryUnsupported {
			c.JSON(http.StatusOK, gin.H{"ok": true, "role": role, "status": "unsupported", "models": []string{}, "partial": false})
			return
		}
		writeLLMProfileOperationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "role": role, "status": "success", "models": models.Models, "partial": models.Partial})
}

func (h *Handler) resolveLLMProfileOperation(ctx context.Context, owner string, shared bool, input llmProfileOperationInput, role llm.ProfileRole, requireModel bool) (llm.ProfileConfig, []byte, error) {
	credentialMode := strings.TrimSpace(input.CredentialMode)
	switch credentialMode {
	case "saved":
		if strings.TrimSpace(input.APIKey) != "" {
			return llm.ProfileConfig{}, nil, errLLMProfileInvalid
		}
	case "replacement":
		if strings.TrimSpace(input.CredentialReference) != "" {
			return llm.ProfileConfig{}, nil, errLLMProfileCredentialUnavailable
		}
		if strings.TrimSpace(input.APIKey) == "" {
			return llm.ProfileConfig{}, nil, errLLMProfileInvalid
		}
	case "none":
		if strings.TrimSpace(input.APIKey) != "" || strings.TrimSpace(input.CredentialReference) != "" {
			return llm.ProfileConfig{}, nil, errLLMProfileCredentialUnavailable
		}
	default:
		return llm.ProfileConfig{}, nil, errLLMProfileInvalid
	}
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	protocol := llm.Protocol(strings.TrimSpace(input.Protocol))
	endpoint := strings.TrimSpace(input.Endpoint)
	models, err := profileOperationModels(input)
	if err != nil {
		return llm.ProfileConfig{}, nil, err
	}
	model := models.Fast
	if role == llm.ProfileRoleHeavy {
		model = models.Heavy
	}
	if requireModel && model == "" {
		return llm.ProfileConfig{}, nil, errLLMProfileInvalid
	}

	var record *llmProfileDocument
	profileID := strings.TrimSpace(input.ProfileID)
	if profileID != "" {
		if !auth.ValidPathSegment(profileID) || input.Revision < 1 {
			return llm.ProfileConfig{}, nil, errLLMProfileInvalid
		}
		ref := h.llmProfileRef(owner, profileID, shared)
		snapshot, err := ref.Get(ctx)
		if err != nil || (!shared && snapshot.Data()["owner_id"] != owner) {
			return llm.ProfileConfig{}, nil, errLLMProfileNotFound
		}
		var current llmProfileDocument
		if snapshot.DataTo(&current) != nil || current.OwnerID != owner {
			return llm.ProfileConfig{}, nil, errLLMProfileNotFound
		}
		if current.Revision != input.Revision {
			return llm.ProfileConfig{}, nil, errLLMProfileConflict
		}
		storedModels, ok := llmProfileRoleModels(current)
		if !ok {
			return llm.ProfileConfig{}, nil, errLLMProfileInvalid
		}
		if provider == "" {
			provider = current.Provider
		}
		if protocol == "" {
			protocol = llm.Protocol(current.Protocol)
		}
		if endpoint == "" {
			endpoint = current.Endpoint
		}
		if len(input.Models) == 0 && strings.TrimSpace(input.Model) == "" {
			models = storedModels
			if role == llm.ProfileRoleFast {
				model = models.Fast
			} else {
				model = models.Heavy
			}
		}
		if (provider != current.Provider || protocol != llm.Protocol(current.Protocol) || endpoint != current.Endpoint) && strings.TrimSpace(input.APIKey) == "" {
			return llm.ProfileConfig{}, nil, errLLMProfileCredentialUnavailable
		}
		if input.CredentialReference != "" && input.CredentialReference != current.CredentialReference {
			return llm.ProfileConfig{}, nil, errLLMProfileCredentialUnavailable
		}
		if current.CredentialOrigin == "deployment" && (input.APIKey != "" || input.CredentialReference != current.CredentialReference) {
			return llm.ProfileConfig{}, nil, errLLMProfileCredentialUnavailable
		}
		record = &current
	} else if input.Revision != 0 {
		return llm.ProfileConfig{}, nil, errLLMProfileInvalid
	}

	if input.CredentialReference != "" && !shared {
		return llm.ProfileConfig{}, nil, errLLMProfileCredentialUnavailable
	}
	if input.CredentialReference != "" && strings.TrimSpace(input.APIKey) != "" {
		return llm.ProfileConfig{}, nil, errLLMProfileCredentialUnavailable
	}
	if provider == "" || endpoint == "" || (protocol != llm.ProtocolOpenAIChatCompletions && protocol != llm.ProtocolAnthropicMessages) {
		return llm.ProfileConfig{}, nil, errLLMProfileInvalid
	}
	key := []byte(nil)
	switch credentialMode {
	case "replacement":
		key = []byte(strings.TrimSpace(input.APIKey))
	case "saved":
		if record != nil {
			if record.SecretConfigured {
				key, err = h.profileCredential(*record, owner)
			} else {
				err = errLLMProfileCredentialUnavailable
			}
		} else if input.CredentialReference != "" {
			record = &llmProfileDocument{ID: "draft", OwnerID: owner, CredentialOrigin: "deployment", CredentialReference: input.CredentialReference, SecretConfigured: true}
			key, err = h.profileCredential(*record, owner)
		} else {
			err = errLLMProfileCredentialUnavailable
		}
	case "none":
		// Explicitly do not consult the saved profile or deployment reference.
		err = nil
	}
	if err != nil {
		clear(key)
		return llm.ProfileConfig{}, nil, errLLMProfileCredentialUnavailable
	}
	if len(key) > 4096 || bytes.ContainsAny(key, "\r\n\x00") {
		clear(key)
		return llm.ProfileConfig{}, nil, errLLMProfileInvalid
	}
	if profileID == "" {
		profileID = "draft"
	}
	revision := input.Revision
	if revision == 0 {
		revision = 1
	}
	config := llm.ProfileConfig{ID: profileID, Revision: revision, Role: role, Provider: provider, Protocol: protocol, Endpoint: endpoint, Model: model}
	return config, key, nil
}

func profileOperationModels(input llmProfileOperationInput) (llmProfileModels, error) {
	legacy := strings.TrimSpace(input.Model)
	if len(input.Models) == 0 {
		if legacy == "" {
			return llmProfileModels{}, nil
		}
		if len(legacy) > 200 {
			return llmProfileModels{}, errLLMProfileInvalid
		}
		return llmProfileModels{Fast: legacy, Heavy: legacy}, nil
	}
	trimmedModels := bytes.TrimSpace(input.Models)
	if len(trimmedModels) == 0 || trimmedModels[0] != '{' {
		return llmProfileModels{}, errLLMProfileInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmedModels))
	decoder.DisallowUnknownFields()
	var models llmProfileModels
	if decoder.Decode(&models) != nil || decoder.Decode(new(any)) != io.EOF {
		return llmProfileModels{}, errLLMProfileInvalid
	}
	models.Fast, models.Heavy = strings.TrimSpace(models.Fast), strings.TrimSpace(models.Heavy)
	if len(models.Fast) > 200 || len(models.Heavy) > 200 || (legacy != "" && legacy != models.Heavy) {
		return llmProfileModels{}, errLLMProfileInvalid
	}
	return models, nil
}

func writeLLMProfileOperationError(c *gin.Context, err error) {
	var profileErr *llm.ProfileCallError
	if errors.As(err, &profileErr) {
		status := http.StatusBadGateway
		if profileErr.Kind == llm.ProviderRateLimited || profileErr.Kind == llm.ProviderQuotaExhausted {
			status = http.StatusTooManyRequests
		}
		if profileErr.Kind == llm.ProviderTimeout {
			status = http.StatusGatewayTimeout
		}
		c.JSON(status, gin.H{"error": string(profileErr.Kind)})
		return
	}
	switch {
	case errors.Is(err, errLLMProfileNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "profile_not_found"})
	case errors.Is(err, errLLMProfileConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "profile_changed"})
	case errors.Is(err, errLLMProfileCredentialUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "credential_unavailable"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_profile_operation"})
	}
}
