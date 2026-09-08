// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 🔴 THE COMPLETENESS CHECK READS THE CONST BLOCK, NOT A SECOND COPY OF IT. WorkWasAttempted
// decides whether a consumer may SETTLE state, so a Reason nobody classified must not acquire
// an answer by default — and a guard that compares a hand-written table against a hand-written
// count cannot see that happen: it fails only when someone edits the TABLE, which is precisely
// the edit a person who forgot about this file did not make. The declared set is therefore
// parsed out of deadletter.go, so the thing being counted is the thing that actually moves.
//
// The EXPECTATIONS stay hand-written, and that half is deliberate. A table derived from the
// switch would agree with the switch no matter what the switch said; what the scan supplies is
// the set that must be covered, not the answers.
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

	declared := declaredConstants(t, vocabularySource, "Reason")
	for _, value := range declared {
		r := Reason(value)
		want, covered := classified[r]
		if !covered {
			t.Errorf("reason %q is declared in %s and this test does not classify it: whether a "+
				"new way of giving up settles state has to be answered deliberately, and "+
				"WorkWasAttempted answers false for it by default", r, vocabularySource)
			continue
		}
		if got := r.WorkWasAttempted(); got != want {
			t.Errorf("reason %q: WorkWasAttempted() = %v, want %v — a consumer that settles "+
				"state on a dead letter acts on exactly this answer", r, got, want)
		}
		if !r.Valid() {
			t.Errorf("reason %q is declared but Reason.Valid rejects it, so Envelope.Validate "+
				"would refuse a letter every producer is entitled to write", r)
		}
	}

	// The other direction: a reason removed from the const block must not linger here, or the
	// table starts describing a vocabulary that no longer exists.
	for r := range classified {
		if !slices.Contains(declared, string(r)) {
			t.Errorf("this test classifies %q, which is no longer declared in %s", r, vocabularySource)
		}
	}
}

// Kinds() is what dcctl's `--kind` help offers and Kind.Valid is what Envelope.Validate
// enforces; both read allKinds, so both are wrong together the moment a Kind is declared and
// not registered there. Read from the const block for the same reason the Reason check is.
func TestEveryDeclaredKindIsOfferedAndValid(t *testing.T) {
	offered := Kinds()
	for _, value := range declaredConstants(t, vocabularySource, "Kind") {
		if !slices.Contains(offered, value) {
			t.Errorf("kind %q is declared in %s but not in allKinds, so dcctl's --kind help "+
				"tells an operator a real filter value does not exist", value, vocabularySource)
		}
		if !Kind(value).Valid() {
			t.Errorf("kind %q is declared in %s but Kind.Valid rejects it, so Envelope.Validate "+
				"refuses a letter its producer is entitled to write", value, vocabularySource)
		}
	}
}

// vocabularySource is the file the two vocabularies are declared in. Named once so a rename
// breaks the scan loudly at one place rather than silently narrowing it at several.
const vocabularySource = "deadletter.go"

// declaredConstants returns every string constant declared with the named type in path,
// including the untyped continuations of a typed const block — which is how Go lets a group
// share the type written once on its first spec.
//
// 🔴 A SCAN THAT FINDS NOTHING WOULD PASS EVERY CHECK ABOVE VACUOUSLY, so an empty result is
// a failure of the instrument, reported as one.
func declaredConstants(t *testing.T, path, typeName string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("cannot parse the %s vocabulary at %s: %v", typeName, path, err)
	}

	var values []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		// Within one const block the type carries forward from the last spec that named
		// one, so track it across specs rather than reading each in isolation.
		typed := false
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				id, ok := vs.Type.(*ast.Ident)
				typed = ok && id.Name == typeName
			}
			if !typed {
				continue
			}
			for _, value := range vs.Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unreadable %s literal %s in %s: %v", typeName, lit.Value, path, err)
				}
				values = append(values, unquoted)
			}
		}
	}
	if len(values) == 0 {
		t.Fatalf("found no %s constants in %s; the scan is broken, not the vocabulary",
			typeName, path)
	}
	return values
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
