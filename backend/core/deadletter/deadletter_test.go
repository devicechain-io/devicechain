// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// fakeWriter fails a set number of times before succeeding, so the sink's own retry can
// be observed rather than described.
type fakeWriter struct {
	failures int
	calls    int
	got      []messaging.Message
	err      error
}

func (w *fakeWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.calls++
	if w.calls <= w.failures {
		return w.err
	}
	w.got = append(w.got, msgs...)
	return nil
}

func good() Envelope {
	return Envelope{
		Kind: KindNotification, Reason: ReasonExhausted, Source: "notification-management",
		Summary: "an alarm reached nobody", OccurredAt: time.Now().UTC(),
	}
}

// 🔴 A LETTER IS WRITTEN AT THE MOMENT SOMETHING HAS ALREADY GONE WRONG, by a caller about
// to ack and forget. A field left empty here is a record nobody can act on, found weeks
// later by the person it existed for — so the refusal is on the WRITE path.
func TestAnUnusableLetterIsRefusedBeforeItIsWritten(t *testing.T) {
	for name, mutate := range map[string]func(*Envelope){
		"no kind":     func(e *Envelope) { e.Kind = "" },
		"no reason":   func(e *Envelope) { e.Reason = "" },
		"no source":   func(e *Envelope) { e.Source = "   " },
		"no summary":  func(e *Envelope) { e.Summary = "" },
		"no occurred": func(e *Envelope) { e.OccurredAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			e := good()
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Fatalf("an envelope with %s was accepted", name)
			}
			w := &fakeWriter{}
			if err := NewSink(w, nil).Write(context.Background(), e); err == nil {
				t.Fatalf("the sink wrote an envelope with %s", name)
			}
			if w.calls != 0 {
				t.Fatalf("the sink reached the broker with an unusable envelope")
			}
		})
	}
}

// The counterweight: a complete envelope is written. Without it, a Validate that refused
// everything would satisfy every case above.
func TestACompleteLetterIsWritten(t *testing.T) {
	w := &fakeWriter{}
	if err := NewSink(w, nil).Write(context.Background(), good()); err != nil {
		t.Fatalf("a complete envelope was refused: %v", err)
	}
	if len(w.got) != 1 {
		t.Fatalf("the sink wrote %d messages, want 1", len(w.got))
	}
	back, err := Unmarshal(w.got[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if back.Kind != KindNotification || back.Source != "notification-management" {
		t.Fatalf("the letter lost its identity in transit: %+v", back)
	}
}

// 🔴 THE RETRY IS THE ONLY CHANCE LEFT. Every caller writes on the FINAL delivery, so
// JetStream will not redeliver the original whatever the consumer does — a single failed
// attempt would silently lose the work.
func TestATransientWriteFailureIsRetried(t *testing.T) {
	w := &fakeWriter{failures: 2, err: errors.New("broker is away")}
	lost := 0
	sink := NewSink(w, func(error) { lost++ })

	if err := sink.Write(context.Background(), good()); err != nil {
		t.Fatalf("the sink gave up on a failure it should have ridden out: %v", err)
	}
	if w.calls != 3 {
		t.Fatalf("the sink made %d attempts, want 3", w.calls)
	}
	if lost != 0 {
		t.Fatalf("a recovered write was reported as a loss")
	}
}

// 🔴 AND A WRITE THAT NEVER SUCCEEDS IS A LOSS, reported as one. This is the only outcome
// on these paths where work is gone with no record anywhere, which is why the caller is
// told through a hook it can count rather than only through a returned error it might
// discard.
func TestAWriteThatNeverSucceedsIsReportedAsALoss(t *testing.T) {
	w := &fakeWriter{failures: 99, err: errors.New("broker is away")}
	lost := 0
	sink := NewSink(w, func(error) { lost++ })

	err := sink.Write(context.Background(), good())
	if err == nil {
		t.Fatal("a letter that was never written was reported as written")
	}
	if !strings.Contains(err.Error(), "LOST") {
		t.Fatalf("the error does not say the work is gone: %v", err)
	}
	if lost != 1 {
		t.Fatalf("the loss hook fired %d times, want 1", lost)
	}
	if w.calls != writeAttempts {
		t.Fatalf("the sink made %d attempts, want %d", w.calls, writeAttempts)
	}
}

// A letter abandoned at shutdown is as lost as one abandoned at the cap, and must be
// counted the same way — otherwise a rolling restart quietly erases work.
func TestAWriteAbandonedAtShutdownIsStillALoss(t *testing.T) {
	w := &fakeWriter{failures: 99, err: errors.New("broker is away")}
	lost := 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := NewSink(w, func(error) { lost++ }).Write(ctx, good()); err == nil {
		t.Fatal("a letter abandoned at shutdown was reported as written")
	}
	if lost != 1 {
		t.Fatalf("the loss hook fired %d times at shutdown, want 1", lost)
	}
}

// A letter already on the stream is evidence whatever its shape: reading one back must not
// apply the write-path rules, or a field added later turns an older record into a consumer
// that cannot start.
func TestAnIncompleteLetterStillReadsBack(t *testing.T) {
	if _, err := Unmarshal([]byte(`{"kind":"notification"}`)); err != nil {
		t.Fatalf("a letter written by an older build could not be read: %v", err)
	}
	if _, err := Unmarshal([]byte(`not json`)); err == nil {
		t.Fatal("unreadable bytes were accepted as a letter")
	}
}
