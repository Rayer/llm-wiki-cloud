package mcpquota

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/google/uuid"
	"github.com/rayer/llm-wiki-bff/internal/config"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testPolicy() config.MCPProtection {
	bucket := config.MCPProtectionBucket{Capacity: 100, RefillMicrounitsPerSecond: 1_000_000, MaxInflight: 8}
	class := config.MCPProtectionClass{
		Project: bucket, Account: bucket, FleetMaxInflight: 8, LocalMaxInflight: 4,
		WorkTimeoutMS: 1000, LeaseMS: 11_000,
	}
	content := class
	content.WorkingBytes = 128 << 20
	return config.MCPProtection{
		SchemaVersion: 1, PolicyRevision: "mcp-quota-test-v1", Content: content, Query: class,
		AdmissionTimeoutMS: 2000, TransactionMaxAttempts: 3, ReleaseTimeoutMS: 2000, ReleaseMaxAttempts: 3,
		ControlLocalMaxInflight: 16, ListDefault: 20, ListMax: 50, ChunkMaxBytes: 16_384,
		ResponseMaxBytes: 256 << 10, ReferencePageMax: 50,
	}
}

func frozenContentPolicy() config.MCPProtectionClass {
	return config.MCPProtectionClass{
		Project:          config.MCPProtectionBucket{Capacity: 24, RefillMicrounitsPerSecond: 2_000_000, MaxInflight: 8},
		Account:          config.MCPProtectionBucket{Capacity: 48, RefillMicrounitsPerSecond: 4_000_000, MaxInflight: 16},
		FleetMaxInflight: 64, LocalMaxInflight: 32, WorkTimeoutMS: 20_000, LeaseMS: 60_000, WorkingBytes: 128 << 20,
	}
}

func TestAdmissionKeysSeparateIdentityAndClassAndDoNotStoreIdentity(t *testing.T) {
	identity := Identity{Environment: "dev", Subject: "owner-1", ProjectID: "wiki-1"}
	content := admissionKeys(identity, Content)
	query := admissionKeys(identity, Query)
	if len(content) != 3 || len(query) != 3 {
		t.Fatalf("content/query dimension counts=%d/%d", len(content), len(query))
	}
	for i := range content {
		if content[i].id == query[i].id || strings.Contains(content[i].id, identity.Subject) || strings.Contains(content[i].id, identity.ProjectID) {
			t.Fatalf("admission key leaked identity or collided across classes: %#v %#v", content[i], query[i])
		}
	}
	if content[0].dimension != "account_project" || content[1].dimension != "account" || content[2].dimension != "fleet" {
		t.Fatalf("admission dimension order=%v", []string{content[0].dimension, content[1].dimension, content[2].dimension})
	}
}

func TestDecideAdmissionConsumesProjectAndAccountTokensAndHonorsInflight(t *testing.T) {
	policy := testPolicy().Query
	keys := admissionKeys(Identity{Environment: "local", Subject: "owner", ProjectID: "wiki"}, Query)
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	records := make([]admissionRecord, len(keys))
	for i, key := range keys {
		records[i] = initialRecord(key, now, policy, "mcp-quota-test-v1")
	}
	decision := decideAdmission(records, "lease-one", now, now.Add(time.Second))
	if decision.Code != "admitted" || records[0].TokensMicrounits != 99*microunits || records[1].TokensMicrounits != 99*microunits {
		t.Fatalf("first decision=%+v project/account tokens=%d/%d", decision, records[0].TokensMicrounits, records[1].TokensMicrounits)
	}
	records[0].TokensMicrounits = 0
	decision = decideAdmission(records, "lease-two", now, now.Add(time.Second))
	if decision.Code != "rate_limited" || decision.RetryAfterMS < 1 {
		t.Fatalf("empty token bucket decision=%+v", decision)
	}
	records[0].TokensMicrounits = microunits
	records[1].Leases = []storedLease{{ID: "active", AdmittedAt: now, ExpiresAt: now.Add(2 * time.Second)}}
	records[1].Limits.MaxInflight = 1
	decision = decideAdmission(records, "lease-three", now, now.Add(time.Second))
	if decision.Code != "concurrency_limited" || decision.RetryAfterMS < 1 {
		t.Fatalf("full in-flight bucket decision=%+v", decision)
	}
}

func TestLocalReservationIsBoundedAndIdempotent(t *testing.T) {
	policy := testPolicy()
	policy.Content.WorkingBytes = 100
	owner := &Limiter{policy: policy, active: map[Class]int{}}
	lease := &Lease{owner: owner, class: Content, localHeld: true}
	release, ok := lease.ReserveBytes(60)
	if !ok || owner.activeBytes != 60 {
		t.Fatalf("first reservation ok=%v active=%d", ok, owner.activeBytes)
	}
	if _, ok := lease.ReserveBytes(41); ok {
		t.Fatal("reservation exceeded the process working-set limit")
	}
	release()
	release()
	if owner.activeBytes != 0 {
		t.Fatalf("idempotent release left %d bytes", owner.activeBytes)
	}
	if _, ok := lease.ReserveBytes(-1); ok {
		t.Fatal("negative reservation was accepted")
	}
	if _, ok := (&Lease{owner: owner, class: Query}).ReserveBytes(1); ok {
		t.Fatal("query request borrowed the content working-set budget")
	}
	if _, ok := lease.ReserveBytes(100); !ok {
		t.Fatal("an exact-fit reservation was rejected")
	}
	if owner.activeBytes != 100 {
		t.Fatalf("exact-fit reservation active=%d", owner.activeBytes)
	}
}

func TestRefillDeltaSaturatesWithoutOverflow(t *testing.T) {
	if got := refillDelta(time.Duration(math.MaxInt64), math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("refillDelta overflowed: %d", got)
	}
}

func emulatorClient(t *testing.T, project, scope string) *cloudfirestore.Client {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("requires a loopback Firestore emulator")
	}
	host := endpoint
	if strings.HasPrefix(host, "http://") {
		host = strings.TrimPrefix(host, "http://")
	}
	if strings.Contains(host, "/") {
		t.Fatal("Firestore emulator endpoint must be a host and port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := cloudfirestore.NewClient(ctx, project, option.WithEndpoint("http://"+host), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("create loopback Firestore client: %v", err)
	}
	if err := scopedfirestore.RegisterLocalScope(client, scope); err != nil {
		_ = client.Close()
		t.Fatalf("register test-only local scope: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestLimiterAdmissionGateCancelAndMixedClassEmulator(t *testing.T) {
	project := "mcpquota-gate-" + fmt.Sprint(time.Now().UnixNano())
	scope := "mcpquota-gate-" + fmt.Sprint(time.Now().UnixNano())
	fs := emulatorClient(t, project, scope)
	limiter, err := New(fs, "local", testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{Environment: "local", Subject: "gate-owner", ProjectID: "gate-wiki"}
	limiter.admissionGate <- struct{}{}
	id := "gate-cancel-0"
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("gate-cancel-%d", i)
		seed := sha256.Sum256([]byte(candidate))
		if time.Duration(seed[0])*250*time.Millisecond/255 < 20*time.Millisecond {
			id = candidate
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	calledRunner := make(chan struct{}, 1)
	runner := func(context.Context, func(context.Context, *cloudfirestore.Transaction) error, int) error {
		calledRunner <- struct{}{}
		return nil
	}
	lease, decision, err := limiter.admitWithRunner(ctx, identity, Content, id, runner)
	if lease != nil || !errors.Is(err, ErrUnavailable) || decision.Code != "admission_unavailable" {
		t.Fatalf("canceled gate wait lease=%v decision=%+v err=%v", lease != nil, decision, err)
	}
	select {
	case <-calledRunner:
		t.Fatal("expired admission reached Firestore after the shared deadline")
	default:
	}
	limiter.mu.Lock()
	activeContent := limiter.active[Content]
	limiter.mu.Unlock()
	if activeContent != 0 || len(limiter.admissionGate) != 1 {
		t.Fatalf("canceled gate wait leaked local slot or gate: active_content=%d gate=%d", activeContent, len(limiter.admissionGate))
	}
	<-limiter.admissionGate

	type outcome struct {
		class    Class
		lease    *Lease
		decision Decision
		err      error
	}
	results := make(chan outcome, 2)
	for _, class := range []Class{Content, Query} {
		go func(class Class) {
			got, decision, err := limiter.Admit(context.Background(), identity, class)
			results <- outcome{class: class, lease: got, decision: decision, err: err}
		}(class)
	}
	var admitted []*Lease
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			if got.err != nil || got.lease == nil || got.decision.Code != "admitted" {
				for _, active := range admitted {
					_ = active.Close()
				}
				t.Fatalf("mixed-class %s admission lease=%v decision=%+v err=%v", got.class, got.lease != nil, got.decision, got.err)
			}
			admitted = append(admitted, got.lease)
		case <-time.After(5 * time.Second):
			for _, active := range admitted {
				_ = active.Close()
			}
			t.Fatal("mixed-class admissions did not settle")
		}
	}
	for _, active := range admitted {
		if err := active.Close(); err != nil {
			t.Fatalf("release mixed-class %s lease: %v", active.class, err)
		}
	}
}

func TestLimiterFirestoreAdmissionRateRecoveryCancellationAndLateReleaseEmulator(t *testing.T) {
	project := "mcpquota-emulator-" + fmt.Sprint(time.Now().UnixNano())
	scope := "mcpquota-rate-" + fmt.Sprint(time.Now().UnixNano())
	fs := emulatorClient(t, project, scope)
	policy := testPolicy()
	policy.Query.Project.Capacity = 1
	policy.Query.Project.RefillMicrounitsPerSecond = 1_000_000
	limiter, err := New(fs, "local", policy)
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{Environment: "local", Subject: "rate-owner", ProjectID: "rate-wiki"}

	lease, decision, err := limiter.Admit(context.Background(), identity, Query)
	if err != nil || lease == nil || decision.Code != "admitted" {
		t.Fatalf("initial admission lease=%v decision=%+v err=%v", lease != nil, decision, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if denied, decision, err := limiter.Admit(context.Background(), identity, Query); err != nil || denied != nil || decision.Code != "rate_limited" {
		t.Fatalf("quota rejection lease=%v decision=%+v err=%v", denied != nil, decision, err)
	}
	time.Sleep(1100 * time.Millisecond)
	lease, decision, err = limiter.Admit(context.Background(), identity, Query)
	if err != nil || lease == nil || decision.Code != "admitted" {
		t.Fatalf("refilled admission lease=%v decision=%+v err=%v", lease != nil, decision, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	lease, decision, err = limiter.Admit(cancelled, Identity{Environment: "local", Subject: "cancel-owner", ProjectID: "cancel-wiki"}, Query)
	if !errors.Is(err, ErrUnavailable) || lease != nil || decision.Code != "admission_unavailable" {
		t.Fatalf("canceled admission lease=%v decision=%+v err=%v", lease != nil, decision, err)
	}
	limiter.policy.Query.Project.Capacity = 100
	identity = Identity{Environment: "local", Subject: "late-owner", ProjectID: "late-wiki"}
	oldLease, decision, err := limiter.Admit(context.Background(), identity, Query)
	if err != nil || oldLease == nil || decision.Code != "admitted" {
		t.Fatalf("crash-simulated lease=%v decision=%+v err=%v", oldLease != nil, decision, err)
	}
	keys := oldLease.refs
	oldNow := limiter.clock()
	for _, key := range keys {
		ref := scopedfirestore.Collection(fs, collectionName).Doc(key.id)
		snapshot, err := ref.Get(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var record admissionRecord
		if err := snapshot.DataTo(&record); err != nil {
			t.Fatal(err)
		}
		for i := range record.Leases {
			if record.Leases[i].ID == oldLease.id {
				record.Leases[i].ExpiresAt = oldNow.Add(time.Second)
			}
		}
		if _, err := ref.Set(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	freshNow := oldNow.Add(20 * time.Second)
	limiter.clock = func() time.Time { return freshNow }
	newLease, decision, err := limiter.Admit(context.Background(), identity, Query)
	if err != nil || newLease == nil || decision.Code != "admitted" {
		t.Fatalf("post-crash recovery lease=%v decision=%+v err=%v", newLease != nil, decision, err)
	}
	if err := oldLease.Close(); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		snapshot, err := scopedfirestore.Collection(fs, collectionName).Doc(key.id).Get(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var record admissionRecord
		if err := snapshot.DataTo(&record); err != nil {
			t.Fatal(err)
		}
		if len(record.Leases) != 1 || record.Leases[0].ID != newLease.id {
			t.Fatalf("late release removed the new lease for %s: %+v", key.dimension, record.Leases)
		}
	}
	if err := newLease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLimiterFirestoreSustainedOverloadDoesNotStartWorkEmulator(t *testing.T) {
	project := "mcpquota-overload-" + fmt.Sprint(time.Now().UnixNano())
	scope := "mcpquota-overload-" + fmt.Sprint(time.Now().UnixNano())
	fs := emulatorClient(t, project, scope)
	policy := testPolicy()
	policy.Query.Project.MaxInflight = 1
	policy.Query.LocalMaxInflight = 16
	limiter, err := New(fs, "local", policy)
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{Environment: "local", Subject: "overload-owner", ProjectID: "overload-wiki"}
	active, decision, err := limiter.Admit(context.Background(), identity, Query)
	if err != nil || active == nil || decision.Code != "admitted" {
		t.Fatalf("initial overload admission lease=%v decision=%+v err=%v", active != nil, decision, err)
	}
	keys := admissionKeys(identity, Query)
	readRecords := func() []admissionRecord {
		t.Helper()
		records := make([]admissionRecord, len(keys))
		for i, key := range keys {
			snapshot, err := scopedfirestore.Collection(fs, collectionName).Doc(key.id).Get(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := snapshot.DataTo(&records[i]); err != nil {
				t.Fatal(err)
			}
		}
		return records
	}
	initialRecords := readRecords()
	workStarts, denied := 0, 0
	started := time.Now()
	for attempt := 0; attempt < 12; attempt++ {
		lease, decision, err := limiter.Admit(context.Background(), identity, Query)
		if err != nil || lease != nil || decision.Code != "concurrency_limited" {
			if lease != nil {
				workStarts++
				_ = lease.Close()
			}
			t.Fatalf("overload attempt %d lease=%v decision=%+v err=%v", attempt+1, lease != nil, decision, err)
		}
		denied++
	}
	if denied != 12 || workStarts != 0 {
		t.Fatalf("sustained overload denied=%d work_starts=%d", denied, workStarts)
	}
	for i, record := range readRecords() {
		if !recordHasLease(record, active.id, time.Now().UTC()) || len(record.Leases) != 1 {
			t.Fatalf("denied overload changed active lease count in %s: %+v", keys[i].dimension, record.Leases)
		}
		if record.TokensMicrounits != initialRecords[i].TokensMicrounits {
			t.Fatalf("concurrency denial consumed tokens in %s: before=%d after=%d", keys[i].dimension, initialRecords[i].TokensMicrounits, record.TokensMicrounits)
		}
	}
	if err := active.Close(); err != nil {
		t.Fatalf("close shared overload lease: %v", err)
	}
	recovered, decision, err := limiter.Admit(context.Background(), identity, Query)
	if err != nil || recovered == nil || decision.Code != "admitted" {
		t.Fatalf("post-overload recovery lease=%v decision=%+v err=%v", recovered != nil, decision, err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatalf("close recovered overload lease: %v", err)
	}
	for i, record := range readRecords() {
		if len(record.Leases) != 0 {
			t.Fatalf("recovery left shared lease in %s: %+v", keys[i].dimension, record.Leases)
		}
	}
	t.Logf("shared_cap=%d overload_attempts=%d denied=%d recovered=true work_starts=%d elapsed_ms=%d", policy.Query.Project.MaxInflight, 12, denied, workStarts, time.Since(started).Milliseconds())
}

func TestLimiterDelayedSameUUIDCommitAfterUnavailableReadbackEmulator(t *testing.T) {
	project := "mcpquota-late-commit-" + fmt.Sprint(time.Now().UnixNano())
	scope := "mcpquota-late-commit-" + fmt.Sprint(time.Now().UnixNano())
	fs := emulatorClient(t, project, scope)
	policy := testPolicy()
	policy.Content.FleetMaxInflight = 1
	limiter, err := New(fs, "local", policy)
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{Environment: "local", Subject: "late-owner", ProjectID: "late-wiki"}
	id := uuid.NewString()
	commitGate := make(chan struct{})
	commitResult := make(chan error, 1)
	runner := func(_ context.Context, callback func(context.Context, *cloudfirestore.Transaction) error, attempts int) error {
		go func() {
			<-commitGate
			time.Sleep(25 * time.Millisecond)
			commitResult <- fs.RunTransaction(context.Background(), callback, cloudfirestore.MaxAttempts(attempts))
		}()
		return status.Error(codes.Unavailable, "injected lost transaction response before delayed emulator commit")
	}

	started := time.Now()
	lease, decision, err := limiter.admitWithRunner(context.Background(), identity, Content, id, runner)
	if !errors.Is(err, ErrUnavailable) || lease != nil || decision.Code != "admission_unavailable" {
		t.Fatalf("unconfirmed admission lease=%v decision=%+v err=%v", lease != nil, decision, err)
	}
	keys := admissionKeys(identity, Content)
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	found, readErr := limiter.readLeaseTriplet(readCtx, keys, id, time.Now().UTC())
	readCancel()
	if readErr != nil || found {
		t.Fatalf("pre-commit exact readback found=%t err=%v", found, readErr)
	}
	workStarts := 0
	t.Logf("uuid=%s decision=admission_unavailable exact_readback=false work_starts=%d elapsed_ms=%d", id, workStarts, time.Since(started).Milliseconds())
	close(commitGate)
	if err := <-commitResult; err != nil {
		t.Fatalf("delayed same-UUID Firestore transaction: %v", err)
	}
	readCtx, readCancel = context.WithTimeout(context.Background(), time.Second)
	found, readErr = limiter.readLeaseTriplet(readCtx, keys, id, time.Now().UTC())
	readCancel()
	if readErr != nil || !found {
		t.Fatalf("post-delay exact readback found=%t err=%v", found, readErr)
	}
	for i, snapshot := range mustReadQuotaRecords(t, fs, keys) {
		var record admissionRecord
		if err := snapshot.DataTo(&record); err != nil {
			t.Fatal(err)
		}
		if !recordHasLease(record, id, time.Now().UTC()) {
			t.Fatalf("late UUID %s not active in %s", id, keys[i].dimension)
		}
		if len(record.Leases) != 1 {
			t.Fatalf("late UUID produced %d leases in %s, want one", len(record.Leases), keys[i].dimension)
		}
		if i < 2 && record.TokensMicrounits != 99*microunits {
			t.Fatalf("late transaction consumed tokens in %s: %d", keys[i].dimension, record.TokensMicrounits)
		}
	}

	// A new request remains blocked by the unexpired late lease; a same-UUID
	// reconciliation does not create another lease or debit another token.
	reconciled, decision, err := limiter.admitWithID(context.Background(), identity, Content, id)
	if err != nil || reconciled == nil || decision.Code != "admitted" {
		t.Fatalf("same-UUID reconciliation lease=%v decision=%+v err=%v", reconciled != nil, decision, err)
	}
	for i, snapshot := range mustReadQuotaRecords(t, fs, keys) {
		var record admissionRecord
		if err := snapshot.DataTo(&record); err != nil {
			t.Fatal(err)
		}
		if i < 2 && record.TokensMicrounits != 99*microunits {
			t.Fatalf("same-UUID retry debited %s twice: %d", keys[i].dimension, record.TokensMicrounits)
		}
	}
	if blocked, decision, err := limiter.Admit(context.Background(), identity, Content); err != nil || blocked != nil || decision.Code != "concurrency_limited" {
		t.Fatalf("distinct request crossed late lease blocked=%v decision=%+v err=%v", blocked != nil, decision, err)
	}
	for i, snapshot := range mustReadQuotaRecords(t, fs, keys) {
		var record admissionRecord
		if err := snapshot.DataTo(&record); err != nil {
			t.Fatal(err)
		}
		if len(record.Leases) != 1 || (i < 2 && record.TokensMicrounits != 99*microunits) {
			t.Fatalf("blocked distinct request changed %s record leases=%d tokens=%d", keys[i].dimension, len(record.Leases), record.TokensMicrounits)
		}
	}

	// Discard the local limiter to model the process that lost the response; the
	// shared lease remains until the already-configured 11s lease expires.
	recoveryLimiter, err := New(fs, "local", policy)
	if err != nil {
		t.Fatal(err)
	}
	recoveryLimiter.clock = func() time.Time { return time.Now().UTC().Add(12 * time.Second) }
	recovered, decision, err := recoveryLimiter.Admit(context.Background(), identity, Content)
	if err != nil || recovered == nil || decision.Code != "admitted" {
		t.Fatalf("post-expiry recovery lease=%v decision=%+v err=%v", recovered != nil, decision, err)
	}
	for i, snapshot := range mustReadQuotaRecords(t, fs, keys) {
		var record admissionRecord
		if err := snapshot.DataTo(&record); err != nil {
			t.Fatal(err)
		}
		if recordHasLease(record, id, recoveryLimiter.clock()) {
			t.Fatalf("late lease %s remained after expiry in %s", id, keys[i].dimension)
		}
		if !recordHasLease(record, recovered.id, recoveryLimiter.clock()) {
			t.Fatalf("recovery lease %s missing in %s", recovered.id, keys[i].dimension)
		}
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	for i, snapshot := range mustReadQuotaRecords(t, fs, keys) {
		var record admissionRecord
		if err := snapshot.DataTo(&record); err != nil {
			t.Fatal(err)
		}
		if len(record.Leases) != 0 {
			t.Fatalf("recovery close left %d leases in %s", len(record.Leases), keys[i].dimension)
		}
	}
	t.Logf("uuid=%s same_uuid_retry=admitted distinct_retry=concurrency_limited cap=1 orphan_recovered_after_expiry=true work_starts=%d", id, workStarts)
}

func mustReadQuotaRecords(t *testing.T, fs *cloudfirestore.Client, keys []recordKey) []*cloudfirestore.DocumentSnapshot {
	t.Helper()
	refs := make([]*cloudfirestore.DocumentRef, len(keys))
	for i, key := range keys {
		refs[i] = scopedfirestore.Collection(fs, collectionName).Doc(key.id)
	}
	snapshots, err := fs.GetAll(context.Background(), refs)
	if err != nil {
		t.Fatal(err)
	}
	return snapshots
}

func TestLimiterFirestoreMalformedRecordFailsClosedEmulator(t *testing.T) {
	project := "mcpquota-invalid-" + fmt.Sprint(time.Now().UnixNano())
	scope := "mcpquota-invalid-" + fmt.Sprint(time.Now().UnixNano())
	fs := emulatorClient(t, project, scope)
	limiter, err := New(fs, "local", testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{Environment: "local", Subject: "malformed-owner", ProjectID: "malformed-wiki"}
	keys := admissionKeys(identity, Query)
	fleet := scopedfirestore.Collection(fs, collectionName).Doc(keys[2].id)
	if _, err := fleet.Set(context.Background(), map[string]any{"schema_version": 1, "environment": "local", "class": "query", "dimension": "fleet", "unexpected": true}); err != nil {
		t.Fatal(err)
	}
	lease, decision, err := limiter.Admit(context.Background(), identity, Query)
	if !errors.Is(err, ErrUnavailable) || lease != nil || decision.Code != "admission_unavailable" || limiter.active[Query] != 0 {
		t.Fatalf("malformed admission lease=%v decision=%+v err=%v local=%d", lease != nil, decision, err, limiter.active[Query])
	}
}

func TestLimiterReleaseUnknownCommittedOutcomeReadbackEmulator(t *testing.T) {
	project := "mcpquota-release-unknown-" + fmt.Sprint(time.Now().UnixNano())
	scope := "mcpquota-release-unknown-" + fmt.Sprint(time.Now().UnixNano())
	fs := emulatorClient(t, project, scope)
	limiter, err := New(fs, "local", testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{Environment: "local", Subject: "release-owner", ProjectID: "release-wiki"}
	lease, decision, err := limiter.Admit(context.Background(), identity, Content)
	if err != nil || lease == nil || decision.Code != "admitted" {
		t.Fatalf("initial release test admission lease=%v decision=%+v err=%v", lease != nil, decision, err)
	}
	attempts := 0
	limiter.releaseRunner = func(ctx context.Context, callback func(context.Context, *cloudfirestore.Transaction) error, maxAttempts int) error {
		attempts++
		if err := fs.RunTransaction(ctx, callback, cloudfirestore.MaxAttempts(maxAttempts)); err != nil {
			return err
		}
		return status.Error(codes.Unavailable, "injected lost release response after commit")
	}
	started := time.Now()
	if err := lease.Close(); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		present, readErr := limiter.readLeasePresence(ctx, lease.refs, lease.id)
		cancel()
		for i, snapshot := range mustReadQuotaRecords(t, fs, lease.refs) {
			t.Logf("release readback diagnostic dimension=%s data=%#v", lease.refs[i].dimension, snapshot.Data())
		}
		t.Fatalf("exact triplet readback did not confirm committed release: close_err=%v readback_present=%d readback_err=%v runner_attempts=%d", err, present, readErr, attempts)
	}
	if attempts != limiter.policy.ReleaseMaxAttempts {
		t.Fatalf("injected response-loss attempts=%d, want bounded maximum %d", attempts, limiter.policy.ReleaseMaxAttempts)
	}
	present, err := limiter.readLeasePresence(context.Background(), lease.refs, lease.id)
	if err != nil || present != 0 {
		t.Fatalf("post-release lease triplet presence=%d err=%v", present, err)
	}
	if limiter.active[Content] != 0 || limiter.activeBytes != 0 {
		t.Fatalf("local reservation remained after confirmed release: active=%d bytes=%d", limiter.active[Content], limiter.activeBytes)
	}
	t.Logf("release_unknown_outcome attempts=%d exact_triplet_present=%d settled=true elapsed_ms=%d", attempts, present, time.Since(started).Milliseconds())
}

func TestLimiterConcurrentAcrossProcessesEmulator(t *testing.T) {
	project := "mcpquota-burst-" + fmt.Sprint(time.Now().UnixNano())
	scope := "mcpquota-burst-" + fmt.Sprint(time.Now().UnixNano())
	fs := emulatorClient(t, project, scope)
	base := filepath.Join(t.TempDir(), "burst")
	startPath := base + "-start"
	releasePath := base + "-release"
	const workers = 6
	type child struct {
		id     string
		ready  string
		active string
		trace  string
		done   chan error
	}
	children := make([]child, 0, workers)
	for i := 0; i < workers; i++ {
		id := fmt.Sprintf("worker-%d", i)
		ready, active, trace := base+"-"+id+"-ready", base+"-"+id+"-active", base+"-"+id+"-trace"
		cmd := exec.Command(os.Args[0], "-test.run=^TestMCPQuotaAdmissionWorkerProcess$")
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir(),
			"FIRESTORE_EMULATOR_HOST=" + strings.TrimPrefix(strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")), "http://"),
			"GOOGLE_APPLICATION_CREDENTIALS=/dev/null", "LOCAL_CLOUD_SCOPE=" + scope,
			"MCPQUOTA_WORKER=1", "MCPQUOTA_PROJECT=" + project, "MCPQUOTA_SCOPE=" + scope,
			"MCPQUOTA_WORKER_ID=" + id, "MCPQUOTA_READY=" + ready, "MCPQUOTA_ACTIVE=" + active,
			"MCPQUOTA_START=" + startPath, "MCPQUOTA_RELEASE=" + releasePath, "MCPQUOTA_TRACE=" + trace,
		}
		var output strings.Builder
		cmd.Stdout, cmd.Stderr = &output, &output
		done := make(chan error, 1)
		go func() {
			err := cmd.Run()
			if err != nil {
				done <- fmt.Errorf("subprocess %s: %w: %s", id, err, output.String())
				return
			}
			done <- nil
		}()
		children = append(children, child{id: id, ready: ready, active: active, trace: trace, done: done})
	}
	defer func() { _ = os.WriteFile(releasePath, []byte("release"), 0o600) }()
	waitFor := func(condition func() bool, label string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			for _, worker := range children {
				select {
				case err := <-worker.done:
					if err != nil {
						t.Fatalf("worker exited before %s: %v", label, err)
					}
					t.Fatalf("worker exited before %s", label)
				default:
				}
			}
			if condition() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", label)
	}
	waitFor(func() bool {
		for _, worker := range children {
			if _, err := os.Stat(worker.ready); err != nil {
				return false
			}
		}
		return true
	}, "all six emulator workers to become ready")
	if err := os.WriteFile(startPath, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(func() bool {
		count := 0
		for _, worker := range children {
			if _, err := os.Stat(worker.active); err == nil {
				count++
			}
		}
		return count == workers
	}, "all six fleet leases")

	identity := Identity{Environment: "local", Subject: "burst-owner", ProjectID: "burst-wiki"}
	keys := admissionKeys(identity, Content)
	activeIDs := make(map[string]struct{}, workers)
	for _, worker := range children {
		data, err := os.ReadFile(worker.active)
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(data))
		if len(fields) != 2 {
			t.Fatalf("worker %s active marker=%q", worker.id, data)
		}
		activeIDs[fields[0]] = struct{}{}
	}
	readRecords := func() []admissionRecord {
		t.Helper()
		snapshots, err := fs.GetAll(context.Background(), []*cloudfirestore.DocumentRef{
			scopedfirestore.Collection(fs, collectionName).Doc(keys[0].id),
			scopedfirestore.Collection(fs, collectionName).Doc(keys[1].id),
			scopedfirestore.Collection(fs, collectionName).Doc(keys[2].id),
		})
		if err != nil {
			t.Fatal(err)
		}
		records := make([]admissionRecord, len(snapshots))
		for i, snapshot := range snapshots {
			if !snapshot.Exists() {
				t.Fatalf("admission record %s disappeared", keys[i].dimension)
			}
			if err := snapshot.DataTo(&records[i]); err != nil {
				t.Fatal(err)
			}
		}
		return records
	}
	records := readRecords()
	for i, record := range records {
		if len(record.Leases) > record.Limits.MaxInflight {
			t.Fatalf("%s leases=%d exceeds configured cap=%d", keys[i].dimension, len(record.Leases), record.Limits.MaxInflight)
		}
		for id := range activeIDs {
			if !recordHasLease(record, id, time.Now().UTC()) {
				t.Fatalf("worker lease %s absent from %s record", id, keys[i].dimension)
			}
		}
	}
	orphans := make(map[string]struct{})
	for _, lease := range records[2].Leases {
		if _, active := activeIDs[lease.ID]; !active {
			orphans[lease.ID] = struct{}{}
		}
	}
	var allTraces strings.Builder
	for _, worker := range children {
		trace, err := os.ReadFile(worker.trace)
		if err != nil {
			t.Fatal(err)
		}
		traceText := strings.TrimSpace(string(trace))
		allTraces.WriteString(traceText)
		allTraces.WriteByte('\n')
		t.Logf("worker=%s admission_trace=%s", worker.id, traceText)
	}
	for id := range orphans {
		for i, record := range records {
			if !recordHasLease(record, id, time.Now().UTC()) {
				t.Fatalf("orphan lease %s is not present in all records; missing %s", id, keys[i].dimension)
			}
		}
		if !strings.Contains(allTraces.String(), "attempt_result uuid="+id+" decision=admission_unavailable") || strings.Contains(allTraces.String(), "work_start uuid="+id) {
			t.Fatalf("orphan lease %s has no unavailable/no-work trace", id)
		}
	}
	t.Logf("held workers=%d active UUIDs=%d orphan UUIDs=%d record leases=%d/%d/%d configured caps=%d/%d/%d", workers, len(activeIDs), len(orphans), len(records[0].Leases), len(records[1].Leases), len(records[2].Leases), records[0].Limits.MaxInflight, records[1].Limits.MaxInflight, records[2].Limits.MaxInflight)
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, worker := range children {
		if err := <-worker.done; err != nil {
			t.Fatal(err)
		}
	}
	records = readRecords()
	allTraces.Reset()
	for _, worker := range children {
		trace, err := os.ReadFile(worker.trace)
		if err != nil {
			t.Fatal(err)
		}
		traceText := strings.TrimSpace(string(trace))
		allTraces.WriteString(traceText)
		allTraces.WriteByte('\n')
		t.Logf("worker=%s complete_trace=%s", worker.id, traceText)
	}
	pendingExpiry := make(map[string]struct{}, len(orphans))
	for id := range orphans {
		pendingExpiry[id] = struct{}{}
		for i, record := range records {
			if !recordHasLease(record, id, time.Now().UTC()) {
				t.Fatalf("admission orphan %s is not retained in %s until expiry", id, keys[i].dimension)
			}
		}
		if !strings.Contains(allTraces.String(), "attempt_result uuid="+id+" decision=admission_unavailable") || strings.Contains(allTraces.String(), "work_start uuid="+id) {
			t.Fatalf("orphan lease %s has no unavailable/no-work trace", id)
		}
	}
	for i, record := range records {
		if len(record.Leases) > record.Limits.MaxInflight {
			t.Fatalf("post-release %s leases=%d exceeds configured cap=%d", keys[i].dimension, len(record.Leases), record.Limits.MaxInflight)
		}
	}
	for id := range activeIDs {
		present := 0
		for _, record := range records {
			if recordHasLease(record, id, time.Now().UTC()) {
				present++
			}
		}
		switch present {
		case 0:
			if !strings.Contains(allTraces.String(), "lease_close uuid="+id+" result=success") {
				t.Fatalf("lease %s absent without a successful close trace", id)
			}
		case len(records):
			if !strings.Contains(allTraces.String(), "lease_close uuid="+id+" result=error") {
				t.Fatalf("lease %s retained without a bounded release failure trace", id)
			}
			pendingExpiry[id] = struct{}{}
		default:
			t.Fatalf("lease %s was only partially released across %d records", id, present)
		}
	}
	t.Logf("concurrent release results pending_expiry=%d all_records=%d/%d/%d", len(pendingExpiry), len(records[0].Leases), len(records[1].Leases), len(records[2].Leases))
	recoveryLimiter, err := New(fs, "local", testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	recoveryLimiter.policy.Content = frozenContentPolicy()
	recoveryLimiter.clock = func() time.Time { return time.Now().UTC().Add(61 * time.Second) }
	recoveryID := uuid.NewString()
	var transactionErrors []error
	recoveryRunner := func(ctx context.Context, callback func(context.Context, *cloudfirestore.Transaction) error, attempts int) error {
		err := fs.RunTransaction(ctx, callback, cloudfirestore.MaxAttempts(attempts))
		if err != nil {
			transactionErrors = append(transactionErrors, err)
		}
		return err
	}
	recoveryLease, decision, err := recoveryLimiter.admitWithRunner(context.Background(), identity, Content, recoveryID, recoveryRunner)
	if err != nil || recoveryLease == nil || decision.Code != "admitted" {
		readback, readbackErr := recoveryLimiter.readLeaseTriplet(context.Background(), admissionKeys(identity, Content), recoveryID, recoveryLimiter.clock())
		t.Fatalf("post-expiry recovery lease=%v decision=%+v err=%v transaction_errors=%v exact_readback=%t readback_err=%v", recoveryLease != nil, decision, err, transactionErrors, readback, readbackErr)
	}
	if len(transactionErrors) != 0 {
		t.Logf("post-expiry admission succeeded after transaction errors: %v", transactionErrors)
	}
	for i, record := range readRecords() {
		for id := range pendingExpiry {
			if recordHasLease(record, id, recoveryLimiter.clock()) {
				t.Fatalf("expired pending lease %s remained in %s after recovery", id, keys[i].dimension)
			}
		}
		if len(record.Leases) != 1 || !recordHasLease(record, recoveryLease.id, recoveryLimiter.clock()) {
			t.Fatalf("recovery lease %s missing from %s", recoveryLease.id, keys[i].dimension)
		}
	}
	if err := recoveryLease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPQuotaAdmissionWorkerProcess(t *testing.T) {
	if os.Getenv("MCPQUOTA_WORKER") != "1" {
		return
	}
	project, scope := os.Getenv("MCPQUOTA_PROJECT"), os.Getenv("MCPQUOTA_SCOPE")
	fs := emulatorClient(t, project, scope)
	policy := testPolicy()
	policy.Content = frozenContentPolicy()
	limiter, err := New(fs, "local", policy)
	if err != nil {
		t.Fatal(err)
	}
	trace := func(format string, values ...any) {
		file, err := os.OpenFile(os.Getenv("MCPQUOTA_TRACE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		_, _ = fmt.Fprintf(file, "%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), fmt.Sprintf(format, values...))
	}
	_, err = scopedfirestore.Collection(fs, "mcp_admission_worker_warmup").Doc(os.Getenv("MCPQUOTA_WORKER_ID")).Get(context.Background())
	if err != nil && status.Code(err) != codes.NotFound {
		t.Fatalf("warm the local Firestore client before the burst: %v", err)
	}
	identity := Identity{Environment: "local", Subject: "burst-owner", ProjectID: "burst-wiki"}
	if err := os.WriteFile(os.Getenv("MCPQUOTA_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitUntil := func(path string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(path); err == nil {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	if !waitUntil(os.Getenv("MCPQUOTA_START"), 10*time.Second) {
		t.Fatal("burst start signal was not received")
	}
	unavailableRetries := 0
	for attempt := 0; attempt < 500; attempt++ {
		leaseID := uuid.NewString()
		started := time.Now()
		trace("attempt_start uuid=%s call=%d", leaseID, attempt+1)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lease, decision, err := limiter.admitWithID(ctx, identity, Content, leaseID)
		cancel()
		trace("attempt_result uuid=%s decision=%s error=%t duration_ms=%d", leaseID, decision.Code, err != nil, time.Since(started).Milliseconds())
		if err != nil {
			if decision.Code == "admission_unavailable" && unavailableRetries < 3 {
				readCtx, readCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				found, readErr := limiter.readLeaseTriplet(readCtx, admissionKeys(identity, Content), leaseID, time.Now().UTC())
				readCancel()
				trace("post_return_readback uuid=%s exact_triplet=%t error=%t", leaseID, found, readErr != nil)
				seed := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", os.Getenv("MCPQUOTA_WORKER_ID"), unavailableRetries)))
				delay := max(1000, decision.RetryAfterMS) + int(seed[0])%251
				unavailableRetries++
				trace("client_retry_after uuid=%s delay_ms=%d retry=%d", leaseID, delay, unavailableRetries)
				time.Sleep(time.Duration(delay) * time.Millisecond)
				continue
			}
			t.Fatalf("admission failed: %v (%s)", err, decision.Code)
		}
		if lease != nil {
			if decision.Code != "admitted" {
				t.Fatalf("lease returned with decision %q", decision.Code)
			}
			trace("work_start uuid=%s unavailable_retries=%d", lease.id, unavailableRetries)
			if err := os.WriteFile(os.Getenv("MCPQUOTA_ACTIVE"), []byte(fmt.Sprintf("%s unavailable_retries=%d", lease.id, unavailableRetries)), 0o600); err != nil {
				_ = lease.Close()
				t.Fatal(err)
			}
			if !waitUntil(os.Getenv("MCPQUOTA_RELEASE"), 20*time.Second) {
				_ = lease.Close()
				t.Fatal("burst release signal was not received")
			}
			closeStarted := time.Now()
			if err := lease.Close(); err != nil {
				trace("lease_close uuid=%s result=error duration_ms=%d", lease.id, time.Since(closeStarted).Milliseconds())
				return
			}
			trace("lease_close uuid=%s result=success duration_ms=%d", lease.id, time.Since(closeStarted).Milliseconds())
			return
		}
		if decision.Code != "concurrency_limited" && decision.Code != "rate_limited" {
			t.Fatalf("unexpected bounded retry decision %q", decision.Code)
		}
		retryAfter := max(1, min(decision.RetryAfterMS, 1000))
		time.Sleep(time.Duration(retryAfter) * time.Millisecond)
	}
	t.Fatal("bounded Firestore admission retries did not succeed")
}
