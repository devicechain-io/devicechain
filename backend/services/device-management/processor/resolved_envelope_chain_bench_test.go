// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// BenchmarkResolvedEnvelopeChain measures what the resolved hop costs the broker per event, with
// the REAL device-management pipeline in the middle, in the shape of the design's local chain
// bench (E0): a paced publisher to inbound-events (R3, Msg-Id), the processor (reader, resolver
// pool, resolved writer), and three consumers of resolved-events shaped like device-state and
// event-management (R3 durables, ack each message) and event-processing (R3 durable, ack the last
// of each fetch). The figure is server messages per event, in plus out, summed over the three
// servers of the in-process cluster.
//
// k = 1 runs with the flag off (today's wire); k > 1 runs with envelopes on and k FIXED: the
// collection holds exactly k events and the chunk cap is k, so a steady single-tenant stream
// forms envelopes of k members (the realised k of production depends on the arrival rate and
// the collection window; this fixes it so the figure is comparable at the same k).
//
//	DM_ENVELOPE_CHAIN=1 GOMAXPROCS=6 go test -run '^$' -bench ResolvedEnvelopeChain -benchtime 1x -timeout 60m ./processor/
//	DM_CHAIN_KS=1,16,64  DM_CHAIN_RATES=3000  DM_CHAIN_WINDOW_S=20  DM_CHAIN_REPS=2
func BenchmarkResolvedEnvelopeChain(b *testing.B) {
	if os.Getenv("DM_ENVELOPE_CHAIN") == "" {
		b.Skip("a measurement; set DM_ENVELOPE_CHAIN=1")
	}
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })
	ks := chainInts("DM_CHAIN_KS", []int{1, 16, 64})
	rates := chainInts("DM_CHAIN_RATES", []int{3000})
	reps := chainInts("DM_CHAIN_REPS", []int{2})[0]
	window := time.Duration(chainInts("DM_CHAIN_WINDOW_S", []int{20})[0]) * time.Second
	servers := dctest.StartJetStreamCluster(b, 3)
	fmt.Printf("DMCHAIN GOMAXPROCS=%d window=%s\n", runtime.GOMAXPROCS(0), window)
	n := 0
	for r := 0; r < reps; r++ {
		for _, rate := range rates {
			for _, k := range ks {
				n++
				chainArm(b, servers, fmt.Sprintf("chain%d-%d", time.Now().UnixNano(), n), k, rate, window, r)
			}
		}
	}
}

func chainInts(env string, def []int) []int {
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	var out []int
	for _, s := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

type chainVarz struct {
	at      time.Time
	in, out int64
}

func chainTake(servers []*natsserver.Server) chainVarz {
	v := chainVarz{at: time.Now()}
	for _, s := range servers {
		z, err := s.Varz(nil)
		if err != nil {
			continue
		}
		v.in += z.InMsgs
		v.out += z.OutMsgs
	}
	return v
}

func chainArm(b *testing.B, servers []*natsserver.Server, instance string, k, rate int, window time.Duration, rep int) {
	b.Helper()
	p := startResolvePipeline(b, pipelineSpec{
		srv: servers[0], instance: instance, replicas: 3, resolvers: 8, window: 256, readers: 1,
		events: 0, api: instantApi{}, authMode: config.AuthModeOptional,
	})
	if k > 1 {
		rounds, err := p.nmgr.NewRoundPublisher(streams.ResolvedEvents)
		if err != nil {
			b.Fatal(err)
		}
		WithResolvedEnvelopes(true, rounds)(p.iproc)
		linger := time.Second
		p.iproc.envelopeLinger = &linger
		p.iproc.envelopeCollectMax = k
		p.iproc.envelopeChunkMax = k
	}
	if err := p.iproc.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() { _ = p.iproc.Stop(context.Background()) }()

	conns := []*nats.Conn{}
	connect := func(i int) *nats.Conn {
		nc, err := nats.Connect(servers[i%3].ClientURL())
		if err != nil {
			b.Fatal(err)
		}
		conns = append(conns, nc)
		return nc
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	ajs, _ := connect(0).JetStream()
	for _, c := range []struct {
		name   string
		ackAll bool
	}{{"ds", false}, {"em", false}, {"ep", true}} {
		cc := &nats.ConsumerConfig{Durable: c.name, AckPolicy: nats.AckExplicitPolicy, AckWait: 60 * time.Second, MaxDeliver: 5,
			MaxAckPending: 65536}
		if c.ackAll {
			cc.AckPolicy = nats.AckAllPolicy
		}
		if _, err := ajs.AddConsumer(p.resolvedStream, cc); err != nil {
			b.Fatal(err)
		}
		js, _ := connect(1 + len(c.name)).JetStream()
		sub, err := js.PullSubscribe("", c.name, nats.Bind(p.resolvedStream, c.name))
		if err != nil {
			b.Fatal(err)
		}
		ackAll := c.ackAll
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ms, err := sub.Fetch(256, nats.MaxWait(500*time.Millisecond))
				if len(ms) == 0 {
					_ = err
					continue
				}
				if ackAll {
					_ = ms[len(ms)-1].Ack()
				} else {
					for _, m := range ms {
						_ = m.Ack()
					}
				}
			}
		}()
	}

	// The publisher (event-sources): events paced at rate per second, single tenant, one per
	// message, with the Msg-Id the real source gives them.
	pjs, _ := connect(0).JetStream(nats.PublishAsyncMaxPending(1024))
	subject := messaging.ScopedSubject(instance, "acme", streams.InboundEvents)
	var pwg sync.WaitGroup
	var published atomic.Int64
	pwg.Add(1)
	go func() {
		defer pwg.Done()
		t0 := time.Now()
		tk := time.NewTicker(time.Millisecond)
		defer tk.Stop()
		var seq int64
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
			}
			due := int64(time.Since(t0).Seconds() * float64(rate))
			for seq < due {
				body, err := esproto.MarshalUnresolvedEvent(benchMeasurement(int(seq) % 1000))
				if err != nil {
					b.Error(err)
					return
				}
				m := nats.NewMsg(subject)
				m.Data = body
				m.Header.Set(nats.MsgIdHdr, fmt.Sprintf("acme.%d", seq))
				if _, err := pjs.PublishMsgAsync(m); err != nil {
					time.Sleep(time.Millisecond)
					break
				}
				seq++
				published.Store(seq)
			}
		}
	}()

	time.Sleep(5 * time.Second)
	runtime.GC()
	resolvedMsgs := func() (msgs uint64) {
		si, err := ajs.StreamInfo(p.resolvedStream)
		if err != nil {
			return 0
		}
		return si.State.Msgs
	}
	// Events are counted from the processor's own members/legacy counters when envelopes are
	// on, and from the stream when they are off (one message per event).
	events := func() int64 {
		if p.iproc.env != nil {
			return p.iproc.env.Members.Load() + p.iproc.env.legacy.Load()
		}
		return int64(resolvedMsgs())
	}
	e0, m0 := events(), resolvedMsgs()
	s0 := chainTake(servers)
	time.Sleep(window)
	s1 := chainTake(servers)
	e1, m1 := events(), resolvedMsgs()
	ci, _ := ajs.ConsumerInfo(p.inboundStream, messaging.DurableName(instance, "device-management", streams.InboundEvents))
	lag := uint64(0)
	if ci != nil {
		lag = ci.NumPending + uint64(ci.NumAckPending)
	}
	close(stop)
	pwg.Wait()
	wg.Wait()

	dt := s1.at.Sub(s0.at).Seconds()
	ev := float64(e1 - e0)
	if ev == 0 {
		ev = 1
	}
	fmt.Printf("DMCHAIN rep=%d rate=%5d k=%2d | events/s=%7.0f resolved msgs/s=%7.0f realised k=%5.1f | srv msgs/ev=%6.2f | inbound backlog at end=%d\n",
		rep, rate, k, ev/dt, float64(m1-m0)/dt, ev/float64(max(m1-m0, 1)),
		float64((s1.in-s0.in)+(s1.out-s0.out))/ev, lag)
}
