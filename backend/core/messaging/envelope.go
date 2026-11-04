// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
)

// Event envelopes (core/eventenvelope) put k events of one tenant in one message on
// inbound-events or resolved-events. Every reader of those streams reads both shapes, and
// SplitEnvelope is how: it turns an envelope message into one Message per member, each shaped
// exactly like the legacy single-event message it stands for, so the reader's existing
// per-message path runs on it unchanged.
//
// A member Message is a copy of the envelope's with
//
//   - Value the member's own, self-contained legacy event bytes (the bytes a legacy producer
//     would have published), so anything that reads Value - a decoder, a failed-event record -
//     sees one ordinary event;
//   - Subject, Headers (the correlation id), NumDelivered, StreamSeq and AppendTime the
//     envelope's: all of a member's deliveries are the envelope's deliveries;
//   - an acknowledger that settles the member, and acks the envelope once EVERY member has
//     been acked.
//
// That last rule is the partial-failure contract in one line. A member a reader leaves unacked
// (the per-message "retry" disposition: do not ack, let AckWait pace the redelivery) leaves the
// envelope unacked, so the envelope is redelivered whole; the members already settled are
// settled again by idempotent writes (a content-derived event id, strictly-newer guards), and
// the stubborn member reaches its own give-up on the final delivery while the others are
// already stored. A poison member is acked and costs the others nothing.

const (
	// HeaderRetry rides a member republished alone out of an event envelope (a "split out"
	// member, device-management's inbound retry). Its value is
	//
	//	<origin envelope stream sequence, base 36>:<member index, base 36>:<attempts spent>
	//
	// the member's ORIGIN coordinate and the deliveries it has already used. A reader of
	// inbound-events adds the attempts to the copy's own delivery count, so a member's total
	// budget stays MaxDeliver, and keeps the coordinate (Message.SourceCoordinate) so that what
	// the copy produces - its resolved events, its failed record - is named by where the event
	// came from and not by the copy's own sequence. The same member resolved from the copy and,
	// after a redelivery, from its envelope then publishes under the same ids, and the broker's
	// window folds the second into the first.
	HeaderRetry = "Dc-Retry"

	// The kinds of the per-member dedup ids (Message.MemberDedupID).
	DedupKindFailed = "fe" // event-management's failed-events record of a member. NOT "f": device-management owns that kind on the same stream, and the two number their sources independently
	DedupKindSplit  = "x"  // the copy of a member republished alone
)

// splitOrigin is where a split-out copy came from.
type splitOrigin struct {
	seq uint64
	idx uint32
}

// RetryHeader is the HeaderRetry value for a copy of member index of the envelope at stream
// sequence seq, which has spent attempts deliveries.
func RetryHeader(seq uint64, index uint32, attempts int) string {
	return strconv.FormatUint(seq, 36) + ":" + strconv.FormatUint(uint64(index), 36) + ":" + strconv.Itoa(attempts)
}

// parseRetryHeader reads HeaderRetry. ok is false when it is absent or not in the form
// RetryHeader writes (a malformed one is ignored, never half believed).
func parseRetryHeader(headers map[string]string) (o splitOrigin, attempts int, ok bool) {
	v, present := headers[HeaderRetry]
	if !present {
		return o, 0, false
	}
	parts := strings.Split(v, ":")
	if len(parts) != 3 {
		return o, 0, false
	}
	seq, err1 := strconv.ParseUint(parts[0], 36, 64)
	idx, err2 := strconv.ParseUint(parts[1], 36, 32)
	n, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || seq == 0 || idx >= 1<<20 || n < 0 || n > MaxDeliver {
		return o, 0, false
	}
	return splitOrigin{seq: seq, idx: uint32(idx)}, n, true
}

// ApplyRetryHeader is what a reader of inbound-events does with HeaderRetry on a message it
// has built: the attempts are added to its delivery count and its origin is kept. Scoped to
// inbound-events by the caller: no other stream carries the header.
func ApplyRetryHeader(m *Message) {
	o, attempts, ok := parseRetryHeader(m.Headers)
	if !ok {
		return
	}
	if m.NumDelivered > 0 {
		m.NumDelivered += attempts
	}
	m.split = &o
}

// SourceCoordinate is the (stream sequence, member index) an event is named by: the
// envelope's sequence and the member's place in it for a member, the origin envelope's for a
// copy split out of one, and ok=false for an ordinary message (named by its own sequence).
func (m Message) SourceCoordinate() (seq uint64, index uint32, ok bool) {
	switch {
	case m.member != nil:
		return m.StreamSeq, m.member.index, true
	case m.split != nil:
		return m.split.seq, m.split.idx, true
	}
	return 0, 0, false
}

// memberInfo is what a member Message knows about where it came from.
type memberInfo struct {
	index uint32
}

// MemberIndex reports the member's position in its source and true when the message is a
// member of an envelope; false for a legacy message.
func (m Message) MemberIndex() (uint32, bool) {
	if m.member == nil {
		return 0, false
	}
	return m.member.index, true
}

// MemberDedupID is the broker dedup id of a record about this message when it is a member of
// an envelope: <kind>:<tenant>:<envelope stream sequence, base 36>:<member index, base 36>.
// It is "" for a legacy message (whose records carry the ids they always did) and for a member
// whose envelope has no stream sequence.
//
// The member index is the member's place in its SOURCE, stable however the envelope was
// regrouped, and the sequence is the envelope's, so the id names one event once however many
// times its envelope is delivered. The ids are disjoint from every legacy id: a legacy id's
// kind is followed by a message sequence and a fan-out position, an envelope's is the same
// shape only because the envelope has a sequence of its own, never shared with a message.
func (m Message) MemberDedupID(kind, tenant string) string {
	if m.member == nil || m.StreamSeq == 0 {
		return ""
	}
	return kind + ":" + tenant + ":" + strconv.FormatUint(m.StreamSeq, 36) + ":" + strconv.FormatUint(uint64(m.member.index), 36)
}

// IsEnvelope reports whether the message is marked as an envelope.
func (m Message) IsEnvelope() bool {
	return m.member == nil && m.Headers != nil && m.Headers[eventenvelope.HeaderName] == eventenvelope.HeaderValue
}

// EnvelopeUnit is one member of a split envelope: the Message to handle and the RED clock-stop
// to call, exactly once, when it has been handled.
type EnvelopeUnit struct {
	Msg  Message
	Done func(result string)
}

// SplitEnvelope splits msg when it is an envelope. A legacy message returns marked=false and
// nothing else: the caller handles msg itself.
//
// subjectTenant is the tenant the subject names; an envelope for another tenant is poison.
// A non-nil err (with marked=true) means the envelope as a whole cannot be read - a bad
// checksum, framing, version or tenant. Nothing is acked or done: it is the caller's to
// dispose of as the poison it is, as its reader does for an undecodable message.
//
// An envelope that carries no members (a settlement record: every member of its round went
// elsewhere) is acked here and done(OK) called, and returns marked=true with no units.
//
// 🔴 THE RED COUNTERS COUNT MEMBERS, as device-management's do. done is the clock the caller
// started when it received the envelope and goes to the FIRST member; startClock starts one for
// each of the others (nil: they are not timed). Every member then stops its own clock with its
// own result, so a poison member is counted invalid and a dropped one dropped whatever its
// neighbours did - an envelope's one worst result would let one hide the other, and would count
// k events as one message beside the services that count them k times.
func SplitEnvelope(msg Message, subjectTenant string, done func(string), startClock func() func(string)) (units []EnvelopeUnit, marked bool, err error) {
	if !msg.IsEnvelope() {
		return nil, false, nil
	}
	r, err := eventenvelope.Parse(msg.Value)
	if err != nil {
		return nil, true, err
	}
	if err := r.VerifyTenant(subjectTenant); err != nil {
		return nil, true, err
	}
	n := r.Len()
	if n == 0 {
		_ = msg.Ack()
		done(core.ResultOK)
		return nil, true, nil
	}
	// Rebuild every member before anything is handed out, so a member that cannot be rebuilt
	// fails the envelope whole rather than leaving some of its members in flight.
	values := make([][]byte, n)
	for i := range values {
		if values[i], err = r.Member(i); err != nil {
			return nil, true, err
		}
	}
	g := &memberGroup{parent: msg, acksLeft: int32(n)}
	units = make([]EnvelopeUnit, n)
	for i := range units {
		member := msg
		member.Value = values[i]
		member.slot = nil // the envelope's slot is released when the envelope is acked
		member.member = &memberInfo{index: r.Index(i)}
		member.ack = &memberAck{g: g}
		clock := func(string) {}
		switch {
		case i == 0:
			clock = done
		case startClock != nil:
			clock = startClock()
		}
		units[i] = EnvelopeUnit{Msg: member, Done: clock}
	}
	return units, true, nil
}

// memberGroup settles an envelope through its members.
type memberGroup struct {
	parent   Message
	acksLeft int32 // members not yet acked

	// lastBeat is when an InProgress last reached the broker for this envelope (unix nanos).
	lastBeat atomic.Int64
}

// beatInterval is the least time between two InProgress sent to the broker for one envelope.
// The store hold beats every held message on a timer that is a fraction of AckWait, and an
// envelope's members are all held together, so without this the broker would get k beats
// where it needs one.
const beatInterval = time.Second

// memberAck acks one member, once.
type memberAck struct {
	g     *memberGroup
	acked atomic.Bool
}

func (a *memberAck) Ack() error {
	if !a.acked.CompareAndSwap(false, true) {
		return nil
	}
	if atomic.AddInt32(&a.g.acksLeft, -1) == 0 {
		return a.g.parent.Ack()
	}
	return nil
}

// InProgress restarts the envelope's ack-wait: a member being held keeps the whole envelope
// from being redelivered under it. One envelope is beaten once per beatInterval however many
// of its members are being held.
func (a *memberAck) InProgress() error {
	now := time.Now().UnixNano()
	last := a.g.lastBeat.Load()
	if now-last < int64(beatInterval) || !a.g.lastBeat.CompareAndSwap(last, now) {
		return nil
	}
	return a.g.parent.InProgress()
}
