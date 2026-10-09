// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"runtime"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// BenchmarkAckCost measures what acknowledging costs the whole process (client and the
// embedded broker share it) when a consumer acknowledges every message, against when it
// acknowledges only a floor with AckAll every 64 or every 1,000 messages.
//
// Each arm consumes the same 100,000 x 300 B stream through a pull consumer and is
// reported as process CPU time per delivered message (cpu-ns/msg) beside the usual ns/op
// and allocs. The timed region is the consume: from the first fetch until the consumer
// reports nothing ack-pending, so acknowledgements still queued at the broker are paid
// for inside it. Publishing is outside the timer.
//
// R1 runs one embedded server. R3 runs a three-node embedded cluster with a three-replica
// stream, where an ack is a consumer-group proposal rather than a local write. The R3
// arm builds its own plain three-node cluster with no fault proxies: the shared cluster
// fixture needs a refusing route address that exists only on unix, and this benchmark has
// no use for its fault injection.
//
//	go test -run '^$' -bench AckCost -count=5 -benchmem ./messaging/
func BenchmarkAckCost(b *testing.B) {
	arms := []struct {
		name   string
		policy jetstream.AckPolicy
		every  int // acknowledge every message when 1; otherwise the floor message every N
	}{
		{"explicit-each", jetstream.AckExplicitPolicy, 1},
		{"ackall-64", jetstream.AckAllPolicy, 64},
		{"ackall-1000", jetstream.AckAllPolicy, 1000},
	}
	for _, topo := range []struct {
		name     string
		replicas int
	}{{"R1", 1}, {"R3", 3}} {
		for _, arm := range arms {
			b.Run(topo.name+"/"+arm.name, func(b *testing.B) {
				var url string
				if topo.replicas == 1 {
					url = ackBenchSingle(b).ClientURL()
				} else {
					url = ackBenchCluster(b).ClientURL()
				}
				runAckCostArm(b, url, topo.replicas, arm.policy, arm.every)
			})
		}
	}
}

func ackBenchSingle(b *testing.B) *natsserver.Server {
	b.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  dctest.JetStreamStoreDir(b),
		NoLog:     true,
		NoSigs:    true,
	})
	if err != nil {
		b.Fatalf("new embedded server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(15 * time.Second) {
		b.Fatal("embedded server not ready")
	}
	b.Cleanup(srv.Shutdown)
	return srv
}

// ackBenchCluster starts a plain three-node JetStream cluster on loopback and returns the
// first server once a three-replica stream can be placed. A clustered server holds back
// its listeners until it can reach a meta quorum, so every server is given the route
// ports of the other two up front, and all three are started before any is waited on.
func ackBenchCluster(b *testing.B) *natsserver.Server {
	b.Helper()
	name := fmt.Sprintf("ackbench-%d", os.Getpid())
	ports := make([]int, 3)
	for i := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			b.Fatalf("pick a route port: %v", err)
		}
		ports[i] = l.Addr().(*net.TCPAddr).Port
		l.Close()
	}
	var servers []*natsserver.Server
	for i := 0; i < 3; i++ {
		var routes []*url.URL
		for j, port := range ports {
			if j != i {
				routes = append(routes, &url.URL{Scheme: "nats-route", Host: fmt.Sprintf("127.0.0.1:%d", port)})
			}
		}
		srv, err := natsserver.NewServer(&natsserver.Options{
			ServerName: fmt.Sprintf("ackbench-%d", i),
			Host:       "127.0.0.1",
			Port:       -1,
			JetStream:  true,
			StoreDir:   dctest.JetStreamStoreDir(b),
			NoLog:      true,
			NoSigs:     true,
			Cluster:    natsserver.ClusterOpts{Name: name, Host: "127.0.0.1", Port: ports[i]},
			Routes:     routes,
		})
		if err != nil {
			b.Fatalf("new cluster server %d: %v", i, err)
		}
		go srv.Start()
		b.Cleanup(srv.Shutdown)
		servers = append(servers, srv)
	}
	for i, srv := range servers {
		if !srv.ReadyForConnections(60 * time.Second) {
			b.Fatalf("cluster server %d not ready", i)
		}
	}
	// A meta leader and a placed three-replica stream are the readiness that matters:
	// poll until one can be created on the first server.
	nc, err := nats.Connect(servers[0].ClientURL())
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		b.Fatalf("jetstream: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := js.CreateStream(ctx, jetstream.StreamConfig{
			Name: "ACKBENCH_PROBE", Subjects: []string{"ackbenchprobe.>"}, Replicas: 3,
		})
		if err == nil {
			err = js.DeleteStream(ctx, "ACKBENCH_PROBE")
		}
		cancel()
		if err == nil {
			return servers[0]
		}
		if time.Now().After(deadline) {
			b.Fatalf("cluster did not form: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

const (
	ackBenchMessages = 100_000
	ackBenchPayload  = 300
	ackBenchFetch    = 256
)

func runAckCostArm(b *testing.B, url string, replicas int, policy jetstream.AckPolicy, every int) {
	b.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	b.Cleanup(nc.Close)
	js, err := jetstream.New(nc, jetstream.WithPublishAsyncMaxPending(4096))
	if err != nil {
		b.Fatalf("jetstream: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "ACKBENCH", Subjects: []string{"ackbench.>"}, Replicas: replicas,
		Storage: jetstream.FileStorage,
	}); err != nil {
		b.Fatalf("create stream: %v", err)
	}
	payload := make([]byte, ackBenchPayload)
	for i := 0; i < ackBenchMessages; i++ {
		if _, err := js.PublishAsync("ackbench.x", payload, jetstream.WithStallWait(time.Minute)); err != nil {
			b.Fatalf("publish: %v", err)
		}
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-ctx.Done():
		b.Fatal("publish did not complete")
	}
	cons, err := js.CreateConsumer(ctx, "ACKBENCH", jetstream.ConsumerConfig{
		Durable: "bench", AckPolicy: policy, AckWait: 5 * time.Minute,
		MaxAckPending: 65536, Replicas: replicas,
	})
	if err != nil {
		b.Fatalf("create consumer: %v", err)
	}
	// Settle the broker before the clock starts.
	runtime.GC()
	time.Sleep(500 * time.Millisecond)

	b.ReportAllocs()
	b.ResetTimer()
	cpu0 := ackProcessCPU()
	got := 0
	for got < ackBenchMessages {
		batch, err := cons.Fetch(ackBenchFetch, jetstream.FetchMaxWait(10*time.Second))
		if err != nil {
			b.Fatalf("fetch: %v", err)
		}
		for m := range batch.Messages() {
			got++
			if every == 1 || got%every == 0 || got == ackBenchMessages {
				if err := m.Ack(); err != nil {
					b.Fatalf("ack: %v", err)
				}
			}
		}
		if err := batch.Error(); err != nil {
			b.Fatalf("batch: %v", err)
		}
	}
	// Acknowledgements are fire and forget; the consume is not done until the broker
	// has applied them.
	if err := nc.Flush(); err != nil {
		b.Fatalf("flush: %v", err)
	}
	for {
		info, err := cons.Info(ctx)
		if err != nil {
			b.Fatalf("info: %v", err)
		}
		if info.NumAckPending == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cpu := ackProcessCPU() - cpu0
	b.StopTimer()
	b.ReportMetric(float64(cpu.Nanoseconds())/float64(got), "cpu-ns/msg")
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(got), "wall-ns/msg")
	if testing.Verbose() {
		fmt.Printf("# %s: %d messages\n", b.Name(), got)
	}
}
