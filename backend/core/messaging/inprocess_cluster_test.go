// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// The writers take a duplicate PubAck, after re-sending through "in process", as proof the
// message is stored. That is the broker's contract, and this test holds a real replicated
// stream to it rather than trusting a stub's reply.
//
// What the contract rests on, in nats-server (2.14.4, which the chart runs, and 2.15.x, which
// these tests embed): a clustered publish with an id is staged in the dedup map with sequence 0
// before its proposal, and a second publish meeting a sequence-0 entry is answered 10158
// (checkMsgHeadersPreClusteredProposal). The entry gets its real sequence only in
// processJetStreamMsg, after the committed entry has been stored, and only an entry with a
// sequence answers "duplicate". A leader change drops the sequence-0 entries
// (processStreamLeaderChange), and a new leader takes writes only once it has processed every
// entry committed before its election (raft Processed / aflr), so a re-send after a dropped
// proposal is stored, never answered as a duplicate of nothing.
//
// The stream leader is cut off from both followers, so a raw publish's proposal cannot commit
// and its id stays in process — which the broker is shown to say, by its code. Each writer then
// publishes the same id and re-sends through the conflict. When the routes come back the
// original commits, both writers report success, and the stream holds each id exactly once.
//
// GUARD: kills a writer that reports the conflict (an outcome fails), a classifier whose code
// is not the broker's (the precondition, and then the writers, fail), and a writer that takes a
// duplicate PubAck for something it is not (a stored copy short, or a second one).
func TestAnInProcessIdResolvesToItsStoredOriginalOnAReplicatedStream(t *testing.T) {
	servers, faults := dctest.StartJetStreamClusterWithRouteFaults(t, 3)
	area := uniqueArea("inproc-r3")
	setup := clusterManagerOn(t, servers, area)
	waitUntilPublishable(t, setup)
	stream := StreamName(setup.Microservice.InstanceId, streams.ResolvedEvents)
	info := waitForReplicated(t, setup.js, stream, 3)
	leader := -1
	for i, s := range servers {
		if s.Name() == info.Cluster.Leader {
			leader = i
		}
	}
	if leader < 0 {
		t.Fatalf("stream leader %q is none of the cluster's servers", info.Cluster.Leader)
	}

	// The writers' connection is to the leader, so it reaches the stream while the leader's
	// routes are silent.
	nmgr := clusterManagerOn(t, []*natsserver.Server{servers[leader]}, area)
	ow := newOrdered(t, nmgr, 8)
	sw, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := storedDedupIDs(t, nmgr)
	subject := ScopedSubject(nmgr.Microservice.InstanceId, orderedTenant, streams.ResolvedEvents)
	ids := []string{dedupID(0), dedupID(1)}

	silenced := true
	restore := func() {
		if silenced {
			for i := range servers {
				if i != leader {
					faults.Restore(i)
				}
			}
			silenced = false
		}
	}
	t.Cleanup(restore)
	for i := range servers {
		if i != leader {
			faults.Silence(i)
		}
	}

	// The originals: proposed, and unable to commit while the leader is alone.
	for _, id := range ids {
		if _, err := nmgr.js.PublishMsgAsync(natsMsg(subject, Message{Value: []byte("e"), DedupID: id})); err != nil {
			t.Fatalf("original %s: %v", id, err)
		}
	}
	// Precondition, from the broker itself: a publish of an id whose original is uncommitted
	// is answered with the in-process conflict, by the code the writers match.
	var apiErr *nats.APIError
	_, err = nmgr.js.PublishMsg(natsMsg(subject, Message{Value: []byte("e"), DedupID: ids[0]}), nats.AckWait(2*time.Second))
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != jsErrCodeDuplicateMsgIdInProcess {
		t.Fatalf("a publish racing its uncommitted original got %v, want the broker's in-process conflict (%d)",
			err, jsErrCodeDuplicateMsgIdInProcess)
	}

	ordered := make(chan error, 1)
	ow.Publish(orderedCtx(), Message{Value: []byte("e"), DedupID: ids[0]}, func(err error) { ordered <- err })
	synced := make(chan error, 1)
	go func() {
		synced <- sw.WriteMessages(orderedCtx(), Message{Value: []byte("e"), DedupID: ids[1], BypassBackpressure: true})
	}()
	// Both are re-sending against an id in process; well inside the publish ceiling, and the
	// leader's ten-second quorum loss, the original is let through.
	time.Sleep(500 * time.Millisecond)
	restore()

	for name, ch := range map[string]chan error{"ordered": ordered, "sync": synced} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("the %s writer's publish of an id that resolved to its original failed: %v", name, err)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("the %s writer's publish was never reported", name)
		}
	}
	ow.Close()

	stored, distinct := storedDedupIDs(t, nmgr)
	if stored != base+len(ids) || distinct != base+len(ids) {
		t.Fatalf("stream holds %d messages with %d distinct ids, want exactly %d of each (each id once)",
			stored, distinct, base+len(ids))
	}
	if got := duplicateCount(nmgr); got != 1 {
		t.Fatalf("duplicates counted by the ordered writer: %v, want 1 (its re-send resolved to the original)", got)
	}
	// And the id the duplicate stood for is the one stored: what a later re-send is told is
	// the original's sequence, and the message there carries the id.
	for _, id := range ids {
		ack, err := nmgr.js.PublishMsg(natsMsg(subject, Message{Value: []byte("e"), DedupID: id}))
		if err != nil || !ack.Duplicate {
			t.Fatalf("a re-send of %s after it resolved got %+v, %v, want a duplicate PubAck", id, ack, err)
		}
		raw, err := nmgr.js.GetMsg(stream, ack.Sequence)
		if err != nil {
			t.Fatalf("the duplicate's sequence %d: %v", ack.Sequence, err)
		}
		if got := raw.Header.Get(nats.MsgIdHdr); got != id {
			t.Fatalf("the duplicate of %s points at sequence %d, which holds %q", id, ack.Sequence, got)
		}
	}
}
