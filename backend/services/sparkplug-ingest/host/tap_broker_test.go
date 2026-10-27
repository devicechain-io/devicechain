// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// TEST 7, the tap, over real paho and the embedded broker with a scripted edge node: what the Client
// hands its dispatcher as reports, and what it must never hand it.

type tapCollector struct {
	mu      sync.Mutex
	reports []Report
}

func (c *tapCollector) add(_ string, rs []Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = append(c.reports, rs...)
}

func (c *tapCollector) all() []Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Report(nil), c.reports...)
}

func (c *tapCollector) metrics(name string) []Report {
	var out []Report
	for _, r := range c.all() {
		if r.Kind == ReportMetric && r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

func (c *tapCollector) births() []Report {
	var out []Report
	for _, r := range c.all() {
		if r.Kind == ReportNodeBirth {
			out = append(out, r)
		}
	}
	return out
}

// tapClient is a decode-only Client with commands enabled, whose reports go to the collector.
func tapClient(t *testing.T, rig *spRig) (*Client, *tapCollector, *prometheus.CounterVec) {
	t.Helper()
	msgs := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tap_messages"}, []string{"type"})
	col := &tapCollector{}
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"plant-a"}, Commands: enabledCommands()},
		Broker{URL: rig.mqttURL, ClientID: "sp-tap-host"}, nil, nil, time.Now, Metrics{Messages: msgs})
	c.SetCommandHooks(CommandHooks{Reported: col.add})
	c.Connect()
	t.Cleanup(func() { stopWithin(t, c, 30*time.Second) })
	require.Eventually(t, c.SessionUp, 20*time.Second, 10*time.Millisecond)
	return c, col, msgs
}

func ddata(seq uint64, metrics ...*sppb.Payload_Metric) *sppb.Payload {
	return &sppb.Payload{Seq: proto.Uint64(seq), Metrics: metrics}
}

func boolM(alias uint64, v bool) *sppb.Payload_Metric {
	return &sppb.Payload_Metric{Alias: proto.Uint64(alias), Value: &sppb.Payload_Metric_BooleanValue{BooleanValue: v}}
}

const wireDData = "spBv1.0/plant-a/DDATA/" + wireNode + "/pump-1"

func (c *tapCollector) wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 20*time.Second, 5*time.Millisecond, what)
}

// The same hook on the INGESTING path (a Client with an ingester, as the service builds it): the
// reports reach the dispatcher from there too, not only from a decode-only Client.
func TestTheTapReportsOnTheIngestingPathToo(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	m := newSpMetrics()
	ing := NewIngester(NewRegistrar(fakeCreateGQL{}, "url", nil), NewEmitter(failingWriter{}, time.Now),
		IngestMetrics{MeasurementsEmitted: m.emitted, PresenceEmitted: m.presence})
	src := config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"plant-a"}, AutoRegister: true,
		DeviceTypeToken: "sp-node", Commands: enabledCommands()}
	c := NewClient(src, Broker{URL: rig.mqttURL, ClientID: "sp-tap-ingest"}, ing, admitAllSamples{}, time.Now, m.host())
	c.SetPublisher(rig.writer)
	col := &tapCollector{}
	c.SetCommandHooks(CommandHooks{Reported: col.add})
	rig.online(c)
	t.Cleanup(func() { stopWithin(t, c, 30*time.Second) })
	rig.feeder().publish(wireNBirth, nbirthWire(t, 11))
	col.wait(t, "the ingesting path never reported the birth", func() bool { return len(col.births()) == 1 && len(col.metrics("Setpoint")) == 1 })
}

// 🔴 A scripted node: its births are reported with their values; a Boolean DDATA is a report of the
// value (by name, resolved through the BIRTH's alias); a HISTORICAL metric is not; a null is not;
// and the Host's OWN command, echoed back by the broker's wildcard subscription, is never evidence.
func TestTheTapReportsWhatANodeSaysAndNeverTheHostsOwnCommands(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	c, col, msgs := tapClient(t, rig)
	feed := rig.feeder()
	feed.publish(wireNBirth, nbirthWire(t, 11))
	feed.publish(wireDBirth, dbirthWire(t))
	col.wait(t, "the births were never reported", func() bool { return len(col.metrics("Run")) == 1 && len(col.births()) == 1 })

	// The NBIRTH is reported as a node birth, with its bdSeq, and carries the Setpoint it declared.
	b := col.births()[0]
	assert.Equal(t, wireNodeT, b.Target)
	assert.True(t, b.HasBdSeq)
	assert.Equal(t, uint64(7), b.BdSeq)
	require.Len(t, col.metrics("Setpoint"), 1)
	assert.Equal(t, "NBIRTH", col.metrics("Setpoint")[0].Via)
	assert.Equal(t, WireValue{Kind: 'i', N: 1}, col.metrics("Setpoint")[0].Value)

	// A Boolean DDATA, sent by alias, is a report of Run=true on the device.
	feed.publish(wireDData, encodeT(t, ddata(2, boolM(50, true))))
	col.wait(t, "the Boolean report never arrived", func() bool { return len(col.metrics("Run")) == 2 })
	run := col.metrics("Run")[1]
	assert.Equal(t, wireDevT, run.Target)
	assert.Equal(t, "DDATA", run.Via)
	assert.Equal(t, WireValue{Kind: 'b', N: 1}, run.Value)

	// A HISTORICAL report of the same value: not a report. The control right behind it IS, so the
	// silence above is the flag and not a lost message.
	hist := boolM(50, false)
	hist.IsHistorical = proto.Bool(true)
	feed.publish(wireDData, encodeT(t, ddata(3, hist)))
	feed.publish(wireDData, encodeT(t, ddata(4, boolM(50, false))))
	col.wait(t, "the control report never arrived", func() bool { return len(col.metrics("Run")) == 3 })
	assert.Equal(t, WireValue{Kind: 'b', N: 0}, col.metrics("Run")[2].Value, "the historical report was taken in its place")

	// The Host's own command is echoed back on the wildcard subscription; none of it is evidence.
	before := len(col.all())
	ref, err := c.LookupMetric(wireDevT, "Run")
	require.NoError(t, err)
	w := watchWire(t, rig.mqttURL, "tap-watch", "spBv1.0/plant-a/#")
	_, err = c.PublishMetric(context.Background(), ref, true, nil)
	require.NoError(t, err)
	awaitMsgs(t, w, 1)
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, col.all(), before, "the Host's own DCMD came back to it as a report")
	assert.Zero(t, testutil.ToFloat64(msgs.WithLabelValues("DCMD")),
		"the Host's own DCMD was counted as inbound traffic: its echo is no longer dropped first")
	assert.NotZero(t, testutil.ToFloat64(msgs.WithLabelValues("DDATA")), "control: the node's traffic is counted")
}

// A node birth is reported whether it opens a new session (a new bdSeq) or answers a commanded
// rebirth (the SAME bdSeq), and a redelivered NBIRTH of the live session is not a birth at all: the
// dispatcher tells a reboot from a rebirth by the bdSeq it is handed.
func TestTheTapReportsNodeBirthsWithTheirBdSeq(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	c, col, _ := tapClient(t, rig)
	feed := rig.feeder()
	feed.publish(wireNBirth, nbirthWire(t, 11))
	col.wait(t, "the NBIRTH was never reported", func() bool { return len(col.births()) == 1 })

	// An unsolicited NBIRTH of the same session: a QoS-1 redelivery, not a birth.
	feed.publish(wireNBirth, nbirthWire(t, 11))
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, col.births(), 1, "a redelivered NBIRTH was reported as a birth")

	// A commanded rebirth is answered with the same bdSeq.
	_, err := c.PublishRebirth(context.Background(), wireNodeT, nil)
	require.NoError(t, err)
	feed.publish(wireNBirth, nbirthWire(t, 11))
	col.wait(t, "the answer to the rebirth was never reported", func() bool { return len(col.births()) == 2 })
	assert.Equal(t, uint64(7), col.births()[1].BdSeq, "a rebirth keeps the session's bdSeq")

	// A restarted node announces a new session.
	nb := encodeT(t, &sppb.Payload{Seq: proto.Uint64(0), Metrics: []*sppb.Payload_Metric{bdSeqM(8)}})
	feed.publish(wireNBirth, nb)
	col.wait(t, "the new session's NBIRTH was never reported", func() bool { return len(col.births()) == 3 })
	assert.Equal(t, uint64(8), col.births()[2].BdSeq)
}

// 🔴 "Already at the value": a write to the value the node last reported is marked, at the publish,
// from the table's last-reported value - and one to any other value is not. The mark is what lets
// the end of the window confirm it as unchanged instead of leaving it unanswered.
func TestAWriteToTheValueTheNodeLastReportedIsMarkedAtTheValue(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	c, col, _ := tapClient(t, rig)
	feed := rig.feeder()
	feed.publish(wireNBirth, nbirthWire(t, 11)) // Setpoint (Int8) is declared at 1
	feed.publish(wireDBirth, dbirthWire(t))
	col.wait(t, "births", func() bool { return len(col.metrics("Run")) == 1 && len(col.metrics("Setpoint")) == 1 })
	ref, err := c.LookupMetric(wireNodeT, "Setpoint")
	require.NoError(t, err)

	same, err := c.PublishMetric(context.Background(), ref, 1, nil)
	require.NoError(t, err)
	assert.True(t, same.AtRequestedValue, "a write of the value the BIRTH declared was not marked")
	assert.False(t, same.LastReportedAt.IsZero())

	other, err := c.PublishMetric(context.Background(), ref, 2, nil)
	require.NoError(t, err)
	assert.False(t, other.AtRequestedValue, "a write of another value was marked as already there")

	// The node reports 2: now 2 is the value it holds, and 1 is not.
	feed.publish("spBv1.0/plant-a/NDATA/"+wireNode, encodeT(t, ddata(2, intM(11, 2))))
	col.wait(t, "the report", func() bool { return len(col.metrics("Setpoint")) == 2 })
	now2, err := c.PublishMetric(context.Background(), ref, 2, nil)
	require.NoError(t, err)
	assert.True(t, now2.AtRequestedValue)
	back, err := c.PublishMetric(context.Background(), ref, 1, nil)
	require.NoError(t, err)
	assert.False(t, back.AtRequestedValue)
}
