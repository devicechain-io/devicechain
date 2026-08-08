// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"encoding/json"
	"testing"
)

func strptr(s string) *string { return &s }

// 🔴 THE REGRESSION THIS FILE EXISTS FOR. MetadataStrOf's guard checked the error from
// json.RawMessage.UnmarshalJSON, which copies its input and returns an error only on a nil
// receiver — never for malformed JSON. So the check READ like validation, could not fire, and
// every invalid value became a JSON column write that Postgres rejected at execution time
// (SQLSTATE 22P02). Restoring the dead guard must fail this test.
func TestMetadataStrOfRejectsInvalidJSON(t *testing.T) {
	for _, invalid := range []string{
		"acknowledged by livedevice", // the value that actually stranded a command in SENT
		"not json at all",
		"",
		"{unclosed",
		`{"a":1}trailing`,
	} {
		if got := MetadataStrOf(strptr(invalid)); got != nil {
			t.Errorf("MetadataStrOf(%q) = %s, want nil: a non-JSON value must never reach a JSON column",
				invalid, string(*got))
		}
	}
}

// The counterweight. A guard that rejected everything would satisfy the test above while making
// every metadata field unwritable, so valid JSON must still pass through byte-for-byte.
func TestMetadataStrOfKeepsValidJSON(t *testing.T) {
	for _, valid := range []string{
		`{"a":1}`,
		`[1,2,3]`,
		`"a bare json string"`,
		`null`,
		`123`,
	} {
		got := MetadataStrOf(strptr(valid))
		if got == nil {
			t.Fatalf("MetadataStrOf(%q) = nil, want it preserved", valid)
		}
		if string(*got) != valid {
			t.Errorf("MetadataStrOf(%q) = %q, want it unchanged", valid, string(*got))
		}
	}
}

func TestMetadataStrOfNilIsNil(t *testing.T) {
	if got := MetadataStrOf(nil); got != nil {
		t.Errorf("MetadataStrOf(nil) = %v, want nil", got)
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
