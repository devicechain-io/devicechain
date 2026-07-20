// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package streams is the single declaration of every MESSAGE stream the platform
// creates — that is, every stream backing a subject services publish to and read
// from.
//
// It is deliberately not a census of everything occupying JetStream storage. KV
// buckets and the MQTT gateway's own stores are JetStream streams too, and
// neither is declared here. So the guarantee this package provides is precise:
// the budget cannot be incomplete over the streams ensureStream creates. It says
// nothing about the rest, which is tracked separately — and by now the rest is
// mostly tracked rather than merely hoped about. KV buckets are declared in
// core/kv and bounded through messaging.KeyValueStore; the gateway's message and
// QoS 2 stores are bounded by messaging.ReconcileMqttStores. Both are counted in
// the same reservation (config.KvReservation, config.MqttStoreReservation). What
// genuinely remains unaccounted is $MQTT_sess and $MQTT_rmsgs, left unbounded on
// purpose because they discard OLD and a ceiling would evict live sessions.
//
// It exists because the set used to be unknowable. Suffixes were declared as
// literals in per-service config packages, and core — which owns the disk-budget
// arithmetic — cannot import services, so the budget test carried a
// hand-maintained mirror of the set with a comment asking future authors to keep
// it in sync. Nothing enforced that, and it had already been wrong once:
// "connector-dispatch.dead" is built by CONCATENATION at its call site, so it
// appears in no scan for string constants and no grep for a suffix literal.
//
// That gap is not cosmetic. JetStream reserves each stream's MaxBytes UP FRONT at
// creation, so the disk floor is the SUM of the ceilings rather than what the
// streams actually hold. A suffix missing from the list understates that floor,
// and overrunning the broker's max_file_store is not a soft failure — it
// crashloops every stream-creating service with "insufficient storage resources
// available". That has happened here, when 13 x 1 GiB was pointed at an 8 GiB PV.
//
// So this package is deliberately a LEAF: it imports nothing, which is what lets
// both core/config (the budget) and core/messaging (the transport) depend on it
// without a cycle, and lets every service reference a stream by name instead of
// by literal. Renaming a suffix now breaks the build rather than silently
// disconnecting a subscriber.
//
// Adding a stream means adding an entry to All. There is no second list to
// remember, and a derived suffix must be registered here too — see DeadLetter.
package streams

// Tier is a stream's disk-budget class. The two tiers exist because the streams
// divide cleanly by what drives their volume, and sizing them alike wastes most
// of the platform's disk floor on streams that will never fill.
type Tier int

const (
	// Hot is a stream whose volume scales with device traffic — fleet size times
	// message rate. It takes the larger ceiling.
	Hot Tier = iota
	// Cold is a stream driven by human or CRUD activity, whose volume cannot
	// scale with device count. It takes the smaller ceiling.
	Cold
)

// Shape names how a stream's concrete subject is built. It replaced a
// PerDevice bool once a third shape existed: a bool alongside a shape is two
// encodings of the same fact, and the failure mode when they disagree is a
// publish that matches no stream at all — no error, no delivery, nothing to say
// the addressing was wrong.
type Shape int

const (
	// ShapeTenant is the ordinary internal subject: "{instance}.{tenant}.{suffix}",
	// captured as "{instance}.*.{suffix}".
	ShapeTenant Shape = iota
	// ShapeTenantDevice appends a device token to the internal subject:
	// "{instance}.{tenant}.{suffix}.{device}". The broker grant uses it to confine a
	// device to its own messages, so the stream captures one more wildcard level.
	ShapeTenantDevice
	// ShapeDeviceEvents is the INBOUND device telemetry shape,
	// "{instance}.{tenant}.devices.{device}.events" — the one subject a device is
	// granted to publish to. It is unlike the other two in a way that matters: the
	// suffix does not appear in the subject at all, because the subject shape is
	// fixed by the grant rather than named by us. The suffix is purely the stream's
	// identity (its name and its budget key).
	//
	// A stream of this shape is a CAPTURE stream: nothing in the platform publishes
	// to it. The producer is the device, via the broker's MQTT gateway, which is the
	// entire point — the message is durable before any of our code runs.
	ShapeDeviceEvents
)

// Stream is one JetStream stream's declaration: the subject suffix that names
// it, its disk-budget tier, and how its concrete subject is shaped.
type Stream struct {
	// Suffix is the subject suffix, and always the stream's identity. For
	// ShapeTenant and ShapeTenantDevice it also appears in the subject; for
	// ShapeDeviceEvents it does not (see Shape).
	Suffix string
	// Tier selects the disk ceiling. Anything riding the device path is Hot.
	Tier Tier
	// Shape decides the subject the stream captures and that producers publish to.
	Shape Shape
	// MaxBytesCap, when positive, is an upper bound on this stream's disk ceiling,
	// applied ON TOP of its tier's rather than instead of it. It exists for a stream
	// whose tier is right but whose tier ceiling does not fit the budget — see
	// DeviceEventsCapture, where taking the Hot ceiling in full would overrun
	// max_file_store.
	//
	// A cap, specifically, and not an absolute size. The tier ceilings are what an
	// operator sizes a deployment with, and --compact sets them far below the
	// defaults (64 MiB Hot against a 2Gi volume). An absolute ceiling would ignore
	// that entirely and claim four times the largest other stream in a compact
	// install — the deployment profile would stop meaning anything for this stream.
	// Taking the smaller of the two keeps the operator's sizing authoritative and
	// lets the cap bind only where it is actually needed.
	//
	// config.StreamMaxBytesFor applies it, so a capped ceiling flows into the
	// reservation arithmetic — and so into both the default and the --compact budget
	// tests — automatically rather than needing to be remembered twice.
	MaxBytesCap int64
	// DuplicateWindowSeconds, when positive, is how long JetStream remembers a
	// published message's "Nats-Msg-Id" and suppresses a repeat of it. Zero means
	// the stream is not managed for dedup at all — which is NOT the same as a
	// window of zero, and the difference matters: writing 0 into a StreamConfig
	// would reset a window the broker already has, so ensureStream only ever
	// widens/narrows a window that was declared here.
	//
	// It is seconds rather than a time.Duration to keep this package a LEAF with no
	// imports at all, which is what lets both core/config and core/messaging depend
	// on it. messaging converts at the one place it is applied.
	//
	// The window is a MEMORY cost on the broker — it holds every id seen within it —
	// so it is sized to the redelivery gap it must cover, not to how long duplicates
	// are conceivable.
	DuplicateWindowSeconds int
	// Why records what drives this stream's volume. It is the reasoning behind
	// the tier, kept next to the tier so a reclassification has to confront it.
	Why string
}

// The subject suffixes. These are constants rather than fields read off All so a
// call site can name a stream without a lookup, and so a rename is a compile
// error at every reference.
const (
	InboundEvents     = "inbound-events"
	ResolvedEvents    = "resolved-events"
	DerivedEvents     = "derived-events"
	DeviceAttribute   = "device-attribute"
	DeviceCommands    = "device-commands"
	CommandResponses  = "command-responses"
	ConnectorDispatch = "connector-dispatch"

	// DeviceEventsCapture is the durable capture of raw inbound device telemetry
	// (ADR-030 amendment). It backs the ShapeDeviceEvents subject, so its suffix
	// names the stream but never appears in a subject.
	DeviceEventsCapture = "device-events-capture"

	DetectionRulesPublished = "detection-rules-published"
	DeviceRoster            = "device-roster"
	EntityDeleted           = "entity-deleted"
	AlarmEvents             = "alarm-events"
	RaiseAlarm              = "raise-alarm"
	FailedDecode            = "failed-decode"
	FailedEvents            = "failed-events"
)

// ConnectorDispatchDead is the terminal dead-letter sink for connector dispatch
// (ADR-060 SD-2).
//
// It is spelled out as its own constant rather than left to be built at its call
// site. Deriving it there is exactly how it went missing from the budget: a
// suffix assembled by concatenation is invisible to every way anyone looks for
// the stream set. DeadLetter builds the name; this constant makes it findable.
const ConnectorDispatchDead = ConnectorDispatch + deadLetterSuffix

const deadLetterSuffix = ".dead"

// The fixed segments bracketing the device token in the device-events subject,
// "{instance}.{tenant}.devices.{device}.events".
//
// They live in this package — the leaf everything else may import — because FOUR
// things must agree on this shape and they live in different modules: the broker
// grant that confines a device to its own topic (core/natsauth), the MQTT topic
// the gateway exposes it as, the parser that recovers the device token from a
// delivered subject (event-sources), and now the capture stream that stores it
// (DeviceEventsCapture). core/messaging held them while three of the four were
// downstream of it; the stream declaration is not, and a leaf is the only place
// all four can reach without a cycle.
//
// Bracketing the token is what makes the shape distinguishable from every
// internal subject: no suffix in All is named "devices", which
// messaging.TestNoStreamSuffixCollidesWithDeviceEventsShape pins.
const (
	SegmentDevices = "devices"
	SegmentEvents  = "events"
)

// deviceEventsCaptureMaxBytesCap caps DeviceEventsCapture below the Hot tier
// ceiling, because taking the Hot ceiling in full does not fit the disk budget.
//
// The arithmetic, at the shipped defaults: the declared streams reserve 8 GiB
// (7 Hot x 1 GiB + 8 Cold x 128 MiB), the MQTT gateway stores 384 MiB and the KV
// buckets 768 MiB, against a 10 GiB max_file_store — leaving 896 MiB over a
// headroom floor of 512 MiB, so only 384 MiB is actually free. Every ceiling is
// reserved UP FRONT, so an uncapped capture stream does not merely overcommit, it
// crashloops every stream-creating service at upgrade with "insufficient storage
// resources available".
//
// 256 MiB rather than the whole 384: at ~250 B per raw device publish it buys
// roughly an hour of capture at 250 events/s, which is a real outage-recovery
// envelope, and it leaves the headroom floor with slack rather than sitting
// exactly on it.
//
// Being a CAP is what keeps this honest under --compact, whose Hot ceiling is
// 64 MiB against a 2Gi volume: there the tier binds and this number never applies.
// An absolute 256 MiB would have overrun the compact budget outright — it did,
// which is how the cap semantics were arrived at rather than assumed.
//
// If it proves tight at the default size, the space to take is $MQTT_msgs: 256 MiB
// reserved for a gateway store that stops buffering telemetry entirely once
// nothing subscribes to it over MQTT (ADR-030 slice I7).
const deviceEventsCaptureMaxBytesCap = 256 << 20

// DeadLetter returns the dead-letter suffix derived from a base suffix.
//
// A derived suffix names a REAL stream that reserves REAL disk, so anything this
// function returns must also appear in All. That is asserted by a test rather
// than left to a comment — the whole point of this package is that the set cannot
// be incomplete by omission.
func DeadLetter(base string) string {
	return base + deadLetterSuffix
}

// All is the complete stream set: every stream any service creates, in one place.
//
// The tier classification is deliberately CONSERVATIVE. A stream is Cold only
// when its volume cannot scale with device count times message rate; anything on
// the device path — including command and connector traffic, which scale with
// fleet size — stays Hot. TierFor fails safe to Hot for a suffix absent from this
// list, so a stream nobody classified over-reserves disk (cheap and visible)
// rather than silently evicting live data via DiscardOld (expensive and silent).
// The per-entry notes below record each stream's DELIVERY contract as well as its
// tier. That reasoning used to live on the SUBJECT_* constants in each service's
// config package; when those were consolidated here it would otherwise have been
// deleted. It belongs with the declaration rather than with one call site,
// because whether a fact is at-most-once or at-least-once is a property of the
// stream that every producer and consumer of it has to agree on.
//
// Note the recurring pattern in the control-plane facts: emission is at-most-once
// (ADR-044's async-fact posture) and the consumer persists each fact into a
// durable projection it rebuilds from on restart. The finite-retention stream is
// the live delta transport, never the system of record.
var All = []Stream{
	// ---- Device path (Hot): volume scales with fleet size x message rate ----

	// The dedup window makes ingest idempotent (ADR-030 amendment). It suppresses a
	// re-publish of a capture message event-sources had already forwarded before it
	// died — the gap between "published to inbound-events" and "acked to the capture
	// stream". The message is only redelivered once a consumer next fetches it, so
	// the gap to cover is a POD RESTART, and the restart worth sizing for is not the
	// healthy one but the bad one: an image-pull backoff or a failing rollout, tens
	// of minutes rather than seconds.
	//
	// Thirty minutes, then. The cost is broker MEMORY — every id seen in the window
	// is held — and it is affordable only because the ids are small and fixed-size
	// (tenant plus a capture sequence, ~25 bytes; see processor.DedupID for why
	// nothing device-controlled may enter them). An id family with device-supplied
	// content would make this window a memory-exhaustion vector, which is one of the
	// reasons there isn't one.
	//
	// The cost scales with the AGGREGATE admitted rate across every tenant, not with
	// any one tenant's: at 250 events/s the table is ~450k entries, tens of MB, but
	// the per-tenant ingest ceiling (ADR-023) defaults well above that and nothing
	// caps the instance-wide sum, so a busy deployment should read this as low
	// hundreds of MB. That lands on a broker pod which neither the chart nor the
	// tofu module gives a memory limit. Widening the window multiplies it linearly.
	//
	// RESIDUAL, stated plainly because it is easy to assume otherwise: a restart
	// longer than the window leaves the messages that were in flight at the crash
	// duplicated, with no second line of defence for most events. event-management's
	// unique index on (tenant_id, alt_id, occurred_time) is PARTIAL — "WHERE alt_id
	// IS NOT NULL" — and alternate ids are optional and usually absent, so it
	// backstops only the events that carry one. Widening the window trades memory
	// for a longer covered outage; it does not remove the residual.
	{Suffix: InboundEvents, Tier: Hot, DuplicateWindowSeconds: 1800,
		Why: "raw device telemetry — the primary ingest path"},
	{Suffix: ResolvedEvents, Tier: Hot, Why: "every ingested event after resolution; fans out to four consumers"},

	// One message per detection, and a subscribe-able product in its own right
	// (ADR-037): clients live-subscribe by tenant like any other event feed.
	{Suffix: DerivedEvents, Tier: Hot, Why: "DETECT output — scales with rule firings against device traffic"},

	// Emitted post-commit when a numeric platform-set attribute (ADR-012 scope
	// SHARED/SERVER, DOUBLE/LONG) is upserted or deleted, so a DYNAMIC detection
	// threshold can read the device's own attribute instead of a compile-time
	// literal. At-most-once; consumer keeps a durable projection.
	{Suffix: DeviceAttribute, Tier: Hot, Why: "attribute set/delete per device — scales with fleet size"},

	// PER-DEVICE: the concrete subject carries the target device's token as a
	// final segment so the broker grant can confine a device to its own commands.
	// event-sources also has to recognize this suffix — to tell command traffic
	// from device telemetry — which is the case that first motivated centralizing
	// these names: a second literal is how the two drift apart.
	{Suffix: DeviceCommands, Tier: Hot, Shape: ShapeTenantDevice, Why: "outbound commands — scale with fleet size"},

	// ADR-030 amendment. The durable capture of raw device telemetry, and the
	// reason the gateway is no longer an MQTT client: the broker writes a device's
	// publish here before it PUBACKs, so the message is durable BEFORE our code
	// runs. The previous design acked the device from an in-memory channel, so
	// anything buffered at SIGKILL was silently lost — and could not be fixed by
	// manual acking, because NATS speaks MQTT 3.1.1, where a CleanSession
	// disconnect discards the session and shared subscriptions do not exist at all.
	//
	// Nothing in the platform publishes here; the producer is the device. Writers
	// are refused (messaging.WriteMessages) rather than allowed to publish to a
	// subject that matches no stream.
	{Suffix: DeviceEventsCapture, Tier: Hot, Shape: ShapeDeviceEvents,
		MaxBytesCap: deviceEventsCaptureMaxBytesCap,
		Why:         "raw device publishes, captured before PUBACK — the ingest durability floor"},

	// Deliberately NOT per-device: every device publishes to the one subject a
	// single consumer reads, and a response names its command by token.
	{Suffix: CommandResponses, Tier: Hot, Why: "device replies to commands — scale with fleet size"},

	// One message per fired httpCall/publish action. The stream is auto-provisioned
	// when event-processing creates the writer, so publishing is safe before the
	// consumer exists (a DeliverNew consumer skips the pre-consumer backlog).
	// CAVEAT: a DETECT replay after outbound-connectors deploys re-publishes
	// detections as NEW messages the consumer will run — a stale OccurredTime is
	// the consumer's drop/flag signal.
	{Suffix: ConnectorDispatch, Tier: Hot, Why: "REACT outbound dispatch — scales with rule firings"},

	// ---- Control plane (Cold): volume cannot scale with device count ----

	// Emitted post-commit on profile publish, carrying the ENABLED rules frozen
	// into the new version, keyed on profile-version token. At-most-once.
	{Suffix: DetectionRulesPublished, Tier: Cold, Why: "a rule publish — a human authoring action"},

	// Emitted post-commit when a device is created or re-typed, naming the device
	// and the stable profile token its type adopts, so DETECT can arm absence for
	// a device that has NEVER reported (the dead-man roster). Removal rides the
	// entity-deleted fact rather than this one. At-most-once.
	{Suffix: DeviceRoster, Tier: Cold, Why: "roster projection updates"},

	// ADR-044. Emitted when an edge entity (device, customer, area, asset, and
	// their groups) is deleted, so cross-service reference holders — such as
	// event-management's event_anchors — can reconcile dangling references.
	// At-least-once and idempotent.
	{Suffix: EntityDeleted, Tier: Cold, Why: "entity lifecycle fan-out"},

	// ADR-041, re-emitted on each alarm transition. The substrate for graphql-ws
	// subscriptions (ADR-037) and for notifications (ADR-017).
	{Suffix: AlarmEvents, Tier: Cold, Why: "alarm state changes, not raw telemetry"},

	// ADR-051 5c / ADR-054, carrying a JSON RaiseAlarmRequest. AT-LEAST-once, and
	// safe as such: ApplyAlarmContributorEdge (ADR-057) is an idempotent
	// contributor-set fold keyed on (device, alarmKey) with a per-contributor
	// monotonic decision timestamp. Since the ADR-057 cutover retired the
	// measurement evaluator this is the SOLE alarm-raise path — there is no peer
	// to double-raise against.
	{Suffix: RaiseAlarm, Tier: Cold, Why: "REACT alarm requests"},

	{Suffix: FailedDecode, Tier: Cold, Why: "error path — near zero in steady state; see the spike caveat below"},
	{Suffix: FailedEvents, Tier: Cold, Why: "error path — near zero in steady state; see the spike caveat below"},
	{Suffix: ConnectorDispatchDead, Tier: Cold, Why: "terminal dead-letter sink (ADR-060 SD-2)"},
}

// CAVEAT on the error-path streams (FailedDecode / FailedEvents): these are
// near-zero in steady state, but a broken decoder or a bad firmware roll can
// drive them at the FULL inbound rate. They are Cold because dropping the oldest
// decode failures under a sustained fault is more acceptable than sizing every
// deployment's disk for one — not because they cannot spike.
//
// An operator debugging such a fault should raise StreamMaxBytesCold — but not on
// its own. The cold bound may not exceed the hot bound (config.Validate rejects an
// inverted pair at startup, so raising only this one locks the instance out rather
// than widening the capture window), and every ceiling here is reserved UP FRONT,
// so raising a bound that applies to eight streams also needs the JetStream PV to
// grow with it. Raise StreamMaxBytes, StreamMaxBytesCold and
// nats_jetstream_storage together, and size the PV by the rule in that variable's
// own comment rather than by a percentage.

// bySuffix indexes All for lookup. Built once at init rather than scanned per
// call, since TierFor sits on the stream-creation path.
var bySuffix = func() map[string]Stream {
	m := make(map[string]Stream, len(All))
	for _, s := range All {
		m[s.Suffix] = s
	}
	return m
}()

// TierFor returns a suffix's tier, failing safe to Hot for an unrecognized one.
// Over-reserving disk is cheap and shows up in jetstream_stream_limit_bytes;
// under-bounding a busy stream silently discards live data.
func TierFor(suffix string) Tier {
	if s, found := bySuffix[suffix]; found {
		return s.Tier
	}
	return Hot
}

// ShapeOf returns a suffix's subject shape, defaulting to ShapeTenant for one
// this package does not declare.
//
// The STREAM shape and the PUBLISH shape are decided in different places — the
// stream when it is created, the subject at write time — so both derive from this
// one answer. If they disagreed, publishes would land in no stream at all: no
// error, no delivery, nothing to indicate the addressing was wrong.
func ShapeOf(suffix string) Shape {
	return bySuffix[suffix].Shape
}

// IsPerDevice reports whether a suffix's concrete subject appends a device token.
func IsPerDevice(suffix string) bool {
	return ShapeOf(suffix) == ShapeTenantDevice
}

// DuplicateWindowSecondsFor returns a suffix's declared dedup window in seconds,
// or 0 when the stream declares none. See Stream.DuplicateWindowSeconds for why
// "declares none" must not be applied as a window of zero.
func DuplicateWindowSecondsFor(suffix string) int {
	return bySuffix[suffix].DuplicateWindowSeconds
}

// MaxBytesCapFor returns a suffix's declared ceiling cap, or 0 when it declares
// none. It is an upper bound on the tier ceiling, not a replacement for it; see
// config.StreamMaxBytesFor, which is the single place the two are reconciled.
func MaxBytesCapFor(suffix string) int64 {
	return bySuffix[suffix].MaxBytesCap
}

// IsDeclared reports whether a suffix names a declared stream.
//
// The transport refuses to create a stream for anything this rejects. That is
// what turns "the declaration should be complete" into "the declaration cannot be
// incomplete" for message streams: a new one is not something you can forget to
// declare, because an undeclared one never gets created. The failure is loud and immediate at
// startup rather than a stream quietly reserving disk that no budget counted.
func IsDeclared(suffix string) bool {
	_, found := bySuffix[suffix]
	return found
}

// Suffixes returns every declared suffix. This is what the disk-budget test sums
// over, which is the reason the declaration is centralized at all.
func Suffixes() []string {
	out := make([]string, 0, len(All))
	for _, s := range All {
		out = append(out, s.Suffix)
	}
	return out
}
