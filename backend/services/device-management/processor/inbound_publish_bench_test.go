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
	u, err := url.Parse(srv.ClientURL())
	if err != nil {
		b.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		b.Fatal(err)
	}
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "device-management", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: u.Hostname(), Port: uint32(port), StreamReplicas: uint32(replicas)}
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
	iproc := newBenchProcessor(b, nmgr, reader, instantApi{}, window, resolvers)
	resolvedStream := messaging.StreamName(instance, streams.ResolvedEvents)
	if replicas > 1 {
		for _, s := range []string{resolvedStream, messaging.StreamName(instance, streams.InboundEvents)} {
			benchSettled(b, js, s)
		}
	}

	subject := messaging.ScopedSubject(instance, "acme", streams.InboundEvents)
	for i := 0; i < events; i++ {
		body, err := esproto.MarshalUnresolvedEvent(benchMeasurement(i % 1000))
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
	b.StartTimer()
	start := time.Now()
	if err := iproc.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		info, err := js.StreamInfo(resolvedStream)
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
	_ = iproc.Stop(context.Background())
	info, err := js.StreamInfo(resolvedStream)
	if err != nil {
		b.Fatal(err)
	}
	if info.State.Msgs != uint64(events) {
		b.Fatalf("resolved-events holds %d, want %d", info.State.Msgs, events)
	}
	return float64(events) / elapsed.Seconds()
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

func (instantApi) ProfileScopeByDeviceType(context.Context, uint) (*dmodel.ProfileScope, error) {
	return &dmodel.ProfileScope{}, nil
}

func (instantApi) MetricDefinitionsByDeviceType(context.Context, uint) ([]*dmodel.MetricDefinition, error) {
	return nil, nil
}

func (instantApi) TrackedRelationshipsForDevice(context.Context, uint) (*dmodel.EntityRelationshipSearchResults, error) {
	return &dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{{
		Model: gorm.Model{ID: 1}, SourceType: "device", SourceId: 1,
		TargetType: "asset", TargetId: 2, TargetToken: "asset-2",
	}}}, nil
}
