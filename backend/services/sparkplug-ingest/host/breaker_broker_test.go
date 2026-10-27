// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"io"
	"net"
	"strings"
	"sync/atomic"
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

// dropOnDCMDProxy stands in for a customer broker that answers a refused publish by dropping the
// WHOLE session (some brokers do by default). The embedded broker only discards a publish its
// permissions refuse, so the refusal is modelled in front of it: a PUBLISH to a DCMD topic on the
// Host's connection closes that connection instead of being forwarded, exactly while `drop` is set.
// Everything else, in both directions, passes through untouched.
type dropOnDCMDProxy struct {
	ln       net.Listener
	upstream string
	drop     atomic.Bool
	dropped  atomic.Int32 // sessions it closed
	accepted atomic.Int32 // sessions it carried
}

func startDropOnDCMDProxy(t *testing.T, upstreamURL string) *dropOnDCMDProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &dropOnDCMDProxy{ln: ln, upstream: strings.TrimPrefix(upstreamURL, "tcp://")}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.accepted.Add(1)
			go p.serve(c)
		}
	}()
	return p
}

func (p *dropOnDCMDProxy) url() string { return "tcp://" + p.ln.Addr().String() }

func (p *dropOnDCMDProxy) serve(client net.Conn) {
	up, err := net.Dial("tcp", p.upstream)
	if err != nil {
		_ = client.Close()
		return
	}
	closeBoth := func() { _ = client.Close(); _ = up.Close() }
	go func() { _, _ = io.Copy(client, up); closeBoth() }()
	// Client -> broker, one MQTT packet at a time.
	for {
		hdr := make([]byte, 1)
		if _, err := io.ReadFull(client, hdr); err != nil {
			closeBoth()
			return
		}
		raw := append([]byte(nil), hdr...)
		remaining, mult := 0, 1
		for {
			b := make([]byte, 1)
			if _, err := io.ReadFull(client, b); err != nil {
				closeBoth()
				return
			}
			raw = append(raw, b[0])
			remaining += int(b[0]&0x7f) * mult
			if b[0]&0x80 == 0 {
				break
			}
			mult *= 128
		}
		body := make([]byte, remaining)
		if _, err := io.ReadFull(client, body); err != nil {
			closeBoth()
			return
		}
		if hdr[0]>>4 == 3 && len(body) >= 2 { // PUBLISH
			n := int(body[0])<<8 | int(body[1])
			if len(body) >= 2+n && strings.Contains(string(body[2:2+n]), "/DCMD/") && p.drop.Load() {
				p.dropped.Add(1)
				closeBoth()
				return
			}
		}
		if _, err := up.Write(append(raw, body...)); err != nil {
			closeBoth()
			return
		}
	}
}

func birthsWithBdSeq(t *testing.T, bd uint64) (nbirth, dbirth []byte) {
	nbirth = encodeT(t, &sppb.Payload{Seq: proto.Uint64(0), Metrics: []*sppb.Payload_Metric{
		bdSeqM(bd),
		{Name: proto.String("Setpoint"), Alias: proto.Uint64(11), Datatype: proto.Uint32(datatypeInt8), Value: &sppb.Payload_Metric_IntValue{IntValue: 1}},
	}})
	return nbirth, dbirthWire(t)
}

// 🔴 TEST 8b: a broker that drops the session on a refused publish. The first command opens the
// breaker; the commands queued behind it fail fast WITHOUT being published, so the session is not
// dropped again and the Host's STATE is announced once per session, never once per queued command;
// the half-open probe reopens it for twice as long while the cause stands, and closes it once the
// publish permission is fixed.
func TestABrokerThatDropsTheSessionOnARefusedPublishIsPausedNotHammered(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	proxy := startDropOnDCMDProxy(t, rig.mqttURL)
	proxy.drop.Store(true)
	states := watchState(t, rig.mqttURL, StateTopic("h1"))
	online := func() int {
		n := 0
		for _, st := range states() {
			if st.Online {
				n++
			}
		}
		return n
	}

	tripsCtr := prometheus.NewCounter(prometheus.CounterOpts{Name: "command_breaker_trips"})
	var tripHook atomic.Int32
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"plant-a"}, Commands: enabledCommands()},
		Broker{URL: proxy.url(), ClientID: "sp-8b-host"}, nil, nil, time.Now, Metrics{CommandBreakerTrips: tripsCtr})
	clk := newClock()
	c.breaker = NewBreaker(clk.now) // the backoff is measured on this clock, not the wall's
	c.SetCommandHooks(CommandHooks{Tripped: func(time.Time) { tripHook.Add(1) }})
	c.Connect()
	t.Cleanup(func() { stopWithin(t, c, 30*time.Second) })
	require.Eventually(t, c.SessionUp, 20*time.Second, 10*time.Millisecond)
	feed := rig.feeder()

	bd := uint64(0)
	rebirth := func() MetricRef {
		bd++
		nb, db := birthsWithBdSeq(t, bd)
		feed.publish(wireNBirth, nb)
		feed.publish(wireDBirth, db)
		var ref MetricRef
		require.Eventually(t, func() bool {
			r, err := c.LookupMetric(wireDevT, "Run")
			ref = r
			return err == nil
		}, 20*time.Second, 5*time.Millisecond, "the births never reached the table")
		return ref
	}
	ref := rebirth()
	require.Equal(t, 1, online())

	// The first command is published; the broker drops the session.
	_, err := c.PublishMetric(context.Background(), ref, true, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return c.BreakerState() == BreakerOpen }, 20*time.Second, 10*time.Millisecond,
		"a session dropped right after a command did not open the breaker")
	assert.Equal(t, int32(1), proxy.dropped.Load())
	assert.Equal(t, int32(1), tripHook.Load())

	// The Host reconnects as it always did; ingest is unaffected.
	require.Eventually(t, c.SessionUp, 20*time.Second, 10*time.Millisecond, "the Host did not reconnect")
	require.Eventually(t, func() bool { return online() == 2 }, 20*time.Second, 10*time.Millisecond)
	ref = rebirth()

	// Ten queued commands fail fast and are never published.
	for i := 0; i < 10; i++ {
		_, err := c.PublishMetric(context.Background(), ref, true, nil)
		require.ErrorIs(t, err, ErrBreakerOpen, "queued command %d", i)
	}
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(1), proxy.dropped.Load(), "queued commands were published and dropped the session again")
	assert.Equal(t, int32(2), proxy.accepted.Load(), "the Host reconnected more than once")
	assert.Equal(t, 2, online(), "STATE was announced more than once per session")

	// Half-open after the backoff: one command goes out, the cause still stands, so the breaker
	// reopens for twice as long.
	clk.advance(breakerMinBackoff)
	require.Equal(t, BreakerHalfOpen, c.BreakerState())
	_, err = c.PublishMetric(context.Background(), ref, true, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return c.BreakerState() == BreakerOpen }, 20*time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(2), proxy.dropped.Load())
	require.Eventually(t, c.SessionUp, 20*time.Second, 10*time.Millisecond)
	ref = rebirth()
	clk.advance(breakerMinBackoff)
	assert.Equal(t, BreakerOpen, c.BreakerState(), "the second backoff was not doubled")
	clk.advance(breakerMinBackoff)
	require.Equal(t, BreakerHalfOpen, c.BreakerState())

	// The publish permission is fixed: the probe is carried, and a watch with no disconnect closes it.
	proxy.drop.Store(false)
	_, err = c.PublishMetric(context.Background(), ref, true, nil)
	require.NoError(t, err)
	clk.advance(breakerWatch)
	assert.Equal(t, BreakerClosed, c.BreakerState())
	assert.Equal(t, int32(2), proxy.dropped.Load(), "the carried probe dropped the session")
	_, err = c.PublishMetric(context.Background(), ref, true, nil)
	assert.NoError(t, err, "a closed breaker refused a command")
	assert.Equal(t, float64(2), testutil.ToFloat64(tripsCtr), "the trip counter does not match the two times the breaker opened")
}
