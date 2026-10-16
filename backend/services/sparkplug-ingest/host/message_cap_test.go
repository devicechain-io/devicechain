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

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// The per-message cap, restated as a number rather than read from MaxMetricsPerMessage, so
// a change to the constant is a change this file has to agree with (sample_test.go pins
// that the two agree).
const wantMetricCap = 64 * 256

// capBirth builds an NBIRTH carrying bdSeq plus n numeric metrics.
func capBirth(t *testing.T, n int) []byte {
	t.Helper()
	metrics := make([]*sppb.Payload_Metric, 0, n+1)
	metrics = append(metrics, bdSeqM(1))
	for i := 0; i < n; i++ {
		metrics = append(metrics, valuedBirth(fmt.Sprintf("m%05d", i), uint64(i+1), float64(i)))
	}
	enc, err := codec.Encode(&sppb.Payload{Seq: proto.Uint64(0), Metrics: metrics})
	require.NoError(t, err)
	return enc
}

// 🔴 A MESSAGE OVER THE CAP HAS NONE OF ITS READINGS TAKEN, and each of its metrics is
// counted as skipped for being oversize. Driven through the real receive path, so what is
// asserted is what reaches the ingester. The birth still drives the session: its presence
// is emitted, and the node's DATA after it is accepted rather than answered with a rebirth.
func TestAMessageOverTheCapIsRefusedWholeAndCounted(t *testing.T) {
	skipped := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "skipped_cap_test"}, []string{"reason"})
	fake := &fakeIngester{}
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h1"}, Broker{}, fake,
		admitAllSamples{}, fixedNow, Metrics{SamplesSkipped: skipped})

	// bdSeq counts towards the cap: it is a metric the message carries.
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NBIRTH/node-3", payload: capBirth(t, wantMetricCap)})

	assert.Equal(t, 0, fake.ingestedSamples(), "no reading of an oversize message reaches the ingester")
	assert.Equal(t, float64(wantMetricCap+1), testutil.ToFloat64(skipped.WithLabelValues("oversize")),
		"every metric of the refused message is counted, as oversize")
	assert.Equal(t, 1, fake.presenceCount(), "the refused birth still connects the node")

	enc, err := codec.Encode(&sppb.Payload{Seq: proto.Uint64(1), Metrics: []*sppb.Payload_Metric{valuedData(1, 42)}})
	require.NoError(t, err)
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NDATA/node-3", payload: enc})
	assert.Equal(t, 1, fake.ingestedSamples(), "the refused birth's aliases stand, so the next DATA is read")
}

// The counterweight: a message AT the cap is read in full. Without it, a cap of zero
// would pass the test above.
func TestAMessageAtTheCapIsReadInFull(t *testing.T) {
	skipped := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "skipped_cap_test"}, []string{"reason"})
	fake := &fakeIngester{}
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h1"}, Broker{}, fake,
		admitAllSamples{}, fixedNow, Metrics{SamplesSkipped: skipped})

	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NBIRTH/node-3", payload: capBirth(t, wantMetricCap-1)})

	assert.Equal(t, wantMetricCap-1, fake.ingestedSamples(), "every reading of a message at the cap is taken")
	assert.Equal(t, 0.0, testutil.ToFloat64(skipped.WithLabelValues("oversize")))
}

// A DATA message over the cap is refused the same way, and the session is not disturbed:
// the DATA after it, in sequence, is read.
func TestADataMessageOverTheCapIsRefusedAndTheSessionContinues(t *testing.T) {
	skipped := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "skipped_cap_test"}, []string{"reason"})
	fake := &fakeIngester{}
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h1"}, Broker{}, fake,
		admitAllSamples{}, fixedNow, Metrics{SamplesSkipped: skipped})

	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NBIRTH/node-3", payload: capBirth(t, 1)})
	require.Equal(t, 1, fake.ingestedSamples())

	big := make([]*sppb.Payload_Metric, 0, wantMetricCap+1)
	for i := 0; i <= wantMetricCap; i++ {
		big = append(big, valuedData(1, float64(i)))
	}
	enc, err := codec.Encode(&sppb.Payload{Seq: proto.Uint64(1), Metrics: big})
	require.NoError(t, err)
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NDATA/node-3", payload: enc})
	assert.Equal(t, 1, fake.ingestedSamples(), "the oversize DATA adds nothing")
	assert.Equal(t, float64(wantMetricCap+1), testutil.ToFloat64(skipped.WithLabelValues("oversize")))

	enc, err = codec.Encode(&sppb.Payload{Seq: proto.Uint64(2), Metrics: []*sppb.Payload_Metric{valuedData(1, 7)}})
	require.NoError(t, err)
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NDATA/node-3", payload: enc})
	assert.Equal(t, 2, fake.ingestedSamples(), "the next DATA in sequence is read")
}
