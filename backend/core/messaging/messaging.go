// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The COMMAND-PLANE subject suffixes, re-exported from the single stream
// declaration in core/streams so transport call sites can name them without
// reaching past the messaging layer. They are aliases, not copies: a rename in
// the declaration breaks the build here rather than leaving a stale literal that
// silently stops matching.
const (
	// SubjectDeviceCommands carries persisted commands OUT to devices. It is
	// PER-DEVICE: the concrete subject carries the target device's token as a final
	// segment, "{instance}.{tenant}.device-commands.{deviceToken}", so the broker
	// grant can confine a device to its own commands. See DeviceScopedSubject.
	SubjectDeviceCommands = streams.DeviceCommands
	// SubjectCommandResponses carries a device's response to a command back IN. It
	// is tenant-scoped, not per-device: every device publishes to the one subject a
	// single consumer reads, and a response names its command by token.
	SubjectCommandResponses = streams.CommandResponses
)

// IsPerDeviceSuffix reports whether a suffix addresses an individual device.
//
// The answer comes from the stream declaration rather than a set kept here,
// because the STREAM shape and the PUBLISH shape are decided in different places
// — the stream at ensureStream, the subject at write time. Deriving both from one
// declaration is what stops them disagreeing, which would show up as publishes
// silently landing in no stream at all.
func IsPerDeviceSuffix(suffix string) bool {
	return streams.IsPerDevice(suffix)
}

// DeviceScopedSubject builds the concrete per-device subject a command is
// published to: "{instance}.{tenant}.{suffix}.{deviceToken}".
func DeviceScopedSubject(instanceId, tenant, suffix, deviceToken string) string {
	return fmt.Sprintf("%s.%s.%s.%s", instanceId, tenant, suffix, deviceToken)
}

// ConcreteSubjectFor builds the concrete subject a declared stream's producer
// publishes to, dispatching on the stream's shape.
//
// It exists because callers were reconstructing this by hand — "ScopedSubject,
// unless IsPerDeviceSuffix, then DeviceScopedSubject" — which is a copy of the
// shape arithmetic that does not fail when a new shape appears; it just quietly
// returns a subject the stream does not have. Both subject-disjointness tests
// were doing exactly that, and both would have gone on passing about a fiction
// when the capture stream landed. One constructor means a new shape breaks the
// build at every site instead.
//
// deviceToken is used only by the shapes that carry one.
func ConcreteSubjectFor(instanceId, tenant, suffix, deviceToken string) string {
	switch streams.ShapeOf(suffix) {
	case streams.ShapeTenantDevice:
		return DeviceScopedSubject(instanceId, tenant, suffix, deviceToken)
	case streams.ShapeDeviceEvents:
		return DeviceEventsSubject(instanceId, tenant, deviceToken)
	default:
		return ScopedSubject(instanceId, tenant, suffix)
	}
}

// StreamSubject is the subject pattern a suffix's stream captures, per its
// declared shape. A tenant-scoped suffix captures "{instance}.*.{suffix}"; a
// per-device one captures an extra level, "{instance}.*.{suffix}.*", so every
// device's subject falls inside one stream while each device is granted only its
// own; a device-events CAPTURE stream captures the inbound telemetry shape,
// "{instance}.*.devices.*.events", where the suffix names the stream but appears
// nowhere in the subject.
func StreamSubject(instanceId, suffix string) string {
	switch streams.ShapeOf(suffix) {
	case streams.ShapeTenantDevice:
		return WildcardSubject(instanceId, suffix) + ".*"
	case streams.ShapeDeviceEvents:
		return DeviceEventsWildcard(instanceId)
	default:
		return WildcardSubject(instanceId, suffix)
	}
}

// Message is a transport-neutral message envelope used by producers and
// consumers across services. It replaces the previous direct dependency on
// segmentio/kafka-go's Message type.
//
//   - On publish, the subject is always derived from the tenant in context
//     ("{instance}.{tenant}.{suffix}", fail-closed); the Subject field is not
//     read by the writer.
//   - On delivery, Subject is the concrete subject the message was published to,
//     carrying the tenant as its second dot-segment (see ParseTenantFromSubject).
//
// Key is retained for producer-side fidelity (it was the Kafka partition key);
// it is currently producer-local and not transmitted, since no consumer reads it.
//
// NumDelivered is the JetStream delivery attempt count for a consumed message
// (1 on first delivery); it is 0 for produced or synthetic (unit-test) messages.
// Consumers use it to bound redelivery before routing a poison message to the
// dead-letter path (see MaxDeliver).
//
// A consumed message also carries an opaque acknowledger (set by the reader) so
// the consumer can Ack it only after the message has been durably handled.
// Produced and synthetic messages carry none, so Ack is a no-op on them — tests
// and producers need not change.
//
// There is deliberately NO Nak: a transient handling failure is retried by simply
// NOT acking, which lets the consumer's AckWait (60s) pace redelivery so MaxDeliver
// spans minutes. An explicit Nak asks the broker to redeliver IMMEDIATELY (~1 RTT),
// which turns MaxDeliver into a ~1.4ms fuse that blows INSIDE any downstream outage
// and dead-letters the whole flow — the platform-wide bug this type no longer lets a
// consumer wire. The reference disposition, with the full rationale, is the settler
// in event-sources' gateway_jetstream.go (ADR-030).
type Message struct {
	Subject      string
	Key          []byte
	Value        []byte
	NumDelivered int
	// StreamSeq is the JetStream stream sequence of a consumed message — a durable,
	// gapless, monotonically-increasing position in the stream. It is the stable id a
	// checkpointing consumer (the event-processing DETECT engine, ADR-051) needs to
	// dedup already-applied events after a restart-and-replay: the engine records the
	// highest applied StreamSeq in its committed snapshot and drops any redelivery at
	// or below it. It is 0 for produced/synthetic messages and for a consumed message
	// whose broker metadata is unavailable (non-JetStream), so consumers that don't
	// checkpoint are unaffected.
	StreamSeq uint64
	// AppendTime is the broker's timestamp for when a consumed message was written
	// to the stream — server-assigned, never supplied by the publisher. For the
	// ingest capture stream (ADR-030) the write happens before the device is
	// PUBACKed, so it marks when the message REACHED the platform, not when we got
	// round to reading it.
	//
	// That distinction only matters when a consumer is behind, and then it matters
	// a great deal: a per-tenant rate gate fed arrival time meters how fast WE are
	// draining, so a recovered backlog — which drains at fetch speed by definition —
	// looks like one enormous burst and is shed, discarding messages the device was
	// already told were safe. Fed this instead, the same backlog is metered against
	// the timeline it was actually produced on (core.TenantRateLimiter.AllowAt).
	//
	// It is the zero time for produced/synthetic messages and for a consumed message
	// whose broker metadata is unavailable (non-JetStream), which AllowAt reads as
	// "now" — the correct default for any path that is not replaying a backlog.
	AppendTime time.Time
	// DedupID, when non-empty, is published as the JetStream "Nats-Msg-Id" header,
	// making the write idempotent within the target stream's DuplicateWindow: a
	// second publish carrying an id already seen is acknowledged as success and
	// stored ONCE (ADR-030 amendment).
	//
	// Two properties of the mechanism decide how an id may be built, and both have
	// already produced defects here. It is STREAM-scoped, not tenant-scoped, so an
	// id that is not unique across tenants makes one tenant's message suppress
	// another's — a silent, unrecoverable loss that is worse than the duplicate it
	// was meant to prevent. And it dedups only what it can RECOGNIZE, so an id that
	// is not byte-identical across a redelivery of the same message silently does
	// nothing at all. An id must therefore be both tenant-scoped and derived only
	// from values that survive a redelivery unchanged.
	//
	// It is 0-cost when unset: no header, no dedup, and the stream's window is
	// irrelevant. That is the correct posture for a producer whose duplicates are
	// prevented some other way — the HTTP ingest path has no broker redelivery at
	// all, so it sets none.
	DedupID string
	// Headers carries transport metadata across the pipeline (E15). Today it holds
	// the correlation id used to follow a message through event-sources ->
	// device-management -> event-management/device-state. It is transmitted via
	// NATS message headers by the writer and populated from them by the reader.
	Headers map[string]string

	ack Acknowledger
}

// HeaderCorrelationID is the message-header key for the correlation id that ties
// together every message derived from one originating event (E15).
const HeaderCorrelationID = "Dc-Correlation-Id"

// CorrelationID returns the message's correlation id, or "" when none is set.
func (m Message) CorrelationID() string {
	if m.Headers == nil {
		return ""
	}
	return m.Headers[HeaderCorrelationID]
}

// WithCorrelationID returns a copy of the message carrying id as its correlation
// id, so a producer can propagate the id from the message that triggered it onto
// the messages it emits. The headers map is copied so the original is untouched.
func (m Message) WithCorrelationID(id string) Message {
	headers := make(map[string]string, len(m.Headers)+1)
	for k, v := range m.Headers {
		headers[k] = v
	}
	headers[HeaderCorrelationID] = id
	m.Headers = headers
	return m
}

// Acknowledger is the transport-neutral ack handle threaded onto a consumed
// Message. The NATS reader implements it over the underlying *nats.Msg; it is
// kept off the public surface so the envelope stays transport-neutral and so a
// MessageReader can be mocked without one.
type Acknowledger interface {
	Ack() error
}

// NewConsumedMessage builds a consumed message carrying its delivery count,
// headers, and ack handle. Only message readers construct these; producers and
// tests use the plain struct literal (no acknowledger, so Ack is a no-op).
func NewConsumedMessage(subject string, value []byte, numDelivered int, headers map[string]string, ack Acknowledger) Message {
	return Message{Subject: subject, Value: value, NumDelivered: numDelivered, Headers: headers, ack: ack}
}

// Ack acknowledges that the message has been durably handled so it is not
// redelivered. It is a no-op when the message carries no acknowledger (a
// produced or synthetic message), so producers and unit tests are unaffected.
func (m Message) Ack() error {
	if m.ack == nil {
		return nil
	}
	return m.ack.Ack()
}

// MessageReader is the consumer-side abstraction (kept small for unit testing).
type MessageReader interface {
	ReadMessage(ctx context.Context) (Message, error)
	HandleResponse(err error)
}

// ReplayReader is a bounded, strictly-ordered read of a stream from a start
// sequence up to the head captured when it was opened. Read returns each message in
// ascending stream-sequence order and then io.EOF once the head has been delivered.
//
// It exists for the DETECT engine's restart replay (ADR-051): a durable consumer's
// post-crash redelivery is NOT ordered — a message held in Ack Pending past AckWait
// can be overtaken by a newer delivery — which would let the engine's idempotent
// "seq <= lastSeq" guard permanently discard a never-applied event. Replaying by
// sequence, in order, from the committed snapshot up to the head is what makes
// restart-and-replay correct. Kept transport-neutral so the consumer can be tested
// without a broker. Close releases the underlying ephemeral consumer.
type ReplayReader interface {
	Read(ctx context.Context) (Message, error)
	Close() error
}

// MessageWriter is the producer-side abstraction (kept small for unit testing).
type MessageWriter interface {
	WriteMessages(ctx context.Context, msgs ...Message) error
	// WriteToDevice publishes to a PER-DEVICE subject
	// ("{instance}.{tenant}.{suffix}.{deviceToken}") rather than the tenant-wide one,
	// so the broker grant can confine a device to its own messages instead of relying
	// on every device to filter correctly.
	//
	// It is a separate method rather than a field on Message so the compiler finds
	// every caller if the addressing changes again, and so a per-device suffix cannot
	// be published to by the tenant-wide path by omission.
	WriteToDevice(ctx context.Context, deviceToken string, msgs ...Message) error
	HandleResponse(err error)
}

// ParseTenantFromSubject extracts the tenant id from a messaging subject built
// as "{instanceId}.{tenantId}.{suffix}" (and the ADR-006/ADR-048 MQTT mapping
// "{instanceId}.{tenant}.devices.{token}.events" — the tenant is the second
// segment in both). It returns the second dot-segment and true on a match, or ("", false)
// when the subject does not have at least three non-empty leading segments. Used
// by ingest consumers to derive a per-message tenant for WithTenant.
func ParseTenantFromSubject(subject string) (string, bool) {
	parts := strings.SplitN(subject, ".", 3)
	if len(parts) < 3 {
		return "", false
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[1], true
}

// TenantContextFromSubject derives the tenant from a scoped subject (see
// ParseTenantFromSubject) and returns a tenant-scoped context together with the
// tenant id. ok is false when no tenant is parseable, in which case the caller
// must NOT process the message (fail-closed). Used by ingest consumers so the
// per-message tenant flows into the tenant-scope DB callbacks and can also be
// threaded onto downstream produced messages.
func TenantContextFromSubject(ctx context.Context, subject string) (context.Context, string, bool) {
	tenant, ok := ParseTenantFromSubject(subject)
	if !ok {
		return ctx, "", false
	}
	return core.WithTenant(ctx, tenant), tenant, true
}

// ScopedSubject builds the fully-scoped publish subject for a tenant:
// "{instanceId}.{tenant}.{suffix}".
func ScopedSubject(instanceId, tenant, suffix string) string {
	return fmt.Sprintf("%s.%s.%s", instanceId, tenant, suffix)
}

// WildcardSubject builds the cross-tenant subscribe filter for the shared
// (single pod-set per instance) microservice model: "{instanceId}.*.{suffix}".
// One consumer receives every tenant's messages for the suffix and derives the
// per-message tenant from the delivered subject.
func WildcardSubject(instanceId, suffix string) string {
	return fmt.Sprintf("%s.*.%s", instanceId, suffix)
}

// The device-events subject is the ONE subject a device may publish telemetry to:
// "{instance}.{tenant}.devices.{deviceToken}.events" (MQTT topic
// "{instance}/{tenant}/devices/{deviceToken}/events").
//
// Its shape is declared here, once, because three places must agree on it and they
// live in different modules: the broker grant that confines a device to its own
// topic (core/natsauth), the gateway subscription that ingests telemetry
// (event-sources), and the parser that recovers the device token from a delivered
// topic (event-sources/processor). They previously carried three independent
// copies of the literals, and the copies drifted — see DeviceEventsWildcard.
const (
	// SegmentDevices and SegmentEvents bracket the device token, which is what
	// makes the shape distinguishable from every internal subject: no suffix in
	// streams.All is named "devices", so nothing internal can match this pattern.
	//
	// They are declared in core/streams and re-exported here. The capture stream
	// (streams.DeviceEventsCapture) became a fourth agreeing party to this shape,
	// and its declaration is upstream of this package — so the literals moved to
	// the leaf, which is the only place all four can reach without a cycle.
	SegmentDevices = streams.SegmentDevices
	SegmentEvents  = streams.SegmentEvents

	// Segment count and the position of each fixed segment within a well-formed
	// device events subject/topic: {instance}/{tenant}/devices/{token}/events.
	DeviceEventsSegmentCount = 5
	DeviceEventsDevicesIndex = 2
	DeviceEventsTokenIndex   = 3
	DeviceEventsEventsIndex  = 4
)

// DeviceEventsSubject builds the concrete subject a single device publishes its
// telemetry to. The broker grant is minted from this, so a device is confined to
// exactly the subject this returns.
func DeviceEventsSubject(instanceId, tenant, deviceToken string) string {
	return fmt.Sprintf("%s.%s.%s.%s.%s", instanceId, tenant, SegmentDevices, deviceToken, SegmentEvents)
}

// DeviceEventsWildcard is the subject pattern covering every device's telemetry
// for an instance: "{instance}.*.devices.*.events".
//
// This is deliberately NARROW, and the narrowness is the point. The gateway
// subscription used to be "{instance}.*.>", which also covered the subjects
// event-sources itself publishes to ("{instance}.{tenant}.inbound-events",
// ".failed-decode", …). The service therefore received its own internal traffic
// back, metered it a SECOND time against the publishing tenant's ingest bucket,
// failed to decode it as device telemetry, and dead-lettered it — so a tenant
// configured for 1000 msg/s got roughly 600 with no burst at all, and the
// failed-decode stream filled with internal envelopes.
//
// The earlier fix denylisted the two command-plane suffixes it knew about, which
// closed that instance and left the class: every other internal suffix was still
// caught. Matching the grant exactly is what closes the class — an internal
// subject cannot be granted to a device, so it cannot be ingested as telemetry.
func DeviceEventsWildcard(instanceId string) string {
	return fmt.Sprintf("%s.*.%s.*.%s", instanceId, SegmentDevices, SegmentEvents)
}

// SubjectToMqttTopic maps a NATS subject (or subject pattern) to the MQTT topic
// the NATS MQTT gateway exposes it as: "." becomes "/", and the NATS wildcards
// "*" and ">" become their MQTT equivalents "+" and "#".
//
// The gateway subscribes over MQTT while every other producer and consumer speaks
// subjects, so a topic literal written by hand would be a fourth copy of the shape
// with no compiler or test tying it to the other three. Deriving it instead means
// the subscription cannot drift from the grant.
//
// PRECONDITION: no segment may itself contain "." or "/". This is a simplified
// mapping, not the gateway's full codec — the server also escapes a literal "."
// inside an MQTT topic level as "//", which this does not reproduce. Every segment
// used here is an instance id, tenant or device token, all constrained to
// [A-Za-z0-9][A-Za-z0-9_-]* by core.ValidateToken, so the escape case cannot
// arise. Do not reach for this on operator- or device-supplied strings.
func SubjectToMqttTopic(subject string) string {
	return strings.NewReplacer(".", "/", "*", "+", ">", "#").Replace(subject)
}

// JetStream stream and durable-consumer names share the subject token space but
// disallow ".", "*", ">", "/", "\\" and whitespace. sanitizeName replaces any
// such character with "_" so an instance id / suffix can seed a valid name.
func sanitizeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', '*', '>', '/', '\\', ' ', '\t', '\n':
			return '_'
		default:
			return r
		}
	}, name)
}

// StreamName is the per-suffix JetStream stream name for an instance. The stream
// captures every tenant's subjects for that suffix (see WildcardSubject).
func StreamName(instanceId, suffix string) string {
	return sanitizeName(fmt.Sprintf("%s_%s", instanceId, suffix))
}

// DurableName is the durable pull-consumer name. It is scoped to the instance,
// functional area and suffix but NOT the tenant: in the shared model one durable
// consumer drains all tenants' messages (multiple pods of the same functional
// area share it and NATS distributes messages across them).
func DurableName(instanceId, functionalArea, suffix string) string {
	return sanitizeName(fmt.Sprintf("%s_%s_%s", instanceId, functionalArea, suffix))
}
