// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package evaluate

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/internal/nightshift"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/testdata/streamprobe"
	"github.com/devicechain-io/dc-event-processing/processor"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// This test cannot be parallel: the deliberately private sink seam belongs to this package.
func TestNightShiftProbeDetectsPublishingSinks(t *testing.T) {
	c := nightshift.Load(t)
	probe := streamprobe.New(t)
	httpProbe := streamprobe.NewHTTP(t)
	before := probe.Counts(t)
	original := newSinks
	t.Cleanup(func() { newSinks = original })
	newSinks = func(rec *recorder) (react.CommandSink, react.AlarmSink, react.ConnectorSink) {
		return processor.NewCommandClient(httpProbe.Client, httpProbe.URL), processor.NewAlarmClient(probe.Writers[streams.RaiseAlarm]), processor.NewConnectorClient(probe.Writers[streams.ConnectorDispatch])
	}
	def, err := rules.Decode(c.Header.R1.Definition)
	require.NoError(t, err)
	threshold := 25.5
	def.Threshold = &threshold
	var events []Event
	for _, s := range c.Samples {
		if s.Device == c.Header.Bystander {
			continue
		}
		e := Event{Device: s.Device, At: s.At}
		for k, v := range s.Metrics {
			e.Metrics = append(e.Metrics, Metric{Name: k, Value: v})
		}
		events = append(events, e)
	}
	out, err := Run(context.Background(), Params{Tenant: nightshift.Tenant, ProfileVersion: c.Header.ProfileVersion, Rules: []Rule{{Token: c.Header.R1.Token, Def: def}}, Events: events, AdvanceTo: c.Samples[len(c.Samples)-1].At, Lateness: config.DefaultWatermarkLatenessSeconds * time.Second})
	require.NoError(t, err)
	require.NotEmpty(t, out.Edges)
	require.Empty(t, out.Degradations, "publishing sinks must successfully reach the broker")
	require.GreaterOrEqual(t, httpProbe.CommandEnqueues(), int64(1), "negative control: probe missed a real command enqueue")
	after := probe.Counts(t)
	require.Greater(t, after[streams.RaiseAlarm], before[streams.RaiseAlarm], "negative control: probe missed a real alarm publish")
	for _, suffix := range streamprobe.Suffixes {
		if suffix != streams.RaiseAlarm {
			require.Equal(t, before[suffix], after[suffix], suffix)
		}
	}
}
