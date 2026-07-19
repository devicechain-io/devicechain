// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package streams is the single declaration of every MESSAGE stream the platform
// creates — that is, every stream backing a subject services publish to and read
// from.
//
// It is deliberately not a census of everything occupying JetStream storage. KV
// buckets (messaging.KeyValueStore) are JetStream streams too, as are the MQTT
// gateway's own session and message stores, and neither is declared here or
// bounded by the reservation budget below. They share whatever headroom the PV
// has left over. So the guarantee this package provides is precise: the budget
// cannot be incomplete over the streams ensureStream creates. It says nothing
// about the rest, which is tracked separately.
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

// Stream is one JetStream stream's declaration: the subject suffix that names
// it, its disk-budget tier, and whether its concrete subject carries a device
// token as a trailing segment.
type Stream struct {
	// Suffix is the subject suffix. The concrete subject is
	// "{instance}.{tenant}.{suffix}", and the stream captures
	// "{instance}.*.{suffix}" — one more wildcard level when PerDevice.
	Suffix string
	// Tier selects the disk ceiling. Anything riding the device path is Hot.
	Tier Tier
	// PerDevice marks a suffix whose concrete subject appends a device token, so
	// the broker grant can confine a device to its own messages. The stream must
	// capture an extra wildcard level to match.
	PerDevice bool
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

	{Suffix: InboundEvents, Tier: Hot, Why: "raw device telemetry — the primary ingest path"},
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
	{Suffix: DeviceCommands, Tier: Hot, PerDevice: true, Why: "outbound commands — scale with fleet size"},

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
// deployment's disk for one — not because they cannot spike. An operator
// debugging such a fault should raise StreamMaxBytesCold.

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

// IsPerDevice reports whether a suffix's concrete subject carries a device token.
//
// The STREAM shape and the PUBLISH shape are decided in different places — the
// stream when it is created, the subject at write time — so both derive from this
// one answer. If they disagreed, publishes would land in no stream at all: no
// error, no delivery, nothing to indicate the addressing was wrong.
func IsPerDevice(suffix string) bool {
	return bySuffix[suffix].PerDevice
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
