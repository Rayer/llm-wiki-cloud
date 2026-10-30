package contentcontext

import (
	"testing"
	"time"
)

var testTime = time.Date(2026, time.October, 30, 9, 0, 0, 0, time.UTC)

func publication(generation, digest string, sequence uint64) Publication {
	return Publication{
		Generation: generation, ConceptsDigest: digest,
		ManifestIdentity: generation + ":" + digest, TargetSequence: sequence,
	}
}

func confirm(t *testing.T, state State, p Publication) State {
	t.Helper()
	next, ok := state.ConfirmPublication(p)
	if !ok {
		t.Fatalf("ConfirmPublication(%+v) rejected", p)
	}
	return next
}

func claim(t *testing.T, state State, id string, now time.Time) (State, Attempt) {
	t.Helper()
	next, attempt, ok := state.ClaimAttempt(id, now, time.Minute)
	if !ok {
		t.Fatalf("ClaimAttempt(%q) rejected", id)
	}
	return next, attempt
}

func TestSameDigestCarryForwardKeepsPendingAttemptAndAcceptsResult(t *testing.T) {
	state := confirm(t, State{}, publication("G1", "D", 1))
	state, attempt := claim(t, state, "attempt-g1", testTime)

	carried := confirm(t, state, publication("G2", "D", 2))
	if carried.LatestPublication.Generation != "G2" || carried.Work != state.Work {
		t.Fatalf("carry-forward changed target/work unexpectedly: target=%+v work=%+v", carried.LatestPublication, carried.Work)
	}
	if carried.Work.RetryCount != 0 || carried.Work.Attempt != attempt {
		t.Fatalf("carry-forward changed attempt budget: %+v", carried.Work)
	}
	retried, retryAttempt, ok := carried.ClaimAttempt("attempt-g1-retry", testTime.Add(time.Minute), time.Minute)
	if !ok || retried.Work.RetryCount != 1 || retryAttempt.SourceGeneration != "G1" ||
		retryAttempt.SourceManifestIdentity != "G1:D" {
		t.Fatalf("same-digest retry lost original source identity: work=%+v attempt=%+v ok=%t", retried.Work, retryAttempt, ok)
	}

	ready, ok := carried.CompleteAttempt(attempt, testTime.Add(30*time.Second), "summary")
	if !ok {
		t.Fatal("still-valid G1 attempt was rejected after same-digest carry-forward")
	}
	if ready.Status() != StatusReady || ready.Stale() || ready.LastGood.SourceGeneration != "G1" ||
		ready.LastGood.ConceptsDigest != "D" || ready.LastGood.Text != "summary" {
		t.Fatalf("unexpected completed state: %+v", ready)
	}
	if ready.LatestPublication.Generation != "G2" {
		t.Fatalf("latest publication = %+v, want G2", ready.LatestPublication)
	}
}

func TestDifferentDigestRejectsOldAttemptAndRetainsLastGood(t *testing.T) {
	state := confirm(t, State{}, publication("G0", "D0", 1))
	state, first := claim(t, state, "attempt-d0", testTime)
	state, ok := state.CompleteAttempt(first, testTime.Add(time.Second), "last good")
	if !ok {
		t.Fatal("initial completion rejected")
	}
	lastGood := state.LastGood

	state = confirm(t, state, publication("G1", "D1", 2))
	state, stale := claim(t, state, "attempt-d1", testTime.Add(2*time.Second))
	state = confirm(t, state, publication("G2", "D2", 3))

	if _, ok := state.CompleteAttempt(stale, testTime.Add(3*time.Second), "stale"); ok {
		t.Fatal("old digest attempt completed after a different digest was published")
	}
	if state.Status() != StatusPending || !state.Stale() || !state.HasLastGood || state.LastGood != lastGood {
		t.Fatalf("new digest did not retain stale last-good and start new work: %+v", state)
	}
	// A late G1 reconcile must re-read the authoritative manifest under the
	// publisher fence and register the current G2 identity again.
	state = confirm(t, state, publication("G2", "D2", 4))
	if state.Work.ConceptsDigest != "D2" || state.Work.Status != StatusPending || state.Work.SourceGeneration != "G2" {
		t.Fatalf("same-manifest reconcile changed active work: %+v", state.Work)
	}
	if state.LatestPublication.Generation != "G2" || state.LatestPublication.ConceptsDigest != "D2" {
		t.Fatalf("late publication regressed target: %+v", state.LatestPublication)
	}
}

func TestSameDigestCarryForwardDoesNotRetryReadyOrFailedWork(t *testing.T) {
	ready := confirm(t, State{}, publication("G1", "D", 1))
	ready, readyAttempt := claim(t, ready, "ready-attempt", testTime)
	ready, ok := ready.CompleteAttempt(readyAttempt, testTime.Add(time.Second), "summary")
	if !ok {
		t.Fatal("completion rejected")
	}
	ready = confirm(t, ready, publication("G2", "D", 2))
	readyWork := ready.Work
	if _, _, ok := ready.ClaimAttempt("ready-retry", testTime.Add(2*time.Second), time.Minute); ok {
		t.Fatal("ready same-digest work was claimed again")
	}
	if ready.Status() != StatusReady || ready.Work != readyWork || ready.Stale() {
		t.Fatalf("ready carry-forward changed work: %+v", ready)
	}

	failed := confirm(t, State{}, publication("G1", "D", 1))
	failed, failedAttempt := claim(t, failed, "failed-attempt", testTime)
	failed, ok = failed.FailAttempt(failedAttempt, testTime.Add(time.Second))
	if !ok {
		t.Fatal("failure transition rejected")
	}
	failed = confirm(t, failed, publication("G2", "D", 2))
	failedWork := failed.Work
	if _, _, ok := failed.ClaimAttempt("failed-retry", testTime.Add(2*time.Second), time.Minute); ok {
		t.Fatal("failed same-digest work was claimed again")
	}
	if failed.Status() != StatusFailed || failed.Work != failedWork || failed.Work.RetryCount != 0 {
		t.Fatalf("failed carry-forward changed terminal work or retry count: %+v", failed)
	}
}

func TestDuplicateFinalizeIsRejected(t *testing.T) {
	state := confirm(t, State{}, publication("G1", "D", 1))
	state, attempt := claim(t, state, "attempt", testTime)
	ready, ok := state.CompleteAttempt(attempt, testTime.Add(time.Second), "summary")
	if !ok {
		t.Fatal("first completion rejected")
	}
	if duplicate, ok := ready.CompleteAttempt(attempt, testTime.Add(2*time.Second), "duplicate"); ok || duplicate != ready {
		t.Fatalf("duplicate completion accepted or changed state: ok=%t state=%+v", ok, duplicate)
	}
}

func TestExpiredAttemptCannotFinalizeAndTakeoverInvalidatesIt(t *testing.T) {
	state := confirm(t, State{}, publication("G1", "D", 1))
	state, expired := claim(t, state, "attempt-1", testTime)
	at := testTime.Add(time.Minute)
	if _, ok := state.CompleteAttempt(expired, at, "late"); ok {
		t.Fatal("expired attempt completed")
	}

	state, retry := claim(t, state, "attempt-2", at)
	if state.Work.ClaimCount != 2 || state.Work.RetryCount != 1 || retry.ID != "attempt-2" {
		t.Fatalf("expired claim takeover did not consume one retry: %+v", state.Work)
	}
	if _, ok := state.CompleteAttempt(expired, at.Add(time.Second), "late"); ok {
		t.Fatal("superseded attempt completed after takeover")
	}
	if _, ok := state.CompleteAttempt(retry, at.Add(time.Second), "current"); !ok {
		t.Fatal("current takeover attempt was rejected")
	}
}
