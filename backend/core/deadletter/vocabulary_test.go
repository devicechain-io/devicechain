// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 🔴 THE TABLE IS THE POINT, AND IT IS HAND-WRITTEN ON PURPOSE. WorkWasAttempted decides
// whether a consumer may SETTLE state — mark a command lost, an alarm undelivered — so a
// reason nobody classified must not acquire an answer by default. Go cannot enumerate a
// const group, so the only way to assert "every declared Reason has a deliberate answer"
// is to declare them here, against their VALUES: a table derived from the switch would
// agree with the switch no matter what the switch said.
//
// Adding a Reason to the const block therefore has two visible consequences rather than
// none. It is missing from this table, which the completeness check below fails on; and
// until it is added to Reason.Valid it is refused by Envelope.Validate at the producer,
// so it cannot reach a consumer unclassified in the first place.
func TestWorkWasAttemptedClassifiesEveryDeclaredReason(t *testing.T) {
	classified := map[Reason]bool{
		// The write was attempted to the redelivery cap and never landed: the work is
		// genuinely gone, and downstream state must stop reading as in flight.
		ReasonExhausted: true,
		// Accepted, then found to be something this consumer can never complete. The
		// producer declined before attempting, so nothing was lost.
		ReasonUnprocessable: false,
		// Refused on a governed ceiling. Never attempted, and would succeed if sent again.
		ReasonShed: false,
	}

	for r, want := range classified {
		if got := r.WorkWasAttempted(); got != want {
			t.Errorf("reason %q: WorkWasAttempted() = %v, want %v — a consumer that settles "+
				"state on a dead letter acts on exactly this answer", r, got, want)
		}
		if !r.Valid() {
			t.Errorf("reason %q is declared but Reason.Valid rejects it, so Envelope.Validate "+
				"would refuse a letter every producer is entitled to write", r)
		}
	}

	// The completeness check. It is written against the count so that adding a fourth
	// reason to the const block without classifying it here fails rather than passes
	// quietly with three of four covered.
	if len(classified) != 3 {
		t.Fatalf("this table classifies %d reasons; a reason was added or removed without "+
			"deciding whether it settles state", len(classified))
	}
}

// The weakest possible mutant of the positive list is `default: return true`, which is
// exactly the skip-list shape the doc comment forbids. This is what kills it: a reason
// outside the declared vocabulary must answer false, because a consumer reading true would
// settle a command on a letter nobody has said describes lost work.
func TestAnUnclassifiedReasonNeverSettlesState(t *testing.T) {
	for _, r := range []Reason{
		Reason("a-reason-a-later-build-added"),
		Reason("exhaused"), // a typo of the one reason that does settle state
		Reason(""),
	} {
		if r.WorkWasAttempted() {
			t.Errorf("reason %q reports that work was attempted, so a consumer would settle "+
				"state on a letter nobody classified; the list must be positive", r)
		}
	}
}

// Kinds() is what dcctl's `--kind` help offers, and Validate is what decides whether a
// letter carrying one of those values can be written at all. If they disagree, an operator
// is offered a filter for a kind no producer is allowed to emit, or a producer emits a kind
// the filter never offers.
func TestEveryOfferedKindIsValid(t *testing.T) {
	for _, k := range Kinds() {
		if !Kind(k).Valid() {
			t.Errorf("kind %q is offered by Kinds() but rejected by Kind.Valid, so dcctl "+
				"offers a filter value Envelope.Validate refuses to write", k)
		}
	}
}

// 🔴 THE VOCABULARY IS ENFORCED ON THE WRITE PATH. Kind and Reason are documented as closed
// sets because they are metric labels and query filters, and a non-empty check enforces
// neither: a typo'd kind marshals, lands in an indexed column, and is then invisible to
// every reader that offers the declared set — which is worse than no record at all, because
// the operator is shown a list that looks complete.
func TestAnUndeclaredKindOrReasonIsRefusedBeforeItIsWritten(t *testing.T) {
	for name, mutate := range map[string]func(*Envelope){
		"a typo'd kind":    func(e *Envelope) { e.Kind = Kind("conector-dispatch") },
		"an invented kind": func(e *Envelope) { e.Kind = Kind("device-registration") },
		"a typo'd reason":  func(e *Envelope) { e.Reason = Reason("exhaused") },
		"an invented reason": func(e *Envelope) {
			e.Reason = Reason("a-reason-a-later-build-added")
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := good()
			mutate(&e)

			err := e.Validate()
			if err == nil {
				t.Fatalf("an envelope carrying %s was accepted", name)
			}
			if !strings.Contains(err.Error(), "undeclared") {
				t.Fatalf("the refusal does not say the value is undeclared, so the writer "+
					"cannot tell it apart from a missing field: %v", err)
			}
			if _, err := Marshal(e); err == nil {
				t.Fatalf("Marshal rendered an envelope carrying %s", name)
			}
			w := &fakeWriter{}
			if err := NewSink(w, nil).Write(context.Background(), e); err == nil {
				t.Fatalf("the sink wrote an envelope carrying %s", name)
			}
			if w.calls != 0 {
				t.Fatalf("the sink reached the broker with %s", name)
			}
		})
	}
}

// 🔴 THE COUNTERWEIGHT, AND IT IS LOAD-BEARING: Unmarshal still does not validate. A record
// already on the stream is evidence whatever its shape, and a build that refused to read a
// letter written by an older or newer producer would turn an unrecognised value into a
// consumer that cannot start — losing every letter behind it, not just the odd one. The
// enforcement belongs at the moment of writing, where there is still a producer to tell.
func TestUnmarshalStillReadsAnUndeclaredKindOrReason(t *testing.T) {
	// json.Marshal directly rather than Marshal: the point is a letter that is already on
	// the stream, written by a build whose vocabulary this one does not share.
	e := good()
	e.Kind = Kind("a-kind-a-later-build-added")
	e.Reason = Reason("a-reason-a-later-build-added")
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("building the on-stream letter: %v", err)
	}

	back, err := Unmarshal(body)
	if err != nil {
		t.Fatalf("Unmarshal refused a letter already on the stream, which would strand every "+
			"letter behind it rather than merely one it does not recognise: %v", err)
	}
	if back.Kind != Kind("a-kind-a-later-build-added") ||
		back.Reason != Reason("a-reason-a-later-build-added") {
		t.Fatalf("the unrecognised values did not survive the read: %+v", back)
	}
	// And the consumer's own defence still holds: the unrecognised reason settles nothing.
	if back.Reason.WorkWasAttempted() {
		t.Fatalf("an unrecognised reason read off the stream reports attempted work")
	}
}

// A well-formed letter is still written untouched. Without this, a Validate that refused
// everything would satisfy every case above.
func TestTheDeclaredVocabularyStillWrites(t *testing.T) {
	for _, k := range []Kind{
		KindDetectionAction, KindNotification, KindCommandResponse, KindConnectorDispatch,
	} {
		for _, r := range []Reason{ReasonExhausted, ReasonUnprocessable, ReasonShed} {
			e := Envelope{
				Kind: k, Reason: r, Source: "event-processing",
				Summary: "it did not happen", OccurredAt: time.Now().UTC(),
			}
			if err := e.Validate(); err != nil {
				t.Errorf("a letter carrying the declared %q/%q was refused: %v", k, r, err)
			}
		}
	}
}
