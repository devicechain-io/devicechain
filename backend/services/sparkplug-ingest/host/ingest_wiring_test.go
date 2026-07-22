// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"errors"
	"sync"
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

// --- Observe → []Sample -----------------------------------------------------

func valuedBirth(name string, alias uint64, v float64) *sppb.Payload_Metric {
	return &sppb.Payload_Metric{Name: proto.String(name), Alias: proto.Uint64(alias),
		Datatype: proto.Uint32(datatypeDouble), Value: &sppb.Payload_Metric_DoubleValue{DoubleValue: v}}
}
func valuedData(alias uint64, v float64) *sppb.Payload_Metric {
	return &sppb.Payload_Metric{Alias: proto.Uint64(alias),
		Datatype: proto.Uint32(datatypeDouble), Value: &sppb.Payload_Metric_DoubleValue{DoubleValue: v}}
}

func TestObserveAcceptedDataYieldsSamples(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(1), valuedBirth("temperature", 1, 20)))
	out := h.tr.Observe(nTop(NDATA), pl(1, valuedData(1, 21.5)))
	if assert.Len(t, out, 1, "accepted data yields its numeric sample") {
		assert.Equal(t, "temperature", out[0].Name)
		assert.Equal(t, 21.5, out[0].Value)
	}
}

// TestObserveDuplicateBirthEmitsSamplesOnlyOnce is the double-count negative
// control: a redelivered NBIRTH (same bdSeq) must ingest NOTHING the second time.
func TestObserveDuplicateBirthEmitsSamplesOnlyOnce(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	birth := func() []Sample {
		return h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(1), valuedBirth("temperature", 1, 20)))
	}
	assert.Len(t, birth(), 1, "the first birth ingests its metric values")
	assert.Empty(t, birth(), "a duplicate birth must not re-ingest (no double-count)")
}

func TestObserveRejectedMessageYieldsNoSamples(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, valuedBirth("temperature", 1, 20)))
	h.tr.Observe(nTop(NDATA), pl(1, valuedData(1, 21)))
	out := h.tr.Observe(nTop(NDATA), pl(3, valuedData(1, 22))) // seq 2 skipped
	assert.Empty(t, out, "a rejected (gapped) message yields no samples")
	assert.Equal(t, 1, h.rec.count(), "and still asks for a rebirth")
}

func TestObserveDeathYieldsNoSamples(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(9), valuedBirth("t", 1, 1)))
	assert.Empty(t, h.tr.Observe(nTop(NDEATH), pl(-1, bdSeqM(9))), "a death carries no measurements")
}

// --- receive → ingest wiring -------------------------------------------------

type fakeIngester struct {
	mu    sync.Mutex
	calls []ingestCall
	err   error
	failN int // fail (transiently) the first failN calls, then honor err
}

type ingestCall struct {
	tenant     string
	policy     IngestPolicy
	externalId string
	samples    []Sample
}

func (f *fakeIngester) Ingest(_ context.Context, tenant string, policy IngestPolicy, externalId string, samples []Sample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ingestCall{tenant, policy, externalId, samples})
	if len(f.calls) <= f.failN {
		return errors.New("transient")
	}
	return f.err
}

func (f *fakeIngester) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeMessage is a minimal mqtt.Message carrying a topic + payload.
type fakeMessage struct {
	topic   string
	payload []byte
}

func (m fakeMessage) Duplicate() bool   { return false }
func (m fakeMessage) Qos() byte         { return 1 }
func (m fakeMessage) Retained() bool    { return false }
func (m fakeMessage) Topic() string     { return m.topic }
func (m fakeMessage) MessageID() uint16 { return 0 }
func (m fakeMessage) Payload() []byte   { return m.payload }
func (m fakeMessage) Ack()              {}

// TestOnMessageIngestsWithConnectionTenant drives the full receive path (decode →
// session → ingest) and pins the SP3a invariant end-to-end: the tenant handed to the
// ingester is the CONNECTION's (from config), never the Sparkplug group in the topic
// (here "plant-a"), and the external id is derived from the topic identity.
func TestOnMessageIngestsWithConnectionTenant(t *testing.T) {
	fake := &fakeIngester{}
	src := config.SparkplugSource{Tenant: "acme", HostId: "h1", AutoRegister: true, DeviceTypeToken: "sp-node"}
	c := NewClient(src, Broker{}, fake, fixedNow, Metrics{})

	enc, err := codec.Encode(&sppb.Payload{
		Seq:     proto.Uint64(0),
		Metrics: []*sppb.Payload_Metric{bdSeqM(1), valuedBirth("temperature", 1, 20)},
	})
	require.NoError(t, err)
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NBIRTH/node-3", payload: enc})

	require.Equal(t, 1, fake.count())
	call := fake.calls[0]
	assert.Equal(t, "acme", call.tenant, "tenant comes from the connection, NOT the topic group")
	assert.Equal(t, "plant-a/node-3", call.externalId)
	assert.Equal(t, "sparkplug:h1", call.policy.Source)
	require.Len(t, call.samples, 1)
	assert.Equal(t, "temperature", call.samples[0].Name)
	assert.Equal(t, float64(20), call.samples[0].Value)
}

func TestIngestSamplesRetriesThenSucceeds(t *testing.T) {
	fake := &fakeIngester{failN: 1}
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, fake, fixedNow, Metrics{})
	c.ingestSamples("g/n", []Sample{{Name: "t", Value: 1, Time: 1}})
	assert.Equal(t, 2, fake.count(), "a transient failure is retried, then succeeds")
}

// TestIngestSamplesDropsAndCountsWhenCancelled pins that a persistent failure does
// not block the receive goroutine forever: once the connection context is done, the
// samples are dropped and counted (a clean-session Host gets no broker redelivery).
func TestIngestSamplesDropsAndCountsWhenCancelled(t *testing.T) {
	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "ingest_failures"})
	fake := &fakeIngester{err: errors.New("always down")}
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, fake, fixedNow,
		Metrics{IngestFailures: failures})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.mu.Lock()
	c.runCtx = ctx
	c.mu.Unlock()

	c.ingestSamples("g/n", []Sample{{Name: "t", Value: 1, Time: 1}})
	assert.Equal(t, float64(1), testutil.ToFloat64(failures), "the drop is counted")
	assert.Equal(t, 1, fake.count(), "a cancelled connection stops retrying immediately")
}
