package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"github.com/rayer/llm-wiki-bff/internal/profileruntime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const profileDebounce = 3 * time.Minute
const maxProfileRequestBytes = 1 << 20

const (
	profileDerivationPending     = "pending"
	profileDerivationReady       = "ready"
	profileDerivationFailed      = "failed"
	profileDerivationSuperseded  = "superseded"
	profileJobScheduled          = "scheduled"
	profileJobRunning            = "running"
	profileJobRetryWait          = "retry_wait"
	profileJobIncomplete         = "incomplete"
	profileJobReady              = "ready"
	profileJobSuperseded         = "superseded"
	profileBootstrapPreviewReady = "preview_ready"
	profileBootstrapConfirmed    = "confirmed"
)

var (
	errProfileProjectNotFound     = errors.New("project not found")
	errInvalidProfileRequirements = errors.New("invalid profile requirements")
	errProfileCandidateNotCurrent = errors.New("profile candidate is not current")
	errProfileTransitionInvalid   = errors.New("invalid profile transition")
	errProfileNoRequirements      = errors.New("profile has no requirements")
)

// ProjectAction is the permission required at the existing Project boundary.
type ProjectAction string

const (
	ProjectRead ProjectAction = "read"
	ProjectEdit ProjectAction = "edit"
)

// AuthorizedProject is derived only from a validated real Project metadata document.
type AuthorizedProject struct {
	UserID    string
	ProjectID string
	Document  string
}

// ProfileRequirement is a user-authored requirement; text is preserved verbatim.
type ProfileRequirement struct {
	ID   string `json:"id" firestore:"id" binding:"required"`
	Text string `json:"text" firestore:"text" binding:"required"`
}

func (r *ProfileRequirement) UnmarshalJSON(data []byte) error {
	var fields struct {
		ID   *string `json:"id"`
		Text *string `json:"text"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	if fields.ID == nil || fields.Text == nil {
		return errors.New("requirement id and text are required")
	}
	r.ID, r.Text = *fields.ID, *fields.Text
	return nil
}

// ProfileDerivedRef identifies an immutable derived artifact without embedding its payload.
type ProfileDerivedRef struct {
	Revision      string `json:"revision" firestore:"revision" binding:"required"`
	InputDigest   string `json:"input_digest" firestore:"input_digest" binding:"required"`
	ModelVersion  string `json:"model_version" firestore:"model_version" binding:"required"`
	PromptVersion string `json:"prompt_version" firestore:"prompt_version" binding:"required"`
	SchemaVersion string `json:"schema_version" firestore:"schema_version" binding:"required"`
}

// ProfileCandidate is immutable once stored under the project's Profile state.
type ProfileCandidate struct {
	CandidateID        string            `json:"candidate_id" firestore:"candidate_id" binding:"required"`
	Source             string            `json:"source" firestore:"source" binding:"required" enums:"manual,compile_auto"`
	BaseRevision       int64             `json:"base_revision" firestore:"base_revision" binding:"required"`
	RequirementsDigest string            `json:"requirements_digest" firestore:"requirements_digest" binding:"required"`
	ContentGeneration  string            `json:"content_generation" firestore:"content_generation" binding:"required"`
	Dictionary         ProfileDerivedRef `json:"dictionary" firestore:"dictionary" binding:"required"`
	Guidance           ProfileDerivedRef `json:"guidance" firestore:"guidance" binding:"required"`
	Preview            ProfilePreview    `json:"preview" firestore:"preview" binding:"required"`
}

type ProfilePreview struct {
	DictionaryDiff string                         `json:"dictionary_diff" firestore:"dictionary_diff" binding:"required"`
	GuidanceDiff   string                         `json:"guidance_diff" firestore:"guidance_diff" binding:"required"`
	Requirements   []ProfileRequirementAccounting `json:"requirements" firestore:"requirements" binding:"required"`
}

// ProfileBootstrapGuidance is a generation-free preview used only before the
// first confirmed content generation exists. Its object is immutable and its
// Profile revision/input digest binding is checked on confirmation and compile.
type ProfileBootstrapGuidance struct {
	Revision        string                  `json:"revision" firestore:"revision" binding:"required"`
	InputDigest     string                  `json:"input_digest" firestore:"input_digest" binding:"required"`
	ProfileRevision int64                   `json:"profile_revision" firestore:"profile_revision" binding:"required"`
	Status          string                  `json:"status" firestore:"status" binding:"required" enums:"preview_ready,confirmed"`
	ModelVersion    string                  `json:"model_version" firestore:"model_version" binding:"required"`
	PromptVersion   string                  `json:"prompt_version" firestore:"prompt_version" binding:"required"`
	SchemaVersion   string                  `json:"schema_version" firestore:"schema_version" binding:"required"`
	ConfirmedAt     *string                 `json:"confirmed_at" firestore:"confirmed_at" binding:"required"`
	Preview         ProfileBootstrapPreview `json:"preview" firestore:"preview" binding:"required"`
}

type ProfileBootstrapPreview struct {
	GuidanceDiff string                         `json:"guidance_diff" firestore:"guidance_diff" binding:"required"`
	Requirements []ProfileRequirementAccounting `json:"requirements" firestore:"requirements" binding:"required"`
}

type ProfileRequirementAccounting struct {
	ID          string `json:"id" firestore:"id" binding:"required"`
	Disposition string `json:"disposition" firestore:"disposition" binding:"required" enums:"compile_guidance,dictionary_or_query,both,limitation"`
	Explanation string `json:"explanation" firestore:"explanation" binding:"required"`
}

// ProfileJob is the public projection of a scoped tagging job.
type ProfileJob struct {
	JobID             string  `json:"job_id" firestore:"job_id" binding:"required"`
	CandidateID       string  `json:"candidate_id" firestore:"candidate_id" binding:"required"`
	ContentGeneration string  `json:"content_generation" firestore:"content_generation" binding:"required"`
	Status            string  `json:"status" firestore:"status" binding:"required" enums:"scheduled,running,retry_wait,incomplete,ready,superseded"`
	MissingCount      int64   `json:"missing_count" firestore:"missing_count" binding:"required"`
	ErrorCode         *string `json:"error_code" firestore:"error_code" binding:"required"`
}

// ProfileActive is a single atomically switched reader pointer.
type ProfileActive struct {
	CandidateID        string `json:"candidate_id" firestore:"candidate_id" binding:"required"`
	ContentGeneration  string `json:"content_generation" firestore:"content_generation" binding:"required"`
	DictionaryRevision string `json:"dictionary_revision" firestore:"dictionary_revision" binding:"required"`
	TagSetRevision     string `json:"tag_set_revision" firestore:"tag_set_revision" binding:"required"`
	QueryRuleRevision  string `json:"query_rule_revision" firestore:"query_rule_revision" binding:"required"`
	GuidanceRevision   string `json:"guidance_revision" firestore:"guidance_revision" binding:"required"`
}

// ProfileState is the complete API and Firestore projection for one project.
type ProfileState struct {
	ProjectID               string                    `json:"project_id" firestore:"project_id" binding:"required"`
	Revision                int64                     `json:"revision" firestore:"revision" binding:"required"`
	Requirements            []ProfileRequirement      `json:"requirements" firestore:"requirements" binding:"required"`
	DerivationStatus        *string                   `json:"derivation_status" firestore:"derivation_status" binding:"required" enums:"pending,ready,failed,superseded"`
	ScheduledFor            *string                   `json:"scheduled_for" firestore:"scheduled_for" binding:"required"`
	DerivationErrorCode     *string                   `json:"derivation_error_code" firestore:"derivation_error_code" binding:"required"`
	Candidate               *ProfileCandidate         `json:"candidate" firestore:"candidate" binding:"required"`
	BootstrapGuidance       *ProfileBootstrapGuidance `json:"bootstrap_guidance" firestore:"bootstrap_guidance" binding:"required"`
	ConfirmedCandidateID    *string                   `json:"confirmed_candidate_id" firestore:"confirmed_candidate_id" binding:"required"`
	Active                  *ProfileActive            `json:"active" firestore:"active" binding:"required"`
	Job                     *ProfileJob               `json:"job" firestore:"job" binding:"required"`
	CompileRetryGeneration  string                    `json:"-" firestore:"compile_retry_generation,omitempty"`
	derivationClaimAcquired bool
}

type profileRevisionConflict struct{ Latest int64 }

func (e *profileRevisionConflict) Error() string { return "profile revision conflict" }

type profileRepository interface {
	GetProfile(context.Context, string, string) (ProfileState, error)
	SaveProfile(context.Context, string, string, int64, []ProfileRequirement) (ProfileState, error)
	ConfirmProfileCandidate(context.Context, string, string, string, int64) (ProfileState, error)
	ConfirmProfileBootstrapGuidance(context.Context, string, string, string, string, int64) (ProfileState, error)
	RetryProfileTagging(context.Context, string, string, string, int64) (ProfileState, error)
	RetryProfileDerivation(context.Context, string, string, int64) (ProfileState, error)
	GetProfileJob(context.Context, string, string, string) (ProfileJob, error)
}

type firestoreProfileRepository struct {
	client *firestore.Client
	now    func() time.Time
}

func newFirestoreProfileRepository(client *firestore.Client) *firestoreProfileRepository {
	return &firestoreProfileRepository{client: client, now: time.Now}
}

func newProfileState(projectID string) ProfileState {
	return ProfileState{ProjectID: projectID, Requirements: []ProfileRequirement{}}
}

func hasUnactivatedProfileCandidate(state ProfileState) bool {
	return state.Candidate != nil && (state.Active == nil || state.Candidate.CandidateID != state.Active.CandidateID)
}

func saveProfileState(current ProfileState, expected int64, requirements []ProfileRequirement, now time.Time) (ProfileState, error) {
	if current.Revision != expected {
		return ProfileState{}, &profileRevisionConflict{Latest: current.Revision}
	}
	if err := validateProfileRequirements(requirements); err != nil {
		return ProfileState{}, err
	}
	current.Revision++
	current.Requirements = append([]ProfileRequirement{}, requirements...)
	if len(requirements) == 0 && current.Active == nil {
		current.DerivationStatus = nil
		current.ScheduledFor = nil
	} else {
		current.DerivationStatus = stringPtr(profileDerivationPending)
		current.ScheduledFor = stringPtr(now.Add(profileDebounce).Format(time.RFC3339Nano))
	}
	current.DerivationErrorCode = nil
	current.CompileRetryGeneration = ""
	current.Candidate = nil
	current.BootstrapGuidance = nil
	current.ConfirmedCandidateID = nil
	current.Job = nil
	return current, nil
}

func validateProfileRequirements(requirements []ProfileRequirement) error {
	seen := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		if requirement.ID == "" {
			return errInvalidProfileRequirements
		}
		if _, exists := seen[requirement.ID]; exists {
			return errInvalidProfileRequirements
		}
		seen[requirement.ID] = struct{}{}
	}
	return nil
}

func profileRequirementsDigest(requirements []ProfileRequirement) string {
	data, _ := json.Marshal(requirements)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func failProfileDerivation(current ProfileState, revision int64, digest, errorCode string) (ProfileState, error) {
	if current.Revision != revision {
		return ProfileState{}, &profileRevisionConflict{Latest: current.Revision}
	}
	if current.Candidate != nil || current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationPending || profileRequirementsDigest(current.Requirements) != digest {
		return ProfileState{}, errProfileCandidateNotCurrent
	}
	if strings.TrimSpace(errorCode) == "" {
		return ProfileState{}, errProfileTransitionInvalid
	}
	current.DerivationStatus = stringPtr(profileDerivationFailed)
	current.ScheduledFor = nil
	current.DerivationErrorCode = stringPtr(errorCode)
	return current, nil
}

func retryFailedProfileDerivation(current ProfileState, expected int64, now time.Time) (ProfileState, error) {
	if current.Revision != expected {
		return ProfileState{}, &profileRevisionConflict{Latest: current.Revision}
	}
	if current.Candidate != nil {
		return ProfileState{}, errProfileCandidateNotCurrent
	}
	if current.DerivationStatus != nil && *current.DerivationStatus == profileDerivationPending {
		// Repeated retry requests for the same revision preserve the original schedule.
		return current, nil
	}
	if current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationFailed {
		return ProfileState{}, errProfileTransitionInvalid
	}
	current.DerivationStatus = stringPtr(profileDerivationPending)
	current.ScheduledFor = stringPtr(now.Format(time.RFC3339Nano))
	current.DerivationErrorCode = nil
	return current, nil
}

func authorizeProjectDocument(docID string, data map[string]interface{}, userID, projectID string, action ProjectAction) error {
	if !auth.ValidPathSegment(userID) || !auth.ValidPathSegment(projectID) || (action != ProjectRead && action != ProjectEdit) {
		return errProfileProjectNotFound
	}
	kind, record, ok := classifyAdminProjectDocForOwner(docID, data, userID)
	if !ok || kind != adminRealProjectDoc || record.projectID != projectID {
		return errProfileProjectNotFound
	}
	// The current Project policy grants read and edit to the owner principal.
	return nil
}

// AuthorizeProject is the shared Project metadata authorization seam for read/edit actions.
func (h *Handler) AuthorizeProject(ctx context.Context, principal, projectID string, action ProjectAction) (AuthorizedProject, error) {
	if strings.TrimSpace(principal) == "" {
		return AuthorizedProject{}, errors.New("authenticated principal is required")
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		return AuthorizedProject{}, errors.New("Firestore is not configured")
	}
	if !auth.ValidPathSegment(principal) || !auth.ValidPathSegment(projectID) {
		return AuthorizedProject{}, errProfileProjectNotFound
	}
	projectRef := h.firestore.Raw().Collection("projects").Doc(projectDocID(principal, projectID))
	snapshot, err := projectRef.Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return AuthorizedProject{}, errProfileProjectNotFound
		}
		return AuthorizedProject{}, err
	}
	if err := authorizeProjectDocument(projectRef.ID, snapshot.Data(), principal, projectID, action); err != nil {
		return AuthorizedProject{}, err
	}
	return AuthorizedProject{UserID: principal, ProjectID: projectID, Document: projectRef.ID}, nil
}

func (r *firestoreProfileRepository) profileRefs(userID, projectID string) (*firestore.DocumentRef, *firestore.DocumentRef) {
	projectRef := r.client.Collection("projects").Doc(projectDocID(userID, projectID))
	return projectRef, projectRef.Collection("profile").Doc("state")
}

func (r *firestoreProfileRepository) authorizeTransaction(ctx context.Context, tx *firestore.Transaction, userID, projectID string, action ProjectAction) (*firestore.DocumentRef, *firestore.DocumentRef, error) {
	projectRef, stateRef := r.profileRefs(userID, projectID)
	if err := validateProfileRuntimeLease(ctx, tx, userID, projectID, r.now()); err != nil {
		return nil, nil, err
	}
	snapshot, err := tx.Get(projectRef)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil, errProfileProjectNotFound
		}
		return nil, nil, err
	}
	if err := authorizeProjectDocument(projectRef.ID, snapshot.Data(), userID, projectID, action); err != nil {
		return nil, nil, err
	}
	return projectRef, stateRef, nil
}

func readProfileState(ctx context.Context, tx *firestore.Transaction, stateRef *firestore.DocumentRef, projectID string) (ProfileState, error) {
	snapshot, err := tx.Get(stateRef)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return newProfileState(projectID), nil
		}
		return ProfileState{}, err
	}
	state, err := profileStateFromData(snapshot.Data())
	if err != nil {
		return ProfileState{}, err
	}
	if state.ProjectID != projectID || state.Requirements == nil || state.Revision < 0 {
		return ProfileState{}, errors.New("corrupt profile state")
	}
	return state, nil
}

func profileStateFromData(data map[string]interface{}) (ProfileState, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return ProfileState{}, err
	}
	var state ProfileState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return ProfileState{}, err
	}
	state.CompileRetryGeneration, _ = data["compile_retry_generation"].(string)
	return state, nil
}

func setProfileState(tx *firestore.Transaction, stateRef *firestore.DocumentRef, state ProfileState) error {
	return tx.Set(stateRef, state)
}

func (r *firestoreProfileRepository) GetProfile(ctx context.Context, userID, projectID string) (ProfileState, error) {
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectRead)
		if err != nil {
			return err
		}
		result, err = readProfileState(ctx, tx, stateRef, projectID)
		return err
	})
	return result, err
}

func (r *firestoreProfileRepository) SaveProfile(ctx context.Context, userID, projectID string, expected int64, requirements []ProfileRequirement) (ProfileState, error) {
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		intentRef := profileDerivationIntentRef(stateRef)
		oldIntent, err := readCurrentProfileDerivationIntent(ctx, tx, stateRef)
		if err != nil {
			return err
		}
		var oldAttemptRef *firestore.DocumentRef
		var oldAttempt profileDerivationIntent
		if oldIntent.AttemptID != "" && (oldIntent.Status == "scheduled" || oldIntent.Status == "running" || oldIntent.Status == profileDerivationFailed) {
			oldAttemptRef = intentRef.Collection("attempts").Doc(oldIntent.AttemptID)
			oldAttemptSnapshot, getErr := tx.Get(oldAttemptRef)
			if getErr != nil && status.Code(getErr) != codes.NotFound {
				return getErr
			}
			if getErr == nil {
				oldAttempt, getErr = profileDerivationIntentFromData(oldAttemptSnapshot.Data())
				if getErr != nil {
					return getErr
				}
			}
		}
		var oldJobRef *firestore.DocumentRef
		var oldJob storedProfileJob
		if current.Job != nil {
			oldJobRef = stateRef.Collection("jobs").Doc(current.Job.JobID)
			snapshot, getErr := tx.Get(oldJobRef)
			if getErr != nil && status.Code(getErr) != codes.NotFound {
				return getErr
			}
			if getErr == nil {
				oldJob, getErr = profileJobFromData(snapshot.Data())
				if getErr != nil {
					return getErr
				}
			}
		}
		result, err = saveProfileState(current, expected, requirements, r.now())
		if err != nil {
			return err
		}
		if oldJobRef != nil && oldJob.JobID != "" && oldJob.Status != profileJobSuperseded {
			oldJob.Status = profileJobSuperseded
			if oldJob.ErrorCode == nil {
				oldJob.ErrorCode = stringPtr("superseded")
			}
			if err := tx.Set(oldJobRef, profileJobData(oldJob)); err != nil {
				return err
			}
		}
		if oldAttemptRef != nil && oldAttempt.AttemptID != "" {
			oldAttempt.Status = profileJobSuperseded
			oldAttempt.ScheduledFor = nil
			oldAttempt.ErrorCode = stringPtr("superseded")
			if err := tx.Set(oldAttemptRef, oldAttempt); err != nil {
				return err
			}
		}
		if len(result.Requirements) == 0 && current.Active == nil {
			if err := tx.Set(intentRef, profileDerivationIntent{Revision: result.Revision, Status: profileJobSuperseded}); err != nil {
				return err
			}
			return setProfileState(tx, stateRef, result)
		}
		attemptRef := intentRef.Collection("attempts").NewDoc()
		intent := profileDerivationIntent{
			AttemptID: attemptRef.ID, Revision: result.Revision, Attempt: 1,
			RequirementsDigest: profileRequirementsDigest(result.Requirements), Status: "scheduled", ScheduledFor: result.ScheduledFor,
		}
		if err := tx.Create(attemptRef, intent); err != nil {
			return err
		}
		if err := enqueueProfileDerivation(tx, r.client, userID, projectID, intent); err != nil {
			return err
		}
		if err := tx.Set(intentRef, intent); err != nil {
			return err
		}
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

func (r *firestoreProfileRepository) ConfirmProfileCandidate(ctx context.Context, userID, projectID, candidateID string, expected int64) (ProfileState, error) {
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return &profileRevisionConflict{Latest: current.Revision}
		}
		if current.Candidate == nil || current.Candidate.CandidateID != candidateID || current.Candidate.BaseRevision != current.Revision || current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationReady {
			return errProfileCandidateNotCurrent
		}
		candidateRef := stateRef.Collection("candidates").Doc(candidateID)
		candidateSnapshot, err := tx.Get(candidateRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return errProfileCandidateNotCurrent
			}
			return err
		}
		candidate, err := profileCandidateFromData(candidateSnapshot.Data())
		if err != nil || !reflect.DeepEqual(candidate, *current.Candidate) || candidate.Source != "manual" || candidate.RequirementsDigest != profileRequirementsDigest(current.Requirements) {
			return errProfileCandidateNotCurrent
		}
		var storedJob storedProfileJob
		var jobRef *firestore.DocumentRef
		if current.Job != nil {
			jobRef = stateRef.Collection("jobs").Doc(current.Job.JobID)
			jobSnapshot, getErr := tx.Get(jobRef)
			if getErr != nil {
				return getErr
			}
			storedJob, err = profileJobFromData(jobSnapshot.Data())
			if err != nil {
				return err
			}
		}

		firstConfirmation := current.ConfirmedCandidateID == nil || *current.ConfirmedCandidateID != candidateID
		current.ConfirmedCandidateID = stringPtr(candidateID)
		if firstConfirmation && current.Job != nil && (storedJob.Status == profileJobIncomplete || storedJob.Status == profileJobRetryWait) {
			storedJob.Status = profileJobScheduled
			storedJob.ErrorCode = nil
			storedJob.ScheduledFor = stringPtr(r.now().Format(time.RFC3339Nano))
			current.Job = &storedJob.ProfileJob
			if err := enqueueProfileTagging(tx, r.client, userID, projectID, current.Revision, storedJob); err != nil {
				return err
			}
			if err := tx.Set(jobRef, profileJobData(storedJob)); err != nil {
				return err
			}
		}
		if current.Job != nil && storedJob.Status == profileJobReady && !activateProfileState(&current, candidate, storedJob) {
			storedJob.Status = profileJobIncomplete
			storedJob.ErrorCode = stringPtr("active_conflict")
			current.Job = &storedJob.ProfileJob
			if err := tx.Set(jobRef, profileJobData(storedJob)); err != nil {
				return err
			}
		}
		result = current
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

func (r *firestoreProfileRepository) RetryProfileTagging(ctx context.Context, userID, projectID, candidateID string, expected int64) (ProfileState, error) {
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return &profileRevisionConflict{Latest: current.Revision}
		}
		if current.Candidate == nil || current.Candidate.CandidateID != candidateID || current.Job == nil || current.Job.CandidateID != candidateID {
			return errProfileCandidateNotCurrent
		}
		jobRef := stateRef.Collection("jobs").Doc(current.Job.JobID)
		jobSnapshot, err := tx.Get(jobRef)
		if err != nil {
			return err
		}
		job, err := profileJobFromData(jobSnapshot.Data())
		if err != nil {
			return err
		}
		if job.Status == profileJobScheduled || job.Status == profileJobRunning {
			result = current
			return nil
		}
		if job.Status != profileJobIncomplete && job.Status != profileJobRetryWait {
			return errProfileTransitionInvalid
		}
		job.Status = profileJobScheduled
		job.ErrorCode = nil
		job.ScheduledFor = stringPtr(r.now().Format(time.RFC3339Nano))
		if err := enqueueProfileTagging(tx, r.client, userID, projectID, current.Revision, job); err != nil {
			return err
		}
		if err := tx.Set(jobRef, profileJobData(job)); err != nil {
			return err
		}
		current.Job = &job.ProfileJob
		result = current
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

// ClaimProfileJob starts one due tagging job; workers must claim before reporting progress.
func (r *firestoreProfileRepository) ClaimProfileJob(ctx context.Context, userID, projectID string, revision int64, candidateID, jobID string) (ProfileState, error) {
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return &profileRevisionConflict{Latest: current.Revision}
		}
		if current.Candidate == nil || current.Candidate.CandidateID != candidateID || current.Job == nil || current.Job.JobID != jobID {
			return errProfileCandidateNotCurrent
		}
		jobRef := stateRef.Collection("jobs").Doc(jobID)
		jobSnapshot, err := tx.Get(jobRef)
		if err != nil {
			return err
		}
		job, err := profileJobFromData(jobSnapshot.Data())
		if err != nil || job.CandidateID != candidateID || job.ContentGeneration != current.Candidate.ContentGeneration {
			return errProfileCandidateNotCurrent
		}
		if job.Status == profileJobRunning {
			result = current
			return nil
		}
		if job.Status != profileJobScheduled || job.ScheduledFor == nil {
			return errProfileTransitionInvalid
		}
		dueAt, err := time.Parse(time.RFC3339Nano, *job.ScheduledFor)
		if err != nil {
			return errors.New("corrupt profile job schedule")
		}
		if dueAt.After(r.now()) {
			return errProfileTransitionInvalid
		}
		job.Status = profileJobRunning
		if err := tx.Set(jobRef, profileJobData(job)); err != nil {
			return err
		}
		current.Job = &job.ProfileJob
		result = current
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

func (r *firestoreProfileRepository) RetryProfileDerivation(ctx context.Context, userID, projectID string, expected int64) (ProfileState, error) {
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return &profileRevisionConflict{Latest: current.Revision}
		}
		if current.CompileRetryGeneration != "" {
			result, err = r.retryExhaustedProfileCompile(ctx, tx, stateRef, userID, projectID, expected, current)
			if err != nil {
				return err
			}
			return setProfileState(tx, stateRef, result)
		}
		intent, err := readCurrentProfileDerivationIntent(ctx, tx, stateRef)
		if err != nil {
			return err
		}
		if intent.Revision != expected || intent.RequirementsDigest != profileRequirementsDigest(current.Requirements) {
			return errProfileCandidateNotCurrent
		}
		if current.DerivationStatus != nil && *current.DerivationStatus == profileDerivationPending && intent.Retry && (intent.Status == "scheduled" || intent.Status == "running") {
			result = current
			return nil
		}
		if current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationFailed || intent.Status != profileDerivationFailed || current.Candidate != nil {
			return errProfileTransitionInvalid
		}
		result, err = retryFailedProfileDerivation(current, expected, r.now())
		if err != nil {
			return err
		}
		intentRef := profileDerivationIntentRef(stateRef)
		attemptRef := intentRef.Collection("attempts").NewDoc()
		intent.AttemptID = attemptRef.ID
		intent.Attempt++
		intent.Status = "scheduled"
		intent.ScheduledFor = result.ScheduledFor
		intent.ErrorCode = nil
		intent.Retry = true
		if err := tx.Create(attemptRef, intent); err != nil {
			return err
		}
		if err := enqueueProfileDerivation(tx, r.client, userID, projectID, intent); err != nil {
			return err
		}
		if err := tx.Set(intentRef, intent); err != nil {
			return err
		}
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

func (r *firestoreProfileRepository) retryExhaustedProfileCompile(ctx context.Context, tx *firestore.Transaction, stateRef *firestore.DocumentRef, userID, projectID string, expected int64, current ProfileState) (ProfileState, error) {
	if current.CompileRetryGeneration == "" || hasUnactivatedProfileCandidate(current) {
		return ProfileState{}, errProfileTransitionInvalid
	}
	work := profileruntime.Work{UserID: userID, ProjectID: projectID, Kind: "compile", Revision: expected, ID: current.CompileRetryGeneration}
	if !validProfileRuntimeIdentity(work) {
		return ProfileState{}, errProfileTransitionInvalid
	}
	workRef := r.client.Collection(profileruntime.WorkCollection).Doc(profileruntime.WorkID(work))
	workSnapshot, err := tx.Get(workRef)
	if err != nil {
		return ProfileState{}, errProfileTransitionInvalid
	}
	if err := workSnapshot.DataTo(&work); err != nil || work.UserID != userID || work.ProjectID != projectID || work.Kind != "compile" ||
		work.Revision != expected || work.ID != current.CompileRetryGeneration {
		return ProfileState{}, errProfileTransitionInvalid
	}
	receiptSnapshot, err := tx.Get(stateRef.Collection(profileruntime.CompileReceiptsCollection).Doc(work.ID))
	if err != nil {
		return ProfileState{}, errProfileTransitionInvalid
	}
	var receipt profileruntime.CompileReceipt
	if err := receiptSnapshot.DataTo(&receipt); err != nil || receipt.UserID != userID || receipt.ProjectID != projectID ||
		receipt.ProfileRevision != expected || receipt.ContentGeneration != work.ID ||
		receipt.RequirementsDigest != profileRequirementsDigest(current.Requirements) ||
		!isLowerProfileDigest(receipt.ManifestSHA256) || !isLowerProfileDigest(receipt.CanonicalConceptsDigest) {
		return ProfileState{}, errProfileCandidateNotCurrent
	}
	if current.DerivationStatus != nil && *current.DerivationStatus == profileDerivationPending && work.Pending &&
		(work.Status == "pending" || work.Status == "running" || work.Status == "retry_wait") {
		return current, nil
	}
	if current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationFailed ||
		current.DerivationErrorCode == nil || *current.DerivationErrorCode != "runtime_retry_exhausted" {
		return ProfileState{}, errProfileTransitionInvalid
	}
	if work.Pending || work.Status != "exhausted" {
		return ProfileState{}, errProfileTransitionInvalid
	}
	work.Attempts = 0
	work.Token = ""
	work.LeaseUntil = time.Time{}
	work.Due = r.now()
	if err := profileruntime.Enqueue(tx, r.client, work); err != nil {
		return ProfileState{}, err
	}
	current.DerivationStatus = stringPtr(profileDerivationPending)
	current.ScheduledFor = stringPtr(r.now().Format(time.RFC3339Nano))
	current.DerivationErrorCode = nil
	return current, nil
}

func (r *firestoreProfileRepository) GetProfileJob(ctx context.Context, userID, projectID, jobID string) (ProfileJob, error) {
	var result ProfileJob
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileJob{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectRead)
		if err != nil {
			return err
		}
		jobRef := stateRef.Collection("jobs").Doc(jobID)
		snapshot, err := tx.Get(jobRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return errProfileProjectNotFound
			}
			return err
		}
		job, err := profileJobFromData(snapshot.Data())
		if err != nil || job.JobID != jobID {
			return errors.New("corrupt profile job")
		}
		result = job.ProfileJob
		return nil
	})
	return result, err
}

// ClaimProfileDerivation marks a due durable intent running for one attempt ID.
func (r *firestoreProfileRepository) ClaimProfileDerivation(ctx context.Context, userID, projectID string, revision int64, attemptID string) (ProfileState, error) {
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return &profileRevisionConflict{Latest: current.Revision}
		}
		intent, err := readCurrentProfileDerivationIntent(ctx, tx, stateRef)
		if err != nil {
			return err
		}
		if intent.AttemptID != attemptID || intent.Revision != revision || intent.RequirementsDigest != profileRequirementsDigest(current.Requirements) || current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationPending || current.Candidate != nil {
			return errProfileCandidateNotCurrent
		}
		historyRef, history, err := readProfileDerivationAttemptHistory(ctx, tx, stateRef, attemptID)
		if err != nil {
			return err
		}
		if history.Revision != intent.Revision || history.RequirementsDigest != intent.RequirementsDigest {
			return errors.New("profile derivation attempt history does not match current intent")
		}
		if intent.Status == "running" {
			if history.Status != "running" {
				return errors.New("profile derivation attempt history status mismatch")
			}
			result = current
			result.derivationClaimAcquired = false
			return nil
		}
		if intent.Status != "scheduled" || intent.ScheduledFor == nil || history.Status != "scheduled" {
			return errProfileTransitionInvalid
		}
		dueAt, err := time.Parse(time.RFC3339Nano, *intent.ScheduledFor)
		if err != nil {
			return errors.New("corrupt profile derivation schedule")
		}
		if dueAt.After(r.now()) {
			return errProfileTransitionInvalid
		}
		intent.Status = "running"
		history.Status = "running"
		if err := tx.Set(historyRef, history); err != nil {
			return err
		}
		if err := tx.Set(profileDerivationIntentRef(stateRef), intent); err != nil {
			return err
		}
		result = current
		result.derivationClaimAcquired = true
		return nil
	})
	return result, err
}

// ProfileDerivationSucceeded persists a worker result only for the current input revision, digest, and attempt ID.
// Callers must authenticate the worker claim before supplying its scoped Project identity.
func (r *firestoreProfileRepository) ProfileDerivationSucceeded(ctx context.Context, userID, projectID string, revision int64, attemptID, requirementsDigest, generation string, dictionary, guidance ProfileDerivedRef, preview ProfilePreview) (ProfileState, error) {
	if r == nil || r.client == nil {
		return ProfileState{}, errors.New("Firestore is not configured")
	}
	_, stateRef := r.profileRefs(userID, projectID)
	candidateRef := stateRef.Collection("candidates").NewDoc()
	jobRef := stateRef.Collection("jobs").NewDoc()
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		intent, err := readCurrentProfileDerivationIntent(ctx, tx, stateRef)
		if err != nil {
			return err
		}
		if current.Revision != revision {
			return &profileRevisionConflict{Latest: current.Revision}
		}
		if intent.AttemptID != attemptID || intent.Revision != revision || intent.Status != "running" || intent.RequirementsDigest != requirementsDigest || current.DerivationStatus == nil || *current.DerivationStatus != profileDerivationPending || current.Candidate != nil || profileRequirementsDigest(current.Requirements) != requirementsDigest {
			return errProfileCandidateNotCurrent
		}
		historyRef, history, err := readProfileDerivationAttemptHistory(ctx, tx, stateRef, attemptID)
		if err != nil {
			return err
		}
		if history.Revision != revision || history.RequirementsDigest != requirementsDigest || history.Status != "running" {
			return errors.New("profile derivation attempt history status mismatch")
		}
		if strings.TrimSpace(generation) == "" || dictionary.Revision == "" || dictionary.InputDigest != requirementsDigest || guidance.Revision == "" || guidance.InputDigest != requirementsDigest {
			return errProfileTransitionInvalid
		}
		candidate := ProfileCandidate{
			CandidateID: candidateRef.ID, Source: "manual", BaseRevision: revision, RequirementsDigest: requirementsDigest,
			ContentGeneration: generation, Dictionary: dictionary, Guidance: guidance, Preview: preview,
		}
		job := storedProfileJob{ProfileJob: ProfileJob{JobID: jobRef.ID, CandidateID: candidateRef.ID, ContentGeneration: generation, Status: profileJobScheduled, MissingCount: 0}}
		job.ExpectedActive = cloneProfileActive(current.Active)
		job.ScheduledFor = stringPtr(r.now().Format(time.RFC3339Nano))
		if err := tx.Create(candidateRef, candidate); err != nil {
			return err
		}
		if err := tx.Create(jobRef, profileJobData(job)); err != nil {
			return err
		}
		if err := enqueueProfileTagging(tx, r.client, userID, projectID, revision, job); err != nil {
			return err
		}
		current.DerivationStatus = stringPtr(profileDerivationReady)
		current.ScheduledFor = nil
		current.DerivationErrorCode = nil
		current.Candidate = &candidate
		current.Job = &job.ProfileJob
		result = current
		intent.Status = profileDerivationReady
		intent.ScheduledFor = nil
		intent.ErrorCode = nil
		history.Status = profileDerivationReady
		history.ScheduledFor = nil
		history.ErrorCode = nil
		if err := tx.Set(historyRef, history); err != nil {
			return err
		}
		if err := tx.Set(profileDerivationIntentRef(stateRef), intent); err != nil {
			return err
		}
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

// ProfileDerivationFailed records a retryable worker failure without replacing requirements or active state.
func (r *firestoreProfileRepository) ProfileDerivationFailed(ctx context.Context, userID, projectID string, revision int64, attemptID, requirementsDigest, errorCode string) (ProfileState, error) {
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		intent, err := readCurrentProfileDerivationIntent(ctx, tx, stateRef)
		if err != nil {
			return err
		}
		if intent.AttemptID != attemptID || intent.Revision != revision || intent.Status != "running" || intent.RequirementsDigest != requirementsDigest {
			return errProfileCandidateNotCurrent
		}
		historyRef, history, err := readProfileDerivationAttemptHistory(ctx, tx, stateRef, attemptID)
		if err != nil {
			return err
		}
		if history.Revision != revision || history.RequirementsDigest != requirementsDigest || history.Status != "running" {
			return errors.New("profile derivation attempt history status mismatch")
		}
		result, err = failProfileDerivation(current, revision, requirementsDigest, errorCode)
		if err != nil {
			return err
		}
		intent.Status = profileDerivationFailed
		intent.ScheduledFor = nil
		intent.ErrorCode = stringPtr(errorCode)
		history.Status = profileDerivationFailed
		history.ScheduledFor = nil
		history.ErrorCode = stringPtr(errorCode)
		if err := tx.Set(historyRef, history); err != nil {
			return err
		}
		if err := tx.Set(profileDerivationIntentRef(stateRef), intent); err != nil {
			return err
		}
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

// ProfileJobTransition persists a versioned worker transition; ready requires complete coverage.
func (r *firestoreProfileRepository) ProfileJobTransition(ctx context.Context, userID, projectID string, revision int64, candidateID, jobID, nextStatus string, missingCount int64, errorCode, tagSetRevision, queryRuleRevision string, coverageComplete bool) (ProfileState, error) {
	if missingCount < 0 || !validProfileJobStatus(nextStatus) {
		return ProfileState{}, errProfileTransitionInvalid
	}
	var result ProfileState
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		jobRef := stateRef.Collection("jobs").Doc(jobID)
		jobSnapshot, err := tx.Get(jobRef)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return errProfileCandidateNotCurrent
			}
			return err
		}
		job, err := profileJobFromData(jobSnapshot.Data())
		if err != nil || job.JobID != jobID || job.CandidateID != candidateID {
			return errProfileCandidateNotCurrent
		}
		if current.Revision != revision || current.Candidate == nil || current.Candidate.CandidateID != candidateID || current.Job == nil || current.Job.JobID != jobID {
			if job.Status != profileJobSuperseded {
				job.Status = profileJobSuperseded
				job.ErrorCode = stringPtr("superseded")
				if err := tx.Set(jobRef, profileJobData(job)); err != nil {
					return err
				}
			}
			result = current
			return nil
		}
		candidateRef := stateRef.Collection("candidates").Doc(candidateID)
		candidateSnapshot, err := tx.Get(candidateRef)
		if err != nil {
			return err
		}
		candidate, err := profileCandidateFromData(candidateSnapshot.Data())
		if err != nil || !reflect.DeepEqual(candidate, *current.Candidate) || candidate.ContentGeneration != job.ContentGeneration || candidate.RequirementsDigest != profileRequirementsDigest(current.Requirements) {
			return errProfileCandidateNotCurrent
		}
		if !validProfileJobTransition(job.Status, nextStatus) {
			return errProfileTransitionInvalid
		}
		if nextStatus == profileJobReady && (!coverageComplete || missingCount != 0 || tagSetRevision == "" || queryRuleRevision == "") {
			return errProfileTransitionInvalid
		}
		job.Status, job.MissingCount, job.ErrorCode = nextStatus, missingCount, nullableString(errorCode)
		job.TagSetRevision, job.QueryRuleRevision = tagSetRevision, queryRuleRevision
		job.CoverageComplete = coverageComplete
		if nextStatus == profileJobReady && current.Candidate != nil && activateProfileState(&current, *current.Candidate, job) {
			// The active pointer and ready state commit together.
		} else if nextStatus == profileJobReady && current.ConfirmedCandidateID != nil || nextStatus == profileJobReady && current.Candidate != nil && current.Candidate.Source == "compile_auto" {
			job.Status = profileJobIncomplete
			job.ErrorCode = stringPtr("active_conflict")
		}
		if err := tx.Set(jobRef, profileJobData(job)); err != nil {
			return err
		}
		current.Job = &job.ProfileJob
		result = current
		return setProfileState(tx, stateRef, result)
	})
	return result, err
}

// ProfileCompileReconcileRequested deduplicates a confirmed generation and advances it to current requirements.
func (r *firestoreProfileRepository) ProfileCompileReconcileRequested(ctx context.Context, userID, projectID, generation string, _ int64, _ string) error {
	if strings.TrimSpace(generation) == "" {
		return errProfileTransitionInvalid
	}
	_, stateRef := r.profileRefs(userID, projectID)
	reconcileRef := profileCompileReconcileRef(stateRef, generation)
	return r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		if len(current.Requirements) == 0 && current.Active == nil {
			return errProfileNoRequirements
		}
		digest := profileRequirementsDigest(current.Requirements)
		previous, err := readProfileCompileReconcile(ctx, tx, reconcileRef)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if previous.ContentGeneration == generation && previous.LatestRevision == current.Revision && previous.RequirementsDigest == digest && (previous.Status == "candidate_ready" || previous.Status == "active") {
			return nil
		}
		reconcile := profileCompileReconcile{
			ContentGeneration: generation, LatestRevision: current.Revision, RequirementsDigest: digest,
			Status: "scheduled", RequestedAt: r.now().Format(time.RFC3339Nano),
		}
		if status.Code(err) == codes.NotFound {
			return tx.Create(reconcileRef, reconcile)
		}
		return tx.Set(reconcileRef, reconcile)
	})
}

// ProfileCompileCandidateReady creates a Tags-only candidate after successful compile work.
// It leaves guidance unchanged and does not call compilers, taggers, or providers.
func (r *firestoreProfileRepository) ProfileCompileCandidateReady(ctx context.Context, userID, projectID, generation string, revision int64, requirementsDigest string, dictionary ProfileDerivedRef, preview ProfilePreview) (ProfileState, error) {
	if r == nil || r.client == nil || strings.TrimSpace(generation) == "" {
		return ProfileState{}, errProfileTransitionInvalid
	}
	_, stateRef := r.profileRefs(userID, projectID)
	reconcileRef := profileCompileReconcileRef(stateRef, generation)
	candidateRef := stateRef.Collection("candidates").NewDoc()
	jobRef := stateRef.Collection("jobs").NewDoc()
	var result ProfileState
	var stale, waitingManual, empty bool
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		stale, waitingManual, empty = false, false, false
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		reconcileRef = profileCompileReconcileRef(stateRef, generation)
		reconcile, err := readProfileCompileReconcile(ctx, tx, reconcileRef)
		if err != nil {
			return err
		}
		if len(current.Requirements) == 0 && current.Active == nil {
			reconcile.Status = profileJobSuperseded
			if err := tx.Set(reconcileRef, reconcile); err != nil {
				return err
			}
			result, empty = current, true
			return nil
		}
		currentDigest := profileRequirementsDigest(current.Requirements)
		if current.Revision != revision || currentDigest != requirementsDigest || reconcile.LatestRevision != revision || reconcile.RequirementsDigest != requirementsDigest {
			reconcile.LatestRevision = current.Revision
			reconcile.RequirementsDigest = currentDigest
			reconcile.Status = "scheduled"
			reconcile.CandidateID = nil
			reconcile.RequestedAt = r.now().Format(time.RFC3339Nano)
			if err := tx.Set(reconcileRef, reconcile); err != nil {
				return err
			}
			result, stale = current, true
			return nil
		}
		if dictionary.Revision == "" || dictionary.InputDigest != currentDigest {
			return errProfileTransitionInvalid
		}
		if current.Candidate != nil && current.Candidate.Source == "compile_auto" && current.Candidate.BaseRevision == revision && current.Candidate.ContentGeneration == generation && current.Candidate.RequirementsDigest == currentDigest {
			result = current
			return nil
		}

		compileRetryInProgress := current.CompileRetryGeneration == generation && current.DerivationStatus != nil && *current.DerivationStatus == profileDerivationPending
		manualDerivationInProgress := !compileRetryInProgress && current.DerivationStatus != nil && (*current.DerivationStatus == profileDerivationPending || *current.DerivationStatus == profileDerivationFailed)
		manualCandidateAwaitingActivation := current.Candidate != nil && current.Candidate.Source == "manual" &&
			(current.ConfirmedCandidateID == nil || *current.ConfirmedCandidateID != current.Candidate.CandidateID || current.Active == nil || current.Active.CandidateID != current.Candidate.CandidateID)
		if current.Active == nil || manualDerivationInProgress || manualCandidateAwaitingActivation {
			reconcile.Status = "waiting_manual"
			if err := tx.Set(reconcileRef, reconcile); err != nil {
				return err
			}
			result, waitingManual = current, true
			return nil
		}
		var guidance ProfileDerivedRef
		guidanceCandidateRef := stateRef.Collection("candidates").Doc(current.Active.CandidateID)
		guidanceSnapshot, err := tx.Get(guidanceCandidateRef)
		if err != nil {
			return errors.New("active Profile candidate is unavailable")
		}
		guidanceCandidate, err := profileCandidateFromData(guidanceSnapshot.Data())
		if err != nil {
			return err
		}
		if current.Active != nil && guidanceCandidate.Guidance.Revision != current.Active.GuidanceRevision {
			return errors.New("active Profile guidance revision is corrupt")
		}
		guidance = guidanceCandidate.Guidance
		preview.GuidanceDiff = ""
		candidate := ProfileCandidate{
			CandidateID: candidateRef.ID, Source: "compile_auto", BaseRevision: revision,
			RequirementsDigest: currentDigest, ContentGeneration: generation,
			Dictionary: dictionary, Guidance: guidance, Preview: preview,
		}
		job := storedProfileJob{ProfileJob: ProfileJob{
			JobID: jobRef.ID, CandidateID: candidateRef.ID, ContentGeneration: generation, Status: profileJobScheduled,
		}}
		job.ExpectedActive = cloneProfileActive(current.Active)
		due := r.now()
		if current.ScheduledFor != nil {
			if stateDue, parseErr := time.Parse(time.RFC3339Nano, *current.ScheduledFor); parseErr == nil && stateDue.After(due) {
				due = stateDue
			}
		}
		job.ScheduledFor = stringPtr(due.Format(time.RFC3339Nano))

		var oldJobRef *firestore.DocumentRef
		var oldJob storedProfileJob
		if current.Job != nil {
			oldJobRef = stateRef.Collection("jobs").Doc(current.Job.JobID)
			oldJobSnapshot, getErr := tx.Get(oldJobRef)
			if getErr != nil {
				return getErr
			}
			oldJob, err = profileJobFromData(oldJobSnapshot.Data())
			if err != nil {
				return err
			}
		}
		if err := tx.Create(candidateRef, candidate); err != nil {
			return err
		}
		if err := tx.Create(jobRef, profileJobData(job)); err != nil {
			return err
		}
		if err := enqueueProfileTagging(tx, r.client, userID, projectID, revision, job); err != nil {
			return err
		}
		if oldJobRef != nil && oldJob.Status != profileJobSuperseded {
			oldJob.Status = profileJobSuperseded
			oldJob.ErrorCode = stringPtr("superseded")
			if err := tx.Set(oldJobRef, profileJobData(oldJob)); err != nil {
				return err
			}
		}
		current.DerivationStatus = stringPtr(profileDerivationReady)
		current.ScheduledFor = nil
		current.DerivationErrorCode = nil
		current.CompileRetryGeneration = ""
		current.Candidate = &candidate
		current.ConfirmedCandidateID = nil
		current.Job = &job.ProfileJob
		result = current
		reconcile.Status = "candidate_ready"
		reconcile.CandidateID = stringPtr(candidate.CandidateID)
		if err := tx.Set(reconcileRef, reconcile); err != nil {
			return err
		}
		return setProfileState(tx, stateRef, result)
	})
	if err != nil {
		return result, err
	}
	if stale {
		return result, &profileRevisionConflict{Latest: result.Revision}
	}
	if waitingManual || empty {
		return result, nil
	}
	return result, nil
}

// ProfileBootstrapCompileCandidateReady promotes confirmed generation-free
// writing guidance into the first generation-bound compile candidate. It is
// valid only before any active Profile tuple exists.
func (r *firestoreProfileRepository) ProfileBootstrapCompileCandidateReady(ctx context.Context, userID, projectID, generation string, revision int64, requirementsDigest string, consumedBootstrapRef *profileartifacts.BootstrapGuidanceRef, dictionary, guidance ProfileDerivedRef, preview ProfilePreview) (ProfileState, error) {
	if r == nil || r.client == nil || strings.TrimSpace(generation) == "" {
		return ProfileState{}, errProfileTransitionInvalid
	}
	_, stateRef := r.profileRefs(userID, projectID)
	reconcileRef := profileCompileReconcileRef(stateRef, generation)
	candidateRef := stateRef.Collection("candidates").NewDoc()
	jobRef := stateRef.Collection("jobs").NewDoc()
	var result ProfileState
	var stale, empty bool
	err := r.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		result = ProfileState{}
		stale, empty = false, false
		_, stateRef, err := r.authorizeTransaction(ctx, tx, userID, projectID, ProjectEdit)
		if err != nil {
			return err
		}
		current, err := readProfileState(ctx, tx, stateRef, projectID)
		if err != nil {
			return err
		}
		reconcileRef = profileCompileReconcileRef(stateRef, generation)
		reconcile, err := readProfileCompileReconcile(ctx, tx, reconcileRef)
		if err != nil {
			return err
		}
		if len(current.Requirements) == 0 {
			reconcile.Status = profileJobSuperseded
			if err := tx.Set(reconcileRef, reconcile); err != nil {
				return err
			}
			result, empty = current, true
			return nil
		}
		currentDigest := profileRequirementsDigest(current.Requirements)
		if current.Revision != revision || currentDigest != requirementsDigest || reconcile.LatestRevision != revision || reconcile.RequirementsDigest != requirementsDigest {
			reconcile.LatestRevision = current.Revision
			reconcile.RequirementsDigest = currentDigest
			reconcile.Status = "scheduled"
			reconcile.CandidateID = nil
			reconcile.RequestedAt = r.now().Format(time.RFC3339Nano)
			if err := tx.Set(reconcileRef, reconcile); err != nil {
				return err
			}
			result, stale = current, true
			return nil
		}
		if current.Active != nil || !profileBootstrapRefMatches(current.BootstrapGuidance, consumedBootstrapRef, revision, currentDigest) ||
			!isLowerProfileDigest(dictionary.Revision) || dictionary.InputDigest != currentDigest || dictionary.SchemaVersion != "profile.dictionary.v1" ||
			strings.TrimSpace(dictionary.ModelVersion) == "" || strings.TrimSpace(dictionary.PromptVersion) == "" ||
			!isLowerProfileDigest(guidance.Revision) || guidance.InputDigest != currentDigest || guidance.SchemaVersion != "profile.guidance.v1" ||
			strings.TrimSpace(guidance.ModelVersion) == "" || strings.TrimSpace(guidance.PromptVersion) == "" {
			return errProfileBootstrapNotCurrent
		}
		if current.Candidate != nil && current.Candidate.Source == "compile_auto" && current.Candidate.BaseRevision == revision &&
			current.Candidate.ContentGeneration == generation && current.Candidate.RequirementsDigest == currentDigest {
			result = current
			return nil
		}
		preview.GuidanceDiff = ""
		candidate := ProfileCandidate{
			CandidateID: candidateRef.ID, Source: "compile_auto", BaseRevision: revision,
			RequirementsDigest: currentDigest, ContentGeneration: generation,
			Dictionary: dictionary, Guidance: guidance, Preview: preview,
		}
		job := storedProfileJob{ProfileJob: ProfileJob{
			JobID: jobRef.ID, CandidateID: candidateRef.ID, ContentGeneration: generation, Status: profileJobScheduled,
		}}
		job.ExpectedActive = cloneProfileActive(current.Active)
		due := r.now()
		if current.ScheduledFor != nil {
			if stateDue, parseErr := time.Parse(time.RFC3339Nano, *current.ScheduledFor); parseErr == nil && stateDue.After(due) {
				due = stateDue
			}
		}
		job.ScheduledFor = stringPtr(due.Format(time.RFC3339Nano))
		var oldJobRef *firestore.DocumentRef
		var oldJob storedProfileJob
		if current.Job != nil {
			oldJobRef = stateRef.Collection("jobs").Doc(current.Job.JobID)
			oldJobSnapshot, getErr := tx.Get(oldJobRef)
			if getErr != nil {
				return getErr
			}
			oldJob, err = profileJobFromData(oldJobSnapshot.Data())
			if err != nil {
				return err
			}
		}
		if err := tx.Create(candidateRef, candidate); err != nil {
			return err
		}
		if err := tx.Create(jobRef, profileJobData(job)); err != nil {
			return err
		}
		if err := enqueueProfileTagging(tx, r.client, userID, projectID, revision, job); err != nil {
			return err
		}
		if oldJobRef != nil && oldJob.Status != profileJobSuperseded {
			oldJob.Status = profileJobSuperseded
			oldJob.ErrorCode = stringPtr("superseded")
			if err := tx.Set(oldJobRef, profileJobData(oldJob)); err != nil {
				return err
			}
		}
		current.DerivationStatus = stringPtr(profileDerivationReady)
		current.ScheduledFor = nil
		current.DerivationErrorCode = nil
		current.CompileRetryGeneration = ""
		current.Candidate = &candidate
		current.ConfirmedCandidateID = nil
		current.Job = &job.ProfileJob
		result = current
		reconcile.Status = "candidate_ready"
		reconcile.CandidateID = stringPtr(candidate.CandidateID)
		if err := tx.Set(reconcileRef, reconcile); err != nil {
			return err
		}
		return setProfileState(tx, stateRef, result)
	})
	if err != nil {
		return result, err
	}
	if stale {
		return result, &profileRevisionConflict{Latest: result.Revision}
	}
	if empty {
		return result, nil
	}
	return result, nil
}

func isLowerProfileDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func profileBootstrapRefMatches(bootstrap *ProfileBootstrapGuidance, consumed *profileartifacts.BootstrapGuidanceRef, revision int64, digest string) bool {
	if bootstrap == nil || consumed == nil || bootstrap.Status != profileBootstrapConfirmed ||
		bootstrap.ProfileRevision != revision || bootstrap.InputDigest != digest || bootstrap.ConfirmedAt == nil ||
		strings.TrimSpace(*bootstrap.ConfirmedAt) == "" {
		return false
	}
	expected := profileartifacts.BootstrapGuidanceRef{
		Revision: bootstrap.Revision, ProfileRevision: bootstrap.ProfileRevision, InputDigest: bootstrap.InputDigest,
		ModelVersion: bootstrap.ModelVersion, PromptVersion: bootstrap.PromptVersion, SchemaVersion: bootstrap.SchemaVersion,
	}
	return *consumed == expected && expected.SchemaVersion == profileartifacts.BootstrapGuidanceSchema &&
		isLowerProfileDigest(expected.Revision) && isLowerProfileDigest(expected.InputDigest) &&
		strings.TrimSpace(expected.ModelVersion) != "" && strings.TrimSpace(expected.PromptVersion) != ""
}

func profileCompileReconcileRef(stateRef *firestore.DocumentRef, generation string) *firestore.DocumentRef {
	generationID := sha256.Sum256([]byte(generation))
	return stateRef.Collection("reconciles").Doc(hex.EncodeToString(generationID[:]))
}

func readProfileCompileReconcile(ctx context.Context, tx *firestore.Transaction, ref *firestore.DocumentRef) (profileCompileReconcile, error) {
	snapshot, err := tx.Get(ref)
	if err != nil {
		return profileCompileReconcile{}, err
	}
	encoded, err := json.Marshal(snapshot.Data())
	if err != nil {
		return profileCompileReconcile{}, err
	}
	var reconcile profileCompileReconcile
	if err := json.Unmarshal(encoded, &reconcile); err != nil {
		return profileCompileReconcile{}, err
	}
	if reconcile.ContentGeneration == "" || reconcile.LatestRevision < 0 {
		return profileCompileReconcile{}, errors.New("corrupt Profile compile reconcile intent")
	}
	return reconcile, nil
}

type storedProfileJob struct {
	ProfileJob
	TagSetRevision    string         `json:"tag_set_revision,omitempty" firestore:"tag_set_revision,omitempty"`
	QueryRuleRevision string         `json:"query_rule_revision,omitempty" firestore:"query_rule_revision,omitempty"`
	CoverageComplete  bool           `json:"coverage_complete,omitempty" firestore:"coverage_complete,omitempty"`
	ExpectedActive    *ProfileActive `json:"expected_active,omitempty" firestore:"expected_active,omitempty"`
	ScheduledFor      *string        `json:"scheduled_for,omitempty" firestore:"scheduled_for,omitempty"`
}

type profileDerivationIntent struct {
	AttemptID          string  `json:"attempt_id" firestore:"attempt_id"`
	Revision           int64   `json:"revision" firestore:"revision"`
	Attempt            int64   `json:"attempt_number" firestore:"attempt_number"`
	RequirementsDigest string  `json:"requirements_digest" firestore:"requirements_digest"`
	Status             string  `json:"status" firestore:"status"`
	ScheduledFor       *string `json:"scheduled_for" firestore:"scheduled_for"`
	ErrorCode          *string `json:"error_code" firestore:"error_code"`
	Retry              bool    `json:"retry" firestore:"retry"`
}

type profileCompileReconcile struct {
	ContentGeneration  string  `json:"content_generation" firestore:"content_generation"`
	LatestRevision     int64   `json:"latest_revision" firestore:"latest_revision"`
	RequirementsDigest string  `json:"requirements_digest" firestore:"requirements_digest"`
	Status             string  `json:"status" firestore:"status"`
	RequestedAt        string  `json:"requested_at" firestore:"requested_at"`
	CandidateID        *string `json:"candidate_id" firestore:"candidate_id"`
}

func profileDerivationIntentRef(stateRef *firestore.DocumentRef) *firestore.DocumentRef {
	return stateRef.Collection("derivations").Doc("current")
}

func readCurrentProfileDerivationIntent(ctx context.Context, tx *firestore.Transaction, stateRef *firestore.DocumentRef) (profileDerivationIntent, error) {
	snapshot, err := tx.Get(profileDerivationIntentRef(stateRef))
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return profileDerivationIntent{}, nil
		}
		return profileDerivationIntent{}, err
	}
	return profileDerivationIntentFromData(snapshot.Data())
}

func readProfileDerivationAttemptHistory(ctx context.Context, tx *firestore.Transaction, stateRef *firestore.DocumentRef, attemptID string) (*firestore.DocumentRef, profileDerivationIntent, error) {
	if strings.TrimSpace(attemptID) == "" {
		return nil, profileDerivationIntent{}, errProfileTransitionInvalid
	}
	ref := profileDerivationIntentRef(stateRef).Collection("attempts").Doc(attemptID)
	snapshot, err := tx.Get(ref)
	if err != nil {
		return nil, profileDerivationIntent{}, err
	}
	history, err := profileDerivationIntentFromData(snapshot.Data())
	if err != nil {
		return nil, profileDerivationIntent{}, err
	}
	if history.AttemptID != attemptID {
		return nil, profileDerivationIntent{}, errors.New("profile derivation attempt history ID mismatch")
	}
	return ref, history, nil
}

func profileDerivationIntentFromData(data map[string]interface{}) (profileDerivationIntent, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return profileDerivationIntent{}, err
	}
	var intent profileDerivationIntent
	if err := json.Unmarshal(encoded, &intent); err != nil {
		return profileDerivationIntent{}, err
	}
	if intent.AttemptID == "" && intent.Attempt == 0 && intent.Status == profileJobSuperseded {
		return intent, nil
	}
	if intent.AttemptID == "" || intent.Attempt < 1 || intent.Revision < 1 {
		return profileDerivationIntent{}, errors.New("corrupt profile derivation intent")
	}
	return intent, nil
}

func profileJobFromData(data map[string]interface{}) (storedProfileJob, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return storedProfileJob{}, err
	}
	var job storedProfileJob
	if err := json.Unmarshal(encoded, &job); err != nil {
		return storedProfileJob{}, err
	}
	return job, nil
}

func profileJobData(job storedProfileJob) storedProfileJob { return job }

func profileCandidateFromData(data map[string]interface{}) (ProfileCandidate, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return ProfileCandidate{}, err
	}
	var candidate ProfileCandidate
	if err := json.Unmarshal(encoded, &candidate); err != nil {
		return ProfileCandidate{}, err
	}
	return candidate, nil
}

func activateProfileState(state *ProfileState, candidate ProfileCandidate, job storedProfileJob) bool {
	if job.CandidateID != candidate.CandidateID || job.ContentGeneration != candidate.ContentGeneration || job.Status != profileJobReady || job.MissingCount != 0 || !job.CoverageComplete || job.TagSetRevision == "" || job.QueryRuleRevision == "" || state.Candidate == nil || state.Candidate.CandidateID != candidate.CandidateID {
		return false
	}
	if candidate.BaseRevision != state.Revision || candidate.RequirementsDigest != profileRequirementsDigest(state.Requirements) {
		return false
	}
	if candidate.Source == "manual" && (state.ConfirmedCandidateID == nil || *state.ConfirmedCandidateID != candidate.CandidateID) {
		return false
	}
	if candidate.Source != "manual" && candidate.Source != "compile_auto" {
		return false
	}
	wanted := &ProfileActive{
		CandidateID: candidate.CandidateID, ContentGeneration: candidate.ContentGeneration,
		DictionaryRevision: candidate.Dictionary.Revision, TagSetRevision: job.TagSetRevision,
		QueryRuleRevision: job.QueryRuleRevision, GuidanceRevision: candidate.Guidance.Revision,
	}
	if sameProfileActive(state.Active, wanted) {
		return true
	}
	if !sameProfileActive(state.Active, job.ExpectedActive) {
		return false
	}
	state.Active = wanted
	return true
}

func sameProfileActive(a, b *ProfileActive) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func cloneProfileActive(active *ProfileActive) *ProfileActive {
	if active == nil {
		return nil
	}
	copy := *active
	return &copy
}

func validProfileJobStatus(status string) bool {
	switch status {
	case profileJobScheduled, profileJobRunning, profileJobRetryWait, profileJobIncomplete, profileJobReady, profileJobSuperseded:
		return true
	default:
		return false
	}
}

func validProfileJobTransition(current, next string) bool {
	if current == next || next == profileJobSuperseded {
		return true
	}
	switch current {
	case profileJobScheduled:
		return false
	case profileJobRunning:
		return next == profileJobRetryWait || next == profileJobIncomplete || next == profileJobReady
	case profileJobRetryWait:
		return next == profileJobIncomplete
	case profileJobIncomplete:
		return next == profileJobScheduled
	default:
		return false
	}
}

func stringPtr(value string) *string { return &value }

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

type profilePutRequest struct {
	ExpectedRevision *int64                `json:"expected_revision" binding:"required"`
	Requirements     *[]ProfileRequirement `json:"requirements" binding:"required"`
}

type profileRevisionRequest struct {
	ExpectedRevision *int64 `json:"expected_revision" binding:"required"`
}

type profileConflictResponse struct {
	Error    string `json:"error"`
	Revision int64  `json:"revision"`
}

func (h *Handler) profileScope(c *gin.Context) (string, string, bool) {
	userID := strings.TrimSpace(c.GetString("userID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return "", "", false
	}
	projectID := strings.TrimSpace(c.Param("projectID"))
	if projectID == "" {
		projectID = strings.TrimSpace(c.Param("pid"))
	}
	headerProjectID := strings.TrimSpace(c.GetHeader("X-Project-ID"))
	if !auth.ValidPathSegment(projectID) || !auth.ValidPathSegment(headerProjectID) || headerProjectID != projectID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project ID mismatch"})
		return "", "", false
	}
	if h.profileRepository == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "profile storage unavailable"})
		return "", "", false
	}
	return userID, projectID, true
}

func decodeProfileJSON(c *gin.Context, target any) error {
	if c.Request.Body == nil {
		return errors.New("profile request body is empty")
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, maxProfileRequestBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxProfileRequestBytes {
		return errors.New("profile request body too large")
	}
	if !utf8.Valid(data) {
		return errors.New("profile request body is not valid UTF-8")
	}
	return decodeStrictJSON(bytes.NewReader(data), target)
}

func (h *Handler) writeProfileError(c *gin.Context, userID, projectID string, err error) {
	if errors.Is(err, errProfileProjectNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	var conflict *profileRevisionConflict
	if errors.As(err, &conflict) {
		c.JSON(http.StatusConflict, profileConflictResponse{Error: "profile revision conflict", Revision: conflict.Latest})
		return
	}
	if errors.Is(err, errProfileCandidateNotCurrent) || errors.Is(err, errProfileTransitionInvalid) {
		state, readErr := h.profileRepository.GetProfile(c.Request.Context(), userID, projectID)
		if readErr != nil {
			h.writeProfileError(c, userID, projectID, readErr)
			return
		}
		c.JSON(http.StatusConflict, profileConflictResponse{Error: "profile candidate is no longer current", Revision: state.Revision})
		return
	}
	if errors.Is(err, errInvalidProfileRequirements) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid profile requirements"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "profile storage unavailable"})
}

// GetProfile handles GET /api/v1/projects/{projectID}/profile.
//
//	@Summary	Get project Profile state
//	@Tags		profile
//	@Security	BearerAuth
//	@Param		projectID	path	string	true	"Project ID"
//	@Param		X-Project-ID	header	string	true	"Project ID; must match the URL"
//	@Success	200	{object}	ProfileState
//	@Failure	401	{object}	handler.ErrorResponse
//	@Failure	404	{object}	handler.ErrorResponse
//	@Failure	500	{object}	handler.ErrorResponse
//	@Router		/api/v1/projects/{projectID}/profile [get]
func (h *Handler) GetProfile(c *gin.Context) {
	userID, projectID, ok := h.profileScope(c)
	if !ok {
		return
	}
	state, err := h.profileRepository.GetProfile(c.Request.Context(), userID, projectID)
	if err != nil {
		h.writeProfileError(c, userID, projectID, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

// PutProfile handles PUT /api/v1/projects/{projectID}/profile.
//
//	@Summary	Save project Profile requirements
//	@Tags		profile
//	@Security	BearerAuth
//	@Param		projectID	path	string	true	"Project ID"
//	@Param		X-Project-ID	header	string	true	"Project ID; must match the URL"
//	@Param		request	body	profilePutRequest	true	"Expected revision and complete requirements"
//	@Success	200	{object}	ProfileState
//	@Failure	400	{object}	handler.ErrorResponse
//	@Failure	401	{object}	handler.ErrorResponse
//	@Failure	404	{object}	handler.ErrorResponse
//	@Failure	409	{object}	profileConflictResponse
//	@Failure	428	{object}	handler.ErrorResponse
//	@Failure	500	{object}	handler.ErrorResponse
//	@Router		/api/v1/projects/{projectID}/profile [put]
func (h *Handler) PutProfile(c *gin.Context) {
	userID, projectID, ok := h.profileScope(c)
	if !ok {
		return
	}
	var request profilePutRequest
	if err := decodeProfileJSON(c, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}
	if request.ExpectedRevision == nil {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "expected_revision is required"})
		return
	}
	if *request.ExpectedRevision < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected_revision must be non-negative"})
		return
	}
	if request.Requirements == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "requirements must be an array"})
		return
	}
	state, err := h.profileRepository.SaveProfile(c.Request.Context(), userID, projectID, *request.ExpectedRevision, *request.Requirements)
	if err != nil {
		h.writeProfileError(c, userID, projectID, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

// ConfirmProfileCandidate handles POST /api/v1/projects/{projectID}/profile/candidates/{candidateID}/confirm.
//
//	@Summary	Confirm the current Profile candidate
//	@Tags		profile
//	@Security	BearerAuth
//	@Param		projectID	path	string	true	"Project ID"
//	@Param		candidateID	path	string	true	"Candidate ID"
//	@Param		X-Project-ID	header	string	true	"Project ID; must match the URL"
//	@Param		request	body	profileRevisionRequest	true	"Expected revision"
//	@Success	200	{object}	ProfileState
//	@Failure	400	{object}	handler.ErrorResponse
//	@Failure	401	{object}	handler.ErrorResponse
//	@Failure	404	{object}	handler.ErrorResponse
//	@Failure	409	{object}	profileConflictResponse
//	@Failure	428	{object}	handler.ErrorResponse
//	@Failure	500	{object}	handler.ErrorResponse
//	@Router		/api/v1/projects/{projectID}/profile/candidates/{candidateID}/confirm [post]
func (h *Handler) ConfirmProfileCandidate(c *gin.Context) {
	h.mutateProfileCandidate(c, true)
}

// RetryProfileCandidate handles POST /api/v1/projects/{projectID}/profile/candidates/{candidateID}/retry.
//
//	@Summary	Retry missing Profile tagging work
//	@Tags		profile
//	@Security	BearerAuth
//	@Param		projectID	path	string	true	"Project ID"
//	@Param		candidateID	path	string	true	"Candidate ID"
//	@Param		X-Project-ID	header	string	true	"Project ID; must match the URL"
//	@Param		request	body	profileRevisionRequest	true	"Expected revision"
//	@Success	200	{object}	ProfileState
//	@Failure	400	{object}	handler.ErrorResponse
//	@Failure	401	{object}	handler.ErrorResponse
//	@Failure	404	{object}	handler.ErrorResponse
//	@Failure	409	{object}	profileConflictResponse
//	@Failure	428	{object}	handler.ErrorResponse
//	@Failure	500	{object}	handler.ErrorResponse
//	@Router		/api/v1/projects/{projectID}/profile/candidates/{candidateID}/retry [post]
func (h *Handler) RetryProfileCandidate(c *gin.Context) {
	h.mutateProfileCandidate(c, false)
}

func (h *Handler) mutateProfileCandidate(c *gin.Context, confirm bool) {
	userID, projectID, ok := h.profileScope(c)
	if !ok {
		return
	}
	candidateID := strings.TrimSpace(c.Param("candidateID"))
	if !auth.ValidPathSegment(candidateID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid candidate ID"})
		return
	}
	var request profileRevisionRequest
	if err := decodeProfileJSON(c, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}
	if request.ExpectedRevision == nil {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "expected_revision is required"})
		return
	}
	if *request.ExpectedRevision < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected_revision must be non-negative"})
		return
	}
	var state ProfileState
	var err error
	if confirm {
		state, err = h.profileRepository.ConfirmProfileCandidate(c.Request.Context(), userID, projectID, candidateID, *request.ExpectedRevision)
	} else {
		state, err = h.profileRepository.RetryProfileTagging(c.Request.Context(), userID, projectID, candidateID, *request.ExpectedRevision)
	}
	if err != nil {
		h.writeProfileError(c, userID, projectID, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

// RetryProfileDerivation handles the accepted recovery route for failed derivation before a candidate exists.
//
//	@Summary	Retry failed Profile derivation
//	@Tags		profile
//	@Security	BearerAuth
//	@Param		projectID	path	string	true	"Project ID"
//	@Param		X-Project-ID	header	string	true	"Project ID; must match the URL"
//	@Param		request	body	profileRevisionRequest	true	"Expected revision"
//	@Success	200	{object}	ProfileState
//	@Failure	400	{object}	handler.ErrorResponse
//	@Failure	401	{object}	handler.ErrorResponse
//	@Failure	404	{object}	handler.ErrorResponse
//	@Failure	409	{object}	profileConflictResponse
//	@Failure	428	{object}	handler.ErrorResponse
//	@Failure	500	{object}	handler.ErrorResponse
//	@Router		/api/v1/projects/{projectID}/profile/derivation/retry [post]
func (h *Handler) RetryProfileDerivation(c *gin.Context) {
	userID, projectID, ok := h.profileScope(c)
	if !ok {
		return
	}
	var request profileRevisionRequest
	if err := decodeProfileJSON(c, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}
	if request.ExpectedRevision == nil {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "expected_revision is required"})
		return
	}
	if *request.ExpectedRevision < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected_revision must be non-negative"})
		return
	}
	state, err := h.profileRepository.RetryProfileDerivation(c.Request.Context(), userID, projectID, *request.ExpectedRevision)
	if err != nil {
		h.writeProfileError(c, userID, projectID, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

// GetProfileJob returns an exact job scoped beneath the authorized Project Profile.
//
//	@Summary	Get a Profile tagging job
//	@Tags		profile
//	@Security	BearerAuth
//	@Param		projectID	path	string	true	"Project ID"
//	@Param		jobID	path	string	true	"Job ID"
//	@Param		X-Project-ID	header	string	true	"Project ID; must match the URL"
//	@Success	200	{object}	ProfileJob
//	@Failure	400	{object}	handler.ErrorResponse
//	@Failure	401	{object}	handler.ErrorResponse
//	@Failure	404	{object}	handler.ErrorResponse
//	@Failure	500	{object}	handler.ErrorResponse
//	@Router		/api/v1/projects/{projectID}/profile/jobs/{jobID} [get]
func (h *Handler) GetProfileJob(c *gin.Context) {
	userID, projectID, ok := h.profileScope(c)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(c.Param("jobID"))
	if !auth.ValidPathSegment(jobID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid job ID"})
		return
	}
	job, err := h.profileRepository.GetProfileJob(c.Request.Context(), userID, projectID, jobID)
	if err != nil {
		h.writeProfileError(c, userID, projectID, err)
		return
	}
	c.JSON(http.StatusOK, job)
}

type profileWorkerRepository interface {
	ClaimProfileDerivation(context.Context, string, string, int64, string) (ProfileState, error)
	ProfileBootstrapDerivationSucceeded(context.Context, string, string, int64, string, string, ProfileBootstrapGuidance) (ProfileState, error)
	ClaimProfileJob(context.Context, string, string, int64, string, string) (ProfileState, error)
	ProfileDerivationSucceeded(context.Context, string, string, int64, string, string, string, ProfileDerivedRef, ProfileDerivedRef, ProfilePreview) (ProfileState, error)
	ProfileDerivationFailed(context.Context, string, string, int64, string, string, string) (ProfileState, error)
	ProfileJobTransition(context.Context, string, string, int64, string, string, string, int64, string, string, string, bool) (ProfileState, error)
	ProfileCompileReconcileRequested(context.Context, string, string, string, int64, string) error
	ProfileCompileCandidateReady(context.Context, string, string, string, int64, string, ProfileDerivedRef, ProfilePreview) (ProfileState, error)
	ProfileBootstrapCompileCandidateReady(context.Context, string, string, string, int64, string, *profileartifacts.BootstrapGuidanceRef, ProfileDerivedRef, ProfileDerivedRef, ProfilePreview) (ProfileState, error)
}

// ClaimProfileDerivation starts one due server-scheduled derivation attempt.
func (h *Handler) ClaimProfileDerivation(ctx context.Context, userID, projectID string, revision int64, attemptID string) (ProfileState, error) {
	worker, ok := h.profileRepository.(profileWorkerRepository)
	if !ok {
		return ProfileState{}, errors.New("profile worker repository unavailable")
	}
	return worker.ClaimProfileDerivation(ctx, userID, projectID, revision, attemptID)
}

// ClaimProfileJob starts one due, current candidate tagging job.
func (h *Handler) ClaimProfileJob(ctx context.Context, userID, projectID string, revision int64, candidateID, jobID string) (ProfileState, error) {
	worker, ok := h.profileRepository.(profileWorkerRepository)
	if !ok {
		return ProfileState{}, errors.New("profile worker repository unavailable")
	}
	return worker.ClaimProfileJob(ctx, userID, projectID, revision, candidateID, jobID)
}

// CompleteProfileDerivation persists an authenticated worker's versioned candidate result.
func (h *Handler) CompleteProfileDerivation(ctx context.Context, userID, projectID string, revision int64, attemptID, requirementsDigest, generation string, dictionary, guidance ProfileDerivedRef, preview ProfilePreview) (ProfileState, error) {
	worker, ok := h.profileRepository.(profileWorkerRepository)
	if !ok {
		return ProfileState{}, errors.New("profile worker repository unavailable")
	}
	return worker.ProfileDerivationSucceeded(ctx, userID, projectID, revision, attemptID, requirementsDigest, generation, dictionary, guidance, preview)
}

// FailProfileDerivation records a versioned, retryable worker failure.
func (h *Handler) FailProfileDerivation(ctx context.Context, userID, projectID string, revision int64, attemptID, requirementsDigest, errorCode string) (ProfileState, error) {
	worker, ok := h.profileRepository.(profileWorkerRepository)
	if !ok {
		return ProfileState{}, errors.New("profile worker repository unavailable")
	}
	return worker.ProfileDerivationFailed(ctx, userID, projectID, revision, attemptID, requirementsDigest, errorCode)
}

// TransitionProfileJob updates one scoped job; ready is accepted only with complete coverage.
func (h *Handler) TransitionProfileJob(ctx context.Context, userID, projectID string, revision int64, candidateID, jobID, nextStatus string, missingCount int64, errorCode, tagSetRevision, queryRuleRevision string, coverageComplete bool) (ProfileState, error) {
	worker, ok := h.profileRepository.(profileWorkerRepository)
	if !ok {
		return ProfileState{}, errors.New("profile worker repository unavailable")
	}
	return worker.ProfileJobTransition(ctx, userID, projectID, revision, candidateID, jobID, nextStatus, missingCount, errorCode, tagSetRevision, queryRuleRevision, coverageComplete)
}

// RequestProfileCompileReconcile durably deduplicates generation work at the current Profile revision.
func (h *Handler) RequestProfileCompileReconcile(ctx context.Context, userID, projectID, generation string, observedRevision int64, observedDigest string) error {
	worker, ok := h.profileRepository.(profileWorkerRepository)
	if !ok {
		return errors.New("profile worker repository unavailable")
	}
	return worker.ProfileCompileReconcileRequested(ctx, userID, projectID, generation, observedRevision, observedDigest)
}

// CompleteProfileCompileCandidate persists a compile_auto Tags-only candidate while pinning existing guidance.
func (h *Handler) CompleteProfileCompileCandidate(ctx context.Context, userID, projectID, generation string, revision int64, requirementsDigest string, dictionary ProfileDerivedRef, preview ProfilePreview) (ProfileState, error) {
	worker, ok := h.profileRepository.(profileWorkerRepository)
	if !ok {
		return ProfileState{}, errors.New("profile worker repository unavailable")
	}
	return worker.ProfileCompileCandidateReady(ctx, userID, projectID, generation, revision, requirementsDigest, dictionary, preview)
}

// CompleteProfileBootstrapCompileCandidate stages the first generation-bound
// artifacts from the same typed result passed by the successful compile batch.
func (h *Handler) CompleteProfileBootstrapCompileCandidate(ctx context.Context, userID, projectID string, success ProfileCompileSuccess, revision int64, requirementsDigest string, dictionary, guidance ProfileDerivedRef, preview ProfilePreview) (ProfileState, error) {
	worker, ok := h.profileRepository.(profileWorkerRepository)
	if !ok {
		return ProfileState{}, errors.New("profile worker repository unavailable")
	}
	if err := success.validate(); err != nil {
		return ProfileState{}, err
	}
	return worker.ProfileBootstrapCompileCandidateReady(ctx, userID, projectID, success.manifest.GenerationID, revision, requirementsDigest, success.consumedBootstrapGuidance, dictionary, guidance, preview)
}
