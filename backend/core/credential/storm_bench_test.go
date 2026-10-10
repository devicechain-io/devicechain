// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	dctest "github.com/devicechain-io/dc-microservice/test"
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
			}, credential.WithDeviceSecretKey(testDeviceKey(b)))
			if err != nil {
				b.Fatal(err)
			}
			stored := digestOf(b, "s3cret")
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
							ID: fmt.Sprintf("acme:dev-%d-%d", i, d), Tenant: "acme"}
						t0 := time.Now()
						err := c.Check(context.Background(), p, "s3cret", stored)
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

// replicatedKV is an R3 attempt bucket on a three-node in-process JetStream cluster,
// the shared fixture's, so the bucket is created on a cluster already able to place it.
func replicatedKV(b *testing.B) nats.KeyValue {
	b.Helper()
	const nodes = 3
	servers := dctest.StartJetStreamCluster(b, nodes)
	nc, err := nats.Connect(servers[0].ClientURL())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		b.Fatal(err)
	}
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket: "device_credential_attempts_storm", TTL: credential.AttemptTTL, Replicas: nodes,
	})
	if err != nil {
		b.Fatalf("creating the R3 bucket: %v", err)
	}
	return kv
}
