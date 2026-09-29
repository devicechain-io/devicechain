// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Device authentication policy applied to inbound events (transport security,
// ADR-014).
const (
	// AuthModeDisabled performs no authentication: the self-asserted device token
	// on the event is trusted. Appropriate only when the transport itself is
	// trusted (e.g. a broker that already authenticated the device).
	AuthModeDisabled = "disabled"
	// AuthModeOptional authenticates when a credential is presented (rejecting
	// bad credentials) but allows events that present none, falling back to the
	// device token. It is an explicit opt-out for trusted/bootstrapping transports;
	// it is not a secure posture for untrusted ones.
	AuthModeOptional = "optional"
	// AuthModeRequired rejects any event that does not present a valid credential.
	// This is the default, hardened posture: paired with the ADR-025 broker
	// auth-callout (which authenticates the connection), requiring a per-event
	// credential also closes intra-tenant spoofing — an authenticated device cannot
	// publish an event under another device's self-asserted token.
	AuthModeRequired = "required"
)

// Defaults for the hot-path resolution caches (ADR-022 review B2). A short TTL
// bounds staleness for entries that change rarely.
//
// The caches are NATS JetStream KV buckets (ADR-007), so the size of the BUCKETS is a
// server-side platform concern rather than a per-service one: each bucket carries a
// byte ceiling from the instance config's cache tier (kv.All / ADR-023), which is why no
// bucket size is configured here. The TTL still matters to that budget even so — it is
// what bounds the working set, since a bucket only ever holds entries that have not yet
// expired. What each replica keeps of them in its OWN memory is sized separately, by
// InMemoryCacheConfiguration.
// DefaultMaxEventFutureSkewSeconds bounds how far a device-reported occurred time may
// lead the server-stamped processed time before resolution replaces it with the ceiling.
// Generous enough for legitimate device/server clock drift, and small enough that a
// device cannot poison a shared, strictly-newer projection with a timestamp years out.
//
// It lives in device-management because resolution is the ONE place the platform decides
// what instant a reading happened at — the resolved event then travels with an already-
// bounded time, so no consumer configures or re-applies this (see core/eventtime).
const DefaultMaxEventFutureSkewSeconds = 300

// DefaultResolutionWorkers is how many inbound events are resolved at once when
// resolution.workers is not set.
//
// A warm event makes its lookups in two steps — the credential (one database read, for
// every event that carries one), then the profile, the tracked relationships and whether
// any scoped group exists, read from the message broker's key-value store at the same
// time — so a resolver spends most of each event waiting for replies, not using CPU. The
// measurements below were taken when those three reads were still made one after another.
// On a kind cluster five resolvers topped out at about 1600 events a second on 1.5 of the
// pod's 4 cores, with the events above that rate queued in front of them.
//
// Measured in-process (BenchmarkInboundStageOccupancy: a three-server broker, credentialed
// events, each lookup answering after 750µs, which puts five resolvers near the kind
// ceiling): 5 resolvers resolved about 1500 events a second, 8 about 2300, 10 about 2900
// and 16 about 4600, the resolvers busy throughout in every arm. The count is capped at 10
// rather than taken from the widest arm because each resolver holds a pooled connection
// while it authenticates an event, and 10 is half of the default pool of 20, the most
// rdb.CheckWriterCount accepts without a warning. On a real PostgreSQL the credential read
// did not wait for a connection at 10 (BenchmarkAuthenticateDeviceConcurrency).
const DefaultResolutionWorkers = 10

// DefaultPerDeviceCacheEntries and DefaultPerDeviceCacheMiB bound what one replica keeps
// in memory of EACH of the three caches keyed by device: a device by its token, a device's
// tracked relationships, and an entity's group memberships (keyed by device, and by each
// area or asset a device is tracked to). The two caches keyed by device type and by tenant
// keep messaging's 4,096 entries and 4 MiB; there are few of those keys.
//
// What an entry costs, as the cache counts it (key, the value's allocation, and 192 B for
// the list element, entry and map slot, which the messaging package measured and holds
// with a test): a device with no tracked relationship is about 290 B in the relationships
// cache (a 72-byte value), and one with a single tracked relationship about 950 B (a value
// of about 650 to 700 bytes). So 24 MiB holds about 87,000 unassigned devices or 26,000
// singly assigned ones, and the entry bound is set above either so that memory, not the
// count, is what binds.
//
// 🔑 A CACHE ENTRY LIVES 5 S, SO THE FLEET SIZE IS NOT WHAT TO SIZE FOR. An entry expires
// 5 s after it was stored, whether or not it was read, so on one replica only a device read
// again within 5 s can be answered from memory: the working set is 5 s × that replica's
// event rate, about 35,000 at the highest rate one replica has been measured resolving
// (just under 7,000 events a second). A fleet larger than that, or reporting less often
// than every 5 s, gets nothing more from a larger bound. Below it, a cache that is full
// holds the devices that reported in the last (bound ÷ rate) seconds.
//
// 🔴 THE MEMORY BUDGET, STATED AGAINST THE LIMIT IT RUNS UNDER. At these defaults the five
// caches hold at most 3 × 24 + 2 × 4 = 80 MiB, counted as above. With no GOMEMLIMIT set
// (the chart's default), the heap can grow to about twice what is live before a
// collection: about 160 MiB for full caches, plus the rest of the service (a replica
// resolving several thousand events a second on a three-node GKE cluster used 32 MiB in
// all, its then 4-MiB caches included), about 190 MiB against device-management's 256 MiB
// memory limit. Full caches need a busy replica: three full 24-MiB caches at once take
// device-by-token being read (devices authenticated at the transport, or device auth set to
// optional or disabled) and a tenant with rule-scoped groups. Raising either setting needs
// the memory limit raised with it; MaxPerDeviceCacheMiB is well past what 256 MiB can hold.
//
// That budget is why the default is 24 MiB and not the ~33 MiB that 35,000 singly assigned
// devices would need: 32 MiB per cache would put full caches at about 240 MiB under the
// same arithmetic, too close to the limit for a default.
const (
	DefaultPerDeviceCacheEntries = 131072
	DefaultPerDeviceCacheMiB     = 24
	MaxPerDeviceCacheEntries     = 4 << 20
	MaxPerDeviceCacheMiB         = 256
)

const (
	DefaultDeviceCacheTtlSeconds       = 60
	DefaultRelationshipCacheTtlSeconds = 60
	DefaultMetricDefCacheTtlSeconds    = 60
	DefaultMembershipCacheTtlSeconds   = 60
)

type DeviceManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
	// DeviceAuthMode selects how inbound events are authenticated (one of the
	// AuthMode* constants). Empty is treated as AuthModeRequired (the hardened
	// default); relax to "optional"/"disabled" only for a trusted transport.
	DeviceAuthMode string

	// Hot inbound-event resolution path caches (ADR-022 review B2).
	// DeviceCacheTtlSeconds bounds the device-by-token cache;
	// RelationshipCacheTtlSeconds bounds the tracked-relationships-by-source-device
	// cache; MetricDefCacheTtlSeconds bounds the per-device-type profile-resolution
	// cache — the published version's metric definitions used by ingest-time metric
	// validation (ADR-016), plus the rule scope and fence-set version stamped onto every
	// event (ADR-051/078). Its name predates that cache holding more than the metric
	// definitions, and is kept because a rename would reject every existing values file.
	// All are NATS KV bucket TTLs, in seconds (ADR-007). Each also caps how long a replica
	// keeps what it read in process memory (messaging.DefaultLocalCacheTTL, 5 s), so a TTL
	// below 5 s shortens that too.
	DeviceCacheTtlSeconds       int
	RelationshipCacheTtlSeconds int
	MetricDefCacheTtlSeconds    int
	// MembershipCacheTtlSeconds bounds the per-entity dynamic-group membership cache
	// read on the hot resolve path when stamping scope memberships onto an event
	// (ADR-062). Negative results are cached (a non-member is the common case), and the
	// TTL is a self-healing backstop behind the explicit per-entity eviction on every
	// membership mutation.
	MembershipCacheTtlSeconds int

	// MaxEventFutureSkewSeconds bounds how far a device-reported occurred time may lead
	// the server-stamped processed time; a reading past that ceiling is stored AT the
	// ceiling. Unset (0) defaults to 300s; a negative value disables the bound, which
	// lets any device freeze its own presence and poison every strictly-newer projection
	// it feeds — see core/eventtime for what that costs.
	//
	// 🔴 UPGRADE ORDER MATTERS ONCE, AT THE RELEASE THAT MOVED THIS KEY HERE. The bound
	// used to be applied downstream, by the detection engine, on events it read off the
	// resolved stream; it is now applied here, before they are published. So a resolved
	// event published by an OLDER device-management and consumed by a NEWER
	// event-processing is bounded by neither — and if such an event carries a far-future
	// time it advances DETECT's shared, snapshotted watermark and fires every tenant's
	// timers, persistently. The exposure is the in-flight tail at the upgrade, or the
	// whole consumer backlog if detection was lagging; recovery is a snapshot reset.
	// Pre-GA an instance is recreated rather than upgraded in place, which is why this is
	// recorded rather than mitigated with a transitional bound.
	MaxEventFutureSkewSeconds int

	// Resolution sizes the pool that resolves inbound events.
	Resolution ResolutionConfiguration

	// InMemoryCache sizes what each replica keeps in its own memory of the caches keyed by
	// device (see DefaultPerDeviceCacheMiB for what that costs).
	InMemoryCache InMemoryCacheConfiguration
}

// InMemoryCacheConfiguration sizes what one replica keeps in memory of each cache keyed by
// device. Each setting applies to each of those three caches separately, per replica.
type InMemoryCacheConfiguration struct {
	// PerDeviceCacheEntries bounds each such cache's entry count. Unset (0) defaults to
	// DefaultPerDeviceCacheEntries.
	PerDeviceCacheEntries int
	// PerDeviceCacheMiB bounds each such cache's memory, in MiB. Unset (0) defaults to
	// DefaultPerDeviceCacheMiB.
	PerDeviceCacheMiB int
}

// ApplyDefaults fills each bound that is unset.
func (c *InMemoryCacheConfiguration) ApplyDefaults() {
	if c.PerDeviceCacheEntries == 0 {
		c.PerDeviceCacheEntries = DefaultPerDeviceCacheEntries
	}
	if c.PerDeviceCacheMiB == 0 {
		c.PerDeviceCacheMiB = DefaultPerDeviceCacheMiB
	}
}

// Validate refuses a bound outside its range. A negative value is refused, not read as
// unset: only 0 means that, and ApplyDefaults has replaced it by now.
func (c InMemoryCacheConfiguration) Validate() error {
	if c.PerDeviceCacheEntries < 1 || c.PerDeviceCacheEntries > MaxPerDeviceCacheEntries {
		return fmt.Errorf("inMemoryCache.perDeviceCacheEntries must be between 1 and %d (got %d)",
			MaxPerDeviceCacheEntries, c.PerDeviceCacheEntries)
	}
	if c.PerDeviceCacheMiB < 1 || c.PerDeviceCacheMiB > MaxPerDeviceCacheMiB {
		return fmt.Errorf("inMemoryCache.perDeviceCacheMiB must be between 1 and %d (got %d)",
			MaxPerDeviceCacheMiB, c.PerDeviceCacheMiB)
	}
	return nil
}

// ResolutionConfiguration sizes inbound-event resolution.
type ResolutionConfiguration struct {
	// Workers is how many inbound events are resolved at once. Unset (0) defaults to
	// DefaultResolutionWorkers.
	//
	// Every event that carries a credential — every event, under the default "required"
	// device-auth mode — is authenticated with one database read, which holds a pooled
	// connection while it runs (the credential lookup is deliberately never cached, so a
	// revocation takes effect on the next event). A resolver reads the key-value caches for
	// one event at the same time, but reads the database for whatever they could not answer
	// one lookup after another, so it still holds at most one connection at a time, and with
	// every resolver busy
	// the pool gives up to this many connections to resolution. So the count is bounded below
	// the relational pool it shares with GraphQL, the MQTT connect checks and the raise-alarm
	// consumer (which applies every alarm raise and resolve edge), by the same check the
	// other services' writer counts use.
	Workers int
}

// ApplyDefaults fills the worker count when it is unset. It is the ONE definition of that
// default: the configuration load calls it, and so does the processor for a pool built in
// code.
func (r *ResolutionConfiguration) ApplyDefaults() {
	if r.Workers == 0 {
		r.Workers = DefaultResolutionWorkers
	}
}

// Validate bounds the worker count against the pool the resolvers draw from.
func (r ResolutionConfiguration) Validate(pool config.MicroserviceDatastoreConfiguration) error {
	return rdb.CheckWriterCount("resolution.workers", r.Workers, pool)
}

// Creates the default device management configuration
func NewDeviceManagementConfiguration() *DeviceManagementConfiguration {
	cfg := &DeviceManagementConfiguration{
		RdbConfiguration: config.MicroserviceDatastoreConfiguration{
			SqlDebug: true,
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields with their defaults so configuration loaded
// from a document that omits them is still well-formed (ADR-022 decision 1).
func (c *DeviceManagementConfiguration) ApplyDefaults() {
	if c.DeviceAuthMode == "" {
		c.DeviceAuthMode = AuthModeRequired
	}
	if c.DeviceCacheTtlSeconds == 0 {
		c.DeviceCacheTtlSeconds = DefaultDeviceCacheTtlSeconds
	}
	if c.RelationshipCacheTtlSeconds == 0 {
		c.RelationshipCacheTtlSeconds = DefaultRelationshipCacheTtlSeconds
	}
	if c.MetricDefCacheTtlSeconds == 0 {
		c.MetricDefCacheTtlSeconds = DefaultMetricDefCacheTtlSeconds
	}
	if c.MembershipCacheTtlSeconds == 0 {
		c.MembershipCacheTtlSeconds = DefaultMembershipCacheTtlSeconds
	}
	if c.MaxEventFutureSkewSeconds == 0 {
		c.MaxEventFutureSkewSeconds = DefaultMaxEventFutureSkewSeconds
	}
	c.Resolution.ApplyDefaults()
	c.InMemoryCache.ApplyDefaults()
}

// Validate enforces semantic constraints after decoding and defaulting, failing
// the load closed on an invalid configuration (ADR-022 decision 1).
func (c *DeviceManagementConfiguration) Validate() error {
	switch c.DeviceAuthMode {
	case AuthModeDisabled, AuthModeOptional, AuthModeRequired:
	default:
		return fmt.Errorf("deviceAuthMode must be one of %q, %q, %q (got %q)",
			AuthModeDisabled, AuthModeOptional, AuthModeRequired, c.DeviceAuthMode)
	}
	if c.DeviceCacheTtlSeconds <= 0 {
		return fmt.Errorf("deviceCacheTtlSeconds must be positive (got %d)", c.DeviceCacheTtlSeconds)
	}
	if c.RelationshipCacheTtlSeconds <= 0 {
		return fmt.Errorf("relationshipCacheTtlSeconds must be positive (got %d)", c.RelationshipCacheTtlSeconds)
	}
	if c.MetricDefCacheTtlSeconds <= 0 {
		return fmt.Errorf("metricDefCacheTtlSeconds must be positive (got %d)", c.MetricDefCacheTtlSeconds)
	}
	if c.MembershipCacheTtlSeconds <= 0 {
		return fmt.Errorf("membershipCacheTtlSeconds must be positive (got %d)", c.MembershipCacheTtlSeconds)
	}
	// 🔴 A NEGATIVE VALUE HERE USED TO START THE INSTANCE WITH THE SKEW BOUND OFF. eventtime
	// treats maxSkew <= 0 as "no ceiling" — a defensive read, since ApplyDefaults turns the
	// unset 0 into 300 — so a negative reached that branch and disabled the one place the
	// platform decides what instant a reading happened at. The cost is not a wrong chart: one
	// event dated years out pins a device's last-activity under the strictly-newer guard, its
	// inactivity sweep never fires again, and the device can never be seen to go offline. The
	// chart already tells operators not to do it; this is the half that refuses.
	//
	// Every sibling above rejects a non-positive value. This one is bounded BELOW at zero
	// rather than at one because 0 is the legitimate "unset" ApplyDefaults has already
	// replaced by the time Validate runs — so reaching Validate with 0 means a caller
	// constructed the config without defaulting it, which the sibling checks would catch
	// anyway. Only a value the operator wrote can be negative.
	if c.MaxEventFutureSkewSeconds < 0 {
		return fmt.Errorf("maxEventFutureSkewSeconds must not be negative (got %d): a negative "+
			"value disables the clock-skew bound, which lets one far-future timestamp freeze a "+
			"device's presence permanently", c.MaxEventFutureSkewSeconds)
	}
	if err := c.Resolution.Validate(c.RdbConfiguration); err != nil {
		return err
	}
	if err := c.InMemoryCache.Validate(); err != nil {
		return err
	}
	return nil
}
