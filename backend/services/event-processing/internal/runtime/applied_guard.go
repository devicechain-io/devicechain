// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"encoding/binary"
	"time"

	"github.com/devicechain-io/dc-microservice/eventidentity/eventid"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
)

const (
	// DefaultAppliedRetention is how far back, in the broker's APPEND time of the message that
	// carried them, the guard remembers applied events. It is the platform's own redelivery span:
	// streams.RedeliveryDuplicateWindowSeconds is the longest after a message's first delivery
	// that one of its re-publishes can land (MaxDeliver deliveries two AckWaits apart, plus one
	// AckWait to spare). A duplicate produced by redelivery cannot be further behind its original
	// than that, so inside this the guard is exact; the producer's own retry give-up (5 minutes)
	// is inside it with room to spare.
	DefaultAppliedRetention = time.Duration(streams.RedeliveryDuplicateWindowSeconds) * time.Second
	// DefaultAppliedMaxEntries bounds the remembered events whatever the rate. 16,000,000 holds the
	// default retention (plus the up to 30 s the generations round it up by) at ~25,000 events/s.
	DefaultAppliedMaxEntries = 16_000_000

	// genSeconds is the width of one generation. Eviction is a whole generation at a time, so the
	// real retention is the retention rounded UP to a multiple of it (and never less).
	genSeconds = 30
)

// Digest is an event's identity, truncated to 16 bytes of the SHA-256 event id.
type Digest [16]byte

// Fingerprint is the 64 bits of the identity the guard actually keeps.
func (d Digest) Fingerprint() uint64 {
	f := binary.BigEndian.Uint64(d[:8])
	if f == 0 {
		f = 1 // 0 marks an empty slot
	}
	return f
}

// AppliedGuard is DETECT's duplicate-event guard. It is ENABLED ONLY WHEN event envelopes are
// switched on (infrastructure.messaging.envelopes.resolved): with the switch off nothing builds,
// primes or consults one, and behaviour and cost are exactly as before it existed.
//
// 🔴 WHY IT EXISTS. The engine's idempotency guard is the STREAM SEQUENCE (seq <= LastSeq), which
// drops a redelivery or a replay of a message it has applied. It cannot see an EVENT delivered
// twice under two sequences: a residual of the resolved-envelope settlement protocol (a lost
// PubAck plus a group boundary inside the set, a retry that gave up and was redelivered, a
// recreated inbound durable resetting the delivery count) or a republish across a rolling upgrade
// stores the same event in resolved-events a second time. Event-management and device-state
// dedupe by event id; without this DETECT would apply the second copy, double-counting every
// counting, windowed and aggregating rule.
//
// The identity is event-management's event id (core/eventidentity/eventid, ONE function shared by
// both): tenant, device, event type, occurred time (ns), alternate id and the payload's identity
// view (calculated values excluded). So DETECT and the store agree on which events are one, and
// two identical readings from one device at the same nanosecond are counted ONCE by both. Tenant
// and device are part of it, so the remembered set is per series by construction.
//
// WHY NOT A SOURCE COORDINATE. A resolved member does not carry the inbound sequence and member
// index it came from (only its position in the resolved envelope, which differs between two
// copies), so there is no smaller key than the event's own identity, and a smaller one would have
// to be added to the wire format. The identity is kept as 64 bits, not 16 bytes: a false match
// needs a collision among the ~millions remembered, about 1e-11 per event at the default
// retention, and drops one event.
//
// 🔴 IT IS PART OF THE REPLAY-CORRECT PATH. Admit is called by the single loop for every event,
// legacy message or envelope member, in stream order, and its answer depends ONLY on the events
// before it in the stream and their broker append times (message metadata, the same on every
// read), so live and replay decide identically. A duplicate REFRESHES its entry, so "remembered"
// means "seen within the retention", which depends only on that suffix of the stream. It is
// deliberately NOT in the engine snapshot (that would add 8 bytes per remembered event to every
// checkpoint); a term build PRIMES it by reading the retention's worth of the stream before
// LastSeq+1 (ResolvedEventsProcessor.primeApplied), which rebuilds the identical set.
//
// BOUND. Events are kept in generations of 30 s of append time; generations older than the
// retention are dropped whole, and if the total passes MaxEntries the oldest generation goes too
// (identically live and on replay while the cap binds the same way; under cap pressure priming
// starts at the cap boundary, so exactness there is approximate and the cap is sized so it does not
// bind at the rates the platform targets). Each generation is a flat open-addressing table of
// 8-byte fingerprints at <= 70% load: ~12-24 bytes per remembered event. At the default 10
// minutes, at 20,000 events/s that is about 12 million events and ~180 MB; at 5,000/s ~45 MB. The
// 16,000,000 cap is reached near 25,000 events/s, past which coverage falls below the redelivery
// span (at 100,000/s it is ~2.7 minutes). The memory is held by the DETECT LEADER only; a standby
// builds it when it takes over.
//
// A generation's size is bounded too: a message whose append time is BEHIND the newest generation
// is recorded in that generation, so a clock that jumped far ahead and came back would grow one
// table without limit. When it reaches its share of the cap the next entry opens a new generation
// (the next number), which expires like any other.
//
// RESIDUAL, plainly: a duplicate delivered further behind its original than the retention is
// applied twice, as it was before the guard existed: a recreated or renamed inbound durable (it
// redelivers from its start, hours back), an outage that outlasts the redelivery span, or a rate
// past the cap.
//
// Not safe for concurrent use: the loop owns it.
type AppliedGuard struct {
	retentionGens int64
	maxEntries    int
	headLimit     int
	total         int
	gens          []*generation
}

type generation struct {
	gen   int64
	count int
	slots []uint64
}

// NewAppliedGuard builds a guard with the given retention (0 means DefaultAppliedRetention) and
// entry cap (0 means DefaultAppliedMaxEntries).
func NewAppliedGuard(retention time.Duration, maxEntries int) *AppliedGuard {
	if retention <= 0 {
		retention = DefaultAppliedRetention
	}
	if maxEntries <= 0 {
		maxEntries = DefaultAppliedMaxEntries
	}
	// n generations back plus the current one cover n*genSeconds seconds at minimum.
	n := (int64(retention/time.Second) + genSeconds - 1) / genSeconds
	// A generation may hold twice its even share of the cap: ample for a steady rate, and the
	// bound that stops a skewed clock growing one without limit.
	limit := max(1024, 2*maxEntries/int(n+1))
	return &AppliedGuard{retentionGens: n, maxEntries: maxEntries, headLimit: limit}
}

// Len is the number of remembered events.
func (g *AppliedGuard) Len() int { return g.total }

// Generations is how many generations are held (tests, and the state gauge).
func (g *AppliedGuard) Generations() int { return len(g.gens) }

// MaxEntries is the entry cap.
func (g *AppliedGuard) MaxEntries() int { return g.maxEntries }

// Retention is the retention the guard keeps, rounded up to its generations.
func (g *AppliedGuard) Retention() time.Duration {
	return time.Duration(g.retentionGens*genSeconds) * time.Second
}

// Admit records id as applied by a message appended at t and reports true, or reports false (and
// records nothing new) when it is already remembered: the event is a duplicate to skip. Times
// should be non-decreasing (a stream's append times are); one that is not is recorded in the
// newest generation.
func (g *AppliedGuard) Admit(t time.Time, id Digest) bool {
	gn := t.Unix() / genSeconds
	if n := len(g.gens); n == 0 || gn > g.gens[n-1].gen {
		g.gens = append(g.gens, &generation{gen: gn})
	} else if g.gens[n-1].count >= g.headLimit {
		g.gens = append(g.gens, &generation{gen: g.gens[n-1].gen + 1})
	}
	head := g.gens[len(g.gens)-1]
	// Expire: everything not within retentionGens of the newest generation.
	for len(g.gens) > 1 && (head.gen-g.gens[0].gen > g.retentionGens || g.total > g.maxEntries) {
		g.total -= g.gens[0].count
		g.gens = g.gens[1:]
	}
	f := id.Fingerprint()
	// 🔴 A DUPLICATE REFRESHES THE ENTRY (it is re-recorded in the newest generation). Remembered
	// then means "SEEN, applied or refused, within the retention", which depends only on that
	// suffix of the stream, so priming from the retention's worth of history rebuilds exactly
	// the set the live loop held. Were a refusal not to refresh, the set would depend on which
	// occurrence came first, which a primed suffix cannot know.
	seen := false
	for _, gen := range g.gens {
		if gen.has(f) {
			seen = true
			break
		}
	}
	if seen {
		if !head.has(f) {
			head.add(f)
			g.total++
		}
		return false
	}
	head.add(f)
	g.total++
	return true
}

func (s *generation) has(f uint64) bool {
	if len(s.slots) == 0 {
		return false
	}
	mask := uint64(len(s.slots) - 1)
	for i := mix(f) & mask; ; i = (i + 1) & mask {
		switch s.slots[i] {
		case 0:
			return false
		case f:
			return true
		}
	}
}

func (s *generation) add(f uint64) {
	if len(s.slots) == 0 || (s.count+1)*10 > len(s.slots)*7 {
		s.grow()
	}
	mask := uint64(len(s.slots) - 1)
	i := mix(f) & mask
	for s.slots[i] != 0 {
		i = (i + 1) & mask
	}
	s.slots[i] = f
	s.count++
}

func (s *generation) grow() {
	old := s.slots
	n := 1024
	if len(old) > 0 {
		n = len(old) * 2
	}
	s.slots = make([]uint64, n)
	mask := uint64(n - 1)
	for _, f := range old {
		if f == 0 {
			continue
		}
		i := mix(f) & mask
		for s.slots[i] != 0 {
			i = (i + 1) & mask
		}
		s.slots[i] = f
	}
}

// mix spreads the fingerprint's bits (it is already a hash, but its low bits index the table).
func mix(x uint64) uint64 {
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	return x
}

// EventIdentity is the identity of a resolved event of tenant: event-management's event id,
// truncated.
func EventIdentity(tenant string, ev *resolvedevent.ResolvedEvent) (Digest, error) {
	id, err := eventid.OfResolved(tenant, ev)
	if err != nil {
		return Digest{}, err
	}
	var d Digest
	copy(d[:], id)
	return d, nil
}
