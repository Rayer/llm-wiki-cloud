package authconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestVerifyCacheHitAcrossExpiryBoundaryRechecksSafely(t *testing.T) {
	const helperEnv = "GAT21_VERIFY_CACHE_EXPIRY_HELPER"
	if os.Getenv(helperEnv) == "1" {
		cache, err := newVerificationCache()
		if err != nil {
			t.Fatal(err)
		}
		base := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
		validUntil := base.Add(verificationReuseTTL)
		var clockCalls atomic.Int64
		cache.now = func() time.Time {
			if clockCalls.Add(1) == 1 {
				return validUntil.Add(-time.Nanosecond)
			}
			return validUntil
		}
		cache.entries["boundary"] = verificationEntry{
			identity:   webIdentity("user-a", validUntil.Add(time.Minute)),
			validUntil: validUntil,
			lastUsed:   1,
		}
		var outboundCalls atomic.Int64
		_, err = cache.verify(context.Background(), "boundary", func() (Identity, error) {
			outboundCalls.Add(1)
			return Identity{}, ErrInvalidCredential
		})
		if !errors.Is(err, ErrInvalidCredential) || outboundCalls.Load() != 1 {
			t.Fatalf("expiry-boundary verification err=%v outbound=%d, want one fresh rejection", err, outboundCalls.Load())
		}
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestVerifyCacheHitAcrossExpiryBoundaryRechecksSafely$")
	command.Env = append(os.Environ(), helperEnv+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("expiry-boundary revalidation subprocess failed: %v\n%s", err, output)
	}
}

func TestVerifyResourceCacheUsesSixtySecondAndOriginalExpiryCeilingsAndClones(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		expiry := now.Add(2 * time.Minute)
		if r.Header.Get("X-LWC-User-Credential") == "short" {
			expiry = now.Add(15 * time.Second)
		}
		writeTestIdentity(w, Identity{SchemaVersion: 1, Subject: "user-a", CredentialKind: "web-access", Environment: "dev",
			Issuer: "https://auth.example.test", Resource: "https://bff.example.test", ExpiresAt: expiry.Unix(),
			Scopes: []string{"read"}, ManagementScopes: []string{"lwc:admin"}})
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	client.verifyCache.now = func() time.Time { return now }

	first, err := client.Verify(context.Background(), "credential", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	first.Scopes[0] = "mutated"
	first.ManagementScopes[0] = "mutated"
	second, err := client.Verify(context.Background(), "credential", "project-a")
	if err != nil || second.Scopes[0] != "read" || second.ManagementScopes[0] != "lwc:admin" || calls.Load() != 1 {
		t.Fatalf("cached identity=%+v calls=%d err=%v", second, calls.Load(), err)
	}

	now = now.Add(59*time.Second + 999*time.Millisecond)
	if _, err := client.Verify(context.Background(), "credential", "project-a"); err != nil || calls.Load() != 1 {
		t.Fatalf("pre-TTL hit calls=%d err=%v", calls.Load(), err)
	}
	now = time.Date(2026, 11, 3, 0, 1, 0, 0, time.UTC)
	if _, err := client.Verify(context.Background(), "credential", "project-a"); err != nil || calls.Load() != 2 {
		t.Fatalf("at-TTL revalidation calls=%d err=%v", calls.Load(), err)
	}

	now = time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	if _, err := client.Verify(context.Background(), "short", "project-a"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(14 * time.Second)
	if _, err := client.Verify(context.Background(), "short", "project-a"); err != nil || calls.Load() != 3 {
		t.Fatalf("short-token pre-expiry calls=%d err=%v", calls.Load(), err)
	}
	now = now.Add(time.Second)
	if _, err := client.Verify(context.Background(), "short", "project-a"); err != nil || calls.Load() != 4 {
		t.Fatalf("original-expiry revalidation calls=%d err=%v", calls.Load(), err)
	}
}

func TestVerifyResourceDoesNotJoinInFlightAuthorizationChecks(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		enterOnce.Do(func() { close(entered) })
		<-release
		writeTestIdentity(w, webIdentity("user-a", time.Now().Add(time.Minute)))
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	const workers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := client.Verify(context.Background(), "same-credential", "project-a")
			errCh <- err
		}()
	}
	close(start)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("verifier request did not start")
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() < workers && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != workers {
		close(release)
		wg.Wait()
		t.Fatalf("in-flight verifier calls=%d, want one fresh call per authorization", calls.Load())
	}
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != workers {
		t.Fatalf("outbound verifier calls=%d, want %d", calls.Load(), workers)
	}
	if _, err := client.Verify(context.Background(), "same-credential", "project-a"); err != nil || calls.Load() != workers {
		t.Fatalf("post-success Web cache calls=%d err=%v; subsequent Web request should reuse success", calls.Load(), err)
	}
}

func TestVerifyResourceAdmissionWaitMakesItsOwnAuthorizationCheck(t *testing.T) {
	base := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	cache, err := newVerificationCache()
	if err != nil {
		t.Fatal(err)
	}
	cache.maxOutbound = 1
	cache.now = func() time.Time { return base }
	cache.mu.Lock()
	cache.active = 1
	cache.mu.Unlock()

	var outbound atomic.Int64
	type result struct {
		identity Identity
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		identity, verifyErr := cache.verify(context.Background(), "same-key", func() (Identity, error) {
			outbound.Add(1)
			return Identity{}, ErrInvalidCredential
		})
		resultCh <- result{identity: identity, err: verifyErr}
	}()

	time.Sleep(20 * time.Millisecond)
	if got := outbound.Load(); got != 0 {
		t.Fatalf("outbound verification before capacity release=%d, want 0", got)
	}

	cache.mu.Lock()
	cache.active--
	close(cache.capacityChanged)
	cache.capacityChanged = make(chan struct{})
	cache.mu.Unlock()

	got := <-resultCh
	if !errors.Is(got.err, ErrInvalidCredential) || got.identity.Subject != "" {
		t.Fatalf("admitted identity=%+v err=%v, want fresh invalid-credential rejection", got.identity, got.err)
	}
	if got := outbound.Load(); got != 1 {
		t.Fatalf("post-expiry outbound verifications=%d, want 1", got)
	}
}

func TestVerifyResourceRejectsFixedOriginalExpiryAfterCacheRevalidation(t *testing.T) {
	base := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	now := base
	fixedExpiry := base.Add(15 * time.Second)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeTestIdentity(w, webIdentity("user-a", fixedExpiry))
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	client.verifyCache.now = func() time.Time { return now }

	if _, err := client.Verify(context.Background(), "fixed-expiry-token", "project-a"); err != nil {
		t.Fatalf("initial verification: %v", err)
	}
	now = fixedExpiry.Add(-time.Second)
	if _, err := client.Verify(context.Background(), "fixed-expiry-token", "project-a"); err != nil || calls.Load() != 1 {
		t.Fatalf("pre-expiry cache hit calls=%d err=%v", calls.Load(), err)
	}
	now = fixedExpiry
	if _, err := client.Verify(context.Background(), "fixed-expiry-token", "project-a"); !errors.Is(err, ErrUnavailable) || calls.Load() != 2 {
		t.Fatalf("fixed original expiry calls=%d err=%v, want rejected revalidation", calls.Load(), err)
	}
}

func TestVerifyResourceCacheSeparatesCredentialEnvironmentIssuerResourceAndProject(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Resource  string `json:"resource"`
			ProjectID string `json:"project_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode verification request: %v", err)
		}
		credential := r.Header.Get("X-LWC-User-Credential")
		kind := "web-access"
		identity := webIdentity("user-a", time.Now().Add(time.Minute))
		if strings.HasPrefix(credential, "cli-") {
			kind = "cli-access"
			identity.CredentialKind = kind
			identity.SessionID = "session-a"
		}
		if strings.HasPrefix(body.Resource, "https://bff.example.test/mcp/") {
			kind = "mcp-oauth"
			identity.CredentialKind = kind
			identity.Resource = body.Resource
			identity.ProjectID = body.ProjectID
			identity.SessionID = ""
			identity.GrantID = "grant-a"
			identity.ClientID = "client-a"
			identity.ManagementScopes = []string{}
		}
		_ = kind
		writeTestIdentity(w, identity)
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	check := func(credential, resource, project string) {
		t.Helper()
		if _, err := client.VerifyResource(context.Background(), credential, resource, project); err != nil {
			t.Fatalf("verify credential scope resource=%q project=%q: %v", resource, project, err)
		}
	}
	check("web-account-a", "https://bff.example.test", "project-a")
	check("web-account-a", "https://bff.example.test", "project-a")
	check("web-account-b", "https://bff.example.test", "project-a")
	check("cli-session-a", "https://bff.example.test", "project-a")
	check("mcp-project-a", "https://bff.example.test/mcp/projects/project-a", "project-a")
	check("mcp-project-a", "https://bff.example.test/mcp/projects/project-b", "project-b")
	if calls.Load() != 5 {
		t.Fatalf("outbound verifier calls=%d, want 5 across credential, class, resource, and Project scopes", calls.Load())
	}

	otherEnvironment, err := New(Config{BaseURL: server.URL, Environment: "stage", Resource: "https://bff.example.test", Audience: "https://auth-runtime.example.test"}, nil,
		func(context.Context, string) (string, error) { return "runtime", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherEnvironment.Verify(context.Background(), "web-account-a", "project-a"); err == nil {
		t.Fatal("stage client accepted a dev identity")
	}
	if calls.Load() != 6 {
		t.Fatalf("environment-specific verifier calls=%d, want 6", calls.Load())
	}
	baseKey := client.verifyCache.cacheKey("user-token", "dev", "issuer-a", "resource-a", "project-a")
	for _, parts := range [][5]string{
		{"other-token", "dev", "issuer-a", "resource-a", "project-a"},
		{"user-token", "stage", "issuer-a", "resource-a", "project-a"},
		{"user-token", "dev", "issuer-b", "resource-a", "project-a"},
		{"user-token", "dev", "issuer-a", "resource-b", "project-a"},
		{"user-token", "dev", "issuer-a", "resource-a", "project-b"},
	} {
		if key := client.verifyCache.cacheKey(parts[0], parts[1], parts[2], parts[3], parts[4]); key == baseKey || strings.Contains(key, "user-token") {
			t.Fatalf("credential scope key did not isolate dimensions or exposed credential: %q", key)
		}
	}
}

func TestVerificationCacheMetricsExposeOnlyFiniteSafeDimensions(t *testing.T) {
	ctx := context.Background()
	previous := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(ctx)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestIdentity(w, webIdentity("user-a", time.Now().Add(time.Minute)))
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	for range 2 {
		if _, err := client.Verify(context.Background(), "sensitive-token-value", "project-a"); err != nil {
			t.Fatal(err)
		}
	}

	var data metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	var points []metricdata.DataPoint[int64]
	for _, scope := range data.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name != "lwc.auth_consumer.verification_events" {
				continue
			}
			sum, ok := item.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("verification metric data=%T, want sum", item.Data)
			}
			points = append(points, sum.DataPoints...)
		}
	}
	seen := map[string]int64{}
	for _, point := range points {
		event, _ := point.Attributes.Value(attribute.Key("event"))
		outcome, _ := point.Attributes.Value(attribute.Key("outcome"))
		reason, _ := point.Attributes.Value(attribute.Key("reason"))
		seen[event.AsString()+"/"+outcome.AsString()+"/"+reason.AsString()] = point.Value
		if point.Attributes.Len() != 3 {
			t.Fatalf("verification telemetry carried unexpected dimensions: %#v", point.Attributes.ToSlice())
		}
	}
	if len(points) != 2 || seen["outbound/success/miss"] != 1 || seen["cache_hit/success/fresh"] != 1 {
		t.Fatalf("verification telemetry points=%d series=%#v", len(points), seen)
	}
}

func TestVerifyResourceFailuresAreNotCachedAndRecover(t *testing.T) {
	statuses := []int{http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusOK, http.StatusOK}
	bodies := []string{`{"error":"rate limited"}`, `{"error":"invalid"}`, `{"schema_version":2}`, ""}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(statuses) {
			index = len(statuses) - 1
		}
		if statuses[index] != http.StatusOK {
			w.WriteHeader(statuses[index])
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		if bodies[index] != "" {
			_, _ = w.Write([]byte(bodies[index]))
			return
		}
		writeTestIdentity(w, webIdentity("user-a", time.Now().Add(time.Minute)))
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	if _, err := client.Verify(context.Background(), "same-token", "project-a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("429 error=%v, want unavailable", err)
	}
	if _, err := client.Verify(context.Background(), "same-token", "project-a"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("401 error=%v, want invalid credential", err)
	}
	if _, err := client.Verify(context.Background(), "same-token", "project-a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("malformed response error=%v, want unavailable", err)
	}
	if _, err := client.Verify(context.Background(), "same-token", "project-a"); err != nil {
		t.Fatalf("recovery verify: %v", err)
	}
	if _, err := client.Verify(context.Background(), "same-token", "project-a"); err != nil || calls.Load() != 4 {
		t.Fatalf("successful recovery was not reused: calls=%d err=%v", calls.Load(), err)
	}
}

func TestVerifyResourceTimeoutIsNotCachedAndRecovers(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			time.Sleep(100 * time.Millisecond)
			return
		}
		writeTestIdentity(w, webIdentity("user-a", time.Now().Add(time.Minute)))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Environment: "dev", Resource: "https://bff.example.test",
		Audience: "https://auth-runtime.example.test", Timeout: 5 * time.Second}, &http.Client{Timeout: 20 * time.Millisecond},
		func(context.Context, string) (string, error) { return "runtime", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Verify(context.Background(), "same-token", "project-a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("timeout error=%v, want unavailable", err)
	}
	if _, err := client.Verify(context.Background(), "same-token", "project-a"); err != nil || calls.Load() != 2 {
		t.Fatalf("timeout recovery calls=%d err=%v", calls.Load(), err)
	}
}

func TestVerifyResourceAdmissionStaysBoundedWhenEntriesEvict(t *testing.T) {
	var active atomic.Int64
	var maxActive atomic.Int64
	var calls atomic.Int64
	var gated atomic.Bool
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		credential := r.Header.Get("X-LWC-User-Credential")
		if strings.HasPrefix(credential, "gate-") && gated.Load() {
			current := active.Add(1)
			for previous := maxActive.Load(); current > previous && !maxActive.CompareAndSwap(previous, current); previous = maxActive.Load() {
			}
			<-release
			active.Add(-1)
		}
		writeTestIdentity(w, webIdentity("user-a", time.Now().Add(time.Minute)))
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	client.verifyCache.maxEntries = 2
	client.verifyCache.maxOutbound = 3
	for _, credential := range []string{"warm-a", "warm-b", "warm-c"} {
		if _, err := client.Verify(context.Background(), credential, "project-a"); err != nil {
			t.Fatal(err)
		}
	}
	client.verifyCache.mu.Lock()
	entryCount := len(client.verifyCache.entries)
	client.verifyCache.mu.Unlock()
	if entryCount != 2 {
		t.Fatalf("bounded cache entries=%d, want 2", entryCount)
	}

	gated.Store(true)
	const requests = 8
	var wg sync.WaitGroup
	errCh := make(chan error, requests)
	for index := range requests {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := client.Verify(context.Background(), fmt.Sprintf("gate-%d", index), "project-a")
			errCh <- err
		}(index)
	}
	deadline := time.Now().Add(2 * time.Second)
	for active.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 3 || maxActive.Load() > 3 || calls.Load() != 6 {
		close(release)
		wg.Wait()
		t.Fatalf("gated upstream calls=%d active=%d max=%d; want three admitted with bounded concurrency", calls.Load(), active.Load(), maxActive.Load())
	}
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 11 || maxActive.Load() > 3 {
		t.Fatalf("final upstream calls=%d max concurrent=%d; want 11 calls and <=3 concurrent", calls.Load(), maxActive.Load())
	}
}

func TestVerifyResourceRevalidatesLogoutAndRevocationAtCacheExpiry(t *testing.T) {
	base := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		kind       string
		credential string
	}{
		{name: "web logout waits for original access expiry", kind: "web-access", credential: "web-token"},
		{name: "cli revocation", kind: "cli-access", credential: "cli-token"},
		{name: "mcp grant revocation", kind: "mcp-oauth", credential: "mcp-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := base
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				revokedAt := base.Add(verificationReuseTTL)
				if tc.kind == "web-access" {
					revokedAt = base.Add(2 * verificationReuseTTL)
				}
				if !now.Before(revokedAt) {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"revoked"}`))
					return
				}
				expires := base.Add(2 * verificationReuseTTL)
				identity := webIdentity("user-a", expires)
				identity.CredentialKind = tc.kind
				if tc.kind == "cli-access" {
					identity.SessionID = "session-a"
				}
				if tc.kind == "mcp-oauth" {
					var body struct {
						Resource  string `json:"resource"`
						ProjectID string `json:"project_id"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode MCP verification request: %v", err)
					}
					identity.Resource = body.Resource
					identity.ProjectID = body.ProjectID
					identity.GrantID = "grant-a"
					identity.ClientID = "client-a"
					identity.ManagementScopes = []string{}
				}
				writeTestIdentity(w, identity)
			}))
			defer server.Close()
			client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
			client.verifyCache.now = func() time.Time { return now }
			resource, project := "https://bff.example.test", "project-a"
			if tc.kind == "mcp-oauth" {
				resource = "https://bff.example.test/mcp/projects/project-a"
			}
			verify := func() error {
				_, err := client.VerifyResource(context.Background(), tc.credential, resource, project)
				return err
			}
			if err := verify(); err != nil {
				t.Fatal(err)
			}
			now = base.Add(verificationReuseTTL)
			if tc.kind == "web-access" {
				if err := verify(); err != nil || calls.Load() != 2 {
					t.Fatalf("post-logout revalidation calls=%d err=%v, want existing valid Web result", calls.Load(), err)
				}
				now = base.Add(2 * verificationReuseTTL)
			}
			if err := verify(); !errors.Is(err, ErrInvalidCredential) || calls.Load() != 3 && tc.kind == "web-access" {
				t.Fatalf("expiry revalidation calls=%d err=%v, want invalid credential", calls.Load(), err)
			}
			if tc.kind != "web-access" && calls.Load() != 2 {
				t.Fatalf("revoked credential outbound calls=%d, want 2", calls.Load())
			}
		})
	}
}

func TestVerifyResourceRefreshesCLIAndMCPImmediatelyButRetainsWebCache(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       string
		credential string
		resource   string
		project    string
	}{
		{name: "Web", kind: "web-access", credential: "web-token", resource: "https://bff.example.test"},
		{name: "CLI", kind: "cli-access", credential: "cli-token", resource: "https://bff.example.test"},
		{name: "MCP", kind: "mcp-oauth", credential: "mcp-token", resource: "https://bff.example.test/mcp/projects/project-a", project: "project-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			var revoked atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if revoked.Load() {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"revoked"}`))
					return
				}
				identity := webIdentity("user-a", time.Now().Add(time.Minute))
				identity.CredentialKind = tc.kind
				if tc.kind == "cli-access" {
					identity.SessionID = "session-a"
				}
				if tc.kind == "mcp-oauth" {
					identity.Resource = tc.resource
					identity.ProjectID = tc.project
					identity.GrantID = "grant-a"
					identity.ClientID = "client-a"
				}
				writeTestIdentity(w, identity)
			}))
			defer server.Close()
			client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
			verify := func() error {
				_, err := client.VerifyResource(context.Background(), tc.credential, tc.resource, tc.project)
				return err
			}
			if err := verify(); err != nil {
				t.Fatalf("positive verifier response rejected: %v", err)
			}
			revoked.Store(true)
			if tc.kind == "web-access" {
				if err := verify(); err != nil || calls.Load() != 1 {
					t.Fatalf("Web cached response calls=%d err=%v; want the existing positive cache behavior", calls.Load(), err)
				}
				return
			}
			if err := verify(); !errors.Is(err, ErrInvalidCredential) || calls.Load() != 2 {
				t.Fatalf("immediate revoked verification calls=%d err=%v; want a second Auth round trip and rejection", calls.Load(), err)
			}
		})
	}
}

func TestVerifyFreshBypassesAndDoesNotReplaceWebCache(t *testing.T) {
	var calls atomic.Int64
	var revoked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if revoked.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"revoked"}`))
			return
		}
		writeTestIdentity(w, webIdentity("user-a", time.Now().Add(time.Minute)))
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	if _, err := client.Verify(context.Background(), "web-token", "project-a"); err != nil {
		t.Fatalf("warm positive Web cache: %v", err)
	}
	revoked.Store(true)
	if _, err := client.VerifyFresh(context.Background(), "web-token", "project-a"); !errors.Is(err, ErrInvalidCredential) || calls.Load() != 2 {
		t.Fatalf("fresh Web verification calls=%d err=%v; want an immediate authority rejection", calls.Load(), err)
	}
	if _, err := client.Verify(context.Background(), "web-token", "project-a"); err != nil || calls.Load() != 2 {
		t.Fatalf("failed fresh verification replaced legacy Web cache: calls=%d err=%v", calls.Load(), err)
	}
}

func TestVerificationCacheCheckAccountAndManagementCallsRemainUncached(t *testing.T) {
	var verifyCalls atomic.Int64
	var accountCalls atomic.Int64
	var mutationCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case verifyPath:
			verifyCalls.Add(1)
			writeTestIdentity(w, webIdentity("user-a", time.Now().Add(time.Minute)))
		case checkPath:
			accountCalls.Add(1)
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(`{"schema_version":1,"subject":"user-a","active":true,"auth_version":3}`))
		case "/api/v1/auth/consumers/lwc/management/users/user-a":
			mutationCalls.Add(1)
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(`{"schema_version":1,"status":"updated","id":"user-a"}`))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := testClient(t, server.URL, func(context.Context, string) (string, error) { return "runtime", nil })
	for range 2 {
		if _, err := client.Verify(context.Background(), "credential", "project-a"); err != nil {
			t.Fatal(err)
		}
		if _, err := client.CheckAccount(context.Background(), "user-a"); err != nil {
			t.Fatal(err)
		}
		if err := client.UpdateAccountStatus(context.Background(), "admin", "user-a", "suspended"); err != nil {
			t.Fatal(err)
		}
	}
	if verifyCalls.Load() != 1 || accountCalls.Load() != 2 || mutationCalls.Load() != 2 {
		t.Fatalf("verification=%d account checks=%d mutations=%d; want 1,2,2", verifyCalls.Load(), accountCalls.Load(), mutationCalls.Load())
	}
}

func webIdentity(subject string, expires time.Time) Identity {
	return Identity{SchemaVersion: 1, Subject: subject, CredentialKind: "web-access", Environment: "dev",
		Issuer: "https://auth.example.test", Resource: "https://bff.example.test", ExpiresAt: expires.Unix(),
		Scopes: []string{}, ManagementScopes: []string{}}
}

func writeTestIdentity(w http.ResponseWriter, identity Identity) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(identity)
}
