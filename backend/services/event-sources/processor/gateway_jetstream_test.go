// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// recordingAck stands in for the broker's acknowledgement of a consumed message.
type recordingAck struct {
	mu     sync.Mutex
	acks   int
	naks   int
	ackErr error
}

func (a *recordingAck) Ack() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acks++
	return a.ackErr
}

func (a *recordingAck) Nak() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.naks++
	return nil
}

func (a *recordingAck) counts() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.acks, a.naks
}

// settled blocks until the message has been acked or naked, so a test never
// races the decode worker. Returns false on timeout, which reads as "the message
// was left in limbo" — itself a defect worth reporting distinctly from a wrong
// decision.
func (a *recordingAck) settled(t *testing.T) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if acks, naks := a.counts(); acks+naks > 0 {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// captureHarness wires a GatewayJetStreamSource to controllable callbacks.
type captureHarness struct {
	source     *GatewayJetStreamSource
	publishErr error
	failedErr  error

	mu          sync.Mutex
	published   int
	failedCalls int
	lastSeq     uint64
	received    int
	allowResult bool
	// gate, when set, replaces the flat allowResult so a test can drive the real
	// per-tenant limiter and observe what admission actually depends on. Set it
	// before the first handle call.
	gate RateGate
}

func newCaptureHarness(t *testing.T) *captureHarness {
	t.Helper()
	h := &captureHarness{allowResult: true}
	h.source = NewGatewayJetStreamSource("gw-test", nil, NewJsonDecoder(map[string]string{}),
		func(string, []byte) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.received++
		},
		func(_ string, _ string, _ *model.UnresolvedEvent, _ interface{}, seq uint64) error {
			h.mu.Lock()
			h.published++
			h.lastSeq = seq
			h.mu.Unlock()
			return h.publishErr
		},
		func(string, string, []byte, error) error {
			h.mu.Lock()
			h.failedCalls++
			h.mu.Unlock()
			return h.failedErr
		},
		func(src string, tenant string, sentAt time.Time) bool {
			if h.gate != nil {
				return h.gate(src, tenant, sentAt)
			}
			return h.allowResult
		})

	// Start only the decode side; the read loop is bypassed so tests drive handle
	// directly with the exact message they mean to exercise.
	h.source.messages = make(chan rawMessage, DECODE_CHANNEL_DEPTH)
	for w := 1; w <= 2; w++ {
		worker := NewDecodeWorker(w, h.source.Id, h.source.Decoder, h.source.messages,
			h.source.decoded, h.source.failed)
		go worker.Process()
	}
	t.Cleanup(func() { close(h.source.messages) })
	return h
}

func (h *captureHarness) counts() (published, failed, received int, seq uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.published, h.failedCalls, h.received, h.lastSeq
}

const captureSubject = "inst-1.acme.devices.sensor-001.events"

func capturedMsg(subject string, body string, numDelivered int, seq uint64, ack messaging.Acknowledger) messaging.Message {
	m := messaging.NewConsumedMessage(subject, []byte(body), numDelivered, nil, ack)
	m.StreamSeq = seq
	return m
}

// capturedMsgAt is capturedMsg with the broker append time the message would
// carry — the time the device SENT it, which is what admission is metered on.
func capturedMsgAt(subject string, body string, seq uint64, appendTime time.Time,
	ack messaging.Acknowledger) messaging.Message {
	m := capturedMsg(subject, body, 1, seq, ack)
	m.AppendTime = appendTime
	return m
}

const validEvent = `{"device":"sensor-001","eventType":"Location","occurredTime":"2026-07-20T10:30:00Z","latitude":1,"longitude":2,"elevation":3}`

// ============================ THE ACK TRAP ============================
//
// Every deliberate DROP must acknowledge the capture stream. Under the previous
// MQTT source these were bare returns and that was correct, because paho had
// already auto-acked before the callback ran. Consuming a durable stream inverts
// it: an unacked message is REDELIVERED, so a fail-closed drop that merely
// returns becomes an infinite redelivery loop — the same message, dropped for the
// same reason, forever, with nothing logged because each pass works as designed.
//
// This is the defect reviews walk past, because every one of these returns is
// individually and obviously correct. So it is asserted per drop reason, not once.
func TestEveryDeliberateDropAcknowledgesTheCaptureStream(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		body    string
		setup   func(*captureHarness)
	}{
		{
			name:    "no parseable tenant in the subject",
			subject: "inst-1",
			body:    validEvent,
		},
		{
			name:    "tenant fails the token grammar",
			subject: "inst-1.not a valid tenant.devices.sensor-001.events",
			body:    validEvent,
		},
		{
			name:    "over the tenant ingest rate limit",
			subject: captureSubject,
			body:    validEvent,
			setup:   func(h *captureHarness) { h.allowResult = false },
		},
		{
			name:    "payload cannot be decoded",
			subject: captureSubject,
			body:    `{"this is not": `,
		},
		{
			name:    "payload claims a device the transport did not authorize",
			subject: captureSubject,
			body:    `{"device":"someone-else","eventType":"Location","occurredTime":"2026-07-20T10:30:00Z","latitude":1,"longitude":2,"elevation":3}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newCaptureHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			ack := &recordingAck{}
			h.source.handle(capturedMsg(tc.subject, tc.body, 1, 7, ack))

			require.True(t, ack.settled(t),
				"the message was neither acked nor naked: it stays unacknowledged until AckWait "+
					"expires and is then redelivered, forever")
			acks, naks := ack.counts()
			require.Equal(t, 1, acks,
				"a deliberate drop MUST ack; leaving it unacked redelivers the same message to be "+
					"dropped identically, forever, at fetch rate — the ACK TRAP")
			require.Zero(t, naks, "a drop is terminal and must not request redelivery")
		})
	}
}

// A message that IS wanted must not be acknowledged until the publish durably
// succeeded. Acking earlier is precisely the defect ADR-030 removes: the previous
// source acked from an in-memory channel, so anything in flight at SIGKILL was
// lost after the device had been told it was safe.
func TestASuccessfulPublishIsAckedAndCarriesTheCaptureSequence(t *testing.T) {
	h := newCaptureHarness(t)
	ack := &recordingAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, 1, 4242, ack))

	require.True(t, ack.settled(t), "message never settled")
	acks, naks := ack.counts()
	require.Equal(t, 1, acks)
	require.Zero(t, naks)

	published, failed, received, seq := h.counts()
	require.Equal(t, 1, published)
	require.Zero(t, failed)
	require.Equal(t, 1, received, "a message that clears the gate is counted as inbound exactly once")
	require.Equal(t, uint64(4242), seq,
		"the capture sequence must reach the publisher: it is the dedup id, and a zero one "+
			"silently disables dedup with every other test still green")
}

// A publish failure must NOT be acked — that is the whole point of consuming a
// durable stream — and must ALSO not be naked.
//
// Nak means redeliver IMMEDIATELY, which makes MaxDeliver a millisecond-scale
// fuse: measured against a real broker, all five attempts burn about 1.4ms after
// the first Nak, because each redelivery costs one round trip. The failure this
// path actually sees is a DOWNSTREAM outage — message-independent and lasting
// minutes — so retrying at RTT speed exhausts the fuse INSIDE the outage and then
// strands or mass-dead-letters everything that flowed through it. The capture
// stream's hour-long recovery envelope would never engage at all.
//
// Saying nothing lets AckWait (60s) pace the retry, so MaxDeliver spans five
// minutes instead of milliseconds. This test pins the ABSENCE of a nak, which is
// easy to reintroduce as an apparent improvement.
func TestAPublishFailureIsLeftUnackedForAckWaitPacedRedelivery(t *testing.T) {
	h := newCaptureHarness(t)
	h.publishErr = errors.New("jetstream unavailable")
	ack := &recordingAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, 1, 9, ack))

	// Wait for the publish attempt, then assert nothing was settled either way.
	require.Eventually(t, func() bool { published, _, _, _ := h.counts(); return published == 1 },
		2*time.Second, time.Millisecond, "the publish was never attempted")

	acks, naks := ack.counts()
	require.Zero(t, acks,
		"acking a message whose publish FAILED discards it silently — the exact loss ADR-030 removes")
	require.Zero(t, naks,
		"a nak redelivers in ~one RTT, so MaxDeliver burns through in milliseconds and the "+
			"message is stranded or dead-lettered INSIDE the very outage the capture stream "+
			"exists to ride out; leaving it unacked lets AckWait pace the retry instead")
}

// After MaxDeliver attempts the message is poison, not unlucky. It must be routed
// to failed-decode and acked, or it is redelivered until the broker gives up and
// the diagnostic is lost with it — the drop trap one level down.
func TestAPoisonMessageIsDeadLetteredAndAckedAtMaxDeliver(t *testing.T) {
	h := newCaptureHarness(t)
	h.publishErr = errors.New("permanently unpublishable")
	ack := &recordingAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 11, ack))

	require.True(t, ack.settled(t), "message never settled")
	acks, naks := ack.counts()
	require.Equal(t, 1, acks, "a poison message must be acked once dead-lettered, or it redelivers forever")
	require.Zero(t, naks)

	_, failed, _, _ := h.counts()
	require.Equal(t, 1, failed, "the payload must reach failed-decode before being acked, or it is lost silently")
}

// If even the dead-letter route fails, the message must be left UNACKED. Losing
// it loudly later — when the broker's own MaxDeliver terminates it — beats
// discarding it silently now.
func TestAPoisonMessageIsNotAckedWhenDeadLetteringAlsoFails(t *testing.T) {
	h := newCaptureHarness(t)
	h.publishErr = errors.New("permanently unpublishable")
	h.failedErr = errors.New("failed-decode stream unavailable")
	ack := &recordingAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, messaging.MaxDeliver, 12, ack))

	// Give the worker time to run; the assertion is that NOTHING was acked.
	require.Eventually(t, func() bool { _, failed, _, _ := h.counts(); return failed == 1 },
		2*time.Second, time.Millisecond, "dead-letter route was never attempted")
	acks, _ := ack.counts()
	require.Zero(t, acks,
		"acking after the dead-letter route failed discards the payload with no record anywhere")
}

// A shed message must not be counted as inbound. The gate is metered before the
// arrival counter so a rate-limited message is not counted as both received and
// shed, matching the HTTP path.
func TestAShedMessageIsNotCountedAsInbound(t *testing.T) {
	h := newCaptureHarness(t)
	h.allowResult = false
	ack := &recordingAck{}
	h.source.handle(capturedMsg(captureSubject, validEvent, 1, 3, ack))

	require.True(t, ack.settled(t))
	published, _, received, _ := h.counts()
	require.Zero(t, received, "a shed message must not also be counted as an inbound arrival")
	require.Zero(t, published)
}

// The device token must come from the SUBJECT, since that is the broker-verified
// identity — a device is granted only its own subject — and it is what
// checkDeviceMatchesTransport compares the payload against.
func TestDeviceAndTenantAreDerivedFromTheCaptureSubject(t *testing.T) {
	require.Equal(t, "sensor-001", deviceFromSubject(captureSubject))
	tenant, ok := tenantFromSubject(captureSubject)
	require.True(t, ok)
	require.Equal(t, "acme", tenant)

	// Shapes that are not the device-events subject carry no device identity. "" means
	// "the transport carried none", not "no check needed".
	require.Empty(t, deviceFromSubject("inst-1.acme.inbound-events"))
	require.Empty(t, deviceFromSubject("inst-1.acme.device-commands.sensor-001"))
	require.Empty(t, deviceFromSubject("inst-1.acme.devices.sensor-001.telemetry"))
}

// The command-plane exclusion moved from a DENYLIST into the capture stream's own
// subject filter, and that is a strictly stronger guarantee: a denylist can only
// exclude what it has been told to name, and this one named two internal suffixes
// while staying silent about the rest — which is how the gateway came to
// re-ingest its own traffic and meter tenants twice.
//
// Asserting it against the real filter, rather than against a list, is what makes
// a newly-added internal subject covered automatically.
func TestCaptureStreamFilterCannotMatchInternalSubjects(t *testing.T) {
	filter := messaging.DeviceEventsWildcard("inst-1")
	require.Equal(t, "inst-1.*.devices.*.events", filter)

	for _, subject := range []string{
		"inst-1.acme.device-commands.sensor-001",
		"inst-1.acme.command-responses",
		"inst-1.acme.inbound-events",
		"inst-1.acme.failed-decode",
		"inst-1.acme.resolved-events",
		"inst-1.acme.connector-dispatch.dead",
	} {
		require.False(t, subjectMatchesFilter(filter, subject),
			"internal subject %q matches the capture filter; it would be ingested as device "+
				"telemetry and metered against the tenant", subject)
	}
	require.True(t, subjectMatchesFilter(filter, captureSubject),
		"the capture filter must match real device telemetry")
}

// subjectMatchesFilter implements NATS subject matching for the "*" wildcard,
// which is all the capture filter uses. It is a test-local check of OUR shape
// arithmetic; that the BROKER agrees is the job of the interop pin (slice I6).
func subjectMatchesFilter(filter, subject string) bool {
	f, s := splitSubject(filter), splitSubject(subject)
	if len(f) != len(s) {
		return false
	}
	for i := range f {
		if f[i] != "*" && f[i] != s[i] {
			return false
		}
	}
	return true
}

func splitSubject(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

// The read loop must exit on context cancellation rather than spinning, and must
// not treat cancellation as a message-level failure.
func TestReadLoopStopsOnContextCancellation(t *testing.T) {
	h := newCaptureHarness(t)
	reader := &blockingReader{}
	h.source.reader = reader

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.source.readLoop(ctx, make(chan struct{})); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not stop on context cancellation")
	}
}

// blockingReader returns only when its context is cancelled.
type blockingReader struct{}

func (r *blockingReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}

func (r *blockingReader) HandleResponse(error) {}

// ==================== DRAIN ADMISSION (ADR-030 I4) ====================
//
// These exercise the gate through a REAL core.TenantRateLimiter rather than a
// stubbed allow, because the defect I4 fixes is not in the gate's plumbing — the
// plumbing was always correct — but in WHICH CLOCK the bucket is fed. A stubbed
// gate cannot see the difference, which is exactly why the original design walked
// past it.

// backlogHarness wires the capture source to a real per-tenant limiter at the
// given ceiling, and reports how many messages the gate admitted.
func backlogHarness(t *testing.T, rps float64, burst int) *captureHarness {
	t.Helper()
	h := newCaptureHarness(t)
	limiter := core.NewTenantRateLimiter(func(string) (float64, int) { return rps, burst })
	h.gate = func(_ string, tenant string, sentAt time.Time) bool {
		return limiter.AllowAt(tenant, sentAt)
	}
	return h
}

// drain feeds count messages whose send times are `spacing` apart ending an hour
// ago — a backlog that accumulated during an outage and is now arriving all at
// once — and returns how many the gate admitted.
func drain(t *testing.T, h *captureHarness, count int, spacing time.Duration) int {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < count; i++ {
		h.source.handle(capturedMsgAt(captureSubject, validEvent, uint64(i+1),
			base.Add(time.Duration(i)*spacing), &recordingAck{}))
	}
	_, _, received, _ := h.counts()
	return received
}

// THE I4 PROPERTY: a tenant who sent at or below their ceiling loses nothing to
// the gate, no matter how far behind the drain is.
//
// This is the whole point of the capture stream. The broker PUBACKed every one of
// these messages — it told the device they were safe — so shedding them on the way
// back up would make the durability guarantee false in exactly the situation the
// guarantee exists for. Metered on ARRIVAL all 1000 land in the same instant and
// ~990 are shed; metered on SEND time they replay against the timeline the tenant
// actually produced them on.
//
// Mutation-checked: reverting AllowAt to Allow (metering at now) drops this from
// 1000 to 10 — the burst and nothing else.
func TestRecoveredBacklogAtTheCeilingIsAdmittedInFull(t *testing.T) {
	const count = 1000
	h := backlogHarness(t, 100, 10)

	// Exactly the ceiling: 100/s == one message every 10ms.
	admitted := drain(t, h, count, 10*time.Millisecond)

	require.Equal(t, count, admitted,
		"a compliant tenant's recovered backlog must not be shed; the broker already PUBACKed it")
}

// THE COUNTERWEIGHT, and the reason this slice gives nothing away.
//
// Admitting the backlog would be trivial to achieve by weakening the gate, and a
// test suite that only pinned the property above would be satisfied by exactly
// that mistake. So: the same drain, from a tenant who genuinely sent at 10x their
// ceiling, must still be shed — and shed by the amount the LIVE path would have
// shed, not merely by some amount. A tenant cannot buy extra throughput by
// arranging to be replayed.
func TestRecoveredBacklogAboveTheCeilingIsStillShed(t *testing.T) {
	const count = 1000
	const rps, burst = 100.0, 10

	// 10x the ceiling: 1000/s == one message every 1ms.
	const spacing = time.Millisecond
	drained := drain(t, backlogHarness(t, rps, burst), count, spacing)

	// What the same traffic costs live: the burst, plus the ceiling applied across
	// the span the messages were actually sent over. That span is (count-1)*spacing,
	// not count*spacing — the first message is sent at the start of the window and
	// accrues nothing. Derived rather than hardcoded, so this stays a statement about
	// the ceiling instead of a number tuned until it matched.
	span := time.Duration(count-1) * spacing
	wantLive := burst + int(rps*span.Seconds())
	require.InDelta(t, wantLive, drained, 1,
		"an over-ceiling backlog must be shed to the same volume the live path would admit")
	require.Less(t, drained, count/2, "an over-ceiling tenant must not be admitted wholesale")
}

// A message with no broker metadata (AppendTime zero) must degrade to metering at
// now — the pre-I4 behaviour — rather than to unmetered. Getting this backwards
// would turn a metadata gap into a free pass past the tenant's ceiling.
func TestCapturedMessageWithNoAppendTimeIsStillMetered(t *testing.T) {
	h := backlogHarness(t, 100, 10)

	for i := 0; i < 1000; i++ {
		h.source.handle(capturedMsgAt(captureSubject, validEvent, uint64(i+1),
			time.Time{}, &recordingAck{}))
	}
	_, _, admitted, _ := h.counts()

	require.LessOrEqual(t, admitted, 12,
		"a message with no send time must meter at now, not bypass the ceiling")
}

// A broker whose clock runs fast must not be able to mint tokens by stamping
// messages as sent in the future. The send time is clamped to now, so a future
// append time buys at most the burst that was already available.
func TestFutureAppendTimeCannotMintTokens(t *testing.T) {
	h := backlogHarness(t, 100, 10)

	// ADVANCING through the future, not a constant future time. A constant one
	// accrues nothing after the first call with or without the clamp, so it cannot
	// distinguish the two implementations — this test used one and passed against a
	// build with the clamp deleted.
	future := time.Now().Add(24 * time.Hour)
	for i := 0; i < 1000; i++ {
		h.source.handle(capturedMsgAt(captureSubject, validEvent, uint64(i+1),
			future.Add(time.Duration(i)*time.Second), &recordingAck{}))
	}
	_, _, admitted, _ := h.counts()

	require.LessOrEqual(t, admitted, 12,
		"a future send time must be clamped to now, not accrue a day's worth of tokens")
}
