package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/llm"
	"google.golang.org/api/idtoken"
)

const maxPipelineProfileResponseBytes = 1 << 20
const maxPipelineProfileAttempts = 32

type pipelineLLMProfile struct {
	SchemaVersion  int               `json:"schema_version"`
	SnapshotDigest string            `json:"snapshot_digest"`
	RoleMapVersion int               `json:"role_map_version"`
	ID             string            `json:"id"`
	Revision       int64             `json:"revision"`
	Provider       string            `json:"provider"`
	Protocol       llm.Protocol      `json:"protocol"`
	Endpoint       string            `json:"endpoint"`
	Model          string            `json:"model,omitempty"`
	Models         pipelineLLMModels `json:"models,omitempty"`
	APIKey         []byte            `json:"api_key,omitempty"`
}

type pipelineLLMProfileResponse struct {
	SchemaVersion  int                  `json:"schema_version"`
	SnapshotDigest string               `json:"snapshot_digest"`
	RoleMapVersion int                  `json:"role_map_version"`
	Source         string               `json:"source"`
	Profiles       []pipelineLLMProfile `json:"profiles"`
}

type pipelineLLMModels struct {
	Fast  string `json:"fast"`
	Heavy string `json:"heavy"`
}

type pipelineLLMProfileResolverRequest struct {
	ReservationID      string `json:"reservation_id"`
	UserID             string `json:"user_id"`
	ProjectID          string `json:"project_id"`
	IncludeCredentials bool   `json:"include_credentials"`
}

func resolvePipelineLLMProfiles(ctx context.Context, cfg workerConfig) ([]pipelineLLMProfile, bool, error) {
	return resolvePipelineLLMProfilesWithCredentials(ctx, cfg, true)
}

func resolvePipelineLLMProfilesWithCredentials(ctx context.Context, cfg workerConfig, includeCredentials bool) ([]pipelineLLMProfile, bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(cfg.ProfileResolverURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path != "/internal/pipeline/llm-profile" ||
		(parsed.Scheme != "https" && !(cfg.LocalCloudScope != "" && parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost"))) {
		return nil, false, errors.New("invalid Pipeline profile resolver")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if cfg.LocalCloudScope == "" {
		if cfg.ProfileResolverAudience == "" {
			return nil, false, errors.New("Pipeline profile resolver identity is unavailable")
		}
		client, err = idtoken.NewClient(ctx, cfg.ProfileResolverAudience)
		if err != nil {
			return nil, false, errors.New("Pipeline profile resolver identity is unavailable")
		}
	}
	client.Timeout = 10 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	body, err := json.Marshal(pipelineLLMProfileResolverRequest{ReservationID: cfg.PipelineReservationID, UserID: cfg.UserID, ProjectID: cfg.ProjectID, IncludeCredentials: includeCredentials})
	if err != nil {
		return nil, false, errors.New("Pipeline profile request is invalid")
	}
	defer clear(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, false, errors.New("Pipeline profile request is invalid")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, false, errors.New("Pipeline profile resolution failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false, errors.New("Pipeline profile resolution failed")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxPipelineProfileResponseBytes+1))
	if err != nil || len(raw) > maxPipelineProfileResponseBytes {
		clear(raw)
		return nil, false, errors.New("Pipeline profile response is invalid")
	}
	defer clear(raw)
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, false, errors.New("Pipeline profile response is invalid")
	}
	if _, hasVersion := envelope["schema_version"]; !hasVersion {
		legacy, legacyErr := decodeExactBaseLegacyPipelineProfileResponse(raw)
		if legacyErr != nil {
			return nil, false, legacyErr
		}
		return legacy.Profiles, legacy.Source == "shared", nil
	}
	var result pipelineLLMProfileResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		clearPipelineProfileKeys(result.Profiles)
		return nil, false, errors.New("Pipeline profile response is invalid")
	}
	if result.SchemaVersion != 2 || !validProfileSnapshotDigest(result.SnapshotDigest) || result.RoleMapVersion != 1 {
		clearPipelineProfileKeys(result.Profiles)
		return nil, false, errors.New("Pipeline profile snapshot is invalid")
	}
	switch result.Source {
	case "bootstrap":
		if len(result.Profiles) != 0 {
			clearPipelineProfileKeys(result.Profiles)
			return nil, false, errors.New("Pipeline bootstrap profile response is invalid")
		}
		return nil, false, nil
	case "personal":
		if len(result.Profiles) != 1 {
			clearPipelineProfileKeys(result.Profiles)
			return nil, false, errors.New("Pipeline personal profile response is invalid")
		}
	case "shared":
		if len(result.Profiles) == 0 {
			clearPipelineProfileKeys(result.Profiles)
			return nil, false, errors.New("Pipeline shared pool is empty")
		}
	default:
		clearPipelineProfileKeys(result.Profiles)
		return nil, false, errors.New("Pipeline profile source is invalid")
	}
	for index := range result.Profiles {
		profile := &result.Profiles[index]
		if profile.SchemaVersion == 1 {
			if profile.Model == "" || profile.Models != (pipelineLLMModels{}) || profile.SnapshotDigest != result.SnapshotDigest || profile.RoleMapVersion != result.RoleMapVersion {
				clearPipelineProfileKeys(result.Profiles)
				return nil, false, errors.New("legacy Pipeline profile is invalid")
			}
			profile.Models = pipelineLLMModels{Fast: profile.Model, Heavy: profile.Model}
			profile.Model = ""
			profile.SchemaVersion = 2
		} else if profile.SchemaVersion != 2 || profile.SnapshotDigest != result.SnapshotDigest || profile.RoleMapVersion != result.RoleMapVersion ||
			profile.Model != "" || profile.Models.Fast == "" || profile.Models.Heavy == "" {
			clearPipelineProfileKeys(result.Profiles)
			return nil, false, errors.New("Pipeline profile role models are invalid")
		}
		if err := validatePipelineLLMProfile(*profile); err != nil {
			clearPipelineProfileKeys(result.Profiles)
			return nil, false, err
		}
	}
	return result.Profiles, result.Source == "shared", nil
}

// decodeExactBaseLegacyPipelineProfileResponse accepts only the wire emitted
// by the pre-freeze Producer at the frozen source base. It does not fabricate
// schema, snapshot, or role-map metadata; shared legacy profiles therefore
// cannot use snapshot-dependent quota fallback.
func decodeExactBaseLegacyPipelineProfileResponse(raw []byte) (pipelineLLMProfileResponse, error) {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil || !exactJSONKeySet(top, "source", "profiles") {
		return pipelineLLMProfileResponse{}, errors.New("legacy Pipeline profile response is invalid")
	}
	var result struct {
		Source   string            `json:"source"`
		Profiles []json.RawMessage `json:"profiles"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return pipelineLLMProfileResponse{}, errors.New("legacy Pipeline profile response is invalid")
	}
	profiles := make([]pipelineLLMProfile, 0, len(result.Profiles))
	for _, rawProfile := range result.Profiles {
		var fields map[string]json.RawMessage
		if json.Unmarshal(rawProfile, &fields) != nil || !(exactJSONKeySet(fields, "id", "revision", "provider", "protocol", "endpoint", "model") ||
			exactJSONKeySet(fields, "id", "revision", "provider", "protocol", "endpoint", "model", "api_key")) {
			clearPipelineProfileKeys(profiles)
			return pipelineLLMProfileResponse{}, errors.New("legacy Pipeline profile is invalid")
		}
		var profile pipelineLLMProfile
		profileDecoder := json.NewDecoder(bytes.NewReader(rawProfile))
		profileDecoder.DisallowUnknownFields()
		if profileDecoder.Decode(&profile) != nil || profileDecoder.Decode(new(any)) != io.EOF ||
			strings.TrimSpace(profile.Model) == "" {
			clearPipelineProfileKeys(profiles)
			return pipelineLLMProfileResponse{}, errors.New("legacy Pipeline profile is invalid")
		}
		profile.Models = pipelineLLMModels{Fast: profile.Model, Heavy: profile.Model}
		if err := validatePipelineLLMProfile(profile); err != nil {
			clear(profile.APIKey)
			clearPipelineProfileKeys(profiles)
			return pipelineLLMProfileResponse{}, errors.New("legacy Pipeline profile is invalid")
		}
		profiles = append(profiles, profile)
	}
	if result.Source == "bootstrap" && len(profiles) != 0 || result.Source == "personal" && len(profiles) != 1 ||
		result.Source == "shared" && len(profiles) == 0 ||
		(result.Source != "bootstrap" && result.Source != "personal" && result.Source != "shared") {
		clearPipelineProfileKeys(profiles)
		return pipelineLLMProfileResponse{}, errors.New("legacy Pipeline profile source is invalid")
	}
	return pipelineLLMProfileResponse{Source: result.Source, Profiles: profiles}, nil
}

func exactJSONKeySet(fields map[string]json.RawMessage, keys ...string) bool {
	if len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func validProfileSnapshotDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

// resolveSuggestedQueriesSource resolves the Project-bound source before any
// standalone local or cloud suggested-query work begins. Only the resolver's
// never-managed bootstrap result can use the legacy platform client.
func resolveSuggestedQueriesSource(ctx context.Context, cfg workerConfig) (workerConfig, error) {
	cfg = configFromEnvironment(cfg)
	if cfg.PipelineReservationID == "" || cfg.UserID == "" || cfg.ProjectID == "" || cfg.ProfileResolverURL == "" {
		return workerConfig{}, errors.New("Project LLM source is unavailable to suggested-query generation")
	}
	profiles, shared, err := resolvePipelineLLMProfiles(ctx, cfg)
	if err != nil {
		return workerConfig{}, errors.New("Project LLM source is unavailable to suggested-query generation")
	}
	cfg.ProfileCandidates = profiles
	cfg.SharedProfilePool = shared
	cfg.BootstrapLLMSource = len(profiles) == 0 && !shared
	cfg.ActiveLLMProfile = nil
	if len(cfg.ProfileCandidates) > 0 {
		cfg.ActiveLLMProfile = &cfg.ProfileCandidates[0]
	}
	return cfg, nil
}

func validatePipelineLLMProfile(profile pipelineLLMProfile) error {
	models := normalizePipelineLLMModels(profile)
	for role, model := range map[llm.ProfileRole]string{llm.ProfileRoleFast: models.Fast, llm.ProfileRoleHeavy: models.Heavy} {
		key := string(profile.APIKey)
		if key == "" {
			// Constructor-only validation for identity-only revalidation. This value
			// is never serialized or sent to a provider.
			key = "identity-wire-validation-only"
		}
		client, err := llm.NewProfileClient(llm.ProfileConfig{ID: profile.ID, Revision: profile.Revision, Role: role, Provider: profile.Provider,
			Protocol: profile.Protocol, Endpoint: profile.Endpoint, Model: model}, key, llm.ProfileClientOptions{Timeout: 10 * time.Second})
		if err != nil {
			return errors.New("Pipeline profile configuration is invalid")
		}
		client.Close()
	}
	return nil
}

func normalizePipelineLLMModels(profile pipelineLLMProfile) pipelineLLMModels {
	if profile.Models.Fast != "" && profile.Models.Heavy != "" {
		return profile.Models
	}
	return pipelineLLMModels{Fast: profile.Model, Heavy: profile.Model}
}

func clearPipelineProfileKeys(profiles []pipelineLLMProfile) {
	for i := range profiles {
		clear(profiles[i].APIKey)
	}
}

// revalidatePipelineProfileCandidates checks current eligibility for the same
// reservation snapshot without loading credentials. It returns the originally
// pinned candidates so an eligible candidate always keeps its original model
// revision and credential bytes.
func revalidatePipelineProfileCandidates(ctx context.Context, cfg workerConfig, pinned []pipelineLLMProfile) ([]pipelineLLMProfile, error) {
	if !cfg.SharedProfilePool || len(pinned) == 0 {
		return nil, errors.New("shared Pipeline profile snapshot is unavailable")
	}
	byID := make(map[string]pipelineLLMProfile, len(pinned))
	for _, profile := range pinned {
		if profile.ID == "" || !validProfileSnapshotDigest(profile.SnapshotDigest) || profile.RoleMapVersion != 1 {
			return nil, errors.New("shared Pipeline profile snapshot identity is invalid")
		}
		byID[profile.ID] = profile
	}
	current, shared, err := resolvePipelineLLMProfilesWithCredentials(ctx, cfg, false)
	defer clearPipelineProfileKeys(current)
	if err != nil || !shared {
		return nil, errors.New("shared Pipeline profile eligibility could not be revalidated")
	}
	eligible := make(map[string]struct{}, len(current))
	for _, identity := range current {
		profile, ok := byID[identity.ID]
		if !ok || !samePipelineProfileIdentity(profile, identity) || len(identity.APIKey) != 0 {
			return nil, errors.New("shared Pipeline profile identity changed during fallback")
		}
		eligible[identity.ID] = struct{}{}
	}
	result := make([]pipelineLLMProfile, 0, len(eligible))
	for _, profile := range pinned {
		if _, ok := eligible[profile.ID]; ok {
			result = append(result, profile)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("shared Pipeline profile pool has no currently eligible candidates")
	}
	return result, nil
}

func samePipelineProfileIdentity(left, right pipelineLLMProfile) bool {
	return left.ID == right.ID && left.Revision == right.Revision && left.Provider == right.Provider &&
		left.Protocol == right.Protocol && left.Endpoint == right.Endpoint && left.Models == right.Models &&
		left.SnapshotDigest == right.SnapshotDigest && left.RoleMapVersion == right.RoleMapVersion &&
		left.SchemaVersion == right.SchemaVersion
}

func runWorkerBatchAtVaultWithProfilePool(ctx context.Context, cfg workerConfig, commands [][]string, vault, workspace string, snapshots []sourceSnapshot, materialize bool) (string, error) {
	pinnedProfiles := cfg.ProfileCandidates
	profiles := pinnedProfiles
	defer func() { clearPipelineProfileKeys(pinnedProfiles) }()
	if !cfg.SharedProfilePool || len(profiles) == 0 {
		if len(profiles) > 0 {
			cfg.ActiveLLMProfile = &profiles[0]
		}
		err := localWorkspaceBatchHooks.runWorkerBatchAtVault(ctx, cfg, commands, workspace)
		return workspace, err
	}
	attempted := make(map[string]struct{}, len(profiles))
	index := 0
	for attempt := 0; attempt < maxPipelineProfileAttempts; attempt++ {
		if index >= len(profiles) {
			return workspace, errors.New("shared Pipeline profile pool exhausted")
		}
		cfg.ProfileCandidates = profiles
		cfg.ActiveLLMProfile = &profiles[index]
		attempted[profiles[index].ID] = struct{}{}
		err := localWorkspaceBatchHooks.runWorkerBatchAtVault(ctx, cfg, commands, workspace)
		if err == nil {
			return workspace, nil
		}
		if !pipelineProfileQuotaMarkerExists(cfg) {
			return workspace, err
		}
		quotaErr := err
		if pipelineProfileWorkOrAttemptMarkerExists(cfg) {
			return workspace, errors.Join(quotaErr, errors.New("shared Pipeline quota fallback stopped because no-replay safety was not proven"))
		}
		if attempt+1 >= maxPipelineProfileAttempts {
			return workspace, errors.Join(quotaErr, errors.New("shared Pipeline profile attempt limit reached"))
		}
		// Eligibility may have changed after the initial credential-bearing
		// resolution. Revalidate identity-only against this reservation's pinned
		// snapshot, then retain the original models and keys for eligible IDs.
		profiles, err = revalidatePipelineProfileCandidates(ctx, cfg, pinnedProfiles)
		if err != nil {
			return workspace, errors.Join(quotaErr, err, errors.New("shared Pipeline profile fallback stopped because current eligibility was not proven"))
		}
		index = -1
		for candidateIndex := range profiles {
			if _, alreadyAttempted := attempted[profiles[candidateIndex].ID]; !alreadyAttempted {
				index = candidateIndex
				break
			}
		}
		if index < 0 {
			return workspace, errors.Join(quotaErr, errors.New("shared Pipeline profile pool has no untried currently eligible candidate"))
		}
		if removeErr := localWorkspaceBatchHooks.removeWorkspace(workspace); removeErr != nil {
			return "", errors.Join(err, fmt.Errorf("remove quota-exhausted Synto workspace: %w", removeErr))
		}
		for _, marker := range []string{cfg.QuotaMarkerPath, cfg.ProfileCallSuccessMarkerPath, cfg.ProfileCallAttemptMarkerPath} {
			if marker != "" {
				if removeErr := os.Remove(marker); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					return "", errors.Join(err, fmt.Errorf("clear Synto profile marker: %w", removeErr))
				}
			}
		}
		workspace, err = localWorkspaceBatchHooks.createWorkspace(cfg.WorkspaceDir, vault)
		if err != nil {
			return "", errors.Join(err, errors.New("recreate clean Synto workspace after explicit shared quota exhaustion"))
		}
		if materialize {
			if err := localWorkspaceBatchHooks.materializeSnapshots(workspace, snapshots); err != nil {
				_ = localWorkspaceBatchHooks.removeWorkspace(workspace)
				return "", err
			}
		}
	}
	return workspace, errors.New("shared Pipeline profile pool exhausted")
}

// runCloudWorkerBatchAtVaultWithProfilePool keeps the materialized cloud
// workspace as an immutable seed. Each profile attempt runs in a private copy;
// only an explicit account-quota marker can advance a shared pool to the next
// profile, and the retry starts from the same clean seed.
func runCloudWorkerBatchAtVaultWithProfilePool(ctx context.Context, cfg workerConfig, commands [][]string, workspace string) (string, error) {
	if len(cfg.ProfileCandidates) == 0 {
		return workspace, runWorkerBatchAtVault(ctx, cfg, commands, workspace)
	}
	parent := filepath.Dir(workspace)
	seed, err := localWorkspaceBatchHooks.createWorkspace(parent, workspace)
	if err != nil {
		return workspace, fmt.Errorf("copy cloud workspace for Project profile execution: %w", err)
	}
	defer func() { _ = localWorkspaceBatchHooks.removeWorkspace(seed) }()
	attempt, err := localWorkspaceBatchHooks.createWorkspace(parent, seed)
	if err != nil {
		return workspace, fmt.Errorf("create Project profile execution workspace: %w", err)
	}
	result, runErr := runWorkerBatchAtVaultWithProfilePool(ctx, cfg, commands, seed, attempt, nil, false)
	if result == "" {
		return workspace, runErr
	}
	return result, runErr
}

func pipelineProfileQuotaMarkerExists(cfg workerConfig) bool {
	if !cfg.SharedProfilePool || cfg.QuotaMarkerPath == "" {
		return false
	}
	info, err := os.Lstat(cfg.QuotaMarkerPath)
	return err == nil && info.Mode().IsRegular() && info.Size() == 0
}

// An unset marker path or unreadable marker state fails closed. The monotonic
// worked marker prevents replay after any successful generation; a remaining
// attempt marker means an interrupted or non-quota request may have worked.
func pipelineProfileWorkOrAttemptMarkerExists(cfg workerConfig) bool {
	if cfg.ProfileCallSuccessMarkerPath == "" || cfg.ProfileCallAttemptMarkerPath == "" {
		return true
	}
	for _, marker := range []string{cfg.ProfileCallSuccessMarkerPath, cfg.ProfileCallAttemptMarkerPath} {
		_, err := os.Lstat(marker)
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}
