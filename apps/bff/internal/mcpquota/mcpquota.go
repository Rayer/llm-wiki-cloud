package mcpquota

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/google/uuid"
	"github.com/rayer/llm-wiki-bff/internal/config"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	Content        Class = "content"
	Query          Class = "query"
	collectionName       = "mcp_admission_v1"
	microunits           = int64(1_000_000)
)

type Class string

type Identity struct {
	Environment string
	Subject     string
	ProjectID   string
}

type Decision struct {
	Code         string
	RetryAfterMS int
}

var ErrUnavailable = errors.New("MCP admission unavailable")
var ErrInvalidIdentity = errors.New("invalid MCP admission identity")

// Limiter combines a per-process active-work and byte budget with a Firestore
// transaction over account/project/fleet records.
type Limiter struct {
	fs            *cloudfirestore.Client
	environment   string
	policy        config.MCPProtection
	clock         func() time.Time
	admissionGate chan struct{}

	mu          sync.Mutex
	active      map[Class]int
	activeBytes int64
}

type Lease struct {
	owner    *Limiter
	class    Class
	id       string
	refs     []recordKey
	deadline time.Time

	mu        sync.Mutex
	bytes     int64
	closed    bool
	localHeld bool
}

type admissionTransactionRunner func(context.Context, func(context.Context, *cloudfirestore.Transaction) error, int) error

func New(fs *cloudfirestore.Client, environment string, policy config.MCPProtection) (*Limiter, error) {
	if fs == nil || (environment != "local" && environment != "dev" && environment != "prod") || config.ValidateMCPProtection(policy) != nil {
		return nil, ErrUnavailable
	}
	return &Limiter{fs: fs, environment: environment, policy: policy, clock: time.Now, admissionGate: make(chan struct{}, 1), active: map[Class]int{}}, nil
}

func (l *Limiter) Environment() string {
	if l == nil {
		return ""
	}
	return l.environment
}

// Admit first reserves a local slot. A denial or unknown shared outcome gives
// that slot back and never starts the caller's protected work.
func (l *Limiter) Admit(ctx context.Context, identity Identity, class Class) (*Lease, Decision, error) {
	return l.admitWithID(ctx, identity, class, uuid.NewString())
}

// admitWithID keeps the same lease identity across the bounded transaction
// attempts. Tests use it to correlate an ambiguous commit with its work start.
func (l *Limiter) admitWithID(ctx context.Context, identity Identity, class Class, id string) (*Lease, Decision, error) {
	return l.admitWithRunner(ctx, identity, class, id, nil)
}

func (l *Limiter) admitWithRunner(ctx context.Context, identity Identity, class Class, id string, runner admissionTransactionRunner) (*Lease, Decision, error) {
	if l == nil || l.fs == nil {
		return nil, Decision{Code: "admission_unavailable", RetryAfterMS: 1000}, ErrUnavailable
	}
	if !validIdentity(identity) || identity.Environment != l.environment {
		return nil, Decision{Code: "scope_denied"}, ErrInvalidIdentity
	}
	classPolicy, localLimit, ok := l.classPolicy(class)
	if !ok {
		return nil, Decision{Code: "admission_unavailable", RetryAfterMS: 1000}, ErrUnavailable
	}
	if !l.tryLocal(class, localLimit) {
		return nil, Decision{Code: "concurrency_limited", RetryAfterMS: 250}, nil
	}

	if id == "" {
		return nil, Decision{Code: "admission_unavailable", RetryAfterMS: 1000}, ErrUnavailable
	}
	keys := admissionKeys(identity, class)
	now := l.clock().UTC()
	deadline := now.Add(time.Duration(classPolicy.WorkTimeoutMS) * time.Millisecond)
	lease := Lease{owner: l, class: class, id: id, refs: keys, deadline: deadline, localHeld: true}
	admissionCtx, cancel := context.WithTimeout(ctx, time.Duration(l.policy.AdmissionTimeoutMS)*time.Millisecond)
	defer cancel()
	if !waitAdmissionStartJitter(admissionCtx, id) {
		l.releaseLocal(class)
		lease.localHeld = false
		return nil, Decision{Code: "admission_unavailable", RetryAfterMS: 1000}, ErrUnavailable
	}
	// Same-process requests share the account and fleet records. Serialize only
	// this bounded transaction section to avoid making a normal local burst burn
	// all SDK transaction attempts against its own writes.
	select {
	case l.admissionGate <- struct{}{}:
		defer func() { <-l.admissionGate }()
	case <-admissionCtx.Done():
		l.releaseLocal(class)
		lease.localHeld = false
		return nil, Decision{Code: "admission_unavailable", RetryAfterMS: 1000}, ErrUnavailable
	}
	leaseExpiry := now.Add(time.Duration(classPolicy.LeaseMS) * time.Millisecond)
	decision, err := l.admitTransaction(admissionCtx, keys, id, now, deadline, leaseExpiry, classPolicy, runner)
	if err != nil || decision.Code != "admitted" {
		l.releaseLocal(class)
		lease.localHeld = false
		if err != nil {
			return nil, Decision{Code: "admission_unavailable", RetryAfterMS: 1000}, ErrUnavailable
		}
		return nil, decision, nil
	}
	return &lease, Decision{Code: "admitted"}, nil
}

// Context applies the product work deadline captured by the admitted lease.
func (lease *Lease) Context(parent context.Context) (context.Context, context.CancelFunc) {
	if lease == nil {
		return context.WithCancel(parent)
	}
	return context.WithDeadline(parent, lease.deadline)
}

// ReserveBytes is a nonblocking reservation against the process working set.
// Call it before the next allocation or storage read; it is not an RSS claim.
func (lease *Lease) ReserveBytes(size int64) (func(), bool) {
	if lease == nil || lease.owner == nil || size < 0 || lease.class != Content {
		return func() {}, false
	}
	owner := lease.owner
	owner.mu.Lock()
	if owner.activeBytes > int64(owner.policy.Content.WorkingBytes)-size {
		owner.mu.Unlock()
		return func() {}, false
	}
	owner.activeBytes += size
	owner.mu.Unlock()
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		owner.mu.Lock()
		owner.activeBytes -= size
		owner.mu.Unlock()
		return func() {}, false
	}
	lease.bytes += size
	lease.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			lease.mu.Lock()
			if lease.closed {
				lease.mu.Unlock()
				return
			}
			lease.bytes -= size
			owner.mu.Lock()
			owner.activeBytes -= size
			owner.mu.Unlock()
			lease.mu.Unlock()
		})
	}, true
}

// Close settles all reservations and releases the exact shared lease. It uses
// a fresh bounded context so caller cancellation cannot strand local capacity.
func (lease *Lease) Close() error {
	if lease == nil || lease.owner == nil {
		return nil
	}
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		return nil
	}
	lease.closed = true
	bytesHeld := lease.bytes
	lease.bytes = 0
	owner := lease.owner
	if bytesHeld != 0 {
		owner.mu.Lock()
		owner.activeBytes -= bytesHeld
		owner.mu.Unlock()
	}
	lease.mu.Unlock()

	var releaseErr error
	for attempt := 0; attempt < owner.policy.ReleaseMaxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(owner.policy.ReleaseTimeoutMS)*time.Millisecond)
		if !waitAdmissionStartJitter(ctx, lease.id+"-release") {
			releaseErr = ErrUnavailable
		} else {
			releaseErr = owner.releaseTransaction(ctx, lease.refs, lease.id)
		}
		cancel()
		if releaseErr == nil {
			break
		}
		if !retryableAdmissionFailure(releaseErr) || attempt+1 == owner.policy.ReleaseMaxAttempts {
			break
		}
		if !waitAdmissionRetry(context.Background(), lease.id+"-release", attempt) {
			break
		}
	}
	if lease.localHeld {
		owner.releaseLocal(lease.class)
		lease.localHeld = false
	}
	if releaseErr != nil {
		return ErrUnavailable
	}
	return nil
}

func (l *Limiter) classPolicy(class Class) (config.MCPProtectionClass, int, bool) {
	switch class {
	case Content:
		return l.policy.Content, l.policy.Content.LocalMaxInflight, true
	case Query:
		return l.policy.Query, l.policy.Query.LocalMaxInflight, true
	default:
		return config.MCPProtectionClass{}, 0, false
	}
}

func (l *Limiter) tryLocal(class Class, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[class] >= limit {
		return false
	}
	l.active[class]++
	return true
}
func (l *Limiter) releaseLocal(class Class) { l.mu.Lock(); l.active[class]--; l.mu.Unlock() }

func validIdentity(identity Identity) bool {
	if identity.Environment != "local" && identity.Environment != "dev" && identity.Environment != "prod" {
		return false
	}
	return validKeyPart(identity.Subject) && validKeyPart(identity.ProjectID)
}
func validKeyPart(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

type recordKey struct{ id, dimension, environment, class string }

func admissionKeys(identity Identity, class Class) []recordKey {
	return []recordKey{
		{recordID(identity.Environment, class, "account_project", identity.Subject, identity.ProjectID), "account_project", identity.Environment, string(class)},
		{recordID(identity.Environment, class, "account", identity.Subject, ""), "account", identity.Environment, string(class)},
		{recordID(identity.Environment, class, "fleet", "", ""), "fleet", identity.Environment, string(class)},
	}
}
func recordID(environment string, class Class, dimension, subject, project string) string {
	hash := sha256.New()
	for _, field := range []string{"1", environment, string(class), dimension, subject, project} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type storedLimits struct {
	CapacityMicrounits        int64 `firestore:"capacity_microunits"`
	RefillMicrounitsPerSecond int64 `firestore:"refill_microunits_per_second"`
	MaxInflight               int   `firestore:"max_inflight"`
}
type storedLease struct {
	ID         string    `firestore:"id"`
	AdmittedAt time.Time `firestore:"admitted_at"`
	ExpiresAt  time.Time `firestore:"expires_at"`
}
type admissionRecord struct {
	SchemaVersion    int           `firestore:"schema_version"`
	Environment      string        `firestore:"environment"`
	Class            string        `firestore:"class"`
	Dimension        string        `firestore:"dimension"`
	PolicyRevision   string        `firestore:"policy_revision"`
	TokensMicrounits int64         `firestore:"tokens_microunits"`
	RefilledAt       time.Time     `firestore:"refilled_at"`
	UpdatedAt        time.Time     `firestore:"updated_at"`
	Limits           storedLimits  `firestore:"limits"`
	Leases           []storedLease `firestore:"leases"`
}

func (l *Limiter) admitTransaction(ctx context.Context, keys []recordKey, id string, now, deadline, leaseExpiry time.Time, policy config.MCPProtectionClass, runner admissionTransactionRunner) (Decision, error) {
	refs := make([]*cloudfirestore.DocumentRef, len(keys))
	for i, key := range keys {
		refs[i] = scopedfirestore.Collection(l.fs, collectionName).Doc(key.id)
	}
	var decision Decision
	attempts := l.policy.TransactionMaxAttempts
	if runner == nil {
		runner = func(ctx context.Context, fn func(context.Context, *cloudfirestore.Transaction) error, maxAttempts int) error {
			return l.fs.RunTransaction(ctx, fn, cloudfirestore.MaxAttempts(maxAttempts))
		}
	}
	runTransaction := func(txCtx context.Context) error {
		return runner(txCtx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
			records := make([]admissionRecord, len(refs))
			missing := make([]bool, len(refs))
			snapshots, err := tx.GetAll(refs)
			if err != nil {
				return err
			}
			for i, snapshot := range snapshots {
				if !snapshot.Exists() {
					missing[i] = true
					continue
				}
				if err := validateStoredSnapshot(snapshot, keys[i]); err != nil {
					return err
				}
				if err := snapshot.DataTo(&records[i]); err != nil {
					return err
				}
				if err := validateRecordIdentity(records[i], keys[i]); err != nil {
					return err
				}
			}
			// A shared transaction retry uses the same UUID. If all three exact
			// leases exist, it is a readback of the original accepted attempt.
			found := 0
			for i := range records {
				if !missing[i] && recordHasLease(records[i], id, now) {
					found++
				}
			}
			if found == len(records) {
				decision = Decision{Code: "admitted"}
				return nil
			}
			if found != 0 {
				return errors.New("partial MCP admission lease")
			}
			effectiveNow := now
			for i := range records {
				if !missing[i] && records[i].UpdatedAt.After(effectiveNow) {
					effectiveNow = records[i].UpdatedAt
				}
			}

			effective := make([]admissionRecord, len(refs))
			for i := range refs {
				if missing[i] {
					effective[i] = initialRecord(keys[i], effectiveNow, policy, l.policy.PolicyRevision)
				} else {
					effective[i] = records[i]
					if err := normalizeRecord(&effective[i], keys[i], effectiveNow, policy, l.policy.PolicyRevision); err != nil {
						return err
					}
				}
			}
			decision = decideAdmission(effective, id, effectiveNow, deadline)
			if decision.Code != "admitted" {
				return nil
			}
			for i := range effective {
				effective[i].Leases = append(effective[i].Leases, storedLease{ID: id, AdmittedAt: effectiveNow, ExpiresAt: effectiveNow.Add(leaseExpiry.Sub(now))})
				effective[i].UpdatedAt = effectiveNow
				if err := tx.Set(refs[i], effective[i]); err != nil {
					return err
				}
			}
			return nil
		}, attempts)
	}
	decision = Decision{}
	totalBudget := time.Duration(l.policy.AdmissionTimeoutMS) * time.Millisecond
	readbackBudget := min(250*time.Millisecond, totalBudget/4)
	transactionCtx, cancel := context.WithTimeout(ctx, totalBudget-readbackBudget)
	err := runTransaction(transactionCtx)
	cancel()
	if err == nil {
		return decision, nil
	}
	// Keep readback inside the shared admission deadline and accept only the
	// exact UUID triplet when the transaction outcome is unknown.
	found, readErr := l.readLeaseTriplet(ctx, keys, id, now)
	if readErr == nil && found {
		return Decision{Code: "admitted"}, nil
	}
	if readErr != nil || !retryableAdmissionFailure(err) {
		return Decision{Code: "admission_unavailable", RetryAfterMS: 1000}, ErrUnavailable
	}
	return Decision{Code: "admission_unavailable", RetryAfterMS: 1000}, ErrUnavailable
}

func retryableAdmissionFailure(err error) bool {
	switch status.Code(err) {
	case codes.Aborted, codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		return true
	default:
		return false
	}
}

func waitAdmissionRetry(ctx context.Context, id string, attempt int) bool {
	seed := sha256.Sum256([]byte(id))
	delay := time.Duration(25+int(seed[attempt%len(seed)]%76)) * time.Millisecond * time.Duration(attempt+1)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func waitAdmissionStartJitter(ctx context.Context, id string) bool {
	seed := sha256.Sum256([]byte(id))
	delay := time.Duration(seed[0]) * 250 * time.Millisecond / 255
	if delay == 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (l *Limiter) readLeaseTriplet(ctx context.Context, keys []recordKey, id string, now time.Time) (bool, error) {
	refs := make([]*cloudfirestore.DocumentRef, len(keys))
	for i, key := range keys {
		refs[i] = scopedfirestore.Collection(l.fs, collectionName).Doc(key.id)
	}
	snapshots, err := l.fs.GetAll(ctx, refs)
	if err != nil {
		return false, err
	}
	for i, snapshot := range snapshots {
		if !snapshot.Exists() {
			return false, nil
		}
		var record admissionRecord
		if validateStoredSnapshot(snapshot, keys[i]) != nil || snapshot.DataTo(&record) != nil || validateRecordIdentity(record, keys[i]) != nil {
			return false, ErrUnavailable
		}
		if !recordHasLease(record, id, now) {
			return false, nil
		}
	}
	return true, nil
}

func (l *Limiter) releaseTransaction(ctx context.Context, keys []recordKey, id string) error {
	refs := make([]*cloudfirestore.DocumentRef, len(keys))
	for i, key := range keys {
		refs[i] = scopedfirestore.Collection(l.fs, collectionName).Doc(key.id)
	}
	return l.fs.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		effectiveNow := l.clock().UTC()
		records := make([]admissionRecord, len(refs))
		missing := make([]bool, len(refs))
		snapshots, err := tx.GetAll(refs)
		if err != nil {
			return err
		}
		for i, snapshot := range snapshots {
			if !snapshot.Exists() {
				missing[i] = true
				continue
			}
			if err := validateStoredSnapshot(snapshot, keys[i]); err != nil {
				return err
			}
			if err := snapshot.DataTo(&records[i]); err != nil {
				return err
			}
			if err := validateRecordIdentity(records[i], keys[i]); err != nil {
				return err
			}
			if records[i].UpdatedAt.After(effectiveNow) {
				effectiveNow = records[i].UpdatedAt
			}
		}
		for i := range records {
			if missing[i] {
				continue
			}
			leases := records[i].Leases[:0]
			for _, lease := range records[i].Leases {
				if lease.ID != id {
					leases = append(leases, lease)
				}
			}
			records[i].Leases = leases
			records[i].UpdatedAt = effectiveNow
		}
		for i, ref := range refs {
			if !missing[i] {
				if err := tx.Set(ref, records[i]); err != nil {
					return err
				}
			}
		}
		return nil
	}, cloudfirestore.MaxAttempts(l.policy.ReleaseMaxAttempts))
}

func initialRecord(key recordKey, now time.Time, policy config.MCPProtectionClass, revision string) admissionRecord {
	class := Class(key.class)
	limits := policyLimits(policy, class, key.dimension)
	capacity := limits.CapacityMicrounits
	if key.dimension == "fleet" {
		capacity = 0
	}
	return admissionRecord{SchemaVersion: 1, Environment: key.environment, Class: string(class), Dimension: key.dimension, PolicyRevision: revision, TokensMicrounits: capacity, RefilledAt: now, UpdatedAt: now, Limits: limits, Leases: []storedLease{}}
}

func policyLimits(policy config.MCPProtectionClass, class Class, dimension string) storedLimits {
	if dimension == "fleet" {
		return storedLimits{MaxInflight: policy.FleetMaxInflight}
	}
	bucket := policy.Project
	if dimension == "account" {
		bucket = policy.Account
	}
	return storedLimits{CapacityMicrounits: bucket.Capacity * microunits, RefillMicrounitsPerSecond: bucket.RefillMicrounitsPerSecond, MaxInflight: bucket.MaxInflight}
}

func normalizeRecord(record *admissionRecord, key recordKey, now time.Time, policy config.MCPProtectionClass, revision string) error {
	if record.Leases == nil {
		record.Leases = []storedLease{}
	}
	class := Class(record.Class)
	if err := validateRecordIdentity(*record, key); err != nil {
		return err
	}
	if class != Content && class != Query {
		return ErrUnavailable
	}
	prune := record.Leases[:0]
	seen := make(map[string]struct{}, len(record.Leases))
	for _, lease := range record.Leases {
		if lease.ID == "" || lease.AdmittedAt.IsZero() || lease.ExpiresAt.IsZero() {
			return ErrUnavailable
		}
		if _, exists := seen[lease.ID]; exists {
			return ErrUnavailable
		}
		seen[lease.ID] = struct{}{}
		if lease.ExpiresAt.After(now) {
			prune = append(prune, lease)
		}
	}
	record.Leases = prune
	current := policyLimits(policy, class, key.dimension)
	if record.PolicyRevision != revision && len(record.Leases) == 0 {
		record.PolicyRevision = revision
		record.Limits = current
	} else if record.PolicyRevision != revision {
		record.Limits = minLimits(record.Limits, current)
	}
	if record.Limits.CapacityMicrounits > 0 && record.TokensMicrounits > record.Limits.CapacityMicrounits {
		record.TokensMicrounits = record.Limits.CapacityMicrounits
	}
	elapsed := now.Sub(record.RefilledAt)
	if elapsed < 0 {
		elapsed = 0
	}
	if record.Limits.RefillMicrounitsPerSecond > 0 && elapsed > 0 {
		delta := refillDelta(elapsed, record.Limits.RefillMicrounitsPerSecond)
		record.TokensMicrounits = min(record.Limits.CapacityMicrounits, saturatingAdd(record.TokensMicrounits, delta))
	}
	if now.After(record.RefilledAt) {
		record.RefilledAt = now
	}
	return nil
}

func validateRecordIdentity(record admissionRecord, key recordKey) error {
	if record.SchemaVersion != 1 || record.Environment != key.environment || record.Class != key.class || record.Dimension != key.dimension ||
		record.PolicyRevision == "" || record.RefilledAt.IsZero() || record.UpdatedAt.IsZero() || record.Limits.MaxInflight < 1 || record.TokensMicrounits < 0 {
		return ErrUnavailable
	}
	if key.dimension == "fleet" {
		if record.Limits.CapacityMicrounits != 0 || record.Limits.RefillMicrounitsPerSecond != 0 || record.TokensMicrounits != 0 {
			return ErrUnavailable
		}
	} else if record.Limits.CapacityMicrounits < microunits || record.Limits.RefillMicrounitsPerSecond < 1 {
		return ErrUnavailable
	}
	if len(record.Leases) > 10_000 {
		return ErrUnavailable
	}
	seen := make(map[string]struct{}, len(record.Leases))
	for _, lease := range record.Leases {
		if lease.ID == "" || lease.AdmittedAt.IsZero() || lease.ExpiresAt.IsZero() || !lease.ExpiresAt.After(lease.AdmittedAt) {
			return ErrUnavailable
		}
		if _, exists := seen[lease.ID]; exists {
			return ErrUnavailable
		}
		seen[lease.ID] = struct{}{}
	}
	return nil
}

func minLimits(a, b storedLimits) storedLimits {
	minInt64 := func(x, y int64) int64 {
		if x == 0 || y == 0 {
			return 0
		}
		return min(x, y)
	}
	return storedLimits{CapacityMicrounits: minInt64(a.CapacityMicrounits, b.CapacityMicrounits), RefillMicrounitsPerSecond: minInt64(a.RefillMicrounitsPerSecond, b.RefillMicrounitsPerSecond), MaxInflight: min(a.MaxInflight, b.MaxInflight)}
}
func refillDelta(elapsed time.Duration, rate int64) int64 {
	seconds := int64(elapsed / time.Second)
	nanos := int64(elapsed % time.Second)
	if seconds > 0 && rate > math.MaxInt64/seconds {
		return math.MaxInt64
	}
	whole := seconds * rate
	perSecond := int64(time.Second)
	fraction := nanos*(rate/perSecond) + nanos*(rate%perSecond)/perSecond
	return saturatingAdd(whole, fraction)
}
func saturatingAdd(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}

func decideAdmission(records []admissionRecord, id string, now, deadline time.Time) Decision {
	concurrencyWait := 0
	rateWait := 0
	for _, record := range records {
		if len(record.Leases) >= record.Limits.MaxInflight {
			earliest := time.Time{}
			for _, lease := range record.Leases {
				if earliest.IsZero() || lease.ExpiresAt.Before(earliest) {
					earliest = lease.ExpiresAt
				}
			}
			wait := retryMillis(earliest.Sub(now))
			concurrencyWait = max(concurrencyWait, wait)
		}
		if record.Limits.CapacityMicrounits > 0 && record.TokensMicrounits < microunits {
			deficit := microunits - record.TokensMicrounits
			rate := record.Limits.RefillMicrounitsPerSecond
			wait := int(min(int64(math.MaxInt32), (deficit*1000+rate-1)/rate))
			rateWait = max(rateWait, wait)
		}
	}
	if concurrencyWait > 0 {
		return Decision{Code: "concurrency_limited", RetryAfterMS: max(250, concurrencyWait)}
	}
	if rateWait > 0 {
		return Decision{Code: "rate_limited", RetryAfterMS: max(1, rateWait)}
	}
	for i := range records {
		if records[i].Limits.CapacityMicrounits > 0 {
			records[i].TokensMicrounits -= microunits
		}
	}
	_ = id
	_ = deadline
	return Decision{Code: "admitted"}
}
func retryMillis(wait time.Duration) int {
	if wait <= 0 {
		return 250
	}
	ms := int(math.Ceil(float64(wait) / float64(time.Millisecond)))
	return min(ms, 60_000)
}

func recordHasLease(record admissionRecord, id string, now time.Time) bool {
	for _, lease := range record.Leases {
		if lease.ID == id && lease.ExpiresAt.After(now) {
			return true
		}
	}
	return false
}
func validateStoredSnapshot(snapshot *cloudfirestore.DocumentSnapshot, key recordKey) error {
	data := snapshot.Data()
	allowed := map[string]bool{"schema_version": true, "environment": true, "class": true, "dimension": true, "policy_revision": true, "tokens_microunits": true, "refilled_at": true, "updated_at": true, "limits": true, "leases": true}
	for field := range data {
		if !allowed[field] {
			return ErrUnavailable
		}
	}
	for _, field := range []string{"schema_version", "environment", "class", "dimension", "policy_revision", "tokens_microunits", "refilled_at", "updated_at", "limits", "leases"} {
		if _, ok := data[field]; !ok {
			return ErrUnavailable
		}
	}
	if _, ok := data["leases"].([]interface{}); !ok {
		return ErrUnavailable
	}
	if snapshot.Ref.ID != key.id {
		return ErrUnavailable
	}
	return nil
}
