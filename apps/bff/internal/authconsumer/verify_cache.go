package authconsumer

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const (
	verificationReuseTTL  = 60 * time.Second
	verificationCacheCap  = 256
	verificationFlightCap = 32
)

type verificationCache struct {
	mu              sync.Mutex
	key             []byte
	entries         map[string]verificationEntry
	flights         map[string]*verificationFlight
	capacityChanged chan struct{}
	maxEntries      int
	maxFlights      int
	sequence        uint64
	now             func() time.Time
}

type verificationEntry struct {
	identity   Identity
	validUntil time.Time
	lastUsed   uint64
}

type verificationFlight struct {
	done       chan struct{}
	identity   Identity
	validUntil time.Time
	err        error
	joined     int
}

var verificationEvents metric.Int64Counter

func init() {
	var err error
	verificationEvents, err = otel.Meter("llm-wiki-bff/auth-consumer").Int64Counter(
		"lwc.auth_consumer.verification_events",
		metric.WithDescription("Auth credential verification cache hits, joined checks, and outbound verifier calls"),
	)
	if err != nil {
		verificationEvents, _ = noop.NewMeterProvider().Meter("").Int64Counter("")
	}
}

func newVerificationCache() (*verificationCache, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, errors.New("Auth verification cache key is unavailable")
	}
	return &verificationCache{
		key:             key,
		entries:         make(map[string]verificationEntry),
		flights:         make(map[string]*verificationFlight),
		capacityChanged: make(chan struct{}),
		maxEntries:      verificationCacheCap,
		maxFlights:      verificationFlightCap,
		now:             time.Now,
	}, nil
}

func (cache *verificationCache) cacheKey(credential, environment, issuer, resource, projectID string) string {
	encoded, _ := json.Marshal([5]string{credential, environment, issuer, resource, projectID})
	mac := hmac.New(sha256.New, cache.key)
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil))
}

func (cache *verificationCache) verify(ctx context.Context, key string, verify func() (Identity, error)) (Identity, error) {
	reason := "miss"
	for {
		cache.mu.Lock()
		now := cache.now()
		if entry, ok := cache.entries[key]; ok {
			if now.Before(entry.validUntil) {
				cache.sequence++
				entry.lastUsed = cache.sequence
				cache.entries[key] = entry
				identity := cloneIdentity(entry.identity)
				cache.mu.Unlock()
				if !cache.now().Before(entry.validUntil) {
					reason = "expired"
					continue
				}
				recordVerificationEvent(ctx, "cache_hit", "success", "fresh")
				return identity, nil
			}
			delete(cache.entries, key)
			reason = "expired"
		}

		if flight, ok := cache.flights[key]; ok {
			flight.joined++
			cache.mu.Unlock()
			select {
			case <-ctx.Done():
				recordVerificationEvent(ctx, "singleflight_join", "canceled", "inflight")
				return Identity{}, ErrUnavailable
			case <-flight.done:
			}
			if flight.err != nil {
				recordVerificationEvent(ctx, "singleflight_join", verificationOutcome(flight.err), "inflight")
				return Identity{}, flight.err
			}
			identity := cloneIdentity(flight.identity)
			if !cache.now().Before(flight.validUntil) {
				recordVerificationEvent(ctx, "singleflight_join", "expired", "inflight")
				reason = "expired"
				continue
			}
			recordVerificationEvent(ctx, "singleflight_join", "success", "inflight")
			return identity, nil
		}

		if len(cache.flights) >= cache.maxFlights {
			changed := cache.capacityChanged
			cache.mu.Unlock()
			select {
			case <-ctx.Done():
				return Identity{}, ErrUnavailable
			case <-changed:
			}
			if reason == "miss" {
				reason = "admission_wait"
			}
			continue
		}

		flight := &verificationFlight{done: make(chan struct{})}
		cache.flights[key] = flight
		cache.mu.Unlock()

		identity, err := verify()
		completedAt := cache.now()
		validUntil := time.Time{}
		if err == nil {
			validUntil = completedAt.Add(verificationReuseTTL)
			credentialExpiry := time.Unix(identity.ExpiresAt, 0)
			if credentialExpiry.Before(validUntil) {
				validUntil = credentialExpiry
			}
			if !completedAt.Before(validUntil) {
				err = ErrUnavailable
			}
		}
		cache.mu.Lock()
		if err == nil && !cache.now().Before(validUntil) {
			err = ErrUnavailable
		}
		var returned Identity
		if err == nil {
			cache.storeLocked(key, identity, validUntil)
			flight.identity = cloneIdentity(identity)
			flight.validUntil = validUntil
			returned = cloneIdentity(identity)
		} else {
			flight.err = err
		}
		delete(cache.flights, key)
		close(flight.done)
		close(cache.capacityChanged)
		cache.capacityChanged = make(chan struct{})
		cache.mu.Unlock()
		recordVerificationEvent(ctx, "outbound", verificationOutcome(err), reason)

		if err != nil {
			return Identity{}, err
		}
		if !cache.now().Before(validUntil) {
			return Identity{}, ErrUnavailable
		}
		return returned, nil
	}
}

func (cache *verificationCache) storeLocked(key string, identity Identity, validUntil time.Time) {
	if cache.maxEntries <= 0 {
		return
	}
	if _, exists := cache.entries[key]; !exists && len(cache.entries) >= cache.maxEntries {
		var oldestKey string
		var oldestUse uint64
		first := true
		for candidate, entry := range cache.entries {
			if first || entry.lastUsed < oldestUse {
				oldestKey, oldestUse, first = candidate, entry.lastUsed, false
			}
		}
		delete(cache.entries, oldestKey)
	}
	cache.sequence++
	cache.entries[key] = verificationEntry{
		identity:   cloneIdentity(identity),
		validUntil: validUntil,
		lastUsed:   cache.sequence,
	}
}

func cloneIdentity(identity Identity) Identity {
	if identity.Scopes != nil {
		identity.Scopes = append([]string{}, identity.Scopes...)
	}
	if identity.ManagementScopes != nil {
		identity.ManagementScopes = append([]string{}, identity.ManagementScopes...)
	}
	return identity
}

func verificationOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrInvalidCredential):
		return "invalid_credential"
	case errors.Is(err, ErrForbidden):
		return "denied"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	default:
		return "failure"
	}
}

func recordVerificationEvent(ctx context.Context, event, outcome, reason string) {
	verificationEvents.Add(ctx, 1, metric.WithAttributes(
		attribute.String("event", boundedVerificationLabel(event, "outbound", "cache_hit", "singleflight_join")),
		attribute.String("outcome", boundedVerificationLabel(outcome, "success", "invalid_credential", "denied", "unavailable", "failure", "canceled", "expired")),
		attribute.String("reason", boundedVerificationLabel(reason, "miss", "expired", "admission_wait", "fresh", "inflight")),
	))
}

func boundedVerificationLabel(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "other"
}
