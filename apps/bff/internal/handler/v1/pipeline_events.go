package v1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	pipelineEventHistoryLimit = 50
	pipelineEventGlobalLimit  = 100
	pipelineEventPageLimit    = 5
	pipelineEventReadTimeout  = 15 * time.Second
	pipelineEventPollInterval = 10 * time.Second
	pipelineEventAuthInterval = 20 * time.Second
	pipelineEventAuthTimeout  = 5 * time.Second
	pipelineEventAuthMaxAge   = 30 * time.Second
	pipelineEventWriteTimeout = 5 * time.Second
	pipelineEventListenMaxAge = 10 * time.Minute
)

const pipelineEventsResourceDescription = "Read-only Pipeline lifecycle snapshot for this authorized Project; requires mcp:query. A subscriptions/listen acknowledgement and each resource-updated notification are wake-ups only: immediately read this resource after acknowledgement, update, and reconnect. Merge fixed {run_id}:started and {run_id}:terminal phase IDs, applying changed event_revision values even below a saved time watermark. The snapshot keeps at most 50 history runs plus three exact current-run overlays; coverage complete/partial and history_truncated describe bounded recovery, and partial snapshots do not advance a contiguous checkpoint."

type mcpListenControlContextKey struct{}

type pipelineEventTiming struct {
	readTimeout  time.Duration
	pollInterval time.Duration
	authInterval time.Duration
	authTimeout  time.Duration
	authMaxAge   time.Duration
	writeTimeout time.Duration
	listenMaxAge time.Duration
	backoffs     []time.Duration
}

func (h *Handler) eventTiming() pipelineEventTiming {
	if h.pipelineEventTiming != nil {
		return *h.pipelineEventTiming
	}
	return pipelineEventTiming{
		readTimeout: pipelineEventReadTimeout, pollInterval: pipelineEventPollInterval,
		authInterval: pipelineEventAuthInterval, authTimeout: pipelineEventAuthTimeout,
		authMaxAge: pipelineEventAuthMaxAge, writeTimeout: pipelineEventWriteTimeout,
		listenMaxAge: pipelineEventListenMaxAge, backoffs: []time.Duration{time.Second, 2 * time.Second},
	}
}

type mcpListenControl struct {
	mu         sync.Mutex
	active     bool
	cancel     context.CancelFunc
	ack        chan struct{}
	ackOnce    sync.Once
	ackPending bool
	ackTail    []byte
	timing     pipelineEventTiming
}

func newMCPListenControl(timing pipelineEventTiming) *mcpListenControl {
	return &mcpListenControl{ack: make(chan struct{}), timing: timing}
}

func (c *mcpListenControl) activate(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, c.timing.listenMaxAge)
	c.mu.Lock()
	c.active, c.cancel = true, cancel
	c.mu.Unlock()
	return ctx, cancel
}

func (c *mcpListenControl) deactivate() {
	c.mu.Lock()
	c.active = false
	c.cancel = nil
	c.mu.Unlock()
}

func (c *mcpListenControl) isActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

func (c *mcpListenControl) setWriteDeadline(w http.ResponseWriter) error {
	err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(c.timing.writeTimeout))
	if err != nil {
		c.fail(err)
	}
	return err
}

func (c *mcpListenControl) fail(_ error) {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *mcpListenControl) observeWrite(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return
	}
	combined := append(append([]byte(nil), c.ackTail...), p...)
	if bytes.Contains(combined, []byte("notifications/subscriptions/acknowledged")) {
		c.ackPending = true
	}
	if len(combined) > 64 {
		combined = combined[len(combined)-64:]
	}
	c.ackTail = combined
}

func (c *mcpListenControl) acknowledgeAfterFlush() {
	c.mu.Lock()
	pending := c.ackPending
	c.ackPending = false
	c.mu.Unlock()
	if pending {
		c.ackOnce.Do(func() { close(c.ack) })
	}
}

type pipelineEventPosition struct {
	At    string `json:"at"`
	RunID string `json:"run_id"`
	Phase int    `json:"phase"`
}

type pipelineEventCoverage struct {
	State                 string                        `json:"state"`
	Reasons               []string                      `json:"reasons"`
	HistoryTruncated      bool                          `json:"history_truncated"`
	GlobalScanLimit       int                           `json:"global_scan_limit"`
	GlobalScanned         int                           `json:"global_scanned"`
	KnownRunIDs           []string                      `json:"known_run_ids"`
	UnresolvedKnownRunIDs []string                      `json:"unresolved_known_run_ids"`
	PendingAttempts       []pipelineEventPendingAttempt `json:"pending_attempts"`
}

type pipelineEventPendingAttempt struct {
	AttemptID     string `json:"attempt_id,omitempty"`
	ReservationID string `json:"reservation_id,omitempty"`
	ExecutionID   string `json:"execution_id,omitempty"`
	Trigger       string `json:"trigger,omitempty"`
	IntentStatus  string `json:"intent_status,omitempty"`
	Status        string `json:"status,omitempty"`
}

type pipelineEventSnapshot struct {
	SchemaVersion         int                    `json:"schema_version"`
	ProjectID             string                 `json:"project_id"`
	Revision              string                 `json:"revision"`
	Coverage              pipelineEventCoverage  `json:"coverage"`
	OldestPosition        *pipelineEventPosition `json:"oldest_position"`
	ObservedHighWatermark *pipelineEventPosition `json:"observed_high_watermark"`
	KnownRuns             []pipelineEventRun     `json:"known_runs"`
	Runs                  []pipelineEventRun     `json:"runs"`
}

type pipelineEventRun struct {
	RunID  string          `json:"run_id"`
	Status string          `json:"status"`
	Events []pipelineEvent `json:"events"`
}

type pipelineEvent struct {
	EventID         string                `json:"event_id"`
	EventRevision   string                `json:"event_revision"`
	Position        pipelineEventPosition `json:"position"`
	Kind            string                `json:"kind"`
	Outcome         string                `json:"outcome,omitempty"`
	OccurredAt      string                `json:"occurred_at"`
	Publication     string                `json:"publication_outcome,omitempty"`
	QuotaSettlement string                `json:"quota_settlement,omitempty"`
	Result          *pipelineEventResult  `json:"result,omitempty"`
	FailureReason   string                `json:"failure_reason,omitempty"`
	DiagnosticState string                `json:"diagnostic_state,omitempty"`
}

type pipelineEventResult struct {
	Name            string                                `json:"name"`
	Status          string                                `json:"status"`
	CompileMode     string                                `json:"compile_mode,omitempty"`
	StartTime       string                                `json:"start_time,omitempty"`
	EndTime         string                                `json:"end_time,omitempty"`
	Duration        string                                `json:"duration,omitempty"`
	Diagnostic      *handlerapi.PipelineFailureDiagnostic `json:"diagnostic,omitempty"`
	DiagnosticState string                                `json:"diagnostic_state,omitempty"`
	FailureReason   string                                `json:"failure_reason,omitempty"`
	QuotaSettlement string                                `json:"quota_settlement,omitempty"`
}

type pipelineEventRunRecord struct {
	id            string
	status        string
	startedAt     time.Time
	actualStarted time.Time
	endedAt       time.Time
	publication   string
	quota         string
	response      *handlerapi.PipelineExecutionResponse
	known         bool
	candidate     bool
}

func pipelineEventsURI(projectID string) string {
	return "llmwiki://project/" + projectID + "/pipeline/events"
}

func (h *Handler) readMCPPipelineEvents(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || !hasMCPCapability(identity.capabilities, mcpQueryCapability) || request == nil || request.Params == nil ||
		request.Params.URI != pipelineEventsURI(identity.projectID) {
		return nil, mcp.ResourceNotFoundError("Pipeline events resource not found")
	}
	readCtx, cancel := context.WithTimeout(ctx, h.eventTiming().readTimeout)
	defer cancel()
	snapshot, err := h.readPipelineEventSnapshot(readCtx, identity.userID, identity.projectID)
	if err != nil {
		return nil, fmt.Errorf("Pipeline event snapshot unavailable: %w", err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("Pipeline event snapshot unavailable: %w", err)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: request.Params.URI, MIMEType: "application/json", Text: string(data),
	}}}, nil
}

func (h *Handler) subscribeMCPPipelineEvents(ctx context.Context, request *mcp.SubscribeRequest, server *mcp.Server) error {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || !hasMCPCapability(identity.capabilities, mcpQueryCapability) || request == nil || request.Params == nil ||
		request.Params.URI != pipelineEventsURI(identity.projectID) {
		return mcp.ResourceNotFoundError("Pipeline events resource not found")
	}
	if identity.revalidate == nil {
		return errors.New("Pipeline event subscription authority cannot be revalidated")
	}
	control, ok := ctx.Value(mcpListenControlContextKey{}).(*mcpListenControl)
	if !ok || control == nil || server == nil {
		return errors.New("Pipeline event subscription control unavailable")
	}
	go h.watchMCPPipelineEvents(ctx, identity, pipelineEventsURI(identity.projectID), server, control)
	return nil
}

func (h *Handler) watchMCPPipelineEvents(ctx context.Context, identity mcpProjectKeyIdentity, uri string, server *mcp.Server, control *mcpListenControl) {
	timing := h.eventTiming()
	select {
	case <-control.ack:
	case <-ctx.Done():
		return
	}
	if err := revalidateMCPListen(ctx, identity, timing.authTimeout); err != nil {
		control.fail(err)
		return
	}
	go h.revalidateMCPListenLoop(ctx, identity, control)

	var revision string
	consecutiveFailures := 0
	nextPoll := time.Now()
	for {
		if wait := time.Until(nextPoll); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		nextPoll = time.Now().Add(timing.pollInterval)
		readCtx, cancel := context.WithTimeout(ctx, timing.readTimeout)
		snapshot, err := h.readPipelineEventSnapshot(readCtx, identity.userID, identity.projectID)
		cancel()
		if err != nil {
			consecutiveFailures++
			if consecutiveFailures >= 3 {
				control.fail(err)
				return
			}
			delay := timing.backoffs[consecutiveFailures-1]
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			nextPoll = time.Now()
			continue
		}
		consecutiveFailures = 0
		if revision == "" {
			revision = snapshot.Revision
			continue
		}
		if snapshot.Revision != revision {
			revision = snapshot.Revision
			if err := server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri}); err != nil {
				control.fail(err)
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func revalidateMCPListen(ctx context.Context, identity mcpProjectKeyIdentity, timeout time.Duration) error {
	if identity.revalidate == nil {
		return errors.New("Pipeline event subscription authority cannot be revalidated")
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return identity.revalidate(verifyCtx)
}

func (h *Handler) revalidateMCPListenLoop(ctx context.Context, identity mcpProjectKeyIdentity, control *mcpListenControl) {
	timing := h.eventTiming()
	timer := time.NewTimer(timing.authInterval)
	defer timer.Stop()
	lastSuccess := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if time.Since(lastSuccess) > timing.authMaxAge {
				control.fail(context.DeadlineExceeded)
				return
			}
			if err := revalidateMCPListen(ctx, identity, timing.authTimeout); err != nil {
				control.fail(err)
				return
			}
			lastSuccess = time.Now()
			timer.Reset(timing.authInterval)
		}
	}
}

func (h *Handler) readPipelineEventSnapshot(ctx context.Context, userID, projectID string) (pipelineEventSnapshot, error) {
	if h.pipelineEventSource != nil {
		return h.pipelineEventSource(ctx, userID, projectID)
	}
	if h.localPipeline != nil {
		return h.readLocalPipelineEventSnapshot(ctx, userID, projectID)
	}
	return h.readCloudPipelineEventSnapshot(ctx, userID, projectID)
}

func (h *Handler) readLocalPipelineEventSnapshot(ctx context.Context, userID, projectID string) (pipelineEventSnapshot, error) {
	if h.firestore == nil || h.firestore.Raw() == nil {
		return pipelineEventSnapshot{}, errors.New("Firestore source unavailable")
	}
	collection := scopedfirestore.Collection(h.firestore.Raw(), "executions")
	query := collection.Where("user_id", "==", userID).Where("project_id", "==", projectID).
		Where("native", "==", true).Where("task_type", "==", "pipeline").OrderBy("started_at", cloudfirestore.Desc).Limit(pipelineEventHistoryLimit + 1)
	documents, err := query.Documents(ctx).GetAll()
	if err != nil {
		return pipelineEventSnapshot{}, fmt.Errorf("read bounded local Pipeline history: %w", err)
	}
	truncated := len(documents) > pipelineEventHistoryLimit
	if truncated {
		documents = documents[:pipelineEventHistoryLimit]
	}
	candidates, err := scopedfirestore.LoadPipelineEventCandidates(ctx, h.firestore.Raw(), userID, projectID)
	if err != nil {
		return pipelineEventSnapshot{}, fmt.Errorf("read Project Pipeline intent identities: %w", err)
	}
	records := make(map[string]pipelineEventRunRecord, pipelineEventHistoryLimit+3)
	candidateSet := make(map[string]struct{}, len(candidates.Known))
	for _, candidate := range candidates.Known {
		candidateSet[candidate.ExecutionID] = struct{}{}
	}
	for _, document := range documents {
		data := document.Data()
		if data["user_id"] != userID || data["project_id"] != projectID || data["native"] != true || data["task_type"] != "pipeline" {
			continue
		}
		_, isCandidate := candidateSet[document.Ref.ID]
		record, err := h.localPipelineEventRecord(ctx, userID, projectID, document.Ref.ID, data, false)
		if err != nil {
			return pipelineEventSnapshot{}, err
		}
		record.candidate = isCandidate
		records[record.id] = record
	}
	unresolved := make([]string, 0)
	for _, candidate := range candidates.Known {
		if _, ok := records[candidate.ExecutionID]; ok {
			continue
		}
		document, err := collection.Doc(candidate.ExecutionID).Get(ctx)
		if status.Code(err) == codes.NotFound {
			unresolved = append(unresolved, candidate.ExecutionID)
			continue
		}
		if err != nil {
			return pipelineEventSnapshot{}, fmt.Errorf("read exact local Pipeline execution: %w", err)
		}
		data := document.Data()
		if data["user_id"] != userID || data["project_id"] != projectID || data["native"] != true || data["task_type"] != "pipeline" {
			unresolved = append(unresolved, candidate.ExecutionID)
			continue
		}
		record, err := h.localPipelineEventRecord(ctx, userID, projectID, candidate.ExecutionID, data, true)
		if err != nil {
			return pipelineEventSnapshot{}, err
		}
		record.candidate = true
		records[record.id] = record
	}
	snapshot := makePipelineEventSnapshot(projectID, records, candidateIDs(candidates.Known), candidatePending(candidates.Pending), false, pipelineEventGlobalLimit, 0, truncated, unresolved, candidates.UnresolvedCount)
	return snapshot, nil
}

func (h *Handler) localPipelineEventRecord(ctx context.Context, userID, projectID, executionID string, data map[string]interface{}, known bool) (pipelineEventRunRecord, error) {
	response, err := h.localPipeline.Status(ctx, userID, projectID, executionID)
	if err != nil {
		return pipelineEventRunRecord{}, fmt.Errorf("read local Pipeline result: %w", err)
	}
	startedAt, _ := firestoreTime(data["started_at"])
	actualStarted, _ := firestoreTime(data["actual_started_at"])
	endedAt, _ := firestoreTime(data["finished_at"])
	statusValue := strings.ToUpper(strings.TrimSpace(stringData(data, "status")))
	if response != nil && response.Status != "" {
		statusValue = strings.ToUpper(response.Status)
	}
	owner := &pipelineExecutionOwner{userID: userID, projectID: projectID}
	publication := publicationOutcomeName(h.cloudRunPublicationOutcome(ctx, owner, executionID))
	quota := stringData(data, "quota_settlement")
	if response != nil && response.QuotaSettlement != "" {
		quota = response.QuotaSettlement
	}
	if response == nil {
		response = &handlerapi.PipelineExecutionResponse{Name: executionID, Status: statusValue}
	}
	if response.CompileMode == "" {
		response.CompileMode = stringData(data, "stage")
	}
	response.PublicationOutcome = publication
	response.QuotaSettlement = quota
	if response.FailureReason == "" {
		response.FailureReason = stringData(data, "failure_reason")
	}
	return pipelineEventRunRecord{
		id: executionID, status: statusValue, startedAt: startedAt,
		actualStarted: actualStarted, endedAt: endedAt, publication: publication,
		quota: quota, response: response, known: known,
	}, nil
}

func (h *Handler) readCloudPipelineEventSnapshot(ctx context.Context, userID, projectID string) (pipelineEventSnapshot, error) {
	if h.firestore == nil || h.firestore.Raw() == nil {
		return pipelineEventSnapshot{}, errors.New("Project Pipeline intent source unavailable")
	}
	candidates, err := scopedfirestore.LoadPipelineEventCandidates(ctx, h.firestore.Raw(), userID, projectID)
	if err != nil {
		return pipelineEventSnapshot{}, fmt.Errorf("read Project Pipeline intent identities: %w", err)
	}
	token, err := h.getMetadataAccessToken(ctx)
	if err != nil {
		return pipelineEventSnapshot{}, fmt.Errorf("read Cloud Run Pipeline history: %w", err)
	}
	listed := make(map[string]cloudRunExecution, pipelineEventGlobalLimit)
	pageToken := ""
	globalCap := false
	historyTruncated := false
	globalScanned := 0
	for page := 0; page < pipelineEventPageLimit; page++ {
		result, err := h.listCloudRunExecutions(ctx, token, pageToken)
		if err != nil {
			return pipelineEventSnapshot{}, fmt.Errorf("read Cloud Run Pipeline history: %w", err)
		}
		remaining := pipelineEventGlobalLimit - globalScanned
		pageExecutions := result.Executions
		if len(pageExecutions) > remaining {
			pageExecutions = pageExecutions[:remaining]
			globalCap = true
		}
		globalScanned += len(pageExecutions)
		for _, execution := range pageExecutions {
			if !cloudRunExecutionOwnedBy(execution, userID, projectID) {
				continue
			}
			id := shortCloudRunExecutionName(execution.Name, true)
			if id != "" {
				listed[id] = execution
			}
		}
		pageToken = result.NextPageToken
		if pageToken == "" {
			break
		}
		if page == pipelineEventPageLimit-1 {
			globalCap = true
		}
	}
	ids := make([]string, 0, len(listed))
	for id := range listed {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, listed[ids[i]].StartTime)
		right, _ := time.Parse(time.RFC3339Nano, listed[ids[j]].StartTime)
		if left.Equal(right) {
			return ids[i] > ids[j]
		}
		return left.After(right)
	})
	historyIDs := ids
	if len(historyIDs) > pipelineEventHistoryLimit {
		historyIDs = historyIDs[:pipelineEventHistoryLimit]
		historyTruncated = true
	}
	needed := make(map[string]struct{}, len(historyIDs)+3)
	historySet := make(map[string]struct{}, len(historyIDs))
	for _, id := range historyIDs {
		needed[id] = struct{}{}
		historySet[id] = struct{}{}
	}
	for _, candidate := range candidates.Known {
		needed[candidate.ExecutionID] = struct{}{}
	}
	records := make(map[string]pipelineEventRunRecord, len(needed))
	unresolved := make([]string, 0)
	owner := &pipelineExecutionOwner{userID: userID, projectID: projectID}
	for id := range needed {
		response, err := h.pipelineExecutionStatusForOwner(ctx, id, userID, projectID)
		if errors.Is(err, errPipelineExecutionNotFound) || errors.Is(err, handlerapi.ErrPipelineExecutionNotFound) || response == nil && err == nil {
			unresolved = append(unresolved, id)
			continue
		}
		if err != nil {
			return pipelineEventSnapshot{}, fmt.Errorf("read exact Cloud Run Pipeline execution: %w", err)
		}
		actualStarted, _ := time.Parse(time.RFC3339Nano, response.StartTime)
		endedAt, _ := time.Parse(time.RFC3339Nano, response.EndTime)
		publication := publicationOutcomeName(h.cloudRunPublicationOutcome(ctx, owner, id))
		response.PublicationOutcome = publication
		records[id] = pipelineEventRunRecord{
			id: id, status: strings.ToUpper(response.Status), startedAt: actualStarted,
			actualStarted: actualStarted, endedAt: endedAt, publication: publication,
			quota: response.QuotaSettlement, response: response,
		}
	}
	for _, candidate := range candidates.Known {
		if record, ok := records[candidate.ExecutionID]; ok {
			record.candidate = true
			if _, inHistory := historySet[candidate.ExecutionID]; !inHistory {
				record.known = true
			}
			records[candidate.ExecutionID] = record
		}
	}
	snapshot := makePipelineEventSnapshot(projectID, records, candidateIDs(candidates.Known), candidatePending(candidates.Pending), globalCap, pipelineEventGlobalLimit, globalScanned, historyTruncated || globalCap, unresolved, candidates.UnresolvedCount)
	return snapshot, nil
}

func makePipelineEventSnapshot(projectID string, records map[string]pipelineEventRunRecord, knownIDs []string, pending []pipelineEventPendingAttempt, partialGlobal bool, globalLimit, globalScanned int, historyTruncated bool, unresolved []string, unresolvedIntents int) pipelineEventSnapshot {
	coverage := pipelineEventCoverage{
		State: "complete", Reasons: []string{}, HistoryTruncated: historyTruncated,
		GlobalScanLimit: globalLimit, GlobalScanned: globalScanned,
		KnownRunIDs: sortedUnique(knownIDs), UnresolvedKnownRunIDs: sortedUnique(unresolved),
		PendingAttempts: pending,
	}
	if partialGlobal {
		coverage.Reasons = append(coverage.Reasons, "global_scan_cap")
	}
	if len(coverage.UnresolvedKnownRunIDs) > 0 {
		coverage.Reasons = append(coverage.Reasons, "unresolved_known_run")
	}
	if unresolvedIntents > 0 {
		coverage.Reasons = append(coverage.Reasons, "unresolved_intent")
	}
	if len(coverage.Reasons) > 0 {
		coverage.State = "partial"
	}

	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := records[ids[i]], records[ids[j]]
		if left.startedAt.Equal(right.startedAt) {
			return ids[i] > ids[j]
		}
		return left.startedAt.After(right.startedAt)
	})
	history := make([]pipelineEventRunRecord, 0, pipelineEventHistoryLimit)
	known := make([]pipelineEventRunRecord, 0, 3)
	for _, id := range ids {
		record := records[id]
		if record.known {
			known = append(known, record)
		} else if len(history) < pipelineEventHistoryLimit {
			history = append(history, record)
		}
	}
	if len(records) > pipelineEventHistoryLimit+len(known) {
		coverage.HistoryTruncated = true
	}
	if len(known) > 3 {
		known = known[:3]
	}

	runs := recordsToEventRuns(history, &coverage)
	knownRuns := recordsToEventRuns(known, &coverage)
	positions := make([]pipelineEventPosition, 0)
	for _, run := range runs {
		for _, event := range run.Events {
			positions = append(positions, event.Position)
		}
	}
	sort.Slice(positions, func(i, j int) bool { return compareEventPosition(positions[i], positions[j]) < 0 })
	var oldest, high *pipelineEventPosition
	if len(positions) > 0 {
		oldest, high = &positions[0], &positions[len(positions)-1]
	}
	snapshot := pipelineEventSnapshot{
		SchemaVersion: 1, ProjectID: projectID, Coverage: coverage,
		OldestPosition: oldest, ObservedHighWatermark: high,
		KnownRuns: knownRuns, Runs: runs,
	}
	for i := range snapshot.Runs {
		sort.Slice(snapshot.Runs[i].Events, func(a, b int) bool {
			return compareEventPosition(snapshot.Runs[i].Events[a].Position, snapshot.Runs[i].Events[b].Position) < 0
		})
	}
	for i := range snapshot.KnownRuns {
		sort.Slice(snapshot.KnownRuns[i].Events, func(a, b int) bool {
			return compareEventPosition(snapshot.KnownRuns[i].Events[a].Position, snapshot.KnownRuns[i].Events[b].Position) < 0
		})
	}
	for i := range snapshot.Runs {
		for j := range snapshot.Runs[i].Events {
			snapshot.Runs[i].Events[j].EventRevision = eventRevision(snapshot.Runs[i].Events[j])
		}
	}
	for i := range snapshot.KnownRuns {
		for j := range snapshot.KnownRuns[i].Events {
			snapshot.KnownRuns[i].Events[j].EventRevision = eventRevision(snapshot.KnownRuns[i].Events[j])
		}
	}
	snapshot.Revision = snapshotRevision(snapshot)
	return snapshot
}

func recordsToEventRuns(records []pipelineEventRunRecord, coverage *pipelineEventCoverage) []pipelineEventRun {
	runs := make([]pipelineEventRun, 0, len(records))
	for _, record := range records {
		run := pipelineEventRun{RunID: record.id, Status: record.status, Events: []pipelineEvent{}}
		if record.actualStarted.IsZero() {
			if isPipelineTerminalStatus(record.status) || record.status == "RUNNING" {
				coverage.UnresolvedKnownRunIDs = append(coverage.UnresolvedKnownRunIDs, record.id)
				coverage.Reasons = append(coverage.Reasons, "unconfirmed_actual_start")
			}
			if record.candidate {
				coverage.PendingAttempts = append(coverage.PendingAttempts, pipelineEventPendingAttempt{
					ExecutionID: record.id, Status: record.status,
				})
			}
			continue
		}
		startAt := record.actualStarted.UTC()
		started := pipelineEvent{
			EventID: record.id + ":started", Position: eventPosition(startAt, record.id, 0),
			Kind: "started", OccurredAt: formatEventTime(startAt),
		}
		run.Events = append(run.Events, started)
		if isPipelineTerminalStatus(record.status) {
			if record.endedAt.IsZero() {
				coverage.UnresolvedKnownRunIDs = append(coverage.UnresolvedKnownRunIDs, record.id)
				coverage.Reasons = append(coverage.Reasons, "terminal_time_unavailable")
				runs = append(runs, run)
				continue
			}
			outcome := pipelineTerminalOutcome(record.status, record.publication)
			event := pipelineEvent{
				EventID: record.id + ":terminal", Position: eventPosition(record.endedAt.UTC(), record.id, 1),
				Kind: "terminal", Outcome: outcome, OccurredAt: formatEventTime(record.endedAt.UTC()),
				Publication: record.publication, QuotaSettlement: record.quota,
				Result: safePipelineEventResult(record),
			}
			if record.response != nil {
				event.FailureReason = record.response.FailureReason
				event.DiagnosticState = record.response.DiagnosticState
			}
			run.Events = append(run.Events, event)
		}
		runs = append(runs, run)
	}
	coverage.KnownRunIDs = sortedUnique(coverage.KnownRunIDs)
	coverage.UnresolvedKnownRunIDs = sortedUnique(coverage.UnresolvedKnownRunIDs)
	coverage.Reasons = sortedUnique(coverage.Reasons)
	if len(coverage.Reasons) > 0 {
		coverage.State = "partial"
	}
	return runs
}

func safePipelineEventResult(record pipelineEventRunRecord) *pipelineEventResult {
	response := record.response
	if response == nil {
		return nil
	}
	result := &pipelineEventResult{
		Name: record.id, Status: record.status, CompileMode: response.CompileMode,
		StartTime: formatEventTime(record.actualStarted), EndTime: formatEventTime(record.endedAt),
		Duration: response.Duration, Diagnostic: response.Diagnostic,
		DiagnosticState: response.DiagnosticState, FailureReason: response.FailureReason,
		QuotaSettlement: record.quota,
	}
	return result
}

func pipelineTerminalOutcome(statusValue, publication string) string {
	if statusValue == "CANCELLED" {
		return "cancelled"
	}
	if publication == "unknown" {
		return "publication_pending"
	}
	if statusValue == "FAILED" {
		return "failed"
	}
	if statusValue == "SUCCEEDED" && publication == "confirmed_published" {
		return "succeeded"
	}
	if statusValue == "UNKNOWN" {
		return "unknown"
	}
	if publication == "confirmed_not_published" {
		return "failed"
	}
	return "unknown"
}

func isPipelineTerminalStatus(statusValue string) bool {
	switch strings.ToUpper(statusValue) {
	case "SUCCEEDED", "FAILED", "CANCELLED", "UNKNOWN":
		return true
	default:
		return false
	}
}

func publicationOutcomeName(outcome pipelinePublicationOutcome) string {
	switch outcome {
	case pipelinePublicationCommitted:
		return "confirmed_published"
	case pipelinePublicationAbsent:
		return "confirmed_not_published"
	default:
		return "unknown"
	}
}

func candidateIDs(candidates []scopedfirestore.PipelineEventCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ExecutionID != "" {
			ids = append(ids, candidate.ExecutionID)
		}
	}
	return sortedUnique(ids)
}

func candidatePending(candidates []scopedfirestore.PipelineEventCandidate) []pipelineEventPendingAttempt {
	result := make([]pipelineEventPendingAttempt, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, pipelineEventPendingAttempt{
			AttemptID: candidate.AttemptID, ReservationID: candidate.ReservationID,
			ExecutionID: candidate.ExecutionID, Trigger: string(candidate.Trigger), IntentStatus: candidate.IntentStatus, Status: "accepted",
		})
	}
	return result
}

func firestoreTime(value interface{}) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), !typed.IsZero()
	case *time.Time:
		if typed != nil {
			return typed.UTC(), !typed.IsZero()
		}
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		return parsed.UTC(), err == nil && !parsed.IsZero()
	}
	return time.Time{}, false
}

func stringData(data map[string]interface{}, key string) string {
	value, _ := data[key].(string)
	return value
}

func eventPosition(at time.Time, runID string, phase int) pipelineEventPosition {
	return pipelineEventPosition{At: formatEventTime(at), RunID: runID, Phase: phase}
}

func formatEventTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func compareEventPosition(left, right pipelineEventPosition) int {
	leftAt, _ := time.Parse(time.RFC3339Nano, left.At)
	rightAt, _ := time.Parse(time.RFC3339Nano, right.At)
	if leftAt.Before(rightAt) {
		return -1
	}
	if leftAt.After(rightAt) {
		return 1
	}
	if left.RunID < right.RunID {
		return -1
	}
	if left.RunID > right.RunID {
		return 1
	}
	if left.Phase < right.Phase {
		return -1
	}
	if left.Phase > right.Phase {
		return 1
	}
	return 0
}

func eventRevision(event pipelineEvent) string {
	event.EventRevision = ""
	data, _ := json.Marshal(event)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func snapshotRevision(snapshot pipelineEventSnapshot) string {
	snapshot.Revision = ""
	data, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if value == "" || len(result) > 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
}
