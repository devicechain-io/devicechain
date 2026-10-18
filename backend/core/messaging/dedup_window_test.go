// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// republish is how a stream's publishers can send the same Nats-Msg-Id again, which decides
// the longest time between a message's first publish and its last possible re-publish.
type republish int

const (
	// noID: no publisher stamps a Nats-Msg-Id, so nothing can be recognised as a repeat
	// and the window holds nothing.
	noID republish = iota
	// onRedelivery: the producer re-publishes under a stable id on each delivery of its own
	// input, to a consumer that is running.
	onRedelivery
	// afterRestart: a re-publish can also wait for the producing (or recording) pod to come
	// back, because no other replica takes its input over.
	afterRestart
)

// restartAllowance is the restart a restart-sized window has to outlast: a bad one, an
// image-pull backoff or a failing rollout, not a healthy few seconds. It is the floor the
// stream declarations size those windows to ("tens of minutes").
const restartAllowance = 15 * time.Minute

// redeliverySpan is the longest time from a message's first publish to its last re-publish
// when every re-publish comes from a redelivery of the producer's input, computed from the
// retry contract the durables are actually created with (AckWait, MaxDeliver).
//
// A delivery is handled within its own AckWait (the plain-reader contract on AckWait; REACT's
// one-slot capacity reader), so successive deliveries are at most two AckWaits apart and the
// last one's publish lands within one more AckWait. No durable sets a BackOff, so nothing
// lengthens a redelivery beyond AckWait. A publisher's own retry is inside the delivery that
// made it: one publish waits at most publishWait and one failure backoff at most
// orderedBackoffMax, which TestRedeliverySpanPremise holds under AckWait.
func redeliverySpan() time.Duration {
	return time.Duration(MaxDeliver-1)*2*AckWait + AckWait
}

func (r republish) worstCase() time.Duration {
	switch r {
	case onRedelivery:
		return redeliverySpan()
	case afterRestart:
		return restartAllowance + redeliverySpan()
	}
	return 0
}

// The span above assumes a delivery's publish (with its retry) finishes inside the delivery's
// AckWait, and that no durable adds a BackOff. Both are read off the live code.
func TestRedeliverySpanPremise(t *testing.T) {
	if h := publishWait + orderedBackoffMax; h >= AckWait {
		t.Errorf("a publish can take %v (publishWait + orderedBackoffMax), not inside AckWait %v: "+
			"redeliveries are no longer two AckWaits apart and every redelivery-sized window is short", h, AckWait)
	}
	r := &natsReader{nmgr: &NatsManager{}}
	if cfg := r.consumerConfig(); len(cfg.BackOff) != 0 {
		t.Errorf("durables now carry a BackOff %v: redeliveries wait longer than AckWait and "+
			"redeliverySpan understates every window", cfg.BackOff)
	}
}

// Every stream's dedup window must outlast the longest time between a message's first publish
// and its last possible re-publish: a repeat that arrives after the window is stored a second
// time, and nothing about the stored copy says so. A stream nobody stamps an id on declares the
// broker's own default, so declaring it changes nothing on an existing stream.
//
// The inputs are read from the live constants (AckWait, MaxDeliver, the declared window), never
// copied. A new stream has to be classified here before this test passes.
func TestEveryDuplicateWindowCoversItsRepublishInterval(t *testing.T) {
	// Who stamps a Nats-Msg-Id on each stream, and so how a repeat can arrive.
	streamsByRepublish := map[string]republish{
		// event-sources (capture forward, processor.DedupID; adapter ingest), sparkplug-ingest,
		// lwm2m-ingest and device-state presence demotions. A forwarded capture message is
		// re-published when it is redelivered. A redelivery that waits for a restart is
		// covered only up to about 8 minutes: that residual is the declared trade-off, and
		// the budget below is why (TestDuplicateWindowsFitTheBrokerMemoryLimit).
		streams.InboundEvents: onRedelivery,
		// device-management, once per resolved event (processor.resolvedDedupID), on each
		// redelivery of its inbound source.
		streams.ResolvedEvents: onRedelivery,
		// DETECT re-publishes every detection past its checkpoint after a restart.
		streams.DerivedEvents: afterRestart,
		// REACT, on each delivery of the derived event.
		streams.ConnectorDispatch: onRedelivery,
		streams.RaiseAlarm:        onRedelivery,
		// device-management's failure record, on each redelivery of its source.
		streams.FailedEvents: onRedelivery,
		// The in-handler arm and the max-delivery recorder write the same give-up; the
		// recorder may be down for a restart.
		streams.ConnectorDispatchDead: afterRestart,
		streams.DeadLetters:           afterRestart,

		streams.DeviceAttribute:         noID,
		streams.DeviceCommands:          noID,
		streams.DeviceDesired:           noID,
		streams.DeviceReports:           noID,
		streams.DeviceEventsCapture:     noID,
		streams.CommandResponses:        noID,
		streams.DetectionRulesPublished: noID,
		streams.DeviceRoster:            noID,
		streams.DeviceOwnership:         noID,
		streams.GeoFenceSetManifest:     noID,
		streams.EntityDeleted:           noID,
		streams.AlarmEvents:             noID,
		streams.FailedDecode:            noID,
		streams.MaxDeliveries:           noID,
	}

	for _, suffix := range streams.Suffixes() {
		r, ok := streamsByRepublish[suffix]
		if !ok {
			t.Errorf("%s is not classified here: say who stamps a Nats-Msg-Id on it and how a repeat "+
				"can arrive, so its window can be checked", suffix)
			continue
		}
		window := time.Duration(streams.DuplicateWindowSecondsFor(suffix)) * time.Second
		t.Run(suffix, func(t *testing.T) {
			if r == noID {
				if window != natsserver.StreamDefaultDuplicatesWindow {
					t.Errorf("%s declares a %v window, want the broker default %v: no publisher stamps "+
						"an id, so any other value is an update to every existing stream for nothing",
						suffix, window, natsserver.StreamDefaultDuplicatesWindow)
				}
				return
			}
			if worst := r.worstCase(); window < worst {
				t.Errorf("%s duplicate window %v is shorter than its worst-case republish interval %v: "+
					"a repeat arriving after the window is stored twice", suffix, window, worst)
			}
		})
	}
	for suffix := range streamsByRepublish {
		if !streams.IsDeclared(suffix) {
			t.Errorf("%s is classified here but not declared in core/streams", suffix)
		}
	}
}

// The declarations name the broker default by a constant of their own, because the leaf
// cannot import the server; this is what ties the two together.
func TestBrokerDefaultDuplicateWindowIsTheServers(t *testing.T) {
	got := time.Duration(streams.BrokerDefaultDuplicateWindowSeconds) * time.Second
	if got != natsserver.StreamDefaultDuplicatesWindow {
		t.Errorf("streams.BrokerDefaultDuplicateWindowSeconds is %v, the server's default is %v",
			got, natsserver.StreamDefaultDuplicatesWindow)
	}
}

// startDedupManager starts a manager against a fresh embedded broker.
func startDedupManager(t *testing.T, area string) *NatsManager {
	t.Helper()
	srv := startEmbeddedServer(t)
	ms := testMicroservice(t, srv, uniqueArea(area))
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() { _ = nmgr.Stop(ctx) })
	return nmgr
}

// The streams whose window this change moved, and the window an earlier build left on each:
// resolved-events and failed-events declared none (the broker default, written here as 0), and
// inbound-events declared 30 minutes. All three now declare the redelivery span.
var movedWindows = map[string]time.Duration{
	streams.ResolvedEvents: 0,
	streams.FailedEvents:   0,
	streams.InboundEvents:  30 * time.Minute,
}

// The mechanism the windows rely on, on the real broker, at a time scale a test can afford:
// a re-publish that arrives after the OLD window (the broker default) but inside the NEW one
// (the redelivery span) is dropped, and the same re-publish on a stream still on the old
// window is stored a second time. Every duration is the real one divided by timeScale, so the
// re-publish lands at the scaled worst case — the last redelivery — between the two windows.
// That the real streams declare the real windows is the table test's job, and that an
// existing stream takes its new window is TestAnExistingStreamGetsItsWindowInPlace's.
func TestARepublishPastTheOldWindowIsDroppedOnlyInsideTheNewOne(t *testing.T) {
	const timeScale = 60
	oldWindow := time.Duration(streams.BrokerDefaultDuplicateWindowSeconds) * time.Second / timeScale
	newWindow := time.Duration(streams.RedeliveryDuplicateWindowSeconds) * time.Second / timeScale
	republishAt := redeliverySpan() / timeScale
	require.Less(t, oldWindow, republishAt, "the re-publish must come after the old window")
	require.Less(t, republishAt, newWindow, "the re-publish must come inside the new window")

	nmgr := startDedupManager(t, "dedup-scaled")
	stored := func(name string) uint64 {
		t.Helper()
		info, err := nmgr.js.StreamInfo(name)
		require.NoError(t, err)
		return info.State.Msgs
	}
	publish := func(name, id string) {
		t.Helper()
		_, err := nmgr.js.PublishMsg(&nats.Msg{Subject: name, Data: []byte(id),
			Header: nats.Header{nats.MsgIdHdr: []string{id}}})
		require.NoError(t, err)
	}
	windows := map[string]time.Duration{"DEDUP_OLD": oldWindow, "DEDUP_NEW": newWindow}
	for name, w := range windows {
		cfg := &nats.StreamConfig{Name: name, Subjects: []string{name}, Storage: nats.FileStorage}
		require.True(t, applyStreamDuplicateWindow(cfg, w))
		_, err := nmgr.js.AddStream(cfg)
		require.NoError(t, err)
		publish(name, "src:1:0")
	}
	start := time.Now()

	time.Sleep(republishAt - time.Since(start))
	for name := range windows {
		publish(name, "src:1:0")
		publish(name, "src:2:0") // the negative control: a new id is always stored
	}
	require.Equal(t, uint64(2), stored("DEDUP_NEW"),
		"inside the new window the late repeat of src:1:0 must be dropped and src:2:0 stored")
	require.Equal(t, uint64(3), stored("DEDUP_OLD"),
		"past the old window the repeat is stored again: the duplicate the new window exists to stop")
}

// An instance upgraded from an earlier build has these streams on their old windows. The first
// start of the new build must move the window — wider or narrower — ON THE EXISTING STREAM:
// UpdateStream, not a delete and recreate, so the stream keeps its messages and the ids it
// already holds.
func TestAnExistingStreamGetsItsWindowInPlace(t *testing.T) {
	nmgr := startDedupManager(t, "dedup-upgrade")
	tctx := core.WithTenant(context.Background(), "acme")
	want := time.Duration(streams.RedeliveryDuplicateWindowSeconds) * time.Second

	for suffix, old := range movedWindows {
		t.Run(suffix, func(t *testing.T) {
			name := StreamName(nmgr.Microservice.InstanceId, suffix)
			// The stream as the earlier build created and reconciled it: ensureStream's config,
			// ceilings included, and its old window (none: the broker fills in its default).
			// The window is then the ONLY drift, so nothing else can carry it to the broker.
			legacy := &nats.StreamConfig{
				Name:       name,
				Subjects:   StreamSubjects(nmgr.Microservice.InstanceId, suffix),
				Storage:    nats.FileStorage,
				Retention:  nats.LimitsPolicy,
				Discard:    nats.DiscardOld,
				MaxAge:     streamMaxAge,
				Replicas:   nmgr.effectiveStreamReplicas(),
				Duplicates: old,
			}
			applyStreamBounds(legacy, nmgr.streamBounds(suffix))
			_, err := nmgr.js.AddStream(legacy)
			require.NoError(t, err)
			pub, err := nmgr.js.PublishMsg(&nats.Msg{
				Subject: tenantSubjectFor(t, nmgr, suffix),
				Data:    []byte("a"),
				Header:  nats.Header{nats.MsgIdHdr: []string{"src:1:0"}},
			})
			require.NoError(t, err)
			require.False(t, pub.Duplicate)
			before, err := nmgr.js.StreamInfo(name)
			require.NoError(t, err)
			if old == 0 {
				old = natsserver.StreamDefaultDuplicatesWindow
			}
			require.Equal(t, old, before.Config.Duplicates)

			w, err := nmgr.NewWriter(suffix) // ensureStream, as the upgraded pod runs it
			require.NoError(t, err)

			after, err := nmgr.js.StreamInfo(name)
			require.NoError(t, err)
			require.Equal(t, want, after.Config.Duplicates, "the window must be reconciled on the existing stream")
			require.Equal(t, before.Created, after.Created, "the stream must be updated in place, not recreated")
			require.Equal(t, uint64(1), after.State.Msgs, "the stream must keep what it held")

			// The ids held before the update are still held after it.
			require.NoError(t, w.WriteMessages(tctx, Message{Value: []byte("a"), DedupID: "src:1:0"}))
			again, err := nmgr.js.StreamInfo(name)
			require.NoError(t, err)
			require.Equal(t, uint64(1), again.State.Msgs, "an id stored before the update must still be recognised")
		})
	}
}

// tenantSubjectFor is the subject a tenant-scoped writer for suffix publishes acme's messages to.
func tenantSubjectFor(t *testing.T, nmgr *NatsManager, suffix string) string {
	t.Helper()
	subject, err := nmgr.tenantSubject(core.WithTenant(context.Background(), "acme"), suffix, "")
	require.NoError(t, err)
	return subject
}

// The broker memory budget for duplicate detection, at the 10,000 events/s reference load.
//
// Every id a stream holds costs heap on EVERY broker server (each replica keeps its own table),
// so a server holds the sum over every stream of rate x window x bytesPerHeldID, on top of what
// the broker already uses. That sum has to fit the memory limit the instance gives each NATS
// server, with headroom, or a sustained load OOM-kills the broker the platform depends on. A
// window change that breaks the budget fails here.
//
// The windows and the limit are read from the live declarations (core/streams, the OpenTofu
// default). The rates are the reference load's, stated per stream below: they are the
// assumptions of the budget, not measurements, and a reason to change one belongs next to it.
//
// The budget is the one with infrastructure.nats.mqttFailClosed ON, the larger of the two:
// the fail-closed message store adds what it costs each server to hold a full store's backlog
// (failClosedStoreMiB). It is small, and that is a measurement worth keeping next to the
// budget: a backlog in the store is not what fills a broker's memory, the duplicate tables are.
func TestDuplicateWindowsFitTheBrokerMemoryLimit(t *testing.T) {
	const (
		referenceEvents = 10_000.0
		// bytesPerHeldID is the live heap one held id costs, measured against nats-server
		// v2.15.0 with 35-byte ids (a ddentry, the id's own bytes, a map slot and the ordered
		// array's pointer). The deployed broker (nats chart 2.15.1) has the same structures.
		// See streams.BrokerDefaultDuplicateWindowSeconds.
		bytesPerHeldID = 120.0
		// derivedIDExtraBytes: a derived-events id is "de." plus a hex sha256, 67 bytes, which
		// the allocator rounds to 80 rather than the 48 a 35-byte id takes.
		derivedIDExtraBytes = 32.0
		// existingBrokerMiB is the highest resident memory any server reached in the release
		// benchmark (up to 9,200 events/s). It already includes the dedup tables those runs
		// had filled, so adding the full tables to it counts some of them twice: it errs high.
		// Cross-check against GKE round 10's 10,000 events/s x 600 s run: the peak was
		// 1,124 MiB RSS, holding about 7.2M ids under the old windows (inbound 6M, resolved
		// 1.2M; ~824 MiB at bytesPerHeldID), so what is not a dedup table is about 300 MiB.
		existingBrokerMiB = 830.0
		// The Go runtime's soft limit is 80% of the container limit (modules/nats); the
		// budget must stay this far under the SOFT limit, so the collector is not running
		// continuously against a live set it cannot shrink.
		goMemLimitFraction = 0.80
		headroom           = 0.15
		// storeHeldMsgBytes is the live heap each server spends per message the fail-closed
		// message store holds unread, measured against nats-server v2.15.1 on a three-node
		// cluster (TestPerfStoreMemory: 1.5M messages, 570 MiB on disk, ~25-35 MiB of heap per
		// server at rest; the block caches a reader loads while it drains are transient and are
		// the same for the capture stream a reader drains with the setting off).
		storeHeldMsgBytes = 25.0
		// storedEventBytes is what one device event takes in the store, measured on GKE round 15:
		// 1,557,739 messages held 57% of a 1 GiB store, about 412 bytes each. A smaller event
		// packs more messages into the bound, so this is the figure to lower if events shrink.
		storedEventBytes = 412.0
	)
	// ids/s each stream receives at the reference load.
	rates := map[string]float64{
		streams.InboundEvents: referenceEvents,
		// An inbound event either resolves or fails, so the two share the event rate; they
		// declare the same window, so the split does not move the budget.
		streams.ResolvedEvents: referenceEvents,
		streams.FailedEvents:   0,
		// Detections are assumed to be a tenth of the events, and each REACT action stream
		// to carry at most one action per detection.
		streams.DerivedEvents:     referenceEvents / 10,
		streams.RaiseAlarm:        referenceEvents / 10,
		streams.ConnectorDispatch: referenceEvents / 10,
		// Give-ups: zero in steady state; budgeted at a burst of 100/s.
		streams.DeadLetters:           100,
		streams.ConnectorDispatchDead: 100,
	}

	limitMiB := natsMemoryLimitMiB(t)
	budgetMiB := limitMiB * goMemLimitFraction * (1 - headroom)
	totalMiB := existingBrokerMiB
	// The fail-closed store, full: its bound as the setting on applies it, in messages.
	on := config.NewDefaultInstanceConfiguration()
	on.Infrastructure.Nats.MqttFailClosed = true
	on.ApplyDefaults()
	storeMsgs := float64(on.Infrastructure.Nats.MqttMessageStoreMaxBytes()) / storedEventBytes
	failClosedStoreMiB := storeMsgs * storeHeldMsgBytes / (1 << 20)
	totalMiB += failClosedStoreMiB
	t.Logf("%-24s %8s %8s %10s %10s", "stream", "ids/s", "window", "ids held", "MiB")
	for _, suffix := range streams.Suffixes() {
		rate, ok := rates[suffix]
		if !ok {
			continue // a stream nobody stamps an id on holds nothing; the table test pins which
		}
		window := float64(streams.DuplicateWindowSecondsFor(suffix))
		ids := rate * window
		perID := bytesPerHeldID
		if suffix == streams.DerivedEvents {
			perID += derivedIDExtraBytes
		}
		mib := ids * perID / (1 << 20)
		totalMiB += mib
		t.Logf("%-24s %8.0f %7.0fs %10.0f %10.0f", suffix, rate, window, ids, mib)
	}
	t.Logf("%-24s %40.0f (%.0f messages held)", "fail-closed store, full", failClosedStoreMiB, storeMsgs)
	t.Logf("%-24s %40.0f", "existing broker memory", existingBrokerMiB)
	t.Logf("%-24s %40.0f (limit %.0f MiB, GOMEMLIMIT %.0f MiB, budget %.0f MiB)", "total",
		totalMiB, limitMiB, limitMiB*goMemLimitFraction, budgetMiB)
	if totalMiB > budgetMiB {
		t.Errorf("at %.0f events/s each NATS server needs about %.0f MiB, over the budget of %.0f MiB "+
			"(%.0f%% under the %.0f MiB soft limit of the %.0f MiB container limit): shorten a window, "+
			"or raise nats_memory_limit", referenceEvents, totalMiB, budgetMiB, headroom*100,
			limitMiB*goMemLimitFraction, limitMiB)
	}
	for suffix := range rates {
		if !streams.IsDeclared(suffix) {
			t.Errorf("%s has a budgeted rate but is not declared in core/streams", suffix)
		}
	}
}

// natsMemoryLimitMiB reads the default memory limit each NATS server gets, so the budget is held
// to the limit that actually ships. Two defaults declare it: the instance root's, which every
// install passes to the module, and the nats module's own, which a root that passes nothing
// gets. The budget is held to the LOWER of the two, so neither can drift below it unnoticed.
func natsMemoryLimitMiB(t *testing.T) float64 {
	t.Helper()
	opentofu := filepath.Join("..", "..", "..", "deploy", "opentofu")
	root := tofuDefaultMiB(t, filepath.Join(opentofu, "instance", "variables.tf"), "nats_memory_limit")
	module := tofuDefaultMiB(t, filepath.Join(opentofu, "modules", "nats", "main.tf"), "memory_limit")
	return min(root, module)
}

// tofuDefaultMiB reads the Mi or Gi default of an OpenTofu variable, in MiB.
func tofuDefaultMiB(t *testing.T, path, variable string) float64 {
	t.Helper()
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	m := regexp.MustCompile(`(?s)variable "` + variable + `" \{.*?\n\s*default\s*=\s*"(\d+)(Mi|Gi)"`).FindSubmatch(src)
	require.NotNil(t, m, "no Mi or Gi default for %s in %s", variable, path)
	n, err := strconv.Atoi(string(m[1]))
	require.NoError(t, err)
	if string(m[2]) == "Gi" {
		n *= 1024
	}
	return float64(n)
}
