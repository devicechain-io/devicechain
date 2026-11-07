// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func asWriter(ctx context.Context) context.Context {
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DeviceRead), string(auth.DeviceWrite)}})
}

// The observer's refusal reaches the error the resolver loop acts on: the failure is marked
// suppressed, and only when the observer said so.
func TestARefusedRecordMarksTheFailureSuppressed(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)

	_, _, err := d.resolve(d.uplink("chirp-1", d.token, frameUplink(0x00)))
	var f *DecodeFailure
	require.True(t, errors.As(err, &f))
	assert.False(t, f.Suppressed, "an observer that allows the record leaves the failure to be recorded")

	d.obs.refuse = true
	_, _, err = d.resolve(d.uplink("chirp-1", d.token, frameUplink(0x00)))
	require.True(t, errors.As(err, &f))
	assert.True(t, f.Suppressed, "a refused record is a suppressed failure")
	require.ErrorIs(t, err, ErrDecodeFailed, "and it is still the terminal decode failure")
}

// The loop creates one deliveryState per delivery and the store hold runs the resolve closure again
// on each probe of an outage. The observer must hear that delivery once, and every run must get the
// same verdict.
func TestTheObserverHearsAReRunDeliveryOnce(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)

	for name, up := range map[string]*esmodel.UnresolvedIntegrationUplinkPayload{
		"decoded": frameUplink(0x00, 0xD7), "failed": frameUplink(0x00)} {
		t.Run(name, func(t *testing.T) {
			before := len(d.obs.all())
			st := &deliveryState{}
			ctx := withDelivery(d.ctx, st)
			for i := 0; i < 3; i++ { // three probes of one delivery
				_, _, _ = d.rez.ResolveEvent(ctx, d.uplink("chirp-1", d.token, up))
			}
			assert.Equal(t, before+1, len(d.obs.all()), "observed once, not once per run")
		})
	}

	// And the verdict of the first run is the verdict of the re-runs.
	d.obs.refuse = true
	st := &deliveryState{}
	ctx := withDelivery(d.ctx, st)
	var verdicts []bool
	for i := 0; i < 2; i++ {
		_, _, err := d.rez.ResolveEvent(ctx, d.uplink("chirp-1", d.token, frameUplink(0x00)))
		var f *DecodeFailure
		require.True(t, errors.As(err, &f))
		verdicts = append(verdicts, f.Suppressed)
	}
	assert.Equal(t, []bool{true, true}, verdicts)
}

func TestARedeliveryIsToldToTheObserver(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	ctx := withDelivery(d.ctx, &deliveryState{redelivered: true})
	_, _, _ = d.rez.ResolveEvent(ctx, d.uplink("chirp-1", d.token, frameUplink(0x00)))
	require.Len(t, d.obs.all(), 1)
	assert.True(t, d.obs.all()[0].Redelivered)

	_, _, _ = d.resolve(d.uplink("chirp-1", d.token, frameUplink(0x00)))
	assert.False(t, d.obs.all()[1].Redelivered, "a first delivery is not")
}

// Decision 14: a stored sample replays, through the test panel's door, to the outcome it had live.
func TestAStoredSampleReplaysToTheOutcomeItHadLive(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	clk := newFakeClock()
	acct := newDecodeAccountant(d.api, "pod-a", nil, clk.now)
	d.rez.decodeObserver = acct
	require.NoError(t, d.api.RDB.DB(d.ctx).Exec(
		`CREATE UNIQUE INDEX uix_profile_decoder_healths_window ON profile_decoder_healths
		 (tenant_id, device_profile_id, profile_version, window_start, replica)`).Error)

	fport, fcnt := int32(2), uint32(7)
	up := frameUplink(0x00)
	up.FPort, up.FCnt = &fport, &fcnt
	_, _, err := d.resolve(d.uplink("chirp-1", d.token, up))
	var live *DecodeFailure
	require.True(t, errors.As(err, &live))
	require.NoError(t, acct.Flush(d.ctx))

	writer := asWriter(d.ctx)
	samples, err := d.api.PayloadDecoderSamples(writer, "p", 10)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, string(live.Reason), samples[0].Reason)

	id := samples[0].ID
	res, err := d.api.TestPayloadDecoder(writer, &model.PayloadDecoderTestRequest{ProfileToken: "p", SampleId: &id})
	require.NoError(t, err)
	require.False(t, res.OK)
	require.NotNil(t, res.Failure)
	assert.Equal(t, string(live.Reason), res.Failure.Reason, "the replay fails the way the live decode did")
	assert.Contains(t, live.Text, res.Failure.Message, "with the same message")
}

// vectorOf is the one mapping from the carrier to the stored form, and it carries exactly what the
// live decode was given (uplinkInput); the replay test above proves the stored form reads back.
func TestTheSampleMappingCarriesWhatTheLiveInputHeld(t *testing.T) {
	fport, fcnt := int32(2), uint32(7)
	up := &esmodel.UnresolvedIntegrationUplinkPayload{Frame: []byte{0, 0xD7}, FPort: &fport, FCnt: &fcnt,
		Json: []byte(`{"a":1}`), Decoded: []byte(`{"b":2}`), Link: map[string]float64{"rssi": -90}}
	v, err := vectorOf(up)
	require.NoError(t, err)
	live := uplinkInput(up)
	require.NotNil(t, v.FrameHex)
	assert.Equal(t, "00d7", *v.FrameHex)
	assert.Equal(t, live.LoRa.FPort, int64(*v.FPort))
	assert.Equal(t, live.LoRa.FCnt, int64(*v.FCnt))
	assert.JSONEq(t, string(live.JSON), string(v.JSON))
	assert.JSONEq(t, string(live.Decoded), string(v.Decoded))
	assert.Equal(t, live.Link, v.Link)
}

func TestACarriedObjectThatIsNotJSONStoresNoSample(t *testing.T) {
	_, err := vectorOf(&esmodel.UnresolvedIntegrationUplinkPayload{Json: []byte(`{"a":`)})
	require.Error(t, err)
	_, err = vectorOf(&esmodel.UnresolvedIntegrationUplinkPayload{Decoded: []byte(`nope`)})
	require.Error(t, err)

	clk := newFakeClock()
	st := &fakeStore{}
	a := newDecodeAccountant(st, "pod-a", nil, clk.now)
	o := failure("acme", 7, "meter@1")
	o.Uplink = &esmodel.UnresolvedIntegrationUplinkPayload{Json: []byte(`{"a":`)}
	require.True(t, a.ObserveDecode(o), "the failure is still recorded")
	require.NoError(t, a.Flush(context.Background()))
	assert.Empty(t, st.samples, "but no sample is kept in a form that would replay differently")
	r, _ := st.last("acme", "meter", 1)
	assert.EqualValues(t, 1, r.Failed)
}
