// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/rs/zerolog"
)

// BenchmarkPublishThroughput measures the publish stage alone: M messages of 512 bytes to
// resolved-events, through the synchronous writer (serial: one publish, one PubAck, then the
// next) and through the ordered writer at several windows, on a single server (R1) and on a
// 3-node cluster with a replicated stream (R3). In-process over loopback, so the absolute
// numbers are a FLOOR on real round trips; the ratios are the result.
//
// It reports:
//   - ev/s: messages stored per second of wall time;
//   - p50-us / p99-us: per message, from the call that submitted it to its outcome — for the
//     ordered writer that includes waiting for room in the window;
//   - heap-MB: live heap after the run and a GC, less before it (server and client share the
//     process), with and without a Nats-Msg-Id on every message, which is what the broker's
//     duplicate window costs.
//
// Run with: go test ./messaging -run '^$' -bench BenchmarkPublishThroughput -benchtime 1x -p 1
func BenchmarkPublishThroughput(b *testing.B) {
	const messages = 20000
	// The connection and lifecycle logging of each arm's manager would interleave with the
	// result lines; only errors are wanted here.
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })
	type arm struct {
		topology string
		window   int // 0 = the synchronous writer
		dedup    bool
	}
	arms := []arm{
		{"R1", 0, false}, {"R1", 128, false}, {"R1", 128, true},
		{"R3", 0, false}, {"R3", 1, false}, {"R3", 16, false}, {"R3", 64, false},
		{"R3", 128, false}, {"R3", 256, false}, {"R3", 128, true},
	}
	// The servers belong to the parent benchmark: a sub-benchmark's cleanup would shut them
	// down under the next arm.
	parent := b
	var single *natsserver.Server
	var cluster []*natsserver.Server
	for n, a := range arms {
		writer := "serial"
		if a.window > 0 {
			writer = fmt.Sprintf("window=%d", a.window)
		}
		name := fmt.Sprintf("topology=%s/writer=%s/dedup=%t", a.topology, writer, a.dedup)
		b.Run(name, func(b *testing.B) {
			var srv *natsserver.Server
			replicas := 1
			if a.topology == "R1" {
				if single == nil {
					single = benchServer(parent)
				}
				srv = single
			} else {
				if cluster == nil {
					cluster = dctest.StartJetStreamCluster(parent, 3)
				}
				srv, replicas = cluster[0], 3
			}
			nmgr := benchManager(b, srv, fmt.Sprintf("bench%d", n), replicas)
			stream := StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)
			payload := make([]byte, 512)
			ctx := core.WithTenant(context.Background(), "acme")
			msg := func(i int) Message {
				m := Message{Value: payload}
				if a.dedup {
					m.DedupID = "acme:" + strconv.Itoa(i)
				}
				return m
			}

			var sync MessageWriter
			var ordered OrderedWriter
			var err error
			if a.window == 0 {
				sync, err = nmgr.NewWriter(streams.ResolvedEvents)
			} else {
				ordered, err = nmgr.NewOrderedWriter(streams.ResolvedEvents, a.window)
			}
			if err != nil {
				b.Fatal(err)
			}
			probes := uint64(0)
			if replicas > 1 {
				benchWaitForLeader(b, nmgr, stream)
				benchProbe(b, nmgr)
				probes = 1
			}

			heapBefore := liveHeap()
			lat := make([]time.Duration, messages)
			var failed atomic.Int64
			b.ResetTimer()
			start := time.Now()
			for iter := 0; iter < b.N; iter++ {
				for i := 0; i < messages; i++ {
					t0 := time.Now()
					if sync != nil {
						if err := sync.WriteMessages(ctx, msg(iter*messages+i)); err != nil {
							failed.Add(1)
						}
						lat[i] = time.Since(t0)
						continue
					}
					i := i
					ordered.Publish(ctx, msg(iter*messages+i), func(err error) {
						if err != nil {
							failed.Add(1)
						}
						lat[i] = time.Since(t0)
					})
				}
				if ordered != nil && iter == b.N-1 {
					ordered.Close()
				}
			}
			elapsed := time.Since(start)
			b.StopTimer()
			if n := failed.Load(); n != 0 {
				b.Fatalf("%d publishes failed", n)
			}
			info, err := nmgr.js.StreamInfo(stream)
			if err != nil {
				b.Fatal(err)
			}
			if want := uint64(b.N*messages) + probes; info.State.Msgs != want {
				b.Fatalf("stored %d, want %d", info.State.Msgs, want)
			}
			heapAfter := liveHeap()
			slices.Sort(lat)
			b.ReportMetric(float64(b.N*messages)/elapsed.Seconds(), "ev/s")
			b.ReportMetric(float64(lat[len(lat)/2].Microseconds()), "p50-us")
			b.ReportMetric(float64(lat[len(lat)*99/100].Microseconds()), "p99-us")
			b.ReportMetric(float64(int64(heapAfter)-int64(heapBefore))/(1<<20), "heap-MB")
			_ = nmgr.Stop(context.Background())
		})
	}
}

// liveHeap is the live heap after a GC.
func liveHeap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func benchServer(b *testing.B) *natsserver.Server {
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

// benchManager is a started manager on srv under its own instance id (so each arm gets a
// fresh stream), configured for replicas.
func benchManager(b *testing.B, srv *natsserver.Server, instance string, replicas int) *NatsManager {
	b.Helper()
	addr := srv.Addr().(*net.TCPAddr)
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "bench", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = addr.IP.String()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(addr.Port)
	ms.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = uint32(replicas)
	ms.Readiness.MarkReadyWithoutAuthSurface()
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	nmgr.RecordMaxDeliveries(recordNothing)
	if err := nmgr.Initialize(context.Background()); err != nil {
		b.Fatal(err)
	}
	if err := nmgr.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	return nmgr
}

// benchProbe stores one message through a synchronous writer, retrying until the broker
// accepts it: a new replicated stream can have a leader before the server this client is on
// sees its interest, and a publish in that window is answered "no responders".
func benchProbe(b *testing.B, nmgr *NatsManager) {
	b.Helper()
	w, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		b.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := w.WriteMessages(core.WithTenant(context.Background(), "probe"), Message{Value: []byte("probe")})
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			b.Fatalf("resolved-events never accepted a publish: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func benchWaitForLeader(b *testing.B, nmgr *NatsManager, stream string) {
	b.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := nmgr.js.StreamInfo(stream)
		if err == nil && info.Cluster != nil && info.Cluster.Leader != "" && len(info.Cluster.Replicas) == 2 {
			current := true
			for _, r := range info.Cluster.Replicas {
				current = current && r.Current
			}
			if current {
				return
			}
		}
		if time.Now().After(deadline) {
			b.Fatalf("stream %s never settled with a leader and two current replicas (last err: %v)", stream, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
