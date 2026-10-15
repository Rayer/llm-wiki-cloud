package query

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// GenerationPinner must resolve the retained archive, never the latest manifest.
type GenerationPinner interface {
	PinQueryGeneration(context.Context, string) (storage.Store, generation.Manifest, error)
}

type ProfileSnapshot struct {
	Active profiletags.ActiveRef
	Corpus cache.PinnedCanonicalSnapshot
	Bundle profiletags.Bundle
	IDMap  wikiindex.IDMap
}

type limitedProfileReader interface {
	ReadFileLimited(context.Context, string, int64) ([]byte, error)
}
type tagRead struct {
	finish func(error, string, string)
	err    error
}
type tagReadState struct{ reads []tagRead }
type readOnlyTags struct {
	limitedProfileReader
	state *tagReadState
}

func (s readOnlyTags) Read(ctx context.Context, name string, limit int) ([]byte, error) {
	role := "other"
	switch {
	case strings.HasPrefix(name, ".lwc/profile/tags/sets/"):
		role = "tag_set"
	case strings.HasPrefix(name, ".lwc/profile/query/rules/"):
		role = "query_rules"
	}
	readCtx, finish := observability.StartStorageRead(ctx, role)
	data, err := s.ReadFileLimited(readCtx, name, int64(limit))
	s.state.reads = append(s.state.reads, tagRead{finish: finish, err: err})
	return data, err
}
func (s readOnlyTags) Create(context.Context, string, []byte) error {
	return errors.New("query artifact store is read only")
}

// LoadProfile joins only stable canonical concept IDs and independently retained sources.
// The aggregate reader validates complete coverage without reading decision objects or pages.
func LoadProfile(ctx context.Context, c *cache.Cache, reader storage.Store, manifest generation.Manifest, active profiletags.ActiveRef, dictionaryRef profileartifacts.DerivedRef, requirementOrder []string) (*ProfileSnapshot, error) {
	if c == nil || manifest.GenerationID != active.ContentGeneration {
		return nil, errors.New("invalid profile generation")
	}
	limited, ok := reader.(limitedProfileReader)
	if !ok {
		return nil, errors.New("bounded profile artifact reads unavailable")
	}
	corpus, err := c.PinnedCanonicalSnapshot(ctx, reader)
	if err != nil {
		return nil, err
	}
	if corpus.Identity.GenerationID != active.ContentGeneration {
		return nil, errors.New("profile corpus generation mismatch")
	}
	idReadCtx, finishIDMap := observability.StartStorageRead(ctx, "id_map")
	idBytes, err := reader.ReadFile(idReadCtx, "cache/id_map.json")
	if err != nil {
		finishIDMap(err, "failure", "other")
		return nil, err
	}
	idFile, ok := manifest.File("cache/id_map.json")
	if !ok || generation.Digest(idBytes) != idFile.SHA256 {
		finishIDMap(errors.New("id map digest mismatch"), "failure", "digest")
		return nil, errors.New("profile ID map digest mismatch")
	}
	ids, err := wikiindex.DecodeIDMap(idBytes)
	if err != nil {
		finishIDMap(err, "failure", "decode")
		return nil, err
	}
	if ids.Source == nil || len(ids.Concept) != len(corpus.Entries) {
		finishIDMap(errors.New("id map coverage mismatch"), "failure", "coverage")
		return nil, errors.New("profile canonical inventory incomplete")
	}
	finishIDMap(nil, "success", "none")
	sourceReader, ok := reader.(interface {
		ReadSourceSnapshot(context.Context) (generation.SourceSnapshotManifest, error)
	})
	if !ok {
		return nil, errors.New("retained source snapshots unavailable")
	}
	sourceCtx, finishSourceInventory := observability.StartStorageRead(ctx, "source_inventory")
	sources, err := sourceReader.ReadSourceSnapshot(sourceCtx)
	if err != nil {
		finishSourceInventory(err, "failure", "other")
		return nil, err
	}
	if sources.ContentGeneration != active.ContentGeneration || sources.IDMapDigest != idFile.SHA256 || len(sources.Rows) != len(ids.Source) {
		finishSourceInventory(errors.New("source inventory coverage mismatch"), "failure", "coverage")
		return nil, errors.New("profile source inventory mismatch")
	}
	index := profiletags.InventoryIndex{ContentGeneration: active.ContentGeneration, ConceptsDigest: strings.TrimPrefix(corpus.Identity.ConceptsDigest, "sha256:"), IDMapDigest: idFile.SHA256, SourceSnapshotDigest: manifest.SourceSnapshotDigest}
	for _, item := range corpus.Entries {
		if ids.Concept[item.StableID] != item.Entry.Slug || !wikiindex.ValidSyntoEntityID(item.StableID) {
			finishSourceInventory(errors.New("concept inventory identity mismatch"), "failure", "identity")
			return nil, errors.New("profile concept identity mismatch")
		}
		index.Items = append(index.Items, profiletags.ItemRef{Kind: profiletags.Concept, StableID: item.StableID, ContentDigest: item.ContentDigest})
	}
	for _, row := range sources.Rows {
		if _, ok := ids.Source[row.StableID]; !ok || (ids.SourceMeta[row.StableID].SourceFile != "" && ids.SourceMeta[row.StableID].SourceFile != row.RawPath) {
			finishSourceInventory(errors.New("source inventory identity mismatch"), "failure", "identity")
			return nil, errors.New("profile source identity mismatch")
		}
		index.Items = append(index.Items, profiletags.ItemRef{Kind: profiletags.Source, StableID: row.StableID, ContentDigest: row.ContentDigest})
	}
	finishSourceInventory(nil, "success", "none")
	if !profileartifacts.IsSHA256(active.DictionaryRevision) {
		return nil, errors.New("invalid dictionary revision")
	}
	dictionaryCtx, finishDictionary := observability.StartStorageRead(ctx, "dictionary")
	data, err := limited.ReadFileLimited(dictionaryCtx, profileartifacts.DictionaryObjectPath(active.DictionaryRevision), profileartifacts.MaxArtifactBytes)
	if err != nil {
		finishDictionary(err, "failure", "other")
		return nil, err
	}
	if dictionaryRef.Revision != active.DictionaryRevision {
		finishDictionary(errors.New("dictionary identity mismatch"), "failure", "identity")
		return nil, errors.New("active dictionary reference mismatch")
	}
	envelope, err := profileartifacts.ValidateDictionary(data, dictionaryRef, active.ContentGeneration, index.ConceptsDigest, requirementOrder)
	if err != nil {
		finishDictionary(err, "failure", "decode")
		return nil, fmt.Errorf("profile dictionary: %w", err)
	}
	finishDictionary(nil, "success", "none")
	dictionary := profiletags.Dictionary{Revision: active.DictionaryRevision}
	for _, tag := range envelope.Tags {
		t := profiletags.Tag{ID: tag.ID, Definition: tag.Definition, MatchRule: tag.MatchRule, NonMatchRule: tag.NonMatchRule, UnknownRule: tag.UnknownRule, QueryUse: tag.QueryUse}
		for _, kind := range tag.AppliesTo {
			t.AppliesTo = append(t.AppliesTo, profiletags.Kind(kind))
		}
		dictionary.Tags = append(dictionary.Tags, t)
	}
	tagReads := &tagReadState{}
	bundle, err := profiletags.ReadBundle(ctx, readOnlyTags{limitedProfileReader: limited, state: tagReads}, active, index, dictionary)
	for _, read := range tagReads.reads {
		verificationErr, verification, reason := read.err, "success", "none"
		if err != nil {
			verificationErr, verification, reason = errors.New("profile artifact validation failed"), "failure", "other"
		}
		if read.err != nil {
			verificationErr, verification, reason = read.err, "failure", "other"
		}
		read.finish(verificationErr, verification, reason)
	}
	if err != nil {
		return nil, err
	}
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.Int("query.corpus.count", len(corpus.Entries)),
		attribute.Int("query.source_inventory.count", len(sources.Rows)),
		attribute.Int("query.tag_set.count", len(bundle.Set.Rows)),
		attribute.Int("query.query_rules.count", len(bundle.Rules.Rules)),
	)
	return &ProfileSnapshot{Active: active, Corpus: corpus, Bundle: bundle, IDMap: ids}, nil
}

func (p *ProfileSnapshot) PreferenceScores() map[string]int {
	scores := map[string]int{}
	preferred := map[string]bool{}
	for _, rule := range p.Bundle.Rules.Rules {
		if rule.Kind == profiletags.Concept && rule.QueryUse == "preferred" {
			preferred[rule.TagID] = true
		}
	}
	byID := map[string]string{}
	for _, item := range p.Corpus.Entries {
		byID[item.StableID] = item.Entry.Slug
	}
	for _, row := range p.Bundle.Set.Rows {
		if row.Kind == profiletags.Concept && row.Judgment == profiletags.Match && preferred[row.TagID] {
			scores[byID[row.StableID]] = 1
		}
	}
	return scores
}

var ErrUnsupportedRequired = profiletags.ErrUnsupportedRequired

func ValidateProfileRequest(request Request) error {
	if len(request.RequiredTagIDs) == 0 {
		return nil
	}
	if request.Profile == nil {
		return ErrUnsupportedRequired
	}
	return request.Profile.Bundle.RequireSupported(request.RequiredTagIDs, profiletags.Concept)
}

// buildProfileContexts uses the already verified request corpus and identity map,
// including when another request invalidates the shared cache during synthesis.
func buildProfileContexts(ctx context.Context, results []search.Result, authority *search.CitationAuthority, snapshot *ProfileSnapshot) ([]string, error) {
	resolver, err := newCitationIdentityResolver(snapshot.IDMap)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]cache.Entry, len(snapshot.Corpus.Entries))
	for _, item := range snapshot.Corpus.Entries {
		entries[item.Entry.Slug] = item.Entry
	}
	contexts := make([]string, 0, len(results))
	for rank, result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry, ok := entries[result.Slug]
		if !ok || result.Type != "concept" {
			return nil, errors.New("result outside pinned profile corpus")
		}
		resolved, err := resolver.resolve(result)
		if err != nil {
			return nil, err
		}
		results[rank].ID = resolved.ID
		sourceContext := "Sources: none listed"
		if len(entry.Sources) > 0 {
			sourceContext = "Sources: [" + strings.Join(entry.Sources, ", ") + "]"
		}
		contexts = append(contexts, authority.AddContext(rank, resolved, sourceContext+"\n\n"+entry.Body))
	}
	return contexts, nil
}
