package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const profileRoleMapVersion = 1

func (h *Handler) captureProfileOperationSnapshotTx(ctx context.Context, tx *cloudfirestore.Transaction, owner, project string, action ProjectAction) (llm.OperationProfileSnapshot, error) {
	projectRef := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(owner, project))
	projectSnapshot, err := tx.Get(projectRef)
	if err != nil || authorizeProjectDocument(projectRef.ID, projectSnapshot.Data(), owner, project, action) != nil {
		return llm.OperationProfileSnapshot{}, errProfileProjectNotFound
	}

	binding := llmBindingView{Source: "shared"}
	bindingSnapshot, err := tx.Get(h.projectLLMBindingRef(owner, project))
	if err == nil {
		if bindingSnapshot.DataTo(&binding) != nil || (binding.Source != "shared" && binding.Source != "personal") {
			return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		}
	} else if status.Code(err) != codes.NotFound {
		return llm.OperationProfileSnapshot{}, err
	}
	source := h.effectiveProjectLLMSource(owner, binding.Source)
	profiles := make([]llmProfileDocument, 0)
	switch source {
	case "personal":
		if !auth.ValidPathSegment(binding.ProfileID) {
			return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		}
		profileSnapshot, err := tx.Get(h.userLLMProfileRef(owner, binding.ProfileID))
		if err != nil {
			return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		}
		var profile llmProfileDocument
		if profileSnapshot.DataTo(&profile) != nil || profile.OwnerID != owner || !profile.Enabled {
			return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		}
		profiles = append(profiles, profile)
	case "shared":
		query := scopedfirestore.Collection(h.firestore.Raw(), sharedLLMProfilesCollection).
			Where("enabled", "==", true).Where("public", "==", true)
		docs, err := tx.Documents(query).GetAll()
		if err != nil {
			return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		}
		for _, doc := range docs {
			var profile llmProfileDocument
			if doc.DataTo(&profile) != nil || profile.OwnerID != "platform" || !profile.Enabled || !profile.Public {
				return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
			}
			profiles = append(profiles, profile)
		}
		sort.Slice(profiles, func(i, j int) bool {
			if profiles[i].Priority == profiles[j].Priority {
				return profiles[i].ID < profiles[j].ID
			}
			return profiles[i].Priority < profiles[j].Priority
		})
		if len(profiles) == 0 {
			state, stateErr := tx.Get(h.sharedLLMPoolStateRef())
			if stateErr == nil {
				var pool sharedLLMPoolState
				if state.DataTo(&pool) != nil {
					return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
				}
				if pool.Initialized {
					return llm.OperationProfileSnapshot{}, errLLMSharedPoolUnavailable
				}
			} else if status.Code(stateErr) != codes.NotFound {
				return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
			}
			source = "bootstrap"
		}
	default:
		return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
	}

	snapshot := llm.OperationProfileSnapshot{
		Version: 1, Source: source, BindingRevision: binding.Revision,
		BindingProfileID: binding.ProfileID, RoleMapVersion: profileRoleMapVersion,
		Profiles: make([]llm.ProfileRevisionReference, 0, len(profiles)),
	}
	profileRefs := make([]*cloudfirestore.DocumentRef, len(profiles))
	missingRevisionSnapshots := make([]int, 0, len(profiles))
	for index, profile := range profiles {
		if _, ok := llmProfileRoleModels(profile); !ok || profile.Revision < 1 {
			return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		}
		shared := profile.OwnerID == "platform"
		profileRef := h.llmProfileRef(profile.OwnerID, profile.ID, shared)
		profileRefs[index] = profileRevisionRef(profileRef, profile.Revision)
		prior, readErr := tx.Get(profileRefs[index])
		if status.Code(readErr) == codes.NotFound {
			missingRevisionSnapshots = append(missingRevisionSnapshots, index)
		} else if readErr != nil {
			return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		} else {
			var immutable llmProfileDocument
			if prior.DataTo(&immutable) != nil || !sameProfileRevision(immutable, profile) {
				return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
			}
		}
		snapshot.Profiles = append(snapshot.Profiles, profileRevisionReference(profile))
	}
	sealProfileOperationSnapshot(&snapshot)
	for _, index := range missingRevisionSnapshots {
		if err := tx.Create(profileRefs[index], profiles[index]); err != nil {
			return llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		}
	}
	return snapshot, nil
}

func (h *Handler) pinPipelineProfileSnapshot(ctx context.Context, reservationID, owner, project string) (llm.OperationProfileSnapshot, bool, error) {
	ref := scopedfirestore.Collection(h.firestore.Raw(), "pipeline_quota_reservations").Doc(reservationID)
	var snapshot llm.OperationProfileSnapshot
	created := false
	err := h.firestore.Raw().RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		created = false
		reservation, err := tx.Get(ref)
		if err != nil {
			return errLLMProfileUnavailable
		}
		data := reservation.Data()
		if data["user_id"] != owner || data["project_id"] != project || data["settlement_status"] != "pending" {
			return errLLMProfileUnavailable
		}
		if raw := data["profile_snapshot"]; raw != nil {
			if snapshotFromFirestore(raw, &snapshot) != nil || !validProfileOperationSnapshot(snapshot) {
				return errLLMProfileUnavailable
			}
			return nil
		}
		snapshot, err = h.captureProfileOperationSnapshotTx(ctx, tx, owner, project, ProjectRead)
		if err != nil {
			return err
		}
		if err := tx.Update(ref, []cloudfirestore.Update{{Path: "profile_snapshot", Value: snapshot}}); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return llm.OperationProfileSnapshot{}, false, err
	}
	return snapshot, created, nil
}

func (h *Handler) pinProfileRuntimeSnapshot(ctx context.Context, work profileruntime.Work) (llm.OperationProfileSnapshot, bool, error) {
	ref := scopedfirestore.Collection(h.firestore.Raw(), profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	var snapshot llm.OperationProfileSnapshot
	created := false
	err := h.firestore.Raw().RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		created = false
		stored, err := tx.Get(ref)
		if err != nil {
			return errProfileRuntimeLease
		}
		var current profileruntime.Work
		if stored.DataTo(&current) != nil || current.Token != work.Token || current.Status != "running" {
			return errProfileRuntimeLease
		}
		if current.ProfileSnapshot.Version > 0 {
			snapshot = current.ProfileSnapshot
			if !validProfileOperationSnapshot(snapshot) {
				return errLLMProfileUnavailable
			}
			return nil
		}
		snapshot, err = h.captureProfileOperationSnapshotTx(ctx, tx, work.UserID, work.ProjectID, ProjectEdit)
		if err != nil {
			return err
		}
		current.ProfileSnapshot = snapshot
		if err := tx.Set(ref, current); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return llm.OperationProfileSnapshot{}, false, err
	}
	return snapshot, created, nil
}

func (h *Handler) markPipelineProfileCredentialUsed(ctx context.Context, reservationID, owner, project string, expected llm.OperationProfileSnapshot) (bool, error) {
	ref := scopedfirestore.Collection(h.firestore.Raw(), "pipeline_quota_reservations").Doc(reservationID)
	previouslyUsed := false
	err := h.firestore.Raw().RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		reservation, err := tx.Get(ref)
		if err != nil {
			return errLLMProfileUnavailable
		}
		data := reservation.Data()
		if data["user_id"] != owner || data["project_id"] != project {
			return errLLMProfileUnavailable
		}
		var current llm.OperationProfileSnapshot
		if snapshotFromFirestore(data["profile_snapshot"], &current) != nil || current.ConfigDigest != expected.ConfigDigest {
			return errLLMProfileUnavailable
		}
		previouslyUsed = current.CredentialUsed
		if current.CredentialUsed {
			return nil
		}
		if err := tx.Update(ref, []cloudfirestore.Update{{Path: "profile_snapshot.credential_used", Value: true}}); err != nil {
			return err
		}
		return nil
	})
	return previouslyUsed, err
}

func (h *Handler) markProfileRuntimeCredentialUsed(ctx context.Context, work profileruntime.Work, expected llm.OperationProfileSnapshot) (bool, error) {
	ref := scopedfirestore.Collection(h.firestore.Raw(), profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	previouslyUsed := false
	err := h.firestore.Raw().RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		stored, err := tx.Get(ref)
		if err != nil {
			return errProfileRuntimeLease
		}
		var current profileruntime.Work
		if stored.DataTo(&current) != nil || current.Token != work.Token || current.ProfileSnapshot.ConfigDigest != expected.ConfigDigest {
			return errProfileRuntimeLease
		}
		previouslyUsed = current.ProfileSnapshot.CredentialUsed
		if previouslyUsed {
			return nil
		}
		if err := tx.Update(ref, []cloudfirestore.Update{{Path: "llm_profile_snapshot.credential_used", Value: true}}); err != nil {
			return err
		}
		return nil
	})
	return previouslyUsed, err
}

func (h *Handler) profileRecordsForOperationSnapshot(ctx context.Context, owner string, snapshot llm.OperationProfileSnapshot) ([]llmProfileDocument, error) {
	if !validProfileOperationSnapshot(snapshot) {
		return nil, errLLMProfileUnavailable
	}
	if snapshot.Source == "bootstrap" {
		return nil, nil
	}
	profiles := make([]llmProfileDocument, 0, len(snapshot.Profiles))
	for _, identity := range snapshot.Profiles {
		if identity.OwnerID == "" || identity.ID == "" || identity.Revision < 1 {
			return nil, errLLMProfileUnavailable
		}
		shared := identity.OwnerID == "platform"
		if (snapshot.Source == "personal" && (shared || identity.OwnerID != owner)) || (snapshot.Source == "shared" && !shared) {
			return nil, errLLMProfileUnavailable
		}
		currentSnapshot, err := h.llmProfileRef(identity.OwnerID, identity.ID, shared).Get(ctx)
		if err != nil {
			if shared && status.Code(err) == codes.NotFound {
				continue
			}
			return nil, errLLMProfileUnavailable
		}
		var current llmProfileDocument
		if currentSnapshot.DataTo(&current) != nil || current.OwnerID != identity.OwnerID {
			return nil, errLLMProfileUnavailable
		}
		if snapshot.Source == "personal" {
			if !current.Enabled {
				return nil, errLLMProfileUnavailable
			}
		} else if !current.Enabled || !current.Public {
			continue
		}
		revisionSnapshot, err := profileRevisionRef(h.llmProfileRef(identity.OwnerID, identity.ID, shared), identity.Revision).Get(ctx)
		if err != nil {
			return nil, errLLMProfileUnavailable
		}
		var immutable llmProfileDocument
		if revisionSnapshot.DataTo(&immutable) != nil || !sameProfileRevisionReference(profileRevisionReference(immutable), identity) {
			return nil, errLLMProfileUnavailable
		}
		if _, ok := llmProfileRoleModels(immutable); !ok {
			return nil, errLLMProfileUnavailable
		}
		profiles = append(profiles, immutable)
	}
	if snapshot.Source == "personal" && len(profiles) != 1 {
		return nil, errLLMProfileUnavailable
	}
	if snapshot.Source == "shared" && len(profiles) == 0 {
		return nil, errLLMSharedPoolUnavailable
	}
	return profiles, nil
}

func (h *Handler) resolvePipelineProfileOperation(ctx context.Context, reservationID, owner, project string, includeCredentials bool) (string, []pipelineLLMProfile, error) {
	source, profiles, _, err := h.resolvePipelineProfileOperationWire(ctx, reservationID, owner, project, includeCredentials)
	return source, profiles, err
}

func (h *Handler) resolvePipelineProfileOperationWire(ctx context.Context, reservationID, owner, project string, includeCredentials bool) (string, []pipelineLLMProfile, llm.OperationProfileSnapshot, error) {
	snapshot, _, err := h.pinPipelineProfileSnapshot(ctx, reservationID, owner, project)
	if err != nil {
		return "", nil, llm.OperationProfileSnapshot{}, err
	}
	if snapshot.Source == "bootstrap" {
		return "bootstrap", nil, snapshot, nil
	}
	profiles, err := h.profileRecordsForOperationSnapshot(ctx, owner, snapshot)
	if err != nil {
		return "", nil, llm.OperationProfileSnapshot{}, err
	}
	if includeCredentials && profileRecordsHaveCredential(profiles) {
		previouslyUsed, markErr := h.markPipelineProfileCredentialUsed(ctx, reservationID, owner, project, snapshot)
		if markErr != nil {
			return "", nil, llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		}
		if previouslyUsed && profileRecordsHaveDeploymentCredential(profiles) {
			return "", nil, llm.OperationProfileSnapshot{}, errLLMProfileCredentialUnavailable
		}
	}
	result := make([]pipelineLLMProfile, 0, len(profiles))
	for _, profile := range profiles {
		models, ok := llmProfileRoleModels(profile)
		if !ok {
			clearPipelineProfileAPIKeys(result)
			return "", nil, llm.OperationProfileSnapshot{}, errLLMProfileUnavailable
		}
		item := pipelineLLMProfile{SchemaVersion: 2, SnapshotDigest: snapshot.ConfigDigest, RoleMapVersion: snapshot.RoleMapVersion,
			ID: profile.ID, Revision: profile.Revision, Provider: profile.Provider,
			Protocol: llm.Protocol(profile.Protocol), Endpoint: profile.Endpoint, Models: models}
		if includeCredentials {
			key, keyErr := h.profileCredential(profile, profile.OwnerID)
			if keyErr != nil {
				clearPipelineProfileAPIKeys(result)
				return "", nil, llm.OperationProfileSnapshot{}, errLLMProfileCredentialUnavailable
			}
			item.APIKey = key
		}
		result = append(result, item)
	}
	return snapshot.Source, result, snapshot, nil
}

func profileRecordsHaveCredential(profiles []llmProfileDocument) bool {
	for _, profile := range profiles {
		if profile.SecretConfigured {
			return true
		}
	}
	return false
}

func profileRecordsHaveDeploymentCredential(profiles []llmProfileDocument) bool {
	for _, profile := range profiles {
		if profile.SecretConfigured && profile.CredentialOrigin == "deployment" {
			return true
		}
	}
	return false
}

func clearPipelineProfileAPIKeys(profiles []pipelineLLMProfile) {
	for i := range profiles {
		clear(profiles[i].APIKey)
	}
}

func sameProfileRevisionReference(left, right llm.ProfileRevisionReference) bool {
	return left.OwnerID == right.OwnerID && left.ID == right.ID && left.Revision == right.Revision &&
		left.CredentialRevision == right.CredentialRevision && left.CredentialOrigin == right.CredentialOrigin &&
		left.CredentialReference == right.CredentialReference && left.SecretConfigured == right.SecretConfigured && left.ConfigDigest == right.ConfigDigest
}

func profileRevisionReference(profile llmProfileDocument) llm.ProfileRevisionReference {
	origin := strings.TrimSpace(profile.CredentialOrigin)
	if origin == "" {
		origin = "managed"
	}
	return llm.ProfileRevisionReference{
		OwnerID: profile.OwnerID, ID: profile.ID, Revision: profile.Revision,
		CredentialRevision: profile.CredentialRevision, CredentialOrigin: origin,
		CredentialReference: profile.CredentialReference, SecretConfigured: profile.SecretConfigured,
		ConfigDigest: profileConfigurationDigest(profile),
	}
}

func profileConfigurationDigest(profile llmProfileDocument) string {
	models, _ := llmProfileRoleModels(profile)
	origin := strings.TrimSpace(profile.CredentialOrigin)
	if origin == "" {
		origin = "managed"
	}
	data, _ := json.Marshal(struct {
		OwnerID             string           `json:"owner_id"`
		ID                  string           `json:"id"`
		Revision            int64            `json:"revision"`
		Name                string           `json:"name"`
		Provider            string           `json:"provider"`
		Protocol            string           `json:"protocol"`
		Endpoint            string           `json:"endpoint"`
		Models              llmProfileModels `json:"models"`
		CredentialRevision  int64            `json:"credential_revision"`
		CredentialOrigin    string           `json:"credential_origin"`
		CredentialReference string           `json:"credential_reference"`
		SecretConfigured    bool             `json:"secret_configured"`
		Enabled             bool             `json:"enabled"`
		Public              bool             `json:"public"`
		Priority            int              `json:"priority"`
	}{profile.OwnerID, profile.ID, profile.Revision, profile.Name, profile.Provider, profile.Protocol, profile.Endpoint, models,
		profile.CredentialRevision, origin, profile.CredentialReference, profile.SecretConfigured, profile.Enabled, profile.Public, profile.Priority})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sealProfileOperationSnapshot(snapshot *llm.OperationProfileSnapshot) {
	if snapshot.Source == "bootstrap" && snapshot.Profiles == nil {
		// Firestore may decode an empty array as a nil slice. Preserve the
		// producer's canonical [] digest for the explicitly pinned bootstrap
		// source when a durable operation is resumed.
		snapshot.Profiles = []llm.ProfileRevisionReference{}
	}
	snapshot.ConfigDigest = ""
	snapshot.CredentialUsed = false
	data, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(data)
	snapshot.ConfigDigest = hex.EncodeToString(sum[:])
}

func validProfileOperationSnapshot(snapshot llm.OperationProfileSnapshot) bool {
	if snapshot.Version != 1 || snapshot.RoleMapVersion != profileRoleMapVersion ||
		(snapshot.Source != "personal" && snapshot.Source != "shared" && snapshot.Source != "bootstrap") {
		return false
	}
	if (snapshot.Source == "personal" && len(snapshot.Profiles) != 1) ||
		(snapshot.Source == "shared" && len(snapshot.Profiles) == 0) ||
		(snapshot.Source == "bootstrap" && len(snapshot.Profiles) != 0) {
		return false
	}
	seen := make(map[string]struct{}, len(snapshot.Profiles))
	for _, profile := range snapshot.Profiles {
		if profile.OwnerID == "" || profile.ID == "" || profile.Revision < 1 || profile.ConfigDigest == "" {
			return false
		}
		key := profile.OwnerID + "/" + profile.ID
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	original := snapshot.ConfigDigest
	sealProfileOperationSnapshot(&snapshot)
	return original != "" && snapshot.ConfigDigest == original
}

func snapshotFromFirestore(value any, target *llm.OperationProfileSnapshot) error {
	if value == nil || target == nil {
		return errors.New("profile snapshot is missing")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func sameProfileRevision(left, right llmProfileDocument) bool {
	return profileConfigurationDigest(left) == profileConfigurationDigest(right) && left.EncryptedAPIKey == right.EncryptedAPIKey
}
