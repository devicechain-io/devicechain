// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// The confirmation of a published command (G19 S4, tests 7 and 8), driven through the hook the
// dispatcher installs on its source (hooks.Reported), exactly as a Client's receive path calls it.

const confirmWait = 5 * time.Second

func i8(t *testing.T, n int) host.WireValue {
	t.Helper()
	v, err := host.RequestedWire(dtInt8, n)
	require.NoError(t, err)
	return v
}

func (r *rig) report(rs ...host.Report) { r.src.hooks.Reported("acme", rs) }

// metric is a DDATA report of an Int8 Setpoint on pump-1, timestamped at.
func metric(t *testing.T, name string, v host.WireValue, at time.Time) host.Report {
	return host.Report{Kind: host.ReportMetric, Target: devT, Name: name, Via: "DDATA", Value: v, At: at, ReceivedAt: at}
}

func nodeBirth(at time.Time, bd uint64) host.Report {
	return host.Report{Kind: host.ReportNodeBirth, Target: nodeT, Via: "NBIRTH", ReceivedAt: at, BdSeq: bd, HasBdSeq: true}
}

// responseTo waits for the single response a command gets.
func (r *rig) responseTo(t *testing.T, token string) response {
	t.Helper()
	require.Eventually(t, func() bool { return len(r.responses.of(token)) >= 1 }, confirmWait, 5*time.Millisecond,
		"%s was never answered: %v", token, r.log.all())
	res := r.responses.of(token)
	require.Len(t, res, 1, "%s was answered more than once", token)
	return res[0]
}

func (r *rig) noResponse(t *testing.T, token string) {
	t.Helper()
	time.Sleep(60 * time.Millisecond)
	assert.Empty(t, r.responses.of(token), "%s was answered when it should still be awaiting", token)
	_, ok := r.store.get(AwaitingKey("acme", token))
	assert.True(t, ok, "the record of %s is gone although it is still awaiting", token)
}

func evidenceOf(t *testing.T, res response) evidence {
	t.Helper()
	require.NotNil(t, res.env.Payload, "a confirmed command carries its evidence")
	var ev evidence
	require.NoError(t, json.Unmarshal([]byte(*res.env.Payload), &ev))
	return ev
}

// ---- TEST 7: the tap ----

// 🔴 A report of the requested value, after the publish, confirms the command: SUCCESSFUL with the
// evidence, quoting the nonce the claim returned, and its record is deleted once the response is out.
func TestAReportOfTheRequestedValueConfirmsTheCommand(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	require.Len(t, r.src.published(), 1)
	_, ok := r.store.get(AwaitingKey("acme", "c"))
	require.True(t, ok)

	clk.advance(3 * time.Second)
	r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))

	res := r.responseTo(t, "c")
	assert.True(t, res.env.Success)
	assert.Empty(t, res.env.Reach, "reach is omitted on success")
	assert.Equal(t, "claim-nonce", res.env.DispatchNonce, "the response must quote the nonce of the claim")
	ev := evidenceOf(t, res)
	assert.Equal(t, "DDATA", ev.Via)
	assert.Equal(t, "Setpoint", ev.Metric)
	assert.JSONEq(t, `5`, string(ev.Value))
	assert.Equal(t, config.HostRolePrimary, ev.HostRole)
	assert.Equal(t, devT.Topic(), ev.Wire.Topic, "the evidence names the topic the command went to")
	assert.Equal(t, "abc", ev.Wire.SHA256, "the evidence keeps the digest of the bytes that were sent")
	require.Eventually(t, func() bool { return r.store.len() == 0 }, confirmWait, 5*time.Millisecond, "the record was not deleted after the response")
	assert.Equal(t, 1, r.result(ResultConfirmed))
	assert.Zero(t, r.result(ResultUnconfirmed))
	r.d.mu.Lock()
	assert.Empty(t, r.d.entries)
	r.d.mu.Unlock()
}

// A report of ANOTHER value does not confirm the command; the window then ends UNANSWERED. The pair
// of the test above: the value is what decides.
func TestAReportOfAnotherValueDoesNotConfirm(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)

	clk.advance(10 * time.Second)
	r.report(metric(t, "Setpoint", i8(t, 4), clk.now()))
	r.noResponse(t, "c")

	clk.advance(120 * time.Second)
	r.d.sweep()
	res := r.responseTo(t, "c")
	assert.False(t, res.env.Success)
	assert.Equal(t, cmddownlink.ReachUnanswered, res.env.Reach)
	assert.Contains(t, errorOf(res), "no report of Setpoint at the requested value within 2m0s")
	assert.Contains(t, errorOf(res), "Sparkplug has no command acknowledgement")
	// The console keys on the code, not on that wording.
	require.NotNil(t, res.env.Payload)
	assert.JSONEq(t, `{"reason":"unconfirmed_timeout"}`, *res.env.Payload)
	assert.Equal(t, "claim-nonce", res.env.DispatchNonce)
	assert.Equal(t, 1, r.result(ResultUnconfirmed))
	assert.Zero(t, r.store.len())
}

// A report of the value from ANOTHER metric, device, or source confirms nothing.
func TestAReportIsMatchedOnItsMetricItsTargetAndItsSource(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)

	other := metric(t, "Setpoint", i8(t, 5), clk.now())
	other.Target = host.CommandTarget{Group: "plant-a", Node: "node-3", Device: "pump-2"}
	r.report(other)
	r.report(metric(t, "Big", i8(t, 5), clk.now()))
	r.noResponse(t, "c")

	// Control: the right metric on the right device confirms.
	r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))
	assert.True(t, r.responseTo(t, "c").env.Success)

	// Another source of the tenant: the same names on another broker are another node.
	r2 := newRig(t)
	clk2 := r2.useClock()
	r2.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	foreign := newSource(r2.log, "acme")
	foreign.born("pump-1", devT)
	r2.d.onReports(&otherSource{foreign}, "acme", []host.Report{metric(t, "Setpoint", i8(t, 5), clk2.now())})
	r2.noResponse(t, "c")
}

// otherSource is a source with another identity.
type otherSource struct{ *fakeSource }

func (otherSource) SourceID() string { return "sparkplug:another-host" }

// 🔴 THE SKEW RULE. A report whose timestamp is older than the command by more than the source's
// clock skew is the node saying how things WERE; one inside the skew confirms. Boundary both sides.
func TestAReportOlderThanTheCommandIsNotAConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		before  time.Duration
		confirm bool
	}{
		{"31s before the publish, outside the 30s skew", 31 * time.Second, false},
		{"29s before the publish, inside the skew", 29 * time.Second, true},
		{"after the publish", -time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			clk := r.useClock()
			r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
			r.report(metric(t, "Setpoint", i8(t, 5), clk.now().Add(-tc.before)))
			if tc.confirm {
				assert.True(t, r.responseTo(t, "c").env.Success)
			} else {
				r.noResponse(t, "c")
			}
		})
	}
}

// UNCHANGED (primary host only): a window that ends with the node already at the value and nothing
// contrary seen confirms the command, as UNCHANGED since the node's last report. The controls: a
// contrary report, a secondary host, and a node that was not at the value all end UNANSWERED.
func TestAWindowThatEndsWithTheNodeAlreadyAtTheValueConfirmsUnchanged(t *testing.T) {
	since := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		name      string
		role      string
		atValue   bool
		contrary  bool
		unchanged bool
	}{
		{"primary, at the value, nothing contrary", config.HostRolePrimary, true, false, true},
		{"CONTROL primary, a contrary report arrived", config.HostRolePrimary, true, true, false},
		{"CONTROL secondary host", config.HostRoleSecondary, true, false, false},
		{"CONTROL the node was not at the value", config.HostRolePrimary, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			clk := r.useClock()
			r.src.cfg.HostRole = tc.role
			r.src.atValue, r.src.lastAt, r.src.lastVal = tc.atValue, since, i8(t, 5)
			r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
			if tc.contrary {
				r.report(metric(t, "Setpoint", i8(t, 9), clk.now().Add(time.Second)))
			}
			clk.advance(121 * time.Second)
			r.d.sweep()
			res := r.responseTo(t, "c")
			if !tc.unchanged {
				assert.False(t, res.env.Success)
				assert.Equal(t, cmddownlink.ReachUnanswered, res.env.Reach)
				assert.Equal(t, 1, r.result(ResultUnconfirmed))
				return
			}
			assert.True(t, res.env.Success)
			ev := evidenceOf(t, res)
			assert.Equal(t, "UNCHANGED", ev.Via)
			require.NotNil(t, ev.Since)
			assert.True(t, ev.Since.Equal(since), "since is when the node last reported the value")
			assert.Equal(t, 1, r.result(ResultConfirmedUnchanged))
			assert.Zero(t, r.result(ResultUnconfirmed))
		})
	}
}

// A newer write to the same metric resolves the one still awaiting (exercised by the supersede test
// in dispatcher_test) and must not be confirmed by a report meant for the old value: the entry is
// gone from the mirror.
func TestASupersededWriteIsNotConfirmedByALaterReport(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "pump-1", "old", CommandWrite, writeCmd("Setpoint", 1), nil)
	r.live("acme", "pump-1", "new", CommandWrite, writeCmd("Setpoint", 2), nil)
	old := r.responseTo(t, "old")
	assert.False(t, old.env.Success)

	r.report(metric(t, "Setpoint", i8(t, 1), clk.now()))
	r.noResponse(t, "new")
	assert.Len(t, r.responses.of("old"), 1, "the superseded command was answered a second time")
	r.report(metric(t, "Setpoint", i8(t, 2), clk.now()))
	assert.True(t, r.responseTo(t, "new").env.Success)
}

// A rebirth is confirmed by any NBIRTH of the node after the publish, not before it.
func TestARebirthIsConfirmedByTheNodesNextBirth(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "node-3", "rb", CommandRebirth, ``, nil)
	require.Len(t, r.src.published(), 1)

	r.report(nodeBirth(clk.now().Add(-time.Second), 5)) // a birth from before the command
	r.noResponse(t, "rb")
	// A metric report is not a birth.
	r.report(metric(t, "Setpoint", i8(t, 1), clk.now()))
	r.noResponse(t, "rb")

	clk.advance(2 * time.Second)
	r.report(nodeBirth(clk.now(), 5))
	res := r.responseTo(t, "rb")
	assert.True(t, res.env.Success)
	ev := evidenceOf(t, res)
	assert.Equal(t, "NBIRTH", ev.Via)
	assert.Equal(t, KindRebirth, ev.Kind)
}

// A rebirth for a device resolves to the node: the node's NBIRTH confirms it.
func TestARebirthSentToADeviceIsConfirmedByItsNodesBirth(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "pump-1", "rb", CommandRebirth, ``, nil)
	require.Len(t, r.src.published(), 1)
	clk.advance(time.Second)
	r.report(nodeBirth(clk.now(), 5))
	assert.True(t, r.responseTo(t, "rb").env.Success)
}

// 🔴 A reboot is confirmed only by an NBIRTH whose bdSeq differs from the one the node had when the
// command was published: a same-bdSeq NBIRTH is the node answering a rebirth, not coming back.
func TestARebootIsConfirmedOnlyByANewBdSeq(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.src.bdSeq = 7
	r.live("acme", "node-3", "boot", CommandReboot, ``, map[string]string{
		cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandImpact: "DISRUPTIVE"})
	require.Len(t, r.src.published(), 1)

	clk.advance(5 * time.Second)
	r.report(nodeBirth(clk.now(), 7)) // the same session, re-announced
	r.noResponse(t, "boot")

	clk.advance(5 * time.Second)
	r.report(nodeBirth(clk.now(), 8))
	res := r.responseTo(t, "boot")
	assert.True(t, res.env.Success)
	ev := evidenceOf(t, res)
	require.NotNil(t, ev.BdSeqBefore)
	require.NotNil(t, ev.BdSeqAfter)
	assert.Equal(t, uint64(7), *ev.BdSeqBefore)
	assert.Equal(t, uint64(8), *ev.BdSeqAfter)
}

// A reboot has its own window (default 600 s), a write its own (120 s), and a deadline is clamped
// to the command's expiry.
func TestEachKindIsAwaitedForItsOwnWindowClampedToTheCommandsExpiry(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "pump-1", "w", CommandWrite, writeCmd("Setpoint", 5), nil)
	r.live("acme", "node-3", "boot", CommandReboot, ``, map[string]string{
		cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandImpact: "DISRUPTIVE"})
	exp := clk.now().Add(30 * time.Second)
	r.live("acme", "pump-1", "short", CommandWrite, writeCmd("Mode", "x"), map[string]string{
		cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandExpires: exp.Format(time.RFC3339)})

	aw := func(token string) Awaiting { a, _ := r.store.get(AwaitingKey("acme", token)); return a }
	assert.Equal(t, 120*time.Second, aw("w").Deadline.Sub(aw("w").PublishedAt))
	assert.Equal(t, 600*time.Second, aw("boot").Deadline.Sub(aw("boot").PublishedAt))
	assert.WithinDuration(t, exp, aw("short").Deadline, time.Second, "the window is clamped to the command's own expiry")

	clk.advance(31 * time.Second)
	r.d.sweep()
	assert.Len(t, r.responses.of("short"), 1, "the clamped window ended")
	assert.Empty(t, r.responses.of("w"))
	assert.Empty(t, r.responses.of("boot"))
	clk.advance(100 * time.Second)
	r.d.sweep()
	assert.Len(t, r.responses.of("w"), 1)
	assert.Empty(t, r.responses.of("boot"), "a reboot is awaited past a write's window")
}

// The sweep is what ends a window: nothing resolves before its deadline.
func TestNothingIsResolvedBeforeItsDeadline(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	clk.advance(119 * time.Second)
	r.d.sweep()
	r.noResponse(t, "c")
	clk.advance(2 * time.Second)
	r.d.sweep()
	r.responseTo(t, "c")
}

// A response that cannot be published leaves the command awaiting, and the sweep tries again: the
// command is not forgotten, and it is answered exactly once.
func TestAResponseThatCannotBePublishedIsRetriedNotForgotten(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)

	r.responses.mu.Lock()
	r.responses.err = errBoom
	r.responses.mu.Unlock()
	r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))
	require.Eventually(t, func() bool {
		r.d.mu.Lock()
		defer r.d.mu.Unlock()
		e := r.d.entries[AwaitingKey("acme", "c")]
		return e != nil && !e.resolving && !e.retryAt.IsZero()
	}, confirmWait, 5*time.Millisecond, "the failed response was never put back for a retry")
	assert.Empty(t, r.responses.of("c"))
	_, ok := r.store.get(AwaitingKey("acme", "c"))
	assert.True(t, ok, "the record was deleted although the response was never published")
	assert.Zero(t, r.result(ResultConfirmed), "a confirmation was counted that never reached command-delivery")

	r.responses.mu.Lock()
	r.responses.err = nil
	r.responses.mu.Unlock()
	r.d.sweep() // before the retry is due
	assert.Empty(t, r.responses.of("c"))
	clk.advance(responseRetryEvery)
	r.d.sweep()
	res := r.responseTo(t, "c")
	assert.True(t, res.env.Success, "the retry must publish the outcome that was decided, not re-decide it")
	require.Eventually(t, func() bool { return r.store.len() == 0 }, confirmWait, 5*time.Millisecond)
}

// 🔴 A publish whose outcome is unknown is AWAITED like any other: the window settles it. A report
// of the value confirms it (the bytes did reach the node); with none, the window ends UNANSWERED.
func TestAnUnknownPublishOutcomeIsSettledByTheWindow(t *testing.T) {
	for _, confirms := range []bool{true, false} {
		r := newRig(t)
		clk := r.useClock()
		r.src.publishErr = &host.PublishIndeterminateError{Err: errBoom}
		spy := r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
		assert.Equal(t, int32(1), spy.acks.Load())
		assert.Empty(t, r.responses.of("c"), "an unknown outcome was answered at once")
		_, ok := r.store.get(AwaitingKey("acme", "c"))
		require.True(t, ok)

		if confirms {
			r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))
			assert.True(t, r.responseTo(t, "c").env.Success)
			continue
		}
		clk.advance(121 * time.Second)
		r.d.sweep()
		res := r.responseTo(t, "c")
		assert.Equal(t, cmddownlink.ReachUnanswered, res.env.Reach)
		assert.False(t, res.env.Success)
	}
}

// The installed hook is the wiring: New attaches a Reported hook to every source, and with nothing
// awaiting a report costs a counter read.
func TestNewAttachesTheReportHookToEverySource(t *testing.T) {
	r := newRig(t)
	require.NotNil(t, r.src.hooks.Reported, "the dispatcher never listens to what the source's nodes report")
	r.report(metric(t, "Setpoint", i8(t, 5), time.Now()))
	assert.Empty(t, r.responses.all())
}

// ---- TEST 8: a publish the broker silently discards ----

// An ACL that denies the publish without disconnecting (a QoS 0 publish is simply lost): the node
// never sees it and never reports. The command ends UNANSWERED at the end of ITS window - not at its
// TTL - which frees the tenant's undelivered ceiling. Paired: with the permission granted the node
// reports and the same command is SUCCESSFUL.
func TestAPublishTheBrokerDiscardedEndsUnansweredAtTheWindowNotTheTTL(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.live("acme", "pump-1", "denied", CommandWrite, writeCmd("Setpoint", 5), map[string]string{
		cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandExpires: clk.now().Add(24 * time.Hour).Format(time.RFC3339)})
	r.live("acme", "pump-1", "granted", CommandWrite, writeCmd("Mode", "x"), nil)
	vm, err := host.RequestedWire(dtString, "x")
	require.NoError(t, err)

	r.report(metric(t, "Mode", vm, clk.now())) // only the permitted one is answered by the node
	assert.True(t, r.responseTo(t, "granted").env.Success)

	clk.advance(120 * time.Second)
	r.d.sweep()
	res := r.responseTo(t, "denied")
	assert.Equal(t, cmddownlink.ReachUnanswered, res.env.Reach)
	assert.Contains(t, errorOf(res), "the node may have ignored or rejected it, or the broker dropped the publish")
	assert.Equal(t, 1, r.result(ResultUnconfirmed))
	assert.Equal(t, 1, r.result(ResultConfirmed))
}

// 🔴 THE ENTRY IS IN THE MIRROR BEFORE THE BYTES ARE ON THE WIRE. A node that answers in a
// millisecond must find the entry it is answering: here the report lands after the record is
// written and before the publish returns. A mirror filled only after the publish would miss it, and
// the command would be left to time out although the node did exactly what it was asked.
func TestAReportThatArrivesBeforeThePublishReturnsStillConfirms(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	r.src.afterHook = func() { r.report(metric(t, "Setpoint", i8(t, 5), clk.now())) }
	r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	res := r.responseTo(t, "c")
	assert.True(t, res.env.Success, "a report that raced the publish was lost")
	assert.Equal(t, 1, r.result(ResultConfirmed))
}
