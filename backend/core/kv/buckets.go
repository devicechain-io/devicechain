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

import (
	"slices"
	"time"
)

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

// Account is the NATS account a bucket lives in.
type Account int

const (
	// App is the account every service and device shares (natsauth.AppAccount). The zero value, so
	// a bucket that declares nothing lives where every bucket always has.
	App Account = iota
	// Identity is user-management's own account (natsauth.IdentityAccount). It holds the buckets
	// whose contents are bearer secrets, and only a login that user-management alone holds can
	// open it. A separate account is what makes the buckets uncapturable: a login in App that can
	// create streams could otherwise source, mirror or subject-capture a bucket into a stream it
	// reads, and a deny list does not stop a stream.
	Identity
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
	// Account is the NATS account the bucket lives in. See Account.
	Account Account
	// TTL is the bucket's entry lifetime, which the broker administrator creates it with. A KV
	// bucket has ONE lifetime, so for a bucket several areas open it is the value they all have
	// to agree on: the lease bucket's is the fencing window. It is declared here, once, because
	// the creator is no longer the service that opens the bucket and so can no longer be told
	// the value at the call site.
	//
	// 🔴 EVERY BUCKET DECLARES ONE, the Identity account's included. Where the identity account
	// exists, the one login that owns it creates those buckets with the lifetimes user-management
	// asks for (the refresh-token one is configurable). Where it does not (a broker built before
	// it, or a chart-only install, whose default document has none), they live in the shared
	// account, user-management only verifies them, and the administrator creates them with THIS
	// value. A sign-in bucket created with no lifetime keeps every authorization code redeemable
	// and every failed sign-in charged until the bucket fills, and a full attempt bucket fails
	// open.
	TTL time.Duration
	// Writers are the areas that put, update, delete or purge keys in the bucket, and
	// Readers the areas that get, list or watch them. They are separate lists because a
	// key written into a bucket is a PUBLISH into a stream's subject, and who may publish
	// there is a different question from who may look: device-by-token is what event
	// resolution trusts to turn a token into a device, so writing it is forging a device.
	//
	// Today every bucket's two lists are the same set, because every area that opens a
	// bucket both reads and writes it (a lease is read to be renewed, a cache read to be
	// filled). They are declared apart anyway, so the first read-only user is a one-line
	// declaration and not a redesign.
	//
	// A cache bucket (the Cache tier) is created per instance AND per area, so its named
	// area is the only one that can open it; the lists name that area, not "every pod".
	Writers []string
	Readers []string
	// Why records what drives this bucket's growth and what a full bucket breaks.
	// It sits next to the tier so a reclassification has to confront it.
	Why string
}

// All is every KV bucket the platform creates.
var All = []Bucket{
	{
		Name:    BucketRefreshTokens,
		Tier:    State,
		Account: Identity,
		// auth.DefaultRefreshTTL. user-management's configured refresh lifetime applies where it
		// creates the bucket itself (the identity account); in the shared account an entry lives
		// at most this long after its last rotation, and the token's own expiry still bounds it.
		TTL:     7 * 24 * time.Hour,
		Writers: []string{"user-management"},
		Readers: []string{"user-management"},
		Why: "One entry per live refresh token (jti -> email), plus two small entries " +
			"per sign-in (its record and its per-identity index entry), so it scales " +
			"with concurrent signed-in humans, not with devices. A full bucket refuses " +
			"the write in identity.Manager, which fails the login that issued it.",
	},
	{
		Name:    BucketOAuthCodes,
		Tier:    State,
		Account: Identity,
		TTL:     60 * time.Second, // identity.AuthCodeTTL: the bucket's lifetime is what bounds redeemability
		Writers: []string{"user-management"},
		Readers: []string{"user-management"},
		Why: "One entry per in-flight OAuth 2.1 authorization code (ADR-047), held " +
			"only between the authorize redirect and the token exchange. Lowest " +
			"volume of any bucket here; a full one breaks MCP authorization.",
	},
	{
		Name:    BucketSsoState,
		Tier:    State,
		Account: Identity,
		TTL:     10 * time.Minute, // identity.SsoStateTTL
		Writers: []string{"user-management"},
		Readers: []string{"user-management"},
		Why: "One entry per in-flight single sign-on attempt (state, nonce, the upstream " +
			"PKCE verifier and the return address), written by the UNAUTHENTICATED beginSso " +
			"call and held ten minutes or until the identity provider's redirect consumes it. " +
			"Because anyone can call it, it scales with request rate rather than with " +
			"people, which is why it is a bucket of its own: a flood fills this one and " +
			"breaks only single sign-on, never a password sign-in or an OAuth code. A full " +
			"bucket makes beginSso FAIL CLOSED and raises the SsoStateStoreFull alert.",
	},
	{
		Name:    BucketSsoCodes,
		Tier:    State,
		Account: Identity,
		TTL:     60 * time.Second, // identity.SsoCodeTTL
		Writers: []string{"user-management"},
		Readers: []string{"user-management"},
		Why: "One entry per completed single sign-on awaiting its completeSso call, " +
			"single-use with a 60-second lifetime. Kept apart from the OAuth authorization " +
			"codes so neither redeemer can ever be handed the other's record. Lowest volume " +
			"of the sign-in buckets; a full one breaks single sign-on completion only.",
	},
	{
		Name:    BucketMfaChallenges,
		Tier:    State,
		Account: Identity,
		TTL:     5 * time.Minute, // identity.MfaChallengeTTL
		Writers: []string{"user-management"},
		Readers: []string{"user-management"},
		Why: "One entry per password sign-in that is waiting for its second factor: the owner's " +
			"email, the session epoch it was minted under and a count of the codes tried, written " +
			"when a correct password belongs to an identity with a second factor and held five " +
			"minutes or until the code is accepted. It is written only AFTER the password was " +
			"verified, so it scales with correct-password sign-ins of enrolled people, not with " +
			"guesses. Kept apart from the refresh-token bucket so a full one breaks only the second " +
			"step of an enrolled sign-in, never a refresh. A full or unreachable bucket makes an " +
			"enrolled person's sign-in FAIL CLOSED: no token is minted without the second factor.",
	},
	{
		Name:    BucketCredentialAttempts,
		Tier:    State,
		Account: Identity,
		TTL:     10 * time.Minute, // credential.AttemptTTL
		Writers: []string{"user-management"},
		Readers: []string{"user-management"},
		Why: "One entry per principal of a throttled kind (in user-management, a " +
			"hashed email; OAuth client secrets are unthrottled and write nothing) that " +
			"was checked recently, expiring 10 minutes after its last write, so it " +
			"scales with recent failed sign-ins — including ones for accounts that do not " +
			"exist, since an attempt is charged before the account is looked up. A full " +
			"bucket refuses the charge, and credential.Checker then FAILS OPEN: sign-ins " +
			"are still checked, but no address that is not already inside a running delay " +
			"is slowed down until entries expire (a replicated bucket refuses updates of " +
			"existing records too, not only new ones) — failing closed would let anyone " +
			"who fills it stop EVERY password " +
			"sign-in on the instance. Filling it takes roughly " +
			"650k distinct addresses inside one TTL window at the default 128 MiB State " +
			"ceiling (~1,100 new addresses a second), and roughly 80k at the compact " +
			"preset's 16 MiB (~135 a second, which five aliases per login request make " +
			"~27 requests a second) — each address paying a bcrypt compare. The " +
			"outcome=\"store_full\" series of devicechain_usermanagement_credential_checks_total " +
			"is where that shows, and the CredentialAttemptStoreFull alert fires on it.",
	},
	{
		Name:    BucketDeviceCredentialAttempts,
		Tier:    State,
		TTL:     10 * time.Minute, // credential.AttemptTTL
		Writers: []string{"device-management"},
		Readers: []string{"device-management"},
		Why: "device-management's MQTT password backoff (credential.KindDeviceCredential): " +
			"one entry per MQTT username tried in the last 10 minutes, a real one or not, " +
			"since a connect is charged before the credential is looked up. A SUCCESSFUL " +
			"connect leaves an entry too (the delete marker that clears its record, held for " +
			"the same TTL), so this bucket scales with the CONNECT RATE, not only with failures. " +
			"At roughly 200 bytes an entry that is about 650k connects inside 10 minutes at the " +
			"default 128 MiB State ceiling and about 80k at the compact preset's 16 MiB. A full " +
			"bucket makes the checker FAIL OPEN for devices only (connects are still checked, " +
			"without the backoff); a people's sign-in spray fills the other bucket and cannot " +
			"switch this one off, nor the reverse. A very large reconnect wave can fill it " +
			"legitimately. The outcome=\"store_full\" series of " +
			"devicechain_devicemanagement_credential_checks_total is where that shows, and the " +
			"DeviceCredentialAttemptStoreFull alert fires on it.",
	},
	{
		Name:    BucketLocks,
		Tier:    State,
		TTL:     30 * time.Second, // the longest of the lifetimes its openers asked for before this was declared
		Writers: []string{"event-sources", "user-management"},
		Readers: []string{"event-sources", "user-management"},
		Why: "One entry per HELD lock — a handful at a time, TTL'd so a crashed " +
			"holder cannot wedge one. A full bucket fails acquisition, and " +
			"DistributedLock.WithLock is fail-closed, so guarded reconcilers stop.",
	},
	{
		Name:    BucketSparkplugCommandsAwaiting,
		Tier:    State,
		TTL:     2 * time.Hour, // longer than the largest per-source reboot window (3600 s), the longest an entry is awaited
		Writers: []string{"sparkplug-ingest"},
		Readers: []string{"sparkplug-ingest"},
		Why: "One entry per Sparkplug command that was published to a node and has not yet been " +
			"resolved, keyed tenant|commandToken, written BEFORE the publish so a change of leader " +
			"can find what the last one left in flight. It scales with the commands outstanding " +
			"inside a confirm window, not with the fleet, and the source's publish rate ceiling " +
			"(commands.publishPerSecond) bounds how fast it can fill. The bucket is created wherever " +
			"sparkplug-ingest is deployed and is written only when a source enables commands. A full " +
			"bucket REFUSES the write, and the command is then not " +
			"published but handed back to command-delivery (PARKED), so the failure is a delayed " +
			"command, never an unrecorded actuation.",
	},
	{
		Name:    BucketLeases,
		Tier:    State,
		TTL:     30 * time.Second, // messaging.DefaultLeaseTTL
		Writers: []string{"event-processing", "event-sources", "lwm2m-ingest", "sparkplug-ingest"},
		Readers: []string{"event-processing", "event-sources", "lwm2m-ingest", "sparkplug-ingest"},
		Why: "One entry per HELD partition lease (ADR-070) — a handful at a time, one " +
			"per Class-3 partition (DETECT per tenant, Sparkplug and LwM2M per Instance, an " +
			"event-sources external MQTT source per source), TTL'd " +
			"so a crashed owner's partition fails over. A full bucket fails Acquire, " +
			"so a standby cannot take a partition: it degrades to no active owner, " +
			"never two — the same fail-closed shape as the lock bucket above.",
	},
	{
		Name:    BucketDeviceByToken,
		Tier:    Cache,
		TTL:     CacheTTL,
		Writers: []string{"device-management"},
		Readers: []string{"device-management"},
		Why: "One entry per device resolved on the hot ingest path. Scales directly " +
			"with fleet size — the single largest KV consumer at scale, and the " +
			"reason the cache tier is bounded below the state tier.",
	},
	{
		Name:    BucketRelationshipsBySource,
		Tier:    Cache,
		TTL:     CacheTTL,
		Writers: []string{"device-management"},
		Readers: []string{"device-management"},
		Why: "One entry per device holding tracked relationships (ADR-044 F2), so " +
			"it scales with fleet size.",
	},
	{
		Name:    BucketMembershipsByEntity,
		Tier:    Cache,
		TTL:     CacheTTL,
		Writers: []string{"device-management"},
		Readers: []string{"device-management"},
		Why: "One entry per entity with rule-scoped group memberships (ADR-062), so " +
			"it scales with fleet size.",
	},
	{
		Name:    BucketProfileResolutionByType,
		Tier:    Cache,
		TTL:     CacheTTL,
		Writers: []string{"device-management"},
		Readers: []string{"device-management"},
		Why: "One entry per device TYPE: the active published profile version's metric " +
			"definitions and rule-scoping identity (with the tenant's fence-set version), " +
			"read once per resolved event (ADR-016/045/051). Scales with the type catalog, " +
			"not the fleet.",
	},
	{
		Name:    BucketScopedGroupsExist,
		Tier:    Cache,
		TTL:     CacheTTL,
		Writers: []string{"device-management"},
		Readers: []string{"device-management"},
		Why: "One entry per TENANT — the resolver's pay-nothing gate (ADR-062 " +
			"Decision 7). The smallest bucket the platform creates.",
	},
}

// CacheTTL is the entry lifetime of every cache bucket. It is what the device-management cache
// settings defaulted to (300 seconds); those settings now bound only how long a replica keeps a
// value in its own memory, which never exceeds the bucket's lifetime.
const CacheTTL = 300 * time.Second

// The logical names of every declared bucket. They are constants rather than bare
// strings in All so that each creation site references the same identifier the
// inventory does, and renaming one breaks the build instead of silently
// disconnecting a bucket from its ceiling.
const (
	BucketRefreshTokens      = "dc_refresh_tokens"
	BucketOAuthCodes         = "dc_oauth_codes"
	BucketSsoState           = "dc_sso_state"
	BucketSsoCodes           = "dc_sso_codes"
	BucketMfaChallenges      = "dc_mfa_challenges"
	BucketCredentialAttempts = "dc_credential_attempts"
	// BucketDeviceCredentialAttempts is kept apart from BucketCredentialAttempts on
	// purpose: see its entry in All.
	BucketDeviceCredentialAttempts = "dc_device_credential_attempts"
	BucketLocks                    = "dc_locks"
	// BucketSparkplugCommandsAwaiting holds the Sparkplug commands sparkplug-ingest has published
	// and is still waiting to hear an outcome for. See its entry in All.
	BucketSparkplugCommandsAwaiting = "dc_sparkplug_commands_awaiting"
	BucketLeases                    = "dc_leases"
	BucketDeviceByToken             = "device-by-token"
	BucketRelationshipsBySource     = "relationships-by-source"
	BucketMembershipsByEntity       = "memberships-by-entity"
	BucketProfileResolutionByType   = "profile-resolution-by-type"
	BucketScopedGroupsExist         = "scoped-groups-exist"
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

// AccountFor returns the account of the named bucket. An unregistered name is in App, where a
// bucket nobody declared has always been created.
func AccountFor(name string) Account {
	for _, b := range All {
		if b.Name == name {
			return b.Account
		}
	}
	return App
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

// Admins returns the areas that OPEN the bucket, sorted: the union of Writers and Readers,
// derived rather than listed.
//
// 🔴 THE NAME IS OLDER THAN THE DISTINCTION. Until the stream-administration step each of these
// areas also created the bucket and reconciled its ceiling and replica count when it opened it.
// Now the broker administrator does that (an App-account bucket), and an area listed here
// verifies the bucket and fails its start, naming what differs, when it is missing or wrong.
// The identity account's buckets are the exception: the one login that owns the account creates
// them.
func (b Bucket) Admins() []string {
	out := append([]string(nil), b.Writers...)
	for _, a := range b.Readers {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// Lookup returns the declaration for a bucket's logical name.
func Lookup(name string) (Bucket, bool) {
	for _, b := range All {
		if b.Name == name {
			return b, true
		}
	}
	return Bucket{}, false
}

// Writes reports whether area may write the bucket's keys.
func Writes(name, area string) bool {
	b, ok := Lookup(name)
	return ok && slices.Contains(b.Writers, area)
}

// Reads reports whether area may read the bucket's keys.
func Reads(name, area string) bool {
	b, ok := Lookup(name)
	return ok && slices.Contains(b.Readers, area)
}

// Administers reports whether area OPENS the bucket (see Admins).
func Administers(name, area string) bool {
	b, ok := Lookup(name)
	return ok && slices.Contains(b.Admins(), area)
}
