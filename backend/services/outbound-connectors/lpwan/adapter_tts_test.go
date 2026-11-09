// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

const ttsKey = "dc:0123456789abcdef0123456789abcdef"

func newTTSRig(t *testing.T) (*rig, *memPending) {
	t.Helper()
	r, p := newConfirmedRig(t)
	r.links.target = connectorspec.LoRaWANDownlinkTarget{NetworkServer: connectorspec.NetworkServerTTSV3,
		Integration: "cs1", ApplicationID: "my-app", APIKey: "k"}
	r.devices.ttsID = "sensor-1"
	r.send.id = ttsKey
	return r, p
}

func TestATTSDownlinkIsAddressedByTheDeviceIDAndNotTheDevEUI(t *testing.T) {
	r, _ := newTTSRig(t)
	r.devices.eui = "" // a TTS device needs no DevEUI alias
	r.handle(t, subject, envelope(t, goodParams), routed())
	require.Len(t, r.send.calls, 1)
	require.Equal(t, "sensor-1", r.send.calls[0].eui, "the identity the network server addresses it by, read from the device")
	require.Len(t, r.resp.envs, 1)
	require.True(t, r.resp.envs[0].Success, "unconfirmed: answered at acceptance, as for ChirpStack")
}

func TestAConfirmedTTSDownlinkWaitsAndIsStoredWithItsDeviceRefAndCorrelationKey(t *testing.T) {
	r, pend := newTTSRig(t)
	r.handle(t, subject, envelope(t, confirmedParams), routed())
	require.Empty(t, r.resp.envs)
	require.Equal(t, 1, pend.size())
	for _, p := range pend.rows {
		require.Equal(t, "my-app/sensor-1", p.DeviceRef)
		require.Empty(t, p.DevEUI)
		require.Equal(t, ttsKey, p.QueueItemID)
		require.Equal(t, "claimed-nonce", p.DispatchNonce)
		require.Equal(t, "cs1", p.Integration)
	}
}

func TestATTSDeviceWithNoDeviceIDAliasIsRefusedUnsentNeverGuessed(t *testing.T) {
	r, _ := newTTSRig(t)
	r.devices.ttsID = ""
	r.devices.eui = "0102030405060708" // a DevEUI must NOT stand in for the device id
	r.handle(t, subject, envelope(t, goodParams), routed())
	require.Empty(t, r.send.calls)
	require.Len(t, r.resp.envs, 1)
	require.Equal(t, cmddownlink.ReachUnsent, r.resp.envs[0].Reach)
	require.JSONEq(t, `{"reason":"NO_DEVICE_ID"}`, *r.resp.envs[0].Payload)
	require.Equal(t, 1, r.m.get(ResultRefusedNoDevEUI))

	// A transient read failure is left unacked, not refused.
	r2, _ := newTTSRig(t)
	r2.devices.err = errors.New("device-management down")
	ack := r2.handle(t, subject, envelope(t, goodParams), routed())
	require.False(t, ack.acked)
	require.Empty(t, r2.resp.envs)
}

func TestAChirpStackLinkStillNeedsADevEUINotADeviceID(t *testing.T) {
	r := newRig(t, nil)
	r.devices.eui = ""
	r.devices.ttsID = "sensor-1"
	r.handle(t, subject, envelope(t, goodParams), routed())
	require.Empty(t, r.send.calls)
	require.JSONEq(t, `{"reason":"NO_DEVEUI"}`, *r.resp.envs[0].Payload)
}
