// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strconv"
	"sync"
	"sync/atomic"

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
	// HeaderRetry carries the deliveries an event has already spent when it is republished
	// alone out of an envelope (a "split out" member). A consumed message's NumDelivered is
	// its own delivery count plus this, so a member's total budget stays MaxDeliver rather than
	// starting again at the split.
	HeaderRetry = "Dc-Retry"

	// The kinds of the per-member dedup ids (Message.MemberDedupID).
	DedupKindFailed = "f" // the failed-events record of a member
	DedupKindSplit  = "x" // the copy of a member republished alone
)

// retryCarry is the deliveries a message's HeaderRetry says it has already spent, 0 when it
// carries none or something that is not a small count.
func retryCarry(headers map[string]string) int {
	v, ok := headers[HeaderRetry]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > MaxDeliver {
		return 0
	}
	return n
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
	return m.member == nil && m.Headers != nil && m.Headers[eventenvelope.HeaderName] != ""
}

// EnvelopeUnit is one member of a split envelope: the Message to handle and the RED clock-stop
// to call, exactly once, when it has been handled.
type EnvelopeUnit struct {
	Msg  Message
	Done func(result string)
}

// resultRank orders the dispositions an envelope reports as one: its single clock-stop carries
// the most severe outcome of its members.
func resultRank(r string) int {
	switch r {
	case core.ResultRetry:
		return 6
	case core.ResultFailed:
		return 5
	case core.ResultInvalid:
		return 4
	case core.ResultErased:
		return 3
	case core.ResultDropped:
		return 2
	default:
		return 1
	}
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
// done is the envelope's own clock-stop; the units' Done funcs call it once, after the last of
// them has been called, with the most severe of their results.
func SplitEnvelope(msg Message, subjectTenant string, done func(string)) (units []EnvelopeUnit, marked bool, err error) {
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
	g := &memberGroup{parent: msg, acksLeft: int32(n), done: done, doneLeft: n, worst: core.ResultOK}
	units = make([]EnvelopeUnit, n)
	for i := range units {
		member := msg
		member.Value = values[i]
		member.slot = nil // the envelope's slot is released when the envelope is acked
		member.member = &memberInfo{index: r.Index(i)}
		member.ack = &memberAck{g: g}
		units[i] = EnvelopeUnit{Msg: member, Done: g.memberDone}
	}
	return units, true, nil
}

// memberGroup settles an envelope through its members.
type memberGroup struct {
	parent   Message
	acksLeft int32 // members not yet acked
	done     func(string)

	mu       sync.Mutex
	doneLeft int
	worst    string
}

// memberDone records one member's result and, after the last, reports the most severe.
func (g *memberGroup) memberDone(result string) {
	g.mu.Lock()
	if resultRank(result) > resultRank(g.worst) {
		g.worst = result
	}
	g.doneLeft--
	last, worst := g.doneLeft == 0, g.worst
	g.mu.Unlock()
	if last {
		g.done(worst)
	}
}

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
// from being redelivered under it.
func (a *memberAck) InProgress() error { return a.g.parent.InProgress() }
