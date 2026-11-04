// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fanApi resolves like instantApi but gives every device TWO tracked relationships: one resolved
// event per inbound event, carrying both anchors (the larger event the cluster tests publish).
type fanApi struct{ instantApi }

func (fanApi) TrackedRelationshipsForDevice(context.Context, uint) (*dmodel.EntityRelationshipSearchResults, error) {
	return &dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{
		{Model: gorm.Model{ID: 1}, SourceType: "device", SourceId: 1, TargetType: "asset", TargetId: 2, TargetToken: "asset-2"},
		{Model: gorm.Model{ID: 2}, SourceType: "device", SourceId: 1, TargetType: "asset", TargetId: 3, TargetToken: "asset-3"},
	}}, nil
}

// dyingWriter is the resolved writer of a pod that is killed between a PubAck and the ack it
// would trigger: the first `allow` publishes settle normally, and from then on every publish is
// still sent and stored by the broker but its outcome is NEVER reported - no source is acked, no
// failure is latched - which is exactly what the pod dying right after the PubAck looks like.
type dyingWriter struct {
	messaging.AckedWriter
	allow   int32
	settled atomic.Int32
	dropped atomic.Int32
}

func (d *dyingWriter) swallow() bool {
	if d.settled.Add(1) <= d.allow {
		return false
	}
	d.dropped.Add(1)
	return true
}

func (d *dyingWriter) PublishAcked(ctx context.Context, msg messaging.Message, done func(messaging.PubAckInfo, error)) {
	d.AckedWriter.PublishAcked(ctx, msg, func(a messaging.PubAckInfo, err error) {
		if err == nil && d.swallow() {
			return
		}
		done(a, err)
	})
}

func (d *dyingWriter) Publish(ctx context.Context, msg messaging.Message, done func(error)) {
	d.AckedWriter.Publish(ctx, msg, func(err error) {
		if err == nil && d.swallow() {
			return
		}
		done(err)
	})
}

func trialEvent(trial, i int, base time.Time) *esmodel.UnresolvedEvent {
	return &esmodel.UnresolvedEvent{
		Source:       "kill",
		Device:       fmt.Sprintf("t%d-d%d", trial, i),
		EventType:    eventtype.Measurement,
		OccurredTime: base.Add(time.Duration(trial*1000+i) * 100 * time.Microsecond),
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{
			{Measurements: map[string]string{"temp": "21.5"}},
		}},
	}
}

// identity is the loadtest identity oracle's key: (device, anchor, occurred microsecond).
type identity struct {
	device, anchor string
	occurredMicro  int64
}

// storedIdentities reads every message of the resolved stream the way a reader does - an
// envelope member by member, a legacy message whole - and counts each identity's multiplicity.
// envelopes and legacy are how many of each shape it saw.
func storedIdentities(t testing.TB, js nats.JetStreamContext, stream string) (seen map[identity]int, envelopes, legacy, members int) {
	t.Helper()
	info, err := js.StreamInfo(stream)
	require.NoError(t, err)
	sub, err := js.PullSubscribe("", "", nats.BindStream(stream), nats.DeliverAll(), nats.AckNone())
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()
	seen = map[identity]int{}
	count := func(b []byte) {
		ev, err := resolvedevent.UnmarshalResolvedEvent(b)
		require.NoError(t, err)
		anchor := ""
		if len(ev.Anchors) > 0 {
			anchor = ev.Anchors[0].AnchorToken
		}
		seen[identity{ev.SourceDeviceToken, anchor, ev.OccurredTime.UnixMicro()}]++
		members++
	}
	read := uint64(0)
	for read < info.State.Msgs {
		msgs, err := sub.Fetch(256, nats.MaxWait(5*time.Second))
		require.NoError(t, err, "reading the resolved stream back")
		for _, m := range msgs {
			read++
			if eventenvelope.Marked(m.Header) {
				envelopes++
				r, err := eventenvelope.Parse(m.Data)
				require.NoError(t, err)
				require.NoError(t, r.VerifyTenant("acme"))
				for i := 0; i < r.Len(); i++ {
					b, err := r.Member(i)
					require.NoError(t, err)
					count(b)
				}
				continue
			}
			legacy++
			count(m.Data)
		}
	}
	return seen, envelopes, legacy, members
}

func publishInbound(t testing.TB, js nats.JetStreamContext, instance string, events []*esmodel.UnresolvedEvent) {
	t.Helper()
	subject := messaging.ScopedSubject(instance, "acme", streams.InboundEvents)
	for _, ev := range events {
		body, err := esproto.MarshalUnresolvedEvent(ev)
		require.NoError(t, err)
		_, err = js.PublishAsync(subject, body)
		require.NoError(t, err)
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-time.After(60 * time.Second):
		t.Fatal("publishing the inbound events did not complete")
	}
}

func envTrials(def int) int {
	if v := os.Getenv("DM_ENVELOPE_KILL_TRIALS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// A pod killed between a round's PubAck and the ack of its sources loses nothing and duplicates
// nothing, whatever the replacement groups differently.
//
// The pipeline is real: a three-node R3 cluster, the durable reader, the resolver pool and the
// real ordered writer with envelopes on. Every trial publishes a batch of inbound events (each
// resolving to one resolved event), starts a pod whose writer swallows every outcome after a
// random number of settled publishes (the broker stored them; no source was acked), kills it
// with fetched-but-unresolved events in flight, and starts a replacement on the same durable,
// which is redelivered whatever was not acked and resolves it again, grouped as the pool happens
// to drain it.
//
// The oracle is the loadtest's identity check - (device, anchor, occurred microsecond), with
// multiplicity - over EVERYTHING in resolved-events after all trials, read the way a reader
// reads it. DETECT applies every member of every stored message, so a multiplicity of 2 here is
// an event DETECT would apply twice: the gate is that there is none, and none missing.
//
// GUARD (see the mutation record in the PR): a recovery that publishes the whole chunk again, one
// that treats an unreadable original as empty, an id keyed on the first source instead of the
// bucket, and a missing probe each leave duplicates or losses here.
func TestKillBetweenRoundZeroPubAckAndAckLosesAndDuplicatesNothing(t *testing.T) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	const perTrial = 300
	trials := envTrials(100)
	servers := dctest.StartJetStreamCluster(t, 3)
	instance := fmt.Sprintf("envkill%d", time.Now().UnixNano())
	rng := rand.New(rand.NewSource(20261103))
	base := time.Now().Add(-time.Minute)

	spec := pipelineSpec{
		srv: servers[0], instance: instance, replicas: 3, resolvers: 4, window: 64, readers: 1,
		events: 0, api: fanApi{}, authMode: config.AuthModeOptional, ackWait: 1500 * time.Millisecond,
	}
	durable := messaging.DurableName(instance, "device-management", streams.InboundEvents)

	var killed, partiallyAcked, recoveredMembers, duplicateAcks int
	nc, err := nats.Connect(servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	resolvedStream := messaging.StreamName(instance, streams.ResolvedEvents)
	for trial := 0; trial < trials; trial++ {
		trial := trial
		t.Run(fmt.Sprintf("trial-%03d", trial), func(t *testing.T) {
			allow := int32(rng.Intn(5)) // settled publishes before the pod dies; 0 = nothing acked
			var dying *dyingWriter
			specA := spec
			specA.wrapResolved = func(w messaging.OrderedWriter) messaging.OrderedWriter {
				dying = &dyingWriter{AckedWriter: w.(messaging.AckedWriter), allow: allow}
				return dying
			}
			a := startResolvePipeline(t, specA)
			rounds, err := a.nmgr.NewRoundPublisher(streams.ResolvedEvents)
			require.NoError(t, err)
			WithResolvedEnvelopes(true, rounds)(a.iproc)

			batch := make([]*esmodel.UnresolvedEvent, perTrial)
			for i := range batch {
				batch[i] = trialEvent(trial, i, base)
			}
			publishInbound(t, a.js, instance, batch)
			require.NoError(t, a.iproc.Start(context.Background()))

			// Run until the pod has had outcomes swallowed (or finished first), then a random
			// moment more, then kill it: connection cut, whatever it holds left unacked.
			deadline := time.Now().Add(30 * time.Second)
			for dying.dropped.Load() == 0 && time.Now().Before(deadline) {
				ci, err := a.js.ConsumerInfo(a.inboundStream, durable)
				require.NoError(t, err)
				if ci.NumPending == 0 && ci.NumAckPending == 0 {
					break // finished before the kill point: an unkilled trial
				}
				time.Sleep(time.Millisecond)
			}
			time.Sleep(time.Duration(rng.Intn(15)) * time.Millisecond)
			wasKilled := dying.dropped.Load() > 0
			a.nmgr.Conn().Close()
			stopped := make(chan struct{})
			go func() { _ = a.iproc.Stop(context.Background()); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(60 * time.Second):
				t.Fatal("the killed pod's processor did not stop")
			}
			if wasKilled {
				killed++
				if allow > 0 {
					partiallyAcked++
				}
			}

			// The replacement pod: same durable, same instance, nothing new published.
			specB := spec
			b := startResolvePipeline(t, specB)
			rounds, err = b.nmgr.NewRoundPublisher(streams.ResolvedEvents)
			require.NoError(t, err)
			WithResolvedEnvelopes(true, rounds)(b.iproc)
			require.NoError(t, b.iproc.Start(context.Background()))
			last := uint64((trial + 1) * perTrial)
			deadline = time.Now().Add(90 * time.Second)
			for {
				ci, err := b.js.ConsumerInfo(b.inboundStream, durable)
				require.NoError(t, err)
				if ci.NumPending == 0 && ci.NumAckPending == 0 && ci.AckFloor.Stream == last {
					break
				}
				require.True(t, time.Now().Before(deadline),
					"the replacement did not settle trial %d: pending %d, ack pending %d, ack floor %d of %d",
					trial, ci.NumPending, ci.NumAckPending, ci.AckFloor.Stream, last)
				time.Sleep(10 * time.Millisecond)
			}
			require.NoError(t, b.iproc.Stop(context.Background()))
			recoveredMembers += int(b.iproc.env.Recovered.Load())
			duplicateAcks += int(b.iproc.env.Duplicates.Load())
		})
		if t.Failed() {
			break
		}
	}

	seen, envelopes, legacy, members := storedIdentities(t, js, resolvedStream)
	var missing, twice []identity
	for trial := 0; trial < trials; trial++ {
		for i := 0; i < perTrial; i++ {
			ev := trialEvent(trial, i, base)
			for _, anchor := range []string{"asset-2"} {
				id := identity{ev.Device, anchor, ev.OccurredTime.UnixMicro()}
				switch seen[id] {
				case 1:
				case 0:
					missing = append(missing, id)
				default:
					twice = append(twice, id)
				}
				delete(seen, id)
			}
		}
	}
	require.Empty(t, missing, "resolved events that were lost")
	if len(twice) > 0 {
		dumpDuplicates(t, js, resolvedStream, twice)
	}
	require.Empty(t, twice, "resolved events stored more than once (each would be applied twice by DETECT)")
	require.Empty(t, seen, "stored identities that no inbound event accounts for")
	require.Equal(t, trials*perTrial, members)

	t.Logf("kill trials: %d run, %d killed with outcomes swallowed (%d with some sources acked before the kill); "+
		"stream holds %d events in %d envelopes + %d legacy messages; the replacements saw %d duplicate PubAcks "+
		"and published %d members as later rounds; 0 missing, 0 duplicated",
		trials, killed, partiallyAcked, members, envelopes, legacy, duplicateAcks, recoveredMembers)
	require.GreaterOrEqual(t, killed, trials*8/10, "too few trials reached the kill point to mean anything")
	require.Greater(t, duplicateAcks, 0, "no replacement was answered a duplicate: the probe path was never exercised")
	require.Greater(t, envelopes, 0, "no envelope was published: the flag-on path was never exercised")
}

// Flag off, the wire is today's: on a real broker, every resolved event is its own message,
// under its legacy id, with no envelope marker and no member-index header. (The unit test pins
// the message the processor hands the writer; this one pins what the broker stores.)
func TestFlagOffStoresTodaysMessagesOnTheBroker(t *testing.T) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	const events = 200
	srv := startReadersServer(t)
	instance := fmt.Sprintf("envoff%d", time.Now().UnixNano())
	base := time.Now().Add(-time.Minute)
	p := startResolvePipeline(t, pipelineSpec{
		srv: srv, instance: instance, replicas: 1, resolvers: 4, window: 64, readers: 1, events: 0,
		api: fanApi{}, authMode: config.AuthModeOptional,
	})
	batch := make([]*esmodel.UnresolvedEvent, events)
	for i := range batch {
		batch[i] = trialEvent(0, i, base)
	}
	publishInbound(t, p.js, instance, batch)
	require.NoError(t, p.iproc.Start(context.Background()))
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := p.js.StreamInfo(p.resolvedStream)
		require.NoError(t, err)
		if info.State.Msgs >= events {
			break
		}
		if !time.Now().Before(deadline) {
			ci, _ := p.js.ConsumerInfo(p.inboundStream, messaging.DurableName(instance, "device-management", streams.InboundEvents))
			fi, _ := p.js.StreamInfo(messaging.StreamName(instance, streams.FailedEvents))
			t.Fatalf("the pipeline did not resolve %d events: resolved %d, consumer %+v, failed %+v", events, info.State.Msgs, ci, fi)
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.NoError(t, p.iproc.Stop(context.Background()))

	sub, err := p.js.PullSubscribe("", "", nats.BindStream(p.resolvedStream), nats.DeliverAll(), nats.AckNone())
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()
	idRe := regexp.MustCompile(`^r:acme:[0-9a-z]+:[01]$`)
	stored := 0
	for stored < events {
		msgs, err := sub.Fetch(256, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		for _, m := range msgs {
			stored++
			for k := range m.Header {
				require.Contains(t, []string{nats.MsgIdHdr, messaging.HeaderCorrelationID}, k,
					"flag off, a resolved message carries a header today's does not: %s", k)
			}
			require.Regexp(t, idRe, m.Header.Get(nats.MsgIdHdr))
			ev, err := resolvedevent.UnmarshalResolvedEvent(m.Data)
			require.NoError(t, err, "a flag-off message is one legacy event")
			require.NotEmpty(t, ev.SourceDeviceToken)
		}
	}
	require.Equal(t, events, stored)
}

// dumpDuplicates logs, for the duplicated identities, every stored message that holds one:
// stream sequence, dedup id, shape and member indices. A failure's evidence.
func dumpDuplicates(t testing.TB, js nats.JetStreamContext, stream string, twice []identity) {
	t.Helper()
	want := map[identity]bool{}
	for _, id := range twice {
		want[id] = true
	}
	info, err := js.StreamInfo(stream)
	require.NoError(t, err)
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq; seq++ {
		raw, err := js.GetMsg(stream, seq)
		if err != nil {
			continue
		}
		var evs [][]byte
		var idx []uint32
		if raw.Header.Get(eventenvelope.HeaderName) != "" {
			r, err := eventenvelope.Parse(raw.Data)
			if err != nil {
				continue
			}
			for i := 0; i < r.Len(); i++ {
				b, _ := r.Member(i)
				evs = append(evs, b)
				idx = append(idx, r.Index(i))
			}
		} else {
			evs = [][]byte{raw.Data}
			idx = []uint32{0}
		}
		hit := 0
		for _, b := range evs {
			ev, err := resolvedevent.UnmarshalResolvedEvent(b)
			if err == nil && len(ev.Anchors) > 0 && want[identity{ev.SourceDeviceToken, ev.Anchors[0].AnchorToken, ev.OccurredTime.UnixMicro()}] {
				hit++
			}
		}
		if hit > 0 {
			t.Logf("DUP seq=%d id=%s mi=%q members=%d holdingADuplicate=%d firstIdx=%v lastIdx=%v", seq,
				raw.Header.Get(nats.MsgIdHdr), raw.Header.Get(messaging.HeaderMemberIndex), len(evs), hit, idx[0], idx[len(idx)-1])
		}
	}
}
