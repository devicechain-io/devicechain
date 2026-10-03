// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
)

// 🔴 A BIRTH CARRYING ONE METRIC OLDER THAN THE PLATFORM'S AGE LIMIT KEEPS EVERY OTHER ONE.
// Sparkplug is report-by-exception: a metric's timestamp is when its value last CHANGED, and
// the birth is the only message a rarely-changing metric appears in. A setpoint untouched for
// more than 366 days therefore arrives dated more than 366 days back on every birth. The
// platform does not store a reading that old, but refusing the whole birth would throw away
// every fresh metric with it, on every rebirth, for good. So the stale sample alone is
// dropped and counted, and the rest are stored.
//
// Driven through the shipped receive path (onMessage → session → the real Ingester,
// Registrar and Emitter), with only the GraphQL client and the stream writer faked.
func TestABirthMetricOlderThanTheAgeLimitIsDroppedAloneAndTheRestStored(t *testing.T) {
	w := &fakeWireWriter{}
	tooOld := prometheus.NewCounter(prometheus.CounterOpts{Name: "too_old"})
	emitted := prometheus.NewCounter(prometheus.CounterOpts{Name: "emitted"})
	ing := NewIngester(NewRegistrar(fakeCreateGQL{}, "url", nil), NewEmitter(w, fixedNow),
		IngestMetrics{TooOldDropped: tooOld, MeasurementsEmitted: emitted})
	src := config.SparkplugSource{Tenant: "acme", HostId: "h1", AutoRegister: true, DeviceTypeToken: "sp-node"}
	c := NewClient(src, Broker{}, ing, admitAllSamples{}, fixedNow, Metrics{})

	fresh := fixedNow().Add(-time.Minute)
	stale := fixedNow().Add(-400 * 24 * time.Hour)
	setpoint := valuedBirth("setpoint", 1, 42)
	setpoint.Timestamp = proto.Uint64(uint64(stale.UnixMilli()))
	temperature := valuedBirth("temperature", 2, 21.5)
	temperature.Timestamp = proto.Uint64(uint64(fresh.UnixMilli()))
	enc, err := codec.Encode(&sppb.Payload{Seq: proto.Uint64(0),
		Metrics: []*sppb.Payload_Metric{bdSeqM(1), setpoint, temperature}})
	require.NoError(t, err)
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NBIRTH/node-3", payload: enc})

	stored := map[string]time.Time{}
	var envelopes []time.Time
	for _, m := range w.msgs {
		ev, err := esproto.UnmarshalUnresolvedEvent(m.Value)
		require.NoError(t, err)
		p, ok := ev.Payload.(*esmodel.UnresolvedMeasurementsPayload)
		if !ok {
			continue // the birth's CONNECTED presence
		}
		envelopes = append(envelopes, ev.OccurredTime)
		for _, en := range p.Entries {
			for k := range en.Measurements {
				require.NotNil(t, en.OccurredTime)
				stored[k] = *en.OccurredTime
			}
		}
	}
	require.Contains(t, stored, "temperature", "the fresh metric of the birth is stored")
	assert.True(t, stored["temperature"].Equal(fresh.Truncate(time.Millisecond)), "at its own time")
	assert.NotContains(t, stored, "setpoint", "a metric more than 366 days old is not stored")
	require.Len(t, envelopes, 1)
	assert.True(t, envelopes[0].Equal(fresh.Truncate(time.Millisecond)),
		"the envelope is dated from the samples that were kept: %v", envelopes[0])
	assert.Equal(t, 1.0, testutil.ToFloat64(tooOld), "the dropped metric is counted")
	assert.Equal(t, 1.0, testutil.ToFloat64(emitted), "and only the kept one as emitted")
}
