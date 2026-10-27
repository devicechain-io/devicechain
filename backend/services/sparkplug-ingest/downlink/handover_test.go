// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The handover of the awaiting set from one leadership term to the next (G19 S4, test 9), and the
// loss of the lease between a claim and a publish.

// term runs the rig's dispatcher for one leadership term and returns once it is ready to serve (its
// previous term's state loaded); stop ends the term gracefully and waits for Run to return.
func (r *rig) term(t *testing.T) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); r.d.Run(ctx, func() { close(ready) }) }()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the term never became ready")
	}
	var once atomic.Bool
	stop := func() {
		if once.CompareAndSwap(false, true) {
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return ctx, stop
}

// liveIn works one live command in the given term.
func (r *rig) liveIn(ctx context.Context, token, name, payload string, headers map[string]string) *ackSpy {
	w, spy := r.msg("acme", map[string]string{CommandWrite: "pump-1", CommandRebirth: "node-3", CommandReboot: "node-3"}[name], token, name, payload, headers)
	r.d.live(ctx, w)
	return spy
}

var rebootHeaders = map[string]string{cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandImpact: "DISRUPTIVE"}

// 🔴 A graceful end of term resolves NOTHING and leaves every entry in the bucket; the next term
// loads them and re-arms them with the time left on their deadline, and the BIRTH the new leader's
// rebirth-all brings confirms them. The paired mutant (a term that does not load the bucket) must
// fail the last step.
func TestAGracefulEndOfTermLeavesTheEntriesAndTheNextTermReArmsThem(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	ctxA, stopA := r.term(t)
	r.liveIn(ctxA, "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	r.liveIn(ctxA, "rb", CommandRebirth, ``, nil)
	require.Len(t, r.src.published(), 2)
	stopA()

	assert.Empty(t, r.responses.all(), "a graceful end of term resolved a command")
	require.Equal(t, 2, r.store.len(), "a graceful end of term took the entries out of the bucket")
	assert.False(t, r.log.has("kvdel:"))

	// The next leader, some seconds later.
	clk.advance(20 * time.Second)
	r.build(nil)
	r.term(t)
	r.d.mu.Lock()
	armed := len(r.d.entries)
	r.d.mu.Unlock()
	assert.Equal(t, 2, armed, "the new term did not re-arm what the last left")
	assert.Equal(t, 2, r.result(ResultRearmed))
	assert.InDelta(t, 2, testutilGauge(r), 0, "the awaiting gauge does not count the re-armed entries")
	assert.Empty(t, r.responses.all(), "an entry with time left was resolved at the start of the term")

	// The new leader's rebirth-all: the node's births carry every current value.
	r.report(nodeBirth(clk.now(), 5))
	r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))
	c := r.responseTo(t, "c")
	assert.True(t, c.env.Success)
	assert.Equal(t, "claim-nonce", c.env.DispatchNonce, "the response must quote the nonce stored with the entry")
	assert.True(t, r.responseTo(t, "rb").env.Success)
	require.Eventually(t, func() bool { return r.store.len() == 0 }, confirmWait, 5*time.Millisecond)
}

func testutilGauge(r *rig) float64 {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	return float64(len(r.d.entries))
}

// Entries past their deadline are resolved at the start of the term, at once, with the nonce that
// was stored with them. A re-armed entry never confirms UNCHANGED, even past its deadline: the new
// leader did not see what the node reported while nobody was listening.
func TestEntriesPastTheirDeadlineAreResolvedAtOnceWithTheirStoredNonce(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.src.atValue, r.src.lastAt, r.src.lastVal = true, clk.now().Add(-time.Hour), i8(t, 5)
	ctxA, stopA := r.term(t)
	r.liveIn(ctxA, "late", CommandWrite, writeCmd("Setpoint", 5), nil)
	stopA()

	clk.advance(10 * time.Minute) // the window (2 min) ended while nobody was leading
	r.build(nil)
	r.term(t)
	res := r.responseTo(t, "late")
	assert.False(t, res.env.Success, "a handover cannot say the node stayed at a value it did not watch")
	assert.Equal(t, cmddownlink.ReachUnanswered, res.env.Reach)
	assert.Equal(t, "claim-nonce", res.env.DispatchNonce)
	assert.Zero(t, r.store.len())
	assert.Zero(t, r.result(ResultRearmed))
}

func TestAReArmedEntryCannotConfirmUnchangedWhereAnUninterruptedOneCan(t *testing.T) {
	for _, handover := range []bool{false, true} {
		r := newRig(t)
		clk := r.useClock()
		r.src.atValue, r.src.lastAt, r.src.lastVal = true, clk.now().Add(-time.Hour), i8(t, 5)
		ctxA, stopA := r.term(t)
		r.liveIn(ctxA, "c", CommandWrite, writeCmd("Setpoint", 5), nil)
		if handover {
			stopA()
			r.build(nil)
			r.term(t)
		}
		clk.advance(121 * time.Second)
		r.d.sweep()
		res := r.responseTo(t, "c")
		assert.Equal(t, !handover, res.env.Success, "handover=%v", handover)
	}
}

// Entries of a tenant this service has no source for are left alone; an entry whose source is no
// longer enabled cannot be learned about, and is resolved rather than left to hang.
func TestReArmingLeavesOtherTenantsAloneAndResolvesAnEntryWhoseSourceIsGone(t *testing.T) {
	r := newRig(t)
	r.clk = &testClock{t: time.Now()}
	r.src.now = r.clk.now
	r.build(nil)
	other := Awaiting{Command: "theirs", Device: "x", Source: "sparkplug:h1", Target: "g/n", Nonce: "n", Kind: KindRebirth,
		PublishedAt: r.clk.now(), Deadline: r.clk.now().Add(time.Minute)}
	require.NoError(t, r.store.Put(context.Background(), AwaitingKey("someone-else", "theirs"), other))
	gone := other
	gone.Command, gone.Source = "orphan", "sparkplug:retired-host"
	require.NoError(t, r.store.Put(context.Background(), AwaitingKey("acme", "orphan"), gone))
	live := other
	live.Command, live.Device, live.Target = "mine", "node-3", "plant-a/node-3"
	require.NoError(t, r.store.Put(context.Background(), AwaitingKey("acme", "mine"), live))

	r.term(t)
	_, ok := r.store.get(AwaitingKey("someone-else", "theirs"))
	assert.True(t, ok, "another tenant's entry was touched")
	assert.Empty(t, r.responses.of("theirs"))
	res := r.responseTo(t, "orphan")
	assert.False(t, res.env.Success)
	assert.Contains(t, errorOf(res), "no longer has commands enabled")
	require.NotNil(t, res.env.Payload)
	assert.JSONEq(t, `{"reason":"source_disabled"}`, *res.env.Payload)
	assert.Empty(t, r.responses.of("mine"), "a live entry was resolved at the start of the term")
	assert.Equal(t, 1, r.result(ResultRearmed))
}

// 🔴 A KV write failure means NO publish: the command is handed back (PARKED) with nothing left
// behind. (Gate 12; kept here beside the other bucket faults of test 9.)
func TestAnEntryThatCannotBeWrittenIsNotPublishedAndTheRowIsParked(t *testing.T) {
	r := newRig(t)
	r.store.putErr = errBoom
	spy := r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	assert.Empty(t, r.src.published())
	assert.True(t, r.log.has("park:c:claim-nonce"))
	assert.Equal(t, int32(1), spy.acks.Load())
	assert.Zero(t, r.store.len())
	r.d.mu.Lock()
	assert.Empty(t, r.d.entries, "an entry that was never recorded is being awaited")
	r.d.mu.Unlock()
}

// ---- the one-reboot-per-window rule survives a change of leader ----

// 🔴 The rule is kept in the bucket: a second reboot of the same node in the same window is refused
// by the NEXT leader, which loaded the mark at the start of its term. The paired controls: past the
// window it is sent; a node never rebooted is sent one; and a leader that could not read the bucket
// refuses to send any reboot at all (it cannot apply a rule to a history it does not have).
func TestTheOneRebootPerWindowRuleSurvivesAChangeOfLeader(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	ctxA, stopA := r.term(t)
	r.liveIn(ctxA, "boot-1", CommandReboot, ``, rebootHeaders)
	require.Len(t, r.src.published(), 1)
	require.True(t, r.log.has("kvreboot:plant-a/node-3"), "the mark was not written with the record")
	stopA()
	require.Len(t, r.store.reboots, 1)

	clk.advance(60 * time.Second)
	r.build(nil)
	ctxB, _ := r.term(t)
	r.liveIn(ctxB, "boot-2", CommandReboot, ``, rebootHeaders)
	assert.Len(t, r.src.published(), 1, "a second reboot went to a node inside its window")
	res := r.responses.of("boot-2")
	require.Len(t, res, 1)
	assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach)
	assert.Contains(t, errorOf(res[0]), "was already rebooted")
	assert.Equal(t, 1, r.result(ResultRefusedRebootGate))

	// Past the window (600 s from the first), the same command goes.
	clk.advance(600 * time.Second)
	r.liveIn(ctxB, "boot-3", CommandReboot, ``, rebootHeaders)
	assert.Len(t, r.src.published(), 2, "the window ended and the reboot was still refused")
}

func TestARebootWithNoMarkIsSent(t *testing.T) {
	r := newRig(t)
	r.useClock()
	ctx, _ := r.term(t)
	r.liveIn(ctx, "boot", CommandReboot, ``, rebootHeaders)
	assert.Len(t, r.src.published(), 1)
}

func TestALeaderThatCannotReadTheBucketRefusesRebootsButServesWrites(t *testing.T) {
	r := newRig(t)
	r.useClock()
	r.store.listErr = errBoom
	oldRetry, oldBg := seedRetryDelay, seedBackground
	seedRetryDelay, seedBackground = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { seedRetryDelay, seedBackground = oldRetry, oldBg })

	ctx, _ := r.term(t)
	r.liveIn(ctx, "boot", CommandReboot, ``, rebootHeaders)
	assert.Empty(t, r.src.published(), "a reboot was sent by a leader that could not read the reboot history")
	res := r.responses.of("boot")
	require.Len(t, res, 1)
	assert.Contains(t, errorOf(res[0]), "could not be read yet")
	// Writes are served all the same.
	r.liveIn(ctx, "w", CommandWrite, writeCmd("Setpoint", 5), nil)
	assert.Len(t, r.src.published(), 1)

	// The bucket comes back: the background load succeeds and reboots are served again.
	r.store.mu.Lock()
	r.store.listErr = nil
	r.store.mu.Unlock()
	require.Eventually(t, func() bool { return !r.d.unseeded.Load() }, confirmWait, 5*time.Millisecond)
	r.liveIn(ctx, "boot-again", CommandReboot, ``, rebootHeaders)
	assert.Len(t, r.src.published(), 2)
}

// A reboot that was not sent leaves no mark behind: the operator can re-issue it at once.
func TestARebootThatWasNotSentLeavesNoMark(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(r *rig)
	}{
		{"the session dropped after the record was written", func(r *rig) {
			r.src.afterHook = func() { r.src.mu.Lock(); r.src.up = false; r.src.mu.Unlock() }
		}},
		{"the mark itself could not be written", func(r *rig) { r.store.putRebootErr = errBoom }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.useClock()
			ctx, _ := r.term(t)
			tc.setup(r)
			r.liveIn(ctx, "boot-1", CommandReboot, ``, rebootHeaders)
			assert.Empty(t, r.src.published())
			assert.Empty(t, r.store.reboots, "a reboot that was never sent left a mark")
			r.d.mu.Lock()
			assert.Empty(t, r.d.reboots)
			assert.Empty(t, r.d.entries)
			r.d.mu.Unlock()
			assert.Zero(t, r.store.len(), "its record was left behind")

			// Re-issued once the node can take it.
			r.src.mu.Lock()
			r.src.afterHook, r.src.up = nil, true
			r.src.mu.Unlock()
			r.store.putRebootErr = nil
			r.liveIn(ctx, "boot-2", CommandReboot, ``, rebootHeaders)
			assert.Len(t, r.src.published(), 1, "a reboot that was never sent blocked the next one")
		})
	}
}

// ---- lost lease, through a real consumer ----

// 🔴 LOST LEASE. The term ends between the claim and the publish. Nothing is published by it, the
// row is handed back, and the message is LEFT UNACKED - so the broker redelivers it to the next
// leader, which publishes it exactly once. Run on a real JetStream consumer: that the message comes
// back is a property of the broker, not of a spy.
func TestALeaseLostBetweenTheClaimAndThePublishHandsTheCommandToTheNextLeader(t *testing.T) {
	nmgr := startJetStream(t, "inst-lostlease")
	nmgr.SetAckWaitForTesting(t, 700*time.Millisecond)
	reader, err := nmgr.NewReader(streams.DeviceCommands, messaging.ReaderWithDeliverNew())
	require.NoError(t, err)
	writer, err := nmgr.NewWriter(streams.DeviceCommands)
	require.NoError(t, err)

	r := newRig(t)
	build := func() {
		r.d = New(reader, r.responses, r.fetcher, r.claimer, r.parker, r.store, []Source{r.src},
			Metrics{Commands: r.commands, Awaiting: r.awaiting},
			Options{Workers: 1, ReadPacer: core.NewReadPacer(nil, "device commands"), ProgressEvery: 100 * time.Millisecond})
	}
	build()

	// Term A loses its lease while the record is being written.
	ctxA, cancelA := context.WithCancel(context.Background())
	r.src.afterHook = cancelA
	doneA := make(chan struct{})
	readyA := make(chan struct{})
	go func() { defer close(doneA); r.d.Run(ctxA, func() { close(readyA) }) }()
	<-readyA

	raw := json.RawMessage(writeCmd("Setpoint", 5))
	env, _ := json.Marshal(cmddownlink.DeliveryEnvelope{Token: "c", DeviceToken: "pump-1", Name: CommandWrite, Payload: &raw, DispatchNonce: "n-c"})
	require.NoError(t, writer.WriteToDevice(core.WithTenant(context.Background(), "acme"), "pump-1",
		messaging.Message{Value: env, Headers: map[string]string{cmddownlink.HeaderTransport: "sparkplug"}}))
	select {
	case <-doneA:
	case <-time.After(20 * time.Second):
		t.Fatal("term A never ended")
	}
	assert.Empty(t, r.src.published(), "a term that had lost its lease published a command")
	assert.True(t, r.log.has("park:c:claim-nonce"), "the claimed row was not handed back: %v", r.log.all())
	ackPending := consumerState(t, nmgr)
	assert.Equal(t, 1, ackPending, "the message was acked by the term that lost its lease, so no next leader would ever see it")
	assert.Zero(t, r.store.len(), "the record of a command that was not sent was left behind")

	// Term B: the broker redelivers the unacked message, and it is published exactly once.
	r.src.afterHook = nil
	build()
	ctxB, cancelB := context.WithCancel(context.Background())
	doneB := make(chan struct{})
	go func() { defer close(doneB); r.d.Run(ctxB, nil) }()
	t.Cleanup(func() { cancelB(); <-doneB })
	require.Eventually(t, func() bool { return len(r.src.published()) == 1 }, 20*time.Second, 20*time.Millisecond,
		"the next leader was never handed the command: %v", r.log.all())
	require.Eventually(t, func() bool { return consumerState(t, nmgr) == 0 }, 10*time.Second, 20*time.Millisecond,
		"the next leader never acknowledged the redelivered command")
	time.Sleep(1500 * time.Millisecond) // longer than the ack wait: a duplicate would show now
	assert.Len(t, r.src.published(), 1, "the command was published more than once")
	assert.Equal(t, 1, r.store.len())
}

// consumerState is how many messages the stream's consumers hold unacknowledged (delivered, not
// acked, whether or not their ack wait has run out).
func consumerState(t *testing.T, nmgr *messaging.NatsManager) (ackPending int) {
	t.Helper()
	js, err := nmgr.Conn().JetStream()
	require.NoError(t, err)
	for name := range js.StreamNames() {
		for info := range js.ConsumersInfo(name) {
			ackPending += info.NumAckPending
		}
	}
	return ackPending
}

// ---- the real bucket ----

// The KV store round-trips what the dispatcher writes and lists both kinds of key, against a real
// JetStream KV bucket (the registered one, created the way the service creates it).
func TestTheKVStoreRoundTripsEntriesAndRebootMarks(t *testing.T) {
	nmgr := startJetStream(t, "inst-kvstore")
	kv, err := nmgr.SparkplugCommandsAwaitingStore()
	require.NoError(t, err)
	store := NewKVAwaitingStore(kv)
	ctx := context.Background()

	empty, err := store.List(ctx)
	require.NoError(t, err, "an empty bucket is not an error")
	assert.Empty(t, empty.Entries)
	assert.Empty(t, empty.Reboots)

	now := time.Now().Truncate(time.Millisecond).UTC()
	a := Awaiting{Command: "cmd-1", Device: "pump-1", Source: "sparkplug:h1", Target: "plant-a/node-3/pump-1", Nonce: "n1",
		Kind: KindWrite, Metric: "Setpoint", Requested: json.RawMessage(`5`), Datatype: 1, HostRole: "primary",
		AtRequestedValue: true, LastReportedAt: now.Add(-time.Hour), PublishedAt: now, Deadline: now.Add(2 * time.Minute)}
	require.NoError(t, store.Put(ctx, AwaitingKey("acme", "cmd-1"), a))
	require.NoError(t, store.PutReboot(ctx, "acme", "plant-a", "node-3", now))
	// Something that is not ours: it must be reported, not fatal.
	_, err = kv.Put(base64.RawURLEncoding.EncodeToString([]byte("acme|junk")), []byte("not json"))
	require.NoError(t, err)

	got, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, got.Entries, 1)
	back := got.Entries[AwaitingKey("acme", "cmd-1")]
	assert.Equal(t, a.Command, back.Command)
	assert.Equal(t, "plant-a/node-3/pump-1", back.Target)
	assert.JSONEq(t, `5`, string(back.Requested))
	assert.True(t, back.AtRequestedValue)
	assert.True(t, back.Deadline.Equal(a.Deadline))
	require.Len(t, got.Reboots, 1)
	assert.Equal(t, RebootMark{Tenant: "acme", Group: "plant-a", Node: "node-3", At: got.Reboots[0].At}, got.Reboots[0])
	assert.True(t, got.Reboots[0].At.Equal(now))
	assert.Equal(t, []string{"acme|junk"}, got.Unreadable)

	require.NoError(t, store.Delete(ctx, AwaitingKey("acme", "cmd-1")))
	require.NoError(t, store.Delete(ctx, AwaitingKey("acme", "cmd-1")), "deleting an absent key is not an error")
	require.NoError(t, store.DeleteReboot(ctx, "acme", "plant-a", "node-3"))
	got, err = store.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, got.Entries)
	assert.Empty(t, got.Reboots)
}

// The whole handover against the real bucket: term A publishes and ends, term B (a new dispatcher)
// lists the bucket, re-arms, and the new birth confirms the command; the reboot mark held it back.
func TestAHandoverThroughTheRealBucket(t *testing.T) {
	nmgr := startJetStream(t, "inst-handover")
	kv, err := nmgr.SparkplugCommandsAwaitingStore()
	require.NoError(t, err)
	real := NewKVAwaitingStore(kv)

	r := newRig(t)
	clk := r.useClock()
	r.d = New(nil, r.responses, r.fetcher, r.claimer, r.parker, real, []Source{r.src},
		Metrics{Commands: r.commands, Awaiting: r.awaiting}, Options{Workers: 2, Now: clk.now})
	ctxA, stopA := r.term(t)
	r.liveIn(ctxA, "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	r.liveIn(ctxA, "boot", CommandReboot, ``, rebootHeaders)
	require.Len(t, r.src.published(), 2)
	stopA()
	assert.Empty(t, r.responses.all())

	clk.advance(30 * time.Second)
	r.d = New(nil, r.responses, r.fetcher, r.claimer, r.parker, real, []Source{r.src},
		Metrics{Commands: r.commands, Awaiting: r.awaiting}, Options{Workers: 2, Now: clk.now})
	ctxB, _ := r.term(t)
	assert.Equal(t, 2, r.result(ResultRearmed))
	r.liveIn(ctxB, "boot-2", CommandReboot, ``, rebootHeaders)
	assert.Len(t, r.src.published(), 2, "the mark in the real bucket did not hold the second reboot back")

	r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))
	assert.True(t, r.responseTo(t, "c").env.Success)
	r.report(nodeBirth(clk.now(), 99))
	assert.True(t, r.responseTo(t, "boot").env.Success)
	require.Eventually(t, func() bool {
		l, err := real.List(context.Background())
		return err == nil && len(l.Entries) == 0
	}, confirmWait, 20*time.Millisecond, "the confirmed commands' records were left in the bucket")
	l, err := real.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, l.Reboots, 1, "the reboot mark must survive the reboot's resolution")
}
