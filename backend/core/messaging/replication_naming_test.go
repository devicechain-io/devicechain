// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The ADR-020 A0 replication check can only be as good as its names. If
// ReplicationExpectation looks for a bucket under a name the runtime stopped
// using, the check reports it MISSING forever — and the realistic response to a
// permanently red assertion is that someone relaxes it, at which point the object
// leaves the check entirely.
//
// So these cases assert the names against WHAT THE RUNTIME ACTUALLY CREATED on a
// live broker, never against a second copy of the format string. A test that
// spells "%s_%s" a second time passes whatever the first one does.

// TestExpectationCoversTheBucketsTheRuntimeCreates creates a lease, a lock and a
// cache through the ordinary constructors, then asks Collect — driven by the
// expectation, with no help — to find them.
//
// Collect returns only objects the expectation puts in scope. So a bucket the
// runtime made and the expectation cannot name simply does not appear, which is
// exactly the drift being guarded, and it is invisible from either side alone.
func TestExpectationCoversTheBucketsTheRuntimeCreates(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	const instance = "test" // newTestManager's InstanceId

	if _, err := nmgr.NewDistributedLease(DefaultLeaseTTL); err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	if _, err := nmgr.NewDistributedLock(5 * time.Second); err != nil {
		t.Fatalf("NewDistributedLock: %v", err)
	}
	if _, err := nmgr.NewCache(kv.BucketDeviceByToken, time.Minute); err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	exp := ReplicationExpectation(instance, 1, nil)
	snap, err := replication.Collect(nmgr.js, exp)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snap.Objects) == 0 {
		t.Fatal("Collect found nothing after three buckets were created; the expectation " +
			"puts none of the runtime's own objects in scope")
	}

	// Every KV backing stream that exists on this broker was created by the calls
	// above, so every one of them must have been collected. Comparing against the
	// broker's own listing rather than a hard-coded list is what makes this an
	// observation.
	onBroker := map[string]bool{}
	for si := range nmgr.js.StreamsInfo() {
		if si != nil && strings.HasPrefix(si.Config.Name, "KV_") {
			onBroker[si.Config.Name] = true
		}
	}
	if len(onBroker) < 3 {
		t.Fatalf("expected at least the lease, lock and cache backing streams on the "+
			"broker; found %v", onBroker)
	}
	collected := map[string]bool{}
	for _, o := range snap.Objects {
		collected[o.Name] = true
	}
	for name := range onBroker {
		if !collected[name] {
			t.Errorf("the runtime created KV backing stream %q and the expectation does "+
				"not put it in scope, so the replication check silently never looks at "+
				"it. Names must be built in ONE place (messaging.ReplicationExpectation "+
				"and the constructor that made this bucket)", name)
		}
	}
}

// TestLeaseBucketIsNamedAsTheRuntimeCreatedIt pins the A.2 named assertion to the
// bucket NewDistributedLease actually made.
//
// dc_leases gets its own check because it is the ADR-070 fence substrate — a
// silent fallback to single-replica there is what lets two owners of a partition
// write over each other. That check is worth exactly as much as its name being
// right, and nothing else in the suite would notice if it were not: a generic
// sweep would still cover the bucket, so the named assertion could point at
// nothing and every run would stay green.
func TestLeaseBucketIsNamedAsTheRuntimeCreatedIt(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()

	if _, err := nmgr.NewDistributedLease(DefaultLeaseTTL); err != nil {
		t.Fatalf("NewDistributedLease: %v", err)
	}
	// Find the lease bucket's backing stream by looking at the broker, not by
	// rebuilding the name.
	var found []string
	for si := range nmgr.js.StreamsInfo() {
		if si != nil && strings.HasPrefix(si.Config.Name, "KV_") {
			found = append(found, si.Config.Name)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one KV backing stream after creating only a lease; got %v", found)
	}
	exp := ReplicationExpectation("test", 3, nil)
	if exp.LeaseBucket != found[0] {
		t.Fatalf("the A.2 lease-bucket assertion names %q but NewDistributedLease "+
			"created %q — the highest-consequence check in the suite is pointed at an "+
			"object that does not exist", exp.LeaseBucket, found[0])
	}
}

// TestExpectationNamesEveryDeclaredStream keeps the required list tied to the
// stream inventory rather than to a list someone remembers to extend. A stream
// added to streams.All and not here would be replicated or not with nothing
// watching, and the suite would report a confident PASS over the other sixteen.
func TestExpectationNamesEveryDeclaredStream(t *testing.T) {
	exp := ReplicationExpectation("inst", 3, nil)
	if len(exp.Streams) != len(streams.All) {
		t.Fatalf("the expectation names %d stream(s) but the platform declares %d",
			len(exp.Streams), len(streams.All))
	}
	named := map[string]bool{}
	for _, s := range exp.Streams {
		named[s] = true
	}
	for _, s := range streams.All {
		if want := StreamName("inst", s.Suffix); !named[want] {
			t.Errorf("stream %q (suffix %q) is declared in streams.All but the "+
				"replication check does not require it", want, s.Suffix)
		}
	}
}

// TestExpectationCoversEveryCacheBucket does the same for the KV inventory.
func TestExpectationCoversEveryCacheBucket(t *testing.T) {
	exp := ReplicationExpectation("inst", 3, nil)
	for _, b := range kv.All {
		if b.Tier != kv.Cache {
			continue
		}
		// The concrete name carries a functional area, so the expectation matches on
		// the trailing logical name. Assert the suffix it uses actually matches what
		// CacheBucketName builds — the runtime's own construction, not a second copy.
		concrete := KvStreamName(CacheBucketName("inst", "some-area", b.Name))
		matched := false
		for _, suffix := range exp.CacheBucketSuffixes {
			if strings.HasSuffix(concrete, suffix) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("cache bucket %q would be created as %q, which matches none of the "+
				"suffixes the replication check looks for (%v)", b.Name, concrete,
				exp.CacheBucketSuffixes)
		}
	}
}

// TestExpectationRequiresTheMqttLever pins that the four broker-owned MQTT
// streams are REQUIRED rather than checked-if-present.
//
// Their replica factor is the one this platform does not set: nats-server decides
// it once, at the first MQTT connect after a broker start. If no device has ever
// connected the streams do not exist — and treating that as a pass would mean the
// lever A0 exists to move was never moved, reported as success.
func TestExpectationRequiresTheMqttLever(t *testing.T) {
	exp := ReplicationExpectation("inst", 3, nil)
	want := map[string]bool{
		"$MQTT_sess": true, "$MQTT_msgs": true, "$MQTT_qos2in": true, "$MQTT_out": true,
	}
	for _, n := range exp.MqttStreams {
		delete(want, n)
	}
	if len(want) != 0 {
		t.Fatalf("these broker MQTT streams are not required by the check: %v", want)
	}
	// Absent, they must FAIL rather than be skipped. Asserting the behaviour, not
	// the list, is what stops a later refactor turning them optional.
	snap := replication.Snapshot{
		Objects:   []replication.Object{{Name: "unrelated", Replicas: 3}},
		Consumers: []replication.Consumer{{Stream: "unrelated", Name: "c"}},
	}
	rep := replication.Verify(snap, exp)
	mqtt := 0
	for _, f := range rep.Findings {
		if f.Check == "A4" {
			mqtt++
		}
	}
	if mqtt != len(exp.MqttStreams) {
		t.Fatalf("expected every absent MQTT stream to be reported; got %d finding(s) "+
			"for %d stream(s):\n%s", mqtt, len(exp.MqttStreams), rep.Format())
	}
}

// TestUndeployedAreasStreamsAreNotRequired covers the reason ReplicationExpectation
// takes a deployed-area set at all.
//
// connector-dispatch-dead is created only by outbound-connectors, which the
// `default` profile does not run. Requiring it unconditionally makes the check
// report a failure on every healthy default install — and the realistic response
// to an assertion that is red on a working system is to stop running it, which
// costs more than the assertion ever caught.
func TestUndeployedAreasStreamsAreNotRequired(t *testing.T) {
	deployed := []string{
		"user-management", "device-management", "event-sources", "event-management",
		"device-state", "dashboard-management", "command-delivery",
		"notification-management", "event-processing",
	} // the `default` profile
	exp := ReplicationExpectation("inst", 3, deployed)

	required := map[string]bool{}
	for _, s := range exp.Streams {
		required[s] = true
	}
	for _, st := range streams.All {
		name := StreamName("inst", st.Suffix)
		anyDeployed := false
		for _, owner := range st.Areas {
			for _, d := range deployed {
				if owner == d {
					anyDeployed = true
				}
			}
		}
		if anyDeployed && !required[name] {
			t.Errorf("stream %q is created by a DEPLOYED area (%v) but is not required",
				name, st.Areas)
		}
		if !anyDeployed && required[name] {
			t.Errorf("stream %q is created only by undeployed areas (%v) but is required "+
				"present; it cannot exist, so this would fail every healthy install",
				name, st.Areas)
		}
	}
	// Non-vacuity: if this list ever covers everything, the case above is asserting
	// only its trivial half and the gating could be broken without notice.
	if len(exp.Streams) == len(streams.All) {
		t.Fatal("the default profile is expected to leave at least one stream " +
			"unrequired (connector-dispatch-dead, owned solely by outbound-connectors); " +
			"if that is no longer true this test no longer exercises the gating")
	}
}

// TestEveryStreamDeclaresItsOwners keeps the ownership annotations from silently
// going missing. A stream with no owners is treated as always-required, which is
// the safe direction — but it is safe by accident rather than by declaration, and
// this makes the omission visible.
func TestEveryStreamDeclaresItsOwners(t *testing.T) {
	known := map[string]bool{
		"user-management": true, "device-management": true, "event-sources": true,
		"event-management": true, "device-state": true, "dashboard-management": true,
		"command-delivery": true, "notification-management": true,
		"event-processing": true, "outbound-connectors": true, "mcp": true,
		"ai-inference": true, "sparkplug-ingest": true, "lwm2m-ingest": true,
	}
	for _, st := range streams.All {
		if len(st.Areas) == 0 {
			t.Errorf("stream %q declares no owning functional area", st.Suffix)
			continue
		}
		for _, a := range st.Areas {
			if !known[a] {
				t.Errorf("stream %q names %q as an owner, which is not a functional area",
					st.Suffix, a)
			}
		}
	}
}

// TestNilAreasRequiresEveryStream pins the strict default: a caller that cannot
// observe what is deployed gets the loudest check, not the most forgiving one.
func TestNilAreasRequiresEveryStream(t *testing.T) {
	if got := len(ReplicationExpectation("inst", 3, nil).Streams); got != len(streams.All) {
		t.Fatalf("an unknown deployment must require all %d stream(s); got %d",
			len(streams.All), got)
	}
}
