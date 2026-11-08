// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A DEVICE'S OWN UPLINK. It carries no integration mark, so the only provenance it has is the
// stream it was read from, and the only authority is the device's own credential policy. Each
// refusal is paired with the control that resolves, so a resolver that refused everything fails.

// deviceUplink is the event event-sources builds for a device's own payload: no mark, no seal.
func (d *decodeRig) deviceUplink(up *esmodel.UnresolvedIntegrationUplinkPayload) *esmodel.UnresolvedEvent {
	e := tempEvent("21")
	e.Device = "dev"
	e.Source = "http"
	e.EventType = eventtype.IntegrationUplink
	e.Payload = up
	return e
}

func TestADeviceUplinkIsDecodedOnlyWhenReadFromTheUplinkStream(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)

	// Control: from the uplink stream it decodes into the readings its decoder produces.
	results, reason, err := d.rez.ResolveEvent(withUplinkStream(d.ctx), d.deviceUplink(frameUplink(0x00, 0xD7)))
	require.NoError(t, err)
	require.Zero(t, reason)
	require.Equal(t, map[string]string{"temp": "21.5"}, readingsOf(t, results))
	assert.Equal(t, eventtype.Measurement, results[0].Resolved.EventType)
	assert.Equal(t, "http", results[0].Resolved.Source)
	assert.Equal(t, 1.0, resultOf(d.metrics, "decoded"))

	// Anywhere else (the context carries no stream mark) it is refused, before any device lookup.
	results, reason, err = d.rez.ResolveEvent(d.ctx, d.deviceUplink(frameUplink(0x00, 0xD7)))
	require.Error(t, err)
	require.Empty(t, results)
	assert.Equal(t, uint(resolvedevent.FailureReason_Unauthenticated), reason)

	// And it is refused BEFORE the device is looked up: for a device nobody registered the answer is
	// still Unauthenticated, not DeviceNotFound, so the refusal says nothing about which devices exist.
	ghost := d.deviceUplink(frameUplink(0x00, 0xD7))
	ghost.Device = "no-such-device"
	_, reason, err = d.rez.ResolveEvent(d.ctx, ghost)
	require.Error(t, err)
	assert.Equal(t, uint(resolvedevent.FailureReason_Unauthenticated), reason)
	_, reason, err = d.rez.ResolveEvent(withUplinkStream(d.ctx), ghost)
	require.Error(t, err)
	assert.Equal(t, uint(resolvedevent.FailureReason_DeviceNotFound), reason, "control: from the stream the lookup does happen")
}

func TestADeviceUplinkClaimsNoAuthorityButItsOwnCredential(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	ctx := withUplinkStream(d.ctx)
	for name, mutate := range map[string]func(*esmodel.UnresolvedEvent){
		"a gateway":               func(e *esmodel.UnresolvedEvent) { e.Gateway = "gw" },
		"transport authenticated": func(e *esmodel.UnresolvedEvent) { e.AuthenticatedTransport = true },
		"an integration identity": func(e *esmodel.UnresolvedEvent) { e.IntegrationIdentifier = "x" },
	} {
		e := d.deviceUplink(frameUplink(0x00, 0xD7))
		mutate(e)
		results, reason, err := d.rez.ResolveEvent(ctx, e)
		require.Error(t, err, name)
		require.Empty(t, results, name)
		assert.Equal(t, uint(resolvedevent.FailureReason_Unauthenticated), reason, name)
	}
}

// The credential policy applies as to any device event: with authentication required, an uplink
// that presents no credential is refused (and the same uplink resolves with it off).
func TestADeviceUplinkFollowsTheDeviceAuthPolicy(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	ctx := withUplinkStream(d.ctx)

	_, _, err := d.rez.ResolveEvent(ctx, d.deviceUplink(frameUplink(0x00, 0xD7)))
	require.NoError(t, err, "control: authentication off")

	required := d.resolver(config.AuthModeRequired)
	required.decode, required.decodeObserver = d.metrics, d.obs
	results, reason, err := required.ResolveEvent(ctx, d.deviceUplink(frameUplink(0x00, 0xD7)))
	require.Error(t, err)
	require.Empty(t, results)
	assert.Equal(t, uint(resolvedevent.FailureReason_Unauthenticated), reason)
}

// A device whose profile has no decoder is a recorded no_decoder failure, terminal on delivery 1:
// its bytes are never flattened and never silently dropped. The control is the same type of
// uplink for an integration, which does flatten.
func TestADeviceUplinkForATypeWithoutADecoderFailsNoDecoder(t *testing.T) {
	d := newDecodeRig(t) // the profile has no decoder
	results, reason, err := d.rez.ResolveEvent(withUplinkStream(d.ctx), d.deviceUplink(&esmodel.UnresolvedIntegrationUplinkPayload{
		Json: []byte(`{"temp":20.5}`), MaxReadings: 2}))
	require.Error(t, err)
	require.Empty(t, results)
	require.ErrorIs(t, err, ErrDecodeFailed)
	assert.Equal(t, uint(resolvedevent.FailureReason_DecodeFailed), reason)
	assert.Contains(t, err.Error(), "decode_failed: no_decoder")
	assert.Equal(t, 1.0, resultOf(d.metrics, "no_decoder"))
	require.Len(t, d.obs.all(), 1)
	assert.Equal(t, DecodeFailed, d.obs.all()[0].Outcome)

	flat, _, err := d.resolve(d.uplink("chirp-1", d.token, &esmodel.UnresolvedIntegrationUplinkPayload{
		Decoded: []byte(`{"temp":20.5}`), MaxReadings: 2}))
	require.NoError(t, err, "control: an integration's uplink for the same type flattens")
	assert.Equal(t, "20.5", readingsOf(t, flat)["temp"])
}

// A decode failure on the device wire takes the same path as an integration's: missing input is a
// terminal DecodeFailed.
func TestADeviceUplinkWithoutTheDecodersInputIsAMissingInputFailure(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	_, reason, err := d.rez.ResolveEvent(withUplinkStream(d.ctx), d.deviceUplink(&esmodel.UnresolvedIntegrationUplinkPayload{MaxReadings: 1}))
	require.ErrorIs(t, err, ErrDecodeFailed)
	assert.Equal(t, uint(resolvedevent.FailureReason_DecodeFailed), reason)
	assert.Contains(t, err.Error(), "missing_input")
}

// An integration's mark still decides an integration event, whatever the stream says: the stream
// flag never widens an integration's own checks.
func TestTheUplinkStreamMarkDoesNotWidenAnIntegrationEvent(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	e := d.uplink("chirp-1", "not-the-token", frameUplink(0x00, 0xD7))
	_, reason, err := d.rez.ResolveEvent(withUplinkStream(d.ctx), e)
	require.Error(t, err)
	assert.Equal(t, uint(resolvedevent.FailureReason_Unauthenticated), reason)
}

// END TO END, ON A REAL BROKER: a device's own uplink, published to the integration-uplinks stream
// the way event-sources does (no integration mark), is pulled by the uplink durable, accepted
// because of the stream it was read from, decoded and stored. The control for the mark being the
// stream (and nothing in the event) is TestADeviceUplinkIsDecodedOnlyWhenReadFromTheUplinkStream.
func TestADeviceUplinkOnTheStreamIsConsumedDecodedAndStored(t *testing.T) {
	e := newE2ERig(t)
	ev := e.uplink("chirp-1", e.token, frameUplink(0x00, 0xD7))
	ev.Integration, ev.SealedIntegrationToken, ev.IntegrationIdentifier = "", nil, ""
	ev.Source = "http"
	ev.OccurredTime, ev.ProcessedTime = time.Now(), time.Now()
	wire, err := esproto.MarshalUnresolvedEvent(ev)
	require.NoError(t, err)
	require.NoError(t, e.uplk.WriteMessages(core.WithTenant(context.Background(), "acme"),
		messaging.Message{Key: []byte("dev"), Value: wire}))

	m := e.next(streams.ResolvedEvents)
	re, err := resolvedevent.UnmarshalResolvedEvent(m.Data)
	require.NoError(t, err)
	p := re.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
	require.Equal(t, "temp", p.Entries[0].Entries[0].Name)
	require.Equal(t, "21.5", p.Entries[0].Entries[0].Value)
	require.Eventually(t, func() bool {
		ci := e.consumerInfo(streams.IntegrationUplinks, "device-management")
		return ci.AckFloor.Consumer == 1 && ci.NumAckPending == 0
	}, 10*time.Second, 50*time.Millisecond)
}

// THE STREAM IS THE PROVENANCE. The same mark-less event, written to inbound-events (where a device
// path could put one) is refused and never decoded; the one on the uplink stream, afterwards, is.
func TestADeviceUplinkOnInboundEventsIsNeverDecoded(t *testing.T) {
	e := newE2ERig(t)
	ev := e.uplink("chirp-1", e.token, frameUplink(0x00, 0xD7))
	ev.Integration, ev.SealedIntegrationToken, ev.IntegrationIdentifier = "", nil, ""
	ev.Source = "http"
	ev.OccurredTime, ev.ProcessedTime = time.Now(), time.Now()
	wire, err := esproto.MarshalUnresolvedEvent(ev)
	require.NoError(t, err)

	inbound, err := e.esm.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	ctx := core.WithTenant(context.Background(), "acme")
	require.NoError(t, inbound.WriteMessages(ctx, messaging.Message{Key: []byte("dev"), Value: wire}))
	// The delivery is observed, and the resolver is given a bounded window in which a decode would
	// show. The event is held for retry rather than settled (a device the platform does not know yet
	// may appear), so there is no settled state to wait for; what makes the check sound is that it
	// can only fail loudly: a stream that was never created has no messages, and any other failure to
	// ask is a failed test, never a skipped assertion.
	require.Eventually(t, func() bool {
		return e.consumerInfo(streams.InboundEvents, "device-management").Delivered.Consumer >= 1
	}, 10*time.Second, 50*time.Millisecond)
	resolvedMsgs := func() uint64 {
		info, err := e.js.StreamInfo(messaging.StreamName(e2eInstance, streams.ResolvedEvents))
		if errors.Is(err, nats.ErrStreamNotFound) {
			return 0
		}
		require.NoError(t, err)
		return info.State.Msgs
	}
	require.Never(t, func() bool { return resolvedMsgs() != 0 }, 2*time.Second, 50*time.Millisecond,
		"a mark-less uplink read from inbound-events was decoded")

	// The control: a DIFFERENT frame on the uplink stream resolves, so the rig can resolve it at all,
	// and it is the first and only thing on resolved-events. Were the forbidden event emitted late,
	// it would be read first (21.5) or make the count two.
	ctrl := e.uplink("chirp-1", e.token, frameUplink(0x00, 0xD8))
	ctrl.Integration, ctrl.SealedIntegrationToken, ctrl.IntegrationIdentifier = "", nil, ""
	ctrl.Source = "http"
	ctrl.OccurredTime, ctrl.ProcessedTime = time.Now(), time.Now()
	ctrlWire, err := esproto.MarshalUnresolvedEvent(ctrl)
	require.NoError(t, err)
	require.NoError(t, e.uplk.WriteMessages(ctx, messaging.Message{Key: []byte("dev"), Value: ctrlWire}))
	m := e.next(streams.ResolvedEvents)
	re, err := resolvedevent.UnmarshalResolvedEvent(m.Data)
	require.NoError(t, err)
	require.Equal(t, "21.6", re.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries[0].Value)
	require.Never(t, func() bool { return resolvedMsgs() > 1 }, time.Second, 50*time.Millisecond,
		"the inbound-events uplink was decoded after the control")
}

// A device's uplink is never given link metrics, whatever it carries: they are the platform's,
// appended by an authenticated integration's own setting, and a device has none.
func TestADeviceUplinkStoresNoLinkMetrics(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	up := frameUplink(0x00, 0xD7)
	up.Link = map[string]float64{"rssi": -90, "snr": 7, "gateways": 3}

	results, _, err := d.rez.ResolveEvent(withUplinkStream(d.ctx), d.deviceUplink(up))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"temp": "21.5"}, readingsOf(t, results), "no lorawan.* reading for a device")
}
