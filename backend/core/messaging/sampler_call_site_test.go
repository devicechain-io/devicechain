// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The SAMPLER must report the CONFIGURED replica factor, not the clamped one.
//
// This drives runStreamMetrics — the real goroutine — rather than calling
// metrics.sample with arguments the test chose. That distinction is the whole
// point: an earlier version asserted on a direct sample() call and supplied
// nmgr.desiredStreamReplicas() itself, which restates the production call site
// instead of exercising it. Swapping desired for effective at the two call sites
// in runStreamMetrics left the entire package green.
//
// And that swap is the one that matters. On an unclustered broker the clamp makes
// effective == actual == 1, so reporting effective would make desired == actual
// on exactly the instance whose two HA levers disagree — the alert built on that
// comparison would go quiet precisely where it is supposed to shout.
func TestSamplerReportsConfiguredNotClampedReplicas(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	// A unique area: newStreamMetrics registers into the default registerer, so two
	// managers sharing a FunctionalArea would collide on duplicate registration.
	nmgr.Microservice.FunctionalArea = "samplercallsite"
	nmgr.metrics = newStreamMetrics(nmgr.Microservice)

	// Configured for HA against a single-node broker: the state the alert exists
	// for. The clamp will create the stream at 1; desired must still read 3.
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	name, err := nmgr.ensureStream("inbound-events")
	if err != nil {
		t.Fatalf("ensureStream: %v", err)
	}
	if got := nmgr.effectiveStreamReplicas(); got != 1 {
		t.Fatalf("effectiveStreamReplicas() = %d against an unclustered broker, want the "+
			"clamped 1; without the clamp engaged this test cannot distinguish the two values", got)
	}

	nmgr.stopSampler = make(chan struct{})
	nmgr.samplerWg.Add(1)
	go nmgr.runStreamMetrics()
	defer func() {
		close(nmgr.stopSampler)
		nmgr.samplerWg.Wait()
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		desired := testutil.ToFloat64(nmgr.metrics.replicasDesired.WithLabelValues(name))
		if desired != 0 {
			if desired != 3 {
				t.Fatalf("the sampler reported desired=%v, want the configured 3. Reporting the "+
					"CLAMPED value makes desired == actual on an unclustered broker, so "+
					"JetStreamNotReplicatedAsConfigured can never fire on the very instance "+
					"whose two HA levers disagree", desired)
			}
			if actual := testutil.ToFloat64(nmgr.metrics.replicasActual.WithLabelValues(name)); actual != 1 {
				t.Errorf("actual = %v, want 1 (the clamped value the broker really holds)", actual)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the sampler never populated the replication gauges; runStreamMetrics is " +
				"not reachable from this test, so it asserts nothing about the production path")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A bucket that ALREADY EXISTS must still be tracked for metrics.
//
// This is the path every restarted pod takes — the create branch runs once in an
// instance's life, the existing branch runs on every subsequent start. Only the
// create branch was covered, so deleting the tracking call from the existing
// branch left the package green while meaning dc_leases exports no replication
// series after its first start: the alerts would read `absent()` for the one
// bucket A0 exists to protect, which looks exactly like healthy silence.
//
// It is the same fresh-install-only asymmetry this whole branch was written to
// close, reproduced one layer up in the observability.
func TestExistingBucketIsStillTrackedForMetrics(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	if _, err := nmgr.KeyValueStore(kv.BucketLocks, "restart_tracking_bucket", time.Minute); err != nil {
		t.Fatalf("creating the bucket: %v", err)
	}
	if len(nmgr.trackedBuckets()) == 0 {
		t.Fatal("the create path tracked nothing; the existing-path assertion below would be vacuous")
	}

	// Simulate the restart: same broker, same bucket, a manager that has not seen
	// it before.
	nmgr.streamMu.Lock()
	nmgr.bucketNames = nil
	nmgr.streamMu.Unlock()

	if _, err := nmgr.KeyValueStore(kv.BucketLocks, "restart_tracking_bucket", time.Minute); err != nil {
		t.Fatalf("reopening the existing bucket: %v", err)
	}
	if len(nmgr.trackedBuckets()) == 0 {
		t.Error("reopening an EXISTING bucket tracked nothing, so a restarted pod exports no " +
			"replication series for it. For dc_leases that means the lease-replication alert " +
			"silently has no data after the first start")
	}
}
