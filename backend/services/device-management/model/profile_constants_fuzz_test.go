// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"testing"
	"unicode/utf8"
)

// constantsOracle is what encoding/json says a metadata document holds under keys, written
// without readConstants' own helpers (constantOf in particular) so that it can disagree with
// them: "n:<bits>" for a JSON number that decodes to a float64, "x" for a key the object holds
// as anything else (null, a string, a boolean, a container, a number out of range). ok is false
// when the document is not a JSON object, which has no constants.
func constantsOracle(doc []byte, keys []string) (map[string]string, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(doc, &obj) != nil || obj == nil {
		return nil, false
	}
	out := map[string]string{}
	for _, k := range keys {
		raw, held := obj[k]
		if !held {
			continue
		}
		var f float64
		// A null decodes into a float64 as a no-op without an error, so it is told apart first.
		if string(raw) == "null" || json.Unmarshal(raw, &f) != nil {
			out[k] = "x"
			continue
		}
		out[k] = "n:" + strconv.FormatUint(math.Float64bits(f), 16)
	}
	return out, true
}

// scannedConstants is readConstants' answer in the oracle's terms.
func scannedConstants(doc []byte, keys []string) (map[string]string, bool) {
	got := readConstants(doc, keys)
	if got == nil {
		return nil, false
	}
	out := map[string]string{}
	for _, c := range got {
		if v, ok := c.Number(); ok {
			out[c.Key()] = "n:" + strconv.FormatUint(math.Float64bits(v), 16)
		} else {
			out[c.Key()] = "x"
		}
	}
	return out, true
}

// FuzzReadConstantsAgreesWithEncodingJSON holds the hand-written metadata scanner to
// encoding/json on every VALID UTF-8 JSON document the fuzzer can build: the same keys found,
// the same last-wins answer for a repeated key, the same number (to the bit, -0 included) or
// the same verdict that a value is not one. On a document that is not valid JSON it must not
// panic; which constants it then reports is not specified (the column refuses such a document).
func FuzzReadConstantsAgreesWithEncodingJSON(f *testing.F) {
	for _, seed := range []string{
		`{"cal": 1.5}`, `{"cal":1,"cal":2}`, `{"c\u0061l": 3}`, `{"cal": -0}`, `{"cal": -0.0e+0}`,
		`{"cal": 1e400}`, `{"cal": -1e400}`, `{"cal": 1e-400}`, `{"cal": 4.9e-324}`, `{"cal": 1.7976931348623157e308}`,
		`{"cal": 123456789012345678901234567890}`, `{"cal": 0.1E1}`, `{"cal": 01}`, `{"cal": +1}`, `{"cal": .5}`,
		`{"cal": 0x10}`, `{"cal": NaN}`, `{"cal": -Infinity}`, `{"cal": 1_0}`, `{"cal": 1.}`,
		"\ufeff{\"cal\": 1}", `{"cal": 1} trailing`, `{"cal": 1}}`, `{"a": [[[[[[[[[[[[{}]]]]]]]]]]]], "cal": 2}`,
		`{"a": "\"}", "cal": 5}`, `{"a\\": 1, "cal": 6}`, `{"\u00e9": 7}`, `{"é": 8, "\u00e9": 9}`,
		`{"\ud800": 1}`, `{"\uFFFD": 2}`, `{"a\"b": 3}`, `{"cal": null}`, `{"cal": true}`, `{"cal": "1"}`,
		`{"cal": {"cal": 1}}`, `{"cal": [1]}`, `{}`, `[]`, `null`, `1`, `{"cal" : 1 , "x" : 2 }`,
		"{\"cal\"\t:\r\n1}", `{"cal": 1,}`, `{"cal": 1 2}`, `{"cal": 1e}`, `{"cal": -}`, `{"cal": 1e+}`,
	} {
		f.Add([]byte(seed), "cal")
	}
	f.Fuzz(func(t *testing.T, doc []byte, extra string) {
		keys := []string{"cal", "a", "é", "\ufffd", "a\"b", "a\\"}
		if extra != "" && len(extra) <= 64 && !slices.Contains(keys, extra) {
			keys = append(keys, extra)
		}
		got, gotObj := scannedConstants(doc, keys) // must not panic, whatever doc is
		if !json.Valid(doc) || !utf8.Valid(doc) || !utf8.ValidString(extra) {
			return
		}
		want, wantObj := constantsOracle(doc, keys)
		if gotObj != wantObj {
			t.Fatalf("object-ness: scanner %v, encoding/json %v, doc %q", gotObj, wantObj, doc)
		}
		if len(got) != len(want) {
			t.Fatalf("keys differ: scanner %v, encoding/json %v, doc %q", got, want, doc)
		}
		for k, w := range want {
			if got[k] != w {
				t.Fatalf("key %q: scanner %q, encoding/json %q, doc %q", k, got[k], w, doc)
			}
		}
	})
}
