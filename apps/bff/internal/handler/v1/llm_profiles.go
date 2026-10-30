package v1

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/byok"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/idtoken"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	userLLMProfilesCollection    = "llm_profiles"
	sharedLLMProfilesCollection  = "llm_shared_profiles"
	sharedLLMPoolStateCollection = "llm_shared_profile_state"
	profileRevisionCollection    = "revisions"
)

type llmProfileModels struct {
	Fast  string `firestore:"fast" json:"fast"`
	Heavy string `firestore:"heavy" json:"heavy"`
}

type sharedLLMPoolState struct {
	Initialized bool      `firestore:"initialized"`
	UpdatedAt   time.Time `firestore:"updated_at"`
}

type BYOKDeploymentCredential struct {
	Reference string
	Label     string
	Value     []byte
}

type deploymentCredentialOption struct {
	Reference string `json:"reference"`
	Label     string `json:"label"`
}

type llmProfileDocument struct {
	ID                  string           `firestore:"id"`
	OwnerID             string           `firestore:"owner_id"`
	Name                string           `firestore:"name"`
	Provider            string           `firestore:"provider"`
	Protocol            string           `firestore:"protocol"`
	Endpoint            string           `firestore:"endpoint"`
	Model               string           `firestore:"model"`
	Models              llmProfileModels `firestore:"models,omitempty"`
	Revision            int64            `firestore:"revision"`
	CredentialRevision  int64            `firestore:"credential_revision"`
	CredentialOrigin    string           `firestore:"credential_origin"`
	CredentialReference string           `firestore:"credential_reference"`
	EncryptedAPIKey     string           `firestore:"encrypted_api_key"`
	SecretConfigured    bool             `firestore:"secret_configured"`
	Enabled             bool             `firestore:"enabled"`
	Public              bool             `firestore:"public"`
	Priority            int              `firestore:"priority"`
	BoundProjects       []string         `firestore:"bound_projects"`
	UpdatedAt           time.Time        `firestore:"updated_at"`
}

type llmProfileView struct {
	ID                  string           `json:"id"`
	Name                string           `json:"name"`
	Provider            string           `json:"provider"`
	Protocol            string           `json:"protocol"`
	Endpoint            string           `json:"endpoint"`
	Model               string           `json:"model"`
	Models              llmProfileModels `json:"models"`
	Revision            int64            `json:"revision"`
	SecretConfigured    bool             `json:"secret_configured"`
	CredentialOrigin    string           `json:"credential_origin,omitempty"`
	CredentialReference string           `json:"credential_reference,omitempty"`
	Enabled             bool             `json:"enabled,omitempty"`
	Public              bool             `json:"available_to_public,omitempty"`
	Priority            int              `json:"priority,omitempty"`
}

type llmProfileInput struct {
	Name                string          `json:"name"`
	Provider            string          `json:"provider"`
	Protocol            string          `json:"protocol"`
	Endpoint            string          `json:"endpoint"`
	Model               string          `json:"model"`
	Models              json.RawMessage `json:"models"`
	APIKey              string          `json:"api_key,omitempty"`
	CredentialReference string          `json:"credential_reference,omitempty"`
	ClearAPIKey         bool            `json:"clear_api_key,omitempty"`
	Revision            int64           `json:"revision"`
	Enabled             *bool           `json:"enabled,omitempty"`
	Public              *bool           `json:"available_to_public,omitempty"`
	Priority            *int            `json:"priority,omitempty"`
}

type llmBindingInput struct {
	Source    string `json:"source"`
	ProfileID string `json:"profile_id,omitempty"`
	Revision  int64  `json:"revision"`
}

type llmBindingView struct {
	Source          string `json:"source"`
	ProfileID       string `json:"profile_id,omitempty"`
	ProfileRevision int64  `json:"profile_revision,omitempty"`
	Revision        int64  `json:"revision"`
}

type pipelineLLMProfile struct {
	SchemaVersion  int              `json:"schema_version"`
	SnapshotDigest string           `json:"snapshot_digest"`
	RoleMapVersion int              `json:"role_map_version"`
	ID             string           `json:"id"`
	Revision       int64            `json:"revision"`
	Provider       string           `json:"provider"`
	Protocol       llm.Protocol     `json:"protocol"`
	Endpoint       string           `json:"endpoint"`
	Model          string           `json:"model,omitempty"`
	Models         llmProfileModels `json:"models,omitempty"`
	APIKey         []byte           `json:"api_key,omitempty"`
}

type pipelineLLMProfileRequest struct {
	ReservationID      string `json:"reservation_id"`
	UserID             string `json:"user_id"`
	ProjectID          string `json:"project_id"`
	IncludeCredentials bool   `json:"include_credentials"`
}

func (h *Handler) SetBYOKCredentialBox(box *byok.CredentialBox) { h.byokCredentialBox = box }

func (h *Handler) SetBYOKCredentialEnvironment(environment string) {
	h.byokEnvironment = strings.TrimSpace(environment)
}

func (h *Handler) SetBYOKDeploymentCredentials(credentials []BYOKDeploymentCredential) {
	for _, existing := range h.byokDeploymentCredentials {
		clear(existing.Value)
	}
	h.byokDeploymentCredentials = make(map[string]BYOKDeploymentCredential, len(credentials))
	for _, credential := range credentials {
		reference := strings.TrimSpace(credential.Reference)
		if reference == "" || len(credential.Value) == 0 {
			continue
		}
		credential.Reference = reference
		credential.Label = strings.TrimSpace(credential.Label)
		credential.Value = bytes.Clone(credential.Value)
		h.byokDeploymentCredentials[reference] = credential
	}
}

func (h *Handler) SetPipelineWorkerIdentity(audience, serviceAccount string) {
	h.pipelineWorkerAudience = strings.TrimSpace(audience)
	h.pipelineWorkerServiceAccount = strings.TrimSpace(serviceAccount)
}

func (h *Handler) SetPipelineWorkerIdentityValidator(validator func(context.Context, string, string) (string, error)) {
	h.validatePipelineWorkerIdentity = validator
}

// PipelineLLMProfile serves server-owned, one-attempt profile resolution to the
// authenticated Pipeline worker. Provider credentials travel only over the
// worker's authenticated TLS request and never enter Cloud Run job overrides.
func (h *Handler) PipelineLLMProfile(c *gin.Context) {
	if h.localPipeline == nil {
		if h.pipelineWorkerAudience == "" || h.pipelineWorkerServiceAccount == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Pipeline profile resolution is unavailable"})
			return
		}
		bearer := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if bearer == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Pipeline worker identity is required"})
			return
		}
		validate := h.validatePipelineWorkerIdentity
		if validate == nil {
			validate = func(ctx context.Context, token, audience string) (string, error) {
				payload, err := idtoken.Validate(ctx, token, audience)
				if err != nil || payload == nil || payload.Audience != audience || payload.Claims["email_verified"] != true {
					return "", errors.New("invalid Pipeline worker identity")
				}
				email, _ := payload.Claims["email"].(string)
				return email, nil
			}
		}
		email, err := validate(c.Request.Context(), bearer, h.pipelineWorkerAudience)
		if err != nil || email != h.pipelineWorkerServiceAccount {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Pipeline worker identity is invalid"})
			return
		}
	}
	var request pipelineLLMProfileRequest
	bindErr := decodeStrictLLMJSON(c, maxLLMProfileRequestBytes, &request)
	reservationValid := auth.ValidPathSegment(request.ReservationID)
	userValid := auth.ValidPathSegment(request.UserID)
	projectValid := auth.ValidPathSegment(request.ProjectID)
	if bindErr != nil || !reservationValid || !userValid || !projectValid {
		slog.InfoContext(c.Request.Context(), "pipeline.profile.resolve.request_rejected",
			"json_valid", bindErr == nil,
			"reservation_id_valid", reservationValid,
			"user_id_valid", userValid,
			"project_id_valid", projectValid,
		)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid Pipeline profile request"})
		return
	}
	quotaStore, ok := h.effectiveQuotaStore().(pipelineQuotaStore)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Pipeline profile resolution is unavailable"})
		return
	}
	reservation, exists, err := quotaStore.GetQuotaReservation(c.Request.Context(), request.ReservationID)
	if err != nil || !exists || reservation.SettlementStatus != "pending" || reservation.UserID != request.UserID || reservation.ProjectID != request.ProjectID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Pipeline profile request is not current"})
		return
	}
	if _, err := h.AuthorizeProject(c.Request.Context(), request.UserID, request.ProjectID, ProjectRead); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Project profile resolution is not authorized"})
		return
	}
	source, profiles, snapshot, err := h.resolvePipelineProfileOperationWire(c.Request.Context(), request.ReservationID, request.UserID, request.ProjectID, request.IncludeCredentials)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Project LLM source is unavailable"})
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("llm.source", source),
		attribute.Int("llm.profile.count", len(profiles)),
	}
	if len(profiles) > 0 {
		attrs = append(attrs,
			attribute.String("llm.profile.id", profiles[0].ID),
			attribute.Int64("llm.profile.revision", profiles[0].Revision),
			attribute.String("llm.provider", profiles[0].Provider),
			attribute.String("llm.protocol", string(profiles[0].Protocol)),
			attribute.String("llm.model.fast", profiles[0].Models.Fast),
			attribute.String("llm.model.heavy", profiles[0].Models.Heavy),
		)
	}
	trace.SpanFromContext(c.Request.Context()).SetAttributes(attrs...)
	response := struct {
		SchemaVersion  int                  `json:"schema_version"`
		SnapshotDigest string               `json:"snapshot_digest"`
		RoleMapVersion int                  `json:"role_map_version"`
		Source         string               `json:"source"`
		Profiles       []pipelineLLMProfile `json:"profiles"`
	}{SchemaVersion: 2, SnapshotDigest: snapshot.ConfigDigest, RoleMapVersion: snapshot.RoleMapVersion, Source: source, Profiles: profiles}
	body, err := json.Marshal(response)
	if err != nil {
		for i := range profiles {
			clear(profiles[i].APIKey)
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Project LLM source is unavailable"})
		return
	}
	for i := range profiles {
		clear(profiles[i].APIKey)
	}
	c.Data(http.StatusOK, "application/json", body)
	clear(body)
}

func (h *Handler) resolvePipelineLLMProfiles(ctx context.Context, owner, project string) (string, []pipelineLLMProfile, error) {
	bindingSnapshot, err := h.projectLLMBindingRef(owner, project).Get(ctx)
	source := "shared"
	profileID := ""
	if status.Code(err) != codes.NotFound {
		if err != nil {
			return "", nil, errLLMProfileUnavailable
		}
		var binding llmBindingView
		if bindingSnapshot.DataTo(&binding) != nil {
			return "", nil, errLLMProfileUnavailable
		}
		source = h.effectiveProjectLLMSource(owner, binding.Source)
		if source == "personal" {
			profileID = binding.ProfileID
		}
	}
	if source == "personal" {
		if !auth.ValidPathSegment(profileID) {
			return "", nil, errLLMProfileUnavailable
		}
		snapshot, err := h.userLLMProfileRef(owner, profileID).Get(ctx)
		if err != nil {
			return "", nil, errLLMProfileUnavailable
		}
		var record llmProfileDocument
		if snapshot.DataTo(&record) != nil || record.OwnerID != owner || !record.Enabled {
			return "", nil, errLLMProfileUnavailable
		}
		profile, err := h.pipelineProfileForRecord(record, owner)
		if err != nil {
			return "", nil, err
		}
		return source, []pipelineLLMProfile{profile}, nil
	}
	if source != "shared" {
		return "", nil, errLLMProfileUnavailable
	}
	docs, err := scopedfirestore.Collection(h.firestore.Raw(), sharedLLMProfilesCollection).Documents(ctx).GetAll()
	if err != nil {
		return "", nil, errLLMProfileUnavailable
	}
	if len(docs) == 0 {
		initialized, err := h.sharedLLMPoolInitialized(ctx)
		if err != nil {
			return "", nil, errLLMProfileUnavailable
		}
		if initialized {
			return "", nil, errLLMSharedPoolUnavailable
		}
		return "bootstrap", nil, nil
	}
	records := make([]llmProfileDocument, 0, len(docs))
	for _, doc := range docs {
		var record llmProfileDocument
		if doc.DataTo(&record) != nil || record.OwnerID != "platform" {
			return "", nil, errLLMProfileUnavailable
		}
		if record.Enabled && record.Public {
			records = append(records, record)
		}
	}
	if len(records) == 0 {
		return "", nil, errLLMSharedPoolUnavailable
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Priority == records[j].Priority {
			return records[i].ID < records[j].ID
		}
		return records[i].Priority < records[j].Priority
	})
	profiles := make([]pipelineLLMProfile, 0, len(records))
	for _, record := range records {
		profile, err := h.pipelineProfileForRecord(record, "platform")
		if err != nil {
			for i := range profiles {
				clear(profiles[i].APIKey)
			}
			return "", nil, err
		}
		profiles = append(profiles, profile)
	}
	return source, profiles, nil
}

func (h *Handler) pipelineProfileForRecord(record llmProfileDocument, owner string) (pipelineLLMProfile, error) {
	if h.byokCredentialBox == nil {
		return pipelineLLMProfile{}, errLLMProfileUnavailable
	}
	models, ok := llmProfileRoleModels(record)
	if !ok {
		return pipelineLLMProfile{}, errLLMProfileUnavailable
	}
	key, err := h.profileCredential(record, owner)
	if err != nil {
		return pipelineLLMProfile{}, errLLMProfileUnavailable
	}
	return pipelineLLMProfile{SchemaVersion: 2, ID: record.ID, Revision: record.Revision, Provider: record.Provider,
		Protocol: llm.Protocol(record.Protocol), Endpoint: record.Endpoint, Models: models, APIKey: key}, nil
}

func (h *Handler) profileCredential(record llmProfileDocument, owner string) ([]byte, error) {
	if !record.SecretConfigured {
		if record.CredentialOrigin == "deployment" || record.CredentialReference != "" {
			return nil, errLLMProfileUnavailable
		}
		return nil, nil
	}
	origin := record.CredentialOrigin
	if origin == "" {
		origin = "managed"
	}
	switch origin {
	case "deployment":
		credential, ok := h.byokDeploymentCredentials[record.CredentialReference]
		if !ok || len(credential.Value) == 0 {
			return nil, errLLMProfileUnavailable
		}
		return bytes.Clone(credential.Value), nil
	case "managed":
		if h.byokCredentialBox == nil || record.CredentialRevision < 1 {
			return nil, errLLMProfileUnavailable
		}
		return h.byokCredentialBox.Decrypt(record.EncryptedAPIKey, h.llmProfileAAD(owner, record))
	default:
		return nil, errLLMProfileUnavailable
	}
}

func (h *Handler) UserLLMProfiles(c *gin.Context) {
	if !h.requireBYOK(c) {
		return
	}
	owner := c.GetString("userID")
	if !auth.ValidPathSegment(owner) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authenticated account is required"})
		return
	}
	query := scopedfirestore.Collection(h.firestore.Raw(), userLLMProfilesCollection).Where("owner_id", "==", owner)
	docs, err := query.Documents(c.Request.Context()).GetAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read LLM profiles"})
		return
	}
	profiles := make([]llmProfileView, 0, len(docs))
	for _, doc := range docs {
		var record llmProfileDocument
		if doc.DataTo(&record) == nil && record.OwnerID == owner {
			if _, ok := llmProfileRoleModels(record); !ok {
				continue
			}
			profiles = append(profiles, viewLLMProfile(record, false))
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	c.JSON(http.StatusOK, gin.H{"profiles": profiles})
}

func (h *Handler) CreateUserLLMProfile(c *gin.Context) {
	h.createLLMProfile(c, false)
}

func (h *Handler) UpdateUserLLMProfile(c *gin.Context) {
	h.updateLLMProfile(c, false)
}

func (h *Handler) DeleteUserLLMProfile(c *gin.Context) {
	h.deleteLLMProfile(c, false)
}

func (h *Handler) AdminLLMProfiles(c *gin.Context) {
	if !h.requireBYOK(c) {
		return
	}
	docs, err := scopedfirestore.Collection(h.firestore.Raw(), sharedLLMProfilesCollection).Documents(c.Request.Context()).GetAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read shared profiles"})
		return
	}
	profiles := make([]llmProfileView, 0, len(docs))
	for _, doc := range docs {
		var record llmProfileDocument
		if doc.DataTo(&record) == nil {
			if _, ok := llmProfileRoleModels(record); !ok {
				continue
			}
			profiles = append(profiles, viewLLMProfile(record, true))
		}
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Priority == profiles[j].Priority {
			return profiles[i].ID < profiles[j].ID
		}
		return profiles[i].Priority < profiles[j].Priority
	})
	c.JSON(http.StatusOK, gin.H{"profiles": profiles, "deployment_credentials": h.deploymentCredentialOptions()})
}

func (h *Handler) deploymentCredentialOptions() []deploymentCredentialOption {
	options := make([]deploymentCredentialOption, 0, len(h.byokDeploymentCredentials))
	for _, credential := range h.byokDeploymentCredentials {
		options = append(options, deploymentCredentialOption{Reference: credential.Reference, Label: credential.Label})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Reference < options[j].Reference })
	return options
}

func (h *Handler) CreateAdminLLMProfile(c *gin.Context) { h.createLLMProfile(c, true) }
func (h *Handler) UpdateAdminLLMProfile(c *gin.Context) { h.updateLLMProfile(c, true) }
func (h *Handler) DeleteAdminLLMProfile(c *gin.Context) { h.deleteLLMProfile(c, true) }

func (h *Handler) ProjectLLMBinding(c *gin.Context) {
	if !h.requireBYOK(c) {
		return
	}
	userID, projectID := c.GetString("userID"), strings.TrimSpace(c.Param("pid"))
	if _, err := h.AuthorizeProject(c.Request.Context(), userID, projectID, ProjectRead); err != nil {
		writeLLMProfileError(c, err)
		return
	}
	ref := h.projectLLMBindingRef(userID, projectID)
	snapshot, err := ref.Get(c.Request.Context())
	if status.Code(err) == codes.NotFound {
		c.JSON(http.StatusOK, llmBindingView{Source: "shared", Revision: 0})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read Project LLM source"})
		return
	}
	var binding llmBindingView
	if err := snapshot.DataTo(&binding); err != nil || (binding.Source != "shared" && binding.Source != "personal") {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Project LLM source is unreadable"})
		return
	}
	if h.effectiveProjectLLMSource(userID, binding.Source) == "shared" {
		binding.Source = "shared"
		binding.ProfileID = ""
		binding.ProfileRevision = 0
	}
	c.JSON(http.StatusOK, binding)
}

func (h *Handler) SetProjectLLMBinding(c *gin.Context) {
	var input llmBindingInput
	if err := c.ShouldBindJSON(&input); err != nil || (input.Source != "shared" && input.Source != "personal") || (input.Source == "personal" && input.ProfileID == "") || input.Revision < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid Project LLM source"})
		return
	}
	if input.Source == "personal" && h.rejectTrialDemo(c) {
		return
	}
	if !h.requireBYOK(c) {
		return
	}
	userID, projectID := c.GetString("userID"), strings.TrimSpace(c.Param("pid"))
	if _, err := h.AuthorizeProject(c.Request.Context(), userID, projectID, ProjectEdit); err != nil {
		writeLLMProfileError(c, err)
		return
	}
	if input.Source == "shared" {
		input.ProfileID = ""
	}
	projectRef := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(userID, projectID))
	bindingRef := projectRef.Collection("llm_profile_binding").Doc("current")
	var next llmBindingView
	err := h.firestore.Raw().RunTransaction(c.Request.Context(), func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		project, err := tx.Get(projectRef)
		if err != nil {
			return errProfileProjectNotFound
		}
		if err := authorizeProjectDocument(projectRef.ID, project.Data(), userID, projectID, ProjectEdit); err != nil {
			return err
		}
		currentSnapshot, err := tx.Get(bindingRef)
		current := llmBindingView{Source: "shared"}
		if err == nil {
			if err := currentSnapshot.DataTo(&current); err != nil {
				return errLLMProfileUnavailable
			}
		} else if status.Code(err) != codes.NotFound {
			return err
		}
		if current.Revision != input.Revision {
			return errLLMProfileConflict
		}
		var newProfileRef *cloudfirestore.DocumentRef
		var newProfile llmProfileDocument
		if input.Source == "personal" {
			newProfileRef = h.userLLMProfileRef(userID, input.ProfileID)
			profileSnapshot, err := tx.Get(newProfileRef)
			if err != nil {
				return errLLMProfileNotFound
			}
			if err := profileSnapshot.DataTo(&newProfile); err != nil || newProfile.OwnerID != userID {
				return errLLMProfileNotFound
			}
			if !newProfile.Enabled {
				return errLLMProfileDisabled
			}
		}
		projectKey := userID + "/" + projectID
		if current.Source == "personal" && current.ProfileID != "" && current.ProfileID != input.ProfileID {
			oldRef := h.userLLMProfileRef(userID, current.ProfileID)
			oldSnapshot, err := tx.Get(oldRef)
			if err == nil {
				var old llmProfileDocument
				if oldSnapshot.DataTo(&old) == nil {
					old.BoundProjects = removeString(old.BoundProjects, projectKey)
					if err := tx.Set(oldRef, old); err != nil {
						return err
					}
				}
			} else if status.Code(err) != codes.NotFound {
				return err
			}
		}
		if newProfileRef != nil {
			newProfile.BoundProjects = appendUnique(newProfile.BoundProjects, projectKey)
			if err := tx.Set(newProfileRef, newProfile); err != nil {
				return err
			}
		}
		next = llmBindingView{Source: input.Source, ProfileID: input.ProfileID, Revision: current.Revision + 1}
		if newProfileRef != nil {
			next.ProfileRevision = newProfile.Revision
		}
		return tx.Set(bindingRef, next)
	})
	if err != nil {
		writeLLMProfileError(c, err)
		return
	}
	c.JSON(http.StatusOK, next)
}

func (h *Handler) createLLMProfile(c *gin.Context, shared bool) {
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
	var input llmProfileInput
	if err := decodeLLMProfileJSON(c, &input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid LLM profile"})
		return
	}
	profileID, err := newLLMProfileID()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create LLM profile"})
		return
	}
	record, err := h.buildLLMProfile(owner, profileID, input, nil, shared)
	if err != nil {
		writeLLMProfileError(c, err)
		return
	}
	ref := h.llmProfileRef(owner, profileID, shared)
	err = h.firestore.Raw().RunTransaction(c.Request.Context(), func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		if err := tx.Create(ref, record); err != nil {
			return err
		}
		if err := tx.Create(profileRevisionRef(ref, record.Revision), record); err != nil {
			return err
		}
		if shared {
			return tx.Set(h.sharedLLMPoolStateRef(), sharedLLMPoolState{Initialized: true, UpdatedAt: time.Now().UTC()})
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save LLM profile"})
		return
	}
	c.JSON(http.StatusCreated, viewLLMProfile(record, shared))
}

func (h *Handler) updateLLMProfile(c *gin.Context, shared bool) {
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
	profileID := strings.TrimSpace(c.Param("profileID"))
	if !auth.ValidPathSegment(profileID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "LLM profile not found"})
		return
	}
	var input llmProfileInput
	if err := decodeLLMProfileJSON(c, &input); err != nil || input.Revision < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid LLM profile update"})
		return
	}
	ref := h.llmProfileRef(owner, profileID, shared)
	var next llmProfileDocument
	err := h.firestore.Raw().RunTransaction(c.Request.Context(), func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil || (!shared && snapshot.Data()["owner_id"] != owner) {
			return errLLMProfileNotFound
		}
		var current llmProfileDocument
		if err := snapshot.DataTo(&current); err != nil || current.OwnerID != owner || current.Revision != input.Revision {
			if current.Revision != input.Revision {
				return errLLMProfileConflict
			}
			return errLLMProfileNotFound
		}
		updated, err := h.buildLLMProfile(owner, profileID, input, &current, shared)
		if err != nil {
			return err
		}
		updated.BoundProjects = current.BoundProjects
		next = updated
		if err := tx.Set(ref, updated); err != nil {
			return err
		}
		if err := tx.Create(profileRevisionRef(ref, updated.Revision), updated); err != nil {
			return err
		}
		if shared {
			if err := tx.Set(h.sharedLLMPoolStateRef(), sharedLLMPoolState{Initialized: true, UpdatedAt: time.Now().UTC()}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeLLMProfileError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewLLMProfile(next, shared))
}

func (h *Handler) deleteLLMProfile(c *gin.Context, shared bool) {
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
	profileID := strings.TrimSpace(c.Param("profileID"))
	ref := h.llmProfileRef(owner, profileID, shared)
	err := h.firestore.Raw().RunTransaction(c.Request.Context(), func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		snapshot, err := tx.Get(ref)
		if err != nil {
			return errLLMProfileNotFound
		}
		var record llmProfileDocument
		if err := snapshot.DataTo(&record); err != nil || record.OwnerID != owner {
			return errLLMProfileNotFound
		}
		if len(record.BoundProjects) > 0 {
			return errLLMProfileInUse
		}
		if err := tx.Delete(ref); err != nil {
			return err
		}
		if shared {
			if err := tx.Set(h.sharedLLMPoolStateRef(), sharedLLMPoolState{Initialized: true, UpdatedAt: time.Now().UTC()}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeLLMProfileError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) buildLLMProfile(owner, profileID string, input llmProfileInput, current *llmProfileDocument, shared bool) (llmProfileDocument, error) {
	name := strings.TrimSpace(input.Name)
	models, err := profileInputModels(input, current)
	if err != nil {
		return llmProfileDocument{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(input.Provider))
	protocol := llm.Protocol(strings.TrimSpace(input.Protocol))
	endpoint := strings.TrimSpace(input.Endpoint)
	if len(name) < 1 || len(name) > 100 {
		return llmProfileDocument{}, errLLMProfileInvalid
	}
	if endpoint == "" {
		switch provider {
		case "openai":
			endpoint = "https://api.openai.com/v1"
		case "xai":
			endpoint = "https://api.x.ai/v1"
		case "anthropic":
			endpoint = "https://api.anthropic.com"
		}
	}
	if protocol == "" {
		if provider == "anthropic" {
			protocol = llm.ProtocolAnthropicMessages
		} else if provider == "openai" || provider == "xai" {
			protocol = llm.ProtocolOpenAIChatCompletions
		}
	}
	key := strings.TrimSpace(input.APIKey)
	credentialOrigin, credentialReference := "managed", ""
	if current != nil {
		credentialOrigin = current.CredentialOrigin
		if credentialOrigin == "" {
			credentialOrigin = "managed"
		}
		credentialReference = current.CredentialReference
	}
	requestedReference := strings.TrimSpace(input.CredentialReference)
	if requestedReference != "" {
		if !shared || key != "" || input.ClearAPIKey || (credentialOrigin == "deployment" && requestedReference != credentialReference) {
			return llmProfileDocument{}, errLLMProfileInvalid
		}
		credentialOrigin, credentialReference = "deployment", requestedReference
	}
	if !shared && requestedReference != "" {
		return llmProfileDocument{}, errLLMProfileInvalid
	}
	if credentialOrigin == "deployment" {
		if key != "" || input.ClearAPIKey {
			return llmProfileDocument{}, errLLMProfileInvalid
		}
		credential, ok := h.byokDeploymentCredentials[credentialReference]
		if !ok || len(credential.Value) == 0 {
			return llmProfileDocument{}, errLLMProfileUnavailable
		}
		key = string(credential.Value)
	}
	if provider != "custom" && key == "" && !input.ClearAPIKey && (current == nil || !current.SecretConfigured) {
		return llmProfileDocument{}, errLLMProfileKeyRequired
	}
	resolved := llm.ProfileConfig{ID: profileID, Revision: maxInt64(1, input.Revision+1), Role: llm.ProfileRoleHeavy, Provider: provider, Protocol: protocol, Endpoint: endpoint, Model: models.Heavy}
	validationKey := key
	if validationKey == "" && current != nil && current.SecretConfigured && !input.ClearAPIKey {
		// Validate the nonsecret settings without decrypting or replacing the
		// write-only key during an ordinary metadata edit.
		validationKey = "configured-secret-placeholder"
	}
	if validationKey == "" && input.ClearAPIKey && provider != "custom" {
		validationKey = "configured-secret-placeholder"
	}
	validator, err := llm.NewProfileClient(resolved, validationKey, llm.ProfileClientOptions{})
	if err != nil {
		return llmProfileDocument{}, errLLMProfileInvalid
	}
	validator.Close()
	fastValidator := resolved
	fastValidator.Role, fastValidator.Model = llm.ProfileRoleFast, models.Fast
	validator, err = llm.NewProfileClient(fastValidator, validationKey, llm.ProfileClientOptions{})
	if err != nil {
		return llmProfileDocument{}, errLLMProfileInvalid
	}
	validator.Close()
	if current != nil && input.Revision != current.Revision {
		return llmProfileDocument{}, errLLMProfileConflict
	}
	revision := int64(1)
	if current != nil {
		revision = current.Revision + 1
	}
	secretConfigured := key != ""
	credentialRevision := int64(0)
	ciphertext := ""
	if current != nil && credentialOrigin == "managed" && key == "" && !input.ClearAPIKey {
		ciphertext = current.EncryptedAPIKey
		secretConfigured = current.SecretConfigured
		credentialRevision = current.CredentialRevision
	} else if current != nil && credentialOrigin == "deployment" && credentialReference == current.CredentialReference {
		credentialRevision = current.CredentialRevision
	}
	if input.ClearAPIKey {
		ciphertext, secretConfigured, credentialOrigin, credentialReference = "", false, "managed", ""
		credentialRevision = 0
	} else if credentialOrigin == "deployment" {
		ciphertext, secretConfigured = "", true
		if credentialRevision == 0 {
			credentialRevision = revision
		}
	} else if key != "" {
		credentialRevision = revision
		ciphertext, _ = h.byokCredentialBox.Encrypt([]byte(key), h.llmProfileAAD(owner, llmProfileDocument{ID: profileID, CredentialRevision: credentialRevision}))
		if ciphertext == "" {
			return llmProfileDocument{}, errLLMProfileUnavailable
		}
		secretConfigured = true
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	} else if current != nil {
		enabled = current.Enabled
	}
	public, priority := false, 0
	if current != nil {
		public, priority = current.Public, current.Priority
	}
	if input.Public != nil {
		public = *input.Public
	}
	if input.Priority != nil {
		priority = *input.Priority
	}
	if priority < 0 || priority > 100000 {
		return llmProfileDocument{}, errLLMProfileInvalid
	}
	return llmProfileDocument{ID: profileID, OwnerID: owner, Name: name, Provider: provider, Protocol: string(protocol), Endpoint: endpoint, Model: models.Heavy, Models: models,
		Revision: revision, CredentialRevision: credentialRevision, CredentialOrigin: credentialOrigin, CredentialReference: credentialReference,
		EncryptedAPIKey: ciphertext, SecretConfigured: secretConfigured, Enabled: enabled, Public: public, Priority: priority, UpdatedAt: time.Now().UTC()}, nil
}

func (h *Handler) requireBYOK(c *gin.Context) bool {
	if h.firestore == nil || h.firestore.Raw() == nil || h.byokCredentialBox == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "LLM profile storage is unavailable"})
		return false
	}
	return true
}

func (h *Handler) llmProfileRef(owner, profileID string, shared bool) *cloudfirestore.DocumentRef {
	collection := userLLMProfilesCollection
	if shared {
		collection = sharedLLMProfilesCollection
	}
	return scopedfirestore.Collection(h.firestore.Raw(), collection).Doc(owner + "_" + profileID)
}

func (h *Handler) userLLMProfileRef(owner, profileID string) *cloudfirestore.DocumentRef {
	return h.llmProfileRef(owner, profileID, false)
}

func (h *Handler) projectLLMBindingRef(owner, project string) *cloudfirestore.DocumentRef {
	return scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(owner, project)).Collection("llm_profile_binding").Doc("current")
}

func (h *Handler) resolveProjectLLMProfile(ctx context.Context, owner, project string) (llm.ProfileConfig, string, error) {
	if _, err := h.AuthorizeProject(ctx, owner, project, ProjectRead); err != nil {
		return llm.ProfileConfig{}, "", err
	}
	bindingSnapshot, err := h.projectLLMBindingRef(owner, project).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return llm.ProfileConfig{}, "shared", nil
	}
	if err != nil {
		return llm.ProfileConfig{}, "", err
	}
	var binding llmBindingView
	if err := bindingSnapshot.DataTo(&binding); err != nil {
		return llm.ProfileConfig{}, "", errLLMProfileUnavailable
	}
	source := h.effectiveProjectLLMSource(owner, binding.Source)
	if source == "shared" {
		return llm.ProfileConfig{}, "shared", nil
	}
	if source != "personal" || !auth.ValidPathSegment(binding.ProfileID) {
		return llm.ProfileConfig{}, "", errLLMProfileUnavailable
	}
	ref := h.userLLMProfileRef(owner, binding.ProfileID)
	snapshot, err := ref.Get(ctx)
	if err != nil {
		return llm.ProfileConfig{}, "", errLLMProfileUnavailable
	}
	var record llmProfileDocument
	if err := snapshot.DataTo(&record); err != nil || record.OwnerID != owner || !record.Enabled {
		return llm.ProfileConfig{}, "", errLLMProfileUnavailable
	}
	keyBytes, err := h.profileCredential(record, owner)
	if err != nil {
		return llm.ProfileConfig{}, "", errLLMProfileUnavailable
	}
	defer clear(keyBytes)
	key := string(keyBytes)
	models, ok := llmProfileRoleModels(record)
	if !ok {
		return llm.ProfileConfig{}, "", errLLMProfileUnavailable
	}
	return llm.ProfileConfig{ID: record.ID, Revision: record.Revision, Role: llm.ProfileRoleHeavy, Provider: record.Provider, Protocol: llm.Protocol(record.Protocol), Endpoint: record.Endpoint, Model: models.Heavy}, key, nil
}

func (h *Handler) projectLLMClient(ctx context.Context, owner, project string) (llm.ProjectChatClient, string, error) {
	clients, source, err := h.projectLLMRoleClients(ctx, owner, project)
	return clients.Fast, source, err
}

func (h *Handler) projectLLMRoleClients(ctx context.Context, owner, project string) (llm.ProjectRoleClients, string, error) {
	if h.firestore == nil || h.firestore.Raw() == nil {
		return llm.ProjectRoleClients{}, "bootstrap", nil
	}
	var operation llm.OperationProfileSnapshot
	err := h.firestore.Raw().RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		var captureErr error
		operation, captureErr = h.captureProfileOperationSnapshotTx(ctx, tx, owner, project, ProjectRead)
		return captureErr
	})
	if err != nil {
		return llm.ProjectRoleClients{}, "", err
	}
	if operation.Source == "bootstrap" {
		return llm.ProjectRoleClients{}, "bootstrap", nil
	}
	profiles, err := h.profileRecordsForOperationSnapshot(ctx, owner, operation)
	if err != nil {
		return llm.ProjectRoleClients{}, operation.Source, err
	}
	roles, err := h.profileRoleClientsForRecords(ctx, owner, operation.Source, profiles)
	return roles, operation.Source, err
}

func (h *Handler) profileRoleClientsForRecords(ctx context.Context, owner, source string, profiles []llmProfileDocument) (llm.ProjectRoleClients, error) {
	if len(profiles) == 0 {
		return llm.ProjectRoleClients{}, errLLMProfileUnavailable
	}
	if source == "personal" {
		fast, err := h.profileClientForRole(profiles[0], owner, llm.ProfileRoleFast)
		if err != nil {
			return llm.ProjectRoleClients{}, err
		}
		heavy, err := h.profileClientForRole(profiles[0], owner, llm.ProfileRoleHeavy)
		if err != nil {
			fast.Close()
			return llm.ProjectRoleClients{}, err
		}
		return llm.ProjectRoleClients{Fast: fast, Heavy: heavy}, nil
	}
	if source != "shared" {
		return llm.ProjectRoleClients{}, errLLMProfileUnavailable
	}
	build := func(role llm.ProfileRole) ([]*llm.ProfileClient, error) {
		clients := make([]*llm.ProfileClient, 0, len(profiles))
		for _, profile := range profiles {
			client, err := h.profileClientForRole(profile, "platform", role)
			if err != nil {
				for _, existing := range clients {
					existing.Close()
				}
				return nil, err
			}
			clients = append(clients, client)
		}
		return clients, nil
	}
	fastClients, err := build(llm.ProfileRoleFast)
	if err != nil {
		return llm.ProjectRoleClients{}, err
	}
	heavyClients, err := build(llm.ProfileRoleHeavy)
	if err != nil {
		for _, client := range fastClients {
			client.Close()
		}
		return llm.ProjectRoleClients{}, err
	}
	check := func(checkCtx context.Context, resolved llm.ProfileConfig) error {
		snapshot, err := scopedfirestore.Collection(h.firestore.Raw(), sharedLLMProfilesCollection).Doc("platform_" + resolved.ID).Get(checkCtx)
		if err != nil {
			return errLLMProfileChanged
		}
		var current llmProfileDocument
		if snapshot.DataTo(&current) != nil || current.OwnerID != "platform" || !current.Enabled || !current.Public {
			return errLLMProfileChanged
		}
		return nil
	}
	fast, err := llm.NewProfilePoolClient(fastClients, check)
	if err != nil {
		for _, client := range fastClients {
			client.Close()
		}
		for _, client := range heavyClients {
			client.Close()
		}
		return llm.ProjectRoleClients{}, errLLMSharedPoolUnavailable
	}
	heavy, err := llm.NewProfilePoolClient(heavyClients, check)
	if err != nil {
		fast.Close()
		for _, client := range heavyClients {
			client.Close()
		}
		return llm.ProjectRoleClients{}, errLLMSharedPoolUnavailable
	}
	return llm.ProjectRoleClients{Fast: fast, Heavy: heavy}, nil
}

func (h *Handler) sharedLLMPoolStateRef() *cloudfirestore.DocumentRef {
	return scopedfirestore.Collection(h.firestore.Raw(), sharedLLMPoolStateCollection).Doc("platform")
}

func (h *Handler) sharedLLMPoolInitialized(ctx context.Context) (bool, error) {
	if h.firestore == nil || h.firestore.Raw() == nil {
		return false, errLLMProfileUnavailable
	}
	snapshot, err := h.sharedLLMPoolStateRef().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var state sharedLLMPoolState
	if err := snapshot.DataTo(&state); err != nil || !state.Initialized {
		return false, errLLMProfileUnavailable
	}
	return true, nil
}

func (h *Handler) effectiveProjectLLMSource(owner, source string) string {
	if source == "personal" && h.isDemoUser(owner) {
		return "shared"
	}
	return source
}

func (h *Handler) profileClientFor(profile llmProfileDocument, owner string) (*llm.ProfileClient, error) {
	return h.profileClientForRole(profile, owner, llm.ProfileRoleFast)
}

func (h *Handler) profileClientForRole(profile llmProfileDocument, owner string, role llm.ProfileRole) (*llm.ProfileClient, error) {
	if role != llm.ProfileRoleFast && role != llm.ProfileRoleHeavy {
		return nil, errLLMProfileUnavailable
	}
	if h.byokCredentialBox == nil {
		return nil, errLLMProfileUnavailable
	}
	models, ok := llmProfileRoleModels(profile)
	if !ok {
		return nil, errLLMProfileUnavailable
	}
	keyBytes, err := h.profileCredential(profile, owner)
	if err != nil {
		return nil, errLLMProfileUnavailable
	}
	defer clear(keyBytes)
	key := string(keyBytes)
	model := models.Fast
	if role == llm.ProfileRoleHeavy {
		model = models.Heavy
	}
	client, err := llm.NewProfileClient(llm.ProfileConfig{ID: profile.ID, Revision: profile.Revision, Role: role, Provider: profile.Provider,
		Protocol: llm.Protocol(profile.Protocol), Endpoint: profile.Endpoint, Model: model}, key, llm.ProfileClientOptions{})
	if err != nil {
		return nil, errLLMProfileUnavailable
	}
	return client, nil
}

func viewLLMProfile(record llmProfileDocument, admin bool) llmProfileView {
	models, ok := llmProfileRoleModels(record)
	if !ok {
		models = llmProfileModels{}
	}
	view := llmProfileView{ID: record.ID, Name: record.Name, Provider: record.Provider, Protocol: record.Protocol,
		Endpoint: record.Endpoint, Model: models.Heavy, Models: models, Revision: record.Revision, SecretConfigured: record.SecretConfigured}
	if admin {
		view.Enabled, view.Public, view.Priority = record.Enabled, record.Public, record.Priority
		if record.SecretConfigured {
			view.CredentialOrigin = record.CredentialOrigin
			if view.CredentialOrigin == "" {
				view.CredentialOrigin = "managed"
			}
			if view.CredentialOrigin == "deployment" {
				view.CredentialReference = record.CredentialReference
			}
		}
	}
	return view
}

var (
	errLLMProfileInvalid               = errors.New("invalid LLM profile")
	errLLMProfileKeyRequired           = errors.New("API key is required for this provider")
	errLLMProfileNotFound              = errors.New("LLM profile not found")
	errLLMProfileConflict              = errors.New("LLM profile was changed; reload before saving")
	errLLMProfileRoleModelsRequired    = errors.New("refresh the profile before saving its role models")
	errLLMProfileInUse                 = errors.New("LLM profile is assigned to a Project")
	errLLMProfileUnavailable           = errors.New("LLM profile is unavailable")
	errLLMProfileDisabled              = errors.New("LLM profile is disabled")
	errLLMSharedPoolUnavailable        = errors.New("shared LLM pool is unavailable")
	errLLMProfileChanged               = errors.New("LLM profile availability changed")
	errLLMProfileCredentialUnavailable = errors.New("deployment credential is unavailable for the saved operation")
)

func writeLLMProfileError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errProfileProjectNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Project not found"})
	case errors.Is(err, errLLMProfileNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "LLM profile not found"})
	case errors.Is(err, errLLMProfileInUse):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, errLLMProfileConflict):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, errLLMProfileRoleModelsRequired):
		c.JSON(http.StatusConflict, gin.H{"error_code": "role_models_required", "error": err.Error()})
	case errors.Is(err, errLLMProfileCredentialUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error_code": "credential_unavailable", "error": "The saved operation cannot prove the configured deployment credential."})
	case errors.Is(err, errLLMProfileKeyRequired):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, errLLMProfileInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider, protocol, endpoint, or model"})
	case errors.Is(err, errLLMProfileDisabled):
		c.JSON(http.StatusConflict, gin.H{"error": "disabled LLM profiles cannot be assigned"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "LLM profile operation failed"})
	}
}

func newLLMProfileID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}

func (h *Handler) llmProfileAAD(owner string, profile llmProfileDocument) []byte {
	environment := strings.TrimSpace(h.byokEnvironment)
	if environment == "" {
		environment = "default"
	}
	return []byte("lwc:llm-profile:v2:" + environment + ":" + owner + ":" + profile.ID + ":" + strconv.FormatInt(profile.CredentialRevision, 10))
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
func removeString(values []string, value string) []string {
	filtered := values[:0]
	for _, item := range values {
		if item != value {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func enabledPublicProfiles(ctx context.Context, client *cloudfirestore.Client) ([]llmProfileDocument, error) {
	docs, err := scopedfirestore.Collection(client, sharedLLMProfilesCollection).Where("enabled", "==", true).Where("public", "==", true).Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	profiles := make([]llmProfileDocument, 0, len(docs))
	for _, doc := range docs {
		var profile llmProfileDocument
		if doc.DataTo(&profile) == nil && profile.Enabled && profile.Public {
			profiles = append(profiles, profile)
		}
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Priority == profiles[j].Priority {
			return profiles[i].ID < profiles[j].ID
		}
		return profiles[i].Priority < profiles[j].Priority
	})
	return profiles, nil
}
