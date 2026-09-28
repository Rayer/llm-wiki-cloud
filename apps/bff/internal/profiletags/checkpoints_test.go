package profiletags

import (
	"context"
	"errors"
	"io/fs"
	"testing"
)

type checkpointMemoryStore struct{ memoryStore }

func (s checkpointMemoryStore) Read(ctx context.Context, path string, limit int) ([]byte, error) {
	if _, ok := s.memoryStore[path]; !ok {
		return nil, fs.ErrNotExist
	}
	return s.memoryStore.Read(ctx, path, limit)
}
func TestCheckpointRejectsCorruptionAndChangedPolicyReevaluates(t *testing.T) {
	ctx := context.Background()
	input := fixture()
	input.CheckpointID = "job-1"
	objects := checkpointMemoryStore{memoryStore{}}
	eval := &fakeEvaluator{}
	first, err := Build(ctx, objects, eval, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Provider.PromptVersion = "changed"
	prior, err := PriorDecisions(ctx, objects, ActiveRef{ContentGeneration: input.Inventory.ContentGeneration, DictionaryRevision: input.Dictionary.Revision, TagSetRevision: first.SetRevision, QueryRuleRevision: first.RulesRevision}, input.Provider)
	if err != nil || len(prior) != 0 {
		t.Fatalf("changed provider reused decisions: %v %v", prior, err)
	}
	input.PriorDecisionRevisions = prior
	if _, err := Build(ctx, objects, eval, input); err != nil {
		t.Fatal(err)
	}
	input.Provider.PromptVersion = "p1"
	item := input.Inventory.Items[0]
	tag := input.Dictionary.Tags[0]
	_, cd, id, _ := inputFor(item)
	rd, _ := RuleDigest(tag, item.Kind, input.Provider)
	lookup := key(item.Kind, item.StableID, tag.ID) + "\x00" + cd + "\x00" + id + "\x00" + rd
	objects.memoryStore[checkpointPath(input.CheckpointID, lookup)] = []byte(`{"decision_revision":"invalid"}`)
	if _, err := Build(ctx, objects, eval, input); err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
}

type checkpointDeniedStore struct{ Store }

func (s checkpointDeniedStore) Read(context.Context, string, int) ([]byte, error) {
	return nil, errors.New("permission denied")
}
func TestCheckpointDoesNotTreatReadFailureAsAbsent(t *testing.T) {
	input := fixture()
	input.CheckpointID = "job"
	if _, err := Build(context.Background(), checkpointDeniedStore{checkpointMemoryStore{memoryStore{}}}, &fakeEvaluator{}, input); err == nil {
		t.Fatal("read failure ignored")
	}
}
