// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
)

const confirmedParams = `{"fPort":10,"hex":"0a0b","confirmed":true}`

func newConfirmedRig(t *testing.T) (*rig, *memPending) {
	t.Helper()
	p := newMemPending()
	return newRig(t, func(o *Options) { o.Pending = p }), p
}

func TestAConfirmedDownlinkWaitsForItsAcknowledgementAndIsNotAnsweredYet(t *testing.T) {
	r, pend := newConfirmedRig(t)
	ack := r.handle(t, subject, envelope(t, confirmedParams), routed())

	require.True(t, ack.acked, "the message is done: the pending record carries the command from here")
	require.Empty(t, r.resp.envs, "queue acceptance is not the device's acknowledgement; the command stays SENT")
	require.Len(t, r.send.calls, 1)
	require.True(t, r.send.calls[0].params.Confirmed, "the network server is asked for a confirmed downlink")

	require.Equal(t, 1, pend.size())
	var got Pending
	for _, p := range pend.rows {
		got = p
	}
	require.Equal(t, Pending{
		ID: got.ID, Tenant: "acme", Integration: "cs1", DeviceToken: "dev-1", DevEUI: "0102030405060708",
		CommandToken: "cmd-1", DispatchNonce: "claimed-nonce", QueueItemID: "q-1",
		DeadlineAt: r.now.Add(DefaultAckWait),
	}, got, "stored with the CLAIM's nonce (not the envelope's), the network server's queue item id, and the default wait")
	require.Equal(t, 1, r.m.get(ResultQueuedConfirmed))
	require.Zero(t, r.m.get(ResultQueued))
}

func TestTheQueueItemIdIsStoredLowerCaseAsTheReportArrives(t *testing.T) {
	r, pend := newConfirmedRig(t)
	r.send.id = "5E0A6C1E-6A1C-4F38-9D2B-0C7A2B7D9F11"
	r.handle(t, subject, envelope(t, confirmedParams), routed())
	for _, p := range pend.rows {
		require.Equal(t, "5e0a6c1e-6a1c-4f38-9d2b-0c7a2b7d9f11", p.QueueItemID, "reports are lower-cased by the codec")
	}
}

func TestAConfirmedDownlinksWaitEndsAtTheCommandsOwnExpiry(t *testing.T) {
	r, pend := newConfirmedRig(t)
	h := routed()
	exp := r.now.Add(90 * time.Second)
	h[cmddownlink.HeaderCommandExpires] = exp.Format(time.RFC3339)
	r.handle(t, subject, envelope(t, confirmedParams), h)
	for _, p := range pend.rows {
		require.True(t, p.DeadlineAt.Equal(exp), "the wait is capped by the expiry, got %s", p.DeadlineAt)
	}

	// A far expiry does not stretch the wait past the adapter's own.
	r2, pend2 := newConfirmedRig(t)
	h2 := routed()
	h2[cmddownlink.HeaderCommandExpires] = r2.now.Add(1000 * time.Hour).Format(time.RFC3339)
	r2.handle(t, subject, envelope(t, confirmedParams), h2)
	for _, p := range pend2.rows {
		require.True(t, p.DeadlineAt.Equal(r2.now.Add(DefaultAckWait)))
	}
}

func TestAnUnconfirmedDownlinkIsAnsweredAtQueueAcceptanceAndStoresNothing(t *testing.T) {
	// The same rig WITH a pending store: unconfirmed behaviour must not depend on it.
	r, pend := newConfirmedRig(t)
	r.handle(t, subject, envelope(t, goodParams), routed())
	require.Zero(t, pend.size(), "an unconfirmed downlink has no acknowledgement to wait for")
	require.Len(t, r.resp.envs, 1)
	require.True(t, r.resp.envs[0].Success)
	require.Equal(t, cmddownlink.ReachUnanswered, r.resp.envs[0].Reach)
	require.JSONEq(t, `{"stage":"queued","queueItemId":"q-1"}`, *r.resp.envs[0].Payload)
	require.Equal(t, 1, r.m.get(ResultQueued))
	require.Zero(t, r.m.get(ResultQueuedConfirmed))
}

func TestAConfirmedDownlinkThatCannotBeTrackedIsAnsweredUnansweredAtOnce(t *testing.T) {
	r, pend := newConfirmedRig(t)
	pend.addErr = errors.New("db down")
	ack := r.handle(t, subject, envelope(t, confirmedParams), routed())

	require.True(t, ack.acked)
	require.Len(t, r.resp.envs, 1, "the downlink is queued and nothing could ever close the command, so it is answered now")
	e := r.resp.envs[0]
	require.False(t, e.Success)
	require.Equal(t, cmddownlink.ReachUnanswered, e.Reach, "TIMEOUT: the device may act on a downlink the platform lost track of")
	require.Equal(t, "claimed-nonce", e.DispatchNonce)
	require.JSONEq(t, `{"reason":"ACK_UNTRACKED"}`, *e.Payload)
	require.Equal(t, 1, r.m.get(ResultPendingFailed))
}

func TestAConfirmedDownlinkTheNetworkServerRefusedStoresNothingAndIsUnsent(t *testing.T) {
	r, pend := newConfirmedRig(t)
	r.send.err = &SendError{Reach: cmddownlink.ReachUnsent, Text: "refused (HTTP 400)"}
	r.handle(t, subject, envelope(t, confirmedParams), routed())
	require.Zero(t, pend.size())
	require.Len(t, r.resp.envs, 1)
	require.Equal(t, cmddownlink.ReachUnsent, r.resp.envs[0].Reach)
}

func TestAConfirmedDownlinkRedeliveredAfterTheClaimIsNotSentTwice(t *testing.T) {
	r, pend := newConfirmedRig(t)
	r.handle(t, subject, envelope(t, confirmedParams), routed())
	r.claim.won = false
	r.handle(t, subject, envelope(t, confirmedParams), routed())
	require.Len(t, r.send.calls, 1)
	require.Equal(t, 1, pend.size())
}

func TestWithoutAPendingStoreAConfirmedDownlinkIsStillRefusedNeverDowngraded(t *testing.T) {
	r := newRig(t, nil)
	r.handle(t, subject, envelope(t, confirmedParams), routed())
	require.Empty(t, r.send.calls, "it must not be sent unconfirmed")
	require.Len(t, r.resp.envs, 1)
	require.Equal(t, cmddownlink.ReachUnsent, r.resp.envs[0].Reach)
	require.Equal(t, 1, r.m.get(ResultRefusedConfirm))
}
