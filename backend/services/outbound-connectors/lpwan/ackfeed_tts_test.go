// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
)

func (r *ackRig) seedTTS(t *testing.T) Pending {
	t.Helper()
	return r.seed(t, func(p *Pending) {
		p.Integration, p.DevEUI, p.DeviceRef, p.QueueItemID = "tts1", "", "my-app/sensor-1", ttsKey
	})
}

func ttsReport(kind string, acked bool) cmddownlink.DownlinkReport {
	return cmddownlink.DownlinkReport{Integration: "tts1", Kind: kind, DevEUI: "0004A30B001C0530",
		DeviceRef: "my-app/sensor-1", DeviceToken: "dev-1", QueueItemID: ttsKey, Acknowledged: acked}
}

func TestATTSAckNackSentAndFailedMapOnlyToWhatTheyProve(t *testing.T) {
	// downlink_ack: the device acknowledged.
	r := newAckRig(t)
	r.seedTTS(t)
	r.deliver(t, ttsReport("ack", true), 1)
	require.Len(t, r.resp.envs, 1)
	require.True(t, r.resp.envs[0].Success)
	require.Equal(t, "claimed-nonce", r.resp.envs[0].DispatchNonce)

	// downlink_nack: the device did not. That is not proof it never heard the frame (the ack may
	// have been lost), so it is TIMEOUT / UNKNOWN, never NOT_DELIVERED.
	r = newAckRig(t)
	r.seedTTS(t)
	r.deliver(t, ttsReport("ack", false), 1)
	require.Len(t, r.resp.envs, 1)
	require.False(t, r.resp.envs[0].Success)
	require.Equal(t, cmddownlink.ReachUnanswered, r.resp.envs[0].Reach)
	require.Equal(t, 1, r.m.get(AckTimedOut))

	// downlink_sent: a gateway transmitted it. Recorded; the command keeps waiting.
	r = newAckRig(t)
	r.seedTTS(t)
	r.deliver(t, ttsReport("txack", false), 1)
	require.Empty(t, r.resp.envs)
	require.Equal(t, 1, r.pend.size())
	require.Equal(t, 1, r.m.get(AckTransmitted))

	// downlink_failed with one of the network server's own pre-transmission drops, no retry before it and
	// no transmission reported: NOT_DELIVERED (UNSENT).
	r = newAckRig(t)
	r.seedTTS(t)
	a := r.deliver(t, ttsFailed(0, "pkg/networkserver", "downlink_expired"), 1)
	require.True(t, a.acked)
	require.Len(t, r.resp.envs, 1)
	e := r.resp.envs[0]
	require.False(t, e.Success)
	require.Equal(t, cmddownlink.ReachUnsent, e.Reach)
	require.JSONEq(t, `{"reason":"DOWNLINK_FAILED"}`, *e.Payload)
	require.Zero(t, r.pend.size())
	require.Equal(t, 1, r.m.get(AckFailed))
}

func ttsFailed(attempt int, ns, name string) cmddownlink.DownlinkReport {
	rep := ttsReport("failed", false)
	rep.Attempt, rep.ErrorNamespace, rep.ErrorName = attempt, ns, name
	return rep
}

// 🔴 downlink_failed IS NOT ALWAYS A FRAME THAT NEVER LEFT. The Application Server emits it when it
// gives up retrying a transmitted confirmed downlink, so every case that cannot positively show a
// pre-transmission drop must be TIMEOUT / UNKNOWN, never NOT_DELIVERED.
func TestAFailedReportThatCannotShowTheFrameNeverLeftIsTimeoutNotNotDelivered(t *testing.T) {
	cases := map[string]struct {
		rep      cmddownlink.DownlinkReport
		txBefore bool
	}{
		"retries exhausted (AS emits it after transmissions)": {ttsFailed(8, "pkg/applicationserver", "max_retries_reached"), false},
		"re-push after a nack failed (attempt 1)":             {ttsFailed(1, "pkg/networkserver", "downlink_expired"), false},
		"any retry at all":                               {ttsFailed(2, "pkg/networkserver", "unknown_session"), false},
		"failed after a downlink_sent":                   {ttsFailed(0, "pkg/networkserver", "downlink_expired"), true},
		"a gateway transmission error":                   {ttsFailed(0, "pkg/networkserver", "transmission"), false},
		"an error name nobody classified":                {ttsFailed(0, "pkg/networkserver", "something_new"), false},
		"no error at all":                                {ttsFailed(0, "", ""), false},
		"a pre-transmission name from another namespace": {ttsFailed(0, "pkg/applicationserver", "downlink_expired"), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newAckRig(t)
			r.seedTTS(t)
			if c.txBefore {
				r.deliver(t, ttsReport("txack", false), 1)
				require.Empty(t, r.resp.envs)
			}
			a := r.deliver(t, c.rep, 1)
			require.True(t, a.acked)
			require.Len(t, r.resp.envs, 1)
			e := r.resp.envs[0]
			require.False(t, e.Success)
			require.Equal(t, cmddownlink.ReachUnanswered, e.Reach, "UNSENT would offer a retry as safe for a frame that may have been transmitted")
			require.JSONEq(t, `{"reason":"DOWNLINK_FAILED_MAYBE_SENT"}`, *e.Payload)
			require.Equal(t, 1, r.m.get(AckFailedMaybeSent))
			require.Zero(t, r.m.get(AckFailed))
		})
	}

	// Each of the network server's own pre-transmission drops, alone, is NOT_DELIVERED.
	for name := range preTransmissionErrors {
		r := newAckRig(t)
		r.seedTTS(t)
		r.deliver(t, ttsFailed(0, "pkg/networkserver", name), 1)
		require.Len(t, r.resp.envs, 1, name)
		require.Equal(t, cmddownlink.ReachUnsent, r.resp.envs[0].Reach, name)
	}
}

func TestAReportForTheRightDeviceRefButNotTheDeviceTheDownlinkWentToSettlesNothing(t *testing.T) {
	for name, token := range map[string]string{"another device token": "dev-2", "no device token": ""} {
		t.Run(name, func(t *testing.T) {
			r := newAckRig(t)
			r.seedTTS(t)
			rep := ttsReport("ack", true)
			rep.DeviceToken = token
			a := r.deliver(t, rep, 1)
			require.True(t, a.acked)
			require.Empty(t, r.resp.envs, "DeviceRef is text the network server sent; the resolved device is what must agree")
			require.Equal(t, 1, r.m.get(AckMismatch))
		})
	}
}

func TestATTSReportIsMatchedOnItsDeviceRefAndANoLongerPendingFailedIsUnmatched(t *testing.T) {
	for name, mut := range map[string]func(*cmddownlink.DownlinkReport){
		"another application": func(rep *cmddownlink.DownlinkReport) { rep.DeviceRef = "other-app/sensor-1" },
		"another device":      func(rep *cmddownlink.DownlinkReport) { rep.DeviceRef = "my-app/sensor-2" },
		"another integration": func(rep *cmddownlink.DownlinkReport) { rep.Integration = "tts2" },
		"no device ref":       func(rep *cmddownlink.DownlinkReport) { rep.DeviceRef = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newAckRig(t)
			r.seedTTS(t)
			rep := ttsReport("failed", false)
			mut(&rep)
			a := r.deliver(t, rep, 1)
			require.True(t, a.acked)
			require.Empty(t, r.resp.envs, "a correlation id alone settles nothing")
			require.Equal(t, 1, r.pend.size())
			require.Equal(t, 1, r.m.get(AckMismatch))
		})
	}

	// A ChirpStack-shaped report (a DevEUI, no device ref) cannot settle a TTS downlink, nor the reverse.
	r := newAckRig(t)
	r.seedTTS(t)
	cs := report("ack", true)
	cs.QueueItemID = ttsKey
	cs.Integration = "tts1"
	r.deliver(t, cs, 1)
	require.Empty(t, r.resp.envs)
	require.Equal(t, 1, r.m.get(AckMismatch))

	r2 := newAckRig(t)
	r2.seed(t, nil) // a ChirpStack downlink
	rep := report("ack", true)
	rep.DeviceRef = "my-app/sensor-1"
	r2.deliver(t, rep, 1)
	require.Empty(t, r2.resp.envs)
	require.Equal(t, 1, r2.m.get(AckMismatch))
}

func TestAReportNamingNoDeviceAtAllIsPoison(t *testing.T) {
	r := newAckRig(t)
	r.seedTTS(t)
	rep := ttsReport("ack", true)
	rep.DevEUI, rep.DeviceRef = "", ""
	a := r.deliver(t, rep, 1)
	require.True(t, a.acked)
	require.Equal(t, 1, r.m.get(AckPoison))
	require.Empty(t, r.resp.envs)
}
