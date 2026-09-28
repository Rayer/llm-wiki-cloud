package profiletags

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
)

type checkpoint struct {
	DecisionRevision string `json:"decision_revision"`
}

func checkpointPath(scope, key string) string {
	return ".lwc/profile/tags/checkpoints/v1/" + digest([]byte(scope)) + "/" + digest([]byte(key)) + ".json"
}

func readCheckpoint(ctx context.Context, s Store, scope, key string, policy ProviderPolicy) (Decision, string, error) {
	b, err := s.Read(ctx, checkpointPath(scope, key), 1024)
	if errors.Is(err, fs.ErrNotExist) {
		return Decision{}, "", nil
	}
	if err != nil {
		return Decision{}, "", err
	}
	var c checkpoint
	if err := StrictDecode(b, &c); err != nil {
		return Decision{}, "", err
	}
	d, err := load[Decision](ctx, s, decisionPath(c.DecisionRevision), c.DecisionRevision)
	if err != nil {
		return Decision{}, "", err
	}
	if !validDecision(d, policy) || decisionKey(d) != key {
		return Decision{}, "", errors.New("checkpoint decision mismatch")
	}
	return d, c.DecisionRevision, nil
}

func writeCheckpoint(ctx context.Context, s Store, scope, key, revision string) error {
	b, _ := json.Marshal(checkpoint{revision})
	path := checkpointPath(scope, key)
	if err := s.Create(ctx, path, b); err != nil {
		return err
	}
	read, err := s.Read(ctx, path, 1024)
	if err != nil {
		return err
	}
	if !bytes.Equal(read, b) {
		return errors.New("checkpoint readback mismatch")
	}
	return nil
}

// PriorDecisions verifies the hash-bound prior active set and its decisions,
// retaining only decisions compatible with the currently pinned provider policy.
// Build additionally checks each item's content, input and rule digests before reuse.
func PriorDecisions(ctx context.Context, s Store, ref ActiveRef, policy ProviderPolicy) ([]string, error) {
	set, err := load[TagSet](ctx, s, SetPath(ref.TagSetRevision), ref.TagSetRevision)
	if err != nil {
		return nil, err
	}
	if set.SchemaVersion != SetSchema || set.ContentGeneration != ref.ContentGeneration || set.DictionaryRevision != ref.DictionaryRevision || set.MissingCount != 0 || set.ExpectedCount != len(set.Rows) || set.CompletedCount != len(set.Rows) {
		return nil, errors.New("invalid prior set")
	}
	revisions := make([]string, 0, len(set.Rows))
	for i, row := range set.Rows {
		if i > 0 && !rowLess(set.Rows[i-1], row) {
			return nil, errors.New("invalid prior row order")
		}
		d, err := load[Decision](ctx, s, decisionPath(row.DecisionRevision), row.DecisionRevision)
		if err != nil {
			return nil, err
		}
		if row.DictionaryRevision != set.DictionaryRevision || d.Kind != row.Kind || d.StableID != row.StableID || d.TagID != row.TagID || d.ContentDigest != row.ContentDigest || d.InputDigest != row.InputDigest || d.RuleDigest != row.RuleDigest || d.Judgment != row.Judgment {
			return nil, errors.New("prior decision mismatch")
		}
		if validDecision(d, policy) {
			revisions = append(revisions, row.DecisionRevision)
		}
	}
	return revisions, nil
}
