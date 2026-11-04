// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// An envelope's idempotency rests on one broker behaviour: a publish whose Nats-Msg-Id the stream
// holds is answered "duplicate: true" WITH THE SEQUENCE OF THE ORIGINAL. That reply is made on
// the stream leader's side of a replicated stream (the leader's dedup table, not the client's
// view), and a client may be connected to any server, so this holds the contract on a real
// three-node R3 cluster with a publisher on each server: every one of them must be told the
// original's sequence, and reading that sequence back must give the original's bytes, headers
// and tenant.
//
// GUARD: kills a PublishAcked that drops the PubAck (the second publish reads as new), one that
// reports the second publish's own sequence, a Get that is not answered by the leader, and a Get
// that serves another tenant's message.
func TestPubAckDuplicateNamesTheOriginalOnAClusteredStream(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	area := uniqueArea("pubackdup")
	setup := clusterManagerOn(t, servers, area)
	waitUntilPublishable(t, setup)
	stream := StreamName(setup.Microservice.InstanceId, streams.ResolvedEvents)
	waitForReplicated(t, setup.js, stream, 3)

	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: orderedTenant},
		[]eventenvelope.Event{{Index: 0, Bytes: []byte{0x0a, 0x01, 's'}}, {Index: 3, Bytes: []byte{0x0a, 0x01, 't'}}})
	if err != nil {
		t.Fatal(err)
	}
	const id = "G:acme:7:0"
	msg := Message{Value: body, DedupID: id, Headers: map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}}

	type acked struct {
		ack PubAckInfo
		err error
	}
	publish := func(w OrderedWriter) acked {
		ch := make(chan acked, 1)
		w.(AckedWriter).PublishAcked(orderedCtx(), msg, func(a PubAckInfo, err error) { ch <- acked{a, err} })
		select {
		case r := <-ch:
			return r
		case <-time.After(30 * time.Second):
			t.Fatal("no PubAck")
			return acked{}
		}
	}

	first := publish(newOrdered(t, setup, 8))
	if first.err != nil || first.ack.Duplicate || first.ack.Sequence == 0 {
		t.Fatalf("the first publish: %+v, want stored as new with a sequence", first)
	}
	original := first.ack.Sequence

	for i := range servers {
		nmgr := clusterManagerOn(t, servers[i:i+1], area)
		w := newOrdered(t, nmgr, 8)
		got := publish(w)
		if got.err != nil || !got.ack.Duplicate || got.ack.Sequence != original {
			t.Fatalf("a publisher on server %d: %+v, want a duplicate naming sequence %d", i, got, original)
		}
		rp, err := nmgr.NewRoundPublisher(streams.ResolvedEvents)
		if err != nil {
			t.Fatal(err)
		}
		again, err := rp.Publish(orderedCtx(), msg)
		if err != nil || !again.Duplicate || again.Sequence != original {
			t.Fatalf("the round publisher on server %d: %+v, %v, want a duplicate naming sequence %d", i, again, err, original)
		}
		stored, err := rp.Get(orderedCtx(), orderedTenant, original)
		if err != nil {
			t.Fatalf("reading back sequence %d from server %d: %v", original, i, err)
		}
		if string(stored.Value) != string(body) || stored.Headers[eventenvelope.HeaderName] != eventenvelope.HeaderValue ||
			stored.Headers[natsMsgID] != id {
			t.Fatalf("the stored message is not the original: headers %v, %d bytes (want %d)", stored.Headers, len(stored.Value), len(body))
		}
		if _, err := rp.Get(orderedCtx(), "other-tenant", original); err == nil {
			t.Fatal("a message on another tenant's subject was returned")
		}
	}

	// Nothing but the original is stored, and a different id is new.
	info, err := setup.js.StreamInfo(stream)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.LastSeq != original {
		t.Fatalf("the stream's last sequence is %d after the duplicates, want the original %d", info.State.LastSeq, original)
	}
	msg.DedupID = "G:acme:7:1"
	next := publish(newOrdered(t, setup, 8))
	if next.err != nil || next.ack.Duplicate || next.ack.Sequence <= original {
		t.Fatalf("a new round's publish: %+v, want stored as new after %d", next, original)
	}
}

// natsMsgID is the dedup header's name (nats.MsgIdHdr), spelled out so the assertion does not
// share a constant with the code it checks.
const natsMsgID = "Nats-Msg-Id"
