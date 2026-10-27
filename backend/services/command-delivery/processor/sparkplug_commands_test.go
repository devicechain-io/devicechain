// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
)

// impactSpy is a model.CommandImpactResolver that records how it was asked. It answers every
// device it is given with impact, unless err is set or answer is.
type impactSpy struct {
	mu     sync.Mutex
	calls  int
	asked  [][]string
	impact string
	err    error
	// answer, when set, decides the reply from the tenant in ctx and the command key.
	answer func(tenant, key string) string
	// block makes the call wait for its context to end, like a device-management that hangs.
	block bool
}

func (s *impactSpy) ImpactsFor(ctx context.Context, devices []string, key string) (map[string]string, error) {
	s.mu.Lock()
	s.calls++
	s.asked = append(s.asked, append([]string(nil), devices...))
	block, err, impact, answer := s.block, s.err, s.impact, s.answer
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if answer != nil {
		tenant, _ := core.TenantFromContext(ctx)
		impact = answer(tenant, key)
	}
	out := map[string]string{}
	for _, d := range devices {
		out[d] = impact
	}
	return out, nil
}

func (s *impactSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// mixedFleet is one command to each kind of device the gate can meet: a plain MQTT device, an
// LwM2M device, a Sparkplug device that is present, and a device the projection has no row for.
func mixedFleet() (*fakeApi, *scriptedReader) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{
		queued(1, "c1"), queued(2, "c2"), queued(3, "c3"), queued(4, "c4"),
	}}
	reader := &scriptedReader{states: map[string]presence.State{
		"dev-c1": asserted(true, mqttSource),
		"dev-c2": asserted(true, lwm2mSource),
		"dev-c3": asserted(true, sparkplugSource),
	}}
	return api, reader
}

// headersByDevice indexes what the writer was handed.
func headersByDevice(w *recordingWriter) map[string]map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]map[string]string{}
	for i, device := range w.devices {
		out[device] = w.messages[i].Headers
	}
	return out
}

// TestNothingEnabledLeavesDispatchExactlyAsItWas is THE guard of this slice: with the Sparkplug
// capability flag off, the same sweep over a mixed fleet publishes exactly what it published
// before the feature existed. Three things are pinned together, because each is a separate way to
// change behaviour while "adding a capability": the Sparkplug command still fails on sight, no
// message carries a header (a wire change on every command of every transport), and
// device-management is never asked for an impact.
//
// TestEnablingSparkplugCommandsChangesExactlyTheSparkplugPath is its NEGATIVE CONTROL: the same
// fixture with the flag on must differ in every one of those respects. A guard that passes
// whatever the flag says would pass here too.
func TestNothingEnabledLeavesDispatchExactlyAsItWas(t *testing.T) {
	api, reader := mixedFleet()
	writer := &recordingWriter{}
	spy := &impactSpy{impact: "DISRUPTIVE"}
	proc := procWith(api, writer)
	proc.Presence = reader
	proc.CommandImpacts = spy

	proc.sweepLocked(context.Background())

	assert.Equal(t, []uint{3}, api.undeliverable, "the Sparkplug command must still fail on sight")
	require.Equal(t, 3, writer.count(), "MQTT, LwM2M and the unknown device are still published")
	for device, headers := range headersByDevice(writer) {
		assert.Nil(t, headers, "device %s: a message carries a header with nothing enabled", device)
	}
	assert.Zero(t, spy.count(), "no impact is resolved when the capability is off")
}

func TestEnablingSparkplugCommandsChangesExactlyTheSparkplugPath(t *testing.T) {
	api, reader := mixedFleet()
	writer := &recordingWriter{}
	spy := &impactSpy{impact: "DISRUPTIVE"}
	proc := procWith(api, writer)
	proc.Presence = reader
	proc.CommandImpacts = spy
	proc.SparkplugCommands = true

	proc.sweepLocked(context.Background())

	assert.Empty(t, api.undeliverable, "with the capability on a Sparkplug command is dispatched, not failed")
	require.Equal(t, 4, writer.count())
	headers := headersByDevice(writer)
	assert.Nil(t, headers["dev-c1"], "a plain MQTT device projects the operator's own source id, which is never stamped")
	assert.Equal(t, map[string]string{cmddownlink.HeaderTransport: "lwm2m"}, headers["dev-c2"])
	assert.Equal(t, map[string]string{
		cmddownlink.HeaderTransport:     "sparkplug",
		cmddownlink.HeaderCommandImpact: "DISRUPTIVE",
	}, headers["dev-c3"])
	assert.Nil(t, headers["dev-c4"], "a device the projection knows nothing about has no transport to stamp")
	assert.Equal(t, 1, spy.count(), "only the Sparkplug device's impact is resolved")
}

// TestADispatchedSparkplugCommandIsStillAClaimedEnvelope proves the stamp rides the REAL envelope
// path rather than replacing it: the message decodes into the shared wire type with the claim's
// nonce, which is what the adapter must quote.
func TestADispatchedSparkplugCommandIsStillAClaimedEnvelope(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(3, "c3")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c3": asserted(true, sparkplugSource)}}
	proc.CommandImpacts = &impactSpy{impact: "NORMAL"}
	proc.SparkplugCommands = true

	proc.sweepLocked(context.Background())

	require.Equal(t, 1, writer.count())
	var env cmddownlink.DeliveryEnvelope
	require.NoError(t, json.Unmarshal(writer.messages[0].Value, &env))
	assert.Equal(t, "c3", env.Token)
	assert.Equal(t, "dev-c3", env.DeviceToken)
	assert.NotEmpty(t, env.DispatchNonce, "the adapter confirms the dispatch with this nonce")
	require.Len(t, api.markedSent, 1, "the command is claimed before it is published")
}

// TestASparkplugDeviceThatIsAbsentIsHeldWhenEnabledAndFailedWhenNot is the verdict pair, through
// the sweep: asserted-absent Sparkplug waits for the device with the capability on (the node
// comes back on its next birth), and is failed as undeliverable with it off.
func TestASparkplugDeviceThatIsAbsentIsHeldWhenEnabledAndFailedWhenNot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		wantHeld   []uint
		wantFailed []uint
	}{
		{"enabled holds", true, []uint{1}, nil},
		{"disabled fails", false, nil, []uint{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
			writer := &recordingWriter{}
			proc := procWith(api, writer)
			proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c1": asserted(false, sparkplugSource)}}
			proc.SparkplugCommands = tc.enabled

			proc.sweepLocked(context.Background())

			assert.Equal(t, tc.wantHeld, api.held)
			assert.Equal(t, tc.wantFailed, api.undeliverable)
			assert.Zero(t, writer.count(), "an absent device is never published to")
		})
	}
}

// TestImpactIsLeftOffWhenItCannotBeDecided: a failed or unwired lookup must still dispatch the
// command (the adapter, not this service, refuses the dangerous class on an absent stamp), must
// leave the impact header off rather than guess, must keep the transport stamp, and must be
// COUNTED so that an adapter refusing every reboot has a visible cause.
func TestImpactIsLeftOffWhenItCannotBeDecided(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolver model.CommandImpactResolver
	}{
		{"lookup fails", &impactSpy{err: errors.New("device-management unreachable")}},
		{"no resolver wired", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(3, "c3")}}
			writer := &recordingWriter{}
			proc := procWith(api, writer)
			proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c3": asserted(true, sparkplugSource)}}
			proc.SparkplugCommands = true
			proc.CommandImpacts = tc.resolver
			proc.ImpactLookupFailures = prometheus.NewCounter(prometheus.CounterOpts{Name: "impact_failures"})

			proc.sweepLocked(context.Background())

			require.Equal(t, 1, writer.count(), "a failed lookup must not stop the dispatch")
			assert.Equal(t, map[string]string{cmddownlink.HeaderTransport: "sparkplug"},
				headersByDevice(writer)["dev-c3"], "the impact header must be absent, not defaulted")
			assert.Equal(t, 1.0, testutil.ToFloat64(proc.ImpactLookupFailures))
		})
	}
}

// TestImpactIsMemoizedButAFailureIsNot: a second sweep over the same device and key reuses the
// answer (no further call to the owner), and a failure is retried on the next sweep instead of
// being cached for the TTL, because the header a failure leaves off is the fail-closed direction
// and caching it would stretch an outage.
func TestImpactIsMemoizedButAFailureIsNot(t *testing.T) {
	state := map[string]presence.State{"dev-s": asserted(true, sparkplugSource)}
	one := func() []*model.Command {
		a := queued(1, "c1")
		a.DeviceToken = "dev-s"
		return []*model.Command{a}
	}

	t.Run("success is cached", func(t *testing.T) {
		spy := &impactSpy{impact: "DESTRUCTIVE"}
		api := &fakeApi{lockAvailable: true, pending: one()}
		proc := procWith(api, &recordingWriter{})
		proc.Presence = &scriptedReader{states: state}
		proc.SparkplugCommands, proc.CommandImpacts = true, spy

		proc.sweepLocked(context.Background())
		api.pending = one()
		proc.sweepLocked(context.Background())

		assert.Equal(t, 1, spy.count(), "the second sweep reuses the first sweep's answer")
	})
	t.Run("failure is not cached", func(t *testing.T) {
		spy := &impactSpy{err: errors.New("down")}
		api := &fakeApi{lockAvailable: true, pending: one()}
		proc := procWith(api, &recordingWriter{})
		proc.Presence = &scriptedReader{states: state}
		proc.SparkplugCommands, proc.CommandImpacts = true, spy

		proc.sweepLocked(context.Background())
		api.pending = one()
		proc.sweepLocked(context.Background())

		assert.Equal(t, 2, spy.count(), "a failed lookup is retried on the next sweep")
	})
	t.Run("an entry expires", func(t *testing.T) {
		now := time.Now()
		var c impactCache
		c.now = func() time.Time { return now }
		k := impactKey{"acme", "dev-s", "reboot"}
		c.put(k, "DISRUPTIVE")
		_, hit := c.get(k)
		assert.True(t, hit)
		now = now.Add(impactCacheTTL + time.Second)
		_, hit = c.get(k)
		assert.False(t, hit, "a stale impact must not outlive the TTL")
	})
}

// TestAFleetWriteIsOneImpactCall: N Sparkplug devices and one command key resolve in ONE call that
// names every device, not N serial ones; a second key adds exactly one more. Devices the gate will
// not dispatch (held for absence) and devices on other transports are not asked about.
func TestAFleetWriteIsOneImpactCall(t *testing.T) {
	const n = 40
	var cmds []*model.Command
	states := map[string]presence.State{}
	for i := uint(1); i <= n; i++ {
		c := queued(i, fmt.Sprintf("c%d", i))
		cmds = append(cmds, c)
		states[c.DeviceToken] = asserted(true, sparkplugSource)
	}
	held := queued(n+1, "held")
	states[held.DeviceToken] = asserted(false, sparkplugSource)
	plain := queued(n+2, "plain")
	states[plain.DeviceToken] = asserted(true, mqttSource)
	other := queued(n+3, "other")
	other.Name = "wipe"
	states[other.DeviceToken] = asserted(true, sparkplugSource)
	cmds = append(cmds, held, plain, other)

	spy := &impactSpy{impact: "DISRUPTIVE"}
	writer := &recordingWriter{}
	proc := procWith(&fakeApi{lockAvailable: true, pending: cmds}, writer)
	proc.Presence = &scriptedReader{states: states}
	proc.SparkplugCommands, proc.CommandImpacts = true, spy

	proc.sweepLocked(context.Background())

	require.Equal(t, 2, spy.count(), "one call per distinct command key (reboot, wipe), not per device")
	sizes := map[int]bool{}
	for _, asked := range spy.asked {
		sizes[len(asked)] = true
	}
	assert.True(t, sizes[n] && sizes[1], "the reboot call names all %d dispatchable devices and the wipe call one: %v", n, spy.asked)
	for _, asked := range spy.asked {
		assert.NotContains(t, asked, held.DeviceToken, "a command the gate holds is not dispatched, so not asked about")
		assert.NotContains(t, asked, plain.DeviceToken, "a non-Sparkplug device is not asked about")
	}
}

// TestASlowImpactOwnerCannotStallDispatch: a device-management that never answers delays a sweep by
// the batch deadline at most, once, and every command is still dispatched: the non-Sparkplug one
// unaffected, the Sparkplug ones without an impact stamp and counted. Control: with the owner
// answering, the same sweep stamps the impact.
func TestASlowImpactOwnerCannotStallDispatch(t *testing.T) {
	build := func(spy *impactSpy) (*CommandDeliveryProcessor, *recordingWriter) {
		cmds := []*model.Command{queued(1, "c1"), queued(2, "c2"), queued(3, "c3")}
		writer := &recordingWriter{}
		proc := procWith(&fakeApi{lockAvailable: true, pending: cmds}, writer)
		proc.Presence = &scriptedReader{states: map[string]presence.State{
			"dev-c1": asserted(true, sparkplugSource),
			"dev-c2": asserted(true, sparkplugSource),
			"dev-c3": asserted(true, mqttSource),
		}}
		proc.SparkplugCommands, proc.CommandImpacts = true, spy
		proc.impactTimeout = 50 * time.Millisecond
		proc.ImpactLookupFailures = prometheus.NewCounter(prometheus.CounterOpts{Name: "slow_impact_failures"})
		return proc, writer
	}

	proc, writer := build(&impactSpy{block: true})
	start := time.Now()
	proc.sweepLocked(context.Background())
	assert.Less(t, time.Since(start), 2*time.Second, "the sweep waited on a hung owner far beyond the deadline")
	require.Equal(t, 3, writer.count(), "every command is dispatched whatever the owner does")
	headers := headersByDevice(writer)
	for _, d := range []string{"dev-c1", "dev-c2"} {
		assert.Equal(t, map[string]string{cmddownlink.HeaderTransport: "sparkplug"}, headers[d],
			"%s: no impact stamp when the owner timed out", d)
	}
	assert.Nil(t, headers["dev-c3"])
	assert.Equal(t, 2.0, testutil.ToFloat64(proc.ImpactLookupFailures), "each unstamped Sparkplug command is counted")

	proc, writer = build(&impactSpy{impact: "DISRUPTIVE"})
	proc.sweepLocked(context.Background())
	assert.Equal(t, "DISRUPTIVE", headersByDevice(writer)["dev-c1"][cmddownlink.HeaderCommandImpact],
		"control: with the owner answering the stamp is present")
}

// TestTheImpactCacheKeyNamesTenantDeviceAndCommand pins every part of the key through the sweep.
// The same device token exists in two tenants and the same device receives two command keys; the
// owner answers a DIFFERENT impact for each (tenant, key). Each message must carry its own. Dropping
// the tenant, or the command, from impactKey makes two of these share one cached answer.
func TestTheImpactCacheKeyNamesTenantDeviceAndCommand(t *testing.T) {
	mk := func(id uint, token, tenant, name string) *model.Command {
		c := queuedFor(id, token, tenant)
		c.DeviceToken = "dev-shared"
		c.Name = name
		return c
	}
	cmds := []*model.Command{
		mk(1, "a-reboot", "acme", "reboot"), mk(2, "a-wipe", "acme", "wipe"),
		mk(3, "o-reboot", "other", "reboot"), mk(4, "o-wipe", "other", "wipe"),
	}
	spy := &impactSpy{answer: func(tenant, key string) string { return tenant + "/" + key }}
	writer := &recordingWriter{}
	proc := procWith(&fakeApi{lockAvailable: true, pending: cmds}, writer)
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-shared": asserted(true, sparkplugSource)}}
	proc.SparkplugCommands, proc.CommandImpacts = true, spy

	proc.sweepLocked(context.Background())

	require.Equal(t, 4, writer.count())
	got := map[string]string{}
	for _, m := range writer.messages {
		var env cmddownlink.DeliveryEnvelope
		require.NoError(t, json.Unmarshal(m.Value, &env))
		got[env.Token] = m.Headers[cmddownlink.HeaderCommandImpact]
	}
	assert.Equal(t, map[string]string{
		"a-reboot": "acme/reboot", "a-wipe": "acme/wipe",
		"o-reboot": "other/reboot", "o-wipe": "other/wipe",
	}, got)
}

// TestTheHoldReconcilerAgreesWithTheSweepAboutSparkplug: one reading a held Sparkplug command as
// releasable while the other holds it would flap the row between HELD and QUEUED. With the
// capability on, a Sparkplug device still absent keeps its hold; with it off the same row is
// released so the sweep can fail it properly (today's behaviour).
func TestTheHoldReconcilerAgreesWithTheSweepAboutSparkplug(t *testing.T) {
	for _, tc := range []struct {
		name         string
		enabled      bool
		wantReleased int
	}{
		{"enabled keeps the hold", true, 0},
		{"disabled releases it for the sweep to fail", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
			proc := reconcilerWith(api, &scriptedReader{
				states: map[string]presence.State{"dev-c1": asserted(false, sparkplugSource)},
			})
			proc.SparkplugCommands = tc.enabled

			proc.reconcileHolds(context.Background())

			assert.Len(t, api.releasedHolds, tc.wantReleased)
		})
	}
	t.Run("enabled releases once the node is back", func(t *testing.T) {
		api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
		proc := reconcilerWith(api, &scriptedReader{
			states: map[string]presence.State{"dev-c1": asserted(true, sparkplugSource)},
		})
		proc.SparkplugCommands = true

		proc.reconcileHolds(context.Background())

		assert.Equal(t, []uint{1}, api.releasedHolds, "a returned Sparkplug node's held command must be released")
	})
}

// TestSparkplugStaysOffTheStrandedAllowList: the stranded-SENT pass re-arms a command for a SECOND
// actuation, and re-arming a QoS 0 DCMD would actuate twice. The adapter settles its own SENT rows,
// so the pass must skip Sparkplug whether or not the capability is on. The LwM2M control proves
// the pass runs at all.
func TestSparkplugStaysOffTheStrandedAllowList(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		api := &fakeApi{
			strandedLockAvailable: true,
			strandedPage:          []*model.Command{strandedCmd(1, "c1", "dev-1", "nonce-abc", time.Hour)},
		}
		proc := strandedProc(api, &scriptedReader{states: map[string]presence.State{
			"dev-1": asserted(false, sparkplugSource),
		}})
		proc.SparkplugCommands = enabled

		proc.reconcileStranded(context.Background())

		assert.Empty(t, api.parkCalls, "enabled=%v: a Sparkplug SENT row must never be re-armed", enabled)
	}

	api := &fakeApi{
		strandedLockAvailable: true,
		strandedPage:          []*model.Command{strandedCmd(1, "c1", "dev-1", "nonce-abc", time.Hour)},
	}
	proc := strandedProc(api, &scriptedReader{states: map[string]presence.State{"dev-1": asserted(false, lwm2mSource)}})
	proc.SparkplugCommands = true
	proc.reconcileStranded(context.Background())
	assert.Len(t, api.parkCalls, 1, "control: the same pass does park an LwM2M row")
}

// TestAnAdapterRefusalReachesTheCommandAsNotDelivered drives the UNSENT answer path through the
// real consumer: the response an adapter publishes (built from the SHARED envelope type, not a
// hand-written string) for a tenant with no enabled Sparkplug source is decoded by the response
// loop and handed to MarkResponse as a pre-wire refusal quoting the claim's nonce. The paired
// UNANSWERED case keeps the two classes apart: UNSENT is proven-safe-to-retry, UNANSWERED is not.
func TestAnAdapterRefusalReachesTheCommandAsNotDelivered(t *testing.T) {
	refusal := "COMMAND_TRANSPORT_UNSUPPORTED: commands are not enabled for this tenant's Sparkplug sources"
	for _, tc := range []struct {
		name  string
		reach string
		want  model.Reach
	}{
		{"unsent", cmddownlink.ReachUnsent, model.ReachUnsent},
		{"unanswered", cmddownlink.ReachUnanswered, model.ReachUnanswered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(cmddownlink.ResponseEnvelope{
				CommandToken: "cmd-1", Success: false, Error: &refusal, Reach: tc.reach, DispatchNonce: "n-7",
			})
			require.NoError(t, err)
			api := &fakeApi{}
			p := responseProcessor(t, api, "inst-1.acme.command-responses.dev-c3", string(body))

			readAndHandleOne(p, context.Background())

			require.Len(t, api.responseCalls, 1)
			call := api.responseCalls[0]
			assert.Equal(t, "cmd-1", call.commandToken)
			assert.Equal(t, "n-7", call.nonce)
			assert.False(t, call.success)
			assert.Equal(t, tc.want, call.reach)
		})
	}
}

// TestTheSharedWireTypesAgreeWithThisServicesOwn pins both directions of the contract the shared
// package states in prose ("byte-identical"): what this service publishes decodes into the shared
// delivery type with NO unknown field, and what the shared response type marshals decodes into
// this service's own response type with no unknown field. A field added on one side only fails
// here instead of being silently dropped by the other.
func TestTheSharedWireTypesAgreeWithThisServicesOwn(t *testing.T) {
	strict := func(data []byte, into any) error {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		return dec.Decode(into)
	}
	raw := json.RawMessage(`{"metric":"x"}`)
	msg := "no"

	// Delivery: published here, read by the adapter, and back.
	out, err := json.Marshal(deliveryEnvelope{Token: "c", DeviceToken: "d", Name: "n", Payload: &raw, DispatchNonce: "z"})
	require.NoError(t, err)
	var shared cmddownlink.DeliveryEnvelope
	require.NoError(t, strict(out, &shared), "this service publishes a field the shared type does not know")
	assert.Equal(t, "z", shared.DispatchNonce)
	back, err := json.Marshal(shared)
	require.NoError(t, err)
	var own deliveryEnvelope
	require.NoError(t, strict(back, &own), "the shared delivery type sends a field this service does not read")
	assert.Equal(t, "z", own.DispatchNonce)

	// Response: published by the adapter, read here, and back.
	in, err := json.Marshal(cmddownlink.ResponseEnvelope{CommandToken: "c", Error: &msg,
		Reach: cmddownlink.ReachUnsent, DispatchNonce: "z"})
	require.NoError(t, err)
	var ownResp responseEnvelope
	require.NoError(t, strict(in, &ownResp), "the shared response type sends a field this service does not read")
	assert.Equal(t, model.ReachUnsent, ownResp.Reach)
	assert.Equal(t, "z", ownResp.DispatchNonce)
	ownOut, err := json.Marshal(ownResp)
	require.NoError(t, err)
	var sharedResp cmddownlink.ResponseEnvelope
	require.NoError(t, strict(ownOut, &sharedResp), "this service's response type carries a field the shared type does not know")
	assert.Equal(t, "z", sharedResp.DispatchNonce)
}

// A Sparkplug command carries its own expiry as a header, so the adapter can look at it again after
// a wait of its own; a command that does not expire carries none, and only the Sparkplug path is
// stamped. Without the flag nothing at all is stamped (covered above).
func TestASparkplugCommandCarriesItsExpiryAsAHeader(t *testing.T) {
	expiring := queued(3, "c3")
	expiring.ExpiresAt = sql.NullTime{Time: time.Date(2026, 10, 27, 12, 30, 0, 0, time.UTC), Valid: true}
	forever := queued(4, "c4")
	other := queued(5, "c5")
	other.ExpiresAt = expiring.ExpiresAt

	api := &fakeApi{lockAvailable: true, pending: []*model.Command{expiring, forever, other}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = &scriptedReader{states: map[string]presence.State{
		"dev-c3": asserted(true, sparkplugSource),
		"dev-c4": asserted(true, sparkplugSource),
		"dev-c5": asserted(true, lwm2mSource),
	}}
	proc.CommandImpacts = &impactSpy{impact: "NORMAL"}
	proc.SparkplugCommands = true
	proc.sweepLocked(context.Background())

	h := headersByDevice(writer)
	assert.Equal(t, "2026-10-27T12:30:00Z", h["dev-c3"][cmddownlink.HeaderCommandExpires], "an expiring Sparkplug command lost its expiry")
	_, has := h["dev-c4"][cmddownlink.HeaderCommandExpires]
	assert.False(t, has, "a command that never expires was stamped with an expiry")
	_, has = h["dev-c5"][cmddownlink.HeaderCommandExpires]
	assert.False(t, has, "another transport's command was stamped with an expiry")
}
