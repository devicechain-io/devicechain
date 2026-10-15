// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

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
		// re-published when it is redelivered, which with one event-sources replica waits for
		// the restart.
		streams.InboundEvents: afterRestart,
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

// The streams device-management re-publishes to on a redelivery of its source.
var redeliveryRepublished = []string{streams.ResolvedEvents, streams.FailedEvents}

// A re-publish that arrives after the broker's two-minute default but inside the redelivery
// span — a source whose next two deliveries both failed to publish — is stored ONCE, and a
// different id is still stored (the collapse is by id, not blanket).
//
// It waits out the broker default in real time, because that is the only clock the broker's
// dedup table reads: past it, a stream still on the default has already forgotten the id.
func TestARepublishPastTheBrokerDefaultIsStoredOnce(t *testing.T) {
	t.Parallel()
	nmgr := startDedupManager(t, "dedup-late")
	tctx := core.WithTenant(context.Background(), "acme")

	writers := map[string]MessageWriter{}
	for _, suffix := range redeliveryRepublished {
		w, err := nmgr.NewWriter(suffix)
		require.NoError(t, err)
		writers[suffix] = w
		require.NoError(t, w.WriteMessages(tctx, Message{Value: []byte("a"), DedupID: "src:1:0"}))
	}

	time.Sleep(natsserver.StreamDefaultDuplicatesWindow + 10*time.Second)

	for _, suffix := range redeliveryRepublished {
		w := writers[suffix]
		require.NoError(t, w.WriteMessages(tctx, Message{Value: []byte("a"), DedupID: "src:1:0"}))
		require.NoError(t, w.WriteMessages(tctx, Message{Value: []byte("b"), DedupID: "src:2:0"}))
		info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, suffix))
		require.NoError(t, err)
		require.Equalf(t, uint64(2), info.State.Msgs,
			"%s: the late repeat of src:1:0 must be dropped and src:2:0 stored", suffix)
	}
}

// An instance upgraded from a build that declared no window has these streams on the broker
// default. The first start of the new build must widen the window ON THE EXISTING STREAM —
// UpdateStream, not a delete and recreate — so the stream keeps its messages and the ids it
// already holds.
func TestAnExistingStreamGetsItsWindowInPlace(t *testing.T) {
	nmgr := startDedupManager(t, "dedup-upgrade")
	tctx := core.WithTenant(context.Background(), "acme")
	want := time.Duration(streams.RedeliveryDuplicateWindowSeconds) * time.Second

	for _, suffix := range redeliveryRepublished {
		t.Run(suffix, func(t *testing.T) {
			name := StreamName(nmgr.Microservice.InstanceId, suffix)
			// The stream as the earlier build created and reconciled it: ensureStream's config,
			// ceilings included, with no Duplicates, which the broker fills with its default.
			// The window is then the ONLY drift, so nothing else can carry it to the broker.
			legacy := &nats.StreamConfig{
				Name:      name,
				Subjects:  StreamSubjects(nmgr.Microservice.InstanceId, suffix),
				Storage:   nats.FileStorage,
				Retention: nats.LimitsPolicy,
				Discard:   nats.DiscardOld,
				MaxAge:    streamMaxAge,
				Replicas:  nmgr.effectiveStreamReplicas(),
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
			require.Equal(t, natsserver.StreamDefaultDuplicatesWindow, before.Config.Duplicates)

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
