// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// newIndexingConsumer builds a consumer wired to both terminal writers, so a give-up can be observed
// on this service's own subject AND in the platform's dead-letter list.
func newIndexingConsumer(dead, index messaging.MessageWriter) *DispatchConsumer {
	e := NewExecutor(NewSecretResolver(&fakeSecretStore{}), nil, loopbackClient(), 5*time.Second)
	return NewDispatchConsumer(nil, &fakeReader{}, dead, index, e, nil, 5*time.Second, nil, 1, 1)
}

func indexEnvelope(t *testing.T, w *fakeWriter) deadletter.Envelope {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.messages) != 1 {
		t.Fatalf("index writer saw %d messages, want exactly 1", len(w.messages))
	}
	e, err := deadletter.Unmarshal(w.messages[0].Value)
	if err != nil {
		t.Fatalf("index entry is not a dead-letter envelope: %v", err)
	}
	return e
}

// 🔴 THE GAP: a dispatch written to connector-dispatch.dead was invisible to the operator surface,
// because that subject has no reader and no store. This asserts the give-up now also lands in the
// platform's one list.
func TestGiveUpIsIndexedOnThePlatformDeadLetterStream(t *testing.T) {
	dead, index := &fakeWriter{}, &fakeWriter{}
	c := newIndexingConsumer(dead, index)
	tctx := core.WithTenant(context.Background(), "tenant-a")
	msg := messaging.Message{
		Subject: messaging.ScopedSubject("inst", "tenant-a", "connector-dispatch"),
		Value:   []byte(`{"kind":"httpCall"}`), NumDelivered: messaging.MaxDeliver, StreamSeq: 41,
	}.WithCorrelationID("corr-9")

	c.deadLetter(tctx, msg, "rule-7", "httpCall", outcomeDead)

	dead.mu.Lock()
	verbatim := len(dead.messages)
	dead.mu.Unlock()
	if verbatim != 1 {
		t.Fatalf("the verbatim terminal write did not happen (%d messages); the index must never "+
			"displace it", verbatim)
	}
	e := indexEnvelope(t, index)
	if e.Kind != deadletter.KindConnectorDispatch {
		t.Fatalf("index kind = %q, want %q", e.Kind, deadletter.KindConnectorDispatch)
	}
	if e.Source != connectorsArea {
		t.Fatalf("index source = %q, want %q", e.Source, connectorsArea)
	}
	// 🔑 SUBJECT AND SEQUENCE ARE THE ORIGINAL'S COORDINATES ON THE SOURCE STREAM — the consumed
	// message's own — and NOT the copy's on connector-dispatch.dead, whose position nothing here
	// can know (the write returns no PubAck). Losing them turns the index entry into a note that
	// something failed with no way to find what.
	if e.Subject != msg.Subject || e.Sequence != msg.StreamSeq {
		t.Fatalf("index locates the original at %q/%d, want %q/%d",
			e.Subject, e.Sequence, msg.Subject, msg.StreamSeq)
	}
	// 🔴 AND THE JOIN ONTO THE DEAD SUBJECT IS THE CORRELATION, which is the only field the
	// verbatim copy and this record share. Without it the copy is unfindable: an operator reading
	// subject/sequence as the copy's address lands on a different message.
	if e.Correlation != "corr-9" {
		t.Fatalf("index correlation = %q, want %q — it is what joins this record to the verbatim "+
			"copy on the dead subject", e.Correlation, "corr-9")
	}
	// ...and the verbatim copy really does carry the same id, or the join has one end only.
	dead.mu.Lock()
	copied := dead.messages[0].CorrelationID()
	dead.mu.Unlock()
	if copied != "corr-9" {
		t.Fatalf("the verbatim copy carries correlation %q, want %q; without it the index entry "+
			"names a message that cannot be found on the dead subject", copied, "corr-9")
	}
	// 🔑 THE REFERENCE IDENTIFIES THE WORK WITHIN ITS KIND, which for a connector dispatch
	// is the rule that drove it — the thing an operator can act on. The action alone would
	// be a category rather than an identifier, and every give-up would look the same in
	// the list.
	if e.Reference != "rule-7" {
		t.Fatalf("index reference = %q, want the rule that drove the dispatch", e.Reference)
	}
	if len(e.Payload) != 0 {
		t.Fatal("the index entry carried the request body; the verbatim copy is already durable on " +
			"the connector-dispatch dead subject, and storing it twice doubles this path's retention")
	}
	// The tenant is what scopes the subject the index is written to; without it the real writer
	// refuses, fail-closed.
	index.mu.Lock()
	tenant := index.tenants[0]
	index.mu.Unlock()
	if tenant != "tenant-a" {
		t.Fatalf("index written under tenant %q, want tenant-a", tenant)
	}
}

// 🔑 A SHED DISPATCH IS NOT A FAILURE, and the reason vocabulary has to say so. Reported as
// "exhausted" it sends an operator to inspect a healthy destination; as "unprocessable" it calls
// replayable work poison.
func TestIndexReasonDistinguishesAShedFromAFailure(t *testing.T) {
	for outcome, want := range map[string]deadletter.Reason{
		outcomeDead:        deadletter.ReasonExhausted,
		outcomeRateLimited: deadletter.ReasonShed,
		outcomeUnsupported: deadletter.ReasonUnprocessable,
		outcomeInvalid:     deadletter.ReasonUnprocessable,
	} {
		if got := indexReasonFor(outcome); got != want {
			t.Errorf("indexReasonFor(%q) = %q, want %q", outcome, got, want)
		}
	}
	// An outcome nobody has classified reads as the vocabulary's own "says nothing about the cause"
	// value rather than as a claim.
	if got := indexReasonFor("something-added-later"); got != deadletter.ReasonExhausted {
		t.Errorf("an unclassified outcome mapped to %q, want %q", got, deadletter.ReasonExhausted)
	}
}

// 🔴 THE DETAIL MUST NOT CARRY THE ENDPOINT'S OWN ERROR TEXT. A send failure's message names the
// status and address of a destination the tenant chose, and this record is read across tenants —
// which is the status-class oracle the egress boundary closed.
func TestIndexDetailIsTheBoundedOutcomeLabel(t *testing.T) {
	dead, index := &fakeWriter{}, &fakeWriter{}
	c := newIndexingConsumer(dead, index)
	tctx := core.WithTenant(context.Background(), "tenant-a")

	c.deadLetter(tctx, messaging.Message{
		Subject: messaging.ScopedSubject("inst", "tenant-a", "connector-dispatch"),
		Value:   []byte(`{}`), NumDelivered: messaging.MaxDeliver,
	}, "rule-7", "httpCall", outcomeRateLimited)

	// Both halves are bounded enums, so neither can carry an endpoint's own error text.
	if e := indexEnvelope(t, index); e.Detail != "httpCall/"+outcomeRateLimited {
		t.Fatalf("index detail = %q, want the bounded action/outcome labels", e.Detail)
	}
}

// 🔑 THE INDEX IS BEST-EFFORT, AND THAT MUST NOT COST THE TERMINAL RECORD. The authoritative copy is
// already durable when the index is attempted, so an index failure loses the operator's VIEW of a
// give-up, not the give-up. Blocking the ack on it would trade a durable terminal record for a
// redelivery of a dispatch already abandoned — a duplicate outbound call bought with a log line.
func TestAFailedIndexWriteStillAcksTheTerminalMessage(t *testing.T) {
	dead := &fakeWriter{}
	index := &fakeWriter{fail: context.DeadlineExceeded}
	c := newIndexingConsumer(dead, index)
	tctx := core.WithTenant(context.Background(), "tenant-a")

	acked := &countingAcker{}
	msg := messaging.NewConsumedMessage(
		messaging.ScopedSubject("inst", "tenant-a", "connector-dispatch"),
		[]byte(`{}`), messaging.MaxDeliver, nil, acked)

	c.deadLetter(tctx, msg, "rule-7", "httpCall", outcomeDead)

	if acked.n != 1 {
		t.Fatalf("acks = %d, want 1; a failed index write must not strand a dispatch that was "+
			"already durably dead-lettered", acked.n)
	}
}

type countingAcker struct{ n int }

func (a *countingAcker) Ack() error { a.n++; return nil }

// orderRecordingAcker is a countingAcker that also records how much the index writer had
// taken AT THE MOMENT OF THE ACK, which is the only way to observe an ordering from the
// outside: both writes happen, so counting them afterwards cannot tell which came first.
type orderRecordingAcker struct {
	index        *fakeWriter
	n            int
	indexedAtAck int
}

func (a *orderRecordingAcker) Ack() error {
	a.n++
	a.index.mu.Lock()
	a.indexedAtAck = len(a.index.messages)
	a.index.mu.Unlock()
	return nil
}

// 🔴 THE ACK COMES BEFORE THE INDEX, AND UNTIL NOW ONLY A COMMENT SAID SO. The ordering is
// deliberate: the index is best-effort and its sink retries on its own deadline, so running
// it FIRST would put a bounded-but-slow broker write between "durably dead-lettered" and
// "stops redelivering" — and a pod dying in that window redelivers a dispatch that was
// already terminal, which for an outbound connector is a duplicate call to a tenant's
// endpoint. Swapping the two lines is a one-character-looking edit that no other assertion
// in this package can see, because both writes still happen and both counts still match.
func TestTheGiveUpIsAckedBeforeTheIndexIsWritten(t *testing.T) {
	dead, index := &fakeWriter{}, &fakeWriter{}
	c := newIndexingConsumer(dead, index)
	acker := &orderRecordingAcker{index: index}
	msg := messaging.NewConsumedMessage(
		messaging.ScopedSubject("inst", "tenant-a", "connector-dispatch"),
		[]byte(`{}`), messaging.MaxDeliver, nil, acker)

	c.deadLetter(core.WithTenant(context.Background(), "tenant-a"), msg, "rule-7", "httpCall", outcomeDead)

	if acker.n != 1 {
		t.Fatalf("acks = %d, want 1; with no ack this proves nothing about what precedes one", acker.n)
	}
	if acker.indexedAtAck != 0 {
		t.Errorf("the index writer had already taken %d message(s) when the ack ran; the index "+
			"must follow the ack, or a slow index write holds a terminal dispatch open for "+
			"redelivery", acker.indexedAtAck)
	}
	// The counterweight: "nothing indexed before the ack" is also true of an index that
	// never runs, which would pass the assertion above while losing the operator's view.
	index.mu.Lock()
	defer index.mu.Unlock()
	if len(index.messages) != 1 {
		t.Fatalf("index writes = %d, want 1; ordering is only worth asserting about a write "+
			"that happens", len(index.messages))
	}
}

// An unconfigured index writer must not panic the worker. It is reachable only from a test-built
// consumer today, but the nil check is what keeps that true rather than a comment saying so.
func TestIndexIsANoOpWhenUnconfigured(t *testing.T) {
	dead := &fakeWriter{}
	c := newTestConsumer(dead, &fakeSecretStore{})
	c.deadLetter(core.WithTenant(context.Background(), "tenant-a"), messaging.Message{
		Subject: messaging.ScopedSubject("inst", "tenant-a", "connector-dispatch"),
		Value:   []byte(`{}`), NumDelivered: messaging.MaxDeliver,
	}, "rule-7", "httpCall", outcomeDead)

	dead.mu.Lock()
	defer dead.mu.Unlock()
	if len(dead.messages) != 1 {
		t.Fatalf("the verbatim terminal write did not happen (%d messages)", len(dead.messages))
	}
}
