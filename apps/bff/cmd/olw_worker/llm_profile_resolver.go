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
	body, err := json.Marshal(pipelineLLMProfileResolverRequest{ReservationID: cfg.PipelineReservationID, UserID: cfg.UserID, ProjectID: cfg.ProjectID, IncludeCredentials: true})
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
	var result struct {
		SchemaVersion  int                  `json:"schema_version"`
		SnapshotDigest string               `json:"snapshot_digest"`
		RoleMapVersion int                  `json:"role_map_version"`
		Source         string               `json:"source"`
		Profiles       []pipelineLLMProfile `json:"profiles"`
	}
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
		client, err := llm.NewProfileClient(llm.ProfileConfig{ID: profile.ID, Revision: profile.Revision, Role: role, Provider: profile.Provider,
			Protocol: profile.Protocol, Endpoint: profile.Endpoint, Model: model}, string(profile.APIKey), llm.ProfileClientOptions{Timeout: 10 * time.Second})
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

func runWorkerBatchAtVaultWithProfilePool(ctx context.Context, cfg workerConfig, commands [][]string, vault, workspace string, snapshots []sourceSnapshot, materialize bool) (string, error) {
	profiles := cfg.ProfileCandidates
	defer func() { clearPipelineProfileKeys(profiles) }()
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
		if pipelineProfileCallSuccessMarkerExists(cfg) {
			return workspace, errors.Join(err, errors.New("shared Pipeline quota fallback stopped because no-replay safety was not proven"))
		}
		if attempt+1 >= maxPipelineProfileAttempts {
			return workspace, errors.Join(err, errors.New("shared Pipeline profile attempt limit reached"))
		}
		// The resolver response pins the binding, shared pool and profile revisions
		// for this reservation. Keep this immutable candidate set for pre-work
		// quota fallback; a resumed worker resolves the same reservation again.
		index = -1
		for candidateIndex := range profiles {
			if _, alreadyAttempted := attempted[profiles[candidateIndex].ID]; !alreadyAttempted {
				index = candidateIndex
				break
			}
		}
		if index < 0 {
			return workspace, err
		}
		if removeErr := localWorkspaceBatchHooks.removeWorkspace(workspace); removeErr != nil {
			return "", errors.Join(err, fmt.Errorf("remove quota-exhausted Synto workspace: %w", removeErr))
		}
		for _, marker := range []string{cfg.QuotaMarkerPath, cfg.ProfileCallSuccessMarkerPath} {
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

// An unset marker path or unreadable marker state fails closed: quota fallback
// replays the Synto command only when no profile response succeeded.
func pipelineProfileCallSuccessMarkerExists(cfg workerConfig) bool {
	if cfg.ProfileCallSuccessMarkerPath == "" {
		return true
	}
	_, err := os.Lstat(cfg.ProfileCallSuccessMarkerPath)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}
