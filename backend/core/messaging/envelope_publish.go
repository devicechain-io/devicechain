// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
)

// The producer side of event envelopes (core/eventenvelope): what a producer needs from the
// writers to publish an envelope exactly once.
//
// An envelope's idempotency is not one id but a short protocol (the design's settlement
// rounds). A publish carries the envelope's id; when the broker answers it as a DUPLICATE, the
// PubAck names the sequence of the message that holds the id, and the producer reads that
// message to learn which of its members the original carried. It publishes only the members
// the original did not carry, as the next round under the next round's id. These are the
// primitives: the PubAck as the writer saw it (PublishAcked), and the one-shot synchronous
// publish and read the later rounds use (RoundPublisher).

// PubAckInfo is what the broker's PubAck says about a stored publish.
type PubAckInfo struct {
	// Sequence is the stream sequence of the message the publish is stored as - or, when
	// Duplicate, of the earlier message that holds the same Nats-Msg-Id.
	Sequence uint64
	// Duplicate is true when the broker answered the publish as a repeat of an id it already
	// holds and stored nothing.
	Duplicate bool
}

// AckedWriter is an OrderedWriter that can also report the PubAck, for a producer that acts
// on a duplicate answer. orderedWriter implements it; a producer that needs it asserts the
// writer it was given and refuses to start without it, rather than publish envelopes it
// cannot settle.
type AckedWriter interface {
	OrderedWriter
	// PublishAcked is Publish with the PubAck: done receives it, and err nil only after the
	// broker's PubAck. Everything else is Publish's contract - one submitting goroutine,
	// done on the writer's goroutine, in order, quick, never calling Publish or Fail.
	PublishAcked(ctx context.Context, msg Message, done func(ack PubAckInfo, err error))
}

// PublishAcked implements AckedWriter.
func (o *orderedWriter) PublishAcked(ctx context.Context, msg Message, done func(PubAckInfo, error)) {
	if done == nil {
		panic("messaging: OrderedWriter.PublishAcked needs a done callback: it is the only place the outcome goes")
	}
	info := new(PubAckInfo)
	o.publishGuardedAck(ctx, msg, func(err error) { done(*info, err) }, nil, info)
}

// HeaderMemberIndex rides an envelope's member published ALONE, in the legacy shape (an
// envelope of one is never encoded). Its value is the member's source index in base 36, so a
// reader of the stored message - the duplicate probe - can tell WHICH member that message
// settled even though the shape carries no index.
const HeaderMemberIndex = "Dc-Mi"

// EnvelopeDedupKind is the kind letter of the Nats-Msg-Id of an envelope published for a
// group of legacy inbound messages (device-management's interim grouping, before inbound
// envelopes exist). It is upper case, so it is disjoint from every per-event id's lower-case
// kind, and it is NOT "R": that letter is reserved for an envelope mapped 1:1 from an inbound
// envelope, whose second field is that envelope's stream sequence. The second field of this
// kind is a BUCKET (the legacy inbound sequence space cut into fixed ranges), a different
// number space, and sharing a letter would let a bucket's id be answered as the duplicate of
// an unrelated envelope.
const EnvelopeDedupKind = "G"

// EnvelopeDedupID is <kind>:<tenant>:<source b36>:<round b36>: the id that names a round of
// the envelope derived from source. Compact for the reason sourceDedupID is: it rides every
// envelope and every broker server holds it for the window.
func EnvelopeDedupID(kind, tenant string, source uint64, round uint32) string {
	return kind + ":" + tenant + ":" + strconv.FormatUint(source, 36) + ":" + strconv.FormatUint(uint64(round), 36)
}

// StoredMessage is a message read back from a stream by sequence.
type StoredMessage struct {
	Sequence uint64
	Subject  string
	Value    []byte
	Headers  map[string]string
}

// RoundPublisher publishes one message at a time and waits for its PubAck, and reads a stored
// message back by sequence. It is the later rounds' publisher (see the section above): rare,
// so synchronous, and used off the ordered writer's goroutine, which only one goroutine may
// submit to.
type RoundPublisher interface {
	// Publish sends msg to ctx's tenant subject and returns the PubAck. A publish whose id is
	// "in process" (another publish of the id is being replicated) is sent again until the id
	// resolves or the publish ceiling passes, as the writers do.
	Publish(ctx context.Context, msg Message) (PubAckInfo, error)
	// Get returns the stored message at seq from the tenant's stream. It is answered by the
	// stream leader, not by a follower that may not have applied a sequence a PubAck named.
	// A message on another tenant's subject is an error.
	Get(ctx context.Context, tenant string, seq uint64) (StoredMessage, error)
}

type roundPublisher struct {
	nmgr   *NatsManager
	suffix string
}

// NewRoundPublisher builds the RoundPublisher for suffix; the stream is created if needed.
func (nmgr *NatsManager) NewRoundPublisher(suffix string) (RoundPublisher, error) {
	if err := nmgr.requireWriter(suffix); err != nil {
		return nil, err
	}
	if err := refuseTenantWide(suffix); err != nil {
		return nil, err
	}
	if _, err := nmgr.ensureStream(suffix); err != nil {
		return nil, err
	}
	return &roundPublisher{nmgr: nmgr, suffix: suffix}, nil
}

func (r *roundPublisher) Publish(ctx context.Context, msg Message) (PubAckInfo, error) {
	subject, err := r.nmgr.tenantSubject(ctx, r.suffix, "")
	if err != nil {
		return PubAckInfo{}, err
	}
	nm := natsMsg(subject, msg)
	pctx, callerBound, cancel := publishContext(ctx)
	defer cancel()
	deadline, _ := pctx.Deadline()
	var delay time.Duration
	for {
		ack, err := r.nmgr.js.PublishMsg(nm, nats.Context(pctx))
		if err == nil {
			return PubAckInfo{Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
		}
		if !duplicateInProcess(err) {
			if !callerBound && errors.Is(err, context.DeadlineExceeded) {
				err = nats.ErrTimeout
			}
			return PubAckInfo{}, err
		}
		var again bool
		if delay, again = waitToResendInProcess(delay, deadline); !again {
			return PubAckInfo{}, err
		}
	}
}

func (r *roundPublisher) Get(ctx context.Context, tenant string, seq uint64) (StoredMessage, error) {
	pctx, _, cancel := publishContext(ctx)
	defer cancel()
	stream := StreamName(r.nmgr.Microservice.InstanceId, r.suffix)
	raw, err := r.nmgr.js.GetMsg(stream, seq, nats.Context(pctx))
	if err != nil {
		return StoredMessage{}, fmt.Errorf("reading %s seq %d: %w", stream, seq, err)
	}
	if raw.Sequence != seq {
		return StoredMessage{}, fmt.Errorf("reading %s: asked for seq %d, broker returned %d", stream, seq, raw.Sequence)
	}
	if t, ok := ParseTenantFromSubject(raw.Subject); !ok || t != tenant {
		return StoredMessage{}, fmt.Errorf("reading %s seq %d: the message is on subject %q, not tenant %q's", stream, seq, raw.Subject, tenant)
	}
	out := StoredMessage{Sequence: raw.Sequence, Subject: raw.Subject, Value: raw.Data}
	if len(raw.Header) > 0 {
		out.Headers = make(map[string]string, len(raw.Header))
		for k := range raw.Header {
			out.Headers[k] = raw.Header.Get(k)
		}
	}
	return out, nil
}
