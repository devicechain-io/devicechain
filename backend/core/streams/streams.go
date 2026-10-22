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
//
// The entry also says WHO uses the stream, in three roles (roles.go): the areas that write
// it, the areas that read it (and in which mode, which decides the consumer's name), and the
// areas that administer it. A manager over a deployed service refuses any use its area does
// not declare (core/messaging, catalog.go), so the declaration is what a service is held to
// rather than a description of it.
package streams

// Tier is a stream's disk-budget class. The two tiers exist because the streams
// divide cleanly by what drives their volume, and sizing them alike wastes most
// of the platform's disk floor on streams that will never fill.
//
// 🔴 IT IS A DISK-BUDGET CLASS AND NOTHING ELSE. It is the obvious hook for per-tier
// CONSUMER tuning — different retry budgets or ack windows for hot and cold streams —
// and that was considered and declined. Two reasons, both in the MaxDeliver comment in
// core/messaging/nats.go: the server freezes a durable's config, so varying a field by
// tier crash-loops any cluster that is not brand new; and the dead-letter arms spread
// across the service estate compare Message.NumDelivered against the package-level
// messaging.MaxDeliver, so a per-tier limit would make them write their dead letter at
// the wrong attempt, or never, with nothing in the test estate able to see it. Read
// that comment before giving this type a second job.
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
	// ShapeAdvisory is a capture of the BROKER'S OWN advisories, which live outside the
	// instance's subject tree altogether ("$JS.EVENT.ADVISORY..."). Like
	// ShapeDeviceEvents the suffix names the stream and appears in no subject, and
	// nothing in the platform publishes to it: the producer is nats-server. Its
	// subjects carry no tenant, which is why a tenant purge skips a stream of this
	// shape. See MaxDeliveries, its one user.
	ShapeAdvisory
)

// Retention is how a stream lets go of a message.
//
// It is declared rather than assumed because the difference is visible to every
// reader of a stream's counts: under Limits a message stays until a ceiling or its
// age evicts it, whoever has read it; under WorkQueue an ACK removes it. A reader
// that reasons about "what has this durable skipped" from stream positions reads
// acked-and-removed work-queue messages as skipped, so it must know which kind of
// stream it is looking at, and it must learn that from the declaration rather than
// from a list of exceptions kept beside it.
type Retention int

const (
	// RetentionLimits is every stream but one: messages age out, or are evicted
	// oldest-first at the ceiling, independently of who has read them.
	RetentionLimits Retention = iota
	// RetentionWorkQueue removes a message when it is acked, so the stream's count
	// is exactly the work nobody has finished. Two streams are declared so, each with
	// exactly one reader: MaxDeliveries and DeviceEventsCapture.
	RetentionWorkQueue
)

// The platform's ONE consumer retry contract. messaging.AckWait and messaging.MaxDeliver ARE
// these values: their comments in core/messaging/nats.go say why there is exactly one of each
// and why neither is a knob. They are declared here, in the leaf, for one reason. A stream whose
// producer re-publishes on each redelivery of its consumer's input has to declare a dedup window
// that covers the whole redelivery span, and this package cannot import messaging. Declaring the
// inputs here makes that window a constant expression rather than a second literal that could
// drift from them.
//
// They also have a reader outside Go. A write that fails while a database primary is being
// replaced is left unacked, so an event is abandoned only by an outage longer than
// (ConsumerMaxDeliver-1) x ConsumerAckWaitSeconds; the database's stop window (stopDelay and
// switchoverDelay in the cnpg-cluster chart) is sized inside that. hack/check-cnpg-chart-schema.sh
// reads both constants from this file by their `Name = <int>` lines, so lowering either one, or
// computing it instead of writing it as a literal, fails that check.
const (
	// ConsumerAckWaitSeconds is how long the broker waits for an ack before redelivering.
	ConsumerAckWaitSeconds = 60
	// ConsumerMaxDeliver is how many deliveries a message gets before consumers give up on it.
	ConsumerMaxDeliver = 5
	// RedeliveryDuplicateWindowSeconds covers every delivery of one consumed message, for a
	// producer that re-publishes the same message, under a dedup id stable across them, on each
	// delivery of its input. Within it the broker stores that message once.
	//
	// It rests on a premise its consumer has to MAKE true, not one the broker provides: each
	// delivery is handled within its own AckWait. Three consumers rely on it. REACT makes it
	// true by reading through a one-slot capacity reader (a delivery is never left queued
	// behind another past its clock) and by bounding every sink call by the delivery's ack
	// deadline. device-management's resolver and event-sources' capture forward read through
	// plain readers, whose contract (messaging.AckWait) is that a fetched batch clears well
	// inside AckWait. One publish waits at most the 5 s publish ceiling plus one failure
	// backoff of at most 2 s, but a BATCH clears inside AckWait only while the broker answers:
	// a failing stream is published to at one message per backoff, so a plain reader's
	// fetched batch can be handed out long after its clock ran out. Given the premise,
	// successive deliveries are at most two AckWaits apart (the redelivery becomes due one
	// AckWait after its predecessor was fetched, and can wait up to one more for the message
	// ahead of it to finish), so the last re-publish lands at most
	// (MaxDeliver-1)*2*AckWait + AckWait = 4*120 s + 60 s = 540 s after the first.
	// 2*MaxDeliver*AckWait = 600 s covers that with one AckWait to spare. No consumer BackOff
	// adds to it: no durable sets one, so every redelivery waits exactly AckWait.
	//
	// Longer than this is possible in two ways, and then the repeat is stored again: no
	// consumer at all for the gap (every replica down, or a lease handover that outlasts it),
	// or a broker that stored the first publish, lost its acknowledgement and then failed the
	// stream's publishes for longer than about the window, so that a later delivery, held in a
	// plain reader's batch through the outage, is published once the broker answers again.
	// Covering that gap too is what RestartDuplicateWindowSeconds is for, and its memory cost
	// (see BrokerDefaultDuplicateWindowSeconds) is why it is not the default.
	RedeliveryDuplicateWindowSeconds = 2 * ConsumerMaxDeliver * ConsumerAckWaitSeconds

	// RestartDuplicateWindowSeconds covers a re-publish that waits for a POD RESTART rather
	// than for a redelivery to a live consumer: the producer died after its publish was stored
	// and before it acked the input, and no other replica takes the input over (every area
	// runs one replica by default). It is sized to a bad restart — an image-pull backoff or a
	// failing rollout — not a healthy one: 1800 s is a restart of up to 1800 - 540 = 1260 s
	// (21 minutes) followed by the rest of the redelivery span.
	RestartDuplicateWindowSeconds = 1800

	// BrokerDefaultDuplicateWindowSeconds is nats-server's own default window
	// (server.StreamDefaultDuplicatesWindow, two minutes), the one a stream gets when its
	// config names none. A stream no publisher stamps a Nats-Msg-Id on declares exactly this:
	// its window holds no ids and costs nothing, and declaring the value the broker already
	// filled in means an upgrade changes nothing on the existing stream. A test reads the
	// server's constant, so a broker that changes its default fails a test rather than turning
	// these declarations into a silent change.
	//
	// 🔑 WHAT A WINDOW COSTS. The broker keeps every id published inside the window, in a map
	// plus an ordered array (nats-server stream.go: ddmap, ddarr), and EVERY replica keeps its
	// own, so under R3 each server holds every stream's table. Measured against nats-server
	// v2.15.0 (the embedded test server) with 35-byte ids; the deployed broker is the nats
	// chart's 2.15.1, whose ddentry, ddmap and ddarr are the same. A held id costs about 120
	// bytes of live heap, more for a longer id (derived-events' are 67 bytes): about 120 MB per
	// million ids per server, before the garbage collector's headroom. A table holds the
	// stream's id rate times its window: at 10,000 ids/s a 120 s window holds 1.2M ids
	// (~145 MB), 600 s holds 6M (~720 MB) and 1800 s holds 18M (~2.2 GB). The sum over every
	// stream has to fit each NATS server's memory limit: TestDuplicateWindowsFitTheBrokerMemoryLimit
	// (core/messaging) holds the declared windows to the shipped limit at the reference load.
	BrokerDefaultDuplicateWindowSeconds = 120
)

// String names a shape for the messages that mention one.
//
// Those messages are the ones a maintainer reads when a subject came out wrong, and a
// shape is precisely what tends to be wrong — so "device-events-capture (device-events)"
// is actionable where "device-events-capture (2)" is a number to go and look up.
func (s Shape) String() string {
	switch s {
	case ShapeTenantDevice:
		return "tenant-device"
	case ShapeDeviceEvents:
		return "device-events"
	case ShapeAdvisory:
		return "advisory"
	default:
		return "tenant"
	}
}

// Stream is one JetStream stream's declaration: the subject suffix that names
// it, its disk-budget tier, and how its concrete subject is shaped.
type Stream struct {
	// Suffix is the subject suffix, and always the stream's identity. For
	// ShapeTenant and ShapeTenantDevice it also appears in the subject; for
	// ShapeDeviceEvents it does not (see Shape).
	Suffix string
	// Writers are the areas that PUBLISH into this stream's subjects. An area absent
	// from the list has no writer on the stream, and a manager built for a platform
	// service refuses to open one (core/messaging, NewWriter and the writers built on it).
	//
	// A stream no service writes - DeviceEventsCapture (the broker captures raw device
	// publishes into it) and MaxDeliveries (the broker publishes advisories into it) - has an
	// empty list, and that is a statement, not an omission. DeadLetters is the third shape:
	// see RecorderWrites.
	Writers []string
	// RecorderWrites declares that every area that runs the max-delivery recorder writes this
	// stream, and so replaces a hand-kept list of them (see RecorderAreas). The recorder runs
	// in any area with a durable reader, and it letters that area's give-ups here, so "which
	// areas write dead letters" and "which areas read" are one fact stated twice - and were two
	// lists, which disagreed: dashboard-management reads entity-deleted, runs the recorder, and
	// was in neither Areas list. Deriving the writers makes the disagreement unrepresentable.
	RecorderWrites bool
	// Readers are the consumers of this stream, each an area and the MODE it reads in, because
	// the mode decides what the area may do and what the consumer is called (see ReadMode).
	// An area absent from the list has no reader on the stream.
	//
	// A MaxDeliveries stream (ShapeAdvisory) declares none: each area with a durable reader
	// reads it through its max-delivery recorder, so they are derived the same way
	// RecorderWrites derives its writers (see ReadersOf).
	Readers []Reader
	// Tier selects the disk ceiling. Anything riding the device path is Hot.
	Tier Tier
	// Shape decides the subject the stream captures and that producers publish to.
	Shape Shape
	// MaxBytesCap, when positive, is an upper bound on this stream's disk ceiling,
	// applied ON TOP of its tier's rather than instead of it. It exists for a stream
	// whose tier is right but whose tier ceiling would reserve far more than it can
	// ever hold — see MaxDeliveries, which is empty in steady state.
	//
	// 🔴 NEVER ON A STREAM WHOSE BACKLOG IS DATA SOMEONE WAS PROMISED. A cap is not
	// operator-tunable (raising the tier does not move it), so it fixes that stream's
	// margin for every deployment. DeviceEventsCapture carried one, and in a perf run it
	// set how soon PUBACKed device events were lost; see its declaration.
	//
	// A cap, specifically, and not an absolute size. The tier ceilings are what an
	// operator sizes a deployment with, and --compact sets them far below the
	// defaults (64 MiB Hot against a 3Gi volume). An absolute ceiling would ignore
	// that entirely and could claim more than the tier in a compact install — the
	// deployment profile would stop meaning anything for this stream.
	// Taking the smaller of the two keeps the operator's sizing authoritative and
	// lets the cap bind only where it is actually needed.
	//
	// config.StreamMaxBytesFor applies it, so a capped ceiling flows into the
	// reservation arithmetic — and so into both the default and the --compact budget
	// tests — automatically rather than needing to be remembered twice.
	MaxBytesCap int64
	// DuplicateWindowSeconds, when positive, is how long JetStream remembers a
	// published message's "Nats-Msg-Id" and suppresses a repeat of it. Every stream
	// declares one, and it must be at least the longest time between a message's first
	// publish and its last possible re-publish: a repeat that arrives after the window
	// is stored a second time. TestEveryDuplicateWindowCoversItsRepublishInterval
	// (core/messaging) holds each one to that interval, computed from the live retry
	// constants. Zero would mean the stream is not managed for dedup at all — which
	// is NOT the same as a window of zero: writing 0 into a StreamConfig would reset a
	// window the broker already has, so ensureStream never writes it.
	//
	// ensureStream reconciles a declared window onto an EXISTING stream in place
	// (UpdateStream; JetStream allows the field to change), so an upgrade gets a new
	// window at the first start of any area that ensures the stream.
	//
	// It is seconds rather than a time.Duration to keep this package a LEAF with no
	// imports at all, which is what lets both core/config and core/messaging depend
	// on it. messaging converts at the one place it is applied.
	//
	// The window is a MEMORY cost on the broker — it holds every id seen within it —
	// so it is sized to the redelivery gap it must cover, not to how long duplicates
	// are conceivable.
	DuplicateWindowSeconds int
	// Retention is how the stream lets go of a message; see Retention. The zero value
	// is RetentionLimits, which every stream but one is.
	Retention Retention
	// RecreateOnRetentionChange declares that an existing stream of this suffix holding a
	// different retention than the one declared here may be deleted and recreated, once its
	// reader has drained it, instead of being refused. JetStream cannot change retention in
	// place, and every other stream keeps the refusal: a recreate is only safe where the
	// stream holds nothing but what its single reader has not yet forwarded.
	RecreateOnRetentionChange bool
	// Dormant marks a stream that is DECLARED but that no service ensures yet, so it does
	// not exist on a running broker. The replication check does not require a dormant
	// stream even where its areas (Areas()) are deployed - a check that fails on a healthy install
	// is one somebody switches off. Whoever ships the first reader or writer removes the
	// flag in the same change; TestDormantStreamsArePinned names the set so that cannot
	// be forgotten.
	Dormant bool
	// PlatformAuthored declares that every message on this stream is written by the
	// platform itself, never by a device. Only such a stream's dead letters keep their
	// payload verbatim; a stream that does not declare it - including one added later and
	// forgotten here - has its payload redacted of credential material before it is kept
	// (core/deadletter, core/payloadredact). The default is the safe side: forgetting the
	// flag costs a less detailed letter, never a kept credential.
	PlatformAuthored bool
	// DeadLetterKind is the kind (core/deadletter's vocabulary) of a dead letter written
	// about a message consumed from this stream — by an in-handler arm or by the
	// platform's max-delivery recorder, which is why it is declared once, here, rather
	// than chosen at each call site. NotLettered forbids a letter: the stream is itself
	// a sink, or a report, and a give-up on it is counted as a loss instead.
	//
	// It is a string rather than a deadletter.Kind because this package is a LEAF;
	// core/deadletter checks every declared value against its vocabulary.
	DeadLetterKind string
	// VerbatimCopy, when set, names the stream that holds a BYTE-IDENTICAL copy of a
	// message given up on from this one, and makes the dead letter an index entry for
	// that copy (no payload). Whoever writes a give-up — the service's own arm or the
	// max-delivery recorder — writes the copy there first. "" means none.
	VerbatimCopy string
	// ReplayCovered names the areas whose durable on this stream loses nothing when its
	// deliveries run out, because the area does not depend on the durable to see a message
	// again: it re-reads the STREAM by sequence from a checkpoint it commits itself, and it
	// acks only once that checkpoint covers the message. For such a durable an exhausted
	// delivery is not an abandoned message, it is a checkpoint that has not committed for
	// AckWait x MaxDeliver — so the max-delivery recorder writes no dead letter for it and
	// counts it instead (outcome "replay-covered"), which is what the chart's alert reads.
	//
	// 🔴 PER AREA, NOT PER STREAM. The claim is about how one area consumes, and a stream
	// has as many ways of being consumed as it has readers: resolved-events is re-read by
	// event-processing's DETECT loop, but device-state and event-management read the same
	// stream through ordinary durables, and an exhausted delivery there IS a message those
	// areas never handled. A stream-wide flag would silence their letters too.
	//
	// 🔴 A NAME HERE IS A CLAIM THAT NEEDS A PROOF. Adding one removes the only record of
	// that durable's give-ups, so it is backed by a test that exhausts the durable's
	// deliveries against a real broker and shows the area's checkpoint still covers every
	// message (event-processing's replay_covers_exhausted_test.go is the one for DETECT).
	// Each name must also be a reader (a durable or ack-floor one).
	//
	// 🔴 A single-writer area's claim rests on its LEASE too. DETECT applies every delivery
	// on first sight only while one replica consumes the shared durable; a zombie leader that
	// has lost its lease but not yet noticed can apply messages whose checkpoint is then
	// refused, and those redeliver to the real leader, which may already be past their
	// sequence and drop them as duplicates. That split-brain loss exists with or without
	// exhaustion, and the lease's local deadline (30s) makes a zombie outliving AckWait x
	// MaxDeliver unrealistic — see ResolvedEventsProcessor's type comment.
	ReplayCovered []string
	// BackpressureReaders names the areas whose durable on this stream must never lose a
	// message it has not read yet. While any of them is so far behind that its UNREAD backlog
	// fills 90% of the stream's ceiling, the stream applies backpressure: core/messaging
	// refuses its writers (messaging.ErrStreamBackpressure), and a reader that forwards into
	// it (Forwards) stops fetching, until that backlog is below 80%. On a full stream it also
	// applies backpressure while the messages that reader has already read, which DiscardOld
	// removes first, would all be discarded within 30 s at the rate the stream is discarding
	// them (the runway rule, core/messaging/backpressure.go). Empty means the stream
	// never refuses: when it is full it discards its oldest message, read or not, and the
	// readers' unread-loss counters are what record a message nobody had read.
	//
	// 🔴 THE BROKER STILL DISCARDS OLD, AND THAT IS DELIBERATE. Every stream is Limits
	// retention with a week of history, so what a busy stream holds at its ceiling is mostly
	// messages every reader has ALREADY read. Discard-new would refuse on that history and
	// stop ingest for good on any install averaging more than a few events a second over a
	// week. Interest or work-queue retention would make "full" mean "full of unread", but
	// rule preview and DETECT replay need the read history, an interest stream with no
	// consumer acknowledges a publish and stores nothing, and the broker refuses a retention
	// change to or from work-queue on a live stream. So the broker keeps discarding old, as a
	// counted backstop, and the refusal is made by the platform's own writers from the
	// readers' backlog, which is the quantity that decides whether anything is lost.
	//
	// 🔴 A NAME HERE CAN STOP INGEST FOR EVERY TENANT. The stream is shared by all tenants, so
	// one slow reader listed here refuses everyone's events. That is why the list holds only
	// the readers whose unread loss IS data loss (resolution, and persistence), and not a
	// reader that loses only a projection (device-state's live view converges on the next
	// event) or one that has its own recovery (event-processing replays by sequence from its
	// checkpoint, and its unread position is that checkpoint, which no durable's backlog
	// shows). Their loss stays counted by the unread-loss alerts, as before.
	//
	// Each name must be a durable reader, and none may be in ReplayCovered.
	BackpressureReaders []string
	// Forwards maps an area that reads this stream to the stream it forwards what it reads
	// into, when that stream applies backpressure. Every reader the area opens on this stream
	// then parks before each fetch while the downstream stream's gate is closed, so a closed
	// gate holds the backlog here instead of failing a publish: a message already fetched has
	// spent one of its MaxDeliver deliveries, and a forwarding hop that failed its publish
	// would leave it unacked and spend another, until the message is given up on as poison.
	// Parking before the fetch spends none.
	//
	// It is a declaration rather than an option at the call site so that the wiring cannot be
	// left out: NewReader applies it from the reader's area, and a test pins the table.
	Forwards map[string]string
	// Why records what drives this stream's volume. It is the reasoning behind
	// the tier, kept next to the tier so a reclassification has to confront it.
	Why string
}

// NotLettered is the DeadLetterKind of a stream whose give-ups are never lettered:
// the dead-letter sinks themselves (a letter about a letter would loop), the
// write-only reports, and the max-delivery capture.
const NotLettered = "-"

// The dead-letter kinds the declarations below use. They must equal core/deadletter's
// Kind values, which that package's vocabulary test enforces over every declaration.
const (
	kindEvent             = "event"
	kindDetectionAction   = "detection-action"
	kindNotification      = "notification"
	kindConnectorDispatch = "connector-dispatch"
	kindCommandResponse   = "command-response"
	kindCommand           = "command"
	kindControlFact       = "control-fact"
)

// The subject suffixes. These are constants rather than fields read off All so a
// call site can name a stream without a lookup, and so a rename is a compile
// error at every reference.
const (
	InboundEvents     = "inbound-events"
	ResolvedEvents    = "resolved-events"
	DerivedEvents     = "derived-events"
	DeviceAttribute   = "device-attribute"
	DeviceCommands    = "device-commands"
	DeviceDesired     = "device-desired"
	DeviceReports     = "device-reports"
	CommandResponses  = "command-responses"
	ConnectorDispatch = "connector-dispatch"

	// DeviceEventsCapture is the durable capture of raw inbound device telemetry
	// (ADR-030 amendment). It backs the ShapeDeviceEvents subject, so its suffix
	// names the stream but never appears in a subject.
	DeviceEventsCapture = "device-events-capture"

	DetectionRulesPublished = "detection-rules-published"
	DeviceRoster            = "device-roster"

	// GeoFenceSetManifest carries a fence-set fact as a MANIFEST: the version, when it was
	// minted, and for each fence its token plus the CONTENT ADDRESS of its geometry. The
	// geometry itself is fetched separately and keyed by that address, so this fact's size is a
	// function of the fence COUNT alone and of nothing an author can write.
	//
	// 🔴 IT IS ITS OWN SUBJECT RATHER THAN A NEW SHAPE ON THE OLD ONE, AND THE REASONING IS
	// INHERITED FROM THE TWO SUBJECTS IT REPLACES — both of which this change retires, which is
	// why it is written out here rather than cross-referenced into a comment that no longer
	// exists. What distinguishes a manifest from a whole fence set is that its entries carry a
	// hash instead of a geometry, and an entry's SHAPE is invisible to a decoder that predates
	// it: json.Unmarshal ignores keys it does not know and leaves the ones it wants at their
	// zero values, so a consumer built before this existed decodes a manifest as a fence set
	// whose every fence has EMPTY GEOMETRY. That is not a decode failure — it installs as a set
	// of fences that cannot be evaluated, or worse as one that looks answerable and is not.
	// device-management and event-processing roll as independent Deployments, so that
	// mixed-version window opens on every upgrade and again on any rollback of one of them.
	//
	// Publishing to a subject the old consumer never subscribed to makes its behaviour exactly
	// what it was before this existed: the fact does not arrive, the version stays missing,
	// containment reports a COUNTED eval error, and the five-minute reconcile sweep repairs it.
	// Degrading to a loud, already-handled failure beats degrading to a silent wrong answer,
	// and it needs no feature flag and no upgrade ordering to do it.
	GeoFenceSetManifest = "geofence-set-manifest"
	EntityDeleted       = "entity-deleted"

	// DeviceOwnership carries the device ownership-changed fact (ADR-081): a device's
	// complete current anchor set, published by device-management after every write that
	// may have changed which tracked relationships it has, so a projection keyed by device
	// (device-state's live view) can keep a customer-scoped stamp current between events.
	DeviceOwnership = "device-ownership"
	AlarmEvents     = "alarm-events"
	RaiseAlarm      = "raise-alarm"
	FailedDecode    = "failed-decode"

	// FailedEvents carries an event the resolve/persist pipeline could not process:
	// device-management publishes one when resolution fails, event-management when
	// the message will not unmarshal or the row will not insert.
	//
	// 🔴 IT IS WRITE-ONLY TODAY, AND SAYING SO HERE IS THE POINT OF THIS COMMENT.
	// Grep the backend for this constant and every hit is NewWriter — two of them.
	// Nothing subscribes, nothing stores it, no console surface reads it, and there
	// is no reconciler that works it off. A record published here is retained for the
	// cold tier's window and then expires unread.
	//
	// So it is a REPORT, not a queue, and it must not be described as a dead-letter
	// queue in comments or in prose. The platform does have one of those —
	// DeadLetters below, whose letters user-management stores and command-delivery
	// reads back — and the distinction is exactly the one an operator needs: work in
	// DeadLetters can be recovered, and an event reported here cannot. The
	// operator-visible signal for a persistence failure is the producing service's
	// log line and its RED metrics counter.
	//
	// Giving it a consumer is open work, not something this comment forecloses; what
	// it forecloses is a reader inferring one from the vocabulary.
	FailedEvents = "failed-events"

	// DeadLetters is the platform's one sink for work a consumer accepted and then gave
	// up on (ADR-024) — a detection's actions that could not be dispatched, an alarm
	// that could not be delivered to anyone, a command response that could not be
	// recorded.
	//
	// 🔑 ONE STREAM, NOT A `.dead` TWIN PER SOURCE, and the reason is this budget. The
	// convention above is `<base>.dead`, and following it for the three consumers that
	// lacked an arm would have cost three more cold streams — three more MaxBytes
	// RESERVED UP FRONT on every deployment's JetStream volume, forever, to hold
	// something that is empty in steady state. What that gives up is telling producers
	// apart by subject, which the envelope carries anyway (core/deadletter).
	DeadLetters = "dead-letters"

	// MaxDeliveries captures the broker's max-delivery advisory for every platform
	// durable: the notice nats-server publishes when it stops redelivering a message
	// because every delivery ran out. A message that ends that way with no outcome
	// recorded — a pod stopped mid-handling, a handler that ran past its window — never
	// reaches a service's own dead-letter arm, so this capture is the only place its
	// give-up is written down until each area's recorder letters it (core/messaging's
	// recorder). See its declaration below for the four properties the capture rests on.
	MaxDeliveries = "max-deliveries"
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

// The GATEWAY shape, the second device-plane subject family a capture stream stores. A
// gateway is an ordinary device that fronts other devices (the edge agent is one); what it
// publishes for a child is addressed on the GATEWAY's own namespace,
//
//	{instance}.{tenant}.gateways.{gateway}.devices.{child}.{events|responses|reports}   uplink
//	{instance}.{tenant}.gateways.{gateway}.devices.{child}.{commands|desired}           downlink
//	{instance}.{tenant}.gateways.{gateway}.notices                                       downlink
//
// because a captured message does not say who published it: the broker's grant confines a
// login to the SUBJECT it may publish to, and the subject is the only place a gateway's
// identity can travel to the platform. The broker asserts the gateway (the grant is spliced
// from the authenticated token), the subject names the child, and ONE column
// (devices.gateway_id) decides whether that gateway may speak for that child.
//
// Declared here, once, beside the device-events shape: the capture stream's subjects, the
// event-sources capture filter and assertion parser, the device grant, the service deny and
// the edge agent's topic builder all derive from these segments (through core/messaging).
// The response/report/command/desired leaves are all in the v1 grant although only the
// events leaf is captured today, so adding their routing later changes no grant and no fleet
// has to reconnect.
const (
	SegmentGateways  = "gateways"
	SegmentNotices   = "notices"
	SegmentResponses = "responses"
	SegmentReports   = "reports"
	SegmentCommands  = "commands"
	SegmentDesired   = "desired"
)

// maxDeliveriesMaxBytesCap caps the max-delivery capture. An advisory is a few hundred
// bytes and the stream is a work queue, so it holds only advisories no recorder has
// finished with — steady state is empty. 8 MiB is some tens of thousands of them.
//
// 🔴 8, NOT 32, BECAUSE OF --compact. There the Cold ceiling is 16 MiB and binds before
// any larger cap would; taking all 16 left the compact volume (2Gi then, a 1 GiB store)
// 184 MiB unreserved, under cli/bootstrap's 192 MiB headroom floor
// (TestCompactReservationFitsItsSmallerVolume). At 8 MiB that headroom sat exactly ON
// the floor, and the next State bucket did push it under: the compact volume grew to
// 3Gi, a 2 GiB store, which leaves about a gigabyte over the floor. So 8 MiB is no
// longer pinned by the compact budget; it stays because steady state is empty, and
// raising it would buy nothing.
// Overflow is DiscardOld: the oldest unrecorded notices are evicted, which the
// MaxDeliveryRecordsWaiting and near-full alerts are there to make visible first.
const maxDeliveriesMaxBytesCap = 8 << 20

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
	// re-publish of a capture message event-sources had already forwarded but not yet
	// acked — the gap between "published to inbound-events" and "acked to the capture
	// stream" — and the same for the other ingest producers' retries. The capture durable
	// runs the platform retry contract, so while a consumer is running every re-publish
	// lands inside the redelivery span: (5-1)*2*60 s + 60 s = 540 s.
	//
	// Window: RedeliveryDuplicateWindowSeconds = 600 s >= 540 s. Cost at the 10,000
	// events/s reference load: 6M ids, ~720 MB on EVERY broker server (each replica keeps
	// its own table). It was 1800 s, sized to a bad pod restart, which held 18M ids
	// (~2.2 GB per server) — more than the broker's memory limit by itself. The budget
	// across every stream is TestDuplicateWindowsFitTheBrokerMemoryLimit (core/messaging).
	// The ids are small and fixed-size (tenant plus a capture sequence, ~25 bytes; see
	// processor.DedupID for why nothing device-controlled may enter them); an id family
	// with device-supplied content would make any window a memory-exhaustion vector.
	//
	// THE TRADE-OFF, stated plainly. A re-publish that waits for a POD RESTART is covered
	// only if the restart, plus the rest of the redelivery span, fits in the window: a
	// producer that died after its publish was stored but before it acked is covered for a
	// restart of up to about 8 minutes (600 s minus one AckWait for the redelivery, and the
	// fetch). Past that, the messages it had in flight at the crash — at most its publish
	// window plus one fetch batch — are stored twice. event-management's event key backstops
	// measurements; nothing backstops DETECT, which applies every delivery on first sight.
	// The HA answer is not a longer window but a second consumer: run event-sources and
	// device-management with 2+ replicas, and a dead pod's unacked input goes to a survivor
	// one AckWait later, inside the window.
	//
	// It applies BACKPRESSURE on device-management's backlog: an event device-management has
	// not resolved is an event nothing downstream has, so a full stream refuses new events
	// rather than discard one of those. device-management forwards into resolved-events, so
	// it stops reading here while that stream's gate is closed, and the backlog it leaves is
	// what closes this one.
	{Suffix: InboundEvents, Writers: []string{"device-state", "event-sources", "lwm2m-ingest", "sparkplug-ingest"}, Readers: []Reader{{"device-management", ReadDurable}}, Tier: Hot, DuplicateWindowSeconds: RedeliveryDuplicateWindowSeconds,
		DeadLetterKind:      kindEvent,
		BackpressureReaders: []string{"device-management"},
		Forwards:            map[string]string{"device-management": ResolvedEvents},
		Why:                 "raw device telemetry — the primary ingest path"},
	//
	// event-processing is REPLAY-COVERED: DETECT acks a resolved event only after a
	// snapshot checkpoint covers it, applies every delivery on first sight (true while the
	// partition lease keeps it a single writer), and after a restart replays this stream by
	// sequence from its last committed checkpoint — so a checkpoint outage that exhausts its
	// deliveries loses no event (see ReplayCovered for the split-brain residual).
	// device-state and event-management are not: their exhausted deliveries are lettered.
	//
	// device-management stamps every resolved publish with a Nats-Msg-Id that a redelivery of
	// its source reproduces (processor.resolvedDedupID), and a source is redelivered up to
	// MaxDeliver times: a publish stored on the first delivery whose acknowledgement was lost,
	// followed by failed publishes on the next deliveries, is published again up to 540 s later
	// (RedeliveryDuplicateWindowSeconds). The window is that redelivery span. It used to be the
	// broker's two-minute default, which a source that failed two deliveries in a row outlived,
	// and the repeat was stored twice — and DETECT applies every delivery on first sight.
	//
	// Window: RedeliveryDuplicateWindowSeconds = 600 s >= (5-1)*2*60 s + 60 s = 540 s.
	// Cost at the 10,000 events/s reference load: 6M ids, ~720 MB per broker server (it was
	// 1.2M, ~145 MB, at the default). Like inbound-events', it covers a device-management
	// restart of up to about 8 minutes; past that, the events in that pod's publish window are
	// stored twice, and 2+ device-management replicas are the HA answer.
	//
	// It applies BACKPRESSURE on event-management's backlog alone: an event it has not
	// persisted is lost when evicted. device-state is not listed, because its unread loss is
	// a stale live view that the device's next event repairs, and at the shipped sizing it is
	// the slowest reader, so gating on it would stop every tenant's ingest to let a projection
	// catch up. event-processing is not listed because its unread position is its checkpoint,
	// which its durable's backlog does not show exactly (see BackpressureReaders). That
	// backlog is still warned on (the chart's JetStreamDurableUnreadNearFull), as an upper
	// bound: the checkpoint is never behind what the durable has acknowledged.
	{Suffix: ResolvedEvents, PlatformAuthored: true, Writers: []string{"device-management"}, Readers: []Reader{{"device-state", ReadDurable}, {"event-management", ReadDurable}, {"event-management", ReadLive}, {"event-processing", ReadAckFloor}, {"event-processing", ReadEphemeral}}, Tier: Hot, DeadLetterKind: kindEvent,
		DuplicateWindowSeconds: RedeliveryDuplicateWindowSeconds,
		ReplayCovered:          []string{"event-processing"},
		BackpressureReaders:    []string{"event-management"},
		Why:                    "every ingested event after resolution; device-management produces it, every other area listed is a durable reader"},

	// One message per detection, and a subscribe-able product in its own right
	// (ADR-037): clients live-subscribe by tenant like any other event feed.
	//
	// The dedup window is what makes a DETECT replay cheap. After a restart DETECT replays
	// resolved-events from its last checkpoint and re-publishes every detection it had
	// already published past it; each carries a Nats-Msg-Id derived from the detection's
	// identity (runtime.DerivedEvent.DedupID), so the broker stores it once. Without the
	// window every re-published detection reached REACT again and charged the tenant's
	// outbound ceiling a second time — shedding, and dead-lettering, actions whose originals
	// had already been delivered. It is sized like inbound-events' to a bad rollout; a replay
	// older than it re-publishes duplicates. A sendCommand duplicate is still collapsed by its
	// idempotency key at the command sink. A connector duplicate is NOT: REACT's outbound gate
	// charges it again, and connector-dispatch's own window covers only REACT's redelivery span,
	// so a replay older than this one reaches the connectors service again. Collapsing it then
	// is the destination's job, on the forwarded idempotency key.
	//
	// Window: RestartDuplicateWindowSeconds = 1800 s >= a 15-minute restart allowance + the
	// 540 s redelivery span = 1440 s. Cost: the rule-firing rate times 1800 s, ~270 MB (67-byte ids) per
	// broker server per 1,000 detections/s.
	{Suffix: DerivedEvents, PlatformAuthored: true, Writers: []string{"event-processing"}, Readers: []Reader{{"event-processing", ReadDurable}, {"event-processing", ReadLive}}, Tier: Hot, DuplicateWindowSeconds: RestartDuplicateWindowSeconds,
		DeadLetterKind: kindDetectionAction, Why: "DETECT output — scales with rule firings against device traffic"},

	// Emitted post-commit when a numeric platform-set attribute (ADR-012 scope
	// SHARED/SERVER, DOUBLE/LONG) is upserted or deleted, so a DYNAMIC detection
	// threshold can read the device's own attribute instead of a compile-time
	// literal. At-most-once; the consumer keeps a durable projection, and a fact
	// that never reaches the stream is repaired by event-processing's reconcile
	// against device-management (at every DETECT term start and every five
	// minutes), not by replay.
	// Window: device-management publishes it with no Nats-Msg-Id (at-most-once).
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: DeviceAttribute, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, PlatformAuthored: true, Writers: []string{"device-management"}, Readers: []Reader{{"event-processing", ReadDurable}}, Tier: Hot, DeadLetterKind: kindControlFact, Why: "attribute set/delete per device — scales with fleet size"},

	// PER-DEVICE: the concrete subject carries the target device's token as a
	// final segment so the broker grant can confine a device to its own commands.
	// event-sources also has to recognize this suffix — to tell command traffic
	// from device telemetry — which is the case that first motivated centralizing
	// these names: a second literal is how the two drift apart.
	// Window: command-delivery publishes it with no Nats-Msg-Id.
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: DeviceCommands, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, PlatformAuthored: true, Writers: []string{"command-delivery"}, Readers: []Reader{{"lwm2m-ingest", ReadDurable}}, Tier: Hot, Shape: ShapeTenantDevice, DeadLetterKind: kindCommand, Why: "outbound commands — scale with fleet size"},

	// PER-DEVICE, PLATFORM -> DEVICE: the desired-state downlink (configuration revisions,
	// and later update assignments). Like DeviceCommands, the concrete subject ends in the
	// target device's token so the signed broker grant can confine a device to its own.
	//
	// device-management publishes a device's configuration revisions here, and
	// update-management its update assignments, download grants and cancellations. It is bounded
	// like every other stream: Limits retention, a week of history and the Cold tier's byte
	// ceiling, evicting the oldest, so an unread channel cannot grow without limit. Cold
	// because volume follows state CHANGES, not telemetry samples.
	// Window: device-management and update-management publish it with no Nats-Msg-Id.
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: DeviceDesired, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, PlatformAuthored: true, Writers: []string{"device-management", "update-management"}, Tier: Cold, Shape: ShapeTenantDevice, DeadLetterKind: kindControlFact,
		Why: "desired-state downlink per device - one message per state change"},

	// PER-DEVICE, DEVICE -> PLATFORM: progress and status reports. The device token in the
	// subject is the one AUTHENTICATED statement of who reported, for the reason given on
	// CommandResponses: a payload field is self-asserted. device-management consumes the
	// configuration kinds and update-management the update kinds, each on its own durable; bounded
	// exactly as DeviceDesired is.
	// Window: devices publish it, and nothing gives them a Nats-Msg-Id.
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	// event-sources is an area too: with the fail-closed MQTT acknowledgement it forwards
	// device reports from the MQTT gateway's message store into this stream.
	{Suffix: DeviceReports, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, Writers: []string{"event-sources"}, Readers: []Reader{{"device-management", ReadDurable}, {"update-management", ReadDurable}}, Tier: Cold, Shape: ShapeTenantDevice, DeadLetterKind: kindControlFact,
		Why: "device reports per device - one message per state change"},

	// ADR-030 amendment. The durable capture of raw device telemetry, and the
	// reason the gateway is no longer an MQTT client: the broker stores a device's
	// publish here itself, so the message is in a stream BEFORE our code runs. The
	// previous design acked the device from an in-memory channel, so anything
	// buffered at SIGKILL was silently lost — and could not be fixed by manual
	// acking, because NATS speaks MQTT 3.1.1, where a CleanSession disconnect
	// discards the session and shared subscriptions do not exist at all.
	//
	// 🔴 THE PUBACK DOES NOT WAIT FOR THIS STREAM. For QoS 1 the broker's MQTT gateway
	// publishes the message into the subject space with no reply subject and PUBACKs once
	// its own session store has it; this stream stores the message from its own ingest
	// queue, and a failure to store is only logged by the broker (nats-server's mqtt.go
	// and stream.go; the edge agent's spool comment records the same). So a refusal here
	// could never reach the device, which is why this stream does NOT apply backpressure:
	// discard-new would drop the NEWEST message after its PUBACK with nothing in the
	// platform counting it, where discard-old drops the oldest unread one and
	// event-sources' capture durable counts it as unread loss.
	//
	// event-sources forwards what it reads here into inbound-events, so it stops reading
	// while that stream applies backpressure, and the backlog waits here.
	//
	// SO ITS CEILING IS THE WHOLE MARGIN between devices publishing faster than event-sources
	// drains and losing PUBACKed events, and it takes the Hot tier's in full: an operator
	// sizing a fleet raises it with streamMaxBytes (and the volume), like every other Hot
	// stream. It used to be capped at 256 MiB, below the tier, when the disk budget had no
	// room for more. It is gone because it decided how soon events were lost and no setting
	// could move it: GKE perf round 12 offered 12,000/s over 3,000 MQTT sessions,
	// event-sources drained ~7,100/s, the backlog passed 256 MiB in about three minutes, and
	// 155,415 of 2,159,925 PUBACKed events (7.2%) were evicted unread, while streamMaxBytes
	// moved the tier and the cap stayed where it was. Under --compact the Hot tier is far below
	// either number and binds as before.
	//
	// Nothing in the platform publishes here; the producer is the device. Writers
	// are refused (messaging.WriteMessages) rather than allowed to publish to a
	// subject that matches no stream.
	//
	// IT IS A WORK QUEUE: the stream holds only what event-sources has not yet forwarded,
	// and an ack removes the message. Nothing reads it for history (the one reader is
	// event-sources' durable; rule preview and DETECT replay read resolved-events), so
	// keeping a forwarded publish would serve no consumer. Unread messages keep the
	// durability this stream exists for, because a work queue removes only on ack. A work
	// queue allows one consumer per subject, so no second reader may be attached.
	// RecreateOnRetentionChange lets an instance whose stream was created under Limits
	// retention move onto this one once the old stream has been drained.
	// Window: the broker's MQTT gateway stores device publishes here with no Nats-Msg-Id.
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: DeviceEventsCapture, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, Readers: []Reader{{"event-sources", ReadDurable}}, Tier: Hot, Shape: ShapeDeviceEvents,
		DeadLetterKind: kindEvent, Retention: RetentionWorkQueue, RecreateOnRetentionChange: true,
		Forwards: map[string]string{"event-sources": InboundEvents},
		Why:      "raw device publishes, captured by the broker and held only until forwarded — the ingest durability floor"},

	// PER-DEVICE, and it is the device token that makes a response ATTRIBUTABLE.
	//
	// 🔴 IT USED TO BE TENANT-SCOPED, AND THE ASYMMETRY WITH DeviceCommands WAS THE BUG.
	// Downlink was confined to a device's own subject while uplink was granted
	// tenant-wide, so any authenticated device could publish a response naming any
	// command token in its tenant and command-delivery would stamp it — the response
	// carried no identity, and batch tokens are enumerable, so a whole fleet write could
	// be marked complete with no device acting.
	//
	// The subject is the only place that identity can live. A deviceToken FIELD in the
	// payload is self-asserted and a forger simply fills it in; the broker does not
	// carry publisher identity into the message, and a device's JWT names its tenant,
	// not itself. The token here is spliced into the SIGNED grant by natsauth, so the
	// broker refuses a publish to anyone else's subject before our code ever runs —
	// which is what turns "the device says it is dev-1" into "the broker verified it".
	//
	// One consumer still reads every response: StreamSubject appends the extra wildcard
	// level for this shape, so the filter follows the declaration automatically. That
	// the two must move together is the point of deriving both from here.
	// Window: devices and lwm2m-ingest publish it with no Nats-Msg-Id.
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: CommandResponses, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, Writers: []string{"event-sources", "lwm2m-ingest"}, Readers: []Reader{{"command-delivery", ReadDurable}}, Tier: Hot, Shape: ShapeTenantDevice, DeadLetterKind: kindCommandResponse, Why: "device replies to commands — scale with fleet size"},

	// One message per fired httpCall/publish action. The stream is auto-provisioned
	// when event-processing creates the writer, so publishing is safe before the
	// consumer exists (a DeliverNew consumer skips the pre-consumer backlog).
	// CAVEAT: a DETECT replay after outbound-connectors deploys re-publishes
	// detections as NEW messages the consumer will run — a stale OccurredTime is
	// the consumer's drop/flag signal.
	//
	// Its give-ups are kept VERBATIM on connector-dispatch.dead, because a replay of an
	// outbound send has to be byte-identical; the dead letter is only the index entry.
	//
	// REACT re-publishes every connector action of a detection on each delivery of the derived
	// event, with the action's content-addressed token as the dedup id. The window is the
	// redelivery span (RedeliveryDuplicateWindowSeconds), so a re-publish from a retry, or
	// from both replicas during a lease handover, is stored once. It is NOT sized to a DETECT
	// replay: derived-events' own window collapses those before REACT sees them. Past it the
	// connectors service runs the request again, and the forwarded idempotency key is the
	// destination's to honour.
	//
	// Window: RedeliveryDuplicateWindowSeconds = 600 s >= (5-1)*2*60 s + 60 s = 540 s. Cost:
	// the connector-action rate times 600 s, ~72 MB per broker server per 1,000 actions/s.
	{Suffix: ConnectorDispatch, PlatformAuthored: true, Writers: []string{"event-processing"}, Readers: []Reader{{"outbound-connectors", ReadDurable}}, Tier: Hot, DuplicateWindowSeconds: RedeliveryDuplicateWindowSeconds,
		DeadLetterKind: kindConnectorDispatch, VerbatimCopy: ConnectorDispatchDead, Why: "REACT outbound dispatch — scales with rule firings"},

	// ---- Control plane (Cold): volume cannot scale with device count ----

	// Emitted post-commit on profile publish and rollback, carrying the ENABLED
	// rules of the now-active version, keyed on profile-version token, and the
	// stored activation instant. At-most-once; the consumer keeps a durable
	// projection, and a fact that never reaches the stream is repaired by
	// event-processing's reconcile against device-management (at every DETECT term
	// start and every five minutes), not by replay.
	// Window: device-management publishes it with no Nats-Msg-Id (at-most-once).
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: DetectionRulesPublished, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, PlatformAuthored: true, Writers: []string{"device-management"}, Readers: []Reader{{"event-processing", ReadDurable}}, Tier: Cold, DeadLetterKind: kindControlFact, Why: "a rule publish — a human authoring action"},

	// Emitted post-commit when a device is created or re-typed, naming the device
	// and the stable profile token its type adopts, so DETECT can arm absence for
	// a device that has NEVER reported (the dead-man roster). Removal rides the
	// entity-deleted fact rather than this one. At-most-once; the consumer keeps a
	// durable projection, and a fact that never reaches the stream is repaired by
	// event-processing's reconcile against device-management (at every DETECT term
	// start and every five minutes), not by replay.
	// Window: device-management publishes it with no Nats-Msg-Id (at-most-once).
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: DeviceRoster, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, PlatformAuthored: true, Writers: []string{"device-management"}, Readers: []Reader{{"command-delivery", ReadDurable}, {"event-processing", ReadDurable}, {"update-management", ReadDurable}}, Tier: Cold, DeadLetterKind: kindControlFact, Why: "roster projection updates"},

	// Emitted post-commit whenever a geofence change mints a new fence-set version, naming that
	// version's fences and the content address of each one's geometry so event-processing's live
	// containment projection can resolve whatever it does not already hold. On its own subject
	// so a consumer that predates it never receives one — see GeoFenceSetManifest for why that
	// separation is load-bearing.
	//
	// At-most-once, and unlike the other control-plane facts its consumer keeps NO durable
	// projection: the frozen manifests and the geometry archive are already durable in
	// device-management, so a restarted processor re-seeds by READING them back rather than from
	// a second copy of its own. A fact that never reaches the stream is recovered by that startup
	// reconcile, not by replay. Its size depends on the fence COUNT and on nothing the fences
	// contain, so no fence set can outgrow one message.
	// Window: device-management publishes it with no Nats-Msg-Id (at-most-once).
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: GeoFenceSetManifest, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, PlatformAuthored: true, Writers: []string{"device-management"}, Readers: []Reader{{"event-processing", ReadDurable}}, Tier: Cold, DeadLetterKind: kindControlFact, Why: "a fence edit — a human authoring action"},

	// ADR-044. Emitted when an edge entity (device, customer, area, asset, and
	// their groups) is deleted, so cross-service reference holders — such as
	// event-management's event_anchors — can reconcile dangling references.
	// At-least-once and idempotent.
	// Window: device-management publishes it with no Nats-Msg-Id; its consumers are idempotent.
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: EntityDeleted, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, PlatformAuthored: true, Writers: []string{"device-management"}, Readers: []Reader{{"dashboard-management", ReadDurable}, {"event-management", ReadDurable}, {"event-processing", ReadDurable}}, Tier: Cold, DeadLetterKind: kindControlFact, Why: "entity lifecycle fan-out"},

	// ADR-081. Emitted post-commit by device-management whenever a device's tracked
	// relationships may have changed (an edge created or removed, a claim redeemed or
	// reopened, a relationship type's tracked flag flipped, the target of a tracked edge
	// deleted), carrying the device's complete current anchor set. A statement of state,
	// not a delta, so a consumer overwrites what it holds and a duplicate or reorder is
	// harmless. At-most-once: a lost fact is repaired by the device's next resolved event,
	// which carries the same anchors.
	// Window: device-management publishes it with no Nats-Msg-Id (at-most-once).
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: DeviceOwnership, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, PlatformAuthored: true, Writers: []string{"device-management"}, Readers: []Reader{{"device-state", ReadDurable}}, Tier: Cold, DeadLetterKind: kindControlFact, Why: "a relationship edit — an operator assigning or claiming a device"},

	// ADR-041, re-emitted on each alarm transition. The substrate for graphql-ws
	// subscriptions (ADR-037) and for notifications (ADR-017).
	// Window: device-management publishes it with no Nats-Msg-Id.
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: AlarmEvents, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, PlatformAuthored: true, Writers: []string{"device-management"}, Readers: []Reader{{"notification-management", ReadDurable}, {"device-management", ReadLive}}, Tier: Cold, DeadLetterKind: kindNotification, Why: "alarm state changes, not raw telemetry"},

	// ADR-051 5c / ADR-054, carrying a JSON RaiseAlarmRequest. AT-LEAST-once, and
	// safe as such: ApplyAlarmContributorEdge (ADR-057) is an idempotent
	// contributor-set fold keyed on (device, alarmKey) with a per-contributor
	// monotonic decision timestamp. Since the ADR-057 cutover retired the
	// measurement evaluator this is the SOLE alarm-raise path — there is no peer
	// to double-raise against.
	//
	// REACT re-publishes each alarm edge on every delivery of the derived event, under an
	// edge-scoped token as the dedup id, and the window covers that redelivery span. The fold
	// would absorb the repeat anyway; the window means device-management does not have to.
	//
	// Window: RedeliveryDuplicateWindowSeconds = 600 s >= (5-1)*2*60 s + 60 s = 540 s. Cost:
	// the alarm-edge rate times 600 s, ~72 MB per broker server per 1,000 edges/s.
	{Suffix: RaiseAlarm, PlatformAuthored: true, Writers: []string{"event-processing"}, Readers: []Reader{{"device-management", ReadDurable}}, Tier: Cold,
		DuplicateWindowSeconds: RedeliveryDuplicateWindowSeconds, DeadLetterKind: kindDetectionAction, Why: "REACT alarm requests"},

	// Window: event-sources publishes it with no Nats-Msg-Id.
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: FailedDecode, DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, Writers: []string{"event-sources"}, Tier: Cold, DeadLetterKind: NotLettered, Why: "error path — near zero in steady state; see the spike caveat below"},
	// device-management publishes each failure record with an id its source's redelivery
	// reproduces (processor.sourceDedupID), on the same redelivery span as resolved-events;
	// event-management's records carry none. It used to be the broker's two-minute default.
	//
	// Window: RedeliveryDuplicateWindowSeconds = 600 s >= (5-1)*2*60 s + 60 s = 540 s. Cost:
	// ~0 in steady state; a spike at the full 10,000 events/s holds 6M ids, ~720 MB per server.
	{Suffix: FailedEvents, PlatformAuthored: true, Writers: []string{"device-management", "event-management"}, Tier: Cold, DeadLetterKind: NotLettered,
		DuplicateWindowSeconds: RedeliveryDuplicateWindowSeconds, Why: "error path — near zero in steady state; see the spike caveat below"},
	// The dedup window is what lets the connectors service's own arm and the platform's
	// max-delivery recorder both write the verbatim copy of one give-up and store it
	// ONCE: both carry the same id, derived from the original's stream position.
	//
	// Window: RestartDuplicateWindowSeconds = 1800 s >= a 15-minute restart allowance (a
	// recorder down for a pod restart) + the 540 s redelivery span = 1440 s. Cost: ~0, one id
	// per give-up.
	{Suffix: ConnectorDispatchDead, Writers: []string{"outbound-connectors"}, Tier: Cold, DuplicateWindowSeconds: RestartDuplicateWindowSeconds,
		DeadLetterKind: NotLettered, Why: "terminal dead-letter sink (ADR-060 SD-2)"},
	// 🔴 user-management IS IN THIS LIST BECAUSE IT READS, NOT BECAUSE IT WRITES. The areas
	// are the ADR-020 A0 deployment predicate, and NewReader creates the stream exactly as
	// NewWriter does — so a profile running the store but none of the four producers would
	// otherwise have a stream the replication check believes should not be there.
	//
	// Every area that reads a stream writes here: each runs a max-delivery recorder that
	// letters its own abandoned deliveries. The 30-minute dedup window is what makes a
	// give-up recorded by BOTH an in-handler arm and that recorder land once — both carry
	// an id derived from the original's (stream, durable, sequence). It is sized to cover
	// an arm that overran its ack window by a long way, and a recorder that was down for
	// a pod restart, not an outage of any length: past it a second letter is a benign
	// duplicate, never a loss.
	//
	// Window: RestartDuplicateWindowSeconds = 1800 s >= a 15-minute restart allowance + the
	// 540 s redelivery span = 1440 s. Cost: ~0 in steady state, one id per letter.
	{Suffix: DeadLetters,
		RecorderWrites: true, Readers: []Reader{{"user-management", ReadDurable}, {"command-delivery", ReadDurable}},
		Tier: Cold, DuplicateWindowSeconds: RestartDuplicateWindowSeconds, DeadLetterKind: NotLettered,
		Why: "ADR-024 dead-letter sink: every area that consumes a stream writes it, user-management " +
			"stores it, command-delivery reads it back to settle the commands its letters are about"},

	// The max-delivery capture. Four properties it rests on, each with a consequence an
	// operator can see:
	//
	//  1. INTEREST MUST EXIST BEFORE THE NEXT PULL. nats-server publishes the advisory on
	//     the next pull of the exhausted durable, and only if something is subscribed to
	//     its subject at that moment. This stream is that something, so an advisory for a
	//     stream this capture does not list (yet) is produced into nothing.
	//  2. AN OLD POD CAN FLIP THE SUBJECT LIST BACK. Every area reconciles this stream's
	//     subjects on start, so during a rollout a restarting old pod can drop a stream
	//     newly declared here; that stream's advisories have no interest until a new pod
	//     restarts. The same flip applies to each area's recorder durable, whose filter is
	//     reconciled to the starting pod's readers: an old pod of an area restarting
	//     mid-rollout drops a newly added reader's advisory subject from it. Those
	//     advisories are still captured and wait on this queue until a new pod of the area
	//     restarts, so they are late, not lost (the alert over this stream sees the wait).
	//  3. IT IS A WORK QUEUE, so an acked advisory leaves the stream and its message count
	//     is the unrecorded backlog. That is what the MaxDeliveryRecordsWaiting alert
	//     reads, and why nothing may read this stream's cursor gaps as unread loss.
	//  4. A LEADERLESS STREAM DROPS THE BROKER'S INTERNAL PUBLISH. There is no retry on the
	//     server side; an advisory emitted while this stream has no leader is gone.
	//
	// Window: nats-server publishes the advisories with no Nats-Msg-Id.
	// 0 ids are held, so the window is inert; it is the broker default, so declaring it moves nothing.
	{Suffix: MaxDeliveries,
		Tier: Cold, Shape: ShapeAdvisory, Retention: RetentionWorkQueue, MaxBytesCap: maxDeliveriesMaxBytesCap,
		DuplicateWindowSeconds: BrokerDefaultDuplicateWindowSeconds, DeadLetterKind: NotLettered,
		Why: "broker advisories for deliveries that ran out — empty in steady state"},
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

// RetentionFor returns a suffix's declared retention. An undeclared suffix reads as
// RetentionLimits, but nothing creates a stream for one (see IsDeclared).
func RetentionFor(suffix string) Retention {
	return bySuffix[suffix].Retention
}

// RecreatesOnRetentionChange reports whether an existing stream of suffix with a different
// retention may be drained and recreated (Stream.RecreateOnRetentionChange).
func RecreatesOnRetentionChange(suffix string) bool {
	return bySuffix[suffix].RecreateOnRetentionChange
}

// PlatformAuthoredFor reports whether suffix's stream is declared platform-authored. A
// suffix this package does not declare is not: the caller redacts.
func PlatformAuthoredFor(suffix string) bool {
	return bySuffix[suffix].PlatformAuthored
}

// KindPlatformAuthored reports whether every stream declaring dead-letter kind is
// platform-authored (and at least one does). It answers for a letter written with no origin
// stream, where the kind is all that is known; a kind shared with any device-authored
// stream is not.
func KindPlatformAuthored(kind string) bool {
	found := false
	for _, st := range All {
		if st.DeadLetterKind != kind {
			continue
		}
		if !st.PlatformAuthored {
			return false
		}
		found = true
	}
	return found
}

// DeadLetterKindFor returns the dead-letter kind declared for a message consumed from
// suffix's stream: a core/deadletter Kind value, NotLettered, or "" for a suffix this
// package does not declare. A caller treats "" as a defect, never as permission.
func DeadLetterKindFor(suffix string) string {
	return bySuffix[suffix].DeadLetterKind
}

// VerbatimCopyFor returns the suffix holding a byte-identical copy of a give-up on
// suffix's stream, or "" when the stream declares none.
func VerbatimCopyFor(suffix string) string {
	return bySuffix[suffix].VerbatimCopy
}

// ReplayCoveredBy reports whether area's durable on suffix's stream is declared
// replay-covered: its exhausted deliveries lose nothing, so they are counted rather than
// dead-lettered. See Stream.ReplayCovered.
func ReplayCoveredBy(suffix, area string) bool {
	for _, a := range bySuffix[suffix].ReplayCovered {
		if a == area {
			return true
		}
	}
	return false
}

// BackpressureReadersFor returns the areas whose unread backlog on suffix's stream makes it
// refuse its writers, or nil for a stream that never refuses. See Stream.BackpressureReaders.
func BackpressureReadersFor(suffix string) []string {
	return bySuffix[suffix].BackpressureReaders
}

// AppliesBackpressure reports whether suffix's stream refuses its writers while a declared
// reader's unread backlog nears its ceiling.
func AppliesBackpressure(suffix string) bool {
	return len(bySuffix[suffix].BackpressureReaders) > 0
}

// ForwardsInto returns the stream area forwards what it reads from suffix's stream into, or
// "" when it forwards nowhere that applies backpressure. See Stream.Forwards.
func ForwardsInto(suffix, area string) string {
	return bySuffix[suffix].Forwards[area]
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
