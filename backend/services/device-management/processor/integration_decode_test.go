// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/transport"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE RESOLVER'S DECODE STEP. Each guard below is paired with the control that shows the same
// uplink resolving when the one thing is right, so a resolver that refused (or stored) everything
// cannot pass.

const tempDecoder = `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[
  {"name":"t","outputs":[{"key":"temp","expr":"double(i16be(frame, 0)) / 10.0"}]}],
  "vectors":[{"name":"v","input":{"frameHex":"00D7"},"expect":{"temp":21.5}}]}`

// publishDecoder sets doc on the rig's profile draft and publishes it.
func (r *integrationRig) publishDecoder(doc string) {
	r.t.Helper()
	_, err := r.api.SetDeviceProfileDecoder(r.ctx, "p", []byte(doc))
	require.NoError(r.t, err)
	_, err = r.capi.PublishDeviceProfile(r.ctx, "p", nil, nil, "t")
	require.NoError(r.t, err)
}

func (r *integrationRig) metric(key, dataType string, max *float64) {
	r.t.Helper()
	_, err := r.api.CreateMetricDefinition(r.ctx, &model.MetricDefinitionCreateRequest{
		Token: "md-" + key, DeviceProfileToken: "p", MetricKey: key, DataType: dataType, MaxValue: max})
	require.NoError(r.t, err)
}

// uplink is an integration's raw uplink for device, sealed the way the route seals the token.
func (r *integrationRig) uplink(integration, token string, up *esmodel.UnresolvedIntegrationUplinkPayload) *esmodel.UnresolvedEvent {
	e := r.event("dev", integration, token)
	e.EventType = eventtype.IntegrationUplink
	e.Payload = up
	return e
}

func frameUplink(b ...byte) *esmodel.UnresolvedIntegrationUplinkPayload {
	return &esmodel.UnresolvedIntegrationUplinkPayload{Frame: b, MaxReadings: 1}
}

// decodeRig is the integration rig with a resolver that counts into its own registry.
type decodeRig struct {
	*integrationRig
	rez     *EventResolver
	metrics *DecodeMetrics
	obs     *recordingObserver
}

type recordingObserver struct {
	mu  sync.Mutex
	got []DecodeObservation
}

func (o *recordingObserver) ObserveDecode(d DecodeObservation) {
	o.mu.Lock()
	o.got = append(o.got, d)
	o.mu.Unlock()
}

func (o *recordingObserver) all() []DecodeObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]DecodeObservation(nil), o.got...)
}

func newDecodeRig(t *testing.T) *decodeRig {
	t.Helper()
	rig := newIntegrationRig(t)
	ms := &core.Microservice{InstanceId: "t", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	d := &decodeRig{integrationRig: rig, metrics: newDecodeMetrics(ms), obs: &recordingObserver{}}
	d.rez = rig.resolver(config.AuthModeDisabled)
	d.rez.decode = d.metrics
	d.rez.decodeObserver = d.obs
	return d
}

func (d *decodeRig) resolve(e *esmodel.UnresolvedEvent) ([]EventResolutionResults, uint, error) {
	return d.rez.ResolveEvent(d.ctx, e)
}

func readingsOf(t *testing.T, results []EventResolutionResults) map[string]string {
	t.Helper()
	require.Len(t, results, 1)
	p, ok := results[0].Resolved.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
	require.True(t, ok)
	require.Len(t, p.Entries, 1)
	out := map[string]string{}
	for _, e := range p.Entries[0].Entries {
		out[e.Name] = e.Value
	}
	return out
}

func skewOf(m *DecodeMetrics) float64           { return testutil.ToFloat64(m.chargeSkew) }
func kindOf(m *DecodeMetrics, d string) float64 { return testutil.ToFloat64(m.kind.WithLabelValues(d)) }
func resultOf(m *DecodeMetrics, r string) float64 {
	return testutil.ToFloat64(m.results.WithLabelValues(r))
}

func TestAnUplinkIsDecodedIntoTheReadingsItsDecoderProduces(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)

	results, reason, err := d.resolve(d.uplink("chirp-1", d.token, frameUplink(0x00, 0xD7)))
	require.NoError(t, err)
	require.Zero(t, reason)
	require.Equal(t, map[string]string{"temp": "21.5"}, readingsOf(t, results))
	// It is an ordinary measurement from here on: the type, the source and the unit stamp.
	assert.Equal(t, eventtype.Measurement, results[0].Resolved.EventType)
	assert.Equal(t, transport.Qualify(transport.Integration, "chirp-1"), results[0].Resolved.Source)
	assert.Equal(t, "Cel", stampedUnit(t, results[0].Resolved))
	assert.Equal(t, 1.0, resultOf(d.metrics, "decoded"))
	require.Len(t, d.obs.all(), 1)
	assert.Equal(t, DecodeDecoded, d.obs.all()[0].Outcome)
	assert.Equal(t, "t", d.obs.all()[0].Section)
}

// An uplink missing the input the decoder declared is a recorded failure, never a silent drop.
func TestAnUplinkWithoutTheDecodersInputIsAMissingInputFailure(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	results, reason, err := d.resolve(d.uplink("chirp-1", d.token, &esmodel.UnresolvedIntegrationUplinkPayload{MaxReadings: 1}))
	require.Error(t, err)
	require.Empty(t, results)
	require.ErrorIs(t, err, ErrDecodeFailed)
	assert.Equal(t, uint(resolvedevent.FailureReason_DecodeFailed), reason)
	assert.Contains(t, err.Error(), "decode_failed: missing_input")
}

// A23: an unstorable or out-of-range output is a decode failure on delivery 1, found in the decode
// step; the control is the same decoder over a frame whose output is fine.
func TestAnUnstorableOrOutOfRangeOutputIsAnInvalidOutputDecodeFailure(t *testing.T) {
	d := newDecodeRig(t)
	limit := 100.0
	d.metric("count", "INT", nil)
	d.metric("level", "DOUBLE", &limit)
	d.publishDecoder(`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[
	  {"name":"t","outputs":[
	    {"key":"count","expr":"int(u16be(frame, 0)) * 1000000000"},
	    {"key":"level","expr":"double(u8(frame, 2))"}]}],
	  "vectors":[{"name":"v","input":{"frameHex":"000105"},"expect":{"count":1000000000,"level":5}}]}`)

	results, _, err := d.resolve(d.uplink("chirp-1", d.token, frameUplink(0x00, 0x01, 0x05)))
	require.NoError(t, err, "control: in range and storable")
	assert.Equal(t, "5", readingsOf(t, results)["level"])

	for name, frame := range map[string][]byte{
		"1e13 into an INT":         {0xFF, 0xFF, 0x05},
		"above the declared limit": {0x00, 0x01, 0xC8},
	} {
		results, reason, err := d.resolve(d.uplink("chirp-1", d.token, frameUplink(frame...)))
		require.Error(t, err, name)
		require.Empty(t, results, name)
		require.ErrorIs(t, err, ErrDecodeFailed, name)
		assert.Equal(t, uint(resolvedevent.FailureReason_DecodeFailed), reason, name)
		assert.Contains(t, err.Error(), "decode_failed: invalid_output", name)
	}
	assert.Equal(t, 2.0, resultOf(d.metrics, "invalid_output"))
}

// A5, at the resolver loop: a failing decode is recorded on its FIRST delivery, never left for a
// redelivery; the control is a transient failure on the same delivery, which is left unacked.
func TestAFailingDecodeIsRecordedOnTheFirstDeliveryNotRetried(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)

	run := func(e *esmodel.UnresolvedEvent) (failed []uint, acked bool) {
		wire, err := esproto.MarshalUnresolvedEvent(e)
		require.NoError(t, err)
		msgs := make(chan messaging.Message, 1)
		var mu sync.Mutex
		done := make(chan struct{})
		ack := &flagAck{}
		rez := NewEventResolver(1, d.capi, config.AuthModeDisabled, EventTimePolicy{}, msgs,
			func(error, messaging.Message) { t.Error("an unparseable message") },
			func(src messaging.Message, _ string, _ []EventResolutionResults) { _ = src.Ack(); close(done) },
			func(src messaging.Message, _ string, reason uint, _ esmodel.UnresolvedEvent, _ error) {
				mu.Lock()
				failed = append(failed, reason)
				mu.Unlock()
				_ = src.Ack()
				close(done)
			}, nil, nil)
		rez.SecretTransit = testSecretTransit(t)
		processed := make(chan struct{})
		go func() { rez.Process(context.Background()); close(processed) }()
		msgs <- messaging.NewConsumedMessage(messaging.ScopedSubject("t", "acme", streams.IntegrationUplinks), wire, 1, nil, ack)
		select {
		case <-done:
		case <-time.After(300 * time.Millisecond):
		}
		close(msgs)
		// Join the resolver: the buffered message may still be in flight, and sampling before it
		// settles would let an ack-and-drop regression read as 'not acked'.
		select {
		case <-processed:
		case <-time.After(10 * time.Second):
			t.Fatal("the resolver did not finish")
		}
		mu.Lock()
		defer mu.Unlock()
		return failed, ack.acked()
	}

	failed, acked := run(d.uplink("chirp-1", d.token, frameUplink(0x00)))
	assert.Equal(t, []uint{uint(resolvedevent.FailureReason_DecodeFailed)}, failed, "recorded on delivery 1")
	assert.True(t, acked, "settled with its record")

	// Control: a transient failure (the integration's token no longer opens) on delivery 1 is
	// neither recorded nor acked; it waits for its redelivery.
	bad := d.uplink("chirp-1", "not-the-token", frameUplink(0x00, 0xD7))
	bad.SealedIntegrationToken, _ = testSecretTransit(t).SealToken("acme", credential.IntegrationSealKind("chirp-1"), "not-the-token")
	failed, acked = run(bad)
	assert.NotContains(t, failed, uint(resolvedevent.FailureReason_DecodeFailed))
	assert.False(t, acked, "a transient failure on a non-final delivery must wait for its redelivery, not be acked and dropped")
}

type flagAck struct {
	mu sync.Mutex
	a  bool
}

func (f *flagAck) Ack() error                       { f.mu.Lock(); f.a = true; f.mu.Unlock(); return nil }
func (f *flagAck) acked() bool                      { f.mu.Lock(); defer f.mu.Unlock(); return f.a }
func (f *flagAck) Nak() error                       { return nil }
func (f *flagAck) InProgress() error                { return nil }
func (f *flagAck) Term() error                      { return nil }
func (f *flagAck) NakWithDelay(time.Duration) error { return nil }

// envResolution answers every profile read with a resolution whose decoder names an environment
// version this build does not know, the shape of a snapshot written by a newer release.
type envResolution struct {
	model.DeviceManagementApi
	res model.ProfileResolution
}

func (e envResolution) ProfileResolutionByDeviceType(context.Context, uint) (model.ProfileResolution, error) {
	return e.res, nil
}

func TestADecoderFromANewerEnvironmentFailsOnlyUplinksAndIsNeverFlattened(t *testing.T) {
	d := newDecodeRig(t)
	scope := model.ProfileScope{ProfileVersionToken: "p@9"}
	env2 := `{"kind":"CEL","envVersion":2,"input":"DECODED","sections":[{"name":"t","outputs":[{"key":"temp","expr":"1.0"}]}]}`
	d.rez.Api = envResolution{DeviceManagementApi: d.capi, res: model.NewProfileResolutionWithDecoder(scope, nil, []byte(env2))}

	// An uplink that CARRIES a decoded object (which a flatten would store) fails env_unsupported.
	up := &esmodel.UnresolvedIntegrationUplinkPayload{Decoded: []byte(`{"temp":20.5}`), MaxReadings: 1}
	results, reason, err := d.resolve(d.uplink("chirp-1", d.token, up))
	require.Error(t, err)
	require.Empty(t, results, "never flattened")
	require.ErrorIs(t, err, ErrDecodeFailed)
	assert.Equal(t, uint(resolvedevent.FailureReason_DecodeFailed), reason)
	assert.Contains(t, err.Error(), "decode_failed: env_unsupported")
	assert.Equal(t, 1.0, resultOf(d.metrics, "env_unsupported"))

	// A24: the same type's other events are untouched: a location resolves, and a Cut A
	// measurement is accepted as it came (counted as kind skew).
	loc := devLocationEvent()
	loc.Integration, loc.Source = "", ""
	results, _, err = d.resolve(loc)
	require.NoError(t, err)
	require.Len(t, results, 1)
	results, _, err = d.resolve(d.event("dev", "chirp-1", d.token))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"temp": "21"}, readingsOf(t, results))
	assert.Equal(t, 1.0, kindOf(d.metrics, kindSkewDecoderNew))
}

// A13, decoder_gone: an uplink carried raw for a decoder that was cleared inside the route's
// window is flattened here with the route's own code, so nothing the route accepted is lost.
func TestAnUplinkForAClearedDecoderIsFlattenedHere(t *testing.T) {
	d := newDecodeRig(t) // the profile has no decoder
	up := &esmodel.UnresolvedIntegrationUplinkPayload{Decoded: []byte(`{"temp":20.5,"battery":{"v":3.3},"name":"x"}`), MaxReadings: 3}
	results, _, err := d.resolve(d.uplink("chirp-1", d.token, up))
	require.NoError(t, err)
	got := readingsOf(t, results)
	assert.Equal(t, "20.5", got["temp"])
	assert.Equal(t, "3.3", got["battery.v"])
	assert.Equal(t, 1.0, kindOf(d.metrics, kindSkewDecoderGone))
}

// The mutant for A13: an uplink that flattens to nothing is not dropped, it is recorded on the
// integration as undecoded, as the route would have, and acknowledged.
func TestAnUplinkThatFlattensToNothingIsRecordedAsUndecoded(t *testing.T) {
	d := newDecodeRig(t)
	for name, up := range map[string]*esmodel.UnresolvedIntegrationUplinkPayload{
		"strings only":          {Decoded: []byte(`{"s":"x"}`), Frame: []byte{0xAB, 0xCD}, MaxReadings: 1},
		"a frame and no object": {Frame: []byte{0xAB, 0xCD}, MaxReadings: 1},
	} {
		results, _, err := d.resolve(d.uplink("chirp-1", d.token, up))
		require.NoError(t, err, name)
		require.Empty(t, results, name)
	}
	found, err := d.api.IntegrationsByToken(d.ctx, []string{"chirp-1"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, found[0].UndecodedCount)
	assert.Equal(t, hex.EncodeToString([]byte{0xAB, 0xCD}), found[0].LastUndecodedSample.String)
	assert.Equal(t, 2.0, kindOf(d.metrics, kindSkewDecoderGone))
}

// A25: the route charged for version 1 (max_readings 1) and this resolver runs a version that
// produces four: all four are stored and the excess is counted.
func TestReadingsBeyondTheChargeAreStoredAndCountedAsSkew(t *testing.T) {
	d := newDecodeRig(t)
	for _, k := range []string{"a", "b", "c", "d"} {
		d.metric(k, "DOUBLE", nil)
	}
	d.publishDecoder(`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[
	  {"name":"t","outputs":[
	    {"key":"a","expr":"double(u8(frame, 0))"},{"key":"b","expr":"double(u8(frame, 1))"},
	    {"key":"c","expr":"double(u8(frame, 2))"},{"key":"d","expr":"double(u8(frame, 3))"}]}],
	  "vectors":[{"name":"v","input":{"frameHex":"01020304"},"expect":{"a":1,"b":2,"c":3,"d":4}}]}`)

	up := frameUplink(1, 2, 3, 4)
	up.MaxReadings = 1
	results, _, err := d.resolve(d.uplink("chirp-1", d.token, up))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"}, readingsOf(t, results))
	assert.Equal(t, 3.0, skewOf(d.metrics))

	// Control: charged for what it produced, no skew.
	up = frameUplink(1, 2, 3, 4)
	up.MaxReadings = 4
	_, _, err = d.resolve(d.uplink("chirp-1", d.token, up))
	require.NoError(t, err)
	assert.Equal(t, 3.0, skewOf(d.metrics))
}

// The link metrics are appended from the AUTHENTICATED integration's own setting, after the decode.
func TestLinkMetricsFollowTheAuthenticatedIntegrationsSetting(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	on := true
	created, err := d.api.CreateIntegration(d.ctx, &model.IntegrationCreateRequest{
		Token: "chirp-2", Type: string(model.IntegrationChirpStackV4), Enabled: true, DeviceTypeTokens: []string{"dt"}, LinkMetrics: &on})
	require.NoError(t, err)

	link := map[string]float64{"rssi": -90, "snr": 7.5, "gateways": 2}
	up := frameUplink(0x00, 0xD7)
	up.Link, up.MaxReadings = link, 4
	// chirp-1 has linkMetrics off: a route that carried the link anyway does not get it stored.
	results, _, err := d.resolve(d.uplink("chirp-1", d.token, up))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"temp": "21.5"}, readingsOf(t, results))

	e := d.uplink("chirp-2", created.IssuedToken, up)
	results, _, err = d.resolve(e)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"temp": "21.5", "lorawan.rssi": "-90", "lorawan.snr": "7.5", "lorawan.gateways": "2"}, readingsOf(t, results))
	assert.Zero(t, skewOf(d.metrics), "the charge covered the link readings")
}

// An integration uplink is trusted only through an integration's mark, whatever path it arrives on.
func TestAnIntegrationUplinkWithoutAnIntegrationMarkIsRefused(t *testing.T) {
	d := newDecodeRig(t)
	d.publishDecoder(tempDecoder)
	good := d.uplink("chirp-1", d.token, frameUplink(0x00, 0xD7))
	_, _, err := d.resolve(good)
	require.NoError(t, err, "control")

	for name, mutate := range map[string]func(*esmodel.UnresolvedEvent){
		"no integration or token": func(e *esmodel.UnresolvedEvent) { e.Integration, e.SealedIntegrationToken = "", nil },
		"a gateway":               func(e *esmodel.UnresolvedEvent) { e.Integration, e.SealedIntegrationToken, e.Gateway = "", nil, "gw" },
	} {
		e := d.uplink("chirp-1", d.token, frameUplink(0x00, 0xD7))
		mutate(e)
		results, reason, err := d.resolve(e)
		require.Error(t, err, name)
		require.Empty(t, results, name)
		assert.Equal(t, uint(resolvedevent.FailureReason_Unauthenticated), reason, name)
	}
	// And the type is refused on a handler that was not given an authenticated integration.
	_, reason, err := d.rez.HandleEvent(d.ctx, &model.Device{}, good)
	require.Error(t, err)
	assert.Equal(t, uint(resolvedevent.FailureReason_Unauthenticated), reason)
}

// A ledger of the sequence-collision defect this stream would otherwise have: an uplink at
// integration-uplinks sequence N and an inbound event at inbound-events sequence N name different
// publishes on the shared resolved and failed streams.
func TestAnUplinksPublishIdsNeverEqualAnInboundEventsAtTheSameSequence(t *testing.T) {
	inbound := messaging.NewConsumedMessage(messaging.ScopedSubject("t", "acme", streams.InboundEvents), nil, 1, nil, nil)
	uplink := messaging.NewConsumedMessage(messaging.ScopedSubject("t", "acme", streams.IntegrationUplinks), nil, 1, nil, nil)
	inbound.StreamSeq, uplink.StreamSeq = 100, 100
	for _, kind := range []string{dedupKindResolved, dedupKindFailed} {
		a, b := sourceDedupID(kind, "acme", inbound, 0), sourceDedupID(kind, "acme", uplink, 0)
		assert.NotEmpty(t, a)
		assert.NotEqual(t, a, b, kind)
	}
	assert.Equal(t, "r:acme:2s:0", sourceDedupID(dedupKindResolved, "acme", inbound, 0), "an inbound id is what it always was")
}

func TestADecodeFailureNamesItsReasonAndNothingInternal(t *testing.T) {
	var f error = &DecodeFailure{Reason: payloaddecode.ReasonNoSection, Text: "no_section: no section matched this uplink"}
	assert.True(t, errors.Is(f, ErrDecodeFailed))
	assert.Equal(t, "decode_failed: no_section: no section matched this uplink", f.Error())
}

// An uplink is never grouped into a resolved envelope: a group is named by (tenant, sequence,
// append time), unique within one stream only, so an inbound event and an uplink at the same
// sequence would merge and one would be acknowledged without being published.
func TestAnUplinkIsNeverGroupedIntoAResolvedEnvelope(t *testing.T) {
	mk := func(suffix string) resolvedItem {
		src := messaging.NewConsumedMessage(messaging.ScopedSubject("t", "acme", suffix), nil, 1, nil, nil)
		src.StreamSeq, src.AppendTime = 100, time.Unix(1_000_000, 0)
		return resolvedItem{tenant: "acme", coord: &ackCoord{src: src}}
	}
	_, _, ok := memberIndex(mk(streams.InboundEvents), 10)
	assert.True(t, ok, "control: an inbound event is grouped")
	_, _, ok = memberIndex(mk(streams.IntegrationUplinks), 10)
	assert.False(t, ok)
}
