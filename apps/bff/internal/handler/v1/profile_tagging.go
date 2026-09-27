package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

type profileTagGenerationStore interface {
	PinGeneration(context.Context, string) (*gcs.Client, gcs.GenerationSnapshot, error)
}
type profileTagSnapshotReader interface {
	ReadFileLimited(context.Context, string, int64) ([]byte, error)
	ReadImmutableFile(context.Context, string, int64, int64) ([]byte, error)
}

type profileTagPinnedReader struct{ *gcs.Client }

// Content-addressed source objects are create-only. Read bytes and generation
// atomically using the existing GCS seam and reject any version replacement.
func (r profileTagPinnedReader) ReadImmutableFile(ctx context.Context, path string, wanted, limit int64) ([]byte, error) {
	data, actual, err := r.ReadFileWithGeneration(ctx, path)
	if err != nil {
		return nil, err
	}
	if wanted <= 0 || actual != wanted || int64(len(data)) > limit {
		return nil, store.ErrDeclaredObjectUnavailable
	}
	return data, nil
}

// RunProfileTagging executes one already leased job. The caller must hold the
// durable execution lease through the final transition and propagate its context.
func (h *Handler) RunProfileTagging(ctx context.Context, userID, projectID string, revision int64, candidateID, jobID string, evaluator profiletags.Evaluator, policy profiletags.ProviderPolicy) error {
	return h.runProfileTagging(ctx, userID, projectID, revision, candidateID, jobID, evaluator, policy, func(ctx context.Context, generationID string) (profiletags.Inventory, profiletags.Store, error) {
		if h.store == nil {
			return profiletags.Inventory{}, nil, errors.New("Profile store unavailable")
		}
		scoped := h.store.Scope(userID, projectID)
		generations, ok := scoped.(profileTagGenerationStore)
		if !ok {
			return profiletags.Inventory{}, nil, errors.New("exact generation reader unavailable")
		}
		objects, ok := scoped.(profileartifacts.ObjectStore)
		if !ok {
			return profiletags.Inventory{}, nil, errors.New("immutable Profile store unavailable")
		}
		pinned, snapshot, err := generations.PinGeneration(ctx, generationID)
		if err != nil {
			return profiletags.Inventory{}, nil, err
		}
		inventory, err := readProfileTagInventory(ctx, profileTagPinnedReader{pinned}, snapshot.Manifest, generationID)
		return inventory, profileTagObjectStore{objects}, err
	})
}

type profileTagInputLoader func(context.Context, string) (profiletags.Inventory, profiletags.Store, error)

func (h *Handler) runProfileTagging(ctx context.Context, userID, projectID string, revision int64, candidateID, jobID string, evaluator profiletags.Evaluator, policy profiletags.ProviderPolicy, load profileTagInputLoader) error {
	state, err := h.ClaimProfileJob(ctx, userID, projectID, revision, candidateID, jobID)
	if err != nil {
		return err
	}
	fail := func(code string, missing int64, cause error) error {
		_, transitionErr := h.TransitionProfileJob(ctx, userID, projectID, revision, candidateID, jobID, profileJobIncomplete, missing, code, "", "", false)
		return errors.Join(cause, transitionErr)
	}
	c := state.Candidate
	if c == nil || state.Revision != revision || c.BaseRevision != revision || c.CandidateID != candidateID || c.RequirementsDigest != profileRequirementsDigest(state.Requirements) || c.Dictionary.InputDigest != c.RequirementsDigest || state.Job == nil || state.Job.JobID != jobID || state.Job.CandidateID != candidateID || state.Job.ContentGeneration != c.ContentGeneration || state.Job.Status != profileJobRunning {
		return fail("invalid_candidate", 0, errProfileCandidateNotCurrent)
	}
	inventory, objects, err := load(ctx, c.ContentGeneration)
	if err != nil {
		return fail("inventory_invalid", 0, err)
	}
	if inventory.ContentGeneration != c.ContentGeneration {
		return fail("inventory_invalid", 0, errors.New("candidate generation mismatch"))
	}
	data, err := objects.Read(ctx, profileartifacts.DictionaryObjectPath(c.Dictionary.Revision), profileartifacts.MaxArtifactBytes+1)
	if err != nil {
		return fail("dictionary_invalid", 0, err)
	}
	order := make([]string, len(state.Requirements))
	for i, r := range state.Requirements {
		order[i] = r.ID
	}
	ref := profileartifacts.DerivedRef{Revision: c.Dictionary.Revision, InputDigest: c.Dictionary.InputDigest, ModelVersion: c.Dictionary.ModelVersion, PromptVersion: c.Dictionary.PromptVersion, SchemaVersion: c.Dictionary.SchemaVersion}
	envelope, err := profileartifacts.ValidateDictionary(data, ref, c.ContentGeneration, inventory.ConceptsDigest, order)
	if err != nil {
		return fail("dictionary_invalid", 0, err)
	}
	dictionary := profiletags.Dictionary{Revision: ref.Revision, Tags: make([]profiletags.Tag, 0, len(envelope.Tags))}
	for _, t := range envelope.Tags {
		kinds := make([]profiletags.Kind, len(t.AppliesTo))
		for i, k := range t.AppliesTo {
			kinds[i] = profiletags.Kind(k)
		}
		dictionary.Tags = append(dictionary.Tags, profiletags.Tag{ID: t.ID, Definition: t.Definition, AppliesTo: kinds, MatchRule: t.MatchRule, NonMatchRule: t.NonMatchRule, UnknownRule: t.UnknownRule, QueryUse: t.QueryUse})
	}
	input := profiletags.Input{Inventory: inventory, Dictionary: dictionary, Provider: policy, MaxAttempts: 3, CheckpointID: jobID}
	if state.Active != nil {
		a := state.Active
		input.PriorDecisionRevisions, err = profiletags.PriorDecisions(ctx, objects, profiletags.ActiveRef{ContentGeneration: a.ContentGeneration, DictionaryRevision: a.DictionaryRevision, TagSetRevision: a.TagSetRevision, QueryRuleRevision: a.QueryRuleRevision}, policy)
		if err != nil {
			return fail("prior_set_invalid", 0, err)
		}
	}
	result, err := profiletags.Build(ctx, objects, evaluator, input)
	if err != nil {
		return fail("tagging_failed", 0, err)
	}
	if result.Set.MissingCount != 0 {
		return fail("provider_incomplete", int64(result.Set.MissingCount), errors.New("Profile tagging coverage incomplete"))
	}
	active := profiletags.ActiveRef{ContentGeneration: c.ContentGeneration, DictionaryRevision: ref.Revision, TagSetRevision: result.SetRevision, QueryRuleRevision: result.RulesRevision}
	if _, err := profiletags.ValidatePublication(ctx, objects, active, inventory, dictionary, policy, input.Quality); err != nil {
		return fail("publication_invalid", 0, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	completed, err := h.TransitionProfileJob(ctx, userID, projectID, revision, candidateID, jobID, profileJobReady, 0, "", result.SetRevision, result.RulesRevision, true)
	if err != nil {
		// Resolve an ambiguous commit only against the exact tuple we validated.
		current, readErr := h.profileRepository.GetProfile(ctx, userID, projectID)
		if readErr == nil && current.Active != nil {
			a := current.Active
			if a.CandidateID == candidateID && a.ContentGeneration == active.ContentGeneration && a.DictionaryRevision == active.DictionaryRevision && a.TagSetRevision == active.TagSetRevision && a.QueryRuleRevision == active.QueryRuleRevision && a.GuidanceRevision == c.Guidance.Revision {
				return nil
			}
		}
		return err
	}
	if completed.Revision != revision || completed.Candidate == nil || completed.Candidate.CandidateID != candidateID || completed.Job == nil || completed.Job.JobID != jobID || completed.Job.Status != profileJobReady {
		return errors.New("Profile tagging completion superseded or conflicted")
	}
	return nil
}

// readProfileTagInventory enumerates only manifest-listed canonical concepts and
// retained source snapshots; it never reads moving raw files or concept pages.
func readProfileTagInventory(ctx context.Context, reader profileTagSnapshotReader, manifest generation.Manifest, wanted string) (profiletags.Inventory, error) {
	inventory := profiletags.Inventory{ContentGeneration: wanted}
	if err := manifest.Validate(); err != nil {
		return inventory, err
	}
	if manifest.GenerationID != wanted {
		return inventory, errors.New("pinned generation mismatch")
	}
	read := func(path string) ([]byte, error) {
		f, ok := manifest.File(path)
		if !ok {
			return nil, fmt.Errorf("generation does not list %s", path)
		}
		b, err := reader.ReadFileLimited(ctx, path, generation.MaxFileBytes+1)
		if err != nil {
			return nil, err
		}
		if int64(len(b)) != f.Size || generation.Digest(b) != f.SHA256 {
			return nil, fmt.Errorf("generation file mismatch: %s", path)
		}
		return b, nil
	}
	idBytes, err := read("cache/id_map.json")
	if err != nil {
		return inventory, err
	}
	ids, err := wikiindex.DecodeIDMap(idBytes)
	if err != nil {
		return inventory, err
	}
	var idFields map[string]json.RawMessage
	if err := json.Unmarshal(idBytes, &idFields); err != nil {
		return inventory, err
	}
	if source, present := idFields["source"]; !present || bytes.Equal(bytes.TrimSpace(source), []byte("null")) {
		return inventory, errors.New("canonical source ID map absent")
	}
	conceptsBytes, err := read("cache/concepts.jsonl")
	if err != nil {
		return inventory, err
	}
	concepts, digest, err := profilederive.ValidateConceptSnapshot(conceptsBytes, ids)
	if err != nil {
		return inventory, err
	}
	inventory.ConceptsDigest, inventory.IDMapDigest = digest, generation.Digest(idBytes)
	path, err := generation.SourceSnapshotPath(manifest.SourceSnapshotDigest)
	if err != nil {
		return inventory, err
	}
	snapshotBytes, err := reader.ReadFileLimited(ctx, path, generation.MaxSourceSnapshotBytes+1)
	if err != nil {
		return inventory, err
	}
	if generation.Digest(snapshotBytes) != manifest.SourceSnapshotDigest {
		return inventory, errors.New("source snapshot digest mismatch")
	}
	snapshot, err := generation.DecodeSourceSnapshot(snapshotBytes)
	if err != nil {
		return inventory, err
	}
	// The publisher validated the private post-run receipts before committing
	// this immutable snapshot. SourceStatusDigest is retained provenance; the
	// mutable receipt file is deliberately not a reader input.
	if snapshot.ContentGeneration != wanted || snapshot.IDMapDigest != inventory.IDMapDigest || len(snapshot.Rows) != len(ids.Source) {
		return inventory, errors.New("source snapshot binding mismatch")
	}
	inventory.SourceSnapshotDigest = manifest.SourceSnapshotDigest
	for _, row := range snapshot.Rows {
		_, active := ids.Source[row.StableID]
		metaPath := ids.SourceMeta[row.StableID].SourceFile
		if !active || (metaPath != "" && metaPath != row.RawPath) {
			return inventory, errors.New("source snapshot identity mismatch")
		}
		path, err := generation.SourceBytesPath(row.ContentDigest)
		if err != nil {
			return inventory, err
		}
		content, err := reader.ReadImmutableFile(ctx, path, row.ObjectGeneration, generation.MaxFileBytes+1)
		if err != nil {
			return inventory, err
		}
		if len(content) > generation.MaxFileBytes || generation.Digest(content) != row.ContentDigest {
			return inventory, errors.New("source bytes digest mismatch")
		}
		inventory.Items = append(inventory.Items, profiletags.Item{Kind: profiletags.Source, StableID: row.StableID, Content: content})
	}
	for _, concept := range concepts {
		inventory.Items = append(inventory.Items, profiletags.Item{Kind: profiletags.Concept, StableID: concept.ID, Content: []byte(concept.Row)})
	}
	return inventory, nil
}

type profileTagObjectStore struct{ profileartifacts.ObjectStore }

func (s profileTagObjectStore) Read(ctx context.Context, path string, limit int) ([]byte, error) {
	b, err := s.ReadFileLimited(ctx, path, int64(limit))
	if errors.Is(err, store.ErrObjectNotExist) {
		return nil, fs.ErrNotExist
	}
	return b, err
}
func (s profileTagObjectStore) Create(ctx context.Context, path string, data []byte) error {
	if _, err := s.WriteFileIfGeneration(ctx, data, path, 0); err != nil {
		existing, readErr := s.ReadFileLimited(ctx, path, int64(len(data)+1))
		if readErr != nil || !bytes.Equal(existing, data) {
			return fmt.Errorf("immutable Profile object write: %w", err)
		}
	}
	return nil
}
