package observability_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/observability/otlpfixture"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type localContextKey struct{}

func TestOTLPTransportUseCaseRealHTTPAndConcurrentIsolation(t *testing.T) {
	capture := otlpfixture.New(t)
	project := "synthetic-project"
	if otlpfixture.CloudMode() {
		project = os.Getenv("LWC_OTLP_QA_PROJECT")
	}
	const serviceAName, serviceBName = "lwc-qa-uc2-a", "lwc-qa-uc2-b"
	providerA := capture.Provider(t, serviceAName, "local-fixture", project)
	providerB := capture.Provider(t, serviceBName, "local-fixture", project)
	tracerA := providerA.Tracer("llm-wiki-bff/query")
	tracerB := providerB.Tracer("llm-wiki-bff/query")
	const count = 24
	type outcome struct {
		parentID, rootID, clientID, serverParentID string
	}
	var outcomesMu sync.Mutex
	outcomes := make(map[string]outcome, count)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("tracestate") != "" || r.Header.Get("baggage") != "" {
			http.Error(w, "private propagation payload crossed owned HTTP seam", http.StatusBadRequest)
			return
		}
		ctx := observability.ExtractTraceContext(r.Context(), propagation.HeaderCarrier(r.Header))
		parentID := trace.SpanContextFromContext(ctx).SpanID().String()
		_, span := tracerB.Start(ctx, "query.http.server")
		defer span.End()
		w.Header().Set("X-QA-Trace-ID", span.SpanContext().TraceID().String())
		w.Header().Set("X-QA-Parent-ID", parentID)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer peer.Close()

	serviceA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := observability.ExtractTraceContext(r.Context(), propagation.HeaderCarrier(r.Header))
		incoming := trace.SpanContextFromContext(ctx)
		ctx = context.WithValue(ctx, localContextKey{}, "request-context-preserved")
		rootCtx, root := tracerA.Start(ctx, "query.request")
		clientCtx, clientSpan := tracerA.Start(rootCtx, "query.http.client")
		if clientCtx.Value(localContextKey{}) != "request-context-preserved" {
			clientSpan.End()
			root.End()
			http.Error(w, "request context value was lost", http.StatusInternalServerError)
			return
		}
		request, err := http.NewRequestWithContext(clientCtx, http.MethodGet, peer.URL, nil)
		if err == nil {
			request.Header.Set("tracestate", "vendor=PRIVATE_TRACESTATE_CANARY")
			request.Header.Set("baggage", "private=PRIVATE_BAGGAGE_CANARY")
			observability.InjectTraceContext(clientCtx, request.Header)
			var response *http.Response
			response, err = http.DefaultClient.Do(request)
			if err == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					err = fmt.Errorf("peer status %d", response.StatusCode)
				} else {
					traceID, serverParent := response.Header.Get("X-QA-Trace-ID"), response.Header.Get("X-QA-Parent-ID")
					if traceID != root.SpanContext().TraceID().String() {
						err = fmt.Errorf("peer trace %s does not match root trace", traceID)
					}
					outcomesMu.Lock()
					outcomes[root.SpanContext().TraceID().String()] = outcome{
						parentID: incoming.SpanID().String(), rootID: root.SpanContext().SpanID().String(),
						clientID: clientSpan.SpanContext().SpanID().String(), serverParentID: serverParent,
					}
					outcomesMu.Unlock()
				}
			}
		}
		clientSpan.End()
		root.End()
		if err != nil {
			http.Error(w, "synthetic peer request failed", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer serviceA.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	var wg sync.WaitGroup
	requestErrs := make(chan error, count)
	inputParents := make(map[string]string, count)
	requests := make([]*http.Request, 0, count)
	for i := 0; i < count; i++ {
		traceID, parentID, traceParent := otlpfixture.NewParent(t)
		request, err := http.NewRequest(http.MethodGet, serviceA.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("traceparent", traceParent)
		request.Header.Set("tracestate", "vendor=PRIVATE_TRACESTATE_CANARY")
		request.Header.Set("baggage", "private=PRIVATE_BAGGAGE_CANARY")
		inputParents[traceID] = parentID
		requests = append(requests, request)
	}
	for _, request := range requests {
		wg.Add(1)
		go func(request *http.Request) {
			defer wg.Done()
			response, err := client.Do(request)
			if err != nil {
				requestErrs <- err
				return
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				requestErrs <- fmt.Errorf("A-to-B status=%d", response.StatusCode)
			}
		}(request)
	}
	wg.Wait()
	close(requestErrs)
	for err := range requestErrs {
		t.Error(err)
	}
	capture.Flush(t, providerA)
	capture.Flush(t, providerB)
	identities := capture.Identities()
	if len(identities) != count*3 {
		t.Fatalf("captured span identities=%d, want %d", len(identities), count*3)
	}
	servicesByTrace := make(map[string]map[string]string, count)
	for _, item := range identities {
		wantService := serviceAName
		if item.Name == "query.http.server" {
			wantService = serviceBName
		} else if item.Name != "query.request" && item.Name != "query.http.client" {
			t.Fatalf("unexpected UC2 span name %q", item.Name)
		}
		if item.ServiceName != wantService {
			t.Fatalf("span %s attributed to service %q, want %q", item.Name, item.ServiceName, wantService)
		}
		if servicesByTrace[item.TraceID] == nil {
			servicesByTrace[item.TraceID] = make(map[string]string, 3)
		}
		if _, duplicate := servicesByTrace[item.TraceID][item.Name]; duplicate {
			t.Fatalf("duplicate captured span %q for trace %s", item.Name, item.TraceID)
		}
		servicesByTrace[item.TraceID][item.Name] = item.ServiceName
	}
	for traceID := range inputParents {
		if got := servicesByTrace[traceID]; len(got) != 3 || got["query.request"] != serviceAName ||
			got["query.http.client"] != serviceAName || got["query.http.server"] != serviceBName {
			t.Fatalf("trace %s service identities=%v", traceID, got)
		}
	}
	capture.LogIdentities(t, "UC2")
	t.Logf("OTLP_QA_CASE=UC2 count=%d expected_spans_per_trace=3", count)
	if otlpfixture.CloudMode() {
		for traceID := range inputParents {
			t.Logf("OTLP_QA_TRACE trace_id=%s expected=3", traceID)
		}
		return
	}

	spansByTrace := make(map[string]map[string]*tracev1.Span, count)
	resourceServicesByTrace := make(map[string]map[string]string, count)
	var encoded bytes.Buffer
	for _, request := range capture.Requests() {
		data, err := proto.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		encoded.Write(data)
		for _, resource := range request.ResourceSpans {
			serviceName := ""
			if resource.Resource != nil {
				for _, attr := range resource.Resource.Attributes {
					if attr.Key == "service.name" {
						serviceName = attr.Value.GetStringValue()
						break
					}
				}
			}
			if serviceName != serviceAName && serviceName != serviceBName {
				t.Fatalf("unexpected UC2 OTLP resource service.name %q", serviceName)
			}
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					traceID := hex.EncodeToString(span.TraceId)
					if spansByTrace[traceID] == nil {
						spansByTrace[traceID] = map[string]*tracev1.Span{}
					}
					if _, duplicate := spansByTrace[traceID][span.Name]; duplicate {
						t.Fatalf("duplicate span %q for trace %s", span.Name, traceID)
					}
					spansByTrace[traceID][span.Name] = span
					if resourceServicesByTrace[traceID] == nil {
						resourceServicesByTrace[traceID] = make(map[string]string, 3)
					}
					resourceServicesByTrace[traceID][span.Name] = serviceName
				}
			}
		}
	}
	if bytes.Contains(encoded.Bytes(), []byte("PRIVATE_TRACESTATE_CANARY")) || bytes.Contains(encoded.Bytes(), []byte("PRIVATE_BAGGAGE_CANARY")) {
		t.Fatal("propagation state or baggage reached OTLP")
	}
	if len(spansByTrace) != count {
		t.Fatalf("exported traces=%d, want %d", len(spansByTrace), count)
	}
	for traceID, parentID := range inputParents {
		spans := spansByTrace[traceID]
		root, clientSpan, serverSpan := spans["query.request"], spans["query.http.client"], spans["query.http.server"]
		if len(spans) != 3 || root == nil || clientSpan == nil || serverSpan == nil {
			t.Fatalf("trace %s span set=%v, want request/client/server", traceID, spans)
		}
		if hex.EncodeToString(root.ParentSpanId) != parentID || !bytes.Equal(clientSpan.ParentSpanId, root.SpanId) ||
			!bytes.Equal(serverSpan.ParentSpanId, clientSpan.SpanId) || bytes.Equal(root.SpanId, clientSpan.SpanId) || bytes.Equal(clientSpan.SpanId, serverSpan.SpanId) {
			t.Fatalf("trace %s parent/span IDs do not match the real A-to-B sequence: root=%+v client=%+v server=%+v", traceID, root, clientSpan, serverSpan)
		}
		services := resourceServicesByTrace[traceID]
		if len(services) != 3 || services["query.request"] != serviceAName || services["query.http.client"] != serviceAName || services["query.http.server"] != serviceBName {
			t.Fatalf("trace %s OTLP resource service identities=%v", traceID, services)
		}
		outcome := outcomes[traceID]
		if outcome.parentID != parentID || outcome.rootID != hex.EncodeToString(root.SpanId) ||
			outcome.clientID != hex.EncodeToString(clientSpan.SpanId) || outcome.serverParentID != outcome.clientID {
			t.Fatalf("trace %s transport correlation=%+v", traceID, outcome)
		}
	}
}

func TestOTLPAsyncBoundaryUseCaseWorkerAndProfileAttemptLinks(t *testing.T) {
	capture := otlpfixture.New(t)
	project := "synthetic-project"
	if otlpfixture.CloudMode() {
		project = os.Getenv("LWC_OTLP_QA_PROJECT")
	}
	provider := capture.Provider(t, "lwc-qa-async-fixture", "local-fixture", project)
	workerTraceID, workerParentID, workerCarrier := otlpfixture.NewParent(t)
	workerCtx := observability.ExtractWorkerTraceContext(context.Background(), workerCarrier)
	_, worker := observability.StartWorkerAttemptSpan(workerCtx, "pipeline.worker")
	worker.End()
	profileTraceID, profileParentID, profileCarrier := otlpfixture.NewParent(t)
	profileCtx := observability.ExtractTraceContext(context.Background(), propagation.MapCarrier{
		"traceparent": profileCarrier, "tracestate": "vendor=PRIVATE_TRACESTATE_CANARY",
	})
	claimCtx, claim := observability.StartProfileSpan(profileCtx, "profile.dispatch")
	_, profileWork := observability.StartProfileWorkSpan(claimCtx)
	profileWork.End()
	claim.End()
	capture.Flush(t, provider)
	capture.LogIdentities(t, "UC5")
	t.Logf("OTLP_QA_CASE=UC5 worker_trace_id=%s worker_parent_span_id=%s profile_dispatch_trace_id=%s profile_parent_span_id=%s",
		workerTraceID, workerParentID, profileTraceID, profileParentID)
	if otlpfixture.CloudMode() {
		return
	}

	var workerSpan, claimSpan, workSpan *tracev1.Span
	var encoded bytes.Buffer
	for _, request := range capture.Requests() {
		data, err := proto.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		encoded.Write(data)
		for _, resource := range request.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					switch span.Name {
					case "pipeline.worker":
						workerSpan = span
					case "profile.dispatch":
						claimSpan = span
					case "profile.work":
						workSpan = span
					}
				}
			}
		}
	}
	if workerSpan == nil || len(workerSpan.ParentSpanId) != 0 || hex.EncodeToString(workerSpan.TraceId) == workerTraceID ||
		len(workerSpan.Links) != 1 || hex.EncodeToString(workerSpan.Links[0].TraceId) != workerTraceID ||
		hex.EncodeToString(workerSpan.Links[0].SpanId) != workerParentID || len(workerSpan.Links[0].Attributes) != 0 || workerSpan.Links[0].TraceState != "" {
		t.Fatalf("worker span lost fresh-root/link semantics or leaked link metadata: %+v", workerSpan)
	}
	if claimSpan == nil || workSpan == nil || hex.EncodeToString(claimSpan.TraceId) != profileTraceID ||
		hex.EncodeToString(claimSpan.ParentSpanId) != profileParentID || claimSpan.TraceState != "" || len(workSpan.ParentSpanId) != 0 ||
		hex.EncodeToString(workSpan.TraceId) == hex.EncodeToString(claimSpan.TraceId) ||
		len(workSpan.Links) != 1 || !bytes.Equal(workSpan.Links[0].TraceId, claimSpan.TraceId) ||
		!bytes.Equal(workSpan.Links[0].SpanId, claimSpan.SpanId) || len(workSpan.Links[0].Attributes) != 0 {
		t.Fatalf("Profile work span does not link to the real claim span: claim=%+v work=%+v", claimSpan, workSpan)
	}
	if bytes.Contains(encoded.Bytes(), []byte("PRIVATE_TRACESTATE_CANARY")) {
		t.Fatal("private propagation metadata reached OTLP")
	}
}
