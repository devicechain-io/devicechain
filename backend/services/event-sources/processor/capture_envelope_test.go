// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envBroker is inbound-events as a map: a message is stored under its dedup id, and a repeated id
// is answered as a duplicate naming the first.
type envBroker struct {
	mu    sync.Mutex
	order []storedEnv
	byID  map[string]uint64
	bySeq map[uint64]storedEnv
	seq   uint64
	failN map[string]int // ids whose next N publishes (either path) are rejected
	// rejectAll rejects every round publish, whatever its id: a broker that is down for rounds.
	rejectAll atomic.Bool
}

type storedEnv struct {
	tenant string
	msg    messaging.Message
}

func newEnvBroker() *envBroker {
	return &envBroker{byID: map[string]uint64{}, bySeq: map[uint64]storedEnv{}, failN: map[string]int{}}
}

func (b *envBroker) store(ctx context.Context, msg messaging.Message) (messaging.PubAckInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if msg.DedupID != "" {
		if seq, ok := b.byID[msg.DedupID]; ok {
			return messaging.PubAckInfo{Sequence: seq, Duplicate: true}, nil
		}
	}
	tenant, _ := core.TenantFromContext(ctx)
	b.seq++
	if msg.DedupID != "" {
		b.byID[msg.DedupID] = b.seq
	}
	st := storedEnv{tenant: tenant, msg: msg}
	b.order = append(b.order, st)
	b.bySeq[b.seq] = st
	return messaging.PubAckInfo{Sequence: b.seq}, nil
}

// reject reports whether the next publish of id is to be rejected, and uses one rejection up.
func (b *envBroker) reject(id string) bool {
	if b.rejectAll.Load() {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failN[id] > 0 {
		b.failN[id]--
		return true
	}
	return false
}

type envWriter struct{ b *envBroker }

func (w envWriter) Publish(ctx context.Context, msg messaging.Message, done func(error)) {
	_, err := w.b.store(ctx, msg)
	done(err)
}
func (w envWriter) Fail(err error, done func(error)) { done(err) }
func (envWriter) Draining()                          {}
func (envWriter) Close()                             {}
func (w envWriter) PublishAcked(ctx context.Context, msg messaging.Message, done func(messaging.PubAckInfo, error)) {
	if w.b.reject(msg.DedupID) {
		done(messaging.PubAckInfo{}, errors.New("rejected"))
		return
	}
	ack, err := w.b.store(ctx, msg)
	done(ack, err)
}

type envRounds struct{ b *envBroker }

func (r envRounds) Publish(ctx context.Context, msg messaging.Message) (messaging.PubAckInfo, error) {
	if r.b.reject(msg.DedupID) {
		return messaging.PubAckInfo{}, errors.New("rejected")
	}
	return r.b.store(ctx, msg)
}
func (r envRounds) Get(_ context.Context, tenant string, seq uint64) (messaging.StoredMessage, error) {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	st, ok := r.b.bySeq[seq]
	if !ok {
		return messaging.StoredMessage{}, fmt.Errorf("no message %d", seq)
	}
	return messaging.StoredMessage{Sequence: seq, Subject: "i." + tenant + ".inbound-events", Value: st.msg.Value, Headers: st.msg.Headers}, nil
}

// pbDevice is a well-formed protobuf carrying the device token: what a marshalled event is to
// the envelope encoder.
func pbDevice(s string) []byte { return append([]byte{0x0a, byte(len(s))}, s...) }

// envSource is a capture source in envelope mode over the broker above.
func envSource(t *testing.T, b *envBroker) *GatewayJetStreamSource {
	t.Helper()
	es := envSourceUnmanaged(t, b)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.True(t, es.stopPipeline(ctx), "the pipeline did not stop")
	})
	return es
}

// envSourceUnmanaged is envSource whose pipeline the test stops itself.
func envSourceUnmanaged(t *testing.T, b *envBroker) *GatewayJetStreamSource {
	t.Helper()
	es := NewGatewayJetStreamSource(nil, "gw-env", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		func(_ string, tenant string, event *model.UnresolvedEvent, _ interface{}, id string) (context.Context, messaging.Message, bool) {
			return core.WithTenant(context.Background(), tenant), messaging.Message{Value: pbDevice(event.Device), DedupID: id}, true
		},
		func(string, string, []byte, error) error { return nil },
		nil, admitAllReadings, nil)
	es.SetWriter(envWriter{b})
	require.NoError(t, es.UseEnvelopes(CaptureEnvelopes{Rounds: envRounds{b}, Former: messaging.FormerConfig{Linger: 5 * time.Millisecond}}))
	es.startPipeline(3)
	return es
}

// midBucket is a fixed instant well in the past and in the MIDDLE of a 30 s bucket, so the
// messages of one test never straddle a bucket edge and a restarted source sees the same buckets.
func midBucket() time.Time {
	return time.Unix((time.Now().Add(-time.Hour).Unix()/messaging.RoundBucketSeconds)*messaging.RoundBucketSeconds+10, 0)
}

func envBody(device string) string {
	return strings.Replace(validEvent, "sensor-001", device, 1)
}

func envCaptured(tenant string, seq uint64, delivered int, at time.Time, ack messaging.Acknowledger) messaging.Message {
	dev := fmt.Sprintf("%s-%d", tenant, seq)
	m := capturedMsgAt("inst-1."+tenant+".devices."+dev+".events", envBody(dev), seq, at, ack)
	m.NumDelivered = delivered
	return m
}

// membersOf lists the device tokens a stored message carries, in order.
func membersOf(t *testing.T, st storedEnv) []string {
	t.Helper()
	if st.msg.Headers[eventenvelope.HeaderName] == "" {
		return []string{string(st.msg.Value[2:])}
	}
	r, err := eventenvelope.Parse(st.msg.Value)
	require.NoError(t, err)
	require.NoError(t, r.VerifyTenant(st.tenant), "an envelope holds one tenant's events")
	out := make([]string, r.Len())
	for i := range out {
		b, err := r.Member(i)
		require.NoError(t, err)
		out[i] = string(b[2:])
	}
	return out
}

func (b *envBroker) devices(t *testing.T) []string {
	b.mu.Lock()
	order := append([]storedEnv(nil), b.order...)
	b.mu.Unlock()
	var out []string
	for _, st := range order {
		out = append(out, membersOf(t, st)...)
	}
	return out
}

func TestPositionTokenRoundTrips(t *testing.T) {
	key, idx, ok := messaging.RoundKeyOf("acme", 515, time.Unix(1_700_000_010, 0))
	require.True(t, ok)
	for _, redelivered := range []bool{false, true} {
		legacy, pos := splitPosition("acme", positionToken(DedupID("acme", 515), key, redelivered, 77))
		assert.Equal(t, DedupID("acme", 515), legacy)
		require.NotNil(t, pos)
		assert.Equal(t, key, pos.key)
		assert.Equal(t, idx, pos.index)
		assert.Equal(t, redelivered, pos.redelivered)
		assert.EqualValues(t, 77, pos.ref)
	}
	legacy, pos := splitPosition("acme", "")
	assert.Empty(t, legacy)
	assert.Nil(t, pos)
}

// A quiet tenant is never waited for: each event is published at once, in the legacy shape (not
// an envelope of one), under its bucket's group id, and acked after its PubAck.
func TestQuietTenantsCaptureEventsKeepTheLegacyShapeUnderGroupIds(t *testing.T) {
	b := newEnvBroker()
	es := envSource(t, b)
	at := midBucket()
	var acks []*recordingAck
	for seq := uint64(1); seq <= 4; seq++ {
		a := &recordingAck{}
		acks = append(acks, a)
		es.handle(envCaptured("acme", seq, 1, at, a))
		require.True(t, a.settled(t))
		time.Sleep(30 * time.Millisecond)
	}
	require.Len(t, b.order, 4)
	for i, st := range b.order {
		assert.Empty(t, st.msg.Headers[eventenvelope.HeaderName], "a group of one is not an envelope")
		assert.NotEmpty(t, st.msg.Headers[messaging.HeaderMemberIndex])
		assert.True(t, strings.HasPrefix(st.msg.DedupID, "C:acme:"), "event %d: id %q", i, st.msg.DedupID)
	}
	for _, a := range acks {
		assert.Equal(t, 1, a.counts())
	}
}

// A busy tenant is grouped: more events than messages, every event stored exactly once and acked
// exactly once, and no envelope spans two tenants.
func TestBusyTenantsCaptureEventsShareEnvelopesAndNeverMixTenants(t *testing.T) {
	b := newEnvBroker()
	es := envSource(t, b)
	at := midBucket()
	const perTenant = 300
	var acks []*recordingAck
	for seq := uint64(1); seq <= 2*perTenant; seq++ {
		tenant := "acme"
		if seq%2 == 0 {
			tenant = "globex"
		}
		a := &recordingAck{}
		acks = append(acks, a)
		es.handle(envCaptured(tenant, seq, 1, at, a))
		if seq%40 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	for _, a := range acks {
		require.True(t, a.settled(t))
		require.Equal(t, 1, a.counts())
	}
	devices := b.devices(t)
	assert.Len(t, devices, 2*perTenant)
	seen := map[string]int{}
	for _, d := range devices {
		seen[d]++
	}
	assert.Len(t, seen, 2*perTenant, "every event stored once")
	assert.Less(t, len(b.order), 2*perTenant, "a busy tenant's events share messages")
	for _, st := range b.order {
		for _, d := range membersOf(t, st) {
			assert.True(t, strings.HasPrefix(d, st.tenant+"-"), "tenant %s message holds %s", st.tenant, d)
		}
	}
}

// A crash between the PubAck and the capture ack: the same messages come back (delivery 2) to a
// fresh source over the same stream. They derive the same group ids, so nothing is stored a
// second time, and every redelivered message is acked.
func TestRedeliveredCaptureMessagesAreNotStoredTwice(t *testing.T) {
	b := newEnvBroker()
	first := envSource(t, b)
	at := midBucket()
	var firstAcks []*recordingAck
	for seq := uint64(1); seq <= 60; seq++ {
		a := &recordingAck{}
		firstAcks = append(firstAcks, a)
		first.handle(envCaptured("acme", seq, 1, at, a))
		if seq%10 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	for _, a := range firstAcks {
		require.True(t, a.settled(t))
	}
	stored := len(b.devices(t))
	require.Equal(t, 60, stored)

	second := envSource(t, b)
	var again []*recordingAck
	for seq := uint64(1); seq <= 60; seq++ {
		a := &recordingAck{}
		again = append(again, a)
		second.handle(envCaptured("acme", seq, 2, at, a))
	}
	for _, a := range again {
		require.True(t, a.settled(t), "a redelivered message was not acked")
		require.Equal(t, 1, a.counts())
	}
	assert.Len(t, b.devices(t), 60, "the redelivery stored nothing new")
}

// A group's PubAck is lost (the broker rejects the first publish): the sources are NOT acked
// until the round is stored under the same id.
func TestCaptureRoundRejectedOnceIsRetriedAndAckedAfterwards(t *testing.T) {
	b := newEnvBroker()
	key, _, _ := messaging.RoundKeyOf("acme", 1, time.Time{})
	b.failN[messaging.EnvelopeDedupID(captureDedupKind, "acme", key.Source(), 0)] = 1
	es := envSource(t, b)
	a := &recordingAck{}
	es.handle(envCaptured("acme", 1, 1, time.Time{}, a))
	require.True(t, a.settled(t))
	assert.Equal(t, 1, a.counts())
	assert.Len(t, b.devices(t), 1)
}

// The writer must be able to report a PubAck, or the source refuses to start.
func TestCaptureSourceRefusesEnvelopesItCannotSettle(t *testing.T) {
	es := NewGatewayJetStreamSource(nil, "gw", NewJsonDecoder(map[string]string{}), func(string, []byte) {},
		inboundBuildNoop, func(string, string, []byte, error) error { return nil }, nil, admitAllReadings, nil)
	es.SetReader(&blockingReader{})
	es.SetWriter(plainWriter{})
	require.NoError(t, es.UseEnvelopes(CaptureEnvelopes{Rounds: envRounds{newEnvBroker()}}))
	err := es.ExecuteStart(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "envelopes.inbound")

	store := NewGatewayJetStreamSource(nil, "gw", NewJsonDecoder(map[string]string{}), func(string, []byte) {},
		inboundBuildNoop, func(string, string, []byte, error) error { return nil }, nil, admitAllReadings, nil)
	store.UseMessageStore()
	assert.Error(t, store.UseEnvelopes(CaptureEnvelopes{Rounds: envRounds{newEnvBroker()}}), "the store source keeps its per-event ids")
}

func inboundBuildNoop(string, string, *model.UnresolvedEvent, interface{}, string) (context.Context, messaging.Message, bool) {
	return nil, messaging.Message{}, false
}

type plainWriter struct{}

func (plainWriter) Publish(context.Context, messaging.Message, func(error)) {}
func (plainWriter) Fail(error, func(error))                                 {}
func (plainWriter) Draining()                                               {}
func (plainWriter) Close()                                                  {}

// ---- HTTP group commit ------------------------------------------------------------------

func newHTTPEnv(t *testing.T, publish func(ctx context.Context, source string, msg messaging.Message) error) *HTTPEnvelopes {
	t.Helper()
	h, err := NewHTTPEnvelopes(messaging.FormerConfig{Linger: 5 * time.Millisecond}, nil, publish, nil)
	require.NoError(t, err)
	t.Cleanup(h.Stop)
	return h
}

// A quiet tenant's request closes in Former.Add and is published alone with legacy bytes.
// The former records exactly zero wait when Add closes at the first member's arrival time;
// timer expiry records a positive wait. Checking these observations is deterministic even
// when scheduling or publishing is slow, unlike measuring request latency.
func TestHTTPQuietTenantRequestIsPublishedAloneImmediately(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "event-sources"}
	ms.UseMetricsRegistry(reg)
	metrics := messaging.NewEnvelopeMetrics(ms, "inbound")
	var got []messaging.Message
	var mu sync.Mutex
	h, err := NewHTTPEnvelopes(messaging.FormerConfig{Linger: 5 * time.Millisecond}, metrics, func(_ context.Context, _ string, msg messaging.Message) error {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
		return nil
	}, nil)
	require.NoError(t, err)
	t.Cleanup(h.Stop)
	for i := 0; i < 3; i++ {
		require.NoError(t, h.Publish(core.WithTenant(context.Background(), "acme"), "http", "acme",
			messaging.Message{Value: pbDevice("d"), Key: []byte("d")}))
		time.Sleep(20 * time.Millisecond)
	}
	families, err := reg.Gather()
	require.NoError(t, err)
	histograms := map[string]*dto.Histogram{}
	for _, family := range families {
		if family.GetType() != dto.MetricType_HISTOGRAM {
			continue
		}
		require.Len(t, family.Metric, 1)
		metric := family.Metric[0]
		require.Len(t, metric.Label, 1)
		assert.Equal(t, "hop", metric.Label[0].GetName())
		assert.Equal(t, "http", metric.Label[0].GetValue())
		histograms[family.GetName()] = metric.GetHistogram()
	}
	members := histograms["devicechain_eventsources_inbound_envelope_members"]
	require.NotNil(t, members)
	assert.EqualValues(t, 3, members.GetSampleCount(), "three batches closed")
	assert.Equal(t, float64(3), members.GetSampleSum(), "each batch had one member")
	waited := histograms["devicechain_eventsources_inbound_envelope_linger_seconds"]
	require.NotNil(t, waited)
	assert.EqualValues(t, 3, waited.GetSampleCount(), "every batch reported its wait")
	assert.Zero(t, waited.GetSampleSum(), "quiet batches must close in Add, not on timer expiry")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 3)
	for _, m := range got {
		assert.Empty(t, m.DedupID)
		assert.Empty(t, m.Headers[eventenvelope.HeaderName])
	}
}

// A busy tenant's requests share envelopes, and a request is answered only after the publish of
// the envelope that holds its event: while the publish is blocked nobody is answered, and when it
// fails every member of that envelope is refused.
func TestHTTPBusyTenantGroupCommitAnswersAfterTheEnvelopePubAck(t *testing.T) {
	var published atomic.Int32
	var envelopes atomic.Int32
	release := make(chan struct{})
	var fail atomic.Bool
	var gated atomic.Bool
	h := newHTTPEnv(t, func(_ context.Context, _ string, msg messaging.Message) error {
		published.Add(1)
		if msg.Headers[eventenvelope.HeaderName] != "" {
			envelopes.Add(1)
			assert.True(t, strings.HasPrefix(msg.DedupID, "H:acme:"), "id %q", msg.DedupID)
		}
		if gated.Load() {
			<-release
			if fail.Load() {
				return errors.New("unavailable")
			}
		}
		return nil
	})
	// Learn a fast rate so groups form.
	for i := 0; i < 20; i++ {
		_ = h.Publish(core.WithTenant(context.Background(), "acme"), "http", "acme", messaging.Message{Value: pbDevice("warm")})
	}
	gated.Store(true)

	const n = 6
	errs := make(chan error, n)
	answered := atomic.Int32{}
	for i := 0; i < n; i++ {
		go func() {
			err := h.Publish(core.WithTenant(context.Background(), "acme"), "http", "acme", messaging.Message{Value: pbDevice("d")})
			answered.Add(1)
			errs <- err
		}()
	}
	require.Eventually(t, func() bool { return envelopes.Load() >= 1 }, 2*time.Second, time.Millisecond, "no envelope was published")
	time.Sleep(10 * time.Millisecond)
	assert.Zero(t, answered.Load(), "a request was answered before its envelope's PubAck")
	fail.Store(true)
	close(release)
	var failed int
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			if err != nil {
				failed++
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a request never got its answer")
		}
	}
	assert.Positive(t, failed, "the members of a failed envelope are refused")
}

// ---- beats, stop, ordering ---------------------------------------------------------------

// beatAck is a capture ack that also counts the InProgress the source sends the broker.
type beatAck struct {
	recordingAck
	progress atomic.Int32
}

func (a *beatAck) InProgress() error { a.progress.Add(1); return nil }

// While a bucket's rounds are in retry, the captured messages are kept alive at the capture broker
// (InProgress) so their AckWait does not expire and redeliver them under the walk. A redelivery
// that nevertheless arrives meanwhile does not get its event stored twice, and every source is
// acked once when the rounds finally land.
func TestCaptureMembersAreBeatenWhileTheirRoundsRetryAndARedeliveryStoresNothingTwice(t *testing.T) {
	b := newEnvBroker()
	at := midBucket()
	key, _, _ := messaging.RoundKeyOf("acme", 1, at)
	r0 := messaging.EnvelopeDedupID(captureDedupKind, "acme", key.Source(), 0)
	r1 := messaging.EnvelopeDedupID(captureDedupKind, "acme", key.Source(), 1)
	b.mu.Lock()
	b.failN[r0], b.failN[r1] = 3, 3
	b.mu.Unlock()

	es := envSource(t, b)
	es.ce.engine.SetBeatEvery(time.Millisecond)
	mk := func(seq uint64, delivered int) (messaging.Message, *beatAck) {
		a := &beatAck{}
		return envCaptured("acme", seq, delivered, at, a), a
	}
	m1, a1 := mk(1, 1)
	es.handle(m1)
	time.Sleep(40 * time.Millisecond) // a separate group: round 1 (quiet tenant, no company)
	m2, a2 := mk(2, 1)
	es.handle(m2)
	time.Sleep(50 * time.Millisecond)
	// seq 1 is redelivered while rounds 0 and 1 are both still in retry.
	m1b, a1b := mk(1, 2)
	es.handle(m1b)

	for _, a := range []*beatAck{a1, a2, a1b} {
		require.True(t, a.settled(t) || func() bool {
			time.Sleep(8 * time.Second)
			return a.counts() > 0
		}(), "a source was never acked once the rounds landed")
	}
	assert.Positive(t, a1.progress.Load(), "the walk kept the captured message alive")
	assert.Positive(t, a2.progress.Load(), "the walk kept the captured message alive")
	assert.Equal(t, 1, a1.counts())
	assert.Equal(t, 1, a2.counts())
	seen := map[string]int{}
	for _, d := range b.devices(t) {
		seen[d]++
	}
	assert.Equal(t, map[string]int{"acme-1": 1, "acme-2": 1}, seen, "no event stored twice")
}

// Stopping the pipeline ends the settlement walks when draining begins: stop returns promptly even
// with a round the broker keeps rejecting, the source is left UNACKED for the next pod, and no
// settlement fires after stop has returned.
func TestStoppingThePipelineEndsTheWalksAndLeavesTheSourceUnacked(t *testing.T) {
	b := newEnvBroker()
	at := midBucket()
	key, _, _ := messaging.RoundKeyOf("acme", 1, at)
	b.mu.Lock()
	b.failN[messaging.EnvelopeDedupID(captureDedupKind, "acme", key.Source(), 0)] = 1 << 30
	b.mu.Unlock()
	es := envSourceUnmanaged(t, b)
	a := &beatAck{}
	es.handle(envCaptured("acme", 1, 1, at, a))
	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.failN[messaging.EnvelopeDedupID(captureDedupKind, "acme", key.Source(), 0)] < 1<<30-1
	}, 2*time.Second, time.Millisecond, "the round was never attempted")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	require.True(t, es.stopPipeline(ctx), "stop did not finish: the walk was still retrying")
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Zero(t, a.counts(), "the source must be left unacked")
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, a.counts(), "nothing settled after stop returned")
	assert.Empty(t, b.devices(t))
}

// A message that cannot be grouped must not overtake an earlier event of its tenant that is still
// waiting for company: the tenant's open group is flushed first.
func TestAnUngroupableMessageFlushesItsTenantsOpenGroupFirst(t *testing.T) {
	b := newEnvBroker()
	es := NewGatewayJetStreamSource(nil, "gw", NewJsonDecoder(map[string]string{}), func(string, []byte) {},
		inboundBuildNoop, func(string, string, []byte, error) error { return nil }, nil, admitAllReadings, nil)
	es.SetWriter(envWriter{b})
	require.NoError(t, es.UseEnvelopes(CaptureEnvelopes{Rounds: envRounds{b}, Former: messaging.FormerConfig{Linger: 10 * time.Millisecond}}))
	ce, err := es.newCaptureEngine()
	require.NoError(t, err)
	defer ce.cancel()
	publishes := make(chan pendingPublish)
	done := make(chan struct{})
	go es.envelopeSubmitLoop(ce, publishes, done)

	at := midBucket()
	ctx := core.WithTenant(context.Background(), "acme")
	positioned := func(seq uint64, dev string) pendingPublish {
		key, idx, _ := messaging.RoundKeyOf("acme", seq, at)
		return pendingPublish{ctx: ctx, msg: messaging.Message{Value: pbDevice(dev)}, done: func(error) {},
			pos: &capturePos{key: key, index: idx}}
	}
	for seq := uint64(1); seq <= 30; seq++ { // learn a fast rate: the next event will wait
		publishes <- positioned(seq, fmt.Sprintf("warm-%d", seq))
	}
	publishes <- positioned(31, "first")
	publishes <- pendingPublish{ctx: ctx, msg: messaging.Message{Value: pbDevice("bypass")}, done: func(error) {}} // no position
	close(publishes)
	<-done
	ce.engine.Wait()

	devices := b.devices(t)
	first, bypass := -1, -1
	for i, d := range devices {
		switch d {
		case "first":
			first = i
		case "bypass":
			bypass = i
		}
	}
	require.NotEqual(t, -1, first)
	require.NotEqual(t, -1, bypass)
	assert.Less(t, first, bypass, "the bypassing message overtook an earlier event of its tenant: %v", devices)
}

// ---- HTTP failure answers ----------------------------------------------------------------

// A group refused under backpressure refuses EVERY request in it, and each is counted once with its
// own source: three requests, three refusals, never one per envelope.
//
// The group is handed to flush already formed: whether three concurrent requests share a group is
// the former's call (learned arrival rate, a linger capped at 10 ms), so a test that sleeps and
// hopes sometimes gets two groups on a loaded runner.
func TestHTTPGroupRefusedUnderBackpressureRefusesAndCountsEveryRequest(t *testing.T) {
	var mu sync.Mutex
	counted := map[string]int{}
	var published atomic.Int32
	h, err := NewHTTPEnvelopes(messaging.FormerConfig{Linger: 5 * time.Millisecond}, nil,
		func(_ context.Context, _ string, _ messaging.Message) error {
			published.Add(1)
			return &messaging.BackpressureError{}
		}, func(source string) { mu.Lock(); counted[source]++; mu.Unlock() })
	require.NoError(t, err)
	defer h.Stop()
	batch := messaging.FormedBatch{Tenant: "acme"}
	pend := map[string]*httpPending{}
	for _, src := range []string{"http-a", "http-b", "http-c"} {
		p := &httpPending{
			ctx:    core.WithTenant(context.Background(), "acme"),
			msg:    messaging.Message{Value: pbDevice("d")},
			source: src,
			done:   make(chan error, 1),
		}
		pend[src] = p
		batch.Members = append(batch.Members, messaging.FormedMember{Bytes: p.msg.Value, Token: p})
	}
	h.flush(batch)
	assert.EqualValues(t, 1, published.Load(), "the group is one envelope, published once")
	for src, p := range pend {
		select {
		case err := <-p.done:
			require.Error(t, err, src)
			assert.ErrorIs(t, err, messaging.ErrStreamBackpressure, src)
		default:
			t.Fatalf("request %s was never answered", src)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	total := 0
	for src, n := range counted {
		assert.LessOrEqual(t, n, 1, "source %s counted %d times", src, n)
		total += n
	}
	assert.Equal(t, 3, total, "one refusal per request: %v", counted)
}

// A group the envelope encoder refuses (a token that is not UTF-8) is published event by event, so
// each request gets its own answer: the good ones succeed, nothing is lost to the bad neighbour.
//
// The group is handed to flush already formed. Whether three concurrent requests land in ONE group
// is the former's call, made from the tenant's learned arrival rate and a linger capped at
// 10 ms, so a test that relies on goroutine timing to get one group sometimes gets two (a lone
// request, then a pair of good ones that envelope fine) on a loaded runner. The former has its own
// tests; this one is about what flush does with a group the encoder refuses.
func TestHTTPGroupTheEncoderRefusesFallsBackToSinglesWithTheirOwnAnswers(t *testing.T) {
	var mu sync.Mutex
	var singles, envs int
	h := newHTTPEnv(t, func(_ context.Context, _ string, msg messaging.Message) error {
		mu.Lock()
		defer mu.Unlock()
		if msg.Headers[eventenvelope.HeaderName] != "" {
			envs++
		} else {
			singles++
		}
		return nil
	})
	bodies := [][]byte{pbDevice("good-1"), {0xff, 0xfe}, pbDevice("good-2")}
	batch := messaging.FormedBatch{Tenant: "acme"}
	pend := make([]*httpPending, len(bodies))
	for i, v := range bodies {
		pend[i] = &httpPending{
			ctx:    core.WithTenant(context.Background(), "acme"),
			msg:    messaging.Message{Value: v},
			source: "http",
			done:   make(chan error, 1),
		}
		batch.Members = append(batch.Members, messaging.FormedMember{Bytes: v, Token: pend[i]})
	}
	h.flush(batch)
	for i, p := range pend {
		select {
		case err := <-p.done:
			require.NoError(t, err, "request %d", i)
		default:
			t.Fatalf("request %d was never answered", i)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 0, envs, "an unencodable group is never published as an envelope")
	assert.Equal(t, 3, singles, "each request was published on its own")
}

// A FULL WALK POOL MUST NOT WEDGE THE SHUTDOWN. Every bucket's round is rejected, so each holds a
// walker retrying, and the pool (16) fills. The next walk blocks the writer's settle callback in
// startWalk, which backs the submitter, the decode workers and finally the read loop's hand-off
// up behind it. ExecuteStop used to wait for that read loop BEFORE it cancelled the walks that
// held the pool, so the stop sat out its whole deadline; it must cancel them first.
func TestStopWithAFullWalkPoolDoesNotWaitOutTheDeadline(t *testing.T) {
	b := newEnvBroker()
	b.rejectAll.Store(true)
	es := envSourceUnmanaged(t, b)
	t.Cleanup(func() { es.ce.cancel() })

	// A stand-in read loop: hands the source far more buckets than the pool holds, then returns.
	es.drained = make(chan struct{})
	base := midBucket()
	go func() {
		defer close(es.drained)
		for i := 0; i < 600; i++ {
			at := base.Add(time.Duration(i) * messaging.RoundBucketSeconds * time.Second)
			es.handle(envCaptured("acme", uint64(i+1), 1, at, &recordingAck{}))
		}
	}()
	// Wait until the hand-off is wedged: the loop has not finished and nothing has progressed.
	time.Sleep(500 * time.Millisecond)
	select {
	case <-es.drained:
		t.Fatal("setup: the read loop finished, so the pool never filled")
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, es.ExecuteStop(ctx))
	assert.Less(t, time.Since(start), 2*time.Second, "the stop waited on a read loop wedged behind a full walk pool")
	select {
	case <-es.workersDone:
	default:
		t.Fatal("the stop gave up before the decode workers finished")
	}
}
