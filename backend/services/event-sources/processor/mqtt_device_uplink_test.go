// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/payloaddecode/plancost"
	"github.com/stretchr/testify/require"
)

// A device's raw payload on its own MQTT uplink topic, carried to the profile decoder from the
// capture source.

const uplinkSubject = "inst-1.acme.devices.meter-1.uplink"

// brokerPlanFake answers the broker-asserted question: a nil plan is "no such device".
type brokerPlanFake struct {
	mu    sync.Mutex
	plan  *integration.DecoderPlan
	err   error
	calls []integration.DeviceCredential
	asked []string
	// hang, when set, holds every call until it is closed (or the test ends): a device-management
	// that has stopped answering.
	hang chan struct{}
}

func (f *brokerPlanFake) DeviceDecoder(_ context.Context, _, device string, cred integration.DeviceCredential) (*integration.DecoderPlan, error) {
	f.mu.Lock()
	hang := f.hang
	f.mu.Unlock()
	if hang != nil {
		<-hang
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cred)
	f.asked = append(f.asked, device)
	if f.err != nil {
		return nil, f.err
	}
	if f.plan == nil {
		return nil, nil
	}
	p := *f.plan
	return &p, nil
}

type uplinkOut struct {
	source, tenant string
	event          *model.UnresolvedEvent
	payload        *model.UnresolvedIntegrationUplinkPayload
	dedupID        string
}

type uplinkHarness struct {
	*captureHarness
	plans *brokerPlanFake

	mu      sync.Mutex
	out     []uplinkOut
	results []string
	charged []int
	pubErr  error
}

func (u *uplinkHarness) published() []uplinkOut {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]uplinkOut(nil), u.out...)
}

func (u *uplinkHarness) resultList() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.results...)
}

func newUplinkHarness(t *testing.T, plan *integration.DecoderPlan) *uplinkHarness {
	t.Helper()
	u := &uplinkHarness{plans: &brokerPlanFake{plan: plan}}
	u.captureHarness = newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) {
		require.NoError(t, s.UseDeviceUplinks(&DeviceUplinkRoute{
			Plans: integration.NewDeviceDecoders(u.plans, integration.DeviceDecoderConfig{}),
			Publish: func(source, tenant string, e *model.UnresolvedEvent, p interface{}, dedupID string) error {
				u.mu.Lock()
				defer u.mu.Unlock()
				if u.pubErr != nil {
					return u.pubErr
				}
				u.out = append(u.out, uplinkOut{source, tenant, e, p.(*model.UnresolvedIntegrationUplinkPayload), dedupID})
				return nil
			},
			Message: func(r string) { u.mu.Lock(); u.results = append(u.results, r); u.mu.Unlock() },
			Charged: func(n int) { u.mu.Lock(); u.charged = append(u.charged, n); u.mu.Unlock() },
		}))
	})
	return u
}

func uplinkFramePlan() *integration.DecoderPlan {
	return &integration.DecoderPlan{Present: true, Input: "FRAME", MaxReadings: 4, ProfileVersionToken: "meter@3",
		Cost: plancost.Shape{Composed: 100}}
}

func TestADevicesRawPayloadIsCarriedToItsTypesDecoder(t *testing.T) {
	u := newUplinkHarness(t, uplinkFramePlan())
	ack := &recordingAck{}
	at := time.Now().Add(-time.Second).UTC()
	u.source.handle(capturedMsgAt(uplinkSubject, "\x01\x02\x03", 77, at, ack))

	require.True(t, ack.settled(t))
	require.Equal(t, 1, ack.counts(), "acked exactly once, after the publish")
	out := u.published()
	require.Len(t, out, 1)
	o := out[0]
	require.Equal(t, "acme", o.tenant)
	require.Equal(t, "meter-1", o.event.Device, "the device is the subject's, the one the broker confined the publisher to")
	require.Equal(t, eventtype.IntegrationUplink, o.event.EventType)
	require.True(t, o.event.BrokerAsserted, "the broker authenticated the device")
	require.Empty(t, o.event.Integration, "a device carries no integration's authority")
	require.Nil(t, o.event.SealedIntegrationToken)
	require.Nil(t, o.event.CredentialType, "no credential: the identity is the broker's")
	require.Equal(t, []byte{1, 2, 3}, o.payload.Frame)
	require.Equal(t, "meter@3", o.payload.ProfileVersionToken)
	require.Equal(t, DedupID("acme", 77), o.dedupID, "a redelivered capture message is stored once")
	require.True(t, o.event.OccurredTime.Equal(at), "the broker's append time")

	// It is asked as the broker-asserted device, not with a credential.
	require.Len(t, u.plans.calls, 1)
	require.True(t, u.plans.calls[0].AssertedByBroker)
	require.Empty(t, u.plans.calls[0].ID)
	require.Equal(t, []string{"meter-1"}, u.plans.asked)

	// Charged at the reading gate from the plan's figures, as the HTTP wire is.
	require.Len(t, u.readingCalls, 1)
	require.Equal(t, u.charged[0], u.readingCalls[0].readings)
	require.GreaterOrEqual(t, u.readingCalls[0].readings, 4, "at least the declared readings")
	require.Equal(t, OriginAuthenticated, u.readingCalls[0].origin)

	// Nothing went down the telemetry path, and nothing was recorded as a failure.
	published, failed, _, _ := u.counts()
	require.Zero(t, published, "a raw payload never reaches inbound-events")
	require.Zero(t, failed)
	require.Equal(t, []string{DeviceUplinkAccepted}, u.resultList())
}

// The same message on the EVENTS topic is telemetry, decoded as an envelope, and never reaches the
// profile decoder: the topic is the opt-in.
func TestAPayloadOnTheEventsTopicIsNeverReadByTheProfileDecoder(t *testing.T) {
	u := newUplinkHarness(t, uplinkFramePlan())
	ack := &recordingAck{}
	u.source.handle(capturedMsgAt(captureSubject, "\x01\x02\x03", 1, time.Now(), ack))
	require.True(t, ack.settled(t))
	published, failed, _, _ := u.counts()
	require.Zero(t, published)
	require.Equal(t, 1, failed, "not an envelope: failed-decode, as before")
	require.Empty(t, u.published())
	require.Empty(t, u.plans.calls, "the decoder was never asked about it")
}

func TestAPayloadForADeviceWithNoDecoderIsRecordedNotFlattenedNorDropped(t *testing.T) {
	u := newUplinkHarness(t, &integration.DecoderPlan{})
	ack := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, "\x01", 1, time.Now(), ack))
	require.True(t, ack.settled(t), "terminal: acked so it does not redeliver forever")
	_, failed, _, _ := u.counts()
	require.Equal(t, 1, failed)
	require.ErrorIs(t, u.failedReasons[0], ErrUplinkNoDecoder)
	require.Empty(t, u.published())
	require.Equal(t, []string{DeviceUplinkNoDecoder}, u.resultList())
}

func TestAPayloadForAnUnknownDeviceIsRecorded(t *testing.T) {
	u := newUplinkHarness(t, nil)
	ack := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, "\x01", 1, time.Now(), ack))
	require.True(t, ack.settled(t))
	_, failed, _, _ := u.counts()
	require.Equal(t, 1, failed)
	require.ErrorIs(t, u.failedReasons[0], ErrUplinkDeviceUnknown)
	require.Empty(t, u.published())
}

// Not knowing is neither "no such device" nor "no decoder": the message is left unacked for the
// broker to redeliver, and the failure is not recorded as the device's.
func TestAnUnavailablePlanLeavesThePayloadForRedelivery(t *testing.T) {
	u := newUplinkHarness(t, uplinkFramePlan())
	u.plans.err = integration.ErrDeviceDecoderUnsupported
	ack := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, "\x01", 1, time.Now(), ack))
	require.Eventually(t, func() bool { return len(u.resultList()) == 1 }, 2*time.Second, time.Millisecond)
	require.Zero(t, ack.counts(), "left unacked")
	_, failed, _, _ := u.counts()
	require.Zero(t, failed, "a failure to ask is not a verdict on the payload")
	require.Equal(t, []string{DeviceUplinkPlanUnavail}, u.resultList())
}

func TestAFailedPublishLeavesThePayloadUnackedAndABackpressuredOneToo(t *testing.T) {
	for name, perr := range map[string]error{
		"downstream":   errors.New("stream unavailable"),
		"backpressure": messaging.ErrStreamBackpressure,
	} {
		t.Run(name, func(t *testing.T) {
			u := newUplinkHarness(t, uplinkFramePlan())
			u.pubErr = perr
			ack := &recordingAck{}
			u.source.handle(capturedMsgAt(uplinkSubject, "\x01", 1, time.Now(), ack))
			require.Eventually(t, func() bool { return len(u.resultList()) == 1 }, 2*time.Second, time.Millisecond)
			require.Zero(t, ack.counts(), "unacked: the broker redelivers it")
		})
	}
}

func TestATerminalBuildDropSettlesTheUplink(t *testing.T) {
	u := newUplinkHarness(t, uplinkFramePlan())
	u.pubErr = ErrEventNotBuilt
	ack := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, "\x01", 1, time.Now(), ack))
	require.True(t, ack.settled(t), "a retry would fail identically")
	_, failed, _, _ := u.counts()
	require.Equal(t, 1, failed, "recorded with its cause, like every other payload that cannot be carried")
	require.ErrorIs(t, u.failedReasons[0], ErrEventNotBuilt)
}

// A frame over the carrier's cap for a frame decoder is refused, recorded with its cause.
func TestAnOversizeFrameIsRecordedAndAcked(t *testing.T) {
	u := newUplinkHarness(t, uplinkFramePlan())
	ack := &recordingAck{}
	big := make([]byte, plancost.MaxFrameBytes+1)
	u.source.handle(capturedMsgAt(uplinkSubject, string(big), 1, time.Now(), ack))
	require.True(t, ack.settled(t))
	_, failed, _, _ := u.counts()
	require.Equal(t, 1, failed)
	require.Empty(t, u.published())
	require.Equal(t, []string{DeviceUplinkTooLarge}, u.resultList())
}

// MQTT carries no content type: bytes that begin like an object are parsed for a decoder that reads
// JSON, and are carried as a frame when they are not one.
func TestJSONIsParsedForAJSONDecoderAndBrokenJSONIsJustBytes(t *testing.T) {
	jplan := &integration.DecoderPlan{Present: true, Input: "JSON", MaxReadings: 2, ProfileVersionToken: "meter@4",
		Cost: plancost.Shape{Composed: 100, ParsesJSON: true}}
	u := newUplinkHarness(t, jplan)
	ack := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, ` {"t":21.5}`, 1, time.Now(), ack))
	require.True(t, ack.settled(t))
	ack2 := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, `{"t":21.5`, 2, time.Now(), ack2))
	require.True(t, ack2.settled(t))
	// Trailing bytes after a complete object: not one JSON value, so not read as the object before them.
	ack3 := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, `{"t":21.5}junk`, 3, time.Now(), ack3))
	require.True(t, ack3.settled(t))
	out := u.published()
	require.Len(t, out, 3)
	byDedup := map[string]uplinkOut{}
	for _, o := range out {
		byDedup[o.dedupID] = o
	}
	good := byDedup[DedupID("acme", 1)]
	require.JSONEq(t, `{"t":21.5}`, string(good.payload.Json))
	broken := byDedup[DedupID("acme", 2)]
	require.Empty(t, broken.payload.Json, "not an object: not carried as one")
	require.Equal(t, `{"t":21.5`, string(broken.payload.Frame))
	trailing := byDedup[DedupID("acme", 3)]
	require.Empty(t, trailing.payload.Json, "trailing bytes: not read as the object that precedes them")
	require.Equal(t, `{"t":21.5}junk`, string(trailing.payload.Frame))
	_, failed, _, _ := u.counts()
	require.Zero(t, failed, "the decoder records what it could not read, as for any bytes")
}

// The tenant's ceilings apply: the message stage sheds before any decode work, the reading stage
// after, and a shed payload is acked (a redelivery would meet the same gate).
func TestAnOverCeilingUplinkIsShedAtBothStages(t *testing.T) {
	t.Run("message stage", func(t *testing.T) {
		u := newUplinkHarness(t, uplinkFramePlan())
		u.mu.Lock()
		u.allowResult = false
		u.mu.Unlock()
		ack := &recordingAck{}
		u.source.handle(capturedMsgAt(uplinkSubject, "\x01", 1, time.Now(), ack))
		require.True(t, ack.settled(t))
		require.Empty(t, u.plans.calls, "shed before the lookup")
		require.Empty(t, u.published())
	})
	t.Run("reading stage", func(t *testing.T) {
		u := newUplinkHarness(t, uplinkFramePlan())
		u.mu.Lock()
		u.readings = func(string, string, time.Time, bool, Origin, int) bool { return false }
		u.mu.Unlock()
		ack := &recordingAck{}
		u.source.handle(capturedMsgAt(uplinkSubject, "\x01", 1, time.Now(), ack))
		require.True(t, ack.settled(t))
		require.Empty(t, u.published())
		require.Equal(t, []string{DeviceUplinkRateLimited}, u.resultList())
		require.Empty(t, u.charged, "a shed payload is not counted as charged")
	})
}

// A source with no route behind it records the payload with its cause: never decoded as an
// envelope, never dropped without a word.
func TestARawPayloadOnASourceWithNoRouteIsRecorded(t *testing.T) {
	h := newCaptureHarness(t)
	ack := &recordingAck{}
	h.source.handle(capturedMsgAt(uplinkSubject, "\x01\x02", 1, time.Now(), ack))
	require.True(t, ack.settled(t))
	published, failed, _, _ := h.counts()
	require.Zero(t, published)
	require.Equal(t, 1, failed)
	require.ErrorIs(t, h.failedReasons[0], ErrDeviceUplinksNotServed)
}

// The route cannot be half-built.
func TestUseDeviceUplinksRefusesAnIncompleteRoute(t *testing.T) {
	h := newCaptureHarness(t)
	require.Error(t, h.source.UseDeviceUplinks(nil))
	require.Error(t, h.source.UseDeviceUplinks(&DeviceUplinkRoute{}))
}

// Provenance: only the capture source asserts, and only for a subject the broker confined the
// publisher to. The raw-payload subject asserts the device, never a gateway.
func TestTheUplinkSubjectAssertsTheDeviceAndNotAGateway(t *testing.T) {
	a := assertedByCaptureBroker(messaging.Message{Subject: uplinkSubject})
	require.True(t, a.asserted)
	require.Empty(t, a.gateway)
	require.Equal(t, "meter-1", deviceFromSubject(uplinkSubject))
	// A gateway's namespace has no raw-payload leaf.
	require.False(t, assertedByCaptureBroker(messaging.Message{Subject: "inst-1.acme.gateways.g1.devices.c1.uplink"}).asserted)
	require.Empty(t, deviceFromSubject("inst-1.acme.gateways.g1.devices.c1.uplink"))
}

// With envelope mode on, a raw payload still carries the plain capture-sequence dedup id: it goes to
// the uplink stream, not through the engine that settles inbound-events rounds, so it must not be
// wrapped in the engine's position token.
func TestARawPayloadInEnvelopeModeKeepsAPlainDedupID(t *testing.T) {
	b := newEnvBroker()
	plans := &brokerPlanFake{plan: uplinkFramePlan()}
	var mu sync.Mutex
	var dedups []string
	es := NewGatewayJetStreamSource(nil, "gw-env", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		func(_ string, tenant string, event *model.UnresolvedEvent, _ interface{}, id string) (context.Context, messaging.Message, bool) {
			return core.WithTenant(context.Background(), tenant), messaging.Message{Value: pbDevice(event.Device), DedupID: id}, true
		},
		func(string, string, []byte, error) error { return nil },
		nil, admitAllReadings, nil)
	es.SetWriter(envWriter{b})
	require.NoError(t, es.UseEnvelopes(CaptureEnvelopes{Rounds: envRounds{b}, Former: messaging.FormerConfig{Linger: 5 * time.Millisecond}}))
	require.NoError(t, es.UseDeviceUplinks(&DeviceUplinkRoute{
		Plans: integration.NewDeviceDecoders(plans, integration.DeviceDecoderConfig{}),
		Publish: func(_, _ string, _ *model.UnresolvedEvent, _ interface{}, dedupID string) error {
			mu.Lock()
			defer mu.Unlock()
			dedups = append(dedups, dedupID)
			return nil
		},
	}))
	es.startPipeline(2)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.True(t, es.stopPipeline(ctx), "the pipeline did not stop")
	})

	ack := &recordingAck{}
	es.handle(capturedMsgAt(uplinkSubject, "\x01", 91, midBucket(), ack))
	require.True(t, ack.settled(t))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{DedupID("acme", 91)}, dedups)
}

// A raw payload the broker did not assert is never carried: the worker refuses it before the route
// is asked anything (the assertion is the only thing that makes the subject's device the publisher).
func TestARawPayloadWithoutTheBrokersAssertionIsRefused(t *testing.T) {
	plans := &brokerPlanFake{plan: uplinkFramePlan()}
	var published int
	var failed []error
	rt := &DeviceUplinkRoute{
		Plans: integration.NewDeviceDecoders(plans, integration.DeviceDecoderConfig{}),
		Publish: func(string, string, *model.UnresolvedEvent, interface{}, string) error {
			published++
			return nil
		},
	}
	in := make(chan rawMessage, 2)
	w := NewDecodeWorker(1, "src", NewJsonDecoder(nil), in, admitAllReadings,
		func(string, string, *model.UnresolvedEvent, interface{}, string, func(error)) {},
		func(_ string, _ string, _ []byte, err error) error { failed = append(failed, err); return nil })
	w.Uplinks = newUplinkRunner(rt, admitAllReadings, w.Failed)

	settled := make(chan error, 2)
	// No assertion (the zero value, which is what every non-capture source carries), and a gateway's.
	in <- rawMessage{tenant: "acme", device: "meter-1", payload: []byte{1}, uplink: true, done: func(err error) { settled <- err }}
	in <- rawMessage{tenant: "acme", device: "meter-1", payload: []byte{1}, uplink: true,
		assertion: brokerAssertion{asserted: true, gateway: "gw", child: "meter-1"}, done: func(err error) { settled <- err }}
	close(in)
	w.Process()

	require.Len(t, failed, 2)
	for _, err := range failed {
		require.ErrorIs(t, err, ErrUplinkNotAsserted)
	}
	require.Zero(t, published)
	require.Empty(t, plans.calls, "the decoder was never asked")
	require.NoError(t, <-settled)
	require.NoError(t, <-settled)
}

// 🔴 A DEVICE-MANAGEMENT THAT STOPS ANSWERING MUST NOT STOP ORDINARY TELEMETRY. The plan question is
// never asked on a decode worker (a few, shared by every tenant): a cold payload waits in its own
// slot, and telemetry beside it is decoded and published as usual.
func TestAHangingPlanBackendDoesNotDelayOrdinaryEvents(t *testing.T) {
	u := newUplinkHarness(t, uplinkFramePlan())
	hang := make(chan struct{})
	u.plans.mu.Lock()
	u.plans.hang = hang
	u.plans.mu.Unlock()
	t.Cleanup(func() { close(hang) })

	// More cold payloads than there are decode workers (2 in the harness), all stuck on the backend.
	var stuck []*recordingAck
	for i := 0; i < 6; i++ {
		ack := &recordingAck{}
		stuck = append(stuck, ack)
		u.source.handle(capturedMsgAt(fmt.Sprintf("inst-1.acme.devices.m-%d.uplink", i), "\x01", uint64(10+i), time.Now(), ack))
	}
	// Ordinary telemetry beside them is published and acked promptly.
	ack := &recordingAck{}
	u.source.handle(capturedMsgAt(captureSubject, validEvent, 1, time.Now(), ack))
	require.True(t, ack.settled(t), "telemetry was held behind a hung plan lookup")
	published, _, _, _ := u.counts()
	require.Equal(t, 1, published)
	for _, a := range stuck {
		require.Zero(t, a.counts(), "a payload still waiting for its plan is not acked")
	}
}

// With the lookup's deadline passed, the payload is left for redelivery and counted, never recorded
// against the device.
func TestAPlanLookupThatTimesOutLeavesThePayloadForRedelivery(t *testing.T) {
	saved := planLookupTimeout
	planLookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { planLookupTimeout = saved })
	u := newUplinkHarness(t, uplinkFramePlan())
	hang := make(chan struct{})
	u.plans.mu.Lock()
	u.plans.hang = hang
	u.plans.mu.Unlock()
	t.Cleanup(func() { close(hang) })

	ack := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, "\x01", 1, time.Now(), ack))
	require.Eventually(t, func() bool { return len(u.resultList()) == 1 }, 3*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{DeviceUplinkPlanUnavail}, u.resultList())
	require.Zero(t, ack.counts())
	_, failed, _, _ := u.counts()
	require.Zero(t, failed)
}

// A worker never waits for a slot: with every one taken by lookups that are stuck, the next payload
// is left unacked for the broker to offer again.
func TestAFullSetOfSlotsLeavesThePayloadUnackedInsteadOfBlockingAWorker(t *testing.T) {
	u := newUplinkHarness(t, uplinkFramePlan())
	hang := make(chan struct{})
	u.plans.mu.Lock()
	u.plans.hang = hang
	u.plans.mu.Unlock()
	t.Cleanup(func() { close(hang) })

	for i := 0; i < uplinkPublishSlots; i++ {
		u.source.handle(capturedMsgAt(fmt.Sprintf("inst-1.acme.devices.m-%d.uplink", i), "\x01", uint64(100+i), time.Now(), &recordingAck{}))
	}
	require.Eventually(t, func() bool { return len(u.source.uplinkRun.slots) == uplinkPublishSlots },
		3*time.Second, time.Millisecond, "every slot should be held by a parked lookup")

	ack := &recordingAck{}
	u.source.handle(capturedMsgAt("inst-1.acme.devices.one-more.uplink", "\x01", 999, time.Now(), ack))
	require.Eventually(t, func() bool {
		for _, r := range u.resultList() {
			if r == DeviceUplinkBackpressure {
				return true
			}
		}
		return false
	}, 3*time.Second, 5*time.Millisecond)
	require.Zero(t, ack.counts(), "left unacked: the broker offers it again")
}

// A plan the cache holds past its TTL is served at once (the worker does not wait for
// device-management) and refreshed in the background.
func TestAStalePlanIsServedAtOnceAndRefreshedInTheBackground(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	plans := &brokerPlanFake{plan: uplinkFramePlan()}
	u := &uplinkHarness{plans: plans}
	u.captureHarness = newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) {
		require.NoError(t, s.UseDeviceUplinks(&DeviceUplinkRoute{
			Plans: integration.NewDeviceDecoders(plans, integration.DeviceDecoderConfig{Now: clock, TTL: time.Minute}),
			Publish: func(source, tenant string, e *model.UnresolvedEvent, p interface{}, dedupID string) error {
				u.mu.Lock()
				defer u.mu.Unlock()
				u.out = append(u.out, uplinkOut{source, tenant, e, p.(*model.UnresolvedIntegrationUplinkPayload), dedupID})
				return nil
			},
		}))
	})

	first := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, "\x01", 1, time.Now(), first))
	require.True(t, first.settled(t))

	// The TTL passes and device-management stops answering.
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	hang := make(chan struct{})
	plans.mu.Lock()
	plans.hang = hang
	plans.mu.Unlock()
	t.Cleanup(func() { close(hang) })

	second := &recordingAck{}
	u.source.handle(capturedMsgAt(uplinkSubject, "\x02", 2, time.Now(), second))
	require.True(t, second.settled(t), "a stale plan must be served without waiting for device-management")
	require.Len(t, u.published(), 2)
	// ...and the refresh was asked for, in the background.
	require.Eventually(t, func() bool { return len(u.source.uplinkRun.refreshing) == 1 }, 2*time.Second, time.Millisecond)
}

// A failed-decode stream that stops answering must not stop telemetry either: a payload that can
// never be carried is recorded from a slot, not from a decode worker.
func TestASlowFailedDecodeStreamDoesNotDelayOrdinaryEvents(t *testing.T) {
	hold := make(chan struct{})
	u := &uplinkHarness{plans: &brokerPlanFake{plan: uplinkFramePlan()}}
	u.captureHarness = newCaptureHarnessWith(t, func(s *GatewayJetStreamSource) {
		require.NoError(t, s.UseDeviceUplinks(&DeviceUplinkRoute{
			Plans: integration.NewDeviceDecoders(u.plans, integration.DeviceDecoderConfig{}),
			Publish: func(string, string, *model.UnresolvedEvent, interface{}, string) error {
				return nil
			},
		}))
	})
	// The harness' failed hook reads failedHold when it is called, so it can be set after start.
	u.captureHarness.mu.Lock()
	u.captureHarness.failedHold = hold
	u.captureHarness.mu.Unlock()
	t.Cleanup(func() { close(hold) })

	// More over-cap payloads than there are workers, each needing a failed-decode record.
	big := string(make([]byte, maxBodyBytes+1)) // refused on the worker, before any lookup
	for i := 0; i < 6; i++ {
		u.source.handle(capturedMsgAt(fmt.Sprintf("inst-1.acme.devices.m-%d.uplink", i), big, uint64(10+i), time.Now(), &recordingAck{}))
	}
	// Each holds a slot on the failed-decode publish; ordinary telemetry must still flow.
	ack := &recordingAck{}
	u.source.handle(capturedMsgAt(captureSubject, validEvent, 1, time.Now(), ack))
	require.True(t, ack.settled(t), "telemetry was held behind a slow failed-decode publish")
}
