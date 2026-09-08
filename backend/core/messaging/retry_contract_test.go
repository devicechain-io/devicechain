// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The retry contract is ONE contract for every durable reader, and these tests exist
// to make that a property of the tree rather than a fact about today's code.
//
// 🔴 THE DEFECT THEY GUARD AGAINST IS A FUTURE ONE, AND IT IS SILENT. Consumers spread
// across the service estate decide "this is the final attempt, write the dead letter and
// ack" with `msg.NumDelivered >= messaging.MaxDeliver`, reading the package constant
// (`grep -rn 'messaging.MaxDeliver' --include='*.go' backend` enumerates them; no tally
// is written down here, because the one this file used to carry was already wrong). Their
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
//
// 🔴 AND THAT IS WHY IT GOES THROUGH NewReader AND A REAL BROKER RATHER THAN THROUGH A
// HAND-BUILT natsReader. The first version of this file built `&natsReader{suffix: …}`
// and called consumerConfig() directly, and that version could be defeated by the most
// natural way to plumb a tier in: `r.tier = streams.TierFor(suffix)` inside NewReader
// (or a ReaderWithTier option), exactly mirroring how deliverNew/ReaderWithDeliverNew
// already work in the same struct. A hand-built reader never runs that assignment, so
// every fixture would sit at the zero-value tier — and streams.Hot is iota, i.e. 0 — so
// the test would see uniform Hot configs and pass green while production diverged. Only
// a tier derived INSIDE consumerConfig from r.suffix would have failed it. Building
// through the constructor covers both plumbings, and reading the config back from the
// BROKER rather than from consumerConfig() covers a third: a field set on the way to
// AddConsumer without going through consumerConfig at all. The broker's answer is also
// the one that decides redelivery, which is the thing the dead-letter arms are pairing
// with.

// retryContractRig starts an embedded JetStream server and returns the manager plus the
// instance/area the durable names are built from, so a test can create readers through
// the real NewReader path and then ask the server what it actually holds.
func retryContractRig(t *testing.T, areaPrefix string) (*NatsManager, string, string) {
	t.Helper()
	srv := startEmbeddedServer(t)
	area := uniqueArea(areaPrefix)
	ms := testMicroservice(t, srv, area)
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx), "initialize")
	require.NoError(t, nmgr.Start(ctx), "start")
	t.Cleanup(func() { _ = nmgr.Stop(ctx) })
	return nmgr, ms.InstanceId, area
}

// assertPlatformRetryContract reads the durable BACK OFF THE BROKER and asserts the one
// contract. `why` names what was being varied, so a failure says which plumbing broke it.
func assertPlatformRetryContract(t *testing.T, nmgr *NatsManager, instance, area, suffix, why string) {
	t.Helper()
	info, err := nmgr.js.ConsumerInfo(StreamName(instance, suffix), DurableName(instance, area, suffix))
	require.NoErrorf(t, err, "consumer info for %s (%s)", suffix, why)

	assert.Equalf(t, MaxDeliver, info.Config.MaxDeliver,
		"stream %q (%s, tier %v) must get the platform MaxDeliver: dead-letter arms across "+
			"the service estate compare NumDelivered against messaging.MaxDeliver, so a "+
			"consumer configured with a different limit writes its dead letter at the wrong "+
			"attempt — or never", suffix, why, streams.TierFor(suffix))
	assert.Equalf(t, AckWait, info.Config.AckWait,
		"stream %q (%s) must get the platform AckWait: callers size stranded-work grace "+
			"periods against the exported constant, and a per-stream value would make those "+
			"readings quietly wrong", suffix, why)
	assert.Equalf(t, readerMaxAckPending, info.Config.MaxAckPending,
		"stream %q (%s) must get the platform MaxAckPending", suffix, why)
}

// Every declared stream — across both disk tiers — must yield the same retry contract
// once it has been through NewReader and landed on the broker. streams.All is iterated
// rather than a couple of hand-picked suffixes so that a stream added later is covered
// without editing this file, and so both tiers are genuinely represented rather than
// assumed to be.
func TestEveryStreamGetsTheSameRetryContract(t *testing.T) {
	nmgr, instance, area := retryContractRig(t, "retry-contract")

	var sawHot, sawCold bool
	for _, s := range streams.All {
		switch streams.TierFor(s.Suffix) {
		case streams.Hot:
			sawHot = true
		case streams.Cold:
			sawCold = true
		}
		_, err := nmgr.NewReader(s.Suffix)
		require.NoErrorf(t, err, "NewReader(%s)", s.Suffix)
		assertPlatformRetryContract(t, nmgr, instance, area, s.Suffix, "default construction")
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

// ReaderOptions are the OTHER place a per-reader input could reach consumerConfig, and
// they run after NewReader has filled the struct — so an option is where a ReaderWithTier
// would live. This pins that the options in use today move the delivery START POSITION
// and the termination gate, and move the retry contract not at all: an opted-in reader
// must come off the broker with the same three numbers as a default one.
func TestReaderOptionsDoNotMoveTheRetryContract(t *testing.T) {
	nmgr, instance, area := retryContractRig(t, "retry-contract-opts")

	// A Hot and a Cold stream, so an option applied per tier would show up here too.
	for _, suffix := range []string{streams.InboundEvents, streams.DeviceRoster} {
		_, err := nmgr.NewReader(suffix, ReaderWithDeliverNew(), ReaderWithTermGate(func() bool { return false }))
		require.NoErrorf(t, err, "NewReader(%s) with options", suffix)

		info, err := nmgr.js.ConsumerInfo(StreamName(instance, suffix), DurableName(instance, area, suffix))
		require.NoError(t, err)
		require.Equalf(t, nats.DeliverNewPolicy, info.Config.DeliverPolicy,
			"fixture drift: ReaderWithDeliverNew no longer reaches the durable for %s, so this "+
				"test is no longer exercising an option at all", suffix)

		assertPlatformRetryContract(t, nmgr, instance, area, suffix, "constructed with options")
	}
}

// streams.Tier classifies DISK BUDGET and nothing else. It is the field someone reaches
// for when adding per-tier consumer tuning, so its meaning is pinned here, next to the
// reason that would be a mistake: the two tiers differ in stream MaxBytes and must not
// start differing in retry behaviour without the dead-letter arms learning the new
// value. Read the MaxDeliver comment in nats.go before changing this.
func TestTierIsADiskBudgetClassNotARetryClass(t *testing.T) {
	nmgr, instance, area := retryContractRig(t, "retry-contract-tier")

	const hot, cold = streams.InboundEvents, streams.DeviceRoster
	require.Equal(t, streams.Hot, streams.TierFor(hot), "fixture drift: this suffix is no longer Hot")
	require.Equal(t, streams.Cold, streams.TierFor(cold), "fixture drift: this suffix is no longer Cold")

	for _, suffix := range []string{hot, cold} {
		_, err := nmgr.NewReader(suffix)
		require.NoErrorf(t, err, "NewReader(%s)", suffix)
	}
	h, err := nmgr.js.ConsumerInfo(StreamName(instance, hot), DurableName(instance, area, hot))
	require.NoError(t, err)
	c, err := nmgr.js.ConsumerInfo(StreamName(instance, cold), DurableName(instance, area, cold))
	require.NoError(t, err)

	assert.Equal(t, h.Config.MaxDeliver, c.Config.MaxDeliver, "a Hot and a Cold stream must retry alike")
	assert.Equal(t, h.Config.AckWait, c.Config.AckWait, "a Hot and a Cold stream must wait alike")
	assert.Equal(t, h.Config.MaxAckPending, c.Config.MaxAckPending, "a Hot and a Cold stream must pace alike")

	// The counterweight: the two durables must genuinely be different consumers, or the
	// three assertions above are comparing one config with itself.
	require.NotEqual(t, h.Name, c.Name, "the Hot and Cold fixtures resolved to one durable")
}
