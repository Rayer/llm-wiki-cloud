package contentcontext

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
)

func TestFirestoreRepositoryPublicationCarryForwardFencingAndAttemptCAS(t *testing.T) {
	client := newContentContextFirestoreEmulator(t)
	defer client.Close()
	ownerID, projectID := "cc-repo-owner", fmt.Sprintf("cc-repo-%d", time.Now().UnixNano())
	defer func() {
		_, _ = scopedfirestore.Collection(client.Raw(), "projects").Doc(ownerID + "_" + projectID).Collection("content_context").Doc("state").Delete(context.Background())
	}()

	now := time.Date(2026, time.October, 30, 13, 0, 0, 0, time.UTC)
	repository := NewFirestoreRepository(client.Raw())
	repository.SetClockForTest(func() time.Time { return now })
	authority := &testPublicationAuthority{publication: testPublication("G1", 'a', "manifest-g1")}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	state, err := repository.ReconcileCurrent(ctx, ownerID, projectID, authority)
	if err != nil || state.Status != StatusPending || state.TargetGeneration != "G1" || state.TargetSequence != 1 {
		t.Fatalf("initial publication state=%+v err=%v", state, err)
	}
	claim, err := repository.Claim(ctx, ownerID, projectID, authority)
	if err != nil || claim == nil || claim.SourceGeneration != "G1" || claim.RetryCount != 0 {
		t.Fatalf("initial claim=%+v err=%v", claim, err)
	}

	authority.publication = testPublication("G2", 'a', "manifest-g2")
	state, err = repository.ReconcileCurrent(ctx, ownerID, projectID, authority)
	if err != nil || state.TargetGeneration != "G2" || state.TargetSequence != 2 || state.SummaryWork == nil ||
		state.SummaryWork.WorkID != claim.WorkID || state.SummaryWork.SourceGeneration != "G1" || state.SummaryWork.AttemptID != claim.AttemptID ||
		state.SummaryWork.LeaseUntil == nil || !state.SummaryWork.LeaseUntil.Equal(claim.LeaseUntil) || state.SummaryWork.RetryCount != 0 {
		t.Fatalf("same-digest carry-forward reset work: state=%+v err=%v", state, err)
	}

	completed, err := repository.Complete(ctx, ownerID, projectID, authority, *claim, testSummaryCompletion("summary from G1"))
	if err != nil || !completed {
		t.Fatalf("same-digest attempt completion accepted=%t err=%v", completed, err)
	}
	state, err = repository.Read(ctx, ownerID, projectID)
	if err != nil || state.Status != StatusReady || state.TargetGeneration != "G2" || state.LastGood == nil ||
		state.LastGood.Text != "summary from G1" || state.LastGood.SourceGeneration != "G1" ||
		state.LatestPublication == nil || state.LatestPublication.ManifestIdentity != "manifest-g2" {
		t.Fatalf("persisted carry-forward summary=%+v err=%v", state, err)
	}
	if completed, err := repository.Complete(ctx, ownerID, projectID, authority, *claim, testSummaryCompletion("duplicate")); completed || err == nil {
		t.Fatalf("duplicate finalize accepted=%t err=%v", completed, err)
	}

	authority.publication = testPublication("G3", 'b', "manifest-g3")
	state, err = repository.ReconcileCurrent(ctx, ownerID, projectID, authority)
	if err != nil || state.Status != StatusPending || state.LastGood == nil || state.LastGood.Text != "summary from G1" ||
		state.TargetGeneration != "G3" || state.SummaryWork == nil || state.SummaryWork.WorkID == claim.WorkID {
		t.Fatalf("different digest did not retain last-good: state=%+v err=%v", state, err)
	}
	oldDigestClaim, err := repository.Claim(ctx, ownerID, projectID, authority)
	if err != nil || oldDigestClaim == nil {
		t.Fatalf("G3 claim=%+v err=%v", oldDigestClaim, err)
	}
	authority.publication = testPublication("G4", 'c', "manifest-g4")
	if completed, err := repository.Complete(ctx, ownerID, projectID, authority, *oldDigestClaim, testSummaryCompletion("must be rejected")); err != nil || completed {
		t.Fatalf("different-digest stale result accepted=%t err=%v", completed, err)
	}
	state, err = repository.Read(ctx, ownerID, projectID)
	if err != nil || state.TargetGeneration != "G4" || state.Status != StatusPending || state.LastGood == nil || state.LastGood.Text != "summary from G1" {
		t.Fatalf("stale finalize did not reconcile current digest: state=%+v err=%v", state, err)
	}

	newClaim, err := repository.Claim(ctx, ownerID, projectID, authority)
	if err != nil || newClaim == nil || newClaim.SourceGeneration != "G4" {
		t.Fatalf("G4 claim=%+v err=%v", newClaim, err)
	}
	if failed, err := repository.Fail(ctx, ownerID, projectID, authority, *newClaim, "provider_timeout"); err != nil || !failed {
		t.Fatalf("provider failure transition failed=%t err=%v", failed, err)
	}
	authority.publication = testPublication("G5", 'c', "manifest-g5")
	state, err = repository.ReconcileCurrent(ctx, ownerID, projectID, authority)
	if err != nil || state.Status != StatusFailed || state.ErrorClass != "provider_timeout" || state.SummaryWork == nil ||
		state.SummaryWork.RetryCount != 0 || state.LastGood == nil || state.LastGood.Text != "summary from G1" {
		t.Fatalf("failed same-digest carry-forward restarted work: state=%+v err=%v", state, err)
	}
	if retry, err := repository.Claim(ctx, ownerID, projectID, authority); err != nil || retry != nil {
		t.Fatalf("failed same-digest work was retried: claim=%+v err=%v", retry, err)
	}
	if authority.unfencedReads != 0 || authority.fenceCalls == 0 {
		t.Fatalf("manifest reads escaped the publisher fence: fences=%d unfenced=%d", authority.fenceCalls, authority.unfencedReads)
	}
}

func TestFirestoreRepositoryBoundsExpiredAttemptTakeover(t *testing.T) {
	client := newContentContextFirestoreEmulator(t)
	defer client.Close()
	ownerID, projectID := "cc-lease-owner", fmt.Sprintf("cc-lease-%d", time.Now().UnixNano())
	defer func() {
		_, _ = scopedfirestore.Collection(client.Raw(), "projects").Doc(ownerID + "_" + projectID).Collection("content_context").Doc("state").Delete(context.Background())
	}()

	now := time.Date(2026, time.October, 30, 13, 0, 0, 0, time.UTC)
	repository := NewFirestoreRepository(client.Raw())
	repository.SetClockForTest(func() time.Time { return now })
	authority := &testPublicationAuthority{publication: testPublication("G1", 'd', "manifest-g1")}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := repository.ReconcileCurrent(ctx, ownerID, projectID, authority); err != nil {
		t.Fatal(err)
	}
	first, err := repository.Claim(ctx, ownerID, projectID, authority)
	if err != nil || first == nil {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	now = first.LeaseUntil
	second, err := repository.Claim(ctx, ownerID, projectID, authority)
	if err != nil || second == nil || second.AttemptID == first.AttemptID || second.RetryCount != 1 {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
	if completed, err := repository.Complete(ctx, ownerID, projectID, authority, *first, testSummaryCompletion("stale attempt")); completed || err == nil {
		t.Fatalf("expired attempt finalized accepted=%t err=%v", completed, err)
	}
	now = second.LeaseUntil
	third, err := repository.Claim(ctx, ownerID, projectID, authority)
	if err != nil || third != nil {
		t.Fatalf("work exceeded two claim budget: claim=%+v err=%v", third, err)
	}
	state, err := repository.Read(ctx, ownerID, projectID)
	if err != nil || state.Status != StatusFailed || state.ErrorClass != "lease_expired" || state.SummaryWork == nil || state.SummaryWork.RetryCount != 1 {
		t.Fatalf("expired claim budget state=%+v err=%v", state, err)
	}
}

func testPublication(generation string, digestByte byte, manifestIdentity string) Publication {
	return Publication{Generation: generation, ConceptsDigest: strings.Repeat(string(digestByte), 64), ManifestIdentity: manifestIdentity}
}

func testSummaryCompletion(text string) SummaryCompletion {
	return SummaryCompletion{
		Text: text, PromptRevision: SummaryPromptRevision, ProfileID: "profile-1", ProfileRevision: 2,
		Role: SummaryRole, RequestedModel: "model-requested", ReturnedModel: "model-returned",
		InputDigest: strings.Repeat("e", 64), SelectionVersion: SummarySelectionVersion,
	}
}

type testPublicationAuthority struct {
	publication   Publication
	fenceCalls    int
	unfencedReads int
	inFence       bool
}

func (a *testPublicationAuthority) WithProjectFence(ctx context.Context, _, _ string, operation func(context.Context) error) error {
	a.fenceCalls++
	if a.inFence {
		return errors.New("publisher fence reentered")
	}
	a.inFence = true
	defer func() { a.inFence = false }()
	return operation(ctx)
}

func (a *testPublicationAuthority) CurrentPublication(_ context.Context, _, _ string) (Publication, error) {
	if !a.inFence {
		a.unfencedReads++
		return Publication{}, errors.New("manifest read must hold publisher fence")
	}
	return a.publication, nil
}

func newContentContextFirestoreEmulator(t *testing.T) *scopedfirestore.Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatalf("invalid Firestore emulator host %q", endpoint)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}
	project := fmt.Sprintf("content-context-%d", time.Now().UnixNano())
	client, err := scopedfirestore.NewClientWithDatabaseAndScope(project, "(default)", "", "", "")
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	return client
}
