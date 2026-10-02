// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
)

// 🔴 A 600-METRIC BIRTH IS STORED AS THREE EVENTS, EVERY VALUE KEPT. Industrial births
// routinely carry hundreds to thousands of metrics, and a Sparkplug message used to become
// ONE event of that many readings. It is driven here through the shipped receive path
// (onMessage → session → the real Ingester, Registrar and Emitter this binary builds), with
// only the GraphQL client and the stream writer faked, so what is asserted is what the
// binary would publish.
func TestA600MetricBirthIsEmittedAsThreeEvents(t *testing.T) {
	w := &fakeWireWriter{}
	ing := NewIngester(NewRegistrar(fakeCreateGQL{}, "url", nil), NewEmitter(w, fixedNow), IngestMetrics{})
	src := config.SparkplugSource{Tenant: "acme", HostId: "h1", AutoRegister: true, DeviceTypeToken: "sp-node"}
	c := NewClient(src, Broker{}, ing, admitAllSamples{}, fixedNow, Metrics{})

	metrics := []*sppb.Payload_Metric{bdSeqM(1)}
	want := map[string]bool{}
	for i := 0; i < 600; i++ {
		name := fmt.Sprintf("m%03d", i)
		want[name] = true
		metrics = append(metrics, valuedBirth(name, uint64(i+1), float64(i)))
	}
	enc, err := codec.Encode(&sppb.Payload{Seq: proto.Uint64(0), Metrics: metrics})
	require.NoError(t, err)
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/NBIRTH/node-3", payload: enc})

	var sizes []int
	got := map[string]bool{}
	for _, m := range w.msgs {
		ev, err := esproto.UnmarshalUnresolvedEvent(m.Value)
		require.NoError(t, err)
		p, ok := ev.Payload.(*esmodel.UnresolvedMeasurementsPayload)
		if !ok {
			continue // the birth's CONNECTED presence
		}
		sizes = append(sizes, len(p.Entries))
		for _, en := range p.Entries {
			for k := range en.Measurements {
				got[k] = true
			}
		}
	}
	require.Equal(t, []int{256, 256, 88}, sizes, "600 metric values are three events of at most 256")
	assert.Equal(t, want, got, "every birth metric value is stored, once")
}
