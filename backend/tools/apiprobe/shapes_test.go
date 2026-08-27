// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// raw is a JSON literal written the way a server would send it.
func raw(s string) json.RawMessage { return json.RawMessage(s) }

// The five non-zero codes are what a rig branches on, so two of them colliding
// would make one control silently assert the other's outcome — and the control
// would still pass, because the number it compares against would still match.
func TestEveryExitCodeIsDistinct(t *testing.T) {
	named := map[string]int{
		"OK": exitOK, "SETUP": exitSetup, "MISSING": exitMissing,
		"MISMATCH": exitMismatch, "REFUSED": exitRefused, "SHAPE": exitShape,
	}
	seen := map[int]string{}
	for name, code := range named {
		if other, clash := seen[code]; clash {
			t.Errorf("%s and %s are both %d", name, other, code)
		}
		seen[code] = name
	}
}

// An unclassified failure must report INCONCLUSIVE, never a verdict about data.
func TestAnUnclassifiedErrorIsInconclusive(t *testing.T) {
	if got := codeOf(errPlain("something went sideways")); got != exitSetup {
		t.Errorf("an uncoded error resolved to %d, want exitSetup (%d)", got, exitSetup)
	}
}

type errPlain string

func (e errPlain) Error() string { return string(e) }

// ---- createdObjects ----------------------------------------------------

func TestAPlainCreateYieldsOneObject(t *testing.T) {
	e := entity{Name: "device"}
	got, err := createdObjects(e, raw(`{"token":"t"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d objects, want 1", len(got))
	}
}

// 🔑 A bulk create of N is N receipt rows. One row standing in for the batch
// would let the other N-1 vanish across an upgrade with nothing to notice.
func TestABulkCreateYieldsEveryObject(t *testing.T) {
	e := entity{Name: "devices-bulk", Bulk: true}
	got, err := createdObjects(e, raw(`[{"token":"a"},{"token":"b"},{"token":"c"}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d objects, want 3", len(got))
	}
}

func TestAnEmptyBulkCreateIsAShapeProblem(t *testing.T) {
	e := entity{Name: "devices-bulk", Bulk: true}
	_, err := createdObjects(e, raw(`[]`))
	assertCode(t, err, exitShape)
}

// A REFUSAL is a verdict about the API, not a shape problem: the request was
// understood and declined. Getting this wrong sends a reader hunting for a
// schema change that never happened.
func TestARefusedEnvelopeIsRefusedAndCarriesTheReason(t *testing.T) {
	e := entity{Name: "command", Wrap: "command", Reject: "rejection{code reason}"}
	_, err := createdObjects(e, raw(`{"command":null,"rejection":{"code":"COMMAND_NOT_IN_VOCABULARY","reason":"no such command"}}`))
	assertCode(t, err, exitRefused)
	// The code is what a person acts on. A refusal reported without it is a
	// dead end at the end of a drill.
	if !strings.Contains(err.Error(), "COMMAND_NOT_IN_VOCABULARY") {
		t.Errorf("the refusal did not carry its code: %v", err)
	}
}

func TestAnAcceptedEnvelopeYieldsTheInnerObject(t *testing.T) {
	e := entity{Name: "command", Wrap: "command", Reject: "rejection{code reason}"}
	got, err := createdObjects(e, raw(`{"command":{"token":"c1"},"rejection":null}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || !strings.Contains(string(got[0]), `"c1"`) {
		t.Fatalf("got %q, want the wrapped command", got)
	}
}

func TestAnEnvelopeMissingItsKeyIsAShapeProblem(t *testing.T) {
	e := entity{Name: "command", Wrap: "command", Reject: "rejection{code reason}"}
	_, err := createdObjects(e, raw(`{"rejection":null}`))
	assertCode(t, err, exitShape)
}

// ---- readObject --------------------------------------------------------

func TestAnEmptyListReadsAsMissing(t *testing.T) {
	e := entity{Name: "device", Read: "devicesByToken"}
	_, err := readObject(e, map[string]json.RawMessage{"devicesByToken": raw(`[]`)}, "apiprobe-device", nil)
	assertCode(t, err, exitMissing)
	// The token is what an operator greps the database for.
	if !strings.Contains(err.Error(), "apiprobe-device") {
		t.Errorf("the finding did not name the token: %v", err)
	}
}

// 🔴 The counterweight. A read-back that finds the row must NOT report missing,
// or the whole taxonomy collapses into "always fails" — which passes every
// negative control and no positive one.
func TestAPopulatedListReadsBack(t *testing.T) {
	e := entity{Name: "device", Read: "devicesByToken"}
	got, err := readObject(e, map[string]json.RawMessage{"devicesByToken": raw(`[{"token":"t"}]`)}, "t", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), `"t"`) {
		t.Fatalf("got %q, want the first list element", got)
	}
}

// The single-object lookups (dashboard, connector) reach MISSING by a different
// route — a null rather than an empty list — and an earlier draft of this tool
// assumed the list form was universal.
func TestANullSingleReadReadsAsMissing(t *testing.T) {
	e := entity{Name: "dashboard", Read: "dashboard", Single: true}
	_, err := readObject(e, map[string]json.RawMessage{"dashboard": raw(`null`)}, "apiprobe-dashboard", nil)
	assertCode(t, err, exitMissing)
}

func TestAPresentSingleReadReadsBack(t *testing.T) {
	e := entity{Name: "dashboard", Read: "dashboard", Single: true}
	got, err := readObject(e, map[string]json.RawMessage{"dashboard": raw(`{"token":"d"}`)}, "d", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), `"d"`) {
		t.Fatalf("got %q, want the object", got)
	}
}

// A list that became an object is the SCHEMA moving, not the data going missing.
func TestAListThatIsNoLongerAListIsAShapeProblem(t *testing.T) {
	e := entity{Name: "device", Read: "devicesByToken"}
	_, err := readObject(e, map[string]json.RawMessage{"devicesByToken": raw(`{"token":"t"}`)}, "t", nil)
	assertCode(t, err, exitShape)
}

func TestAnAbsentReadKeyIsAShapeProblem(t *testing.T) {
	e := entity{Name: "device", Read: "devicesByToken"}
	_, err := readObject(e, map[string]json.RawMessage{"somethingElse": raw(`[]`)}, "t", nil)
	assertCode(t, err, exitShape)
}

func assertCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with code %d, got none", want)
	}
	if got := codeOf(err); got != want {
		t.Fatalf("got exit code %d, want %d (%v)", got, want, err)
	}
}
