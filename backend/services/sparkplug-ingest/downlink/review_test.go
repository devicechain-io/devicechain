// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

func rows(n int) []cmddownlink.DrainCommand {
	out := make([]cmddownlink.DrainCommand, n)
	for i := range out {
		out[i] = cmddownlink.DrainCommand{Token: fmt.Sprintf("d%d", i+1), Name: CommandWrite, Payload: []byte(writeCmd("Setpoint", i))}
	}
	return out
}

// drainWithin runs one drain for pump-1 and fails the test if it does not return: a drain that
// claims the rows it hands back never does, and must not hang the suite.
func drainWithin(t *testing.T, r *rig, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.d.drain(ctx, deviceKey{"acme", "pump-1"}); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		cancel() // let the spinning drain unwind
		<-done
		t.Fatal("the drain never returned: it is claiming the rows it hands back")
	}
}

// A drain that hands a row back must stop. The backlog returns the row at once, so a drain that
// carried on would claim it, park it and fetch it again for as long as the device stayed unborn.
// Six rows, none addressable: exactly one claim, one park, one fetch.
func TestADrainStopsAtTheFirstRowItHandsBack(t *testing.T) {
	r := newRig(t)
	r.src.notBorn = true
	r.fetcher.rows = rows(6)

	drainWithin(t, r, 5*time.Second)

	assert.Equal(t, 1, r.log.count("drainclaim:"), "events: %v", r.log.all())
	assert.Equal(t, 1, r.log.count("park:"))
	assert.Equal(t, 1, r.log.count("fetch:"))
	assert.Empty(t, r.src.published())

	// Control: with the table there, the same six rows are all sent (the stop is the hand-back's).
	r2 := newRig(t)
	r2.fetcher.rows = rows(6)
	r2.d.drain(context.Background(), deviceKey{"acme", "pump-1"})
	assert.Len(t, r2.src.published(), 6, "a drain that hands nothing back must carry on through the backlog")
}

// The same stop when the hand-back happens in the send (the session dropped after the record).
func TestADrainStopsWhenTheSendHandsTheRowBack(t *testing.T) {
	r := newRig(t)
	r.src.afterHook = func() { r.src.mu.Lock(); r.src.up = false; r.src.mu.Unlock() }
	r.fetcher.rows = rows(5)
	drainWithin(t, r, 5*time.Second)
	assert.Equal(t, 1, r.log.count("drainclaim:"), "events: %v", r.log.all())
	assert.Equal(t, 1, r.log.count("park:"))
}

// Each batch is located afresh: a node that dies between batches ends the drain.
func TestADrainLocatesTheDeviceAgainForEveryBatch(t *testing.T) {
	r := newRig(t)
	r.fetcher.rows = rows(8) // two batches of four
	r.claimer.onClaim = func(token string) {
		r.fetcher.remove(token)
		if token == "d4" { // the last of the first batch: the node dies
			r.src.mu.Lock()
			delete(r.src.index, "pump-1")
			r.src.mu.Unlock()
		}
	}
	r.d.drain(context.Background(), deviceKey{"acme", "pump-1"})
	assert.Len(t, r.src.published(), 4, "the second batch was sent to a device that was no longer born")
	assert.Equal(t, 1, r.log.count("fetch:"), "the dead device's second batch was fetched")
	assert.Equal(t, 4, r.log.count("drainclaim:"), "the dead device's second batch was claimed")
}

// ---- reboots are never parked ----

func TestARebootIsNeverParked(t *testing.T) {
	good := map[string]string{cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandImpact: "DISRUPTIVE"}

	check := func(t *testing.T, r *rig) {
		t.Helper()
		assert.False(t, r.log.has("park:"), "a reboot was parked: %v", r.log.all())
		assert.Equal(t, 1, r.result(ResultRefusedRebootGate))
		assert.Zero(t, r.result(ResultParked))
		assert.Empty(t, r.src.published())
		res := r.responses.of("rb")
		require.Len(t, res, 1)
		assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach)
		assert.Equal(t, msgRebootOffline, errorOf(res[0]))
	}

	t.Run("live: the node is not born", func(t *testing.T) {
		r := newRig(t)
		delete(r.src.index, "node-3")
		r.live("acme", "node-3", "rb", CommandReboot, ``, good)
		check(t, r)
	})
	t.Run("live: the node's table is not there yet", func(t *testing.T) {
		r := newRig(t)
		r.src.notBorn = true
		r.live("acme", "node-3", "rb", CommandReboot, ``, good)
		check(t, r)
	})
	t.Run("live: the session drops after the record", func(t *testing.T) {
		r := newRig(t)
		r.src.afterHook = func() { r.src.mu.Lock(); r.src.up = false; r.src.mu.Unlock() }
		r.live("acme", "node-3", "rb", CommandReboot, ``, good)
		check(t, r)
		assert.Zero(t, r.store.len(), "the record of an unsent reboot was left behind")
	})
	t.Run("drain: refused before any other gate", func(t *testing.T) {
		r := newRig(t)
		r.src.breaker = host.BreakerOpen // the breaker gate would otherwise answer first
		r.fetcher.rows = []cmddownlink.DrainCommand{{Token: "rb", Name: CommandReboot}}
		r.d.drain(context.Background(), deviceKey{"acme", "node-3"})
		check(t, r)
		assert.Zero(t, r.result(ResultRefusedBreaker), "the breaker answered a reboot before the reboot rule did")
	})

	// Control: an online node takes the reboot, and a write that parks still parks.
	t.Run("control: online node", func(t *testing.T) {
		r := newRig(t)
		r.live("acme", "node-3", "rb", CommandReboot, ``, good)
		require.Len(t, r.src.published(), 1)
	})
	t.Run("control: a write to an unborn device still parks", func(t *testing.T) {
		r := newRig(t)
		r.live("acme", "unborn", "w", CommandWrite, writeCmd("Setpoint", 1), nil)
		assert.True(t, r.log.has("park:w:"))
	})
}

// ---- an unknown publish outcome is not "not sent" ----

func TestAPublishWithAnUnknownOutcomeIsUnansweredAndKeepsItsRecord(t *testing.T) {
	r := newRig(t)
	r.src.publishErr = &host.PublishIndeterminateError{Err: errBoom}
	spy := r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)

	res := r.responses.of("c")
	require.Len(t, res, 1)
	assert.Equal(t, cmddownlink.ReachUnanswered, res[0].env.Reach, "a publish that may have reached the node is TIMEOUT, not the retry-safe NOT_DELIVERED")
	assert.Contains(t, errorOf(res[0]), "may have reached the node")
	assert.Equal(t, "claim-nonce", res[0].env.DispatchNonce)
	_, ok := r.store.get(AwaitingKey("acme", "c"))
	assert.True(t, ok, "the record of a command that may have gone out was deleted")
	assert.False(t, r.log.has("kvdel:"))
	assert.False(t, r.log.has("park:"), "a command that may have been sent was handed back to be sent again")
	assert.Equal(t, int32(1), spy.acks.Load())
	assert.Equal(t, 1, r.result(ResultPublishFailed))

	// Control: an error that says nothing reached the wire still refuses it as not delivered.
	r2 := newRig(t)
	r2.src.publishErr = host.ErrBreakerOpen
	r2.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	assert.Equal(t, cmddownlink.ReachUnsent, r2.responses.of("c")[0].env.Reach)
}

// Only a finished term re-sends. A context that is cancelled while a publish of unknown outcome
// returns is still an unknown outcome, and must not be handed back to go out a second time.
func TestOnlyAnEndedTermIsReSent(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.src.publishErr = &host.PublishIndeterminateError{Err: errBoom}
	r.src.afterHook = cancel
	w, _ := r.msg("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	r.d.live(ctx, w)
	assert.False(t, r.log.has("park:"), "a command that may have been published was parked for a second send")
	res := r.responses.of("c")
	require.Len(t, res, 1)
	assert.Equal(t, cmddownlink.ReachUnanswered, res[0].env.Reach)
}

// ---- the table moved while the record was written ----

func TestACommandWhoseTableMovedIsRecordedAgainAndSentOnce(t *testing.T) {
	r := newRig(t)
	r.src.moves = 1
	spy := r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)

	assert.Len(t, r.src.published(), 1, "events: %v", r.log.all())
	assert.Equal(t, 2, r.log.count("kvput:"), "the command was not recorded again against the new table")
	assert.Equal(t, 1, r.log.count("kvdel:"), "the stale record was not retracted")
	assert.Less(t, r.log.index("kvdel:"), r.log.index("publish:"))
	_, ok := r.store.get(AwaitingKey("acme", "c"))
	assert.True(t, ok)
	assert.Equal(t, int32(1), spy.acks.Load())
}

func TestANodeThatKeepsMovingIsRefusedAndLeavesNoRecord(t *testing.T) {
	r := newRig(t)
	r.src.moves = 99
	r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	assert.Empty(t, r.src.published())
	assert.Zero(t, r.store.len(), "records were left behind by a command that never went out")
	assert.Equal(t, 1, r.result(ResultRefusedMetric))
	res := r.responses.of("c")
	require.Len(t, res, 1)
	assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach)
}

// ---- ordering: a live command does not overtake an older parked one ----

func TestALiveCommandDoesNotOvertakeTheDevicesParkedBacklog(t *testing.T) {
	r := newRig(t)
	r.fetcher.rows = rows(1)
	s := newShard()
	key := deviceKey{"acme", "pump-1"}
	w, _ := r.msg("acme", "pump-1", "live", CommandWrite, writeCmd("Mode", "x"), nil)

	// The wake is already waiting when the live command is taken.
	s.wake(key)
	s.ch <- w
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.d.serve(ctx, s)
	require.Eventually(t, func() bool { return len(r.src.published()) == 2 }, 5*time.Second, 5*time.Millisecond, "events: %v", r.log.all())

	drained, live := r.log.index("drainclaim:d1"), r.log.index("claim:live")
	require.True(t, drained >= 0 && live >= 0)
	assert.Less(t, drained, live, "the live command was sent before the older parked one: %v", r.log.all())
}

// ---- smaller things ----

func TestTrailingDataInAWriteIsRefused(t *testing.T) {
	// A live payload is valid JSON by construction; a drained one is the row's text, which is not.
	for name, payload := range map[string]string{
		"a second object":   writeCmd("Setpoint", 5) + `{"x":1}`,
		"trailing garbage":  writeCmd("Setpoint", 5) + ` junk`,
		"a value then more": `{"metric":"Setpoint","value":5} 6`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.fetcher.rows = []cmddownlink.DrainCommand{{Token: "c", Name: CommandWrite, Payload: []byte(payload)}}
			r.d.drain(context.Background(), deviceKey{"acme", "pump-1"})
			assert.Equal(t, 1, r.result(ResultRefusedPayload), "events: %v", r.log.all())
			assert.Empty(t, r.src.published())
		})
	}
	// Control: the same command with only whitespace after it is sent.
	r := newRig(t)
	r.fetcher.rows = []cmddownlink.DrainCommand{{Token: "c", Name: CommandWrite, Payload: []byte(writeCmd("Setpoint", 5) + " ")}}
	r.d.drain(context.Background(), deviceKey{"acme", "pump-1"})
	assert.Len(t, r.src.published(), 1)
}

func TestAParkWithNoDispatchIsAnsweredNotSilentlyAcked(t *testing.T) {
	r := newRig(t)
	env := cmddownlink.DeliveryEnvelope{Token: "c", DeviceToken: "unborn", Name: CommandWrite}
	b, _ := json.Marshal(env)
	spy := &ackSpy{log: r.log, token: "c"}
	m := messaging.NewConsumedMessage("i.acme.device-commands.unborn", b, 1, map[string]string{cmddownlink.HeaderTransport: "sparkplug"}, spy)
	r.d.live(context.Background(), work{msg: m, tenant: "acme", env: env})

	res := r.responses.of("c")
	require.Len(t, res, 1, "a command that could not be parked was acked in silence")
	assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach)
	assert.False(t, r.log.has("park:"))
	assert.Equal(t, int32(1), spy.acks.Load())
}

// A wait shorter than the progress interval is reported at once, not never.
func TestAShortRateWaitIsStillReportedAsInProgress(t *testing.T) {
	r := newRig(t)
	r.src.cfg.PublishPerSecond, r.src.cfg.Burst = 20, 1 // a 50 ms wait
	r.build(func(o *Options) { o.ProgressEvery = time.Minute })
	r.live("acme", "pump-1", "first", CommandWrite, writeCmd("Setpoint", 1), nil)
	spy := r.live("acme", "pump-1", "second", CommandWrite, writeCmd("Setpoint", 2), nil)
	assert.GreaterOrEqual(t, spy.inProgress.Load(), int32(1), "a command that waited was never reported as in progress")
	assert.Len(t, r.src.published(), 2)
}

// The term does not end with work still running: the goroutine resolving a tripped command is
// waited for, and a drain retry that has not fired is stopped.
func TestRunWaitsForTheTripResolutionAndStopsPendingRetries(t *testing.T) {
	r := newRig(t)
	r.responses.delay = 150 * time.Millisecond
	reader := &chanReader{msgs: make(chan messaging.Message, 1)}
	r.d = New(reader, r.responses, r.fetcher, r.claimer, r.parker, r.store, []Source{r.src},
		Metrics{Commands: r.commands, Awaiting: r.awaiting}, Options{Workers: 1, ReadPacer: core.NewReadPacer(nil, "device commands")})

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); r.d.Run(ctx, func() { close(ready) }) }()
	<-ready

	// One command in flight, a trip resolving it, and a retry waiting to fire.
	w, spy := r.msg("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 1), nil)
	reader.msgs <- w.msg
	require.Eventually(t, func() bool { return spy.acks.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
	r.src.hooks.Tripped(time.Now())
	r.d.retryDrain(ctx, deviceKey{"acme", "pump-1"})
	r.d.mu.Lock()
	pending := len(r.d.timers)
	r.d.mu.Unlock()
	require.Equal(t, 1, pending)

	cancel()
	<-done
	assert.Len(t, r.responses.of("c"), 1, "Run returned before the trip's resolution was published")
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	assert.Empty(t, r.d.timers, "a drain retry was left armed after the term ended")
}
