// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

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
// the consumer can Ack it only after the message has been durably handled, or
// Nak it to request redelivery. Produced and synthetic messages carry none, so
// Ack/Nak are no-ops on them — tests and producers need not change.
type Message struct {
	Subject      string
	Key          []byte
	Value        []byte
	NumDelivered int

	ack Acknowledger
}

// Acknowledger is the transport-neutral ack handle threaded onto a consumed
// Message. The NATS reader implements it over the underlying *nats.Msg; it is
// kept off the public surface so the envelope stays transport-neutral and so a
// MessageReader can be mocked without one.
type Acknowledger interface {
	Ack() error
	Nak() error
}

// NewConsumedMessage builds a consumed message carrying its delivery count and
// ack handle. Only message readers construct these; producers and tests use the
// plain struct literal (no acknowledger, so Ack/Nak are no-ops).
func NewConsumedMessage(subject string, value []byte, numDelivered int, ack Acknowledger) Message {
	return Message{Subject: subject, Value: value, NumDelivered: numDelivered, ack: ack}
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

// Nak negatively-acknowledges the message, asking the broker to redeliver it
// later (for a transient handling failure). It is a no-op when the message
// carries no acknowledger.
func (m Message) Nak() error {
	if m.ack == nil {
		return nil
	}
	return m.ack.Nak()
}

// MessageReader is the consumer-side abstraction (kept small for unit testing).
type MessageReader interface {
	ReadMessage(ctx context.Context) (Message, error)
	HandleResponse(err error)
}

// MessageWriter is the producer-side abstraction (kept small for unit testing).
type MessageWriter interface {
	WriteMessages(ctx context.Context, msgs ...Message) error
	HandleResponse(err error)
}

// ParseTenantFromSubject extracts the tenant id from a messaging subject built
// as "{instanceId}.{tenantId}.{suffix}" (and the ADR-006 MQTT mapping
// "dc.{tenant}.devices.{token}.events" — the tenant is the second segment in
// both). It returns the second dot-segment and true on a match, or ("", false)
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
