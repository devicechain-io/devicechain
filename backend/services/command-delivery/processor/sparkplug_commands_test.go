// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
)

// impactSpy is a model.CommandImpactResolver that records how often it was asked.
type impactSpy struct {
	mu     sync.Mutex
	calls  int
	impact string
	err    error
}

func (s *impactSpy) CommandImpact(_ context.Context, _, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.impact, s.err
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

// TestImpactIsMemoizedButAFailureIsNot: the second command to the same device and key reuses the
// answer (one call to the owner), and a failure is retried on the next command instead of being
// cached for the TTL, because the header a failure leaves off is the fail-closed direction and
// caching it would stretch an outage.
func TestImpactIsMemoizedButAFailureIsNot(t *testing.T) {
	cmds := func() []*model.Command {
		a, b := queued(1, "c1"), queued(2, "c2")
		a.DeviceToken, b.DeviceToken = "dev-s", "dev-s"
		return []*model.Command{a, b}
	}
	state := map[string]presence.State{"dev-s": asserted(true, sparkplugSource)}

	t.Run("success is cached", func(t *testing.T) {
		spy := &impactSpy{impact: "DESTRUCTIVE"}
		proc := procWith(&fakeApi{lockAvailable: true, pending: cmds()}, &recordingWriter{})
		proc.Presence = &scriptedReader{states: state}
		proc.SparkplugCommands, proc.CommandImpacts = true, spy

		proc.sweepLocked(context.Background())

		assert.Equal(t, 1, spy.count(), "two commands to one device and key resolve the impact once")
	})
	t.Run("failure is not cached", func(t *testing.T) {
		spy := &impactSpy{err: errors.New("down")}
		proc := procWith(&fakeApi{lockAvailable: true, pending: cmds()}, &recordingWriter{})
		proc.Presence = &scriptedReader{states: state}
		proc.SparkplugCommands, proc.CommandImpacts = true, spy

		proc.sweepLocked(context.Background())

		assert.Equal(t, 2, spy.count(), "a failed lookup is retried for the next command")
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
	raw := json.RawMessage(`{"metric":"x"}`)
	out, err := json.Marshal(deliveryEnvelope{Token: "c", DeviceToken: "d", Name: "n", Payload: &raw, DispatchNonce: "z"})
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	var shared cmddownlink.DeliveryEnvelope
	require.NoError(t, dec.Decode(&shared), "this service publishes a field the shared type does not know")
	assert.Equal(t, "z", shared.DispatchNonce)

	msg := "no"
	in, err := json.Marshal(cmddownlink.ResponseEnvelope{CommandToken: "c", Error: &msg,
		Reach: cmddownlink.ReachUnsent, DispatchNonce: "z"})
	require.NoError(t, err)
	dec = json.NewDecoder(bytes.NewReader(in))
	dec.DisallowUnknownFields()
	var own responseEnvelope
	require.NoError(t, dec.Decode(&own), "the shared type sends a field this service does not read")
	assert.Equal(t, model.ReachUnsent, own.Reach)
	assert.Equal(t, "z", own.DispatchNonce)
}
