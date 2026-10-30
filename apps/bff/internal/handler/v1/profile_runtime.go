package v1

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	cloudstorage "cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/documentimport"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"github.com/rayer/llm-wiki-bff/internal/sourceautomation"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type profileRuntimeLeaseKey struct{}
type profileRuntimeLease struct {
	ref                      *firestore.DocumentRef
	token, userID, projectID string
}

var errProfileRuntimeLease = errors.New("profile execution lease is not current")
var errProfileRuntimeWaiting = errors.New("profile compile waiting for manual work")

func validateProfileRuntimeLease(ctx context.Context, tx *firestore.Transaction, userID, projectID string, now time.Time) error {
	lease, ok := ctx.Value(profileRuntimeLeaseKey{}).(profileRuntimeLease)
	if !ok {
		return nil
	}
	if lease.userID != userID || lease.projectID != projectID {
		return errProfileRuntimeLease
	}
	snapshot, err := tx.Get(lease.ref)
	if err != nil {
		return err
	}
	var work profileruntime.Work
	if err = snapshot.DataTo(&work); err != nil {
		return err
	}
	if !work.Pending || work.Token != lease.token || !work.LeaseUntil.After(now) {
		return errProfileRuntimeLease
	}
	return nil
}
func enqueueProfileDerivation(ctx context.Context, tx *firestore.Transaction, client *firestore.Client, userID, projectID string, intent profileDerivationIntent) error {
	if intent.ScheduledFor == nil {
		return errors.New("missing derivation schedule")
	}
	due, err := time.Parse(time.RFC3339Nano, *intent.ScheduledFor)
	if err != nil {
		return err
	}
	return profileruntime.Enqueue(tx, client, profileruntime.Work{UserID: userID, ProjectID: projectID, Kind: "derive", Revision: intent.Revision, ID: intent.AttemptID, RequestID: observability.RequestIDFromContext(ctx), Due: due})
}
func enqueueProfileTagging(ctx context.Context, tx *firestore.Transaction, client *firestore.Client, userID, projectID string, revision int64, job storedProfileJob) error {
	if job.ScheduledFor == nil {
		return errors.New("missing tagging schedule")
	}
	due, err := time.Parse(time.RFC3339Nano, *job.ScheduledFor)
	if err != nil {
		return err
	}
	return profileruntime.Enqueue(tx, client, profileruntime.Work{UserID: userID, ProjectID: projectID, Kind: "tag", Revision: revision, ID: job.JobID, CandidateID: job.CandidateID, RequestID: observability.RequestIDFromContext(ctx), Due: due})
}

// ProfileDispatcher is invoked by an external durable scheduler. It never
// starts a background timer. The identity validator is injectable for local HTTP tests.
type ProfileDispatcher struct {
	Handler                  *Handler
	Provider                 *profilederive.Provider
	Evaluator                profiletags.Evaluator
	Policy                   profiletags.ProviderPolicy
	Audience, ServiceAccount string
	ValidateIdentity         func(context.Context, string, string) (*idtoken.Payload, error)
}

func (d *ProfileDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := observability.StartProfileSpan(r.Context(), "profile.dispatch")
	outcome := "success"
	defer func() { observability.FinishProfileSpan(span, outcome) }()
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		outcome = "rejected"
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if d.Audience == "" || d.ServiceAccount == "" {
		outcome = "failure"
		http.Error(w, "Profile runtime is not configured", 503)
		return
	}
	bearer := r.Header.Get("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(bearer, "Bearer ")) == "" {
		outcome = "rejected"
		http.Error(w, "unauthorized", 401)
		return
	}
	validate := d.ValidateIdentity
	if validate == nil {
		validate = idtoken.Validate
	}
	payload, err := validate(r.Context(), strings.TrimPrefix(bearer, "Bearer "), d.Audience)
	if err != nil || payload == nil || payload.Audience != d.Audience || payload.Claims["email"] != d.ServiceAccount || payload.Claims["email_verified"] != true {
		outcome = "rejected"
		http.Error(w, "unauthorized", 401)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), profileruntime.ExecutionTimeout)
	defer cancel()
	count, err := d.Dispatch(ctx, 8)
	if err != nil {
		outcome = "failure"
		http.Error(w, "Profile dispatch unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"processed":%d}`, count)
}
func (d *ProfileDispatcher) Gin(c *gin.Context) { d.ServeHTTP(c.Writer, c.Request) }

// Dispatch reads only due work. Pending work remains durable across crashes;
// a lease expiry allows bounded recovery and fences every Profile transaction.
func (d *ProfileDispatcher) Dispatch(ctx context.Context, limit int) (processed int, resultErr error) {
	ctx, dispatchSpan := observability.StartProfileSpan(ctx, "profile.dispatch.sweep")
	defer func() {
		observability.SetProfileCount(dispatchSpan, "profile.processed_count", processed)
		outcome := "success"
		if resultErr != nil {
			outcome = "failure"
		}
		observability.FinishProfileSpan(dispatchSpan, outcome)
	}()
	repo, ok := d.Handler.profileRepository.(*firestoreProfileRepository)
	if !ok || limit < 1 || limit > 100 {
		return 0, errors.New("Profile dispatcher repository unavailable")
	}
	iter := scopedfirestore.Collection(repo.client, profileruntime.WorkCollection).Where("pending", "==", true).Where("due", "<=", repo.now()).OrderBy("due", firestore.Asc).Limit(limit).Documents(ctx)
	defer iter.Stop()
	count := 0
	for {
		snapshot, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		claimCtx, claimSpan := observability.StartProfileSpan(ctx, "profile.claim")
		work, claimed, err := d.claim(claimCtx, repo, snapshot.Ref)
		claimOutcome := "success"
		if err != nil {
			claimOutcome = "failure"
		} else if claimed {
			claimOutcome = "committed"
		} else if work.Status == "exhausted" {
			claimOutcome = "committed"
		} else {
			claimOutcome = "no_op"
		}
		if err == nil {
			observability.SetRequestIDAttribute(claimSpan, work.RequestID)
			if !claimed && work.Status == "exhausted" {
				terminalCtx := observability.WithRequestID(observability.WithoutRequestID(claimCtx), work.RequestID)
				_, terminalSpan := observability.StartProfileWorkTerminalSpan(terminalCtx)
				observability.SetProfileOutcome(terminalSpan, "profile.claim.outcome", "committed")
				observability.SetProfileStatus(terminalSpan, work.Status)
				observability.SetProfileWorkKind(terminalSpan, work.Kind)
				observability.FinishProfileSpan(terminalSpan, "failure")
			}
		}
		observability.SetProfileOutcome(claimSpan, "profile.claim.outcome", claimOutcome)
		observability.SetProfileStatus(claimSpan, work.Status)
		observability.SetProfileWorkKind(claimSpan, work.Kind)
		observability.FinishProfileSpan(claimSpan, map[bool]string{true: "failure", false: "success"}[err != nil])
		if err != nil {
			return count, err
		}
		if !claimed {
			continue
		}
		lease := profileRuntimeLease{snapshot.Ref, work.Token, work.UserID, work.ProjectID}
		workCtx := observability.WithRequestID(observability.WithoutRequestID(claimCtx), work.RequestID)
		attemptCtx, attemptSpan := observability.StartProfileWorkSpan(workCtx)
		observability.SetProfileCount(attemptSpan, "profile.attempt_number", work.Attempts)
		observability.SetProfileWorkKind(attemptSpan, work.Kind)
		runctx := context.WithValue(attemptCtx, profileRuntimeLeaseKey{}, lease)
		executeCtx, executeSpan := observability.StartProfileSpan(runctx, "profile.execute")
		executeErr := d.execute(executeCtx, repo, work)
		executeOutcome := "success"
		if executeErr != nil {
			executeOutcome = "failure"
		}
		observability.SetProfileOutcome(attemptSpan, "profile.execution.outcome", executeOutcome)
		observability.FinishProfileSpan(executeSpan, executeOutcome)
		finishCtx, finishSpan := observability.StartProfileSpan(attemptCtx, "profile.finish")
		finishStatus, finishErr := d.finish(finishCtx, repo, snapshot.Ref, work, executeErr)
		if finishErr == nil {
			commitOutcome := "committed"
			attemptOutcome := "success"
			if finishStatus != "complete" {
				commitOutcome, attemptOutcome = "no_op", "no_op"
				if finishStatus == "retry_wait" {
					commitOutcome = "not_committed"
				}
			}
			observability.SetProfileOutcome(attemptSpan, "profile.commit.outcome", commitOutcome)
			observability.SetProfileOutcome(attemptSpan, "profile.execution.outcome", executeOutcome)
			observability.SetProfileStatus(attemptSpan, finishStatus)
			observability.SetProfileStatus(finishSpan, finishStatus)
			observability.FinishProfileSpan(finishSpan, attemptOutcome)
			observability.FinishProfileSpan(attemptSpan, attemptOutcome)
		} else {
			observability.SetProfileOutcome(attemptSpan, "profile.commit.outcome", "unknown")
			observability.SetProfileStatus(finishSpan, finishStatus)
			observability.FinishProfileSpan(finishSpan, "failure")
			observability.FinishProfileSpan(attemptSpan, "failure")
		}
		if finishErr != nil {
			return count, finishErr
		}
		count++
	}
}
func (d *ProfileDispatcher) claim(ctx context.Context, repo *firestoreProfileRepository, ref *firestore.DocumentRef) (profileruntime.Work, bool, error) {
	var work profileruntime.Work
	claimed := false
	token := scopedfirestore.Collection(repo.client, profileruntime.WorkCollection).NewDoc().ID
	err := repo.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		claimed = false
		snapshot, err := tx.Get(ref)
		if err != nil {
			return err
		}
		if err = snapshot.DataTo(&work); err != nil {
			return err
		}
		now := repo.now()
		if !work.Pending || work.Due.After(now) || work.LeaseUntil.After(now) {
			return nil
		}
		if profileruntime.WorkID(work) != ref.ID || !validProfileRuntimeIdentity(work) {
			work.Pending = false
			work.Status = "invalid"
			return tx.Set(ref, work)
		}
		if work.Attempts >= profileruntime.MaxAttempts {
			if err := markProfileRuntimeExhausted(ctx, tx, repo, work); err != nil {
				return err
			}
			work.Pending = false
			work.Status = "exhausted"
			return tx.Set(ref, work)
		}
		work.Attempts++
		work.Token = token
		work.LeaseUntil = now.Add(profileruntime.LeaseDuration)
		work.Due = work.LeaseUntil
		work.Status = "running"
		claimed = true
		return tx.Set(ref, work)
	})
	return work, claimed, err
}
func validProfileRuntimeIdentity(w profileruntime.Work) bool {
	if !auth.ValidPathSegment(w.UserID) || !auth.ValidPathSegment(w.ProjectID) || !auth.ValidPathSegment(w.ID) {
		return false
	}
	if w.Kind == "pipeline" || w.Kind == "pipeline_automation" {
		return w.Revision >= 0
	}
	if w.Kind == "document_import" {
		return w.Revision > 0 && auth.ValidPathSegment(w.DocumentID)
	}
	if w.Kind == "pipeline_automation_scan" {
		return w.UserID == "system" && w.ProjectID == "automation" && w.Revision > 0
	}
	if w.Kind == "source_commit_reconcile" {
		return w.Revision == 0
	}
	return w.Revision > 0 && (w.Kind == "derive" || w.Kind == "tag" || w.Kind == "compile")
}
func (d *ProfileDispatcher) finish(ctx context.Context, repo *firestoreProfileRepository, ref *firestore.DocumentRef, claimed profileruntime.Work, cause error) (string, error) {
	settledStatus := "unknown"
	err := repo.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		settledStatus = "unknown"
		snapshot, err := tx.Get(ref)
		if err != nil {
			return err
		}
		var w profileruntime.Work
		if err = snapshot.DataTo(&w); err != nil {
			return err
		}
		if w.Token != claimed.Token {
			return errProfileRuntimeLease
		}
		w.LeaseUntil = time.Time{}
		w.Token = ""
		var conflict *profileRevisionConflict
		if errors.Is(cause, errProfileRuntimeWaiting) {
			w.Attempts--
			w.Status = "waiting_manual"
			w.Due = repo.now().Add(time.Minute)
			if w.Kind == "document_import" {
				documentRef := scopedfirestore.Collection(repo.client, "projects").Doc(projectDocID(w.UserID, w.ProjectID)).Collection("document_imports").Doc(w.DocumentID)
				documentSnapshot, readErr := tx.Get(documentRef)
				if readErr != nil && status.Code(readErr) != codes.NotFound {
					return readErr
				}
				if readErr == nil {
					var record documentimport.Record
					if err := documentSnapshot.DataTo(&record); err != nil {
						return err
					}
					if record.AttemptID == w.ID && record.LeaseUntil.After(repo.now()) {
						w.Status = "waiting_worker"
						w.Due = record.LeaseUntil
					}
				}
			}
		} else if cause == nil {
			if w.Kind == "document_import" {
				documentRef := scopedfirestore.Collection(repo.client, "projects").Doc(projectDocID(w.UserID, w.ProjectID)).Collection("document_imports").Doc(w.DocumentID)
				documentSnapshot, err := tx.Get(documentRef)
				if err != nil && status.Code(err) != codes.NotFound {
					return err
				}
				var record documentimport.Record
				if err == nil {
					if err := documentSnapshot.DataTo(&record); err != nil {
						return err
					}
				}
				if err == nil && documentImportWorkRecoverable(record, w.ID) {
					w.Pending = true
					w.Status = "retry_wait"
					w.Due = repo.now().Add(profileruntime.LeaseDuration)
					if record.LeaseUntil.After(repo.now()) {
						w.Attempts--
						w.Status = "waiting_worker"
						w.Due = record.LeaseUntil
					}
				} else {
					w.Pending = false
					w.Status = "complete"
				}
			} else {
				w.Pending = false
				w.Status = "complete"
			}
		} else if errors.As(cause, &conflict) || errors.Is(cause, errProfileCandidateNotCurrent) || errors.Is(cause, errProfileProjectNotFound) {
			if w.Kind == "compile" {
				if err := clearSupersededProfileCompileRetry(ctx, tx, repo, w); err != nil {
					return err
				}
			}
			w.Pending = false
			w.Status = "superseded"
		} else if w.Attempts >= profileruntime.MaxAttempts {
			if err := markProfileRuntimeExhausted(ctx, tx, repo, w); err != nil {
				return err
			}
			w.Pending = false
			w.Status = "exhausted"
		} else {
			w.Status = "retry_wait"
			w.Due = repo.now().Add(time.Duration(w.Attempts) * time.Minute)
		}
		if err := tx.Set(ref, w); err != nil {
			return err
		}
		settledStatus = w.Status
		return nil
	})
	if err != nil {
		return "unknown", err
	}
	return settledStatus, nil
}
func (d *ProfileDispatcher) execute(ctx context.Context, repo *firestoreProfileRepository, w profileruntime.Work) error {
	if w.Kind == "source_commit_reconcile" {
		journal := d.Handler.sourceCommitJournal()
		if journal == nil {
			return errProfileRuntimeWaiting
		}
		_, err := journal.ReconcileSourceCommit(ctx, sourceautomation.Ref{
			OwnerID: w.UserID, ProjectID: w.ProjectID, OperationID: w.ID,
		})
		if err == nil || errors.Is(err, sourceautomation.ErrInvalidSourceCommit) || errors.Is(err, sourceautomation.ErrSourceCommitConflict) {
			return err
		}
		// Reconciliation owns transient Firestore and raw-storage failures. Keep
		// its durable work pending until the read succeeds or the prepared event's
		// missing-object grace window closes.
		return errProfileRuntimeWaiting
	}
	if w.Kind == "pipeline_automation_scan" {
		return d.executePipelineAutomationScan(ctx, repo, w)
	}
	if w.Kind == "pipeline_automation" {
		if _, err := d.Handler.AuthorizeProject(ctx, w.UserID, w.ProjectID, ProjectEdit); err != nil {
			return err
		}
		if err := d.executePipelineAutomation(ctx, repo, w); err != nil {
			return err
		}
		return d.associateDocumentImportsWithPipeline(ctx, repo, w.UserID, w.ProjectID)
	}
	if _, err := d.Handler.AuthorizeProject(ctx, w.UserID, w.ProjectID, ProjectEdit); err != nil {
		return err
	}
	if w.Kind == "document_import" {
		return d.executeDocumentImport(ctx, repo, w)
	}
	projectClient, _, err := d.Handler.projectLLMClient(ctx, w.UserID, w.ProjectID)
	if err != nil {
		return err
	}
	if projectClient != nil {
		defer func() {
			if closer, ok := projectClient.(interface{ Close() }); ok {
				closer.Close()
			}
		}()
		ctx = llm.WithProjectChatClient(ctx, projectClient)
	}
	if w.Kind == "pipeline" {
		return d.executePipelineIntent(ctx, repo, w)
	}
	if w.Kind == "compile" {
		state, err := repo.GetProfile(ctx, w.UserID, w.ProjectID)
		if err != nil {
			return err
		}
		if state.Revision < w.Revision {
			return &profileRevisionConflict{Latest: state.Revision}
		}
		if state.DerivationStatus != nil {
			if *state.DerivationStatus == profileDerivationPending && state.CompileRetryGeneration != w.ID || *state.DerivationStatus == profileDerivationFailed {
				return errProfileRuntimeWaiting
			}
		}
		if c := state.Candidate; c != nil && c.Source == "manual" && (state.Active == nil || state.Active.CandidateID != c.CandidateID) {
			return errProfileRuntimeWaiting
		}
		return d.executeCompile(ctx, repo, w)
	}
	if err := prepareProfileRuntimeAttempt(ctx, repo, w); err != nil {
		return err
	}
	if w.Kind == "derive" {
		_, err := d.Handler.RunProfileDerivation(ctx, w.UserID, w.ProjectID, w.Revision, w.ID, d.Provider)
		return err
	}
	return d.Handler.RunProfileTagging(ctx, w.UserID, w.ProjectID, w.Revision, w.CandidateID, w.ID, d.Evaluator, d.Policy)
}

func (d *ProfileDispatcher) associateDocumentImportsWithPipeline(ctx context.Context, repo *firestoreProfileRepository, ownerID, projectID string) error {
	if d.Handler.documentImports == nil || d.Handler.documentImports.Repository == nil || d.Handler.store == nil {
		return nil
	}
	intentStore, ok := d.Handler.effectiveQuotaStore().(pipelineIntentQuotaStore)
	if !ok {
		return nil
	}
	reservation, exists, err := latestDocumentSourceCommitIntent(ctx, repo, intentStore, ownerID, projectID)
	if err != nil || !exists {
		return err
	}
	if reservation.SourceRevision <= 0 {
		return nil
	}
	objects, ok := d.Handler.store.Scope(ownerID, projectID).(storage.ConditionalWriter)
	if !ok {
		return nil
	}
	docs := scopedfirestore.Collection(repo.client, "projects").Doc(projectDocID(ownerID, projectID)).Collection("document_imports").Documents(ctx)
	defer docs.Stop()
	for {
		snapshot, err := docs.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return err
		}
		var record documentimport.Record
		if err := snapshot.DataTo(&record); err != nil {
			return err
		}
		if record.PipelineRef == nil || record.PipelineRef.SourceRevision <= 0 || record.PipelineRef.SourceRevision > reservation.SourceRevision ||
			record.SourceRef == nil || record.ConversionState != documentimport.ConversionSucceeded || record.SourceState != documentimport.SourceSaved ||
			record.DeletionState != documentimport.DeletionPresent || storage.ValidateRawSyncPath(record.SourceRef.RawPath) != nil {
			continue
		}
		data, generation, err := objects.ReadFileWithGeneration(ctx, "raw/"+record.SourceRef.RawPath)
		if errors.Is(err, storage.ErrObjectNotExist) || errors.Is(err, cloudstorage.ErrObjectNotExist) || status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return err
		}
		if generation != record.SourceRef.ObjectGeneration || fmt.Sprintf("%x", sha256.Sum256(data)) != record.SourceRef.SHA256 {
			continue
		}
		pipelineRef := documentImportPipelineRef(reservation, record.PipelineRef.SourceRevision)
		if _, err := d.Handler.documentImports.Repository.SetPipelineAssociation(ctx, ownerID, projectID, record.DocumentID,
			record.AttemptID, *record.SourceRef, pipelineRef); err != nil {
			return err
		}
	}
}

func latestDocumentSourceCommitIntent(ctx context.Context, repo *firestoreProfileRepository, intentStore pipelineIntentQuotaStore, ownerID, projectID string) (scopedfirestore.QuotaReservation, bool, error) {
	docs := scopedfirestore.Collection(repo.client, "pipeline_quota_reservations").Where("project_id", "==", projectID).Documents(ctx)
	defer docs.Stop()
	var reservationID string
	var sourceRevision int64
	for {
		snapshot, err := docs.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return scopedfirestore.QuotaReservation{}, false, err
		}
		data := snapshot.Data()
		if data["user_id"] != ownerID || data["project_id"] != projectID || data["trigger"] != string(pipelinequota.TriggerSourceCommit) {
			continue
		}
		revision, _ := data["source_revision"].(int64)
		if revision > sourceRevision {
			sourceRevision, reservationID = revision, snapshot.Ref.ID
		}
	}
	if reservationID == "" {
		return scopedfirestore.QuotaReservation{}, false, nil
	}
	return intentStore.GetPipelineIntent(ctx, ownerID, projectID, reservationID)
}

func documentImportPipelineRef(reservation scopedfirestore.QuotaReservation, sourceRevision int64) documentimport.PipelineRef {
	ref := documentimport.PipelineRef{
		Status: "pending", IntentID: reservation.ID, ExecutionID: reservation.ExecutionID, SourceRevision: sourceRevision,
		Reason: reservation.IntentErrorCode,
	}
	switch reservation.IntentStatus {
	case "failed", "cancelled":
		ref.Status = "failed"
	case "compiling", "succeeded":
		ref.Status = "running"
	}
	return ref
}

func (h *Handler) projectDocumentImportPipeline(ctx context.Context, ownerID, projectID string, record documentimport.Record) documentimport.Record {
	ref := record.PipelineRef
	if ref == nil || ref.IntentID == "" || ref.SourceRevision <= 0 {
		return record
	}
	ref.ContentGeneration = ""
	intentStore, ok := h.effectiveQuotaStore().(pipelineIntentQuotaStore)
	if !ok {
		return record
	}
	reservation, exists, err := intentStore.GetPipelineIntent(ctx, ownerID, projectID, ref.IntentID)
	if err != nil {
		ref.Status = "unknown"
		return record
	}
	if !exists || reservation.Trigger != pipelinequota.TriggerSourceCommit || reservation.SourceRevision < ref.SourceRevision {
		ref.Status = "unknown"
		return record
	}
	ref.ExecutionID = reservation.ExecutionID
	ref.Reason = reservation.IntentErrorCode
	if reservation.IntentStatus == "failed" || reservation.IntentStatus == "cancelled" {
		ref.Status = "failed"
		return record
	}
	if reservation.ExecutionID == "" {
		if reservation.IntentStatus == "preparing" || reservation.IntentStatus == "ready" || reservation.IntentStatus == "invoking" || reservation.IntentStatus == "invocation_unknown" {
			ref.Status = "pending"
		} else {
			ref.Status = "unknown"
		}
		return record
	}
	execution, err := h.pipelineExecutionStatusForOwner(ctx, reservation.ExecutionID, ownerID, projectID)
	if err != nil || execution == nil {
		ref.Status = "unknown"
		return record
	}
	owner := &pipelineExecutionOwner{userID: ownerID, projectID: projectID}
	publication, contentGeneration := h.documentPipelinePublication(ctx, owner, reservation.ExecutionID)
	if publication == pipelinePublicationCommitted {
		ref.Status = "published"
		ref.ContentGeneration = contentGeneration
		return record
	}
	ref.ContentGeneration = ""
	switch strings.ToUpper(execution.Status) {
	case "RUNNING":
		ref.Status = "running"
	case "SUCCEEDED", "FAILED", "CANCELLED", "UNKNOWN":
		if publication == pipelinePublicationAbsent {
			ref.Status = "failed"
			if ref.Reason == "" {
				ref.Reason = "pipeline_publication_missing"
			}
		} else {
			ref.Status = "unknown"
		}
	default:
		ref.Status = "unknown"
	}
	return record
}

func (h *Handler) documentPipelinePublication(ctx context.Context, owner *pipelineExecutionOwner, executionID string) (pipelinePublicationOutcome, string) {
	if h.store != nil && owner != nil && executionID != "" {
		project := h.store.Scope(owner.userID, owner.projectID)
		if reader, ok := project.(pipelinePublicationReceiptReader); ok {
			data, err := reader.ReadLocalPublicationReceipt(ctx, executionID)
			if err == nil {
				receipt, decodeErr := localcloud.DecodePublicationReceipt(data)
				if decodeErr == nil && receipt.ExecutionID == executionID {
					return pipelinePublicationCommitted, receipt.GenerationID
				}
				return pipelinePublicationUnknown, ""
			}
			if !errors.Is(err, storage.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist) {
				return pipelinePublicationUnknown, ""
			}
		}
		if reader, ok := project.(pipelineCurrentManifestReader); ok {
			manifest, _, exists, err := reader.CurrentManifest(ctx)
			if err != nil {
				return pipelinePublicationUnknown, ""
			}
			if exists && manifest.LocalExecutionID == executionID {
				return pipelinePublicationCommitted, manifest.GenerationID
			}
		}
	}
	return h.cloudRunPublicationOutcome(ctx, owner, executionID), ""
}

func (d *ProfileDispatcher) executeDocumentImport(ctx context.Context, repo *firestoreProfileRepository, w profileruntime.Work) error {
	if d.Handler.documentImports == nil || d.Handler.documentImports.Repository == nil || d.Handler.documentImports.Starter == nil {
		return errors.New("document import dispatcher is unavailable")
	}
	record, err := d.Handler.documentImports.Repository.Get(ctx, w.UserID, w.ProjectID, w.DocumentID)
	if err != nil {
		return err
	}
	if record.AttemptID != w.ID {
		return errors.New("document import dispatch identity changed")
	}
	if !documentImportWorkRecoverable(record, w.ID) {
		return nil
	}
	if record.LeaseUntil.After(repo.now()) {
		return errProfileRuntimeWaiting
	}
	executionID, err := d.Handler.documentImports.Starter.Start(ctx, w.UserID, w.ProjectID, w.DocumentID, w.ID)
	if err != nil {
		_ = d.Handler.documentImports.Repository.SetDispatchState(ctx, w.UserID, w.ProjectID, w.DocumentID, "unknown", "")
		return err
	}
	return d.Handler.documentImports.Repository.SetDispatchState(ctx, w.UserID, w.ProjectID, w.DocumentID, "started", executionID)
}

func documentImportWorkRecoverable(record documentimport.Record, attemptID string) bool {
	if record.AttemptID != attemptID || record.DeletionState != documentimport.DeletionPresent || record.ConversionState == documentimport.ConversionFailed {
		return false
	}
	if record.ConversionState == documentimport.ConversionSucceeded {
		return record.SourceState != documentimport.SourceSaved && record.SourceState != documentimport.SourceFailed
	}
	return record.ConversionState == documentimport.ConversionAccepted || record.ConversionState == documentimport.ConversionQueued || record.ConversionState == documentimport.ConversionConverting
}
func prepareProfileRuntimeAttempt(ctx context.Context, repo *firestoreProfileRepository, w profileruntime.Work) error {
	return repo.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		_, stateRef, err := repo.authorizeTransaction(ctx, tx, w.UserID, w.ProjectID, ProjectEdit)
		if err != nil {
			return err
		}
		state, err := readProfileState(ctx, tx, stateRef, w.ProjectID)
		if err != nil {
			return err
		}
		if state.Revision != w.Revision {
			return &profileRevisionConflict{Latest: state.Revision}
		}
		if w.Kind == "derive" {
			intent, err := readCurrentProfileDerivationIntent(ctx, tx, stateRef)
			if err != nil {
				return err
			}
			if intent.AttemptID != w.ID || intent.RequirementsDigest != profileRequirementsDigest(state.Requirements) || state.Candidate != nil {
				return errProfileCandidateNotCurrent
			}
			historyRef, history, err := readProfileDerivationAttemptHistory(ctx, tx, stateRef, w.ID)
			if err != nil {
				return err
			}
			if intent.Status == profileDerivationReady {
				return errProfileCandidateNotCurrent
			}
			intent.Status = "scheduled"
			intent.ScheduledFor = stringPtr(repo.now().Format(time.RFC3339Nano))
			intent.ErrorCode = nil
			history.Status = intent.Status
			history.ScheduledFor = intent.ScheduledFor
			history.ErrorCode = nil
			state.DerivationStatus = stringPtr(profileDerivationPending)
			state.DerivationErrorCode = nil
			state.DerivationDiagnostic = nil
			if err = tx.Set(historyRef, history); err != nil {
				return err
			}
			if err = tx.Set(profileDerivationIntentRef(stateRef), intent); err != nil {
				return err
			}
		} else {
			if state.Candidate == nil || state.Candidate.CandidateID != w.CandidateID || state.Job == nil || state.Job.JobID != w.ID {
				return errProfileCandidateNotCurrent
			}
			ref := stateRef.Collection("jobs").Doc(w.ID)
			snapshot, err := tx.Get(ref)
			if err != nil {
				return err
			}
			job, err := profileJobFromData(snapshot.Data())
			if err != nil {
				return err
			}
			if job.Status == profileJobReady || job.Status == profileJobSuperseded {
				return errProfileCandidateNotCurrent
			}
			job.Status = profileJobScheduled
			job.ScheduledFor = stringPtr(repo.now().Format(time.RFC3339Nano))
			job.ErrorCode = nil
			state.Job = &job.ProfileJob
			if err = tx.Set(ref, job); err != nil {
				return err
			}
		}
		return setProfileState(tx, stateRef, state)
	})
}

func (d *ProfileDispatcher) executeCompile(ctx context.Context, repo *firestoreProfileRepository, w profileruntime.Work) error {
	_, stateRef := repo.profileRefs(w.UserID, w.ProjectID)
	snapshot, err := stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(w.ID).Get(ctx)
	if err != nil {
		return err
	}
	var receipt profileruntime.CompileReceipt
	if err = snapshot.DataTo(&receipt); err != nil {
		return err
	}
	if receipt.UserID != w.UserID || receipt.ProjectID != w.ProjectID || receipt.ProfileRevision != w.Revision {
		return errProfileCandidateNotCurrent
	}
	if receipt.ContentGeneration != w.ID || !isLowerProfileDigest(receipt.ManifestSHA256) || !isLowerProfileDigest(receipt.CanonicalConceptsDigest) {
		return errProfileCandidateNotCurrent
	}
	if d.Handler.store == nil {
		return errors.New("Profile generation store unavailable")
	}
	scoped, ok := d.Handler.store.Scope(w.UserID, w.ProjectID).(profileGenerationStore)
	if !ok {
		return errors.New("Profile generation reader unavailable")
	}
	_, generationSnapshot, err := scoped.PinCurrentGeneration(ctx)
	if err != nil {
		return err
	}
	concepts, listed := generationSnapshot.Manifest.File("cache/concepts.jsonl")
	if generationSnapshot.Manifest.GenerationID != w.ID || generationSnapshot.ManifestGeneration != receipt.ManifestGeneration || generationSnapshot.ManifestSHA256 != receipt.ManifestSHA256 || !listed || concepts.SHA256 != receipt.CanonicalConceptsDigest {
		return errProfileCandidateNotCurrent
	}
	success, err := NewProfileCompileSuccess(generationSnapshot.Manifest, receipt.ManifestGeneration, nil, receipt.ConsumedBootstrapGuidance, receipt.PipelineReservationID)
	if err != nil {
		return err
	}
	if err := d.Handler.RequestProfileCompileReconcile(ctx, w.UserID, w.ProjectID, w.ID, w.Revision, receipt.RequirementsDigest); err != nil {
		if errors.Is(err, errProfileNoRequirements) {
			return nil
		}
		return err
	}
	state, err := repo.GetProfile(ctx, w.UserID, w.ProjectID)
	if err != nil {
		return err
	}
	state, err = d.Handler.RunProfileCompileDerivation(ctx, w.UserID, w.ProjectID, success, state.Revision, profileRequirementsDigest(state.Requirements), d.Provider)
	if err != nil {
		return err
	}
	if state.Candidate != nil && state.Candidate.ContentGeneration != w.ID {
		return errProfileRuntimeWaiting
	}
	return nil
}

// An abandoned final execution must leave a visible terminal failure, not a
// permanently running UI state, while preserving all active references.
func markProfileRuntimeExhausted(ctx context.Context, tx *firestore.Transaction, repo *firestoreProfileRepository, w profileruntime.Work) error {
	if !validProfileRuntimeIdentity(w) {
		return nil
	}
	if w.Kind == "compile" {
		return markProfileCompileRuntimeExhausted(ctx, tx, repo, w)
	}
	if w.Kind == "pipeline" {
		if _, _, err := repo.authorizeTransaction(ctx, tx, w.UserID, w.ProjectID, ProjectEdit); errors.Is(err, errProfileProjectNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		return scopedfirestore.SettlePipelineIntentRuntimeExhaustedInTransaction(ctx, tx, repo.client, w.ID, w.ID, w.UserID, w.ProjectID, w.Revision)
	}
	if w.Kind == "document_import" {
		return documentimport.NewFirestoreRepository(repo.client).FailDispatchInTransaction(ctx, tx, w.UserID, w.ProjectID, w.DocumentID, w.ID)
	}
	_, stateRef, err := repo.authorizeTransaction(ctx, tx, w.UserID, w.ProjectID, ProjectEdit)
	if errors.Is(err, errProfileProjectNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	state, err := readProfileState(ctx, tx, stateRef, w.ProjectID)
	if err != nil {
		return err
	}
	if state.Revision != w.Revision {
		return nil
	}
	if w.Kind == "derive" {
		intent, err := readCurrentProfileDerivationIntent(ctx, tx, stateRef)
		if err != nil {
			return err
		}
		if intent.AttemptID != w.ID || intent.Status == profileDerivationReady || state.Candidate != nil {
			return nil
		}
		historyRef, history, err := readProfileDerivationAttemptHistory(ctx, tx, stateRef, w.ID)
		if err != nil {
			return err
		}
		intent.Status = profileDerivationFailed
		intent.ErrorCode = stringPtr("runtime_retry_exhausted")
		intent.ScheduledFor = nil
		history.Status = intent.Status
		history.ErrorCode = intent.ErrorCode
		history.ScheduledFor = nil
		state.DerivationStatus = stringPtr(profileDerivationFailed)
		state.DerivationErrorCode = intent.ErrorCode
		state.ScheduledFor = nil
		if err = tx.Set(historyRef, history); err != nil {
			return err
		}
		if err = tx.Set(profileDerivationIntentRef(stateRef), intent); err != nil {
			return err
		}
	} else {
		if state.Job == nil || state.Job.JobID != w.ID || state.Candidate == nil || state.Candidate.CandidateID != w.CandidateID {
			return nil
		}
		ref := stateRef.Collection("jobs").Doc(w.ID)
		snapshot, err := tx.Get(ref)
		if err != nil {
			return err
		}
		job, err := profileJobFromData(snapshot.Data())
		if err != nil {
			return err
		}
		if job.Status == profileJobReady {
			return nil
		}
		job.Status = profileJobIncomplete
		job.ErrorCode = stringPtr("runtime_retry_exhausted")
		state.Job = &job.ProfileJob
		if err = tx.Set(ref, job); err != nil {
			return err
		}
	}
	return setProfileState(tx, stateRef, state)
}

func markProfileCompileRuntimeExhausted(ctx context.Context, tx *firestore.Transaction, repo *firestoreProfileRepository, w profileruntime.Work) error {
	_, stateRef, err := repo.authorizeTransaction(ctx, tx, w.UserID, w.ProjectID, ProjectEdit)
	if errors.Is(err, errProfileProjectNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	state, err := readProfileState(ctx, tx, stateRef, w.ProjectID)
	if err != nil {
		return err
	}
	if state.Revision != w.Revision || len(state.Requirements) == 0 || hasUnactivatedProfileCandidate(state) {
		return nil
	}
	receiptSnapshot, err := tx.Get(stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(w.ID))
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return err
	}
	var receipt profileruntime.CompileReceipt
	if err := receiptSnapshot.DataTo(&receipt); err != nil || receipt.UserID != w.UserID || receipt.ProjectID != w.ProjectID ||
		receipt.ProfileRevision != w.Revision || receipt.ContentGeneration != w.ID ||
		receipt.RequirementsDigest != profileRequirementsDigest(state.Requirements) ||
		!isLowerProfileDigest(receipt.ManifestSHA256) || !isLowerProfileDigest(receipt.CanonicalConceptsDigest) {
		return nil
	}
	state.DerivationStatus = stringPtr(profileDerivationFailed)
	state.ScheduledFor = nil
	state.DerivationErrorCode = stringPtr("runtime_retry_exhausted")
	state.CompileRetryGeneration = w.ID
	return setProfileState(tx, stateRef, state)
}

func clearSupersededProfileCompileRetry(ctx context.Context, tx *firestore.Transaction, repo *firestoreProfileRepository, w profileruntime.Work) error {
	_, stateRef, err := repo.authorizeTransaction(ctx, tx, w.UserID, w.ProjectID, ProjectEdit)
	if errors.Is(err, errProfileProjectNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	state, err := readProfileState(ctx, tx, stateRef, w.ProjectID)
	if err != nil {
		return err
	}
	if state.Revision != w.Revision || state.CompileRetryGeneration != w.ID || state.DerivationStatus == nil || *state.DerivationStatus != profileDerivationPending {
		return nil
	}
	state.DerivationStatus = stringPtr(profileDerivationReady)
	state.ScheduledFor = nil
	state.DerivationErrorCode = nil
	state.DerivationDiagnostic = nil
	state.CompileRetryGeneration = ""
	return setProfileState(tx, stateRef, state)
}
