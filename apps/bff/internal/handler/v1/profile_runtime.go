package v1

import (
	"context"
	"errors"
	"fmt"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/iterator"
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
func enqueueProfileDerivation(tx *firestore.Transaction, client *firestore.Client, userID, projectID string, intent profileDerivationIntent) error {
	if intent.ScheduledFor == nil {
		return errors.New("missing derivation schedule")
	}
	due, err := time.Parse(time.RFC3339Nano, *intent.ScheduledFor)
	if err != nil {
		return err
	}
	return profileruntime.Enqueue(tx, client, profileruntime.Work{UserID: userID, ProjectID: projectID, Kind: "derive", Revision: intent.Revision, ID: intent.AttemptID, Due: due})
}
func enqueueProfileTagging(tx *firestore.Transaction, client *firestore.Client, userID, projectID string, revision int64, job storedProfileJob) error {
	if job.ScheduledFor == nil {
		return errors.New("missing tagging schedule")
	}
	due, err := time.Parse(time.RFC3339Nano, *job.ScheduledFor)
	if err != nil {
		return err
	}
	return profileruntime.Enqueue(tx, client, profileruntime.Work{UserID: userID, ProjectID: projectID, Kind: "tag", Revision: revision, ID: job.JobID, CandidateID: job.CandidateID, Due: due})
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
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if d.Audience == "" || d.ServiceAccount == "" {
		http.Error(w, "Profile runtime is not configured", 503)
		return
	}
	bearer := r.Header.Get("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(bearer, "Bearer ")) == "" {
		http.Error(w, "unauthorized", 401)
		return
	}
	validate := d.ValidateIdentity
	if validate == nil {
		validate = idtoken.Validate
	}
	payload, err := validate(r.Context(), strings.TrimPrefix(bearer, "Bearer "), d.Audience)
	if err != nil || payload == nil || payload.Audience != d.Audience || payload.Claims["email"] != d.ServiceAccount || payload.Claims["email_verified"] != true {
		http.Error(w, "unauthorized", 401)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), profileruntime.ExecutionTimeout)
	defer cancel()
	count, err := d.Dispatch(ctx, 8)
	if err != nil {
		http.Error(w, "Profile dispatch unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"processed":%d}`, count)
}
func (d *ProfileDispatcher) Gin(c *gin.Context) { d.ServeHTTP(c.Writer, c.Request) }

// Dispatch reads only due work. Pending work remains durable across crashes;
// a lease expiry allows bounded recovery and fences every Profile transaction.
func (d *ProfileDispatcher) Dispatch(ctx context.Context, limit int) (int, error) {
	repo, ok := d.Handler.profileRepository.(*firestoreProfileRepository)
	if !ok || limit < 1 || limit > 100 {
		return 0, errors.New("Profile dispatcher repository unavailable")
	}
	iter := repo.client.Collection(profileruntime.WorkCollection).Where("pending", "==", true).Where("due", "<=", repo.now()).OrderBy("due", firestore.Asc).Limit(limit).Documents(ctx)
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
		work, claimed, err := d.claim(ctx, repo, snapshot.Ref)
		if err != nil {
			return count, err
		}
		if !claimed {
			continue
		}
		lease := profileRuntimeLease{snapshot.Ref, work.Token, work.UserID, work.ProjectID}
		runctx := context.WithValue(ctx, profileRuntimeLeaseKey{}, lease)
		err = d.execute(runctx, repo, work)
		if finishErr := d.finish(ctx, repo, snapshot.Ref, work, err); finishErr != nil {
			return count, finishErr
		}
		count++
	}
}
func (d *ProfileDispatcher) claim(ctx context.Context, repo *firestoreProfileRepository, ref *firestore.DocumentRef) (profileruntime.Work, bool, error) {
	var work profileruntime.Work
	claimed := false
	token := repo.client.Collection(profileruntime.WorkCollection).NewDoc().ID
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
	return auth.ValidPathSegment(w.UserID) && auth.ValidPathSegment(w.ProjectID) && auth.ValidPathSegment(w.ID) && w.Revision > 0 && (w.Kind == "derive" || w.Kind == "tag" || w.Kind == "compile")
}
func (d *ProfileDispatcher) finish(ctx context.Context, repo *firestoreProfileRepository, ref *firestore.DocumentRef, claimed profileruntime.Work, cause error) error {
	return repo.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
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
		} else if cause == nil {
			w.Pending = false
			w.Status = "complete"
		} else if errors.As(cause, &conflict) || errors.Is(cause, errProfileCandidateNotCurrent) || errors.Is(cause, errProfileProjectNotFound) {
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
		return tx.Set(ref, w)
	})
}
func (d *ProfileDispatcher) execute(ctx context.Context, repo *firestoreProfileRepository, w profileruntime.Work) error {
	if _, err := d.Handler.AuthorizeProject(ctx, w.UserID, w.ProjectID, ProjectEdit); err != nil {
		return err
	}
	if w.Kind == "compile" {
		state, err := repo.GetProfile(ctx, w.UserID, w.ProjectID)
		if err != nil {
			return err
		}
		if state.Revision < w.Revision {
			return &profileRevisionConflict{Latest: state.Revision}
		}
		if state.DerivationStatus != nil && (*state.DerivationStatus == profileDerivationPending || *state.DerivationStatus == profileDerivationFailed) {
			return errProfileRuntimeWaiting
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
	success, err := NewProfileCompileSuccess(generationSnapshot.Manifest, receipt.ManifestGeneration, nil, receipt.ConsumedBootstrapGuidance)
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
	if !validProfileRuntimeIdentity(w) || w.Kind == "compile" {
		return nil
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
