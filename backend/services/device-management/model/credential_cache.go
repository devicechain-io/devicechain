// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"strconv"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
)

// CredentialCacheName labels the credential cache's metrics and names its eviction
// broadcast. It is not a key-value bucket and is deliberately absent from kv.All: an entry
// holds secrets (see Caches.Credentials).
const CredentialCacheName = messaging.DeviceCredentialCacheName

// CredentialCacheTTL is how long a verified credential is kept unless the cache is built
// WithCredentialCacheTTL: config.DefaultInMemoryCacheTtlSeconds, the same number that bounds
// every other in-process copy this service holds (inMemoryCache.ttlSeconds). The clock
// starts when the database read that produced the entry STARTED, not when the entry was
// stored, so a read that began before a change and finished after it cannot keep the old
// credential past the change by more than this. A hit never extends it (see
// messaging.DefaultLocalCacheTTL for why a sliding TTL would let a hot key outlive every
// eviction).
//
// The time is a BACKSTOP. A change to a credential or its device evicts the entry on every
// replica, and a replica that reconnects to the broker drops all of them; the time is what
// bounds an eviction message that was lost anyway.
const CredentialCacheTTL = time.Duration(config.DefaultInMemoryCacheTtlSeconds) * time.Second

// CredentialCacheMaxEntries bounds one replica's credential cache by count. The byte bound
// (inMemoryCache.credentialCacheMiB) binds first: an entry is about a kilobyte before its
// device's metadata, and what the bytes cost against the service's memory limit is set out
// at config.DefaultPerDeviceCacheMiB.
const CredentialCacheMaxEntries = 262144

// credentialEntryOverhead is what one held entry costs on the heap beyond the strings and
// metadata credentialEntrySize counts: the entry struct, its list element, its slot in
// each of the two maps at their emptiest, and its key's place in the device index.
// TestCredentialEntryOverheadCoversTheStructs holds it at or above that arithmetic,
// computed from the running Go's own struct sizes.
const credentialEntryOverhead = 1024

// credentialKey is the hash of what a device presented. The raw identity is never a map
// key and is not stored at all: the id of an ACCESS_TOKEN IS the bearer secret.
type credentialKey [sha256.Size]byte

// credentialCacheKey hashes (tenant, type, id) with NUL separators.
func credentialCacheKey(tenant, credentialType, credentialId string) credentialKey {
	h := sha256.New()
	h.Write([]byte(tenant))
	h.Write([]byte{0})
	h.Write([]byte(credentialType))
	h.Write([]byte{0})
	h.Write([]byte(credentialId))
	var k credentialKey
	h.Sum(k[:0])
	return k
}

// deviceKey is a device row, by tenant: what an eviction names.
type deviceKey struct {
	tenant   string
	deviceId uint
}

type credentialEntry struct {
	key credentialKey
	dev deviceKey
	// cred holds only what a hit reads: TenantId, DeviceId, CredentialType,
	// CredentialValue, ExpiresAt, and Enabled (always true). Device is nil here.
	cred DeviceCredential
	// device is a private copy: DeviceType nil, Metadata bytes its own.
	device  Device
	expires time.Time
	size    int
}

// CredentialCache keeps a device credential that has JUST VERIFIED, with its device, in
// process memory for CredentialCacheTTL, so the device's next events are checked without a
// database read. It is in front of AuthenticateDevice only (see CachedApi.AuthenticateDevice).
//
// 🔑 IT HOLDS WHAT A DATABASE READ RETURNED, NOT A VERDICT. Every hit is checked again
// exactly as a row read from the database is — expiry, then the constant-time secret
// compare — so a wrong password is refused from memory as it is from the database, and an
// expiry takes effect at its time. A failure is never stored.
//
// 🔑 EVERY CHANGE EVICTS BY DEVICE. Each write that can change a credential or the device
// it resolves to names the owning device ids after it commits; this replica drops their
// entries before the write returns, and the eviction broadcast tells every other replica
// to drop theirs. A lost message is bounded by the TTL.
type CredentialCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	maxBytes   int
	bytes      int
	// gen is bumped by every eviction. A miss reads it before going to the database and
	// fill drops the result if it moved, so a read that was in flight across a change
	// cannot put the credential from before the change back.
	//
	// It is one counter for the whole cache, not one per device or tenant, and that is
	// accepted: any eviction, from any tenant and including this replica's own broadcast
	// coming back, drops every fill in flight at that moment. Those reads still return
	// their answer; only the copy is skipped, and the next event costs one more read. That
	// is hit rate lost, never a wrong answer.
	gen      uint64
	order    *list.List // front = most recently used; elements hold *credentialEntry
	byKey    map[credentialKey]*list.Element
	byDevice map[deviceKey][]credentialKey
	now      func() time.Time

	metrics   *credentialCacheMetrics      // nil: nothing is counted (unit fixtures)
	broadcast *messaging.EvictionBroadcast // nil: evictions stay on this replica (unit fixtures)
}

// CredentialCacheOption adjusts a CredentialCache when it is built.
type CredentialCacheOption func(*CredentialCache)

// NewCredentialCache builds an empty cache. It panics on a bound of zero or less, as
// messaging.WithLocalBounds does, so a computed zero cannot shrink it to nothing silently.
func NewCredentialCache(maxEntries, maxBytes int, opts ...CredentialCacheOption) *CredentialCache {
	if maxEntries <= 0 || maxBytes <= 0 {
		panic("model: NewCredentialCache needs a positive entry count and byte count")
	}
	c := &CredentialCache{
		ttl:        CredentialCacheTTL,
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		order:      list.New(),
		byKey:      map[credentialKey]*list.Element{},
		byDevice:   map[deviceKey][]credentialKey{},
		now:        time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	c.metrics.bounds(maxEntries, maxBytes)
	c.report()
	return c
}

// WithCredentialCacheMetrics exports the cache's lookups, evictions and size through ms.
func WithCredentialCacheMetrics(ms *core.Microservice) CredentialCacheOption {
	return func(c *CredentialCache) { c.metrics = newCredentialCacheMetrics(ms) }
}

// WithCredentialCacheTTL sets how long an entry is kept, in place of CredentialCacheTTL. It
// panics on a duration of zero or less, as messaging.WithLocalTTL does.
func WithCredentialCacheTTL(d time.Duration) CredentialCacheOption {
	if d <= 0 {
		panic("model: WithCredentialCacheTTL needs a positive duration")
	}
	return func(c *CredentialCache) { c.ttl = d }
}

// WithCredentialEvictionBroadcast makes every eviction reach the other replicas through b.
func WithCredentialEvictionBroadcast(b *messaging.EvictionBroadcast) CredentialCacheOption {
	return func(c *CredentialCache) { c.broadcast = b }
}

// withCredentialCacheClock replaces the cache's clock, for tests.
func withCredentialCacheClock(now func() time.Time) CredentialCacheOption {
	return func(c *CredentialCache) { c.now = now }
}

// lookup returns a COPY of the credential held under key for tenant, whose Device points
// at a fresh copy of the held device (its Metadata bytes copied too), so nothing a caller
// does to what it gets reaches the next hit. A hit moves recency only. An expired entry is
// removed and reported as a miss, and so is an entry of another tenant, which the key
// already rules out; the comparison is one line and fails closed if the key ever stops
// including the tenant.
func (c *CredentialCache) lookup(tenant string, key credentialKey) (DeviceCredential, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[key]
	if !ok {
		c.metrics.lookup(false)
		return DeviceCredential{}, false
	}
	e := el.Value.(*credentialEntry)
	if !c.now().Before(e.expires) {
		c.removeLocked(el, "expired")
		c.report()
		c.metrics.lookup(false)
		return DeviceCredential{}, false
	}
	if e.cred.TenantId != tenant {
		c.metrics.lookup(false)
		return DeviceCredential{}, false
	}
	c.order.MoveToFront(el)
	c.metrics.lookup(true)
	cred := e.cred
	device := cloneDevice(&e.device)
	cred.Device = &device
	return cred, true
}

// readStarted is taken BEFORE the database read whose result fill stores: the generation
// that read must still see, and the time its entry's TTL counts from.
func (c *CredentialCache) readStarted() (gen uint64, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen, c.now()
}

// fill stores a credential that has JUST PASSED evaluateCredential and credentialDevice,
// read by a database read that started at readAt with generation gen, unless:
//
//   - an eviction ran while it was being read (gen moved): it may be the row from before
//     the change;
//   - its type is not the one presented. A row read through deviceCredentialForConnect
//     carries an EMPTY type, and evaluateCredential would then skip the secret compare on
//     every hit. That refusal makes the danger unreachable, not merely unused;
//   - it is not enabled, or carries no device: no row the per-event lookup returns is
//     either, so a row that is was not read by it. Nothing reaches this refusal today; it
//     is belt-and-braces against a finder that one day does, and a test pins it;
//   - it has already outlived the TTL counted from readAt;
//   - it is larger than the byte bound on its own, in which case the entry it would have
//     replaced goes too, so an older copy is never left in its place.
//
// Storing sweeps expired entries from the least recently used end, and then evicts for
// capacity, as core's in-process cache does.
func (c *CredentialCache) fill(key credentialKey, presentedType string, cred *DeviceCredential, gen uint64, readAt time.Time) {
	if cred == nil || cred.Device == nil || !cred.Enabled || cred.CredentialType != presentedType {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	now := c.now()
	expires := readAt.Add(c.ttl)
	if el, ok := c.byKey[key]; ok {
		c.removeLocked(el, "")
	}
	defer c.report()
	if !now.Before(expires) {
		return
	}
	e := &credentialEntry{
		key: key,
		dev: deviceKey{tenant: cred.TenantId, deviceId: cred.DeviceId},
		cred: DeviceCredential{
			DeviceId:        cred.DeviceId,
			CredentialType:  cred.CredentialType,
			CredentialValue: cred.CredentialValue,
			Enabled:         true,
			ExpiresAt:       cred.ExpiresAt,
		},
		device:  cloneDevice(cred.Device),
		expires: expires,
	}
	e.cred.TenantId = cred.TenantId
	e.size = credentialEntrySize(e)
	if e.size > c.maxBytes {
		return
	}
	c.byKey[key] = c.order.PushFront(e)
	c.byDevice[e.dev] = append(c.byDevice[e.dev], key)
	c.bytes += e.size
	for c.order.Len() > c.maxEntries || c.bytes > c.maxBytes {
		c.removeLocked(c.order.Back(), "capacity")
	}
	for back := c.order.Back(); back != nil && !now.Before(back.Value.(*credentialEntry).expires); back = c.order.Back() {
		c.removeLocked(back, "expired")
	}
}

// EvictDevices drops every entry of tenant filed under any of deviceIds, on this replica
// only, and moves the generation on even when nothing was held, because a read for one of
// them may be in flight.
func (c *CredentialCache) EvictDevices(tenant string, deviceIds []uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for _, id := range deviceIds {
		dev := deviceKey{tenant: tenant, deviceId: id}
		// removeLocked rewrites byDevice[dev], so walk a copy.
		for _, key := range append([]credentialKey(nil), c.byDevice[dev]...) {
			if el, ok := c.byKey[key]; ok {
				c.removeLocked(el, "revoked")
			}
		}
	}
	c.report()
}

// EvictTenant drops every entry of tenant, on this replica only, and moves the generation on.
func (c *CredentialCache) EvictTenant(tenant string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for dev := range c.byDevice {
		if dev.tenant != tenant {
			continue
		}
		for _, key := range append([]credentialKey(nil), c.byDevice[dev]...) {
			if el, ok := c.byKey[key]; ok {
				c.removeLocked(el, "revoked")
			}
		}
	}
	c.report()
}

// Clear drops every entry and moves the generation on. A replica does it when its
// connection to the broker comes back, since it may have missed eviction messages while it
// was away and cannot tell which entries they named.
func (c *CredentialCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for el := c.order.Front(); el != nil; el = c.order.Front() {
		c.removeLocked(el, "revoked")
	}
	c.report()
}

// ApplyEviction is the eviction broadcast's apply function: an eviction another replica
// (or this one) sent. Each key is a device row id in decimal. A key that does not parse is
// skipped, and the rest still apply. A tenant-wide eviction (a tenant's erasure) drops every
// entry of the tenant.
func (c *CredentialCache) ApplyEviction(e messaging.CacheEviction) {
	if e.All {
		c.EvictTenant(e.Tenant)
	}
	ids := make([]uint, 0, len(e.Keys))
	for _, k := range e.Keys {
		id, err := strconv.ParseUint(k, 10, strconv.IntSize)
		if err != nil || id == 0 {
			log.Warn().Str("cache", CredentialCacheName).Str("tenant", e.Tenant).
				Msg("A credential cache eviction named a device that is not a row id; it was skipped.")
			continue
		}
		ids = append(ids, uint(id))
	}
	c.EvictDevices(e.Tenant, ids)
}

// publishEviction tells the other replicas to drop the entries of the given devices, when
// a broadcast is wired. It never returns an error, because the write it follows has
// committed: it logs one, and the TTL bounds what the lost message would have evicted.
func (c *CredentialCache) publishEviction(tenant string, deviceIds []uint) {
	if c.broadcast == nil || len(deviceIds) == 0 {
		return
	}
	keys := make([]string, len(deviceIds))
	for i, id := range deviceIds {
		keys[i] = strconv.FormatUint(uint64(id), 10)
	}
	if err := c.broadcast.Publish(tenant, keys); err != nil {
		log.Warn().Err(err).Str("tenant", tenant).
			Msg("A credential cache eviction could not be broadcast; other replicas drop the entry when it expires.")
	}
}

// len reports the entries and accounted bytes held, expired entries not yet removed
// included.
func (c *CredentialCache) len() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len(), c.bytes
}

// removeLocked drops one entry from all three structures. reason is the eviction label, or
// "" for a replacement, which is not an eviction. Called with mu held.
func (c *CredentialCache) removeLocked(el *list.Element, reason string) {
	e := el.Value.(*credentialEntry)
	c.order.Remove(el)
	delete(c.byKey, e.key)
	keys := c.byDevice[e.dev]
	for i, k := range keys {
		if k == e.key {
			keys = append(keys[:i], keys[i+1:]...)
			break
		}
	}
	if len(keys) == 0 {
		delete(c.byDevice, e.dev)
	} else {
		c.byDevice[e.dev] = keys
	}
	c.bytes -= e.size
	if reason != "" {
		c.metrics.evicted(reason)
	}
}

// report publishes the size gauges. Called with mu held (or before the cache is shared).
func (c *CredentialCache) report() {
	c.metrics.size(c.order.Len(), c.bytes)
}

// credentialEntrySize is what an entry is charged against the byte bound: the fixed
// overhead plus every string and the metadata it holds. Metadata is bounded only by the
// request body limit of the API that writes it, which is why the byte bound and not the
// entry count is what binds.
func credentialEntrySize(e *credentialEntry) int {
	n := credentialEntryOverhead + len(e.dev.tenant) +
		len(e.cred.TenantId) + len(e.cred.CredentialType) + len(e.cred.CredentialValue.String) +
		len(e.device.TenantId) + len(e.device.Token) + len(e.device.ExternalId.String) +
		len(e.device.Name.String) + len(e.device.Description.String)
	if e.device.Metadata != nil {
		n += cap(*e.device.Metadata)
	}
	return n
}

// cloneDevice copies a device so that nothing done to the copy reaches the original: its
// Metadata bytes are copied and its DeviceType dropped, as the per-event lookup's JOIN
// never loads one.
func cloneDevice(d *Device) Device {
	c := *d
	c.DeviceType = nil
	if d.Metadata != nil {
		m := datatypes.JSON(bytes.Clone(*d.Metadata))
		c.Metadata = &m
	}
	return c
}

// credentialCacheMetrics exports one CredentialCache. Every method is a no-op on a nil
// receiver.
type credentialCacheMetrics struct {
	hit, miss  prometheus.Counter
	evictions  *prometheus.CounterVec
	entries    prometheus.Gauge
	bytes      prometheus.Gauge
	maxEntries prometheus.Gauge
	maxBytes   prometheus.Gauge
}

func newCredentialCacheMetrics(ms *core.Microservice) *credentialCacheMetrics {
	lookups := ms.NewCounterVec("credential_cache_lookups_total",
		"Device credential checks answered from this replica's memory (result=hit) or passed on to the "+
			"database (result=miss). A credential is kept for up to its time to live (inMemoryCache.ttlSeconds) after the read that verified it.",
		[]string{"result"})
	m := &credentialCacheMetrics{
		hit:  lookups.WithLabelValues("hit"),
		miss: lookups.WithLabelValues("miss"),
		evictions: ms.NewCounterVec("credential_cache_evictions_total",
			"Credentials removed from this replica's memory: reason=expired (found past its time to live), capacity "+
				"(the cache was full), revoked (dropped after a change to the credential or its device, made "+
				"on this replica or announced by another).",
			[]string{"reason"}),
		entries: ms.NewGauge("credential_cache_entries",
			"Device credentials held in this replica's memory, including expired ones not yet removed."),
		bytes: ms.NewGauge("credential_cache_bytes",
			"Approximate bytes the credentials held in this replica's memory take, including expired ones not yet removed."),
		maxEntries: ms.NewGauge("credential_cache_max_entries",
			"The most device credentials this replica keeps in memory before it drops the least recently used."),
		maxBytes: ms.NewGauge("credential_cache_max_bytes",
			"The most bytes, counted as credential_cache_bytes counts them, this replica keeps in memory."),
	}
	m.hit.Add(0)
	m.miss.Add(0)
	for _, r := range []string{"expired", "capacity", "revoked"} {
		m.evictions.WithLabelValues(r).Add(0)
	}
	return m
}

func (m *credentialCacheMetrics) lookup(hit bool) {
	if m == nil {
		return
	}
	if hit {
		m.hit.Inc()
	} else {
		m.miss.Inc()
	}
}

func (m *credentialCacheMetrics) evicted(reason string) {
	if m == nil {
		return
	}
	m.evictions.WithLabelValues(reason).Inc()
}

func (m *credentialCacheMetrics) size(entries, bytes int) {
	if m == nil {
		return
	}
	m.entries.Set(float64(entries))
	m.bytes.Set(float64(bytes))
}

func (m *credentialCacheMetrics) bounds(maxEntries, maxBytes int) {
	if m == nil {
		return
	}
	m.maxEntries.Set(float64(maxEntries))
	m.maxBytes.Set(float64(maxBytes))
}
