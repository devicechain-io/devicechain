// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// BenchmarkConcurrentAwaitedPublish is the publish pattern of HTTP ingest: c concurrent
// callers, each of which publishes ONE message and waits for its own PubAck before it
// publishes again (a request answered only once its event is stored). It compares the
// ways that wait can be built:
//
//   - sync: the synchronous writer, one request/reply per publish (what HTTP ingest uses);
//   - async: the same message through PublishMsgAsync on a private JetStream context, the
//     caller waiting on its own future (the lower bound any async design can reach);
//   - ordered: the ordered writer behind a mutex (it has ONE submitter), each caller
//     waiting on a channel its done callback fills.
//
// Every arm waits for the ack before the caller goes on, so the pipelining depth is c in all
// of them: what differs is what one publish costs the client, not how many are in flight.
// Reported: ev/s, and client CPU per publish when DC_BENCH_NATS_URL points at a nats-server
// in another process.
//
//	go test ./messaging -run '^$' -bench ConcurrentAwaitedPublish -benchtime 20000x -count 6
func BenchmarkConcurrentAwaitedPublish(b *testing.B) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	nmgr := benchCostManager(b, "concpub")
	w, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		b.Fatal(err)
	}
	ow, err := nmgr.NewOrderedWriter(streams.ResolvedEvents, maxOrderedWindow)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(ow.Close)
	js, err := nmgr.nc.JetStream(nats.PublishAsyncTimeout(publishWait), nats.PublishAsyncMaxPending(4096))
	if err != nil {
		b.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	subject, err := nmgr.tenantSubject(ctx, streams.ResolvedEvents, "")
	if err != nil {
		b.Fatal(err)
	}
	payload := make([]byte, 300)

	var omu sync.Mutex
	arms := map[string]func() error{
		"sync": func() error { return w.WriteMessages(ctx, Message{Value: payload}) },
		"async": func() error {
			nm := natsMsg(subject, Message{Value: payload})
			fut, err := js.PublishMsgAsync(nm)
			if err != nil {
				return err
			}
			return awaitPubAck(fut, time.Now().Add(publishWait), false)
		},
		"ordered": func() error {
			done := make(chan error, 1)
			omu.Lock()
			ow.Publish(ctx, Message{Value: payload}, func(err error) { done <- err })
			omu.Unlock()
			return <-done
		},
	}
	for _, c := range []int{1, 8, 64, 256} {
		for _, name := range []string{"sync", "async", "ordered"} {
			publish := arms[name]
			b.Run(fmt.Sprintf("c=%d/%s", c, name), func(b *testing.B) {
				for i := 0; i < 200; i++ {
					if err := publish(); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				cpu0, start := ackProcessCPU(), time.Now()
				var wg sync.WaitGroup
				errs := make(chan error, c)
				for g := 0; g < c; g++ {
					n := b.N / c
					if g < b.N%c {
						n++
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := 0; i < n; i++ {
							if err := publish(); err != nil {
								errs <- err
								return
							}
						}
					}()
				}
				wg.Wait()
				elapsed, cpu := time.Since(start), ackProcessCPU()-cpu0
				b.StopTimer()
				close(errs)
				for err := range errs {
					b.Fatal(err)
				}
				b.ReportMetric(float64(b.N)/elapsed.Seconds(), "ev/s")
				b.ReportMetric(float64(cpu.Nanoseconds())/float64(b.N), "cpu-ns/op")
			})
		}
	}
}
