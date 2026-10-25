package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
)

type listenIDCaptureTransport struct {
	base http.RoundTripper
	ids  chan json.RawMessage
}

type mcpPipelineUpdate struct{ id, uri string }

func (t *listenIDCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request.Header.Set("Authorization", "Bearer fixture-token")
	if request.Body != nil {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		_ = request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(body))
		var message struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if json.Unmarshal(body, &message) == nil && message.Method == "subscriptions/listen" {
			select {
			case t.ids <- append(json.RawMessage(nil), message.ID...):
			default:
			}
		}
	}
	return t.base.RoundTrip(request)
}

func TestPipelineEventPhaseIdentitySurvivesOutcomeAndDiagnosticCorrections(t *testing.T) {
	start := time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC)
	terminal := start.Add(11 * time.Second)
	record := pipelineEventRunRecord{
		id: "run-a", status: "UNKNOWN", actualStarted: start, endedAt: terminal,
		publication: "unknown", quota: "pending",
		response: &handlerapi.PipelineExecutionResponse{Name: "run-a", Status: "UNKNOWN", DiagnosticState: "unavailable"},
	}
	first := makePipelineEventSnapshot("project-a", map[string]pipelineEventRunRecord{record.id: record}, nil, nil, false, 0, 1, false, nil, 0)
	firstTerminal := findPipelineEvent(t, first.Runs, "run-a:terminal")
	if firstTerminal.Outcome != "publication_pending" {
		t.Fatalf("unknown publication outcome=%q", firstTerminal.Outcome)
	}

	record.status = "SUCCEEDED"
	record.publication = "confirmed_published"
	record.response.Status = "SUCCEEDED"
	revised := makePipelineEventSnapshot("project-a", map[string]pipelineEventRunRecord{record.id: record}, nil, nil, false, 0, 1, false, nil, 0)
	revisedTerminal := findPipelineEvent(t, revised.Runs, "run-a:terminal")
	if revisedTerminal.EventID != firstTerminal.EventID || revisedTerminal.OccurredAt != firstTerminal.OccurredAt ||
		revisedTerminal.Position != firstTerminal.Position || revisedTerminal.EventRevision == firstTerminal.EventRevision || revisedTerminal.Outcome != "succeeded" {
		t.Fatalf("outcome correction changed phase identity/time or failed to revise content: before=%+v after=%+v", firstTerminal, revisedTerminal)
	}
	if first.Revision == revised.Revision {
		t.Fatal("top-level snapshot revision did not change with the correction")
	}

	record.response.DiagnosticState = "available"
	record.response.Diagnostic = &handlerapi.PipelineFailureDiagnostic{Status: "observed", Stage: "publish", ErrorClass: "provider", Message: "safe summary"}
	diagnosticRevision := makePipelineEventSnapshot("project-a", map[string]pipelineEventRunRecord{record.id: record}, nil, nil, false, 0, 1, false, nil, 0)
	diagnosticTerminal := findPipelineEvent(t, diagnosticRevision.Runs, "run-a:terminal")
	if diagnosticTerminal.EventID != revisedTerminal.EventID || diagnosticTerminal.OccurredAt != revisedTerminal.OccurredAt ||
		diagnosticTerminal.EventRevision == revisedTerminal.EventRevision {
		t.Fatalf("diagnostic correction did not upsert the existing phase: before=%+v after=%+v", revisedTerminal, diagnosticTerminal)
	}
}

func TestPipelineEventLateOlderPhaseIsRetainedAndPartialEmptyHasNoCursor(t *testing.T) {
	base := time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)
	newRecord := pipelineEventRunRecord{id: "run-new", status: "RUNNING", actualStarted: base.Add(time.Hour)}
	first := makePipelineEventSnapshot("project-a", map[string]pipelineEventRunRecord{"run-new": newRecord}, nil, nil, false, 0, 1, false, nil, 0)
	lateRecord := pipelineEventRunRecord{id: "run-old", status: "RUNNING", actualStarted: base}
	second := makePipelineEventSnapshot("project-a", map[string]pipelineEventRunRecord{"run-new": newRecord, "run-old": lateRecord}, nil, nil, false, 0, 2, false, nil, 0)
	late := findPipelineEvent(t, second.Runs, "run-old:started")
	if compareEventPosition(late.Position, *first.ObservedHighWatermark) >= 0 {
		t.Fatalf("fixture event position %v is not below prior watermark %v", late.Position, *first.ObservedHighWatermark)
	}
	if late.EventID != "run-old:started" {
		t.Fatalf("late older phase identity=%q", late.EventID)
	}

	empty := makePipelineEventSnapshot("project-empty", map[string]pipelineEventRunRecord{}, nil, nil, true, pipelineEventGlobalLimit, pipelineEventGlobalLimit, true, nil, 0)
	if empty.Coverage.State != "partial" || !empty.Coverage.HistoryTruncated || empty.OldestPosition != nil || empty.ObservedHighWatermark != nil || len(empty.Runs) != 0 {
		t.Fatalf("capped empty read falsely resembles complete/no-run: %+v", empty)
	}
}

func TestPipelineEventExactOverlayDoesNotMoveHistoryWatermark(t *testing.T) {
	base := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	records := make(map[string]pipelineEventRunRecord)
	for i := 0; i < 51; i++ {
		id := "history-" + string(rune('a'+i))
		records[id] = pipelineEventRunRecord{id: id, status: "RUNNING", actualStarted: base.Add(time.Duration(i+1) * time.Minute)}
	}
	knownID := "known-active"
	knownStart := base.Add(-24 * time.Hour)
	records[knownID] = pipelineEventRunRecord{id: knownID, status: "RUNNING", actualStarted: knownStart, known: true, candidate: true}
	snapshot := makePipelineEventSnapshot("project-a", records, []string{knownID}, nil, false, pipelineEventGlobalLimit, 51, false, nil, 0)
	if len(snapshot.Runs) != 50 || len(snapshot.KnownRuns) != 1 || snapshot.KnownRuns[0].RunID != knownID || !snapshot.Coverage.HistoryTruncated {
		t.Fatalf("bounded history/overlay counts: runs=%d known=%+v coverage=%+v", len(snapshot.Runs), snapshot.KnownRuns, snapshot.Coverage)
	}
	if snapshot.OldestPosition == nil || snapshot.OldestPosition.RunID == knownID || snapshot.OldestPosition.At == formatEventTime(knownStart) {
		t.Fatalf("exact overlay widened the history cursor window: oldest=%+v", snapshot.OldestPosition)
	}
}

func TestPipelineEventPublicationOutcomesRemainTruthful(t *testing.T) {
	for _, tc := range []struct{ status, publication, want string }{
		{"FAILED", "confirmed_not_published", "failed"},
		{"FAILED", "confirmed_published", "failed"},
		{"CANCELLED", "unknown", "cancelled"},
		{"SUCCEEDED", "unknown", "publication_pending"},
		{"UNKNOWN", "confirmed_published", "unknown"},
		{"SUCCEEDED", "confirmed_published", "succeeded"},
	} {
		if got := pipelineTerminalOutcome(tc.status, tc.publication); got != tc.want {
			t.Errorf("pipelineTerminalOutcome(%q,%q)=%q, want %q", tc.status, tc.publication, got, tc.want)
		}
	}
}

func TestPipelineEventQueuedAndIncompleteTerminalNeverInventPhases(t *testing.T) {
	started := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	queued := makePipelineEventSnapshot("project-a", map[string]pipelineEventRunRecord{
		"run-queued": {id: "run-queued", status: "RUNNING", candidate: true},
	}, []string{"run-queued"}, nil, false, 0, 1, false, nil, 0)
	if len(queued.Runs) != 0 || len(queued.Coverage.PendingAttempts) != 1 || queued.Coverage.PendingAttempts[0].ExecutionID != "run-queued" ||
		len(queued.Coverage.UnresolvedKnownRunIDs) != 1 {
		t.Fatalf("queued record fabricated lifecycle phases or disappeared: %+v", queued)
	}

	incomplete := makePipelineEventSnapshot("project-a", map[string]pipelineEventRunRecord{
		"run-incomplete": {id: "run-incomplete", status: "FAILED", actualStarted: started, publication: "confirmed_not_published"},
	}, nil, nil, false, 0, 1, false, nil, 0)
	if incomplete.Coverage.State != "partial" || len(incomplete.Runs) != 1 || len(incomplete.Runs[0].Events) != 1 || incomplete.Runs[0].Events[0].Kind != "started" {
		t.Fatalf("terminal record without durable time invented a terminal phase: %+v", incomplete)
	}
}

func TestMCPPipelineEventReadRequiresExactProjectURIAndPropagatesSourceErrors(t *testing.T) {
	h := &Handler{pipelineEventSource: func(context.Context, string, string) (pipelineEventSnapshot, error) {
		return pipelineEventSnapshot{}, errors.New("source offline")
	}}
	ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
		userID: "user-a", projectID: "project-a", capabilities: []string{mcpQueryCapability},
	})
	if _, err := h.readMCPPipelineEvents(ctx, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: pipelineEventsURI("project-b")}}); err == nil {
		t.Fatal("foreign Project URI was accepted")
	}
	if _, err := h.readMCPPipelineEvents(ctx, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: pipelineEventsURI("project-a")}}); err == nil || !strings.Contains(err.Error(), "source offline") {
		t.Fatalf("source failure returned success/empty resource: %v", err)
	}

	control := newMCPListenControl((&Handler{}).eventTiming())
	listenCtx, cancel := control.activate(context.Background())
	defer cancel()
	control.fail(errors.New("simulated writer failure"))
	select {
	case <-listenCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream failure did not cancel the request-scoped listener")
	}
}

func TestMCPObservationMiddlewarePreservesTypedNilResourceErrors(t *testing.T) {
	wantErr := errors.New("resource denied")
	next := mcpRequestObservationMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return (*mcp.ReadResourceResult)(nil), wantErr
	})
	result, err := next(context.Background(), "resources/read", &mcp.ReadResourceRequest{})
	if !nilMCPResult(result) || !errors.Is(err, wantErr) {
		t.Fatalf("typed nil resource error result=%#v err=%v", result, err)
	}
}

func TestMCPListenReadVerifierAndCleanupBounds(t *testing.T) {
	timing := pipelineEventTiming{
		readTimeout: 25 * time.Millisecond, pollInterval: time.Millisecond,
		authInterval: 10 * time.Millisecond, authTimeout: 20 * time.Millisecond,
		authMaxAge: 50 * time.Millisecond, writeTimeout: 20 * time.Millisecond,
		listenMaxAge: 80 * time.Millisecond, backoffs: []time.Duration{5 * time.Millisecond, 10 * time.Millisecond},
	}
	t.Run("source deadline", func(t *testing.T) {
		sourceReturned := make(chan error, 1)
		h := &Handler{pipelineEventTiming: &timing, pipelineEventSource: func(ctx context.Context, _, _ string) (pipelineEventSnapshot, error) {
			<-ctx.Done()
			sourceReturned <- ctx.Err()
			return pipelineEventSnapshot{}, ctx.Err()
		}}
		ctx := context.WithValue(context.Background(), mcpProjectKeyIdentityContextKey{}, mcpProjectKeyIdentity{
			userID: "user-a", projectID: "project-a", capabilities: []string{mcpQueryCapability},
		})
		_, err := h.readMCPPipelineEvents(ctx, &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: pipelineEventsURI("project-a")}})
		if err == nil {
			t.Fatal("hanging source returned a resource")
		}
		if err := <-sourceReturned; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("source cancellation error=%v, want deadline exceeded", err)
		}
	})

	t.Run("third source failure closes listener", func(t *testing.T) {
		var calls atomic.Int32
		h := &Handler{pipelineEventTiming: &timing, pipelineEventSource: func(context.Context, string, string) (pipelineEventSnapshot, error) {
			calls.Add(1)
			return pipelineEventSnapshot{}, errors.New("source unavailable")
		}}
		control := newMCPListenControl(timing)
		listenCtx, cancel := control.activate(context.Background())
		defer cancel()
		control.ackOnce.Do(func() { close(control.ack) })
		identity := mcpProjectKeyIdentity{userID: "user-a", projectID: "project-a", capabilities: []string{mcpQueryCapability}, revalidate: func(context.Context) error { return nil }}
		server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		go h.watchMCPPipelineEvents(listenCtx, identity, pipelineEventsURI("project-a"), server, control)
		select {
		case <-listenCtx.Done():
		case <-time.After(time.Second):
			t.Fatal("three bounded source failures did not close the listener")
		}
		if calls.Load() != 3 {
			t.Fatalf("source calls=%d, want exactly 3 before closure", calls.Load())
		}
	})

	t.Run("verifier deadline and revocation", func(t *testing.T) {
		identity := mcpProjectKeyIdentity{revalidate: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
		if err := revalidateMCPListen(context.Background(), identity, timing.authTimeout); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("hanging verifier error=%v, want deadline exceeded", err)
		}
		control := newMCPListenControl(timing)
		listenCtx, cancel := control.activate(context.Background())
		defer cancel()
		var calls atomic.Int32
		identity.revalidate = func(context.Context) error {
			calls.Add(1)
			return errors.New("authority revoked")
		}
		h := &Handler{pipelineEventTiming: &timing}
		go h.revalidateMCPListenLoop(listenCtx, identity, control)
		select {
		case <-listenCtx.Done():
		case <-time.After(time.Second):
			t.Fatal("revoked authority did not close the listener")
		}
		if calls.Load() != 1 {
			t.Fatalf("periodic verifier calls=%d, want one fail-closed check", calls.Load())
		}
	})

	t.Run("listener maximum age", func(t *testing.T) {
		control := newMCPListenControl(timing)
		listenCtx, cancel := control.activate(context.Background())
		defer cancel()
		select {
		case <-listenCtx.Done():
		case <-time.After(time.Second):
			t.Fatal("listener exceeded configured maximum age")
		}
	})
}

type controlledMCPResponseWriter struct {
	header      http.Header
	writeErr    error
	flushErr    error
	deadlineErr error
}

func (w *controlledMCPResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (*controlledMCPResponseWriter) WriteHeader(int) {}
func (w *controlledMCPResponseWriter) Write(data []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(data), nil
}
func (w *controlledMCPResponseWriter) FlushError() error { return w.flushErr }
func (w *controlledMCPResponseWriter) Flush()            { _ = w.FlushError() }
func (w *controlledMCPResponseWriter) SetWriteDeadline(time.Time) error {
	return w.deadlineErr
}

func TestMCPListenWriterCancelsOnWriteFlushAndDeadlineFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*controlledMCPResponseWriter)
		call  func(*mcpResponseObservationWriter) error
	}{
		{name: "write", setup: func(w *controlledMCPResponseWriter) { w.writeErr = errors.New("write failed") }, call: func(w *mcpResponseObservationWriter) error { _, err := w.Write([]byte("frame")); return err }},
		{name: "flush", setup: func(w *controlledMCPResponseWriter) { w.flushErr = errors.New("flush failed") }, call: func(w *mcpResponseObservationWriter) error { _, _ = w.Write([]byte("frame")); return w.FlushError() }},
		{name: "deadline", setup: func(w *controlledMCPResponseWriter) { w.deadlineErr = errors.New("deadline unsupported") }, call: func(w *mcpResponseObservationWriter) error { _, err := w.Write([]byte("frame")); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			underlying := &controlledMCPResponseWriter{}
			tc.setup(underlying)
			control := newMCPListenControl((&Handler{}).eventTiming())
			ctx, cancel := control.activate(context.Background())
			defer cancel()
			writer := &mcpResponseObservationWriter{ResponseWriter: underlying, listen: control}
			if err := tc.call(writer); err == nil {
				t.Fatal("controlled stream failure was not returned")
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("stream failure did not cancel the request-scoped listener")
			}
		})
	}
}

func TestMCPPipelineEventsRealTCPListenAckFreshReadAndCancellation(t *testing.T) {
	h := &Handler{pipelineEventTiming: &pipelineEventTiming{
		readTimeout: 500 * time.Millisecond, pollInterval: 20 * time.Millisecond,
		authInterval: 100 * time.Millisecond, authTimeout: 20 * time.Millisecond,
		authMaxAge: 200 * time.Millisecond, writeTimeout: time.Second,
		listenMaxAge: 3 * time.Second, backoffs: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond},
	}}
	var calls atomic.Int32
	var blockNextSource atomic.Bool
	changeOnce := make(chan struct{})
	blockedSource := make(chan struct{})
	blockReturned := make(chan struct{})
	h.pipelineEventSource = func(ctx context.Context, userID, projectID string) (pipelineEventSnapshot, error) {
		calls.Add(1)
		if blockNextSource.CompareAndSwap(true, false) {
			close(blockedSource)
			select {
			case <-ctx.Done():
				close(blockReturned)
				return pipelineEventSnapshot{}, ctx.Err()
			case <-time.After(10 * time.Second):
				return pipelineEventSnapshot{}, errors.New("test source unexpectedly outlived request")
			}
		}
		select {
		case <-changeOnce:
			return testEventSnapshot(userID, projectID, "changed")
		default:
			return testEventSnapshot(userID, projectID, "initial")
		}
	}
	identity := mcpProjectKeyIdentity{
		userID: "user-a", projectID: "project-a", keyID: "key-a", capabilities: []string{mcpQueryCapability},
		revalidate: func(context.Context) error { return nil },
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.serveAuthenticatedMCP(h.mcpSDKHandler(identity), identity, w, r)
	}))
	t.Cleanup(server.Close)
	listenIDs := make(chan json.RawMessage, 1)
	transport := &listenIDCaptureTransport{base: http.DefaultTransport, ids: listenIDs}
	acks := make(chan any, 1)
	updates := make(chan mcpPipelineUpdate, 1)
	client := mcp.NewClient(&mcp.Implementation{Name: "lwc-428-test-client", Version: "1"}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, request *mcp.ResourceUpdatedNotificationRequest) {
			if request != nil && request.Params != nil {
				updates <- mcpPipelineUpdate{id: fmt.Sprint(request.Params.Meta[mcp.MetaKeySubscriptionID]), uri: request.Params.URI}
			}
		},
	})
	client.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "notifications/subscriptions/acknowledged" {
				if ack, ok := request.(*mcp.ClientRequest[*mcp.SubscriptionsAcknowledgedParams]); ok && ack.Params != nil {
					acks <- ack.Params.Meta[mcp.MetaKeySubscriptionID]
				}
			}
			return next(ctx, method, request)
		}
	})
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 5*time.Second)
	session, err := client.Connect(connectCtx, &mcp.StreamableClientTransport{
		Endpoint: server.URL, HTTPClient: &http.Client{Transport: transport}, DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	cancelConnect()
	if err != nil {
		t.Fatalf("connect real TCP MCP client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	uri := pipelineEventsURI("project-a")
	initial, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil || len(initial.Contents) != 1 || !strings.Contains(initial.Contents[0].Text, `"project_id":"project-a"`) {
		t.Fatalf("initial resources/read=%+v err=%v", initial, err)
	}
	subscribeDone := make(chan error, 1)
	go func() { subscribeDone <- session.Subscribe(context.Background(), &mcp.SubscribeParams{URI: uri}) }()
	ack, err := waitPipelineValue("subscriptions/listen acknowledgement", acks, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	wireID, err := waitPipelineValue("captured subscriptions/listen request ID", listenIDs, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ackJSON, err := json.Marshal(ack)
	if err != nil || !bytes.Equal(bytes.TrimSpace(wireID), ackJSON) {
		t.Fatalf("ack ID=%s actual listen request ID=%s err=%v", ackJSON, wireID, err)
	}
	postAckRead, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil || len(postAckRead.Contents) != 1 {
		t.Fatalf("fresh resource read after ack=%+v err=%v", postAckRead, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() < 3 {
		t.Fatalf("listener did not complete its initial durable read; source calls=%d", calls.Load())
	}
	close(changeOnce)
	update, err := waitPipelineValue("resource update", updates, 12*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if update.id != fmt.Sprint(ack) || update.uri != uri {
		t.Fatalf("update ID/URI=%s/%s, want %v/%s", update.id, update.uri, ack, uri)
	}
	postUpdateRead, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil || len(postUpdateRead.Contents) != 1 || !strings.Contains(postUpdateRead.Contents[0].Text, `"revision":"changed"`) {
		t.Fatalf("fresh resource read after notification=%+v err=%v", postUpdateRead, err)
	}
	blockNextSource.Store(true)
	select {
	case <-blockedSource:
	case <-time.After(12 * time.Second):
		t.Fatal("watcher did not start its next bounded source read")
	}
	if err := session.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	select {
	case <-blockReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("request cancellation did not reach the context-aware source")
	}
	select {
	case <-subscribeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("subscriptions/listen client call did not unwind after unsubscribe")
	}
}

func testEventSnapshot(userID, projectID, revision string) (pipelineEventSnapshot, error) {
	return pipelineEventSnapshot{
		SchemaVersion: 1, ProjectID: projectID, Revision: revision,
		Coverage:  pipelineEventCoverage{State: "complete", Reasons: []string{}, KnownRunIDs: []string{}, UnresolvedKnownRunIDs: []string{}, PendingAttempts: []pipelineEventPendingAttempt{}},
		KnownRuns: []pipelineEventRun{}, Runs: []pipelineEventRun{},
	}, nil
}

func waitPipelineValue[T any](label string, values <-chan T, timeout time.Duration) (T, error) {
	var zero T
	select {
	case value := <-values:
		return value, nil
	case <-time.After(timeout):
		return zero, fmt.Errorf("timed out waiting for %s", label)
	}
}

func findPipelineEvent(t *testing.T, runs []pipelineEventRun, id string) pipelineEvent {
	t.Helper()
	for _, run := range runs {
		for _, event := range run.Events {
			if event.EventID == id {
				return event
			}
		}
	}
	t.Fatalf("event %q not found in %+v", id, runs)
	return pipelineEvent{}
}
