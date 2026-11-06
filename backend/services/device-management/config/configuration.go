// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
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

// Defaults for the hot-path resolution caches (ADR-022 review B2). The TTL is the backstop
// on staleness for entries that change rarely; evict-on-change is the mechanism.
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
// an event whose credential was not verified on this replica within inMemoryCache.ttlSeconds), then the
// profile, the tracked relationships and whether
// any scoped group exists, read from the message broker's key-value store at the same
// time — so a resolver spends most of each event waiting for replies, not using CPU. The
// measurements below were taken when those three reads were still made one after another.
// On a kind cluster five resolvers topped out at about 1600 events a second on 1.5 of the
// pod's 4 cores, with the events above that rate queued in front of them.
//
// Measured in-process (BenchmarkInboundStageOccupancy: a three-server broker, credentialed
// events, each lookup answering after 750µs, which puts five resolvers near the kind
// ceiling): 5 resolvers resolved about 1500 events a second, 8 about 2300, 10 about 2900
// and 16 about 4600, the resolvers busy throughout in every arm. The broker's routes were
// direct when these were measured. The test fixture now carries each route through a
// loopback proxy, so a re-run measures that hop too. The count is capped at 10
// rather than taken from the widest arm because each resolver holds a pooled connection
// while it reads an event's credential, and 10 is half of the default pool of 20, the most
// rdb.CheckWriterCount accepts without a warning. On a real PostgreSQL the credential read
// did not wait for a connection at 10 (BenchmarkAuthenticateDeviceConcurrency).
const DefaultResolutionWorkers = 10

// DefaultPublishWindow is how many resolved (or failed) events one pod keeps awaiting the
// broker's acknowledgement at once when resolution.publishWindow is not set.
//
// 🔑 THE WINDOW IS A THROUGHPUT CEILING: events per second can be no more than the window
// divided by the time a PubAck takes (Little's law). On a replicated stream a PubAck is a
// quorum commit plus a route hop. Measured on a three-node cluster it took 8.4 ms at the
// median and 11.4 ms on average at saturation, so a window of 128 capped the pod at
// 128 / 11.4 ms, about 11,200 events a second, and the window sat full in half the samples
// taken at 12,400 and 14,000 offered. A window of 512 puts that cap near 45,000.
//
// The earlier 128 was chosen with an in-process benchmark whose PubAck took well under a
// millisecond, where no window is ever full; it could not see this ceiling.
//
// The cost of a deeper window is bounded and small: more sources held unacked in the pod
// (a crash redelivers them at AckWait, and the dedup id absorbs the republish), and more
// publishes failed together by a reconnect, which pays one backoff for the whole window.
//
// Memory: each publish in the window holds its encoded event and its unacked source until
// the PubAck, so a writer holds at most window x (event + source). At the few hundred bytes
// of a typical event that is well under a megabyte at 512, and two writers are built (the
// failed one is idle in steady state). The ceiling is window x the stream's maximum message
// size (1 MiB unless raised) twice over: about 1 GiB at 512 if every event were that large,
// a pathological case the fetch batch and the 100-slot backlogs ahead of the writer
// already multiply the same way.
//
// A wider window is filled only as far as the inbound reader supplies it: in flight is the
// rate times the PubAck time, about 130 at 11,400 events a second. So on its own a window
// of 512 lifts this ceiling and leaves the next one, the inbound pull, to bind.
const DefaultPublishWindow = 512

// DefaultInboundReaders is how many pulls one pod keeps outstanding at once against the
// inbound-events consumer when resolution.inboundReaders is not set. Every reader is a pull
// reader on the SAME durable, so the broker hands each message to exactly one of them, as it
// does across replicas; they feed one resolver pool, one set of caches and one publish writer.
//
// 🔑 A READER IS A THROUGHPUT CEILING OF ITS OWN: it keeps one pull in flight (fetch-ahead), so
// one reader delivers at most a batch per broker round trip. Measured on a three-node cluster
// with one reader, the read loop was waiting on its in-flight pull in 87-93% of samples while
// the resolvers sat 69-82% idle and the publish window was full only 3-4% of the time: the
// pull, not resolution and not publishing, set the pod's ceiling (about 10,300 events a second).
// Two arms then lifted it by the same amount: a second pull (a second replica, 11,400 at
// 12,400 offered, +10%) and a deeper one (a 256-message batch, 11,600, +12%). A pull that is
// twice as wide and a second pull in flight both buy depth, so the consume end is bound by the
// round trip, not by what one pull carries.
//
// A second reader is the second replica's depth without the second replica's cost: a replica
// holds its own copy of every cache (640-768 MiB each for 60,000 devices) and reads the
// database again for every cold lookup, while a reader holds only the messages it has
// fetched. More than two was not measured: in the same arms the node holding the inbound
// stream's leader ran at 96-97% CPU and the resolved-events PubAck doubled (9.5 to 17.7 ms at
// the median).
//
// 🔑 WHY THE DEFAULT IS STILL ONE. The gain was measured only on a pod already resolving about
// 10,000 events a second against a replicated broker: at 10,800 offered the second pull
// changed nothing, and below that the pull is not what binds. It was also measured at a
// 128-message batch; the 256 default took the same +12% from the same pull, and what a second
// reader adds on top of that is unmeasured. Every pod would pay for a second reader anyway:
//   - up to two more batches held unacknowledged (one handed out, one fetched ahead: 512
//     messages at the default batch of 256), which a crash or a rollout leaves for the broker
//     to redeliver after AckWait, under the same dedup ids, and so LATE to detection;
//   - a wider reorder whenever the consumer is behind (a burst, a catch-up after a restart
//     or an outage). Each reader then holds full batches of adjacent sequences and they hand
//     out at once, so events up to a batch and a half apart swap; one reader with the
//     resolver pool swaps events only about a pool's width apart. Measured on an embedded
//     broker, 3,000 messages backlogged: one reader handed them out with no inversion, two
//     with 88,401 inversions spanning up to 410 sequences (405 ms of publishing). On a quiet
//     stream two readers did not reorder at all. It is the reorder a second replica causes
//     (see ProcessResolvedEvent), but its width is a batch of the FLEET's events, so on a
//     slow fleet catching up (256 events at 50 a second is five seconds) it reaches past
//     detection's default lateness, and the kinds that read arrival order rather than event
//     time (DeltaRate drops the older sample, CountWindow groups events by arrival) see it.
//
// So the setting is raised where its symptom is: the resolvers mostly idle while the inbound
// consumer falls behind.
const DefaultInboundReaders = 1

// MaxInboundReaders bounds resolution.inboundReaders. Four readers hold up to 2,048 inbound
// messages unacknowledged at the default batch: twenty times the 100-slot queue in front of the
// resolvers, which is as far ahead of them as reading can usefully get. A deployment that
// needs more read depth than that needs more resolvers or more CPU, which is a replica.
const MaxInboundReaders = 4

// DefaultPerDeviceCacheEntries and DefaultPerDeviceCacheMiB bound what one replica keeps
// in memory of EACH of the three caches keyed by device: a device by its token, a device's
// tracked relationships, and an entity's group memberships (keyed by device, and by each
// area or asset a device is tracked to). The cache keyed by tenant keeps messaging's
// 4,096 entries and 4 MiB; there are few of those keys. The one keyed by device type has its own
// bound (DefaultProfileResolutionCacheMiB).
//
// What an entry costs, as the cache counts it (key, the value's allocation, 192 B for the
// list element, entry and map slot, which the messaging package measured and holds with a
// test, and, for a value read back as a struct, a second charge for the decoded copy kept
// beside the encoded bytes):
//
//   - a device in the device-by-token cache is the LARGEST of the three, because its value is
//     the device row with its device type, about 1.1 to 1.3 KB of JSON, charged twice (encoded
//     and decoded): 2.5 KB for a bare device and 2.7 KB with a name, description and a few
//     metadata keys, and 3.1 KB is planned for richer ones
//     (TestDeviceByTokenEntryCostSetsTheDefaultBound holds the first two). So 24 MiB held only
//     8,000 to 10,000 devices, which round 30 on GKE found out:
//     the cache was fine at 8,000 devices, evicted on 73% of lookups at 16,000 (DM CPU per
//     event 0.048 at 8,000, 0.105 at 16,000, 0.191 at 20,000 CPU-s per 1,000 events), and at
//     20,000 the lookups fell through to the broker's key-value bucket and timed out. It had
//     been sized from the other two caches' entries, not from this one's;
//   - a device with no tracked relationship is about 500 B in the relationships cache (a
//     72-byte value, charged encoded and decoded), and one with a single tracked
//     relationship about 2.4 KB (a value of about 650 to 700 bytes, charged the same way and
//     held with the decoded charge relationshipsCharge makes);
//   - an entity's group memberships are smaller than either.
//
// So the bound is set from the device-by-token entry, the larger of the two for a device that
// is also assigned: 80 MiB holds 27,000 devices at the planned 3.1 KB (31,000 at 2.7 KB,
// 33,000 at 2.5 KB), which is the 20,000-device fleet the sizing tables target plus a quarter
// of headroom. The same 80 MiB holds about 34,000 singly assigned devices in the
// relationships cache, or all 131,072 entries for unassigned ones; the entry bound is what
// binds there, so a fleet past 131,000 devices raises it as well.
//
// 🔑 AN ENTRY LIVES inMemoryCache.ttlSeconds (5 MINUTES BY DEFAULT), SO SIZE EACH REPLICA FOR
// THE WHOLE ACTIVE FLEET. An entry expires that long after it was stored, whether or not it
// was read, so the working set is every device that reported within the time. The inbound
// stream is ONE durable shared by every replica, so events are not divided between replicas
// by device: a replica resolves whichever batch it fetched next. When replicas × a device's
// reporting interval is under the time, every replica sees nearly every device, so every
// replica holds nearly the whole fleet. Dividing the fleet by the replica count is wrong.
// A bound under the fleet makes the least-recently-used order cycle through it and answer
// almost nothing (every entry is evicted before its device reports again). At the 5 s this
// used to be, the working set was 5 s × the replica's event rate and a fleet reporting less
// often than every 5 s got nothing from memory at all.
//
// PER-FLEET RULE. Per replica, for N devices reporting within the time:
//
//	perDeviceCacheMiB >= N × 3.1 KB × 1.25, which is 4 MiB for each 1,000 devices that
//	                     resolve on their token (the device-by-token cache binds first); a
//	                     fleet on the credential path binds on the relationships cache at
//	                     2.4 KB a singly assigned device, 3 MiB for each 1,000
//	credentialCacheMiB >= N × 1.1 KB × 1.25, which is 1.4 MiB for each 1,000 devices that
//	                     present a credential (an event takes one path or the other)
//	memory limit >= 2 × (3 × perDeviceCacheMiB + credentialCacheMiB + 36 + 60) MiB
//
// where 36 is the other two caches (4 MiB by tenant and 32 MiB by device type) and 60 is the
// rest of the service. The limit rule counts all three per-device caches full, which only the
// token cache does at this fleet, so it is the conservative side; it is the arithmetic the
// 768Mi chart floor is set by (below).
//
// WORKED EXAMPLE, 60,000 devices on the token path, each reporting every 10 s, each with one
// tracked edge: device-by-token 60,000 × 3.1 KB × 1.25 = about 222 MiB, so perDeviceCacheMiB
// 240 by the rule (the entry bound of 131,072 already covers 60,000), which holds the
// relationships (60,000 × about 2.4 KB = 137 MiB) and the memberships with room to spare. The
// same fleet authenticating by credential fills the credential cache instead: 60,000 × about
// 1.1 KB = about 63 MiB, so credentialCacheMiB 80, and perDeviceCacheMiB 180 for the
// relationships. For the token path the rule gives a memory limit of
// 2 × (3 × 240 + 32 + 36 + 60) = about 1.7 GiB, and for the credential path
// 2 × (3 × 180 + 80 + 36 + 60) = about 1.4 GiB.
//
// 🔴 THE MEMORY BUDGET, STATED AGAINST THE LIMIT IT RUNS UNDER. At these defaults the six
// in-process caches — these three, the two keyed by device type and by tenant, and the
// credential cache, 32 MiB (inMemoryCache.credentialCacheMiB) — hold at most
// 3 × 80 + 4 + 32 + 32 = 308 MiB, counted as above: the three keyed by device, the one keyed
// by tenant (4), the credential cache (32), and the profile resolution cache, which is 32
// now that an entry may hold a compiled payload decoder (inMemoryCache.profileResolutionCacheMiB;
// it was 4). Add the rest of the service (a replica resolving several thousand events a
// second used 32 MiB in all with its then 4-MiB caches; round 30 measured a Go heap of 219 to
// 243 MiB at 8,000 to 20,000 devices with the 24-MiB bound full) and the worst case is about
// 370 MiB live. The chart gives this service a 768Mi memory limit (a floor, memoryLimitFloor)
// and a GOMEMLIMIT of 75% of it, 576 MiB, so with every cache full the soft limit is still
// 1.5 times what is live; at the 512Mi the limit was before this bound grew, it was 1.04
// times (384 MiB against about 370), the collector held at its limit. That is the worst case,
// with every cache full: a fleet that fills them is the fleet to raise the memory limit for.
//
// Full caches need a large fleet on one replica. Three full 80-MiB caches at once take
// device-by-token being read (devices authenticated at the transport, or device auth set to
// optional or disabled) and a tenant with rule-scoped groups. Device-by-token and the
// credential cache are filled by DISJOINT events — an event either presents a credential or
// resolves on its token — so both full needs about 27,000 token-path devices (80 MiB at
// about 3.1 KB) and about 29,000 credentialed ones (32 MiB at about 1.1 KB) each reporting
// within the time to live on one replica. Raising either setting needs the memory limit
// raised with it; MaxPerDeviceCacheMiB is well past what the chart's floor can hold.
//
// That budget is why the default is 80 MiB and not the 24 MiB it was: 24 MiB filled at 8,000
// to 10,000 devices, below the 12,000 to 20,000 the sizing tables cover, and a cache that
// cycles answers almost nothing (see above). 64 MiB would hold 21,000 devices at 3.1 KB,
// a 5% margin over the 20,000 fleet; 80 keeps a quarter.
const (
	DefaultPerDeviceCacheEntries = 131072
	DefaultPerDeviceCacheMiB     = 80
	MaxPerDeviceCacheEntries     = 4 << 20
	MaxPerDeviceCacheMiB         = 256
)

// 🔑 THE CACHE TIME TO LIVE IS MINUTES, BECAUSE THE FLEET'S REPORTING INTERVAL IS. A cached
// value is of use to a device only if the device reports again before it expires, so a
// time to live shorter than the fleet's reporting interval is a cache that never answers: at
// 5 s, 60,000 devices reporting every 10 s measured 0% credential hits and a relationships
// hit ratio of 0, capping resolution at about 2,400 events a second against 6,000 offered.
// Five minutes covers every interval a fleet is likely to report at, and a value costs
// memory only while it is held, which the bounds below cap whatever the time.
//
// Correctness does not rest on the time to live. Every write that changes what a cache holds
// evicts it on every replica (see model.InitializeCaches), and the time is the backstop for
// a message that was lost. Longer means a lost message costs longer, which is why there is a
// ceiling: an hour, the longest a stale answer is allowed to be kept even if every
// eviction failed. Five minutes is the default because it is long enough to hold a device
// that reports every few minutes and short enough that a lost eviction heals within the
// time an operator would notice and retry.
const (
	DefaultCacheTtlSeconds = 300
	MaxCacheTtlSeconds     = 3600

	DefaultDeviceCacheTtlSeconds       = DefaultCacheTtlSeconds
	DefaultRelationshipCacheTtlSeconds = DefaultCacheTtlSeconds
	DefaultMetricDefCacheTtlSeconds    = DefaultCacheTtlSeconds
	DefaultMembershipCacheTtlSeconds   = DefaultCacheTtlSeconds

	// DefaultInMemoryCacheTtlSeconds is how long a replica keeps a value in its own memory
	// (inMemoryCache.ttlSeconds). A bucket-backed cache never keeps one longer than its own
	// time to live above, so the shorter of the two applies.
	DefaultInMemoryCacheTtlSeconds = DefaultCacheTtlSeconds
)

// DefaultProfileResolutionCacheMiB bounds the per-device-type profile resolution cache's in-process
// copy (inMemoryCache.profileResolutionCacheMiB). It was 4 MiB while an entry held only metric
// definitions (about 1 KiB). An entry that carries a payload decoder holds its compiled programs,
// measured at 1 to 5 MB for a decoder of any realistic size and about 6 MB charged for a maximal
// one, so 32 MiB keeps about five maximal decoders decoded; beyond that the oldest are let go and
// compiled again when next read. DefaultProfileResolutionTenantSharePercent keeps one tenant from
// taking all of it: at 25% a tenant holds at most 8 MiB of it, and a tenant past that evicts its
// own entries and never another tenant's.
const (
	DefaultProfileResolutionCacheMiB           = 32
	MaxProfileResolutionCacheMiB               = 256
	DefaultProfileResolutionTenantSharePercent = 25
)

// DefaultCredentialCacheMiB bounds the credential cache. An entry is about 1.1 KB, so 32 MiB
// holds about 29,000 credentialed devices; a fleet of 60,000 needs about 70 MiB, with the
// memory limit raised to match.
const (
	DefaultCredentialCacheMiB = 32
	MaxCredentialCacheMiB     = 256
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
	// All are NATS KV bucket TTLs, in seconds (ADR-007), from 1 to MaxCacheTtlSeconds. Each
	// also caps how long a replica keeps what it read in process memory
	// (InMemoryCache.TtlSeconds), so a TTL below that shortens it too.
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

	// RaiseAlarm sizes the consumer that applies REACT raise/resolve edges to alarm rows.
	RaiseAlarm RaiseAlarmConfiguration

	// InMemoryCache sizes what each replica keeps in its own memory of the caches keyed by
	// device (see DefaultPerDeviceCacheMiB for what that costs).
	InMemoryCache InMemoryCacheConfiguration

	// Provisioning bounds the device self-registration endpoint: what a stranger can make the
	// service spend before it has proved anything. It is FIXED configuration (the mounted
	// file); the per-tenant ceiling is the one limit that lives on the database plane.
	Provisioning ProvisioningConfiguration

	// Callout bounds the device auth callout's registration of devices by certificate on their
	// first connect. FIXED configuration, like Provisioning.
	Callout CalloutConfiguration
}

// InMemoryCacheConfiguration sizes what one replica keeps in memory of each cache keyed by
// device. Each setting applies to each of those three caches separately, per replica.
type InMemoryCacheConfiguration struct {
	// TtlSeconds is how long a replica keeps a value in its own memory before it asks the
	// bucket (or, for device credentials, the database) again, from 1 to MaxCacheTtlSeconds.
	// Unset (0) defaults to DefaultInMemoryCacheTtlSeconds. The clock starts when the value
	// was read and a hit does not extend it.
	//
	// A change made through any replica evicts the value on every replica at once, so this is
	// the bound on a change whose eviction message was lost (a replica disconnected while it
	// was sent, or one too far behind to receive it), not the delay of a change. A replica that
	// reconnects drops everything it holds.
	TtlSeconds int
	// CredentialCacheMiB bounds the device credential cache, in MiB. Unset (0) defaults to
	// DefaultCredentialCacheMiB. It is separate from the per-device bound because the
	// credential cache holds a whole device beside the credential, and is never stored in a
	// key-value bucket.
	CredentialCacheMiB int
	// PerDeviceCacheEntries bounds each such cache's entry count. Unset (0) defaults to
	// DefaultPerDeviceCacheEntries.
	PerDeviceCacheEntries int
	// PerDeviceCacheMiB bounds each such cache's memory, in MiB. Unset (0) defaults to
	// DefaultPerDeviceCacheMiB.
	PerDeviceCacheMiB int
	// ProfileResolutionCacheMiB bounds the in-process copy of the per-device-type profile
	// resolution cache, in MiB. Unset (0) defaults to DefaultProfileResolutionCacheMiB. A
	// resolution that carries a payload decoder holds its compiled programs, about 5 to 6 MB
	// for a maximal one, so this is what bounds how many such decoders a replica keeps decoded.
	ProfileResolutionCacheMiB int
	// ProfileResolutionTenantSharePercent is the most of that bound one tenant may hold, 1 to
	// 100. Unset (0) defaults to DefaultProfileResolutionTenantSharePercent. A tenant past its
	// share evicts its own entries and never another tenant's.
	ProfileResolutionTenantSharePercent int
}

// ApplyDefaults fills each bound that is unset.
func (c *InMemoryCacheConfiguration) ApplyDefaults() {
	if c.TtlSeconds == 0 {
		c.TtlSeconds = DefaultInMemoryCacheTtlSeconds
	}
	if c.CredentialCacheMiB == 0 {
		c.CredentialCacheMiB = DefaultCredentialCacheMiB
	}
	if c.PerDeviceCacheEntries == 0 {
		c.PerDeviceCacheEntries = DefaultPerDeviceCacheEntries
	}
	if c.PerDeviceCacheMiB == 0 {
		c.PerDeviceCacheMiB = DefaultPerDeviceCacheMiB
	}
	if c.ProfileResolutionCacheMiB == 0 {
		c.ProfileResolutionCacheMiB = DefaultProfileResolutionCacheMiB
	}
	if c.ProfileResolutionTenantSharePercent == 0 {
		c.ProfileResolutionTenantSharePercent = DefaultProfileResolutionTenantSharePercent
	}
}

// Validate refuses a bound outside its range. A negative value is refused, not read as
// unset: only 0 means that, and ApplyDefaults has replaced it by now.
func (c InMemoryCacheConfiguration) Validate() error {
	if c.TtlSeconds < 1 || c.TtlSeconds > MaxCacheTtlSeconds {
		return fmt.Errorf("inMemoryCache.ttlSeconds must be between 1 and %d (got %d)",
			MaxCacheTtlSeconds, c.TtlSeconds)
	}
	if c.CredentialCacheMiB < 1 || c.CredentialCacheMiB > MaxCredentialCacheMiB {
		return fmt.Errorf("inMemoryCache.credentialCacheMiB must be between 1 and %d (got %d)",
			MaxCredentialCacheMiB, c.CredentialCacheMiB)
	}
	if c.PerDeviceCacheEntries < 1 || c.PerDeviceCacheEntries > MaxPerDeviceCacheEntries {
		return fmt.Errorf("inMemoryCache.perDeviceCacheEntries must be between 1 and %d (got %d)",
			MaxPerDeviceCacheEntries, c.PerDeviceCacheEntries)
	}
	if c.PerDeviceCacheMiB < 1 || c.PerDeviceCacheMiB > MaxPerDeviceCacheMiB {
		return fmt.Errorf("inMemoryCache.perDeviceCacheMiB must be between 1 and %d (got %d)",
			MaxPerDeviceCacheMiB, c.PerDeviceCacheMiB)
	}
	if c.ProfileResolutionCacheMiB < 1 || c.ProfileResolutionCacheMiB > MaxProfileResolutionCacheMiB {
		return fmt.Errorf("inMemoryCache.profileResolutionCacheMiB must be between 1 and %d (got %d)",
			MaxProfileResolutionCacheMiB, c.ProfileResolutionCacheMiB)
	}
	if c.ProfileResolutionTenantSharePercent < 1 || c.ProfileResolutionTenantSharePercent > 100 {
		return fmt.Errorf("inMemoryCache.profileResolutionTenantSharePercent must be between 1 and 100 (got %d)",
			c.ProfileResolutionTenantSharePercent)
	}
	return nil
}

// ResolutionConfiguration sizes inbound-event resolution.
type ResolutionConfiguration struct {
	// Workers is how many inbound events are resolved at once. Unset (0) defaults to
	// DefaultResolutionWorkers.
	//
	// Every event that carries a credential — every event, under the default "required"
	// device-auth mode — is authenticated with one database read when that credential was
	// not verified on this replica within inMemoryCache.ttlSeconds, and that read holds a pooled
	// connection while it runs. A cold fleet (every device reporting less often than that, or a
	// fleet larger than the credential cache holds) still
	// reads on every event, so the bound below is unchanged by the credential cache. A
	// resolver reads the key-value caches for
	// one event at the same time, but reads the database for whatever they could not answer
	// one lookup after another, so it still holds at most one connection at a time, and with
	// every resolver busy
	// the pool gives up to this many connections to resolution. So the count is bounded below
	// the relational pool it shares with GraphQL, the MQTT connect checks and the raise-alarm
	// consumer (which applies every alarm raise and resolve edge), by the same check the
	// other services' writer counts use.
	Workers int

	// PublishWindow is how many resolved (or failed) events this pod keeps awaiting the
	// broker's acknowledgement at once, from 1 to messaging.MaxOrderedWindow. Unset (absent)
	// defaults to DefaultPublishWindow; an explicit 0 is refused, not defaulted, and so is
	// anything outside the range: a window that is silently clamped is a throughput limit
	// nobody chose. A fixed setting, read once at startup.
	//
	// It is a pointer so that "not written" and "written as 0" are different things; after
	// ApplyDefaults it is never nil.
	PublishWindow *int

	// InboundReaders is how many pull readers this pod runs on the inbound-events consumer,
	// from 1 to MaxInboundReaders. Unset (absent) defaults to DefaultInboundReaders; an
	// explicit 0 is refused, not defaulted, and so is anything outside the range. A fixed
	// setting, read once at startup.
	//
	// A pointer for the same reason as PublishWindow; after ApplyDefaults it is never nil.
	InboundReaders *int
}

// InboundReaderCount is the number of inbound readers in effect: the configured one, or the
// default when the setting was never written.
func (r ResolutionConfiguration) InboundReaderCount() int {
	if r.InboundReaders == nil {
		return DefaultInboundReaders
	}
	return *r.InboundReaders
}

// PublishWindowSize is the publish window in effect: the configured one, or the default
// when the setting was never written.
func (r ResolutionConfiguration) PublishWindowSize() int {
	if r.PublishWindow == nil {
		return DefaultPublishWindow
	}
	return *r.PublishWindow
}

// ApplyDefaults fills the worker count, the publish window and the inbound readers when they
// are unset. It is the ONE definition of those defaults: the configuration load calls it, and
// so does the processor for a pool built in code.
func (r *ResolutionConfiguration) ApplyDefaults() {
	if r.Workers == 0 {
		r.Workers = DefaultResolutionWorkers
	}
	if r.PublishWindow == nil {
		w := DefaultPublishWindow
		r.PublishWindow = &w
	}
	if r.InboundReaders == nil {
		n := DefaultInboundReaders
		r.InboundReaders = &n
	}
}

// Validate bounds the publish window and the inbound readers, and the worker count against
// the pool the resolvers draw from.
func (r ResolutionConfiguration) Validate(pool config.MicroserviceDatastoreConfiguration) error {
	if w := r.PublishWindowSize(); w < 1 || w > messaging.MaxOrderedWindow {
		return fmt.Errorf("resolution.publishWindow must be between 1 and %d (got %d)", messaging.MaxOrderedWindow, w)
	}
	if n := r.InboundReaderCount(); n < 1 || n > MaxInboundReaders {
		return fmt.Errorf("resolution.inboundReaders must be between 1 and %d (got %d)", MaxInboundReaders, n)
	}
	return rdb.CheckWriterCount("resolution.workers", r.Workers, pool)
}

// DefaultRaiseAlarmWorkers is the most alarm edges applied at once when raiseAlarm.workers is
// not set. The default in effect can be lower: see RaiseAlarmConfiguration.ApplyDefaults.
const DefaultRaiseAlarmWorkers = 8

// PoolHeadroom is how many pooled connections the two worker pools leave to everything else
// that draws on the relational pool: GraphQL requests, the MQTT connect checks and the other
// consumers' reads. With the defaults (a pool of 20, 10 resolvers) it caps an unset
// raiseAlarm.workers at 6, so the workers hold at most 16 connections and 4 remain. Two would
// have left the old default of 8 unchanged, and 18 of 20 held by workers is the starvation
// this exists to prevent.
const PoolHeadroom = 4

// RaiseAlarmConfiguration sizes the raise-alarm consumer.
type RaiseAlarmConfiguration struct {
	// Workers is how many alarm edges are applied at once. Unset (0) defaults to the smaller of
	// DefaultRaiseAlarmWorkers and what the pool has left after resolution.workers and
	// PoolHeadroom, but at least 1; 1 applies them one after another, as the consumer did
	// before this setting existed.
	//
	// One edge is about ten sequential database round trips, so a single worker applies only as
	// many edges per second as the round trips allow (about 110 per second against a local
	// PostgreSQL) while the service sits idle between them. Each worker holds a pooled
	// connection for the duration of a write transaction, so the count is bounded below the
	// relational pool by the same check the other writer counts use. Edges for one alarm are
	// always applied in the order they arrive, whatever the count. A fixed setting, read once at
	// startup.
	Workers int
}

// ApplyDefaults fills the worker count when it is unset. The default is the share of the pool
// the resolvers and PoolHeadroom leave, capped at DefaultRaiseAlarmWorkers and floored at 1, so
// by default the two worker pools leave PoolHeadroom connections free whenever the pool is big
// enough to. Where it is not, the floor wins over the headroom: a pool of 11 beside the 10
// default resolvers gets 1 raise-alarm worker, and the two then hold all 11. That is the price
// of a deployment that sized a small pool before this setting existed still starting, and it is
// no worse than before, when the same pool got 8. An explicit count is never
// adjusted, only validated on its own. resolution is the resolver count in effect, so it must
// already have its defaults applied.
func (r *RaiseAlarmConfiguration) ApplyDefaults(pool config.MicroserviceDatastoreConfiguration, resolution ResolutionConfiguration) {
	if r.Workers == 0 {
		spare := rdb.EffectiveMaxOpenConnections(pool) - resolution.Workers - PoolHeadroom
		r.Workers = max(1, min(DefaultRaiseAlarmWorkers, spare))
	}
}

// Validate bounds the worker count against the pool the workers draw from.
func (r RaiseAlarmConfiguration) Validate(pool config.MicroserviceDatastoreConfiguration) error {
	return rdb.CheckWriterCount("raiseAlarm.workers", r.Workers, pool)
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
	c.RaiseAlarm.ApplyDefaults(c.RdbConfiguration, c.Resolution)
	c.InMemoryCache.ApplyDefaults()
	c.Provisioning.ApplyDefaults()
	c.Callout.ApplyDefaults()
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
	if c.DeviceCacheTtlSeconds <= 0 || c.DeviceCacheTtlSeconds > MaxCacheTtlSeconds {
		return fmt.Errorf("deviceCacheTtlSeconds must be between 1 and %d seconds (got %d)", MaxCacheTtlSeconds, c.DeviceCacheTtlSeconds)
	}
	if c.RelationshipCacheTtlSeconds <= 0 || c.RelationshipCacheTtlSeconds > MaxCacheTtlSeconds {
		return fmt.Errorf("relationshipCacheTtlSeconds must be between 1 and %d seconds (got %d)", MaxCacheTtlSeconds, c.RelationshipCacheTtlSeconds)
	}
	if c.MetricDefCacheTtlSeconds <= 0 || c.MetricDefCacheTtlSeconds > MaxCacheTtlSeconds {
		return fmt.Errorf("metricDefCacheTtlSeconds must be between 1 and %d seconds (got %d)", MaxCacheTtlSeconds, c.MetricDefCacheTtlSeconds)
	}
	if c.MembershipCacheTtlSeconds <= 0 || c.MembershipCacheTtlSeconds > MaxCacheTtlSeconds {
		return fmt.Errorf("membershipCacheTtlSeconds must be between 1 and %d seconds (got %d)", MaxCacheTtlSeconds, c.MembershipCacheTtlSeconds)
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
	if err := c.RaiseAlarm.Validate(c.RdbConfiguration); err != nil {
		return err
	}
	if err := c.InMemoryCache.Validate(); err != nil {
		return err
	}
	if err := c.Provisioning.Validate(c.RdbConfiguration); err != nil {
		return err
	}
	if err := c.Callout.Validate(); err != nil {
		return err
	}
	// The writers the pool is shared by. Over the pool they still run, queueing for connections,
	// which is why this is a warning and not a refusal; but the registration endpoint's
	// places are part of the same budget and an operator should see the sum.
	if sum, pool := c.Resolution.Workers+c.RaiseAlarm.Workers+c.Provisioning.MaxConcurrentRegistrations,
		rdb.EffectiveMaxOpenConnections(c.RdbConfiguration); sum >= pool {
		log.Warn().Int("resolutionWorkers", c.Resolution.Workers).Int("raiseAlarmWorkers", c.RaiseAlarm.Workers).
			Int("provisioningMaxConcurrentRegistrations", c.Provisioning.MaxConcurrentRegistrations).Int("pool", pool).
			Msg("Database writers (resolution, raise-alarm and device self-registration) can together hold the whole connection pool, so reads would wait for them.")
	}
	return nil
}
