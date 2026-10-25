package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
)

type listenIDCaptureTransport struct {
	base               http.RoundTripper
	ids                chan json.RawMessage
	duplicateURI       bool
	pauseAfterFirstSSE bool
	resumeResponse     <-chan struct{}
	responseReadPaused chan<- struct{}
}

type mcpPipelineUpdate struct{ id, uri string }

type pauseAfterFirstSSEBody struct {
	io.ReadCloser
	resume    <-chan struct{}
	paused    chan<- struct{}
	closed    chan struct{}
	closeOnce sync.Once
	pauseOnce sync.Once
	first     []byte
	framed    bool
}

func (b *pauseAfterFirstSSEBody) Read(p []byte) (int, error) {
	if !b.framed {
		for !bytes.Contains(b.first, []byte("\n\n")) {
			var next [1]byte
			n, err := b.ReadCloser.Read(next[:])
			if n > 0 {
				b.first = append(b.first, next[0])
			}
			if err != nil {
				return 0, err
			}
		}
		b.framed = true
	}
	if len(b.first) > 0 {
		n := copy(p, b.first)
		b.first = b.first[n:]
		return n, nil
	}
	b.pauseOnce.Do(func() { close(b.paused) })
	select {
	case <-b.resume:
	case <-b.closed:
		return 0, io.ErrClosedPipe
	}
	return b.ReadCloser.Read(p)
}

func (b *pauseAfterFirstSSEBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return b.ReadCloser.Close()
}

type observedTCPWrite struct {
	started  time.Time
	duration time.Duration
	deadline time.Time
	bytes    int
	err      error
}

type observingTCPListener struct {
	net.Listener
	writeEvents chan observedTCPWrite
	totalBytes  atomic.Int64
	deadlines   atomic.Int32
}

func (l *observingTCPListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("accepted loopback connection has type %T, want *net.TCPConn", conn)
	}
	if err := tcp.SetWriteBuffer(1024); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set loopback server send buffer: %w", err)
	}
	return &observingTCPConn{Conn: conn, listener: l}, nil
}

type observingTCPConn struct {
	net.Conn
	listener *observingTCPListener
	deadline atomic.Int64
}

func (c *observingTCPConn) SetWriteDeadline(deadline time.Time) error {
	c.listener.deadlines.Add(1)
	if deadline.IsZero() {
		c.deadline.Store(0)
	} else {
		c.deadline.Store(deadline.UnixNano())
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *observingTCPConn) Write(p []byte) (int, error) {
	started := time.Now()
	n, err := c.Conn.Write(p)
	duration := time.Since(started)
	c.listener.totalBytes.Add(int64(n))
	if err != nil || duration >= 25*time.Millisecond {
		deadline := time.Time{}
		if deadlineNanos := c.deadline.Load(); deadlineNanos != 0 {
			deadline = time.Unix(0, deadlineNanos)
		}
		select {
		case c.listener.writeEvents <- observedTCPWrite{started: started, duration: duration, deadline: deadline, bytes: n, err: err}:
		default:
		}
	}
	return n, err
}

func (t *listenIDCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request.Header.Set("Authorization", "Bearer fixture-token")
	isListen := false
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
			Params struct {
				Notifications struct {
					ResourceSubscriptions []string `json:"resourceSubscriptions"`
				} `json:"notifications"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &message) == nil && message.Method == "subscriptions/listen" {
			isListen = true
			select {
			case t.ids <- append(json.RawMessage(nil), message.ID...):
			default:
			}
			if t.duplicateURI && len(message.Params.Notifications.ResourceSubscriptions) > 0 {
				var envelope map[string]any
				if json.Unmarshal(body, &envelope) == nil {
					params := envelope["params"].(map[string]any)
					notifications := params["notifications"].(map[string]any)
					resources := notifications["resourceSubscriptions"].([]any)
					notifications["resourceSubscriptions"] = append(resources, resources[0])
					if modified, err := json.Marshal(envelope); err == nil {
						body = modified
					}
				}
			}
		}
		request.ContentLength = int64(len(body))
		request.Body = io.NopCloser(bytes.NewReader(body))
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if isListen && t.pauseAfterFirstSSE {
		response.Body = &pauseAfterFirstSSEBody{
			ReadCloser: response.Body, resume: t.resumeResponse,
			paused: t.responseReadPaused, closed: make(chan struct{}),
		}
	}
	return response, nil
}

func TestPipelineEventPhaseIdentitySurvivesOutcomeAndDiagnosticCorrections(t *testing.T) {
	start := time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC)
	terminal := start.Add(11 * time.Second)
	record := pipelineEventRunRecord{
		id: "run-a", status: "FAILED", actualStarted: start, endedAt: terminal,
		publication: "unknown", quota: "pending",
		response: &handlerapi.PipelineExecutionResponse{Name: "run-a", Status: "FAILED", DiagnosticState: "unavailable"},
	}
	first := makePipelineEventSnapshot("project-a", map[string]pipelineEventRunRecord{record.id: record}, nil, nil, false, 0, 1, false, nil, 0)
	firstTerminal := findPipelineEvent(t, first.Runs, "run-a:terminal")
	if firstTerminal.Outcome != "publication_pending" {
		t.Fatalf("unknown publication outcome=%q", firstTerminal.Outcome)
	}

	record.publication = "confirmed_published"
	revised := makePipelineEventSnapshot("project-a", map[string]pipelineEventRunRecord{record.id: record}, nil, nil, false, 0, 1, false, nil, 0)
	revisedTerminal := findPipelineEvent(t, revised.Runs, "run-a:terminal")
	if revisedTerminal.EventID != firstTerminal.EventID || revisedTerminal.OccurredAt != firstTerminal.OccurredAt ||
		revisedTerminal.Position != firstTerminal.Position || revisedTerminal.EventRevision == firstTerminal.EventRevision || revisedTerminal.Outcome != "succeeded" {
		t.Fatalf("outcome correction changed phase identity/time or failed to revise content: before=%+v after=%+v", firstTerminal, revisedTerminal)
	}
	if first.Revision == revised.Revision {
		t.Fatal("top-level snapshot revision did not change with the correction")
	}
	if revisedTerminal.Result == nil || revisedTerminal.Result.Status != "FAILED" {
		t.Fatalf("publication outcome hid provider failure evidence: %+v", revisedTerminal.Result)
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
		{"FAILED", "confirmed_published", "succeeded"},
		{"FAILED", "unknown", "publication_pending"},
		{"CANCELLED", "unknown", "cancelled"},
		{"CANCELLED", "confirmed_published", "cancelled"},
		{"CANCELLED", "confirmed_not_published", "cancelled"},
		{"SUCCEEDED", "unknown", "publication_pending"},
		{"SUCCEEDED", "confirmed_not_published", "unknown"},
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

type ginFlushErrorWriter struct {
	header        http.Header
	flushErr      error
	flushCalls    atomic.Int32
	deadlineCalls atomic.Int32
	status        int
	wrote         bool
	body          bytes.Buffer
}

func (w *ginFlushErrorWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *ginFlushErrorWriter) WriteHeader(status int) { w.status = status }
func (w *ginFlushErrorWriter) Write(data []byte) (int, error) {
	w.wrote = true
	_, _ = w.body.Write(data)
	return len(data), nil
}
func (w *ginFlushErrorWriter) Flush() { w.flushCalls.Add(1) }
func (w *ginFlushErrorWriter) FlushError() error {
	w.flushCalls.Add(1)
	return w.flushErr
}
func (w *ginFlushErrorWriter) SetWriteDeadline(time.Time) error {
	w.deadlineCalls.Add(1)
	return nil
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

func TestMCPListenGinWriterPropagatesUnderlyingFlushError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var sourceCalls atomic.Int32
	h := &Handler{pipelineEventSource: func(context.Context, string, string) (pipelineEventSnapshot, error) {
		sourceCalls.Add(1)
		return pipelineEventSnapshot{}, nil
	}}
	var verifierCalls atomic.Int32
	identity := mcpProjectKeyIdentity{
		userID: "user-a", projectID: "project-a", keyID: "key-a", capabilities: []string{mcpQueryCapability},
		revalidate: func(context.Context) error { verifierCalls.Add(1); return nil },
	}
	listenHandlerReturned := make(chan struct{})
	router := gin.New()
	router.POST("/mcp", func(c *gin.Context) {
		c.Header("X-MCP-Writer-Test", "preserved")
		h.serveAuthenticatedMCP(h.mcpSDKHandler(identity), identity, c.Writer, c.Request)
		if c.GetHeader("MCP-Method") == "subscriptions/listen" {
			close(listenHandlerReturned)
		}
	})
	underlying := &ginFlushErrorWriter{flushErr: errors.New("injected underlying flush failure")}
	uri := pipelineEventsURI(identity.projectID)
	body := `{"jsonrpc":"2.0","id":27,"method":"subscriptions/listen","params":{"notifications":{"resourceSubscriptions":["` + uri + `"]},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"gin-flush-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Authorization", "Bearer key-a")
	request.Header.Set("MCP-Method", "subscriptions/listen")
	request.Header.Set("MCP-Name", uri)
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(underlying, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Gin-routed listener did not return after underlying FlushError")
	}
	if !underlying.wrote || underlying.flushCalls.Load() == 0 || underlying.deadlineCalls.Load() == 0 || underlying.status != http.StatusOK || underlying.Header().Get("X-MCP-Writer-Test") != "preserved" {
		t.Fatalf("Gin writer path did not exercise Write/deadline/FlushError: status=%d wrote=%t flush=%d deadline=%d body=%s", underlying.status, underlying.wrote, underlying.flushCalls.Load(), underlying.deadlineCalls.Load(), underlying.body.String())
	}
	if verifierCalls.Load() != 0 || sourceCalls.Load() != 0 {
		t.Fatalf("failed acknowledgement continued listener work: verifier calls=%d source calls=%d", verifierCalls.Load(), sourceCalls.Load())
	}
}

func TestMCPPipelineEventsRealTCPListenAckFreshReadAndCancellation(t *testing.T) {
	h := &Handler{pipelineEventTiming: &pipelineEventTiming{
		readTimeout: 500 * time.Millisecond, pollInterval: 20 * time.Millisecond,
		authInterval: time.Second, authTimeout: 20 * time.Millisecond,
		authMaxAge: 3 * time.Second, writeTimeout: time.Second,
		listenMaxAge: 8 * time.Second, backoffs: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond},
	}}
	var calls atomic.Int32
	var verifierCalls atomic.Int32
	var watcherActive atomic.Int32
	var watcherMaxActive atomic.Int32
	var blockFirstWatcherRead atomic.Bool
	var blockNextSource atomic.Bool
	initialWatcherRead := make(chan struct{})
	releaseInitialWatcherRead := make(chan struct{})
	blockedSource := make(chan struct{})
	blockReturned := make(chan struct{})
	var currentRevision atomic.Value
	currentRevision.Store("initial")
	h.pipelineEventSource = func(ctx context.Context, userID, projectID string) (pipelineEventSnapshot, error) {
		calls.Add(1)
		if watching, _ := ctx.Value(pipelineEventWatcherReadContextKey{}).(bool); watching {
			active := watcherActive.Add(1)
			defer watcherActive.Add(-1)
			for maximum := watcherMaxActive.Load(); active > maximum; maximum = watcherMaxActive.Load() {
				if watcherMaxActive.CompareAndSwap(maximum, active) {
					break
				}
			}
		}
		if blockFirstWatcherRead.CompareAndSwap(true, false) {
			close(initialWatcherRead)
			select {
			case <-ctx.Done():
				return pipelineEventSnapshot{}, ctx.Err()
			case <-releaseInitialWatcherRead:
			}
		}
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
		return testEventSnapshot(userID, projectID, currentRevision.Load().(string))
	}
	identity := mcpProjectKeyIdentity{
		userID: "user-a", projectID: "project-a", keyID: "key-a", capabilities: []string{mcpQueryCapability},
		revalidate: func(context.Context) error { verifierCalls.Add(1); return nil },
	}
	router := gin.New()
	router.POST("/mcp", func(c *gin.Context) {
		h.serveAuthenticatedMCP(h.mcpSDKHandler(identity), identity, c.Writer, c.Request)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	listenIDs := make(chan json.RawMessage, 1)
	transport := &listenIDCaptureTransport{base: http.DefaultTransport, ids: listenIDs, duplicateURI: true}
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
		Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: transport}, DisableStandaloneSSE: true,
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
	blockFirstWatcherRead.Store(true)
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
	select {
	case <-initialWatcherRead:
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not enter its deliberately delayed first source read")
	}
	time.Sleep(100 * time.Millisecond)
	if watcherMaxActive.Load() != 1 || verifierCalls.Load() != 1 {
		t.Fatalf("duplicate URI created parallel listener work: max source concurrency=%d initial verifier calls=%d", watcherMaxActive.Load(), verifierCalls.Load())
	}
	postAckRead, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil || len(postAckRead.Contents) != 1 {
		t.Fatalf("fresh resource read after ack=%+v err=%v", postAckRead, err)
	}
	currentRevision.Store("after-ack")
	close(releaseInitialWatcherRead)
	update, err := waitPipelineValue("resource update", updates, 12*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if update.id != fmt.Sprint(ack) || update.uri != uri {
		t.Fatalf("update ID/URI=%s/%s, want %v/%s", update.id, update.uri, ack, uri)
	}
	postUpdateRead, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil || len(postUpdateRead.Contents) != 1 || !strings.Contains(postUpdateRead.Contents[0].Text, `"revision":"after-ack"`) {
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

func TestMCPPipelineListenStoppedReaderTimesOutOnTCPBackpressure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	timing := pipelineEventTiming{
		readTimeout: 250 * time.Millisecond, pollInterval: 0,
		authInterval: 20 * time.Millisecond, authTimeout: 50 * time.Millisecond,
		authMaxAge: 10 * time.Second, writeTimeout: 100 * time.Millisecond,
		listenMaxAge: 60 * time.Second, backoffs: []time.Duration{5 * time.Millisecond, 10 * time.Millisecond},
	}
	var sourceCalls, verifierCalls atomic.Int32
	h := &Handler{pipelineEventTiming: &timing}
	h.pipelineEventSource = func(_ context.Context, userID, projectID string) (pipelineEventSnapshot, error) {
		revision := sourceCalls.Add(1)
		return testEventSnapshot(userID, projectID, fmt.Sprintf("revision-%d", revision))
	}
	identity := mcpProjectKeyIdentity{
		userID: "user-a", projectID: "project-a", keyID: "key-stopped-reader", capabilities: []string{mcpQueryCapability},
		revalidate: func(context.Context) error { verifierCalls.Add(1); return nil },
	}
	listenerSocket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	listener := &observingTCPListener{Listener: listenerSocket, writeEvents: make(chan observedTCPWrite, 128)}
	listenHandlerReturned := make(chan struct{})
	router := gin.New()
	router.POST("/mcp", func(c *gin.Context) {
		isListen := c.GetHeader("MCP-Method") == "subscriptions/listen"
		h.serveAuthenticatedMCP(h.mcpSDKHandler(identity), identity, c.Writer, c.Request)
		if isListen {
			close(listenHandlerReturned)
		}
	})
	server := &http.Server{Handler: router}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-serveDone
	})
	resumeResponse := make(chan struct{})
	var resumeResponseOnce sync.Once
	releaseClientReader := func() { resumeResponseOnce.Do(func() { close(resumeResponse) }) }
	responseReadPaused := make(chan struct{})
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err == nil {
			if tcp, ok := conn.(*net.TCPConn); ok {
				if err := tcp.SetReadBuffer(1024); err != nil {
					_ = conn.Close()
					return nil, fmt.Errorf("set loopback client receive buffer: %w", err)
				}
			}
		}
		return conn, err
	}}
	defer transport.CloseIdleConnections()
	authenticatedTransport := &listenIDCaptureTransport{
		base: transport, ids: make(chan json.RawMessage, 1), pauseAfterFirstSSE: true,
		resumeResponse: resumeResponse, responseReadPaused: responseReadPaused,
	}
	acks := make(chan any, 1)
	client := mcp.NewClient(&mcp.Implementation{Name: "lwc-428-stopped-reader", Version: "1"}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(context.Context, *mcp.ResourceUpdatedNotificationRequest) {},
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
	endpoint := "http://" + listener.Addr().String() + "/mcp"
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 5*time.Second)
	session, err := client.Connect(connectCtx, &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: &http.Client{Transport: authenticatedTransport}, DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	cancelConnect()
	if err != nil {
		t.Fatalf("connect stopped-reader MCP client: %v", err)
	}
	t.Cleanup(func() {
		releaseClientReader()
		_ = session.Close()
	})
	uri := pipelineEventsURI(identity.projectID)
	if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err != nil {
		t.Fatalf("initial resource read: %v", err)
	}
	subscribeCtx, cancelSubscribe := context.WithCancel(context.Background())
	defer cancelSubscribe()
	subscribeDone := make(chan error, 1)
	go func() { subscribeDone <- session.Subscribe(subscribeCtx, &mcp.SubscribeParams{URI: uri}) }()
	if _, err := waitPipelineValue("stopped-reader listen acknowledgement", acks, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := waitPipelineValue("client reader pause after acknowledgement event", responseReadPaused, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	var blockedWrite *observedTCPWrite
	writeDeadline := time.After(8 * time.Second)
	for blockedWrite == nil {
		select {
		case write := <-listener.writeEvents:
			if write.err == nil {
				continue
			}
			var networkErr net.Error
			if !errors.As(write.err, &networkErr) || !networkErr.Timeout() {
				t.Fatalf("blocked stream write failed without a network timeout: duration=%s deadline=%s bytes=%d err=%v", write.duration, write.deadline, write.bytes, write.err)
			}
			blockedWrite = &write
		case <-writeDeadline:
			t.Fatalf("stopped TCP reader did not cause a write timeout; source calls=%d verifier calls=%d total server bytes=%d deadlines=%d", sourceCalls.Load(), verifierCalls.Load(), listener.totalBytes.Load(), listener.deadlines.Load())
		}
	}
	deadlineRemaining := blockedWrite.deadline.Sub(blockedWrite.started)
	if blockedWrite.deadline.IsZero() || deadlineRemaining <= 0 || deadlineRemaining > 2*timing.writeTimeout ||
		blockedWrite.duration < timing.writeTimeout/2 || blockedWrite.duration > timing.writeTimeout+time.Second {
		t.Fatalf("TCP timeout did not follow the forward product deadline: write=%+v remaining=%s configured=%s", *blockedWrite, deadlineRemaining, timing.writeTimeout)
	}
	if listener.totalBytes.Load() < 4<<10 || listener.deadlines.Load() == 0 || sourceCalls.Load() < 2 || verifierCalls.Load() == 0 {
		t.Fatalf("no sustained post-ack socket backpressure observed: server bytes=%d deadline updates=%d source calls=%d verifier calls=%d timeout write=%+v", listener.totalBytes.Load(), listener.deadlines.Load(), sourceCalls.Load(), verifierCalls.Load(), *blockedWrite)
	}
	t.Logf("observed TCP backpressure timeout: deadline_remaining=%s write_duration=%s blocked_write_bytes=%d total_server_bytes=%d deadline_updates=%d source_calls=%d verifier_calls=%d", deadlineRemaining, blockedWrite.duration, blockedWrite.bytes, listener.totalBytes.Load(), listener.deadlines.Load(), sourceCalls.Load(), verifierCalls.Load())
	start := time.Now()
	select {
	case <-listenHandlerReturned:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("listener handler did not exit promptly after TCP write timeout: %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("listener handler did not exit after observed TCP write timeout: %+v", *blockedWrite)
	}
	sourcesAtClose, verifiersAtClose := sourceCalls.Load(), verifierCalls.Load()
	time.Sleep(100 * time.Millisecond)
	if sourceCalls.Load() != sourcesAtClose || verifierCalls.Load() != verifiersAtClose {
		t.Fatalf("listener watcher/auth loop continued after stream close: source %d->%d verifier %d->%d", sourcesAtClose, sourceCalls.Load(), verifiersAtClose, verifierCalls.Load())
	}
	releaseClientReader()
	cancelSubscribe()
	select {
	case <-subscribeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("native SDK Subscribe call did not unwind after the server closed the timed-out stream")
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
