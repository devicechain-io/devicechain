// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package kv is the single declaration of every JetStream KV bucket the platform
// creates.
//
// It is the companion to core/streams, and it exists to close the gap that
// package's own doc comment names: KV buckets are JetStream streams too, backed
// by a KV_-prefixed stream in the same account, drawing on the same
// max_file_store — and until this package existed none of them was declared
// anywhere, bounded, or counted in the disk budget. They shared whatever headroom
// the PV happened to have left, which is a hope rather than a budget (ADR-023
// never-unlimited).
//
// The failure mode differs from a message stream's, and the difference drives the
// tiers below. nats.go creates every KV bucket with Discard=DiscardNew, so a
// bucket that reaches its ceiling REFUSES writes rather than evicting old ones.
// Nothing is silently lost — but whether that refusal is survivable depends
// entirely on what the caller does with the failed write, which is a property of
// the call site and not of the bucket. So the tier records that property.
//
// Like core/streams this is deliberately a LEAF: it imports nothing, which lets
// core/config (the budget arithmetic) and core/messaging (the transport) both
// depend on it without a cycle.
//
// Adding a bucket means adding an entry to All. A bucket created without one
// still gets a ceiling — TierFor falls back to State, the larger and safer of the
// two — but it is absent from the reservation the budget test checks, which is
// the understatement this package exists to prevent.
//
// One gap to know about, because nothing here can close it: messaging.NewCache
// takes an arbitrary string, so a service that creates a cache with a literal
// rather than a constant from this file gets the State ceiling by fallback and is
// missing from the reservation entirely. The tripwire that catches this
// (TestEveryCacheIsDeclaredInTheKvInventory) lives in device-management, the only
// service that creates caches today, and structurally cannot see any other — the
// same hand-maintained-mirror shape this package exists to end, one level up. If
// a second service ever creates a cache, give it the same tripwire, or lift the
// check somewhere that can see every caller.
package kv

// Tier is a KV bucket's disk-budget class. The two tiers are distinguished by
// what happens when a bucket fills, because DiscardNew makes that a refused write
// rather than an eviction, and a refused write is survivable in one of these
// cases and an outage in the other.
type Tier int

const (
	// Cache is a bucket whose entries can be recomputed from the database. A
	// refused write degrades to a cache miss and the next read falls through to
	// the source of truth, so the only cost of hitting the ceiling is latency.
	// These take the smaller ceiling — which is deliberate, because they are also
	// the buckets that scale with fleet size and would otherwise be the ones to
	// eat the headroom.
	Cache Tier = iota
	// State is a bucket holding the only copy of something. A refused write fails
	// the operation that needed it — a login, an authorization-code exchange, a
	// lock acquisition — so these take the larger ceiling. What keeps that bound
	// safe is that none of them scales with fleet size: they scale with concurrent
	// humans and concurrent reconcilers, and every entry carries a TTL.
	State
)

// Bucket is one KV bucket's declaration.
type Bucket struct {
	// Name is the LOGICAL bucket name — the identifier the creating code passes,
	// before any instance or functional-area prefix is applied. The concrete
	// bucket is prefixed per instance (and per area, for caches) so that two
	// instances sharing a broker do not collide; the ceiling is a property of what
	// the bucket holds, which the prefix does not change.
	Name string
	// Tier selects the disk ceiling, keyed on what a refused write costs.
	Tier Tier
	// Why records what drives this bucket's growth and what a full bucket breaks.
	// It sits next to the tier so a reclassification has to confront it.
	Why string
}

// All is every KV bucket the platform creates.
var All = []Bucket{
	{
		Name: BucketRefreshTokens,
		Tier: State,
		Why: "One entry per live refresh token (jti -> email), so it scales with " +
			"concurrent signed-in humans, not with devices. A full bucket refuses " +
			"the write in identity.Manager, which fails the login that issued it.",
	},
	{
		Name: BucketOAuthCodes,
		Tier: State,
		Why: "One entry per in-flight OAuth 2.1 authorization code (ADR-047), held " +
			"only between the authorize redirect and the token exchange. Lowest " +
			"volume of any bucket here; a full one breaks MCP authorization.",
	},
	{
		Name: BucketLocks,
		Tier: State,
		Why: "One entry per HELD lock — a handful at a time, TTL'd so a crashed " +
			"holder cannot wedge one. A full bucket fails acquisition, and " +
			"DistributedLock.WithLock is fail-closed, so guarded reconcilers stop.",
	},
	{
		Name: BucketLeases,
		Tier: State,
		Why: "One entry per HELD partition lease (ADR-070) — a handful at a time, one " +
			"per Class-3 partition (DETECT per tenant, Sparkplug per Instance), TTL'd " +
			"so a crashed owner's partition fails over. A full bucket fails Acquire, " +
			"so a standby cannot take a partition: it degrades to no active owner, " +
			"never two — the same fail-closed shape as the lock bucket above.",
	},
	{
		Name: BucketDeviceByToken,
		Tier: Cache,
		Why: "One entry per device resolved on the hot ingest path. Scales directly " +
			"with fleet size — the single largest KV consumer at scale, and the " +
			"reason the cache tier is bounded below the state tier.",
	},
	{
		Name: BucketRelationshipsBySource,
		Tier: Cache,
		Why: "One entry per device holding tracked relationships (ADR-044 F2), so " +
			"it scales with fleet size.",
	},
	{
		Name: BucketMembershipsByEntity,
		Tier: Cache,
		Why: "One entry per entity with rule-scoped group memberships (ADR-062), so " +
			"it scales with fleet size.",
	},
	{
		Name: BucketMetricDefsByType,
		Tier: Cache,
		Why: "One entry per device TYPE (ADR-016/045), so it scales with the type " +
			"catalog rather than the fleet.",
	},
	{
		Name: BucketProfileScopeByType,
		Tier: Cache,
		Why:  "One entry per device TYPE (ADR-051), scaling with the type catalog.",
	},
	{
		Name: BucketScopedGroupsExist,
		Tier: Cache,
		Why: "One entry per TENANT — the resolver's pay-nothing gate (ADR-062 " +
			"Decision 7). The smallest bucket the platform creates.",
	},
}

// The logical names of every declared bucket. They are constants rather than bare
// strings in All so that each creation site references the same identifier the
// inventory does, and renaming one breaks the build instead of silently
// disconnecting a bucket from its ceiling.
const (
	BucketRefreshTokens         = "dc_refresh_tokens"
	BucketOAuthCodes            = "dc_oauth_codes"
	BucketLocks                 = "dc_locks"
	BucketLeases                = "dc_leases"
	BucketDeviceByToken         = "device-by-token"
	BucketRelationshipsBySource = "relationships-by-source"
	BucketMembershipsByEntity   = "memberships-by-entity"
	BucketMetricDefsByType      = "metric-defs-by-type"
	BucketProfileScopeByType    = "profile-scope-by-type"
	BucketScopedGroupsExist     = "scoped-groups-exist"
)

// TierFor returns the tier of the named bucket.
//
// An unregistered name falls to State — the LARGER ceiling. That is the opposite
// of a fail-safe default in most of this codebase, and it is deliberate: the
// tiers here rank by what a refused write costs, so the unknown case must assume
// the expensive one. Guessing Cache for a bucket that actually holds state would
// bound someone's only copy of something at the smaller ceiling and turn a full
// bucket into a failed login.
//
// The cost of guessing wrong in this direction is a bucket that reserves more
// disk than it needs and is missing from Reservation, which the budget test's
// headroom floor is sized to absorb.
func TierFor(name string) Tier {
	for _, b := range All {
		if b.Name == name {
			return b.Tier
		}
	}
	return State
}

// Count returns how many declared buckets are in the given tier. The disk budget
// multiplies this by the tier's ceiling, so it is the KV half of the platform's
// reservation.
func Count(t Tier) int {
	n := 0
	for _, b := range All {
		if b.Tier == t {
			n++
		}
	}
	return n
}
