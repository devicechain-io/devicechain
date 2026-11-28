// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// Bounding the sessions one device can hold at the broker.
//
// A device may append a discriminator of its own to its MQTT client id (messaging.DeviceClientID
// says why), and every distinct client id is a session of its own. A persistent session leaves a
// record in the broker's session store, which is unbounded on purpose (a ceiling there would
// evict live sessions) and which shares the file store with every platform stream. The format
// is part of the device wire and is not narrowed here. What is bounded is how many DISTINCT
// discriminators one device may connect with, and how often one device may connect at all.
//
// The authorization request does not say whether the device asked for a clean session, so the
// bound counts discriminators whatever the flag: a device that invents a new one on every boot
// is counted the same as one that keeps a few persistent sessions.

// Defaults for the bounds. config.Default* mirror them, kept equal by a test.
const (
	DefaultMaxSessionsPerDevice       = 16
	DefaultConnectBurstPerDevice      = 20
	DefaultConnectsPerMinutePerDevice = 60
)

// Session-bound refusal reasons, the label values of callout_session_limited_total.
const (
	sessionLimitedPerDevice        = "sessions_per_device"
	sessionLimitedConnectRate      = "connect_rate"
	sessionLimitedStoreUnavailable = "session_store_unavailable"
)

// sessionLimitedReasons is the closed vocabulary, every member present at zero from startup.
var sessionLimitedReasons = []string{sessionLimitedPerDevice, sessionLimitedConnectRate, sessionLimitedStoreUnavailable}

// sessionDiscriminatorStore is the part of nats.KeyValue the session bound uses. nats.KeyValue
// satisfies it; a test substitutes a map.
type sessionDiscriminatorStore interface {
	Get(key string) (nats.KeyValueEntry, error)
	Create(key string, value []byte) (uint64, error)
	Update(key string, value []byte, last uint64) (uint64, error)
}

// sessionDiscriminatorRefresh is how stale a known discriminator's last-seen time may get before a
// connect with it rewrites the entry. Rewriting at most daily keeps an entry in use alive under the
// bucket's 30-day lifetime without a write on every reconnect.
const sessionDiscriminatorRefresh = 24 * time.Hour

// sessionDiscriminatorWriteAttempts bounds the compare-and-set retries when two replicas record a
// discriminator for the same device at once.
const sessionDiscriminatorWriteAttempts = 4

// sessionDiscriminators is the value of one device's entry: a short hash of each discriminator it
// has connected with, and when it last did (unix seconds). The discriminator itself is not
// stored: it is the device's own string and needs no reading back.
type sessionDiscriminators struct {
	Seen map[string]int64 `json:"s"`
}

// sessionBound is the per-device distinct-discriminator cap.
type sessionBound struct {
	store     sessionDiscriminatorStore
	max       int
	retention time.Duration
}

// discriminatorHash is the stored form of a discriminator: 12 bytes of its SHA-256, base64url.
func discriminatorHash(discriminator string) string {
	sum := sha256.Sum256([]byte(discriminator))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// clientIDDiscriminator returns what follows the device's own plain client id, separator
// included, so every id but the plain one is a non-empty discriminator ("{plain}:" is ":"). It is
// "" only for the plain id itself. presented has already passed DeviceClientIDMatches.
func clientIDDiscriminator(presented, required string) string {
	return strings.TrimPrefix(presented, required)
}

// admit decides whether a device may connect with discriminator, recording it when it is new. It
// returns "" to admit, or the refusal reason.
//
//   - The plain client id ("") is never counted: it is one session per device, the documented
//     form, and it costs no store round trip.
//   - A discriminator already recorded for the device is ALWAYS admitted, also when the
//     cap has since been lowered below the recorded count and also when rewriting its last-seen
//     time fails.
//   - A new one is admitted and recorded while fewer than max are recorded, and refused past it.
//   - Recorded discriminators not seen for the retention are dropped, so a device that stopped
//     using one gets its place back.
//   - A store that cannot be read, or that refuses the write recording a new one (a full bucket),
//     refuses the connect: fail closed, for new discriminators only.
func (b *sessionBound) admit(tenant, deviceToken, discriminator string, now time.Time) string {
	if discriminator == "" {
		return ""
	}
	key := messaging.MqttSessionDiscriminatorKey(tenant, deviceToken)
	hash := discriminatorHash(discriminator)
	for attempt := 0; attempt < sessionDiscriminatorWriteAttempts; attempt++ {
		entry, err := b.store.Get(key)
		var rev uint64
		set := sessionDiscriminators{Seen: map[string]int64{}}
		switch {
		case errors.Is(err, nats.ErrKeyNotFound):
		case err != nil:
			log.Warn().Err(err).Str("tenant", tenant).Str("device", deviceToken).
				Msg("Auth-callout could not read a device's MQTT session record; refusing a connect with a new client id.")
			return sessionLimitedStoreUnavailable
		default:
			rev = entry.Revision()
			if err := json.Unmarshal(entry.Value(), &set); err != nil || set.Seen == nil {
				// Only this code writes the entry. One that does not decode is rewritten from
				// scratch rather than locking the device out of new sessions until it ages out.
				log.Warn().Err(err).Str("tenant", tenant).Str("device", deviceToken).
					Msg("Auth-callout found an unreadable MQTT session record; starting it again.")
				set = sessionDiscriminators{Seen: map[string]int64{}}
			}
		}

		pruned := false
		cutoff := now.Add(-b.retention).Unix()
		for h, seen := range set.Seen {
			if seen < cutoff && h != hash {
				delete(set.Seen, h)
				pruned = true
			}
		}

		last, known := set.Seen[hash]
		if known {
			if !pruned && now.Unix()-last < int64(sessionDiscriminatorRefresh/time.Second) {
				return ""
			}
			set.Seen[hash] = now.Unix()
			// Best effort: a known discriminator is admitted whether or not the rewrite lands.
			_ = b.write(key, set, rev)
			return ""
		}
		if len(set.Seen) >= b.max {
			log.Debug().Str("tenant", tenant).Str("device", deviceToken).Int("recorded", len(set.Seen)).Int("max", b.max).
				Msg("Auth-callout refused a device connect: the device has used as many MQTT client ids as callout.maxSessionsPerDevice allows.")
			return sessionLimitedPerDevice
		}
		set.Seen[hash] = now.Unix()
		err = b.write(key, set, rev)
		if err == nil {
			return ""
		}
		if isRevisionConflict(err) {
			continue
		}
		log.Warn().Err(err).Str("tenant", tenant).Str("device", deviceToken).
			Msg("Auth-callout could not record a device's new MQTT client id; refusing the connect.")
		return sessionLimitedStoreUnavailable
	}
	log.Warn().Str("tenant", tenant).Str("device", deviceToken).
		Msg("Auth-callout lost every race to record a device's new MQTT client id; refusing the connect.")
	return sessionLimitedStoreUnavailable
}

// write stores set as the entry's next revision: a create when there was none (rev 0), an update
// of exactly rev otherwise.
func (b *sessionBound) write(key string, set sessionDiscriminators, rev uint64) error {
	data, err := json.Marshal(set)
	if err != nil {
		return err
	}
	if rev == 0 {
		_, err = b.store.Create(key, data)
	} else {
		_, err = b.store.Update(key, data, rev)
	}
	return err
}

// isRevisionConflict reports whether a write lost a compare-and-set to a concurrent one.
func isRevisionConflict(err error) bool {
	if errors.Is(err, nats.ErrKeyExists) || errors.Is(err, nats.ErrKeyRevisionMismatch) {
		return true
	}
	var apiErr *nats.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode == nats.JSErrCodeStreamWrongLastSequence
}

// connectLimiter is the per-device connect-rate limit: a token bucket per (tenant, device), per
// replica, holding at most burst connects and refilling perMinute a minute. A bucket that has
// refilled is the same as no bucket, so those are swept and memory stays bounded by the devices
// connecting right now.
type connectLimiter struct {
	burst     float64
	perSecond float64

	mu        sync.Mutex
	buckets   map[string]*connectBucket
	lastSweep time.Time
}

type connectBucket struct {
	tokens float64
	at     time.Time
}

// connectLimiterSweepEvery is how often refilled buckets are dropped.
const connectLimiterSweepEvery = time.Minute

func newConnectLimiter(burst, perMinute int) *connectLimiter {
	return &connectLimiter{
		burst:     float64(burst),
		perSecond: float64(perMinute) / 60,
		buckets:   map[string]*connectBucket{},
	}
}

// allow charges one connect to the device and reports whether it is within the limit.
func (l *connectLimiter) allow(tenant, deviceToken string, now time.Time) bool {
	key := tenant + "|" + deviceToken
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= connectLimiterSweepEvery {
		for k, b := range l.buckets {
			if l.level(b, now) >= l.burst {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &connectBucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = l.level(b, now)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// level is the bucket's tokens at now, refilled and capped at the burst.
func (l *connectLimiter) level(b *connectBucket, now time.Time) float64 {
	elapsed := now.Sub(b.at).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	t := b.tokens + elapsed*l.perSecond
	if t > l.burst {
		t = l.burst
	}
	return t
}

// size is how many device buckets are held (for tests).
func (l *connectLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// WithSessionBounds turns on the per-device bounds: at most maxSessions distinct client-id
// discriminators recorded in store per device, and at most burst connects per device refilling
// perMinute a minute. Call it before Start. A non-positive bound is refused by the fixed
// configuration before this is reached; here it keeps the default.
func (c *CalloutResponder) WithSessionBounds(store sessionDiscriminatorStore, maxSessions, burst, perMinute int) *CalloutResponder {
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessionsPerDevice
	}
	if burst <= 0 {
		burst = DefaultConnectBurstPerDevice
	}
	if perMinute <= 0 {
		perMinute = DefaultConnectsPerMinutePerDevice
	}
	c.sessions = &sessionBound{store: store, max: maxSessions, retention: kv.MqttSessionDiscriminatorTTL}
	c.connects = newConnectLimiter(burst, perMinute)
	return c
}

// SessionBounds reports the bounds in force, for a wiring test: zero when they are off.
func (c *CalloutResponder) SessionBounds() (maxSessions, burst, perMinute int) {
	if c.sessions != nil {
		maxSessions = c.sessions.max
	}
	if c.connects != nil {
		burst = int(c.connects.burst)
		perMinute = int(c.connects.perSecond*60 + 0.5)
	}
	return maxSessions, burst, perMinute
}

// countSessionLimited counts one refusal by a session bound. A responder built without metrics
// counts nothing.
func (c *CalloutResponder) countSessionLimited(reason string) {
	if c.metrics != nil && c.metrics.SessionLimited != nil {
		c.metrics.SessionLimited.WithLabelValues(reason).Inc()
	}
}
