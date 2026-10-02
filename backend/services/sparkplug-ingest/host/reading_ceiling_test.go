// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// admitAllSamples is a sample limiter with no ceiling, for tests about something else.
type admitAllSamples struct{}

func (admitAllSamples) AdmitSamples(_ string, n int) int { return n }

// countingLimiter counts the charges made through it.
type countingLimiter struct {
	next  SampleLimiter
	calls int
}

func (l *countingLimiter) AdmitSamples(tenant string, n int) int {
	l.calls++
	return l.next.AdmitSamples(tenant, n)
}

// birthOf encodes an NBIRTH (bdSeq 1) declaring n numeric metrics, aliases 1..n.
func birthOf(t *testing.T, n int) []byte {
	t.Helper()
	metrics := []*sppb.Payload_Metric{bdSeqM(1)}
	for i := 1; i <= n; i++ {
		metrics = append(metrics, valuedBirth(fmt.Sprintf("m%d", i), uint64(i), float64(i)))
	}
	enc, err := codec.Encode(&sppb.Payload{Seq: proto.Uint64(0), Metrics: metrics})
	require.NoError(t, err)
	return enc
}

// dataOf encodes an NDATA at seq carrying a value for each of aliases 1..n.
func dataOf(t *testing.T, seq uint64, n int) []byte {
	t.Helper()
	metrics := make([]*sppb.Payload_Metric, 0, n)
	for i := 1; i <= n; i++ {
		metrics = append(metrics, valuedData(uint64(i), float64(i)))
	}
	enc, err := codec.Encode(&sppb.Payload{Seq: proto.Uint64(seq), Metrics: metrics})
	require.NoError(t, err)
	return enc
}

// ingestedSamples is the total samples the fake was handed, and how many calls carried them.
func (f *fakeIngester) ingestedSamples() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, c := range f.calls {
		total += len(c.samples)
	}
	return total
}

// 🔴 A DATA MESSAGE'S READINGS ARE CHARGED AGAINST THE TENANT'S CEILING. Before, Sparkplug had
// no per-tenant ceiling at all: a node's DATA reached the store at whatever rate the broker
// delivered it. At a tier of 20 (floored to one full event, 256), two DATA messages of 300
// readings each store one event's worth and drop the rest, counted on the shed counter and
// not as an ingest failure. The birth that declared the metrics is not charged.
func TestSparkplugReadingsAreChargedAtTheTenantCeiling(t *testing.T) {
	run := func(t *testing.T, limiter SampleLimiter) (*fakeIngester, prometheus.Counter) {
		fake := &fakeIngester{}
		failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "ingest_failures"})
		c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", AutoRegister: true, DeviceTypeToken: "sp"},
			Broker{}, fake, limiter, fixedNow, Metrics{IngestFailures: failures})
		c.onMessage(nil, fakeMessage{topic: "spBv1.0/g/NBIRTH/n", payload: birthOf(t, 300)})
		require.Equal(t, 300, fake.ingestedSamples(), "the birth's values are ingested whole")
		c.onMessage(nil, fakeMessage{topic: "spBv1.0/g/NDATA/n", payload: dataOf(t, 1, 300)})
		c.onMessage(nil, fakeMessage{topic: "spBv1.0/g/NDATA/n", payload: dataOf(t, 2, 300)})
		return fake, failures
	}

	t.Run("metered", func(t *testing.T) {
		shed := prometheus.NewCounter(prometheus.CounterOpts{Name: "samples_shed"})
		fake, failures := run(t, adapter.NewSampleLimiter(core.StaticCeiling(1e-9, 20),
			adapter.IngestLimiterMetrics{SamplesShed: shed}, nil))
		assert.Equal(t, 300+256, fake.ingestedSamples(), "one full event of DATA fits the floored burst")
		assert.Equal(t, float64(600-256), testutil.ToFloat64(shed), "every DATA reading not stored is counted")
		assert.Zero(t, testutil.ToFloat64(failures), "a ceiling shed is not an ingest failure")
	})
	// NEGATIVE CONTROL: the same traffic with no ceiling stores all of it, so the observable
	// above can see the difference.
	t.Run("unmetered", func(t *testing.T) {
		fake, _ := run(t, admitAllSamples{})
		assert.Equal(t, 900, fake.ingestedSamples())
	})
}

// A birth is never shed at the reading ceiling, even when the tenant's bucket is empty: it
// carries the only copy of a report-by-exception node's slow-changing values.
func TestABirthIsNeverShedAtTheReadingCeiling(t *testing.T) {
	limiter := adapter.NewSampleLimiter(core.StaticCeiling(1e-9, 20), adapter.IngestLimiterMetrics{}, nil)
	require.Equal(t, 256, limiter.AdmitSamples("acme", 256), "the bucket is now empty")
	fake := &fakeIngester{}
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", AutoRegister: true, DeviceTypeToken: "sp"},
		Broker{}, fake, limiter, fixedNow, Metrics{})

	c.onMessage(nil, fakeMessage{topic: "spBv1.0/g/NBIRTH/n", payload: birthOf(t, 300)})
	assert.Equal(t, 300, fake.ingestedSamples(), "every value of the birth is ingested")
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/g/NDATA/n", payload: dataOf(t, 1, 10)})
	assert.Equal(t, 300, fake.ingestedSamples(), "DATA on the empty bucket is shed")
}

// The charge is made ONCE per message, before the in-handler retry: a message whose ingest
// fails twice and then succeeds spends its readings once, not three times.
func TestARetriedSparkplugIngestIsChargedOnce(t *testing.T) {
	limiter := &countingLimiter{next: admitAllSamples{}}
	fake := &fakeIngester{}
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", AutoRegister: true, DeviceTypeToken: "sp"},
		Broker{}, fake, limiter, fixedNow, Metrics{})
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/g/NBIRTH/n", payload: birthOf(t, 3)})
	fake.mu.Lock()
	fake.failN = len(fake.calls) + 2
	fake.mu.Unlock()

	c.onMessage(nil, fakeMessage{topic: "spBv1.0/g/NDATA/n", payload: dataOf(t, 1, 3)})
	assert.Equal(t, 4, fake.count(), "the birth, then the DATA tried three times")
	assert.Equal(t, 1, limiter.calls, "the DATA was charged once")
}

// A client that ingests cannot be built without a limiter: it would ingest unmetered.
func TestNewClientRefusesAnIngesterWithoutALimiter(t *testing.T) {
	assert.Panics(t, func() {
		NewClient(config.SparkplugSource{Tenant: "acme"}, Broker{}, &fakeIngester{}, nil, fixedNow, Metrics{})
	})
	assert.NotPanics(t, func() { NewClient(config.SparkplugSource{Tenant: "acme"}, Broker{}, nil, nil, fixedNow, Metrics{}) },
		"a decode-only client has nothing to meter")
}
