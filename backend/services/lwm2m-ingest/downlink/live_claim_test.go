// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// --- claimLive(): the live-path confirmation --------------------------------
//
// 🔴 A LIVE ENVELOPE CAN ARRIVE LATE, and these tests are about what happens when it does.
// It can be redelivered after waiting out its ack deadline, redelivered to a new leader, or
// pulled for the first time long after it was published. By then command-delivery may have
// re-armed the row and a wake drain may have carried it out. Before the confirmation the live
// path actuated whatever arrived, so that was a second physical actuation. The properties are
// asserted on VALUES — which nonce was quoted, which nonce the response carries, how many ops
// ran — because a count of "a claim happened" cannot tell a claim of the right dispatch from a
// claim of the wrong one.

// liveMetrics builds REAL, unregistered counters for the live-claim outcomes, so assertions
// measure the production increments.
func liveMetrics() Metrics {
	c := func(name string) prometheus.Counter {
		return prometheus.NewCounter(prometheus.CounterOpts{Name: name})
	}
	return Metrics{
		StaleDispatch:   c("stale_dispatch"),
		LiveClaimErrors: c("live_claim_errors"),
		ParkErrors:      c("park_errors"),
		ParkSkipped:     c("park_skipped"),
		ServedOffline:   c("served_offline"),
	}
}

func liveLook() *fakeLookup {
	return &fakeLookup{conn: &fakeConn{}, reaches: map[string]Reach{"acme/pump-1": ReachLive}}
}

// TestLiveDispatchClaimsBeforeTheOpQuotingTheEnvelopeNonce: the confirmation is issued BEFORE
// the op, and it quotes the nonce the envelope arrived with. A confirmation after the op would
// confirm a dispatch that had already moved the hardware; one quoting anything but the
// envelope's nonce would confirm a dispatch this delivery does not belong to.
func TestLiveDispatchClaimsBeforeTheOpQuotingTheEnvelopeNonce(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	opsAtClaim := -1
	claimer := &fakeClaimer{won: true}
	claimer.onLive = func() { opsAtClaim = exec.callCount() }
	d := NewDispatcher(nil, &fakePublisher{}, liveLook(), exec, nil, claimer, liveMetrics(), Options{})

	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, "n-env", newAck()))

	assert.Equal(t, []liveClaim{{commandToken: "c1", nonce: "n-env"}}, claimer.liveClaims(),
		"the live command is confirmed once, quoting the envelope's nonce")
	assert.Equal(t, 0, opsAtClaim, "the confirmation must be issued BEFORE the op runs")
	assert.Equal(t, 1, exec.callCount(), "a confirmed live command is actuated")
}

// TestLiveResponseQuotesTheClaimedNonce: the confirmation moved the row onto a NEW dispatch, and
// command-delivery matches an answer against the row's current nonce. An outcome quoting the
// envelope's nonce would be refused and the command would ride to TIMEOUT after the device
// carried it out.
func TestLiveResponseQuotesTheClaimedNonce(t *testing.T) {
	ack := newAck()
	pub := &fakePublisher{}
	d := NewDispatcher(nil, pub, liveLook(), &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}},
		nil, &fakeClaimer{won: true}, liveMetrics(), Options{})

	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, "n-env", ack))

	resp := pub.responses()
	require.Len(t, resp, 1, "a response is published")
	assert.Equal(t, "confirmed-nonce-1", resp[0].DispatchNonce,
		"the outcome must quote the nonce the confirmation returned, not the envelope's")
	assert.True(t, ack.acked(), "a confirmed, actuated command is acked (seal-fate)")
}

// recordingFetcher records which device each wake-drain fetch was for.
type recordingFetcher struct {
	mu      sync.Mutex
	devices []string
}

func (f *recordingFetcher) Pending(_ context.Context, tenant, deviceToken string) ([]DrainCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devices = append(f.devices, tenant+"/"+deviceToken)
	return nil, nil
}

func (f *recordingFetcher) fetched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.devices...)
}

// runDispatcher drives the real Run loop until stop is called. Every wait is bounded, so a
// hang reports as a failure rather than blocking the suite.
func runDispatcher(t *testing.T, d *Dispatcher) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	select {
	case <-d.Ready():
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("dispatcher did not become ready")
	}
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancellation")
		}
	}
}

// TestStaleLiveDispatchIsAckedNotActuatedAndNudgesADrain is the headline: a delivery whose
// dispatch the platform already re-armed is discarded, not actuated, and — because the usual
// cause is that the row was parked, and a device that stays connected sends no wake of its
// own — a wake drain is nudged for that device so the parked row is delivered now rather than
// at its next re-handshake. It drives the real Run loop, because the nudge lands on the
// worker's own shard and only a running dispatcher has shards.
func TestStaleLiveDispatchIsAckedNotActuatedAndNudgesADrain(t *testing.T) {
	ack := newAck()
	rdr := &scriptReader{msgs: []messaging.Message{
		cmdMsgNonce("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, "n-env", ack),
	}}
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	ff := &recordingFetcher{}
	m := liveMetrics()
	d := NewDispatcher(rdr, pub, liveLook(), exec, ff, &fakeClaimer{won: true, liveLost: true}, m,
		Options{ReadPacer: core.NewReadPacer(nil, "device commands").UseClock(core.VirtualClock())})

	stop := runDispatcher(t, d)
	require.Eventually(t, func() bool { return ack.acked() && len(ff.fetched()) > 0 }, 2*time.Second, 5*time.Millisecond,
		"the stale delivery must be acked and a drain fetched for its device")
	stop()

	assert.Equal(t, 1, ack.count(), "a stale delivery is settled — redelivering it can never succeed")
	assert.Equal(t, 0, exec.callCount(), "a stale delivery must NOT be actuated")
	assert.Empty(t, pub.responses(), "nothing ran, so there is no outcome to report")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.StaleDispatch), "the avoided duplicate is counted")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.LiveClaimErrors), "a stale dispatch is not a fault")
	assert.Equal(t, []string{"acme/pump-1"}, ff.fetched(), "the nudge drains THAT device's backlog")
}

// rowClaimer models command-delivery's rows closely enough to replay the double-actuation
// sequence end to end: the drain's claim moves a HELD/PARKED row to SENT on a new nonce, and
// the live confirmation succeeds only on (SENT, quoted nonce) and rotates it.
type rowClaimer struct {
	mu     sync.Mutex
	status map[string]string
	nonce  map[string]string
	minted int
}

func (c *rowClaimer) mint() string { c.minted++; return fmt.Sprintf("row-nonce-%d", c.minted) }

func (c *rowClaimer) Claim(_ context.Context, _, token string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.status[token]; s != statusHeld && s != statusParked {
		return "", false, nil
	}
	c.status[token], c.nonce[token] = "SENT", c.mint()
	return c.nonce[token], true, nil
}

func (c *rowClaimer) ClaimDispatch(_ context.Context, _, token, quoted string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status[token] != "SENT" || c.nonce[token] != quoted {
		return "", false, nil
	}
	c.nonce[token] = c.mint()
	return c.nonce[token], true, nil
}

// TestALateEnvelopeForARowAlreadyDrainedIsNotActuatedAgain replays the sequence the
// confirmation exists for: the sweep published c1 on n-env; the platform re-armed it (PARKED,
// still carrying n-env); the device's wake drain claimed and actuated it; THEN the original
// envelope arrives. It names a dispatch that no longer exists, so the hardware moves once. A
// second copy of an envelope that WAS confirmed loses the same way.
func TestALateEnvelopeForARowAlreadyDrainedIsNotActuatedAgain(t *testing.T) {
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	rows := &rowClaimer{
		status: map[string]string{"c1": statusParked, "c2": "SENT"},
		nonce:  map[string]string{"c1": "n-env", "c2": "n-env-2"},
	}
	ff := &fakeFetcher{cmds: []DrainCommand{
		{Token: "c1", Name: CommandWrite, Payload: []byte(`{"path":"/5/0/1","value":"u"}`), Status: statusParked},
	}}
	m := liveMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, liveLook(), exec, ff, rows, m, Options{})

	d.drain(context.Background(), drainJob{tenant: "acme", deviceToken: "pump-1"})
	require.Equal(t, 1, exec.callCount(), "fixture: the wake drain actuated the re-armed row")

	late := newAck()
	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, "n-env", late))
	assert.Equal(t, 1, exec.callCount(), "the late envelope must NOT actuate the device a second time")
	assert.True(t, late.acked(), "the late envelope is discarded, not left to redeliver")

	// A fresh envelope confirms once; its redelivered copy then names a superseded dispatch.
	first, dup := newAck(), newAck()
	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c2", CommandExecute, `{"path":"/5/0/2"}`, "n-env-2", first))
	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c2", CommandExecute, `{"path":"/5/0/2"}`, "n-env-2", dup))
	assert.Equal(t, 2, exec.callCount(), "c2 actuates exactly once across its delivery and its redelivery")
	assert.True(t, first.acked() && dup.acked())
	assert.Equal(t, 2.0, testutil.ToFloat64(m.StaleDispatch), "both discarded deliveries are counted")
}

// TestLiveClaimErrorLeavesTheMessageUnacked: a confirmation that could not be established FAILS
// CLOSED. The message is left unacked (an ack count of zero — messaging.Message has no Nak, so
// an unacked message redelivers at the ack deadline, never immediately), nothing actuates, and
// the fault is counted apart from a stale dispatch.
func TestLiveClaimErrorLeavesTheMessageUnacked(t *testing.T) {
	ack := newAck()
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	pub := &fakePublisher{}
	m := liveMetrics()
	d := NewDispatcher(nil, pub, liveLook(), exec, nil,
		&fakeClaimer{won: true, liveErr: errors.New("command-delivery unreachable")}, m, Options{})

	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, "n-env", ack))

	assert.Equal(t, 0, ack.count(), "an unconfirmed dispatch must be left for redelivery, never acked")
	assert.Equal(t, 0, exec.callCount(), "an unconfirmed dispatch must never actuate")
	assert.Empty(t, pub.responses())
	assert.Equal(t, 1.0, testutil.ToFloat64(m.LiveClaimErrors), "the outage is visible")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.StaleDispatch), "an outage is not a stale dispatch")
}

// TestLiveClaimAbortedByEvictionIsNeitherAckedNorCounted: the term was evicted while the
// confirmation was in flight. The error is the eviction's, not command-delivery's, so counting
// it would make every failover look like an outage; and the message stays for the next leader.
func TestLiveClaimAbortedByEvictionIsNeitherAckedNorCounted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ack := newAck()
	exec := &fakeExecutor{}
	m := liveMetrics()
	claimer := &fakeClaimer{won: true, liveErr: context.Canceled}
	claimer.onLive = cancel
	d := NewDispatcher(nil, &fakePublisher{}, liveLook(), exec, nil, claimer, m, Options{})

	d.dispatch(ctx, liveWorkNonce("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, "n-env", ack))

	assert.Equal(t, 0, ack.count(), "an evicted term leaves the message for the next leader")
	assert.Equal(t, 0, exec.callCount())
	assert.Equal(t, 0.0, testutil.ToFloat64(m.LiveClaimErrors),
		"a confirmation aborted by eviction is not a confirmation failure")
}

// TestEvictionBetweenClaimAndOpParksTheNewDispatch: the confirmation WON, then the term was
// evicted before the op. The row is now on the NEW dispatch, so the redelivered envelope will
// lose its own confirmation on the next leader; the command reaches the device only if this
// dispatcher hands the row back — and a hand-back quoting the envelope's nonce would match
// nothing. So it parks quoting the CONFIRMED nonce, and leaves the message unacked.
func TestEvictionBetweenClaimAndOpParksTheNewDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ack := newAck()
	exec := &fakeExecutor{}
	claimer := &fakeClaimer{won: true}
	claimer.onLive = cancel // evicted while the (successful) confirmation is in flight
	parker := &fakeParker{parked: true}
	d := NewDispatcher(nil, &fakePublisher{}, liveLook(), exec, nil, claimer, liveMetrics(), Options{Parker: parker})

	d.dispatch(ctx, liveWorkNonce("acme", "pump-1", "c1", CommandRead, `{"path":"/3/0/9"}`, "n-env", ack))

	assert.Equal(t, []parkCall{{tenant: "acme", commandToken: "c1", nonce: "confirmed-nonce-1"}}, parker.parks(),
		"the hand-back must quote the dispatch the confirmation created")
	assert.Equal(t, 0, exec.callCount(), "an evicted term does not actuate")
	assert.Equal(t, 0, ack.count(), "the message is left for redelivery, where it will be discarded as stale")
}

// TestLiveCommandWithNoNonceIsRefused: a delivery naming no dispatch can never be confirmed, so
// it is not actuated, and it is settled rather than left to burn its redeliveries.
func TestLiveCommandWithNoNonceIsRefused(t *testing.T) {
	ack := newAck()
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	claimer := &fakeClaimer{won: true}
	m := liveMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, liveLook(), exec, nil, claimer, m, Options{})

	d.dispatch(context.Background(),
		liveWorkNonce("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, "", ack))

	assert.Equal(t, 0, exec.callCount(), "a delivery naming no dispatch must not actuate")
	assert.Empty(t, claimer.liveClaims(), "there is nothing to confirm, so command-delivery is not asked")
	assert.Equal(t, 1, ack.count(), "it can never succeed, so it is settled")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.LiveClaimErrors), "a publisher fault is visible")
}

// TestLiveCommandWithoutAClaimerIsRefused: with no claimer wired nothing can be confirmed, and
// the live command FAILS CLOSED rather than taking the pre-confirmation fail-open path.
func TestLiveCommandWithoutAClaimerIsRefused(t *testing.T) {
	ack := newAck()
	exec := &fakeExecutor{result: OpResult{Op: labelWrite, Success: true}}
	m := liveMetrics()
	d := NewDispatcher(nil, &fakePublisher{}, liveLook(), exec, nil, nil, m, Options{})

	require.NotPanics(t, func() {
		d.dispatch(context.Background(),
			liveWorkNonce("acme", "pump-1", "c1", CommandWrite, `{"path":"/5/0/1","value":"u"}`, "n-env", ack))
	})

	assert.Equal(t, 0, exec.callCount(), "an unconfirmable live command must not actuate")
	assert.Equal(t, 0, ack.count(), "it is left for redelivery, like any other failure to confirm")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.LiveClaimErrors))
}
