// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/streams"
)

const detectInstance = "inst-1"

// detectRig starts a plain broker (no JetStream needed — this is core NATS request/reply)
// and returns a connected client.
func detectRig(t *testing.T) *nats.Conn {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		ServerName: "detect-purge-test",
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "the test broker never became ready")
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// respondAs subscribes a fake DETECT partition that answers with the supplied reply, and
// reports how many requests it saw.
func respondAs(t *testing.T, nc *nats.Conn, reply DetectPurgeReply) *atomic.Int64 {
	t.Helper()
	var seen atomic.Int64
	sub, err := nc.Subscribe(DetectPurgeSubject(detectInstance), func(msg *nats.Msg) {
		seen.Add(1)
		body, err := json.Marshal(reply)
		if err != nil {
			return
		}
		_ = nc.Publish(msg.Reply, body)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return &seen
}

// TestDetectPurgeSubjectIsOutsideEveryStreamCaptureSpace pins the reason the request does
// not live in the instance's own subject tree.
//
// Every platform stream captures "{instance}.*.{suffix}" (one level deeper for the
// per-device shape). A control request placed in that tree is captured and DURABLY STORED
// by any stream whose suffix matches — not today necessarily, but the day someone adds a
// stream whose suffix collides, at which point every eviction request is written to disk
// and replayed to that stream's consumers as though it were a platform message. There
// would be no error: the request still reaches the responder, so nothing fails and nobody
// looks.
//
// The check is that the subject's FIRST token is not an instance id, which is what makes
// the collision impossible by construction rather than by inspection of today's suffixes.
func TestDetectPurgeSubjectIsOutsideEveryStreamCaptureSpace(t *testing.T) {
	subject := DetectPurgeSubject(detectInstance)
	require.Truef(t, strings.HasPrefix(subject, "$DC."),
		"the eviction subject %q is not rooted outside the stream subject space", subject)

	// Non-vacuity: the platform really does declare stream suffixes, so "no suffix collides"
	// is a statement about a populated set rather than an empty one.
	suffixes := streams.Suffixes()
	require.NotEmpty(t, suffixes)
	first, _, _ := strings.Cut(subject, ".")
	assert.NotEqual(t, detectInstance, first,
		"the subject begins with the instance id, so it sits inside the tree streams capture")
	for _, s := range suffixes {
		assert.NotContainsf(t, subject, "."+s,
			"the eviction subject contains the declared stream suffix %q", s)
	}
}

// TestDetectPurgeSubjectSeparatesInstances pins ADR-048's isolation on a shared broker: two
// instances must not answer each other's eviction requests, which they would if the subject
// were instance-independent — one instance's purge would evict a tenant of the same name
// from another instance's engine.
func TestDetectPurgeSubjectSeparatesInstances(t *testing.T) {
	assert.NotEqual(t, DetectPurgeSubject("inst-1"), DetectPurgeSubject("inst-2"))
	assert.Contains(t, DetectPurgeSubject("inst-1"), "inst-1")
}

// TestNoResponderIsReportedAsSuchAndReturnsImmediately pins how "nobody is running the
// engine" reaches the caller, against a real broker.
//
// 🔴 THE FIRST DRAFT OF THIS FILE GOT IT WRONG, AND THIS TEST IS WHY IT DID NOT SHIP.
// nats-server answers a request with no subscriber by sending an empty 503-status message
// to the reply inbox; that draft read the status header by hand, on the reasoning that
// nats.go exposes the case only through the Request() family (which returns one reply and
// stops listening, so a multi-partition gather cannot use it). The reasoning was wrong: a
// SYNC subscription converts the 503 into the exported nats.ErrNoResponders, so the
// hand-read never matched anything and the case fell through to the ordinary timeout
// branch.
//
// Nothing about that looks broken from the outside — the call still returns promptly, with
// no error and no replies — which is exactly why it needs a test that asserts the FACT was
// recognised rather than merely that the call came back. Without it the store reports "the
// engine did not answer within 5s" for an instance that simply has no engine, and an
// operator goes looking for a process that was never there.
func TestNoResponderIsReportedAsSuchAndReturnsImmediately(t *testing.T) {
	nc := detectRig(t)

	start := time.Now()
	res, err := PurgeTenantDetect(context.Background(), nc, detectInstance, "acme", nil)
	elapsed := time.Since(start)

	require.NoError(t, err, "no responder is a fact about the instance, not a failure of the call")
	assert.True(t, res.NoResponders,
		"a subject with no subscriber was not recognised as such; the caller cannot tell "+
			"'no engine is running' from 'an engine did not answer in time'")
	assert.Empty(t, res.Replies)
	assert.Lessf(t, elapsed, DetectPurgeWindow/2, "the no-responder case took %s, which means it "+
		"waited out the gather window rather than reading the broker's answer", elapsed)
}

// TestEveryExpectedPartitionMustAnswerBeforeTheGatherEnds is the property that makes the
// store's coverage claim true on a sharded fleet.
//
// A gather that returned on the first reply would report the whole engine erased the moment
// any ONE partition answered — and with GA running a single partition, that bug is
// invisible until the day the fleet is sharded, when a purge starts completing with other
// partitions' checkpoints untouched.
func TestEveryExpectedPartitionMustAnswerBeforeTheGatherEnds(t *testing.T) {
	nc := detectRig(t)
	respondAs(t, nc, DetectPurgeReply{PartitionId: "p1", Evicted: 3})
	respondAs(t, nc, DetectPurgeReply{PartitionId: "p2", Evicted: 4})

	start := time.Now()
	res, err := PurgeTenantDetect(context.Background(), nc, detectInstance, "acme",
		[]string{"p1", "p2"})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Len(t, res.Replies, 2, "the gather stopped before every expected partition answered")
	got := map[string]int64{}
	for _, r := range res.Replies {
		got[r.PartitionId] = r.Evicted
	}
	assert.Equal(t, map[string]int64{"p1": 3, "p2": 4}, got)
	assert.Lessf(t, elapsed, DetectPurgeWindow/2,
		"the gather waited out its whole window (%s) after every expected partition had "+
			"answered, which costs the coordinator that long on every pass", elapsed)
}

// TestASilentPartitionEndsTheGatherAtTheWindow covers the case the store turns into a
// deferral. The caller is told only who answered; deciding that someone is missing is the
// store's job, and it can only do that if this returns the partial set rather than an error.
func TestASilentPartitionEndsTheGatherAtTheWindow(t *testing.T) {
	nc := detectRig(t)
	respondAs(t, nc, DetectPurgeReply{PartitionId: "p1", Evicted: 1})

	// Bounded well inside DetectPurgeWindow: the caller's context deadline is what actually
	// ends this gather, so the test does not have to sit out the full five seconds to prove
	// that a partial answer comes back rather than an error.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res, err := PurgeTenantDetect(ctx, nc, detectInstance, "acme", []string{"p1", "p2"})

	require.NoError(t, err, "a partition that did not answer is data for the ledger, not an error")
	require.Len(t, res.Replies, 1)
	assert.Equal(t, "p1", res.Replies[0].PartitionId)
	assert.False(t, res.NoResponders, "something did answer, so this is not the no-engine case")
}

// TestAnUnexpectedPartitionIsStillCollected covers a running engine that has never
// checkpointed: it holds tenant state but no row names it, so it is outside the expected
// set. Discarding its reply would lose the eviction count for state that really was
// removed — and with it the settle-window restart that keeps a purge from completing over
// a pass that found something.
func TestAnUnexpectedPartitionIsStillCollected(t *testing.T) {
	nc := detectRig(t)
	respondAs(t, nc, DetectPurgeReply{PartitionId: "never-checkpointed", Evicted: 7})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res, err := PurgeTenantDetect(ctx, nc, detectInstance, "acme", nil)

	require.NoError(t, err)
	require.Len(t, res.Replies, 1)
	assert.Equal(t, "never-checkpointed", res.Replies[0].PartitionId)
	assert.Equal(t, int64(7), res.Replies[0].Evicted)
}

// TestPurgeTenantDetectRefusesAWideningTenant pins the check on the sending side.
//
// The tenant does not travel in the subject, so unlike the broker purge it cannot widen a
// filter here — what it widens is the PREFIX MATCH at the far end. An empty token makes the
// engine's match prefix "/", and a token containing "/" matches a deeper segment of another
// tenant's rule id. Either evicts state belonging to tenants that were never deleted, and
// the symptom is a live tenant that silently stops alarming.
func TestPurgeTenantDetectRefusesAWideningTenant(t *testing.T) {
	nc := detectRig(t)
	for _, bad := range []string{"", "   ", "acme/prof", "*", ">", "acme.corp"} {
		_, err := PurgeTenantDetect(context.Background(), nc, detectInstance, bad, nil)
		assert.Errorf(t, err, "the tenant token %q was accepted", bad)
	}
}

// TestPurgeTenantDetectRefusesAnEmptyInstance guards the other half of the address. With no
// instance id the subject reaches no engine, and the resulting silence is indistinguishable
// from an instance holding nothing — a clean report over an untouched engine.
func TestPurgeTenantDetectRefusesAnEmptyInstance(t *testing.T) {
	nc := detectRig(t)
	_, err := PurgeTenantDetect(context.Background(), nc, "  ", "acme", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance id")
}

// TestTheRequestCarriesTheTenantVerbatim pins the payload contract from the responder's
// side of the wire: the field the engine reads is the field the store writes. A rename on
// one side alone is not a build failure — both sides encode JSON — it is an engine that
// evicts the empty tenant, finds nothing, and answers "clean" to every purge forever.
func TestTheRequestCarriesTheTenantVerbatim(t *testing.T) {
	nc := detectRig(t)
	got := make(chan string, 1)
	sub, err := nc.Subscribe(DetectPurgeSubject(detectInstance), func(msg *nats.Msg) {
		var req DetectPurgeRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			got <- "«undecodable»"
			return
		}
		got <- req.Tenant
		body, _ := json.Marshal(DetectPurgeReply{PartitionId: "p1"})
		_ = nc.Publish(msg.Reply, body)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	_, err = PurgeTenantDetect(context.Background(), nc, detectInstance, "acme-corp",
		[]string{"p1"})
	require.NoError(t, err)
	select {
	case tenant := <-got:
		assert.Equal(t, "acme-corp", tenant)
	case <-time.After(2 * time.Second):
		t.Fatal("the responder never saw the request")
	}
}

// TestADisconnectedClientFailsRatherThanWaits pins the same stall guard the broker purge
// carries. The manager reconnects forever, so a publish while the broker is down is
// BUFFERED rather than refused — the gather would then sit out its whole window on the
// coordinator's single goroutine, holding the advisory lock, with no error and no log line.
func TestADisconnectedClientFailsRatherThanWaits(t *testing.T) {
	nc := detectRig(t)
	nc.Close()

	start := time.Now()
	_, err := PurgeTenantDetect(context.Background(), nc, detectInstance, "acme", nil)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second,
		"a disconnected client waited instead of failing")
}

// respondAsAfter is respondAs with a delay before the reply, so a test can order two
// responders on the wire deterministically.
func respondAsAfter(t *testing.T, nc *nats.Conn, delay time.Duration, reply DetectPurgeReply) {
	t.Helper()
	sub, err := nc.Subscribe(DetectPurgeSubject(detectInstance), func(msg *nats.Msg) {
		go func() {
			time.Sleep(delay)
			body, err := json.Marshal(reply)
			if err != nil {
				return
			}
			_ = nc.Publish(msg.Reply, body)
		}()
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })
}

// TestAFailedReplyDoesNotSatisfyItsPartition is the regression for a deferral that repeats
// forever and that no documented operator action clears.
//
// 🔴 TWO RESPONDERS ON ONE PARTITION IS A DESIGNED-FOR STATE, not a contrived one. A DETECT
// writer that loses a split brain latches `stale` and cancels its own loop, but the process
// keeps running with its subscription intact — Stop happens only at service shutdown. Asked
// to evict, it fails on the cancelled context in microseconds, while the writer that really
// owns the partition is still doing an eviction and a database commit.
//
// If an error reply cleared the partition from the awaited set, the gather would return on
// the halted pod's answer EVERY TIME and never read the committed one. The store would then
// write "this tenant's open detection windows and timers are still in its checkpoint" about
// a partition that had just erased them — a false sentence, repeated on every pass, blocking
// the purge until someone happens to notice a pod that looks healthy.
func TestAFailedReplyDoesNotSatisfyItsPartition(t *testing.T) {
	nc := detectRig(t)
	// The halted writer: instant, and it erased nothing.
	respondAs(t, nc, DetectPurgeReply{PartitionId: "p1",
		Error: "this DETECT writer is halted as a stale split-brain loser"})
	// The writer that owns the partition: slower, because it did the work.
	respondAsAfter(t, nc, 150*time.Millisecond, DetectPurgeReply{PartitionId: "p1", Evicted: 9})

	res, err := PurgeTenantDetect(context.Background(), nc, detectInstance, "acme", []string{"p1"})
	require.NoError(t, err)

	var cleanReplies int
	for _, r := range res.Replies {
		if r.Error == "" {
			cleanReplies++
		}
	}
	require.NotZerof(t, cleanReplies, "the gather returned on the failed reply and never read "+
		"the committed one: %+v", res.Replies)
}

// TestAGatherWithNoExpectedPartitionsStillEndsPromptly is the regression for a stall that
// no other test in this file could see.
//
// 🔴 EVERY OTHER TEST HERE SUPPLIES EITHER AN EXPECTED PARTITION OR A SHORT CALLER DEADLINE,
// and both hide this. With neither, the gather has nothing to satisfy and can only end on
// DetectPurgeWindow — so a responder that answers in under a millisecond still costs five
// seconds, every pass, per purging tenant, on the coordinator's single goroutine while it
// holds the instance-wide advisory lock.
//
// It is not an unusual shape. A partition joins the expected set only once it has committed
// a checkpoint, and an engine that has processed nothing never marks itself dirty and so
// never writes a row — which is the steady state of a quiet instance that nonetheless has a
// DETECT engine running and subscribed.
//
// The bound asserted here is deliberately much looser than the quiescence window itself:
// the property is "this does not wait out the full window", not "it waits exactly 300ms",
// so tuning the constant does not require touching the test.
func TestAGatherWithNoExpectedPartitionsStillEndsPromptly(t *testing.T) {
	nc := detectRig(t)
	respondAs(t, nc, DetectPurgeReply{PartitionId: "never-checkpointed", Evicted: 2})

	start := time.Now()
	res, err := PurgeTenantDetect(context.Background(), nc, detectInstance, "acme", nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Len(t, res.Replies, 1, "the responder's reply was not collected")
	assert.Lessf(t, elapsed, DetectPurgeWindow/2, "a gather with no expected partitions took %s "+
		"against a responder that answered immediately — it waited out the whole window, which "+
		"the coordinator pays on every pass while holding the advisory lock", elapsed)
}

// TestAFailedReplyDoesNotShortenTheGather is the other half of the test two above, and the
// half that was missing.
//
// 🔴 THAT COMPANION TEST PASSES AGAINST THIS BUG. It fixes the `awaited` bookkeeping — an
// error reply does not clear a partition — and then hands the same failure straight back
// through the quiescence window, because ANY first reply used to switch the gather from
// DetectPurgeWindow down to detectPurgeQuiesce. Its slow responder answers in 150ms, inside
// the 300ms window, so the truncation never shows.
//
// The replica that answers fast is by construction the one that did NOT do the work: a warm
// standby holding no term (ADR-070) refuses in microseconds, and so does a halted
// split-brain writer. The replica that DID the work sweeps the whole instance's state,
// serializes a snapshot and commits it to Postgres. Giving that replica 300ms measured from
// the moment the standby speaks is not a bound anybody sized — it is whatever was left over.
//
// The consequence is not a slow purge but a WRONG deletion record: the coordinator writes
// "this tenant's open detection windows and timers are still in its checkpoint" about a
// partition that had just erased them, on every pass, and the purge never completes.
//
// So the delay below is deliberately LONGER than detectPurgeQuiesce and shorter than
// DetectPurgeWindow: the only way to collect that reply is to have kept the full window
// open, which is exactly the property under test.
func TestAFailedReplyDoesNotShortenTheGather(t *testing.T) {
	// 🔴 THE PREMISE, ASSERTED RATHER THAN ASSUMED. The slow responder below answers at
	// 2×detectPurgeQuiesce, and that delay only distinguishes the two behaviours while it
	// still fits inside DetectPurgeWindow. Raise the quiescence constant past half the
	// window and this test fails — correctly, but with a message blaming the quiescence
	// window for dropping a reply the gather never had time to hear, which sends the next
	// reader after the wrong thing. Fail here instead, saying what actually moved.
	require.Lessf(t, 2*detectPurgeQuiesce, DetectPurgeWindow, "detectPurgeQuiesce (%s) has "+
		"grown past half of DetectPurgeWindow (%s), so this test's slow reply no longer "+
		"fits inside the gather at all and it can no longer tell the two behaviours apart. "+
		"Re-derive the delay below, do not just widen it", detectPurgeQuiesce, DetectPurgeWindow)

	nc := detectRig(t)
	// The warm standby: instant, and it erased nothing.
	respondAs(t, nc, DetectPurgeReply{PartitionId: "p1",
		Error: "this DETECT replica holds no leadership term for this partition"})
	// The leader: slower than the quiescence window, because a real eviction commits a
	// snapshot to the database.
	respondAsAfter(t, nc, 2*detectPurgeQuiesce, DetectPurgeReply{PartitionId: "p1", Evicted: 9})

	// No expected partitions: the shape a quiet instance is in (a partition joins the
	// expected set only once it has checkpointed), and the shape in which the gather has
	// nothing but the window to end on.
	res, err := PurgeTenantDetect(context.Background(), nc, detectInstance, "acme", nil)
	require.NoError(t, err)

	var cleanReplies int
	for _, r := range res.Replies {
		if r.Error == "" {
			cleanReplies++
		}
	}
	require.NotZerof(t, cleanReplies, "the standby's instant refusal collapsed the gather to "+
		"the %s quiescence window, so the reply from the replica that actually evicted and "+
		"committed was never read: %+v", detectPurgeQuiesce, res.Replies)
}

// The counterweight, and without it the test above is satisfied by deleting the quiescence
// window altogether — which reinstates the five-second stall per pass, per purging tenant,
// on the coordinator's goroutine under the instance-wide advisory lock.
//
// A CLEAN first reply must still shorten the gather. That is the property
// TestAGatherWithNoExpectedPartitionsStillEndsPromptly asserts, restated here against a
// SECOND responder that is subscribed and never answers — so an empty awaited set cannot be
// what ends this gather early, and the quiescence window is the only thing left that can.
func TestACleanReplyStillShortensTheGather(t *testing.T) {
	nc := detectRig(t)
	respondAs(t, nc, DetectPurgeReply{PartitionId: "p1", Evicted: 3})
	silent, err := nc.Subscribe(DetectPurgeSubject(detectInstance), func(*nats.Msg) {})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = silent.Unsubscribe() })

	start := time.Now()
	res, err := PurgeTenantDetect(context.Background(), nc, detectInstance, "acme", nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Len(t, res.Replies, 1)
	assert.Lessf(t, elapsed, DetectPurgeWindow/2, "a gather whose first reply erased and "+
		"committed took %s — the quiescence window is gone, and the coordinator now pays the "+
		"full %s on every pass", elapsed, DetectPurgeWindow)
}

// TestACleanReplyDoesNotShortenTheGatherWhileAPartitionIsStillExpected is the same failure
// as TestAFailedReplyDoesNotShortenTheGather, one level out — and it is the one a FLEET
// reaches rather than a rollout.
//
// 🔴 TWO CLEAN REPLIES ARE NOT INTERCHANGEABLE, AND THE FAST ONE IS AGAIN THE ONE THAT DID
// NOTHING. A partition holding nothing of this tenant sweeps nothing, never sets dirty,
// never commits and answers in under a millisecond. A partition holding its open windows
// does the whole sweep plus a snapshot serialization and a database commit. Both answer
// cleanly, and the empty one is systematically first — so a quiescence clock keyed on "a
// clean reply" hands the busy partition 300ms, deterministically, on every pass for as long
// as that tenant is being purged. Missing it defers the purge with a sentence about the
// partition still holding state it had just erased: the same self-repeating deferral, on a
// ledger nothing re-checks.
//
// The `awaited` set is the partitions the caller KNOWS hold durable state, so while it is
// non-empty there is a specific outstanding answer and no heuristic should displace the full
// window. Both partitions here are expected, which is what makes p1's reply not a licence to
// stop listening for p2's.
//
// 🔴 THIS IS UNREACHABLE ON GA, and that is stated so nobody reads a passing run as evidence
// about production: one instance runs one `singleton` partition, so a second expected
// partition cannot exist yet. It is pinned now because the sharded fleet is what makes it
// live, and by then the symptom looks nothing like the line that causes it.
func TestACleanReplyDoesNotShortenTheGatherWhileAPartitionIsStillExpected(t *testing.T) {
	require.Lessf(t, 2*detectPurgeQuiesce, DetectPurgeWindow, "detectPurgeQuiesce (%s) has "+
		"grown past half of DetectPurgeWindow (%s), so the slow partition below no longer "+
		"fits inside the gather at all", detectPurgeQuiesce, DetectPurgeWindow)

	nc := detectRig(t)
	// p1 holds nothing of this tenant: an instant, clean, zero-eviction answer.
	respondAs(t, nc, DetectPurgeReply{PartitionId: "p1", Evicted: 0})
	// p2 holds the tenant: it sweeps and commits, so it answers later.
	respondAsAfter(t, nc, 2*detectPurgeQuiesce, DetectPurgeReply{PartitionId: "p2", Evicted: 12})

	// BOTH are expected — both have committed a checkpoint, which is what puts a partition
	// in this set.
	res, err := PurgeTenantDetect(context.Background(), nc, detectInstance, "acme",
		[]string{"p1", "p2"})
	require.NoError(t, err)

	var heard []string
	for _, r := range res.Replies {
		if r.Error == "" {
			heard = append(heard, r.PartitionId)
		}
	}
	assert.Containsf(t, heard, "p2", "p1's instant empty answer collapsed the gather to the "+
		"%s quiescence window, so the partition that actually held the tenant — and evicted "+
		"and committed it — was never heard. The caller then records that partition as still "+
		"holding this tenant's detection windows: %+v", detectPurgeQuiesce, res.Replies)
}
