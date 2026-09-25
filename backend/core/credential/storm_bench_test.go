// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// BenchmarkDeviceConnectStormOnReplicatedJetStream measures what a reconnect storm
// costs the MQTT auth callout's credential check: every device of a fleet presenting
// the RIGHT password at once, each check a read, a charge and a delete against an R3
// attempt bucket on a three-node in-process JetStream cluster. The callout runs one
// goroutine per request with no bound, so concurrency here is the whole fleet.
//
// It reports the p50/p99/max latency of one check, which is what has to stay well
// inside the broker's authorization timeout (5s as deployed) for a storm not to refuse
// itself. It is a benchmark rather than a test because its number depends on the
// machine: run it with
//
//	go test ./credential -run '^$' -bench DeviceConnectStorm -benchtime 1x
//
// An in-process cluster has no network latency and no disk contention from anything
// else, so the number is a floor for a real cluster, not a prediction of one.
//
// What it showed when the throttle was added (one i9-10900K, all three nodes and the
// checker in one process): every check in a storm waits behind the others on the one
// connection, so latency grows with the storm, at roughly 45 microseconds a check —
// p99 47ms for 1,000 simultaneous connects, 224ms for 5,000, 932ms for 20,000. So on
// that floor, a storm of about a hundred thousand password connects arriving at ONE
// replica in the same instant would reach the 5s authorization timeout. Rerun it
// rather than trusting those figures on other hardware.
func BenchmarkDeviceConnectStormOnReplicatedJetStream(b *testing.B) {
	for _, fleet := range []int{1_000, 5_000, 20_000} {
		b.Run(fmt.Sprintf("fleet=%d", fleet), func(b *testing.B) {
			kv := replicatedKV(b)
			c, err := credential.NewChecker(kv, map[credential.Kind]credential.Policy{
				credential.KindDeviceCredential: {Free: 10, Base: time.Second, Cap: 30 * time.Second},
			})
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < b.N; i++ {
				lat := make([]time.Duration, fleet)
				var failed sync.Map
				var wg sync.WaitGroup
				start := make(chan struct{})
				for d := 0; d < fleet; d++ {
					wg.Add(1)
					go func(d int) {
						defer wg.Done()
						<-start
						p := credential.Principal{Kind: credential.KindDeviceCredential,
							ID: fmt.Sprintf("acme:dev-%d-%d", i, d)}
						t0 := time.Now()
						err := c.Check(context.Background(), p, "s3cret",
							func(context.Context) (string, error) { return "s3cret", nil })
						lat[d] = time.Since(t0)
						if err != nil {
							failed.Store(d, err)
						}
					}(d)
				}
				close(start)
				wg.Wait()
				failed.Range(func(k, v any) bool {
					b.Fatalf("device %v was refused in the storm: %v", k, v)
					return false
				})
				slices.Sort(lat)
				b.ReportMetric(float64(lat[len(lat)/2].Milliseconds()), "p50-ms")
				b.ReportMetric(float64(lat[len(lat)*99/100].Milliseconds()), "p99-ms")
				b.ReportMetric(float64(lat[len(lat)-1].Milliseconds()), "max-ms")
			}
		})
	}
}

// replicatedKV is an R3 attempt bucket on a three-node in-process JetStream cluster.
func replicatedKV(b *testing.B) nats.KeyValue {
	b.Helper()
	const nodes = 3
	ports := make([]int, nodes)
	for i := range ports {
		ports[i] = 17_222 + i
	}
	var servers []*natsserver.Server
	for i := 0; i < nodes; i++ {
		var routes []string
		for j, p := range ports {
			if j != i {
				routes = append(routes, fmt.Sprintf("nats://127.0.0.1:%d", p))
			}
		}
		opts := &natsserver.Options{
			ServerName: fmt.Sprintf("n%d", i), Host: "127.0.0.1", Port: -1,
			JetStream: true, StoreDir: b.TempDir(), NoLog: true, NoSigs: true,
			Cluster: natsserver.ClusterOpts{Name: "storm", Host: "127.0.0.1", Port: ports[i]},
			Routes:  natsserver.RoutesFromStr(strings.Join(routes, ",")),
		}
		srv, err := natsserver.NewServer(opts)
		if err != nil {
			b.Fatal(err)
		}
		go srv.Start()
		b.Cleanup(srv.Shutdown)
		servers = append(servers, srv)
	}
	for _, s := range servers {
		if !s.ReadyForConnections(20 * time.Second) {
			b.Fatal("a cluster node never became ready")
		}
	}
	nc, err := nats.Connect(servers[0].ClientURL())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		b.Fatal(err)
	}
	// The meta leader takes a moment to elect; retry the create until it has.
	deadline := time.Now().Add(30 * time.Second)
	for {
		kv, err := js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket: "device_credential_attempts_storm", TTL: credential.AttemptTTL, Replicas: nodes,
		})
		if err == nil {
			return kv
		}
		if time.Now().After(deadline) {
			b.Fatalf("creating the R3 bucket: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
