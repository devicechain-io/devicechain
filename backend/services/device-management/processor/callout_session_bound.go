// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// Bounding the sessions one device can hold at the broker.
//
// A device may append a discriminator of its own to its MQTT client id (messaging.DeviceClientID
// says why), and every distinct client id is a session of its own. A persistent session leaves a
// record in the broker's session store, which is unbounded on purpose (a ceiling there would evict
// live sessions at random) and which shares the file store with every platform stream. The format
// is part of the device wire and is not narrowed here, and a device is never refused for using one
// id too many. Instead:
//
//   - each device's suffixed client ids are recorded (at most callout.maxSessionsPerDevice), and a
//     NEW id past the count first EVICTS the session of the id the device used least recently
//     (messaging.MqttSessionEvictor deletes its consumers and record, as the broker's own session
//     clear does), then takes its place. So a device holds at most that many sessions it opened
//     after this release, whatever it does, and a device that changes suffixes keeps working;
//   - a slot is released ONLY by evicting the session it counts. Nothing ages out by time, so no
//     slot can come back while its session still exists;
//   - and a per-device token bucket bounds how often one device may connect at all.
//
// The authorization request does not say whether the device asked for a clean session, so every
// suffixed id is counted; for a clean session the eviction simply finds no record to remove.
//
// 🔴 KNOWN LIMITS. Sessions a device left before this release are not recorded, so they are not
// counted or evicted (the broker exporter's nats_stream_total_messages{stream_name="$MQTT_sess"}
// shows them). And a session still connected when it is evicted keeps its connection; if it
// subscribes again the broker writes its record anew, uncounted. Only the least recently seen id
// is evicted, so that takes a device running more than the count of ids AT ONCE.
//
// The bucket holds no identifier (see sessionIndexKey), so a tenant purge has nothing in it to find.

// Defaults for the bounds. config.Default* mirror them, kept equal by a test.
const (
	DefaultMaxSessionsPerDevice       = 16
	DefaultConnectBurstPerDevice      = 20
	DefaultConnectsPerMinutePerDevice = 60
)

// Session-bound refusal reasons, the label values of callout_session_limited_total.
const (
	sessionLimitedConnectRate      = "connect_rate"
	sessionLimitedStoreUnavailable = "session_store_unavailable"
	sessionLimitedEvictFailed      = "session_evict_failed"
)

// sessionLimitedReasons is the closed vocabulary, every member present at zero from startup.
var sessionLimitedReasons = []string{sessionLimitedConnectRate, sessionLimitedStoreUnavailable, sessionLimitedEvictFailed}

// sessionDiscriminatorStore is the part of jetstream.KeyValue the session bound uses. Every call
// takes the connect's context. jetstream.KeyValue satisfies it; a test substitutes a map.
type sessionDiscriminatorStore interface {
	Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error)
	Create(ctx context.Context, key string, value []byte, opts ...jetstream.KVCreateOpt) (uint64, error)
	Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error)
}

// sessionEvictor removes one persistent session by the broker's digest of its client id.
// *messaging.MqttSessionEvictor satisfies it.
type sessionEvictor interface {
	Evict(ctx context.Context, idHash string, owned func(clientID string) bool) (bool, error)
}

// sessionDiscriminatorRefresh is how stale a known id's last-seen time may get before a connect
// with it rewrites the entry. Last-seen decides only which id is evicted first, so a day is fine
// and spares a write on every reconnect.
const sessionDiscriminatorRefresh = 24 * time.Hour

// sessionDiscriminatorWriteAttempts bounds the compare-and-set retries when two replicas record a
// new id for the same device at once.
const sessionDiscriminatorWriteAttempts = 4

// SessionIndexKey turns identifiers into what the session bucket stores, so the bucket holds none:
//
//   - the entry key is a MAC of the tenant and device;
//   - each id is recorded as a MAC of tenant, device and discriminator;
//   - the broker's digest of the full client id (which is what an eviction needs, and which is
//     an unkeyed hash of a guessable string) is sealed with AES-GCM, bound to the entry key.
//
// The key is derived from the instance root key, which user-management's purge does not hold. That
// is why the bucket is a purge exemption: an entry cannot be attributed to a tenant by anyone but
// this service, and once the tenant's broker sessions are erased it names nothing that exists.
type SessionIndexKey struct {
	mac  []byte
	aead cipher.AEAD
}

// sessionIndexInfo labels the derivation, so this key is never another key derived from the root.
const sessionIndexInfo = "devicechain device-management mqtt-session-index v1"

// NewSessionIndexKey derives the session index key from the instance root key.
func NewSessionIndexKey(rootKey []byte) (*SessionIndexKey, error) {
	if len(rootKey) < 32 {
		return nil, errors.New("the MQTT session index key needs an instance root key of at least 32 bytes")
	}
	okm, err := hkdf.Key(sha256.New, rootKey, nil, sessionIndexInfo, 64)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(okm[32:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SessionIndexKey{mac: okm[:32], aead: aead}, nil
}

func (k *SessionIndexKey) sum(parts ...string) string {
	m := hmac.New(sha256.New, k.mac)
	for _, p := range parts {
		// Length-prefixed, so ("ab","c") and ("a","bc") differ.
		fmt.Fprintf(m, "%d:%s;", len(p), p)
	}
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:18])
}

func (k *SessionIndexKey) entryKey(tenant, device string) string {
	return k.sum("entry", tenant, device)
}

func (k *SessionIndexKey) idDigest(tenant, device, discriminator string) string {
	return k.sum("id", tenant, device, discriminator)
}

func (k *SessionIndexKey) seal(entryKey, idHash string) (string, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(k.aead.Seal(nonce, nonce, []byte(idHash), []byte(entryKey))), nil
}

func (k *SessionIndexKey) open(entryKey, sealed string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < k.aead.NonceSize() {
		return "", errors.New("malformed sealed session digest")
	}
	n := k.aead.NonceSize()
	plain, err := k.aead.Open(nil, raw[:n], raw[n:], []byte(entryKey))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// sessionSlot is one recorded id: the broker's session digest, sealed, and when it was last seen.
type sessionSlot struct {
	Sealed string `json:"h"`
	Seen   int64  `json:"t"`
}

// sessionDiscriminators is the value of one device's entry, keyed by idDigest.
type sessionDiscriminators struct {
	Slots map[string]sessionSlot `json:"s"`
}

// sessionBound is the per-device session cap with eviction.
type sessionBound struct {
	store   sessionDiscriminatorStore
	key     *SessionIndexKey
	evictor sessionEvictor
	max     int
	// evicted counts sessions evicted to make room; may be nil.
	evicted prometheus.Counter
}

// clientIDDiscriminator returns what follows the device's own plain client id, separator
// included, so every id but the plain one is a non-empty discriminator ("{plain}:" is ":"). It is
// "" only for the plain id itself. presented has already passed DeviceClientIDMatches.
func clientIDDiscriminator(presented, required string) string {
	return strings.TrimPrefix(presented, required)
}

// admit decides a connect whose client id is required+discriminator. It returns "" to admit, or
// the refusal reason. A device is never refused for the number of ids it uses; it is refused only
// when the bound cannot be kept (the bucket cannot be read or written, an eviction failed, or the
// connect's deadline ran out first).
//
//   - The plain client id ("") is never counted and costs no store round trip.
//   - A recorded id is admitted; its last-seen time is rewritten at most daily, best effort.
//   - A new id is recorded; at the count, the least recently seen ids are evicted first (their
//     sessions removed at the broker, then their slots dropped) until there is room.
//   - Nothing is written once ctx is done, so a connect the broker has already given up on does
//     not take a slot.
func (b *sessionBound) admit(ctx context.Context, tenant, deviceToken, required, discriminator string, now time.Time) string {
	if discriminator == "" {
		return ""
	}
	key := b.key.entryKey(tenant, deviceToken)
	id := b.key.idDigest(tenant, deviceToken, discriminator)
	owned := func(clientID string) bool {
		return clientID != required && messaging.DeviceClientIDMatches(clientID, required)
	}
	for attempt := 0; attempt < sessionDiscriminatorWriteAttempts; attempt++ {
		if ctx.Err() != nil {
			return sessionLimitedStoreUnavailable
		}
		entry, err := b.store.Get(ctx, key)
		var rev uint64
		set := sessionDiscriminators{Slots: map[string]sessionSlot{}}
		switch {
		case errors.Is(err, jetstream.ErrKeyNotFound):
		case err != nil:
			log.Warn().Err(err).Str("tenant", tenant).Str("device", deviceToken).
				Msg("Auth-callout could not read a device's MQTT session record; refusing a connect with a new client id.")
			return sessionLimitedStoreUnavailable
		default:
			rev = entry.Revision()
			if err := json.Unmarshal(entry.Value(), &set); err != nil || set.Slots == nil {
				// 🔴 NOT reset: the entry is what counts the device's sessions, and starting it
				// again would release every slot without removing a session. Only this code writes
				// it, so this is corruption; the device's plain id still connects.
				log.Error().Err(err).Str("tenant", tenant).Str("device", deviceToken).
					Msg("Auth-callout found an unreadable MQTT session record; refusing a connect with a new client id.")
				return sessionLimitedStoreUnavailable
			}
		}

		if slot, known := set.Slots[id]; known {
			if now.Unix()-slot.Seen >= int64(sessionDiscriminatorRefresh/time.Second) && ctx.Err() == nil {
				slot.Seen = now.Unix()
				set.Slots[id] = slot
				_ = b.write(ctx, key, set, rev) // best effort: a recorded id is admitted either way
			}
			return ""
		}

		for len(set.Slots) >= b.max {
			victim, slot := oldestSlot(set.Slots)
			idHash, err := b.key.open(key, slot.Sealed)
			if err != nil {
				log.Error().Err(err).Str("tenant", tenant).Str("device", deviceToken).
					Msg("Auth-callout could not open a recorded MQTT session digest; refusing a connect with a new client id.")
				return sessionLimitedEvictFailed
			}
			removed, err := b.evictor.Evict(ctx, idHash, owned)
			if err != nil {
				log.Warn().Err(err).Str("tenant", tenant).Str("device", deviceToken).
					Msg("Auth-callout could not evict a device's least recently used MQTT session; refusing a connect with a new client id.")
				return sessionLimitedEvictFailed
			}
			if removed && b.evicted != nil {
				b.evicted.Inc()
			}
			log.Debug().Str("tenant", tenant).Str("device", deviceToken).Bool("removed", removed).
				Msg("Auth-callout evicted a device's least recently used MQTT session to admit a new client id.")
			delete(set.Slots, victim)
		}

		sealed, err := b.key.seal(key, messaging.MqttSessionClientIDHash(required+discriminator))
		if err != nil {
			return sessionLimitedStoreUnavailable
		}
		set.Slots[id] = sessionSlot{Sealed: sealed, Seen: now.Unix()}
		if ctx.Err() != nil {
			return sessionLimitedStoreUnavailable
		}
		err = b.write(ctx, key, set, rev)
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

// oldestSlot is the least recently seen slot, ties broken by digest so every replica picks the
// same one.
func oldestSlot(slots map[string]sessionSlot) (string, sessionSlot) {
	ids := make([]string, 0, len(slots))
	for id := range slots {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := slots[ids[i]], slots[ids[j]]
		if a.Seen != b.Seen {
			return a.Seen < b.Seen
		}
		return ids[i] < ids[j]
	})
	return ids[0], slots[ids[0]]
}

// write stores set as the entry's next revision: a create when there was none (rev 0), an update
// of exactly rev otherwise.
func (b *sessionBound) write(ctx context.Context, key string, set sessionDiscriminators, rev uint64) error {
	data, err := json.Marshal(set)
	if err != nil {
		return err
	}
	if rev == 0 {
		_, err = b.store.Create(ctx, key, data)
	} else {
		_, err = b.store.Update(ctx, key, data, rev)
	}
	return err
}

// isRevisionConflict reports whether a write lost a compare-and-set to a concurrent one.
func isRevisionConflict(err error) bool {
	if errors.Is(err, jetstream.ErrKeyExists) || errors.Is(err, jetstream.ErrKeyRevisionMismatch) {
		return true
	}
	var apiErr *jetstream.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence ||
		apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequenceConstant)
}

// connectLimiter is the per-device connect-rate limit: a token bucket per (tenant, device), per
// replica, holding at most burst connects and refilling perMinute a minute. A bucket that has
// refilled is the same as no bucket, so those are swept.
//
// 🔑 IT HOLDS AT MOST maxDevices BUCKETS, AND PAST THAT IT FAILS OPEN. When the map is full and a
// sweep frees nothing, a device with no bucket is admitted without one (and counted). Fail-open is
// the right side here because the limiter is load protection, not the bound: the session count is
// bounded by the eviction cap whatever the rate, and only an authenticated device ever gets a
// bucket, so filling the map takes that many distinct devices connecting inside one refill. Failing
// closed would refuse every new device of a fleet reconnecting all at once.
type connectLimiter struct {
	burst      float64
	perSecond  float64
	maxDevices int

	mu        sync.Mutex
	buckets   map[string]*connectBucket
	lastSweep time.Time
	// saturated counts connects admitted untracked because the map was full; may be nil.
	saturated prometheus.Counter
}

type connectBucket struct {
	tokens float64
	at     time.Time
}

// connectLimiterSweepEvery is how often refilled buckets are dropped, and
// connectLimiterFullSweepEvery how often a full map may force an extra sweep.
const (
	connectLimiterSweepEvery     = time.Minute
	connectLimiterFullSweepEvery = time.Second
	// DefaultConnectLimiterMaxDevices bounds the buckets one replica holds (about 100 bytes each).
	DefaultConnectLimiterMaxDevices = 65536
)

func newConnectLimiter(burst, perMinute, maxDevices int) *connectLimiter {
	return &connectLimiter{
		burst:      float64(burst),
		perSecond:  float64(perMinute) / 60,
		maxDevices: maxDevices,
		buckets:    map[string]*connectBucket{},
	}
}

func (l *connectLimiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if l.level(b, now) >= l.burst {
			delete(l.buckets, k)
		}
	}
	l.lastSweep = now
}

// allow charges one connect to the device and reports whether it is within the limit.
func (l *connectLimiter) allow(tenant, deviceToken string, now time.Time) bool {
	key := tenant + "|" + deviceToken
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= connectLimiterSweepEvery {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxDevices && now.Sub(l.lastSweep) >= connectLimiterFullSweepEvery {
			l.sweep(now)
		}
		if len(l.buckets) >= l.maxDevices {
			if l.saturated != nil {
				l.saturated.Inc()
			}
			return true
		}
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

// WithSessionBounds turns on the per-device bounds: at most maxSessions suffixed client ids
// recorded in store per device, evicting the least recently seen one's session through evictor to
// admit another, and at most burst connects per device refilling perMinute a minute. Call it
// before Start. A non-positive bound keeps the default (the fixed configuration refuses one).
func (c *CalloutResponder) WithSessionBounds(store sessionDiscriminatorStore, key *SessionIndexKey, evictor sessionEvictor,
	maxSessions, burst, perMinute int) *CalloutResponder {
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessionsPerDevice
	}
	if burst <= 0 {
		burst = DefaultConnectBurstPerDevice
	}
	if perMinute <= 0 {
		perMinute = DefaultConnectsPerMinutePerDevice
	}
	c.sessions = &sessionBound{store: store, key: key, evictor: evictor, max: maxSessions}
	c.connects = newConnectLimiter(burst, perMinute, DefaultConnectLimiterMaxDevices)
	if c.metrics != nil {
		c.sessions.evicted = c.metrics.SessionsEvicted
		c.connects.saturated = c.metrics.ConnectLimiterSaturated
	}
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
