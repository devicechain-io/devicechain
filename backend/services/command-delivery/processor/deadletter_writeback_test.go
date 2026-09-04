// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// dispositionRecorder records what the write-back asked for and answers with whatever the
// test staged. It is the whole downstream: the disposition logic is what is under test,
// not the SQL, which model's own tests pin.
type dispositionRecorder struct {
	calls   []string
	settled bool
	err     error
}

func (r *dispositionRecorder) MarkResponseLost(_ context.Context, token string) (bool, error) {
	r.calls = append(r.calls, token)
	return r.settled, r.err
}

// countingAck records whether the message was acked, which is the OTHER half of every
// disposition here: "leave it unacked" is how this consumer buys another attempt, and a
// test that only checked the write would not tell it apart from "gave up".
type countingAck struct{ acks int }

func (c *countingAck) Ack() error { c.acks++; return nil }

// idleReader satisfies the reader dependency for a component whose loop the test never
// starts. Handle is exercised directly, so nothing is ever read.
type idleReader struct{}

func (idleReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}
func (idleReader) HandleResponse(error) {}

// areaSeq keeps every constructed write-back on its own metrics subsystem. promauto
// registers globally and PANICS on a duplicate, so two components built with the same
// functional area in one test binary would take the whole package down.
var areaSeq int

func newTestWriteback(t *testing.T, api CommandDispositionWriter) *DeadLetterWriteback {
	t.Helper()
	areaSeq++
	ms := &core.Microservice{FunctionalArea: fmt.Sprintf("cdwriteback%d", areaSeq)}
	w, err := NewDeadLetterWriteback(ms, idleReader{}, api, core.NewNoOpLifecycleCallbacks())
	if err != nil {
		t.Fatalf("NewDeadLetterWriteback: %v", err)
	}
	if err := w.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	return w
}

// letter builds a consumed dead-letter message on a tenant-scoped subject.
func letter(t *testing.T, tenant string, e deadletter.Envelope, delivered int, ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	body, err := deadletter.Marshal(e)
	if err != nil {
		t.Fatalf("marshaling the envelope: %v", err)
	}
	subject := messaging.ScopedSubject("inst", tenant, "dead-letters")
	return messaging.NewConsumedMessage(subject, body, delivered, nil, ack)
}

// responseLetter is the envelope the command-delivery arm actually writes.
func responseLetter(command string) deadletter.Envelope {
	return deadletter.Envelope{
		Kind:       deadletter.KindCommandResponse,
		Reason:     deadletter.ReasonExhausted,
		Source:     "command-delivery",
		Summary:    "a device answered a command and the answer could not be recorded",
		Reference:  command,
		OccurredAt: time.Now().UTC(),
	}
}

func TestWritebackSettlesTheCommandTheLetterNames(t *testing.T) {
	api := &dispositionRecorder{settled: true}
	w := newTestWriteback(t, api)
	ack := &countingAck{}

	w.Handle(letter(t, "tenant-a", responseLetter("cmd-9"), 5, ack))

	if len(api.calls) != 1 || api.calls[0] != "cmd-9" {
		t.Fatalf("write-back settled %v, want [cmd-9]", api.calls)
	}
	if ack.acks != 1 {
		t.Fatalf("acks = %d, want 1; a settled letter must not stay on the stream", ack.acks)
	}
}

// 🔑 THE TENANT COMES FROM THE SUBJECT, AND THE WRITE IS REFUSED WITHOUT IT. The commands
// table is tenant-scoped, so the disposition write needs a tenant in context or the scope
// callback rejects it. This asserts the context handed downstream actually carries one —
// the assertion that stops a refactor from passing the bare process context through and
// turning every write-back into a fail-closed error.
func TestWritebackPassesTheSubjectTenantDownstream(t *testing.T) {
	var seen string
	var found bool
	api := &tenantCapturingWriter{onCall: func(ctx context.Context) { seen, found = core.TenantFromContext(ctx) }}
	w := newTestWriteback(t, api)

	w.Handle(letter(t, "tenant-b", responseLetter("cmd-1"), 1, &countingAck{}))

	if !found || seen != "tenant-b" {
		t.Fatalf("downstream context carried tenant %q (present=%v), want tenant-b", seen, found)
	}
}

type tenantCapturingWriter struct{ onCall func(context.Context) }

func (w *tenantCapturingWriter) MarkResponseLost(ctx context.Context, _ string) (bool, error) {
	w.onCall(ctx)
	return true, nil
}

// Every producer shares one dead-letter stream, so most of what this durable receives is
// somebody else's. Those are acked and ignored — not retried, and not written.
func TestWritebackIgnoresOtherKindsOfDeadLetter(t *testing.T) {
	api := &dispositionRecorder{settled: true}
	w := newTestWriteback(t, api)
	ack := &countingAck{}

	e := responseLetter("cmd-1")
	e.Kind = deadletter.KindNotification
	w.Handle(letter(t, "tenant-a", e, 1, ack))

	if len(api.calls) != 0 {
		t.Fatalf("write-back acted on a %s letter: %v", deadletter.KindNotification, api.calls)
	}
	if ack.acks != 1 {
		t.Fatalf("acks = %d, want 1; a letter for another kind must not hold this durable", ack.acks)
	}
}

// 🔴 THE FILTER IS ON KIND, NOT SOURCE. A letter written by a build that recorded a
// different source — the work having moved service, or the area having been renamed —
// still describes a command whose response was lost, and must still settle it. Matching
// on the area name would reinstate the original defect silently.
func TestWritebackSettlesRegardlessOfTheRecordedSource(t *testing.T) {
	api := &dispositionRecorder{settled: true}
	w := newTestWriteback(t, api)

	e := responseLetter("cmd-2")
	e.Source = "some-other-area"
	w.Handle(letter(t, "tenant-a", e, 1, &countingAck{}))

	if len(api.calls) != 1 {
		t.Fatalf("write-back skipped a command-response letter whose source was %q", e.Source)
	}
}

func TestWritebackDropsWhatItCannotActOn(t *testing.T) {
	t.Run("no parseable tenant", func(t *testing.T) {
		api := &dispositionRecorder{settled: true}
		w := newTestWriteback(t, api)
		ack := &countingAck{}
		body, _ := deadletter.Marshal(responseLetter("cmd-1"))
		w.Handle(messaging.NewConsumedMessage("no-tenant-here", body, 1, nil, ack))
		if len(api.calls) != 0 || ack.acks != 1 {
			t.Fatalf("calls=%v acks=%d; an unattributable letter must be acked and not written",
				api.calls, ack.acks)
		}
	})
	t.Run("body is not an envelope", func(t *testing.T) {
		api := &dispositionRecorder{settled: true}
		w := newTestWriteback(t, api)
		ack := &countingAck{}
		subject := messaging.ScopedSubject("inst", "tenant-a", "dead-letters")
		w.Handle(messaging.NewConsumedMessage(subject, []byte("{not json"), 1, nil, ack))
		if len(api.calls) != 0 || ack.acks != 1 {
			t.Fatalf("calls=%v acks=%d; an undecodable letter must be acked and not written",
				api.calls, ack.acks)
		}
	})
	t.Run("names no command", func(t *testing.T) {
		api := &dispositionRecorder{settled: true}
		w := newTestWriteback(t, api)
		ack := &countingAck{}
		e := responseLetter("")
		w.Handle(letter(t, "tenant-a", e, 1, ack))
		if len(api.calls) != 0 || ack.acks != 1 {
			t.Fatalf("calls=%v acks=%d; a letter naming no command must be acked and not written",
				api.calls, ack.acks)
		}
	})
}

// Below the redelivery cap a store failure buys the next attempt, which is what leaving
// the message unacked means. Acking here would spend the whole budget on the first blip.
func TestWritebackLeavesAFailedWriteUnackedBelowTheCap(t *testing.T) {
	api := &dispositionRecorder{err: errors.New("database is away")}
	w := newTestWriteback(t, api)
	ack := &countingAck{}

	w.Handle(letter(t, "tenant-a", responseLetter("cmd-3"), messaging.MaxDeliver-1, ack))

	if ack.acks != 0 {
		t.Fatalf("acks = %d, want 0; acking below the cap discards the remaining attempts", ack.acks)
	}
}

// On the FINAL attempt no redelivery follows, so leaving it unacked would strand the
// letter rather than retry it. It is acked and counted as the loss it is.
func TestWritebackAcksAFailedWriteAtTheCap(t *testing.T) {
	api := &dispositionRecorder{err: errors.New("database is away")}
	w := newTestWriteback(t, api)
	ack := &countingAck{}

	w.Handle(letter(t, "tenant-a", responseLetter("cmd-4"), messaging.MaxDeliver, ack))

	if ack.acks != 1 {
		t.Fatalf("acks = %d, want 1; a letter past the cap left unacked is stranded, not retried",
			ack.acks)
	}
}

// 🔴 A PURGED TENANT IS TERMINAL ON THE FIRST LOOK. The fence refuses every further write
// for that tenant, so spending the cap would bury the letters behind it and then raise
// the stranded alert for a record that was deliberately destroyed.
func TestWritebackTreatsAPurgedTenantAsTerminal(t *testing.T) {
	api := &dispositionRecorder{err: fmt.Errorf("write refused: %w", rdb.ErrTenantPurged)}
	w := newTestWriteback(t, api)
	ack := &countingAck{}

	w.Handle(letter(t, "tenant-gone", responseLetter("cmd-5"), 1, ack))

	if ack.acks != 1 {
		t.Fatalf("acks = %d, want 1; a purged tenant's letter must not consume the "+
			"redelivery budget on a write that can never succeed", ack.acks)
	}
}

// A command the write cannot settle is a normal outcome, not a failure: the write reports
// no transition and the letter is done with. It covers BOTH misses of the predicate — a
// command already terminal some other way, and one that has gone back to being live — which
// is why the counter it feeds is named for the predicate rather than for finishing.
func TestWritebackAcksALetterWhoseCommandIsNotAnswerable(t *testing.T) {
	api := &dispositionRecorder{settled: false}
	w := newTestWriteback(t, api)
	ack := &countingAck{}

	w.Handle(letter(t, "tenant-a", responseLetter("cmd-6"), 1, ack))

	if ack.acks != 1 {
		t.Fatalf("acks = %d, want 1", ack.acks)
	}
}

// 🔴 THE CONSTRUCTOR REFUSES A NIL DEPENDENCY. A nil-tolerant handler is how a wiring
// mistake becomes "nothing has ever failed" — silently, forever, on the path whose entire
// job is to stop a record going quiet.
func TestWritebackRefusesToBeBuiltWithoutItsDependencies(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "cdwritebacknil"}
	if _, err := NewDeadLetterWriteback(ms, nil, &dispositionRecorder{}, core.NewNoOpLifecycleCallbacks()); err == nil {
		t.Fatal("built a write-back with no reader; it would read nothing and report success")
	}
	if _, err := NewDeadLetterWriteback(ms, idleReader{}, nil, core.NewNoOpLifecycleCallbacks()); err == nil {
		t.Fatal("built a write-back with no command API; it would read every letter and write nothing")
	}
}
