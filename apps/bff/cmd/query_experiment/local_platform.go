package main

// Local platform operations reuse the existing generation reader, strict cases,
// sealed runtime config, production executor and secure snapshot reader.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/queryconfig"
	"github.com/rayer/llm-wiki-bff/internal/queryquality"
	"github.com/rayer/llm-wiki-bff/internal/queryruntime"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/storage"
)

type localRequest struct {
	Profile     string                     `json:"profile"`
	Prompt      string                     `json:"prompt"`
	Operation   string                     `json:"operation"`
	Snapshot    string                     `json:"snapshot"`
	Destination string                     `json:"destination"`
	Config      string                     `json:"config"`
	Cases       string                     `json:"cases"`
	Project     string                     `json:"project"`
	Generation  string                     `json:"generation"`
	Stage       int                        `json:"stage"`
	Saved       map[string]localCaseResult `json:"saved"`
}
type localCaseResult struct {
	Replay     queryquality.StageReplay     `json:"replay"`
	Result     query.Result                 `json:"result"`
	Status     string                       `json:"status"`
	Identity   *query.RuntimeConfigIdentity `json:"runtime_config_identity,omitempty"`
	Diagnostic string                       `json:"diagnostic,omitempty"`
}
type localIdentityReader struct {
	cache.Reader
	identity storage.QueryGenerationIdentity
}

func (r localIdentityReader) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return r.Reader.(interface {
		ReadFile(context.Context, string) ([]byte, error)
	}).ReadFile(ctx, path)
}

func (r localIdentityReader) QueryGenerationIdentity(context.Context) (storage.QueryGenerationIdentity, error) {
	return r.identity, nil
}

func localPlatformMain(in io.Reader, out io.Writer) error {
	var req localRequest
	dec := json.NewDecoder(io.LimitReader(in, 64<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return errors.New("invalid local request")
	}
	if err := ensureJSONEOF(dec); err != nil {
		return errors.New("invalid local request")
	}
	return executeLocalPlatform(context.Background(), req, out)
}

func executeLocalPlatform(ctx context.Context, req localRequest, out io.Writer) error {
	if req.Operation == "import" {
		parsed, err := parseGCSProjectRoot(req.Snapshot)
		if err != nil {
			return err
		}
		client, err := gcs.NewClient(parsed.bucket)
		if err != nil {
			return errors.New("GCS client unavailable")
		}
		defer client.Close()
		pinned, snapshot, err := client.WithScope(parsed.userID, parsed.project).PinCurrentGeneration(ctx)
		if err != nil {
			return errors.New("published generation unavailable or invalid; legacy and historical locators are unsupported")
		}
		if err = materializePinnedSnapshot(ctx, pinned, snapshot, req.Destination); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(snapshot)
	}
	cfg, err := queryconfig.LoadFile(req.Config)
	if err != nil {
		return errors.New("invalid sealed query configuration")
	}
	if req.Operation == "validate-profile" || req.Operation == "bind-config" {
		var selected *queryconfig.Profile
		for i := range cfg.Profiles {
			if cfg.Profiles[i].ID == req.Profile {
				selected = &cfg.Profiles[i]
			}
		}
		prompt, ok := queryquality.LookupPrompt(req.Prompt)
		if selected == nil || !ok {
			return errors.New("unknown existing query profile or prompt")
		}
		if req.Operation == "validate-profile" {
			return json.NewEncoder(out).Encode(map[string]string{"status": "valid"})
		}
		prepared, err := preflightSnapshot(ctx, req.Snapshot)
		if err != nil {
			return errors.New("invalid profile binding snapshot")
		}
		binding := queryconfig.ProjectBinding{ProjectID: req.Project, GenerationID: req.Generation, ConceptsDigest: "sha256:" + prepared.digest, ProfileID: selected.ID, ProfileDigest: selected.ProfileDigest, PromptID: prompt.ID, PromptDigest: prompt.TemplateDigest, Source: queryconfig.SourceCorpusDerivedApproximation}
		found := false
		for _, old := range cfg.ProjectBindings {
			if old.ProjectID == req.Project && old.GenerationID == req.Generation {
				if old != binding {
					return errors.New("existing generation binding conflicts with explicit selection")
				}
				found = true
			}
		}
		if !found {
			cfg.ProjectBindings = append(cfg.ProjectBindings, binding)
		}
		cfg.ConfigDigest = ""
		sealed, err := queryconfig.Seal(cfg)
		if err != nil {
			return errors.New("invalid exact query profile binding")
		}
		return json.NewEncoder(out).Encode(sealed)
	}
	if req.Operation == "validate-config" {
		return json.NewEncoder(out).Encode(map[string]string{"config_digest": cfg.ConfigDigest})
	}
	if req.Operation == "validate-snapshot" || req.Operation == "suggested-cases" {
		prepared, err := preflightSnapshot(ctx, req.Snapshot)
		if err != nil {
			return errors.New("invalid snapshot corpus")
		}
		entries, err := prepared.cache.All(ctx, prepared.reader)
		if err != nil || len(entries) == 0 {
			return errors.New("snapshot has no concepts")
		}
		for _, entry := range entries {
			if _, _, err := prepared.reader.GetPage(ctx, entry.Slug, "concepts"); err != nil {
				return errors.New("snapshot missing concept page")
			}
		}
		if req.Operation == "suggested-cases" {
			cases, err := suggestedCases(prepared.suggestedData, "wiki", nil)
			if err != nil {
				return errors.New("invalid saved suggested queries")
			}
			values := []map[string]string{}
			for _, c := range cases {
				values = append(values, map[string]string{"id": c.ID, "query": c.Query, "mode": c.Mode})
			}
			return json.NewEncoder(out).Encode(values)
		}
		return json.NewEncoder(out).Encode(map[string]int{"concepts": len(entries)})
	}
	cases, err := readCases(req.Cases)
	if err != nil {
		return errors.New("invalid strict query cases")
	}
	if req.Operation == "validate" {
		return json.NewEncoder(out).Encode(map[string]any{"config_digest": cfg.ConfigDigest, "cases": len(cases)})
	}
	if req.Operation != "query" {
		return errors.New("unknown local operation")
	}
	prepared, err := preflightSnapshot(ctx, req.Snapshot)
	if err != nil {
		return errors.New("invalid local query snapshot")
	}
	identity := storage.QueryGenerationIdentity{ProjectID: req.Project, GenerationID: req.Generation, ConceptsDigest: "sha256:" + prepared.digest}
	reader := localIdentityReader{prepared.reader, identity}
	expansion := cfg.Stages.QueryExpander
	synthesis := cfg.Stages.AnswerSynthesizer
	// The key is read only at execution; it is never in requests or receipts.
	key := os.Getenv("DEEPSEEK_API_KEY")
	if (req.Stage == 70 || req.Stage == 100) && key == "" {
		return errors.New("DEEPSEEK_API_KEY is required for live query inference")
	}
	// Identity validation also applies to provider-free replay. The placeholder is
	// never called in those stages; production clients have no eager network I/O.
	if key == "" {
		key = "unused-replay-identity"
	}
	exp := llm.NewClientWithOptions(key, llm.ClientOptions{Model: expansion.Model, Reasoning: llm.Reasoning(expansion.Reasoning), Temperature: &expansion.Temperature})
	syn := llm.NewClientWithOptions(key, llm.ClientOptions{Model: synthesis.Model, Reasoning: llm.Reasoning(synthesis.Reasoning), Temperature: &synthesis.Temperature})
	executor, err := queryruntime.NewExecutor(cfg, prepared.cache, exp, nil, query.NewService(prepared.cache, nil, syn))
	if err != nil {
		return errors.New("query runtime identity rejected")
	}
	results := map[string]localCaseResult{}
	for _, c := range cases {
		state := req.Saved[c.ID].Replay
		state.Stage = req.Stage
		stageCtx, err := queryquality.WithStageReplay(ctx, &state)
		if err != nil {
			return err
		}
		result, executeErr := executor.Execute(stageCtx, reader, query.Request{Query: c.Query, Mode: c.Mode})
		status := "success"
		if executeErr != nil {
			status = "execution_failure"
		} else if req.Stage == 70 && (state.Plan == nil || state.Plan.Fallback || state.Expansion.ProviderFailedAttempts > 0) {
			status = "partial_expansion"
		} else if req.Stage == 100 && (result.AISynth == "" || len(result.Citations) == 0) {
			status = "no_grounded_answer"
		}
		diagnostic := ""
		if status == "no_grounded_answer" && len(result.Results) > 0 && len(result.Citations) == 0 {
			diagnostic = "citation_inventory_empty"
			safeRoutes := 0
			for _, selected := range result.Results {
				if search.SafeCitationResult(selected) {
					safeRoutes++
				}
			}
			if safeRoutes == 0 {
				diagnostic = "citation_routes_rejected"
			}
		}
		if executeErr != nil {
			diagnostic = llm.SafeErrorCategory(executeErr)
		}
		results[c.ID] = localCaseResult{Replay: state, Result: result, Status: status, Identity: result.RuntimeConfigIdentity, Diagnostic: diagnostic}
	}
	data, err := json.Marshal(results)
	if err != nil {
		return err
	}
	for _, name := range []string{"DEEPSEEK_API_KEY", "SYNTO_API_KEY"} {
		if secret := os.Getenv(name); secret != "" {
			quoted, _ := json.Marshal(secret)
			data = bytes.ReplaceAll(data, quoted[1:len(quoted)-1], []byte("[redacted]"))
		}
	}
	_, err = out.Write(append(data, '\n'))
	return err
}

// No remote write capability is needed by the materializer. The pinned reader
// verifies exact object generations; this boundary additionally checks every
// manifest size/digest before publishing the local directory.
func materializePinnedSnapshot(ctx context.Context, reader interface {
	ReadFile(context.Context, string) ([]byte, error)
}, snapshot gcs.GenerationSnapshot, destination string) error {
	if err := snapshot.Manifest.Validate(); err != nil {
		return errors.New("invalid published manifest")
	}
	if snapshot.ManifestGeneration <= 0 || snapshot.ManifestSHA256 == "" {
		return errors.New("missing pinned manifest identity")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("snapshot destination must not exist")
	}
	parent := filepath.Dir(destination)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return errors.New("snapshot parent must be canonical and exist")
	}
	temp, err := os.MkdirTemp(parent, ".import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	for _, file := range snapshot.Manifest.Files {
		data, err := reader.ReadFile(ctx, file.Path)
		if err != nil {
			return errors.New("declared snapshot object unavailable; import discarded")
		}
		if int64(len(data)) != file.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != file.SHA256 {
			return errors.New("snapshot object digest mismatch; import discarded")
		}
		target := filepath.Join(temp, filepath.FromSlash(file.Path))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err = os.WriteFile(target, data, 0600); err != nil {
			return err
		}
		// Published manifests have no per-file modification timestamp. Pin a
		// deterministic input for the worker's mtime-based suggestion sampler.
		timestamp, _ := time.Parse(time.RFC3339, snapshot.Manifest.CreatedAt)
		if err = os.Chtimes(target, timestamp, timestamp); err != nil {
			return err
		}
	}
	if err := validateMaterializedSnapshot(ctx, temp); err != nil {
		return err
	}
	// Preserve the source manifest as provenance, not a local moving pointer.
	data, _ := json.Marshal(snapshot)
	if err = os.WriteFile(filepath.Join(temp, "import-provenance.json"), data, 0600); err != nil {
		return err
	}
	// Reserve the destination exclusively after validation. Rename alone can
	// replace an empty user directory created during the download.
	if err := os.Mkdir(destination, 0700); err != nil {
		return errors.New("snapshot destination appeared during import")
	}
	entries, err := os.ReadDir(temp)
	if err == nil {
		for _, entry := range entries {
			if err = os.Rename(filepath.Join(temp, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				break
			}
		}
	}
	if err != nil {
		_ = os.RemoveAll(destination)
		return errors.New("local snapshot publication failed")
	}
	return nil
}

func validateMaterializedSnapshot(ctx context.Context, root string) error {
	prepared, err := preflightSnapshot(ctx, root)
	if err != nil {
		return errors.New("snapshot corpus invalid; import discarded")
	}
	entries, err := prepared.cache.All(ctx, prepared.reader)
	if err != nil || len(entries) == 0 {
		return errors.New("snapshot has no concepts; import discarded")
	}
	for _, entry := range entries {
		if _, _, err := prepared.reader.GetPage(ctx, entry.Slug, "concepts"); err != nil {
			return errors.New("snapshot missing concept page; import discarded")
		}
	}
	return nil
}
