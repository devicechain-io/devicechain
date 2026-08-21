// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"encoding/json"
	"strings"
	"testing"
)

func strptr(s string) *string { return &s }

// 🔴 THE TWO REGRESSIONS THIS FILE EXISTS FOR, IN ORDER. First, JSONInputOf's predecessor
// guarded with the error from json.RawMessage.UnmarshalJSON, which copies its input and
// returns an error only on a nil receiver — never for malformed JSON. The check READ like
// validation, could not fire, and every invalid value became a JSON column write Postgres
// rejected at execution time (SQLSTATE 22P02). Second, swapping in json.Valid stopped the
// doomed write and replaced it with something worse: returning nil does not mean "reject",
// it means "write NULL", so an update carrying one bad field ERASED that column and
// answered 200. Restoring either shape must fail these tests.
func TestJSONInputOfRefusesInvalidJSON(t *testing.T) {
	for _, invalid := range []string{
		"acknowledged by livedevice", // the value that actually stranded a command in SENT
		"not json at all",
		"{unclosed",
		`{"a":1}trailing`,
	} {
		got, err := JSONInputOf("metadata", strptr(invalid))
		if err == nil {
			t.Errorf("JSONInputOf(%q) returned no error; a malformed value must be REFUSED, "+
				"not silently turned into a NULL that erases the stored value", invalid)
		}
		if got != nil {
			t.Errorf("JSONInputOf(%q) = %s, want nil alongside the error", invalid, string(*got))
		}
	}
}

// The error has to name the field, because one request can carry several JSON-typed fields —
// metadata, payload, config, parameterSchema, enum, recipients — and "invalid JSON" with no
// field name leaves the caller guessing which of them to fix.
func TestJSONInputOfNamesTheField(t *testing.T) {
	_, err := JSONInputOf("parameterSchema", strptr("nope"))
	if err == nil {
		t.Fatal("JSONInputOf returned no error for a malformed value")
	}
	if !strings.Contains(err.Error(), "parameterSchema") {
		t.Errorf("error %q does not name the offending field", err)
	}
}

// The counterweight. A guard that refused everything would satisfy the tests above while
// making every JSON field unwritable, so valid JSON must still pass through byte-for-byte.
func TestJSONInputOfKeepsValidJSON(t *testing.T) {
	for _, valid := range []string{
		`{"a":1}`,
		`[1,2,3]`,
		`"a bare json string"`,
		`null`,
		`123`,
	} {
		got, err := JSONInputOf("metadata", strptr(valid))
		if err != nil {
			t.Fatalf("JSONInputOf(%q) errored: %v", valid, err)
		}
		if got == nil {
			t.Fatalf("JSONInputOf(%q) = nil, want it preserved", valid)
		}
		if string(*got) != valid {
			t.Errorf("JSONInputOf(%q) = %q, want it unchanged", valid, string(*got))
		}
	}
}

func TestJSONInputOfNilIsNil(t *testing.T) {
	got, err := JSONInputOf("metadata", nil)
	if err != nil || got != nil {
		t.Errorf("JSONInputOf(nil) = (%v, %v), want (nil, nil)", got, err)
	}
}

// 🔴 EMPTY IS "NO VALUE", NOT "MALFORMED", AND THE DISTINCTION IS DELIBERATE. An empty or
// whitespace string is not valid JSON, so the obvious reading of this change would start
// refusing it — and that would be a SECOND behaviour change riding along with the fix,
// hitting callers who were never doing anything wrong. Clearing a field by sending "" has
// always worked and still does; refusal is reserved for a value that was meant to be JSON
// and is not. This is the same reading NullStrOf gives an empty string.
func TestJSONInputOfTreatsEmptyAsNoValue(t *testing.T) {
	for _, empty := range []string{"", "   ", "\t\n"} {
		got, err := JSONInputOf("metadata", strptr(empty))
		if err != nil {
			t.Errorf("JSONInputOf(%q) errored: %v — an empty value clears the column, it is not a malformed document", empty, err)
		}
		if got != nil {
			t.Errorf("JSONInputOf(%q) = %s, want nil", empty, string(*got))
		}
	}
}

// JSONTextOf takes a DEVICE-supplied value, whose wire contract is a plain string. Valid JSON is
// preserved as-is; anything else is encoded rather than dropped, because a device answering
// "bucket raised" is answering correctly and losing that text loses what the device said.
func TestJSONTextOfPreservesValidJSON(t *testing.T) {
	for _, valid := range []string{`{"ok":true}`, `[1,2]`, `"already a json string"`} {
		got := JSONTextOf(strptr(valid))
		if got == nil || string(*got) != valid {
			t.Errorf("JSONTextOf(%q) = %v, want it unchanged", valid, got)
		}
	}
}

func TestJSONTextOfEncodesPlainText(t *testing.T) {
	got := JSONTextOf(strptr("acknowledged by livedevice"))
	if got == nil {
		t.Fatal("JSONTextOf dropped a device's plain-text answer")
	}
	if !json.Valid(*got) {
		t.Fatalf("JSONTextOf produced invalid JSON %q — the very thing Postgres rejects", string(*got))
	}
	if string(*got) != `"acknowledged by livedevice"` {
		t.Errorf("JSONTextOf = %s, want the text encoded as a JSON string", string(*got))
	}

	// Round-trips: the encoding must be readable back as exactly what the device sent.
	var back string
	if err := json.Unmarshal(*got, &back); err != nil {
		t.Fatalf("unmarshalling the stored value: %v", err)
	}
	if back != "acknowledged by livedevice" {
		t.Errorf("round trip = %q, want the original text", back)
	}
}

// Whatever a device sends, the result must be storable in a JSON column — that is the whole
// point, and it is asserted over the shapes a device plausibly produces rather than one example.
func TestJSONTextOfAlwaysProducesValidJSON(t *testing.T) {
	for _, any := range []string{
		"", "plain", "{unclosed", `{"a":1}`, "with \"quotes\" and \\backslashes", "múltibyte ✓", "\x00\x01",
	} {
		got := JSONTextOf(strptr(any))
		if got == nil {
			t.Errorf("JSONTextOf(%q) = nil; a device value must always be storable", any)
			continue
		}
		if !json.Valid(*got) {
			t.Errorf("JSONTextOf(%q) = %q, which is not valid JSON", any, string(*got))
		}
	}
}

func TestJSONTextOfNilIsNil(t *testing.T) {
	if got := JSONTextOf(nil); got != nil {
		t.Errorf("JSONTextOf(nil) = %v, want nil", got)
	}
}
