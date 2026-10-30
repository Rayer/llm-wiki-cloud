package contentcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/uuid"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	SummaryPromptRevision = "project-summary-v1"
	SummaryRole           = "fast"
	SummaryAttemptLease   = 120 * time.Second
	SummaryMaxClaims      = 2
)

var (
	ErrPublicationChanged = errors.New("published manifest changed during content-context operation")
	ErrAttemptRejected    = errors.New("content-context attempt is no longer current")
)

// Publication identifies a confirmed Project publication read while the
// existing publisher fence is held.
type Publication struct {
	Generation       string
	ConceptsDigest   string
	ManifestIdentity string
}

// PublicationAuthority is implemented by the worker around the existing
// project publication lock. Its fence covers only the state transaction and
// manifest comparisons; the summary model call runs outside this callback.
type PublicationAuthority interface {
	WithProjectFence(context.Context, string, string, func(context.Context) error) error
	CurrentPublication(context.Context, string, string) (Publication, error)
}

type SummaryClaim struct {
	WorkID                 string
	AttemptID              string
	ConceptsSHA256         string
	SourceGeneration       string
	SourceManifestIdentity string
	LeaseUntil             time.Time
	RetryCount             int
}

type SummaryCompletion struct {
	Text             string
	PromptRevision   string
	ProfileID        string
	ProfileRevision  int64
	Role             string
	RequestedModel   string
	ReturnedModel    string
	InputDigest      string
	SelectionVersion string
}

type FirestoreRepository struct {
	client *firestore.Client
	now    func() time.Time
}

func NewFirestoreRepository(client *firestore.Client) *FirestoreRepository {
	return &FirestoreRepository{client: client, now: func() time.Time { return time.Now().UTC() }}
}

func (r *FirestoreRepository) SetClockForTest(now func() time.Time) {
	if now != nil {
		r.now = now
	}
}

func (r *FirestoreRepository) Read(ctx context.Context, ownerID, projectID string) (StateDocument, error) {
	ref, err := r.stateRef(ownerID, projectID)
	if err != nil {
		return StateDocument{}, err
	}
	snapshot, err := ref.Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return emptyStateDocument(), nil
		}
		return StateDocument{}, err
	}
	return decodeStateDocument(snapshot.Data())
}

// ReconcileCurrent records the manifest that the existing publisher fence can
// currently prove. It rechecks on both sides of the Firestore transaction and
// rereads a changed head; target_sequence never chooses publication order.
func (r *FirestoreRepository) ReconcileCurrent(ctx context.Context, ownerID, projectID string, authority PublicationAuthority) (StateDocument, error) {
	if authority == nil {
		return StateDocument{}, errors.New("content-context publication authority unavailable")
	}
	var result StateDocument
	err := authority.WithProjectFence(ctx, ownerID, projectID, func(fencedCtx context.Context) error {
		for tries := 0; tries < 3; tries++ {
			publication, err := currentPublication(fencedCtx, ownerID, projectID, authority)
			if err != nil {
				return err
			}
			updated, err := r.updateState(fencedCtx, ownerID, projectID, func(state StateDocument) (StateDocument, bool, error) {
				next, changed, err := observePublication(state, publication, ownerID, projectID)
				if err != nil || !changed {
					return next, changed, err
				}
				verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
				if err != nil {
					return state, false, err
				}
				if !samePublication(publication, verified) {
					return state, false, ErrPublicationChanged
				}
				return next, true, nil
			})
			if errors.Is(err, ErrPublicationChanged) {
				continue
			}
			if err != nil {
				return err
			}
			verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
			if err != nil {
				return err
			}
			if samePublication(publication, verified) {
				result = updated
				return nil
			}
		}
		return ErrPublicationChanged
	})
	if err != nil {
		return StateDocument{}, err
	}
	return result, nil
}

// Claim reconciles the pinned target and claims its digest work in one short
// fenced section. Provider inference must happen after this method returns.
func (r *FirestoreRepository) Claim(ctx context.Context, ownerID, projectID string, authority PublicationAuthority) (*SummaryClaim, error) {
	if authority == nil {
		return nil, errors.New("content-context publication authority unavailable")
	}
	var claim *SummaryClaim
	err := authority.WithProjectFence(ctx, ownerID, projectID, func(fencedCtx context.Context) error {
		for tries := 0; tries < 3; tries++ {
			publication, err := currentPublication(fencedCtx, ownerID, projectID, authority)
			if err != nil {
				return err
			}
			updatedClaim := (*SummaryClaim)(nil)
			_, err = r.updateState(fencedCtx, ownerID, projectID, func(state StateDocument) (StateDocument, bool, error) {
				updatedClaim = nil
				next, changed, err := observePublication(state, publication, ownerID, projectID)
				if err != nil {
					return state, false, err
				}
				verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
				if err != nil {
					return state, false, err
				}
				if !samePublication(publication, verified) {
					return state, false, ErrPublicationChanged
				}
				claimed, claim, didClaim := claimState(next, r.now())
				if didClaim {
					updatedClaim = &claim
				}
				return claimed, changed || didClaim || claimed.Status == StatusFailed && next.Status != StatusFailed, nil
			})
			if errors.Is(err, ErrPublicationChanged) {
				continue
			}
			if err != nil {
				return err
			}
			verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
			if err != nil {
				return err
			}
			if samePublication(publication, verified) {
				claim = updatedClaim
				return nil
			}
		}
		return ErrPublicationChanged
	})
	if err != nil {
		return nil, err
	}
	return claim, nil
}

// Complete accepts a result only for its still-leased work and only while the
// current published digest matches. Generation may advance on the same digest.
func (r *FirestoreRepository) Complete(ctx context.Context, ownerID, projectID string, authority PublicationAuthority, claim SummaryClaim, completion SummaryCompletion) (bool, error) {
	if authority == nil {
		return false, errors.New("content-context publication authority unavailable")
	}
	if err := validateCompletion(completion); err != nil {
		return false, err
	}
	completed := false
	err := authority.WithProjectFence(ctx, ownerID, projectID, func(fencedCtx context.Context) error {
		publication, err := currentPublication(fencedCtx, ownerID, projectID, authority)
		if err != nil {
			return err
		}
		_, err = r.updateState(fencedCtx, ownerID, projectID, func(state StateDocument) (StateDocument, bool, error) {
			completed = false
			next, changed, err := observePublication(state, publication, ownerID, projectID)
			if err != nil {
				return state, false, err
			}
			if !claimMatches(next, claim, publication, r.now()) {
				if changed {
					return next, true, nil
				}
				return state, false, ErrAttemptRejected
			}
			verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
			if err != nil {
				return state, false, err
			}
			if !samePublication(publication, verified) {
				return state, false, ErrPublicationChanged
			}
			now := r.now()
			next.LastGood = &LastGood{
				Text: completion.Text, SourceGeneration: claim.SourceGeneration,
				ConceptsSHA256: claim.ConceptsSHA256, GeneratedAt: now,
				PromptRevision: completion.PromptRevision, ProfileID: completion.ProfileID,
				ProfileRevision: completion.ProfileRevision, Role: completion.Role,
				RequestedModel: completion.RequestedModel, ReturnedModel: completion.ReturnedModel,
				InputDigest: completion.InputDigest, SelectionVersion: completion.SelectionVersion,
			}
			next.Status = StatusReady
			next.ErrorClass = ""
			next.AttemptFinishedAt = &now
			next.SummaryWork.Status = StatusReady
			if err := next.Validate(); err != nil {
				return state, false, err
			}
			completed = true
			return next, true, nil
		})
		if err != nil {
			return err
		}
		verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
		if err != nil {
			return err
		}
		if !samePublication(publication, verified) {
			return ErrPublicationChanged
		}
		return nil
	})
	return completed && err == nil, err
}

// Fail marks only the current valid attempt failed. The retained last-good
// summary is never changed by provider, parsing, or summary storage failures.
func (r *FirestoreRepository) Fail(ctx context.Context, ownerID, projectID string, authority PublicationAuthority, claim SummaryClaim, errorClass string) (bool, error) {
	if authority == nil {
		return false, errors.New("content-context publication authority unavailable")
	}
	if !validErrorClass(errorClass) {
		return false, errors.New("invalid content-context error class")
	}
	failed := false
	err := authority.WithProjectFence(ctx, ownerID, projectID, func(fencedCtx context.Context) error {
		publication, err := currentPublication(fencedCtx, ownerID, projectID, authority)
		if err != nil {
			return err
		}
		_, err = r.updateState(fencedCtx, ownerID, projectID, func(state StateDocument) (StateDocument, bool, error) {
			failed = false
			next, changed, err := observePublication(state, publication, ownerID, projectID)
			if err != nil {
				return state, false, err
			}
			if !claimMatches(next, claim, publication, r.now()) {
				if changed {
					return next, true, nil
				}
				return state, false, ErrAttemptRejected
			}
			verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
			if err != nil {
				return state, false, err
			}
			if !samePublication(publication, verified) {
				return state, false, ErrPublicationChanged
			}
			now := r.now()
			next.Status = StatusFailed
			next.ErrorClass = errorClass
			next.AttemptFinishedAt = &now
			next.SummaryWork.Status = StatusFailed
			if err := next.Validate(); err != nil {
				return state, false, err
			}
			failed = true
			return next, true, nil
		})
		if err != nil {
			return err
		}
		verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
		if err != nil {
			return err
		}
		if !samePublication(publication, verified) {
			return ErrPublicationChanged
		}
		return nil
	})
	return failed && err == nil, err
}

// MarkEmpty finalizes a valid claim without inference when the pinned
// publication contains no concepts. The status is missing, not ready/failed.
func (r *FirestoreRepository) MarkEmpty(ctx context.Context, ownerID, projectID string, authority PublicationAuthority, claim SummaryClaim) (bool, error) {
	return r.finishWithoutSummary(ctx, ownerID, projectID, authority, claim, StatusMissing, "")
}

func (r *FirestoreRepository) finishWithoutSummary(ctx context.Context, ownerID, projectID string, authority PublicationAuthority, claim SummaryClaim, finalStatus Status, errorClass string) (bool, error) {
	if authority == nil {
		return false, errors.New("content-context publication authority unavailable")
	}
	finished := false
	err := authority.WithProjectFence(ctx, ownerID, projectID, func(fencedCtx context.Context) error {
		publication, err := currentPublication(fencedCtx, ownerID, projectID, authority)
		if err != nil {
			return err
		}
		_, err = r.updateState(fencedCtx, ownerID, projectID, func(state StateDocument) (StateDocument, bool, error) {
			finished = false
			next, changed, err := observePublication(state, publication, ownerID, projectID)
			if err != nil {
				return state, false, err
			}
			if !claimMatches(next, claim, publication, r.now()) {
				if changed {
					return next, true, nil
				}
				return state, false, ErrAttemptRejected
			}
			verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
			if err != nil {
				return state, false, err
			}
			if !samePublication(publication, verified) {
				return state, false, ErrPublicationChanged
			}
			now := r.now()
			next.Status = finalStatus
			next.ErrorClass = errorClass
			next.AttemptFinishedAt = &now
			next.SummaryWork.Status = finalStatus
			if err := next.Validate(); err != nil {
				return state, false, err
			}
			finished = true
			return next, true, nil
		})
		if err != nil {
			return err
		}
		verified, err := currentPublication(fencedCtx, ownerID, projectID, authority)
		if err != nil {
			return err
		}
		if !samePublication(publication, verified) {
			return ErrPublicationChanged
		}
		return nil
	})
	return finished && err == nil, err
}

func (r *FirestoreRepository) updateState(ctx context.Context, ownerID, projectID string, update func(StateDocument) (StateDocument, bool, error)) (StateDocument, error) {
	ref, err := r.stateRef(ownerID, projectID)
	if err != nil {
		return StateDocument{}, err
	}
	var result StateDocument
	err = r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, getErr := tx.Get(ref)
		state := emptyStateDocument()
		if getErr == nil {
			state, getErr = decodeStateDocument(snapshot.Data())
		} else if status.Code(getErr) != codes.NotFound {
			return getErr
		}
		if getErr != nil && status.Code(getErr) != codes.NotFound {
			return getErr
		}
		next, changed, updateErr := update(state)
		if updateErr != nil {
			return updateErr
		}
		if !changed {
			result = state
			return nil
		}
		if err := next.Validate(); err != nil {
			return err
		}
		if err := tx.Set(ref, next); err != nil {
			return err
		}
		result = next
		return nil
	})
	return result, err
}

func (r *FirestoreRepository) stateRef(ownerID, projectID string) (*firestore.DocumentRef, error) {
	if r == nil || r.client == nil || !auth.ValidPathSegment(ownerID) || !auth.ValidPathSegment(projectID) {
		return nil, errors.New("content-context Firestore reference unavailable")
	}
	return scopedfirestore.Collection(r.client, "projects").Doc(ownerID + "_" + projectID).Collection("content_context").Doc("state"), nil
}

func emptyStateDocument() StateDocument {
	return StateDocument{SchemaVersion: StateSchemaVersion, Status: StatusMissing}
}

func decodeStateDocument(data map[string]interface{}) (StateDocument, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return StateDocument{}, err
	}
	var state StateDocument
	if err := json.Unmarshal(encoded, &state); err != nil {
		return StateDocument{}, err
	}
	if err := state.Validate(); err != nil {
		return StateDocument{}, err
	}
	return state, nil
}

func currentPublication(ctx context.Context, ownerID, projectID string, authority PublicationAuthority) (Publication, error) {
	publication, err := authority.CurrentPublication(ctx, ownerID, projectID)
	if err != nil {
		return Publication{}, err
	}
	digest, ok := NormalizeSHA256(publication.ConceptsDigest)
	if !ok || strings.TrimSpace(publication.Generation) == "" || strings.TrimSpace(publication.ManifestIdentity) == "" {
		return Publication{}, errors.New("invalid current content-context publication")
	}
	publication.ConceptsDigest = digest
	return publication, nil
}

func observePublication(state StateDocument, publication Publication, ownerID, projectID string) (StateDocument, bool, error) {
	if state.SchemaVersion == 0 {
		state = emptyStateDocument()
	}
	if err := state.Validate(); err != nil {
		return state, false, err
	}
	if state.LatestPublication != nil && state.LatestPublication.ManifestIdentity == publication.ManifestIdentity {
		if state.LatestPublication.Generation != publication.Generation {
			return state, false, errors.New("content-context manifest identity collision")
		}
		currentDigest, ok := NormalizeSHA256(state.LatestPublication.ConceptsSHA256)
		if !ok || currentDigest != publication.ConceptsDigest {
			return state, false, errors.New("content-context manifest identity collision")
		}
		return state, false, nil
	}
	if state.TargetSequence == int64(^uint64(0)>>1) {
		return state, false, errors.New("content-context target sequence exhausted")
	}
	previousDigest := ""
	if state.LatestPublication != nil {
		previousDigest, _ = NormalizeSHA256(state.LatestPublication.ConceptsSHA256)
	}
	state.TargetSequence++
	state.TargetGeneration = publication.Generation
	state.TargetConceptsSHA = publication.ConceptsDigest
	state.LatestPublication = &PublishedHead{
		Generation: publication.Generation, ConceptsSHA256: publication.ConceptsDigest,
		ManifestIdentity: publication.ManifestIdentity,
	}
	if state.SummaryWork != nil {
		workDigest, ok := NormalizeSHA256(state.SummaryWork.ConceptsSHA256)
		if previousDigest == publication.ConceptsDigest && ok && workDigest == publication.ConceptsDigest {
			state.Status = state.SummaryWork.Status
			return state, true, nil
		}
	}
	workID := SummaryWorkID(ownerID, projectID, publication.ConceptsDigest, SummaryPromptRevision, SummarySelectionVersion)
	if state.LastGood != nil {
		lastDigest, ok := NormalizeSHA256(state.LastGood.ConceptsSHA256)
		if ok && lastDigest == publication.ConceptsDigest {
			state.Status = StatusReady
			state.ErrorClass = ""
			state.AttemptID = ""
			state.AttemptStartedAt = nil
			state.AttemptFinishedAt = nil
			state.SummaryWork = &SummaryWorkRecord{
				WorkID: workID, ConceptsSHA256: publication.ConceptsDigest,
				SourceGeneration: state.LastGood.SourceGeneration, Status: StatusReady,
			}
			return state, true, nil
		}
	}
	state.Status = StatusPending
	state.ErrorClass = ""
	state.AttemptID = ""
	state.AttemptStartedAt = nil
	state.AttemptFinishedAt = nil
	state.SummaryWork = &SummaryWorkRecord{
		WorkID: workID, ConceptsSHA256: publication.ConceptsDigest,
		SourceGeneration: publication.Generation, SourceManifestIdentity: publication.ManifestIdentity,
		Status: StatusPending,
	}
	return state, true, nil
}

func SummaryWorkID(ownerID, projectID, conceptsSHA256, promptRevision, selectionVersion string) string {
	digest, ok := NormalizeSHA256(conceptsSHA256)
	if !ok {
		return ""
	}
	input := strings.Join([]string{ownerID, projectID, digest, promptRevision, selectionVersion}, "\x00")
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func claimState(state StateDocument, now time.Time) (StateDocument, SummaryClaim, bool) {
	if state.Status != StatusPending || state.SummaryWork == nil || state.SummaryWork.Status != StatusPending {
		return state, SummaryClaim{}, false
	}
	work := state.SummaryWork
	if work.AttemptID != "" && work.LeaseUntil != nil && now.Before(*work.LeaseUntil) {
		return state, SummaryClaim{}, false
	}
	if work.AttemptID != "" {
		if work.RetryCount >= SummaryMaxClaims-1 {
			state.Status = StatusFailed
			state.ErrorClass = "lease_expired"
			work.Status = StatusFailed
			finished := now
			state.AttemptFinishedAt = &finished
			return state, SummaryClaim{}, false
		}
		work.RetryCount++
	}
	attemptID := uuid.NewString()
	leaseUntil := now.Add(SummaryAttemptLease)
	work.AttemptID = attemptID
	work.LeaseUntil = &leaseUntil
	state.Status = StatusPending
	state.ErrorClass = ""
	state.AttemptID = attemptID
	started := now
	state.AttemptStartedAt = &started
	state.AttemptFinishedAt = nil
	claim := SummaryClaim{
		WorkID: work.WorkID, AttemptID: attemptID, ConceptsSHA256: work.ConceptsSHA256,
		SourceGeneration: work.SourceGeneration, SourceManifestIdentity: work.SourceManifestIdentity,
		LeaseUntil: leaseUntil, RetryCount: work.RetryCount,
	}
	return state, claim, true
}

func claimMatches(state StateDocument, claim SummaryClaim, publication Publication, now time.Time) bool {
	if state.Status != StatusPending || state.SummaryWork == nil || state.SummaryWork.Status != StatusPending ||
		state.SummaryWork.WorkID != claim.WorkID || state.SummaryWork.AttemptID != claim.AttemptID ||
		state.AttemptID != claim.AttemptID || state.SummaryWork.RetryCount != claim.RetryCount ||
		!now.Before(claim.LeaseUntil) || state.SummaryWork.LeaseUntil == nil || !state.SummaryWork.LeaseUntil.Equal(claim.LeaseUntil) {
		return false
	}
	workDigest, ok := NormalizeSHA256(state.SummaryWork.ConceptsSHA256)
	publicationDigest, publicationOK := NormalizeSHA256(publication.ConceptsDigest)
	claimDigest, claimOK := NormalizeSHA256(claim.ConceptsSHA256)
	return ok && publicationOK && claimOK && workDigest == publicationDigest && workDigest == claimDigest &&
		state.SummaryWork.SourceGeneration == claim.SourceGeneration &&
		state.SummaryWork.SourceManifestIdentity == claim.SourceManifestIdentity
}

func validateCompletion(completion SummaryCompletion) error {
	if strings.TrimSpace(completion.Text) == "" || len([]rune(completion.Text)) > 1000 ||
		completion.PromptRevision != SummaryPromptRevision || completion.ProfileID == "" || completion.ProfileRevision < 1 ||
		completion.Role != SummaryRole || completion.RequestedModel == "" || completion.ReturnedModel == "" ||
		!validSHA256(completion.InputDigest) || completion.SelectionVersion != SummarySelectionVersion {
		return errors.New("invalid content-context summary completion")
	}
	return nil
}

func samePublication(left, right Publication) bool {
	leftDigest, leftOK := NormalizeSHA256(left.ConceptsDigest)
	rightDigest, rightOK := NormalizeSHA256(right.ConceptsDigest)
	return leftOK && rightOK && left.Generation == right.Generation && leftDigest == rightDigest && left.ManifestIdentity == right.ManifestIdentity
}
