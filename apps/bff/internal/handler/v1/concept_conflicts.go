package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	pathpkg "path"
	"sort"
	"strconv"
	"strings"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	"gopkg.in/yaml.v3"
)

const maxConceptConflictMarkdownBytes = 4 << 20

type conceptConflictVersion struct {
	Path     string                 `json:"path"`
	SHA256   string                 `json:"sha256"`
	Content  string                 `json:"content,omitempty"`
	Identity conceptArticleIdentity `json:"identity"`
}

type conceptConflictSummary struct {
	ConflictID    string                 `json:"conflict_id"`
	Published     conceptConflictVersion `json:"published"`
	Candidate     conceptConflictVersion `json:"candidate"`
	IndexSHA256   string                 `json:"index_sha256"`
	Confidence    float64                `json:"confidence"`
	MinConfidence float64                `json:"min_confidence"`
	FirstSeenRun  string                 `json:"first_seen_run,omitempty"`
	IsNew         bool                   `json:"is_new"`
	Eligible      bool                   `json:"eligible"`
}

type conceptConflictListResponse struct {
	ProjectID     string                   `json:"project_id"`
	StorageView   string                   `json:"storage_view"`
	PairSHA256    string                   `json:"pair_sha256"`
	RunID         string                   `json:"run_id,omitempty"`
	NewCount      int                      `json:"new_count"`
	PendingCount  int                      `json:"pending_count"`
	HeldBackCount int                      `json:"held_back_count"`
	Conflicts     []conceptConflictSummary `json:"conflicts"`
	HeldBack      []conceptConflictSummary `json:"held_back"`
	Count         int                      `json:"count"`
}

type conceptConflictCompareResponse struct {
	ProjectID     string                 `json:"project_id"`
	StorageView   string                 `json:"storage_view"`
	PairSHA256    string                 `json:"pair_sha256"`
	ConflictID    string                 `json:"conflict_id"`
	IndexSHA256   string                 `json:"index_sha256"`
	Confidence    float64                `json:"confidence"`
	MinConfidence float64                `json:"min_confidence"`
	Published     conceptConflictVersion `json:"published"`
	Candidate     conceptConflictVersion `json:"candidate"`
}

type boundedConflictFileReader interface {
	ReadFileLimited(context.Context, string, int64) ([]byte, error)
}

func readConceptConflictFile(ctx context.Context, scoped store.Store, relPath string) ([]byte, error) {
	return readConceptFile(ctx, scoped, relPath, maxConceptConflictMarkdownBytes)
}

func readConceptFile(ctx context.Context, scoped store.Store, relPath string, maxBytes int) ([]byte, error) {
	if reader, ok := scoped.(boundedConflictFileReader); ok {
		data, err := reader.ReadFileLimited(ctx, relPath, int64(maxBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxBytes {
			return nil, errConceptConflictTooLarge
		}
		return data, nil
	}
	data, err := scoped.ReadFile(ctx, relPath)
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, errConceptConflictTooLarge
	}
	return data, nil
}

var (
	errConceptConflictTooLarge       = errors.New("concept conflict article exceeds comparison limit")
	errInvalidConceptPath            = errors.New("invalid published concept path")
	errPublishedConceptFileMissing   = errors.New("published concept file is missing while its Synto row exists")
	errConceptConflictBelowThreshold = errors.New("concept candidate is below the applicable confidence threshold")
	errConceptConflictSnapshotBusy   = errors.New("local publication is in progress")
)

type conceptConflictReadLeaser interface {
	WithWorkerReadLease(context.Context, func() error) error
}

func withConceptConflictReadLease(ctx context.Context, scoped store.Store, read func() error) error {
	leaser, ok := scoped.(conceptConflictReadLeaser)
	if !ok {
		return read()
	}
	err := leaser.WithWorkerReadLease(ctx, read)
	if errors.Is(err, localfs.ErrWorkerLeaseHeld) {
		return fmt.Errorf("%w: %v", errConceptConflictSnapshotBusy, err)
	}
	return err
}

func normalizePublishedConceptPath(raw string) (string, bool) {
	if raw == "" || pathpkg.IsAbs(raw) || strings.ContainsAny(raw, "\\\x00") ||
		pathpkg.Clean(raw) != raw || !strings.HasPrefix(raw, "wiki/") || !strings.HasSuffix(raw, ".md") {
		return "", false
	}
	rel := strings.TrimPrefix(raw, "wiki/")
	if rel == "" || strings.HasPrefix(rel, ".drafts/") || rel == ".drafts" {
		return "", false
	}
	return raw, true
}

func candidatePathForPublished(publishedPath string) string {
	rel := strings.TrimPrefix(publishedPath, "wiki/")
	return pathpkg.Join("wiki/.drafts", rel)
}

func conceptConflictHash(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func identityDigest(identity conceptArticleIdentity) string {
	data, _ := json.Marshal(identity)
	return conceptConflictHash(data)
}

func conceptConfidence(data []byte) float64 {
	text := strings.TrimPrefix(string(data), "\ufeff")
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return 1
	}
	text = strings.TrimLeft(text[3:], "\r\n")
	end := strings.Index(text, "\n---")
	if end < 0 {
		return 0
	}
	var metadata map[string]any
	if yaml.Unmarshal([]byte(text[:end]), &metadata) != nil {
		return 0
	}
	value, exists := metadata["confidence"]
	if !exists {
		return 1
	}
	var confidence float64
	switch typed := value.(type) {
	case float64:
		confidence = typed
	case int:
		confidence = float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0
		}
		confidence = parsed
	default:
		return 0
	}
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
		return 0
	}
	return confidence
}

func conceptConflictID(projectID, publishedPath, publishedSHA256, candidateSHA256 string) string {
	return conceptConflictHash([]byte(strings.Join([]string{projectID, publishedPath, publishedSHA256, candidateSHA256}, "\x00")))
}

func conceptConflictView(scoped store.Store) string {
	if view, ok := scoped.(store.ViewToken); ok {
		return view.ViewToken()
	}
	return ""
}

func conceptConflictStorageView(projectID string, scoped store.Store, conflicts []conceptConflictSummary) string {
	view := conceptConflictView(scoped)
	if view != "" && view != "legacy" {
		return view
	}
	// Local storage has no manifest generation; this digest identifies the
	// database rows, files, and index read while holding the worker lease.
	return "local-lease-" + conceptConflictPairDigest(projectID, "local-lease", conflicts)
}

func conceptConflictPairDigest(projectID, view string, conflicts []conceptConflictSummary) string {
	h := sha256.New()
	_, _ = h.Write([]byte(projectID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(view))
	for _, conflict := range conflicts {
		for _, value := range []string{
			conflict.Published.Path,
			conflict.Published.SHA256,
			identityDigest(conflict.Published.Identity),
			conflict.Candidate.Path,
			conflict.Candidate.SHA256,
			identityDigest(conflict.Candidate.Identity),
			conflict.IndexSHA256,
			fmt.Sprintf("%.9f", conflict.MinConfidence),
		} {
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(value))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func conceptConflictSummaryForPath(ctx context.Context, projectID string, scoped store.Store, state *conceptConflictDatabase, publishedPath string, includeContent bool) (conceptConflictSummary, error) {
	publishedPath, ok := normalizePublishedConceptPath(publishedPath)
	if !ok {
		return conceptConflictSummary{}, errInvalidConceptPath
	}
	candidatePath := candidatePathForPublished(publishedPath)
	candidate, err := readConceptConflictFile(ctx, scoped, candidatePath)
	if err != nil {
		return conceptConflictSummary{}, err
	}
	publishedIdentity, err := state.articleIdentity(ctx, publishedPath)
	if err != nil {
		return conceptConflictSummary{}, err
	}
	published, err := readConceptConflictFile(ctx, scoped, publishedPath)
	if err != nil {
		if (errors.Is(err, store.ErrObjectNotExist) || errors.Is(err, cloudstorage.ErrObjectNotExist)) && publishedIdentity.Exists {
			return conceptConflictSummary{}, errPublishedConceptFileMissing
		}
		return conceptConflictSummary{}, err
	}
	candidateIdentity, err := state.articleIdentity(ctx, candidatePath)
	if err != nil {
		return conceptConflictSummary{}, err
	}
	confidence := conceptConfidence(candidate)
	indexSHA256 := ""
	index, err := readConceptFile(ctx, scoped, ".synto/INDEX.json", maxConceptConflictStateDBBytes)
	if err == nil {
		indexSHA256 = conceptConflictHash(index)
	} else if !errors.Is(err, store.ErrObjectNotExist) && !errors.Is(err, cloudstorage.ErrObjectNotExist) {
		return conceptConflictSummary{}, err
	}
	publishedSHA256 := conceptConflictHash(published)
	candidateSHA256 := conceptConflictHash(candidate)
	conflictID := conceptConflictID(projectID, publishedPath, publishedSHA256, candidateSHA256)
	firstSeen, pending, err := state.conflictFirstSeenRun(ctx, conflictID)
	if err != nil {
		return conceptConflictSummary{}, err
	}
	_, isNewThisRun := state.NewConflictIDs[conflictID]
	if !pending && firstSeen == "" {
		isNewThisRun = true
	}
	if !includeContent {
		published = nil
		candidate = nil
	}
	return conceptConflictSummary{
		ConflictID: conflictID,
		Published: conceptConflictVersion{
			Path: publishedPath, SHA256: publishedSHA256, Content: string(published), Identity: publishedIdentity,
		},
		Candidate: conceptConflictVersion{
			Path: candidatePath, SHA256: candidateSHA256, Content: string(candidate), Identity: candidateIdentity,
		},
		IndexSHA256: indexSHA256, Confidence: confidence, MinConfidence: state.MinConfidence,
		FirstSeenRun: firstSeen, IsNew: isNewThisRun, Eligible: confidence >= state.MinConfidence,
	}, nil
}

func countNewConceptConflicts(conflicts []conceptConflictSummary) int {
	count := 0
	for _, conflict := range conflicts {
		if conflict.IsNew {
			count++
		}
	}
	return count
}

func listConceptConflicts(ctx context.Context, projectID string, scoped store.Store) (conceptConflictListResponse, error) {
	var response conceptConflictListResponse
	err := withConceptConflictReadLease(ctx, scoped, func() error {
		var err error
		response, err = listConceptConflictsWithinLease(ctx, projectID, scoped)
		return err
	})
	return response, err
}

func listConceptConflictsWithinLease(ctx context.Context, projectID string, scoped store.Store) (conceptConflictListResponse, error) {
	drafts, err := scoped.ListMarkdownFiles(ctx, "wiki/.drafts/")
	if err != nil {
		return conceptConflictListResponse{}, err
	}
	state, closeState, err := readConceptConflictDatabase(ctx, scoped)
	if err != nil {
		return conceptConflictListResponse{}, err
	}
	defer closeState()
	conflicts := make([]conceptConflictSummary, 0)
	heldBack := make([]conceptConflictSummary, 0)
	for _, draft := range drafts {
		if len(draft.Data) > maxConceptConflictMarkdownBytes {
			return conceptConflictListResponse{}, errConceptConflictTooLarge
		}
		candidatePath := draft.Path
		if !strings.HasPrefix(candidatePath, "wiki/.drafts/") || pathpkg.Clean(candidatePath) != candidatePath {
			continue
		}
		publishedPath, ok := normalizePublishedConceptPath("wiki/" + strings.TrimPrefix(candidatePath, "wiki/.drafts/"))
		if !ok {
			continue
		}
		conflict, err := conceptConflictSummaryForPath(ctx, projectID, scoped, state, publishedPath, false)
		if errors.Is(err, store.ErrObjectNotExist) || errors.Is(err, cloudstorage.ErrObjectNotExist) {
			continue
		}
		if err != nil {
			return conceptConflictListResponse{}, err
		}
		if conflict.Eligible {
			conflicts = append(conflicts, conflict)
		} else {
			heldBack = append(heldBack, conflict)
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].Published.Path < conflicts[j].Published.Path
	})
	sort.Slice(heldBack, func(i, j int) bool {
		return heldBack[i].Published.Path < heldBack[j].Published.Path
	})
	view := conceptConflictStorageView(projectID, scoped, conflicts)
	return conceptConflictListResponse{
		ProjectID: projectID, StorageView: view,
		PairSHA256: conceptConflictPairDigest(projectID, view, conflicts),
		RunID:      state.RunID, NewCount: countNewConceptConflicts(conflicts), PendingCount: len(conflicts),
		HeldBackCount: len(heldBack), Conflicts: conflicts, HeldBack: heldBack, Count: len(conflicts),
	}, nil
}

func compareConceptConflict(ctx context.Context, projectID string, scoped store.Store, publishedPath string) (conceptConflictCompareResponse, error) {
	var response conceptConflictCompareResponse
	err := withConceptConflictReadLease(ctx, scoped, func() error {
		state, closeState, err := readConceptConflictDatabase(ctx, scoped)
		if err != nil {
			return err
		}
		defer closeState()
		response, err = compareConceptConflictWithDatabase(ctx, projectID, scoped, publishedPath, state)
		return err
	})
	return response, err
}

func compareConceptConflictWithDatabase(ctx context.Context, projectID string, scoped store.Store, publishedPath string, state *conceptConflictDatabase) (conceptConflictCompareResponse, error) {
	conflict, err := conceptConflictSummaryForPath(ctx, projectID, scoped, state, publishedPath, true)
	if err != nil {
		return conceptConflictCompareResponse{}, err
	}
	view := conceptConflictStorageView(projectID, scoped, []conceptConflictSummary{conflict})
	return conceptConflictCompareResponse{
		ProjectID: projectID, StorageView: view,
		PairSHA256: conceptConflictPairDigest(projectID, view, []conceptConflictSummary{conflict}),
		ConflictID: conflict.ConflictID, IndexSHA256: conflict.IndexSHA256,
		Confidence: conflict.Confidence, MinConfidence: conflict.MinConfidence,
		Published: conflict.Published, Candidate: conflict.Candidate,
	}, nil
}

func (h *Handler) authorizeConceptConflictRead(c *gin.Context) (string, string, bool) {
	userID := strings.TrimSpace(c.GetString("userID"))
	projectID := strings.TrimSpace(c.GetString("projectID"))
	if userID == "" {
		c.JSON(http.StatusUnauthorized, handlerapi.ErrorResponse{Error: "user not authenticated"})
		return "", "", false
	}
	if projectID == "" {
		c.JSON(http.StatusBadRequest, handlerapi.ErrorResponse{Error: "project is required"})
		return "", "", false
	}
	if _, err := h.AuthorizeProject(c.Request.Context(), userID, projectID, ProjectRead); err != nil {
		if errors.Is(err, errProfileProjectNotFound) {
			c.JSON(http.StatusNotFound, handlerapi.ErrorResponse{Error: "project not found"})
		} else {
			c.JSON(http.StatusServiceUnavailable, handlerapi.ErrorResponse{Error: "project is unavailable"})
		}
		return "", "", false
	}
	return userID, projectID, true
}

func writeConceptConflictReadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errPublishedConceptFileMissing):
		c.JSON(http.StatusConflict, handlerapi.ErrorResponse{Error: "published concept file is inconsistent with Synto state"})
	case errors.Is(err, store.ErrObjectNotExist):
		c.JSON(http.StatusNotFound, handlerapi.ErrorResponse{Error: "concept conflict not found"})
	case errors.Is(err, cloudstorage.ErrObjectNotExist):
		c.JSON(http.StatusNotFound, handlerapi.ErrorResponse{Error: "concept conflict not found"})
	case errors.Is(err, errConceptConflictTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, handlerapi.ErrorResponse{Error: "concept conflict exceeds comparison limit"})
	case errors.Is(err, errConceptConflictBelowThreshold):
		c.JSON(http.StatusConflict, handlerapi.ErrorResponse{Error: "candidate is below the applicable confidence threshold"})
	case errors.Is(err, errConceptConflictSnapshotBusy):
		c.JSON(http.StatusConflict, handlerapi.ErrorResponse{Error: "project publication is in progress; retry comparison"})
	default:
		c.JSON(http.StatusInternalServerError, handlerapi.ErrorResponse{Error: "concept conflict data unavailable"})
	}
}

// ListConceptConflicts returns same-path pairs from one scoped project view.
func (h *Handler) ListConceptConflicts(c *gin.Context) {
	_, projectID, ok := h.authorizeConceptConflictRead(c)
	if !ok {
		return
	}
	scoped, err := h.GetStore(c)
	if err != nil {
		writeConceptConflictReadError(c, err)
		return
	}
	response, err := listConceptConflicts(c.Request.Context(), projectID, scoped)
	if err != nil {
		writeConceptConflictReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

// CompareConceptConflict returns both Markdown versions without mutating either one.
// Cloud reads are manifest-pinned; localfs holds the cooperative worker lease
// while reading the database, pair, and index, then the worker revalidates at commit.
func (h *Handler) CompareConceptConflict(c *gin.Context) {
	_, projectID, ok := h.authorizeConceptConflictRead(c)
	if !ok {
		return
	}
	publishedPath := c.Query("path")
	if _, valid := normalizePublishedConceptPath(publishedPath); !valid {
		c.JSON(http.StatusBadRequest, handlerapi.ErrorResponse{Error: "invalid published concept path"})
		return
	}
	scoped, err := h.GetStore(c)
	if err != nil {
		writeConceptConflictReadError(c, err)
		return
	}
	response, err := compareConceptConflict(c.Request.Context(), projectID, scoped, publishedPath)
	if err != nil {
		writeConceptConflictReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, response)
}
