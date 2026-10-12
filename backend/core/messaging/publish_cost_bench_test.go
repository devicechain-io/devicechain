// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog"
)

// BenchmarkSyncPublishCost is one synchronous JetStream publish through the writer, reported
// as wall time per publish and as process CPU per publish (cpu-ns/op).
//
// With DC_BENCH_NATS_URL set to a nats-server running in ANOTHER process, the CPU and any
// profile taken (-cpuprofile) are the client's alone: the publishing goroutine, the
// connection's flusher and the read loop that parses the PubAck. With it unset the broker is
// embedded, and its CPU is in every number.
//
//	go test ./messaging -run '^$' -bench SyncPublishCost -benchtime 20000x -count 10
func BenchmarkSyncPublishCost(b *testing.B) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	nmgr := benchCostManager(b, "pubcost")
	w, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		b.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	payload := make([]byte, 300)
	// Warm the connection, the stream and the response mux before the timer.
	for i := 0; i < 100; i++ {
		if err := w.WriteMessages(ctx, Message{Value: payload}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	cpu0 := ackProcessCPU()
	for i := 0; i < b.N; i++ {
		if err := w.WriteMessages(ctx, Message{Value: payload}); err != nil {
			b.Fatal(err)
		}
	}
	cpu := ackProcessCPU() - cpu0
	b.StopTimer()
	b.ReportMetric(float64(cpu.Nanoseconds())/float64(b.N), "cpu-ns/op")
}

// benchCostManager is a started manager on the nats-server DC_BENCH_NATS_URL names, or on an
// embedded one when it is unset; instance is made unique per process against an external
// server, whose streams outlive the run.
func benchCostManager(b *testing.B, instance string) *NatsManager {
	b.Helper()
	var nmgr *NatsManager
	if raw := os.Getenv("DC_BENCH_NATS_URL"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			b.Fatal(err)
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			b.Fatal(err)
		}
		p, err := strconv.Atoi(port)
		if err != nil {
			b.Fatal(err)
		}
		nmgr = benchManagerAt(b, host, p, instance+strconv.Itoa(os.Getpid()), 1)
	} else {
		srv := benchServer(b)
		addr := srv.Addr().(*net.TCPAddr)
		nmgr = benchManagerAt(b, addr.IP.String(), addr.Port, instance, 1)
	}
	b.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return nmgr
}
