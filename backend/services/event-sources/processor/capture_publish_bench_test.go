// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
)

// BenchmarkCaptureIngestThroughput measures the gateway source end to end, publish-bound: N
// device-event messages pre-published to the capture stream (one tenant, 1000 devices), read
// by the real durable reader, decoded by the real JSON decoder in the real worker pool, and
// published to inbound-events. Timing ends when inbound-events holds all N (for the none arm,
// when all N have been settled).
//
// Arms: topology (R1 single server, R3 3-node cluster) x publish mode:
//
//	none   each message is built (marshal + dedup id) and settled nil at once: the stage's
//	       ceiling WITHOUT a publish
//	sync   the shape this source had before its publishes were pipelined: a synchronous
//	       MessageWriter publish on each of DECODE_WORKER_COUNT goroutines at once
//	w=N    the real ordered writer with a window of N (1 behaves as a serial writer;
//	       CAPTURE_PUBLISH_WINDOW is what ships)
//
// In-process over loopback: the absolute numbers are a FLOOR, the ratios are the result.
//
// Run with: go test ./processor -run '^$' -bench BenchmarkCaptureIngestThroughput -benchtime 1x -count 3 -p 1
func BenchmarkCaptureIngestThroughput(b *testing.B) {
	const events = 20000
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	parent := b
	var single *natsserver.Server
	var cluster []*natsserver.Server
	n := 0
	for _, topology := range []string{"R1", "R3"} {
		for _, mode := range []string{"none", "sync", "w=1", "w=128", "w=256"} {
			n++
			instance := fmt.Sprintf("capbench%d", n)
			b.Run(fmt.Sprintf("topology=%s/publish=%s", topology, mode), func(b *testing.B) {
				var srv *natsserver.Server
				replicas := 1
				if topology == "R1" {
					if single == nil {
						single = captureBenchServer(parent)
					}
					srv = single
				} else {
					if cluster == nil {
						cluster = dctest.StartJetStreamCluster(parent, 3)
					}
					srv, replicas = cluster[0], 3
				}
				for iter := 0; iter < b.N; iter++ {
					rate := runCapturePipeline(b, srv, fmt.Sprintf("%s-%d-%d", instance, iter, time.Now().UnixNano()),
						replicas, mode, events)
					b.ReportMetric(rate, "ev/s")
				}
			})
		}
	}
}

// benchInboundMessage is a bench-local copy of what event-sources' main package does to
// turn a decoded event into its inbound-events message (inboundEventMessage), without the
// metric, so every arm is charged the same build.
func benchInboundMessage(source, tenant string, event *model.UnresolvedEvent, payload interface{},
	captureSeq uint64) (context.Context, messaging.Message, bool) {
	event.Source = source
	event.Payload = payload
	bytes, err := esproto.MarshalUnresolvedEvent(event)
	if err != nil {
		return nil, messaging.Message{}, false
	}
	return core.WithTenant(context.Background(), tenant), messaging.Message{
		Key: []byte(event.Device), Value: bytes, DedupID: DedupID(tenant, captureSeq),
	}, true
}

// settleAtOnce is the none arm's writer: every publish succeeds without being sent.
type settleAtOnce struct{ settled *atomic.Int64 }

func (w settleAtOnce) Publish(_ context.Context, _ messaging.Message, done func(error)) {
	done(nil)
	w.settled.Add(1)
}
func (settleAtOnce) Fail(err error, done func(error)) { done(err) }
func (settleAtOnce) Draining()                        {}
func (settleAtOnce) Close()                           {}

// syncPool is the sync arm's writer: a synchronous publish on up to DECODE_WORKER_COUNT
// goroutines at once, which is what the decode workers each publishing and waiting for
// their PubAck amounted to. It reports outcomes out of order, which only a bench may do.
type syncPool struct {
	w     messaging.MessageWriter
	slots chan struct{}
	wg    sync.WaitGroup
}

func (p *syncPool) Publish(ctx context.Context, msg messaging.Message, done func(error)) {
	p.slots <- struct{}{}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		done(p.w.WriteMessages(ctx, msg))
		<-p.slots
	}()
}
func (p *syncPool) Fail(err error, done func(error)) { done(err) }
func (p *syncPool) Draining()                        {}
func (p *syncPool) Close()                           { p.wg.Wait() }

// runCapturePipeline runs one arm and returns events/s.
func runCapturePipeline(b *testing.B, srv *natsserver.Server, instance string, replicas int, mode string, events int) float64 {
	b.Helper()
	b.StopTimer()
	u, err := url.Parse(srv.ClientURL())
	if err != nil {
		b.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		b.Fatal(err)
	}
	var settled atomic.Int64
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: u.Hostname(), Port: uint32(port), StreamReplicas: uint32(replicas)}
	ms.Readiness.MarkReadyWithoutAuthSurface()
	var reader messaging.MessageReader
	var writer messaging.OrderedWriter
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		r, err := n.NewReader(streams.DeviceEventsCapture)
		if err != nil {
			return err
		}
		reader = r
		switch mode {
		case "none":
			writer = settleAtOnce{settled: &settled}
			_, err = n.NewWriter(streams.InboundEvents) // the stream exists in every arm
			return err
		case "sync":
			w, err := n.NewWriter(streams.InboundEvents)
			writer = &syncPool{w: w, slots: make(chan struct{}, DECODE_WORKER_COUNT)}
			return err
		default:
			window, err := strconv.Atoi(mode[len("w="):])
			if err != nil {
				return err
			}
			writer, err = n.NewOrderedWriter(streams.InboundEvents, window)
			return err
		}
	})
	nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	if err := nmgr.Initialize(context.Background()); err != nil {
		b.Fatal(err)
	}
	if err := nmgr.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	defer func() { _ = nmgr.Stop(context.Background()) }()

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		b.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(1024))
	if err != nil {
		b.Fatal(err)
	}
	inboundStream := messaging.StreamName(instance, streams.InboundEvents)
	if replicas > 1 {
		for _, s := range []string{inboundStream, messaging.StreamName(instance, streams.DeviceEventsCapture)} {
			captureBenchSettled(b, js, s)
		}
	}

	for i := 0; i < events; i++ {
		device := i % 1000
		subject := fmt.Sprintf("%s.acme.devices.dev-%d.events", instance, device)
		body := fmt.Sprintf(`{"device":"dev-%d","eventType":"Measurement","occurredTime":"2026-07-20T10:30:00Z",`+
			`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`, device)
		if _, err := js.PublishAsync(subject, []byte(body)); err != nil {
			b.Fatal(err)
		}
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-time.After(60 * time.Second):
		b.Fatal("pre-publishing the captured events did not complete")
	}

	var failedDecodes atomic.Int64
	src := NewGatewayJetStreamSource(nil, "bench", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {}, benchInboundMessage,
		func(string, string, []byte, error) error { failedDecodes.Add(1); return nil },
		nil, admitAllReadings)
	if err := src.Initialize(context.Background()); err != nil {
		b.Fatal(err)
	}
	src.SetReader(reader)
	src.SetWriter(writer)

	b.StartTimer()
	start := time.Now()
	if err := src.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		if failedDecodes.Load() > 0 {
			b.Fatalf("%d captured events failed to decode", failedDecodes.Load())
		}
		if mode == "none" {
			if settled.Load() >= int64(events) {
				break
			}
		} else {
			info, err := js.StreamInfo(inboundStream)
			if err == nil && info.State.Msgs >= uint64(events) {
				break
			}
		}
		if time.Now().After(deadline) {
			b.Fatalf("the %s arm did not reach %d events", mode, events)
		}
		time.Sleep(2 * time.Millisecond)
	}
	elapsed := time.Since(start)
	b.StopTimer()
	_ = src.Stop(context.Background())
	if mode != "none" {
		info, err := js.StreamInfo(inboundStream)
		if err != nil {
			b.Fatal(err)
		}
		if info.State.Msgs != uint64(events) {
			b.Fatalf("inbound-events holds %d, want %d", info.State.Msgs, events)
		}
	}
	return float64(events) / elapsed.Seconds()
}

func captureBenchServer(b *testing.B) *natsserver.Server {
	b.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(b),
	})
	if err != nil {
		b.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		b.Fatal("embedded nats server not ready")
	}
	b.Cleanup(srv.Shutdown)
	return srv
}

// captureBenchSettled waits for a replicated stream to have a leader and two current replicas.
func captureBenchSettled(b *testing.B, js nats.JetStreamContext, stream string) {
	b.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := js.StreamInfo(stream)
		if err == nil && info.Cluster != nil && info.Cluster.Leader != "" && len(info.Cluster.Replicas) == 2 &&
			info.Cluster.Replicas[0].Current && info.Cluster.Replicas[1].Current {
			return
		}
		if time.Now().After(deadline) {
			b.Fatalf("stream %s never settled (last err: %v)", stream, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
