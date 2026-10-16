// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"testing"
)

// TestArgumentsDigestIsCanonical: two spellings of the same arguments share a digest, so
// the field correlates calls that asked for the same thing however the client wrote it.
func TestArgumentsDigestIsCanonical(t *testing.T) {
	a := argumentsDigest(json.RawMessage(`{ "pageSize": 7,  "deviceType": "truck" }`))
	b := argumentsDigest(json.RawMessage(`{"deviceType":"truck","pageSize":7}`))
	if a != b {
		t.Errorf("reordered/re-spaced arguments digest differently: %s vs %s", a, b)
	}
	if want := sha256Hex(`{"deviceType":"truck","pageSize":7}`); a != want {
		t.Errorf("digest = %s, want the SHA-256 of the compact sorted form %s", a, want)
	}
}

// TestArgumentsDigestSeparatesDifferentArguments is the counterweight: canonicalizing
// must not merge arguments that differ — including two integers float64 cannot tell
// apart, which a decode into float64 would collapse onto one digest.
func TestArgumentsDigestSeparatesDifferentArguments(t *testing.T) {
	cases := [][2]string{
		{`{"pageSize":7}`, `{"pageSize":8}`},
		{`{"n":9007199254740993}`, `{"n":9007199254740992}`},
		{`{"a":1}`, `not json`},
		// Trailing content after a valid value is not dropped from the digest.
		{`{"a":1}`, `{"a":1} {"b":2}`},
		// ...including a stray closing delimiter, which json.Decoder.More reports as "no
		// more values" and so cannot be the test of trailing content.
		{`{"a":1}`, `{"a":1}]`},
		{`{"a":1}`, `{"a":1}}`},
	}
	for _, c := range cases {
		if argumentsDigest(json.RawMessage(c[0])) == argumentsDigest(json.RawMessage(c[1])) {
			t.Errorf("%s and %s share a digest", c[0], c[1])
		}
	}
	// Absent arguments digest as the empty input, so the field is never blank.
	if got, want := argumentsDigest(nil), sha256Hex(""); got != want {
		t.Errorf("digest of absent arguments = %s, want %s", got, want)
	}
	// Arguments that are not JSON are digested exactly as sent.
	for _, raw := range []string{`not json`, `{"a":1}]`} {
		if got, want := argumentsDigest(json.RawMessage(raw)), sha256Hex(raw); got != want {
			t.Errorf("digest of non-JSON arguments %s = %s, want %s", raw, got, want)
		}
	}
	// Whitespace after the value is insignificant, so it does not change the digest.
	if got, want := argumentsDigest(json.RawMessage("{\"a\":1} \n")), sha256Hex(`{"a":1}`); got != want {
		t.Errorf("digest of arguments with trailing whitespace = %s, want %s", got, want)
	}
}
