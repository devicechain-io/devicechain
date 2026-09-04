// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The retry contract is ONE contract for every durable reader, and these tests exist
// to make that a property of the tree rather than a fact about today's code.
//
// 🔴 THE DEFECT THEY GUARD AGAINST IS A FUTURE ONE, AND IT IS SILENT. Consumers across
// eight services decide "this is the final attempt, write the dead letter and ack" with
// `msg.NumDelivered >= messaging.MaxDeliver`, reading the package constant. Their
// consumer's ACTUAL limit comes from consumerConfig. Those are the same number today
// only because there is one constant. Introduce per-tier (or per-consumer) tuning
// without carrying the value to the arms and the two diverge:
//
//   - arm's constant too LOW  -> a dead letter is written and the message acked while
//     the broker would still have retried it, so a transient outage becomes data loss;
//   - arm's constant too HIGH -> the arm never fires, the broker stops redelivering at
//     its own limit, and the message disappears with no dead-letter record at all.
//
// Nothing else in the estate can see either: every test of those arms sets
// NumDelivered by hand, so both halves agree with the test and with nothing else.
//
// 🔑 WHY THIS IS NOT A TAUTOLOGY TODAY. consumerConfig currently takes no tier and
// cannot vary, so of course every reader matches. That is the point: the test states
// the INVARIANT, so the day someone plumbs a tier (or any other per-reader input) into
// consumerConfig, this is what goes red — while nothing about the dead-letter arms
// themselves changes and no other test notices.

// Every declared stream — across both disk tiers — must yield the same retry contract.
// streams.All is iterated rather than a couple of hand-picked suffixes so that a stream
// added later is covered without editing this file, and so both tiers are genuinely
// represented rather than assumed to be.
func TestEveryStreamGetsTheSameRetryContract(t *testing.T) {
	var sawHot, sawCold bool
	for _, s := range streams.All {
		switch streams.TierFor(s.Suffix) {
		case streams.Hot:
			sawHot = true
		case streams.Cold:
			sawCold = true
		}

		r := &natsReader{suffix: s.Suffix, stream: s.Suffix, subject: s.Suffix, durable: s.Suffix}
		cfg := r.consumerConfig()

		assert.Equalf(t, MaxDeliver, cfg.MaxDeliver,
			"stream %q (tier %v) must get the platform MaxDeliver: the dead-letter arms in "+
				"eight services compare NumDelivered against messaging.MaxDeliver, so a "+
				"consumer configured with a different limit writes its dead letter at the "+
				"wrong attempt — or never", s.Suffix, streams.TierFor(s.Suffix))
		assert.Equalf(t, AckWait, cfg.AckWait,
			"stream %q must get the platform AckWait: callers size stranded-work grace "+
				"periods against the exported constant, and a per-stream value would make "+
				"those readings quietly wrong", s.Suffix)
		assert.Equalf(t, readerMaxAckPending, cfg.MaxAckPending,
			"stream %q must get the platform MaxAckPending", s.Suffix)
	}

	// The counterweight. Without it the loop above passes vacuously over an empty or
	// single-tier stream set, which is the shape it would take if streams.All were
	// filtered or if the tier vocabulary collapsed — and a uniformity assertion over
	// one tier proves nothing about uniformity ACROSS tiers.
	require.True(t, sawHot, "streams.All must contain at least one Hot stream, or this "+
		"test is asserting uniformity across a set that has no variation in it")
	require.True(t, sawCold, "streams.All must contain at least one Cold stream, or this "+
		"test is asserting uniformity across a set that has no variation in it")
}

// streams.Tier classifies DISK BUDGET and nothing else. It is the field someone reaches
// for when adding per-tier consumer tuning, so its meaning is pinned here, next to the
// reason that would be a mistake: the two tiers differ in stream MaxBytes and must not
// start differing in retry behaviour without the dead-letter arms learning the new
// value. Read the MaxDeliver comment in nats.go before changing this.
func TestTierIsADiskBudgetClassNotARetryClass(t *testing.T) {
	hot := &natsReader{suffix: streams.InboundEvents, durable: "d1", subject: "s1"}
	cold := &natsReader{suffix: streams.DeviceRoster, durable: "d2", subject: "s2"}
	require.Equal(t, streams.Hot, streams.TierFor(hot.suffix), "fixture drift: this suffix is no longer Hot")
	require.Equal(t, streams.Cold, streams.TierFor(cold.suffix), "fixture drift: this suffix is no longer Cold")

	h, c := hot.consumerConfig(), cold.consumerConfig()
	assert.Equal(t, h.MaxDeliver, c.MaxDeliver, "a Hot and a Cold stream must retry alike")
	assert.Equal(t, h.AckWait, c.AckWait, "a Hot and a Cold stream must wait alike")
	assert.Equal(t, h.MaxAckPending, c.MaxAckPending, "a Hot and a Cold stream must pace alike")
}
