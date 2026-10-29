// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// ---- routing: gates 1-5 ----

// Another protocol's command is acknowledged and dropped at the reader, costing nothing; a
// Sparkplug one is queued. The pair is the control for each: the header alone decides.
func TestTheReaderDropsOtherTransportsAndQueuesSparkplug(t *testing.T) {
	r := newRig(t)
	shards := []*shard{newShard(), newShard()}
	queued := func() int { return len(shards[0].ch) + len(shards[1].ch) }

	w, spy := r.msg("acme", "pump-1", "c1", CommandWrite, writeCmd("Setpoint", 5), map[string]string{cmddownlink.HeaderTransport: "lwm2m"})
	require.True(t, r.d.route(context.Background(), shards, w.msg))
	assert.Equal(t, int32(1), spy.acks.Load(), "another transport's command was not acked off the stream")
	assert.Zero(t, queued(), "another transport's command was queued")
	assert.Equal(t, 1, r.result(ResultNotServed))

	w, spy = r.msg("acme", "pump-1", "c2", CommandWrite, writeCmd("Setpoint", 5), nil)
	require.True(t, r.d.route(context.Background(), shards, w.msg))
	assert.Zero(t, spy.acks.Load(), "a Sparkplug command was acked before it was worked")
	assert.Equal(t, 1, queued(), "a Sparkplug command was not queued")
}

// An ABSENT header means unknown, not "not mine": it is served only when a source of the tenant
// already has the device on its session.
func TestACommandWithNoTransportHeaderIsServedOnlyForADeviceWeHold(t *testing.T) {
	r := newRig(t)
	shards := []*shard{newShard(), newShard()}
	none := map[string]string{}

	w, spy := r.msg("acme", "stranger", "c1", CommandWrite, writeCmd("Setpoint", 5), none)
	require.True(t, r.d.route(context.Background(), shards, w.msg))
	assert.Equal(t, int32(1), spy.acks.Load(), "an unknown device with no header was queued")

	w, spy = r.msg("acme", "pump-1", "c2", CommandWrite, writeCmd("Setpoint", 5), none)
	require.True(t, r.d.route(context.Background(), shards, w.msg))
	assert.Zero(t, spy.acks.Load(), "a device we hold was dropped for lack of a header")
	assert.Equal(t, 1, len(shards[0].ch)+len(shards[1].ch))
}

func TestPoisonAndDeletedTenantsAreAckedAndNeverQueued(t *testing.T) {
	r := newRig(t)
	r.build(func(o *Options) { o.TenantDeleted = func(tn string) bool { return tn == "gone" } })
	shards := []*shard{newShard(), newShard()}

	spy := &ackSpy{log: r.log, token: "p"}
	bad := messagingMessage("nodots", []byte(`{}`), spy)
	require.True(t, r.d.route(context.Background(), shards, bad))
	assert.Equal(t, int32(1), spy.acks.Load())
	assert.Equal(t, 1, r.result(ResultPoison))

	spy2 := &ackSpy{log: r.log, token: "p2"}
	bad2 := messagingMessage("i.acme.device-commands.x", []byte(`{not json`), spy2)
	require.True(t, r.d.route(context.Background(), shards, bad2))
	assert.Equal(t, int32(1), spy2.acks.Load())
	assert.Equal(t, 2, r.result(ResultPoison))

	w, spy3 := r.msg("gone", "pump-1", "c3", CommandWrite, writeCmd("Setpoint", 5), nil)
	require.True(t, r.d.route(context.Background(), shards, w.msg))
	assert.Equal(t, int32(1), spy3.acks.Load(), "a deleted tenant's command was not acked")
	assert.Equal(t, 1, r.result(ResultTenantGone))
	assert.Zero(t, len(shards[0].ch)+len(shards[1].ch))

	// And the live tenant is unaffected.
	w, spy4 := r.msg("acme", "pump-1", "c4", CommandWrite, writeCmd("Setpoint", 5), nil)
	require.True(t, r.d.route(context.Background(), shards, w.msg))
	assert.Zero(t, spy4.acks.Load())
}

// Test 4. Each case is reachable and decided by exactly one fact.
func TestLiveRoutingByWhoHoldsTheDevice(t *testing.T) {
	t.Run("no source of the tenant has commands enabled: UNSENT, never ack-dropped", func(t *testing.T) {
		r := newRig(t)
		r.src.cfg.Enabled = false
		spy := r.live("acme", "pump-1", "c1", CommandWrite, writeCmd("Setpoint", 5), nil)

		res := r.responses.of("c1")
		require.Len(t, res, 1, "a Sparkplug command was dropped with its row left SENT")
		assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach)
		assert.Equal(t, "env-nonce", res[0].env.DispatchNonce, "an unclaimed refusal quotes the envelope's dispatch")
		assert.Equal(t, msgDisabledTenant, errorOf(res[0]))
		assert.Equal(t, int32(1), spy.acks.Load())
		assert.Equal(t, 1, r.result(ResultRefusedDisabled))
		assert.False(t, r.log.has("park:"), "a refused command was parked")
		assert.Zero(t, r.claimer.calls.Load(), "a refused command was claimed")
	})

	t.Run("the device is on a source with commands off: UNSENT", func(t *testing.T) {
		r := newRig(t)
		off := newSource(r.log, "acme")
		off.cfg.Enabled = false
		off.born("valve-9", devT)
		on := newSource(r.log, "acme") // an enabled source that does not hold valve-9
		r.d = New(nil, r.responses, r.fetcher, r.claimer, r.parker, r.store, []Source{off, on}, Metrics{Commands: r.commands}, Options{})

		r.live("acme", "valve-9", "c2", CommandWrite, writeCmd("Setpoint", 5), nil)
		res := r.responses.of("c2")
		require.Len(t, res, 1)
		assert.Equal(t, msgDisabledSource, errorOf(res[0]))
		assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach)
		assert.False(t, r.log.has("park:"), "a command to a disabled source was parked")
	})

	t.Run("the device is on no source: PARKED under the envelope's dispatch, claim untouched", func(t *testing.T) {
		r := newRig(t)
		spy := r.live("acme", "unborn", "c3", CommandWrite, writeCmd("Setpoint", 5), nil)

		assert.True(t, r.log.has("park:c3:env-nonce"), "the command was not parked, or not under the envelope's dispatch: %v", r.log.all())
		assert.Equal(t, int32(1), spy.acks.Load(), "a parked command must be acked: it is where it should be")
		assert.Empty(t, r.responses.all(), "a parked command was answered")
		assert.Zero(t, r.claimer.calls.Load(), "an unborn device's command was claimed before there was a table to check")
		assert.Equal(t, 1, r.result(ResultParked))
		assert.Empty(t, r.src.published())
	})

	t.Run("the device is on an enabled source: claimed, recorded, published, acked, in that order", func(t *testing.T) {
		r := newRig(t)
		spy := r.live("acme", "pump-1", "c4", CommandWrite, writeCmd("Setpoint", 5), nil)

		require.Len(t, r.src.published(), 1)
		got := r.log.all()
		claim, kv, pub, ack := r.log.index("claim:c4"), r.log.index("kvput:"), r.log.index("publish:"), r.log.index("ack:c4")
		require.True(t, claim >= 0 && kv >= 0 && pub >= 0 && ack >= 0, "%v", got)
		assert.Less(t, claim, kv, "the record was written before the command was claimed")
		assert.Less(t, kv, pub, "the command was published before its record was written")
		assert.Less(t, pub, ack, "the message was acked before the command was published")
		assert.Equal(t, int32(1), spy.acks.Load())
		assert.Empty(t, r.responses.all(), "a published command must not be answered: its outcome is learned from the node")
		assert.Equal(t, 1, r.result(ResultPublished))
		a, ok := r.store.get(AwaitingKey("acme", "c4"))
		require.True(t, ok)
		assert.Equal(t, "claim-nonce", a.Nonce, "the record must quote the claim's nonce, not the envelope's")
		assert.Equal(t, "Setpoint", a.Metric)
		assert.Equal(t, KindWrite, a.Kind)
		assert.JSONEq(t, `5`, string(a.Requested))
	})
}

// Cross-tenant control: tenant B's command carrying tenant A's device token reaches nothing of A's.
func TestACommandCannotReachAnotherTenantsSource(t *testing.T) {
	r := newRig(t)
	other := newSource(r.log, "globex") // globex has its own, enabled source and no pump-1
	r.d = New(nil, r.responses, r.fetcher, r.claimer, r.parker, r.store, []Source{r.src, other}, Metrics{Commands: r.commands}, Options{})

	r.live("globex", "pump-1", "cb", CommandWrite, writeCmd("Setpoint", 5), nil)
	assert.Empty(t, r.src.published(), "tenant B's command was published on tenant A's broker")
	assert.Empty(t, other.published())
	assert.True(t, r.log.has("park:cb:"), "tenant B's command for a device it does not hold must park, not publish")

	// Paired: tenant A publishes the same token on its own source.
	r.live("acme", "pump-1", "ca", CommandWrite, writeCmd("Setpoint", 5), nil)
	require.Len(t, r.src.published(), 1, "the control: tenant A's own command was not published")
}

// ---- gate order (test 5): every pair has both conditions true ----

func TestGateOrder(t *testing.T) {
	t.Run("not enabled + not born: refused_disabled, not parked", func(t *testing.T) {
		r := newRig(t)
		r.src.cfg.Enabled = false
		r.live("acme", "unborn", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
		assert.Equal(t, 1, r.result(ResultRefusedDisabled))
		assert.False(t, r.log.has("park:"))
		assert.Equal(t, "commands_not_enabled", reasonOf(r.responses.of("c")[0]))
	})

	t.Run("breaker open + invalid payload: refused_breaker", func(t *testing.T) {
		r := newRig(t)
		r.src.breaker = host.BreakerOpen
		r.live("acme", "pump-1", "c", CommandWrite, `{"junk":true}`, nil)
		assert.Equal(t, 1, r.result(ResultRefusedBreaker))
		assert.Zero(t, r.result(ResultRefusedPayload), "the payload was validated before the breaker was consulted")
		res := r.responses.of("c")
		require.Len(t, res, 1)
		assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach)
		assert.Equal(t, msgBreaker, errorOf(res[0]))
		assert.Equal(t, "broker_refused", reasonOf(res[0]))
	})

	t.Run("expired + invalid payload: refused_expired", func(t *testing.T) {
		r := newRig(t)
		past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		r.live("acme", "pump-1", "c", CommandWrite, `{"junk":true}`, map[string]string{
			cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandExpires: past})
		assert.Equal(t, 1, r.result(ResultRefusedExpired))
		assert.Zero(t, r.result(ResultRefusedPayload), "the payload was validated before expiry")
		assert.Equal(t, "expired", reasonOf(r.responses.of("c")[0]))
	})

	t.Run("not born + expired: PARKED (expiry is checked at the drain)", func(t *testing.T) {
		r := newRig(t)
		past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		r.live("acme", "unborn", "c", CommandWrite, writeCmd("Setpoint", 5), map[string]string{
			cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandExpires: past})
		assert.True(t, r.log.has("park:c:env-nonce"), "an unborn device's command was refused instead of parked: %v", r.log.all())
		assert.Zero(t, r.result(ResultRefusedExpired))

		// The drain after its birth refuses it as expired.
		r.fetcher.rows = []cmddownlink.DrainCommand{{Token: "c", Name: CommandWrite, Payload: []byte(writeCmd("Setpoint", 5)), ExpiresAt: time.Now().Add(-time.Minute)}}
		r.d.drain(context.Background(), deviceKey{"acme", "pump-1"})
		assert.Equal(t, 1, r.result(ResultRefusedExpired))
		res := r.responses.of("c")
		require.Len(t, res, 1)
		assert.Equal(t, "drain-nonce", res[0].env.DispatchNonce, "a drained refusal quotes the claim's nonce")
		assert.Empty(t, r.src.published())
	})

	t.Run("session down + unknown metric: PARKED, then refused_metric at the drain after the next birth", func(t *testing.T) {
		r := newRig(t)
		// A session that is down holds no index entries, so the device reads as not born.
		delete(r.src.index, "pump-1")
		r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Nope", 5), nil)
		assert.True(t, r.log.has("park:c:env-nonce"))
		assert.Zero(t, r.result(ResultRefusedMetric), "an unknown metric was refused with no table to check against")

		// The device is born again; its table does not declare "Nope".
		r.src.born("pump-1", devT)
		r.src.cfg.WritableMetrics = append(r.src.cfg.WritableMetrics, "Nope")
		r.fetcher.rows = []cmddownlink.DrainCommand{{Token: "c", Name: CommandWrite, Payload: []byte(writeCmd("Nope", 5))}}
		r.d.drain(context.Background(), deviceKey{"acme", "pump-1"})
		assert.Equal(t, 1, r.result(ResultRefusedMetric))
	})

	t.Run("a command waiting for its rate token that expires is refused, and was never claimed", func(t *testing.T) {
		r := newRig(t)
		r.src.cfg.PublishPerSecond, r.src.cfg.Burst = 5, 1
		r.build(nil)
		// Spend the burst.
		r.live("acme", "pump-1", "first", CommandWrite, writeCmd("Setpoint", 1), nil)
		require.Equal(t, 1, r.log.count("claim:first"))
		before := r.claimer.calls.Load()

		soon := time.Now().Add(60 * time.Millisecond).UTC().Format(time.RFC3339Nano)
		spy := r.live("acme", "pump-1", "slow", CommandWrite, writeCmd("Setpoint", 2), map[string]string{
			cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandExpires: soon})

		assert.Equal(t, 1, r.result(ResultRefusedExpired))
		assert.Equal(t, before, r.claimer.calls.Load(), "a command that expired while it waited for its turn was claimed")
		assert.Equal(t, int32(1), spy.acks.Load())
		res := r.responses.of("slow")
		require.Len(t, res, 1)
		assert.Equal(t, "env-nonce", res[0].env.DispatchNonce)
		assert.Equal(t, "expired", reasonOf(res[0]))
		assert.Len(t, r.src.published(), 1, "only the first command should have been published")
	})
}

// Test 6: the rate is charged BEFORE the claim, and a waiting message is reported as in progress.
func TestTheRateIsChargedBeforeTheClaimAndAWaitingMessageStaysInProgress(t *testing.T) {
	r := newRig(t)
	r.src.cfg.PublishPerSecond, r.src.cfg.Burst = 10, 1 // one token per 100 ms
	r.build(nil)

	r.live("acme", "pump-1", "first", CommandWrite, writeCmd("Setpoint", 1), nil)
	spy := r.live("acme", "pump-1", "second", CommandWrite, writeCmd("Setpoint", 2), nil)

	claim := r.log.index("claim:second")
	progress := r.log.index("inprogress:second")
	require.True(t, claim >= 0, "the second command was never claimed: %v", r.log.all())
	require.True(t, progress >= 0, "a command waiting for its turn was never reported as in progress, so its ack wait would redeliver it")
	assert.Less(t, progress, claim, "the claim was taken before the wait was over")
	assert.GreaterOrEqual(t, spy.inProgress.Load(), int32(2), "a ~100 ms wait at a 10 ms progress interval should report several times")
	assert.Equal(t, int32(1), spy.acks.Load())
	assert.Len(t, r.src.published(), 2)

	// Control: the first command, which found a token, was never reported as waiting.
	assert.False(t, r.log.has("inprogress:first"), "a command that did not wait was reported as in progress")
}

// A term that ends during the wait leaves the message unacked and the row unclaimed.
func TestATermEndingDuringTheRateWaitClaimsNothing(t *testing.T) {
	r := newRig(t)
	r.src.cfg.PublishPerSecond, r.src.cfg.Burst = 1, 1 // the second waits a second
	r.build(nil)
	r.live("acme", "pump-1", "first", CommandWrite, writeCmd("Setpoint", 1), nil)
	before := r.claimer.calls.Load()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(40*time.Millisecond, cancel)
	w, spy := r.msg("acme", "pump-1", "second", CommandWrite, writeCmd("Setpoint", 2), nil)
	r.d.live(ctx, w)

	assert.Equal(t, before, r.claimer.calls.Load(), "a command was claimed by a term that had ended")
	assert.Zero(t, spy.acks.Load(), "an abandoned command was acked, so no later term would ever see it")
	assert.Len(t, r.src.published(), 1)
	assert.Empty(t, r.responses.of("second"))
}

// ---- gate 8: validation ----

func TestValidationRefusesWithAReasonAndPublishesNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		payload string
		result  string
		reason  string
		code    string
	}{
		{"unknown key", "sparkplug.nope", ``, ResultRefusedPayload, "unknown Sparkplug command", "unknown_command"},
		{"write with no parameters", CommandWrite, ``, ResultRefusedPayload, "needs", "invalid_parameters"},
		{"write with a misspelled field", CommandWrite, `{"metrics":"Setpoint","value":1}`, ResultRefusedPayload, "needs", "invalid_parameters"},
		{"write with no value", CommandWrite, `{"metric":"Setpoint"}`, ResultRefusedPayload, "needs", "invalid_parameters"},
		{"write with a null value", CommandWrite, `{"metric":"Setpoint","value":null}`, ResultRefusedPayload, "must be a boolean, a number or a string", "invalid_parameters"},
		{"write with an object value", CommandWrite, `{"metric":"Setpoint","value":{"a":1}}`, ResultRefusedPayload, "cannot take", "value_not_accepted"},
		{"write off the allowlist", CommandWrite, writeCmd("Hidden", 1), ResultRefusedNotAllowed, "allowlist", "metric_not_writable"},
		{"write to a reserved metric", CommandWrite, writeCmd("Node Control/Reboot", true), ResultRefusedNotAllowed, "protocol control metric", "metric_protocol_control"},
		{"write to bdSeq", CommandWrite, writeCmd("bdSeq", 1), ResultRefusedNotAllowed, "protocol control metric", "metric_protocol_control"},
		{"write to a metric the BIRTH did not declare", CommandWrite, writeCmd("Outputs/Missing", true), ResultRefusedMetric, "not declared", "metric_not_declared"},
		{"write out of range", CommandWrite, writeCmd("Setpoint", 300), ResultRefusedPayload, "datatype 1", "value_not_accepted"},
		{"write a string into an integer", CommandWrite, writeCmd("Setpoint", "high"), ResultRefusedPayload, "cannot take", "value_not_accepted"},
		{"write a number into a string", CommandWrite, writeCmd("Mode", 3), ResultRefusedPayload, "cannot take", "value_not_accepted"},
		{"write a datatype commands do not carry", CommandWrite, writeCmd("Blob", "x"), ResultRefusedDatatype, "do not carry", "datatype_unsupported"},
		{"rebirth with parameters", CommandRebirth, `{"force":true}`, ResultRefusedPayload, "takes no parameters", "invalid_parameters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			spy := r.live("acme", "pump-1", "c", tc.command, tc.payload, nil)
			assert.Equal(t, 1, r.result(tc.result), "events: %v", r.log.all())
			assert.Empty(t, r.src.published(), "a refused command was published")
			assert.Zero(t, r.claimer.calls.Load(), "a refused command was claimed")
			res := r.responses.of("c")
			require.Len(t, res, 1)
			assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach)
			assert.Contains(t, errorOf(res[0]), tc.reason)
			assert.Equal(t, tc.code, reasonOf(res[0]), "the refusal's machine-readable reason")
			assert.Equal(t, int32(1), spy.acks.Load())
			assert.Zero(t, r.store.len(), "a refused command left a record")
		})
	}
}

// The control for the table above: the same shapes, valid, are published.
func TestValidCommandsOfEveryKindArePublished(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		payload string
		impact  string
		device  string
		kind    string
		target  host.CommandTarget
	}{
		{"write an integer", CommandWrite, writeCmd("Setpoint", 100), "", "pump-1", KindWrite, devT},
		{"write a string", CommandWrite, writeCmd("Mode", "manual"), "", "pump-1", KindWrite, devT},
		{"write under a pattern", CommandWrite, writeCmd("Outputs/Valve", true), "", "pump-1", KindWrite, devT},
		{"rebirth a device resolves to its node", CommandRebirth, ``, "", "pump-1", KindRebirth, nodeT},
		{"rebirth a node", CommandRebirth, `{}`, "", "node-3", KindRebirth, nodeT},
		{"reboot a node", CommandReboot, ``, "DISRUPTIVE", "node-3", KindReboot, nodeT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			h := map[string]string{cmddownlink.HeaderTransport: "sparkplug"}
			if tc.impact != "" {
				h[cmddownlink.HeaderCommandImpact] = tc.impact
			}
			r.live("acme", tc.device, "c", tc.command, tc.payload, h)
			pubs := r.src.published()
			require.Len(t, pubs, 1, "events: %v", r.log.all())
			assert.Equal(t, tc.kind, pubs[0].kind)
			assert.Equal(t, tc.target, pubs[0].target, "the target's identity, not the command key, decides NCMD vs DCMD")
			a, ok := r.store.get(AwaitingKey("acme", "c"))
			require.True(t, ok)
			assert.Equal(t, tc.kind, a.Kind)
		})
	}
}

// ---- reboot (test 10) ----

func TestRebootNeedsAllFourGates(t *testing.T) {
	good := map[string]string{cmddownlink.HeaderTransport: "sparkplug", cmddownlink.HeaderCommandImpact: "DISRUPTIVE"}
	for _, tc := range []struct {
		name   string
		break_ func(r *rig, h map[string]string) string // returns the device to command
		reason string
		code   string
	}{
		{"no impact header", func(_ *rig, h map[string]string) string { delete(h, cmddownlink.HeaderCommandImpact); return "node-3" }, "impact is unknown", "reboot_impact"},
		{"a NORMAL impact", func(_ *rig, h map[string]string) string {
			h[cmddownlink.HeaderCommandImpact] = "NORMAL"
			return "node-3"
		}, "impact is NORMAL", "reboot_impact"},
		{"allowReboot off", func(r *rig, _ map[string]string) string { r.src.cfg.AllowReboot = false; return "node-3" }, "allowReboot", "reboot_not_allowed"},
		{"the NBIRTH did not declare Reboot", func(r *rig, _ map[string]string) string {
			delete(r.src.metrics, nodeT.String()+"|"+nodeControlReboot)
			return "node-3"
		}, "did not declare", "reboot_not_declared"},
		{"addressed to a device", func(_ *rig, _ map[string]string) string { return "pump-1" }, "edge node", "reboot_not_a_node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			h := map[string]string{}
			for k, v := range good {
				h[k] = v
			}
			device := tc.break_(r, h)
			r.live("acme", device, "c", CommandReboot, ``, h)
			assert.Equal(t, 1, r.result(ResultRefusedRebootGate), "events: %v", r.log.all())
			assert.Empty(t, r.src.published(), "a reboot with a failed gate was published")
			res := r.responses.of("c")
			require.Len(t, res, 1)
			assert.Contains(t, errorOf(res[0]), tc.reason)
			assert.Equal(t, tc.code, reasonOf(res[0]))
		})
	}

	t.Run("all four true: published", func(t *testing.T) {
		r := newRig(t)
		r.live("acme", "node-3", "c", CommandReboot, ``, good)
		require.Len(t, r.src.published(), 1)
		assert.Equal(t, true, r.src.published()[0].value)
		assert.Zero(t, r.result(ResultRefusedRebootGate))
	})

	t.Run("a second reboot inside the window is refused, and one after it is not", func(t *testing.T) {
		r := newRig(t)
		now := time.Now()
		r.build(func(o *Options) { o.Now = func() time.Time { return now } })
		r.live("acme", "node-3", "r1", CommandReboot, ``, good)
		require.Len(t, r.src.published(), 1)

		now = now.Add(5 * time.Minute)
		r.live("acme", "node-3", "r2", CommandReboot, ``, good)
		assert.Len(t, r.src.published(), 1, "a second reboot of the node went out inside the reboot window")
		assert.Equal(t, 1, r.result(ResultRefusedRebootGate))

		now = now.Add(6 * time.Minute) // past the 600 s window since r1
		r.live("acme", "node-3", "r3", CommandReboot, ``, good)
		assert.Len(t, r.src.published(), 2, "a reboot after the window was still refused")
	})
}

// ---- gates 10-15 ----

func TestAFailedClaimPublishesNothing(t *testing.T) {
	t.Run("lost: acked, nothing sent, the device's backlog is looked at", func(t *testing.T) {
		r := newRig(t)
		r.claimer.liveWon = false
		spy := r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
		assert.Empty(t, r.src.published())
		assert.Equal(t, int32(1), spy.acks.Load(), "a stale envelope must be acked")
		assert.Equal(t, 1, r.result(ResultClaimLost))
		assert.Zero(t, r.store.len())
		assert.Empty(t, r.responses.all(), "a lost claim answered a command that is somebody else's now")
	})
	t.Run("an error fails closed: unacked, nothing sent", func(t *testing.T) {
		r := newRig(t)
		r.claimer.liveErr = errBoom
		spy := r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
		assert.Empty(t, r.src.published())
		assert.Zero(t, spy.acks.Load(), "a command whose claim errored was acked, so it would never be retried")
		assert.Equal(t, 1, r.result(ResultClaimFailed))
	})
}

func TestARecordThatCannotBeWrittenMeansNothingIsPublished(t *testing.T) {
	r := newRig(t)
	r.store.putErr = errBoom
	spy := r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)

	assert.Empty(t, r.src.published(), "a command went out with no record of it")
	assert.Equal(t, 1, r.result(ResultKVFailed))
	assert.True(t, r.log.has("park:c:claim-nonce"), "the claimed command was not handed back: %v", r.log.all())
	assert.Equal(t, int32(1), spy.acks.Load())
	assert.Empty(t, r.responses.all(), "the command was answered although it can still be delivered")
}

// Lost lease: the term ends between the claim and the publish. Nothing is published, the row is
// handed back, and the message is left unacked for the next leader.
func TestATermEndingBetweenTheClaimAndThePublishPublishesNothing(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.src.afterHook = cancel // the term ends while the record is being written
	w, spy := r.msg("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	r.d.live(ctx, w)

	assert.Empty(t, r.src.published(), "a command was published by a term that had ended")
	assert.Zero(t, spy.acks.Load(), "the message was acked, so the next leader would never see it")
	assert.True(t, r.log.has("park:c:claim-nonce"), "the claimed row was not handed back: %v", r.log.all())
	assert.True(t, r.log.has("kvdel:"), "the record of a command that was not sent was left behind")
	assert.Zero(t, r.store.len())
}

func TestASessionThatDropsAfterTheRecordParksTheCommand(t *testing.T) {
	r := newRig(t)
	r.src.afterHook = func() { r.src.mu.Lock(); r.src.up = false; r.src.mu.Unlock() }
	spy := r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)

	assert.Empty(t, r.src.published())
	assert.True(t, r.log.has("park:c:claim-nonce"))
	assert.Zero(t, r.store.len(), "the record of a command that was not sent was left behind")
	assert.Equal(t, int32(1), spy.acks.Load())
	assert.Empty(t, r.responses.all())
}

func TestABreakerThatOpensBeforeThePublishRefusesAndDropsTheRecord(t *testing.T) {
	r := newRig(t)
	// Closed when gate 6 looks, open by the time the publish asks (the half-open probe was taken).
	r.src.afterHook = func() {}
	r.src.breaker = host.BreakerHalfOpen
	r.live("acme", "pump-1", "ok", CommandWrite, writeCmd("Setpoint", 1), nil)
	require.Len(t, r.src.published(), 1, "half-open must let a command reach the publish (the Client admits exactly one)")

	r.src.mu.Lock()
	r.src.breaker = host.BreakerClosed
	r.src.afterHook = func() { r.src.mu.Lock(); r.src.breaker = host.BreakerOpen; r.src.mu.Unlock() }
	r.src.mu.Unlock()
	// The fake reads the breaker before the hook, so model "open at publish" by failing the publish
	// the way the Client does.
	r.src.publishErr = host.ErrBreakerOpen
	r.live("acme", "pump-1", "late", CommandWrite, writeCmd("Setpoint", 2), nil)
	assert.Len(t, r.src.published(), 1)
	res := r.responses.of("late")
	require.Len(t, res, 1)
	assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach)
	assert.Equal(t, msgBreaker, errorOf(res[0]))
	assert.Equal(t, "broker_refused", reasonOf(res[0]))
	_, ok := r.store.get(AwaitingKey("acme", "late"))
	assert.False(t, ok, "a refused command left its record")
}

func TestAPublishThatFailsIsRefusedAsNotDelivered(t *testing.T) {
	r := newRig(t)
	r.src.publishErr = errBoom
	spy := r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	res := r.responses.of("c")
	require.Len(t, res, 1)
	assert.Equal(t, cmddownlink.ReachUnsent, res[0].env.Reach, "a command that never reached the wire is NOT_DELIVERED")
	assert.Equal(t, "claim-nonce", res[0].env.DispatchNonce)
	assert.Equal(t, 1, r.result(ResultPublishFailed))
	assert.Equal(t, "publish_failed", reasonOf(res[0]))
	assert.Zero(t, r.store.len())
	assert.Equal(t, int32(1), spy.acks.Load())
}

// A table that moved between validation and the wire is refused with the reason.
func TestAMetricThatChangedBetweenValidationAndThePublishIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		result string
		code   string
	}{
		"gone":          {host.ErrMetricUnknown, ResultRefusedMetric, "metric_not_declared"},
		"retyped":       {host.ErrDatatypeChanged, ResultRefusedDatatype, "datatype_changed"},
		"value refused": {host.ErrValueRefused, ResultRefusedPayload, "value_not_accepted"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.src.publishErr = tc.err
			r.live("acme", "pump-1", "c", CommandWrite, writeCmd("Setpoint", 5), nil)
			assert.Equal(t, 1, r.result(tc.result))
			assert.Equal(t, tc.code, reasonOf(r.responses.of("c")[0]))
			assert.Empty(t, r.src.published())
			assert.Zero(t, r.store.len())
		})
	}
}

// ---- supersede (gate 11) ----

func TestANewerWriteSupersedesTheOneStillAwaitingForTheSameMetric(t *testing.T) {
	r := newRig(t)
	r.live("acme", "pump-1", "old", CommandWrite, writeCmd("Setpoint", 1), nil)
	require.Len(t, r.src.published(), 1)
	_, ok := r.store.get(AwaitingKey("acme", "old"))
	require.True(t, ok)

	r.live("acme", "pump-1", "new", CommandWrite, writeCmd("Setpoint", 2), nil)
	require.Len(t, r.src.published(), 2)

	res := r.responses.of("old")
	require.Len(t, res, 1, "the superseded command was not resolved")
	assert.Equal(t, cmddownlink.ReachUnanswered, res[0].env.Reach)
	assert.Equal(t, "claim-nonce", res[0].env.DispatchNonce, "it must be resolved with its OWN stored nonce")
	assert.Contains(t, errorOf(res[0]), "superseded by new before it was confirmed")
	require.NotNil(t, res[0].env.Payload)
	assert.JSONEq(t, `{"reason":"superseded"}`, *res[0].env.Payload)
	_, ok = r.store.get(AwaitingKey("acme", "old"))
	assert.False(t, ok, "the superseded record was not deleted")
	_, ok = r.store.get(AwaitingKey("acme", "new"))
	assert.True(t, ok)
	assert.Equal(t, 1, r.result(ResultSuperseded))
}

// The control: a write to a DIFFERENT metric, or another device, supersedes nothing.
func TestAWriteToAnotherMetricSupersedesNothing(t *testing.T) {
	r := newRig(t)
	r.live("acme", "pump-1", "a", CommandWrite, writeCmd("Setpoint", 1), nil)
	r.live("acme", "pump-1", "b", CommandWrite, writeCmd("Mode", "x"), nil)
	assert.Zero(t, r.result(ResultSuperseded))
	assert.Empty(t, r.responses.all())
	assert.Equal(t, 2, r.store.len())
}

// ---- the breaker (test 8b, the dispatcher's half) ----

func TestABrokerThatDropsTheSessionOnARefusedPublishPausesCommandsAndResolvesTheOne(t *testing.T) {
	r := newRig(t)
	r.live("acme", "pump-1", "first", CommandWrite, writeCmd("Setpoint", 1), nil)
	require.Len(t, r.src.published(), 1)

	// The broker drops the session right after the publish: the Client reports the trip.
	r.src.hooks.Tripped(time.Now())
	require.Eventually(t, func() bool { return len(r.responses.of("first")) == 1 }, 2*time.Second, 5*time.Millisecond,
		"the command published just before the disconnect was not resolved")
	res := r.responses.of("first")[0]
	assert.Equal(t, cmddownlink.ReachUnanswered, res.env.Reach, "a refused publish is TIMEOUT/UNKNOWN, not FAILED: it may have been delivered")
	assert.Equal(t, msgTripped, errorOf(res))
	require.NotNil(t, res.env.Payload)
	assert.JSONEq(t, `{"reason":"session_dropped"}`, *res.env.Payload)
	assert.Equal(t, "claim-nonce", res.env.DispatchNonce)
	require.Eventually(t, func() bool { return r.store.len() == 0 }, 2*time.Second, 5*time.Millisecond, "its record was not deleted")
	assert.Equal(t, 1, r.result(ResultUnconfirmed))

	// The breaker is open: ten queued commands fail fast and nothing more is published (so nothing
	// can drop the session again).
	r.src.mu.Lock()
	r.src.breaker = host.BreakerOpen
	r.src.mu.Unlock()
	for i := 0; i < 10; i++ {
		r.live("acme", "pump-1", "q"+string(rune('a'+i)), CommandWrite, writeCmd("Setpoint", 2), nil)
	}
	assert.Equal(t, 10, r.result(ResultRefusedBreaker))
	assert.Len(t, r.src.published(), 1, "commands were published while the breaker was open")
	assert.Equal(t, 1, r.log.count("claim:"), "queued commands were claimed while the breaker was open")

	// Paired: half-open lets the next one through, and closed lets all through.
	r.src.mu.Lock()
	r.src.breaker = host.BreakerHalfOpen
	r.src.mu.Unlock()
	r.live("acme", "pump-1", "probe", CommandWrite, writeCmd("Setpoint", 3), nil)
	assert.Len(t, r.src.published(), 2, "the half-open probe was not let through")
}

// A trip resolves only what was published inside the watch before the disconnect.
func TestATripResolvesOnlyTheCommandsPublishedInsideTheWatch(t *testing.T) {
	r := newRig(t)
	r.live("acme", "pump-1", "recent", CommandWrite, writeCmd("Setpoint", 1), nil)
	r.live("acme", "pump-1", "other", CommandWrite, writeCmd("Mode", "x"), nil)

	// A disconnect long after both: neither is blamed.
	r.src.hooks.Tripped(time.Now().Add(time.Minute))
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, r.responses.all(), "a command published a minute before the disconnect was blamed for it")
	assert.Equal(t, 2, r.store.len())
}

// ---- the wake drain ----

func TestTheDrainClaimsThenPublishesBacklogInOrderAndSettlesNoneOfThem(t *testing.T) {
	r := newRig(t)
	r.fetcher.rows = []cmddownlink.DrainCommand{
		{Token: "d1", Name: CommandWrite, Payload: []byte(writeCmd("Setpoint", 1))},
		{Token: "d2", Name: CommandWrite, Payload: []byte(writeCmd("Mode", "x"))},
	}
	r.d.drain(context.Background(), deviceKey{"acme", "pump-1"})

	require.Len(t, r.src.published(), 2)
	assert.Less(t, r.log.index("drainclaim:d1"), r.log.index("publish:"), "the first row was published before it was claimed")
	assert.Less(t, r.log.index("publish:"), r.log.index("drainclaim:d2"), "rows must be handled one at a time, in order")
	a, ok := r.store.get(AwaitingKey("acme", "d1"))
	require.True(t, ok)
	assert.Equal(t, "drain-nonce", a.Nonce)
	assert.Equal(t, 2, r.result(ResultDrained))
	assert.Empty(t, r.responses.all())
}

func TestTheDrainDoesNothingForADeviceThatIsNotBornOnAnEnabledSource(t *testing.T) {
	r := newRig(t)
	r.fetcher.rows = []cmddownlink.DrainCommand{{Token: "d1", Name: CommandWrite, Payload: []byte(writeCmd("Setpoint", 1))}}
	r.d.drain(context.Background(), deviceKey{"acme", "unborn"})
	assert.False(t, r.log.has("fetch:"), "a drain read the backlog of a device nobody holds")
	assert.Equal(t, 0, r.log.count("drainclaim:"))

	r.src.cfg.Enabled = false
	r.d.drain(context.Background(), deviceKey{"acme", "pump-1"})
	assert.False(t, r.log.has("fetch:"), "a drain read the backlog of a disabled source")
}

func TestTheDrainStopsAtTheFirstClaimFault(t *testing.T) {
	r := newRig(t)
	r.claimer.drainErr = errBoom
	r.fetcher.rows = []cmddownlink.DrainCommand{
		{Token: "d1", Name: CommandWrite, Payload: []byte(writeCmd("Setpoint", 1))},
		{Token: "d2", Name: CommandWrite, Payload: []byte(writeCmd("Setpoint", 2))},
	}
	r.d.drain(context.Background(), deviceKey{"acme", "pump-1"})
	assert.Equal(t, 1, r.log.count("drainclaim:"), "a newer row was claimed past an older one that could not be")
	assert.Empty(t, r.src.published())
	assert.Equal(t, 1, r.result(ResultClaimFailed))
}

func TestTheDrainHandsBackARowWhoseTableIsNotThereYetAndWakesOnTheNextBirth(t *testing.T) {
	r := newRig(t)
	r.src.notBorn = true
	r.fetcher.rows = []cmddownlink.DrainCommand{{Token: "d1", Name: CommandWrite, Payload: []byte(writeCmd("Setpoint", 1))}}
	r.d.drain(context.Background(), deviceKey{"acme", "pump-1"})
	assert.True(t, r.log.has("park:d1:drain-nonce"), "the row was not handed back under the claim's nonce: %v", r.log.all())
	assert.Empty(t, r.src.published())
	assert.Empty(t, r.responses.all(), "a row that can still be delivered was answered")

	// The device is already in the index, so its next birth is not "new": the park remembers it.
	shards := []*shard{newShard(), newShard()}
	r.d.shardsPtr.Store(&shards)
	r.d.OnBorn("acme", "pump-1", false)
	woken := shards[0].takeWakes()
	woken = append(woken, shards[1].takeWakes()...)
	assert.Equal(t, []deviceKey{{"acme", "pump-1"}}, woken, "a birth did not wake the drain of a device a command was parked for")

	// Control: with nothing parked, an already-known token does not wake anything.
	r.d.OnBorn("acme", "pump-1", false)
	assert.Empty(t, append(shards[0].takeWakes(), shards[1].takeWakes()...), "a known token with nothing parked woke a drain")
	// And a new token always does.
	r.d.OnBorn("acme", "pump-9", true)
	assert.Len(t, append(shards[0].takeWakes(), shards[1].takeWakes()...), 1)
}

func TestAWakeOnAStandbyIsANoOp(t *testing.T) {
	r := newRig(t)
	r.d.Drain("acme", "pump-1") // no term running: no shards
	r.d.OnBorn("acme", "pump-1", true)
	assert.False(t, r.log.has("fetch:"))
}

// ---- the term ----

func messagingMessage(subject string, value []byte, ack messaging.Acknowledger) messaging.Message {
	return messaging.NewConsumedMessage(subject, value, 1, map[string]string{cmddownlink.HeaderTransport: "sparkplug"}, ack)
}

// chanReader is a Reader fed from a channel.
type chanReader struct{ msgs chan messaging.Message }

func (c *chanReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	select {
	case <-ctx.Done():
		return messaging.Message{}, context.Canceled
	case m := <-c.msgs:
		return m, nil
	}
}
func (c *chanReader) HandleResponse(error) {}

// The whole term: Run starts the workers and reports ready, the reader's messages are routed to a
// worker and published, a birth wakes a drain through the same workers, and cancelling the term
// ends Run with nothing left running.
func TestRunServesATermEndToEnd(t *testing.T) {
	r := newRig(t)
	reader := &chanReader{msgs: make(chan messaging.Message, 4)}
	r.d = New(reader, r.responses, r.fetcher, r.claimer, r.parker, r.store, []Source{r.src},
		Metrics{Commands: r.commands, Awaiting: r.awaiting}, Options{Workers: 2, ReadPacer: core.NewReadPacer(nil, "device commands")})

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); r.d.Run(ctx, func() { close(ready) }) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("Run never reported ready")
	}

	w, spy := r.msg("acme", "pump-1", "live", CommandWrite, writeCmd("Setpoint", 5), nil)
	reader.msgs <- w.msg
	require.Eventually(t, func() bool { return spy.acks.Load() == 1 }, 5*time.Second, 5*time.Millisecond, "the routed command was never worked: %v", r.log.all())
	assert.Len(t, r.src.published(), 1)

	r.fetcher.rows = []cmddownlink.DrainCommand{{Token: "woken", Name: CommandWrite, Payload: []byte(writeCmd("Mode", "x"))}}
	r.d.OnBorn("acme", "pump-1", true)
	require.Eventually(t, func() bool { return len(r.src.published()) == 2 }, 5*time.Second, 5*time.Millisecond,
		"a birth did not wake the drain: %v", r.log.all())

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its term ended")
	}
	r.d.Drain("acme", "pump-1") // after the term: a no-op, not a panic or a send on a closed channel
}

func TestNewRefusesToBeBuiltWithoutWhatItNeedsToClaimAndSettle(t *testing.T) {
	r := newRig(t)
	src := []Source{r.src}
	for name, build := range map[string]func(){
		"no claimer": func() { New(nil, r.responses, r.fetcher, nil, r.parker, r.store, src, Metrics{}, Options{}) },
		"no parker":  func() { New(nil, r.responses, r.fetcher, r.claimer, nil, r.store, src, Metrics{}, Options{}) },
		"no store":   func() { New(nil, r.responses, r.fetcher, r.claimer, r.parker, nil, src, Metrics{}, Options{}) },
		"no fetcher": func() { New(nil, r.responses, nil, r.claimer, r.parker, r.store, src, Metrics{}, Options{}) },
		"no pacer": func() {
			New(&chanReader{}, r.responses, r.fetcher, r.claimer, r.parker, r.store, src, Metrics{}, Options{})
		},
	} {
		assert.Panicsf(t, build, "%s: a dispatcher missing it was built", name)
	}
	assert.NotPanics(t, func() { New(nil, r.responses, r.fetcher, r.claimer, r.parker, r.store, src, Metrics{}, Options{}) })
}

func TestTheBucketQueuesCallersBehindEachOther(t *testing.T) {
	now := time.Unix(1000, 0)
	b := newBucket(10, 2, now)
	assert.Zero(t, b.reserve(now))
	assert.Zero(t, b.reserve(now))
	assert.Equal(t, 100*time.Millisecond, b.reserve(now), "the first caller over the burst waits for one token")
	assert.Equal(t, 200*time.Millisecond, b.reserve(now), "the second waits behind the first, not alongside it")
	assert.Zero(t, b.reserve(now.Add(10*time.Second)), "a long idle refills to the burst and no more")
	assert.Zero(t, b.reserve(now.Add(10*time.Second)))
	assert.NotZero(t, b.reserve(now.Add(10*time.Second)), "the burst is a ceiling")
}
