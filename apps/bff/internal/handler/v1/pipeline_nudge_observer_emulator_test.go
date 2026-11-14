package v1

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
)

func TestPipelineNudgeLegacyBootstrapAndSequencedRecovery(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	ownerID, projectID := "nudge-pipeline-owner", fmt.Sprintf("pipeline-nudge-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(ownerID + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": "Legacy Archive"}); err != nil {
		t.Fatalf("seed owner Project: %v", err)
	}
	quotaRef := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(ownerID, projectID))
	if _, err := quotaRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "project_pipeline_sequence": int64(7)}); err != nil {
		t.Fatalf("seed legacy quota sequence: %v", err)
	}
	repository := nudge.NewRepository(fsClient.Raw())
	handler := &Handler{firestore: fsClient}
	var current pipelineEventSnapshot
	handler.pipelineEventSource = func(_ context.Context, _, currentProject string) (pipelineEventSnapshot, error) {
		if currentProject != projectID {
			return pipelineEventSnapshot{}, errors.New("unexpected test Project")
		}
		return current, nil
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	legacy := pipelineEventRunRecord{id: "legacy-run", status: "FAILED", startedAt: now.Add(-time.Hour),
		actualStarted: now.Add(-time.Hour), endedAt: now.Add(-time.Minute), publication: "confirmed_not_published"}
	refresh := func(records map[string]pipelineEventRunRecord) scopedfirestore.PipelineEventCandidateSet {
		t.Helper()
		candidates, err := scopedfirestore.LoadPipelineEventCandidates(ctx, fsClient.Raw(), ownerID, projectID)
		if err != nil {
			t.Fatalf("load source candidate snapshot: %v", err)
		}
		current = makePipelineEventSnapshot(projectID, records, candidateIDs(candidates.Known), candidatePending(candidates.Pending), false,
			pipelineEventGlobalLimit, 0, false, nil, candidates.UnresolvedCount)
		current.records, current.candidates = records, candidates
		failures, _ := handler.reconcilePipelineFailureNudges(ctx, ownerID, repository)
		if len(failures) != 0 {
			t.Fatalf("Pipeline reconciliation failures: %+v", failures)
		}
		return candidates
	}
	refresh(map[string]pipelineEventRunRecord{legacy.id: legacy})
	slot, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || !slot.Active || slot.ProducerWitness.ProjectPipelineSequence != 7 || slot.ProducerWitness.BootstrapFloor != 7 || slot.ProducerWitness.BootstrapDigest == "" {
		t.Fatalf("singleton legacy failure was not atomically bootstrapped: slot=%+v err=%v", slot, err)
	}
	quota, err := quotaRef.Get(ctx)
	if err != nil || quota.Data()["project_pipeline_sequence"] != int64(7) || quota.Data()["pipeline_bootstrap_floor"] != int64(7) {
		t.Fatalf("bootstrap did not preserve and persist quota floor: data=%v err=%v", quota.Data(), err)
	}
	view, err := repository.Read(ctx, ownerID, nil, nil)
	if err != nil || len(view.Items) != 1 || len(view.Items[0].Observations) != 1 {
		t.Fatalf("legacy failure is not visible in common Nudge view: view=%+v err=%v", view, err)
	}
	observation := view.Items[0].Observations[0].Observation
	if observation == nil || observation.ProjectName != "Legacy Archive" || observation.ExecutionID != "legacy-run" || len(observation.Actions) != 3 {
		t.Fatalf("bootstrap lost exact Project/run actions: %+v", observation)
	}
	initialCategory, err := scopedfirestore.Collection(fsClient.Raw(), "user_nudges").Doc(ownerID).Collection("categories").Doc(nudge.CategoryPipelineFailure).Get(ctx)
	if err != nil {
		t.Fatalf("read initial attention state: %v", err)
	}
	attentionID, _ := initialCategory.Data()["attention_id"].(string)
	if err := repository.PatchAttention(ctx, ownerID, nudge.CategoryPipelineFailure, attentionID, "dismiss"); err != nil {
		t.Fatalf("dismiss yellow attention: %v", err)
	}
	refresh(map[string]pipelineEventRunRecord{legacy.id: legacy})
	slot, err = repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	categoryAfterReplay, categoryErr := scopedfirestore.Collection(fsClient.Raw(), "user_nudges").Doc(ownerID).Collection("categories").Doc(nudge.CategoryPipelineFailure).Get(ctx)
	if err != nil || categoryErr != nil || !slot.Active || slot.SourceRevision != 1 || categoryAfterReplay.Data()["attention_id"] != attentionID || categoryAfterReplay.Data()["attention_state"] != string(nudge.AttentionDismissed) {
		t.Fatalf("legacy replay changed source or dismissed attention: slot=%+v category=%v err=%v", slot, categoryAfterReplay.Data(), err)
	}

	limits := pipelinequota.Limits{DailyLimit: 20, Cooldown: time.Hour, MinNewRaw: 1}
	reserve := func(sequence int64, runStatus, publication string, at time.Time) pipelineEventRunRecord {
		t.Helper()
		reservationID := fmt.Sprintf("reservation-%d", sequence)
		runID := fmt.Sprintf("run-%d", sequence)
		if _, accepted, err := fsClient.ReserveQuota(ctx, reservationID, ownerID, projectID, limits, at, pipelinequota.ModeFull, false, false, 1, 0, 0); err != nil || !accepted {
			t.Fatalf("reserve sequence %d accepted=%t err=%v", sequence, accepted, err)
		}
		reservation, exists, err := fsClient.GetQuotaReservation(ctx, reservationID)
		if err != nil || !exists || reservation.ProjectPipelineSequence != sequence || !reservation.ProjectPipelineSequencePresent || reservation.Mode != pipelinequota.ModeFull || reservation.AttemptID != reservationID {
			t.Fatalf("reservation sequence metadata=%+v exists=%t err=%v", reservation, exists, err)
		}
		if err := fsClient.LinkQuotaReservation(ctx, reservationID, runID); err != nil {
			t.Fatalf("link sequence %d run: %v", sequence, err)
		}
		end := time.Time{}
		if runStatus != "RUNNING" {
			end = at.Add(time.Minute)
		}
		return pipelineEventRunRecord{id: runID, status: runStatus, startedAt: at, actualStarted: at, endedAt: end,
			publication: publication, reservationID: reservationID, attemptID: reservation.AttemptID, trigger: string(reservation.Trigger),
			mode: string(reservation.Mode), sequence: reservation.ProjectPipelineSequence, sequenceSet: true}
	}
	finish := func(run pipelineEventRunRecord, state string) {
		t.Helper()
		if _, err := fsClient.SettleQuotaReservation(ctx, run.reservationID, state); err != nil {
			t.Fatalf("settle %s: %v", run.reservationID, err)
		}
	}
	run8 := reserve(8, "RUNNING", "unknown", now.Add(2*time.Hour))
	refresh(map[string]pipelineEventRunRecord{legacy.id: legacy, run8.id: run8})
	slot, err = repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || !slot.Active || slot.ProducerWitness.ExecutionID != "legacy-run" {
		t.Fatalf("new RUNNING sequence cleared the bootstrapped failure: slot=%+v err=%v", slot, err)
	}
	finish(run8, "CANCELLED")
	run9 := reserve(9, "SUCCEEDED", "confirmed_published", now.Add(4*time.Hour))
	finish(run9, "SUCCEEDED")
	run10 := reserve(10, "RUNNING", "unknown", now.Add(6*time.Hour))
	// The panel remains closed through both publication and the later running attempt.
	combined := map[string]pipelineEventRunRecord{legacy.id: legacy, run8.id: withStatus(run8, "CANCELLED", "unknown"),
		run9.id: run9, run10.id: run10}
	refresh(combined)
	slot, err = repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || slot.Active || slot.ProducerWitness.ExecutionID != run9.id || slot.ProducerWitness.TerminalClass != "recovered" {
		t.Fatalf("published sequence did not clear failure before later RUNNING: slot=%+v err=%v", slot, err)
	}
	finish(run10, "CANCELLED")
	run11 := reserve(11, "FAILED", "confirmed_not_published", now.Add(8*time.Hour))
	latest := withStatus(run11, "FAILED", "confirmed_not_published")
	refresh(map[string]pipelineEventRunRecord{legacy.id: legacy, run8.id: withStatus(run8, "CANCELLED", "unknown"),
		run9.id: run9, run10.id: withStatus(run10, "CANCELLED", "unknown"), run11.id: latest})
	slot, err = repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || !slot.Active || slot.ProducerWitness.ExecutionID != run11.id || slot.ProducerWitness.ProjectPipelineSequence != 11 {
		t.Fatalf("newer failure did not replace recovery tombstone: slot=%+v err=%v", slot, err)
	}
	run11Published := withStatus(run11, "FAILED", "confirmed_published")
	refresh(map[string]pipelineEventRunRecord{legacy.id: legacy, run11Published.id: run11Published})
	slot, err = repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || slot.Active || slot.ProducerWitness.ExecutionID != run11.id || slot.ProducerWitness.TerminalClass != "recovered" {
		t.Fatalf("same-run publication confirmation did not beat its failed witness: slot=%+v err=%v", slot, err)
	}
	finish(run11, "SUCCEEDED")
	run12 := reserve(12, "FAILED", "confirmed_not_published", now.Add(10*time.Hour))
	latest = withStatus(run12, "FAILED", "confirmed_not_published")
	refresh(map[string]pipelineEventRunRecord{legacy.id: legacy, run11Published.id: run11Published, run12.id: latest})
	slot, err = repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || !slot.Active || slot.ProducerWitness.ExecutionID != run12.id || slot.SourceRevision != 5 {
		t.Fatalf("later failure did not replace same-run recovery: slot=%+v err=%v", slot, err)
	}
	staleWitness, err := pipelineProducerWitness(projectID, run9, slot.ProducerWitness, "recovered")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ApplyClear(ctx, ownerID, nudge.ClearInput{Category: nudge.CategoryPipelineFailure,
		SourceKey: "pipeline-fail:" + projectID, ExpectedSourceRevision: slot.SourceRevision - 1,
		ProducerWitness: staleWitness, Proof: "confirmed-recovery"}); !errors.Is(err, nudge.ErrSourceRevisionConflict) {
		t.Fatalf("delayed old clear err=%v, want source revision conflict", err)
	}
	slot, err = repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if err != nil || !slot.Active || slot.ProducerWitness.ExecutionID != run12.id {
		t.Fatalf("delayed clear replaced newer failure: slot=%+v err=%v", slot, err)
	}
	finalView, err := repository.Read(ctx, ownerID, nil, nil)
	if err != nil || len(finalView.Items) != 1 || finalView.Items[0].Observations[0].Observation.ProjectName != "Legacy Archive" {
		t.Fatalf("new failure does not retain exact Project grouping: view=%+v err=%v", finalView, err)
	}
}

func TestPipelineNudgeObserverKeepsUnknownAmbiguousAndTruncatedLegacyPartial(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	ownerID, projectID := "nudge-pipeline-partial-owner", fmt.Sprintf("pipeline-partial-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(ownerID + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": "Partial Project"}); err != nil {
		t.Fatalf("seed owner Project: %v", err)
	}
	repository := nudge.NewRepository(fsClient.Raw())
	handler := &Handler{firestore: fsClient}
	var current pipelineEventSnapshot
	handler.pipelineEventSource = func(context.Context, string, string) (pipelineEventSnapshot, error) { return current, nil }
	now := time.Now().UTC()
	failure := pipelineEventRunRecord{id: "legacy-fail", status: "FAILED", startedAt: now.Add(-time.Hour),
		actualStarted: now.Add(-time.Hour), endedAt: now.Add(-time.Minute), publication: "confirmed_not_published"}
	unknown := failure
	unknown.publication = "unknown"
	recovered := pipelineEventRunRecord{id: "legacy-success", status: "SUCCEEDED", startedAt: now,
		actualStarted: now, endedAt: now.Add(time.Minute), publication: "confirmed_published"}
	for _, test := range []struct {
		name       string
		records    map[string]pipelineEventRunRecord
		truncated  bool
		wantNotice string
	}{
		{name: "unknown publication", records: map[string]pipelineEventRunRecord{unknown.id: unknown}, wantNotice: "pipeline_terminal_unconfirmed"},
		{name: "ambiguous legacy order", records: map[string]pipelineEventRunRecord{failure.id: failure, recovered.id: recovered}, wantNotice: "pipeline_legacy_order_ambiguous"},
		{name: "truncated history", records: map[string]pipelineEventRunRecord{failure.id: failure}, truncated: true, wantNotice: "pipeline_history_partial"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidates, err := scopedfirestore.LoadPipelineEventCandidates(ctx, fsClient.Raw(), ownerID, projectID)
			if err != nil {
				t.Fatalf("load source candidates: %v", err)
			}
			current = makePipelineEventSnapshot(projectID, test.records, nil, nil, false, pipelineEventGlobalLimit, 0, test.truncated, nil, candidates.UnresolvedCount)
			current.records, current.candidates = test.records, candidates
			failures, _ := handler.reconcilePipelineProjectFailure(ctx, repository, ownerID, projectID, "Partial Project", nudge.Slot{SchemaVersion: nudge.SchemaVersion, SourceKey: "pipeline-fail:" + projectID})
			if len(failures) != 1 || failures[0].SafeClass != test.wantNotice {
				t.Fatalf("failure disposition=%+v, want safe class %s", failures, test.wantNotice)
			}
			slot, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
			if err != nil || slot.Active || slot.SourceRevision != 0 {
				t.Fatalf("incomplete evidence created a source row: slot=%+v err=%v", slot, err)
			}
		})
	}
	// A complete legacy source set with a confirmed publication is already
	// recovered; it must not create a retroactive attention row.
	candidates, err := scopedfirestore.LoadPipelineEventCandidates(ctx, fsClient.Raw(), ownerID, projectID)
	if err != nil {
		t.Fatalf("load recovered legacy candidates: %v", err)
	}
	current = makePipelineEventSnapshot(projectID, map[string]pipelineEventRunRecord{recovered.id: recovered}, nil, nil,
		false, pipelineEventGlobalLimit, 0, false, nil, candidates.UnresolvedCount)
	current.records, current.candidates = map[string]pipelineEventRunRecord{recovered.id: recovered}, candidates
	failures, notices := handler.reconcilePipelineFailureNudges(ctx, ownerID, repository)
	slot, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	if len(failures) != 0 || len(notices) != 0 || err != nil || slot.Active || slot.SourceRevision != 0 {
		t.Fatalf("recovered legacy history created attention/source state: failures=%+v notices=%+v slot=%+v err=%v", failures, notices, slot, err)
	}
}

func TestPipelineNudgeBootstrapRacesReobserveBeforeSeeding(t *testing.T) {
	if !requireLoopbackEmulator(t, "FIRESTORE_EMULATOR_HOST") {
		t.Skip("requires loopback Firestore emulator")
	}
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fsClient := newContentContextEmulator(t)
	defer fsClient.Close()
	ownerID, projectID := "nudge-bootstrap-race-owner", fmt.Sprintf("pipeline-race-%d", time.Now().UnixNano())
	projectRef := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(ownerID + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "name": "Raced Project"}); err != nil {
		t.Fatalf("seed owner Project: %v", err)
	}
	quotaRef := scopedfirestore.Collection(fsClient.Raw(), "pipeline_quota").Doc(scopedfirestore.QuotaDocID(ownerID, projectID))
	if _, err := quotaRef.Set(ctx, map[string]interface{}{"user_id": ownerID, "project_id": projectID, "project_pipeline_sequence": int64(7)}); err != nil {
		t.Fatalf("seed quota floor: %v", err)
	}
	repository := nudge.NewRepository(fsClient.Raw())
	handler := &Handler{firestore: fsClient}
	now := time.Now().UTC().Truncate(time.Microsecond)
	legacy := pipelineEventRunRecord{id: "legacy-fail", status: "FAILED", startedAt: now.Add(-time.Hour),
		actualStarted: now.Add(-time.Hour), endedAt: now.Add(-time.Minute), publication: "confirmed_not_published"}
	var stale pipelineEventSnapshot
	initialCandidates, err := scopedfirestore.LoadPipelineEventCandidates(ctx, fsClient.Raw(), ownerID, projectID)
	if err != nil {
		t.Fatalf("load initial source candidates: %v", err)
	}
	stale = makePipelineEventSnapshot(projectID, map[string]pipelineEventRunRecord{legacy.id: legacy}, nil, nil,
		false, pipelineEventGlobalLimit, 0, false, nil, initialCandidates.UnresolvedCount)
	stale.records, stale.candidates = map[string]pipelineEventRunRecord{legacy.id: legacy}, initialCandidates
	var sourceCalls int
	handler.pipelineEventSource = func(ctx context.Context, _, currentProject string) (pipelineEventSnapshot, error) {
		sourceCalls++
		if currentProject != projectID {
			return pipelineEventSnapshot{}, errors.New("unexpected race-test Project")
		}
		if sourceCalls == 1 {
			// Reserve a new run after the bounded scan, invalidating its quota
			// updateTime and sequence snapshot before the bootstrap transaction.
			if _, accepted, err := fsClient.ReserveQuota(ctx, "race-reservation", ownerID, projectID,
				pipelinequota.Limits{DailyLimit: 10, Cooldown: time.Hour, MinNewRaw: 1}, now,
				pipelinequota.ModeFull, false, false, 1, 0, 0); err != nil || !accepted {
				return pipelineEventSnapshot{}, fmt.Errorf("reserve competing run accepted=%t: %w", accepted, err)
			}
			if err := fsClient.LinkQuotaReservation(ctx, "race-reservation", "race-run"); err != nil {
				return pipelineEventSnapshot{}, err
			}
			return stale, nil
		}
		candidates, err := scopedfirestore.LoadPipelineEventCandidates(ctx, fsClient.Raw(), ownerID, projectID)
		if err != nil {
			return pipelineEventSnapshot{}, err
		}
		reservation, exists, err := fsClient.GetQuotaReservation(ctx, "race-reservation")
		if err != nil || !exists {
			return pipelineEventSnapshot{}, fmt.Errorf("read competing reservation exists=%t: %w", exists, err)
		}
		run := pipelineEventRunRecord{id: "race-run", status: "RUNNING", startedAt: now, actualStarted: now,
			publication: "unknown", reservationID: reservation.ID, attemptID: reservation.AttemptID,
			trigger: string(reservation.Trigger), mode: string(reservation.Mode), sequence: reservation.ProjectPipelineSequence,
			sequenceSet: reservation.ProjectPipelineSequencePresent}
		records := map[string]pipelineEventRunRecord{legacy.id: legacy, run.id: run}
		snapshot := makePipelineEventSnapshot(projectID, records, candidateIDs(candidates.Known), candidatePending(candidates.Pending),
			false, pipelineEventGlobalLimit, 0, false, nil, candidates.UnresolvedCount)
		snapshot.records, snapshot.candidates = records, candidates
		return snapshot, nil
	}
	failures, _ := handler.reconcilePipelineFailureNudges(ctx, ownerID, repository)
	if sourceCalls != 2 || len(failures) != 1 || failures[0].SafeClass != "pipeline_legacy_order_ambiguous" {
		t.Fatalf("race was not re-observed and rejected: calls=%d failures=%+v", sourceCalls, failures)
	}
	slot, err := repository.ReadSourceSlot(ctx, ownerID, nudge.CategoryPipelineFailure, "pipeline-fail:"+projectID)
	quota, quotaErr := quotaRef.Get(ctx)
	if err != nil || quotaErr != nil || slot.Active || slot.SourceRevision != 0 || quota.Data()["pipeline_bootstrap_floor"] != nil || quota.Data()["project_pipeline_sequence"] != int64(8) {
		t.Fatalf("pre-race legacy snapshot seeded after concurrent reservation: slot=%+v quota=%v err=%v quota_err=%v", slot, quota.Data(), err, quotaErr)
	}
}

func withStatus(record pipelineEventRunRecord, status, publication string) pipelineEventRunRecord {
	record.status, record.publication = status, publication
	if status == "RUNNING" {
		record.endedAt = time.Time{}
	} else if record.endedAt.IsZero() {
		record.endedAt = record.actualStarted.Add(time.Minute)
	}
	return record
}
