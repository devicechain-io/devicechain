// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedPublisher is a Publisher whose outcomes a test decides. Each Submit is recorded;
// the nth is answered with results[n] (nil past the end of the script), at once, unless
// hold is set, in which case its done is kept for the test to call.
type scriptedPublisher struct {
	mu      sync.Mutex
	calls   [][]messaging.Message
	results []error
	hold    bool
	pending []func(error)
	settle  func(ctx context.Context) error
	settled int
}

func (p *scriptedPublisher) Submit(_ context.Context, msgs []messaging.Message, done func(error)) {
	p.mu.Lock()
	n := len(p.calls)
	p.calls = append(p.calls, msgs)
	var res error
	if n < len(p.results) {
		res = p.results[n]
	}
	hold := p.hold
	if hold {
		p.pending = append(p.pending, done)
	}
	p.mu.Unlock()
	if !hold {
		done(res)
	}
}

func (p *scriptedPublisher) Settle(ctx context.Context) error {
	p.mu.Lock()
	p.settled++
	f := p.settle
	p.mu.Unlock()
	if f != nil {
		return f(ctx)
	}
	return nil
}

func (p *scriptedPublisher) submitted() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *scriptedPublisher) call(i int) []messaging.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[i]
}

func (p *scriptedPublisher) settles() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settled
}

// captureLog collects what is logged for the remainder of t.
func captureLog(t *testing.T) *dctest.LogSink {
	t.Helper()
	return logSink.Capture(t)
}

// quietClient is a Client over the fake ingester whose connection is a fake that never
// raises its connect handler, so Connect starts the term's goroutines (including the one that
// resubmits failed publishes) and nothing else.
func quietClient(t *testing.T, ing SampleIngester, pub Publisher, failures prometheus.Counter) *Client {
	t.Helper()
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h", AutoRegister: true, DeviceTypeToken: "sp-node"}, Broker{}, ing, admitAllSamples{}, fixedNow,
		Metrics{IngestFailures: failures})
	c.SetPublisher(pub)
	c.newClient = func(*mqtt.ClientOptions) mqtt.Client { return newFakeClient() }
	c.Connect()
	t.Cleanup(c.Stop)
	return c
}

func oneSample() []Sample { return []Sample{{Name: "t", Value: 1, Time: fixedNow().UnixMilli()}} }

func newFailures() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: "ingest_failures"})
}

// 🔴 A RESUBMISSION IS THE SAME MESSAGE, which is what makes it idempotent. Dedup ids are
// content-derived, but a message rebuilt from the samples is stamped anew (a retry has its own
// receipt instant), so the Client resubmits the messages it BUILT, not a rebuild. The first
// attempt fails, the second succeeds, and both must carry the same key, bytes and dedup id.
// Driven through the real Ingester, Registrar and Emitter, so the ids are the shipped ones.
func TestAResubmittedPublicationIsByteIdentical(t *testing.T) {
	// A clock that moves on every read: a rebuild would differ in its stamped times, and so in its bytes.
	var tick int64
	var tmu sync.Mutex
	moving := func() time.Time {
		tmu.Lock()
		defer tmu.Unlock()
		tick++
		return time.Unix(1_700_000_000, 0).Add(time.Duration(tick) * time.Second)
	}
	ing := NewIngester(NewRegistrar(fakeCreateGQL{}, "url", nil), NewEmitter(failingWriter{}, moving), IngestMetrics{})
	pub := &scriptedPublisher{results: []error{errors.New("no PubAck")}}
	failures := newFailures()
	c := quietClient(t, ing, pub, failures)

	c.ingestSamples("plant-a/node-3", oneSample())
	require.Eventually(t, func() bool { return pub.submitted() == 2 }, 10*time.Second, 5*time.Millisecond,
		"a failed publish was not resubmitted")

	first, second := pub.call(0), pub.call(1)
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	assert.NotEmpty(t, first[0].DedupID, "a resubmission is only idempotent if the message carries a dedup id")
	assert.Equal(t, first[0].DedupID, second[0].DedupID)
	assert.Equal(t, first[0].Key, second[0].Key)
	assert.True(t, bytes.Equal(first[0].Value, second[0].Value),
		"the resubmission was rebuilt from the samples, so it carries different stamped times")
	assert.Zero(t, testutil.ToFloat64(failures), "a publish that succeeded on its second attempt is not a loss")
}

// A publish that keeps failing is resubmitted within the same budget the in-handler retry had
// (ingestMaxAttempts in all), then ABANDONED AND COUNTED, with a log line that names what was
// lost. It is never a silent drop and never an endless retry.
func TestAFailingPublishIsRetriedWithinTheBudgetThenCountedAndLogged(t *testing.T) {
	logs := captureLog(t)
	pub := &scriptedPublisher{results: []error{errors.New("down"), errors.New("down"), errors.New("down"), errors.New("down")}}
	failures := newFailures()
	c := quietClient(t, &fakeIngester{}, pub, failures)

	c.ingestSamples("g/n", oneSample())
	require.Eventually(t, func() bool { return testutil.ToFloat64(failures) == 1 }, 15*time.Second, 5*time.Millisecond,
		"an abandoned publish was not counted")
	assert.Equal(t, ingestMaxAttempts, pub.submitted(), "attempts spent on one publication")
	time.Sleep(2 * ingestRetryBackoff)
	assert.Equal(t, ingestMaxAttempts, pub.submitted(), "an abandoned publication was resubmitted again")
	out := logs.String()
	assert.Contains(t, out, "Dropping Sparkplug samples")
	assert.Contains(t, out, "ingest retry budget exhausted")
	assert.Contains(t, out, `"externalId":"g/n"`)
}

// The ingest counters record what is STORED. The old path counted after a synchronous write
// returned; the publish is now acknowledged later, so counting at submit would report a
// reading as emitted that the stream then never took.
func TestMeasurementsAreCountedWhenTheStreamAcknowledgesThemNotWhenSubmitted(t *testing.T) {
	emitted := prometheus.NewCounter(prometheus.CounterOpts{Name: "emitted"})
	presence := prometheus.NewCounter(prometheus.CounterOpts{Name: "presence"})
	ing := NewIngester(NewRegistrar(fakeCreateGQL{}, "url", nil), NewEmitter(failingWriter{}, fixedNow),
		IngestMetrics{MeasurementsEmitted: emitted, PresenceEmitted: presence})
	pub := &scriptedPublisher{hold: true}
	c := quietClient(t, ing, pub, newFailures())

	c.ingestSamples("plant-a/node-3", oneSample())
	c.ingestPresence([]PresenceEvent{{ExternalId: "plant-a/node-3", Connected: true, SessionId: 5, OccurredAt: fixedNow()}})
	require.Equal(t, 2, pub.submitted())
	assert.Zero(t, testutil.ToFloat64(emitted), "counted as emitted before the stream acknowledged it")
	assert.Zero(t, testutil.ToFloat64(presence), "counted as emitted before the stream acknowledged it")

	pub.mu.Lock()
	pending := append([]func(error){}, pub.pending...)
	pub.mu.Unlock()
	for _, done := range pending {
		done(nil)
	}
	assert.Equal(t, 1.0, testutil.ToFloat64(emitted))
	assert.Equal(t, 1.0, testutil.ToFloat64(presence))
}

// 🔴 A RETRY THAT IS STILL WAITING WHEN THE TERM ENDS IS NOT RESUBMITTED, and it is not lost
// quietly: it is counted. Resubmitting after Stop would publish after the lease is released.
func TestStopAbandonsAndCountsAPublishWaitingToBeRetried(t *testing.T) {
	logs := captureLog(t)
	pub := &scriptedPublisher{results: []error{errors.New("down"), nil}}
	failures := newFailures()
	c := quietClient(t, &fakeIngester{}, pub, failures)

	c.ingestSamples("g/n", oneSample())
	require.Equal(t, 1, pub.submitted())
	stopWithin(t, c, 10*time.Second) // well inside the retry backoff

	assert.Equal(t, 1, pub.submitted(), "a retry was resubmitted after the term ended")
	assert.Equal(t, 1.0, testutil.ToFloat64(failures), "the abandoned retry was not counted")
	assert.Contains(t, logs.String(), "connection shutting down")
	assert.Equal(t, 1, pub.settles(), "Stop must drain the publisher before the lease can be released")
}

// 🔴 NOTHING IS PUBLISHED AFTER STOP: a handler that is still running when Stop returns finds
// the Client stopped and abandons what it accepted, counted, without touching the publisher.
func TestAHandlerAfterStopPublishesNothing(t *testing.T) {
	pub := &scriptedPublisher{}
	failures := newFailures()
	c := quietClient(t, &fakeIngester{}, pub, failures)
	stopWithin(t, c, 10*time.Second)

	c.ingestSamples("g/n", oneSample())
	c.ingestPresence([]PresenceEvent{{ExternalId: "g/n", Connected: true, SessionId: 1, OccurredAt: fixedNow()}})

	assert.Zero(t, pub.submitted(), "a stopped Client submitted to the publisher")
	assert.Equal(t, 2.0, testutil.ToFloat64(failures), "what a stopped Client abandons must be counted")
}

// Stop waits for the publisher to settle, and the wait is BOUNDED: a broker that never
// answers must not hold the lease for ever. It gives up at its bound and says so.
func TestStopIsBoundedWhenThePublisherNeverSettles(t *testing.T) {
	logs := captureLog(t)
	// The test's own release, so a Settle that ignores its bound is failed by the assertion
	// below instead of hanging the cleanup's Stop as well.
	release := make(chan struct{})
	pub := &scriptedPublisher{settle: func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}}
	c := quietClient(t, &fakeIngester{}, pub, newFailures())
	t.Cleanup(func() { close(release) }) // runs before quietClient's Stop
	c.settleTimeout = 300 * time.Millisecond

	begun := time.Now()
	stopWithin(t, c, 5*time.Second)
	assert.GreaterOrEqual(t, time.Since(begun), 250*time.Millisecond, "it gave up before its bound")
	assert.Contains(t, logs.String(), "could not confirm that everything it published is stored")
}

// A client given an ingest path and no publisher cannot store anything. It must say so, count
// what it accepted as lost, and never report it ingested.
func TestAClientWithNoPublisherAbandonsLoudly(t *testing.T) {
	logs := captureLog(t)
	failures := newFailures()
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, &fakeIngester{}, admitAllSamples{}, fixedNow,
		Metrics{IngestFailures: failures})
	c.ingestSamples("g/n", oneSample())
	assert.Equal(t, 1.0, testutil.ToFloat64(failures))
	assert.Contains(t, logs.String(), "has no publisher")
}

// A Client built without an ingester (decode-only) needs no publisher, and Stop on it settles
// nothing and does not panic.
func TestADecodeOnlyClientNeedsNoPublisher(t *testing.T) {
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, nil, nil, fixedNow, Metrics{})
	c.newClient = func(*mqtt.ClientOptions) mqtt.Client { return newFakeClient() }
	c.Connect()
	stopWithin(t, c, 10*time.Second)
}

// 🔴 A PUBLISH THAT FAILS WHILE STOP IS SETTLING IS COUNTED, NOT RESUBMITTED. The term is
// ending: a retry queued now would never be resubmitted (its goroutine is gone) and, unless the
// queue was closed first, would vanish uncounted. Stop is held in its Settle, the publish then
// fails, and the loss must show on the counter with nothing submitted again.
func TestAPublishThatFailsWhileStopIsSettlingIsCounted(t *testing.T) {
	settling, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	pub := &scriptedPublisher{hold: true, settle: func(ctx context.Context) error {
		once.Do(func() { close(settling) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}}
	failures := newFailures()
	c := quietClient(t, &fakeIngester{}, pub, failures)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	c.ingestSamples("g/n", oneSample())
	require.Equal(t, 1, pub.submitted())
	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()
	select {
	case <-settling:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never began settling")
	}

	pub.mu.Lock()
	done := pub.pending[0]
	pub.mu.Unlock()
	done(errors.New("no PubAck"))
	assert.Equal(t, 1.0, testutil.ToFloat64(failures), "a publish that failed during Stop's settle was lost uncounted")
	close(release)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}
	assert.Equal(t, 1, pub.submitted(), "a failed publish was resubmitted after the term ended")
}
