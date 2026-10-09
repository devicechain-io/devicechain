// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

// BenchmarkPipeFetch compares the live reader's fetch shape (one synchronous Fetch(64) at a
// time, the batch handed out one message at a time) against pipelined alternatives, on the
// real embedded broker (R1, and a three-node R3 cluster with an R3 stream), with a realistic
// per-message processing cost (a CPU spin) and an explicit ack per message.
//
// It reuses BenchmarkAckCost's fixtures (ackBenchSingle, ackBenchCluster, ackProcessCPU).
//
// Arms (all on the same durable shape the reader uses: explicit ack, MaxAckPending 65536):
//
//	serial-B  legacy PullSubscribe+Bind, sub.Fetch(B) then process the batch (today, B=64)
//	ahead-B   one fetcher goroutine calling sub.Fetch(B) serially into a 1-batch channel;
//	          the consumer processes while the next request is in flight (ONE outstanding pull)
//	msgs-M    jetstream Consumer.Messages(PullMaxMessages(M)): the library keeps up to M
//	          messages requested/buffered and re-pulls at M/2 (SEVERAL outstanding pulls)
//	reader-off-B, reader-ahead-B
//	          the PRODUCTION reader (natsReader.ReadMessage), built with
//	          infrastructure.nats.fetch {batch: B, ahead: off/on}: the measurement of the
//	          mechanism itself rather than of a model of it. reader-off-64 is today's reader.
//
// lat is a one-way delay injected by an in-process TCP delay line between the consumer's
// connection and the server (0 = direct loopback), standing in for a busy leader on another
// node. Publishing and consumer creation use a direct connection outside the timer.
//
//	go test -run '^$' -bench PipeFetch -benchtime 2x -count 6 ./messaging/

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	pipeMessages = 6400
	pipePayload  = 300
)

var pipeStreamSeq atomic.Int64

var pipeDebug = false

func BenchmarkPipeFetch(b *testing.B) {
	type arm struct {
		name string
		run  func(b *testing.B, nc *nats.Conn, stream string, cost time.Duration) int
	}
	arms := []arm{
		{"serial-64", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int { return pipeSerial(b, nc, s, 64, c) }},
		{"serial-256", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int { return pipeSerial(b, nc, s, 256, c) }},
		{"ahead-64", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int { return pipeAhead(b, nc, s, 64, c) }},
		{"ahead-128", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int { return pipeAhead(b, nc, s, 128, c) }},
		{"ahead-256", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int { return pipeAhead(b, nc, s, 256, c) }},
		{"reader-off-64", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int {
			return pipeReader(b, nc, s, 64, false, c)
		}},
		{"reader-ahead-64", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int {
			return pipeReader(b, nc, s, 64, true, c)
		}},
		{"reader-ahead-128", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int {
			return pipeReader(b, nc, s, 128, true, c)
		}},
		{"msgs-128", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int { return pipeMsgs(b, nc, s, 128, c) }},
		{"msgs-256", func(b *testing.B, nc *nats.Conn, s string, c time.Duration) int { return pipeMsgs(b, nc, s, 256, c) }},
	}
	for _, topo := range []struct {
		name     string
		replicas int
	}{{"R1", 1}, {"R3", 3}} {
		b.Run(topo.name, func(b *testing.B) {
			var direct string
			if topo.replicas == 1 {
				direct = ackBenchSingle(b).ClientURL()
			} else {
				direct = ackBenchCluster(b).ClientURL()
			}
			for _, lat := range []time.Duration{0, time.Millisecond, 2500 * time.Microsecond, 5 * time.Millisecond} {
				url := direct
				if lat > 0 {
					url = startDelayProxy(b, direct, lat)
				}
				for _, cost := range []time.Duration{50 * time.Microsecond, 150 * time.Microsecond} {
					for _, a := range arms {
						name := fmt.Sprintf("lat=%s/cost=%s/%s", lat, cost, a.name)
						b.Run(name, func(b *testing.B) {
							pipeRunArm(b, direct, url, topo.replicas, cost, a.run)
						})
					}
				}
			}
		})
	}
}

func pipeRunArm(b *testing.B, direct, url string, replicas int, cost time.Duration,
	run func(b *testing.B, nc *nats.Conn, stream string, cost time.Duration) int) {
	b.Helper()
	setup, err := nats.Connect(direct)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	defer setup.Close()
	sjs, err := jetstream.New(setup, jetstream.WithPublishAsyncMaxPending(4096))
	if err != nil {
		b.Fatalf("jetstream: %v", err)
	}
	nc, err := nats.Connect(url)
	if err != nil {
		b.Fatalf("connect consume: %v", err)
	}
	defer nc.Close()
	var rttUs float64
	for i := 0; i < 5; i++ {
		if rtt, err := nc.RTT(); err == nil {
			rttUs += float64(rtt.Microseconds()) / 5
		}
	}
	var wall, cpu, consumeWall time.Duration
	total := 0
	b.ResetTimer()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		stream := fmt.Sprintf("PIPE%d", pipeStreamSeq.Add(1))
		if _, err := sjs.CreateStream(ctx, jetstream.StreamConfig{
			Name: stream, Subjects: []string{stream + ".>"}, Replicas: replicas, Storage: jetstream.FileStorage,
		}); err != nil {
			b.Fatalf("create stream: %v", err)
		}
		payload := make([]byte, pipePayload)
		for j := 0; j < pipeMessages; j++ {
			if _, err := sjs.PublishAsync(stream+".x", payload, jetstream.WithStallWait(time.Minute)); err != nil {
				b.Fatalf("publish: %v", err)
			}
		}
		select {
		case <-sjs.PublishAsyncComplete():
		case <-ctx.Done():
			b.Fatal("publish did not complete")
		}
		// The reader's durable shape (consumerConfig), with a long AckWait so nothing
		// redelivers inside a run.
		cons, err := sjs.CreateConsumer(ctx, stream, jetstream.ConsumerConfig{
			Durable: "bench", AckPolicy: jetstream.AckExplicitPolicy, AckWait: 5 * time.Minute,
			MaxDeliver: 5, MaxAckPending: 65536, Replicas: replicas,
		})
		if err != nil {
			b.Fatalf("create consumer: %v", err)
		}
		runtime.GC()
		time.Sleep(200 * time.Millisecond)

		b.StartTimer()
		t0 := time.Now()
		c0 := ackProcessCPU()
		got := run(b, nc, stream, cost)
		consumeWall += time.Since(t0)
		if err := nc.Flush(); err != nil {
			b.Fatalf("flush: %v", err)
		}
		for {
			info, err := cons.Info(ctx)
			if err != nil {
				b.Fatalf("info: %v", err)
			}
			if info.NumAckPending == 0 && info.NumPending == 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		cpu += ackProcessCPU() - c0
		wall += time.Since(t0)
		b.StopTimer()
		total += got
		_ = sjs.DeleteStream(ctx, stream)
		cancel()
	}
	b.ReportMetric(rttUs, "rtt-us")
	b.ReportMetric(float64(total)/wall.Seconds(), "msgs/s")
	b.ReportMetric(float64(total)/consumeWall.Seconds(), "handout-msgs/s")
	// Process CPU (client + embedded server + delay line) per message, minus the spin.
	spin := time.Duration(total) * cost
	b.ReportMetric(float64((cpu-spin).Nanoseconds())/float64(total), "ovh-cpu-ns/msg")
}

// pipeWork is the per-message handler's CPU cost: a calibrated arithmetic spin. (A
// time.Now() spin is useless on Windows, where the clock it reads ticks every ~0.5 ms.)
func pipeWork(cost time.Duration) { pipeBurn(int(float64(cost.Nanoseconds()) / pipeNsPerIter())) }

var (
	pipeSink    uint64
	pipeCalOnce sync.Once
	pipeCal     float64
)

func pipeBurn(n int) {
	x := pipeSink
	for i := 0; i < n; i++ {
		x = x*6364136223846793005 + 1442695040888963407
	}
	pipeSink = x
}

func pipeNsPerIter() float64 {
	pipeCalOnce.Do(func() {
		const n = 50_000_000
		t0 := time.Now()
		pipeBurn(n)
		pipeCal = float64(time.Since(t0).Nanoseconds()) / n
	})
	return pipeCal
}

type seqCheck struct {
	b    *testing.B
	last uint64
}

func (s *seqCheck) see(seq uint64) {
	if seq <= s.last {
		s.b.Fatalf("out of order: seq %d after %d", seq, s.last)
	}
	s.last = seq
}

func pipeLegacySub(b *testing.B, nc *nats.Conn, stream string) *nats.Subscription {
	js, err := nc.JetStream()
	if err != nil {
		b.Fatalf("legacy js: %v", err)
	}
	sub, err := js.PullSubscribe("", "bench", nats.Bind(stream, "bench"))
	if err != nil {
		b.Fatalf("pull subscribe: %v", err)
	}
	return sub
}

func handleLegacy(b *testing.B, sc *seqCheck, m *nats.Msg, cost time.Duration) {
	md, err := m.Metadata()
	if err != nil {
		b.Fatalf("metadata: %v", err)
	}
	sc.see(md.Sequence.Stream)
	pipeWork(cost)
	if err := m.Ack(); err != nil {
		b.Fatalf("ack: %v", err)
	}
}

// pipeReader drives the production reader (ReadMessage, with the fetch settings a service's
// instance configuration would give it) over the same stream, handing out and acking one
// message at a time as a consumer loop does.
func pipeReader(b *testing.B, nc *nats.Conn, stream string, batch int, ahead bool, cost time.Duration) int {
	js, err := nc.JetStream()
	if err != nil {
		b.Fatalf("legacy js: %v", err)
	}
	nmgr := &NatsManager{nc: nc, js: js, Microservice: &core.Microservice{InstanceId: "bench", FunctionalArea: "bench"}}
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Fetch = config.NatsFetchConfiguration{Batch: batch, Ahead: ahead}
	r := &natsReader{nmgr: nmgr, stream: stream, durable: "bench", subject: stream + ".>"}
	r.configureFetch()
	sub := pipeLegacySub(b, nc, stream)
	defer sub.Unsubscribe()
	r.sub.Store(sub)
	sc := &seqCheck{b: b}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	got := 0
	for got < pipeMessages {
		msg, err := r.ReadMessage(ctx)
		if err != nil {
			b.Fatalf("read: %v", err)
		}
		sc.see(msg.StreamSeq)
		pipeWork(cost)
		if err := msg.Ack(); err != nil {
			b.Fatalf("ack: %v", err)
		}
		got++
	}
	r.dropPending()
	return got
}

// pipeSerial is today's reader: one Fetch at a time, the batch processed before the next.
func pipeSerial(b *testing.B, nc *nats.Conn, stream string, batch int, cost time.Duration) int {
	sub := pipeLegacySub(b, nc, stream)
	defer sub.Unsubscribe()
	sc := &seqCheck{b: b}
	got := 0
	for got < pipeMessages {
		msgs, err := sub.Fetch(batch, nats.MaxWait(time.Second))
		if err != nil && !errors.Is(err, nats.ErrTimeout) {
			b.Fatalf("fetch: %v", err)
		}
		for _, m := range msgs {
			handleLegacy(b, sc, m, cost)
			got++
		}
	}
	return got
}

// pipeAhead keeps exactly one pull request outstanding from a fetcher goroutine while the
// consumer works through the previous batch. Batches are handed over a 1-slot channel, so at
// most: one batch being processed + one queued + one in flight.
func pipeAhead(b *testing.B, nc *nats.Conn, stream string, batch int, cost time.Duration) int {
	sub := pipeLegacySub(b, nc, stream)
	ch := make(chan []*nats.Msg, 1)
	// Stopping cancels the fetch in flight, so the join below is immediate rather than a
	// wait for an empty long-poll to expire (the production shape must do the same).
	stopCtx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for stopCtx.Err() == nil {
			fctx, fcancel := context.WithTimeout(stopCtx, time.Second)
			msgs, err := sub.Fetch(batch, nats.Context(fctx))
			fcancel()
			if err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
				return
			}
			if len(msgs) == 0 {
				continue
			}
			select {
			case ch <- msgs:
			case <-stopCtx.Done():
				return
			}
		}
	}()
	sc := &seqCheck{b: b}
	got := 0
	for got < pipeMessages {
		for _, m := range <-ch {
			handleLegacy(b, sc, m, cost)
			got++
		}
	}
	stop()
	wg.Wait()
	_ = sub.Unsubscribe()
	return got
}

// pipeMsgs uses the jetstream package's pull iterator, which keeps up to max messages
// requested and re-pulls when the buffer falls under max/2 (several outstanding pulls).
func pipeMsgs(b *testing.B, nc *nats.Conn, stream string, max int, cost time.Duration) int {
	js, err := jetstream.New(nc)
	if err != nil {
		b.Fatalf("jetstream: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cons, err := js.Consumer(ctx, stream, "bench")
	if err != nil {
		b.Fatalf("consumer: %v", err)
	}
	it, err := cons.Messages(jetstream.PullMaxMessages(max))
	if err != nil {
		b.Fatalf("messages: %v", err)
	}
	defer it.Stop()
	sc := &seqCheck{b: b}
	got := 0
	for got < pipeMessages {
		m, err := it.Next()
		if err != nil {
			b.Fatalf("next: %v", err)
		}
		md, err := m.Metadata()
		if err != nil {
			b.Fatalf("metadata: %v", err)
		}
		sc.see(md.Sequence.Stream)
		pipeWork(cost)
		if err := m.Ack(); err != nil {
			b.Fatalf("ack: %v", err)
		}
		got++
	}
	return got
}

// startDelayProxy forwards TCP to target with a one-way delay on each direction (a delay
// line, so bandwidth is not throttled, only latency added).
func startDelayProxy(b *testing.B, target string, delay time.Duration) string {
	b.Helper()
	host := target
	if len(host) > 7 && host[:7] == "nats://" {
		host = host[7:]
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("proxy listen: %v", err)
	}
	b.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t, err := net.Dial("tcp", host)
			if err != nil {
				c.Close()
				continue
			}
			go delayPipe(t, c, delay)
			go delayPipe(c, t, delay)
		}
	}()
	return "nats://" + ln.Addr().String()
}

func delayPipe(dst, src net.Conn, delay time.Duration) {
	type chunk struct {
		data []byte
		at   time.Time
	}
	ch := make(chan chunk, 1<<14)
	go func() {
		defer close(ch)
		buf := make([]byte, 64<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				ch <- chunk{cp, time.Now().Add(delay)}
			}
			if err != nil {
				return
			}
		}
	}()
	for c := range ch {
		if w := time.Until(c.at); w > 0 {
			time.Sleep(w)
		}
		if _, err := dst.Write(c.data); err != nil {
			break
		}
	}
	dst.Close()
	src.Close()
	for range ch {
	}
	_ = io.EOF
}
