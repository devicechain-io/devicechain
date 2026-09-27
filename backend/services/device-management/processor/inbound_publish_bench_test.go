// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// BenchmarkResolvedPublishThroughput measures the whole inbound pipeline, publish-bound: M
// measurement events pre-published to inbound-events (one tenant, 1000 devices), read by the
// real durable reader, resolved by the real resolver pool against an API that answers at
// once, and published to resolved-events by the real writer. Timing ends when resolved-events
// holds all M.
//
// Arms: topology (R1 single server, R3 3-node cluster) × resolver pool width × publish window
// (1 behaves as a serial writer). In-process over loopback: the absolute numbers are a FLOOR,
// the ratios are the result. (The broker's memory for the dedup ids every resolved publish
// now carries is measured in isolation by core/messaging's BenchmarkPublishThroughput; here
// it is lost in the heap of the pipeline around it.)
//
// Run with: go test ./processor -run '^$' -bench BenchmarkResolvedPublishThroughput -benchtime 1x -p 1
func BenchmarkResolvedPublishThroughput(b *testing.B) {
	const events = 20000
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	parent := b
	var single *natsserver.Server
	var cluster []*natsserver.Server
	n := 0
	for _, topology := range []string{"R1", "R3"} {
		for _, resolvers := range []int{1, 5, 20} {
			for _, window := range []int{1, 128, 256} {
				n++
				instance := fmt.Sprintf("bench%d", n)
				name := fmt.Sprintf("topology=%s/resolvers=%d/window=%d", topology, resolvers, window)
				b.Run(name, func(b *testing.B) {
					var srv *natsserver.Server
					replicas := 1
					if topology == "R1" {
						if single == nil {
							single = benchNatsServer(parent)
						}
						srv = single
					} else {
						if cluster == nil {
							cluster = dctest.StartJetStreamCluster(parent, 3)
						}
						srv, replicas = cluster[0], 3
					}
					for iter := 0; iter < b.N; iter++ {
						rate := runResolvePipeline(b, srv, fmt.Sprintf("%s-%d", instance, iter), replicas, resolvers, window, events)
						b.ReportMetric(rate, "ev/s")
					}
				})
			}
		}
	}
}

// runResolvePipeline runs one arm and returns events/s.
func runResolvePipeline(b *testing.B, srv *natsserver.Server, instance string, replicas, resolvers, window, events int) float64 {
	b.Helper()
	b.StopTimer()
	p := startResolvePipeline(b, pipelineSpec{
		srv: srv, instance: instance, replicas: replicas, resolvers: resolvers, window: window,
		events: events, api: instantApi{}, authMode: config.AuthModeOptional,
		event: func(i int) *esmodel.UnresolvedEvent { return benchMeasurement(i % 1000) },
	})
	b.StartTimer()
	start := time.Now()
	if err := p.iproc.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		info, err := p.js.StreamInfo(p.resolvedStream)
		if err == nil && info.State.Msgs >= uint64(events) {
			break
		}
		if time.Now().After(deadline) {
			b.Fatalf("resolved-events did not reach %d messages", events)
		}
		time.Sleep(2 * time.Millisecond)
	}
	elapsed := time.Since(start)
	b.StopTimer()
	_ = p.iproc.Stop(context.Background())
	info, err := p.js.StreamInfo(p.resolvedStream)
	if err != nil {
		b.Fatal(err)
	}
	if info.State.Msgs != uint64(events) {
		b.Fatalf("resolved-events holds %d, want %d", info.State.Msgs, events)
	}
	return float64(events) / elapsed.Seconds()
}

// pipelineSpec is one arm of a pipeline benchmark: where it runs, how wide, and what it
// resolves. The wrap hooks let a benchmark put its own instruments around the reader and the
// resolved writer the processor is built over; nil leaves them as main.go builds them.
type pipelineSpec struct {
	srv                         *natsserver.Server
	instance                    string
	replicas, resolvers, window int
	events                      int
	api                         dmodel.DeviceManagementApi
	authMode                    string
	event                       func(i int) *esmodel.UnresolvedEvent
	registry                    *prometheus.Registry
	wrapReader                  func(messaging.MessageReader) messaging.MessageReader
	wrapResolved                func(messaging.OrderedWriter) messaging.OrderedWriter
}

// resolvePipeline is a pipeline started by startResolvePipeline: its inbound events
// published and its processor initialized, but not yet started.
type resolvePipeline struct {
	iproc                         *InboundEventsProcessor
	js                            nats.JetStreamContext
	inboundStream, resolvedStream string
}

// startResolvePipeline builds the real durable reader and writers on spec.srv, pre-publishes
// spec.events inbound events (event i is spec.event(i), stored at stream sequence i+1), and
// returns the processor initialized, ready for Start. What it opens is closed by the
// benchmark's cleanup.
func startResolvePipeline(b *testing.B, spec pipelineSpec) *resolvePipeline {
	b.Helper()
	u, err := url.Parse(spec.srv.ClientURL())
	if err != nil {
		b.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		b.Fatal(err)
	}
	ms := &core.Microservice{InstanceId: spec.instance, FunctionalArea: "device-management", Readiness: core.NewReadinessGate()}
	if spec.registry != nil {
		ms.UseMetricsRegistry(spec.registry)
	}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: u.Hostname(), Port: uint32(port), StreamReplicas: uint32(spec.replicas)}
	ms.Readiness.MarkReadyWithoutAuthSurface()
	var reader messaging.MessageReader
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		r, err := n.NewReader(streams.InboundEvents)
		reader = r
		return err
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
	b.Cleanup(func() { _ = nmgr.Stop(context.Background()) })

	nc, err := nats.Connect(spec.srv.ClientURL())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(1024))
	if err != nil {
		b.Fatal(err)
	}
	if spec.wrapReader != nil {
		reader = spec.wrapReader(reader)
	}
	iproc := newBenchProcessor(b, nmgr, reader, spec.api, spec.window, spec.resolvers)
	iproc.AuthMode = spec.authMode
	if spec.wrapResolved != nil {
		iproc.ResolvedEventsWriter = spec.wrapResolved(iproc.ResolvedEventsWriter)
	}
	p := &resolvePipeline{
		iproc:          iproc,
		js:             js,
		inboundStream:  messaging.StreamName(spec.instance, streams.InboundEvents),
		resolvedStream: messaging.StreamName(spec.instance, streams.ResolvedEvents),
	}
	if spec.replicas > 1 {
		for _, s := range []string{p.resolvedStream, p.inboundStream} {
			benchSettled(b, js, s)
		}
	}

	subject := messaging.ScopedSubject(spec.instance, "acme", streams.InboundEvents)
	for i := 0; i < spec.events; i++ {
		body, err := esproto.MarshalUnresolvedEvent(spec.event(i))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := js.PublishAsync(subject, body); err != nil {
			b.Fatal(err)
		}
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-time.After(60 * time.Second):
		b.Fatal("pre-publishing the inbound events did not complete")
	}

	if err := iproc.Initialize(context.Background()); err != nil {
		b.Fatal(err)
	}
	return p
}

func benchMeasurement(device int) *esmodel.UnresolvedEvent {
	return &esmodel.UnresolvedEvent{
		Source:       "bench",
		Device:       fmt.Sprintf("dev-%d", device),
		EventType:    esmodel.Measurement,
		OccurredTime: time.Now(),
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{
			{Measurements: map[string]string{"temp": "21.5"}},
		}},
	}
}

func benchNatsServer(b *testing.B) *natsserver.Server {
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

// benchSettled waits for a replicated stream to have a leader and two current replicas.
func benchSettled(b *testing.B, js nats.JetStreamContext, stream string) {
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

// instantApi answers exactly what resolving a measurement event asks, at once, with one
// tracked relationship per device and no metric definitions or scoped groups.
//
// It embeds the interface, so any call it does not override PANICS (a nil interface): a
// change in what resolution calls is loud here, never silently slow. It is deliberately not
// the testify mock, which records every call and would dominate the measurement.
type instantApi struct {
	dmodel.DeviceManagementApi
}

func (instantApi) DevicesByToken(_ context.Context, tokens []string) ([]*dmodel.Device, error) {
	out := make([]*dmodel.Device, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, &dmodel.Device{
			Model:          gorm.Model{ID: 1},
			TokenReference: rdb.TokenReference{Token: t},
			DeviceTypeId:   7,
		})
	}
	return out, nil
}

func (instantApi) AnyScopedGroups(context.Context) (bool, error) { return false, nil }

// ProfileResolutionByDeviceType answers an unscoped profile that declares no metrics.
func (instantApi) ProfileResolutionByDeviceType(context.Context, uint) (*dmodel.ProfileResolution, error) {
	return dmodel.NewProfileResolution(dmodel.ProfileScope{}, nil), nil
}

func (instantApi) TrackedRelationshipsForDevice(context.Context, uint) (*dmodel.EntityRelationshipSearchResults, error) {
	return &dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{{
		Model: gorm.Model{ID: 1}, SourceType: "device", SourceId: 1,
		TargetType: "asset", TargetId: 2, TargetToken: "asset-2",
	}}}, nil
}
